package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func TestLoggingFormatsAndThresholds(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		for _, threshold := range []string{"debug", "info", "warn", "error"} {
			t.Run(format+"/"+threshold, func(t *testing.T) {
				if err := ValidateLogging(threshold, format); err != nil {
					t.Fatal(err)
				}
				var output bytes.Buffer
				logger := NewLogger(&output, threshold, format)
				levels := []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError}
				first := map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}[threshold]
				for _, level := range levels {
					logger.Log(context.Background(), level, "fixed event", "event", "fixture", "detail", "quoted\nline")
				}
				lines := strings.Split(strings.TrimSpace(output.String()), "\n")
				if len(lines) != 4-first {
					t.Fatalf("threshold: %q", output.String())
				}
				for i, line := range lines {
					if format == "json" {
						var fields map[string]any
						if err := json.Unmarshal([]byte(line), &fields); err != nil {
							t.Fatal(err)
						}
						if fields["level"] != levels[first+i].String() || fields["event"] != "fixture" || fields["detail"] != "quoted\nline" {
							t.Fatalf("record: %v", fields)
						}
					} else if !strings.Contains(line, "level="+levels[first+i].String()) || !strings.Contains(line, `detail="quoted\nline"`) {
						t.Fatalf("record: %s", line)
					}
				}
			})
		}
	}
	for _, pair := range [][2]string{{"", "text"}, {"INFO", "text"}, {"off", "json"}, {"debug", ""}, {"warn", "JSON"}, {"secret", "secret"}} {
		if err := ValidateLogging(pair[0], pair[1]); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid/private settings: %v", err)
		}
	}
}

func TestLoggingConcurrentAndFailedWrites(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		var buffer bytes.Buffer
		logger := NewLogger(&buffer, "info", format)
		var group sync.WaitGroup
		for range 16 {
			group.Go(func() {
				for range 20 {
					logger.Info("fixed", "event", "fixture")
				}
			})
		}
		group.Wait()
		lines := strings.Split(strings.TrimSpace(buffer.String()), "\n")
		if len(lines) != 320 {
			t.Fatalf("lost records: %d", len(lines))
		}
		for _, line := range lines {
			if format == "json" && !json.Valid([]byte(line)) {
				t.Fatalf("interleaved record: %s", line)
			}
		}
		output := &failedLogWriter{}
		logger = NewLogger(output, "error", format)
		logger.Info("filtered")
		logger.Error("fixed failure")
		if output.calls != 1 {
			t.Fatalf("failure retried or filtered log written: %d", output.calls)
		}
	}
}

type failedLogWriter struct{ calls int }

func (w *failedLogWriter) Write([]byte) (int, error) {
	w.calls++
	return 0, errors.New("private sink failure")
}

func TestStartupProjectionAndTerminalFormat(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		var output bytes.Buffer
		logger := (Process{"catalog", "worker", "test", "1.2.3+build"}).Logger(NewLogger(&output, "info", format))
		Startup(logger, "abc123", "info", format, true)
		text := output.String()
		for _, value := range []string{"process.logging_configured", "1.2.3+build", "abc123", "injected", "log_level", "log_format"} {
			if !strings.Contains(text, value) {
				t.Errorf("missing %s: %s", value, text)
			}
		}
		output.Reset()
		Startup((Process{"unsafe\nvalue", "ui", "test", strings.Repeat("x", 129)}).Logger(NewLogger(&output, "info", format)), "", "info", format, false)
		if strings.Contains(output.String(), "unsafe") || strings.Contains(output.String(), "xxx") {
			t.Fatal("unbounded build/deployment value")
		}
		output.Reset()
		Startup(NewLogger(&output, "warn", format), "unknown", "warn", format, false)
		if output.Len() != 0 {
			t.Fatal("startup bypassed threshold")
		}
		cause := errors.New("SYNTHETIC_PRIVATE_CAUSE")
		err := WithFormat(safeerr.At("startup", cause), format, Process{"catalog", "worker", "test", "dev"})
		if !errors.Is(err, cause) || WithFormat(nil, format, Process{}) != nil {
			t.Fatal("error identity changed")
		}
		Report(&output, "catalogd", "INTERNAL", err, true)
		if json.Valid(bytes.TrimSpace(output.Bytes())) != (format == "json") || strings.Contains(output.String(), cause.Error()) || !strings.Contains(output.String(), "process.failed") {
			t.Fatalf("terminal: %s", output.String())
		}
		output.Reset()
		Report(&output, "catalogctl", "INTERNAL", err, false)
		if !strings.HasPrefix(output.String(), "catalogctl: startup: INTERNAL:") {
			t.Fatalf("CLI contract: %s", output.String())
		}
	}
	var output bytes.Buffer
	Report(&output, "catalogd", "INTERNAL", fmt.Errorf("private: %w", safeerr.ErrPanic), true)
	if json.Valid(bytes.TrimSpace(output.Bytes())) || !strings.Contains(output.String(), "INTERNAL_PANIC") {
		t.Fatal("bootstrap fallback")
	}
}
