//go:build minikube

// Unit tests exercise isolated validation and controlled dependencies.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestReplicaBatchPercentiles(t *testing.T) {
	durations := []time.Duration{100 * time.Millisecond, time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond}
	original := slices.Clone(durations)
	report := summarizeReplicaBatch("test", durations, 2*time.Second)
	if report.Requests != 4 || report.PerSecond != 2 || report.P50Millis != 5 || report.P95Millis != 100 || report.P99Millis != 100 || !slices.Equal(original, durations) {
		t.Fatalf("incorrect summary or mutated input: %#v", report)
	}
	empty := summarizeReplicaBatch("empty", nil, 0)
	if empty.P99Millis != 0 || empty.PerSecond != 0 {
		t.Fatalf("empty report is not finite: %#v", empty)
	}
}

func TestReplicaCreationResponseContract(t *testing.T) {
	for _, tc := range []struct {
		name, replay, retry, body string
		status                    int
		writer, bad               bool
	}{
		{"created", "", "", "", 201, true, false},
		{"replayed", "true", "", "", 201, false, false},
		{"false-replay", "false", "", "", 201, false, true},
		{"unknown-replay", "yes", "", "", 201, false, true},
		{"pending", "", "1", `{"code":"DEPENDENCY_UNAVAILABLE","status":503}`, 503, false, false},
		{"missing-retry", "", "", `{"code":"DEPENDENCY_UNAVAILABLE","status":503}`, 503, false, true},
		{"replayed-pending", "true", "1", `{"code":"DEPENDENCY_UNAVAILABLE","status":503}`, 503, false, true},
		{"wrong-code", "", "1", `{"code":"INTERNAL","status":503}`, 503, false, true},
		{"wrong-status", "", "1", `{"code":"DEPENDENCY_UNAVAILABLE","status":500}`, 503, false, true},
		{"invalid-json", "", "1", `{`, 503, false, true},
		{"missing-problem", "", "1", `{}`, 503, false, true},
		{"unexpected-status", "", "", "", 500, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writer, err := replicaCreationWriter(httpResult{Status: tc.status, Header: http.Header{"Idempotency-Replayed": {tc.replay}, "Retry-After": {tc.retry}}, Body: []byte(tc.body)})
			if writer != tc.writer || (err != nil) != tc.bad {
				t.Fatalf("writer=%v error=%v", writer, err)
			}
		})
	}
}

func TestAllReplicaPodsReplaced(t *testing.T) {
	before := []podIdentity{{UID: "old-1", Ready: true}, {UID: "old-2", Ready: true}, {UID: "old-3", Ready: true}}
	after := []podIdentity{{UID: "new-1", Ready: true}, {UID: "new-2", Ready: true}, {UID: "new-3", Ready: true}}
	if !allReplicaPodsReplaced(before, after) {
		t.Fatal("complete replacement rejected")
	}
	for _, mutate := range []func([]podIdentity) []podIdentity{
		func(p []podIdentity) []podIdentity { return p[:2] },
		func(p []podIdentity) []podIdentity { p[2].UID = "old-2"; return p },
		func(p []podIdentity) []podIdentity { p[2].UID = "new-1"; return p },
		func(p []podIdentity) []podIdentity { p[2].UID = ""; return p },
		func(p []podIdentity) []podIdentity { p[2].Ready = false; return p },
	} {
		if allReplicaPodsReplaced(before, mutate(slices.Clone(after))) {
			t.Fatal("incomplete or invalid replacement accepted")
		}
	}
	if allReplicaPodsReplaced(before[:2], after) || allReplicaPodsReplaced(append(slices.Clone(before[:2]), before[0]), after) {
		t.Fatal("invalid initial evidence accepted")
	}
}

func TestReplicaUpdateChecksEveryReplicaAndAudit(t *testing.T) {
	want := httpResult{Status: 200, Header: http.Header{"Etag": {`"rev-2"`}}, Body: []byte(`{"id":"namespace-id","tenantId":"` + primaryTenantID + `","displayName":"Winner","revision":2}`)}
	for _, broken := range []string{"", "stale-replica", "duplicate-audit"} {
		t.Run(broken, func(t *testing.T) {
			var reads []int
			request := func(replica int, method, path, _, _ string, _ any) (httpResult, error) {
				if method != http.MethodGet {
					t.Fatal("recovery verification must not write")
				}
				if strings.Contains(path, "audit-events?") {
					raw := `{"items":[{"targetId":"namespace-id","action":"namespace.updated"}]}`
					if broken == "duplicate-audit" {
						raw = `{"items":[{},{}]}`
					}
					return httpResult{Status: 200, Body: []byte(raw)}, nil
				}
				reads = append(reads, replica)
				result := want
				if broken == "stale-replica" && replica == 2 {
					result.Header = http.Header{"Etag": {`"rev-1"`}}
				}
				return result, nil
			}
			err := (&harness{}).verifyReplicaUpdate(request, want)
			if (err == nil) != (broken == "") || !slices.Equal(reads, []int{0, 1, 2}) {
				t.Fatalf("reads=%v err=%v", reads, err)
			}
		})
	}
}

func TestReplicaResourceSamples(t *testing.T) {
	valid := `{"podRef":{"name":"owned","namespace":"` + namespace + `"},"memory":{"workingSetBytes":0},"containers":[{"cpu":{"usageCoreNanoSeconds":12},"memory":{"workingSetBytes":34}}]}`
	for _, tc := range []struct {
		name, pods string
		want       int
		bad        bool
	}{
		{"container-not-pod-aggregate", valid, 1, false},
		{"other-namespace", strings.ReplaceAll(valid, namespace, "another"), 0, false},
		{"unknown-pod", strings.ReplaceAll(valid, "owned", "unselected"), 0, false},
		{"pending-stats", strings.ReplaceAll(valid, `"workingSetBytes":34`, `"workingSetBytes":0`), 0, false},
		{"duplicate", valid + "," + valid, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			samples, err := replicaResourceSamples([]byte(`{"pods":[`+tc.pods+`]}`), map[string]string{"owned": "ui"}, "test")
			if (err != nil) != tc.bad || len(samples) != tc.want {
				t.Fatalf("samples=%v err=%v", samples, err)
			}
			if len(samples) == 1 && (samples[0].WorkingSet != 34 || samples[0].CPUCoreNanos != 12 || samples[0].Role != "ui") {
				t.Fatal("wrong container counters")
			}
		})
	}
	if _, err := replicaResourceSamples([]byte(`{`), nil, "test"); err == nil {
		t.Fatal("malformed stats accepted")
	}
}

func TestReplicaNamespaceComparison(t *testing.T) {
	value := catalog.Namespace{ID: "test-namespace", TenantID: primaryTenantID, Slug: "test", DisplayName: "Test", Revision: 1}
	encode := func(value catalog.Namespace) httpResult {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return httpResult{Status: 200, Body: raw, Header: http.Header{"Etag": {`"rev-1"`}}}
	}
	want := encode(value)
	if !sameNamespaceState(want, want) {
		t.Fatal("identical namespace rejected")
	}
	for _, change := range []func(*catalog.Namespace){
		func(v *catalog.Namespace) { v.ID = "different" },
		func(v *catalog.Namespace) { v.TenantID = otherTenantID },
		func(v *catalog.Namespace) { v.Slug = "different" },
		func(v *catalog.Namespace) { v.DisplayName = "different" },
		func(v *catalog.Namespace) { v.Revision++ },
	} {
		changed := value
		change(&changed)
		if sameNamespaceState(encode(changed), want) {
			t.Fatal("changed namespace accepted")
		}
	}
	bad := want
	bad.Status = 404
	if sameNamespaceState(bad, want) {
		t.Fatal("error response accepted")
	}
	bad = want
	bad.Body = []byte(`{`)
	if sameNamespaceState(bad, want) {
		t.Fatal("invalid JSON accepted")
	}
}

func TestReplicaEvidenceRejectsChangedReplayAndDuplicateAudit(t *testing.T) {
	raw := []byte(`{"id":"namespace-id","tenantId":"` + primaryTenantID + `","slug":"fixture","displayName":"Fixture","revision":1}`)
	headers := http.Header{"Etag": {`"rev-1"`}}
	entry := replicaEvidence{Key: "fixture-key", Body: map[string]string{"slug": "fixture", "displayName": "Fixture"}, Want: httpResult{Status: 201, Body: raw, Header: headers}}
	for _, broken := range []string{"", "replay-body", "replay-header", "read-state", "audit-duplicate", "all-etags-missing", "wrong-etag", "wrong-status"} {
		t.Run(broken, func(t *testing.T) {
			checked := entry
			checked.Want.Header = headers.Clone()
			if broken == "all-etags-missing" {
				checked.Want.Header.Del("ETag")
			}
			if broken == "wrong-etag" {
				checked.Want.Header.Set("ETag", `"rev-9"`)
			}
			if broken == "wrong-status" {
				checked.Want.Status = 200
			}
			var replayed, read []int
			request := func(index int, method, path, _, _ string, _ any) (httpResult, error) {
				result := httpResult{Status: 200, Body: raw, Header: checked.Want.Header.Clone()}
				switch {
				case method == "POST":
					replayed = append(replayed, index)
					result.Status = 201
					result.Header.Set("Idempotency-Replayed", "true")
					if broken == "replay-body" {
						result.Body = append(slices.Clone(raw), ' ')
					}
					if broken == "replay-header" {
						result.Header.Del("Idempotency-Replayed")
					}
				case strings.Contains(path, "audit-events?"):
					result.Body = []byte(`{"items":[{"targetId":"namespace-id","action":"namespace.created"}]}`)
					if broken == "audit-duplicate" {
						result.Body = []byte(`{"items":[{},{}]}`)
					}
				default:
					read = append(read, index)
					if broken == "read-state" {
						result.Status = 404
					}
				}
				return result, nil
			}
			err := (&harness{}).verifyReplicaEvidence(request, checked)
			if broken == "" && (!slices.Equal(replayed, []int{0, 1, 2}) || !slices.Equal(read, []int{0, 1, 2})) {
				t.Fatalf("incomplete replica checks: replay=%v read=%v", replayed, read)
			}
			if (err == nil) != (broken == "") {
				t.Fatalf("broken=%q: %v", broken, err)
			}
		})
	}
}

func TestReplicaCompletedCommandBinding(t *testing.T) {
	for _, method := range []string{"POST", "PATCH"} {
		for writer := range 3 {
			for _, fault := range []string{"", "wrong-status", "wrong-code", "wrong-problem-status", "cached-rejection", "precondition-412", "replay-body", "replay-etag", "replay-marker"} {
				t.Run(fmt.Sprintf("%s/%d/%s", method, writer, fault), func(t *testing.T) {
					body := map[string]string{"displayName": "Original"}
					match := ""
					status, etag := 201, `"rev-1"`
					if method == "PATCH" {
						match = `"rev-1"`
						status, etag = 200, `"rev-2"`
					}
					want := httpResult{Status: status, Header: http.Header{"Etag": {etag}}, Body: []byte(`{"id":"retained"}`)}
					var rejects, replays []int
					request := func(index int, gotMethod, path, key, gotMatch string, value any) (httpResult, error) {
						if gotMethod != method || path != "/command" || key != "original-key" {
							t.Fatal("changed command binding")
						}
						gotBody, ok := value.(map[string]string)
						if !ok {
							t.Fatal("unexpected command body type")
						}
						if gotBody["displayName"] != body["displayName"] || gotMatch != match {
							rejects = append(rejects, index)
							if index != (writer+1)%3 || gotBody["displayName"] != "Original changed" && gotMatch != `"rev-999"` {
								t.Fatal("wrong rejection route/input")
							}
							code, problemStatus, responseStatus := "IDEMPOTENCY_KEY_REUSED", 409, 409
							if fault == "wrong-status" {
								responseStatus = 500
							}
							if fault == "wrong-code" {
								code = "CONFLICT"
							}
							if fault == "wrong-problem-status" {
								problemStatus = 500
							}
							if fault == "precondition-412" && gotMatch != match {
								responseStatus = 412
								problemStatus = 412
								code = "PRECONDITION_FAILED"
							}
							result := httpResult{Status: responseStatus, Header: http.Header{}, Body: fmt.Appendf(nil, `{"status":%d,"code":%q}`, problemStatus, code)}
							if fault == "cached-rejection" {
								result.Header.Set("Idempotency-Replayed", "true")
							}
							return result, nil
						}
						replays = append(replays, index)
						result := want
						result.Header = want.Header.Clone()
						result.Header.Set("Idempotency-Replayed", "true")
						if fault == "replay-body" {
							result.Body = append(slices.Clone(want.Body), ' ')
						}
						if fault == "replay-etag" {
							result.Header.Del("ETag")
						}
						if fault == "replay-marker" {
							result.Header.Del("Idempotency-Replayed")
						}
						return result, nil
					}
					err := verifyReplicaCommandBinding(request, writer, method, "/command", "original-key", match, body, want)
					success := fault == "" || fault == "precondition-412" && method == "POST"
					if (err == nil) != success {
						t.Fatalf("unexpected result: %v", err)
					}
					if body["displayName"] != "Original" {
						t.Fatal("negative request changed original body")
					}
					if success {
						expected := []int{(writer + 1) % 3, (writer + 2) % 3, writer}
						if !slices.Equal(replays, expected) {
							t.Fatalf("replayed indices %v want %v", replays, expected)
						}
						count := 1
						if method == "PATCH" {
							count = 2
						}
						if len(rejects) != count {
							t.Fatalf("rejections: %v", rejects)
						}
					}
				})
			}
		}
	}
}
