//go:build minikube

package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestServiceReadRequiresFreshRetainedStateOnExpectedProcess(t *testing.T) {
	want := httpResult{Status: 200, Header: http.Header{"Etag": {`"rev-1"`}}, Body: []byte(`{"id":"retained"}`)}
	good := serviceCommandResult{Outcome: "ok", Status: 200, InstanceID: "survivor", ETag: `"rev-1"`, Digest: serviceDigest(want.Body)}
	if !serviceReadMatches(good, want, "survivor") {
		t.Fatal("valid retained read refused")
	}
	for _, mutate := range []func(*serviceCommandResult){func(r *serviceCommandResult) { r.InstanceID = "replacement" }, func(r *serviceCommandResult) { r.Replayed = true }, func(r *serviceCommandResult) { r.Digest = serviceDigest([]byte(`{"id":"stale"}`)) }, func(r *serviceCommandResult) { r.Status = 503 }, func(r *serviceCommandResult) { r.Outcome = "timeout" }, func(r *serviceCommandResult) { r.ETag = `"rev-2"` }} {
		bad := good
		mutate(&bad)
		if serviceReadMatches(bad, want, "survivor") {
			t.Fatal("invalid read accepted")
		}
	}
}

func TestServiceEndpointReportPreservesDegradedReadiness(t *testing.T) {
	pods := []podIdentity{{Name: "selected", UID: "selected-id"}, {Name: "survivor", UID: "survivor-id"}}
	const base = `{"items":[{"metadata":{"namespace":"NAMESPACE","labels":{"kubernetes.io/service-name":"catalog"}},"endpoints":[{"conditions":{"ready":true},"targetRef":{"kind":"Pod","namespace":"NAMESPACE","name":"selected","uid":"selected-id"}},{"conditions":{"ready":true},"targetRef":{"kind":"Pod","namespace":"NAMESPACE","name":"survivor","uid":"survivor-id"}}]}]}`
	raw := strings.ReplaceAll(base, "NAMESPACE", namespace)
	before, err := serviceEndpointObservation([]byte(raw), pods, false, "before")
	if err != nil || before.ReadyBackends != 2 || !before.SelectedReady || !before.SurvivorReady || !before.SelectedPresent {
		t.Fatal(before, err)
	}
	degraded := strings.Replace(raw, `"ready":true`, `"ready":false`, 1)
	during, err := serviceEndpointObservation([]byte(degraded), pods, true, "degraded")
	if err != nil || during.ReadyBackends != 1 || during.SelectedReady || !during.SurvivorReady || during.Digest == before.Digest {
		t.Fatal(during, err)
	}
	if _, err := serviceEndpointObservation([]byte(raw), pods, true, "degraded"); err == nil {
		t.Fatal("ready selected backend accepted after removal")
	}
	if _, err := serviceEndpointObservation([]byte(strings.ReplaceAll(degraded, "survivor-id", "wrong")), pods, true, "degraded"); err == nil {
		t.Fatal("different survivor accepted")
	}
}
