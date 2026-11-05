package ratings

import (
	"context"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"testing"
)

func TestRatingOverviewRetainsUnavailableTargetsAndStates(t *testing.T) {
	s, store, evidence, authority, reader := fixture(t)
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: tenant, Subject: "moderator", Roles: []identity.Role{identity.RoleTenantAdmin}})
	out, err := s.Overview(ctx)
	if err != nil || out.Total.Value != 0 || len(out.States) != 3 {
		t.Fatal(out, err)
	}
	authority.deny = true
	for _, state := range []string{"active", "withdrawn", "removed"} {
		store.records[state] = Record{ID: state, TenantID: tenant, State: state}
	}
	store.records["other"] = Record{TenantID: "other", State: "active"}
	out, err = s.Overview(ctx)
	if err != nil || out.Total.Value != 3 || len(evidence.records) != 0 {
		t.Fatal(out, err)
	}
	for _, state := range out.States {
		if state.Count != 1 {
			t.Fatal(out)
		}
	}
	if _, err = s.Overview(reader); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal(err)
	}
	for i := 0; i < 9998; i++ {
		store.records[fmt.Sprint(i)] = Record{TenantID: tenant, State: "active"}
	}
	out, err = s.Overview(ctx)
	if err != nil || out.Total.Value != 10000 || !out.Total.Limited || out.States != nil {
		t.Fatal(out, err)
	}
	delete(store.records, "0")
	out, err = s.Overview(ctx)
	if err != nil || out.Total.Value != 10000 || out.Total.Limited || out.States[0].Count != 9998 {
		t.Fatal(out, err)
	}
	s.store = failedRatingOverview{s.store}
	if _, err = s.Overview(ctx); err == nil {
		t.Fatal("read failure became zero")
	}
}

type failedRatingOverview struct{ Store }

func (failedRatingOverview) ReadOverview(context.Context, string) ([]string, error) {
	return nil, errors.New("injected rating summary failure")
}
