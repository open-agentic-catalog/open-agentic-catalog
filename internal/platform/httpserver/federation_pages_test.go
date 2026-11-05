package httpserver

import (
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestFederationPageProjection(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"sources", "snapshots", "observations-current", "observations-all", "occurrences", "search"} {
		for _, outcome := range []string{"pages", "empty", "unavailable"} {
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				actor := projectionActor(identity.RoleSourceAdmin)
				ctx := identityfixture.WithActor(t.Context(), actor)
				reads, parents := 0, 0
				checkBinding := func(c context.Context, tenant string) {
					if !sameProjectionContext(c, ctx) || tenant != actor.TenantID {
						t.Fatal("federation page lost context or tenant")
					}
				}
				check := func(c context.Context, tenant string, got, want any) error {
					checkBinding(c, tenant)
					reads++
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("repository page=%+v want=%+v", got, want)
					}
					if outcome == "unavailable" {
						return catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-store")
					}
					return nil
				}
				source := federationFixtureSource()
				snapshot := federationFixtureSnapshot(t)
				observation := federationFixtureObservation(t, true)
				occurrence := federationFixtureOccurrence(t)
				repo := federationProjectionStore{
					getSource: func(c context.Context, tenant, key string) (federation.Source, error) {
						checkBinding(c, tenant)
						parents++
						if key != source.ID {
							t.Fatal("source parent")
						}
						return source, nil
					},
					getObservationMetadata: func(c context.Context, tenant, key string) (federation.Observation, error) {
						checkBinding(c, tenant)
						parents++
						if key != observation.ID {
							t.Fatal("observation parent")
						}
						return observation, nil
					},
				}
				var first, second any
				query := url.Values{"limit": {"1"}}
				switch kind {
				case "sources":
					next := source
					next.ID = projectionNext
					next.DisplayName = "Next source"
					first, second = source, next
					repo.sources = func(c context.Context, tenant string, p federation.SourceListRequest) ([]federation.Source, error) {
						want := federation.SourceListRequest{Limit: 2}
						if reads > 0 {
							want.AfterDisplayName = source.DisplayName
							want.AfterID = source.ID
						}
						if err := check(c, tenant, p, want); err != nil {
							return nil, err
						}
						if outcome == "empty" {
							return []federation.Source{}, nil
						}
						if reads == 1 {
							return []federation.Source{source, next}, nil
						}
						return []federation.Source{next}, nil
					}
				case "snapshots":
					next := snapshot
					next.ID = projectionNext
					next.StartedAt = projectionTime.Add(-time.Minute)
					first, second = snapshot, next
					repo.snapshots = func(c context.Context, tenant string, p federation.SnapshotListRequest) ([]federation.Snapshot, error) {
						want := federation.SnapshotListRequest{SourceID: source.ID, Limit: 2}
						if reads > 0 {
							want.AfterStartedAt = &snapshot.StartedAt
							want.AfterID = snapshot.ID
						}
						if err := check(c, tenant, p, want); err != nil {
							return nil, err
						}
						if outcome == "empty" {
							return []federation.Snapshot{}, nil
						}
						if reads == 1 {
							return []federation.Snapshot{snapshot, next}, nil
						}
						return []federation.Snapshot{next}, nil
					}
				case "observations-current", "observations-all":
					query.Set("sourceId", source.ID)
					if kind == "observations-all" {
						query.Set("freshness", "all")
					}
					next := federationFixtureObservation(t, false)
					next.ID = projectionNext
					first, second = observation, next
					repo.observations = func(c context.Context, tenant string, p federation.ObservationListRequest) ([]federation.Observation, error) {
						want := federation.ObservationListRequest{SourceID: source.ID, CurrentOnly: kind == "observations-current", Limit: 2}
						if reads > 0 {
							want.AfterIdentifier = observation.Identifier
							want.AfterVersion = observation.Version
							want.AfterVersionPresent = true
							want.AfterID = observation.ID
						}
						if err := check(c, tenant, p, want); err != nil {
							return nil, err
						}
						if outcome == "empty" {
							return []federation.Observation{}, nil
						}
						if reads == 1 {
							return []federation.Observation{observation, next}, nil
						}
						return []federation.Observation{next}, nil
					}
				case "occurrences":
					next := occurrence
					next.SnapshotID = projectionResource
					next.ObservedAt = projectionTime.Add(-time.Minute)
					first, second = occurrence, next
					repo.occurrences = func(c context.Context, tenant string, p federation.OccurrenceListRequest) ([]federation.ObservationOccurrence, error) {
						want := federation.OccurrenceListRequest{ObservationID: observation.ID, Limit: 2}
						if reads > 0 {
							want.AfterObservedAt = &occurrence.ObservedAt
							want.AfterSnapshotID = occurrence.SnapshotID
							want.AfterRetrievalDigest = occurrence.RetrievalDigest
							want.AfterEntryLocation = occurrence.EntryLocation
						}
						if err := check(c, tenant, p, want); err != nil {
							return nil, err
						}
						if outcome == "empty" {
							return []federation.ObservationOccurrence{}, nil
						}
						if reads == 1 {
							return []federation.ObservationOccurrence{occurrence, next}, nil
						}
						return []federation.ObservationOccurrence{next}, nil
					}
				case "search":
					result := federation.SearchResult{Observation: observation, Score: 90}
					next := result
					next.Observation.ID = projectionNext
					next.Score = 80
					first, second = result, next
					repo.search = func(c context.Context, tenant string, p federation.ObservationSearchRequest) ([]federation.SearchResult, error) {
						want := federation.ObservationSearchRequest{Query: federation.SearchQuery{Text: "fixture agent", Kinds: []catalog.Kind{catalog.KindAgent}, Tags: []string{"fixture"}}, Limit: 2}
						if reads > 0 {
							want.AfterScore = result.Score
							want.AfterIdentifier = observation.Identifier
							want.AfterID = observation.ID
						}
						if err := check(c, tenant, p, want); err != nil {
							return nil, err
						}
						if outcome == "empty" {
							return []federation.SearchResult{}, nil
						}
						if reads == 1 {
							return []federation.SearchResult{result, next}, nil
						}
						return []federation.SearchResult{next}, nil
					}
				}
				s := federationProjectionServer(t, repo, nil)
				handler := s.handleSourcesList
				switch kind {
				case "snapshots":
					handler = s.handleSourceSnapshots
				case "observations-current", "observations-all":
					handler = s.handleObservations
				case "occurrences":
					handler = s.handleObservationOccurrences
				case "search":
					handler = s.handleObservationSearch
				}
				token := ""
				invoke := func() *httptest.ResponseRecorder {
					r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/federation?"+query.Encode(), nil)
					r.SetPathValue("sourceId", source.ID)
					r.SetPathValue("observationId", observation.ID)
					if kind == "search" {
						body := map[string]any{"text": "fixture agent", "kinds": []string{"agent"}, "tags": []string{"fixture"}, "limit": 1}
						if token != "" {
							body["cursor"] = token
						}
						raw, err := json.Marshal(body)
						if err != nil {
							t.Fatal(err)
						}
						r = httptest.NewRequestWithContext(ctx, http.MethodPost, "/search", strings.NewReader(string(raw)))
						r.Header.Set("Content-Type", "application/json")
					}
					w := httptest.NewRecorder()
					handler(w, r)
					return w
				}
				w := invoke()
				if outcome == "unavailable" {
					requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
					return
				}
				var page cursorPage[json.RawMessage]
				if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
					t.Fatal(err)
				}
				if outcome == "empty" {
					requireFederationJSON(t, w, 200, cursorPage[any]{Items: []any{}})
					return
				}
				if page.NextCursor == "" {
					t.Fatalf("missing continuation: %s", w.Body)
				}
				requireFederationJSON(t, w, 200, cursorPage[any]{Items: []any{first}, NextCursor: page.NextCursor})
				token = page.NextCursor
				query.Set("cursor", token)
				w = invoke()
				requireFederationJSON(t, w, 200, cursorPage[any]{Items: []any{second}})
				wantParents := 2
				if kind == "sources" || kind == "search" {
					wantParents = 0
				}
				if reads != 2 || parents != wantParents {
					t.Fatalf("reads=%d parents=%d", reads, parents)
				}
			})
		}
	}
}
