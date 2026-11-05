package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/trustevidence"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards/anstrust"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/tenantsettings"
)

type trustGuardKey struct{}
type trustOutbound struct{ outbound trustevidence.Outbound }

func (o trustOutbound) Fetch(ctx context.Context, r fetcher.Request) (fetcher.Result, error) {
	guard, ok := ctx.Value(trustGuardKey{}).(func(context.Context) error)
	if !ok {
		return fetcher.Result{}, trustevidence.ErrPolicy
	}
	if err := guard(ctx); err != nil {
		return fetcher.Result{}, err
	}
	return o.outbound.Fetch(ctx, r)
}

type trustVerifier struct {
	settings    config.TrustVerification
	digest      string
	acquisition *trustevidence.Acquirer
	credentials fetcher.CredentialResolver
}

func newTrustVerifier(c config.Config, outbound trustevidence.Outbound, credentials fetcher.CredentialResolver) (*trustVerifier, error) {
	a, err := trustevidence.New(c, trustOutbound{outbound})
	if err != nil {
		return nil, err
	}
	return &trustVerifier{c.TrustVerification, c.TrustVerification.PolicyDigest(c.Federation.PolicyDigest), a, credentials}, nil
}
func (v *trustVerifier) Policy(name string) (string, string, bool) {
	p, _, ok := v.settings.Policy(name)
	return v.digest, p.RegistrationAuthority, ok
}

// ValidateSource checks the mounted credential generation without making a request.
func (v *trustVerifier) ValidateSource(ctx context.Context, source federation.Source) error {
	if source.CredentialBinding == "" {
		return nil
	}
	if v.credentials == nil {
		return fetcher.ErrCredentialChanged
	}
	origin, err := fetcher.NormalizeOrigin(source.Endpoint, false)
	if err != nil {
		return fetcher.ErrPolicyDenied
	}
	credential, err := v.credentials.Resolve(ctx, fetcher.CredentialRequest{TenantID: source.TenantID, Binding: source.CredentialBinding, Origin: origin})
	if err != nil {
		return fetcher.ErrCredentialChanged
	}
	if credential.Revision != source.CredentialRevision || credential.Origin != origin {
		return fetcher.ErrCredentialChanged
	}
	return nil
}
func (v *trustVerifier) Assess(ctx context.Context, p jobs.TrustPayload, guard func(context.Context) error, now func() time.Time) (anstrust.Bundle, anstrust.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, v.settings.Timeout.Duration)
	defer cancel()
	b, err := v.acquisition.Acquire(context.WithValue(ctx, trustGuardKey{}, guard), p.PolicyName, trustTenant(ctx), p.Entry)
	var result anstrust.Result
	_, verifier, ok := v.settings.Policy(p.PolicyName)
	if !ok {
		return b, result, &jobs.HandlerError{Class: jobs.FailurePolicyDenied, Safe: "Trust verification policy is unavailable."}
	}
	if err != nil {
		var httpErr *trustevidence.HTTPError
		class := jobs.FailureTransientUpstream
		switch {
		case errors.Is(err, context.Canceled):
			return b, result, err
		case errors.As(err, &httpErr):
			switch {
			case httpErr.Status == 429:
				class = jobs.FailureRateLimited
			case httpErr.Status == 401 || httpErr.Status == 403:
				class = jobs.FailureAuthentication
			case httpErr.Status >= 500:
				class = jobs.FailureTransientUpstream
			default:
				class = jobs.FailurePermanentUpstream
			}
			// Missing evidence and 410 do not prove revocation. Preserve the
			// authentic partial checks as an insufficient terminal assessment.
			if httpErr.Status == 404 || httpErr.Status == 410 {
				err = nil
			}
		case errors.Is(err, fetcher.ErrPolicyDenied), errors.Is(err, fetcher.ErrPolicyChanged), errors.Is(err, fetcher.ErrCredentialChanged), errors.Is(err, trustevidence.ErrDisabled), errors.Is(err, trustevidence.ErrPolicy), catalog.ErrorCodeOf(err) == catalog.CodePreconditionFailed, catalog.ErrorCodeOf(err) == catalog.CodeNotFound, catalog.ErrorCodeOf(err) == catalog.CodeForbidden:
			class = jobs.FailurePolicyDenied
		case errors.Is(err, trustevidence.ErrBudget), errors.Is(err, trustevidence.ErrResponse), errors.Is(err, anstrust.ErrReference), errors.Is(err, anstrust.ErrFramework):
			err = nil
		}
		if err != nil {
			return b, result, &jobs.HandlerError{Class: class, Safe: "Trust evidence could not be acquired within the selected policy.", Cause: err}
		}
	}
	if len(b.Entry) == 0 {
		return b, result, &jobs.HandlerError{Class: jobs.FailureInvalidInput, Safe: "Selected entry exceeds the evidence bundle budget."}
	}
	if b.Receipt == nil {
		b.Receipt = []byte{}
	}
	if b.Checkpoint == nil {
		b.Checkpoint = []byte{}
	}
	if b.StatusToken == nil {
		b.StatusToken = []byte{}
	}
	result, err = verifier.Verify(ctx, b, now())
	if errors.Is(err, context.DeadlineExceeded) {
		return b, result, &jobs.HandlerError{Class: jobs.FailureTransientUpstream, Safe: "Trust verification attempt exceeded its deadline.", Cause: err}
	}
	return b, result, err
}

func trustJobHandler(manager *federation.Manager, logger *slog.Logger) jobs.Handler {
	return func(ctx context.Context, job jobs.Job, progress func(int) error) (any, error) {
		p, err := jobs.DecodeTrustPayload(job)
		if err != nil {
			return nil, &jobs.HandlerError{Class: jobs.FailureInvalidInput, Safe: "Trust assessment Job payload is invalid.", Cause: err}
		}
		ctx, err = workerActor(ctx, job, p.SourceID)
		if err != nil {
			return nil, err
		}
		ctx = audit.WithJobExecution(ctx, audit.JobExecution{ID: job.ID, Kind: job.Kind, Attempt: job.Attempt, Initiator: job.Actor, Trigger: "manual"})
		if err = progress(10); err != nil {
			return nil, err
		}
		result, err := manager.ExecuteTrustAssessment(ctx, job)
		if err != nil {
			return nil, trustExecutionFailure(err)
		}
		outcome := "negative_or_insufficient"
		if result.Assessment.CurrentRegistration {
			outcome = "current_registration"
		}
		logger.DebugContext(ctx, "trust assessment checks completed", "event", "trust.assessment_checked", "component", "job_worker", "outcome", outcome)
		return result, nil
	}
}

func trustExecutionFailure(err error) error {
	var typed *jobs.HandlerError
	if errors.As(err, &typed) || errors.Is(err, context.Canceled) {
		return err
	}
	class := jobs.FailureInternal
	safe := "Trust assessment could not complete."
	code := catalog.ErrorCodeOf(err)
	switch {
	case code == catalog.CodeForbidden || code == catalog.CodeNotFound || code == catalog.CodePreconditionFailed:
		class = jobs.FailurePolicyDenied
		safe = "Trust assessment source or policy is no longer eligible."
	case code == catalog.CodeDependencyUnavailable:
		class = jobs.FailureDependency
	case code == catalog.CodeInvalidArgument:
		class = jobs.FailureInvalidInput
	}
	return &jobs.HandlerError{Class: class, Safe: safe, Cause: err}
}

// memoryTrustJobs holds the source read fence through each local transition.
// PostgreSQL applies its equivalent row fence in the Job transaction.
type memoryTrustJobs struct {
	jobs.Store
	sources *federation.MemoryStore
}

func (m memoryTrustJobs) Enqueue(ctx context.Context, job jobs.Job, evidence ...audit.Record) (result jobs.Job, reused bool, err error) {
	if job.Kind != jobs.TrustKind {
		return m.Store.Enqueue(ctx, job, evidence...)
	}
	p, err := jobs.DecodeTrustPayload(job)
	if err != nil {
		return result, false, err
	}
	err = m.sources.WithTrustSource(ctx, job.TenantID, p, func() error { result, reused, err = m.Store.Enqueue(ctx, job, evidence...); return err })
	return result, reused, err
}
func (m memoryTrustJobs) Complete(ctx context.Context, tenant, id, owner string, now time.Time, raw json.RawMessage, evidence ...audit.Record) (result jobs.Job, err error) {
	job, err := m.Store.Get(ctx, tenant, id)
	if err != nil {
		return result, err
	}
	if job.Kind != jobs.TrustKind {
		return m.Store.Complete(ctx, tenant, id, owner, now, raw, evidence...)
	}
	p, err := jobs.DecodeTrustPayload(job)
	if err != nil {
		return result, err
	}
	err = m.sources.WithTrustSource(ctx, tenant, p, func() error {
		result, err = m.Store.Complete(ctx, tenant, id, owner, now, raw, evidence...)
		return err
	})
	return result, err
}

func trustTenant(ctx context.Context) string { a, _ := identity.FromContext(ctx); return a.TenantID }

func (v *trustVerifier) Enabled() bool { return v.settings.Enabled }

// memorySettingsJobs orders current authorization, settings, source and Job fences.
// The settings lock remains held through evidence and the enqueue, without I/O.
type memorySettingsJobs struct {
	jobs.Store
	settings *tenantsettings.MemoryStore
}

func (m memorySettingsJobs) Enqueue(ctx context.Context, job jobs.Job, evidence ...audit.Record) (result jobs.Job, reused bool, err error) {
	if identity.NeedsPermissionAdmission(ctx) {
		err = identity.WithCurrentPermissionAdmission(ctx, func(admitted context.Context) error {
			result, reused, err = m.Enqueue(admitted, job, evidence...)
			return err
		})
		return
	}
	if !job.DefaultAttemptLimit || job.ParentJobID != "" {
		job.DefaultAttemptLimit = false
		return m.Store.Enqueue(ctx, job, evidence...)
	}
	err = m.settings.WithNewJobAttemptLimit(ctx, job.TenantID, job.AttemptLimit, func(attempts int) error {
		job.AttemptLimit = attempts
		job.DefaultAttemptLimit = false
		result, reused, err = m.Store.Enqueue(ctx, job, evidence...)
		return err
	})
	return
}
