//go:build functional

package httpserver

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/tenantsettings"
)

type settingsScopeAuth struct {
	authn.Authenticator
	allowed bool
}

func (a settingsScopeAuth) Authenticate(ctx context.Context, token string) (identity.Actor, error) {
	actor, err := a.Authenticator.Authenticate(ctx, token)
	if err == nil && a.allowed {
		if actor.Claims == nil {
			actor.Claims = map[string]string{}
		}
		actor.Claims["scope:tenant.admin"] = "true"
	}
	return actor, err
}
func TestTenantSettingsHTTPContractsReplayAndIsolation(t *testing.T) {
	repository := catalog.NewMemoryRepository()
	store := tenantsettings.NewMemoryStore(repository)
	settings, err := tenantsettings.New(store, 15*time.Minute, 5, clock.NewFake(time.Date(2026, 10, 13, 14, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	server := newConfiguredHTTPServer(t, func(d *Dependencies) { d.TenantSettings = settings })
	defer server.Close()
	const path = "/api/v1/tenant-settings"
	read := requestJSON(t, server.URL, http.MethodGet, path, "admin", "", "", nil)
	requireStatus(t, read, 200)
	requireControlSchema(t, "TenantSettings", read.body)
	var initial tenantsettings.View
	decodeResult(t, read, &initial)
	if initial.Revision != 0 || read.header.Get("ETag") == "" || read.header.Get("Cache-Control") != "no-store" {
		t.Fatal(initial, read.header)
	}
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodGet, path, "reader", "", "", nil), 403, catalog.CodeForbidden)
	input := map[string]any{"overrides": map[string]any{"automaticSourceSyncEnabled": false, "automaticSourceSyncInterval": "90.5s"}, "reason": "Pause tenant automatic work"}
	for _, tag := range []string{"", `"rev-0"`, "*", read.header.Get("ETag") + ", " + read.header.Get("ETag")} {
		requireControlProblem(t, requestJSON(t, server.URL, http.MethodPost, path, "admin", "bad-tag-"+tag, tag, input), 412, catalog.CodePreconditionFailed)
	}
	saved := requestJSON(t, server.URL, http.MethodPost, path, "admin", "save-settings", read.header.Get("ETag"), input)
	requireStatus(t, saved, 200)
	requireControlSchema(t, "TenantSettings", saved.body)
	var policy tenantsettings.View
	decodeResult(t, saved, &policy)
	if policy.Revision != 1 || policy.Effective.AutomaticSourceSyncEnabled || time.Duration(policy.Effective.AutomaticSourceSyncInterval) != 90500*time.Millisecond {
		t.Fatal(policy)
	}
	replay := requestJSON(t, server.URL, http.MethodPost, path, "admin", "save-settings", read.header.Get("ETag"), input)
	requireStatus(t, replay, 200)
	if replay.header.Get("Idempotency-Replayed") != "true" || !bytes.Equal(saved.body, replay.body) {
		t.Fatal("changed command receipt")
	}
	other := requestJSON(t, server.URL, http.MethodGet, path, "other-admin", "", "", nil)
	requireStatus(t, other, 200)
	var otherPolicy tenantsettings.View
	decodeResult(t, other, &otherPolicy)
	if otherPolicy.Revision != 0 || !otherPolicy.Effective.AutomaticSourceSyncEnabled || other.header.Get("ETag") == read.header.Get("ETag") {
		t.Fatal("cross-tenant policy leak")
	}
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodPost, path, "other-admin", "foreign-tag", saved.header.Get("ETag"), input), 412, catalog.CodePreconditionFailed)
	resetInput := map[string]any{"overrides": map[string]any{}, "reason": "Restore inheritance"}
	reset := requestJSON(t, server.URL, http.MethodPost, path, "admin", "reset-settings", saved.header.Get("ETag"), resetInput)
	requireStatus(t, reset, 200)
	var resetPolicy tenantsettings.View
	decodeResult(t, reset, &resetPolicy)
	if resetPolicy.Revision != 2 || !resetPolicy.Effective.AutomaticSourceSyncEnabled || resetPolicy.Overrides.AutomaticSourceSyncEnabled != nil {
		t.Fatal(resetPolicy)
	}
	historical := requestJSON(t, server.URL, http.MethodPost, path, "admin", "save-settings", read.header.Get("ETag"), input)
	requireStatus(t, historical, 200)
	if !bytes.Equal(historical.body, saved.body) {
		t.Fatal("replay changed after reset")
	}
	current := requestJSON(t, server.URL, http.MethodGet, path, "admin", "", "", nil)
	if !bytes.Equal(current.body, reset.body) {
		t.Fatal("historical replay rewrote current policy")
	}
	for index, overrides := range []any{nil, map[string]any{"automaticSourceSyncEnabled": nil}, map[string]any{"AutomaticSourceSyncEnabled": true}, map[string]any{"automaticSourceSyncInterval": "59s"}, map[string]any{"automaticSourceSyncInterval": nil}, map[string]any{"newJobMaxAttempts": 21}} {
		requireControlProblem(t, requestJSON(t, server.URL, http.MethodPost, path, "admin", string(rune('a'+index)), reset.header.Get("ETag"), map[string]any{"overrides": overrides, "reason": "Invalid field"}), 400, catalog.CodeInvalidArgument)
	}
	// Go accepts both Unicode microsecond unit symbols. The schema's length
	// bound counts characters, independently of transport bytes.
	unicodeInput := map[string]any{"overrides": map[string]any{"automaticSourceSyncInterval": "1m0s" + strings.Repeat("0\u00b5s", 20)}, "reason": "Use a valid 64-character duration"}
	unicodeSaved := requestJSON(t, server.URL, http.MethodPost, path, "admin", "unicode-settings", reset.header.Get("ETag"), unicodeInput)
	requireStatus(t, unicodeSaved, 200)
	requireControlSchema(t, "TenantSettings", unicodeSaved.body)
	var unicodePolicy tenantsettings.View
	decodeResult(t, unicodeSaved, &unicodePolicy)
	if unicodePolicy.Revision != 3 || unicodePolicy.Effective.AutomaticSourceSyncInterval != tenantsettings.Duration(time.Minute) {
		t.Fatal("valid Unicode duration changed its value", unicodePolicy)
	}
	unicodeInput["overrides"] = map[string]any{"automaticSourceSyncInterval": "1m" + strings.Repeat("0\u00b5s", 21)}
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodPost, path, "admin", "unicode-settings-too-long", unicodeSaved.header.Get("ETag"), unicodeInput), 400, catalog.CodeInvalidArgument)
	unchanged := requestJSON(t, server.URL, http.MethodGet, path, "admin", "", "", nil)
	if !bytes.Equal(unchanged.body, unicodeSaved.body) || unchanged.header.Get("ETag") != unicodeSaved.header.Get("ETag") {
		t.Fatal("overlong Unicode duration changed retained policy")
	}
}
func TestTenantSettingsOAuthCeiling(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		server := newConfiguredHTTPServer(t, func(d *Dependencies) {
			d.TenantSettings, _ = tenantsettings.New(tenantsettings.NewMemoryStore(catalog.NewMemoryRepository()), time.Minute, 5, clock.NewFake(time.Now().UTC()), id.Generator{})
			d.EnforceOAuthScopes = true
			d.Authenticator = settingsScopeAuth{d.Authenticator, allowed}
		})
		response := requestJSON(t, server.URL, http.MethodGet, "/api/v1/tenant-settings", "admin", "", "", nil)
		expected := 403
		if allowed {
			expected = 200
		}
		requireStatus(t, response, expected)
		server.Close()
	}
}
