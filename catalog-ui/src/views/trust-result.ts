// Private assessment consumption checks; signatures remain the server verifier's responsibility.
import { permits } from "../api/permissions";
import type { Access } from "../api/client";
import {
  object,
  text,
  number,
  timestamp,
  choice,
  optional,
} from "../api/values";
export function canReadTrust(access: Access): boolean {
  return (
    permits(access, "job.read", "sources.read") &&
    permits(access, "source.read_evidence", "sources.read")
  );
}

/** Generic Job metadata has a different OAuth ceiling from private assessment disclosure. */
export function canReadTrustJob(access: Access): boolean {
  return (
    permits(access, "job.read", "catalog.read") &&
    access.permissions.includes("source.read_evidence")
  );
}

export function trustResultHref(
  basePath: string,
  sourceId: string,
  jobId: string,
) {
  return `${basePath}/trust-assessments/${jobId}?sourceId=${encodeURIComponent(sourceId)}`;
}

export function trustID(value: unknown): value is string {
  return (
    text(value) &&
    /^[\da-f]{8}-[\da-f]{4}-[\da-f]{4}-[\da-f]{4}-[\da-f]{12}$/i.test(value)
  );
}

export const trustChecks = [
  "receipt",
  "producer",
  "log",
  "checkpoint",
  "inclusion",
  "target",
  "status",
] as const;
export const evidenceNames = [
  "entry",
  "receipt",
  "checkpoint",
  "checkpointHistory",
  "statusToken",
] as const;
const evidenceLimits = [349528, 349528, 43692, 349528, 87384];
function bounded(value: unknown, min: number, max: number): value is string {
  return text(value) && [...value].length >= min && [...value].length <= max;
}
function hash(value: unknown): value is string {
  return text(value) && value.length === 64 && /^[a-f0-9]{64}$/.test(value);
}
function base64(value: unknown, max: number): boolean {
  if (!text(value) || value.length > max) return false;
  try {
    return btoa(atob(value)) === value;
  } catch {
    return false;
  }
}

/** Validate fields used by rendering and downloads before any private result is installed. */
export function trustResponse(value: unknown): boolean {
  if (!object(value) || !object(value.result)) return false;
  const r = value.result;
  const a = r.assessment;
  const e = r.evidence;
  if (
    !trustID(r.sourceId) ||
    !number(r.sourceRevision, 1) ||
    !bounded(r.policyName, 1, 63) ||
    !bounded(r.authority, 1, 253) ||
    !text(r.policyDigest) ||
    !r.policyDigest.startsWith("sha256:") ||
    !hash(r.policyDigest.slice(7)) ||
    !text(r.exactEntryDigest) ||
    !r.exactEntryDigest.startsWith("sha256:") ||
    !hash(r.exactEntryDigest.slice(7)) ||
    !bounded(r.mediaType, 1, 255) ||
    !choice(r.delivery, ["url", "data"]) ||
    r.softwareTrust !== "not_assessed" ||
    r.artifactIntegrity !== "not_assessed" ||
    r.rights !== "not_assessed" ||
    !object(a) ||
    a.profile !== "ans-registration-v1" ||
    a.policyDigest !== r.policyDigest.slice(7) ||
    a.entryDigest !== r.exactEntryDigest.slice(7) ||
    !timestamp(a.checkedAt) ||
    (a.validUntil !== undefined && !timestamp(a.validUntil)) ||
    typeof a.currentRegistration !== "boolean" ||
    !trustChecks.every((name) => {
      const check = a[name];
      return (
        object(check) &&
        choice(check.state, [
          "valid",
          "invalid",
          "unavailable",
          "unsupported",
          "not_assessed",
        ]) &&
        bounded(check.code, 1, 100) &&
        (!a.currentRegistration || check.state === "valid")
      );
    }) ||
    !["schemaVersion", "eventType", "subject", "logId"].every(
      (name, i) =>
        a[name] === undefined || bounded(a[name], 0, [100, 100, 4096, 128][i]!),
    ) ||
    ![
      "registrationKeyFingerprint",
      "logKeyFingerprint",
      "envelopeLogKeyFingerprint",
    ].every((name) => a[name] === undefined || hash(a[name])) ||
    !object(e) ||
    e.profile !== "ans-registration-v1" ||
    !evidenceNames.every(
      (name, i) =>
        (name === "checkpointHistory" && e[name] === undefined) ||
        base64(e[name], evidenceLimits[i]!),
    ) ||
    !e.entry ||
    new TextEncoder().encode(JSON.stringify(e)).length > 1048576 ||
    typeof value.current !== "boolean" ||
    !choice(value.applicability, [
      "historical",
      "negative_or_insufficient",
      "expired",
      "current",
    ])
  )
    return false;
  if (
    a.currentRegistration &&
    optional(
      a.validUntil,
      (_validUntilValue) =>
        Date.parse(String(_validUntilValue)) <= Date.parse(String(a.checkedAt)),
    )
  )
    return false;
  return (
    value.current === (value.applicability === "current") &&
    (!value.current || a.currentRegistration) &&
    (value.applicability !== "negative_or_insufficient" ||
      !a.currentRegistration)
  );
}

/** Decode original bytes without interpreting JSON, text, URLs or active content. */
export function evidenceBytes(encoded: string): Uint8Array<ArrayBuffer> {
  return Uint8Array.from(atob(encoded), (char) => char.charCodeAt(0));
}
