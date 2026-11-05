import { choice, identifier, object, text, timestamp } from "../api/values";

export function publisherVerificationMode(value: unknown): boolean {
  return (
    object(value) &&
    object(value.governance) &&
    choice(value.governance.publisherDomainVerification, [
      "disabled",
      "optional",
      "required_for_approval",
    ])
  );
}

export function publisherVerificationStatus(value: unknown): boolean {
  if (
    !object(value) ||
    !text(value.domain) ||
    value.domain.length < 1 ||
    value.domain.length > 221 ||
    !choice(value.mode, ["disabled", "optional", "required_for_approval"]) ||
    !choice(value.state, ["not_verified", "verified", "expired"]) ||
    !choice(value.challengeState, [
      "not_issued",
      "pending",
      "expired",
      "consumed",
    ])
  )
    return false;
  for (const field of ["challengeId", "lastCheckChallengeId"]) {
    if (value[field] !== undefined && !identifier(value[field])) return false;
  }
  for (const field of [
    "challengeIssuedAt",
    "challengeExpiresAt",
    "verifiedAt",
    "expiresAt",
    "lastCheckAt",
  ]) {
    if (value[field] !== undefined && !timestamp(value[field])) return false;
  }
  if (
    value.state !== "not_verified" &&
    (!timestamp(value.verifiedAt) || !timestamp(value.expiresAt))
  )
    return false;
  if (
    value.challengeState !== "not_issued" &&
    (!identifier(value.challengeId) ||
      !timestamp(value.challengeIssuedAt) ||
      !timestamp(value.challengeExpiresAt))
  )
    return false;
  if (
    value.lastCheckOutcome !== undefined &&
    (!choice(value.lastCheckOutcome, [
      "matched",
      "mismatch",
      "not_found",
      "unavailable",
      "response_limit",
    ]) ||
      !timestamp(value.lastCheckAt) ||
      !identifier(value.lastCheckChallengeId))
  )
    return false;
  return true;
}

export function publisherVerificationChallenge(value: unknown): boolean {
  if (!object(value) || !publisherVerificationStatus(value.status))
    return false;
  if (value.value === undefined)
    return (
      value.name === undefined &&
      value.challengeId === undefined &&
      value.expiresAt === undefined
    );
  return (
    object(value.status) &&
    value.status.challengeState === "pending" &&
    value.status.mode !== "disabled" &&
    identifier(value.challengeId) &&
    value.challengeId === value.status.challengeId &&
    typeof value.name === "string" &&
    value.name ===
      `_open-agentic-catalog-challenge.${String(value.status.domain)}.` &&
    typeof value.value === "string" &&
    /^open-agentic-catalog-verification=[A-Za-z0-9_-]{43}$/.test(value.value) &&
    timestamp(value.expiresAt) &&
    value.expiresAt === value.status.challengeExpiresAt
  );
}

/** Presentation eligibility only. The backend validates the exact declared domain. */
export function publisherVerificationDomain(domain: string): boolean {
  if (
    domain.length > 221 ||
    !domain.includes(".") ||
    !/^[a-z0-9.-]+$/.test(domain)
  )
    return false;
  const labels = domain.split(".");
  return !(
    labels.length === 4 &&
    labels.every(
      (label) => /^(0|[1-9][0-9]{0,2})$/.test(label) && Number(label) <= 255,
    )
  );
}
