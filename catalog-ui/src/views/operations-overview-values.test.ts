import { expect, it } from "vitest";
import {
  sourcesFixture,
  jobsFixture,
} from "../../tests/unit/operations-fixtures";
import { sourceOverview, jobOverview } from "./operations-overview-values";

it("rejects an overflowing due subset when the complete retained population fits the limit", () => {
  const value = jobsFixture();
  value.total.value = 10000;
  value.due.value = 10000;
  value.kinds![0]!.states.forEach(
    (row) => (row.count = row.state === "queued" ? 10000 : 0),
  );
  expect(jobOverview(value, "tenant-a")).toBe(true);
  value.due.limited = true;
  expect(jobOverview(value, "tenant-a")).toBe(false);
});
it("rejects a Job state count with the wrong JSON type", () => {
  const value = jobsFixture();
  value.kinds![0]!.states[0]!.count = "one" as unknown as number;
  expect(jobOverview(value, "tenant-a")).toBe(false);
});

it.each([
  { total: null },
  { total: { value: -1, limited: false } },
  { selectionLimit: 5 },
  { problems: null },
  { problems: Array(6).fill({}) },
  { states: [] },
  { sync: [] },
  { historicalComplete: 4 },
  { historicalComplete: -1 },
  {
    states: [
      { state: "active", count: 3 },
      { state: "active", count: 0 },
      { state: "error", count: 0 },
    ],
  },
  { sync: [{ state: "unknown", count: 3 }] },
  { total: { value: 10000, limited: true } },
])("rejects invalid Source populations %j", (change) => {
  expect(sourceOverview({ ...sourcesFixture(), ...change }, "tenant-a")).toBe(
    false,
  );
});
it.each([
  { sourceId: "../invalid" },
  { displayName: 7 },
  { displayName: "x".repeat(161) },
  { revision: 0 },
  { latest: null },
  {
    latest: {
      revision: 1,
      state: "failed",
      completedAt: "2026-10-25T10:00:00Z",
    },
  },
  {
    latest: {
      revision: 2,
      state: "running",
      completedAt: "2026-10-25T10:00:00Z",
    },
  },
  { lastComplete: null },
  {
    lastComplete: {
      revision: 3,
      state: "complete",
      completedAt: "2026-10-24T10:00:00Z",
    },
  },
])("rejects invalid Source problem metadata %j", (change) => {
  const value = sourcesFixture();
  Object.assign(value.problems[0]!, change);
  expect(sourceOverview(value, "tenant-a")).toBe(false);
});
it("accepts complete empty and capped Source sets with an independent problem list", () => {
  const empty = sourcesFixture();
  empty.total.value = 0;
  empty.states!.forEach((r) => (r.count = 0));
  empty.sync!.forEach((r) => (r.count = 0));
  empty.historicalComplete = 0;
  empty.problems = [];
  expect(sourceOverview(empty, "tenant-a")).toBe(true);
  const limited = sourcesFixture();
  limited.total = { value: 10000, limited: true };
  delete limited.states;
  delete limited.sync;
  delete limited.historicalComplete;
  expect(sourceOverview(limited, "tenant-a")).toBe(true);
  delete limited.problems[0]!.lastComplete;
  expect(sourceOverview(limited, "tenant-a")).toBe(true);
});
it.each([
  { due: null },
  { due: { value: -1, limited: false } },
  { oldestDueAt: undefined },
  { oldestDueAt: "bad" },
  { oldestDueAt: "2026-10-26T00:00:00Z" },
  { total: null },
  { total: { value: 10000, limited: true } },
  { kinds: null },
  { kinds: [] },
  { kinds: Array(5).fill({}) },
  { kinds: [{ kind: "unknown", states: [] }] },
  { kinds: [{ kind: "source.sync", states: null }] },
  { kinds: [{ kind: "source.sync", states: [] }] },
])("rejects invalid Job populations %j", (change) => {
  expect(jobOverview({ ...jobsFixture(), ...change }, "tenant-a")).toBe(false);
});
it("checks Job totals, state uniqueness and independently bounded due work", () => {
  const duplicate = jobsFixture();
  duplicate.kinds!.push(duplicate.kinds![0]!);
  expect(jobOverview(duplicate, "tenant-a")).toBe(false);
  const state = jobsFixture();
  state.kinds![0]!.states[0]!.count = -1;
  expect(jobOverview(state, "tenant-a")).toBe(false);
  const empty = jobsFixture();
  empty.total.value = 0;
  empty.due.value = 0;
  delete empty.oldestDueAt;
  delete empty.kinds;
  expect(jobOverview(empty, "tenant-a")).toBe(true);
  empty.kinds = [];
  expect(jobOverview(empty, "tenant-a")).toBe(true);
  empty.oldestDueAt = "2026-10-25T10:00:00Z";
  expect(jobOverview(empty, "tenant-a")).toBe(false);
  const limited = jobsFixture();
  limited.total = { value: 10000, limited: true };
  delete limited.kinds;
  limited.due = { value: 10000, limited: true };
  expect(jobOverview(limited, "tenant-a")).toBe(true);
});
