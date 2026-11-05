// Audit exports bind deterministic record bytes to bounded query manifests and
// independently retained recovery-point evidence.

package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

// RangeTooLargeError means the requested range exceeded an aggregate limit.
type RangeTooLargeError struct{ Reason string }

// Error describes which bounded export constraint was exceeded.
func (e *RangeTooLargeError) Error() string { return "audit export range is too large: " + e.Reason }

// NormalizeQuery validates exact filters. requireRange requires both half-open
// endpoints, as used by export and restored-database comparison.
func NormalizeQuery(query Query, requireRange bool) (Query, error) {
	for name, value := range map[string]string{"actor filter": query.Actor, "target-type filter": query.TargetType, "target-ID filter": query.TargetID} {
		if value != "" {
			maximum := MaxActorBytes
			switch name {
			case "target-type filter":
				maximum = MaxTargetTypeBytes
			case "target-ID filter":
				maximum = MaxTargetIDBytes
			}
			if err := boundedText(name, value, maximum); err != nil {
				return Query{}, err
			}
		}
	}
	if len(query.Actor) > MaxActorBytes || len(query.Action) > MaxActionBytes || len(query.RequestID) > MaxRequestIDBytes || len(query.TargetType) > MaxTargetTypeBytes || len(query.TargetID) > MaxTargetIDBytes {
		return Query{}, errors.New("audit filter exceeds its byte limit")
	}
	if query.Action != "" && !actionPattern.MatchString(query.Action) {
		return Query{}, errors.New("audit action filter is invalid")
	}
	if query.RequestID != "" && !requestIDPattern.MatchString(query.RequestID) {
		return Query{}, errors.New("audit request-ID filter is invalid")
	}
	if (query.TargetType == "") != (query.TargetID == "") {
		return Query{}, errors.New("audit targetType and targetId filters must be provided together")
	}
	if requireRange && (query.OccurredFrom == nil || query.OccurredBefore == nil) {
		return Query{}, errors.New("audit export requires occurredFrom and occurredBefore")
	}
	if query.OccurredFrom != nil {
		value := query.OccurredFrom.UTC().Truncate(time.Microsecond)
		query.OccurredFrom = &value
	}
	if query.OccurredBefore != nil {
		value := query.OccurredBefore.UTC().Truncate(time.Microsecond)
		query.OccurredBefore = &value
	}
	if query.OccurredFrom != nil && query.OccurredBefore != nil && !query.OccurredFrom.Before(*query.OccurredBefore) {
		return Query{}, errors.New("audit occurredFrom must be before occurredBefore")
	}
	return query, nil
}

// BuildExport validates ordering and limits, then returns the exact closed
// response bytes and decoded envelope.
func BuildExport(tenantID string, query Query, cutoff, generatedAt time.Time, records []Record) (Export, []byte, error) {
	query, err := NormalizeQuery(query, true)
	if err != nil {
		return Export{}, nil, err
	}
	filters := Filters{Actor: query.Actor, Action: query.Action, RequestID: query.RequestID, TargetType: query.TargetType, TargetID: query.TargetID, OccurredFrom: *query.OccurredFrom, OccurredBefore: *query.OccurredBefore}
	if !uuidPattern.MatchString(tenantID) {
		return Export{}, nil, errors.New("audit export tenant identifier is invalid")
	}
	cutoff = cutoff.UTC().Truncate(time.Microsecond)
	generatedAt = generatedAt.UTC().Truncate(time.Microsecond)
	if cutoff.IsZero() || generatedAt.IsZero() || cutoff.Before(filters.OccurredBefore) || generatedAt.Before(cutoff) {
		return Export{}, nil, errors.New("audit export cutoff or generation time is invalid")
	}
	if len(records) > MaxExportRows {
		return Export{}, nil, &RangeTooLargeError{Reason: "row limit exceeded"}
	}
	normalizedRecords := make([]Record, len(records))
	copy(normalizedRecords, records)
	var previous Record
	for index := range normalizedRecords {
		normalizedRecords[index].At = normalizedRecords[index].At.UTC().Truncate(time.Microsecond)
		record := normalizedRecords[index]
		if !uuidPattern.MatchString(record.ID) || !uuidPattern.MatchString(record.TenantID) || record.At.IsZero() || record.TenantID != tenantID || !recordMatchesQuery(record, query) {
			return Export{}, nil, errors.New("audit export selection does not match its manifest")
		}
		if index > 0 && comparePosition(previous, record) >= 0 {
			return Export{}, nil, errors.New("audit export records are not strictly ascending")
		}
		previous = record
		encoded, encodeErr := json.Marshal(record)
		if encodeErr != nil {
			return Export{}, nil, fmt.Errorf("encode audit event %s: %w", record.ID, encodeErr)
		}
		if len(encoded) > MaxEncodedRecordBytes {

			return Export{}, nil, errors.New("version-1 audit event exceeds its invariant record limit")
		}
	}
	digest, rawEvents, err := DigestRecords(normalizedRecords)
	if err != nil {
		return Export{}, nil, err
	}
	manifest := Manifest{EncodingProfile: ExportEncodingProfile, TenantID: strings.ToLower(tenantID), Filters: filters, Cutoff: cutoff, RowCount: len(records), EventsDigest: digest, GeneratedAt: generatedAt}
	if len(normalizedRecords) > 0 {
		manifest.First = &Position{OccurredAt: normalizedRecords[0].At, ID: normalizedRecords[0].ID}
		last := normalizedRecords[len(normalizedRecords)-1]
		manifest.Last = &Position{OccurredAt: last.At.UTC(), ID: last.ID}
	}
	export := Export{FormatVersion: ExportFormatV1, Manifest: manifest, RawEvents: json.RawMessage(rawEvents)}
	encoded, err := json.Marshal(export)
	if err != nil {
		return Export{}, nil, fmt.Errorf("encode audit export: %w", err)
	}
	if len(encoded) > MaxExportResponseBytes {
		return Export{}, nil, &RangeTooLargeError{Reason: "encoded response limit exceeded"}
	}
	return export, encoded, nil
}

// recordMatchesQuery applies exact filters and the half-open time interval to
// an audit record.
func recordMatchesQuery(record Record, query Query) bool {
	return (query.Actor == "" || record.Actor == query.Actor) &&
		(query.Action == "" || record.Action == query.Action) &&
		(query.RequestID == "" || record.RequestID == query.RequestID) &&
		(query.TargetType == "" || record.TargetType == query.TargetType && record.TargetID == query.TargetID) &&
		(query.OccurredFrom == nil || !record.At.Before(*query.OccurredFrom)) &&
		(query.OccurredBefore == nil || record.At.Before(*query.OccurredBefore))
}

// VerifyExport validates a bounded closed file and its embedded manifest.
// It proves self-consistency, not authenticity or completeness against an
// independently trusted database snapshot; no external authority is consulted.
func VerifyExport(raw []byte) (Export, VerificationReport, error) {
	if len(raw) == 0 || len(raw) > MaxExportFileBytes {
		return Export{}, VerificationReport{}, errors.New("audit export file is empty or exceeds its byte limit")
	}
	if err := jsondoc.Validate(raw); err != nil {
		return Export{}, VerificationReport{}, fmt.Errorf("validate audit export JSON: %w", err)
	}
	manifestRaw, err := requiredObjectMember(raw, "manifest")
	if err != nil {
		return Export{}, VerificationReport{}, err
	}
	if _, err := requiredObjectMember(manifestRaw, "rowCount"); err != nil {
		return Export{}, VerificationReport{}, err
	}
	var wire struct {
		FormatVersion int             `json:"formatVersion"`
		Manifest      Manifest        `json:"manifest"`
		Events        json.RawMessage `json:"events"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&wire); err != nil {
		return Export{}, VerificationReport{}, fmt.Errorf("decode audit export: %w", err)
	}
	if wire.FormatVersion != ExportFormatV1 || wire.Manifest.EncodingProfile != ExportEncodingProfile || !uuidPattern.MatchString(wire.Manifest.TenantID) || wire.Manifest.Cutoff.IsZero() || wire.Manifest.GeneratedAt.IsZero() {
		return Export{}, VerificationReport{}, errors.New("audit export manifest header is invalid")
	}
	query := Query{Actor: wire.Manifest.Filters.Actor, Action: wire.Manifest.Filters.Action, RequestID: wire.Manifest.Filters.RequestID, TargetType: wire.Manifest.Filters.TargetType, TargetID: wire.Manifest.Filters.TargetID, OccurredFrom: &wire.Manifest.Filters.OccurredFrom, OccurredBefore: &wire.Manifest.Filters.OccurredBefore}
	if _, err := NormalizeQuery(query, true); err != nil || wire.Manifest.Cutoff.Before(wire.Manifest.Filters.OccurredBefore) {
		return Export{}, VerificationReport{}, errors.New("audit export manifest filters are invalid")
	}
	if len(wire.Events) == 0 || len(wire.Events) > MaxExportResponseBytes {
		return Export{}, VerificationReport{}, errors.New("audit export events array is missing or oversized")
	}
	if err := jsondoc.Validate(wire.Events); err != nil {
		return Export{}, VerificationReport{}, fmt.Errorf("validate audit export events: %w", err)
	}
	var records []Record
	eventsDecoder := json.NewDecoder(bytes.NewReader(wire.Events))
	eventsDecoder.DisallowUnknownFields()
	eventsDecoder.UseNumber()
	if err := eventsDecoder.Decode(&records); err != nil {
		return Export{}, VerificationReport{}, fmt.Errorf("decode audit export events: %w", err)
	}
	if records == nil {
		return Export{}, VerificationReport{}, errors.New("audit export events must be an array")
	}
	if len(records) > MaxExportRows {
		return Export{}, VerificationReport{}, errors.New("audit export row limit exceeded")
	}
	rebuilt, _, err := BuildExport(wire.Manifest.TenantID, query, wire.Manifest.Cutoff, wire.Manifest.GeneratedAt, records)
	if err != nil {
		return Export{}, VerificationReport{}, err
	}
	if !ManifestsEqual(rebuilt.Manifest, wire.Manifest) || !slices.Equal(rebuilt.RawEvents, wire.Events) {
		return Export{}, VerificationReport{}, errors.New("audit export manifest or canonical event encoding does not match its contents")
	}
	report := VerificationReport{Mode: "file", Valid: true, RowCount: rebuilt.Manifest.RowCount, EventsDigest: rebuilt.Manifest.EventsDigest, AssuranceBoundary: "structural self-consistency only; this file alone cannot prove owner-level omission or rewrite"}
	return Export{FormatVersion: wire.FormatVersion, Manifest: wire.Manifest, RawEvents: append(json.RawMessage(nil), wire.Events...)}, report, nil
}

// ParseTrustedManifest validates a separately retained manifest and required
// recovery-point identifier.
func ParseTrustedManifest(raw []byte, recoveryPointID string) (TrustedManifest, error) {
	if len(raw) == 0 || len(raw) > 256<<10 {
		return TrustedManifest{}, errors.New("trusted audit manifest and recovery-point ID are required and bounded")
	}
	if err := jsondoc.Validate(raw); err != nil {
		return TrustedManifest{}, fmt.Errorf("validate trusted audit manifest JSON: %w", err)
	}
	manifestRaw, err := requiredObjectMember(raw, "manifest")
	if err != nil {
		return TrustedManifest{}, err
	}
	if _, err := requiredObjectMember(manifestRaw, "rowCount"); err != nil {
		return TrustedManifest{}, err
	}
	var trusted TrustedManifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&trusted); err != nil {
		return TrustedManifest{}, fmt.Errorf("decode trusted audit manifest: %w", err)
	}
	if err := ValidateTrustedManifest(trusted, recoveryPointID); err != nil {
		return TrustedManifest{}, err
	}
	return trusted, nil
}

// BindTrustedManifest binds a verified export manifest to recovery provenance
// that an external backup workflow will retain separately.
func BindTrustedManifest(export Export, recoveryPointID string) (TrustedManifest, error) {
	trusted := TrustedManifest{FormatVersion: ExportFormatV1, RecoveryPointID: recoveryPointID, Manifest: export.Manifest}
	if err := ValidateTrustedManifest(trusted, recoveryPointID); err != nil {
		return TrustedManifest{}, err
	}
	return trusted, nil
}

// ValidateRecoveryPointID applies the bounded safe-text contract shared by
// restore inspection and trusted audit-manifest binding. The value remains an
// operator-supplied correlation label; validation does not make it proof of a
// backup's identity or provenance.
func ValidateRecoveryPointID(recoveryPointID string) error {
	if boundedText("recovery-point ID", recoveryPointID, 512) != nil {
		return errors.New("recovery-point ID is required and bounded")
	}
	return nil
}

// ValidateTrustedManifest validates the semantic manifest and exact external
// recovery-point binding without opening a database.
func ValidateTrustedManifest(trusted TrustedManifest, recoveryPointID string) error {
	if ValidateRecoveryPointID(recoveryPointID) != nil || trusted.FormatVersion != ExportFormatV1 || trusted.RecoveryPointID != recoveryPointID || trusted.Manifest.EncodingProfile != ExportEncodingProfile || !uuidPattern.MatchString(trusted.Manifest.TenantID) || trusted.Manifest.EventsDigest == "" || trusted.Manifest.RowCount < 0 || trusted.Manifest.RowCount > MaxExportRows || trusted.Manifest.Cutoff.IsZero() || trusted.Manifest.GeneratedAt.IsZero() || trusted.Manifest.GeneratedAt.Before(trusted.Manifest.Cutoff) {
		return errors.New("trusted audit manifest or recovery-point binding is invalid")
	}
	query := Query{Actor: trusted.Manifest.Filters.Actor, Action: trusted.Manifest.Filters.Action, RequestID: trusted.Manifest.Filters.RequestID, TargetType: trusted.Manifest.Filters.TargetType, TargetID: trusted.Manifest.Filters.TargetID, OccurredFrom: &trusted.Manifest.Filters.OccurredFrom, OccurredBefore: &trusted.Manifest.Filters.OccurredBefore}
	if _, err := NormalizeQuery(query, true); err != nil || trusted.Manifest.Cutoff.Before(trusted.Manifest.Filters.OccurredBefore) {
		return errors.New("trusted audit manifest range is invalid")
	}
	if !digestPattern.MatchString(trusted.Manifest.EventsDigest) {
		return errors.New("trusted audit manifest digest is invalid")
	}
	if trusted.Manifest.RowCount == 0 && (trusted.Manifest.First != nil || trusted.Manifest.Last != nil) || trusted.Manifest.RowCount > 0 && (trusted.Manifest.First == nil || trusted.Manifest.Last == nil) {
		return errors.New("trusted audit manifest positions do not match its row count")
	}
	if trusted.Manifest.RowCount > 0 {
		first, last := trusted.Manifest.First, trusted.Manifest.Last
		if !uuidPattern.MatchString(first.ID) || !uuidPattern.MatchString(last.ID) || first.OccurredAt.Before(trusted.Manifest.Filters.OccurredFrom) || !first.OccurredAt.Before(trusted.Manifest.Filters.OccurredBefore) || last.OccurredAt.Before(trusted.Manifest.Filters.OccurredFrom) || !last.OccurredAt.Before(trusted.Manifest.Filters.OccurredBefore) || first.OccurredAt.After(last.OccurredAt) || first.OccurredAt.Equal(last.OccurredAt) && first.ID > last.ID || trusted.Manifest.RowCount == 1 && (first.ID != last.ID || !first.OccurredAt.Equal(last.OccurredAt)) {
			return errors.New("trusted audit manifest positions are invalid")
		}
	}
	return nil
}

// comparePosition orders retained audit records by timestamp and ID for
// deterministic export validation.
func comparePosition(left, right Record) int {
	if left.At.Before(right.At) {
		return -1
	}
	if left.At.After(right.At) {
		return 1
	}
	return strings.Compare(left.ID, right.ID)
}

// ManifestsEqual compares the exact JSON projection of two closed manifests.
func ManifestsEqual(left, right Manifest) bool {
	leftRaw, leftErr := json.Marshal(left)
	rightRaw, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftRaw, rightRaw)
}

// DigestBytes returns the audit package's conventional SHA-256 string.
func DigestBytes(raw []byte) string {
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// requiredObjectMember requires a nonnull named member in a JSON object.
func requiredObjectMember(raw []byte, name string) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, errors.New("audit manifest must be a JSON object")
	}
	member, ok := object[name]
	if !ok || len(member) == 0 || bytes.Equal(member, []byte("null")) {
		return nil, fmt.Errorf("audit manifest member %s is required", name)
	}
	return member, nil
}
