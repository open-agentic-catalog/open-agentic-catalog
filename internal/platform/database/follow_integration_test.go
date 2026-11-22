//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"slices"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

type followFixtureAuthority struct {
	*catalog.Service
}

func (followFixtureAuthority) Admit(context.Context, personal.Reference) error { return nil }

// Let another committed Catalog command win after service admission and before
// the real personal transaction takes its locks.
type beforeFollowSave struct {
	personal.Store
	before func()
}

func (b beforeFollowSave) Save(ctx context.Context, a identity.Actor, item personal.Item, expected int64, remove bool, e audit.Record) (personal.Item, error) {
	b.before()
	return b.Store.Save(ctx, a, item, expected, remove, e)
}

func TestPostgresListingFollow(t *testing.T) {
	f := newMCPToolFixture(t)
	target := f.seed(t, 0, "Follow target", "", false)
	other := f.seed(t, 1, "Independent target", "", false)
	memberships, err := NewPostgresIdentityStore(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte(t.Name()))
	auth, err := identity.NewService(identity.ServiceOptions{Store: memberships, Clock: clock.Real{}, IDs: id.Generator{}, CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	subject := "scoped-follower"
	_, err = auth.GrantRole(f.author(t), identity.GrantRoleInput{Subject: subject, ActorType: identity.ActorHuman, Role: identity.RoleServiceClient, Reason: "Follow fixture"}, "provision")
	if err != nil {
		t.Fatal(err)
	}
	a, err := memberships.ResolvePrincipal(t.Context(), identity.Principal{TenantID: target.Resource.TenantID, Issuer: "https://identity.performance.example", Subject: subject})
	if err != nil {
		t.Fatal(err)
	}
	grant := func(permission identity.Permission, ns string) identity.AccessAssignment {
		t.Helper()
		r, err := auth.SaveRoleDefinition(f.author(t), "", 0, identity.RoleDefinitionInput{Name: "Follow " + string(permission), Permissions: []identity.Permission{permission}, Reason: "Private follow fixture"}, false, "role")
		if err != nil {
			t.Fatal(err)
		}
		assign, err := auth.GrantAuthorizationAssignment(f.author(t), identity.AccessAssignmentInput{RoleDefinitionID: r.ID, MembershipID: a.MembershipID, NamespaceID: ns, Reason: "Private follow fixture"}, "grant")
		if err != nil {
			t.Fatal(err)
		}
		return assign
	}
	grant("library.own", "")
	access := grant("resource.read", f.namespace)
	ctx := identity.WithActor(t.Context(), a)
	st, err := NewPostgresPersonalStore(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	service, err := personal.New(st, followFixtureAuthority{f.service}, clock.Real{}, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	input := personal.Input{Type: "follow", Label: "Private label", ListingID: personal.ListingID(target.Listing.ID)}
	observed := catalog.ReleaseObservation{ReleaseID: target.Release.ID, Revision: target.Release.Revision}
	// Start both creates before either caller knows a private item ID.
	var items [2]personal.Item
	var errs [2]error
	var wg sync.WaitGroup
	start := make(chan struct{})
	for n := range items {
		wg.Go(func() {
			<-start
			local := input
			if n == 1 {
				local.Label = "Other private label"
			}
			items[n], errs[n] = service.SaveObserved(ctx, "", 0, local, false, &observed)
		})
	}
	close(start)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || items[0].ID != items[1].ID || items[0].Existing == items[1].Existing || items[0].Label != items[1].Label {
		t.Fatalf("duplicate race: %+v %v", items, errs)
	}
	item := items[0]
	input.Label = item.Label
	independent, err := service.Save(ctx, "", 0, personal.Input{Type: "follow", Label: "Restored preference", ListingID: personal.ListingID(other.Listing.ID)}, false)
	if err != nil {
		t.Fatal(err)
	}
	check := func(id, want string) {
		t.Helper()
		r, err := service.ResolveFollow(ctx, id)
		if err != nil || r.Change != want || (r.Current == nil) != (want == "") {
			t.Fatalf("check %s: %+v %v", want, r, err)
		}
	}
	check(item.ID, "unchanged")
	check(independent.ID, "unacknowledged")
	_, revision, _, err := f.service.CreateVersion(f.author(t), target.Resource.ID, catalog.CreateVersionInput{Version: "2.0.0", Representations: f.representations(t, "2.0.0", "", false)})
	if err != nil {
		t.Fatal(err)
	}
	next := f.publish(t, target.Listing, revision, "Follow replacement")
	check(item.ID, "changed")
	check(independent.ID, "unacknowledged")
	if _, err = service.SaveObserved(ctx, item.ID, item.Revision, input, false, &observed); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal("stale acknowledgement", err)
	}
	now := catalog.ReleaseObservation{ReleaseID: next.ID, Revision: next.Revision}
	item, err = service.SaveObserved(ctx, item.ID, item.Revision, input, false, &now)
	if err != nil {
		t.Fatal(err)
	}
	check(item.ID, "unchanged")
	racing, err := personal.New(beforeFollowSave{st, func() {
		if _, err := f.service.TransitionRelease(f.author(t), next.ID, catalog.ReleaseDeprecated, "Changed support status", "", nil, next.Revision); err != nil {
			t.Fatal(err)
		}
	}}, followFixtureAuthority{f.service}, clock.Real{}, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := racing.SaveObserved(ctx, item.ID, item.Revision, input, false, &now); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal("accepted observation superseded after admission", err)
	}
	check(item.ID, "changed")
	if _, err = auth.RevokeAuthorizationAssignment(f.author(t), access.ID, access.Revision, "Revoke target read", "revoke"); err != nil {
		t.Fatal(err)
	}
	check(item.ID, "")
	if _, err = service.Save(ctx, "", 0, input, false); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("unreadable restore", err)
	}
	_, err = withTenantRead(t.Context(), f.db.operator, a.TenantID, func(tx pgx.Tx) (bool, error) {
		rows, more, err := scanRestorePersonal(t.Context(), tx, a.TenantID, "", 10)
		if err != nil {
			return false, err
		}
		if more || len(rows) != 2 {
			t.Fatalf("restore follow inventory: %v", rows)
		}
		for _, row := range rows {
			if row.finding != nil {
				t.Fatalf("valid private follow reported invalid: %+v", row.finding)
			}
		}
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	input.Label = "New owner label"
	item, err = service.Save(ctx, item.ID, item.Revision, input, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = auth.GrantAuthorizationAssignment(f.author(t), identity.AccessAssignmentInput{RoleDefinitionID: access.RoleDefinitionID, MembershipID: a.MembershipID, NamespaceID: f.namespace, Reason: "Read target again"}, "regain"); err != nil {
		t.Fatal(err)
	}
	check(item.ID, "changed")
	for _, wrong := range []context.Context{f.author(t), semanticDisclosureActor(t, f.db, housekeepingTenantB)} {
		if _, err := service.Get(wrong, item.ID); err == nil {
			t.Fatal("foreign owner read follow")
		}
	}
	if _, err = service.Save(ctx, item.ID, item.Revision, personal.Input{}, true); err != nil {
		t.Fatal(err)
	}
	check(independent.ID, "unacknowledged")
	withdrawingAuthor := f.author(t)
	t.Run("target retired between observation and acknowledgement", func(t *testing.T) {
		observed := catalog.ReleaseObservation{ReleaseID: other.Release.ID, Revision: other.Release.Revision}
		racing, err := personal.New(beforeFollowSave{st, func() {
			if _, err := f.service.TransitionRelease(withdrawingAuthor, observed.ReleaseID, catalog.ReleaseRetired, "Retire the followed release", "", nil, observed.Revision); err != nil {
				t.Fatal(err)
			}
		}}, followFixtureAuthority{f.service}, clock.Real{}, id.Generator{})
		if err != nil {
			t.Fatal(err)
		}
		edited := independent.Input
		edited.Label = "Rename while acknowledging"
		if _, err := racing.SaveObserved(ctx, independent.ID, independent.Revision, edited, false, &observed); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
			t.Fatalf("acknowledgement of the retired target returned %v", err)
		}
		resolution, err := service.ResolveFollow(ctx, independent.ID)
		if err != nil || resolution.Status != "unavailable" || resolution.Current != nil {
			t.Fatalf("retired target resolved to %+v, error %v", resolution, err)
		}
		retained, err := service.Get(ctx, independent.ID)
		if err != nil || retained.Revision != independent.Revision || retained.Label != independent.Label || retained.Acknowledged != nil {
			t.Fatalf("failed acknowledgement changed the saved follow: %+v, %v", retained, err)
		}
		var actions []string
		if err := f.db.operator.QueryRow(ctx, `SELECT ARRAY(SELECT action FROM audit_events
 WHERE tenant_id=$1 AND target_type='personal_item' AND target_id=$2 ORDER BY id)`,
			a.TenantID, independent.ID).Scan(&actions); err != nil || !slices.Equal(actions, []string{"personal.item.created"}) {
			t.Fatalf("saved follow history after failed acknowledgement: %v, %v", actions, err)
		}
	})
}
