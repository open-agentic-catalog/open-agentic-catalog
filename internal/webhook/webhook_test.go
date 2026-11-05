package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
)

type sequenceIDs struct {
	mu   sync.Mutex
	next int
}

func (s *sequenceIDs) New(time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	return fmt.Sprintf("00000000-0000-7000-8000-%012d", s.next), nil
}

type recordingSender struct {
	mu       sync.Mutex
	requests []SendRequest
	results  []SendResult
	errors   []error
}

func (s *recordingSender) Send(_ context.Context, request SendRequest) (SendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, request)
	index := len(s.requests) - 1
	var result SendResult
	if index < len(s.results) {
		result = s.results[index]
	}
	if result.Status == 0 {
		result.Status = 204
	}
	if index < len(s.errors) && s.errors[index] != nil {
		return SendResult{}, s.errors[index]
	}
	return result, nil
}

func (s *recordingSender) Requests() []SendRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SendRequest(nil), s.requests...)
}

type memoryOutbox struct {
	evidence   EvidenceSink
	expiries   map[string]time.Time
	mu         sync.Mutex
	events     []catalog.OutboxEvent
	published  map[string]time.Time
	leases     map[string]string
	claimErr   error
	renewErr   error
	publishErr error
}

func (m *memoryOutbox) ClaimOutbox(_ context.Context, owner string, now time.Time, lease time.Duration, limit int) ([]catalog.OutboxEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claimErr != nil {
		return nil, m.claimErr
	}
	if m.leases == nil {
		m.leases = make(map[string]string)
	}
	if m.expiries == nil {
		m.expiries = make(map[string]time.Time)
	}
	items := make([]catalog.OutboxEvent, 0, limit)
	for _, event := range m.events {
		_, published := m.published[event.ID]
		_, leased := m.leases[event.ID]
		if !published && !leased && (event.DispatchAfter == nil || !event.DispatchAfter.After(now)) {
			m.leases[event.ID] = owner
			m.expiries[event.ID] = now.Add(lease)
			items = append(items, event)
			if len(items) == limit {
				break
			}
		}
	}
	return items, nil
}

func (m *memoryOutbox) MarkOutboxPublished(_ context.Context, tenantID, eventID, owner string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.publishErr != nil {
		return m.publishErr
	}
	if !m.eventBelongsToTenant(eventID, tenantID) || m.leases[eventID] != owner {
		return errors.New("lease not owned")
	}
	if m.published == nil {
		m.published = make(map[string]time.Time)
	}
	m.published[eventID] = at
	delete(m.leases, eventID)
	return nil
}

func (m *memoryOutbox) RenewOutbox(_ context.Context, tenantID, eventID, owner string, now time.Time, lease time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.renewErr != nil {
		return m.renewErr
	}
	if !m.eventBelongsToTenant(eventID, tenantID) || m.leases[eventID] != owner {
		return errors.New("lease not owned")
	}
	m.expiries[eventID] = now.Add(lease)
	return nil
}

func (m *memoryOutbox) ReleaseOutbox(_ context.Context, tenantID, eventID, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.eventBelongsToTenant(eventID, tenantID) || m.leases[eventID] != owner {
		return errors.New("lease not owned")
	}
	delete(m.leases, eventID)
	return nil
}

func (m *memoryOutbox) DeferOutbox(ctx context.Context, tenantID, eventID, owner string, _ time.Time, notBefore time.Time) error {
	if err := m.ReleaseOutbox(ctx, tenantID, eventID, owner); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.events {
		if m.events[i].TenantID == tenantID && m.events[i].ID == eventID {
			m.events[i].DispatchAfter = &notBefore
			return nil
		}
	}
	return errors.New("missing outbox event")
}

func (m *memoryOutbox) eventBelongsToTenant(eventID, tenantID string) bool {
	for _, event := range m.events {
		if event.ID == eventID && event.TenantID == tenantID {
			return true
		}
	}
	return false
}

func adminContext(tenant string) context.Context {
	return identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenant, Subject: "admin", MembershipID: "00000000-0000-7000-8000-000000000501", RealmID: "00000000-0000-7000-8000-000000000502", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}})
}

func TestAESGCMRoundTripAndTamperDetection(t *testing.T) {
	cipher, err := NewAESGCM(keyBytes(7))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := cipher.Encrypt([]byte("whsec_example"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := cipher.Decrypt(ciphertext)
	if err != nil || string(plaintext) != "whsec_example" {
		t.Fatalf("decrypt = %q, %v", plaintext, err)
	}
	ciphertext[len(ciphertext)-1] ^= 1
	if _, err := cipher.Decrypt(ciphertext); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
	if _, err := cipher.Decrypt([]byte("short")); err == nil {
		t.Fatal("truncated ciphertext was accepted")
	}
	for _, size := range []int{0, 16, 31, 33} {
		if _, err := NewAESGCM(make([]byte, size)); err == nil {
			t.Fatalf("%d-byte encryption key was accepted", size)
		}
	}
}

func TestWebhookEndpointRejectsCredentialURLs(t *testing.T) {
	for _, endpointURL := range []string{
		"https://hooks.example.com/catalog?access_token=secret",
		"https://hooks.example.com/catalog?X-Amz-Signature=secret",
		"https://hooks.example.com:0/catalog",
		"https://hooks.ex\u00e4mple.com/catalog",
		"https://hooks.example.com/" + strings.Repeat("x", 2048),
	} {
		if _, _, _, err := validateEndpointInput("Catalog events", endpointURL, []string{"resource.created"}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("unsafe webhook URL %q returned %v", endpointURL, err)
		}
	}
}

func TestServiceLifecycleSigningAndOptimisticConcurrency(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	serviceClock := clock.NewFake(now)
	store := NewMemoryStore()
	cipher, _ := NewAESGCM(keyBytes(9))
	sender := &recordingSender{}
	service, err := NewService(store, cipher, sender, serviceClock, &sequenceIDs{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := adminContext("00000000-0000-7000-8000-000000000001")
	created, err := service.Create(ctx, CreateInput{Name: "Catalog events", URL: "https://hooks.example.com/catalog", EventTypes: []string{"resource.created", "*", "resource.created"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Secret, "whsec_") || created.Endpoint.Revision != 1 || created.Endpoint.SecretVersion != 1 || !slices.Equal(created.Endpoint.EventTypes, []string{"*", "com.open.agentic.catalog.resource.created.v1"}) {
		t.Fatalf("unexpected create result: %#v", created)
	}
	serialized, err := json.Marshal(created.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "SecretCiphertext") || strings.Contains(string(serialized), base64.StdEncoding.EncodeToString(created.Endpoint.SecretCiphertext)) {
		t.Fatalf("encrypted secret leaked from endpoint JSON: %s", serialized)
	}
	name := "Catalog lifecycle events"
	updated, err := service.Update(ctx, created.Endpoint.ID, PatchInput{Name: &name}, 1)
	if err != nil || updated.Revision != 2 || updated.Name != name {
		t.Fatalf("update = %#v, %v", updated, err)
	}
	if _, err := service.Update(ctx, created.Endpoint.ID, PatchInput{Name: &name}, 1); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale update returned %v", err)
	}
	if _, err := service.Test(ctx, created.Endpoint.ID, 1); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale test returned %v", err)
	}
	delivery, err := service.Test(ctx, created.Endpoint.ID, 2)
	if err != nil || delivery.State != DeliverySucceeded || delivery.Attempt != 1 {
		t.Fatalf("test delivery = %#v, %v", delivery, err)
	}
	requests := sender.Requests()
	if len(requests) != 1 {
		t.Fatalf("sent %d requests", len(requests))
	}
	verifySignature(t, created.Secret, 1, created.Endpoint.ID, delivery.ID, requests[0])
	rotated, err := service.RotateSecret(ctx, created.Endpoint.ID, 2, RotateInput{})
	if err != nil || rotated.Endpoint.Revision != 3 || rotated.Endpoint.SecretVersion != 2 || rotated.Secret == created.Secret {
		t.Fatalf("rotate = %#v, %v", rotated, err)
	}
	rotatedDelivery, err := service.Test(ctx, created.Endpoint.ID, 3)
	if err != nil || rotatedDelivery.State != DeliverySucceeded {
		t.Fatalf("rotated test delivery = %#v, %v", rotatedDelivery, err)
	}
	requests = sender.Requests()
	if len(requests) != 2 {
		t.Fatalf("sent %d requests after rotation", len(requests))
	}
	verifySignature(t, rotated.Secret, 2, created.Endpoint.ID, rotatedDelivery.ID, requests[1])
	disabled, err := service.Disable(ctx, created.Endpoint.ID, 3)
	if err != nil || disabled.Status != StatusDisabled || disabled.Revision != 4 {
		t.Fatalf("disable = %#v, %v", disabled, err)
	}
	if _, err := service.Disable(ctx, created.Endpoint.ID, 3); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale idempotent disable returned %v", err)
	}
	if _, err := service.Test(ctx, created.Endpoint.ID, 4); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("disabled endpoint test returned %v", err)
	}
	if _, err := service.List(context.Background(), EndpointListRequest{Limit: 20}); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("unauthorized list returned %v", err)
	}
}

func TestDispatcherRetriesFiltersAndAcknowledgesOutbox(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	serviceClock := clock.NewFake(now)
	store := NewMemoryStore()
	cipher, _ := NewAESGCM(keyBytes(5))
	sender := &recordingSender{results: []SendResult{{Status: 503}, {Status: 204}}}
	ids := &sequenceIDs{}
	service, _ := NewService(store, cipher, sender, serviceClock, ids)
	ctx := adminContext("00000000-0000-7000-8000-000000000001")
	endpoint, err := service.Create(ctx, CreateInput{Name: "Resource events", URL: "https://hooks.example.com/resources", EventTypes: []string{"resource.created"}})
	if err != nil {
		t.Fatal(err)
	}
	oldEvent := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000100", TenantID: "00000000-0000-7000-8000-000000000001", Type: catalog.CanonicalEventType("resource.created"), Subject: "00000000-0000-7000-8000-000000000110", Data: testPublicReference(t, "resource", "00000000-0000-7000-8000-000000000110"), CreatedAt: now.Add(-time.Second)}
	matchingEvent := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000101", TenantID: "00000000-0000-7000-8000-000000000001", Type: catalog.CanonicalEventType("resource.created"), Subject: "00000000-0000-7000-8000-000000000111", Data: testPublicReference(t, "resource", "00000000-0000-7000-8000-000000000111"), CreatedAt: now.Add(time.Second)}
	unmatchedEvent := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000102", TenantID: "00000000-0000-7000-8000-000000000001", Type: catalog.CanonicalEventType("publisher.created"), Subject: "00000000-0000-7000-8000-000000000112", Data: testPublicReference(t, "publisher", "00000000-0000-7000-8000-000000000112"), CreatedAt: now.Add(time.Second)}
	outbox := &memoryOutbox{events: []catalog.OutboxEvent{oldEvent, matchingEvent, unmatchedEvent}, published: make(map[string]time.Time)}
	dispatcher, err := newTestDispatcher(outbox, store, cipher, sender, serviceClock, ids, "dispatcher-one", time.Minute, 3, time.Second, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := dispatcher.DispatchOnce(context.Background())
	if err != nil || completed != 1 {
		t.Fatalf("first dispatch = %d, %v", completed, err)
	}
	completed, err = dispatcher.DispatchOnce(context.Background())
	if err != nil || completed != 0 {
		t.Fatalf("matching dispatch = %d, %v", completed, err)
	}
	delivery, err := store.GetDelivery(context.Background(), "00000000-0000-7000-8000-000000000001", endpoint.Endpoint.ID, matchingEvent.ID)
	if err != nil || delivery.State != DeliveryWaiting || delivery.Attempt != 1 || delivery.NextAttemptAt == nil {
		t.Fatalf("waiting delivery = %#v, %v", delivery, err)
	}
	if _, ok := outbox.published[matchingEvent.ID]; ok {
		t.Fatal("waiting event was acknowledged")
	}
	completed, err = dispatcher.DispatchOnce(context.Background())
	if err != nil || completed != 1 || len(sender.Requests()) != 1 {
		t.Fatalf("premature retry = %d, requests %d, %v", completed, len(sender.Requests()), err)
	}
	if _, ok := outbox.published[matchingEvent.ID]; ok {
		t.Fatal("deferred retry was acknowledged before its next attempt")
	}
	serviceClock.Advance(5 * time.Second)
	completed, err = dispatcher.DispatchOnce(context.Background())
	if err != nil || completed != 1 {
		t.Fatalf("second dispatch = %d, %v", completed, err)
	}
	delivery, _ = store.GetDelivery(context.Background(), "00000000-0000-7000-8000-000000000001", endpoint.Endpoint.ID, matchingEvent.ID)
	if delivery.State != DeliverySucceeded || delivery.Attempt != 2 {
		t.Fatalf("retried delivery = %#v", delivery)
	}
	if completed, err = dispatcher.DispatchOnce(context.Background()); err != nil || completed != 0 {
		t.Fatalf("unmatched dispatch = %d, %v", completed, err)
	}
	if len(outbox.published) != 3 {
		t.Fatalf("published outbox events = %#v", outbox.published)
	}
}

func TestDispatcherDeadLettersAfterBoundedAttempts(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	serviceClock := clock.NewFake(now)
	store := NewMemoryStore()
	cipher, _ := NewAESGCM(keyBytes(3))
	sender := &recordingSender{errors: []error{errors.New("offline"), errors.New("offline")}}
	ids := &sequenceIDs{}
	service, _ := NewService(store, cipher, sender, serviceClock, ids)
	ctx := adminContext("00000000-0000-7000-8000-000000000001")
	endpoint, _ := service.Create(ctx, CreateInput{Name: "All events", URL: "https://hooks.example.com/all", EventTypes: []string{"*"}})
	event := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000200", TenantID: "00000000-0000-7000-8000-000000000001", Type: catalog.CanonicalEventType("resource.created"), Subject: "00000000-0000-7000-8000-000000000201", Data: testPublicReference(t, "resource", "00000000-0000-7000-8000-000000000201"), CreatedAt: now.Add(time.Second)}
	outbox := &memoryOutbox{events: []catalog.OutboxEvent{event}, published: make(map[string]time.Time)}
	dispatcher, _ := newTestDispatcher(outbox, store, cipher, sender, serviceClock, ids, "dispatcher-one", time.Minute, 2, time.Second, time.Hour)
	if completed, err := dispatcher.DispatchOnce(context.Background()); err != nil || completed != 0 {
		t.Fatalf("first dispatch = %d, %v", completed, err)
	}
	serviceClock.Advance(5 * time.Second)
	if completed, err := dispatcher.DispatchOnce(context.Background()); err != nil || completed != 1 {
		t.Fatalf("second dispatch = %d, %v", completed, err)
	}
	delivery, _ := store.GetDelivery(context.Background(), "00000000-0000-7000-8000-000000000001", endpoint.Endpoint.ID, event.ID)
	if delivery.State != DeliveryDeadLetter || delivery.Attempt != 2 || delivery.NextAttemptAt != nil {
		t.Fatalf("dead-letter delivery = %#v", delivery)
	}
}

func TestDispatcherNeverDeliversInternalOrUnregisteredEventsToWildcardEndpoints(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	serviceClock := clock.NewFake(now)
	store := NewMemoryStore()
	cipher, _ := NewAESGCM(keyBytes(4))
	sender := &recordingSender{}
	ids := &sequenceIDs{}
	service, _ := NewService(store, cipher, sender, serviceClock, ids)
	tenantID := "00000000-0000-7000-8000-000000000001"
	if _, err := service.Create(adminContext(tenantID), CreateInput{Name: "Public events", URL: "https://hooks.example.com/public", EventTypes: []string{"*"}}); err != nil {
		t.Fatal(err)
	}
	events := []catalog.OutboxEvent{
		{ID: "00000000-0000-7000-8000-000000000401", TenantID: tenantID, Type: catalog.CanonicalEventType("resource.created"), Subject: "00000000-0000-7000-8000-000000000411", Internal: true, CreatedAt: now},
		{ID: "00000000-0000-7000-8000-000000000402", TenantID: tenantID, Type: "com.open.agentic.catalog.internal.repair.v1", Subject: "00000000-0000-7000-8000-000000000412", CreatedAt: now},
	}
	outbox := &memoryOutbox{events: events, published: make(map[string]time.Time)}
	dispatcher, err := newTestDispatcher(outbox, store, cipher, sender, serviceClock, ids, "dispatcher-one", time.Minute, 3, time.Second, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for range events {
		if completed, err := dispatcher.DispatchOnce(context.Background()); err != nil || completed != 1 {
			t.Fatalf("internal event drain = %d, %v", completed, err)
		}
	}
	if len(outbox.published) != len(events) || len(sender.Requests()) != 0 || len(store.deliveries) != 0 {
		t.Fatalf("internal event isolation: published=%#v requests=%d deliveries=%#v", outbox.published, len(sender.Requests()), store.deliveries)
	}
}

func TestDisabledWebhookDrainerAcknowledgesWithoutDeliveryState(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	serviceClock := clock.NewFake(now)
	events := []catalog.OutboxEvent{
		{ID: "00000000-0000-7000-8000-000000000301", TenantID: "00000000-0000-7000-8000-000000000001", Type: catalog.CanonicalEventType("resource.created"), CreatedAt: now},
		{ID: "00000000-0000-7000-8000-000000000302", TenantID: "00000000-0000-7000-8000-000000000001", Type: catalog.CanonicalEventType("publisher.created"), CreatedAt: now},
	}
	outbox := &memoryOutbox{events: events, published: make(map[string]time.Time)}
	drainer, err := NewDrainer(outbox, serviceClock, "disabled-webhook-worker", time.Minute, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := drainer.DrainOnce(context.Background())
	if err != nil || completed != len(events) || len(outbox.published) != len(events) {
		t.Fatalf("drain = %d, published=%#v, error=%v", completed, outbox.published, err)
	}
	if completed, err := drainer.DrainOnce(context.Background()); err != nil || completed != 0 {
		t.Fatalf("second drain = %d, %v", completed, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := drainer.Run(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled drainer returned %v", err)
	}
}

func TestDisabledWebhookDrainerPropagatesOutboxFailures(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	serviceClock := clock.NewFake(now)
	event := catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000303", TenantID: "00000000-0000-7000-8000-000000000001", Type: catalog.CanonicalEventType("resource.created"), CreatedAt: now}
	failure := errors.New("outbox unavailable")
	for name, outbox := range map[string]*memoryOutbox{
		"claim":   {events: []catalog.OutboxEvent{event}, published: make(map[string]time.Time), claimErr: failure},
		"renew":   {events: []catalog.OutboxEvent{event}, published: make(map[string]time.Time), renewErr: failure},
		"publish": {events: []catalog.OutboxEvent{event}, published: make(map[string]time.Time), publishErr: failure},
	} {
		t.Run(name, func(t *testing.T) {
			drainer, err := NewDrainer(outbox, serviceClock, "disabled-webhook-worker", time.Minute, time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			completed, err := drainer.DrainOnce(context.Background())
			if !errors.Is(err, failure) || completed != 0 || len(outbox.published) != 0 {
				t.Fatalf("failed drain = %d, published=%#v, error=%v", completed, outbox.published, err)
			}
		})
	}
}

func verifySignature(t *testing.T, secret string, secretVersion int, endpointID, deliveryID string, request SendRequest) {
	t.Helper()
	if request.Headers["X-Catalog-Webhook-ID"] != endpointID || request.Headers["X-Catalog-Delivery-ID"] != deliveryID {
		t.Fatalf("webhook identifiers = endpoint %q, delivery %q", request.Headers["X-Catalog-Webhook-ID"], request.Headers["X-Catalog-Delivery-ID"])
	}
	timestamp := request.Headers["X-Catalog-Timestamp"]
	if timestamp == "" {
		t.Fatal("webhook timestamp is empty")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%s.", timestamp)
	_, _ = mac.Write(request.Body)
	want := fmt.Sprintf("v1,kid=%d,sha256=%s", secretVersion, hex.EncodeToString(mac.Sum(nil)))
	if !hmac.Equal([]byte(request.Headers["X-Catalog-Signature"]), []byte(want)) {
		t.Fatalf("signature = %q, want %q", request.Headers["X-Catalog-Signature"], want)
	}
	var event map[string]any
	if err := json.Unmarshal(request.Body, &event); err != nil || event["specversion"] != "1.0" || event["id"] != request.Headers["Ce-Id"] || event["id"] == deliveryID {
		t.Fatalf("invalid CloudEvent body: %s, %v", request.Body, err)
	}
}

func keyBytes(value byte) []byte {
	result := make([]byte, 32)
	for index := range result {
		result[index] = value
	}
	return result
}

func TestWebhookNameCharacterBounds(t *testing.T) {
	for _, count := range []int{0, 159, 160, 161} {
		name := strings.Repeat("🙂", count)
		if count == 0 {
			name = "source\x00name"
		}
		got, _, _, err := validateEndpointInput(name, "https://receiver.example/events", []string{"resource.created"})
		if count > 0 && count <= 160 {
			if err != nil || got != name {
				t.Fatalf("valid name changed: %q, %v", got, err)
			}
		} else if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("oversized name: %v", err)
		}
	}
}
