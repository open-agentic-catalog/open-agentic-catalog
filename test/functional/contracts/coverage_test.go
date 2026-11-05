// Unit tests exercise isolated validation and controlled dependencies.
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	serviceapi "github.com/open-agentic-catalog/open-agentic-catalog/api"
)

func TestOperationDenominatorAndMatching(t *testing.T) {
	t.Parallel()
	ids, err := operationIDs(t.Context())
	if err != nil || len(ids) == 0 || !slices.IsSorted(ids) {
		t.Fatalf("invalid denominator: %v", err)
	}
	document, err := loadContract(t.Context(), serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct{ method, path, id string }{
		{"GET", "/api/v1/webhooks/reconciliation?limit=1", "getWebhookReconciliation"},
		{"POST", "/api/v1/webhooks/owned:rotate-secret", "rotateWebhookSecret"},
		{"GET", "/api/v1/resources/owned/versions", "listResourceVersions"},
		{"GET", "/.well-known/ai-catalog.json", "getAICatalog"},
	} {
		_, op, err := matchOperation(document, check.method, check.path)
		if err != nil || op.OperationID != check.id || !slices.Contains(ids, check.id) {
			t.Fatalf("matching %s: %v", check.id, err)
		}
	}
	for _, method := range []string{"get", "UNKNOWN", "DELETE"} {
		if _, _, err := matchOperation(document, method, "/livez"); err == nil {
			t.Fatal("accepted wrong method")
		}
	}
}

func TestObservationDoesNotClaimExpectedStatusOrRetainPrivateInput(t *testing.T) {
	t.Parallel()
	for _, check := range []struct {
		status         int
		header         http.Header
		body, contract string
	}{
		{0, nil, "", "not-checked"},
		{200, http.Header{"Content-Type": {"application/json"}}, `{"items":[]}`, "passed"},
		{200, http.Header{"Content-Type": {"application/json"}}, `{"items":"private-secret"}`, "failed"},
		{503, http.Header{"Content-Type": {"application/problem+json"}, "X-Request-ID": {"private-request"}, "Cache-Control": {"no-store"}}, `{"type":"about:blank","title":"Unavailable","status":503,"detail":"private-secret","code":"DEPENDENCY_UNAVAILABLE","requestId":"private-request"}`, "passed"},
	} {
		raw, err := json.Marshal(validateInput{"GET", "/api/v1/namespaces?cursor=private-secret", check.status, check.header, []byte(check.body)})
		if err != nil {
			t.Fatal(err)
		}
		value, err := observeStdin(t.Context(), bytes.NewReader(raw))
		if err != nil || value.OperationID != "listNamespaces" || value.Contract != check.contract || value.Status != check.status {
			t.Fatalf("observation: %+v, %v", value, err)
		}
		encoded, err := json.Marshal(value)
		if err != nil || bytes.Contains(encoded, []byte("private")) {
			t.Fatal("private evidence in observation")
		}
	}
	for _, input := range []validateInput{
		{Method: "GET", Path: "/livez", Status: 600},
		{Method: "GET", Path: "/unknown", Status: 200},
		{Method: "GET", Path: "/livez", Body: []byte("private")},
	} {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := observeStdin(t.Context(), bytes.NewReader(raw)); err == nil {
			t.Fatal("accepted invalid observation")
		}
	}
}
