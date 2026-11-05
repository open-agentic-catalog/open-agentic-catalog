// The audit action registry defines closed fact schemas and validates records
// before deterministic encoding or persistence.

package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

var (
	uuidPattern      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	digestPattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	actionPattern    = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)+$`)
	safeName         = regexp.MustCompile(`^[A-Za-z0-9._:@/-]+$`)
	requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

type actionRule struct {
	reasonRequired bool
	targetType     string
	targetUUID     bool
	facts          map[string]factRule
}

type factKind uint8

const (
	factText factKind = iota + 1
	factUUID
	factDigest
	factInteger
	factBoolean
	factTextArray
	factUUIDArray
	factDigestArray
)

type factRule struct {
	kind     factKind
	required bool
}

// requiredFact declares an action fact that must be present with the selected
// value kind.
func requiredFact(kind factKind) factRule { return factRule{kind: kind, required: true} }

// optionalFact permits an action fact only when its supplied value matches the
// selected kind.
func optionalFact(kind factKind) factRule { return factRule{kind: kind} }

const (
	ActionAuditEventsRead                = "audit.events_read"
	ActionAuditExported                  = "audit.exported"
	ActionSourceInspected                = "source.inspected"
	ActionSourceChangePreviewCreated     = "source.change_preview.created"
	ActionSourceArtworkRevised           = "source.artwork.revised"
	ActionSourceConnected                = "source.connected"
	ActionSourceRevisionActivated        = "source.revision.activated"
	ActionSourceTested                   = "source.tested"
	ActionSourcePaused                   = "source.paused"
	ActionSourceResumed                  = "source.resumed"
	ActionSourceRemoved                  = "source.removed"
	ActionSourceTrustRequested           = "source.trust.requested"
	ActionSourceTrustCompleted           = "source.trust.completed"
	ActionSourceTrustRead                = "source.trust.read"
	ActionSourceSyncRequested            = "source.sync.requested"
	ActionSourceSyncCompleted            = "source.sync.completed"
	ActionSourceSyncFailed               = "source.sync.failed"
	ActionPublicationCandidateCreated    = "publication.candidate.created"
	ActionPublicationActivated           = "publication.activated"
	ActionPublicationRolledBack          = "publication.rolled_back"
	ActionPublicationUnpublished         = "publication.unpublished"
	ActionJobCancellationRequested       = "job.cancellation_requested"
	ActionJobRetried                     = "job.retried"
	ActionSemanticReindexRequested       = "semantic.reindex.requested"
	ActionSearchProjectionRebuilt        = "repair.search_projection.rebuilt"
	ActionWebhookSecretsReencrypted      = "repair.webhook_secrets.reencrypted" // #nosec G101 -- public audit action name, not a credential.
	ActionWebhookCreated                 = "webhook.created"
	ActionWebhookUpdated                 = "webhook.updated"
	ActionWebhookDisabled                = "webhook.disabled"
	ActionWebhookSecretRotated           = "webhook.secret_rotated" // #nosec G101 -- public audit action name, not a credential.
	ActionWebhookDeliveryAttempted       = "webhook.delivery_attempted"
	ActionWebhookDeliveriesRead          = "webhook.deliveries_read"
	ActionVersionRevisionRead            = "evidence.version_revision.read"
	ActionCatalogDocumentRead            = "evidence.catalog_document.read"
	ActionSourceInspectionRead           = "evidence.source_inspection.read"
	ActionRawObservationRead             = "evidence.raw_observation.read"
	ActionFederationLiveSearchDiagnostic = "federation.live_search.diagnostic"
	ActionIdempotencyReservationReleased = "idempotency.reservation_released"
)

var actionRules = map[string]actionRule{
	"personal.item.created":        {targetType: "personal_item", targetUUID: true, facts: map[string]factRule{"revision": requiredFact(factInteger)}},
	"personal.item.updated":        {targetType: "personal_item", targetUUID: true, facts: map[string]factRule{"revision": requiredFact(factInteger)}},
	"personal.item.removed":        {targetType: "personal_item", targetUUID: true, facts: map[string]factRule{"revision": requiredFact(factInteger)}},
	"tenant.settings.changed":      {targetType: "tenant_settings", targetUUID: true, reasonRequired: true, facts: map[string]factRule{"beforeAttemptsSet": requiredFact(factBoolean), "afterAttemptsSet": requiredFact(factBoolean), "beforeAttempts": optionalFact(factInteger), "afterAttempts": optionalFact(factInteger), "beforeRevision": requiredFact(factInteger), "afterRevision": requiredFact(factInteger), "beforeEnabledSet": requiredFact(factBoolean), "afterEnabledSet": requiredFact(factBoolean), "beforeIntervalSet": requiredFact(factBoolean), "afterIntervalSet": requiredFact(factBoolean), "beforeEnabled": optionalFact(factBoolean), "afterEnabled": optionalFact(factBoolean), "beforeInterval": optionalFact(factText), "afterInterval": optionalFact(factText)}},
	"rating.submit":                {targetType: "rating", targetUUID: true, facts: feedbackFacts()},
	"rating.withdraw":              {targetType: "rating", targetUUID: true, facts: feedbackFacts()},
	"rating.remove":                {targetType: "rating", targetUUID: true, reasonRequired: true, facts: feedbackFacts()},
	"rating.permit":                {targetType: "rating", targetUUID: true, reasonRequired: true, facts: feedbackFacts()},
	"rating.sharing":               {targetType: "rating_sharing", targetUUID: true, reasonRequired: true, facts: map[string]factRule{"revision": requiredFact(factInteger), "enabled": requiredFact(factBoolean)}},
	"feedback.created":             {targetType: "feedback", targetUUID: true, facts: feedbackFacts()},
	"feedback.edit":                {targetType: "feedback", targetUUID: true, facts: feedbackFacts()},
	"feedback.address":             {targetType: "feedback", targetUUID: true, facts: feedbackFacts()},
	"feedback.reopen":              {targetType: "feedback", targetUUID: true, facts: feedbackFacts()},
	"feedback.withdraw":            {targetType: "feedback", targetUUID: true, facts: feedbackFacts()},
	"feedback.remove":              {targetType: "feedback", targetUUID: true, facts: feedbackFacts()},
	ActionWebhookCreated:           {targetType: "webhook_endpoint", targetUUID: true, facts: webhookEndpointFacts()},
	ActionWebhookUpdated:           {targetType: "webhook_endpoint", targetUUID: true, facts: webhookEndpointFacts()},
	ActionWebhookDisabled:          {targetType: "webhook_endpoint", targetUUID: true, facts: webhookEndpointFacts()},
	ActionWebhookSecretRotated:     {targetType: "webhook_endpoint", targetUUID: true, facts: webhookEndpointFacts()},
	ActionWebhookDeliveryAttempted: {targetType: "webhook_delivery", targetUUID: true, facts: map[string]factRule{"endpointId": requiredFact(factUUID), "eventId": requiredFact(factUUID), "attempt": requiredFact(factInteger), "state": requiredFact(factText), "httpStatus": requiredFact(factInteger)}},
	ActionWebhookDeliveriesRead:    {targetType: "webhook_endpoint", targetUUID: true, facts: map[string]factRule{"deliveryCount": requiredFact(factInteger), "deliveriesDigest": requiredFact(factDigest)}},
	ActionWebhookSecretsReencrypted: {
		reasonRequired: true, targetType: "webhook_secret_batch", targetUUID: true,
		facts: map[string]factRule{"changed": requiredFact(factInteger), "verified": requiredFact(factInteger), "firstId": requiredFact(factUUID), "lastId": requiredFact(factUUID)},
	},
	ActionAuditEventsRead: {
		targetType: "audit_event_page", targetUUID: true,
		facts: map[string]factRule{"eventCount": requiredFact(factInteger), "eventsDigest": requiredFact(factDigest)},
	},
	ActionAuditExported: {
		reasonRequired: true, targetType: "audit_range", targetUUID: true,
		facts: map[string]factRule{"rowCount": requiredFact(factInteger), "eventsDigest": requiredFact(factDigest), "occurredFrom": requiredFact(factText), "occurredBefore": requiredFact(factText)},
	},
	"catalog_listing.created": {targetType: "catalog_listing", targetUUID: true},
	"catalog_listing.revised": {targetType: "catalog_listing", targetUUID: true, facts: map[string]factRule{"revisionId": optionalFact(factUUID)}},
	ActionIdempotencyReservationReleased: {
		reasonRequired: true, targetType: "idempotency_reservation", targetUUID: true,
		facts: map[string]factRule{"reservationId": requiredFact(factUUID), "protocolVersion": requiredFact(factInteger)},
	},
	"identity.membership.offboarded":        {reasonRequired: true, targetType: "tenant_membership", targetUUID: true},
	"identity.membership_provisioned":       {reasonRequired: true, targetType: "tenant_membership", targetUUID: true},
	"identity.membership.reactivated":       {reasonRequired: true, targetType: "tenant_membership", targetUUID: true},
	"identity.membership.suspended":         {reasonRequired: true, targetType: "tenant_membership", targetUUID: true},
	"identity.realm.reactivated":            {reasonRequired: true, targetType: "tenant_identity_realm", targetUUID: true},
	"identity.realm.suspended":              {reasonRequired: true, targetType: "tenant_identity_realm", targetUUID: true},
	"identity.role_assignment.granted":      {reasonRequired: true, targetType: "role_assignment", targetUUID: true},
	"identity.role_assignment.revoked":      {reasonRequired: true, targetType: "role_assignment", targetUUID: true},
	"authorization.role.created":            {reasonRequired: true, targetType: "role_definition", targetUUID: true},
	"authorization.role.updated":            {reasonRequired: true, targetType: "role_definition", targetUUID: true},
	"authorization.role.archived":           {reasonRequired: true, targetType: "role_definition", targetUUID: true},
	"authorization.group.created":           {reasonRequired: true, targetType: "access_group", targetUUID: true},
	"authorization.group.updated":           {reasonRequired: true, targetType: "access_group", targetUUID: true},
	"authorization.group.suspended":         {reasonRequired: true, targetType: "access_group", targetUUID: true},
	"authorization.group.reactivated":       {reasonRequired: true, targetType: "access_group", targetUUID: true},
	"authorization.group.archived":          {reasonRequired: true, targetType: "access_group", targetUUID: true},
	"authorization.group_member.added":      {reasonRequired: true, targetType: "group_member", targetUUID: true},
	"authorization.group_member.revoked":    {reasonRequired: true, targetType: "group_member", targetUUID: true},
	"authorization.assignment.granted":      {reasonRequired: true, targetType: "role_assignment", targetUUID: true},
	"authorization.assignment.revoked":      {reasonRequired: true, targetType: "role_assignment", targetUUID: true},
	"listing_release.deprecated":            {reasonRequired: true, targetType: "listing_release", targetUUID: true},
	"listing_release.published":             {targetType: "listing_release", targetUUID: true},
	"listing_release.retired":               {reasonRequired: true, targetType: "listing_release", targetUUID: true},
	"listing_release.revoked":               {reasonRequired: true, targetType: "listing_release", targetUUID: true},
	"listing_release.superseded":            {reasonRequired: true, targetType: "listing_release", targetUUID: true},
	"namespace.created":                     {targetType: "namespace", targetUUID: true},
	"namespace.updated":                     {reasonRequired: true, targetType: "namespace", targetUUID: true},
	"observation.adopted":                   {targetType: "resource", targetUUID: true},
	"publisher.domain_challenge_issued":     {targetType: "publisher", targetUUID: true, reasonRequired: true, facts: map[string]factRule{"domain": requiredFact(factText), "verificationId": requiredFact(factUUID), "revision": requiredFact(factInteger), "method": requiredFact(factText), "challengeId": optionalFact(factUUID), "outcome": optionalFact(factText)}},
	"publisher.domain_verification_checked": {targetType: "publisher", targetUUID: true, reasonRequired: true, facts: map[string]factRule{"domain": requiredFact(factText), "verificationId": requiredFact(factUUID), "revision": requiredFact(factInteger), "method": requiredFact(factText), "challengeId": optionalFact(factUUID), "outcome": optionalFact(factText)}},
	"publisher.domain_verification_cleared": {targetType: "publisher", targetUUID: true, reasonRequired: true, facts: map[string]factRule{"domain": requiredFact(factText), "verificationId": requiredFact(factUUID), "revision": requiredFact(factInteger), "method": requiredFact(factText), "challengeId": optionalFact(factUUID), "outcome": optionalFact(factText)}},
	"publisher.created":                     {targetType: "publisher", targetUUID: true},
	"publisher.publication_domain_approved": {reasonRequired: true, targetType: "publisher", targetUUID: true},
	"publisher.publication_domain_revoked":  {reasonRequired: true, targetType: "publisher", targetUUID: true},
	"publisher.updated":                     {reasonRequired: true, targetType: "publisher", targetUUID: true},
	"resource.created":                      {targetType: "resource", targetUUID: true},
	"resource.deleted":                      {reasonRequired: true, targetType: "resource", targetUUID: true},
	"resource_version.created":              {targetType: "resource_version", targetUUID: true, facts: map[string]factRule{"revisionId": optionalFact(factUUID)}},
	"resource_version.revised":              {targetType: "resource_version", targetUUID: true, facts: map[string]factRule{"revisionId": optionalFact(factUUID)}},
	"review_submission.approve":             {reasonRequired: true, targetType: "review_submission", targetUUID: true},
	"review_submission.reject":              {reasonRequired: true, targetType: "review_submission", targetUUID: true},
	"review_submission.request_changes":     {reasonRequired: true, targetType: "review_submission", targetUUID: true},
	"review_submission.submitted":           {targetType: "review_submission", targetUUID: true},
	"version_revision.validated":            {targetType: "version_revision", targetUUID: true},
	ActionSourceInspected: {
		targetType: "source_inspection", targetUUID: true,
		facts: map[string]factRule{"outcome": requiredFact(factText), "originClass": requiredFact(factText), "policyDigest": requiredFact(factDigest), "configurationDigest": requiredFact(factDigest), "inspectionDigest": optionalFact(factDigest), "expiresAt": optionalFact(factText)},
	},
	ActionSourceChangePreviewCreated: {
		targetType: "source_change_preview", targetUUID: true,
		facts: map[string]factRule{"sourceId": requiredFact(factUUID), "sourceRevision": requiredFact(factInteger), "intent": requiredFact(factText), "configurationDigest": requiredFact(factDigest), "observationSetDigest": requiredFact(factDigest), "previewDigest": requiredFact(factDigest), "currentMatched": requiredFact(factInteger), "proposedMatched": requiredFact(factInteger), "excludedObservations": requiredFact(factInteger), "sendsQueriesUpstream": requiredFact(factBoolean), "expiresAt": requiredFact(factText)},
	},
	ActionSourceArtworkRevised: {reasonRequired: true, targetType: "source_artwork", targetUUID: true, facts: map[string]factRule{"sourceId": requiredFact(factUUID), "generation": requiredFact(factInteger), "revision": requiredFact(factInteger), "iconMode": requiredFact(factText), "screenshotsMode": requiredFact(factText)}},
	ActionSourceConnected: {
		reasonRequired: true, targetType: "federation_source", targetUUID: true,
		facts: map[string]factRule{"inspectionId": requiredFact(factUUID), "sourceRevision": requiredFact(factInteger), "configurationDigest": requiredFact(factDigest), "retrievalMode": requiredFact(factText), "updateMode": requiredFact(factText), "policyDigest": requiredFact(factDigest)},
	},
	ActionSourceRevisionActivated: {
		reasonRequired: true, targetType: "federation_source", targetUUID: true,
		facts: map[string]factRule{"previewId": requiredFact(factUUID), "sourceRevision": requiredFact(factInteger), "beforeConfigurationDigest": requiredFact(factDigest), "afterConfigurationDigest": requiredFact(factDigest), "retrievalMode": requiredFact(factText), "updateMode": requiredFact(factText), "policyDigest": requiredFact(factDigest), "trigger": requiredFact(factText)},
	},
	ActionSourceTested: {
		targetType: "federation_source", targetUUID: true,
		facts: map[string]factRule{"beforeRevision": requiredFact(factInteger), "afterRevision": requiredFact(factInteger), "outcome": requiredFact(factText), "stateChanged": requiredFact(factBoolean), "httpClass": optionalFact(factText), "policyDigest": requiredFact(factDigest), "bodyDigest": optionalFact(factDigest), "capabilities": optionalFact(factTextArray), "credentialRevision": optionalFact(factText)},
	},
	ActionSourcePaused:         {reasonRequired: true, targetType: "federation_source", targetUUID: true, facts: lifecycleFacts()},
	ActionSourceResumed:        {reasonRequired: true, targetType: "federation_source", targetUUID: true, facts: lifecycleFacts()},
	ActionSourceRemoved:        {reasonRequired: true, targetType: "federation_source", targetUUID: true, facts: removalFacts()},
	ActionSourceTrustRequested: {reasonRequired: true, targetType: "job", targetUUID: true, facts: map[string]factRule{"sourceId": requiredFact(factUUID), "sourceRevision": requiredFact(factInteger), "entryDigest": requiredFact(factDigest), "policyDigest": requiredFact(factDigest), "jobId": requiredFact(factUUID), "deduplicated": requiredFact(factBoolean)}},
	ActionSourceTrustCompleted: {targetType: "job", targetUUID: true, facts: map[string]factRule{"sourceId": requiredFact(factUUID), "sourceRevision": requiredFact(factInteger), "entryDigest": requiredFact(factDigest), "policyDigest": requiredFact(factDigest), "resultDigest": requiredFact(factDigest), "currentRegistration": requiredFact(factBoolean), "jobId": requiredFact(factUUID), "jobKind": requiredFact(factText), "jobAttempt": requiredFact(factInteger), "initiator": requiredFact(factUUID)}},
	ActionSourceTrustRead:      {targetType: "job", targetUUID: true, facts: map[string]factRule{"contentDigest": requiredFact(factDigest), "sizeBytes": requiredFact(factInteger)}},
	ActionSourceSyncRequested: {
		targetType: "federation_source", targetUUID: true,
		facts: map[string]factRule{"sourceRevision": requiredFact(factInteger), "jobId": requiredFact(factUUID), "trigger": requiredFact(factText), "deduplicated": requiredFact(factBoolean)},
	},
	ActionSemanticReindexRequested: {reasonRequired: true, targetType: "job", targetUUID: true, facts: map[string]factRule{"profile": requiredFact(factDigest), "scope": requiredFact(factText), "jobId": requiredFact(factUUID), "deduplicated": requiredFact(factBoolean)}},
	ActionSourceSyncCompleted:      {targetType: "source_snapshot", targetUUID: true, facts: syncFacts()},
	ActionSourceSyncFailed:         {targetType: "source_snapshot", targetUUID: true, facts: syncFacts()},
	ActionPublicationCandidateCreated: {
		targetType: "publication_snapshot", targetUUID: true,
		facts: map[string]factRule{"host": requiredFact(factText), "audience": requiredFact(factText), "generation": requiredFact(factInteger), "ardProfile": requiredFact(factText), "aiCatalogProfile": requiredFact(factText), "ardDigest": requiredFact(factDigest), "aiCatalogDigest": requiredFact(factDigest), "entryCount": requiredFact(factInteger), "exclusionCount": requiredFact(factInteger), "trigger": requiredFact(factText), "jobId": optionalFact(factUUID), "jobKind": optionalFact(factText), "jobAttempt": optionalFact(factInteger), "initiator": optionalFact(factUUID)},
	},
	ActionPublicationActivated:  {reasonRequired: true, targetType: "publication_snapshot", targetUUID: true, facts: publicationTransitionFacts()},
	ActionPublicationRolledBack: {reasonRequired: true, targetType: "publication_snapshot", targetUUID: true, facts: publicationTransitionFacts()},
	ActionPublicationUnpublished: {
		targetType: "publication_snapshot", targetUUID: true,
		facts: map[string]factRule{"host": requiredFact(factText), "generation": requiredFact(factInteger), "cause": requiredFact(factText), "originatingAction": requiredFact(factText), "originatingTargetId": requiredFact(factUUID), "ardDigest": optionalFact(factDigest), "aiCatalogDigest": optionalFact(factDigest)},
	},
	ActionJobCancellationRequested:       {reasonRequired: true, targetType: "job", targetUUID: true, facts: map[string]factRule{"kind": requiredFact(factText), "beforeState": requiredFact(factText), "afterState": requiredFact(factText), "cancellationRequested": requiredFact(factBoolean), "failureClass": optionalFact(factText)}},
	ActionJobRetried:                     {reasonRequired: true, targetType: "job", targetUUID: true, facts: map[string]factRule{"kind": requiredFact(factText), "beforeState": requiredFact(factText), "afterState": requiredFact(factText), "parentJobId": requiredFact(factUUID), "replacementJobId": requiredFact(factUUID), "failureClass": optionalFact(factText)}},
	ActionSearchProjectionRebuilt:        {reasonRequired: true, targetType: "catalog_search_projection", targetUUID: true, facts: map[string]factRule{"selectedFirstId": requiredFact(factUUID), "selectedLastId": requiredFact(factUUID), "processed": requiredFact(factInteger), "hasMore": requiredFact(factBoolean), "beforeGeneration": requiredFact(factInteger), "afterGeneration": requiredFact(factInteger)}},
	ActionVersionRevisionRead:            {targetType: "version_revision", targetUUID: true, facts: map[string]factRule{"contentDigest": requiredFact(factDigest)}},
	ActionCatalogDocumentRead:            {targetType: "catalog_document", facts: map[string]factRule{"contentDigest": requiredFact(factDigest), "sizeBytes": requiredFact(factInteger)}},
	ActionSourceInspectionRead:           {targetType: "source_inspection", targetUUID: true, facts: map[string]factRule{"contentDigest": requiredFact(factDigest)}},
	ActionRawObservationRead:             {targetType: "observation", targetUUID: true, facts: map[string]factRule{"contentDigest": requiredFact(factDigest), "sizeBytes": requiredFact(factInteger)}},
	ActionFederationLiveSearchDiagnostic: {reasonRequired: true, targetType: "federation_live_search", targetUUID: true, facts: map[string]factRule{"contactedSourceIds": requiredFact(factUUIDArray), "outcome": requiredFact(factText), "resultCount": requiredFact(factInteger), "diagnosticCount": requiredFact(factInteger), "partial": requiredFact(factBoolean), "policyDigests": requiredFact(factDigestArray)}},
}

// lifecycleFacts defines the closed source-lifecycle fact vocabulary shared by
// status transitions.
func lifecycleFacts() map[string]factRule {
	return map[string]factRule{"beforeState": requiredFact(factText), "afterState": requiredFact(factText), "beforeRevision": requiredFact(factInteger), "afterRevision": requiredFact(factInteger)}
}

// removalFacts adds removal-specific observation consequences to the
// source-lifecycle facts.
func removalFacts() map[string]factRule {
	facts := lifecycleFacts()
	facts["previewId"] = requiredFact(factUUID)
	facts["previewDigest"] = requiredFact(factDigest)
	facts["observationSetDigest"] = requiredFact(factDigest)
	return facts
}

// syncFacts defines the bounded source-sync counts, provenance and execution
// facts used by outcome records.
func syncFacts() map[string]factRule {
	return map[string]factRule{"sourceId": requiredFact(factUUID), "sourceRevision": requiredFact(factInteger), "state": requiredFact(factText), "policyDigest": requiredFact(factDigest), "memberCount": requiredFact(factInteger), "added": requiredFact(factInteger), "updated": requiredFact(factInteger), "withdrawn": requiredFact(factInteger), "failureClass": optionalFact(factText), "jobId": requiredFact(factUUID), "jobKind": requiredFact(factText), "jobAttempt": requiredFact(factInteger), "initiator": optionalFact(factText), "trigger": requiredFact(factText)}
}

// publicationTransitionFacts defines the immutable snapshot bindings required
// for active-publication changes.
func publicationTransitionFacts() map[string]factRule {
	return map[string]factRule{"host": requiredFact(factText), "generation": requiredFact(factInteger), "replacedSnapshotId": optionalFact(factUUID), "ardProfile": requiredFact(factText), "aiCatalogProfile": requiredFact(factText), "ardDigest": requiredFact(factDigest), "aiCatalogDigest": requiredFact(factDigest)}
}

// RegisteredAction reports whether action is valid for a newly written v1 row.
func RegisteredAction(action string) bool {
	_, ok := actionRules[action]
	return ok
}

// Actions returns the sorted version-1 action vocabulary for contract tests.
func Actions() []string {
	result := make([]string, 0, len(actionRules))
	for action := range actionRules {
		result = append(result, action)
	}
	sort.Strings(result)
	return result
}

// NormalizeReason validates and canonicalizes durable human-authored reason
// text. Empty is accepted only for actions that do not require a reason.
func NormalizeReason(action, reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	rule, registered := actionRules[action]
	if !registered {
		return "", fmt.Errorf("audit action %q is not registered", action)
	}
	if reason == "" {
		if rule.reasonRequired {
			return "", errors.New("audit action requires a reason")
		}
		return "", nil
	}
	if err := validateReasonText(reason); err != nil {
		return "", err
	}
	return reason, nil
}

// NormalizeRequiredReason applies the common retained-reason contract to an
// operation whose action can also be emitted automatically without a human
// reason, such as a safety withdrawal.
func NormalizeRequiredReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", errors.New("audit action requires a reason")
	}
	if err := validateReasonText(reason); err != nil {
		return "", err
	}
	return reason, nil
}

// validateReasonText enforces bounded UTF-8 audit reasons and rejects
// disallowed control characters.
func validateReasonText(reason string) error {
	if !utf8.ValidString(reason) || utf8.RuneCountInString(reason) > MaxReasonRunes {
		return errors.New("audit reason is not valid bounded UTF-8")
	}
	for _, character := range reason {
		if unicode.IsControl(character) {
			return errors.New("audit reason contains a control character")
		}
	}
	return nil
}

// NewRecord constructs and validates one version-1 record.
func NewRecord(id, tenantID string, principal Principal, action, targetType, targetID, reason string, execution Execution, facts map[string]any, at time.Time) (Record, error) {
	reason, err := NormalizeReason(action, reason)
	if err != nil {
		return Record{}, err
	}
	detailsValue := Details{
		PrincipalType:       principal.Type,
		ActorType:           principal.ActorType,
		RealmID:             principal.RealmID,
		AuthorizationDigest: execution.AuthorizationDigest,
		CommandID:           execution.CommandID,
		CurrentUser:         execution.CurrentUser,
		OperatorLabel:       execution.OperatorLabel,
		Facts:               cloneFacts(facts),
	}
	detailsRaw, err := json.Marshal(detailsValue)
	if err != nil {
		return Record{}, fmt.Errorf("encode audit details: %w", err)
	}
	var details map[string]any
	decoder := json.NewDecoder(bytes.NewReader(detailsRaw))
	decoder.UseNumber()
	if err := decoder.Decode(&details); err != nil {
		return Record{}, fmt.Errorf("decode audit details: %w", err)
	}
	version := RecordFormatV1
	record := Record{FormatVersion: &version, ID: strings.ToLower(id), TenantID: strings.ToLower(tenantID), Actor: principal.ID, Action: action, TargetType: targetType, TargetID: targetID, Reason: reason, RequestID: execution.RequestID, Details: details, At: at.UTC().Truncate(time.Microsecond)}
	if err := ValidateNewRecord(record); err != nil {
		return Record{}, err
	}
	return record, nil
}

// Facts converts one typed action-fact struct into the normalized object used
// by a record. Domain packages use this rather than constructing open-ended
// maps for newly covered operations.
func Facts(value any) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode typed audit facts: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var facts map[string]any
	if err := decoder.Decode(&facts); err != nil {
		return nil, fmt.Errorf("decode typed audit facts: %w", err)
	}
	return facts, nil
}

// ValidateNewRecord enforces the complete version-1 in-process record contract.
func ValidateNewRecord(record Record) error {
	if record.FormatVersion == nil || *record.FormatVersion != RecordFormatV1 {
		return errors.New("new audit record must use format version 1")
	}
	if !uuidPattern.MatchString(record.ID) || !uuidPattern.MatchString(record.TenantID) {
		return errors.New("audit record identifiers are invalid")
	}
	if err := boundedText("actor", record.Actor, MaxActorBytes); err != nil {
		return err
	}
	if !actionPattern.MatchString(record.Action) || !RegisteredAction(record.Action) || len(record.Action) > MaxActionBytes {
		return errors.New("audit action is invalid or unregistered")
	}
	if err := boundedText("target type", record.TargetType, MaxTargetTypeBytes); err != nil {
		return err
	}
	if err := boundedText("target ID", record.TargetID, MaxTargetIDBytes); err != nil {
		return err
	}
	if rule := actionRules[record.Action]; rule.targetType != "" && record.TargetType != rule.targetType {
		return errors.New("audit target type does not match its action")
	}
	if actionRules[record.Action].targetUUID && !uuidPattern.MatchString(record.TargetID) {
		return errors.New("audit target ID does not match its action")
	}
	reason, err := NormalizeReason(record.Action, record.Reason)
	if err != nil || reason != record.Reason {
		return errors.New("audit reason is invalid or not normalized")
	}
	if record.RequestID != "" && (!requestIDPattern.MatchString(record.RequestID) || len(record.RequestID) > MaxRequestIDBytes) {
		return errors.New("audit request ID is invalid")
	}
	if !validOptionalDigest(record.BeforeDigest) || !validOptionalDigest(record.AfterDigest) {
		return errors.New("audit state digest is invalid")
	}
	if record.At.IsZero() || record.At.Location() != time.UTC || !record.At.Equal(record.At.Truncate(time.Microsecond)) {
		return errors.New("audit occurrence time must use UTC microsecond precision")
	}
	if err := validateDetails(record.Action, record.Actor, record.Details); err != nil {
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode audit record: %w", err)
	}
	if len(encoded) > MaxEncodedRecordBytes {
		return errors.New("audit record exceeds its encoded byte limit")
	}
	return nil
}

// EncodeRecords deterministically encodes an ordered slice while retaining
// json.Number lexical values rather than routing them through float64.
func EncodeRecords(records []Record) ([]byte, error) {
	if records == nil {
		records = []Record{}
	}
	for _, record := range records {
		if err := ValidateNewRecord(record); err != nil {
			return nil, err
		}
		encodedRecord, err := json.Marshal(record)
		if err != nil {
			return nil, fmt.Errorf("encode audit record %s: %w", record.ID, err)
		}
		if len(encodedRecord) > MaxEncodedRecordBytes {

			return nil, errors.New("version-1 audit event exceeds its invariant record limit")
		}
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		return nil, fmt.Errorf("encode audit records: %w", err)
	}
	if err := jsondoc.Validate(encoded); err != nil {
		return nil, fmt.Errorf("validate encoded audit records: %w", err)
	}
	return encoded, nil
}

// DigestRecords returns the digest of the exact deterministic array encoding.
func DigestRecords(records []Record) (string, []byte, error) {
	encoded, err := EncodeRecords(records)
	if err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), encoded, nil
}

// validateDetails keeps the versioned attribution envelope closed and ensures
// fields from Catalog, service, and database principals cannot be mixed.
func validateDetails(action, actor string, details map[string]any) error {
	if details == nil {
		return errors.New("version-1 audit details are required")
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode audit details: %w", err)
	}
	if len(raw) > MaxDetailsBytes {
		return errors.New("audit details exceed their encoded byte limit")
	}
	if err := jsondoc.Validate(raw); err != nil {
		return fmt.Errorf("audit details are invalid: %w", err)
	}
	allowed := map[string]bool{"principalType": true, "actorType": true, "realmId": true, "authorizationDigest": true, "commandId": true, "currentUser": true, "operatorLabel": true, "facts": true}
	for name := range details {
		if !allowed[name] {
			return fmt.Errorf("audit details member %q is not allowed", name)
		}
	}
	principalType, _ := details["principalType"].(string)
	switch principalType {
	case PrincipalCatalogMembership:
		if hasAnyDetail(details, "currentUser", "operatorLabel") {
			return errors.New("catalog membership audit attribution contains database-only fields")
		}
		if !uuidPattern.MatchString(actor) {
			return errors.New("catalog membership audit principal must be a UUID")
		}
		realmID, _ := details["realmId"].(string)
		if !uuidPattern.MatchString(realmID) {
			return errors.New("catalog membership audit realm must be a UUID")
		}
		actorType, _ := details["actorType"].(string)
		if actorType != "human" && actorType != "service" {
			return errors.New("catalog membership audit actor type is invalid")
		}
	case PrincipalServiceExecutor:
		if hasAnyDetail(details, "actorType", "realmId", "currentUser", "operatorLabel") {
			return errors.New("service-executor audit attribution contains fields for another principal type")
		}
		if !safeName.MatchString(actor) {
			return errors.New("service-executor audit principal is invalid")
		}
	case PrincipalDatabaseLogin:
		if hasAnyDetail(details, "actorType", "realmId", "authorizationDigest", "commandId") {
			return errors.New("database-login audit attribution contains fields for another principal type")
		}
		if err := boundedText("database-login principal", actor, MaxActorBytes); err != nil {
			return errors.New("database-login audit principal is invalid")
		}
		currentUser, _ := details["currentUser"].(string)
		if err := boundedText("database-login effective authority", currentUser, MaxActorBytes); err != nil {
			return errors.New("database-login effective authority is invalid")
		}
	default:
		return errors.New("audit principal type is invalid")
	}
	for _, name := range []string{"authorizationDigest"} {
		if value, present := details[name]; present {
			text, ok := value.(string)
			if !ok || !digestPattern.MatchString(text) {
				return fmt.Errorf("audit %s is invalid", name)
			}
		}
	}
	if value, present := details["commandId"]; present {
		text, ok := value.(string)
		if !ok || !uuidPattern.MatchString(text) {
			return errors.New("audit command ID is invalid")
		}
	}
	if value, present := details["operatorLabel"]; present {
		text, ok := value.(string)
		if !ok || boundedText("operator label", text, MaxActorBytes) != nil {
			return errors.New("audit operator label is invalid")
		}
	}
	if facts, present := details["facts"]; present {
		factObject, ok := facts.(map[string]any)
		if !ok {
			return errors.New("audit facts must be an object")
		}
		members := 0
		if err := validateValue(facts, 1, &members); err != nil {
			return fmt.Errorf("audit facts are invalid: %w", err)
		}
		if err := validateActionFacts(action, factObject); err != nil {
			return err
		}
	} else {
		for _, rule := range actionRules[action].facts {
			if rule.required {
				return errors.New("audit action facts are required")
			}
		}
	}
	return nil
}

// validateActionFacts rejects unknown or missing action facts and verifies
// every declared value kind.
func validateActionFacts(action string, facts map[string]any) error {
	rules := actionRules[action].facts
	if len(rules) == 0 {
		return nil
	}
	for name := range facts {
		if _, ok := rules[name]; !ok {
			return fmt.Errorf("audit fact %q is not allowed for action %q", name, action)
		}
	}
	for name, rule := range rules {
		value, present := facts[name]
		if !present {
			if rule.required {
				return fmt.Errorf("audit fact %q is required for action %q", name, action)
			}
			continue
		}
		if err := validateFactKind(name, value, rule.kind); err != nil {
			return err
		}
	}
	return validateActionFactSemantics(action, facts)
}

// validateFactKind checks scalar and array facts against their closed text,
// UUID, digest, integer or boolean contract.
func validateFactKind(name string, value any, kind factKind) error {
	invalid := func() error { return fmt.Errorf("audit fact %q has an invalid type or value", name) }
	switch kind {
	case factText:
		text, ok := value.(string)
		if !ok || boundedText("fact", text, 2048) != nil {
			return invalid()
		}
	case factUUID:
		text, ok := value.(string)
		if !ok || !uuidPattern.MatchString(text) {
			return invalid()
		}
	case factDigest:
		text, ok := value.(string)
		if !ok || !digestPattern.MatchString(text) {
			return invalid()
		}
	case factInteger:
		if _, ok := integerFact(value); !ok {
			return invalid()
		}
	case factBoolean:
		if _, ok := value.(bool); !ok {
			return invalid()
		}
	case factTextArray, factUUIDArray, factDigestArray:
		values, ok := value.([]any)
		if !ok || len(values) > 32 {
			return invalid()
		}
		for _, item := range values {
			text, ok := item.(string)
			if !ok || boundedText("fact array item", text, 512) != nil {
				return invalid()
			}
			if kind == factUUIDArray && !uuidPattern.MatchString(text) || kind == factDigestArray && !digestPattern.MatchString(text) {
				return invalid()
			}
		}
	default:
		return invalid()
	}
	return nil
}

// validateActionFactSemantics is the second validation layer after field shape:
// it rejects action facts that are individually valid but mutually impossible.
func validateActionFactSemantics(action string, facts map[string]any) error {
	text := func(name string) string {
		value, _ := facts[name].(string)
		return value
	}
	number := func(name string) int64 {
		value, _ := integerFact(facts[name])
		return value
	}
	nonNegative := func(names ...string) error {
		for _, name := range names {
			value, ok := integerFact(facts[name])
			if !ok || value < 0 {
				return fmt.Errorf("audit fact %q must be a non-negative integer", name)
			}
		}
		return nil
	}
	positive := func(names ...string) error {
		for _, name := range names {
			value, ok := integerFact(facts[name])
			if !ok || value < 1 {
				return fmt.Errorf("audit fact %q must be a positive integer", name)
			}
		}
		return nil
	}
	oneOf := func(name string, values ...string) error {
		if slices.Contains(values, text(name)) {
			return nil
		}
		return fmt.Errorf("audit fact %q is outside its action vocabulary", name)
	}
	timestamp := func(name string) error {
		value, err := time.Parse(time.RFC3339Nano, text(name))
		if err != nil || value.Location() != time.UTC {
			return fmt.Errorf("audit fact %q must be an RFC 3339 UTC timestamp", name)
		}
		return nil
	}
	switch action {
	case ActionAuditEventsRead:
		return nonNegative("eventCount")
	case ActionAuditExported:
		if err := nonNegative("rowCount"); err != nil {
			return err
		}
		if number("rowCount") > MaxExportRows {
			return errors.New("audit export row count exceeds its bounded result limit")
		}
		if err := timestamp("occurredFrom"); err != nil {
			return err
		}
		if err := timestamp("occurredBefore"); err != nil {
			return err
		}
		from, _ := time.Parse(time.RFC3339Nano, text("occurredFrom"))
		before, _ := time.Parse(time.RFC3339Nano, text("occurredBefore"))
		if !from.Before(before) {
			return errors.New("audit export range is empty or reversed")
		}
	case ActionIdempotencyReservationReleased:
		if text("reservationId") == "" || number("protocolVersion") != 2 {
			return errors.New("idempotency repair audit facts are inconsistent")
		}
	case ActionSourceInspected:
		if err := oneOf("outcome", outboundOutcomes...); err != nil {
			return err
		}
		origin := text("originClass")
		if origin != "public" {
			profile := strings.TrimPrefix(origin, "named_private:")
			if profile == origin || !safeProfileName(profile) {
				return errors.New("source inspection origin class is invalid")
			}
		}
		_, hasDigest := facts["inspectionDigest"]
		_, hasExpiry := facts["expiresAt"]
		if hasDigest != hasExpiry || text("outcome") == "succeeded" != hasDigest {
			return errors.New("source inspection outcome evidence is inconsistent")
		}
		if hasExpiry {
			return timestamp("expiresAt")
		}
	case ActionSourceChangePreviewCreated:
		if err := oneOf("intent", "update", "remove"); err != nil {
			return err
		}
		if err := positive("sourceRevision"); err != nil {
			return err
		}
		if err := nonNegative("currentMatched", "proposedMatched", "excludedObservations"); err != nil {
			return err
		}
		if number("proposedMatched") > number("currentMatched") || number("excludedObservations") != number("currentMatched")-number("proposedMatched") {
			return errors.New("source change preview counts are inconsistent")
		}
		if text("intent") == "remove" && number("proposedMatched") != 0 {
			return errors.New("source removal preview counts are inconsistent")
		}
		return timestamp("expiresAt")
	case ActionSourceArtworkRevised:
		if err := positive("generation", "revision"); err != nil {
			return err
		}
		if err := oneOf("iconMode", "inherit", "replace", "suppress"); err != nil {
			return err
		}
		return oneOf("screenshotsMode", "inherit", "replace", "suppress")
	case ActionSourceConnected:
		if number("sourceRevision") != 1 {
			return errors.New("newly connected source revision must be one")
		}
		if err := oneOf("retrievalMode", "indexed", "live", "hybrid"); err != nil {
			return err
		}
		return oneOf("updateMode", "manual_review", "automatic")
	case ActionSourceRevisionActivated:
		if err := positive("sourceRevision"); err != nil {
			return err
		}
		if err := oneOf("retrievalMode", "indexed", "live", "hybrid"); err != nil {
			return err
		}
		if err := oneOf("updateMode", "manual_review", "automatic"); err != nil {
			return err
		}
		if text("beforeConfigurationDigest") == text("afterConfigurationDigest") {
			return errors.New("source revision must change its reviewed configuration digest")
		}
		return oneOf("trigger", "reviewed_update")
	case ActionSourceTested:
		if err := oneOf("outcome", outboundOutcomes...); err != nil {
			return err
		}
		if err := positive("beforeRevision", "afterRevision"); err != nil {
			return err
		}
		stateChanged, _ := facts["stateChanged"].(bool)
		if stateChanged && number("afterRevision") != number("beforeRevision")+1 || !stateChanged && number("afterRevision") != number("beforeRevision") {
			return errors.New("source test revision facts are inconsistent")
		}
		if value := text("httpClass"); value != "" && !slices.Contains([]string{"1xx", "2xx", "3xx", "4xx", "5xx"}, value) {
			return errors.New("source test HTTP class is invalid")
		}
		outcome := text("outcome")
		_, hasBodyDigest := facts["bodyDigest"]
		if outcome == "succeeded" && (!stateChanged || text("httpClass") != "2xx" || !hasBodyDigest) {
			return errors.New("successful source test evidence is incomplete")
		}
		if stateChanged && outcome != "succeeded" {
			return errors.New("only a successful source test can advance source state")
		}
	case ActionSourcePaused, ActionSourceResumed, ActionSourceRemoved:
		if err := positive("beforeRevision", "afterRevision"); err != nil {
			return err
		}
		if number("afterRevision") != number("beforeRevision")+1 {
			return errors.New("source lifecycle revision facts are inconsistent")
		}
		if err := oneOf("beforeState", "active", "paused", "error"); err != nil {
			return err
		}
		wanted := map[string]string{ActionSourcePaused: "paused", ActionSourceResumed: "active", ActionSourceRemoved: "removed"}[action]
		if text("afterState") != wanted || text("beforeState") == wanted {
			return errors.New("source lifecycle state facts are inconsistent")
		}
	case ActionSourceSyncRequested:
		if err := positive("sourceRevision"); err != nil {
			return err
		}
		return oneOf("trigger", "manual")
	case ActionSourceSyncCompleted, ActionSourceSyncFailed:
		if err := positive("sourceRevision", "jobAttempt"); err != nil {
			return err
		}
		if err := nonNegative("memberCount", "added", "updated", "withdrawn"); err != nil {
			return err
		}
		if number("added")+number("updated") > number("memberCount") {
			return errors.New("source synchronization change counts exceed snapshot membership")
		}
		if text("jobKind") != "source.sync" {
			return errors.New("source synchronization Job kind is invalid")
		}
		if err := oneOf("trigger", "manual", "automatic"); err != nil {
			return err
		}
		initiator := text("initiator")
		if initiator != "" && !uuidPattern.MatchString(initiator) && initiator != InitiatorSourceMaintainer {
			return errors.New("source synchronization initiator is invalid")
		}
		if text("trigger") == "automatic" && initiator != InitiatorSourceMaintainer {
			return errors.New("automatic source synchronization requires its code-owned initiator")
		}
		if text("trigger") == "manual" && initiator == InitiatorSourceMaintainer {
			return errors.New("manual source synchronization cannot use the automatic initiator")
		}
		if action == ActionSourceSyncCompleted && text("state") != "complete" || action == ActionSourceSyncFailed && !slices.Contains([]string{"incomplete", "failed"}, text("state")) {
			return errors.New("source synchronization state does not match its action")
		}
		failureClass := text("failureClass")
		if action == ActionSourceSyncCompleted && failureClass != "" {
			return errors.New("completed source synchronization cannot have a failure class")
		}
		if action == ActionSourceSyncFailed && !slices.Contains(sourceSyncFailureClasses, failureClass) {
			return errors.New("failed source synchronization requires a valid failure class")
		}
	case ActionPublicationCandidateCreated:
		if err := positive("generation"); err != nil {
			return err
		}
		if err := nonNegative("entryCount", "exclusionCount"); err != nil {
			return err
		}
		if err := oneOf("audience", "anonymous", "authenticated"); err != nil {
			return err
		}
		if err := oneOf("trigger", "direct", "manual"); err != nil {
			return err
		}
		_, hasJobID := facts["jobId"]
		_, hasJobKind := facts["jobKind"]
		_, hasAttempt := facts["jobAttempt"]
		_, hasInitiator := facts["initiator"]
		if text("trigger") == "direct" && (hasJobID || hasJobKind || hasAttempt || hasInitiator) {
			return errors.New("direct publication candidate contains worker attribution")
		}
		if text("trigger") == "manual" && (!hasJobID || !hasJobKind || !hasAttempt || !hasInitiator || text("jobKind") != "publication.generate") {
			return errors.New("worker publication candidate attribution is incomplete")
		}
		if hasAttempt {
			return positive("jobAttempt")
		}
	case ActionPublicationActivated, ActionPublicationRolledBack:
		return positive("generation")
	case ActionPublicationUnpublished:
		if err := positive("generation"); err != nil {
			return err
		}
		if err := oneOf("cause", "explicit_unpublish", "publisher_domain_revoked", "release_retired", "release_superseded", "release_revoked", "external_representation_revalidation_blocked"); err != nil {
			return err
		}
		originatingActions := map[string]string{
			"explicit_unpublish":                           "publication.unpublish",
			"publisher_domain_revoked":                     "publisher.publication_domain_revoked",
			"release_retired":                              "listing_release.retired",
			"release_superseded":                           "listing_release.superseded",
			"release_revoked":                              "listing_release.revoked",
			"external_representation_revalidation_blocked": "version_revision.validated",
		}
		if text("originatingAction") != originatingActions[text("cause")] {
			return errors.New("publication withdrawal cause does not match its originating action")
		}
		_, hasARD := facts["ardDigest"]
		_, hasAI := facts["aiCatalogDigest"]
		if hasARD != hasAI {
			return errors.New("publication withdrawal document digests are incomplete")
		}
	case ActionJobCancellationRequested:
		if failureClass := text("failureClass"); failureClass != "" && !slices.Contains(sourceSyncFailureClasses, failureClass) {
			return errors.New("job cancellation failure class is invalid")
		}
		if err := oneOf("beforeState", jobStates...); err != nil {
			return err
		}
		if err := oneOf("afterState", jobStates...); err != nil {
			return err
		}
		requested, _ := facts["cancellationRequested"].(bool)
		switch text("beforeState") {
		case "queued", "waiting":
			if requested || text("afterState") != "cancelled" {
				return errors.New("job cancellation facts are inconsistent")
			}
		case "running":
			if !requested || text("afterState") != "running" {
				return errors.New("job cancellation facts are inconsistent")
			}
		default:
			if text("afterState") != text("beforeState") {
				return errors.New("terminal job cancellation facts are inconsistent")
			}
		}
	case ActionJobRetried:
		if failureClass := text("failureClass"); failureClass != "" && !slices.Contains(sourceSyncFailureClasses, failureClass) {
			return errors.New("job retry failure class is invalid")
		}
		if !slices.Contains([]string{"failed", "dead_letter"}, text("beforeState")) || text("afterState") != "queued" || text("parentJobId") == text("replacementJobId") {
			return errors.New("job retry state facts are inconsistent")
		}
	case ActionWebhookSecretsReencrypted:
		if number("verified") < 1 || number("verified") > 100 || number("changed") < 0 || number("changed") > number("verified") || text("firstId") > text("lastId") {
			return errors.New("webhook re-encryption batch facts are inconsistent")
		}
	case ActionWebhookCreated, ActionWebhookUpdated, ActionWebhookDisabled, ActionWebhookSecretRotated:
		if err := positive("revision", "secretVersion"); err != nil {
			return err
		}
		if number("secretVersion") > number("revision") {
			return errors.New("webhook secret version exceeds its revision")
		}
		if err := oneOf("status", "active", "disabled"); err != nil {
			return err
		}
		_, hasRevoke := facts["revokePrevious"]
		if hasRevoke != (action == ActionWebhookSecretRotated) {
			return errors.New("webhook revocation fact is reserved for secret rotation")
		}
		if action == ActionWebhookCreated && (number("revision") != 1 || number("secretVersion") != 1 || text("status") != "active") || action == ActionWebhookDisabled && text("status") != "disabled" || action == ActionWebhookSecretRotated && (text("status") != "active" || number("secretVersion") < 2) {
			return errors.New("webhook lifecycle facts are inconsistent")
		}
	case ActionWebhookDeliveryAttempted:
		if number("attempt") < 1 || number("attempt") > 20 || number("httpStatus") != 0 && (number("httpStatus") < 100 || number("httpStatus") > 599) {
			return errors.New("webhook delivery attempt facts are outside their bounds")
		}
		if err := oneOf("state", "waiting", "succeeded", "dead_letter"); err != nil {
			return err
		}
		if (text("state") == "succeeded") != (number("httpStatus") >= 200 && number("httpStatus") < 300) {
			return errors.New("webhook delivery outcome and HTTP status disagree")
		}
	case ActionWebhookDeliveriesRead:
		if number("deliveryCount") < 0 || number("deliveryCount") > 100 {
			return errors.New("webhook delivery access count exceeds its page bound")
		}
	case ActionSearchProjectionRebuilt:
		if err := positive("processed", "afterGeneration"); err != nil {
			return err
		}
		if err := nonNegative("beforeGeneration"); err != nil {
			return err
		}
		// PostgreSQL invalidates the conservative search generation once for
		// every observation row rebuilt by the bounded repair statement.
		if number("afterGeneration") != number("beforeGeneration")+number("processed") {
			return errors.New("search projection generation facts are inconsistent")
		}
		if text("selectedFirstId") > text("selectedLastId") {
			return errors.New("search projection selected ID range is reversed")
		}
	case ActionCatalogDocumentRead, ActionRawObservationRead, ActionSourceTrustRead:
		return nonNegative("sizeBytes")
	case ActionFederationLiveSearchDiagnostic:
		if err := oneOf("outcome", outboundOutcomes...); err != nil {
			return err
		}
		if err := nonNegative("resultCount", "diagnosticCount"); err != nil {
			return err
		}
		partial, _ := facts["partial"].(bool)
		if text("outcome") == "partial" && !partial || text("outcome") == "succeeded" && partial {
			return errors.New("live-search outcome and partial facts are inconsistent")
		}
		for _, name := range []string{"contactedSourceIds", "policyDigests"} {
			values, _ := facts[name].([]any)
			for index := 1; index < len(values); index++ {
				previous, _ := values[index-1].(string)
				current, _ := values[index].(string)
				if previous >= current {
					return fmt.Errorf("audit fact %q must be sorted and unique", name)
				}
			}
		}
	}
	return nil
}

var outboundOutcomes = []string{"succeeded", "policy_denied", "transport_error", "http_error", "parse_error", "negative_result", "partial", "invalid_input"}
var sourceSyncFailureClasses = []string{"invalid_input", "policy_denied", "authentication", "rate_limited", "transient_upstream", "permanent_upstream", "dependency", "internal", "cancelled"}
var jobStates = []string{"queued", "running", "waiting", "succeeded", "failed", "cancelled", "dead_letter"}

// integerFact converts exact integer representations without accepting
// floating-point values or overflowing int64.
func integerFact(value any) (int64, bool) {
	switch number := value.(type) {
	case json.Number:
		result, err := number.Int64()
		return result, err == nil
	case int:
		return int64(number), true
	case int8:
		return int64(number), true
	case int16:
		return int64(number), true
	case int32:
		return int64(number), true
	case int64:
		return number, true
	case uint:
		if uint64(number) <= math.MaxInt64 {
			return int64(number), true
		}
	case uint8:
		return int64(number), true
	case uint16:
		return int64(number), true
	case uint32:
		return int64(number), true
	case uint64:
		if number <= math.MaxInt64 {
			return int64(number), true
		}
	}
	return 0, false
}

// safeProfileName reports whether a bounded lowercase profile label satisfies
// the permitted hyphen syntax.
func safeProfileName(value string) bool {
	if value == "" || len(value) > 63 {
		return false
	}
	first := value[0]
	if !((first >= 'a' && first <= 'z') || (first >= '0' && first <= '9')) {
		return false
	}
	for index, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' && index > 0 && index < len(value)-1 {
			continue
		}
		return false
	}
	return true
}

// validateValue recursively enforces details depth, member, array and text
// bounds on JSON-shaped facts.
func validateValue(value any, depth int, members *int) error {
	if depth > MaxDetailsDepth {
		return errors.New("maximum nesting depth exceeded")
	}
	switch typed := value.(type) {
	case nil, bool, string, json.Number, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		if text, ok := typed.(string); ok {
			if len(text) > MaxDetailsBytes || !utf8.ValidString(text) {
				return errors.New("string value is invalid or too large")
			}
			for _, character := range text {
				if unicode.IsControl(character) {
					return errors.New("string value contains a control character")
				}
			}
		}
		return nil
	case map[string]any:
		*members += len(typed)
		if *members > MaxDetailsMembers {
			return errors.New("too many object members")
		}
		for name, child := range typed {
			if boundedText("fact member name", name, 128) != nil {
				return errors.New("object member name is invalid")
			}
			if err := validateValue(child, depth+1, members); err != nil {
				return err
			}
		}
		return nil
	case []any:
		if len(typed) > MaxDetailsArrayEntries {
			return errors.New("array has too many entries")
		}
		for _, child := range typed {
			if err := validateValue(child, depth+1, members); err != nil {
				return err
			}
		}
		return nil
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return errors.New("unsupported value")
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var normalized any
		if err := decoder.Decode(&normalized); err != nil {
			return errors.New("unsupported value")
		}
		return validateValue(normalized, depth, members)
	}
}

// hasAnyDetail reports whether a details object contains any of the supplied
// field names, including explicit null values.
func hasAnyDetail(details map[string]any, names ...string) bool {
	for _, name := range names {
		if _, present := details[name]; present {
			return true
		}
	}
	return false
}

// cloneFacts copies JSON-shaped facts with exact numbers; unencodable values
// remain available for subsequent validation failure.
func cloneFacts(facts map[string]any) map[string]any {
	if len(facts) == 0 {
		return nil
	}
	raw, err := json.Marshal(facts)
	if err != nil {
		return facts
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil {
		return facts
	}
	return result
}

// boundedText requires a nonempty, already trimmed UTF-8 value within the byte
// bound and without control characters.
func boundedText(name, value string, maximum int) error {
	if len(value) < 1 || len(value) > maximum || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return fmt.Errorf("audit %s is invalid", name)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("audit %s contains a control character", name)
		}
	}
	return nil
}

// validOptionalDigest accepts an absent digest or the canonical sha256:
// spelling.
func validOptionalDigest(value string) bool {
	return value == "" || digestPattern.MatchString(value)
}
