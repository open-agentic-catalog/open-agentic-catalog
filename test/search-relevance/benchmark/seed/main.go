// Command seed prepares only an empty invocation-owned Catalog for the benchmark.
// It reuses current domain governance and exact PostgreSQL roles, not SQL inserts.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/database"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	fixture "github.com/open-agentic-catalog/open-agentic-catalog/test/performance/scalefixture"
)

type document struct {
	ID                    string       `json:"id"`
	Identifier            string       `json:"identifier"`
	Name                  string       `json:"name"`
	Description           string       `json:"description"`
	Tags                  []string     `json:"tags"`
	RepresentativeQueries []string     `json:"representativeQueries"`
	Kind                  catalog.Kind `json:"kind"`
	Family                string       `json:"family"`
	Source                string       `json:"source"`
	Public                bool         `json:"public"`
}

type input struct {
	Port      int        `json:"port"`
	Documents []document `json:"documents"`
}

type retained struct {
	ReleaseID  string `json:"releaseId"`
	ResourceID string `json:"resourceId"`
	Projection string `json:"projection"`
}

// Only the existing embedded digest-bound Plugin packages can be retrieved.
type retrieval struct{}

func (retrieval) PolicyDigest() string { return "sha256:" + strings.Repeat("a", 64) }
func (r retrieval) FetchExternalRepresentation(ctx context.Context, request catalog.ExternalRepresentationFetchRequest) (catalog.ExternalRepresentationFetchResult, error) {
	if err := ctx.Err(); err != nil {
		return catalog.ExternalRepresentationFetchResult{}, err
	}
	body, ok := fixture.PluginArchive(request.URL)
	if !ok || request.MediaType != catalog.PluginMediaZIP || int64(len(body)) > request.MaxResponseBytes {
		return catalog.ExternalRepresentationFetchResult{}, errors.New("unexpected benchmark package retrieval")
	}
	return catalog.ExternalRepresentationFetchResult{RequestedURL: request.URL, FinalURL: request.URL, Status: 200, MediaType: "application/zip", Body: body,
		ContentDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(body)), PolicyID: "benchmark-fixture", PolicyRevision: 1, PolicyDigest: r.PolicyDigest(), RetrievedAt: time.Now()}, nil
}

func principal(ctx context.Context, owner, api *pgxpool.Pool, subject string, roles []identity.Role, namespaces map[string][]identity.Role) (context.Context, error) {
	var name string
	if err := owner.QueryRow(ctx, "SELECT display_name FROM tenants WHERE id=$1", fixture.Tenant).Scan(&name); err != nil {
		return nil, err
	}
	operator := "benchmark-fixture"
	if len(namespaces) != 0 {
		operator = "catalogctl-development-bootstrap"
	}
	_, err := database.ProvisionIdentity(ctx, owner, database.IdentityProvisioningInput{TenantID: fixture.Tenant, TenantDisplayName: name,
		Issuer: authn.DevelopmentIssuer, TenancyMode: "dedicated", Subject: subject, ActorType: identity.ActorHuman,
		Roles: roles, Namespaces: namespaces, Operator: operator, Reason: "Owned synthetic benchmark", AcknowledgeOutbox: true})
	if err != nil {
		return nil, err
	}
	repo, err := database.NewPostgresRepository(api)
	if err != nil {
		return nil, err
	}
	store, err := database.NewPostgresIdentityStore(repo)
	if err != nil {
		return nil, err
	}
	actor, err := store.ResolvePrincipal(ctx, identity.Principal{TenantID: fixture.Tenant, Issuer: authn.DevelopmentIssuer, Subject: subject})
	return identity.WithActor(ctx, actor), err
}

func create(ctx, reviewer context.Context, service *catalog.Service, namespace, publisher string, d document, representation catalog.RepresentationInput) (retained, error) {
	r, err := service.CreateResource(ctx, catalog.CreateResourceInput{NamespaceID: namespace, CanonicalIdentifier: d.Identifier, Kind: d.Kind})
	if err != nil {
		return retained{}, err
	}
	_, revision, _, err := service.CreateVersion(ctx, r.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{representation}})
	if err != nil {
		return retained{}, err
	}
	listing, err := service.CreateListing(ctx, catalog.CreateListingInput{NamespaceID: namespace, ResourceID: r.ID})
	if err != nil {
		return retained{}, err
	}
	listing, _, err = service.CreateListingRevision(ctx, listing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: revision.ID, DisplayName: d.Name,
		Description: d.Description, Tags: d.Tags, RepresentativeQueries: d.RepresentativeQueries, PublisherID: publisher, Visibility: catalog.VisibilityTenant, License: "Apache-2.0"}, listing.Revision)
	if err != nil {
		return retained{}, err
	}
	submission, err := service.Submit(ctx, listing.ID)
	if err != nil {
		return retained{}, err
	}
	_, _, err = service.DecideSubmission(reviewer, submission.ID, "approve", "Reviewed synthetic metadata; no execution claim", submission.Revision)
	if err != nil {
		return retained{}, err
	}
	listing, err = service.GetListing(ctx, listing.ID)
	if err != nil {
		return retained{}, err
	}
	_, release, _, err := service.Publish(ctx, listing.ID, catalog.PublishInput{SubmissionID: submission.ID}, listing.Revision)
	if err != nil {
		return retained{}, err
	}
	actual, err := service.GetCatalogRecord(ctx, release.ID)
	if err != nil {
		return retained{}, err
	}
	projection, _, err := (catalog.SemanticDocument{DisplayName: actual.ListingRevision.DisplayName, Description: actual.ListingRevision.Description,
		Tags: actual.ListingRevision.Tags, RepresentativeQueries: actual.ListingRevision.RepresentativeQueries}).Projection()
	return retained{ReleaseID: release.ID, ResourceID: r.ID, Projection: projection}, err
}

func run(reader io.Reader, writer io.Writer) error {
	decoder := json.NewDecoder(io.LimitReader(reader, (2<<20)+1))
	decoder.DisallowUnknownFields()
	var request input
	if err := decoder.Decode(&request); err != nil {
		return err
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF || request.Port < 1 || request.Port > 65535 || len(request.Documents) != 160 {
		return errors.New("invalid owned benchmark input")
	}
	seen := map[string]bool{}
	for _, d := range request.Documents {
		if d.ID == "" || seen[d.ID] || d.Source != "local" || d.Public {
			return errors.New("invalid benchmark corpus")
		}
		seen[d.ID] = true
		if _, _, err := (catalog.SemanticDocument{DisplayName: d.Name, Description: d.Description, Tags: d.Tags, RepresentativeQueries: d.RepresentativeQueries}).Projection(); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	open := func(user, password string) (*pgxpool.Pool, error) {
		return database.OpenRuntimeObserved(ctx, fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/catalog?sslmode=disable", user, password, request.Port), database.PoolConfig{MaxConnections: 2, MaxConnLifetime: 30 * time.Minute, MaxConnIdleTime: 5 * time.Minute, HealthCheckPeriod: 30 * time.Second}, nil)
	}
	owner, err := open("catalog", "catalog")
	if err != nil {
		return err
	}
	defer owner.Close()
	var count int
	if err = owner.QueryRow(ctx, "SELECT count(*) FROM resources").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("benchmark requires an empty disposable Catalog; refusing writes")
	}
	api, err := open("catalog_api", "catalog-api")
	if err != nil {
		return err
	}
	defer api.Close()
	author, err := principal(ctx, owner, api, "benchmark-author", []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor, identity.RolePublisher, identity.RoleReader}, nil)
	if err != nil {
		return err
	}
	reviewer, err := principal(ctx, owner, api, "benchmark-reviewer", []identity.Role{identity.RoleReviewer}, nil)
	if err != nil {
		return err
	}
	repo, err := database.NewPostgresRepository(api)
	if err != nil {
		return err
	}
	key := sha256.Sum256([]byte("owned-benchmark-fixture"))
	service, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), ExternalFetcher: retrieval{}, CursorKey: key[:], Policy: "default-v1", AcknowledgeOutboxAtCreation: true})
	if err != nil {
		return err
	}
	namespace, err := service.CreateNamespace(author, catalog.CreateNamespaceInput{Slug: "benchmark", DisplayName: "Benchmark"})
	if err != nil {
		return err
	}
	outside, err := service.CreateNamespace(author, catalog.CreateNamespaceInput{Slug: "benchmark-excluded", DisplayName: "Excluded control"})
	if err != nil {
		return err
	}
	_, err = principal(ctx, owner, api, "benchmark-reader", nil, map[string][]identity.Role{namespace.ID: {identity.RoleReader}})
	if err != nil {
		return err
	}
	publisher, err := service.CreatePublisher(author, catalog.CreatePublisherInput{DisplayName: "Synthetic benchmark", Domains: []string{"catalog.example.test"}})
	if err != nil {
		return err
	}
	values := map[string]retained{}
	for i, d := range request.Documents {
		if fixture.Kind(i) != d.Kind {
			return errors.New("unexpected reviewed corpus kind order")
		}
		value, e := create(author, reviewer, service, namespace.ID, publisher.ID, d, fixture.Representation(i))
		if e != nil {
			return fmt.Errorf("seed %s: %w", d.ID, e)
		}
		values[d.ID] = value
	}
	control := document{ID: "excluded", Identifier: "urn:air:catalog.example.test:benchmark:excluded", Name: "Excluded benchmark control", Description: "SENTINEL_EXCLUDED_METADATA", Kind: catalog.KindAgent}
	excluded, err := create(author, reviewer, service, outside.ID, publisher.ID, control, fixture.Representation(4))
	if err != nil {
		return err
	}
	draft, err := service.CreateResource(author, catalog.CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.test:benchmark:draft", Kind: catalog.KindAgent})
	if err != nil {
		return err
	}
	return json.NewEncoder(writer).Encode(map[string]any{"namespaceId": namespace.ID, "records": values, "excluded": excluded, "draftId": draft.ID, "indexedReleases": len(values) + 1})
}

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
