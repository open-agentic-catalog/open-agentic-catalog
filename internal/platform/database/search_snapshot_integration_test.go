//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

type snapshotQueryKey struct{}

// Only this isolated one-connection test pool interleaves commits. The writer
// uses a separate owned connection, so no sleep or scheduler race is needed.
type snapshotCommitTracer struct {
	observer queryTracer
	commit   func()
}

func (s *snapshotCommitTracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	ctx = s.observer.TraceQueryStart(ctx, conn, data)
	return context.WithValue(ctx, snapshotQueryKey{}, strings.Contains(data.SQL, "SELECT generation FROM catalog_search_generations"))
}

func (s *snapshotCommitTracer) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	s.observer.TraceQueryEnd(ctx, conn, data)
	if selected, _ := ctx.Value(snapshotQueryKey{}).(bool); selected && data.Err == nil && s.commit != nil {
		s.commit()
	}
}

type snapshotLiveBoundary struct {
	*federation.Manager
	check func()
}

func (s snapshotLiveBoundary) SearchLiveCatalog(ctx context.Context, request catalog.FederatedCatalogSearchRequest) (catalog.FederatedCatalogSearchPage, error) {
	s.check()
	return s.Manager.SearchLiveCatalog(ctx, request)
}

func TestPostgresCatalogSnapshotSurvivesInterleavedCommits(t *testing.T) {
	db, _, _, seed := newSearchCostFixture(t)
	for i := 0; i < 3; i++ {
		seed(i)
	}
	commits := 0
	commit := func() {
		// The generation is an ordinary transactional row, not a PostgreSQL
		// sequence. Reproduce several committed invalidations after each read.
		for range 3 {
			if _, err := db.operator.Exec(t.Context(), "UPDATE catalog_search_generations SET generation=generation+1 WHERE tenant_id=$1", housekeepingTenantA); err != nil {
				t.Fatal(err)
			}
			commits++
		}
	}
	counts := &searchQueryCounts{}
	counts.reset()
	tracer := &snapshotCommitTracer{observer: queryTracer{observer: counts, now: time.Now}}
	configuration := db.api.Config().Copy()
	configuration.MaxConns, configuration.ConnConfig.Tracer = 1, tracer
	pool, err := pgxpool.NewWithConfig(t.Context(), configuration)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresFederationStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	manager := seedLexicalSource(t, db, repository, []catalog.SearchDocument{{Identifier: "urn:air:remote.example:finance:snapshot", DisplayName: "Performance remote", Description: "Performance snapshot fixture", Tags: []string{}, RepresentativeQueries: []string{}}})
	tracer.commit = commit
	local := catalog.CatalogSearchRepositoryRequest{Limit: 1}
	indexed := catalog.FederatedCatalogSearchRequest{Limit: 1}
	// Negative control: the old separate transactions observe different
	// generations under this exact deterministic interleave.
	before, err := repository.SearchCatalog(t.Context(), housekeepingTenantA, local)
	if err != nil {
		t.Fatal(err)
	}
	after, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, indexed)
	if err != nil || before.Generation == after.Generation || commits != 6 {
		t.Fatalf("separate-read race was not reproduced: %q %q commits=%d err=%v", before.Generation, after.Generation, commits, err)
	}
	counts.reset()
	snapshot, err := repository.SearchCatalogSnapshot(t.Context(), housekeepingTenantA, local, indexed)
	if err != nil || snapshot.Local.Generation != snapshot.Indexed.Generation || snapshot.Local.Total != 3 || len(snapshot.Local.Items) != 1 || snapshot.Indexed.IndexedTotal != 1 || len(snapshot.Indexed.Indexed) != 1 || commits != 12 {
		t.Fatalf("shared snapshot: %+v commits=%d err=%v", snapshot, commits, err)
	}
	if counts.counts["select"] != 5 || counts.counts["with"] != 3 || counts.counts["begin"] != 1 || counts.counts["commit"] != 1 || counts.counts["rollback"] != 0 {
		t.Fatalf("shared snapshot statement budget: %v", counts.counts)
	}
	tracer.commit = nil
	key := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{Repository: repository, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "snapshot-test"})
	if err != nil {
		t.Fatal(err)
	}
	liveCalls := 0
	search, err := catalog.NewSearchService(service, snapshotLiveBoundary{Manager: manager, check: func() {
		liveCalls++
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		// With MaxConns=1 this cannot acquire a connection if the durable
		// transaction is still held across live discovery.
		if _, err := pool.Exec(ctx, "SELECT 1"); err != nil {
			t.Fatalf("live work started before snapshot commit: %v", err)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	reader := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "snapshot-reader", Roles: []identity.Role{identity.RoleReader}})
	page, err := search.Search(reader, catalog.SearchQuery{Text: "Performance", Limit: 1})
	if err != nil || page.DurableTotal != 4 || len(page.Items) != 1 || page.NextCursor == "" || liveCalls != 1 {
		t.Fatalf("composed snapshot first page: %+v %v", page, err)
	}
	next, err := search.Search(reader, catalog.SearchQuery{Text: "Performance", Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID == page.Items[0].ID {
		t.Fatalf("snapshot continuation: %+v %v", next, err)
	}
	// A zero count skips only that repository's page, not the other repository
	// or live discovery. The live adapter above also verifies commit ordering.
	for _, item := range []struct {
		text              string
		total, statements int
	}{{"synthetic", 3, 2}, {"remote", 1, 3}, {"absenttest", 0, 2}} {
		counts.reset()
		beforeLive := liveCalls
		result, err := search.Search(reader, catalog.SearchQuery{Text: item.text, Limit: 20})
		if err != nil || result.Items == nil || len(result.Items) != item.total || result.DurableTotal != item.total || result.NextCursor != "" || liveCalls != beforeLive+1 {
			t.Fatalf("empty-side composed search %q: %+v %v", item.text, result, err)
		}
		// One durable snapshot and one subsequent live-topology read.
		if counts.counts["with"] != item.statements || counts.counts["begin"] != 2 || counts.counts["commit"] != 2 {
			t.Fatalf("empty-side statement budget %q: %v", item.text, counts.counts)
		}
	}
	for _, side := range []string{"local", "indexed"} {
		l, i := local, indexed
		if side == "local" {
			l.ExpectedGeneration = "stale"
		} else {
			i.ExpectedGeneration = "stale"
		}
		if _, err := repository.SearchCatalogSnapshot(t.Context(), housekeepingTenantA, l, i); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("stale %s generation accepted: %v", side, err)
		}
	}
	other, err := repository.SearchCatalogSnapshot(t.Context(), housekeepingTenantB, local, indexed)
	if err != nil || other.Local.Total != 0 || len(other.Local.Items) != 0 || other.Indexed.IndexedTotal != 0 || len(other.Indexed.Indexed) != 0 {
		t.Fatalf("cross-tenant snapshot: %+v %v", other, err)
	}
}
