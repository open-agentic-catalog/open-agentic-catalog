import { expect, it } from "vitest";
import {
  reviewFixture,
  publicationFixture,
} from "../../tests/unit/overview-fixtures";
import { reviewOverview, publicationOverview } from "./review-overview-values";

it("accepts complete review populations and keeps incomplete populations independent", () => {
  const valid = reviewFixture();
  expect(reviewOverview(valid, "tenant-a")).toBe(true);
  const limited = { value: 10000, limited: true };
  expect(
    reviewOverview(
      {
        ...valid,
        pending: limited,
        pendingAge: undefined,
        pendingAgeUnavailable: "limited",
      },
      "tenant-a",
    ),
  ).toBe(true);
  expect(
    reviewOverview(
      {
        ...valid,
        pendingAge: undefined,
        pendingAgeUnavailable: "invalid_timestamps",
      },
      "tenant-a",
    ),
  ).toBe(true);
  expect(
    reviewOverview(
      {
        ...valid,
        submissions: limited,
        days: valid.days.map(({ submitted: _submitted, ...day }) => day),
      },
      "tenant-a",
    ),
  ).toBe(true);
  expect(
    reviewOverview(
      {
        ...valid,
        decisions: limited,
        medianSeconds: undefined,
        medianUnavailable: "limited",
        days: valid.days.map((day) => ({
          ...day,
          approved: undefined,
          changesRequired: undefined,
          rejected: undefined,
        })),
      },
      "tenant-a",
    ),
  ).toBe(true);
  expect(
    reviewOverview(
      {
        ...valid,
        medianSeconds: undefined,
        medianUnavailable: "invalid_timestamps",
      },
      "tenant-a",
    ),
  ).toBe(true);
  expect(
    reviewOverview(
      {
        ...valid,
        decisions: { value: 0, limited: false },
        medianSeconds: undefined,
        days: valid.days.map((day) => ({
          ...day,
          approved: 0,
          changesRequired: 0,
          rejected: 0,
        })),
      },
      "tenant-a",
    ),
  ).toBe(true);
});

it("rejects malformed, inconsistent or incomplete consumed review values", () => {
  const valid = reviewFixture();
  for (const patch of [
    { tenantId: "other" },
    { pending: null },
    { approvedUnreleased: null },
    { submissions: null },
    { decisions: null },
    { startAt: "bad" },
    { endAt: "bad" },
    { endAt: "2026-10-25T13:00:00Z" },
    { startAt: "2026-09-25T00:00:00Z" },
    { days: null },
    { days: [] },
    { days: [null, ...valid.days.slice(1)] },
    {
      days: [{ ...valid.days[0], date: "2026-09-25" }, ...valid.days.slice(1)],
    },
    { days: [{ ...valid.days[0], partial: true }, ...valid.days.slice(1)] },
    { days: [{ ...valid.days[0], submitted: -1 }, ...valid.days.slice(1)] },
    { days: [{ ...valid.days[0], approved: -1 }, ...valid.days.slice(1)] },
    { submissions: { value: 2, limited: false } },
    { decisions: { value: 2, limited: false } },
    { oldestPendingAt: undefined },
    { pending: { value: 0, limited: false } },
    { pendingAge: null },
    { pendingAge: { ...valid.pendingAge, under24Hours: -1 } },
    { pendingAge: { ...valid.pendingAge, under24Hours: 2 } },
    { pendingAgeUnavailable: "other" },
    { pendingAgeUnavailable: "invalid_timestamps" },
    { pending: { value: 10000, limited: true } },
    { submissions: { value: 10000, limited: true } },
    { decisions: { value: 10000, limited: true } },
    { medianSeconds: undefined },
    { medianSeconds: -1 },
    { medianSeconds: Infinity },
    { medianSeconds: Number.MAX_SAFE_INTEGER + 1 },
    { medianSeconds: "1" },
    { medianUnavailable: "unknown" },
    { medianUnavailable: "invalid_timestamps" },
  ])
    expect(reviewOverview({ ...valid, ...patch }, "tenant-a")).toBe(false);
  const limited = {
    ...valid,
    decisions: { value: 10000, limited: true },
    medianSeconds: undefined,
    medianUnavailable: "limited",
    days: valid.days.map((day) => ({
      ...day,
      approved: undefined,
      changesRequired: undefined,
      rejected: undefined,
    })),
  };
  expect(
    reviewOverview({ ...limited, medianUnavailable: undefined }, "tenant-a"),
  ).toBe(false);
  expect(reviewOverview({ ...limited, medianSeconds: 1 }, "tenant-a")).toBe(
    false,
  );
});

it("checks host identity, private metadata, status and mismatch labels", () => {
  const valid = publicationFixture();
  expect(publicationOverview(valid, "tenant-a")).toBe(true);
  expect(publicationOverview({ ...valid, hosts: [] }, "tenant-a")).toBe(true);
  for (const patch of [
    { tenantId: "other" },
    { hosts: null },
    { hosts: Array(65).fill(valid.hosts[0]) },
    { hosts: [valid.hosts[0], valid.hosts[0]] },
  ])
    expect(publicationOverview({ ...valid, ...patch }, "tenant-a")).toBe(false);
  for (const host of [
    null,
    { ...valid.hosts[0], host: "" },
    { ...valid.hosts[0], host: "x/evil" },
    { ...valid.hosts[0], host: "x".repeat(254) },
    { ...valid.hosts[0], mode: "other" },
    { ...valid.hosts[0], status: "other" },
    { ...valid.hosts[0], mismatches: null },
    { ...valid.hosts[0], mismatches: ["other"] },
    { ...valid.hosts[0], mismatches: ["audience", "audience"] },
    { ...valid.hosts[0], mismatches: Array(4).fill("audience") },
    { ...valid.hosts[0], mode: "anonymous" },
    { ...valid.hosts[0], active: valid.hosts[2]!.active },
    { ...valid.hosts[1], mode: "disabled" },
    { ...valid.hosts[1], mismatches: ["audience"] },
    { ...valid.hosts[1], active: valid.hosts[2]!.active },
    { ...valid.hosts[2], active: {} },
    {
      ...valid.hosts[2],
      active: { ...valid.hosts[2]!.active, tenantId: "other" },
    },
    { ...valid.hosts[2], active: { ...valid.hosts[2]!.active, host: "other" } },
    {
      ...valid.hosts[2],
      active: { ...valid.hosts[2]!.active, state: "retired" },
    },
    { ...valid.hosts[2], mismatches: [] },
  ])
    expect(publicationOverview({ ...valid, hosts: [host] }, "tenant-a")).toBe(
      false,
    );
  expect(
    publicationOverview(
      {
        ...valid,
        hosts: [{ ...valid.hosts[2], mode: "anonymous", mismatches: [] }],
      },
      "tenant-a",
    ),
  ).toBe(true);
  expect(
    publicationOverview(
      { ...valid, hosts: [{ ...valid.hosts[0], host: "[::1]:8443" }] },
      "tenant-a",
    ),
  ).toBe(true);
});
