// Command performance exercises only the invocation-owned loopback Catalog.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

type profile struct {
	Public          *publicProfile `json:"public,omitempty"`
	Scale           *scaleProfile  `json:"scale,omitempty"`
	HybridItems     int            `json:"hybridItems,omitempty"`
	MixedScaleItems int            `json:"mixedScaleItems,omitempty"`
	Scenario        string         `json:"scenario,omitempty"`
	Query           string         `json:"query,omitempty"`
	Port            int            `json:"port"`
	WarmupSeconds   int            `json:"warmupSeconds"`
	Seconds         int            `json:"seconds"`
	Workers         int            `json:"workers"`
	MaxRequests     int            `json:"maxRequests"`
}

type result struct {
	SuccessfulResponseBytes uint64         `json:"successfulResponseBytes"`
	Count                   int            `json:"count"`
	Succeeded               int            `json:"succeeded"`
	Failed                  int            `json:"failed"`
	Seconds                 float64        `json:"elapsedSeconds"`
	LatencySecondsTotal     float64        `json:"latencySecondsTotal"`
	Buckets                 [12]uint64     `json:"latencyBuckets"`
	Overflow                uint64         `json:"latencyOverflow"`
	Outcomes                outcomes       `json:"outcomes"`
	HTTPFailures            map[string]int `json:"httpFailureStatuses"`
	StartedAt               int64          `json:"startedAtUnixMillis"`
	IssuanceEndedAt         int64          `json:"issuanceEndedAtUnixMillis"`
	FinishedAt              int64          `json:"finishedAtUnixMillis"`
	Complete                bool           `json:"complete"`
	ClockAdjustmentMS       int64          `json:"clockAdjustmentMillis"`
}

type outcomes struct {
	Timeout     int `json:"timeout"`
	Transport   int `json:"transport"`
	Status      int `json:"status"`
	Body        int `json:"body"`
	Correctness int `json:"correctness"`
	Canceled    int `json:"canceled"`
}

type requestError string

// Error returns the fixed failure class, never a transport's original message.
func (f requestError) Error() string { return string(f) }

type httpStatusError string

// Error keeps status failures in the public fixed diagnostic vocabulary.
func (f httpStatusError) Error() string { return "status" }

// Unwrap makes status failures available to the common outcome classifier.
func (f httpStatusError) Unwrap() error { return requestError("status") }

// classifyHTTPFailure retains an HTTP status or an exact known cursor
// classification, never arbitrary problem detail or request identifiers.
func classifyHTTPFailure(status int, body []byte) httpStatusError {
	key := strconv.Itoa(status)
	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if status == http.StatusBadRequest && jsondoc.Validate(body) == nil && json.Unmarshal(body, &problem) == nil && problem.Code == "INVALID_ARGUMENT" {
		switch problem.Detail {
		case "catalog cursor is stale":
			key = "400-stale-generation"
		case "live search cursor is invalid or expired; restart the search":
			key = "400-invalid-live-cursor"
		}
	}
	return httpStatusError(key) // Never retain untrusted problem text or request IDs.
}

// classifyRequest separates cancellation and deadlines from other transport errors.
func classifyRequest(err error) requestError {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout() {
		return "timeout"
	}
	return "transport"
}

type report struct {
	SchemaVersion   int             `json:"schemaVersion"`
	Model           string          `json:"model"`
	Profile         profile         `json:"profile"`
	BaselineResults int             `json:"baselineResults"`
	ResponseDigest  string          `json:"responseDigest"`
	BucketBounds    [12]float64     `json:"bucketUpperSeconds"`
	Warmup          result          `json:"warmup"`
	Measured        result          `json:"measured"`
	Verdict         string          `json:"verdict"`
	Generator       generatorSample `json:"generator"`
}

type generatorSample struct {
	Processors     int    `json:"gomaxprocs"`
	HeapBytes      uint64 `json:"heapBytesAtFinish"`
	AllocatedBytes uint64 `json:"allocatedBytes"`
	GCCycles       uint32 `json:"gcCycles"`
	Goroutines     int    `json:"goroutinesAtFinish"`
}

var bounds = [12]float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2, 3, 5}

// validate limits scenario selection, request count, duration and concurrency;
// profiles cannot select arbitrary destination hosts, routes or credentials.
func (p profile) validate() error {
	if p.Public != nil {
		if p.Scenario != "public" || p.Public.validate() != nil {
			return errors.New("invalid-public-profile")
		}
	}
	if p.MixedScaleItems != 0 && (p.Scenario != "mixed" || p.MixedScaleItems != 1000 && p.MixedScaleItems != 10000 && p.MixedScaleItems != 100000) {
		return errors.New("invalid-mixed-scale-size")
	}
	if p.HybridItems != 0 && (p.Scenario != "hybrid" || p.HybridItems != 1000) {
		return errors.New("invalid-hybrid-size")
	}
	if p.Scale != nil {
		if p.Scenario != "scale" || p.Query != "" {
			return errors.New("invalid-scale-profile")
		}
		if err := p.Scale.validate(); err != nil {
			return err
		}
	}
	if p.Scenario != "" && p.Scenario != "local" && p.Scenario != "indexed" && p.Scenario != "public" && p.Scenario != "mixed" && p.Scenario != "hybrid" && !(p.Scenario == "scale" && p.Scale != nil) {
		return errors.New("invalid-scenario")
	}
	if len(p.Query) > 80 || strings.ContainsAny(p.Query, "\r\n\x00") || p.Query != "" && p.Scenario != "indexed" {
		return errors.New("invalid-query")
	}
	if p.Port < 1 || p.Port > 65535 || p.WarmupSeconds < 0 || p.WarmupSeconds > 60 || p.Seconds < 1 || p.Seconds > 3600 || p.Workers < 1 || p.Workers > 8 || p.MaxRequests < p.Workers || p.MaxRequests > 1000000 {
		return errors.New("invalid-profile")
	}
	return nil
}

// newClient bounds transport resources and rejects redirects and environment proxies.
// workloadRequest alone chooses the invocation-owned loopback destination.
func newClient() *http.Client {
	return &http.Client{
		Timeout:       3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect-refused") },
		Transport:     &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, ResponseHeaderTimeout: 2 * time.Second, MaxResponseHeaderBytes: 64 << 10, MaxConnsPerHost: 8, MaxIdleConns: 8, MaxIdleConnsPerHost: 8, IdleConnTimeout: 30 * time.Second, DisableCompression: true},
	}
}

// workloadRequest issues one reviewed scenario request and bounds its response.
// It reports transport/body/status failures separately from content correctness.
func workloadRequest(ctx context.Context, client *http.Client, p profile) ([]byte, error) {
	method, path, payload := http.MethodPost, "/api/v1/catalog/search", `{"limit":20}`
	switch p.Scenario {
	case "hybrid":
		payload = `{"limit":20,"text":"order"}`
		if p.HybridItems == 1000 {
			payload = `{"limit":20,"text":"Scale","tags":["scale"]}`
		}
	case "scale":
		selectedPath, raw, err := p.Scale.request()
		if err != nil {
			return nil, err
		}
		path, payload = selectedPath, string(raw)
	case "public":
		method, path, payload = http.MethodGet, "/.well-known/ard.json", ""
	case "local":
		payload = `{"limit":20,"visibility":"public"}`
	case "indexed":
		raw, err := json.Marshal(map[string]any{"limit": 20, "text": p.Query})
		if err != nil {
			return nil, errors.New("request-construction")
		}
		payload = string(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:"+strconv.Itoa(p.Port)+path, strings.NewReader(payload))
	if err != nil {
		return nil, errors.New("request-construction")
	}
	request.Header.Set("Content-Type", "application/json")
	if p.Public != nil && p.Public.Conditional {
		request.Header.Set("If-None-Match", p.Public.ETag)
	}
	if p.Scenario != "public" {
		request.Header.Set("Authorization", "Bearer local-reader") // Owned development fixture only.
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, classifyRequest(err)
	}
	defer func() { _ = response.Body.Close() }()
	limit := int64(1 << 20)
	if p.Public != nil {
		limit = int64(p.Public.Bytes)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		if failure := classifyRequest(err); failure != "transport" {
			return nil, failure
		}
		return nil, requestError("body")
	}
	if int64(len(body)) > limit {
		return nil, requestError("body")
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if p.Public != nil {
		expected := http.StatusOK
		if p.Public.Conditional {
			expected = http.StatusNotModified
		}
		if response.StatusCode != expected {
			return nil, classifyHTTPFailure(response.StatusCode, body)
		}
		if response.Header.Get("ETag") != p.Public.ETag {
			return nil, requestError("correctness")
		}
	}
	if p.Public != nil && p.Public.Conditional {
		if response.StatusCode != http.StatusNotModified {
			return nil, classifyHTTPFailure(response.StatusCode, body)
		}
		if len(body) != 0 {
			return nil, requestError("body")
		}
		return body, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, classifyHTTPFailure(response.StatusCode, body)
	}
	if mediaErr != nil || mediaType != "application/json" && !(p.Scenario == "public" && mediaType == "application/ard+json") {
		return nil, requestError("body")
	}
	return body, nil
}

// validateBaseline requires a complete, unique local-tenant search result before
// stable response bytes can serve as the timed phase's correctness baseline.
func validateBaseline(body []byte) (catalog.CatalogSearchResponse, error) {
	var page catalog.CatalogSearchResponse
	if jsondoc.Validate(body) != nil {
		return page, errors.New("invalid-search-json")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&page); err != nil {
		return page, errors.New("invalid-search-contract")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return page, errors.New("trailing-search-data")
	}
	if page.Partial || page.LiveCount != 0 || page.DurableTotal < 3 || len(page.Items) != page.DurableTotal || page.NextCursor != "" || page.Generation == "" || page.Profile != catalog.CoreSearchProfile || page.Tier != "core" {
		return page, errors.New("incomplete-seed")
	}
	seen := make(map[string]bool)
	for _, item := range page.Items {
		if item.Type != catalog.SearchResultLocal || item.Local == nil || item.Indexed != nil || item.Live != nil || item.Local.Resource.TenantID != "00000000-0000-7000-8000-000000000001" || item.ID == "" || seen[item.ID] {
			return page, errors.New("invalid-or-cross-tenant-result")
		}
		seen[item.ID] = true
	}
	return page, nil
}

// phase runs a closed-loop worker population and drains every issued sample.
// Cancellation or an early request-cap stop makes the phase incomplete. Fixed
// latency buckets include failed requests; monotonic duration drives the timeline.
func phase(ctx context.Context, client *http.Client, p profile, seconds int, digest [32]byte) result {
	summary := result{HTTPFailures: map[string]int{}}
	if seconds == 0 {
		summary.Complete = true
		return summary
	}
	start := time.Now()
	until := start.Add(time.Duration(seconds) * time.Second)
	type sample struct {
		responseBytes  uint64
		elapsed        time.Duration
		failure        requestError
		statusFailure  httpStatusError
		quotaExhausted bool
	}
	samples := make(chan sample, p.Workers)
	var workers sync.WaitGroup
	// Each fixed worker owns a finite share of the request cap. No open-loop
	// offered-rate or capacity claim follows from this closed-loop diagnostic.
	for worker := 0; worker < p.Workers; worker++ {
		quota := p.MaxRequests / p.Workers
		if worker < p.MaxRequests%p.Workers {
			quota++
		}
		workers.Go(func() {
			count := 0
			for ; count < quota && time.Now().Before(until) && ctx.Err() == nil; count++ {
				began := time.Now()
				body, err := workloadRequest(ctx, client, p)
				var failure requestError
				var statusFailure httpStatusError
				switch {
				case err != nil:
					_ = errors.As(err, &statusFailure)
					if !errors.As(err, &failure) {
						failure = "transport"
					}
				case p.Scenario == "mixed" || p.Scenario == "hybrid":
					if _, err := validateScenario(body, p); err != nil {
						failure = "correctness"
					}
				case p.Scale != nil:
					value, scaleErr := scaleDigest(body, p.Scale)
					if scaleErr != nil || value != digest {
						failure = "correctness"
					}
				case sha256.Sum256(body) != digest:
					failure = "correctness"
				}
				samples <- sample{elapsed: time.Since(began), failure: failure, statusFailure: statusFailure, responseBytes: uint64(len(body))}
				timer := time.NewTimer(10 * time.Millisecond)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return
				}
			}
			if count == quota && time.Now().Before(until) {
				samples <- sample{quotaExhausted: true}
			}
		})
	}
	go func() { workers.Wait(); close(samples) }()
	quotaExhausted := false
	for sample := range samples {
		if sample.quotaExhausted {
			quotaExhausted = true
			continue
		}
		summary.Count++
		if sample.failure == "" {
			summary.Succeeded++
			summary.SuccessfulResponseBytes += sample.responseBytes
		} else {
			summary.Failed++
			switch sample.failure {
			case "timeout":
				summary.Outcomes.Timeout++
			case "transport":
				summary.Outcomes.Transport++
			case "status":
				summary.Outcomes.Status++
				summary.HTTPFailures[string(sample.statusFailure)]++
			case "body":
				summary.Outcomes.Body++
			case "canceled":
				summary.Outcomes.Canceled++
			default:
				summary.Outcomes.Correctness++
			}
		}
		elapsed := sample.elapsed.Seconds()
		summary.LatencySecondsTotal += elapsed
		for i, upper := range bounds {
			if elapsed <= upper {
				summary.Buckets[i]++
			}
		}
		if elapsed > bounds[len(bounds)-1] {
			summary.Overflow++
		}
	}
	end := time.Now()
	summary.Seconds = end.Sub(start).Seconds()
	summary.StartedAt = start.UnixMilli()
	summary.IssuanceEndedAt = start.Add(min(end.Sub(start), time.Duration(seconds)*time.Second)).UnixMilli()
	// Build the phase timeline from Go's monotonic clock. Keep any wall-clock
	// step explicit so consumers do not falsely correlate external timestamps.
	summary.FinishedAt = start.Add(end.Sub(start)).UnixMilli()
	summary.ClockAdjustmentMS = end.UnixMilli() - summary.FinishedAt
	summary.Complete = ctx.Err() == nil && !quotaExhausted && !end.Before(until)
	return summary
}

// run validates a baseline, warms up, measures, and returns evidence even on failure.
// It samples the generator's own memory separately from the application's metrics.
func run(ctx context.Context, p profile) (output report, runErr error) {
	output = report{SchemaVersion: 3, Model: "closed-loop-development-diagnostic", Profile: p, BucketBounds: bounds, Verdict: "incomplete", Warmup: result{HTTPFailures: map[string]int{}}, Measured: result{HTTPFailures: map[string]int{}}}
	defer func() {
		var sample runtime.MemStats
		runtime.ReadMemStats(&sample)
		output.Generator = generatorSample{Processors: runtime.GOMAXPROCS(0), HeapBytes: sample.HeapAlloc, AllocatedBytes: sample.TotalAlloc, GCCycles: sample.NumGC, Goroutines: runtime.NumGoroutine()}
	}()
	if err := p.validate(); err != nil {
		return output, err
	}
	client := newClient()
	defer client.CloseIdleConnections()
	baselineProfile := p
	if p.Public != nil {
		selected := *p.Public
		selected.Conditional = false
		baselineProfile.Public = &selected
	}
	body, err := workloadRequest(ctx, client, baselineProfile)
	if err != nil {
		return output, err
	}
	baselineCount, err := validateScenario(body, p)
	if err != nil {
		return output, err
	}
	digest := sha256.Sum256(body)
	if p.Public != nil && (len(body) != p.Public.Bytes || fmt.Sprintf("sha256:%x", digest) != p.Public.Digest) {
		return output, errors.New("public-baseline-mismatch")
	}
	if p.Scale != nil {
		digest, err = scaleDigest(body, p.Scale)
		if err != nil {
			return output, err
		}
	}
	output.ResponseDigest = fmt.Sprintf("sha256:%x", digest)
	output.BaselineResults = baselineCount
	phaseDigest := digest
	if p.Public != nil && p.Public.Conditional {
		phaseDigest = sha256.Sum256(nil)
	}
	output.Warmup = phase(ctx, client, p, p.WarmupSeconds, phaseDigest)
	output.Measured = phase(ctx, client, p, p.Seconds, phaseDigest)
	if ctx.Err() != nil || output.Measured.Count == 0 || !output.Warmup.Complete || !output.Measured.Complete {
		return output, errors.New("incomplete-or-failed-workload")
	}
	if output.Warmup.Failed != 0 || output.Measured.Failed != 0 {
		output.Verdict = "failed"
		return output, errors.New("incomplete-or-failed-workload")
	}
	output.Verdict = "passed"
	return output, nil
}

// main forwards the test driver's exit status after execute completes its cleanup.
func main() {
	os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr))
}

// execute reads one strict owned profile and optionally creates a private report
// exclusively. It preserves incomplete-run evidence and never overwrites a report.
func execute(args []string, stdout, stderr io.Writer) int {
	var p profile
	if len(args) < 1 || len(args) > 2 {
		_, _ = fmt.Fprintln(stderr, "expected-owned-profile-file")
		return 2
	}
	// This test-only CLI explicitly accepts an operator-selected local profile;
	// it has no service authority or arbitrary destination setting.
	file, err := os.Open(args[0]) // #nosec G703 -- intentional local CLI input, bounded below.
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "invalid-profile-file")
		return 2
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	_ = file.Close()
	if err != nil || len(raw) > 4096 || jsondoc.Validate(raw) != nil {
		_, _ = fmt.Fprintln(stderr, "invalid-profile-file")
		return 2
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&p) != nil || decoder.Decode(new(any)) != io.EOF || p.validate() != nil {
		_, _ = fmt.Fprintln(stderr, "invalid-profile")
		return 2
	}
	// The supervisor supplies an unused path in its private owned directory.
	// Exclusive creation refuses existing files and symlinks; child diagnostics
	// remain separate from this bounded, typed aggregate on every exit status.
	var reportFile *os.File
	if len(args) == 2 {
		reportFile, err = os.OpenFile(args[1], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G703 -- explicit test-only CLI output, exclusive creation.
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "invalid-report-file")
			return 2
		}
		defer func() { _ = reportFile.Close() }()
		stdout = reportFile
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.WarmupSeconds+p.Seconds+10)*time.Second)
	output, err := run(ctx, p)
	cancel()
	if encodeErr := json.NewEncoder(stdout).Encode(output); encodeErr != nil {
		return 1
	}
	if reportFile != nil && reportFile.Close() != nil {
		return 1
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "performance-workload-incomplete")
		return 1
	}
	return 0
}
