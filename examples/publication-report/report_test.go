package main

import (
	"context"
	"errors"
	sdk "github.com/open-agentic-catalog/open-agentic-catalog/sdk/go"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCurrentReportProjection(t *testing.T) {
	for _, reconcile := range []bool{false, true} {
		name := "receipt"
		if reconcile {
			name = "reconciliation"
		}
		t.Run(name, func(t *testing.T) {
			e := publicationEvent()
			var calls []string
			s := &storeCalls{
				next: func(context.Context) (*Event, error) { calls = append(calls, "next"); return &e, nil },
				complete: func(_ context.Context, event *Event, r *Report, at time.Time, loss *[2]int64) error {
					calls = append(calls, "complete")
					var trigger *Event
					if reconcile {
						if event != nil || loss == nil || *loss != [2]int64{2, 7} {
							t.Fatal("loss evidence was dropped, swapped or bound to an event")
						}
					} else {
						trigger = &e
						if event == nil || *event != e || loss != nil {
							t.Fatal("receipt completion changed")
						}
					}
					want := expectedReport(trigger)
					if r == nil || !reflect.DeepEqual(*r, want) || !at.Equal(testTime) || at.Location() != time.UTC {
						t.Fatalf("report=%+v, want %+v at %v", r, want, at)
					}
					return nil
				},
			}
			c := &catalogCalls{
				snapshots: func(_ context.Context, p *sdk.ListPublicationSnapshotsParams) (*sdk.ListPublicationSnapshotsResponse, error) {
					calls = append(calls, "snapshots")
					requireSelection(t, p)
					return snapshotReply(t, activePage(8), "application/json", 200), nil
				},
				loss: func(context.Context) (*sdk.GetWebhookReconciliationResponse, error) {
					calls = append(calls, "loss")
					return lossReply(t, `{"pendingEvents":0,"pendingEventLimit":1000,"capacityLossCount":2,"deliveryLossCount":7}`, "application/json", 200), nil
				},
			}
			a := &application{store: s, catalog: c, tenant: testTenant, host: testHost, now: func() time.Time { return testTime.In(time.FixedZone("fixture", 7200)) }}
			var err error
			wantCalls := []string{"next", "snapshots", "complete"}
			if reconcile {
				err = a.reconcile(t.Context())
				wantCalls = []string{"snapshots", "loss", "complete"}
			} else {
				err = a.process(t.Context())
			}
			if err != nil || !reflect.DeepEqual(calls, wantCalls) {
				t.Fatal("projection flow", err, calls)
			}
		})
	}
}

func TestSnapshotSemanticRefusalsAfterSDKDecode(t *testing.T) {
	valid := activePage(8)
	for _, tc := range []struct{ name, body, media, want string }{
		{"generation zero", strings.Replace(valid, `"generation":8`, `"generation":0`, 1), "application/json", "invalid_catalog_response"},
		{"unknown audience", strings.Replace(valid, `"anonymous"`, `"tenant"`, 1), "application/json", "invalid_catalog_response"},
		{"invalid ETag", strings.Replace(valid, strings.Repeat("b", 64), "bad", 1), "application/json", "invalid_catalog_response"},
		{"missing creation time", strings.Replace(valid, `,"createdAt":"2026-10-25T10:00:00Z"`, "", 1), "application/json", "invalid_catalog_response"},
		{"null creation time", strings.Replace(valid, `"createdAt":"2026-10-25T10:00:00Z"`, `"createdAt":null`, 1), "application/json", "invalid_catalog_response"},
		{"non JSON success", valid, "text/plain", "catalog_read_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := snapshotReply(t, tc.body, tc.media, http.StatusOK)
			if tc.media == "application/json" && response.JSON200 == nil {
				t.Fatal("semantic negative was masked by SDK decoding")
			}
			calls := 0
			c := &catalogCalls{snapshots: func(_ context.Context, p *sdk.ListPublicationSnapshotsParams) (*sdk.ListPublicationSnapshotsResponse, error) {
				calls++
				requireSelection(t, p)
				return response, nil
			}}
			r, err := readReport(t.Context(), c, testTenant, testHost, nil, testTime)
			requireCode(t, err, tc.want)
			if calls != 1 || !reflect.DeepEqual(r, Report{}) {
				t.Fatal("refused response produced a report or repeated a read")
			}
		})
	}
}

func TestLossResponseBoundsAndFailures(t *testing.T) {
	valid := `{"pendingEvents":0,"pendingEventLimit":1000,"capacityLossCount":2,"deliveryLossCount":7}`
	for _, tc := range []struct {
		name, body, media string
		status            int
		failure           error
		valid             bool
	}{
		{name: "zero pending control", body: valid, media: "application/json", status: 200, valid: true},
		{name: "maximum pending control", body: strings.Replace(valid, `"pendingEvents":0`, `"pendingEvents":1000`, 1), media: "application/json", status: 200, valid: true},
		{name: "pending over maximum", body: strings.Replace(valid, `"pendingEvents":0`, `"pendingEvents":1001`, 1), media: "application/json", status: 200},
		{name: "pending below zero", body: strings.Replace(valid, `"pendingEvents":0`, `"pendingEvents":-1`, 1), media: "application/json", status: 200},
		{name: "wrong fixed limit", body: strings.Replace(valid, `"pendingEventLimit":1000`, `"pendingEventLimit":999`, 1), media: "application/json", status: 200},
		{name: "negative delivery loss", body: strings.Replace(valid, `"deliveryLossCount":7`, `"deliveryLossCount":-1`, 1), media: "application/json", status: 200},
		{name: "non JSON success", body: valid, media: "text/plain", status: 200},
		{name: "denied", body: `{"title":"Forbidden","status":403}`, media: "application/problem+json", status: 403},
		{name: "unavailable", body: `{"title":"Unavailable","status":503}`, media: "application/problem+json", status: 503},
		{name: "transport failure", failure: errors.New("PRIVATE_LOSS_TRANSPORT")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := &catalogCalls{loss: func(context.Context) (*sdk.GetWebhookReconciliationResponse, error) {
				calls++
				if tc.failure != nil {
					return nil, tc.failure
				}
				r := lossReply(t, tc.body, tc.media, tc.status)
				if tc.status == 200 && tc.media == "application/json" && r.JSON200 == nil {
					t.Fatal("bound case did not reach receiver validation")
				}
				return r, nil
			}}
			capacity, delivery, err := readLoss(t.Context(), c)
			if tc.valid {
				if err != nil || capacity != 2 || delivery != 7 {
					t.Fatal("valid distinct loss evidence changed", capacity, delivery, err)
				}
			} else {
				requireCode(t, err, "reconciliation_read_failed")
				if capacity != 0 || delivery != 0 {
					t.Fatal("failed read produced evidence")
				}
			}
			if calls != 1 {
				t.Fatal("loss read repeated")
			}
		})
	}
}
