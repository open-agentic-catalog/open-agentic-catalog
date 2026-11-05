package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/catalogmcp"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/controlclient"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestSharedCatalogReadsAdmitScopesBeforeDependencies(t *testing.T) {
	// Nil dependencies intentionally fail if a refused read reaches any store.
	s := &Server{enforceOAuthScopes: true}
	reads := []func(context.Context) (controlclient.Envelope, error){
		func(c context.Context) (controlclient.Envelope, error) { return s.ReadSearch(c, catalog.SearchQuery{}) },
		func(c context.Context) (controlclient.Envelope, error) {
			return s.ReadExplore(c, catalog.ExploreRequest{})
		},
		func(c context.Context) (controlclient.Envelope, error) { return s.ReadRelease(c, projectionItem) },
		func(c context.Context) (controlclient.Envelope, error) {
			return s.ReadIndexed(c, catalog.IndexedIdentity{})
		},
	}
	for _, read := range reads {
		if _, err := read(t.Context()); catalog.ErrorCodeOf(err) != catalog.CodeUnauthenticated {
			t.Fatal("missing actor admitted", err)
		}
		ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleReader))
		if _, err := read(ctx); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
			t.Fatal("missing OAuth scope admitted", err)
		}
		actor := projectionActor(identity.RoleReader)
		actor.Claims = map[string]string{"scope:catalog.read": "true"}
		ctx, cancel := context.WithCancel(identityfixture.WithActor(t.Context(), actor))
		cancel()
		if _, err := read(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled read admitted", err)
		}
	}
	if _, err := s.ReadCapabilities(t.Context()); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal(err)
	}
	if _, err := s.ReadAccess(t.Context(), catalogmcp.AccessRequest{}.Values()); catalog.ErrorCodeOf(err) != catalog.CodeUnauthenticated {
		t.Fatal(err)
	}
}

func TestSharedMCPAccessUsesExistingSignedSelfPagesWithoutResourceScope(t *testing.T) {
	s := &Server{logger: slog.New(slog.DiscardHandler), cursorKey: []byte(strings.Repeat("k", 32)), now: func() time.Time { return time.Unix(100, 0) }, enforceOAuthScopes: true}
	actor := projectionActor(identity.RoleReader)
	actor.Roles = nil
	actor.Namespaces = map[string][]identity.Role{projectionItem: {identity.RoleReviewer}, projectionResource: {identity.RoleContributor}}
	actor.Claims = map[string]string{"private": "PRIVATE_CLAIM", "scope:audit.read": "true"}
	ctx := context.WithValue(identityfixture.WithActor(t.Context(), actor), requestIDKey{}, "shared-read-request")
	invoke := func(arguments string) (*mcp.CallToolResult, controlclient.Envelope) {
		t.Helper()
		result := catalogmcp.Call(ctx, s, &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "catalog_control_access", Arguments: json.RawMessage(arguments)}}, "unit", 256<<10, "")
		var reply controlclient.Envelope
		if !result.IsError {
			raw, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			if json.Unmarshal(raw, &reply) != nil || reply.Meta.RequestID != "shared-read-request" {
				t.Fatal("lost correlation", string(raw))
			}
			if strings.Contains(string(reply.Data), `"subject"`) || strings.Contains(string(reply.Data), `"realmId"`) || strings.Contains(string(reply.Data), `"membershipId"`) || strings.Contains(string(reply.Data), "PRIVATE_CLAIM") {
				t.Fatal("identity data exposed", string(reply.Data))
			}
		}
		return result, reply
	}
	result, reply := invoke(`{"limit":1}`)
	var first selfResponse
	if result.IsError || json.Unmarshal(reply.Data, &first) != nil || len(first.NamespaceGrants) != 1 || first.NextCursor == "" || len(first.Permissions) != 0 || !first.ScopeEnforcement || len(first.Scopes) != 1 || first.Scopes[0] != "audit.read" {
		t.Fatal("valid first self page refused", result, string(reply.Data))
	}
	// Continue an MCP page through the shared Control read to prove that no
	// MCP-specific cursor format or authority was added.
	next, err := s.ReadAccess(ctx, catalogmcp.AccessRequest{Limit: 1, Cursor: first.NextCursor}.Values())
	var second selfResponse
	if err != nil || json.Unmarshal(next.Data, &second) != nil || len(second.NamespaceGrants) != 1 || second.NextCursor != "" || second.NamespaceGrants[0].NamespaceID == first.NamespaceGrants[0].NamespaceID {
		t.Fatal("invalid continuation", err, string(next.Data))
	}
	if result, _ := invoke(`{"limit":2,"cursor":"` + first.NextCursor + `"}`); !result.IsError {
		t.Fatal("changed limit accepted")
	}
	if result, _ := invoke(`{"namespaceId":"` + projectionItem + `"}`); result.IsError {
		t.Fatal("exact namespace access refused")
	}
	if result := catalogmcp.Call(ctx, s, &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "catalog_control_search", Arguments: json.RawMessage(`{}`)}}, "unit", 256<<10, ""); !result.IsError {
		t.Fatal("navigation hints bypassed OAuth admission")
	}
}
