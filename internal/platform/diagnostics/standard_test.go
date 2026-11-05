package diagnostics

import (
	"bytes"
	"log"
	"log/slog"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// Standard logger ownership is process-global; these cases must stay serial and
// restore both Go's logger and the command-installed adapter after each test.
func preserveStandardLog(t *testing.T) {
	t.Helper()
	writer, flags, prefix, fallback := log.Writer(), log.Flags(), log.Prefix(), standardFallback.Load()
	t.Cleanup(func() {
		log.SetOutput(writer)
		log.SetFlags(flags)
		log.SetPrefix(prefix)
		standardFallback.Store(fallback)
	})
}

func TestStandardDiagnosticsRespectProcessPolicyWithoutPrivateText(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		for _, level := range []string{"debug", "info", "warn", "error"} {
			t.Run(format+"/"+level, func(t *testing.T) {
				preserveStandardLog(t)
				originalSlog := slog.Default()
				ProtectStandardLog("catalog-ui")
				var output bytes.Buffer
				logger := (Process{Service: "catalog-ui", Role: "ui", Environment: "test", Version: "fixture"}).Logger(NewLogger(&output, level, format))
				Startup(logger, "fixture", level, format, false)
				output.Reset()
				for range 3 {
					log.Print("SYNTHETIC_PRIVATE_RESPONSE\npassword=private")
				}
				if slog.Default() != originalSlog || strings.Contains(output.String(), "private") || strings.Contains(output.String(), "SYNTHETIC") {
					t.Fatal("standard fallback changed slog ownership or exposed private data")
				}
				if level == "error" {
					if output.Len() != 0 {
						t.Fatal("standard warning ignored the configured threshold")
					}
				} else if strings.Count(output.String(), "dependency.diagnostic") != 1 || strings.Count(output.String(), "\n") != 1 || !strings.Contains(output.String(), "WARN") || !strings.Contains(output.String(), "fixture") {
					t.Fatal("missing, unbounded or incorrectly configured standard diagnostic")
				}
			})
		}
	}
}

func TestStandardDiagnosticsBootstrapAndConfiguredHandoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		preserveStandardLog(t)
		bootstrap, err := os.CreateTemp(t.TempDir(), "bootstrap")
		if err != nil {
			t.Fatal(err)
		}
		stderr := os.Stderr
		t.Cleanup(func() { os.Stderr = stderr; _ = bootstrap.Close() })
		os.Stderr = bootstrap
		ProtectStandardLog("catalogd")
		os.Stderr = stderr
		// A raw dependency line before validated configuration must already be safe.
		log.Print("SYNTHETIC_PRIVATE_BOOTSTRAP\npassword=private")
		raw, err := os.ReadFile(bootstrap.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"level=WARN", "event=dependency.diagnostic", "service=catalogd", "role=unknown", "suppressed=0"} {
			if !strings.Contains(string(raw), want) {
				t.Fatalf("bootstrap missing %s: %s", want, raw)
			}
		}
		if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "password") || strings.Count(string(raw), "\n") != 1 {
			t.Fatal("bootstrap exposed private data or emitted multiple records")
		}
		var output bytes.Buffer
		logger := (Process{Service: "catalogd", Role: "worker", Environment: "test", Version: "fixture"}).Logger(NewLogger(&output, "warn", "json"))
		Startup(logger, "fixture", "warn", "json", false)
		// Suppression survives the handoff; use virtual time instead of sleeping.
		log.Print("SYNTHETIC_PRIVATE_SUPPRESSED")
		if output.Len() != 0 {
			t.Fatal("configuration handoff reset the repeat window")
		}
		time.Sleep(time.Minute)
		log.Print("SYNTHETIC_PRIVATE_CONFIGURED")
		if strings.Count(output.String(), "\n") != 1 || !strings.Contains(output.String(), `"role":"worker"`) || !strings.Contains(output.String(), `"suppressed":1`) || strings.Contains(output.String(), "PRIVATE") {
			t.Fatalf("configured handoff: %s", output.String())
		}
		after, err := os.ReadFile(bootstrap.Name())
		if err != nil || !bytes.Equal(raw, after) {
			t.Fatal("configured standard logging continued writing to bootstrap stderr")
		}
	})
}
