//go:build functional

package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/controlclient"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/operations"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/processinfo"
)

// Expected populations come from the reviewed contract, not the classifier.
// Wait for the recorded completion as well as the complete HTTP response.
func TestServiceActivityRealTrafficPartitionsAndCombinedListeners(t *testing.T) {
	process, err := processinfo.New()
	if err != nil {
		t.Fatal(err)
	}
	registry := observability.New(nil, process)
	api := newConfiguredHTTPServer(t, func(d *Dependencies) {
		d.Role = "all"
		d.Process = process
		d.Observability = registry
		d.Capabilities.ServiceActivity = CapabilityEnabled
		d.MCP = config.Defaults().MCP
		d.MCP.Enabled = true
		d.PublicBaseURL = "https://catalog.example.com"
		d.AuthIssuer = "https://issuer.example.com"
	})
	defer api.Close()
	worker, err := operations.New(operations.Dependencies{Process: process, Observability: registry, Readiness: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	auxiliary := httptest.NewServer(worker.Handler())
	defer auxiliary.Close()
	type population struct {
		count   uint64
		methods map[string]uint64
		classes map[string]uint64
	}
	expected := map[string]*population{}
	for _, name := range []string{"control_search", "control_explore", "observation_search", "administrative_live_search", "ard_search", "ard_explore", "ard_list", "discovery_documents", "http_mcp", "control_read", "control_command", "platform", "observer", "unknown_auxiliary"} {
		expected[name] = &population{methods: map[string]uint64{}, classes: map[string]uint64{}}
	}
	check := func() {
		t.Helper()
		s, err := registry.Activity(true)
		if err != nil {
			t.Fatal(err)
		}
		var total uint64
		for _, row := range s.HTTP.Families {
			want := expected[row.Family]
			if row.Completed != want.count {
				t.Fatalf("%s completed=%d want=%d", row.Family, row.Completed, want.count)
			}
			total += want.count
			for _, method := range row.Methods {
				if method.Completed != want.methods[method.Method] {
					t.Fatalf("%s/%s: %d want %d", row.Family, method.Method, method.Completed, want.methods[method.Method])
				}
			}
			for _, class := range row.StatusClasses {
				if class.Completed != want.classes[class.StatusClass] {
					t.Fatalf("%s/%s: %d want %d", row.Family, class.StatusClass, class.Completed, want.classes[class.StatusClass])
				}
			}
		}
		if s.HTTP.Completed != total || s.HTTP.ActiveCatalog != 0 {
			t.Fatalf("wrong totals: %+v", s.HTTP)
		}
	}
	perform := func(origin, method, path, body, token, key, family string, status int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, origin+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		response, err := api.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.StatusCode != status {
			t.Fatalf("%s %s = %d: %s %v", method, path, response.StatusCode, raw, err)
		}
		p := expected[family]
		p.count++
		bucket := method
		if method == "PUT" {
			bucket = "OTHER"
		}
		p.methods[bucket]++
		p.classes[strconv.Itoa(status/100)+"xx"]++
		var completed uint64
		for _, row := range expected {
			completed += row.count
		}
		activityAfterCompletion(t, registry, completed)
		check()
		return raw
	}
	// Probes, scrapes, auxiliary routes and Activity reads stay outside Catalog work.
	for _, path := range []string{"/livez", "/readyz", "/metrics"} {
		perform(api.URL, "GET", path, "", "", "", "platform", 200)
	}
	perform(auxiliary.URL, "GET", "/livez", "", "", "", "unknown_auxiliary", 200)
	for _, path := range []string{"/status", "/version"} {
		perform(auxiliary.URL, "GET", path, "", "", "", "unknown_auxiliary", 200)
	}
	for _, target := range []struct {
		method, path, family, token, body string
		status                            int
	}{
		{"GET", "/api/v1/namespaces", "control_read", "admin", "", 200},
		{"HEAD", "/api/v1/namespaces", "control_read", "admin", "", 200},
		{"GET", "/api/v1/namespaces", "control_read", "", "", 401},
		{"POST", "/api/v1/catalog/search", "control_search", "reader", "{}", 200},
		{"POST", "/api/v1/catalog/search", "control_search", "reader", "{", 400},
		{"POST", "/api/v1/catalog/explore", "control_explore", "reader", "{", 400},
		{"POST", "/api/v1/observations:search", "observation_search", "admin", "{", 400},
		{"POST", "/api/v1/federation/sources:live-search", "administrative_live_search", "admin", "{", 400},
		{"POST", "/ard/v1/search", "ard_search", "", "{", 400},
		{"POST", "/ard/v1/explore", "ard_explore", "", "{", 400},
		{"GET", "/ard/v1/agents", "ard_list", "", "", 404},
		{"GET", "/.well-known/ard.json", "discovery_documents", "", "", 404},
		{"GET", "/not-a-route?text=PRIVATE_QUERY", "unknown_auxiliary", "", "", 404},
		{"PUT", "/api/v1/namespaces", "unknown_auxiliary", "admin", "", 405},
		{"OPTIONS", "/mcp", "http_mcp", "", "", 403},
		{"GET", mcpMetadataPath, "http_mcp", "", "", 200},
		{"POST", "/mcp", "http_mcp", "", "{}", 401},
	} {
		perform(api.URL, target.method, target.path, target.body, target.token, "", target.family, target.status)
	}
	body := `{"slug":"activity-population","displayName":"Activity population"}`
	first := perform(api.URL, "POST", "/api/v1/namespaces", body, "admin", "activity-replay", "control_command", 201)
	replay := perform(api.URL, "POST", "/api/v1/namespaces", body, "admin", "activity-replay", "control_command", 201)
	if string(first) != string(replay) {
		t.Fatal("mutation replay changed")
	}
	// Metadata, refused preflight and missing Bearer are MCP protocol outcomes.
	outcomes, err := registry.Activity(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range outcomes.ControlSearch.Outcomes {
		want := uint64(0)
		if row.Outcome == "succeeded" || row.Outcome == "rejected" {
			want = 1
		}
		if row.Completed != want {
			t.Fatalf("Control Search %s: %d want %d", row.Outcome, row.Completed, want)
		}
	}
	for _, row := range outcomes.MCP.Outcomes {
		want := uint64(0)
		if row.Operation == "protocol" && row.Outcome == "completed" {
			want = 1
		}
		if row.Operation == "protocol" && row.Outcome == "rejected" {
			want = 2
		}
		if row.Completed != want {
			t.Fatalf("MCP %s/%s: %d want %d", row.Operation, row.Outcome, row.Completed, want)
		}
	}
	// Identical health routes still merge in legacy metrics, without losing family provenance.
	if !strings.Contains(string(registry.Render(t.Context())), `open_agentic_catalog_http_responses_total{method="GET",route="/livez",status_class="2xx"} 2`) {
		t.Fatal("combined listener raw health series changed")
	}
	// Current observation has not completed in its own response. Prior reads have.
	for i := 0; i < 2; i++ {
		prior := expected["observer"].count
		raw := perform(api.URL, "GET", "/api/v1/service-activity", "", "admin", "", "observer", 200)
		var wire serviceActivityResponse
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		if wire.Role != "all" || wire.HTTP.Families[observability.FamilyObserver].Completed != prior {
			t.Fatal("current observer counted too soon")
		}
		for _, private := range []string{"PRIVATE_QUERY", "activity-population", "/api/v1/", `"admin"`, "catalog.example.com"} {
			if strings.Contains(string(raw), private) {
				t.Fatalf("private context leaked: %s", private)
			}
		}
	}
}

func TestServiceActivityMCPLogicalOutcomesUseIndependentPopulations(t *testing.T) {
	registry := observability.New(nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	reader := mcpFaultReader{access: func(context.Context, url.Values) (controlclient.Envelope, error) {
		close(entered)
		<-release
		return controlclient.Envelope{}, errors.New("PRIVATE_TOOL_DEPENDENCY")
	}}
	auth := mcpAuthenticator(func(context.Context, string) (identity.Actor, error) {
		return projectionActor(identity.RoleReader), nil
	})
	actual := newMCPFaultHTTP(t, reader, auth, time.Second, registry)
	defer actual.Close()
	defer once.Do(func() { close(release) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		status, _, body := mcpRequest(t, actual.Client(), actual.URL+"/mcp", "synthetic-activity-token-908", "2026-07-28", "tools/call", "catalog_control_access", mcpWire(t, "2026-07-28", "tools/call", "catalog_control_access", map[string]any{}), nil)
		if status != 200 || !strings.Contains(string(body), `"isError":true`) || strings.Contains(string(body), "PRIVATE_TOOL_DEPENDENCY") {
			t.Errorf("wrong tool failure: %d %s", status, body)
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("held MCP read did not start")
	}
	status, _, _ := mcpRequest(t, actual.Client(), actual.URL+"/mcp", "synthetic-activity-token-908", "2026-07-28", "tools/list", "", mcpWire(t, "2026-07-28", "tools/list", "", nil), nil)
	if status != 503 {
		t.Fatal("busy request not refused", status)
	}
	once.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("held MCP call did not finish")
	}
	snapshot, err := registry.Activity(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range snapshot.MCP.Outcomes {
		want := uint64(0)
		if outcome.Operation == "protocol" && outcome.Outcome == "busy" || outcome.Operation == "call" && outcome.Outcome == "tool_error" {
			want = 1
		}
		if outcome.Completed != want {
			t.Fatalf("%s/%s = %d want %d", outcome.Operation, outcome.Outcome, outcome.Completed, want)
		}
	}
	// This helper owns MCP transport only. No API middleware or Search ran.
	if snapshot.HTTP.Completed != 0 {
		t.Fatal("MCP logical outcomes manufactured HTTP completions")
	}
	for _, outcome := range snapshot.ControlSearch.Outcomes {
		if outcome.Completed != 0 {
			t.Fatal("MCP outcomes manufactured Search outcomes")
		}
	}
}

// Content-Length can make the body readable before metricsMiddleware unwinds.
// Wait for the exact expected completion without repeating the HTTP request.
func activityAfterCompletion(t *testing.T, registry *observability.Registry, completed uint64) observability.ActivitySample {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		sample, err := registry.Activity(true)
		if err != nil {
			t.Fatal(err)
		}
		if sample.HTTP.Completed >= completed {
			return sample
		}
		if ctx.Err() != nil {
			t.Fatalf("HTTP completion was not recorded: got %d, want at least %d", sample.HTTP.Completed, completed)
		}
		runtime.Gosched()
	}
}
