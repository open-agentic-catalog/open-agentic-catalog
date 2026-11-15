// Consumed-field cases follow Control OpenAPI; private evidence is tested separately.
import { expect, it } from "vitest";
import * as checks from "./responses";
import { object } from "../api/values";
import { processSample } from "../../tests/unit/fixtures";

const sourceTestReceipt = {
  sourceId: "source-1",
  status: "failed",
  httpStatus: 0,
  mediaType: "",
  bodyDigest: "",
  entryCount: 0,
  capabilities: {},
  profileId: "",
  sourceRevision: 4,
  policyId: "",
  policyRevision: 0,
  policyDigest: "",
  testedAt: "2026-09-01T12:00:00Z",
};
it.each([
  ["sourceId", null],
  ["status", "complete"],
  ["httpStatus", -1],
  ["httpStatus", 1.5],
  ["mediaType", null],
  ["bodyDigest", null],
  ["entryCount", -1],
  ["capabilities", null],
  ["capabilities", { search: "yes" }],
  ["profileId", null],
  ["sourceRevision", 0],
  ["policyId", null],
  ["policyRevision", -1],
  ["policyDigest", "not-a-digest"],
  ["credentialRevision", ""],
  ["credentialRevision", null],
  ["testedAt", "invalid"],
  ["safeDiagnostic", null],
])("rejects an invalid consumed source-test field %s=%j", (field, value) => {
  expect(checks.FederationTestResult(sourceTestReceipt)).toBe(true);
  expect(
    checks.FederationTestResult({ ...sourceTestReceipt, [field]: value }),
  ).toBe(false);
});
it("binds test receipts to the exact source and the status-specific resulting revision", () => {
  expect(checks.FederationTestFor(sourceTestReceipt, "source-1", 4)).toBe(true);
  expect(
    checks.FederationTestFor(
      {
        ...sourceTestReceipt,
        status: "succeeded",
        sourceRevision: 5,
        credentialRevision: "binding-r2",
        safeDiagnostic: "Safe text",
        policyDigest: "sha256:" + "a".repeat(64),
      },
      "source-1",
      4,
    ),
  ).toBe(true);
  expect(
    checks.FederationTestFor(
      { ...sourceTestReceipt, status: "succeeded" },
      "source-1",
      4,
    ),
  ).toBe(false);
  expect(checks.FederationTestFor(sourceTestReceipt, "source-2", 4)).toBe(
    false,
  );
  expect(checks.FederationTestFor(sourceTestReceipt, "source-1", 3)).toBe(
    false,
  );
  expect(checks.FederationTestResult([])).toBe(false);
});

const sampleAuditEvent = {
  actor: "value",
  action: "value",
  id: "00000000-0000-0000-0000-000000000001",
  at: "2026-09-01T12:00:00Z",
  targetType: "value",
  targetId: "value",
  requestId: "value",
  reason: "value",
};
const sampleAuditEventPage = { items: [sampleAuditEvent], nextCursor: "value" };
const sampleCapabilities = {
  optionalCapabilities: { webhooks: "enabled" },
  operations: ["value"],
  degradations: [{ component: "value", reason: "value" }],
  governance: { legalRights: "not_assessed" },
  limits: { requestBodyBytes: 1024 },
  search: {
    indexing: {
      indexed: 0,
      failed: 0,
      state: "pending",
      jobId: "00000000-0000-0000-0000-000000000001",
    },
    degraded: false,
    profile: "value",
    semantic: "enabled",
  },
  profileBundle: { digest: "value", artifacts: 0 },
  resourceKinds: ["agent"],
  profiles: { ard: ["profile"] },
};
const sampleCatalogDisplaySummary = { displayName: "value" };

it.each(["unknown", "healthy", "degraded", undefined])(
  "accepts the bounded reported provider state %s",
  (providerState) => {
    expect(
      checks.Capabilities({
        ...sampleCapabilities,
        search: { ...sampleCapabilities.search, providerState },
      }),
    ).toBe(true);
  },
);
it.each([{}, "https://private.invalid/provider", "enabled", 1])(
  "rejects an unsafe or invented provider state %j",
  (providerState) => {
    expect(
      checks.Capabilities({
        ...sampleCapabilities,
        search: { ...sampleCapabilities.search, providerState },
      }),
    ).toBe(false);
  },
);
const sampleCatalogFacetBucket = { count: 1, value: "value" };
const sampleCatalogFacetResult = {
  otherCount: 1,
  buckets: [sampleCatalogFacetBucket],
};
const sampleCatalogExploreResponse = {
  partialIndex: false,
  facets: { kind: sampleCatalogFacetResult },
  fallbackReason: "indexing",
  matched: 0,
  tier: "core",
};
const sampleCatalogListing = {
  namespaceId: "00000000-0000-0000-0000-000000000001",
  candidateRevisionId: "00000000-0000-0000-0000-000000000001",
  resourceId: "00000000-0000-0000-0000-000000000001",
  currentReleaseId: "00000000-0000-0000-0000-000000000001",
  id: "00000000-0000-0000-0000-000000000001",
};
const sampleResourceVersion = {
  version: "value",
  candidateRevisionId: "00000000-0000-0000-0000-000000000001",
  resourceId: "00000000-0000-0000-0000-000000000001",
  sealedRevisionId: "00000000-0000-0000-0000-000000000001",
  id: "00000000-0000-0000-0000-000000000001",
};
const sampleListingRelease = {
  listingId: "00000000-0000-0000-0000-000000000001",
  activatedAt: "2026-09-01T12:00:00Z",
  id: "00000000-0000-0000-0000-000000000001",
  state: "published",
  submissionId: "00000000-0000-0000-0000-000000000001",
  versionRevisionId: "00000000-0000-0000-0000-000000000001",
  listingRevisionId: "00000000-0000-0000-0000-000000000001",
  updatedAt: "2026-09-01T12:00:00Z",
  revision: 1,
};

it("validates newly consumed release disposition fields and Unicode reason bounds", () => {
  for (const extras of [
    {
      state: "superseded",
      replacementReleaseId: "replacement-1",
      dispositionReason: "Use the replacement",
    },
    {
      state: "deprecated",
      sunsetAt: "2026-10-01T12:00:00Z",
      dispositionReason: "😀".repeat(2000),
    },
    { dispositionReason: "" },
  ])
    expect(checks.ListingRelease({ ...sampleListingRelease, ...extras })).toBe(
      true,
    );
  for (const extras of [
    { replacementReleaseId: "../other" },
    { replacementReleaseId: "https://external.test" },
    { replacementReleaseId: null },
    { dispositionReason: null },
    { dispositionReason: "😀".repeat(2001) },
    { dispositionReason: [] },
    { sunsetAt: null },
    { sunsetAt: "not-a-date" },
    { revision: 0 },
    { revision: 1.5 },
    { revision: Number.MAX_SAFE_INTEGER + 1 },
  ])
    expect(checks.ListingRelease({ ...sampleListingRelease, ...extras })).toBe(
      false,
    );
});
const sampleFinding = {
  code: "value",
  message: "value",
  severity: "warning",
  path: "/name",
};
const samplePluginInspection = {
  documents: [{ content: "value", path: "value" }],
  components: [
    { kind: "skill", name: "value", path: "value", findings: [sampleFinding] },
  ],
  files: [],
};
const sampleRepresentation = {
  contentDigest: "value",
  profileId: "00000000-0000-0000-0000-000000000001",
  validationState: "valid",
  mediaType: "value",
  role: "primary",
  size: 0,
  id: "00000000-0000-0000-0000-000000000001",
  plugin: samplePluginInspection,
  url: "value",
  artifactId: "00000000-0000-0000-0000-000000000001",
};
const sampleVersionRevision = {
  resourceVersionId: "00000000-0000-0000-0000-000000000001",
  representations: [sampleRepresentation],
  id: "00000000-0000-0000-0000-000000000001",
  revisionNumber: 1,
};
const sampleResource = {
  createdBy: "value",
  kind: "agent",
  namespaceId: "00000000-0000-0000-0000-000000000001",
  id: "00000000-0000-0000-0000-000000000001",
  canonicalIdentifier: "value",
  updatedAt: "2026-09-01T12:00:00Z",
};
const sampleRelationship = {
  type: "depends_on",
  targetVersion: "value",
  targetIdentifier: "value",
};
const sampleListingRevision = {
  listingId: "00000000-0000-0000-0000-000000000001",
  tags: ["value"],
  visibility: "private",
  displayName: "value",
  termsUrl: "value",
  revisionNumber: 1,
  description: "value",
  representativeQueries: ["value"],
  documentationUrl: "value",
  relationships: [sampleRelationship],
  versionRevisionId: "00000000-0000-0000-0000-000000000001",
  id: "00000000-0000-0000-0000-000000000001",
  supportUrl: "value",
  license: "value",
  categories: ["value"],
  publisherId: "00000000-0000-0000-0000-000000000001",
};
const sampleTrustSummary = { state: "not_assessed" };
const sampleLegalRightsSummary = { state: "not_assessed" };
const sampleEvidenceSet = {
  trust: sampleTrustSummary,
  digest: "value",
  legalRights: sampleLegalRightsSummary,
  policyVersion: "value",
  validationProfile: "catalog-validation-v7",
  id: "00000000-0000-0000-0000-000000000001",
  findings: [sampleFinding],
  state: "complete",
};
const sampleCatalogRecord = {
  evidence: sampleEvidenceSet,
  version: sampleResourceVersion,
  release: sampleListingRelease,
  versionRevision: sampleVersionRevision,
  resource: sampleResource,
  listingRevision: sampleListingRevision,
  listing: sampleCatalogListing,
};
const sampleCatalogResourceSummary = { kind: "agent" };
const sampleCatalogReleaseSummary = {
  state: "published",
  id: "00000000-0000-0000-0000-000000000001",
};
const sampleCatalogVersionSummary = { version: "value" };
const sampleCatalogSummary = {
  resource: sampleCatalogResourceSummary,
  release: sampleCatalogReleaseSummary,
  listingRevision: sampleCatalogDisplaySummary,
  version: sampleCatalogVersionSummary,
};
const sampleCatalogRecordPage = {
  items: [sampleCatalogSummary],
  nextCursor: "value",
};
it("checks optional browse presentation before allowing a Catalog summary", () => {
  const display = {
    publisherName: "Example publisher",
    namespaceName: "Development",
    license: "Apache-2.0",
  };
  expect(checks.CatalogSummary(sampleCatalogSummary)).toBe(true);
  expect(checks.CatalogSummary({ ...sampleCatalogSummary, display })).toBe(
    true,
  );
  for (const invalid of [
    null,
    { ...display, publisherName: 7 },
    { ...display, licenseOmitted: true },
  ])
    expect(
      checks.CatalogSummary({ ...sampleCatalogSummary, display: invalid }),
    ).toBe(false);
});
const sampleCatalogSearchDiagnostic = {
  message: "value",
  status: "failed",
  sourceId: "00000000-0000-0000-0000-000000000001",
};
const sampleCatalogSearchLocal = {
  listing: { namespaceId: "namespace" },
  release: sampleCatalogReleaseSummary,
  listingRevision: { visibility: "private" },
};
const sampleIndexedCatalogSearchEvidence = {
  observationGroupId: "observation-group-v1:sha256:" + "a".repeat(64),
  sourceIds: ["00000000-0000-7000-8000-000000000010"],
  sameSourceConflict: false,
  sourcesTruncated: false,
  sourceCount: 1,
  conflict: false,
  normalizedEntryDigest: "value",
};
const sampleLiveCatalogSearchEvidence = { source: "value" };
const sampleCatalogSearchResult = {
  identifier: "value",
  version: "value",
  versionPresent: true,
  tags: ["value"],
  displayName: "value",
  publisher: "value",
  local: sampleCatalogSearchLocal,
  kind: "agent",
  description: "value",
  id: "00000000-0000-0000-0000-000000000001",
  type: "local",
};
const sampleCatalogSearchResponse = {
  nextCursor: "value",
  partialIndex: false,
  diagnostics: [sampleCatalogSearchDiagnostic],
  items: [sampleCatalogSearchResult],
  durableTotal: 0,
  fallbackReason: "indexing",
  partial: false,
  liveCount: 0,
  tier: "core",
};
const sampleWebhookReconciliation = {
  pendingEvents: 2,
  pendingEventLimit: 1000,
  capacityLossCount: 3,
  deliveryLossCount: 4,
  lastCapacityLossAt: "2026-09-01T12:00:00Z",
  lastDeliveryLossAt: "2026-09-01T12:00:00Z",
};
const sampleFederationChangePreview = {
  tenantId: "tenant-1",
  sourceId: "source-1",
  intent: "update",
  configurationDigest: "sha256:" + "c".repeat(64),
  observationSetDigest: "sha256:" + "b".repeat(64),
  createdAt: "2026-09-01T11:00:00Z",
  currentMatched: 3,
  proposedMatched: 1,
  expiresAt: "2026-09-01T12:00:00Z",
  digest: "sha256:" + "a".repeat(64),
  excludedObservations: 0,
  artworkOverridesAffected: 0,
  id: "00000000-0000-0000-0000-000000000001",
  sourceRevision: 1,
  sendsQueriesUpstream: false,
};
const sampleFederationSnapshot = {
  sourceRevision: 1,
  enumerationComplete: false,
  reconcileCurrentView: false,
  added: 0,
  updated: 0,
  withdrawn: 0,
  completedAt: "2026-09-01T12:00:00Z",
  withdrawalAuthority: false,
  sourceId: "00000000-0000-0000-0000-000000000001",
  startedAt: "2026-09-01T12:00:00Z",
  id: "00000000-0000-0000-0000-000000000001",
  memberCount: 0,
  state: "running",
};
const sampleFederationSnapshotPage = {
  items: [sampleFederationSnapshot],
  nextCursor: "value",
};
const sampleReferralPolicy = {
  anonymousDisclosureAcknowledged: false,
  approvedSourceIds: ["00000000-0000-0000-0000-000000000001"],
  maxHops: 1,
  enabled: false,
};
const sampleSourceScope = {
  excludedIdentifiers: ["urn:air:example.com:agents:excluded"],
  tags: ["value"],
  publishers: ["value"],
  kinds: ["agent"],
  identifiers: ["value"],
  versions: ["value"],
};
const sampleDiscoveryAssignment = { host: "value", audience: "anonymous" };
const sampleLivePolicy = {
  federationMode: "none",
  enabled: false,
  referralOnly: false,
  anonymousDisclosureAcknowledged: false,
  queryPrivacyAcknowledged: false,
};
const sampleNestingPolicy = {
  maxDepth: 1,
  allowedOrigins: ["value"],
  enabled: false,
};
const sampleFederationSource = {
  lastSyncAt: "2026-09-01T12:00:00Z",
  lastCompleteSnapshotId: "snapshot-1",
  revision: 1,
  referrals: sampleReferralPolicy,
  displayName: "value",
  profileId: "00000000-0000-0000-0000-000000000001",
  status: "active",
  retrievalMode: "indexed",
  updateMode: "manual_review",
  scope: sampleSourceScope,
  discoveryAssignments: [sampleDiscoveryAssignment],
  networkProfile: "value",
  live: sampleLivePolicy,
  id: "00000000-0000-0000-0000-000000000001",
  nesting: sampleNestingPolicy,
  endpoint: "value",
  lastError: "value",
  credentialBinding: "value",
};
const sampleFederationSourceInspection = {
  profileId: "ard",
  entryCount: 1,
  warnings: [sampleFinding],
  resolvedUrl: "value",
  expiresAt: "2026-09-01T12:00:00Z",
  networkProfile: "value",
  id: "00000000-0000-0000-0000-000000000001",
  credentialBinding: "value",
};
const sampleFederationSourcePage = {
  items: [sampleFederationSource],
  nextCursor: "value",
};
const sampleFeedback = {
  revision: 1,
  updatedAt: "2026-09-01T12:00:00Z",
  releaseId: "00000000-0000-0000-0000-000000000001",
  response: "value",
  createdAt: "2026-09-01T12:00:00Z",
  membershipId: "00000000-0000-0000-0000-000000000001",
  versionRevisionId: "00000000-0000-0000-0000-000000000001",
  resourceId: "00000000-0000-0000-0000-000000000001",
  id: "00000000-0000-0000-0000-000000000001",
  comment: "value",
  state: "open",
  category: "problem",
};
const sampleFeedbackPage = { items: [sampleFeedback], nextCursor: "value" };
const sampleFeedbackReceipt = {
  revision: 1,
  id: "00000000-0000-0000-0000-000000000001",
};
const sampleJob = {
  attempt: 0,
  cancellationRequested: false,
  kind: "value",
  notBefore: "2026-09-01T12:00:00Z",
  createdAt: "2026-09-01T12:00:00Z",
  attemptLimit: 1,
  safeError: "value",
  progress: 0,
  failureClass: "invalid_input",
  id: "00000000-0000-0000-0000-000000000001",
  updatedAt: "2026-09-01T12:00:00Z",
  state: "queued",
  parentJobId: "00000000-0000-0000-0000-000000000001",
  priority: 0,
};
const sampleJobPage = { items: [sampleJob], nextCursor: "value" };

it("binds Job replies to the tenant and optional expected kind", () => {
  const job = { ...sampleJob, tenantId: "tenant-a" };
  expect(checks.JobFor(job, "tenant-a")).toBe(true);
  expect(checks.JobFor(job, "tenant-a", job.kind)).toBe(true);
  for (const value of [
    null,
    {},
    sampleJob,
    { ...job, tenantId: "tenant-b" },
    { ...job, progress: 101 },
  ])
    expect(checks.JobFor(value, "tenant-a")).toBe(false);
  expect(checks.JobFor(job, "tenant-a", "semantic.reindex")).toBe(false);
  expect(checks.SourceSyncAccepted({ job, existing: true })).toBe(true);
  expect(checks.SourceSyncAccepted({ job, existing: false })).toBe(true);
  for (const existing of [undefined, null, "false", 0])
    expect(checks.SourceSyncAccepted({ job, existing })).toBe(false);
});

it("accepts only succeeded matching semantic completion summaries with safe nonnegative counts", () => {
  const result = {
    type: "semantic_index",
    generationId: sampleJob.id,
    members: 0,
  };
  const job = {
    ...sampleJob,
    kind: "semantic.reindex",
    state: "succeeded",
    result,
  };
  expect(checks.Job(job)).toBe(true);
  expect(checks.Job({ ...job, result: { ...result, members: 100000 } })).toBe(
    true,
  );
  for (const members of [
    undefined,
    null,
    -1,
    1.5,
    "2",
    Infinity,
    Number.MAX_SAFE_INTEGER + 1,
  ])
    expect(checks.Job({ ...job, result: { ...result, members } })).toBe(false);
  for (const generationId of [
    undefined,
    null,
    "../unsafe",
    "another-generation",
  ])
    expect(checks.Job({ ...job, result: { ...result, generationId } })).toBe(
      false,
    );
  expect(checks.Job({ ...job, kind: "source.sync" })).toBe(false);
  for (const state of [
    "queued",
    "running",
    "waiting",
    "failed",
    "dead_letter",
    "cancelled",
  ])
    expect(checks.Job({ ...job, state })).toBe(false);
});
const sampleListingPublished = {
  listing: { ...sampleCatalogListing, tenantId: "tenant-a" },
  release: { ...sampleListingRelease, tenantId: "tenant-a" },
  priorRelease: {
    ...sampleListingRelease,
    tenantId: "tenant-a",
    id: "prior-release",
    state: "retired",
  },
};
const sampleListingRevisionCreated = {
  listing: sampleCatalogListing,
  revision: sampleListingRevision,
};
const sampleLiveSearchResponse = {
  partial: false,
  diagnostics: [{ status: "succeeded" }],
  results: [
    {
      identifier: "urn:air:peer.example:agents:helper",
      sourceRevision: 1,
      exactEntryDigest: `sha256:${"a".repeat(64)}`,
      versionPresent: false,
      score: 91,
      sourceId: "00000000-0000-7000-8000-000000000001",
      source: "https://peer.example/ard/",
      entry: {},
    },
  ],
};
const sampleMembership = {
  subject: "value",
  actorType: "human",
  realmId: "00000000-0000-0000-0000-000000000001",
  id: "00000000-0000-0000-0000-000000000001",
  state: "active",
};
const sampleMembershipLifecycleResult = {
  membership: sampleMembership,
  revokedRoleAssignments: 0,
  revokedNamespaceMemberships: 0,
};
const sampleMembershipPage = { items: [sampleMembership], nextCursor: "value" };
const sampleNamespace = {
  displayName: "value",
  slug: "value",
  description: "value",
  id: "00000000-0000-0000-0000-000000000001",
  revision: 1,
};
const sampleNamespacePage = { items: [sampleNamespace], nextCursor: "value" };
const sampleObservationCompatibility = {
  structural: "valid",
  profileId: "ard",
  support: "supported",
};
const sampleObservation = {
  kind: "agent",
  identifier: "value",
  version: "value",
  versionPresent: false,
  completeness: "complete",
  sourceKey: "value",
  sourceId: "00000000-0000-0000-0000-000000000001",
  compatibility: sampleObservationCompatibility,
  id: "00000000-0000-0000-0000-000000000001",
  freshness: "current",
};
const sampleObservationAdoption = { resource: sampleResource };
const sampleObservationGroupContribution = {
  sourceConflict: false,
  observation: sampleObservation,
  occurrenceCount: 1,
};
const sampleObservationGroupMember = {
  contributions: [sampleObservationGroupContribution],
  normalizedEntryDigest: "value",
};
const sampleObservationGroup = {
  identifier: "value",
  members: [sampleObservationGroupMember],
  contributionsTruncated: false,
  conflict: false,
};
const sampleObservationOccurrence = {
  sourceId: "source-1",
  sourceRevision: 1,
  observedAt: "2026-09-01T12:00:00Z",
  snapshotId: "00000000-0000-0000-0000-000000000001",
  retrievalDigest: "value",
  entryLocation: "value",
  evidenceComplete: true,
};
const sampleObservationOccurrencePage = {
  items: [sampleObservationOccurrence],
  nextCursor: "value",
};
const sampleObservationPage = {
  items: [sampleObservation],
  nextCursor: "value",
};
const sampleObservationSearchResult = {
  observation: sampleObservation,
  score: 91,
  source: "00000000-0000-7000-8000-000000000001",
};
const sampleObservationSearchPage = {
  items: [sampleObservationSearchResult],
  nextCursor: "value",
};
const samplePublicationExclusion = {
  profiles: ["ard-profile"],
  reasons: ["value"],
  identifier: "value",
  resourceId: "00000000-0000-0000-0000-000000000001",
};
const samplePublicationSnapshot = {
  ardProfile: "ard-profile",
  aiCatalogProfile: "ai-catalog-profile",
  ard: {
    entries: [
      {
        identifier: "urn:air:example.com:registry:catalog",
        type: "application/ai-registry+json",
        version: "1.0",
        metadata: { catalogReleaseId: "registry:self" },
      },
    ],
  },
  aiCatalog: {
    entries: [
      {
        identifier: "urn:air:example.com:ns:agent",
        type: "application/a2a+json",
        version: "1.0",
      },
    ],
  },
  exclusions: [samplePublicationExclusion],
  audience: "anonymous",
  generation: 1,
  id: "00000000-0000-0000-0000-000000000001",
  host: "value",
  state: "candidate",
};
const samplePublicationSnapshotSummary = {
  tenantId: "tenant-a",
  host: "catalog.example.com",
  ardProfile: "ard-test",
  aiCatalogProfile: "ai-test",
  etag: '"snapshot"',
  createdAt: "2026-10-20T00:00:00Z",
  activatedAt: "2026-10-20T00:01:00Z",
  generation: 1,
  state: "candidate",
  id: "00000000-0000-0000-0000-000000000001",
  audience: "anonymous",
};
const samplePublicationSnapshotPage = {
  items: [samplePublicationSnapshotSummary],
  nextCursor: "value",
};
const samplePublisher = {
  approvedPublicationDomains: ["value"],
  displayName: "value",
  domains: ["value"],
  id: "00000000-0000-0000-0000-000000000001",
  revision: 1,
};
const samplePublisherPage = { items: [samplePublisher], nextCursor: "value" };
const samplePublicationDomainRevocation = {
  publisher: samplePublisher,
  affectedReleaseIds: ["00000000-0000-0000-0000-000000000001"],
  withdrawnHosts: ["value"],
};
const sampleResourcePage = { items: [sampleResource], nextCursor: "value" };
const sampleReviewSubmission = {
  listingId: "00000000-0000-0000-0000-000000000001",
  listingRevisionId: "00000000-0000-0000-0000-000000000001",
  evidenceSetId: "00000000-0000-0000-0000-000000000001",
  policyVersion: "value",
  versionRevisionId: "00000000-0000-0000-0000-000000000001",
  evidenceDigest: "value",
  submittedBy: "value",
  id: "00000000-0000-0000-0000-000000000001",
  state: "submitted",
  submittedAt: "2026-09-01T12:00:00Z",
};
const sampleReviewDecision = { submission: sampleReviewSubmission };
const sampleRevisionEvidenceEnvelope = {
  namespaceId: "namespace-1",
  evidence: sampleEvidenceSet,
  revision: sampleVersionRevision,
};
const sampleRevisionSummary = {
  revisionNumber: 1,
  createdBy: "value",
  id: "00000000-0000-0000-0000-000000000001",
};
const sampleRoleAssignment = {
  revision: 1,
  subject: "value",
  namespaceId: "00000000-0000-0000-0000-000000000001",
  membershipId: "00000000-0000-0000-0000-000000000001",
  role: "reader",
  id: "00000000-0000-0000-0000-000000000001",
  state: "active",
};
const sampleServiceVersion = {
  sourceState: "modified",
  ardProfile: "value",
  version: "value",
  aiCatalogProfile: "value",
  commit: "value",
};
const sampleSourceSyncAccepted = { job: sampleJob, existing: false };
const sampleVersionEvidenceEnvelope = {
  version: sampleResourceVersion,
  revision: sampleVersionRevision,
  evidence: sampleEvidenceSet,
};
const sampleWebhook = {
  status: "active",
  eventTypes: ["*"],
  name: "value",
  id: "00000000-0000-0000-0000-000000000001",
  url: "value",
  revision: 1,
  secretVersion: 1,
  createdAt: "2026-09-01T12:00:00Z",
  updatedAt: "2026-09-01T12:00:00Z",
};
const sampleWebhookDelivery = {
  eventId: "event-1",
  updatedAt: "2026-09-01T12:00:00Z",
  responseStatus: 503,
  safeError: "Receiver unavailable",
  nextAttemptAt: "2026-09-01T12:05:00Z",
  attempt: 0,
  eventType: "*",
  state: "pending",
  id: "00000000-0000-0000-0000-000000000001",
};
const sampleWebhookDeliveryPage = {
  items: [sampleWebhookDelivery],
  nextCursor: "value",
};
const sampleWebhookPage = { items: [sampleWebhook], nextCursor: "value" };
const sampleWebhookSecretResult = {
  secret: "value",
  endpoint: sampleWebhook,
  previousSecretExpiresAt: "2026-09-01T12:00:00Z",
};

const cases = [
  {
    name: "AuditEvent",
    check: checks.AuditEvent,
    value: sampleAuditEvent,
    invalidFields: [
      "actor",
      "action",
      "id",
      "at",
      "targetType",
      "targetId",
      "reason",
      "requestId",
    ],
    optionalFields: ["reason", "requestId"],
  },
  {
    name: "AuditEventPage",
    check: checks.AuditEventPage,
    value: sampleAuditEventPage,
    invalidFields: ["items", "items.0", "nextCursor"],
    optionalFields: ["nextCursor"],
  },
  {
    name: "Capabilities",
    check: checks.Capabilities,
    value: sampleCapabilities,
    invalidFields: [
      "optionalCapabilities",
      "optionalCapabilities.webhooks",
      "operations",
      "operations.0",
      "degradations",
      "degradations.0",
      "degradations.0.component",
      "degradations.0.reason",
      "governance",
      "limits",
      "search",
      "search.indexing",
      "search.indexing.indexed",
      "search.indexing.failed",
      "search.indexing.state",
      "search.indexing.jobId",
      "search.degraded",
      "search.profile",
      "search.semantic",
      "profileBundle",
      "profileBundle.digest",
      "profileBundle.artifacts",
      "resourceKinds",
      "resourceKinds.0",
      "profiles",
    ],
    optionalFields: ["search.indexing.jobId", "search.indexing"],
  },
  {
    name: "CatalogDisplaySummary",
    check: checks.CatalogDisplaySummary,
    value: sampleCatalogDisplaySummary,
    invalidFields: ["displayName"],
    optionalFields: [],
  },
  {
    name: "CatalogExploreResponse",
    check: checks.CatalogExploreResponse,
    value: sampleCatalogExploreResponse,
    invalidFields: [
      "partialIndex",
      "facets",
      "fallbackReason",
      "matched",
      "tier",
    ],
    optionalFields: ["partialIndex", "fallbackReason", "tier"],
  },
  {
    name: "CatalogFacetBucket",
    check: checks.CatalogFacetBucket,
    value: sampleCatalogFacetBucket,
    invalidFields: ["count", "value"],
    optionalFields: [],
  },
  {
    name: "CatalogFacetResult",
    check: checks.CatalogFacetResult,
    value: sampleCatalogFacetResult,
    invalidFields: ["otherCount", "buckets", "buckets.0"],
    optionalFields: ["otherCount"],
  },
  {
    name: "CatalogListing",
    check: checks.CatalogListing,
    value: sampleCatalogListing,
    invalidFields: [
      "namespaceId",
      "candidateRevisionId",
      "resourceId",
      "currentReleaseId",
      "id",
    ],
    optionalFields: ["candidateRevisionId", "currentReleaseId"],
  },
  {
    name: "CatalogRecord",
    check: checks.CatalogRecord,
    value: sampleCatalogRecord,
    invalidFields: [
      "version",
      "release",
      "versionRevision",
      "resource",
      "listingRevision",
      "listing",
      "evidence",
    ],
    optionalFields: [],
  },
  {
    name: "CatalogRecordPage",
    check: checks.CatalogRecordPage,
    value: sampleCatalogRecordPage,
    invalidFields: ["items", "items.0", "nextCursor"],
    optionalFields: ["nextCursor"],
  },
  {
    name: "CatalogReleaseSummary",
    check: checks.CatalogReleaseSummary,
    value: sampleCatalogReleaseSummary,
    invalidFields: ["state", "id"],
    optionalFields: [],
  },
  {
    name: "CatalogResourceSummary",
    check: checks.CatalogResourceSummary,
    value: sampleCatalogResourceSummary,
    invalidFields: ["kind"],
    optionalFields: [],
  },
  {
    name: "CatalogSearchDiagnostic",
    check: checks.CatalogSearchDiagnostic,
    value: sampleCatalogSearchDiagnostic,
    invalidFields: ["message", "status", "sourceId"],
    optionalFields: ["message", "sourceId"],
  },
  {
    name: "CatalogSearchLocal",
    check: checks.CatalogSearchLocal,
    value: sampleCatalogSearchLocal,
    invalidFields: ["release", "listingRevision", "listingRevision.visibility"],
    optionalFields: [],
  },
  {
    name: "CatalogSearchResponse",
    check: checks.CatalogSearchResponse,
    value: sampleCatalogSearchResponse,
    invalidFields: [
      "nextCursor",
      "partialIndex",
      "diagnostics",
      "diagnostics.0",
      "items",
      "items.0",
      "durableTotal",
      "fallbackReason",
      "partial",
      "liveCount",
      "tier",
    ],
    optionalFields: [
      "nextCursor",
      "partialIndex",
      "diagnostics",
      "fallbackReason",
    ],
  },
  {
    name: "CatalogSearchResult",
    check: checks.CatalogSearchResult,
    value: sampleCatalogSearchResult,
    invalidFields: [
      "identifier",
      "version",
      "versionPresent",
      "tags",
      "tags.0",
      "displayName",
      "publisher",
      "local",
      "kind",
      "description",
      "id",
      "type",
    ],
    optionalFields: ["publisher", "description"],
  },
  {
    name: "CatalogSummary",
    check: checks.CatalogSummary,
    value: sampleCatalogSummary,
    invalidFields: ["resource", "release", "listingRevision", "version"],
    optionalFields: [],
  },
  {
    name: "CatalogVersionSummary",
    check: checks.CatalogVersionSummary,
    value: sampleCatalogVersionSummary,
    invalidFields: ["version"],
    optionalFields: [],
  },
  {
    name: "EvidenceSet",
    check: checks.EvidenceSet,
    value: sampleEvidenceSet,
    invalidFields: [
      "trust",
      "digest",
      "legalRights",
      "policyVersion",
      "id",
      "findings",
      "findings.0",
      "state",
    ],
    optionalFields: ["findings"],
  },
  {
    name: "WebhookReconciliation",
    check: checks.WebhookReconciliation,
    value: sampleWebhookReconciliation,
    invalidFields: [
      "pendingEvents",
      "pendingEventLimit",
      "capacityLossCount",
      "deliveryLossCount",
      "lastCapacityLossAt",
      "lastDeliveryLossAt",
    ],
    optionalFields: ["lastCapacityLossAt", "lastDeliveryLossAt"],
  },
  {
    name: "FederationChangePreview",
    check: checks.FederationChangePreview,
    value: sampleFederationChangePreview,
    invalidFields: [
      "tenantId",
      "sourceId",
      "intent",
      "configurationDigest",
      "observationSetDigest",
      "createdAt",
      "currentMatched",
      "proposedMatched",
      "expiresAt",
      "digest",
      "excludedObservations",
      "id",
      "sourceRevision",
      "sendsQueriesUpstream",
    ],
    optionalFields: [],
  },
  {
    name: "FederationSnapshot",
    check: checks.FederationSnapshot,
    value: sampleFederationSnapshot,
    invalidFields: [
      "enumerationComplete",
      "sourceRevision",
      "reconcileCurrentView",
      "added",
      "updated",
      "withdrawn",
      "completedAt",
      "withdrawalAuthority",
      "sourceId",
      "startedAt",
      "id",
      "memberCount",
      "state",
    ],
    optionalFields: ["completedAt", "reconcileCurrentView"],
  },
  {
    name: "FederationSnapshotPage",
    check: checks.FederationSnapshotPage,
    value: sampleFederationSnapshotPage,
    invalidFields: ["items", "items.0", "nextCursor"],
    optionalFields: ["nextCursor"],
  },
  {
    name: "FederationSource",
    check: checks.FederationSource,
    value: sampleFederationSource,
    invalidFields: [
      "lastSyncAt",
      "lastCompleteSnapshotId",
      "revision",
      "referrals",
      "displayName",
      "profileId",
      "status",
      "retrievalMode",
      "updateMode",
      "scope",
      "discoveryAssignments",
      "discoveryAssignments.0",
      "networkProfile",
      "live",
      "id",
      "nesting",
      "endpoint",
      "lastError",
      "credentialBinding",
    ],
    optionalFields: [
      "lastSyncAt",
      "lastCompleteSnapshotId",
      "discoveryAssignments",
      "networkProfile",
      "lastError",
      "credentialBinding",
    ],
  },
  {
    name: "FederationSourceInspection",
    check: checks.FederationSourceInspection,
    value: sampleFederationSourceInspection,
    invalidFields: [
      "profileId",
      "entryCount",
      "warnings",
      "resolvedUrl",
      "expiresAt",
      "networkProfile",
      "id",
      "credentialBinding",
    ],
    optionalFields: ["warnings", "networkProfile", "credentialBinding"],
  },
  {
    name: "FederationSourcePage",
    check: checks.FederationSourcePage,
    value: sampleFederationSourcePage,
    invalidFields: ["items", "items.0", "nextCursor"],
    optionalFields: ["nextCursor"],
  },
  {
    name: "Feedback",
    check: checks.Feedback,
    value: sampleFeedback,
    invalidFields: [
      "updatedAt",
      "revision",
      "releaseId",
      "response",
      "createdAt",
      "membershipId",
      "versionRevisionId",
      "resourceId",
      "id",
      "comment",
      "state",
      "category",
    ],
    optionalFields: ["releaseId", "response", "versionRevisionId", "comment"],
  },
  {
    name: "FeedbackPage",
    check: checks.FeedbackPage,
    value: sampleFeedbackPage,
    invalidFields: ["items", "items.0", "nextCursor"],
    optionalFields: ["nextCursor"],
  },
  {
    name: "FeedbackReceipt",
    check: checks.FeedbackReceipt,
    value: sampleFeedbackReceipt,
    invalidFields: ["revision", "id"],
    optionalFields: [],
  },
  {
    name: "Finding",
    check: checks.Finding,
    value: sampleFinding,
    invalidFields: ["code", "message", "severity", "path"],
    optionalFields: ["path"],
  },
  {
    name: "IndexedCatalogSearchEvidence",
    check: checks.IndexedCatalogSearchEvidence,
    value: sampleIndexedCatalogSearchEvidence,
    invalidFields: [
      "sourcesTruncated",
      "sourceCount",
      "conflict",
      "normalizedEntryDigest",
      "observationGroupId",
      "sourceIds",
      "sourceIds.0",
      "sameSourceConflict",
    ],
    optionalFields: [],
  },
  {
    name: "Job",
    check: checks.Job,
    value: sampleJob,
    invalidFields: [
      "attempt",
      "cancellationRequested",
      "kind",
      "notBefore",
      "createdAt",
      "attemptLimit",
      "safeError",
      "progress",
      "failureClass",
      "id",
      "updatedAt",
      "state",
      "parentJobId",
      "priority",
    ],
    optionalFields: ["safeError", "failureClass", "parentJobId"],
  },
  {
    name: "JobPage",
    check: checks.JobPage,
    value: sampleJobPage,
    invalidFields: ["items", "items.0", "nextCursor"],
    optionalFields: ["nextCursor"],
  },
  {
    name: "LegalRightsSummary",
    check: checks.LegalRightsSummary,
    value: sampleLegalRightsSummary,
    invalidFields: ["state"],
    optionalFields: [],
  },
  {
    name: "ListingPublished",
    check: checks.ListingPublished,
    value: sampleListingPublished,
    invalidFields: [
      "listing",
      "release",
      "listing.tenantId",
      "release.tenantId",
      "priorRelease.id",
      "priorRelease.tenantId",
    ],
    optionalFields: [],
  },
  {
    name: "ListingRelease",
    check: checks.ListingRelease,
    value: sampleListingRelease,
    invalidFields: [
      "listingId",
      "activatedAt",
      "id",
      "state",
      "submissionId",
      "versionRevisionId",
      "listingRevisionId",
      "updatedAt",
      "revision",
    ],
    optionalFields: ["replacementReleaseId", "dispositionReason", "sunsetAt"],
  },
  {
    name: "ListingRevision",
    check: checks.ListingRevision,
    value: sampleListingRevision,
    invalidFields: [
      "listingId",
      "tags",
      "tags.0",
      "visibility",
      "displayName",
      "termsUrl",
      "revisionNumber",
      "description",
      "representativeQueries",
      "representativeQueries.0",
      "documentationUrl",
      "relationships",
      "relationships.0",
      "versionRevisionId",
      "id",
      "supportUrl",
      "license",
      "categories",
      "categories.0",
      "publisherId",
    ],
    optionalFields: [
      "tags",
      "termsUrl",
      "representativeQueries",
      "documentationUrl",
      "relationships",
      "supportUrl",
      "license",
      "categories",
    ],
  },
  {
    name: "ListingRevisionCreated",
    check: checks.ListingRevisionCreated,
    value: sampleListingRevisionCreated,
    invalidFields: ["revision"],
    optionalFields: [],
  },
  {
    name: "LiveCatalogSearchEvidence",
    check: checks.LiveCatalogSearchEvidence,
    value: sampleLiveCatalogSearchEvidence,
    invalidFields: ["source"],
    optionalFields: [],
  },
  {
    name: "LivePolicy",
    check: checks.LivePolicy,
    value: sampleLivePolicy,
    invalidFields: [
      "federationMode",
      "enabled",
      "referralOnly",
      "anonymousDisclosureAcknowledged",
      "queryPrivacyAcknowledged",
    ],
    optionalFields: [
      "federationMode",
      "enabled",
      "referralOnly",
      "anonymousDisclosureAcknowledged",
      "queryPrivacyAcknowledged",
    ],
  },
  {
    name: "LiveSearchResponse",
    check: checks.LiveSearchResponse,
    value: sampleLiveSearchResponse,
    invalidFields: [
      "partial",
      "diagnostics",
      "diagnostics.0",
      "results",
      "results.0",
      "results.0.identifier",
      "results.0.versionPresent",
      "results.0.score",
      "results.0.sourceId",
      "results.0.source",
      "results.0.entry",
      "diagnostics.0.status",
    ],
    optionalFields: ["diagnostics"],
  },
  {
    name: "Membership",
    check: checks.Membership,
    value: sampleMembership,
    invalidFields: ["subject", "actorType", "realmId", "id", "state"],
    optionalFields: [],
  },
  {
    name: "MembershipLifecycleResult",
    check: checks.MembershipLifecycleResult,
    value: sampleMembershipLifecycleResult,
    invalidFields: ["membership"],
    optionalFields: [],
  },
  {
    name: "MembershipPage",
    check: checks.MembershipPage,
    value: sampleMembershipPage,
    invalidFields: ["items", "items.0", "nextCursor"],
    optionalFields: ["nextCursor"],
  },
  {
    name: "Namespace",
    check: checks.Namespace,
    value: sampleNamespace,
    invalidFields: ["displayName", "slug", "description", "id", "revision"],
    optionalFields: ["description"],
  },
  {
    name: "NamespacePage",
    check: checks.NamespacePage,
    value: sampleNamespacePage,
    invalidFields: ["items", "items.0", "nextCursor"],
    optionalFields: ["nextCursor"],
  },
  {
    name: "Observation",
    check: checks.Observation,
    value: sampleObservation,
    invalidFields: [
      "identifier",
      "version",
      "versionPresent",
      "completeness",
      "sourceKey",
      "sourceId",
      "compatibility",
      "id",
      "freshness",
      "kind",
    ],
    optionalFields: ["identifier", "version", "kind"],
  },
  {
    name: "ObservationAdoption",
    check: checks.ObservationAdoption,
    value: sampleObservationAdoption,
    invalidFields: ["resource"],
    optionalFields: [],
  },
  {
    name: "ObservationCompatibility",
    check: checks.ObservationCompatibility,
    value: sampleObservationCompatibility,
    invalidFields: ["support", "structural", "profileId"],
    optionalFields: [],
  },
  {
    name: "ObservationGroup",
    check: checks.ObservationGroup,
    value: sampleObservationGroup,
    invalidFields: [
      "identifier",
      "members",
      "members.0",
      "contributionsTruncated",
      "conflict",
    ],
    optionalFields: [],
  },
  {
    name: "ObservationGroupContribution",
    check: checks.ObservationGroupContribution,
    value: sampleObservationGroupContribution,
    invalidFields: ["sourceConflict", "observation", "occurrenceCount"],
    optionalFields: [],
  },
  {
    name: "ObservationGroupMember",
    check: checks.ObservationGroupMember,
    value: sampleObservationGroupMember,
    invalidFields: [
      "contributions",
      "contributions.0",
      "normalizedEntryDigest",
    ],
    optionalFields: [],
  },
  {
    name: "ObservationOccurrence",
    check: checks.ObservationOccurrence,
    value: sampleObservationOccurrence,
    invalidFields: [
      "sourceId",
      "sourceRevision",
      "observedAt",
      "snapshotId",
      "retrievalDigest",
      "entryLocation",
      "evidenceComplete",
    ],
    optionalFields: [],
  },
  {
    name: "ObservationOccurrencePage",
    check: checks.ObservationOccurrencePage,
    value: sampleObservationOccurrencePage,
    invalidFields: ["items", "items.0", "nextCursor"],
    optionalFields: ["nextCursor"],
  },
  {
    name: "ObservationPage",
    check: checks.ObservationPage,
    value: sampleObservationPage,
    invalidFields: ["items", "items.0", "nextCursor"],
    optionalFields: ["nextCursor"],
  },
  {
    name: "ObservationSearchPage",
    check: checks.ObservationSearchPage,
    value: sampleObservationSearchPage,
    invalidFields: ["items", "items.0", "nextCursor"],
    optionalFields: ["nextCursor"],
  },
  {
    name: "PluginInspection",
    check: checks.PluginInspection,
    value: samplePluginInspection,
    invalidFields: [
      "documents",
      "documents.0",
      "documents.0.path",
      "components",
      "components.0",
      "components.0.kind",
      "components.0.name",
      "components.0.path",
      "components.0.findings",
      "components.0.findings.0",
      "files",
    ],
    optionalFields: ["components.0.findings", "components"],
  },
  {
    name: "PublicationDomainRevocation",
    check: checks.PublicationDomainRevocation,
    value: samplePublicationDomainRevocation,
    invalidFields: [
      "publisher",
      "publisher.revision",
      "affectedReleaseIds",
      "affectedReleaseIds.0",
      "withdrawnHosts",
      "withdrawnHosts.0",
    ],
    optionalFields: [],
  },
  {
    name: "PublicationExclusion",
    check: checks.PublicationExclusion,
    value: samplePublicationExclusion,
    invalidFields: [
      "profiles",
      "profiles.0",
      "reasons",
      "reasons.0",
      "identifier",
      "resourceId",
    ],
    optionalFields: [],
  },
  {
    name: "PublicationSnapshot",
    check: checks.PublicationSnapshot,
    value: samplePublicationSnapshot,
    invalidFields: [
      "ardProfile",
      "aiCatalogProfile",
      "ard",
      "ard.entries",
      "ard.entries.0",
      "ard.entries.0.identifier",
      "ard.entries.0.type",
      "ard.entries.0.version",
      "ard.entries.0.metadata",
      "ard.entries.0.metadata.catalogReleaseId",
      "aiCatalog",
      "aiCatalog.entries",
      "aiCatalog.entries.0",
      "exclusions",
      "exclusions.0",
      "audience",
      "generation",
      "id",
      "host",
      "state",
    ],
    optionalFields: [],
  },
  {
    name: "PublicationHostInventory",
    check: checks.PublicationHostInventory,
    value: { items: [{ host: "catalog.example.com", mode: "anonymous" }] },
    invalidFields: ["items", "items.0", "items.0.host", "items.0.mode"],
    optionalFields: [],
  },
  {
    name: "PublicationSnapshotPage",
    check: checks.PublicationSnapshotPage,
    value: samplePublicationSnapshotPage,
    invalidFields: ["items", "items.0", "nextCursor"],
    optionalFields: ["nextCursor"],
  },
  {
    name: "PublicationSnapshotSummary",
    check: checks.PublicationSnapshotSummary,
    value: samplePublicationSnapshotSummary,
    invalidFields: [
      "generation",
      "state",
      "id",
      "audience",
      "tenantId",
      "host",
      "ardProfile",
      "aiCatalogProfile",
      "etag",
      "createdAt",
      "activatedAt",
    ],
    optionalFields: ["activatedAt"],
  },
  {
    name: "Publisher",
    check: checks.Publisher,
    value: samplePublisher,
    invalidFields: [
      "approvedPublicationDomains",
      "approvedPublicationDomains.0",
      "displayName",
      "domains",
      "domains.0",
      "id",
      "revision",
    ],
    optionalFields: [],
  },
  {
    name: "PublisherPage",
    check: checks.PublisherPage,
    value: samplePublisherPage,
    invalidFields: ["items", "items.0", "nextCursor"],
    optionalFields: ["nextCursor"],
  },
  {
    name: "Relationship",
    check: checks.Relationship,
    value: sampleRelationship,
    invalidFields: ["type", "targetVersion", "targetIdentifier"],
    optionalFields: ["targetVersion"],
  },
  {
    name: "Representation",
    check: checks.Representation,
    value: sampleRepresentation,
    invalidFields: [
      "contentDigest",
      "profileId",
      "validationState",
      "mediaType",
      "role",
      "size",
      "id",
      "plugin",
      "url",
      "artifactId",
    ],
    optionalFields: ["plugin", "url", "artifactId"],
  },
  {
    name: "Resource",
    check: checks.Resource,
    value: sampleResource,
    invalidFields: [
      "createdBy",
      "kind",
      "namespaceId",
      "id",
      "canonicalIdentifier",
      "updatedAt",
    ],
    optionalFields: [],
  },
  {
    name: "ResourcePage",
    check: checks.ResourcePage,
    value: sampleResourcePage,
    invalidFields: ["items", "items.0", "nextCursor"],
    optionalFields: ["nextCursor"],
  },
  {
    name: "ResourceVersion",
    check: checks.ResourceVersion,
    value: sampleResourceVersion,
    invalidFields: [
      "version",
      "candidateRevisionId",
      "resourceId",
      "sealedRevisionId",
      "id",
    ],
    optionalFields: ["candidateRevisionId", "sealedRevisionId"],
  },
  {
    name: "ReviewDecision",
    check: checks.ReviewDecision,
    value: sampleReviewDecision,
    invalidFields: ["submission"],
    optionalFields: [],
  },
  {
    name: "ReviewSubmission",
    check: checks.ReviewSubmission,
    value: sampleReviewSubmission,
    invalidFields: [
      "listingId",
      "listingRevisionId",
      "evidenceSetId",
      "policyVersion",
      "versionRevisionId",
      "evidenceDigest",
      "submittedBy",
      "id",
      "state",
      "submittedAt",
    ],
    optionalFields: [],
  },
  {
    name: "RevisionEvidenceEnvelope",
    check: checks.RevisionEvidenceEnvelope,
    value: sampleRevisionEvidenceEnvelope,
    invalidFields: ["namespaceId", "evidence", "revision"],
    optionalFields: [],
  },
  {
    name: "RevisionSummary",
    check: checks.RevisionSummary,
    value: sampleRevisionSummary,
    invalidFields: ["revisionNumber", "createdBy", "id"],
    optionalFields: [],
  },
  {
    name: "RoleAssignment",
    check: checks.RoleAssignment,
    value: sampleRoleAssignment,
    invalidFields: [
      "revision",
      "subject",
      "namespaceId",
      "membershipId",
      "role",
      "id",
      "state",
    ],
    optionalFields: ["namespaceId"],
  },
  {
    name: "ServiceInformation",
    check: checks.ServiceInformation,
    value: {
      ...sampleServiceVersion,
      ...processSample,
      service: "open-agentic-catalog",
      role: "api",
    },
    invalidFields: [
      "service",
      "role",
      "ardProfile",
      "aiCatalogProfile",
      "version",
      "commit",
      "sourceState",
      "instanceId",
      "startedAt",
      "observedAt",
      "uptimeSeconds",
    ],
    optionalFields: [],
  },
  {
    name: "ServiceVersion",
    check: checks.ServiceVersion,
    value: sampleServiceVersion,
    invalidFields: [
      "ardProfile",
      "version",
      "aiCatalogProfile",
      "commit",
      "sourceState",
    ],
    optionalFields: [],
  },
  {
    name: "SourceSyncAccepted",
    check: checks.SourceSyncAccepted,
    value: sampleSourceSyncAccepted,
    invalidFields: ["job", "existing"],
    optionalFields: [],
  },
  {
    name: "TrustSummary",
    check: checks.TrustSummary,
    value: sampleTrustSummary,
    invalidFields: ["state"],
    optionalFields: [],
  },
  {
    name: "VersionEvidenceEnvelope",
    check: checks.VersionEvidenceEnvelope,
    value: sampleVersionEvidenceEnvelope,
    invalidFields: ["version", "evidence"],
    optionalFields: [],
  },
  {
    name: "VersionRevision",
    check: checks.VersionRevision,
    value: sampleVersionRevision,
    invalidFields: [
      "resourceVersionId",
      "representations",
      "representations.0",
      "id",
      "revisionNumber",
    ],
    optionalFields: [],
  },
  {
    name: "Webhook",
    check: checks.Webhook,
    value: sampleWebhook,
    invalidFields: [
      "status",
      "eventTypes",
      "eventTypes.0",
      "name",
      "id",
      "url",
      "revision",
      "secretVersion",
      "createdAt",
      "updatedAt",
    ],
    optionalFields: [],
  },
  {
    name: "WebhookDelivery",
    check: checks.WebhookDelivery,
    value: sampleWebhookDelivery,
    invalidFields: [
      "attempt",
      "eventType",
      "state",
      "id",
      "eventId",
      "updatedAt",
      "responseStatus",
      "safeError",
      "nextAttemptAt",
    ],
    optionalFields: ["responseStatus", "safeError", "nextAttemptAt"],
  },
  {
    name: "WebhookDeliveryPage",
    check: checks.WebhookDeliveryPage,
    value: sampleWebhookDeliveryPage,
    invalidFields: ["items", "items.0", "nextCursor"],
    optionalFields: ["nextCursor"],
  },
  {
    name: "WebhookPage",
    check: checks.WebhookPage,
    value: sampleWebhookPage,
    invalidFields: ["items", "items.0", "nextCursor"],
    optionalFields: ["nextCursor"],
  },
  {
    name: "WebhookSecretResult",
    check: checks.WebhookSecretResult,
    value: sampleWebhookSecretResult,
    invalidFields: ["secret", "endpoint", "previousSecretExpiresAt"],
    optionalFields: ["previousSecretExpiresAt"],
  },
  {
    name: "DiscoveryAssignment",
    check: checks.DiscoveryAssignment,
    value: sampleDiscoveryAssignment,
    invalidFields: ["host", "audience"],
    optionalFields: [],
  },
  {
    name: "NestingPolicy",
    check: checks.NestingPolicy,
    value: sampleNestingPolicy,
    invalidFields: [
      "maxDepth",
      "allowedOrigins",
      "allowedOrigins.0",
      "enabled",
    ],
    optionalFields: ["maxDepth", "allowedOrigins", "enabled"],
  },
  {
    name: "ObservationSearchResult",
    check: checks.ObservationSearchResult,
    value: sampleObservationSearchResult,
    invalidFields: ["observation", "score", "source"],
    optionalFields: [],
  },
  {
    name: "ReferralPolicy",
    check: checks.ReferralPolicy,
    value: sampleReferralPolicy,
    invalidFields: [
      "anonymousDisclosureAcknowledged",
      "approvedSourceIds",
      "approvedSourceIds.0",
      "maxHops",
      "enabled",
    ],
    optionalFields: [
      "anonymousDisclosureAcknowledged",
      "approvedSourceIds",
      "maxHops",
      "enabled",
    ],
  },
  {
    name: "SourceScope",
    check: checks.SourceScope,
    value: sampleSourceScope,
    invalidFields: [
      "tags",
      "tags.0",
      "publishers",
      "publishers.0",
      "kinds",
      "kinds.0",
      "identifiers",
      "identifiers.0",
      "versions",
      "versions.0",
    ],
    optionalFields: ["tags", "publishers", "kinds", "identifiers", "versions"],
  },
];

it("accepts API and combined-process information without confusing the UI host or worker", () => {
  const valid = {
    ...sampleServiceVersion,
    ...processSample,
    service: "open-agentic-catalog",
    role: "api",
  };
  for (const role of ["api", "all"])
    expect(checks.ServiceInformation({ ...valid, role })).toBe(true);
  for (const patch of [
    { service: "other-service" },
    { role: "ui" },
    { role: "worker" },
    { role: "unknown" },
  ])
    expect(checks.ServiceInformation({ ...valid, ...patch })).toBe(false);
  // This response consumes every field in the closed service-information contract.
  for (const field of Object.keys(valid))
    expect(checks.ServiceInformation(changed(valid, field, true))).toBe(false);
});

it.each(["ardProfile", "aiCatalogProfile"])(
  "rejects an empty %s instead of displaying missing standards information",
  (field) => {
    const valid = {
      ...sampleServiceVersion,
      ...processSample,
      service: "open-agentic-catalog",
      role: "api",
    };
    // Both contracts require at least one Unicode code point in each profile name.
    for (const check of [checks.ServiceVersion, checks.ServiceInformation]) {
      expect(check(valid)).toBe(true);
      expect(check({ ...valid, [field]: "" })).toBe(false);
    }
  },
);

/** Corrupt only the selected field so failures prove that boundary rather than a sibling guard. */
function changed(value: unknown, path: string, remove = false): unknown {
  const copy: unknown = structuredClone(value);
  const keys = path.split(".");
  let current = copy;
  for (const key of keys.slice(0, -1)) {
    if (Array.isArray(current)) current = current[Number(key)];
    else if (object(current)) current = current[key];
    else throw new Error("Invalid test path");
  }
  if (!object(current) && !Array.isArray(current))
    throw new Error("Invalid test parent");
  const key = keys[keys.length - 1]!;
  if (remove) Reflect.deleteProperty(current, key);
  else Reflect.set(current, key, null);
  return copy;
}

for (const { name, check, value, invalidFields, optionalFields } of cases) {
  it(`${name} accepts its consumed projection and rejects non-object control replies`, () => {
    expect(check(value)).toBe(true);
    for (const invalid of [null, [], false, 0, "secret sentinel"])
      expect(check(invalid)).toBe(false);
  });
  it.each(invalidFields)(
    `${name} rejects malformed %s without changing the original`,
    (path) => {
      expect(check(changed(value, path))).toBe(false);
      expect(check(value)).toBe(true);
    },
  );
  it.each(optionalFields)(`${name} permits omitted %s`, (path) => {
    expect(check(changed(value, path, true))).toBe(true);
  });
}

it("keeps Search variants mutually exclusive and respects absent versus empty versions", () => {
  const common = { ...sampleCatalogSearchResult, local: undefined };
  const indexed = {
    ...common,
    type: "indexed",
    indexed: sampleIndexedCatalogSearchEvidence,
  };
  const live = {
    ...common,
    type: "live",
    live: sampleLiveCatalogSearchEvidence,
  };
  expect(checks.CatalogSearchResult(indexed)).toBe(true);
  expect(checks.CatalogSearchResult(live)).toBe(true);
  expect(checks.CatalogSearchResult({ ...indexed, version: "" })).toBe(true);
  expect(checks.CatalogSearchResult({ ...indexed, version: undefined })).toBe(
    true,
  );
  expect(
    checks.CatalogSearchResult({
      ...indexed,
      versionPresent: false,
      version: undefined,
    }),
  ).toBe(true);
  for (const invalid of [
    { ...common, type: "local" },
    { ...indexed, live: sampleLiveCatalogSearchEvidence },
    { ...live, local: sampleCatalogSearchLocal },
    { ...common, type: "unknown" },
    { ...indexed, version: null },
    { ...live, versionPresent: false },
    { ...common, type: "indexed" },
    { ...common, type: "live" },
  ])
    expect(checks.CatalogSearchResult(invalid)).toBe(false);
  // Search identity is an opaque React key, not a link path or an invocation URL.
  expect(
    checks.CatalogSearchResult({ ...indexed, id: "indexed:opaque/key" }),
  ).toBe(true);
});

it("validates dynamic facet/profile values rather than accepting only their outer map", () => {
  expect(
    checks.CatalogExploreResponse({
      matched: 0,
      facets: { kind: { buckets: [] } },
    }),
  ).toBe(true);
  for (const facets of [
    { kind: null },
    { kind: { buckets: {} } },
    { kind: { buckets: [{ value: [], count: 1 }] } },
    { kind: { buckets: [], otherCount: -1 } },
  ])
    expect(checks.CatalogExploreResponse({ matched: 0, facets })).toBe(false);
  for (const [key, value] of Object.entries({
    profiles: { ard: "wrong" },
    governance: { rights: {} },
    limits: { bytes: "1" },
    optionalCapabilities: { webhooks: "enabled", media: {} },
  }))
    expect(checks.Capabilities({ ...sampleCapabilities, [key]: value })).toBe(
      false,
    );
});

it("preserves exact opaque evidence, unknown extensions and legitimate large collections", () => {
  const evidence = {
    future: { nested: [null, false, 0, "<script>literal</script>"] },
  };
  expect(
    checks.Representation({
      ...sampleRepresentation,
      profileId: "",
      data: evidence,
      normalizedFacts: evidence,
      handoff: evidence,
      package: evidence,
    }),
  ).toBe(true);
  expect(
    checks.ListingRevision({ ...sampleListingRevision, extensions: evidence }),
  ).toBe(true);
  expect(checks.Job({ ...sampleJob, result: evidence })).toBe(true);
  expect(
    checks.CatalogRecord({
      ...sampleCatalogRecord,
      evidence: { ...sampleEvidenceSet, ...evidence },
    }),
  ).toBe(true);
  expect(
    checks.PluginInspection({
      ...samplePluginInspection,
      files: new Array(1024).fill({ path: "unchanged" }),
      documents: [{ path: "plugin.json", content: evidence }],
    }),
  ).toBe(true);
  expect(
    checks.CatalogRecordPage({
      items: new Array(100).fill(sampleCatalogSummary),
      nextCursor: "x".repeat(2048),
    }),
  ).toBe(true);
  expect(checks.FederationTestResult(evidence)).toBe(false);
  expect(
    checks.WebhookReconciliation({
      ...sampleWebhookReconciliation,
      ...evidence,
    }),
  ).toBe(true);
});

it("validates the exact nested Job fields used for readable outcomes and links", () => {
  const source = {
    type: "source_snapshot",
    sourceId: "source-1",
    snapshotId: "snapshot-1",
    state: "complete",
  };
  const publication = {
    type: "publication_snapshot",
    snapshotId: "snapshot-2",
    host: "catalog.example.com",
    generation: 1,
    state: "candidate",
  };
  for (const result of [source, publication])
    expect(checks.Job({ ...sampleJob, result })).toBe(true);
  for (const [key, value] of [
    ["snapshotId", "../unsafe"],
    ["sourceId", null],
    ["state", "succeeded"],
  ])
    expect(
      checks.Job({ ...sampleJob, result: { ...source, [String(key)]: value } }),
    ).toBe(false);
  for (const [key, value] of [
    ["host", ""],
    ["host", "x".repeat(254)],
    ["host", {}],
    ["generation", 0],
    ["generation", 1.5],
    ["state", "succeeded"],
  ])
    expect(
      checks.Job({
        ...sampleJob,
        result: { ...publication, [String(key)]: value },
      }),
    ).toBe(false);
  expect(
    checks.Job({
      ...sampleJob,
      result: { ...publication, host: "x".repeat(253) },
    }),
  ).toBe(true);
});

it("bounds readable delivery pages, Unicode safe errors and actual HTTP status codes", () => {
  expect(
    checks.WebhookDeliveryPage({
      items: Array(100).fill(sampleWebhookDelivery),
    }),
  ).toBe(true);
  expect(
    checks.WebhookDeliveryPage({
      items: Array(101).fill(sampleWebhookDelivery),
    }),
  ).toBe(false);
  for (const responseStatus of [100, 599])
    expect(
      checks.WebhookDelivery({ ...sampleWebhookDelivery, responseStatus }),
    ).toBe(true);
  for (const responseStatus of [99, 600, 200.1])
    expect(
      checks.WebhookDelivery({ ...sampleWebhookDelivery, responseStatus }),
    ).toBe(false);
  expect(
    checks.WebhookDelivery({
      ...sampleWebhookDelivery,
      safeError: "😀".repeat(2000),
    }),
  ).toBe(true);
  expect(
    checks.WebhookDelivery({
      ...sampleWebhookDelivery,
      safeError: "😀".repeat(2001),
    }),
  ).toBe(false);
});

it("rejects invalid operational counts and discriminators at their owning contracts", () => {
  for (const [key, bad] of [
    ["pendingEvents", -1],
    ["pendingEvents", 1001],
    ["pendingEvents", 0.5],
    ["capacityLossCount", -1],
    ["deliveryLossCount", 0.5],
    ["pendingEventLimit", 999],
  ] as const)
    expect(
      checks.WebhookReconciliation({
        ...sampleWebhookReconciliation,
        [key]: bad,
      }),
    ).toBe(false);
  for (const pendingEvents of [0, 1000])
    expect(
      checks.WebhookReconciliation({
        ...sampleWebhookReconciliation,
        pendingEvents,
      }),
    ).toBe(true);
  for (const [key, bad] of [
    ["progress", -1],
    ["progress", 101],
    ["attemptLimit", 0],
    ["attemptLimit", 21],
    ["attempt", 0.5],
    ["priority", 101],
    ["cancellationRequested", "false"],
    ["state", "completed"],
  ] as const)
    expect(checks.Job({ ...sampleJob, [key]: bad })).toBe(false);
  for (const [key, bad] of [
    ["durableTotal", -1],
    ["liveCount", 51],
    ["durableTotal", "1"],
    ["partial", {}],
    ["tier", "lexical"],
    ["nextCursor", "x".repeat(4097)],
  ] as const)
    expect(
      checks.CatalogSearchResponse({
        ...sampleCatalogSearchResponse,
        [key]: bad,
      }),
    ).toBe(false);
  expect(
    checks.CatalogSearchResponse({
      ...sampleCatalogSearchResponse,
      tier: "hybrid",
    }),
  ).toBe(true);
  expect(
    checks.FederationChangePreview({
      ...sampleFederationChangePreview,
      expiresAt: "invalid",
    }),
  ).toBe(false);
  expect(checks.Namespace({ ...sampleNamespace, id: "../auth/logout" })).toBe(
    false,
  );
  expect(
    checks.WebhookSecretResult({
      ...sampleWebhookSecretResult,
      secret: { private: "sentinel" },
    }),
  ).toBe(false);
});

it("checks package presence and usable one-time secrets without rewriting private data", () => {
  for (const bad of [null, true, "archive"])
    expect(
      checks.Representation({ ...sampleRepresentation, package: bad }),
    ).toBe(false);
  expect(
    checks.WebhookSecretResult({ ...sampleWebhookSecretResult, secret: "" }),
  ).toBe(false);
});

it("accepts bounded upstream continuation diagnostics without losing useful results", () => {
  const diagnostic = {
    status: "continuation_unavailable",
    message:
      "The upstream Registry has more results; this bounded live query does not continue them.",
  };
  expect(checks.CatalogSearchDiagnostic(diagnostic)).toBe(true);
  expect(
    checks.CatalogSearchDiagnostic({ status: "insufficient_evidence" }),
  ).toBe(true);
  expect(checks.LiveSearchDiagnostic({ status: "insufficient_evidence" })).toBe(
    true,
  );
  expect(
    checks.CatalogSearchResponse({
      ...sampleCatalogSearchResponse,
      partial: true,
      diagnostics: [diagnostic],
    }),
  ).toBe(true);
  for (const status of ["unknown_continuation", null, 1]) {
    expect(checks.CatalogSearchDiagnostic({ ...diagnostic, status })).toBe(
      false,
    );
    expect(
      checks.CatalogSearchResponse({
        ...sampleCatalogSearchResponse,
        partial: true,
        diagnostics: [{ ...diagnostic, status }],
      }),
    ).toBe(false);
  }
});

it("validates optional diagnostic source IDs before copying or linking without requiring attribution", () => {
  expect(checks.CatalogSearchDiagnostic({ status: "failed" })).toBe(true);
  expect(checks.CatalogSearchDiagnostic(sampleCatalogSearchDiagnostic)).toBe(
    true,
  );
  expect(
    checks.CatalogSearchDiagnostic({
      ...sampleCatalogSearchDiagnostic,
      sourceId: "ABCDEF01-2345-6789-ABCD-EF0123456789",
    }),
  ).toBe(true);
  for (const sourceId of [
    null,
    1,
    {},
    "",
    "source-1",
    "../sources",
    "/sources",
    "https://peer.example",
    "javascript:alert(1)",
    "00000000-0000-0000-0000-000000000001?query",
    "00000000-0000-0000-0000-000000000001\n",
  ]) {
    const diagnostic = { ...sampleCatalogSearchDiagnostic, sourceId };
    expect(checks.CatalogSearchDiagnostic(diagnostic)).toBe(false);
    expect(
      checks.CatalogSearchResponse({
        ...sampleCatalogSearchResponse,
        diagnostics: [diagnostic],
      }),
    ).toBe(false);
  }
});

it("checks bounded indexed attribution and group identities before any copy or navigation", () => {
  const indexed = {
    ...sampleCatalogSearchResult,
    type: "indexed",
    local: undefined,
    indexed: sampleIndexedCatalogSearchEvidence,
  };
  const ids = Array.from(
    { length: 16 },
    (_, i) => `00000000-0000-7000-8000-${String(i).padStart(12, "0")}`,
  );
  for (const sourceIds of [[], ids]) {
    expect(
      checks.CatalogSearchResult({
        ...indexed,
        indexed: {
          ...sampleIndexedCatalogSearchEvidence,
          sourceIds,
          sourceCount: 20,
          sourcesTruncated: true,
        },
      }),
    ).toBe(true);
  }
  for (const fields of [
    { sourceIds: null },
    { sourceIds: [null] },
    { sourceIds: ["../private?query"] },
    { sourceIds: ["https://source.test"] },
    { sourceIds: [ids[0], ids[0]] },
    { sourceIds: [...ids, "00000000-0000-7000-8000-000000000016"] },
    { observationGroupId: "group-1" },
    { observationGroupId: "observation-group-v1:sha256:" + "g".repeat(64) },
    { observationGroupId: "../observation-group" },
    { sameSourceConflict: undefined },
    { sameSourceConflict: "false" },
  ]) {
    const value = {
      ...indexed,
      indexed: { ...sampleIndexedCatalogSearchEvidence, ...fields },
    };
    expect(checks.CatalogSearchResult(value)).toBe(false);
    expect(
      checks.CatalogSearchResponse({
        ...sampleCatalogSearchResponse,
        items: [value],
      }),
    ).toBe(false);
  }
});

it("requires local validation evidence and exact independent trust and legal-rights states", () => {
  for (const state of ["complete", "blocked"]) {
    for (const trust of [
      "not_assessed",
      "unsupported",
      "unverified",
      "verified",
      "failed",
      "expired",
    ]) {
      expect(
        checks.CatalogRecord({
          ...sampleCatalogRecord,
          evidence: { ...sampleEvidenceSet, state, trust: { state: trust } },
        }),
      ).toBe(true);
    }
  }
  for (const evidence of [
    undefined,
    null,
    {},
    { ...sampleEvidenceSet, state: "passed" },
    { ...sampleEvidenceSet, trust: { state: "approved" } },
    { ...sampleEvidenceSet, legalRights: { state: "cleared" } },
    { ...sampleEvidenceSet, legalRights: undefined },
  ])
    expect(checks.CatalogRecord({ ...sampleCatalogRecord, evidence })).toBe(
      false,
    );
});

it("checks indexed diagnostic rows and compatibility before rendering links while retaining unknown evidence", () => {
  expect(
    checks.ObservationSearchResult({
      ...sampleObservationSearchResult,
      observation: {
        ...sampleObservation,
        compatibility: {
          ...sampleObservationCompatibility,
          findings: [{ future: true }],
        },
        future: { exact: null },
      },
    }),
  ).toBe(true);
  for (const row of [
    {},
    sampleObservation,
    { ...sampleObservationSearchResult, source: "../sources" },
    { ...sampleObservationSearchResult, score: -1 },
    { ...sampleObservationSearchResult, score: 101 },
    { ...sampleObservationSearchResult, score: 1.5 },
    {
      ...sampleObservationSearchResult,
      observation: { ...sampleObservation, kind: "future" },
    },
    {
      ...sampleObservationSearchResult,
      observation: {
        ...sampleObservation,
        compatibility: {
          ...sampleObservationCompatibility,
          structural: "approved",
        },
      },
    },
  ])
    expect(checks.ObservationSearchResult(row)).toBe(false);
  expect(
    checks.ObservationSearchPage({
      items: new Array(100).fill(sampleObservationSearchResult),
    }),
  ).toBe(true);
  expect(
    checks.ObservationSearchPage({
      items: new Array(101).fill(sampleObservationSearchResult),
    }),
  ).toBe(false);
});

it("validates a source's optional policy name before exposing it to the editor", () => {
  expect(checks.FederationSource(sampleFederationSource)).toBe(true);
  for (const trustVerificationPolicy of ["", "operator-ans", "a".repeat(63)])
    expect(
      checks.FederationSource({
        ...sampleFederationSource,
        trustVerificationPolicy,
      }),
    ).toBe(true);
  for (const trustVerificationPolicy of [
    null,
    false,
    42,
    "Uppercase",
    "../policy",
    "a".repeat(64),
    "operator\n",
  ])
    expect(
      checks.FederationSource({
        ...sampleFederationSource,
        trustVerificationPolicy,
      }),
    ).toBe(false);
});

it("validates bounded live results separately from Catalog diagnostics without imposing a native entry schema", () => {
  const row = sampleLiveSearchResponse.results[0]!;
  for (const version of [undefined, "", "1.0+build"]) {
    expect(
      checks.LiveSearchResult({
        ...row,
        versionPresent: true,
        version,
        entry: { type: { future: true }, future: [null] },
      }),
    ).toBe(true);
  }
  for (const change of [
    { version: null },
    { sourceId: "../sources" },
    { score: -1 },
    { score: 101 },
    { score: 0.5 },
    { entry: [] },
    { versionPresent: null },
    { source: {} },
    { sourceRevision: undefined },
    { sourceRevision: 0 },
    { sourceRevision: 1.5 },
    { sourceRevision: Number.MAX_SAFE_INTEGER + 1 },
    { exactEntryDigest: undefined },
    { exactEntryDigest: `sha256:${"A".repeat(64)}` },
    { exactEntryDigest: `sha256:${"a".repeat(63)}` },
    { exactEntryDigest: `sha256:${"a".repeat(64)}\n` },
  ])
    expect(checks.LiveSearchResult({ ...row, ...change })).toBe(false);
  for (const status of [
    "succeeded",
    "failed",
    "invalid_response",
    "policy_stale",
    "fanout_limited",
    "diagnostics_truncated",
    "continuation_unavailable",
  ]) {
    expect(checks.LiveSearchDiagnostic({ status })).toBe(true);
    expect(
      checks.LiveSearchDiagnostic({
        status,
        sourceId: row.sourceId,
        message: "Safe message",
      }),
    ).toBe(true);
  }
  for (const diagnostic of [
    { status: "live_results_limited" },
    { status: 1 },
    { status: "succeeded", sourceId: "../sources" },
    { status: "failed", message: {} },
  ]) {
    expect(checks.LiveSearchDiagnostic(diagnostic)).toBe(false);
  }
  expect(
    checks.LiveSearchResponse({
      results: new Array(50).fill(row),
      partial: true,
      diagnostics: new Array(32).fill({ status: "failed" }),
    }),
  ).toBe(true);
  expect(
    checks.LiveSearchResponse({
      results: new Array(51).fill(row),
      partial: true,
    }),
  ).toBe(false);
  expect(
    checks.LiveSearchResponse({
      results: [],
      partial: true,
      diagnostics: new Array(33).fill({ status: "failed" }),
    }),
  ).toBe(false);
});

it("bounds configured host choices, rejects ambiguous or unsafe values and preserves canonical IP hosts", () => {
  const item = { host: "catalog.example.com", mode: "anonymous" };
  const items = Array.from({ length: 64 }, (_, index) => ({
    ...item,
    host: `host${index}.example`,
  }));
  for (const value of [
    { items: [] },
    { items },
    {
      items: [
        { host: "2001:db8::1", mode: "authenticated" },
        { host: "127.0.0.1", mode: "disabled" },
      ],
    },
    { items: [{ ...item, future: { untouched: true } }] },
  ])
    expect(checks.PublicationHostInventory(value)).toBe(true);
  for (const value of [
    { items: [...items, { ...item, host: "extra.example" }] },
    { items: [item, item] },
    { items: [{ ...item, mode: "public" }] },
    ...[
      "",
      "https://catalog.example.com",
      "../catalog",
      "host?token=x",
      "host#secret",
      " host.example",
      "x".repeat(254),
    ].map((host) => ({ items: [{ ...item, host }] })),
  ])
    expect(checks.PublicationHostInventory(value)).toBe(false);
});
it.each([undefined, -1, 10001, 1.5, "3"])(
  "rejects an unavailable or invalid affected-artwork count (%s)",
  (artworkOverridesAffected) => {
    expect(
      checks.FederationChangePreview({
        ...sampleFederationChangePreview,
        artworkOverridesAffected,
      }),
    ).toBe(false);
  },
);

it("validates consumed local ratings, separate peer claims and private import links", () => {
  const rating = { count: 5, average: 3.4, receipt: "live-proof" };
  const peer = {
    reporter: "https://peer.example",
    count: 8,
    average: 4.1,
    measuredAt: "2026-10-08T12:00:00Z",
  };
  const imported = { resourceId: "resource-1", changedEntry: true };
  expect(
    checks.CatalogSearchResult({
      ...sampleCatalogSearchResult,
      ratings: rating,
      peerRatings: peer,
      importedHere: imported,
    }),
  ).toBe(true);
  expect(
    checks.CatalogSearchResult({
      ...sampleCatalogSearchResult,
      ratings: { count: 0 },
    }),
  ).toBe(true);
  for (const ratings of [
    null,
    { ...rating, count: -1 },
    { ...rating, count: 5.5 },
    { ...rating, average: null },
    { ...rating, average: 6 },
    { count: 0, average: 0 },
    { ...rating, receipt: 42 },
  ])
    expect(
      checks.CatalogSearchResult({ ...sampleCatalogSearchResult, ratings }),
    ).toBe(false);
  for (const peerRatings of [
    null,
    { ...peer, reporter: 2 },
    { ...peer, count: 0 },
    { ...peer, average: "4" },
    { ...peer, measuredAt: "yesterday" },
  ])
    expect(
      checks.CatalogSearchResult({ ...sampleCatalogSearchResult, peerRatings }),
    ).toBe(false);
  for (const importedHere of [
    null,
    { ...imported, resourceId: "" },
    { ...imported, changedEntry: "yes" },
  ])
    expect(
      checks.CatalogSearchResult({
        ...sampleCatalogSearchResult,
        importedHere,
      }),
    ).toBe(false);
});

it("keeps Search cursor limits separate from collection cursors", () => {
  expect(
    checks.CatalogSearchResponse({
      ...sampleCatalogSearchResponse,
      nextCursor: "x".repeat(4096),
    }),
  ).toBe(true);
  expect(
    checks.CatalogSearchResponse({
      ...sampleCatalogSearchResponse,
      nextCursor: "x".repeat(4097),
    }),
  ).toBe(false);
  expect(
    checks.ResourcePage({
      ...sampleResourcePage,
      nextCursor: "x".repeat(2049),
    }),
  ).toBe(false);
});

it("bounds newly consumed Listing categories without trimming or UTF-16 length mistakes", () => {
  const maximum = "🧭".repeat(80);
  expect(
    checks.ListingRevision({
      ...sampleListingRevision,
      categories: Array.from({ length: 16 }, () => maximum),
    }),
  ).toBe(true);
  expect(
    checks.ListingRevision({
      ...sampleListingRevision,
      categories: ["", " Finance, Résumé 🧭 "],
    }),
  ).toBe(true);
  for (const categories of [
    null,
    {},
    [4],
    [null],
    ["x\0y"],
    ["🧭".repeat(81)],
    Array.from({ length: 17 }, () => "valid"),
  ]) {
    expect(
      checks.ListingRevision({ ...sampleListingRevision, categories }),
    ).toBe(false);
  }
});
