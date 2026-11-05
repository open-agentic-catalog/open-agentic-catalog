//go:build functional

package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

type rawAdministrationPage struct {
	Items      []json.RawMessage `json:"items"`
	NextCursor string            `json:"nextCursor"`
}

func TestAdministrationHandlersResumeEveryDatabaseBoundList(t *testing.T) {
	fixture := newAdministrationPaginationFixture(t)

	tests := []struct {
		name       string
		target     string
		handler    http.HandlerFunc
		pathValues map[string]string
	}{
		{name: "namespaces", target: "/api/v1/namespaces?limit=1", handler: fixture.server.handleNamespacesList},
		{name: "publishers", target: "/api/v1/publishers?limit=1", handler: fixture.server.handlePublishersList},
		{name: "resources", target: "/api/v1/resources?kind=agent&limit=1", handler: fixture.server.handleResourcesList},
		{name: "audit events", target: "/api/v1/audit-events?limit=1", handler: fixture.server.handleAuditEvents},
		{name: "federation sources", target: "/api/v1/federation/sources?limit=1", handler: fixture.server.handleSourcesList},
		{name: "source snapshots", target: "/api/v1/federation/sources/" + fixture.sourceID + "/snapshots?limit=1", handler: fixture.server.handleSourceSnapshots, pathValues: map[string]string{"sourceId": fixture.sourceID}},
		{name: "observations", target: "/api/v1/observations?sourceId=" + fixture.sourceID + "&freshness=current&limit=1", handler: fixture.server.handleObservations},
		{name: "observation occurrences", target: "/api/v1/observations/" + fixture.observationID + "/occurrences?limit=1", handler: fixture.server.handleObservationOccurrences, pathValues: map[string]string{"observationId": fixture.observationID}},
		{name: "jobs", target: "/api/v1/jobs?limit=1", handler: fixture.server.handleJobsList},
		{name: "role assignments", target: "/api/v1/role-assignments?limit=1", handler: fixture.server.handleRoleAssignmentsList},
		{name: "publication snapshots", target: "/api/v1/publication/snapshots?host=catalog.example.com&limit=1", handler: fixture.server.handlePublicationList},
		{name: "webhooks", target: "/api/v1/webhooks?limit=1", handler: fixture.server.handleWebhooksList},
		{name: "webhook deliveries", target: "/api/v1/webhooks/" + fixture.webhookID + "/deliveries?limit=1", handler: fixture.server.handleWebhookDeliveries, pathValues: map[string]string{"webhookId": fixture.webhookID}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := invokeAdministrationHandler(t, fixture.actor, test.handler, http.MethodGet, test.target, nil, test.pathValues)
			if len(first.Items) != 1 || first.NextCursor == "" {
				t.Fatalf("first page = %#v", first)
			}
			target, err := url.Parse(test.target)
			if err != nil {
				t.Fatal(err)
			}
			query := target.Query()
			query.Set("cursor", first.NextCursor)
			target.RawQuery = query.Encode()
			second := invokeAdministrationHandler(t, fixture.actor, test.handler, http.MethodGet, target.String(), nil, test.pathValues)
			if len(second.Items) == 0 {
				t.Fatalf("resumed page = %#v", second)
			}
		})
	}
}

func TestObservationSearchResumesItsBodyBoundCursor(t *testing.T) {
	fixture := newAdministrationPaginationFixture(t)
	first := invokeAdministrationHandler(t, fixture.actor, fixture.server.handleObservationSearch, http.MethodPost, "/api/v1/observations:search", map[string]any{"text": "agent", "limit": 1}, nil)
	if len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("first observation-search page = %#v", first)
	}
	second := invokeAdministrationHandler(t, fixture.actor, fixture.server.handleObservationSearch, http.MethodPost, "/api/v1/observations:search", map[string]any{"text": "agent", "limit": 1, "cursor": first.NextCursor}, nil)
	if len(second.Items) != 1 || second.NextCursor != "" {
		t.Fatalf("resumed observation-search page = %#v", second)
	}
}

func TestAuditCursorBindsEveryNormalizedFilter(t *testing.T) {
	fixture := newAdministrationPaginationFixture(t)
	from := time.Date(2026, time.September, 2, 11, 59, 0, 0, time.UTC)
	before := from.Add(2 * time.Minute)
	base := "/api/v1/audit-events?action=namespace.created&occurredFrom=" + url.QueryEscape(from.Format(time.RFC3339Nano)) + "&occurredBefore=" + url.QueryEscape(before.Format(time.RFC3339Nano)) + "&limit=1"
	first := invokeAdministrationHandler(t, fixture.actor, fixture.server.handleAuditEvents, http.MethodGet, base, nil, nil)
	if len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("filtered audit first page = %#v", first)
	}
	resumed := addAdministrationQuery(t, base, "cursor", first.NextCursor)
	second := invokeAdministrationHandler(t, fixture.actor, fixture.server.handleAuditEvents, http.MethodGet, resumed, nil, nil)
	if len(second.Items) != 1 {
		t.Fatalf("filtered audit resumed page = %#v", second)
	}
	changed := addAdministrationQuery(t, "/api/v1/audit-events?action=resource.created&occurredFrom="+url.QueryEscape(from.Format(time.RFC3339Nano))+"&occurredBefore="+url.QueryEscape(before.Format(time.RFC3339Nano))+"&limit=1", "cursor", first.NextCursor)
	requireAdministrationHandlerBadRequest(t, fixture.actor, fixture.server.handleAuditEvents, http.MethodGet, changed, nil, nil)
}

func TestAdministrationHandlersRejectInvalidLimitsAndSignedPositions(t *testing.T) {
	fixture := newAdministrationPaginationFixture(t)
	validID := "00000000-0000-7000-8000-000000000199"
	validTime := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	tests := []struct {
		name            string
		target          string
		handler         http.HandlerFunc
		pathValues      map[string]string
		binding         adminCursorBinding
		malformed       []string
		invalidPosition []string
		serviceInvalid  []string
	}{
		{name: "namespaces", target: "/api/v1/namespaces", handler: fixture.server.handleNamespacesList, binding: adminCursorBinding{Purpose: "namespaces", Order: "slug,id"}, malformed: []string{"only-one"}, invalidPosition: []string{"alpha", "not-a-uuid"}},
		{name: "publishers", target: "/api/v1/publishers", handler: fixture.server.handlePublishersList, binding: adminCursorBinding{Purpose: "publishers", Order: "displayName,id"}, malformed: []string{"only-one"}, invalidPosition: []string{"Alpha publisher", "not-a-uuid"}},
		{name: "resources", target: "/api/v1/resources?kind=agent", handler: fixture.server.handleResourcesList, binding: adminCursorBinding{Purpose: "resources", Filters: map[string]string{"kind": "agent", "canonicalIdentifier": ""}, Order: "updatedAt desc,id desc"}, malformed: []string{"only-one"}, invalidPosition: []string{"not-a-time", validID}, serviceInvalid: []string{validTime, "not-a-uuid"}},
		{name: "audit events", target: "/api/v1/audit-events", handler: fixture.server.handleAuditEvents, binding: adminCursorBinding{Purpose: "audit-events", Order: "occurredAt desc,id desc"}, malformed: []string{"only-one"}, invalidPosition: []string{"not-a-time", validID}, serviceInvalid: []string{validTime, "not-a-uuid"}},
		{name: "federation sources", target: "/api/v1/federation/sources", handler: fixture.server.handleSourcesList, binding: adminCursorBinding{Purpose: "federation-sources", Order: "displayName,id"}, malformed: []string{"only-one"}, invalidPosition: []string{"source", "not-a-uuid"}},
		{name: "source snapshots", target: "/api/v1/federation/sources/" + fixture.sourceID + "/snapshots", handler: fixture.server.handleSourceSnapshots, pathValues: map[string]string{"sourceId": fixture.sourceID}, binding: adminCursorBinding{Purpose: "federation-source-snapshots", Parent: fixture.sourceID, Order: "startedAt desc,id desc"}, malformed: []string{"only-one"}, invalidPosition: []string{"not-a-time", validID}, serviceInvalid: []string{validTime, "not-a-uuid"}},
		{name: "observations", target: "/api/v1/observations?sourceId=" + fixture.sourceID + "&freshness=current", handler: fixture.server.handleObservations, binding: adminCursorBinding{Purpose: "observations", Filters: map[string]string{"sourceId": fixture.sourceID, "freshness": "current"}, Order: "identifier,versionPresent desc,version,id"}, malformed: []string{"only-one"}, invalidPosition: []string{"identifier", "not-a-bool", "1", validID}, serviceInvalid: []string{"identifier", "true", "1", "not-a-uuid"}},
		{name: "observation occurrences", target: "/api/v1/observations/" + fixture.observationID + "/occurrences", handler: fixture.server.handleObservationOccurrences, pathValues: map[string]string{"observationId": fixture.observationID}, binding: adminCursorBinding{Purpose: "observation-occurrences", Parent: fixture.observationID, Order: "observedAt desc,snapshotId desc,retrievalDigest,entryLocation"}, malformed: []string{"only-one"}, invalidPosition: []string{"not-a-time", validID, "sha256:invalid", "/entries/0"}, serviceInvalid: []string{validTime, validID, "invalid-digest", "/entries/0"}},
		{name: "jobs", target: "/api/v1/jobs", handler: fixture.server.handleJobsList, binding: adminCursorBinding{Purpose: "jobs", Order: "createdAt desc,id desc"}, malformed: []string{"only-one"}, invalidPosition: []string{"not-a-time", validID}, serviceInvalid: []string{validTime, "not-a-uuid"}},
		{name: "role assignments", target: "/api/v1/role-assignments", handler: fixture.server.handleRoleAssignmentsList, binding: adminCursorBinding{Purpose: "role-assignments", Order: "subject,namespaceId,role,id"}, malformed: []string{"only-one"}, invalidPosition: []string{"reader", "", "invalid-role", validID}},
		{name: "publication snapshots", target: "/api/v1/publication/snapshots?host=catalog.example.com", handler: fixture.server.handlePublicationList, binding: adminCursorBinding{Purpose: "publication-snapshots", Parent: "catalog.example.com", Filters: map[string]string{"host": "catalog.example.com"}, Order: "generation desc,id desc"}, malformed: []string{"only-one"}, invalidPosition: []string{"not-a-generation", validID}, serviceInvalid: []string{"1", ""}},
		{name: "webhooks", target: "/api/v1/webhooks", handler: fixture.server.handleWebhooksList, binding: adminCursorBinding{Purpose: "webhooks", Filters: map[string]string{"name": "", "status": ""}, Order: "name,id"}, malformed: []string{"only-one"}, invalidPosition: []string{"webhook", "not-a-uuid"}},
		{name: "webhook deliveries", target: "/api/v1/webhooks/" + fixture.webhookID + "/deliveries", handler: fixture.server.handleWebhookDeliveries, pathValues: map[string]string{"webhookId": fixture.webhookID}, binding: adminCursorBinding{Purpose: "webhook-deliveries", Parent: fixture.webhookID, Filters: map[string]string{"deliveryId": "", "eventId": "", "state": ""}, Order: "updatedAt desc,id desc"}, malformed: []string{"only-one"}, invalidPosition: []string{"not-a-time", validID}, serviceInvalid: []string{validTime, "not-a-uuid"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalidLimitTarget := addAdministrationQuery(t, test.target, "limit", "0")
			requireAdministrationHandlerBadRequest(t, fixture.actor, test.handler, http.MethodGet, invalidLimitTarget, nil, test.pathValues)

			if test.name == "audit events" {
				// Audit cursors bind normalized filters, including absent values.
				// Without this binding, the cursor would fail before position decoding.
				_, filters, err := parseAuditFilters(httptest.NewRequestWithContext(t.Context(), http.MethodGet, test.target, nil))
				if err != nil {
					t.Fatal(err)
				}
				test.binding.Filters = filters
			}
			test.binding.AuthorizationDigest = fixtureDecisionDigest(t, t.Context(), fixture.actor)
			test.binding.Limit = 1
			if test.name == "audit events" {
				token, err := fixture.server.encodeAdminCursor(adminPageRequest{Limit: 1, binding: test.binding}, []string{validTime, validID})
				if err != nil {
					t.Fatal(err)
				}
				target := addAdministrationQuery(t, test.target, "limit", "1")
				invokeAdministrationHandler(t, fixture.actor, test.handler, http.MethodGet, addAdministrationQuery(t, target, "cursor", token), nil, nil)
			}
			positions := [][]string{test.malformed, test.invalidPosition}
			if test.serviceInvalid != nil {
				positions = append(positions, test.serviceInvalid)
			}
			for _, position := range positions {
				token, err := fixture.server.encodeAdminCursor(adminPageRequest{Limit: 1, binding: test.binding}, position)
				if err != nil {
					t.Fatal(err)
				}
				target := addAdministrationQuery(t, test.target, "limit", "1")
				target = addAdministrationQuery(t, target, "cursor", token)
				requireAdministrationHandlerBadRequest(t, fixture.actor, test.handler, http.MethodGet, target, nil, test.pathValues)
			}
		})
	}

	searchBinding := adminCursorBinding{Purpose: "observation-search", AuthorizationDigest: fixtureDecisionDigest(t, t.Context(), fixture.actor), Filters: map[string]string{"query": `{"text":"agent"}`}, Order: "score desc,identifier,id", Limit: 1}
	for _, position := range [][]string{{"only-one"}, {"not-a-score", "identifier", validID}, {"80", "identifier", "not-a-uuid"}} {
		token, err := fixture.server.encodeAdminCursor(adminPageRequest{Limit: 1, binding: searchBinding}, position)
		if err != nil {
			t.Fatal(err)
		}
		requireAdministrationHandlerBadRequest(t, fixture.actor, fixture.server.handleObservationSearch, http.MethodPost, "/api/v1/observations:search", map[string]any{"text": "agent", "limit": 1, "cursor": token}, nil)
	}
	requireAdministrationHandlerBadRequest(t, fixture.actor, fixture.server.handleObservationSearch, http.MethodPost, "/api/v1/observations:search?limit=1", map[string]any{"text": "agent", "limit": 1}, nil)
}

type administrationPaginationFixture struct {
	server        *Server
	actor         identity.Actor
	sourceID      string
	observationID string
	webhookID     string
}

func newAdministrationPaginationFixture(t *testing.T) administrationPaginationFixture {
	t.Helper()
	serviceClock := clock.NewFake(time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC))
	tenantID := "00000000-0000-7000-8000-000000000001"
	actor := identity.Actor{TenantID: tenantID, Subject: "admin", MembershipID: "00000000-0000-7000-8000-000000000501", RealmID: "00000000-0000-7000-8000-000000000502", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleSourceAdmin, identity.RoleReader, identity.RoleContributor, identity.RolePublisher}}
	cursorKey := sha256.Sum256([]byte("administration-handler-pagination-test-key"))
	actorContext := identityfixture.WithActor(context.Background(), actor)

	catalogRepository := catalog.NewMemoryRepository()
	catalogService, err := catalog.NewService(catalog.Options{Repository: catalogRepository, Clock: serviceClock, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: cursorKey[:]})
	if err != nil {
		t.Fatal(err)
	}
	var namespace catalog.Namespace
	for index, input := range []catalog.CreateNamespaceInput{
		{Slug: "alpha", DisplayName: "Alpha namespace"},
		{Slug: "beta", DisplayName: "Beta namespace"},
	} {
		created, createErr := catalogService.CreateNamespace(actorContext, input)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if index == 0 {
			namespace = created
		}
	}
	for _, input := range []catalog.CreatePublisherInput{
		{DisplayName: "Alpha publisher", Domains: []string{"alpha.example.com"}},
		{DisplayName: "Beta publisher", Domains: []string{"beta.example.com"}},
	} {
		if _, createErr := catalogService.CreatePublisher(actorContext, input); createErr != nil {
			t.Fatal(createErr)
		}
	}
	for _, input := range []catalog.CreateResourceInput{
		{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:alpha.example.com:default:agent", Kind: catalog.KindAgent},
		{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:beta.example.com:default:agent", Kind: catalog.KindAgent},
	} {
		if _, createErr := catalogService.CreateResource(actorContext, input); createErr != nil {
			t.Fatal(createErr)
		}
	}

	authenticator, identityService := newTestIdentity(t, serviceClock, map[string]config.DevelopmentUser{
		"admin":  {Subject: actor.Subject, TenantID: tenantID, Roles: actor.Roles},
		"reader": {Subject: "reader", TenantID: tenantID, Roles: []identity.Role{identity.RoleReader}},
	})

	actor, err = authenticator.Authenticate(t.Context(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	federationStore := federation.NewMemoryStore()
	sourceID := "00000000-0000-7000-8000-000000000100"
	sources := []federation.Source{
		{ID: sourceID, TenantID: tenantID, DisplayName: "Alpha source", Endpoint: "https://alpha.example.com/ard.json", Status: federation.SourceActive, RetrievalMode: federation.RetrievalIndexed, Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, Revision: 1, CreatedAt: serviceClock.Now(), UpdatedAt: serviceClock.Now()},
		{ID: "00000000-0000-7000-8000-000000000101", TenantID: tenantID, DisplayName: "Beta source", Endpoint: "https://beta.example.com/ard.json", Status: federation.SourceActive, RetrievalMode: federation.RetrievalIndexed, Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, Revision: 1, CreatedAt: serviceClock.Now(), UpdatedAt: serviceClock.Now()},
	}
	for _, source := range sources {
		if _, err := federationStore.CreateSource(context.Background(), source); err != nil {
			t.Fatal(err)
		}
	}
	firstSnapshot, firstObservations := administrationSnapshot(serviceClock.Now(), tenantID, sources[0], "00000000-0000-7000-8000-000000000110", 0)
	if _, _, err := federationStore.CommitSync(context.Background(), firstSnapshot, firstObservations, sources[0], 1); err != nil {
		t.Fatal(err)
	}
	secondSnapshot, secondObservations := administrationSnapshot(serviceClock.Now().Add(2*time.Minute), tenantID, sources[0], "00000000-0000-7000-8000-000000000111", 1)
	if _, _, err := federationStore.CommitSync(context.Background(), secondSnapshot, secondObservations, sources[0], 1); err != nil {
		t.Fatal(err)
	}
	federationManager, err := federation.NewManager(federationStore, unavailableFetcher{}, serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}

	jobStore := jobs.NewMemoryStore()
	for index, jobID := range []string{"00000000-0000-7000-8000-000000000121", "00000000-0000-7000-8000-000000000120"} {
		createdAt := serviceClock.Now().Add(time.Duration(index) * time.Minute)
		if _, _, err := jobStore.Enqueue(context.Background(), jobs.Job{ID: jobID, TenantID: tenantID, Kind: "source.sync", Actor: actor.Subject, PayloadVersion: 1, Payload: json.RawMessage(`{}`), State: jobs.StateQueued, NotBefore: createdAt, AttemptLimit: 5, DeduplicationKey: jobID, CreatedAt: createdAt, UpdatedAt: createdAt}); err != nil {
			t.Fatal(err)
		}
	}
	jobService, err := jobs.NewService(jobStore, serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}

	publicationStore := publication.NewMemoryStore()
	for generation, snapshotID := range map[int64]string{1: "00000000-0000-7000-8000-000000000130", 2: "00000000-0000-7000-8000-000000000131"} {
		if _, err := publicationStore.SaveCandidate(context.Background(), publication.Snapshot{ID: snapshotID, TenantID: tenantID, Host: "catalog.example.com", Audience: publication.AudienceAuthenticated, Generation: generation, ARDProfile: "ard-test", AICatalogProfile: "ai-test", State: publication.SnapshotCandidate, ETag: `"snapshot-` + snapshotID + `"`, CreatedAt: serviceClock.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	publicationManager, err := publication.NewManager(publication.Options{Records: fixedPublicationRecordSource{tenantID: tenantID}, Store: publicationStore, Clock: serviceClock, IDs: id.Generator{}, BaseURL: "https://catalog.example.com", PublisherDomain: "catalog.example.com", ARDProfile: "ard-test", AICatalogProfile: "ai-test", CursorKey: cursorKey[:]})
	if err != nil {
		t.Fatal(err)
	}

	webhookStore := webhook.NewMemoryStore()
	webhookID := "00000000-0000-7000-8000-000000000140"
	for index, endpoint := range []webhook.Endpoint{
		{ID: webhookID, TenantID: tenantID, Name: "Alpha webhook", URL: "https://hooks.example.com/alpha", EventTypes: []string{"*"}, Status: webhook.StatusActive, Revision: 1, CreatedBy: actor.Subject, CreatedAt: serviceClock.Now(), UpdatedAt: serviceClock.Now()},
		{ID: "00000000-0000-7000-8000-000000000141", TenantID: tenantID, Name: "Beta webhook", URL: "https://hooks.example.com/beta", EventTypes: []string{"*"}, Status: webhook.StatusActive, Revision: 1, CreatedBy: actor.Subject, CreatedAt: serviceClock.Now(), UpdatedAt: serviceClock.Now()},
	} {
		if _, err := webhookStore.CreateEndpoint(context.Background(), endpoint); err != nil {
			t.Fatal(err)
		}
		updatedAt := serviceClock.Now().Add(time.Duration(index) * time.Minute)
		if _, err := webhookStore.SaveDelivery(context.Background(), webhook.Delivery{ID: "00000000-0000-7000-8000-00000000015" + string(rune('0'+index)), TenantID: tenantID, EndpointID: webhookID, EventID: "00000000-0000-7000-8000-00000000016" + string(rune('0'+index)), EventType: "com.open.agentic.catalog.resource.created.v1", State: webhook.DeliverySucceeded, Attempt: 1, CreatedAt: updatedAt, UpdatedAt: updatedAt}); err != nil {
			t.Fatal(err)
		}
	}
	cipher, err := webhook.NewAESGCM(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	webhookService, err := webhook.NewService(webhookStore, cipher, successfulWebhookSender{}, serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}

	server := &Server{
		catalog:    catalogService,
		identity:   identityService,
		federation: federationManager,
		jobs:       jobService,
		webhooks:   webhookService,
		discovery: map[string]DiscoveryHost{
			"catalog.example.com": {Host: "catalog.example.com", TenantID: tenantID, Mode: DiscoveryAuthenticated, PublisherDomain: "catalog.example.com", Publication: publicationManager},
		},
		maxBodyBytes: 1 << 20,
		logger:       slog.New(slog.DiscardHandler),
		cursorKey:    cursorKey[:],
		now:          serviceClock.Now,
	}
	return administrationPaginationFixture{server: server, actor: actor, sourceID: sourceID, observationID: firstObservations[0].ID, webhookID: webhookID}
}

func administrationSnapshot(startedAt time.Time, tenantID string, source federation.Source, snapshotID string, sequence int) (federation.Snapshot, []federation.Observation) {
	completedAt := startedAt.Add(time.Minute)
	identifiers := []string{"urn:air:source.example.com:default:alpha", "urn:air:source.example.com:default:beta"}
	if sequence > 0 {
		identifiers = identifiers[:1]
	}
	snapshot := federation.Snapshot{ID: snapshotID, TenantID: tenantID, SourceID: source.ID, SourceRevision: source.Revision, State: federation.SnapshotComplete, EnumerationComplete: true, MemberCount: len(identifiers), Added: len(identifiers), StartedAt: startedAt, CompletedAt: &completedAt}
	items := make([]federation.Observation, 0, len(identifiers))
	for index, identifier := range identifiers {
		name := "Alpha Agent"
		if index == 1 {
			name = "Beta Agent"
		}
		raw := json.RawMessage(`{"identifier":"` + identifier + `","displayName":"` + name + `"}`)
		normalized := json.RawMessage(`{"profile":"catalog-observation-v1","identifier":"` + identifier + `","versionPresent":true,"version":"1.0.0","kind":"agent","mediaType":"application/a2a-agent-card+json","displayName":"` + name + `","description":"Pagination test","url":"","data":null,"tags":["pagination"],"representativeQueries":["Find agent"],"metadata":null}`)
		exactDigest := administrationDigest(raw)
		normalizedDigest := administrationDigest(normalized)
		observationID := "00000000-0000-7000-8000-00000000011" + string(rune('2'+index))
		item := federation.Observation{ID: observationID, TenantID: tenantID, SourceID: source.ID, SnapshotID: snapshot.ID, SourceKey: identifier, Identifier: identifier, Version: "1.0.0", VersionPresent: true, Kind: catalog.KindAgent, MediaType: "application/a2a-agent-card+json", Completeness: federation.CompleteEntry, Compatibility: federation.Compatibility{Structural: "valid", ProfileID: "ard-test", Support: "supported"}, RawEntry: raw, NormalizedEntry: normalized, RawEntryAvailable: true, IdentityDigest: exactDigest, ExactEntryDigest: exactDigest, NormalizedEntryDigest: normalizedDigest, NormalizationProfile: "catalog-observation-v1", SearchDisplayName: name, SearchDescription: "Pagination test", SearchTags: []string{"pagination"}, SearchQueries: []string{"Find agent"}, SearchPublisher: "source.example.com", Freshness: federation.FreshCurrent, FirstSeenAt: completedAt, LastSeenAt: completedAt}
		item.Occurrence = federation.ObservationOccurrence{ObservationID: item.ID, SourceID: source.ID, SnapshotID: snapshot.ID, SourceRevision: source.Revision, RetrievalDigest: administrationDigest([]byte(snapshot.ID + ":" + identifier)), EntryLocation: "/entries/" + string(rune('0'+index)), ExactEntryDigest: exactDigest, NormalizedEntryDigest: normalizedDigest, EvidenceComplete: true, ObservedAt: completedAt}
		items = append(items, item)
	}
	return snapshot, items
}

func administrationDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func invokeAdministrationHandler(t *testing.T, actor identity.Actor, handler http.HandlerFunc, method, target string, body any, pathValues map[string]string) rawAdministrationPage {
	t.Helper()
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	request := httptest.NewRequestWithContext(identity.WithActor(context.Background(), actor), method, target, requestBody)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range pathValues {
		request.SetPathValue(name, value)
	}
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("%s %s returned %d: %s", method, target, response.Code, response.Body.String())
	}
	var page rawAdministrationPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func requireAdministrationHandlerBadRequest(t *testing.T, actor identity.Actor, handler http.HandlerFunc, method, target string, body any, pathValues map[string]string) {
	t.Helper()
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	request := httptest.NewRequestWithContext(identity.WithActor(context.Background(), actor), method, target, requestBody)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range pathValues {
		request.SetPathValue(name, value)
	}
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("%s %s returned %d, want %d: %s", method, target, response.Code, http.StatusBadRequest, response.Body.String())
	}
}

func addAdministrationQuery(t *testing.T, target, name, value string) string {
	t.Helper()
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set(name, value)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
