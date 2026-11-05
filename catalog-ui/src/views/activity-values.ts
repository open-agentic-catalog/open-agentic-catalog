import type { components } from "../api/schema";
import { object, timestamp } from "../api/values";

export type Activity = components["schemas"]["ServiceActivity"];
export const families = [
  "control_search",
  "control_explore",
  "observation_search",
  "administrative_live_search",
  "ard_search",
  "ard_explore",
  "ard_list",
  "discovery_documents",
  "http_mcp",
  "control_read",
  "control_command",
  "platform",
  "observer",
  "unknown_auxiliary",
] as const;
export const methods = [
  "GET",
  "HEAD",
  "POST",
  "PATCH",
  "DELETE",
  "OPTIONS",
  "OTHER",
] as const;
export const statuses = ["1xx", "2xx", "3xx", "4xx", "5xx"] as const;
export const searchOutcomes = [
  "succeeded",
  "partial",
  "rejected",
  "failed",
] as const;
export const mcpOperations = ["protocol", "discover", "list", "call"] as const;
export const mcpOutcomes = [
  "completed",
  "rejected",
  "failed",
  "tool_error",
  "busy",
  "cancelled",
] as const;
const maximum = 18446744073709551615n;
export const counter = (v: unknown): v is string =>
  typeof v === "string" &&
  /^(0|[1-9][0-9]{0,19})$/.test(v) &&
  BigInt(v) <= maximum;
const finite = (v: unknown): v is number =>
  typeof v === "number" && Number.isFinite(v) && v >= 0;
function rows(
  v: unknown,
  names: readonly string[],
  key: string,
): v is Record<string, unknown>[] {
  return (
    Array.isArray(v) &&
    v.length === names.length &&
    v.every(
      (row, i) =>
        object(row) && row[key] === names[i] && counter(row.completed),
    )
  );
}
const sum = (v: Record<string, unknown>[]) =>
  v.reduce((total, row) => total + BigInt(row.completed as string), 0n);

/** Validate the bounded fields consumed by Activity, including population arithmetic. */
export function activity(value: unknown): value is Activity {
  if (
    !object(value) ||
    value.schema !== "service-activity-v1" ||
    value.population !== "catalog-http-v1" ||
    value.scope !== "responding_api_process" ||
    value.source !== "process_memory" ||
    typeof value.instanceId !== "string" ||
    !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
      value.instanceId,
    ) ||
    !["api", "all"].includes(value.role as string) ||
    !timestamp(value.startedAt) ||
    !timestamp(value.observedAt) ||
    !finite(value.elapsedSeconds) ||
    !object(value.http) ||
    !counter(value.http.completed) ||
    !counter(value.http.activeCatalog) ||
    !rows(value.http.families, families, "family") ||
    !object(value.controlSearch) ||
    !rows(value.controlSearch.outcomes, searchOutcomes, "outcome") ||
    !object(value.mcp) ||
    typeof value.mcp.enabled !== "boolean"
  )
    return false;
  if (
    !value.http.families.every(
      (row) =>
        finite(row.durationSeconds) &&
        rows(row.methods, methods, "method") &&
        rows(row.statusClasses, statuses, "statusClass") &&
        sum(row.methods) === BigInt(row.completed as string) &&
        sum(row.statusClasses) === BigInt(row.completed as string),
    )
  )
    return false;
  if (
    sum(value.http.families) !== BigInt(value.http.completed) ||
    !Number.isFinite(
      value.http.families.reduce(
        (total, row) => total + (row.durationSeconds as number),
        0,
      ),
    )
  )
    return false;
  const outcomes = value.mcp.outcomes;
  return (
    Array.isArray(outcomes) &&
    outcomes.length === 24 &&
    outcomes.every(
      (row, i) =>
        object(row) &&
        row.operation === mcpOperations[Math.floor(i / 6)] &&
        row.outcome === mcpOutcomes[i % 6] &&
        counter(row.completed),
    )
  );
}

export const catalogFamilies = (sample: Activity) =>
  sample.http.families.slice(0, 11);
export const completed = (sample: Activity) =>
  catalogFamilies(sample).reduce(
    (total, row) => total + BigInt(row.completed),
    0n,
  );
export const duration = (sample: Activity) =>
  catalogFamilies(sample).reduce(
    (total, row) => total + row.durationSeconds,
    0,
  );
export function mean(seconds: number, count: bigint) {
  const value = count === 0n ? undefined : (seconds / Number(count)) * 1000;
  return value !== undefined && Number.isFinite(value) ? value : undefined;
}
export const countText = (value: string | bigint) =>
  BigInt(value).toLocaleString();

/** Differences stay exact until conversion to an explicitly approximate rate or mean. */
export function interval(before: Activity | undefined, next: Activity) {
  if (!before) return undefined;
  if (
    before.instanceId !== next.instanceId ||
    before.startedAt !== next.startedAt ||
    before.role !== next.role ||
    before.schema !== next.schema ||
    before.population !== next.population ||
    before.source !== next.source ||
    before.scope !== next.scope ||
    before.mcp.enabled !== next.mcp.enabled
  )
    return undefined;
  const elapsed = next.elapsedSeconds - before.elapsedSeconds;
  if (elapsed < 1 || elapsed > 30) return undefined;
  const counts = (sample: Activity) => [
    sample.http.completed,
    ...sample.http.families.flatMap((row) => [
      row.completed,
      ...row.methods.map((v) => v.completed),
      ...row.statusClasses.map((v) => v.completed),
    ]),
    ...sample.controlSearch.outcomes.map((row) => row.completed),
    ...sample.mcp.outcomes.map((row) => row.completed),
  ];
  const old = counts(before);
  if (
    counts(next).some((n, i) => BigInt(n) < BigInt(old[i]!)) ||
    next.http.families.some(
      (row, i) =>
        row.durationSeconds < before.http.families[i]!.durationSeconds,
    )
  )
    return undefined;
  const delta = completed(next) - completed(before);
  // Subtract matching rows before summing to avoid cancellation between large totals.
  const seconds = catalogFamilies(next).reduce(
    (total, row, i) =>
      total + (row.durationSeconds - before.http.families[i]!.durationSeconds),
    0,
  );
  if (
    !Number.isFinite(seconds) ||
    seconds < 0 ||
    (delta === 0n && seconds !== 0)
  )
    return undefined;
  const rate = Number(delta) / elapsed;
  const latency = mean(seconds, delta);
  if (delta > 0n && latency === undefined) return undefined;
  return Number.isFinite(rate) &&
    (latency === undefined || Number.isFinite(latency))
    ? { elapsed, rate, latency }
    : undefined;
}
