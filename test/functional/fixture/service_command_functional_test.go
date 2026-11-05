//go:build functional

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestServiceCommandPinsIdentityAndMutationToOneConnection(t *testing.T) {
	for _, mode := range []string{"discard", "not-selected", "replay", "revoked", "closed"} {
		t.Run(mode, func(t *testing.T) {
			var connections, mutations atomic.Int64
			var identityAddress string
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer private-token" {
					t.Error("lost authorization")
				}
				if r.URL.Path == "/api/v1/service-information" {
					identityAddress = r.RemoteAddr
					if mode == "closed" {
						w.Header().Set("Connection", "close")
					}
					_, _ = fmt.Fprintf(w, `{"service":"open-agentic-catalog","role":"api","instanceId":%q}`, commandTestID)
					return
				}
				mutations.Add(1)
				if r.RemoteAddr != identityAddress || r.Method != http.MethodPost || r.URL.Path != "/api/v1/namespaces" || r.Header.Get("Idempotency-Key") != "same-key" {
					t.Error("command changed connection or binding")
				}
				if mode == "revoked" {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"code":"FORBIDDEN"}`))
					return
				}
				w.Header().Set("ETag", `"rev-1"`)
				if mode == "replay" {
					w.Header().Set("Idempotency-Replayed", "true")
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"private":"not-for-diagnostics"}`))
			}))
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					connections.Add(1)
				}
			}
			server.Start()
			defer server.Close()
			var in serviceCommandInput
			in.Address = strings.TrimPrefix(server.URL, "http://")
			in.Token = "private-token"
			in.Key = "same-key"
			in.Operation = "discard"
			in.Process = commandTestID
			in.Namespace = &struct {
				Slug        string `json:"slug"`
				DisplayName string `json:"displayName"`
			}{Slug: "test", DisplayName: "Test"}
			if mode == "not-selected" {
				in.Process = "11234567-89ab-cdef-0123-456789abcdef"
			}
			if mode == "replay" || mode == "revoked" {
				in.Operation = "create"
				in.Process = ""
			}
			result := executeServiceCommand(in)
			if connections.Load() != 1 {
				t.Fatal("command opened a fallback connection")
			}
			switch mode {
			case "discard":
				if result.Outcome != "response_discarded" || result.Digest != "" || result.Status != 0 || result.ETag != "" || mutations.Load() != 1 {
					t.Fatalf("receipt was not withheld: %+v", result)
				}
			case "not-selected":
				if result.Outcome != "not_selected" || mutations.Load() != 0 {
					t.Fatal("mutation reached wrong process")
				}
			case "closed":
				if result.Outcome != "transport_error" || mutations.Load() != 0 {
					t.Fatal("closed connection was retried")
				}
			case "replay":
				sum := sha256.Sum256([]byte(`{"private":"not-for-diagnostics"}`))
				if !result.Replayed || result.Status != 201 || result.Digest != hex.EncodeToString(sum[:]) || result.ETag != `"rev-1"` {
					t.Fatalf("replay lost exact evidence: %+v", result)
				}
			case "revoked":
				if result.Status != 403 || result.Code != "FORBIDDEN" || result.Replayed {
					t.Fatal("refusal was not preserved")
				}
			}
		})
	}
}
