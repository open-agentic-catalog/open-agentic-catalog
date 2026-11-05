package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestControlHeaderAdmission(t *testing.T) {
	for _, name := range []string{"Authorization", "Content-Type", "Idempotency-Key", "If-Match", "Accept"} {
		for _, path := range []string{"/api/v1/namespaces", "/livez"} {
			for _, count := range []int{0, 1, 2} {
				r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
				for range count {
					r.Header.Add(name, "value")
				}
				called := false
				w := httptest.NewRecorder()
				controlHeaderAdmission(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(http.StatusNoContent) })).ServeHTTP(w, r)
				reject := path == "/api/v1/namespaces" && count > 1 && name != "Accept"
				if called == reject || (reject && w.Code != 400) || (!reject && w.Code != 204) {
					t.Fatalf("%s %s count=%d: called=%v status=%d", name, path, count, called, w.Code)
				}
			}
		}
	}
}
