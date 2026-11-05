package httpserver

import (
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type controlPageRepository struct {
	catalog.Repository
	namespaces  func(context.Context, string, catalog.NamespaceListRequest) ([]catalog.Namespace, error)
	publishers  func(context.Context, string, catalog.PublisherListRequest) ([]catalog.Publisher, error)
	resources   func(context.Context, string, catalog.ResourceListRequest) ([]catalog.Resource, error)
	events      func(context.Context, string, catalog.AuditEventListRequest) ([]catalog.AuditEvent, error)
	appendAudit func(context.Context, audit.Record) error
}

func (r controlPageRepository) ListNamespaces(c context.Context, t string, p catalog.NamespaceListRequest) ([]catalog.Namespace, error) {
	return r.namespaces(c, t, p)
}
func (r controlPageRepository) ListPublisherPage(c context.Context, t string, p catalog.PublisherListRequest) ([]catalog.Publisher, error) {
	return r.publishers(c, t, p)
}
func (r controlPageRepository) ListResources(c context.Context, t string, p catalog.ResourceListRequest) ([]catalog.Resource, error) {
	return r.resources(c, t, p)
}
func (r controlPageRepository) AuditEvents(c context.Context, t string, p catalog.AuditEventListRequest) ([]catalog.AuditEvent, error) {
	return r.events(c, t, p)
}
func (r controlPageRepository) AppendAuditEvent(c context.Context, r2 audit.Record) error {
	return r.appendAudit(c, r2)
}

func TestControlPageProjection(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"namespaces", "publishers", "resources", "audit-events"} {
		for _, outcome := range []string{"pages", "empty", "unavailable", "audit-unavailable"} {
			if outcome == "audit-unavailable" && kind != "audit-events" {
				continue
			}
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				role := identity.RoleReader
				if kind == "audit-events" {
					role = identity.RoleSecurityReviewer
				}
				actor := projectionActor(role)
				ctx := identityfixture.WithActor(t.Context(), actor)
				reads, audits := 0, 0
				check := func(c context.Context, tenant string, got, want any) error {
					reads++
					if !sameProjectionContext(c, ctx) || tenant != projectionTenant || !reflect.DeepEqual(got, want) {
						t.Fatalf("page binding: tenant=%s got=%+v want=%+v", tenant, got, want)
					}
					if outcome == "unavailable" {
						return catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-page")
					}
					return nil
				}
				repo := controlPageRepository{}
				fixture := controlReadFixture(t)
				query := url.Values{"limit": {"1"}}
				var first, second any
				switch kind {
				case "namespaces":
					a := fixture.namespace
					b := a
					b.ID = projectionNext
					b.Slug = "next"
					first, second = a, b
					repo.namespaces = func(c context.Context, tenant string, p catalog.NamespaceListRequest) ([]catalog.Namespace, error) {
						want := catalog.NamespaceListRequest{Limit: 2}
						if reads > 0 {
							want.AfterSlug = a.Slug
							want.AfterID = a.ID
						}
						if err := check(c, tenant, p, want); err != nil {
							return nil, err
						}
						if outcome == "empty" {
							return []catalog.Namespace{}, nil
						}
						if reads == 1 {
							return []catalog.Namespace{a, b}, nil
						}
						return []catalog.Namespace{b}, nil
					}
				case "publishers":
					a := fixture.publisher
					b := a
					b.ID = projectionNext
					b.DisplayName = "Z publisher"
					first, second = a, b
					repo.publishers = func(c context.Context, tenant string, p catalog.PublisherListRequest) ([]catalog.Publisher, error) {
						want := catalog.PublisherListRequest{Limit: 2}
						if reads > 0 {
							want.AfterDisplayName = a.DisplayName
							want.AfterID = a.ID
						}
						if err := check(c, tenant, p, want); err != nil {
							return nil, err
						}
						if outcome == "empty" {
							return []catalog.Publisher{}, nil
						}
						if reads == 1 {
							return []catalog.Publisher{a, b}, nil
						}
						return []catalog.Publisher{b}, nil
					}
				case "resources":
					query.Set("kind", "agent")
					a := fixture.record.Resource
					b := a
					b.ID = projectionNext
					b.UpdatedAt = a.UpdatedAt.Add(-time.Minute)
					first, second = a, b
					repo.resources = func(c context.Context, tenant string, p catalog.ResourceListRequest) ([]catalog.Resource, error) {
						want := catalog.ResourceListRequest{Kind: catalog.KindAgent, Limit: 2}
						if reads > 0 {
							want.AfterUpdatedAt = &a.UpdatedAt
							want.AfterID = a.ID
						}
						if err := check(c, tenant, p, want); err != nil {
							return nil, err
						}
						if outcome == "empty" {
							return []catalog.Resource{}, nil
						}
						if reads == 1 {
							return []catalog.Resource{a, b}, nil
						}
						return []catalog.Resource{b}, nil
					}
				case "audit-events":
					query.Set("action", "namespace.created")
					a, err := audit.NewRecord(projectionItem, projectionTenant, audit.Principal{ID: actor.MembershipID, Type: audit.PrincipalCatalogMembership, ActorType: "human", RealmID: actor.RealmID}, "namespace.created", "namespace", projectionResource, "", audit.Execution{}, nil, projectionTime)
					if err != nil {
						t.Fatal(err)
					}
					b := a
					b.ID = projectionNext
					b.At = a.At.Add(-time.Minute)
					first, second = a, b
					repo.events = func(c context.Context, tenant string, p catalog.AuditEventListRequest) ([]catalog.AuditEvent, error) {
						want := catalog.AuditEventListRequest{Filters: audit.Query{Action: "namespace.created"}, Limit: 2}
						if reads > 0 {
							want.AfterOccurredAt = &a.At
							want.AfterID = a.ID
						}
						if err := check(c, tenant, p, want); err != nil {
							return nil, err
						}
						if outcome == "empty" {
							return []catalog.AuditEvent{}, nil
						}
						if reads == 1 {
							return []catalog.AuditEvent{a, b}, nil
						}
						return []catalog.AuditEvent{b}, nil
					}
					repo.appendAudit = func(c context.Context, record audit.Record) error {
						audits++
						expected := []audit.Record{a}
						if reads == 2 {
							expected = []audit.Record{b}
						}
						if outcome == "empty" {
							expected = []audit.Record{}
						}
						digest, _, err := audit.DigestRecords(expected)
						if err != nil {
							t.Fatal(err)
						}
						if !sameProjectionContext(c, ctx) || record.Action != audit.ActionAuditEventsRead || record.Actor != actor.MembershipID || record.AfterDigest != digest {
							t.Fatalf("page access=%+v", record)
						}
						if err := audit.ValidateNewRecord(record); err != nil {
							t.Fatal(err)
						}
						if outcome == "audit-unavailable" {
							return catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-audit")
						}
						return nil
					}
				}
				s := projectionServer(projectionCatalog(t, repo, nil))
				handler := map[string]http.HandlerFunc{"namespaces": s.handleNamespacesList, "publishers": s.handlePublishersList, "resources": s.handleResourcesList, "audit-events": s.handleAuditEvents}[kind]
				pages := 1
				if outcome == "pages" {
					pages = 2
				}
				for page := 0; page < pages; page++ {
					w := httptest.NewRecorder()
					handler(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "/?"+query.Encode(), nil))
					if outcome == "unavailable" || outcome == "audit-unavailable" {
						requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
						break
					}
					var got cursorPage[json.RawMessage]
					if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					items := []any{}
					if outcome != "empty" {
						if page == 0 {
							items = append(items, first)
						} else {
							items = append(items, second)
						}
					}
					requireControlJSON(t, w, 200, cursorPage[any]{Items: items, NextCursor: got.NextCursor})
					if (got.NextCursor != "") != (outcome == "pages" && page == 0) {
						t.Fatalf("unexpected continuation %q", got.NextCursor)
					}
					query.Set("cursor", got.NextCursor)
				}
				wantAudits := 0
				if kind == "audit-events" && outcome != "unavailable" {
					wantAudits = pages
				}
				if reads != pages || audits != wantAudits {
					t.Fatalf("reads=%d audits=%d", reads, audits)
				}
			})
		}
	}
}

func TestResourceListKindPresence(t *testing.T) {
	t.Parallel()
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleReader))
	for _, test := range []struct {
		name    string
		query   string
		kind    catalog.Kind
		invalid bool
	}{
		{name: "omitted", query: "limit=2"},
		{name: "agent", query: "kind=agent&limit=2", kind: catalog.KindAgent},
		{name: "MCP", query: "kind=mcp_server&limit=2", kind: catalog.KindMCPServer},
		{name: "Skill", query: "kind=skill&limit=2", kind: catalog.KindSkill},
		{name: "Plugin", query: "kind=plugin&limit=2", kind: catalog.KindPlugin},
		{name: "empty value", query: "kind=&limit=2", invalid: true},
		{name: "bare parameter", query: "kind&limit=2", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads := 0
			repo := controlPageRepository{resources: func(c context.Context, tenant string, request catalog.ResourceListRequest) ([]catalog.Resource, error) {
				reads++
				if !sameProjectionContext(c, ctx) || tenant != projectionTenant || request != (catalog.ResourceListRequest{Kind: test.kind, Limit: 3}) {
					t.Fatalf("resource filter binding: tenant=%s request=%+v", tenant, request)
				}
				return []catalog.Resource{}, nil
			}}
			server := projectionServer(projectionCatalog(t, repo, nil))
			response := httptest.NewRecorder()
			server.handleResourcesList(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/resources?"+test.query, nil))
			if test.invalid {
				requireProjectionProblem(t, response, http.StatusBadRequest, catalog.CodeInvalidArgument)
				if reads != 0 {
					t.Fatal("empty resource kind reached the repository")
				}
				return
			}
			requireControlJSON(t, response, http.StatusOK, cursorPage[any]{Items: []any{}})
			if reads != 1 {
				t.Fatalf("valid resource filter performed %d repository reads", reads)
			}
		})
	}
}

func TestControlPagePositionAdmission(t *testing.T) {
	t.Parallel()
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleReader))
	for _, kind := range []string{"namespaces", "publishers", "resources", "audit-events"} {
		for _, bad := range []string{"limit", "shape", "time"} {
			if bad == "time" && (kind == "namespaces" || kind == "publishers") {
				continue
			}
			t.Run(kind+"/"+bad, func(t *testing.T) {
				s := projectionServer(nil)
				handler := map[string]http.HandlerFunc{"namespaces": s.handleNamespacesList, "publishers": s.handlePublishersList, "resources": s.handleResourcesList, "audit-events": s.handleAuditEvents}[kind]
				query := url.Values{"limit": {"1"}}
				if bad == "limit" {
					query.Set("limit", "0")
				} else {
					order := map[string]string{"namespaces": "slug,id", "publishers": "displayName,id", "resources": "updatedAt desc,id desc", "audit-events": "occurredAt desc,id desc"}[kind]
					filters := map[string]string{}
					// Sign for the current filter binding so a malformed position
					// cannot pass this test through an unrelated signature refusal.
					if kind == "namespaces" {
						filters = map[string]string{"slug": "", "displayName": ""}
					}
					if kind == "publishers" {
						filters = map[string]string{"displayName": "", "declaredDomain": ""}
					}
					if kind == "resources" {
						filters["kind"] = ""
					}
					if kind == "audit-events" {
						_, filters, _ = parseAuditFilters(httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))
					}
					page, err := s.parseAdminPage(httptest.NewRequestWithContext(ctx, http.MethodGet, "/?"+query.Encode(), nil), kind, "", order, filters)
					if err != nil {
						t.Fatal(err)
					}
					position := []string{"only-one"}
					if bad == "time" {
						position = []string{"not-a-time", projectionItem}
					}
					token, err := s.encodeAdminCursor(page, position)
					if err != nil {
						t.Fatal(err)
					}
					query.Set("cursor", token)
				}
				w := httptest.NewRecorder()
				handler(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "/?"+query.Encode(), nil))
				requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
			})
		}
	}
}

// Scripted repository ports and HTTP recorders exercise admission without a
// listener, database, application binary or composed runtime.
func TestControlDirectoryFilterBinding(t *testing.T) {
	t.Parallel()
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleReader))
	for _, kind := range []string{"namespaces", "publishers"} {
		t.Run(kind, func(t *testing.T) {
			reads := 0
			name := " Exact  日本語 "
			query := url.Values{"limit": {"1"}, "displayName": {name}}
			repo := controlPageRepository{
				namespaces: func(c context.Context, tenant string, got catalog.NamespaceListRequest) ([]catalog.Namespace, error) {
					reads++
					want := catalog.NamespaceListRequest{Limit: 2, Slug: "tools--one", DisplayName: name}
					if tenant != projectionTenant || !sameProjectionContext(c, ctx) || got != want {
						t.Fatalf("namespace filter binding: tenant=%s got=%+v want=%+v", tenant, got, want)
					}
					return []catalog.Namespace{}, nil
				},
				publishers: func(c context.Context, tenant string, got catalog.PublisherListRequest) ([]catalog.Publisher, error) {
					reads++
					want := catalog.PublisherListRequest{Limit: 2, DeclaredDomain: "declared.example", DisplayName: name}
					if tenant != projectionTenant || !sameProjectionContext(c, ctx) || got != want {
						t.Fatalf("publisher filter binding: tenant=%s got=%+v want=%+v", tenant, got, want)
					}
					return []catalog.Publisher{}, nil
				},
			}
			s := projectionServer(projectionCatalog(t, repo, nil))
			var handler http.HandlerFunc
			filter := "slug"
			if kind == "namespaces" {
				query.Set(filter, "\u2028TOOLS--ONE\u3000")
				handler = s.handleNamespacesList
			} else {
				filter = "declaredDomain"
				query.Set(filter, "\u2028DECLARED.Example.\u3000")
				handler = s.handlePublishersList
			}
			w := httptest.NewRecorder()
			handler(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "/?"+query.Encode(), nil))
			requireControlJSON(t, w, http.StatusOK, cursorPage[any]{Items: []any{}})
			if reads != 1 {
				t.Fatalf("valid filter performed %d repository reads", reads)
			}
			for _, invalid := range []string{
				filter + "=", filter + "=one&" + filter + "=one", filter + "=%00", "DisplayName=Exact",
				"displayName=" + url.QueryEscape(strings.Repeat("\U0001f680", 161)),
			} {
				w = httptest.NewRecorder()
				handler(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "/?limit=1&"+invalid, nil))
				requireProjectionProblem(t, w, http.StatusBadRequest, catalog.CodeInvalidArgument)
				if reads != 1 {
					t.Fatalf("invalid filter %q reached the repository", invalid)
				}
			}
		})
	}
}
