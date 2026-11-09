//go:build integration

package functional_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// Reuses the owned migrated database, exact API login and two compiled API
// processes. The proxy drops one committed response, then routes replay to the
// other process. No direct repository call creates the imported drafts.
func qualifyProcessImport(ctx context.Context, t *testing.T, f processFixture, owner *pgx.Conn, dsn, address, peer, tenant, namespace string) {
	t.Helper()
	var publisher catalog.Publisher
	decodeProcessJSON(t, f.request(ctx, address, "POST", "/api/v1/publishers", "local-admin", `{"displayName":"Import process fixture","domains":["example.com"]}`, 201), &publisher)
	client := &http.Client{Transport: &http.Transport{}, Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	var mu sync.Mutex
	dropped := false
	postCalls := 0
	type capturedCommand struct {
		path, key   string
		body, reply []byte
	}
	var initialCommands []capturedCommand
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		upstream := address
		if dropped {
			upstream = peer
		}
		var initialBody []byte
		if r.Method == http.MethodPost && postCalls < 2 {
			var err error
			initialBody, err = io.ReadAll(io.LimitReader(r.Body, (2<<20)+1))
			if err != nil || len(initialBody) > 2<<20 {
				t.Error("initial import request exceeds the owned fixture budget")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(initialBody))
		}
		forward, err := http.NewRequestWithContext(r.Context(), r.Method, "http://"+upstream+r.URL.RequestURI(), r.Body) // #nosec G704 -- fixed owned loopback API origin; only path/query come from this local proxy.
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		forward.Header = r.Header.Clone()
		response, err := client.Do(forward) // #nosec G704 -- owned fixed loopback API, redirects refused.
		if err != nil {
			t.Error("owned import forwarding failed")
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer func() { _ = response.Body.Close() }()
		if r.Method == http.MethodPost {
			postCalls++
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if initialBody != nil && response.StatusCode == http.StatusCreated {
			initialCommands = append(initialCommands, capturedCommand{r.URL.Path, r.Header.Get("Idempotency-Key"), initialBody, body})
		}
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		if !dropped && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/revisions") && postCalls == 8 && response.StatusCode == http.StatusCreated {
			dropped = true
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"committed":`)
			return
		}
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(body)
	}))
	defer proxy.Close()
	type step struct {
		State, Key string
		Ack        struct{ ID, RevisionID, EvidenceID string }
	}
	type itemReport struct {
		Disposition string
		Steps       []step
	}
	type report struct {
		Disposition string
		CurrentKey  string
		Items       []itemReport
	}
	dir := t.TempDir()
	filename := filepath.Join(dir, "import.json")
	state := filepath.Join(dir, "private", "progress.json")
	const extensions = `{"integer":9007199254740993,"decimal":0.123456789012345678901,"exponent":1e999,"ordinary":{"null":null,"boolean":true,"array":[1.20,"value"]}}`
	native := json.RawMessage(`{"name":"Owned CLI import","framework":"custom","extensions":{"integer":9007199254740993,"decimal":0.123456789012345678901,"lexical":1e3}}`)
	asset := filepath.Join(dir, "agent.json")
	if err := os.WriteFile(asset, native, 0600); err != nil {
		t.Fatal(err)
	}
	// Exercise the character limits through the compiled CLI, both API processes
	// and PostgreSQL, including an exact cross-namespace relationship.
	identifier, version := "custom:"+strings.Repeat("🙂", 505), strings.Repeat("🙂", 128)
	manifest := map[string]any{"format": 1, "resources": []any{map[string]any{"key": "process-import", "namespaceId": namespace, "canonicalIdentifier": identifier, "kind": "agent", "version": version, "listing": map[string]any{"publisherId": publisher.ID, "displayName": "Imported draft", "description": "Created through the actual remote import CLI.", "visibility": "private", "extensions": json.RawMessage(extensions)}, "representations": []any{map[string]any{"role": "primary", "mediaType": "application/vnd.open-agentic-catalog.agent+json", "dataFile": "agent.json"}}}}}
	var secondNamespace catalog.Namespace
	decodeProcessJSON(t, f.request(ctx, address, "POST", "/api/v1/namespaces", "local-admin", `{"slug":"import-second","displayName":"Second import namespace"}`, 201, map[string]string{"Idempotency-Key": "import-second-namespace"}), &secondNamespace)
	second := map[string]any{"key": "process-dependent", "namespaceId": secondNamespace.ID, "canonicalIdentifier": "custom:process-dependent", "kind": "agent", "version": "1.0.0", "listing": map[string]any{"publisherId": publisher.ID, "displayName": "Dependent draft", "description": "References the earlier draft in another namespace.", "visibility": "private", "relationships": []catalog.Relationship{{Type: "depends_on", TargetIdentifier: identifier, TargetVersion: version}}}, "representations": []any{map[string]any{"role": "primary", "mediaType": "application/vnd.open-agentic-catalog.agent+json", "dataFile": "agent.json"}}}
	resources, ok := manifest["resources"].([]any)
	if !ok {
		t.Fatal("import fixture resources are not a sequence")
	}
	resources = append(resources, second)
	manifest["resources"] = resources
	// UUID inputs permit uppercase hexadecimal. The client must bind replies
	// to the canonical spelling returned by PostgreSQL and the API.
	for _, item := range resources {
		resource, ok := item.(map[string]any)
		if !ok {
			t.Fatal("import fixture item is not an object")
		}
		namespaceID, ok := resource["namespaceId"].(string)
		if !ok {
			t.Fatal("import fixture namespace is not a UUID string")
		}
		resource["namespaceId"] = strings.ToUpper(namespaceID)
		listing, ok := resource["listing"].(map[string]any)
		if !ok {
			t.Fatal("import fixture listing is not an object")
		}
		listing["publisherId"] = strings.ToUpper(publisher.ID)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filename, raw, 0600); err != nil {
		t.Fatal(err)
	}
	counts := func() []int {
		t.Helper()
		result := make([]int, 10)
		if err := owner.QueryRow(ctx, `SELECT (SELECT count(*) FROM resources),(SELECT count(*) FROM resource_versions),(SELECT count(*) FROM resource_version_revisions),(SELECT count(*) FROM evidence_sets),(SELECT count(*) FROM catalog_listings),(SELECT count(*) FROM listing_revisions),(SELECT count(*) FROM audit_events WHERE action IN ('resource.created','resource_version.created','catalog_listing.created','catalog_listing.revised')),(SELECT count(*) FROM outbox_events),(SELECT count(*) FROM review_submissions)+(SELECT count(*) FROM listing_releases)+(SELECT count(*) FROM publication_snapshots),(SELECT count(*) FROM audit_events WHERE action='evidence.version_revision.read')`).Scan(&result[0], &result[1], &result[2], &result[3], &result[4], &result[5], &result[6], &result[7], &result[8], &result[9]); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := counts()
	run := func(action string, wantExit int) report {
		t.Helper()
		command := f.command(ctx, "catalogctl", dsn, "import", action, "--file", filename, "--state-file", state, "--base-url", proxy.URL)
		command.Env = append(command.Env, "CATALOG_REMOTE_TOKEN=local-contributor")
		var stdout, stderr bytes.Buffer
		command.Stdout = &stdout
		command.Stderr = &stderr
		err := command.Run()
		f.counters(command)
		if command.ProcessState == nil || command.ProcessState.ExitCode() != wantExit {
			t.Fatalf("import %s exit: %v stderr=%s stdout=%s", action, err, &stderr, &stdout)
		}
		if wantExit == 0 && stderr.Len() != 0 || wantExit != 0 && (stderr.Len() == 0 || strings.Count(stderr.String(), "\n") != 1) {
			t.Fatal("import diagnostic/output contract failed")
		}
		var value report
		decodeProcessJSON(t, stdout.Bytes(), &value)
		if len(value.Items) != 2 {
			t.Fatal("missing import item reports")
		}

		if bytes.Contains(stdout.Bytes(), []byte("local-contributor")) || bytes.Contains(stdout.Bytes(), []byte("Owned CLI import")) {
			t.Fatal("import output exposed credentials/native body")
		}
		return value
	}
	first := run("apply", 1)
	if first.Disposition != "uncertain" || len(first.Items[1].Steps) != 4 || first.Items[1].Steps[3].State != "uncertain" || first.Items[0].Disposition != "complete" || first.CurrentKey != "process-dependent" {
		t.Fatal("committed response loss not classified uncertain")
	}
	completed := run("resume", 0)
	if completed.Disposition != "complete" || completed.Items[1].Steps[3].Key != first.Items[1].Steps[3].Key || completed.Items[1].Steps[3].Ack.ID == "" {
		t.Fatal("durable cross-process replay failed")
	}
	for index := range completed.Items {
		for stepIndex, step := range completed.Items[index].Steps {
			if step.Key != first.Items[index].Steps[stepIndex].Key {
				t.Fatal("batch resume changed an original command key")
			}
		}
	}
	mu.Lock()
	commands := initialCommands
	mu.Unlock()
	if len(commands) != 2 || commands[0].key != completed.Items[0].Steps[0].Key || commands[1].key != completed.Items[0].Steps[1].Key {
		t.Fatal("missing original Unicode resource/version commands")
	}
	for _, command := range commands {
		replayed := f.request(ctx, peer, "POST", command.path, "local-contributor", string(command.body), 201, map[string]string{"Idempotency-Key": command.key})
		if !bytes.Equal(replayed, command.reply) {
			t.Fatal("PostgreSQL replay changed the original Unicode creation response")
		}
	}
	relationshipJSON, err := json.Marshal([]catalog.Relationship{{Type: "depends_on", TargetIdentifier: identifier, TargetVersion: version}})
	if err != nil {
		t.Fatal(err)
	}
	var relationshipOK bool
	if err := owner.QueryRow(ctx, `SELECT relationships = $3::jsonb FROM listing_revisions WHERE tenant_id=$1 AND id=$2`, tenant, completed.Items[1].Steps[3].Ack.ID, relationshipJSON).Scan(&relationshipOK); err != nil || !relationshipOK {
		t.Fatal("cross-namespace relationship not retained", err)
	}
	var resource catalog.Resource
	decodeProcessJSON(t, f.request(ctx, peer, "GET", "/api/v1/resources/"+completed.Items[0].Steps[0].Ack.ID, "local-contributor", "", 200), &resource)
	var retainedVersion catalog.ResourceVersion
	decodeProcessJSON(t, f.request(ctx, peer, "GET", "/api/v1/resource-versions/"+completed.Items[0].Steps[1].Ack.ID, "local-contributor", "", 200), &retainedVersion)
	if resource.CanonicalIdentifier != identifier || retainedVersion.Version != version {
		t.Fatal("Unicode identity changed during PostgreSQL readback")
	}
	after := counts()
	for i, want := range []int{2, 2, 2, 2, 2, 2, 8, 8, 0} {
		if after[i]-before[i] != want {
			t.Fatalf("import retained effect %d: delta=%d want=%d", i, after[i]-before[i], want)
		}
	}
	if after[9] <= before[9] {
		t.Fatal("exact private revision reads missing audit")
	}
	listingRevision := completed.Items[0].Steps[3].Ack.ID
	var numericOK bool
	if err := owner.QueryRow(ctx, `SELECT extensions->'integer'='9007199254740993'::jsonb AND extensions->'decimal'='0.123456789012345678901'::jsonb AND extensions->'exponent'='1e999'::jsonb AND extensions->'ordinary'='{"null":null,"boolean":true,"array":[1.20,"value"]}'::jsonb FROM listing_revisions WHERE tenant_id=$1 AND id=$2`, tenant, listingRevision).Scan(&numericOK); err != nil || !numericOK {
		var retained string
		_ = owner.QueryRow(ctx, "SELECT extensions::text FROM listing_revisions WHERE tenant_id=$1 AND id=$2", tenant, listingRevision).Scan(&retained)
		t.Fatalf("API/PostgreSQL extension numbers changed: %v retained=%s", err, retained)
	}
	retained := f.request(ctx, peer, "GET", "/api/v1/listing-revisions/"+listingRevision, "local-contributor", "", 200)
	if !bytes.Contains(retained, []byte("9007199254740993")) || !bytes.Contains(retained, []byte("0.123456789012345678901")) {
		t.Fatal("listing readback rounded numeric extensions")
	}
	var retainedRevision catalog.ListingRevision
	if err := json.Unmarshal(retained, &retainedRevision); err != nil {
		t.Fatal(err)
	}
	// HTTP omits empty optional collections. Restore the fixture's actual SQL
	// collections before checking the domain digest, as a repository read does.
	var relationships []byte
	if err := owner.QueryRow(ctx, "SELECT tags,categories,representative_queries,relationships FROM listing_revisions WHERE tenant_id=$1 AND id=$2", tenant, listingRevision).Scan(&retainedRevision.Tags, &retainedRevision.Categories, &retainedRevision.RepresentativeQueries, &relationships); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(relationships, &retainedRevision.Relationships); err != nil {
		t.Fatal(err)
	}
	if err := catalog.ValidateListingRevisionDigest(retainedRevision); err != nil {
		t.Fatalf("retained extension normalization broke listing integrity: %v", err)
	}
	for range 2 {
		if run("resume", 0).Disposition != "complete" {
			t.Fatal("complete rerun failed")
		}
	}
	rerun := counts()
	for i := range 9 {
		if rerun[i] != after[i] {
			t.Fatalf("complete rerun changed retained mutation effect %d", i)
		}
	}
	mu.Lock()
	calls := postCalls
	mu.Unlock()
	if calls != 9 {
		t.Fatalf("expected eight creations and one exact replay, got %d", calls)
	}
	// A separate import never adopts or updates an existing canonical identity.
	state = filepath.Join(dir, "private", "collision.json")
	collision := run("apply", 1)
	if collision.Disposition != "failed" || collision.Items[0].Steps[0].State != "failed" {
		t.Fatal("canonical collision not refused")
	}
	collisionCounts := counts()
	for i := range 9 {
		if collisionCounts[i] != rerun[i] {
			t.Fatal("canonical collision changed domain/audit/outbox effects")
		}
	}
	// Decoder guards must remain strict beside arbitrary extension JSON.
	validMetadata, err := json.Marshal(map[string]string{"versionRevisionId": completed.Items[0].Steps[1].Ack.RevisionID, "publisherId": publisher.ID, "displayName": "Imported draft", "description": "Created through the actual remote import CLI.", "visibility": "private"})
	if err != nil {
		t.Fatal(err)
	}
	for index, badField := range []string{`"Extensions":{}`, `"extensions":null`, `"extensions":{"x":1,"x":2}`, `"extensions":{"x":"\ud800"}`, `"extensions":{"x":1e131072}`, `"extensions":{"x":1e999999999999}`} {
		bad := strings.TrimSuffix(string(validMetadata), "}") + "," + badField + "}"
		raw := f.request(ctx, address, "POST", "/api/v1/catalog-listings/"+completed.Items[0].Steps[2].Ack.ID+"/revisions", "local-contributor", bad, 400, map[string]string{"If-Match": `"rev-2"`, "Idempotency-Key": "invalid-import-control-" + strconv.Itoa(index)})
		var problem struct {
			Code catalog.ErrorCode `json:"code"`
		}
		decodeProcessJSON(t, raw, &problem)
		if problem.Code != catalog.CodeInvalidArgument {
			t.Fatal("invalid listing metadata did not report an input error")
		}
	}
	refusalCounts := counts()
	for i := range 9 {
		if refusalCounts[i] != collisionCounts[i] {
			t.Fatal("invalid controls changed retained mutation effects")
		}
	}
	qualifyProcessExport(ctx, t, f, owner, dsn, address, tenant, namespace, publisher.ID, completed.Items[0].Steps[2].Ack.ID, completed.Items[0].Steps[1].Ack.RevisionID, version)
	qualifyProcessDraftImport(ctx, t, f, owner, address, peer, tenant, namespace, publisher.ID)
	qualifyProcessImportSignals(ctx, t, f, owner, address, peer, tenant, publisher.ID)
}
