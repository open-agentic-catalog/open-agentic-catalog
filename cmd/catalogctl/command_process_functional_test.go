//go:build functional

// These functional tests qualify terminal streams and process signal ownership.
package main

import (
	"bytes"
	"context"
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
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// buildCatalogctlProcessBinary shares one real CLI within a top-level test.
// The existing coverage collector owns optional child counters and their cleanup.
func buildCatalogctlProcessBinary(t *testing.T, ctx context.Context) string {
	t.Helper()
	binary := filepath.Join(canonicalTempDir(t), "catalogctl")
	arguments := []string{"build", "-mod=readonly", "-o", binary}
	if os.Getenv("CATALOG_PROCESS_COVERAGE_DIR") != "" {
		arguments = append(arguments, "-cover", "-covermode=atomic", "-coverpkg=github.com/open-agentic-catalog/open-agentic-catalog/cmd/catalogctl")
	}
	arguments = append(arguments, ".")
	build := exec.CommandContext(ctx, "go", arguments...) // #nosec G204,G702 -- Fixed source package and invocation-owned binary.
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual command: %v\n%s", err, output)
	}
	return binary
}

// catalogctlProcessBaseEnvironment omits ambient authority without creating coverage inputs.
// Only an intentionally hard-killed child uses it directly: that child cannot flush counters.
func catalogctlProcessBaseEnvironment(t *testing.T) []string {
	t.Helper()
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + canonicalTempDir(t)}
}

// catalogctlProcessEnvironment adds complete orderly-child evidence to the selected collector.
func catalogctlProcessEnvironment(t *testing.T) []string {
	t.Helper()
	environment := catalogctlProcessBaseEnvironment(t)
	if collector := os.Getenv("CATALOG_PROCESS_COVERAGE_DIR"); collector != "" {
		directory, err := os.MkdirTemp(collector, "catalogctl-")
		if err != nil {
			t.Fatal("prepare owned child coverage", err)
		}
		environment = append(environment, "GOCOVERDIR="+directory)
	}
	return environment
}

// processCommandOutput bounds retained bytes and observes complete login URL lines.
type processCommandOutput struct {
	mu            sync.Mutex
	bytes         []byte
	line          string
	truncated     bool
	authorization chan string
}

// Write retains bounded evidence without blocking child output on its observer.
func (output *processCommandOutput) Write(value []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	const maximum = 64 << 10
	remaining := maximum - len(output.bytes)
	output.bytes = append(output.bytes, value[:min(len(value), remaining)]...)
	output.truncated = output.truncated || len(value) > remaining
	if output.authorization != nil && !output.truncated {
		output.line += string(value)
		for {
			line, rest, found := strings.Cut(output.line, "\n")
			if !found {
				break
			}
			output.line = rest
			if strings.HasPrefix(line, "https://") {
				select {
				case output.authorization <- line:
				default:
				}
			}
		}
	}
	return len(value), nil
}

// snapshot is safe while a child still writes to the observed stream.
func (output *processCommandOutput) snapshot() []byte {
	output.mu.Lock()
	defer output.mu.Unlock()
	return bytes.Clone(output.bytes)
}

type processCommand struct {
	command *exec.Cmd
	stdout  processCommandOutput
	stderr  processCommandOutput
	done    chan struct{}
	err     error
}

// startCatalogctlProcess registers bounded reaping before a test can fail.
func startCatalogctlProcess(t *testing.T, ctx context.Context, binary string, environment []string, arguments ...string) *processCommand {
	t.Helper()
	process := &processCommand{done: make(chan struct{})}
	process.stderr.authorization = make(chan string, 1)
	process.command = exec.CommandContext(ctx, binary, arguments...) // #nosec G204,G702 -- Owned compiled CLI and explicit synthetic local inputs.
	process.command.Env = environment
	process.command.Stdout, process.command.Stderr = &process.stdout, &process.stderr
	process.command.WaitDelay = 2 * time.Second
	if err := process.command.Start(); err != nil {
		t.Fatal("start actual command", err)
	}
	go func() { process.err = process.command.Wait(); close(process.done) }()
	t.Cleanup(func() {
		select {
		case <-process.done:
			return
		default:
			_ = process.command.Process.Kill()
		}
		select {
		case <-process.done:
		case <-time.After(5 * time.Second):
			t.Error("owned command did not reap after kill")
		}
	})
	return process
}

// wait requires the command's own exit, rather than accepting a test watchdog kill.
func (process *processCommand) wait(t *testing.T, exitCode int) ([]byte, []byte) {
	t.Helper()
	select {
	case <-process.done:
	case <-time.After(10 * time.Second):
		t.Fatal("actual command did not finish before independent watchdog")
	}
	actual := 0
	if process.err != nil {
		var exit *exec.ExitError
		if !errors.As(process.err, &exit) {
			t.Fatalf("command wait failed: %v", process.err)
		}
		actual = exit.ExitCode()
	}
	if actual != exitCode || process.stdout.truncated || process.stderr.truncated {
		t.Fatalf("actual command exit=%d, expected=%d, output truncated=%v", actual, exitCode, process.stdout.truncated || process.stderr.truncated)
	}
	return process.stdout.snapshot(), process.stderr.snapshot()
}

// assertProcessTerminalFailure checks the stable projection without snapshotting punctuation.
func assertProcessTerminalFailure(t *testing.T, output, diagnostic []byte, stage string, forbidden ...string) {
	t.Helper()
	if len(output) != 0 || len(diagnostic) == 0 || len(diagnostic) > 8192 || bytes.Count(diagnostic, []byte{'\n'}) != 1 {
		t.Fatal("failed command did not keep one bounded diagnostic separate from success stdout")
	}
	fields := strings.SplitN(strings.TrimSuffix(string(diagnostic), "\n"), ": ", 4)
	if len(fields) != 4 || fields[0] != "catalogctl" || fields[1] != stage || fields[2] != "INTERNAL" || fields[3] == "" {
		t.Fatal("command diagnostic lost its command, stage or classified code")
	}
	for _, sentinel := range forbidden {
		if bytes.Contains(diagnostic, []byte(sentinel)) {
			t.Fatal("terminal diagnostic disclosed a private supplied value")
		}
	}
}

// TestCatalogctlProcessContracts exercises compiled dispatch and signal-owning entrypoints.
func TestCatalogctlProcessContracts(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	binary := buildCatalogctlProcessBinary(t, ctx)
	t.Run("help and terminal failures", func(t *testing.T) {
		const sentinel = "PRIVATE_PROCESS_SENTINEL"
		missing := filepath.Join(canonicalTempDir(t), sentinel, "missing.json")
		for _, test := range []struct {
			name, prefix, stage string
			arguments           []string
		}{
			{name: "top-level help", prefix: "usage: catalogctl <", arguments: []string{"--help"}},
			{name: "ordinary flag help", prefix: "Usage of catalogctl config validate:", arguments: []string{"config", "validate", "--help"}},
			{name: "poisoned configuration control", stage: "config", arguments: []string{"config", "validate"}},
			{name: "missing command", stage: "command"},
			{name: "unknown command", stage: "command", arguments: []string{sentinel}},
			{name: "invalid numeric flag", stage: "probe", arguments: []string{"probe", "--expect-status", sentinel}},
			{name: "unreadable explicit config", stage: "config", arguments: []string{"config", "validate", "--config", missing}},
		} {
			t.Run(test.name, func(t *testing.T) {
				environment := append(catalogctlProcessEnvironment(t), "CATALOG_CONFIG_FILE="+missing, "CATALOG_DATABASE_DRIVER="+sentinel, "CATALOG_DATABASE_DSN="+sentinel, "CATALOG_REMOTE_BASE_URL="+sentinel, "CATALOG_REMOTE_TOKEN="+sentinel, "CATALOG_REMOTE_TOKEN_FILE="+missing)
				process := startCatalogctlProcess(t, ctx, binary, environment, test.arguments...)
				if test.prefix == "" {
					output, diagnostic := process.wait(t, 1)
					assertProcessTerminalFailure(t, output, diagnostic, test.stage, sentinel, missing)
					return
				}
				output, diagnostic := process.wait(t, 0)
				if !bytes.HasPrefix(output, []byte(test.prefix)) || len(diagnostic) != 0 || bytes.Contains(output, []byte(sentinel)) {
					t.Fatal("compiled help loaded runtime authority or disclosed environment defaults")
				}
			})
		}
	})
	t.Run("remote success and hostile problem streams", func(t *testing.T) {
		qualifyRemoteProcessStreams(t, ctx, binary)
	})
	t.Run("remote SIGTERM", func(t *testing.T) {
		qualifyRemoteProcessSignal(t, ctx, binary)
	})
	t.Run("pending login SIGTERM", func(t *testing.T) {
		qualifyLoginProcessSignal(t, ctx, binary)
	})
}

// qualifyRemoteProcessStreams checks real stdout fidelity and credential-safe JSON stderr.
func qualifyRemoteProcessStreams(t *testing.T, ctx context.Context, binary string) {
	t.Helper()
	const token = "private-process-token"
	const wire = `{"count":9007199254740993,"ratio":1.2300e+5,"extra":{"label":"private café🙂\u001b]52;clipboard\u0007\u202e"}}`
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/capabilities" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("compiled remote request lost its route or credential binding")
		}
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Request-ID", "process-success")
			w.Header().Set("ETag", `"rev-3"`)
			_, _ = io.WriteString(w, wire)
			return
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("X-Request-ID", "request-"+token)
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "code": "DENIED", "detail": token + " denied \x1b]52;clipboard\a\u202e café", "internal": "private-proxy-evidence"})
	}))
	defer server.Close()
	environment := func() []string { return append(catalogctlProcessEnvironment(t), "CATALOG_REMOTE_TOKEN="+token) }
	arguments := []string{"remote", "capabilities", "--base-url", server.URL}
	output, diagnostic := startCatalogctlProcess(t, ctx, binary, environment(), arguments...).wait(t, 0)
	if len(diagnostic) != 0 || calls.Load() != 1 {
		t.Fatal("successful remote process produced diagnostics or repeated its request")
	}
	var result struct {
		Data map[string]any `json:"data"`
		Meta struct {
			RequestID string `json:"requestId"`
			ETag      string `json:"etag"`
		} `json:"meta"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		t.Fatal("successful process did not emit one JSON envelope", err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || result.Data["count"] != json.Number("9007199254740993") || result.Data["ratio"] != json.Number("1.2300e+5") || result.Meta.RequestID != "process-success" || result.Meta.ETag != `"rev-3"` {
		t.Fatal("actual remote stdout changed number lexemes, metadata or framing")
	}
	label, ok := result.Data["extra"].(map[string]any)
	if !ok || label["label"] != "private café🙂\x1b]52;clipboard\a\u202e" {
		t.Fatal("actual remote stdout lost unknown Unicode/control metadata")
	}
	assertProcessJSONEscapes(t, output)
	output, diagnostic = startCatalogctlProcess(t, ctx, binary, environment(), arguments...).wait(t, 1)
	var failure remoteAPIError
	if len(output) != 0 || len(diagnostic) > 8192 || json.Unmarshal(diagnostic, &failure) != nil || calls.Load() != 2 || failure.Status != 403 || failure.Code != "DENIED" || failure.Detail != "[redacted] denied \x1b]52;clipboard\a\u202e café" || failure.RequestID != "request-[redacted]" {
		t.Fatal("compiled remote error lost status, redaction or one-request behavior")
	}
	if bytes.Contains(diagnostic, []byte(token)) || bytes.Contains(diagnostic, []byte("private-proxy-evidence")) {
		t.Fatal("compiled remote stderr disclosed credentials or ignored problem fields")
	}
	assertProcessJSONEscapes(t, diagnostic)
}

// assertProcessJSONEscapes preserves one terminal-safe JSON line without altering decoded data.
func assertProcessJSONEscapes(t *testing.T, output []byte) {
	t.Helper()
	if bytes.Count(output, []byte{'\n'}) != 1 || !bytes.HasSuffix(output, []byte{'\n'}) {
		t.Fatal("JSON process output is not exactly one line")
	}
	for _, value := range output {
		if value >= 127 || value < 32 && value != '\n' {
			t.Fatal("JSON process output contains an unescaped terminal or Unicode byte")
		}
	}
}

// qualifyRemoteProcessSignal observes a pending body before terminating its owning process.
func qualifyRemoteProcessSignal(t *testing.T, ctx context.Context, binary string) {
	t.Helper()
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/capabilities" || r.Header.Get("Authorization") != "Bearer signal-private-token" {
			t.Error("pending remote request lost its route or credential binding")
		}
		if calls.Add(1) != 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"recovered":true}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"private":"pending-body-sentinel",`)
		_ = http.NewResponseController(w).Flush()
		close(started)
		select {
		case <-r.Context().Done():
			close(canceled)
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	environment := func() []string {
		return append(catalogctlProcessEnvironment(t), "CATALOG_REMOTE_TOKEN=signal-private-token")
	}
	arguments := []string{"remote", "capabilities", "--base-url", server.URL, "--timeout", "1m"}
	process := startCatalogctlProcess(t, ctx, binary, environment(), arguments...)
	select {
	case <-started:
	case <-process.done:
		t.Fatal("remote child exited before the pending-response boundary")
	case <-time.After(10 * time.Second):
		t.Fatal("remote child never reached its owned API")
	}
	if err := process.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal("signal owned remote process", err)
	}
	output, diagnostic := process.wait(t, 1)
	assertProcessTerminalFailure(t, output, diagnostic, "remote", "signal-private-token", "pending-body-sentinel", server.URL)
	if !bytes.Contains(diagnostic, []byte("request cancelled or deadline exceeded")) || calls.Load() != 1 {
		t.Fatal("SIGTERM did not preserve the cancellation classification without replay")
	}
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("remote child left its pending upstream request alive")
	}
	output, diagnostic = startCatalogctlProcess(t, ctx, binary, environment(), arguments...).wait(t, 0)
	if len(diagnostic) != 0 || !bytes.Contains(output, []byte(`"recovered":true`)) || calls.Load() != 2 {
		t.Fatal("explicit later remote process did not recover")
	}
}

// qualifyLoginProcessSignal verifies callback and persistent-lock cleanup without a token exchange.
func qualifyLoginProcessSignal(t *testing.T, ctx context.Context, binary string) {
	t.Helper()
	var discovery, forbidden atomic.Int32
	var issuer *httptest.Server
	issuer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/realm/.well-known/openid-configuration" || r.Method != http.MethodGet {
			forbidden.Add(1)
			http.Error(w, "no authorization or exchange expected", http.StatusBadRequest)
			return
		}
		discovery.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL + "/realm", "authorization_endpoint": issuer.URL + "/realm/authorize", "token_endpoint": issuer.URL + "/realm/token", "jwks_uri": issuer.URL + "/realm/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
	}))
	defer issuer.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		forbidden.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer api.Close()
	directory := canonicalTempDir(t)
	if err := os.Chmod(directory, 0700); err != nil { // #nosec G302 -- Owned private credential fixture.
		t.Fatal(err)
	}
	ca, credentials := filepath.Join(directory, "issuer-ca.pem"), filepath.Join(directory, "login.json")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuer.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	arguments := []string{"auth", "login", "--issuer", issuer.URL + "/realm", "--issuer-ca-file", ca, "--base-url", api.URL, "--client-id", "process-client", "--file", credentials, "--timeout", "1m"}
	process := startCatalogctlProcess(t, ctx, binary, catalogctlProcessEnvironment(t), arguments...)
	var authorization string
	select {
	case authorization = <-process.stderr.authorization:
	case <-process.done:
		t.Fatal("login child exited before printing its owned callback")
	case <-time.After(10 * time.Second):
		t.Fatal("login child never reached the pending authorization boundary")
	}
	parsed, err := url.Parse(authorization)
	if err != nil || parsed.Scheme+"://"+parsed.Host+parsed.Path != issuer.URL+"/realm/authorize" || parsed.Query().Get("client_id") != "process-client" || parsed.Query().Get("state") == "" || parsed.Query().Get("code_challenge") == "" || parsed.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("login instructions lost their issuer, state or PKCE binding")
	}
	callback, err := url.Parse(parsed.Query().Get("redirect_uri"))
	if err != nil || callback.Scheme != "http" || callback.Hostname() != "127.0.0.1" || callback.Path != "/oauth/callback" {
		t.Fatal("login did not own an explicit loopback callback")
	}
	connection, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", callback.Host)
	if err != nil {
		t.Fatal("printed callback was not listening")
	}
	_ = connection.Close()
	if err := process.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal("signal owned login child", err)
	}
	output, diagnostic := process.wait(t, 1)
	if len(output) != 0 || !bytes.Contains(diagnostic, []byte("auth: login cancelled or deadline exceeded")) || bytes.Count(diagnostic, []byte("catalogctl: remote: INTERNAL:")) != 1 || discovery.Load() != 1 || forbidden.Load() != 0 {
		t.Fatal("pending login signal lost its classified refusal or contacted a token/API endpoint")
	}
	if bytes.Contains(diagnostic, []byte(credentials)) || bytes.Contains(diagnostic, []byte(ca)) {
		t.Fatal("canceled login printed private local paths")
	}
	if connection, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", callback.Host); err == nil {
		_ = connection.Close()
		t.Fatal("canceled login retained its callback listener")
	}
	if _, err := os.Stat(credentials); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pending login wrote credentials")
	}
	output, diagnostic = startCatalogctlProcess(t, ctx, binary, catalogctlProcessEnvironment(t), "auth", "status", "--file", credentials, "--timeout", "2s").wait(t, 0)
	var status map[string]bool
	if len(diagnostic) != 0 || json.Unmarshal(output, &status) != nil || len(status) != 3 || discovery.Load() != 1 || forbidden.Load() != 0 {
		t.Fatal("fresh status invocation could not acquire the released credential lock without network")
	}
	for _, field := range []string{"loggedIn", "expired", "canRefresh"} {
		if value, present := status[field]; !present || value {
			t.Fatal("fresh status invocation did not report an absent credential")
		}
	}
}
