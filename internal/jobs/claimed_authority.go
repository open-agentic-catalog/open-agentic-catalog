package jobs

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"sync/atomic"
)

type claimedAuthorityKey struct{}
type claimedAuthority struct {
	job    Job
	active atomic.Bool
	verify func(context.Context) error
}

// CheckClaimedAttempt can only validate a context installed by Worker after a
// successful claim. Subject names, Job-shaped JSON and audit context are not
// proof of a current lease. Authority expires when the handler returns.
func CheckClaimedAttempt(ctx context.Context, job Job) error {
	claim, ok := ctx.Value(claimedAuthorityKey{}).(*claimedAuthority)
	if !ok || !claim.active.Load() || claim.job.ID != job.ID || claim.job.TenantID != job.TenantID || claim.job.Kind != job.Kind || claim.job.Attempt != job.Attempt || claim.job.LeaseOwner != job.LeaseOwner || string(claim.job.Payload) != string(job.Payload) {
		return catalog.E(catalog.CodeForbidden, "claimed Job authority is required")
	}
	return claim.verify(ctx)
}
