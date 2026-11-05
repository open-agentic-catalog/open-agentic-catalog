//go:build integration

// The compiled owner command qualifies alias preflight and exact scoped output.
package functional_test

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/database"
)

func qualifyDevelopmentProvisioningProcess(t *testing.T, ctx context.Context, owner *pgx.Conn, fixture processFixture, cfg config.Config, dsn string) {
	t.Helper()
	const namespace = "00000000-0000-7000-8000-000000009690"
	tenant := cfg.Discovery.Hosts[0].TenantID
	if _, err := owner.Exec(ctx, `INSERT INTO namespaces(id,tenant_id,slug,display_name,revision,created_at,updated_at) VALUES($1,$2,'scoped-process','Scoped process',1,clock_timestamp(),clock_timestamp())`, namespace, tenant); err != nil {
		t.Fatal(err)
	}
	u := config.DevelopmentUser{TenantID: tenant, Subject: "scoped-process", RoleTemplates: []string{"library_member"}, Namespaces: map[string][]identity.Role{namespace: {identity.RoleReader}}}
	v := u
	v.RoleTemplates = []string{"library_member", "feedback_member"}
	v.Namespaces = map[string][]identity.Role{namespace: {identity.RoleReviewer}}
	only := u
	only.Subject, only.RoleTemplates = "scoped-only-process", nil
	cfg.Auth.DevelopmentTokens = map[string]config.DevelopmentUser{"owned-private-alias-a": u, "owned-private-alias-b": v, "owned-scoped-only": only}
	configuration := fixture.configuration("development-scoped", cfg)
	run := func() []database.IdentityProvisioningResult {
		t.Helper()
		var result struct {
			Total       int                                   `json:"total"`
			Provisioned []database.IdentityProvisioningResult `json:"provisioned"`
		}
		decodeProcessJSON(t, fixture.run(ctx, fixture.command(ctx, "catalogctl", dsn, "identity", "provision-development", "--config", configuration), 0), &result)
		if result.Total != 2 || len(result.Provisioned) != 2 {
			t.Fatal("owner command did not combine aliases per principal", result.Total)
		}
		seen := map[string]bool{}
		for _, p := range result.Provisioned {
			roles, templates := []identity.Role{identity.RoleReader}, 0
			switch p.Subject {
			case u.Subject:
				roles, templates = []identity.Role{identity.RoleReader, identity.RoleReviewer}, 2
			case only.Subject:
			default:
				t.Fatal("owner command returned an unexpected principal")
			}
			if seen[p.Subject] || len(p.Assignments) != len(roles) || len(p.TemplateAssignments) != templates {
				t.Fatal("owner command duplicated a principal or lost its alias grant union", p)
			}
			seen[p.Subject] = true
			for i, assignment := range p.Assignments {
				if assignment.NamespaceID != namespace || assignment.Role != roles[i] || assignment.MembershipID != p.MembershipID || assignment.TenantID != tenant {
					t.Fatal("owner command lost exact scoped principal binding", assignment)
				}
			}
		}
		return result.Provisioned
	}
	first := run()
	if next := run(); !reflect.DeepEqual(first, next) {
		t.Fatal("compiled repeat changed active grant references")
	}
	var before, after int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM audit_events`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	v.RoleTemplates = []string{"service_client"}
	cfg.Auth.DevelopmentTokens = map[string]config.DevelopmentUser{"owned-private-alias-a": u, "owned-private-alias-b": v, "owned-would-be-written-first": {TenantID: tenant, Subject: "aaa-must-not-provision", RoleTemplates: []string{"tenant_admin"}}}
	invalid := fixture.configuration("development-conflict", cfg)
	command := fixture.command(ctx, "catalogctl", dsn, "identity", "provision-development", "--config", invalid)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err == nil || ctx.Err() != nil || command.ProcessState == nil || command.ProcessState.ExitCode() != 1 {
		t.Fatal("conflicting aliases did not exit with the expected rejection")
	}
	// Check the actual terminal channel against a complete safe diagnostic;
	// stdout alone cannot detect leaked token aliases on a rejected command.
	if stdout.Len() != 0 || stderr.String() != "catalogctl: identity: INVALID_ARGUMENT: operation failed; check configuration and dependency availability\n" {
		t.Fatal("conflict omitted its safe classification or disclosed private input")
	}
	fixture.counters(command)
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM audit_events`).Scan(&after); err != nil || before != after {
		t.Fatal("invalid alias plan wrote evidence", before, after, err)
	}
	var exists bool
	if err := owner.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenant_memberships WHERE tenant_id=$1 AND subject='aaa-must-not-provision')`, tenant).Scan(&exists); err != nil || exists {
		t.Fatal("conflicting aliases were rejected after a partial principal commit", exists, err)
	}
	// The empty template does not determine Human/Service classification.
	cfg.Auth.DevelopmentTokens = map[string]config.DevelopmentUser{
		"owned-human-membership-only":   {TenantID: tenant, Subject: "human-membership-only", ActorType: identity.ActorHuman, RoleTemplates: []string{"service_client"}},
		"owned-service-membership-only": {TenantID: tenant, Subject: "service-membership-only", ActorType: identity.ActorService, RoleTemplates: []string{"service_client"}},
		"owned-default-membership-only": {TenantID: tenant, Subject: "default-membership-only"},
	}
	configuration = fixture.configuration("development-membership-only", cfg)
	var empty struct {
		Total       int                                   `json:"total"`
		Provisioned []database.IdentityProvisioningResult `json:"provisioned"`
	}
	decodeProcessJSON(t, fixture.run(ctx, fixture.command(ctx, "catalogctl", dsn, "identity", "provision-development", "--config", configuration), 0), &empty)
	if empty.Total != 3 || len(empty.Provisioned) != 3 {
		t.Fatal("membership-only owner result lost principals")
	}
	seen := map[string]bool{}
	for _, member := range empty.Provisioned {
		if seen[member.Subject] {
			t.Fatal("duplicate membership-only principal")
		}
		seen[member.Subject] = true
		want := identity.ActorHuman
		if member.Subject == "service-membership-only" {
			want = identity.ActorService
		} else if member.Subject != "human-membership-only" && member.Subject != "default-membership-only" {
			t.Fatal("unexpected membership-only subject")
		}
		if member.ActorType != want || len(member.Assignments) != 0 || len(member.TemplateAssignments) != 1 {
			t.Fatal("empty template changed classification or grants", member)
		}
		var keys []string
		if err := owner.QueryRow(ctx, `SELECT array_agg(d.key ORDER BY d.key) FROM role_assignments a JOIN role_definitions d ON d.tenant_id=a.tenant_id AND d.id=a.role_definition_id WHERE a.tenant_id=$1 AND a.membership_id=$2 AND a.state='active'`, tenant, member.MembershipID).Scan(&keys); err != nil || !reflect.DeepEqual(keys, []string{"service_client"}) {
			t.Fatal("membership-only setup granted temporary Reader access", keys, err)
		}
	}
}
