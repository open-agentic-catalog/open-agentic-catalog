// Rating transport units substitute persistence and target admission. Real
// authorization, replay and SQL concurrency remain separately qualified.
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

type ratingProjectionStore struct {
	ratings.Store
	get         func(context.Context, string, string) (ratings.Record, error)
	own         func(context.Context, string, ratings.Target, string) (ratings.Record, error)
	save        func(context.Context, ratings.Record, int64, ratings.Admission, audit.Record) (ratings.Record, error)
	list        func(context.Context, string, ratings.ListInput) ([]ratings.Record, error)
	aggregates  func(context.Context, string, []ratings.Target) (map[string]ratings.Aggregate, error)
	sharing     func(context.Context, string, string, string) (ratings.Sharing, error)
	saveSharing func(context.Context, ratings.Sharing, int64, audit.Record) (ratings.Sharing, error)
}

func (s ratingProjectionStore) Get(c context.Context, t, k string) (ratings.Record, error) {
	return s.get(c, t, k)
}
func (s ratingProjectionStore) Own(c context.Context, t string, k ratings.Target, p string) (ratings.Record, error) {
	return s.own(c, t, k, p)
}
func (s ratingProjectionStore) Save(c context.Context, r ratings.Record, e int64, a ratings.Admission, v audit.Record) (ratings.Record, error) {
	return s.save(c, r, e, a, v)
}
func (s ratingProjectionStore) List(c context.Context, t string, q ratings.ListInput) ([]ratings.Record, error) {
	return s.list(c, t, q)
}
func (s ratingProjectionStore) Aggregates(c context.Context, t string, q []ratings.Target) (map[string]ratings.Aggregate, error) {
	return s.aggregates(c, t, q)
}
func (s ratingProjectionStore) GetSharing(c context.Context, t, h, a string) (ratings.Sharing, error) {
	return s.sharing(c, t, h, a)
}
func (s ratingProjectionStore) SaveSharing(c context.Context, r ratings.Sharing, e int64, a audit.Record) (ratings.Sharing, error) {
	return s.saveSharing(c, r, e, a)
}

type ratingProjectionAuthority struct{}

func (ratingProjectionAuthority) Admit(_ context.Context, a ratings.Admission) (ratings.Admission, error) {
	return a, nil
}
func ratingProjectionServer(t *testing.T, store ratingProjectionStore) *Server {
	t.Helper()
	s := projectionServer(nil)
	var err error
	s.ratings, err = ratings.New(store, ratingProjectionAuthority{}, clock.NewFake(projectionTime), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	s.discovery = map[string]DiscoveryHost{"catalog.example.com": {Host: "catalog.example.com", TenantID: projectionTenant, Mode: DiscoveryAnonymous}}
	return s
}
func ratingProjectionRecord(t *testing.T, ctx context.Context) ratings.Record {
	t.Helper()
	a, _ := identity.FromContext(ctx)
	key, err := ratings.PrincipalKey(a)
	if err != nil {
		t.Fatal(err)
	}
	return ratings.Record{ID: projectionItem, TenantID: projectionTenant, PrincipalKey: key, SubmittedBy: a.MembershipID, Target: ratings.Target{Origin: "foreign", SourceID: projectionNext, PresentationGeneration: 1, Identifier: federationFixtureAIR, Kind: catalog.KindAgent}, Score: 4, Consent: true, State: "active", Revision: 3, CreatedAt: projectionTime, UpdatedAt: projectionTime}
}
func invokeRatingUnit(ctx context.Context, handler http.HandlerFunc, method, path, body, etag string, pathValues map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if etag != "" {
		r.Header.Set("If-Match", etag)
	}
	for k, v := range pathValues {
		r.SetPathValue(k, v)
	}
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func TestOwnRatingCommandsRetainOAuthAndHumanAdmission(t *testing.T) {
	for _, action := range []string{"submit", "withdraw"} {
		for _, test := range []struct {
			name    string
			enforce bool
			scope   bool
			service bool
			noRoles bool
			status  int
		}{
			{"human with write scope", true, true, false, false, http.StatusOK},
			{"human missing write scope", true, false, false, false, http.StatusForbidden},
			{"service with write scope", true, true, true, false, http.StatusForbidden},
			{"enforcement disabled", false, false, false, false, http.StatusOK},
			{"owner without current grants", true, true, false, true, http.StatusOK},
		} {
			t.Run(action+"/"+test.name, func(t *testing.T) {
				ctx := ratingProjectionContext(t.Context())
				old := ratingProjectionRecord(t, ctx)
				actor, _ := identity.FromContext(ctx)
				actor.Claims = map[string]string{"scope:catalog.read": "true"}
				if test.scope {
					actor.Claims["scope:ratings.write"] = "true"
				}
				if test.service {
					actor.ActorType = identity.ActorService
				}
				if test.noRoles {
					actor.Roles = nil
				}
				ctx = identityfixture.WithActor(t.Context(), actor)
				writes := 0
				s := ratingProjectionServer(t, ratingProjectionStore{
					get: func(context.Context, string, string) (ratings.Record, error) { return old, nil },
					own: func(context.Context, string, ratings.Target, string) (ratings.Record, error) { return old, nil },
					save: func(_ context.Context, value ratings.Record, expected int64, _ ratings.Admission, _ audit.Record) (ratings.Record, error) {
						writes++
						if expected != old.Revision || value.Revision != old.Revision+1 || value.PrincipalKey != old.PrincipalKey || value.Target != old.Target {
							t.Fatal("rating owner, target or revision changed")
						}
						if action == "withdraw" && (value.State != "withdrawn" || value.Score != 0 || value.Consent) {
							t.Fatal("withdrawal retained score or consent")
						}
						return value, nil
					},
				})
				handler, path := http.HandlerFunc(s.handleRatingSubmit), "/api/v1/ratings"
				body, err := json.Marshal(map[string]any{"target": old.Target, "score": 2})
				if err != nil {
					t.Fatal(err)
				}
				values := map[string]string{}
				if action == "withdraw" {
					handler, path = s.handleRatingCommand, path+"/"+old.ID+":withdraw"
					values["ratingCommand"] = old.ID + ":withdraw"
					body = []byte(`{}`)
				}
				status := test.status
				if action == "submit" && test.noRoles {
					status = http.StatusForbidden
				}
				guard := authorizationScopes(test.enforce, handler)
				w := invokeRatingUnit(ctx, guard.ServeHTTP, http.MethodPost, path, string(body), `"rev-3"`, values)
				if status == http.StatusForbidden {
					requireProjectionProblem(t, w, status, catalog.CodeForbidden)
					if writes != 0 {
						t.Fatal("denied rating command persisted")
					}
					if test.enforce && !test.scope && (w.Header().Get("WWW-Authenticate") != `Bearer error="insufficient_scope", scope="ratings.write"` || !strings.Contains(w.Body.String(), "OAuth scope ratings.write is required")) {
						t.Fatal("missing write scope did not reach its admission guard")
					}
				} else if w.Code != status || writes != 1 {
					t.Fatalf("allowed rating command: status=%d writes=%d", w.Code, writes)
				}
			})
		}
	}
}
func TestRatingCommandProjection(t *testing.T) {
	for _, action := range []string{"create", "edit", "withdraw", "remove", "permit"} {
		for _, failure := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failure=%t", action, failure), func(t *testing.T) {
				ctx := ratingProjectionContext(t.Context())
				old := ratingProjectionRecord(t, ctx)
				if action == "permit" {
					old.State = "removed"
					old.Score = 0
					old.Consent = false
				}
				writes := 0
				store := ratingProjectionStore{
					get: func(_ context.Context, tenant, key string) (ratings.Record, error) {
						if tenant != old.TenantID || key != old.ID {
							t.Fatal("lookup binding")
						}
						return old, nil
					},
					own: func(_ context.Context, tenant string, target ratings.Target, key string) (ratings.Record, error) {
						if tenant != old.TenantID || target != old.Target || key != old.PrincipalKey {
							t.Fatal("owner binding")
						}
						if action == "create" {
							return ratings.Record{}, catalog.E(catalog.CodeNotFound, "absent")
						}
						return old, nil
					},
					save: func(_ context.Context, r ratings.Record, expected int64, a ratings.Admission, e audit.Record) (ratings.Record, error) {
						writes++
						want := int64(3)
						auditAction := action
						if action == "edit" || action == "create" {
							auditAction = "submit"
						}
						if action == "create" {
							want = 0
						}
						if expected != want || r.Revision != want+1 || r.Target != old.Target || r.PrincipalKey != old.PrincipalKey || e.Action != "rating."+auditAction || e.TargetID != r.ID || audit.ValidateNewRecord(e) != nil {
							t.Fatal("command binding", r, e)
						}
						if action == "edit" && !r.Consent {
							t.Fatal("edit lost existing consent")
						}
						if action == "create" && r.Consent {
							t.Fatal("new opinion shared by default")
						}
						if failure {
							return ratings.Record{}, catalog.E(catalog.CodeDependencyUnavailable, "private-fixture")
						}
						return r, nil
					},
				}
				s := ratingProjectionServer(t, store)
				body, _ := json.Marshal(map[string]any{"target": old.Target, "score": 2})
				handler := s.handleRatingSubmit
				path := "/api/v1/ratings"
				values := map[string]string{}
				etag := `"rev-3"`
				if action == "create" {
					etag = ""
				}
				if action != "edit" && action != "create" {
					handler = s.handleRatingCommand
					path += "/" + old.ID + ":" + action
					values["ratingCommand"] = old.ID + ":" + action
					body = []byte(`{"reason":"Confirmed moderation decision"}`)
				}
				w := invokeRatingUnit(ctx, handler, "POST", path, string(body), etag, values)
				if failure {
					requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
				} else {
					want := 200
					if action == "create" {
						want = 201
					}
					var receipt feedbackReceipt
					if json.Unmarshal(w.Body.Bytes(), &receipt) != nil || w.Code != want || receipt.ID == "" || receipt.Revision < 1 || w.Header().Get("ETag") == "" || w.Header().Get("Cache-Control") != "no-store" {
						t.Fatal("receipt", w.Code, w.Body.String())
					}
					for _, private := range []string{"principalKey", "submittedBy", "score", "consent"} {
						if strings.Contains(w.Body.String(), private) {
							t.Fatal("receipt disclosed opinion")
						}
					}
				}
				if writes != 1 {
					t.Fatal("unexpected persistence calls", writes)
				}
			})
		}
	}
}
func TestRatingReadAndBoundedPageProjection(t *testing.T) {
	ctx := ratingProjectionContext(t.Context())
	record := ratingProjectionRecord(t, ctx)
	store := ratingProjectionStore{
		get: func(context.Context, string, string) (ratings.Record, error) { return record, nil },
		own: func(context.Context, string, ratings.Target, string) (ratings.Record, error) { return record, nil },
		aggregates: func(_ context.Context, tenant string, targets []ratings.Target) (map[string]ratings.Aggregate, error) {
			if tenant != record.TenantID || len(targets) != 1 || targets[0] != record.Target {
				t.Fatal("aggregate binding")
			}
			return map[string]ratings.Aggregate{ratings.Key(record.Target): {Count: 2, Sum: 7}}, nil
		},
		list: func(_ context.Context, tenant string, q ratings.ListInput) ([]ratings.Record, error) {
			if tenant != record.TenantID || q.Limit != 2 || q.State != "active" {
				t.Fatal("bounded page", q)
			}
			if q.AfterID != "" {
				return []ratings.Record{}, nil
			}
			other := record
			other.ID = projectionNext
			return []ratings.Record{record, other}, nil
		},
	}
	s := ratingProjectionServer(t, store)
	raw, err := json.Marshal(ratingRequest{Target: record.Target})
	if err != nil {
		t.Fatal(err)
	}
	w := invokeRatingUnit(ctx, s.handleRatingRead, "POST", "/api/v1/ratings/read", string(raw), "", nil)
	var view ratings.View
	if json.Unmarshal(w.Body.Bytes(), &view) != nil || w.Code != 200 || view.Average == nil || *view.Average != 3.5 || view.Own == nil {
		t.Fatal("rating view", w.Body.String())
	}
	w = invokeRatingUnit(ctx, s.handleRatingGet, "GET", "/api/v1/ratings/"+record.ID, "", "", map[string]string{"ratingId": record.ID})
	if w.Code != 200 || strings.Contains(w.Body.String(), record.PrincipalKey) {
		t.Fatal("private owner projection", w.Body.String())
	}
	for _, path := range []string{"/api/v1/ratings?state=active&limit=1", "/api/v1/ratings/inbox?state=active&limit=1"} {
		w = invokeRatingUnit(ctx, s.handleRatingList, "GET", path, "", "", nil)
		var page struct {
			Items      []ratings.Record `json:"items"`
			NextCursor string           `json:"nextCursor"`
		}
		if json.Unmarshal(w.Body.Bytes(), &page) != nil || w.Code != 200 || len(page.Items) != 1 || page.NextCursor == "" {
			t.Fatal("page", w.Body.String())
		}
		w = invokeRatingUnit(ctx, s.handleRatingList, "GET", path+"&cursor="+page.NextCursor, "", "", nil)
		if w.Code != 200 {
			t.Fatal("continuation", w.Body.String())
		}
	}
}
func TestRatingSharingProjectionAndExplicitChoice(t *testing.T) {
	ctx := ratingProjectionContext(t.Context())
	writes := 0
	s := ratingProjectionServer(t, ratingProjectionStore{
		sharing: func(_ context.Context, tenant, host, audience string) (ratings.Sharing, error) {
			if tenant != projectionTenant || host != "catalog.example.com" || audience != "anonymous" {
				t.Fatal("sharing binding")
			}
			return ratings.Sharing{}, catalog.E(catalog.CodeNotFound, "absent")
		},
		saveSharing: func(_ context.Context, r ratings.Sharing, expected int64, e audit.Record) (ratings.Sharing, error) {
			writes++
			if expected != 0 || !r.Enabled || r.Revision != 1 || e.Action != "rating.sharing" || audit.ValidateNewRecord(e) != nil {
				t.Fatal("sharing mutation binding")
			}
			return r, nil
		},
	})
	w := invokeRatingUnit(ctx, s.handleRatingSharing, "GET", "/api/v1/ratings/sharing?host=catalog.example.com&audience=anonymous", "", "", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, body := range []string{`{"host":"catalog.example.com","audience":"anonymous","reason":"Missing choice"}`, `{"host":"other.example","audience":"anonymous","enabled":true,"reason":"Wrong host"}`, `{"host":"catalog.example.com","audience":"authenticated","enabled":true,"reason":"Wrong audience"}`} {
		w = invokeRatingUnit(ctx, s.handleRatingSharing, "POST", "/api/v1/ratings/sharing", body, "", nil)
		if w.Code != 400 {
			t.Fatal("invalid choice accepted", w.Body.String())
		}
	}
	if writes != 0 {
		t.Fatal("invalid choice persisted")
	}
	w = invokeRatingUnit(ctx, s.handleRatingSharing, "POST", "/api/v1/ratings/sharing", `{"host":"catalog.example.com","audience":"anonymous","enabled":true,"reason":"Explicit opted-in audience"}`, "", nil)
	if w.Code != 200 || writes != 1 {
		t.Fatal("sharing receipt", w.Body.String())
	}
}
func TestRatingHandlersRejectMalformedControlsBeforePersistence(t *testing.T) {
	ctx := ratingProjectionContext(t.Context())
	s := ratingProjectionServer(t, ratingProjectionStore{})
	for name, handler := range map[string]http.HandlerFunc{"read": s.handleRatingRead, "submit": s.handleRatingSubmit, "sharing": s.handleRatingSharing, "command": s.handleRatingCommand} {
		t.Run(name, func(t *testing.T) {
			w := invokeRatingUnit(ctx, handler, "POST", "/api/v1/ratings", `{"broken":`, `"rev-3"`, map[string]string{"ratingCommand": projectionItem + ":withdraw"})
			requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
		})
	}
	for _, command := range []string{projectionItem + ":unknown", projectionItem} {
		w := invokeRatingUnit(ctx, s.handleRatingCommand, "POST", "/api/v1/ratings", `{}`, `"rev-3"`, map[string]string{"ratingCommand": command})
		requireProjectionProblem(t, w, 404, catalog.CodeNotFound)
	}
	for _, action := range []string{"withdraw", "remove", "permit"} {
		w := invokeRatingUnit(ctx, s.handleRatingCommand, "POST", "/api/v1/ratings", `{}`, `"rev-3"`, map[string]string{"ratingCommand": "bad:" + action})
		requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
		if !strings.Contains(w.Body.String(), "ratingId") {
			t.Fatal("invalid path problem does not identify ratingId", w.Body.String())
		}
	}
	w := invokeRatingUnit(ctx, s.handleRatingSubmit, "POST", "/api/v1/ratings", `{}`, `bad-etag`, nil)
	requireProjectionProblem(t, w, 412, catalog.CodePreconditionFailed)
	w = invokeRatingUnit(ctx, s.handleRatingCommand, "POST", "/api/v1/ratings", `{}`, "", map[string]string{"ratingCommand": projectionItem + ":withdraw"})
	requireProjectionProblem(t, w, 412, catalog.CodePreconditionFailed)
	w = invokeRatingUnit(ctx, s.handleRatingList, "GET", "/api/v1/ratings?limit=101", "", "", nil)
	requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
}

func ratingProjectionContext(ctx context.Context) context.Context {
	a := projectionActor(identity.RoleTenantAdmin)
	a.Issuer = "urn:catalog:unit-fixture"
	return identityfixture.WithActor(ctx, a)
}

func TestRatingSearchProjectionDecoratesOnlySelectedValidTargets(t *testing.T) {
	ctx := ratingProjectionContext(t.Context())
	source := federationFixtureSource()
	source.Live.Enabled = true
	source.PresentationGeneration = 1
	manager, err := federation.NewManager(&artworkReceiptStore{source: source}, publicPolicyFetcher{}, clock.NewFake(projectionTime), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	local := catalog.CatalogSearchResult{Identifier: federationFixtureAIR, Kind: catalog.KindAgent, VersionPresent: true, Version: "1.0", Local: &catalog.CatalogSearchLocal{Resource: catalog.CatalogResourceSummary{ID: projectionItem}, VersionRevision: catalog.CatalogRevisionSummary{ID: projectionNext}, Release: catalog.CatalogReleaseSummary{ID: projectionItem}}}
	indexed := catalog.CatalogSearchResult{Identifier: federationFixtureAIR, Kind: catalog.KindAgent, Indexed: &catalog.IndexedSearchEvidence{PresentationSourceID: source.ID, PresentationGeneration: 1, PresentationSourceRevision: source.Revision, NormalizedEntryDigest: "sha256:" + strings.Repeat("a", 64)}}
	live := catalog.CatalogSearchResult{Identifier: federationFixtureAIR, Kind: catalog.KindAgent, Live: &catalog.LiveSearchEvidence{SourceID: source.ID, SourceRevision: source.Revision, PresentationGeneration: 1, PolicyDigest: source.PolicyDigest, Entry: json.RawMessage(`{"identifier":"urn:air:peer.example:default:fixture"}`)}}
	calls := 0
	server := ratingProjectionServer(t, ratingProjectionStore{aggregates: func(_ context.Context, tenant string, targets []ratings.Target) (map[string]ratings.Aggregate, error) {
		calls++
		if tenant != projectionTenant || len(targets) != 3 {
			t.Fatal("selected-page boundary", tenant, targets)
		}
		return map[string]ratings.Aggregate{ratings.Key(ratingItemAdmission(local).Target): {Count: 2, Sum: 7}}, nil
	}})
	server.federation = manager
	items := []catalog.CatalogSearchResult{local, indexed, live, {Identifier: "invalid", Kind: catalog.KindAgent}}
	got, err := server.resolveRatings(ctx, items)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(got) != 4 || got[0].Ratings == nil || got[0].Ratings.Count != 2 || got[0].Ratings.Average == nil || *got[0].Ratings.Average != 3.5 || got[1].Ratings == nil || got[1].Ratings.Count != 0 || got[1].Ratings.Average != nil || got[2].Ratings == nil || got[2].Ratings.Receipt == "" || got[3].Ratings != nil {
		t.Fatal(got)
	}
	if _, err = server.verifyRatingReceipt(ctx, got[2].Ratings.Receipt); err != nil {
		t.Fatal(err)
	}
	if _, err = server.resolveRatings(context.Background(), items); catalog.ErrorCodeOf(err) != catalog.CodeUnauthenticated {
		t.Fatal(err)
	}
	server.ratings = nil
	if got, err = server.resolveRatings(ctx, items); err != nil || len(got) != len(items) {
		t.Fatal(got, err)
	}
}

type ratingLocalRepository struct {
	catalog.Repository
	record catalog.CatalogRecord
	err    error
}

func (r ratingLocalRepository) GetCatalogRecord(_ context.Context, tenant, release string) (catalog.CatalogRecord, error) {
	if tenant != r.record.Resource.TenantID || release != r.record.Release.ID {
		return catalog.CatalogRecord{}, catalog.E(catalog.CodeNotFound, "release not found")
	}
	return r.record, r.err
}
func TestLocalRatingAuthorityRechecksExactReleasedVersion(t *testing.T) {
	ctx := ratingProjectionContext(t.Context())
	record := catalog.CatalogRecord{Resource: catalog.Resource{ID: projectionItem, TenantID: projectionTenant, NamespaceID: projectionResource, CanonicalIdentifier: federationFixtureAIR, Kind: catalog.KindAgent}, Version: catalog.ResourceVersion{Version: "1.0"}, VersionRevision: catalog.VersionRevision{ID: projectionNext, ContentSetDigest: "sha256:" + strings.Repeat("a", 64)}, Release: catalog.ListingRelease{ID: projectionItem}}
	input := ratings.Admission{Target: ratings.Target{Origin: "local", ResourceID: record.Resource.ID, VersionRevisionID: record.VersionRevision.ID, Identifier: record.Resource.CanonicalIdentifier, VersionPresent: true, Version: "1.0", Kind: record.Resource.Kind}, ReleaseID: record.Release.ID}
	for _, failure := range []string{"none", "release", "resource", "revision", "identifier", "kind", "version"} {
		t.Run(failure, func(t *testing.T) {
			repo := ratingLocalRepository{record: record}
			if failure == "release" {
				repo.err = catalog.E(catalog.CodeNotFound, "withdrawn release")
			}
			service := projectionCatalog(t, repo, nil)
			a := input
			switch failure {
			case "resource":
				a.Target.ResourceID = projectionNext
			case "revision":
				a.Target.VersionRevisionID = projectionItem
			case "identifier":
				a.Target.Identifier = "urn:air:other.example:agents:helper"
			case "kind":
				a.Target.Kind = catalog.KindSkill
			case "version":
				a.Target.Version = "2.0"
			}
			got, err := (ratingAuthority{local: service}).Admit(ctx, a)
			if (err == nil) != (failure == "none") {
				t.Fatal(got, err)
			}
			if err == nil && (got.NamespaceID != record.Resource.NamespaceID || got.EntryDigest != record.VersionRevision.ContentSetDigest) {
				t.Fatal(got)
			}
		})
	}
}

func TestRatingReadFailuresRemainProblemsInsteadOfEmptySuccess(t *testing.T) {
	ctx := ratingProjectionContext(t.Context())
	record := ratingProjectionRecord(t, ctx)
	server := ratingProjectionServer(t, ratingProjectionStore{
		get: func(context.Context, string, string) (ratings.Record, error) {
			return ratings.Record{}, catalog.E(catalog.CodeDependencyUnavailable, "private-fixture")
		},
		aggregates: func(context.Context, string, []ratings.Target) (map[string]ratings.Aggregate, error) {
			return nil, catalog.E(catalog.CodeDependencyUnavailable, "private-fixture")
		},
	})
	got := invokeRatingUnit(ctx, server.handleRatingGet, "GET", "/api/v1/ratings/"+record.ID, "", "", map[string]string{"ratingId": record.ID})
	requireProjectionProblem(t, got, 503, catalog.CodeDependencyUnavailable)
	body, err := json.Marshal(ratingRequest{Target: record.Target})
	if err != nil {
		t.Fatal(err)
	}
	got = invokeRatingUnit(ctx, server.handleRatingRead, "POST", "/api/v1/ratings/read", string(body), "", nil)
	requireProjectionProblem(t, got, 503, catalog.CodeDependencyUnavailable)
}
