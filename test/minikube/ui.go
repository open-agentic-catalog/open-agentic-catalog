//go:build minikube

// UI checks verify optional chart wiring and shared sessions across independently selected Pods.

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/securecookie"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// verifyUI qualifies the optional packaged host with two or three replicas. Synthetic
// login exchanges real fixture-issued API tokens; it is not human OIDC evidence.
func (h *harness) verifyUI() error {
	replicas := 2
	if h.options.scenario == "replicas" || h.options.scenario == "semantic" {
		replicas = 3
	}
	before, err := h.pods("ui")
	if err != nil || len(before) != 0 {
		return errors.New("UI-disabled installation unexpectedly has UI pods")
	}
	backendBefore := make(map[string][]podIdentity)
	if h.options.scenario == "ui" {
		for _, role := range []string{"api", "worker"} {
			pods, err := h.pods(role)
			if err != nil {
				return err
			}
			if len(pods) != h.options.runtimeReplicas() {
				return fmt.Errorf("UI-disabled installation has unexpected %s replicas", role)
			}
			backendBefore[role] = pods
			h.recordPlacement("before-ui-"+role, pods)
		}
	}
	image := "open-agentic-catalog-ui:minikube-" + h.suffix
	if _, err := h.run("build optional UI image", h.options.docker, "build", "--tag", image, "--file", "catalog-ui/Containerfile", "."); err != nil {
		return err
	}
	h.uiImage = image
	imageID, err := h.run("inspect optional UI image", h.options.docker, "image", "inspect", "--format={{.Id}}", image)
	if err != nil {
		return err
	}
	h.report.Images["ui"] = strings.TrimSpace(string(imageID))
	if _, err := h.minikube("load optional UI image", "image", "load", "--daemon=true", image); err != nil {
		return err
	}
	hash, err := randomHex(64)
	if err != nil {
		return err
	}
	block, err := randomHex(32)
	if err != nil {
		return err
	}
	users, err := json.Marshal([]map[string]string{{"alias": "admin", "label": "Local administrator", "token": h.tokens["admin"]}})
	if err != nil {
		return err
	}
	if err := h.createSecret("create scoped UI fixture keys", "catalog-ui-inputs", map[string]string{"keys.json": fmt.Sprintf(`[{"hash":%q,"block":%q}]`, hash, block), "users.json": string(users)}); err != nil {
		return err
	}
	marker := "ui"
	if h.options.scenario == "topology" || h.options.scenario == "ui" {
		// UI-only changes must not replace the backend; rollout qualification
		// belongs to the separate reconfiguration and upgrade scenarios.
		marker = "initial"
	}
	values, err := h.writeHelmValues(marker, "catalog_api")
	if err != nil {
		return err
	}
	overlay := filepath.Join(h.temporary, "ui.json")
	uiConfig := map[string]any{
		"enabled": true, "replicas": replicas, "existingSecret": "catalog-ui-inputs",
		"image":  map[string]string{"repository": "open-agentic-catalog-ui", "tag": "minikube-" + h.suffix, "pullPolicy": "Never"},
		"config": map[string]string{"environment": "development", "authMode": "development", "origin": "http://127.0.0.1:4180", "basePath": "/browser"},
	}
	if h.options.scenario == "topology" {
		uiConfig["networkPolicy"] = uiNetworkPolicyValues(false)
	}
	raw, err := json.Marshal(map[string]any{"ui": uiConfig})
	if err != nil {
		return err
	}
	if err := os.WriteFile(overlay, raw, 0o600); err != nil {
		return err
	}
	chart := filepath.Join(h.options.repositoryRoot, "deployments/helm/open-agentic-catalog")
	if _, err := h.helm("enable UI replicas", "upgrade", releaseName, chart, "--namespace", namespace, "--values", values, "--values", overlay, "--wait", "--wait-for-jobs", "--timeout=4m"); err != nil {
		return err
	}
	pods, err := h.pods("ui")
	if err != nil || len(pods) != replicas {
		return errors.New("UI replicas did not become ready")
	}
	h.recordPlacement("ui", pods)
	addresses := make([]string, 0, replicas)
	for _, pod := range pods {
		if !pod.Ready || pod.Image != image || pod.ImageID != h.report.Images["ui"] || pod.ServiceAccount != "catalog-ui" {
			return errors.New("UI replica is not ready or has an unexpected image/account")
		}
		address, err := h.startPortForward("pod/"+pod.Name, 8080)
		if err != nil {
			return err
		}
		addresses = append(addresses, "http://"+address)
	}
	login, err := h.uiRequest(addresses[0], "POST", "/browser/auth/login", `{"alias":"admin"}`, "")
	if err != nil {
		return uiLoginTransportFailure(err)
	}
	if login.Status != http.StatusOK {
		return fmt.Errorf("packaged UI development login returned HTTP %d", login.Status)
	}
	cookie := strings.SplitN(login.Header.Get("Set-Cookie"), ";", 2)[0]
	if cookie == "" {
		return errors.New("packaged UI login returned no session")
	}
	for _, address := range addresses {
		for _, path := range []string{"/livez", "/readyz", "/browser/catalog", "/browser/runtime.json", "/browser/auth/session", "/browser/_catalog/api/v1/me"} {
			response, err := h.uiRequest(address, "GET", path, "", cookie)
			if err != nil || response.Status != http.StatusOK {
				return fmt.Errorf("packaged UI replica check failed: %s", path)
			}
		}
	}
	if h.options.scenario == "topology" {
		if err := h.verifyUINetworkPolicy(pods[0].Name, chart, values, overlay, uiConfig); err != nil {
			return err
		}
	}
	if h.options.scenario == "ui" {
		if err := h.verifyUIKeyLifecycle(pods, addresses, cookie, hash, block); err != nil {
			return err
		}
	}
	if h.options.scenario == "replicas" {
		if err := h.verifyReplicaWorkload(addresses, cookie); err != nil {
			return err
		}
	}
	if h.options.scenario == "semantic" {
		for _, address := range addresses {
			response, err := h.uiRequest(address, "POST", "/browser/_catalog/api/v1/catalog/search", `{"text":"order","limit":2}`, cookie)
			var search struct {
				Tier string `json:"tier"`
			}
			if err != nil || response.Status != http.StatusOK || json.Unmarshal(response.Body, &search) != nil || search.Tier != "hybrid" {
				return errors.New("UI replica did not proxy hybrid retrieval")
			}
		}
		h.report.Checks = append(h.report.Checks, "three-ui-replicas-hybrid-search")
	}
	if _, err := h.helm("disable UI without removing Catalog", "upgrade", releaseName, chart, "--namespace", namespace, "--values", values, "--wait", "--wait-for-jobs", "--timeout=4m"); err != nil {
		return err
	}
	raw, err = h.kubectl("check UI removal", "-n", namespace, "get", "deployment,service,configmap,serviceaccount,networkpolicy", "-l", "app.kubernetes.io/component=ui", "-o", "name")
	if err != nil || strings.TrimSpace(string(raw)) != "" {
		return errors.New("disabled UI retained chart resources")
	}
	for _, role := range []string{"api", "worker"} {
		previous, retained := backendBefore[role]
		if !retained {
			continue
		}
		current, err := h.pods(role)
		if err != nil {
			return err
		}
		if !sameUIDs(previous, current) {
			return fmt.Errorf("UI enable/disable replaced %s Pods", role)
		}
		for _, pod := range current {
			if !pod.Ready {
				return fmt.Errorf("UI enable/disable left an unready %s Pod", role)
			}
		}
		h.recordPlacement("after-ui-"+role, current)
	}
	if len(backendBefore) != 0 {
		h.report.Checks = append(h.report.Checks, "ui-enable-disable-preserves-backend-pods")
	}
	h.report.Checks = append(h.report.Checks, "ui-default-off", "ui-subpath-and-probes", "ui-cross-replica-session-and-api", "ui-disable-preserves-backend")
	return nil
}

// uiLoginTransportFailure preserves a fixed failure category without disclosing request or session values.
func uiLoginTransportFailure(err error) error {
	category := "transport"
	transportCause := err
	var requestError *url.Error
	if errors.As(err, &requestError) {
		transportCause = requestError.Err
	}
	var networkError net.Error
	switch {
	case errors.Is(err, context.Canceled):
		category = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		category = "deadline"
	case errors.As(transportCause, &networkError) && networkError.Timeout():
		category = "timeout"
	case errors.Is(err, io.ErrUnexpectedEOF):
		category = "unexpected_eof"
	case errors.Is(err, io.EOF):
		category = "eof"
	case errors.Is(err, net.ErrClosed):
		category = "closed"
	}
	return fmt.Errorf("packaged UI development login transport failed (%s)", category)
}

// uiRequest selects a Pod through an owned loopback forward while retaining the
// configured public Host/Origin, as an ingress would. Never log session cookies.
func (h *harness) uiRequest(address, method, path, body, cookie string) (httpResult, error) {
	client := replicaHTTPClient()
	defer client.CloseIdleConnections()
	return h.uiCommand(client, address, method, path, body, cookie, "", "")
}

// uiCommand preserves ingress headers while pinning a request to an owned Pod.
// A caller can reuse the bounded transport during a concurrent workload.
func (h *harness) uiCommand(client *http.Client, address, method, path, body, cookie, key, match string) (httpResult, error) {
	return h.uiCommandContext(h.ctx, client, address, method, path, body, cookie, key, match)
}

func (h *harness) uiCommandContext(ctx context.Context, client *http.Client, address, method, path, body, cookie, key, match string) (httpResult, error) {
	request, err := http.NewRequestWithContext(ctx, method, address+path, strings.NewReader(body))
	if err != nil {
		return httpResult{}, err
	}
	request.Host = "127.0.0.1:4180"
	request.Header.Set("Origin", "http://127.0.0.1:4180")
	request.Header.Set("X-Catalog-Request", "1")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Cookie", cookie)
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if match != "" {
		request.Header.Set("If-Match", match)
	}
	response, err := client.Do(request)
	if err != nil {
		return httpResult{}, err
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if len(raw) > 1<<20 {
		return httpResult{}, errors.New("UI response exceeded 1 MiB")
	}
	return httpResult{Status: response.StatusCode, Header: response.Header.Clone(), Body: raw}, err
}

// verifyUIKeyLifecycle uses synthetic mounted inputs and explicit Pod routes.
// Immediate retirement qualifies the emergency path; normal time windows are
// deterministic host tests, not a claim that a production lifetime elapsed.
func (h *harness) verifyUIKeyLifecycle(pods []podIdentity, addresses []string, oldCookie, oldHash, oldBlock string) error {
	raw, err := h.kubectl("check UI has no session affinity", "-n", namespace, "get", "service/catalog-ui", "-o", "jsonpath={.spec.sessionAffinity}")
	if err != nil || string(raw) != "None" {
		return errors.New("UI Service unexpectedly requires affinity")
	}
	nextHash, err := randomHex(64)
	if err != nil {
		return err
	}
	nextBlock, err := randomHex(32)
	if err != nil {
		return err
	}
	old := map[string]string{"hash": oldHash, "block": oldBlock}
	next := map[string]string{"hash": nextHash, "block": nextBlock}
	oracle := func(cookie string, pair map[string]string, valid bool) error {
		split := strings.SplitN(cookie, "=", 2)
		if len(split) != 2 {
			return errors.New("missing rotation cookie")
		}
		hash, _ := hex.DecodeString(pair["hash"])
		block, _ := hex.DecodeString(pair["block"])
		var value map[string]any
		err := securecookie.New(hash, block).SetSerializer(securecookie.JSONEncoder{}).MaxAge(28800).Decode(split[0], split[1], &value)
		if (err == nil) != valid {
			return errors.New("packaged signing key differs from independent oracle")
		}
		if valid {
			expiry, ok := value["e"].(float64)
			if !ok || expiry <= float64(time.Now().Unix()) {
				return errors.New("rotation cookie not unexpired")
			}
		}
		return nil
	}
	check := func(address, cookie string, valid bool) error {
		for _, path := range []string{"/browser/auth/session", "/browser/_catalog/api/v1/me"} {
			response, err := h.uiRequest(address, "GET", path, "", cookie)
			want := http.StatusUnauthorized
			if valid {
				want = http.StatusOK
			}
			if err != nil || response.Status != want {
				return errors.New("rotation session/API admission differs")
			}
			if valid {
				var access struct {
					TenantID  string `json:"tenantId"`
					Subject   string `json:"subject"`
					ActorType string `json:"actorType"`
				}
				if path == "/browser/auth/session" {
					var session struct {
						Access *struct {
							TenantID  string `json:"tenantId"`
							Subject   string `json:"subject"`
							ActorType string `json:"actorType"`
						} `json:"access"`
					}
					if json.Unmarshal(response.Body, &session) != nil || session.Access == nil {
						return errors.New("rotation session missing access")
					}
					access = *session.Access
				} else if json.Unmarshal(response.Body, &access) != nil {
					return errors.New("rotation API readback malformed")
				}
				if access.TenantID != primaryTenantID || access.Subject != "oidc-admin" || access.ActorType != "human" || bytes.Contains(response.Body, []byte(h.tokens["admin"])) {
					return errors.New("rotation readback lost identity or disclosed token")
				}
			}
		}
		return nil
	}
	login := func(address string, want, other map[string]string) (string, error) {
		response, err := h.uiRequest(address, "POST", "/browser/auth/login", `{"alias":"admin"}`, "")
		if err != nil || response.Status != 200 {
			return "", errors.New("rotation login failed")
		}
		cookie := strings.SplitN(response.Header.Get("Set-Cookie"), ";", 2)[0]
		if err := oracle(cookie, want, true); err != nil {
			return "", err
		}
		if err := oracle(cookie, other, false); err != nil {
			return "", err
		}
		return cookie, nil
	}
	update := func(stage string, ring ...map[string]string) error {
		keys, err := json.Marshal(ring)
		if err != nil {
			return err
		}
		patch, err := json.Marshal(map[string]any{"data": map[string]string{"keys.json": base64.StdEncoding.EncodeToString(keys)}})
		if err != nil {
			return err
		}
		_, err = h.kubectlInput("mount UI keys "+stage, patch, "-n", namespace, "patch", "secret/catalog-ui-inputs", "--type=merge", "--patch-file=/dev/stdin")
		return err
	}
	replace := func(stage string, before []podIdentity) ([]podIdentity, []string, error) {
		if _, err := h.kubectl("replace UI processes "+stage, "-n", namespace, "rollout", "restart", "deployment/catalog-ui"); err != nil {
			return nil, nil, err
		}
		if _, err := h.kubectl("wait UI processes "+stage, "-n", namespace, "rollout", "status", "deployment/catalog-ui", "--timeout=3m"); err != nil {
			return nil, nil, err
		}
		after, err := h.pods("ui")
		if err != nil {
			return nil, nil, err
		}
		if !uiRotationPodsReplaced(before, after) {
			return nil, nil, errors.New("UI rotation retained an old process")
		}
		h.recordPlacement("ui-key-"+stage, after)
		routes := []string{}
		for _, pod := range after {
			if pod.ImageID != h.report.Images["ui"] || pod.ServiceAccount != "catalog-ui" {
				return nil, nil, errors.New("rotation changed UI image/account")
			}
			address, err := h.startPortForward("pod/"+pod.Name, 8080)
			if err != nil {
				return nil, nil, err
			}
			routes = append(routes, "http://"+address)
		}
		return after, routes, nil
	}
	if err := oracle(oldCookie, old, true); err != nil {
		return err
	}
	if err := oracle(oldCookie, next, false); err != nil {
		return err
	}
	if err := update("staging", old, next); err != nil {
		return err
	}
	// Secret mutation alone must leave startup-loaded writers on the old key.
	for _, address := range addresses {
		if _, err := login(address, old, next); err != nil {
			return err
		}
	}
	stagedPods, staged, err := replace("staging", pods)
	if err != nil {
		return err
	}
	for _, address := range staged {
		if err := check(address, oldCookie, true); err != nil {
			return err
		}
		if _, err := login(address, old, next); err != nil {
			return err
		}
	}
	if err := update("promotion", next, old); err != nil {
		return err
	}
	// First promotion replaces one host, retaining the staged peer for mixed reads.
	if _, err := h.kubectl("promote first UI process", "-n", namespace, "delete", "pod", stagedPods[0].Name, "--wait=true", "--timeout=2m"); err != nil {
		return err
	}
	if _, err := h.kubectl("wait first promoted UI process", "-n", namespace, "rollout", "status", "deployment/catalog-ui", "--timeout=3m"); err != nil {
		return err
	}
	mixedPromotion, err := h.pods("ui")
	if err != nil || len(mixedPromotion) != 2 {
		return errors.New("promotion mixed stage missing replicas")
	}
	h.recordPlacement("ui-key-mixed-promotion", mixedPromotion)
	var mixedNew string
	for _, pod := range mixedPromotion {
		if pod.UID == stagedPods[1].UID {
			continue
		}
		if pod.UID == stagedPods[0].UID || !pod.Ready || pod.ImageID != h.report.Images["ui"] || pod.ServiceAccount != "catalog-ui" {
			return errors.New("promotion process not replaced")
		}
		forward, err := h.startPortForward("pod/"+pod.Name, 8080)
		if err != nil {
			return err
		}
		mixedNew, err = login("http://"+forward, next, old)
		if err != nil {
			return err
		}
		if err := check("http://"+forward, oldCookie, true); err != nil {
			return err
		}
	}
	if mixedNew == "" {
		return errors.New("promotion had no new writer")
	}
	if err := check(staged[1], mixedNew, true); err != nil {
		return err
	}
	if _, err := login(staged[1], old, next); err != nil {
		return err
	}
	promotedPods, promoted, err := replace("promotion", mixedPromotion)
	if err != nil {
		return err
	}
	newCookie, err := login(promoted[0], next, old)
	if err != nil {
		return err
	}
	for _, address := range promoted {
		if err := check(address, oldCookie, true); err != nil {
			return err
		}
		if err := check(address, newCookie, true); err != nil {
			return err
		}
	}
	if err := update("emergency-retirement", next); err != nil {
		return err
	}
	// Keep one running overlap host as a valid control while replacing its peer.
	if _, err := h.kubectl("replace first retired-key host", "-n", namespace, "delete", "pod", promotedPods[0].Name, "--wait=true", "--timeout=2m"); err != nil {
		return err
	}
	if _, err := h.kubectl("wait first retired-key host", "-n", namespace, "rollout", "status", "deployment/catalog-ui", "--timeout=3m"); err != nil {
		return err
	}
	mixed, err := h.pods("ui")
	if err != nil || len(mixed) != 2 {
		return errors.New("retirement mixed stage missing replicas")
	}
	h.recordPlacement("ui-key-mixed-emergency-retirement", mixed)
	for _, pod := range mixed {
		if pod.UID == promotedPods[1].UID {
			continue
		}
		if pod.UID == promotedPods[0].UID || !pod.Ready || pod.ImageID != h.report.Images["ui"] || pod.ServiceAccount != "catalog-ui" {
			return errors.New("first retirement process not replaced")
		}
		forward, err := h.startPortForward("pod/"+pod.Name, 8080)
		if err != nil {
			return err
		}
		if err := oracle(oldCookie, old, true); err != nil {
			return err
		}
		if err := check(promoted[1], oldCookie, true); err != nil {
			return err
		}
		if err := check("http://"+forward, oldCookie, false); err != nil {
			return err
		}
		if err := check("http://"+forward, newCookie, true); err != nil {
			return err
		}
	}
	_, retired, err := replace("emergency-retirement", mixed)
	if err != nil {
		return err
	}
	for _, address := range retired {
		if err := check(address, oldCookie, false); err != nil {
			return err
		}
		if err := check(address, newCookie, true); err != nil {
			return err
		}
	}
	h.report.Checks = append(h.report.Checks, "ui-key-staging-old-writes", "ui-key-promotion-new-writes", "ui-startup-only-mounted-keys", "ui-no-affinity-per-pod-rotation", "ui-emergency-retirement-unexpired-old-rejected-overlap-control-valid")
	return nil
}

// uiRotationPodsReplaced checks both independently ready two-replica processes.
func uiRotationPodsReplaced(before, after []podIdentity) bool {
	if len(before) != 2 || len(after) != 2 {
		return false
	}
	seen := map[string]bool{}
	for _, group := range [][]podIdentity{before, after} {
		for _, pod := range group {
			if !pod.Ready || pod.UID == "" || seen[pod.UID] {
				return false
			}
			seen[pod.UID] = true
		}
	}
	return true
}
