//go:build functional && integration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdk "github.com/open-agentic-catalog/open-agentic-catalog/sdk/go"
)

// This independent executable double exercises the actual Go/Python process
// boundary. It does not import MCP Scanner or claim native YARA qualification.
const scannerDouble = `import argparse,hashlib,json,os,pathlib,sys,time
here=pathlib.Path(__file__).resolve().parent
identity=json.loads((here/"identity.json").read_text())
mode=(here/"mode").read_text().strip()
assert not any(name in os.environ for name in ("OPENAI_API_KEY","CATALOG_TEST_DATABASE_DSN","PGPASSWORD","HTTP_PROXY"))
if sys.argv[1]=="identity":
 print(json.dumps(identity)); raise SystemExit(0)
p=argparse.ArgumentParser()
for flag in ("input","output","source","captured-at"):p.add_argument("--"+flag,required=True)
a=p.parse_args(sys.argv[2:])
content=pathlib.Path(a.input).read_bytes();tools=json.loads(content)["tools"]
(here/"started").write_text("started")
if mode=="hold":time.sleep(90)
count=int((here/"calls").read_text())+1;(here/"calls").write_text(str(count))
results=[{"tool_name":t["name"],"status":"completed","analyzers":["YARA"],"analyzer_errors":[],"findings":[]} for t in tools]
status="completed" if tools else "skipped"
if "nextCursor" in json.loads(content):status="partial"
report={"schemaVersion":1,"status":status,"capture":{"inputSha256":hashlib.sha256(content).hexdigest(),"source":a.source,"capturedAt":a.captured_at,"hasNextCursor":"nextCursor" in json.loads(content),"scope":"supplied_page"},"callerContext":{},"contextSha256":hashlib.sha256(b"{}").hexdigest(),"scanner":{"name":"cisco-ai-mcp-scanner","version":identity["version"],"imageId":identity["imageId"]},"rulesSha256":identity["rulesSha256"],"configurationSha256":identity["configurationSha256"],"plannedTools":len(tools),"completedTools":len(tools),"cleanup":"passed","results":results}
if mode=="mismatch":report["capture"]["inputSha256"]="0"*64
if mode=="failed":report.update(status="failed",error="synthetic_failure")
pathlib.Path(a.output).write_text(json.dumps(report));os.chmod(a.output,0o600)
raise SystemExit(0 if report["status"]=="completed" else 2)
`

func TestReceiverPostgresProcessAndRecovery(t *testing.T) {
	dsn, binary, python := os.Getenv("CATALOG_TEST_DATABASE_DSN"), os.Getenv("MCP_SCANNER_RECEIVER_BINARY"), os.Getenv("MCP_SCANNER_TEST_PYTHON")
	if dsn == "" || binary == "" || python == "" {
		t.Fatal("select make test-mcp-scanner-receiver with owned prerequisites")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	owner, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("owned PostgreSQL unavailable")
	}
	defer owner.Close()
	nonce := make([]byte, 8)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	name := "mcp_receiver_" + hex.EncodeToString(nonce)
	password := hex.EncodeToString(nonce) + "-receiver-only"
	var quotedPassword string
	if owner.QueryRow(ctx, "SELECT quote_literal($1)", password).Scan(&quotedPassword) != nil {
		t.Fatal("password quote")
	}
	if _, err = owner.Exec(ctx, "CREATE ROLE "+pgx.Identifier{name}.Sanitize()+" LOGIN PASSWORD "+quotedPassword); err != nil {
		t.Fatal("owned receiver role")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := owner.Exec(cleanup, "DROP ROLE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Error("owned receiver role cleanup failed")
		}
	}()
	if _, err = owner.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" OWNER "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal("owned receiver database")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := owner.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Error("owned receiver database cleanup failed")
		}
	}()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("owned DSN")
	}
	u.Path = "/" + name
	u.User = url.UserPassword(name, password)
	receiverDSN := u.String()
	pool, err := pgxpool.New(ctx, receiverDSN)
	if err != nil {
		t.Fatal("receiver pool")
	}
	defer pool.Close()
	const fixtureCapacity = 11
	s := &postgresStore{pool: pool, capacity: fixtureCapacity, retention: time.Minute}
	var invokes atomic.Int32
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "owned-runtime", Version: "1"}, nil)
	mcpServer.AddTool(&mcp.Tool{Name: "sum", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		invokes.Add(1)
		return &mcp.CallToolResult{}, nil
	})
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	var contacts atomic.Int32
	runtime := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer runtime-only" {
			t.Error("Catalog credential reached MCP")
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		contacts.Add(1)
		mcpHandler.ServeHTTP(w, r)
	}))
	defer runtime.Close()
	dir := t.TempDir()
	runtimeToken := writePrivate(t, "runtime-token", []byte("runtime-only"))
	apiToken := writePrivate(t, "api-token", []byte("catalog-only"))
	mcpCA := writePrivate(t, "mcp-ca", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: runtime.Certificate().Raw}))
	entry := testMapping
	entry.Endpoint = runtime.URL + "/mcp"
	entry.CAFile = mcpCA
	entry.TokenFile = runtimeToken
	config := configuration{Resources: []mapping{entry}}
	mappingBytes := fixtureJSON(config)
	configFile := writePrivate(t, "mapping.json", mappingBytes)
	var mu sync.Mutex
	current := recordFixture()
	(*current.VersionRevision.Representations[0].Data)["remotes"] = []any{map[string]any{"type": "streamable-http", "url": entry.Endpoint}}
	status := 200
	listed := false
	browseUnavailable := false
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer catalog-only" {
			t.Error("unexpected Catalog request/credential")
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/webhooks/reconciliation":
			_, _ = io.WriteString(w, `{"pendingEvents":0,"pendingEventLimit":1000,"capacityLossCount":0,"deliveryLossCount":0}`)
		case "/api/v1/catalog/resources":
			if browseUnavailable {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"title":"Unavailable","status":503}`)
				return
			}
			if r.URL.Query().Get("namespaceId") != namespace || r.URL.Query().Get("kind") != "mcp_server" || r.URL.Query().Get("limit") != "32" {
				t.Error("unbounded Catalog traversal")
			}
			page := sdk.CatalogRecordPage{Items: []sdk.CatalogSummary{}, Total: 0}
			if listed {
				page.Items = append(page.Items, sdk.CatalogSummary{Source: "local", Resource: sdk.CatalogResourceSummary{Id: current.Resource.Id, NamespaceId: current.Resource.NamespaceId, TenantId: current.Resource.TenantId, Kind: "mcp_server"}, Release: sdk.CatalogReleaseSummary{Id: current.Release.Id}})
				page.Total = 1
			}
			_ = json.NewEncoder(w).Encode(page)
		case "/api/v1/catalog/resources/" + current.Release.Id.String():
			w.WriteHeader(status)
			if status == 200 {
				_ = json.NewEncoder(w).Encode(current)
			} else {
				_ = json.NewEncoder(w).Encode(map[string]int{"status": status})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	caFile := writePrivate(t, "api-ca", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: api.Certificate().Raw}))
	private, err := x509.MarshalPKCS8PrivateKey(api.TLS.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := writePrivate(t, "tls-key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
	signerFile := writePrivate(t, "signer.json", []byte(`{"secret":"`+secret+`","version":1}`))
	dsnFile := writePrivate(t, "receiver.dsn", []byte(receiverDSN))
	script := filepath.Join(dir, "scanner-double.py")
	if os.WriteFile(script, []byte(scannerDouble), 0600) != nil {
		t.Fatal("scanner double")
	}
	identityRaw := fixtureJSON(testIdentity)
	for name, raw := range map[string][]byte{"identity.json": identityRaw, "mode": []byte("normal"), "calls": []byte("0")} {
		if os.WriteFile(filepath.Join(dir, name), raw, 0600) != nil {
			t.Fatal("double inputs")
		}
	}
	setMode := func(mode string) {
		t.Helper()
		if os.WriteFile(filepath.Join(dir, "mode"), []byte(mode), 0600) != nil {
			t.Fatal("double mode")
		}
	}
	base := []string{"--origin", api.URL, "--tenant", tenant, "--config-file", configFile, "--database-file", dsnFile, "--token-file", apiToken, "--catalog-ca-file", caFile, "--scanner-python", python, "--scanner-runner", script, "--reports-dir", filepath.Join(dir, "reports"), "--capacity", strconv.Itoa(fixtureCapacity), "--retention", "1m"}
	command := func(action string, extra ...string) ([]byte, error) {
		args := append([]string{action}, base...)
		args = append(args, extra...)
		// #nosec G204 G702 -- Make selects the newly built receiver and all inputs are test-owned.
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}
		raw, err := cmd.CombinedOutput()
		for _, value := range []string{password, "runtime-only", "catalog-only", secret, receiverDSN} {
			if bytes.Contains(raw, []byte(value)) {
				t.Error("private diagnostic leak")
			}
		}
		return raw, err
	}
	if _, err = command("init"); err != nil {
		t.Fatal("receiver init failed")
	}
	_, digest, err := loadConfiguration(configFile)
	if err != nil || bindSettings(ctx, pool, tenant, api.URL, digest) != nil {
		t.Fatal("receiver binding")
	}
	if bindSettings(ctx, pool, resource, api.URL, digest) == nil {
		t.Fatal("tenant binding changed")
	}
	_, unlock, err := s.processor(ctx)
	if err != nil {
		t.Fatal("processor lock unavailable")
	}
	_, secondErr := command("process")
	unlock()
	if secondErr == nil || contacts.Load() != 0 {
		t.Fatal("second processor acquired authority")
	}
	if s.Reconciled(ctx, config.Resources, map[string]string{resource: release}, [2]int64{0, 0}, time.Now()) != nil {
		t.Fatal("initial reconciliation slot")
	}
	e := Event{ID: eventID, Source: eventSource + tenant, Action: "listing.release.published", TargetID: release}
	s.capacity = 1
	for range 2 {
		if s.Accept(ctx, e, time.Now()) != nil {
			t.Fatal("durable duplicate refused")
		}
	}
	other := e
	other.ID = resource
	if !errors.Is(s.Accept(ctx, other, time.Now()), errFull) {
		t.Fatal("capacity not enforced")
	}
	s.capacity = fixtureCapacity
	for range 2 {
		if _, err = command("process"); err != nil {
			t.Fatal("accepted work did not scan")
		}
	}
	var pending int
	if pool.QueryRow(ctx, `SELECT count(*) FROM mcp_receiver_inbox WHERE completed_at IS NULL`).Scan(&pending) != nil || pending != 0 {
		t.Fatal("receipt not completed")
	}
	readObservation := func() observation {
		t.Helper()
		var current observation
		if err := pool.QueryRow(ctx, `SELECT document FROM mcp_receiver_observations WHERE resource_id=$1`, resource).Scan(&current); err != nil {
			t.Fatal("read saved observation", err)
		}
		return current
	}
	observed := readObservation()
	if observed.State != "eligible_at_check" || observed.Catalog == nil || observed.Catalog.ReleaseID != release {
		t.Fatal("incorrect saved binding")
	}
	beforeContacts := contacts.Load()
	if s.Accept(ctx, e, fixed) != nil {
		t.Fatal("duplicate accept")
	}
	if _, err = command("process"); err != nil {
		t.Fatal("empty process")
	}
	if contacts.Load() != beforeContacts {
		t.Fatal("completed event ran again")
	}
	// Reconciliation collects again but reuses exact completed analysis.
	mu.Lock()
	listed = true
	mu.Unlock()
	if _, err = command("reconcile"); err != nil {
		t.Fatal("reconcile failed")
	}
	if _, err = command("process"); err != nil {
		t.Fatal("reconciled work failed")
	}
	// #nosec G304 G703 -- Only the invocation-owned double counter is read.
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if string(calls) != "1" {
		t.Fatal("unchanged capture reran scanner")
	}
	observed = readObservation()
	if !observed.AnalysisReused || observed.Trigger == nil || observed.Trigger.Source != reconciliationSource {
		t.Fatal("reconciliation did not bind reused analysis")
	}
	// A later draft changes mutable pointers, not the selected release evidence.
	mu.Lock()
	candidate := uuid.MustParse(eventID)
	current.Version.CandidateRevisionId = &candidate
	mu.Unlock()
	if _, err = command("reconcile"); err != nil {
		t.Fatal("draft reconciliation")
	}
	if _, err = command("process"); err != nil {
		t.Fatal("draft changed selected release")
	}
	// Store completion must roll back a report when receipt ownership changes.
	var priorObservation, observationJSON []byte
	if pool.QueryRow(ctx, `SELECT document FROM mcp_receiver_observations WHERE resource_id=$1`, resource).Scan(&priorObservation) != nil {
		t.Fatal("rollback baseline unavailable")
	}
	w := &work{Event: other}
	rawReport := reportFixture(captureFixture(), testIdentity)
	o := &observation{ResourceID: resource, State: "failed", CheckedAt: fixed, ScanKey: strings.Repeat("1", 64)}
	if s.Finish(ctx, w, o, rawReport, fixed, "") == nil {
		t.Fatal("unknown receipt committed")
	}
	var exists bool
	if pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mcp_receiver_scans WHERE scan_key=$1)`, o.ScanKey).Scan(&exists) != nil || exists {
		t.Fatal("failed completion left report")
	}
	if pool.QueryRow(ctx, `SELECT document FROM mcp_receiver_observations WHERE resource_id=$1`, resource).Scan(&observationJSON) != nil || !bytes.Equal(priorObservation, observationJSON) {
		t.Fatal("failed completion changed retained observation")
	}
	// Actual HTTPS acceptance survives a receiver process restart.
	var child *exec.Cmd
	var finished chan error
	var diagnostics bytes.Buffer
	stopChild := func() {
		t.Helper()
		if child == nil {
			return
		}
		_ = child.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-finished:
			if err != nil {
				t.Error("receiver shutdown failed")
			}
		case <-time.After(15 * time.Second):
			_ = child.Process.Kill()
			if err := <-finished; err == nil {
				t.Log("owned receiver stopped before forced cleanup")
			}
			t.Error("receiver shutdown timeout")
		}
		child = nil
	}
	defer stopChild()
	start := func() string {
		t.Helper()
		l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		_ = l.Close()
		args := append([]string{"serve"}, base...)
		args = append(args, "--listen", addr, "--tls-cert", caFile, "--tls-key", keyFile, "--signer-file", signerFile, "--poll-interval", "1s", "--reconcile-interval", "24h")
		// #nosec G204 G702 -- The executable and every path belong to this fixture.
		child = exec.CommandContext(ctx, binary, args...)
		child.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}
		diagnostics.Reset()
		child.Stdout = &diagnostics
		child.Stderr = &diagnostics
		if child.Start() != nil {
			t.Fatal("receiver start")
		}
		finished = make(chan error, 1)
		go func(cmd *exec.Cmd) { finished <- cmd.Wait() }(child)
		origin := "https://" + addr
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/healthz", nil)
			response, err := api.Client().Do(req)
			if err == nil {
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return origin
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("receiver readiness timeout")
		return ""
	}
	// Disable startup discovery so this report must come from a signed receipt.
	mu.Lock()
	listed = false
	mu.Unlock()
	// Change scanner identity to ensure this attempt cannot reuse the earlier scan.
	changed := testIdentity
	changed.ImageID = "sha256:" + strings.Repeat("d", 64)
	identityRaw = fixtureJSON(changed)
	if os.WriteFile(filepath.Join(dir, "identity.json"), identityRaw, 0600) != nil {
		t.Fatal("changed double identity")
	}
	selectedScan := func(wantCount int, wantImage string) (string, []byte) {
		t.Helper()
		var key string
		var raw []byte
		var count int
		if err := pool.QueryRow(ctx, `SELECT s.scan_key,s.report,(SELECT count(*) FROM mcp_receiver_scans)
			FROM mcp_receiver_scans s JOIN mcp_receiver_observations o USING (scan_key) WHERE o.resource_id=$1`, resource).Scan(&key, &raw, &count); err != nil {
			t.Fatal("read retained scan", err)
		}
		var report struct {
			Scanner scannerIdentity `json:"scanner"`
		}
		if err := json.Unmarshal(raw, &report); err != nil || count != wantCount || report.Scanner.ImageID != wantImage {
			t.Fatalf("expected %d retained scans with selected image %q, got %d and %q (decode error %v)", wantCount, wantImage, count, report.Scanner.ImageID, err)
		}
		return key, raw
	}
	checkCompleted := func(id, wantOutcome string) {
		t.Helper()
		var completed bool
		var outcome string
		if err := pool.QueryRow(ctx, `SELECT completed_at IS NOT NULL,outcome FROM mcp_receiver_inbox WHERE source=$1 AND id=$2`, e.Source, id).Scan(&completed, &outcome); err != nil || !completed || outcome != wantOutcome {
			t.Fatalf("receipt completion=%t outcome=%q err=%v", completed, outcome, err)
		}
	}
	previousKey, previousScan := selectedScan(1, testIdentity.ImageID)
	atCapacity := e
	atCapacity.ID = uuid.NewString()
	if err := s.Accept(ctx, atCapacity, time.Now()); err != nil {
		t.Fatal("admit distinct receipt before limiting report storage", err)
	}
	if _, err := command("process", "--capacity", "1"); err != nil {
		t.Fatal("record terminal report-capacity outcome", err)
	}
	checkCompleted(atCapacity.ID, "receiver_report_capacity")
	protectedKey, protectedScan := selectedScan(1, testIdentity.ImageID)
	if protectedKey != previousKey || !bytes.Equal(protectedScan, previousScan) {
		t.Fatal("full storage replaced the selected report")
	}
	capacityObservation := readObservation()
	if capacityObservation.State != "failed" || capacityObservation.Failure != "receiver_report_capacity" || capacityObservation.Trigger == nil || *capacityObservation.Trigger != atCapacity || capacityObservation.AttemptedReleaseID != release {
		t.Fatal("capacity failure did not identify the new attempt")
	}
	if capacityObservation.Scanner == nil || capacityObservation.Scanner.ImageID != testIdentity.ImageID {
		t.Fatal("capacity failure changed the previous scan identity")
	}
	capacityFile, err := privateFile(filepath.Join(dir, "reports", resource+".json"), reportLimit)
	var renderedFailure observation
	if err != nil || json.Unmarshal(capacityFile, &renderedFailure) != nil || renderedFailure.Failure != "receiver_report_capacity" || renderedFailure.ScanDigest != sha(previousScan) {
		t.Fatal("rendered capacity failure did not retain the previous scan digest")
	}
	// A fresh signed receipt uses the ordinary capacity and survives a restart.
	setMode("hold")
	_ = os.Remove(filepath.Join(dir, "started"))
	origin := start()
	signed := eventBytesFor("listing.release.published", namespace, release)
	headers := signatureAt(signed, 1, secret, time.Now())
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/webhook", bytes.NewReader(signed))
	request.Header = headers
	response, err := api.Client().Do(request)
	if err != nil || response.StatusCode != http.StatusNoContent {
		t.Fatal("HTTPS durable acceptance failed")
	}
	_ = response.Body.Close()
	deadline := time.Now().Add(8 * time.Second)
	started := false
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "started")); err == nil {
			started = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !started {
		t.Fatal("owned scan process did not start")
	}
	stopChild()
	if pool.QueryRow(ctx, `SELECT count(*) FROM mcp_receiver_inbox WHERE id=$1 AND completed_at IS NULL`, namespace).Scan(&pending) != nil || pending != 1 {
		t.Fatal("interruption lost pending receipt")
	}
	setMode("normal")
	_ = start()
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM mcp_receiver_inbox WHERE id=$1 AND completed_at IS NULL`, namespace).Scan(&pending)
		if pending == 0 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	stopChild()
	if pending != 0 {
		t.Fatal("restart did not finish pending work")
	}
	observed = readObservation()
	if observed.State != "eligible_at_check" || observed.Scanner == nil || observed.Scanner.ImageID != changed.ImageID || observed.Trigger == nil || observed.Trigger.ID != namespace {
		t.Fatal("restart completed without the expected report")
	}
	checkCompleted(namespace, "")
	newKey, newScan := selectedScan(2, changed.ImageID)
	if newKey == previousKey || bytes.Equal(newScan, previousScan) {
		t.Fatal("restored capacity did not commit the distinct scan for the new receipt")
	}
	for _, mode := range []string{"mismatch", "failed", "normal"} {
		setMode(mode)
		changed.ImageID = "sha256:" + strings.Repeat("f", 64)
		if os.WriteFile(filepath.Join(dir, "identity.json"), fixtureJSON(changed), 0600) != nil {
			t.Fatal("failure-control identity")
		}
		attempt := e
		attempt.ID = uuid.NewString()
		if s.Accept(ctx, attempt, time.Now()) != nil {
			t.Fatal("failure-control receipt")
		}
		if _, err = command("process"); err != nil {
			t.Fatal("failure-control processing")
		}
		observed = readObservation()
		if mode == "normal" {
			if observed.State != "eligible_at_check" || observed.AnalysisReused {
				t.Fatal("failed report shadowed successful retry")
			}
		} else if observed.State != "failed" {
			t.Fatal("scanner failure claimed completed analysis")
		}
	}
	readRendered := func() (observation, []byte) {
		t.Helper()
		rendered, err := privateFile(filepath.Join(dir, "reports", resource+".json"), reportLimit)
		var observed observation
		if err != nil || json.Unmarshal(rendered, &observed) != nil || observed.ScanFile != resource+"-scan.json" {
			t.Fatal("rendered observation binding")
		}
		native, err := privateFile(filepath.Join(dir, "reports", observed.ScanFile), reportLimit)
		if err != nil || sha(native) != observed.ScanDigest {
			t.Fatal("rendered scan checksum")
		}
		return observed, native
	}
	readRendered()
	acceptSigned := func(t *testing.T, action, id, target string) {
		t.Helper()
		event := eventBytesFor(action, id, target)
		request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/webhook", bytes.NewReader(event))
		request.Header = signatureAt(event, 1, secret, time.Now())
		reply := httptest.NewRecorder()
		app := application{store: s, tenant: tenant, signerFile: signerFile, slots: make(chan struct{}, 1), now: time.Now}
		app.ServeHTTP(reply, request)
		if reply.Code != http.StatusNoContent {
			t.Fatalf("signed %s returned HTTP %d", action, reply.Code)
		}
	}
	t.Run("endpoint test leaves release observations unchanged", func(t *testing.T) {
		before := fixtureJSON(readObservation())
		probeID := uuid.NewString()
		acceptSigned(t, "webhook.test", probeID, release)
		if _, err := command("process"); err != nil {
			t.Fatal(err)
		}
		checkCompleted(probeID, "test_accepted")
		if !bytes.Equal(before, fixtureJSON(readObservation())) {
			t.Fatal("endpoint test changed the release observation with the same target ID")
		}
	})
	t.Run("pending reconciliation follows complete Catalog selection", func(t *testing.T) {
		run := func(action string) {
			t.Helper()
			if raw, err := command(action); err != nil {
				t.Fatalf("%s: %v %s", action, err, raw)
			}
		}
		mu.Lock()
		listed = true
		mu.Unlock()
		run("reconcile")
		old, err := s.Next(ctx, time.Now())
		if err != nil || old.Event.Source != reconciliationSource || old.Event.TargetID != release || old.Attempts != 0 {
			t.Fatal("original reconciliation work", old, err)
		}
		due := time.Now().Add(time.Hour).Truncate(time.Microsecond)
		if err := s.Retry(ctx, old, due, "catalog_unavailable"); err != nil {
			t.Fatal(err)
		}
		run("reconcile")
		mu.Lock()
		browseUnavailable = true
		mu.Unlock()
		if _, err := command("reconcile"); err == nil {
			t.Fatal("unavailable traversal accepted")
		}
		var failure string
		if err := pool.QueryRow(ctx, "SELECT reconciliation_failure FROM mcp_receiver_settings WHERE singleton").Scan(&failure); err != nil || failure != "catalog_browse_failed" {
			t.Fatal("failed traversal did not record the browse refusal", failure, err)
		}
		if _, err := s.Next(ctx, due.Add(-time.Microsecond)); !errors.Is(err, errEmpty) {
			t.Fatal("unchanged or failed reconciliation reset the retry delay", err)
		}
		if w, err := s.Next(ctx, due); err != nil || w.Event != old.Event || w.Attempts != 1 {
			t.Fatal("unchanged or failed reconciliation lost the pending retry", w, err)
		}

		replacement := uuid.New()
		mu.Lock()
		browseUnavailable = false
		current.Release.Id, current.Listing.CurrentReleaseId = replacement, &replacement
		mu.Unlock()
		run("reconcile")
		if err := s.Retry(ctx, old, due, "catalog_unavailable"); err == nil || err.Error() != "pending_receipt_changed" {
			t.Fatal("old retry changed replacement work", err)
		}
		if err := s.Finish(ctx, old, nil, nil, time.Now(), "catalog_record_unavailable"); err == nil || err.Error() != "pending_receipt_changed" {
			t.Fatal("old completion changed replacement work", err)
		}
		if w, err := s.Next(ctx, time.Now()); err != nil || w.Event.TargetID != replacement.String() || w.Attempts != 0 {
			t.Fatal("new release did not receive a fresh attempt", w, err)
		}
		run("process")
		saved := readObservation()
		exported, scan := readRendered()
		for _, got := range []observation{saved, exported} {
			if got.State != "eligible_at_check" || got.Catalog == nil || got.Catalog.ReleaseID != replacement.String() || got.Trigger == nil || got.Trigger.Source != reconciliationSource || got.Trigger.TargetID != replacement.String() {
				t.Fatal("processing selected the wrong release", got.State, got.Trigger)
			}
		}

		run("reconcile")
		// Accept a signed notification with the same id as the synthetic slot.
		// Only the synthetic work belongs to the subsequent absent selection.
		acceptSigned(t, "listing.release.published", resource, replacement.String())
		mu.Lock()
		listed, status = false, http.StatusNotFound
		mu.Unlock()
		run("reconcile")
		if w, err := s.Next(ctx, time.Now()); err != nil || w.Event.Source != e.Source || w.Event.ID != resource {
			t.Fatal("absent selection should leave only the signed notification", w, err)
		}
		before := contacts.Load()
		run("process")
		checkCompleted(resource, "catalog_record_unavailable")
		if contacts.Load() != before {
			t.Fatal("delayed publication contacted the withdrawn release")
		}
		if _, err := s.Next(ctx, due); !errors.Is(err, errEmpty) {
			t.Fatal("obsolete work remained after disappearance", err)
		}
		absent, retained := readRendered()
		if final := readObservation(); final.State != "not_listed" || final.ScanKey != saved.ScanKey || absent.State != "not_listed" || absent.ScanDigest != exported.ScanDigest || !bytes.Equal(retained, scan) {
			t.Fatal("absent selection lost its state or original scan")
		}
	})
	mu.Lock()
	status = http.StatusOK
	originalRelease := uuid.MustParse(release)
	current.Release.Id, current.Listing.CurrentReleaseId = originalRelease, &originalRelease
	mu.Unlock()
	// A successful metadata read with no tools remains an explicit skipped scan.
	mcpServer.RemoveTools("sum")
	empty := e
	empty.ID = uuid.NewString()
	if s.Accept(ctx, empty, time.Now()) != nil {
		t.Fatal("empty-capture receipt")
	}
	if _, err = command("process"); err != nil {
		t.Fatal("empty-capture processing")
	}
	observed = readObservation()
	if observed.State != "skipped" || observed.Trigger == nil || observed.Trigger.ID != empty.ID {
		t.Fatal("empty capture did not retain its skipped outcome")
	}
	// Denied and mismatched mappings do not initiate another MCP session.
	mu.Lock()
	status = 403
	mu.Unlock()
	beforeContacts = contacts.Load()
	other.ID = eventID
	other.ID = strings.Replace(other.ID, "000005", "000015", 1)
	if s.Accept(ctx, other, time.Now()) != nil {
		t.Fatal("denial event")
	}
	if _, err = command("process"); err != nil {
		t.Fatal("denial handling")
	}
	if contacts.Load() != beforeContacts {
		t.Fatal("denial contacted MCP")
	}
	if denied := readObservation(); denied.State != "historical" || denied.Failure != "catalog_access_denied" {
		t.Fatalf("revoked access left state %q with failure %q", denied.State, denied.Failure)
	}
	mu.Lock()
	status = 200
	(*current.VersionRevision.Representations[0].Data)["remotes"] = []any{map[string]any{"type": "streamable-http", "url": "https://127.0.0.1:1/unmapped"}}
	mu.Unlock()
	other.ID = uuid.NewString()
	if s.Accept(ctx, other, time.Now()) != nil {
		t.Fatal("mapping event")
	}
	if _, err = command("process"); err != nil {
		t.Fatal("mapping handling")
	}
	if contacts.Load() != beforeContacts {
		t.Fatal("mismatched endpoint contacted")
	}
	if invokes.Load() != 0 {
		t.Fatal("tool invoked")
	}
	for _, value := range []string{password, "runtime-only", "catalog-only", secret, receiverDSN} {
		if bytes.Contains(diagnostics.Bytes(), []byte(value)) {
			t.Error("receiver diagnostic leaked credentials")
		}
	}
}
