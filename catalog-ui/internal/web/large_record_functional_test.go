//go:build functional

package web

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Exercise the fixed API transport with a detail larger than its default,
// preserving complete bytes when the operator selects a sufficient budget.
func TestLargeRecordConfiguredResponseBudget(t *testing.T) {
	data := `{"versionRevision":{"representations":[{"data":{"description":"` + strings.Repeat("x", 16<<20) + `"}}]}}`
	route := "/api/v1/catalog/resources/00000000-0000-7000-8000-000000000001"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/me":
			_, _ = io.WriteString(w, testAccess)
		case route:
			_, _ = io.WriteString(w, data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	for _, limit := range []int64{16 << 20, int64(len(data))} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			c := fixtureConfig(t, api.URL)
			c.ResponseBytes = limit
			s := newFixture(t, c)
			response := uiRequest(s, http.MethodGet, "/_catalog"+route, "", loginFixture(t, s))
			if limit < int64(len(data)) {
				if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), strings.Repeat("x", 100)) {
					t.Fatalf("unsafe overflow response: %d", response.Code)
				}
			} else if response.Code != http.StatusOK || sha256.Sum256(response.Body.Bytes()) != sha256.Sum256([]byte(data)) {
				t.Fatalf("configured exact detail failed: %d", response.Code)
			}
		})
	}
}
