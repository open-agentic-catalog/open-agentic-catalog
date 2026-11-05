//go:build functional

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	serviceapi "github.com/open-agentic-catalog/open-agentic-catalog/api"
)

func TestRequestEnvelopeJourneyDetectsFalseSuccessAndMutation(t *testing.T) {
	document, err := loadContract(t.Context(), serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "wrong problem", "rejected write persisted", "replay marker missing", "report failure"} {
		t.Run(mode, func(t *testing.T) {
			namespace := map[string]any{"id": "00000000-0000-7000-8000-000000000010", "tenantId": "00000000-0000-7000-8000-000000000001", "slug": "functional-json-envelope", "displayName": "Owned raw JSON contract", "revision": 1, "createdAt": "2026-09-11T00:00:00Z", "updatedAt": "2026-09-11T00:00:00Z"}
			created, rejected, missingType, nullBody, duplicateBody := 0, 0, 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", "owned-request")
				w.Header().Set("Cache-Control", "no-store")
				if r.Method == http.MethodGet {
					items := []any{}
					if created > 0 || mode == "rejected write persisted" && rejected > 0 {
						items = append(items, namespace)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
					return
				}
				body, _ := io.ReadAll(r.Body)
				if bytes.Count(body, []byte(`"slug"`)) == 2 {
					duplicateBody++
					// Ordinary decoding keeps the last duplicate. That result must
					// be valid, so missing required fields cannot mask this case.
					var decoded any
					if err := json.Unmarshal(body, &decoded); err != nil {
						t.Error(err)
					} else if err := document.Components.Schemas["CreateNamespace"].Value.VisitJSON(decoded); err != nil {
						t.Errorf("duplicate-key fixture has another validation error: %v", err)
					}
				}
				if _, ok := r.Header["Content-Type"]; !ok {
					missingType++
				}
				if string(body) == "null" {
					nullBody++
				}
				if r.Header.Get("Idempotency-Key") != "functional-json-valid" {
					rejected++
					w.Header().Set("Content-Type", "application/problem+json")
					w.WriteHeader(http.StatusBadRequest)
					code := "INVALID_ARGUMENT"
					if mode == "wrong problem" {
						code = "CONFLICT"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": "Invalid input", "status": 400, "detail": "Owned rejection", "code": code, "requestId": "owned-request"})
					return
				}
				created++
				w.Header().Set("ETag", `"rev-1"`)
				if created > 1 && mode != "replay marker missing" {
					w.Header().Set("Idempotency-Replayed", "true")
				}
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(namespace)
			}))
			defer server.Close()
			client, err := localClient(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			observed := 0
			err = requestEnvelopes(t.Context(), client, server.URL, document, func(_ observation, _ int) error {
				observed++
				if mode == "report failure" {
					return errors.New("owned report failure")
				}
				return nil
			})
			if mode == "valid" {
				if err != nil || created != 2 || rejected != 12 || missingType != 2 || nullBody != 2 || duplicateBody != 2 || observed != 17 {
					t.Fatalf("raw journey: err=%v created=%d rejected=%d missing=%d null=%d duplicate=%d observed=%d", err, created, rejected, missingType, nullBody, duplicateBody, observed)
				}
			} else if !errors.Is(err, errContract) {
				t.Fatalf("bad evidence accepted: %v", err)
			}
		})
	}
}
