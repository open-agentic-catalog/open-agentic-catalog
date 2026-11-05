import { expect, it } from "vitest";
import {
  settingsDraft,
  settingsOverrides,
  settingsChanges,
  settingText,
} from "./settings-policy";
import type { components } from "../api/schema";

const defaults = {
  automaticSourceSyncEnabled: true,
  automaticSourceSyncInterval: "15m0s",
  newJobMaxAttempts: 5,
};
const base = {
  tenantId: "tenant-a",
  revision: 2,
  overrides: {},
  deploymentDefaults: defaults,
  effective: defaults,
};
it("refuses unsupported mode selections instead of silently treating them as overrides or inheritance", () => {
  expect(() =>
    settingsOverrides({ ...settingsDraft({}), enabled: "invalid" }),
  ).toThrow("Choose Inherit");
  expect(() =>
    settingsOverrides({
      ...settingsDraft({}),
      intervalMode: "invalid" as "inherit",
    }),
  ).toThrow("Choose Inherit");
  expect(() =>
    settingsOverrides({
      ...settingsDraft({}),
      attemptsMode: "invalid" as "inherit",
    }),
  ).toThrow("Choose Inherit");
});
it.each(Array.from({ length: 8 }, (_, i) => i))(
  "retains exactly the present overrides for mask %i",
  (mask) => {
    const overrides: components["schemas"]["TenantSettingsOverrides"] = {};
    if (mask & 1) overrides.automaticSourceSyncEnabled = false;
    if (mask & 2) overrides.automaticSourceSyncInterval = "1m30.123456789s";
    if (mask & 4) overrides.newJobMaxAttempts = 5;
    expect(settingsOverrides(settingsDraft(overrides))).toEqual(overrides);
    const draft = settingsDraft(overrides);
    draft.intervalMode = "inherit";
    draft.interval = "invalid discarded input";
    const { automaticSourceSyncInterval: _discarded, ...remaining } = overrides;
    expect(settingsOverrides(draft)).toEqual(remaining);
  },
);
it.each([
  "1m",
  "24h",
  "+60s",
  "1m30.123456789s",
  ".5h",
  "1.m",
  "1m0µs0μs",
  "60.0000000001s",
  "59.999999999999999999s",
  "59s999.999999999999999999ms",
  "1m0.999999999999999999999s",
  "1m0.9223372036854775809s",
])("preserves exact valid duration %s", (interval) => {
  expect(
    settingsOverrides({
      ...settingsDraft({}),
      intervalMode: "override",
      interval,
    }),
  ).toEqual({ automaticSourceSyncInterval: interval });
});
it.each([
  "",
  "1",
  "soon",
  "59.999999999s",
  "24h1ns",
  "24h0.999999999999999999ns",
  "-1m",
  "1m ",
  "1m!",
  "9".repeat(64) + "h",
  "1m" + "0µs".repeat(22),
])("rejects invalid duration %s before preparation", (interval) => {
  expect(() =>
    settingsOverrides({
      ...settingsDraft({}),
      intervalMode: "override",
      interval,
    }),
  ).toThrow();
});
it.each(["", "0", "21", "1.5", "NaN", "1e1", "-1"])(
  "rejects invalid attempts %s",
  (attempts) => {
    expect(() =>
      settingsOverrides({
        ...settingsDraft({}),
        attemptsMode: "override",
        attempts,
      }),
    ).toThrow("1 to 20");
  },
);
it("compares inheritance separately from explicit false or a value equal to defaults", () => {
  const changes = settingsChanges(base, {
    automaticSourceSyncEnabled: false,
    newJobMaxAttempts: 5,
  });
  expect(changes).toEqual([
    {
      key: "automaticSourceSyncEnabled",
      label: "Automatic source sync",
      before: "Inherit deployment default",
      after: "Paused",
      effectiveBefore: "Enabled",
      effectiveAfter: "Paused",
    },
    {
      key: "newJobMaxAttempts",
      label: "New Job maximum attempts",
      before: "Inherit deployment default",
      after: "5",
      effectiveBefore: "5",
      effectiveAfter: "5",
    },
  ]);
  expect(
    settingsChanges(
      {
        ...base,
        overrides: { automaticSourceSyncEnabled: false },
        deploymentDefaults: { ...defaults, automaticSourceSyncEnabled: false },
      },
      {},
    ),
  ).toEqual([
    {
      key: "automaticSourceSyncEnabled",
      label: "Automatic source sync",
      before: "Paused",
      after: "Inherit deployment default",
      effectiveBefore: "Paused",
      effectiveAfter: "Paused",
    },
  ]);
  expect(settingsChanges(base, {})).toEqual([]);
  expect(settingText(undefined)).toBe("Inherit deployment default");
});
