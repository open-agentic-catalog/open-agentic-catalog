//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

// This SDK/CLI exchange verifies Batch 1 wiring. Independent client and peer
// qualification remains the separately selected N-132 Batch 2 workflow.
func TestMCPStdioAgainstCatalog(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	binary := filepath.Join(canonicalTempDir(t), "catalogctl")
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-ldflags=-X main.version=1.2.3-test", "-o", binary, ".") // #nosec G204,G702 -- fixed source and invocation-owned binary.
	if raw, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", raw, err)
	}
	runtime, server, origin, _, writes := configuredDemoRuntime(t)
	report, err := executeExpandedDemo(ctx, server.URL, filepath.Join(canonicalTempDir(t), "seed.json"), origin, false)
	if err != nil {
		t.Fatal(err)
	}
	startWrites := writes.Load()
	command := exec.CommandContext(ctx, binary, "mcp", "serve", "--base-url", server.URL, "--token-file", clientSkillToken(t, "local-reader")) // #nosec G204,G702 -- owned command, local server and synthetic token.
	command.Env = append(os.Environ(), "CATALOG_CONFIG_FILE=/missing/PRIVATE_SENTINEL", "CATALOG_DATABASE_DSN=postgres://PRIVATE_SENTINEL@invalid", "CATALOG_REMOTE_CA_FILE=", "CATALOG_REMOTE_TOKEN=ignored")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "batch-one-functional-control", Version: "1"}, &mcp.ClientOptions{Logger: slog.New(slog.DiscardHandler)})
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal("initialize", err, stderr.String())
	}
	t.Cleanup(func() { _ = session.Close() })
	if session.InitializeResult().ServerInfo.Version != "1.2.3-test" {
		t.Fatal("compiled command lost its application version")
	}
	listed, err := session.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 6 {
		t.Fatal("tool list", listed, err)
	}
	for _, tool := range listed.Tools {
		if !strings.HasPrefix(tool.Name, "catalog_control_") || !tool.Annotations.ReadOnlyHint {
			t.Fatal("unsafe tool", tool.Name)
		}
	}
	invoke := func(name string, args any, wantError bool) json.RawMessage {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || result.IsError != wantError {
			t.Fatal("tool result", name, result, err)
		}
		if wantError {
			return nil
		}
		var envelope remoteEnvelope
		raw, err := json.Marshal(result.StructuredContent)
		if err != nil || json.Unmarshal(raw, &envelope) != nil || len(envelope.Data) == 0 || envelope.Meta.RequestID == "" {
			t.Fatal("invalid MCP output", name, string(raw), err)
		}
		if name == "catalog_control_release" && !strings.HasPrefix(envelope.Meta.ETag, `"rev-`) {
			t.Fatal("release ETag lost", envelope.Meta)
		}
		return envelope.Data
	}
	beforeActivity, err := runtime.Observability.Activity(false)
	if err != nil {
		t.Fatal(err)
	}
	invoke("catalog_control_capabilities", map[string]any{}, false)
	afterActivity, err := runtime.Observability.Activity(false)
	if err != nil {
		t.Fatal(err)
	}
	// The Control response can arrive before the API middleware records completion.
	completionContext, stopCompletion := context.WithTimeout(ctx, 3*time.Second)
	defer stopCompletion()
	for afterActivity.HTTP.Completed < beforeActivity.HTTP.Completed+1 {
		if completionContext.Err() != nil {
			t.Fatal("stdio Control read completion was not recorded")
		}
		goruntime.Gosched()
		afterActivity, err = runtime.Observability.Activity(false)
		if err != nil {
			t.Fatal(err)
		}
	}
	stopCompletion()
	// The stdio tool makes one Control API read. It creates no HTTP MCP operation.
	if afterActivity.HTTP.Completed != beforeActivity.HTTP.Completed+1 || afterActivity.HTTP.Families[observability.FamilyPlatform].Completed != beforeActivity.HTTP.Families[observability.FamilyPlatform].Completed+1 || afterActivity.HTTP.Families[observability.FamilyHTTPMCP].Completed != beforeActivity.HTTP.Families[observability.FamilyHTTPMCP].Completed {
		t.Fatal("stdio activity population is not the underlying Control read")
	}
	for i, outcome := range afterActivity.MCP.Outcomes {
		if outcome.Completed != beforeActivity.MCP.Outcomes[i].Completed {
			t.Fatal("stdio changed HTTP MCP outcomes")
		}
	}
	seen := map[string]bool{}
	kinds := map[catalog.Kind]bool{}
	namespaces := map[string]bool{}
	cursor := ""
	for range 30 {
		raw := invoke("catalog_control_search", map[string]any{"limit": 1, "cursor": cursor}, false)
		var page struct {
			Items      []catalog.CatalogSearchResult `json:"items"`
			NextCursor string                        `json:"nextCursor"`
			Partial    bool                          `json:"partial"`
		}
		if json.Unmarshal(raw, &page) != nil || page.Partial || len(page.Items) != 1 || page.Items[0].Local == nil || seen[page.Items[0].Local.Release.ID] {
			t.Fatal("lost or repeated local page", string(raw))
		}
		item := page.Items[0]
		seen[item.Local.Release.ID] = true
		kinds[item.Kind] = true
		namespaces[item.Local.Resource.NamespaceID] = true
		detail := invoke("catalog_control_release", map[string]any{"releaseId": item.Local.Release.ID}, false)
		var projection struct {
			Release         catalog.ListingRelease `json:"release"`
			Representations []struct {
				ContentDigest string          `json:"contentDigest"`
				Data          json.RawMessage `json:"data"`
			} `json:"representations"`
		}
		if json.Unmarshal(detail, &projection) != nil || projection.Release.ID != item.Local.Release.ID || len(projection.Representations) != 1 || projection.Representations[0].ContentDigest == "" || len(projection.Representations[0].Data) != 0 {
			t.Fatal("native document exposed or identity lost", string(detail))
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	expected := 0
	for _, item := range report.Items {
		if item.ReleaseID != "" {
			expected++
		}
	}
	if cursor != "" || len(seen) != expected || len(kinds) != 4 {
		t.Fatal("incomplete four-kind continuation", len(seen), expected, kinds)
	}
	var facets catalog.ExploreResponse
	if json.Unmarshal(invoke("catalog_control_explore", map[string]any{"facets": []map[string]any{{"field": "kind", "limit": 1}}}, false), &facets) != nil || facets.Matched != expected || len(facets.Facets["kind"].Buckets) != 1 || facets.Facets["kind"].OtherCount <= 0 {
		t.Fatal("local facets lost counts or bucket remainder", facets)
	}
	access := invoke("catalog_control_access", map[string]any{}, false)
	if strings.Contains(string(access), `"subject"`) || strings.Contains(string(access), `"membershipId"`) || strings.Contains(string(access), `"realmId"`) || !strings.Contains(string(access), `"availablePermissionsUse":"navigation_only"`) {
		t.Fatal("access projection exposed identity or lost its boundary", string(access))
	}
	invoke("catalog_control_release", map[string]any{"releaseId": "../../private"}, true)
	invoke("catalog_control_search", map[string]any{"baseURL": "https://never-contact.test"}, true)
	// Convert the same active principal to namespace-only Reader authority. The
	// running MCP command must observe retained policy changes without a restart.
	admin, err := runtime.Authenticator.Authenticate(ctx, "local-admin")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := runtime.Authenticator.Authenticate(ctx, "local-reader")
	if err != nil {
		t.Fatal(err)
	}
	adminCtx := identity.WithActor(ctx, admin)
	// The development Reader bundle has explicit discovery/feedback companions.
	// Revoke every seeded assignment, not only the legacy Reader projection.
	assignments, err := runtime.Identity.ListAuthorizationAssignments(adminCtx, identity.AuthorizationPageRequest{MembershipID: reader.MembershipID}, 100, "")
	if err != nil || len(assignments.Items) != 3 || assignments.NextCursor != "" {
		t.Fatal("reader assignment", err)
	}
	for _, assignment := range assignments.Items {
		if _, err := runtime.Identity.RevokeAuthorizationAssignment(adminCtx, assignment.ID, assignment.Revision, "Qualify current MCP scope", "mcp-revoke-"+assignment.ID); err != nil {
			t.Fatal(err)
		}
	}
	var selected catalog.CatalogSearchResult
	for _, item := range report.Items {
		if item.ReleaseID != "" {
			reply, err := requestRemote(ctx, remoteOptions{baseURL: server.URL, token: "local-admin", timeout: time.Second, path: "/api/v1/catalog/resources/" + item.ReleaseID})
			if err != nil {
				t.Fatal(err)
			}
			var record catalog.CatalogRecord
			if json.Unmarshal(reply.Data, &record) != nil {
				t.Fatal("record")
			}
			local := record.Summary().SearchLocal()
			selected = catalog.CatalogSearchResult{Local: &local}
			break
		}
	}
	grant, err := runtime.Identity.GrantRole(adminCtx, identity.GrantRoleInput{Subject: reader.Subject, ActorType: reader.ActorType, Role: identity.RoleReader, NamespaceID: selected.Local.Resource.NamespaceID, Reason: "Qualify namespace-only MCP discovery"}, "mcp-namespace")
	if err != nil {
		t.Fatal(err)
	}
	var companion identity.RoleAssignment
	for namespaceID := range namespaces {
		if namespaceID == selected.Local.Resource.NamespaceID {
			continue
		}
		companion, err = runtime.Identity.GrantRole(adminCtx, identity.GrantRoleInput{Subject: reader.Subject, ActorType: reader.ActorType, Role: identity.RoleReader, NamespaceID: namespaceID, Reason: "Qualify self-access continuation"}, "mcp-access-companion")
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if companion.ID == "" {
		t.Fatal("the maintained demo must provide two local namespaces")
	}
	var first, second struct {
		NamespaceGrants []identity.NamespacePermissionAccess `json:"namespaceGrants"`
		NextCursor      string                               `json:"nextCursor"`
	}
	if json.Unmarshal(invoke("catalog_control_access", map[string]any{"limit": 1}, false), &first) != nil || len(first.NamespaceGrants) != 1 || first.NextCursor == "" {
		t.Fatal("missing first access page", first)
	}
	if json.Unmarshal(invoke("catalog_control_access", map[string]any{"limit": 1, "cursor": first.NextCursor}, false), &second) != nil || len(second.NamespaceGrants) != 1 || second.NextCursor != "" || first.NamespaceGrants[0].NamespaceID == second.NamespaceGrants[0].NamespaceID {
		t.Fatal("missing or repeated access continuation", second)
	}
	invoke("catalog_control_access", map[string]any{"limit": 2, "cursor": first.NextCursor}, true)
	if _, err := runtime.Identity.RevokeRole(adminCtx, companion.ID, "Qualify stale access cursor", "mcp-access-companion-revoke", companion.Revision); err != nil {
		t.Fatal(err)
	}
	invoke("catalog_control_access", map[string]any{"limit": 1, "cursor": first.NextCursor}, true)
	invoke("catalog_control_access", map[string]any{"namespaceId": selected.Local.Resource.NamespaceID}, false)
	var scoped catalog.CatalogSearchResponse
	if json.Unmarshal(invoke("catalog_control_search", map[string]any{"namespaceId": selected.Local.Resource.NamespaceID}, false), &scoped) != nil || len(scoped.Items) == 0 {
		t.Fatal("namespace read lost all authorized releases")
	}
	for _, item := range scoped.Items {
		if item.Local == nil || item.Local.Resource.NamespaceID != selected.Local.Resource.NamespaceID {
			t.Fatal("namespace read exposed another origin or scope")
		}
	}
	invoke("catalog_control_release", map[string]any{"releaseId": selected.Local.Release.ID}, false)
	invoke("catalog_control_search", map[string]any{"origins": []string{"indexed"}}, true)
	if _, err := runtime.Identity.RevokeRole(adminCtx, grant.ID, "Revoke MCP read permission", "mcp-final-revoke", grant.Revision); err != nil {
		t.Fatal(err)
	}
	invoke("catalog_control_release", map[string]any{"releaseId": selected.Local.Release.ID}, true)
	invoke("catalog_control_capabilities", map[string]any{}, false)
	invoke("catalog_control_access", map[string]any{}, false)
	var hidden catalog.ExploreResponse
	if json.Unmarshal(invoke("catalog_control_explore", map[string]any{"facets": []map[string]any{{"field": "kind"}}}, false), &hidden) != nil || hidden.Matched != 0 || len(hidden.Facets["kind"].Buckets) != 0 {
		t.Fatal("revoked access exposed local facets", hidden)
	}
	if writes.Load() != startWrites {
		t.Fatal("MCP command sent a mutating API request")
	}
	if err := session.Close(); err != nil {
		t.Fatal("EOF cleanup", err)
	}
	if stderr.Len() != 0 {
		t.Fatal("unexpected command diagnostics", stderr.String())
	}
	t.Log("real stdio: four kinds, continuation, private references, current scoped authority, refusal and EOF passed")
}

func TestMCPStdioFrameRefusal(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	binary := filepath.Join(canonicalTempDir(t), "catalogctl")
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binary, ".") // #nosec G204,G702 -- owned compiled command.
	if raw, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", raw, err)
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	command := exec.CommandContext(ctx, binary, "mcp", "serve", "--base-url", server.URL, "--token-file", clientSkillToken(t, "PRIVATE_SENTINEL")) // #nosec G204,G702 -- owned local fixture.
	command.Env = append(os.Environ(), "CATALOG_REMOTE_CA_FILE=")
	command.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"padding":"` + strings.Repeat("x", 300<<10) + `"}}` + "\n")
	var out, diagnostic bytes.Buffer
	command.Stdout = &out
	command.Stderr = &diagnostic
	if err := command.Run(); err == nil || requests.Load() != 0 || strings.Contains(diagnostic.String(), "PRIVATE_SENTINEL") || !strings.Contains(diagnostic.String(), "protocol session failed") {
		t.Fatal("unsafe oversized frame outcome", err, requests.Load(), diagnostic.String())
	}
	decoder := json.NewDecoder(&out)
	for {
		var raw json.RawMessage
		err := decoder.Decode(&raw)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal("non-protocol stdout", err)
		}
	}
}

func TestMCPStdioCancellationAndTermination(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	binary := filepath.Join(canonicalTempDir(t), "catalogctl")
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binary, ".") // #nosec G204,G702 -- owned compiled command.
	if raw, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", raw, err)
	}
	started, cancelled := make(chan struct{}, 2), make(chan struct{}, 2)
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/capabilities" || r.Header.Get("Authorization") != "Bearer PRIVATE_SENTINEL" {
			t.Error("wrong read destination or credential")
		}
		n := requests.Add(1)
		if n == 2 || n == 4 {
			started <- struct{}{}
			<-r.Context().Done()
			cancelled <- struct{}{}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "lifecycle-control")
		_, _ = w.Write([]byte(`{"resourceKinds":["agent","mcp_server","skill","plugin"],"search":{},"limits":{}}`))
	}))
	t.Cleanup(upstream.Close)
	command := exec.CommandContext(ctx, binary, "mcp", "serve", "--base-url", upstream.URL, "--token-file", clientSkillToken(t, "PRIVATE_SENTINEL")) // #nosec G204,G702 -- owned loopback fixture and synthetic credential.
	command.Env = append(os.Environ(), "CATALOG_REMOTE_CA_FILE=")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-lifecycle-control", Version: "1"}, &mcp.ClientOptions{Logger: slog.New(slog.DiscardHandler)})
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatal("initialize", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	wait := func(signal <-chan struct{}) {
		t.Helper()
		select {
		case <-signal:
		case <-time.After(5 * time.Second):
			t.Fatal("stdio lifecycle did not complete promptly")
		case <-ctx.Done():
			t.Fatal("stdio lifecycle did not complete before its deadline")
		}
	}
	invoke := func(callCtx context.Context) bool {
		result, err := session.CallTool(callCtx, &mcp.CallToolParams{Name: "catalog_control_capabilities"})
		return err != nil || result.IsError
	}
	if invoke(ctx) {
		t.Fatal("initial successful control failed")
	}
	callCtx, stopCall := context.WithCancel(ctx)
	defer stopCall()
	callDone := make(chan bool, 1)
	go func() { callDone <- invoke(callCtx) }()
	wait(started)
	stopCall()
	select {
	case failed := <-callDone:
		if !failed {
			t.Fatal("cancelled stdio request succeeded")
		}
	case <-ctx.Done():
		t.Fatal("client call did not cancel")
	}
	wait(cancelled)
	if invoke(ctx) {
		t.Fatal("call cancellation ended the usable stdio session")
	}
	go func() { callDone <- invoke(ctx) }()
	wait(started)
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal("signal owned command", err)
	}
	wait(cancelled)
	select {
	case failed := <-callDone:
		if !failed {
			t.Fatal("terminated command returned a successful pending call")
		}
	case <-ctx.Done():
		t.Fatal("terminated command left a pending call")
	}
	_ = session.Close() // Wait for the owned process and its stderr writer.
	if command.ProcessState == nil || command.ProcessState.ExitCode() != 1 || !strings.Contains(stderr.String(), "protocol session failed or was cancelled") || strings.Contains(stderr.String(), "PRIVATE_SENTINEL") {
		t.Fatal("unexpected exit or unsafe termination diagnostic", command.ProcessState, stderr.String())
	}
	if requests.Load() != 4 {
		t.Fatal("unexpected requests after cancellation or shutdown", requests.Load())
	}
}
