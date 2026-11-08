// Summary adapters use existing read ports and recorders, without constructing
// an application, worker, HTTP listener or outbound client. Successful control
// cache policy is owned by securityHeaders and its existing middleware tests.
package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/feedback"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

type pass2IdentitySummaryStore struct {
	identity.Store
	identity.AdministrationStore
	permission func(context.Context, identity.Actor, []identity.PermissionCheck) (identity.PermissionDecision, error)
	summary    func(context.Context, identity.Actor) (identity.AdministrationOverviewRows, error)
	members    func(context.Context, identity.Actor, identity.AuthorizationPageRequest) (identity.MemberContributionSelection, error)
}

func (s pass2IdentitySummaryStore) ResolvePrincipal(c context.Context, p identity.Principal) (identity.Actor, error) {
	return s.AdministrationStore.ResolvePrincipal(c, p)
}
func (s pass2IdentitySummaryStore) CheckPermissions(c context.Context, a identity.Actor, p []identity.PermissionCheck) (identity.PermissionDecision, error) {
	return s.permission(c, a, p)
}
func (s pass2IdentitySummaryStore) ReadAdministrationOverview(c context.Context, a identity.Actor) (identity.AdministrationOverviewRows, error) {
	return s.summary(c, a)
}
func (s pass2IdentitySummaryStore) ListMemberContributions(c context.Context, a identity.Actor, p identity.AuthorizationPageRequest) (identity.MemberContributionSelection, error) {
	return s.members(c, a, p)
}

type pass2FeedbackSummaryStore struct {
	feedback.Store
	read func(context.Context, string) (feedback.OverviewRows, error)
}

func (s pass2FeedbackSummaryStore) ReadOverview(c context.Context, tenant string) (feedback.OverviewRows, error) {
	return s.read(c, tenant)
}

type pass2RatingSummaryStore struct {
	ratings.Store
	read func(context.Context, string) ([]string, error)
}

func (s pass2RatingSummaryStore) ReadOverview(c context.Context, tenant string) ([]string, error) {
	return s.read(c, tenant)
}

type pass2FederationSummaryStore struct {
	federation.Store
	indexed func(context.Context, string) (*int, bool, error)
	sources func(context.Context, string) (federation.SourceOverviewRows, error)
}

func (s pass2FederationSummaryStore) CountIndexedInventory(c context.Context, tenant string) (*int, bool, error) {
	return s.indexed(c, tenant)
}
func (s pass2FederationSummaryStore) ReadSourceOverview(c context.Context, tenant string) (federation.SourceOverviewRows, error) {
	return s.sources(c, tenant)
}

type pass2JobSummaryStore struct {
	jobs.Store
	read func(context.Context, string, jobs.ListRequest, time.Time) (jobs.OverviewRows, error)
}

func (s pass2JobSummaryStore) ReadJobOverview(c context.Context, tenant string, q jobs.ListRequest, at time.Time) (jobs.OverviewRows, error) {
	return s.read(c, tenant, q, at)
}

type pass2WebhookSummaryStore struct {
	webhook.Store
	endpoints  func(context.Context, string) ([]string, error)
	deliveries func(context.Context, string) (webhook.DeliveryOverviewRows, error)
}

func (s pass2WebhookSummaryStore) ReadEndpointOverview(c context.Context, tenant string) ([]string, error) {
	return s.endpoints(c, tenant)
}
func (s pass2WebhookSummaryStore) ReadDeliveryOverview(c context.Context, tenant string) (webhook.DeliveryOverviewRows, error) {
	return s.deliveries(c, tenant)
}

func pass2IdentityService(t *testing.T, store pass2IdentitySummaryStore) *identity.Service {
	t.Helper()
	service, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clock.NewFake(projectionTime), IDs: id.Generator{}, CursorKey: []byte(strings.Repeat("p", 32))})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func pass2Permission(t *testing.T, request *http.Request, allowed bool) func(context.Context, identity.Actor, []identity.PermissionCheck) (identity.PermissionDecision, error) {
	t.Helper()
	return func(ctx context.Context, actor identity.Actor, checks []identity.PermissionCheck) (identity.PermissionDecision, error) {
		if !sameProjectionContext(ctx, request.Context()) || actor.TenantID != projectionTenant || actor.MembershipID != projectionActor(identity.RoleTenantAdmin).MembershipID || !reflect.DeepEqual(checks, []identity.PermissionCheck{{Permission: "access.manage"}}) {
			t.Fatal("administration admission changed actor, context or permission", actor, checks)
		}
		return identity.PermissionDecision{Revision: 8, Allowed: []bool{allowed}}, nil
	}
}

func pass2SummaryScope() catalog.OverviewScope {
	return catalog.OverviewScope{TenantID: projectionTenant, Scope: "current_access", ObservedAt: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC), SelectionLimit: 10000}
}

func pass2SummaryFixture(t *testing.T, name string, request *http.Request, allow bool, read func(context.Context, string) error) (http.HandlerFunc, any) {
	t.Helper()
	server := projectionServer(nil)
	scope := pass2SummaryScope()
	stamp := projectionTime.Add(-time.Hour)
	var err error
	switch name {
	case "administration":
		server.identity = pass2IdentityService(t, pass2IdentitySummaryStore{
			permission: pass2Permission(t, request, allow),
			summary: func(c context.Context, a identity.Actor) (identity.AdministrationOverviewRows, error) {
				return identity.AdministrationOverviewRows{Human: []identity.MembershipState{identity.MembershipActive, identity.MembershipSuspended}, Service: []identity.MembershipState{identity.MembershipOffboarded}, Groups: 3, BuiltinRoles: []bool{true, false, false}, Assignments: 7}, read(c, a.TenantID)
			},
		})
		builtin, custom := 1, 2
		return server.handleOverviewAdministration, identity.AdministrationOverview{Scope: scope,
			Human:        identity.MembershipOverview{Total: catalog.BoundedCount{Value: 2}, States: []identity.MembershipStateCount{{State: identity.MembershipActive, Count: 1}, {State: identity.MembershipSuspended, Count: 1}, {State: identity.MembershipOffboarded}}},
			Service:      identity.MembershipOverview{Total: catalog.BoundedCount{Value: 1}, States: []identity.MembershipStateCount{{State: identity.MembershipActive}, {State: identity.MembershipSuspended}, {State: identity.MembershipOffboarded, Count: 1}}},
			ActiveGroups: catalog.BoundedCount{Value: 3}, ActiveRoles: identity.RoleOverview{Total: catalog.BoundedCount{Value: 3}, Builtin: &builtin, Custom: &custom}, ActiveAssignments: catalog.BoundedCount{Value: 7}}
	case "feedback":
		server.feedback, err = feedback.NewService(pass2FeedbackSummaryStore{read: func(c context.Context, tenant string) (feedback.OverviewRows, error) {
			return feedback.OverviewRows{Categories: []string{"problem", "positive", "problem"}, OldestOpenAt: &stamp}, read(c, tenant)
		}}, feedbackProjectionContext{}, clock.NewFake(projectionTime), id.Generator{})
		if err != nil {
			t.Fatal(err)
		}
		return server.handleOverviewFeedback, feedback.Overview{OverviewScope: scope, Open: catalog.BoundedCount{Value: 3}, Categories: []feedback.CategoryCount{{Category: "problem", Count: 2}, {Category: "documentation"}, {Category: "suggestion"}, {Category: "positive", Count: 1}, {Category: "other"}}, OldestOpenAt: &stamp}
	case "ratings":
		server.ratings, err = ratings.New(pass2RatingSummaryStore{read: func(c context.Context, tenant string) ([]string, error) {
			return []string{"active", "withdrawn", "active", "removed"}, read(c, tenant)
		}}, ratingProjectionAuthority{}, clock.NewFake(projectionTime), id.Generator{})
		if err != nil {
			t.Fatal(err)
		}
		return server.handleOverviewRatings, ratings.Overview{OverviewScope: scope, Total: catalog.BoundedCount{Value: 4}, States: []ratings.StateCount{{State: "active", Count: 2}, {State: "withdrawn", Count: 1}, {State: "removed", Count: 1}}}
	case "indexed", "sources":
		count, history := 7, 1
		problem := federation.SourceSyncProblem{SourceID: projectionItem, DisplayName: "Peer catalog", Revision: 3, Latest: federation.SyncSummary{Revision: 3, State: federation.SnapshotFailed, CompletedAt: stamp}, LastComplete: &federation.SyncSummary{Revision: 2, State: federation.SnapshotComplete, CompletedAt: stamp.Add(-time.Hour)}}
		store := pass2FederationSummaryStore{
			indexed: func(c context.Context, tenant string) (*int, bool, error) { return &count, false, read(c, tenant) },
			sources: func(c context.Context, tenant string) (federation.SourceOverviewRows, error) {
				return federation.SourceOverviewRows{Items: []federation.SourceOverviewRow{{Status: "active", Sync: "complete"}, {Status: "error", Sync: "failed", HistoricalComplete: true}}, Problems: []federation.SourceSyncProblem{problem}}, read(c, tenant)
			},
		}
		server = federationProjectionServer(t, store, nil)
		if name == "indexed" {
			return server.handleOverviewIndexed, catalog.IndexedOverview{OverviewScope: scope, Count: &count}
		}
		return server.handleOverviewSources, federation.SourceOverview{OverviewScope: scope, Total: catalog.BoundedCount{Value: 2}, States: []federation.SourceStateCount{{State: "active", Count: 1}, {State: "paused"}, {State: "error", Count: 1}}, Sync: []federation.SourceStateCount{{State: "not_applicable"}, {State: "not_yet_synced"}, {State: "complete", Count: 1}, {State: "incomplete"}, {State: "failed", Count: 1}}, HistoricalComplete: &history, Problems: []federation.SourceSyncProblem{problem}}
	case "jobs":
		server.jobs, err = jobs.NewService(pass2JobSummaryStore{read: func(c context.Context, tenant string, q jobs.ListRequest, at time.Time) (jobs.OverviewRows, error) {
			if !at.Equal(projectionTime) || q.Limit != 0 || !reflect.DeepEqual(q.Targets, jobs.ListTargets{}) {
				t.Fatal("Job summary substituted its observation time or deployment selection", at, q)
			}
			return jobs.OverviewRows{Items: []jobs.OverviewItem{{Kind: "source.sync", State: jobs.StateQueued}, {Kind: "publication.generate", State: jobs.StateRunning}}, Due: 1, OldestDueAt: &stamp}, read(c, tenant)
		}}, clock.NewFake(projectionTime), id.Generator{})
		if err != nil {
			t.Fatal(err)
		}
		kinds := []jobs.OverviewKind{}
		for _, kind := range []string{"source.sync", jobs.TrustKind, "publication.generate", "semantic.reindex"} {
			states := []jobs.OverviewStateCount{{State: jobs.StateQueued}, {State: jobs.StateRunning}, {State: jobs.StateWaiting}, {State: jobs.StateSucceeded}, {State: jobs.StateFailed}, {State: jobs.StateCancelled}, {State: jobs.StateDeadLetter}}
			if kind == "source.sync" {
				states[0].Count = 1
			}
			if kind == "publication.generate" {
				states[1].Count = 1
			}
			kinds = append(kinds, jobs.OverviewKind{Kind: kind, States: states})
		}
		return server.handleOverviewJobs, jobs.Overview{OverviewScope: scope, Total: catalog.BoundedCount{Value: 2}, Kinds: kinds, Due: catalog.BoundedCount{Value: 1}, OldestDueAt: &stamp}
	case "webhook-endpoints", "webhook-deliveries":
		failed := webhook.FailedEndpoint{DeliveryID: projectionNext, State: webhook.DeliveryWaiting, EndpointID: projectionItem, Name: "Publication receiver", AttemptedAt: stamp}
		store := pass2WebhookSummaryStore{
			endpoints: func(c context.Context, tenant string) ([]string, error) {
				return []string{"active", "disabled", "active"}, read(c, tenant)
			},
			deliveries: func(c context.Context, tenant string) (webhook.DeliveryOverviewRows, error) {
				return webhook.DeliveryOverviewRows{States: []string{"pending", "waiting", "succeeded", "dead_letter"}, FailedEndpoints: []webhook.FailedEndpoint{failed}}, read(c, tenant)
			},
		}
		cipher, cipherErr := webhook.NewAESGCM(make([]byte, 32))
		if cipherErr != nil {
			t.Fatal(cipherErr)
		}
		server.webhooks, err = webhook.NewService(store, cipher, webhookProjectionSender(func(context.Context, webhook.SendRequest) (webhook.SendResult, error) {
			t.Fatal("metadata summaries must never contact a receiver")
			return webhook.SendResult{}, nil
		}), clock.NewFake(projectionTime), id.Generator{})
		if err != nil {
			t.Fatal(err)
		}
		if name == "webhook-endpoints" {
			return server.handleOverviewWebhookEndpoints, webhook.EndpointOverview{OverviewScope: scope, Total: catalog.BoundedCount{Value: 3}, States: []webhook.StateCount{{State: "active", Count: 2}, {State: "disabled", Count: 1}}}
		}
		return server.handleOverviewWebhookDeliveries, webhook.DeliveryOverview{OverviewScope: scope, Total: catalog.BoundedCount{Value: 4}, States: []webhook.StateCount{{State: "pending", Count: 1}, {State: "waiting", Count: 1}, {State: "succeeded", Count: 1}, {State: "dead_letter", Count: 1}}, FailedEndpoints: []webhook.FailedEndpoint{failed}}
	default:
		t.Fatal("unknown summary fixture", name)
		return nil, nil
	}
}

func TestPass2SummaryAdaptersPreserveProjectionAndFailures(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"administration", "feedback", "ratings", "indexed", "sources", "jobs", "webhook-endpoints", "webhook-deliveries"} {
		for _, outcome := range []string{"success", "dependency", "unclassified", "forbidden"} {
			t.Run(name+"/"+outcome, func(t *testing.T) {
				actor := projectionActor(identity.RoleTenantAdmin)
				if outcome == "forbidden" {
					actor.Roles = nil
				}
				request := httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), actor), http.MethodGet, "/api/v1/overview/"+name, nil)
				calls := 0
				handler, want := pass2SummaryFixture(t, name, request, outcome != "forbidden", func(ctx context.Context, tenant string) error {
					calls++
					if tenant != projectionTenant || !sameProjectionContext(ctx, request.Context()) {
						t.Fatal("summary lost its current tenant or request context", tenant)
					}
					if name != "administration" {
						if _, selected := identity.CollectionRevision(ctx); !selected {
							t.Fatal("summary did not establish collection admission")
						}
						if err := identity.RequireCollectionTenant(ctx, tenant); err != nil {
							t.Fatal("summary collection admission lost its tenant", err)
						}
					}
					cause := errors.New("private-fixture summary storage credentials")
					switch outcome {
					case "dependency":
						return catalog.Wrap(catalog.CodeDependencyUnavailable, "private-fixture summary dependency", cause)
					case "unclassified":
						return cause
					default:
						return nil
					}
				})
				response := httptest.NewRecorder()
				handler(response, request)
				wantCalls := 1
				switch outcome {
				case "success":
					requireControlJSON(t, response, http.StatusOK, want)
				case "dependency":
					requireProjectionProblem(t, response, http.StatusServiceUnavailable, catalog.CodeDependencyUnavailable)
				case "unclassified":
					requireProjectionProblem(t, response, http.StatusInternalServerError, catalog.CodeInternal)
				case "forbidden":
					wantCalls = 0
					requireProjectionProblem(t, response, http.StatusForbidden, catalog.CodeForbidden)
				}
				if calls != wantCalls {
					t.Fatal("summary read count", calls, "want", wantCalls)
				}
			})
		}
	}
}
