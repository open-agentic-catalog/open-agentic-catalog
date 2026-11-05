package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMetadataAdmissionAndPrivateControls(t *testing.T) {
	forwarded := 0
	f := &fixture{methods: map[string]int{}, mode: "benign", token: "runtime-only", admin: "control-only", server: mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "0.1.0"}, nil), handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { forwarded++; w.WriteHeader(http.StatusNoContent) })}
	request := func(method, path, body, token string) int {
		t.Helper()
		r := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		f.ServeHTTP(w, r)
		return w.Code
	}
	for _, method := range []string{"initialize", "notifications/initialized", "tools/list"} {
		if request(http.MethodPost, "/mcp", `{"jsonrpc":"2.0","method":"`+method+`"}`, "runtime-only") != http.StatusNoContent {
			t.Fatal("metadata method refused")
		}
	}
	if forwarded != 3 {
		t.Fatal("metadata forwarding lost")
	}
	for _, method := range []string{"tools/call", "prompts/list", "resources/read", "sampling/createMessage", "unknown"} {
		if request(http.MethodPost, "/mcp", `{"method":"`+method+`"}`, "runtime-only") != http.StatusForbidden {
			t.Fatal("forbidden method forwarded")
		}
	}
	if forwarded != 3 || f.invoked != 0 {
		t.Fatal("fixture invoked a tool")
	}
	if request(http.MethodPost, "/mcp", `{"method":"tools/list"}`, "catalog-token") != http.StatusUnauthorized || f.denied != 1 {
		t.Fatal("wrong credential accepted")
	}
	if request(http.MethodGet, "/fixture/state", "", "runtime-only") != http.StatusUnauthorized {
		t.Fatal("runtime key can read controls")
	}
	for _, body := range []string{`{"mode":"unknown"}`, `{"mode":"finding","extra":true}`, `{"mode":"finding"}{}`, strings.Repeat("x", 1025)} {
		if request(http.MethodPost, "/fixture/control", body, "control-only") != http.StatusBadRequest {
			t.Fatal("invalid control accepted")
		}
	}
	if request(http.MethodPost, "/fixture/control", `{"mode":"finding"}`, "control-only") != http.StatusNoContent || f.mode != "finding" {
		t.Fatal("finding control refused")
	}
	if request(http.MethodGet, "/fixture/state", "", "control-only") != http.StatusOK {
		t.Fatal("private observation refused")
	}
}
