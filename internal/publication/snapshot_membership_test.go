package publication

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// Membership checks must see matching document digests; otherwise a malformed
// membership fixture only tests checksum rejection and never its named guard.
func rehashSnapshotDocuments(t *testing.T, snapshot *Snapshot) {
	t.Helper()
	ardRaw, err := json.Marshal(snapshot.ARD)
	if err != nil {
		t.Fatal(err)
	}
	aiRaw, err := json.Marshal(snapshot.AICatalog)
	if err != nil {
		t.Fatal(err)
	}
	ardSum, aiSum := sha256.Sum256(ardRaw), sha256.Sum256(aiRaw)
	snapshot.ETag = `"` + hex.EncodeToString(ardSum[:]) + `"`
	snapshot.ARDContentDigest = "sha-256=:" + base64.StdEncoding.EncodeToString(ardSum[:]) + ":"
	snapshot.AICatalogETag = `"` + hex.EncodeToString(aiSum[:]) + `"`
	snapshot.AIContentDigest = "sha-256=:" + base64.StdEncoding.EncodeToString(aiSum[:]) + ":"
}

func TestRetainedSnapshotRejectsInvalidMembershipWithMatchingDigests(t *testing.T) {
	t.Parallel()
	manager, _, _ := newPublicationManager(t, []catalog.CatalogRecord{eligiblePublicationRecord()})
	valid, err := manager.BuildCandidate(publicationContext(t.Context(), "tenant-1"), "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	resourceIndex, registryIndex := -1, -1
	for index, entry := range valid.ARD.Entries {
		if entry.Metadata[catalogReleaseIDMetadata] == "registry:self" {
			registryIndex = index
		} else {
			resourceIndex = index
		}
	}
	if resourceIndex < 0 || registryIndex < 0 || len(valid.AICatalog.Entries) != 1 {
		t.Fatal("fixture needs one governed release and one Registry")
	}
	for _, tc := range []struct {
		name, message string
		mutate        func(*testing.T, *Snapshot)
	}{
		{"ARD structural validation", "not a conforming ARD manifest", func(_ *testing.T, s *Snapshot) { s.ARD.Entries[resourceIndex].Identifier = "" }},
		{"AI structural validation", "not a conforming AI Catalog", func(_ *testing.T, s *Snapshot) { s.AICatalog.SpecVersion = "wrong" }},
		{"missing AI release metadata", "AI Catalog snapshot release metadata is invalid", func(_ *testing.T, s *Snapshot) { delete(s.AICatalog.Entries[0].Extensions, aiCatalogMetadataExtension) }},
		{"empty AI release metadata", "AI Catalog snapshot release metadata is invalid", func(t *testing.T, s *Snapshot) {
			t.Helper()
			metadata, ok := s.AICatalog.Entries[0].Extensions[aiCatalogMetadataExtension].(map[string]any)
			if !ok {
				t.Fatal("fixture needs AI release metadata")
			}
			metadata[catalogReleaseIDMetadata] = ""
		}},
		{"empty ARD release metadata", "ARD snapshot release metadata is invalid", func(_ *testing.T, s *Snapshot) { s.ARD.Entries[resourceIndex].Metadata[catalogReleaseIDMetadata] = "" }},
		{"duplicate AI release", "AI Catalog snapshot contains a duplicate release", func(_ *testing.T, s *Snapshot) {
			s.AICatalog.Entries = append(s.AICatalog.Entries, cloneAICatalogEntries(s.AICatalog.Entries)...)
			s.AICatalog.Entries[1].Identifier = "urn:air:example.com:agents:agent:another-agent"
		}},
		{"missing ARD release metadata", "ARD snapshot release metadata is invalid", func(_ *testing.T, s *Snapshot) {
			delete(s.ARD.Entries[resourceIndex].Metadata, catalogReleaseIDMetadata)
		}},
		{"duplicate ARD release", "ARD snapshot contains a duplicate release", func(_ *testing.T, s *Snapshot) {
			s.ARD.Entries = append(s.ARD.Entries, cloneEntries([]Entry{s.ARD.Entries[resourceIndex]})...)
		}},
		{"unpaired ARD release", "ARD and AI Catalog snapshot membership is inconsistent", func(_ *testing.T, s *Snapshot) { s.AICatalog.Entries = []AICatalogEntry{} }},
		{"divergent representations", "ARD and AI Catalog snapshot membership is inconsistent", func(_ *testing.T, s *Snapshot) { s.AICatalog.Entries[0].DisplayName = "Different listing" }},
		{"wrong Registry URL", "ARD snapshot Registry reference is invalid", func(_ *testing.T, s *Snapshot) {
			s.ARD.Entries[registryIndex].URL = "https://other.example.com/ard/v1/"
		}},
		{"missing Registry", "publication snapshot membership metadata is inconsistent", func(_ *testing.T, s *Snapshot) { s.ARD.Entries = []Entry{s.ARD.Entries[resourceIndex]} }},
		{"malformed managed AI URL", "AI Catalog snapshot artifact reference is invalid", func(_ *testing.T, s *Snapshot) {
			s.AICatalog.Entries[0].Data = nil
			s.AICatalog.Entries[0].URL = "https://catalog.example.com/public/v1/artifacts/one/two"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := cloneSnapshot(valid)
			tc.mutate(t, &snapshot)
			rehashSnapshotDocuments(t, &snapshot)
			err := ValidateSnapshotIntegrity(snapshot)
			if catalog.ErrorCodeOf(err) != catalog.CodePublicationNotReady || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("wrong rejection: %v; want %q", err, tc.message)
			}
		})
	}
	if err := ValidateSnapshotIntegrity(valid); err != nil {
		t.Fatalf("negative fixtures mutated the valid control: %v", err)
	}
}

func TestSnapshotActivationRejectsAChangedEligibleReleaseSet(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"new eligible release", "changed listing", "changed AI-only listing", "selection overflow"} {
		t.Run(mode, func(t *testing.T) {
			record := eligiblePublicationRecord()
			if mode == "changed AI-only listing" {
				record.ListingRevision.RepresentativeQueries = nil
			}
			manager, store, _ := newPublicationManager(t, []catalog.CatalogRecord{record})
			snapshot, err := manager.BuildCandidate(publicationContext(t.Context(), "tenant-1"), "tenant-1")
			if err != nil {
				t.Fatal(err)
			}
			spy := &publicationMutationSpy{Store: store}
			manager.store = spy
			changed := record
			wantCode := catalog.CodePublicationNotReady
			switch mode {
			case "new eligible release":
				changed.Resource.CanonicalIdentifier = "urn:air:example.com:agents:agent:another-agent"
				changed.Release.ID = "release-2"
				manager.records = publicationRecordSource{records: []catalog.CatalogRecord{record, changed}}
			case "selection overflow":
				manager.records = publicationRecordSource{overflow: true}
				wantCode = catalog.CodeWorkLimitExceeded
			default:
				changed.ListingRevision.DisplayName = "New listing name"
				manager.records = publicationRecordSource{records: []catalog.CatalogRecord{changed}}
			}
			if _, err := manager.Activate(publicationContext(t.Context(), "tenant-1"), "tenant-1", snapshot.ID, "Activate reviewed candidate"); catalog.ErrorCodeOf(err) != wantCode {
				t.Fatalf("changed eligibility activated stale candidate: %v", err)
			}
			if spy.calls != 0 {
				t.Fatal("changed eligibility reached a store transition")
			}
			if _, err := store.Active(t.Context(), "tenant-1"); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
				t.Fatalf("failed activation installed a pointer: %v", err)
			}
			manager.records = publicationRecordSource{records: []catalog.CatalogRecord{record}}
			if _, err := manager.Activate(publicationContext(t.Context(), "tenant-1"), "tenant-1", snapshot.ID, "Activate reviewed candidate"); err != nil {
				t.Fatalf("unchanged eligibility failed valid activation: %v", err)
			}
		})
	}
}
