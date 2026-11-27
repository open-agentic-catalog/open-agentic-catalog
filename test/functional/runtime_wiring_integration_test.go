//go:build integration

package functional_test

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	platformruntime "github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/runtime"
)

// A scrape must describe the pool of the role that served it. Only the worker
// can also report aggregate Job counts from the database.
func qualifyRuntimePoolMetrics(t *testing.T, ctx context.Context, f processFixture, cfg config.Config, apiDSN, workerDSN string) {
	t.Helper()
	for _, role := range []struct {
		name, dsn, address string
		max                int32
	}{
		{"api", apiDSN, cfg.HTTP.Address, 3},
		{"worker", workerDSN, cfg.Worker.OperationsAddress, 5},
	} {
		t.Run(role.name, func(t *testing.T) {
			fixture := f
			fixture.t = t
			settings := cfg
			settings.Database.MaxConnections = role.max
			stop := fixture.start(ctx, role.name, role.dsn, fixture.configuration("metrics-"+role.name, settings), role.address)
			defer stop()
			readAcquires := func() int64 {
				t.Helper()
				body := string(fixture.request(ctx, role.address, "GET", "/metrics", "", "", 200))
				if !strings.Contains(body, "\nopen_agentic_catalog_postgresql_pool_connections{state=\"maximum\"} "+strconv.Itoa(int(role.max))+"\n") {
					t.Fatalf("%s metrics omitted configured pool maximum %d", role.name, role.max)
				}
				if role.name == "worker" {
					if !strings.Contains(body, "\nopen_agentic_catalog_operational_snapshot_available 1\n") || !strings.Contains(body, "\nopen_agentic_catalog_jobs{state=\"queued\"} ") {
						t.Fatal("worker metrics omitted the current PostgreSQL Job snapshot")
					}
				} else if strings.Contains(body, "\nopen_agentic_catalog_operational_snapshot_available ") || strings.Contains(body, "\nopen_agentic_catalog_jobs{") {
					t.Fatal("API reported worker-only database aggregates")
				}
				for line := range strings.Lines(body) {
					if raw, ok := strings.CutPrefix(line, "open_agentic_catalog_postgresql_pool_acquires_total "); ok {
						count, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
						if err != nil || count < 1 {
							t.Fatalf("%s returned an invalid pool acquisition count %q", role.name, raw)
						}
						return count
					}
				}
				t.Fatalf("%s metrics omitted the pool acquisition counter", role.name)
				return 0
			}
			before := readAcquires()
			fixture.request(ctx, role.address, "GET", "/readyz", "", "", 200)
			if after := readAcquires(); after <= before {
				t.Fatalf("pool acquisitions stayed at %d after PostgreSQL readiness check (before %d)", after, before)
			}
		})
	}
}

func qualifyFailedBuildPoolCleanup(t *testing.T, f processFixture, owner *pgx.Conn, cfg config.Config, apiDSN string) {
	t.Helper()
	f.t = t
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	address, err := url.Parse(apiDSN)
	if err != nil {
		t.Fatal("parse fixture API address")
	}
	const tag = "catalog-process-failed-build"
	q := address.Query()
	q.Set("application_name", tag)
	address.RawQuery = q.Encode()
	cfg.Database.DSN = address.String()
	cfg.Database.MaxConnLifetime.Duration = time.Hour
	cfg.Database.MaxConnIdleTime.Duration = time.Hour
	directory := t.TempDir()
	cfg.CredentialResolution = config.CredentialResolution{Enabled: true, Directory: directory}

	awaitSessions := func(present bool) {
		t.Helper()
		deadline, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		for {
			var observed bool
			err := owner.QueryRow(deadline, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
				WHERE datname=current_database() AND usename='catalog_api' AND application_name=$1)`, tag).Scan(&observed)
			if err != nil {
				t.Fatal("could not observe fixture API sessions", err)
			}
			if observed == present {
				return
			}
			select {
			case <-deadline.Done():
				t.Fatalf("timed out waiting for API session presence=%t", present)
			case <-time.After(25 * time.Millisecond):
			}
		}
	}
	build := func() (*platformruntime.Runtime, error) {
		return platformruntime.Build(ctx, platformruntime.BuildOptions{Role: config.RoleAPI, ConfigPath: f.configuration("pool-cleanup", cfg), LogOutput: io.Discard})
	}
	func() {
		healthy, err := build()
		if err != nil {
			t.Fatal(err)
		}
		defer healthy.Close()
		awaitSessions(true)
	}()
	awaitSessions(false)
	cfg.CredentialResolution.Directory = filepath.Join(directory, "missing")
	failed, err := build()
	if failed != nil {
		failed.Close()
		t.Fatal("missing credential directory unexpectedly admitted API")
	}
	if err == nil || !strings.Contains(err.Error(), "configure federation credential resolution") {
		t.Fatalf("expected failure after database admission, got %v", err)
	}
	awaitSessions(false)
}

// A durable Job crosses a configuration change. Run each attempt before the
// process fixture starts any independent worker that could claim it instead.
func qualifyPublicationJobHostChange(t *testing.T, f processFixture, owner *pgx.Conn, cfg config.Config, apiDSN, workerDSN string) {
	t.Helper()
	f.t = t
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	const host = "publication-job.example.test"
	tenant := f.tenantID
	other := cfg.Auth.DevelopmentTokens["other-tenant-admin"].TenantID
	if other == "" || other == tenant {
		t.Fatal("two distinct publication tenants required")
	}
	hostConfig := func(assignedTenant string) config.Config {
		c := cfg
		c.Discovery.Hosts = append(slices.Clone(cfg.Discovery.Hosts), config.DiscoveryHost{Host: host, Mode: "anonymous", PublisherDomain: host, TenantID: assignedTenant})
		return c
	}
	apiConfig := hostConfig(tenant)
	apiConfig.Database.DSN = apiDSN
	api, err := platformruntime.Build(ctx, platformruntime.BuildOptions{Role: config.RoleAPI, ConfigPath: f.configuration("publication-api", apiConfig), LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	actor, err := api.Authenticator.Authenticate(ctx, "local-admin")
	if err != nil {
		t.Fatal(err)
	}
	request := identity.WithActor(ctx, actor)
	countCandidates := func() int {
		t.Helper()
		var n int
		if err := owner.QueryRow(ctx, "SELECT count(*) FROM publication_snapshots WHERE host=$1 AND tenant_id IN ($2,$3)", host, tenant, other).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, assigned := range []string{tenant, other} {
		before := countCandidates()
		payload := json.RawMessage(`{"host":"` + host + `"}`)
		job, reused, err := api.Jobs.Enqueue(request, jobs.EnqueueInput{Kind: "publication.generate", DeduplicationKey: "process-publication-" + assigned, Payload: payload})
		if err != nil || reused || job.State != jobs.StateQueued || job.TenantID != tenant {
			t.Fatalf("publication Job admission failed: reused=%t err=%v", reused, err)
		}
		worker := hostConfig(assigned)
		stop := f.start(ctx, "worker", workerDSN, f.configuration("publication-worker", worker), worker.Worker.OperationsAddress)
		var state jobs.State
		var class jobs.FailureClass
		var result []byte
		var attempt int
		wait, cancel := context.WithTimeout(ctx, 15*time.Second)
		for {
			if err := owner.QueryRow(wait, "SELECT state, failure_class, result, attempt FROM jobs WHERE tenant_id=$1 AND id=$2", tenant, job.ID).Scan(&state, &class, &result, &attempt); err != nil {
				cancel()
				t.Fatalf("reading publication Job %s (last state %s): %v", job.ID, state, err)
			}
			if state != jobs.StateQueued && state != jobs.StateRunning {
				break
			}
			select {
			case <-wait.Done():
				cancel()
				t.Fatalf("publication Job %s remained %s", job.ID, state)
			case <-time.After(25 * time.Millisecond):
			}
		}
		cancel()
		stop()
		after := countCandidates()
		if assigned != tenant {
			if state != jobs.StateFailed || class != jobs.FailurePolicyDenied || len(result) != 0 || attempt != 1 || after != before {
				t.Fatalf("reassigned host: Job state=%s class=%s attempt=%d result=%s candidate count %d -> %d", state, class, attempt, result, before, after)
			}
			continue
		}
		var snapshot jobs.SnapshotReference
		decodeProcessJSON(t, result, &snapshot)
		if state != jobs.StateSucceeded || attempt != 1 || after != before+1 || snapshot.Host != host || snapshot.State != "candidate" || !identity.ValidTenantID(snapshot.ID) {
			t.Fatalf("unchanged host: Job state=%s attempt=%d result=%s candidate count %d -> %d", state, attempt, result, before, after)
		}
		var generation int64
		if err := owner.QueryRow(ctx, "SELECT generation FROM publication_snapshots WHERE id=$1 AND tenant_id=$2 AND host=$3 AND state='candidate'", snapshot.ID, tenant, host).Scan(&generation); err != nil || generation != snapshot.Generation {
			t.Fatalf("retained candidate generation=%d, result=%d, err=%v", generation, snapshot.Generation, err)
		}
	}
}
