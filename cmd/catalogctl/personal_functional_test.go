//go:build functional

// Personal functional checks use the compiled command and real authenticated API workflows.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
)

// interceptPersonalSaves lets a test drop an actual server response after the API has handled the save.
func interceptPersonalSaves(t *testing.T, api http.Handler, drop func(*http.Request, []byte, *httptest.ResponseRecorder) bool) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != personalSavePath {
			api.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read personal save request: %v", err)
			http.Error(w, "read failed", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		reply := httptest.NewRecorder()
		api.ServeHTTP(reply, r)
		if drop(r, body, reply) {
			panic(http.ErrAbortHandler) // Close the connection before sending any response.
		}
		for key, values := range reply.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(reply.Code)
		_, _ = w.Write(reply.Body.Bytes())
	})
}

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
	var failExportContinuation atomic.Bool
	var mutations, targets atomic.Int32
	var sentLock sync.Mutex
	var sent []struct{ key, body string }
	api := interceptPersonalSaves(t, runtime.HTTP.Handler(), func(r *http.Request, body []byte, reply *httptest.ResponseRecorder) bool {
		mutations.Add(1)
		sentLock.Lock()
		sent = append(sent, struct{ key, body string }{r.Header.Get("Idempotency-Key"), string(body)})
		sentLock.Unlock()
		if reply.Code != http.StatusCreated {
			return false
		}
		if member := suspendAfterSave.Swap(nil); member != nil {
			if _, err := runtime.Identity.SuspendMembership(adminCtx, member.ID, "Test authority changes during restore", "personal-suspend", member.Revision); err != nil {
				t.Error("owned membership suspension failed", err)
			}
		}
		return loseReplyAfter.Load() > 0 && loseReplyAfter.Add(-1) == 0
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/resolve") || strings.Contains(r.URL.Path, "/catalog/") {
			targets.Add(1)
		}
		if r.Method == http.MethodGet && r.URL.Path == personalSavePath && r.URL.Query().Get("cursor") != "" && failExportContinuation.CompareAndSwap(true, false) {
			// Only inject the outage if this cursor actually opens the owner's second page.
			reply := httptest.NewRecorder()
			api.ServeHTTP(reply, r)
			var page struct {
				Items      []personal.Item `json:"items"`
				NextCursor string          `json:"nextCursor"`
			}
			if reply.Code != http.StatusOK || json.Unmarshal(reply.Body.Bytes(), &page) != nil || len(page.Items) != 99 || page.NextCursor != "" {
				t.Error("export did not reach the 99 remaining owner records")
			}
			http.Error(w, "temporary outage", http.StatusServiceUnavailable)
			return
		}
		api.ServeHTTP(w, r)
	}))
	defer server.Close()
	root := canonicalTempDir(t)
	run := func(success bool, token string, args ...string) []byte {
		t.Helper()
		env := append(catalogctlProcessEnvironment(t), "CATALOG_REMOTE_BASE_URL="+server.URL, "CATALOG_REMOTE_TOKEN="+token, "CATALOG_CONFIG_FILE=/missing/PRIVATE_SENTINEL")
		exitCode := 1
		if success {
			exitCode = 0
		}
		stdout, stderr := startCatalogctlProcess(t, ctx, binary, env, append([]string{"personal"}, args...)...).wait(t, exitCode)
		if success && len(stderr) != 0 || !success && len(stderr) == 0 || bytes.Contains(stderr, []byte("PRIVATE_SENTINEL")) {
			t.Fatalf("personal CLI: stdout=%s stderr=%s", stdout, stderr)
		}
		return stdout
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
	readLibrary := func() map[string]personal.Item {
		t.Helper()
		items := make(map[string]personal.Item)
		const pagePath = personalSavePath + "?limit=100"
		path := pagePath
		for offset := 0; offset < personal.MaxItems; offset += 100 {
			var page struct {
				Items      []personal.Item `json:"items"`
				NextCursor string          `json:"nextCursor"`
			}
			if err := json.Unmarshal(request("local-reader", "GET", path, "", nil, 200).Data, &page); err != nil {
				t.Fatal(err)
			}
			for _, saved := range page.Items {
				if _, seen := items[saved.ID]; saved.ID == "" || seen {
					t.Fatalf("empty or repeated item ID in owner inventory: %q", saved.ID)
				}
				items[saved.ID] = saved
			}
			if len(page.Items) > 100 || (page.NextCursor != "" && len(page.Items) != 100) {
				t.Fatalf("unexpected library page size %d", len(page.Items))
			}
			if page.NextCursor == "" {
				return items
			}
			path = pagePath + "&cursor=" + url.QueryEscape(page.NextCursor)
		}
		t.Fatal("library pagination exceeded the owner quota")
		return nil
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
	if len(outcome.Items) != 1 || outcome.Items[0].State != "uncertain" || outcome.Items[0].ID != "" {
		t.Fatal("lost committed response was called a rollback")
	}
	committedWhileUncertain := readLibrary()
	if len(committedWhileUncertain) != 1 {
		t.Fatalf("expected one committed save before resume, got %d", len(committedWhileUncertain))
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
	if outcome.Status != "complete" || len(outcome.Items) != 1 || outcome.Items[0].State != "acknowledged" {
		t.Fatal("same-key resume failed")
	}
	recoveredSearch := outcome.Items[0].ID
	if savedItem, ok := committedWhileUncertain[recoveredSearch]; !ok || savedItem.Revision != 1 || !reflect.DeepEqual(savedItem.Input, item) {
		t.Fatal("resume acknowledged a different item from the save that had already committed")
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
	if !reflect.DeepEqual(readLibrary(), committedWhileUncertain) {
		t.Fatal("resume changed the item that committed before acknowledgement")
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
	if len(readLibrary()) != 5 {
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
		filler := item
		filler.Label = fmt.Sprintf("Quota item %03d", i)
		request("local-reader", "POST", personalSavePath, fmt.Sprintf("personal-quota-%d", i), personalTestBytes(t, filler), 201)
	}
	expectedLibrary := readLibrary()
	if len(expectedLibrary) != personal.MaxItems-1 {
		t.Fatalf("expected one free slot, found %d", personal.MaxItems-len(expectedLibrary))
	}
	// Export into the same fresh destination again after an interrupted continuation.
	exportDir := filepath.Join(root, "paginated-backup")
	exportPath := filepath.Join(exportDir, "library.json")
	failExportContinuation.Store(true)
	if stdout := run(false, "local-reader", "export", "--output-dir", exportDir); failExportContinuation.Load() || len(stdout) != 0 {
		t.Fatal("failed continuation was missed or reported a completed export")
	}
	if _, err := os.Stat(exportPath); !os.IsNotExist(err) {
		t.Fatalf("interrupted export left a completed file: %v", err)
	}
	if !reflect.DeepEqual(readLibrary(), expectedLibrary) {
		t.Fatal("interrupted export changed the owner's library")
	}
	var summary struct {
		File  string `json:"file"`
		Items int    `json:"items"`
	}
	if err := json.Unmarshal(run(true, "local-reader", "export", "--output-dir", exportDir), &summary); err != nil || summary.File != "library.json" || summary.Items != len(expectedLibrary) {
		t.Fatalf("incorrect completed export summary: %+v, %v", summary, err)
	}
	exported, backup, err := readPersonalFile(exportPath)
	if err != nil || len(backup.Items) != len(expectedLibrary) || bytes.Contains(exported, []byte(other.Label)) {
		t.Fatalf("incomplete or cross-owner backup: %d items, %v", len(backup.Items), err)
	}
	for i, id := range slices.Sorted(maps.Keys(expectedLibrary)) {
		if !reflect.DeepEqual(backup.Items[i], expectedLibrary[id].Input) {
			t.Fatalf("backup item %d differs from the API record in owner ID order", i+1)
		}
	}
	file.Items = []personal.Input{item, item}
	file.Items[0].Label = "Fits in the last slot"
	file.Items[1].Label = "Requires a free slot"
	if err := os.WriteFile(filename, personalTestBytes(t, file), 0600); err != nil {
		t.Fatal(err)
	}
	args = []string{"--file", filename, "--state-file", filepath.Join(root, "quota", "state.json"), "--all", "--confirm"}
	outcome = readReport(run(false, "local-reader", append([]string{"apply"}, args...)...))
	if outcome.Status != "incomplete" || len(outcome.Items) != 2 || outcome.Items[0].State != "acknowledged" || outcome.Items[1].State != "failed" {
		t.Fatal("quota race lost per-item admission", outcome)
	}
	if failure := outcome.Items[1].Failure; failure == nil || failure.Status != http.StatusConflict || failure.Code != string(catalog.CodeConflict) || outcome.Items[1].ID != "" {
		t.Fatalf("expected quota refusal without an item ID, got %+v", outcome.Items[1])
	}
	onlyAdded := func(prior map[string]personal.Item, id string, want personal.Input) personal.Item {
		t.Helper()
		after := readLibrary()
		added, found := after[id]
		if _, existed := prior[id]; existed || !found || added.Revision != 1 || !reflect.DeepEqual(added.Input, want) {
			t.Fatalf("missing or incorrect acknowledged item %q: %+v", id, added)
		}
		delete(after, id)
		if !reflect.DeepEqual(after, prior) {
			t.Fatal("restore changed other items in the owner's library")
		}
		return added
	}
	firstAtQuota := onlyAdded(expectedLibrary, outcome.Items[0].ID, file.Items[0])
	request("local-reader", "DELETE", personalSavePath+"/"+recoveredSearch, "quota-free-one-slot", nil, 200)
	expectedLibrary[firstAtQuota.ID] = firstAtQuota
	delete(expectedLibrary, recoveredSearch)
	before = mutations.Load()
	resumed := readReport(run(false, "local-reader", append([]string{"resume"}, args...)...))
	if mutations.Load() != before || !reflect.DeepEqual(resumed, outcome) || !reflect.DeepEqual(readLibrary(), expectedLibrary) {
		t.Fatal("resume retried a definitive quota refusal or changed retained state")
	}
	// After the owner frees a slot, a new plan can explicitly select just the failed item.
	args = []string{"--file", filename, "--state-file", filepath.Join(root, "quota-recovery", "state.json"), "--create", "2", "--confirm"}
	recovered := readReport(run(true, "local-reader", append([]string{"apply"}, args...)...))
	if recovered.Status != "complete" || len(recovered.Items) != 2 || recovered.Items[0] != (personalRestoreRow{Index: 1, State: "skipped"}) || recovered.Items[1].State != "acknowledged" || mutations.Load() != before+1 {
		t.Fatalf("fresh quota recovery plan did not save only the selected item: %+v", recovered)
	}
	secondAtQuota := onlyAdded(expectedLibrary, recovered.Items[1].ID, file.Items[1])
	// Current membership is checked between saves and again before exposing progress on resume.
	request("local-reader", "DELETE", personalSavePath+"/"+firstAtQuota.ID, "authority-space-1", nil, 200)
	request("local-reader", "DELETE", personalSavePath+"/"+secondAtQuota.ID, "authority-space-2", nil, 200)
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

func TestPersonalCLIResumesLostExistingFollow(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	binary := buildCatalogctlProcessBinary(t, ctx)
	runtime := buildSmokeTestRuntime(t)
	defer runtime.Close()
	ownerAPI := httptest.NewServer(runtime.HTTP.Handler())
	defer ownerAPI.Close()
	root := canonicalTempDir(t)
	demo, err := executeMinimalDemo(ctx, ownerAPI.URL, filepath.Join(root, "demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	var published demoItem
	for _, item := range demo.Items {
		if item.ReleaseID != "" {
			published = item
			break
		}
	}
	if published.ListingID == "" {
		t.Fatal("fixture did not publish a Listing to follow")
	}
	const reader = "local-reader"
	client := &smokeHTTPClient{baseURL: ownerAPI.URL, client: ownerAPI.Client()}
	original := personal.Input{Type: "follow", ListingID: personal.ListingID(published.ListingID), Label: "Backed-up label"}
	follow, etag, err := demoExchangeETag[personal.Item](ctx, client, http.MethodPost, personalSavePath, reader, "follow-create", "", original, http.StatusCreated)
	if err != nil || follow.ID == "" || follow.Revision != 1 {
		t.Fatalf("create follow: %+v, %v", follow, err)
	}

	type exchange struct {
		key, body, etag, replay string
		status                  int
	}
	var mu sync.Mutex
	var saves []exchange
	cliAPI := httptest.NewServer(interceptPersonalSaves(t, runtime.HTTP.Handler(), func(r *http.Request, body []byte, reply *httptest.ResponseRecorder) bool {
		mu.Lock()
		defer mu.Unlock()
		saves = append(saves, exchange{
			key: r.Header.Get("Idempotency-Key"), body: string(body),
			status: reply.Code, etag: reply.Header().Get("ETag"), replay: reply.Header().Get("Idempotency-Replayed"),
		})
		return len(saves) == 1 && reply.Code == http.StatusOK
	}))
	defer cliAPI.Close()
	env := append(catalogctlProcessEnvironment(t), "CATALOG_REMOTE_BASE_URL="+cliAPI.URL, "CATALOG_REMOTE_TOKEN="+reader)
	backup := filepath.Join(root, "backup")
	startCatalogctlProcess(t, ctx, binary, env, "personal", "export", "--output-dir", backup).wait(t, 0)
	path := personalSavePath + "/" + follow.ID
	resolve := func() personal.FollowResolution {
		t.Helper()
		result, err := demoExchange[personal.FollowResolution](ctx, client, http.MethodPost, path+"/resolve", reader, "", "", struct{}{}, http.StatusOK)
		if err != nil || result.Status != "available" || result.Current == nil || result.Current.ReleaseID != published.ReleaseID {
			t.Fatalf("resolve follow: %+v, %v", result, err)
		}
		return result
	}
	observed := *resolve().Current
	const newLabel = "Owner's current label"
	update := map[string]any{"type": "follow", "listingId": published.ListingID, "label": newLabel, "observed": observed}
	updated, currentETag, err := demoExchangeETag[personal.Item](ctx, client, http.MethodPatch, path, reader, "follow-acknowledge", etag, update, http.StatusOK)
	if err != nil || updated.ID != follow.ID || updated.Revision != 2 || currentETag == "" || currentETag == etag {
		t.Fatalf("rename and acknowledge follow: %+v, %v", updated, err)
	}
	if before := resolve(); before.Change != "unchanged" || *before.Current != observed {
		t.Fatalf("owner acknowledgement missing before restore: %+v", before)
	}

	restore := func(action string, exitCode int) personalRestoreReport {
		t.Helper()
		stdout, _ := startCatalogctlProcess(t, ctx, binary, env, "personal", action, "--file", filepath.Join(backup, "library.json"), "--state-file", filepath.Join(root, "progress", "state.json"), "--all", "--confirm").wait(t, exitCode)
		var report personalRestoreReport
		if err := json.Unmarshal(stdout, &report); err != nil {
			t.Fatalf("%s: invalid restore report: %v", action, err)
		}
		return report
	}
	interrupted := restore("apply", 1)
	if interrupted.Status != "incomplete" || len(interrupted.Items) != 1 || interrupted.Items[0].State != "uncertain" || interrupted.Items[0].ID != "" {
		t.Fatalf("lost reply should leave one uncertain follow: %+v", interrupted)
	}
	mu.Lock()
	attempts := len(saves)
	mu.Unlock()
	if attempts != 1 {
		t.Fatalf("apply sent %d saves before explicit resume, want 1", attempts)
	}
	completed := restore("resume", 0)
	if completed.Status != "complete" || len(completed.Items) != 1 || completed.Items[0].State != "already-followed" || completed.Items[0].ID != follow.ID {
		t.Fatalf("resume did not identify the existing follow: %+v", completed)
	}
	if again := restore("resume", 0); !reflect.DeepEqual(again, completed) {
		t.Fatalf("completed resume changed the acknowledgement: %+v", again)
	}
	mu.Lock()
	got := append([]exchange(nil), saves...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("expected one save on apply and one on explicit recovery, got %d", len(got))
	}
	first, replay := got[0], got[1]
	if first.status != http.StatusOK || replay.status != http.StatusOK || first.replay == "true" || replay.replay != "true" ||
		first.etag != currentETag || replay.etag != currentETag {
		t.Fatalf("expected the existing follow revision on both responses: %+v", got)
	}
	if first.key == "" || replay.key != first.key || replay.body != first.body {
		t.Fatal("recovery changed the original follow save key or request bytes")
	}
	var sent map[string]string
	want := map[string]string{"type": "follow", "listingId": published.ListingID, "label": original.Label}
	if err := json.Unmarshal([]byte(first.body), &sent); err != nil || !reflect.DeepEqual(sent, want) {
		t.Fatalf("apply did not send the exported follow: %v, %v", sent, err)
	}
	page, err := demoExchange[struct{ Items []personal.Item }](ctx, client, http.MethodGet, personalSavePath+"?limit=2", reader, "", "", nil, http.StatusOK)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("expected one retained item: %+v, %v", page, err)
	}
	item := page.Items[0]
	if item.ID != follow.ID || item.Revision != updated.Revision || item.Type != "follow" || item.ListingID != original.ListingID || item.Label != newLabel {
		t.Fatalf("restore replaced the owner's current follow: %+v", item)
	}
	if result := resolve(); result.Change != "unchanged" || *result.Current != observed {
		t.Fatalf("restore lost the owner's acknowledged release: %+v", result)
	}
}
