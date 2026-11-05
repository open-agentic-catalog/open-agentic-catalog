//go:build minikube

package main

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
)

// diagnoseFederationRoute samples only fixed network outcomes from the owned
// APIs after a failed workflow. It never reads tokens or private responses.
func (h *harness) diagnoseFederationRoute() {
	pods, err := h.pods("api")
	if err != nil || len(pods) != 2 {
		return
	}
	peer, err := h.policyPeer("identity")
	if err != nil {
		return
	}
	address, err := h.policyAddress(peer, "8443")
	if err != nil {
		return
	}
	serviceRaw, serviceErr := h.kubectl("identify owned federation Service address", "-n", namespace, "get", "service/federation-fixture", "-o", "jsonpath={.spec.clusterIP}")
	serviceIP, parseErr := netip.ParseAddr(strings.TrimSpace(string(serviceRaw)))
	for _, pod := range pods {
		if h.attachNetworkProbe(namespace, pod.Name) != nil {
			continue
		}
		for _, probe := range []struct{ mode, destination string }{
			{"dns-udp", "federation-fixture." + namespace + ".svc.cluster.local"},
			{"dns-tcp", "federation-fixture." + namespace + ".svc.cluster.local"},
			{"tcp", address},
		} {
			raw, err := h.kubectl("diagnose API federation "+probe.mode, "-n", namespace, "exec", pod.Name, "-c", "network-probe", "--", "/federation-fixture", "network-probe", probe.mode, probe.destination)
			outcome := "probe_error"
			if err == nil && (strings.TrimSpace(string(raw)) == "connected" || strings.TrimSpace(string(raw)) == "timeout") {
				outcome = strings.TrimSpace(string(raw))
			}
			fmt.Printf("[topology] federation network diagnostic: pod=%s mode=%s outcome=%s\n", pod.Name, probe.mode, outcome)
		}
		h.diagnoseFederationTLS(pod.Name, address, "pod")
		if serviceErr == nil && parseErr == nil {
			h.diagnoseFederationTLS(pod.Name, netip.AddrPortFrom(serviceIP, 8443).String(), "service")
		}
	}
}

func (h *harness) diagnoseFederationTLS(pod, address, target string) {
	ca, err := os.ReadFile(filepath.Join(h.temporary, "fixture", "trust", "functional-ca.pem")) // #nosec G304 -- Owned generated public CA.
	if err != nil {
		return
	}
	for _, protocol := range []string{"h2", "http1"} {
		raw, err := h.topologyCommand(h.ctx, "diagnose owned fixture TLS stage", ca, "-n", namespace, "exec", "-i", pod, "-c", "network-probe", "--", "/federation-fixture", "tls-probe", protocol, address)
		var result struct {
			Stage    string `json:"stage"`
			Outcome  string `json:"outcome"`
			Protocol string `json:"protocol"`
		}
		if err != nil || json.Unmarshal(raw, &result) != nil {
			continue
		}
		validStage := result.Stage == "tcp" || result.Stage == "tls" || result.Stage == "headers" || result.Stage == "body" || result.Stage == "complete"
		validOutcome := result.Outcome == "connected" || result.Outcome == "timeout" || result.Outcome == "error"
		if validStage && validOutcome {
			fmt.Printf("[topology] federation TLS diagnostic: pod=%s target=%s mode=%s stage=%s outcome=%s\n", pod, target, protocol, result.Stage, result.Outcome)
		}
	}
}

// Reuse the Python runner's closed, bounded diagnostic schema before cleanup.
func (h *harness) captureFederationCheckpoint() {
	script := "import json,sys; sys.path.insert(0,'test/functional'); from runner import read_job_wait_failure; value=read_job_wait_failure(sys.argv[1]); print(json.dumps(value) if value else '')"
	raw, err := h.run("capture bounded federation retry fields", "python3", "-c", script, h.temporary)
	if err == nil && len(raw) < 2048 && json.Valid(raw) {
		fmt.Printf("[topology] retained retry diagnostic: %s\n", strings.TrimSpace(string(raw)))
	}
}
