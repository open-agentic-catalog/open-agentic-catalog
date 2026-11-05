package database

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/feedback"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

// These units exercise row decoding and query bindings through the existing
// transaction port. The pool is an identity token; no connection or SQL runs.
func TestOverviewRepositoryProjectionsAndReadFailures(t *testing.T) {
	actor := authorizationUnitActor()
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	revision := int64(3)
	id := "00000000-0000-7000-8000-000000000030"
	counts := catalog.InventoryCounts{Total: catalog.BoundedCount{Value: 26}, Kinds: []catalog.KindCount{{Kind: catalog.KindAgent, Count: 7}, {Kind: catalog.KindMCPServer, Count: 7}, {Kind: catalog.KindSkill, Count: 6}, {Kind: catalog.KindPlugin, Count: 6}}}
	snapshot := publication.SnapshotSummary{ID: id, TenantID: actor.TenantID, Host: "catalog.example", Audience: publication.AudienceAnonymous, Generation: 3, ARDProfile: "ard-v1", AICatalogProfile: "ai-v1", State: publication.SnapshotActive, ETag: "etag", CreatedAt: at, ActivatedAt: &at}
	problem := federation.SourceSyncProblem{SourceID: id, DisplayName: "Peer", Revision: 4, Latest: federation.SyncSummary{Revision: 4, State: federation.SnapshotFailed, CompletedAt: at}, LastComplete: &federation.SyncSummary{Revision: revision, State: federation.SnapshotComplete, CompletedAt: at}}
	work := catalog.PersonalWorkItem{ID: "resource:" + id, Type: "resource", ResourceID: id, NamespaceID: id, Kind: catalog.KindAgent, State: "empty_resource"}
	indexedCount := 5
	type indexedProjection struct {
		Count   *int
		Limited bool
	}
	type selection struct {
		name    string
		read    func(context.Context, *PostgresRepository) (any, error)
		queries [][][]any
		rows    [][]any
		want    any
	}
	cases := []selection{
		{"indexed", func(c context.Context, p *PostgresRepository) (any, error) {
			count, limited, err := (&PostgresFederationStore{repository: p}).CountIndexedInventory(c, actor.TenantID)
			return indexedProjection{count, limited}, err
		}, nil, [][]any{{int64(3), false}, {37, indexedCount}}, indexedProjection{Count: &indexedCount}},
		{"limited indexed", func(c context.Context, p *PostgresRepository) (any, error) {
			count, limited, err := (&PostgresFederationStore{repository: p}).CountIndexedInventory(c, actor.TenantID)
			return indexedProjection{count, limited}, err
		}, nil, [][]any{{int64(3), false}, {10001, 7}}, indexedProjection{Limited: true}},
		{"resources", func(c context.Context, p *PostgresRepository) (any, error) {
			return p.ReadInventoryCounts(c, actor.TenantID, catalog.InventoryResources)
		}, nil, [][]any{{26, 7, 7, 6, 6}}, counts},
		{"limited resources", func(c context.Context, p *PostgresRepository) (any, error) {
			return p.ReadInventoryCounts(c, actor.TenantID, catalog.InventoryResources)
		}, nil, [][]any{{10001, 2501, 2500, 2500, 2500}}, catalog.InventoryCounts{Total: catalog.BoundedCount{Value: 10000, Limited: true}}},
		{"releases", func(c context.Context, p *PostgresRepository) (any, error) {
			return p.ReadInventoryCounts(c, actor.TenantID, catalog.InventoryReleases)
		}, nil, [][]any{{2}, {1}}, catalog.InventoryCounts{Published: catalog.BoundedCount{Value: 2}, Deprecated: catalog.BoundedCount{Value: 1}}},
		{"namespaces", func(c context.Context, p *PostgresRepository) (any, error) {
			return p.ReadInventoryCounts(c, actor.TenantID, catalog.InventoryNamespaces)
		}, nil, [][]any{{3}}, catalog.InventoryCounts{Total: catalog.BoundedCount{Value: 3}}},
		{"publishers", func(c context.Context, p *PostgresRepository) (any, error) {
			return p.ReadInventoryCounts(c, actor.TenantID, catalog.InventoryPublishers)
		}, nil, [][]any{{4}}, catalog.InventoryCounts{Total: catalog.BoundedCount{Value: 4}}},
		{"reviews", func(c context.Context, p *PostgresRepository) (any, error) {
			return p.ReadReviewTimes(c, actor.TenantID, at.AddDate(0, 0, -29), at)
		}, [][][]any{{{at}}, {{at}}, {{at, at, catalog.SubmissionApproved}}}, [][]any{{at}, {2}}, catalog.ReviewTimes{Pending: []time.Time{at}, OldestPendingAt: &at, ApprovedUnreleased: 2, Submitted: []time.Time{at}, Decisions: []catalog.ReviewDecisionTime{{SubmittedAt: at, DecidedAt: at, State: catalog.SubmissionApproved}}}},
		{"jobs", func(c context.Context, p *PostgresRepository) (any, error) {
			return (&PostgresJobStore{repository: p}).ReadJobOverview(c, actor.TenantID, jobs.ListRequest{}, at)
		}, [][][]any{{{"source.sync", jobs.StateQueued}}}, [][]any{{1}, {at}}, jobs.OverviewRows{Items: []jobs.OverviewItem{{Kind: "source.sync", State: jobs.StateQueued}}, Due: 1, OldestDueAt: &at}},
		{"sources", func(c context.Context, p *PostgresRepository) (any, error) {
			return (&PostgresFederationStore{repository: p}).ReadSourceOverview(c, actor.TenantID)
		}, [][][]any{{{"active", "failed", true}}, {{id, "Peer", int64(4), federation.SnapshotFailed, at, &revision, &at}}}, nil, federation.SourceOverviewRows{Items: []federation.SourceOverviewRow{{Status: "active", Sync: "failed", HistoricalComplete: true}}, Problems: []federation.SourceSyncProblem{problem}}},
		{"feedback", func(c context.Context, p *PostgresRepository) (any, error) {
			return (&PostgresFeedbackStore{repository: p}).ReadOverview(c, actor.TenantID)
		}, [][][]any{{{"documentation"}}, {{at}}}, nil, feedback.OverviewRows{Categories: []string{"documentation"}, OldestOpenAt: &at}},
		{"ratings", func(c context.Context, p *PostgresRepository) (any, error) {
			return (&PostgresRatingsStore{repository: p}).ReadOverview(c, actor.TenantID)
		}, [][][]any{{{"active"}, {"withdrawn"}}}, nil, []string{"active", "withdrawn"}},
		{"webhook endpoints", func(c context.Context, p *PostgresRepository) (any, error) {
			return (&PostgresWebhookStore{repository: p}).ReadEndpointOverview(c, actor.TenantID)
		}, [][][]any{{{"active"}}}, nil, []string{"active"}},
		{"webhook deliveries", func(c context.Context, p *PostgresRepository) (any, error) {
			return (&PostgresWebhookStore{repository: p}).ReadDeliveryOverview(c, actor.TenantID)
		}, [][][]any{{{"waiting"}}, {{id, "Receiver", at, id, webhook.DeliveryWaiting}}}, nil, webhook.DeliveryOverviewRows{States: []string{"waiting"}, FailedEndpoints: []webhook.FailedEndpoint{{EndpointID: id, Name: "Receiver", AttemptedAt: at, DeliveryID: id, State: webhook.DeliveryWaiting}}}},
		{"publication", func(c context.Context, p *PostgresRepository) (any, error) {
			return p.ReadActiveSummaries(c, actor.TenantID, []string{"catalog.example"})
		}, [][][]any{{{snapshot.ID, snapshot.TenantID, snapshot.Host, snapshot.Audience, snapshot.Generation, snapshot.ARDProfile, snapshot.AICatalogProfile, snapshot.State, snapshot.ETag, snapshot.CreatedAt, snapshot.ActivatedAt}}}, nil, []publication.SnapshotSummary{snapshot}},
		{"personal work", func(c context.Context, p *PostgresRepository) (any, error) {
			return p.ListPersonalWork(c, actor.TenantID, catalog.PersonalWorkRequest{Queue: "drafts", Limit: 2})
		}, [][][]any{{{work.ID, work.Type, work.ResourceID, work.NamespaceID, work.Kind, "", "", "", "", "", "", "", work.State, ""}}}, nil, []catalog.PersonalWorkItem{work}},
		{"administration", func(c context.Context, p *PostgresRepository) (any, error) {
			return (&PostgresIdentityStore{repository: p}).ReadAdministrationOverview(c, actor)
		}, [][][]any{{{identity.MembershipActive}}, {{identity.MembershipSuspended}}, {{true}, {false}}}, [][]any{{int64(7), []bool{true}}, {2}, {3}}, identity.AdministrationOverviewRows{Human: []identity.MembershipState{identity.MembershipActive}, Service: []identity.MembershipState{identity.MembershipSuspended}, Groups: 2, BuiltinRoles: []bool{true, false}, Assignments: 3}},
	}
	for _, test := range cases {
		modes := []string{"success", "wrong tenant"}
		for i := range test.queries {
			for _, stage := range []string{"query", "scan", "iteration"} {
				modes = append(modes, fmt.Sprintf("%s/%d", stage, i))
			}
		}
		for i := range test.rows {
			modes = append(modes, fmt.Sprintf("row/%d", i))
		}
		for _, mode := range modes {
			t.Run(test.name+"/"+mode, func(t *testing.T) {
				failure := errors.New("overview read interrupted")
				tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
				opened := []*identityScriptedRows{}
				for i, values := range test.queries {
					rows := &identityScriptedRows{values: values}
					result := identityQueryResult{rows: rows}
					switch mode {
					case fmt.Sprintf("query/%d", i):
						result = identityQueryResult{err: failure}
					case fmt.Sprintf("scan/%d", i):
						rows.values = [][]any{{}}
					case fmt.Sprintf("iteration/%d", i):
						rows.err = failure
					}
					opened = append(opened, rows)
					tx.queryResults = append(tx.queryResults, result)
				}
				for i, values := range test.rows {
					row := scriptedValueRow(values)
					if mode == fmt.Sprintf("row/%d", i) {
						row = scannerFunc(func(...any) error { return failure })
					}
					tx.queryRows = append(tx.queryRows, row)
				}
				pool := &pgxpool.Pool{}
				ctx := identityRepositoryContext(pool, tx)
				ctx = identity.WithActor(ctx, actor)
				commandTransactionFrom(ctx).authorizationShared = true
				if mode == "wrong tenant" {
					commandTransactionFrom(ctx).tenantID = "00000000-0000-7000-8000-000000000099"
				}
				value, err := test.read(ctx, &PostgresRepository{pool: pool})
				if mode == "success" {
					if err != nil || !reflect.DeepEqual(value, test.want) {
						t.Fatalf("projection=%+v want=%+v error=%v", value, test.want, err)
					}
					if len(tx.queryResults) != 0 || len(tx.queryRows) != 0 {
						t.Fatal("selected reads were not completed")
					}
				} else {
					if err == nil || value != nil && !reflect.ValueOf(value).IsZero() {
						t.Fatalf("failed read disclosed a partial projection: %+v %v", value, err)
					}
					if mode != "wrong tenant" && !strings.HasPrefix(mode, "scan/") && !errors.Is(err, failure) {
						t.Fatalf("read lost its failure cause: %v", err)
					}
				}
				for i := range tx.queryStatements {
					if mode == fmt.Sprintf("query/%d", i) {
						continue
					}
					if !opened[i].closed {
						t.Fatalf("result %d was not closed", i)
					}
				}
				arguments := slices.Concat(tx.queryArguments, tx.queryRowArguments)
				statements := slices.Concat(tx.queryStatements, tx.queryRowStatements)
				for i, args := range arguments {
					if len(args) == 0 || args[0] != actor.TenantID {
						t.Fatalf("read lost its tenant binding: %+v", args)
					}
					if bound := regexp.MustCompile(`LIMIT \$([0-9]+)`).FindStringSubmatch(statements[i]); bound != nil {
						index, parseErr := strconv.Atoi(bound[1])
						want := 10001
						if test.name == "personal work" {
							want = 2
						}
						if parseErr != nil || index < 1 || index > len(args) || args[index-1] != want {
							t.Fatalf("selection lost its independent work bound: %+v", args)
						}
					}
				}
				if mode == "wrong tenant" && len(tx.queryStatements)+len(tx.queryRowStatements) != 0 {
					t.Fatal("wrong-tenant context reached a query")
				}
			})
		}
	}
}
