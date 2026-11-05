//go:build minikube && functional

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPressureSourceSetupUsesDistinctScopedContractInputs(t *testing.T) {
	step := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := step / 2
		token := "primary"
		if index >= 9 {
			token = "other"
		}
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("Idempotency-Key") == "" {
			t.Error("source setup lost method, tenant or command identity")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid control JSON")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if step%2 == 0 {
			if r.URL.Path != "/api/v1/federation/source-inspections" || body["url"] != fmt.Sprintf("https://federation-fixture:8443/pressure/%d/.well-known/ard.json", index) || body["networkProfile"] != "functional-fixture" {
				t.Error("source inspection lost exact distinct URL")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"id":"00000000-0000-7000-8000-%012d"}`, 100+index)
		} else {
			if r.URL.Path != "/api/v1/federation/sources" || body["inspectionId"] != fmt.Sprintf("00000000-0000-7000-8000-%012d", 100+index) || body["updateMode"] != "manual_review" || body["retrievalMode"] != "indexed" || body["networkProfile"] != "functional-fixture" {
				t.Error("source connection differs from inspected/manual-review contract")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"id":"00000000-0000-7000-8000-%012d","revision":1}`, 200+index)
		}
		step++
	}))
	defer server.Close()
	h := &harness{ctx: t.Context(), tokens: map[string]string{"admin": "primary", "other": "other"}, suffix: "contract"}
	sources, err := h.preparePressureSources(t.Context(), []string{server.URL, server.URL, server.URL})
	if err != nil || step != 24 || len(sources) != 12 {
		t.Fatalf("setup calls=%d sources=%d error=%v", step, len(sources), err)
	}
	seen := map[string]bool{}
	for _, source := range sources {
		if seen[source.ID] || source.Revision != 1 {
			t.Fatal("source identity reused or lost")
		}
		seen[source.ID] = true
	}
}
