import { isProcessInfo, isBuildInfo } from "../api/build-info";
import { validCriteria } from "./personal-criteria";
// Checks for fields consumed by workflow views. Unknown evidence stays unchanged.
// These predicates do not assert whole-schema conformance or reconstruct records.
import {
  object,
  text,
  flag,
  number,
  array,
  choice,
  cursor,
  identifier,
  timestamp,
  optional,
} from "../api/values";
export function AuditEvent(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    text(value.actor) &&
    text(value.action) &&
    text(value.targetType) &&
    text(value.targetId) &&
    optional(
      value.requestId,
      (_requestIdValue) =>
        typeof _requestIdValue === "string" &&
        /^[A-Za-z0-9._-]{1,128}$/.test(_requestIdValue),
    ) &&
    optional(value.reason, text) &&
    timestamp(value.at)
  );
}

export function AuditEventPage(value: unknown): boolean {
  return (
    object(value) && array(value.items, AuditEvent) && cursor(value.nextCursor)
  );
}

export function Capabilities(value: unknown): boolean {
  return (
    object(value) &&
    array(value.resourceKinds, (item) =>
      choice(item, ["agent", "mcp_server", "skill", "plugin"]),
    ) &&
    object(value.profiles) &&
    Object.values(value.profiles).every((item) => array(item, text)) &&
    array(value.operations, text) &&
    object(value.governance) &&
    Object.values(value.governance).every(text) &&
    object(value.search) &&
    text(value.search.profile) &&
    choice(value.search.semantic, ["enabled", "disabled", "unavailable"]) &&
    flag(value.search.degraded) &&
    optional(value.search.providerState, (_providerStateValue) =>
      choice(_providerStateValue, ["unknown", "healthy", "degraded"]),
    ) &&
    optional(
      value.search.indexing,
      (_indexingValue) =>
        object(_indexingValue) &&
        choice(_indexingValue.state, [
          "pending",
          "building",
          "ready",
          "failed",
        ]) &&
        optional(_indexingValue.jobId, identifier) &&
        number(_indexingValue.indexed, 0, 100000, true) &&
        number(_indexingValue.failed, 0, 100000, true),
    ) &&
    object(value.optionalCapabilities) &&
    Object.values(value.optionalCapabilities).every((item) =>
      choice(item, ["enabled", "disabled", "unavailable"]),
    ) &&
    choice(value.optionalCapabilities.webhooks, [
      "enabled",
      "disabled",
      "unavailable",
    ]) &&
    object(value.limits) &&
    Object.values(value.limits).every((item) => number(item, 0)) &&
    object(value.profileBundle) &&
    text(value.profileBundle.digest) &&
    number(value.profileBundle.artifacts, 0, Infinity, true) &&
    array(
      value.degradations,
      (item) => object(item) && text(item.component) && text(item.reason),
    )
  );
}

export function CatalogDisplaySummary(value: unknown): boolean {
  return object(value) && text(value.displayName);
}

export function CatalogExploreResponse(value: unknown): boolean {
  return (
    object(value) &&
    optional(value.tier, (_tierValue) =>
      choice(_tierValue, ["core", "hybrid"]),
    ) &&
    optional(value.fallbackReason, (_fallbackReasonValue) =>
      choice(_fallbackReasonValue, [
        "indexing",
        "provider_unavailable",
        "index_changed",
        "work_limit",
      ]),
    ) &&
    optional(value.partialIndex, flag) &&
    number(value.matched, 0, Infinity, true) &&
    object(value.facets) &&
    Object.values(value.facets).every(CatalogFacetResult)
  );
}

export function CatalogFacetBucket(value: unknown): boolean {
  return (
    object(value) && text(value.value) && number(value.count, 1, Infinity, true)
  );
}

export function CatalogFacetResult(value: unknown): boolean {
  return (
    object(value) &&
    array(value.buckets, CatalogFacetBucket) &&
    optional(value.otherCount, (_otherCountValue) =>
      number(_otherCountValue, 1, Infinity, true),
    )
  );
}

export function CatalogListing(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    identifier(value.namespaceId) &&
    identifier(value.resourceId) &&
    optional(value.candidateRevisionId, identifier) &&
    optional(value.currentReleaseId, identifier)
  );
}

/** Admit only bounded current labels and an exact compact license or explicit omission. */
export function CatalogPresentation(value: unknown): boolean {
  return (
    object(value) &&
    Object.keys(value).every((key) =>
      ["publisherName", "namespaceName", "license", "licenseOmitted"].includes(
        key,
      ),
    ) &&
    [value.publisherName, value.namespaceName].every(
      (v) =>
        v === undefined ||
        (text(v) && [...v].length > 0 && [...v].length <= 160),
    ) &&
    optional(
      value.license,
      (_licenseValue) =>
        text(_licenseValue) &&
        [..._licenseValue].length > 0 &&
        [..._licenseValue].length <= 256,
    ) &&
    optional(
      value.licenseOmitted,
      (_licenseOmittedValue) => _licenseOmittedValue === true,
    ) &&
    !(value.license !== undefined && value.licenseOmitted !== undefined)
  );
}

export function CatalogRecord(value: unknown): boolean {
  return (
    object(value) &&
    optional(value.display, CatalogPresentation) &&
    Resource(value.resource) &&
    ResourceVersion(value.version) &&
    VersionRevision(value.versionRevision) &&
    CatalogListing(value.listing) &&
    ListingRevision(value.listingRevision) &&
    ListingRelease(value.release) &&
    EvidenceSet(value.evidence)
  );
}

export function CatalogRecordPage(value: unknown): boolean {
  return (
    object(value) &&
    array(value.items, CatalogSummary) &&
    cursor(value.nextCursor)
  );
}

export function CatalogReleaseSummary(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    choice(value.state, [
      "published",
      "deprecated",
      "retired",
      "superseded",
      "revoked",
    ])
  );
}

export function CatalogResourceSummary(value: unknown): boolean {
  return (
    object(value) &&
    choice(value.kind, ["agent", "mcp_server", "skill", "plugin"])
  );
}

export function CatalogSearchDiagnostic(value: unknown): boolean {
  return (
    object(value) &&
    optional(value.sourceId, sourceUUID) &&
    choice(value.status, [
      "failed",
      "invalid_response",
      "insufficient_evidence",
      "policy_stale",
      "fanout_limited",
      "diagnostics_truncated",
      "live_results_limited",
      "continuation_unavailable",
    ]) &&
    optional(value.message, text)
  );
}

export function CatalogSearchLocal(value: unknown): boolean {
  return (
    object(value) &&
    optional(value.display, CatalogPresentation) &&
    object(value.listing) &&
    identifier(value.listing.namespaceId) &&
    object(value.listingRevision) &&
    choice(value.listingRevision.visibility, ["private", "tenant", "public"]) &&
    CatalogReleaseSummary(value.release)
  );
}

export function CatalogSearchResponse(value: unknown): boolean {
  return (
    object(value) &&
    array(value.items, CatalogSearchResult) &&
    cursor(value.nextCursor, 4096) &&
    number(value.durableTotal, 0, Infinity, true) &&
    number(value.liveCount, 0, 50, true) &&
    choice(value.tier, ["core", "hybrid"]) &&
    optional(value.fallbackReason, (_fallbackReasonValue2) =>
      choice(_fallbackReasonValue2, [
        "indexing",
        "provider_unavailable",
        "index_changed",
        "work_limit",
      ]),
    ) &&
    optional(value.partialIndex, flag) &&
    flag(value.partial) &&
    optional(value.diagnostics, (_diagnosticsValue) =>
      array(_diagnosticsValue, CatalogSearchDiagnostic),
    )
  );
}

export function CatalogSearchResult(value: unknown): boolean {
  return (
    object(value) &&
    text(value.id) &&
    choice(value.type, ["local", "indexed", "live"]) &&
    text(value.identifier) &&
    optional(value.version, text) &&
    flag(value.versionPresent) &&
    choice(value.kind, ["agent", "mcp_server", "skill", "plugin"]) &&
    text(value.displayName) &&
    optional(value.description, text) &&
    array(value.tags, text) &&
    optional(value.publisher, text) &&
    optional(value.local, CatalogSearchLocal) &&
    optional(value.indexed, IndexedCatalogSearchEvidence) &&
    optional(value.live, LiveCatalogSearchEvidence) &&
    (value.type === "local"
      ? value.local !== undefined &&
        value.indexed === undefined &&
        value.live === undefined
      : value.type === "indexed"
        ? value.indexed !== undefined &&
          value.local === undefined &&
          value.live === undefined
        : value.live !== undefined &&
          value.local === undefined &&
          value.indexed === undefined) &&
    optional(
      value.ratings,
      (_ratingsValue) =>
        object(_ratingsValue) &&
        number(_ratingsValue.count, 0, Number.MAX_SAFE_INTEGER, true) &&
        (_ratingsValue.count === 0
          ? _ratingsValue.average === undefined
          : number(_ratingsValue.average, 1, 5, false)) &&
        optional(_ratingsValue.receipt, text),
    ) &&
    optional(
      value.peerRatings,
      (_peerRatingsValue) =>
        object(_peerRatingsValue) &&
        text(_peerRatingsValue.reporter) &&
        number(_peerRatingsValue.count, 1, Number.MAX_SAFE_INTEGER, true) &&
        number(_peerRatingsValue.average, 1, 5, false) &&
        timestamp(_peerRatingsValue.measuredAt),
    ) &&
    optional(
      value.importedHere,
      (_importedHereValue) =>
        object(_importedHereValue) &&
        identifier(_importedHereValue.resourceId) &&
        flag(_importedHereValue.changedEntry),
    ) &&
    // Empty versions may be omitted by the Go JSON encoder. Presence remains
    // authoritative, independently of that optional value.
    (value.versionPresent || value.version === undefined)
  );
}

export function CatalogSummary(value: unknown): boolean {
  return (
    object(value) &&
    optional(value.display, CatalogPresentation) &&
    CatalogResourceSummary(value.resource) &&
    CatalogVersionSummary(value.version) &&
    CatalogDisplaySummary(value.listingRevision) &&
    CatalogReleaseSummary(value.release)
  );
}

export function CatalogVersionSummary(value: unknown): boolean {
  return object(value) && text(value.version);
}

export function EvidenceSet(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    text(value.digest) &&
    choice(value.state, ["complete", "blocked"]) &&
    optional(value.findings, (_findingsValue) =>
      array(_findingsValue, Finding),
    ) &&
    text(value.policyVersion) &&
    text(value.validationProfile) &&
    optional(value.expiresAt, timestamp) &&
    LegalRightsSummary(value.legalRights) &&
    TrustSummary(value.trust)
  );
}

export function FederationChangePreview(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    identifier(value.tenantId) &&
    identifier(value.sourceId) &&
    choice(value.intent, ["update", "remove"]) &&
    number(value.sourceRevision, 1, Infinity, true) &&
    number(value.currentMatched, 0, Infinity, true) &&
    number(value.proposedMatched, 0, Infinity, true) &&
    number(value.excludedObservations, 0, Infinity, true) &&
    number(value.artworkOverridesAffected, 0, 10000, true) &&
    flag(value.sendsQueriesUpstream) &&
    text(value.configurationDigest) &&
    /^sha256:[a-f0-9]{64}$/.test(value.configurationDigest) &&
    text(value.observationSetDigest) &&
    /^sha256:[a-f0-9]{64}$/.test(value.observationSetDigest) &&
    text(value.digest) &&
    /^sha256:[a-f0-9]{64}$/.test(value.digest) &&
    timestamp(value.createdAt) &&
    timestamp(value.expiresAt) &&
    Date.parse(value.createdAt as string) <
      Date.parse(value.expiresAt as string)
  );
}

/** The server digest is opaque; compare returned bindings without reconstructing fingerprints. */
export function FederationPreviewFor(
  value: unknown,
  tenantId: string,
  sourceId: string,
  sourceRevision: number,
  intent: "update" | "remove",
): boolean {
  return (
    object(value) &&
    FederationChangePreview(value) &&
    value.tenantId === tenantId &&
    value.sourceId === sourceId &&
    value.sourceRevision === sourceRevision &&
    value.intent === intent
  );
}

export function FederationSnapshot(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    identifier(value.sourceId) &&
    choice(value.state, ["running", "complete", "incomplete", "failed"]) &&
    flag(value.withdrawalAuthority) &&
    flag(value.enumerationComplete) &&
    optional(value.reconcileCurrentView, flag) &&
    number(value.memberCount, 0, Infinity, true) &&
    number(value.added, 0, Infinity, true) &&
    number(value.updated, 0, Infinity, true) &&
    number(value.withdrawn, 0, Infinity, true) &&
    optional(value.completedAt, timestamp) &&
    timestamp(value.startedAt)
  );
}

export function FederationSnapshotPage(value: unknown): boolean {
  return (
    object(value) &&
    array(value.items, FederationSnapshot) &&
    cursor(value.nextCursor)
  );
}

export function FederationSource(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    text(value.displayName) &&
    text(value.endpoint) &&
    choice(value.status, ["active", "paused", "error", "removed"]) &&
    text(value.profileId) &&
    choice(value.retrievalMode, ["indexed", "live", "hybrid"]) &&
    SourceScope(value.scope) &&
    choice(value.updateMode, ["manual_review", "automatic"]) &&
    LivePolicy(value.live) &&
    ReferralPolicy(value.referrals) &&
    optional(value.discoveryAssignments, (_discoveryAssignmentsValue) =>
      array(_discoveryAssignmentsValue, DiscoveryAssignment),
    ) &&
    NestingPolicy(value.nesting) &&
    optional(value.credentialBinding, text) &&
    optional(
      value.trustVerificationPolicy,
      (_trustVerificationPolicyValue) =>
        text(_trustVerificationPolicyValue) &&
        validTrustPolicy(_trustVerificationPolicyValue),
    ) &&
    optional(value.networkProfile, text) &&
    number(value.revision, 1, Infinity, true) &&
    optional(value.lastSyncAt, timestamp) &&
    optional(value.lastCompleteSnapshotId, identifier) &&
    optional(value.lastError, text)
  );
}

/** An exact current read must match the route and current tenant before it supplies actions. */
export function FederationSourceFor(
  value: unknown,
  tenantId: string,
  sourceId: string,
): boolean {
  return (
    object(value) &&
    FederationSource(value) &&
    value.tenantId === tenantId &&
    value.id === sourceId
  );
}

export function FederationSourceInspection(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    text(value.resolvedUrl) &&
    text(value.profileId) &&
    number(value.entryCount, 0, Infinity, true) &&
    optional(value.warnings, (_warningsValue) =>
      array(_warningsValue, Finding),
    ) &&
    optional(value.networkProfile, text) &&
    optional(value.credentialBinding, text) &&
    timestamp(value.expiresAt)
  );
}

/** Bind explicit reinspection to its captured destination, tenant and opaque deployment references. */
export function FederationInspectionFor(
  value: unknown,
  tenantId: string,
  request: {
    url: string;
    networkProfile?: string;
    credentialBinding?: string;
  },
): boolean {
  return (
    object(value) &&
    FederationSourceInspection(value) &&
    value.tenantId === tenantId &&
    value.requestedUrl === request.url &&
    value.networkProfile === request.networkProfile &&
    value.credentialBinding === request.credentialBinding
  );
}

export function FederationSourcePage(value: unknown): boolean {
  return (
    object(value) &&
    array(value.items, FederationSource) &&
    cursor(value.nextCursor)
  );
}

export function Feedback(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    identifier(value.membershipId) &&
    identifier(value.resourceId) &&
    optional(value.releaseId, identifier) &&
    optional(value.versionRevisionId, identifier) &&
    choice(value.category, [
      "problem",
      "documentation",
      "suggestion",
      "positive",
      "other",
    ]) &&
    choice(value.state, ["open", "addressed", "withdrawn", "removed"]) &&
    optional(value.comment, text) &&
    optional(value.response, text) &&
    number(value.revision, 1, Infinity, true) &&
    timestamp(value.createdAt) &&
    timestamp(value.updatedAt)
  );
}

export function FeedbackPage(value: unknown): boolean {
  return (
    object(value) && array(value.items, Feedback) && cursor(value.nextCursor)
  );
}

export function FeedbackReceipt(value: unknown): boolean {
  return object(value) && identifier(value.id) && number(value.revision, 1);
}

export function Finding(value: unknown): boolean {
  return (
    object(value) &&
    text(value.code) &&
    text(value.message) &&
    choice(value.severity, ["info", "warning", "error"]) &&
    optional(value.path, text)
  );
}

export function IndexedCatalogSearchEvidence(value: unknown): boolean {
  return (
    object(value) &&
    text(value.observationGroupId) &&
    /^observation-group-v1:sha256:[a-f0-9]{64}$/.test(
      value.observationGroupId,
    ) &&
    text(value.normalizedEntryDigest) &&
    Array.isArray(value.sourceIds) &&
    array(value.sourceIds, sourceUUID) &&
    value.sourceIds.length <= 16 &&
    new Set(value.sourceIds).size === value.sourceIds.length &&
    number(value.sourceCount, 1, Infinity, true) &&
    flag(value.sourcesTruncated) &&
    flag(value.conflict) &&
    flag(value.sameSourceConflict)
  );
}

/** Source references used for literal copy and local routes follow the UUID contract. */
function sourceUUID(value: unknown): value is string {
  return (
    text(value) &&
    /^[\da-f]{8}-[\da-f]{4}-[\da-f]{4}-[\da-f]{4}-[\da-f]{12}$/i.test(value)
  );
}

export function Job(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    text(value.kind) &&
    optional(value.parentJobId, identifier) &&
    choice(value.state, [
      "queued",
      "running",
      "waiting",
      "succeeded",
      "failed",
      "cancelled",
      "dead_letter",
    ]) &&
    number(value.priority, -100, 100, true) &&
    number(value.progress, 0, 100, true) &&
    timestamp(value.notBefore) &&
    number(value.attempt, 0, Infinity, true) &&
    number(value.attemptLimit, 1, 20, true) &&
    flag(value.cancellationRequested) &&
    (!object(value.result) ||
      !choice(value.result.type, [
        "source_snapshot",
        "publication_snapshot",
        "semantic_index",
      ]) ||
      (JobResultSummary(value.result) &&
        (value.result.type !== "semantic_index" ||
          (value.kind === "semantic.reindex" &&
            value.state === "succeeded" &&
            value.result.generationId === value.id)))) &&
    optional(value.safeError, text) &&
    optional(value.failureClass, (_failureClassValue) =>
      choice(_failureClassValue, [
        "invalid_input",
        "policy_denied",
        "authentication",
        "rate_limited",
        "transient_upstream",
        "permanent_upstream",
        "dependency",
        "internal",
        "cancelled",
      ]),
    ) &&
    timestamp(value.createdAt) &&
    timestamp(value.updatedAt)
  );
}

/** Validate known summaries before they can produce navigation or state claims. */
function JobResultSummary(value: Record<string, unknown>): boolean {
  if (value.type === "semantic_index")
    return (
      identifier(value.generationId) && number(value.members, 0, Infinity, true)
    );
  return (
    identifier(value.snapshotId) &&
    (value.type === "source_snapshot"
      ? identifier(value.sourceId) &&
        choice(value.state, ["complete", "incomplete", "failed"])
      : text(value.host) &&
        value.host.length > 0 &&
        value.host.length <= 253 &&
        number(value.generation, 1, Infinity, true) &&
        choice(value.state, ["candidate", "active", "retired"]))
  );
}

/** Bind a Job acknowledgement or read to the current tenant and expected family. */
export function JobFor(
  value: unknown,
  tenantId: string,
  kind?: string,
): boolean {
  return (
    Job(value) &&
    object(value) &&
    value.tenantId === tenantId &&
    (kind === undefined || value.kind === kind)
  );
}

export function JobPage(value: unknown): boolean {
  return object(value) && array(value.items, Job) && cursor(value.nextCursor);
}

export function LegalRightsSummary(value: unknown): boolean {
  return object(value) && choice(value.state, ["not_assessed"]);
}

export function ListingPublished(value: unknown): value is {
  listing: Record<string, unknown>;
  release: Record<string, unknown>;
  priorRelease: Record<string, unknown> | null;
} {
  return (
    object(value) &&
    object(value.listing) &&
    CatalogListing(value.listing) &&
    identifier(value.listing.tenantId) &&
    object(value.release) &&
    ListingRelease(value.release) &&
    identifier(value.release.tenantId) &&
    (value.priorRelease === null ||
      (object(value.priorRelease) &&
        ListingRelease(value.priorRelease) &&
        identifier(value.priorRelease.tenantId)))
  );
}

export function ListingRelease(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    identifier(value.listingId) &&
    identifier(value.submissionId) &&
    identifier(value.versionRevisionId) &&
    identifier(value.listingRevisionId) &&
    number(value.revision, 1) &&
    timestamp(value.updatedAt) &&
    optional(value.replacementReleaseId, identifier) &&
    optional(
      value.dispositionReason,
      (_dispositionReasonValue) =>
        text(_dispositionReasonValue) &&
        [..._dispositionReasonValue].length <= 2000,
    ) &&
    optional(value.sunsetAt, timestamp) &&
    choice(value.state, [
      "published",
      "deprecated",
      "retired",
      "superseded",
      "revoked",
    ]) &&
    timestamp(value.activatedAt)
  );
}

export function ListingRevision(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    identifier(value.listingId) &&
    number(value.revisionNumber, 1, Infinity, true) &&
    identifier(value.versionRevisionId) &&
    text(value.displayName) &&
    text(value.description) &&
    optional(value.tags, (_tagsValue) => array(_tagsValue, text)) &&
    optional(
      value.categories,
      (_categoriesValue) =>
        Array.isArray(_categoriesValue) &&
        _categoriesValue.length <= 16 &&
        _categoriesValue.every(
          (item) =>
            text(item) && [...item].length <= 80 && !item.includes("\0"),
        ),
    ) &&
    optional(value.representativeQueries, (_representativeQueriesValue) =>
      array(_representativeQueriesValue, text),
    ) &&
    identifier(value.publisherId) &&
    optional(value.documentationUrl, text) &&
    optional(value.supportUrl, text) &&
    optional(value.license, text) &&
    optional(value.termsUrl, text) &&
    choice(value.visibility, ["private", "tenant", "public"]) &&
    optional(value.relationships, (_relationshipsValue) =>
      array(_relationshipsValue, Relationship),
    )
  );
}

export function ListingRevisionCreated(value: unknown): boolean {
  return (
    object(value) &&
    CatalogListing(value.listing) &&
    ListingRevision(value.revision)
  );
}

export function LiveCatalogSearchEvidence(value: unknown): boolean {
  return object(value) && text(value.source);
}

export function LivePolicy(value: unknown): boolean {
  return (
    object(value) &&
    optional(value.enabled, flag) &&
    optional(value.referralOnly, flag) &&
    optional(value.queryPrivacyAcknowledged, flag) &&
    optional(value.anonymousDisclosureAcknowledged, flag) &&
    optional(value.federationMode, (_federationModeValue) =>
      choice(_federationModeValue, ["none", "referrals"]),
    )
  );
}

export function LiveSearchResponse(value: unknown): boolean {
  return (
    object(value) &&
    Array.isArray(value.results) &&
    value.results.length <= 50 &&
    array(value.results, LiveSearchResult) &&
    flag(value.partial) &&
    optional(
      value.diagnostics,
      (_diagnosticsValue2) =>
        Array.isArray(_diagnosticsValue2) &&
        _diagnosticsValue2.length <= 32 &&
        array(_diagnosticsValue2, LiveSearchDiagnostic),
    )
  );
}

export function LiveSearchResult(value: unknown): boolean {
  return (
    object(value) &&
    text(value.identifier) &&
    flag(value.versionPresent) &&
    optional(value.version, text) &&
    number(value.score, 0, 100, true) &&
    sourceUUID(value.sourceId) &&
    number(value.sourceRevision, 1) &&
    text(value.exactEntryDigest) &&
    value.exactEntryDigest.length === 71 &&
    /^sha256:[a-f0-9]{64}$/.test(value.exactEntryDigest) &&
    text(value.source) &&
    object(value.entry)
  );
}
function validTrustPolicy(value: string): boolean {
  return (
    value === value.trim() &&
    /^(?:|[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)$/.test(value)
  );
}

export function LiveSearchDiagnostic(value: unknown): boolean {
  return (
    object(value) &&
    optional(value.sourceId, sourceUUID) &&
    choice(value.status, [
      "succeeded",
      "failed",
      "invalid_response",
      "insufficient_evidence",
      "policy_stale",
      "fanout_limited",
      "diagnostics_truncated",
      "continuation_unavailable",
    ]) &&
    optional(value.message, text)
  );
}

export function Membership(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    identifier(value.realmId) &&
    text(value.subject) &&
    choice(value.actorType, ["human", "service"]) &&
    choice(value.state, ["active", "suspended", "offboarded"])
  );
}

export function MembershipLifecycleResult(value: unknown): boolean {
  return (
    object(value) &&
    Membership(value.membership) &&
    number(value.revokedRoleAssignments, 0, Infinity, true) &&
    number(value.revokedNamespaceMemberships, 0, Infinity, true) &&
    optional(value.revokedGroupMemberships, (_revokedGroupMembershipsValue) =>
      number(_revokedGroupMembershipsValue, 0, Infinity, true),
    )
  );
}

export function MembershipPage(value: unknown): boolean {
  return (
    object(value) && array(value.items, Membership) && cursor(value.nextCursor)
  );
}

export function Namespace(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    text(value.slug) &&
    text(value.displayName) &&
    optional(value.description, text) &&
    number(value.revision, 1, Infinity, true)
  );
}

export function NamespacePage(value: unknown): boolean {
  return (
    object(value) && array(value.items, Namespace) && cursor(value.nextCursor)
  );
}

export function Observation(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    identifier(value.sourceId) &&
    text(value.sourceKey) &&
    optional(value.identifier, text) &&
    optional(value.version, text) &&
    flag(value.versionPresent) &&
    optional(value.kind, (_kindValue) =>
      choice(_kindValue, ["agent", "mcp_server", "skill", "plugin"]),
    ) &&
    choice(value.completeness, ["complete", "projection", "unresolved"]) &&
    ObservationCompatibility(value.compatibility) &&
    choice(value.freshness, ["current", "stale", "withdrawn", "source_removed"])
  );
}

export function ObservationAdoption(value: unknown): boolean {
  return object(value) && Resource(value.resource);
}

export function ObservationCompatibility(value: unknown): boolean {
  return (
    object(value) &&
    choice(value.structural, ["valid", "invalid"]) &&
    text(value.profileId) &&
    choice(value.support, ["supported", "unknown"])
  );
}

export function ObservationGroup(value: unknown): boolean {
  return (
    object(value) &&
    text(value.identifier) &&
    flag(value.conflict) &&
    flag(value.contributionsTruncated) &&
    array(value.members, ObservationGroupMember)
  );
}

export function ObservationGroupContribution(value: unknown): boolean {
  return (
    object(value) &&
    Observation(value.observation) &&
    number(value.occurrenceCount, 1, Infinity, true) &&
    flag(value.sourceConflict)
  );
}

export function ObservationGroupMember(value: unknown): boolean {
  return (
    object(value) &&
    text(value.normalizedEntryDigest) &&
    array(value.contributions, ObservationGroupContribution)
  );
}

export function ObservationOccurrence(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.sourceId) &&
    number(value.sourceRevision, 1, Infinity, true) &&
    identifier(value.snapshotId) &&
    text(value.retrievalDigest) &&
    text(value.entryLocation) &&
    choice(value.evidenceComplete, [true]) &&
    timestamp(value.observedAt)
  );
}

export function ObservationOccurrencePage(value: unknown): boolean {
  return (
    object(value) &&
    array(value.items, ObservationOccurrence) &&
    cursor(value.nextCursor)
  );
}

export function ObservationPage(value: unknown): boolean {
  return (
    object(value) && array(value.items, Observation) && cursor(value.nextCursor)
  );
}

export function ObservationSearchPage(value: unknown): boolean {
  return (
    object(value) &&
    Array.isArray(value.items) &&
    value.items.length <= 100 &&
    array(value.items, ObservationSearchResult) &&
    cursor(value.nextCursor)
  );
}

export function PluginInspection(value: unknown): boolean {
  return (
    object(value) &&
    Array.isArray(value.files) &&
    array(value.documents, (item) => object(item) && text(item.path)) &&
    optional(value.components, (_componentsValue) =>
      array(
        _componentsValue,
        (item) =>
          object(item) &&
          choice(item.kind, ["skill", "mcp_server"]) &&
          text(item.name) &&
          text(item.path) &&
          optional(item.findings, (_findingsValue2) =>
            array(_findingsValue2, Finding),
          ),
      ),
    )
  );
}

export function PublicationDomainRevocation(value: unknown): boolean {
  return (
    object(value) &&
    Publisher(value.publisher) &&
    array(value.affectedReleaseIds, text) &&
    array(value.withdrawnHosts, text)
  );
}

export function PublicationExclusion(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.resourceId) &&
    text(value.identifier) &&
    array(value.profiles, text) &&
    array(value.reasons, text)
  );
}

export function PublicationSnapshot(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    text(value.host) &&
    choice(value.audience, ["anonymous", "authenticated"]) &&
    number(value.generation, 1, Infinity, true) &&
    choice(value.state, ["candidate", "active", "retired"]) &&
    text(value.ardProfile) &&
    text(value.aiCatalogProfile) &&
    object(value.ard) &&
    array(value.ard.entries, publicationEntry) &&
    object(value.aiCatalog) &&
    array(value.aiCatalog.entries, publicationEntry) &&
    array(value.exclusions, PublicationExclusion)
  );
}
function publicationEntry(value: unknown): boolean {
  return (
    object(value) &&
    text(value.identifier) &&
    text(value.type) &&
    optional(value.version, text) &&
    optional(
      value.metadata,
      (_metadataValue) =>
        object(_metadataValue) &&
        optional(_metadataValue.catalogReleaseId, text),
    )
  );
}

export function PublicationHostInventory(value: unknown): boolean {
  return (
    object(value) &&
    Array.isArray(value.items) &&
    value.items.length <= 64 &&
    array(
      value.items,
      (item) =>
        object(item) &&
        text(item.host) &&
        /^[a-z0-9.:-]{1,253}$/.test(item.host) &&
        choice(item.mode, ["disabled", "anonymous", "authenticated"]),
    ) &&
    new Set(value.items.map((item: { host: string }) => item.host)).size ===
      value.items.length
  );
}

export function PublicationSnapshotPage(value: unknown): value is {
  items: Record<string, unknown>[];
  nextCursor?: string;
} {
  return (
    object(value) &&
    array(value.items, PublicationSnapshotSummary) &&
    cursor(value.nextCursor)
  );
}

export function PublicationSnapshotSummary(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    identifier(value.tenantId) &&
    text(value.host) &&
    text(value.ardProfile) &&
    text(value.aiCatalogProfile) &&
    text(value.etag) &&
    timestamp(value.createdAt) &&
    optional(value.activatedAt, timestamp) &&
    choice(value.audience, ["anonymous", "authenticated"]) &&
    number(value.generation, 1, Infinity, true) &&
    choice(value.state, ["candidate", "active", "retired"])
  );
}

export function Publisher(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    text(value.displayName) &&
    array(value.domains, text) &&
    array(value.approvedPublicationDomains, text) &&
    number(value.revision, 1, Infinity, true)
  );
}

export function PublisherPage(value: unknown): boolean {
  return (
    object(value) && array(value.items, Publisher) && cursor(value.nextCursor)
  );
}

export function Relationship(value: unknown): boolean {
  return (
    object(value) &&
    choice(value.type, [
      "depends_on",
      "recommended_with",
      "supersedes",
      "bundled_with",
    ]) &&
    text(value.targetIdentifier) &&
    optional(value.targetVersion, text)
  );
}

export function Representation(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    choice(value.role, [
      "primary",
      "distribution",
      "metadata",
      "documentation",
    ]) &&
    text(value.mediaType) &&
    text(value.profileId) &&
    optional(value.url, text) &&
    text(value.contentDigest) &&
    number(value.size, 0, Infinity, true) &&
    optional(value.artifactId, identifier) &&
    choice(value.validationState, ["valid", "invalid"]) &&
    optional(value.plugin, PluginInspection) &&
    optional(value.package, object)
  );
}

export function Resource(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    identifier(value.namespaceId) &&
    text(value.canonicalIdentifier) &&
    choice(value.kind, ["agent", "mcp_server", "skill", "plugin"]) &&
    text(value.createdBy) &&
    timestamp(value.updatedAt)
  );
}

export function ResourcePage(value: unknown): boolean {
  return (
    object(value) && array(value.items, Resource) && cursor(value.nextCursor)
  );
}

export function ResourceVersion(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    identifier(value.resourceId) &&
    text(value.version) &&
    optional(value.candidateRevisionId, identifier) &&
    optional(value.sealedRevisionId, identifier)
  );
}

export function ReviewDecision(value: unknown): boolean {
  return object(value) && ReviewSubmission(value.submission);
}

export function ReviewSubmission(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    optional(
      value.canonicalIdentifier,
      (_canonicalIdentifierValue) =>
        text(_canonicalIdentifierValue) &&
        _canonicalIdentifierValue.length > 0 &&
        Array.from(_canonicalIdentifierValue).length <= 512 &&
        !_canonicalIdentifierValue.includes("\0"),
    ) &&
    optional(
      value.submittedDisplayName,
      (_submittedDisplayNameValue) =>
        text(_submittedDisplayNameValue) &&
        _submittedDisplayNameValue.length > 0 &&
        Array.from(_submittedDisplayNameValue).length <= 160 &&
        !_submittedDisplayNameValue.includes("\0"),
    ) &&
    optional(value.namespaceId, identifier) &&
    optional(
      value.listingConcurrencyRevision,
      (_listingConcurrencyRevisionValue) =>
        number(_listingConcurrencyRevisionValue, 1),
    ) &&
    identifier(value.listingId) &&
    identifier(value.versionRevisionId) &&
    identifier(value.listingRevisionId) &&
    identifier(value.evidenceSetId) &&
    text(value.evidenceDigest) &&
    text(value.policyVersion) &&
    choice(value.state, [
      "submitted",
      "approved",
      "changes_required",
      "rejected",
    ]) &&
    optional(
      value.decisions,
      (_decisionsValue) =>
        Array.isArray(_decisionsValue) &&
        _decisionsValue.length <= 100 &&
        array(
          _decisionsValue,
          (item) =>
            object(item) &&
            text(item.actor) &&
            choice(item.action, ["approve", "request_changes", "reject"]) &&
            timestamp(item.at) &&
            optional(item.reason, text),
        ),
    ) &&
    text(value.submittedBy) &&
    timestamp(value.submittedAt)
  );
}

export function RevisionEvidenceEnvelope(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.namespaceId) &&
    VersionRevision(value.revision) &&
    EvidenceSet(value.evidence)
  );
}

export function RevisionSummary(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    number(value.revisionNumber, 1, Infinity, true) &&
    text(value.createdBy)
  );
}

export function RoleAssignment(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    identifier(value.membershipId) &&
    text(value.subject) &&
    choice(value.role, [
      "reader",
      "contributor",
      "reviewer",
      "publisher",
      "source_admin",
      "security_reviewer",
      "tenant_admin",
      "service_client",
    ]) &&
    optional(value.namespaceId, identifier) &&
    choice(value.state, ["active", "revoked"]) &&
    number(value.revision, 1, Infinity, true)
  );
}

export function ServiceVersion(value: unknown): boolean {
  return (
    object(value) &&
    text(value.ardProfile) &&
    value.ardProfile.length > 0 &&
    text(value.aiCatalogProfile) &&
    value.aiCatalogProfile.length > 0 &&
    isBuildInfo(value)
  );
}

export function ServiceInformation(value: unknown): boolean {
  return (
    object(value) &&
    value.service === "open-agentic-catalog" &&
    choice(value.role, ["api", "all"]) &&
    ServiceVersion(value) &&
    isProcessInfo(value)
  );
}

export function SourceSyncAccepted(value: unknown): boolean {
  return object(value) && Job(value.job) && flag(value.existing);
}

export function TrustSummary(value: unknown): boolean {
  return (
    object(value) &&
    choice(value.state, [
      "not_assessed",
      "unsupported",
      "unverified",
      "verified",
      "failed",
      "expired",
    ])
  );
}

export function VersionEvidenceEnvelope(value: unknown): boolean {
  return (
    object(value) &&
    ResourceVersion(value.version) &&
    VersionRevision(value.revision) &&
    EvidenceSet(value.evidence)
  );
}

export function VersionRevision(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    identifier(value.resourceVersionId) &&
    number(value.revisionNumber, 1, Infinity, true) &&
    array(value.representations, Representation)
  );
}

export function Webhook(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    text(value.name) &&
    text(value.url) &&
    array(value.eventTypes, text) &&
    choice(value.status, ["active", "disabled"]) &&
    number(value.revision, 1, Infinity, true) &&
    number(value.secretVersion, 1, Infinity, true) &&
    timestamp(value.createdAt) &&
    timestamp(value.updatedAt)
  );
}

export function WebhookDelivery(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.id) &&
    identifier(value.eventId) &&
    text(value.eventType) &&
    choice(value.state, ["pending", "succeeded", "waiting", "dead_letter"]) &&
    number(value.attempt, 0, Infinity, true) &&
    optional(value.responseStatus, (_responseStatusValue) =>
      number(_responseStatusValue, 100, 599, true),
    ) &&
    optional(
      value.safeError,
      (_safeErrorValue) =>
        text(_safeErrorValue) && [..._safeErrorValue].length <= 2000,
    ) &&
    optional(value.nextAttemptAt, timestamp) &&
    timestamp(value.updatedAt)
  );
}

export function WebhookDeliveryPage(value: unknown): boolean {
  return (
    object(value) &&
    array(value.items, WebhookDelivery) &&
    Array.isArray(value.items) &&
    value.items.length <= 100 &&
    cursor(value.nextCursor)
  );
}

export function WebhookPage(value: unknown): boolean {
  return (
    object(value) && array(value.items, Webhook) && cursor(value.nextCursor)
  );
}

export function WebhookSecretResult(value: unknown): boolean {
  return (
    object(value) &&
    Webhook(value.endpoint) &&
    text(value.secret) &&
    value.secret.length > 0 &&
    optional(value.previousSecretExpiresAt, timestamp)
  );
}

export function DiscoveryAssignment(value: unknown): boolean {
  return (
    object(value) &&
    text(value.host) &&
    choice(value.audience, ["anonymous", "authenticated"])
  );
}

export function NestingPolicy(value: unknown): boolean {
  return (
    object(value) &&
    optional(value.enabled, flag) &&
    optional(value.maxDepth, (_maxDepthValue) =>
      number(_maxDepthValue, 1, 4, true),
    ) &&
    optional(value.allowedOrigins, (_allowedOriginsValue) =>
      array(_allowedOriginsValue, text),
    )
  );
}

export function ObservationSearchResult(value: unknown): boolean {
  return (
    object(value) &&
    Observation(value.observation) &&
    number(value.score, 0, 100, true) &&
    sourceUUID(value.source)
  );
}

export function ReferralPolicy(value: unknown): boolean {
  return (
    object(value) &&
    optional(value.enabled, flag) &&
    optional(value.maxHops, (_maxHopsValue) =>
      number(_maxHopsValue, 1, 2, true),
    ) &&
    optional(value.approvedSourceIds, (_approvedSourceIdsValue) =>
      array(_approvedSourceIdsValue, text),
    ) &&
    optional(value.anonymousDisclosureAcknowledged, flag)
  );
}

export function SourceScope(value: unknown): boolean {
  return (
    object(value) &&
    optional(value.excludedIdentifiers, (_excludedIdentifiersValue) =>
      array(_excludedIdentifiersValue, text),
    ) &&
    optional(value.identifiers, (_identifiersValue) =>
      array(_identifiersValue, text),
    ) &&
    optional(value.versions, (_versionsValue) => array(_versionsValue, text)) &&
    optional(value.kinds, (_kindsValue) =>
      array(_kindsValue, (item) =>
        choice(item, ["agent", "mcp_server", "skill", "plugin"]),
      ),
    ) &&
    optional(value.publishers, (_publishersValue) =>
      array(_publishersValue, text),
    ) &&
    optional(value.tags, (_tagsValue2) => array(_tagsValue2, text))
  );
}

// Validate fields consumed by the readable diagnostic summary and retain raw evidence.
export function FederationTestResult(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.sourceId) &&
    choice(value.status, ["succeeded", "failed"]) &&
    number(value.httpStatus, 0, 599, true) &&
    text(value.mediaType) &&
    text(value.bodyDigest) &&
    number(value.entryCount, 0, Number.MAX_SAFE_INTEGER, true) &&
    object(value.capabilities) &&
    Object.values(value.capabilities).every(flag) &&
    text(value.profileId) &&
    number(value.sourceRevision, 1, Number.MAX_SAFE_INTEGER, true) &&
    text(value.policyId) &&
    number(value.policyRevision, 0, Number.MAX_SAFE_INTEGER, true) &&
    text(value.policyDigest) &&
    /^(?:|sha256:[a-f0-9]{64})$/.test(value.policyDigest) &&
    optional(
      value.credentialRevision,
      (_credentialRevisionValue) =>
        text(_credentialRevisionValue) && !!_credentialRevisionValue,
    ) &&
    timestamp(value.testedAt) &&
    optional(value.safeDiagnostic, text)
  );
}

/** A successful test activates refreshed policy evidence and advances the source revision once. */
export function FederationTestFor(
  value: unknown,
  sourceId: string,
  requestedRevision: number,
): boolean {
  return (
    object(value) &&
    FederationTestResult(value) &&
    value.sourceId === sourceId &&
    value.sourceRevision ===
      requestedRevision + (value.status === "succeeded" ? 1 : 0)
  );
}

export function WebhookReconciliation(value: unknown): boolean {
  return (
    object(value) &&
    number(value.pendingEvents, 0, 1000, true) &&
    choice(value.pendingEventLimit, [1000]) &&
    number(value.capacityLossCount, 0, Infinity, true) &&
    number(value.deliveryLossCount, 0, Infinity, true) &&
    optional(value.lastCapacityLossAt, timestamp) &&
    optional(value.lastDeliveryLossAt, timestamp)
  );
}

/** Private library projections must bind every field used in links and commands. */
export function PersonalReference(v: unknown): boolean {
  if (!object(v)) return false;
  if (v.origin === "local")
    return (
      identifier(v.releaseId) &&
      identifier(v.versionRevisionId) &&
      Object.keys(v).every((k) =>
        ["origin", "releaseId", "versionRevisionId"].includes(k),
      )
    );
  return (
    v.origin === "foreign" &&
    choice(v.kind, ["agent", "mcp_server", "skill", "plugin"]) &&
    identifier(v.sourceId) &&
    number(v.presentationGeneration, 1) &&
    text(v.identifier) &&
    [...v.identifier].length <= 512 &&
    flag(v.versionPresent) &&
    optional(
      v.version,
      (_versionValue) =>
        text(_versionValue) && [..._versionValue].length <= 128,
    ) &&
    (v.versionPresent === true ||
      v.version === undefined ||
      v.version === "") &&
    Object.keys(v).every((k) =>
      [
        "origin",
        "kind",
        "sourceId",
        "presentationGeneration",
        "identifier",
        "versionPresent",
        "version",
      ].includes(k),
    )
  );
}

export function PersonalCriteria(v: unknown): boolean {
  return validCriteria(v);
}

export function PersonalInput(v: unknown): boolean {
  return (
    object(v) &&
    text(v.label) &&
    [...v.label].length >= 1 &&
    [...v.label].length <= 120 &&
    v.label.trim().length > 0 &&
    ![...v.label].some((c) => {
      const n = c.codePointAt(0)!;
      return n < 32 || (n >= 127 && n <= 159);
    }) &&
    ((v.type === "resource" &&
      PersonalReference(v.reference) &&
      v.search === undefined) ||
      (v.type === "search" &&
        PersonalCriteria(v.search) &&
        v.reference === undefined))
  );
}

export function PersonalItem(v: unknown): boolean {
  return (
    object(v) &&
    PersonalInput(v) &&
    identifier(v.id) &&
    number(v.revision, 1) &&
    timestamp(v.createdAt) &&
    timestamp(v.updatedAt)
  );
}

export function PersonalReceipt(v: unknown): boolean {
  return object(v) && identifier(v.id) && number(v.revision, 1);
}

export function PersonalResolution(v: unknown): boolean {
  if (!object(v)) return false;
  if (v.status === "unavailable")
    return v.reference === undefined && v.indexed === undefined;
  if (
    v.status !== "available" ||
    !PersonalReference(v.reference) ||
    !object(v.reference)
  )
    return false;
  if (v.reference.origin === "local") return v.indexed === undefined;
  const item = v.indexed;
  return (
    CatalogSearchResult(item) &&
    object(item) &&
    item.type === "indexed" &&
    item.identifier === v.reference.identifier &&
    item.kind === v.reference.kind &&
    item.versionPresent === v.reference.versionPresent &&
    (item.version ?? "") === (v.reference.version ?? "") &&
    object(item.indexed) &&
    item.indexed.presentationSourceId === v.reference.sourceId &&
    item.indexed.presentationGeneration === v.reference.presentationGeneration
  );
}

export function PersonalWorkItem(v: unknown): boolean {
  return (
    object(v) &&
    identifier(v.id) &&
    identifier(v.resourceId) &&
    identifier(v.namespaceId) &&
    choice(v.kind, ["agent", "mcp_server", "skill", "plugin"]) &&
    choice(v.type, ["resource", "version", "candidate", "submission"]) &&
    choice(v.state, [
      "empty_resource",
      "unsealed_version",
      "unsubmitted_candidate",
      "submitted",
      "changes_required",
      "approved",
    ]) &&
    [
      v.versionId,
      v.versionRevisionId,
      v.listingId,
      v.listingRevisionId,
      v.submissionId,
      v.currentCandidateRevisionId,
    ].every((x) => x === undefined || identifier(x)) &&
    [v.canonicalIdentifier, v.displayName].every(
      (x) => x === undefined || text(x),
    )
  );
}

/** Validate the exact acknowledged subject before an authoring continuation uses it. */
export function versionCommandResult(
  value: unknown,
  target: {
    tenantId: string;
    resourceId: string;
    label: string;
    versionId?: string;
  },
): boolean {
  return (
    VersionEvidenceEnvelope(value) &&
    object(value) &&
    object(value.version) &&
    object(value.revision) &&
    object(value.evidence) &&
    text(value.revision.tenantId) &&
    text(value.revision.evidenceSetId) &&
    text(value.revision.contentSetDigest) &&
    number(value.version.revision, 1, Infinity, true) &&
    value.version.candidateRevisionId === value.revision.id &&
    value.version.id === value.revision.resourceVersionId &&
    value.version.tenantId === value.revision.tenantId &&
    value.revision.tenantId === value.evidence.tenantId &&
    value.revision.evidenceSetId === value.evidence.id &&
    value.revision.contentSetDigest === value.evidence.subjectDigest &&
    value.version.tenantId === target.tenantId &&
    value.version.resourceId === target.resourceId &&
    value.version.version === target.label &&
    (!target.versionId || value.version.id === target.versionId)
  );
}
