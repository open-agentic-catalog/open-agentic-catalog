package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func exportTestSummary(t *testing.T, id string) catalog.CatalogSummary {
	t.Helper()
	var record catalog.CatalogRecord
	if err := json.Unmarshal(bytes.ReplaceAll(exportTestRecord(exportTestRepresentation(`{"large":9007199254740993}`)), []byte(exportReleaseID), []byte(id)), &record); err != nil {
		t.Fatal(err)
	}
	return record.Summary()
}

func exportTestPage(t *testing.T, mode string, ids []string, next string, partial bool) []byte {
	t.Helper()
	items := []any{}
	for _, id := range ids {
		summary := exportTestSummary(t, id)
		if mode == "search" {
			local := summary.SearchLocal()
			items = append(items, catalog.CatalogSearchResult{ID: "local:" + id, Type: catalog.SearchResultLocal, Kind: summary.Resource.Kind, VersionPresent: true, Version: summary.Version.Version, Local: &local})
		} else {
			items = append(items, summary)
		}
	}
	raw, err := json.Marshal(map[string]any{"items": items, "nextCursor": next, "partial": partial, "total": len(ids), "future": true})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func exportBatchTestSession(t *testing.T, o exportBatchOptions, transport http.RoundTripper) (*remoteSession, *exportByteBudget) {
	t.Helper()
	budget := &exportByteBudget{transport: transport, remaining: o.maxTotalBytes}
	return &remoteSession{origin: "https://catalog.invalid", token: "synthetic", client: &http.Client{Transport: budget}}, budget
}

func exportTestResponse(raw []byte, status int) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw))}
}

func TestExportBatchArguments(t *testing.T) {
	env := remoteTestEnvironment("https://catalog.invalid", "synthetic")
	for _, args := range [][]string{{"batch", "--release-id", exportReleaseID, "--output-dir", "new"}, {"batch", "--selection-file", "selection.json", "--output-dir", "new"}, {"preview", "--query", "search", "--output-dir", "new", "--namespace", exportReleaseID}, {"preview", "--query", "browse", "--output-dir", "new", "--kind", "plugin", "--visibility", "tenant"}} {
		o, err := parseExportBatch(args, env)
		if err != nil || o.maxRecords != 64 || o.maxTotalBytes != 64<<20 || o.remote.token != "synthetic" {
			t.Fatalf("parse: %+v %v", o, err)
		}
	}
	o, err := parseExportBatch([]string{"preview", "--query", "search", "--output-dir", "new", "--text", "tools", "--publisher", "example.org", "--license", "Apache-2.0", "--category", "developer", "--category", "testing", "--tag", "go", "--tag", "tools"}, env)
	if err != nil || o.search.Text != "tools" || o.search.Publisher != "example.org" || o.search.License != "Apache-2.0" || !reflect.DeepEqual(o.search.Categories, []string{"developer", "testing"}) || !reflect.DeepEqual(o.search.Tags, []string{"go", "tools"}) || !reflect.DeepEqual(o.search.Origins, []catalog.SearchResultType{catalog.SearchResultLocal}) {
		t.Fatal("Search preview lost filters or local origin", o.search, err)
	}
	for _, extra := range [][]string{{"--max-records", "0"}, {"--max-records", "65"}, {"--max-total-bytes", "0"}, {"--max-total-bytes", "268435457"}, {"--timeout", "3m"}, {"--max-response-bytes", "67108865"}, {"--max-content-bytes", "0"}, {"--release-id", "../PRIVATE_SENTINEL"}, {"--selection-file", "both"}, {"--token", "PRIVATE_SENTINEL"}, {"--representation-id", "bad"}, {"extra"}} {
		_, err := parseExportBatch(append([]string{"batch", "--release-id", exportReleaseID, "--output-dir", "new"}, extra...), env)
		if err == nil || strings.Contains(err.Error(), "PRIVATE_SENTINEL") {
			t.Fatalf("unsafe acceptance: %v %v", extra, err)
		}
	}
	for _, args := range [][]string{{"preview", "--output-dir", "new"}, {"preview", "--query", "live", "--output-dir", "new"}, {"preview", "--query", "search", "--output-dir", "new", "--max-pages", "101"}, {"preview", "--query", "search", "--output-dir", "new", "--limit", "0"}, {"preview", "--query", "browse", "--output-dir", "new", "--tag", "x"}, {"preview", "--query", "browse", "--output-dir", "new", "--kind", "agent", "--kind", "skill"}, {"batch", "--output-dir", "new"}, {"batch", "--selection-file", "selection", "--output-dir", "new", "--representation-id", exportRepID}} {
		if _, err := parseExportBatch(args, env); err == nil {
			t.Fatalf("accepted: %v", args)
		}
	}
	if _, err := parseExportBatch([]string{"batch", "--help"}, env); !errors.Is(err, flag.ErrHelp) {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runExport(t.Context(), []string{"preview", "--help"}, env, &out); err != nil || out.String() != exportUsage {
		t.Fatal(err, out.String())
	}
}

func TestExportSelectionFilesAndIdentity(t *testing.T) {
	items, _, _, err := decodeExportPage(exportTestPage(t, "search", []string{exportReleaseID}, "", false), "search", 1)
	if err != nil || len(items) != 1 || items[0].Version != "1.0.0" {
		t.Fatal(items, err)
	}
	selection := exportSelection{Format: 1, Origin: "https://catalog.invalid", Complete: true, StopReason: "exhausted", Items: append(items, items...)}
	filename := filepath.Join(canonicalTempDir(t), "selection.json")
	raw := importTestJSON(t, selection)
	if err := os.WriteFile(filename, raw, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readExportSelection(t.Context(), filename, 1)
	if err != nil || !reflect.DeepEqual(got.Items, items) {
		t.Fatal(got, err)
	}
	manifest, _, err := selectTestExport(exportTestRecord(exportTestRepresentation(`{}`)), "")
	if err != nil || !items[0].matches(manifest) {
		t.Fatal("unmodified frozen selection must match", err)
	}
	for _, field := range []string{"tenant", "resource", "version", "revision", "listing", "listingRevision", "kind", "label"} {
		item := items[0]
		switch field {
		case "tenant":
			item.TenantID = exportReleaseID
		case "resource":
			item.ResourceID = exportReleaseID
		case "version":
			item.VersionID = exportReleaseID
		case "revision":
			item.VersionRevisionID = exportReleaseID
		case "listing":
			item.ListingID = exportReleaseID
		case "listingRevision":
			item.ListingRevisionID = exportReleaseID
		case "kind":
			item.Kind = catalog.KindSkill
		case "label":
			item.Version = "2"
		}
		if item.matches(manifest) {
			t.Fatalf("changed %s matched", field)
		}
	}
	for _, change := range []func(*exportSelection){func(s *exportSelection) { s.Format = 2 }, func(s *exportSelection) { s.Origin = "https://user:secret@catalog.invalid" }, func(s *exportSelection) { s.Complete = false }, func(s *exportSelection) { s.Items[0].TenantID = "" }, func(s *exportSelection) { s.Items[0].Version = "" }, func(s *exportSelection) { s.Items[0].RepresentationID = "../../bad" }} {
		invalid := selection
		invalid.Items = append([]exportSelectionItem{}, selection.Items...)
		change(&invalid)
		raw := importTestJSON(t, invalid)
		if err := os.WriteFile(filename, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readExportSelection(t.Context(), filename, 1); err == nil {
			t.Fatal("invalid selection accepted")
		}
	}
	for _, raw := range []string{`{"format":1,"format":1}`, `{"format":1,"unknown":true}`, `{"format":1,"items":null}`, strings.Repeat("x", exportSelectionLimit+1)} {
		if err := os.WriteFile(filename, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readExportSelection(t.Context(), filename, 1); err == nil {
			t.Fatal("ambiguous/oversized selection accepted")
		}
	}
	link := filepath.Join(canonicalTempDir(t), "symlink")
	if err := os.Symlink(filename, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readExportSelection(t.Context(), link, 1); err == nil {
		t.Fatal("symlink selection accepted")
	}
	other := items[0]
	other.ReleaseID = exportRepID
	if _, err := deduplicateExportSelection(append(items, other), 1); err == nil {
		t.Fatal("record bound ignored")
	}
	conflict := items[0]
	conflict.RepresentationID = exportRepID
	if _, err := deduplicateExportSelection(append(items, conflict), 2); err == nil {
		t.Fatal("conflicting same-release selections accepted")
	}
}

func TestExportSelectionCompletionAndFileLimit(t *testing.T) {
	selection := exportSelection{Format: 1, Origin: "https://catalog.invalid", Complete: false, StopReason: "server_partial", Items: []exportSelectionItem{}}
	filename := filepath.Join(canonicalTempDir(t), "selection.json")
	raw := importTestJSON(t, selection)
	// Whitespace keeps the complete input valid JSON, so rejection above the
	// boundary must come from the file limit rather than a syntax error.
	boundary := append(bytes.Clone(raw), bytes.Repeat([]byte(" "), exportSelectionLimit-len(raw))...)
	if err := os.WriteFile(filename, boundary, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readExportSelection(t.Context(), filename, 64)
	if err != nil || got.Complete || got.StopReason != "server_partial" || got.Items == nil {
		t.Fatal("valid bounded incomplete selection refused", got, err)
	}
	for _, invalid := range [][]byte{
		append(bytes.Clone(boundary), ' '),
		bytes.Replace(raw, []byte(`"complete":false,`), nil, 1),
		bytes.Replace(raw, []byte(`"complete":false`), []byte(`"complete":null`), 1),
	} {
		if bytes.Equal(invalid, raw) {
			t.Fatal("selection fault was not injected")
		}
		if err := os.WriteFile(filename, invalid, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readExportSelection(t.Context(), filename, 64); err == nil {
			t.Fatal("oversized or ambiguous completion status accepted")
		}
	}
}

func TestExportPreviewPagingAndBounds(t *testing.T) {
	for _, mode := range []string{"search", "browse"} {
		for _, reason := range []string{"exhausted", "page_limit", "record_limit", "server_partial", "repeated", "invalid", "transport"} {
			t.Run(mode+"/"+reason, func(t *testing.T) {
				o := exportBatchOptions{query: mode, search: remoteSearch{Origins: []catalog.SearchResultType{catalog.SearchResultLocal}, Limit: 1, NamespaceID: exportReleaseID, Kinds: []catalog.Kind{catalog.KindAgent}, Visibility: catalog.VisibilityTenant}, maxPages: 3, maxRecords: 3, maxTotalBytes: 1 << 20}
				if reason == "page_limit" {
					o.maxPages = 1
				}
				if reason == "record_limit" {
					o.maxRecords = 1
				}
				calls := 0
				session, _ := exportBatchTestSession(t, o, catalogctlRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					calls++
					cursor := request.URL.Query().Get("cursor")
					if mode == "search" {
						var query remoteSearch
						if json.NewDecoder(request.Body).Decode(&query) != nil || !reflect.DeepEqual(query.Origins, []catalog.SearchResultType{catalog.SearchResultLocal}) || query.Limit != 1 || query.NamespaceID != exportReleaseID {
							t.Fatal("nonlocal or changed query")
						}
						cursor = query.Cursor
					} else if request.Method != http.MethodGet || request.URL.Query().Get("namespaceId") != exportReleaseID || request.URL.Query().Get("kind") != "agent" || request.URL.Query().Get("limit") != "1" {
						t.Fatal("wrong Browse query")
					}
					if calls == 1 && cursor != "" || calls > 1 && cursor != "opaque+/=" {
						t.Fatal("cursor changed")
					}
					if reason == "transport" {
						return nil, errors.New("private transport text")
					}
					if reason == "invalid" {
						return exportTestResponse([]byte(`{"items":null}`), 200), nil
					}
					next, id := "opaque+/=", exportReleaseID
					if calls > 1 {
						next, id = "", exportRepID
					}
					if reason == "repeated" {
						next = "opaque+/="
					}
					return exportTestResponse(exportTestPage(t, mode, []string{id}, next, reason == "server_partial"), 200), nil
				}))
				selection, err := previewExport(t.Context(), session, o)
				if reason == "invalid" || reason == "repeated" || reason == "transport" {
					if err == nil {
						t.Fatal("invalid page accepted")
					}
					return
				}
				if err != nil || selection.Complete != (reason == "exhausted") || selection.StopReason != reason || len(selection.Items) < 1 {
					t.Fatalf("%+v %v", selection, err)
				}
				if reason == "exhausted" && (calls != 2 || len(selection.Items) != 2) {
					t.Fatal("lost continuation")
				}
			})
		}
	}
	valid := exportTestPage(t, "search", []string{exportReleaseID}, "", false)
	for _, raw := range [][]byte{bytes.Replace(valid, []byte(`"type":"local"`), []byte(`"type":"indexed"`), 1), bytes.Replace(valid, []byte(`"versionPresent":true`), []byte(`"versionPresent":false`), 1), bytes.Replace(valid, []byte(`"version":"1.0.0"`), []byte(`"version":"different"`), 1), bytes.Replace(valid, []byte(`"partial":false`), []byte(`"Partial":false`), 1), exportTestPage(t, "search", []string{exportReleaseID, exportRepID}, "", false), exportTestPage(t, "search", nil, strings.Repeat("a", 4097), false)} {
		if _, _, _, err := decodeExportPage(raw, "search", 1); err == nil {
			t.Fatal("malformed/nonlocal page accepted", string(raw))
		}
	}
}

func TestExportPreviewPageIdentityAndRecordLimit(t *testing.T) {
	for _, mode := range []string{"search", "browse"} {
		for _, fault := range []string{"none", "record_limit", "changed_identity"} {
			t.Run(mode+"/"+fault, func(t *testing.T) {
				o := exportBatchOptions{query: mode, search: remoteSearch{Origins: []catalog.SearchResultType{catalog.SearchResultLocal}, Limit: 2}, maxPages: 3, maxRecords: 2, maxTotalBytes: 1 << 20}
				if fault == "record_limit" {
					o.maxRecords = 1
				}
				calls := 0
				session, _ := exportBatchTestSession(t, o, catalogctlRoundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					if fault == "record_limit" {
						return exportTestResponse(exportTestPage(t, mode, []string{exportReleaseID, exportRepID}, "", false), http.StatusOK), nil
					}
					if calls == 1 {
						return exportTestResponse(exportTestPage(t, mode, []string{exportReleaseID}, "next", false), http.StatusOK), nil
					}
					// The same release can recur on another page, but its frozen
					// identities must agree. A changed UUID is still syntactically valid.
					raw := exportTestPage(t, mode, []string{exportReleaseID, exportRepID}, "", false)
					if fault == "changed_identity" {
						old := []byte(`"tenantId":"00000000-0000-7000-8000-000000000010"`)
						raw = bytes.Replace(raw, old, []byte(`"tenantId":"00000000-0000-7000-8000-000000000099"`), 1)
					}
					return exportTestResponse(raw, http.StatusOK), nil
				}))
				selection, err := previewExport(t.Context(), session, o)
				if fault == "changed_identity" {
					if err == nil || calls != 2 || selection.Complete {
						t.Fatal("conflicting cross-page identity accepted", selection, err, calls)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if fault == "record_limit" {
					if selection.Complete || selection.StopReason != "record_limit" || calls != 1 || len(selection.Items) != 1 || selection.Items[0].ReleaseID != exportReleaseID {
						t.Fatal("record limit within a final page lost selected releases", selection, calls)
					}
				} else if !selection.Complete || calls != 2 || len(selection.Items) != 2 || selection.Items[0].ReleaseID != exportReleaseID || selection.Items[1].ReleaseID != exportRepID {
					t.Fatal("consistent overlapping pages changed selection order", selection, calls)
				}
			})
		}
	}
}

func TestExportBrowsePageRequiresLocalMatchingRevision(t *testing.T) {
	valid := exportTestPage(t, "browse", []string{exportReleaseID}, "", false)
	items, next, partial, err := decodeExportPage(valid, "browse", 1)
	if err != nil || len(items) != 1 || items[0].ReleaseID != exportReleaseID || next != "" || partial {
		t.Fatal("valid local Browse control failed", items, err)
	}
	for _, fault := range []string{"external", "version_revision", "listing_revision"} {
		t.Run(fault, func(t *testing.T) {
			var page map[string]json.RawMessage
			if err := json.Unmarshal(valid, &page); err != nil {
				t.Fatal(err)
			}
			summary := exportTestSummary(t, exportReleaseID)
			switch fault {
			case "external":
				summary.Source = "indexed"
			case "version_revision":
				summary.Release.VersionRevisionID = exportRepID
			case "listing_revision":
				summary.Release.ListingRevisionID = exportRepID
			}
			page["items"] = importTestJSON(t, []catalog.CatalogSummary{summary})
			if _, _, _, err := decodeExportPage(importTestJSON(t, page), "browse", 1); err == nil {
				t.Fatal("invalid Browse identity accepted", fault)
			}
		})
	}
}

func TestExportPageCursorRequiresStringWhenPresent(t *testing.T) {
	for _, mode := range []string{"search", "browse"} {
		for _, cursor := range []string{"omitted", `""`, `"opaque+/="`, "null", "false", "17", "[]", "{}"} {
			t.Run(mode+"/"+cursor, func(t *testing.T) {
				var page map[string]json.RawMessage
				if err := json.Unmarshal(exportTestPage(t, mode, []string{exportReleaseID}, "", false), &page); err != nil {
					t.Fatal(err)
				}
				if cursor == "omitted" {
					delete(page, "nextCursor")
				} else {
					page["nextCursor"] = json.RawMessage(cursor)
				}
				items, next, partial, err := decodeExportPage(importTestJSON(t, page), mode, 1)
				valid := cursor == "omitted" || cursor == `""` || cursor == `"opaque+/="`
				if (err == nil) != valid {
					t.Fatal("cursor presence/type contract violated", err)
				}
				if valid && (len(items) != 1 || items[0].ReleaseID != exportReleaseID || partial || next != "" && next != "opaque+/=") {
					t.Fatal("valid cursor changed the selected release", items, next, partial)
				}
				if cursor == `"opaque+/="` && next != "opaque+/=" {
					t.Fatal("opaque cursor was lost")
				}
			})
		}
	}
}

func TestExportBatchPartialFailureAndSharedDocuments(t *testing.T) {
	for _, fault := range []string{"none", "denied", "missing", "stale", "cancelled", "byte_limit"} {
		t.Run(fault, func(t *testing.T) {
			o := exportBatchOptions{exportOptions: exportOptions{maxContentBytes: 1 << 20}, maxTotalBytes: 1 << 20}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			items, _, _, err := decodeExportPage(exportTestPage(t, "search", []string{exportReleaseID, exportRepID, "00000000-0000-7000-8000-000000000099"}, "", false), "search", 3)
			if err != nil {
				t.Fatal(err)
			}
			if fault == "stale" {
				items[1].Version = "2.0"
			}
			calls := 0
			session, budget := exportBatchTestSession(t, o, catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if !strings.HasPrefix(r.URL.Path, "/api/v1/catalog/resources/") {
					t.Fatal("unexpected artifact/query")
				}
				id := strings.TrimPrefix(r.URL.Path, "/api/v1/catalog/resources/")
				if calls == 2 {
					switch fault {
					case "denied":
						return exportTestResponse([]byte(`{"code":"FORBIDDEN"}`), 403), nil
					case "missing":
						return exportTestResponse([]byte(`{"code":"NOT_FOUND"}`), 404), nil
					case "cancelled":
						cancel()
					case "byte_limit":
						budgetResponse := strings.Repeat("x", int(o.maxTotalBytes))
						return exportTestResponse([]byte(budgetResponse), 200), nil
					}
				}
				return exportTestResponse(bytes.ReplaceAll(exportTestRecord(exportTestRepresentation(`{"large":9007199254740993,"decimal":1.200}`)), []byte(exportReleaseID), []byte(id)), 200), nil
			}))
			parent, name, err := openExportParent(filepath.Join(canonicalTempDir(t), "batch"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = parent.Close() }()
			root, err := createExportDirectory(parent, name)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			r := executeExportBatch(ctx, session, o, root, exportSelection{Complete: true, Items: items}, budget)
			if r.Complete != (fault == "none") || len(r.Items) != 3 || r.Items[0].Outcome != "complete" {
				t.Fatal(r)
			}
			if fault == "denied" && r.Items[1].Outcome != "denied" || fault == "missing" && r.Items[1].Code != "not_found" || fault == "stale" && r.Items[1].Code != "selection_changed" {
				t.Fatal(r)
			}
			if fault == "cancelled" || fault == "byte_limit" {
				if r.Items[2].Outcome != "not_attempted" || calls != 2 {
					t.Fatal(r, calls)
				}
			} else if calls != 3 {
				t.Fatal("must continue independent failures", calls)
			}
			for i, item := range r.Items {
				raw, err := root.ReadFile(items[i].ReleaseID + "/native.json")
				if item.Outcome == "complete" {
					if err != nil || string(raw) != `{"large":9007199254740993,"decimal":1.200}` {
						t.Fatal("shared native bytes/release directory lost", err)
					}
				} else if !os.IsNotExist(err) {
					t.Fatal("failed item published content", err)
				}
			}
		})
	}
}

func TestExportBatchSelectsEachRepresentation(t *testing.T) {
	const secondRepresentation = "00000000-0000-7000-8000-000000000099"
	const firstContent = `{"selected":"first"}`
	const secondContent = `{"selected":"second"}`
	second := strings.ReplaceAll(exportTestRepresentation(secondContent), exportRepID, secondRepresentation)
	record := exportTestRecord(exportTestRepresentation(firstContent) + "," + second)
	items := []exportSelectionItem{{ReleaseID: exportReleaseID, RepresentationID: secondRepresentation}, {ReleaseID: exportRepID, RepresentationID: exportRepID}}
	o := exportBatchOptions{exportOptions: exportOptions{representationID: exportRepID, maxContentBytes: 1024}, maxTotalBytes: 1 << 20}
	calls := 0
	session, budget := exportBatchTestSession(t, o, catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if calls >= len(items) || r.URL.Path != "/api/v1/catalog/resources/"+items[calls].ReleaseID {
			t.Fatal("unexpected release or artifact read")
		}
		id := items[calls].ReleaseID
		calls++
		return exportTestResponse(bytes.ReplaceAll(record, []byte(exportReleaseID), []byte(id)), http.StatusOK), nil
	}))
	root, err := os.OpenRoot(canonicalTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	report := executeExportBatch(t.Context(), session, o, root, exportSelection{Complete: true, Items: items}, budget)
	if !report.Complete || len(report.Items) != len(items) || calls != len(items) {
		t.Fatal("explicit representation batch incomplete", report, calls)
	}
	for index, want := range []string{secondContent, firstContent} {
		raw, err := root.ReadFile(items[index].ReleaseID + "/native.json")
		if err != nil || string(raw) != want {
			t.Fatal("wrong selected representation bytes", index, string(raw), err)
		}
		raw, err = root.ReadFile(items[index].ReleaseID + "/export.json")
		var sidecar exportManifest
		if err != nil || json.Unmarshal(raw, &sidecar) != nil || sidecar.RepresentationID != items[index].RepresentationID {
			t.Fatal("wrong selected representation identity", index, err)
		}
	}
}

func TestExportAggregateResponseBudget(t *testing.T) {
	for _, limit := range []int64{2, 3, 4} {
		body := &catalogctlTrackedBody{Reader: strings.NewReader("abc")}
		calls := 0
		b := &exportByteBudget{remaining: limit, transport: catalogctlRoundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return &http.Response{Body: body}, nil })}
		response, err := b.RoundTrip(&http.Request{})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if (err == nil) != (limit >= 3) || len(raw) != 3 || body.closes != 1 || b.remaining != limit-3 {
			t.Fatal(limit, len(raw), err, body.closes, b.remaining)
		}
		if b.remaining <= 0 {
			response, err := b.RoundTrip(&http.Request{})
			if response != nil {
				_ = response.Body.Close()
			}
			if err == nil || calls != 1 {
				t.Fatal("exhausted budget sent another request")
			}
		}
	}
}

func TestExportFrozenSelectionRefusesArtifactBeforeDownload(t *testing.T) {
	representation, _ := exportTestArtifact([]byte("retained document"))
	record := exportTestRecord(string(importTestJSON(t, representation)))
	items, _, _, err := decodeExportPage(exportTestPage(t, "search", []string{exportReleaseID}, "", false), "search", 1)
	if err != nil {
		t.Fatal(err)
	}
	items[0].Version = "changed"
	calls := 0
	o := exportBatchOptions{maxTotalBytes: 1 << 20}
	session, _ := exportBatchTestSession(t, o, catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/api/v1/catalog/resources/"+exportReleaseID {
			t.Fatal("changed selection requested artifact")
		}
		return exportTestResponse(record, 200), nil
	}))
	_, _, err = prepareExport(t.Context(), session, exportOptions{releaseID: exportReleaseID, selection: &items[0], maxContentBytes: 1024, remote: remoteOptions{path: "/api/v1/catalog/resources/" + exportReleaseID}})
	if !errors.Is(err, errExportSelectionChanged) || calls != 1 {
		t.Fatal("changed identity was not fenced before content", err, calls)
	}
}

func TestExportBatchPreflightAndOriginBinding(t *testing.T) {
	for _, fault := range []string{"selection", "origin", "cancelled", "output", "flags"} {
		t.Run(fault, func(t *testing.T) {
			dir := canonicalTempDir(t)
			output := filepath.Join(dir, "export")
			filename := filepath.Join(dir, "selection.json")
			selection := exportSelection{Format: 1, Origin: "https://other.invalid", Complete: true, StopReason: "exhausted", Items: []exportSelectionItem{{ReleaseID: exportReleaseID}}}
			if err := os.WriteFile(filename, importTestJSON(t, selection), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"batch", "--selection-file", filename, "--output-dir", output}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch fault {
			case "selection":
				args[2] = filepath.Join(dir, "missing")
			case "origin":
			case "cancelled":
				cancel()
			case "output":
				if err := os.Mkdir(output, 0700); err != nil {
					t.Fatal(err)
				}
			case "flags":
				args = append(args, "--bad")
			}
			var out bytes.Buffer
			if err := runExport(ctx, args, remoteTestEnvironment("https://catalog.invalid", "synthetic"), &out); err == nil || out.Len() != 0 {
				t.Fatal("preflight refusal missing", err, out.String())
			}
			if fault != "output" {
				if _, err := os.Stat(output); !os.IsNotExist(err) {
					t.Fatal("preflight failure created output", err)
				}
			}
		})
	}
}

// A local query can select no releases. Exporting that frozen empty selection
// uses files only: it must never resolve or contact the configured API origin.
func TestExportEmptySelectionUsesOnlyLocalFiles(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(map[bool]string{true: "complete", false: "partial"}[complete], func(t *testing.T) {
			dir := canonicalTempDir(t)
			selection := exportSelection{Format: 1, Origin: "https://catalog.invalid", Complete: complete, StopReason: "exhausted", Items: []exportSelectionItem{}}
			if !complete {
				selection.StopReason = "server_partial"
			}
			filename := filepath.Join(dir, "selection.json")
			if err := os.WriteFile(filename, importTestJSON(t, selection), 0600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			outDir := filepath.Join(dir, "export")
			args := []string{"batch", "--selection-file", filename, "--output-dir", outDir}
			err := runExport(t.Context(), args, remoteTestEnvironment(selection.Origin, "synthetic"), &output)
			var report exportBatchReport
			if (err == nil) != complete || json.Unmarshal(output.Bytes(), &report) != nil || report.Complete != complete || report.SelectionComplete != complete || report.ResponseBytes != 0 || len(report.Items) != 0 {
				t.Fatal("empty selection outcome", err, output.String())
			}
			for _, name := range []string{"selection.json", "batch.json"} {
				info, err := os.Stat(filepath.Join(outDir, name))
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal("private marker missing", name, err)
				}
			}
			args[len(args)-1] = filepath.Join(dir, "short-stdout")
			if err := runExport(t.Context(), args, remoteTestEnvironment(selection.Origin, "synthetic"), importShortWriter{}); err == nil {
				t.Fatal("short stdout accepted")
			}
			if _, err := os.Stat(filepath.Join(dir, "short-stdout", "batch.json")); err != nil {
				t.Fatal("stdout failure removed report", err)
			}
		})
	}
}

func TestExportTerminalJSONKeepsUnicodeInert(t *testing.T) {
	value := exportSelectionItem{ReleaseID: exportReleaseID, Version: "1.0\u202euntrusted\nlabel"}
	var output bytes.Buffer
	if err := writeExportJSON(&output, value); err != nil {
		t.Fatal(err)
	}
	for _, b := range output.Bytes() {
		if b >= 127 {
			t.Fatal("untrusted Unicode reached the terminal")
		}
	}
	if !strings.Contains(output.String(), `\u202e`) || !strings.Contains(output.String(), `\n`) {
		t.Fatal("terminal controls were not escaped", output.String())
	}
	var decoded exportSelectionItem
	if json.Unmarshal(output.Bytes(), &decoded) != nil || decoded != value {
		t.Fatal("safe JSON changed the selected version")
	}
}

func TestExportReportByteLimitAndOutputFailure(t *testing.T) {
	t.Parallel()
	// Two JSON quotes and the final newline count toward the output budget.
	value := strings.Repeat("x", exportSelectionLimit-3)
	var output bytes.Buffer
	if err := writeExportJSON(&output, value); err != nil || output.Len() != exportSelectionLimit {
		t.Fatalf("exact report budget refused: %v, bytes %d", err, output.Len())
	}
	root, err := os.OpenRoot(canonicalTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err = saveExportJSON(t.Context(), root, "selection.json", value); err != nil {
		t.Fatal(err)
	}
	saved, err := root.ReadFile("selection.json")
	if err != nil || !bytes.Equal(saved, output.Bytes()) {
		t.Fatal("saved report and terminal result differ", err)
	}
	output.Reset()
	tooLarge := value + "x"
	for _, err := range []error{writeExportJSON(&output, tooLarge), saveExportJSON(t.Context(), root, "batch.json", tooLarge)} {
		if err == nil || err.Error() != "export: selection/report exceeds its 1 MiB limit" {
			t.Fatalf("oversized report admission: %v", err)
		}
	}
	if output.Len() != 0 {
		t.Fatal("oversized report wrote partial output")
	}
	if _, err := root.Lstat("batch.json"); !os.IsNotExist(err) {
		t.Fatal("oversized report published a completion file", err)
	}
	closed, err := os.CreateTemp(canonicalTempDir(t), "closed-output")
	if err != nil {
		t.Fatal(err)
	}
	if err = closed.Close(); err != nil {
		t.Fatal(err)
	}
	for _, writer := range []io.Writer{importShortWriter{}, closed} {
		if err := writeExportJSON(writer, exportSelectionItem{ReleaseID: exportReleaseID}); err == nil || err.Error() != "export: cannot write result; inspect saved files" {
			t.Fatalf("report output failure not classified: %v", err)
		}
	}
}

func TestExportSelectionPreservesVersionsOfOneResource(t *testing.T) {
	first := exportTestSummary(t, exportReleaseID)
	second := first
	second.Release.ID = exportRepID
	second.Version.ID = "00000000-0000-7000-8000-000000000013"
	second.Version.Version = "2.0.0"
	second.VersionRevision.ID = "00000000-0000-7000-8000-000000000014"
	second.Release.VersionRevisionID = second.VersionRevision.ID
	second.ListingRevision.ID = "00000000-0000-7000-8000-000000000016"
	second.Release.ListingRevisionID = second.ListingRevision.ID
	items, _, _, err := decodeExportPage(importTestJSON(t, map[string]any{"items": []catalog.CatalogSummary{first, second}, "total": 2}), "browse", 2)
	if err != nil || len(items) != 2 || items[0].ResourceID != items[1].ResourceID || items[0].Version != "1.0.0" || items[1].Version != "2.0.0" || items[0].VersionID == items[1].VersionID {
		t.Fatal("different versions were deduplicated", items, err)
	}
}
