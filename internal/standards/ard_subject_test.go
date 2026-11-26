package standards

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestResolveARDTermPreservesIRIAndLiteralPathSemantics(t *testing.T) {
	context := map[string]any{
		"vocab.example": ardNamespace, "topic": "http://purl.org/dc/terms/", "topic.example": "http://purl.org/dc/terms/",
		"extra": ardNamespace + "metadata", "entry.extra": ardNamespace + "metadata", "key": "https://vendor.example/ns#",
	}
	for _, tc := range []struct{ term, want string }{
		{"tags", "tags"}, {ardNamespace + "tags", "tags"}, {"vocab.example:tags", "tags"},
		{"topic:subject", ARDSubjectIRI}, {"topic.example:subject", ARDSubjectIRI}, {ARDSubjectIRI, ARDSubjectIRI},
		{"metadata.license", "metadata.license"}, {ardNamespace + "metadata.license", "metadata.license"},
		{"vocab.example:metadata.license", "metadata.license"}, {"metadata.unmapped:literal", "metadata.unmapped:literal"},
		// ARD 5.3.1 resolves only the leading term, even when the literal
		// metadata member has a prefix defined in the query context.
		{"metadata.key:literal", "metadata.key:literal"}, {"extra.key:literal", "metadata.key:literal"}, {"entry.extra.key:literal", "metadata.key:literal"},
		{"vocab.example:metadata.key:literal", "metadata.key:literal"}, {ardNamespace + "metadata.key:literal", "metadata.key:literal"},
	} {
		t.Run(tc.term, func(t *testing.T) {
			got, err := ResolveARDTerm(tc.term, context)
			if err != nil || got != tc.want {
				t.Fatalf("resolved=%q error=%v want=%q", got, err, tc.want)
			}
		})
	}
	for _, term := range []string{ARDSubjectIRI + ".nested", "topic:subject.nested", "metadata.", "vocab.example:metadata.", "https://unindexed.example/subject", "key:metadata.key:literal"} {
		if got, err := ResolveARDTerm(term, context); err == nil || got != "" {
			t.Fatalf("unindexed or empty path %q resolved to %q, %v", term, got, err)
		}
	}
}

func TestARDSubjectValuesUsesEntryContextAndLiteralShapes(t *testing.T) {
	for _, tc := range []struct {
		name, document string
		want           []string
		present, fails bool
	}{
		{"absent", `{"identifier":"urn:air:example.com:one"}`, nil, false, false},
		{"sparse remote context", `{"@context":"https://unavailable.invalid/context","identifier":"urn:air:example.com:one","type":"application/json"}`, nil, false, false},
		{"subject with embedded base context", `{"@context":"https://agenticresourcediscovery.org/context/v1","http://purl.org/dc/terms/subject":"finance"}`, []string{"finance"}, true, false},
		{"subject with unavailable context", `{"@context":"https://unavailable.invalid/context","http://purl.org/dc/terms/subject":"finance"}`, nil, true, true},
		{"full scalar", `{"http://purl.org/dc/terms/subject":"finance"}`, []string{"finance"}, true, false},
		{"full array", `{"http://purl.org/dc/terms/subject":["tools","finance","finance"]}`, []string{"finance", "tools"}, true, false},
		{"alias scalar", `{"@context":{"topic":"http://purl.org/dc/terms/"},"topic:subject":"finance"}`, []string{"finance"}, true, false},
		{"ordinary term alias", `{"@context":{"tags":"http://purl.org/dc/terms/subject"},"tags":["tools"]}`, []string{"tools"}, true, false},
		{"singleton", `{"@context":["https://agenticresourcediscovery.org/context/v1",{"topic.example":"http://purl.org/dc/terms/"}],"topic.example:subject":["finance"]}`, []string{"finance"}, true, false},
		{"aliases merge", `{"@context":{"x":"http://purl.org/dc/terms/","y":"http://purl.org/dc/terms/"},"x:subject":["tools","finance"],"y:subject":"finance"}`, []string{"finance", "tools"}, true, false},
		{"different namespace", `{"@context":{"topic":"https://other.example/"},"topic:subject":"finance"}`, nil, false, false},
		{"explicit null", `{"http://purl.org/dc/terms/subject":null}`, nil, true, false},
		{"empty array", `{"http://purl.org/dc/terms/subject":[]}`, nil, true, false},
		{"object", `{"http://purl.org/dc/terms/subject":{"name":"finance"}}`, nil, true, true},
		{"number", `{"http://purl.org/dc/terms/subject":1}`, nil, true, true},
		{"null element", `{"http://purl.org/dc/terms/subject":[null]}`, nil, true, true},
		{"reverse subject", `{"@context":{"topic":{"@reverse":"http://purl.org/dc/terms/subject"}},"topic":"finance"}`, nil, true, true},
		{"IRI subject", `{"@context":{"topic":{"@id":"http://purl.org/dc/terms/subject","@type":"@id"}},"topic":"https://example.com/finance"}`, nil, true, true},
		{"vocabulary subject", `{"@context":{"topic":{"@id":"http://purl.org/dc/terms/subject","@type":"@vocab"}},"topic":"finance"}`, nil, true, true},
		{"typed JSON subject", `{"@context":{"topic":{"@id":"http://purl.org/dc/terms/subject","@type":"@json"}},"topic":"finance"}`, nil, true, true},
		{"language on subject", `{"@context":{"@language":"fr","topic":"http://purl.org/dc/terms/subject"},"topic":"finance"}`, nil, true, true},
		{"scoped language on subject", `{"@context":{"topic":{"@id":"http://purl.org/dc/terms/subject","@context":{"@language":"fr"}}},"topic":"finance"}`, nil, true, true},
		{"subject with default language removed", `{"@context":{"@language":"fr","topic":{"@id":"http://purl.org/dc/terms/subject","@language":null}},"topic":"finance"}`, []string{"finance"}, true, false},
		{"ordered subject list", `{"@context":{"topic":{"@id":"http://purl.org/dc/terms/subject","@container":"@list"}},"topic":["finance","tools"]}`, nil, true, true},
		{"set of literal subjects", `{"@context":{"topic":{"@id":"http://purl.org/dc/terms/subject","@container":"@set"}},"topic":["finance","tools"]}`, []string{"finance", "tools"}, true, false},
		{"remote alias", `{"@context":"https://unavailable.invalid/context","topic:subject":"finance"}`, nil, false, true},
		{"disclosed subject and unresolved context", `{"@context":["https://unavailable.invalid/context"],"http://purl.org/dc/terms/subject":"finance"}`, nil, true, true},
		{"protected redefinition", `{"@context":[{"topic":{"@id":"http://purl.org/dc/terms/subject","@protected":true}},{"topic":"https://other.example/subject"}],"topic":"finance"}`, nil, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.document), &fields); err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(fields)
			got, present, err := ARDSubjectValues(fields)
			if (err != nil) != tc.fails || present != tc.present || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("subjects=%v present=%t error=%v, want %v/%t/%t", got, present, err, tc.want, tc.present, tc.fails)
			}
			after, _ := json.Marshal(fields)
			if string(before) != string(after) {
				t.Fatal("source fields changed")
			}
		})
	}
}

func TestOpaquePathsResolveDottedLeadingAliasesOnly(t *testing.T) {
	context := map[string]any{"native.body": "https://agenticresourcediscovery.org/ns#data", "entry.meta": "https://agenticresourcediscovery.org/ns#metadata", "id": "urn:unrelated-member"}
	for term, want := range map[string]string{"native.body.skills.id": "data.skills.id", "native.body.defaultInputModes": "data.defaultInputModes", "native.body.defaultOutputModes": "data.defaultOutputModes", "native.body.skills.tags": "data.skills.tags", "entry.meta.license": "metadata.license"} {
		got, err := ResolveARDTerm(term, context)
		if err != nil || got != want {
			t.Fatalf("%s = %s / %v, want %s", term, got, err, want)
		}
	}
	for _, term := range []string{"native.body.skills.id.extra", "native.body.", "unmapped.body.skills.id"} {
		if _, err := ResolveARDTerm(term, context); err == nil {
			t.Fatalf("unsupported path resolved: %s", term)
		}
	}
}
