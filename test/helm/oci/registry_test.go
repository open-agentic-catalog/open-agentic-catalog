//go:build helm

package oci_test

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
)

// The registry stores every blob in memory and accepts only anonymous requests.
func TestAnonymousChartPulls(t *testing.T) {
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("registry received credentials")
			http.Error(w, "anonymous client required", http.StatusForbidden)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()

	cmd := exec.CommandContext(t.Context(), os.Getenv("PYTHON"), "-B", "test/helm/test_functional_oci.py", server.URL)
	cmd.Dir = "../../.."
	cmd.Env = append(cmd.Environ(), "PYTHONPATH=test/ci")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
}
