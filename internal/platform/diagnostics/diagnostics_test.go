package diagnostics

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

type privateError struct{}

func (privateError) Error() string { panic("private-error-stringifier") }

func TestTerminalProjectionNeverFormatsPrivateCauses(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, err := range []error{privateError{}, safeerr.Wrap(privateError{}, "reviewed guidance"), safeerr.ErrPanic} {
			var output bytes.Buffer
			Report(&output, "catalog", "INTERNAL", err, service)
			text := output.String()
			if strings.Contains(text, "private-error-stringifier") || !strings.Contains(text, safeerr.Message(err)) || strings.Count(text, "\n") != 1 {
				t.Fatalf("terminal: %q", text)
			}
			if service && (!strings.Contains(text, "level=ERROR") || !strings.Contains(text, "event=process.failed")) {
				t.Fatalf("service diagnostic: %q", text)
			}
		}
	}
}

func TestFlagErrorsRetainCauseWithoutImplicitOutput(t *testing.T) {
	for _, arguments := range [][]string{{"--private-secret"}, {"--count", "private-secret"}} {
		flags := Flags("catalog test")
		flags.Int("count", 1, "bounded count")
		var implicit bytes.Buffer
		flags.SetOutput(&implicit)
		err := Parse(flags, arguments)
		if err == nil || implicit.Len() != 0 || !strings.Contains(err.Error(), "private-secret") || strings.Contains(safeerr.Message(err), "private-secret") {
			t.Fatalf("flag error=%v, implicit=%q", err, implicit.String())
		}
	}
}

func TestFlagHelpOmitsPrivateDefaultsAndSuppliedValues(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "help")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	t.Cleanup(func() { os.Stdout = previous; _ = output.Close() })
	os.Stdout = output
	flags := Flags("catalog test")
	flags.String("token-file", "SYNTHETIC_PRIVATE_DEFAULT", "path to credential file")
	err = Parse(flags, []string{"--token-file", "SYNTHETIC_PRIVATE_ARGUMENT", "--help"})
	os.Stdout = previous
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help identity lost: %v", err)
	}
	raw, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "Usage of catalog test:\n  -token-file\n    path to credential file\n" {
		t.Fatalf("help exposed values or changed declarations: %q", raw)
	}
}

func TestHTTPFallbackAndRecoveryAreSafeAndDoNotDuplicateAborts(t *testing.T) {
	for _, late := range []bool{false, true} {
		for _, fault := range []struct {
			value any
			abort bool
		}{
			{privateError{}, false},
			{"private-panic", false},
			{nil, false},
			{http.ErrAbortHandler, true},
			{fmt.Errorf("private-wrapper: %w", http.ErrAbortHandler), true},
		} {
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, nil))
			h := RecoverHTTP(logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if late {
					_, _ = w.Write([]byte("already committed"))
				}
				panic(fault.value)
			}))
			recorder := httptest.NewRecorder()
			func() {
				defer func() {
					got, ok := recover().(error)
					if !ok || !errors.Is(got, http.ErrAbortHandler) {
						t.Errorf("connection abort=%v", got)
					}
				}()
				h.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), "GET", "/private-path", nil))
				t.Error("panic resumed handler")
			}()
			if strings.Contains(recorder.Body.String(), "private") || strings.Contains(output.String(), "private") || strings.Contains(output.String(), "goroutine") {
				t.Fatal("private panic leaked")
			}
			wantBody := ""
			if late {
				wantBody = "already committed"
			}
			if recorder.Body.String() != wantBody {
				t.Fatal("panic recovery changed the committed response")
			}
			want := 1
			if fault.abort {
				want = 0
			}
			if strings.Count(output.String(), "\n") != want {
				t.Fatalf("diagnostic count: %q", output.String())
			}
		}
	}
	var output bytes.Buffer
	fallback := HTTPErrorLog(slog.New(slog.NewTextHandler(&output, nil)))
	fallback.Print("http: panic serving private-peer: private-panic\nprivate-stack")
	if strings.Count(output.String(), "\n") != 1 || strings.Contains(output.String(), "private") || !strings.Contains(output.String(), "level=ERROR") {
		t.Fatalf("fallback=%q", output.String())
	}
	// An ordinary request keeps its response semantics and emits no error.
	output.Reset()
	response := httptest.NewRecorder()
	RecoverHTTP(slog.New(slog.NewTextHandler(&output, nil)), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), "GET", "/", nil))
	if response.Code != http.StatusNoContent || output.Len() != 0 {
		t.Fatal("healthy request changed")
	}
}
