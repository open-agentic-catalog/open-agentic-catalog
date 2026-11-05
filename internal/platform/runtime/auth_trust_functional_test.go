//go:build functional

package runtime

import (
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

func TestAPIRuntimeReportsOnlyLoadedOIDCTrustDigest(t *testing.T) {
	var issuer string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"issuer": issuer, "jwks_uri": issuer + "/keys"})
	}))
	defer server.Close()
	issuer = server.URL
	bundle := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	path := filepath.Join(t.TempDir(), "issuer-ca.pem")
	if err := os.WriteFile(path, bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := localRuntimeConfig()
	cfg.Auth = config.Auth{Mode: "oidc", Issuer: issuer, Audience: "catalog", TenancyMode: "dedicated", DedicatedTenantID: "00000000-0000-7000-8000-000000000001", TrustBundleFile: path}
	runtime, err := Build(t.Context(), BuildOptions{ConfigPath: writeRuntimeConfig(t, cfg), Role: config.RoleAPI, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	report := runtime.Config.Report()
	if !report.Auth.TrustBundleConfigured || report.Auth.TrustBundleDigest != fmt.Sprintf("sha256:%x", sha256.Sum256(bundle)) {
		t.Fatal("API diagnostics do not identify the startup-loaded trust bytes")
	}
}
