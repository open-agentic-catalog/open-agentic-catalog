package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const (
	exportReleaseID = "00000000-0000-7000-8000-000000000001"
	exportRepID     = "00000000-0000-7000-8000-000000000007"
)

func exportTestRecord(representations string) []byte {
	return []byte(`{"source":"local",
"resource":{"id":"00000000-0000-7000-8000-000000000002","tenantId":"00000000-0000-7000-8000-000000000010","namespaceId":"00000000-0000-7000-8000-000000000011","kind":"agent"},
"version":{"id":"00000000-0000-7000-8000-000000000003","resourceId":"00000000-0000-7000-8000-000000000002","tenantId":"00000000-0000-7000-8000-000000000010","version":"1.0.0"},
"versionRevision":{"id":"00000000-0000-7000-8000-000000000004","tenantId":"00000000-0000-7000-8000-000000000010","resourceVersionId":"00000000-0000-7000-8000-000000000003","representations":[` + representations + `]},
"listing":{"id":"00000000-0000-7000-8000-000000000005","tenantId":"00000000-0000-7000-8000-000000000010","namespaceId":"00000000-0000-7000-8000-000000000011","resourceId":"00000000-0000-7000-8000-000000000002"},
"listingRevision":{"id":"00000000-0000-7000-8000-000000000006","tenantId":"00000000-0000-7000-8000-000000000010","listingId":"00000000-0000-7000-8000-000000000005","versionRevisionId":"00000000-0000-7000-8000-000000000004"},
"release":{"id":"00000000-0000-7000-8000-000000000001","tenantId":"00000000-0000-7000-8000-000000000010","listingId":"00000000-0000-7000-8000-000000000005","listingRevisionId":"00000000-0000-7000-8000-000000000006","versionRevisionId":"00000000-0000-7000-8000-000000000004"},
"futureExtension":{"large":9007199254740993}}`)
}

func exportTestRepresentation(data string) string {
	return `{"id":"` + exportRepID + `","mediaType":"application/json","profileId":"test-profile","contentDigest":"sha256:` + strings.Repeat("a", 64) + `","size":9,"validationState":"valid","data":` + data + `}`
}

// Test helper composes the production record decoder and representation selector.
func selectTestExport(raw []byte, representationID string) (exportManifest, exportRepresentation, error) {
	record, err := decodeExportRecord(raw, exportReleaseID)
	if err != nil {
		return exportManifest{}, exportRepresentation{}, err
	}
	return selectExportRecord(record, representationID)
}

func TestExportArgumentsAndSafeHelp(t *testing.T) {
	args := []string{"--release-id", exportReleaseID, "--output-dir", "new-output"}
	environment := remoteTestEnvironment("https://catalog.invalid", "PRIVATE_SENTINEL")
	options, err := parseExport(args, environment)
	if err != nil || options.remote.path != "/api/v1/catalog/resources/"+exportReleaseID || options.remote.token != "PRIVATE_SENTINEL" || options.remote.timeout != 30*time.Second || options.maxContentBytes != remoteMaxBody || options.remote.maxResponseBytes != remoteMaxBody {
		t.Fatalf("defaults: %+v %v", options, err)
	}
	options, err = parseExport(append(args, "--base-url", "https://selected.invalid", "--token-file", "token", "--ca-file", "roots", "--representation-id", exportRepID, "--timeout", "2m", "--max-response-bytes", "67108864", "--max-content-bytes", "1"), environment)
	if err != nil || options.remote.baseURL != "https://selected.invalid" || options.remote.tokenFile != "token" || options.remote.caFile != "roots" || options.representationID != exportRepID || options.maxContentBytes != 1 || options.remote.maxResponseBytes != 64<<20 {
		t.Fatalf("explicit inputs: %+v %v", options, err)
	}
	for _, extra := range [][]string{{"--token", "PRIVATE_SENTINEL"}, {"--timeout", "PRIVATE_SENTINEL"}, {"--timeout", "0"}, {"--timeout", "3m"}, {"--max-content-bytes", "0"}, {"--max-content-bytes", "67108865"}, {"--max-response-bytes", "0"}, {"--max-response-bytes", "67108865"}, {"--representation-id", "../PRIVATE_SENTINEL"}, {"--release-id", "PRIVATE_SENTINEL"}, {"extra"}, {"--overwrite"}} {
		_, err := parseExport(append(append([]string{}, args...), extra...), environment)
		if err == nil || strings.Contains(safeerr.Message(err), "PRIVATE_SENTINEL") {
			t.Fatalf("unsafe accepted arguments: %v %v", extra, err)
		}
	}
	for _, args := range [][]string{nil, {"--release-id", exportReleaseID}, {"--output-dir", "output"}} {
		if _, err := parseExport(args, environment); err == nil {
			t.Fatal("missing selection accepted")
		}
	}
	var output bytes.Buffer
	if err := runExport(t.Context(), []string{"--help"}, environment, &output); err != nil || output.String() != exportUsage || strings.Contains(output.String(), "PRIVATE_SENTINEL") {
		t.Fatalf("help: %s %v", output.String(), err)
	}
	if _, err := parseExport([]string{"-h"}, environment); !errors.Is(err, flag.ErrHelp) {
		t.Fatal(err)
	}
	if err := runExport(t.Context(), []string{"--help"}, environment, importShortWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short help output accepted", err)
	}
}

func TestExportStartupRefusalsLeaveNoOutput(t *testing.T) {
	for _, failure := range []string{"invalid flags", "invalid origin", "missing token file", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "export")
			args := []string{"--release-id", exportReleaseID, "--output-dir", directory}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			origin, want := "https://catalog.invalid", ""
			switch failure {
			case "invalid flags":
				args = append(args, "--token", "PRIVATE_SENTINEL")
				want = "invalid flags"
			case "invalid origin":
				origin, want = "https://user:PRIVATE_SENTINEL@catalog.invalid", "--base-url" // #nosec G101 -- synthetic credential proves unsafe origins are rejected without disclosure.
			case "missing token file":
				args = append(args, "--token-file", filepath.Join(t.TempDir(), "PRIVATE_SENTINEL"))
				want = "token file"
			case "cancelled":
				cancel()
				want = "cancelled or deadline exceeded"
			}
			var output bytes.Buffer
			err := runExport(ctx, args, remoteTestEnvironment(origin, "fixture-token"), &output)
			if err == nil || !strings.Contains(safeerr.Message(err), want) || strings.Contains(safeerr.Message(err), "PRIVATE_SENTINEL") || output.Len() != 0 {
				t.Fatalf("startup failure was lost or disclosed private inputs: %v stdout=%s", err, &output)
			}
			if _, err := os.Lstat(directory); !os.IsNotExist(err) {
				t.Fatal("startup refusal created output", err)
			}
		})
	}
}

func TestExportInlineRawSelectionAndDigestScopes(t *testing.T) {
	const native = `{ "large":9007199254740993, "decimal":1.2300e+25, "minusZero":-0, "extension":{"escaped":"\u0061","null":null} }`
	manifest, rep, err := selectTestExport(exportTestRecord(exportTestRepresentation(native)), "")
	if err != nil || string(rep.Data) != native || manifest.Disposition != "inline_json" || manifest.Recorded.Digest != "sha256:"+strings.Repeat("a", 64) || manifest.Recorded.Size != 9 || manifest.Recorded.Scope != "retained_document" || manifest.File.Name != "native.json" || manifest.File.Scope != "record_inline_json" || manifest.VersionRevisionID != "00000000-0000-7000-8000-000000000004" {
		t.Fatalf("raw native data or digest meaning changed: %+v %s %v", manifest, rep.Data, err)
	}
	second := strings.Replace(exportTestRepresentation(`{"second":true}`), exportRepID, "00000000-0000-7000-8000-000000000008", 1)
	raw := exportTestRecord(exportTestRepresentation(native) + "," + second)
	if _, _, err := selectTestExport(raw, ""); err == nil || !strings.Contains(safeerr.Message(err), "--representation-id") {
		t.Fatal("ambiguous representation silently chosen", err)
	}
	selected, rep, err := selectTestExport(raw, "00000000-0000-7000-8000-000000000008")
	if err != nil || selected.RepresentationID == exportRepID || string(rep.Data) != `{"second":true}` {
		t.Fatal("explicit selection lost", selected, err)
	}
	// The API's version-label bound counts Unicode code points, not bytes.
	version := strings.Repeat("\u754c", 128)
	raw = bytes.Replace(exportTestRecord(exportTestRepresentation(native)), []byte(`"version":"1.0.0"`), []byte(`"version":"`+version+`"`), 1)
	if selected, _, err := selectTestExport(raw, ""); err != nil || selected.Version != version {
		t.Fatal("valid Unicode version label rejected or changed", selected, err)
	}
}

func TestExportRejectsInvalidRecordsAndDeliveryForms(t *testing.T) {
	base := string(exportTestRecord(exportTestRepresentation(`{}`)))
	if _, _, err := selectTestExport([]byte(base), exportRepID); err != nil {
		t.Fatal("valid record control rejected", err)
	}
	for name, raw := range map[string]string{
		"missing version label":    strings.Replace(base, `,"version":"1.0.0"`, ``, 1),
		"empty version label":      strings.Replace(base, `"version":"1.0.0"`, `"version":""`, 1),
		"null version label":       strings.Replace(base, `"version":"1.0.0"`, `"version":null`, 1),
		"oversized version label":  strings.Replace(base, `"version":"1.0.0"`, `"version":"`+strings.Repeat("\u754c", 129)+`"`, 1),
		"wrong release":            strings.Replace(base, `"release":{"id":"`+exportReleaseID, `"release":{"id":"00000000-0000-7000-8000-000000000099`, 1),
		"wrong parent":             strings.Replace(base, `"resourceVersionId":"00000000-0000-7000-8000-000000000003"`, `"resourceVersionId":"00000000-0000-7000-8000-000000000099"`, 1),
		"cross tenant":             strings.Replace(base, `"tenantId":"00000000-0000-7000-8000-000000000010"`, `"tenantId":"00000000-0000-7000-8000-000000000099"`, 1),
		"not local":                strings.Replace(base, `"source":"local"`, `"source":"indexed"`, 1),
		"invalid identity":         strings.Replace(base, `"namespaceId":"00000000-0000-7000-8000-000000000011"`, `"namespaceId":"../private"`, 1),
		"wrong kind":               strings.Replace(base, `"kind":"agent"`, `"kind":"alien"`, 1),
		"null data":                strings.Replace(base, `"data":{}`, `"data":null`, 1),
		"array data":               strings.Replace(base, `"data":{}`, `"data":[]`, 1),
		"no delivery":              strings.Replace(base, `,"data":{}`, ``, 1),
		"unretained URL":           strings.Replace(base, `"data":{}`, `"url":"https://never-fetch.invalid/native.json"`, 1),
		"bad media":                strings.Replace(base, `"mediaType":"application/json"`, `"mediaType":"invalid"`, 1),
		"bad profile":              strings.Replace(base, `"profileId":"test-profile"`, `"profileId":""`, 1),
		"bad digest":               strings.Replace(base, `"sha256:`+strings.Repeat("a", 64)+`"`, `"sha256:bad"`, 1),
		"negative size":            strings.Replace(base, `"size":9`, `"size":-1`, 1),
		"null size":                strings.Replace(base, `"size":9`, `"size":null`, 1),
		"invalid representation":   strings.Replace(base, `"validationState":"valid"`, `"validationState":"invalid"`, 1),
		"mixed inline artifact":    strings.Replace(base, `"data":{}`, `"data":{},"artifactId":"`+strings.Repeat("a", 64)+`"`, 1),
		"artifact mismatch":        strings.Replace(base, `"data":{}`, `"artifactId":"`+strings.Repeat("b", 64)+`"`, 1),
		"empty package":            strings.Replace(base, `"data":{}`, `"package":{},"handoff":{}`, 1),
		"duplicate fields":         strings.Replace(base, `"data":{}`, `"data":{},"data":{"shadow":true}`, 1),
		"duplicate representation": string(exportTestRecord(exportTestRepresentation(`{}`) + "," + exportTestRepresentation(`{}`))),
		"wrong case":               strings.Replace(base, `"contentDigest"`, `"ContentDigest"`, 1),
		"trailing JSON":            base + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := selectTestExport([]byte(raw), exportRepID); err == nil {
				t.Fatal("unsafe selection accepted")
			}
		})
	}
	if _, _, err := selectTestExport([]byte(base), exportReleaseID); err == nil {
		t.Fatal("missing explicit representation accepted")
	}
}

func TestExportReferencesRetainInspectionWithoutFetching(t *testing.T) {
	plugin := clientSkillPluginRecord(t).VersionRevision.Representations[0]
	plugin.ID = exportRepID
	// A structurally inspected Skill package from an existing local unit fixture.
	dir, input := importTestInput(t)
	input.Resources[0].Kind = catalog.KindSkill
	input.Resources[0].Representations = []importRepresentation{{Role: catalog.RepresentationPrimary, MediaType: catalog.SkillPackageMediaZIP, PackageDirectory: "safe-skill", Handoff: &catalog.SkillHandoff{OCI: &catalog.OCIHandoffReference{Reference: "registry.example.com/safe-skill@sha256:" + strings.Repeat("a", 64)}}}}
	writeImportTestFile(t, dir, "safe-skill/SKILL.md", []byte("---\nname: safe-skill\ndescription: Inspect metadata.\n---\nDo not execute this example.\n"), 0600)
	plan, err := prepareImportTest(t, dir, input, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	var prepared catalog.CreateVersionInput
	if err := json.Unmarshal(plan.Bodies[1], &prepared); err != nil {
		t.Fatal(err)
	}
	validation := adapter.NewRegistry().Validate(t.Context(), catalog.KindSkill, prepared.Representations[0])
	skill := catalog.Representation{ID: exportRepID, Role: catalog.RepresentationPrimary, MediaType: catalog.SkillPackageMediaZIP, ProfileID: validation.ProfileID, ContentDigest: exportChecksum(prepared.Representations[0].PackageData), Size: int64(len(prepared.Representations[0].PackageData)), ValidationState: "valid", Package: validation.Package, Handoff: input.Resources[0].Representations[0].Handoff}
	for _, rep := range []catalog.Representation{plugin, skill} {
		kind := catalog.KindPlugin
		if rep.Package != nil {
			kind = catalog.KindSkill
		}
		rawRep, err := json.Marshal(rep)
		if err != nil {
			t.Fatal(err)
		}
		raw := bytes.Replace(exportTestRecord(string(rawRep)), []byte(`"kind":"agent"`), []byte(`"kind":"`+kind+`"`), 1)
		calls := 0
		session := &remoteSession{origin: "https://catalog.invalid", token: "fixture-token", client: &http.Client{Transport: catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.Path != "/api/v1/catalog/resources/"+exportReleaseID || r.Method != http.MethodGet {
				t.Fatal("external reference followed or mutation attempted")
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw))}, nil
		})}}
		manifest, content, err := prepareExport(t.Context(), session, exportOptions{releaseID: exportReleaseID, remote: remoteOptions{path: "/api/v1/catalog/resources/" + exportReleaseID, maxResponseBytes: remoteMaxBody}, maxContentBytes: remoteMaxBody})
		if err != nil || calls != 1 || len(content) != 0 || manifest.File != nil || manifest.Disposition != "reference_only" || manifest.Recorded.Scope != "external_archive" || manifest.URL != rep.URL || len(manifest.Plugin) == 0 && len(manifest.Package) == 0 {
			t.Fatalf("reference-only evidence changed: %+v err=%v calls=%d", manifest, err, calls)
		}
		if rep.Plugin != nil {
			var inspection catalog.PluginInspection
			if json.Unmarshal(manifest.Plugin, &inspection) != nil || inspection.Documents[0] != rep.Plugin.Documents[0] {
				t.Fatal("available exact Plugin document lost")
			}
		}
		parent, err := os.OpenRoot(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = parent.Close() }()
		if err := writeExport(t.Context(), parent, "reference", manifest, content); err != nil {
			t.Fatal(err)
		}
		root, err := parent.OpenRoot("reference")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = root.Close() }()
		entries, err := fs.ReadDir(root.FS(), ".")
		if err != nil || len(entries) != 1 || entries[0].Name() != "export.json" {
			t.Fatal("reference export manufactured content files", entries, err)
		}
		sidecar, err := root.ReadFile("export.json")
		if err != nil {
			t.Fatal(err)
		}
		var saved exportManifest
		if err := json.Unmarshal(sidecar, &saved); err != nil {
			t.Fatal(err)
		}
		expected, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := json.Marshal(saved)
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatal("reference-only sidecar changed retained inspection or handoff", string(sidecar))
		}
	}
}

func TestExportPrivateFilesNoOverwriteAndCleanup(t *testing.T) {
	manifest, rep, err := selectTestExport(exportTestRecord(exportTestRepresentation(`{ "large":9007199254740993 }`)), "")
	if err != nil {
		t.Fatal(err)
	}
	manifest.File.Digest, manifest.File.Size = exportChecksum(rep.Data), int64(len(rep.Data))
	base := t.TempDir()
	directory := filepath.Join(base, "selected")
	parent, name, err := openExportParent(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	if err := writeExport(t.Context(), parent, name, manifest, rep.Data); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{directory, filepath.Join(directory, "native.json"), filepath.Join(directory, "export.json")} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm()&0077 != 0 || info.Mode()&os.ModeSymlink != 0 {
			t.Fatal("export is not private", path, err)
		}
	}
	raw, err := parent.ReadFile(name + "/native.json")
	if err != nil || !bytes.Equal(raw, rep.Data) {
		t.Fatal("native file reformatted", err)
	}
	var sidecar exportManifest
	if raw, err := parent.ReadFile(name + "/export.json"); err != nil || json.Unmarshal(raw, &sidecar) != nil || sidecar.File.Digest != manifest.File.Digest || sidecar.File.Digest == sidecar.Recorded.Digest {
		t.Fatal("distinct digest scopes lost", err)
	}
	if root, _, err := openExportParent(directory); err == nil {
		_ = root.Close()
		t.Fatal("existing directory accepted")
	}
	if err := writeExport(t.Context(), parent, name, manifest, []byte("changed")); err == nil {
		t.Fatal("existing directory overwritten")
	}
	if err := writeExport(t.Context(), parent, "bad-checksum", manifest, []byte("changed")); err == nil {
		t.Fatal("changed bytes saved")
	}
	if _, err := os.Lstat(filepath.Join(base, "bad-checksum")); !os.IsNotExist(err) {
		t.Fatal("failed export left output", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := writeExport(ctx, parent, "cancelled", manifest, rep.Data); err == nil {
		t.Fatal("cancelled export committed")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := writeExportFile(ctx, root, "cancelled", []byte("content")); err == nil {
		t.Fatal("cancelled file published")
	}
	if err := writeExportFile(t.Context(), root, "native.json", []byte("replacement")); err == nil {
		t.Fatal("existing file overwritten")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 2 {
		t.Fatal("temporary files not cleaned", entries, err)
	}
}

func TestExportRefusesSymlinksSpecialFilesAndMissingParents(t *testing.T) {
	base := t.TempDir()
	if err := os.Symlink(base, filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "missing"), filepath.Join(base, "leaf")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(base, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"link/child", "leaf", "fifo", "fifo/child", "missing/child"} {
		if root, _, err := openExportParent(filepath.Join(base, name)); err == nil {
			_ = root.Close()
			t.Fatal("unsafe output accepted", name)
		}
	}
	// Refusal precedes client construction, including missing token-file reads.
	err := runExport(t.Context(), []string{"--release-id", exportReleaseID, "--output-dir", filepath.Join(base, "fifo"), "--token-file", "/missing/PRIVATE_SENTINEL"}, remoteTestEnvironment("https://invalid.example", ""), io.Discard)
	if err == nil || !strings.Contains(safeerr.Message(err), "output") || strings.Contains(safeerr.Message(err), "PRIVATE_SENTINEL") {
		t.Fatal("output preflight did not fail safely before credentials", err)
	}
}

func TestExportDigestSyntax(t *testing.T) {
	for _, value := range []string{"", "sha512:" + strings.Repeat("a", 64), "sha256:" + strings.Repeat("A", 64), "sha256:" + strings.Repeat("z", 64), "sha256:" + strings.Repeat("a", 63)} {
		if exportValidDigest(value) {
			t.Fatal("bad content address accepted", value)
		}
	}
	if !exportValidDigest(exportChecksum([]byte("abc"))) {
		t.Fatal("SHA-256 encoding rejected")
	}
}
