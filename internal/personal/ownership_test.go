package personal

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestSavedCriteriaSlicesAreDetachedAtEveryReadBoundary(t *testing.T) {
	s, _, evidence, admission, ctx := fixture(t)
	in := Input{Type: "search", Label: " Exact private label ", Search: &Criteria{
		Text:       "private query",
		Origins:    []catalog.SearchResultType{catalog.SearchResultLocal, catalog.SearchResultIndexed},
		Kinds:      []catalog.Kind{catalog.KindAgent, catalog.KindSkill},
		Tags:       []string{"a,b", " exact "},
		Categories: []string{" Support ", "Tools"},
	}}
	want := Criteria{
		Text:       "private query",
		Origins:    []catalog.SearchResultType{catalog.SearchResultLocal, catalog.SearchResultIndexed},
		Kinds:      []catalog.Kind{catalog.KindAgent, catalog.KindSkill},
		Tags:       []string{"a,b", " exact "},
		Categories: []string{" Support ", "Tools"},
	}
	saved, err := s.Save(ctx, "", 0, in, false)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Get(ctx, saved.ID)
	if err != nil || !reflect.DeepEqual(loaded.Search, &want) {
		t.Fatal("positive exact-criteria read", loaded, err)
	}
	page, err := s.List(ctx, ListRequest{Type: "search", Label: &in.Label, Limit: 1})
	if err != nil || len(page) != 1 || page[0].ID != saved.ID || !reflect.DeepEqual(page[0].Search, &want) {
		t.Fatal("positive filtered criteria read", page, err)
	}
	for _, boundary := range []struct {
		name  string
		value *Criteria
	}{{"save input", in.Search}, {"save result", saved.Search}, {"get result", loaded.Search}, {"filtered list result", page[0].Search}} {
		boundary.value.Text = "changed outside the store"
		boundary.value.Origins[0] = catalog.SearchResultLive
		boundary.value.Kinds[0] = catalog.KindPlugin
		boundary.value.Tags[0] = "changed tag"
		boundary.value.Categories[0] = "changed category"
		retained, err := s.Get(ctx, saved.ID)
		if err != nil || retained.Revision != 1 || retained.Label != " Exact private label " || !reflect.DeepEqual(retained.Search, &want) {
			t.Fatal("caller changed retained criteria through", boundary.name, retained, err)
		}
	}
	if admission.calls != 0 || len(evidence.records) != 1 {
		t.Fatal("criteria reads admitted a resource or produced mutation evidence", admission.calls, len(evidence.records))
	}
}

func TestStoreRejectsUnboundAuditBeforeRetainingPreference(t *testing.T) {
	s, _, evidence, _, ctx := fixture(t)
	item, err := s.Save(ctx, "", 0, searchInput(), false)
	if err != nil {
		t.Fatal(err)
	}
	actor, _ := identity.FromContext(ctx)
	raw, err := json.Marshal(evidence.records[0])
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*audit.Record){
		"actor":    func(e *audit.Record) { e.Actor = realm },
		"tenant":   func(e *audit.Record) { e.TenantID = realm },
		"target":   func(e *audit.Record) { e.TargetID = realm },
		"realm":    func(e *audit.Record) { e.Details["realmId"] = member },
		"revision": func(e *audit.Record) { e.Details["facts"] = map[string]any{"revision": 2} },
		"time":     func(e *audit.Record) { e.At = e.At.Add(time.Microsecond) },
		"action":   func(e *audit.Record) { e.Action = "personal.item.updated" },
		"invalid":  func(e *audit.Record) { e.ID = "invalid" },
	} {
		t.Run(name, func(t *testing.T) {
			var record audit.Record
			if err := json.Unmarshal(raw, &record); err != nil {
				t.Fatal(err)
			}
			change(&record)
			sink := &sink{}
			store := NewMemoryStore(sink)
			if _, err := store.Save(ctx, actor, item, 0, false, record); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
				t.Fatal("unbound audit accepted", err)
			}
			if _, err := store.Get(ctx, actor, item.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound || len(sink.records) != 0 {
				t.Fatal("failed admission retained preference or evidence", err)
			}
		})
	}
	store := NewMemoryStore(&sink{})
	if _, err := store.Save(ctx, actor, item, 0, false, evidence.records[0]); err != nil {
		t.Fatal("exact audit positive control", err)
	}
}

func TestForeignPointerIsolationFilteredPagesAndCanceledReads(t *testing.T) {
	s, store, _, _, ctx := fixture(t)
	if _, err := s.Save(ctx, "", 0, searchInput(), false); err != nil {
		t.Fatal(err)
	}
	present := false
	in := Input{Type: "resource", Label: "Exact external target", Reference: &Reference{Origin: "foreign", Kind: catalog.KindAgent, SourceID: member, PresentationGeneration: 1, Identifier: "urn:air:example.com:default:agent", VersionPresent: &present}}
	item, err := s.Save(ctx, "", 0, in, false)
	if err != nil {
		t.Fatal(err)
	}
	present = true
	page, err := s.List(ctx, ListRequest{Type: "resource", Limit: 1})
	if err != nil || len(page) != 1 || page[0].ID != item.ID || *page[0].Reference.VersionPresent {
		t.Fatal("filter or input pointer isolation", page, err)
	}
	*page[0].Reference.VersionPresent = true
	got, err := s.Get(ctx, item.ID)
	if err != nil || *got.Reference.VersionPresent {
		t.Fatal("returned pointer mutated retained identity", got, err)
	}
	if _, err := s.Get(ctx, "malformed"); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal(err)
	}
	actor, _ := identity.FromContext(ctx)
	if _, err := store.List(ctx, actor, ListRequest{Limit: 0}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.List(canceled, actor, ListRequest{Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled list continued", err)
	}
	if _, err := store.Get(canceled, actor, item.ID); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled read continued", err)
	}
	other := actor
	other.MembershipID = realm
	if _, err := store.List(ctx, other, ListRequest{Limit: 1}); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("caller selected a different library owner", err)
	}
}
