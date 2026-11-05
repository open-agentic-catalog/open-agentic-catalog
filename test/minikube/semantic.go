//go:build minikube

// Semantic qualification adds an offline model fixture to the existing owned
// Helm scenario. It is not a model chart or a production topology requirement.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// verifySemantic qualifies rolling requests across three APIs/workers and two
// identical models, with independent UI replicas and enforced provider egress.
func (h *harness) verifySemantic() error {
	raw, err := h.run("verify offline embedding cache", "python3", "test/embeddings/local.py")
	if err != nil {
		return err
	}
	var inputs map[string]string
	if err = json.Unmarshal(raw, &inputs); err != nil {
		return err
	}
	h.semanticImage = "open-agentic-catalog-embeddings:minikube-" + h.suffix
	if _, err = h.run("build owned offline model", h.options.docker, "build", "--tag", h.semanticImage, "--build-arg", "TEI_IMAGE="+inputs["CATALOG_EMBEDDING_IMAGE"], "--file", filepath.Join(h.options.repositoryRoot, "test/embeddings/Containerfile"), inputs["CATALOG_EMBEDDING_MODEL_DIRECTORY"]); err != nil {
		return err
	}
	if _, err = h.run("reverify offline embedding cache", "python3", "test/embeddings/local.py"); err != nil {
		return err
	}
	imageID, err := h.run("inspect offline model image", h.options.docker, "image", "inspect", "--format={{.Id}}", h.semanticImage)
	if err != nil {
		return err
	}
	h.report.Images["embeddings"] = strings.TrimSpace(string(imageID))
	if _, err = h.minikube("preload offline model", "image", "load", "--daemon=true", h.semanticImage); err != nil {
		return err
	}
	for _, object := range semanticModelObjects(h.semanticImage, inputs) {
		if err = h.applyObject("create optional model fixture", object); err != nil {
			return err
		}
	}
	if _, err = h.kubectl("wait for both offline model replicas", "-n", namespace, "rollout", "status", "deployment/embeddings", "--timeout=4m"); err != nil {
		return err
	}
	models, err := h.pods("embeddings")
	if err != nil || len(models) != 2 {
		return errors.New("expected two owned model replicas")
	}
	for _, model := range models {
		address, err := h.startPortForward("pod/"+model.Name, 8080)
		if err != nil {
			return err
		}
		info, err := h.request(http.MethodGet, "http://"+address+"/info", "", "", "", nil, nil)
		if err != nil || info.Status != http.StatusOK || !validSemanticModelInfo(info.Body, inputs) {
			return errors.New("model replica does not match the pinned inference profile")
		}
	}
	h.report.Checks = append(h.report.Checks, "both-model-replicas-pinned-profile")
	if err = h.applyNetworkPolicies(); err != nil {
		return err
	}
	if err = h.semanticNetworkAccess(true); err != nil {
		return err
	}
	// Stop both runtime roles before optional grant reconciliation. A Helm hook
	// alone does not drain old processes whose exact grants are about to change.
	if _, err = h.kubectl("drain roles before optional schema", "-n", namespace, "scale", "deployment/catalog-api", "deployment/catalog-worker", "--replicas=0"); err != nil {
		return err
	}
	for _, role := range []string{"api", "worker"} {
		if _, err = h.kubectl("wait for drained "+role, "-n", namespace, "wait", "--for=delete", "pod", "-l", "app.kubernetes.io/instance=catalog,app.kubernetes.io/component="+role, "--timeout=2m"); err != nil {
			return err
		}
	}
	if _, err = h.kubectl("install optional extension as fixture owner", "-n", namespace, "exec", "postgres-0", "--", "psql", "--no-psqlrc", "-v", "ON_ERROR_STOP=1", "-U", "catalog_owner", "-d", "catalog", "-c", "CREATE EXTENSION vector"); err != nil {
		return err
	}
	h.semantic = map[string]any{"enabled": true, "endpoint": "http://embeddings:80/v1/embeddings", "model": inputs["CATALOG_EMBEDDING_MODEL"], "profileRevision": inputs["CATALOG_EMBEDDING_REVISION"], "dimensions": 384, "similarityCutoff": 0.2, "allowHttp": true, "allowedAddressPrefixes": []string{localServiceCIDR}, "batchSize": 8, "batchTimeout": "2s"}
	values, err := h.writeHelmValues("semantic-enabled", "catalog_api")
	if err != nil {
		return err
	}
	if _, err = h.helm("enable semantic Helm configuration", "upgrade", releaseName, filepath.Join(h.options.repositoryRoot, "deployments/helm/open-agentic-catalog"), "--namespace", namespace, "--values", values, "--wait", "--wait-for-jobs", "--timeout=4m"); err != nil {
		return err
	}
	// The initial port-forward may refer to a drained Pod.
	address, err := h.startPortForward("service/catalog", 8080)
	if err != nil {
		return err
	}
	baseURL := "http://" + address
	if err = h.awaitSemantic(baseURL, "hybrid", time.Minute); err != nil {
		return err
	}
	pods, err := h.pods("api")
	if err != nil || len(pods) != 3 {
		return errors.New("expected three API replicas")
	}
	var cursor string
	var firstID string
	for i, pod := range pods {
		address, err := h.startPortForward("pod/"+pod.Name, 8080)
		if err != nil {
			return err
		}
		query := map[string]any{"text": "order", "limit": 1}
		if i == 1 {
			query["cursor"] = cursor
		}
		result, err := h.request(http.MethodPost, "http://"+address+"/api/v1/catalog/search", h.tokens["admin"], "", "", query, nil)
		var search struct {
			Tier  string `json:"tier"`
			Next  string `json:"nextCursor"`
			Total int    `json:"durableTotal"`
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		if err != nil || result.Status != http.StatusOK || json.Unmarshal(result.Body, &search) != nil || search.Tier != "hybrid" || search.Total != 3 || len(search.Items) != 1 {
			return fmt.Errorf("hybrid API replica %d failed", i)
		}
		if i == 0 {
			cursor = search.Next
			firstID = search.Items[0].ID
			if cursor == "" {
				return errors.New("missing hybrid cursor")
			}
		}
		if i == 1 && search.Items[0].ID == firstID {
			return errors.New("cross-replica cursor repeated identity")
		}
	}
	h.report.Checks = append(h.report.Checks, "three-api-worker-replicas-hybrid", "cross-api-model-replica-continuation")
	if err = h.verifyUI(); err != nil {
		return err
	}
	address, err = h.startPortForward("service/catalog", 8080)
	if err != nil {
		return err
	}
	baseURL = "http://" + address
	if err = h.semanticNetworkAccess(false); err != nil {
		return err
	}
	// Drop established keep-alive connections so this assertion verifies new
	// connections against the denied policy, independent of CNI conntrack grace.
	if _, err = h.kubectl("replace models under denied egress", "-n", namespace, "rollout", "restart", "deployment/embeddings"); err != nil {
		return err
	}
	if _, err = h.kubectl("wait for replacement models", "-n", namespace, "rollout", "status", "deployment/embeddings", "--timeout=4m"); err != nil {
		return err
	}
	if err = h.awaitSemantic(baseURL, "core", 30*time.Second); err != nil {
		return err
	}
	if err = h.semanticNetworkAccess(true); err != nil {
		return err
	}
	if err = h.awaitSemantic(baseURL, "hybrid", 30*time.Second); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "enforced-model-egress-fallback-recovery")
	// Retained generations survive disabling the capability; the model may stay
	// running without becoming a dependency of core Catalog startup or reads.
	h.semantic = map[string]any{"enabled": false}
	values, err = h.writeHelmValues("semantic-disabled", "catalog_api")
	if err != nil {
		return err
	}
	if _, err = h.helm("disable semantic retrieval", "upgrade", releaseName, filepath.Join(h.options.repositoryRoot, "deployments/helm/open-agentic-catalog"), "--namespace", namespace, "--values", values, "--wait", "--wait-for-jobs", "--timeout=4m"); err != nil {
		return err
	}
	address, err = h.startPortForward("service/catalog", 8080)
	if err != nil {
		return err
	}
	result, err := h.request(http.MethodGet, "http://"+address+"/api/v1/capabilities", h.tokens["admin"], "", "", nil, nil)
	if err != nil || result.Status != http.StatusOK || !strings.Contains(string(result.Body), `"semantic":"disabled"`) {
		return errors.New("disabled Helm semantic capability did not start")
	}
	h.report.Checks = append(h.report.Checks, "semantic-disable-preserves-core")
	return nil
}

// validSemanticModelInfo checks every replica's loaded inference settings, not
// merely its Deployment image tag or a successful readiness response.
func validSemanticModelInfo(raw []byte, inputs map[string]string) bool {
	var info struct {
		Name     string `json:"served_model_name"`
		Revision string `json:"model_sha"`
		Dtype    string `json:"model_dtype"`
		Length   int    `json:"max_input_length"`
		Truncate *bool  `json:"auto_truncate"`
		Version  string `json:"version"`
		Type     struct {
			Embedding struct {
				Pooling string `json:"pooling"`
			} `json:"embedding"`
		} `json:"model_type"`
	}
	return json.Unmarshal(raw, &info) == nil && info.Name == inputs["CATALOG_EMBEDDING_MODEL"] && info.Revision == inputs["CATALOG_EMBEDDING_REVISION"] && info.Dtype == "float32" && info.Length == 256 && info.Truncate != nil && !*info.Truncate && info.Version == "1.9.4" && info.Type.Embedding.Pooling == "mean"
}

// awaitSemantic waits on actual retrieval, not just enabled configuration.
func (h *harness) awaitSemantic(base, tier string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		result, err := h.request(http.MethodPost, base+"/api/v1/catalog/search", h.tokens["admin"], "", "", map[string]any{"text": "order", "limit": 1}, nil)
		var response struct {
			Tier     string `json:"tier"`
			Fallback string `json:"fallbackReason"`
		}
		if err == nil && result.Status == http.StatusOK && json.Unmarshal(result.Body, &response) == nil && response.Tier == tier && (tier != "core" || response.Fallback == "provider_unavailable") {
			return nil
		}
		select {
		case <-h.ctx.Done():
			return h.ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("semantic retrieval did not reach %s", tier)
}

// semanticNetworkAccess adds/removes only the model exception to existing
// API/worker default-deny egress. Database, identity and DNS rules stay intact.
func (h *harness) semanticNetworkAccess(allow bool) error {
	for _, role := range []string{"api", "worker"} {
		if !allow {
			if _, err := h.kubectl("deny optional provider egress", "-n", namespace, "delete", "networkpolicy", "semantic-"+role, "--ignore-not-found"); err != nil {
				return err
			}
			continue
		}
		if err := h.applyObject("allow optional provider egress", kubernetesObject{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": map[string]any{"name": "semantic-" + role, "namespace": namespace}, "spec": map[string]any{"podSelector": map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/instance": releaseName, "app.kubernetes.io/component": role}}, "policyTypes": []string{"Egress"}, "egress": []any{map[string]any{"to": []any{map[string]any{"podSelector": map[string]any{"matchLabels": testLabels("embeddings")}}}, "ports": []any{map[string]any{"protocol": "TCP", "port": 8080}}}}}}); err != nil {
			return err
		}
	}
	return nil
}

// semanticModelObjects is a fixture-only Deployment/Service with no volumes,
// credentials, external download or application readiness coupling.
func semanticModelObjects(image string, inputs map[string]string) []kubernetesObject {
	labels := testLabels("embeddings")
	return []kubernetesObject{
		{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "embeddings", "namespace": namespace}, "spec": map[string]any{"replicas": 2, "selector": map[string]any{"matchLabels": labels}, "template": map[string]any{"metadata": map[string]any{"labels": labels}, "spec": map[string]any{"automountServiceAccountToken": false, "securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 10001, "runAsGroup": 10001, "seccompProfile": map[string]string{"type": "RuntimeDefault"}}, "containers": []any{map[string]any{"name": "embeddings", "image": image, "imagePullPolicy": "Never", "args": []string{"--model-id", "/model", "--served-model-name", inputs["CATALOG_EMBEDDING_MODEL"], "--revision", inputs["CATALOG_EMBEDDING_REVISION"], "--dtype", "float32", "--pooling", "mean", "--port", "8080", "--auto-truncate=false", "--max-client-batch-size", "32", "--max-batch-tokens", "4096", "--tokenization-workers", "2"}, "env": []any{map[string]string{"name": "HF_HUB_OFFLINE", "value": "1"}, map[string]string{"name": "OMP_NUM_THREADS", "value": "2"}}, "ports": []any{map[string]any{"containerPort": 8080}}, "securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}}, "resources": map[string]any{"requests": map[string]string{"cpu": "100m", "memory": "512Mi"}, "limits": map[string]string{"cpu": "2", "memory": "3Gi"}}, "readinessProbe": map[string]any{"httpGet": map[string]any{"path": "/health", "port": 8080}, "periodSeconds": 2}}}}}}},
		{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "embeddings", "namespace": namespace}, "spec": map[string]any{"selector": labels, "ports": []any{map[string]any{"port": 80, "targetPort": 8080}}}},
	}
}
