//go:build integration

package functional_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/draftfile"
)

// Server-controlled checkpoints occur only after the importer published durable
// inflight intent. They exercise actual signals without a production test hook.
func qualifyProcessImportSignals(ctx context.Context, t *testing.T, f processFixture, owner *pgx.Conn, address, peer, tenant, publisher string) {
	t.Helper()
	for index, signal := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		t.Run(fmt.Sprintf("import-signal-%d", signal), func(t *testing.T) {
			f := f
			f.t = t
			work, stop := context.WithTimeout(ctx, 35*time.Second)
			defer stop()
			// Each signal case owns a distinct namespace. Scoped grants remain
			// confined there until the maintained whole-database cleanup.
			var selectedNamespace catalog.Namespace
			decodeProcessJSON(t, f.request(work, address, "POST", "/api/v1/namespaces", "local-admin", fmt.Sprintf(`{"slug":"signal-import-%d","displayName":"Owned signal import %d"}`, index, index), http.StatusCreated, map[string]string{"Idempotency-Key": fmt.Sprintf("signal-namespace-%d", index)}), &selectedNamespace)
			if selectedNamespace.ID == "" || selectedNamespace.TenantID != tenant {
				t.Fatal("owned signal namespace binding changed")
			}
			namespace := selectedNamespace.ID
			identifier := fmt.Sprintf("custom:signal-import-%d", index)
			directory := t.TempDir()
			filename := filepath.Join(directory, "input.json")
			state := filepath.Join(directory, "private", "receipt.json")
			native := `{"name":"Signal import fixture","framework":"custom"}`
			var input []byte
			var err error
			args := []string{"--file", filename, "--state-file", state}
			if index == 0 {
				input, err = json.Marshal(map[string]any{"format": 1, "resources": []any{map[string]any{"key": "signal", "namespaceId": namespace, "canonicalIdentifier": identifier, "kind": "agent", "version": "1.0.0", "listing": map[string]any{"publisherId": publisher, "displayName": "Signal draft", "description": "Owned process interruption", "visibility": "private"}, "representations": []any{map[string]any{"role": "primary", "mediaType": "application/vnd.open-agentic-catalog.agent+json", "dataFile": "agent.json"}}}}})
				if err := os.WriteFile(filepath.Join(directory, "agent.json"), []byte(native), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				input, err = draftfile.Encode(draftfile.File{Format: draftfile.Format, Kind: catalog.KindAgent, CanonicalIdentifier: identifier, Version: "1.0.0", Representations: []draftfile.Representation{{Role: catalog.RepresentationPrimary, MediaType: "application/vnd.open-agentic-catalog.agent+json", ProfileID: adapter.ProfilePrivateAgent, Input: draftfile.Input{Mode: "data", Text: &native}}}})
				args = append(args, "--namespace-id", namespace)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filename, input, 0600); err != nil {
				t.Fatal(err)
			}
			var member struct{ MembershipID string }
			decodeProcessJSON(t, f.request(work, address, "GET", "/api/v1/me", "local-reader", "", 200), &member)
			var role string
			if err := owner.QueryRow(work, `SELECT id FROM role_definitions WHERE tenant_id=$1 AND key='contributor' AND state='active'`, tenant).Scan(&role); err != nil {
				t.Fatal(err)
			}
			grantBody := fmt.Sprintf(`{"roleDefinitionId":%q,"membershipId":%q,"namespaceId":%q,"reason":"Owned signal-import author"}`, role, member.MembershipID, namespace)
			var grant struct {
				ID        string
				GrantedAt time.Time
			}
			decodeProcessJSON(t, f.request(work, address, "POST", "/api/v1/access/assignments", "local-admin", grantBody, 201, map[string]string{"Idempotency-Key": fmt.Sprintf("signal-author-%d", index)}), &grant)
			checkpoint := make(chan struct{})
			release := make(chan struct{})
			handlerDone := make(chan struct{})
			var once sync.Once
			var mu sync.Mutex
			type sent struct{ Path, Key, Digest string }
			commands := []sent{}
			held := false
			client := &http.Client{Transport: &http.Transport{}, Timeout: 8 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
			defer client.CloseIdleConnections()
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(io.LimitReader(r.Body, (2<<20)+1))
				if err != nil || len(body) > 2<<20 {
					t.Error("owned signal proxy body")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				hold := false
				if r.Method == http.MethodPost {
					sum := sha256.Sum256(body)
					mu.Lock()
					commands = append(commands, sent{r.URL.Path, r.Header.Get("Idempotency-Key"), "sha256:" + hex.EncodeToString(sum[:])})
					if !held && strings.HasSuffix(r.URL.Path, "/versions") {
						held = true
						hold = true
					}
					mu.Unlock()
				}
				if hold {
					defer close(handlerDone)
				}
				if hold && index == 0 {
					close(checkpoint)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					case <-work.Done():
						return
					}
					return
				}
				// The proxy owns accepted forwarding through its bounded fixture
				// context; importer death does not imply an accepted write failed.
				forward, err := http.NewRequestWithContext(work, r.Method, "http://"+peer+r.URL.RequestURI(), bytes.NewReader(body)) // #nosec G704 -- fixed owned API peer; only request path/query is forwarded.
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				forward.Header = r.Header.Clone()
				response, err := client.Do(forward) // #nosec G704 -- fixed local process fixture, redirects refused.
				if err != nil {
					t.Error("owned signal proxy forwarding failed")
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				defer func() { _ = response.Body.Close() }()
				raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				if hold && index == 1 {
					if response.StatusCode != http.StatusCreated {
						t.Error("commit checkpoint was not a successful version creation")
					}
					close(checkpoint)
					select {
					case <-release:
					case <-r.Context().Done():
					case <-work.Done():
					}
					return
				}
				for k, values := range response.Header {
					for _, v := range values {
						w.Header().Add(k, v)
					}
				}
				w.WriteHeader(response.StatusCode)
				_, _ = w.Write(raw)
			}))
			defer proxy.Close()
			defer once.Do(func() { close(release) })
			args = append(args, "--base-url", proxy.URL)
			command := func(action string) *exec.Cmd {
				selected := f
				if action == "apply" && signal == syscall.SIGKILL {
					// An intentionally hard-killed Go process cannot flush
					// counters. Do not create an incomplete collector input;
					// resumes, refusals and orderly SIGTERM stay instrumented.
					selected.coverageDirectory = ""
				}
				c := selected.command(work, "catalogctl", "", append([]string{"import", action}, args...)...)
				c.Env = append(c.Env, "CATALOG_REMOTE_TOKEN=local-reader")
				return c
			}
			resume := func(want int) {
				t.Helper()
				c := command("resume")
				raw, err := c.CombinedOutput()
				f.counters(c)
				if c.ProcessState == nil || c.ProcessState.ExitCode() != want {
					t.Fatalf("import resume exit %v: %s", err, raw)
				}
			}
			child := command("apply")
			var stdout, stderr bytes.Buffer
			child.Stdout = &stdout
			child.Stderr = &stderr
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- child.Wait() }()
			exited := false
			defer func() {
				if !exited {
					_ = child.Process.Kill()
					select {
					case err := <-done:
						if err == nil {
							t.Log("owned importer exited before forced cleanup")
						}
					case <-time.After(5 * time.Second):
						t.Error("owned import process cleanup")
					}
				}
			}()
			select {
			case <-checkpoint:
			case err := <-done:
				exited = true
				t.Fatalf("import exited before durable checkpoint: %v %s", err, &stderr)
			case <-work.Done():
				t.Fatal("import durable checkpoint timeout")
			}
			type step struct {
				State, Key, Path, BodyDigest string
				Ack                          struct{ ID, RevisionID string }
			}
			type receipt struct{ Steps []step }
			read := func() receipt {
				t.Helper()
				stateRoot, err := os.OpenRoot(filepath.Dir(state))
				if err != nil {
					t.Fatal("private receipt directory unavailable", err)
				}
				defer func() { _ = stateRoot.Close() }()
				info, err := stateRoot.Lstat(filepath.Base(state))
				if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
					t.Fatal("private completed receipt unavailable", err)
				}
				raw, err := stateRoot.ReadFile(filepath.Base(state))
				if err != nil {
					t.Fatal(err)
				}
				var document struct {
					Receipt receipt
					Digest  string
				}
				decodeProcessJSON(t, raw, &document)
				if document.Digest == "" {
					t.Fatal("receipt omitted checksum")
				}
				return document.Receipt
			}
			original := read()
			if len(original.Steps) < 2 || original.Steps[0].State != "acknowledged" || original.Steps[1].State != "inflight" || original.Steps[0].Ack.ID == "" {
				t.Fatal("checkpoint preceded complete durable intent")
			}
			mu.Lock()
			before := len(commands)
			captured := commands[1]
			mu.Unlock()
			if original.Steps[1].Key != captured.Key || original.Steps[1].Path != captured.Path || original.Steps[1].BodyDigest != captured.Digest {
				t.Fatal("durable intent did not bind actual command bytes")
			}
			// Another live invoker must refuse while the original process owns flock.
			resume(1)
			mu.Lock()
			liveChanged := len(commands) != before
			mu.Unlock()
			if liveChanged {
				t.Fatal("competing live importer wrote despite private lock")
			}
			if err := child.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				exited = true
				if err == nil {
					t.Fatal("interrupted import reported success")
				}
			case <-work.Done():
				t.Fatal("interrupted import did not exit")
			}
			if signal != syscall.SIGKILL {
				f.counters(child)
			}
			once.Do(func() { close(release) })
			select {
			case <-handlerDone:
			case <-work.Done():
				t.Fatal("signal proxy checkpoint did not close")
			}
			interrupted := read()
			if len(interrupted.Steps) != len(original.Steps) {
				t.Fatal("interruption changed the complete retained import plan")
			}
			if interrupted.Steps[1].State != "inflight" && interrupted.Steps[1].State != "uncertain" {
				t.Fatal("interruption lost unacknowledged intent")
			}
			mu.Lock()
			later := len(commands)
			mu.Unlock()
			if later != before {
				t.Fatal("terminated invocation started a later command")
			}
			if info, err := os.Stat(state + ".lock"); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
				t.Fatal("private lock entry unexpectedly removed or exposed", err)
			}
			// Changed input and current grant loss must refuse before another mutation.
			if err := os.WriteFile(filename, bytes.Replace(input, []byte("1.0.0"), []byte("2.0.0"), 1), 0600); err != nil {
				t.Fatal(err)
			}
			resume(1)
			if err := os.WriteFile(filename, input, 0600); err != nil {
				t.Fatal(err)
			}
			// Admit this single chronology-sensitive mutation only after a
			// read proves the owning server's clock has passed its grant. This
			// is not a retry: a later backward step still fails the command.
			if grant.GrantedAt.IsZero() {
				t.Fatal("owned grant omitted its retained timestamp")
			}
			clockBarrier, stopClock := context.WithTimeout(work, 10*time.Second)
			defer stopClock()
			clockTick := time.NewTicker(25 * time.Millisecond)
			defer clockTick.Stop()
			for {
				probe, err := http.NewRequestWithContext(clockBarrier, http.MethodGet, "http://"+address+"/readyz", nil) // #nosec G704 -- fixed owned API listener, no document-selected destination.
				if err != nil {
					t.Fatal(err)
				}
				reply, err := client.Do(probe) // #nosec G704 -- fixed local process fixture; redirects refused.
				if err != nil {
					t.Fatal("owned grant clock admission probe failed", err)
				}
				data, readErr := io.ReadAll(io.LimitReader(reply.Body, 1025))
				closeErr := reply.Body.Close()
				observed, dateErr := http.ParseTime(reply.Header.Get("Date"))
				if readErr != nil || closeErr != nil || len(data) > 1024 || dateErr != nil || reply.StatusCode != http.StatusOK {
					t.Fatal("owned grant clock probe omitted a valid healthy Date")
				}
				if observed.After(grant.GrantedAt) {
					break
				}
				select {
				case <-clockBarrier.Done():
					t.Fatal("owning API clock did not pass the retained grant within its budget")
				case <-clockTick.C:
				}
			}
			f.request(work, address, "POST", "/api/v1/access/assignments/"+grant.ID+":revoke", "local-admin", `{"reason":"Current authority must govern interrupted resume"}`, 200, map[string]string{"If-Match": `"rev-1"`, "Idempotency-Key": fmt.Sprintf("signal-revoke-%d", index)})
			resume(1)
			mu.Lock()
			refusalWrites := len(commands) != before
			mu.Unlock()
			if refusalWrites {
				t.Fatal("changed input or revoked current authority sent another command")
			}
			decodeProcessJSON(t, f.request(work, address, "POST", "/api/v1/access/assignments", "local-admin", grantBody, 201, map[string]string{"Idempotency-Key": fmt.Sprintf("signal-regrant-%d", index)}), &grant)
			resume(0)
			complete := read()
			if len(complete.Steps) != len(original.Steps) {
				t.Fatal("fresh importer changed the complete original plan")
			}
			for i, s := range complete.Steps {
				if s.State != "acknowledged" || s.Key != original.Steps[i].Key {
					t.Fatal("fresh importer changed command keys or failed completion")
				}
				if i <= 1 && (s.Path != original.Steps[i].Path || s.BodyDigest != original.Steps[i].BodyDigest) {
					t.Fatal("resume changed original durable command binding")
				}
			}
			if complete.Steps[0].Ack.ID != original.Steps[0].Ack.ID {
				t.Fatal("completed resource identity changed after death")
			}
			var resources, versions, audits int
			if err := owner.QueryRow(work, `SELECT (SELECT count(*) FROM resources WHERE tenant_id=$1 AND canonical_identifier=$2),(SELECT count(*) FROM resource_versions WHERE tenant_id=$1 AND resource_id=$3),(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='resource_version.created' AND target_id=$4)`, tenant, identifier, complete.Steps[0].Ack.ID, complete.Steps[1].Ack.ID).Scan(&resources, &versions, &audits); err != nil || resources != 1 || versions != 1 || audits != 1 {
				t.Fatal("signal resume did not retain exactly one logical creation", err, resources, versions, audits)
			}
			mu.Lock()
			replayed := commands[before]
			mu.Unlock()
			if replayed != captured {
				t.Fatal("resumed request changed key/path/body")
			}
			mu.Lock()
			completedCalls := len(commands)
			mu.Unlock()
			resume(0)
			mu.Lock()
			changed := len(commands) != completedCalls
			mu.Unlock()
			if changed {
				t.Fatal("completed receipt repeated a mutation")
			}
		})
	}
}
