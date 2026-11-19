// Validate only the maintenance fields consumed by this view and its links.
import {
  array,
  choice,
  cursor,
  flag,
  identifier,
  number,
  object,
  optional,
  text,
  timestamp,
} from "../api/values";

/** Refuse malformed maintenance rows rather than presenting them as healthy. */
export function maintenanceItem(value: unknown): boolean {
  return (
    object(value) &&
    identifier(value.revisionId) &&
    identifier(value.resourceId) &&
    identifier(value.namespaceId) &&
    identifier(value.versionId) &&
    identifier(value.evidenceId) &&
    text(value.canonicalIdentifier) &&
    text(value.version) &&
    number(value.revisionNumber, 1) &&
    choice(value.kind, ["agent", "mcp_server", "skill", "plugin"]) &&
    choice(value.evidenceState, ["complete", "blocked"]) &&
    text(value.validationProfile) &&
    text(value.policyVersion) &&
    timestamp(value.validatedAt) &&
    optional(value.expiresAt, timestamp) &&
    flag(value.applicable) &&
    Array.isArray(value.conditions) &&
    array(value.conditions, (condition) =>
      choice(condition, ["expiring", "expired", "blocked", "stale"]),
    ) &&
    value.conditions.length > 0 &&
    value.conditions.length <= 4 &&
    new Set(value.conditions).size === value.conditions.length
  );
}

/** The server observation time describes a moving window, never a frozen inventory. */
export function maintenancePage(value: unknown): boolean {
  return (
    object(value) &&
    Array.isArray(value.items) &&
    array(value.items, maintenanceItem) &&
    value.items.length <= 100 &&
    cursor(value.nextCursor) &&
    timestamp(value.observedAt) &&
    number(value.horizonHours, 1, 168)
  );
}
