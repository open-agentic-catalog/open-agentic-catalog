package catalog

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestMutationRejectsIncompleteAttributionWithoutWriting(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(*identity.Actor){
		"membership": func(actor *identity.Actor) { actor.MembershipID = "" },
		"realm":      func(actor *identity.Actor) { actor.RealmID = "" },
		"actor type": func(actor *identity.Actor) { actor.ActorType = "" },
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newGuardrailFixture(t)
			actor := fixture.admin
			change(&actor)
			input := CreateNamespaceInput{Slug: "current-attribution", DisplayName: "Current attribution"}
			_, err := fixture.service.CreateNamespace(identityfixture.WithActor(context.Background(), actor), input)
			if ErrorCodeOf(err) != CodeInternal {
				t.Fatalf("incomplete attribution returned %v", err)
			}
			if len(fixture.repo.namespaces) != 0 || len(fixture.repo.audit) != 0 || len(fixture.repo.outbox) != 0 {
				t.Fatal("rejected attribution wrote domain state or evidence")
			}
			if _, err := fixture.service.CreateNamespace(fixture.adminContext(), input); err != nil {
				t.Fatalf("current attributed mutation failed: %v", err)
			}
			if len(fixture.repo.namespaces) != 1 || len(fixture.repo.audit) != 1 || len(fixture.repo.outbox) != 1 {
				t.Fatal("current attributed mutation did not write exactly once")
			}
		})
	}
}
