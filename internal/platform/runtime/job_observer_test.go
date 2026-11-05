package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	sdk "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestJobDiagnosticsIndependentOfSamplingAndPrivateData(t *testing.T) {
	cases := []struct {
		outcome   string
		class     jobs.FailureClass
		committed bool
		level     string
	}{
		{"succeeded", "", true, "DEBUG"}, {"cancelled", jobs.FailureCancelled, true, "DEBUG"},
		{"cancelled", jobs.FailureInvalidInput, true, "DEBUG"},
		{"waiting", jobs.FailureDependency, true, "DEBUG"}, {"waiting", jobs.FailureInternal, true, "ERROR"},
		{"failed", jobs.FailurePolicyDenied, true, "WARN"}, {"dead_letter", jobs.FailureTransientUpstream, true, "WARN"},
		{"unsupported", jobs.FailureInvalidInput, true, "WARN"}, {"failed", jobs.FailureInternal, true, "ERROR"},
		{"dependency_error", "", false, "DEBUG"}, {"failed", "", false, "DEBUG"},
		{"private-outcome", "private-error", true, "ERROR"},
	}
	for _, sampling := range []string{"disabled", "sampled", "unsampled"} {
		ctx := t.Context()
		if sampling != "disabled" {
			sampler := sdk.NeverSample()
			if sampling == "sampled" {
				sampler = sdk.AlwaysSample()
			}
			provider := sdk.NewTracerProvider(sdk.WithSampler(sampler))
			t.Cleanup(func() {
				if err := provider.Shutdown(t.Context()); err != nil && !errors.Is(err, t.Context().Err()) {
					t.Error(err)
				}
			})
			local, span := provider.Tracer("fixture").Start(ctx, "job attempt")
			ctx = local
			defer span.End()
		}
		for _, format := range []string{"text", "json"} {
			for _, threshold := range []string{"debug", "info", "warn", "error"} {
				for _, tc := range cases {
					t.Run(sampling+"/"+format+"/"+threshold+"/"+tc.outcome+"/"+string(tc.class), func(t *testing.T) {
						var output bytes.Buffer
						registry := observability.New(nil)
						logger := (diagnostics.Process{Service: "catalog", Role: "worker", Environment: "test", Version: "dev"}).Logger(diagnostics.NewLogger(&output, threshold, format))
						observer := jobObserver{registry, logger}
						observer.JobStarted("private-kind")
						if !strings.Contains(string(registry.Render(ctx)), "open_agentic_catalog_job_executions_active{kind=\"other\"} 1\n") {
							t.Fatal("active attempt not counted")
						}
						observer.JobFinished(ctx, "private-kind", jobs.AttemptResult{Outcome: tc.outcome, FailureClass: tc.class, Committed: tc.committed})
						outcome := tc.outcome
						if outcome == "private-outcome" {
							outcome = "failed"
						}
						metric := `open_agentic_catalog_job_executions_total{kind="other",outcome="` + outcome + "\"} 1\n"
						metrics := string(registry.Render(ctx))
						if !strings.Contains(metrics, metric) || !strings.Contains(metrics, "open_agentic_catalog_job_executions_active{kind=\"other\"} 0\n") || strings.Contains(metrics, "private-") {
							t.Fatalf("filtering changed attempt accounting: %s", metrics)
						}
						visible := tc.level == "ERROR" || tc.level == "WARN" && threshold != "error" || tc.level == "DEBUG" && threshold == "debug"
						if !visible {
							if output.Len() != 0 {
								t.Fatalf("attempt ignored threshold %s: %s", threshold, output.String())
							}
							return
						}
						body := output.String()
						if strings.Count(body, "\n") != 1 || strings.Contains(body, "private-") {
							t.Fatalf("unsafe/duplicate event: %s", body)
						}
						if format == "json" {
							var got map[string]any
							if err := json.Unmarshal(output.Bytes(), &got); err != nil {
								t.Fatal(err)
							}
							event := "job.attempt_completed"
							if !tc.committed {
								event = "job.attempt_interrupted"
							}
							if got["level"] != tc.level || got["event"] != event || got["committed"] != tc.committed || got["kind"] != "other" || got["component"] != "job_worker" || got["role"] != "worker" {
								t.Fatalf("event: %s", body)
							}
							if sampling == "disabled" {
								if got["trace_id"] != nil || got["span_id"] != nil || got["trace_sampled"] != nil {
									t.Fatal("invented trace")
								}
							} else {
								active := trace.SpanContextFromContext(ctx)
								if got["trace_id"] != active.TraceID().String() || got["span_id"] != active.SpanID().String() || got["trace_sampled"] != (sampling == "sampled") {
									t.Fatal("active correlation lost")
								}
							}
						} else if !strings.Contains(body, "level="+tc.level) || !strings.Contains(body, fmt.Sprintf("committed=%t", tc.committed)) {
							t.Fatalf("event: %s", body)
						}
					})
				}
			}
		}
	}
}
