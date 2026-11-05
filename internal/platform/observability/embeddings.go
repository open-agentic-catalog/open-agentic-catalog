// Embedding metrics expose bounded query/batch outcomes without model identities or input text.
package observability

import (
	"bytes"
	"fmt"
	"time"
)

// ObserveRetrieval counts effective modes/fallback, never arbitrary error text.
func (r *Registry) ObserveRetrieval(operation, outcome string) {
	switch operation {
	case "search", "explore", "public":
	default:
		return
	}
	switch outcome {
	case "core", "hybrid", "indexing", "provider_unavailable", "index_changed", "work_limit", "error":
	default:
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retrieval == nil {
		r.retrieval = make(map[embeddingKey]uint64)
	}
	r.retrieval[embeddingKey{operation, outcome}]++
}

type embeddingKey struct{ operation, outcome string }

// ObserveEmbedding records an operation, including rejection and local admission time.
func (r *Registry) ObserveEmbedding(operation, outcome string, elapsed time.Duration) {
	if operation != "query" && operation != "batch" {
		return
	}
	switch outcome {
	case "success", "rejected", "admission", "cancelled", "authentication", "invalid_response", "unavailable":
	default:
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.embeddings == nil {
		r.embeddings = make(map[embeddingKey]durationMetric)
	}
	key := embeddingKey{operation, outcome}
	r.embeddings[key] = observeDuration(r.embeddings[key], elapsed)
}

// writeEmbeddingMetrics uses a fixed iteration order and no disabled provider targets.
func (r *Registry) writeEmbeddingMetrics(output *bytes.Buffer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.retrieval) > 0 {
		const name = "open_agentic_catalog_search_retrieval_total"
		writeMetricHeader(output, name, "Optional retrieval outcomes, including classified lexical fallback.", "counter")
		for _, operation := range []string{"search", "explore", "public"} {
			for _, outcome := range []string{"core", "hybrid", "indexing", "provider_unavailable", "index_changed", "work_limit", "error"} {
				if count, ok := r.retrieval[embeddingKey{operation, outcome}]; ok {
					fmt.Fprintf(output, "%s{operation=%q,outcome=%q} %d\n", name, operation, outcome, count)
				}
			}
		}
	}
	if len(r.embeddings) == 0 {
		return
	}
	const name = "open_agentic_catalog_embedding_request_duration_seconds"
	writeMetricHeader(output, name, "Embedding operation duration including local rejection and concurrency admission, by fixed outcome; not provider-only inference latency.", "histogram")
	for _, operation := range []string{"query", "batch"} {
		for _, outcome := range []string{"success", "rejected", "admission", "cancelled", "authentication", "invalid_response", "unavailable"} {
			if metric, ok := r.embeddings[embeddingKey{operation, outcome}]; ok {
				writeDurationHistogram(output, name, `operation="`+operation+`",outcome="`+outcome+`"`, metric)
			}
		}
	}
}
