package catalog

import (
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"testing"
	"time"
)

func TestDocumentReferenceRequiresLiveSameTenantScopedContentAuthority(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	const ns = "00000000-0000-7000-8000-000000000010"
	const hidden = "00000000-0000-7000-8000-000000000011"
	repo := NewMemoryRepository()
	repo.resources["visible"] = Resource{ID: "visible", TenantID: tenant, NamespaceID: ns}
	repo.resources["hidden"] = Resource{ID: "hidden", TenantID: tenant, NamespaceID: hidden}
	repo.versions["visible-version"] = ResourceVersion{ID: "visible-version", TenantID: tenant, ResourceID: "visible"}
	repo.versions["hidden-version"] = ResourceVersion{ID: "hidden-version", TenantID: tenant, ResourceID: "hidden"}
	repo.versionRevisions["visible-revision"] = VersionRevision{TenantID: tenant, ResourceVersionID: "visible-version", Representations: []Representation{{ArtifactID: "shared"}}}
	repo.versionRevisions["hidden-revision"] = VersionRevision{TenantID: tenant, ResourceVersionID: "hidden-version", Representations: []Representation{{ArtifactID: "shared"}, {ArtifactID: "private"}}}
	a := identity.Actor{TenantID: tenant, Subject: "reader", ActorType: identity.ActorHuman, Namespaces: map[string][]identity.Role{ns: {identity.RoleReader}}}
	ctx := identityfixture.WithActor(t.Context(), a)
	if scope, err := repo.DocumentAccessNamespace(ctx, tenant, "shared"); err != nil || scope != ns {
		t.Fatalf("shared authorized reference lost: %s %v", scope, err)
	}
	for _, document := range []string{"private", "missing"} {
		if _, err := repo.DocumentAccessNamespace(ctx, tenant, document); ErrorCodeOf(err) != CodeNotFound {
			t.Fatalf("private reference disclosed: %v", err)
		}
	}
	resource := repo.resources["visible"]
	now := time.Now()
	resource.DeletedAt = &now
	repo.resources["visible"] = resource
	if _, err := repo.DocumentAccessNamespace(ctx, tenant, "shared"); ErrorCodeOf(err) != CodeNotFound {
		t.Fatal("deleted reference authorized", err)
	}
	if _, err := repo.DocumentAccessNamespace(ctx, "00000000-0000-7000-8000-000000000002", "shared"); ErrorCodeOf(err) != CodeNotFound {
		t.Fatal("cross-tenant reference authorized", err)
	}
}
