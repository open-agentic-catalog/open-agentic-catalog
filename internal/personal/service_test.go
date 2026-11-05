package personal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

const tenant = "00000000-0000-7000-8000-000000000001"
const member = "00000000-0000-7000-8000-000000000002"
const realm = "00000000-0000-7000-8000-000000000003"

type sink struct {
	records []audit.Record
	fail    bool
}

func (s *sink) AppendAuditEvent(_ context.Context, e audit.Record) error {
	if s.fail {
		return errors.New("audit unavailable")
	}
	s.records = append(s.records, e)
	return nil
}

type authority struct {
	deny  bool
	calls int
}

func (a *authority) Admit(context.Context, Reference) error {
	a.calls++
	if a.deny {
		return catalog.E(catalog.CodeForbidden, "target denied")
	}
	return nil
}
func fixture(t *testing.T) (*Service, *MemoryStore, *sink, *authority, context.Context) {
	t.Helper()
	e := &sink{}
	a := &authority{}
	m := NewMemoryStore(e)
	s, err := New(m, a, clock.NewFake(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: tenant, MembershipID: member, RealmID: realm, Subject: "same-subject", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}})
	return s, m, e, a, ctx
}
func searchInput() Input {
	return Input{Type: "search", Label: "Private label", Search: &Criteria{Text: "private-query-sentinel", Origins: []catalog.SearchResultType{catalog.SearchResultLocal}}}
}
func TestLibraryMemberManagesCriteriaWithoutResourceOrAdminAuthority(t *testing.T) {
	policy, err := identity.NewMemoryStore([]identity.Seed{{TenantID: tenant, Issuer: "https://issuer.example.com", Subject: "library-only", ActorType: identity.ActorHuman, RoleTemplates: []string{"library_member"}}})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := policy.ResolvePrincipal(t.Context(), identity.Principal{TenantID: tenant, Issuer: "https://issuer.example.com", Subject: "library-only"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), actor)
	decision, err := policy.CheckPermissions(ctx, actor, []identity.PermissionCheck{{Permission: "library.own"}, {Permission: "resource.read"}, {Permission: "resource.edit"}, {Permission: "access.manage"}})
	if err != nil || len(decision.Allowed) != 4 || !decision.Allowed[0] || decision.Allowed[1] || decision.Allowed[2] || decision.Allowed[3] {
		t.Fatal("library template gained unrelated authority", decision, err)
	}
	e := &sink{}
	s, err := New(NewMemoryStore(e), &authority{}, clock.NewFake(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.Save(ctx, "", 0, searchInput(), false)
	if err != nil {
		t.Fatal("library-only save", err)
	}
	got, err := s.Get(ctx, item.ID)
	if err != nil || got.Search == nil || got.Search.Text != "private-query-sentinel" {
		t.Fatal("library-only read", got, err)
	}
	if _, err := s.Save(ctx, item.ID, item.Revision, Input{}, true); err != nil {
		t.Fatal("library-only removal", err)
	}
	if len(e.records) != 2 {
		t.Fatal("library commands require audit")
	}
}
func TestPersonalLifecycleIsolationAndEvidence(t *testing.T) {
	s, m, e, a, ctx := fixture(t)
	in := searchInput()
	item, err := s.Save(ctx, "", 0, in, false)
	if err != nil {
		t.Fatal(err)
	}
	in.Search.Text = "mutated"
	got, err := s.Get(ctx, item.ID)
	if err != nil || got.Search.Text != "private-query-sentinel" {
		t.Fatal(got, err)
	}
	got.Search.Text = "returned mutation"
	got, _ = s.Get(ctx, item.ID)
	if got.Search.Text != "private-query-sentinel" {
		t.Fatal("alias")
	}
	actor, _ := identity.FromContext(ctx)
	for _, field := range []string{"member", "realm", "tenant", "service"} {
		t.Run(field, func(t *testing.T) {
			other := actor
			switch field {
			case "member":
				other.MembershipID = realm
			case "realm":
				other.RealmID = member
			case "tenant":
				other.TenantID = realm
			case "service":
				other.ActorType = identity.ActorService
			}
			otherCtx := identityfixture.WithActor(t.Context(), other)
			_, err := s.Get(otherCtx, item.ID)
			if err == nil {
				t.Fatal("private owner escaped")
			}
			if _, err = m.Get(otherCtx, actor, item.ID); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
				t.Fatal("selected owner", err)
			}
		})
	}
	in = searchInput()
	in.Label = "Renamed"
	item, err = s.Save(ctx, item.ID, 1, in, false)
	if err != nil || item.Revision != 2 {
		t.Fatal(item, err)
	}
	if _, err = s.Save(ctx, item.ID, 1, in, false); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal(err)
	}
	e.fail = true
	in.Label = "Must roll back"
	if _, err = s.Save(ctx, item.ID, 2, in, false); err == nil {
		t.Fatal("missing audit accepted")
	}
	got, _ = s.Get(ctx, item.ID)
	if got.Label != "Renamed" || got.Revision != 2 {
		t.Fatal(got)
	}
	e.fail = false
	ref := Input{Type: "resource", Label: "Exact release", Reference: &Reference{Origin: "local", ReleaseID: member, VersionRevisionID: realm}}
	saved, err := s.Save(ctx, "", 0, ref, false)
	if err != nil || a.calls != 1 {
		t.Fatal(saved, err)
	}
	a.deny = true
	ref.Label = "Rename after target loss"
	saved, err = s.Save(ctx, saved.ID, saved.Revision, ref, false)
	if err != nil || a.calls != 1 {
		t.Fatal(saved, err)
	}
	ref.Reference.VersionRevisionID = tenant
	if _, err = s.Save(ctx, saved.ID, saved.Revision, ref, false); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal(err)
	}
	if _, err = s.Save(ctx, item.ID, item.Revision, Input{}, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(ctx, item.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal(err)
	}
	for _, record := range e.records {
		raw, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if strings.Contains(string(raw), "private-query-sentinel") || strings.Contains(string(raw), "Private label") {
			t.Fatal("private preference leaked into evidence")
		}
		if err := audit.ValidateNewRecord(record); err != nil {
			t.Fatal(err)
		}
	}
}
func TestPersonalBackdatedCommandsPreserveItemAndEvidence(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "update", true: "remove"}[remove], func(t *testing.T) {
			s, _, evidence, _, ctx := fixture(t)
			item, err := s.Save(ctx, "", 0, searchInput(), false)
			if err != nil {
				t.Fatal(err)
			}
			backdated := clock.NewFake(item.UpdatedAt.Add(-time.Microsecond))
			s.clock = backdated
			input := searchInput()
			input.Label = "A deliberate update"
			_, err = s.Save(ctx, item.ID, item.Revision, input, remove)
			var refused *catalog.DomainError
			if !errors.As(err, &refused) || refused.Code != catalog.CodeConflict || refused.Message != "saved item transition time is before its retained update" {
				t.Fatal("backdated command must refuse explicitly", err)
			}
			got, err := s.Get(ctx, item.ID)
			if err != nil || got.Label != item.Label || got.Revision != item.Revision || !got.UpdatedAt.Equal(item.UpdatedAt) || len(evidence.records) != 1 {
				t.Fatal("refused command changed preference or audit", got, err)
			}
			backdated.Advance(2 * time.Microsecond)
			ack, err := s.Save(ctx, item.ID, item.Revision, input, remove)
			if err != nil || ack.Revision != 2 || !ack.UpdatedAt.After(item.UpdatedAt) || len(evidence.records) != 2 {
				t.Fatal("valid forward-time control", ack, err)
			}
			got, err = s.Get(ctx, item.ID)
			if remove {
				if catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
					t.Fatal("acknowledged removal retained preference", err)
				}
			} else if err != nil || got.Label != input.Label || got.Revision != 2 {
				t.Fatal("acknowledged update was not retained", got, err)
			}
		})
	}
}

func TestPersonalBoundsPagingAndConcurrentQuota(t *testing.T) {
	s, _, _, _, ctx := fixture(t)
	in := searchInput()
	for range MaxItems - 1 {
		if _, err := s.Save(ctx, "", 0, in, false); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := s.Save(ctx, "", 0, in, false); out <- err })
	}
	wg.Wait()
	close(out)
	success := 0
	for err := range out {
		if err == nil {
			success++
		} else if catalog.ErrorCodeOf(err) != catalog.CodeConflict {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatal(success)
	}
	page, err := s.List(ctx, ListRequest{Limit: 1})
	if err != nil || len(page) != 2 {
		t.Fatal(page, err)
	}
	next, err := s.List(ctx, ListRequest{Limit: 1, AfterID: page[0].ID})
	if err != nil || len(next) != 2 || next[0].ID != page[1].ID {
		t.Fatal(next, err)
	}
	for _, q := range []ListRequest{{Limit: 0}, {Limit: 101}, {Limit: 1, Type: "unsupported"}, {Limit: 1, AfterID: "bad"}} {
		if _, err := s.List(ctx, q); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatal(q, err)
		}
	}
}
func TestPersonalInputAndForeignPresence(t *testing.T) {
	f := false
	tr := true
	valid := Input{Type: "resource", Label: strings.Repeat("🙂", 120), Reference: &Reference{Origin: "foreign", Kind: catalog.KindAgent, SourceID: member, PresentationGeneration: 1, Identifier: "urn:air:catalog.example.com:default:agent", VersionPresent: &f}}
	if err := Validate(valid); err != nil {
		t.Fatal(err)
	}
	valid.Reference.VersionPresent = &tr
	if err := Validate(valid); err != nil {
		t.Fatal("empty version is supported", err)
	}
	for _, change := range []func(*Input){func(i *Input) { i.Label = " " }, func(i *Input) { i.Label = strings.Repeat("🙂", 121) }, func(i *Input) { i.Label = "line\nbreak" }, func(i *Input) { i.Search = &Criteria{} }, func(i *Input) { i.Reference.VersionPresent = nil }, func(i *Input) { i.Reference.VersionPresent = &f; i.Reference.Version = "unexpected" }, func(i *Input) { i.Reference.PresentationGeneration = 0 }, func(i *Input) { i.Reference.Identifier = "https://arbitrary.example/" }, func(i *Input) { i.Reference.ReleaseID = realm }} {
		candidate := valid
		r := *valid.Reference
		candidate.Reference = &r
		change(&candidate)
		if err := Validate(candidate); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatal(candidate, err)
		}
	}
}

func TestPersonalTimestampMatchesRetainedAuditPrecision(t *testing.T) {
	_, m, e, a, ctx := fixture(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 123456789, time.UTC)
	s, err := New(m, a, clock.NewFake(now), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.Save(ctx, "", 0, searchInput(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !item.UpdatedAt.Equal(now.Truncate(time.Microsecond)) || !e.records[0].At.Equal(item.UpdatedAt) {
		t.Fatal("item and required audit disagree at retained precision")
	}
}

func TestReferenceWireDiscriminatesFieldPresence(t *testing.T) {
	for _, raw := range []string{`{"origin":"local","releaseId":"` + member + `","versionRevisionId":"` + realm + `","sourceId":""}`, `{"origin":"local","releaseId":"` + member + `","versionRevisionId":"` + realm + `","presentationGeneration":0}`, `{"origin":"foreign","releaseId":""}`, `{"origin":"local","ReleaseId":"` + member + `"}`, `{"origin":"local","versionPresent":null}`, `{"origin":"unknown"}`} {
		var ref Reference
		if err := json.Unmarshal([]byte(raw), &ref); err == nil {
			t.Fatal("invalid wire reference accepted", raw)
		}
	}
}

func TestSavedCriteriaOrderWireValidation(t *testing.T) {
	for _, tc := range []struct {
		document string
		valid    bool
	}{
		{`{"type":"search","label":"Ordered","search":{"order":"publisher_desc"}}`, true},
		{`{"type":"search","label":"Default","search":{}}`, true},
		{`{"type":"search","label":"Invalid","search":{"order":""}}`, false},
		{`{"type":"search","label":"Invalid","search":{"order":null}}`, false},
		{`{"type":"search","label":"Invalid","search":{"order":"rating_desc"}}`, false},
	} {
		var input Input
		err := json.Unmarshal([]byte(tc.document), &input)
		if (err == nil) != tc.valid {
			t.Fatal(tc.document, err)
		}
		if err == nil && input.Search.Query().Order != input.Search.Order {
			t.Fatal("saved order lost")
		}
	}
}
