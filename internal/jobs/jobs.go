// Package jobs implements durable, leased, retryable background work.
// [Service] records attributed enqueue, cancellation and retry commands; [Worker]
// executes registered handlers under renewable claims. Worker updates check
// tenant and lease ownership so interrupted work can be recovered without
// accepting stale completion. Caller-requested cancellation is tenant-scoped
// and does not require owning the worker's lease.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
	"golang.org/x/sync/errgroup"
)

// State records a job's scheduling or terminal lifecycle position.
type State string

const (
	StateQueued     State = "queued"
	StateRunning    State = "running"
	StateWaiting    State = "waiting"
	StateSucceeded  State = "succeeded"
	StateFailed     State = "failed"
	StateCancelled  State = "cancelled"
	StateDeadLetter State = "dead_letter"
)

// FailureClass classifies a handler failure for retry and terminal-state policy.
type FailureClass string

const (
	FailureInvalidInput      FailureClass = "invalid_input"
	FailurePolicyDenied      FailureClass = "policy_denied"
	FailureAuthentication    FailureClass = "authentication"
	FailureRateLimited       FailureClass = "rate_limited"
	FailureTransientUpstream FailureClass = "transient_upstream"
	FailurePermanentUpstream FailureClass = "permanent_upstream"
	FailureDependency        FailureClass = "dependency"
	FailureInternal          FailureClass = "internal"
	FailureCancelled         FailureClass = "cancelled"
)

// Job is the internal durable work record, including payload and lease state.
// Use [PublicView] for client responses rather than serializing Job directly.
type Job struct {
	// DefaultAttemptLimit is transient admission intent, never retained or disclosed.
	DefaultAttemptLimit   bool                 `json:"-"`
	CausalTrace           catalog.TraceContext `json:"-"`
	ID                    string               `json:"id"`
	TenantID              string               `json:"tenantId"`
	Kind                  string               `json:"kind"`
	Actor                 string               `json:"actor"`
	ParentJobID           string               `json:"parentJobId,omitempty"`
	PayloadVersion        int                  `json:"payloadVersion"`
	Payload               json.RawMessage      `json:"payload"`
	State                 State                `json:"state"`
	Priority              int                  `json:"priority"`
	Progress              int                  `json:"progress"`
	NotBefore             time.Time            `json:"notBefore"`
	Attempt               int                  `json:"attempt"`
	AttemptLimit          int                  `json:"attemptLimit"`
	LeaseOwner            string               `json:"leaseOwner,omitempty"`
	LeaseExpiresAt        *time.Time           `json:"leaseExpiresAt,omitempty"`
	CancellationRequested bool                 `json:"cancellationRequested"`
	DeduplicationKey      string               `json:"deduplicationKey"`
	Result                json.RawMessage      `json:"result,omitempty"`
	SafeError             string               `json:"safeError,omitempty"`
	FailureClass          FailureClass         `json:"failureClass,omitempty"`
	CreatedAt             time.Time            `json:"createdAt"`
	UpdatedAt             time.Time            `json:"updatedAt"`
}

// ListRequest selects a tenant's job page using a creation-time and ID cursor.
// ListTargets are composition-owned deployment inputs, not runtime authority.
// SQL uses them with retained targets before LIMIT; memory validates its target
// snapshot outside the Job mutex to avoid cross-store lock inversion.
type ListTargets struct {
	Enforced               bool
	PublicationHosts       []string
	DefaultPublicationHost string
	SemanticProfile        string
}

type ListRequest struct {
	Targets        ListTargets
	validateTarget func(context.Context, Job) error
	Kind           string
	State          State
	ParentJobID    string
	AfterCreatedAt *time.Time
	AfterID        string
	Limit          int
}

// ResultSummary exposes validated snapshot references or indexing completion
// facts from known Job kinds without disclosing full result documents.
type ResultSummary struct {
	Type         string `json:"type"`
	SnapshotID   string `json:"snapshotId,omitempty"`
	SourceID     string `json:"sourceId,omitempty"`
	Host         string `json:"host,omitempty"`
	Generation   int64  `json:"generation,omitempty"`
	State        string `json:"state,omitempty"`
	GenerationID string `json:"generationId,omitempty"`
	Members      *int64 `json:"members,omitempty"`
}

// SnapshotReference is the compact durable result of snapshot-producing Jobs.
// The owning publication or federation store retains the complete snapshot.
// Its field names also decode results retained as complete snapshots.
type SnapshotReference struct {
	ID         string `json:"id"`
	SourceID   string `json:"sourceId,omitempty"`
	Host       string `json:"host,omitempty"`
	Generation int64  `json:"generation,omitempty"`
	State      string `json:"state"`
}

// View is the bounded client-facing Job projection. Payloads, deduplication
// keys, lease ownership, and complete result documents remain internal.
type View struct {
	ID                    string         `json:"id"`
	TenantID              string         `json:"tenantId"`
	Kind                  string         `json:"kind"`
	ParentJobID           string         `json:"parentJobId,omitempty"`
	State                 State          `json:"state"`
	Priority              int            `json:"priority"`
	Progress              int            `json:"progress"`
	NotBefore             time.Time      `json:"notBefore"`
	Attempt               int            `json:"attempt"`
	AttemptLimit          int            `json:"attemptLimit"`
	CancellationRequested bool           `json:"cancellationRequested"`
	Result                *ResultSummary `json:"result,omitempty"`
	SafeError             string         `json:"safeError,omitempty"`
	FailureClass          FailureClass   `json:"failureClass,omitempty"`
	CreatedAt             time.Time      `json:"createdAt"`
	UpdatedAt             time.Time      `json:"updatedAt"`
}

// PublicView projects a Job into client-visible state without its private
// payload, claim owner or causal trace.
func PublicView(job Job) View {
	view := View{ID: job.ID, TenantID: job.TenantID, Kind: job.Kind, ParentJobID: job.ParentJobID, State: job.State, Priority: job.Priority, Progress: job.Progress, NotBefore: job.NotBefore, Attempt: job.Attempt, AttemptLimit: job.AttemptLimit, CancellationRequested: job.CancellationRequested, SafeError: job.SafeError, FailureClass: job.FailureClass, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt}
	if len(job.Result) == 0 || string(job.Result) == "null" {
		return view
	}
	if job.Kind == "semantic.reindex" {
		if job.State != StateSucceeded {
			return view
		}
		var result struct {
			Generation string `json:"generation"`
			Members    *int64 `json:"members"`
		}
		if json.Unmarshal(job.Result, &result) != nil || !identity.ValidTenantID(result.Generation) || result.Generation != job.ID || result.Members == nil || *result.Members < 0 {
			return view
		}
		view.Result = &ResultSummary{Type: "semantic_index", GenerationID: result.Generation, Members: result.Members}
		return view
	}
	var result SnapshotReference
	if json.Unmarshal(job.Result, &result) != nil || result.ID == "" || result.State == "" {
		return view
	}
	summary := ResultSummary{SnapshotID: result.ID, State: result.State}
	switch job.Kind {
	case "source.sync":
		if !identity.ValidTenantID(result.ID) || !identity.ValidTenantID(result.SourceID) || result.State != "complete" && result.State != "incomplete" && result.State != "failed" {
			return view
		}
		summary.Type = "source_snapshot"
		summary.SourceID = result.SourceID
	case "publication.generate":
		if !identity.ValidTenantID(result.ID) || result.Host == "" || len(result.Host) > 253 || result.Generation < 1 || result.State != "candidate" && result.State != "active" && result.State != "retired" {
			return view
		}
		summary.Type = "publication_snapshot"
		summary.Host = result.Host
		summary.Generation = result.Generation
	default:
		return view
	}
	view.Result = &summary
	return view
}

// Store owns durable job deduplication, leases, cancellation, and terminal
// transitions. Worker follow-up writes must check tenant, job and lease owner;
// cancellation requests are separate caller-authorized operations.
type Store interface {
	// Enqueue stores work and its supplied audit evidence, or returns an existing
	// job for the deduplication key. The boolean is true for a reused job.
	Enqueue(context.Context, Job, ...audit.Record) (Job, bool, error)
	// Get resolves a job by tenant ID and job ID.
	Get(context.Context, string, string) (Job, error)
	// GetView reads authorized metadata without loading execution payloads.
	GetView(context.Context, string, string) (View, error)
	// List retains execution inputs for internal maintenance admission.
	List(context.Context, string, ListRequest) ([]Job, error)
	// ListViews returns a tenant's metadata page without execution payloads.
	ListViews(context.Context, string, ListRequest) ([]View, error)
	// ReadJobOverview selects bounded state metadata and independently exact due time.
	ReadJobOverview(context.Context, string, ListRequest, time.Time) (OverviewRows, error)
	// Claim selects eligible work across tenants and assigns the supplied owner
	// a lease starting at the supplied time, including recovery of expired work.
	Claim(context.Context, string, time.Time, time.Duration) (Job, error)
	// Heartbeat renews an owned lease and records progress. Arguments identify
	// the tenant, job and owner, followed by current time, lease and progress.
	Heartbeat(context.Context, string, string, string, time.Time, time.Duration, int) (Job, error)
	// Complete records a result for the tenant, job and current lease owner.
	Complete(context.Context, string, string, string, time.Time, json.RawMessage, ...audit.Record) (Job, error)
	// Fail records an owned attempt's classified failure and applies retry policy.
	// The error text must already be safe for clients; the last time is retryAt.
	Fail(context.Context, string, string, string, time.Time, FailureClass, string, time.Time) (Job, error)
	// RequestCancellation records caller-requested cancellation and its audit
	// evidence by tenant and job ID; it does not forcibly interrupt a handler.
	RequestCancellation(context.Context, string, string, time.Time, ...audit.Record) (Job, error)
}

// EvidenceSink is the append-only audit inventory used by the in-memory
// composition. PostgreSQL stores insert evidence directly in the owning
// transaction and does not need this adapter.
type EvidenceSink interface {
	// AppendAuditEvent retains the supplied job-transition evidence.
	AppendAuditEvent(context.Context, audit.Record) error
}

// Clock supplies Now, the current instant used for job scheduling and evidence.
type Clock interface{ Now() time.Time }

// IDGenerator allocates job and audit identifiers from the supplied event time.
type IDGenerator interface {
	// New returns an identifier or an allocation error without changing job state.
	New(time.Time) (string, error)
}

// Service authorizes and validates operator-facing job commands before they
// cross the durable Store boundary.
type Service struct {
	enqueueValidation func(context.Context, Job) error
	targetValidation  func(context.Context, Job) error
	listTargets       func(string) ListTargets
	store             Store
	clock             Clock
	ids               IDGenerator
	auditEvidence     bool
	maxAttempts       int
}

// ServiceOption configures a job service during construction.
type ServiceOption func(*Service) error

// WithTargetValidation binds user-visible Job operations to their existing
// tenant source/configured host. It performs no execution or remote fetching.
func WithTargetValidation(validate func(context.Context, Job) error) ServiceOption {
	return func(service *Service) error {
		if validate == nil {
			return errors.New("Job target validation is required")
		}
		service.targetValidation = validate
		return nil
	}
}

// WithEnqueueValidation checks execution eligibility for new and retried work.
// TargetValidation remains disclosure-only so stale work can still be cancelled.
func WithEnqueueValidation(validate func(context.Context, Job) error) ServiceOption {
	return func(s *Service) error {
		if validate == nil {
			return errors.New("Job enqueue validation is required")
		}
		s.enqueueValidation = validate
		return nil
	}
}

// WithListTargets binds public collection selection to configured deployments.
func WithListTargets(targets func(string) ListTargets) ServiceOption {
	return func(service *Service) error {
		if targets == nil {
			return errors.New("Job list targets are required")
		}
		service.listTargets = targets
		return nil
	}
}

// WithAuditEvidence enables construction of the reviewed operator and manual
// synchronization audit records. Runtime composition enables it for both
// durable and in-memory deployments.
func WithAuditEvidence() ServiceOption {
	return func(service *Service) error {
		service.auditEvidence = true
		return nil
	}
}

// WithMaxAttempts sets the creation default, including the initial attempt.
// Explicit internal limits and retained retry-parent limits remain authoritative.
func WithMaxAttempts(limit int) ServiceOption {
	return func(service *Service) error {
		if limit < 1 || limit > 20 {
			return errors.New("job attempt limit must be between one and twenty")
		}
		service.maxAttempts = limit
		return nil
	}
}

// NewService validates the Job store, clock, ID generator and explicitly
// supplied service options.
func NewService(store Store, clock Clock, ids IDGenerator, options ...ServiceOption) (*Service, error) {
	if store == nil || clock == nil || ids == nil {
		return nil, errors.New("job service dependencies are required")
	}
	service := &Service{store: store, clock: clock, ids: ids, maxAttempts: 5}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("job service option is required")
		}
		if err := option(service); err != nil {
			return nil, err
		}
	}
	return service, nil
}

// SourceSyncRequestAudit binds manual synchronization evidence to the source
// revision and trigger that were authorized before enqueueing.
type SourceSyncRequestAudit struct {
	SourceID       string
	SourceRevision int64
	Trigger        string
}

// EnqueueInput describes work before the service supplies identity, timestamps
// and scheduling defaults. SourceSyncAudit is internal evidence, not JSON input.
type EnqueueInput struct {
	SemanticReason   string                  `json:"-"`
	Kind             string                  `json:"kind"`
	PayloadVersion   int                     `json:"payloadVersion,omitempty"`
	Payload          json.RawMessage         `json:"payload"`
	Priority         int                     `json:"priority,omitempty"`
	AttemptLimit     int                     `json:"attemptLimit,omitempty"`
	DeduplicationKey string                  `json:"deduplicationKey"`
	NotBefore        *time.Time              `json:"notBefore,omitempty"`
	SourceSyncAudit  *SourceSyncRequestAudit `json:"-"`
	TrustReason      string                  `json:"-"`
}

// Enqueue validates work and retains the initiating actor. Its boolean result
// is true when the store reuses an existing live deduplication match.
func (s *Service) Enqueue(ctx context.Context, input EnqueueInput) (result Job, created bool, resultErr error) {
	ctx = identity.WithPermissionContext(ctx)
	ctx, endTrace := catalog.StartTrace(ctx, "job enqueue", catalog.TraceProducer, "")
	defer func() {
		if resultErr != nil {
			endTrace("error")
		} else {
			endTrace("ok")
		}
	}()
	actor, ok := identity.FromContext(ctx)
	if !ok || actor.TenantID == "" {
		return Job{}, false, catalog.E(catalog.CodeForbidden, "authenticated actor is required")
	}
	if input.Kind == "" || len(input.Kind) > 100 || input.DeduplicationKey == "" || len(input.DeduplicationKey) > 256 || !json.Valid(input.Payload) {
		return Job{}, false, catalog.E(catalog.CodeInvalidArgument, "job kind, valid payload, and deduplication key are required")
	}
	if err := requireJobPermission(ctx, input.Kind, "enqueue"); err != nil {
		return Job{}, false, err
	}
	if input.PayloadVersion <= 0 {
		input.PayloadVersion = 1
	}
	useDefaultAttemptLimit := input.AttemptLimit <= 0
	if useDefaultAttemptLimit {
		input.AttemptLimit = s.maxAttempts
	}
	if input.AttemptLimit > 20 || input.Priority < -100 || input.Priority > 100 {
		return Job{}, false, catalog.E(catalog.CodeInvalidArgument, "job attempt limit or priority is invalid")
	}
	now := s.clock.Now()
	idValue, err := s.ids.New(now)
	if err != nil {
		return Job{}, false, err
	}
	notBefore := now
	if input.NotBefore != nil && input.NotBefore.After(now) {
		notBefore = input.NotBefore.UTC()
	}
	initiator := actor.Subject
	if identity.ValidTenantID(actor.MembershipID) {
		initiator = actor.MembershipID
	}
	job := Job{DefaultAttemptLimit: useDefaultAttemptLimit, ID: idValue, TenantID: actor.TenantID, Kind: input.Kind, Actor: initiator, PayloadVersion: input.PayloadVersion, Payload: append(json.RawMessage(nil), input.Payload...), State: StateQueued, Priority: input.Priority, NotBefore: notBefore, AttemptLimit: input.AttemptLimit, DeduplicationKey: input.DeduplicationKey, CreatedAt: now, UpdatedAt: now}
	if s.targetValidation != nil {
		if err := s.targetValidation(ctx, job); err != nil {
			return Job{}, false, err
		}
	}
	if s.enqueueValidation != nil {
		if err := s.enqueueValidation(ctx, job); err != nil {
			return Job{}, false, err
		}
	}
	var evidence []audit.Record
	if input.Kind == "semantic.reindex" {
		if _, err := identity.RequirePermission(ctx, "operations.reindex", ""); err != nil {
			return Job{}, false, catalog.E(catalog.CodeForbidden, "tenant_admin role is required")
		}
		reason, err := audit.NormalizeReason(audit.ActionSemanticReindexRequested, input.SemanticReason)
		if err != nil {
			return Job{}, false, catalog.Wrap(catalog.CodeInvalidArgument, "semantic reindex reason is invalid", err)
		}
		payload, payloadErr := catalog.DecodeSemanticReindex(input.Payload, input.PayloadVersion)
		if payloadErr != nil || input.DeduplicationKey != payload.Profile+":"+payload.Scope {
			return Job{}, false, catalog.E(catalog.CodeInvalidArgument, "invalid semantic reindex payload")
		}
		if s.auditEvidence {
			record, err := s.newAuditRecord(ctx, actor, audit.ActionSemanticReindexRequested, "job", job.ID, reason, semanticReindexFacts{Profile: payload.Profile, Scope: payload.Scope, JobID: job.ID}, now)
			if err != nil {
				return Job{}, false, err
			}
			evidence = append(evidence, record)
		}
	}
	if input.Kind == TrustKind {
		payload, err := DecodeTrustPayload(job)
		if err != nil {
			return Job{}, false, err
		}
		if s.auditEvidence {
			record, err := s.newAuditRecord(ctx, actor, audit.ActionSourceTrustRequested, "job", job.ID, input.TrustReason, map[string]any{"sourceId": payload.SourceID, "sourceRevision": payload.SourceRevision, "entryDigest": payload.ExactEntryDigest, "policyDigest": payload.PolicyDigest, "jobId": job.ID, "deduplicated": false}, now)
			if err != nil {
				return Job{}, false, err
			}
			evidence = append(evidence, record)
		}
	}
	if input.SourceSyncAudit != nil {
		if input.Kind != "source.sync" || !identity.ValidTenantID(input.SourceSyncAudit.SourceID) || input.SourceSyncAudit.SourceRevision < 1 || input.SourceSyncAudit.Trigger != "manual" {
			return Job{}, false, catalog.E(catalog.CodeInvalidArgument, "source synchronization audit attribution is invalid")
		}
		if s.auditEvidence {
			record, recordErr := s.newAuditRecord(ctx, actor, audit.ActionSourceSyncRequested, "federation_source", input.SourceSyncAudit.SourceID, "", sourceSyncRequestFacts{SourceRevision: input.SourceSyncAudit.SourceRevision, JobID: job.ID, Trigger: input.SourceSyncAudit.Trigger, Deduplicated: false}, now)
			if recordErr != nil {
				return Job{}, false, recordErr
			}
			record.AfterDigest, recordErr = jobStateDigest(job)
			if recordErr != nil {
				return Job{}, false, catalog.Wrap(catalog.CodeInternal, "digest enqueued Job state", recordErr)
			}
			evidence = append(evidence, record)
		}
	}
	stored, reused, err := s.store.Enqueue(ctx, job, evidence...)
	if err == nil {
		identity.RecordJobDisclosure(ctx, stored.ID)
	}
	return stored, reused, err
}

// Get requires reader access and returns metadata only from the actor's tenant.
func (s *Service) Get(ctx context.Context, jobID string) (View, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := identity.RequirePermission(ctx, "job.read", "")
	if err != nil {
		return View{}, catalog.PermissionError("current Job permission is required", err)
	}
	view, err := s.store.GetView(ctx, actor.TenantID, jobID)
	if err != nil {
		return View{}, err
	}
	if err := requireJobPermission(ctx, view.Kind, "read"); err != nil {
		return View{}, err
	}
	if s.targetValidation != nil {
		job, err := s.store.Get(ctx, actor.TenantID, jobID)
		if err != nil {
			return View{}, err
		}
		if err := s.targetValidation(ctx, job); err != nil {
			return View{}, err
		}
	}
	return s.store.GetView(ctx, actor.TenantID, jobID)
}

// List validates the reader's page request and selects one extra row for
// continuation detection.
func (s *Service) List(ctx context.Context, request ListRequest) ([]View, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := identity.RequirePermission(ctx, "job.read", "")
	if err != nil {
		return nil, catalog.PermissionError("current Job permission is required", err)
	}
	ctx, _, err = identity.WithCollectionContext(ctx)
	if err != nil {
		return nil, catalog.PermissionError("Job collection admission failed", err)
	}
	if request.Limit < 1 || request.Limit > 100 || request.AfterCreatedAt == nil && request.AfterID != "" || request.AfterCreatedAt != nil && !identity.ValidTenantID(request.AfterID) {
		return nil, catalog.E(catalog.CodeInvalidArgument, "job page position or limit is invalid")
	}
	if err := validateListFilters(request); err != nil {
		return nil, err
	}
	request.Limit++
	request.validateTarget = s.targetValidation
	if s.listTargets != nil {
		request.Targets = s.listTargets(actor.TenantID)
	}
	return s.store.ListViews(ctx, actor.TenantID, request)
}

// Cancel applies the kind-specific management role and reasoned cancellation
// request; terminal Jobs retain an audited no-op.
func (s *Service) Cancel(ctx context.Context, jobID, reason string) (Job, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, ok := identity.FromContext(ctx)
	if !ok {
		return Job{}, catalog.E(catalog.CodeForbidden, "authenticated actor is required")
	}
	original, err := s.store.Get(ctx, actor.TenantID, jobID)
	if err != nil {
		return Job{}, err
	}
	if err := requireJobPermission(ctx, original.Kind, "cancel"); err != nil {
		return Job{}, err
	}
	if s.targetValidation != nil {
		if err := s.targetValidation(ctx, original); err != nil {
			return Job{}, err
		}
	}
	reason, err = audit.NormalizeReason(audit.ActionJobCancellationRequested, reason)
	if err != nil {
		return Job{}, catalog.Wrap(catalog.CodeInvalidArgument, "job cancellation reason is invalid", err)
	}
	var evidence []audit.Record
	if s.auditEvidence {
		anticipated := original
		switch original.State {
		case StateQueued, StateWaiting:
			anticipated.State = StateCancelled
		case StateRunning:
			anticipated.CancellationRequested = true
		case StateSucceeded, StateFailed, StateCancelled, StateDeadLetter:
			// A terminal cancellation is an intentional audited no-op.
		default:
			return Job{}, catalog.E(catalog.CodeInternal, "stored job state is invalid")
		}
		record, recordErr := s.newAuditRecord(ctx, actor, audit.ActionJobCancellationRequested, "job", original.ID, reason, cancellationFacts(original, anticipated), s.clock.Now())
		if recordErr != nil {
			return Job{}, recordErr
		}
		record.BeforeDigest, recordErr = jobStateDigest(original)
		if recordErr != nil {
			return Job{}, catalog.Wrap(catalog.CodeInternal, "digest Job state before cancellation", recordErr)
		}
		record.AfterDigest, recordErr = jobStateDigest(anticipated)
		if recordErr != nil {
			return Job{}, catalog.Wrap(catalog.CodeInternal, "digest anticipated Job cancellation state", recordErr)
		}
		evidence = append(evidence, record)
	}
	identity.RecordJobDisclosure(ctx, original.ID)
	return s.store.RequestCancellation(ctx, actor.TenantID, jobID, s.clock.Now(), evidence...)
}

// Retry creates a new child Job from an authorized failed or dead-letter Job,
// preserving the original terminal history.
func (s *Service) Retry(ctx context.Context, jobID, reason string) (Job, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, ok := identity.FromContext(ctx)
	if !ok {
		return Job{}, catalog.E(catalog.CodeForbidden, "authenticated actor is required")
	}
	original, err := s.store.Get(ctx, actor.TenantID, jobID)
	if err != nil {
		return Job{}, err
	}
	if err := requireJobPermission(ctx, original.Kind, "retry"); err != nil {
		return Job{}, err
	}
	if s.targetValidation != nil {
		if err := s.targetValidation(ctx, original); err != nil {
			return Job{}, err
		}
	}
	if original.State != StateFailed && original.State != StateDeadLetter {
		return Job{}, catalog.E(catalog.CodeInvalidTransition, "only a failed or dead-letter job can be retried")
	}
	reason, err = audit.NormalizeReason(audit.ActionJobRetried, reason)
	if err != nil {
		return Job{}, catalog.Wrap(catalog.CodeInvalidArgument, "job retry reason is invalid", err)
	}
	now := s.clock.Now()
	newID, err := s.ids.New(now)
	if err != nil {
		return Job{}, catalog.Wrap(catalog.CodeInternal, "create retry job identifier", err)
	}
	initiator := actor.Subject
	if identity.ValidTenantID(actor.MembershipID) {
		initiator = actor.MembershipID
	}
	retry := Job{ID: newID, TenantID: actor.TenantID, Kind: original.Kind, Actor: initiator, ParentJobID: original.ID, PayloadVersion: original.PayloadVersion, Payload: append(json.RawMessage(nil), original.Payload...), State: StateQueued, Priority: original.Priority, NotBefore: now, AttemptLimit: original.AttemptLimit, DeduplicationKey: "retry:" + original.ID + ":" + newID, CreatedAt: now, UpdatedAt: now}
	if s.targetValidation != nil {
		if err := s.targetValidation(ctx, retry); err != nil {
			return Job{}, err
		}
	}
	if s.enqueueValidation != nil {
		if err := s.enqueueValidation(ctx, retry); err != nil {
			return Job{}, err
		}
	}
	var evidence []audit.Record
	if s.auditEvidence {
		record, recordErr := s.newAuditRecord(ctx, actor, audit.ActionJobRetried, "job", original.ID, reason, jobRetryFacts{Kind: original.Kind, BeforeState: original.State, AfterState: retry.State, ParentJobID: original.ID, ReplacementJobID: retry.ID, FailureClass: original.FailureClass}, now)
		if recordErr != nil {
			return Job{}, recordErr
		}
		record.BeforeDigest, recordErr = jobStateDigest(original)
		if recordErr != nil {
			return Job{}, catalog.Wrap(catalog.CodeInternal, "digest Job state before retry", recordErr)
		}
		record.AfterDigest, recordErr = jobStateDigest(retry)
		if recordErr != nil {
			return Job{}, catalog.Wrap(catalog.CodeInternal, "digest replacement Job state", recordErr)
		}
		evidence = append(evidence, record)
	}
	created, _, err := s.store.Enqueue(ctx, retry, evidence...)
	if err == nil {
		identity.RecordJobDisclosure(ctx, created.ID)
	}
	return created, err
}

// requireJobPermission keeps Job controls and owner-family authority separate.
// Unknown kinds have no user-management authority, including for administrators.
func requireJobPermission(ctx context.Context, kind, operation string) error {
	var family identity.Permission
	switch kind {
	case TrustKind:
		family = "source.read_evidence"
		if operation != "read" {
			if _, err := identity.RequirePermission(ctx, "source.manage", ""); err != nil {
				return catalog.PermissionError("current source management permission is required", err)
			}
			if _, err := identity.RequirePermission(ctx, "federation.discover", ""); err != nil {
				return catalog.PermissionError("current discovery permission is required", err)
			}
		}
	case "source.sync":
		family = "source.read"
		if operation == "enqueue" {
			family = "source.sync"
		} else if operation != "read" {
			family = "source.manage"
		}
	case "publication.generate":
		family = "publication.read"
		if operation != "read" {
			family = "publication.generate"
		}
	case "semantic.reindex":
		family = "operations.reindex"
	default:
		return catalog.E(catalog.CodeForbidden, "Job kind has no user authority")
	}
	if _, err := identity.RequirePermission(ctx, family, ""); err != nil {
		return catalog.PermissionError("current Job family permission is required", err)
	}
	if operation != "enqueue" {
		if _, err := identity.RequirePermission(ctx, identity.Permission("job."+operation), ""); err != nil {
			return catalog.PermissionError("current Job control permission is required", err)
		}
	}
	return nil
}

// Handler runs one leased attempt. It must honor cancellation and propagate
// progress-callback failures; the callback records bounded percentage progress.
// Work may run again after interruption, so side effects must be retry-safe.
type Handler func(context.Context, Job, func(int) error) (any, error)

// AttemptResult describes an attempt without private Job data or error text.
// Committed is true only after the final state was successfully persisted.
type AttemptResult struct {
	Outcome      string
	FailureClass FailureClass
	Committed    bool
}

// Observer receives the Job kind, attempt classification and retry delay.
// The completion context permits active trace correlation only.
// Implementations must not retain Job payloads, actors, tenants, or errors.
type Observer interface {
	// JobStarted records an attempt for a bounded job kind.
	JobStarted(string)
	// JobFinished records the kind and fixed outcome of a completed attempt.
	JobFinished(context.Context, string, AttemptResult)
	// JobRetryScheduled records a persisted retry delay from attempt completion.
	JobRetryScheduled(string, time.Duration)
}

// HandlerError separates retry classification and safe retained text from an
// internal cause. Error() may expose the cause and is not a public projection.
type HandlerError struct {
	Class FailureClass
	Safe  string
	// RetryAfter is an upstream minimum, accepted through 24 hours for retryable failures.
	RetryAfter time.Duration
	Cause      error
}

// Error returns the diagnostic cause when present, otherwise the safe handler
// message.
func (e *HandlerError) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return e.Safe
}

// Worker claims durable jobs and keeps each lease alive while one registered
// handler runs. A lost lease cancels the handler and prevents stale completion.
type Worker struct {
	store         Store
	clock         Clock
	owner         string
	leaseDuration time.Duration
	pollInterval  time.Duration
	retryCeiling  time.Duration
	handlers      map[string]Handler
	observer      Observer
}

// NewWorker validates claim ownership and positive timing with at most one
// observer; handlers are keyed by Job kind.
func NewWorker(store Store, clock Clock, owner string, leaseDuration, pollInterval, retryCeiling time.Duration, handlers map[string]Handler, observers ...Observer) (*Worker, error) {
	if store == nil || clock == nil || owner == "" || leaseDuration <= 0 || pollInterval <= 0 {
		return nil, errors.New("worker dependencies and timing are required")
	}
	if retryCeiling < 5*time.Second || retryCeiling > time.Hour {
		return nil, errors.New("job retry ceiling must be between five seconds and one hour")
	}
	if len(observers) > 1 {
		return nil, errors.New("at most one Job observer is supported")
	}
	var observer Observer
	if len(observers) == 1 {
		observer = observers[0]
	}
	return &Worker{store: store, clock: clock, owner: owner, leaseDuration: leaseDuration, pollInterval: pollInterval, retryCeiling: retryCeiling, handlers: handlers, observer: observer}, nil
}

// RunConcurrent starts a fixed number of claim loops under one cancellation
// scope; the first loop error stops its siblings.
func (w *Worker) RunConcurrent(ctx context.Context, concurrency int) error {
	if concurrency < 1 || concurrency > 128 {
		return errors.New("worker concurrency must be between one and one hundred twenty-eight")
	}
	var panicked atomic.Bool
	group, groupContext := errgroup.WithContext(ctx)
	for range concurrency {
		group.Go(func() (err error) {
			defer func() {
				if errors.Is(err, safeerr.ErrPanic) {
					panicked.Store(true)
				}
			}()
			defer safeerr.Recover(&err)
			return w.runLoop(groupContext)
		})
	}
	err := group.Wait()
	if panicked.Load() {
		return errors.Join(safeerr.ErrPanic, err)
	}
	return err
}

// runLoop attempts one Job at each polling interval and returns the first
// execution or cancellation error.
func (w *Worker) runLoop(ctx context.Context) error {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		if err := w.RunOnce(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// RunOnce claims at most one job, supervises its heartbeat and cancellation,
// and finalizes an attempt only while this worker still owns the lease.
func (w *Worker) RunOnce(ctx context.Context) (resultErr error) {
	defer safeerr.Recover(&resultErr)
	now := w.clock.Now()
	job, err := w.store.Claim(ctx, w.owner, now, w.leaseDuration)
	if err != nil {
		if catalog.ErrorCodeOf(err) == catalog.CodeNotFound {
			return nil
		}
		return err
	}
	result := AttemptResult{Outcome: "failed"}
	ctx, endTrace := catalog.StartTrace(ctx, "job process", catalog.TraceAttempt, job.CausalTrace)
	defer func() { endTrace(result.Outcome) }()
	if w.observer != nil {
		w.observer.JobStarted(job.Kind)
		defer func() { w.observer.JobFinished(ctx, job.Kind, result) }()
	}
	handler, ok := w.handlers[job.Kind]
	if !ok {
		updated, err := w.store.Fail(ctx, job.TenantID, job.ID, w.owner, w.clock.Now(), FailureInvalidInput, "This worker version does not support the queued job kind.", time.Time{})
		if err != nil {
			result.Outcome = "dependency_error"
		} else {
			result = AttemptResult{Outcome: "unsupported", FailureClass: FailureInvalidInput, Committed: true}
			if updated.State == StateCancelled {
				result.Outcome = "cancelled"
			}
		}
		return err
	}
	handlerContext, cancelHandler := context.WithCancel(ctx)
	defer cancelHandler()
	claim := &claimedAuthority{job: job, verify: func(check context.Context) error {
		current, err := w.store.Get(check, job.TenantID, job.ID)
		if err != nil {
			return err
		}
		if current.State != StateRunning || current.LeaseOwner != w.owner || current.Attempt != job.Attempt || current.LeaseExpiresAt == nil || !current.LeaseExpiresAt.After(w.clock.Now()) {
			return catalog.E(catalog.CodeForbidden, "claimed Job lease is no longer current")
		}
		if current.CancellationRequested {
			return context.Canceled
		}
		return nil
	}}
	claim.active.Store(true)
	defer claim.active.Store(false)
	handlerContext = context.WithValue(handlerContext, claimedAuthorityKey{}, claim)
	initiator := ""
	if identity.ValidTenantID(job.Actor) || job.Actor == audit.InitiatorSourceMaintainer {
		initiator = job.Actor
	}
	handlerContext = audit.WithJobExecution(handlerContext, audit.JobExecution{ID: job.ID, Kind: job.Kind, Attempt: job.Attempt, Initiator: initiator})
	heartbeatContext, stopHeartbeats := context.WithCancel(ctx)
	var progressValue atomic.Int64
	progressValue.Store(int64(job.Progress))
	var cancellationRequested atomic.Bool
	var heartbeatMu sync.Mutex
	heartbeat := func(callContext context.Context) error {
		callContext = catalog.WithoutTracing(callContext)
		heartbeatMu.Lock()
		defer heartbeatMu.Unlock()
		updated, heartbeatErr := w.store.Heartbeat(callContext, job.TenantID, job.ID, w.owner, w.clock.Now(), w.leaseDuration, int(progressValue.Load()))
		if heartbeatErr != nil {
			return heartbeatErr
		}
		if updated.CancellationRequested {
			cancellationRequested.Store(true)
			cancelHandler()
			return context.Canceled
		}
		return nil
	}
	heartbeatDone := make(chan error, 1)
	go func() {
		var result error
		defer func() {
			if result != nil {
				cancelHandler()
			}
			heartbeatDone <- result
		}()
		defer safeerr.Recover(&result)
		interval := w.leaseDuration / 3
		if interval <= 0 {
			interval = w.leaseDuration
		}
		if interval > 5*time.Second {
			interval = 5 * time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatContext.Done():
				result = nil
				return
			case <-ticker.C:
				if heartbeatErr := heartbeat(heartbeatContext); heartbeatErr != nil {
					// Stopping an in-flight heartbeat can return a driver error
					// that does not wrap context.Canceled. Completion still
					// checks the retained lease; fatal failures stay fatal.
					if !errors.Is(heartbeatErr, safeerr.ErrPanic) && (cancellationRequested.Load() || heartbeatContext.Err() != nil) {
						result = nil
						return
					}
					cancelHandler()
					result = heartbeatErr
					return
				}
			}
		}
	}()
	var heartbeatErr error
	joinHeartbeat := sync.OnceFunc(func() {
		stopHeartbeats()
		heartbeatErr = <-heartbeatDone
	})
	defer func() {
		cancelHandler()
		joinHeartbeat()
		if errors.Is(heartbeatErr, safeerr.ErrPanic) {
			resultErr = errors.Join(resultErr, heartbeatErr)
		}
	}()
	progress := func(value int) error {
		if value < 0 || value > 100 {
			return errors.New("job progress must be between zero and one hundred")
		}
		progressValue.Store(int64(value))
		return heartbeat(handlerContext)
	}
	value, handlerErr := handler(handlerContext, job, progress)
	joinHeartbeat()
	if errors.Is(heartbeatErr, safeerr.ErrPanic) || errors.Is(handlerErr, safeerr.ErrPanic) {
		result.Outcome = "error"
		return errors.Join(heartbeatErr, handlerErr)
	}
	if ctx.Err() != nil {
		result.Outcome = "cancelled"
		return ctx.Err()
	}
	if heartbeatErr != nil {
		result.Outcome = "dependency_error"
		return heartbeatErr
	}
	if cancellationRequested.Load() {
		handlerErr = context.Canceled
	}
	if handlerErr == nil {
		raw, err := json.Marshal(value)
		if err != nil {
			handlerErr = &HandlerError{Class: FailureInternal, Safe: "Job result could not be serialized.", Cause: err}
		} else {
			var evidence []audit.Record
			if attributed, ok := value.(CompletionEvidence); ok {
				evidence = append(evidence, attributed.JobCompletionEvidence())
			}
			_, err = w.store.Complete(ctx, job.TenantID, job.ID, w.owner, w.clock.Now(), raw, evidence...)
			if err == nil {
				result = AttemptResult{Outcome: "succeeded", Committed: true}
			} else {
				result.Outcome = "dependency_error"
			}
			return err
		}
	}
	class := FailureInternal
	safe := "The job failed. Inspect its attempt evidence for more information."
	retryAt := time.Time{}
	var hint time.Duration
	var typed *HandlerError
	if errors.As(handlerErr, &typed) {
		class = typed.Class
		if typed.Safe != "" {
			safe = typed.Safe
		}
		hint = typed.RetryAfter
	}
	if errors.Is(handlerErr, context.Canceled) {
		class = FailureCancelled
		safe = "Job cancellation was acknowledged."
	}
	completedAt := w.clock.Now()
	if retryable(class) {
		retryAt = completedAt.Add(backoff(job.Attempt, w.retryCeiling, hint))
	}
	updated, err := w.store.Fail(ctx, job.TenantID, job.ID, w.owner, completedAt, class, safe, retryAt)
	if err != nil {
		result.Outcome = "dependency_error"
	} else {
		result = AttemptResult{Outcome: string(updated.State), FailureClass: class, Committed: true}
		if w.observer != nil && updated.State == StateWaiting {
			w.observer.JobRetryScheduled(job.Kind, updated.NotBefore.Sub(updated.UpdatedAt))
		}
	}
	return err
}

// retryable reports whether a classified failure permits durable retry.
func retryable(class FailureClass) bool {
	return class == FailureRateLimited || class == FailureTransientUpstream || class == FailureDependency || class == FailureInternal
}

// backoff adds full jitter to a bounded upstream minimum. Cap the window before
// sampling so the total stays within 24 hours; use its midpoint if entropy fails.
func backoff(attempt int, ceiling, hint time.Duration) time.Duration {
	if hint < 0 || hint > 24*time.Hour {
		hint = 0
	}
	base := min(backoffCeiling(attempt), ceiling, 24*time.Hour-hint)
	value, err := rand.Int(rand.Reader, big.NewInt(int64(base)+1))
	if err != nil {
		return hint + base/2
	}
	return hint + time.Duration(value.Int64())
}

// backoffCeiling saturates the exponential window before multiplication can
// overflow. Expired-lease reclaims can take attempts beyond the retry limit.
func backoffCeiling(attempt int) time.Duration {
	delay := 5 * time.Second
	for attempt > 1 {
		if delay >= time.Hour/3 {
			return time.Hour
		}
		delay *= 3
		attempt--
	}
	return delay
}

// MemoryStore provides mutex-protected, process-local job state for development
// and tests. It does not retain work across process restarts.
type MemoryStore struct {
	mu       sync.Mutex
	jobs     map[string]Job
	evidence EvidenceSink
}

// NewMemoryStore creates an empty development Job store and attaches an
// evidence sink only when exactly one is supplied.
func NewMemoryStore(evidence ...EvidenceSink) *MemoryStore {
	store := &MemoryStore{jobs: make(map[string]Job)}
	if len(evidence) == 1 {
		store.evidence = evidence[0]
	}
	return store
}

// Enqueue verifies retry-parent eligibility and deduplicates live work by
// tenant, kind and key. The boolean reports reuse; failed evidence restores
// prior state.
func (m *MemoryStore) Enqueue(ctx context.Context, job Job, evidence ...audit.Record) (Job, bool, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Job
		var admitted1 bool
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, admitted1, operationErr = m.Enqueue(ctx, job, evidence...)
			return operationErr
		})
		return admitted0, admitted1, err
	}

	job.CausalTrace = catalog.CausalTrace(ctx)
	job.DefaultAttemptLimit = false
	m.mu.Lock()
	defer m.mu.Unlock()
	if job.ParentJobID != "" {
		parent, exists := m.jobs[job.ParentJobID]
		if !exists || parent.TenantID != job.TenantID {
			return Job{}, false, catalog.E(catalog.CodeNotFound, "retry parent job was not found")
		}
		if parent.State != StateFailed && parent.State != StateDeadLetter {
			return Job{}, false, catalog.E(catalog.CodeInvalidTransition, "only a failed or dead-letter job can be retried")
		}
	}
	for _, existing := range m.jobs {
		if existing.TenantID == job.TenantID && existing.Kind == job.Kind && existing.DeduplicationKey == job.DeduplicationKey && (existing.State == StateQueued || existing.State == StateRunning || existing.State == StateWaiting) {
			if err := m.appendEnqueueEvidence(ctx, evidence, existing, true); err != nil {
				return Job{}, false, err
			}
			return cloneJob(existing), true, nil
		}
	}
	m.jobs[job.ID] = cloneJob(job)
	if err := m.appendEnqueueEvidence(ctx, evidence, job, false); err != nil {
		delete(m.jobs, job.ID)
		return Job{}, false, err
	}
	return cloneJob(job), false, nil
}

// Get returns an isolated copy of the Job only when its tenant matches.
func (m *MemoryStore) Get(ctx context.Context, tenantID, jobID string) (Job, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Job
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.Get(ctx, tenantID, jobID)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[jobID]
	if !ok || job.TenantID != tenantID {
		return Job{}, catalog.E(catalog.CodeNotFound, "job was not found")
	}
	return cloneJob(job), nil
}

// List returns the requested tenant's Jobs in descending creation-time and ID
// order after the supplied position.
func (m *MemoryStore) List(ctx context.Context, tenantID string, request ListRequest) ([]Job, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 []Job
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.List(ctx, tenantID, request)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	items := m.listLocked(tenantID, request)
	result := make([]Job, len(items))
	for index, job := range items {
		result[index] = cloneJob(job)
	}
	return result, nil
}

// ListViews projects the selected page without cloning execution bytes.
func (m *MemoryStore) ListViews(ctx context.Context, tenantID string, request ListRequest) ([]View, error) {
	if err := identity.RequireCollectionTenant(ctx, tenantID); err != nil {
		return nil, err
	}
	if identity.NeedsReadAdmission(ctx) {
		return identity.WithReadAdmission(ctx, func(ctx context.Context) ([]View, error) { return m.ListViews(ctx, tenantID, request) })
	}

	items, err := m.visibleJobSnapshot(ctx, tenantID, request)
	if err != nil {
		return nil, err
	}

	if request.validateTarget != nil {
		visible := make([]Job, 0, len(items))
		for _, item := range items {
			if err := request.validateTarget(ctx, item); err != nil {
				code := catalog.ErrorCodeOf(err)
				if code == catalog.CodeForbidden || code == catalog.CodeNotFound || code == catalog.CodeInvalidArgument {
					continue
				}
				return nil, err
			}
			visible = append(visible, item)
			if len(visible) == request.Limit {
				break
			}
		}
		items = visible
	}
	views := make([]View, len(items))
	for index, job := range items {
		views[index] = PublicView(job)
	}
	return views, nil
}

// visibleJobSnapshot releases its mutex before cross-store target validation.
// Retained memory input is cloned under lock; panic paths cannot leak the lock.
func (m *MemoryStore) visibleJobSnapshot(ctx context.Context, tenantID string, request ListRequest) ([]Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	selection := request
	if request.validateTarget != nil {
		selection.Limit = len(m.jobs)
	}
	items, err := m.listVisibleLocked(ctx, tenantID, selection)
	if err != nil {
		return nil, err
	}
	if request.validateTarget != nil {
		for i := range items {
			items[i] = cloneJob(items[i])
		}
	}
	return items, nil
}

func (m *MemoryStore) listLocked(tenantID string, request ListRequest) []Job {
	items := make([]Job, 0)
	for _, job := range m.jobs {
		beforeCursor := request.AfterCreatedAt == nil || job.CreatedAt.Before(*request.AfterCreatedAt) || job.CreatedAt.Equal(*request.AfterCreatedAt) && job.ID < request.AfterID
		if job.TenantID == tenantID && beforeCursor && matchesListFilters(job, request) {
			items = append(items, job)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.After(items[j].CreatedAt)
		}
		return items[i].ID > items[j].ID
	})
	if len(items) > request.Limit {
		items = items[:request.Limit]
	}
	return items
}

// GetView projects metadata under the lock without cloning retained bytes.
func (m *MemoryStore) GetView(ctx context.Context, tenantID, jobID string) (View, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 View
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.GetView(ctx, tenantID, jobID)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[jobID]
	if !ok || job.TenantID != tenantID {
		return View{}, catalog.E(catalog.CodeNotFound, "job was not found")
	}
	return PublicView(job), nil
}

// Claim selects the next due or expired-lease Job across tenants, increments
// its attempt and assigns a new lease.
func (m *MemoryStore) Claim(ctx context.Context, owner string, now time.Time, lease time.Duration) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var selected Job
	found := false
	for _, candidate := range m.jobs {
		if err := ctx.Err(); err != nil {
			return Job{}, err
		}
		eligible := (candidate.State == StateQueued || candidate.State == StateWaiting) && !candidate.NotBefore.After(now)
		if candidate.State == StateRunning && candidate.LeaseExpiresAt != nil && !candidate.LeaseExpiresAt.After(now) {
			eligible = true
		}
		if eligible && (!found || jobClaimLess(candidate, selected)) {
			selected = candidate
			found = true
		}
	}
	if !found {
		return Job{}, catalog.E(catalog.CodeNotFound, "no job is ready")
	}
	selected.State = StateRunning
	selected.Attempt++
	selected.LeaseOwner = owner
	selected.SafeError = ""
	selected.FailureClass = ""
	expires := now.Add(lease)
	selected.LeaseExpiresAt = &expires
	selected.UpdatedAt = now
	m.jobs[selected.ID] = selected
	return cloneJob(selected), nil
}

// jobClaimLess orders claims by descending priority, then due time, creation
// time and stable ID.
func jobClaimLess(left, right Job) bool {
	if left.Priority != right.Priority {
		return left.Priority > right.Priority
	}
	if !left.NotBefore.Equal(right.NotBefore) {
		return left.NotBefore.Before(right.NotBefore)
	}
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.Before(right.CreatedAt)
	}
	return left.ID < right.ID
}

// Heartbeat extends an unexpired matching tenant/owner lease and records
// progress.
func (m *MemoryStore) Heartbeat(ctx context.Context, tenantID, jobID, owner string, now time.Time, lease time.Duration, progress int) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, err := m.leased(tenantID, jobID, owner, now)
	if err != nil {
		return Job{}, err
	}
	expires := now.Add(lease)
	if expires.Before(*job.LeaseExpiresAt) {
		expires = *job.LeaseExpiresAt
	}
	job.LeaseExpiresAt = &expires
	job.Progress = progress
	job.UpdatedAt = jobTransitionTime(job, now)
	m.jobs[job.ID] = job
	return cloneJob(job), nil
}

// Complete records success and result bytes only under the current lease, then
// clears claim ownership.
func (m *MemoryStore) Complete(ctx context.Context, tenantID, jobID, owner string, now time.Time, result json.RawMessage, evidence ...audit.Record) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, err := m.leased(tenantID, jobID, owner, now)
	if err != nil {
		return Job{}, err
	}
	if job.Kind == TrustKind {
		if job.CancellationRequested {
			return Job{}, context.Canceled
		}
		if len(result) > MaxTrustRecordBytes || len(evidence) != 1 || m.evidence == nil {
			return Job{}, catalog.E(catalog.CodeInternal, "trust completion evidence is required")
		}
		if err := ValidateTrustCompletion(job, result, evidence[0]); err != nil {
			return Job{}, err
		}
		if err := m.evidence.AppendAuditEvent(ctx, evidence[0]); err != nil {
			return Job{}, err
		}
	}
	job.State = StateSucceeded
	job.Progress = 100
	job.Result = append(json.RawMessage(nil), result...)
	job.LeaseOwner = ""
	job.LeaseExpiresAt = nil
	job.UpdatedAt = jobTransitionTime(job, now)
	m.jobs[job.ID] = job
	return cloneJob(job), nil
}

// Fail records the classified attempt outcome under the lease and selects
// cancellation, retry, failure or dead-letter state.
func (m *MemoryStore) Fail(ctx context.Context, tenantID, jobID, owner string, now time.Time, class FailureClass, safe string, retryAt time.Time) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, err := m.leased(tenantID, jobID, owner, now)
	if err != nil {
		return Job{}, err
	}
	job.FailureClass = class
	job.SafeError = safe
	job.LeaseOwner = ""
	job.LeaseExpiresAt = nil
	job.UpdatedAt = jobTransitionTime(job, now)
	switch {
	case class == FailureCancelled || job.CancellationRequested:
		job.State = StateCancelled
	case !retryAt.IsZero() && job.Attempt < job.AttemptLimit:
		job.State = StateWaiting
		job.NotBefore = jobTransitionTime(job, retryAt)
	case job.Attempt >= job.AttemptLimit:
		job.State = StateDeadLetter
	default:
		job.State = StateFailed
	}
	m.jobs[job.ID] = job
	return cloneJob(job), nil
}

// RequestCancellation cancels queued work or flags a running Job, retaining
// supplied evidence before making the change visible.
func (m *MemoryStore) RequestCancellation(ctx context.Context, tenantID, jobID string, now time.Time, evidence ...audit.Record) (Job, error) {
	if identity.NeedsPermissionAdmission(ctx) {
		var admitted0 Job
		err := identity.WithCurrentPermissionAdmission(ctx, func(ctx context.Context) error {
			var operationErr error
			admitted0, operationErr = m.RequestCancellation(ctx, tenantID, jobID, now, evidence...)
			return operationErr
		})
		return admitted0, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[jobID]
	if !ok || job.TenantID != tenantID {
		return Job{}, catalog.E(catalog.CodeNotFound, "job was not found")
	}
	before := cloneJob(job)
	switch job.State {
	case StateQueued, StateWaiting:
		job.State = StateCancelled
	case StateRunning:
		job.CancellationRequested = true
	case StateSucceeded, StateFailed, StateCancelled, StateDeadLetter:
		if err := m.appendCancellationEvidence(ctx, evidence, before, job); err != nil {
			return Job{}, err
		}
		return cloneJob(job), nil
	}
	job.UpdatedAt = jobTransitionTime(job, now)
	m.jobs[job.ID] = job
	if err := m.appendCancellationEvidence(ctx, evidence, before, job); err != nil {
		m.jobs[job.ID] = before
		return Job{}, err
	}
	return cloneJob(job), nil
}

// jobTransitionTime preserves retained ordering without changing the wall time
// used by lease checks when the host clock steps backwards.
func jobTransitionTime(job Job, now time.Time) time.Time {
	if now.Before(job.UpdatedAt) {
		return job.UpdatedAt
	}
	return now
}

// appendEnqueueEvidence validates enqueue or deduplication facts against the
// selected Job before appending audit evidence.
func (m *MemoryStore) appendEnqueueEvidence(ctx context.Context, records []audit.Record, job Job, existing bool) error {
	if len(records) == 0 {
		return nil
	}
	if len(records) != 1 || m.evidence == nil {
		return catalog.E(catalog.CodeInternal, "Job enqueue evidence is not configured")
	}
	record, err := FinalizeEnqueueEvidence(records[0], job, existing)
	if err != nil {
		return err
	}
	return m.evidence.AppendAuditEvent(ctx, record)
}

// appendCancellationEvidence validates the exact before/after cancellation
// state before appending audit evidence.
func (m *MemoryStore) appendCancellationEvidence(ctx context.Context, records []audit.Record, before, after Job) error {
	if len(records) == 0 {
		return nil
	}
	if len(records) != 1 || m.evidence == nil {
		return catalog.E(catalog.CodeInternal, "Job cancellation evidence is not configured")
	}
	record, err := FinalizeCancellationEvidence(records[0], before, after)
	if err != nil {
		return err
	}
	return m.evidence.AppendAuditEvent(ctx, record)
}

// leased requires matching tenant and owner, running state and an unexpired
// lease before a worker mutation.
func (m *MemoryStore) leased(tenantID, jobID, owner string, now time.Time) (Job, error) {
	job, ok := m.jobs[jobID]
	if !ok || job.TenantID != tenantID || job.State != StateRunning || job.LeaseOwner != owner || job.LeaseExpiresAt == nil || !job.LeaseExpiresAt.After(now) {
		return Job{}, catalog.E(catalog.CodeConflict, "job lease is not owned or has expired")
	}
	return job, nil
}

// cloneJob copies payload, result and lease timestamp storage while preserving
// private operational fields.
func cloneJob(job Job) Job {
	copyValue := job
	copyValue.Payload = append(json.RawMessage(nil), job.Payload...)
	copyValue.Result = append(json.RawMessage(nil), job.Result...)
	if job.LeaseExpiresAt != nil {
		expires := *job.LeaseExpiresAt
		copyValue.LeaseExpiresAt = &expires
	}
	return copyValue
}

// String returns the Job kind and ID without its payload or execution metadata.
func (j Job) String() string { return fmt.Sprintf("%s/%s", j.Kind, j.ID) }

func (m *MemoryStore) listVisibleLocked(ctx context.Context, tenantID string, request ListRequest) ([]Job, error) {
	if _, selected := identity.CollectionRevision(ctx); !selected {
		return m.listLocked(tenantID, request), nil
	}
	items := make([]Job, 0)
	for _, job := range m.jobs {
		if job.TenantID != tenantID || !matchesListFilters(job, request) {
			continue
		}
		permission, supported := ReadPermission(job.Kind)
		if !supported {
			continue
		}
		allowed, err := identity.CollectionAllows(ctx, permission, "")
		if err != nil {
			return nil, err
		}
		if !allowed {
			continue
		}
		before := request.AfterCreatedAt == nil || job.CreatedAt.Before(*request.AfterCreatedAt) || job.CreatedAt.Equal(*request.AfterCreatedAt) && job.ID < request.AfterID
		if before {
			items = append(items, job)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.After(items[j].CreatedAt)
		}
		return items[i].ID > items[j].ID
	})
	if len(items) > request.Limit {
		items = items[:request.Limit]
	}
	return items, nil
}

// ReadPermission binds a public Job's disclosure to its fixed family.
func ReadPermission(kind string) (identity.Permission, bool) {
	switch kind {
	case TrustKind:
		return "source.read_evidence", true
	case "source.sync":
		return "source.read", true
	case "publication.generate":
		return "publication.read", true
	case "semantic.reindex":
		return "operations.reindex", true
	default:
		return "", false
	}
}
