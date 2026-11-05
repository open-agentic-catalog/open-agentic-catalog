package database

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func packageWriteParams(t *testing.T) catalog.CreateVersionParams {
	t.Helper()
	params := versionWriteParams(t, false)
	params.Documents = nil
	params.VersionRevision.Representations = []catalog.Representation{{
		ID: "package", Role: catalog.RepresentationDistribution,
		MediaType: catalog.SkillPackageMediaZIP, ProfileID: "skill-package",
		ContentDigest: "sha256:" + strings.Repeat("a", 64), Size: 100, ValidationState: "valid",
		Package: &catalog.SkillPackageInspection{
			StructureState: catalog.PackageAssessmentPassed, RootDirectory: "safe-skill", ExpandedSize: 10, FileCount: 1,
			Files:       []catalog.SkillPackageFile{{Path: "safe-skill/SKILL.md", Size: 10, MediaType: "text/markdown", Digest: "sha256:" + strings.Repeat("b", 64)}},
			Assessments: catalog.NotAssessedSkillPackageAssessments(),
		},
		Handoff: &catalog.SkillHandoff{Git: &catalog.GitHandoffReference{Repository: "https://git.example.com/team/skills.git", Commit: strings.Repeat("c", 40), Directory: "skills/safe-skill"}},
	}}
	// Golden digest of the fixed representation manifest, including canonical
	// package and handoff digests. Do not reproduce the digest algorithm here.
	params.VersionRevision.ContentSetDigest = "sha256:62ef89e33a3ee9634a18b86b254c1942dd527b971a063da3b6ea92c505a584d0"
	params.Evidence.SubjectDigest = params.VersionRevision.ContentSetDigest
	if err := catalog.ValidatePackageRepresentation(params.VersionRevision.Representations[0]); err != nil {
		t.Fatal(err)
	}
	if err := catalog.ValidatePackageRevisionDigest(params.VersionRevision); err != nil {
		t.Fatal(err)
	}
	return params
}

func TestPackageRevisionInsertionBindsInventoryAndRejectsCorruption(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"valid", "invalid package", "changed inventory", "metadata failure"} {
		t.Run(mode, func(t *testing.T) {
			params := packageWriteParams(t)
			representation := &params.VersionRevision.Representations[0]
			tx := &fakeCommandTx{}
			failure := errors.New("package metadata insert failed")
			switch mode {
			case "invalid package":
				representation.Package.FileCount++
			case "changed inventory":
				representation.Package.Files[0].Digest = "sha256:" + strings.Repeat("c", 64)
			case "metadata failure":
				tx.execErrors = []error{nil, nil, nil, nil, failure}
			}
			err := insertVersionRevision(t.Context(), tx, params.VersionRevision, params.Evidence, nil)
			if mode == "invalid package" || mode == "changed inventory" {
				if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || len(tx.statements) != 0 {
					t.Fatalf("corrupt package reached storage: %v, %d writes", err, len(tx.statements))
				}
				return
			}
			if mode == "metadata failure" {
				if !errors.Is(err, failure) || len(tx.statements) != 5 {
					t.Fatalf("metadata failure was lost or continued writing: %v", err)
				}
				return
			}
			if err != nil || len(tx.statements) != 6 || !strings.Contains(tx.statements[3], "INSERT INTO representations") {
				t.Fatalf("package insert: %v, %v", err, tx.statements)
			}
			args := tx.arguments[3]
			if args[0] != representation.ID || args[1] != restoreTestTenant || args[2] != params.VersionRevision.ID || args[6] != nil || args[7] != nil || args[10] != nil {
				t.Fatal("package lost tenant/revision identity or stored transient archive bytes")
			}
			want := []any{true, catalog.PackageAssessmentPassed, "safe-skill", int64(10), 1}
			for range 5 {
				want = append(want, catalog.PackageAssessmentNotAssessed)
			}
			if !reflect.DeepEqual(args[15:25], want) {
				t.Fatalf("package metadata binding=%v", args[15:25])
			}
		})
	}
}

func TestRepresentationInsertionPreservesInlineAndRetrievedEvidence(t *testing.T) {
	t.Parallel()
	for _, external := range []bool{false, true} {
		params := versionWriteParams(t, false)
		representation := &params.VersionRevision.Representations[0]
		params.VersionRevision.Adoption = &catalog.AdoptionProvenance{ObservationID: "observation", SourceID: "source", SourceRevision: 2, SnapshotID: "snapshot", RetrievalDigest: representation.ContentDigest, EntryLocation: "/entries/0", ExactEntryDigest: representation.ContentDigest, AdoptedBy: "author", AdoptedAt: params.Evidence.CreatedAt}
		if external {
			representation.URL = "https://source.example/card.json"
			params.Evidence.ExternalRetrievals = []catalog.ExternalRetrieval{{RepresentationID: representation.ID, RequestedURL: representation.URL, FinalURL: representation.URL, Status: 200, MediaType: representation.MediaType, ContentDigest: representation.ContentDigest, Size: representation.Size, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: representation.ContentDigest, RetrievedAt: params.Evidence.CreatedAt, ExpiresAt: params.Evidence.CreatedAt.Add(catalog.ExternalRepresentationFreshness)}}
			params.Evidence.ExpiresAt = params.Evidence.ExternalRetrievals[0].ExpiresAt
		} else {
			representation.Data = json.RawMessage(params.Documents[0].Content)
			representation.ArtifactID, params.Documents = "", nil
		}
		tx := &fakeCommandTx{}
		if err := insertVersionRevision(t.Context(), tx, params.VersionRevision, params.Evidence, params.Documents); err != nil {
			t.Fatal(err)
		}
		offset := len(params.Documents)
		args := tx.arguments[offset+3]
		if external {
			if args[6] != representation.URL || args[7] != nil || args[10] != representation.ArtifactID {
				t.Fatal("retrieved representation lost URL or document binding")
			}
		} else if args[6] != nil || !reflect.DeepEqual(args[7], representation.Data) || args[10] != nil {
			t.Fatal("inline representation lost retained JSON or acquired external storage")
		}
		var adoption catalog.AdoptionProvenance
		raw, ok := tx.arguments[offset+1][7].([]byte)
		if !ok || json.Unmarshal(raw, &adoption) != nil || adoption != *params.VersionRevision.Adoption {
			t.Fatal("adoption provenance lost at serialization boundary")
		}
		var retrievals []catalog.ExternalRetrieval
		raw, ok = tx.arguments[offset][10].([]byte)
		if !ok || json.Unmarshal(raw, &retrievals) != nil || retrievals == nil || len(retrievals) != len(params.Evidence.ExternalRetrievals) {
			t.Fatal("external retrievals must be retained as a JSON array")
		}
		if external && !reflect.DeepEqual(retrievals, params.Evidence.ExternalRetrievals) {
			t.Fatal("retrieval policy or freshness evidence changed")
		}
	}
}
