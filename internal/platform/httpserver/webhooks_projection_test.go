// Webhook handler tests use independent stored fixtures and a controlled sender;
// endpoint lifecycle workflows and actual delivery remain functional tests.
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

type webhookProjectionStore struct {
	webhook.Store
	get        func(context.Context, string, string) (webhook.Endpoint, error)
	create     func(context.Context, webhook.Endpoint, ...audit.Record) (webhook.Endpoint, error)
	update     func(context.Context, webhook.Endpoint, int64, ...audit.Record) (webhook.Endpoint, error)
	list       func(context.Context, string, webhook.EndpointListRequest) ([]webhook.Endpoint, error)
	deliveries func(context.Context, string, webhook.DeliveryListRequest, ...webhook.DeliveryReadEvidence) ([]webhook.Delivery, error)
	save       func(context.Context, webhook.Delivery, ...audit.Record) (webhook.Delivery, error)
	state      func(context.Context, string) (catalog.NotificationState, error)
}

func (s webhookProjectionStore) GetEndpoint(c context.Context, t, k string) (webhook.Endpoint, error) {
	return s.get(c, t, k)
}
func (s webhookProjectionStore) CreateEndpoint(c context.Context, e webhook.Endpoint, r ...audit.Record) (webhook.Endpoint, error) {
	return s.create(c, e, r...)
}
func (s webhookProjectionStore) UpdateEndpoint(c context.Context, e webhook.Endpoint, v int64, r ...audit.Record) (webhook.Endpoint, error) {
	return s.update(c, e, v, r...)
}
func (s webhookProjectionStore) ListEndpointPage(c context.Context, t string, p webhook.EndpointListRequest) ([]webhook.Endpoint, error) {
	return s.list(c, t, p)
}
func (s webhookProjectionStore) ListDeliveries(c context.Context, t string, p webhook.DeliveryListRequest, r ...webhook.DeliveryReadEvidence) ([]webhook.Delivery, error) {
	return s.deliveries(c, t, p, r...)
}
func (s webhookProjectionStore) SaveDelivery(c context.Context, d webhook.Delivery, r ...audit.Record) (webhook.Delivery, error) {
	return s.save(c, d, r...)
}
func (s webhookProjectionStore) NotificationState(c context.Context, t string) (catalog.NotificationState, error) {
	return s.state(c, t)
}

type webhookProjectionSender func(context.Context, webhook.SendRequest) (webhook.SendResult, error)

func (s webhookProjectionSender) Send(c context.Context, r webhook.SendRequest) (webhook.SendResult, error) {
	return s(c, r)
}
func webhookProjectionServer(t *testing.T, store webhookProjectionStore, cipher webhook.Cipher, sender webhookProjectionSender) *Server {
	t.Helper()
	service, err := webhook.NewService(store, cipher, sender, clock.NewFake(projectionTime), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	s := projectionServer(nil)
	s.webhooks = service
	return s
}
func projectionEndpoint(t *testing.T) (webhook.Endpoint, *webhook.AESGCM) {
	t.Helper()
	cipher, err := webhook.NewAESGCM(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"version":1,"current":"whsec_` + strings.Repeat("a", 43) + `"}`)
	encrypted, err := cipher.Encrypt(raw)
	if err != nil {
		t.Fatal(err)
	}
	return webhook.Endpoint{ID: projectionItem, TenantID: projectionTenant, Name: "Receiver", URL: "https://receiver.example/events", EventTypes: []string{"*"}, Status: webhook.StatusActive, Revision: 4, SecretVersion: 1, SecretCiphertext: encrypted, CreatedBy: "administrator", CreatedAt: projectionTime, UpdatedAt: projectionTime}, cipher
}
func requireEndpointProjection(t *testing.T, raw []byte, want webhook.Endpoint) {
	t.Helper()
	var got webhook.Endpoint
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want.SecretCiphertext = nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("endpoint=%+v expected=%+v", got, want)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 11 || strings.Contains(string(raw), "whsec_") {
		t.Fatalf("endpoint fields=%s", raw)
	}
}
func TestWebhookCommandProjection(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"get", "create", "patch", "disable", "rotate", "emergency rotation", "test"} {
		for _, failure := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failure=%t", action, failure), func(t *testing.T) {
				actor := projectionActor(identity.RoleTenantAdmin)
				ctx := identityfixture.WithActor(t.Context(), actor)
				endpoint, cipher := projectionEndpoint(t)
				expected := endpoint
				var saved webhook.Delivery
				reads, writes, sends := 0, 0, 0
				failureErr := catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-store")
				checkEvidence := func(e webhook.Endpoint, r []audit.Record, action string) {
					if len(r) != 1 || r[0].Actor != actor.MembershipID {
						t.Fatalf("endpoint evidence=%+v", r)
					}
					if err := webhook.ValidateEndpointEvidence(r, e, action); err != nil {
						t.Fatal(err)
					}
				}
				store := webhookProjectionStore{
					get: func(c context.Context, tenant, key string) (webhook.Endpoint, error) {
						reads++
						if !sameProjectionContext(c, ctx) || tenant != actor.TenantID || key != endpoint.ID {
							t.Fatal("webhook lookup binding")
						}
						if action == "get" && failure {
							return webhook.Endpoint{}, failureErr
						}
						return endpoint, nil
					},
					create: func(c context.Context, e webhook.Endpoint, r ...audit.Record) (webhook.Endpoint, error) {
						writes++
						if !sameProjectionContext(c, ctx) || e.TenantID != actor.TenantID || !identity.ValidTenantID(e.ID) || e.Revision != 1 || e.SecretVersion != 1 || e.Name != "New receiver" || e.URL != endpoint.URL || !reflect.DeepEqual(e.EventTypes, endpoint.EventTypes) || e.CreatedBy != actor.Subject {
							t.Fatalf("create handoff=%+v", e)
						}
						checkEvidence(e, r, audit.ActionWebhookCreated)
						expected = e
						if failure {
							return webhook.Endpoint{}, failureErr
						}
						return e, nil
					},
					update: func(c context.Context, e webhook.Endpoint, revision int64, r ...audit.Record) (webhook.Endpoint, error) {
						writes++
						if !sameProjectionContext(c, ctx) || e.TenantID != actor.TenantID || e.ID != endpoint.ID || revision != 4 || e.Revision != 5 {
							t.Fatalf("update binding=%+v revision=%d", e, revision)
						}
						expectedAction := audit.ActionWebhookUpdated
						switch action {
						case "patch":
							if e.Name != "Updated receiver" || e.URL != endpoint.URL || e.SecretVersion != endpoint.SecretVersion {
								t.Fatal("patch handoff")
							}
						case "disable":
							expectedAction = audit.ActionWebhookDisabled
							if e.Status != webhook.StatusDisabled {
								t.Fatal("disable handoff")
							}
						case "rotate", "emergency rotation":
							expectedAction = audit.ActionWebhookSecretRotated
							if e.SecretVersion != 2 {
								t.Fatal("rotation version")
							}
						default:
							t.Fatal("unexpected endpoint update")
						}
						checkEvidence(e, r, expectedAction)
						expected = e
						if failure {
							return webhook.Endpoint{}, failureErr
						}
						return e, nil
					},
					save: func(c context.Context, d webhook.Delivery, r ...audit.Record) (webhook.Delivery, error) {
						writes++
						if !sameProjectionContext(c, ctx) || d.TenantID != actor.TenantID || d.EndpointID != endpoint.ID || d.State != webhook.DeliverySucceeded || d.Attempt != 1 || d.ResponseStatus != 204 || d.EventType != events.CanonicalType("webhook.test") || !identity.ValidTenantID(d.EventID) || !identity.ValidTenantID(d.ID) {
							t.Fatalf("delivery handoff=%+v", d)
						}
						if len(r) != 1 {
							t.Fatal("missing delivery evidence")
						}
						if err := webhook.ValidateDeliveryEvidence(r, d); err != nil {
							t.Fatal(err)
						}
						saved = d
						if failure {
							return webhook.Delivery{}, failureErr
						}
						return d, nil
					},
				}
				sender := webhookProjectionSender(func(c context.Context, r webhook.SendRequest) (webhook.SendResult, error) {
					sends++
					if !sameProjectionContext(c, ctx) || r.TenantID != actor.TenantID || r.URL != endpoint.URL || r.Headers["X-Catalog-Webhook-ID"] != endpoint.ID || r.Headers["X-Catalog-Signature"] == "" || !json.Valid(r.Body) {
						t.Fatalf("test send handoff=%+v", r)
					}
					return webhook.SendResult{Status: 204}, nil
				})
				s := webhookProjectionServer(t, store, cipher, sender)
				body := ""
				method := http.MethodPost
				switch action {
				case "get":
					method = http.MethodGet
				case "create":
					body = `{"name":" New receiver ","url":"https://receiver.example/events","eventTypes":["*"]}`
				case "patch":
					method = http.MethodPatch
					body = `{"name":"Updated receiver"}`
				case "disable":
					method = http.MethodDelete
				case "emergency rotation":
					body = `{"revokePrevious":true}`
				case "test":
					body = `{}`
				}
				r := httptest.NewRequestWithContext(ctx, method, "/api/v1/webhooks/"+endpoint.ID, strings.NewReader(body))
				r.Header.Set("If-Match", `"rev-4"`)
				if body != "" {
					r.Header.Set("Content-Type", "application/json")
				}
				r.SetPathValue("webhookId", endpoint.ID)
				w := httptest.NewRecorder()
				switch action {
				case "get":
					s.handleWebhookGet(w, r)
				case "create":
					s.handleWebhookCreate(w, r)
				case "patch":
					s.handleWebhookPatch(w, r)
				case "disable":
					s.handleWebhookDelete(w, r)
				case "rotate", "emergency rotation":
					s.handleWebhookRotateSecret(w, r)
				case "test":
					s.handleWebhookTest(w, r)
				}
				wantReads, wantWrites, wantSends := 1, 1, 0
				if action == "get" {
					wantWrites = 0
				}
				if action == "create" {
					wantReads = 0
				}
				if action == "test" {
					wantSends = 1
				}
				if reads != wantReads || writes != wantWrites || sends != wantSends {
					t.Fatalf("calls read/write/send=%d/%d/%d; response=%d %s", reads, writes, sends, w.Code, w.Body)
				}
				if failure {
					requireProjectionProblem(t, w, http.StatusServiceUnavailable, catalog.CodeDependencyUnavailable)
					if w.Header().Get("Location") != "" || strings.Contains(w.Body.String(), "whsec_") {
						t.Fatal("failed command receipt leaked data")
					}
					return
				}
				status := 200
				if action == "create" {
					status = 201
				}
				if action == "disable" {
					status = 204
				}
				if w.Code != status {
					t.Fatalf("response=%d %s", w.Code, w.Body)
				}
				switch action {
				case "disable":
					if w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" {
						t.Fatal("disable response must have no body")
					}
				case "test":
					var got webhook.Delivery
					if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, saved) {
						t.Fatalf("delivery receipt=%+v expected=%+v", got, saved)
					}
				case "create", "rotate", "emergency rotation":
					var result struct {
						Endpoint                json.RawMessage `json:"endpoint"`
						Secret                  string          `json:"secret"`
						PreviousSecretExpiresAt *time.Time      `json:"previousSecretExpiresAt"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					requireEndpointProjection(t, result.Endpoint, expected)
					if len(result.Secret) != 49 || !strings.HasPrefix(result.Secret, "whsec_") {
						t.Fatal("secret receipt missing")
					}
					raw, err := cipher.Decrypt(expected.SecretCiphertext)
					if err != nil {
						t.Fatal(err)
					}
					if action == "create" {
						if string(raw) != result.Secret || result.PreviousSecretExpiresAt != nil || w.Header().Get("Location") != "/api/v1/webhooks/"+expected.ID {
							t.Fatal("create secret/Location")
						}
					} else {
						var material struct {
							Current       string     `json:"current"`
							Previous      string     `json:"previous"`
							PreviousUntil *time.Time `json:"previousUntil"`
						}
						if err := json.Unmarshal(raw, &material); err != nil {
							t.Fatal(err)
						}
						if material.Current != result.Secret {
							t.Fatal("rotation secret mismatch")
						}
						if action == "rotate" {
							until := projectionTime.Add(webhook.SigningOverlap)
							if result.PreviousSecretExpiresAt == nil || !result.PreviousSecretExpiresAt.Equal(until) || material.PreviousUntil == nil || !material.PreviousUntil.Equal(until) || material.Previous != "whsec_"+strings.Repeat("a", 43) {
								t.Fatal("rotation overlap receipt")
							}
						} else if result.PreviousSecretExpiresAt != nil || material.Previous != "" || material.PreviousUntil != nil {
							t.Fatal("emergency rotation retained predecessor")
						}
					}
				default:
					requireEndpointProjection(t, w.Body.Bytes(), expected)
				}
				if action != "disable" && action != "test" && w.Header().Get("ETag") != fmt.Sprintf(`"rev-%d"`, expected.Revision) {
					t.Fatal("webhook ETag")
				}
			})
		}
	}
}

func TestWebhookReadProjection(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"endpoints", "deliveries", "reconciliation"} {
		for _, failure := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failure=%t", kind, failure), func(t *testing.T) {
				ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleTenantAdmin))
				endpoint, cipher := projectionEndpoint(t)
				delivery := webhook.Delivery{ID: projectionNext, TenantID: projectionTenant, EndpointID: endpoint.ID, EventID: projectionResource, EventType: events.CanonicalType("webhook.test"), State: webhook.DeliverySucceeded, Attempt: 1, ResponseStatus: 204, CreatedAt: projectionTime, UpdatedAt: projectionTime}
				state := catalog.NotificationState{PendingEvents: 2, PendingEventLimit: catalog.PendingWebhookEventLimit, CapacityLossCount: 3, DeliveryLossCount: 4, LastDeliveryLossAt: &projectionTime}
				calls, gets := 0, 0
				check := func(c context.Context, tenant string) {
					calls++
					if !sameProjectionContext(c, ctx) || tenant != projectionTenant {
						t.Fatal("webhook read binding")
					}
				}
				failureErr := catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-store")
				store := webhookProjectionStore{
					get: func(c context.Context, tenant, key string) (webhook.Endpoint, error) {
						gets++
						if !sameProjectionContext(c, ctx) || tenant != projectionTenant || key != endpoint.ID {
							t.Fatal("delivery parent binding")
						}
						return endpoint, nil
					},
					state: func(c context.Context, tenant string) (catalog.NotificationState, error) {
						check(c, tenant)
						if failure {
							return catalog.NotificationState{}, failureErr
						}
						return state, nil
					},
					list: func(c context.Context, tenant string, p webhook.EndpointListRequest) ([]webhook.Endpoint, error) {
						check(c, tenant)
						if p.Limit != 2 {
							t.Fatalf("endpoint limit=%d", p.Limit)
						}
						if failure {
							return nil, failureErr
						}
						if calls == 1 {
							if p.AfterName != "" || p.AfterID != "" {
								t.Fatal("first endpoint position")
							}
							next := endpoint
							next.ID = projectionNext
							next.URL = "https://second-receiver.example/events"
							return []webhook.Endpoint{endpoint, next}, nil
						}
						if p.AfterName != endpoint.Name || p.AfterID != endpoint.ID {
							t.Fatalf("endpoint continuation=%+v", p)
						}
						return []webhook.Endpoint{}, nil
					},
					deliveries: func(c context.Context, tenant string, p webhook.DeliveryListRequest, e ...webhook.DeliveryReadEvidence) ([]webhook.Delivery, error) {
						check(c, tenant)
						if p.EndpointID != endpoint.ID || p.Limit != 2 || len(e) != 1 {
							t.Fatalf("delivery page binding=%+v evidence=%d", p, len(e))
						}
						if failure {
							return nil, failureErr
						}
						items := []webhook.Delivery{}
						if calls == 1 {
							if p.AfterUpdatedAt != nil || p.AfterID != "" {
								t.Fatal("first delivery position")
							}
							next := delivery
							next.ID = projectionResource
							next.EventID = "00000000-0000-7000-8000-000000000021"
							next.UpdatedAt = projectionTime.Add(-time.Second)
							next.CreatedAt = next.UpdatedAt
							items = []webhook.Delivery{delivery, next}
						} else if p.AfterID != delivery.ID || p.AfterUpdatedAt == nil || !p.AfterUpdatedAt.Equal(delivery.UpdatedAt) {
							t.Fatalf("delivery continuation=%+v", p)
						}
						evidence, err := e[0](items)
						if err != nil {
							t.Fatal(err)
						}
						if err := audit.ValidateNewRecord(evidence); err != nil {
							t.Fatal(err)
						}
						visible := items[:min(len(items), 1)]
						raw, err := json.Marshal(visible)
						if err != nil {
							t.Fatal(err)
						}
						facts, ok := evidence.Details["facts"].(map[string]any)
						if !ok {
							t.Fatal("missing delivery read facts")
						}
						if evidence.Action != audit.ActionWebhookDeliveriesRead || evidence.TargetID != endpoint.ID || facts["deliveriesDigest"] != audit.DigestBytes(raw) {
							t.Fatalf("delivery read evidence=%+v", evidence)
						}
						return items, nil
					},
				}
				s := webhookProjectionServer(t, store, cipher, nil)
				cursor := ""
				pages := 2
				if failure || kind == "reconciliation" {
					pages = 1
				}
				for page := 0; page < pages; page++ {
					r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/webhooks?limit=1"+cursor, nil)
					r.SetPathValue("webhookId", endpoint.ID)
					w := httptest.NewRecorder()
					switch kind {
					case "endpoints":
						s.handleWebhooksList(w, r)
					case "deliveries":
						s.handleWebhookDeliveries(w, r)
					case "reconciliation":
						s.handleWebhookReconciliation(w, r)
					}
					if failure {
						requireProjectionProblem(t, w, http.StatusServiceUnavailable, catalog.CodeDependencyUnavailable)
						break
					}
					if w.Code != 200 {
						t.Fatalf("read response=%d %s", w.Code, w.Body)
					}
					if kind == "reconciliation" {
						var got catalog.NotificationState
						if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(got, state) {
							t.Fatalf("reconciliation=%+v", got)
						}
						continue
					}
					var got struct {
						Items      []json.RawMessage `json:"items"`
						NextCursor string            `json:"nextCursor"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if got.Items == nil {
						t.Fatal("null items")
					}
					if page == 0 {
						if len(got.Items) != 1 || got.NextCursor == "" {
							t.Fatalf("first page=%s", w.Body)
						}
						cursor = "&cursor=" + got.NextCursor
						if kind == "endpoints" {
							requireEndpointProjection(t, got.Items[0], endpoint)
						} else {
							var d webhook.Delivery
							if err := json.Unmarshal(got.Items[0], &d); err != nil {
								t.Fatal(err)
							}
							if !reflect.DeepEqual(d, delivery) {
								t.Fatalf("delivery page=%+v", d)
							}
						}
					} else if len(got.Items) != 0 || got.NextCursor != "" {
						t.Fatalf("last page=%s", w.Body)
					}
				}
				wantGets := 0
				if kind == "deliveries" {
					wantGets = pages
				}
				if calls != pages || gets != wantGets {
					t.Fatalf("read calls=%d gets=%d", calls, gets)
				}
			})
		}
	}
}
