package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	serviceapi "github.com/open-agentic-catalog/open-agentic-catalog/api"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

func TestPublicationHostsInventory(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromData(serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	expected := publicationHostInventory{Items: []publicationHost{{"a.example", DiscoveryAnonymous}, {"b.example", DiscoveryDisabled}, {"c.example", DiscoveryAuthenticated}}}
	for _, test := range []struct {
		name                    string
		role                    identity.Role
		tenant                  string
		namespace, scope, token bool
		status                  int
	}{
		{"publisher", identity.RolePublisher, projectionTenant, false, true, true, 200},
		{"administrator", identity.RoleTenantAdmin, projectionTenant, false, true, true, 200},
		{"other tenant", identity.RolePublisher, "00000000-0000-7000-8000-000000000099", false, true, true, 200},
		{"namespace publisher", identity.RolePublisher, projectionTenant, true, true, true, 403},
		{"reader", identity.RoleReader, projectionTenant, false, true, true, 403},
		{"missing scope", identity.RolePublisher, projectionTenant, false, false, true, 403},
		{"missing token", identity.RolePublisher, projectionTenant, false, true, false, 401},
	} {
		t.Run(test.name, func(t *testing.T) {
			actor := projectionActor(test.role)
			actor.TenantID = test.tenant
			actor.Claims = map[string]string{}
			if test.scope {
				actor.Claims["scope:catalog.read"] = "true"
			}
			if test.namespace {
				actor.Roles = nil
				actor.Namespaces = map[string][]identity.Role{"00000000-0000-7000-8000-000000000097": {identity.RolePublisher}}
			}
			d := inertServerDependencies()
			d.EnforceOAuthScopes = true
			d.Authenticator = discoveryAdmissionAuthenticator(func(ctx context.Context, _ string) (identity.Actor, error) {
				resolved, _ := identity.FromContext(identityfixture.WithActor(ctx, actor))
				return resolved, nil
			})
			for _, item := range []publicationHost{{"c.example", DiscoveryAuthenticated}, {"b.example", DiscoveryDisabled}, {"a.example", DiscoveryAnonymous}} {
				d.DiscoveryHosts = append(d.DiscoveryHosts, DiscoveryHost{Host: item.Host, TenantID: projectionTenant, Mode: item.Mode, PublisherDomain: "private-fixture-domain", Publication: &publication.Manager{}})
			}
			d.DiscoveryHosts = append(d.DiscoveryHosts, DiscoveryHost{Host: "private-fixture.example", TenantID: "00000000-0000-7000-8000-000000000098", Mode: DiscoveryDisabled, PublisherDomain: "private-fixture-other"})
			s, err := New(d)
			if err != nil {
				t.Fatal(err)
			}
			for _, query := range []string{"", "?limit=1"} {
				r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/publication/hosts"+query, nil)
				if test.token {
					r.Header.Set("Authorization", "Bearer synthetic")
				}
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				status := test.status
				if query != "" && test.token && test.scope {
					status = 400
				}
				if w.Code != status || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Request-ID") == "" {
					t.Fatalf("response=%d headers=%v body=%s", w.Code, w.Header(), w.Body)
				}
				if status != 200 {
					var problem Problem
					if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
						t.Fatal(err)
					}
					code := catalog.CodeForbidden
					if status == 401 {
						code = catalog.CodeUnauthenticated
					}
					if status == 400 {
						code = catalog.CodeInvalidArgument
					}
					if problem.Code != code || problem.RequestID != w.Header().Get("X-Request-ID") {
						t.Fatalf("problem=%+v", problem)
					}
					continue
				}
				want := expected
				if test.name == "other tenant" {
					want = publicationHostInventory{Items: []publicationHost{}}
				}
				bytes, err := json.Marshal(want)
				if err != nil {
					t.Fatal(err)
				}
				var got, value any
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(bytes, &value); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, value) {
					t.Fatalf("unexpected inventory=%s", w.Body)
				}
				if err := document.Components.Schemas["PublicationHostInventory"].Value.VisitJSON(got, openapi3.EnableJSONSchema2020()); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPublicationHostBound(t *testing.T) {
	t.Parallel()
	d := inertServerDependencies()
	actor := projectionActor(identity.RolePublisher)
	d.Authenticator = discoveryAdmissionAuthenticator(func(ctx context.Context, _ string) (identity.Actor, error) {
		resolved, _ := identity.FromContext(identityfixture.WithActor(ctx, actor))
		return resolved, nil
	})
	for index := 0; index < 64; index++ {
		d.DiscoveryHosts = append(d.DiscoveryHosts, DiscoveryHost{Host: fmt.Sprintf("host%02d.example", index), TenantID: projectionTenant, Mode: DiscoveryDisabled, PublisherDomain: "example"})
	}
	s, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/publication/hosts", nil)
	r.Header.Set("Authorization", "Bearer synthetic")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var got publicationHostInventory
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(got.Items) != 64 || got.Items[0].Host != "host00.example" || got.Items[63].Host != "host63.example" {
		t.Fatalf("boundary=%d %s", w.Code, w.Body)
	}
	d.DiscoveryHosts = append(d.DiscoveryHosts, DiscoveryHost{Host: "overflow.example", TenantID: projectionTenant, Mode: DiscoveryDisabled, PublisherDomain: "example"})
	if s, err := New(d); err == nil || s != nil {
		t.Fatal("over-limit discovery bindings accepted")
	}
}
