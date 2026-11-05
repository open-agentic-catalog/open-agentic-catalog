// Semantic index records describe rebuildable inputs and their fenced generation.
package catalog

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

// Provider errors are safe categories shared by the domain and HTTP adapter.
// Authentication failures must never mark Catalog metadata permanently invalid.
var (
	ErrEmbeddingUnavailable     = errors.New("embedding provider unavailable")
	ErrEmbeddingRejected        = errors.New("embedding input rejected")
	ErrEmbeddingAuthentication  = errors.New("embedding provider authentication failed")
	ErrEmbeddingInvalidResponse = errors.New("embedding response invalid")
)

// EmbeddingRetryError carries a bounded provider minimum for durable indexing.
// Query callers retain the unavailable category and fall back without waiting.
// It contains no provider response text, address or credential.
type EmbeddingRetryError struct {
	RetryAfter time.Duration
}

func (e *EmbeddingRetryError) Error() string { return ErrEmbeddingUnavailable.Error() }
func (e *EmbeddingRetryError) Unwrap() error { return ErrEmbeddingUnavailable }

// SemanticProvider implements the bounded query/document inference contract.
type SemanticProvider interface {
	Query(context.Context, string) ([]float32, error)
	Documents(context.Context, []string) ([][]float32, error)
}

// SemanticInput is a bounded eligible metadata projection selected by its owner.
type SemanticInput struct {
	Key      string
	Document SemanticDocument
}

// SemanticIndexStore keeps enumeration, vector reuse and writes tenant/lease fenced.
// Each method owns a short transaction; none calls inference while holding it.
type SemanticIndexStore interface {
	Begin(context.Context, SemanticGeneration, string) error
	Inputs(context.Context, SemanticGeneration, string, int) ([]SemanticInput, int64, error)
	KnownInputs(context.Context, SemanticGeneration, []string) (map[string]bool, error)
	SaveBatch(context.Context, SemanticGeneration, string, []SemanticMember) error
	Complete(context.Context, SemanticGeneration, string, int64) error
}

// SemanticGeneration binds derived membership to one immutable Job/profile/scope
// and one authoritative search generation. It is not a publication snapshot.
type SemanticGeneration struct {
	ID               string
	TenantID         string
	Profile          string
	Scope            string
	SourceGeneration string
	Dimensions       int
	State            string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// SemanticReindexInput binds one Job to a current tenant-local search scope.
// The server selects the profile; clients cannot submit this internal payload.
type SemanticReindexInput struct {
	Profile          string `json:"profile"`
	Scope            string `json:"scope"`
	SourceGeneration string `json:"sourceGeneration"`
}

// SemanticIndexStatus is an administrator-only summary of the current control
// generation. Counts describe processed metadata, never private input contents.
type SemanticIndexStatus struct {
	State   string `json:"state"`
	JobID   string `json:"jobId,omitempty"`
	Indexed int64  `json:"indexed"`
	Failed  int64  `json:"failed"`
}

// DecodeSemanticReindex accepts only the current closed, bounded Job payload.
// Restore checks and runtime execution use the same executable contract.
func DecodeSemanticReindex(raw []byte, version int) (SemanticReindexInput, error) {
	var input SemanticReindexInput
	if version != 1 || len(raw) > 1024 || jsondoc.Validate(raw) != nil {
		return input, E(CodeInvalidArgument, "invalid semantic indexing Job payload")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return SemanticReindexInput{}, E(CodeInvalidArgument, "invalid semantic indexing Job payload")
	}
	validDigest := func(value string) bool {
		digest, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
		return err == nil && len(digest) == 32 && value == "sha256:"+hex.EncodeToString(digest)
	}
	generation, generationErr := strconv.ParseInt(input.SourceGeneration, 10, 64)
	if !validDigest(input.Profile) || generationErr != nil || generation < 0 || strconv.FormatInt(generation, 10) != input.SourceGeneration || input.Scope != "control" && (!strings.HasPrefix(input.Scope, "public:") || !identity.ValidTenantID(strings.TrimPrefix(input.Scope, "public:"))) {
		return SemanticReindexInput{}, E(CodeInvalidArgument, "invalid semantic indexing Job identity")
	}
	return input, nil
}

// SemanticMember contains one immutable metadata key and normalized embedding,
// or a bounded permanent input failure. It carries no original text or credentials.
type SemanticMember struct {
	Key         string
	InputDigest string
	Vector      []float32
	Failure     string
	// Reuse links an already retained same-profile/disclosure vector without
	// retrieving its bytes or sending its metadata to the provider again.
	Reuse bool
}

// SemanticSearchPosition retains the precise fused rank, not the rounded score.
type SemanticSearchPosition struct {
	Rank     int64  `json:"r"`
	SortName string `json:"n"`
	ID       string `json:"i"`
}

// SemanticSearchRequest selects the complete filtered local or durable unified
// population. The vector was obtained outside the repository transaction.
type SemanticSearchRequest struct {
	IdentityProbes []IndexedIdentity
	Generation     SemanticGeneration
	Query          SearchQuery
	Vector         []float32
	Cutoff         float64
	LocalOnly      bool
	After          *SemanticSearchPosition
}

// SemanticSearchItem carries a bounded hydrated result and private page position.
type SemanticSearchItem struct {
	Result   CatalogSearchResult
	Local    *CatalogSummary
	Position SemanticSearchPosition
}

// SemanticSearchPage reports exact filtered totals even on an empty final page.
type SemanticSearchPage struct {
	Matches      []IndexedIdentity
	Items        []SemanticSearchItem
	Total        int
	HasMore      bool
	PartialIndex bool
}
