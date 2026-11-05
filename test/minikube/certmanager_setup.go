//go:build minikube

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const certificateControllerNamespace = "catalog-cert-manager"

func (h *harness) certificateValuesFile(name string, values map[string]any) (string, error) {
	raw, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	path := filepath.Join(h.temporary, name+".json")
	return path, os.WriteFile(path, raw, 0o600) // #nosec G306 -- Private invocation-owned inputs.
}

func certificateControllerValues(images map[string]string) map[string]any {
	image := func(name string) map[string]any {
		return map[string]any{"repository": "open-agentic-catalog", "tag": strings.SplitN(images[name], ":", 2)[1], "pullPolicy": "Never"}
	}
	resources := map[string]any{"requests": map[string]string{"cpu": "25m", "memory": "64Mi"}, "limits": map[string]string{"cpu": "500m", "memory": "256Mi"}}
	return map[string]any{"crds": map[string]any{"enabled": true}, "replicaCount": 1, "image": image("controller"), "resources": resources,
		"webhook":         map[string]any{"replicaCount": 1, "image": image("webhook"), "resources": resources},
		"cainjector":      map[string]any{"replicaCount": 1, "image": image("cainjector"), "resources": resources},
		"startupapicheck": map[string]any{"enabled": false}, "prometheus": map[string]any{"enabled": false},
		"global": map[string]any{"leaderElection": map[string]string{"namespace": certificateControllerNamespace}}}
}

func certificateIngressValues(image string) map[string]any {
	return map[string]any{
		"image": map[string]any{"registry": "docker.io", "repository": "library/open-agentic-catalog", "tag": strings.SplitN(image, ":", 2)[1], "pullPolicy": "Never"}, "versionOverride": "3.7.16",
		"deployment": map[string]any{"replicas": 1}, "service": map[string]any{"spec": map[string]any{"type": "ClusterIP"}},
		"api":                   map[string]any{"dashboard": false},
		"instanceLabelOverride": "catalog-edge",
		"ingressClass":          map[string]any{"enabled": false}, "ingressRoute": map[string]any{"dashboard": map[string]any{"enabled": false}},
		"providers": map[string]any{"kubernetesCRD": map[string]any{"enabled": false}, "kubernetesGateway": map[string]any{"enabled": false}, "kubernetesIngress": map[string]any{"enabled": true, "namespaces": []string{namespace}, "ingressClass": "catalog-local", "publishedService": map[string]any{"enabled": false}}},
		"rbac":      map[string]any{"namespaced": true},
		"ports":     map[string]any{"web": map[string]any{"expose": map[string]any{"default": false}}, "websecure": map[string]any{"port": 8443}},
		"global":    map[string]any{"checkNewVersion": false, "sendAnonymousUsage": false},
		"resources": map[string]any{"requests": map[string]string{"cpu": "25m", "memory": "64Mi"}, "limits": map[string]string{"cpu": "500m", "memory": "256Mi"}},
	}
}

func (h *harness) installCertificateController() error {
	pins, err := readCertificatePins()
	if err != nil {
		return err
	}
	values, err := h.certificateValuesFile("cert-manager", certificateControllerValues(h.certificateImages))
	if err != nil {
		return err
	}
	if _, err := h.helm("install owned external cert-manager", "upgrade", "--install", "catalog-cert-manager", h.certificateTool(pins.Charts["cert-manager"].File), "--namespace", certificateControllerNamespace, "--create-namespace", "--values", values, "--wait", "--timeout", "3m"); err != nil {
		return err
	}
	if _, err := h.run("wait for certificate API admission", h.certificateTool(pins.CMCTL.File), "--kubeconfig", h.kubeconfig, "--context", h.profile, "check", "api", "--wait=90s"); err != nil {
		return err
	}
	material, err := localIssuerMaterial(time.Now())
	if err != nil {
		return err
	}
	h.certificateRoot = []byte(material["tls.crt"])
	if err := h.createSecret("create private local CA", localCASecret, material); err != nil {
		return err
	}
	for _, object := range localIssuerObjects() {
		if err := h.applyObject("create namespaced certificate input", object); err != nil {
			return err
		}
	}
	if _, err := h.kubectl("wait for local CA issuance", "-n", namespace, "wait", "--for=condition=Ready", "issuer/catalog-local-ca", "certificate/catalog-mcp", "--timeout=120s"); err != nil {
		return err
	}
	return nil
}

func (h *harness) installCertificateIngress() error {
	pins, err := readCertificatePins()
	if err != nil {
		return err
	}
	valuesPath, err := h.writeHelmValues("certificate-ingress", "catalog_api")
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(valuesPath) // #nosec G304 -- Invocation-owned values.
	if err != nil {
		return err
	}
	var values map[string]any
	if json.Unmarshal(raw, &values) != nil {
		return errors.New("invalid certificate Catalog values")
	}
	config, ok := values["configEnv"].(map[string]any)
	if !ok {
		return errors.New("missing Catalog configuration")
	}
	api, ok := values["api"].(map[string]any)
	if !ok {
		return errors.New("missing API values")
	}
	config["CATALOG_MCP_ENABLED"] = "true"
	config["CATALOG_PUBLIC_BASE_URL"] = "https://" + certificateHost
	config["CATALOG_MCP_ALLOWED_ORIGINS"] = `["https://assistant.example.com"]`
	api["ingress"] = map[string]any{"enabled": true, "host": certificateHost, "tlsSecretName": certificateSecret, "annotations": map[string]string{"kubernetes.io/ingress.class": "catalog-local", "traefik.ingress.kubernetes.io/router.entrypoints": "websecure", "traefik.ingress.kubernetes.io/router.tls": "true"}}
	valuesPath, err = h.certificateValuesFile("certificate-catalog", values)
	if err != nil {
		return err
	}
	if err := h.deployCatalog(valuesPath, "4m"); err != nil {
		return err
	}
	edgeValues, err := h.certificateValuesFile("certificate-edge", certificateIngressValues(h.certificateImages["traefik"]))
	if err != nil {
		return err
	}
	_, err = h.helm("install owned external ingress", "upgrade", "--install", "catalog-edge", h.certificateTool(pins.Charts["traefik"].File), "--namespace", namespace, "--skip-crds", "--values", edgeValues, "--wait", "--timeout", "3m")
	return err
}
