//go:build minikube

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v4"
)

// verifySPIRE runs only in the explicitly selected, disposable SPIRE scenario.
func (h *harness) verifySPIRE() error {
	h.spireImage = "open-agentic-catalog:minikube-spire-" + h.suffix
	phase := func(name string, extra ...string) error {
		args := []string{"-B", "test/interoperability/spire/kubernetes.py", name,
			"--work", filepath.Join(h.temporary, "spire"), "--image", h.spireImage, "--base", h.application,
			"--docker", h.options.docker, "--kubectl", h.options.kubectl, "--kubeconfig", h.kubeconfig,
			"--context", h.profile, "--namespace", h.catalogNamespace()}
		_, err := h.run("SPIRE "+name, environment("CATALOG_SPIRE_PYTHON", "python3"), append(args, extra...)...)
		return err
	}
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
