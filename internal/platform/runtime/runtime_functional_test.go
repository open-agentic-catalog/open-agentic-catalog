//go:build functional

// Functional tests exercise real process, network or application composition boundaries.
package runtime

import (
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestWorkerRuntimeDoesNotRequireOIDCDiscoveryOrHTTP(t *testing.T) {
	duration := func(value time.Duration) config.Duration { return config.Duration{Duration: value} }
	cfg := config.Config{PublisherDomainVerification: config.PublisherDomainVerificationDefaults(), TrustVerification: config.TrustVerificationDefaults(), Ratings: config.Defaults().Ratings, LogLevel: "info", LogFormat: "text", Validation: config.Defaults().Validation, ListingMedia: config.ListingMediaDefaults(),
		Environment: "test",
		Profile:     "worker-test",
		ServiceName: "open-agentic-catalog-worker-test",
		HTTP:        config.HTTP{Address: "127.0.0.1:0", PublicBaseURL: "https://catalog.example.com", ReadTimeout: duration(time.Second), RequestTimeout: duration(time.Second), WriteTimeout: duration(7 * time.Second), IdleTimeout: duration(time.Second), ShutdownTimeout: duration(7 * time.Second), MaxBodyBytes: 1 << 20},
		Database:    config.Database{Driver: "memory", MaxConnLifetime: config.Defaults().Database.MaxConnLifetime, MaxConnIdleTime: config.Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: config.Defaults().Database.HealthCheckPeriod},
		Auth:        config.Auth{Mode: "oidc", Issuer: "https://unreachable.invalid", TrustBundleFile: "/not-mounted/oidc-ca.pem", Audience: "catalog", TenancyMode: "dedicated", DedicatedTenantID: "00000000-0000-7000-8000-000000000001"},
		Jobs:        config.Jobs{MaxAttempts: 5, RetryCeiling: config.Duration{Duration: time.Hour}},
		Worker:      config.Worker{DependencyRetryInitialDelay: config.Duration{Duration: time.Second}, DependencyRetryCeiling: config.Duration{Duration: 30 * time.Second}, PollInterval: duration(time.Millisecond), LeaseDuration: duration(time.Minute), Concurrency: 1, SourceMaintenanceInterval: duration(15 * time.Minute), HousekeepingInterval: duration(5 * time.Minute), TerminalJobRetention: duration(168 * time.Hour), AcknowledgedOutboxRetention: duration(24 * time.Hour)},
		Federation:  config.Federation{RequestTimeout: duration(time.Second), MaxResponseBytes: 1 << 20, MaxRedirects: 1, MaxFanout: 1, MaxHops: 1, MaxLivePagesPerSource: 1},
		Discovery:   testDiscovery(),
		Publication: config.Publication{ARDProfile: "ard-0.91-aa3e598", AICatalogProfile: "ai-catalog-1.0-b062278"},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "worker.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := Build(context.Background(), BuildOptions{ConfigPath: path, Role: "worker", Version: "test", Commit: "test", Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if runtime.HTTP != nil || runtime.Operations == nil || runtime.Observability == nil || runtime.JobWorker == nil || runtime.SourceMaintainer == nil || runtime.Housekeeper == nil || runtime.Webhooks != nil || runtime.WebhookDispatcher != nil || runtime.WebhookDrainer == nil || runtime.OutboxProcessor == nil {
		t.Fatalf("worker runtime wiring = %#v", runtime)
	}
	actor := identity.Actor{MembershipID: "00000000-0000-7000-8000-000000000093", RealmID: "00000000-0000-7000-8000-000000000094", ActorType: identity.ActorHuman, TenantID: cfg.Discovery.Hosts[0].TenantID, Subject: "admin", Roles: []identity.Role{identity.RoleTenantAdmin}}
	if _, err := runtime.Catalog.CreateNamespace(identityfixture.WithActor(context.Background(), actor), catalog.CreateNamespaceInput{Slug: "disabled-mode", DisplayName: "Disabled mode"}); err != nil {
		t.Fatal(err)
	}
	if events, err := runtime.Repository.ClaimOutbox(context.Background(), "test-worker", time.Now(), time.Minute, 10); err != nil || len(events) != 0 {
		t.Fatalf("disabled-mode pending events = %d, %v", len(events), err)
	}
}

func TestMemoryAPIRuntimeSharesIdentityEvidenceWithCatalogAudit(t *testing.T) {
	cfg := localRuntimeConfig()
	cfg.Validation = catalog.DiagnosticLimits{MaxFindings: 2, MaxDiagnosticBytes: 1024}
	cfg.Auth.DevelopmentTokens["admin"] = config.DevelopmentUser{Subject: "admin", TenantID: cfg.Discovery.Hosts[0].TenantID, Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleSecurityReviewer}}
	runtime, err := Build(t.Context(), BuildOptions{ConfigPath: writeRuntimeConfig(t, cfg), Role: config.RoleAPI, Version: "test", Commit: "test", Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	actor, err := runtime.Authenticator.Authenticate(t.Context(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), actor)
	assignment, err := runtime.Identity.GrantRole(ctx, identity.GrantRoleInput{Subject: "reader", ActorType: identity.ActorHuman, Role: identity.RoleReader, Reason: "Grant local reader access"}, "identity-shared-audit")
	if err != nil {
		t.Fatal(err)
	}
	items, err := runtime.Catalog.AuditEvents(ctx, catalog.AuditEventListRequest{Filters: audit.Query{Action: "identity.role_assignment.granted", TargetType: "role_assignment", TargetID: assignment.ID}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Actor != actor.MembershipID || items[0].RequestID != "identity-shared-audit" || items[0].FormatVersion == nil {
		t.Fatalf("shared in-memory identity audit = %#v", items)
	}
	if runtime.Catalog.ValidationLimits() != cfg.Validation {
		t.Fatal("runtime lost configured diagnostic limits")
	}
	ns, err := runtime.Catalog.CreateNamespace(ctx, catalog.CreateNamespaceInput{Slug: "diagnostics", DisplayName: "Diagnostics"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := runtime.Catalog.CreateResource(ctx, catalog.CreateResourceInput{NamespaceID: ns.ID, CanonicalIdentifier: "urn:air:catalog.example.com:diagnostics:agent", Kind: catalog.KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	_, revision, evidence, err := runtime.Catalog.CreateVersion(ctx, resource.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: "application/a2a-agent-card+json", Data: json.RawMessage(`{}`)}}})
	if err != nil || evidence.State != catalog.EvidenceBlocked || len(evidence.Findings) != 2 || len(revision.Representations[0].Findings) != 2 || evidence.Findings[1].Code != catalog.DiagnosticBudgetExhausted {
		t.Fatalf("configured runtime diagnostics: %v / %+v", err, evidence.Findings)
	}
}

func TestSourceSyncWorkerAcceptsOnlyCurrentExplicitPayloads(t *testing.T) {
	tests := []struct {
		name     string
		version  int
		payload  string
		accepted bool
	}{
		{name: "retained v1", version: 1, payload: `{"sourceId":"00000000-0000-7000-8000-000000000099","sourceRevision":1}`},
		{name: "v2 manual", version: 2, payload: `{"sourceId":"00000000-0000-7000-8000-000000000099","sourceRevision":1,"trigger":"manual"}`, accepted: true},
		{name: "v2 automatic", version: 2, payload: `{"sourceId":"00000000-0000-7000-8000-000000000099","sourceRevision":1,"trigger":"automatic"}`, accepted: true},
		{name: "v1 trigger", version: 1, payload: `{"sourceId":"00000000-0000-7000-8000-000000000099","sourceRevision":1,"trigger":"automatic"}`},
		{name: "v2 missing trigger", version: 2, payload: `{"sourceId":"00000000-0000-7000-8000-000000000099","sourceRevision":1}`},
		{name: "v2 unknown field", version: 2, payload: `{"sourceId":"00000000-0000-7000-8000-000000000099","sourceRevision":1,"trigger":"automatic","extra":true}`},
		{name: "v2 duplicate field", version: 2, payload: `{"sourceId":"00000000-0000-7000-8000-000000000099","sourceId":"00000000-0000-7000-8000-000000000098","sourceRevision":1,"trigger":"automatic"}`},
		{name: "unknown version", version: 3, payload: `{"sourceId":"00000000-0000-7000-8000-000000000099","sourceRevision":1,"trigger":"automatic"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := jobs.NewMemoryStore()
			now := time.Now().UTC()
			job := jobs.Job{ID: "00000000-0000-7000-8000-000000000011", TenantID: "00000000-0000-7000-8000-000000000001", Kind: "source.sync", State: jobs.StateQueued, PayloadVersion: test.version, Payload: json.RawMessage(test.payload), AttemptLimit: 1, CreatedAt: now, UpdatedAt: now}
			if _, _, err := store.Enqueue(t.Context(), job); err != nil {
				t.Fatal(err)
			}
			handler := sourceSyncJobHandler(func(context.Context, string, int64) (federation.Snapshot, error) {
				return federation.Snapshot{}, catalog.E(catalog.CodeNotFound, "source was not found")
			}, observability.New(time.Now))
			worker, err := jobs.NewWorker(store, clock.Real{}, "payload-fixture", time.Minute, time.Second, time.Hour, map[string]jobs.Handler{"source.sync": handler})
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.RunOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			stored, err := store.Get(t.Context(), job.TenantID, job.ID)
			if err != nil || stored.State != jobs.StateDeadLetter || stored.FailureClass != jobs.FailureInvalidInput {
				t.Fatalf("source sync payload result = %#v, %v", stored, err)
			}

			invalidPayload := stored.SafeError == "Source sync job payload is invalid."
			if invalidPayload == test.accepted {
				t.Fatalf("source sync payload accepted=%t, safe error=%q", !invalidPayload, stored.SafeError)
			}
		})
	}
}

func TestWorkerRuntimeWiresOnlyTheEnabledWebhookDispatcher(t *testing.T) {
	cfg := localRuntimeConfig()
	cfg.Webhooks = config.Webhooks{RetryCeiling: config.Duration{Duration: time.Hour},
		Enabled:      true,
		MaxAttempts:  3,
		Timeout:      config.Duration{Duration: time.Second},
		PollInterval: config.Duration{Duration: 2 * time.Millisecond},
	}
	unsetEnvironment(t, "CATALOG_CURSOR_SIGNING_KEY")
	unsetEnvironment(t, "CATALOG_CURSOR_SIGNING_KEY_FILE")
	unsetEnvironment(t, "CATALOG_WEBHOOK_ENCRYPTION_KEY")
	unsetEnvironment(t, "CATALOG_WEBHOOK_ENCRYPTION_KEY_FILE")
	runtime, err := Build(context.Background(), BuildOptions{ConfigPath: writeRuntimeConfig(t, cfg), Role: config.RoleWorker, Version: "test", Commit: "test", Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if runtime.HTTP != nil || runtime.Webhooks != nil || runtime.WebhookDispatcher == nil || runtime.WebhookDrainer != nil || runtime.OutboxProcessor != runtime.WebhookDispatcher {
		t.Fatalf("enabled worker webhook wiring = %#v", runtime)
	}
	actor := identity.Actor{MembershipID: "00000000-0000-7000-8000-000000000093", RealmID: "00000000-0000-7000-8000-000000000094", ActorType: identity.ActorHuman, TenantID: cfg.Discovery.Hosts[0].TenantID, Subject: "admin", Roles: []identity.Role{identity.RoleTenantAdmin}}
	if _, err := runtime.Catalog.CreateNamespace(identityfixture.WithActor(context.Background(), actor), catalog.CreateNamespaceInput{Slug: "enabled-mode", DisplayName: "Enabled mode"}); err != nil {
		t.Fatal(err)
	}
	if events, err := runtime.Repository.ClaimOutbox(context.Background(), "test-worker", time.Now(), time.Minute, 10); err != nil || len(events) != 1 {
		t.Fatalf("enabled-mode pending events = %d, %v", len(events), err)
	}
}

func TestMemoryRuntimeWiresFilesystemAndOptionalWebhooks(t *testing.T) {
	duration := func(value time.Duration) config.Duration { return config.Duration{Duration: value} }
	tenantID := "00000000-0000-7000-8000-000000000001"
	cfg := config.Config{PublisherDomainVerification: config.PublisherDomainVerificationDefaults(), TrustVerification: config.TrustVerificationDefaults(), Ratings: config.Defaults().Ratings, LogLevel: "info", LogFormat: "text", Validation: config.Defaults().Validation, ListingMedia: config.ListingMediaDefaults(),
		Environment: "development",
		Profile:     "runtime-test",
		ServiceName: "open-agentic-catalog-runtime-test",
		HTTP:        config.HTTP{Address: "127.0.0.1:0", PublicBaseURL: "https://catalog.example.com", ReadTimeout: duration(time.Second), RequestTimeout: duration(time.Second), WriteTimeout: duration(7 * time.Second), IdleTimeout: duration(time.Second), ShutdownTimeout: duration(7 * time.Second), MaxBodyBytes: 1 << 20},
		Database:    config.Database{Driver: "memory", MaxConnLifetime: config.Defaults().Database.MaxConnLifetime, MaxConnIdleTime: config.Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: config.Defaults().Database.HealthCheckPeriod},
		Auth: config.Auth{Mode: "development", DevelopmentTokens: map[string]config.DevelopmentUser{
			"admin": {Subject: "admin", TenantID: tenantID, Roles: []identity.Role{identity.RoleTenantAdmin}},
		}},
		Jobs:        config.Jobs{MaxAttempts: 5, RetryCeiling: config.Duration{Duration: time.Hour}},
		Worker:      config.Worker{DependencyRetryInitialDelay: config.Duration{Duration: time.Second}, DependencyRetryCeiling: config.Duration{Duration: 30 * time.Second}, PollInterval: duration(time.Millisecond), LeaseDuration: duration(time.Minute), Concurrency: 1, SourceMaintenanceInterval: duration(15 * time.Minute), HousekeepingInterval: duration(5 * time.Minute), TerminalJobRetention: duration(168 * time.Hour), AcknowledgedOutboxRetention: duration(24 * time.Hour)},
		Federation:  config.Federation{RequestTimeout: duration(time.Second), MaxResponseBytes: 1 << 20, MaxRedirects: 1, MaxFanout: 1, MaxHops: 1, MaxLivePagesPerSource: 1},
		Discovery:   testDiscovery(),
		Publication: config.Publication{ARDProfile: "ard-0.91-aa3e598", AICatalogProfile: "ai-catalog-1.0-b062278"},
		Webhooks:    config.Webhooks{RetryCeiling: config.Duration{Duration: time.Hour}, Enabled: true, MaxAttempts: 3, Timeout: duration(time.Second), PollInterval: duration(time.Millisecond)},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "runtime.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	unsetEnvironment(t, "CATALOG_CURSOR_SIGNING_KEY")
	unsetEnvironment(t, "CATALOG_CURSOR_SIGNING_KEY_FILE")
	unsetEnvironment(t, "CATALOG_WEBHOOK_ENCRYPTION_KEY")
	unsetEnvironment(t, "CATALOG_WEBHOOK_ENCRYPTION_KEY_FILE")
	runtime, err := Build(context.Background(), BuildOptions{ConfigPath: path, Role: "api", Version: "test", Commit: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if runtime.HTTP == nil || runtime.Operations != nil || runtime.Observability == nil || runtime.JobWorker != nil || runtime.Housekeeper != nil || runtime.Webhooks == nil || runtime.WebhookDispatcher != nil || runtime.WebhookDrainer != nil || runtime.OutboxProcessor != nil || runtime.Pool != nil {
		t.Fatalf("incomplete memory runtime: %#v", runtime)
	}
}

func TestRuntimeWorkersExecutePublicationAndRejectStaleSourceJobs(t *testing.T) {
	duration := func(value time.Duration) config.Duration { return config.Duration{Duration: value} }
	tenantID := "00000000-0000-7000-8000-000000000001"
	cfg := config.Config{PublisherDomainVerification: config.PublisherDomainVerificationDefaults(), TrustVerification: config.TrustVerificationDefaults(), Ratings: config.Defaults().Ratings, LogLevel: "info", LogFormat: "text", Validation: config.Defaults().Validation, ListingMedia: config.ListingMediaDefaults(),
		Environment: "test",
		Profile:     "worker-jobs-test",
		ServiceName: "open-agentic-catalog-worker-jobs-test",
		HTTP:        config.HTTP{Address: "127.0.0.1:0", PublicBaseURL: "https://catalog.example.com", ReadTimeout: duration(time.Second), RequestTimeout: duration(time.Second), WriteTimeout: duration(7 * time.Second), IdleTimeout: duration(time.Second), ShutdownTimeout: duration(7 * time.Second), MaxBodyBytes: 1 << 20},
		Database:    config.Database{Driver: "memory", MaxConnLifetime: config.Defaults().Database.MaxConnLifetime, MaxConnIdleTime: config.Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: config.Defaults().Database.HealthCheckPeriod},
		Auth: config.Auth{Mode: "development", DevelopmentTokens: map[string]config.DevelopmentUser{
			"worker": {Subject: "worker", TenantID: tenantID, Roles: []identity.Role{identity.RolePublisher, identity.RoleReader, identity.RoleSourceAdmin}},
		}},
		Jobs:        config.Jobs{MaxAttempts: 5, RetryCeiling: config.Duration{Duration: time.Hour}},
		Worker:      config.Worker{DependencyRetryInitialDelay: config.Duration{Duration: time.Second}, DependencyRetryCeiling: config.Duration{Duration: 30 * time.Second}, PollInterval: duration(time.Millisecond), LeaseDuration: duration(time.Minute), Concurrency: 1, SourceMaintenanceInterval: duration(15 * time.Minute), HousekeepingInterval: duration(5 * time.Minute), TerminalJobRetention: duration(168 * time.Hour), AcknowledgedOutboxRetention: duration(24 * time.Hour)},
		Federation:  config.Federation{RequestTimeout: duration(time.Second), MaxResponseBytes: 1 << 20, MaxRedirects: 1, MaxFanout: 1, MaxHops: 1, MaxLivePagesPerSource: 1},
		Discovery:   testDiscovery(),
		Publication: config.Publication{ARDProfile: "ard-0.91-aa3e598", AICatalogProfile: "ai-catalog-1.0-b062278"},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "worker-jobs.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := Build(context.Background(), BuildOptions{ConfigPath: path, Role: "worker", Version: "test", Commit: "test", Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	actorContext := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "publisher", MembershipID: "00000000-0000-7000-8000-000000000010", RealmID: "00000000-0000-7000-8000-000000000011", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RolePublisher, identity.RoleReader, identity.RoleSourceAdmin}})

	publicationJob, _, err := runtime.Jobs.Enqueue(actorContext, jobs.EnqueueInput{Kind: "publication.generate", Payload: json.RawMessage(`{}`), DeduplicationKey: "publication"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.JobWorker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	completed, err := runtime.Jobs.Get(actorContext, publicationJob.ID)
	if err != nil || completed.State != jobs.StateSucceeded || completed.Progress != 100 || completed.Result == nil {
		t.Fatalf("publication job = %#v, %v", completed, err)
	}
	if completed.Result.Type != "publication_snapshot" || completed.Result.State != "candidate" || completed.Result.Host == "" || completed.Result.Generation != 1 {
		t.Fatalf("publication Job must identify its candidate, not imply activation: %#v", completed.Result)
	}
	resultingSnapshot, err := runtime.Publication.GetSnapshot(actorContext, tenantID, completed.Result.SnapshotID)
	if err != nil || resultingSnapshot.ID != completed.Result.SnapshotID || resultingSnapshot.Host != completed.Result.Host || resultingSnapshot.Generation != completed.Result.Generation || resultingSnapshot.State != publication.SnapshotCandidate {
		t.Fatalf("Job result does not resolve to the exact retained candidate: %#v, %v", resultingSnapshot, err)
	}
	if snapshots, err := runtime.Publication.ListSnapshotSummaries(actorContext, tenantID, publication.SnapshotListRequest{Limit: 10}); err != nil || len(snapshots) != 1 {
		t.Fatalf("publication snapshots = %#v, %v", snapshots, err)
	}

	// User commands cannot enqueue malformed or missing-target Jobs. The
	// owned-store payload fixture above separately proves worker rejection.
	for _, payload := range []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{"sourceId":"missing","sourceRevision":1}`)} {
		if _, _, err := runtime.Jobs.Enqueue(actorContext, jobs.EnqueueInput{Kind: "source.sync", Payload: payload, DeduplicationKey: "invalid-source"}); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
			t.Fatalf("invalid source binding admitted: %v", err)
		}
	}
}

// Issuance and required-mode admission use no DNS. Only the explicit check
// command can call the resolver, which these composition controls never invoke.
func TestPublisherVerificationWiringByRuntimeRole(t *testing.T) {
	for _, role := range []config.Role{config.RoleAPI, config.RoleAll, config.RoleWorker} {
		t.Run(string(role), func(t *testing.T) {
			cfg := localRuntimeConfig()
			cfg.PublisherDomainVerification.Mode = catalog.DomainVerificationRequired
			runtime, err := Build(t.Context(), BuildOptions{ConfigPath: writeRuntimeConfig(t, cfg), Role: role, Version: "test", Commit: "test", Logger: slog.New(slog.DiscardHandler)})
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "domain-runtime-admin", MembershipID: "00000000-0000-7000-8000-000000000093", RealmID: "00000000-0000-7000-8000-000000000094", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}})
			p, err := runtime.Catalog.CreatePublisher(ctx, catalog.CreatePublisherInput{DisplayName: "Domain runtime test", Domains: []string{"catalog.example.com"}})
			if err != nil {
				t.Fatal(err)
			}
			status, err := runtime.Catalog.GetPublisherDomainVerification(ctx, p.ID, "catalog.example.com")
			if err != nil {
				t.Fatal(err)
			}
			if role == "worker" {
				if status.Mode != catalog.DomainVerificationDisabled || runtime.HTTP != nil {
					t.Fatal("worker activated verification")
				}
				if _, err = runtime.Catalog.IssuePublisherDomainChallenge(ctx, p.ID, "catalog.example.com", "Worker has no verifier", status.Version); catalog.ErrorCodeOf(err) != catalog.CodeInvalidTransition {
					t.Fatal("worker issued challenge", err)
				}
				return
			}
			if status.Mode != catalog.DomainVerificationRequired {
				t.Fatal("API did not receive verification policy")
			}
			if _, err = runtime.Catalog.IssuePublisherDomainChallenge(ctx, p.ID, "catalog.example.com", "Issue without DNS", status.Version); err != nil {
				t.Fatal(err)
			}
			if _, err = runtime.Catalog.ApprovePublisherPublicationDomain(ctx, p.ID, "catalog.example.com", "Reject absent proof", p.Revision); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
				t.Fatal("composed approval ignored required policy", err)
			}
		})
	}
}
