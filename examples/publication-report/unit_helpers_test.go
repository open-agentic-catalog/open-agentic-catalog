package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	sdk "github.com/open-agentic-catalog/open-agentic-catalog/sdk/go"
)

// These ports record calls; they do not model database persistence.
type storeCalls struct {
	accept   func(context.Context, Event, time.Time) error
	next     func(context.Context) (*Event, error)
	complete func(context.Context, *Event, *Report, time.Time, *[2]int64) error
	fail     func(context.Context, time.Time, string) error
	read     func(context.Context) (Saved, error)
}

func (s *storeCalls) Accept(c context.Context, e Event, at time.Time) error {
	return s.accept(c, e, at)
}
func (s *storeCalls) Next(c context.Context) (*Event, error) { return s.next(c) }
func (s *storeCalls) Complete(c context.Context, e *Event, r *Report, at time.Time, l *[2]int64) error {
	return s.complete(c, e, r, at, l)
}
func (s *storeCalls) Fail(c context.Context, at time.Time, code string) error {
	return s.fail(c, at, code)
}
func (s *storeCalls) Read(c context.Context) (Saved, error) { return s.read(c) }

type catalogCalls struct {
	snapshots func(context.Context, *sdk.ListPublicationSnapshotsParams) (*sdk.ListPublicationSnapshotsResponse, error)
	loss      func(context.Context) (*sdk.GetWebhookReconciliationResponse, error)
}

func (c *catalogCalls) ListPublicationSnapshotsWithResponse(ctx context.Context, p *sdk.ListPublicationSnapshotsParams, _ ...sdk.RequestEditorFn) (*sdk.ListPublicationSnapshotsResponse, error) {
	return c.snapshots(ctx, p)
}
func (c *catalogCalls) GetWebhookReconciliationWithResponse(ctx context.Context, _ ...sdk.RequestEditorFn) (*sdk.GetWebhookReconciliationResponse, error) {
	return c.loss(ctx)
}

func snapshotReply(t *testing.T, raw, media string, status int) *sdk.ListPublicationSnapshotsResponse {
	t.Helper()
	r, err := sdk.ParseListPublicationSnapshotsResponse(&http.Response{StatusCode: status, Header: http.Header{"Content-Type": {media}}, Body: io.NopCloser(strings.NewReader(raw))})
	if err != nil {
		t.Fatal("snapshot fixture does not reach the receiver decoder", err)
	}
	return r
}
func lossReply(t *testing.T, raw, media string, status int) *sdk.GetWebhookReconciliationResponse {
	t.Helper()
	r, err := sdk.ParseGetWebhookReconciliationResponse(&http.Response{StatusCode: status, Header: http.Header{"Content-Type": {media}}, Body: io.NopCloser(strings.NewReader(raw))})
	if err != nil {
		t.Fatal("loss fixture does not reach the receiver decoder", err)
	}
	return r
}
func requireSelection(t *testing.T, p *sdk.ListPublicationSnapshotsParams) {
	t.Helper()
	if p == nil || p.Host != testHost || p.State == nil || *p.State != "active" || p.Limit == nil || *p.Limit != 2 || p.Cursor != nil {
		t.Fatal("changed bounded current-publication selection", p)
	}
}
func expectedReport(trigger *Event) Report {
	return Report{Label: "Last observed publication state", ObservedAt: testTime, Tenant: testTenant, Host: testHost, Trigger: trigger, Active: &Snapshot{ID: testTarget, TenantID: testTenant, Host: testHost, State: "active", Generation: 8, Audience: "anonymous", ETag: `"` + strings.Repeat("b", 64) + `"`, CreatedAt: time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)}}
}
func publicationEvent() Event {
	return Event{ID: testID, Source: eventSource + testTenant, Action: "publication.activated", TargetID: testTarget, Digest: "sha256:" + strings.Repeat("a", 64)}
}
func requireCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Fatalf("error=%v, want fixed %q", err, want)
	}
}
