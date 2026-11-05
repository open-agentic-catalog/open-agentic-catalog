//go:build functional

// Functional tests exercise real process, network or application composition boundaries.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// clientSkillCall runs controlled argv with a mounted token and decodes only
// complete successful output. No helper ever reads the token into model input.
func clientSkillCall(t *testing.T, ctx context.Context, origin, tokenFile string, args ...string) (remoteEnvelope, error) {
	t.Helper()
	var output bytes.Buffer
	err := runRemote(ctx, args, func(name string) string {
		switch name {
		case "CATALOG_REMOTE_BASE_URL":
			return origin
		case "CATALOG_REMOTE_TOKEN_FILE":
			return tokenFile
		default:
			return ""
		}
	}, &output)
	var result remoteEnvelope
	if err != nil {
		if output.Len() != 0 {
			t.Fatal("failed request emitted success JSON")
		}
		return result, err
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result, nil
}

// TestClientSkillCatalogWorkflow uses the existing governed demo for real
// Reader search/inspection. Writes during setup are excluded from observation.
func TestClientSkillCatalogWorkflow(t *testing.T) {
	runtime := buildSmokeTestRuntime(t)
	defer runtime.Close()
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		runtime.HTTP.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	if _, err := executeDemo(t.Context(), server.URL, filepath.Join(t.TempDir(), "receipt.json")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	requests = nil
	mu.Unlock()
	tokenFile := clientSkillToken(t, "local-reader")
	capabilities, err := clientSkillCall(t, t.Context(), server.URL, tokenFile, "capabilities")
	if err != nil {
		t.Fatal(err)
	}
	var capability struct {
		Operations []string       `json:"operations"`
		Kinds      []catalog.Kind `json:"resourceKinds"`
		Search     struct {
			Tier     string `json:"tier"`
			Semantic string `json:"semantic"`
		} `json:"search"`
	}
	if json.Unmarshal(capabilities.Data, &capability) != nil || !slices.Contains(capability.Operations, "catalog.search") || capability.Search.Tier != "core" || capability.Search.Semantic != "disabled" {
		t.Fatalf("unexpected capabilities: %s", capabilities.Data)
	}
	for _, kind := range []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill, catalog.KindPlugin} {
		if !slices.Contains(capability.Kinds, kind) {
			t.Fatalf("capability missing %s", kind)
		}
	}
	response, err := clientSkillCall(t, t.Context(), server.URL, tokenFile, "search", "--text", "order", "--limit", "5")
	if err != nil {
		t.Fatal(err)
	}
	var page catalog.CatalogSearchResponse
	if json.Unmarshal(response.Data, &page) != nil || len(page.Items) != 3 || page.Partial {
		t.Fatalf("unexpected search: %s", response.Data)
	}
	kinds := make(map[catalog.Kind]bool, len(page.Items))
	for _, item := range page.Items {
		if item.Type != catalog.SearchResultLocal || item.Local == nil || !item.VersionPresent || item.Version != item.Local.Version.Version {
			t.Fatal("demo result lost local provenance or explicit version")
		}
		kinds[item.Kind] = true
	}
	for _, kind := range []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill} {
		if !kinds[kind] {
			t.Fatalf("governed demo search missing %s", kind)
		}
	}
	selected := page.Items[0]
	if selected.Type != catalog.SearchResultLocal || selected.Local == nil || selected.Local.Release.ID == selected.Local.Resource.ID || selected.ID == selected.Local.Release.ID {
		t.Fatal("selection lost distinct release identity")
	}
	response, err = clientSkillCall(t, t.Context(), server.URL, tokenFile, "record", "--id", selected.Local.Release.ID)
	if err != nil {
		t.Fatal(err)
	}
	var record catalog.CatalogRecord
	if json.Unmarshal(response.Data, &record) != nil || record.Release.ID != selected.Local.Release.ID || len(record.VersionRevision.Representations) == 0 {
		t.Fatal("record did not retain selected release evidence")
	}
	mu.Lock()
	got := slices.Clone(requests)
	mu.Unlock()
	want := []string{"GET /api/v1/capabilities", "POST /api/v1/catalog/search", "GET /api/v1/catalog/resources/" + selected.Local.Release.ID}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("discovery made unexpected requests: %v", got)
	}
	for _, test := range []struct {
		name, token string
		status      int
		args        []string
	}{
		{"invalid token", "invalid-token", 401, []string{"capabilities"}},
		{"unknown release", "local-reader", 404, []string{"record", "--id", clientSkillReleaseID}},
		{"resource is not release", "local-reader", 404, []string{"record", "--id", selected.Local.Resource.ID}},
		{"other tenant", "other-tenant-admin", 404, []string{"record", "--id", selected.Local.Release.ID}},
		{"invalid cursor", "local-reader", 400, []string{"search", "--cursor", "invalid"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := clientSkillCall(t, t.Context(), server.URL, clientSkillToken(t, test.token), test.args...)
			var failure remoteAPIError
			if !errors.As(err, &failure) || failure.Status != test.status {
				t.Fatalf("expected %d, got %v", test.status, err)
			}
		})
	}
	// Change only this test's in-memory state. Each subsequent CLI call must
	// resolve current publication/access rather than reuse the earlier read.
	admin, err := runtime.Authenticator.Authenticate(t.Context(), "local-admin")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("selected release retired after search", func(t *testing.T) {
		ctx := t.Context()
		adminContext := identity.WithActor(ctx, admin)
		if _, err := runtime.Catalog.TransitionRelease(adminContext, record.Release.ID, catalog.ReleaseRetired, "Retire synthetic selection", "", nil, record.Release.Revision); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		before := len(requests)
		mu.Unlock()
		_, err := clientSkillCall(t, ctx, server.URL, tokenFile, "record", "--id", record.Release.ID)
		var failure remoteAPIError
		if !errors.As(err, &failure) || failure.Status != http.StatusNotFound || failure.Code != string(catalog.CodeNotFound) || failure.RequestID == "" {
			t.Fatalf("stale selected release did not return safe not-found: %v", err)
		}
		mu.Lock()
		staleCalls := slices.Clone(requests[before:])
		mu.Unlock()
		if !slices.Equal(staleCalls, []string{"GET /api/v1/catalog/resources/" + record.Release.ID}) {
			t.Fatalf("stale selection triggered a retry or replacement search: %v", staleCalls)
		}
		result, err := clientSkillCall(t, ctx, server.URL, tokenFile, "search", "--text", "order", "--limit", "5")
		if err != nil {
			t.Fatal(err)
		}
		var refreshed catalog.CatalogSearchResponse
		if json.Unmarshal(result.Data, &refreshed) != nil || len(refreshed.Items) != 2 || refreshed.Partial {
			t.Fatal("explicit fresh search did not reflect withdrawal")
		}
		for _, item := range refreshed.Items {
			if item.Local == nil || item.Local.Release.ID == record.Release.ID {
				t.Fatal("withdrawn release remained discoverable")
			}
		}
	})
	t.Run("capabilities are not Reader permission", func(t *testing.T) {
		ctx := t.Context()
		adminContext := identity.WithActor(ctx, admin)
		reader, err := runtime.Authenticator.Authenticate(ctx, "local-reader")
		if err != nil {
			t.Fatal(err)
		}
		assignments, err := runtime.Identity.ListRoleAssignments(adminContext, identity.RoleAssignmentListRequest{MembershipID: reader.MembershipID, Limit: 100})
		if err != nil || len(assignments) != 1 || assignments[0].Role != identity.RoleReader || assignments[0].NamespaceID != "" {
			t.Fatalf("expected one tenant Reader grant: %v", err)
		}
		if _, err := runtime.Identity.RevokeRole(adminContext, assignments[0].ID, "Remove synthetic tenant read access", "skill-revoke", assignments[0].Revision); err != nil {
			t.Fatal(err)
		}
		for _, namespaceOnly := range []bool{false, true} {
			if namespaceOnly {
				if _, err := runtime.Identity.GrantRole(adminContext, identity.GrantRoleInput{Subject: reader.Subject, ActorType: reader.ActorType, Role: identity.RoleReader, NamespaceID: page.Items[1].Local.Listing.NamespaceID, Reason: "Grant synthetic namespace-only access"}, "skill-namespace"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := clientSkillCall(t, ctx, server.URL, tokenFile, "capabilities"); err != nil {
				t.Fatalf("active membership could not read capabilities: %v", err)
			}
			for _, args := range [][]string{{"search", "--text", "order", "--limit", "5"}, {"record", "--id", page.Items[1].Local.Release.ID}} {
				result, err := clientSkillCall(t, ctx, server.URL, tokenFile, args...)
				if args[0] == "search" {
					if err != nil {
						t.Fatal(err)
					}
					var actual catalog.CatalogSearchResponse
					if json.Unmarshal(result.Data, &actual) != nil {
						t.Fatal("invalid scoped search")
					}
					expected := map[string]bool{}
					if namespaceOnly {
						for _, item := range page.Items {
							if item.Local.Listing.NamespaceID == page.Items[1].Local.Listing.NamespaceID && item.Local.Release.ID != record.Release.ID {
								expected[item.Local.Release.ID] = true
							}
						}
					}
					if len(actual.Items) != len(expected) || actual.DurableTotal != len(expected) {
						t.Fatal("incorrect authorized population", actual)
					}
					for _, item := range actual.Items {
						if item.Local == nil || !expected[item.Local.Release.ID] {
							t.Fatal("unauthorized search result", item)
						}
					}
					continue
				}
				if namespaceOnly && args[0] == "record" {
					if err != nil {
						t.Fatalf("exact namespace read was refused: %v", err)
					}
					continue
				}
				var failure remoteAPIError
				if !errors.As(err, &failure) || failure.Status != http.StatusForbidden || failure.Code != string(catalog.CodeForbidden) || failure.RequestID == "" {
					t.Fatalf("Catalog read accepted insufficient permission (namespace-only=%t): %v", namespaceOnly, err)
				}
			}
		}
	})
}

// TestClientSkillReadEvidence covers Plugin inspection, partial federation and
// large exact payloads without duplicating the server's federation machinery.
func TestClientSkillReadEvidence(t *testing.T) {
	t.Parallel()
	plugin := clientSkillPluginRecord(t)
	for _, test := range []struct {
		name    string
		args    []string
		payload any
	}{
		{"plugin record", []string{"record", "--id", clientSkillReleaseID}, plugin},
		{"large Agent record", []string{"record", "--id", "00000000-0000-7000-8000-000000000003"}, clientSkillLargeRecord(t)},
		{"partial conflict and hostile text", []string{"search", "--text", "triage", "--limit", "5"}, clientSkillExternalPage()},
		{"unavailable search", []string{"capabilities"}, map[string]any{"operations": []string{}, "resourceKinds": []string{}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			want, err := json.Marshal(test.payload)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-ID", "skill-fixture")
				_, _ = w.Write(want)
			}))
			defer server.Close()
			result, err := clientSkillCall(t, t.Context(), server.URL, clientSkillToken(t, "synthetic-reader"), test.args...)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(result.Data, want) || result.Meta.RequestID != "skill-fixture" {
				t.Fatal("CLI changed, dropped or truncated retained evidence")
			}
		})
	}
}

// clientSkillToken creates synthetic credentials owned by the test.
func clientSkillToken(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reader-token")
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
