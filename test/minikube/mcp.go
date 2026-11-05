//go:build minikube

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// verifyMCP qualifies chart admission, API Service routing and disablement.
// The packaged OAuth gate owns actual TLS proxy and independent issuer checks;
// this cluster has no ingress controller and makes no ingress TLS claim.
func (h *harness) verifyMCP() error {
	certificate, err := os.ReadFile(filepath.Join(h.temporary, "fixture", "server.pem")) // #nosec G304 -- exact invocation-owned fixture.
	if err != nil {
		return err
	}
	key, err := os.ReadFile(filepath.Join(h.temporary, "fixture", "server-key.pem")) // #nosec G304 -- exact invocation-owned fixture.
	if err != nil {
		return err
	}
	if err := h.applyObject("create owned MCP TLS Secret", kubernetesObject{"apiVersion": "v1", "kind": "Secret", "type": "kubernetes.io/tls", "metadata": map[string]any{"name": "catalog-mcp-tls", "namespace": namespace, "labels": testLabels("mcp")}, "stringData": map[string]string{"tls.crt": string(certificate), "tls.key": string(key)}}); err != nil {
		return err
	}
	rollout := func(enabled bool) (string, error) {
		marker := "mcp-disabled"
		if enabled {
			marker = "mcp-enabled"
		}
		valuesPath, err := h.writeHelmValues(marker, "catalog_api")
		if err != nil {
			return "", err
		}
		raw, err := os.ReadFile(valuesPath) // #nosec G304 -- invocation-owned Helm values.
		if err != nil {
			return "", err
		}
		var values map[string]any
		if err := json.Unmarshal(raw, &values); err != nil {
			return "", err
		}
		if enabled {
			config, configOK := values["configEnv"].(map[string]any)
			api, apiOK := values["api"].(map[string]any)
			if !configOK || !apiOK {
				return "", errors.New("invalid MCP Helm sections")
			}
			config["CATALOG_MCP_ENABLED"] = "true"
			config["CATALOG_MCP_MAX_TOOL_BYTES"] = "16384"
			config["CATALOG_MCP_MAX_CONCURRENCY"] = "2"
			config["CATALOG_MCP_ALLOWED_ORIGINS"] = `["https://browser.example.com"]`
			config["CATALOG_PUBLIC_BASE_URL"] = "https://federation-fixture"
			api["ingress"] = map[string]any{"enabled": true, "host": "federation-fixture", "tlsSecretName": "catalog-mcp-tls"} // #nosec G101 -- TLS Secret name, not a credential.
			raw, err = json.Marshal(values)
			if err != nil {
				return "", err
			}
			if err := os.WriteFile(valuesPath, raw, 0o600); err != nil {
				return "", err
			} // #nosec G306 -- private invocation inputs.
		}
		// Configuration changes use the operator's drain/migrate/start sequence.
		// Helm hooks alone do not drain existing runtime connections.
		if err := h.deployCatalog(valuesPath, "4m"); err != nil {
			return "", err
		}
		address, err := h.startPortForward("service/catalog", 8080)
		if err != nil {
			return "", err
		}
		base := "http://" + address
		return base, h.waitHTTP(base + "/readyz")
	}
	base, err := rollout(true)
	if err != nil {
		return err
	}
	raw, err := h.kubectl("inspect MCP ingress", "--namespace", namespace, "get", "ingress", "catalog-mcp", "--output=json")
	if err != nil {
		return err
	}
	var ingress struct {
		Spec struct {
			TLS []struct {
				Hosts      []string `json:"hosts"`
				SecretName string   `json:"secretName"`
			} `json:"tls"`
			Rules []struct {
				Host string `json:"host"`
				HTTP struct {
					Paths []struct {
						Path     string `json:"path"`
						PathType string `json:"pathType"`
						Backend  struct {
							Service struct {
								Name string `json:"name"`
								Port struct {
									Name string `json:"name"`
								} `json:"port"`
							} `json:"service"`
						} `json:"backend"`
					} `json:"paths"`
				} `json:"http"`
			} `json:"rules"`
		} `json:"spec"`
	}
	if json.Unmarshal(raw, &ingress) != nil || len(ingress.Spec.Rules) != 1 || len(ingress.Spec.TLS) != 1 || ingress.Spec.TLS[0].SecretName != "catalog-mcp-tls" || len(ingress.Spec.TLS[0].Hosts) != 1 || ingress.Spec.TLS[0].Hosts[0] != "federation-fixture" || ingress.Spec.Rules[0].Host != "federation-fixture" || len(ingress.Spec.Rules[0].HTTP.Paths) != 2 {
		return errors.New("mcp ingress authority differs from configured origin")
	}
	for i, p := range ingress.Spec.Rules[0].HTTP.Paths {
		if p.Path != []string{"/mcp", "/.well-known/oauth-protected-resource/mcp"}[i] || p.PathType != "Exact" || p.Backend.Service.Name != "catalog" || p.Backend.Service.Port.Name != "http" {
			return errors.New("mcp ingress exposed unexpected route or workload")
		}
	}
	metadata, err := h.request(http.MethodGet, base+"/.well-known/oauth-protected-resource/mcp", "", "", "", nil, nil)
	if err != nil || metadata.Status != http.StatusOK {
		return errors.New("helm MCP resource metadata unavailable")
	}
	var authority struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if json.Unmarshal(metadata.Body, &authority) != nil || authority.Resource != "https://federation-fixture/mcp" || len(authority.AuthorizationServers) != 1 || authority.AuthorizationServers[0] != oidcIssuer {
		return errors.New("helm MCP canonical resource or issuer differs")
	}
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, method := range []string{"tools/list", "tools/call"} {
			result, err := h.mcpRequest(base, version, method)
			if err != nil {
				return err
			}
			var reply struct {
				Result struct {
					Tools             []json.RawMessage `json:"tools"`
					IsError           bool              `json:"isError"`
					StructuredContent struct {
						Data struct {
							Items []json.RawMessage `json:"items"`
						} `json:"data"`
					} `json:"structuredContent"`
				} `json:"result"`
			}
			if result.Status != http.StatusOK || json.Unmarshal(result.Body, &reply) != nil || reply.Result.IsError {
				return errors.New("helm MCP request failed")
			}
			if method == "tools/list" && len(reply.Result.Tools) != 6 || method == "tools/call" && len(reply.Result.StructuredContent.Data.Items) == 0 {
				return errors.New("helm MCP tool inventory or governed Search is empty")
			}
		}
	}
	h.report.Checks = append(h.report.Checks, "mcp-exact-tls-ingress-admission-and-existing-api-service", "mcp-both-protocols-api-service-reads-with-fixture-oidc")
	base, err = rollout(false)
	if err != nil {
		return err
	}
	for _, path := range []string{"/mcp", "/.well-known/oauth-protected-resource/mcp"} {
		result, err := h.request(http.MethodGet, base+path, "", "", "", nil, nil)
		if err != nil || result.Status != http.StatusNotFound {
			return errors.New("disabled Helm MCP route remains available")
		}
	}
	raw, err = h.kubectl("inspect removed MCP ingress", "--namespace", namespace, "get", "ingress", "catalog-mcp", "--ignore-not-found", "--output=name")
	if err != nil || len(bytes.TrimSpace(raw)) != 0 {
		return errors.New("disabled MCP ingress remains installed")
	}
	h.report.Checks = append(h.report.Checks, "mcp-disablement-removes-routes-and-ingress")
	return nil
}

func (h *harness) mcpRequest(base, version, method string) (httpResult, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	defer client.CloseIdleConnections()
	return h.mcpRequestWithClient(base, version, method, client)
}

func (h *harness) mcpRequestWithClient(base, version, method string, client *http.Client) (httpResult, error) {
	params := map[string]any{}
	if method == "tools/call" {
		params["name"] = "catalog_control_search"
		params["arguments"] = map[string]int{"limit": 10}
	}
	if version == "2026-07-28" {
		params["_meta"] = map[string]any{"io.modelcontextprotocol/protocolVersion": version, "io.modelcontextprotocol/clientInfo": map[string]string{"name": "owned-helm-check", "version": "1"}, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}
	}
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return httpResult{}, err
	}
	req, err := http.NewRequestWithContext(h.ctx, http.MethodPost, base+"/mcp", bytes.NewReader(raw))
	if err != nil {
		return httpResult{}, err
	}
	// Preserve the URL's Host for the SDK rebinding guard. Direct fixture calls
	// use loopback; the certificate scenario uses its verified public hostname.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+h.tokens["reader"])
	req.Header.Set("Mcp-Protocol-Version", version)
	if version == "2026-07-28" {
		req.Header.Set("Mcp-Method", method)
		if method == "tools/call" {
			req.Header.Set("Mcp-Name", "catalog_control_search")
		}
	}
	reply, err := client.Do(req)
	if err != nil {
		return httpResult{}, errors.New("owned Helm MCP exchange failed")
	}
	defer func() { _ = reply.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(reply.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return httpResult{}, errors.New("owned Helm MCP response exceeded its bound")
	}
	return httpResult{Status: reply.StatusCode, Header: reply.Header, Body: body}, nil
}
