//go:build functional

// Real TLS, process and publication boundaries belong to the functional selection.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

func discoveryFixtureCA(t *testing.T, server *httptest.Server) string {
	t.Helper()
	path := filepath.Join(canonicalTempDir(t), "api-ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDiscoveryHTTPSProcessKeepsCredentialsAndLocationsInert(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	binary := buildCatalogctlProcessBinary(t, ctx)
	var requests, declared atomic.Int32
	var malformed atomic.Bool
	location := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { declared.Add(1) }))
	defer location.Close()
	continuation := " opaque+/= &?\u00e9 "
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Forwarded") != "" || r.Header.Get("X-Forwarded-Host") != "" || r.Header.Get("If-None-Match") != "" {
			t.Error("ambient authority on anonymous request", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "public-request")
		w.Header().Set("ETag", `"generation-1"`)
		w.Header().Set("Set-Cookie", "ignored=credential")
		if malformed.Load() {
			_, _ = io.WriteString(w, `{"entries":["untrusted-body-secret`)
			return
		}
		entry := map[string]any{"identifier": "urn:air:agent:example:reader", "displayName": "Reader\x1b[31m\u202e", "type": "application/a2a+json", "url": location.URL, "version": "", "extensions": map[string]any{"future": true}}
		var body any
		switch r.URL.Path {
		case "/.well-known/ard.json":
			body = map[string]any{"entries": []any{entry}}
		case "/ard/v1/agents":
			if r.URL.Query().Get("pageToken") != continuation || r.URL.Query().Get("filter") != " displayName = 'Reader' " || r.URL.Query().Get("orderBy") != "updatedAt DESC" || r.URL.Query().Get("pageSize") != "1" {
				t.Error("continuation selection changed", r.URL.RawQuery)
			}
			body = map[string]any{"items": []any{}, "total": 2, "pageToken": continuation}
		default:
			t.Error("unexpected private or entry request", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	ca := discoveryFixtureCA(t, server)
	environment := append(catalogctlProcessEnvironment(t), "CATALOG_REMOTE_TOKEN=ambient-secret", "CATALOG_REMOTE_TOKEN_FILE=/unreadable/secret", "CATALOG_REMOTE_LOGIN_FILE=/unreadable/login", "CATALOG_REMOTE_BASE_URL=https://wrong.invalid", "CATALOG_REMOTE_CA_FILE=/unreadable/ca", "CATALOG_CONFIG=/unreadable/runtime", "HTTPS_PROXY=http://127.0.0.1:1", "HTTP_PROXY=http://127.0.0.1:1")
	for _, action := range []string{"manifest", "list"} {
		args := []string{"discovery", action, "--base-url", server.URL, "--ca-file", ca}
		if action == "list" {
			args = append(args, "--page-size", "1", "--page-token", continuation, "--filter", " displayName = 'Reader' ", "--order-by", "updatedAt DESC")
		}
		before := requests.Load()
		process := startCatalogctlProcess(t, ctx, binary, environment, args...)
		out, diagnostic := process.wait(t, 0)
		if len(diagnostic) != 0 || bytes.ContainsRune(out, '\x1b') || bytes.Contains(out, []byte("\u202e")) || requests.Load() != before+1 {
			t.Fatal("unsafe output or additional requests", string(out), string(diagnostic), requests.Load())
		}
		var envelope remoteEnvelope
		if err := json.Unmarshal(out, &envelope); err != nil || envelope.Meta.RequestID != "public-request" || envelope.Meta.ETag != `"generation-1"` {
			t.Fatal(string(out), err)
		}
		if action == "list" {
			var page struct {
				Items []any  `json:"items"`
				Token string `json:"pageToken"`
			}
			if err := json.Unmarshal(envelope.Data, &page); err != nil || len(page.Items) != 0 || page.Token != continuation {
				t.Fatal("empty continuation page lost", string(out), err)
			}
		} else if !bytes.Contains(envelope.Data, []byte(`"future":true`)) || !bytes.Contains(envelope.Data, []byte(`"version":""`)) {
			t.Fatal("metadata projection lost exact fields", string(out))
		}
	}
	for _, flag := range []string{"--token", "--token-file", "--login-file", "--issuer-ca-file"} {
		before := requests.Load()
		process := startCatalogctlProcess(t, ctx, binary, environment, "discovery", "manifest", "--base-url", server.URL, "--ca-file", "/not-read", flag, "private-secret\x1b")
		out, diagnostic := process.wait(t, 1)
		assertProcessTerminalFailure(t, out, diagnostic, "remote", "private-secret", "/not-read", "\x1b")
		if !bytes.Contains(diagnostic, []byte("discovery: invalid flags")) {
			t.Fatal("credential admission did not precede CA-file handling", string(diagnostic))
		}
		if requests.Load() != before {
			t.Fatal("explicit credentials reached network")
		}
	}
	if declared.Load() != 0 {
		t.Fatal("declared location was fetched")
	}
	malformed.Store(true)
	before := requests.Load()
	failed := startCatalogctlProcess(t, ctx, binary, environment, "discovery", "manifest", "--base-url", server.URL, "--ca-file", ca)
	failedOutput, failedDiagnostic := failed.wait(t, 1)
	var failure remoteAPIError
	if len(failedOutput) != 0 || json.Unmarshal(failedDiagnostic, &failure) != nil || failure.RequestID != "public-request" || failure.Status != http.StatusOK || failure.Code != "invalid_response" || requests.Load() != before+1 || bytes.Contains(failedDiagnostic, []byte("untrusted-body-secret")) {
		t.Fatal("malformed reply lost safe correlation or leaked its body", string(failedOutput), string(failedDiagnostic))
	}
	process := startCatalogctlProcess(t, ctx, binary, environment, "discovery", "list", "--help")
	out, diagnostic := process.wait(t, 0)
	if string(out) != discoveryUsage || len(diagnostic) != 0 {
		t.Fatal("help changed", string(out), string(diagnostic))
	}
}

func TestDiscoveryRealTransportRefusesTLSRedirectBoundsAndDeadline(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetHits.Add(1) }))
	defer target.Close()
	for _, mode := range []string{"valid", "untrusted", "hostname", "redirect", "headers", "truncated", "body", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			var hits atomic.Int32
			cancelled := make(chan struct{}, 1)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				switch mode {
				case "redirect":
					w.Header().Set("Location", target.URL)
					w.WriteHeader(http.StatusTemporaryRedirect)
				case "headers":
					w.Header().Set("X-Oversized", strings.Repeat("h", 40<<10))
				case "truncated":
					w.Header().Set("Content-Length", "200")
				case "body", "cancelled":
					w.WriteHeader(http.StatusOK)
					if err := http.NewResponseController(w).Flush(); err != nil {
						t.Error(err)
						return
					}
					<-r.Context().Done()
					cancelled <- struct{}{}
					return
				}
				_, _ = io.WriteString(w, `{"entries":[]}`)
			}))
			defer server.Close()
			ca := discoveryFixtureCA(t, server)
			origin := server.URL
			if mode == "hostname" {
				origin = strings.Replace(origin, "127.0.0.1", "localhost", 1)
			}
			args := []string{"manifest", "--base-url", origin, "--timeout", "200ms"}
			if mode != "body" {
				args[4] = "2s"
			}
			if mode != "untrusted" {
				args = append(args, "--ca-file", ca)
			}
			var out bytes.Buffer
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			err := runDiscovery(ctx, args, &out)
			if mode == "valid" {
				if err != nil || !bytes.Contains(out.Bytes(), []byte(`"entries":[]`)) || hits.Load() != 1 {
					t.Fatal(out.String(), err)
				}
				for _, short := range []bool{false, true} {
					if err := runDiscovery(t.Context(), args, remoteBrokenWriter{short}); err == nil || !strings.Contains(err.Error(), "cannot write output") {
						t.Fatal("output failure was lost", err)
					}
				}
				return
			}
			if err == nil || out.Len() != 0 || strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), target.URL) {
				t.Fatal("transport refusal leaked or succeeded", out.String(), err)
			}
			if mode == "untrusted" || mode == "hostname" || mode == "cancelled" {
				if hits.Load() != 0 {
					t.Fatal("bad TLS/cancellation reached handler")
				}
			} else if hits.Load() != 1 {
				t.Fatal("request was retried", hits.Load())
			}
			if mode == "body" {
				select {
				case <-cancelled:
				case <-time.After(time.Second):
					t.Fatal("deadline did not cancel server request")
				}
			}
		})
	}
	if targetHits.Load() != 0 {
		t.Fatal("redirect followed")
	}
}

func TestDiscoveryProcessSignalCancelsReadWithoutReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	binary := buildCatalogctlProcessBinary(t, ctx)
	started, ended := make(chan struct{}, 1), make(chan struct{}, 1)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
			return
		}
		started <- struct{}{}
		<-r.Context().Done()
		ended <- struct{}{}
	}))
	defer server.Close()
	process := startCatalogctlProcess(t, ctx, binary, catalogctlProcessEnvironment(t), "discovery", "manifest", "--base-url", server.URL)
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("read never started")
	}
	if err := process.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	out, diagnostic := process.wait(t, 1)
	assertProcessTerminalFailure(t, out, diagnostic, "remote")
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("process cancellation left request alive")
	}
	if hits.Load() != 1 {
		t.Fatal("cancelled request replayed")
	}
}

func TestDiscoveryProcessReadsOnlyActivePublicationAndExactContinuation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	binary := buildCatalogctlProcessBinary(t, ctx)
	runtime := buildSmokeTestRuntime(t)
	defer runtime.Close()
	setup := httptest.NewServer(runtime.HTTP.Handler())
	defer setup.Close()
	options, err := parseSmokeOptions([]string{"--base-url", setup.URL, "--run-id", "anonymous-cli"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := executeSmoke(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	admin := &smokeHTTPClient{baseURL: setup.URL, client: setup.Client()}
	retained, err := admin.request(ctx, "read fixture Resource", http.MethodGet, "/api/v1/resources/"+report.Resources[0].ResourceID, "local-admin", "", "", nil, http.StatusOK)
	if err != nil {
		t.Fatal(err)
	}
	var resource struct {
		NamespaceID string `json:"namespaceId"`
	}
	if json.Unmarshal(retained.Body, &resource) != nil || resource.NamespaceID == "" {
		t.Fatal("missing fixture namespace")
	}
	const draftIdentifier = "urn:example:anonymous-cli:unpublished"
	_, err = admin.request(ctx, "create unpublished draft", http.MethodPost, "/api/v1/resources", "local-admin", "anonymous-cli-draft", "", map[string]any{"namespaceId": resource.NamespaceID, "kind": "agent", "canonicalIdentifier": draftIdentifier}, http.StatusCreated)
	if err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.URL.Path != "/.well-known/ard.json" && r.URL.Path != "/ard/v1/agents" {
			t.Error("private authority or non-public request", r.URL.Path)
		}
		runtime.HTTP.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	ca := discoveryFixtureCA(t, server)
	environment := catalogctlProcessEnvironment(t)
	call := func(action string, extra ...string) remoteEnvelope {
		t.Helper()
		before := reads.Load()
		args := append([]string{"discovery", action, "--base-url", server.URL, "--ca-file", ca}, extra...)
		process := startCatalogctlProcess(t, ctx, binary, environment, args...)
		out, diagnostic := process.wait(t, 0)
		var envelope remoteEnvelope
		if len(diagnostic) != 0 || json.Unmarshal(out, &envelope) != nil || envelope.Meta.RequestID == "" || action == "manifest" && envelope.Meta.ETag == "" || reads.Load() != before+1 {
			t.Fatal("publication process result", string(out), string(diagnostic))
		}
		return envelope
	}
	manifest := call("manifest")
	var inventory struct {
		Entries []struct {
			Identifier string `json:"identifier"`
		} `json:"entries"`
	}
	if json.Unmarshal(manifest.Data, &inventory) != nil || len(inventory.Entries) != 4 {
		t.Fatal("wrong active manifest", string(manifest.Data))
	}
	for _, resource := range report.Resources {
		if !bytes.Contains(manifest.Data, []byte(resource.Identifier)) {
			t.Fatal("released fixture missing")
		}
	}
	selection := []string{"--page-size", "1", "--order-by", "displayName DESC", "--filter", "type = 'application/ai-registry+json'"}
	filtered := call("list", selection...)
	if !bytes.Contains(filtered.Data, []byte(`"total":1`)) {
		t.Fatal("filter was not applied", string(filtered.Data))
	}
	selection = []string{"--page-size", "1", "--order-by", "displayName DESC"}
	seen := make([]string, 0, 4)
	var token string
	for range 4 {
		args := append([]string{}, selection...)
		if token != "" {
			args = append(args, "--page-token", token)
		}
		page := call("list", args...)
		var value struct {
			Items []struct {
				Identifier string `json:"identifier"`
			} `json:"items"`
			Total int    `json:"total"`
			Token string `json:"pageToken"`
		}
		if json.Unmarshal(page.Data, &value) != nil || len(value.Items) != 1 || value.Total != 4 {
			t.Fatal("wrong page", string(page.Data))
		}
		seen = append(seen, value.Items[0].Identifier)
		token = value.Token
	}
	if token != "" {
		t.Fatal("unexpected trailing continuation")
	}
	identifiers := map[string]bool{}
	for _, id := range seen {
		if identifiers[id] {
			t.Fatal("duplicate continuation entry")
		}
		identifiers[id] = true
	}
	for _, entry := range inventory.Entries {
		if !identifiers[entry.Identifier] {
			t.Fatal("continuation omitted active entry")
		}
	}
	first := call("list", "--page-size", "1")
	var firstPage struct {
		Token string `json:"pageToken"`
	}
	if json.Unmarshal(first.Data, &firstPage) != nil || firstPage.Token == "" {
		t.Fatal("missing continuation")
	}
	// Build and activate a real successor; old tokens must not silently restart.
	candidate, err := admin.request(ctx, "new candidate", http.MethodPost, "/api/v1/publication/snapshots", "local-admin", "anonymous-cli-successor", "", map[string]any{"host": "127.0.0.1"}, http.StatusCreated)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(candidate.Body, &snapshot) != nil || snapshot.ID == "" {
		t.Fatal("missing successor")
	}
	_, err = admin.request(ctx, "activate successor", http.MethodPost, "/api/v1/publication/snapshots/"+snapshot.ID+":activate", "local-admin", "anonymous-cli-activate", candidate.Header.Get("ETag"), map[string]any{"host": "127.0.0.1", "reason": "Owned anonymous CLI continuation qualification"}, http.StatusOK)
	if err != nil {
		t.Fatal(err)
	}
	before := reads.Load()
	process := startCatalogctlProcess(t, ctx, binary, environment, "discovery", "list", "--base-url", server.URL, "--ca-file", ca, "--page-size", "1", "--page-token", firstPage.Token)
	out, diagnostic := process.wait(t, 1)
	var refusal remoteAPIError
	if len(out) != 0 || json.Unmarshal(diagnostic, &refusal) != nil || refusal.Status != 400 || refusal.Code != "INVALID_ARGUMENT" || reads.Load() != before+1 {
		t.Fatal("stale continuation restarted or not refused", string(out), string(diagnostic))
	}
	current := call("manifest")
	if bytes.Contains(current.Data, []byte(draftIdentifier)) || json.Unmarshal(current.Data, &inventory) != nil || len(inventory.Entries) != 4 {
		t.Fatal("draft entered rebuilt publication", string(current.Data))
	}
	for _, entry := range inventory.Entries {
		if !identifiers[entry.Identifier] {
			t.Fatal("successor changed published membership")
		}
	}
	_, err = admin.request(ctx, "withdraw publication", http.MethodPost, "/api/v1/publication:unpublish", "local-admin", "anonymous-cli-unpublish", "", map[string]any{"host": "127.0.0.1", "reason": "Owned CLI withdrawal qualification"}, http.StatusOK)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"manifest", "list"} {
		before := reads.Load()
		process := startCatalogctlProcess(t, ctx, binary, environment, "discovery", action, "--base-url", server.URL, "--ca-file", ca)
		out, diagnostic := process.wait(t, 1)
		var refusal remoteAPIError
		if len(out) != 0 || json.Unmarshal(diagnostic, &refusal) != nil || refusal.Status != 404 || refusal.Code != "NOT_FOUND" || reads.Load() != before+1 {
			t.Fatal("withdrawn publication did not refuse", string(out), string(diagnostic))
		}
	}
}

func TestDiscoveryExistingHostRefusals(t *testing.T) {
	for _, mode := range []string{"unknown", "disabled", "authenticated", "inactive"} {
		t.Run(mode, func(t *testing.T) {
			runtime := buildSmokeTestRuntime(t, func(c *config.Config) {
				switch mode {
				case "unknown":
					c.Discovery.Hosts[0].Host = "unselected.example"
				case "inactive":
				case "disabled", "authenticated":
					c.Discovery.Hosts[0].Mode = mode
				}
			})
			defer runtime.Close()
			server := httptest.NewServer(runtime.HTTP.Handler())
			defer server.Close()
			for _, action := range []string{"manifest", "list"} {
				var out bytes.Buffer
				err := runDiscovery(t.Context(), []string{action, "--base-url", server.URL}, &out)
				var failure remoteAPIError
				status, code := 404, "NOT_FOUND"
				if mode == "authenticated" {
					status, code = 401, "UNAUTHENTICATED"
				}
				if !errors.As(err, &failure) || failure.Status != status || failure.Code != code || failure.RequestID == "" || out.Len() != 0 {
					t.Fatal(action, mode, out.String(), err)
				}
			}
		})
	}
}

// Keep the import used by the typed metadata comparison below explicit.
var _ = reflect.DeepEqual
