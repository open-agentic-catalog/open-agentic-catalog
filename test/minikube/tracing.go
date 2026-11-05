//go:build minikube

// Tracing checks follow request and durable-work causality through a controlled OTLP receiver.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	tracefixture "github.com/open-agentic-catalog/open-agentic-catalog/test/tracing"
)

type traceIngressTransport struct{ parent string }

// RoundTrip injects synthetic private trace context into an owned ingress probe.
// It clones the request so the caller's headers are unchanged; export assertions
// must prove that the private baggage and tracestate do not leave the Catalog.
func (t traceIngressTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Traceparent", t.parent)
	r.Header.Set("Tracestate", "private=fixture-secret")
	r.Header.Set("Baggage", "private=fixture-secret")
	return http.DefaultTransport.RoundTrip(r)
}

// verifyTracing qualifies opt-in tracing with owned collectors and replicas, checking parentage, replay, worker takeover, exporter failure isolation and disablement.
func (h *harness) verifyTracing() error {
	caPath := filepath.Join(h.temporary, "fixture", "trust", "functional-ca.pem")
	ca, err := os.ReadFile(caPath) // #nosec G304 -- invocation-owned fixture CA.
	if err != nil {
		return err
	}
	client, err := fixtureHTTPClient(ca)
	if err != nil {
		return err
	}
	address, err := h.startPortForward("service/federation-fixture", 8443)
	if err != nil {
		return err
	}
	fixtureURL := "https://" + address
	capture := func() ([]tracefixture.Span, error) {
		result, err := h.request(http.MethodGet, fixtureURL+"/tracing/state", "", "", "", nil, client)
		if err != nil || result.Status != 200 {
			return nil, errors.New("trace fixture unavailable")
		}
		return tracefixture.Decode(result.Body)
	}
	await := func(label string, check func([]tracefixture.Span) bool) error {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			spans, err := capture()
			if err != nil {
				return err
			}
			if check(spans) {
				return nil
			}
			select {
			case <-h.ctx.Done():
				return h.ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
		return fmt.Errorf("tracing assertion timed out: %s", label)
	}
	if spans, err := capture(); err != nil || len(spans) != 0 {
		return errors.New("disabled Helm processes exported spans")
	}
	if err := h.createSecret("create scoped tracing inputs", "catalog-tracing", map[string]string{"headers.json": `{"Authorization":"Bearer tracing-fixture"}`, "ca.pem": string(ca)}); err != nil {
		return err
	}
	rollout := func(enabled bool, marker string) ([]string, error) {
		h.tracing = enabled
		values, err := h.writeHelmValues(marker, "catalog_api")
		if err != nil {
			return nil, err
		}
		if _, err := h.helm("roll out tracing setting", "upgrade", releaseName, h.options.repositoryRoot+"/deployments/helm/open-agentic-catalog", "--namespace", namespace, "--values", values, "--wait", "--wait-for-jobs", "--timeout=4m"); err != nil {
			return nil, err
		}
		pods, err := h.pods("api")
		if err != nil || len(pods) != 2 {
			return nil, errors.New("tracing requires two ready APIs")
		}
		var bases []string
		for _, pod := range pods {
			address, err := h.startPortForward("pod/"+pod.Name, 8080)
			if err != nil {
				return nil, err
			}
			bases = append(bases, "http://"+address)
		}
		return bases, nil
	}
	bases, err := rollout(true, "tracing-enabled")
	if err != nil {
		return err
	}
	parent := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	upstream := &http.Client{Transport: traceIngressTransport{parent}, Timeout: 10 * time.Second}
	for _, base := range bases {
		result, err := h.request(http.MethodGet, base+"/api/v1/namespaces?limit=2", h.tokens["admin"], "", "", nil, upstream)
		if err != nil || result.Status != 200 {
			return errors.New("cross-replica traced request failed")
		}
	}
	instances := map[string]bool{}
	if err := await("two API identities and upstream parent", func(spans []tracefixture.Span) bool {
		for _, s := range spans {
			if s.TraceID == parent[3:35] && s.ParentID == parent[36:52] {
				instances[s.Instance] = true
			}
		}
		return len(instances) == 2
	}); err != nil {
		return err
	}
	var createdID string
	for index, base := range bases {
		result, err := h.request(http.MethodPost, base+"/api/v1/namespaces", h.tokens["admin"], "trace-replay-"+h.suffix, "", map[string]string{"slug": "trace-" + h.suffix, "displayName": "Trace replay"}, upstream)
		if err != nil || result.Status != 201 {
			return errors.New("cross-replica traced command failed")
		}
		var document struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(result.Body, &document) != nil || document.ID == "" {
			return errors.New("invalid traced command response")
		}
		if index == 0 {
			createdID = document.ID
		} else if document.ID != createdID || result.Header.Get("Idempotency-Replayed") != "true" {
			return errors.New("cross-replica replay repeated mutation")
		}
	}
	inspection, err := h.request(http.MethodPost, bases[0]+"/api/v1/federation/source-inspections", h.tokens["admin"], "trace-inspect-"+h.suffix, "", map[string]any{"url": "https://federation-fixture:8443/.well-known/ard.json", "networkProfile": "functional-fixture"}, upstream)
	if err != nil || inspection.Status != 201 {
		return errors.New("trace source inspection failed")
	}
	var inspected struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(inspection.Body, &inspected) != nil {
		return errors.New("invalid trace inspection")
	}
	source, err := h.request(http.MethodPost, bases[0]+"/api/v1/federation/sources", h.tokens["admin"], "trace-source-"+h.suffix, "", map[string]any{"inspectionId": inspected.ID, "displayName": "Trace source", "retrievalMode": "indexed", "scope": map[string]any{"kinds": []string{"agent"}}, "updateMode": "manual_review", "networkProfile": "functional-fixture", "reason": "Trace qualification"}, upstream)
	if err != nil || source.Status != 201 {
		return errors.New("trace source connection failed")
	}
	var connected struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(source.Body, &connected) != nil || connected.ID == "" {
		return errors.New("invalid trace source")
	}
	// Reuse the existing deterministic database barrier and owner-loss scenario.
	// It starts with two workers, isolates the owner, then replaces that process
	// before releasing the barrier and restoring two replicas.
	if err := h.verifyWorkerLeaseRecovery(bases[0], fixtureURL, caPath, connected.ID); err != nil {
		return err
	}
	if err := await("fresh linked attempts after process loss", func(spans []tracefixture.Span) bool {
		producers := map[string]bool{}
		for _, s := range spans {
			if s.Name == "job enqueue" {
				producers[s.TraceID+"/"+s.SpanID] = true
			}
		}
		attempts := map[string]map[string]bool{}
		for _, s := range spans {
			if s.Name != "job process" || len(s.Links) != 1 || s.Service != "open-agentic-catalog-worker" {
				continue
			}
			link := s.Links[0]
			key := link.TraceID + "/" + link.SpanID
			if !producers[key] || s.TraceID == link.TraceID {
				continue
			}
			if attempts[key] == nil {
				attempts[key] = map[string]bool{}
			}
			attempts[key][s.Instance] = true
			if len(attempts[key]) >= 2 {
				return true
			}
		}
		return false
	}); err != nil {
		return err
	}
	control, err := h.request(http.MethodPost, fixtureURL+"/tracing/control", "", "", "", map[string]bool{"reject": true}, client)
	if err != nil || control.Status != 204 {
		return errors.New("collector rejection fixture failed")
	}
	for _, base := range bases {
		for range 5 {
			if result, err := h.request(http.MethodGet, base+"/api/v1/namespaces?limit=2", h.tokens["admin"], "", "", nil, nil); err != nil || result.Status != 200 {
				return errors.New("collector outage affected product")
			}
		}
		if err := h.waitHTTP(base + "/readyz"); err != nil {
			return err
		}
	}
	if _, err := h.request(http.MethodPost, fixtureURL+"/tracing/control", "", "", "", map[string]bool{"reject": false}, client); err != nil {
		return err
	}
	bases, err = rollout(true, "tracing-restarted")
	if err != nil {
		return err
	}
	for _, base := range bases {
		if _, err := h.request(http.MethodGet, base+"/api/v1/namespaces?limit=2", h.tokens["admin"], "", "", nil, upstream); err != nil {
			return err
		}
	}
	if err := await("new replica identities after rollout", func(spans []tracefixture.Span) bool {
		fresh := map[string]bool{}
		for _, s := range spans {
			if s.TraceID == parent[3:35] && s.ParentID == parent[36:52] && !instances[s.Instance] {
				fresh[s.Instance] = true
			}
		}
		return len(fresh) == 2
	}); err != nil {
		return err
	}
	if _, err := rollout(false, "tracing-disabled"); err != nil {
		return err
	}
	if _, err := h.request(http.MethodPost, fixtureURL+"/tracing/control", "", "", "", map[string]bool{"reset": true}, client); err != nil {
		return err
	}
	if spans, err := capture(); err != nil || len(spans) != 0 {
		return errors.New("disabled tracing still exports")
	}
	h.report.Checks = append(h.report.Checks, "otlp-two-api-parentage-and-replay", "otlp-linked-worker-lease-takeover", "otlp-secret-trust-outage-and-disable", "otlp-distinct-process-identities-after-rollout")
	return nil
}
