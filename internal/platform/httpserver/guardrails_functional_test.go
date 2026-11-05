//go:build functional

// Functional tests exercise local HTTP or composed service workflows.
package httpserver

import (
	"net/http"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

func TestHTTPEmptyCollectionsMissingResourcesAndDispatchers(t *testing.T) {
	testServer := newWebhookHTTPServer(t)
	defer testServer.Close()

	for _, path := range []string{
		"/api/v1/namespaces",
		"/api/v1/publishers",
		"/api/v1/resources",
		"/api/v1/catalog/resources",
		"/api/v1/audit-events",
		"/api/v1/publication/snapshots",
		"/api/v1/federation/sources",
		"/api/v1/observations",
		"/api/v1/jobs",
		"/api/v1/webhooks",
	} {
		t.Run("empty "+path, func(t *testing.T) {
			requestPath := path
			if path == "/api/v1/publication/snapshots" {
				requestPath += "?host=127.0.0.1"
			}
			result := requestJSON(t, testServer.URL, http.MethodGet, requestPath, "admin", "", "", nil)
			requireStatus(t, result, http.StatusOK)
		})
	}

	for _, path := range []string{
		"/api/v1/namespaces/00000000-0000-7000-8000-000000000099",
		"/api/v1/publishers/00000000-0000-7000-8000-000000000099",
		"/api/v1/resources/00000000-0000-7000-8000-000000000099",
		"/api/v1/resource-versions/00000000-0000-7000-8000-000000000099",
		"/api/v1/version-revisions/00000000-0000-7000-8000-000000000099",
		"/api/v1/catalog-listings/00000000-0000-7000-8000-000000000099",
		"/api/v1/listing-revisions/00000000-0000-7000-8000-000000000099",
		"/api/v1/review-submissions/00000000-0000-7000-8000-000000000099",
		"/api/v1/listing-releases/00000000-0000-7000-8000-000000000099",
		"/api/v1/catalog/resources/00000000-0000-7000-8000-000000000099",
		"/api/v1/publication/snapshots/00000000-0000-7000-8000-000000000099",
		"/api/v1/federation/source-inspections/00000000-0000-7000-8000-000000000099",
		"/api/v1/federation/sources/00000000-0000-7000-8000-000000000099",
		"/api/v1/federation/snapshots/00000000-0000-7000-8000-000000000099",
		"/api/v1/observations/00000000-0000-7000-8000-000000000099/raw",
		"/api/v1/jobs/00000000-0000-7000-8000-000000000099",
		"/api/v1/webhooks/00000000-0000-7000-8000-000000000099",
	} {
		t.Run("missing "+path, func(t *testing.T) {
			requestPath := path
			if strings.HasPrefix(path, "/api/v1/publication/snapshots/") {
				requestPath += "?host=127.0.0.1"
			}
			result := requestJSON(t, testServer.URL, http.MethodGet, requestPath, "admin", "", "", nil)
			requireStatus(t, result, http.StatusNotFound)
		})
	}

	dispatchers := []string{
		"/api/v1/publishers/00000000-0000-7000-8000-000000000099/publication-domains/not-a-command",
		"/api/v1/review-submissions/missing:unknown",
		"/api/v1/catalog-listings/missing:unknown",
		"/api/v1/listing-releases/missing:unknown",
		"/api/v1/publication/snapshots/missing:unknown",
		"/api/v1/federation/sources/missing:unknown",
		"/api/v1/jobs/missing:unknown",
		"/api/v1/webhooks/missing:unknown",
	}
	for index, path := range dispatchers {
		result := requestJSON(t, testServer.URL, http.MethodPost, path, "admin", "dispatcher-"+jsonNumber(int64(index)), "", map[string]any{})
		requireStatus(t, result, http.StatusNotFound)
	}

	badBodies := []struct {
		method string
		path   string
		match  string
		body   []byte
		key    string
		status int
	}{
		{http.MethodPost, "/api/v1/catalog/search", "", []byte(`[]`), "", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/catalog/explore", "", []byte(`{"unknown":true}`), "", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/observations:search", "", []byte(`[]`), "", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/federation/sources:live-search", "", []byte(`[]`), "", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/audit-exports", "", []byte(`{"occurredFrom":"2026-09-03T10:00:00Z","occurredBefore":"2026-09-03T11:00:00Z","reason":"review","unknown":true}`), "bad-audit-export", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/audit-exports", "", []byte(`{"actor":"","occurredFrom":"2026-09-03T10:00:00Z","occurredBefore":"2026-09-03T11:00:00Z","reason":"review"}`), "empty-audit-export-filter", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/audit-exports", "", []byte(`{"actor":null,"occurredFrom":"2026-09-03T10:00:00Z","occurredBefore":"2026-09-03T11:00:00Z","reason":"review"}`), "null-audit-export-filter", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/federation/sources/00000000-0000-7000-8000-000000000099:test", `"rev-1"`, []byte(`{"unknown":true}`), "bad-source-test", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/federation/sources/00000000-0000-7000-8000-000000000099:sync", `"rev-1"`, []byte(`{"unknown":true}`), "bad-source-sync", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/federation/sources/00000000-0000-7000-8000-000000000099:pause", `"rev-1"`, []byte(`{"unknown":true}`), "bad-source-pause", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/publication/snapshots", "", []byte(`{"unknown":true}`), "bad-publication", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/jobs/00000000-0000-7000-8000-000000000099:cancel", "", []byte(`{"unknown":true}`), "bad-job-cancel", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/jobs/00000000-0000-7000-8000-000000000099:retry", "", []byte(`{"unknown":true}`), "bad-job-retry", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/review-submissions/00000000-0000-7000-8000-000000000099:reject", `"rev-1"`, []byte(`{"unknown":true}`), "bad-review", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/catalog-listings/00000000-0000-7000-8000-000000000099:publish", `"rev-1"`, []byte(`{"unknown":true}`), "bad-publish", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/listing-releases/00000000-0000-7000-8000-000000000099:retire", `"rev-1"`, []byte(`{"unknown":true}`), "bad-release", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/webhooks/00000000-0000-7000-8000-000000000099:rotate-secret", `"rev-1"`, []byte(`{"unknown":true}`), "bad-rotate", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/webhooks/00000000-0000-7000-8000-000000000099:test", `"rev-1"`, []byte(`{"unknown":true}`), "bad-webhook-test", http.StatusBadRequest},
	}
	for _, testCase := range badBodies {
		result := requestWithHeaders(t, testServer.URL, testCase.method, testCase.path, testCase.body, map[string]string{
			"Authorization":   "Bearer admin",
			"Content-Type":    "application/json",
			"Idempotency-Key": testCase.key,
			"If-Match":        testCase.match,
		})
		requireStatus(t, result, testCase.status)
	}

	for _, path := range []string{
		"/api/v1/resources?kind=unsupported",
		"/api/v1/resources?kind=",
		"/api/v1/resources?kind",
		"/api/v1/catalog/resources?visibility=external",
		"/api/v1/catalog/resources?kind=",
		"/api/v1/catalog/resources?visibility=",
		"/api/v1/catalog/resources?kind=&visibility=",
		"/api/v1/audit-events?limit=101",
		"/api/v1/audit-events?targetType=resource",
		"/api/v1/audit-events?actor=",
		"/api/v1/audit-events?occurredFrom=",
		"/api/v1/audit-events?occurredFrom=2026-09-03T12%3A00%3A00Z&occurredBefore=2026-09-03T11%3A00%3A00Z",
		"/api/v1/audit-events?occurredFrom=not-a-time",
		"/api/v1/jobs?limit=0",
		"/api/v1/webhooks/00000000-0000-7000-8000-000000000099/deliveries?limit=101",
	} {
		result := requestJSON(t, testServer.URL, http.MethodGet, path, "admin", "", "", nil)
		requireStatus(t, result, http.StatusBadRequest)
		if strings.HasPrefix(path, "/api/v1/resources?kind") {
			var problem Problem
			decodeResult(t, result, &problem)
			if problem.Code != catalog.CodeInvalidArgument || problem.RequestID == "" || problem.RequestID != result.header.Get("X-Request-ID") || result.header.Get("Content-Type") != "application/problem+json" {
				t.Fatalf("invalid resource kind problem contract: %+v headers=%v", problem, result.header)
			}
		}
	}
}

func TestHTTPPublicationEndpointsRequirePublisherRole(t *testing.T) {
	testServer := newWebhookHTTPServer(t)
	defer testServer.Close()

	tests := []struct {
		method string
		path   string
		key    string
	}{
		{method: http.MethodGet, path: "/api/v1/publication/snapshots"},
		{method: http.MethodPost, path: "/api/v1/publication/snapshots", key: "forbidden-build"},
		{method: http.MethodGet, path: "/api/v1/publication/snapshots/00000000-0000-7000-8000-000000000099"},
		{method: http.MethodPost, path: "/api/v1/publication/snapshots/00000000-0000-7000-8000-000000000099:activate", key: "forbidden-activate"},
		{method: http.MethodPost, path: "/api/v1/publication:unpublish", key: "forbidden-unpublish"},
		{method: http.MethodPost, path: "/api/v1/publication/snapshots/00000000-0000-7000-8000-000000000099:rollback", key: "forbidden-rollback"},
	}
	for _, testCase := range tests {
		t.Run(testCase.method+" "+testCase.path, func(t *testing.T) {
			result := requestJSON(t, testServer.URL, testCase.method, testCase.path, "contributor", testCase.key, `"missing"`, map[string]any{})
			requireStatus(t, result, http.StatusForbidden)
		})
	}
}

func TestHTTPPublicationAndPublicDiscoveryRoundTrip(t *testing.T) {
	testServer := newWebhookHTTPServer(t)
	defer testServer.Close()
	inventory := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/publication/hosts", "admin", "", "", nil)
	requireStatus(t, inventory, http.StatusOK)
	requireControlSchema(t, "PublicationHostInventory", inventory.body)
	var hosts publicationHostInventory
	decodeResult(t, inventory, &hosts)
	if len(hosts.Items) != 1 || hosts.Items[0] != (publicationHost{Host: "127.0.0.1", Mode: DiscoveryAnonymous}) {
		t.Fatalf("host inventory = %+v", hosts)
	}
	for token, status := range map[string]int{"": http.StatusUnauthorized, "contributor": http.StatusForbidden} {
		denied := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/publication/hosts", token, "", "", nil)
		code := catalog.CodeForbidden
		if status == http.StatusUnauthorized {
			code = catalog.CodeUnauthenticated
		}
		requireControlProblem(t, denied, status, code)
	}

	build := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publication/snapshots", "admin", "publication-build", "", map[string]any{"host": "127.0.0.1"})
	requireStatus(t, build, http.StatusCreated)
	var candidate publication.Snapshot
	decodeResult(t, build, &candidate)
	if candidate.ID == "" || build.header.Get("ETag") == "" {
		t.Fatalf("publication candidate = %#v, headers %#v", candidate, build.header)
	}
	list := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/publication/snapshots?host=127.0.0.1", "admin", "", "", nil)
	requireStatus(t, list, http.StatusOK)
	get := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/publication/snapshots/"+candidate.ID+"?host=127.0.0.1", "admin", "", "", nil)
	requireStatus(t, get, http.StatusOK)

	wrongMatch := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publication/snapshots/"+candidate.ID+":activate", "admin", "publication-activate-wrong", `"wrong"`, map[string]any{"host": "127.0.0.1", "reason": "Activate the reviewed snapshot"})
	requireStatus(t, wrongMatch, http.StatusPreconditionFailed)
	activate := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publication/snapshots/"+candidate.ID+":activate", "admin", "publication-activate", candidate.ETag, map[string]any{"host": "127.0.0.1", "reason": "Activate the reviewed snapshot"})
	requireStatus(t, activate, http.StatusOK)

	manifest := requestJSON(t, testServer.URL, http.MethodGet, "/.well-known/ard.json", "", "", "", nil)
	requireStatus(t, manifest, http.StatusOK)
	if manifest.header.Get("Content-Digest") == "" || manifest.header.Get("Last-Modified") == "" {
		t.Fatalf("public manifest headers = %#v", manifest.header)
	}
	notModified := requestWithHeaders(t, testServer.URL, http.MethodGet, "/.well-known/ard.json", nil, map[string]string{"If-None-Match": "W/" + manifest.header.Get("ETag")})
	requireStatus(t, notModified, http.StatusNotModified)
	aiCatalog := requestJSON(t, testServer.URL, http.MethodGet, "/.well-known/ai-catalog.json", "", "", "", nil)
	requireStatus(t, aiCatalog, http.StatusOK)
	aiCatalogNotModified := requestWithHeaders(t, testServer.URL, http.MethodGet, "/.well-known/ai-catalog.json", nil, map[string]string{"If-None-Match": aiCatalog.header.Get("ETag")})
	requireStatus(t, aiCatalogNotModified, http.StatusNotModified)

	for name, body := range map[string]map[string]any{
		"none":      {"query": map[string]any{"text": "catalog"}, "federation": "none"},
		"auto":      {"query": map[string]any{"text": "catalog"}, "federation": "auto"},
		"referrals": {"query": map[string]any{"text": "catalog"}, "federation": "referrals"},
	} {
		t.Run("search "+name, func(t *testing.T) {
			result := requestJSON(t, testServer.URL, http.MethodPost, "/ard/v1/search", "", "", "", body)
			requireStatus(t, result, http.StatusOK)
			if result.header.Get("Cache-Control") != "no-store" || result.header.Get("Link") == "" {
				t.Fatalf("ARD search headers = %#v", result.header)
			}
		})
	}
	invalidSearch := requestWithHeaders(t, testServer.URL, http.MethodPost, "/ard/v1/search", []byte(`[]`), map[string]string{"Content-Type": "application/json"})
	requireStatus(t, invalidSearch, http.StatusBadRequest)
	invalidFederation := requestJSON(t, testServer.URL, http.MethodPost, "/ard/v1/search", "", "", "", map[string]any{"query": map[string]any{"text": "catalog"}, "federation": "unknown"})
	requireStatus(t, invalidFederation, http.StatusBadRequest)
	oversizedSearchPage := requestJSON(t, testServer.URL, http.MethodPost, "/ard/v1/search", "", "", "", map[string]any{"query": map[string]any{"text": "catalog"}, "federation": "none", "pageSize": 101})
	requireStatus(t, oversizedSearchPage, http.StatusBadRequest)
	explore := requestJSON(t, testServer.URL, http.MethodPost, "/ard/v1/explore", "", "", "", map[string]any{"resultType": map[string]any{"facets": []map[string]any{{"field": "type"}}}})
	requireStatus(t, explore, http.StatusOK)
	listARD := requestJSON(t, testServer.URL, http.MethodGet, "/ard/v1/agents?pageSize=1&orderBy=displayName", "", "", "", nil)
	requireStatus(t, listARD, http.StatusOK)
	invalidList := requestJSON(t, testServer.URL, http.MethodGet, "/ard/v1/agents?pageSize=101", "", "", "", nil)
	requireStatus(t, invalidList, http.StatusBadRequest)
	missingArtifact := requestJSON(t, testServer.URL, http.MethodGet, "/public/v1/artifacts/missing", "", "", "", nil)
	requireStatus(t, missingArtifact, http.StatusNotFound)

	unpublish := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publication:unpublish", "admin", "publication-unpublish", "", map[string]any{"host": "127.0.0.1", "reason": "Withdraw public discovery during the test"})
	requireStatus(t, unpublish, http.StatusOK)
	missingAfterUnpublish := requestJSON(t, testServer.URL, http.MethodGet, "/.well-known/ard.json", "", "", "", nil)
	requireStatus(t, missingAfterUnpublish, http.StatusNotFound)
	wrongRollback := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publication/snapshots/"+candidate.ID+":rollback", "admin", "publication-rollback-wrong", `"wrong"`, map[string]any{"host": "127.0.0.1", "reason": "Restore the retained snapshot"})
	requireStatus(t, wrongRollback, http.StatusPreconditionFailed)
	rollback := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publication/snapshots/"+candidate.ID+":rollback", "admin", "publication-rollback", candidate.ETag, map[string]any{"host": "127.0.0.1", "reason": "Restore the retained snapshot"})
	requireStatus(t, rollback, http.StatusOK)

	modifiedSince := requestWithHeaders(t, testServer.URL, http.MethodGet, "/.well-known/ard.json", nil, map[string]string{"If-Modified-Since": manifest.header.Get("Last-Modified")})
	if modifiedSince.status != http.StatusNotModified && modifiedSince.status != http.StatusOK {
		t.Fatalf("If-Modified-Since status = %d, body = %s", modifiedSince.status, modifiedSince.body)
	}
	if !strings.Contains(manifest.header.Get("Link"), "ai-catalog") {
		t.Fatalf("public discovery links = %q", manifest.header.Get("Link"))
	}
}
