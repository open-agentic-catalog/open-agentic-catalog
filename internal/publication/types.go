// Package publication builds and serves immutable audience-scoped snapshots.
// [Manager] separates candidate generation from activation and rechecks bounded
// release eligibility for activation and rollback. [Store] retains snapshot
// documents and commits active-pointer changes with audit/outbox evidence.
// Discovery reads use the active retained document for the selected audience.
package publication

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
)

const ARDContext = "https://agenticresourcediscovery.org/context/v1"

// Entry represents complete publication entries and sparse Search projections.
// Snapshot validation enforces the required fields of complete entries.
type Entry struct {
	Ratings               *ratings.Summary    `json:"https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/ratings/v1#summary,omitempty"`
	Artwork               *artwork.Descriptor `json:"https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/artwork/v1#descriptor,omitempty"`
	Context               any                 `json:"@context,omitempty"`
	ID                    string              `json:"@id,omitempty"`
	Identifier            string              `json:"identifier"`
	DisplayName           string              `json:"displayName,omitempty"`
	Type                  string              `json:"type,omitempty"`
	URL                   string              `json:"url,omitempty"`
	Data                  json.RawMessage     `json:"data,omitempty"`
	RepresentativeQueries []string            `json:"representativeQueries,omitempty"`
	Capabilities          []string            `json:"capabilities,omitempty"`
	Description           string              `json:"description,omitempty"`
	Tags                  []string            `json:"tags,omitempty"`
	Version               string              `json:"version,omitempty"`
	UpdatedAt             string              `json:"updatedAt,omitempty"`
	Metadata              map[string]any      `json:"metadata,omitempty"`
	Subjects              []string            `json:"http://purl.org/dc/terms/subject,omitempty"`
}

type ARDManifest struct {
	Entries []Entry `json:"entries"`
}

type AICatalog struct {
	SpecVersion string           `json:"specVersion"`
	Host        AICatalogHost    `json:"host"`
	Entries     []AICatalogEntry `json:"entries"`
}

type AICatalogHost struct {
	DisplayName      string `json:"displayName"`
	Identifier       string `json:"identifier,omitempty"`
	DocumentationURL string `json:"documentationUrl,omitempty"`
}

type AICatalogEntry struct {
	Identifier  string          `json:"identifier"`
	DisplayName string          `json:"displayName,omitempty"`
	Type        string          `json:"type"`
	URL         string          `json:"url,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	Version     string          `json:"version,omitempty"`
	Description string          `json:"description,omitempty"`
	Tags        []string        `json:"tags,omitempty"`
	UpdatedAt   string          `json:"updatedAt,omitempty"`
	Extensions  map[string]any  `json:"extensions,omitempty"`
}

type Exclusion struct {
	ResourceID string   `json:"resourceId"`
	Identifier string   `json:"identifier"`
	Profiles   []string `json:"profiles"`
	Reasons    []string `json:"reasons"`
}

type SnapshotState string

const (
	SnapshotCandidate SnapshotState = "candidate"
	SnapshotActive    SnapshotState = "active"
	SnapshotRetired   SnapshotState = "retired"
)

type Audience string

const (
	AudienceAnonymous     Audience = "anonymous"
	AudienceAuthenticated Audience = "authenticated"
)

type Snapshot struct {
	ID               string        `json:"id"`
	TenantID         string        `json:"tenantId"`
	Host             string        `json:"host"`
	Audience         Audience      `json:"audience"`
	Generation       int64         `json:"generation"`
	ARDProfile       string        `json:"ardProfile"`
	AICatalogProfile string        `json:"aiCatalogProfile"`
	State            SnapshotState `json:"state"`
	ARD              ARDManifest   `json:"ard"`
	AICatalog        AICatalog     `json:"aiCatalog"`
	Exclusions       []Exclusion   `json:"exclusions"`
	ArtifactIDs      []string      `json:"artifactIds"`
	ETag             string        `json:"etag"`
	AICatalogETag    string        `json:"aiCatalogEtag"`
	ARDContentDigest string        `json:"ardContentDigest"`
	AIContentDigest  string        `json:"aiCatalogContentDigest"`
	CreatedAt        time.Time     `json:"createdAt"`
	ActivatedAt      *time.Time    `json:"activatedAt,omitempty"`
}

type SnapshotSummary struct {
	ID               string        `json:"id"`
	TenantID         string        `json:"tenantId"`
	Host             string        `json:"host"`
	Audience         Audience      `json:"audience"`
	Generation       int64         `json:"generation"`
	ARDProfile       string        `json:"ardProfile"`
	AICatalogProfile string        `json:"aiCatalogProfile"`
	State            SnapshotState `json:"state"`
	ETag             string        `json:"etag"`
	CreatedAt        time.Time     `json:"createdAt"`
	ActivatedAt      *time.Time    `json:"activatedAt,omitempty"`
}

type SnapshotListRequest struct {
	State           SnapshotState
	AfterGeneration int64
	AfterID         string
	Limit           int
}

// SummarizeSnapshot projects retained snapshot identity, state and counts
// without embedding full discovery documents.
func SummarizeSnapshot(snapshot Snapshot) SnapshotSummary {
	return SnapshotSummary{ID: snapshot.ID, TenantID: snapshot.TenantID, Host: snapshot.Host, Audience: snapshot.Audience, Generation: snapshot.Generation, ARDProfile: snapshot.ARDProfile, AICatalogProfile: snapshot.AICatalogProfile, State: snapshot.State, ETag: snapshot.ETag, CreatedAt: snapshot.CreatedAt, ActivatedAt: snapshot.ActivatedAt}
}

type SearchRequest struct {
	Query      SearchQuery `json:"query"`
	Federation string      `json:"federation,omitempty"`
	PageSize   int         `json:"pageSize,omitempty"`
	PageToken  string      `json:"pageToken,omitempty"`
}

type SearchQuery struct {
	Context any            `json:"@context,omitempty"`
	Text    string         `json:"text"`
	Filter  map[string]any `json:"filter,omitempty"`
}

// UnmarshalJSON preserves strict control decoding while distinguishing omitted
// @context from explicit null at this otherwise arbitrary-JSON boundary.
func (query *SearchQuery) UnmarshalJSON(raw []byte) error {
	type wireQuery SearchQuery
	var wire wireQuery
	if err := jsondoc.Decode(raw, &wire); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if _, present := fields["@context"]; present && wire.Context == nil {
		return errors.New("ARD query @context cannot be null")
	}
	*query = SearchQuery(wire)
	return nil
}

type SearchResult struct {
	Entry
	// TrustManifest is an unverified live peer declaration, not local publication
	// evidence. Keep open framework members and exact numbers in their raw form.
	TrustManifest json.RawMessage `json:"trustManifest,omitempty"`
	Score         int             `json:"score"`
	Source        string          `json:"source"`
}

type Referral struct {
	Identifier  string `json:"identifier"`
	DisplayName string `json:"displayName"`
	Type        string `json:"type"`
	URL         string `json:"url"`
}

type SourceDiagnostic struct {
	Source  string `json:"source"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type SearchResponse struct {
	Tier           string             `json:"-"`
	FallbackReason string             `json:"-"`
	PartialIndex   bool               `json:"-"`
	Results        []SearchResult     `json:"results"`
	Referrals      []Referral         `json:"referrals,omitempty"`
	PageToken      string             `json:"pageToken,omitempty"`
	Partial        bool               `json:"-"`
	Diagnostics    []SourceDiagnostic `json:"-"`
	page           searchPageState
	local          map[string]struct{}
}

type searchPageState struct {
	baseBinding string
	live        *catalog.LiveCursor
	limit       int
	expiresAt   int64
	prefix      string
	generation  int64
	offset      int
	count       int
	total       int
	binding     string
}

// ContainsLocalIdentity reports whether the complete matched local snapshot,
// not only the current page, contains an exact identifier and version.
func (response SearchResponse) ContainsLocalIdentity(identifier, version string) bool {
	_, ok := response.local[identifier+"\x00"+version]
	return ok
}

type ExploreRequest struct {
	Query      *SearchQuery      `json:"query,omitempty"`
	ResultType ExploreResultType `json:"resultType"`
}

type ExploreResultType struct {
	Facets []ExploreFacetRequest `json:"facets"`
}

type ExploreFacetRequest struct {
	Field    string `json:"field"`
	Limit    int    `json:"limit,omitempty"`
	MinCount int    `json:"minCount,omitempty"`
}

type ExploreBucket struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type ExploreFacetResult struct {
	Buckets    []ExploreBucket `json:"buckets"`
	OtherCount int             `json:"otherCount,omitempty"`
}

type ExploreResponse struct {
	Tier           string                        `json:"-"`
	FallbackReason string                        `json:"-"`
	PartialIndex   bool                          `json:"-"`
	ResultType     string                        `json:"resultType"`
	Facets         map[string]ExploreFacetResult `json:"facets"`
}

type ListRequest struct {
	Filter    string
	OrderBy   string
	PageSize  int
	PageToken string
}

type ListResponse struct {
	Items     []Entry `json:"items"`
	Total     int     `json:"total"`
	PageToken string  `json:"pageToken,omitempty"`
}
