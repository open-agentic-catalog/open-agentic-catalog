//go:build functional

// The upstream corpus also passes through normal authoring, immutable readback,
// governance and publication. Only archive transport is replaced; this is a
// composed functional test, not an isolated validator or native consumer test.
package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

const upstreamPluginURL = "https://packages.example.com/context7.zip"

// corpusArchiveFetcher supplies only the invocation-owned archive address. Any
// attempt to follow a resource endpoint fails instead of using a real network.
type corpusArchiveFetcher struct {
	body       []byte
	clock      *clock.Fake
	calls      int
	unexpected []string
}

// PolicyDigest binds the owned retrieval to a stable test-only outbound policy.
func (f *corpusArchiveFetcher) PolicyDigest() string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("upstream-corpus-test-policy")))
}

// FetchExternalRepresentation refuses every destination except the owned archive.
func (f *corpusArchiveFetcher) FetchExternalRepresentation(_ context.Context, r catalog.ExternalRepresentationFetchRequest) (catalog.ExternalRepresentationFetchResult, error) {
	if r.URL != upstreamPluginURL {
		f.unexpected = append(f.unexpected, r.URL)
		return catalog.ExternalRepresentationFetchResult{}, fmt.Errorf("unexpected external request %s", r.URL)
	}
	f.calls++
	return catalog.ExternalRepresentationFetchResult{RequestedURL: r.URL, FinalURL: r.URL, Status: 200, MediaType: "application/zip", Body: append([]byte(nil), f.body...), ContentDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(f.body)), PolicyID: "upstream-corpus", PolicyRevision: 1, PolicyDigest: f.PolicyDigest(), RetrievedAt: f.clock.Now()}, nil
}

// corpusFile reads shared inert source bytes; the unit suite owns the complete
// independently checked source inventory and licenses.
func corpusFile(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("../adapter/testdata/upstream", filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// corpusZIP changes only the archive container, preserving native member bytes
// and file modes. It neither extracts nor runs the packaged source files.
func corpusZIP(t *testing.T, directory, prefix string) []byte {
	t.Helper()
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	base := filepath.Join("../adapter/testdata/upstream", filepath.FromSlash(directory))
	err := filepath.WalkDir(base, func(name string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(base, name)
		if err != nil {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		header := &zip.FileHeader{Name: prefix + filepath.ToSlash(relative), Method: zip.Store}
		header.SetMode(info.Mode())
		member, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		raw, err := fs.ReadFile(os.DirFS(base), filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		_, err = member.Write(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// TestReviewedUpstreamGovernanceAndPublication checks the selected evidence at
// each boundary. Its repository, fetch port and clocks are invocation-owned.
func TestReviewedUpstreamGovernanceAndPublication(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC))
	fetch := &corpusArchiveFetcher{body: corpusZIP(t, "context7/plugin", ""), clock: c}
	t.Cleanup(func() {
		if len(fetch.unexpected) != 0 {
			t.Errorf("metadata validation attempted resource requests: %v", fetch.unexpected)
		}
	})
	key := sha256.Sum256([]byte(t.Name()))
	s, err := catalog.NewService(catalog.Options{Repository: catalog.NewMemoryRepository(), Clock: c, IDs: id.Generator{}, Validator: adapter.NewRegistry(), ExternalFetcher: fetch, CursorKey: key[:], Policy: "upstream-corpus"})
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", MembershipID: "00000000-0000-7000-8000-000000000002", RealmID: "00000000-0000-7000-8000-000000000003", ActorType: identity.ActorHuman, Subject: "author", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor, identity.RolePublisher, identity.RoleReader}}
	ctx := identityfixture.WithActor(t.Context(), actor)
	reviewer := actor
	reviewer.Subject = "reviewer"
	reviewer.MembershipID = "00000000-0000-7000-8000-000000000004"
	reviewer.Roles = []identity.Role{identity.RoleReviewer}
	reviewctx := identityfixture.WithActor(t.Context(), reviewer)
	ns, err := s.CreateNamespace(ctx, catalog.CreateNamespaceInput{Slug: "upstream", DisplayName: "Reviewed upstream fixtures"})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := s.CreatePublisher(ctx, catalog.CreatePublisherInput{DisplayName: "Corpus test publisher", Domains: []string{"catalog.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err = s.ApprovePublisherPublicationDomain(ctx, publisher.ID, "catalog.example.com", "owned metadata fixture", publisher.Revision)
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(fetch.body))
	handoff := &catalog.SkillHandoff{Git: &catalog.GitHandoffReference{Repository: "https://github.com/openai/skills", Commit: "49f948faa9258a0c61caceaf225e179651397431", Directory: "skills/.curated/jupyter-notebook"}}
	cases := []struct {
		name    string
		kind    catalog.Kind
		version string
		input   catalog.RepresentationInput
	}{
		{"researcher", catalog.KindAgent, "1.0.0", catalog.RepresentationInput{MediaType: adapter.MediaA2AAgentCard, Data: corpusFile(t, "a2a-dotnet/researcher-card.json")}},
		{"jupyter-notebook", catalog.KindSkill, "1.0.0", catalog.RepresentationInput{MediaType: adapter.MediaSkillZIP, PackageData: corpusZIP(t, "openai/jupyter-notebook", "jupyter-notebook/"), Handoff: handoff}},
		{"hf-cli", catalog.KindSkill, "1.0.0", catalog.RepresentationInput{MediaType: adapter.MediaSkillMarkdownARD, Content: string(corpusFile(t, "huggingface/hf-cli/SKILL.md"))}},
		{"playwright", catalog.KindMCPServer, "0.0.85", catalog.RepresentationInput{MediaType: adapter.MediaMCPRegistry, Data: corpusFile(t, "playwright/server.json")}},
		{"context7-registry", catalog.KindMCPServer, "4.0.3", catalog.RepresentationInput{MediaType: adapter.MediaMCPRegistry, Data: corpusFile(t, "context7/server.json")}},
		{"context7-plugin", catalog.KindPlugin, "1.0.2", catalog.RepresentationInput{MediaType: catalog.PluginMediaZIP, URL: upstreamPluginURL, ExpectedDigest: &digest}},
	}
	retained := map[string]catalog.Representation{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			canonical := "urn:air:catalog.example.com:upstream:" + tc.name
			resource, err := s.CreateResource(ctx, catalog.CreateResourceInput{NamespaceID: ns.ID, CanonicalIdentifier: canonical, Kind: tc.kind})
			if err != nil {
				t.Fatal(err)
			}
			input := tc.input
			input.Role = catalog.RepresentationPrimary
			_, revision, evidence, err := s.CreateVersion(ctx, resource.ID, catalog.CreateVersionInput{Version: tc.version, Representations: []catalog.RepresentationInput{input}})
			if err != nil {
				t.Fatal(err)
			}
			if evidence.State != catalog.EvidenceComplete || evidence.Trust.State != catalog.TrustNotAssessed || evidence.LegalRights.State != catalog.LegalRightsNotAssessed || len(revision.Representations) != 1 {
				t.Fatalf("validation evidence=%+v", evidence)
			}
			rep := revision.Representations[0]
			if rep.ValidationState != "valid" {
				t.Fatalf("invalid representation: %+v", rep)
			}
			source := []byte(tc.input.Data)
			if len(source) != 0 {
				// Inline JSON is retained in the Control API's deterministic
				// encoding. Compare all source fields, not upstream whitespace.
				var fields any
				if err := json.Unmarshal(source, &fields); err != nil {
					t.Fatal(err)
				}
				source, err = json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
			}
			if tc.input.Content != "" {
				source = []byte(tc.input.Content)
			}
			if tc.input.PackageData != nil {
				source = tc.input.PackageData
			}
			if tc.kind == catalog.KindPlugin {
				source = fetch.body
			}
			if rep.ContentDigest != fmt.Sprintf("sha256:%x", sha256.Sum256(source)) || rep.Size != int64(len(source)) {
				t.Fatal("source identity changed on authoring")
			}
			if len(tc.input.Data) > 0 && !bytes.Equal(rep.Data, source) {
				t.Fatal("retained native JSON fields changed")
			}
			if rep.ArtifactID != "" {
				_, read, err := s.GetArtifact(ctx, rep.ArtifactID)
				if err != nil || !bytes.Equal(read, source) {
					t.Fatalf("artifact bytes changed: %v", err)
				}
			}
			if tc.name == "jupyter-notebook" && (rep.Package == nil || rep.Package.FileCount != 12 || rep.Package.ExpandedSize != 30864 || !reflect.DeepEqual(rep.Handoff, handoff)) {
				t.Fatal("private package inventory or Git origin changed")
			}
			if tc.kind == catalog.KindPlugin && (rep.Plugin == nil || len(rep.Plugin.Files) != 5 || len(rep.Plugin.Components) != 2 || rep.ArtifactID != "" || rep.URL != upstreamPluginURL) {
				t.Fatal("Plugin archive retention boundary changed")
			}
			if rep.Plugin != nil {
				if len(rep.Plugin.Documents) != 3 {
					t.Fatal("incomplete retained Plugin documents")
				}
				documents := map[string]bool{"plugin.json": true, "mcp.json": true, "skills/context7-mcp/SKILL.md": true}
				for _, document := range rep.Plugin.Documents {
					if !documents[document.Path] || document.Content != string(corpusFile(t, "context7/plugin/"+document.Path)) {
						t.Fatalf("retained Plugin document changed: %s", document.Path)
					}
					delete(documents, document.Path)
				}
				if len(documents) != 0 {
					t.Fatalf("missing retained Plugin documents: %v", documents)
				}
			}
			loaded, _, _, err := s.GetVersionRevision(ctx, revision.ID)
			if err != nil || !reflect.DeepEqual(loaded.Representations, revision.Representations) {
				t.Fatalf("immutable readback: %v", err)
			}
			_, renewed, _, err := s.RevalidateVersionRevision(ctx, revision.ID)
			if err != nil || renewed.State != catalog.EvidenceComplete || renewed.ID == evidence.ID {
				t.Fatalf("revalidation=%+v %v", renewed, err)
			}
			loaded, _, _, err = s.GetVersionRevision(ctx, revision.ID)
			if err != nil || !reflect.DeepEqual(loaded.Representations, revision.Representations) {
				t.Fatal("revalidation rewrote source evidence")
			}
			listing, err := s.CreateListing(ctx, catalog.CreateListingInput{NamespaceID: ns.ID, ResourceID: resource.ID})
			if err != nil {
				t.Fatal(err)
			}
			listing, _, err = s.CreateListingRevision(ctx, listing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: revision.ID, DisplayName: tc.name, Description: "Reviewed upstream metadata fixture", RepresentativeQueries: []string{"Find " + tc.name, "Inspect " + tc.name + " metadata"}, PublisherID: publisher.ID, Visibility: catalog.VisibilityPublic}, listing.Revision)
			if err != nil {
				t.Fatal(err)
			}
			submission, err := s.Submit(ctx, listing.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = s.DecideSubmission(reviewctx, submission.ID, "approve", "metadata and inventory only", submission.Revision); err != nil {
				t.Fatal(err)
			}
			_, release, _, err := s.Publish(ctx, listing.ID, catalog.PublishInput{SubmissionID: submission.ID}, listing.Revision)
			if err != nil {
				t.Fatal(err)
			}
			record, err := s.GetCatalogRecord(ctx, release.ID)
			if err != nil || record.Resource.CanonicalIdentifier != canonical || !reflect.DeepEqual(record.VersionRevision.Representations, revision.Representations) {
				t.Fatalf("release readback: %v", err)
			}
			retained[canonical] = rep
		})
	}
	if t.Failed() {
		return
	}
	if len(retained) != 6 || fetch.calls == 0 {
		t.Fatal("incomplete corpus or archive retrieval was not exercised")
	}
	manager, err := publication.NewManager(publication.Options{Records: s, Store: publication.NewMemoryStore(), Clock: c, IDs: id.Generator{}, BaseURL: "https://catalog.example.com", PublisherDomain: "catalog.example.com", ARDProfile: "ard-0.91-aa3e598", AICatalogProfile: "ai-catalog-1.0-b062278", CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.BuildCandidate(ctx, actor.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	if err := publication.ValidateSnapshotIntegrity(snapshot); err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{"urn:air:catalog.example.com:registry:open-agentic-catalog": "application/ai-registry+json", "urn:air:catalog.example.com:upstream:researcher": adapter.MediaA2AAgentCard, "urn:air:catalog.example.com:upstream:hf-cli": adapter.MediaSkillMarkdownARD, "urn:air:catalog.example.com:upstream:context7-plugin": catalog.PluginMediaZIP}
	if len(snapshot.ARD.Entries) != len(expected) || len(snapshot.AICatalog.Entries) != 3 {
		t.Fatalf("unexpected discovery membership: ARD=%d AI=%d exclusions=%+v", len(snapshot.ARD.Entries), len(snapshot.AICatalog.Entries), snapshot.Exclusions)
	}
	for _, entry := range snapshot.ARD.Entries {
		want, ok := expected[entry.Identifier]
		if !ok || entry.Type != want {
			t.Fatalf("unexpected discovery entry=%+v", entry)
		}
		delete(expected, entry.Identifier)
		if rep, ok := retained[entry.Identifier]; ok {
			if !bytes.Equal(entry.Data, rep.Data) {
				t.Fatalf("published native JSON changed for %s", entry.Identifier)
			}
			if rep.ArtifactID != "" && entry.URL != "https://catalog.example.com/public/v1/artifacts/"+rep.ArtifactID {
				t.Fatalf("wrong published Markdown artifact URL: %s", entry.URL)
			}
		}
		if entry.Identifier == "urn:air:catalog.example.com:upstream:context7-plugin" && (entry.URL != upstreamPluginURL || len(entry.Data) != 0 || entry.Metadata["packageDigest"] != digest || entry.Metadata["packageSize"] != int64(len(fetch.body))) {
			t.Fatalf("Plugin projection=%+v", entry)
		}
	}
	if len(expected) != 0 {
		t.Fatalf("missing discovery members=%v", expected)
	}
	// Both publication formats must retain the same selected resources.
	aiExpected := map[string]string{
		"urn:air:catalog.example.com:upstream:researcher":      adapter.MediaA2AAgentCard,
		"urn:air:catalog.example.com:upstream:hf-cli":          adapter.MediaSkillMarkdown,
		"urn:air:catalog.example.com:upstream:context7-plugin": catalog.PluginMediaZIP,
	}
	for _, entry := range snapshot.AICatalog.Entries {
		if want, ok := aiExpected[entry.Identifier]; !ok || entry.Type != want {
			t.Fatalf("unexpected AI Catalog member: %+v", entry)
		}
		delete(aiExpected, entry.Identifier)
		if rep := retained[entry.Identifier]; !bytes.Equal(entry.Data, rep.Data) {
			t.Fatalf("AI Catalog native JSON changed for %s", entry.Identifier)
		}
		if entry.Identifier == "urn:air:catalog.example.com:upstream:context7-plugin" {
			metadata, ok := entry.Extensions["https://ai-catalog.org/extensions/metadata"].(map[string]any)
			if !ok || entry.URL != upstreamPluginURL || metadata["packageDigest"] != digest || metadata["packageSize"] != int64(len(fetch.body)) {
				t.Fatalf("AI Catalog Plugin archive identity changed: %+v", entry)
			}
		}
	}
	if len(aiExpected) != 0 {
		t.Fatalf("missing AI Catalog members: %v", aiExpected)
	}
	excluded := map[string]string{
		"urn:air:catalog.example.com:upstream:jupyter-notebook":  "PUBLIC_DELIVERY_FORM_REQUIRED",
		"urn:air:catalog.example.com:upstream:playwright":        "STANDARD_PUBLIC_REPRESENTATION_REQUIRED",
		"urn:air:catalog.example.com:upstream:context7-registry": "STANDARD_PUBLIC_REPRESENTATION_REQUIRED",
	}
	if len(snapshot.Exclusions) != len(excluded) {
		t.Fatalf("unexpected exclusions: %+v", snapshot.Exclusions)
	}
	for _, exclusion := range snapshot.Exclusions {
		want, ok := excluded[exclusion.Identifier]
		if !ok || !reflect.DeepEqual(exclusion.Reasons, []string{want}) || !reflect.DeepEqual(exclusion.Profiles, []string{"ard-0.91-aa3e598", "ai-catalog-1.0-b062278"}) {
			t.Fatalf("wrong publication exclusion: %+v", exclusion)
		}
		delete(excluded, exclusion.Identifier)
	}
	if len(excluded) != 0 {
		t.Fatalf("missing publication exclusions: %v", excluded)
	}
	if !reflect.DeepEqual(snapshot.ArtifactIDs, []string{retained["urn:air:catalog.example.com:upstream:hf-cli"].ArtifactID}) {
		t.Fatalf("unexpected public artifact inventory: %v", snapshot.ArtifactIDs)
	}
	active, err := manager.Activate(ctx, actor.TenantID, snapshot.ID, "activate reviewed inert corpus")
	if err != nil || active.State != publication.SnapshotActive {
		t.Fatalf("activation: %+v %v", active, err)
	}
}
