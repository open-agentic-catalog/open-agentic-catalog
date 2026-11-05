// Semantic worker composition schedules derived work and adapts existing Job
// leases to the indexer. Query inference belongs to the separate API process.
package runtime

import (
	"context"
	"errors"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

type semanticMaintenanceStore interface {
	Due(context.Context, string, int, []string, int) ([]catalog.SemanticGeneration, error)
	Admit(context.Context, catalog.SemanticGeneration, []string, int) (bool, error)
	Cleanup(context.Context, string, []string, int) (int64, error)
}

// semanticMaintainer owns no provider and admits at most 16 Jobs per pass.
type semanticMaintainer struct {
	store         semanticMaintenanceStore
	configuration config.Semantic
	maxAttempts   int
	clock         catalog.Clock
	ids           catalog.IDGenerator
}

// Run reconciles immediately and periodically, honoring drain cancellation.
func (s *semanticMaintainer) Run(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		pass, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := s.reconcile(pass)
		cancel()
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// reconcile keeps cleanup and admission in separate bounded transactions.
func (s *semanticMaintainer) reconcile(ctx context.Context) error {
	profile := s.configuration.EmbeddingProfile()
	if _, err := s.store.Cleanup(ctx, profile, s.configuration.ExcludedTenants, 128); err != nil {
		return err
	}
	candidates, err := s.store.Due(ctx, profile, s.configuration.Dimensions, s.configuration.ExcludedTenants, 16)
	if err != nil {
		return err
	}
	for _, g := range candidates {
		if !s.configuration.PermitsTenant(g.TenantID) {
			continue
		}
		g.ID, err = s.ids.New(s.clock.Now())
		if err != nil {
			return err
		}
		if _, err := s.store.Admit(ctx, g, s.configuration.ExcludedTenants, s.maxAttempts); err != nil {
			return err
		}
	}
	return nil
}

// semanticJobHandler admits only the configured profile and excluded-tenant
// policy. Permanent input rejection is handled per member by the indexer.
func semanticJobHandler(configuration config.Semantic, indexer *catalog.SemanticIndexer) jobs.Handler {
	return func(ctx context.Context, job jobs.Job, progress func(int) error) (any, error) {
		input, err := catalog.DecodeSemanticReindex(job.Payload, job.PayloadVersion)
		if err != nil || input.Profile != configuration.EmbeddingProfile() || !configuration.PermitsTenant(job.TenantID) {
			return nil, &jobs.HandlerError{Class: jobs.FailureInvalidInput, Safe: "Semantic indexing job does not match the configured profile or tenant policy.", Cause: err}
		}
		if err := progress(1); err != nil {
			return nil, err
		}
		g := catalog.SemanticGeneration{ID: job.ID, TenantID: job.TenantID, Profile: input.Profile, Scope: input.Scope, SourceGeneration: input.SourceGeneration, Dimensions: configuration.Dimensions}
		count, err := indexer.Run(ctx, g, job.LeaseOwner)
		if err != nil {
			class := jobs.FailureDependency
			if errors.Is(err, catalog.ErrEmbeddingAuthentication) {
				class = jobs.FailureAuthentication
			}
			if errors.Is(err, catalog.ErrEmbeddingInvalidResponse) {
				class = jobs.FailurePermanentUpstream
			}
			if errors.Is(err, catalog.ErrSearchGenerationChanged) || catalog.ErrorCodeOf(err) == catalog.CodeInvalidArgument {
				class = jobs.FailureInvalidInput
			}
			failure := &jobs.HandlerError{Class: class, Safe: "Semantic indexing did not complete; lexical search remains available.", Cause: err}
			var hint *catalog.EmbeddingRetryError
			if class == jobs.FailureDependency && errors.As(err, &hint) {
				failure.RetryAfter = hint.RetryAfter
			}
			return nil, failure
		}
		if err := progress(100); err != nil {
			return nil, err
		}
		return struct {
			Generation string `json:"generation"`
			Members    int64  `json:"members"`
		}{g.ID, count}, nil
	}
}
