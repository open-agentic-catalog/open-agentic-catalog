import type { components } from "../../src/api/schema";
import {
  sourceStates,
  syncStates,
} from "../../src/views/operations-overview-values";
import { jobStates } from "../../src/views/job-state";
const scope = {
  tenantId: "tenant-a",
  scope: "current_access" as const,
  observedAt: "2026-10-25T12:00:00Z",
  selectionLimit: 10000,
};
export function sourcesFixture(): components["schemas"]["SourceOverview"] {
  return {
    ...scope,
    total: { value: 3, limited: false },
    states: sourceStates.map((state) => ({ state, count: 1 })),
    sync: syncStates.map((state) => ({
      state,
      count: ["failed", "not_applicable", "not_yet_synced"].includes(state)
        ? 1
        : 0,
    })),
    historicalComplete: 1,
    problems: [
      {
        sourceId: "00000000-0000-7000-8000-000000000010",
        displayName: "Example source",
        revision: 2,
        latest: {
          revision: 2,
          state: "failed",
          completedAt: "2026-10-25T10:00:00Z",
        },
        lastComplete: {
          revision: 1,
          state: "complete",
          completedAt: "2026-10-24T10:00:00Z",
        },
      },
    ],
  };
}
export function jobsFixture(): components["schemas"]["JobOverview"] {
  return {
    ...scope,
    total: { value: 2, limited: false },
    due: { value: 1, limited: false },
    oldestDueAt: "2026-10-25T10:00:00Z",
    kinds: [
      {
        kind: "source.sync",
        states: Object.keys(jobStates).map((state) => ({
          state: state as keyof typeof jobStates,
          count: ["queued", "failed"].includes(state) ? 1 : 0,
        })),
      },
    ],
  };
}
