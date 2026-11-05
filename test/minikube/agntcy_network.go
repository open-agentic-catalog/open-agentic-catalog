//go:build minikube

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// agntcyPolicies admits only local component traffic, DNS and explicit Catalog-to-gateway reads.
func (h *harness) agntcyPolicies() error {
	return h.interopPolicies("directory", agntcyNamespace, "directory-gateway", []int{5000, 5432, 5555, 8080, 8443, 8888, 8889})
}

func (h *harness) gatewayPolicies() error {
	return h.interopPolicies("gateway", gatewayNamespace, "gateway", []int{8443, 8888, 27017})
}

func (h *harness) interopPolicies(prefix, peerNamespace, component string, localPorts []int) error {
	namespacePeer := func(name string) map[string]any {
		return map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]string{"kubernetes.io/metadata.name": name}}}
	}
	dns := map[string]any{"to": []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]string{"kubernetes.io/metadata.name": "kube-system"}}, "podSelector": map[string]any{"matchLabels": map[string]string{"k8s-app": "kube-dns"}}}}, "ports": []any{map[string]any{"protocol": "UDP", "port": 53}, map[string]any{"protocol": "TCP", "port": 53}}}
	runtimeSelector := map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/instance": releaseName}, "matchExpressions": []any{map[string]any{"key": "app.kubernetes.io/component", "operator": "In", "values": []string{"api", "worker"}}}}
	catalogRuntime := namespacePeer(namespace)
	catalogRuntime["podSelector"] = runtimeSelector
	ports := make([]any, 0, 8)
	for _, port := range localPorts {
		ports = append(ports, map[string]any{"protocol": "TCP", "port": port})
	}
	objects := make([]kubernetesObject, 0, 5)
	objects = append(objects,
		kubernetesObject{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": map[string]any{"name": prefix + "-local-only", "namespace": peerNamespace}, "spec": map[string]any{"podSelector": map[string]any{}, "policyTypes": []string{"Ingress", "Egress"}, "ingress": []any{map[string]any{"from": []any{namespacePeer(peerNamespace)}, "ports": ports}}, "egress": []any{dns, map[string]any{"to": []any{namespacePeer(peerNamespace)}, "ports": ports}}}},
		kubernetesObject{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": map[string]any{"name": prefix + "-catalog-ingress", "namespace": peerNamespace}, "spec": map[string]any{"podSelector": map[string]any{"matchLabels": map[string]string{"component": component}}, "policyTypes": []string{"Ingress"}, "ingress": []any{map[string]any{"from": []any{catalogRuntime}, "ports": []any{map[string]any{"protocol": "TCP", "port": 8443}}}}}},
		kubernetesObject{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": map[string]any{"name": prefix + "-gateway-egress", "namespace": namespace}, "spec": map[string]any{"podSelector": runtimeSelector, "policyTypes": []string{"Egress"}, "egress": []any{map[string]any{"to": []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]string{"kubernetes.io/metadata.name": peerNamespace}}, "podSelector": map[string]any{"matchLabels": map[string]string{"component": component}}}}, "ports": []any{map[string]any{"protocol": "TCP", "port": 8443}}}}}},
	)
	for _, selection := range []struct {
		name     string
		selector map[string]any
		port     int
	}{
		{"postgres", map[string]any{"matchLabels": testLabels("postgres")}, 5432},
		{"worker", map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/instance": releaseName, "app.kubernetes.io/component": "worker"}}, 9090},
	} {
		objects = append(objects, kubernetesObject{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": map[string]any{"name": prefix + "-private-" + selection.name, "namespace": namespace}, "spec": map[string]any{"podSelector": selection.selector, "policyTypes": []string{"Ingress"}, "ingress": []any{map[string]any{"from": []any{namespacePeer(namespace)}, "ports": []any{map[string]any{"protocol": "TCP", "port": selection.port}}}}}})
	}
	if err := h.applyNetworkPolicies(); err != nil {
		return err
	}
	for _, object := range objects {
		if err := h.applyObject("apply selected "+prefix+" network policy", object); err != nil {
			return err
		}
	}
	return nil
}

// verifyAGNTCYHTTP compares original bytes and validates private TLS without relaxing trust.
func (h *harness) verifyAGNTCYHTTP() error {
	native, err := h.startPortForwardIn(agntcyNamespace, "service/directory-apiserver", 8889)
	if err != nil {
		return err
	}
	gateway, err := h.startPortForwardIn(agntcyNamespace, "service/directory-gateway", 8443)
	if err != nil {
		return err
	}
	client, err := fixtureHTTPClient([]byte(h.agntcy.tls["ca.pem"]))
	if err != nil {
		return err
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		return errors.New("fixture TLS transport unavailable")
	}
	transport.TLSClientConfig.ServerName = "directory-gateway." + agntcyNamespace + ".svc.cluster.local"
	for _, control := range []struct {
		method, path string
		status       int
		body         any
	}{
		{http.MethodGet, "/.well-known/ai-catalog.json", http.StatusOK, nil},
		{http.MethodGet, "/v1/agents", http.StatusOK, nil},
		{http.MethodPost, "/v1/explore", http.StatusNotFound, map[string]any{}},
	} {
		direct, err := h.request(control.method, "http://"+native+control.path, "", "", "", control.body, nil)
		if err != nil {
			return fmt.Errorf("native Directory control %s failed: %w", control.path, err)
		}
		if direct.Status != control.status {
			return fmt.Errorf("native Directory control %s returned %d; expected %d", control.path, direct.Status, control.status)
		}
		forwarded, err := h.request(control.method, "https://"+gateway+control.path, "", "", "", control.body, client)
		if err != nil || forwarded.Status != direct.Status || !bytes.Equal(direct.Body, forwarded.Body) || forwarded.Header.Get("Content-Type") != direct.Header.Get("Content-Type") {
			return fmt.Errorf("directory HTTPS changed native bytes for %s", control.path)
		}
		if control.status == http.StatusOK && !json.Valid(direct.Body) {
			return errors.New("directory control returned invalid JSON")
		}
		h.agntcy.report.Native[control.path] = direct.Status
	}
	// A system-root-only client must reject the owned issuer. Use the same expected hostname.
	untrusted := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: transport.TLSClientConfig.ServerName}}}
	_, trustErr := h.request(http.MethodGet, "https://"+gateway+"/.well-known/ai-catalog.json", "", "", "", nil, untrusted)
	var unknown x509.UnknownAuthorityError
	if !errors.As(trustErr, &unknown) {
		return fmt.Errorf("untrusted issuer control did not fail for certificate authority: %w", trustErr)
	}
	schema, err := h.startPortForwardIn(agntcyNamespace, "service/schema-router", 8080)
	if err != nil {
		return err
	}
	response, err := h.request(http.MethodGet, "http://"+schema+"/api/1.0.0/objects/record", "", "", "", nil, nil)
	if err != nil || response.Status != 200 || !json.Valid(response.Body) {
		return errors.New("namespace-local versioned OASF schema is unavailable")
	}
	wrong, err := h.request(http.MethodGet, "http://"+schema+"/api/9.9.9/objects/record", "", "", "", nil, nil)
	if err != nil || wrong.Status != 404 {
		return errors.New("schema router accepted an unselected version")
	}
	h.agntcy.report.Native["/api/1.0.0/objects/record"] = response.Status
	h.report.Checks = append(h.report.Checks, "directory-native-https-byte-preservation", "directory-trusted-and-untrusted-tls", "directory-versioned-schema-control")
	return nil
}

func (h *harness) interopPod(peerNamespace, selector string) (string, error) {
	raw, err := h.kubectl("select owned peer Pod", "-n", peerNamespace, "get", "pods", "-l", selector, "-o", "jsonpath={.items[*].metadata.name}")
	if err != nil {
		return "", err
	}
	var names []string
	for _, name := range bytes.Fields(raw) {
		names = append(names, string(name))
	}
	if len(names) != 1 {
		return "", errors.New("peer probe Pod selection is ambiguous")
	}
	return names[0], nil
}

func (h *harness) interopAddress(peerNamespace, pod, port string) (string, error) {
	raw, err := h.kubectl("read owned peer probe address", "-n", peerNamespace, "get", "pod", pod, "-o", "jsonpath={.status.podIP}")
	if err != nil {
		return "", err
	}
	if net.ParseIP(string(raw)) == nil {
		return "", errors.New("invalid peer Pod IP")
	}
	return net.JoinHostPort(string(raw), port), nil
}

type agntcyProbe struct{ namespace, pod, container string }

// agntcyPolicyTCP uses the actual selected workload network and healthy controls around each sample.
func (h *harness) agntcyPolicyTCP(name, address string, allowed bool, selected, control agntcyProbe) error {
	ctx, cancel := context.WithTimeout(h.ctx, 45*time.Second)
	defer cancel()
	probe := func(peer agntcyProbe) (bool, error) {
		args := []string{"--kubeconfig", h.kubeconfig, "--context", h.profile, "-n", peer.namespace, "exec", peer.pod}
		if peer.container != "" {
			args = append(args, "-c", peer.container)
		}
		args = append(args, "--", "/federation-fixture", "network-probe", "tcp", address)
		raw, err := h.runWithInput(ctx, "observe "+name, nil, h.options.kubectl, args...)
		return policyProbeResult(raw, err)
	}
	consecutive := 0
	err := waitTopology(ctx, func() (bool, error) {
		before, err := probe(control)
		if err != nil || !before {
			return false, errors.New("directory policy destination lacks a healthy allowed control")
		}
		observed, err := probe(selected)
		if err != nil {
			return false, err
		}
		after, err := probe(control)
		if err != nil || !after {
			return false, errors.New("directory policy destination lost its allowed control")
		}
		if observed == allowed {
			consecutive++
		} else {
			consecutive = 0
		}
		return consecutive >= 3, nil
	})
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	h.report.Checks = append(h.report.Checks, name)
	return nil
}

func (h *harness) verifyAGNTCYIsolation() error {
	return h.verifyInteropIsolation("directory", agntcyNamespace, "schema-router", "directory-gateway")
}

func (h *harness) verifyGatewayIsolation() error {
	return h.verifyInteropIsolation("gateway", gatewayNamespace, "gateway", "gateway")
}

func (h *harness) verifyInteropIsolation(prefix, peerNamespace, probeComponent, gatewayComponent string) error {
	peer, err := h.interopPod(peerNamespace, "component="+probeComponent)
	if err != nil {
		return err
	}
	if err := h.attachNetworkProbe(peerNamespace, peer); err != nil {
		return err
	}
	peerProbe := agntcyProbe{peerNamespace, peer, "network-probe"}
	identity, err := h.policyPeer("identity")
	if err != nil {
		return err
	}
	control := agntcyProbe{namespace, identity, ""}
	decoy, err := h.policyPeer("decoy")
	if err != nil {
		return err
	}
	address, err := h.policyAddress(decoy, "8443")
	if err != nil {
		return err
	}
	if err := h.agntcyPolicyTCP(prefix+"-unrelated-egress-denied", address, false, peerProbe, control); err != nil {
		return err
	}
	address, err = h.policyAddress("postgres-0", "5432")
	if err != nil {
		return err
	}
	if err := h.agntcyPolicyTCP(prefix+"-catalog-database-denied", address, false, peerProbe, control); err != nil {
		return err
	}
	for _, role := range []string{"api", "worker"} {
		pods, err := h.pods(role)
		if err != nil || len(pods) != 1 {
			return errors.New("directory policy requires one ready Pod per Catalog role")
		}
		if role == "worker" {
			address, err = h.policyAddress(pods[0].Name, "9090")
			if err != nil {
				return err
			}
			if err := h.agntcyPolicyTCP(prefix+"-worker-operations-denied", address, false, peerProbe, control); err != nil {
				return err
			}
		}
		if err := h.attachNetworkProbe(namespace, pods[0].Name); err != nil {
			return err
		}
		gateway, err := h.interopPod(peerNamespace, "component="+gatewayComponent)
		if err != nil {
			return err
		}
		address, err = h.interopAddress(peerNamespace, gateway, "8443")
		if err != nil {
			return err
		}
		selected := agntcyProbe{namespace, pods[0].Name, "network-probe"}
		if err := h.agntcyPolicyTCP(prefix+"-"+role+"-gateway-allowed", address, true, selected, selected); err != nil {
			return err
		}
		address, err = h.policyAddress(decoy, "8443")
		if err != nil {
			return err
		}
		if err := h.agntcyPolicyTCP(prefix+"-"+role+"-unrelated-egress-denied", address, false, selected, control); err != nil {
			return err
		}
	}
	return nil
}
