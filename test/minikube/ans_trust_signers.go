//go:build minikube

package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"github.com/fxamacker/cbor/v2"
)

func ansSignerTrustConfiguration(phase string) map[string]any {
	logs := []string{"tl.pem"}
	if phase == "tl-signer" || phase == "signers-restored" {
		logs = append(logs, "tl-next.pem")
	} else if phase == "tl-key-removal" {
		logs = []string{"tl-next.pem"}
	}
	cfg := ansTrustConfiguration(true, logs, "1h")
	keys := []any{map[string]string{"id": "ans-ra-signer-next", "file": "/etc/open-agentic-catalog/trust-verification/ra-next.pem"}}
	if phase != "ra-key-removal" {
		keys = append(keys, map[string]string{"id": "ans-ra-signer", "file": "/etc/open-agentic-catalog/trust-verification/ra.pem"})
	}
	// The fixture constructs this exact single-policy object above.
	policies, _ := cfg["policies"].([]any)
	policy, _ := policies[0].(map[string]any)
	policy["registrationKeys"] = keys
	return cfg
}

func ansNativeSignerConfiguration(component string, secrets []string, publicKey, id string) (map[string]any, error) {
	if component == "ra" && id == "ans-ra-signer-next" {
		cfg := ansConfiguration(component, secrets, publicKey)
		cfg["signer"] = map[string]string{"keyId": id, "raId": "ans-ra-local"}
		client, ok := cfg["tl-client"].(map[string]any)
		if !ok {
			return nil, errors.New("missing native RA client")
		}
		client["public-base-url"] = ansOrigin
		return cfg, nil
	}
	if component == "tl" && id == "ans-tl-attestation-next" {
		cfg := ansConfiguration(component, secrets, publicKey)
		cfg["attestation"] = map[string]string{"keyId": id}
		return cfg, nil
	}
	return nil, errors.New("unselected native signer change")
}

// Native startup generates a new file key. The same PVC, SQLite and tiles stay
// mounted; only this invocation's selected component is drained and restarted.
func (h *harness) rotateANSSigner(settings map[string]any, component, id, file string) error {
	cfg, err := ansNativeSignerConfiguration(component, h.ans.secrets, h.ans.publicKeys["ra.pem"], id)
	if err != nil {
		return err
	}
	if err := h.scaleANS(component, 0); err != nil {
		return err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := h.applyObject("select native ANS signing key", kubernetesObject{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": component + "-config", "namespace": ansNamespace}, "stringData": map[string]string{"config.yaml": string(raw)}}); err != nil {
		return err
	}
	if err := h.scaleANS(component, 1); err != nil {
		return err
	}
	address, err := h.startPortForwardIn(ansNamespace, "service/"+component, 8080)
	if err != nil {
		return err
	}
	settings[component] = "http://" + address
	raw, err = h.kubectl("export rotated native public key", "-n", ansNamespace, "exec", "deployment/"+component, "-c", "public-key", "--", "/federation-fixture", "ans-public-key", id)
	if err != nil {
		return err
	}
	output, ok := settings["trust_output"].(string)
	if !ok {
		return errors.New("missing signer evidence directory")
	}
	if err := os.WriteFile(filepath.Join(output, file), raw, 0o600); err != nil {
		return err
	}
	h.ans.publicKeys[file] = string(raw)
	if err := h.applyObject("add rotated public authority key", kubernetesObject{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "ans-public-keys", "namespace": namespace}, "data": h.ans.publicKeys}); err != nil {
		return err
	}
	return h.ansImageInventory()
}

// These independent test checks authenticate the actual checkpoint/status
// signatures with only the new TL key. Short hints alone cannot prove rotation.
func verifyANSSignerPhase(raw, public []byte) (map[string]string, error) {
	var report struct {
		Evidence map[string]struct {
			Checkpoint []byte `json:"checkpoint"`
			Status     []byte `json:"statusToken"`
		} `json:"signer_evidence"`
	}
	if json.Unmarshal(raw, &report) != nil || len(report.Evidence) != 2 {
		return nil, errors.New("missing native signer evidence")
	}
	proofs := map[string]string{}
	for _, name := range []string{"old", "new"} {
		evidence, ok := report.Evidence[name]
		if !ok {
			return nil, errors.New("missing old or new native signer evidence")
		}
		fingerprint, err := verifyANSCheckpointAndStatus(evidence.Checkpoint, evidence.Status, public)
		if err != nil {
			return nil, err
		}
		proofs[name] = fingerprint
	}
	return proofs, nil
}

func verifyANSCheckpointAndStatus(checkpoint, status, public []byte) (string, error) {
	invalid := errors.New("native checkpoint/status does not use the new TL signer")
	block, rest := pem.Decode(public)
	if block == nil || block.Type != "PUBLIC KEY" || len(rest) != 0 || len(public) > 4096 {
		return "", invalid
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	key, ok := parsed.(*ecdsa.PublicKey)
	if err != nil || !ok || key.Curve != elliptic.P256() {
		return "", invalid
	}
	fingerprint := sha256.Sum256(block.Bytes)
	body, signatures, found := strings.Cut(string(checkpoint), "\n\n")
	if !found || len(checkpoint) > 16<<10 || !strings.HasPrefix(body, "owned-ans\n") {
		return "", invalid
	}
	bodyHash := sha256.Sum256([]byte(body + "\n"))
	verified := false
	for _, line := range strings.Split(signatures, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != "—" || fields[1] != "owned-ans" {
			continue
		}
		signature, err := base64.StdEncoding.Strict().DecodeString(fields[2])
		if err == nil && len(signature) > 4 && bytes.Equal(signature[:4], fingerprint[:4]) && ecdsa.VerifyASN1(key, bodyHash[:], signature[4:]) {
			verified = true
		}
	}
	if !verified || len(status) > 64<<10 {
		return "", invalid
	}
	var tag cbor.RawTag
	var parts []cbor.RawMessage
	var protected, payload, signature []byte
	if cbor.Unmarshal(status, &tag) != nil || tag.Number != 18 || cbor.Unmarshal(tag.Content, &parts) != nil || len(parts) != 4 ||
		cbor.Unmarshal(parts[0], &protected) != nil || cbor.Unmarshal(parts[2], &payload) != nil || cbor.Unmarshal(parts[3], &signature) != nil || len(signature) != 64 {
		return "", invalid
	}
	structure, err := cbor.Marshal([]any{"Signature1", protected, []byte{}, payload})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(structure)
	if !ecdsa.Verify(key, hash[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		return "", invalid
	}
	return fmt.Sprintf("%x", fingerprint), nil
}
