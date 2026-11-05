// Package ratings owns tenant-local human opinions, not trust or relevance.
package ratings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

const IRI = "https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/ratings/v1#summary"

// Target is independent of releases, retrieval modes and observation digests.
// Foreign targets retain advertised version presence, including an empty version.
type Target struct {
	Origin                 string       `json:"origin"`
	ResourceID             string       `json:"resourceId,omitempty"`
	VersionRevisionID      string       `json:"versionRevisionId,omitempty"`
	SourceID               string       `json:"sourceId,omitempty"`
	PresentationGeneration int64        `json:"presentationGeneration,omitempty"`
	Identifier             string       `json:"identifier"`
	VersionPresent         bool         `json:"versionPresent"`
	Version                string       `json:"version,omitempty"`
	Kind                   catalog.Kind `json:"kind"`
}

// UnmarshalJSON preserves the required explicit advertised-version flag.
func (t *Target) UnmarshalJSON(raw []byte) error {
	type wire Target
	var value wire
	if err := jsondoc.Decode(raw, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, name := range []string{"origin", "identifier", "kind", "versionPresent"} {
		if len(fields[name]) == 0 {
			return catalog.E(catalog.CodeInvalidArgument, "rating target field is missing")
		}
	}
	*t = Target(value)
	return ValidateTarget(*t)
}

func ValidateTarget(t Target) error {
	identifier, err := catalog.ParseAIRIdentifier(t.Identifier)
	if err != nil || identifier.String() != t.Identifier || len(t.Identifier) > 512 || !t.Kind.Valid() || utf8.RuneCountInString(t.Version) > 128 || !utf8.ValidString(t.Version) || strings.ContainsAny(t.Version, "\x00\r\n") || !t.VersionPresent && t.Version != "" {
		return catalog.E(catalog.CodeInvalidArgument, "rating target is invalid")
	}
	switch t.Origin {
	case "local":
		if !identity.ValidTenantID(t.ResourceID) || !identity.ValidTenantID(t.VersionRevisionID) || t.SourceID != "" || t.PresentationGeneration != 0 || !t.VersionPresent {
			return catalog.E(catalog.CodeInvalidArgument, "local rating target is invalid")
		}
	case "foreign":
		if !identity.ValidTenantID(t.SourceID) || t.PresentationGeneration < 1 || t.ResourceID != "" || t.VersionRevisionID != "" {
			return catalog.E(catalog.CodeInvalidArgument, "foreign rating target is invalid")
		}
	default:
		return catalog.E(catalog.CodeInvalidArgument, "rating origin is invalid")
	}
	return nil
}

// Key excludes evidence that may change without changing the rated version.
func Key(t Target) string {
	if t.Origin == "local" {
		t = Target{Origin: t.Origin, ResourceID: t.ResourceID, VersionRevisionID: t.VersionRevisionID}
	}
	raw, _ := json.Marshal(t) //nolint:errchkjson // This identity contains only strings, integers and a boolean; no fallible marshalers.
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// PrincipalKey survives membership recreation; it is never a public rater ID.
func PrincipalKey(a identity.Actor) (string, error) {
	if a.ActorType != identity.ActorHuman || !identity.ValidTenantID(a.TenantID) || a.Issuer == "" || !utf8.ValidString(a.Issuer) || strings.ContainsAny(a.Issuer, "\x00\r\n") || len(a.Issuer) > 2048 || !identity.ValidSubject(a.Subject) {
		return "", catalog.E(catalog.CodeForbidden, "an authenticated human identity is required")
	}
	raw, _ := json.Marshal([]string{a.TenantID, a.Issuer, a.Subject}) //nolint:errchkjson // A string slice has no fallible marshaler.
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

type Admission struct {
	Target         Target `json:"target"`
	ReleaseID      string `json:"releaseId,omitempty"`
	EntryDigest    string `json:"entryDigest,omitempty"`
	SourceRevision int64  `json:"sourceRevision,omitempty"`
	NamespaceID    string `json:"-"`
	Live           bool   `json:"-"`
}

// Authority rechecks current visibility without a network fetch or adoption.
type Authority interface {
	Admit(context.Context, Admission) (Admission, error)
}

type Record struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenantId"`
	PrincipalKey string    `json:"-"`
	SubmittedBy  string    `json:"-"`
	Target       Target    `json:"target"`
	ReleaseID    string    `json:"releaseId,omitempty"`
	EntryDigest  string    `json:"entryDigest,omitempty"`
	Score        int       `json:"score"`
	Consent      bool      `json:"consent"`
	State        string    `json:"state"`
	Revision     int64     `json:"revision"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type Aggregate struct {
	Count       int64 `json:"count"`
	Sum         int64 `json:"sum"`
	SharedCount int64 `json:"-"`
	SharedSum   int64 `json:"-"`
}

func (a Aggregate) Valid() bool {
	return validTotals(a.Count, a.Sum) && validTotals(a.SharedCount, a.SharedSum) && a.SharedCount <= a.Count && a.SharedSum <= a.Sum && validTotals(a.Count-a.SharedCount, a.Sum-a.SharedSum)
}
func validTotals(count, sum int64) bool {
	return count >= 0 && count <= 9007199254740991/5 && sum >= count && sum <= 5*count
}

// Average uses decimal half-up rounding, independent of floating point ties.
func (a Aggregate) Average() float64 {
	if a.Count == 0 || !a.Valid() {
		return 0
	}
	return float64((10*a.Sum+a.Count/2)/a.Count) / 10
}

type View struct {
	Target       Target   `json:"target"`
	Count        int64    `json:"count"`
	Average      *float64 `json:"average,omitempty"`
	Own          *Record  `json:"own,omitempty"`
	ChangedEntry bool     `json:"changedEntry"`
}

type Sharing struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId"`
	Host      string    `json:"host"`
	Audience  string    `json:"audience"`
	Enabled   bool      `json:"enabled"`
	Revision  int64     `json:"revision"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type ListInput struct {
	PrincipalKey, AfterID, State string
	Limit                        int
}
type Store interface {
	ReadOverview(context.Context, string) ([]string, error)
	Get(context.Context, string, string) (Record, error)
	Own(context.Context, string, Target, string) (Record, error)
	List(context.Context, string, ListInput) ([]Record, error)
	Save(context.Context, Record, int64, Admission, audit.Record) (Record, error)
	Aggregates(context.Context, string, []Target) (map[string]Aggregate, error)
	GetSharing(context.Context, string, string, string) (Sharing, error)
	SaveSharing(context.Context, Sharing, int64, audit.Record) (Sharing, error)
}

type EvidenceSink interface {
	AppendAuditEvent(context.Context, audit.Record) error
}
