import {
  number,
  object,
  timestamp,
  identifier,
  choice,
  optional,
} from "../api/values";
import { scope, boundedCount } from "./overview-values";
import { jobKinds } from "./operational-labels";
import { jobStates } from "./job-state";
export const sourceStates = ["active", "paused", "error"] as const;
export const syncStates = [
  "not_applicable",
  "not_yet_synced",
  "complete",
  "incomplete",
  "failed",
] as const;
export function distribution(
  value: unknown,
  states: readonly string[],
  total: number,
) {
  if (!Array.isArray(value) || value.length !== states.length) return false;
  const seen = new Set();
  let sum = 0;
  for (const row of value) {
    if (
      !object(row) ||
      !choice(row.state, states) ||
      seen.has(row.state) ||
      !number(row.count, 0, 10000)
    )
      return false;
    seen.add(row.state);
    sum += row.count as number;
  }
  return sum === total;
}
function syncSummary(value: unknown): value is Record<string, unknown> {
  return (
    object(value) &&
    number(value.revision, 1) &&
    choice(value.state, ["complete", "incomplete", "failed"]) &&
    timestamp(value.completedAt)
  );
}

export function sourceOverview(value: unknown, tenant: string): boolean {
  if (
    !scope(value, tenant) ||
    !object(value.total) ||
    !boundedCount(value.total) ||
    !Array.isArray(value.problems) ||
    value.problems.length > 5
  )
    return false;
  if (
    value.total.limited
      ? value.states !== undefined ||
        value.sync !== undefined ||
        value.historicalComplete !== undefined
      : !distribution(
          value.states,
          sourceStates,
          value.total.value as number,
        ) ||
        !distribution(value.sync, syncStates, value.total.value as number) ||
        !number(value.historicalComplete, 0, value.total.value as number)
  )
    return false;
  const seen = new Set();
  return value.problems.every((row) => {
    if (
      !object(row) ||
      !identifier(row.sourceId) ||
      seen.has(row.sourceId) ||
      typeof row.displayName !== "string" ||
      [...row.displayName].length > 160 ||
      !number(row.revision, 1) ||
      !syncSummary(row.latest) ||
      row.latest.revision !== row.revision ||
      !choice(row.latest.state, ["incomplete", "failed"])
    )
      return false;
    seen.add(row.sourceId);
    return optional(
      row.lastComplete,
      (_lastCompleteValue) =>
        syncSummary(_lastCompleteValue) &&
        _lastCompleteValue.state === "complete" &&
        (_lastCompleteValue.revision as number) <= (row.revision as number),
    );
  });
}

export function jobOverview(value: unknown, tenant: string): boolean {
  if (
    !scope(value, tenant) ||
    !boundedCount(value.total) ||
    !object(value.total) ||
    !boundedCount(value.due) ||
    !object(value.due)
  )
    return false;
  if (
    value.due.value === 0
      ? value.oldestDueAt !== undefined
      : !timestamp(value.oldestDueAt)
  )
    return false;
  if (
    value.oldestDueAt !== undefined &&
    Date.parse(value.oldestDueAt as string) >
      Date.parse(value.observedAt as string)
  )
    return false;
  if (value.total.limited) return value.kinds === undefined;
  if (
    value.due.limited ||
    (value.due.value as number) > (value.total.value as number)
  )
    return false;
  if (value.kinds === undefined) return value.total.value === 0;
  if (!Array.isArray(value.kinds) || value.kinds.length > 4) return false;
  const seen = new Set();
  let total = 0;
  for (const row of value.kinds) {
    if (
      !object(row) ||
      !choice(row.kind, jobKinds) ||
      seen.has(row.kind) ||
      !Array.isArray(row.states)
    )
      return false;
    seen.add(row.kind);
    const sum = (row.states as unknown[]).reduce<number>(
      (n, item) =>
        n + (object(item) && typeof item.count === "number" ? item.count : NaN),
      0,
    );
    if (!distribution(row.states, Object.keys(jobStates), sum)) return false;
    total += sum;
  }
  return total === value.total.value;
}
