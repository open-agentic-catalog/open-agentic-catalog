package ratings

import (
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestRatingStoreRejectsUnboundedAndInvalidSelections(t *testing.T) {
	_, store, evidence, _, ctx := fixture(t)
	for _, limit := range []int{0, 102} {
		_, err := store.List(ctx, tenant, ListInput{Limit: limit})
		code(t, err, catalog.CodeInvalidArgument)
	}
	for _, limit := range []int{1, 101} {
		got, err := store.List(ctx, tenant, ListInput{Limit: limit})
		if err != nil || len(got) != 0 {
			t.Fatalf("valid empty page: %+v %v", got, err)
		}
	}
	_, err := store.Aggregates(ctx, tenant, make([]Target, catalog.MaxPublicationRecords+1))
	code(t, err, catalog.CodeWorkLimitExceeded)
	_, err = store.Aggregates(ctx, tenant, []Target{{}})
	code(t, err, catalog.CodeInvalidArgument)
	if len(evidence.records) != 0 {
		t.Fatal("invalid read selections appended evidence")
	}
}

func TestRatingStoreRequiresAuditBeforeOpinionAndSharingChanges(t *testing.T) {
	service, store, evidence, _, ctx := fixture(t)
	store.evidence = nil
	_, err := service.Submit(ctx, admission(), 4, nil, 0)
	code(t, err, catalog.CodeInternal)
	if len(store.records) != 0 || len(store.owners) != 0 || len(store.totals) != 0 {
		t.Fatal("missing audit changed opinion or aggregate state")
	}
	actor, _ := identity.FromContext(ctx)
	actor.Roles = []identity.Role{identity.RoleTenantAdmin}
	admin := identityfixture.WithActor(ctx, actor)
	_, err = service.SetSharing(admin, "catalog.example", "anonymous", true, 0, "Share consented summaries")
	code(t, err, catalog.CodeInternal)
	if len(store.sharing) != 0 {
		t.Fatal("missing audit enabled sharing")
	}
	store.evidence = evidence
	evidence.fail = true
	_, err = service.SetSharing(admin, "catalog.example", "anonymous", true, 0, "Share consented summaries")
	if err == nil || len(store.sharing) != 0 || len(evidence.records) != 0 {
		t.Fatalf("failed audit changed sharing: %+v %v", store.sharing, err)
	}
	evidence.fail = false
	got, err := service.SetSharing(admin, "catalog.example", "anonymous", true, 0, "Share consented summaries")
	if err != nil || !got.Enabled || got.Revision != 1 || len(evidence.records) != 1 {
		t.Fatalf("failed attempt prevented a later valid change: %+v %v", got, err)
	}
}

func TestRatingStoreRejectsSecondOpinionIdentityWithoutChangingTheFirst(t *testing.T) {
	service, store, evidence, _, ctx := fixture(t)
	first, err := service.Submit(ctx, admission(), 4, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// A competing first submission can have a different ID for the same owner
	// and target. Exercise that admission deterministically, without a race.
	second := first
	second.ID = release
	command := evidence.records[0]
	command.ID, command.TargetID = resource, second.ID
	if err := ValidateSave(Record{}, second, 0, command); err != nil {
		t.Fatal("invalid candidate masked the owner uniqueness check", err)
	}
	_, err = store.Save(ctx, second, 0, admission(), command)
	code(t, err, catalog.CodePreconditionFailed)
	if len(store.records) != 1 || len(store.owners) != 1 || len(evidence.records) != 1 {
		t.Fatal("competing opinion changed retained identity or evidence")
	}
	got, err := store.Get(ctx, tenant, first.ID)
	if err != nil || got.ID != first.ID || got.Score != 4 || got.Revision != 1 {
		t.Fatal("competing opinion changed the original", got, err)
	}
	values, err := store.Aggregates(ctx, tenant, []Target{first.Target})
	if err != nil || values[Key(first.Target)].Count != 1 || values[Key(first.Target)].Sum != 4 {
		t.Fatal("competing opinion changed the aggregate", values, err)
	}
}
