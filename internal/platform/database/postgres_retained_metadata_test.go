package database

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// These fixed inspections have independently recorded golden revision digests.
// Rows deliberately omit the separately retained handoff and files.
func skillHydrationFixture(t *testing.T, withOCI bool) (catalog.VersionRevision, catalog.VersionRevision, *identityScriptedRows, *identityScriptedRows) {
	t.Helper()
	want := packageWriteParams(t).VersionRevision
	representation := &want.Representations[0]
	representation.Package.Files = append(representation.Package.Files, catalog.SkillPackageFile{Path: "safe-skill/read.txt", Size: 1, MediaType: "text/plain", Digest: "sha256:" + strings.Repeat("e", 64), Executable: true})
	representation.Package.ExpandedSize, representation.Package.FileCount = 11, 2
	want.ContentSetDigest = "sha256:8dca623e8a633cd58baf6cb3f6ba763ecad1e86397230806e240b99876d748dc"
	git := representation.Handoff.Git
	references := &identityScriptedRows{values: [][]any{{representation.ID, "git", git.Repository, git.Commit, git.Directory, ""}}}
	if withOCI {
		representation.Handoff.OCI = &catalog.OCIHandoffReference{Reference: "registry.example.com/team/skill@sha256:" + strings.Repeat("d", 64)}
		references.values = append(references.values, []any{representation.ID, "oci", "", "", "", representation.Handoff.OCI.Reference})
		// Golden for the same fixed manifest with both immutable references.
		want.ContentSetDigest = "sha256:ed0910c175145df31cf12747f57cc83a28c85ea142cedaa0753d50fb575d2870"
	}
	if err := catalog.ValidatePackageRevisionDigest(want); err != nil {
		t.Fatal(err)
	}
	inventory := &identityScriptedRows{values: make([][]any, 0, len(representation.Package.Files))}
	for _, file := range representation.Package.Files {
		inventory.values = append(inventory.values, []any{representation.ID, file.Path, file.Size, file.MediaType, file.Digest, file.Executable})
	}
	got := want
	got.Representations = append([]catalog.Representation(nil), want.Representations...)
	inspection := *representation.Package
	inspection.Files = nil
	got.Representations[0].Package = &inspection
	got.Representations[0].Handoff = nil
	return got, want, references, inventory
}

func TestSkillMetadataHydrationRetainsExactReferencesAndInventory(t *testing.T) {
	t.Parallel()
	for _, withOCI := range []bool{false, true} {
		name := "git"
		if withOCI {
			name = "git and OCI"
		}
		t.Run(name, func(t *testing.T) {
			got, want, references, inventory := skillHydrationFixture(t, withOCI)
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: references}, {rows: inventory}}}
			ids := []string{got.ID}
			if err := readSkillPackageMetadata(t.Context(), tx, got.TenantID, ids, map[string]*catalog.VersionRevision{got.ID: &got}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("retained metadata changed: got=%+v want=%+v", got, want)
			}
			expected := [][]any{{want.TenantID, ids}, {want.TenantID, ids}}
			if !reflect.DeepEqual(tx.queryArguments, expected) || !references.closed || !inventory.closed || len(tx.statements) != 0 || tx.commits != 0 || tx.rollbacks != 0 {
				t.Fatalf("hydration scope/row ownership changed: arguments=%v references closed=%t inventory closed=%t", tx.queryArguments, references.closed, inventory.closed)
			}
		})
	}
}

func TestSkillMetadataHydrationAssociatesSelectedRevisions(t *testing.T) {
	t.Parallel()
	first, wantFirst, firstReferences, firstInventory := skillHydrationFixture(t, false)
	second, wantSecond, secondReferences, secondInventory := skillHydrationFixture(t, true)
	second.ID, wantSecond.ID = "00000000-0000-7000-8000-000000000022", "00000000-0000-7000-8000-000000000022"
	second.Representations[0].ID, wantSecond.Representations[0].ID = "second-package", "second-package"
	for _, rows := range []*identityScriptedRows{secondReferences, secondInventory} {
		for _, row := range rows.values {
			row[0] = "second-package"
		}
	}
	references := &identityScriptedRows{values: append(firstReferences.values, secondReferences.values...)}
	inventory := &identityScriptedRows{values: append(firstInventory.values, secondInventory.values...)}
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: references}, {rows: inventory}}}
	ids := []string{first.ID, second.ID}
	revisions := map[string]*catalog.VersionRevision{first.ID: &first, second.ID: &second}
	if err := readSkillPackageMetadata(t.Context(), tx, first.TenantID, ids, revisions); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, wantFirst) || !reflect.DeepEqual(second, wantSecond) {
		t.Fatalf("metadata crossed revision bindings or lost rows: first=%+v second=%+v", first, second)
	}
	if !reflect.DeepEqual(tx.queryArguments, [][]any{{first.TenantID, ids}, {first.TenantID, ids}}) || !references.closed || !inventory.closed || len(tx.statements) != 0 {
		t.Fatal("multiple revision selection lost scope or row cleanup")
	}
}

func TestSkillMetadataHydrationRejectsCorruptionAndClosesRows(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"orphan reference", "reference on ordinary document", "unknown reference kind", "reference scan", "reference iteration", "orphan file", "file on ordinary document", "file scan", "file iteration", "missing file", "changed file digest", "changed revision digest"} {
		t.Run(scenario, func(t *testing.T) {
			got, _, references, inventory := skillHydrationFixture(t, false)
			failure := errors.New("retained row iteration failed")
			referenceFailure := strings.HasPrefix(scenario, "reference") || scenario == "orphan reference" || scenario == "unknown reference kind"
			switch scenario {
			case "orphan reference":
				references.values[0][0] = "another-representation"
			case "reference on ordinary document":
				got.Representations[0].Package = nil
			case "unknown reference kind":
				references.values[0][1] = "mutable-download"
			case "reference scan":
				references.values[0] = []any{"incomplete"}
			case "reference iteration":
				references.err = failure
			case "orphan file":
				inventory.values[0][0] = "another-representation"
			case "file on ordinary document":
				references.values = nil
				got.Representations[0].Package = nil
			case "file scan":
				inventory.values[0] = []any{"incomplete"}
			case "file iteration":
				inventory.err = failure
			case "missing file":
				inventory.values = nil
			case "changed file digest":
				inventory.values[0][4] = "sha256:" + strings.Repeat("e", 64)
			case "changed revision digest":
				got.ContentSetDigest = "sha256:" + strings.Repeat("f", 64)
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: references}, {rows: inventory}}}
			err := readSkillPackageMetadata(t.Context(), tx, got.TenantID, []string{got.ID}, map[string]*catalog.VersionRevision{got.ID: &got})
			if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
				t.Fatalf("corrupt retained metadata accepted: %v", err)
			}
			if (scenario == "changed file digest" || scenario == "changed revision digest") && !strings.Contains(err.Error(), "revision digest integrity") {
				t.Fatalf("digest disagreement was masked by a different guard: %v", err)
			}
			if scenario == "missing file" && !strings.Contains(err.Error(), "metadata integrity") {
				t.Fatalf("missing inventory was masked by a different guard: %v", err)
			}
			if strings.HasSuffix(scenario, "iteration") && !errors.Is(err, failure) {
				t.Fatalf("iteration cause lost: %v", err)
			}
			expectedQueries := 2
			if referenceFailure {
				expectedQueries = 1
			}
			if len(tx.queryStatements) != expectedQueries || !references.closed || inventory.closed != !referenceFailure || len(tx.statements) != 0 {
				t.Fatalf("failure continued or leaked rows: queries=%d refs=%t files=%t", len(tx.queryStatements), references.closed, inventory.closed)
			}
		})
	}
}

// Exact manifest/component contents and independently recorded SHA-256 identities.
const retainedPluginJSON = `{"rootDirectory":"","expandedSize":60,"files":[{"path":"mcp.json","size":44,"digest":"sha256:9148b5fc82a889f67cee721339bc2388e524c5825c3d5c6d9da30923e8faf654","executable":false},{"path":"plugin.json","size":16,"digest":"sha256:203cafab4798abf4e77759dc9ac98b958c23e15b377d6d9787d84ccc5a3a3bc2","executable":false}],"documents":[{"path":"mcp.json","content":"{\"mcpServers\":{\"local\":{\"command\":\"inert\"}}}"},{"path":"plugin.json","content":"{\"name\":\"tools\"}"}],"components":[{"kind":"mcp_server","name":"local","path":"mcp.json"}],"assessments":{"malware":"not_assessed","secrets":"not_assessed","license":"not_assessed","sbom":"not_assessed","provenance":"not_assessed"}}`

func retainedPluginFixture(t *testing.T) *catalog.PluginInspection {
	t.Helper()
	plugin := &catalog.PluginInspection{
		ExpandedSize: 60,
		Files: []catalog.PluginFile{
			{Path: "mcp.json", Size: 44, Digest: "sha256:9148b5fc82a889f67cee721339bc2388e524c5825c3d5c6d9da30923e8faf654"},
			{Path: "plugin.json", Size: 16, Digest: "sha256:203cafab4798abf4e77759dc9ac98b958c23e15b377d6d9787d84ccc5a3a3bc2"},
		},
		Documents:   []catalog.PluginDocument{{Path: "mcp.json", Content: `{"mcpServers":{"local":{"command":"inert"}}}`}, {Path: "plugin.json", Content: `{"name":"tools"}`}},
		Components:  []catalog.PluginComponent{{Kind: "mcp_server", Name: "local", Path: "mcp.json"}},
		Assessments: catalog.NotAssessedSkillPackageAssessments(),
	}
	if err := catalog.ValidatePluginInspection(plugin); err != nil {
		t.Fatal(err)
	}
	return plugin
}

func TestPluginRepresentationRetainsManifestAndComponentEvidence(t *testing.T) {
	t.Parallel()
	params := versionWriteParams(t, false)
	params.Documents = nil
	plugin := retainedPluginFixture(t)
	representation := catalog.Representation{ID: "plugin-representation", Role: catalog.RepresentationPrimary, MediaType: catalog.PluginMediaZIP, ProfileID: catalog.PluginProfile, URL: "https://packages.example.com/tools.zip", ContentDigest: "sha256:" + strings.Repeat("a", 64), Size: 100, Plugin: plugin}
	params.VersionRevision.Representations = []catalog.Representation{representation}
	// Independently encoded manifest golden; no digest computed by the writer.
	params.VersionRevision.ContentSetDigest = "sha256:7bed2d11666ff741b3ca5ab8f9030873323616fe4b41715810224519ecbdba5f"
	params.Evidence.SubjectDigest = params.VersionRevision.ContentSetDigest
	params.Evidence.ExternalRetrievals = []catalog.ExternalRetrieval{{RepresentationID: representation.ID, RequestedURL: representation.URL, FinalURL: representation.URL, Status: 200, MediaType: representation.MediaType, ContentDigest: representation.ContentDigest, Size: representation.Size, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: representation.ContentDigest, RetrievedAt: params.Evidence.CreatedAt, ExpiresAt: params.Evidence.CreatedAt.Add(catalog.ExternalRepresentationFreshness)}}
	params.Evidence.ExpiresAt = params.Evidence.ExternalRetrievals[0].ExpiresAt
	tx := &fakeCommandTx{}
	if err := insertVersionRevision(t.Context(), tx, params.VersionRevision, params.Evidence, nil); err != nil {
		t.Fatal(err)
	}
	if len(tx.arguments) != 4 {
		t.Fatalf("unexpected retained Plugin writes: %v", tx.statements)
	}
	args := tx.arguments[3]
	var retained catalog.PluginInspection
	raw, ok := args[25].([]byte)
	if !ok || json.Unmarshal(raw, &retained) != nil || !reflect.DeepEqual(&retained, plugin) {
		t.Fatalf("Plugin evidence changed: %v", args[25])
	}
	if args[0] != representation.ID || args[1] != params.VersionRevision.TenantID || args[2] != params.VersionRevision.ID || args[6] != representation.URL || args[7] != nil || args[10] != nil || args[15] != false {
		t.Fatal("Plugin archive acquired inline/blob/Skill storage or lost tenant/revision binding")
	}
}

func TestPluginRepresentationDecoderRetainsEvidenceAndRejectsInvalidJSONShape(t *testing.T) {
	t.Parallel()
	want := retainedPluginFixture(t)
	values := representationReadValues()
	values[3], values[4], values[5] = catalog.PluginMediaZIP, catalog.PluginProfile, "https://packages.example.com/tools.zip"
	values[6], values[7], values[8] = []byte(nil), "sha256:"+strings.Repeat("a", 64), int64(100)
	values[24] = []byte(retainedPluginJSON)
	var revisionID string
	got, err := scanRepresentation(scriptedValueRow(values), &revisionID)
	if err != nil || revisionID != "revision" || !reflect.DeepEqual(got.Plugin, want) {
		t.Fatalf("Plugin evidence lost: %+v %v", got.Plugin, err)
	}
	values[24] = []byte(`{"files":"invalid inventory"}`)
	got, err = scanRepresentation(scriptedValueRow(values), &revisionID)
	if catalog.ErrorCodeOf(err) != catalog.CodeInternal || !reflect.DeepEqual(got, catalog.Representation{}) {
		t.Fatalf("invalid Plugin shape disclosed partial representation: %+v %v", got, err)
	}
}
