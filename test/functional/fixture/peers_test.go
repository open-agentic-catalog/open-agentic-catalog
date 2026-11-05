package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type peerRoundTrip func(*http.Request) (*http.Response, error)

func (f peerRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

// peerFailedBody models an interrupted response without a socket or a sleep.
type peerFailedBody struct{ closed bool }

func (*peerFailedBody) Read(buffer []byte) (int, error) {
	return copy(buffer, "private partial response"), errors.New("private read failure")
}

func (body *peerFailedBody) Close() error {
	body.closed = true
	return nil
}

func TestPeerFrontendHealthDoesNotContactCatalog(t *testing.T) {
	t.Parallel()
	handler := peerHandler(peerRoundTrip(func(_ *http.Request) (*http.Response, error) {
		t.Fatal("health request reached the Catalog")
		return nil, errors.New("unexpected backend request")
	}))
	for _, host := range []string{"127.0.0.1:8443", "peer-a.test:8443", "private-a.test:8443"} {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil)
		request.Host = host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		expected := http.StatusNotFound
		if host == "127.0.0.1:8443" {
			expected = http.StatusOK
		}
		if response.Code != expected {
			t.Fatalf("health response = %d, want %d", response.Code, expected)
		}
	}
}

func TestPeerFrontendClosesAndDiscardsInterruptedResponse(t *testing.T) {
	t.Parallel()
	body := &peerFailedBody{}
	handler := peerHandler(peerRoundTrip(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Etag": {`"incomplete"`}}, Body: body}, nil
	}))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/.well-known/ard.json", nil)
	request.Host = "peer-a.test:8443"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if !body.closed || response.Code != http.StatusBadGateway || response.Body.String() != "Catalog peer unavailable\n" || response.Header().Get("ETag") != "" {
		t.Fatal("interrupted response was leaked, retained or accepted")
	}
}

func TestPeerFrontendBoundsSearchRequestBody(t *testing.T) {
	t.Parallel()
	for _, size := range []int{64 << 10, (64 << 10) + 1} {
		calls := 0
		handler := peerHandler(peerRoundTrip(func(request *http.Request) (*http.Response, error) {
			calls++
			body, err := io.ReadAll(request.Body)
			if err != nil || len(body) != size {
				t.Fatal("frontend rejected a request at its byte ceiling")
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody}, nil
		}))
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ard/v1/search", strings.NewReader(strings.Repeat("x", size)))
		request.Host = "peer-a.test:8443"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		expected := http.StatusOK
		if size > 64<<10 {
			expected = http.StatusRequestEntityTooLarge
		}
		if size > 64<<10 && calls != 0 || size == 64<<10 && calls != 1 {
			t.Fatal("oversized request reached the backend or bounded request was not forwarded")
		}
		if response.Code != expected {
			t.Fatalf("request size %d: status = %d, want %d", size, response.Code, expected)
		}
	}
}

func TestPeerFrontendPropagatesCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	handler := peerHandler(peerRoundTrip(func(request *http.Request) (*http.Response, error) {
		called = true
		if !errors.Is(request.Context().Err(), context.Canceled) {
			t.Fatal("frontend lost caller cancellation")
		}
		return nil, request.Context().Err()
	}))
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/.well-known/ard.json", nil)
	request.Host = "peer-a.test:8443"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if !called || response.Code != http.StatusBadGateway {
		t.Fatal("cancelled upstream request was accepted")
	}
}

func TestPeerFrontendRoutesOnlyDiscoveryAndPreservesWireEvidence(t *testing.T) {
	t.Parallel()
	calls := 0
	handler := peerHandler(peerRoundTrip(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.Host != "api-a:8080" || request.URL.Scheme != "http" || request.Host != "peer-a.test:8443" || request.Header.Get("Forwarded") != "" || request.Header.Get("X-Forwarded-Host") != "" || request.Header.Get("Authorization") != "Bearer fixture" || request.URL.RawQuery != "pageSize=2" {
			t.Fatal("frontend changed routing, authority or credential boundaries")
		}
		if _, ok := request.Context().Deadline(); !ok {
			t.Fatal("frontend has no deadline")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Etag": {`"immutable"`}, "Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"entries":[]}`))}, nil
	}))
	request := httptest.NewRequestWithContext(t.Context(), "GET", "/ard/v1/agents?pageSize=2", nil)
	request.Host = "peer-a.test:8443"
	request.Header.Set("Authorization", "Bearer fixture")
	request.Header.Set("Forwarded", "host=untrusted")
	request.Header.Set("X-Forwarded-Host", "untrusted")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || response.Body.String() != `{"entries":[]}` || response.Header().Get("ETag") != `"immutable"` || calls != 1 {
		t.Fatal("frontend did not preserve Catalog response")
	}
	for _, test := range []struct{ method, host, path string }{
		{"GET", "unknown:8443", "/.well-known/ard.json"},
		{"POST", "peer-a.test:8443", "/control"},
		{"GET", "peer-a.test:8443", "/api/v1/resources"},
		{"DELETE", "peer-a.test:8443", "/ard/v1/agents"},
		{"GET", "peer-a.test:8443", "https://untrusted/.well-known/ard.json"},
	} {
		request = httptest.NewRequestWithContext(t.Context(), test.method, test.path, nil)
		request.Host = test.host
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 404 || calls != 1 {
			t.Fatal("frontend contacted backend for a denied request")
		}
	}
}

func TestPeerFrontendBoundsAndRedactsBackendFailure(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		transport peerRoundTrip
	}{
		{"unavailable", func(_ *http.Request) (*http.Response, error) { return nil, errors.New("private backend detail") }},
		{"oversized", func(_ *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", peerResponseLimit+1)))}, nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), "GET", "/.well-known/ard.json", nil)
			request.Host = "private-a.test:8443"
			response := httptest.NewRecorder()
			peerHandler(test.transport).ServeHTTP(response, request)
			if response.Code != 502 || response.Body.String() != "Catalog peer unavailable\n" {
				t.Fatal("frontend leaked or accepted failed upstream content")
			}
		})
	}
}

func TestPeerFrontendPreservesConditionalAndSearchExchanges(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, method, path, requestBody, responseBody string
		status                                        int
	}{
		{"conditional", http.MethodGet, "/.well-known/ard.json", "", "", http.StatusNotModified},
		{"search", http.MethodPost, "/ard/v1/search", `{"query":{"text":"peer"},"federation":"none"}`, `{"results":[]}`, http.StatusOK},
		{"denied", http.MethodGet, "/.well-known/ard.json", "", `{"code":"UNAUTHENTICATED"}`, http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := peerHandler(peerRoundTrip(func(request *http.Request) (*http.Response, error) {
				var body []byte
				var err error
				if request.Body != nil {
					body, err = io.ReadAll(request.Body)
				}
				if err != nil || string(body) != test.requestBody || request.Method != test.method || request.URL.Path != test.path || request.Host != "private-a.test:8443" || request.Header.Get("If-None-Match") != `"prior"` {
					t.Fatal("frontend changed a conditional or Search request")
				}
				return &http.Response{StatusCode: test.status, Header: http.Header{"Etag": {`"current"`}}, Body: io.NopCloser(strings.NewReader(test.responseBody))}, nil
			}))
			request := httptest.NewRequestWithContext(t.Context(), test.method, test.path, strings.NewReader(test.requestBody))
			request.Host = "private-a.test:8443"
			request.Header.Set("If-None-Match", `"prior"`)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status || response.Body.String() != test.responseBody || response.Header().Get("ETag") != `"current"` {
				t.Fatal("frontend changed the backend status, body or validator")
			}
		})
	}
}

func TestPeerFrontendRoutesBAndCountsOnlyBoundedDiscoveryMetadata(t *testing.T) {
	t.Parallel()
	handler := peerHandler(peerRoundTrip(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "api:8080" || request.Host != "peer-b.test:8443" {
			t.Fatal("B request crossed destination boundary")
		}
		raw, err := io.ReadAll(request.Body)
		if err != nil || !strings.Contains(string(raw), "private query text") {
			t.Fatal("counter consumed request body")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"results":[]}`))}, nil
	}))
	request := httptest.NewRequestWithContext(t.Context(), "POST", "/ard/v1/search", strings.NewReader(`{"query":{"text":"private query text"},"federation":"none"}`))
	request.Host = "peer-b.test:8443"
	request.Header.Set("Authorization", "Bearer private-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("B status = %d", response.Code)
	}
	request = httptest.NewRequestWithContext(t.Context(), "GET", "/peer-stats", nil)
	request.Host = "127.0.0.1:8443"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || response.Body.String() != "{\"peer-b.test:8443 POST /ard/v1/search none 200\":1}\n" {
		t.Fatalf("unsafe or incomplete counter: %s", response.Body.String())
	}
	request.Host = "peer-b.test:8443"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 404 {
		t.Fatal("peer endpoint exposed fixture counters")
	}
}

func TestPeerContinuationFaultPreservesInitialPagesAndRecovers(t *testing.T) {
	t.Parallel()
	calls := 0
	initial := `{"query":{"text":"private query","filter":{"metadata.license":"Apache-2.0"}},"federation":"none","pageSize":2}`
	continuation := initial[:len(initial)-1] + `,"pageToken":"private-token"}`
	handler := peerHandler(peerRoundTrip(func(request *http.Request) (*http.Response, error) {
		calls++
		raw, err := io.ReadAll(request.Body)
		if err != nil || string(raw) != initial && string(raw) != continuation {
			t.Fatal("page control changed the forwarded query or token")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"results":[]}`))}, nil
	}))
	request := func(method, host, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		r.Host = host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, r)
		return response
	}
	if response := request("POST", "127.0.0.1:8443", "/peer-fault", `{"enabled":true}`); response.Code != 200 || response.Body.String() != "{\"enabled\":true}\n" {
		t.Fatal("fault control did not acknowledge activation")
	}
	for _, test := range []struct {
		host, body string
		status     int
	}{
		{"peer-a.test:8443", initial, 200},
		{"peer-a.test:8443", continuation, 503},
		{"private-a.test:8443", continuation, 200},
		{"peer-b.test:8443", continuation, 200},
	} {
		if response := request("POST", test.host, "/ard/v1/search", test.body); response.Code != test.status {
			t.Fatalf("page fault response = %d, want %d", response.Code, test.status)
		}
	}
	if calls != 3 {
		t.Fatal("fault reached a backend or suppressed an unrelated page")
	}
	if response := request("POST", "127.0.0.1:8443", "/peer-fault", `{"enabled":false}`); response.Code != 200 {
		t.Fatal("fault control did not reset")
	}
	if response := request("POST", "peer-a.test:8443", "/ard/v1/search", continuation); response.Code != 200 || calls != 4 {
		t.Fatal("continuation failed after fault reset")
	}
	response := request("GET", "127.0.0.1:8443", "/peer-pages", "")
	var pages map[string]int
	if err := json.Unmarshal(response.Body.Bytes(), &pages); err != nil || response.Code != 200 || len(pages) != 5 || pages["peer-a.test:8443 initial 200"] != 1 || pages["peer-a.test:8443 continuation 503"] != 1 || pages["peer-a.test:8443 continuation 200"] != 1 {
		t.Fatal("page evidence omitted a fresh request, outage or recovery")
	}
	if strings.Contains(response.Body.String(), "private query") || strings.Contains(response.Body.String(), "private-token") {
		t.Fatal("page counters exposed query or token")
	}
}

func TestPeerFaultControlRejectsInvalidOrPeerFacingInputs(t *testing.T) {
	t.Parallel()
	calls := 0
	handler := peerHandler(peerRoundTrip(func(_ *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody}, nil
	}))
	for _, raw := range []string{"", `{}`, `null`, `{"enabled":null}`, `{"Enabled":true}`, `{"enabled":"true"}`, `{"enabled":true,"host":"untrusted"}`, `{"enabled":true,"enabled":false}`, `{"enabled":true} {}`, strings.Repeat(" ", 1024) + `{"enabled":true}`} {
		request := httptest.NewRequestWithContext(t.Context(), "POST", "/peer-fault", strings.NewReader(raw))
		request.Host = "127.0.0.1:8443"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 400 || calls != 0 {
			t.Fatal("invalid fault input reached a backend or changed control state")
		}
	}
	for _, test := range []struct{ method, host, path string }{
		{"POST", "peer-a.test:8443", "/peer-fault"},
		{"GET", "peer-a.test:8443", "/peer-pages"},
		{"POST", "127.0.0.1:8443", "/peer-fault?host=peer-a"},
		{"POST", "127.0.0.1:8443", "https://untrusted/peer-fault"},
	} {
		request := httptest.NewRequestWithContext(t.Context(), test.method, test.path, strings.NewReader(`{"enabled":true}`))
		request.Host = test.host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 404 || calls != 0 {
			t.Fatal("fault control crossed its fixed fixture boundary")
		}
	}
	request := httptest.NewRequestWithContext(t.Context(), "POST", "/ard/v1/search", strings.NewReader(`{"federation":"none","pageToken":"valid-control"}`))
	request.Host = "peer-a.test:8443"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || calls != 1 {
		t.Fatal("rejected control enabled a continuation fault")
	}
}

func TestPeerInitialFaultIsExplicitAndRestorable(t *testing.T) {
	t.Parallel()
	calls := 0
	handler := peerHandler(peerRoundTrip(func(_ *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody}, nil
	}))
	serve := func(host, path, raw string) int {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(raw))
		request.Host = host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}
	if serve("127.0.0.1:8443", "/peer-fault", `{"enabled":true,"initial":true}`) != 200 ||
		serve("peer-a.test:8443", "/ard/v1/search", `{"federation":"none"}`) != 503 || calls != 0 {
		t.Fatal("initial replay fault did not block the real upstream")
	}
	if serve("peer-b.test:8443", "/ard/v1/search", `{"federation":"none"}`) != 200 || calls != 1 {
		t.Fatal("A fault crossed into B")
	}
	if serve("127.0.0.1:8443", "/peer-fault", `{"enabled":false}`) != 200 ||
		serve("peer-a.test:8443", "/ard/v1/search", `{"federation":"none"}`) != 200 || calls != 2 {
		t.Fatal("initial replay fault did not recover")
	}
}

func TestPeerEmbeddingFixtureIsBoundedAndNotPeerFacing(t *testing.T) {
	t.Parallel()
	handler := peerHandler(peerRoundTrip(func(_ *http.Request) (*http.Response, error) {
		t.Fatal("embedding control contacted a Catalog")
		return nil, errors.New("unexpected backend")
	}))
	valid := `{"model":"peer-pagination","input":["first","second"],"encoding_format":"float"}`
	for _, test := range []struct {
		name, host, path, body, token string
		status                        int
	}{
		{"valid", "127.0.0.1:8443", "/v1/embeddings", valid, "", 200},
		{"peer", "peer-a.test:8443", "/v1/embeddings", valid, "", 404},
		{"query", "127.0.0.1:8443", "/v1/embeddings?x=y", valid, "", 404},
		{"credentials", "127.0.0.1:8443", "/v1/embeddings", valid, "Bearer private", 400},
		{"wrong-model", "127.0.0.1:8443", "/v1/embeddings", strings.ReplaceAll(valid, "peer-pagination", "other"), "", 400},
		{"empty", "127.0.0.1:8443", "/v1/embeddings", `{"model":"peer-pagination","input":[],"encoding_format":"float"}`, "", 400},
		{"duplicate", "127.0.0.1:8443", "/v1/embeddings", `{"model":"peer-pagination","model":"peer-pagination"}`, "", 400},
		{"large-input", "127.0.0.1:8443", "/v1/embeddings", strings.ReplaceAll(valid, "first", strings.Repeat("x", 8193)), "", 413},
		{"large-body", "127.0.0.1:8443", "/v1/embeddings", strings.Repeat(" ", 256<<10) + valid, "", 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, test.path, strings.NewReader(test.body))
			request.Host = test.host
			request.Header.Set("Authorization", test.token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d; want %d", response.Code, test.status)
			}
			if test.status == 200 {
				var actual struct {
					Model string `json:"model"`
					Data  []struct {
						Index     int       `json:"index"`
						Embedding []float64 `json:"embedding"`
					} `json:"data"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil || actual.Model != "peer-pagination" || len(actual.Data) != 2 {
					t.Fatal("embedding fixture response invalid")
				}
				for i, row := range actual.Data {
					if row.Index != i || len(row.Embedding) != 3 || row.Embedding[0] != 1 || row.Embedding[1] != 0 || row.Embedding[2] != 0 {
						t.Fatal("fixed equal-rank vectors changed")
					}
				}
			}
		})
	}
}

func TestPeerPageCeilingPreservesRealContinuationAndOtherHosts(t *testing.T) {
	t.Parallel()
	var forwarded []map[string]any
	handler := peerHandler(peerRoundTrip(func(request *http.Request) (*http.Response, error) {
		var value map[string]any
		if err := json.NewDecoder(request.Body).Decode(&value); err != nil {
			t.Fatal(err)
		}
		forwarded = append(forwarded, value)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"results":[],"pageToken":"actual-peer-token"}`))}, nil
	}))
	serve := func(host, path, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
		request.Host = host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	for _, raw := range []string{`{"enabled":false,"pageSize":-1}`, `{"enabled":false,"pageSize":51}`, `{"enabled":false,"pageSize":"1"}`} {
		if serve("127.0.0.1:8443", "/peer-fault", raw).Code != 400 {
			t.Fatal("invalid ceiling accepted")
		}
	}
	if serve("127.0.0.1:8443", "/peer-fault", `{"enabled":false,"pageSize":1}`).Code != 200 {
		t.Fatal("page ceiling control failed")
	}
	body := `{"query":{"text":"private query"},"federation":"none","pageSize":50,"pageToken":"opaque-peer-token"}`
	for _, host := range []string{"peer-a.test:8443", "peer-b.test:8443"} {
		response := serve(host, "/ard/v1/search", body)
		if response.Code != 200 || response.Body.String() != `{"results":[],"pageToken":"actual-peer-token"}` {
			t.Fatal("page ceiling fabricated or changed an upstream response")
		}
	}
	serve("127.0.0.1:8443", "/peer-fault", `{"enabled":false,"pageSize":0}`)
	if serve("peer-a.test:8443", "/ard/v1/search", body).Code != 200 || len(forwarded) != 3 {
		t.Fatal("page ceiling restoration did not reach the Catalog")
	}
	for i, value := range forwarded {
		want := float64(50)
		if i == 0 {
			want = 1
		}
		query, ok := value["query"].(map[string]any)
		if !ok || value["pageSize"] != want || value["pageToken"] != "opaque-peer-token" || value["federation"] != "none" || query["text"] != "private query" {
			t.Fatal("page ceiling changed query, token or another host; or failed to restore")
		}
	}
}

func TestKubernetesPeerRoutesAndOfflineRecovery(t *testing.T) {
	routes, err := kubernetesPeerRoutes("owned", "other")
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"owned", "owned"}, {"owned", ""}, {"owned", "../other"}, {"owned", "Other"}, {"owned", strings.Repeat("x", 64)}} {
		if _, err := kubernetesPeerRoutes(pair[0], pair[1]); err == nil {
			t.Fatal("unsafe peer namespace accepted")
		}
	}
	calls := 0
	handler := routedPeerHandler(peerRoundTrip(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.Host != "catalog.other.svc.cluster.local:8080" || request.Host != "peer-a.owned.svc.cluster.local:8443" {
			t.Fatal("peer did not preserve exact origin and upstream")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Etag": {`"owned"`}}, Body: io.NopCloser(strings.NewReader("retained bytes"))}, nil
	}), routes)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/.well-known/ard.json", nil)
	request.Host = "peer-a.owned.svc.cluster.local:8443"
	healthy := httptest.NewRecorder()
	handler.ServeHTTP(healthy, request.Clone(t.Context()))
	if healthy.Code != http.StatusOK || healthy.Body.String() != "retained bytes" || healthy.Header().Get("ETag") != `"owned"` {
		t.Fatal("valid peer bytes/status/validator changed")
	}
	for _, body := range []string{`{"enabled":true,"offline":true}`, `{"enabled":false}`} {
		control := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/peer-fault", strings.NewReader(body))
		control.Host = "127.0.0.1:8443"
		reply := httptest.NewRecorder()
		handler.ServeHTTP(reply, control)
		if reply.Code != http.StatusOK {
			t.Fatal("owned fault control failed")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request.Clone(t.Context()))
		want := http.StatusOK
		if strings.Contains(body, "offline") {
			want = http.StatusServiceUnavailable
		}
		if response.Code != want {
			t.Fatal("peer offline/recovery status differs")
		}
	}
	if calls != 2 {
		t.Fatal("offline peer made an upstream request")
	}
	request.Host = "peer-a.unowned.svc.cluster.local:8443"
	reply := httptest.NewRecorder()
	handler.ServeHTTP(reply, request)
	if reply.Code != http.StatusNotFound || calls != 2 {
		t.Fatal("unselected peer reached an upstream")
	}
}
