package demofixture

import (
	"bytes"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
	"net/http/httptest"
	"testing"
)

func TestArchiveHandlerAdmission(t *testing.T) {
	for _, tc := range []struct {
		method, path, header string
		status               int
	}{
		{"GET", "/repository-essentials.zip", "", 200}, {"POST", "/repository-essentials.zip", "", 405}, {"GET", "/plugin.json", "", 404}, {"GET", "/repository-essentials.zip?a=1", "", 404}, {"GET", "/repository-essentials.zip?", "", 404}, {"GET", "/repository-essentials.zip", "Authorization", 404}, {"GET", "/repository-essentials.zip", "Cookie", 404}, {"GET", "/%72epository-essentials.zip", "", 404},
	} {
		r := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
		if tc.header != "" {
			r.Header.Set(tc.header, "synthetic-private")
		}
		w := httptest.NewRecorder()
		Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%+v: %d", tc, w.Code)
		}
		if tc.status == 200 {
			raw, digest, _ := demodata.Archive(tc.path)
			if !bytes.Equal(w.Body.Bytes(), raw) || w.Header().Get("ETag") != `"`+digest+`"` || w.Header().Get("Content-Type") != catalog.PluginMediaZIP || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("wrong served archive")
			}
		}
	}
}
