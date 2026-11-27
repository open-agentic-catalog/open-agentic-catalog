//go:build integration

package functional_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

// Start before the fixture populates discovery. Reindexing an empty Catalog
// must complete without inference, even when a different profile has pending work.
func qualifyReindexProfileChange(t *testing.T, ctx context.Context, f processFixture, owner *pgx.Conn, cfg config.Config, apiDSN, workerDSN string) {
	t.Helper()
	f.t = t
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("empty Catalog contacted the embedding provider")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer provider.Close()
	cfg.SemanticSearch.Enabled = true
	cfg.SemanticSearch.Model = "fixture"
	cfg.SemanticSearch.Dimensions = 3
	cfg.SemanticSearch.Endpoint = provider.URL + "/v1/embeddings"
	cfg.SemanticSearch.AllowHTTP = true
	cfg.SemanticSearch.AllowedAddressPrefixes = []string{"127.0.0.1/32"}
	cfg.SemanticSearch.ProfileRevision = "original"
	originalProfile := cfg.SemanticSearch.EmbeddingProfile()
	original := f.configuration("semantic-original", cfg)
	cfg.SemanticSearch.ProfileRevision = "replacement"
	replacementProfile := cfg.SemanticSearch.EmbeddingProfile()
	replacement := f.configuration("semantic-replacement", cfg)

	// Neither role may create the optional schema during startup. The same
	// configurations must start once the schema owner applies it.
	for _, child := range []struct{ role, dsn string }{{"api", apiDSN}, {"worker", workerDSN}} {
		startup, cancel := context.WithTimeout(ctx, 8*time.Second)
		f.run(startup, f.command(startup, "catalogd", child.dsn, child.role, "--config", original), 1, "stage=startup")
		cancel()
	}
	var absent bool
	if err := owner.QueryRow(ctx, "SELECT to_regclass('public.catalog_semantic_schema') IS NULL").Scan(&absent); err != nil || !absent {
		t.Fatalf("optional schema before operator setup: absent=%t err=%v", absent, err)
	}
	f.run(ctx, f.command(ctx, "catalogctl", f.ownerDSN, "migrate", "apply", "--semantic", "--config", original), 0)

	enqueue := func(key string) string {
		t.Helper()
		var job jobs.View
		decodeProcessJSON(t, f.request(ctx, cfg.HTTP.Address, "POST", "/api/v1/catalog:reindex", "local-admin", `{"reason":"Rebuild fixture index"}`, 202, map[string]string{"Idempotency-Key": key}), &job)
		if !identity.ValidTenantID(job.ID) || job.TenantID != f.tenantID || job.Kind != "semantic.reindex" || job.State != jobs.StateQueued {
			t.Fatalf("unexpected reindex admission: %+v", job)
		}
		return job.ID
	}
	onlyListed := func(want string) {
		t.Helper()
		var page struct {
			Items      []jobs.View `json:"items"`
			NextCursor string      `json:"nextCursor"`
		}
		decodeProcessJSON(t, f.request(ctx, cfg.HTTP.Address, "GET", "/api/v1/jobs?kind=semantic.reindex", "local-admin", "", 200), &page)
		if len(page.Items) != 1 || page.Items[0].ID != want || page.NextCursor != "" {
			t.Fatalf("expected only current-profile Job %s, got %+v", want, page)
		}
	}
	complete := func(path, id, wantProfile string) {
		t.Helper()
		stop := f.start(ctx, "worker", workerDSN, path, cfg.Worker.OperationsAddress)
		defer stop()
		wait, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		for {
			var job jobs.View
			decodeProcessJSON(t, f.request(wait, cfg.HTTP.Address, "GET", "/api/v1/jobs/"+id, "local-admin", "", 200), &job)
			if job.ID != id || job.TenantID != f.tenantID || job.Kind != "semantic.reindex" {
				t.Fatalf("wrong reindex Job: %+v", job)
			}
			if job.State != jobs.StateQueued && job.State != jobs.StateRunning {
				if job.State != jobs.StateSucceeded || job.Attempt != 1 || job.Result == nil || job.Result.Type != "semantic_index" || job.Result.GenerationID != id || job.Result.Members == nil || *job.Result.Members != 0 {
					t.Fatalf("incorrect empty index result: %+v", job)
				}
				break
			}
			select {
			case <-wait.Done():
				t.Fatalf("reindex Job %s remained %s", id, job.State)
			case <-time.After(25 * time.Millisecond):
			}
		}
		var ready bool
		if err := owner.QueryRow(ctx, `SELECT g.state='ready' AND g.scope='control'
				AND g.profile=$4 AND g.profile=j.payload->>'profile' AND g.source_generation=j.payload->>'sourceGeneration'
				AND g.dimensions=$3 AND NOT EXISTS (SELECT 1 FROM semantic_members m
					WHERE m.tenant_id=g.tenant_id AND m.generation_id=g.id)
			FROM semantic_generations g JOIN jobs j ON (g.tenant_id,g.id)=(j.tenant_id,j.id)
			WHERE g.tenant_id=$1 AND g.id=$2`, f.tenantID, id, cfg.SemanticSearch.Dimensions, wantProfile).Scan(&ready); err != nil || !ready {
			t.Fatalf("Job %s has no matching ready empty generation: ready=%t err=%v", id, ready, err)
		}
	}
	stop := f.start(ctx, "api", apiDSN, original, cfg.HTTP.Address)
	first := enqueue("original-profile")
	onlyListed(first)
	stop()

	stop = f.start(ctx, "api", apiDSN, replacement, cfg.HTTP.Address)
	second := enqueue("replacement-profile")
	if first == second {
		t.Fatal("profile change reused the original Job")
	}
	onlyListed(second)
	f.request(ctx, cfg.HTTP.Address, "GET", "/api/v1/jobs/"+first, "local-admin", "", 404)
	complete(replacement, second, replacementProfile)
	var untouched bool
	if err := owner.QueryRow(ctx, `SELECT state='queued' AND attempt=0 AND result IS NULL
		FROM jobs WHERE tenant_id=$1 AND id=$2`, f.tenantID, first).Scan(&untouched); err != nil || !untouched {
		t.Fatalf("replacement worker changed original Job: untouched=%t err=%v", untouched, err)
	}
	stop()

	stop = f.start(ctx, "api", apiDSN, original, cfg.HTTP.Address)
	defer stop()
	onlyListed(first)
	complete(original, first, originalProfile)
}
