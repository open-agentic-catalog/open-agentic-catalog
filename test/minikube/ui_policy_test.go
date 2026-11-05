//go:build minikube

package main

import (
	"errors"
	"testing"
)

func TestPolicyProbeRejectsFalseDenials(t *testing.T) {
	for _, test := range []struct {
		name, raw        string
		processErr       error
		connected, valid bool
	}{
		{"allowed", "connected\n", nil, true, true},
		{"dropped", "timeout\n", nil, false, true},
		{"exec failure", "timeout\n", errors.New("exec unavailable"), false, false},
		{"missing output", "", nil, false, false},
		{"refused listener", "refused\n", nil, false, false},
		{"ambiguous", "connected\ntimeout\n", nil, false, false},
		{"extra output", "warning\ntimeout\n", nil, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := policyProbeResult([]byte(test.raw), test.processErr)
			if (err == nil) != test.valid || got != test.connected {
				t.Fatalf("unexpected probe disposition: %v, %v", got, err)
			}
		})
	}
}

func TestPolicyInventoryRejectsAdditiveOrMissingEvidence(t *testing.T) {
	for _, test := range []struct {
		raw      string
		expected []string
		valid    bool
	}{
		{`{"items":[]}`, nil, true},
		{`{"items":[{"metadata":{"name":"ui"}}]}`, []string{"ui"}, true},
		{`{"items":[{"metadata":{"name":"ui"}},{"metadata":{"name":"allow-all"}}]}`, []string{"ui"}, false},
		{`{"items":[{"metadata":{"name":"ui"}},{"metadata":{"name":"ui"}}]}`, []string{"ui"}, false},
		{`{"items":[]}`, []string{"ui"}, false},
		{`{}`, nil, false}, {`null`, nil, false}, {`{`, nil, false},
	} {
		if err := policyInventory([]byte(test.raw), test.expected); (err == nil) != test.valid {
			t.Fatalf("inventory %s: %v", test.raw, err)
		}
	}
}
