//go:build integration

package database

import (
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"strings"
	"testing"
	"time"
)

func TestPostgresGroupArchiveRequiresExplicitCleanup(t *testing.T) {
	db, store, _, admin, alice, _ := authorizationCoreFixture(t)
	var now time.Time
	if err := db.operator.QueryRow(t.Context(), "SELECT clock_timestamp()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	service, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clock.NewFake(now.Add(time.Second)), IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), admin)
	clean, err := service.SaveAccessGroup(ctx, "", 0, identity.GroupInput{Name: "Empty archive control", Reason: "Create owned positive control"}, "", "empty")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.SaveAccessGroup(ctx, clean.ID, clean.Revision, identity.GroupInput{Reason: "Archive empty control"}, identity.GroupArchived, "empty-archive"); err != nil {
		t.Fatal("valid empty archive", err)
	}
	group, err := service.SaveAccessGroup(ctx, "", 0, identity.GroupInput{Name: "Archive relation control", Reason: "Create owned member control"}, "", "group")
	if err != nil {
		t.Fatal(err)
	}
	member, err := service.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Add existing active member", "add", false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SaveAccessGroup(ctx, group.ID, group.Revision, identity.GroupInput{Reason: "Check documented archive restriction"}, identity.GroupArchived, "archive")
	if identity.ErrorCodeOf(err) != identity.CodeConflict {
		t.Fatalf("active-member prerequisite not enforced: %v", err)
	}

	var active bool
	if e := db.operator.QueryRow(t.Context(), `SELECT state='active' FROM access_groups WHERE tenant_id=$1 AND id=$2`, admin.TenantID, group.ID).Scan(&active); e != nil || !active {
		t.Fatal("failed archive changed group", active, e)
	}
	// Owner SQL must enforce the same prerequisite even outside the API store.
	if _, e := db.operator.Exec(t.Context(), `UPDATE access_groups SET state='archived',revision=revision+1,updated_at=greatest(clock_timestamp(),updated_at+interval '1 second') WHERE tenant_id=$1 AND id=$2`, admin.TenantID, group.ID); e != nil {
		var pgerr *pgconn.PgError
		if !errors.As(e, &pgerr) || pgerr.Code != "23514" || !strings.Contains(pgerr.Message, "revoke group assignments and members") {
			t.Fatal("SQL refusal did not reach member prerequisite", e)
		}
	} else {
		t.Fatal("SQL guard admitted an active member")
	}
	if e := db.operator.QueryRow(t.Context(), `SELECT state='active' FROM access_group_members WHERE tenant_id=$1 AND id=$2 AND group_id=$3`, admin.TenantID, member.ID, group.ID).Scan(&active); e != nil || !active {
		t.Fatal("failed archival changed member relation", active, e)
	}
	var successes int
	if e := db.operator.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND target_id=$2 AND action='authorization.group.archived'`, admin.TenantID, group.ID).Scan(&successes); e != nil || successes != 0 {
		t.Fatal("rejected archive wrote success audit", successes, e)
	}
	if _, e := service.ChangeAccessGroupMember(ctx, group.ID, member.ID, "", member.Revision, "Explicit member cleanup", "remove", true); e != nil {
		t.Fatal(e)
	}
	if _, e := service.SaveAccessGroup(ctx, group.ID, group.Revision, identity.GroupInput{Reason: "Archive after explicit cleanup"}, identity.GroupArchived, "archive-clean"); e != nil {
		t.Fatal(e)
	}
}
