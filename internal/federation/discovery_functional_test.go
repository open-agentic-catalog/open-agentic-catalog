//go:build functional

package federation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// The production router supplies policy evidence and delegates to the hardened
// fetcher. Only this tenant's owned HTTPS origin is reachable through the route.
func newFederationHTTPSOrigin(t *testing.T, handler http.Handler) (*httptest.Server, *fetcher.Router) {
	t.Helper()
	return newFederationHTTPSOriginWithResponseLimit(t, handler, 16<<10)
}

func newFederationHTTPSOriginWithResponseLimit(t *testing.T, handler http.Handler, maxResponseBytes int64) (*httptest.Server, *fetcher.Router) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	router, err := fetcher.NewRouter(fetcher.RoutingPolicy{
		ID: "https-discovery-test", Revision: 7, Digest: federationTestPolicyDigest,
		Public: fetcher.PublicRoute{Mode: fetcher.PublicDisabled},
		PrivateRoutes: []fetcher.PrivateRoute{{
			Name: "owned-https", Origins: []string{server.URL},
			AddressPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, AssignedTenantIDs: []string{auditEvidenceTenantID},
			TrustBundlePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), AllowTestAddresses: true,
		}},
		Timeout: 3 * time.Second, MaxResponseBytes: maxResponseBytes, MaxRedirects: 2,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return server, router
}

func TestInspectionHTTPSFallsBackFromInvalidWellKnown(t *testing.T) {
	for _, tc := range []struct {
		name, mediaType, body string
	}{
		{"malformed JSON", ardDocumentMediaType, `{"entries":[`},
		{"contradictory profile", aiCatalogDocumentMediaType, `{"specVersion":"1.0","entries":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const manifest = `{"entries":[{"identifier":"urn:air:remote.example:default:one","displayName":"One","type":"application/a2a-agent-card+json","data":{"name":"One"}}]}`
			var mu sync.Mutex
			var requests []string
			server, outbound := newFederationHTTPSOrigin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests = append(requests, r.Method+" "+r.URL.RequestURI())
				mu.Unlock()
				if r.TLS == nil || r.Method != http.MethodGet {
					t.Errorf("discovery request was not an HTTPS GET: %s", r.Method)
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				switch r.URL.Path {
				case "/.well-known/ard.json":
					w.Header().Set("Content-Type", tc.mediaType)
					_, _ = io.WriteString(w, tc.body)
				case "/":
					w.Header().Set("Content-Type", "text/html")
					_, _ = io.WriteString(w, `<html><head><link rel="ard" href="/manifest.json"></head></html>`)
				case "/manifest.json":
					w.Header().Set("Content-Type", ardDocumentMediaType)
					_, _ = io.WriteString(w, manifest)
				default:
					t.Errorf("unexpected discovery path: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			evidence := &recordingFederationEvidence{}
			store := NewMemoryStore(evidence)
			now := time.Date(2026, time.September, 22, 12, 0, 0, 0, time.UTC)
			manager, err := NewManager(store, outbound, clock.NewFake(now), id.Generator{}, WithEvidenceSink(evidence, true))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(auditEvidenceContext(), 10*time.Second)
			defer cancel()
			inspection, err := manager.InspectWithAccess(ctx, InspectionInput{URL: server.URL, NetworkProfile: "owned-https"})
			if err != nil || ctx.Err() != nil {
				t.Fatalf("HTTPS fallback failed: %v, deadline=%v", err, ctx.Err())
			}
			server.Close()
			mu.Lock()
			gotRequests := slices.Clone(requests)
			mu.Unlock()
			if want := []string{"GET /.well-known/ard.json", "GET /", "GET /manifest.json"}; !slices.Equal(gotRequests, want) {
				t.Fatalf("discovery requests = %v, want %v", gotRequests, want)
			}
			if inspection.RequestedURL != server.URL || inspection.ResolvedURL != server.URL+"/manifest.json" || inspection.SourceType != SourceARDManifest || inspection.ProfileID != "ard-0.91-aa3e598" || inspection.MediaType != ardDocumentMediaType {
				t.Fatalf("fallback retained the wrong interface: %#v", inspection)
			}
			if inspection.EntryCount != 1 || inspection.KindCounts["agent"] != 1 || !inspection.Capabilities["enumerable"] || inspection.Capabilities["search"] || inspection.RegistrySearchURL != "" || !hasFinding(inspection.Warnings, "ARD_DISCOVERY_RESOLVED") {
				t.Fatalf("fallback summary = %#v", inspection)
			}
			if inspection.BodyDigest != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(manifest))) || inspection.PolicyID != "https-discovery-test" || inspection.PolicyRevision != 7 || inspection.PolicyDigest != outbound.PolicyDigest() || inspection.NetworkProfile != "owned-https" || !inspection.ExpiresAt.Equal(now.Add(15*time.Minute)) {
				t.Fatalf("fallback evidence does not bind the accepted document and route: %#v", inspection)
			}
			retained, err := store.GetInspection(ctx, auditEvidenceTenantID, inspection.ID)
			if err != nil || !reflect.DeepEqual(retained, inspection) {
				t.Fatalf("retained inspection = %#v, %v", retained, err)
			}
			if err := ValidateRetainedInspectionIntegrity(retained); err != nil {
				t.Fatalf("invalid retained inspection: %v", err)
			}
			if len(evidence.audit) != 1 || len(evidence.outbox) != 0 || evidence.audit[0].Action != audit.ActionSourceInspected || evidence.audit[0].TargetID != inspection.ID {
				t.Fatalf("fallback should record one successful inspection: %#v", evidence)
			}
			facts := auditFacts(t, evidence.audit[0])
			if facts["outcome"] != outboundOutcomeSucceeded || facts["policyDigest"] != outbound.PolicyDigest() || facts["inspectionDigest"] != inspectionDigest(inspection) {
				t.Fatalf("fallback audit facts = %#v", facts)
			}
		})
	}
}

func TestInspectionHTTPSRegistryProbe(t *testing.T) {
	const privateDetail = "private-registry-response-detail"
	for _, manifest := range []string{"current", "predecessor", "absent"} {
		for _, tc := range []struct {
			name, outcome string
			code          catalog.ErrorCode
			status        int
			body          string
		}{
			{"valid empty response", outboundOutcomeSucceeded, "", http.StatusOK, `{"results":[]}`},
			{"HTTP failure", outboundOutcomeHTTPError, catalog.CodeInvalidArgument, http.StatusServiceUnavailable, `{"error":"` + privateDetail + `"}`},
			{"invalid Search envelope", outboundOutcomeParseError, catalog.CodeInvalidArgument, http.StatusOK, `{"results":{"error":"` + privateDetail + `"}}`},
			{"connection closed", outboundOutcomeTransportError, catalog.CodeDependencyUnavailable, 0, ""},
		} {
			t.Run(manifest+"/"+tc.name, func(t *testing.T) {
				var mu sync.Mutex
				var requests []string
				server, outbound := newFederationHTTPSOrigin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					requests = append(requests, r.Method+" "+r.URL.RequestURI())
					mu.Unlock()
					if r.TLS == nil {
						t.Error("Registry probe did not use HTTPS")
					}
					w.Header().Set("Content-Type", "application/json")
					if r.Method == http.MethodGet {
						switch r.URL.Path {
						case "/ard/v1/search":
							w.Header().Set("Allow", http.MethodPost)
							w.WriteHeader(http.StatusMethodNotAllowed)
						case "/.well-known/ard.json", "/.well-known/ai-catalog.json":
							if manifest == "current" && r.URL.Path == "/.well-known/ard.json" || manifest == "predecessor" && r.URL.Path == "/.well-known/ai-catalog.json" {
								entries := []map[string]string{}
								for _, name := range []string{"one", "two"} {
									entries = append(entries, map[string]string{"identifier": "urn:air:remote.example:registry:" + name, "displayName": name, "type": "application/ai-registry+json", "url": "https://" + r.Host + "/registries/" + name})
								}
								if err := json.NewEncoder(w).Encode(map[string]any{"specVersion": "1.0", "entries": entries}); err != nil {
									t.Errorf("encode fixture manifest: %v", err)
								}
							} else {
								w.WriteHeader(http.StatusNotFound)
							}
						default:
							t.Errorf("unexpected discovery path: %s", r.URL.Path)
							http.NotFound(w, r)
						}
						return
					}
					if r.Method != http.MethodPost || r.URL.Path != "/ard/v1/search" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
						t.Errorf("unexpected Registry probe: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
						http.Error(w, "unexpected probe", http.StatusBadRequest)
						return
					}
					body, err := io.ReadAll(io.LimitReader(r.Body, 1025))
					var probe struct {
						Query struct {
							Text string `json:"text"`
						} `json:"query"`
						Federation string `json:"federation"`
						PageSize   int    `json:"pageSize"`
					}
					if err != nil || len(body) > 1024 || json.Unmarshal(body, &probe) != nil || strings.TrimSpace(probe.Query.Text) == "" || probe.Federation != "none" || probe.PageSize != 1 {
						t.Errorf("unexpected Registry probe body: %s, %v", body, err)
						http.Error(w, "unexpected body", http.StatusBadRequest)
						return
					}
					if tc.status == 0 {
						connection, _, err := http.NewResponseController(w).Hijack()
						if err != nil {
							t.Errorf("hijack owned Registry connection: %v", err)
							return
						}
						_ = connection.Close()
						return
					}
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
				}))
				evidence := &recordingFederationEvidence{}
				store := NewMemoryStore(evidence)
				manager, err := NewManager(store, outbound, clock.NewFake(time.Date(2026, time.September, 22, 12, 0, 0, 0, time.UTC)), id.Generator{}, WithEvidenceSink(evidence, true))
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(auditEvidenceContext(), 10*time.Second)
				defer cancel()
				endpoint := server.URL + "/ard/v1/search"
				inspection, operationErr := manager.InspectWithAccess(ctx, InspectionInput{URL: endpoint, NetworkProfile: "owned-https"})
				if ctx.Err() != nil {
					t.Fatalf("Registry fixture exceeded its deadline: %v", ctx.Err())
				}
				server.Close()
				mu.Lock()
				gotRequests := slices.Clone(requests)
				mu.Unlock()
				// Explicit Search selection must not be masked by another document.
				wantRequests := []string{"GET /ard/v1/search", "POST /ard/v1/search"}
				if !slices.Equal(gotRequests, wantRequests) {
					t.Fatalf("Registry requests = %v, want %v", gotRequests, wantRequests)
				}
				if len(evidence.audit) != 1 || len(evidence.outbox) != 0 {
					t.Fatalf("Registry probe must retain exactly one audit and no outbox: %#v", evidence)
				}
				record := evidence.audit[0]
				if record.Action != audit.ActionSourceInspected || record.Actor != auditEvidenceMembershipID || record.RequestID != "audit-evidence-request" {
					t.Fatalf("Registry failure attribution = %#v", record)
				}
				facts := auditFacts(t, record)
				configurationDigest, ok := facts["configurationDigest"].(string)
				if facts["outcome"] != tc.outcome || facts["originClass"] != "named_private:owned-https" || facts["policyDigest"] != outbound.PolicyDigest() || !ok || !validPolicyDigest(configurationDigest) {
					t.Fatalf("Registry failure facts = %#v", facts)
				}
				retained, readErr := store.GetInspection(ctx, auditEvidenceTenantID, record.TargetID)
				if tc.code == "" {
					if operationErr != nil || readErr != nil || inspection.ID != record.TargetID || !reflect.DeepEqual(retained, inspection) || inspection.ResolvedURL != endpoint || inspection.RegistrySearchURL != endpoint || inspection.SourceType != SourceRegistry || inspection.ProfileID != "ard-0.91-aa3e598" || !inspection.Capabilities["search"] || inspection.Capabilities["enumerable"] || inspection.EntryCount != 0 {
						t.Fatalf("valid Registry probe was not retained correctly: %#v, error=%v, read=%v", inspection, operationErr, readErr)
					}
					if err := ValidateRetainedInspectionIntegrity(retained); err != nil {
						t.Fatalf("invalid retained Registry inspection: %v", err)
					}
					return
				}
				if catalog.ErrorCodeOf(operationErr) != tc.code || !catalog.ErrorPreservesEvidence(operationErr) || !reflect.DeepEqual(inspection, Inspection{}) || catalog.ErrorCodeOf(readErr) != catalog.CodeNotFound || len(facts) != 4 {
					t.Fatalf("Registry failure retained an inspection or lost its classification: %#v, error=%v, read=%v, facts=%v", inspection, operationErr, readErr, facts)
				}
				encoded, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				for _, sensitive := range []string{server.URL, privateDetail, "external-subject-is-not-audit-identity"} {
					if strings.Contains(string(encoded), sensitive) {
						t.Fatalf("Registry failure audit exposed %q: %s", sensitive, encoded)
					}
				}
				if strings.Contains(operationErr.Error(), privateDetail) {
					t.Fatalf("Registry error disclosed the response body: %v", operationErr)
				}
			})
		}
	}
}

func TestInspectionHTTPSPredecessorPathAcceptsCurrentARD(t *testing.T) {
	const manifest = `{"specVersion":"1.0","host":{"trustManifest":{"identity":"https://remote.example","identityType":"https"}},"entries":[{"identifier":"urn:air:remote.example:server:one","displayName":"One","type":"application/mcp-server-card+json","url":"https://remote.example/one"}]}`
	server, outbound := newFederationHTTPSOrigin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/ai-catalog.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, manifest)
	}))
	manager, err := NewManager(NewMemoryStore(), outbound, clock.Real{}, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := manager.InspectWithAccess(auditEvidenceContext(), InspectionInput{URL: server.URL + "/.well-known/ai-catalog.json", NetworkProfile: "owned-https"})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.SourceType != SourceARDManifest || inspection.ProfileID != ardProfileID || inspection.EntryCount != 1 {
		t.Fatalf("current ARD through alias: %#v", inspection)
	}
	source, err := manager.Connect(auditEvidenceContext(), ConnectInput{InspectionID: inspection.ID, DisplayName: "Native alias", Scope: Scope{Publishers: []string{"remote.example"}}, RetrievalMode: RetrievalIndexed, UpdateMode: UpdateManual, NetworkProfile: "owned-https", Reason: "Qualify native alias"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Sync(auditEvidenceContext(), source.ID)
	if err != nil || snapshot.MemberCount != 1 {
		t.Fatalf("alias sync: %#v, %v", snapshot, err)
	}
}
