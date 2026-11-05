import { object, number, timestamp } from "../api/values";
import { scope, boundedCount } from "./overview-values";
import { distribution } from "./operations-overview-values";

export const memberStates = ["active", "suspended", "offboarded"] as const;
export const ratingStates = ["active", "withdrawn", "removed"] as const;
export const feedbackCategories = {
  problem: "Problem",
  documentation: "Documentation correction",
  suggestion: "Suggestion",
  positive: "Positive feedback",
  other: "Other",
};

function members(value: unknown): boolean {
  return (
    object(value) &&
    object(value.total) &&
    boundedCount(value.total) &&
    (value.total.limited
      ? value.states === undefined
      : distribution(value.states, memberStates, value.total.value as number))
  );
}
export function administrationOverview(
  value: unknown,
  tenant: string,
): boolean {
  if (
    !scope(value, tenant) ||
    !members(value.human) ||
    !members(value.service) ||
    !boundedCount(value.activeGroups) ||
    !boundedCount(value.activeAssignments) ||
    !object(value.activeRoles) ||
    !object(value.activeRoles.total) ||
    !boundedCount(value.activeRoles.total)
  )
    return false;
  const roles = value.activeRoles;
  return (roles.total as Record<string, unknown>).limited
    ? roles.builtin === undefined && roles.custom === undefined
    : number(roles.builtin, 0, 10000) &&
        number(roles.custom, 0, 10000) &&
        (roles.builtin as number) + (roles.custom as number) ===
          (roles.total as Record<string, unknown>).value;
}
export function feedbackOverview(value: unknown, tenant: string): boolean {
  if (!scope(value, tenant) || !object(value.open) || !boundedCount(value.open))
    return false;
  if (
    value.open.value === 0
      ? value.oldestOpenAt !== undefined
      : !timestamp(value.oldestOpenAt)
  )
    return false;
  return value.open.limited
    ? value.categories === undefined
    : Array.isArray(value.categories) &&
        distribution(
          value.categories.map((row: unknown) =>
            object(row) ? { state: row.category, count: row.count } : undefined,
          ),
          Object.keys(feedbackCategories),
          value.open.value as number,
        );
}
export function ratingOverview(value: unknown, tenant: string): boolean {
  return (
    scope(value, tenant) &&
    object(value.total) &&
    boundedCount(value.total) &&
    (value.total.limited
      ? value.states === undefined
      : distribution(value.states, ratingStates, value.total.value as number))
  );
}
