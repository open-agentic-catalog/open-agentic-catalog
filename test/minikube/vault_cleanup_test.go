//go:build minikube

package main

import (
	"context"
	"errors"
	"testing"
)

func TestVaultCleanupFaultSelection(t *testing.T) {
	for _, tc := range []struct {
		scenario, mode string
		valid          bool
	}{{"vault", "", true}, {"basic", "", true}, {"vault", "failure", true}, {"vault", "interrupt", true}, {"basic", "failure", false}, {"vault", "success", false}} {
		if err := validateVaultCleanupTest(tc.scenario, tc.mode); (err == nil) != tc.valid {
			t.Fatal("invalid cleanup test selection", tc, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		mode     string
		reached  bool
		err      error
		expected bool
	}{{"failure", true, errVaultCleanupFault, true}, {"interrupt", true, context.Canceled, true}, {"interrupt", true, context.DeadlineExceeded, false}, {"failure", false, errVaultCleanupFault, false}, {"failure", true, errors.New("unexpected failure"), false}, {"", true, errVaultCleanupFault, false}, {"interrupt", true, nil, false}} {
		h := &harness{ctx: ctx, options: options{vaultCleanupTest: tc.mode}, vault: &vaultState{cleanupFaultReached: tc.reached}}
		if h.expectedVaultCleanupFault(tc.err) != tc.expected {
			t.Fatal("misclassified an unexpected failure as cleanup success", tc)
		}
	}
}
func TestVaultPublishedExampleScope(t *testing.T) {
	objects, err := vaultExampleObjects("selected-catalog", "https://selected-vault:8200")
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, object := range objects {
		kind, ok := object["kind"].(string)
		if !ok {
			t.Fatal("missing example kind")
		}
		counts[kind]++
		metadata, ok := object["metadata"].(map[string]any)
		if !ok || metadata["namespace"] != "selected-catalog" {
			t.Fatal("example escaped selected namespace")
		}
		if kind == "ServiceAccount" && object["automountServiceAccountToken"] != false {
			t.Fatal("sync account automount enabled")
		}
		if kind == "VaultConnection" {
			spec, ok := object["spec"].(map[string]any)
			if !ok || spec["address"] != "https://selected-vault:8200" || spec["skipTLSVerify"] != false || spec["caCertSecretRef"] != "catalog-vault-ca" {
				t.Fatal("example weakened connection trust")
			}
		}
	}
	if counts["VaultConnection"] != 1 || counts["ServiceAccount"] != 2 || counts["VaultAuth"] != 2 || counts["VaultStaticSecret"] != 5 || len(counts) != 4 {
		t.Fatal("example expands provider scope", counts)
	}
}
