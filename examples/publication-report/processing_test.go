package main

import (
	"context"
	"errors"
	sdk "github.com/open-agentic-catalog/open-agentic-catalog/sdk/go"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestProcessingFailureBoundaries(t *testing.T) {
	private := errors.New("PRIVATE_STORE_OR_TRANSPORT_CAUSE")
	for _, tc := range []struct {
		name                                                string
		reconcile, setup                                    bool
		nextErr, snapshotErr, lossErr, completeErr, failErr error
		want                                                string
		calls                                               []string
	}{
		{name: "empty inbox", nextErr: errEmpty, calls: []string{"next"}},
		{name: "inbox unavailable", nextErr: private, want: "inbox_read_failed", calls: []string{"next"}},
		{name: "setup completion unavailable", setup: true, completeErr: private, want: "receipt_commit_failed", calls: []string{"next", "complete"}},
		{name: "publication completion unavailable", completeErr: private, want: "report_commit_failed", calls: []string{"next", "snapshots", "complete"}},
		{name: "publication failure recorded", snapshotErr: private, want: "catalog_read_failed", calls: []string{"next", "snapshots", "fail"}},
		{name: "publication failure record unavailable", snapshotErr: private, failErr: private, want: "failure_record_failed", calls: []string{"next", "snapshots", "fail"}},
		{name: "reconcile publication failure recorded", reconcile: true, snapshotErr: private, want: "catalog_read_failed", calls: []string{"snapshots", "fail"}},
		{name: "reconcile publication failure record unavailable", reconcile: true, snapshotErr: private, failErr: private, want: "failure_record_failed", calls: []string{"snapshots", "fail"}},
		{name: "reconcile loss failure recorded", reconcile: true, lossErr: private, want: "reconciliation_read_failed", calls: []string{"snapshots", "loss", "fail"}},
		{name: "reconcile loss failure record unavailable", reconcile: true, lossErr: private, failErr: private, want: "failure_record_failed", calls: []string{"snapshots", "loss", "fail"}},
		{name: "reconcile completion unavailable", reconcile: true, completeErr: private, want: "report_commit_failed", calls: []string{"snapshots", "loss", "complete"}},
		{name: "setup completion control", setup: true, calls: []string{"next", "complete"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := publicationEvent()
			if tc.setup {
				e.Action = "webhook.test"
				e.Digest = ""
			}
			var calls []string
			record := func(name string) {
				calls = append(calls, name)
				if len(calls) > 6 {
					t.Fatal("unexpected retry", calls)
				}
			}
			utc := func(at time.Time) {
				t.Helper()
				if !at.Equal(testTime) || at.Location() != time.UTC {
					t.Fatal("completion timestamp is not the selected UTC instant", at)
				}
			}
			s := &storeCalls{
				next: func(context.Context) (*Event, error) {
					record("next")
					if tc.nextErr != nil {
						return nil, tc.nextErr
					}
					return &e, nil
				},
				complete: func(_ context.Context, event *Event, r *Report, at time.Time, loss *[2]int64) error {
					record("complete")
					utc(at)
					if tc.reconcile {
						if event != nil || loss == nil || *loss != [2]int64{2, 7} {
							t.Fatal("reconcile completion binding changed")
						}
					} else if event == nil || *event != e || loss != nil {
						t.Fatal("receipt completion binding changed")
					}
					if tc.setup {
						if r != nil {
							t.Fatal("setup receipt replaced report")
						}
					} else {
						var trigger *Event
						if !tc.reconcile {
							trigger = &e
						}
						want := expectedReport(trigger)
						if r == nil || !reflect.DeepEqual(*r, want) {
							t.Fatalf("completion report=%+v, want %+v", r, want)
						}
					}
					return tc.completeErr
				},
				fail: func(_ context.Context, at time.Time, code string) error {
					record("fail")
					utc(at)
					want := "catalog_read_failed"
					if tc.lossErr != nil {
						want = "reconciliation_read_failed"
					}
					if code != want {
						t.Fatal("unsafe or wrong failure evidence", code)
					}
					return tc.failErr
				},
			}
			c := &catalogCalls{
				snapshots: func(_ context.Context, p *sdk.ListPublicationSnapshotsParams) (*sdk.ListPublicationSnapshotsResponse, error) {
					record("snapshots")
					requireSelection(t, p)
					if tc.snapshotErr != nil {
						return nil, tc.snapshotErr
					}
					return snapshotReply(t, activePage(8), "application/json", 200), nil
				},
				loss: func(context.Context) (*sdk.GetWebhookReconciliationResponse, error) {
					record("loss")
					if tc.lossErr != nil {
						return nil, tc.lossErr
					}
					return lossReply(t, `{"pendingEvents":0,"pendingEventLimit":1000,"capacityLossCount":2,"deliveryLossCount":7}`, "application/json", 200), nil
				},
			}
			a := &application{store: s, catalog: c, tenant: testTenant, host: testHost, now: func() time.Time { return testTime.In(time.FixedZone("fixture", 7200)) }}
			var err error
			if tc.reconcile {
				err = a.reconcile(t.Context())
			} else {
				err = a.process(t.Context())
			}
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireCode(t, err, tc.want)
			}
			if !slices.Equal(calls, tc.calls) {
				t.Fatalf("calls=%v, want %v", calls, tc.calls)
			}
		})
	}
}
