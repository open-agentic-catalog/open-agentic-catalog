import type { Activity } from "../../src/views/activity-values";
import {
  families,
  methods,
  statuses,
  searchOutcomes,
  mcpOperations,
  mcpOutcomes,
} from "../../src/views/activity-values";
export function activityFixture(count = 4n, elapsed = 10): Activity {
  return {
    schema: "service-activity-v1",
    population: "catalog-http-v1",
    scope: "responding_api_process",
    source: "process_memory",
    instanceId: "00000000-0000-7000-8000-000000000001",
    role: "api",
    startedAt: "2026-11-01T12:00:00Z",
    observedAt: "2026-11-01T12:01:00Z",
    elapsedSeconds: elapsed,
    http: {
      completed: String(count),
      activeCatalog: "0",
      families: families.map((family, index) => ({
        family,
        completed: index === 0 ? String(count) : "0",
        durationSeconds: index === 0 ? 0.04 : 0,
        methods: methods.map((method, i) => ({
          method,
          completed: index === 0 && i === 0 ? String(count) : "0",
        })),
        statusClasses: statuses.map((statusClass, i) => ({
          statusClass,
          completed: index === 0 && i === 1 ? String(count) : "0",
        })),
      })),
    },
    controlSearch: {
      outcomes: searchOutcomes.map((outcome) => ({ outcome, completed: "0" })),
    },
    mcp: {
      enabled: false,
      outcomes: mcpOperations.flatMap((operation) =>
        mcpOutcomes.map((outcome) => ({ operation, outcome, completed: "0" })),
      ),
    },
  };
}
