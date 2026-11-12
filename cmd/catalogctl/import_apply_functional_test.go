//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// This HTTP fixture qualifies the CLI's transport/recovery protocol. The real
// PostgreSQL process fixture separately qualifies service commits and replay.
type importHTTPFixture struct {
	mu                     sync.Mutex
	plans                  []importPlan
	replies                []remoteEnvelope
	keys                   map[string]int
	bodies                 map[string][]byte
	commits, calls         int
	faultStep              int
	fault                  string
	fired                  bool
	principal              importPrincipal
	deny, finalReadFailure bool
	budget                 int64
	beforeReply            func(int)
}

func newImportHTTPFixture(t *testing.T, plan importPlan) *importHTTPFixture {
	t.Helper()
	return &importHTTPFixture{plans: []importPlan{plan}, replies: importTestReplies(t, plan), keys: map[string]int{}, bodies: map[string][]byte{}, faultStep: -1, principal: importTestPrincipal, budget: 2 << 20}
}

func (f *importHTTPFixture) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer snapshot-token" && r.Header.Get("Authorization") != "Bearer renewed-token" {
		t.Error("unexpected credential")
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-ID", "request-owned")
	write := func(value any) { raw := importTestJSON(t, value); _, _ = w.Write(raw) }
	if r.URL.Path == "/api/v1/me" {
		permissions := []string{"resource.create", "resource.edit"}
		if f.deny {
			permissions = nil
		}
		write(map[string]any{"tenantId": f.principal.TenantID, "realmId": f.principal.RealmID, "membershipId": f.principal.MembershipID, "subject": f.principal.Subject, "permissions": permissions, "scopeEnforcement": false})
		return
	}
	if r.URL.Path == "/api/v1/capabilities" {
		write(map[string]any{"resourceKinds": []string{"agent", "mcp_server", "skill", "plugin"}, "limits": map[string]any{"requestBodyBytes": f.budget}})
		return
	}
	if r.Method == http.MethodPost {
		f.calls++
		key := r.Header.Get("Idempotency-Key")
		index, known := f.keys[key]
		if !known {
			index = f.commits
			f.keys[key] = index
			f.commits++
		}
		if index >= len(f.replies) {
			t.Error("extra draft creation")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		itemIndex, stepIndex := index/importStepsPerResource, index%importStepsPerResource
		plan := f.plans[itemIndex]
		var resource catalog.Resource
		var version struct {
			Revision catalog.VersionRevision `json:"revision"`
		}
		var listing catalog.CatalogListing
		_ = json.Unmarshal(f.replies[itemIndex*4].Data, &resource)
		_ = json.Unmarshal(f.replies[itemIndex*4+1].Data, &version)
		_ = json.Unmarshal(f.replies[itemIndex*4+2].Data, &listing)
		if r.URL.Path != strings.ReplaceAll(strings.ReplaceAll(plan.Preview.Commands[stepIndex].Path, "{resourceId}", resource.ID), "{listingId}", listing.ID) {
			t.Errorf("wrong command path %s", r.URL.Path)
		}
		wantETag := ""
		if stepIndex == 3 {
			wantETag = `"rev-1"`
		}
		if r.Header.Get("If-Match") != wantETag {
			t.Error("command changed the expected If-Match binding")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || !json.Valid(body) || len(key) < 8 {
			t.Error("invalid command body/key")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if known && !bytes.Equal(body, f.bodies[key]) {
			t.Error("replay changed the original request bytes")
		} else if !known {
			f.bodies[key] = bytes.Clone(body)
		}
		var actual, expected map[string]any
		for _, input := range []struct {
			raw   []byte
			value *map[string]any
		}{{body, &actual}, {plan.Bodies[stepIndex], &expected}} {
			decoder := json.NewDecoder(bytes.NewReader(input.raw))
			decoder.UseNumber()
			if err := decoder.Decode(input.value); err != nil {
				t.Error("cannot decode command fixture")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		switch stepIndex {
		case 2:
			expected["resourceId"] = resource.ID
		case 3:
			expected["versionRevisionId"] = version.Revision.ID
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Errorf("command %d changed the prepared content or parent binding", index)
		}
		if f.beforeReply != nil {
			f.beforeReply(index)
		}
		if index == f.faultStep && !f.fired {
			f.fired = true
			switch f.fault {
			case "malformed":
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, `{"committed":`)
				return
			case "collision":
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusConflict)
				write(map[string]any{"code": "CONFLICT"})
				return
			case "stale":
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusPreconditionFailed)
				write(map[string]any{"code": "PRECONDITION_FAILED"})
				return
			case "pending":
				w.Header().Set("Content-Type", "application/problem+json")
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusServiceUnavailable)
				write(map[string]any{"code": "DEPENDENCY_UNAVAILABLE"})
				return
			case "blocked":
				f.replies[index].Data = bytes.Replace(f.replies[index].Data, []byte(`"state":"complete"`), []byte(`"state":"blocked"`), 1)
			}
		}
		w.Header().Set("ETag", f.replies[index].Meta.ETag)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(f.replies[index].Data)
		return
	}
	if f.finalReadFailure && f.commits == 4 {
		w.WriteHeader(http.StatusServiceUnavailable)
		write(map[string]any{"code": "DEPENDENCY_UNAVAILABLE"})
		return
	}
	replyOffset := 0
	for index := range f.plans {
		for _, reply := range f.replies[index*4 : (index+1)*4] {
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			if bytes.Contains(reply.Data, []byte(`"id":"`+id+`"`)) {
				replyOffset = index * 4
			}
		}
	}
	var value map[string]json.RawMessage
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/v1/resource-versions/"):
		_ = json.Unmarshal(f.replies[replyOffset+1].Data, &value)
		_, _ = w.Write(value["version"])
	case strings.HasPrefix(r.URL.Path, "/api/v1/version-revisions/"):
		_ = json.Unmarshal(f.replies[replyOffset+1].Data, &value)
		write(map[string]json.RawMessage{"revision": value["revision"], "evidence": value["evidence"]})
	case strings.HasPrefix(r.URL.Path, "/api/v1/resources/"):
		w.Header().Set("ETag", `"rev-7"`)
		var resource catalog.Resource
		_ = json.Unmarshal(f.replies[replyOffset].Data, &resource)
		resource.Revision = 7
		write(resource)
	case strings.HasPrefix(r.URL.Path, "/api/v1/catalog-listings/"):
		_ = json.Unmarshal(f.replies[replyOffset+3].Data, &value)
		_, _ = w.Write(value["listing"])
	case strings.HasPrefix(r.URL.Path, "/api/v1/listing-revisions/"):
		_ = json.Unmarshal(f.replies[replyOffset+3].Data, &value)
		_, _ = w.Write(value["revision"])
	default:
		t.Errorf("unplanned request %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestImportApplyHTTPRecoveryAtEveryStep(t *testing.T) {
	for index := range 4 {
		t.Run(string(rune('0'+index)), func(t *testing.T) {
			plan := importApplyTestPlan(t)
			fixture := newImportHTTPFixture(t, plan)
			fixture.faultStep = index
			fixture.fault = "malformed"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fixture.serve(t, w, r) }))
			defer server.Close()
			options := remoteOptions{baseURL: server.URL, token: "snapshot-token", timeout: 10 * time.Second}
			state := filepath.Join(canonicalTempDir(t), "private", "state.json")
			outcome, err := applyImport(t.Context(), singleImportTestBatch(plan), options, state, false)
			if err == nil || outcome.Disposition != "uncertain" || fixture.calls != index+1 {
				t.Fatalf("did not stop uncertain success: %#v %v", outcome, err)
			}
			raw, _ := os.ReadFile(state) // #nosec G304 -- invocation-owned private receipt path.
			before, err := decodeImportReceipt(raw)
			if err != nil {
				t.Fatal(err)
			}
			options.token = "renewed-token"
			outcome, err = applyImport(t.Context(), singleImportTestBatch(plan), options, state, true)
			if err != nil || outcome.Disposition != "complete" || fixture.commits != 4 || fixture.calls != 5 {
				t.Fatalf("resume failed: %#v %v calls=%d", outcome, err, fixture.calls)
			}
			raw, _ = os.ReadFile(state) // #nosec G304 -- invocation-owned private receipt path.
			after, err := decodeImportReceipt(raw)
			if err != nil || !after.Steps[index].FirstAttempt.Equal(before.Steps[index].FirstAttempt) || after.Steps[index].Key != before.Steps[index].Key {
				t.Fatal("replay reservation changed")
			}
			outcome, err = applyImport(t.Context(), singleImportTestBatch(plan), options, state, true)
			if err != nil || outcome.Disposition != "complete" || fixture.calls != 5 {
				t.Fatal("completed rerun mutated current candidates")
			}
		})
	}
}

func TestImportApplyHTTPRefusalsAndPartialOutcomes(t *testing.T) {
	for _, test := range []struct {
		name, fault        string
		step               int
		deny, finalFailure bool
		budget             int64
		want               string
		calls              int
	}{
		{"blocked", "blocked", 1, false, false, 2 << 20, "blocked", 2},
		{"collision", "collision", 0, false, false, 2 << 20, "failed", 1},
		{"stale ETag", "stale", 3, false, false, 2 << 20, "failed", 4},
		{"pending", "pending", 1, false, false, 2 << 20, "uncertain", 2},
		{"denied", "", -1, true, false, 2 << 20, "failed", 0},
		{"small server limit", "", -1, false, false, 10, "failed", 0},
		{"final read unavailable", "", -1, false, true, 2 << 20, "uncertain", 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := importApplyTestPlan(t)
			fixture := newImportHTTPFixture(t, plan)
			fixture.faultStep, fixture.fault, fixture.deny, fixture.budget, fixture.finalReadFailure = test.step, test.fault, test.deny, test.budget, test.finalFailure
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fixture.serve(t, w, r) }))
			defer server.Close()
			options := remoteOptions{baseURL: server.URL, token: "snapshot-token", timeout: 10 * time.Second}
			state := filepath.Join(canonicalTempDir(t), "private", "state.json")
			outcome, err := applyImport(t.Context(), singleImportTestBatch(plan), options, state, false)
			if err == nil || outcome.Disposition != test.want || fixture.calls != test.calls {
				t.Fatalf("unexpected outcome %#v %v", outcome, err)
			}
			if test.calls > 0 {
				if test.fault == "pending" {
					// The original key can be checked by an explicit invocation,
					// but another pending response must stop that invocation too.
					fixture.mu.Lock()
					fixture.fired = false
					fixture.mu.Unlock()
				}
				if test.finalFailure {
					fixture.mu.Lock()
					fixture.finalReadFailure = false
					fixture.mu.Unlock()
				}
				_, err = applyImport(t.Context(), singleImportTestBatch(plan), options, state, true)
				wantCalls := test.calls
				if test.fault == "pending" {
					wantCalls++
				}
				if (err == nil) != test.finalFailure || fixture.calls != wantCalls {
					t.Fatalf("unsafe rerun %v calls=%d", err, fixture.calls)
				}
			}
		})
	}
}

func TestImportApplyResumeBindingsAndExpiry(t *testing.T) {
	for _, change := range []string{"tenant", "realm", "membership", "subject", "input", "origin", "expired", "future", "pending"} {
		t.Run(change, func(t *testing.T) {
			plan := importApplyTestPlan(t)
			fixture := newImportHTTPFixture(t, plan)
			fixture.faultStep = 0
			fixture.fault = "malformed"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fixture.serve(t, w, r) }))
			defer server.Close()
			options := remoteOptions{baseURL: server.URL, token: "snapshot-token", timeout: 10 * time.Second}
			name := filepath.Join(canonicalTempDir(t), "private", "state.json")
			if _, err := applyImport(t.Context(), singleImportTestBatch(plan), options, name, false); err == nil {
				t.Fatal("fault not injected")
			}
			fixture.mu.Lock()
			switch change {
			case "tenant":
				fixture.principal.TenantID = "00000000-0000-7000-8000-000000000009"
			case "realm":
				fixture.principal.RealmID = importPlaceholderID
			case "membership":
				fixture.principal.MembershipID = importPlaceholderID
			case "subject":
				fixture.principal.Subject = "other"
			case "input":
				plan.Preview.Assets[0].Digest = importDigest([]byte("changed"))
			case "pending":
				fixture.fault = "pending"
				fixture.fired = false
			case "origin", "expired", "future":
				state, err := openImportState(name, true)
				if err != nil {
					t.Fatal(err)
				}
				receipt, err := decodeImportReceipt(state.previous)
				if err != nil {
					t.Fatal(err)
				}
				if change == "origin" {
					receipt.Origin = "https://other.invalid"
				}
				if change == "expired" {
					receipt.Steps[0].FirstAttempt = time.Now().Add(-25 * time.Hour)
				}
				if change == "future" {
					receipt.Steps[0].FirstAttempt = time.Now().Add(time.Hour)
				}
				if err = state.save(receipt); err != nil {
					t.Fatal(err)
				}
				state.close()
			}
			fixture.mu.Unlock()
			if _, err := applyImport(t.Context(), singleImportTestBatch(plan), options, name, true); err == nil {
				t.Fatal("unsafe resume admitted")
			}
			want := 1
			if change == "pending" {
				want = 2
			}
			if fixture.calls != want {
				t.Fatal("refused resume made writes")
			}
			if change == "pending" {
				fixture.mu.Lock()
				fixture.fired = false
				fixture.mu.Unlock()
				if _, err := applyImport(t.Context(), singleImportTestBatch(plan), options, name, true); err == nil || fixture.calls != 3 || fixture.commits != 1 {
					t.Fatal("pending response caused automatic retry or a new mutation")
				}
			}
		})
	}
}

func TestImportApplyReceiptFailureAndCancellation(t *testing.T) {
	for _, fault := range []string{"receipt replacement", "cancellation"} {
		t.Run(fault, func(t *testing.T) {
			plan := importApplyTestPlan(t)
			fixture := newImportHTTPFixture(t, plan)
			name := filepath.Join(canonicalTempDir(t), "private", "state.json")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fixture.beforeReply = func(index int) {
				if index != 0 {
					return
				}
				if fault == "cancellation" {
					cancel()
				} else {
					if err := os.Rename(name, name+".preserved"); err != nil {
						t.Error(err)
					}
					if err := os.Symlink("state.json.preserved", name); err != nil {
						t.Error(err)
					}
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fixture.serve(t, w, r) }))
			defer server.Close()
			options := remoteOptions{baseURL: server.URL, token: "snapshot-token", timeout: 10 * time.Second}
			if _, err := applyImport(ctx, singleImportTestBatch(plan), options, name, false); err == nil || fixture.calls != 1 {
				t.Fatal("failure continued writes")
			}
			if fault == "receipt replacement" {
				info, err := os.Lstat(name)
				if err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatal("receipt substitution was overwritten")
				}
				raw, err := os.ReadFile(name + ".preserved") // #nosec G304 -- exact invocation-owned receipt preserved by this rejection fixture.
				if err != nil {
					t.Fatal(err)
				}
				receipt, err := decodeImportReceipt(raw)
				if err != nil || receipt.Steps[0].State != "inflight" {
					t.Fatal("original uncertain command evidence was changed")
				}
			}
		})
	}
}

func TestImportApplyCLIProcessFourKinds(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	binary := filepath.Join(canonicalTempDir(t), "catalogctl")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".") // #nosec G204,G702 -- fixed package and owned binary.
	if raw, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v %s", err, raw)
	}
	combinedDir := canonicalTempDir(t)
	combined := importManifest{Format: 1}
	for _, kind := range []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill, catalog.KindPlugin} {
		t.Run(string(kind), func(t *testing.T) {
			dir, manifest := importTestInput(t)
			r := &manifest.Resources[0]
			r.Kind = kind
			switch kind {
			case catalog.KindAgent:
				// The base fixture is an Agent.
			case catalog.KindMCPServer:
				r.Version = "2.0.0"
				r.Representations = []importRepresentation{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaMCPServerCard, DataFile: "card.json"}, {Role: catalog.RepresentationDistribution, MediaType: adapter.MediaMCPRegistry, DataFile: "registry.json"}}
				copyImportSample(t, dir, "everything.json", "card.json")
				copyImportSample(t, dir, "everything.registry.json", "registry.json")
			case catalog.KindSkill:
				r.Representations = []importRepresentation{{Role: catalog.RepresentationPrimary, MediaType: catalog.SkillPackageMediaZIP, PackageDirectory: "safe-skill", Handoff: &catalog.SkillHandoff{OCI: &catalog.OCIHandoffReference{Reference: "registry.example.com/safe-skill@sha256:" + strings.Repeat("a", 64)}}}}
				writeImportTestFile(t, dir, "safe-skill/SKILL.md", []byte("---\nname: safe-skill\ndescription: Helps with local examples\n---\nInstructions\n"), 0600)
				writeImportTestFile(t, dir, "safe-skill/scripts/run.sh", []byte("#!/bin/sh\nexit 1\n"), 0700)
			case catalog.KindPlugin:
				r.Representations = []importRepresentation{{Role: catalog.RepresentationPrimary, MediaType: catalog.PluginMediaZIP, URL: "https://metadata-only.invalid/plugin.zip"}}
			}
			item := *r
			item.Key = "batch-" + strings.ReplaceAll(string(kind), "_", "-")
			item.CanonicalIdentifier = "custom:" + item.Key
			item.Representations = slices.Clone(r.Representations)
			prefix := string(kind)
			if err := os.CopyFS(filepath.Join(combinedDir, prefix), os.DirFS(dir)); err != nil {
				t.Fatal(err)
			}
			for index := range item.Representations {
				source := &item.Representations[index]
				for _, field := range []*string{&source.DataFile, &source.ContentFile, &source.PackageFile, &source.PackageDirectory} {
					if *field != "" {
						*field = prefix + "/" + *field
					}
				}
			}
			combined.Resources = append(combined.Resources, item)
			plan, err := prepareImportTestContext(ctx, t, dir, manifest, 2<<20)
			if err != nil {
				t.Fatal(err)
			}
			fixture := newImportHTTPFixture(t, plan)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fixture.serve(t, w, r) }))
			defer server.Close()
			tokenFile := writeImportTestFile(t, dir, "token", []byte("snapshot-token\n"), 0600)
			name := filepath.Join(dir, "private", "state.json")
			for _, action := range []string{"apply", "resume"} {
				command := exec.CommandContext(ctx, binary, "import", action, "--file", filepath.Join(dir, "import.yaml"), "--state-file", name, "--base-url", server.URL, "--token-file", tokenFile) // #nosec G204,G702 -- owned binary and local fixtures.
				command.Env = append(os.Environ(), "CATALOG_REMOTE_TOKEN=ignored-environment-token", "HTTP_PROXY=http://unreachable.invalid", "HTTPS_PROXY=http://unreachable.invalid", "CATALOG_CONFIG_FILE=/missing")
				var stdout, stderr bytes.Buffer
				command.Stdout = &stdout
				command.Stderr = &stderr
				if err = command.Run(); err != nil {
					t.Fatalf("actual CLI: %v %s", err, &stderr)
				}
				var result importOutcome
				if json.Unmarshal(stdout.Bytes(), &result) != nil || result.Disposition != "complete" || len(result.Items[0].Steps) != 4 || stderr.Len() != 0 {
					t.Fatalf("invalid process outcome %s %s", &stdout, &stderr)
				}
				if bytes.Contains(stdout.Bytes(), []byte("snapshot-token")) || bytes.Contains(stdout.Bytes(), []byte("Private example")) {
					t.Fatal("private data in CLI output")
				}
			}
			if fixture.calls != 4 || fixture.commits != 4 {
				t.Fatal("rerun created duplicates")
			}
		})
	}
	t.Run("all kinds in manifest order", func(t *testing.T) {
		filename := writeImportTestFile(t, combinedDir, "import.yaml", importTestJSON(t, combined), 0600)
		batch, err := prepareImportFile(ctx, filename, 2<<20)
		if err != nil {
			t.Fatal(err)
		}
		fixture := newImportBatchHTTPFixture(t, batch)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fixture.serve(t, w, r) }))
		defer server.Close()
		token := writeImportTestFile(t, combinedDir, "token", []byte("snapshot-token"), 0600)
		for _, action := range []string{"apply", "resume"} {
			command := exec.CommandContext(ctx, binary, "import", action, "--file", filename, "--state-file", filepath.Join(combinedDir, "private", "state.json"), "--base-url", server.URL, "--token-file", token) // #nosec G204,G702 -- owned executable, local synthetic inputs.
			command.Env = append(os.Environ(), "CATALOG_REMOTE_TOKEN=ignored-token", "CATALOG_CONFIG_FILE=/missing")
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			if err := command.Run(); err != nil {
				t.Fatalf("four-kind batch process: %v %s", err, &stderr)
			}
			var outcome importOutcome
			if json.Unmarshal(stdout.Bytes(), &outcome) != nil || outcome.Disposition != "complete" || len(outcome.Items) != 4 || stderr.Len() != 0 {
				t.Fatal("four-kind batch report invalid")
			}
			for index, item := range outcome.Items {
				if item.Key != combined.Resources[index].Key || item.Disposition != "complete" {
					t.Fatal("four-kind batch order or status changed")
				}
			}
		}
		if fixture.commits != 16 || fixture.calls != 16 {
			t.Fatal("four-kind completed batch duplicated work")
		}
	})
	t.Run("real API validation and Plugin retrieval", func(t *testing.T) {
		qualifyImportRealAPI(t, binary, combinedDir, combined)
	})
	t.Run("checked-in Agent and Skill starter", func(t *testing.T) {
		qualifyImportStarter(t, binary)
	})
}

func TestImportApplyOutputFailurePreservesCompletedReceipt(t *testing.T) {
	dir, manifest := importTestInput(t)
	plan, err := prepareImportTest(t, dir, manifest, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	fixture := newImportHTTPFixture(t, plan)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fixture.serve(t, w, r) }))
	defer server.Close()
	token := writeImportTestFile(t, dir, "token", []byte("snapshot-token"), 0600)
	arguments := []string{"apply", "--file", filepath.Join(dir, "import.yaml"), "--state-file", filepath.Join(dir, "private", "state.json"), "--base-url", server.URL, "--token-file", token}
	if err := runImport(t.Context(), arguments, importShortWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("expected output failure after acknowledged writes: %v", err)
	}
	arguments[0] = "resume"
	var output bytes.Buffer
	if err := runImport(t.Context(), arguments, &output); err != nil {
		t.Fatal(err)
	}
	if fixture.calls != 4 || fixture.commits != 4 || !strings.Contains(output.String(), `"disposition":"complete"`) {
		t.Fatal("output recovery repeated committed drafts")
	}
}
