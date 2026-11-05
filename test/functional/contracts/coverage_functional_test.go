//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	serviceapi "github.com/open-agentic-catalog/open-agentic-catalog/api"
)

func TestRepresentativeRetainsPartialTransportEvidence(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi/control-v1.yaml" {
			w.Header().Set("Content-Type", "application/yaml")
			_, _ = w.Write(serviceapi.ControlV1)
			return
		}
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("truncated fixture"))
	}))
	defer server.Close()
	var values []observation
	err := runObserved(t.Context(), server.URL, "unused", func(value observation, expected int) error {
		if expected != 200 {
			t.Fatal("changed expected status")
		}
		values = append(values, value)
		return nil
	})
	if !errors.Is(err, errTransport) || len(values) != 2 || values[0].OperationID != "getControlOpenAPI" || values[0].Contract != "passed" || values[1].Status != 0 || values[1].Contract != "not-checked" {
		t.Fatalf("partial observations: %+v, %v", values, err)
	}
}

func TestRepresentativeStopsOnReportingFailure(t *testing.T) {
	t.Parallel()
	for _, truncated := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete response", true: "transport failure"}[truncated], func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/yaml")
				if truncated {
					w.Header().Set("Content-Length", "100")
					_, _ = w.Write([]byte("truncated fixture"))
					return
				}
				_, _ = w.Write(serviceapi.ControlV1)
			}))
			defer server.Close()
			var values []observation
			err := runObserved(t.Context(), server.URL, "unused", func(value observation, _ int) error {
				values = append(values, value)
				return errors.New("private reporting failure")
			})
			if !errors.Is(err, errContract) || len(values) != 1 {
				t.Fatalf("report failure must stop the workflow with a safe error: %+v, %v", values, err)
			}
			want := observation{"getControlOpenAPI", http.StatusOK, "passed"}
			if truncated {
				want.Status, want.Contract = 0, "not-checked"
			}
			if values[0] != want {
				t.Fatalf("observation = %+v, want %+v", values[0], want)
			}
		})
	}
}

func TestRepresentativeDistinguishesSchemaValidityFromExactContractBytes(t *testing.T) {
	t.Parallel()
	for _, mediaType := range []string{"application/yaml", "application/json"} {
		t.Run(mediaType, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", mediaType)
				_, _ = w.Write([]byte("changed: contract"))
			}))
			defer server.Close()
			var values []observation
			err := runObserved(t.Context(), server.URL, "unused", func(value observation, _ int) error {
				values = append(values, value)
				return nil
			})
			wantState := "passed"
			if mediaType != "application/yaml" {
				wantState = "failed"
			}
			// A YAML string fits the response schema, but must still be the
			// exact embedded contract before any further requests are made.
			if !errors.Is(err, errContract) || len(values) != 1 || values[0] != (observation{"getControlOpenAPI", http.StatusOK, wantState}) {
				t.Fatalf("contract mismatch: %+v, %v", values, err)
			}
		})
	}
}
