//go:build functional

package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	platformruntime "github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/runtime"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

type response struct {
	status int
	header http.Header
	body   []byte
}

func TestEndToEndAuthorReviewPublishDiscoverAndWork(t *testing.T) {
	runtime := buildRuntime(t)
	defer runtime.Close()
	server := httptest.NewServer(runtime.HTTP.Handler())
	defer server.Close()

	requireStatus(t, request(t, server.URL, http.MethodGet, "/livez", "", "", "", nil), http.StatusOK)
	requireStatus(t, request(t, server.URL, http.MethodGet, "/readyz", "", "", "", nil), http.StatusOK)
	capabilities := request(t, server.URL, http.MethodGet, "/api/v1/capabilities", "admin", "", "", nil)
	requireStatus(t, capabilities, http.StatusOK)
	liveSearch := request(t, server.URL, http.MethodPost, "/api/v1/federation/sources:live-search", "admin", "e2e-live-search", "", map[string]any{"text": "invoice", "pageSize": 10, "reason": "Verify configured live federation"})
	requireStatus(t, liveSearch, http.StatusOK)

	namespaceResult := request(t, server.URL, http.MethodPost, "/api/v1/namespaces", "admin", "e2e-namespace", "", map[string]any{"slug": "payments", "displayName": "Payments"})
	requireStatus(t, namespaceResult, http.StatusCreated)
	var namespace catalog.Namespace
	decode(t, namespaceResult, &namespace)
	publisherResult := request(t, server.URL, http.MethodPost, "/api/v1/publishers", "contributor", "e2e-publisher", "", map[string]any{"displayName": "Example Publisher", "domains": []string{"example.com"}})
	requireStatus(t, publisherResult, http.StatusCreated)
	var publisher catalog.Publisher
	decode(t, publisherResult, &publisher)
	approveResult := request(t, server.URL, http.MethodPost, "/api/v1/publishers/"+publisher.ID+"/publication-domains/example.com:approve", "admin", "e2e-publication-domain-approve", `"rev-1"`, map[string]any{"reason": "approved for end-to-end publication"})
	requireStatus(t, approveResult, http.StatusOK)

	resourceResult := request(t, server.URL, http.MethodPost, "/api/v1/resources", "contributor", "e2e-resource", "", map[string]any{"namespaceId": namespace.ID, "canonicalIdentifier": "urn:air:example.com:payments:invoice-resolution-agent", "kind": "agent"})
	requireStatus(t, resourceResult, http.StatusCreated)
	var resource catalog.Resource
	decode(t, resourceResult, &resource)
	versionResult := request(t, server.URL, http.MethodPost, "/api/v1/resources/"+resource.ID+"/versions", "contributor", "e2e-version", "", map[string]any{"version": "1.0.0", "representations": []map[string]any{{"role": "primary", "mediaType": adapter.MediaA2AAgentCard, "data": map[string]any{"name": "Invoice resolution agent", "description": "Resolves invoice exceptions", "version": "1.0.0", "supportedInterfaces": []map[string]any{{"url": "https://agents.example.com/invoice-resolution", "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}}, "capabilities": map[string]any{}, "defaultInputModes": []string{"application/json"}, "defaultOutputModes": []string{"application/json"}, "skills": []map[string]any{{"id": "resolve-invoices", "name": "Resolve invoices", "description": "Resolves invoice exceptions", "tags": []string{"invoices"}}}}}}})
	requireStatus(t, versionResult, http.StatusCreated)
	var versionEnvelope struct {
		Revision catalog.VersionRevision `json:"revision"`
		Evidence catalog.EvidenceSet     `json:"evidence"`
	}
	decode(t, versionResult, &versionEnvelope)
	if versionEnvelope.Evidence.ValidationProfile != "catalog-validation-v7" || versionEnvelope.Evidence.LegalRights.State != catalog.LegalRightsNotAssessed || versionEnvelope.Evidence.Trust.State != catalog.TrustNotAssessed {
		t.Fatalf("version evidence = %#v", versionEnvelope.Evidence)
	}
	initialEvidenceID := versionEnvelope.Evidence.ID
	validationResult := request(t, server.URL, http.MethodPost, "/api/v1/version-revisions/"+versionEnvelope.Revision.ID+":validate", "contributor", "e2e-validation", "", map[string]any{})
	requireStatus(t, validationResult, http.StatusCreated)
	decode(t, validationResult, &versionEnvelope)
	if versionEnvelope.Evidence.ID == initialEvidenceID || versionEnvelope.Evidence.State != catalog.EvidenceComplete || !versionEnvelope.Evidence.ExpiresAt.IsZero() || versionEnvelope.Revision.EvidenceSetID != versionEnvelope.Evidence.ID {
		t.Fatalf("renewed version evidence = %#v / %#v", versionEnvelope.Revision, versionEnvelope.Evidence)
	}
	listingResult := request(t, server.URL, http.MethodPost, "/api/v1/catalog-listings", "contributor", "e2e-listing", "", map[string]any{"namespaceId": namespace.ID, "resourceId": resource.ID})
	requireStatus(t, listingResult, http.StatusCreated)
	var listing catalog.CatalogListing
	decode(t, listingResult, &listing)
	listingRevisionResult := request(t, server.URL, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+"/revisions", "contributor", "e2e-listing-revision", listingResult.header.Get("ETag"), map[string]any{"versionRevisionId": versionEnvelope.Revision.ID, "displayName": "Invoice resolution agent", "description": "Resolve invoice exceptions across connected finance systems.", "representativeQueries": []string{"Resolve a disputed invoice", "Investigate an invoice payment exception"}, "publisherId": publisher.ID, "visibility": "public", "license": "Apache-2.0", "tags": []string{"finance", "invoices"}})
	requireStatus(t, listingRevisionResult, http.StatusCreated)
	var listingRevisionEnvelope struct {
		Listing catalog.CatalogListing `json:"listing"`
	}
	decode(t, listingRevisionResult, &listingRevisionEnvelope)
	submissionResult := request(t, server.URL, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+"/submissions", "contributor", "e2e-submit", "", map[string]any{})
	requireStatus(t, submissionResult, http.StatusCreated)
	var submission catalog.ReviewSubmission
	decode(t, submissionResult, &submission)
	requireStatus(t, request(t, server.URL, http.MethodPost, "/api/v1/review-submissions/"+submission.ID+":approve", "reviewer", "e2e-approve", `"rev-1"`, map[string]any{"reason": "independent review complete"}), http.StatusOK)
	publishResult := request(t, server.URL, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+":publish", "publisher", "e2e-publish", revisionETag(listingRevisionEnvelope.Listing.Revision), map[string]any{"submissionId": submission.ID})
	requireStatus(t, publishResult, http.StatusCreated)

	skillResourceResult := request(t, server.URL, http.MethodPost, "/api/v1/resources", "contributor", "e2e-skill-resource", "", map[string]any{"namespaceId": namespace.ID, "canonicalIdentifier": "urn:air:example.com:payments:invoice-reconciliation-skill", "kind": "skill"})
	requireStatus(t, skillResourceResult, http.StatusCreated)
	var skillResource catalog.Resource
	decode(t, skillResourceResult, &skillResource)
	skillPackage := e2eSkillPackageZIP(t)
	skillVersionResult := request(t, server.URL, http.MethodPost, "/api/v1/resources/"+skillResource.ID+"/versions", "contributor", "e2e-skill-version", "", map[string]any{"version": "1.0.0", "representations": []map[string]any{{"role": "primary", "mediaType": adapter.MediaSkillZIP, "packageData": skillPackage, "handoff": map[string]any{"git": map[string]any{"repository": "https://git.example.com/finance/skills.git", "commit": strings.Repeat("a", 40), "directory": "skills/invoice-reconciliation"}, "oci": map[string]any{"reference": "registry.example.com/finance/invoice-reconciliation@sha256:" + strings.Repeat("b", 64)}}}}})
	requireStatus(t, skillVersionResult, http.StatusCreated)
	var skillVersionEnvelope struct {
		Revision catalog.VersionRevision `json:"revision"`
		Evidence catalog.EvidenceSet     `json:"evidence"`
	}
	decode(t, skillVersionResult, &skillVersionEnvelope)
	if skillVersionEnvelope.Evidence.State != catalog.EvidenceComplete || skillVersionEnvelope.Revision.Representations[0].Package == nil || skillVersionEnvelope.Revision.Representations[0].Package.FileCount != 2 {
		t.Fatalf("Skill package version = %#v / %#v", skillVersionEnvelope.Revision, skillVersionEnvelope.Evidence)
	}
	initialSkillEvidenceID := skillVersionEnvelope.Evidence.ID
	skillValidationResult := request(t, server.URL, http.MethodPost, "/api/v1/version-revisions/"+skillVersionEnvelope.Revision.ID+":validate", "contributor", "e2e-skill-validation", "", map[string]any{})
	requireStatus(t, skillValidationResult, http.StatusCreated)
	decode(t, skillValidationResult, &skillVersionEnvelope)
	if skillVersionEnvelope.Evidence.ID == initialSkillEvidenceID || skillVersionEnvelope.Evidence.State != catalog.EvidenceComplete || skillVersionEnvelope.Revision.Representations[0].Package == nil || skillVersionEnvelope.Revision.EvidenceSetID != skillVersionEnvelope.Evidence.ID {
		t.Fatalf("renewed Skill package evidence = %#v / %#v", skillVersionEnvelope.Revision, skillVersionEnvelope.Evidence)
	}
	skillListingResult := request(t, server.URL, http.MethodPost, "/api/v1/catalog-listings", "contributor", "e2e-skill-listing", "", map[string]any{"namespaceId": namespace.ID, "resourceId": skillResource.ID})
	requireStatus(t, skillListingResult, http.StatusCreated)
	var skillListing catalog.CatalogListing
	decode(t, skillListingResult, &skillListing)
	skillListingRevisionResult := request(t, server.URL, http.MethodPost, "/api/v1/catalog-listings/"+skillListing.ID+"/revisions", "contributor", "e2e-skill-listing-revision", skillListingResult.header.Get("ETag"), map[string]any{"versionRevisionId": skillVersionEnvelope.Revision.ID, "displayName": "Invoice reconciliation Skill", "description": "Reconcile disputed invoices using a safely inspected external package.", "representativeQueries": []string{"Reconcile a disputed invoice", "Investigate invoice reconciliation steps"}, "publisherId": publisher.ID, "visibility": "public", "license": "Apache-2.0", "tags": []string{"finance", "invoices"}})
	requireStatus(t, skillListingRevisionResult, http.StatusCreated)
	var skillListingRevisionEnvelope struct {
		Listing catalog.CatalogListing `json:"listing"`
	}
	decode(t, skillListingRevisionResult, &skillListingRevisionEnvelope)
	skillSubmissionResult := request(t, server.URL, http.MethodPost, "/api/v1/catalog-listings/"+skillListing.ID+"/submissions", "contributor", "e2e-skill-submit", "", map[string]any{})
	requireStatus(t, skillSubmissionResult, http.StatusCreated)
	var skillSubmission catalog.ReviewSubmission
	decode(t, skillSubmissionResult, &skillSubmission)
	requireStatus(t, request(t, server.URL, http.MethodPost, "/api/v1/review-submissions/"+skillSubmission.ID+":approve", "reviewer", "e2e-skill-approve", `"rev-1"`, map[string]any{"reason": "package structure and handoff reviewed"}), http.StatusOK)
	skillPublishResult := request(t, server.URL, http.MethodPost, "/api/v1/catalog-listings/"+skillListing.ID+":publish", "publisher", "e2e-skill-publish", revisionETag(skillListingRevisionEnvelope.Listing.Revision), map[string]any{"submissionId": skillSubmission.ID})
	requireStatus(t, skillPublishResult, http.StatusCreated)
	var skillPublishEnvelope struct {
		Release catalog.ListingRelease `json:"release"`
	}
	decode(t, skillPublishResult, &skillPublishEnvelope)
	skillRecordResult := request(t, server.URL, http.MethodGet, "/api/v1/catalog/resources/"+skillPublishEnvelope.Release.ID, "reader", "", "", nil)
	requireStatus(t, skillRecordResult, http.StatusOK)
	var skillRecord catalog.CatalogRecord
	decode(t, skillRecordResult, &skillRecord)
	skillRepresentation := skillRecord.VersionRevision.Representations[0]
	if skillRepresentation.Package == nil || skillRepresentation.Handoff == nil || skillRepresentation.Handoff.Git == nil || skillRepresentation.Handoff.OCI == nil || skillRepresentation.Handoff.Git.Commit != strings.Repeat("a", 40) || bytes.Contains(skillRecordResult.body, []byte("packageData")) {
		t.Fatalf("authenticated Skill handoff = %#v", skillRepresentation)
	}

	searchResult := request(t, server.URL, http.MethodPost, "/api/v1/catalog/search", "reader", "", "", map[string]any{"text": "invoice", "kinds": []string{"agent"}})
	requireStatus(t, searchResult, http.StatusOK)
	var search catalog.CatalogSearchResponse
	decode(t, searchResult, &search)
	if search.DurableTotal != 1 || search.LiveCount != 0 || search.Profile != catalog.CoreSearchProfile || search.Tier != "core" || search.Partial || len(search.Items) != 1 || search.Items[0].Type != catalog.SearchResultLocal || search.Items[0].Local == nil || search.Items[0].Local.Resource.ID != resource.ID {
		t.Fatalf("catalog search = %#v", search)
	}

	publisherActor, err := runtime.Authenticator.Authenticate(context.Background(), "publisher")
	if err != nil {
		t.Fatal(err)
	}
	actorContext := identity.WithActor(context.Background(), publisherActor)
	job, existing, err := runtime.Jobs.Enqueue(actorContext, jobs.EnqueueInput{Kind: "publication.generate", Payload: json.RawMessage(`{}`), DeduplicationKey: "e2e-publication"})
	if err != nil || existing {
		t.Fatalf("enqueue publication job = %#v, existing=%v, err=%v", job, existing, err)
	}
	if err := runtime.JobWorker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	jobView, err := runtime.Jobs.Get(actorContext, job.ID)
	if err != nil || jobView.State != jobs.StateSucceeded || jobView.Progress != 100 {
		t.Fatalf("publication job = %#v, %v", jobView, err)
	}
	if jobView.Result == nil || jobView.Result.Type != "publication_snapshot" || jobView.Result.SnapshotID == "" || jobView.Result.Host != "127.0.0.1" {
		t.Fatalf("publication job lacks an exact snapshot/host reference: %#v", jobView.Result)
	}
	jobSnapshot := request(t, server.URL, http.MethodGet, "/api/v1/publication/snapshots/"+jobView.Result.SnapshotID+"?host="+jobView.Result.Host, "publisher", "", "", nil)
	requireStatus(t, jobSnapshot, http.StatusOK)
	var generatedSnapshot publication.Snapshot
	decode(t, jobSnapshot, &generatedSnapshot)
	if generatedSnapshot.ID != jobView.Result.SnapshotID || generatedSnapshot.Host != jobView.Result.Host || generatedSnapshot.State != publication.SnapshotCandidate {
		t.Fatalf("publication Job reference resolved a different snapshot: %#v", generatedSnapshot)
	}

	snapshotResult := request(t, server.URL, http.MethodPost, "/api/v1/publication/snapshots", "publisher", "e2e-snapshot", "", map[string]any{"host": "127.0.0.1"})
	requireStatus(t, snapshotResult, http.StatusCreated)
	var snapshot publication.Snapshot
	decode(t, snapshotResult, &snapshot)
	foundSkillExclusion := false
	for _, exclusion := range snapshot.Exclusions {
		if exclusion.ResourceID != skillResource.ID {
			continue
		}
		for _, reason := range exclusion.Reasons {
			foundSkillExclusion = foundSkillExclusion || reason == "PUBLIC_DELIVERY_FORM_REQUIRED"
		}
	}
	if !foundSkillExclusion {
		t.Fatalf("snapshot did not explain the externally handed-off Skill exclusion: %#v", snapshot.Exclusions)
	}
	requireStatus(t, request(t, server.URL, http.MethodPost, "/api/v1/publication/snapshots/"+snapshot.ID+":activate", "publisher", "e2e-activate", snapshot.ETag, map[string]any{"host": "127.0.0.1", "reason": "Activate the reviewed end-to-end snapshot"}), http.StatusOK)
	manifest := request(t, server.URL, http.MethodGet, "/.well-known/ard.json", "", "", "", nil)
	requireStatus(t, manifest, http.StatusOK)
	if manifest.header.Get("Content-Digest") == "" || manifest.header.Get("ETag") != snapshot.ETag {
		t.Fatalf("public manifest headers = %#v", manifest.header)
	}
	registrySearch := request(t, server.URL, http.MethodPost, "/ard/v1/search", "", "", "", map[string]any{"query": map[string]any{"text": "resolve invoice"}, "federation": "none"})
	requireStatus(t, registrySearch, http.StatusOK)
	var publicSearch publication.SearchResponse
	decode(t, registrySearch, &publicSearch)
	if len(publicSearch.Results) != 1 || publicSearch.Results[0].Identifier != resource.CanonicalIdentifier {
		t.Fatalf("public search = %#v", publicSearch)
	}
	audit := request(t, server.URL, http.MethodGet, "/api/v1/audit-events?limit=100", "admin", "", "", nil)
	requireStatus(t, audit, http.StatusOK)
	var audits struct {
		Items []catalog.AuditEvent `json:"items"`
	}
	decode(t, audit, &audits)
	if len(audits.Items) < 9 {
		t.Fatalf("only %d audit events recorded", len(audits.Items))
	}

	replay := request(t, server.URL, http.MethodPost, "/api/v1/resources", "contributor", "e2e-resource", "", map[string]any{"namespaceId": namespace.ID, "canonicalIdentifier": "urn:air:example.com:payments:invoice-resolution-agent", "kind": "agent"})
	requireStatus(t, replay, http.StatusCreated)
	if replay.header.Get("Idempotency-Replayed") != "true" {
		t.Fatal("end-to-end command replay was not identified")
	}
	crossTenant := request(t, server.URL, http.MethodGet, "/api/v1/resources/"+resource.ID, "other-admin", "", "", nil)
	requireStatus(t, crossTenant, http.StatusNotFound)
}

const testTenantID = "00000000-0000-7000-8000-000000000001"

func buildRuntime(t *testing.T) *platformruntime.Runtime {
	t.Helper()
	duration := func(value time.Duration) config.Duration { return config.Duration{Duration: value} }
	cfg := config.Config{PublisherDomainVerification: config.PublisherDomainVerificationDefaults(), TrustVerification: config.TrustVerificationDefaults(), Ratings: config.Defaults().Ratings, ListingMedia: config.ListingMediaDefaults(), LogLevel: "info", LogFormat: "text", Validation: config.Defaults().Validation,
		Environment: "test",
		Profile:     "e2e",
		ServiceName: "open-agentic-catalog-e2e",
		HTTP:        config.HTTP{Address: "127.0.0.1:0", PublicBaseURL: "https://catalog.example.com", ReadTimeout: duration(5 * time.Second), RequestTimeout: duration(time.Second), WriteTimeout: duration(10 * time.Second), IdleTimeout: duration(30 * time.Second), ShutdownTimeout: duration(10 * time.Second), MaxBodyBytes: 2 << 20},
		Database:    config.Database{Driver: "memory", MaxConnLifetime: config.Defaults().Database.MaxConnLifetime, MaxConnIdleTime: config.Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: config.Defaults().Database.HealthCheckPeriod},
		Auth: config.Auth{Mode: "development", DevelopmentTokens: map[string]config.DevelopmentUser{
			"admin":       {Subject: "admin", TenantID: testTenantID, Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor, identity.RolePublisher}},
			"contributor": {Subject: "contributor", TenantID: testTenantID, Roles: []identity.Role{identity.RoleContributor, identity.RoleReader}},
			"reviewer":    {Subject: "reviewer", TenantID: testTenantID, Roles: []identity.Role{identity.RoleReviewer, identity.RoleReader}},
			"publisher":   {Subject: "publisher", TenantID: testTenantID, Roles: []identity.Role{identity.RolePublisher, identity.RoleReader}},
			"reader":      {Subject: "reader", TenantID: testTenantID, Roles: []identity.Role{identity.RoleReader}},
			"other-admin": {Subject: "other-admin", TenantID: "00000000-0000-7000-8000-000000000002", Roles: []identity.Role{identity.RoleTenantAdmin}},
		}},
		Jobs:        config.Jobs{MaxAttempts: 5, RetryCeiling: config.Duration{Duration: time.Hour}},
		Worker:      config.Worker{DependencyRetryInitialDelay: config.Duration{Duration: time.Second}, DependencyRetryCeiling: config.Duration{Duration: 30 * time.Second}, PollInterval: duration(time.Millisecond), LeaseDuration: duration(time.Minute), Concurrency: 2, SourceMaintenanceInterval: duration(15 * time.Minute), HousekeepingInterval: duration(5 * time.Minute), TerminalJobRetention: duration(168 * time.Hour), AcknowledgedOutboxRetention: duration(24 * time.Hour)},
		Federation:  config.Federation{RequestTimeout: duration(time.Second), MaxResponseBytes: 1 << 20, MaxRedirects: 2, MaxFanout: 2, MaxHops: 1, MaxLivePagesPerSource: 1},
		Discovery:   config.Discovery{Hosts: []config.DiscoveryHost{{Host: "127.0.0.1", TenantID: testTenantID, Mode: "anonymous", PublisherDomain: "catalog.example.com"}}},
		Publication: config.Publication{ARDProfile: "ard-0.91-aa3e598", AICatalogProfile: "ai-catalog-1.0-b062278"},
	}
	raw, err := json.Marshal(cfg)
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

func request(t *testing.T, baseURL, method, path, token, idempotencyKey, ifMatch string, body any) response {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	httpRequest, err := http.NewRequestWithContext(context.Background(), method, baseURL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+token)
	}
	if idempotencyKey != "" {
		httpRequest.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if ifMatch != "" {
		httpRequest.Header.Set("If-Match", ifMatch)
	}
	if body != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	httpResponse, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	responseBody, err := io.ReadAll(httpResponse.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{status: httpResponse.StatusCode, header: httpResponse.Header.Clone(), body: responseBody}
}

func requireStatus(t *testing.T, result response, want int) {
	t.Helper()
	if result.status != want {
		t.Fatalf("status = %d, want %d; body = %s", result.status, want, result.body)
	}
}

func decode(t *testing.T, result response, target any) {
	t.Helper()
	if err := json.Unmarshal(result.body, target); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, result.body)
	}
}

func revisionETag(revision int64) string {
	return `"rev-` + strconv.FormatInt(revision, 10) + `"`
}

func e2eSkillPackageZIP(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range []struct {
		name    string
		content string
	}{
		{name: "invoice-reconciliation/SKILL.md", content: "---\nname: invoice-reconciliation\ndescription: Reconcile disputed invoices safely.\n---\n# Instructions\n"},
		{name: "invoice-reconciliation/scripts/reconcile.sh", content: "#!/bin/sh\nexit 99\n"},
	} {
		file, err := writer.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(entry.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
