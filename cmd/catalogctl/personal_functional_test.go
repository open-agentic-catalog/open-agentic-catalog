//go:build functional

// Personal functional checks use the compiled command and real authenticated API workflows.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
)

func TestPersonalCLIRealAPIAndLostAcknowledgement(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	binary := buildCatalogctlProcessBinary(t, ctx)
	runtime := buildSmokeTestRuntime(t)
	defer runtime.Close()
	admin, err := runtime.Authenticator.Authenticate(ctx, "local-admin")
	if err != nil {
		t.Fatal(err)
	}
	adminCtx := identity.WithActor(ctx, admin)
	var suspendAfterSave atomic.Pointer[identity.Membership]
	var loseReplyAfter atomic.Int32
	var mutations, targets atomic.Int32
	var sentLock sync.Mutex
	var sent []struct{ key, body string }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/resolve") || strings.Contains(r.URL.Path, "/catalog/") {
			targets.Add(1)
		}
		if r.Method == http.MethodPost && r.URL.Path == personalSavePath {
			mutations.Add(1)
			body, _ := io.ReadAll(r.Body)
			sentLock.Lock()
			sent = append(sent, struct{ key, body string }{r.Header.Get("Idempotency-Key"), string(body)})
			sentLock.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(body))
			capture := httptest.NewRecorder()
			runtime.HTTP.Handler().ServeHTTP(capture, r)
			if capture.Code == http.StatusCreated {
				if member := suspendAfterSave.Swap(nil); member != nil {
					if _, err := runtime.Identity.SuspendMembership(adminCtx, member.ID, "Test authority changes during restore", "personal-suspend", member.Revision); err != nil {
						t.Error("owned membership suspension failed", err)
					}
				}
			}
			if capture.Code == http.StatusCreated && loseReplyAfter.Load() > 0 && loseReplyAfter.Add(-1) == 0 {
				// Drop the actual connection after a real commit, before acknowledgement.
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					t.Error("owned response does not support connection loss")
					return
				}
				connection, _, err := hijacker.Hijack()
				if err != nil {
					t.Error("owned response-loss control", err)
					return
				}
				_ = connection.Close()
				return
			}
			for key, values := range capture.Header() {
				w.Header()[key] = values
			}
			w.WriteHeader(capture.Code)
			_, _ = w.Write(capture.Body.Bytes())
			return
		}
		runtime.HTTP.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	root := canonicalTempDir(t)
	run := func(success bool, token string, args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(ctx, binary, append([]string{"personal"}, args...)...) // #nosec G204,G702 -- compiled owned binary and synthetic fixture arguments.
		command.Env = append(catalogctlProcessEnvironment(t), "CATALOG_REMOTE_BASE_URL="+server.URL, "CATALOG_REMOTE_TOKEN="+token, "CATALOG_CONFIG_FILE=/missing/PRIVATE_SENTINEL")
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		if (err == nil) != success || success && stderr.Len() != 0 || strings.Contains(stderr.String(), "PRIVATE_SENTINEL") {
			t.Fatalf("personal CLI: %v stdout=%s stderr=%s", err, &stdout, &stderr)
		}
		return stdout.Bytes()
	}
	readReport := func(raw []byte) personalRestoreReport {
		t.Helper()
		var report personalRestoreReport
		if err := json.Unmarshal(raw, &report); err != nil {
			t.Fatal("invalid CLI progress response", err)
		}
		return report
	}
	request := func(token, method, path, key string, body []byte, status int) remoteEnvelope {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		if method == "DELETE" {
			r.Header.Set("If-Match", `"rev-1"`)
		}
		reply, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = reply.Body.Close() }()
		raw, err := io.ReadAll(reply.Body)
		if err != nil || reply.StatusCode != status {
			t.Fatalf("fixture request: %d %s %v", reply.StatusCode, raw, err)
		}
		return remoteEnvelope{Data: raw}
	}
	item := personalTestFile().Items[0]
	streaming := false
	item.Search.Origins = []catalog.SearchResultType{catalog.SearchResultLocal}
	item.Search.NativeCapabilities = &catalog.NativeCapabilitiesFilter{AgentStreaming: &streaming, AgentSkillIDs: []string{"invoice, literal "}}
	var saved struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(request("local-reader", "POST", personalSavePath, "personal-fixture-1", personalTestBytes(t, item), 201).Data, &saved) != nil {
		t.Fatal("create reply")
	}
	// Another same-tenant human's private criteria must never enter this export.
	other := item
	other.Label = "OTHER_OWNER_PRIVATE_SENTINEL"
	request("local-admin", "POST", personalSavePath, "personal-fixture-other", personalTestBytes(t, other), 201)
	directory := filepath.Join(root, "backup")
	run(true, "local-reader", "export", "--output-dir", directory)
	filename := filepath.Join(directory, "library.json")
	raw, file, err := readPersonalFile(filename)
	if err != nil || len(file.Items) != 1 || !reflect.DeepEqual(file.Items[0], item) || file.Origin != server.URL || bytes.Contains(raw, []byte(saved.ID)) || bytes.Contains(raw, []byte("OTHER_OWNER_PRIVATE_SENTINEL")) || bytes.Contains(raw, []byte("local-reader")) {
		t.Fatal("export leaked owner data or changed intent", err)
	}
	before := mutations.Load()
	run(true, "unused-preview-token", "preview", "--file", filename, "--tenant-id", file.TenantID, "--all")
	if mutations.Load() != before || targets.Load() != 0 {
		t.Fatal("preview resolved or saved a target")
	}
	// A shared API origin cannot make another authenticated tenant a valid destination.
	refused := readReport(run(false, "other-tenant-admin", "apply", "--file", filename,
		"--state-file", filepath.Join(root, "other-tenant", "state.json"), "--all", "--confirm"))
	if refused.Status != "refused" || len(refused.Items) != 0 || mutations.Load() != before {
		t.Fatal("fresh cross-tenant restore disclosed progress or sent a save")
	}
	request("local-reader", "DELETE", personalSavePath+"/"+saved.ID, "personal-fixture-remove", nil, 200)
	state := filepath.Join(root, "progress", "state.json")
	args := []string{"--file", filename, "--state-file", state, "--all", "--confirm"}
	loseReplyAfter.Store(1)
	outcome := readReport(run(false, "local-reader", append([]string{"apply"}, args...)...))
	if outcome.Items[0].State != "uncertain" {
		t.Fatal("lost committed response was called a rollback")
	}
	progressRoot, err := os.OpenRoot(filepath.Dir(state))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = progressRoot.Close() }()
	originalProgress, err := progressRoot.ReadFile("state.json")
	if err != nil {
		t.Fatal(err)
	}
	var uncertain personalRestoreFile
	if json.Unmarshal(originalProgress, &uncertain) != nil {
		t.Fatal("uncertain progress unavailable")
	}
	for _, first := range []time.Time{{}, time.Now().Add(time.Hour), time.Now().Add(-24 * time.Hour)} {
		changed := uncertain
		changed.State.Steps = append([]personalRestoreStep(nil), uncertain.State.Steps...)
		changed.State.Steps[0].FirstAttempt = first
		changed.Digest = importDigest(personalTestBytes(t, changed.State))
		if err := progressRoot.WriteFile("state.json", personalTestBytes(t, changed), 0600); err != nil {
			t.Fatal(err)
		}
		before := mutations.Load()
		run(false, "local-reader", append([]string{"resume"}, args...)...)
		if mutations.Load() != before {
			t.Fatal("unsafe chronology sent a mutation")
		}
		var retained personalRestoreFile
		retainedBytes, err := progressRoot.ReadFile("state.json")
		if err != nil || json.Unmarshal(retainedBytes, &retained) != nil || !retained.State.Steps[0].FirstAttempt.Equal(first) {
			t.Fatal("unsafe resume refreshed chronology")
		}
	}
	if err := progressRoot.WriteFile("state.json", originalProgress, 0600); err != nil {
		t.Fatal(err)
	}
	// Owner and tenant changes refuse resume before another save or private progress disclosure.
	for _, token := range []string{"local-admin", "other-tenant-admin"} {
		before := mutations.Load()
		outcome = readReport(run(false, token, append([]string{"resume"}, args...)...))
		if len(outcome.Items) != 0 || mutations.Load() != before {
			t.Fatal("identity mismatch disclosed progress or sent a mutation")
		}
	}
	outcome = readReport(run(true, "local-reader", append([]string{"resume"}, args...)...))
	if outcome.Status != "complete" || outcome.Items[0].State != "acknowledged" {
		t.Fatal("same-key resume failed")
	}
	sentLock.Lock()
	sameCommand := len(sent) >= 2 && sent[len(sent)-1] == sent[len(sent)-2]
	sentLock.Unlock()
	if !sameCommand {
		t.Fatal("recovery changed key or request bytes")
	}
	before = mutations.Load()
	// An acknowledged create can outlive the replay window without being recreated.
	completedBytes, err := progressRoot.ReadFile("state.json")
	var completed personalRestoreFile
	if err != nil || json.Unmarshal(completedBytes, &completed) != nil {
		t.Fatal("completed state unavailable", err)
	}
	completed.State.Steps[0].FirstAttempt = time.Now().Add(-48 * time.Hour)
	completed.Digest = importDigest(personalTestBytes(t, completed.State))
	if err := progressRoot.WriteFile("state.json", personalTestBytes(t, completed), 0600); err != nil {
		t.Fatal(err)
	}
	run(true, "local-reader", append([]string{"resume"}, args...)...)
	if mutations.Load() != before {
		t.Fatal("completed restore created a duplicate")
	}
	var page struct {
		Items []personal.Item `json:"items"`
	}
	if json.Unmarshal(request("local-reader", "GET", personalSavePath, "", nil, 200).Data, &page) != nil || len(page.Items) != 1 || !reflect.DeepEqual(page.Items[0].Input, item) {
		t.Fatal("restore did not preserve exact criteria once")
	}
	if targets.Load() != 0 {
		t.Fatal("backup or restore executed search or resolution")
	}
	// A valid exact reference with no current target is refused by normal API admission.
	file.Items = []personal.Input{item, personalTestFile().Items[1], item}
	if err := os.WriteFile(filename, personalTestBytes(t, file), 0600); err != nil {
		t.Fatal(err)
	}
	args = []string{"--file", filename, "--state-file", filepath.Join(root, "denied", "state.json"), "--all", "--confirm"}
	outcome = readReport(run(false, "local-reader", append([]string{"apply"}, args...)...))
	if outcome.Items[0].State != "acknowledged" || outcome.Items[1].State != "failed" || outcome.Items[2].State != "ready" {
		t.Fatal("target refusal lost acknowledged/remaining work", outcome)
	}
	before = mutations.Load()
	run(false, "local-reader", append([]string{"resume"}, args...)...)
	if mutations.Load() != before {
		t.Fatal("definitively refused create repeated")
	}
	// Lose a later selected reply: retain earlier acknowledgements, explicit skips and unstarted work.
	file.Items = make([]personal.Input, 4)
	for i := range file.Items {
		file.Items[i] = item
		file.Items[i].Label = fmt.Sprintf("Selected restore item %d", i+1)
	}
	if err := os.WriteFile(filename, personalTestBytes(t, file), 0600); err != nil {
		t.Fatal(err)
	}
	selectedState := filepath.Join(root, "selected", "state.json")
	args = []string{"--file", filename, "--state-file", selectedState, "--create", "1", "--create", "3", "--create", "4", "--confirm"}
	before = mutations.Load()
	loseReplyAfter.Store(2)
	outcome = readReport(run(false, "local-reader", append([]string{"apply"}, args...)...))
	if len(outcome.Items) != 4 || outcome.Items[0].State != "acknowledged" || outcome.Items[1] != (personalRestoreRow{Index: 2, State: "skipped"}) || outcome.Items[2].State != "uncertain" || outcome.Items[3].State != "ready" || mutations.Load() != before+2 {
		t.Fatal("later response loss changed per-item progress or retried automatically", outcome)
	}
	firstAcknowledgement := outcome.Items[0]
	selectedRoot, err := os.OpenRoot(filepath.Dir(selectedState))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = selectedRoot.Close() }()
	selectedBytes, err := selectedRoot.ReadFile("state.json")
	var interrupted personalRestoreFile
	if err != nil || json.Unmarshal(selectedBytes, &interrupted) != nil {
		t.Fatal("selected progress unavailable", err)
	}
	outcome = readReport(run(true, "local-reader", append([]string{"resume"}, args...)...))
	if outcome.Status != "complete" || len(outcome.Items) != 4 || outcome.Items[0] != firstAcknowledgement || outcome.Items[1] != (personalRestoreRow{Index: 2, State: "skipped"}) || outcome.Items[2].State != "acknowledged" || outcome.Items[3].State != "acknowledged" || mutations.Load() != before+4 {
		t.Fatal("selected resume repeated earlier work or changed selection", outcome)
	}
	selectedBytes, err = selectedRoot.ReadFile("state.json")
	var finished personalRestoreFile
	if err != nil || json.Unmarshal(selectedBytes, &finished) != nil || !finished.State.Steps[2].FirstAttempt.Equal(interrupted.State.Steps[2].FirstAttempt) {
		t.Fatal("later response recovery refreshed the original attempt", err)
	}
	sentLock.Lock()
	commands := append([]struct{ key, body string }(nil), sent[len(sent)-4:]...)
	sentLock.Unlock()
	if commands[1] != commands[2] || commands[0].key == commands[1].key || commands[3].key == commands[1].key || commands[0].body != string(personalTestBytes(t, file.Items[0])) || commands[1].body != string(personalTestBytes(t, file.Items[2])) || commands[3].body != string(personalTestBytes(t, file.Items[3])) {
		t.Fatal("selected restore changed request bindings or created the skipped item")
	}
	if json.Unmarshal(request("local-reader", "GET", personalSavePath, "", nil, 200).Data, &page) != nil || len(page.Items) != 5 {
		t.Fatal("selected restore duplicated or omitted saved items")
	}
	for _, index := range []int{0, 2, 3} {
		var actual personal.Item
		row := outcome.Items[index]
		path := personalSavePath + "/" + row.ID
		if json.Unmarshal(request("local-reader", "GET", path, "", nil, 200).Data, &actual) != nil || !reflect.DeepEqual(actual.Input, file.Items[index]) || actual.Revision != 1 {
			t.Fatal("selected restored intent differs from the confirmed input")
		}
		request("local-reader", "DELETE", path, fmt.Sprintf("selected-remove-%d", index), nil, 200)
	}
	// Quota admission remains per-save: a partial restore never removes existing items.
	for i := 0; i < 197; i++ {
		request("local-reader", "POST", personalSavePath, fmt.Sprintf("personal-quota-%d", i), personalTestBytes(t, item), 201)
	}
	file.Items = []personal.Input{item, item}
	if err := os.WriteFile(filename, personalTestBytes(t, file), 0600); err != nil {
		t.Fatal(err)
	}
	args = []string{"--file", filename, "--state-file", filepath.Join(root, "quota", "state.json"), "--all", "--confirm"}
	outcome = readReport(run(false, "local-reader", append([]string{"apply"}, args...)...))
	if outcome.Items[0].State != "acknowledged" || outcome.Items[1].State != "failed" {
		t.Fatal("quota race lost per-item admission", outcome)
	}
	var full struct {
		Items      []personal.Item `json:"items"`
		NextCursor string          `json:"nextCursor"`
	}
	if json.Unmarshal(request("local-reader", "GET", personalSavePath+"?limit=100", "", nil, 200).Data, &full) != nil || len(full.Items) != 100 || full.NextCursor == "" {
		t.Fatal("quota first page incomplete")
	}
	var remaining struct {
		Items      []personal.Item `json:"items"`
		NextCursor string          `json:"nextCursor"`
	}
	if json.Unmarshal(request("local-reader", "GET", personalSavePath+"?limit=100&cursor="+url.QueryEscape(full.NextCursor), "", nil, 200).Data, &remaining) != nil || len(remaining.Items) != 100 || remaining.NextCursor != "" {
		t.Fatal("quota restore evicted items or exceeded 200")
	}
	// Current membership is checked between saves and again before exposing progress on resume.
	for i := 0; i < 2; i++ {
		request("local-reader", "DELETE", personalSavePath+"/"+full.Items[i].ID, fmt.Sprintf("authority-space-%d", i), nil, 200)
	}
	reader, err := runtime.Authenticator.Authenticate(ctx, "local-reader")
	if err != nil {
		t.Fatal(err)
	}
	member, err := runtime.Identity.GetMembership(adminCtx, reader.MembershipID)
	if err != nil {
		t.Fatal(err)
	}
	args = []string{"--file", filename, "--state-file", filepath.Join(root, "authority", "state.json"), "--all", "--confirm"}
	before = mutations.Load()
	suspendAfterSave.Store(&member)
	outcome = readReport(run(false, "local-reader", append([]string{"apply"}, args...)...))
	if len(outcome.Items) != 2 || outcome.Items[0].State != "acknowledged" || outcome.Items[1].State != "ready" || mutations.Load() != before+1 {
		t.Fatal("changed membership did not stop before the next save", outcome)
	}
	firstAcknowledgement = outcome.Items[0]
	refused = readReport(run(false, "local-reader", append([]string{"resume"}, args...)...))
	if refused.Status != "refused" || len(refused.Items) != 0 || mutations.Load() != before+1 {
		t.Fatal("suspended membership read progress or sent a save")
	}
	member, err = runtime.Identity.GetMembership(adminCtx, reader.MembershipID)
	if err != nil || member.State != identity.MembershipSuspended {
		t.Fatal("owned membership was not suspended", err)
	}
	if _, err := runtime.Identity.ReactivateMembership(adminCtx, member.ID, "Restore fixture authority", "personal-reactivate", member.Revision); err != nil {
		t.Fatal(err)
	}
	outcome = readReport(run(true, "local-reader", append([]string{"resume"}, args...)...))
	if outcome.Status != "complete" || outcome.Items[0] != firstAcknowledgement || outcome.Items[1].State != "acknowledged" || mutations.Load() != before+2 {
		t.Fatal("renewed current authority did not resume only the unstarted save", outcome)
	}
}
