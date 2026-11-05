// Embedding telemetry tests enforce a fixed vocabulary and histogram counts.
package observability

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestEmbeddingMetricsBoundedAndDisabled(t *testing.T) {
	r := New(time.Now)
	var output bytes.Buffer
	r.writeEmbeddingMetrics(&output)
	if output.Len() != 0 {
		t.Fatal("disabled provider metrics")
	}
	r.ObserveEmbedding("tenant-secret", "success", time.Second)
	r.ObserveEmbedding("query", "private-error", time.Second)
	r.ObserveEmbedding("query", "success", time.Second)
	r.ObserveEmbedding("batch", "rejected", 2*time.Second)
	r.ObserveEmbedding("query", "admission", 3*time.Second)
	r.writeEmbeddingMetrics(&output)
	for _, forbidden := range []string{"tenant-secret", "private-error"} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatal("unbounded label")
		}
	}
	if !strings.Contains(output.String(), `open_agentic_catalog_embedding_request_duration_seconds_count{operation="query",outcome="success"} 1`) || !strings.Contains(output.String(), `open_agentic_catalog_embedding_request_duration_seconds_sum{operation="batch",outcome="rejected"} 2`) {
		t.Fatal(output.String())
	}
	if !strings.Contains(output.String(), `open_agentic_catalog_embedding_request_duration_seconds_count{operation="query",outcome="admission"} 1`) || !strings.Contains(output.String(), `open_agentic_catalog_embedding_request_duration_seconds_sum{operation="query",outcome="admission"} 3`) {
		t.Fatal("local admission was omitted from rendering", output.String())
	}
}

func TestRetrievalMetricsRejectUnboundedLabels(t *testing.T) {
	r := New(time.Now)
	r.ObserveRetrieval("search", "hybrid")
	r.ObserveRetrieval("public", "provider_unavailable")
	r.ObserveRetrieval("private-query", "hybrid")
	r.ObserveRetrieval("search", "private-error")
	var out bytes.Buffer
	r.writeEmbeddingMetrics(&out)
	if strings.Contains(out.String(), "private-") || !strings.Contains(out.String(), `open_agentic_catalog_search_retrieval_total{operation="search",outcome="hybrid"} 1`) || !strings.Contains(out.String(), `operation="public",outcome="provider_unavailable"} 1`) {
		t.Fatal(out.String())
	}
}
