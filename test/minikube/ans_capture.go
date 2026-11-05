//go:build minikube

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// captureANSTrust reuses the owned native deployment, without Catalog assessment or enrollment.
func (h *harness) captureANSTrust() error {
	if err := h.deployANS(); err != nil {
		return err
	}
	if err := h.ansImageInventory(); err != nil {
		return err
	}
	output := filepath.Join(h.options.repositoryRoot, "output/interoperability/minikube-ans-"+h.suffix, "trust-fixtures")
	if err := os.MkdirAll(output, 0o700); err != nil {
		return err
	}
	for component, key := range map[string]string{"ra": "ans-ra-signer", "tl": "ans-tl-attestation"} {
		raw, err := h.kubectl("export owned ANS public key", "-n", ansNamespace, "exec", "deployment/"+component, "-c", "public-key", "--", "/federation-fixture", "ans-public-key", key)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(output, component+".pem"), raw, 0o600); err != nil {
			return err
		}
	}
	settings := map[string]any{"origin": ansOrigin, "tl_origin": ansTLOrigin, "key": h.ans.secrets[0], "output": output, "inputs": h.ans.report.Inputs}
	for _, component := range []string{"ra", "tl", "finder", "finder-edge"} {
		port, scheme := 8080, "http://"
		if component == "finder-edge" {
			port, scheme = 8443, "https://"
		}
		address, err := h.startPortForwardIn(ansNamespace, "service/"+component, port)
		if err != nil {
			return err
		}
		settings[component] = scheme + address
	}
	ca := filepath.Join(h.temporary, "ans-capture-ca.pem")
	if err := os.WriteFile(ca, []byte(h.ans.tls["ca.pem"]), 0o600); err != nil {
		return err
	}
	settings["ca"] = ca
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	path := filepath.Join(h.temporary, "ans-capture.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(h.ctx, 5*time.Minute)
	defer cancel()
	raw, err = h.runWithInput(ctx, "capture authentic ANS V1/V2 evidence", nil, h.ansPython(), "-B", "test/interoperability/ans/capture.py", "--settings", path)
	if err != nil {
		return err
	}
	if json.Unmarshal(raw, &h.ans.report.Capture) != nil || h.ans.report.Capture["status"] != "captured" {
		return errors.New("incomplete ANS fixture capture")
	}
	h.report.Checks = append(h.report.Checks, "authentic-ans-v1-v2-public-evidence-captured")
	return h.interopProgress("captured", nil, nil)
}
