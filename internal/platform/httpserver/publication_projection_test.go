// Publication handlers use static retained snapshots and existing storage ports.
// Each command stands alone; no authoring or publication journey prepares it.
package httpserver

import (
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

type publicationProjectionStore struct {
	publication.Store
	get        func(context.Context, string, string) (publication.Snapshot, error)
	active     func(context.Context, string) (publication.Snapshot, error)
	list       func(context.Context, string, publication.SnapshotListRequest) ([]publication.SnapshotSummary, error)
	generation func(context.Context, string, string) (int64, error)
	save       func(context.Context, publication.Snapshot, ...publication.MutationEvidence) (publication.Snapshot, error)
	transition func(string, context.Context, string, string, time.Time, ...publication.MutationEvidence) (publication.Snapshot, error)
}

func (s publicationProjectionStore) Get(c context.Context, tenant, key string) (publication.Snapshot, error) {
	return s.get(c, tenant, key)
}
func (s publicationProjectionStore) Active(c context.Context, tenant string) (publication.Snapshot, error) {
	return s.active(c, tenant)
}
func (s publicationProjectionStore) ListSummaries(c context.Context, tenant string, p publication.SnapshotListRequest) ([]publication.SnapshotSummary, error) {
	return s.list(c, tenant, p)
}
func (s publicationProjectionStore) NextGeneration(c context.Context, tenant, host string) (int64, error) {
	return s.generation(c, tenant, host)
}
func (s publicationProjectionStore) SaveCandidate(c context.Context, snap publication.Snapshot, e ...publication.MutationEvidence) (publication.Snapshot, error) {
	return s.save(c, snap, e...)
}
func (s publicationProjectionStore) Activate(c context.Context, tenant, key string, at time.Time, e ...publication.MutationEvidence) (publication.Snapshot, error) {
	return s.transition("activate", c, tenant, key, at, e...)
}
func (s publicationProjectionStore) Rollback(c context.Context, tenant, key string, at time.Time, e ...publication.MutationEvidence) (publication.Snapshot, error) {
	return s.transition("rollback", c, tenant, key, at, e...)
}
func (s publicationProjectionStore) Deactivate(c context.Context, tenant, key string, at time.Time, e ...publication.MutationEvidence) (publication.Snapshot, error) {
	return s.transition("unpublish", c, tenant, key, at, e...)
}

type publicationProjectionEvidence struct{ publication.EvidenceSink }

func publicationControlServer(t *testing.T, store publication.Store) *Server {
	t.Helper()
	manager, err := publication.NewManager(publication.Options{Records: fixedPublicationRecordSource{tenantID: projectionTenant}, Store: store, Clock: clock.NewFake(projectionTime), IDs: id.Generator{}, BaseURL: publicFixtureOrigin, PublisherDomain: publicFixtureHost, Host: publicFixtureHost, Audience: publication.AudienceAnonymous, ARDProfile: publicFixtureARDProfile, AICatalogProfile: publicFixtureAIProfile, CursorKey: []byte(strings.Repeat("p", 32)), Evidence: publicationProjectionEvidence{}})
	if err != nil {
		t.Fatal(err)
	}
	s := projectionServer(nil)
	s.discovery = map[string]DiscoveryHost{publicFixtureHost: {Host: publicFixtureHost, TenantID: projectionTenant, Mode: DiscoveryAnonymous, PublisherDomain: publicFixtureHost, Publication: manager}}
	return s
}

func requirePublicationResponse(t *testing.T, w *httptest.ResponseRecorder, status int, want publication.Snapshot) {
	t.Helper()
	var got publication.Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != status || w.Header().Get("Content-Type") != "application/json; charset=utf-8" || w.Header().Get("ETag") != want.ETag || !reflect.DeepEqual(got, want) {
		t.Fatalf("publication response=%d %v %s", w.Code, w.Header(), w.Body)
	}
}

func TestPublicationDetailProjection(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"retained", "missing", "unavailable"} {
		t.Run(outcome, func(t *testing.T) {
			actor := projectionActor(identity.RolePublisher)
			ctx := identityfixture.WithActor(t.Context(), actor)
			snap, _ := publicFixtureSnapshot(t, DiscoveryAnonymous)
			reads := 0
			s := publicationControlServer(t, publicationProjectionStore{get: func(c context.Context, tenant, key string) (publication.Snapshot, error) {
				reads++
				if !sameProjectionContext(c, ctx) || tenant != actor.TenantID || key != snap.ID {
					t.Fatal("snapshot lookup binding")
				}
				switch outcome {
				case "missing":
					return publication.Snapshot{}, catalog.E(catalog.CodeNotFound, "missing snapshot")
				case "unavailable":
					return publication.Snapshot{}, catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-store")
				}
				return snap, nil
			}})
			r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/snapshots/"+snap.ID+"?host="+publicFixtureHost, nil)
			r.SetPathValue("snapshotId", snap.ID)
			w := httptest.NewRecorder()
			s.handlePublicationGet(w, r)
			switch outcome {
			case "missing":
				requireProjectionProblem(t, w, 404, catalog.CodeNotFound)
			case "unavailable":
				requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
			default:
				requirePublicationResponse(t, w, 200, snap)
			}
			if reads != 1 {
				t.Fatalf("reads=%d", reads)
			}
		})
	}
}

func TestPublicationPageProjection(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"pages", "active", "empty", "unavailable"} {
		t.Run(outcome, func(t *testing.T) {
			actor := projectionActor(identity.RolePublisher)
			ctx := identityfixture.WithActor(t.Context(), actor)
			snap, _ := publicFixtureSnapshot(t, DiscoveryAnonymous)
			first := publication.SummarizeSnapshot(snap)
			second := first
			second.ID = projectionNext
			second.Generation--
			reads := 0
			s := publicationControlServer(t, publicationProjectionStore{list: func(c context.Context, tenant string, p publication.SnapshotListRequest) ([]publication.SnapshotSummary, error) {
				reads++
				if !sameProjectionContext(c, ctx) || tenant != actor.TenantID || p.Limit != 2 {
					t.Fatalf("snapshot list binding: %s %+v", tenant, p)
				}
				if (outcome == "active" && p.State != publication.SnapshotActive) || (outcome != "active" && p.State != "") {
					t.Fatalf("snapshot state binding: %+v", p)
				}
				if outcome == "unavailable" {
					return nil, catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-store")
				}
				if outcome == "empty" {
					return []publication.SnapshotSummary{}, nil
				}
				switch reads {
				case 1:
					if p.AfterGeneration != 0 || p.AfterID != "" {
						t.Fatal("initial position")
					}
					return []publication.SnapshotSummary{first, second}, nil
				case 2:
					if p.AfterGeneration != first.Generation || p.AfterID != first.ID {
						t.Fatalf("continuation=%+v", p)
					}
					return []publication.SnapshotSummary{second}, nil
				default:
					t.Fatal("unexpected list read")
					return nil, nil
				}
			}})
			query := url.Values{"host": {publicFixtureHost}, "limit": {"1"}}
			if outcome == "active" {
				query.Set("state", "active")
			}
			var page cursorPage[publication.SnapshotSummary]
			invoke := func() *httptest.ResponseRecorder {
				r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/snapshots?"+query.Encode(), nil)
				w := httptest.NewRecorder()
				s.handlePublicationList(w, r)
				return w
			}
			decode := func(w *httptest.ResponseRecorder) {
				t.Helper()
				page = cursorPage[publication.SnapshotSummary]{}
				if w.Code != 200 {
					t.Fatalf("snapshot page=%d %s", w.Code, w.Body)
				}
				if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(w.Body.String(), "private-fixture") || strings.Contains(w.Body.String(), `"ard"`) || strings.Contains(w.Body.String(), `"aiCatalog"`) {
					t.Fatal("snapshot summary disclosed documents")
				}
			}
			w := invoke()
			if outcome == "unavailable" {
				requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
				return
			}
			decode(w)
			if outcome == "empty" {
				if page.Items == nil || len(page.Items) != 0 || page.NextCursor != "" {
					t.Fatal("empty page")
				}
				return
			}
			if !reflect.DeepEqual(page.Items, []publication.SnapshotSummary{first}) || page.NextCursor == "" {
				t.Fatalf("first page=%s", w.Body)
			}
			query.Set("cursor", page.NextCursor)
			originalState := query.Get("state")
			query.Set("state", "retired")
			if invalid := invoke(); invalid.Code != http.StatusBadRequest || reads != 1 {
				t.Fatalf("cursor reused with another state: status=%d reads=%d", invalid.Code, reads)
			}
			if originalState == "" {
				query.Del("state")
			} else {
				query.Set("state", originalState)
			}
			w = invoke()
			decode(w)
			if !reflect.DeepEqual(page.Items, []publication.SnapshotSummary{second}) || page.NextCursor != "" || reads != 2 {
				t.Fatalf("last page=%s reads=%d", w.Body, reads)
			}
		})
	}
}

func TestPublicationCandidateProjection(t *testing.T) {
	t.Parallel()
	for _, failure := range []bool{false, true} {
		t.Run(strconv.FormatBool(failure), func(t *testing.T) {
			actor := projectionActor(identity.RolePublisher)
			ctx := identityfixture.WithActor(t.Context(), actor)
			var retained publication.Snapshot
			generations, writes := 0, 0
			check := func(c context.Context, tenant string) {
				a, ok := identity.FromContext(c)
				if !ok || !reflect.DeepEqual(identity.BindPermissionStore(a, nil), identity.BindPermissionStore(actor, nil)) || tenant != actor.TenantID {
					t.Fatal("candidate actor or tenant binding")
				}
			}
			s := publicationControlServer(t, publicationProjectionStore{
				generation: func(c context.Context, tenant, host string) (int64, error) {
					check(c, tenant)
					generations++
					if host != publicFixtureHost {
						t.Fatal("generation host")
					}
					return 8, nil
				},
				save: func(c context.Context, snap publication.Snapshot, e ...publication.MutationEvidence) (publication.Snapshot, error) {
					check(c, snap.TenantID)
					writes++
					if err := publication.ValidateSnapshotIntegrity(snap); err != nil {
						t.Fatal(err)
					}
					if err := publication.ValidateMutationEvidence(snap.TenantID, snap.ID, e, audit.ActionPublicationCandidateCreated); err != nil {
						t.Fatal(err)
					}
					if len(e) != 1 || e[0].Audit.Actor != actor.MembershipID || e[0].Audit.ID == "" || snap.Generation != 8 || snap.State != publication.SnapshotCandidate || snap.ActivatedAt != nil || snap.Host != publicFixtureHost || len(snap.ARD.Entries) != 1 || len(snap.AICatalog.Entries) != 0 {
						t.Fatalf("candidate=%+v evidence=%+v", snap, e)
					}
					retained = snap
					if failure {
						return publication.Snapshot{}, catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-store")
					}
					return snap, nil
				},
			})
			r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/snapshots", strings.NewReader(`{"host":"`+publicFixtureHost+`"}`))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.handlePublicationBuild(w, r)
			if failure {
				requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
				if w.Header().Get("Location") != "" || w.Header().Get("ETag") != "" {
					t.Fatal("failed build advertised a candidate")
				}
			} else {
				requirePublicationResponse(t, w, 201, retained)
				if w.Header().Get("Location") != "/api/v1/publication/snapshots/"+retained.ID+"?host="+publicFixtureHost {
					t.Fatalf("location=%s", w.Header().Get("Location"))
				}
			}
			if generations != 1 || writes != 1 {
				t.Fatalf("generations=%d writes=%d", generations, writes)
			}
		})
	}
}

// Empty eligible input is sufficient for valid single-command transition tests.
// It still contains the required Registry entry and sealed profile documents.
func publicationTransitionFixture(t *testing.T, action string) publication.Snapshot {
	t.Helper()
	snap, _ := publicFixtureSnapshot(t, DiscoveryAnonymous)
	snap.ARD.Entries = snap.ARD.Entries[:1]
	snap.AICatalog.Entries = []publication.AICatalogEntry{}
	snap.ArtifactIDs = []string{}
	snap.Exclusions = []publication.Exclusion{}
	switch action {
	case "activate":
		snap.State = publication.SnapshotCandidate
		snap.ActivatedAt = nil
	case "rollback":
		snap.State = publication.SnapshotRetired
	}
	return sealPublicFixtureSnapshot(t, snap)
}

func TestPublicationTransitionProjection(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"activate", "rollback", "unpublish"} {
		for _, outcome := range []string{"success", "missing", "invalid-reason", "store-unavailable", "missing-etag", "weak-etag", "wrong-etag"} {
			if action == "unpublish" && strings.HasSuffix(outcome, "etag") {
				continue
			} // This route deliberately has no If-Match contract.
			t.Run(action+"/"+outcome, func(t *testing.T) {
				actor := projectionActor(identity.RolePublisher)
				ctx := identityfixture.WithActor(t.Context(), actor)
				original := publicationTransitionFixture(t, action)
				expected := original
				reads, activeReads, writes := 0, 0, 0
				check := func(c context.Context, tenant string) {
					a, ok := identity.FromContext(c)
					if !ok || !reflect.DeepEqual(identity.BindPermissionStore(a, nil), identity.BindPermissionStore(actor, nil)) || tenant != actor.TenantID {
						t.Fatal("transition lost actor/tenant binding")
					}
				}
				s := publicationControlServer(t, publicationProjectionStore{
					get: func(c context.Context, tenant, key string) (publication.Snapshot, error) {
						check(c, tenant)
						reads++
						if key != original.ID {
							t.Fatal("transition snapshot id")
						}
						if outcome == "missing" {
							return publication.Snapshot{}, catalog.E(catalog.CodeNotFound, "missing snapshot")
						}
						return original, nil
					},
					active: func(c context.Context, tenant string) (publication.Snapshot, error) {
						check(c, tenant)
						activeReads++
						if action != "unpublish" || outcome == "missing" {
							return publication.Snapshot{}, catalog.E(catalog.CodeNotFound, "no active snapshot")
						}
						return original, nil
					},
					transition: func(operation string, c context.Context, tenant, key string, at time.Time, e ...publication.MutationEvidence) (publication.Snapshot, error) {
						check(c, tenant)
						writes++
						if operation != action || key != original.ID || at.Before(projectionTime) {
							t.Fatal("transition operation, identity or time")
						}
						auditAction := map[string]string{"activate": audit.ActionPublicationActivated, "rollback": audit.ActionPublicationRolledBack, "unpublish": audit.ActionPublicationUnpublished}[action]
						if err := publication.ValidateMutationEvidence(tenant, key, e, auditAction); err != nil {
							t.Fatal(err)
						}
						prior := ""
						if action == "unpublish" {
							prior = original.ID
						}
						if len(e) != 1 || e[0].Audit.ID == "" || e[0].Audit.Actor != actor.MembershipID || e[0].Audit.Reason != "Operator review" || e[0].ExpectedActiveID == nil || *e[0].ExpectedActiveID != prior {
							t.Fatalf("transition evidence=%+v", e)
						}
						if failure := outcome == "store-unavailable"; failure {
							return publication.Snapshot{}, catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-store")
						}
						expected.State = publication.SnapshotActive
						expected.ActivatedAt = &at
						if action == "unpublish" {
							expected.State = publication.SnapshotRetired
							expected.ActivatedAt = original.ActivatedAt
						}
						return expected, nil
					},
				})
				reason := "Operator review"
				if outcome == "invalid-reason" {
					reason = strings.Repeat("r", 2001)
				}
				body, _ := json.Marshal(publicationTransitionRequest{Host: publicFixtureHost, Reason: reason})
				s.maxBodyBytes = int64(len(body) + 1)
				r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/transition", strings.NewReader(string(body)))
				r.SetPathValue("snapshotId", original.ID)
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("If-Match", original.ETag)
				switch outcome {
				case "missing-etag":
					r.Header.Del("If-Match")
				case "weak-etag":
					r.Header.Set("If-Match", "W/"+original.ETag)
				case "wrong-etag":
					r.Header.Set("If-Match", `"other"`)
				}
				handler := s.handlePublicationActivate
				switch action {
				case "rollback":
					handler = s.handlePublicationRollback
				case "unpublish":
					handler = s.handlePublicationUnpublish
				}
				w := httptest.NewRecorder()
				handler(w, r)
				wantReads, wantActive, wantWrites := 2, 1, 1
				if action == "unpublish" {
					wantReads = 0
				}
				switch outcome {
				case "missing":
					requireProjectionProblem(t, w, 404, catalog.CodeNotFound)
					wantReads, wantActive, wantWrites = 1, 0, 0
					if action == "unpublish" {
						wantReads, wantActive = 0, 1
					}
				case "invalid-reason":
					requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
					wantReads, wantActive, wantWrites = 1, 0, 0
					if action == "unpublish" {
						wantReads = 0
					}
				case "missing-etag", "weak-etag", "wrong-etag":
					requireProjectionProblem(t, w, 412, catalog.CodePreconditionFailed)
					wantReads, wantActive, wantWrites = 1, 0, 0
				case "store-unavailable":
					requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
				default:
					requirePublicationResponse(t, w, 200, expected)
				}
				if outcome != "success" && w.Header().Get("ETag") != "" {
					t.Fatal("failed transition advertised an ETag")
				}
				if reads != wantReads || activeReads != wantActive || writes != wantWrites {
					t.Fatalf("calls get/active/write=%d/%d/%d want=%d/%d/%d", reads, activeReads, writes, wantReads, wantActive, wantWrites)
				}
			})
		}
	}
}
