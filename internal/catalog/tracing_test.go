package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPrivateTraceContext(t *testing.T) {
	valid := TraceContext("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	if !valid.Valid() {
		t.Fatal("canonical context rejected")
	}
	for _, value := range []string{"", string(valid) + "-extra", strings.ToUpper(string(valid)), strings.Replace(string(valid), "4bf92f3577b34da6a3ce929d0e0e4736", strings.Repeat("0", 32), 1), strings.Replace(string(valid), "00f067aa0ba902b7", strings.Repeat("0", 16), 1), strings.Replace(string(valid), "4bf9", "zzzz", 1), string(valid[:53]) + "02"} {
		if TraceContext(value).Valid() {
			t.Errorf("invalid context accepted: %q", value)
		}
	}
}

type traceStarterFunc func(context.Context, string, TraceKind, TraceContext) (context.Context, func(string))

func (f traceStarterFunc) Start(ctx context.Context, name string, kind TraceKind, cause TraceContext) (context.Context, func(string)) {
	return f(ctx, name, kind, cause)
}

func TestStartTraceRequiresAnActiveCauseForInternalAndClientWork(t *testing.T) {
	valid := TraceContext("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	for _, kind := range []struct {
		name string
		kind TraceKind
	}{
		{"internal", TraceInternal}, {"client", TraceClient}, {"server", TraceServer},
		{"attempt", TraceAttempt}, {"producer", TraceProducer},
	} {
		for _, active := range []bool{false, true} {
			name := kind.name + "/without active cause"
			if active {
				name = kind.name + "/with active cause"
			}
			t.Run(name, func(t *testing.T) {
				calls, completions := 0, 0
				var input context.Context
				tracer := traceStarterFunc(func(ctx context.Context, spanName string, gotKind TraceKind, cause TraceContext) (context.Context, func(string)) {
					calls++
					if ctx != input || spanName != "fixed operation" || gotKind != kind.kind || cause != valid {
						t.Fatal("tracing port changed the context or span arguments")
					}
					return WithTraceContext(ctx, valid), func(outcome string) {
						completions++
						if outcome != "partial" {
							t.Fatalf("completion outcome = %q", outcome)
						}
					}
				})
				input = WithTracer(t.Context(), tracer)
				if active {
					input = WithTraceContext(input, valid)
				}
				got, end := StartTrace(input, "fixed operation", kind.kind, valid)
				end("partial")
				want := 1
				if !active && (kind.kind == TraceInternal || kind.kind == TraceClient) {
					want = 0
					if got != input {
						t.Fatal("suppressed child span changed context")
					}
				} else if CausalTrace(got) != valid {
					t.Fatal("tracing port's returned context was lost")
				}
				if calls != want || completions != want {
					t.Fatalf("starts/completions = %d/%d, want %d", calls, completions, want)
				}
			})
		}
	}
}

func TestWithoutTracingSuppressesAttachedTracerAndPreservesContext(t *testing.T) {
	type contextKey struct{}
	base, cancel := context.WithTimeout(context.WithValue(t.Context(), contextKey{}, "execution attribution"), time.Hour)
	defer cancel()
	if WithTracer(base, nil) != base {
		t.Fatal("nil tracer changed context")
	}
	tracer := traceStarterFunc(func(context.Context, string, TraceKind, TraceContext) (context.Context, func(string)) {
		t.Fatal("suppressed tracing called the provider")
		return nil, nil
	})
	cause := TraceContext("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	original := WithTraceContext(WithTracer(base, tracer), cause)
	suppressed := WithoutTracing(original)
	wantDeadline, _ := base.Deadline()
	deadline, ok := suppressed.Deadline()
	if !ok || !deadline.Equal(wantDeadline) || suppressed.Value(contextKey{}) != "execution attribution" || CausalTrace(suppressed) != "" || CausalTrace(original) != cause {
		t.Fatal("suppression lost caller context or changed the original cause")
	}
	for _, kind := range []TraceKind{TraceInternal, TraceClient, TraceServer, TraceAttempt, TraceProducer} {
		got, end := StartTrace(suppressed, "maintenance", kind, cause)
		end("ok")
		if got != suppressed {
			t.Fatal("suppression changed context on span start")
		}
	}
	cancel()
	if !errors.Is(suppressed.Err(), context.Canceled) || !errors.Is(context.Cause(suppressed), context.Canceled) {
		t.Fatal("suppression lost caller cancellation")
	}
}

func FuzzTraceContext(f *testing.F) {
	f.Add("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	f.Add("")
	f.Fuzz(func(t *testing.T, value string) {
		ctx := WithTraceContext(t.Context(), TraceContext(value))
		if got := CausalTrace(ctx); got != "" && !got.Valid() {
			t.Fatal("retained invalid context")
		}
	})
}
