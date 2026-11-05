//go:build integration

package database

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// postgresFixtureContext uses retained memberships and the actual PostgreSQL
// evaluator. Bare role projections never stand in for database authorization.
func postgresFixtureContext(tb testing.TB, db *housekeepingDatabase, ctx context.Context, input identity.Actor) context.Context {
	tb.Helper()
	issuer := "https://identity.authorization-fixture.example"
	if input.MembershipID != "" && input.RealmID != "" {
		var retained bool
		if err := db.operator.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenant_memberships WHERE tenant_id=$1 AND id=$2 AND realm_id=$3 AND subject=$4)`, input.TenantID, input.MembershipID, input.RealmID, input.Subject).Scan(&retained); err != nil {
			tb.Fatal(err)
		}
		if retained {
			if err := db.operator.QueryRow(ctx, `SELECT issuer FROM tenant_identity_realms WHERE tenant_id=$1 AND id=$2`, input.TenantID, input.RealmID).Scan(&issuer); err != nil {
				tb.Fatal(err)
			}
		} else {
			input.MembershipID = ""
		}
	}
	if input.MembershipID == "" {
		name := "Housekeeping tenant A"
		if input.TenantID == housekeepingTenantB {
			name = "Housekeeping tenant B"
		}
		typ := input.ActorType
		if typ == "" {
			typ = identity.ActorHuman
		}
		_, err := ProvisionIdentity(ctx, db.operator, IdentityProvisioningInput{TenantID: input.TenantID, TenantDisplayName: name, Issuer: issuer, TenancyMode: "dedicated", Subject: input.Subject, ActorType: typ, Roles: input.Roles, Operator: "authorization-fixture", Reason: "Explicit owned test authority", AcknowledgeOutbox: true})
		if err != nil {
			tb.Fatal(err)
		}
	}
	repo, err := NewPostgresRepository(db.api)
	if err != nil {
		tb.Fatal(err)
	}
	store, err := NewPostgresIdentityStore(repo)
	if err != nil {
		tb.Fatal(err)
	}
	actor, err := store.ResolvePrincipal(ctx, identity.Principal{TenantID: input.TenantID, Issuer: issuer, Subject: input.Subject, Claims: input.Claims})
	if err != nil {
		tb.Fatal(err)
	}
	return identity.WithActor(ctx, actor)
}

type fixtureClaimObserver struct {
	jobs.Store
	at         time.Time
	selectedID string
	err        error
}

func (s *fixtureClaimObserver) Claim(ctx context.Context, owner string, at time.Time, lease time.Duration) (jobs.Job, error) {
	s.at = at
	job, err := s.Store.Claim(ctx, owner, at, lease)
	s.selectedID, s.err = job.ID, err
	return job, err
}

// Worker writes use an actual live claim, not a user membership on BYPASSRLS.
func syncClaimedFixture(tb testing.TB, db *housekeepingDatabase, user context.Context, manager *federation.Manager, sourceID string) (federation.Snapshot, error) {
	tb.Helper()
	repo, err := NewPostgresRepository(db.worker)
	if err != nil {
		return federation.Snapshot{}, err
	}
	store, err := NewPostgresJobStore(repo)
	if err != nil {
		return federation.Snapshot{}, err
	}
	actor, _ := identity.FromContext(user)
	now := time.Now().UTC()
	jobID, err := (id.Generator{}).New(now)
	if err != nil {
		return federation.Snapshot{}, err
	}
	var revision int64
	if err := db.operator.QueryRow(user, `SELECT revision FROM federation_sources WHERE tenant_id=$1 AND id=$2`, actor.TenantID, sourceID).Scan(&revision); err != nil {
		return federation.Snapshot{}, err
	}
	payload, err := json.Marshal(map[string]any{"sourceId": sourceID, "sourceRevision": revision, "trigger": "manual"})
	if err != nil {
		return federation.Snapshot{}, err
	}
	job := jobs.Job{NotBefore: now, ID: jobID, TenantID: actor.TenantID, Kind: "source.sync", State: jobs.StateQueued, Actor: actor.MembershipID, PayloadVersion: 2, Payload: payload, AttemptLimit: 3, CreatedAt: now, UpdatedAt: now}
	if _, _, err = store.Enqueue(user, job); err != nil {
		return federation.Snapshot{}, err
	}
	var snapshot federation.Snapshot
	var failure error
	observed := &fixtureClaimObserver{Store: store}
	worker, err := jobs.NewWorker(observed, clock.Real{}, "owned-authorization-fixture", time.Minute, time.Second, time.Minute, map[string]jobs.Handler{"source.sync": func(ctx context.Context, claimed jobs.Job, _ func(int) error) (any, error) {
		if claimed.ID != jobID {
			failure = errors.New("source sync fixture claimed an unexpected Job")
			return nil, failure
		}
		verify := func(ctx context.Context) error { return jobs.CheckClaimedAttempt(ctx, claimed) }
		ctx, failure = identity.WithInternalJobAuthority(ctx, claimed.Kind, claimed.TenantID, sourceID, claimed.ID, claimed.LeaseOwner, claimed.Attempt, verify)
		if failure != nil {
			return nil, failure
		}
		ctx = identity.WithActor(ctx, identity.Actor{TenantID: claimed.TenantID, Subject: "catalog-worker", ActorType: identity.ActorService})
		snapshot, failure = manager.Sync(ctx, sourceID)
		return jobs.SnapshotReference{ID: snapshot.ID, SourceID: snapshot.SourceID, State: string(snapshot.State)}, failure
	}})
	if err != nil {
		return snapshot, err
	}
	if err = worker.RunOnce(user); err != nil {
		return snapshot, err
	}
	if snapshot.ID == "" && failure == nil {
		retained, lookupErr := store.Get(user, actor.TenantID, jobID)
		tb.Logf("source sync claim evidence: claimMissing=%t selectedExpected=%t lookupSucceeded=%t state=%s claimBeforeDue=%t due=%s claimAt=%s", catalog.ErrorCodeOf(observed.err) == catalog.CodeNotFound, observed.selectedID == jobID, lookupErr == nil, retained.State, observed.at.Before(retained.NotBefore), retained.NotBefore.Format(time.RFC3339Nano), observed.at.Format(time.RFC3339Nano))
		failure = errors.New("source sync fixture did not execute its requested Job")
	}
	return snapshot, failure
}
