//go:build functional

// Functional tests exercise local HTTP or composed service workflows.
package httpserver

import (
	"encoding/json"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	serviceapi "github.com/open-agentic-catalog/open-agentic-catalog/api"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
)

func TestJSONCommandBodyRejectionDoesNotMutateOrReplaySuccess(t *testing.T) {
	t.Parallel()
	// Parse the contract once for this sequential table, not for each rejected
	// attempt. Keep response validation without repeated full-document loads.
	contract, err := openapi3.NewLoader().LoadFromData(serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	problemSchema := contract.Components.Schemas["Problem"]
	if problemSchema == nil || problemSchema.Value == nil {
		t.Fatal("OpenAPI Problem schema is missing")
	}
	const validBody = `{"slug":"example","displayName":"Example"}`
	for _, tc := range []struct{ name, body, media string }{
		{"missing media", validBody, ""},
		{"wrong media", validBody, "text/plain"},
		{"invalid media", validBody, "application/json; charset"},
		{"null", `null`, "application/json"},
		{"array", `[]`, "application/json"},
		{"string", `"example"`, "application/json"},
		{"boolean", `true`, "application/json"},
		{"number", `42`, "application/json"},
		{"malformed", `{`, "application/json"},
		{"absent required body", ``, "application/json"},
		{"wrong field case", `{"slug":"example","DisplayName":"Example"}`, "application/json"},
		{"field alias overwrite", `{"slug":"example","displayName":"First","DisplayName":"Second"}`, "application/json"},
		{"null optional string", `{"slug":"example","displayName":"Example","description":null}`, "application/json"},
		{"invalid UTF-8", "{\"slug\":\"example\",\"displayName\":\"" + string([]byte{0xff}) + "\"}", "application/json"},
		{"NUL display name", `{"slug":"example","displayName":"Ex\u0000ample"}`, "application/json"},
		{"NUL description", `{"slug":"example","displayName":"Example","description":"bad\u0000text"}`, "application/json"},
		{"lone surrogate", `{"slug":"example","displayName":"\ud800"}`, "application/json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repository := catalog.NewMemoryRepository()
			service, err := catalog.NewService(catalog.Options{Repository: repository, Clock: clock.NewFake(time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)), IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: []byte(strings.Repeat("k", 32))})
			if err != nil {
				t.Fatal(err)
			}
			logger := slog.New(slog.DiscardHandler)
			server := &Server{catalog: service, logger: logger, maxBodyBytes: 256}
			cache := newIdempotencyCache(idempotency.NewMemoryStore(), logger)
			handler := requestIDMiddleware(auditExecution(cache.middleware(256, http.HandlerFunc(server.handleNamespacesCreate))))
			ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "admin", MembershipID: "00000000-0000-7000-8000-000000000501", RealmID: "00000000-0000-7000-8000-000000000502", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}})
			invoke := func(body, media, key string) *httptest.ResponseRecorder {
				t.Helper()
				request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/namespaces", strings.NewReader(body))
				request.Header.Set("Content-Type", media)
				request.Header.Set("Idempotency-Key", key)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				return response
			}
			for attempt := range 2 {
				response := invoke(tc.body, tc.media, "rejected")
				if response.Code != http.StatusBadRequest {
					t.Fatalf("attempt %d: status=%d body=%s", attempt, response.Code, response.Body)
				}
				var problem any
				if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
					t.Fatal(err)
				}
				if err := problemSchema.Value.VisitJSON(problem, openapi3.EnableJSONSchema2020()); err != nil {
					t.Fatalf("invalid Problem response: %v", err)
				}
			}
			items, err := service.ListNamespaces(ctx, catalog.NamespaceListRequest{Limit: 10})
			if err != nil || len(items) != 0 {
				t.Fatalf("rejected command mutated namespaces: %v, %v", items, err)
			}
			// A valid request proves the same handler and tenant can write. Only
			// that successful response may be replayed as a creation.
			created := invoke(validBody, "application/json; charset=utf-8", "accepted")
			if created.Code != http.StatusCreated {
				t.Fatalf("valid command: status=%d body=%s", created.Code, created.Body)
			}
			replayed := invoke(validBody, "application/json; charset=utf-8", "accepted")
			if replayed.Code != http.StatusCreated || replayed.Header().Get("Idempotency-Replayed") != "true" || replayed.Body.String() != created.Body.String() {
				t.Fatalf("valid replay: status=%d body=%s", replayed.Code, replayed.Body)
			}
			items, err = service.ListNamespaces(ctx, catalog.NamespaceListRequest{Limit: 10})
			if err != nil || len(items) != 1 {
				t.Fatalf("creation/replay namespaces: %v, %v", items, err)
			}
		})
	}
}

// Both requests pass through the actual router and the domain validator.
func TestNamespaceHTTPCharacterBounds(t *testing.T) {
	server := newWebhookHTTPServer(t)
	defer server.Close()
	for index, text := range []string{strings.Repeat("a", 160), strings.Repeat("é", 160), strings.Repeat("🙂", 160)} {
		body := map[string]any{"slug": fmt.Sprintf("unicode-%d", index), "displayName": text, "description": strings.Repeat("中", 2000)}
		result := requestJSON(t, server.URL, http.MethodPost, "/api/v1/namespaces", "admin", fmt.Sprintf("unicode-%d", index), "", body)
		requireStatus(t, result, http.StatusCreated)
		var created catalog.Namespace
		decodeResult(t, result, &created)
		if created.DisplayName != text || created.Description != body["description"] {
			t.Fatal("text changed during creation")
		}
		path := "/api/v1/namespaces/" + created.ID
		update := map[string]any{"displayName": text, "description": strings.Repeat("🙂", 2000), "reason": "Character bound check"}
		patched := requestJSON(t, server.URL, http.MethodPatch, path, "admin", fmt.Sprintf("unicode-patch-%d", index), result.header.Get("ETag"), update)
		requireStatus(t, patched, http.StatusOK)
		for field, value := range map[string]string{"displayName": text + "x", "description": strings.Repeat("中", 2001)} {
			invalid := map[string]any{"reason": "Over-limit check", field: value}
			rejected := requestJSON(t, server.URL, http.MethodPatch, path, "admin", fmt.Sprintf("unicode-over-%d-%s", index, field), patched.header.Get("ETag"), invalid)
			requireStatus(t, rejected, http.StatusBadRequest)
		}
		got := requestJSON(t, server.URL, http.MethodGet, path, "admin", "", "", nil)
		requireStatus(t, got, http.StatusOK)
		var retained catalog.Namespace
		decodeResult(t, got, &retained)
		if retained.Revision != created.Revision+1 || retained.DisplayName != text || retained.Description != update["description"] {
			t.Fatal("rejected update changed retained namespace")
		}
	}
}

func TestNamespaceEncodedBodyLimitPreservesCommandState(t *testing.T) {
	const limit = 1024
	name := strings.Repeat("🙂", 160)
	literal := `{"slug":"bounded","displayName":"` + name + `"}`
	escaped := `{"slug":"bounded","displayName":"` + strings.Repeat(`\ud83d\ude42`, 160) + `"}`
	for _, tc := range []struct {
		name, rejected string
		size           int
	}{
		{"below", literal + strings.Repeat(" ", limit+1-len(literal)), limit - 1},
		{"exact", literal + strings.Repeat(" ", limit+1-len(literal)), limit},
		{"escape spelling counts bytes", escaped, limit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repository := catalog.NewMemoryRepository()
			now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
			service, err := catalog.NewService(catalog.Options{Repository: repository, Clock: clock.NewFake(now), IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: []byte(strings.Repeat("k", 32))})
			if err != nil {
				t.Fatal(err)
			}
			logger := slog.New(slog.DiscardHandler)
			server := &Server{catalog: service, logger: logger, maxBodyBytes: limit}
			cache := newIdempotencyCache(idempotency.NewMemoryStore(), logger)
			handler := requestIDMiddleware(auditExecution(cache.middleware(limit, http.HandlerFunc(server.handleNamespacesCreate))))
			actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "admin", MembershipID: "00000000-0000-7000-8000-000000000501", RealmID: "00000000-0000-7000-8000-000000000502", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}}
			ctx := identityfixture.WithActor(t.Context(), actor)
			invoke := func(body string) *httptest.ResponseRecorder {
				request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/namespaces", strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Idempotency-Key", "same-key")
				result := httptest.NewRecorder()
				handler.ServeHTTP(result, request)
				return result
			}
			// Both spellings represent exactly the same valid domain object.
			var decoded catalog.CreateNamespaceInput
			if err := json.Unmarshal([]byte(tc.rejected), &decoded); err != nil || decoded.DisplayName != name {
				t.Fatalf("invalid limit fixture: %v", err)
			}
			for range 2 {
				result := invoke(tc.rejected)
				var problem struct {
					Code string `json:"code"`
				}
				if err := json.Unmarshal(result.Body.Bytes(), &problem); err != nil || result.Code != http.StatusBadRequest || problem.Code != string(catalog.CodeInvalidArgument) || result.Header().Get("Idempotency-Replayed") != "" {
					t.Fatalf("wrong limit rejection: %d %s", result.Code, result.Body)
				}
			}
			items, err := service.ListNamespaces(ctx, catalog.NamespaceListRequest{Limit: 10})
			if err != nil || len(items) != 0 || len(cache.entries) != 0 {
				t.Fatalf("oversized request retained command/domain state: %v", err)
			}
			audits, err := repository.AuditEvents(ctx, actor.TenantID, catalog.AuditEventListRequest{Limit: 10})
			if err != nil || len(audits) != 0 {
				t.Fatalf("oversized request retained audit: %v", err)
			}
			outbox, err := repository.ClaimOutbox(ctx, "test", now, time.Minute, 10)
			if err != nil || len(outbox) != 0 {
				t.Fatalf("oversized request retained outbox: %v", err)
			}
			valid := literal + strings.Repeat(" ", tc.size-len(literal))
			created := invoke(valid)
			if created.Code != http.StatusCreated {
				t.Fatalf("same-key valid recovery: %d %s", created.Code, created.Body)
			}
			var saved catalog.Namespace
			if err := json.Unmarshal(created.Body.Bytes(), &saved); err != nil || saved.DisplayName != name {
				t.Fatalf("valid Unicode changed: %v", err)
			}
			replay := invoke(valid)
			if replay.Code != http.StatusCreated || replay.Header().Get("Idempotency-Replayed") != "true" || replay.Body.String() != created.Body.String() {
				t.Fatal("valid command did not replay exactly")
			}
			audits, err = repository.AuditEvents(ctx, actor.TenantID, catalog.AuditEventListRequest{Limit: 10})
			if err != nil || len(audits) != 1 {
				t.Fatalf("valid command audit count: %v %v", audits, err)
			}
			outbox, err = repository.ClaimOutbox(ctx, "test", now, time.Minute, 10)
			if err != nil || len(outbox) != 1 {
				t.Fatalf("valid command outbox count: %v %v", outbox, err)
			}
			other := actor
			other.TenantID = "00000000-0000-7000-8000-000000000002"
			if _, err := service.GetNamespace(identityfixture.WithActor(t.Context(), other), saved.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
				t.Fatalf("cross-tenant read: %v", err)
			}
		})
	}
}
