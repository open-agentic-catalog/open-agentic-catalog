package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

type unusedServerIdempotency struct{ idempotency.Store }
type unusedServerAtomicIdempotency struct{ idempotency.AtomicStore }

// Only constructor validation and registration run. These uninitialized domain
// pointers cannot execute a workflow; unused persistence/authentication calls fail.
func inertServerDependencies() Dependencies {
	return Dependencies{Catalog: &catalog.Service{}, Identity: &identity.Service{}, Federation: &federation.Manager{}, Jobs: &jobs.Service{}, Authenticator: discoveryAdmissionAuthenticator(func(context.Context, string) (identity.Actor, error) { panic("unexpected authentication") }), Idempotency: unusedServerIdempotency{}, MaxBodyBytes: 1024, RequestTimeout: time.Second, CursorKey: []byte(strings.Repeat("k", 32))}
}

func TestServerConstructionRejectsIncompleteDependencies(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*Dependencies){
		"catalog": func(d *Dependencies) { d.Catalog = nil }, "identity": func(d *Dependencies) { d.Identity = nil }, "federation": func(d *Dependencies) { d.Federation = nil }, "jobs": func(d *Dependencies) { d.Jobs = nil }, "authenticator": func(d *Dependencies) { d.Authenticator = nil },
		"neither replay store": func(d *Dependencies) { d.Idempotency = nil }, "both replay stores": func(d *Dependencies) { d.AtomicIdempotency = unusedServerAtomicIdempotency{} },
		"zero body bound": func(d *Dependencies) { d.MaxBodyBytes = 0 }, "negative body bound": func(d *Dependencies) { d.MaxBodyBytes = -1 }, "zero deadline": func(d *Dependencies) { d.RequestTimeout = 0 }, "negative deadline": func(d *Dependencies) { d.RequestTimeout = -1 }, "short cursor key": func(d *Dependencies) { d.CursorKey = make([]byte, 31) },
		"capability mismatch": func(d *Dependencies) { d.Capabilities.Webhooks = CapabilityEnabled },
	} {
		t.Run(name, func(t *testing.T) {
			d := inertServerDependencies()
			mutate(&d)
			s, err := New(d)
			if err == nil || s != nil {
				t.Fatalf("constructor=%v err=%v", s, err)
			}
		})
	}
}

func TestServerConstructionValidatesDiscoveryBindings(t *testing.T) {
	t.Parallel()
	valid := DiscoveryHost{Host: "catalog.example.com", TenantID: projectionTenant, Mode: DiscoveryAnonymous, PublisherDomain: "example.com", Publication: &publication.Manager{}}
	for _, mode := range []DiscoveryMode{DiscoveryDisabled, DiscoveryAnonymous, DiscoveryAuthenticated} {
		t.Run(string(mode), func(t *testing.T) {
			d := inertServerDependencies()
			binding := valid
			binding.Mode = mode
			if mode == DiscoveryDisabled {
				binding.Publication = nil
			}
			d.DiscoveryHosts = []DiscoveryHost{binding}
			s, err := New(d)
			if err != nil || !reflect.DeepEqual(s.discovery[binding.Host], binding) {
				t.Fatalf("host binding: %v", err)
			}
		})
	}
	for name, mutate := range map[string]func(*DiscoveryHost){
		"unsafe host": func(b *DiscoveryHost) { b.Host = "https://catalog.example.com" }, "noncanonical host": func(b *DiscoveryHost) { b.Host = "CATALOG.EXAMPLE.COM" }, "tenant": func(b *DiscoveryHost) { b.TenantID = "invalid" }, "publisher absent": func(b *DiscoveryHost) { b.PublisherDomain = "" }, "unknown mode": func(b *DiscoveryHost) { b.Mode = "unknown" }, "missing manager": func(b *DiscoveryHost) { b.Publication = nil },
	} {
		t.Run(name, func(t *testing.T) {
			d := inertServerDependencies()
			binding := valid
			mutate(&binding)
			d.DiscoveryHosts = []DiscoveryHost{binding}
			s, err := New(d)
			if err == nil || s != nil {
				t.Fatal("invalid discovery binding accepted")
			}
		})
	}
	d := inertServerDependencies()
	d.DiscoveryHosts = []DiscoveryHost{valid, valid}
	if s, err := New(d); err == nil || s != nil {
		t.Fatal("duplicate discovery host accepted")
	}
}

func TestServerConstructionDefaultsAndOptionalRoutes(t *testing.T) {
	t.Parallel()
	for _, optional := range []bool{false, true} {
		t.Run(map[bool]string{false: "defaults", true: "injected atomic and webhooks"}[optional], func(t *testing.T) {
			d := inertServerDependencies()
			d.ServiceVersion = "unit-version"
			d.Commit = "unit-commit"
			d.ARDProfile = "unit-ard"
			d.AICatalogProfile = "unit-ai-catalog"
			expectedState := "unknown"
			if optional {
				d.SourceState = "clean"
				expectedState = "clean"
				d.Idempotency = nil
				d.AtomicIdempotency = unusedServerAtomicIdempotency{}
				d.Webhooks = &webhook.Service{}
				d.Logger = slog.New(slog.DiscardHandler)
				d.Now = func() time.Time { return projectionTime }
				d.Observability = observability.New(d.Now)
			}
			s, err := New(d)
			if err != nil {
				t.Fatal(err)
			}
			if s.logger == nil || s.now == nil || s.observability == nil || s.Handler() == nil || !s.ready.Load() {
				t.Fatal("constructor defaults incomplete")
			}
			if optional && (s.logger != d.Logger || s.observability != d.Observability || !s.now().Equal(projectionTime)) {
				t.Fatal("injected options were replaced")
			}
			if !optional && s.logger != slog.Default() {
				t.Fatal("default logger")
			}
			d.CursorKey[0] = 'x'
			if s.cursorKey[0] != 'k' {
				t.Fatal("cursor key aliases caller memory")
			}
			expected := CapabilityDisabled
			if optional {
				expected = CapabilityEnabled
			}
			if s.capabilities.Webhooks != expected {
				t.Fatal("webhook capability disagrees with registration")
			}
			// Match registrations without executing product services or request workflows.
			mux := http.NewServeMux()
			s.routes(mux)
			for _, path := range []string{"/api/v1/webhooks", "/api/v1/webhooks/reconciliation", "/api/v1/webhooks/" + projectionItem, "/api/v1/webhooks/" + projectionItem + "/deliveries"} {
				_, pattern := mux.Handler(httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
				if (pattern != "") != optional {
					t.Fatalf("optional route %s=%q", path, pattern)
				}
			}
			for _, path := range []string{"/livez", "/readyz", "/version", "/metrics", "/openapi/control-v1.yaml", "/api/v1/namespaces", "/.well-known/ard.json"} {
				_, pattern := mux.Handler(httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
				if pattern == "" {
					t.Fatalf("core route missing %s", path)
				}
			}
			// Health and metadata have no domain calls even through the constructed stack.
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/livez", nil))
			requireControlJSON(t, w, 200, serviceStatusResponse{Status: "live"})
			if w.Header().Get("X-Request-ID") == "" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("constructed stack omitted transport protections")
			}
			w = httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/version", nil))
			requireControlJSON(t, w, 200, serviceVersionResponse{Service: "open-agentic-catalog", Version: d.ServiceVersion, Commit: d.Commit, SourceState: expectedState, ARDProfile: d.ARDProfile, AICatalogProfile: d.AICatalogProfile})
		})
	}
}
