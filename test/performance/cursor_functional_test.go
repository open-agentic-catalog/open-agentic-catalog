//go:build functional

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
)

// Exercise the actual Python acquisition and Go replay over local HTTP. The
// fixture verifies their wire binding and uses the real signed-cursor guard;
// it does not substitute for the separately owned PostgreSQL Catalog campaign.
func TestScaleTerminalCursorPythonAcquisitionAndGoReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	at := time.Date(2026, 10, 31, 12, 0, 0, 250000000, time.UTC)
	serviceClock := clock.NewFake(at)
	key, binding := []byte(strings.Repeat("k", 32)), "owned-terminal-reader-query"
	token, err := catalog.EncodeLiveCursor(catalog.LiveCursor{
		Surface: "control", Generation: "7", IndexedGeneration: "3",
		Position:  &catalog.CatalogSearchPosition{Score: 50, DisplayName: "Scale Local 000004", ResultID: "local:four"},
		ExpiresAt: at.Add(15 * time.Minute).Unix(),
	}, key, binding)
	if err != nil {
		t.Fatal("encode owned cursor")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/catalog/search" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer local-reader" {
			t.Error("acquisition or replay changed the request envelope")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error("malformed owned request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		want := map[string]any{"text": "Scale Local", "tags": []any{"terminal"}, "limit": float64(5)}
		if supplied, exists := body["cursor"]; exists {
			want["cursor"] = token
			if !reflect.DeepEqual(body, want) || supplied != token {
				t.Error("replay changed the frozen query or token")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if _, err := catalog.DecodeLiveCursor(token, "control", key, binding, serviceClock.Now()); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": string(catalog.ErrorCodeOf(err)), "detail": "live search cursor is invalid or expired; restart the search"})
				return
			}
			_, _ = w.Write([]byte(`{"generation":"7","items":[{"id":"five"},{"id":"six"}]}`))
			return
		}
		if !reflect.DeepEqual(body, want) {
			t.Error("Python acquisition changed the terminal query")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"generation": "7", "nextCursor": token,
			"items": []map[string]string{{"id": "zero"}, {"id": "one"}, {"id": "two"}, {"id": "three"}, {"id": "four"}},
		})
	}))
	defer server.Close()
	// Import maintained helpers without invoking their executable entry points.
	python := `import importlib.util, pathlib, sys
root = pathlib.Path.cwd()
def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module
scale = load("scale", root / "scale.py")
http = load("http_client", root.parent / "functional" / "http_client.py")
sys.stdout.write(scale.later_cursor(sys.argv[1], http, RuntimeError, terminal=True))
`
	command := exec.CommandContext(ctx, "python3", "-B", "-c", python, server.URL) // #nosec G204 -- fixed helper code and this test's owned loopback server URL, without a shell.
	acquired, err := command.Output()
	if err != nil || string(acquired) != token {
		t.Fatal("actual Python cursor acquisition failed or changed the token")
	}
	client := newClient()
	defer client.CloseIdleConnections()
	p := profile{Scenario: "scale", Port: serverPort(t, server), Scale: &scaleProfile{Items: 1000, Case: "terminal", Publisher: "00000000-0000-7000-8000-000000000010", Cursor: string(acquired)}}
	if _, err := workloadRequest(ctx, client, p); err != nil {
		t.Fatal("unchanged terminal replay failed at the issuance clock")
	}
	serviceClock.Advance(-2 * time.Second)
	_, err = workloadRequest(ctx, client, p)
	var status httpStatusError
	if !errors.As(err, &status) || string(status) != "400-invalid-live-cursor" {
		t.Fatal("backward fixture clock did not reach the real future-expiry refusal")
	}
	serviceClock.Advance(2 * time.Second)
	if _, err := workloadRequest(ctx, client, p); err != nil {
		t.Fatal("the same frozen token did not recover at the original fixture clock")
	}
}
