package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestServiceProbeRejectsInputsWithoutNetwork(t *testing.T) {
	for _, input := range []string{`{}`, `{"address":"127.0.0.1:8080","token":"secret","requests":1}`, `{"address":"10.96.0.1:80","token":"secret","requests":1}`, `{"address":"catalog:8080","token":"secret","requests":1}`, `{"address":"10.96.0.1:8080","token":"","requests":1}`, `{"address":"10.96.0.1:8080","token":"secret","requests":49}`, `{"address":"10.96.0.1:8080","token":"secret","requests":1,"proxy":"secret"}`, `{"address":"10.96.0.1:8080","token":"secret","requests":1} {}`} {
		var output bytes.Buffer
		err := serviceProbe(strings.NewReader(input), &output)
		if err == nil || strings.Contains(err.Error(), "secret") || output.Len() != 0 {
			t.Fatalf("invalid input disclosed or accepted: %v", err)
		}
	}
}

func TestServiceProbeProjectsOnlyBoundedProcessIdentity(t *testing.T) {
	const good = `{"service":"open-agentic-catalog","role":"api","instanceId":"01234567-89ab-cdef-0123-456789abcdef","private":"sentinel"}`
	for _, tc := range []struct {
		name, body string
		status     int
		want       string
	}{
		{"valid", good, 200, "ok"}, {"status", good, 503, "invalid_response"},
		{"empty", `{}`, 200, "invalid_response"}, {"malformed", `{`, 200, "invalid_response"},
		{"trailing", good + `{}`, 200, "invalid_response"},
		{"worker", strings.Replace(good, `"api"`, `"worker"`, 1), 200, "invalid_response"},
		{"wrong-service", strings.Replace(good, "open-agentic-catalog", "other", 1), 200, "invalid_response"},
		{"unbounded-id", strings.Replace(good, "01234567-89ab-cdef-0123-456789abcdef", "private-sentinel", 1), 200, "invalid_response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := serviceInformationOutcome(tc.status, strings.NewReader(tc.body))
			if got.Outcome != tc.want || got.Outcome != "ok" && got.InstanceID != "" {
				t.Fatalf("unexpected result: %+v", got)
			}
		})
	}
}
