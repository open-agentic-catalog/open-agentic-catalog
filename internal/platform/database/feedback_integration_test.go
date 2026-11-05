//go:build integration

// Real PostgreSQL qualification uses only the suite-owned disposable schema.
package database

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/feedback"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestPostgresFeedbackLifecycleIsolationAndConcurrentServices(t *testing.T) {
	db := newHousekeepingDatabase(t)
	repo, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	c := clock.NewFake(time.Now().UTC().Truncate(time.Microsecond))
	key := sha256.Sum256([]byte(t.Name()))
	resources, err := catalog.NewService(catalog.Options{Repository: repo, Clock: c, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "feedback-test"})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: housekeepingTenantA, TenantDisplayName: "Housekeeping tenant A", Issuer: "https://feedback.example", TenancyMode: "dedicated", Subject: "feedback-admin", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}, Operator: "feedback-test", Reason: "synthetic feedback qualification", AcknowledgeOutbox: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "feedback-admin", MembershipID: principal.MembershipID, RealmID: principal.RealmID, ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}})
	ns, err := resources.CreateNamespace(ctx, catalog.CreateNamespaceInput{Slug: "feedback", DisplayName: "Feedback"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresFeedbackStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	one, err := feedback.NewService(store, repo, c, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	otherStore, err := NewPostgresFeedbackStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	two, err := feedback.NewService(otherStore, repo, c, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill, catalog.KindPlugin} {
		t.Run(string(kind), func(t *testing.T) {
			resource, err := resources.CreateResource(ctx, catalog.CreateResourceInput{NamespaceID: ns.ID, CanonicalIdentifier: "urn:air:catalog.example.com:feedback:" + string(kind), Kind: kind})
			if err != nil {
				t.Fatal(err)
			}
			r, err := one.Create(ctx, feedback.CreateInput{ResourceID: resource.ID, Category: "problem", Comment: "private retained feedback"})
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			out := make(chan error, 2)
			for _, s := range []*feedback.Service{one, two} {
				wg.Go(func() {
					_, err := s.Change(ctx, r.ID, 1, "address", feedback.EditInput{}, "private response")
					out <- err
				})
			}
			wg.Wait()
			close(out)
			success := 0
			for err := range out {
				if err == nil {
					success++
				} else if catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
					t.Fatal(err)
				}
			}
			if success != 1 {
				t.Fatalf("committed competing writes=%d", success)
			}
			_, err = withTenantTx(ctx, db.api, housekeepingTenantA, pgx.TxOptions{}, func(tx pgx.Tx) (struct{}, error) {
				_, err := tx.Exec(ctx, `UPDATE item_feedback SET revision=revision+1,state='removed' WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, r.ID)
				return struct{}{}, err
			})
			if err == nil {
				t.Fatal("terminal state retained text")
			}
			read, err := one.Get(ctx, r.ID)
			if err != nil || read.Revision != 2 || read.State != "addressed" {
				t.Fatalf("failed SQL changed state: %#v %v", read, err)
			}
			_, err = withTenantRead(identity.WithActor(ctx, identity.Actor{TenantID: housekeepingTenantB}), db.api, housekeepingTenantB, func(tx pgx.Tx) (string, error) {
				var id string
				err := tx.QueryRow(ctx, `SELECT id FROM item_feedback WHERE id=$1`, r.ID).Scan(&id)
				return id, err
			})
			if err == nil {
				t.Fatal("cross-tenant RLS disclosure")
			}
			if _, err = db.worker.Exec(ctx, `SELECT comment FROM item_feedback LIMIT 1`); err == nil {
				t.Fatal("worker can read feedback")
			}
			if _, err = one.Change(ctx, r.ID, 2, "remove", feedback.EditInput{}, ""); err != nil {
				t.Fatal(err)
			}
			read, err = two.Get(ctx, r.ID)
			if err != nil || read.Comment != "" || read.Response != "" || read.State != "removed" {
				t.Fatalf("removed text returned: %#v %v", read, err)
			}
			page, err := one.List(ctx, feedback.ListInput{ResourceID: resource.ID, State: "removed", Limit: 1}, true)
			if err != nil || len(page) != 1 {
				t.Fatalf("filtered page %d: %v", len(page), err)
			}
			var leaked bool
			if err := db.operator.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM audit_events WHERE tenant_id=$1 AND target_id=$2 AND details::text LIKE '%private%')`, housekeepingTenantA, r.ID).Scan(&leaked); err != nil || leaked {
				t.Fatalf("private audit data: %v %v", leaked, err)
			}
		})
	}
	tx, err := db.operator.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	schema, _, err := currentCatalogSchema(t.Context(), tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyRestoreConstraints(t.Context(), tx, schema); err != nil {
		t.Fatal(fmt.Errorf("feedback restore constraints: %w", err))
	}
	if err := verifyRestoreTriggers(t.Context(), tx, schema); err != nil {
		t.Fatal(err)
	}
	records, more, err := scanRestoreFeedback(t.Context(), tx, housekeepingTenantA, "", 100)
	if err != nil || more || len(records) != 4 {
		t.Fatalf("restore feedback records=%d more=%v err=%v", len(records), more, err)
	}
	for _, r := range records {
		if r.finding != nil {
			t.Fatal(r.finding)
		}
	}
}
