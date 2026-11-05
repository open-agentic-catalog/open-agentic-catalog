package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/draftfile"
)

func importDraftTestFile(t *testing.T, listing bool) (string, draftfile.File) {
	t.Helper()
	text := `{"name":"Private example","framework":"custom","unknown":{"integer":9007199254740993,"decimal":0.12345678901234567890123456789,"exponent":1e999}}`
	f := draftfile.File{Format: draftfile.Format, Kind: catalog.KindAgent, CanonicalIdentifier: "custom:sample-agent", Version: "release candidate", Representations: []draftfile.Representation{{Role: catalog.RepresentationPrimary, MediaType: "application/vnd.open-agentic-catalog.agent+json", ProfileID: adapter.ProfilePrivateAgent, Input: draftfile.Input{Mode: "data", Text: &text}}}}
	if listing {
		f.Listing = &draftfile.Listing{DisplayName: "Example", Description: "Example draft", Visibility: catalog.VisibilityPrivate}
	}
	return filepath.Join(t.TempDir(), "draft.json"), f
}

func writeImportDraftTest(t *testing.T, filename string, f draftfile.File) {
	t.Helper()
	raw, err := draftfile.Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filename, raw, 0600); err != nil { // #nosec G703 -- exact caller-owned temporary test file, not an application path.
		t.Fatal(err)
	}
}

func importDraftTestPlan(t *testing.T, mode string) importBatchPlan {
	t.Helper()
	filename, f := importDraftTestFile(t, mode != "create-content" && mode != "version-content")
	writeImportDraftTest(t, filename, f)
	o := importDraftOptions{NamespaceID: "00000000-0000-7000-8000-000000000010"}
	if f.Listing != nil {
		o.PublisherID = "00000000-0000-7000-8000-000000000020"
	}
	if strings.HasPrefix(mode, "version-") {
		o.ResourceID = "00000000-0000-7000-8000-000000000101"
		o.NamespaceID = "" // Resolve it from the real target, not a file placeholder.
	}
	if mode == "version-exact" {
		o.ListingID = "00000000-0000-7000-8000-000000000105"
	}
	plan, err := prepareImportInput(t.Context(), filename, 2<<20, o)
	if err != nil || !plan.Preview.LocallyValid {
		t.Fatalf("prepare draft: %v %+v", err, plan.Preview)
	}
	return plan
}

func TestImportDraftPreparationAndOfflinePreview(t *testing.T) {
	filename, f := importDraftTestFile(t, true)
	extensions := `{"integer":9007199254740993,"exponent":1e999}`
	f.Listing.Extensions = &extensions
	writeImportDraftTest(t, filename, f)
	args := []string{"preview", "--file", filename, "--namespace-id", importPlaceholderID, "--publisher-id", importPlaceholderID}
	t.Setenv("CATALOG_REMOTE_BASE_URL", "https://must-not-contact.invalid")
	t.Setenv("CATALOG_REMOTE_TOKEN_FILE", "/must-not-read")
	var output bytes.Buffer
	if err := runImport(t.Context(), args, &output); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output.Bytes(), []byte("Private example")) || bytes.Contains(output.Bytes(), []byte("9007199254740993")) {
		t.Fatal("preview leaked native content")
	}
	plan, err := prepareImportInput(t.Context(), filename, 2<<20, importDraftOptions{NamespaceID: importPlaceholderID, PublisherID: importPlaceholderID})
	if err != nil || !plan.Preview.LocallyValid || len(plan.Preview.Resources[0].Commands) != 4 {
		t.Fatal("valid draft preview failed", err)
	}
	for _, body := range []json.RawMessage{plan.Items[0].Bodies[1], plan.Items[0].Bodies[3]} {
		for _, value := range []string{"9007199254740993", "1e999"} {
			if !bytes.Contains(body, []byte(value)) {
				t.Fatal("native or extension number changed")
			}
		}
	}
	content, err := prepareImportInput(t.Context(), filename, 2<<20, importDraftOptions{NamespaceID: importPlaceholderID, ContentOnly: true})
	if err != nil || len(content.Items[0].Bodies) != 2 {
		t.Fatal("content-only import required a Publisher or retained Listing commands", err)
	}
	for _, options := range []importDraftOptions{{}, {NamespaceID: importPlaceholderID}, {NamespaceID: importPlaceholderID, PublisherID: "bad"}, {NamespaceID: importPlaceholderID, ContentOnly: true, PublisherID: importPlaceholderID}, {NamespaceID: importPlaceholderID, ListingID: importPlaceholderID, PublisherID: importPlaceholderID}} {
		if _, err := prepareImportInput(t.Context(), filename, 2<<20, options); err == nil {
			t.Fatal("invalid destination inputs accepted", options)
		}
	}
	dir, manifest := importTestInput(t)
	native := writeImportTestFile(t, dir, "native.json", importTestJSON(t, manifest), 0600)
	if _, err := prepareImportInput(t.Context(), native, 2<<20, importDraftOptions{ContentOnly: true}); err == nil {
		t.Fatal("native manifest silently accepted reusable-only flags")
	}
}

func TestReusableDraftStarterExamples(t *testing.T) {
	for _, name := range []string{"agent", "mcp-server", "skill", "plugin-archive", "skill-package"} {
		t.Run(name, func(t *testing.T) {
			filename := filepath.Join("..", "..", "examples", "native-import", "drafts", name+".json")
			plan, err := prepareImportInput(t.Context(), filename, 2<<20, importDraftOptions{NamespaceID: importPlaceholderID})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Preview.LocallyValid != (name != "skill-package") || len(plan.Items) != 1 {
				t.Fatalf("example readiness or Resource count: %+v", plan.Preview)
			}
		})
	}
}

func TestImportDraftIncompleteInputsAndPackageMappings(t *testing.T) {
	for _, mode := range []string{"data", "content", "url", "skill-package", "plugin-preparation", "extensions"} {
		t.Run(mode, func(t *testing.T) {
			filename, f := importDraftTestFile(t, mode == "extensions")
			text := ""
			input := draftfile.Input{Mode: mode, Text: &text}
			switch mode {
			case "url":
				input = draftfile.Input{Mode: mode, URL: &text}
			case "skill-package":
				f.Kind, f.Representations[0].MediaType, f.Representations[0].ProfileID = catalog.KindSkill, catalog.SkillPackageMediaZIP, adapter.ProfileAgentSkills
				input = draftfile.Input{Mode: mode, Handoff: &text}
			case "plugin-preparation":
				f.Kind, f.Representations[0].MediaType, f.Representations[0].ProfileID = catalog.KindPlugin, catalog.PluginMediaZIP, catalog.PluginProfile
				input = draftfile.Input{Mode: mode, Manifest: &text, MCP: &text, Skills: &[]string{}}
			case "extensions":
				f.Listing.Extensions = &text
				input = f.Representations[0].Input
			}
			f.Representations[0].Input = input
			writeImportDraftTest(t, filename, f)
			o := importDraftOptions{NamespaceID: importPlaceholderID}
			if f.Listing != nil {
				o.PublisherID = importPlaceholderID
			}
			plan, err := prepareImportInput(t.Context(), filename, 2<<20, o)
			if err != nil || plan.Preview.LocallyValid || len(plan.Preview.Resources[0].Findings) == 0 {
				t.Fatal("missing inputs were not reported before writes", err)
			}
			if _, err := applyImport(t.Context(), plan, remoteOptions{}, filepath.Join(t.TempDir(), "never-created", "state"), false); err == nil {
				t.Fatal("incomplete draft applied")
			}
		})
	}
	var paths importPackagePaths
	for _, invalid := range []string{"0=foo", "17=foo", "a=foo", "1=", "foo"} {
		if paths.Set(invalid) == nil {
			t.Fatal("invalid mapping accepted")
		}
	}
	if paths.Set("1=skill.zip") != nil || paths.Set("1=other.zip") == nil || paths.String() == "" {
		t.Fatal("package mapping duplicate or help invalid")
	}
	filename, f := importDraftTestFile(t, false)
	writeImportDraftTest(t, filename, f)
	for _, p := range []importPackagePaths{{1: "x"}, {2: "x"}} {
		if _, err := prepareImportInput(t.Context(), filename, 2<<20, importDraftOptions{NamespaceID: importPlaceholderID, PackageFiles: p}); err == nil {
			t.Fatal("unmatched package mapping accepted")
		}
	}
}

func TestImportDraftSeparateSkillPackageAndURLInputs(t *testing.T) {
	filename, f := importDraftTestFile(t, false)
	handoff := `{"oci":{"reference":"registry.example.com/safe-skill@sha256:` + strings.Repeat("a", 64) + `"}}`
	f.Kind = catalog.KindSkill
	f.Representations = []draftfile.Representation{{Role: catalog.RepresentationPrimary, MediaType: catalog.SkillPackageMediaZIP, ProfileID: adapter.ProfileAgentSkills, Input: draftfile.Input{Mode: "skill-package", Handoff: &handoff}}}
	writeImportDraftTest(t, filename, f)
	directory := filepath.Dir(filename)
	writeImportTestFile(t, directory, "safe-skill/SKILL.md", []byte("---\nname: safe-skill\ndescription: Inspect metadata.\n---\nDo not execute this example.\n"), 0600)
	o := importDraftOptions{NamespaceID: importPlaceholderID, PackageDirectories: importPackagePaths{1: "safe-skill"}}
	plan, err := prepareImportInput(t.Context(), filename, 2<<20, o)
	if err != nil || !plan.Preview.LocallyValid || len(plan.Items[0].Preview.Assets) != 1 {
		t.Fatal("separate package directory failed", err)
	}
	var body catalog.CreateVersionInput
	if json.Unmarshal(plan.Items[0].Bodies[1], &body) != nil || len(body.Representations[0].PackageData) == 0 || body.Representations[0].Handoff.OCI == nil {
		t.Fatal("separate package bytes or handoff lost")
	}
	archive := writeImportTestFile(t, directory, "skill.zip", body.Representations[0].PackageData, 0600)
	o.PackageFiles, o.PackageDirectories = importPackagePaths{1: filepath.Base(archive)}, nil
	filePlan, err := prepareImportInput(t.Context(), filename, 2<<20, o)
	if err != nil || !bytes.Equal(filePlan.Items[0].Bodies[1], plan.Items[0].Bodies[1]) {
		t.Fatal("package file and directory changed request", err)
	}
	writeImportTestFile(t, directory, "safe-skill/extra.txt", []byte("changed"), 0600)
	o.PackageFiles, o.PackageDirectories = nil, importPackagePaths{1: "safe-skill"}
	changed, err := prepareImportInput(t.Context(), filename, 2<<20, o)
	if err != nil || importIntent(changed) == importIntent(plan) {
		t.Fatal("changed package retained original intent", err)
	}
	for _, name := range []string{"../outside.zip", "/outside.zip", "missing.zip"} {
		o.PackageDirectories, o.PackageFiles = nil, importPackagePaths{1: name}
		if _, err := prepareImportInput(t.Context(), filename, 2<<20, o); err == nil {
			t.Fatal("unsafe or missing package accepted")
		}
	}
	url := "https://must-not-contact.invalid/card.json"
	f.Kind, f.Representations[0].MediaType, f.Representations[0].ProfileID = catalog.KindAgent, "application/a2a-agent-card+json", adapter.ProfileA2AAgentCard
	f.Representations[0].Input = draftfile.Input{Mode: "url", URL: &url}
	writeImportDraftTest(t, filename, f)
	plan, err = prepareImportInput(t.Context(), filename, 2<<20, importDraftOptions{NamespaceID: importPlaceholderID})
	if err != nil || !plan.Preview.LocallyValid || plan.Items[0].Preview.Representations[0].LocalValidation != "pending-server-retrieval" {
		t.Fatal("URL was fetched or reported as locally validated", err)
	}
	digest := importDigest([]byte("retained Plugin archive"))
	f.Kind, f.Representations[0].MediaType, f.Representations[0].ProfileID = catalog.KindPlugin, catalog.PluginMediaZIP, catalog.PluginProfile
	f.Representations[0].Input = draftfile.Input{Mode: "plugin-archive", URL: &url, ExpectedDigest: &digest}
	writeImportDraftTest(t, filename, f)
	plan, err = prepareImportInput(t.Context(), filename, 2<<20, importDraftOptions{NamespaceID: importPlaceholderID})
	if err != nil || !plan.Preview.LocallyValid || json.Unmarshal(plan.Items[0].Bodies[1], &body) != nil || body.Representations[0].ExpectedDigest == nil || *body.Representations[0].ExpectedDigest != digest {
		t.Fatal("reusable Plugin import did not forward its retained checksum to admission", err)
	}
}

// Pure controlled transport: no listeners, application runtime or database.
type importDraftUnitAPI struct {
	t                       *testing.T
	plan                    importBatchPlan
	replies                 []remoteEnvelope
	kinds                   []int
	keys                    map[string]int
	bodies                  map[string][]byte
	etags                   map[string]string
	calls, commits, lookups int
	loss, fault             string
	lossKind                int
	fired                   bool
	principal               importPrincipal
	permissions             []string
	listingETag             string
}

func newImportDraftUnitAPI(t *testing.T, plan importBatchPlan, mode string) *importDraftUnitAPI {
	t.Helper()
	f := &importDraftUnitAPI{t: t, plan: plan, replies: importTestReplies(t, plan.Items[0]), kinds: importDraftExpectedKinds(mode), keys: map[string]int{}, bodies: map[string][]byte{}, etags: map[string]string{}, lossKind: -1, principal: importTestPrincipal, permissions: []string{"resource.read", "resource.read_content", "resource.edit", "resource.read_history"}, listingETag: `"rev-1"`}
	if plan.Draft.NamespaceID == "" {
		for _, index := range []int{0, 2, 3} {
			f.replies[index].Data = bytes.ReplaceAll(f.replies[index].Data, []byte(`"namespaceId":"`+importPlaceholderID+`"`), []byte(`"namespaceId":"00000000-0000-7000-8000-000000000010"`))
		}
	}
	if plan.Draft.ResourceID == "" {
		f.permissions = append(f.permissions, "resource.create")
	}
	return f
}

// Expectations come from the command contract, independently of the planner.
func importDraftExpectedKinds(mode string) []int {
	return map[string][]int{
		"create-content": {0, 1}, "create-listing": {0, 1, 2, 3},
		"version-content": {1}, "version-empty": {1, 2, 3},
		"version-one": {1, 3}, "version-exact": {1, 3},
	}[mode]
}

func (f *importDraftUnitAPI) roundTrip(request *http.Request) (*http.Response, error) {
	t := f.t
	status, etag := 200, ""
	var raw []byte
	switch {
	case request.URL.Path == "/api/v1/me":
		raw = importTestJSON(t, map[string]any{"tenantId": f.principal.TenantID, "realmId": f.principal.RealmID, "membershipId": f.principal.MembershipID, "subject": f.principal.Subject, "scopeEnforcement": true, "scopes": []string{"catalog.read", "catalog.write"}, "permissions": f.permissions})
	case request.URL.Path == "/api/v1/capabilities":
		budget := 2 << 20
		if f.fault == "budget" {
			budget = 1
		}
		raw = importTestJSON(t, map[string]any{"resourceKinds": []string{"agent"}, "limits": map[string]any{"requestBodyBytes": budget}})
	case request.Method == http.MethodPost:
		f.calls++
		key := request.Header.Get("Idempotency-Key")
		position, known := f.keys[key]
		if !known {
			position = f.commits
			f.keys[key] = position
			f.commits++
		}
		if position >= len(f.kinds) {
			t.Fatal("unexpected extra mutation")
		}
		kind := f.kinds[position]
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if known && (!bytes.Equal(body, f.bodies[key]) || request.Header.Get("If-Match") != f.etags[key]) {
			t.Fatal("replay changed the original bytes or ETag")
		}
		f.bodies[key], f.etags[key] = body, request.Header.Get("If-Match")
		if kind == 3 && request.Header.Get("If-Match") != `"rev-1"` {
			t.Fatal("Listing revision did not retain its original ETag")
		}
		if kind == 2 && !bytes.Contains(body, []byte(`"namespaceId":"00000000-0000-7000-8000-000000000010"`)) {
			t.Fatal("Listing creation lost the resolved destination Namespace")
		}
		raw, etag, status = f.replies[kind].Data, f.replies[kind].Meta.ETag, 201
		if kind == 3 {
			f.listingETag = `"rev-2"`
		}
		if kind == 1 && f.fault == "blocked" {
			raw = bytes.Replace(raw, []byte(`"state":"complete"`), []byte(`"state":"blocked"`), 1)
			f.replies[1].Data = raw
		}
		if kind == f.lossKind && !f.fired {
			f.fired = true
			if f.loss == "rejected" {
				status, raw = 412, []byte(`{"code":"PRECONDITION_FAILED"}`)
			} else {
				return nil, errors.New("synthetic response loss")
			}
		}
	default:
		var value map[string]json.RawMessage
		switch {
		case strings.HasSuffix(request.URL.Path, "/listings"):
			f.lookups++
			if request.URL.Query().Get("limit") != "2" {
				t.Fatal("lookup was not bounded")
			}
			items := []json.RawMessage{}
			if slices.Contains(f.kinds, 3) && !slices.Contains(f.kinds, 2) {
				items = append(items, f.replies[2].Data)
			}
			page := map[string]any{"items": items}
			switch f.fault {
			case "lookup failed":
				status = 403
			case "ambiguous":
				page["items"] = []json.RawMessage{f.replies[2].Data, f.replies[2].Data}
			case "partial":
				page["nextCursor"] = "more"
			case "null items":
				page["items"] = nil
			}
			raw = importTestJSON(t, page)
		case strings.Contains(request.URL.Path, "/resource-versions/"):
			_ = json.Unmarshal(f.replies[1].Data, &value)
			raw = value["version"]
		case strings.Contains(request.URL.Path, "/version-revisions/"):
			raw = f.replies[1].Data
		case strings.Contains(request.URL.Path, "/resources/"):
			raw, etag = f.replies[0].Data, f.replies[0].Meta.ETag
			if f.plan.Draft.ResourceID != "" {
				raw = bytes.Replace(raw, []byte(`"createdBy":"test-author"`), []byte(`"createdBy":"another-author"`), 1)
			}
			if f.fault == "wrong kind" {
				raw = bytes.Replace(raw, []byte(`"kind":"agent"`), []byte(`"kind":"skill"`), 1)
			}
			if f.fault == "wrong canonical" {
				raw = bytes.Replace(raw, []byte(`custom:sample-agent`), []byte(`custom:different`), 1)
			}
		case strings.Contains(request.URL.Path, "/catalog-listings/"):
			raw, etag = f.replies[2].Data, f.listingETag
			if f.fault == "wrong Listing parent" {
				raw = bytes.Replace(raw, []byte(`"resourceId":"00000000-0000-7000-8000-000000000101"`), []byte(`"resourceId":"`+importPlaceholderID+`"`), 1)
			}
			if etag == `"rev-2"` {
				_ = json.Unmarshal(f.replies[3].Data, &value)
				raw = value["listing"]
			}
		case strings.Contains(request.URL.Path, "/listing-revisions/"):
			_ = json.Unmarshal(f.replies[3].Data, &value)
			raw = value["revision"]
		default:
			t.Fatalf("unexpected read %s", request.URL)
		}
	}
	media := "application/json"
	if status >= 400 {
		media = "application/problem+json"
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {media}, "Etag": {etag}, "X-Request-Id": {"draft-unit-request"}}, Body: io.NopCloser(bytes.NewReader(raw))}, nil
}

func (f *importDraftUnitAPI) session() *remoteSession {
	return &remoteSession{client: &http.Client{Transport: catalogctlRoundTripFunc(f.roundTrip)}, origin: "http://127.0.0.1", token: "unit-token"}
}

func TestImportDraftFinitePlansAndResponseLoss(t *testing.T) {
	for _, mode := range []string{"create-content", "create-listing", "version-content", "version-empty", "version-one", "version-exact"} {
		for _, loss := range append([]int{-1}, importDraftExpectedKinds(mode)...) {
			t.Run(mode+"/loss-"+strconv.Itoa(loss), func(t *testing.T) {
				plan := importDraftTestPlan(t, mode)
				api := newImportDraftUnitAPI(t, plan, mode)
				api.lossKind = loss
				state, err := openImportState(filepath.Join(t.TempDir(), "private", "state.json"), false)
				if err != nil {
					t.Fatal(err)
				}
				defer state.close()
				outcome, err := applyPreparedImport(t.Context(), plan, remoteOptions{}, state, false, api.session())
				if (err == nil) != (loss == -1) {
					t.Fatalf("initial result: %v %+v", err, outcome)
				}
				saved, err := decodeImportReceipt(state.previous)
				if err != nil || len(saved.Steps) != len(api.kinds) || saved.Format != 3 {
					t.Fatal("receipt contains dummy commands or invalid state", err)
				}
				before := saved
				lookupCount := api.lookups
				outcome, err = applyPreparedImport(t.Context(), plan, remoteOptions{}, state, true, api.session())
				if err != nil || outcome.Disposition != "complete" || api.commits != len(api.kinds) || len(outcome.Items[0].Steps) != len(api.kinds) || api.lookups != lookupCount {
					t.Fatalf("resume changed the plan or repeated lookup: %v %+v", err, outcome)
				}
				after, err := decodeImportReceipt(state.previous)
				if err != nil {
					t.Fatal(err)
				}
				for index := range before.Steps {
					if before.Steps[index].Key != after.Steps[index].Key || !before.Steps[index].FirstAttempt.IsZero() && !before.Steps[index].FirstAttempt.Equal(after.Steps[index].FirstAttempt) {
						t.Fatal("resume changed a key or initial attempt time")
					}
				}
				calls := api.calls
				if _, err = applyPreparedImport(t.Context(), plan, remoteOptions{}, state, true, api.session()); err != nil || api.calls != calls {
					t.Fatal("completed resume repeated mutations", err)
				}
			})
		}
	}
}

func TestImportDraftPreflightAndBoundResumeRefusals(t *testing.T) {
	for _, fault := range []string{"wrong kind", "wrong canonical", "wrong Listing parent", "lookup failed", "ambiguous", "partial", "null items", "budget", "missing read", "missing content", "missing edit", "missing history"} {
		t.Run(fault, func(t *testing.T) {
			plan := importDraftTestPlan(t, "version-one")
			api := newImportDraftUnitAPI(t, plan, "version-one")
			api.fault = fault
			for key, permission := range map[string]string{"missing read": "resource.read", "missing content": "resource.read_content", "missing edit": "resource.edit", "missing history": "resource.read_history"} {
				if fault == key {
					api.permissions = slices.DeleteFunc(api.permissions, func(s string) bool { return s == permission })
				}
			}
			state, err := openImportState(filepath.Join(t.TempDir(), "private", "state.json"), false)
			if err != nil {
				t.Fatal(err)
			}
			defer state.close()
			if _, err = applyPreparedImport(t.Context(), plan, remoteOptions{}, state, false, api.session()); err == nil || api.calls != 0 {
				t.Fatal("invalid target or permissions caused writes", err)
			}
			if strings.HasPrefix(fault, "missing ") && !strings.Contains(err.Error(), "missing destination Namespace permission:") {
				t.Fatal("permission error did not name the missing grant", err)
			}
		})
	}
	for _, fault := range []string{"file", "principal", "Namespace", "Publisher", "Listing", "stale ETag", "expired", "definite failure"} {
		t.Run(fault, func(t *testing.T) {
			plan := importDraftTestPlan(t, "version-exact")
			api := newImportDraftUnitAPI(t, plan, "version-exact")
			api.lossKind = 1
			if fault == "definite failure" {
				api.loss = "rejected"
			}
			state, err := openImportState(filepath.Join(t.TempDir(), "private", "state.json"), false)
			if err != nil {
				t.Fatal(err)
			}
			defer state.close()
			if _, err = applyPreparedImport(t.Context(), plan, remoteOptions{}, state, false, api.session()); err == nil {
				t.Fatal("loss control failed")
			}
			calls := api.calls
			changed := plan
			o := *plan.Draft
			changed.Draft = &o
			switch fault {
			case "file":
				changed.Preview.ManifestDigest = importDigest([]byte("different file"))
			case "principal":
				api.principal.Subject = "changed-author"
			case "Namespace":
				o.NamespaceID = importPlaceholderID
			case "Publisher":
				o.PublisherID = importPlaceholderID
			case "Listing":
				o.ListingID = importPlaceholderID
			case "stale ETag":
				api.listingETag = `"rev-3"`
				api.replies[2].Data = bytes.Replace(api.replies[2].Data, []byte(`"revision":1`), []byte(`"revision":3`), 1)
			case "expired":
				r, err := decodeImportReceipt(state.previous)
				if err != nil {
					t.Fatal(err)
				}
				r.Steps[0].FirstAttempt = time.Now().Add(-25 * time.Hour)
				if err = state.save(r); err != nil {
					t.Fatal(err)
				}
			}
			before := bytes.Clone(state.previous)
			if _, err = applyPreparedImport(t.Context(), changed, remoteOptions{}, state, true, api.session()); err == nil || api.calls != calls || !bytes.Equal(before, state.previous) {
				t.Fatal("resume changed captured intent, wrote or rebased", err)
			}
		})
	}
}

func TestImportDraftBlockedAcknowledgementsAndReceiptShapes(t *testing.T) {
	for _, mode := range []string{"create-content", "create-listing", "version-content", "version-exact"} {
		plan := importDraftTestPlan(t, mode)
		api := newImportDraftUnitAPI(t, plan, mode)
		api.fault = "blocked"
		state, err := openImportState(filepath.Join(t.TempDir(), "private", "state.json"), false)
		if err != nil {
			t.Fatal(err)
		}
		outcome, err := applyPreparedImport(t.Context(), plan, remoteOptions{}, state, false, api.session())
		if err == nil || outcome.Disposition != "blocked" || outcome.Items[0].Disposition != "blocked" {
			t.Fatal("blocked 201 was reported complete")
		}
		before := api.calls
		if _, err = applyPreparedImport(t.Context(), plan, remoteOptions{}, state, true, api.session()); err == nil || api.calls != before {
			t.Fatal("blocked receipt resumed writes")
		}
		state.close()
	}
	b := importDraftBinding{NamespaceID: importPlaceholderID, ResourceID: importPlaceholderID}
	r := importReceipt{Format: 3, Draft: &b, Steps: []importStep{{}}}
	if !validImportReceiptShape(r) || !reflect.DeepEqual(b.kinds(), []int{1}) {
		t.Fatal("single Version plan rejected")
	}
	for _, etag := range []string{"", `W/"rev-1"`, `"rev-0"`, `"rev-01"`, `"rev--1"`, `"rev-1`, `"rev-999999999999999999999"`} {
		if strongImportETag(etag) {
			t.Fatal("invalid strong ETag accepted")
		}
	}
	if !strongImportETag(`"rev-2"`) {
		t.Fatal("valid strong ETag rejected")
	}
	for _, mutate := range []func(*importReceipt){func(r *importReceipt) { r.Format = 4 }, func(r *importReceipt) { r.Draft = nil }, func(r *importReceipt) { r.Steps = nil }, func(r *importReceipt) { r.Draft.NamespaceID = "bad" }, func(r *importReceipt) { r.Draft.ListingETag = `"rev-1"` }} {
		copyBinding := b
		changed := r
		changed.Draft = &copyBinding
		mutate(&changed)
		if validImportReceiptShape(changed) {
			t.Fatal("invalid variable receipt shape accepted")
		}
	}
}

func TestImportReceiptFormatsDoNotCrossNativeManifestBoundary(t *testing.T) {
	shared := importDraftTestPlan(t, "create-listing")
	native := singleImportTestBatch(shared.Items[0])
	api := newImportDraftUnitAPI(t, shared, "create-listing")
	state, err := openImportState(filepath.Join(t.TempDir(), "private", "state.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer state.close()
	// Match every native binding deliberately. The independent receipt format
	// must still refuse this four-step reusable receipt before any mutation.
	receipt, err := createImportSteps(importReceipt{Format: 3, Origin: api.session().origin, Principal: importTestPrincipal, IntentDigest: importIntent(native), Draft: &importDraftBinding{NamespaceID: "00000000-0000-7000-8000-000000000010", IncludeListing: true}}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err = state.save(receipt); err != nil {
		t.Fatal(err)
	}
	if _, err = decodeImportReceipt(state.previous); err != nil {
		t.Fatal("positive reusable receipt control failed", err)
	}
	before := bytes.Clone(state.previous)
	if _, err = applyPreparedImport(t.Context(), native, remoteOptions{}, state, true, api.session()); err == nil || api.calls != 0 || !bytes.Equal(before, state.previous) {
		t.Fatal("native import accepted a reusable receipt or changed captured progress", err)
	}
}

func TestImportDraftVersionRequestLimitExcludesResourceTemplate(t *testing.T) {
	for _, listing := range []bool{false, true} {
		t.Run(strconv.FormatBool(listing), func(t *testing.T) {
			filename, file := importDraftTestFile(t, listing)
			options := importDraftOptions{ResourceID: "00000000-0000-7000-8000-000000000101"}
			if listing {
				options.PublisherID = "00000000-0000-7000-8000-000000000020"
				options.ListingID = "00000000-0000-7000-8000-000000000105"
			}
			const limit = 768
			writeImportDraftTest(t, filename, file)
			baseline, err := prepareImportInput(t.Context(), filename, limit, options)
			if err != nil || !baseline.Preview.LocallyValid {
				t.Fatal("short-identifier control failed", err)
			}
			// This valid identifier exceeds the selected request byte limit. It
			// identifies the target. The CLI sends only Version/Listing content.
			file.CanonicalIdentifier = "custom:" + strings.Repeat("界", 505)
			writeImportDraftTest(t, filename, file)
			plan, err := prepareImportInput(t.Context(), filename, limit, options)
			if err != nil || !plan.Preview.LocallyValid {
				t.Fatal("existing Resource metadata incorrectly consumed a request budget", err)
			}
			for _, command := range plan.Preview.Resources[0].Commands {
				if command.BodyBytes > limit {
					t.Fatal("a real command exceeds the selected request limit")
				}
			}
			options.ResourceID, options.ListingID, options.NamespaceID = "", "", importPlaceholderID
			if _, err := prepareImportInput(t.Context(), filename, limit, options); err == nil {
				t.Fatal("new Resource creation bypassed its real request limit")
			}
		})
	}
}
