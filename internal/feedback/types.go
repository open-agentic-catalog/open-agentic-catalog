// Package feedback owns private human submissions and administrator responses.
// Feedback is mutable tenant data, never publication or discovery evidence.
package feedback

import (
	"context"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

const (
	MaxTextRunes = 4000
	MaxBodyBytes = 64 << 10
)

// Record retains immutable context and only the current private text.
type Record struct {
	ID                string    `json:"id"`
	TenantID          string    `json:"tenantId"`
	MembershipID      string    `json:"membershipId"`
	ResourceID        string    `json:"resourceId"`
	ReleaseID         string    `json:"releaseId,omitempty"`
	VersionRevisionID string    `json:"versionRevisionId,omitempty"`
	Category          string    `json:"category"`
	Comment           string    `json:"comment,omitempty"`
	Response          string    `json:"response,omitempty"`
	State             string    `json:"state"`
	Revision          int64     `json:"revision"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// CreateInput accepts context, never submitter identity or lifecycle state.
type CreateInput struct {
	ResourceID        string `json:"resourceId"`
	ReleaseID         string `json:"releaseId,omitempty"`
	VersionRevisionID string `json:"versionRevisionId,omitempty"`
	Category          string `json:"category"`
	Comment           string `json:"comment"`
}

// EditInput replaces the current comment and category, not its context.
type EditInput struct {
	Category string `json:"category"`
	Comment  string `json:"comment"`
}

// ListInput selects a caller-bound, ID-ordered page with a continuation sentinel.
type ListInput struct {
	ResourceID, Category, State, AfterID string
	MembershipID                         string
	Limit                                int
}

// Store commits a validated transition and its audit record atomically.
// Selection predicates are applied before the bounded page is materialized.
type Store interface {
	ReadOverview(context.Context, string) (OverviewRows, error)
	Get(context.Context, string, string) (Record, error)
	List(context.Context, string, ListInput) ([]Record, error)
	Save(context.Context, Record, int64, audit.Record) (Record, error)
}

// ContextReader resolves retained local identities without fetching artifacts.
type ContextReader interface {
	GetResource(context.Context, string, string) (catalog.Resource, error)
	GetRelease(context.Context, string, string) (catalog.ListingRelease, error)
	GetVersionRevision(context.Context, string, string) (catalog.VersionRevision, catalog.EvidenceSet, error)
	GetVersion(context.Context, string, string) (catalog.ResourceVersion, error)
}

// EvidenceSink joins in-memory feedback to the composed audit inventory.
type EvidenceSink interface {
	AppendAuditEvent(context.Context, audit.Record) error
}
