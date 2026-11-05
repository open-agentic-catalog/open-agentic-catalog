//go:build minikube

package main

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

const catalogPeerNamespace = namespace + "-peer"

func (h *harness) catalogNamespace() string {
	if h.catalogScope != "" {
		return h.catalogScope
	}
	return namespace
}

func peerHost(side string) string   { return "peer-" + side + "." + namespace + ".svc.cluster.local" }
func peerOrigin(side string) string { return "https://" + peerHost(side) + ":8443" }

// configureCatalogPeerValues selects separate advertised discovery hosts without changing chart inputs.
func configureCatalogPeerValues(values map[string]any, scope string) error {
	side := "b"
	if scope == catalogPeerNamespace {
		side = "a"
	} else if scope != namespace {
		return errors.New("unowned Catalog peer namespace")
	}
	config, ok := values["configEnv"].(map[string]any)
	if !ok {
		return errors.New("missing Catalog peer configuration")
	}
	hosts, err := json.Marshal([]map[string]string{
		{"host": peerHost(side), "tenantId": primaryTenantID, "mode": "anonymous", "publisherDomain": "catalog.example.com"},
		{"host": "catalog." + scope + ".svc.cluster.local", "tenantId": primaryTenantID, "mode": "authenticated", "publisherDomain": "catalog.example.com"},
	})
	if err != nil {
		return err
	}
	config["CATALOG_PUBLIC_BASE_URL"] = peerOrigin(side)
	config["CATALOG_DISCOVERY_HOSTS"] = string(hosts)
	return nil
}

func (h *harness) prepareCatalogPeerTLS() error {
	material, err := serviceTLSMaterial(time.Now(), "owned-catalog-peers", namespace, []string{"peer-a", "peer-b"}, net.ParseIP("127.0.0.1"))
	if err == nil {
		h.peerTLS = material
	}
	return err
}

// secondaryCatalog shares only the owned cluster and images. It has independent credentials, issuer keys and database storage.
func (h *harness) secondaryCatalog() (*harness, error) {
	directory := filepath.Join(h.temporary, "catalog-peer")
	if err := os.Mkdir(directory, 0o700); err != nil {
		return nil, err
	}
	secrets := fixtureSecrets{}
	for _, value := range []*string{&secrets.ownerPassword, &secrets.apiPassword, &secrets.workerPassword} {
		generated, err := randomHex(24)
		if err != nil {
			return nil, err
		}
		*value = generated
	}
	key, err := randomBase64(32)
	if err != nil {
		return nil, err
	}
	secrets.cursorKey = key
	peer := &harness{options: h.options, ctx: h.ctx, temporary: directory, minikubeHome: h.minikubeHome, kubeconfig: h.kubeconfig, profile: h.profile,
		deploymentTool: filepath.Join(h.temporary, "catalog-deploy"), suffix: h.suffix, application: h.application, fixture: h.fixture, postgres: h.postgres, secrets: secrets, catalogScope: catalogPeerNamespace, peerTLS: h.peerTLS,
		report: runReport{Nodes: h.report.Nodes, Images: h.report.Images, Placements: make(map[string][]podPlacementReport)}}
	return peer, nil
}

func (h *harness) preparePeerFrontend(side string) error {
	name := "peer-" + side
	if err := h.createSecret("create private Catalog peer TLS", name+"-tls", map[string]string{"server.pem": h.peerTLS[name+".crt"], "server-key.pem": h.peerTLS[name+".key"]}); err != nil {
		return err
	}
	pod := h.fixtureDeployment(name, name, map[string]string{})
	spec := map[string]any(pod)
	for _, key := range []string{"spec", "template", "spec"} {
		child, ok := spec[key].(map[string]any)
		if !ok {
			return errors.New("invalid peer frontend Pod template")
		}
		spec = child
	}
	containers, ok := spec["containers"].([]any)
	if !ok || len(containers) != 1 {
		return errors.New("invalid peer frontend containers")
	}
	container, ok := containers[0].(map[string]any)
	if !ok {
		return errors.New("invalid peer frontend container")
	}
	container["args"] = []string{"serve", "--directory", "/fixture", "--peer", "--peer-kubernetes-namespace", namespace, "--peer-kubernetes-other-namespace", catalogPeerNamespace}
	for _, kind := range []string{"readinessProbe", "livenessProbe"} {
		container[kind] = map[string]any{"httpGet": map[string]any{"scheme": "HTTPS", "path": "/healthz", "port": 8443}, "periodSeconds": 2, "timeoutSeconds": 2, "failureThreshold": 30}
	}
	volumes, ok := spec["volumes"].([]any)
	if !ok {
		return errors.New("invalid peer frontend volumes")
	}
	for _, raw := range volumes {
		volume, ok := raw.(map[string]any)
		if !ok {
			return errors.New("invalid peer frontend volume")
		}
		if volume["name"] == "material" {
			volume["secret"] = map[string]any{"secretName": name + "-tls", "defaultMode": 292}
		}
	}
	if err := h.applyObject("create fixed-upstream Catalog TLS frontend", pod); err != nil {
		return err
	}
	if err := h.applyObject("create private Catalog discovery Service", kubernetesObject{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": name, "namespace": namespace, "labels": testLabels(name)}, "spec": map[string]any{"selector": testLabels(name), "ports": []any{map[string]any{"name": "https", "port": 8443, "targetPort": 8443}}}}); err != nil {
		return err
	}
	_, err := h.kubectl("wait for Catalog TLS frontend", "-n", namespace, "rollout", "status", "deployment/"+name, "--timeout=2m")
	return err
}

func (h *harness) verifyCatalogPeers(baseURL string) (resultErr error) {
	peer, err := h.secondaryCatalog()
	if err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			peer.diagnostics()
		}
		resultErr = errors.Join(resultErr, peer.stopPortForwards())
	}()
	if err := peer.prepareDependencies(); err != nil {
		return err
	}
	if err := peer.installCatalog(); err != nil {
		return err
	}
	for _, entry := range []struct{ name, tenant, organization, subject, roles string }{
		{"provision-primary", primaryTenantID, "tenant-one", "oidc-admin", "tenant_admin,contributor,reviewer,publisher,reader,source_admin,security_reviewer"},
		{"provision-other", otherTenantID, "tenant-two", "oidc-other-admin", "tenant_admin"},
	} {
		if err := peer.provisionIdentity(entry.name, entry.tenant, entry.organization, entry.subject, entry.roles); err != nil {
			return err
		}
	}
	if err := peer.issueTokens(); err != nil {
		return err
	}
	address, err := peer.startPortForward("service/catalog", 8080)
	if err != nil {
		return err
	}
	peerURL := "http://" + address
	if err := peer.waitHTTP(peerURL + "/readyz"); err != nil {
		return err
	}
	if err := peer.bootstrapSmokeIdentities(peerURL); err != nil {
		return err
	}
	if err := h.verifyServedContract(baseURL); err != nil {
		return err
	}
	if err := peer.verifyServedContract(peerURL); err != nil {
		return err
	}
	for _, side := range []string{"a", "b"} {
		if err := h.preparePeerFrontend(side); err != nil {
			return err
		}
	}
	frontend, err := h.startPortForward("service/peer-a", 8443)
	if err != nil {
		return err
	}
	validator := filepath.Join(h.temporary, "response-validator")
	if _, err := h.run("build owned response validator", "go", "build", "-mod=readonly", "-trimpath", "-o", validator, "./test/functional/contracts"); err != nil {
		return err
	}
	caPath := filepath.Join(h.temporary, "peer-ca.pem")
	aConfig := map[string]any{"base": peerURL, "tokens": peer.tokens, "host": peerHost("a"), "authenticatedHost": "catalog." + peer.catalogNamespace() + ".svc.cluster.local"}
	bConfig := map[string]any{"base": baseURL, "tokens": h.tokens, "host": peerHost("b"), "authenticatedHost": "catalog." + h.catalogNamespace() + ".svc.cluster.local"}
	config := map[string]any{"a": aConfig, "b": bConfig, "frontend": "https://" + frontend, "ca": caPath, "validator": validator, "state": filepath.Join(h.temporary, "catalog-peers-state.json")}
	if err := os.WriteFile(caPath, []byte(h.peerTLS["ca.pem"]), 0o600); err != nil {
		return err
	}
	input, err := json.Marshal(config)
	if err != nil {
		return err
	}
	run := func(phase string) error {
		raw, err := h.runWithInput(h.ctx, "qualify independent Catalogs: "+phase, input, "python3", "-E", "-B", "test/functional/minikube_peers.py", phase)
		if err != nil {
			return err
		}
		report, err := decodeCatalogPeerEvidence(raw, phase)
		if err != nil {
			return err
		}
		if h.report.Peers == nil {
			h.report.Peers = map[string]any{}
		}
		h.report.Peers[phase] = report
		return nil
	}
	if err := run("lifecycle"); err != nil {
		return err
	}
	for _, instance := range []*harness{h, peer} {
		if err := instance.recreateRuntimePods(); err != nil {
			return err
		}
	}
	address, err = h.startPortForward("service/catalog", 8080)
	if err != nil {
		return err
	}
	baseURL = "http://" + address
	peerAddress, err := peer.startPortForward("service/catalog", 8080)
	if err != nil {
		return err
	}
	peerURL = "http://" + peerAddress
	aConfig["base"] = peerURL
	bConfig["base"] = baseURL
	input, err = json.Marshal(config)
	if err != nil {
		return err
	}
	if err := run("replacement"); err != nil {
		return err
	}
	for _, role := range []string{"api", "worker"} {
		pods, err := peer.pods(role)
		if err != nil {
			return err
		}
		h.recordPlacement("independent-peer-"+role, pods)
	}
	// Bind the independent stores, not just two API listeners.
	var stores []string
	for _, instance := range []*harness{h, peer} {
		raw, err := instance.kubectl("verify independent PostgreSQL storage", "-n", instance.catalogNamespace(), "get", "pvc", "data-postgres-0", "-o", "jsonpath={.metadata.uid}")
		if err != nil {
			return err
		}
		stores = append(stores, strings.TrimSpace(string(raw)))
	}
	if stores[0] == "" || stores[1] == "" || stores[0] == stores[1] {
		return errors.New("catalog peers do not have distinct PostgreSQL storage")
	}
	h.report.Peers["stores"] = stores
	h.report.Peers["namespaces"] = []string{namespace, catalogPeerNamespace}
	h.report.Checks = append(h.report.Checks, "independent-catalogs-separate-storage-credentials-and-oidc-keys", "independent-catalogs-publication-indexed-live-lifecycle-and-recovery", "independent-catalogs-process-replacement-retains-both-databases")
	return nil
}

type catalogPeerEvidence struct {
	Phase    string   `json:"phase"`
	Checks   []string `json:"checks"`
	Kinds    []string `json:"kinds"`
	Catalogs int      `json:"catalogs"`
}

func decodeCatalogPeerEvidence(raw []byte, phase string) (catalogPeerEvidence, error) {
	var value catalogPeerEvidence
	expected := []string{"three-kind-exact-bytes-and-provenance", "private-draft-tenant-and-issuer-denials", "no-implicit-adoption", "one-item-pagination-and-unchanged-sync", "live-query-and-preserved-partial-results", "failed-sync-preserves-complete-snapshot", "same-job-recovery", "update-and-authoritative-withdrawal", "reciprocal-federation-without-reexport"}
	if phase == "replacement" {
		expected = []string{"both-catalogs-retain-native-and-synchronized-state-after-replacement"}
	} else if phase != "lifecycle" {
		return value, errors.New("unknown Catalog peer phase")
	}
	if jsondoc.Decode(raw, &value) != nil || value.Phase != phase || value.Catalogs != 2 || !slices.Equal(value.Kinds, []string{"agent", "mcp_server", "skill"}) || !slices.Equal(value.Checks, expected) {
		return value, errors.New("incomplete independent-Catalog evidence")
	}
	return value, nil
}
