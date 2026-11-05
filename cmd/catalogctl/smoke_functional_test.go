//go:build functional

// Functional tests exercise real process, network or application composition boundaries.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	platformruntime "github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/runtime"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

func TestSmokeExercisesRunningService(t *testing.T) {
	runtime := buildSmokeTestRuntime(t)
	defer runtime.Close()
	server := httptest.NewServer(runtime.HTTP.Handler())
	defer server.Close()

	raw := captureCatalogctlStdout(t, func() error {
		return smoke([]string{
			"--base-url", server.URL,
			"--admin-token", "local-admin",
			"--contributor-token", "local-contributor",
			"--reviewer-token", "local-reviewer",
			"--publisher-token", "local-publisher",
			"--reader-token", "local-reader",
			"--other-tenant-token", "other-tenant-admin",
			"--run-id", "smoke-contract",
			"--timeout", "10s",
		})
	})
	var report smokeReport
	err := json.Unmarshal(raw, &report)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "passed" || report.SnapshotID == "" || report.SnapshotETag == "" {
		t.Fatalf("unexpected smoke report: %#v", report)
	}
	if report.ARDEntries != 4 || report.AICatalogEntries != 3 || len(report.Resources) != 3 || len(report.Checks) != 75 {
		t.Fatalf("incomplete smoke report: %#v", report)
	}
	for _, check := range []string{
		"filter the exact disposable membership",
		"inspect disposable membership contributions",
		"deny cross-tenant membership contributions",
	} {
		if !slices.Contains(report.Checks, check) {
			t.Fatalf("smoke report is missing %q", check)
		}
	}
	kinds := make([]catalog.Kind, 0, len(report.Resources))
	for _, resource := range report.Resources {
		if resource.ResourceID == "" || resource.ListingID == "" || resource.ReleaseID == "" || resource.Identifier == "" {
			t.Fatalf("incomplete resource evidence: %#v", resource)
		}
		if (resource.Kind == catalog.KindSkill) != (resource.DocumentID != "") {
			t.Fatalf("unexpected bounded document evidence: %#v", resource)
		}
		kinds = append(kinds, resource.Kind)
	}
	for _, kind := range []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill} {
		if !slices.Contains(kinds, kind) {
			t.Fatalf("smoke report does not contain %q: %#v", kind, kinds)
		}
	}
}

func TestSmokeHTTPClientBindsRequestsAndFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.Header.Get("Accept") != "application/json" || request.Header.Get("Content-Type") != "application/json" || request.Header.Get("Authorization") != "Bearer token" || request.Header.Get("Idempotency-Key") != "command" || request.Header.Get("If-Match") != `"rev-1"` || request.Header.Get("If-None-Match") != `"cached"` {
			t.Fatalf("smoke request = %#v", request)
		}
		response.Header().Set("ETag", `"rev-2"`)
		response.WriteHeader(http.StatusCreated)
		_, _ = response.Write([]byte(`{"status":"created"}`))
	}))
	defer server.Close()

	client := &smokeHTTPClient{baseURL: server.URL, client: server.Client()}
	result, err := client.requestWithHeaders(context.Background(), "create", http.MethodPost, "/resource", "token", "command", `"rev-1"`, map[string]any{"name": "resource"}, map[string]string{"If-None-Match": `"cached"`}, http.StatusCreated)
	if err != nil || result.Header.Get("ETag") != `"rev-2"` || string(result.Body) != `{"status":"created"}` || len(client.checks) != 1 || client.checks[0] != "create" {
		t.Fatalf("smoke response = %#v, checks %#v, %v", result, client.checks, err)
	}
}

func TestSmokeStopsAfterInvalidNamespaceResponse(t *testing.T) {
	for _, tc := range []struct {
		name, method, command, want string
	}{
		{"missing create ID", http.MethodPost, "-namespace", "create namespace check: response ID or ETag is absent"},
		{"missing create ETag", http.MethodPost, "-namespace", "create namespace check: response ID or ETag is absent"},
		{"wrong update revision", http.MethodPatch, "-namespace-update", "update namespace metadata check: unexpected namespace revision"},
		{"wrong update ETag", http.MethodPatch, "-namespace-update", "update namespace metadata check: unexpected namespace revision"},
		{"redirect", http.MethodPost, "-namespace", "create namespace check: POST /api/v1/namespaces returned 307, expected 201:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := buildSmokeTestRuntime(t)
			defer runtime.Close()
			const runID = "invalid-namespace-response"
			var redirectHits atomic.Int32
			var redirectURL string
			if tc.name == "redirect" {
				destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					redirectHits.Add(1)
					w.WriteHeader(http.StatusNoContent)
				}))
				defer destination.Close()
				redirectURL = destination.URL
			}
			var mu sync.Mutex
			var intercepted, laterRequests int
			var retained catalog.Namespace
			handler := runtime.HTTP.Handler()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				if intercepted != 0 {
					laterRequests++
				}
				mu.Unlock()
				selected := r.Method == tc.method && r.Header.Get("Idempotency-Key") == runID+tc.command &&
					(r.URL.Path == "/api/v1/namespaces" || (tc.method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/v1/namespaces/")))
				if !selected {
					handler.ServeHTTP(w, r)
					return
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, r)
				var namespace catalog.Namespace
				status, revision := http.StatusCreated, int64(1)
				if tc.method == http.MethodPatch {
					status, revision = http.StatusOK, 2
				}
				if err := json.Unmarshal(response.Body.Bytes(), &namespace); err != nil || response.Code != status || namespace.ID == "" || namespace.Revision != revision || response.Header().Get("ETag") != fmt.Sprintf(`"rev-%d"`, revision) {
					t.Errorf("namespace fixture did not produce a valid response: status=%d error=%v", response.Code, err)
					http.Error(w, "invalid fixture response", http.StatusInternalServerError)
					return
				}
				if tc.method == http.MethodPatch && r.URL.Path != "/api/v1/namespaces/"+namespace.ID {
					t.Error("namespace update did not target the returned namespace")
					http.Error(w, "invalid fixture request", http.StatusInternalServerError)
					return
				}
				mu.Lock()
				intercepted++
				retained = namespace
				mu.Unlock()
				switch tc.name {
				case "missing create ID":
					namespace.ID = ""
				case "missing create ETag":
					response.Header().Del("ETag")
				case "wrong update revision":
					namespace.Revision = 1
				case "wrong update ETag":
					response.Header().Set("ETag", `"rev-1"`)
				case "redirect":
					status = http.StatusTemporaryRedirect
					response.Header().Set("Location", redirectURL)
				}
				body, err := json.Marshal(namespace)
				if err != nil {
					t.Error(err)
					http.Error(w, "invalid fixture encoding", http.StatusInternalServerError)
					return
				}
				response.Header().Del("Content-Length")
				for name, values := range response.Header() {
					w.Header()[name] = slices.Clone(values)
				}
				w.WriteHeader(status)
				_, _ = w.Write(body)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			report, err := executeSmoke(ctx, smokeOptions{ // #nosec G101 -- synthetic identities in this invocation's buildSmokeTestRuntime fixture.
				BaseURL: server.URL, AdminToken: "local-admin", ContributorToken: "local-contributor", ReviewerToken: "local-reviewer",
				PublisherToken: "local-publisher", ReaderToken: "local-reader", OtherTenantToken: "other-tenant-admin", RunID: runID, Timeout: 5 * time.Second,
			})
			server.Close()
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) || ctx.Err() != nil || !reflect.DeepEqual(report, smokeReport{}) {
				t.Fatalf("smoke did not stop at the intended response: report=%+v error=%v deadline=%v", report, err, ctx.Err())
			}
			mu.Lock()
			count, later, original := intercepted, laterRequests, retained
			mu.Unlock()
			if count != 1 || later != 0 || redirectHits.Load() != 0 {
				t.Fatalf("intercepted=%d later requests=%d redirect requests=%d", count, later, redirectHits.Load())
			}
			stored, err := runtime.Repository.GetNamespace(ctx, original.TenantID, original.ID)
			if err != nil || !reflect.DeepEqual(stored, original) {
				t.Fatalf("successful namespace mutation was not retained: %v", err)
			}
		})
	}
}

func TestSmokeStopsAfterInvalidSuccessfulResponse(t *testing.T) {
	const runID = "invalid-success-response"
	const domain = runID + ".catalog.example.com"
	identifiers := []string{
		"urn:air:" + domain + ":readiness:order-agent-" + runID,
		"urn:air:" + domain + ":readiness:order-mcp-" + runID,
		"urn:air:" + domain + ":readiness:order-skill-" + runID,
	}
	content, ok := smokeFixtures(runID)[2].Representation["content"].(string)
	if !ok || content == "" {
		t.Fatal("smoke Skill fixture lacks document content")
	}
	document := []byte(content)
	digest := sha256.Sum256(document)
	documentID := hex.EncodeToString(digest[:])
	for _, tc := range []struct {
		name, method, path, command, token, want string
	}{
		{"missing metrics counter", http.MethodGet, "/metrics", "", "", "prometheus metrics check failed: expected request counter is absent"},
		{"missing domain approval", http.MethodPost, "", "-approve-publication-domain", "local-admin", "approve publisher publication domain check: unexpected approval projection []string{}"},
		{"missing search generation", http.MethodPost, "/api/v1/catalog/search", "", "local-reader", "governed Catalog search check: profile or generation metadata is incomplete"},
		{"missing ARD member", http.MethodGet, "/.well-known/ard.json", "", "", "read active ARD manifest check: missing identifiers " + identifiers[0]},
		{"wrong public document digest", http.MethodGet, "/public/v1/artifacts/" + documentID, "", "", "read active public skill document check: exact content headers are incomplete"},
		{"missing publisher facet", http.MethodPost, "/ard/v1/explore", "", "", "explore public ARD Registry check: expected type and publisher facets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := buildSmokeTestRuntime(t)
			defer runtime.Close()
			var mu sync.Mutex
			var intercepted, laterRequests int
			var publisher catalog.Publisher
			var active publication.Snapshot
			handler := runtime.HTTP.Handler()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// The walkthrough is sequential. Serialize fixture observations too,
				// including any unexpected requests after the altered response.
				mu.Lock()
				defer mu.Unlock()
				if intercepted != 0 {
					laterRequests++
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, r)
				approvalPath := "/api/v1/publishers/" + publisher.ID + "/publication-domains/" + domain + ":approve"
				if r.Method == http.MethodPost && ((r.URL.Path == "/api/v1/publishers" && r.Header.Get("Idempotency-Key") == runID+"-publisher") ||
					(r.URL.Path == approvalPath && r.Header.Get("Idempotency-Key") == runID+"-approve-publication-domain")) {
					if err := json.Unmarshal(response.Body.Bytes(), &publisher); err != nil || publisher.ID == "" {
						t.Errorf("could not retain the original publisher response: %v", err)
						http.Error(w, "invalid fixture publisher", http.StatusInternalServerError)
						return
					}
				}
				if r.Method == http.MethodPost && r.Header.Get("Idempotency-Key") == runID+"-activate-snapshot" {
					if err := json.Unmarshal(response.Body.Bytes(), &active); err != nil || response.Code != http.StatusOK || active.ID == "" || active.State != publication.SnapshotActive || r.URL.Path != "/api/v1/publication/snapshots/"+active.ID+":activate" {
						t.Errorf("could not retain the activated snapshot: %v", err)
						http.Error(w, "invalid fixture activation", http.StatusInternalServerError)
						return
					}
				}
				path, command, authorization := tc.path, "", ""
				if tc.command != "" {
					path, command = approvalPath, runID+tc.command
				}
				if tc.token != "" {
					authorization = "Bearer " + tc.token
				}
				body := response.Body.Bytes()
				if r.Method == tc.method && r.URL.Path == path && r.URL.RawQuery == "" && r.Header.Get("Idempotency-Key") == command && r.Header.Get("Authorization") == authorization && r.Header.Get("If-None-Match") == "" {
					change := func() ([]byte, error) {
						if response.Code != http.StatusOK {
							return nil, fmt.Errorf("expected original HTTP 200, got %d", response.Code)
						}
						switch tc.name {
						case "missing metrics counter":
							const counter = "open_agentic_catalog_http_requests_total"
							if !bytes.Contains(body, []byte(counter)) {
								return nil, fmt.Errorf("original metrics lack the request counter")
							}
							return bytes.ReplaceAll(body, []byte(counter), []byte("unexpected_http_requests_total")), nil
						case "missing domain approval":
							if publisher.Revision != 3 || !slices.Equal(publisher.ApprovedPublicationDomains, []string{domain}) || r.Header.Get("If-Match") != `"rev-2"` || response.Header().Get("ETag") != `"rev-3"` {
								return nil, fmt.Errorf("original approval or revision binding is invalid")
							}
							changed := publisher
							changed.ApprovedPublicationDomains = []string{}
							return json.Marshal(changed)
						case "missing search generation":
							var search catalog.CatalogSearchResponse
							if err := json.Unmarshal(body, &search); err != nil {
								return nil, err
							}
							found := make([]string, 0, len(search.Items))
							for _, item := range search.Items {
								if item.Type == catalog.SearchResultLocal && item.Local != nil {
									found = append(found, item.Local.Resource.CanonicalIdentifier)
								}
							}
							if search.Profile != catalog.CoreSearchProfile || search.Tier != "core" || search.Generation == "" || len(missingSmokeIdentifiers(found, identifiers)) != 0 {
								return nil, fmt.Errorf("original search lacks generation, profile or expected resources")
							}
							search.Generation = ""
							return json.Marshal(search)
						case "missing ARD member":
							var manifest publication.ARDManifest
							if err := json.Unmarshal(body, &manifest); err != nil {
								return nil, err
							}
							found := make([]string, 0, len(manifest.Entries))
							for _, entry := range manifest.Entries {
								found = append(found, entry.Identifier)
							}
							if active.ID == "" || response.Header().Get("ETag") != active.ETag || response.Header().Get("Content-Digest") != active.ARDContentDigest || len(missingSmokeIdentifiers(found, identifiers)) != 0 {
								return nil, fmt.Errorf("original manifest lacks active headers or expected members")
							}
							manifest.Entries = slices.DeleteFunc(manifest.Entries, func(entry publication.Entry) bool { return entry.Identifier == identifiers[0] })
							return json.Marshal(manifest)
						case "wrong public document digest":
							if err := validateSmokeDocument(smokeHTTPResponse{Header: response.Header(), Body: body}, documentID, document, true); err != nil {
								return nil, err
							}
							wrongDigest := sha256.Sum256([]byte("different document"))
							response.Header().Set("Content-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(wrongDigest[:])+":")
							return body, nil
						case "missing publisher facet":
							var explore publication.ExploreResponse
							if err := json.Unmarshal(body, &explore); err != nil {
								return nil, err
							}
							if len(explore.Facets) != 2 || len(explore.Facets["type"].Buckets) != 3 || !slices.Equal(explore.Facets["publisher"].Buckets, []publication.ExploreBucket{{Value: domain, Count: 3}}) {
								return nil, fmt.Errorf("original Explore lacks the expected type/publisher facets: %#v", explore)
							}
							delete(explore.Facets, "publisher")
							return json.Marshal(explore)
						}
						return nil, fmt.Errorf("unknown response alteration %q", tc.name)
					}
					var err error
					body, err = change()
					if err != nil {
						t.Errorf("invalid original response: %v", err)
						http.Error(w, "invalid fixture response", http.StatusInternalServerError)
						return
					}
					intercepted++
					response.Header().Set("Content-Length", strconv.Itoa(len(body)))
				}
				for name, values := range response.Header() {
					w.Header()[name] = slices.Clone(values)
				}
				w.WriteHeader(response.Code)
				_, _ = w.Write(body)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			report, err := executeSmoke(ctx, smokeOptions{ // #nosec G101 -- synthetic identities in this invocation's buildSmokeTestRuntime fixture.
				BaseURL: server.URL, AdminToken: "local-admin", ContributorToken: "local-contributor", ReviewerToken: "local-reviewer",
				PublisherToken: "local-publisher", ReaderToken: "local-reader", OtherTenantToken: "other-tenant-admin", RunID: runID, Timeout: 10 * time.Second,
			})
			server.Close()
			if err == nil || err.Error() != tc.want || ctx.Err() != nil || !reflect.DeepEqual(report, smokeReport{}) {
				t.Fatalf("smoke did not stop at the intended check: report=%+v error=%v deadline=%v", report, err, ctx.Err())
			}
			mu.Lock()
			count, later, retainedPublisher, retainedSnapshot := intercepted, laterRequests, publisher, active
			mu.Unlock()
			if count != 1 || later != 0 {
				t.Fatalf("intercepted=%d later requests=%d", count, later)
			}
			if tc.name != "missing metrics counter" {
				stored, err := runtime.Repository.GetPublisher(ctx, retainedPublisher.TenantID, retainedPublisher.ID)
				if err != nil || retainedPublisher.Revision != 3 || !reflect.DeepEqual(stored, retainedPublisher) {
					t.Fatalf("successful publisher approval was not retained: %v", err)
				}
			}
			if tc.name == "missing ARD member" || tc.name == "wrong public document digest" || tc.name == "missing publisher facet" {
				stored, err := runtime.Publications["127.0.0.1"].Active(ctx, retainedPublisher.TenantID)
				if err != nil || retainedSnapshot.ID == "" || stored.ID != retainedSnapshot.ID || stored.ETag != retainedSnapshot.ETag || stored.State != publication.SnapshotActive || !reflect.DeepEqual(stored.ARD, retainedSnapshot.ARD) {
					t.Fatalf("successful snapshot activation was not retained: %v", err)
				}
			}
		})
	}
}

func buildSmokeTestRuntime(t *testing.T, configure ...func(*config.Config)) *platformruntime.Runtime {
	t.Helper()
	duration := func(value time.Duration) config.Duration { return config.Duration{Duration: value} }
	const tenantID = "00000000-0000-7000-8000-000000000001"
	configuration := config.Config{PublisherDomainVerification: config.PublisherDomainVerificationDefaults(), TrustVerification: config.TrustVerificationDefaults(), Ratings: config.Defaults().Ratings, ListingMedia: config.ListingMediaDefaults(), LogLevel: "info", LogFormat: "text", Validation: config.Defaults().Validation,
		Environment: "test", Profile: "smoke", ServiceName: "open-agentic-catalog-smoke",
		HTTP:     config.HTTP{Address: "127.0.0.1:0", PublicBaseURL: "https://catalog.example.com", ReadTimeout: duration(5 * time.Second), RequestTimeout: duration(time.Second), WriteTimeout: duration(10 * time.Second), IdleTimeout: duration(30 * time.Second), ShutdownTimeout: duration(10 * time.Second), MaxBodyBytes: 2 << 20},
		Database: config.Database{Driver: "memory", MaxConnLifetime: config.Defaults().Database.MaxConnLifetime, MaxConnIdleTime: config.Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: config.Defaults().Database.HealthCheckPeriod},
		Auth: config.Auth{Mode: "development", DevelopmentTokens: map[string]config.DevelopmentUser{
			"local-admin":        {Subject: "local-admin", TenantID: tenantID, Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor, identity.RoleReviewer, identity.RolePublisher, identity.RoleSourceAdmin, identity.RoleSecurityReviewer}},
			"local-contributor":  {Subject: "local-contributor", TenantID: tenantID, Roles: []identity.Role{identity.RoleContributor, identity.RoleReader}},
			"local-reviewer":     {Subject: "local-reviewer", TenantID: tenantID, Roles: []identity.Role{identity.RoleReviewer, identity.RoleReader}},
			"local-publisher":    {Subject: "local-publisher", TenantID: tenantID, Roles: []identity.Role{identity.RolePublisher, identity.RoleReader}},
			"local-reader":       {Subject: "local-reader", TenantID: tenantID, Roles: []identity.Role{identity.RoleReader}},
			"other-tenant-admin": {Subject: "other-admin", TenantID: "00000000-0000-7000-8000-000000000002", Roles: []identity.Role{identity.RoleTenantAdmin}},
		}},
		Jobs:        config.Jobs{MaxAttempts: 5, RetryCeiling: config.Duration{Duration: time.Hour}},
		Worker:      config.Worker{DependencyRetryInitialDelay: config.Duration{Duration: time.Second}, DependencyRetryCeiling: config.Duration{Duration: 30 * time.Second}, PollInterval: duration(time.Millisecond), LeaseDuration: duration(time.Minute), Concurrency: 2, SourceMaintenanceInterval: duration(15 * time.Minute), HousekeepingInterval: duration(5 * time.Minute), TerminalJobRetention: duration(168 * time.Hour), AcknowledgedOutboxRetention: duration(24 * time.Hour)},
		Federation:  config.Federation{RequestTimeout: duration(time.Second), MaxResponseBytes: 1 << 20, MaxRedirects: 2, MaxFanout: 2, MaxHops: 1, MaxLivePagesPerSource: 1},
		Discovery:   config.Discovery{Hosts: []config.DiscoveryHost{{Host: "127.0.0.1", TenantID: tenantID, Mode: "anonymous", PublisherDomain: "catalog.example.com"}}},
		Publication: config.Publication{ARDProfile: "ard-0.91-aa3e598", AICatalogProfile: "ai-catalog-1.0-b062278"},
	}
	for _, apply := range configure {
		apply(&configuration)
	}
	raw, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := platformruntime.Build(context.Background(), platformruntime.BuildOptions{ConfigPath: path, Version: "test", Commit: "test", Role: config.RoleAll, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}
