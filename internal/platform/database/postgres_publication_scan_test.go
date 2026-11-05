// Publication row tests isolate retained-document decoding from SQL and lifecycle writes.
package database

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

// publicationScanFixture contains a conforming empty catalog with its Registry
// self-entry, so each corruption case starts from independently valid evidence.
func publicationScanFixture(t *testing.T) publication.Snapshot {
	t.Helper()
	s := publication.Snapshot{
		ID: "00000000-0000-7000-8000-000000000020", TenantID: restoreTestTenant,
		Host: "catalog.example.com", Audience: publication.AudienceAnonymous,
		Generation: 7, ARDProfile: "ard-test", AICatalogProfile: "ai-catalog-test",
		State:     publication.SnapshotCandidate,
		CreatedAt: time.Date(2026, 9, 19, 13, 0, 0, 123456789, time.UTC),
		ARD: publication.ARDManifest{Entries: []publication.Entry{{
			Context: publication.ARDContext, Identifier: "urn:air:catalog.example.com:registry:catalog",
			DisplayName: "Catalog Registry", Type: "application/ai-registry+json",
			URL: "https://catalog.example.com/ard/v1/", RepresentativeQueries: []string{"Find agents"},
			Metadata: map[string]any{"catalogReleaseId": "registry:self"},
		}}},
		AICatalog: publication.AICatalog{SpecVersion: "1.0", Host: publication.AICatalogHost{
			DisplayName: "Catalog", Identifier: "https://catalog.example.com",
		}, Entries: []publication.AICatalogEntry{}},
		Exclusions: []publication.Exclusion{}, ArtifactIDs: []string{},
	}
	ard, err := json.Marshal(s.ARD)
	if err != nil {
		t.Fatal(err)
	}
	ai, err := json.Marshal(s.AICatalog)
	if err != nil {
		t.Fatal(err)
	}
	ardHash, aiHash := sha256.Sum256(ard), sha256.Sum256(ai)
	s.ETag, s.AICatalogETag = `"`+hex.EncodeToString(ardHash[:])+`"`, `"`+hex.EncodeToString(aiHash[:])+`"`
	s.ARDContentDigest = "sha-256=:" + base64.StdEncoding.EncodeToString(ardHash[:]) + ":"
	s.AIContentDigest = "sha-256=:" + base64.StdEncoding.EncodeToString(aiHash[:]) + ":"
	if err := publication.ValidateSnapshotIntegrity(s); err != nil {
		t.Fatalf("invalid publication control: %v", err)
	}
	return s
}

// publicationScanValues keeps row columns distinct from the immutable document.
func publicationScanValues(t *testing.T, s publication.Snapshot) []any {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return []any{s.ID, s.TenantID, s.Host, s.ARDProfile + "+" + s.AICatalogProfile, s.Generation, s.ETag, raw, s.State, s.CreatedAt, s.ActivatedAt}
}

// TestScanPublicationSnapshotPreservesDocument allows database timestamp precision
// without replacing the retained document's timestamps or other evidence.
func TestScanPublicationSnapshotPreservesDocument(t *testing.T) {
	t.Parallel()
	for _, state := range []publication.SnapshotState{publication.SnapshotCandidate, publication.SnapshotActive, publication.SnapshotRetired} {
		t.Run(string(state), func(t *testing.T) {
			s := publicationScanFixture(t)
			s.State = state
			if state != publication.SnapshotCandidate {
				activated := s.CreatedAt.Add(time.Hour)
				s.ActivatedAt = &activated
			}
			if err := publication.ValidateSnapshotIntegrity(s); err != nil {
				t.Fatal(err)
			}
			values := publicationScanValues(t, s)
			zone := time.FixedZone("database", 2*60*60)
			values[8] = time.Date(2026, 9, 19, 15, 0, 0, 123456000, zone)
			if s.ActivatedAt != nil {
				activated := time.Date(2026, 9, 19, 16, 0, 0, 123456000, zone)
				values[9] = &activated
			}
			got, err := scanPublicationSnapshot(scriptedValueRow(values))
			if err != nil || !reflect.DeepEqual(got, s) {
				t.Fatalf("retained document changed: got=%+v error=%v", got, err)
			}
		})
	}
}

// TestScanPublicationSnapshotRejectsRowDisagreement changes one row binding at a
// time; the valid retained document must never be returned after disagreement.
func TestScanPublicationSnapshotRejectsRowDisagreement(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		column int
		value  any
	}{
		{"identity", 0, "00000000-0000-7000-8000-000000000021"},
		{"tenant", 1, "00000000-0000-7000-8000-000000000002"},
		{"host", 2, "other.example.com"},
		{"ARD profile", 3, "other+ai-catalog-test"},
		{"AI Catalog profile", 3, "ard-test+other"},
		{"generation", 4, int64(8)},
		{"etag", 5, `"other"`},
		{"state", 7, publication.SnapshotRetired},
		{"creation time", 8, time.Date(2026, 9, 19, 13, 0, 0, 123457000, time.UTC)},
		{"missing activation", 9, (*time.Time)(nil)},
		{"activation time", 9, new(time.Date(2026, 9, 19, 14, 0, 0, 123457000, time.UTC))},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := publicationScanFixture(t)
			s.State = publication.SnapshotActive
			activated := s.CreatedAt.Add(time.Hour)
			s.ActivatedAt = &activated
			values := publicationScanValues(t, s)
			values[test.column] = test.value
			got, err := scanPublicationSnapshot(scriptedValueRow(values))
			if catalog.ErrorCodeOf(err) != catalog.CodePublicationNotReady || !strings.Contains(err.Error(), "row and immutable document") || !reflect.DeepEqual(got, publication.Snapshot{}) {
				t.Fatalf("row disagreement returned snapshot=%+v error=%v", got, err)
			}
		})
	}
	t.Run("unexpected activation", func(t *testing.T) {
		s := publicationScanFixture(t)
		values := publicationScanValues(t, s)
		values[9] = &s.CreatedAt
		got, err := scanPublicationSnapshot(scriptedValueRow(values))
		if catalog.ErrorCodeOf(err) != catalog.CodePublicationNotReady || !strings.Contains(err.Error(), "row and immutable document") || !reflect.DeepEqual(got, publication.Snapshot{}) {
			t.Fatalf("unexpected activation returned snapshot=%+v error=%v", got, err)
		}
	})
}

// TestScanPublicationSnapshotRejectsInvalidDocuments checks decoding and bound
// propagation, leaving exhaustive domain-bound and integrity tests with publication.
func TestScanPublicationSnapshotRejectsInvalidDocuments(t *testing.T) {
	for _, test := range []struct {
		name    string
		change  func(*testing.T, []any)
		code    catalog.ErrorCode
		context string
	}{
		{"invalid nested field in valid JSON", func(t *testing.T, values []any) {
			t.Helper()
			var document map[string]json.RawMessage
			encoded, ok := values[6].([]byte)
			if !ok {
				t.Fatalf("publication document has type %T", values[6])
			}
			if err := json.Unmarshal(encoded, &document); err != nil {
				t.Fatal(err)
			}
			document["ard"] = json.RawMessage(`{"entries":true}`)
			raw, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			values[6] = raw
		}, catalog.CodePublicationNotReady, "decode publication snapshot"},
		{"encoded size before decoding", func(_ *testing.T, values []any) {
			values[6] = []byte(`{"padding":"` + strings.Repeat("x", publication.MaxPublicationSnapshotBytes) + `"}`)
		}, catalog.CodeWorkLimitExceeded, "retained-byte limit"},
		{"decoded collection bound", func(t *testing.T, values []any) {
			t.Helper()
			s := publicationScanFixture(t)
			s.ArtifactIDs = make([]string, publication.MaxPublicationArtifactReferences+1)
			values[6] = publicationScanValues(t, s)[6]
		}, catalog.CodeWorkLimitExceeded, "entry limit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := publicationScanValues(t, publicationScanFixture(t))
			test.change(t, values)
			got, err := scanPublicationSnapshot(scriptedValueRow(values))
			if catalog.ErrorCodeOf(err) != test.code || !strings.Contains(err.Error(), test.context) || !reflect.DeepEqual(got, publication.Snapshot{}) {
				t.Fatalf("invalid document returned snapshot=%+v error=%v", got, err)
			}
		})
	}
}

// TestScanPublicationSnapshotScanErrors preserves driver causes and translates
// absent rows without leaking a partially scanned snapshot.
func TestScanPublicationSnapshotScanErrors(t *testing.T) {
	t.Parallel()
	failure := errors.New("synthetic scan failure")
	for _, test := range []struct {
		name string
		err  error
		code catalog.ErrorCode
	}{
		{"missing", pgx.ErrNoRows, catalog.CodeNotFound},
		{"driver", failure, catalog.CodeDependencyUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := scanPublicationSnapshot(scannerFunc(func(destinations ...any) error {
				setScanDestination(t, destinations, 0, "partial")
				return test.err
			}))
			if catalog.ErrorCodeOf(err) != test.code || !reflect.DeepEqual(got, publication.Snapshot{}) || (errors.Is(test.err, failure) && !errors.Is(err, failure)) {
				t.Fatalf("scan failure returned snapshot=%+v error=%v", got, err)
			}
		})
	}
}
