package observability

import (
	"bytes"
	"fmt"
	"strconv"
	"time"
)

// Retry buckets cover scheduled delays, not request latency. The final finite
// bucket includes the maximum Job minimum-plus-jitter policy of 24 hours.
var retryBuckets = [...]float64{0, 1, 5, 15, 30, 60, 300, 900, 3600, 10800, 21600, 86400}

type retryMetric struct {
	count   uint64
	sum     float64
	buckets [len(retryBuckets)]uint64
}

// JobRetryScheduled records a successfully persisted waiting transition. Kind
// is mapped to the existing fixed vocabulary; no Job data is retained.
func (r *Registry) JobRetryScheduled(kind string, delay time.Duration) {
	if delay < 0 || delay > 24*time.Hour {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.jobRetries == nil {
		r.jobRetries = make(map[string]retryMetric)
	}
	kind = JobKindLabel(kind)
	r.jobRetries[kind] = observeRetry(r.jobRetries[kind], delay)
}

// WebhookRetryScheduled records a persisted waiting delivery, independently of
// sender outcomes and later outbox deferral. API test sends do not schedule retries.
func (r *Registry) WebhookRetryScheduled(delay time.Duration) {
	if delay < 0 || delay > time.Hour {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.webhookRetries = observeRetry(r.webhookRetries, delay)
}

func observeRetry(metric retryMetric, delay time.Duration) retryMetric {
	seconds := delay.Seconds()
	metric.count++
	metric.sum += seconds
	for i, bound := range retryBuckets {
		if seconds <= bound {
			metric.buckets[i]++
		}
	}
	return metric
}

func (r *Registry) writeRetryMetrics(output *bytes.Buffer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.jobRetries) > 0 {
		const name = "open_agentic_catalog_job_retry_delay_seconds"
		writeMetricHeader(output, name, "Process-local persisted Job retry delays from attempt completion, including upstream minima and jitter; not actual queue wait.", "histogram")
		for _, kind := range []string{"source_sync", "publication_generate", "semantic_reindex", "other"} {
			writeRetryHistogram(output, name, "kind="+quote(kind), r.jobRetries[kind])
		}
	}
	if r.webhookRetries.count > 0 {
		const name = "open_agentic_catalog_webhook_retry_delay_seconds"
		writeMetricHeader(output, name, "Process-local persisted webhook retry delays from attempt completion, including receiver hints and jitter; not actual queue wait.", "histogram")
		writeRetryHistogram(output, name, "", r.webhookRetries)
	}
}

func writeRetryHistogram(output *bytes.Buffer, name, labels string, metric retryMetric) {
	bucketLabels, suffix := "", ""
	if labels != "" {
		bucketLabels = labels + ","
		suffix = "{" + labels + "}"
	}
	for i, bound := range retryBuckets {
		fmt.Fprintf(output, "%s_bucket{%sle=%q} %d\n", name, bucketLabels, strconv.FormatFloat(bound, 'g', -1, 64), metric.buckets[i])
	}
	fmt.Fprintf(output, "%s_bucket{%sle=\"+Inf\"} %d\n", name, bucketLabels, metric.count)
	fmt.Fprintf(output, "%s_sum%s %s\n", name, suffix, strconv.FormatFloat(metric.sum, 'g', -1, 64))
	fmt.Fprintf(output, "%s_count%s %d\n", name, suffix, metric.count)
}
