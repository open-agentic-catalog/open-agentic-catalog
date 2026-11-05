package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// Exercise the host's configuration/recovery defers without opening a listener.
func TestRunHostPanicKeepsConfiguredTerminalFormat(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			file, cfg := commandConfig(t)
			cfg.LogFormat = format
			raw, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, raw, 0600); err != nil {
				t.Fatal(err)
			}
			called := false
			err = runHost(file, func(*http.Server, func()) error {
				called = true
				panic("SYNTHETIC_PRIVATE_UI_PANIC")
			})
			if !called || !errors.Is(err, safeerr.ErrPanic) {
				t.Fatalf("host panic was not fatal: called=%t err=%v", called, err)
			}
			var output bytes.Buffer
			diagnostics.Report(&output, "catalog-ui", "INTERNAL", err, true)
			if json.Valid(bytes.TrimSpace(output.Bytes())) != (format == "json") || strings.Count(output.String(), "\n") != 1 || !strings.Contains(output.String(), "INTERNAL_PANIC") || strings.Contains(output.String(), "SYNTHETIC_PRIVATE_UI_PANIC") {
				t.Fatalf("host panic format/privacy: %s", output.String())
			}
		})
	}
}

func TestUIPanicDrainsAndRemainsFatalDuringCancellation(t *testing.T) {
	for _, simultaneousCancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "listener panic", true: "panic during cancellation"}[simultaneousCancel], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var events []string
				listener := listenerFixture{
					listen: func() error {
						if simultaneousCancel {
							cancel()
						}
						panic("private-ui-listener-panic")
					},
					shutdown: func(shutdown context.Context) error {
						events = append(events, "shutdown")
						if _, ok := shutdown.Deadline(); !ok || shutdown.Err() != nil {
							t.Error("shutdown unbounded or canceled")
						}
						return nil
					},
					close: func() error { events = append(events, "close"); return nil },
				}
				err := serve(ctx, listener, func() { events = append(events, "drain") })
				if !errors.Is(err, safeerr.ErrPanic) || len(events) < 2 || !reflect.DeepEqual(events[:2], []string{"drain", "shutdown"}) {
					t.Fatalf("UI panic cleanup: %v %v", err, events)
				}
			})
		})
	}
}
