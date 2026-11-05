import { expect, it } from "vitest";
import { activityFixture } from "../../tests/unit/activity-fixture";
import {
  activity,
  counter,
  completed,
  duration,
  mean,
  countText,
  interval,
  type Activity,
} from "./activity-values";

it("keeps canonical uint64 counters exact and rejects malformed numeric input", () => {
  for (const value of ["0", "9007199254740993", "18446744073709551615"])
    expect(counter(value)).toBe(true);
  for (const value of [
    "",
    "00",
    "01",
    "-1",
    "1.5",
    "1e3",
    "18446744073709551616",
    1,
    null,
    undefined,
    "9".repeat(100),
  ])
    expect(counter(value)).toBe(false);
  const sample = activityFixture(9007199254740993n);
  expect(activity(sample)).toBe(true);
  expect(completed(sample)).toBe(9007199254740993n);
  expect(countText(sample.http.completed)).toBe(
    9007199254740993n.toLocaleString(),
  );
  expect(duration(sample)).toBe(0.04);
  expect(mean(1, 0n)).toBeUndefined();
  expect(mean(1, 2n)).toBe(500);
  expect(mean(Number.MAX_VALUE, 1n)).toBeUndefined();
});
it("checks fixed cardinalities, consumed metadata, exact partitions and finite duration values", () => {
  expect(activity(activityFixture())).toBe(true);
  for (const value of [
    null,
    [],
    {},
    { ...activityFixture(), schema: "unknown" },
    { ...activityFixture(), population: "unknown" },
    { ...activityFixture(), scope: "fleet" },
    { ...activityFixture(), source: "prometheus" },
    { ...activityFixture(), instanceId: "bad" },
    { ...activityFixture(), role: "worker" },
    { ...activityFixture(), startedAt: "bad" },
    { ...activityFixture(), observedAt: "bad" },
    { ...activityFixture(), elapsedSeconds: -1 },
    { ...activityFixture(), elapsedSeconds: Infinity },
    { ...activityFixture(), http: null },
    { ...activityFixture(), controlSearch: null },
    { ...activityFixture(), mcp: null },
  ])
    expect(activity(value)).toBe(false);
  const mutations: ((v: Activity) => void)[] = [
    (v) => {
      v.http.completed = "10";
    },
    (v) => {
      v.http.activeCatalog = "01";
    },
    (v) => {
      v.http.families.pop();
    },
    (v) => {
      v.http.families.reverse();
    },
    (v) => {
      v.http.families[0]!.methods.pop();
    },
    (v) => {
      v.http.families[0]!.methods.reverse();
    },
    (v) => {
      v.http.families[0]!.statusClasses.pop();
    },
    (v) => {
      v.http.families[0]!.statusClasses.reverse();
    },
    (v) => {
      v.http.families[0]!.methods[0]!.completed = "5";
    },
    (v) => {
      v.http.families[0]!.statusClasses[1]!.completed = "5";
    },
    (v) => {
      v.http.families[0]!.durationSeconds = NaN;
    },
    (v) => {
      v.http.families[0]!.durationSeconds = -1;
    },
    (v) => {
      v.http.families[0]!.durationSeconds = Infinity;
    },
    (v) => {
      v.http.families.forEach(
        (row) => (row.durationSeconds = Number.MAX_VALUE),
      );
    },
    (v) => {
      v.controlSearch.outcomes.pop();
    },
    (v) => {
      v.controlSearch.outcomes[0]!.completed = "-1";
    },
    (v) => {
      v.mcp.outcomes.pop();
    },
    (v) => {
      v.mcp.outcomes.reverse();
    },
    (v) => {
      v.mcp.outcomes[0]!.completed = "bad";
    },
    (v) => {
      Object.assign(v.mcp, { enabled: "true" });
    },
  ];
  for (const mutate of mutations) {
    const sample = activityFixture();
    mutate(sample);
    expect(activity(sample)).toBe(false);
  }
});
it("calculates exact deltas before deriving interval rates and weighted means", () => {
  const before = activityFixture(9007199254740993n, 10);
  const after = activityFixture(9007199254740995n, 20);
  after.http.families[0]!.durationSeconds = 0.05;
  expect(interval(before, after)?.rate).toBe(0.2);
  expect(interval(before, after)?.latency).toBeCloseTo(5);
  expect(interval(before, after)?.elapsed).toBe(10);
  // One 10 ms request and nine 30 ms requests yield 28 ms, not 20 ms.
  const weighted = activityFixture(1n);
  weighted.http.completed = "10";
  weighted.http.families[0]!.durationSeconds = 0.01;
  const second = weighted.http.families[1]!;
  second.completed = "9";
  second.durationSeconds = 0.27;
  second.methods[0]!.completed = "9";
  second.statusClasses[1]!.completed = "9";
  expect(activity(weighted)).toBe(true);
  expect(mean(duration(weighted), completed(weighted))).toBeCloseTo(28);

  after.observedAt = "2026-10-01T00:00:00Z";
  expect(interval(before, after)?.rate).toBe(0.2);
  expect(interval(activityFixture(), activityFixture(4n, 20))).toEqual({
    elapsed: 10,
    rate: 0,
    latency: undefined,
  });
});
it("refuses restarts, replica changes, incompatible populations, stale gaps and decreases", () => {
  const before = activityFixture();
  expect(interval(undefined, before)).toBeUndefined();
  const mutations: ((v: Activity) => void)[] = [
    (v) => {
      v.instanceId = "00000000-0000-7000-8000-000000000002";
    },
    (v) => {
      v.startedAt = "2026-10-01T00:00:00Z";
    },
    (v) => {
      v.role = "all";
    },
    (v) => {
      Object.assign(v, { schema: "future" });
    },
    (v) => {
      Object.assign(v, { population: "future" });
    },
    (v) => {
      Object.assign(v, { scope: "other" });
    },
    (v) => {
      Object.assign(v, { source: "other" });
    },
    (v) => {
      v.mcp.enabled = true;
    },
    (v) => {
      v.elapsedSeconds = 10;
    },
    (v) => {
      v.elapsedSeconds = 10.5;
    },
    (v) => {
      v.elapsedSeconds = 41;
    },
    (v) => {
      v.http.completed = "0";
    },
    (v) => {
      v.http.families[0]!.durationSeconds = 0;
    },
  ];
  for (const mutate of mutations) {
    const next = activityFixture(5n, 20);
    mutate(next);
    expect(interval(before, next)).toBeUndefined();
  }
  const previous = activityFixture();
  previous.controlSearch.outcomes[0]!.completed = "1";
  expect(interval(previous, activityFixture(5n, 20))).toBeUndefined();
  previous.controlSearch.outcomes[0]!.completed = "0";
  previous.mcp.outcomes[0]!.completed = "1";
  expect(interval(previous, activityFixture(5n, 20))).toBeUndefined();
});

it("rejects impossible duration deltas and unrepresentable derived means", () => {
  const before = activityFixture(4n, 10),
    after = activityFixture(4n, 20);
  after.http.families[0]!.durationSeconds = 0.05;
  expect(interval(before, after)).toBeUndefined();
  const next = activityFixture(5n, 20);
  next.http.families[0]!.durationSeconds = Number.MAX_VALUE;
  expect(interval(before, next)).toBeUndefined();
  next.http.families[0]!.durationSeconds = NaN;
  expect(interval(before, next)).toBeUndefined();
});
