package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/draftfile"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func draftDownloadFixture(t *testing.T) (exportRecord, draftfile.File) {
	t.Helper()
	var record exportRecord
	if err := json.Unmarshal(exportTestRecord(exportTestRepresentation(`{"large":9007199254740993}`)), &record); err != nil {
		t.Fatal(err)
	}
	record.Resource.CanonicalIdentifier = "custom:exact-agent"
	record.Revision.ContentSetDigest = "sha256:" + strings.Repeat("b", 64)
	record.Revision.Representations[0].Role = catalog.RepresentationPrimary
	record.Revision.Representations[0].ValidationState = "invalid"
	file := draftfile.File{Format: draftfile.Format, Kind: catalog.KindAgent, CanonicalIdentifier: record.Resource.CanonicalIdentifier, Version: record.Version.Version,
		Representations: []draftfile.Representation{{Role: catalog.RepresentationPrimary, MediaType: "application/json", ProfileID: "test-profile", Input: draftfile.Input{Mode: "data", Text: draftText(`{"large":9007199254740993}`)}}},
		Listing:         &draftfile.Listing{DisplayName: "Exact Agent", Description: "", Visibility: catalog.VisibilityPrivate}}
	return record, file
}

func draftText(value string) *string { return &value }

func TestExportDraftArguments(t *testing.T) {
	env := remoteTestEnvironment("https://catalog.invalid", "synthetic")
	for _, selector := range [][]string{
		{"--release-id", exportReleaseID},
		{"--version-revision-id", exportRepID},
		{"--version-revision-id", exportRepID, "--listing-revision-id", exportReleaseID},
	} {
		o, err := parseExport(append([]string{"--format", "draft", "--output-dir", "new"}, selector...), env)
		if err != nil || o.format != "draft" {
			t.Fatal("draft selector rejected", err)
		}
	}
	for _, extra := range [][]string{
		{"--format", "unknown"}, {"--version-revision-id", exportRepID}, {"--listing-revision-id", exportRepID},
		{"--representation-id", exportRepID}, {"--representation-id", ""},
	} {
		if _, err := parseExport(append([]string{"--format", "draft", "--release-id", exportReleaseID, "--output-dir", "new"}, extra...), env); err == nil {
			t.Fatal("conflicting draft options accepted", extra)
		}
	}
	for _, format := range []string{"native", "draft"} {
		_, err := parseExport([]string{"--format", format, "--version-revision-id", "PRIVATE_SENTINEL", "--output-dir", "new"}, env)
		if err == nil || strings.Contains(safeerr.Message(err), "PRIVATE_SENTINEL") {
			t.Fatal("invalid revision accepted or disclosed", err)
		}
	}
	for _, representation := range []string{exportRepID, ""} {
		if _, err := parseExportBatch([]string{"batch", "--format", "draft", "--release-id", exportReleaseID, "--output-dir", "new", "--representation-id", representation}, env); err == nil {
			t.Fatal("batch representation selector accepted")
		}
	}
	const upper = "AAAAAAAA-AAAA-7AAA-8AAA-AAAAAAAAAAAA"
	o, err := parseExport([]string{"--format", "draft", "--version-revision-id", upper, "--listing-revision-id", upper, "--output-dir", "new"}, env)
	if err != nil || o.versionRevisionID != strings.ToLower(upper) || o.listingRevisionID != strings.ToLower(upper) {
		t.Fatal("accepted UUID selectors were not canonicalized", err)
	}
}

func TestExportDraftDownloadsUseExactTargetsAndRawFiles(t *testing.T) {
	for _, release := range []bool{false, true} {
		for _, withListing := range []bool{false, true} {
			if release && !withListing {
				continue
			}
			t.Run(map[bool]string{false: "revision", true: "release"}[release]+map[bool]string{false: "-content", true: "-Listing"}[withListing], func(t *testing.T) {
				record, file := draftDownloadFixture(t)
				if !withListing {
					file.Listing = nil
				}
				payload, err := draftfile.Encode(file)
				if err != nil {
					t.Fatal(err)
				}
				o := exportOptions{format: "draft", maxContentBytes: 8 << 20}
				o.remote.maxResponseBytes = 8 << 20
				if release {
					o.releaseID = record.Release.ID
					o.remote.path = "/api/v1/catalog/resources/" + record.Release.ID
				} else {
					o.versionRevisionID = record.Revision.ID
					if withListing {
						o.listingRevisionID = record.ListingRevision.ID
					}
				}
				var paths []string
				session := &remoteSession{origin: "https://catalog.invalid", token: "synthetic", client: &http.Client{Transport: catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.Method != http.MethodGet || r.URL.Host != "catalog.invalid" || r.Header.Get("Authorization") != "Bearer synthetic" {
						t.Fatal("draft export lost its origin, credential or read-only boundary")
					}
					paths = append(paths, r.URL.RequestURI())
					var value any
					switch r.URL.Path {
					case "/api/v1/catalog/resources/" + record.Release.ID:
						value = record
					case "/api/v1/version-revisions/" + record.Revision.ID:
						value = map[string]any{"revision": record.Revision}
					case "/api/v1/resource-versions/" + record.Version.ID:
						value = record.Version
					case "/api/v1/resources/" + record.Resource.ID:
						value = record.Resource
					case "/api/v1/listing-revisions/" + record.ListingRevision.ID:
						value = record.ListingRevision
					case "/api/v1/catalog-listings/" + record.Listing.ID:
						value = record.Listing
					case "/api/v1/version-revisions/" + record.Revision.ID + "/export":
						if r.URL.Query().Get("listingRevisionId") != o.listingRevisionID && !release || release && r.URL.Query().Get("listingRevisionId") != record.ListingRevision.ID {
							t.Fatal("wrong optional Listing selected")
						}
						return exportTestResponse(payload, 200), nil
					default:
						t.Fatal("unexpected lookup or external contact", r.URL.Path)
					}
					return exportTestResponse(importTestJSON(t, value), 200), nil
				})}}
				manifest, raw, err := prepareExport(t.Context(), session, o)
				if err != nil || !bytes.Equal(raw, payload) || manifest.Disposition != "reusable_draft" || manifest.File.Name != "draft.json" || manifest.BrowserCompatible == nil || !*manifest.BrowserCompatible || manifest.ContentSetDigest != record.Revision.ContentSetDigest {
					t.Fatal("whole draft download changed input or omitted compatibility", manifest, err)
				}
				if release && len(paths) != 2 || !release && len(paths) != 4+map[bool]int{false: 0, true: 2}[withListing] {
					t.Fatal("unbounded or missing exact reads", paths)
				}
				parent, err := os.OpenRoot(canonicalTempDir(t))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = parent.Close() }()
				if err := writeExport(t.Context(), parent, "draft", manifest, raw); err != nil {
					t.Fatal(err)
				}
				actual, err := parent.ReadFile("draft/draft.json")
				if err != nil || !bytes.Equal(actual, payload) || !bytes.Contains(actual, []byte("9007199254740993")) {
					t.Fatal("saved draft did not preserve the raw attachment")
				}
				if err := writeExport(t.Context(), parent, "draft", manifest, raw); err == nil {
					t.Fatal("completed draft directory was overwritten")
				}
			})
		}
	}
}

func TestExportDraftRejectsSelectionFileRepresentationBeforeReads(t *testing.T) {
	filename := filepath.Join(canonicalTempDir(t), "selection.json")
	selection := exportSelection{Format: 1, Origin: "https://catalog.invalid", Complete: true, StopReason: "exhausted", Items: []exportSelectionItem{{ReleaseID: exportReleaseID, RepresentationID: exportRepID}}}
	if err := os.WriteFile(filename, importTestJSON(t, selection), 0600); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(canonicalTempDir(t), "new")
	var output bytes.Buffer
	err := runExportBatch(t.Context(), []string{"batch", "--format", "draft", "--selection-file", filename, "--output-dir", directory}, remoteTestEnvironment("https://never-contact.invalid", ""), &output)
	if err == nil || !strings.Contains(safeerr.Message(err), "representation selectors") || output.Len() != 0 {
		t.Fatal("selection conflict did not fail before credential loading", err)
	}
	if _, err := os.Lstat(directory); !os.IsNotExist(err) {
		t.Fatal("rejected selection created output")
	}
}

func TestExportDraftBatchPreservesPartialResults(t *testing.T) {
	for _, fault := range []string{"none", "denied", "stale", "cancelled", "byte limit"} {
		t.Run(fault, func(t *testing.T) {
			record, file := draftDownloadFixture(t)
			payload, err := draftfile.Encode(file)
			if err != nil {
				t.Fatal(err)
			}
			o := exportBatchOptions{exportOptions: exportOptions{format: "draft", maxContentBytes: 1 << 20, remote: remoteOptions{maxResponseBytes: 1 << 20}}, maxTotalBytes: 1 << 20}
			items := []exportSelectionItem{{ReleaseID: exportReleaseID}, {ReleaseID: exportRepID}, {ReleaseID: "00000000-0000-7000-8000-000000000099"}}
			if fault == "stale" {
				items[1].ResourceID = exportRepID
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			session, budget := exportBatchTestSession(t, o, catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 4 {
					switch fault {
					case "denied":
						return exportTestResponse([]byte(`{"code":"FORBIDDEN"}`), 403), nil
					case "cancelled":
						cancel()
					case "byte limit":
						return exportTestResponse([]byte(strings.Repeat("x", int(o.maxTotalBytes))), 200), nil
					}
				}
				if strings.HasPrefix(r.URL.Path, "/api/v1/catalog/resources/") {
					selected := record
					selected.Release.ID = strings.TrimPrefix(r.URL.Path, "/api/v1/catalog/resources/")
					return exportTestResponse(importTestJSON(t, selected), 200), nil
				}
				if r.URL.Path != "/api/v1/version-revisions/"+record.Revision.ID+"/export" || r.URL.Query().Get("listingRevisionId") != record.ListingRevision.ID {
					t.Fatal("draft batch changed the exact read or fetched external content")
				}
				return exportTestResponse(payload, 200), nil
			}))
			root, err := os.OpenRoot(canonicalTempDir(t))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			report := executeExportBatch(ctx, session, o, root, exportSelection{Complete: true, Items: items}, budget)
			if report.Complete != (fault == "none") || len(report.Items) != 3 || report.Items[0].Outcome != "complete" || report.Items[0].BrowserCompatible == nil || !*report.Items[0].BrowserCompatible {
				t.Fatal("draft batch lost a successful result", report)
			}
			if fault == "denied" && report.Items[1].Outcome != "denied" || fault == "stale" && report.Items[1].Code != "selection_changed" {
				t.Fatal("draft batch misclassified an independent failure", report)
			}
			if fault == "cancelled" || fault == "byte limit" {
				if report.Items[2].Outcome != "not_attempted" || calls != 4 {
					t.Fatal("draft batch read beyond its budget", report, calls)
				}
			} else if report.Items[2].Outcome != "complete" {
				t.Fatal("draft batch stopped after an independent failure", report)
			}
			for _, item := range report.Items {
				raw, err := root.ReadFile(item.Selection.ReleaseID + "/draft.json")
				_, markerErr := root.Stat(item.Selection.ReleaseID + "/export.json")
				if item.Outcome == "complete" {
					if err != nil || markerErr != nil || !bytes.Equal(raw, payload) {
						t.Fatal("completed draft was lost or changed", err, markerErr)
					}
				} else if !os.IsNotExist(err) || !os.IsNotExist(markerErr) {
					t.Fatal("failed draft published content or a completion marker", err, markerErr)
				}
			}
		})
	}
}

func TestExportDraftResponseAndByteRefusals(t *testing.T) {
	for _, failure := range []string{"denied", "wrong kind", "wrong identifier", "missing Listing", "wrong role", "wrong profile", "extra representation", "native content limit", "response limit", "browser limit", "cancelled", "missing content-set digest", "artifact checksum", "frozen selection"} {
		t.Run(failure, func(t *testing.T) {
			record, file := draftDownloadFixture(t)
			o := exportOptions{format: "draft", releaseID: record.Release.ID, maxContentBytes: 8 << 20, remote: remoteOptions{path: "/api/v1/catalog/resources/" + record.Release.ID, maxResponseBytes: 8 << 20}}
			switch failure {
			case "wrong kind":
				file.Kind = catalog.KindSkill
			case "wrong identifier":
				file.CanonicalIdentifier = "custom:other"
			case "missing Listing":
				file.Listing = nil
			case "wrong role":
				file.Representations[0].Role = catalog.RepresentationMetadata
			case "wrong profile":
				file.Representations[0].ProfileID = "other"
			case "extra representation":
				file.Representations = append(file.Representations, file.Representations[0])
			case "native content limit":
				o.maxContentBytes = 1
			case "browser limit":
				file.Representations[0].Input.Text = draftText(strings.Repeat("x", draftfile.BrowserTextBytes+1))
			case "missing content-set digest":
				record.Revision.ContentSetDigest = ""
			case "artifact checksum":
				record.Revision.Representations[0].ArtifactID = strings.Repeat("a", 64)
				file.Representations[0].Input.Mode = "data"
			case "frozen selection":
				o.selection = &exportSelectionItem{ReleaseID: exportRepID}
			}
			raw, err := draftfile.Encode(file)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "response limit" {
				o.remote.maxResponseBytes = int64(len(raw) - 1)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			session := &remoteSession{origin: "https://catalog.invalid", token: "synthetic", client: &http.Client{Transport: catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/export") {
					if failure == "denied" {
						return exportTestResponse([]byte(`{"code":"FORBIDDEN"}`), 403), nil
					}
					if failure == "cancelled" {
						cancel()
						return nil, context.Canceled
					}
					return exportTestResponse(raw, 200), nil
				}
				return exportTestResponse(importTestJSON(t, record), 200), nil
			})}}
			manifest, result, err := prepareExport(ctx, session, o)
			if failure == "browser limit" {
				if err != nil || !bytes.Equal(result, raw) || manifest.BrowserCompatible == nil || *manifest.BrowserCompatible {
					t.Fatal("browser size restriction was not reported separately", err)
				}
			} else if err == nil || result != nil {
				t.Fatal("invalid response or exceeded budget produced a file", err)
			}
		})
	}
}

func TestReadDraftExportTargetBindsEveryExactResponse(t *testing.T) {
	for _, failure := range []string{"valid", "revision denied", "revision missing", "revision parent", "revision tenant", "Version ID", "Version parent", "Version tenant", "Resource ID", "Resource tenant", "Resource namespace", "ListingRevision ID", "ListingRevision parent", "ListingRevision Version", "ListingRevision tenant", "Listing ID", "Listing Resource", "Listing Namespace", "Listing tenant"} {
		t.Run(failure, func(t *testing.T) {
			record, _ := draftDownloadFixture(t)
			o := exportOptions{versionRevisionID: record.Revision.ID, listingRevisionID: record.ListingRevision.ID, remote: remoteOptions{maxResponseBytes: 8 << 20}}
			calls := 0
			failed := false
			session := &remoteSession{origin: "https://catalog.invalid", token: "synthetic", client: &http.Client{Transport: catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if failed {
					t.Fatal("a failed exact lookup was followed by another request")
				}
				calls++
				var value any
				switch calls {
				case 1:
					if r.URL.Path != "/api/v1/version-revisions/"+o.versionRevisionID {
						t.Fatal("wrong exact revision requested")
					}
					switch failure {
					case "revision denied":
						failed = true
						return exportTestResponse([]byte(`{}`), 403), nil
					case "revision missing":
						failed = true
						value = map[string]any{}
					case "revision parent":
						record.Revision.ResourceVersionID = "invalid"
					case "revision tenant":
						record.Revision.TenantID = "invalid"
					}
					if value == nil {
						value = map[string]any{"revision": record.Revision}
					}
				case 2:
					if r.URL.Path != "/api/v1/resource-versions/"+record.Revision.ResourceVersionID {
						t.Fatal("Version lookup did not use the retained revision parent")
					}
					switch failure {
					case "Version ID":
						record.Version.ID = exportRepID
					case "Version parent":
						record.Version.ResourceID = "invalid"
					case "Version tenant":
						record.Version.TenantID = exportRepID
					}
					value = record.Version
				case 3:
					if r.URL.Path != "/api/v1/resources/"+record.Version.ResourceID {
						t.Fatal("Resource lookup did not use the exact Version parent")
					}
					switch failure {
					case "Resource ID":
						record.Resource.ID = exportRepID
					case "Resource tenant":
						record.Resource.TenantID = exportRepID
					case "Resource namespace":
						record.Resource.NamespaceID = "invalid"
					}
					value = record.Resource
				case 4:
					if r.URL.Path != "/api/v1/listing-revisions/"+o.listingRevisionID {
						t.Fatal("wrong exact Listing revision requested")
					}
					switch failure {
					case "ListingRevision ID":
						record.ListingRevision.ID = exportRepID
					case "ListingRevision parent":
						record.ListingRevision.ListingID = "invalid"
					case "ListingRevision Version":
						record.ListingRevision.VersionRevisionID = exportRepID
					case "ListingRevision tenant":
						record.ListingRevision.TenantID = exportRepID
					}
					value = record.ListingRevision
				case 5:
					if r.URL.Path != "/api/v1/catalog-listings/"+record.ListingRevision.ListingID {
						t.Fatal("Listing lookup did not use the exact revision parent")
					}
					switch failure {
					case "Listing ID":
						record.Listing.ID = exportRepID
					case "Listing Resource":
						record.Listing.ResourceID = exportRepID
					case "Listing Namespace":
						record.Listing.NamespaceID = exportRepID
					case "Listing tenant":
						record.Listing.TenantID = exportRepID
					}
					value = record.Listing
				default:
					t.Fatal("unexpected later request")
				}
				failed = failure != "valid" && (calls == 1 && strings.HasPrefix(failure, "revision ") || calls == 2 && strings.HasPrefix(failure, "Version ") || calls == 3 && strings.HasPrefix(failure, "Resource ") || calls == 4 && strings.HasPrefix(failure, "ListingRevision ") || calls == 5 && strings.HasPrefix(failure, "Listing "))
				return exportTestResponse(importTestJSON(t, value), 200), nil
			})}}
			result, err := readDraftExportTarget(t.Context(), session, o)
			if failure == "valid" {
				if err != nil || calls != 5 || result.Resource.ID != record.Resource.ID {
					t.Fatal("valid exact lookup failed", err)
				}
			} else if err == nil || !failed {
				t.Fatal("invalid parent or tenant passed an exact lookup", err)
			}
		})
	}
}

func TestExportDraftJSONArtifactKeepsTextAndRejectsWrongMode(t *testing.T) {
	for _, mode := range []string{"data", "content"} {
		t.Run(mode, func(t *testing.T) {
			record, file := draftDownloadFixture(t)
			text := *file.Representations[0].Input.Text
			rep := &record.Revision.Representations[0]
			rep.Data = nil
			rep.ContentDigest, rep.Size = exportChecksum([]byte(text)), int64(len(text))
			rep.ArtifactID = strings.TrimPrefix(rep.ContentDigest, "sha256:")
			file.Representations[0].Input.Mode = mode
			payload, err := draftfile.Encode(file)
			if err != nil {
				t.Fatal(err)
			}
			session := &remoteSession{origin: "https://catalog.invalid", token: "synthetic", client: &http.Client{Transport: catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/export") {
					return exportTestResponse(payload, 200), nil
				}
				return exportTestResponse(importTestJSON(t, record), 200), nil
			})}}
			o := exportOptions{format: "draft", releaseID: record.Release.ID, maxContentBytes: 8 << 20, remote: remoteOptions{path: "/api/v1/catalog/resources/" + record.Release.ID, maxResponseBytes: 8 << 20}}
			_, raw, err := prepareExport(t.Context(), session, o)
			if mode == "data" {
				if err != nil || !bytes.Equal(raw, payload) {
					t.Fatal("JSON artifact text was not exported losslessly", err)
				}
			} else if err == nil || raw != nil {
				t.Fatal("JSON artifact was exported in a non-importable mode")
			}
		})
	}
}
