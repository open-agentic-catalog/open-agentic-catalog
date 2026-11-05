package publication

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func publicationContext(ctx context.Context, tenant string) context.Context {
	return identityfixture.WithActor(ctx, identity.Actor{TenantID: tenant, Subject: "publication-fixture", ActorType: identity.ActorHuman,
		MembershipID: "00000000-0000-7000-8000-000000000002", RealmID: "00000000-0000-7000-8000-000000000003", Roles: []identity.Role{identity.RolePublisher}})
}
