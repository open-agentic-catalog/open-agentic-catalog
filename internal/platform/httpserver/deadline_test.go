package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

type deadlineBlockingJSON struct{ encoded *bool }

func (value deadlineBlockingJSON) MarshalJSON() ([]byte, error) {
	*value.encoded = true
	time.Sleep(20 * time.Millisecond)
	return []byte(`{"result":"late"}`), nil
}

func TestCommitmentWriterCommitsExactlyOnceAndPreservesControllerAccess(t *testing.T) {
	underlying := httptest.NewRecorder()
	writer := &commitmentWriter{ResponseWriter: underlying}
	writer.WriteHeader(http.StatusAccepted)
	writer.WriteHeader(http.StatusCreated)
	if _, err := writer.Write([]byte("accepted")); err != nil {
		t.Fatal(err)
	}
	if underlying.Code != http.StatusAccepted || underlying.Body.String() != "accepted" || writer.Unwrap() != underlying {
		t.Fatalf("committed writer = %d %q, unwrap=%T", underlying.Code, underlying.Body.String(), writer.Unwrap())
	}

	implicit := httptest.NewRecorder()
	if _, err := (&commitmentWriter{ResponseWriter: implicit}).Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if implicit.Code != http.StatusOK || implicit.Body.String() != "ok" {
		t.Fatalf("implicit response = %d %q", implicit.Code, implicit.Body.String())
	}
}

func TestProductWorkDeadlineCoversProductRoutesAndPreservesCommittedResponses(t *testing.T) {
	metrics := observability.New(time.Now)
	router := http.NewServeMux()
	cancelled := make(chan struct{}, 1)
	router.HandleFunc("GET /api/v1/slow", func(writer http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
		cancelled <- struct{}{}
	})
	router.HandleFunc("GET /api/v1/committed", func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusAccepted)
		<-request.Context().Done()
	})
	router.HandleFunc("POST /ard/v1/search", func(http.ResponseWriter, *http.Request) {})
	handler := productWorkDeadline(router, 10*time.Millisecond, metrics, router)

	control := httptest.NewRecorder()
	handler.ServeHTTP(control, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/slow", nil))
	if control.Code != http.StatusServiceUnavailable {
		t.Fatalf("control deadline status = %d, body = %s", control.Code, control.Body.String())
	}
	var problem Problem
	if err := json.Unmarshal(control.Body.Bytes(), &problem); err != nil || problem.Code != catalog.CodeRequestDeadlineExceeded {
		t.Fatalf("control deadline problem = %#v, %v", problem, err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("product request cancellation did not reach the handler")
	}

	committed := httptest.NewRecorder()
	handler.ServeHTTP(committed, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/committed", nil))
	if committed.Code != http.StatusAccepted || committed.Body.Len() != 0 {
		t.Fatalf("committed response was replaced: %d %s", committed.Code, committed.Body.String())
	}

	if rendered := string(metrics.Render(context.Background())); !strings.Contains(rendered, "open_agentic_catalog_http_request_deadlines_total 2") {
		t.Fatalf("deadline metric missing: %s", rendered)
	}
}

func TestProductWorkDeadlineUsesARDContractAndEarlierParentDeadline(t *testing.T) {
	router := http.NewServeMux()
	router.HandleFunc("POST /ard/v1/search", func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	})
	handler := productWorkDeadline(router, time.Second, nil, router)
	parent, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequestWithContext(parent, http.MethodPost, "/ard/v1/search", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("ARD deadline status = %d, body = %s", response.Code, response.Body.String())
	}
	var problem ardProblem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.ErrorCode != catalog.CodeRequestDeadlineExceeded {
		t.Fatalf("ARD deadline problem = %#v, %v", problem, err)
	}
}

func TestProductWorkDeadlineExcludesOperationalPreflightAndUnmatchedRoutes(t *testing.T) {
	router := http.NewServeMux()
	router.HandleFunc("GET /livez", func(writer http.ResponseWriter, request *http.Request) {
		if _, hasDeadline := request.Context().Deadline(); hasDeadline {
			t.Error("operational route received a product deadline")
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	router.HandleFunc("GET /api/v1/value", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodOptions {
			if _, hasDeadline := request.Context().Deadline(); hasDeadline {
				t.Error("preflight received a product deadline")
			}
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	handler := productWorkDeadline(router, time.Second, nil, router)

	for _, request := range []*http.Request{
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/livez", nil),
		httptest.NewRequestWithContext(context.Background(), http.MethodOptions, "/api/v1/value", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent && response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("excluded route %s %s returned %d", request.Method, request.URL.Path, response.Code)
		}
	}
	if usesProductWorkDeadline(router, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/missing", nil)) {
		t.Fatal("unmatched route received a product deadline")
	}
}

func TestProductWorkDeadlineClassifiesEveryPublicSurface(t *testing.T) {
	router := http.NewServeMux()
	for _, pattern := range []string{
		"GET /api/v1/value",
		"POST /ard/v1/search",
		"GET /ard/v1/agents",
		"GET /.well-known/ard.json",
		"GET /.well-known/ai-catalog.json",
		"GET /public/v1/artifacts/{artifactId}",
		"GET /livez",
		"GET /readyz",
		"GET /version",
		"GET /metrics",
		"GET /openapi/control-v1.yaml",
	} {
		router.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
	}
	tests := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{name: "control API", method: http.MethodGet, path: "/api/v1/value", want: true},
		{name: "ARD search", method: http.MethodPost, path: "/ard/v1/search", want: true},
		{name: "ARD list", method: http.MethodGet, path: "/ard/v1/agents", want: true},
		{name: "ARD well-known", method: http.MethodGet, path: "/.well-known/ard.json", want: true},
		{name: "AI Catalog well-known", method: http.MethodGet, path: "/.well-known/ai-catalog.json", want: true},
		{name: "public artifact", method: http.MethodGet, path: "/public/v1/artifacts/document", want: true},
		{name: "liveness", method: http.MethodGet, path: "/livez"},
		{name: "readiness", method: http.MethodGet, path: "/readyz"},
		{name: "version", method: http.MethodGet, path: "/version"},
		{name: "metrics", method: http.MethodGet, path: "/metrics"},
		{name: "OpenAPI", method: http.MethodGet, path: "/openapi/control-v1.yaml"},
		{name: "preflight", method: http.MethodOptions, path: "/api/v1/value"},
		{name: "unmatched product-looking route", method: http.MethodGet, path: "/api/v1/missing"},
		{name: "unmatched public-looking route", method: http.MethodGet, path: "/public/v1/missing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(context.Background(), test.method, test.path, nil)
			if got := usesProductWorkDeadline(router, request); got != test.want {
				t.Fatalf("usesProductWorkDeadline(%s %s) = %t, want %t", test.method, test.path, got, test.want)
			}
		})
	}
}

func TestJSONResponseDoesNotCommitSuccessAfterDeadlineDuringEncoding(t *testing.T) {
	for _, surface := range []string{"control", "ARD"} {
		for _, phase := range []string{"before encoding", "during encoding"} {
			t.Run(surface+"/"+phase, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					server := &Server{}
					router := http.NewServeMux()
					path := "/api/v1/encode"
					if surface == "ARD" {
						path = "/ard/v1/agents"
					}
					encoded := false
					router.HandleFunc("GET "+path, func(writer http.ResponseWriter, request *http.Request) {
						if phase == "before encoding" {
							time.Sleep(20 * time.Millisecond)
						}
						value := deadlineBlockingJSON{encoded: &encoded}
						if surface == "ARD" {
							server.respondARDJSON(writer, request, "application/json", value)
						} else {
							server.respondJSON(writer, request, http.StatusOK, value)
						}
					})
					response := httptest.NewRecorder()
					productWorkDeadline(router, 5*time.Millisecond, nil, router).ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
					if response.Code != http.StatusServiceUnavailable || encoded != (phase == "during encoding") {
						t.Fatalf("deadline response: status=%d encoded=%t body=%s", response.Code, encoded, response.Body.String())
					}
					var body map[string]json.RawMessage
					if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
						t.Fatal(err)
					}
					codeKey, mediaType := "code", "application/problem+json"
					if surface == "ARD" {
						codeKey, mediaType = "errorCode", "application/json"
						if len(body) != 2 || body["message"] == nil {
							t.Fatalf("unexpected ARD error envelope: %s", response.Body.String())
						}
					}
					var code catalog.ErrorCode
					if err := json.Unmarshal(body[codeKey], &code); err != nil || code != catalog.CodeRequestDeadlineExceeded || body["result"] != nil {
						t.Fatalf("deadline response leaked success or wrong error: %s", response.Body.String())
					}
					if response.Header().Get("Content-Type") != mediaType || response.Header().Get("Cache-Control") != "no-store" {
						t.Fatalf("unexpected deadline headers: %v", response.Header())
					}
				})
			})
		}
	}
}

func TestProductWorkDeadlineReleasesCompletedRequest(t *testing.T) {
	t.Parallel()
	var handlerError func() error
	router := http.NewServeMux()
	router.HandleFunc("GET /api/v1/complete", func(w http.ResponseWriter, r *http.Request) {
		handlerError = r.Context().Err
		if _, ok := r.Context().Deadline(); !ok {
			t.Fatal("missing product deadline")
		}
		w.WriteHeader(http.StatusAccepted)
	})
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/complete", nil)
	w := httptest.NewRecorder()
	productWorkDeadline(router, time.Hour, nil, router).ServeHTTP(w, r)
	if w.Code != http.StatusAccepted || handlerError == nil || !errors.Is(handlerError(), context.Canceled) || r.Context().Err() != nil {
		t.Fatal("completed request did not release its owned deadline context")
	}
}
