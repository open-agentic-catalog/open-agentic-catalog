//go:build minikube

package main

import (
	"strings"
	"testing"
)

func TestFailedMigrationEvidenceRequiresOwnedFailedJobAndPod(t *testing.T) {
	job := `{"metadata":{"name":"catalog-migrate","uid":"owned-job"},"spec":{"template":{"spec":{"serviceAccountName":"catalog-migration"}}},"status":{"failed":1}}`
	pods := `{"items":[{"metadata":{"ownerReferences":[{"name":"catalog-migrate","kind":"Job","uid":"owned-job","controller":true}]},"spec":{"serviceAccountName":"catalog-migration"},"status":{"phase":"Failed","containerStatuses":[{"name":"migrate","state":{"terminated":{"exitCode":1}}}]}}]}`
	if err := verifyFailedMigrationResources([]byte(job), []byte(pods)); err != nil {
		t.Fatalf("valid retained migration failure rejected: %v", err)
	}
	for _, test := range []struct {
		name, job, pods, want string
	}{
		{"malformed Job", `{`, pods, "Job identity"},
		{"missing Job UID", strings.Replace(job, `"uid":"owned-job"`, `"uid":""`, 1), pods, "Job identity"},
		{"wrong Job", strings.Replace(job, "catalog-migrate", "unrelated-job", 1), pods, "Job identity"},
		{"wrong Job account", strings.Replace(job, "catalog-migration", "default", 1), pods, "Job identity"},
		{"no failed Job", strings.Replace(job, `"failed":1`, `"failed":0`, 1), pods, "Job identity"},
		{"multiple failed attempts", strings.Replace(job, `"failed":1`, `"failed":2`, 1), pods, "Job identity"},
		{"malformed Pod list", job, `{`, "exactly one Pod"},
		{"missing failed Pod", job, `{"items":[]}`, "exactly one Pod"},
		{"wrong Pod account", job, strings.Replace(pods, "catalog-migration", "default", 1), "bound to its Job"},
		{"wrong Pod owner", job, strings.Replace(pods, "owned-job", "different-job", 1), "bound to its Job"},
		{"wrong owner kind", job, strings.Replace(pods, `"kind":"Job"`, `"kind":"ReplicaSet"`, 1), "bound to its Job"},
		{"owner is not controller", job, strings.Replace(pods, `"controller":true`, `"controller":false`, 1), "bound to its Job"},
		{"successful Pod", job, strings.Replace(pods, `"phase":"Failed"`, `"phase":"Succeeded"`, 1), "bound to its Job"},
		{"wrong container", job, strings.Replace(pods, `"name":"migrate"`, `"name":"unrelated"`, 1), "command refusal"},
		{"successful container", job, strings.Replace(pods, `"exitCode":1`, `"exitCode":0`, 1), "command refusal"},
		{"killed container", job, strings.Replace(pods, `"exitCode":1`, `"exitCode":137`, 1), "command refusal"},
		{"no termination evidence", job, strings.Replace(pods, `"terminated":{"exitCode":1}`, `"waiting":{}`, 1), "command refusal"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := verifyFailedMigrationResources([]byte(test.job), []byte(test.pods)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid migration failure evidence returned %v", err)
			}
		})
	}
}
