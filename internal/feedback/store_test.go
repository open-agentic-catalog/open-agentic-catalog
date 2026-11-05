// Repository guard tests exercise invalid writes without a database or HTTP server.
package feedback

import (
	"context"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// TestMemoryStoreCreationContext matches SQL's local context and timestamp guards.
func TestMemoryStoreCreationContext(t *testing.T) {
	s, _, evidence, ctx := fixture(t)
	base, err := s.Create(ctx, input())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Record)
		valid  bool
	}{
		{"general resource", func(*Record) {}, true},
		{"version revision", func(r *Record) { r.VersionRevisionID = testRevision }, true},
		{"exact release", func(r *Record) { r.ReleaseID, r.VersionRevisionID = testRelease, testRevision }, true},
		{"malformed release", func(r *Record) { r.ReleaseID, r.VersionRevisionID = "invalid", testRevision }, false},
		{"malformed version revision", func(r *Record) { r.VersionRevisionID = "invalid" }, false},
		{"release without revision", func(r *Record) { r.ReleaseID = testRelease }, false},
		{"different creation time", func(r *Record) { r.CreatedAt = r.CreatedAt.Add(-time.Second) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.change(&r)
			sink := &auditSink{}
			store := NewMemoryStore(sink, &contextFixture{})
			_, err := store.Save(ctx, r, 0, evidence.records[0])
			if (err == nil) != tc.valid {
				t.Fatalf("Save error = %v, valid = %v", err, tc.valid)
			}
			if tc.valid {
				if len(sink.records) != 1 || len(store.records) != 1 {
					t.Fatal("valid creation did not commit state and audit together")
				}
			} else if len(sink.records) != 0 || len(store.records) != 0 {
				t.Fatal("invalid creation committed state or audit")
			}
		})
	}
}

// TestMemoryStoreRejectsInvalidTransitions verifies guards independently of service policy.
func TestMemoryStoreRejectsInvalidTransitions(t *testing.T) {
	s, _, evidence, ctx := fixture(t)
	old, err := s.Create(ctx, input())
	if err != nil {
		t.Fatal(err)
	}
	edited, err := s.Change(ctx, old.ID, 1, "edit", EditInput{Category: old.Category, Comment: "revised comment"}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Record, *audit.Record)
		code   catalog.ErrorCode
	}{
		{"invalid category", func(r *Record, _ *audit.Record) { r.Category = "rating" }, catalog.CodeInvalidArgument},
		{"empty live comment", func(r *Record, _ *audit.Record) { r.Comment = "" }, catalog.CodeInvalidArgument},
		{"terminal comment", func(r *Record, _ *audit.Record) { r.State = "removed" }, catalog.CodeInvalidArgument},
		{"terminal response", func(r *Record, _ *audit.Record) { r.State, r.Comment, r.Response = "withdrawn", "", "retained" }, catalog.CodeInvalidArgument},
		{"open response", func(r *Record, _ *audit.Record) { r.Response = "unexpected" }, catalog.CodeInvalidArgument},
		{"retarget resource", func(r *Record, _ *audit.Record) { r.ResourceID = testRelease }, catalog.CodeConflict},
		{"change submitter", func(r *Record, _ *audit.Record) { r.MembershipID = testRealm }, catalog.CodeConflict},
		{"retarget revision", func(r *Record, _ *audit.Record) { r.VersionRevisionID = testRevision }, catalog.CodeConflict},
		{"edit without reopening", func(r *Record, _ *audit.Record) { r.State = "addressed" }, catalog.CodeConflict},
		{"response rewrites comment", func(r *Record, e *audit.Record) { r.State, e.Action = "addressed", "feedback.address" }, catalog.CodeConflict},
		{"reopen rewrites category", func(r *Record, e *audit.Record) {
			r.Comment, r.Category, e.Action = old.Comment, "other", "feedback.reopen"
		}, catalog.CodeConflict},
		{"withdraw does not clear", func(_ *Record, e *audit.Record) { e.Action = "feedback.withdraw" }, catalog.CodeConflict},
		{"remove changes category", func(r *Record, e *audit.Record) {
			r.State, r.Comment, r.Category, e.Action = "removed", "", "other", "feedback.remove"
		}, catalog.CodeConflict},
		{"creation on update", func(_ *Record, e *audit.Record) { e.Action = "feedback.created" }, catalog.CodeConflict},
		{"audit retains text", func(_ *Record, e *audit.Record) {
			e.Details["facts"] = map[string]any{"revision": int64(2), "state": "open", "comment": "private"}
		}, catalog.CodeInternal},
		{"audit has wrong revision", func(_ *Record, e *audit.Record) {
			e.Details["facts"] = map[string]any{"revision": int64(3), "state": "open"}
		}, catalog.CodeInternal},
		{"audit has wrong state", func(_ *Record, e *audit.Record) {
			e.Details["facts"] = map[string]any{"revision": int64(2), "state": "addressed"}
		}, catalog.CodeInternal},
		{"audit has wrong tenant", func(_ *Record, e *audit.Record) { e.TenantID = testRealm }, catalog.CodeInternal},
		{"audit has wrong target", func(_ *Record, e *audit.Record) { e.TargetID = testRevision }, catalog.CodeInternal},
		{"audit has wrong time", func(_ *Record, e *audit.Record) { e.At = e.At.Add(time.Second) }, catalog.CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &auditSink{}
			store := NewMemoryStore(sink, &contextFixture{})
			if _, err := store.Save(ctx, old, 0, evidence.records[0]); err != nil {
				t.Fatal(err)
			}
			r, e := edited, evidence.records[1]
			e.Details = maps.Clone(e.Details)
			tc.change(&r, &e)
			_, err := store.Save(ctx, r, 1, e)
			requireCode(t, err, tc.code)
			got, err := store.Get(ctx, testTenant, old.ID)
			if err != nil || got != old || len(sink.records) != 1 {
				t.Fatal("rejected write changed state or audit")
			}
		})
	}
}

// TestMemoryStoreTerminalRecordsCannotBeRevived bypasses service checks deliberately.
func TestMemoryStoreTerminalRecordsCannotBeRevived(t *testing.T) {
	for _, action := range []string{"withdraw", "remove"} {
		t.Run(action, func(t *testing.T) {
			s, store, sink, ctx := fixture(t)
			r, err := s.Create(ctx, input())
			if err != nil {
				t.Fatal(err)
			}
			a, _ := identity.FromContext(ctx)
			a.Roles = []identity.Role{identity.RoleTenantAdmin}
			terminal, err := s.Change(identityfixture.WithActor(ctx, a), r.ID, 1, action, EditInput{}, "")
			if err != nil {
				t.Fatal(err)
			}
			r = terminal
			r.Revision++
			r.State, r.Comment = "open", "must not be restored"
			e := sink.records[1]
			e.Action = "feedback.edit"
			e.Details = maps.Clone(e.Details)
			e.Details["facts"] = map[string]any{"revision": r.Revision, "state": r.State}
			if err := audit.ValidateNewRecord(e); err != nil {
				t.Fatal(err)
			}
			_, err = store.Save(ctx, r, terminal.Revision, e)
			requireCode(t, err, catalog.CodeConflict)
			got, err := store.Get(ctx, testTenant, r.ID)
			if err != nil || got != terminal || len(sink.records) != 2 {
				t.Fatal("terminal text revived or invalid transition audited")
			}
		})
	}
}

// TestMemoryStoreCancellationAndMissingDependencies never requires a real service.
func TestMemoryStoreCancellationAndMissingDependencies(t *testing.T) {
	s, store, evidence, ctx := fixture(t)
	r, err := s.Create(ctx, input())
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = store.Get(canceled, testTenant, r.ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	_, err = store.List(canceled, testTenant, ListInput{Limit: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	_, err = store.Save(canceled, r, 0, evidence.records[0])
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(evidence.records) != 1 {
		t.Fatal("canceled write appended audit")
	}
	for _, limit := range []int{0, 102} {
		_, err := store.List(ctx, testTenant, ListInput{Limit: limit})
		requireCode(t, err, catalog.CodeInvalidArgument)
	}
	withoutAudit := NewMemoryStore(nil, &contextFixture{})
	_, err = withoutAudit.Save(ctx, r, 0, evidence.records[0])
	requireCode(t, err, catalog.CodeInternal)
	if len(withoutAudit.records) != 0 {
		t.Fatal("unaudited write committed")
	}
	store.reader = nil
	_, err = store.List(ctx, testTenant, ListInput{Limit: 1})
	requireCode(t, err, catalog.CodeInternal)
	cause := errors.New("synthetic context failure")
	store.reader = failingContext{ContextReader: &contextFixture{}, operation: "resource", err: cause}
	_, err = store.List(ctx, testTenant, ListInput{Limit: 1})
	if !errors.Is(err, cause) {
		t.Fatal("context failure was hidden as an empty page")
	}
}

// TestFeedbackPagesStayOwnedAndOrdered checks caller filters before bounded selection.
func TestFeedbackPagesStayOwnedAndOrdered(t *testing.T) {
	s, store, _, ctx := fixture(t)
	ids := make([]string, 0, 3)
	for range 3 {
		r, err := s.Create(ctx, input())
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	slices.Sort(ids)
	other, _ := identity.FromContext(ctx)
	other.MembershipID = testRealm
	if _, err := s.Create(identityfixture.WithActor(ctx, other), input()); err != nil {
		t.Fatal(err)
	}
	page, err := s.List(ctx, ListInput{Limit: 1, MembershipID: testRealm, Category: "problem", State: "open", ResourceID: testResource}, false)
	if err != nil || len(page) != 2 || page[0].ID != ids[0] || page[1].ID != ids[1] {
		t.Fatalf("first page or sentinel mismatch: %v", err)
	}
	for _, r := range page {
		if r.MembershipID != testMember {
			t.Fatal("caller-supplied membership selected another reader")
		}
	}
	page, err = s.List(ctx, ListInput{Limit: 1, AfterID: ids[1]}, false)
	if err != nil || len(page) != 1 || page[0].ID != ids[2] {
		t.Fatalf("continuation mismatch: %v", err)
	}
	for _, q := range []ListInput{{Limit: 1, Category: "other"}, {Limit: 1, State: "addressed"}, {Limit: 1, ResourceID: testRelease}} {
		page, err := s.List(ctx, q, false)
		if err != nil || len(page) != 0 {
			t.Fatalf("unmatched filter returned feedback: %v", err)
		}
	}
	page, err = store.List(ctx, testRelease, ListInput{Limit: 1})
	if err != nil || len(page) != 0 {
		t.Fatalf("other tenant returned feedback: %v", err)
	}
}
