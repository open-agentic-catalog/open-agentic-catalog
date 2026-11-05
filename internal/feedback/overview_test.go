package feedback

import (
	"context"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"testing"
	"time"
)

func overviewAdmin(t *testing.T) context.Context {
	t.Helper()
	return identityfixture.WithActor(t.Context(), identity.Actor{TenantID: testTenant, MembershipID: testMember, RealmID: testRealm, Subject: "moderator", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}})
}
func TestFeedbackOverviewVisibilityOriginalTimeAndBounds(t *testing.T) {
	s, store, sink, reader := fixture(t)
	ctx := overviewAdmin(t)
	empty, err := s.Overview(ctx)
	if err != nil || empty.Open.Value != 0 || len(empty.Categories) != 5 || empty.OldestOpenAt != nil {
		t.Fatal(empty, err)
	}
	old := s.clock.Now().Add(-48 * time.Hour)
	for i, category := range []string{"problem", "documentation", "suggestion", "positive", "other"} {
		key := fmt.Sprint(i)
		store.records[key] = Record{ID: key, TenantID: testTenant, ResourceID: testResource, State: "open", Category: category, CreatedAt: old.Add(time.Duration(i) * time.Hour), UpdatedAt: s.clock.Now()}
	}
	store.records["deleted"] = Record{ID: "deleted", TenantID: testTenant, ResourceID: testRelease, State: "open", Category: "problem", CreatedAt: old.Add(-time.Hour)}
	store.records["addressed"] = Record{TenantID: testTenant, ResourceID: testResource, State: "addressed", Category: "problem", CreatedAt: old.Add(-time.Hour)}
	store.records["other"] = Record{TenantID: "other", ResourceID: testResource, State: "open", Category: "problem"}
	out, err := s.Overview(ctx)
	if err != nil || out.Open.Value != 5 || out.Open.Limited || out.OldestOpenAt == nil || !out.OldestOpenAt.Equal(old) || len(sink.records) != 0 {
		t.Fatal(out, err)
	}
	for _, category := range out.Categories {
		if category.Count != 1 {
			t.Fatal(out)
		}
	}
	if _, err = s.Overview(reader); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal(err)
	}
	for i := 0; i < 9996; i++ {
		store.records[fmt.Sprint("scale", i)] = Record{TenantID: testTenant, ResourceID: testResource, State: "open", Category: "problem", CreatedAt: s.clock.Now()}
	}
	out, err = s.Overview(ctx)
	if err != nil || !out.Open.Limited || out.Open.Value != 10000 || out.Categories != nil || out.OldestOpenAt == nil || !out.OldestOpenAt.Equal(old) {
		t.Fatal(out, err)
	}
	delete(store.records, "scale0")
	out, err = s.Overview(ctx)
	if err != nil || out.Open.Limited || out.Open.Value != 10000 || out.Categories[0].Count != 9996 {
		t.Fatal(out, err)
	}
	contextReader, ok := store.reader.(*contextFixture)
	if !ok {
		t.Fatal("fixture context reader is unavailable")
	}
	contextReader.fail = true
	out, err = s.Overview(ctx)
	if err != nil || out.Open.Value != 0 || out.OldestOpenAt != nil {
		t.Fatal(out, err)
	}
}

type failedFeedbackOverview struct{ Store }

func (failedFeedbackOverview) ReadOverview(context.Context, string) (OverviewRows, error) {
	return OverviewRows{}, errors.New("injected feedback summary failure")
}
func TestFeedbackOverviewFailureIsUnavailable(t *testing.T) {
	s, _, _, _ := fixture(t)
	s.store = failedFeedbackOverview{s.store}
	if _, err := s.Overview(overviewAdmin(t)); err == nil {
		t.Fatal("read failure became zero")
	}
}

func TestFeedbackOverviewContextFailureDoesNotBecomeEmpty(t *testing.T) {
	s, store, sink, _ := fixture(t)
	ctx := overviewAdmin(t)
	store.records["open"] = Record{TenantID: testTenant, ResourceID: testResource, State: "open", Category: "problem", CreatedAt: s.clock.Now()}
	if out, err := s.Overview(ctx); err != nil || out.Open.Value != 1 {
		t.Fatal("valid control failed", out, err)
	}
	reader := store.reader
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, catalog.E(catalog.CodeDependencyUnavailable, "injected context read failure")} {
		store.reader = failingContext{ContextReader: reader, operation: "resource", err: failure}
		out, err := s.Overview(ctx)
		if !errors.Is(err, failure) || out.OldestOpenAt != nil || len(out.Categories) != 0 || len(sink.records) != 0 {
			t.Fatal("context failure became a successful empty summary", out, err)
		}
	}
	store.reader = nil
	if _, err := s.Overview(ctx); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatal("missing context reader became a successful empty summary", err)
	}
}
