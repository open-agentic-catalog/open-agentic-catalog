// Public handler fixtures retain static snapshot documents behind existing ports;
// no candidate build, activation or composed publication workflow runs here.
package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

const publicFixtureHost = "catalog.example.com"
const publicFixtureOrigin = "https://" + publicFixtureHost
const publicFixtureARDProfile = "ard-0.91-aa3e598"
const publicFixtureAIProfile = "ai-catalog-1.0-b062278"

type publicSnapshotStore struct {
	publication.Store
	active func(context.Context, string) (publication.Snapshot, error)
}

func (s publicSnapshotStore) Active(ctx context.Context, tenant string) (publication.Snapshot, error) {
	return s.active(ctx, tenant)
}

type publicDocumentRepository struct {
	catalog.Repository
	document func(context.Context, string, string) (catalog.DocumentBlob, error)
}

func (r publicDocumentRepository) GetDocument(ctx context.Context, tenant, key string) (catalog.DocumentBlob, error) {
	return r.document(ctx, tenant, key)
}

func publicFixtureSnapshot(t *testing.T, mode DiscoveryMode) (publication.Snapshot, catalog.DocumentBlob) {
	t.Helper()
	content := []byte("---\nname: fixture-skill\ndescription: Review a static fixture.\n---\n\nStatic instructions.\n")
	sum := sha256.Sum256(content)
	key := hex.EncodeToString(sum[:])
	document := catalog.DocumentBlob{ID: key, TenantID: projectionTenant, Digest: "sha256:" + key, MediaType: "text/markdown", Size: int64(len(content)), Content: content, CreatedAt: projectionTime}
	if err := catalog.ValidateDocumentBlob(projectionTenant, document); err != nil {
		t.Fatal(err)
	}
	registry := publication.Entry{Context: publication.ARDContext, ID: "urn:air:" + publicFixtureHost + ":registry:open-agentic-catalog", Identifier: "urn:air:" + publicFixtureHost + ":registry:open-agentic-catalog", DisplayName: "Open Agentic Catalog Registry", Type: "application/ai-registry+json", URL: publicFixtureOrigin + "/ard/v1/", RepresentativeQueries: []string{"Discover catalog resources", "Search this agentic resource catalog"}, Description: "Registry endpoint for discovering public agentic resources.", Tags: []string{"catalog", "discovery", "registry"}, Metadata: map[string]any{"catalogReleaseId": "registry:self"}}
	s := publication.Snapshot{ID: projectionItem, TenantID: projectionTenant, Host: publicFixtureHost, Audience: publication.AudienceAnonymous, Generation: 7, ARDProfile: publicFixtureARDProfile, AICatalogProfile: publicFixtureAIProfile, State: publication.SnapshotActive, CreatedAt: projectionTime.Add(-time.Hour), ActivatedAt: &projectionTime, ArtifactIDs: []string{key}, Exclusions: []publication.Exclusion{{ResourceID: projectionResource, Identifier: "private-fixture-draft", Reasons: []string{"private-fixture-reason"}}}, ARD: publication.ARDManifest{Entries: []publication.Entry{registry}}, AICatalog: publication.AICatalog{SpecVersion: "1.0", Host: publication.AICatalogHost{DisplayName: "Open Agentic Catalog", Identifier: publicFixtureOrigin}, Entries: []publication.AICatalogEntry{}}}
	if mode == DiscoveryAuthenticated {
		s.Audience = publication.AudienceAuthenticated
	}
	for i, name := range []string{"Alpha", "Beta"} {
		identifier := "urn:air:" + publicFixtureHost + ":default:" + strings.ToLower(name)
		metadata := map[string]any{"catalogReleaseId": []string{projectionNext, projectionResource}[i], "catalogCreatedAt": s.CreatedAt.Format(time.RFC3339Nano)}
		url := publicFixtureOrigin + "/public/v1/artifacts/" + key
		entry := publication.Entry{Context: publication.ARDContext, ID: identifier, Identifier: identifier, DisplayName: name + " fixture", Type: "text/markdown", URL: url, RepresentativeQueries: []string{"Find the fixture", "Review a fixture"}, Description: "A static fixture for discovery.", Version: "1.0.0", Tags: []string{"fixture"}, UpdatedAt: projectionTime.Format(time.RFC3339Nano), Metadata: metadata}
		s.ARD.Entries = append(s.ARD.Entries, entry)
		s.AICatalog.Entries = append(s.AICatalog.Entries, publication.AICatalogEntry{Identifier: identifier, DisplayName: entry.DisplayName, Type: entry.Type, URL: url, Description: entry.Description, Version: entry.Version, Tags: entry.Tags, UpdatedAt: entry.UpdatedAt, Extensions: map[string]any{"https://ai-catalog.org/extensions/metadata": metadata}})
	}
	return sealPublicFixtureSnapshot(t, s), document
}

// sealPublicFixtureSnapshot binds static documents to their retained digests.
func sealPublicFixtureSnapshot(t *testing.T, s publication.Snapshot) publication.Snapshot {
	t.Helper()
	ard, err := json.Marshal(s.ARD)
	if err != nil {
		t.Fatal(err)
	}
	ai, err := json.Marshal(s.AICatalog)
	if err != nil {
		t.Fatal(err)
	}
	ardSum, aiSum := sha256.Sum256(ard), sha256.Sum256(ai)
	s.ETag = `"` + hex.EncodeToString(ardSum[:]) + `"`
	s.AICatalogETag = `"` + hex.EncodeToString(aiSum[:]) + `"`
	s.ARDContentDigest = digestHeader(ard)
	s.AIContentDigest = digestHeader(ai)
	if err := publication.ValidateSnapshotIntegrity(s); err != nil {
		t.Fatal(err)
	}
	return s
}
func publicProjectionServer(t *testing.T, mode DiscoveryMode, active func(context.Context, string) (publication.Snapshot, error)) *Server {
	t.Helper()
	if active == nil {
		active = func(context.Context, string) (publication.Snapshot, error) {
			t.Fatal("unexpected snapshot read")
			return publication.Snapshot{}, nil
		}
	}
	audience := publication.AudienceAnonymous
	if mode == DiscoveryAuthenticated {
		audience = publication.AudienceAuthenticated
	}
	manager, err := publication.NewManager(publication.Options{Records: fixedPublicationRecordSource{tenantID: projectionTenant}, Store: publicSnapshotStore{active: active}, Clock: clock.NewFake(projectionTime), IDs: id.Generator{}, BaseURL: publicFixtureOrigin, PublisherDomain: publicFixtureHost, Host: publicFixtureHost, Audience: audience, ARDProfile: publicFixtureARDProfile, AICatalogProfile: publicFixtureAIProfile, CursorKey: []byte(strings.Repeat("p", 32))})
	if err != nil {
		t.Fatal(err)
	}
	s := projectionServer(nil)
	s.discovery = map[string]DiscoveryHost{publicFixtureHost: {Host: publicFixtureHost, TenantID: projectionTenant, Mode: mode, PublisherDomain: publicFixtureHost, Publication: manager}}
	reader, _ := identity.FromContext(identityfixture.WithActor(t.Context(), identity.Actor{TenantID: projectionTenant, Subject: "reader", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleReader}, Claims: map[string]string{"scope:catalog.read": "true"}}))
	s.authenticator = discoveryAuthenticator{"reader": reader}
	s.enforceOAuthScopes = true
	return s
}
func requireARDProjectionProblem(t *testing.T, w *httptest.ResponseRecorder, status int, code catalog.ErrorCode) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	var got ardProblem
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != status || got.ErrorCode != code || got.Message == "" || len(fields) != 2 || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "private-fixture") {
		t.Fatalf("ARD problem=%d %s %v", w.Code, w.Body, w.Header())
	}
}

// Public authentication now reaches snapshot reads so Search cursors bind the
// current principal and permission decision. Preserve the other context checks.
func samePublicProjectionContext(got, want context.Context, mode DiscoveryMode) bool {
	if mode == DiscoveryAuthenticated {
		actor, ok := identity.FromContext(got)
		if !ok || actor.TenantID != projectionTenant || actor.Subject != "reader" {
			return false
		}
		want = identity.WithActor(want, actor)
	}
	return sameProjectionContext(got, want)
}
