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
	reads, lossReads := 0, 0
	catalog := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer receiver-reader" {
			t.Error("wrong SDK method or credential")
			w.WriteHeader(http.StatusForbidden)
			return
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
			_, _ = io.WriteString(w, `{"pendingEvents":0,"pendingEventLimit":1000,"capacityLossCount":0,"deliveryLossCount":0}`)
		default:
			t.Error("unexpected Catalog/resource request")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer catalog.Close()
	setAPI := func(code int, raw string) { mu.Lock(); status, body = code, raw; mu.Unlock() }
	counts := func() (int, int) { mu.Lock(); defer mu.Unlock(); return reads, lossReads }
	dir := t.TempDir()
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
	base := []string{"--origin", catalog.URL, "--tenant", testTenant, "--host", testHost, "--database-file", dbfile, "--token-file", token, "--ca-file", cert, "--output", output, "--capacity", "1", "--retention", "1m"}
	runCommand := func(command string, extra ...string) ([]byte, error) {
		args := append([]string{command}, base...)
		args = append(args, extra...)
		// #nosec G204 G702 -- Make supplies the explicitly built, invocation-owned sample executable.
		child := exec.CommandContext(ctx, executable, args...)
		raw, err := child.CombinedOutput()
		for _, secret := range []string{testSecret, "receiver-reader", receiverDSN} {
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
		for _, secret := range []string{testSecret, "receiver-reader", receiverDSN} {
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
	wait("first publication report", func() bool {
		v := readSaved()
		return v.LastSuccessful != nil && v.LastSuccessful.Active != nil && v.LastSuccessful.Active.Generation == 9
	})
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
	stopProcess()
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
		return pendingCount() == 0 && v.LastSuccessful.Active != nil && v.LastSuccessful.Active.Generation == 2 && v.LastSuccessful.Trigger != nil
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
	if _, err := runCommand("render", "--output", filepath.Join(dir, "missing", "report.json")); err == nil {
		t.Fatal("output failure ignored")
	}
	if _, err := runCommand("render"); err != nil {
		t.Fatal("saved report not recoverable")
	}
	reports, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reports.Close() }()
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
