package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

// Scripted policy-admitted bytes keep these native admission tests isolated.
// Existing transport suites own TLS, SSRF and credentials, not this helper.
type adoptionNativeFetcher struct {
	body  []byte
	now   time.Time
	calls int
}

func (*adoptionNativeFetcher) PolicyDigest() string { return "sha256:" + strings.Repeat("a", 64) }
func (f *adoptionNativeFetcher) FetchExternalRepresentation(_ context.Context, r catalog.ExternalRepresentationFetchRequest) (catalog.ExternalRepresentationFetchResult, error) {
	f.calls++
	return catalog.ExternalRepresentationFetchResult{RequestedURL: r.URL, FinalURL: r.URL, Status: 200, MediaType: r.MediaType, Body: append([]byte(nil), f.body...), ContentDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(f.body)), PolicyID: "adoption-native", PolicyRevision: 1, PolicyDigest: f.PolicyDigest(), RetrievedAt: f.now}, nil
}

func TestVersionAdoptionRetainsNativeValidationAndPackages(t *testing.T) {
	skill := []byte("---\nname: peer-skill\ndescription: Inert native admission fixture.\n---\n# Instructions\nReview metadata.\n")
	tests := []struct {
		name           string
		kind           catalog.Kind
		representation catalog.RepresentationInput
		body           []byte
	}{
		{"Agent", catalog.KindAgent, catalog.RepresentationInput{MediaType: MediaA2AAgentCard, Data: json.RawMessage(a2aBoundaryCard)}, nil},
		{"MCP", catalog.KindMCPServer, catalog.RepresentationInput{MediaType: MediaMCPServerCard, Data: json.RawMessage(`{"$schema":"https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json","name":"com.example/peer","description":"Provides example tools.","version":"1.0.0","remotes":[{"type":"streamable-http","url":"https://mcp.example/peer","supportedProtocolVersions":["2025-11-25"]}]}`)}, nil},
		{"Skill metadata", catalog.KindSkill, catalog.RepresentationInput{MediaType: MediaSkillJSON, Data: json.RawMessage(`{"name":"peer-skill","description":"Inert metadata"}`)}, nil},
		{"Skill Markdown", catalog.KindSkill, catalog.RepresentationInput{MediaType: MediaSkillMarkdown, Content: string(skill)}, nil},
		{"Skill HTTPS", catalog.KindSkill, catalog.RepresentationInput{MediaType: MediaSkillMarkdown, URL: "https://content.example/SKILL.md"}, skill},
		{"Plugin ZIP", catalog.KindPlugin, catalog.RepresentationInput{MediaType: catalog.PluginMediaZIP, URL: "https://content.example/plugin.zip"}, pluginArchive(t, catalog.PluginMediaZIP, map[string]string{"plugin.json": pluginManifest})},
		{"Plugin GZIP", catalog.KindPlugin, catalog.RepresentationInput{MediaType: catalog.PluginMediaGZIP, URL: "https://content.example/plugin.tar.gz"}, pluginArchive(t, catalog.PluginMediaGZIP, map[string]string{"plugin.json": pluginManifest})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clk := clock.NewFake(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
			key := sha256.Sum256([]byte(t.Name()))
			fetch := &adoptionNativeFetcher{body: tc.body, now: clk.Now()}
			repo := catalog.NewMemoryRepository()
			service, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clk, IDs: id.Generator{}, Validator: NewRegistry(), ExternalFetcher: fetch, CursorKey: key[:], Policy: "adoption-native"})
			if err != nil {
				t.Fatal(err)
			}
			actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", MembershipID: "00000000-0000-7000-8000-000000000002", RealmID: "00000000-0000-7000-8000-000000000003", ActorType: identity.ActorHuman, Subject: "curator", Roles: []identity.Role{identity.RoleTenantAdmin}}
			ctx := identityfixture.WithActor(t.Context(), actor)
			ns, err := service.CreateNamespace(ctx, catalog.CreateNamespaceInput{Slug: "adopt", DisplayName: "Adopt"})
			if err != nil {
				t.Fatal(err)
			}
			pub, err := service.CreatePublisher(ctx, catalog.CreatePublisherInput{DisplayName: "Local", Domains: []string{"content.example"}})
			if err != nil {
				t.Fatal(err)
			}
			rep := tc.representation
			rep.Role = catalog.RepresentationPrimary
			if catalog.IsPluginMediaType(rep.MediaType) {
				digest := fmt.Sprintf("sha256:%x", sha256.Sum256(tc.body))
				rep.ExpectedDigest = &digest
			}
			selection := catalog.ExternalObservationAdoptionInput{NamespaceID: ns.ID, PublisherID: pub.ID, CanonicalIdentifier: "urn:air:content.example:resources:adopt", Kind: tc.kind, Version: "1.0.0", DisplayName: "Local", Description: "Retained local description", Visibility: catalog.VisibilityPrivate, Representation: rep, Provenance: catalog.AdoptionProvenance{ObservationID: "00000000-0000-7000-8000-000000000010", SourceID: "00000000-0000-7000-8000-000000000011", SourceRevision: 1, SnapshotID: "00000000-0000-7000-8000-000000000012", RetrievalDigest: fetch.PolicyDigest(), EntryLocation: "/entries/0", ExactEntryDigest: fetch.PolicyDigest(), NormalizedEntryDigest: fetch.PolicyDigest(), NormalizationProfile: "catalog-observation-v1"}}
			first, err := service.AdoptExternalObservation(ctx, selection)
			if err != nil {
				t.Fatal(err)
			}
			selection.Version = "2.0.0"
			selection.Provenance.ObservationID = "00000000-0000-7000-8000-000000000013"
			selection.Representation.Data = json.RawMessage(strings.ReplaceAll(string(rep.Data), "1.0.0", "2.0.0"))
			result, err := service.AdoptExternalVersion(ctx, catalog.ExternalVersionAdoptionInput{ResourceID: first.Resource.ID, PriorVersionRevisionID: first.VersionRevision.ID, Selection: selection})
			if err != nil {
				t.Fatal(err)
			}
			representation := result.VersionRevision.Representations[0]
			if representation.ValidationState != "valid" {
				t.Fatal("native append invalid", result.Evidence.Findings)
			}
			if result.VersionRevision.Adoption == nil || result.VersionRevision.Adoption.ObservationID != selection.Provenance.ObservationID || result.Version.ResourceID != first.Resource.ID {
				t.Fatal("missing exact native provenance")
			}

			if catalog.IsPluginMediaType(rep.MediaType) && (representation.Plugin == nil || len(representation.Plugin.Documents) == 0) {
				t.Fatal("Plugin inspection missing")
			}
			if tc.body != nil && (fetch.calls != 2 || len(result.Evidence.ExternalRetrievals) != 1) {
				t.Fatal("retrieval evidence missing", fetch.calls)
			}
			retained, _, err := repo.GetVersionRevision(ctx, actor.TenantID, first.VersionRevision.ID)
			if err != nil || retained.ContentSetDigest != first.VersionRevision.ContentSetDigest {
				t.Fatal("old native content changed", err)
			}
			if catalog.IsPluginMediaType(rep.MediaType) {
				before, err := service.ListVersions(ctx, first.Resource.ID, catalog.HistoryPageRequest{Limit: 10})
				if err != nil {
					t.Fatal(err)
				}
				selection.Version = "3.0.0"
				bad := "sha256:" + strings.Repeat("0", 64)
				selection.Representation.ExpectedDigest = &bad
				if _, err := service.AdoptExternalVersion(ctx, catalog.ExternalVersionAdoptionInput{ResourceID: first.Resource.ID, PriorVersionRevisionID: first.VersionRevision.ID, Selection: selection}); catalog.ErrorCodeOf(err) != catalog.CodeValidationFailed {
					t.Fatal("integrity failure admitted", err)
				}
				after, err := service.ListVersions(ctx, first.Resource.ID, catalog.HistoryPageRequest{Limit: 10})
				if err != nil || len(after) != len(before) {
					t.Fatal("failed acquisition left partial Version", err)
				}
			}
		})
	}
}
