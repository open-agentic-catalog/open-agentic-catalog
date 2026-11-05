// Unit tests exercise isolated validation and controlled dependencies.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/controlclient"
)

func remoteTestEnvironment(origin, token string) func(string) string {
	return func(name string) string {
		switch name {
		case "CATALOG_REMOTE_BASE_URL":
			return origin
		case "CATALOG_REMOTE_TOKEN":
			return token
		default:
			return ""
		}
	}
}

func TestRemoteArguments(t *testing.T) {
	t.Parallel()
	_, kindErr := parseRemote([]string{"search", "--kind", "unsupported"}, remoteTestEnvironment("", ""))
	if kindErr == nil || !strings.Contains(kindErr.Error(), "agent, mcp_server, skill or plugin") {
		t.Fatalf("kind help omits supported resource kinds: %v", kindErr)
	}
	for _, args := range [][]string{
		{"unknown"}, {"capabilities", "extra"}, {"capabilities", "--token", "secret"},
		{"capabilities", "--timeout", "secret"}, {"capabilities", "--timeout", "0"}, {"capabilities", "--timeout", "3m"},
		{"capabilities", "--id", "secret"}, {"record"}, {"job", "--id", "../secret"},
		{"search", "--limit", "0"}, {"search", "--limit", "101"}, {"search", "--kind", "unsupported"},
		{"search", "--kind", "agent", "--kind", "agent"}, {"search", "--kind", "agent", "--kind", "skill", "--kind", "mcp_server", "--kind", "agent"},
		{"search", "--kind", "agent", "--kind", "mcp_server", "--kind", "skill", "--kind", "plugin", "--kind", "agent"},
		{"search", "--visibility", "secret"}, {"search", "--text", strings.Repeat("x", 501)},
		{"search", "--publisher", strings.Repeat("x", 257)}, {"search", "--cursor", strings.Repeat("x", catalog.MaxCatalogCursorBytes+1)},
		{"search", "--tag", strings.Repeat("x", 81)}, {"search", "--text", string([]byte{0xff})},
		{"search", "--text", strings.Repeat("é", 251)}, {"search", "--tag", strings.Repeat("é", 81)},
		{"search", "--tag", strings.Repeat("\U0001f642", 81)}, {"search", "--tag", "\xff"}, {"search", "--tag", "nul\x00"},
		{"search", "--publisher", "nul\x00"}, {"search", "--text", "nul\x00"},
		append([]string{"search"}, strings.Fields(strings.Repeat("--tag x ", 33))...),
		{"search", "--license", strings.Repeat("🙂", 257)}, {"search", "--license", " \t "}, {"search", "--license", "x\x00"}, {"search", "--license", "\xff"},
		{"search", "--category", strings.Repeat("🙂", 81)}, {"search", "--category", " "}, {"search", "--category", "\xff"}, {"search", "--category", "x\x00"},
		append([]string{"search"}, strings.Fields(strings.Repeat("--category tools ", 17))...),
		{"search", "--origin", "secret"}, {"search", "--origin", "local", "--origin", "local"},
		{"search", "--origin", "local", "--origin", "indexed", "--origin", "live", "--origin", "local"},
		{"search", "--namespace", "secret"}, {"search", "--namespace", "00000000-0000-7000-8000-000000000001", "--origin", "indexed"},
	} {
		_, err := parseRemote(args, remoteTestEnvironment("", ""))
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("unsafe/accepted args %q: %v", args, err)
		}
	}
	// Returned cursors are opaque to clients, including live-only continuation.
	continued, err := parseRemote([]string{"search", "--origin", "live", "--text", "tools", "--cursor", "opaque.returned-token"}, remoteTestEnvironment("", ""))
	if err != nil || continued.search.Cursor != "opaque.returned-token" || !reflect.DeepEqual(continued.search.Origins, []catalog.SearchResultType{catalog.SearchResultLive}) {
		t.Fatalf("live-only continuation changed or refused: %+v %v", continued.search, err)
	}
	// Four distinct kinds are valid; the fifth-kind bound must not prevent a
	// caller from searching every supported resource kind in one request.
	allKinds, err := parseRemote([]string{"search", "--kind", "agent", "--kind", "mcp_server", "--kind", "skill", "--kind", "plugin"}, remoteTestEnvironment("", ""))
	if err != nil || !reflect.DeepEqual(allKinds.search.Kinds, []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill, catalog.KindPlugin}) {
		t.Fatalf("all-kind search was rejected or changed: %v", err)
	}
	for _, origin := range []string{"local", "indexed", "live"} {
		options, err := parseRemote([]string{"search", "--license", strings.Repeat("🙂", 256), "--category", strings.Repeat("🙂", 80), "--category", " TOOLS ", "--origin", origin}, remoteTestEnvironment("", ""))
		if err != nil || options.search.License != strings.Repeat("🙂", 256) || !reflect.DeepEqual(options.search.Categories, []string{strings.Repeat("🙂", 80), " TOOLS "}) || !reflect.DeepEqual(options.search.Origins, []catalog.SearchResultType{catalog.SearchResultType(origin)}) {
			t.Fatalf("valid declaration flags changed: %+v %v", options.search, err)
		}
	}
	if options, err := parseRemote([]string{"search", "--namespace", "00000000-0000-7000-8000-000000000001"}, remoteTestEnvironment("", "")); err != nil || options.search.NamespaceID != "00000000-0000-7000-8000-000000000001" {
		t.Fatal("valid local namespace refused", err)
	}
	// Validation must preserve accepted Unicode and surrounding whitespace for
	// the server to normalize, rather than changing exact transmitted filters.
	for _, tag := range []string{strings.Repeat("a", 80), strings.Repeat("\u00e9", 41), strings.Repeat("\u00e9", 80), strings.Repeat("\U0001f642", 80), strings.Repeat("e\u0301", 40), " " + strings.Repeat("\u00e9", 80) + " "} {
		options, err := parseRemote([]string{"search", "--tag", tag}, remoteTestEnvironment("", ""))
		if err != nil || len(options.search.Tags) != 1 || options.search.Tags[0] != tag {
			t.Fatalf("Unicode tag was rejected or changed: %v", err)
		}
	}
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"search", "--help"}} {
		var out bytes.Buffer
		if err := runRemote(context.Background(), args, remoteTestEnvironment("", ""), &out); err != nil || !strings.Contains(out.String(), "--token-file") {
			t.Fatalf("help: %v %s", err, &out)
		}
	}
	env := func(name string) string {
		return map[string]string{"CATALOG_REMOTE_BASE_URL": "https://env.test", "CATALOG_REMOTE_TOKEN": "env-token", "CATALOG_REMOTE_TOKEN_FILE": "env-file", "CATALOG_REMOTE_CA_FILE": "env-ca"}[name]
	}
	options, err := parseRemote([]string{"capabilities", "--base-url", "https://flag.test", "--token-file", "flag-file", "--ca-file", "flag-ca"}, env)
	if err != nil || options.baseURL != "https://flag.test" || options.tokenFile != "flag-file" || options.caFile != "flag-ca" || options.token != "" {
		t.Fatalf("precedence: %#v %v", options, err)
	}
}

func TestRemoteOriginAndTokens(t *testing.T) {
	t.Parallel()
	for _, origin := range []string{"https://catalog.example", "https://10.0.0.1:8443", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		if _, err := remoteOrigin(origin); err != nil {
			t.Errorf("valid origin %s: %v", origin, err)
		}
	}
	for _, origin := range []string{"", "file:///secret", "ftp://catalog.example", "http://localhost", "http://10.0.0.1", "https://secret@host", "https://host/", "https://host/api/v1", "https://host?", "https://host#", "https://host?secret", "https://host#secret", "https://host:0", "https://host:65536", "https://host:", "https://host:bad", "http://[::1%25lo]", "https:opaque", strings.Repeat("x", 2049)} {
		if _, err := remoteOrigin(origin); err == nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("invalid origin accepted/leaked: %q %v", origin, err)
		}
	}
	for _, token := range []string{"", "secret\nInjected:yes", "secret with spaces", strings.Repeat("x", remoteMaxToken+1), "é"} {
		if controlclient.ValidToken(token) {
			t.Errorf("invalid bearer accepted")
		}
	}
	if !controlclient.ValidToken("AZaz09-._~+/=") {
		t.Fatal("valid bearer rejected")
	}
	if _, err := parseRemote([]string{"search", "--text", strings.Repeat("é", 250)}, remoteTestEnvironment("", "")); err != nil {
		t.Fatalf("valid multibyte boundary rejected: %v", err)
	}
}

func TestRemoteSafeJSON(t *testing.T) {
	t.Parallel()
	want := map[string]string{"text": "ordinary café 日本語 😀 \x1b]52;secret\a\r\n\u202e\u009b\u007f"}
	encoded, err := remoteJSON(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range encoded {
		if value < 32 || value >= 127 {
			t.Fatalf("unescaped control/Unicode: %q", encoded)
		}
	}
	var got map[string]string
	if err := json.Unmarshal(encoded, &got); err != nil || !reflect.DeepEqual(want, got) {
		t.Fatalf("lossy Unicode: %q %v", encoded, err)
	}
}

func TestRemoteLocalInputFailures(t *testing.T) {
	t.Parallel()
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"capabilities", "--base-url", "invalid"},
		{"capabilities", "--token-file", tokenPath},
		{"capabilities", "--token-file", tokenPath + "missing"},
		{"capabilities", "--ca-file", tokenPath},
		{"capabilities", "--ca-file", tokenPath + "missing"},
	} {
		var out bytes.Buffer
		err := runRemote(context.Background(), args, remoteTestEnvironment("http://127.0.0.1:8080", "token"), &out)
		if err == nil || out.Len() != 0 || strings.Contains(err.Error(), tokenPath) {
			t.Fatalf("local failure: %v", err)
		}
	}
	if err := runRemote(context.Background(), []string{"capabilities"}, remoteTestEnvironment("http://127.0.0.1:8080", ""), io.Discard); err == nil {
		t.Fatal("missing token accepted")
	}
	if _, err := remoteJSON(make(chan int)); err == nil {
		t.Fatal("invalid output value encoded")
	}
	if got := remoteDiagnostic(strings.Repeat("é", 1100), "token", 4); got != "éééé..." {
		t.Fatalf("diagnostic bound: %q", got)
	}
	if err := runArgs([]string{"remote", "capabilities", "--token", "secret"}); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("dispatch failure: %v", err)
	}
}

func FuzzRemoteOrigin(f *testing.F) {
	for _, value := range []string{"http://127.0.0.1:8080", "https://catalog.test", "http://localhost", "https://user@host", "https://[::1%25lo]/", "https://host:bad"} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		origin, err := remoteOrigin(value)
		if err == nil && (origin == "" || strings.ContainsAny(origin, "\r\n")) {
			t.Fatal("unsafe accepted origin")
		}
	})
}

func FuzzRemoteJSON(f *testing.F) {
	f.Add("café 😀 \x1b[0m\u202e")
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 16384 {
			return
		}
		// encoding/json's normal invalid-UTF8 replacement remains explicit.
		value = strings.ToValidUTF8(value, "�")
		encoded, err := remoteJSON(value)
		var decoded string
		if err != nil || json.Unmarshal(encoded, &decoded) != nil || decoded != value {
			t.Fatal("JSON round trip failed")
		}
		for _, b := range encoded {
			if b < 32 || b >= 127 {
				t.Fatal("unsafe output byte")
			}
		}
	})
}

func TestRemoteExactRecordResponseBudget(t *testing.T) {
	for _, limit := range []string{"1", "8388608", "67108864"} {
		options, err := parseRemote([]string{"record", "--id", "00000000-0000-7000-8000-000000000001", "--max-response-bytes", limit}, remoteTestEnvironment("", ""))
		want, parseErr := strconv.ParseInt(limit, 10, 64)
		if err != nil || parseErr != nil || options.maxResponseBytes != want || options.path != "/api/v1/catalog/resources/00000000-0000-7000-8000-000000000001" {
			t.Fatalf("valid budget rejected: %v", err)
		}
	}
	for _, args := range [][]string{
		{"record", "--id", "00000000-0000-7000-8000-000000000001", "--max-response-bytes", "0"},
		{"record", "--id", "00000000-0000-7000-8000-000000000001", "--max-response-bytes", "67108865"},
		{"record", "--id", "00000000-0000-7000-8000-000000000001", "--max-response-bytes", "-1"},
		{"search", "--max-response-bytes", "16777216"},
		{"job", "--id", "00000000-0000-7000-8000-000000000001", "--max-response-bytes", "16777216"},
	} {
		if _, err := parseRemote(args, remoteTestEnvironment("", "")); err == nil {
			t.Fatal("invalid or unscoped override accepted")
		}
	}
}

func TestRemoteReindexArgumentBoundaries(t *testing.T) {
	t.Parallel()
	const snapshot = "00000000-0000-7000-8000-000000000041"
	for _, tc := range []struct {
		name, reason, key, id string
		valid                 bool
	}{
		{"minimum key", " reason ", "!234567~", "", true},
		{"maximum key and reason", strings.Repeat("é", 1000), strings.Repeat("k", 128), snapshot, true},
		{"short key", "reason", "1234567", "", false},
		{"long key", "reason", strings.Repeat("k", 129), "", false},
		{"non ASCII key", "reason", "1234567é", "", false},
		{"DEL key", "reason", "1234567\x7f", "", false},
		{"long byte reason", strings.Repeat("é", 1000) + "x", "12345678", "", false},
		{"invalid UTF8 reason", "reason\xff", "12345678", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"reindex", "--reason", tc.reason, "--idempotency-key", tc.key, "--snapshot-id", tc.id}
			got, err := parseRemote(args, remoteTestEnvironment("https://catalog.invalid", "unit-token"))
			if tc.valid {
				if err != nil || got.path != "/api/v1/catalog:reindex" || got.reindex == nil || got.reindex.Reason != tc.reason || got.reindex.SnapshotID != tc.id || got.idempotencyKey != tc.key || got.search != nil || got.token != "unit-token" {
					t.Fatalf("lost command binding: %#v err=%v", got, err)
				}
			} else {
				const want = "remote: reindex requires a bounded reason, an 8-128 character ASCII idempotency key and an optional snapshot UUID"
				if err == nil || err.Error() != want || got.path != "" {
					t.Fatalf("invalid command path=%q err=%v", got.path, err)
				}
			}
		})
	}
}

func TestRemoteTimeoutAndSearchArgumentBindings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		text string
		want time.Duration
	}{{"1ns", time.Nanosecond}, {"2m", 2 * time.Minute}} {
		got, err := parseRemote([]string{"search", "--timeout", tc.text, "--publisher", " publisher ", "--cursor", "opaque+/=", "--limit", "100", "--tag", "one", "--tag", "two", "--visibility", "tenant"}, remoteTestEnvironment("https://catalog.invalid", "token"))
		want := &remoteSearch{Publisher: " publisher ", Cursor: "opaque+/=", Limit: 100, Tags: []string{"one", "two"}, Visibility: catalog.VisibilityTenant}
		if err != nil || got.timeout != tc.want || got.path != "/api/v1/catalog/search" || !reflect.DeepEqual(got.search, want) {
			t.Fatalf("argument binding=%#v err=%v", got, err)
		}
	}
	for _, timeout := range []string{"-1ns", "2m1ns"} {
		got, err := parseRemote([]string{"capabilities", "--timeout", timeout}, remoteTestEnvironment("", ""))
		if err == nil || err.Error() != "remote: unexpected arguments or timeout outside (0, 2m]" || got.path != "" {
			t.Fatalf("timeout path=%q err=%v", got.path, err)
		}
	}
}

func TestRemoteSearchCursorExactLimit(t *testing.T) {
	for _, n := range []int{4096, 4097} {
		options, err := parseRemote([]string{"search", "--cursor", strings.Repeat("x", n)}, remoteTestEnvironment("", ""))
		if (err == nil) != (n == 4096) || err == nil && len(options.search.Cursor) != n {
			t.Fatalf("Search cursor %d: %v", n, err)
		}
	}
}

func TestRemoteTrustSelectionArguments(t *testing.T) {
	const source = "00000000-0000-7000-8000-000000000010"
	path := filepath.Join(t.TempDir(), "request.json")
	valid := `{"sourceRevision":1,"identifier":"urn:air:peer.example:agents:one","versionPresent":false,"mediaType":"application/a2a-agent-card+json","delivery":"url","exactEntryDigest":"sha256:` + strings.Repeat("a", 64) + `","text":"one","reason":"Verify registration"}`
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"trust-request", "--source-id", source, "--request-file", path, "--idempotency-key", "trust-request-1"}
	parsed, err := parseRemote(args, remoteTestEnvironment("https://catalog.example", "token"))
	if err != nil || parsed.trustRequest == nil || parsed.path != "/api/v1/federation/sources/"+source+"/trust-assessments" {
		t.Fatalf("request flags: %v", err)
	}
	for _, body := range []string{`{"SourceRevision":1}`, `{"sourceRevision":1,"sourceRevision":2}`, `{"text":null}`, strings.Repeat("x", (64<<10)+1)} {
		if err = os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = parseRemote(args, remoteTestEnvironment("https://catalog.example", "token")); err == nil {
			t.Fatal("invalid request file accepted")
		}
	}
	for _, args := range [][]string{{"trust-result", "--source-id", source, "--id", "../bad"}, {"trust-request", "--source-id", "../bad"}, {"trust-request", "--source-id", source, "--request-file", path, "--idempotency-key", "short"}} {
		if _, err = parseRemote(args, remoteTestEnvironment("https://catalog.example", "token")); err == nil {
			t.Fatal("invalid trust command accepted")
		}
	}
	got, err := parseRemote([]string{"trust-result", "--source-id", source, "--id", source}, remoteTestEnvironment("https://catalog.example", "token"))
	if err != nil || got.path != "/api/v1/federation/sources/"+source+"/trust-assessments/"+source {
		t.Fatal("private result path", err)
	}
}
