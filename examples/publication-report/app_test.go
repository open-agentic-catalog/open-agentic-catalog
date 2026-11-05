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

	sdk "github.com/open-agentic-catalog/open-agentic-catalog/sdk/go"
)

const testTenant = "00000000-0000-4000-8000-000000000001"
const testID = "00000000-0000-4000-8000-000000000002"
const testTarget = "00000000-0000-4000-8000-000000000003"
const testHost = "catalog.example.test"
const testSecret = "whsec_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

var testTime = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func eventBytes(action string) []byte {
	target := "publication_snapshot"
	data := map[string]any{"action": action, "targetType": target, "targetId": testTarget, "host": testHost}
	data["digest"] = "sha256:" + strings.Repeat("a", 64)
	if action == "webhook.test" {
		data["targetType"] = "webhook_endpoint"
		delete(data, "host")
		delete(data, "digest")
	}
	raw, err := json.Marshal(map[string]any{"specversion": "1.0", "id": testID, "source": eventSource + testTenant, "type": "com.open.agentic.catalog." + action + ".v1", "subject": testTarget, "time": testTime.Format(time.RFC3339), "datacontenttype": "application/json", "dataschema": eventSchema, "data": data})
	if err != nil {
		panic(err)
	}
	return raw
}
func signature(raw []byte, version int, secret string) http.Header {
	return signatureAt(raw, version, secret, testTime)
}

func signatureAt(raw []byte, version int, secret string, at time.Time) http.Header {
	stamp := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(stamp + "."))
	_, _ = mac.Write(raw)
	return http.Header{"Content-Type": {"application/cloudevents+json"}, "X-Catalog-Timestamp": {stamp}, "X-Catalog-Signature": {"v1,kid=" + strconv.Itoa(version) + ",sha256=" + hex.EncodeToString(mac.Sum(nil))}}
}
func writePrivate(t *testing.T, name string, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	// #nosec G703 -- Only fixed test filenames are written under a private test directory.
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

type memoryStore struct {
	accepted    []Event
	pending     *Event
	saved       Saved
	failure     error
	completions int
}

func (s *memoryStore) Accept(_ context.Context, e Event, _ time.Time) error {
	if s.failure != nil {
		return s.failure
	}
	s.accepted = append(s.accepted, e)
	return nil
}
func (s *memoryStore) Next(context.Context) (*Event, error) { return s.pending, s.failure }
func (s *memoryStore) Complete(_ context.Context, _ *Event, r *Report, _ time.Time, loss *[2]int64) error {
	if s.failure != nil {
		return s.failure
	}
	s.completions++
	s.pending = nil
	if r != nil {
		s.saved.LastSuccessful = r
		s.saved.RefreshFailure = ""
	}
	if loss != nil {
		s.saved.CapacityLoss = loss[0]
		s.saved.DeliveryLoss = loss[1]
	}
	return nil
}
func (s *memoryStore) Fail(_ context.Context, at time.Time, code string) error {
	s.saved.RefreshFailure = code
	s.saved.LastAttemptAt = &at
	return nil
}
func (s *memoryStore) Read(context.Context) (Saved, error) { return s.saved, s.failure }

type scriptedCatalog struct {
	body      string
	status    int
	failure   error
	calls     int
	lossCalls int
	loss      string
}

func (c *scriptedCatalog) ListPublicationSnapshotsWithResponse(_ context.Context, p *sdk.ListPublicationSnapshotsParams, _ ...sdk.RequestEditorFn) (*sdk.ListPublicationSnapshotsResponse, error) {
	c.calls++
	if p.Host != testHost || p.State == nil || *p.State != "active" || p.Limit == nil || *p.Limit != 2 || p.Cursor != nil {
		return nil, errors.New("incorrect selection")
	}
	return &sdk.ListPublicationSnapshotsResponse{HTTPResponse: &http.Response{StatusCode: c.status}, Body: []byte(c.body), JSON200: &sdk.PublicationSnapshotPage{}}, c.failure
}
func (c *scriptedCatalog) GetWebhookReconciliationWithResponse(context.Context, ...sdk.RequestEditorFn) (*sdk.GetWebhookReconciliationResponse, error) {
	c.lossCalls++
	return &sdk.GetWebhookReconciliationResponse{HTTPResponse: &http.Response{StatusCode: http.StatusOK}, Body: []byte(c.loss), JSON200: &sdk.WebhookReconciliation{}}, nil
}
func activePage(generation int) string {
	return `{"items":[{"id":"` + testTarget + `","tenantId":"` + testTenant + `","host":"` + testHost + `","state":"active","generation":` + strconv.Itoa(generation) + `,"audience":"anonymous","ardProfile":"fixture-ard","aiCatalogProfile":"fixture-ai-catalog","etag":"\"` + strings.Repeat("b", 64) + `\"","createdAt":"2026-10-25T10:00:00Z"}]}`
}

func TestEnvelopeAndSignedBytes(t *testing.T) {
	for _, action := range []string{"publication.activated", "publication.unpublished", "webhook.test"} {
		e, err := parseEvent(eventBytes(action), testTenant, testHost)
		if err != nil {
			t.Fatal(action, err)
		}
		if action != "webhook.test" && e.Digest != "sha256:"+strings.Repeat("a", 64) {
			t.Fatal("snapshot-state digest context changed")
		}
	}
	for name, alter := range map[string]func([]byte) []byte{
		"duplicate root": func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"id":`, `"id":"`+testID+`","id":`, 1))
		},
		"duplicate nested": func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"action":`, `"action":"publication.activated","action":`, 1))
		},
		"wrong source": func(b []byte) []byte {
			return []byte(strings.Replace(string(b), eventSource+testTenant, eventSource+testTarget, 1))
		},
		"wrong type": func(b []byte) []byte {
			return []byte(strings.Replace(string(b), "catalog.publication.activated.v1", "catalog.publication.unpublished.v1", 1))
		},
		"wrong target type": func(b []byte) []byte {
			return []byte(strings.Replace(string(b), "publication_snapshot", "resource", 1))
		},
		"wrong subject": func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"subject":"`+testTarget+`"`, `"subject":"`+testID+`"`, 1))
		},
		"wrong host":     func(b []byte) []byte { return []byte(strings.Replace(string(b), testHost, "other.example", 1)) },
		"wrong digest":   func(b []byte) []byte { return []byte(strings.Replace(string(b), "sha256:", "sha512:", 1)) },
		"extra field":    func(b []byte) []byte { return append([]byte(`{"extra":true,`), b[1:]...) },
		"null content":   func(b []byte) []byte { return []byte(strings.Replace(string(b), `"application/json"`, `null`, 1)) },
		"trailing value": func(b []byte) []byte { return append(b, []byte(`{}`)...) },
	} {
		t.Run(name, func(t *testing.T) {
			raw := alter(eventBytes("publication.activated"))
			if _, err := parseEvent(raw, testTenant, testHost); err == nil {
				t.Fatal("accepted inconsistent signed envelope")
			}
		})
	}
}

func TestHandlerAcceptanceAndRotation(t *testing.T) {
	key := writePrivate(t, "signer.json", []byte(`{"secret":"`+testSecret+`","version":1}`))
	s := &memoryStore{}
	a := &application{store: s, tenant: testTenant, host: testHost, signerFile: key, now: func() time.Time { return testTime }, slots: make(chan struct{}, 1)}
	post := func(raw []byte, h http.Header) int {
		r := httptest.NewRequestWithContext(t.Context(), "POST", "https://receiver.test/webhook", strings.NewReader(string(raw)))
		r.Header = h
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w.Code
	}
	raw := eventBytes("publication.activated")
	if post(raw, signature(raw, 1, testSecret)) != 204 || len(s.accepted) != 1 {
		t.Fatal("signed control rejected")
	}
	bad := signature(raw, 1, testSecret)
	bad.Set("Ce-Type", "webhook.test")
	bad.Set("X-Catalog-Event-Id", "untrusted")
	if post(raw, bad) != 204 || s.accepted[1].Action != "publication.activated" {
		t.Fatal("unsigned headers became authority")
	}
	if post(append(raw, ' '), signature(raw, 1, testSecret)) != 401 {
		t.Fatal("changed signed bytes accepted")
	}
	inconsistent := []byte(strings.Replace(string(raw), testHost, "wrong.example", 1))
	if post(inconsistent, signature(inconsistent, 1, testSecret)) != 400 {
		t.Fatal("signature bypassed schema")
	}
	if len(s.accepted) != 2 {
		t.Fatal("rejection stored work")
	}
	s.failure = errors.New("secret database cause")
	if post(raw, signature(raw, 1, testSecret)) != 503 {
		t.Fatal("storage failure acknowledged")
	}
	s.failure = nil
	a.slots <- struct{}{}
	if post(raw, signature(raw, 1, testSecret)) != 503 {
		t.Fatal("concurrency limit ignored")
	}
	<-a.slots
	second := "whsec_" + strings.Repeat("b", 43)
	if os.WriteFile(key, []byte(`{"secret":"`+second+`","version":2}`), 0600) != nil {
		t.Fatal("rotate fixture")
	}
	if post(raw, signature(raw, 1, testSecret)) != 401 || post(raw, signature(raw, 2, second)) != 204 {
		t.Fatal("atomic signer replacement ignored")
	}
	overlap := signature(raw, 2, second)
	overlap.Set("X-Catalog-Previous-Signature", signature(raw, 1, testSecret).Get("X-Catalog-Signature"))
	if os.WriteFile(key, []byte(`{"secret":"`+testSecret+`","version":1}`), 0600) != nil || post(raw, overlap) != 204 {
		t.Fatal("previous signature overlap rejected")
	}
}

func TestReportReadsAndFailurePreservation(t *testing.T) {
	valid := activePage(8)
	for name, body := range map[string]string{"missing items": `{}`, "null items": `{"items":null}`, "cursor": `{"items":[],"nextCursor":"more"}`, "null cursor": `{"items":[],"nextCursor":null}`, "wrong tenant": strings.Replace(valid, testTenant, testID, 1), "wrong host": strings.Replace(valid, testHost, "other.example", 1), "retired": strings.Replace(valid, `"active"`, `"retired"`, 1), "malformed": `{`, "duplicate": strings.Replace(valid, `"items":`, `"items":[],"items":`, 1), "extra active": strings.Replace(valid, `}]}`, `},`+strings.TrimSuffix(strings.TrimPrefix(valid, `{"items":[`), `]}`)+`]}`, 1)} {
		t.Run(name, func(t *testing.T) {
			c := &scriptedCatalog{body: body, status: 200}
			if _, err := readReport(context.Background(), c, testTenant, testHost, nil, testTime); err == nil {
				t.Fatal("malformed success became report")
			}
		})
	}
	c := &scriptedCatalog{body: valid, status: 200, loss: `{"capacityLossCount":2,"deliveryLossCount":3,"pendingEvents":0,"pendingEventLimit":1000}`}
	e := Event{ID: testID, Action: "publication.activated", Digest: "sha256:" + strings.Repeat("a", 64)}
	s := &memoryStore{pending: &e}
	a := &application{store: s, catalog: c, tenant: testTenant, host: testHost, now: func() time.Time { return testTime }}
	if err := a.process(context.Background()); err != nil || s.saved.LastSuccessful.Active.Generation != 8 || s.completions != 1 {
		t.Fatal("refresh failed", err)
	}
	prior := s.saved.LastSuccessful
	for _, status := range []int{401, 403, 404, 500, 503} {
		s.pending = &e
		c.status = status
		if a.process(context.Background()) == nil || s.saved.LastSuccessful != prior || s.saved.RefreshFailure == "" || s.pending == nil {
			t.Fatal("failed read became unpublication", status)
		}
	}
	for _, cause := range []error{context.DeadlineExceeded, context.Canceled, errors.New("private transport diagnostic")} {
		c.status = http.StatusOK
		c.failure = cause
		s.pending = &e
		if err := a.process(context.Background()); err == nil || err.Error() != "catalog_read_failed" || s.saved.LastSuccessful != prior || s.pending == nil {
			t.Fatal("transport failure lost pending work or historical state")
		}
	}
	c.failure = nil
	c.status = 200
	c.body = `{"items":[]}`
	s.pending = &e
	if a.process(context.Background()) != nil || s.saved.LastSuccessful.Active != nil {
		t.Fatal("complete empty response not recorded")
	}
	// A late activation trigger refreshes the current empty state. A rollback
	// can report a lower generation; neither event order nor generation ranks authority.
	c.body = activePage(2)
	if a.reconcile(context.Background()) != nil || s.saved.LastSuccessful.Active.Generation != 2 || c.lossCalls != 1 {
		t.Fatal("rollback reconciliation failed")
	}
	if a.reconcile(context.Background()) != nil || c.lossCalls != 2 {
		t.Fatal("unchanged counters suppressed reconciliation")
	}
	e.Action = "webhook.test"
	s.pending = &e
	calls := c.calls
	if a.process(context.Background()) != nil || c.calls != calls {
		t.Fatal("setup test fetched publication")
	}
}

func TestReportResponseCasingPreservesObservation(t *testing.T) {
	valid := activePage(8)
	for name, body := range map[string]string{
		"missing exact items":           `{"Items":[]}`,
		"alias overwrites active items": strings.TrimSuffix(valid, "}") + `,"Items":[]}`,
		"snapshot alias":                strings.Replace(valid, `"tenantId":`, `"TenantId":`, 1),
		"continuation alias":            `{"items":[],"NextCursor":"more"}`,
	} {
		t.Run(name, func(t *testing.T) {
			prior := &Report{Label: "Last observed publication state", Host: testHost, Active: &Snapshot{Generation: 9}}
			e := Event{ID: testID, Action: "publication.activated"}
			s := &memoryStore{pending: &e, saved: Saved{LastSuccessful: prior}}
			a := &application{store: s, catalog: &scriptedCatalog{body: body, status: 200}, tenant: testTenant, host: testHost, now: func() time.Time { return testTime }}
			if a.process(t.Context()) == nil || s.saved.LastSuccessful != prior || s.pending == nil || s.completions != 0 {
				t.Fatal("wrongly cased response changed the previous observation or completed its receipt")
			}
		})
	}
}

func TestReconciliationResponseFields(t *testing.T) {
	valid := `{"capacityLossCount":2,"deliveryLossCount":3,"pendingEvents":0,"pendingEventLimit":1000}`
	for name, body := range map[string]string{
		"capacity alias":  strings.Replace(valid, "capacityLossCount", "CapacityLossCount", 1),
		"duplicate alias": strings.TrimSuffix(valid, "}") + `,"CapacityLossCount":0}`,
		"missing count":   `{"deliveryLossCount":3,"pendingEvents":0,"pendingEventLimit":1000}`,
		"null count":      strings.Replace(valid, `"capacityLossCount":2`, `"capacityLossCount":null`, 1),
		"negative count":  strings.Replace(valid, `"capacityLossCount":2`, `"capacityLossCount":-1`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			c := &scriptedCatalog{status: 200, loss: body}
			if _, _, err := readLoss(t.Context(), c); err == nil {
				t.Fatal("invalid counters accepted")
			}
		})
	}
	c := &scriptedCatalog{status: 200, loss: valid}
	if capacity, delivery, err := readLoss(t.Context(), c); err != nil || capacity != 2 || delivery != 3 {
		t.Fatal("valid counters rejected")
	}
}

func TestPrivateFilesAndRepeatableRender(t *testing.T) {
	path := writePrivate(t, "token", []byte("protected"))
	if value, err := readToken(t.Context(), path); err != nil || value != "protected" {
		t.Fatal("token file read failed")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readToken(canceled, filepath.Join(t.TempDir(), "missing")); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled provider read the credential")
	}
	if _, err := privateFile(path, 100); err != nil {
		t.Fatal(err)
	}
	// #nosec G302 -- This fixture proves that publicly readable credentials are refused.
	if os.Chmod(path, 0644) != nil {
		t.Fatal("chmod")
	}
	if _, err := privateFile(path, 100); err == nil {
		t.Fatal("public input accepted")
	}
	path = writePrivate(t, "token", []byte("protected"))
	if _, err := privateFile(path, 2); err == nil {
		t.Fatal("unbounded credential")
	}
	saved := Saved{LastSuccessful: &Report{Label: "Last observed publication state", Host: testHost, ObservedAt: testTime}, RefreshFailure: "catalog_read_failed"}
	output := filepath.Join(t.TempDir(), "report.json")
	if render(filepath.Join(output, "missing"), saved) == nil {
		t.Fatal("output failure ignored")
	}
	for range 2 {
		if render(output, saved) != nil {
			t.Fatal("saved report cannot render repeatedly")
		}
	}
	// #nosec G304 -- The report is created under this test's private temporary directory.
	raw, err := os.ReadFile(output)
	if err != nil || !strings.Contains(string(raw), "catalog_read_failed") {
		t.Fatal("historical status lost")
	}
	info, _ := os.Stat(output)
	if info.Mode().Perm() != 0600 {
		t.Fatal("report is public")
	}
}

func TestOptionsAndRequestBounds(t *testing.T) {
	base := []string{"init", "--origin", "https://catalog.example.test", "--tenant", testTenant, "--host", testHost, "--database-file", "private/receiver.dsn"}
	if _, err := parseOptions(base); err != nil {
		t.Fatal("valid offline inputs rejected", err)
	}
	for _, extra := range [][]string{{"--capacity", "0"}, {"--capacity", "10001"}, {"--retention", "30s"}, {"--poll-interval", "0s"}, {"--reconcile-interval", "1s"}, {"--host", "*.example.test"}, {"--origin", "http://remote.example.test"}, {"--unknown", "value"}} {
		if _, err := parseOptions(append(append([]string{}, base...), extra...)); err == nil {
			t.Fatal("invalid bounded input accepted", extra)
		}
	}
	key := writePrivate(t, "signer.json", []byte(`{"secret":"`+testSecret+`","version":1}`))
	s := &memoryStore{}
	a := &application{store: s, tenant: testTenant, host: testHost, signerFile: key, now: func() time.Time { return testTime }, slots: make(chan struct{}, 1)}
	for _, tc := range []struct {
		method, path, media, body string
		want                      int
	}{
		{http.MethodGet, "/webhook", "", "", http.StatusMethodNotAllowed},
		{http.MethodPost, "/other", "", "", http.StatusNotFound},
		{http.MethodPost, "/webhook", "text/plain", "{}", http.StatusUnsupportedMediaType},
		{http.MethodPost, "/webhook", "application/cloudevents+json", strings.Repeat("a", (256<<10)+1), http.StatusRequestEntityTooLarge},
	} {
		r := httptest.NewRequestWithContext(t.Context(), tc.method, "https://receiver.test"+tc.path, strings.NewReader(tc.body))
		r.Header.Set("Content-Type", tc.media)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatal("request bound not enforced", w.Code, tc.want)
		}
	}
	if len(s.accepted) != 0 {
		t.Fatal("invalid request scheduled work")
	}
}
