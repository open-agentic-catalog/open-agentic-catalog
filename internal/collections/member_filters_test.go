package collections

import (
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestMemberFilterNormalizationAndBounds(t *testing.T) {
	kinds := []catalog.Kind{catalog.KindSkill, catalog.KindAgent, catalog.KindSkill}
	text, got, err := NormalizeMemberFilters("  CAFÉ\u2003Invoice\n", kinds)
	if err != nil || text != "café invoice" || !reflect.DeepEqual(got, []catalog.Kind{catalog.KindAgent, catalog.KindSkill}) || kinds[0] != catalog.KindSkill {
		t.Fatal(text, got, kinds, err)
	}
	for _, value := range []string{"", " \t\n\u0085", strings.Repeat("📚", 256)} {
		if _, _, err := NormalizeMemberFilters(value, nil); err != nil {
			t.Fatal(value, err)
		}
	}
	for _, value := range []string{strings.Repeat("📚", 257), strings.Repeat(" ", 257), "a" + strings.Repeat("\t", 256), "a\x00b", "a\x01b", string([]byte{255})} {
		if _, _, err := NormalizeMemberFilters(value, nil); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatal("invalid text accepted", err)
		}
	}
	for _, values := range [][]catalog.Kind{{"AGENT"}, {"agent", ""}, {"unknown"}, {"agent", "agent", "agent", "agent", "agent"}} {
		if _, _, err := NormalizeMemberFilters("", values); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatal("invalid kind selection accepted", values, err)
		}
	}
	_, got, err = NormalizeMemberFilters("", []catalog.Kind{catalog.KindPlugin, catalog.KindSkill, catalog.KindMCPServer, catalog.KindAgent})
	if err != nil || !reflect.DeepEqual(got, []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindPlugin, catalog.KindSkill}) {
		t.Fatal("maximum valid kind selection", got, err)
	}
}

func TestMemberFiltersSelectReadableReleasedMetadataBeforePage(t *testing.T) {
	service, _, source, admin := fixture(t)
	collection := create(t, service, admin)
	hidden := unitID(90)
	source.namespaces[hidden] = true
	rows := []struct {
		n                 int
		ns                string
		kind              catalog.Kind
		name, description string
	}{
		{100, hidden, catalog.KindMCPServer, "Invoice", "CAFÉ"},
		{101, target, catalog.KindAgent, "Invoice", "café"},
		{102, target, catalog.KindMCPServer, "Other", "Unrelated"},
		{103, target, catalog.KindMCPServer, "INVOICE", "CAFÉ crème"},
		{104, target, catalog.KindSkill, "Invoice", "café"},
		{105, target, catalog.KindPlugin, "Invoice", "café"},
		{106, target, catalog.KindMCPServer, "Invoice", "café"},
		{107, target, catalog.KindMCPServer, "Invoice", "cafeteria"},
	}
	for _, row := range rows {
		key := seed(source, row.n, row.ns, row.kind)
		record := source.records[key]
		record.ListingRevision.DisplayName = row.name
		record.ListingRevision.Description = row.description
		record.ListingRevision.Tags = []string{"invoice", "café", "secret-tag"}
		record.Resource.CanonicalIdentifier = "secret-identifier"
		source.records[key] = record
		var err error
		collection, err = service.Change(admin, collection.ID, collection.Revision, Add, Patch{}, key)
		if err != nil {
			t.Fatal(err)
		}
	}
	// A matching resource outside this collection cannot enter a result page.
	outside := seed(source, 108, target, catalog.KindMCPServer)
	record := source.records[outside]
	record.ListingRevision.DisplayName = "invoice café"
	source.records[outside] = record
	reader := actor(t, identity.ActorHuman, nil, map[string][]identity.Role{owner: {identity.RoleContributor}, target: {identity.RoleReader}})
	for _, tc := range []struct {
		text  string
		kinds []catalog.Kind
		after string
		want  []int
	}{
		{"invoice café", []catalog.Kind{catalog.KindMCPServer}, "", []int{103, 106}},
		{"invoice café", []catalog.Kind{catalog.KindMCPServer}, unitID(103), []int{106}},
		{"invoice", []catalog.Kind{catalog.KindAgent, catalog.KindSkill}, "", []int{101, 104}},
		{"invoice", []catalog.Kind{catalog.KindAgent, catalog.KindSkill}, unitID(101), []int{104}},
		{"invoice", []catalog.Kind{catalog.KindAgent, catalog.KindSkill}, unitID(104), nil},
		{"secret-tag", nil, "", nil}, {"secret-identifier", nil, "", nil}, {"inv", nil, "", nil}, {"%_*", nil, "", nil}, {"cafe", nil, "", nil},
		{"", []catalog.Kind{catalog.KindAgent}, "", []int{101}},
		{"", []catalog.Kind{catalog.KindSkill}, "", []int{104}},
		{"", []catalog.Kind{catalog.KindPlugin}, "", []int{105}},
		{" \t", []catalog.Kind{catalog.KindMCPServer}, "", []int{102, 103, 106, 107}},
	} {
		page, err := service.Members(reader, collection.ID, MemberRequest{Text: tc.text, Kinds: tc.kinds, AfterID: tc.after, Limit: 100})
		if err != nil {
			t.Fatal(tc, err)
		}
		ids := []string{}
		for _, item := range page.Items {
			ids = append(ids, item.Listing.ID)
		}
		want := []string{}
		for _, n := range tc.want {
			want = append(want, unitID(n))
		}
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("%+v: got %v want %v", tc, ids, want)
		}
	}
	page, err := service.Members(reader, collection.ID, MemberRequest{Text: "invoice café", Kinds: []catalog.Kind{catalog.KindMCPServer}, Limit: 1})
	if err != nil || len(page.Items) != 2 || page.Items[0].Listing.ID != unitID(103) {
		t.Fatal("filtered lookahead", page, err)
	}
	probe, err := service.Members(reader, collection.ID, MemberRequest{Text: "invoice café", Kinds: []catalog.Kind{catalog.KindMCPServer}})
	if err != nil || len(probe.Items) != 0 || probe.Collection.ID != collection.ID || probe.Generation == "" {
		t.Fatal("probe leaked members", probe, err)
	}
}
