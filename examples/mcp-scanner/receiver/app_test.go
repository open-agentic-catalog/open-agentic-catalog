package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	sdk "github.com/open-agentic-catalog/open-agentic-catalog/sdk/go"
)

const tenant = "00000000-0000-4000-8000-000000000001"
const resource = "00000000-0000-4000-8000-000000000002"
const namespace = "00000000-0000-4000-8000-000000000003"
const release = "00000000-0000-4000-8000-000000000004"
const eventID = "00000000-0000-4000-8000-000000000005"
const secret = "whsec_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

var fixed = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// #nosec G101 -- This is an inert test file path, not a credential.
var testMapping = mapping{ResourceID: resource, NamespaceID: namespace, Endpoint: "https://127.0.0.1:9445/mcp", TokenFile: "/private/mcp-token"}
var testIdentity = scannerIdentity{Version: "4.8.7", ImageID: "sha256:" + strings.Repeat("a", 64), RulesDigest: strings.Repeat("b", 64), ConfigurationDigest: strings.Repeat("c", 64)}

func writePrivate(t *testing.T, name string, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	// #nosec G703 -- Test-selected filenames stay in a new private test directory.
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func fixtureJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic("invalid fixture JSON")
	}
	return raw
}
func recordFixture() sdk.CatalogRecord {
	id := func(n byte) uuid.UUID {
		v := uuid.MustParse("00000000-0000-4000-8000-000000000000")
		v[15] = n
		return v
	}
	tid, rid, nid, rel := uuid.MustParse(tenant), uuid.MustParse(resource), uuid.MustParse(namespace), uuid.MustParse(release)
	d := map[string]any{"name": "org.example/sum", "version": "1.0", "remotes": []any{map[string]any{"type": "streamable-http", "url": testMapping.Endpoint}}}
	return sdk.CatalogRecord{Source: "local", Resource: sdk.Resource{Id: rid, TenantId: tid, NamespaceId: nid, Kind: "mcp_server"}, Version: sdk.ResourceVersion{Id: id(6), TenantId: tid, ResourceId: rid}, VersionRevision: sdk.VersionRevision{Id: id(7), TenantId: tid, ResourceVersionId: id(6), ContentSetDigest: "sha256:" + strings.Repeat("d", 64), Representations: []sdk.Representation{{Id: id(8), Data: &d, Role: "primary", MediaType: "application/mcp-server-card+json", ContentDigest: "sha256:" + strings.Repeat("e", 64), ValidationState: "valid"}}}, Listing: sdk.CatalogListing{Id: id(9), TenantId: tid, ResourceId: rid, NamespaceId: nid, CurrentReleaseId: &rel}, ListingRevision: sdk.ListingRevision{Id: id(10), TenantId: tid, ListingId: id(9), VersionRevisionId: id(7), Digest: "sha256:" + strings.Repeat("f", 64)}, Release: sdk.ListingRelease{Id: rel, TenantId: tid, ListingId: id(9), ListingRevisionId: id(10), VersionRevisionId: id(7), State: "published", ActivatedAt: fixed}}
}

func captureFixture() capture {
	raw := json.RawMessage(`{"tools":[{"name":"sum","description":"Add two integers.","inputSchema":{"type":"object","properties":{}}}]}`)
	return capture{Document: raw, Digest: sha(raw), Source: testMapping.Endpoint, CapturedAt: fixed, Pages: 1, Complete: true}
}
func reportFixture(c capture, i scannerIdentity) json.RawMessage {
	data := map[string]any{"schemaVersion": 1, "status": "completed", "capture": map[string]any{"inputSha256": c.Digest, "source": c.Source, "capturedAt": c.CapturedAt.Format(time.RFC3339Nano), "hasNextCursor": !c.Complete, "scope": "supplied_page"}, "callerContext": map[string]any{}, "contextSha256": sha([]byte("{}")), "scanner": map[string]any{"name": "cisco-ai-mcp-scanner", "version": i.Version, "imageId": i.ImageID}, "rulesSha256": i.RulesDigest, "configurationSha256": i.ConfigurationDigest, "plannedTools": 1, "completedTools": 1, "cleanup": "passed", "results": []any{map[string]any{"tool_name": "sum", "status": "completed", "analyzers": []string{"YARA"}, "analyzer_errors": []any{}, "findings": []any{}}}}
	if !c.Complete {
		data["status"] = "partial"
	}
	return fixtureJSON(data)
}

type memoryStore struct {
	pending                                    *work
	accepted                                   map[string]Event
	reports                                    map[string]json.RawMessage
	last                                       *observation
	lastRaw                                    json.RawMessage
	code                                       string
	finished, retried, reconciled, failedRecon int
	err                                        error
	next                                       time.Time
}

func (s *memoryStore) Accept(_ context.Context, e Event, _ time.Time) error {
	if s.err != nil {
		return s.err
	}
	if s.accepted == nil {
		s.accepted = map[string]Event{}
	}
	s.accepted[e.Source+e.ID] = e
	return nil
}
func (s *memoryStore) Next(context.Context, time.Time) (*work, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.pending == nil {
		return nil, errEmpty
	}
	return s.pending, nil
}
func (s *memoryStore) Finish(_ context.Context, _ *work, o *observation, raw json.RawMessage, _ time.Time, code string) error {
	if s.err != nil {
		return s.err
	}
	s.finished++
	s.last, s.lastRaw, s.code = o, raw, code
	s.pending = nil
	return nil
}
func (s *memoryStore) Retry(_ context.Context, _ *work, next time.Time, code string) error {
	s.retried++
	s.next = next
	s.code = code
	return nil
}
func (s *memoryStore) Cached(_ context.Context, key string) (json.RawMessage, error) {
	return s.reports[key], s.err
}
func (s *memoryStore) Reconciled(context.Context, []mapping, map[string]string, [2]int64, time.Time) error {
	s.reconciled++
	return s.err
}
func (s *memoryStore) ReconciliationFailed(context.Context, string, time.Time) error {
	s.failedRecon++
	return s.err
}

type scriptedCatalog struct {
	record              sdk.CatalogRecord
	status              int
	calls, browse, loss int
	after               func(*scriptedCatalog)
	pageCursor          *string
	pageNull            bool
	lossValue           int64
}

func (c *scriptedCatalog) GetCatalogRecordWithResponse(context.Context, sdk.CatalogRecordId, ...sdk.RequestEditorFn) (*sdk.GetCatalogRecordResponse, error) {
	c.calls++
	if c.after != nil {
		c.after(c)
	}
	raw := fixtureJSON(c.record)
	return &sdk.GetCatalogRecordResponse{HTTPResponse: &http.Response{StatusCode: c.status}, JSON200: &c.record, Body: raw}, nil
}
func (c *scriptedCatalog) BrowseCatalogWithResponse(_ context.Context, p *sdk.BrowseCatalogParams, _ ...sdk.RequestEditorFn) (*sdk.BrowseCatalogResponse, error) {
	c.browse++
	if p.NamespaceId == nil || p.NamespaceId.String() != namespace || p.Kind == nil || *p.Kind != "mcp_server" || p.Limit == nil || *p.Limit != 32 {
		return nil, errors.New("wrong_selection")
	}
	page := sdk.CatalogRecordPage{Items: []sdk.CatalogSummary{{Source: "local", Resource: sdk.CatalogResourceSummary{Id: c.record.Resource.Id, NamespaceId: c.record.Resource.NamespaceId, TenantId: c.record.Resource.TenantId, Kind: "mcp_server"}, Release: sdk.CatalogReleaseSummary{Id: c.record.Release.Id}}}, Total: 1, NextCursor: c.pageCursor}
	if c.pageNull {
		page.Items = nil
	}
	raw := fixtureJSON(page)
	return &sdk.BrowseCatalogResponse{HTTPResponse: &http.Response{StatusCode: http.StatusOK}, Body: raw, JSON200: &page}, nil
}
func (c *scriptedCatalog) GetWebhookReconciliationWithResponse(context.Context, ...sdk.RequestEditorFn) (*sdk.GetWebhookReconciliationResponse, error) {
	c.loss++
	raw := []byte(`{"pendingEvents":0,"pendingEventLimit":1000,"capacityLossCount":` + strconv.FormatInt(c.lossValue, 10) + `,"deliveryLossCount":0}`)
	return &sdk.GetWebhookReconciliationResponse{HTTPResponse: &http.Response{StatusCode: http.StatusOK}, JSON200: &sdk.WebhookReconciliation{}, Body: raw}, nil
}

type fakeCollector struct {
	calls int
	err   error
	after func()
}

func (c *fakeCollector) Collect(context.Context, mapping) (capture, error) {
	c.calls++
	if c.after != nil {
		c.after()
	}
	return captureFixture(), c.err
}

type fakeScanner struct {
	identities, scans int
	identity          scannerIdentity
	err               error
}

func (s *fakeScanner) Identity(context.Context) (scannerIdentity, error) {
	s.identities++
	return s.identity, s.err
}
func (s *fakeScanner) Scan(_ context.Context, c capture, i scannerIdentity) (json.RawMessage, error) {
	s.scans++
	return reportFixture(c, i), s.err
}
func fixtureApplication() (*application, *memoryStore, *scriptedCatalog, *fakeCollector, *fakeScanner) {
	s := &memoryStore{pending: &work{Event: Event{ID: eventID, Source: eventSource + tenant, Action: "listing.release.published", TargetID: release}}}
	c := &scriptedCatalog{record: recordFixture(), status: 200}
	collector := &fakeCollector{}
	scanner := &fakeScanner{identity: testIdentity}
	a := &application{store: s, catalog: c, collector: collector, scanner: scanner, tenant: tenant, mappings: []mapping{testMapping}, now: func() time.Time { return fixed }, guard: func(context.Context) error { return nil }, slots: make(chan struct{}, 1)}
	return a, s, c, collector, scanner
}

func eventBytes(action string) []byte {
	kind := "listing_release"
	if action == "webhook.test" {
		kind = "webhook_endpoint"
	}
	return fixtureJSON(map[string]any{"specversion": "1.0", "id": eventID, "source": eventSource + tenant, "type": "com.open.agentic.catalog." + action + ".v1", "subject": release, "time": fixed.Format(time.RFC3339), "datacontenttype": "application/json", "dataschema": eventSchema, "data": map[string]any{"action": action, "targetType": kind, "targetId": release, "digest": "sha256:" + strings.Repeat("9", 64)}})
}
func signature(raw []byte, version int, key string) http.Header {
	return signatureAt(raw, version, key, fixed)
}

func signatureAt(raw []byte, version int, key string, at time.Time) http.Header {
	ts := strconv.FormatInt(at.Unix(), 10)
	m := hmac.New(sha256.New, []byte(key))
	_, _ = m.Write([]byte(ts + "."))
	_, _ = m.Write(raw)
	return http.Header{"Content-Type": {"application/cloudevents+json"}, "X-Catalog-Timestamp": {ts}, "X-Catalog-Signature": {"v1,kid=" + strconv.Itoa(version) + ",sha256=" + hex.EncodeToString(m.Sum(nil))}}
}

func TestEventAndSignatureAdmission(t *testing.T) {
	a, s, c, collector, scanner := fixtureApplication()
	a.signerFile = writePrivate(t, "signer.json", []byte(`{"secret":"`+secret+`","version":1}`))
	post := func(raw []byte, h http.Header) int {
		r := httptest.NewRequestWithContext(t.Context(), "POST", "https://receiver.test/webhook", strings.NewReader(string(raw)))
		r.Header = h
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w.Code
	}
	raw := eventBytes("listing.release.published")
	if post(raw, signature(raw, 1, secret)) != 204 {
		t.Fatal("valid signed event refused")
	}
	if post(raw, signature(raw, 1, secret)) != 204 || len(s.accepted) != 1 {
		t.Fatal("duplicate identity changed")
	}
	if c.calls != 0 || collector.calls != 0 || scanner.scans != 0 {
		t.Fatal("acceptance performed external work")
	}
	for _, change := range []func([]byte) []byte{
		func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"id":`, `"id":"`+resource+`","id":`, 1))
		},
		func(b []byte) []byte { return []byte(strings.Replace(string(b), "listing_release", "resource", 1)) },
		func(b []byte) []byte {
			return []byte(strings.Replace(string(b), eventSource+tenant, eventSource+resource, 1))
		},
		func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"subject":"`+release+`"`, `"subject":"`+resource+`"`, 1))
		},
		func(b []byte) []byte {
			return []byte(strings.Replace(string(b), "listing.release.published.v1", "listing.release.revoked.v1", 1))
		},
	} {
		bad := change(raw)
		if post(bad, signature(bad, 1, secret)) != 400 {
			t.Fatal("invalid signed event accepted")
		}
	}
	h := signature(raw, 1, secret)
	h.Set("X-Catalog-Timestamp", "0")
	if post(raw, h) != 401 {
		t.Fatal("old signature accepted")
	}
	s.err = errors.New("private-database-cause")
	if post(raw, signature(raw, 1, secret)) != 503 {
		t.Fatal("storage failure acknowledged")
	}
	s.err = nil
	rotated := strings.Replace(secret, "a", "b", 1)
	if err := os.WriteFile(a.signerFile, []byte(`{"secret":"`+rotated+`","version":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	if post(raw, signature(raw, 2, rotated)) != 204 {
		t.Fatal("rotated key not reloaded")
	}
}

func TestValidationRejectsAmbiguousJSON(t *testing.T) {
	for _, raw := range []string{`{"a":1,"a":2}`, `"\ud800"`, `"\udfff"`, `{"nested":{"a":1,"a":2}}`, `{} {}`, `{"bad":"` + string([]byte{0xff}) + `"}`} {
		if uniqueJSON([]byte(raw)) == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
	for _, raw := range []string{`"\ud83d\ude00"`, `"\\ud800"`, `{"a":[true,null,"x"]}`} {
		if uniqueJSON([]byte(raw)) != nil {
			t.Fatal("valid JSON refused")
		}
	}
}

func TestProcessBindsCaptureAndRechecksEligibility(t *testing.T) {
	a, s, c, collector, scanner := fixtureApplication()
	if err := a.process(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c.calls != 2 || collector.calls != 1 || scanner.scans != 1 || s.last == nil || s.last.State != "eligible_at_check" || s.last.EligibleAt == nil || s.finished != 1 {
		t.Fatal("incomplete governed scan")
	}
	if s.last.Catalog.ReleaseID != release || s.last.Catalog.VersionRevisionID != c.record.VersionRevision.Id.String() || s.last.Capture.Digest != captureFixture().Digest {
		t.Fatal("capture identity lost")
	}
	if strings.Contains(string(s.lastRaw), "catalogEvidence") {
		t.Fatal("verified Catalog evidence inserted into caller scan context")
	}
	if s.last.Trigger.TransitionDigest == s.last.Capture.Digest {
		t.Fatal("transition digest used as capture digest")
	}
}

func TestDeniedUnmappedAndStaleEventsDoNotContactMCP(t *testing.T) {
	for _, status := range []int{401, 403, 404} {
		a, s, c, col, scan := fixtureApplication()
		c.status = status
		if err := a.process(t.Context()); err != nil {
			t.Fatal(err)
		}
		if col.calls != 0 || scan.scans != 0 || s.finished != 1 {
			t.Fatal("denied release contacted runtime")
		}
	}
	a, s, c, col, scan := fixtureApplication()
	c.record.Listing.CurrentReleaseId = nil
	if a.process(t.Context()) != nil || col.calls != 0 || scan.scans != 0 || s.code == "" {
		t.Fatal("stale release contacted runtime")
	}
	a, _, _, col, scan = fixtureApplication()
	a.mappings = nil
	if a.process(t.Context()) != nil || col.calls != 0 || scan.scans != 0 {
		t.Fatal("unmapped release contacted runtime")
	}
}

func TestPostCaptureRevocationPreservesHistoricalEvidence(t *testing.T) {
	a, s, c, col, _ := fixtureApplication()
	col.after = func() { c.status = 403 }
	if a.process(t.Context()) != nil {
		t.Fatal("historical report not retained")
	}
	if s.last.State != "historical" || s.last.EligibleAt != nil || len(s.lastRaw) == 0 {
		t.Fatal("revoked report claimed current or lost findings")
	}
}

func TestCacheReusesOnlyExactReleaseCaptureAndScannerIdentity(t *testing.T) {
	a, s, _, _, scan := fixtureApplication()
	c := captureFixture()
	keyRaw := fixtureJSON([]string{resource, release, c.Digest, testIdentity.key()})
	s.reports = map[string]json.RawMessage{sha(keyRaw): reportFixture(c, testIdentity)}
	if a.process(t.Context()) != nil || scan.scans != 0 || !s.last.AnalysisReused {
		t.Fatal("exact completed scan not reused")
	}
	s.pending = &work{Event: Event{ID: eventID, Source: eventSource + tenant, Action: "listing.release.published", TargetID: release}}
	scan.identity.ImageID = "sha256:" + strings.Repeat("f", 64)
	if a.process(t.Context()) != nil || scan.scans != 1 || s.last.AnalysisReused {
		t.Fatal("changed image reused old scan")
	}
}

func TestTransientRetriesAreBoundedAndShutdownKeepsWork(t *testing.T) {
	for attempt := 0; attempt < 3; attempt++ {
		a, s, c, col, _ := fixtureApplication()
		s.pending.Attempts = attempt
		c.status = 503
		if a.process(t.Context()) != nil {
			t.Fatal("failure not recorded")
		}
		if col.calls != 0 || attempt < 2 && s.retried != 1 || attempt == 2 && s.finished != 1 {
			t.Fatal("retry budget not enforced")
		}
	}
	a, s, _, col, _ := fixtureApplication()
	ctx, cancel := context.WithCancel(t.Context())
	col.after = cancel
	if a.process(ctx) == nil || s.finished != 0 || s.retried != 0 {
		t.Fatal("shutdown completed pending work")
	}
}

func TestReconciliationAlwaysTraversesAndRejectsIncompletePages(t *testing.T) {
	a, s, c, _, _ := fixtureApplication()
	for _, n := range []int64{0, 2, 0} {
		c.lossValue = n
		if a.reconcile(t.Context()) != nil {
			t.Fatal("counter change blocked refresh")
		}
	}
	if c.browse != 3 || c.loss != 3 || s.reconciled != 3 {
		t.Fatal("unchanged/decreased counters skipped traversal")
	}
	c.pageNull = true
	if a.reconcile(t.Context()) == nil || s.failedRecon != 1 || s.reconciled != 3 {
		t.Fatal("null page treated as complete absence")
	}
	c.pageNull = false
	cursor := "repeat"
	c.pageCursor = &cursor
	if a.reconcile(t.Context()) == nil || s.failedRecon != 2 || s.reconciled != 3 {
		t.Fatal("repeated page treated as complete")
	}
}
