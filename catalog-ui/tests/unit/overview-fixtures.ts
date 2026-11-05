import type { components } from "../../src/api/schema";

export function reviewFixture(
  tenantId = "tenant-a",
): components["schemas"]["ReviewOverview"] {
  return {
    tenantId,
    scope: "current_access",
    observedAt: "2026-10-25T12:00:00Z",
    selectionLimit: 10000,
    pending: { value: 2, limited: false },
    approvedUnreleased: { value: 1, limited: false },
    oldestPendingAt: "2026-10-24T12:00:00Z",
    pendingAge: { under24Hours: 1, under7Days: 1, atLeast7Days: 0 },
    startAt: "2026-09-26T00:00:00Z",
    endAt: "2026-10-25T12:00:00Z",
    submissions: { value: 3, limited: false },
    decisions: { value: 3, limited: false },
    medianSeconds: 3600,
    days: Array.from({ length: 30 }, (_, index) => ({
      date: new Date(Date.UTC(2026, 8, 26 + index)).toISOString().slice(0, 10),
      partial: index === 29,
      submitted: index === 29 ? 3 : 0,
      approved: index === 29 ? 1 : 0,
      changesRequired: index === 29 ? 1 : 0,
      rejected: index === 29 ? 1 : 0,
    })),
  };
}

export function publicationFixture(
  tenantId = "tenant-a",
): components["schemas"]["PublicationOverview"] {
  return {
    tenantId,
    scope: "current_access",
    observedAt: "2026-10-25T12:00:00Z",
    selectionLimit: 64,
    hosts: [
      {
        host: "disabled.example",
        mode: "disabled",
        status: "disabled",
        mismatches: [],
      },
      {
        host: "empty.example",
        mode: "anonymous",
        status: "unpublished",
        mismatches: [],
      },
      {
        host: "catalog.example",
        mode: "authenticated",
        status: "recorded",
        mismatches: ["audience", "ard_profile", "ai_catalog_profile"],
        active: {
          id: "snapshot-1",
          tenantId,
          host: "catalog.example",
          audience: "anonymous",
          generation: 3,
          ardProfile: "ard-old",
          aiCatalogProfile: "ai-old",
          state: "active",
          etag: '"digest"',
          createdAt: "2026-10-24T12:00:00Z",
          activatedAt: "2026-10-24T12:01:00Z",
        },
      },
    ],
  };
}
