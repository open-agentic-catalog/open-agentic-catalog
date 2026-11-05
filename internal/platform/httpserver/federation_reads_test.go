package httpserver

import (
	"context"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestFederationDetailProjection(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"source", "inspection", "snapshot", "raw"} {
		for _, outcome := range []string{"retained", "missing", "unavailable", "audit-failed"} {
			if outcome == "audit-failed" && kind != "inspection" && kind != "raw" {
				continue
			}
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				actor := projectionActor(identity.RoleSourceAdmin)
				ctx := identityfixture.WithActor(t.Context(), actor)
				reads, audits := 0, 0
				source := federationFixtureSource()
				source.CredentialBinding = "fixture-credential-reference"
				inspection := federationFixtureInspection()
				inspection.CredentialBinding = "fixture-credential-reference"
				snapshot := federationFixtureSnapshot(t)
				observation := federationFixtureObservation(t, true)
				key := projectionItem
				if kind == "source" {
					key = source.ID
				}
				check := func(c context.Context, tenant, id string) error {
					reads++
					if !sameProjectionContext(c, ctx) || tenant != actor.TenantID || id != key {
						t.Fatal("detail binding")
					}
					switch outcome {
					case "missing":
						return catalog.E(catalog.CodeNotFound, "missing federation record")
					case "unavailable":
						return catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-store")
					}
					return nil
				}
				store := federationProjectionStore{
					getSource: func(c context.Context, tenant, key string) (federation.Source, error) {
						return source, check(c, tenant, key)
					},
					getInspection: func(c context.Context, tenant, key string) (federation.Inspection, error) {
						return inspection, check(c, tenant, key)
					},
					getSnapshot: func(c context.Context, tenant, key string) (federation.Snapshot, error) {
						return snapshot, check(c, tenant, key)
					},
					getObservation: func(c context.Context, tenant, key string) (federation.Observation, error) {
						return observation, check(c, tenant, key)
					},
				}
				evidence := federationProjectionEvidence{appendAudit: func(c context.Context, r audit.Record) error {
					audits++
					if !sameProjectionContext(c, ctx) || r.TenantID != actor.TenantID || r.TargetID != key || r.Actor != actor.MembershipID {
						t.Fatal("sensitive-read attribution")
					}
					if err := audit.ValidateNewRecord(r); err != nil {
						t.Fatal(err)
					}
					want := audit.ActionSourceInspectionRead
					if kind == "raw" {
						want = audit.ActionRawObservationRead
					}
					if r.Action != want {
						t.Fatalf("audit action=%s", r.Action)
					}
					if outcome == "audit-failed" {
						return errors.New("private-fixture-audit")
					}
					return nil
				}}
				s := federationProjectionServer(t, store, evidence)
				handler := s.handleSourceGet
				pathKey := "sourceId"
				var expected any = source
				switch kind {
				case "inspection":
					handler, pathKey, expected = s.handleSourceInspectionGet, "inspectionId", inspection
				case "snapshot":
					handler, pathKey, expected = s.handleSourceSnapshotGet, "snapshotId", snapshot
				case "raw":
					handler, pathKey = s.handleObservationRaw, "observationId"
				}
				r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/federation", nil)
				r.SetPathValue(pathKey, key)
				w := httptest.NewRecorder()
				handler(w, r)
				switch outcome {
				case "missing":
					requireProjectionProblem(t, w, 404, catalog.CodeNotFound)
				case "unavailable", "audit-failed":
					requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
				default:
					if kind == "raw" {
						if w.Code != 200 || w.Body.String() != string(observation.RawEntry) || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Content-Disposition") != `attachment; filename="`+key+`.json"` || w.Header().Get("Content-Length") != strconv.Itoa(len(observation.RawEntry)) || w.Header().Get("Content-Digest") != digestHeader(observation.RawEntry) {
							t.Fatalf("raw response=%d %v %s", w.Code, w.Header(), w.Body)
						}
					} else {
						requireFederationJSON(t, w, 200, expected)
					}
					if kind == "source" && w.Header().Get("ETag") != `"rev-3"` {
						t.Fatalf("source ETag=%s", w.Header().Get("ETag"))
					}
				}
				wantAudits := 0
				if (kind == "raw" || kind == "inspection") && (outcome == "retained" || outcome == "audit-failed") {
					wantAudits = 1
				}
				if reads != 1 || audits != wantAudits {
					t.Fatalf("reads=%d audits=%d", reads, audits)
				}
				if outcome != "retained" && (w.Header().Get("Content-Disposition") != "" || w.Header().Get("Content-Digest") != "" || w.Header().Get("ETag") != "") {
					t.Fatal("failure disclosed success headers")
				}
			})
		}
	}
}

type federationGroupStore struct {
	federation.Store
	members   func(context.Context, string, string, string, bool, int) ([]federation.Observation, error)
	conflicts func(context.Context, string, string, string, bool, []string) (bool, map[string]bool, error)
	summary   func(context.Context, string, []string) (map[string]federation.OccurrenceSummary, error)
}

func (s federationGroupStore) ListObservationGroupMembers(c context.Context, t, i, v string, p bool, n int) ([]federation.Observation, error) {
	return s.members(c, t, i, v, p, n)
}
func (s federationGroupStore) ObservationGroupConflicts(c context.Context, t, i, v string, p bool, sources []string) (bool, map[string]bool, error) {
	return s.conflicts(c, t, i, v, p, sources)
}
func (s federationGroupStore) ObservationOccurrenceSummaries(c context.Context, t string, k []string) (map[string]federation.OccurrenceSummary, error) {
	return s.summary(c, t, k)
}

func TestFederationGroupProjection(t *testing.T) {
	t.Parallel()
	for _, present := range []bool{true, false} {
		t.Run(strconv.FormatBool(present), func(t *testing.T) {
			actor := projectionActor(identity.RoleSourceAdmin)
			ctx := identityfixture.WithActor(t.Context(), actor)
			o := federationFixtureObservation(t, present)
			occurrence := federationFixtureOccurrence(t)
			occurrence.ExactEntryDigest = o.ExactEntryDigest
			occurrence.NormalizedEntryDigest = o.NormalizedEntryDigest
			calls := []string{}
			check := func(c context.Context, tenant string) {
				if !sameProjectionContext(c, ctx) || tenant != actor.TenantID {
					t.Fatal("group tenant binding")
				}
			}
			s := federationProjectionServer(t, federationGroupStore{
				members: func(c context.Context, tenant, identifier, version string, p bool, limit int) ([]federation.Observation, error) {
					check(c, tenant)
					calls = append(calls, "members")
					if identifier != o.Identifier || version != o.Version || p != present || limit != 101 {
						t.Fatal("typed group key or limit")
					}
					return []federation.Observation{o}, nil
				},
				conflicts: func(c context.Context, tenant, identifier, version string, p bool, sources []string) (bool, map[string]bool, error) {
					check(c, tenant)
					calls = append(calls, "conflicts")
					if identifier != o.Identifier || version != o.Version || p != present || !reflect.DeepEqual(sources, []string{o.SourceID}) {
						t.Fatal("conflict lookup binding")
					}
					return false, map[string]bool{o.SourceID: false}, nil
				},
				summary: func(c context.Context, tenant string, keys []string) (map[string]federation.OccurrenceSummary, error) {
					check(c, tenant)
					calls = append(calls, "summary")
					if !reflect.DeepEqual(keys, []string{o.ID}) {
						t.Fatal("occurrence lookup")
					}
					return map[string]federation.OccurrenceSummary{o.ID: {Latest: occurrence, Total: 2}}, nil
				},
			}, nil)
			query := url.Values{"identifier": {o.Identifier}, "versionPresent": {strconv.FormatBool(present)}}
			if present {
				query.Set("version", o.Version)
			}
			r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/group?"+query.Encode(), nil)
			w := httptest.NewRecorder()
			s.handleObservationGroup(w, r)
			groupID, err := federation.ObservationGroupID(o.Identifier, o.Version, present)
			if err != nil {
				t.Fatal(err)
			}
			want := federation.ObservationGroup{ID: groupID, Identifier: o.Identifier, Version: o.Version, VersionPresent: present, Kind: o.Kind, Conflict: false, Members: []federation.ObservationGroupMember{{NormalizedEntryDigest: o.NormalizedEntryDigest, Contributions: []federation.ObservationGroupContribution{{Observation: o, OccurrenceCount: 2, LatestOccurrence: occurrence, SourceConflict: false}}}}}
			requireFederationJSON(t, w, 200, want)
			if !reflect.DeepEqual(calls, []string{"members", "conflicts", "summary"}) {
				t.Fatalf("group calls=%v", calls)
			}
		})
	}
}
