package httpserver

import (
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
)

type federationCommandStore struct {
	federationProjectionStore
	create         func(context.Context, federation.Source, ...federation.MutationEvidence) (federation.Source, error)
	update         func(context.Context, federation.Source, int64, ...federation.MutationEvidence) (federation.Source, error)
	getPreview     func(context.Context, string, string) (federation.ChangePreview, error)
	savePreview    func(context.Context, federation.ChangePreview, ...federation.MutationEvidence) (federation.ChangePreview, error)
	observationSet func(context.Context, string, string, int) (federation.ObservationSet, bool, error)
	current        func(context.Context, string, string, int) ([]federation.Observation, bool, error)
}

func (s federationCommandStore) CreateSource(c context.Context, v federation.Source, e ...federation.MutationEvidence) (federation.Source, error) {
	return s.create(c, v, e...)
}
func (s federationCommandStore) UpdateSource(c context.Context, v federation.Source, r int64, e ...federation.MutationEvidence) (federation.Source, error) {
	return s.update(c, v, r, e...)
}
func (s federationCommandStore) GetChangePreview(c context.Context, t, k string) (federation.ChangePreview, error) {
	return s.getPreview(c, t, k)
}
func (s federationCommandStore) SaveChangePreview(c context.Context, v federation.ChangePreview, e ...federation.MutationEvidence) (federation.ChangePreview, error) {
	return s.savePreview(c, v, e...)
}
func (s federationCommandStore) CurrentObservationSet(c context.Context, t, k string, n int) (federation.ObservationSet, bool, error) {
	return s.observationSet(c, t, k, n)
}
func (s federationCommandStore) ListCurrentObservations(c context.Context, t, k string, n int) ([]federation.Observation, bool, error) {
	return s.current(c, t, k, n)
}

// These digests describe the static fixture's canonical reviewed configuration.
// The update changes only DisplayName. No preview command prepares a mutation test.
var federationOriginalConfiguration = federationFixtureConfigurationDigest("Fixture source")
var federationRenamedConfiguration = federationFixtureConfigurationDigest("Renamed source")

// This literal independently fixes the reviewed wire shape and its defaults.
func federationFixtureConfigurationDigest(name string) string {
	raw := `{"artworkEnabled":false,"presentationGeneration":1,"displayName":"` + name + `","registrySearchUrl":"","registryListUrl":"","retrievalMode":"indexed","scope":{"kinds":["agent"]},"updateMode":"manual_review","live":{"enabled":false,"queryPrivacyAcknowledged":false,"federationMode":"none"},"referrals":{"enabled":false},"discoveryAssignments":null,"nesting":{"enabled":false},"credentialBinding":"","networkProfile":""}`
	return audit.DigestBytes([]byte(raw))
}

func federationPreviewFixture(t *testing.T, intent federation.ChangeIntent) federation.ChangePreview {
	t.Helper()
	p := federation.ChangePreview{ID: projectionNext, TenantID: projectionTenant, SourceID: projectionResource, SourceRevision: 3, Intent: intent, ConfigurationDigest: federationOriginalConfiguration, ObservationSetDigest: audit.DigestBytes(nil), CreatedAt: projectionTime, ExpiresAt: projectionTime.Add(10 * time.Minute)}
	if intent == federation.ChangeUpdate {
		p.ConfigurationDigest = federationRenamedConfiguration
	}
	raw, err := json.Marshal(map[string]any{"id": p.ID, "tenantId": p.TenantID, "sourceId": p.SourceID, "revision": p.SourceRevision, "intent": p.Intent, "configurationDigest": p.ConfigurationDigest, "observationSetDigest": p.ObservationSetDigest, "currentMatched": p.CurrentMatched, "proposedMatched": p.ProposedMatched, "expiresAt": p.ExpiresAt})
	if err != nil {
		t.Fatal(err)
	}
	p.Digest = audit.DigestBytes(raw)
	if err := federation.ValidateChangePreviewIntegrity(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFederationConnectProjection(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"created", "save-failed", "stale-inspection"} {
		t.Run(outcome, func(t *testing.T) {
			actor := projectionActor(identity.RoleSourceAdmin)
			ctx := identityfixture.WithActor(t.Context(), actor)
			inspection := federationFixtureInspection()
			reads, writes := 0, 0
			var retained federation.Source
			store := federationCommandStore{federationProjectionStore: federationProjectionStore{getInspection: func(c context.Context, tenant, key string) (federation.Inspection, error) {
				reads++
				if !sameProjectionContext(c, ctx) || tenant != actor.TenantID || key != inspection.ID {
					t.Fatal("connect inspection binding")
				}
				result := inspection
				if outcome == "stale-inspection" {
					result.ExpiresAt = projectionTime
				}
				return result, nil
			}}, create: func(c context.Context, source federation.Source, e ...federation.MutationEvidence) (federation.Source, error) {
				writes++
				if !sameProjectionContext(c, ctx) || source.TenantID != actor.TenantID || source.DisplayName != "Connected source" || source.Endpoint != inspection.ResolvedURL || source.Revision != 1 || source.Status != federation.SourceActive || source.InspectionID != inspection.ID || source.RetrievalMode != federation.RetrievalIndexed || !reflect.DeepEqual(source.Scope.Kinds, []catalog.Kind{catalog.KindAgent}) {
					t.Fatalf("source=%+v", source)
				}
				requireFederationMutation(t, actor, source.ID, e, audit.ActionSourceConnected, "Connect reviewed source")
				retained = source
				if outcome == "save-failed" {
					return federation.Source{}, catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-store")
				}
				return source, nil
			}}
			s := federationProjectionServer(t, store, nil)
			body := `{"inspectionId":"` + inspection.ID + `","displayName":" Connected source ","scope":{"kinds":["agent"]},"retrievalMode":"indexed","updateMode":"manual_review","reason":"Connect reviewed source"}`
			r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/sources", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.handleSourceConnect(w, r)
			switch outcome {
			case "stale-inspection":
				requireProjectionProblem(t, w, 412, catalog.CodePreconditionFailed)
			case "save-failed":
				requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
			default:
				requireFederationJSON(t, w, 201, retained)
				if w.Header().Get("ETag") != `"rev-1"` || w.Header().Get("Location") != "/api/v1/federation/sources/"+retained.ID {
					t.Fatal("connect receipt headers")
				}
			}
			wantWrites := 1
			if outcome == "stale-inspection" {
				wantWrites = 0
			}
			if reads != 1 || writes != wantWrites {
				t.Fatalf("reads=%d writes=%d", reads, writes)
			}
			if outcome != "created" && (w.Header().Get("ETag") != "" || w.Header().Get("Location") != "") {
				t.Fatal("failed connection advertised source")
			}
		})
	}
}

func requireFederationMutation(t *testing.T, actor identity.Actor, target string, e []federation.MutationEvidence, action, reason string) {
	t.Helper()
	if err := federation.ValidateMutationEvidence(actor.TenantID, target, e, action); err != nil {
		t.Fatal(err)
	}
	if len(e) != 1 || e[0].Audit.ID == "" || e[0].Audit.Actor != actor.MembershipID || e[0].Audit.Reason != reason {
		t.Fatalf("mutation attribution=%+v", e)
	}
}

func TestFederationPreviewProjection(t *testing.T) {
	t.Parallel()
	for _, intent := range []federation.ChangeIntent{federation.ChangeUpdate, federation.ChangeRemove} {
		t.Run(string(intent), func(t *testing.T) {
			actor := projectionActor(identity.RoleSourceAdmin)
			ctx := identityfixture.WithActor(t.Context(), actor)
			source := federationFixtureSource()
			var retained federation.ChangePreview
			calls := []string{}
			check := func(c context.Context, tenant, key string, n int) {
				if !sameProjectionContext(c, ctx) || tenant != actor.TenantID || key != source.ID || n != federation.MaxSynchronizationEntries {
					t.Fatal("preview selection binding")
				}
			}
			store := federationCommandStore{
				federationProjectionStore: federationProjectionStore{getSource: func(c context.Context, tenant, key string) (federation.Source, error) {
					check(c, tenant, key, federation.MaxSynchronizationEntries)
					calls = append(calls, "source")
					return source, nil
				}},
				observationSet: func(c context.Context, tenant, key string, n int) (federation.ObservationSet, bool, error) {
					check(c, tenant, key, n)
					calls = append(calls, "set")
					return federation.ObservationSet{Digest: audit.DigestBytes(nil)}, false, nil
				},
				current: func(c context.Context, tenant, key string, n int) ([]federation.Observation, bool, error) {
					check(c, tenant, key, n)
					calls = append(calls, "observations")
					return []federation.Observation{}, false, nil
				},
				savePreview: func(c context.Context, p federation.ChangePreview, e ...federation.MutationEvidence) (federation.ChangePreview, error) {
					calls = append(calls, "save")
					if !sameProjectionContext(c, ctx) || p.TenantID != actor.TenantID || p.SourceID != source.ID || p.SourceRevision != 3 || p.Intent != intent || p.CurrentMatched != 0 || p.ProposedMatched != 0 || p.SendsQueriesUpstream {
						t.Fatalf("preview=%+v", p)
					}
					wantConfig := federationOriginalConfiguration
					if intent == federation.ChangeUpdate {
						wantConfig = federationRenamedConfiguration
					}
					if p.ConfigurationDigest != wantConfig {
						t.Fatalf("configuration digest=%s want=%s", p.ConfigurationDigest, wantConfig)
					}
					if err := federation.ValidateChangePreviewIntegrity(p); err != nil {
						t.Fatal(err)
					}
					requireFederationMutation(t, actor, p.ID, e, audit.ActionSourceChangePreviewCreated, "")
					retained = p
					return p, nil
				},
			}
			s := federationProjectionServer(t, store, nil)
			body := `{"intent":"remove"}`
			if intent == federation.ChangeUpdate {
				body = `{"intent":"update","displayName":"Renamed source"}`
			}
			r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/preview", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.SetPathValue("sourceId", source.ID)
			w := httptest.NewRecorder()
			s.handleSourceChangePreview(w, r)
			requireFederationJSON(t, w, 200, retained)
			if !reflect.DeepEqual(calls, []string{"source", "set", "observations", "save"}) {
				t.Fatalf("preview calls=%v", calls)
			}
		})
	}
}

func TestFederationMutationProjection(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"update", "pause", "resume", "remove"} {
		for _, outcome := range []string{"success", "write-failed", "stale"} {
			t.Run(action+"/"+outcome, func(t *testing.T) {
				actor := projectionActor(identity.RoleSourceAdmin)
				ctx := identityfixture.WithActor(t.Context(), actor)
				source := federationFixtureSource()
				if action == "resume" {
					source.Status = federation.SourcePaused
				}
				intent := federation.ChangeRemove
				if action == "update" {
					intent = federation.ChangeUpdate
				}
				preview := federationPreviewFixture(t, intent)
				calls := []string{}
				var saved federation.Source
				check := func(c context.Context, tenant, key string) {
					if !sameProjectionContext(c, ctx) || tenant != actor.TenantID || key != source.ID {
						t.Fatal("mutation binding")
					}
				}
				store := federationCommandStore{
					federationProjectionStore: federationProjectionStore{getSource: func(c context.Context, tenant, key string) (federation.Source, error) {
						check(c, tenant, key)
						calls = append(calls, "source")
						return source, nil
					}},
					getPreview: func(c context.Context, tenant, key string) (federation.ChangePreview, error) {
						check(c, tenant, source.ID)
						calls = append(calls, "preview")
						if key != preview.Digest {
							t.Fatal("preview digest handoff")
						}
						return preview, nil
					},
					observationSet: func(c context.Context, tenant, key string, n int) (federation.ObservationSet, bool, error) {
						check(c, tenant, key)
						calls = append(calls, "set")
						if n != federation.MaxSynchronizationEntries {
							t.Fatal("set bound")
						}
						return federation.ObservationSet{Digest: preview.ObservationSetDigest}, false, nil
					},
					update: func(c context.Context, proposed federation.Source, expected int64, e ...federation.MutationEvidence) (federation.Source, error) {
						check(c, proposed.TenantID, proposed.ID)
						calls = append(calls, "write")
						if expected != 3 && outcome != "stale" {
							t.Fatal("expected revision handoff")
						}
						if expected != source.Revision {
							return federation.Source{}, catalog.E(catalog.CodePreconditionFailed, "source revision does not match")
						}
						actionName := map[string]string{"update": audit.ActionSourceRevisionActivated, "pause": audit.ActionSourcePaused, "resume": audit.ActionSourceResumed, "remove": audit.ActionSourceRemoved}[action]
						requireFederationMutation(t, actor, source.ID, e, actionName, "Operator review")
						want := source
						want.Revision = 4
						want.UpdatedAt = projectionTime
						switch action {
						case "update":
							want.DisplayName = "Renamed source"
						case "pause":
							want.Status = federation.SourcePaused
						case "resume":
							want.Status = federation.SourceActive
						case "remove":
							want.Status = federation.SourceRemoved
						}
						// InspectionID is transient write metadata, not an API field.
						want.InspectionID = proposed.InspectionID
						if !reflect.DeepEqual(proposed, want) {
							t.Fatalf("proposed=%+v want=%+v", proposed, want)
						}
						if action == "update" || action == "remove" {
							if e[0].ExpectedObservationSet == nil || *e[0].ExpectedObservationSet != (federation.ObservationSet{Digest: preview.ObservationSetDigest}) {
								t.Fatal("missing observation fence")
							}
						}
						saved = proposed
						if outcome == "write-failed" {
							return federation.Source{}, catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-store")
						}
						return proposed, nil
					},
				}
				s := federationProjectionServer(t, store, nil)
				body := map[string]string{"reason": "  Operator review  "}
				if action == "remove" || action == "update" {
					body["changePreviewDigest"] = preview.Digest
				}
				if action == "update" {
					body["displayName"] = "Renamed source"
				}
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/source", strings.NewReader(string(raw)))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("If-Match", `"rev-3"`)
				if outcome == "stale" {
					r.Header.Set("If-Match", `"rev-2"`)
				}
				r.SetPathValue("sourceId", source.ID)
				handler := s.handleSourcePatch
				switch action {
				case "pause":
					handler = s.handleSourcePause
				case "resume":
					handler = s.handleSourceResume
				case "remove":
					handler = s.handleSourceRemove
				}
				w := httptest.NewRecorder()
				handler(w, r)
				switch outcome {
				case "stale":
					requireProjectionProblem(t, w, 412, catalog.CodePreconditionFailed)
				case "write-failed":
					requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
				default:
					if action == "remove" {
						if w.Code != 204 || w.Body.Len() != 0 || w.Header().Get("ETag") != "" {
							t.Fatalf("removal=%d %v %s", w.Code, w.Header(), w.Body)
						}
					} else {
						requireFederationJSON(t, w, 200, saved)
						if w.Header().Get("ETag") != `"rev-4"` {
							t.Fatal("updated ETag")
						}
					}
				}
				wantCalls := []string{"source", "write"}
				if action == "update" || action == "remove" {
					wantCalls = []string{"source", "preview", "set", "write"}
				}
				if action == "update" && outcome == "stale" {
					wantCalls = []string{"source"}
				}
				if !reflect.DeepEqual(calls, wantCalls) {
					t.Fatalf("calls=%v want=%v", calls, wantCalls)
				}
			})
		}
	}
}

func TestFederationPatchPreservesOmissionAndExplicitValues(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{}`, `{"inspectionId":"` + projectionItem + `","displayName":"Renamed source","retrievalMode":"indexed","scope":{"kinds":["agent"]},"updateMode":"manual_review","live":{"enabled":false,"queryPrivacyAcknowledged":false,"federationMode":"none"},"referrals":{"enabled":false},"discoveryAssignments":[],"nesting":{"enabled":false},"credentialBinding":"","networkProfile":""}`} {
		var patch sourceConfigurationPatch
		var expected federation.PatchInput
		if err := json.Unmarshal([]byte(body), &patch); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(body), &expected); err != nil {
			t.Fatal(err)
		}
		if got := patch.domainInput(); !reflect.DeepEqual(got, expected) {
			t.Fatalf("patch=%+v want=%+v", got, expected)
		}
	}
}
