package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

type federationSuccessFetcher struct {
	publicPolicyFetcher
	fetch func(context.Context, fetcher.Request) (fetcher.Result, error)
}

func (f federationSuccessFetcher) Fetch(c context.Context, r fetcher.Request) (fetcher.Result, error) {
	return f.fetch(c, r)
}

type federationInspectionProjectionStore struct {
	federationCommandStore
	save func(context.Context, federation.Inspection, ...federation.MutationEvidence) (federation.Inspection, error)
}

func (s federationInspectionProjectionStore) SaveInspection(c context.Context, v federation.Inspection, e ...federation.MutationEvidence) (federation.Inspection, error) {
	return s.save(c, v, e...)
}

func federationSuccessServer(t *testing.T, store federation.Store, fetch federation.Fetcher) *Server {
	t.Helper()
	entropy := make([]byte, 64)
	for index := range entropy {
		entropy[index] = byte(index / 16)
	}
	manager, err := federation.NewManager(store, fetch, clock.NewFake(projectionTime), id.Generator{Reader: bytes.NewReader(entropy)}, federation.WithEvidenceSink(federationProjectionEvidence{}, true))
	if err != nil {
		t.Fatal(err)
	}
	server := projectionServer(nil)
	server.federation = manager
	return server
}

// This document is static metadata: no listener, archive or remote artifact is
// contacted. Each test invokes one handler independently of source onboarding.
const federationSuccessManifest = `{"entries":[{"identifier":"urn:air:peer.example:default:fixture","displayName":"Fixture agent","type":"application/a2a-agent-card+json","tags":["fixture"],"url":"https://peer.example/agent.json","representativeQueries":["Find the fixture agent","Review the fixture agent"]}]}`

func federationSuccessFetchResult() fetcher.Result {
	// #nosec G101 -- synthetic credential revision evidence, not a secret.
	return fetcher.Result{RequestedURL: "https://peer.example/.well-known/ard.json", FinalURL: "https://peer.example/.well-known/ard.json", Status: http.StatusOK, MediaType: "application/ard+json", Body: []byte(federationSuccessManifest), BodyDigest: audit.DigestBytes([]byte(federationSuccessManifest)), PolicyID: "default", PolicyRevision: 2, PolicyDigest: (publicPolicyFetcher{}).PolicyDigest(), CredentialRevision: "credential-r2", FetchedAt: projectionTime}
}

func requireFederationSuccessEvidence(t *testing.T, ctx context.Context, actor identity.Actor, target, action string, evidence []federation.MutationEvidence) {
	t.Helper()
	requireFederationMutation(t, actor, target, evidence, action, "")
	record := evidence[0].Audit
	if record.RequestID != "federation-projection-request" || record.Details["commandId"] != projectionNext || record.Details["realmId"] != actor.RealmID || record.Details["authorizationDigest"] != fixtureDecisionDigest(t, ctx, actor) || !record.At.Equal(projectionTime) {
		t.Fatalf("source command lost execution attribution: %+v", record)
	}
}

func TestFederationInspectSuccessProjection(t *testing.T) {
	t.Parallel()
	actor := projectionActor(identity.RoleSourceAdmin)
	ctx := audit.WithExecution(identityfixture.WithActor(t.Context(), actor), audit.Execution{RequestID: "federation-projection-request", CommandID: projectionNext})
	result := federationSuccessFetchResult()
	fetches, saves := 0, 0
	fetch := federationSuccessFetcher{fetch: func(c context.Context, request fetcher.Request) (fetcher.Result, error) {
		fetches++
		if !sameProjectionContext(c, ctx) || request.URL != result.FinalURL || request.TenantID != actor.TenantID || request.NetworkProfile != "reviewed-network" || request.CredentialBinding != "source-credential" || len(request.Body) != 0 {
			t.Fatalf("inspection fetch binding=%+v", request)
		}
		return result, nil
	}}
	// Timestamp + zero entropy independently fix the first generated UUIDv7.
	// #nosec G101 -- credential binding and revision are synthetic non-secret identifiers.
	want := federation.Inspection{ID: "01a0beaf-fe00-7000-8000-000000000000", TenantID: actor.TenantID, RequestedURL: result.FinalURL, ResolvedURL: result.FinalURL, SourceType: federation.SourceARDManifest, ProfileID: publicFixtureARDProfile, MediaType: "application/ard+json", BodyDigest: result.BodyDigest, EntryCount: 1, KindCounts: map[string]int{"agent": 1}, Publishers: []string{"peer.example"}, Tags: []string{"fixture"}, Capabilities: map[string]bool{"enumerable": true}, NetworkProfile: "reviewed-network", CredentialBinding: "source-credential", PolicyID: "default", PolicyRevision: 2, PolicyDigest: result.PolicyDigest, CredentialRevision: "credential-r2", CreatedAt: projectionTime, ExpiresAt: projectionTime.Add(15 * time.Minute)}
	store := federationInspectionProjectionStore{save: func(c context.Context, got federation.Inspection, evidence ...federation.MutationEvidence) (federation.Inspection, error) {
		saves++
		if !sameProjectionContext(c, ctx) || !reflect.DeepEqual(got, want) {
			t.Fatalf("inspection=%+v want=%+v", got, want)
		}
		requireFederationSuccessEvidence(t, ctx, actor, want.ID, audit.ActionSourceInspected, evidence)
		facts, ok := evidence[0].Audit.Details["facts"].(map[string]any)
		if !ok || evidence[0].Outbox != nil || evidence[0].Audit.TargetType != "source_inspection" || facts["outcome"] != "succeeded" || facts["policyDigest"] != result.PolicyDigest {
			t.Fatalf("inspection evidence=%+v", evidence)
		}
		return got, nil
	}}
	server := federationSuccessServer(t, store, fetch)
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/federation/source-inspections", strings.NewReader(`{"url":"https://peer.example/.well-known/ard.json","networkProfile":"reviewed-network","credentialBinding":"source-credential"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleSourceInspect(response, request)
	requireFederationJSON(t, response, http.StatusCreated, want)
	if response.Header().Get("Location") != "/api/v1/federation/source-inspections/"+want.ID || response.Header().Get("ETag") != "" || fetches != 1 || saves != 1 {
		t.Fatalf("inspection receipt changed: headers=%v fetches=%d saves=%d", response.Header(), fetches, saves)
	}
}

func TestFederationSourceTestSuccessProjection(t *testing.T) {
	t.Parallel()
	actor := projectionActor(identity.RoleSourceAdmin)
	ctx := audit.WithExecution(identityfixture.WithActor(t.Context(), actor), audit.Execution{RequestID: "federation-projection-request", CommandID: projectionNext})
	source := federationFixtureSource()
	source.NetworkProfile, source.CredentialBinding, source.CredentialRevision = "reviewed-network", "source-credential", "credential-r1"
	unrelated := federationFixtureSource()
	unrelated.ID = projectionItem
	states := map[string]federation.Source{source.ID: source, unrelated.ID: unrelated}
	unrelatedBefore, err := json.Marshal(unrelated)
	if err != nil {
		t.Fatal(err)
	}
	result := federationSuccessFetchResult()
	reads, fetches, writes := 0, 0, 0
	fetch := federationSuccessFetcher{fetch: func(c context.Context, request fetcher.Request) (fetcher.Result, error) {
		fetches++
		if !sameProjectionContext(c, ctx) || request.URL != source.Endpoint || request.TenantID != actor.TenantID || request.NetworkProfile != source.NetworkProfile || request.CredentialBinding != source.CredentialBinding || len(request.Body) != 0 {
			t.Fatalf("test fetch binding=%+v", request)
		}
		return result, nil
	}}
	wantSource := source
	wantSource.Scope.Kinds = append([]catalog.Kind(nil), source.Scope.Kinds...)
	wantSource.Revision, wantSource.PolicyRevision, wantSource.CredentialRevision, wantSource.UpdatedAt = 4, 2, "credential-r2", projectionTime
	store := federationCommandStore{federationProjectionStore: federationProjectionStore{getSource: func(c context.Context, tenant, key string) (federation.Source, error) {
		reads++
		if !sameProjectionContext(c, ctx) || tenant != actor.TenantID || key != source.ID {
			t.Fatal("test source read lost tenant/path binding")
		}
		return states[key], nil
	}}, update: func(c context.Context, got federation.Source, expected int64, evidence ...federation.MutationEvidence) (federation.Source, error) {
		writes++
		if !sameProjectionContext(c, ctx) || expected != 3 || !reflect.DeepEqual(got, wantSource) {
			t.Fatalf("source test update=%+v expected=%d want=%+v", got, expected, wantSource)
		}
		requireFederationSuccessEvidence(t, ctx, actor, source.ID, audit.ActionSourceTested, evidence)
		facts, ok := evidence[0].Audit.Details["facts"].(map[string]any)
		if !ok || evidence[0].Audit.TargetType != "federation_source" || facts["beforeRevision"] != json.Number("3") || facts["afterRevision"] != json.Number("4") || facts["stateChanged"] != true || facts["outcome"] != "succeeded" || facts["bodyDigest"] != result.BodyDigest || evidence[0].Outbox == nil || evidence[0].Outbox.Subject != source.ID {
			t.Fatalf("test evidence=%+v", evidence)
		}
		states[got.ID] = got
		return got, nil
	}}
	server := federationSuccessServer(t, store, fetch)
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/federation/sources/"+source.ID+"/test", strings.NewReader(`{}`))
	request.SetPathValue("sourceId", source.ID)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"rev-3"`)
	response := httptest.NewRecorder()
	server.handleSourceTest(response, request)
	// #nosec G101 -- synthetic credential revision evidence, not a secret.
	want := federation.TestResult{SourceID: source.ID, SourceRevision: 4, Status: "succeeded", HTTPStatus: 200, MediaType: "application/ard+json", BodyDigest: result.BodyDigest, EntryCount: 1, Capabilities: map[string]bool{"enumerable": true}, ProfileID: publicFixtureARDProfile, PolicyID: "default", PolicyRevision: 2, PolicyDigest: result.PolicyDigest, CredentialRevision: "credential-r2", TestedAt: projectionTime}
	requireFederationJSON(t, response, http.StatusOK, want)
	unrelatedAfter, err := json.Marshal(states[unrelated.ID])
	if err != nil {
		t.Fatal(err)
	}
	if response.Header().Get("ETag") != `"rev-4"` || response.Header().Get("Location") != "" || reads != 1 || fetches != 1 || writes != 1 || !reflect.DeepEqual(states[source.ID], wantSource) || !bytes.Equal(unrelatedAfter, unrelatedBefore) {
		t.Fatalf("test receipt or unrelated source changed: headers=%v calls=%d/%d/%d", response.Header(), reads, fetches, writes)
	}
}
