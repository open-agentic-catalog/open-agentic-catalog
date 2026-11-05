package database

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

func TestDecodeFederationSourceUsesAuthoritativeControlledProjection(t *testing.T) {
	now := time.Date(2026, 8, 31, 16, 30, 0, 0, time.UTC)
	raw := []byte(`{"id":"00000000-0000-7000-8000-000000000001","tenantId":"00000000-0000-7000-8000-000000000002","displayName":"Current","type":"ard_manifest","endpoint":"https://source.example/catalog","status":"active","profileId":"ard-0.91-aa3e598","retrievalMode":"hybrid","scope":{"kinds":["agent"]},"updateMode":"manual_review","live":{"enabled":false},"referrals":{"enabled":true,"maxHops":1,"approvedSourceIds":["00000000-0000-7000-8000-000000000003"]},"discoveryAssignments":[{"host":"catalog.example.com","audience":"authenticated"}],"policyId":"current-policy","policyRevision":2,"policyDigest":"sha256:` + strings.Repeat("b", 64) + `","credentialRevision":"credential-2","revision":1,"createdBy":"legacy","createdAt":"2026-08-31T16:30:00Z","updatedAt":"2026-08-31T16:30:00Z"}`)
	assignments := []byte(`[{"host":"catalog.example.com","audience":"authenticated"}]`)
	referrals := []byte(`["00000000-0000-7000-8000-000000000003"]`)
	digest := "sha256:" + strings.Repeat("b", 64)
	source, err := decodeFederationSource(raw, raw, federation.SourceActive, 1, "", nil, now, true, string(federation.RetrievalHybrid), "current-policy", 2, digest, "credential-2", assignments, referrals)
	if err != nil {
		t.Fatal(err)
	}
	if source.RetrievalMode != federation.RetrievalHybrid || source.PolicyID != "current-policy" || source.PolicyRevision != 2 || source.PolicyDigest != digest || source.CredentialRevision != "credential-2" || len(source.DiscoveryAssignments) != 1 || source.DiscoveryAssignments[0].Host != "catalog.example.com" || len(source.Referrals.ApprovedSourceIDs) != 1 || source.Referrals.ApprovedSourceIDs[0] != "00000000-0000-7000-8000-000000000003" {
		t.Fatalf("decoded controlled projection = %#v", source)
	}

	if _, err := decodeFederationSource(raw, raw, federation.SourceActive, 1, "", nil, now, false, "", "", 0, "", "", []byte(`[]`), []byte(`[]`)); err == nil {
		t.Fatal("revision-less source was accepted")
	}

}

func TestDecodeFederationSourceFailsClosedOnCorruptStoredConfiguration(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("b", 64)
	valid := []byte(`{"displayName":"Source","retrievalMode":"hybrid","policyId":"current-policy","policyRevision":2,"policyDigest":"` + digest + `","credentialRevision":"credential-2"}`)
	decode := func(raw, current, assignments, referrals []byte) error {
		_, err := decodeFederationSource(raw, current, federation.SourceActive, 1, "", nil, now, true, "hybrid", "current-policy", 2, digest, "credential-2", assignments, referrals)
		return err
	}
	if err := decode(valid, valid, []byte(`[]`), []byte(`[]`)); err != nil {
		t.Fatalf("current baseline rejected: %v", err)
	}
	for name, call := range map[string]func() error{
		"configuration": func() error {
			return decode([]byte(`{`), valid, []byte(`[]`), []byte(`[]`))
		},
		"current state": func() error {
			return decode(valid, []byte(`{`), []byte(`[]`), []byte(`[]`))
		},
		"audiences": func() error {
			return decode(valid, valid, []byte(`{`), []byte(`[]`))
		},
		"referrals": func() error {
			return decode(valid, valid, []byte(`[]`), []byte(`{`))
		},
		"audience mismatch": func() error {
			return decode(valid, valid, []byte(`[{"host":"catalog.example","audience":"public"}]`), []byte(`[]`))
		},
		"referral mismatch": func() error {
			return decode(valid, valid, []byte(`[]`), []byte(`["00000000-0000-7000-8000-000000000003"]`))
		},
		"policy mismatch": func() error {
			return decode([]byte(strings.Replace(string(valid), "current-policy", "different-policy", 1)), valid, []byte(`[]`), []byte(`[]`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
				t.Fatalf("corrupt stored %s returned %v", name, err)
			}
		})
	}

}

func TestMarshalFederationSourceConfigurationOmitsMutableSyncState(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	raw, err := marshalFederationSourceConfiguration(federation.Source{
		ID:                     "00000000-0000-7000-8000-000000000001",
		LastCompleteSnapshotID: "00000000-0000-7000-8000-000000000002",
		LastSyncAt:             &now,
		LastError:              "safe diagnostic",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"lastCompleteSnapshotId", "lastSyncAt", "lastError"} {
		if strings.Contains(string(raw), `"`+field+`"`) {
			t.Fatalf("immutable source configuration retained %q: %s", field, raw)
		}
	}
}

func TestCatalogSearchDatabaseParametersAndFacetMappings(t *testing.T) {
	t.Parallel()

	tags := []string{"finance", "reviewed"}
	parameters := catalogSearchParameters("tenant-one", catalog.SearchQuery{Text: "vendor.example/agent", Kinds: []catalog.Kind{catalog.KindAgent, catalog.KindSkill}, Tags: tags, Publisher: "publisher-one", Visibility: catalog.VisibilityPrivate, License: "MIT", Categories: []string{"finance"}})
	if len(parameters) != 10 {
		t.Fatalf("Catalog search parameters = %#v", parameters)
	}
	kinds, kindsOK := parameters[3].([]string)
	parameterTags, tagsOK := parameters[4].([]string)
	parameterCategories, categoriesOK := parameters[9].([]string)
	if !kindsOK || !tagsOK || !categoriesOK || parameters[0] != "tenant-one" || parameters[1] != "vendor.example/agent" || parameters[2] != "vendor.example/agent" || !slices.Equal(kinds, []string{"agent", "skill"}) || !slices.Equal(parameterTags, tags) || parameters[5] != "publisher-one" || parameters[6] != "private" || parameters[8] != "MIT" || !slices.Equal(parameterCategories, []string{"finance"}) {
		t.Fatalf("Catalog search parameters = %#v", parameters)
	}
	tags[0] = "changed"
	if parameterTags[0] != "finance" {
		t.Fatal("Catalog search SQL parameters alias request tags")
	}

	want := map[string]string{
		"kind": "ARRAY[kind::text]", "visibility": "ARRAY[visibility::text]",
		"publisher": "ARRAY[publisher_id::text]", "namespace": "ARRAY[namespace_id::text]",
		"trustState": "ARRAY[trust_state]", "source": "ARRAY['local'::text]",
		"tag": "tags", "category": "categories",
	}
	for field, expected := range want {
		expression, ok := catalogFacetValuesSQL(field)
		if !ok || expression != expected {
			t.Errorf("facet mapping for %q = %q, %t", field, expression, ok)
		}
	}
	if expression, ok := catalogFacetValuesSQL("unknown"); ok || expression != "" {
		t.Fatalf("unknown facet mapping = %q, %t", expression, ok)
	}
}
