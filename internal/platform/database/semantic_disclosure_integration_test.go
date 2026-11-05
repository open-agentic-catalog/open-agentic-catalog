//go:build integration

// Disclosure tests capture real SQL-selected metadata at the inference port.
// The provider is inert; PostgreSQL eligibility and immutable evidence are real.
package database

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

// semanticDisclosureActor reuses the fixture's author or provisions the second
// tenant's publication authority through the normal owner-only input.
func semanticDisclosureActor(t *testing.T, db *housekeepingDatabase, tenant string) context.Context {
	t.Helper()
	roles := []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor, identity.RolePublisher, identity.RoleReader}
	name := "Housekeeping tenant A"
	if tenant == housekeepingTenantB {
		name = "Housekeeping tenant B"
	}
	principal, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{
		TenantID: tenant, TenantDisplayName: name, Issuer: "https://identity.performance.example",
		TenancyMode: "dedicated", Subject: "search-author", ActorType: identity.ActorHuman, Roles: roles,
		Operator: "disclosure-test", Reason: "qualify synthetic embedding disclosure", AcknowledgeOutbox: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: tenant, Subject: "search-author", Roles: roles,
		MembershipID: principal.MembershipID, RealmID: principal.RealmID, ActorType: principal.ActorType})
}

// semanticDisclosureSnapshot makes conforming retained documents without
// treating publication generation itself as the input-selection oracle.
func semanticDisclosureSnapshot(t *testing.T, tenant string, generation int64, name string, record *catalog.CatalogRecord) publication.Snapshot {
	t.Helper()
	s := publicationScanFixture(t)
	var err error
	s.ID, err = (id.Generator{}).New(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.TenantID, s.Generation = tenant, generation
	s.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	s.ARD.Entries[0].DisplayName = name
	if record != nil {
		metadata := map[string]any{"catalogReleaseId": record.Release.ID, "privateEvidence": "PUBLIC_EVIDENCE_SENTINEL"}
		data := record.VersionRevision.Representations[0].Data
		entry := publication.Entry{Context: []any{publication.ARDContext, map[string]any{"privateTerm": "https://example.com/CONTEXT_SENTINEL"}},
			Identifier: record.Resource.CanonicalIdentifier, DisplayName: record.ListingRevision.DisplayName, Type: adapter.MediaA2AAgentCard,
			Data: data, Description: record.ListingRevision.Description, Tags: record.ListingRevision.Tags,
			RepresentativeQueries: record.ListingRevision.RepresentativeQueries, Metadata: metadata}
		s.ARD.Entries = append(s.ARD.Entries, entry)
		s.AICatalog.Entries = []publication.AICatalogEntry{{Identifier: entry.Identifier, DisplayName: entry.DisplayName, Type: entry.Type,
			Data: data, Description: entry.Description, Tags: entry.Tags, Extensions: map[string]any{
				"https://ai-catalog.org/extensions/metadata": metadata,
			}}}
	}
	for _, document := range []struct {
		value        any
		etag, digest *string
	}{{s.ARD, &s.ETag, &s.ARDContentDigest}, {s.AICatalog, &s.AICatalogETag, &s.AIContentDigest}} {
		raw, marshalErr := json.Marshal(document.value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		sum := sha256.Sum256(raw)
		*document.etag = `"` + hex.EncodeToString(sum[:]) + `"`
		*document.digest = "sha-256=:" + base64.StdEncoding.EncodeToString(sum[:]) + ":"
	}
	if err := publication.ValidateSnapshotIntegrity(s); err != nil {
		t.Fatalf("invalid disclosure snapshot: %v", err)
	}
	return s
}

// TestSemanticDisclosureUsesReleasedAndActiveSnapshotMetadata proves both
// selection paths exclude retained non-input fields and unrelated populations.
func TestSemanticDisclosureUsesReleasedAndActiveSnapshotMetadata(t *testing.T) {
	local := catalog.SearchDocument{Identifier: "urn:air:catalog.example.com:finance:disclosure", DisplayName: "Released listing",
		Description: "Released description", Tags: []string{"z", "a"}, RepresentativeQueries: []string{"Released question"}}
	db, repository, _, seed := newSearchCostFixture(t, local)
	card := json.RawMessage(`{"name":"RAW_NAME_SENTINEL","description":"RAW_DESCRIPTION_SENTINEL","version":"1.0.0","supportedInterfaces":[{"url":"https://agent.example.com/ENDPOINT_SENTINEL","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["text/plain"],"defaultOutputModes":["text/plain"],"skills":[{"id":"raw","name":"RAW_SKILL_SENTINEL","description":"Raw retained instructions","tags":["raw"]}]}`)
	seed(0, card)
	// The supported AI Catalog profile permits an absent displayName. Current
	// ARD requires it; an invalid ARD observation would not qualify this fallback.
	seedLexicalSourceProfile(t, db, repository, []catalog.SearchDocument{
		{Identifier: "urn:air:remote.example:finance:named", DisplayName: "Indexed listing", Description: "Indexed description", Tags: []string{"remote"}, RepresentativeQueries: []string{"Indexed question"}},
		{Identifier: "urn:air:remote.example:finance:fallback", Description: "Fallback description", Tags: []string{}, RepresentativeQueries: []string{"Fallback question"}},
	}, true)
	page, err := repository.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("local control population: %d %v", len(page.Items), err)
	}
	record, err := repository.GetCatalogRecord(t.Context(), housekeepingTenantA, page.Items[0].Release.ID)
	if err != nil || len(record.VersionRevision.Representations) != 1 || !strings.Contains(string(record.VersionRevision.Representations[0].Data), "RAW_DESCRIPTION_SENTINEL") {
		t.Fatalf("raw evidence was not retained: %v", err)
	}
	author := semanticDisclosureActor(t, db, housekeepingTenantA)
	otherAuthor := semanticDisclosureActor(t, db, housekeepingTenantB)
	publicationStore, err := NewPostgresPublicationStore(repository, "catalog.example.com")
	if err != nil {
		t.Fatal(err)
	}
	save := func(ctx context.Context, tenant string, generation int64, name string, record *catalog.CatalogRecord, activate bool) publication.Snapshot {
		t.Helper()
		s, saveErr := publicationStore.SaveCandidate(ctx, semanticDisclosureSnapshot(t, tenant, generation, name, record))
		if saveErr == nil && activate {
			s, saveErr = publicationStore.Activate(ctx, tenant, s.ID, time.Now())
		}
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		return s
	}
	retired := save(author, housekeepingTenantA, 1, "RETIRED_SENTINEL", &record, true)
	// The release text also occurs in Control and must not reuse that
	// disclosure's vector. Published ARD entries require a displayName.
	active := save(author, housekeepingTenantA, 2, "Catalog Registry", &record, true)
	candidate := save(author, housekeepingTenantA, 3, "CANDIDATE_SENTINEL", &record, false)
	other := save(otherAuthor, housekeepingTenantB, 1, "OTHER_TENANT_SENTINEL", nil, true)
	service, err := catalog.NewService(catalog.Options{Repository: repository, Clock: clock.Real{}, IDs: id.Generator{},
		Validator: adapter.NewRegistry(), CursorKey: []byte(strings.Repeat("k", 32)), Policy: "disclosure-test"})
	if err != nil {
		t.Fatal(err)
	}
	_, draft, err := service.CreateListingRevision(author, record.Listing.ID, catalog.CreateListingRevisionInput{
		VersionRevisionID: record.VersionRevision.ID, DisplayName: "DRAFT_SENTINEL", Description: "Later private description",
		PublisherID: record.ListingRevision.PublisherID, Visibility: catalog.VisibilityTenant,
	}, record.Listing.Revision)
	if err != nil || draft.DisplayName != "DRAFT_SENTINEL" {
		t.Fatalf("private revision control: %v", err)
	}
	retainedDraft, err := repository.GetListingRevision(t.Context(), housekeepingTenantA, draft.ID)
	if err != nil || retainedDraft.DisplayName != "DRAFT_SENTINEL" {
		t.Fatalf("private revision was not retained: %v", err)
	}
	// Every forbidden publication is really retained in the state being tested.
	for _, expected := range []struct {
		snapshot publication.Snapshot
		state    publication.SnapshotState
	}{
		{retired, publication.SnapshotRetired}, {active, publication.SnapshotActive},
		{candidate, publication.SnapshotCandidate}, {other, publication.SnapshotActive},
	} {
		got, readErr := publicationStore.Get(t.Context(), expected.snapshot.TenantID, expected.snapshot.ID)
		if readErr != nil || got.State != expected.state || !reflect.DeepEqual(got.ARD, expected.snapshot.ARD) {
			t.Fatalf("publication control was not retained: state=%s error=%v", got.State, readErr)
		}
	}
	store := provisionSemanticFixture(t, db)
	profile := "sha256:" + strings.Repeat("d", 64)
	workerJobs, err := NewPostgresJobStore(store.repository, profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, scope string
		want        []string
	}{
		{"control", "control", []string{
			"Name: Released listing\nDescription: Released description\nTags: a, z\nRepresentative queries: Released question",
			"Name: Indexed listing\nDescription: Indexed description\nTags: remote\nRepresentative queries: Indexed question",
			"Name: urn:air:remote.example:finance:fallback\nDescription: Fallback description\nTags: \nRepresentative queries: Fallback question",
		}},
		{"public", "public:" + active.ID, []string{
			"Name: Catalog Registry\nDescription: \nTags: \nRepresentative queries: Find agents",
			"Name: Released listing\nDescription: Released description\nTags: a, z\nRepresentative queries: Released question",
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			generationID, idErr := (id.Generator{}).New(time.Now())
			if idErr != nil {
				t.Fatal(idErr)
			}
			source, sourceErr := store.Current(t.Context(), housekeepingTenantA, test.scope)
			if sourceErr != nil {
				t.Fatal(sourceErr)
			}
			g := catalog.SemanticGeneration{ID: generationID, TenantID: housekeepingTenantA, Profile: profile, Scope: test.scope, SourceGeneration: source, Dimensions: 3}
			if admitted, admitErr := store.Admit(t.Context(), g, nil, 5); admitErr != nil || !admitted {
				t.Fatalf("admit=%v error=%v", admitted, admitErr)
			}
			job, claimErr := workerJobs.Claim(t.Context(), "disclosure", time.Now(), time.Minute)
			if claimErr != nil || job.ID != g.ID {
				t.Fatalf("claim=%s error=%v", job.ID, claimErr)
			}
			provider := &fixedIndexProvider{}
			indexer, indexErr := catalog.NewSemanticIndexer(store, provider, 16, 8192)
			if indexErr != nil {
				t.Fatal(indexErr)
			}
			count, runErr := indexer.Run(t.Context(), g, "disclosure")
			slices.Sort(provider.texts)
			want := slices.Clone(test.want)
			slices.Sort(want)
			if runErr != nil || count != int64(len(want)) || provider.calls != 1 || provider.inputs != len(want) || !slices.Equal(provider.texts, want) {
				t.Fatalf("disclosure: count=%d calls=%d inputs=%q error=%v", count, provider.calls, provider.texts, runErr)
			}
			if _, completeErr := workerJobs.Complete(t.Context(), g.TenantID, g.ID, "disclosure", time.Now(), json.RawMessage(`{}`)); completeErr != nil {
				t.Fatal(completeErr)
			}
		})
	}
	for _, denied := range []publication.Snapshot{retired, candidate, other} {
		g := catalog.SemanticGeneration{ID: active.ID, TenantID: housekeepingTenantA, Profile: profile,
			Scope: "public:" + denied.ID, SourceGeneration: "1", Dimensions: 3}
		if inputs, _, readErr := store.Inputs(t.Context(), g, "", 16); catalog.ErrorCodeOf(readErr) != catalog.CodeNotFound || len(inputs) != 0 {
			t.Fatalf("inactive or other-tenant snapshot yielded input: %v %v", inputs, readErr)
		}
	}
	retained, err := publicationStore.Active(t.Context(), housekeepingTenantA)
	if err != nil || !reflect.DeepEqual(retained, active) {
		t.Fatalf("indexing changed retained publication: %v", err)
	}
}
