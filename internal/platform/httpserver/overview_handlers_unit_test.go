// Overview handler units use metadata-only ports and recorders. They preserve
// tenant-scoped queries and safe response projection without an application.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

type overviewInventoryRepository struct {
	catalog.Repository
	read func(context.Context, string, catalog.InventoryMeasure) (catalog.InventoryCounts, error)
}

func (r overviewInventoryRepository) ReadInventoryCounts(ctx context.Context, tenant string, measure catalog.InventoryMeasure) (catalog.InventoryCounts, error) {
	return r.read(ctx, tenant, measure)
}

type overviewReviewRepository struct {
	catalog.Repository
	read func(context.Context, string, time.Time, time.Time) (catalog.ReviewTimes, error)
}

func (r overviewReviewRepository) ReadReviewTimes(ctx context.Context, tenant string, start, end time.Time) (catalog.ReviewTimes, error) {
	return r.read(ctx, tenant, start, end)
}

type overviewPublicationReader func(context.Context, string, []string) ([]publication.SnapshotSummary, error)

func (read overviewPublicationReader) ReadActiveSummaries(ctx context.Context, tenant string, hosts []string) ([]publication.SnapshotSummary, error) {
	return read(ctx, tenant, hosts)
}

func overviewRequest(t *testing.T, name string, role identity.Role) *http.Request {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/overview/"+name, nil)
	return request.WithContext(identityfixture.WithActor(request.Context(), projectionActor(role)))
}

// The expected scope is fixed independently of the service clock projection.
func overviewExpectedScope(limit int) catalog.OverviewScope {
	return catalog.OverviewScope{TenantID: projectionTenant, Scope: "current_access", ObservedAt: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC), SelectionLimit: limit}
}

func overviewRequireReadContext(t *testing.T, got context.Context, tenant string, request *http.Request) {
	t.Helper()
	if tenant != projectionTenant || !sameProjectionContext(got, request.Context()) {
		t.Fatalf("summary lost tenant or request context: tenant=%q", tenant)
	}
	if _, selected := identity.CollectionRevision(got); !selected {
		t.Fatal("summary did not bind collection admission")
	}
	if err := identity.RequireCollectionTenant(got, tenant); err != nil {
		t.Fatalf("summary collection admission lost its tenant: %v", err)
	}
}

func TestOverviewInventoryHandlerProjection(t *testing.T) {
	t.Parallel()
	scope := overviewExpectedScope(10000)
	for _, tc := range []struct {
		name    string
		measure catalog.InventoryMeasure
		counts  catalog.InventoryCounts
		bind    func(*Server) http.HandlerFunc
		want    any
	}{
		{
			name: "resources", measure: catalog.InventoryResources,
			counts: catalog.InventoryCounts{Total: catalog.BoundedCount{Value: 7}, Kinds: []catalog.KindCount{{Kind: catalog.KindAgent, Count: 3}, {Kind: catalog.KindMCPServer, Count: 2}, {Kind: catalog.KindSkill, Count: 1}, {Kind: catalog.KindPlugin, Count: 1}}},
			bind:   func(s *Server) http.HandlerFunc { return s.handleOverviewResources },
			want:   catalog.ResourceOverview{OverviewScope: scope, Total: catalog.BoundedCount{Value: 7}, Kinds: []catalog.KindCount{{Kind: catalog.KindAgent, Count: 3}, {Kind: catalog.KindMCPServer, Count: 2}, {Kind: catalog.KindSkill, Count: 1}, {Kind: catalog.KindPlugin, Count: 1}}},
		},
		{
			name: "resources-limited", measure: catalog.InventoryResources,
			counts: catalog.InventoryCounts{Total: catalog.BoundedCount{Value: 10000, Limited: true}},
			bind:   func(s *Server) http.HandlerFunc { return s.handleOverviewResources },
			want:   catalog.ResourceOverview{OverviewScope: scope, Total: catalog.BoundedCount{Value: 10000, Limited: true}},
		},
		{
			name: "releases", measure: catalog.InventoryReleases,
			counts: catalog.InventoryCounts{Published: catalog.BoundedCount{Value: 10000, Limited: true}, Deprecated: catalog.BoundedCount{Value: 3}},
			bind:   func(s *Server) http.HandlerFunc { return s.handleOverviewReleases },
			want:   catalog.ReleaseOverview{OverviewScope: scope, Published: catalog.BoundedCount{Value: 10000, Limited: true}, Deprecated: catalog.BoundedCount{Value: 3}},
		},
		{
			name: "namespaces", measure: catalog.InventoryNamespaces,
			counts: catalog.InventoryCounts{Total: catalog.BoundedCount{Value: 6}},
			bind:   func(s *Server) http.HandlerFunc { return s.handleOverviewDirectory(catalog.InventoryNamespaces) },
			want:   catalog.DirectoryOverview{OverviewScope: scope, Count: catalog.BoundedCount{Value: 6}},
		},
		{
			name: "publishers", measure: catalog.InventoryPublishers,
			counts: catalog.InventoryCounts{Total: catalog.BoundedCount{Value: 10000, Limited: true}},
			bind:   func(s *Server) http.HandlerFunc { return s.handleOverviewDirectory(catalog.InventoryPublishers) },
			want:   catalog.DirectoryOverview{OverviewScope: scope, Count: catalog.BoundedCount{Value: 10000, Limited: true}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := overviewRequest(t, tc.name, identity.RoleTenantAdmin)
			calls := 0
			repository := overviewInventoryRepository{read: func(ctx context.Context, tenant string, measure catalog.InventoryMeasure) (catalog.InventoryCounts, error) {
				calls++
				overviewRequireReadContext(t, ctx, tenant, request)
				if measure != tc.measure {
					t.Fatalf("summary measure=%q want=%q", measure, tc.measure)
				}
				return tc.counts, nil
			}}
			server := projectionServer(projectionCatalog(t, repository, nil))
			response := httptest.NewRecorder()
			tc.bind(server)(response, request)
			requireControlJSON(t, response, http.StatusOK, tc.want)
			if calls != 1 {
				t.Fatalf("summary reads=%d want=1", calls)
			}
		})
	}
}

func TestOverviewInventoryHandlerFailures(t *testing.T) {
	t.Parallel()
	for _, handler := range []struct {
		name string
		bind func(*Server) http.HandlerFunc
	}{
		{"resources", func(s *Server) http.HandlerFunc { return s.handleOverviewResources }},
		{"releases", func(s *Server) http.HandlerFunc { return s.handleOverviewReleases }},
		{"namespaces", func(s *Server) http.HandlerFunc { return s.handleOverviewDirectory(catalog.InventoryNamespaces) }},
		{"publishers", func(s *Server) http.HandlerFunc { return s.handleOverviewDirectory(catalog.InventoryPublishers) }},
	} {
		for _, outcome := range []string{"dependency", "unclassified", "missing-reader"} {
			t.Run(handler.name+"/"+outcome, func(t *testing.T) {
				request := overviewRequest(t, handler.name, identity.RoleTenantAdmin)
				calls := 0
				var repository catalog.Repository = overviewInventoryRepository{read: func(ctx context.Context, tenant string, _ catalog.InventoryMeasure) (catalog.InventoryCounts, error) {
					calls++
					overviewRequireReadContext(t, ctx, tenant, request)
					cause := errors.New("private-fixture repository credentials")
					if outcome == "unclassified" {
						return catalog.InventoryCounts{}, cause
					}
					return catalog.InventoryCounts{}, catalog.Wrap(catalog.CodeDependencyUnavailable, "private-fixture inventory diagnostics", cause)
				}}
				if outcome == "missing-reader" {
					repository = projectionRepository{}
				}
				server := projectionServer(projectionCatalog(t, repository, nil))
				response := httptest.NewRecorder()
				handler.bind(server)(response, request)
				status, code := http.StatusServiceUnavailable, catalog.CodeDependencyUnavailable
				if outcome == "unclassified" {
					status, code = http.StatusInternalServerError, catalog.CodeInternal
				}
				requireProjectionProblem(t, response, status, code)
				wantCalls := 1
				if outcome == "missing-reader" {
					wantCalls = 0
				}
				if calls != wantCalls {
					t.Fatalf("summary reads=%d want=%d", calls, wantCalls)
				}
			})
		}
	}
}

func TestOverviewDirectoryPublisherPermissionPrecedesRead(t *testing.T) {
	t.Parallel()
	actor := projectionActor(identity.RoleReader)
	actor.Roles = nil
	actor.Namespaces = map[string][]identity.Role{projectionResource: {identity.RoleReader}}
	ctx := identityfixture.WithActor(t.Context(), actor)
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/overview/namespaces", nil)
	calls := 0
	repository := overviewInventoryRepository{read: func(got context.Context, tenant string, measure catalog.InventoryMeasure) (catalog.InventoryCounts, error) {
		calls++
		overviewRequireReadContext(t, got, tenant, request)
		if measure != catalog.InventoryNamespaces {
			t.Fatal("publisher summary queried without current publisher.read permission")
		}
		return catalog.InventoryCounts{Total: catalog.BoundedCount{Value: 1}}, nil
	}}
	server := projectionServer(projectionCatalog(t, repository, nil))
	control := httptest.NewRecorder()
	server.handleOverviewDirectory(catalog.InventoryNamespaces)(control, request)
	requireControlJSON(t, control, http.StatusOK, catalog.DirectoryOverview{OverviewScope: overviewExpectedScope(10000), Count: catalog.BoundedCount{Value: 1}})
	request = httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/overview/publishers", nil)
	response := httptest.NewRecorder()
	server.handleOverviewDirectory(catalog.InventoryPublishers)(response, request)
	requireProjectionProblem(t, response, http.StatusForbidden, catalog.CodeForbidden)
	if calls != 1 {
		t.Fatalf("directory reads=%d want only the authorized namespace read", calls)
	}
}

func TestOverviewReviewHandlerProjection(t *testing.T) {
	t.Parallel()
	request := overviewRequest(t, "reviews", identity.RoleTenantAdmin)
	start := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	pendingAt := projectionTime.Add(-time.Hour)
	calls := 0
	repository := overviewReviewRepository{read: func(ctx context.Context, tenant string, from, to time.Time) (catalog.ReviewTimes, error) {
		calls++
		overviewRequireReadContext(t, ctx, tenant, request)
		if !from.Equal(start) || !to.Equal(projectionTime) {
			t.Fatalf("review interval=%v..%v want=%v..%v", from, to, start, projectionTime)
		}
		return catalog.ReviewTimes{
			Pending: []time.Time{pendingAt}, ApprovedUnreleased: 2, OldestPendingAt: &pendingAt,
			Submitted: []time.Time{start.Add(2 * time.Hour), projectionTime.Add(-2 * time.Hour)},
			Decisions: []catalog.ReviewDecisionTime{{SubmittedAt: projectionTime.Add(-2 * time.Hour), DecidedAt: pendingAt, State: catalog.SubmissionApproved}},
		}, nil
	}}
	server := projectionServer(projectionCatalog(t, repository, nil))
	response := httptest.NewRecorder()
	server.handleOverviewReviews(response, request)
	// The contract includes all 30 UTC dates, including zero-valued days and
	// the partial current date. Counts and the one-hour median are independent.
	days := make([]catalog.ReviewDay, 30)
	for i := range days {
		days[i] = catalog.ReviewDay{Date: start.Add(time.Duration(i) * 24 * time.Hour).Format(time.DateOnly), Partial: i == 29, Submitted: new(int), Approved: new(int), ChangesRequired: new(int), Rejected: new(int)}
	}
	*days[0].Submitted, *days[29].Submitted, *days[29].Approved = 1, 1, 1
	median := float64(3600)
	requireControlJSON(t, response, http.StatusOK, catalog.ReviewOverview{
		OverviewScope: overviewExpectedScope(10000), Pending: catalog.BoundedCount{Value: 1}, ApprovedUnreleased: catalog.BoundedCount{Value: 2},
		OldestPendingAt: &pendingAt, PendingAge: &catalog.ReviewAgeCounts{Under24Hours: 1}, StartAt: start, EndAt: projectionTime,
		Submissions: catalog.BoundedCount{Value: 2}, Decisions: catalog.BoundedCount{Value: 1}, Days: days, MedianSeconds: &median,
	})
	if calls != 1 {
		t.Fatalf("review summary reads=%d want=1", calls)
	}
}

func TestOverviewReviewHandlerFailures(t *testing.T) {
	t.Parallel()
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing-reader=%t", missing), func(t *testing.T) {
			request := overviewRequest(t, "reviews", identity.RoleTenantAdmin)
			calls := 0
			var repository catalog.Repository = overviewReviewRepository{read: func(ctx context.Context, tenant string, _, _ time.Time) (catalog.ReviewTimes, error) {
				calls++
				overviewRequireReadContext(t, ctx, tenant, request)
				return catalog.ReviewTimes{}, catalog.Wrap(catalog.CodeDependencyUnavailable, "private-fixture review metadata", errors.New("private-fixture database failure"))
			}}
			if missing {
				repository = projectionRepository{}
			}
			server := projectionServer(projectionCatalog(t, repository, nil))
			response := httptest.NewRecorder()
			server.handleOverviewReviews(response, request)
			requireProjectionProblem(t, response, http.StatusServiceUnavailable, catalog.CodeDependencyUnavailable)
			if missing && calls != 0 || !missing && calls != 1 {
				t.Fatalf("review summary reads=%d missing=%t", calls, missing)
			}
		})
	}
}

func TestOverviewPublicationHandlerTenantProjection(t *testing.T) {
	t.Parallel()
	request := overviewRequest(t, "publication", identity.RoleTenantAdmin)
	activatedAt := projectionTime.Add(-time.Hour)
	current := publication.SnapshotSummary{ID: projectionItem, TenantID: projectionTenant, Host: "current.example.test", Audience: publication.AudienceAnonymous, Generation: 3, ARDProfile: "ard-current", AICatalogProfile: "ai-current", State: publication.SnapshotActive, ETag: `"` + strings.Repeat("a", 64) + `"`, CreatedAt: projectionTime.Add(-2 * time.Hour), ActivatedAt: &activatedAt}
	mismatched := current
	mismatched.ID, mismatched.Host, mismatched.Audience = projectionNext, "mismatch.example.test", publication.AudienceAuthenticated
	mismatched.ARDProfile, mismatched.AICatalogProfile = "ard-previous", "ai-previous"
	foreign := current
	foreign.ID, foreign.TenantID = projectionResource, projectionNext
	retired := current
	retired.ID, retired.Host, retired.State = "00000000-0000-7000-8000-000000000021", "unpublished.example.test", publication.SnapshotRetired
	server := projectionServer(nil)
	server.ardProfile, server.aiCatalogProfile = "ard-current", "ai-current"
	server.discovery = map[string]DiscoveryHost{
		"current.example.test":         {TenantID: projectionTenant, Mode: DiscoveryAnonymous},
		"disabled.example.test":        {TenantID: projectionTenant, Mode: DiscoveryDisabled},
		"mismatch.example.test":        {TenantID: projectionTenant, Mode: DiscoveryAnonymous},
		"unpublished.example.test":     {TenantID: projectionTenant, Mode: DiscoveryAuthenticated},
		"private-fixture.example.test": {TenantID: projectionNext, Mode: DiscoveryAnonymous},
	}
	calls := 0
	server.publicationOverview = overviewPublicationReader(func(ctx context.Context, tenant string, hosts []string) ([]publication.SnapshotSummary, error) {
		calls++
		overviewRequireReadContext(t, ctx, tenant, request)
		slices.Sort(hosts)
		if !reflect.DeepEqual(hosts, []string{"current.example.test", "mismatch.example.test", "unpublished.example.test"}) {
			t.Fatalf("publication queried disabled or foreign hosts: %v", hosts)
		}
		// Foreign metadata must not replace the current tenant's valid summary;
		// a retained non-active row must not make a host appear published.
		return []publication.SnapshotSummary{current, mismatched, foreign, retired}, nil
	})
	response := httptest.NewRecorder()
	server.handleOverviewPublication(response, request)
	requireControlJSON(t, response, http.StatusOK, publication.Overview{OverviewScope: overviewExpectedScope(64), Hosts: []publication.HostOverview{
		{Host: "current.example.test", Mode: "anonymous", Status: "recorded", Active: &current, Mismatches: []string{}},
		{Host: "disabled.example.test", Mode: "disabled", Status: "disabled", Mismatches: []string{}},
		{Host: "mismatch.example.test", Mode: "anonymous", Status: "recorded", Active: &mismatched, Mismatches: []string{"audience", "ard_profile", "ai_catalog_profile"}},
		{Host: "unpublished.example.test", Mode: "authenticated", Status: "unpublished", Mismatches: []string{}},
	}})
	if calls != 1 {
		t.Fatalf("publication summary reads=%d want=1", calls)
	}
}

func TestOverviewPublicationInactiveHostsSkipRead(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		discovery map[string]DiscoveryHost
		hosts     []publication.HostOverview
	}{
		{name: "none", hosts: []publication.HostOverview{}},
		{name: "other-tenant", discovery: map[string]DiscoveryHost{"private-fixture.example.test": {TenantID: projectionNext, Mode: DiscoveryAnonymous}}, hosts: []publication.HostOverview{}},
		{name: "disabled", discovery: map[string]DiscoveryHost{"disabled.example.test": {TenantID: projectionTenant, Mode: DiscoveryDisabled}}, hosts: []publication.HostOverview{{Host: "disabled.example.test", Mode: "disabled", Status: "disabled", Mismatches: []string{}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := projectionServer(nil)
			server.discovery = tc.discovery
			server.publicationOverview = overviewPublicationReader(func(context.Context, string, []string) ([]publication.SnapshotSummary, error) {
				t.Fatal("publication summary performed an unnecessary lookup")
				return nil, nil
			})
			response := httptest.NewRecorder()
			server.handleOverviewPublication(response, overviewRequest(t, "publication", identity.RoleTenantAdmin))
			requireControlJSON(t, response, http.StatusOK, publication.Overview{OverviewScope: overviewExpectedScope(64), Hosts: tc.hosts})
		})
	}
}

func TestOverviewPublicationHandlerFailures(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"dependency", "unclassified", "missing-reader", "forbidden"} {
		t.Run(outcome, func(t *testing.T) {
			role := identity.RoleTenantAdmin
			if outcome == "forbidden" {
				role = identity.RoleReader
			}
			request := overviewRequest(t, "publication", role)
			server := projectionServer(nil)
			server.discovery = map[string]DiscoveryHost{"catalog.example.test": {TenantID: projectionTenant, Mode: DiscoveryAnonymous}}
			calls := 0
			server.publicationOverview = overviewPublicationReader(func(ctx context.Context, tenant string, _ []string) ([]publication.SnapshotSummary, error) {
				calls++
				overviewRequireReadContext(t, ctx, tenant, request)
				cause := errors.New("private-fixture snapshot reader diagnostics")
				if outcome == "unclassified" {
					return nil, cause
				}
				return nil, catalog.Wrap(catalog.CodeDependencyUnavailable, "private-fixture unavailable publication", cause)
			})
			status, code, wantCalls := http.StatusServiceUnavailable, catalog.CodeDependencyUnavailable, 1
			switch outcome {
			case "unclassified":
				status, code = http.StatusInternalServerError, catalog.CodeInternal
			case "missing-reader":
				server.publicationOverview, wantCalls = nil, 0
			case "forbidden":
				status, code, wantCalls = http.StatusForbidden, catalog.CodeForbidden, 0
			}
			response := httptest.NewRecorder()
			server.handleOverviewPublication(response, request)
			requireProjectionProblem(t, response, status, code)
			if calls != wantCalls {
				t.Fatalf("publication reads=%d want=%d", calls, wantCalls)
			}
		})
	}
}
