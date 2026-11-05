package publication

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestPluginDiscoveryPreservesArchiveIdentityInBothProfiles(t *testing.T) {
	record := eligiblePublicationRecord()
	record.Resource.Kind = catalog.KindPlugin
	record.Resource.ID = "plugin-resource"
	record.Release.ID = "plugin-release"
	record.Resource.CanonicalIdentifier = "urn:air:catalog.example.com:default:tools"
	manifest := `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"tools"}`
	inspection := &catalog.PluginInspection{ExpandedSize: int64(len(manifest)), Files: []catalog.PluginFile{{Path: "plugin.json", Size: int64(len(manifest)), Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(manifest)))}}, Documents: []catalog.PluginDocument{{Path: "plugin.json", Content: manifest}}, Assessments: catalog.NotAssessedSkillPackageAssessments()}
	for _, media := range []string{catalog.PluginMediaZIP, catalog.PluginMediaGZIP} {
		t.Run(media, func(t *testing.T) {
			archive := pluginRoundTripArchive(t, media, manifest)
			rep := catalog.Representation{Role: catalog.RepresentationPrimary, MediaType: media, ProfileID: catalog.PluginProfile, URL: "https://packages.example.com/tools.archive", ContentDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(archive)), Size: int64(len(archive)), ValidationState: "valid", Plugin: inspection}
			if err := catalog.ValidatePackageRepresentation(rep); err != nil {
				t.Fatal(err)
			}
			record.VersionRevision.Representations = []catalog.Representation{rep}
			baseline := eligiblePublicationRecord()
			manager, _, _ := newPublicationManager(t, []catalog.CatalogRecord{record, baseline})
			snapshot, err := manager.BuildCandidate(publicationContext(context.Background(), record.Resource.TenantID), record.Resource.TenantID)
			if err != nil {
				t.Fatal(err)
			}
			if err = ValidateSnapshotIntegrity(snapshot); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, entry := range snapshot.ARD.Entries {
				if entry.Identifier != record.Resource.CanonicalIdentifier {
					continue
				}
				found = true
				if entry.URL != rep.URL || entry.Type != media || len(entry.Data) != 0 || entry.Metadata["packageDigest"] != rep.ContentDigest || entry.Metadata["packageSize"] != rep.Size {
					t.Fatalf("ARD Plugin entry: %+v", entry)
				}
				if len(snapshot.AICatalog.Entries) != 2 {
					t.Fatalf("AI Catalog entries: %+v", snapshot.AICatalog.Entries)
				}
				ai := snapshot.AICatalog.Entries[1]
				metadata, _ := ai.Extensions[aiCatalogMetadataExtension].(map[string]any)
				if ai.URL != entry.URL || ai.Type != media || metadata["packageDigest"] != rep.ContentDigest || metadata["packageSize"] != rep.Size {
					t.Fatalf("AI Catalog Plugin entry: %+v", ai)
				}
			}
			if !found || len(snapshot.ArtifactIDs) != 0 {
				t.Fatal("Plugin missing or incorrectly hosted as an artifact")
			}
			for _, wire := range []struct {
				media    string
				document any
			}{{"application/ard+json", snapshot.ARD}, {"application/ai-catalog+json", snapshot.AICatalog}} {
				t.Run(wire.media, func(t *testing.T) { verifyPluginPeerAdoption(t, wire.media, wire.document, rep, archive) })
			}
		})
	}
}

func pluginRoundTripArchive(t *testing.T, media, manifest string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if media == catalog.PluginMediaZIP {
		w := zip.NewWriter(&buf)
		f, err := w.Create("plugin.json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(manifest)); err != nil {
			t.Fatal(err)
		}
		if err = w.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		g := gzip.NewWriter(&buf)
		w := tar.NewWriter(g)
		if err := w.WriteHeader(&tar.Header{Name: "plugin.json", Mode: 0600, Size: int64(len(manifest))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(manifest)); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if err := g.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

// This in-process peer uses exact generated discovery bytes and a separate
// Catalog repository. Only outbound transport is replaced; adapters, source
// inspection/sync, indexed filtering and explicit adoption remain real code.
type pluginPeerTransport struct {
	document     []byte
	media        string
	archive      []byte
	rep          catalog.Representation
	now          time.Time
	archiveReads int
}

func (f *pluginPeerTransport) PolicyDigest() string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("plugin-peer")))
}
func (f *pluginPeerTransport) Fetch(_ context.Context, r fetcher.Request) (fetcher.Result, error) {
	if r.URL != "https://catalog.example.com/catalog.json" {
		return fetcher.Result{}, fmt.Errorf("unexpected discovery fetch: %s", r.URL)
	}
	return fetcher.Result{Status: http.StatusOK, RequestedURL: r.URL, FinalURL: r.URL, MediaType: f.media, Body: f.document, BodyDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(f.document)), PolicyID: "peer", PolicyRevision: 1, PolicyDigest: f.PolicyDigest(), FetchedAt: f.now}, nil
}
func (f *pluginPeerTransport) FetchExternalRepresentation(_ context.Context, r catalog.ExternalRepresentationFetchRequest) (catalog.ExternalRepresentationFetchResult, error) {
	if r.URL != f.rep.URL {
		return catalog.ExternalRepresentationFetchResult{}, fmt.Errorf("unexpected archive URL")
	}
	f.archiveReads++
	return catalog.ExternalRepresentationFetchResult{Status: http.StatusOK, RequestedURL: r.URL, FinalURL: r.URL, MediaType: f.rep.MediaType, Body: f.archive, ContentDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(f.archive)), PolicyID: "peer", PolicyRevision: 1, PolicyDigest: f.PolicyDigest(), RetrievedAt: f.now}, nil
}
func verifyPluginPeerAdoption(t *testing.T, media string, document any, rep catalog.Representation, archive []byte) {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	c := clock.NewFake(time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC))
	outbound := &pluginPeerTransport{document: raw, media: media, archive: archive, rep: rep, now: c.Now()}
	key := sha256.Sum256([]byte(t.Name()))
	s, err := catalog.NewService(catalog.Options{Repository: catalog.NewMemoryRepository(), Clock: c, IDs: id.Generator{}, Validator: adapter.NewRegistry(), ExternalFetcher: outbound, CursorKey: key[:], Policy: "peer"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := federation.NewManager(federation.NewMemoryStore(), outbound, c, id.Generator{}, federation.WithObservationAdopter(s))
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", MembershipID: "00000000-0000-7000-8000-000000000002", RealmID: "00000000-0000-7000-8000-000000000003", ActorType: identity.ActorHuman, Subject: "peer-admin", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleSourceAdmin, identity.RoleContributor, identity.RoleReader}})
	ns, err := s.CreateNamespace(ctx, catalog.CreateNamespaceInput{Slug: "default", DisplayName: "Default"})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := s.CreatePublisher(ctx, catalog.CreatePublisherInput{DisplayName: "Observed publisher", Domains: []string{"catalog.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := f.Inspect(ctx, "https://catalog.example.com/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	if inspection.KindCounts["plugin"] != 1 {
		t.Fatalf("kind inspection: %+v", inspection.KindCounts)
	}
	source, err := f.Connect(ctx, federation.ConnectInput{InspectionID: inspection.ID, DisplayName: "Plugin peer", Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill, catalog.KindPlugin}}, UpdateMode: federation.UpdateAutomatic, Reason: "Synthetic peer"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Sync(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	page, err := f.SearchFederatedCatalog(ctx, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Kinds: []catalog.Kind{catalog.KindPlugin}}, Limit: 10})
	if err != nil || len(page.Indexed) != 1 || outbound.archiveReads != 0 {
		t.Fatalf("indexed discovery: %+v, %v, archive reads %d", page, err, outbound.archiveReads)
	}
	excluded, err := f.SearchFederatedCatalog(ctx, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Kinds: []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill}}, Limit: 10})
	if err != nil || len(excluded.Indexed) != 1 || excluded.Indexed[0].Kind != catalog.KindAgent {
		t.Fatalf("explicit three-kind filter changed the baseline Agent: %+v %v", excluded, err)
	}
	observations, err := f.ObservationPage(ctx, federation.ObservationListRequest{SourceID: source.ID, CurrentOnly: true, Limit: 10})
	if err != nil || len(observations) != 2 {
		t.Fatalf("observations: %+v %v", observations, err)
	}
	o := observations[1]
	occurrences, _, err := f.ObservationOccurrences(ctx, o.ID, 10)
	if err != nil || len(occurrences) != 1 {
		t.Fatalf("occurrences: %+v %v", occurrences, err)
	}
	occurrence := occurrences[0]
	adopted, err := f.AdoptObservation(ctx, o.ID, federation.AdoptInput{SnapshotID: occurrence.SnapshotID, RetrievalDigest: occurrence.RetrievalDigest, EntryLocation: occurrence.EntryLocation, NamespaceID: ns.ID, PublisherID: publisher.ID, Visibility: catalog.VisibilityPrivate})
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Evidence.State != catalog.EvidenceComplete || adopted.Resource.Kind != catalog.KindPlugin || adopted.VersionRevision.Representations[0].ContentDigest != rep.ContentDigest || adopted.VersionRevision.Adoption == nil || adopted.Listing.CurrentReleaseID != "" || outbound.archiveReads != 1 {
		t.Fatalf("invalid adoption: %+v", adopted)
	}
	// A complete source withdrawal updates indexed discovery, never the local
	// draft's immutable adoption evidence or already inspected archive identity.
	var empty map[string]any
	if err = json.Unmarshal(raw, &empty); err != nil {
		t.Fatal(err)
	}
	// Keep the ARD Registry service entry: removing it changes the source's
	// approved interface, which correctly requires reinspection rather than sync.
	remaining := []any{}
	entries, ok := empty["entries"].([]any)
	if !ok {
		t.Fatal("generated entries are not an array")
	}
	for _, entry := range entries {
		value, ok := entry.(map[string]any)
		if !ok {
			t.Fatal("generated entry is not an object")
		}
		if value["type"] != rep.MediaType {
			remaining = append(remaining, entry)
		}
	}
	empty["entries"] = remaining
	outbound.document, err = json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	c.Advance(time.Minute)
	outbound.now = c.Now()
	withdrawn, err := f.Sync(ctx, source.ID)
	if err != nil || withdrawn.Withdrawn != 1 {
		t.Fatalf("withdrawal: %+v %v", withdrawn, err)
	}
	page, err = f.SearchFederatedCatalog(ctx, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Kinds: []catalog.Kind{catalog.KindPlugin}}, Limit: 10})
	if err != nil || len(page.Indexed) != 0 || outbound.archiveReads != 1 {
		t.Fatalf("withdrawn indexed Plugin retained: %+v %v", page, err)
	}
	retained, _, _, err := s.GetVersionRevision(ctx, adopted.VersionRevision.ID)
	if err != nil || retained.Adoption == nil || retained.Representations[0].ContentDigest != rep.ContentDigest {
		t.Fatalf("source withdrawal changed adopted evidence: %+v %v", retained, err)
	}
}
