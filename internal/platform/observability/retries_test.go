package observability

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRetryMetricsBoundDurationsAndLabels(t *testing.T) {
	r := New(func() time.Time { return time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC) })
	r.runtimeRead = func() runtimeReading { return runtimeReading{} }
	if strings.Contains(string(r.Render(t.Context())), "retry_delay_seconds") {
		t.Fatal("unobserved retry metrics must remain absent")
	}
	r.JobRetryScheduled("private-tenant.example", -time.Nanosecond)
	r.WebhookRetryScheduled(time.Hour + time.Nanosecond)
	if strings.Contains(string(r.Render(t.Context())), "retry_delay_seconds") {
		t.Fatal("rejected observations must not create zero-valued metric families")
	}
	for _, delay := range []time.Duration{0, 5500 * time.Millisecond, 24 * time.Hour} {
		r.JobRetryScheduled("source.sync", delay)
	}
	initial := string(r.Render(t.Context()))
	for _, kind := range []string{"publication_generate", "semantic_reindex", "other"} {
		for _, statistic := range []string{"count", "sum"} {
			want := fmt.Sprintf("open_agentic_catalog_job_retry_delay_seconds_%s{kind=%q} 0\n", statistic, kind)
			if !strings.Contains(initial, want) {
				t.Errorf("unobserved Job kind missing %s", want)
			}
		}
	}
	if strings.Contains(initial, "open_agentic_catalog_webhook_retry_delay_seconds") {
		t.Fatal("Job observations must not create webhook metric samples")
	}
	r.JobRetryScheduled("publication.generate", time.Second)
	r.JobRetryScheduled("semantic.reindex", time.Minute)
	r.JobRetryScheduled("private-tenant.example", time.Second)
	for _, invalid := range []time.Duration{-time.Nanosecond, 24*time.Hour + time.Nanosecond} {
		r.JobRetryScheduled("source.sync", invalid)
	}
	for _, delay := range []time.Duration{5500 * time.Millisecond, time.Hour, -time.Nanosecond, time.Hour + time.Nanosecond} {
		r.WebhookRetryScheduled(delay)
	}
	got := string(r.Render(t.Context()))
	for _, want := range []string{
		`# TYPE open_agentic_catalog_job_retry_delay_seconds histogram`,
		`open_agentic_catalog_job_retry_delay_seconds_bucket{kind="source_sync",le="0"} 1`,
		`open_agentic_catalog_job_retry_delay_seconds_bucket{kind="source_sync",le="5"} 1`,
		`open_agentic_catalog_job_retry_delay_seconds_bucket{kind="source_sync",le="15"} 2`,
		`open_agentic_catalog_job_retry_delay_seconds_bucket{kind="source_sync",le="86400"} 3`,
		`open_agentic_catalog_job_retry_delay_seconds_bucket{kind="source_sync",le="+Inf"} 3`,
		`open_agentic_catalog_job_retry_delay_seconds_sum{kind="source_sync"} 86405.5`,
		`open_agentic_catalog_job_retry_delay_seconds_count{kind="source_sync"} 3`,
		`open_agentic_catalog_job_retry_delay_seconds_count{kind="publication_generate"} 1`,
		`open_agentic_catalog_job_retry_delay_seconds_count{kind="semantic_reindex"} 1`,
		`open_agentic_catalog_job_retry_delay_seconds_count{kind="other"} 1`,
		`# TYPE open_agentic_catalog_webhook_retry_delay_seconds histogram`,
		`open_agentic_catalog_webhook_retry_delay_seconds_bucket{le="5"} 0`,
		`open_agentic_catalog_webhook_retry_delay_seconds_bucket{le="15"} 1`,
		`open_agentic_catalog_webhook_retry_delay_seconds_bucket{le="3600"} 2`,
		`open_agentic_catalog_webhook_retry_delay_seconds_bucket{le="+Inf"} 2`,
		`open_agentic_catalog_webhook_retry_delay_seconds_sum 3605.5`,
		`open_agentic_catalog_webhook_retry_delay_seconds_count 2`,
	} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(got, "private-tenant") || len(got) > MaxMetricsBytes || got != string(r.Render(t.Context())) {
		t.Fatal("retry metrics must remain bounded, redacted and deterministic")
	}
}

func TestRetryMetricsConcurrentUpdatesAndScrapes(t *testing.T) {
	r := New(nil)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 20 {
				r.JobRetryScheduled("source.sync", time.Second)
				r.WebhookRetryScheduled(2 * time.Second)
				_ = r.Render(t.Context())
			}
		})
	}
	group.Wait()
	got := string(r.Render(t.Context()))
	for metric, want := range map[string]int{
		`job_retry_delay_seconds_count{kind="source_sync"}`: 160,
		`job_retry_delay_seconds_sum{kind="source_sync"}`:   160,
		`webhook_retry_delay_seconds_count`:                 160,
		`webhook_retry_delay_seconds_sum`:                   320,
	} {
		if !strings.Contains(got, fmt.Sprintf("open_agentic_catalog_%s %d\n", metric, want)) {
			t.Errorf("incorrect concurrent metric %s", metric)
		}
	}
}
