// Audit support maps export failures to safe domain codes and prepares bounded
// access evidence without retaining exported records in that evidence.

package catalog

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
)

// TranslateAuditExportError maps bounded encoder failures to stable API-safe
// domain codes while retaining the internal cause for diagnostics.
func TranslateAuditExportError(err error) error {
	var rangeError *audit.RangeTooLargeError
	if errors.As(err, &rangeError) {
		return Wrap(CodeAuditExportRangeTooLarge, "audit export range exceeds a safe result limit; narrow the time range", err)
	}
	return Wrap(CodeInternal, "construct bounded audit export", err)
}

// CompleteAuditExportAccess binds the access record to the completed manifest
// without retaining unrestricted filter values.
func CompleteAuditExportAccess(event AuditEvent, manifest audit.Manifest) AuditEvent {
	event.AfterDigest = manifest.EventsDigest
	if event.Details == nil {
		event.Details = make(map[string]any)
	}
	facts, _ := event.Details["facts"].(map[string]any)
	if facts == nil {
		facts = make(map[string]any)
	}
	facts["eventsDigest"] = manifest.EventsDigest
	facts["rowCount"] = auditCountNumber(manifest.RowCount)
	facts["occurredFrom"] = manifest.Filters.OccurredFrom.UTC().Format(time.RFC3339Nano)
	facts["occurredBefore"] = manifest.Filters.OccurredBefore.UTC().Format(time.RFC3339Nano)
	event.Details["facts"] = facts
	return event
}

// CompletePublisherDomainRevocationAudit adds bounded consequence facts after
// the repository has selected the affected releases under its transaction
// lock. Version-1 evidence retains only a count and digest, never an unbounded
// list of release identifiers.
func CompletePublisherDomainRevocationAudit(event AuditEvent, domain string, affectedReleaseIDs []string) (AuditEvent, error) {

	event = cloneAuditEvent(event)
	ids := append(make([]string, 0, len(affectedReleaseIDs)), affectedReleaseIDs...)
	sort.Strings(ids)
	raw, err := json.Marshal(ids)
	if err != nil {
		return AuditEvent{}, Wrap(CodeInternal, "encode affected publication releases", err)
	}
	if event.Details == nil {
		event.Details = make(map[string]any)
	}
	event.Details["facts"] = map[string]any{
		"domain":                domain,
		"affectedReleaseCount":  auditCountNumber(len(ids)),
		"affectedReleaseDigest": audit.DigestBytes(raw),
	}
	if err := audit.ValidateNewRecord(event); err != nil {
		return AuditEvent{}, Wrap(CodeInternal, "validate publisher-domain revocation audit", err)
	}
	return event, nil
}

// auditCountNumber preserves an exact integer count in JSON-shaped audit facts.
func auditCountNumber(value int) any {
	return json.Number(strconv.Itoa(value))
}
