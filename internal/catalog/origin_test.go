package catalog

import (
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestOriginatingPrincipalIndependence(t *testing.T) {
	p := OriginatingPrincipal{TenantID: repositoryGuardrailTenant, MembershipID: guardAuthorMember, RealmID: guardRealm, Subject: "same"}
	for _, trial := range []struct {
		name  string
		actor identity.Actor
		same  bool
	}{
		{"same membership", identity.Actor{TenantID: p.TenantID, MembershipID: p.MembershipID, RealmID: p.RealmID, Subject: p.Subject}, true},
		{"same membership despite different realm and subject", identity.Actor{TenantID: p.TenantID, MembershipID: p.MembershipID, RealmID: guardReviewerMember, Subject: "other"}, true},
		{"different membership same realm subject", identity.Actor{TenantID: p.TenantID, MembershipID: guardReviewerMember, RealmID: p.RealmID, Subject: p.Subject}, true},
		{"equal subject other realm", identity.Actor{TenantID: p.TenantID, MembershipID: guardReviewerMember, RealmID: guardReviewerMember, Subject: p.Subject}, false},
		{"different subject", identity.Actor{TenantID: p.TenantID, MembershipID: guardReviewerMember, RealmID: p.RealmID, Subject: "other"}, false},
		{"other tenant", identity.Actor{TenantID: guardReviewerMember, MembershipID: p.MembershipID, RealmID: p.RealmID, Subject: p.Subject}, false},
	} {
		t.Run(trial.name, func(t *testing.T) {
			if p.SamePrincipal(trial.actor) != trial.same {
				t.Fatal("incorrect independence")
			}
		})
	}
}

func TestMemoryOriginLookupFailsClosed(t *testing.T) {
	for _, mode := range []string{"valid", "adopted", "missing", "duplicate", "invalid realm", "invalid format", "wrong tenant", "wrong target", "wrong action", "invalid principal"} {
		t.Run(mode, func(t *testing.T) {
			m := NewMemoryRepository()
			m.resources["target"] = Resource{TenantID: repositoryGuardrailTenant, CreatedBy: "author"}
			v := audit.RecordFormatV1
			e := AuditEvent{TenantID: repositoryGuardrailTenant, TargetType: "resource", TargetID: "target", Action: "resource.created", Actor: guardAuthorMember, FormatVersion: &v, Details: map[string]any{"realmId": guardRealm, "actorType": "service", "principalType": "catalog_membership"}}
			switch mode {
			case "adopted":
				e.Action = "observation.adopted"
			case "invalid realm":
				e.Details["realmId"] = "invalid"
			case "invalid format":
				e.FormatVersion = nil
			case "wrong tenant":
				e.TenantID = guardRealm
			case "wrong target":
				e.TargetID = "other"
			case "wrong action":
				e.Action = "resource.deleted"
			case "invalid principal":
				e.Details["principalType"] = "operator"
			}
			if mode != "missing" {
				m.indexOriginatingPrincipal(e)
			}
			if mode == "duplicate" {
				m.indexOriginatingPrincipal(e)
			}
			got, err := m.GetOriginatingPrincipal(t.Context(), repositoryGuardrailTenant, "resource", "target")
			if mode == "valid" || mode == "adopted" {
				if err != nil || got.MembershipID != guardAuthorMember || got.Subject != "author" {
					t.Fatal(got, err)
				}
			} else if ErrorCodeOf(err) != CodeForbidden {
				t.Fatal("unbound origin admitted", got, err)
			}
		})
	}
}
