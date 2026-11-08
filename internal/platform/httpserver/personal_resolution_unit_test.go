// Saved-resource resolution exercises the handler and its current-authority
// adapter using retained records and existing policy/store ports only.
package httpserver

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
)

func personalResolutionActor(t *testing.T, templates ...string) identity.Actor {
	t.Helper()
	seed := identity.Seed{TenantID: projectionTenant, Issuer: "urn:catalog:personal-resolution-unit", Subject: "library-reader", ActorType: identity.ActorHuman, RoleTemplates: templates}
	store, err := identity.NewMemoryStore([]identity.Seed{seed})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: seed.TenantID, Issuer: seed.Issuer, Subject: seed.Subject})
	if err != nil {
		t.Fatal(err)
	}
	return actor
}

func TestPersonalResolveHandlerRechecksExactLocalReference(t *testing.T) {
	for _, outcome := range []string{"available", "revision-changed", "target-absent", "target-forbidden", "authority-lost", "dependency-unavailable"} {
		t.Run(outcome, func(t *testing.T) {
			reference := personal.Reference{Origin: "local", ReleaseID: projectionItem, VersionRevisionID: projectionItem}
			if outcome == "revision-changed" {
				reference.VersionRevisionID = projectionNext
			}
			if err := personal.Validate(personal.Input{Type: "resource", Label: "Saved resource", Reference: &reference}); err != nil {
				t.Fatal("invalid local-reference control", err)
			}
			actor := personalResolutionActor(t, "library_member", "reader")
			if outcome == "authority-lost" {
				// Owning a saved reference does not retain authority over its target.
				actor = personalResolutionActor(t, "library_member")
			}
			savedReads, targetReads := 0, 0
			store := personalHandlerStore{get: func(ctx context.Context, owner identity.Actor, key string) (personal.Item, error) {
				savedReads++
				current, ok := identity.FromContext(ctx)
				if !ok || owner.TenantID != actor.TenantID || owner.MembershipID != actor.MembershipID || owner.RealmID != actor.RealmID || owner.Subject != actor.Subject || current.MembershipID != owner.MembershipID || key != personalHandlerID {
					t.Fatal("saved read changed its owner or identifier", owner, current, key)
				}
				return personal.Item{ID: key, Revision: 3, Input: personal.Input{Type: "resource", Label: "private-fixture: saved label", Reference: &reference}}, nil
			}}
			repo := controlReadFixture(t)
			repo.read = func(ctx context.Context, tenant, key, kind string) error {
				targetReads++
				current, ok := identity.FromContext(ctx)
				if !ok || current.MembershipID != actor.MembershipID || tenant != actor.TenantID || key != reference.ReleaseID || kind != "CatalogRecord" {
					t.Fatal("resolution substituted its target, owner or read port", tenant, key, kind, current)
				}
				switch outcome {
				case "target-absent":
					return catalog.E(catalog.CodeNotFound, "private-fixture: target no longer available")
				case "target-forbidden":
					return catalog.E(catalog.CodeForbidden, "private-fixture: target access denied")
				case "dependency-unavailable":
					return catalog.E(catalog.CodeDependencyUnavailable, "private-fixture: target storage failed")
				default:
					return nil
				}
			}
			server, request := personalHandlerFixture(t, store)
			server.catalog = projectionCatalog(t, repo, nil)
			r := request("POST", "/api/v1/personal/items/"+personalHandlerID+"/resolve", "{}")
			r = r.WithContext(identity.WithActor(r.Context(), actor))
			w := httptest.NewRecorder()
			server.handlePersonalResolve(w, r)
			switch outcome {
			case "available":
				requireControlJSON(t, w, 200, map[string]any{"status": "available", "reference": reference})
			case "dependency-unavailable":
				requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
			default:
				// The exact response schema excludes reference, indexed content,
				// retained metadata and the reason target authority was lost.
				requireControlJSON(t, w, 200, map[string]any{"status": "unavailable"})
			}
			if savedReads != 1 || targetReads == 0 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("resolution skipped a current read or lost private cache policy", savedReads, targetReads, w.Header())
			}
		})
	}
}

func TestPersonalResolveHandlerRejectsSavedSearchAndUnavailableForeignDependency(t *testing.T) {
	versionPresent := true
	for _, resource := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved-search", true: "foreign-validator-unavailable"}[resource], func(t *testing.T) {
			calls := 0
			input := personal.Input{Type: "search", Label: "private-fixture: saved search", Search: &personal.Criteria{Text: "private-fixture: criteria"}}
			if resource {
				input = personal.Input{Type: "resource", Label: "private-fixture: saved foreign target", Reference: &personal.Reference{Origin: "foreign", Kind: catalog.KindAgent, SourceID: projectionResource, PresentationGeneration: 1, Identifier: "urn:air:example.com:default:agent", VersionPresent: &versionPresent, Version: "1.0.0"}}
			}
			if err := personal.Validate(input); err != nil {
				t.Fatal("invalid saved-item control", err)
			}
			store := personalHandlerStore{get: func(context.Context, identity.Actor, string) (personal.Item, error) {
				calls++
				return personal.Item{ID: personalHandlerID, Revision: 1, Input: input}, nil
			}}
			server, request := personalHandlerFixture(t, store)
			// No Catalog/search dependency is selected: neither case may try
			// another lookup or make a live request to resolve the item.
			w := httptest.NewRecorder()
			server.handlePersonalResolve(w, request("POST", "/api/v1/personal/items/"+personalHandlerID+"/resolve", "{}"))
			if resource {
				requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
			} else {
				requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
			}
			if calls != 1 {
				t.Fatal("resolution skipped the saved-item read", calls)
			}
		})
	}
}

func TestPersonalResolveHandlerSavedItemErrorsRemainProblems(t *testing.T) {
	for _, code := range []catalog.ErrorCode{catalog.CodeNotFound, catalog.CodeForbidden, catalog.CodeDependencyUnavailable} {
		t.Run(string(code), func(t *testing.T) {
			calls := 0
			store := personalHandlerStore{get: func(context.Context, identity.Actor, string) (personal.Item, error) {
				calls++
				return personal.Item{}, catalog.E(code, "saved item is unavailable")
			}}
			server, request := personalHandlerFixture(t, store)
			w := httptest.NewRecorder()
			server.handlePersonalResolve(w, request("POST", "/api/v1/personal/items/"+personalHandlerID+"/resolve", "{}"))
			status := map[catalog.ErrorCode]int{catalog.CodeNotFound: 404, catalog.CodeForbidden: 403, catalog.CodeDependencyUnavailable: 503}[code]
			requireProjectionProblem(t, w, status, code)
			if calls != 1 {
				t.Fatal("saved-item error did not come from its selected read", calls)
			}
		})
	}
}

func TestPersonalResolveHandlerRequiresEmptyObjectBeforeReading(t *testing.T) {
	for _, body := range []string{"", "null", `{"reference":{}}`, `{} {}`} {
		t.Run(body, func(t *testing.T) {
			server, request := personalHandlerFixture(t, personalHandlerStore{get: func(context.Context, identity.Actor, string) (personal.Item, error) {
				t.Fatal("invalid resolution body reached saved-item storage")
				return personal.Item{}, nil
			}})
			w := httptest.NewRecorder()
			server.handlePersonalResolve(w, request("POST", "/api/v1/personal/items/"+personalHandlerID+"/resolve", body))
			requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
		})
	}
}
