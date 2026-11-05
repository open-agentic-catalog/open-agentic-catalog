// Package runtime composes configured domain services and platform adapters.
// Build selects dependencies by process role and validates deployment inputs
// before serving work. Callers own listener startup, supervision, and draining;
// Runtime.Close releases the composed pool and remaining tracing resources.
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/feedback"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/housekeeping"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/buildinfo"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/database"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/embeddings"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/httpserver"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/operations"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/processinfo"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/tracing"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/webhooksender"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/tenantsettings"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
	"github.com/open-agentic-catalog/open-agentic-catalog/schemas"
)

// Runtime owns the role-specific service graph and any shared PostgreSQL pool.
// A field is nil when its capability does not belong to the selected role.
type Runtime struct {
	Config             config.Config
	Catalog            *catalog.Service
	Federation         *federation.Manager
	Publication        *publication.Manager
	Publications       map[string]*publication.Manager
	Identity           *identity.Service
	Authenticator      authn.Authenticator
	Jobs               *jobs.Service
	JobWorker          *jobs.Worker
	SourceMaintainer   *federation.Maintainer
	SemanticMaintainer interface{ Run(context.Context) error }
	EmbeddingClient    *embeddings.Client
	Housekeeper        interface{ Run(context.Context) error }
	Webhooks           *webhook.Service
	WebhookDispatcher  *webhook.Dispatcher
	WebhookDrainer     *webhook.Drainer
	OutboxProcessor    OutboxProcessor
	HTTP               *httpserver.Server
	Operations         *operations.Server
	Repository         catalog.Repository
	Pool               *pgxpool.Pool
	Observability      *observability.Registry
	Process            *processinfo.Process
	Tracing            *tracing.Provider
	logger             *slog.Logger
}

// Logger returns the process logger shared by composed adapters and listeners.
func (r *Runtime) Logger() *slog.Logger { return r.logger }

type OutboxProcessor interface {
	Run(context.Context) error
}

type memoryIdentityEvidenceSink struct {
	repository catalog.Repository
}

// AppendIdentityEvidence adapts development identity evidence to the Catalog
// repository and captures causal context for the outbox record.
func (s memoryIdentityEvidenceSink) AppendIdentityEvidence(ctx context.Context, tenantID string, evidence identity.ChangeEvidence) error {
	record := catalog.AuditEvent{FormatVersion: evidence.FormatVersion, ID: evidence.AuditID, TenantID: tenantID, Actor: evidence.Actor, Action: evidence.Action, TargetType: evidence.TargetType, TargetID: evidence.TargetID, Reason: evidence.Reason, RequestID: evidence.RequestID, Details: evidence.Details, At: evidence.At}
	if evidence.OutboxID == "" && strings.HasPrefix(evidence.Action, "authorization.") {
		return s.repository.AppendAuditEvent(ctx, record)
	}
	outbox := catalog.OutboxEvent{CausalTrace: catalog.CausalTrace(ctx), ID: evidence.OutboxID, TenantID: record.TenantID, Type: evidence.OutboxType, Subject: evidence.OutboxSubject, Data: json.RawMessage(evidence.OutboxData), CreatedAt: evidence.At, PublishedAt: evidence.OutboxPublished}
	return s.repository.AppendEvidence(ctx, record, &outbox)
}

// BuildOptions identifies the process role and release metadata to compose.
type BuildOptions struct {
	// StartTracing is enabled only by catalogd, never operator runtime construction.
	StartTracing bool
	ConfigPath   string
	Version      string
	Commit       string
	SourceState  string
	Role         config.Role
	Logger       *slog.Logger
	// LogOutput defaults to stdout; operator commands explicitly select stderr.
	// An injected Logger owns its destination and filtering instead.
	LogOutput io.Writer
}

// Build validates immutable inputs first, then composes only the dependencies
// required by the selected API, worker, or in-memory development role.
func Build(ctx context.Context, options BuildOptions) (*Runtime, error) {
	identity := buildinfo.New(options.Version, options.Commit, options.SourceState)
	options.Version, options.Commit, options.SourceState = identity.Version, identity.Commit, identity.SourceState
	return buildWithTracing(ctx, options, tracing.New)
}

// buildWithTracing keeps tracing construction observable while retaining the
// same provider ownership on successful construction and startup failure.
func buildWithTracing(ctx context.Context, options BuildOptions, startTracing func(context.Context, config.Tracing, config.Role, string, string, *slog.Logger, tracing.Observer) (*tracing.Provider, error)) (_ *Runtime, err error) {
	if !options.Role.Valid() || options.Role == config.RoleMigration {
		return nil, fmt.Errorf("unsupported runtime role %q", options.Role)
	}
	runsAPI := options.Role == config.RoleAPI || options.Role == config.RoleAll
	runsWorker := options.Role == config.RoleWorker || options.Role == config.RoleAll
	profileReport, err := schemas.VerifyBundle()
	if err != nil {
		return nil, fmt.Errorf("verify embedded standards profiles: %w", err)
	}
	configuration, err := config.LoadFor(options.ConfigPath, options.Role)
	if err != nil {
		return nil, err
	}
	process := diagnostics.Process{Service: configuration.ServiceName, Role: string(options.Role), Environment: configuration.Environment, Version: options.Version}
	defer func() { err = diagnostics.WithFormat(err, configuration.LogFormat, process) }()
	defer safeerr.Recover(&err)
	cursorKey, err := config.CursorSigningKey(configuration)
	if err != nil {
		return nil, err
	}
	var webhookKey []byte
	if configuration.Webhooks.Enabled {
		webhookKey, err = config.WebhookEncryptionKey(configuration)
		if err != nil {
			return nil, err
		}
	}
	logger := options.Logger
	if logger == nil {
		output := options.LogOutput
		if output == nil {
			output = os.Stdout
		}
		logger = diagnostics.NewLogger(output, configuration.LogLevel, configuration.LogFormat)
	}
	logger = process.Logger(logger)
	realClock := clock.Real{}
	processInfo, err := processinfo.New()
	if err != nil {
		return nil, err
	}
	metrics := observability.New(time.Now, processInfo)
	idGenerator := id.Generator{}
	if runsAPI {
		metrics.SetComponent(observability.ComponentAPI, observability.ComponentStarting)
	}
	if runsWorker {
		metrics.SetComponent(observability.ComponentWorker, observability.ComponentStarting)
		metrics.SetComponent(observability.ComponentOutbox, observability.ComponentStarting)
		metrics.SetComponent(observability.ComponentSourceMaintenance, observability.ComponentStarting)
		metrics.SetComponent(observability.ComponentHousekeeping, observability.ComponentStarting)
		metrics.SetComponent(observability.ComponentOperations, observability.ComponentStarting)
	}
	var repository catalog.Repository
	var pool *pgxpool.Pool
	var embeddingClient *embeddings.Client
	var semanticStore *database.PostgresSemanticStore
	var semanticSearch *catalog.SemanticSearch
	var semanticMaintainerLoop *semanticMaintainer
	var tracingProvider *tracing.Provider
	var tracePort catalog.Tracer
	if options.StartTracing {
		tracingProvider, err = startTracing(ctx, configuration.Tracing, options.Role, options.Version, processInfo.ID(), logger, metrics)
		if err != nil {
			return nil, err
		}
		if tracingProvider != nil {
			tracePort = tracingProvider
		}
	}
	buildSucceeded := false
	// This defer covers failed construction. Branch-local PostgreSQL closes
	// release the pool before tracing shutdown; pgx permits repeated Close calls.
	defer func() {
		if !buildSucceeded && embeddingClient != nil {
			embeddingClient.Close()
		}
		if !buildSucceeded && tracingProvider != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), configuration.Tracing.ExportTimeout.Duration)
			defer cancel()
			_ = tracingProvider.Shutdown(cleanup)
		}
		if !buildSucceeded && pool != nil {
			pool.Close()
		}
	}()
	var jobStore jobs.Store
	var federationStore federation.Store
	var maintenanceStore federation.MaintenanceStore
	var memoryFederationStore *federation.MemoryStore
	publicationStores := make(map[string]publication.Store)
	memoryPublicationSummaries := publication.MemoryHostSummaries{}
	var publicationSummaries publication.ActiveSummaryReader = memoryPublicationSummaries
	var idempotencyStore idempotency.Store
	var atomicIdempotencyStore idempotency.AtomicStore
	var webhookStore webhook.Store
	var personalStore personal.Store
	var feedbackStore feedback.Store
	var settingsStore tenantsettings.Store
	var memorySettings *tenantsettings.MemoryStore
	var ratingStore ratings.Store
	var identityStore identity.Store
	var postgresRepository *database.PostgresRepository
	var housekeepingStore housekeeping.Store
	// Select all persistence adapters together so one role cannot accidentally
	// mix in-memory and PostgreSQL authority.
	switch configuration.Database.Driver {
	case "memory":
		memoryRepository := catalog.NewMemoryRepository()
		repository = memoryRepository
		personalStore = personal.NewMemoryStore(repository)
		feedbackStore = feedback.NewMemoryStore(repository, repository)
		ratingStore = ratings.NewMemoryStore(repository)
		memorySettings = tenantsettings.NewMemoryStore(repository)
		settingsStore = memorySettings
		jobStore = jobs.NewMemoryStore(repository)
		memoryFederationStore = federation.NewMemoryStore(repository)
		memoryFederationStore.BindLocalCatalog(repository)
		federationStore = memoryFederationStore
		for _, host := range configuration.Discovery.Hosts {
			if host.Mode != string(httpserver.DiscoveryDisabled) {
				store := publication.NewMemoryStore(repository)
				publicationStores[host.Host] = store
				memoryPublicationSummaries[host.Host] = store
			}
		}
		if runsAPI {
			idempotencyStore = idempotency.NewMemoryStore()
			identityStore, err = identity.NewMemoryStore(developmentIdentitySeeds(configuration), memoryIdentityEvidenceSink{repository: memoryRepository})
			if err != nil {
				return nil, err
			}
		}
		if configuration.Webhooks.Enabled {
			webhookStore = webhook.NewMemoryStore(repository)
		}
	case "postgres":
		pool, err = database.OpenRuntimeObserved(ctx, configuration.Database.DSN, database.PoolConfig{
			MaxConnections:    configuration.Database.MaxConnections,
			MaxConnLifetime:   configuration.Database.MaxConnLifetime.Duration,
			MaxConnIdleTime:   configuration.Database.MaxConnIdleTime.Duration,
			HealthCheckPeriod: configuration.Database.HealthCheckPeriod.Duration,
		}, metrics)
		if err != nil {
			return nil, err
		}
		if _, err := database.VerifyRuntimeRole(ctx, pool, string(options.Role)); err != nil {
			pool.Close()
			return nil, err
		}
		postgresRepository, err = database.NewPostgresRepository(pool)
		if err != nil {
			pool.Close()
			return nil, err
		}
		repository = postgresRepository
		settingsStore, err = database.NewPostgresTenantSettingsStore(postgresRepository)
		if err != nil {
			return nil, err
		}
		ratingStore, err = database.NewPostgresRatingsStore(postgresRepository)
		if err != nil {
			return nil, err
		}
		personalStore, err = database.NewPostgresPersonalStore(postgresRepository)
		if err != nil {
			return nil, err
		}
		feedbackStore, err = database.NewPostgresFeedbackStore(postgresRepository)
		if err != nil {
			return nil, err
		}
		metrics.SetPoolSnapshot(func() observability.PoolSnapshot {
			stat := pool.Stat()
			return observability.PoolSnapshot{
				AcquireCount: stat.AcquireCount(), AcquireSeconds: stat.AcquireDuration().Seconds(),
				CanceledAcquireCount: stat.CanceledAcquireCount(), EmptyAcquireCount: stat.EmptyAcquireCount(),
				EmptyAcquireWaitSeconds: stat.EmptyAcquireWaitTime().Seconds(), AcquiredConnections: stat.AcquiredConns(),
				IdleConnections: stat.IdleConns(), TotalConnections: stat.TotalConns(), MaxConnections: stat.MaxConns(),
			}
		})
		metrics.SetDependency("postgresql", true)
		if runsWorker {
			metrics.SetSnapshotProvider(postgresRepository)
		}
		jobStore, err = database.NewPostgresJobStore(postgresRepository)
		if err != nil {
			pool.Close()
			return nil, err
		}
		if configuration.SemanticSearch.Enabled {
			semanticStore, err = database.NewPostgresSemanticStore(ctx, postgresRepository)
			if err != nil {
				return nil, err
			}
			embeddingClient, err = embeddings.NewObserved(configuration.SemanticSearch, metrics.ObserveEmbedding)
			if err != nil {
				return nil, err
			}
			semanticSearch, err = catalog.NewSemanticSearch(catalog.SemanticSearchOptions{Observe: metrics.ObserveRetrieval, Store: semanticStore, Provider: embeddingClient, ProviderState: embeddingClient.State, Profile: configuration.SemanticSearch.EmbeddingProfile(), RankingProfile: configuration.SemanticSearch.RankingProfile(), Dimensions: configuration.SemanticSearch.Dimensions, Cutoff: configuration.SemanticSearch.SimilarityCutoff, Timeout: configuration.SemanticSearch.QueryTimeout.Duration, ExcludedTenants: configuration.SemanticSearch.ExcludedTenants})
			if err != nil {
				return nil, err
			}
			jobStore, err = database.NewPostgresSemanticJobStore(postgresRepository, configuration.SemanticSearch.EmbeddingProfile(), configuration.SemanticSearch.ExcludedTenants)
			if err != nil {
				return nil, err
			}
		}
		federationStore, err = database.NewPostgresFederationStore(postgresRepository)
		if err != nil {
			pool.Close()
			return nil, err
		}
		maintenanceStore, err = database.NewPostgresSourceMaintenanceStore(postgresRepository)
		if err != nil {
			pool.Close()
			return nil, err
		}
		if runsWorker {
			housekeepingStore, err = database.NewPostgresHousekeepingStore(postgresRepository)
			if err != nil {
				pool.Close()
				return nil, err
			}
		}
		publicationSummaries = postgresRepository
		for _, host := range configuration.Discovery.Hosts {
			if host.Mode == string(httpserver.DiscoveryDisabled) {
				continue
			}
			publicationStores[host.Host], err = database.NewPostgresPublicationStore(postgresRepository, host.Host)
			if err != nil {
				pool.Close()
				return nil, err
			}
		}
		if runsAPI {
			atomicIdempotencyStore, err = database.NewPostgresIdempotencyStore(postgresRepository)
			if err != nil {
				pool.Close()
				return nil, err
			}
			identityStore, err = database.NewPostgresIdentityStore(postgresRepository)
			if err != nil {
				pool.Close()
				return nil, err
			}
		}
		if configuration.Webhooks.Enabled {
			webhookStore, err = database.NewPostgresWebhookStore(postgresRepository)
			if err != nil {
				pool.Close()
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("unsupported repository driver %q", configuration.Database.Driver)
	}
	withdrawals := &publicationWithdrawalCoordinator{managers: make(map[string]publicationBinding)}
	var credentialResolver fetcher.CredentialResolver
	if configuration.CredentialResolution.Enabled {
		credentialResolver, err = fetcher.NewFileCredentialResolver(configuration.CredentialResolution.Directory)
		if err != nil {
			return nil, fmt.Errorf("configure federation credential resolution: %w", err)
		}
	}
	// Catalog URL representations and federation share this single outbound
	// policy boundary; neither domain creates its own HTTP client.
	routingPolicy, err := federationRoutingPolicy(configuration, options.Version)
	if err != nil {
		return nil, err
	}
	outbound, err := fetcher.NewRouter(routingPolicy, credentialResolver)
	if err != nil {
		return nil, fmt.Errorf("configure federation routing: %w", err)
	}
	artworkService, err := newArtworkService(configuration, outbound)
	if err != nil {
		return nil, err
	}
	representations, err := newDemoArchiveOutbound(configuration, outbound)
	if err != nil {
		return nil, err
	}
	validators, err := adapter.NewRegistryWithLimits(configuration.Validation)
	if err != nil {
		return nil, err
	}
	domainPolicy := catalog.DefaultPublisherDomainPolicy()
	var publisherTXT catalog.PublisherTXTResolver
	if runsAPI {
		domainPolicy = configuration.PublisherDomainVerification.Policy()
		if domainPolicy.Mode != catalog.DomainVerificationDisabled {
			publisherTXT = net.DefaultResolver
		}
	}
	catalogService, err := catalog.NewService(catalog.Options{PublisherDomainPolicy: domainPolicy, PublisherTXT: publisherTXT, Artwork: artworkService, SemanticSearch: semanticSearch, Repository: repository, Clock: realClock, IDs: idGenerator, Validator: validators, DiagnosticLimits: configuration.Validation, ExternalFetcher: externalRepresentationFetcher{outbound: representations}, CursorKey: cursorKey, Policy: "default-v1", AcknowledgeOutboxAtCreation: !configuration.Webhooks.Enabled, PublicationWithdrawer: withdrawals})
	if err != nil {
		return nil, err
	}
	trustVerification, err := newTrustVerifier(configuration, outbound, credentialResolver)
	if err != nil {
		return nil, err
	}
	if memoryFederationStore != nil {
		jobStore = memorySettingsJobs{Store: memoryTrustJobs{Store: jobStore, sources: memoryFederationStore}, settings: memorySettings}
	}
	federationTargets := federationDiscoveryTargets(configuration.Discovery.Hosts)
	federationManager, err := federation.NewManager(federationStore, outbound, realClock, idGenerator, federation.WithTrustVerifier(trustVerification), federation.WithExecutionLimits(configuration.Federation.MaxFanout, configuration.Federation.MaxHops), federation.WithLiveSearchTimeout(configuration.Federation.RequestTimeout.Duration), federation.WithLiveSearchPages(configuration.Federation.MaxLivePagesPerSource), federation.WithDiscoveryTargets(federationTargets), federation.WithObservationAdopter(catalogService), federation.WithEvidenceSink(repository, !configuration.Webhooks.Enabled))
	if err != nil {
		return nil, err
	}
	// Publication is bound per exact discovery host. The withdrawal coordinator
	// preserves that same tenant/host boundary for safety-driven revocation.
	publicationManagers := make(map[string]*publication.Manager, len(publicationStores))
	publicationConstructionLimiter := publication.NewConstructionLimiter()
	discoveryHosts := make([]httpserver.DiscoveryHost, 0, len(configuration.Discovery.Hosts))
	for _, host := range configuration.Discovery.Hosts {
		binding := httpserver.DiscoveryHost{Host: host.Host, TenantID: host.TenantID, Mode: httpserver.DiscoveryMode(host.Mode), PublisherDomain: host.PublisherDomain}
		if host.Mode != string(httpserver.DiscoveryDisabled) {
			audience := publication.AudienceAnonymous
			if host.Mode == string(httpserver.DiscoveryAuthenticated) {
				audience = publication.AudienceAuthenticated
			}
			manager, managerErr := publication.NewManager(publication.Options{Ratings: ratingStore, RatingsSharingEnabled: configuration.Ratings.SharingEnabled, RatingsMinimumSharedCount: configuration.Ratings.MinimumSharedCount, ArtworkAllowed: func(descriptor artwork.Descriptor) bool {
				return artworkPublicDescriptorAllowed(configuration, descriptor)
			}, Semantic: semanticSearch, Records: catalogService, Store: publicationStores[host.Host], Clock: realClock, IDs: idGenerator, BaseURL: discoveryBaseURL(configuration.HTTP.PublicBaseURL, host.Host), PublisherDomain: host.PublisherDomain, ARDProfile: configuration.Publication.ARDProfile, AICatalogProfile: configuration.Publication.AICatalogProfile, CursorKey: cursorKey, DisplayName: "Open Agentic Catalog", Host: host.Host, Audience: audience, Evidence: repository, ConstructionLimiter: publicationConstructionLimiter, AcknowledgeOutboxAtCreation: !configuration.Webhooks.Enabled})
			if managerErr != nil {
				return nil, managerErr
			}
			publicationManagers[host.Host] = manager
			withdrawals.managers[host.Host] = publicationBinding{tenantID: host.TenantID, manager: manager}
			binding.Publication = manager
		}
		discoveryHosts = append(discoveryHosts, binding)
	}
	publicationManager := firstPublicationManager(publicationManagers)
	jobSemanticProfile := ""
	if semanticSearch != nil {
		jobSemanticProfile = configuration.SemanticSearch.EmbeddingProfile()
	}
	jobService, err := jobs.NewService(jobStore, realClock, idGenerator, jobs.WithAuditEvidence(), jobs.WithEnqueueValidation(func(ctx context.Context, job jobs.Job) error {
		if job.Kind == jobs.TrustKind {
			return federationManager.ValidateTrustEnqueue(ctx, job)
		}
		return nil
	}), jobs.WithMaxAttempts(configuration.Jobs.MaxAttempts), jobs.WithListTargets(func(tenantID string) jobs.ListTargets {
		return configuredJobCollectionTargets(tenantID, withdrawals.managers, jobSemanticProfile)
	}), jobs.WithTargetValidation(func(ctx context.Context, job jobs.Job) error {
		switch job.Kind {
		case jobs.TrustKind:
			return federationManager.ValidateTrustJob(ctx, job)
		case "source.sync":
			var input struct {
				SourceID       string `json:"sourceId"`
				SourceRevision int64  `json:"sourceRevision"`
				Trigger        string `json:"trigger"`
			}
			if jsonUnmarshal(job.Payload, &input) != nil || !identity.ValidTenantID(input.SourceID) {
				return catalog.E(catalog.CodeForbidden, "Job source binding is invalid")
			}
			_, err := federationManager.GetSource(ctx, input.SourceID)
			return err
		case "publication.generate":
			var input struct {
				Host string `json:"host"`
			}
			if len(job.Payload) > 0 && jsonUnmarshal(job.Payload, &input) != nil {
				return catalog.E(catalog.CodeForbidden, "Job host binding is invalid")
			}
			if input.Host == "" && len(publicationManagers) == 1 {
				input.Host = firstPublicationManager(publicationManagers).Host()
			}
			binding, ok := withdrawals.managers[input.Host]
			if !ok || binding.tenantID != job.TenantID {
				return catalog.E(catalog.CodeNotFound, "configured Job host was not found")
			}
		case "semantic.reindex":
			input, err := catalog.DecodeSemanticReindex(job.Payload, job.PayloadVersion)
			if err != nil {
				return err
			}
			if semanticSearch == nil || input.Profile != configuration.SemanticSearch.EmbeddingProfile() {
				return catalog.E(catalog.CodeNotFound, "configured Job profile was not found")
			}
		default:
			return catalog.E(catalog.CodeForbidden, "Job kind has no user authority")
		}
		return nil
	}))
	if err != nil {
		return nil, err
	}
	if memoryFederationStore != nil {
		maintenanceStore, err = federation.NewMemoryMaintenanceStore(memoryFederationStore, jobStore, memorySettings)
		if err != nil {
			return nil, err
		}
	}
	var jobWorker *jobs.Worker
	var sourceMaintainer *federation.Maintainer
	var housekeeper interface{ Run(context.Context) error }
	var workerID string
	if runsWorker {
		// Handlers receive only durable, versioned payloads; composition translates
		// their domain failures into the Job retry vocabulary.
		sourceMaintainer, err = federation.NewMaintainer(maintenanceStore, realClock, idGenerator, configuration.Worker.SourceMaintenanceInterval.Duration, configuration.Federation.PolicyDigest, configuration.Jobs.MaxAttempts)
		if err != nil {
			return nil, err
		}
		handlers := map[string]jobs.Handler{
			jobs.TrustKind:         trustJobHandler(federationManager, logger),
			"source.sync":          sourceSyncJobHandler(federationManager.SyncAtRevision, metrics),
			"publication.generate": publicationJobHandler(publicationManagers, metrics),
		}
		if semanticStore != nil {
			indexer, indexerErr := catalog.NewSemanticIndexer(semanticStore, embeddingClient, configuration.SemanticSearch.BatchSize, configuration.SemanticSearch.MaxInputBytes-len(configuration.SemanticSearch.DocumentPrefix))
			if indexerErr != nil {
				return nil, indexerErr
			}
			handlers["semantic.reindex"] = semanticJobHandler(configuration.SemanticSearch, indexer)
			semanticMaintainerLoop = &semanticMaintainer{store: semanticStore, configuration: configuration.SemanticSearch, maxAttempts: configuration.Jobs.MaxAttempts, clock: realClock, ids: idGenerator}
		}
		workerID, err = idGenerator.New(realClock.Now())
		if err != nil {
			return nil, err
		}
		jobWorker, err = jobs.NewWorker(jobStore, realClock, "worker-"+workerID, configuration.Worker.LeaseDuration.Duration, configuration.Worker.PollInterval.Duration, configuration.Jobs.RetryCeiling.Duration, handlers, jobObserver{metrics, logger})
		if err != nil {
			return nil, err
		}
		if housekeepingStore == nil {
			housekeeper = housekeeping.Noop{}
		} else {
			housekeeper, err = housekeeping.New(housekeepingStore, configuration.Worker.HousekeepingInterval.Duration, configuration.Worker.TerminalJobRetention.Duration, configuration.Worker.AcknowledgedOutboxRetention.Duration, metrics)
			if err != nil {
				return nil, err
			}
		}
	}
	var webhookService *webhook.Service
	var webhookDispatcher *webhook.Dispatcher
	var webhookDrainer *webhook.Drainer
	var outboxProcessor OutboxProcessor
	if configuration.Webhooks.Enabled {
		cipher, cipherErr := webhook.NewAESGCM(webhookKey)
		if cipherErr != nil {
			return nil, cipherErr
		}
		webhookFetcher, fetchErr := fetcher.New(fetcher.Policy{Timeout: configuration.Webhooks.Timeout.Duration, MaxResponseBytes: 1 << 20, MaxRedirects: 3, UserAgent: "open-agentic-catalog-webhooks/" + options.Version}, nil)
		if fetchErr != nil {
			return nil, fetchErr
		}
		sender, senderErr := webhooksender.New(webhookFetcher)
		if configuration.Webhooks.PolicyFile != "" {
			policy, policyErr := webhookRoutingPolicy(configuration, options.Version)
			if policyErr != nil {
				return nil, policyErr
			}
			sender, senderErr = webhooksender.NewWithPolicy(policy)
		}
		if senderErr != nil {
			return nil, senderErr
		}
		if runsAPI {
			webhookService, err = webhook.NewService(webhookStore, cipher, sender, realClock, idGenerator, metrics)
			if err != nil {
				return nil, err
			}
		}
		if runsWorker {
			metrics.SetComponent(observability.ComponentWebhookDelivery, observability.ComponentRunning)
			webhookDispatcher, err = webhook.NewDispatcher(repository, webhookStore, cipher, sender, realClock, idGenerator, "webhook-"+workerID, configuration.Worker.LeaseDuration.Duration, configuration.Webhooks.MaxAttempts, configuration.Webhooks.PollInterval.Duration, configuration.Webhooks.RetryCeiling.Duration, metrics)
			if err != nil {
				return nil, err
			}
			outboxProcessor = webhookDispatcher
		}
	} else if runsWorker {
		webhookDrainer, err = webhook.NewDrainer(repository, realClock, "outbox-"+workerID, configuration.Worker.LeaseDuration.Duration, configuration.Worker.PollInterval.Duration, metrics)
		if err != nil {
			return nil, err
		}
		outboxProcessor = webhookDrainer
	}
	var httpService *httpserver.Server
	var operationsService *operations.Server
	var identityService *identity.Service
	var authenticator authn.Authenticator
	if runsAPI {
		identityService, err = identity.NewService(identity.ServiceOptions{Store: identityStore, Clock: realClock, IDs: idGenerator, CursorKey: cursorKey, AcknowledgeOutboxAtCreation: !configuration.Webhooks.Enabled})
		if err != nil {
			return nil, err
		}
		authenticator, err = authn.New(ctx, configuration.Auth, identityStore)
		if err != nil {
			return nil, err
		}
		if verifier, ok := authenticator.(*authn.OIDC); ok {
			configuration.Auth.TrustBundleDigest = verifier.TrustBundleDigest()
		}
		feedbackService, feedbackErr := feedback.NewService(feedbackStore, repository, realClock, idGenerator)
		if feedbackErr != nil {
			return nil, feedbackErr
		}
		settingsService, settingsErr := tenantsettings.New(settingsStore, configuration.Worker.SourceMaintenanceInterval.Duration, configuration.Jobs.MaxAttempts, realClock, idGenerator)
		if settingsErr != nil {
			return nil, settingsErr
		}
		httpService, err = httpserver.New(httpserver.Dependencies{PublicationOverview: publicationSummaries, PersonalStore: personalStore, TenantSettings: settingsService, MCP: configuration.MCP, PublicBaseURL: configuration.HTTP.PublicBaseURL, AuthIssuer: configuration.Auth.Issuer, RatingsStore: ratingStore, Artwork: artworkService, MediaPolicyDigest: mediaPolicyDigest(configuration), Feedback: feedbackService, Catalog: catalogService, Identity: identityService, Federation: federationManager, Jobs: jobService, Webhooks: webhookService, Authenticator: authenticator, Idempotency: idempotencyStore, AtomicIdempotency: atomicIdempotencyStore, Logger: logger, DiscoveryHosts: discoveryHosts, MaxBodyBytes: configuration.HTTP.MaxBodyBytes, RequestTimeout: configuration.HTTP.RequestTimeout.Duration, ServiceVersion: options.Version, Process: processInfo, Role: string(options.Role), Commit: options.Commit, SourceState: options.SourceState, ARDProfile: configuration.Publication.ARDProfile, AICatalogProfile: configuration.Publication.AICatalogProfile, ProfileBundleDigest: profileReport.BundleDigest, ProfileArtifactCount: profileReport.ArtifactCount, CORSOrigins: configuration.HTTP.CORSOrigins, Readiness: repository.Ping, EnforceOAuthScopes: configuration.Auth.EnforceScopes, Capabilities: httpserver.CapabilitySnapshot{ServiceActivity: capabilityState(configuration.HTTP.ServiceActivityEnabled), SemanticSearch: capabilityState(configuration.SemanticSearch.Enabled), CredentialResolution: capabilityState(configuration.CredentialResolution.Enabled), ManagedMedia: httpserver.CapabilityUnavailable, SoftwareAssessment: httpserver.CapabilityUnavailable, Webhooks: capabilityState(configuration.Webhooks.Enabled)}, CursorKey: cursorKey, Observability: metrics, Tracing: tracePort})
		if err != nil {
			return nil, err
		}
	}
	if runsWorker {
		operationsService, err = operations.New(operations.Dependencies{Observability: metrics, Process: processInfo, Readiness: repository.Ping, Version: options.Version, Commit: options.Commit, SourceState: options.SourceState, HousekeepingInterval: configuration.Worker.HousekeepingInterval.Duration, TerminalJobRetention: configuration.Worker.TerminalJobRetention.Duration, AcknowledgedOutboxRetention: configuration.Worker.AcknowledgedOutboxRetention.Duration})
		if err != nil {
			return nil, err
		}
	}
	result := &Runtime{Config: configuration, Catalog: catalogService, Federation: federationManager, Publication: publicationManager, Publications: publicationManagers, Identity: identityService, Authenticator: authenticator, Jobs: jobService, JobWorker: jobWorker, SourceMaintainer: sourceMaintainer, Housekeeper: housekeeper, Webhooks: webhookService, WebhookDispatcher: webhookDispatcher, WebhookDrainer: webhookDrainer, OutboxProcessor: outboxProcessor, HTTP: httpService, Operations: operationsService, Repository: repository, Pool: pool, Observability: metrics, Process: processInfo, Tracing: tracingProvider, logger: logger}
	result.EmbeddingClient = embeddingClient
	if semanticMaintainerLoop != nil {
		result.SemanticMaintainer = semanticMaintainerLoop
	}
	diagnostics.Startup(logger, options.Commit, configuration.LogLevel, configuration.LogFormat, options.Logger != nil)
	buildSucceeded = true
	return result, nil
}

// capabilityState projects a wired boolean capability into enabled/disabled HTTP
// metadata; unavailable capabilities are represented separately.
func capabilityState(enabled bool) httpserver.CapabilityState {
	if enabled {
		return httpserver.CapabilityEnabled
	}
	return httpserver.CapabilityDisabled
}

// jobFailureClass translates federation outcomes into durable retry
// classifications, using transient upstream failure for an unknown class.
func jobFailureClass(class federation.SyncFailureClass) jobs.FailureClass {
	switch class {
	case federation.SyncFailureInvalidInput:
		return jobs.FailureInvalidInput
	case federation.SyncFailurePolicy:
		return jobs.FailurePolicyDenied
	case federation.SyncFailureAuthentication:
		return jobs.FailureAuthentication
	case federation.SyncFailureRateLimited:
		return jobs.FailureRateLimited
	case federation.SyncFailurePermanent:
		return jobs.FailurePermanentUpstream
	case federation.SyncFailureDependency:
		return jobs.FailureDependency
	case federation.SyncFailureInternal:
		return jobs.FailureInternal
	case federation.SyncFailureCancelled:
		return jobs.FailureCancelled
	case federation.SyncFailureTransient:
		return jobs.FailureTransientUpstream
	default:
		return jobs.FailureTransientUpstream
	}
}

// sourceSyncSafeError produces fixed class-specific job diagnostics without
// retaining external response text or source credentials.
func sourceSyncSafeError(class federation.SyncFailureClass) string {
	switch class {
	case federation.SyncFailureInvalidInput:
		return "The source changed, was removed, or returned invalid content."
	case federation.SyncFailurePolicy:
		return "The source is no longer permitted by federation policy."
	case federation.SyncFailureAuthentication:
		return "Source authentication failed."
	case federation.SyncFailureRateLimited:
		return "Source synchronization was rate limited."
	case federation.SyncFailurePermanent:
		return "The source returned an unsupported permanent response."
	case federation.SyncFailureDependency:
		return "A required source synchronization dependency is unavailable."
	case federation.SyncFailureInternal:
		return "Source synchronization encountered an internal error."
	case federation.SyncFailureCancelled:
		return "Source synchronization was cancelled."
	case federation.SyncFailureTransient:
		return "Source synchronization did not complete within its safe bounds."
	default:
		return "Source synchronization did not complete within its safe bounds."
	}
}

// federationDiscoveryTargets projects validated enabled hosts into exact
// tenant and audience bindings for federation source assignment.
func federationDiscoveryTargets(hosts []config.DiscoveryHost) []federation.DiscoveryTarget {
	targets := make([]federation.DiscoveryTarget, 0, len(hosts))
	for _, host := range hosts {
		if host.Mode == string(httpserver.DiscoveryDisabled) {
			continue
		}
		audience := federation.AudienceAnonymous
		if host.Mode == string(httpserver.DiscoveryAuthenticated) {
			audience = federation.AudienceAuthenticated
		}
		targets = append(targets, federation.DiscoveryTarget{Host: host.Host, TenantID: host.TenantID, Audience: audience})
	}
	return targets
}

// webhookRoutingPolicy projects an explicitly loaded webhook policy, retaining
// its independent topology and applying HTTPS-only webhook delivery limits.
func webhookRoutingPolicy(configuration config.Config, version string) (fetcher.RoutingPolicy, error) {
	isolated := configuration
	isolated.Federation.Policy = configuration.Webhooks.Policy
	isolated.Federation.PolicyDigest = configuration.Webhooks.PolicyDigest
	policy, err := federationRoutingPolicy(isolated, version)
	if err != nil {
		return fetcher.RoutingPolicy{}, err
	}
	policy.AllowHTTPForTests = false
	policy.Timeout = configuration.Webhooks.Timeout.Duration
	policy.MaxResponseBytes = 1 << 20
	policy.MaxRedirects = 0
	policy.UserAgent = "open-agentic-catalog-webhooks/" + version
	return policy, nil
}

// federationRoutingPolicy copies normalized operator policy into fetcher-owned
// inputs and restricts loopback test-address permission to the test environment.
func federationRoutingPolicy(configuration config.Config, version string) (fetcher.RoutingPolicy, error) {
	policy := fetcher.RoutingPolicy{
		ID:                configuration.Federation.Policy.PolicyID,
		Revision:          configuration.Federation.Policy.Revision,
		Digest:            configuration.Federation.PolicyDigest,
		Public:            fetcher.PublicRoute{Mode: configuration.Federation.Policy.Public.Mode, Origins: append([]string(nil), configuration.Federation.Policy.Public.Origins...)},
		DeniedOrigins:     append([]string(nil), configuration.Federation.Policy.AdditionalDeniedOrigins...),
		AllowHTTPForTests: configuration.Environment != "production",
		Timeout:           configuration.Federation.RequestTimeout.Duration,
		MaxResponseBytes:  configuration.Federation.MaxResponseBytes,
		MaxRedirects:      configuration.Federation.MaxRedirects,
		UserAgent:         "open-agentic-catalog/" + version,
	}
	var err error
	policy.DeniedAddressPrefixes, err = parseFederationPrefixes(configuration.Federation.Policy.AdditionalDeniedAddressRanges)
	if err != nil {
		return fetcher.RoutingPolicy{}, err
	}
	for _, configured := range configuration.Federation.Policy.PrivateProfiles {
		prefixes, parseErr := parseFederationPrefixes(configured.AddressRanges)
		if parseErr != nil {
			return fetcher.RoutingPolicy{}, parseErr
		}
		policy.PrivateRoutes = append(policy.PrivateRoutes, fetcher.PrivateRoute{
			Name:               configured.Name,
			Origins:            append([]string(nil), configured.Origins...),
			AddressPrefixes:    prefixes,
			AssignedTenantIDs:  append([]string(nil), configured.AssignedTenantIDs...),
			TrustBundlePEM:     append([]byte(nil), configured.TrustBundlePEM...),
			AllowTestAddresses: configuration.Environment == "test",
		})
	}
	return policy, nil
}

// parseFederationPrefixes decodes already normalized CIDRs for the fetcher,
// treating any parse failure as inconsistent composition input.
func parseFederationPrefixes(values []string) ([]netip.Prefix, error) {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("parse normalized federation address range: %w", err)
		}
		result = append(result, prefix)
	}
	return result, nil
}

// Close releases the database pool and abandons any remaining trace export.
// The process must stop callers and perform bounded tracing shutdown first;
// Close is a final cleanup path, not the graceful-drain coordinator.
func (r *Runtime) Close() {
	if r.EmbeddingClient != nil {
		r.EmbeddingClient.Close()
	}
	if r.Tracing != nil {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = r.Tracing.Shutdown(ctx)
	}
	if r.Pool != nil {
		r.Pool.Close()
	}
}

// jsonUnmarshal decodes a nonempty internal job payload, rejecting duplicate
// keys, trailing JSON values, and fields outside its versioned control type.
func jsonUnmarshal(raw []byte, target any) error {
	if len(raw) == 0 {
		return errors.New("payload is empty")
	}
	if err := jsondoc.Validate(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

// workerActor binds only an actual claimed attempt to its exact domain target.
func workerActor(ctx context.Context, job jobs.Job, target string) (context.Context, error) {
	if err := jobs.CheckClaimedAttempt(ctx, job); err != nil {
		return nil, err
	}
	ctx = identity.WithActor(ctx, identity.Actor{TenantID: job.TenantID, Subject: audit.PrincipalIDCatalogWorker, ActorType: identity.ActorService})
	return identity.WithInternalJobAuthority(ctx, job.Kind, job.TenantID, target, job.ID, job.LeaseOwner, job.Attempt, func(check context.Context) error { return jobs.CheckClaimedAttempt(check, job) })
}

// developmentIdentitySeeds translates only development tokens into deterministic
// membership seeds; an OIDC configuration never seeds roles from token claims.
func developmentIdentitySeeds(configuration config.Config) []identity.Seed {
	if configuration.Auth.Mode != "development" {
		return nil
	}
	seeds := make([]identity.Seed, 0, len(configuration.Auth.DevelopmentTokens))
	for _, user := range configuration.Auth.DevelopmentTokens {
		actorType := user.IdentityType()
		seeds = append(seeds, identity.Seed{TenantID: user.TenantID, Issuer: authn.DevelopmentIssuer, Subject: user.Subject, ActorType: actorType, Roles: append([]identity.Role(nil), user.Roles...), RoleTemplates: user.SeedRoleTemplates(), Namespaces: user.Namespaces})
	}
	sort.Slice(seeds, func(i, j int) bool {
		if seeds[i].TenantID != seeds[j].TenantID {
			return seeds[i].TenantID < seeds[j].TenantID
		}
		return seeds[i].Subject < seeds[j].Subject
	})
	return seeds
}

// firstPublicationManager selects a stable default by host, independent of map order.
func firstPublicationManager(managers map[string]*publication.Manager) *publication.Manager {
	if len(managers) == 0 {
		return nil
	}
	hosts := make([]string, 0, len(managers))
	for host := range managers {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return managers[hosts[0]]
}

// discoveryBaseURL substitutes a configured discovery host while retaining the
// deployment's scheme and explicit port, including bracketed IPv6 authorities.
func discoveryBaseURL(configuredBaseURL, host string) string {
	parsed, err := url.Parse(configuredBaseURL)
	if err != nil || parsed.Scheme == "" {
		return configuredBaseURL
	}
	authority := host
	if net.ParseIP(host) != nil && strings.Contains(host, ":") {
		authority = "[" + host + "]"
	}
	if parsed.Port() != "" {
		authority = net.JoinHostPort(host, parsed.Port())
	}
	return parsed.Scheme + "://" + authority
}

type publicationBinding struct {
	tenantID string
	manager  *publication.Manager
}

type publicationWithdrawalCoordinator struct {
	managers map[string]publicationBinding
}

// WithdrawPublicationDomain forwards a domain withdrawal and its originating cause
// to each configured publication manager in the tenant.
func (p *publicationWithdrawalCoordinator) WithdrawPublicationDomain(ctx context.Context, tenantID, domain, action, targetID, cause string) ([]string, error) {
	return p.withdraw(tenantID, func(manager *publication.Manager) (bool, error) {
		return manager.WithdrawDomain(ctx, tenantID, domain, publication.WithdrawalCause{Cause: cause, OriginatingAction: action, OriginatingTargetID: targetID})
	})
}

// WithdrawPublicationRelease forwards release withdrawal and its originating cause
// to each configured publication manager in the tenant.
func (p *publicationWithdrawalCoordinator) WithdrawPublicationRelease(ctx context.Context, tenantID, releaseID, action, targetID, cause string) ([]string, error) {
	return p.withdraw(tenantID, func(manager *publication.Manager) (bool, error) {
		return manager.WithdrawRelease(ctx, tenantID, releaseID, publication.WithdrawalCause{Cause: cause, OriginatingAction: action, OriginatingTargetID: targetID})
	})
}

// withdraw visits only tenant-owned host bindings in stable order and returns
// hosts whose active snapshot changed; it stops at the first failure.
func (p *publicationWithdrawalCoordinator) withdraw(tenantID string, operation func(*publication.Manager) (bool, error)) ([]string, error) {
	hosts := make([]string, 0, len(p.managers))
	for host, binding := range p.managers {
		if binding.tenantID == tenantID {
			hosts = append(hosts, host)
		}
	}
	sort.Strings(hosts)
	withdrawn := make([]string, 0, len(hosts))
	for _, host := range hosts {
		changed, err := operation(p.managers[host].manager)
		if err != nil {
			return nil, err
		}
		if changed {
			withdrawn = append(withdrawn, host)
		}
	}
	return withdrawn, nil
}
