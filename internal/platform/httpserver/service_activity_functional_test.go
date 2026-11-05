//go:build functional

package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
)

// This wrapper supplies controlled OAuth claims after real memory membership
// resolution. It does not replace current Catalog permission evaluation.
type activityScopeAuthenticator struct{ authn.Authenticator }

func (a activityScopeAuthenticator) Authenticate(ctx context.Context, token string) (identity.Actor, error) {
	unscoped := token == "admin-unscoped" // #nosec G101 -- synthetic owned fixture label, never a production credential.
	if unscoped {
		token = "admin"
	}
	actor, err := a.Authenticator.Authenticate(ctx, token)
	if err != nil {
		return actor, err
	}
	actor.Claims = maps.Clone(actor.Claims)
	if actor.Claims == nil {
		actor.Claims = map[string]string{}
	}
	if !unscoped {
		actor.Claims["scope:tenant.admin"] = "true"
		actor.Claims["scope:catalog.read"] = "true"
	}
	return actor, nil
}

func TestServiceActivityRealHTTPGateAndCounting(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			server := newConfiguredHTTPServer(t, func(d *Dependencies) {
				d.EnforceOAuthScopes = true
				d.Authenticator = activityScopeAuthenticator{d.Authenticator}
				d.Capabilities.ServiceActivity = CapabilityDisabled
				if enabled {
					d.Capabilities.ServiceActivity = CapabilityEnabled
				}
			})
			defer server.Close()
			read := func(method, path, token string, want int, requestBody ...string) ([]byte, http.Header) {
				t.Helper()
				var input io.Reader
				if len(requestBody) != 0 {
					input = strings.NewReader(requestBody[0])
				}
				req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, input)
				if err != nil {
					t.Fatal(err)
				}
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				if method == http.MethodPost {
					req.Header.Set("Idempotency-Key", "activity-method-rejection")
				}
				resp, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := resp.Body.Close(); err != nil {
						t.Error(err)
					}
				}()
				body, err := io.ReadAll(io.LimitReader(resp.Body, maxServiceActivityBytes+1))
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != want {
					t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, body)
				}
				if resp.Header.Get("Cache-Control") != "no-store" {
					t.Fatal("response must not be cached, including refusals")
				}
				if want != 200 && strings.Contains(string(body), `"families"`) {
					t.Fatal("refusal disclosed activity counters")
				}
				if len(body) > maxServiceActivityBytes {
					t.Fatal("response exceeded bound")
				}
				return body, resp.Header
			}
			capRaw, _ := read("GET", "/api/v1/capabilities", "admin", 200)
			requireControlSchema(t, "Capabilities", capRaw)
			var caps capabilitiesResponse
			if err := json.Unmarshal(capRaw, &caps); err != nil {
				t.Fatal(err)
			}
			wantCap := CapabilityDisabled
			if enabled {
				wantCap = CapabilityEnabled
			}
			if caps.OptionalCapabilities.ServiceActivity != wantCap {
				t.Fatal("gate hint disagrees")
			}
			if !enabled {
				read("GET", "/api/v1/service-activity", "admin", 404)
				read("HEAD", "/api/v1/service-activity", "admin", 404)
				return
			}
			read("GET", "/api/v1/service-activity", "", 401)
			read("GET", "/api/v1/service-activity", "reader", 403)
			read("HEAD", "/api/v1/service-activity", "reader", 403)
			read("POST", "/api/v1/service-activity", "admin", 405)
			read("GET", "/api/v1/service-activity", "admin", 400, "{}")
			// Deployment enablement explicitly shares aggregates with administrators in each tenant.
			read("GET", "/api/v1/service-activity", "other-admin", 200)
			read("GET", "/api/v1/service-activity", "admin-unscoped", 403)
			read("HEAD", "/api/v1/service-activity", "admin-unscoped", 403)
			read("GET", "/api/v1/service-activity?tenantId=other", "admin", 400)
			first, headers := read("GET", "/api/v1/service-activity", "admin", 200)
			if headers.Get("Cache-Control") != "no-store" {
				t.Fatal("missing no-store")
			}
			requireControlSchema(t, "ServiceActivity", first)
			var before, after serviceActivityResponse
			if err := json.Unmarshal(first, &before); err != nil {
				t.Fatal(err)
			}
			read("GET", "/api/v1/namespaces", "admin", 200)
			second, _ := read("GET", "/api/v1/service-activity", "admin", 200)
			requireControlSchema(t, "ServiceActivity", second)
			if err := json.Unmarshal(second, &after); err != nil {
				t.Fatal(err)
			}
			if after.InstanceID != before.InstanceID || after.HTTP.Completed != before.HTTP.Completed+2 {
				t.Fatal("unexpected completed population")
			}
			for i := range after.HTTP.Families {
				delta := uint64(0)
				if after.HTTP.Families[i].Family == "observer" || after.HTTP.Families[i].Family == "control_read" {
					delta = 1
				}
				if after.HTTP.Families[i].Completed != before.HTTP.Families[i].Completed+delta {
					t.Fatalf("wrong delta for %s", after.HTTP.Families[i].Family)
				}
			}
			body, head := read("HEAD", "/api/v1/service-activity", "admin", 200)
			if len(body) != 0 || head.Get("Cache-Control") != "no-store" {
				t.Fatal("HEAD body/cache mismatch")
			}
		})
	}
}
