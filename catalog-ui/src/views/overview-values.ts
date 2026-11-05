import { choice, flag, number, object, timestamp } from "../api/values";
import { kindChoices } from "../resource-kinds";

export const overviewLimit = 10000;

export function scope(
  value: unknown,
  tenant: string,
  limit = overviewLimit,
): value is Record<string, unknown> {
  return (
    object(value) &&
    value.tenantId === tenant &&
    value.scope === "current_access" &&
    value.selectionLimit === limit &&
    timestamp(value.observedAt)
  );
}

export function boundedCount(value: unknown): boolean {
  return (
    object(value) &&
    flag(value.limited) &&
    number(value.value, 0, overviewLimit) &&
    (!value.limited || value.value === overviewLimit)
  );
}

export function resourceOverview(value: unknown, tenant: string): boolean {
  if (
    !scope(value, tenant) ||
    !boundedCount(value.total) ||
    !object(value.total)
  )
    return false;
  if (value.total.limited) return value.kinds === undefined;
  if (!Array.isArray(value.kinds) || value.kinds.length !== kindChoices.length)
    return false;
  const seen = new Set();
  let total = 0;
  for (const item of value.kinds) {
    if (
      !object(item) ||
      !choice(
        item.kind,
        kindChoices.map((k) => k.value),
      ) ||
      seen.has(item.kind) ||
      !number(item.count, 0, overviewLimit)
    )
      return false;
    seen.add(item.kind);
    total += item.count as number;
  }
  return total === value.total.value;
}

export function releaseOverview(value: unknown, tenant: string): boolean {
  return (
    scope(value, tenant) &&
    boundedCount(value.published) &&
    boundedCount(value.deprecated)
  );
}

export function directoryOverview(value: unknown, tenant: string): boolean {
  return scope(value, tenant) && boundedCount(value.count);
}

export function indexedOverview(value: unknown, tenant: string): boolean {
  return (
    scope(value, tenant) &&
    flag(value.limited) &&
    (value.limited
      ? value.count === undefined
      : number(value.count, 0, overviewLimit))
  );
}

/** Accept only fixed, non-sensitive presets. Never restore private search inputs. */
export function catalogOriginPreset(search: string) {
  const values = new URLSearchParams(search).getAll("origin");
  return values.length === 1 &&
    (values[0] === "local" || values[0] === "indexed")
    ? values[0]
    : undefined;
}

export function resourceKindPreset(search: string) {
  const values = new URLSearchParams(search).getAll("kind");
  return values.length === 1
    ? (kindChoices.find((k) => k.value === values[0])?.value ?? "")
    : "";
}
