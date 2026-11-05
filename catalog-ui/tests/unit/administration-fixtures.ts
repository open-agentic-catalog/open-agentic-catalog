import type { components } from "../../src/api/schema";
const scope = {
  tenantId: "tenant-a",
  scope: "current_access" as const,
  observedAt: "2026-10-25T12:00:00Z",
  selectionLimit: 10000,
};
export function administrationFixture(): components["schemas"]["AdministrationOverview"] {
  return {
    ...scope,
    human: {
      total: { value: 5, limited: false },
      states: [
        { state: "active", count: 3 },
        { state: "suspended", count: 1 },
        { state: "offboarded", count: 1 },
      ],
    },
    service: {
      total: { value: 2, limited: false },
      states: [
        { state: "active", count: 1 },
        { state: "suspended", count: 0 },
        { state: "offboarded", count: 1 },
      ],
    },
    activeGroups: { value: 3, limited: false },
    activeRoles: {
      total: { value: 18, limited: false },
      builtin: 14,
      custom: 4,
    },
    activeAssignments: { value: 8, limited: false },
  };
}
export function feedbackFixture(): components["schemas"]["FeedbackOverview"] {
  return {
    ...scope,
    open: { value: 6, limited: false },
    categories: [
      { category: "problem", count: 3 },
      { category: "documentation", count: 1 },
      { category: "suggestion", count: 1 },
      { category: "positive", count: 1 },
      { category: "other", count: 0 },
    ],
    oldestOpenAt: "2026-10-20T10:00:00Z",
  };
}
export function ratingFixture(): components["schemas"]["RatingOverview"] {
  return {
    ...scope,
    total: { value: 5, limited: false },
    states: [
      { state: "active", count: 3 },
      { state: "withdrawn", count: 1 },
      { state: "removed", count: 1 },
    ],
  };
}
