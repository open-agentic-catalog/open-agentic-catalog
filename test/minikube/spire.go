//go:build minikube

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"
)

const spireImporterAudience = "open-agentic-catalog-b"

// verifySPIRE runs only in the explicitly selected, disposable SPIRE scenario.
func (h *harness) verifySPIRE() error {
	h.spireImage = "open-agentic-catalog:minikube-spire-" + h.suffix
	phase := func(name string, extra ...string) error { return h.spirePhase(name, "", extra...) }
	if err := phase("build"); err != nil {
		return err
	}
	if _, err := h.minikube("load SPIRE fixture image", "image", "load", "--daemon=true", "--overwrite=true", h.spireImage); err != nil {
		return err
	}
	raw, err := h.run("record SPIRE fixture image config", h.options.docker, "image", "inspect", "--format", "{{.Id}}", h.spireImage)
	if err != nil {
		return err
	}
	h.report.Images["spire-fixture"] = strings.TrimSpace(string(raw))
	if err := phase("start"); err != nil {
		return err
	}
	address, err := h.startPortForwardIn("test", "service/catalog-a", 8443)
	if err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	// Bootstrap the same identity before switching issuer, so missing membership
	// cannot explain either of the refusals with the normal Helm values.
	if err := h.runIdentityJob("provision-spire-admin", []string{"/usr/local/bin/catalogctl", "identity", "provision",
		"--tenant", primaryTenantID, "--tenant-name", "SPIRE qualification", "--issuer", "https://identity.test:8447",
		"--subject", "spiffe://workloads.test/admin", "--actor-type", "service", "--roles", "tenant_admin,contributor,publisher,reader",
		"--operator", "minikube-harness", "--reason", "Qualify dedicated workload identity"}); err != nil {
		return err
	}
	if err := phase("unselected", "--port", port); err != nil {
		return err
	}
	base, err := h.writeHelmValues("initial", "catalog_api")
	if err != nil {
		return err
	}
	values, err := h.writeHelmValues("spire", "catalog_api")
	if err != nil {
		return err
	}
	if err := h.deployCatalog(values, "4m"); err != nil {
		return err
	}
	if err := phase("reads", "--port", port); err != nil {
		return err
	}
	if h.spireRenewal {
		if err := h.verifySPIRERenewal(port); err != nil {
			return err
		}
	}
	if err := h.deployCatalog(base, "4m"); err != nil {
		return err
	}
	if err := phase("unselected", "--port", port); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "spire-issuer-opt-in-and-opt-out", "spire-sdk-private-resource-and-authenticated-publication", "spire-namespace-audience-and-current-grants")
	if h.spireLab {
		if err := h.deployCatalog(values, "4m"); err != nil {
			return err
		}
		if err := phase("resume", "--port", port); err != nil {
			return err
		}
		fmt.Printf("SPIRE lab ready. Profile %s, kubeconfig %s\n", h.profile, h.kubeconfig)
		fmt.Printf("curl --cacert %q --connect-to catalog-a.test:8443:%s https://catalog-a.test:8443/readyz\n", filepath.Join(h.temporary, "spire/trust/ca.pem"), address)
		fmt.Printf("Stop and clean up this lab with Ctrl+C or kill -TERM %d\n", os.Getpid())
	}
	return nil
}

// configureSPIREValues merges the shipped issuer example into the generated Helm values.
func (h *harness) configureSPIREValues(values map[string]any) error {
	raw, err := os.ReadFile(filepath.Join(h.options.repositoryRoot, "deployments/helm/open-agentic-catalog/examples/spire-values.yaml")) // #nosec G304 -- repository-owned example.
	if err != nil {
		return err
	}
	var overlay struct {
		Auth map[string]string `yaml:"auth"`
		Env  map[string]string `yaml:"configEnv"`
	}
	if err := yaml.Unmarshal(raw, &overlay); err != nil {
		return err
	}
	config, ok := values["configEnv"].(map[string]any)
	if !ok {
		return errors.New("missing Catalog configuration in base values")
	}
	for key, value := range overlay.Env {
		config[key] = value
	}
	values["auth"] = overlay.Auth
	return nil
}

func (h *harness) spirePhase(name, input string, extra ...string) error {
	args := []string{"-B", "test/interoperability/spire/kubernetes.py", name,
		"--work", filepath.Join(h.temporary, "spire"), "--image", h.spireImage, "--base", h.application,
		"--docker", h.options.docker, "--kubectl", h.options.kubectl, "--kubeconfig", h.kubeconfig,
		"--context", h.profile, "--namespace", h.catalogNamespace()}
	if h.spireRenewal {
		args = append(args, "--renewal")
	}
	_, err := h.runWithInput(h.ctx, "SPIRE "+name, []byte(input), environment("CATALOG_SPIRE_PYTHON", "python3"), append(args, extra...)...)
	return err
}

func (h *harness) verifySPIRERenewal(port string) error {
	peer, err := h.secondaryCatalog()
	if err != nil {
		return err
	}
	if err := peer.prepareDependencies(); err != nil {
		return err
	}
	if err := peer.installCatalog(); err != nil {
		return err
	}
	if err := peer.provisionIdentity("source-operator", otherTenantID, "tenant-two", "oidc-other-admin", "tenant_admin,source_admin,reader"); err != nil {
		return err
	}
	operator, err := signToken(peer.signingKey, "oidc-other-admin", "tenant-two", spireImporterAudience, time.Now().UTC())
	if err != nil {
		return err
	}
	address, err := h.startPortForwardIn("test", "service/catalog-b", 8445)
	if err != nil {
		return err
	}
	_, peerPort, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if err := h.spirePhase("source-setup", ""); err != nil {
		return err
	}
	for _, selection := range []struct{ marker, phase string }{
		{"spire-import", "source-renewal"}, {"spire-import-off", "source-disabled"},
	} {
		values, err := peer.writeHelmValues(selection.marker, "catalog_api")
		if err != nil {
			return err
		}
		if err := peer.deployCatalog(values, "4m"); err != nil {
			return err
		}
		if err := h.spirePhase(selection.phase, operator, "--port", port, "--peer-port", peerPort); err != nil {
			return err
		}
	}
	raw, err := os.ReadFile(filepath.Join(h.temporary, "spire", "renewal.json")) // #nosec G304 -- safe evidence from this fixture.
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &h.report.Peers); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "spire-projected-secret-renewal-without-consumer-restart", "spire-disabled-bound-import-preserves-index")
	return nil
}

func configureSPIREImportValues(values map[string]any, enabled bool) error {
	config, configOK := values["configEnv"].(map[string]any)
	secrets, secretsOK := values["secrets"].(map[string]any)
	if !configOK || !secretsOK {
		return errors.New("missing importer configuration or Secret references")
	}
	config["CATALOG_AUTH_AUDIENCE"] = spireImporterAudience
	// Match the Compose importer so a missing credential exhausts its retries during the test.
	config["CATALOG_JOBS_MAX_ATTEMPTS"] = "2"
	config["CATALOG_JOBS_RETRY_CEILING"] = "5s"
	config["CATALOG_DISCOVERY_HOSTS"] = `[{"host":"catalog-b.test","tenantId":"00000000-0000-7000-8000-000000000002","mode":"authenticated","publisherDomain":"catalog.example.com"}]`
	values["federation"] = map[string]any{"policy": map[string]string{"existingConfigMap": "spire-source-policy", "key": "policy.json"}, "trustBundleConfigMap": "spire-source-ca"}
	if enabled {
		secrets["federationCredentials"] = map[string]string{"name": "catalog-a-reader"}
		config["CATALOG_CREDENTIAL_RESOLUTION_ENABLED"] = "true"
		config["CATALOG_CREDENTIAL_RESOLUTION_DIRECTORY"] = "/etc/open-agentic-catalog/federation/credentials"
	}
	return nil
}
