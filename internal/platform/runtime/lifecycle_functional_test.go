//go:build functional

// Lifecycle tests protect optional-provider ownership without PostgreSQL or
// external collectors. Loopback servers belong exclusively to each test;
// connection notifications never block HTTP server cleanup.
package runtime

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/embeddings"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/tracing"
)

// TestRuntimeTracingOwnership distinguishes failed startup cleanup from the
// successful transfer to Runtime.Close, including cancellation during startup.
func TestRuntimeTracingOwnership(t *testing.T) {
	unsetEnvironment(t, "CATALOG_TRACING")
	t.Setenv("OTEL_EXPORTER_OTLP_CERTIFICATE", "")
	for _, role := range []config.Role{config.RoleAPI, config.RoleWorker, config.RoleAll} {
		for _, failure := range []string{"", "credential directory", "cancelled startup", "OIDC trust"} {
			name := failure
			if name == "" {
				name = "successful startup"
			}
			t.Run(string(role)+"/"+name, func(t *testing.T) {
				idle, closed := make(chan struct{}, 1), make(chan struct{}, 1)
				collector := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/x-protobuf")
				}))
				collector.Config.ConnState = func(_ net.Conn, state http.ConnState) {
					switch state {
					case http.StateIdle:
						select {
						case idle <- struct{}{}:
						default:
						}
					case http.StateClosed:
						select {
						case closed <- struct{}{}:
						default:
						}
					case http.StateNew, http.StateActive, http.StateHijacked:
					}
				}
				collector.Start()
				t.Cleanup(collector.Close)
				cfg := localRuntimeConfig()
				cfg.Tracing = config.Defaults().Tracing
				cfg.Tracing.Enabled = true
				cfg.Tracing.SampleRatio = 1
				cfg.Tracing.BatchSize = 1
				cfg.Tracing.Endpoint = collector.URL + "/v1/traces"
				cfg.Tracing.AllowHTTP = true
				cfg.Tracing.AllowedAddressPrefixes = []string{"127.0.0.0/8"}
				cfg.Tracing.Installation = "runtime-fixture"
				if failure == "credential directory" || failure == "cancelled startup" {
					cfg.CredentialResolution = config.CredentialResolution{Enabled: true, Directory: filepath.Join(t.TempDir(), "missing")}
				}
				if failure == "OIDC trust" {
					cfg.Auth = config.Auth{Mode: "oidc", Issuer: "https://unreachable.invalid", Audience: "catalog", TenancyMode: "dedicated", DedicatedTenantID: cfg.Discovery.Hosts[0].TenantID, TrustBundleFile: filepath.Join(t.TempDir(), "missing-issuer-ca.pem")}
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var provider *tracing.Provider
				calls := 0
				var suppliedInstance string
				start := func(ctx context.Context, settings config.Tracing, gotRole config.Role, version, instance string, logger *slog.Logger, observer tracing.Observer) (*tracing.Provider, error) {
					calls++
					suppliedInstance = instance
					if gotRole != role || version != "fixture" || logger == nil || observer == nil {
						t.Fatal("tracing construction lost role, version, logger or observer")
					}
					var err error
					exported := make(chan bool, 1)
					provider, err = tracing.New(ctx, settings, gotRole, version, instance, logger, runtimeExportObserver{delegate: observer, completed: exported})
					if err == nil {
						t.Cleanup(func() {
							cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
							defer stop()
							_ = provider.Shutdown(cleanup)
						})
						_, finish := provider.Start(ctx, "runtime lifecycle", catalog.TraceInternal, "")
						finish("ok")
						select {
						case success := <-exported:
							if !success {
								t.Fatal("fixture trace export failed")
							}
						case <-time.After(5 * time.Second):
							t.Fatal("fixture trace was not exported")
						}
						awaitRuntimeConnection(t, idle, "collector connection did not become idle")
					}
					if failure == "cancelled startup" {
						cancel()
					}
					return provider, err
				}
				runtime, err := buildWithTracing(ctx, BuildOptions{ConfigPath: writeRuntimeConfig(t, cfg), Role: role, Version: "fixture", StartTracing: true}, start)
				if runtime != nil {
					t.Cleanup(runtime.Close)
				}
				if calls != 1 || provider == nil {
					t.Fatalf("tracing was not constructed once: calls=%d, error=%v", calls, err)
				}
				if failure != "" && !(failure == "OIDC trust" && role == config.RoleWorker) {
					wantError := "configure federation credential resolution"
					if failure == "OIDC trust" {
						wantError = "OIDC trust bundle must be a readable regular file"
					}
					if runtime != nil || err == nil || !strings.Contains(err.Error(), wantError) {
						t.Fatalf("expected startup rejection with no runtime, got %v", err)
					}
					awaitRuntimeConnection(t, closed, "failed build did not close the collector connection")
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if runtime.Process == nil || runtime.Process.ID() != suppliedInstance {
					t.Fatal("tracing and status do not share process identity")
				}
				if runtime.Tracing != provider {
					t.Fatal("successful build did not transfer tracing ownership")
				}
				select {
				case <-closed:
					t.Fatal("successful build closed its collector connection")
				default:
				}
				runsAPI := role != config.RoleWorker
				runsWorker := role != config.RoleAPI
				if (runtime.HTTP != nil) != runsAPI || (runtime.Authenticator != nil) != runsAPI || (runtime.Identity != nil) != runsAPI ||
					(runtime.Operations != nil) != runsWorker || (runtime.JobWorker != nil) != runsWorker || (runtime.SourceMaintainer != nil) != runsWorker ||
					(runtime.Housekeeper != nil) != runsWorker || (runtime.OutboxProcessor != nil) != runsWorker {
					t.Fatal("runtime owns components outside its selected role")
				}
				if runtime.EmbeddingClient != nil || runtime.SemanticMaintainer != nil || runtime.Webhooks != nil || runtime.WebhookDispatcher != nil || runtime.Pool != nil {
					t.Fatal("disabled optional components or database acquired resources")
				}
				runtime.Close()
				awaitRuntimeConnection(t, closed, "runtime cleanup did not close the collector connection")
			})
		}
	}
}

// runtimeExportObserver retains runtime metrics and signals that the exporter
// has consumed its response, making the idle-connection assertion deterministic.
type runtimeExportObserver struct {
	delegate  tracing.Observer
	completed chan<- bool
}

// ObserveTraceExport forwards the outcome before releasing the test's startup.
func (o runtimeExportObserver) ObserveTraceExport(success bool, count int) {
	o.delegate.ObserveTraceExport(success, count)
	o.completed <- success
}

// awaitRuntimeConnection uses a channel for synchronization and a deadline only
// to bound a failed assertion; no sleep determines the expected ordering.
func awaitRuntimeConnection(t *testing.T, event <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(5 * time.Second):
		t.Fatal(failure)
	}
}

// TestRuntimeCloseReleasesEmbeddingConnections proves cleanup closes an actual
// idle owned connection; invoking Close alone would not detect missing delegation.
func TestRuntimeCloseReleasesEmbeddingConnections(t *testing.T) {
	t.Parallel()
	idle := make(chan struct{}, 1)
	closed := make(chan struct{}, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"fixture","data":[{"index":0,"embedding":[3,4]}]}`))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateIdle:
			select {
			case idle <- struct{}{}:
			default:
			}
		case http.StateClosed:
			select {
			case closed <- struct{}{}:
			default:
			}
		case http.StateNew, http.StateActive, http.StateHijacked:
		}
	}
	server.Start()
	defer server.Close()
	settings := config.SemanticDefaults()
	settings.Enabled = true
	settings.Endpoint = server.URL + "/v1/embeddings"
	settings.AllowHTTP = true
	settings.AllowedAddressPrefixes = []string{"127.0.0.0/8"}
	settings.Model = "fixture"
	settings.ProfileRevision = "one"
	settings.Dimensions = 2
	client, err := embeddings.New(settings)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	runtime := &Runtime{EmbeddingClient: client}
	vector, err := client.Query(t.Context(), "fixture")
	if err != nil || len(vector) != 2 {
		t.Fatalf("embedding request: vector=%v error=%v", vector, err)
	}
	awaitRuntimeConnection(t, idle, "embedding connection did not become idle")
	select {
	case <-closed:
		t.Fatal("embedding connection closed before runtime cleanup")
	default:
	}
	runtime.Close()
	awaitRuntimeConnection(t, closed, "runtime cleanup did not close the idle embedding connection")
	runtime.Close()
}
