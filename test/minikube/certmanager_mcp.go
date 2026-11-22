//go:build minikube

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type smokeRelease struct {
	ReleaseID  string `json:"releaseId"`
	Identifier string `json:"identifier"`
}

func (h *harness) verifyCertificateMCPRead(client *http.Client, version, method string, want smokeRelease) error {
	response, err := h.mcpRequestWithClient("https://"+certificateHost, version, method, client)
	if err != nil {
		return err
	}
	var reply struct {
		ID      int             `json:"id"`
		JSONRPC string          `json:"jsonrpc"`
		Error   json.RawMessage `json:"error"`
		Result  struct {
			Tools   []json.RawMessage `json:"tools"`
			IsError bool              `json:"isError"`
			Content struct {
				Data struct {
					Items []struct {
						ID         string `json:"id"`
						Identifier string `json:"identifier"`
						Type       string `json:"type"`
					} `json:"items"`
				} `json:"data"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if response.Status != http.StatusOK || json.Unmarshal(response.Body, &reply) != nil || reply.JSONRPC != "2.0" || reply.ID != 1 || len(reply.Error) != 0 || reply.Result.IsError {
		return fmt.Errorf("authenticated MCP ingress %s failed (HTTP %d)", method, response.Status)
	}
	if method == "tools/list" {
		if len(reply.Result.Tools) != 6 {
			return errors.New("MCP ingress lost tool inventory")
		}
		return nil
	}
	for _, item := range reply.Result.Content.Data.Items {
		if item.Type == "local" && item.ID == "local:"+want.ReleaseID && item.Identifier == want.Identifier {
			return nil
		}
	}
	return errors.New("MCP ingress Search did not return the published smoke release and its identifier")
}

func (h *harness) verifyCertificateMCP(client *http.Client, want smokeRelease) error {
	base := "https://" + certificateHost
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, method := range []string{"tools/list", "tools/call"} {
			if err := h.verifyCertificateMCPRead(client, version, method, want); err != nil {
				return err
			}
		}
	}
	for _, path := range []string{"/api/v1/resources", "/readyz", "/mcp/extra", "/.well-known/oauth-protected-resource/mcp/extra"} {
		response, err := h.request("GET", base+path, "", "", "", nil, client)
		if err != nil || response.Status != 404 {
			return errors.New("MCP ingress exposed an unselected route")
		}
	}
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	for _, authenticated := range []bool{false, true} {
		request, err := http.NewRequestWithContext(h.ctx, http.MethodPost, base+"/mcp", bytes.NewReader(body))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		request.Header.Set("Mcp-Protocol-Version", "2025-11-25")
		want := 401
		if authenticated {
			request.Header.Set("Authorization", "Bearer "+h.tokens["reader"])
			request.Header.Set("Origin", "https://not-allowed.example")
			want = 403
		}
		reply, err := client.Do(request)
		if err != nil {
			return errors.New("MCP ingress authorization probe failed")
		}
		_ = reply.Body.Close()
		if reply.StatusCode != want {
			return errors.New("MCP ingress did not preserve authentication or Origin enforcement")
		}
		if !authenticated && !strings.Contains(reply.Header.Get("WWW-Authenticate"), base+"/.well-known/oauth-protected-resource/mcp") {
			return errors.New("MCP ingress challenge lost its canonical metadata URL")
		}
	}
	for _, role := range []string{"api", "worker"} {
		for _, resource := range []string{"secrets", "certificates.cert-manager.io"} {
			_, err := h.kubectl("deny Catalog access to issuer authority", "auth", "can-i", "get", resource, "-n", namespace, "--as=system:serviceaccount:"+namespace+":catalog-"+role)
			if err == nil || !strings.HasSuffix(strings.TrimSpace(err.Error()), "\nno") {
				return errors.New("catalog service account gained certificate authority access")
			}
		}
	}
	return nil
}
