//go:build minikube

// Instrumentation checks exercise fixed application metrics through owned port-forwards.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

const searchCounter = `open_agentic_catalog_http_responses_total{method="POST",route="/api/v1/catalog/search",status_class="2xx"}`

// This is a bounded instrumentation smoke through an owned port-forward, not a
// Kubernetes capacity benchmark or qualification of an in-cluster collector.
func (h *harness) verifyInstrumentation(phase string) error {
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil, MaxConnsPerHost: 1, MaxIdleConns: 4, MaxIdleConnsPerHost: 1, MaxResponseHeaderBytes: 64 << 10, ResponseHeaderTimeout: 3 * time.Second, IdleConnTimeout: 15 * time.Second}}
	defer client.CloseIdleConnections()
	for _, role := range []string{"api", "worker"} {
		pods, err := h.pods(role)
		if err != nil {
			return err
		}
		if len(pods) < 1 || len(pods) > 2 {
			return errors.New("instrumentation requires the bounded owned replica set")
		}
		for _, pod := range pods {
			if !pod.Ready || pod.ImageID != h.report.Images["application"] {
				return errors.New("instrumentation pod is unready or not the owned image")
			}
			port := 8080
			if role == "worker" {
				port = 9090
			}
			address, err := h.startPortForward("pod/"+pod.Name, port)
			if err != nil {
				return err
			}
			base := "http://" + address
			var baseline httpResult
			if role == "api" {
				baseline, err = h.request(http.MethodPost, base+"/api/v1/catalog/search", h.tokens["reader"], "", "", map[string]int{"limit": 20}, client)
				if err != nil || !validInstrumentationSearch(baseline) {
					return errors.New("instrumentation search baseline failed")
				}
			}
			before, err := h.request(http.MethodGet, base+"/metrics", "", "", "", nil, client)
			if err != nil {
				return errors.New("instrumentation metrics request failed")
			}
			if err := validateInstrumentation(before, role == "worker"); err != nil {
				return fmt.Errorf("%s instrumentation: %w", role, err)
			}
			if role == "api" {
				deadline := time.Now().Add(30 * time.Second)
				for range 25 {
					if time.Now().After(deadline) {
						return errors.New("instrumentation search batch exceeded its deadline")
					}
					result, err := h.request(http.MethodPost, base+"/api/v1/catalog/search", h.tokens["reader"], "", "", map[string]int{"limit": 20}, client)
					if err != nil || result.Status != http.StatusOK || !bytes.Equal(result.Body, baseline.Body) {
						return errors.New("instrumentation search batch changed or failed")
					}
				}
				after, err := h.request(http.MethodGet, base+"/metrics", "", "", "", nil, client)
				if err != nil || validateInstrumentation(after, false) != nil {
					return errors.New("post-workload instrumentation failed")
				}
				initial, initialErr := fixedMetric(before.Body, searchCounter)
				final, finalErr := fixedMetric(after.Body, searchCounter)
				if initialErr != nil || finalErr != nil || final-initial < 25 {
					return errors.New("search workload did not advance its route counter")
				}
			}
		}
	}
	h.report.Checks = append(h.report.Checks, phase+"-api-worker-runtime-metrics-and-25-searches-per-api")
	return nil
}

// validInstrumentationSearch accepts a complete, duplicate-free local search projection for the governed seed, with no live or cross-tenant results.
func validInstrumentationSearch(result httpResult) bool {
	mediaType, _, err := mime.ParseMediaType(result.Header.Get("Content-Type"))
	if result.Status != http.StatusOK || err != nil || mediaType != "application/json" || len(result.Body) > 1<<20 || jsondoc.Validate(result.Body) != nil {
		return false
	}
	var page catalog.CatalogSearchResponse
	if json.Unmarshal(result.Body, &page) != nil || page.Partial || len(page.Items) < 3 || len(page.Items) != page.DurableTotal || page.NextCursor != "" || page.LiveCount != 0 || page.Generation == "" {
		return false
	}
	seen := make(map[string]bool)
	for _, item := range page.Items {
		if item.Local == nil || item.Local.Resource.TenantID != primaryTenantID || item.ID == "" || seen[item.ID] {
			return false
		}
		seen[item.ID] = true
	}
	return true
}

// validateInstrumentation checks the bounded Prometheus response for usable process counters and, for workers, operational snapshot evidence.
func validateInstrumentation(result httpResult, worker bool) error {
	mediaType, _, err := mime.ParseMediaType(result.Header.Get("Content-Type"))
	if result.Status != http.StatusOK || err != nil || mediaType != "text/plain" || len(result.Body) > 1<<20 {
		return errors.New("invalid metrics response")
	}
	for _, family := range []string{"heap_bytes", "heap_objects", "last_gc_live_heap_bytes", "allocated_bytes_total", "goroutines", "gc_cycles_total", "gc_pause_seconds"} {
		name := "open_agentic_catalog_go_" + family
		available, err := fixedMetric(result.Body, `open_agentic_catalog_go_metric_available{metric="`+name+`"}`)
		if err != nil || available != 1 {
			return errors.New("runtime metric unavailable")
		}
		if family == "gc_pause_seconds" {
			name += "_count"
		}
		value, err := fixedMetric(result.Body, name)
		if err != nil || (family == "heap_bytes" || family == "goroutines") && value == 0 {
			return errors.New("runtime metric missing or invalid")
		}
	}
	if worker {
		for _, name := range []string{"open_agentic_catalog_operational_snapshot_available", "open_agentic_catalog_job_oldest_runnable_age_seconds", "open_agentic_catalog_outbox_oldest_pending_age_seconds"} {
			value, err := fixedMetric(result.Body, name)
			if err != nil || name == "open_agentic_catalog_operational_snapshot_available" && value != 1 {
				return errors.New("worker snapshot missing or unavailable")
			}
		}
	}
	return nil
}

// Only fixed reviewed keys are requested; never retain the raw metrics body.
func fixedMetric(body []byte, key string) (float64, error) {
	var value float64
	found := false
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != key {
			continue
		}
		if found || len(fields) != 2 {
			return 0, errors.New("duplicate or malformed metric")
		}
		var err error
		value, err = strconv.ParseFloat(fields[1], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return 0, errors.New("non-finite or negative metric")
		}
		found = true
	}
	if !found {
		return 0, errors.New("missing required metric")
	}
	return value, nil
}
