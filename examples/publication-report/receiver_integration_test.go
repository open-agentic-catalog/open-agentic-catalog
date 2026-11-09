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
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReceiverPostgresAndProcess(t *testing.T) {
	dsn, executable := os.Getenv("CATALOG_TEST_DATABASE_DSN"), os.Getenv("PUBLICATION_REPORT_BINARY")
	if dsn == "" || executable == "" {
		t.Fatal("select make test-webhook-report with its owned database and executable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	owner, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("owned database unavailable")
	}
	defer owner.Close()
	nonce := make([]byte, 8)
	if _, err = rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	schemaName := "receiver_" + hex.EncodeToString(nonce)
	if _, err = owner.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schemaName}.Sanitize()); err != nil {
		t.Fatal("create owned schema")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := owner.Exec(cleanup, "DROP SCHEMA "+pgx.Identifier{schemaName}.Sanitize()+" CASCADE"); err != nil {
			t.Error("owned receiver schema cleanup failed")
		}
	}()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schemaName)
	u.RawQuery = q.Encode()
	receiverDSN := u.String()
	pool, err := pgxpool.New(ctx, receiverDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := &postgresStore{pool: pool, capacity: 1, retention: time.Minute}

	var mu sync.Mutex
	status, body := 200, activePage(9)
	lossStatus, lossBody := 200, `{"pendingEvents":0,"pendingEventLimit":1000,"capacityLossCount":0,"deliveryLossCount":0}`
	expectedBearer, allowPrevious := "Bearer receiver-reader", false
	const replacementToken = "rotated-reader-fixture"
	replacementReads := 0
	reads, lossReads := 0, 0
	catalog := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != http.MethodGet {
			t.Error("wrong SDK method")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Header.Get("Authorization") != expectedBearer {
			if !allowPrevious || r.Header.Get("Authorization") != "Bearer receiver-reader" {
				t.Error("unexpected SDK credential")
			}
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if expectedBearer == "Bearer "+replacementToken {
			replacementReads++
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/publication/snapshots":
			reads++
			if r.URL.Query().Get("host") != testHost || r.URL.Query().Get("state") != "active" || r.URL.Query().Get("limit") != "2" || r.URL.Query().Has("cursor") {
				t.Error("unbounded or changed SDK selection")
			}
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		case "/api/v1/webhooks/reconciliation":
			lossReads++
			w.WriteHeader(lossStatus)
			_, _ = io.WriteString(w, lossBody)
		default:
			t.Error("unexpected Catalog/resource request")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer catalog.Close()
	setAPI := func(code int, raw string) { mu.Lock(); status, body = code, raw; mu.Unlock() }
	setLoss := func(code int, raw string) { mu.Lock(); lossStatus, lossBody = code, raw; mu.Unlock() }
	counts := func() (int, int) { mu.Lock(); defer mu.Unlock(); return reads, lossReads }
	dir := t.TempDir()
	reports, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reports.Close() }()
	cert := filepath.Join(dir, "server.pem")
	key := filepath.Join(dir, "server-key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: catalog.TLS.Certificates[0].Certificate[0]})
	private, err := x509.MarshalPKCS8PrivateKey(catalog.TLS.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if os.WriteFile(cert, certPEM, 0600) != nil || os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600) != nil {
		t.Fatal("write owned TLS")
	}
	dbfile := writePrivate(t, "receiver.dsn", []byte(receiverDSN))
	token := writePrivate(t, "api-token", []byte("receiver-reader"))
	signerFile := writePrivate(t, "signer.json", []byte(`{"secret":"`+testSecret+`","version":1}`))
	output := filepath.Join(dir, "report.json")
	privateDiagnostics := []string{testSecret, "whsec_" + strings.Repeat("b", 43), "receiver-reader", replacementToken, receiverDSN, dbfile, token, signerFile, cert, key}
	base := []string{"--origin", catalog.URL, "--tenant", testTenant, "--host", testHost, "--database-file", dbfile, "--token-file", token, "--ca-file", cert, "--output", output, "--capacity", "1", "--retention", "1m"}
	runCommand := func(command string, extra ...string) ([]byte, error) {
		args := append([]string{command}, base...)
		args = append(args, extra...)
		// #nosec G204 G702 -- Make supplies the explicitly built, invocation-owned sample executable.
		child := exec.CommandContext(ctx, executable, args...)
		raw, err := child.CombinedOutput()
		for _, secret := range privateDiagnostics {
			if bytes.Contains(raw, []byte(secret)) {
				t.Error("process diagnostic disclosed a credential")
			}
		}
		return raw, err
	}
	if _, err := runCommand("init"); err != nil {
		t.Fatal("receiver init failed")
	}
	if err := s.bind(ctx, testTenant, testHost, catalog.URL); err != nil {
		t.Fatal(err)
	}
	if err := s.bind(ctx, testTenant, "other.test", catalog.URL); err == nil {
		t.Fatal("database rebound to another host")
	}
	wait := func(label string, predicate func() bool) {
		t.Helper()
		deadline := time.NewTimer(12 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			if predicate() {
				return
			}
			select {
			case <-deadline.C:
				t.Fatal(label)
			case <-ctx.Done():
				t.Fatal("owned receiver deadline")
			case <-tick.C:
			}
		}
	}
	readSaved := func() Saved {
		t.Helper()
		v, err := s.Read(ctx)
		if err != nil {
			t.Fatal("saved report unavailable")
		}
		return v
	}
	var process *exec.Cmd
	var finished chan error
	var diagnostics bytes.Buffer
	stopProcess := func() {
		t.Helper()
		if process == nil {
			return
		}
		_ = process.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-finished:
			if err != nil {
				t.Error("receiver failed graceful shutdown")
			}
		case <-time.After(12 * time.Second):
			_ = process.Process.Kill()
			if err := <-finished; err == nil {
				t.Log("owned process stopped during forced cleanup")
			}
			t.Error("receiver did not stop")
		}
		process = nil
		for _, secret := range privateDiagnostics {
			if bytes.Contains(diagnostics.Bytes(), []byte(secret)) {
				t.Error("process diagnostic disclosed a credential")
			}
		}
	}
	defer stopProcess()
	var origin string
	startProcess := func(interval ...string) {
		t.Helper()
		reserved, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := reserved.Addr().String()
		_ = reserved.Close()
		origin = "https://" + address
		args := append([]string{"serve"}, base...)
		cadence := "24h"
		poll := "1s"
		if len(interval) > 0 {
			cadence = interval[0]
		}
		if len(interval) > 1 {
			poll = interval[1]
		}
		args = append(args, "--listen", address, "--tls-cert", cert, "--tls-key", key, "--signer-file", signerFile, "--poll-interval", poll, "--reconcile-interval", cadence)
		// #nosec G204 G702 -- Make supplies the explicitly built, invocation-owned sample executable.
		process = exec.CommandContext(ctx, executable, args...)
		diagnostics.Reset()
		process.Stdout = &diagnostics
		process.Stderr = &diagnostics
		if process.Start() != nil {
			t.Fatal("receiver process start")
		}
		finished = make(chan error, 1)
		go func(child *exec.Cmd) { finished <- child.Wait() }(process)
		wait("receiver HTTPS readiness", func() bool {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/healthz", nil)
			if err != nil {
				return false
			}
			r, err := catalog.Client().Do(request)
			if err != nil {
				return false
			}
			_ = r.Body.Close()
			return r.StatusCode == http.StatusOK
		})
	}
	post := func(destination string, raw []byte, version int, secret string) (int, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, destination+"/webhook", bytes.NewReader(raw))
		if err != nil {
			return 0, err
		}
		r.Header = signatureAt(raw, version, secret, time.Now())
		reply, err := catalog.Client().Do(r)
		if err != nil {
			return 0, err
		}
		defer func() { _ = reply.Body.Close() }()
		_, _ = io.Copy(io.Discard, reply.Body)
		return reply.StatusCode, nil
	}
	pendingCount := func() int {
		var n int
		if pool.QueryRow(ctx, `SELECT count(*) FROM publication_receiver_inbox WHERE completed_at IS NULL`).Scan(&n) != nil {
			t.Fatal("inbox inspection failed")
		}
		return n
	}
	pruneReady := func() {
		if _, err := pool.Exec(ctx, `UPDATE publication_receiver_inbox SET completed_at=clock_timestamp()-interval '2 minutes' WHERE completed_at IS NOT NULL`); err != nil {
			t.Fatal("age completed receipt fixture")
		}
	}
	startProcess()
	wait("first publication report and rendered file", func() bool {
		v := readSaved()
		if v.LastSuccessful == nil || v.LastSuccessful.Active == nil || v.LastSuccessful.Active.Generation != 9 {
			return false
		}
		// The database commits before render atomically replaces the file.
		// Processor exclusion below needs both boundaries to be ready.
		raw, err := reports.ReadFile("report.json")
		if errors.Is(err, os.ErrNotExist) {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		var rendered Saved
		if err := json.Unmarshal(raw, &rendered); err != nil {
			t.Fatal("rendered publication report is invalid", err)
		}
		return rendered.LastSuccessful != nil && rendered.LastSuccessful.Active != nil && rendered.LastSuccessful.Active.Generation == 9
	})
	// Authority belongs to one live processor, while rendering is intentionally
	// read-only. A second command must fail at the processor guard, before any
	// Catalog exchange or report replacement (not at an occupied listener).
	beforeProcessor, beforeProcessorLoss := counts()
	beforeProcessorReport, err := reports.ReadFile("report.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, competing := range []string{"reconcile", "serve"} {
		extra := []string{}
		if competing == "serve" {
			extra = []string{"--listen", "127.0.0.1:0", "--tls-cert", cert, "--tls-key", key, "--signer-file", signerFile}
		}
		raw, err := runCommand(competing, extra...)
		if err == nil || !bytes.Contains(raw, []byte("processor_unavailable")) {
			t.Fatal("competing processor did not refuse at its authority guard")
		}
	}
	if _, err := runCommand("render"); err != nil {
		t.Fatal("live processor incorrectly excludes read-only render")
	}
	afterProcessor, afterProcessorLoss := counts()
	afterProcessorReport, err := reports.ReadFile("report.json")
	if err != nil || beforeProcessor != afterProcessor || beforeProcessorLoss != afterProcessorLoss || !bytes.Equal(beforeProcessorReport, afterProcessorReport) {
		t.Fatal("excluded processor or read-only render changed Catalog reads or saved report")
	}
	setAPI(503, `{"unavailable":true}`)
	raw := eventBytes("publication.activated")
	// Lose only the acknowledgement after the real receiver commits.
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
		if err != nil {
			t.Error("proxy read")
			return
		}
		forward, err := http.NewRequestWithContext(r.Context(), http.MethodPost, origin+"/webhook", bytes.NewReader(data))
		if err != nil {
			t.Error("proxy request")
			return
		}
		forward.Header = r.Header.Clone()
		reply, err := catalog.Client().Do(forward)
		if err != nil {
			t.Error("receiver forward")
			return
		}
		_ = reply.Body.Close()
		if reply.StatusCode != http.StatusNoContent {
			t.Error("acceptance control failed")
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("lost reply requires HTTP/1 connection")
			return
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			t.Error("lost reply injection")
			return
		}
		_ = connection.Close()
	}))
	if _, err := post(proxy.URL, raw, 1, testSecret); err == nil {
		t.Fatal("lost acknowledgement was not injected")
	}
	proxy.Close()
	if code, err := post(origin, raw, 1, testSecret); err != nil || code != 204 {
		t.Fatal("durable duplicate not acknowledged")
	}
	other := bytes.ReplaceAll(raw, []byte(testID), []byte("00000000-0000-4000-8000-000000000004"))
	if code, err := post(origin, other, 1, testSecret); err != nil || code != 503 {
		t.Fatal("capacity not enforced")
	}
	wait("failed refresh retained historical report", func() bool {
		v := readSaved()
		return v.RefreshFailure != "" && v.LastSuccessful.Active.Generation == 9 && pendingCount() == 1
	})
	expectedTrigger := Event{ID: testID, Source: eventSource + testTenant, Action: "publication.activated", TargetID: testTarget, Digest: "sha256:" + strings.Repeat("a", 64)}
	assertPendingTrigger := func() {
		t.Helper()
		pending, err := s.Next(ctx)
		if err != nil || pending == nil || *pending != expectedTrigger {
			t.Fatal("accepted pending event identity or trigger context changed", err)
		}
	}
	assertPendingTrigger()
	// The failure above leaves a known accepted event pending. Kill only this
	// owned process, then prove a fresh process can acquire the session lock and
	// finish that same receipt; do not delete a lock or reset retained state.
	if err := process.Process.Kill(); err != nil {
		t.Fatal("kill owned processor", err)
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("abrupt processor death unexpectedly reported success")
		}
	case <-time.After(12 * time.Second):
		t.Fatal("owned killed processor did not exit")
	}
	process = nil
	for _, secret := range privateDiagnostics {
		if bytes.Contains(diagnostics.Bytes(), []byte(secret)) {
			t.Error("abrupt processor diagnostic disclosed a credential")
		}
	}
	wait("abrupt processor's database session lock released", func() bool {
		var free bool
		err := pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND classid=0 AND objid::bigint=$1 AND objsubid=1)`, processorLock).Scan(&free)
		return err == nil && free
	})
	assertPendingTrigger()
	if pendingCount() != 1 || readSaved().LastSuccessful.Active.Generation != 9 {
		t.Fatal("abrupt processor death changed accepted work or the last successful report")
	}
	if _, err := pool.Exec(ctx, `UPDATE publication_receiver_inbox SET accepted_at=clock_timestamp()-interval '2 days'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Accept(ctx, Event{Source: eventSource + testTenant, ID: "new", Action: "publication.activated"}, time.Now()); !errors.Is(err, errFull) {
		t.Fatal("retention removed pending work")
	}
	setAPI(200, activePage(2))
	startProcess()
	wait("restart refresh from current rollback state", func() bool {
		v := readSaved()
		return pendingCount() == 0 && v.LastSuccessful.Active != nil && v.LastSuccessful.Active.Generation == 2 && v.LastSuccessful.Trigger != nil && *v.LastSuccessful.Trigger == expectedTrigger
	})
	pruneReady()
	setAPI(200, `{"items":[]}`)
	withdrawn := bytes.ReplaceAll(eventBytes("publication.unpublished"), []byte(testID), []byte("00000000-0000-4000-8000-000000000005"))
	if code, err := post(origin, withdrawn, 1, testSecret); err != nil || code != 204 {
		t.Fatal("withdrawal trigger rejected")
	}
	wait("current empty state", func() bool { v := readSaved(); return pendingCount() == 0 && v.LastSuccessful.Active == nil })
	pruneReady()
	before, _ := counts()
	testEvent := bytes.ReplaceAll(eventBytes("webhook.test"), []byte(testID), []byte("00000000-0000-4000-8000-000000000006"))
	setAPI(503, `{}`)
	if code, err := post(origin, testEvent, 1, testSecret); err != nil || code != 204 {
		t.Fatal("signed setup test rejected")
	}
	wait("setup receipt completion", func() bool { return pendingCount() == 0 })
	after, _ := counts()
	if before != after {
		t.Fatal("setup test queried publication")
	}
	pruneReady()
	second := "whsec_" + strings.Repeat("b", 43)
	replacement := filepath.Join(filepath.Dir(signerFile), "replacement.json")
	if os.WriteFile(replacement, []byte(`{"secret":"`+second+`","version":2}`), 0600) != nil || os.Rename(replacement, signerFile) != nil {
		t.Fatal("atomic rotation fixture")
	}
	testEvent = bytes.ReplaceAll(testEvent, []byte("00000000-0000-4000-8000-000000000006"), []byte("00000000-0000-4000-8000-000000000007"))
	if code, err := post(origin, testEvent, 1, testSecret); err != nil || code != 401 {
		t.Fatal("old signer still accepted")
	}
	if code, err := post(origin, testEvent, 2, second); err != nil || code != 204 {
		t.Fatal("new signer not loaded")
	}
	wait("rotated setup completion", func() bool { return pendingCount() == 0 })
	if _, err := pool.Exec(ctx, `ALTER TABLE publication_receiver_inbox RENAME TO publication_receiver_offline`); err != nil {
		t.Fatal(err)
	}
	if code, err := post(origin, testEvent, 2, second); err != nil || code != 503 {
		t.Fatal("storage failure acknowledged")
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE publication_receiver_offline RENAME TO publication_receiver_inbox`); err != nil {
		t.Fatal(err)
	}
	stopProcess()
	// Each explicit reconciliation reads current state, including unchanged loss counts.
	setAPI(200, activePage(3))
	startProcess("5s", "6s")
	wait("periodic initial observation", func() bool {
		v := readSaved()
		return v.LastSuccessful.Active != nil && v.LastSuccessful.Active.Generation == 3
	})
	setAPI(200, activePage(4))
	wait("periodic reconciliation without a notification or changed counters", func() bool {
		v := readSaved()
		return v.LastSuccessful.Active != nil && v.LastSuccessful.Active.Generation == 4
	})
	pruneReady()
	testEvent = bytes.ReplaceAll(testEvent, []byte("00000000-0000-4000-8000-000000000007"), []byte("00000000-0000-4000-8000-000000000008"))
	if code, err := post(origin, testEvent, 2, second); err != nil || code != 204 {
		t.Fatal("periodic setup notification rejected")
	}
	wait("pending receipt completion when reconciliation is due on every poll", func() bool { return pendingCount() == 0 })
	stopProcess()

	// Keep one accepted receipt pending, then prove credential repair in the same
	// running process. The original abrupt-death/restart control above is intact.
	pruneReady()
	setAPI(503, `{}`)
	startProcess()
	credentialPID := process.Process.Pid
	credentialEvent := publicationEvent()
	credentialEvent.ID = "00000000-0000-4000-8000-000000000009"
	credentialBytes := bytes.ReplaceAll(eventBytes("publication.activated"), []byte(testID), []byte(credentialEvent.ID))
	if code, err := post(origin, credentialBytes, 2, second); err != nil || code != 204 {
		t.Fatal("credential fixture not accepted")
	}
	wait("credential fixture pending after API refusal", func() bool { v := readSaved(); return pendingCount() == 1 && v.RefreshFailure == "catalog_read_failed" })
	credentialPrior := readSaved()
	if credentialPrior.LastAttemptAt == nil {
		t.Fatal("missing failed-attempt evidence")
	}
	attemptBefore := *credentialPrior.LastAttemptAt
	replacePrivateTest(t, token, []byte(" \n\t"))
	// One in-flight old-token request may finish after replacement. Wait for its
	// failure (or the first provider failure), then observe another failed attempt
	// with no new API exchange. No live diagnostic buffer is read.
	wait("credential transition reached processor", func() bool { v := readSaved(); return v.LastAttemptAt != nil && !v.LastAttemptAt.Equal(attemptBefore) })
	blocked := readSaved()
	blockedReads, blockedLoss := counts()
	wait("private token refusal before transport", func() bool {
		v := readSaved()
		r, l := counts()
		return v.LastAttemptAt != nil && !v.LastAttemptAt.Equal(*blocked.LastAttemptAt) && r == blockedReads && l == blockedLoss
	})
	blocked = readSaved()
	pending, err := s.Next(ctx)
	if err != nil || pending == nil || *pending != credentialEvent || !reflect.DeepEqual(blocked.LastSuccessful, credentialPrior.LastSuccessful) || blocked.RefreshFailure != "catalog_read_failed" {
		t.Fatal("credential failure changed saved state or accepted work")
	}
	mu.Lock()
	expectedBearer = "Bearer " + replacementToken
	allowPrevious = true
	mu.Unlock()
	setAPI(200, activePage(6))
	replacePrivateTest(t, token, []byte(replacementToken))
	wait("same process completed receipt with replacement token", func() bool {
		v := readSaved()
		mu.Lock()
		seen := replacementReads
		mu.Unlock()
		return seen > 0 && pendingCount() == 0 && v.LastSuccessful != nil && v.LastSuccessful.Active != nil && v.LastSuccessful.Active.Generation == 6 && v.LastSuccessful.Trigger != nil && *v.LastSuccessful.Trigger == credentialEvent
	})
	if process.Process.Pid != credentialPID {
		t.Fatal("token reload was qualified by a different process")
	}
	stopProcess()
	replacePrivateTest(t, token, []byte("receiver-reader"))
	mu.Lock()
	expectedBearer = "Bearer receiver-reader"
	allowPrevious = false
	mu.Unlock()

	setAPI(200, activePage(1))
	before, beforeLoss := counts()
	for range 2 {
		if _, err := runCommand("reconcile"); err != nil {
			t.Fatal("explicit reconciliation failed")
		}
	}
	after, afterLoss := counts()
	if after-before != 2 || afterLoss-beforeLoss != 2 {
		t.Fatal("unchanged counters suppressed reconciliation")
	}
	for _, bad := range []struct {
		status int
		body   string
	}{{401, `{}`}, {403, `{}`}, {404, `{}`}, {503, `{}`}, {200, `{}`}, {200, strings.Replace(activePage(1), testTenant, testID, 1)}, {200, strings.Replace(activePage(1), testHost, "other.test", 1)}, {200, `{"Items":[]}`}, {200, strings.TrimSuffix(activePage(1), "}") + `,"Items":[]}`}} {
		setAPI(bad.status, bad.body)
		if _, err := runCommand("reconcile"); err == nil {
			t.Fatal("bad read became a successful refresh")
		}
		v := readSaved()
		if v.LastSuccessful.Active == nil || v.LastSuccessful.Active.Generation != 1 || v.RefreshFailure == "" {
			t.Fatal("failed read became unpublication")
		}
	}

	// Counter evidence must persist in the correct columns, independently of the
	// snapshot read. An unsuccessful second read cannot commit the first one.
	setAPI(200, activePage(5))
	setLoss(200, `{"pendingEvents":0,"pendingEventLimit":1000,"capacityLossCount":2,"deliveryLossCount":7}`)
	if _, err := runCommand("reconcile"); err != nil {
		t.Fatal("nonzero loss control failed")
	}
	lossPrior := readSaved()
	if lossPrior.LastSuccessful == nil || lossPrior.LastSuccessful.Active == nil || lossPrior.CapacityLoss != 2 || lossPrior.DeliveryLoss != 7 || lossPrior.LossObservedAt == nil || lossPrior.LastSuccessful.Active.Generation != 5 {
		t.Fatal("durable loss fields were dropped or swapped")
	}
	checkRawLoss := func(capacity, delivery int64, observed *time.Time) {
		t.Helper()
		var rawCapacity, rawDelivery int64
		var rawObserved *time.Time
		if pool.QueryRow(ctx, `SELECT capacity_loss,delivery_loss,loss_observed_at FROM publication_receiver_report WHERE singleton`).Scan(&rawCapacity, &rawDelivery, &rawObserved) != nil {
			t.Fatal("raw loss evidence unavailable")
		}
		if rawCapacity != capacity || rawDelivery != delivery || observed == nil || rawObserved == nil || !rawObserved.Equal(*observed) {
			t.Fatal("raw loss columns disagree with independent expected values")
		}
	}
	checkRawLoss(2, 7, lossPrior.LossObservedAt)
	checkRendered := func(want Saved) {
		t.Helper()
		raw, err := reports.ReadFile("report.json")
		if err != nil {
			t.Fatal("rendered report unavailable")
		}
		var got Saved
		if json.Unmarshal(raw, &got) != nil {
			t.Fatal("rendered report malformed")
		}
		actual, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(actual, expected) {
			t.Fatal("rendered report differs from saved database values")
		}
	}
	checkRendered(lossPrior)
	for _, bad := range []struct {
		status int
		body   string
	}{
		{403, `{"title":"Forbidden","status":403}`},
		{503, `{"title":"Unavailable","status":503}`},
		{200, `{"pendingEvents":0,"pendingEventLimit":1000,"capacityLossCount":2}`},
	} {
		setAPI(200, activePage(6))
		setLoss(bad.status, bad.body)
		beforeRead, beforeLoss := counts()
		raw, err := runCommand("reconcile")
		if err == nil || !bytes.Contains(raw, []byte("reconciliation_read_failed")) {
			t.Fatal("counter refusal did not fail at the intended read")
		}
		afterRead, afterLoss := counts()
		v := readSaved()
		if afterRead-beforeRead != 1 || afterLoss-beforeLoss != 1 || !reflect.DeepEqual(v.LastSuccessful, lossPrior.LastSuccessful) || v.CapacityLoss != 2 || v.DeliveryLoss != 7 || v.LossObservedAt == nil || !v.LossObservedAt.Equal(*lossPrior.LossObservedAt) || v.RefreshFailure != "reconciliation_read_failed" || v.LastAttemptAt == nil {
			t.Fatal("failed counter read replaced prior publication/loss evidence")
		}
		checkRendered(v)
	}
	// Persistence succeeds before file rendering. Repairing the file destination
	// must require only a read-only render, not another observation or API call.
	setAPI(200, activePage(7))
	setLoss(200, `{"pendingEvents":0,"pendingEventLimit":1000,"capacityLossCount":11,"deliveryLossCount":13}`)
	failedOutput, err := runCommand("reconcile", "--output", filepath.Join(dir, "missing", "report.json"))
	if err == nil || !bytes.Contains(failedOutput, []byte("report_render_failed")) {
		t.Fatal("render-stage failure was not injected after refresh")
	}
	storedBeforeRender := readSaved()
	if storedBeforeRender.LastSuccessful == nil || storedBeforeRender.LastSuccessful.Active == nil || storedBeforeRender.LastSuccessful.Active.Generation != 7 || storedBeforeRender.CapacityLoss != 11 || storedBeforeRender.DeliveryLoss != 13 || storedBeforeRender.LossObservedAt == nil || storedBeforeRender.RefreshFailure != "" {
		t.Fatal("render failure undid the successful database refresh")
	}
	checkRawLoss(11, 13, storedBeforeRender.LossObservedAt)
	renderReads, renderLoss := counts()
	if _, err := runCommand("render"); err != nil {
		t.Fatal("render-only repair failed")
	}
	afterRead, afterLoss := counts()
	if renderReads != afterRead || renderLoss != afterLoss || !reflect.DeepEqual(readSaved(), storedBeforeRender) {
		t.Fatal("output repair called Catalog or changed saved evidence")
	}
	checkRendered(storedBeforeRender)

	if _, err := runCommand("render", "--output", filepath.Join(dir, "missing", "report.json")); err == nil {
		t.Fatal("output failure ignored")
	}
	if _, err := runCommand("render"); err != nil {
		t.Fatal("saved report not recoverable")
	}
	first, err := reports.ReadFile("report.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand("render"); err != nil {
		t.Fatal(err)
	}
	secondRender, err := reports.ReadFile("report.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, secondRender) {
		t.Fatal("repeat rendering changed saved result")
	}
	// Completion and report storage roll back together if the report write fails.
	pruneReady()
	e := Event{Source: eventSource + testTenant, ID: testID, Action: "publication.activated", TargetID: testTarget}
	if s.Accept(ctx, e, time.Now()) != nil {
		t.Fatal("pending transaction fixture")
	}

	// Initialization and each binding dimension must preserve nonempty state.
	// No serve processor is running while the before/after values are compared.
	if err := s.Fail(ctx, time.Now().UTC(), "catalog_read_failed"); err != nil {
		t.Fatal("failed-attempt binding fixture unavailable")
	}
	bindingPrior := readSaved()
	if bindingPrior.LastSuccessful == nil || bindingPrior.LastSuccessful.Active == nil || bindingPrior.LastSuccessful.Active.Generation != 7 || bindingPrior.CapacityLoss != 11 || bindingPrior.DeliveryLoss != 13 || bindingPrior.LossObservedAt == nil || bindingPrior.RefreshFailure != "catalog_read_failed" || bindingPrior.LastAttemptAt == nil {
		t.Fatal("binding guard requires nonempty saved state")
	}
	pendingPrior, err := s.Next(ctx)
	if err != nil || pendingPrior == nil || *pendingPrior != e {
		t.Fatal("nonempty binding fixture missing")
	}
	var boundTenant, boundHost, boundOrigin string
	if pool.QueryRow(ctx, `SELECT tenant,host,origin FROM publication_receiver_settings WHERE singleton`).Scan(&boundTenant, &boundHost, &boundOrigin) != nil {
		t.Fatal("binding unavailable")
	}
	if boundTenant != testTenant || boundHost != testHost || boundOrigin != catalog.URL {
		t.Fatal("initial binding differs from explicit receiver inputs")
	}
	bindingReads, bindingLoss := counts()
	assertBindingUnchanged := func() {
		t.Helper()
		var tenant, host, origin string
		if pool.QueryRow(ctx, `SELECT tenant,host,origin FROM publication_receiver_settings WHERE singleton`).Scan(&tenant, &host, &origin) != nil {
			t.Fatal("binding inspection failed")
		}
		next, err := s.Next(ctx)
		r, l := counts()
		if tenant != boundTenant || host != boundHost || origin != boundOrigin || !reflect.DeepEqual(readSaved(), bindingPrior) || err != nil || next == nil || *next != *pendingPrior || pendingCount() != 1 || r != bindingReads || l != bindingLoss {
			t.Fatal("init/bind changed retained evidence or called Catalog")
		}
	}
	if _, err := runCommand("init"); err != nil {
		t.Fatal("same-binding init failed")
	}
	assertBindingUnchanged()
	for _, change := range [][]string{{"--tenant", testTarget}, {"--host", "other.example.test"}, {"--origin", "https://other.example.test"}} {
		for _, command := range []string{"init", "render"} {
			expected := "receiver_database_binding_failed"
			if command == "init" {
				expected = "receiver_database_init_failed"
			}
			raw, err := runCommand(command, change...)
			if err == nil || !bytes.Contains(raw, []byte(expected)) {
				t.Fatal("mismatched binding did not refuse at the database guard")
			}
			assertBindingUnchanged()
		}
	}

	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_report() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned report failure'; END $$; CREATE TRIGGER reject_report BEFORE UPDATE ON publication_receiver_report FOR EACH ROW EXECUTE FUNCTION reject_report()`); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, &e, &Report{Label: "should roll back"}, time.Now(), nil); err == nil || pendingCount() != 1 {
		t.Fatal("completion escaped report rollback")
	}
	var parsed Saved
	if json.Unmarshal(first, &parsed) != nil || parsed.LastSuccessful == nil {
		t.Fatal("report output invalid")
	}
}
