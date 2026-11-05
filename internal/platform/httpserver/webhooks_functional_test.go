//go:build functional

// Functional tests exercise local HTTP or composed service workflows.
package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/feedback"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

type successfulWebhookSender struct{}

func TestHTTPWebhookRotationContractAndReplay(t *testing.T) {
	server := newWebhookHTTPServer(t)
	defer server.Close()
	createdResponse := requestJSON(t, server.URL, http.MethodPost, "/api/v1/webhooks", "admin", "rotation-create", "", map[string]any{"name": "Receiver", "url": "https://hooks.example.com/rotate", "eventTypes": []string{"*"}})
	requireStatus(t, createdResponse, http.StatusCreated)
	var created webhook.CreateResult
	decodeResult(t, createdResponse, &created)
	path := "/api/v1/webhooks/" + created.Endpoint.ID + ":rotate-secret"
	for i, raw := range []string{`{"unknown":true}`, `{"revokePrevious":"true"}`, `{"revokePrevious":true,"revokePrevious":false}`, `{"revokePrevious":null}`, `{} {}`, `[]`, `null`} {
		result := requestWithHeaders(t, server.URL, http.MethodPost, path, []byte(raw), map[string]string{"Authorization": "Bearer admin", "Content-Type": "application/json", "Idempotency-Key": "invalid-rotation-" + strconv.Itoa(i), "If-Match": `"rev-1"`})
		requireStatus(t, result, http.StatusBadRequest)
	}
	rotatedResponse := requestJSON(t, server.URL, http.MethodPost, path, "admin", "rotation-normal", `"rev-1"`, map[string]any{})
	requireStatus(t, rotatedResponse, http.StatusOK)
	requireControlSchema(t, "WebhookSecretResult", rotatedResponse.body)
	var rotated webhook.CreateResult
	decodeResult(t, rotatedResponse, &rotated)
	if rotated.PreviousSecretExpiresAt == nil || rotated.Endpoint.SecretVersion != 2 || rotatedResponse.header.Get("ETag") != `"rev-2"` {
		t.Fatal("routine rotation omitted overlap or updated revision")
	}
	replay := requestJSON(t, server.URL, http.MethodPost, path, "admin", "rotation-normal", `"rev-1"`, map[string]any{})
	requireStatus(t, replay, http.StatusOK)
	if string(replay.body) != string(rotatedResponse.body) {
		t.Fatal("rotation replay changed its secret or expiry")
	}
	requireStatus(t, requestJSON(t, server.URL, http.MethodPost, path, "admin", "rotation-overlap", `"rev-2"`, map[string]any{}), http.StatusConflict)
	requireStatus(t, requestJSON(t, server.URL, http.MethodPost, path, "other-admin", "rotation-cross-tenant", `"rev-2"`, map[string]any{"revokePrevious": true}), http.StatusNotFound)
	emergencyResponse := requestJSON(t, server.URL, http.MethodPost, path, "admin", "rotation-emergency", `"rev-2"`, map[string]any{"revokePrevious": true})
	requireStatus(t, emergencyResponse, http.StatusOK)
	requireControlSchema(t, "WebhookSecretResult", emergencyResponse.body)
	var emergency webhook.CreateResult
	decodeResult(t, emergencyResponse, &emergency)
	if emergency.PreviousSecretExpiresAt != nil || emergency.Endpoint.SecretVersion != 3 || emergency.Secret == rotated.Secret || emergencyResponse.header.Get("ETag") != `"rev-3"` {
		t.Fatal("emergency rotation did not revoke its predecessor")
	}
	auditResponse := requestJSON(t, server.URL, http.MethodGet, "/api/v1/audit-events?action=webhook.secret_rotated&targetType=webhook_endpoint&targetId="+created.Endpoint.ID+"&limit=10", "admin", "", "", nil)
	requireStatus(t, auditResponse, http.StatusOK)
	var auditPage cursorPage[json.RawMessage]
	decodeResult(t, auditResponse, &auditPage)
	if len(auditPage.Items) != 2 {
		t.Fatalf("routine/emergency rotation should each audit once; replay and denied requests must not add evidence: %d", len(auditPage.Items))
	}
}

func (successfulWebhookSender) Send(context.Context, webhook.SendRequest) (webhook.SendResult, error) {
	return webhook.SendResult{Status: http.StatusNoContent}, nil
}

func TestHTTPWebhookLifecycleAndTenantIsolation(t *testing.T) {
	testServer := newWebhookHTTPServer(t)
	defer testServer.Close()

	capabilitiesResult := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/capabilities", "admin", "", "", nil)
	requireStatus(t, capabilitiesResult, http.StatusOK)
	var capabilities capabilitiesResponse
	decodeResult(t, capabilitiesResult, &capabilities)
	if capabilities.OptionalCapabilities.Webhooks != CapabilityEnabled {
		t.Fatalf("webhook capability state = %q", capabilities.OptionalCapabilities.Webhooks)
	}
	webhooksAdvertised := false
	for _, operation := range capabilities.Operations {
		webhooksAdvertised = webhooksAdvertised || operation == "webhooks.signed"
	}
	if !webhooksAdvertised {
		t.Fatal("capabilities omitted webhooks when the webhook service was configured")
	}

	forbidden := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/webhooks", "contributor", "webhook-forbidden", "", map[string]any{"name": "Denied", "url": "https://hooks.example.com/denied", "eventTypes": []string{"*"}})
	requireStatus(t, forbidden, http.StatusForbidden)

	createdResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/webhooks", "admin", "webhook-create", "", map[string]any{"name": "Catalog lifecycle", "url": "https://hooks.example.com/catalog", "eventTypes": []string{"resource.created"}})
	requireStatus(t, createdResult, http.StatusCreated)
	requireControlSchema(t, "WebhookSecretResult", createdResult.body)
	var created webhook.CreateResult
	decodeResult(t, createdResult, &created)
	if created.Secret == "" || created.Endpoint.Revision != 1 || created.Endpoint.SecretVersion != 1 || createdResult.header.Get("Location") != "/api/v1/webhooks/"+created.Endpoint.ID {
		t.Fatalf("unexpected webhook create: %#v, headers=%#v", created, createdResult.header)
	}

	getResult := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/webhooks/"+created.Endpoint.ID, "admin", "", "", nil)
	requireStatus(t, getResult, http.StatusOK)
	requireControlSchema(t, "Webhook", getResult.body)
	if string(getResult.body) == "" || containsJSONKey(getResult.body, "secret") {
		t.Fatalf("webhook read exposed a signing secret: %s", getResult.body)
	}
	otherTenant := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/webhooks/"+created.Endpoint.ID, "other-admin", "", "", nil)
	requireStatus(t, otherTenant, http.StatusNotFound)

	missingMatch := requestJSON(t, testServer.URL, http.MethodPatch, "/api/v1/webhooks/"+created.Endpoint.ID, "admin", "webhook-patch-no-match", "", map[string]any{"name": "Updated"})
	requireStatus(t, missingMatch, http.StatusPreconditionFailed)
	patchResult := requestJSON(t, testServer.URL, http.MethodPatch, "/api/v1/webhooks/"+created.Endpoint.ID, "admin", "webhook-patch", `"rev-1"`, map[string]any{"name": "Updated"})
	requireStatus(t, patchResult, http.StatusOK)
	requireControlSchema(t, "Webhook", patchResult.body)
	var endpoint webhook.Endpoint
	decodeResult(t, patchResult, &endpoint)
	if endpoint.Revision != 2 || endpoint.Name != "Updated" || patchResult.header.Get("ETag") != `"rev-2"` {
		t.Fatalf("unexpected patched webhook: %#v", endpoint)
	}

	staleTest := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/webhooks/"+created.Endpoint.ID+":test", "admin", "webhook-test-stale", `"rev-1"`, map[string]any{})
	requireStatus(t, staleTest, http.StatusPreconditionFailed)
	testResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/webhooks/"+created.Endpoint.ID+":test", "admin", "webhook-test", `"rev-2"`, map[string]any{})
	requireStatus(t, testResult, http.StatusOK)
	requireControlSchema(t, "WebhookDelivery", testResult.body)
	var delivery webhook.Delivery
	decodeResult(t, testResult, &delivery)
	if delivery.State != webhook.DeliverySucceeded || delivery.Attempt != 1 {
		t.Fatalf("unexpected test delivery: %#v", delivery)
	}
	deliveries := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/webhooks/"+created.Endpoint.ID+"/deliveries?limit=10", "admin", "", "", nil)
	requireStatus(t, deliveries, http.StatusOK)
	requireControlSchema(t, "WebhookDeliveryPage", deliveries.body)
	var page cursorPage[webhook.Delivery]
	decodeResult(t, deliveries, &page)
	if len(page.Items) != 1 || page.Items[0].ID != delivery.ID {
		t.Fatalf("unexpected deliveries: %#v", page)
	}

	rotateResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/webhooks/"+created.Endpoint.ID+":rotate-secret", "admin", "webhook-rotate", `"rev-2"`, map[string]any{})
	requireStatus(t, rotateResult, http.StatusOK)
	requireControlSchema(t, "WebhookSecretResult", rotateResult.body)
	var rotated webhook.CreateResult
	decodeResult(t, rotateResult, &rotated)
	if rotated.Secret == "" || rotated.Secret == created.Secret || rotated.Endpoint.Revision != 3 || rotated.Endpoint.SecretVersion != 2 {
		t.Fatalf("unexpected rotated secret result: %#v", rotated)
	}

	disableResult := requestJSON(t, testServer.URL, http.MethodDelete, "/api/v1/webhooks/"+created.Endpoint.ID, "admin", "webhook-disable", `"rev-3"`, nil)
	requireStatus(t, disableResult, http.StatusNoContent)
	staleDisable := requestJSON(t, testServer.URL, http.MethodDelete, "/api/v1/webhooks/"+created.Endpoint.ID, "admin", "webhook-disable-stale", `"rev-3"`, nil)
	requireStatus(t, staleDisable, http.StatusPreconditionFailed)
	listResult := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/webhooks", "admin", "", "", nil)
	requireStatus(t, listResult, http.StatusOK)
	requireControlSchema(t, "WebhookPage", listResult.body)
	var endpoints cursorPage[webhook.Endpoint]
	decodeResult(t, listResult, &endpoints)
	if len(endpoints.Items) != 1 || endpoints.Items[0].Status != webhook.StatusDisabled || endpoints.Items[0].Revision != 4 {
		t.Fatalf("unexpected webhook list: %#v", endpoints)
	}
}

func newWebhookHTTPServer(t *testing.T, senders ...webhook.Sender) *httptest.Server {
	t.Helper()
	return newConfiguredHTTPServer(t, nil, senders...)
}

func newConfiguredHTTPServer(t *testing.T, configure func(*Dependencies), senders ...webhook.Sender) *httptest.Server {
	t.Helper()
	serviceClock := clock.NewFake(time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC))
	repository := catalog.NewMemoryRepository()
	cursorKey := sha256.Sum256([]byte("webhook-http-test-key"))
	catalogService, err := catalog.NewService(catalog.Options{Repository: repository, Clock: serviceClock, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: cursorKey[:], Policy: "test-policy"})
	if err != nil {
		t.Fatal(err)
	}
	federationManager, err := federation.NewManager(federation.NewMemoryStore(), unavailableFetcher{}, serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	publicationStore := publication.NewMemoryStore()
	publicationManager, err := publication.NewManager(publication.Options{Records: catalogService, Store: publicationStore, Clock: serviceClock, IDs: id.Generator{}, BaseURL: "https://catalog.example.com", PublisherDomain: "catalog.example.com", ARDProfile: "ard-test", AICatalogProfile: "ai-test", CursorKey: cursorKey[:]})
	if err != nil {
		t.Fatal(err)
	}
	jobService, err := jobs.NewService(jobs.NewMemoryStore(), serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	webhookCipher, err := webhook.NewAESGCM(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	var sender webhook.Sender = successfulWebhookSender{}
	if len(senders) > 0 {
		sender = senders[0]
	}
	webhookService, err := webhook.NewService(webhook.NewMemoryStore(repository), webhookCipher, sender, serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	tenantID := "00000000-0000-7000-8000-000000000001"
	authenticator, identityService := newTestIdentity(t, serviceClock, map[string]config.DevelopmentUser{
		"admin":       {Subject: "admin", TenantID: tenantID, Roles: []identity.Role{identity.RoleTenantAdmin}},
		"contributor": {Subject: "contributor", TenantID: tenantID, Roles: []identity.Role{identity.RoleContributor}},
		"reader":      {Subject: "reader", TenantID: tenantID, Roles: []identity.Role{identity.RoleReader}},
		"reader-two":  {Subject: "reader-two", TenantID: tenantID, Roles: []identity.Role{identity.RoleReader}},
		"other-admin": {Subject: "other-admin", TenantID: "00000000-0000-7000-8000-000000000002", Roles: []identity.Role{identity.RoleTenantAdmin}},
	})
	feedbackService, err := feedback.NewService(feedback.NewMemoryStore(repository, repository), repository, serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	dependencies := Dependencies{PublicationOverview: publication.MemoryHostSummaries{"catalog.example.com": publicationStore}, PersonalStore: personal.NewMemoryStore(repository), RatingsStore: ratings.NewMemoryStore(repository), Feedback: feedbackService, Catalog: catalogService, Identity: identityService, Federation: federationManager, Jobs: jobService, Webhooks: webhookService, Authenticator: authenticator, Idempotency: idempotency.NewMemoryStore(), Logger: slog.New(slog.DiscardHandler), DiscoveryHosts: []DiscoveryHost{anonymousDiscoveryHost(publicationManager)}, MaxBodyBytes: 1 << 20, RequestTimeout: time.Second, ServiceVersion: "test", Commit: "test", ARDProfile: "ard-test", AICatalogProfile: "ai-test", CursorKey: cursorKey[:], Now: serviceClock.Now}
	if configure != nil {
		configure(&dependencies)
	}
	server, err := New(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(server.Handler())
}

func containsJSONKey(raw []byte, key string) bool {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return false
	}
	_, ok := object[key]
	return ok
}
