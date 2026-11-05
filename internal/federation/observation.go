// Observation integrity binds normalized external entries to retained source,
// retrieval, profile and occurrence evidence without adopting local governance.

package federation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

const observationNormalizationProfile = "catalog-observation-v1"

// Discovery versions use the Control contract's character limit. Presence,
// emptiness and whitespace policies remain owned by each operation.
func validDiscoveryVersion(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0) && utf8.RuneCountInString(value) <= 128
}

const (
	maxCatalogSearchDisplayNameBytes = 512
	maxCatalogSearchDescriptionBytes = 4000
	maxCatalogSearchTags             = 100
	maxCatalogSearchTagCharacters    = 80
	maxCatalogSearchQueries          = 5
	maxCatalogSearchQueryBytes       = 240
	maxCatalogSearchPublisherBytes   = 256
)

// sourceScopedObservationKey distinguishes identity/version groups and uses
// occurrence evidence for entries without an identity.
func sourceScopedObservationKey(identifier, version string, versionPresent bool, retrievalDigest, entryLocation, exactDigest string) string {
	if identifier != "" {
		// 0xff cannot occur in valid UTF-8, so it is an unambiguous field
		// separator for the JSON strings accepted at this boundary. Hashing keeps
		// the internal key bounded and lets migration 000010 reproduce it exactly.
		preimage := make([]byte, 0, len(identifier)+len(version)+3)
		preimage = append(preimage, identifier...)
		preimage = append(preimage, 0xff)
		if versionPresent {
			preimage = append(preimage, 1)
		} else {
			preimage = append(preimage, 0)
		}
		preimage = append(preimage, 0xff)
		preimage = append(preimage, version...)
		digest := sha256.Sum256(preimage)
		return "identity:v1:sha256:" + hex.EncodeToString(digest[:])
	}
	return "entry:" + retrievalDigest + ":" + entryLocation + ":" + exactDigest
}

type normalizedObservationEntry struct {
	Profile               string          `json:"profile"`
	Identifier            string          `json:"identifier"`
	VersionPresent        bool            `json:"versionPresent"`
	Version               string          `json:"version"`
	Kind                  catalog.Kind    `json:"kind"`
	MediaType             string          `json:"mediaType"`
	DisplayName           string          `json:"displayName"`
	Description           string          `json:"description"`
	URL                   string          `json:"url"`
	Data                  json.RawMessage `json:"data"`
	Tags                  []string        `json:"tags"`
	RepresentativeQueries []string        `json:"representativeQueries"`
	Metadata              json.RawMessage `json:"metadata"`
	UnknownProperties     map[string]any  `json:"unknownProperties,omitempty"`
}

// normalizeObservationEntry creates deterministic semantic bytes from validated
// entry fields while preserving explicit unknown-property evidence.
func normalizeObservationEntry(entry sourceEntry, identifier string, kind catalog.Kind) ([]byte, string) {
	data, ok := normalizeJSONValue(entry.Data)
	if !ok {
		return nil, ""
	}
	metadata, ok := normalizeJSONValue(entry.Metadata)
	if !ok {
		return nil, ""
	}
	tags := normalizeStrings(entry.Tags)
	queries := normalizedSearchQueries(entry.RepresentativeQueries)
	value := normalizedObservationEntry{
		Profile: observationNormalizationProfile, Identifier: identifier, VersionPresent: entry.VersionPresent, Version: entry.Version,
		Kind: kind, MediaType: strings.ToLower(strings.TrimSpace(entry.Type)), DisplayName: strings.Join(strings.Fields(entry.DisplayName), " "), Description: strings.TrimSpace(entry.Description), URL: strings.TrimSpace(entry.URL), Data: data, Tags: tags, RepresentativeQueries: queries, Metadata: metadata, UnknownProperties: entry.UnknownProperties,
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, ""
	}
	digest := sha256.Sum256(raw)
	return raw, "sha256:" + hex.EncodeToString(digest[:])
}

// normalizeJSONValue returns deterministic JSON for a valid raw value and
// preserves an absent value as nil.
func normalizeJSONValue(raw json.RawMessage) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	value, ok := decodeJSONValue(raw)
	if !ok {
		return nil, false
	}
	normalized, err := json.Marshal(value)
	return normalized, err == nil
}

// decodeJSONValue decodes one JSON value, rejecting trailing values and preserving
// number spelling. It does not reject duplicate object members.
func decodeJSONValue(raw json.RawMessage) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	return value, true
}

// normalizedUnknownProperties converts retained extension values into
// deterministic JSON-shaped evidence.
func normalizedUnknownProperties(properties map[string]json.RawMessage) map[string]any {
	if len(properties) == 0 {
		return nil
	}
	normalized := make(map[string]any, len(properties))
	for name, raw := range properties {
		value, ok := decodeJSONValue(raw)
		if !ok {
			continue
		}
		normalized[name] = value
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

// ValidateSnapshotCommit keeps the in-memory and PostgreSQL stores aligned on
// the evidence that current writers must preserve for every committed entry.
func ValidateSnapshotCommit(snapshot Snapshot, observations []Observation) error {
	if snapshot.ID == "" || snapshot.TenantID == "" || snapshot.SourceID == "" || snapshot.SourceRevision <= 0 || snapshot.CompletedAt == nil || snapshot.StartedAt.IsZero() || snapshot.CompletedAt.Before(snapshot.StartedAt) || snapshot.MemberCount != len(observations) || snapshot.Added < 0 || snapshot.Updated < 0 || snapshot.Added+snapshot.Updated > snapshot.MemberCount || snapshot.WithdrawalAuthority && snapshot.ReconcileCurrentView {
		return catalog.E(catalog.CodeInvalidArgument, "source snapshot completion evidence is invalid")
	}
	switch snapshot.State {
	case SnapshotComplete:
		if !snapshot.EnumerationComplete {
			return catalog.E(catalog.CodeInvalidArgument, "a complete source snapshot must finish enumeration")
		}
	case SnapshotIncomplete, SnapshotFailed:
		if snapshot.EnumerationComplete || snapshot.WithdrawalAuthority {
			return catalog.E(catalog.CodeInvalidArgument, "an incomplete source snapshot cannot change active membership")
		}
	case SnapshotRunning:
		return catalog.E(catalog.CodeInvalidArgument, "a running source snapshot cannot be committed")
	default:
		return catalog.E(catalog.CodeInvalidArgument, "source snapshot state is not committable")
	}
	if snapshot.WithdrawalAuthority && snapshot.State != SnapshotComplete {
		return catalog.E(catalog.CodeInvalidArgument, "withdrawal authority requires a complete source snapshot")
	}
	if snapshot.ReconcileCurrentView && snapshot.State != SnapshotComplete {
		return catalog.E(catalog.CodeInvalidArgument, "current-view reconciliation requires a complete source snapshot")
	}
	if snapshot.State == SnapshotFailed && len(observations) != 0 {
		return catalog.E(catalog.CodeInvalidArgument, "a failed source snapshot cannot retain enumerated members")
	}
	if err := validateSnapshotRetrievals(snapshot); err != nil {
		return err
	}
	observationIDs := make(map[string]struct{}, len(observations))
	occurrences := make(map[string]struct{}, len(observations))
	for _, observation := range observations {
		if err := ValidateArtworkMetadata(observation.ArtworkMetadata); err != nil {
			return err
		}
		if _, duplicate := observationIDs[observation.ID]; duplicate {
			return catalog.E(catalog.CodeInvalidArgument, "source snapshot contains a duplicate observation identifier")
		}
		observationIDs[observation.ID] = struct{}{}
		occurrenceKey := observation.Occurrence.RetrievalDigest + "\x00" + observation.Occurrence.EntryLocation
		if _, duplicate := occurrences[occurrenceKey]; duplicate {
			return catalog.E(catalog.CodeInvalidArgument, "source snapshot contains a duplicate observation occurrence")
		}
		occurrences[occurrenceKey] = struct{}{}
		if snapshot.State == SnapshotComplete && observation.Freshness != FreshCurrent || snapshot.State == SnapshotIncomplete && observation.Freshness != FreshStale {
			return catalog.E(catalog.CodeInvalidArgument, "source observation freshness does not match snapshot completeness")
		}
		if err := validateObservationCommit(snapshot, observation); err != nil {
			return err
		}
	}
	return nil
}

// ValidateRetainedSnapshotIntegrity checks the immutable snapshot aggregate
// without requiring all occurrence members to be loaded into memory. Current
// source-revision provenance is required.
func ValidateRetainedSnapshotIntegrity(snapshot Snapshot) error {
	if snapshot.ID == "" || snapshot.TenantID == "" || snapshot.SourceID == "" || snapshot.StartedAt.IsZero() || snapshot.MemberCount < 0 || snapshot.MemberCount > MaxSynchronizationEntries || snapshot.Added < 0 || snapshot.Added > MaxSynchronizationEntries || snapshot.Updated < 0 || snapshot.Updated > MaxSynchronizationEntries || snapshot.Withdrawn < 0 || snapshot.Withdrawn > MaxSynchronizationEntries || snapshot.Added+snapshot.Updated > snapshot.MemberCount || snapshot.WithdrawalAuthority && snapshot.ReconcileCurrentView {
		return catalog.E(catalog.CodeInvalidArgument, "source snapshot completion evidence is invalid")
	}
	switch snapshot.State {
	case SnapshotRunning:
		if snapshot.CompletedAt != nil || snapshot.EnumerationComplete || snapshot.WithdrawalAuthority {
			return catalog.E(catalog.CodeInvalidArgument, "running source snapshot evidence is invalid")
		}
	case SnapshotComplete:
		if snapshot.CompletedAt == nil || !snapshot.EnumerationComplete {
			return catalog.E(catalog.CodeInvalidArgument, "complete source snapshot evidence is invalid")
		}
	case SnapshotIncomplete, SnapshotFailed:
		if snapshot.CompletedAt == nil || snapshot.EnumerationComplete || snapshot.WithdrawalAuthority {
			return catalog.E(catalog.CodeInvalidArgument, "incomplete source snapshot evidence is invalid")
		}
	default:
		return catalog.E(catalog.CodeInvalidArgument, "source snapshot state is invalid")
	}
	if snapshot.CompletedAt != nil && snapshot.CompletedAt.Before(snapshot.StartedAt) || snapshot.WithdrawalAuthority && snapshot.State != SnapshotComplete || snapshot.ReconcileCurrentView && snapshot.State != SnapshotComplete {
		return catalog.E(catalog.CodeInvalidArgument, "source snapshot timing or authority evidence is invalid")
	}
	if snapshot.SourceRevision <= 0 {
		return catalog.E(catalog.CodeDependencyUnavailable, "source snapshot requires current provenance")
	}
	if !validProfileReference(snapshot.PolicyID) || snapshot.PolicyRevision < 1 || !validPolicyDigest(snapshot.PolicyDigest) || !safeRetrievalText(snapshot.CredentialRevision, 128, true) || snapshot.State != SnapshotFailed && !validPolicyDigest(snapshot.BodyDigest) || snapshot.State == SnapshotFailed && snapshot.BodyDigest != "" && !validPolicyDigest(snapshot.BodyDigest) {
		return catalog.E(catalog.CodeInvalidArgument, "source snapshot policy or content evidence is invalid")
	}
	if err := validateSnapshotRetrievals(snapshot); err != nil {
		return err
	}
	return nil
}

// ValidateRetainedOccurrenceIntegrity verifies one immutable occurrence
// without loading another occurrence or any private retrieval document. A
// successful result always has complete current source-revision provenance.
func ValidateRetainedOccurrenceIntegrity(occurrence ObservationOccurrence, observation Observation, snapshot Snapshot) error {
	if occurrence.ObservationID == "" || occurrence.SourceID == "" || occurrence.SnapshotID == "" || occurrence.ObservationID != observation.ID || occurrence.SourceID != observation.SourceID || occurrence.SnapshotID != snapshot.ID || observation.TenantID != snapshot.TenantID || observation.SourceID != snapshot.SourceID || occurrence.ExactEntryDigest != observation.ExactEntryDigest || occurrence.NormalizedEntryDigest != observation.NormalizedEntryDigest || !validPolicyDigest(occurrence.RetrievalDigest) || occurrence.ObservedAt.IsZero() || occurrence.ObservedAt.Before(observation.FirstSeenAt) || occurrence.ObservedAt.After(observation.LastSeenAt) || snapshot.CompletedAt == nil || occurrence.ObservedAt.Before(snapshot.StartedAt) || occurrence.ObservedAt.After(*snapshot.CompletedAt) || snapshot.State == SnapshotRunning || snapshot.State == SnapshotFailed {
		return catalog.E(catalog.CodeDependencyUnavailable, "source observation occurrence evidence is invalid")
	}

	if occurrence.SourceRevision <= 0 || !occurrence.EvidenceComplete || occurrence.SourceRevision != snapshot.SourceRevision || !validEntryLocation(occurrence.EntryLocation) || occurrence.NormalizationInputDigest != "" && !validPolicyDigest(occurrence.NormalizationInputDigest) || observation.IdentityDigest == "" || observation.IdentityDigest != observationIdentityDigest(observation.ExactEntryDigest, occurrence.NormalizationInputDigest) {
		return catalog.E(catalog.CodeDependencyUnavailable, "source observation occurrence provenance is invalid")
	}
	return nil
}

// ValidateRetainedObservationIntegrity verifies exact and normalized bytes,
// context-sensitive identity, and derived lexical fields for one observation.
// Missing exact bytes or identity is invalid, not an alternate retained format.
func ValidateRetainedObservationIntegrity(observation Observation, normalizationInputDigest string, projectionComplete bool) error {
	if observation.FirstSeenAt.IsZero() || observation.LastSeenAt.IsZero() || observation.LastSeenAt.Before(observation.FirstSeenAt) {
		return catalog.E(catalog.CodeDependencyUnavailable, "source observation timestamps are invalid")
	}
	if len(observation.RawEntry) == 0 || observation.IdentityDigest == "" {
		return catalog.E(catalog.CodeDependencyUnavailable, "source observation requires exact bytes and current identity")
	}
	if jsondoc.Validate(observation.RawEntry) != nil || observation.ExactEntryDigest != observationDigest(observation.RawEntry) || observation.IdentityDigest != observationIdentityDigest(observation.ExactEntryDigest, normalizationInputDigest) {
		return catalog.E(catalog.CodeDependencyUnavailable, "source observation exact entry evidence is invalid")
	}
	hasNormalized := observation.NormalizedEntryDigest != "" || observation.NormalizationProfile != "" || len(observation.NormalizedEntry) > 0
	if !hasNormalized {
		if projectionComplete || observation.SearchDisplayName != "" || observation.SearchDescription != "" || len(observation.SearchTags) != 0 || len(observation.SearchQueries) != 0 || observation.SearchPublisher != "" {
			return catalog.E(catalog.CodeDependencyUnavailable, "source observation derived projection exists without normalized evidence")
		}
		return nil
	}
	if observation.NormalizedEntryDigest == "" || observation.NormalizationProfile != observationNormalizationProfile || len(observation.NormalizedEntry) == 0 || jsondoc.Validate(observation.NormalizedEntry) != nil || observation.NormalizedEntryDigest != observationDigest(observation.NormalizedEntry) {
		return catalog.E(catalog.CodeDependencyUnavailable, "source observation normalized entry evidence is invalid")
	}
	if !projectionComplete {
		return nil
	}
	var normalized normalizedObservationEntry
	identifier, identifierErr := catalog.ParseAIRIdentifier(observation.Identifier)
	if err := json.Unmarshal(observation.NormalizedEntry, &normalized); err != nil || normalized.Profile != observation.NormalizationProfile || normalized.Identifier != observation.Identifier || normalized.VersionPresent != observation.VersionPresent || normalized.Version != observation.Version || normalized.Kind != observation.Kind || normalized.MediaType != strings.ToLower(strings.TrimSpace(observation.MediaType)) || identifierErr != nil || observation.SearchDisplayName != normalized.DisplayName || observation.SearchDescription != normalized.Description || !slices.Equal(observation.SearchTags, normalizedSearchTags(normalized.Tags)) || !slices.Equal(observation.SearchQueries, normalized.RepresentativeQueries) || observation.SearchPublisher != identifier.Publisher || !validCatalogSearchProjection(observation.SearchDisplayName, observation.SearchDescription, observation.SearchTags, observation.SearchQueries, observation.SearchPublisher) {
		return catalog.E(catalog.CodeDependencyUnavailable, "source observation normalized evidence and derived projection are inconsistent")
	}
	return nil
}

// validateObservationCommit binds raw, normalized, search, and occurrence
// evidence to the exact snapshot before either store can retain it.
func validateObservationCommit(snapshot Snapshot, observation Observation) error {
	occurrence := observation.Occurrence
	if observation.ID == "" || observation.SourceKey == "" || observation.TenantID != snapshot.TenantID || observation.SourceID != snapshot.SourceID || observation.SnapshotID != snapshot.ID || occurrence.ObservationID != observation.ID || occurrence.SourceID != snapshot.SourceID || occurrence.SnapshotID != snapshot.ID || occurrence.SourceRevision != snapshot.SourceRevision {
		return catalog.E(catalog.CodeInvalidArgument, "source observation does not belong to the committed snapshot")
	}
	if !observation.RawEntryAvailable || len(observation.RawEntry) == 0 || jsondoc.Validate(observation.RawEntry) != nil || observation.ExactEntryDigest != observationDigest(observation.RawEntry) || observation.IdentityDigest != observationIdentityDigest(observation.ExactEntryDigest, occurrence.NormalizationInputDigest) {
		return catalog.E(catalog.CodeInvalidArgument, "source observation exact entry evidence is invalid")
	}
	if occurrence.ExactEntryDigest != observation.ExactEntryDigest || !validPolicyDigest(occurrence.RetrievalDigest) || !validEntryLocation(occurrence.EntryLocation) || !occurrence.EvidenceComplete || occurrence.ObservedAt.IsZero() || !occurrence.ObservedAt.Equal(observation.LastSeenAt) || occurrence.ObservedAt.Before(snapshot.StartedAt) || occurrence.ObservedAt.After(*snapshot.CompletedAt) || occurrence.NormalizationInputDigest != "" && !validPolicyDigest(occurrence.NormalizationInputDigest) {
		return catalog.E(catalog.CodeInvalidArgument, "source observation occurrence evidence is invalid")
	}
	hasNormalized := observation.NormalizedEntryDigest != "" || observation.NormalizationProfile != "" || len(observation.NormalizedEntry) > 0
	if hasNormalized {
		if observation.NormalizedEntryDigest == "" || observation.NormalizationProfile != observationNormalizationProfile || len(observation.NormalizedEntry) == 0 || jsondoc.Validate(observation.NormalizedEntry) != nil || observation.NormalizedEntryDigest != observationDigest(observation.NormalizedEntry) || occurrence.NormalizedEntryDigest != observation.NormalizedEntryDigest {
			return catalog.E(catalog.CodeInvalidArgument, "source observation normalized entry evidence is invalid")
		}
		var normalized normalizedObservationEntry
		identifier, identifierErr := catalog.ParseAIRIdentifier(observation.Identifier)
		if err := json.Unmarshal(observation.NormalizedEntry, &normalized); err != nil || normalized.Profile != observation.NormalizationProfile || normalized.Identifier != observation.Identifier || normalized.VersionPresent != observation.VersionPresent || normalized.Version != observation.Version || normalized.Kind != observation.Kind || normalized.MediaType != strings.ToLower(strings.TrimSpace(observation.MediaType)) || observation.Identifier == "" || !observation.Kind.Valid() || observation.Compatibility.Structural != "valid" || observation.Compatibility.Support != "supported" || observation.VersionPresent && (observation.Version == "" || !validDiscoveryVersion(observation.Version)) || !observation.VersionPresent && observation.Version != "" || identifierErr != nil || observation.SearchDisplayName != normalized.DisplayName || observation.SearchDescription != normalized.Description || !slices.Equal(observation.SearchTags, normalizedSearchTags(normalized.Tags)) || !slices.Equal(observation.SearchQueries, normalized.RepresentativeQueries) || observation.SearchPublisher != identifier.Publisher || !validCatalogSearchProjection(observation.SearchDisplayName, observation.SearchDescription, observation.SearchTags, observation.SearchQueries, observation.SearchPublisher) {
			return catalog.E(catalog.CodeInvalidArgument, "source observation normalized entry does not match its identity and compatibility")
		}
	} else if occurrence.NormalizedEntryDigest != "" || observation.SearchDisplayName != "" || observation.SearchDescription != "" || len(observation.SearchTags) != 0 || len(observation.SearchQueries) != 0 || observation.SearchPublisher != "" {
		return catalog.E(catalog.CodeInvalidArgument, "source observation normalized entry evidence is invalid")
	}
	if observation.FirstSeenAt.IsZero() || observation.LastSeenAt.IsZero() || observation.LastSeenAt.Before(observation.FirstSeenAt) {
		return catalog.E(catalog.CodeInvalidArgument, "source observation timestamps are invalid")
	}
	return nil
}

// validCatalogSearchProjection enforces bounded derived text and collections
// before they enter indexed Catalog search.
func validCatalogSearchProjection(displayName, description string, tags, queries []string, publisher string) bool {
	if len(displayName) > maxCatalogSearchDisplayNameBytes || len(description) > maxCatalogSearchDescriptionBytes || len(publisher) > maxCatalogSearchPublisherBytes || strings.ContainsRune(displayName, '\x00') || strings.ContainsRune(description, '\x00') || strings.ContainsRune(publisher, '\x00') || len(tags) > maxCatalogSearchTags || len(queries) > maxCatalogSearchQueries {
		return false
	}
	for _, value := range tags {
		if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxCatalogSearchTagCharacters || strings.ContainsRune(value, '\x00') {
			return false
		}
	}
	for _, value := range queries {
		if value == "" || len(value) > maxCatalogSearchQueryBytes || strings.ContainsRune(value, '\x00') {
			return false
		}
	}
	return true
}

// normalizedSearchTags canonicalizes tag values for deterministic indexed
// filtering.
func normalizedSearchTags(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" && !containsString(result, value) {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

// normalizedSearchQueries normalizes representative queries for the bounded
// lexical projection.
func normalizedSearchQueries(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.Join(strings.Fields(value), " ")
		if value != "" && !containsString(result, value) {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

// observationDigest hashes exact retained bytes with the source-observation
// digest convention.
func observationDigest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// validEntryLocation requires a bounded, control-free source-entry path with
// the supported indexed location shape.
func validEntryLocation(value string) bool {
	if len(value) < len("/items/0") || len(value) > 1024 || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	prefix := "/entries/"
	if strings.HasPrefix(value, "/items/") {
		prefix = "/items/"
	} else if !strings.HasPrefix(value, prefix) {
		return false
	}
	index, err := strconv.ParseUint(strings.TrimPrefix(value, prefix), 10, 64)
	return err == nil && value == prefix+strconv.FormatUint(index, 10)
}

// containsString reports exact membership in an already normalized source value
// list.
func containsString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
