import { expect, it } from "vitest";
import { processSample } from "../../tests/unit/fixtures";
import { isBuildInfo, isProcessInfo, isUIProcessInfo } from "./build-info";

it("admits independent bounded build identity and every fixed source state", () => {
  for (const sourceState of ["clean", "modified", "unknown"])
    expect(
      isBuildInfo({
        version: "0.1.0-rc.1.dev+gabc",
        commit: "a".repeat(40),
        sourceState,
      }),
    ).toBe(true);
});

it("rejects incomplete, malformed and oversized identities", () => {
  const valid = { version: "dev", commit: "unknown", sourceState: "unknown" };
  for (const bad of [
    null,
    [],
    {},
    { ...valid, sourceState: "dirty" },
    { ...valid, sourceState: null },
    { ...valid, version: "" },
    { ...valid, commit: "a".repeat(129) },
    { ...valid, version: "x\n" },
    { ...valid, commit: "<script>" },
    { ...valid, version: 12 },
  ])
    expect(isBuildInfo(bad)).toBe(false);
});

it("accepts independent UTC observations and elapsed uptime after a clock correction", () => {
  expect(
    isProcessInfo({ ...processSample, observedAt: "2026-09-01T11:00:00Z" }),
  ).toBe(true);
  expect(
    isUIProcessInfo({
      ...processSample,
      role: "ui",
      version: "dev",
      commit: "unknown",
      sourceState: "modified",
    }),
  ).toBe(true);
});
it.each([
  { instanceId: "../private" },
  { instanceId: "" },
  { instanceId: null },
  { startedAt: "invalid" },
  { observedAt: "invalid" },
  { startedAt: null },
  { uptimeSeconds: -1 },
  { uptimeSeconds: 1.5 },
  { uptimeSeconds: Infinity },
  { uptimeSeconds: Number.MAX_SAFE_INTEGER + 1 },
  { uptimeSeconds: "1" },
])("rejects malformed consumed process fields: %j", (patch) => {
  expect(isProcessInfo({ ...processSample, ...patch })).toBe(false);
});
it.each([
  null,
  [],
  {},
  { ...processSample, role: "api" },
  {
    ...processSample,
    role: "ui",
    version: "dev",
    commit: "unknown",
    sourceState: "private",
  },
])("rejects incomplete or misidentified UI status: %j", (value) => {
  expect(isUIProcessInfo(value)).toBe(false);
});
