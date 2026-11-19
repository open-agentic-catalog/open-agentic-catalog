//go:build functional

package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

type ardMCPHostTransport struct {
	base http.RoundTripper
	host string
}

func TestARDMCPHTTPCurrentHostAuthorityAndMetadata(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			snapshot, _ := publicFixtureSnapshot(t, DiscoveryAuthenticated)
			var reads atomic.Int64
			var active atomic.Pointer[publication.Snapshot]
			active.Store(&snapshot)
			projection := publicProjectionServer(t, DiscoveryAuthenticated, func(context.Context, string) (publication.Snapshot, error) {
				reads.Add(1)
				if value := active.Load(); value != nil {
					return *value, nil
				}
				return publication.Snapshot{}, catalog.E(catalog.CodeNotFound, "private-fixture-no-publication")
			})
			binding := projection.discovery[publicFixtureHost]
			binding.MCPEnabled = true
			var authority *identity.Service
			var authenticate func(context.Context, string) (identity.Actor, error)
			server := newConfiguredHTTPServer(t, func(d *Dependencies) {
				d.MCP = config.Defaults().MCP
				d.PublicBaseURL, d.AuthIssuer = publicFixtureOrigin, "https://issuer.example.com"
				d.DiscoveryHosts = []DiscoveryHost{binding}
				d.EnforceOAuthScopes = true
				base, service := newTestIdentity(t, clock.NewFake(d.Now()), map[string]config.DevelopmentUser{
					"owner":   {Subject: "owner", TenantID: projectionTenant, Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleReader}},
					"reader":  {Subject: "reader", TenantID: projectionTenant, Roles: []identity.Role{identity.RoleReader}},
					"foreign": {Subject: "foreign", TenantID: "00000000-0000-7000-8000-000000000099", Roles: []identity.Role{identity.RoleReader}},
				})
				authority, authenticate = service, base.Authenticate
				d.Identity = service
				d.Authenticator = mcpAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
					name, scope, _ := strings.Cut(token, "/")
					actor, err := base.Authenticate(ctx, name)
					actor.Claims = map[string]string{"scope:" + scope: "true"}
					return actor, err
				})
			})
			defer server.Close()
			client := server.Client()
			client.Transport = ardMCPHostTransport{client.Transport, publicFixtureHost}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+ardMCPMetadataPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			var resource struct {
				Resource             string   `json:"resource"`
				AuthorizationServers []string `json:"authorization_servers"`
			}
			err = json.NewDecoder(metadata.Body).Decode(&resource)
			_ = metadata.Body.Close()
			if err != nil || metadata.StatusCode != http.StatusOK || resource.Resource != publicFixtureOrigin+ardMCPPath || len(resource.AuthorizationServers) != 1 || resource.AuthorizationServers[0] != "https://issuer.example.com" || reads.Load() != 0 {
				t.Fatal("metadata required private access or used wrong resource", resource, err)
			}
			method := "server/discover"
			if version == "2025-11-25" {
				method = "initialize"
			}
			for token, want := range map[string]int{"": 401, "invalid/catalog.read": 401, "foreign/catalog.read": 403, "reader/catalog.write": 403} {
				status, headers, body := mcpRequest(t, client, server.URL+ardMCPPath, token, version, method, "", mcpWire(t, version, method, "", nil), nil)
				if status != want || reads.Load() != 0 || strings.Contains(string(body), "private-fixture") {
					t.Fatal("host admission", token, status, string(body))
				}
				if status == 401 || token == "reader/catalog.write" {
					if !strings.Contains(headers.Get("WWW-Authenticate"), publicFixtureOrigin+ardMCPMetadataPath) {
						t.Fatal("wrong protected-resource challenge", headers)
					}
				}
			}
			query := map[string]any{"query": map[string]any{"text": "fixture"}, "federation": "none", "pageSize": 1}
			status, _, body := mcpRequest(t, client, server.URL+ardMCPPath, "reader/catalog.read", version, "tools/call", "search", mcpWire(t, version, "tools/call", "search", query), nil)
			var page struct {
				Result struct {
					Structured publication.SearchResponse `json:"structuredContent"`
					IsError    bool                       `json:"isError"`
				} `json:"result"`
			}
			if json.Unmarshal(body, &page) != nil || status != 200 || page.Result.IsError || len(page.Result.Structured.Results) != 1 || page.Result.Structured.PageToken == "" {
				t.Fatal("read-scope positive control", status, string(body))
			}
			// The active generation changes without altering the client's query.
			next := snapshot
			next.ID = projectionNext
			next.Generation++
			next = sealPublicFixtureSnapshot(t, next)
			active.Store(&next)
			query["pageToken"] = page.Result.Structured.PageToken
			status, _, body = mcpRequest(t, client, server.URL+ardMCPPath, "reader/catalog.read", version, "tools/call", "search", mcpWire(t, version, "tools/call", "search", query), nil)
			if status != 200 || !bytes.Contains(body, []byte(`"isError":true`)) {
				t.Fatal("old generation cursor remained usable", status, string(body))
			}
			delete(query, "pageToken")
			// Absence/withdrawal is not an empty successful catalog or Control fallback.
			active.Store(nil)
			status, _, body = mcpRequest(t, client, server.URL+ardMCPPath, "reader/catalog.read", version, "tools/call", "search", mcpWire(t, version, "tools/call", "search", query), nil)
			if status != 200 || !bytes.Contains(body, []byte("publication_unavailable")) || bytes.Contains(body, []byte("private-fixture")) {
				t.Fatal(status, string(body))
			}
			owner, err := authenticate(t.Context(), "owner")
			if err != nil {
				t.Fatal(err)
			}
			ctx := identity.WithActor(t.Context(), owner)
			reader, err := authenticate(t.Context(), "reader")
			if err != nil {
				t.Fatal(err)
			}
			grants, err := authority.ListAuthorizationAssignments(ctx, identity.AuthorizationPageRequest{MembershipID: reader.MembershipID}, 100, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(grants.Items) == 0 || grants.NextCursor != "" {
				t.Fatal("missing bounded retained grants")
			}
			for _, grant := range grants.Items {
				if _, err := authority.RevokeAuthorizationAssignment(ctx, grant.ID, grant.Revision, "Remove current discovery authority", "ard-mcp-revoke-"+grant.ID); err != nil {
					t.Fatal(err)
				}
			}
			before := reads.Load()
			status, _, _ = mcpRequest(t, client, server.URL+ardMCPPath, "reader/catalog.read", version, "tools/list", "", mcpWire(t, version, "tools/list", "", nil), nil)
			if status != 403 || reads.Load() != before {
				t.Fatal("initialized client retained revoked discovery authority", status, reads.Load())
			}
		})
	}
}

func (t ardMCPHostTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Host = t.host
	return t.base.RoundTrip(r)
}

func TestARDMCPHTTPBothProtocolsBehindExactHostProxy(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			snapshot, _ := publicFixtureSnapshot(t, DiscoveryAnonymous)
			var reads atomic.Int64
			projection := publicProjectionServer(t, DiscoveryAnonymous, func(context.Context, string) (publication.Snapshot, error) { reads.Add(1); return snapshot, nil })
			binding := projection.discovery[publicFixtureHost]
			binding.MCPEnabled = true
			server := newConfiguredHTTPServer(t, func(d *Dependencies) {
				d.MCP = config.Defaults().MCP
				d.PublicBaseURL = publicFixtureOrigin
				d.DiscoveryHosts = []DiscoveryHost{binding}
			})
			defer server.Close()
			client := server.Client()
			baseTransport := client.Transport
			client.Transport = ardMCPHostTransport{baseTransport, publicFixtureHost}
			method := "server/discover"
			if version == "2025-11-25" {
				method = "initialize"
			}
			status, _, body := mcpRequest(t, client, server.URL+ardMCPPath, "", version, method, "", mcpWire(t, version, method, "", nil), nil)
			if status != 200 || !bytes.Contains(body, []byte(ardMCPName)) || reads.Load() != 0 {
				t.Fatal(status, string(body), reads.Load())
			}
			query := map[string]any{"query": map[string]any{"text": "fixture"}, "federation": "none", "pageSize": 2}
			status, _, body = mcpRequest(t, client, server.URL+ardMCPPath, "ignored-public-token", version, "tools/call", "search", mcpWire(t, version, "tools/call", "search", query), nil)
			var reply struct {
				Result struct {
					Structured publication.SearchResponse `json:"structuredContent"`
					IsError    bool                       `json:"isError"`
				} `json:"result"`
			}
			if json.Unmarshal(body, &reply) != nil || status != 200 || reply.Result.IsError || len(reply.Result.Structured.Results) != 2 {
				t.Fatal(status, string(body))
			}
			before := reads.Load()
			for _, header := range []http.Header{{"Origin": []string{"https://evil.example"}}, {"X-Forwarded-Host": []string{publicFixtureHost}}} {
				status, _, _ = mcpRequest(t, client, server.URL+ardMCPPath, "", version, "tools/list", "", mcpWire(t, version, "tools/list", "", nil), header)
				if status != 403 && status != 404 {
					t.Fatal("confused origin/authority", status)
				}
			}
			client.Transport = ardMCPHostTransport{baseTransport, "unknown.example.com"}
			status, _, _ = mcpRequest(t, client, server.URL+ardMCPPath, "", version, "tools/list", "", mcpWire(t, version, "tools/list", "", nil), nil)
			if status != 404 || reads.Load() != before {
				t.Fatal("unknown host reached publication", status, reads.Load())
			}
		})
	}
}

func TestARDMCPHTTPDeadlinesReachAuthenticationAndSearch(t *testing.T) {
	// #nosec G101 -- synthetic bearer selects the owned blocked authenticator.
	const blockedToken = "mcp-deadline-blocked-fixture"
	// #nosec G101 -- synthetic bearer maps only to the local fixture Reader.
	const readerToken = "mcp-deadline-reader-fixture"
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		for _, stage := range []string{"authentication", "local", "live"} {
			t.Run(version+"/"+stage, func(t *testing.T) {
				access := DiscoveryAnonymous
				if stage == "authentication" {
					access = DiscoveryAuthenticated
				}
				snapshot, _ := publicFixtureSnapshot(t, access)
				var reads atomic.Int64
				ended := make(chan struct{}, 1)
				block := func(ctx context.Context) {
					if requestID(ctx) == "" {
						t.Error("callback lost request correlation")
					}
					if _, ok := ctx.Deadline(); !ok {
						t.Error("callback lost deadline")
					}
					<-ctx.Done()
					ended <- struct{}{}
				}
				projection := publicProjectionServer(t, access, func(ctx context.Context, _ string) (publication.Snapshot, error) {
					reads.Add(1)
					if stage == "local" {
						block(ctx)
						return publication.Snapshot{}, ctx.Err()
					}
					return snapshot, nil
				})
				binding := projection.discovery[publicFixtureHost]
				binding.MCPEnabled = true
				manager, err := federation.NewManager(publicTopologyStore{selectSources: func(ctx context.Context, _, _ string, _ federation.DiscoveryAudience, _ string, _ int) ([]federation.Source, bool, error) {
					block(ctx)
					return nil, false, ctx.Err()
				}}, publicPolicyFetcher{}, clock.NewFake(projectionTime), id.Generator{})
				if err != nil {
					t.Fatal(err)
				}
				server := newConfiguredHTTPServer(t, func(d *Dependencies) {
					d.MCP = config.Defaults().MCP
					d.MCP.MaxConcurrency = 1
					d.PublicBaseURL = publicFixtureOrigin
					d.DiscoveryHosts = []DiscoveryHost{binding}
					d.RequestTimeout = 100 * time.Millisecond
					if stage == "live" {
						d.Federation = manager
					}
					if stage == "authentication" {
						original := d.Authenticator
						d.Authenticator = mcpAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
							if token == blockedToken {
								block(ctx)
								return identity.Actor{}, ctx.Err()
							}
							if token == readerToken {
								token = "reader"
							}
							return original.Authenticate(ctx, token)
						})
					}
				})
				defer server.Close()
				client := server.Client()
				client.Transport = ardMCPHostTransport{http.DefaultTransport, publicFixtureHost}
				mode := "none"
				if stage == "live" {
					mode = "auto"
				}
				query := map[string]any{"query": map[string]any{"text": "fixture"}, "federation": mode}
				token, recoveryToken := "", ""
				if stage == "authentication" {
					token, recoveryToken = blockedToken, readerToken
					status, _, body := mcpRequest(t, client, server.URL+ardMCPPath, recoveryToken, version, "tools/call", "search", mcpWire(t, version, "tools/call", "search", query), nil)
					if status != http.StatusOK || !bytes.Contains(body, []byte(`"structuredContent"`)) || !bytes.Contains(body, []byte("urn:air:"+publicFixtureHost+":default:alpha")) || reads.Load() != 1 {
						t.Fatal("authenticated Search positive control", status, string(body), reads.Load())
					}
				}
				status, headers, body := mcpRequest(t, client, server.URL+ardMCPPath, token, version, "tools/call", "search", mcpWire(t, version, "tools/call", "search", query), nil)
				if status != 503 || !strings.Contains(string(body), "deadline") || bytes.Contains(body, []byte("urn:air:")) {
					t.Fatal(status, string(body))
				}
				if stage == "authentication" && (reads.Load() != 1 || headers.Get("WWW-Authenticate") != "") {
					t.Fatal("expired authentication read publication or invalidated credentials", reads.Load(), headers)
				}
				select {
				case <-ended:
				case <-time.After(5 * time.Second):
					t.Fatal("callback outlived the HTTP deadline")
				}
				// A protocol call still works after the bounded read releases its slot.
				status, _, _ = mcpRequest(t, client, server.URL+ardMCPPath, recoveryToken, version, "tools/list", "", mcpWire(t, version, "tools/list", "", nil), nil)
				if status != 200 {
					t.Fatal("slot did not recover", status)
				}
			})
		}
	}
}

func TestARDMCPHTTPSharedConcurrencyAndCancellation(t *testing.T) {
	// #nosec G101 -- dedicated synthetic fixture bearer, not a real credential.
	const controlToken = "mcp-concurrency-fixture-token-4c71"
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			entered, ended := make(chan struct{}), make(chan struct{})
			projection := publicProjectionServer(t, DiscoveryAnonymous, func(ctx context.Context, _ string) (publication.Snapshot, error) {
				close(entered)
				<-ctx.Done()
				close(ended)
				return publication.Snapshot{}, ctx.Err()
			})
			binding := projection.discovery[publicFixtureHost]
			binding.MCPEnabled = true
			other := binding
			other.Host = "other.example.com"
			server := newConfiguredHTTPServer(t, func(d *Dependencies) {
				d.MCP = config.Defaults().MCP
				d.MCP.Enabled = true
				d.MCP.MaxConcurrency = 1
				d.PublicBaseURL = publicFixtureOrigin
				d.DiscoveryHosts = []DiscoveryHost{binding, other}
				d.RequestTimeout = 10 * time.Second
				original := d.Authenticator
				d.Authenticator = mcpAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
					if token == controlToken {
						token = "reader"
					}
					return original.Authenticate(ctx, token)
				})
			})
			defer server.Close()
			transport := server.Client().Transport
			client := &http.Client{Transport: ardMCPHostTransport{transport, publicFixtureHost}, Timeout: 5 * time.Second}
			contenders := []struct {
				client      *http.Client
				path, token string
			}{
				{client, ardMCPPath, ""},
				{&http.Client{Transport: ardMCPHostTransport{transport, other.Host}, Timeout: 5 * time.Second}, ardMCPPath, ""},
				{&http.Client{Transport: transport, Timeout: 5 * time.Second}, "/mcp", controlToken},
			}
			for _, contender := range contenders {
				status, _, body := mcpRequest(t, contender.client, server.URL+contender.path, contender.token, version, "tools/list", "", mcpWire(t, version, "tools/list", "", nil), nil)
				if status != http.StatusOK || !bytes.Contains(body, []byte(`"tools"`)) {
					t.Fatal("idle endpoint positive control", contender.path, status, string(body))
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+ardMCPPath, bytes.NewReader(mcpWire(t, version, "tools/call", "search", map[string]any{"query": map[string]any{"text": "fixture"}, "federation": "none"})))
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Accept", "application/json, text/event-stream")
			r.Header.Set("Mcp-Protocol-Version", version)
			if version == "2026-07-28" {
				r.Header.Set("Mcp-Method", "tools/call")
				r.Header.Set("Mcp-Name", "search")
			}
			finished := make(chan error, 1)
			go func() {
				response, err := client.Do(r)
				if response != nil {
					_ = response.Body.Close()
				}
				finished <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("callback not reached")
			}
			// One occupied ARD request exhausts the deployment-wide budget,
			// including another publication host and the independent Control plane.
			for _, contender := range contenders {
				status, headers, body := mcpRequest(t, contender.client, server.URL+contender.path, contender.token, version, "tools/list", "", mcpWire(t, version, "tools/list", "", nil), nil)
				var refused struct {
					Error struct {
						Code int `json:"code"`
					} `json:"error"`
				}
				if status != http.StatusServiceUnavailable || headers.Get("Retry-After") != "1" || json.Unmarshal(body, &refused) != nil || refused.Error.Code != -32603 || bytes.Contains(body, []byte(`"tools"`)) {
					t.Fatal("shared limit did not refuse protocol work", contender.path, status, string(body))
				}
			}
			cancel()
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("cancelled request unexpectedly succeeded")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("client did not cancel")
			}
			select {
			case <-ended:
			case <-time.After(5 * time.Second):
				t.Fatal("callback ignored client cancellation")
			}
			for _, contender := range contenders {
				deadline := time.Now().Add(5 * time.Second)
				for {
					status, _, body := mcpRequest(t, contender.client, server.URL+contender.path, contender.token, version, "tools/list", "", mcpWire(t, version, "tools/list", "", nil), nil)
					if status == http.StatusOK {
						break
					}
					if status != http.StatusServiceUnavailable || !time.Now().Before(deadline) {
						t.Fatal("cancelled read retained the shared slot", contender.path, status, string(body))
					}
				}
			}
		})
	}
}
