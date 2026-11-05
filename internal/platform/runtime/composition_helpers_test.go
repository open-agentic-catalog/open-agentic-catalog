// Composition helper tests isolate evidence projection and startup failure
// handoff without constructing stores, listeners or optional providers.
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/httpserver"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/tracing"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// identityEvidenceRepository permits only the adapter's single atomic handoff.
// Any unrelated repository call fails through the unimplemented embedded port.
type identityEvidenceRepository struct {
	catalog.Repository
	appendEvidence func(context.Context, catalog.AuditEvent, *catalog.OutboxEvent) error
}

func (r identityEvidenceRepository) AppendEvidence(ctx context.Context, record catalog.AuditEvent, outbox *catalog.OutboxEvent) error {
	return r.appendEvidence(ctx, record, outbox)
}

// TestMemoryIdentityEvidenceProjection preserves attribution and private causal
// metadata in one repository call, including disabled-delivery acknowledgement.
func TestMemoryIdentityEvidenceProjection(t *testing.T) {
	t.Parallel()
	const tenant = "00000000-0000-7000-8000-000000000001"
	const target = "00000000-0000-7000-8000-000000000002"
	const cause = catalog.TraceContext("00-11111111111111111111111111111111-2222222222222222-01")
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	record, err := audit.NewRecord("00000000-0000-7000-8000-000000000003", tenant,
		audit.Principal{ID: "00000000-0000-7000-8000-000000000004", Type: audit.PrincipalCatalogMembership, ActorType: string(identity.ActorHuman), RealmID: "00000000-0000-7000-8000-000000000005"},
		"identity.role_assignment.granted", "role_assignment", target, "Grant reader access", audit.Execution{RequestID: "request-id", AuthorizationDigest: "sha256:" + strings.Repeat("a", 64)},
		map[string]any{"membershipId": "00000000-0000-7000-8000-000000000006", "role": string(identity.RoleReader), "namespaceId": ""}, at)
	if err != nil {
		t.Fatal(err)
	}
	data, err := events.MarshalReference(record.Action, record.TargetType, target, "")
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("evidence persistence failed")
	for _, tc := range []struct {
		name      string
		published *time.Time
		cause     catalog.TraceContext
		failure   error
	}{
		{name: "pending notification", cause: cause},
		{name: "acknowledged without tracing", published: &at},
		{name: "failed atomic append", cause: cause, failure: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := catalog.WithTraceContext(t.Context(), tc.cause)
			evidence := identity.ChangeEvidence{FormatVersion: record.FormatVersion, AuditID: record.ID, OutboxID: "00000000-0000-7000-8000-000000000007", Actor: record.Actor, Action: record.Action, TargetType: record.TargetType, TargetID: record.TargetID, Reason: record.Reason, RequestID: record.RequestID, Details: record.Details, At: record.At, OutboxType: events.CanonicalType(record.Action), OutboxSubject: target, OutboxData: data, OutboxPublished: tc.published}
			wantOutbox := catalog.OutboxEvent{CausalTrace: tc.cause, ID: evidence.OutboxID, TenantID: tenant, Type: evidence.OutboxType, Subject: target, Data: json.RawMessage(data), CreatedAt: at, PublishedAt: tc.published}
			calls := 0
			sink := memoryIdentityEvidenceSink{repository: identityEvidenceRepository{appendEvidence: func(got context.Context, auditRecord catalog.AuditEvent, outbox *catalog.OutboxEvent) error {
				calls++
				if got != ctx || !reflect.DeepEqual(auditRecord, record) || outbox == nil || !reflect.DeepEqual(*outbox, wantOutbox) {
					t.Fatalf("evidence handoff: context=%t audit=%#v outbox=%#v", got == ctx, auditRecord, outbox)
				}
				return tc.failure
			}}}
			if err := sink.AppendIdentityEvidence(ctx, tenant, evidence); !errors.Is(err, tc.failure) || calls != 1 {
				t.Fatalf("append calls=%d error=%v", calls, err)
			}
		})
	}
}

// TestCapabilityState keeps wired-but-disabled capabilities distinct from
// unavailable capabilities, which are supplied explicitly by the caller.
func TestCapabilityState(t *testing.T) {
	t.Parallel()
	for enabled, want := range map[bool]httpserver.CapabilityState{false: httpserver.CapabilityDisabled, true: httpserver.CapabilityEnabled} {
		if got := capabilityState(enabled); got != want {
			t.Fatalf("enabled=%t: capability=%q, want %q", enabled, got, want)
		}
	}
}

// TestRuntimeTracingFailureStopsBeforeDependencies uses the existing startup
// callback to reject construction before any repository or provider is opened.
func TestRuntimeTracingFailureStopsBeforeDependencies(t *testing.T) {
	for _, name := range config.EnvironmentVariables() {
		unsetEnvironment(t, name)
	}
	for _, role := range []config.Role{config.RoleAPI, config.RoleWorker, config.RoleAll} {
		for _, format := range []string{"text", "json"} {
			for _, panicking := range []bool{false, true} {
				t.Run(string(role)+"/"+format+"/"+map[bool]string{false: "error", true: "panic"}[panicking], func(t *testing.T) {
					cfg := localRuntimeConfig()
					cfg.LogFormat = format
					ctx := t.Context()
					failure := errors.New("SYNTHETIC_PRIVATE_STARTUP_FAILURE")
					calls := 0
					start := func(got context.Context, settings config.Tracing, gotRole config.Role, version, instance string, logger *slog.Logger, observer tracing.Observer) (*tracing.Provider, error) {
						calls++
						if got != ctx || gotRole != role || version != "fixture" || logger == nil || settings.Enabled {
							t.Fatal("tracing startup lost its context, role, version, logger or disabled configuration")
						}
						registry, ok := observer.(*observability.Registry)
						if !ok {
							t.Fatalf("missing runtime metrics: %T", observer)
						}
						lifecycle, degraded, components, dependencies := registry.HealthState()
						want := map[observability.Component]observability.ComponentState{}
						if role != config.RoleWorker {
							want[observability.ComponentAPI] = observability.ComponentStarting
						}
						if role != config.RoleAPI {
							for _, component := range []observability.Component{observability.ComponentWorker, observability.ComponentOutbox, observability.ComponentSourceMaintenance, observability.ComponentHousekeeping, observability.ComponentOperations} {
								want[component] = observability.ComponentStarting
							}
						}
						if lifecycle != observability.LifecycleStarting || degraded || !reflect.DeepEqual(components, want) || len(dependencies) != 0 {
							t.Fatalf("startup state=%s degraded=%t components=%v dependencies=%v", lifecycle, degraded, components, dependencies)
						}
						if panicking {
							panic(failure)
						}
						return nil, failure
					}
					result, err := buildWithTracing(ctx, BuildOptions{ConfigPath: writeRuntimeConfig(t, cfg), Role: role, Version: "fixture", StartTracing: true}, start)
					want := failure
					if panicking {
						want = safeerr.ErrPanic
					}
					if result != nil || !errors.Is(err, want) || calls != 1 {
						t.Fatalf("startup result=%v error=%v tracing calls=%d", result, err, calls)
					}
					var output bytes.Buffer
					diagnostics.Report(&output, "catalogd", "INTERNAL", err, true)
					if json.Valid(bytes.TrimSpace(output.Bytes())) != (format == "json") || strings.Count(output.String(), "\n") != 1 || strings.Contains(output.String(), failure.Error()) || !strings.Contains(output.String(), "process.failed") || strings.Contains(output.String(), "INTERNAL_PANIC") != panicking {
						t.Fatalf("startup failure format/privacy: %s", output.String())
					}
				})
			}
		}
	}
}
