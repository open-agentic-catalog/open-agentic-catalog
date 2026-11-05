//go:build minikube

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNativeSignerChangePreservesStateAndFixedDestinations(t *testing.T) {
	for _, component := range []string{"ra", "tl"} {
		id := "ans-ra-signer-next"
		if component == "tl" {
			id = "ans-tl-attestation-next"
		}
		cfg, err := ansNativeSignerConfiguration(component, []string{"ra-private", "tl-private"}, "old-public", id)
		if err != nil {
			t.Fatal(err)
		}
		base := ansConfiguration(component, []string{"ra-private", "tl-private"}, "old-public")
		for _, key := range []string{"store", "keys", "merkle", "producerKeys", "ca", "auth"} {
			if !reflect.DeepEqual(base[key], cfg[key]) {
				t.Fatal("signer change altered retained state or authority", component, key)
			}
		}
		for _, wrong := range []string{"", "../key", "ans-ra-signer", "ans-tl-attestation"} {
			if _, err := ansNativeSignerConfiguration(component, nil, "", wrong); err == nil {
				t.Fatal("unselected signer accepted", component, wrong)
			}
		}
	}
}

func TestSignerPolicyAndOfflinePolicyUseExactlySelectedKeys(t *testing.T) {
	for _, phase := range []string{"ra-signer", "ra-key-removal", "ra-restored", "tl-signer", "tl-key-removal", "signers-restored"} {
		cfg := ansSignerTrustConfiguration(phase)
		policy, err := ansOfflinePolicy(cfg)
		if err != nil {
			t.Fatal(err)
		}
		keys, ok := policy["registrationKeys"].([]map[string]string)
		if !ok || len(keys) != map[bool]int{false: 2, true: 1}[phase == "ra-key-removal"] || keys[0]["id"] != "ans-ra-signer-next" || keys[0]["file"] != "ra-next.pem" {
			t.Fatal("registration authority mismatch", phase, policy)
		}
		logs := []string{"tl.pem"}
		if phase == "tl-signer" || phase == "signers-restored" {
			logs = append(logs, "tl-next.pem")
		} else if phase == "tl-key-removal" {
			logs = []string{"tl-next.pem"}
		}
		if !reflect.DeepEqual(policy["logKeyFiles"], logs) || policy["maxStatusAgeSeconds"] != 3600 || policy["futureSkewSeconds"] != 60 || policy["evidenceOrigin"] != ansOrigin {
			t.Fatal("offline authority mismatch", phase, policy)
		}
	}
	if _, err := ansOfflinePolicy(nil); err == nil {
		t.Fatal("missing policy accepted")
	}
}

func TestNativeCheckpointAndStatusSignerRequiresRealSignatures(t *testing.T) {
	directory := filepath.Join("..", "..", "internal", "standards", "anstrust", "testdata", "native")
	read := func(name string) []byte {
		t.Helper()
		raw, err := os.ReadFile(filepath.Clean(filepath.Join(directory, name)))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	var bundle struct {
		Checkpoint []byte `json:"checkpoint"`
		Status     []byte `json:"statusToken"`
	}
	if err := json.Unmarshal(read("v1.json"), &bundle); err != nil {
		t.Fatal(err)
	}
	key := read("tl.pem")
	fingerprint, err := verifyANSCheckpointAndStatus(bundle.Checkpoint, bundle.Status, key)
	if err != nil || len(fingerprint) != 64 {
		t.Fatal(fingerprint, err)
	}
	badStatus := bytes.Clone(bundle.Status)
	badStatus[len(badStatus)-1] ^= 1
	for _, control := range []struct{ checkpoint, status, key []byte }{
		{bundle.Checkpoint, bundle.Status, read("ra.pem")},
		{bytes.Replace(bundle.Checkpoint, []byte("owned-ans"), []byte("wrong-log"), 1), bundle.Status, key},
		{bundle.Checkpoint, badStatus, key}, {nil, bundle.Status, key}, {bundle.Checkpoint, []byte("not CBOR"), key}, {bundle.Checkpoint, bundle.Status, nil},
	} {
		if _, err := verifyANSCheckpointAndStatus(control.checkpoint, control.status, control.key); err == nil {
			t.Fatal("unauthenticated signer accepted")
		}
	}
	evidence := map[string]any{"checkpoint": bundle.Checkpoint, "statusToken": bundle.Status}
	report := map[string]any{"signer_evidence": map[string]any{"old": evidence, "new": evidence}}
	encode := func() []byte {
		raw, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if proofs, err := verifyANSSignerPhase(encode(), key); err != nil || proofs["old"] != fingerprint || proofs["new"] != fingerprint {
		t.Fatal("complete signer proof rejected", proofs, err)
	}
	report["signer_evidence"] = map[string]any{"old": evidence, "unexpected": evidence}
	if _, err := verifyANSSignerPhase(encode(), key); err == nil {
		t.Fatal("missing new signer proof accepted")
	}
	if _, err := verifyANSSignerPhase([]byte(strings.Repeat("!", 4)), key); err == nil {
		t.Fatal("malformed proof accepted")
	}
}
