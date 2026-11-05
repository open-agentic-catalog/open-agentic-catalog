//go:build functional

package web

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	platformruntime "github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/runtime"
)

func TestLoginAgainstCatalogAndCurrentMembership(t *testing.T) {
	runtime, err := platformruntime.Build(t.Context(), platformruntime.BuildOptions{ConfigPath: "../../../config/local.json", Role: config.RoleAll, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	api := httptest.NewServer(runtime.HTTP.Handler())
	defer api.Close()
	c := fixtureConfig(t, api.URL)
	if err := os.WriteFile(c.DevelopmentUsersFile, []byte(`[{"alias":"reader","label":"Reader","token":"local-reader"},{"alias":"admin","label":"Administrator","token":"local-admin"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	s := newFixture(t, c)
	cookie := loginFixture(t, s)
	w := uiRequest(s, "GET", "/auth/session", "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"subject":"local-reader"`) {
		t.Fatalf("self-context: %d %s", w.Code, w.Body.String())
	}
	if w := uiRequest(s, "POST", "/_catalog/api/v1/catalog/search", `{"text":"","limit":1}`, cookie); w.Code != 200 {
		t.Fatalf("search: %d %s", w.Code, w.Body.String())
	}
	if w := uiRequest(s, "GET", "/_catalog/api/v1/memberships", "", cookie); w.Code != 403 {
		t.Fatalf("reader admin access: %d", w.Code)
	}
	actor, err := runtime.Authenticator.Authenticate(t.Context(), "local-admin")
	if err != nil {
		t.Fatal(err)
	}
	var current struct {
		Access struct {
			MembershipID string `json:"membershipId"`
		} `json:"access"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	members, err := runtime.Identity.ListMemberships(identity.WithActor(t.Context(), actor), 100, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range members.Items {
		if member.ID == current.Access.MembershipID {
			_, err = runtime.Identity.SuspendMembership(identity.WithActor(t.Context(), actor), member.ID, "Test current authorization", "ui-membership-test", member.Revision)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if w := uiRequest(s, "POST", "/_catalog/api/v1/catalog/search", `{"text":""}`, cookie); w.Code != 401 {
		t.Fatalf("suspended membership retained session authority: %d", w.Code)
	}
}
