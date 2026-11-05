// Select server-bound ANS evidence without reconstructing or hashing an entry.
import type { Access } from "../api/client";
import { permits } from "../api/permissions";
import type { components } from "../api/schema";
import { object, text } from "../api/values";

type Schema = components["schemas"];
export type TrustSelection = Omit<Schema["TrustAssessmentRequest"], "reason">;

export function canRequestTrust(access: Access): boolean {
  return (
    ["source.manage", "source.read_evidence", "federation.discover"] as const
  ).every((permission) => permits(access, permission, "sources.manage"));
}

function digest(value: unknown): value is string {
  return (
    text(value) && value.length === 71 && /^sha256:[a-f0-9]{64}$/.test(value)
  );
}

/** Check consumed native selection fields, not the complete standards document. */
function entrySelection(
  entry: unknown,
  identifier: string,
  present: boolean,
  version: string | undefined,
) {
  if (
    !object(entry) ||
    !/^urn:air:[^:\s]+(?::[A-Za-z0-9_.-]+)+$/.test(identifier) ||
    new TextEncoder().encode(identifier).length > 512 ||
    entry.identifier !== identifier ||
    Object.hasOwn(entry, "version") !== present ||
    (present &&
      (!text(entry.version) ||
        entry.version !== (version ?? "") ||
        [...entry.version].length > 128)) ||
    (!present && version !== undefined && version !== "") ||
    !text(entry.type) ||
    !entry.type.trim() ||
    [...entry.type].length > 255
  )
    return;
  // ARD delivery is exclusive. Do not infer a URL or inline document for sparse entries.
  const hasURL = Object.hasOwn(entry, "url");
  const hasData = Object.hasOwn(entry, "data");
  if (
    hasURL === hasData ||
    (hasURL && (!text(entry.url) || !entry.url.trim())) ||
    (hasData && !object(entry.data))
  )
    return;
  return {
    identifier,
    versionPresent: present,
    ...(present ? { version: version ?? "" } : {}),
    mediaType: entry.type,
    delivery: hasURL ? ("url" as const) : ("data" as const),
  };
}

export function eligibleObservation(
  source: Schema["FederationSource"],
  observation: Schema["Observation"],
): boolean {
  return (
    source.status === "active" &&
    !!source.lastCompleteSnapshotId &&
    observation.sourceId === source.id &&
    observation.freshness === "current" &&
    observation.rawEntryAvailable === true &&
    !!observation.identifier &&
    digest(observation.exactEntryDigest)
  );
}

export function indexedTrustSelection(
  source: Schema["FederationSource"],
  observation: Schema["Observation"],
  entry: unknown,
): TrustSelection | undefined {
  if (
    !eligibleObservation(source, observation) ||
    !Number.isSafeInteger(source.revision) ||
    source.revision < 1
  )
    return;
  const selected = entrySelection(
    entry,
    observation.identifier!,
    observation.versionPresent,
    observation.version,
  );
  if (!selected || selected.mediaType !== observation.mediaType) return;
  return {
    ...selected,
    sourceRevision: source.revision,
    observationId: observation.id,
    exactEntryDigest: observation.exactEntryDigest,
  };
}

export function liveTrustSelection(
  item: Schema["LiveSearchResponse"]["results"][number],
  query: string,
): TrustSelection | undefined {
  if (
    !Number.isSafeInteger(item.sourceRevision) ||
    item.sourceRevision < 1 ||
    !digest(item.exactEntryDigest) ||
    !query.trim() ||
    [...query].length > 500
  )
    return;
  const selected = entrySelection(
    item.entry,
    item.identifier,
    item.versionPresent,
    item.version,
  );
  if (!selected) return;
  return {
    ...selected,
    sourceRevision: item.sourceRevision,
    exactEntryDigest: item.exactEntryDigest,
    text: query,
  };
}
