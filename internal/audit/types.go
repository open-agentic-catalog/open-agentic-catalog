// Package audit owns the bounded service audit record and deterministic export
// format. It deliberately has no dependency on Catalog domain or transport
// packages so every owning component can append evidence in its own transaction.
package audit

import (
	"encoding/json"
	"time"
)

const (
	// RecordFormatV1 is the first service-owned audit record shape.
	RecordFormatV1 = 1
	// ExportFormatV1 is the first closed synchronous export envelope.
	ExportFormatV1 = 1
	// ExportEncodingProfile identifies the deterministic JSON encoding rules
	// implemented by this package. It is intentionally not RFC 8785.
	ExportEncodingProfile = "open-agentic-catalog-audit-export-v1"

	MaxActorBytes          = 512
	MaxActionBytes         = 160
	MaxTargetTypeBytes     = 160
	MaxTargetIDBytes       = 512
	MaxRequestIDBytes      = 128
	MaxReasonRunes         = 2000
	MaxDetailsBytes        = 16 << 10
	MaxDetailsDepth        = 8
	MaxDetailsMembers      = 64
	MaxDetailsArrayEntries = 64
	MaxEncodedRecordBytes  = 64 << 10

	MaxExportRows          = 1000
	MaxExportResponseBytes = 8 << 20
	MaxExportFileBytes     = MaxExportResponseBytes
)

// Record is the current attributed API and export projection. A missing or
// unsupported format version is invalid, including on retained-row reads.
type Record struct {
	FormatVersion *int           `json:"formatVersion"`
	ID            string         `json:"id"`
	TenantID      string         `json:"tenantId"`
	Actor         string         `json:"actor"`
	Action        string         `json:"action"`
	TargetType    string         `json:"targetType"`
	TargetID      string         `json:"targetId"`
	Reason        string         `json:"reason,omitempty"`
	RequestID     string         `json:"requestId,omitempty"`
	BeforeDigest  string         `json:"beforeDigest,omitempty"`
	AfterDigest   string         `json:"afterDigest,omitempty"`
	Details       map[string]any `json:"details,omitempty"`
	At            time.Time      `json:"at"`
}

// Principal identifies the indexed actor used by a version-1 record.
type Principal struct {
	ID        string
	Type      string
	ActorType string
	RealmID   string
}

const (
	PrincipalCatalogMembership = "catalog_membership"
	PrincipalServiceExecutor   = "service_executor"
	PrincipalDatabaseLogin     = "database_login"

	// PrincipalIDCatalogWorker is the code-owned identity used for domain
	// results committed by a leased worker attempt.
	PrincipalIDCatalogWorker = "catalog-worker"
	// InitiatorSourceMaintainer identifies Jobs admitted by the code-owned
	// automatic source-maintenance loop rather than by a Catalog membership.
	InitiatorSourceMaintainer = "catalog-source-maintainer"
)

// Execution is safe, bounded attribution supplied by the entry point.
type Execution struct {
	RequestID           string
	AuthorizationDigest string
	CommandID           string
	CurrentUser         string
	OperatorLabel       string
}

// JobExecution identifies one leased worker attempt without retaining the Job
// payload, lease owner, or token-era authorization material.
type JobExecution struct {
	ID        string
	Kind      string
	Attempt   int
	Initiator string
	Trigger   string
}

// Details is the closed common envelope for version-1 non-indexed attribution
// and bounded action-specific facts.
type Details struct {
	PrincipalType       string         `json:"principalType"`
	ActorType           string         `json:"actorType,omitempty"`
	RealmID             string         `json:"realmId,omitempty"`
	AuthorizationDigest string         `json:"authorizationDigest,omitempty"`
	CommandID           string         `json:"commandId,omitempty"`
	CurrentUser         string         `json:"currentUser,omitempty"`
	OperatorLabel       string         `json:"operatorLabel,omitempty"`
	Facts               map[string]any `json:"facts,omitempty"`
}

// Query contains exact database-side investigation filters. OccurredFrom is
// inclusive and OccurredBefore is exclusive when present.
type Query struct {
	Actor          string
	Action         string
	RequestID      string
	TargetType     string
	TargetID       string
	OccurredFrom   *time.Time
	OccurredBefore *time.Time
}

// Filters is the normalized closed filter shape embedded in an export.
type Filters struct {
	Actor          string    `json:"actor,omitempty"`
	Action         string    `json:"action,omitempty"`
	RequestID      string    `json:"requestId,omitempty"`
	TargetType     string    `json:"targetType,omitempty"`
	TargetID       string    `json:"targetId,omitempty"`
	OccurredFrom   time.Time `json:"occurredFrom"`
	OccurredBefore time.Time `json:"occurredBefore"`
}

// Position is one stable audit ordering position.
type Position struct {
	OccurredAt time.Time `json:"occurredAt"`
	ID         string    `json:"id"`
}

// Manifest binds the selected range and exact canonical events-array bytes.
type Manifest struct {
	EncodingProfile string    `json:"encodingProfile"`
	TenantID        string    `json:"tenantId"`
	Filters         Filters   `json:"filters"`
	Cutoff          time.Time `json:"cutoff"`
	First           *Position `json:"first,omitempty"`
	Last            *Position `json:"last,omitempty"`
	RowCount        int       `json:"rowCount"`
	EventsDigest    string    `json:"eventsDigest"`
	GeneratedAt     time.Time `json:"generatedAt"`
}

// Export is the closed bounded synchronous response. RawEvents holds the exact
// canonical array protected by Manifest.EventsDigest.
type Export struct {
	FormatVersion int             `json:"formatVersion"`
	Manifest      Manifest        `json:"manifest"`
	RawEvents     json.RawMessage `json:"events"`
}

// TrustedManifest is stored separately by recovery tooling and binds an export
// manifest to one externally controlled quiesced recovery-point interval.
type TrustedManifest struct {
	FormatVersion   int      `json:"formatVersion"`
	RecoveryPointID string   `json:"recoveryPointId"`
	Manifest        Manifest `json:"manifest"`
}

// VerificationReport describes structural self-consistency without claiming
// database-owner tamper evidence.
type VerificationReport struct {
	Mode              string `json:"mode"`
	Valid             bool   `json:"valid"`
	RowCount          int    `json:"rowCount"`
	EventsDigest      string `json:"eventsDigest"`
	RecoveryPointID   string `json:"recoveryPointId,omitempty"`
	AssuranceBoundary string `json:"assuranceBoundary"`
}
