//go:build minikube

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestDrainEndpointsRequireSelectedMembershipAndSurvivor(t *testing.T) {
	selected := podIdentity{Name: "selected", UID: "old"}
	survivor := podIdentity{Name: "survivor", UID: "survivor-uid"}
	const both = `{"items":[{"metadata":{"namespace":"open-agentic-catalog-e2e","labels":{"kubernetes.io/service-name":"catalog"}},"endpoints":[{"conditions":{"ready":true},"targetRef":{"kind":"Pod","namespace":"open-agentic-catalog-e2e","name":"selected","uid":"old"}},{"conditions":{"ready":true},"targetRef":{"kind":"Pod","namespace":"open-agentic-catalog-e2e","name":"survivor","uid":"survivor-uid"}}]}]}`
	// Bind the fixture to the harness's owned namespace without a second inventory.
	baseline := strings.ReplaceAll(both, "open-agentic-catalog-e2e", namespace)
	removed := strings.Replace(baseline, `"ready":true`, `"ready":false`, 1)
	terminating := strings.Replace(baseline, `"ready":true`, `"ready":true,"terminating":true`, 1)
	for _, tc := range []struct {
		name, raw     string
		removed, want bool
	}{
		{"initial", baseline, false, true}, {"still-ready", baseline, true, false},
		{"removed", removed, true, true}, {"never-ready", removed, false, false}, {"terminating", terminating, true, true},
		{"empty", `{"items":[]}`, true, false}, {"invalid", `{`, true, false},
		{"wrong-service", strings.ReplaceAll(removed, `"catalog"`, `"other"`), true, false},
		{"wrong-namespace", strings.ReplaceAll(removed, namespace, "elsewhere"), true, false},
		{"no-survivor", strings.ReplaceAll(removed, `"ready":true`, `"ready":false`), true, false},
		{"wrong-survivor", strings.ReplaceAll(removed, "survivor-uid", "wrong"), true, false},
		{"wrong-initial-uid", strings.ReplaceAll(baseline, `"old"`, `"wrong"`), false, false},
		{"unknown-ready", strings.ReplaceAll(baseline, `"ready":true`, `"ready":null`), false, false},
		{"unknown-selected-is-not-removal", strings.Replace(baseline, `"ready":true`, `"ready":null`, 1), true, false},
		{"missing-selected-ready-is-not-removal", strings.Replace(baseline, `"ready":true`, `"serving":true`, 1), true, false},
		{"terminating-unknown-selected", strings.Replace(terminating, `"ready":true`, `"ready":null`, 1), true, true},
		{"non-pod-target", strings.ReplaceAll(removed, `"kind":"Pod"`, `"kind":"Other"`), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := drainEndpoints([]byte(tc.raw), selected, survivor, tc.removed); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestDrainResponseRequiresRetainedStateOrExactDeadline(t *testing.T) {
	want := topologyTestEvidence(t)
	success := want.Want
	success.Status = 200
	path := "/api/v1/namespaces/namespace-id"
	deadline := httpResult{Status: 503, Header: http.Header{"Content-Type": {"application/problem+json"}, "X-Request-Id": {"request-id"}}, Body: []byte(`{"type":"https://catalog.example.com/problems/request-deadline-exceeded","title":"Request deadline exceeded","status":503,"code":"REQUEST_DEADLINE_EXCEEDED","detail":"the request exceeded the service work deadline","instance":"` + path + `","requestId":"request-id"}`)}
	for _, response := range []httpResult{success, deadline} {
		if err := validateDrainResponse(response, want.Want, path); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*httpResult)
	}{
		{"gateway", func(r *httpResult) { r.Status = 504 }},
		{"arbitrary-503", func(r *httpResult) { r.Body = []byte(`{}`) }},
		{"dependency", func(r *httpResult) {
			r.Body = []byte(strings.ReplaceAll(string(r.Body), "REQUEST_DEADLINE_EXCEEDED", "DEPENDENCY_UNAVAILABLE"))
		}},
		{"raw-detail", func(r *httpResult) {
			r.Body = []byte(strings.ReplaceAll(string(r.Body), "the request exceeded the service work deadline", "private dependency detail"))
		}},
		{"extra-private-field", func(r *httpResult) { r.Body = append([]byte(`{"cause":"private",`), r.Body[1:]...) }},
		{"missing-request-id", func(r *httpResult) { r.Header.Del("X-Request-ID") }},
		{"wrong-request-id", func(r *httpResult) { r.Header.Set("X-Request-ID", "other") }},
		{"wrong-media", func(r *httpResult) { r.Header.Set("Content-Type", "application/json") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := deadline
			r.Header = deadline.Header.Clone()
			tc.mutate(&r)
			if validateDrainResponse(r, want.Want, path) == nil {
				t.Fatal("invalid deadline accepted")
			}
		})
	}
	success.Body = []byte(`{}`)
	if validateDrainResponse(success, want.Want, path) == nil {
		t.Fatal("missing retained state accepted")
	}
}

func TestReplacementRequiresOldAbsenceSurvivorAndNewReadyUID(t *testing.T) {
	before := []podIdentity{{Name: "old", UID: "old-uid", Ready: true}, {Name: "survivor", UID: "survivor-uid", Ready: true}}
	good := []podIdentity{before[1], {Name: "new", UID: "new-uid", Ready: true}}
	if !replacementReady(before, good) {
		t.Fatal("valid replacement rejected")
	}
	for _, bad := range [][]podIdentity{nil, before, {before[1]}, {before[1], before[1]}, {before[1], {Name: "new", UID: "new-uid"}}, {before[1], {Name: "old", UID: "new-uid", Ready: true}}, {before[1], {Name: "new", UID: "", Ready: true}}, {{Name: "other", UID: "other-uid", Ready: true}, good[1]}} {
		if replacementReady(before, bad) {
			t.Fatalf("invalid replacement accepted: %#v", bad)
		}
	}
}

func TestTopologyWaitRejectsLateOrMissingObservation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		delay time.Duration
		ok    bool
		err   error
	}{
		{"timely", 0, true, nil}, {"late-success", 2 * time.Second, true, nil}, {"never", 0, false, nil}, {"observation-error", 0, false, errors.New("probe failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				err := waitTopology(ctx, func() (bool, error) { time.Sleep(tc.delay); return tc.ok, tc.err })
				if (err == nil) != (tc.name == "timely") {
					t.Fatalf("unexpected result: %v", err)
				}
			})
		})
	}
}

func topologyTestEvidence(t *testing.T) replicaEvidence {
	t.Helper()
	body := map[string]string{"slug": "retained", "displayName": "Retained"}
	raw, err := json.Marshal(catalog.Namespace{ID: "namespace-id", TenantID: primaryTenantID, Slug: body["slug"], DisplayName: body["displayName"], Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	return replicaEvidence{Key: "retained-key", Body: body, Want: httpResult{Status: 201, Header: http.Header{"Etag": {`"rev-1"`}}, Body: raw}}
}

func TestTopologyEvidenceRejectsLostStateReplayAndAudit(t *testing.T) {
	for _, failure := range []string{"", "read", "changed-read", "replay", "changed-replay", "replay-marker", "audit", "duplicate-audit", "transport"} {
		t.Run(failure, func(t *testing.T) {
			entry := topologyTestEvidence(t)
			calls := 0
			request := func(_ int, method, path, key, match string, body any) (httpResult, error) {
				calls++
				if failure == "transport" {
					return httpResult{}, errors.New("offline")
				}
				response := entry.Want
				response.Header = entry.Want.Header.Clone()
				switch calls {
				case 1:
					if method != "GET" || path != "/api/v1/namespaces/namespace-id" || key != "" || body != nil {
						t.Fatal("incorrect read")
					}
					response.Status = 200
					if failure == "read" {
						response.Status = 404
					}
					if failure == "changed-read" {
						response.Body = []byte(strings.ReplaceAll(string(response.Body), "Retained", "Changed"))
					}
				case 2:
					raw, _ := json.Marshal(body)
					expected, _ := json.Marshal(entry.Body)
					if method != "POST" || path != "/api/v1/namespaces" || key != entry.Key || string(raw) != string(expected) || match != "" {
						t.Fatal("replay inputs changed")
					}
					response.Header.Set("Idempotency-Replayed", "true")
					if failure == "replay" {
						response.Status = 503
					}
					if failure == "changed-replay" {
						response.Body = []byte(`{}`)
					}
					if failure == "replay-marker" {
						response.Header.Del("Idempotency-Replayed")
					}
				case 3:
					if method != "GET" || !strings.Contains(path, "targetId=namespace-id") || !strings.Contains(path, "limit=2") {
						t.Fatal("missing targeted audit")
					}
					response.Status = 200
					response.Body = []byte(`{"items":[{"targetId":"namespace-id","action":"namespace.created"}]}`)
					if failure == "audit" {
						response.Body = []byte(`{"items":[]}`)
					}
					if failure == "duplicate-audit" {
						response.Body = []byte(`{"items":[{"targetId":"namespace-id","action":"namespace.created"},{"targetId":"namespace-id","action":"namespace.created"}]}`)
					}
				default:
					t.Fatal("unexpected call")
				}
				return response, nil
			}
			h := &harness{}
			err := h.verifyTopologyEvidence(request, entry)
			if (err == nil) != (failure == "") {
				t.Fatalf("unexpected verification result: %v", err)
			}
			if failure == "" && calls != 3 {
				t.Fatal("skipped evidence phase")
			}
		})
	}
}

func TestTopologyEvidenceRejectsInvalidCreationBeforeReadback(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*replicaEvidence)
	}{
		{"key", func(e *replicaEvidence) { e.Key = "" }},
		{"status", func(e *replicaEvidence) { e.Want.Status = 200 }},
		{"etag", func(e *replicaEvidence) { e.Want.Header.Del("ETag") }},
		{"malformed", func(e *replicaEvidence) { e.Want.Body = []byte(`{`) }},
		{"missing-id", func(e *replicaEvidence) {
			e.Want.Body = []byte(strings.ReplaceAll(string(e.Want.Body), "namespace-id", ""))
		}},
		{"tenant", func(e *replicaEvidence) {
			e.Want.Body = []byte(strings.ReplaceAll(string(e.Want.Body), primaryTenantID, otherTenantID))
		}},
		{"revision", func(e *replicaEvidence) {
			e.Want.Body = []byte(strings.ReplaceAll(string(e.Want.Body), `"revision":1`, `"revision":2`))
		}},
		{"slug", func(e *replicaEvidence) { e.Body["slug"] = "wrong" }},
		{"display-name", func(e *replicaEvidence) { e.Body["displayName"] = "Wrong" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := topologyTestEvidence(t)
			tc.mutate(&entry)
			request := func(_ int, _, _, _, _ string, _ any) (httpResult, error) {
				t.Fatal("invalid creation reached readback")
				return httpResult{}, nil
			}
			h := &harness{}
			if h.verifyTopologyEvidence(request, entry) == nil {
				t.Fatal("invalid creation accepted")
			}
		})
	}
}
