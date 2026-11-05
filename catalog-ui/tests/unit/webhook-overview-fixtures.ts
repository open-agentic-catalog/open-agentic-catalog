import type { components } from "../../src/api/schema";
const scope = {
  tenantId: "tenant-a",
  scope: "current_access" as const,
  observedAt: "2026-10-25T12:00:00Z",
  selectionLimit: 10000,
};
export function endpointOverviewFixture(): components["schemas"]["WebhookEndpointOverview"] {
  return {
    ...scope,
    total: { value: 3, limited: false },
    states: [
      { state: "active", count: 2 },
      { state: "disabled", count: 1 },
    ],
  };
}
export function deliveryOverviewFixture(): components["schemas"]["WebhookDeliveryOverview"] {
  return {
    ...scope,
    total: { value: 10, limited: false },
    states: [
      { state: "pending", count: 1 },
      { state: "waiting", count: 2 },
      { state: "succeeded", count: 6 },
      { state: "dead_letter", count: 1 },
    ],
    failedEndpoints: [
      {
        endpointId: "00000000-0000-7000-8000-000000000010",
        name: "Publication receiver",
        deliveryId: "00000000-0000-7000-9000-000000000010",
        state: "waiting",
        attemptedAt: "2026-10-25T11:00:00Z",
      },
    ],
  };
}
export function notificationsFixture(): components["schemas"]["WebhookReconciliation"] {
  return {
    pendingEvents: 1000,
    pendingEventLimit: 1000,
    capacityLossCount: 5,
    deliveryLossCount: 2,
    lastCapacityLossAt: "2026-10-24T11:00:00Z",
    lastDeliveryLossAt: "2026-10-25T11:00:00Z",
  };
}
