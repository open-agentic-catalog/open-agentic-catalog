//go:build minikube

// Webhook checks verify chart inputs, private TLS, delivery and role rollout behavior.

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

// Compose owns the full delivery matrix. These tests cover the distinct
// chart Secret/ConfigMap, Service DNS, role rollout and private TLS boundary.
func (h *harness) verifyWebhooks() error {
	ca, err := os.ReadFile(filepath.Join(h.temporary, "fixture", "trust", "functional-ca.pem")) // #nosec G304 -- exact invocation-owned fixture.
	if err != nil {
		return err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	if err := h.createSecret("create scoped webhook key", "catalog-webhook-key", map[string]string{"key": base64.StdEncoding.EncodeToString(key)}); err != nil {
		return err
	}
	policy := map[string]any{"schemaVersion": 1, "policyId": "minikube-webhooks", "revision": 1, "public": map[string]any{"mode": "allowlist", "origins": []string{}}, "privateProfiles": []map[string]any{{"name": "owned-receiver", "origins": []string{"https://federation-fixture:8443"}, "addressRanges": []string{localServiceCIDR}, "assignedTenantIds": []string{primaryTenantID}, "trustBundleFile": "/etc/open-agentic-catalog/webhooks/ca.pem", "transportRef": "direct"}}}
	raw, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	if err := h.applyObject("create scoped webhook egress", kubernetesObject{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "catalog-webhook-egress", "namespace": namespace, "labels": testLabels("webhook-egress")}, "data": map[string]string{"policy.json": string(raw), "ca.pem": string(ca)}}); err != nil {
		return err
	}
	rollout := func(enabled bool, marker string) (string, error) {
		h.webhooks = enabled
		values, err := h.writeHelmValues(marker, "catalog_api")
		if err != nil {
			return "", err
		}
		if _, err := h.helm("roll out webhook capability", "upgrade", releaseName, h.options.repositoryRoot+"/deployments/helm/open-agentic-catalog", "--namespace", namespace, "--values", values, "--wait", "--wait-for-jobs", "--timeout=4m"); err != nil {
			return "", err
		}
		address, err := h.startPortForward("service/catalog", 8080)
		if err != nil {
			return "", err
		}
		return "http://" + address, h.waitHTTP("http://" + address + "/readyz")
	}
	base, err := rollout(true, "webhooks-enabled")
	if err != nil {
		return err
	}
	result, err := h.request(http.MethodPost, base+"/api/v1/webhooks", h.tokens["admin"], "webhook-register-"+h.suffix, "", webhook.CreateInput{Name: "Owned Helm receiver", URL: "https://federation-fixture:8443/webhooks/events", EventTypes: []string{"namespace.created"}}, nil)
	if err != nil || result.Status != http.StatusCreated {
		return errors.New("helm webhook registration failed")
	}
	var endpoint webhook.CreateResult
	if json.Unmarshal(result.Body, &endpoint) != nil || endpoint.Endpoint.ID == "" || endpoint.Secret == "" {
		return errors.New("helm webhook registration contract failed")
	}
	address, err := h.startPortForward("service/federation-fixture", 8443)
	if err != nil {
		return err
	}
	client, err := fixtureHTTPClient(ca)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	control := map[string]any{"endpointId": endpoint.Endpoint.ID, "secret": endpoint.Secret, "version": endpoint.Endpoint.SecretVersion, "status": http.StatusNoContent, "delayMillis": 0}
	result, err = h.request(http.MethodPost, "https://"+address+"/webhooks/control", "", "", "", control, client)
	if err != nil || result.Status != http.StatusNoContent {
		return errors.New("helm receiver configuration failed")
	}
	for _, stage := range []string{"initial", "restarted"} {
		if stage == "restarted" {
			if err := h.recreateRuntimePods(); err != nil {
				return err
			}
			address, err := h.startPortForward("service/catalog", 8080)
			if err != nil {
				return err
			}
			base = "http://" + address
			if err := h.waitHTTP(base + "/readyz"); err != nil {
				return err
			}
		}
		result, err = h.request(http.MethodPost, base+"/api/v1/namespaces", h.tokens["admin"], "webhook-mutation-"+stage+"-"+h.suffix, "", map[string]string{"slug": "webhook-" + stage + "-" + h.suffix, "displayName": "Webhook Helm qualification"}, nil)
		if err != nil || result.Status != http.StatusCreated {
			return errors.New("helm webhook mutation failed")
		}
		if err := h.waitWebhookDeliveries(base, endpoint.Endpoint.ID, stage == "restarted"); err != nil {
			return err
		}
		result, err = h.request(http.MethodPost, base+"/api/v1/webhooks/"+endpoint.Endpoint.ID+":test", h.tokens["admin"], "webhook-test-"+stage+"-"+h.suffix, fmt.Sprintf(`"rev-%d"`, endpoint.Endpoint.Revision), map[string]any{}, nil)
		var delivery webhook.Delivery
		if err != nil || result.Status != http.StatusOK || json.Unmarshal(result.Body, &delivery) != nil || delivery.State != webhook.DeliverySucceeded {
			return errors.New("helm API sender or mounted key failed")
		}
	}
	result, err = h.request(http.MethodPost, base+"/api/v1/webhooks", h.tokens["admin"], "webhook-denied-"+h.suffix, "", webhook.CreateInput{Name: "Denied origin", URL: "https://decoy-fixture:8443/webhooks/events", EventTypes: []string{"namespace.created"}}, nil)
	var denied webhook.CreateResult
	if err != nil || result.Status != http.StatusCreated || json.Unmarshal(result.Body, &denied) != nil {
		return errors.New("helm denied-origin fixture registration failed")
	}
	decoy, err := h.startPortForward("service/decoy-fixture", 8443)
	if err != nil {
		return err
	}
	before, err := h.request(http.MethodGet, "https://"+decoy+"/webhooks/state", "", "", "", nil, client)
	if err != nil || before.Status != http.StatusOK {
		return errors.New("helm decoy baseline failed")
	}
	result, err = h.request(http.MethodPost, base+"/api/v1/webhooks/"+denied.Endpoint.ID+":test", h.tokens["admin"], "webhook-denied-send-"+h.suffix, fmt.Sprintf(`"rev-%d"`, denied.Endpoint.Revision), map[string]any{}, nil)
	var refused webhook.Delivery
	if err != nil || result.Status != http.StatusOK || json.Unmarshal(result.Body, &refused) != nil || refused.State == webhook.DeliverySucceeded || refused.ResponseStatus != 0 {
		return errors.New("helm webhook sender accepted an unapproved origin")
	}
	after, err := h.request(http.MethodGet, "https://"+decoy+"/webhooks/state", "", "", "", nil, client)
	if err != nil || after.Status != http.StatusOK || !bytes.Equal(before.Body, after.Body) {
		return errors.New("helm webhook sender contacted the decoy")
	}
	result, err = h.request(http.MethodGet, base+"/api/v1/webhooks/"+endpoint.Endpoint.ID, h.tokens["other"], "", "", nil, nil)
	if err != nil || result.Status != http.StatusNotFound {
		return errors.New("helm webhook tenant isolation failed")
	}
	base, err = rollout(false, "webhooks-disabled")
	if err != nil {
		return err
	}
	result, err = h.request(http.MethodGet, base+"/api/v1/webhooks/reconciliation", h.tokens["admin"], "", "", nil, nil)
	if err != nil || result.Status != http.StatusNotFound {
		return errors.New("helm disabled webhook route remains enabled")
	}
	h.report.Checks = append(h.report.Checks, "webhook-helm-secret-egress-service-dns-tls-restart-and-disable")
	return nil
}

// waitWebhookDeliveries polls delivery evidence for the fixture's namespace-created events, requiring an additional success after restart when requested.
func (h *harness) waitWebhookDeliveries(base, endpoint string, restarted bool) error {
	want := 1
	if restarted {
		want = 2
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		result, err := h.request(http.MethodGet, base+"/api/v1/webhooks/"+endpoint+"/deliveries?limit=100", h.tokens["admin"], "", "", nil, nil)
		var page struct {
			Items []webhook.Delivery `json:"items"`
		}
		if err == nil && result.Status == http.StatusOK && json.Unmarshal(result.Body, &page) == nil {
			count := 0
			for _, delivery := range page.Items {
				if delivery.EventType == "com.open.agentic.catalog.namespace.created.v1" && delivery.State == webhook.DeliverySucceeded {
					count++
				}
			}
			if count >= want {
				return nil
			}
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-h.ctx.Done():
			timer.Stop()
			return h.ctx.Err()
		case <-timer.C:
		}
	}
	return errors.New("helm worker signed delivery timed out")
}
