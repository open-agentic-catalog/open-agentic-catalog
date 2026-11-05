import { object, identifier, timestamp, choice } from "../api/values";
import { boundedCount, scope } from "./overview-values";
import { distribution } from "./operations-overview-values";

export const endpointStates = ["active", "disabled"] as const;
export const deliveryStates = [
  "pending",
  "waiting",
  "succeeded",
  "dead_letter",
] as const;

function summary(
  value: unknown,
  tenant: string,
  states: readonly string[],
): value is Record<string, unknown> & {
  total: { value: number; limited: boolean };
} {
  return (
    scope(value, tenant) &&
    object(value.total) &&
    boundedCount(value.total) &&
    (value.total.limited
      ? value.states === undefined
      : distribution(value.states, states, value.total.value as number))
  );
}

export function webhookEndpointOverview(
  value: unknown,
  tenant: string,
): boolean {
  return summary(value, tenant, endpointStates);
}

export function webhookDeliveryOverview(
  value: unknown,
  tenant: string,
): boolean {
  if (
    !summary(value, tenant, deliveryStates) ||
    !Array.isArray(value.failedEndpoints) ||
    value.failedEndpoints.length > 5 ||
    (!value.total.limited && value.failedEndpoints.length > value.total.value)
  )
    return false;
  const seen = new Set();
  return value.failedEndpoints.every((row) => {
    if (
      !object(row) ||
      !identifier(row.endpointId) ||
      !identifier(row.deliveryId) ||
      !choice(row.state, ["waiting", "dead_letter"]) ||
      seen.has(row.endpointId) ||
      typeof row.name !== "string" ||
      row.name.length === 0 ||
      [...row.name].length > 160 ||
      !timestamp(row.attemptedAt)
    )
      return false;
    seen.add(row.endpointId);
    return true;
  });
}
