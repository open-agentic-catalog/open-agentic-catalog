// Command seed builds governed scale fixtures only in the supervisor's owned
// loopback PostgreSQL. It never accepts a DSN or an external destination.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/database"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	fixture "github.com/open-agentic-catalog/open-agentic-catalog/test/performance/scalefixture"
)

// Static retrieval exercises the real normalization and atomic synchronization
// paths without measuring ingestion throughput or contacting a remote service.
type retrieval struct {
	body      []byte
	duplicate bool
}

// FetchExternalRepresentation uses fixed fixture bytes during governed admission.
func (r retrieval) FetchExternalRepresentation(ctx context.Context, request catalog.ExternalRepresentationFetchRequest) (catalog.ExternalRepresentationFetchResult, error) {
	if err := ctx.Err(); err != nil {
		return catalog.ExternalRepresentationFetchResult{}, err
	}
	body, ok := fixture.PluginArchive(request.URL)
	if !ok || request.MediaType != catalog.PluginMediaZIP || int64(len(body)) > request.MaxResponseBytes {
		return catalog.ExternalRepresentationFetchResult{}, errors.New("unexpected fixture package retrieval")
	}
	return catalog.ExternalRepresentationFetchResult{RequestedURL: request.URL, FinalURL: request.URL, Status: 200, MediaType: "application/zip", Body: body, ContentDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(body)), PolicyID: "scale-fixture", PolicyRevision: 1, PolicyDigest: r.PolicyDigest(), RetrievedAt: time.Now()}, nil
}

// PolicyDigest supplies a stable test-only policy identity, not an outbound policy.
func (r retrieval) PolicyDigest() string { return "sha256:" + strings.Repeat("a", 64) }

// Fetch serves only the selected synthetic manifest from memory, honoring cancellation
// and rejecting secondary URLs instead of issuing any network request.
func (r retrieval) Fetch(ctx context.Context, request fetcher.Request) (fetcher.Result, error) {
	if err := ctx.Err(); err != nil {
		return fetcher.Result{}, err
	}
	url := "https://scale.example/.well-known/ard.json"
	if r.duplicate {
		url = "https://scale-duplicate.example/.well-known/ard.json"
	}
	if request.URL != url {
		return fetcher.Result{}, errors.New("unexpected fixture retrieval")
	}
	return fetcher.Result{Status: 200, Body: r.body, BodyDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(r.body)), MediaType: "application/ard+json", FinalURL: request.URL, FetchedAt: time.Now(), PolicyID: "scale-fixture", PolicyRevision: 1, PolicyDigest: r.PolicyDigest()}, nil
}

// actor provisions real membership evidence for a synthetic identity and returns
// its domain context; the caller supplies the isolated schema-owner connection.
func actor(ctx context.Context, pool, apiPool *pgxpool.Pool, subject string, roles []identity.Role) (context.Context, error) {
	var displayName string
	if err := pool.QueryRow(ctx, "SELECT display_name FROM tenants WHERE id=$1", fixture.Tenant).Scan(&displayName); err != nil {
		return nil, err
	}
	_, err := database.ProvisionIdentity(ctx, pool, database.IdentityProvisioningInput{TenantID: fixture.Tenant, TenantDisplayName: displayName, Issuer: "https://scale.example/identity", TenancyMode: "dedicated", Subject: subject, ActorType: identity.ActorHuman, Roles: roles, Operator: "scale-fixture", Reason: "Provision owned synthetic fixture", AcknowledgeOutbox: true})
	if err != nil {
		return nil, err
	}
	repo, err := database.NewPostgresRepository(apiPool)
	if err != nil {
		return nil, err
	}
	store, err := database.NewPostgresIdentityStore(repo)
	if err != nil {
		return nil, err
	}
	a, err := store.ResolvePrincipal(ctx, identity.Principal{TenantID: fixture.Tenant, Issuer: "https://scale.example/identity", Subject: subject})
	if err != nil {
		return nil, err
	}
	return identity.WithActor(ctx, a), nil
}

type seedProgress struct {
	Stage                 string                   `json:"stage"`
	Status                string                   `json:"status"`
	Code                  catalog.ErrorCode        `json:"code,omitempty"`
	SQLState              string                   `json:"sqlState,omitempty"`
	Detail                string                   `json:"detail,omitempty"`
	Inventory             *seedInventory           `json:"inventory,omitempty"`
	SQL                   map[string]seedQueryCost `json:"sql,omitempty"`
	CompletedLocalRecords int                      `json:"completedLocalRecords"`
}

// Counts describe stored evidence in the measured tenant, not requested size.
type seedInventory struct {
	SparseNamespace              string         `json:"sparseNamespace,omitempty"`
	FixtureVersion               int            `json:"fixtureVersion"`
	OverviewExpected             map[string]int `json:"overviewExpected"`
	Resources                    int64          `json:"resources"`
	VersionRevisions             int64          `json:"versionRevisions"`
	ListingRevisions             int64          `json:"listingRevisions"`
	ListingReleases              int64          `json:"listingReleases"`
	Observations                 int64          `json:"observations"`
	DocumentBytes                int64          `json:"documentBytes"`
	RepresentationReferenceBytes int64          `json:"representationReferenceBytes"`
	IndexedResponseBytes         int            `json:"indexedResponseBytes"`
	HelperHeapBytes              uint64         `json:"helperHeapBytes"`
	HelperAllocatedBytes         uint64         `json:"helperAllocatedBytes"`
}

// seed populates the owned loopback database through domain and exact-role paths,
// including local governance and synthetic indexed observations. It retains stage
// evidence on failure; the supervising harness owns database teardown.
func seed(ctx context.Context, port, items int, progress *seedProgress, localOnly, publicOnly bool) error {
	progress.Stage = "connect"
	queries := &seedQueries{costs: make(map[string]seedQueryCost)}
	defer func() { progress.SQL = queries.snapshot() }()
	open := func(user, password string) (*pgxpool.Pool, error) {
		return database.OpenRuntimeObserved(ctx, fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/catalog?sslmode=disable", user, password, port), database.PoolConfig{MaxConnections: 2, MaxConnLifetime: 30 * time.Minute, MaxConnIdleTime: 5 * time.Minute, HealthCheckPeriod: 30 * time.Second}, queries)
	}
	owner, err := open("catalog", "catalog")
	if err != nil {
		return err
	}
	defer owner.Close()
	api, err := open("catalog_api", "catalog-api")
	if err != nil {
		return err
	}
	defer api.Close()
	worker, err := open("catalog_worker", "catalog-worker")
	if err != nil {
		return err
	}
	defer worker.Close()
	progress.Stage = "provision"
	author, err := actor(ctx, owner, api, "scale-author", []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor, identity.RolePublisher, identity.RoleReader, identity.RoleSourceAdmin})
	if err != nil {
		return err
	}
	reviewer, err := actor(ctx, owner, api, "scale-reviewer", []identity.Role{identity.RoleReviewer})
	if err != nil {
		return err
	}
	repo, err := database.NewPostgresRepository(api)
	if err != nil {
		return err
	}
	key := sha256.Sum256([]byte("owned-scale-fixture"))
	service, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), ExternalFetcher: retrieval{}, CursorKey: key[:], Policy: "default-v1", AcknowledgeOutboxAtCreation: true})
	if err != nil {
		return err
	}
	progress.Stage = "namespace-publisher"
	namespaces := make([]string, 5)
	for i := range namespaces {
		n, e := service.CreateNamespace(author, catalog.CreateNamespaceInput{Slug: fmt.Sprintf("scale-%d", i), DisplayName: fmt.Sprintf("Scale %d", i)})
		if e != nil {
			return e
		}
		namespaces[i] = n.ID
	}
	sparseNamespace := ""
	if !localOnly {
		progress.Stage = "sparse-reader"
		sparseNamespace, err = seedSparseReader(ctx, owner, api, service, author, progress)
		if err != nil {
			return err
		}
		namespaces[4] = sparseNamespace
	}
	publisher, err := service.CreatePublisher(author, catalog.CreatePublisherInput{DisplayName: fixture.Publisher, Domains: []string{"scale.example"}})
	if err != nil {
		return err
	}
	if publicOnly {
		publisher, err = service.ApprovePublisherPublicationDomain(author, publisher.ID, "scale.example", "Approve the owned public delivery fixture", publisher.Revision)
		if err != nil {
			return err
		}
	}
	localCount := fixture.Count(items, false)
	if localOnly {
		// The semantic pilot starts with the three published demo records,
		// and reuses this governance path without a second bulk-seed system.
		localCount = items - 3
	}
	authorActor, _ := identity.FromContext(author)
	reviewerActor, _ := identity.FromContext(reviewer)
	err = seedPairs(ctx, localCount, progress, func(call context.Context, i int, localProgress *seedProgress) (preparedRecord, error) {
		index, visibility := i, catalog.VisibilityTenant
		if publicOnly {
			// Preserve the private four-kind corpus; public delivery uses MCP cards.
			index, visibility = i*4+1, catalog.VisibilityPublic
		}
		return prepareSeedRecord(identity.WithActor(call, authorActor), identity.WithActor(call, reviewerActor), service, namespaces[i%5], publisher.ID, index, localProgress, visibility)
	}, func(record preparedRecord, localProgress *seedProgress) error {
		return publishSeedRecord(author, service, record, localProgress)
	})
	if err != nil {
		return err
	}
	var entries []map[string]any
	var body []byte
	if !localOnly {
		progress.Stage = "indexed-inspect"
		entries = make([]map[string]any, fixture.Count(items, true))
		for i := range entries {
			entries[i] = fixture.Entry(i)
		}
		body, err = json.Marshal(map[string]any{"entries": entries})
		if err != nil {
			return err
		}
		outbound := retrieval{body: body}
		store, err := database.NewPostgresFederationStore(repo)
		if err != nil {
			return err
		}
		manager, err := federation.NewManager(store, outbound, clock.Real{}, id.Generator{}, federation.WithEvidenceSink(repo, true))
		if err != nil {
			return err
		}
		inspection, err := manager.Inspect(author, "https://scale.example/.well-known/ard.json")
		if err != nil {
			return err
		}
		progress.Stage = "indexed-connect"
		source, err := manager.Connect(author, federation.ConnectInput{InspectionID: inspection.ID, DisplayName: "Scale bulk source", Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill, catalog.KindPlugin}}, RetrievalMode: federation.RetrievalIndexed, UpdateMode: federation.UpdateManual, Reason: "Owned synthetic indexed corpus"})
		if err != nil {
			return err
		}
		wrepo, err := database.NewPostgresRepository(worker)
		if err != nil {
			return err
		}
		wstore, err := database.NewPostgresFederationStore(wrepo)
		if err != nil {
			return err
		}
		wmanager, err := federation.NewManager(wstore, outbound, clock.Real{}, id.Generator{}, federation.WithEvidenceSink(wrepo, true))
		if err != nil {
			return err
		}
		progress.Stage = "indexed-sync"
		apiJobs, err := database.NewPostgresJobStore(repo)
		if err != nil {
			return err
		}
		workerJobs, err := database.NewPostgresJobStore(wrepo)
		if err != nil {
			return err
		}
		snapshot, err := syncFixture(author, apiJobs, workerJobs, source, wmanager.SyncAtRevision)
		if err != nil {
			return err
		}
		if snapshot.Added != len(entries) {
			return errors.New("indexed count mismatch")
		}
		// A second source contributes the same seven identities without adding groups.
		duplicateBody, marshalErr := json.Marshal(map[string]any{"entries": entries[:7]})
		if marshalErr != nil {
			return marshalErr
		}
		duplicateRetrieval := retrieval{body: duplicateBody, duplicate: true}
		duplicateManager, managerErr := federation.NewManager(store, duplicateRetrieval, clock.Real{}, id.Generator{}, federation.WithEvidenceSink(repo, true))
		if managerErr != nil {
			return managerErr
		}
		inspection, err = duplicateManager.Inspect(author, "https://scale-duplicate.example/.well-known/ard.json")
		if err != nil {
			return err
		}
		duplicateSource, connectErr := duplicateManager.Connect(author, federation.ConnectInput{InspectionID: inspection.ID, DisplayName: "Scale duplicate source", Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill, catalog.KindPlugin}}, RetrievalMode: federation.RetrievalIndexed, UpdateMode: federation.UpdateManual, Reason: "Qualify distinct indexed counts"})
		if connectErr != nil {
			return connectErr
		}
		duplicateWorker, workerErr := federation.NewManager(wstore, duplicateRetrieval, clock.Real{}, id.Generator{}, federation.WithEvidenceSink(wrepo, true))
		if workerErr != nil {
			return workerErr
		}
		duplicateSnapshot, syncErr := syncFixture(author, apiJobs, workerJobs, duplicateSource, duplicateWorker.SyncAtRevision)
		if syncErr != nil {
			return syncErr
		}
		if duplicateSnapshot.Added != 7 {
			return errors.New("duplicate count mismatch")
		}
	}
	// Only statistics change through the owner; application writes above use
	// their ordinary exact API/worker roles and immutable evidence guards.
	if _, err := owner.Exec(ctx, "ANALYZE"); err != nil {
		return err
	}
	progress.Stage = "inventory"
	inventory := &seedInventory{SparseNamespace: sparseNamespace, FixtureVersion: fixture.Version, IndexedResponseBytes: len(body)}
	// Fixed read-only setup accounting on the owned tenant, never seed DML.
	observeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := owner.QueryRow(observeCtx, `SELECT
	(SELECT count(*) FROM resources WHERE tenant_id=$1),
	(SELECT count(*) FROM resource_version_revisions WHERE tenant_id=$1),
	(SELECT count(*) FROM listing_revisions WHERE tenant_id=$1),
	(SELECT count(*) FROM listing_releases WHERE tenant_id=$1),
	(SELECT count(*) FROM observations WHERE tenant_id=$1),
	(SELECT coalesce(sum(size_bytes),0) FROM document_blobs WHERE tenant_id=$1),
	(SELECT coalesce(sum(size_bytes),0) FROM representations WHERE tenant_id=$1)`, fixture.Tenant).Scan(&inventory.Resources, &inventory.VersionRevisions, &inventory.ListingRevisions, &inventory.ListingReleases, &inventory.Observations, &inventory.DocumentBytes, &inventory.RepresentationReferenceBytes); err != nil {
		return err
	}
	progress.Inventory = inventory
	inventory.OverviewExpected = map[string]int{"indexed": items / 10, "sparseResources": localCount / 5}
	if localOnly {
		inventory.OverviewExpected["indexed"] = 0
	}
	for name, statement := range map[string]string{
		"resources":   `SELECT count(*) FROM resources WHERE tenant_id=$1 AND deleted_at IS NULL`,
		"agent":       `SELECT count(*) FROM resources WHERE tenant_id=$1 AND deleted_at IS NULL AND kind='agent'`,
		"mcp_server":  `SELECT count(*) FROM resources WHERE tenant_id=$1 AND deleted_at IS NULL AND kind='mcp_server'`,
		"skill":       `SELECT count(*) FROM resources WHERE tenant_id=$1 AND deleted_at IS NULL AND kind='skill'`,
		"plugin":      `SELECT count(*) FROM resources WHERE tenant_id=$1 AND deleted_at IS NULL AND kind='plugin'`,
		"published":   `SELECT count(*) FROM catalog_listings l JOIN resources r ON r.tenant_id=l.tenant_id AND r.id=l.resource_id JOIN listing_releases v ON v.tenant_id=l.tenant_id AND v.id=l.current_release_id WHERE l.tenant_id=$1 AND r.deleted_at IS NULL AND v.state='published'`,
		"namespaces":  `SELECT count(*) FROM namespaces WHERE tenant_id=$1`,
		"publishers":  `SELECT count(*) FROM publishers WHERE tenant_id=$1`,
		"sources":     `SELECT count(*) FROM federation_sources WHERE tenant_id=$1 AND status<>'removed'`,
		"human":       `SELECT count(*) FROM tenant_memberships WHERE tenant_id=$1 AND actor_type='human'`,
		"service":     `SELECT count(*) FROM tenant_memberships WHERE tenant_id=$1 AND actor_type='service'`,
		"roles":       `SELECT count(*) FROM role_definitions WHERE tenant_id=$1 AND state='active'`,
		"assignments": `SELECT count(*) FROM role_assignments WHERE tenant_id=$1 AND state='active'`,
		"feedback":    `SELECT count(*) FROM item_feedback f JOIN resources r ON r.tenant_id=f.tenant_id AND r.id=f.resource_id WHERE f.tenant_id=$1 AND f.state='open' AND r.deleted_at IS NULL`,
		"ratings":     `SELECT count(*) FROM item_ratings WHERE tenant_id=$1`,
	} {
		var count int
		if err := owner.QueryRow(observeCtx, statement, fixture.Tenant).Scan(&count); err != nil {
			return err
		}
		inventory.OverviewExpected[name] = count
	}
	// Smoke retains additional non-searchable evidence (including a Registry
	// self entry). Only current releases/groups count toward searchable size.
	wantedLocal, wantedIndexed := int64(items*9/10), int64(items/10)
	if localOnly {
		wantedLocal, wantedIndexed = int64(items), 0
	}
	if inventory.Resources < wantedLocal || inventory.ListingReleases != wantedLocal || inventory.Observations < wantedIndexed {
		return errors.New("stored scale inventory mismatch")
	}
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	inventory.HelperHeapBytes, inventory.HelperAllocatedBytes = stats.HeapAlloc, stats.TotalAlloc
	progress.Inventory = inventory
	fmt.Printf("seed complete: local=%d indexed=%d heap=%d allocated=%d\n", localCount, len(entries), stats.HeapAlloc, stats.TotalAlloc)
	return nil
}

// The supervisor stops the packaged worker during setup. This helper processes
// one real, audited Job with the fixture transport, then the packaged worker
// resumes for measurement. No fabricated Job attribution or SQL seed is used.
func syncFixture(ctx context.Context, apiStore, workerStore jobs.Store, source federation.Source, sync func(context.Context, string, int64) (federation.Snapshot, error)) (federation.Snapshot, error) {
	service, err := jobs.NewService(apiStore, clock.Real{}, id.Generator{}, jobs.WithAuditEvidence())
	if err != nil {
		return federation.Snapshot{}, err
	}
	payload, err := json.Marshal(struct {
		SourceID       string `json:"sourceId"`
		SourceRevision int64  `json:"sourceRevision"`
		Trigger        string `json:"trigger"`
	}{source.ID, source.Revision, "manual"})
	if err != nil {
		return federation.Snapshot{}, err
	}
	job, _, err := service.Enqueue(ctx, jobs.EnqueueInput{Kind: "source.sync", PayloadVersion: 2, Payload: payload, AttemptLimit: 1, DeduplicationKey: "source.sync:" + source.ID + ":" + strconv.FormatInt(source.Revision, 10), SourceSyncAudit: &jobs.SourceSyncRequestAudit{SourceID: source.ID, SourceRevision: source.Revision, Trigger: "manual"}})
	if err != nil {
		return federation.Snapshot{}, err
	}
	var result federation.Snapshot
	var syncErr error
	w, err := jobs.NewWorker(workerStore, clock.Real{}, "scale-seed", time.Minute, time.Second, time.Hour, map[string]jobs.Handler{"source.sync": func(call context.Context, claimed jobs.Job, _ func(int) error) (any, error) {
		if claimed.ID != job.ID {
			return nil, errors.New("unexpected seed Job")
		}
		workerContext := identity.WithActor(call, identity.Actor{TenantID: claimed.TenantID, Subject: "scale-seed", ActorType: identity.ActorService})
		workerContext, syncErr = identity.WithInternalJobAuthority(workerContext, claimed.Kind, claimed.TenantID, source.ID, claimed.ID, claimed.LeaseOwner, claimed.Attempt, func(ctx context.Context) error { return jobs.CheckClaimedAttempt(ctx, claimed) })
		if syncErr != nil {
			return nil, syncErr
		}
		result, syncErr = sync(audit.WithJobTrigger(workerContext, "manual"), source.ID, source.Revision)
		return result, syncErr
	}})
	if err != nil {
		return result, err
	}
	if err := w.RunOnce(ctx); err != nil {
		return result, err
	}
	if syncErr != nil {
		return result, syncErr
	}
	stored, err := service.Get(ctx, job.ID)
	if err != nil {
		return result, err
	}
	if stored.State != jobs.StateSucceeded {
		return result, errors.New("seed Job did not succeed")
	}
	return result, nil
}

// Preparation retains normal authoring, validation and independent approval.
// Every hundredth record also retains an older version without an extra release.
func prepareSeedRecord(author, reviewer context.Context, s *catalog.Service, namespace, publisher string, i int, progress *seedProgress, visibility catalog.Visibility) (preparedRecord, error) {
	progress.Stage = "resource"
	r, err := s.CreateResource(author, catalog.CreateResourceInput{NamespaceID: namespace, CanonicalIdentifier: fixture.Identifier(i, false), Kind: fixture.Kind(i)})
	if err != nil {
		return preparedRecord{}, err
	}
	progress.Stage = "version"
	representation := fixture.Representation(i)
	if visibility == catalog.VisibilityPublic && fixture.Kind(i) == catalog.KindMCPServer {
		// Public discovery accepts Server Cards, not Registry server.json files.
		representation.MediaType = adapter.MediaMCPServerCard
		representation.Data = json.RawMessage(`{"$schema":"https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json","name":"io.example/scale","description":"Synthetic scale representation.","version":"1.0.0","remotes":[{"type":"streamable-http","url":"https://scale.example/mcp","supportedProtocolVersions":["2025-11-25"]}]}`)
	}
	if i%100 == 0 {
		// Retain a prior immutable version, without another searchable release.
		if _, _, _, err := s.CreateVersion(author, r.ID, catalog.CreateVersionInput{Version: "0.9.0", Representations: []catalog.RepresentationInput{representation}}); err != nil {
			return preparedRecord{}, err
		}
	}
	_, v, _, err := s.CreateVersion(author, r.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{representation}})
	if err != nil {
		return preparedRecord{}, err
	}
	progress.Stage = "listing"
	l, err := s.CreateListing(author, catalog.CreateListingInput{NamespaceID: namespace, ResourceID: r.ID})
	if err != nil {
		return preparedRecord{}, err
	}
	listing := catalog.CreateListingRevisionInput{VersionRevisionID: v.ID, DisplayName: fixture.Name(i, false), Description: fixture.Description(i), PublisherID: publisher, Visibility: visibility, Tags: fixture.Tags(i)}
	if visibility == catalog.VisibilityPublic {
		listing.RepresentativeQueries = []string{"Find the scale MCP server", "Discover synthetic scale metadata"}
		listing.License = "Apache-2.0"
	}
	l, _, err = s.CreateListingRevision(author, l.ID, listing, l.Revision)
	if err != nil {
		return preparedRecord{}, err
	}
	progress.Stage = "submit"
	sub, err := s.Submit(author, l.ID)
	if err != nil {
		return preparedRecord{}, err
	}
	progress.Stage = "review"
	if _, _, err = s.DecideSubmission(reviewer, sub.ID, "approve", "Independent synthetic review", sub.Revision); err != nil {
		return preparedRecord{}, err
	}
	return preparedRecord{listing: l, submissionID: sub.ID}, nil
}

func publishSeedRecord(author context.Context, s *catalog.Service, record preparedRecord, progress *seedProgress) error {
	progress.Stage = "publish"
	_, _, _, err := s.Publish(author, record.listing.ID, catalog.PublishInput{SubmissionID: record.submissionID}, record.listing.Revision)
	return err
}

// run validates fixture controls, enforces cancellation/setup time and exclusively
// creates a private progress report, including bounded failure classification.
func run(args []string) (resultErr error) {
	if len(args) != 4 && (len(args) != 5 || args[4] != "local" && args[4] != "public") {
		return errors.New("expected owned port, size and setup seconds")
	}
	port, e1 := strconv.Atoi(args[0])
	n, e2 := strconv.Atoi(args[1])
	seconds, e3 := strconv.Atoi(args[2])
	if e1 != nil || e2 != nil || e3 != nil || port < 1 || port > 65535 || !fixture.ValidSize(n) || seconds < 1 || seconds > 7200 || len(args) == 5 && args[4] == "public" && n != 1000 {
		return errors.New("invalid seed controls")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	// The trusted supervisor supplies a private output path. Exclusive creation
	// rejects existing files and symlinks; this command accepts no HTTP input.
	file, err := os.OpenFile(args[3], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G703 -- supervisor-owned exclusive report output
	if err != nil {
		return err
	}
	defer func() {
		if err := file.Close(); resultErr == nil {
			resultErr = err
		}
	}()
	progress := seedProgress{Status: "incomplete"}
	resultErr = seed(ctx, port, n, &progress, len(args) == 5, len(args) == 5 && args[4] == "public")
	if resultErr == nil {
		progress.Status = "passed"
	} else {
		progress.Code = catalog.ErrorCodeOf(resultErr)
		var domain *catalog.DomainError
		if errors.As(resultErr, &domain) {
			progress.Detail = domain.Message
		}
		var sql *pgconn.PgError
		if errors.As(resultErr, &sql) {
			progress.SQLState = sql.Code
		}
	}
	if err := json.NewEncoder(file).Encode(progress); resultErr == nil {
		resultErr = err
	}
	return resultErr
}

// main exposes only the safe domain error code when scale setup fails.
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "scale seed failed:", catalog.ErrorCodeOf(err))
		os.Exit(1)
	}
}

// The resource-bearing Namespace is last in 105 retained Namespace grants.
// No clock, private policy table or SQL grant is changed to create this scope.
func seedSparseReader(ctx context.Context, owner, api *pgxpool.Pool, service *catalog.Service, author context.Context, progress *seedProgress) (string, error) {
	progress.Stage = "sparse-reader-namespaces"
	var namespaces []string
	for i := 0; i < 105; i++ {
		ns, err := service.CreateNamespace(author, catalog.CreateNamespaceInput{Slug: fmt.Sprintf("scale-sparse-%03d", i), DisplayName: "Scale sparse scope"})
		if err != nil {
			return "", err
		}
		namespaces = append(namespaces, ns.ID)
	}
	repo, err := database.NewPostgresRepository(api)
	if err != nil {
		return "", err
	}
	store, err := database.NewPostgresIdentityStore(repo)
	if err != nil {
		return "", err
	}
	progress.Stage = "sparse-reader-identity"
	// The normal Compose bootstrap already provisioned this configured token.
	// Resolve it instead of provisioning another development identity here.
	member, err := store.ResolvePrincipal(ctx, identity.Principal{TenantID: fixture.Tenant, Issuer: authn.DevelopmentIssuer, Subject: "scale-sparse"})
	if err != nil {
		return "", err
	}
	progress.Stage = "sparse-reader-administrator"
	admin, err := store.ResolvePrincipal(ctx, identity.Principal{TenantID: fixture.Tenant, Issuer: authn.DevelopmentIssuer, Subject: "local-admin"})
	if err != nil {
		return "", err
	}
	authority, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clock.Real{}, IDs: id.Generator{}, CursorKey: []byte("owned-scale-identity-cursor-key-32"), AcknowledgeOutboxAtCreation: true})
	if err != nil {
		return "", err
	}
	progress.Stage = "sparse-reader-template"
	var role string
	if err := owner.QueryRow(ctx, "SELECT id::text FROM role_definitions WHERE tenant_id=$1 AND key='reader' AND builtin", fixture.Tenant).Scan(&role); err != nil {
		return "", err
	}
	progress.Stage = "sparse-reader-assignments"
	for i, namespace := range namespaces {
		if _, err := authority.GrantAuthorizationAssignment(identity.WithActor(ctx, admin), identity.AccessAssignmentInput{RoleDefinitionID: role, MembershipID: member.MembershipID, NamespaceID: namespace, Reason: "Bounded sparse-reader qualification"}, fmt.Sprintf("scale-sparse-%03d", i)); err != nil {
			return "", err
		}
	}
	return slices.Max(namespaces), nil
}
