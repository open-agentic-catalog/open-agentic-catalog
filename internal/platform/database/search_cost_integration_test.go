//go:build integration

package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// Only fixed statement classes and counts survive the observer boundary. The
// isolated pool has no worker or metrics scrapers which could pollute counts.
type searchQueryCounts struct {
	mu     sync.Mutex
	counts map[string]int
}

func (c *searchQueryCounts) ObserveDatabase(operation string, _ error, _ time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[operation]++
}

func (c *searchQueryCounts) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts = make(map[string]int)
}

func newSearchCostFixture(tb testing.TB, documents ...catalog.SearchDocument) (*housekeepingDatabase, *PostgresRepository, *searchQueryCounts, func(int, ...json.RawMessage)) {
	tb.Helper()
	return newSearchCostFixtureWithListings(tb, nil, documents...)
}

// Optional Listing metadata changes only fixture inputs, never retained rows.
func newSearchCostFixtureWithListings(tb testing.TB, listings []catalog.CreateListingRevisionInput, documents ...catalog.SearchDocument) (*housekeepingDatabase, *PostgresRepository, *searchQueryCounts, func(int, ...json.RawMessage)) {
	tb.Helper()
	db := newHousekeepingDatabase(tb) // Reuse the disposable exact-role fixture.
	counts := &searchQueryCounts{}
	counts.reset()
	configuration := db.api.Config().Copy()
	configuration.MaxConns = 1
	configuration.ConnConfig.Tracer = queryTracer{observer: counts, now: time.Now}
	pool, err := pgxpool.NewWithConfig(tb.Context(), configuration)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(pool.Close)
	if err := pool.Ping(tb.Context()); err != nil {
		tb.Fatal(err)
	}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		tb.Fatal(err)
	}
	key := sha256.Sum256([]byte(tb.Name()))
	service, err := catalog.NewService(catalog.Options{Repository: repository, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "search-cost-test"})
	if err != nil {
		tb.Fatal(err)
	}
	provision := func(subject string, roles []identity.Role) context.Context {
		tb.Helper()
		principal, provisionErr := ProvisionIdentity(tb.Context(), db.operator, IdentityProvisioningInput{TenantID: housekeepingTenantA, TenantDisplayName: "Housekeeping tenant A", Issuer: "https://identity.performance.example", TenancyMode: "dedicated", Subject: subject, ActorType: identity.ActorHuman, Roles: roles, Operator: "search-cost-test", Reason: "provision synthetic benchmark identities", AcknowledgeOutbox: true})
		if provisionErr != nil {
			tb.Fatal(provisionErr)
		}
		return postgresFixtureContext(tb, db, tb.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: subject, Roles: roles, MembershipID: principal.MembershipID, RealmID: principal.RealmID, ActorType: principal.ActorType})
	}
	author := provision("search-author", []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor, identity.RolePublisher, identity.RoleReader})
	reviewer := provision("search-reviewer", []identity.Role{identity.RoleReviewer})
	namespaceSlug := "performance"
	if len(documents) > 0 {
		namespaceSlug = "finance"
	}
	namespace, err := service.CreateNamespace(author, catalog.CreateNamespaceInput{Slug: namespaceSlug, DisplayName: "Performance"})
	if err != nil {
		tb.Fatal(err)
	}
	publisher, err := service.CreatePublisher(author, catalog.CreatePublisherInput{DisplayName: "Performance", Domains: []string{"catalog.example.com"}})
	if err != nil {
		tb.Fatal(err)
	}
	publisher, err = service.ApprovePublisherPublicationDomain(author, publisher.ID, "catalog.example.com", "synthetic query cost fixture", publisher.Revision)
	if err != nil {
		tb.Fatal(err)
	}
	seed := func(index int, card ...json.RawMessage) {
		tb.Helper()
		document := catalog.SearchDocument{Identifier: fmt.Sprintf("urn:air:catalog.example.com:performance:skill-%03d", index), DisplayName: fmt.Sprintf("Performance %03d", index), Description: "A synthetic searchable fixture.", Tags: []string{"performance"}}
		if len(documents) > 0 {
			document = documents[index]
		}
		resource, seedErr := service.CreateResource(author, catalog.CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: document.Identifier, Kind: func() catalog.Kind {
			if len(card) > 0 {
				return catalog.KindAgent
			}
			return catalog.KindSkill
		}()})
		if seedErr != nil {
			tb.Fatal(seedErr)
		}
		input := catalog.RepresentationInput{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaSkillMarkdown, Content: "---\nname: performance-skill\ndescription: A synthetic performance fixture.\n---\n# Instructions\nRead metadata only.\n"}
		if len(card) > 0 {
			input = catalog.RepresentationInput{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, Data: card[0]}
		}
		if index%10 == 1 {
			input = catalog.RepresentationInput{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaSkillZIP, PackageData: postgresSkillPackageZIP(tb), Handoff: &catalog.SkillHandoff{Git: &catalog.GitHandoffReference{Repository: "https://git.example.com/team/skill", Commit: strings.Repeat("a", 40), Directory: "postgres-skill"}, OCI: &catalog.OCIHandoffReference{Reference: "registry.example.com/team/skill@sha256:" + strings.Repeat("b", 64)}}}
		}
		_, revision, _, seedErr := service.CreateVersion(author, resource.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{input}})
		if seedErr != nil {
			tb.Fatal(seedErr)
		}
		listing, seedErr := service.CreateListing(author, catalog.CreateListingInput{NamespaceID: namespace.ID, ResourceID: resource.ID})
		if seedErr != nil {
			tb.Fatal(seedErr)
		}
		listingInput := catalog.CreateListingRevisionInput{VersionRevisionID: revision.ID, DisplayName: document.DisplayName, Description: document.Description, PublisherID: publisher.ID, Visibility: catalog.VisibilityTenant, Tags: document.Tags, RepresentativeQueries: document.RepresentativeQueries}
		if len(listings) > 0 {
			listingInput.License, listingInput.Categories = listings[index].License, listings[index].Categories
			if listings[index].PublisherID != "" {
				listingInput.PublisherID = listings[index].PublisherID
			}
		}
		listing, _, seedErr = service.CreateListingRevision(author, listing.ID, listingInput, listing.Revision)
		if seedErr != nil {
			tb.Fatal(seedErr)
		}
		// Follow the latest validation evidence, not only the sealed evidence ID.
		if index == 0 {
			if _, _, _, err := service.RevalidateVersionRevision(author, revision.ID); err != nil {
				tb.Fatal(err)
			}
		}
		submission, seedErr := service.Submit(author, listing.ID)
		if seedErr != nil {
			tb.Fatal(seedErr)
		}
		if _, _, seedErr = service.DecideSubmission(reviewer, submission.ID, "approve", "synthetic fixture reviewed", submission.Revision); seedErr != nil {
			tb.Fatal(seedErr)
		}
		if _, _, _, seedErr = service.Publish(author, listing.ID, catalog.PublishInput{SubmissionID: submission.ID}, listing.Revision); seedErr != nil {
			tb.Fatal(seedErr)
		}
	}
	return db, repository, counts, seed
}

func TestPostgresSearchQueryCostBaseline(t *testing.T) {
	db, repository, counts, seed := newSearchCostFixture(t)
	check := func(total, limit int, tenant string) catalog.CatalogSearchRepositoryPage {
		t.Helper()
		request := catalog.CatalogSearchRepositoryRequest{Query: catalog.SearchQuery{Text: "Performance"}, Limit: limit}
		if _, warmErr := repository.SearchCatalog(t.Context(), tenant, request); warmErr != nil {
			t.Fatal(warmErr)
		}
		counts.reset()
		page, searchErr := repository.SearchCatalog(t.Context(), tenant, request)
		if searchErr != nil {
			t.Fatal(searchErr)
		}
		expected := min(total, limit)
		if page.Total != total || len(page.Items) != expected || page.HasMore != (total > limit) {
			t.Fatalf("invalid bounded page: total=%d items=%d more=%v", page.Total, len(page.Items), page.HasMore)
		}
		// One tenant-context SELECT, generation SELECT, one combined WITH read and two
		// hydration SELECTs for the entire nonempty page, independent of size.
		selects, withs := 2, 1
		if expected > 0 {
			selects += 2
		}
		if counts.counts["select"] != selects || counts.counts["with"] != withs || counts.counts["begin"] != 1 || counts.counts["commit"] != 1 {
			t.Fatalf("query count changed for %d/%d: %v", total, limit, counts.counts)
		}
		for i, item := range page.Items {
			if item.Resource.TenantID != tenant || i > 0 && page.Items[i-1].ListingRevision.DisplayName >= item.ListingRevision.DisplayName {
				t.Fatal("tenant or order contract violated")
			}
		}
		t.Logf("eligible=%d page=%d data_reads=%d context_reads=1 begin=1 commit=1", total, expected, counts.counts["select"]+counts.counts["with"]-1)
		for _, item := range page.Items {
			point, err := repository.GetCatalogRecord(t.Context(), tenant, item.Release.ID)
			if err != nil {
				t.Fatal(err)
			}
			if item.ListingRevision.DisplayName == "Performance 000" {
				if len(point.VersionRevision.Representations) != 1 {
					t.Fatal("retained Card missing")
				}
				var actual map[string]any
				if err := json.Unmarshal(point.VersionRevision.Representations[0].Data, &actual); err != nil {
					t.Fatal(err)
				}
				canonical, err := json.Marshal(actual)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(canonical, largeSearchCostCard(t)) {
					t.Fatal("large exact Card bytes changed")
				}
				if err := catalog.ValidateVersionRevisionIntegrity(point.VersionRevision, point.Evidence); err != nil {
					t.Fatal(err)
				}
			}
			point.RelevanceScore = item.RelevanceScore
			summaryJSON, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			pointJSON, err := json.Marshal(point.Summary())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(summaryJSON, pointJSON) {
				t.Fatal("batched record differs from point-read contract")
			}
		}
		return page
	}
	check(0, 20, housekeepingTenantA)
	for i := 0; i < 101; i++ {
		if i == 0 {
			seed(i, largeSearchCostCard(t))
		} else {
			seed(i)
		}
		switch i + 1 {
		case 1, 20, 100:
			check(i+1, i+1, housekeepingTenantA)
		}
	}
	check(101, 1, housekeepingTenantA)
	check(101, 20, housekeepingTenantA)
	first := check(101, 100, housekeepingTenantA)
	check(101, 101, housekeepingTenantA) // Unified Search's HTTP-100 lookahead.
	check(0, 100, housekeepingTenantB)
	if _, err := repository.SearchCatalog(context.Background(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Limit: 20, ExpectedGeneration: "not-the-generation"}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("stale generation accepted: %v", err)
	}
	if first.Generation == "" {
		t.Fatal("missing stable generation")
	}
	t.Run("keyset continuation", func(t *testing.T) {
		last := first.Items[len(first.Items)-1]
		next, err := repository.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Query: catalog.SearchQuery{Text: "Performance"}, Limit: 100, ExpectedGeneration: first.Generation, After: &catalog.CatalogSearchPosition{Score: last.RelevanceScore, DisplayName: strings.ToLower(last.ListingRevision.DisplayName), ResultID: "local:" + last.Release.ID}})
		if err != nil || next.Total != 101 || len(next.Items) != 1 || next.HasMore || next.Items[0].ListingRevision.DisplayName != "Performance 100" {
			t.Fatalf("keyset continuation differs: items=%d error=%v", len(next.Items), err)
		}
	})
	t.Run("batch tenant boundary and cancellation", func(t *testing.T) {
		for _, pool := range []*pgxpool.Pool{db.api, db.worker} {
			_, err := withTenantRead(t.Context(), pool, housekeepingTenantB, func(tx pgx.Tx) (map[string]*catalog.CatalogSummary, error) {
				return readSearchRecords(t.Context(), tx, housekeepingTenantB, []string{first.Items[0].Release.ID})
			})
			want := catalog.CodeNotFound
			if pool == db.worker {
				// Workers have no directory-label read authority. This API
				// presentation query must not expand their exact privileges.
				want = catalog.CodeForbidden
			}
			if catalog.ErrorCodeOf(err) != want {
				t.Fatalf("cross-tenant batch = %v, want %s", err, want)
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := repository.SearchCatalog(ctx, housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Limit: 20}); err == nil {
			t.Fatal("canceled search succeeded")
		}
		if _, err := repository.GetCatalogRecord(t.Context(), housekeepingTenantA, first.Items[0].Release.ID); err != nil {
			t.Fatalf("pool unusable after cancellation: %v", err)
		}
	})
	t.Run("generation and hydration share a snapshot", func(t *testing.T) {
		_, err := withTenantTx(t.Context(), db.api, housekeepingTenantA, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) (int, error) {
			before, err := readCatalogSearchGeneration(t.Context(), tx, housekeepingTenantA)
			if err != nil {
				return 0, err
			}
			seed(101) // Commit a new generation through a separate API connection.
			items, err := readSearchRecords(t.Context(), tx, housekeepingTenantA, []string{first.Items[0].Release.ID})
			if err != nil {
				return 0, err
			}
			after, err := readCatalogSearchGeneration(t.Context(), tx, housekeepingTenantA)
			if err != nil {
				return 0, err
			}
			if before != after || len(items) != 1 {
				t.Fatal("hydration escaped its snapshot")
			}
			return len(items), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Limit: 20, ExpectedGeneration: first.Generation}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("committed generation change did not stale the cursor: %v", err)
		}
	})
}

// The point path is a retained reference, not a selectable production mode.
// Setup is outside each sub-benchmark; profiles can focus on readSearchRecords
// or readCurrentCatalogRecord to exclude fixture construction allocations.
func BenchmarkPostgresSearchHydration(b *testing.B) {
	db, repository, _, seed := newSearchCostFixture(b)
	for index := 0; index < 101; index++ {
		seed(index)
	}
	page, err := repository.SearchCatalog(b.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Limit: 101})
	if err != nil {
		b.Fatal(err)
	}
	for _, size := range []int{1, 20, 100, 101} {
		ids := make([]string, size)
		for index := range ids {
			ids[index] = page.Items[index].Release.ID
		}
		for _, batch := range []bool{false, true} {
			b.Run(fmt.Sprintf("page=%d/batch=%t", size, batch), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					_, err := withTenantTx(b.Context(), db.api, housekeepingTenantA, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) (int, error) {
						if batch {
							items, err := readSearchRecords(b.Context(), tx, housekeepingTenantA, ids)
							return len(items), err
						}
						for _, releaseID := range ids {
							if _, err := readCurrentCatalogRecord(b.Context(), tx, housekeepingTenantA, releaseID); err != nil {
								return 0, err
							}
						}
						return len(ids), nil
					})
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// A valid 16 MiB Card exercises substantial retained bytes independently of
// collection limits. Inline JSON admission uses the HTTP request ceiling.
func largeSearchCostCard(tb testing.TB) json.RawMessage {
	tb.Helper()
	prefix := `{"name":"Performance","description":"Large retained Card","version":"1.0.0","supportedInterfaces":[{"url":"https://agent.example.com/a2a","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["text/plain"],"defaultOutputModes":["text/plain"],"skills":[{"id":"large","name":"Large","description":"`
	suffix := `","tags":["performance"]}]}`
	var object map[string]any
	if err := json.Unmarshal([]byte(prefix+strings.Repeat("x", catalog.MaxCatalogDocumentBytes-len(prefix)-len(suffix))+suffix), &object); err != nil {
		tb.Fatal(err)
	}
	raw, err := json.Marshal(object)
	if err != nil {
		tb.Fatal(err)
	}
	return raw
}
