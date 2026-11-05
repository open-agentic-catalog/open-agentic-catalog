package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProcessFieldsUTCAndParentOwnership(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		var output bytes.Buffer
		parent := NewLogger(&output, "debug", format)
		child := (Process{"catalog", "worker", "test", "dev"}).Logger(parent)
		instant := time.Date(2026, 9, 26, 13, 14, 15, 0, time.FixedZone("offset", 3600))
		record := slog.NewRecord(instant, slog.LevelInfo, "fixed", 0)
		record.Add("event", "fixture", "component", "job_worker", "elapsed_ms", int64(25))
		if err := child.Handler().Handle(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		if format == "json" {
			var fields map[string]any
			if err := json.Unmarshal(output.Bytes(), &fields); err != nil {
				t.Fatal(err)
			}
			if len(fields) != 10 || fields["time"] != "2026-09-26T12:14:15Z" || fields["service"] != "catalog" || fields["role"] != "worker" || fields["environment"] != "test" || fields["version"] != "dev" || fields["elapsed_ms"] != float64(25) {
				t.Fatalf("record: %s", output.String())
			}
		} else {
			for _, field := range []string{"time=2026-09-26T12:14:15.000Z", "service=catalog", "role=worker", "environment=test", "version=dev", "elapsed_ms=25"} {
				if !strings.Contains(output.String(), field) {
					t.Fatalf("missing %s: %s", field, output.String())
				}
			}
		}
		output.Reset()
		parent.Info("parent")
		if strings.Contains(output.String(), "service") {
			t.Fatal("parent mutated")
		}
	}
}

func TestWarningWindowsAreBoundedConcurrentAndIndependent(t *testing.T) {
	var output bytes.Buffer
	logger := NewLogger(&output, "warn", "json")
	var warning, independent Warning
	now := time.Unix(10, 0)
	warning.Warn(now, logger, "fixed", "event", "fixture")
	var work sync.WaitGroup
	for range 32 {
		work.Go(func() { warning.Warn(now.Add(time.Second), logger, "fixed", "event", "fixture") })
	}
	work.Wait()
	warning.Warn(now.Add(time.Minute-time.Nanosecond), logger, "fixed", "event", "fixture")
	independent.Warn(now, logger, "independent", "event", "independent")
	warning.Warn(now.Add(time.Minute), logger, "fixed", "event", "fixture")
	warning.Warn(now.Add(2*time.Minute), logger, "fixed", "event", "fixture")
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("warnings: %s", output.String())
	}
	for i, want := range []float64{0, 0, 33, 0} {
		var got map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &got); err != nil {
			t.Fatal(err)
		}
		if got["suppressed"] != want || got["level"] != "WARN" {
			t.Fatalf("warning %d: %v", i, got)
		}
	}
	output.Reset()
	warning.Warn(now.Add(3*time.Minute), NewLogger(&output, "error", "json"), "filtered")
	if output.Len() != 0 {
		t.Fatal("warning bypassed filtering")
	}
}
