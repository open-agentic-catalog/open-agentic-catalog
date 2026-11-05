// Webhook administration enforces tenant authority, endpoint admission and
// secret lifecycle rules; test sends use the same signing and sender contracts
// as dispatch.

package webhook

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type Clock interface{ Now() time.Time }
type IDGenerator interface {
	New(time.Time) (string, error)
}
type Sender interface {
	Send(context.Context, SendRequest) (SendResult, error)
}

// Service manages tenant webhook endpoints and write-only signing secrets.
type Service struct {
	store    Store
	cipher   Cipher
	sender   Sender
	clock    Clock
	ids      IDGenerator
	testBusy atomic.Bool
}

// NewService requires the endpoint store, authenticated cipher, sender, clock
// and ID generator, with at most one attempt observer.
func NewService(store Store, cipher Cipher, sender Sender, clock Clock, ids IDGenerator, observers ...AttemptObserver) (*Service, error) {
	if len(observers) > 1 || len(observers) == 1 && observers[0] == nil {
		return nil, errors.New("at most one non-nil webhook attempt observer is supported")
	}
	if store == nil || cipher == nil || sender == nil || clock == nil || ids == nil {
		return nil, errors.New("webhook service dependencies are required")
	}
	if len(observers) == 1 {
		sender = observedSender{sender: sender, observer: observers[0], mode: "test"}
	}
	return &Service{store: store, cipher: cipher, sender: sender, clock: clock, ids: ids}, nil
}

// Create registers an administrator-owned endpoint and returns the newly
// generated signing secret alongside the persisted endpoint.
func (s *Service) Create(ctx context.Context, input CreateInput) (CreateResult, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "webhook.manage")
	if err != nil {
		return CreateResult{}, err
	}
	name, endpointURL, eventTypes, err := validateEndpointInput(input.Name, input.URL, input.EventTypes)
	if err != nil {
		return CreateResult{}, err
	}
	now := s.clock.Now()
	idValue, err := s.ids.New(now)
	if err != nil {
		return CreateResult{}, catalog.Wrap(catalog.CodeInternal, "create webhook identifier", err)
	}
	secret, encrypted, err := s.newSecret()
	if err != nil {
		return CreateResult{}, err
	}
	endpoint := Endpoint{ID: idValue, TenantID: actor.TenantID, Name: name, URL: endpointURL, EventTypes: eventTypes, Status: StatusActive, Revision: 1, SecretVersion: 1, SecretCiphertext: encrypted, CreatedBy: actor.Subject, CreatedAt: now, UpdatedAt: now}
	evidence, err := s.endpointAudit(ctx, endpoint, audit.ActionWebhookCreated, false)
	if err != nil {
		return CreateResult{}, err
	}
	endpoint, err = s.store.CreateEndpoint(ctx, endpoint, evidence)
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Endpoint: endpoint, Secret: secret}, nil
}

// Get returns an endpoint only within the authenticated administrator's tenant.
func (s *Service) Get(ctx context.Context, endpointID string) (Endpoint, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "webhook.read")
	if err != nil {
		return Endpoint{}, err
	}
	return s.store.GetEndpoint(ctx, actor.TenantID, endpointID)
}

// List validates administrator pagination and returns an endpoint page with a
// possible continuation sentinel.
func (s *Service) List(ctx context.Context, request EndpointListRequest) ([]Endpoint, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "webhook.read")
	if err != nil {
		return nil, err
	}
	if request.Limit < 1 || request.Limit > 100 || request.AfterName == "" && request.AfterID != "" || request.AfterName != "" && !identity.ValidTenantID(request.AfterID) {
		return nil, catalog.E(catalog.CodeInvalidArgument, "webhook page position or limit is invalid")
	}
	if request.Name != "" && (!utf8.ValidString(request.Name) || strings.TrimSpace(request.Name) != request.Name || strings.ContainsRune(request.Name, 0) || utf8.RuneCountInString(request.Name) > 160) || request.Status != "" && request.Status != StatusActive && request.Status != StatusDisabled {
		return nil, catalog.E(catalog.CodeInvalidArgument, "webhook name or status filter is invalid")
	}
	request.Limit++
	return s.store.ListEndpointPage(ctx, actor.TenantID, request)
}

// Update applies nonempty endpoint changes under the expected revision and
// retains matching audit evidence.
func (s *Service) Update(ctx context.Context, endpointID string, input PatchInput, expectedRevision int64) (Endpoint, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "webhook.manage")
	if err != nil {
		return Endpoint{}, err
	}
	endpoint, err := s.store.GetEndpoint(ctx, actor.TenantID, endpointID)
	if err != nil {
		return Endpoint{}, err
	}
	if endpoint.Revision != expectedRevision {
		return Endpoint{}, catalog.E(catalog.CodePreconditionFailed, "webhook revision does not match")
	}
	if input.Name == nil && input.URL == nil && input.EventTypes == nil {
		return Endpoint{}, catalog.E(catalog.CodeInvalidArgument, "webhook patch contains no changes")
	}
	name := endpoint.Name
	endpointURL := endpoint.URL
	eventTypes := endpoint.EventTypes
	if input.Name != nil {
		name = *input.Name
	}
	if input.URL != nil {
		endpointURL = *input.URL
	}
	if input.EventTypes != nil {
		eventTypes = *input.EventTypes
	}
	name, endpointURL, eventTypes, err = validateEndpointInput(name, endpointURL, eventTypes)
	if err != nil {
		return Endpoint{}, err
	}
	if name == endpoint.Name && endpointURL == endpoint.URL && slices.Equal(eventTypes, endpoint.EventTypes) {
		return Endpoint{}, catalog.E(catalog.CodeInvalidArgument, "webhook patch contains no changes")
	}
	endpoint.Name = name
	endpoint.URL = endpointURL
	endpoint.EventTypes = eventTypes
	endpoint.Revision++
	endpoint.UpdatedAt = s.clock.Now()
	evidence, err := s.endpointAudit(ctx, endpoint, audit.ActionWebhookUpdated, false)
	if err != nil {
		return Endpoint{}, err
	}
	return s.store.UpdateEndpoint(ctx, endpoint, expectedRevision, evidence)
}

// Disable prevents future eligible sends under the expected revision; an
// already disabled endpoint is returned unchanged.
func (s *Service) Disable(ctx context.Context, endpointID string, expectedRevision int64) (Endpoint, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "webhook.manage")
	if err != nil {
		return Endpoint{}, err
	}
	endpoint, err := s.store.GetEndpoint(ctx, actor.TenantID, endpointID)
	if err != nil {
		return Endpoint{}, err
	}
	if endpoint.Revision != expectedRevision {
		return Endpoint{}, catalog.E(catalog.CodePreconditionFailed, "webhook revision does not match")
	}
	if endpoint.Status == StatusDisabled {
		return endpoint, nil
	}
	endpoint.Status = StatusDisabled
	endpoint.Revision++
	endpoint.UpdatedAt = s.clock.Now()
	evidence, err := s.endpointAudit(ctx, endpoint, audit.ActionWebhookDisabled, false)
	if err != nil {
		return Endpoint{}, err
	}
	return s.store.UpdateEndpoint(ctx, endpoint, expectedRevision, evidence)
}

// RotateSecret replaces an active endpoint's secret under the expected
// revision. Routine rotation retains one predecessor for SigningOverlap;
// emergency rotation revokes it.
func (s *Service) RotateSecret(ctx context.Context, endpointID string, expectedRevision int64, input RotateInput) (CreateResult, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "webhook.rotate")
	if err != nil {
		return CreateResult{}, err
	}
	endpoint, err := s.store.GetEndpoint(ctx, actor.TenantID, endpointID)
	if err != nil {
		return CreateResult{}, err
	}
	if endpoint.Revision != expectedRevision {
		return CreateResult{}, catalog.E(catalog.CodePreconditionFailed, "webhook revision does not match")
	}
	if endpoint.Status != StatusActive {
		return CreateResult{}, catalog.E(catalog.CodePreconditionFailed, "only an active webhook can rotate its secret")
	}
	now := s.clock.Now()
	material, err := readSigningMaterial(s.cipher, endpoint.SecretCiphertext)
	if err != nil {
		return CreateResult{}, catalog.Wrap(catalog.CodeInternal, "webhook signing material is unavailable", err)
	}
	if !input.RevokePrevious && material.PreviousUntil != nil && material.PreviousUntil.After(now) {
		return CreateResult{}, catalog.E(catalog.CodeConflict, "webhook signing overlap is still active")
	}
	secret, _, err := s.newSecret()
	if err != nil {
		return CreateResult{}, err
	}
	nextMaterial := signingMaterial{Version: 1, Current: secret}
	if !input.RevokePrevious {
		expires := now.Add(SigningOverlap)
		nextMaterial.Previous, nextMaterial.PreviousUntil = material.Current, &expires
	}
	raw, err := json.Marshal(nextMaterial)
	if err != nil {
		return CreateResult{}, catalog.Wrap(catalog.CodeInternal, "encode webhook signing material", err)
	}
	encrypted, err := s.cipher.Encrypt(raw)
	if err != nil {
		return CreateResult{}, catalog.Wrap(catalog.CodeInternal, "encrypt webhook signing material", err)
	}
	endpoint.SecretCiphertext = encrypted
	endpoint.SecretVersion++
	endpoint.Revision++
	endpoint.UpdatedAt = now
	evidence, err := s.endpointAudit(ctx, endpoint, audit.ActionWebhookSecretRotated, input.RevokePrevious)
	if err != nil {
		return CreateResult{}, err
	}
	endpoint, err = s.store.UpdateEndpoint(ctx, endpoint, expectedRevision, evidence)
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Endpoint: endpoint, Secret: secret, PreviousSecretExpiresAt: nextMaterial.PreviousUntil}, nil
}

// Test sends one signed synthetic notification after authorization, revision
// and process admission checks, then retains the delivery outcome.
func (s *Service) Test(ctx context.Context, endpointID string, expectedRevision int64) (Delivery, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "webhook.test")
	if err != nil {
		return Delivery{}, err
	}
	endpoint, err := s.store.GetEndpoint(ctx, actor.TenantID, endpointID)
	if err != nil {
		return Delivery{}, err
	}
	if endpoint.Revision != expectedRevision {
		return Delivery{}, catalog.E(catalog.CodePreconditionFailed, "webhook revision does not match")
	}
	if endpoint.Status != StatusActive {
		return Delivery{}, catalog.E(catalog.CodePreconditionFailed, "only an active webhook can be tested")
	}
	if err := ctx.Err(); err != nil {
		return Delivery{}, err
	}
	// One synchronous test operation per API process. Do not queue callers,
	// delay ordinary commands, or confuse this with a per-second rate promise.
	// Authentication, tenant lookup and revision checks precede admission.
	if !s.testBusy.CompareAndSwap(false, true) {
		return Delivery{}, catalog.E(catalog.CodeWebhookTestBusy, "webhook test send is busy; no request was sent")
	}
	defer s.testBusy.Store(false)
	now := s.clock.Now()
	eventID, err := s.ids.New(now)
	if err != nil {
		return Delivery{}, catalog.Wrap(catalog.CodeInternal, "create webhook test event identifier", err)
	}
	deliveryID, err := s.ids.New(now.Add(time.Nanosecond))
	if err != nil {
		return Delivery{}, catalog.Wrap(catalog.CodeInternal, "create webhook delivery identifier", err)
	}
	data, err := events.MarshalReference("webhook.test", "webhook_endpoint", endpoint.ID, "")
	if err != nil {
		return Delivery{}, catalog.Wrap(catalog.CodeInternal, "create webhook test event", err)
	}
	event := catalog.OutboxEvent{CausalTrace: catalog.CausalTrace(ctx), ID: eventID, TenantID: actor.TenantID, Type: events.CanonicalType("webhook.test"), Subject: endpoint.ID, Data: data, CreatedAt: now}
	delivery := Delivery{ID: deliveryID, TenantID: actor.TenantID, EndpointID: endpoint.ID, EventID: event.ID, EventType: event.Type, State: DeliveryPending, CreatedAt: now, UpdatedAt: now}
	delivery = deliver(ctx, s.sender, s.cipher, endpoint, event, delivery, 1, s.clock, time.Hour)
	if err := ctx.Err(); err != nil {
		return Delivery{}, err
	}
	evidence, err := deliveryAudit(ctx, s.ids, delivery, false)
	if err != nil {
		return Delivery{}, err
	}
	return s.store.SaveDelivery(ctx, delivery, evidence)
}

// ListDeliveries authorizes endpoint history access and requires a digest-bound
// audit record for the returned page.
func (s *Service) ListDeliveries(ctx context.Context, request DeliveryListRequest) ([]Delivery, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := permissionActor(ctx, "webhook.read_evidence")
	if err != nil {
		return nil, err
	}
	if !identity.ValidTenantID(request.EndpointID) {
		return nil, catalog.E(catalog.CodeInvalidArgument, "webhookId must be a UUID")
	}
	if _, err := s.store.GetEndpoint(ctx, actor.TenantID, request.EndpointID); err != nil {
		return nil, err
	}
	if request.Limit < 1 || request.Limit > 100 || request.AfterUpdatedAt == nil && request.AfterID != "" || request.AfterUpdatedAt != nil && !identity.ValidTenantID(request.AfterID) {
		return nil, catalog.E(catalog.CodeInvalidArgument, "webhook-delivery page position or limit is invalid")
	}
	visibleLimit := request.Limit
	if request.DeliveryID != "" && !identity.ValidTenantID(request.DeliveryID) || request.EventID != "" && !identity.ValidTenantID(request.EventID) || request.State != "" && request.State != DeliveryPending && request.State != DeliverySucceeded && request.State != DeliveryWaiting && request.State != DeliveryDeadLetter {
		return nil, catalog.E(catalog.CodeInvalidArgument, "webhook delivery filters are invalid")
	}
	request.DeliveryID, request.EventID = strings.ToLower(request.DeliveryID), strings.ToLower(request.EventID)
	request.Limit++
	return s.store.ListDeliveries(ctx, actor.TenantID, request, s.deliveryReadAudit(ctx, actor.TenantID, request.EndpointID, visibleLimit))
}

// newSecret generates a random whsec_ signing value and returns its
// authenticated at-rest ciphertext separately.
func (s *Service) newSecret() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, catalog.Wrap(catalog.CodeInternal, "generate webhook signing secret", err)
	}
	secret := "whsec_" + base64.RawURLEncoding.EncodeToString(raw)
	encrypted, err := s.cipher.Encrypt([]byte(secret))
	if err != nil {
		return "", nil, catalog.Wrap(catalog.CodeInternal, "encrypt webhook signing secret", err)
	}
	return secret, encrypted, nil
}

var eventTypePattern = regexp.MustCompile(`^(?:\*|[a-z0-9]+(?:[._-][a-z0-9]+)*)$`)

// validateEndpointInput normalizes endpoint metadata and rejects
// credential-bearing URLs or unregistered event subscriptions.
func validateEndpointInput(name, endpointURL string, eventTypes []string) (string, string, []string, error) {
	name = strings.TrimSpace(name)
	endpointURL = strings.TrimSpace(endpointURL)
	if name == "" || !utf8.ValidString(name) || strings.ContainsRune(name, 0) || utf8.RuneCountInString(name) > 160 {
		return "", "", nil, catalog.E(catalog.CodeInvalidArgument, "webhook name is required")
	}
	if endpointURL == "" || len(endpointURL) > 2048 {
		return "", "", nil, catalog.E(catalog.CodeInvalidArgument, "webhook URL must contain at most 2048 characters")
	}
	parsed, err := url.Parse(endpointURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", "", nil, catalog.E(catalog.CodeInvalidArgument, "webhook URL must be an absolute HTTPS URL without user info or a fragment")
	}
	if !webhookASCII(parsed.Hostname()) {
		return "", "", nil, catalog.E(catalog.CodeInvalidArgument, "webhook URL hostname must use its normalized ASCII form")
	}
	if port := parsed.Port(); port != "" {
		value, parseErr := strconv.ParseUint(port, 10, 16)
		if parseErr != nil || value == 0 {
			return "", "", nil, catalog.E(catalog.CodeInvalidArgument, "webhook URL port is invalid")
		}
	}
	query, parseErr := url.ParseQuery(parsed.RawQuery)
	if parseErr != nil {
		return "", "", nil, catalog.E(catalog.CodeInvalidArgument, "webhook URL query is invalid")
	}
	for key := range query {
		if webhookSecretQueryParameter(key) {
			return "", "", nil, catalog.E(catalog.CodeInvalidArgument, "webhook URL cannot contain credentials or signatures in its query")
		}
	}
	if len(eventTypes) == 0 || len(eventTypes) > 64 {
		return "", "", nil, catalog.E(catalog.CodeInvalidArgument, "one to sixty-four webhook event types are required")
	}
	normalized := make([]string, 0, len(eventTypes))
	for _, eventType := range eventTypes {
		if eventType != strings.TrimSpace(eventType) || eventType != strings.ToLower(eventType) {
			return "", "", nil, catalog.E(catalog.CodeInvalidArgument, "webhook event type must use its exact lowercase spelling")
		}
		if len(eventType) > 160 || !eventTypePattern.MatchString(eventType) {
			return "", "", nil, catalog.E(catalog.CodeInvalidArgument, "webhook event type is invalid")
		}
		if eventType != "*" {
			eventType = catalog.CanonicalEventType(eventType)
			if !events.IsPublicType(eventType) {
				return "", "", nil, catalog.E(catalog.CodeInvalidArgument, "webhook event type is not in the public event registry")
			}
		}
		if !slices.Contains(normalized, eventType) {
			normalized = append(normalized, eventType)
		}
	}
	sort.Strings(normalized)
	return name, parsed.String(), normalized, nil
}

// webhookASCII reports whether a hostname is entirely ASCII before URL
// retention.
func webhookASCII(value string) bool {
	for _, character := range value {
		if character > 127 {
			return false
		}
	}
	return true
}

// webhookSecretQueryParameter recognizes normalized credential and signature
// query names that endpoint metadata must not retain.
func webhookSecretQueryParameter(name string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.TrimSpace(name)))
	switch normalized {
	case "accesstoken", "apikey", "authorization", "credential", "password", "secret", "sig", "signature", "token", "xamzcredential", "xamzsecuritytoken", "xamzsignature":
		return true
	default:
		return false
	}
}

func permissionActor(ctx context.Context, action identity.Permission) (identity.Actor, error) {
	a, err := identity.RequirePermission(ctx, action, "")
	if err != nil {
		return identity.Actor{}, catalog.PermissionError("current webhook permission is required", err)
	}
	return a, nil
}

// deliver prepares exact-byte signatures, sends one attempt and derives safe
// terminal or retry state without persisting it.
func deliver(ctx context.Context, sender Sender, cipher Cipher, endpoint Endpoint, event catalog.OutboxEvent, delivery Delivery, maxAttempts int, serviceClock Clock, retryCeiling time.Duration) Delivery {
	now := serviceClock.Now()
	delivery.Attempt++
	delivery.UpdatedAt = now
	delivery.ResponseStatus = 0
	material, err := readSigningMaterial(cipher, endpoint.SecretCiphertext)
	if err != nil {
		return failedDelivery(delivery, maxAttempts, now, "Webhook signing material is unavailable.", retryCeiling)
	}
	body, err := cloudEvent(event)
	if err != nil {
		return failedDelivery(delivery, maxAttempts, now, "Webhook event could not be encoded.", retryCeiling)
	}
	timestamp := fmt.Sprintf("%d", now.Unix())
	signature := signingHeader(material.Current, endpoint.SecretVersion, timestamp, body)
	source := events.TenantSource(event.TenantID)
	headers := map[string]string{"X-Catalog-Webhook-ID": endpoint.ID, "X-Catalog-Delivery-ID": delivery.ID, "X-Catalog-Timestamp": timestamp, "X-Catalog-Signature": signature, "Ce-Specversion": "1.0", "Ce-Id": event.ID, "Ce-Type": event.Type, "Ce-Source": source, "Ce-Subject": event.Subject, "Ce-Dataschema": events.DataSchema}
	if material.PreviousUntil != nil && now.Before(*material.PreviousUntil) {
		headers["X-Catalog-Previous-Signature"] = signingHeader(material.Previous, endpoint.SecretVersion-1, timestamp, body)
	}
	result, sendErr := sender.Send(ctx, SendRequest{TenantID: endpoint.TenantID, URL: endpoint.URL, Body: body, Headers: headers})
	// Retry delays start after the response, not before a potentially slow send.
	// Clamp a backwards clock adjustment to preserve monotonic delivery state.
	if finished := serviceClock.Now(); finished.After(now) {
		now = finished
	}
	delivery.UpdatedAt = now
	if sendErr != nil {
		return failedDelivery(delivery, maxAttempts, now, "Webhook delivery did not reach the endpoint.", retryCeiling)
	}
	delivery.ResponseStatus = result.Status
	if result.Status < 200 || result.Status >= 300 {
		if !retryableStatus(result.Status) {
			return failedDelivery(delivery, delivery.Attempt, now, fmt.Sprintf("Webhook endpoint returned terminal HTTP %d.", result.Status), retryCeiling)
		}
		delivery = failedDelivery(delivery, maxAttempts, now, fmt.Sprintf("Webhook endpoint returned HTTP %d.", result.Status), retryCeiling)
		if delivery.NextAttemptAt != nil {
			next := now.Add(max(delivery.NextAttemptAt.Sub(now), retryAfterDelay(result.RetryAfter, now, retryCeiling)))
			delivery.NextAttemptAt = &next
		}
		return delivery
	}
	delivery.State = DeliverySucceeded
	delivery.SafeError = ""
	delivery.NextAttemptAt = nil
	return delivery
}

// failedDelivery records a safe failure and either dead-letters the attempt or
// schedules capped jittered retry.
func failedDelivery(delivery Delivery, maxAttempts int, now time.Time, safeError string, retryCeiling time.Duration) Delivery {
	delivery.SafeError = safeError
	if delivery.Attempt >= maxAttempts {
		delivery.State = DeliveryDeadLetter
		delivery.NextAttemptAt = nil
		return delivery
	}
	delivery.State = DeliveryWaiting
	next := now.Add(jitteredBackoff(delivery.ID, delivery.Attempt, retryCeiling))
	delivery.NextAttemptAt = &next
	return delivery
}

// webhookBackoff computes a five-second exponential window capped at one hour.
func webhookBackoff(attempt int) time.Duration {
	delay := 5 * time.Second
	for current := 1; current < attempt && delay < time.Hour; current++ {
		delay *= 3
	}
	return min(delay, time.Hour)
}

// cloudEvent converts an outbox record into the validated public notification
// envelope.
func cloudEvent(event catalog.OutboxEvent) ([]byte, error) {
	return events.MarshalCloudEvent(event.ID, event.TenantID, event.Type, event.Subject, event.CreatedAt, event.Data)
}
