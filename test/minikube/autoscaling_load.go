//go:build minikube

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

const hpaReaders = 8
const hpaLoadDuration = 3 * time.Minute
const hpaReadInterval = 100 * time.Millisecond
const hpaReadsPerReader = 1800

type hpaFailure struct {
	Reader         int    `json:"reader"`
	Status         int    `json:"status"`
	Code           string `json:"code"`
	Classification string `json:"classification"`
	StartedMS      int64  `json:"startedMs"`
	DurationMS     int64  `json:"durationMs"`
	PhaseStart     string `json:"phaseStart"`
	PhaseEnd       string `json:"phaseEnd"`
}

type hpaLoadReport struct {
	Failures     []hpaFailure `json:"failures,omitempty"`
	Truncated    bool         `json:"failuresTruncated,omitempty"`
	FirstFailure string       `json:"firstFailure,omitempty"`
	Succeeded    int          `json:"succeeded"`
	Failed       int          `json:"failed"`
	Complete     bool         `json:"complete"`
	Readers      int          `json:"readers"`
	MaxRequests  int          `json:"maxRequests"`
	Seconds      float64      `json:"seconds"`
}

type hpaLoad struct {
	done  chan struct{}
	stop  context.CancelFunc
	mu    sync.Mutex
	phase string
	value hpaLoadReport
}

func (l *hpaLoad) report() hpaLoadReport {
	l.mu.Lock()
	defer l.mu.Unlock()
	value := l.value
	value.Failures = slices.Clone(value.Failures)
	return value
}

func (l *hpaLoad) setPhase(phase string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.phase = phase
}

// The fixture keeps access policy stable during load but runs source Jobs.
// Keep every result field exact while allowing the resulting generation change.
func hpaSearchDigest(result httpResult) ([32]byte, error) {
	if !validInstrumentationSearch(result) {
		return [32]byte{}, errors.New("incomplete or invalid autoscaling Search response")
	}
	var page catalog.CatalogSearchResponse
	if err := json.Unmarshal(result.Body, &page); err != nil {
		return [32]byte{}, err
	}
	page.Generation = ""
	raw, err := json.Marshal(page)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(raw), nil
}

func (h *harness) startHPALoad(addresses []string) (*hpaLoad, error) {
	if len(addresses) != 2 {
		return nil, errors.New("autoscaling Search requires two initial Pod addresses")
	}
	client := replicaHTTPClient()
	client.Timeout = 2 * time.Second
	body := map[string]any{"origins": []string{"local"}, "limit": 20}
	control, err := h.request("POST", addresses[0]+"/api/v1/catalog/search", h.tokens["admin"], "", "", body, client)
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	expected, err := hpaSearchDigest(control)
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	ctx, cancel := context.WithCancel(h.ctx)
	load := &hpaLoad{phase: "scaling-up", done: make(chan struct{}), stop: cancel, value: hpaLoadReport{Readers: hpaReaders, MaxRequests: hpaReaders * hpaReadsPerReader}}
	start := time.Now()
	deadline := start.Add(hpaLoadDuration)
	var wg sync.WaitGroup
	for i := range hpaReaders {
		wg.Go(func() {
			ticker := time.NewTicker(hpaReadInterval)
			defer ticker.Stop()
			for range hpaReadsPerReader {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
				if !time.Now().Before(deadline) {
					return
				}
				// Let an issued request finish under its own timeout at the phase boundary.
				started := time.Now()
				load.mu.Lock()
				phase := load.phase
				load.mu.Unlock()
				r, e := h.requestContext(ctx, "POST", addresses[i%len(addresses)]+"/api/v1/catalog/search", h.tokens["admin"], "", "", body, client)
				digest, valid := hpaSearchDigest(r)
				load.mu.Lock()
				if e != nil || valid != nil || digest != expected {
					load.value.Failed++
					if len(load.value.Failures) < 32 {
						load.value.Failures = append(load.value.Failures, hpaFailure{Reader: i, Status: r.Status, Code: hpaProblemCode(r), Classification: hpaProblemClass(r), StartedMS: started.Sub(start).Milliseconds(), DurationMS: time.Since(started).Milliseconds(), PhaseStart: phase, PhaseEnd: load.phase})
					} else {
						load.value.Truncated = true
					}
					if load.value.FirstFailure == "" {
						switch {
						case e != nil:
							load.value.FirstFailure = "transport"
						case r.Status != 200:
							load.value.FirstFailure = fmt.Sprintf("HTTP-%d", r.Status)
						case valid != nil:
							load.value.FirstFailure = "invalid-page"
						default:
							load.value.FirstFailure = "changed-content:" + hpaChangedFields(control.Body, r.Body)
						}
						fmt.Printf("[autoscaling] first Search failure: %s\n", load.value.FirstFailure)
					}
				} else {
					load.value.Succeeded++
				}
				load.mu.Unlock()
			}
		})
	}
	go func() {
		wg.Wait()
		client.CloseIdleConnections()
		load.mu.Lock()
		load.value.Seconds = time.Since(start).Seconds()
		load.value.Complete = ctx.Err() == nil && !time.Now().Before(deadline)
		load.mu.Unlock()
		close(load.done)
	}()
	return load, nil
}

// Retain only field names, not private response values, in failure diagnostics.
func hpaChangedFields(before, after []byte) string {
	var a, b map[string]any
	if json.Unmarshal(before, &a) != nil || json.Unmarshal(after, &b) != nil {
		return "invalid-json"
	}
	fields := []string{}
	known := map[string]bool{"fallbackReason": true, "partialIndex": true, "items": true, "nextCursor": true, "durableTotal": true, "liveCount": true, "profile": true, "tier": true, "partial": true, "diagnostics": true}
	for key := range a {
		if key != "generation" && !known[key] {
			return "unknown-fields"
		}
	}
	for key := range b {
		if key != "generation" && !known[key] {
			return "unknown-fields"
		}
	}
	for key, v := range a {
		if key != "generation" && !reflect.DeepEqual(v, b[key]) {
			fields = append(fields, key)
		}
	}
	for key := range b {
		if _, ok := a[key]; !ok && key != "generation" {
			fields = append(fields, key)
		}
	}
	slices.Sort(fields)
	return fmt.Sprint(fields)
}

// Record only known service codes. Never retain problem details, headers or arbitrary text.
func hpaProblemCode(result httpResult) string {
	if len(result.Body) > 8192 {
		return "unclassified"
	}
	var problem struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(result.Body, &problem) != nil {
		return "unclassified"
	}
	switch problem.Code {
	case string(catalog.CodeInvalidArgument), string(catalog.CodeNotFound), string(catalog.CodeConflict), string(catalog.CodePreconditionFailed),
		string(catalog.CodeUnauthenticated), string(catalog.CodeForbidden), string(catalog.CodeDependencyUnavailable),
		string(catalog.CodeWorkLimitExceeded), string(catalog.CodeRequestDeadlineExceeded), string(catalog.CodeInternal):
		return problem.Code
	}
	return "unclassified"
}

// Match exact code-owned messages, then emit labels rather than response text.
func hpaProblemClass(result httpResult) string {
	if len(result.Body) > 8192 {
		return "unclassified"
	}
	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(result.Body, &problem) != nil {
		return "unclassified"
	}
	if result.Status == 400 && problem.Code == "INVALID_ARGUMENT" && problem.Detail == "collection authorization changed; restart the read" {
		return "collection-authorization-revision"
	}
	if result.Status == 403 && problem.Code == "FORBIDDEN" && problem.Detail == "federation admission failed" {
		return "federation-admission"
	}
	return "unclassified"
}
