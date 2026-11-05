//go:build functional

package httpserver

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"testing"
)

func publicationFixtureContext(tb testing.TB, tenantID string) context.Context {
	tb.Helper()
	return identityfixture.WithActor(tb.Context(), identity.Actor{TenantID: tenantID, Subject: "publication-fixture", MembershipID: "00000000-0000-7000-8000-000000000501", RealmID: "00000000-0000-7000-8000-000000000502", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RolePublisher}})
}
