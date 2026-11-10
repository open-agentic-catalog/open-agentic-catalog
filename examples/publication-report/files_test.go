package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Every caller supplies a path in its own test directory.
func replacePrivateTest(t *testing.T, path string, raw []byte) {
	t.Helper()
	replacement := path + ".replacement"
	// #nosec G703 -- Only invocation-owned fixture paths are atomically replaced.
	if err := os.WriteFile(replacement, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedInputMissingCases(t *testing.T) {
	for _, kind := range []string{"absent", "empty", "whitespace", "symlink", "directory", "group readable"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input")
			switch kind {
			case "empty", "whitespace", "group readable":
				raw := []byte{}
				if kind == "whitespace" {
					raw = []byte(" \n\t")
				}
				if kind == "group readable" {
					raw = []byte("protected")
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if kind == "group readable" { // #nosec G302 -- Prove the credential policy refuses a group-readable fixture.
					if err := os.Chmod(path, 0640); err != nil {
						t.Fatal(err)
					}
				}
			case "symlink":
				target := writePrivate(t, "target", []byte("protected"))
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			_, err := readToken(t.Context(), path)
			want := "private_input_unavailable"
			if kind == "whitespace" {
				want = "empty_token"
			}
			requireCode(t, err, want)
			valid := writePrivate(t, "valid", []byte(" \tprotected\n"))
			if token, err := readToken(t.Context(), valid); err != nil || token != "protected" {
				t.Fatal("valid private neighbor refused", err)
			}
		})
	}
}

func TestTokenCallerLimitAndReplacement(t *testing.T) {
	exact := strings.Repeat("a", 16384)
	path := writePrivate(t, "token", []byte(exact))
	if token, err := readToken(t.Context(), path); err != nil || token != exact {
		t.Fatal("exact token-file byte limit refused", err)
	}
	replacePrivateTest(t, path, []byte(exact+"a"))
	_, err := readToken(t.Context(), path)
	requireCode(t, err, "private_input_unavailable")
	replacePrivateTest(t, path, []byte(" replacement-token\n"))
	if token, err := readToken(t.Context(), path); err != nil || token != "replacement-token" {
		t.Fatal("token was cached or replacement refused", err)
	}
}

func TestSignerDocumentRefusalsAndByteLimit(t *testing.T) {
	valid := `{"secret":"` + testSecret + `","version":7}`
	for name, body := range map[string]string{
		"malformed": `{`, "duplicate": strings.TrimSuffix(valid, "}") + `,"version":8}`,
		"unknown field":                strings.TrimSuffix(valid, "}") + `,"extra":true}`,
		"wrong prefix":                 strings.Replace(valid, "whsec_", "other_", 1),
		"invalid configured character": strings.Replace(valid, "whsec_a", "whsec_!", 1),
		"zero version":                 strings.Replace(valid, `"version":7`, `"version":0`, 1),
		"noninteger version":           strings.Replace(valid, `"version":7`, `"version":1.5`, 1),
		"absent configured key":        `{"version":7}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := writePrivate(t, "signer", []byte(valid))
			if key, err := loadSigner(path); err != nil || key.Secret != testSecret || key.Version != 7 {
				t.Fatal("valid signer neighbor refused", err)
			}
			replacePrivateTest(t, path, []byte(body))
			_, err := loadSigner(path)
			requireCode(t, err, "invalid_signer")
		})
	}
	path := writePrivate(t, "signer", []byte(valid+strings.Repeat(" ", 4096-len(valid))))
	if key, err := loadSigner(path); err != nil || key.Secret != testSecret || key.Version != 7 {
		t.Fatal("exact signer-file byte limit refused", err)
	}
	replacePrivateTest(t, path, []byte(valid+strings.Repeat(" ", 4097-len(valid))))
	_, err := loadSigner(path)
	requireCode(t, err, "private_input_unavailable")
}

func savedFixture() Saved {
	e := publicationEvent()
	report := expectedReport(&e)
	attempt := testTime.Add(time.Minute)
	lossAt := testTime.Add(-time.Minute)
	return Saved{LastSuccessful: &report, LastAttemptAt: &attempt, RefreshFailure: "catalog_read_failed", CapacityLoss: 2, DeliveryLoss: 7, LossObservedAt: &lossAt}
}
func TestApplicationRenderFailureAndPrivateReplacement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "report.json")
	files, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := files.Close(); err != nil {
			t.Error(err)
		}
	})
	// #nosec G306 G703 -- A public fixture proves replacement repairs output permissions.
	if err := os.WriteFile(path, []byte("old synthetic report"), 0644); err != nil {
		t.Fatal(err)
	}
	fail := true
	reads := 0
	s := &storeCalls{read: func(context.Context) (Saved, error) {
		reads++
		if fail {
			return Saved{}, errors.New("PRIVATE_READ_CAUSE")
		}
		return savedFixture(), nil
	}}
	a := &application{store: s, catalog: &catalogCalls{}, output: path}
	requireCode(t, a.render(t.Context()), "report_read_failed")
	raw, err := files.ReadFile("report.json")
	if err != nil || string(raw) != "old synthetic report" {
		t.Fatal("read failure changed existing output", err)
	}
	fail = false
	if err := a.render(t.Context()); err != nil {
		t.Fatal(err)
	}
	raw, err = files.ReadFile("report.json")
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"lastSuccessfulObservation":{"label":"Last observed publication state","observedAt":"2026-10-25T12:00:00Z","tenant":"` + testTenant + `","host":"catalog.example.test","active":{"id":"` + testTarget + `","tenantId":"` + testTenant + `","host":"catalog.example.test","state":"active","generation":8,"audience":"anonymous","etag":"\"` + strings.Repeat("b", 64) + `\"","createdAt":"2026-10-25T10:00:00Z"},"trigger":{"id":"` + testID + `","source":"` + eventSource + testTenant + `","action":"publication.activated","targetId":"` + testTarget + `","snapshotStateDigest":"sha256:` + strings.Repeat("a", 64) + `"}},"lastAttemptAt":"2026-10-25T12:01:00Z","refreshFailure":"catalog_read_failed","capacityLossCount":2,"deliveryLossCount":7,"lossCountsObservedAt":"2026-10-25T11:59:00Z"}`
	var got, want any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		t.Fatal("invalid independent JSON oracle", err)
	}
	if !reflect.DeepEqual(got, want) || reads != 2 || !strings.HasSuffix(string(raw), "\n") {
		t.Fatal("rendered output differs from the complete JSON contract")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("replacement retained public permissions", err)
	}
}

func TestRenderRenameFailureCleansTemporaryFile(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "directory")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(target, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	requireCode(t, render(target, savedFixture()), "report_render_failed")
	files, err := os.OpenRoot(parent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := files.Close(); err != nil {
			t.Error(err)
		}
	})
	raw, err := files.ReadFile("directory/sentinel")
	if err != nil || string(raw) != "keep" {
		t.Fatal("rename failure damaged destination", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 || entries[0].Name() != "directory" {
		t.Fatal("rename failure retained temporary output", err)
	}
}
