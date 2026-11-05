import type { components } from "../api/schema";
import { atField, characterText } from "./input-validation";

type Overrides = components["schemas"]["TenantSettingsOverrides"];
type SettingsValue = components["schemas"]["TenantSettings"];
export type SettingsDraft = {
  enabled: string;
  interval: string;
  attempts: string;
  intervalMode: "inherit" | "override";
  attemptsMode: "inherit" | "override";
};
export const settingLabels = [
  ["automaticSourceSyncEnabled", "Automatic source sync"],
  ["automaticSourceSyncInterval", "Automatic sync interval"],
  ["newJobMaxAttempts", "New Job maximum attempts"],
] as const;

export function settingsDraft(o: Overrides): SettingsDraft {
  return {
    enabled:
      o.automaticSourceSyncEnabled === undefined
        ? "inherit"
        : o.automaticSourceSyncEnabled
          ? "enabled"
          : "paused",
    interval: o.automaticSourceSyncInterval ?? "",
    attempts: o.newJobMaxAttempts?.toString() ?? "",
    intervalMode:
      o.automaticSourceSyncInterval === undefined ? "inherit" : "override",
    attemptsMode: o.newJobMaxAttempts === undefined ? "inherit" : "override",
  };
}

/** Validate bounded Go duration syntax without changing the exact submitted string. */
function interval(value: string): string {
  characterText(value, "Automatic sync interval", 64, 2);
  const units: Record<string, bigint> = {
    ns: 1n,
    us: 1000n,
    µs: 1000n,
    μs: 1000n,
    ms: 1000000n,
    s: 1000000000n,
    m: 60000000000n,
    h: 3600000000000n,
  };
  const pattern = /(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)/y;
  let offset = value.startsWith("+") ? 1 : 0;
  let total = 0n;
  while (offset < value.length) {
    pattern.lastIndex = offset;
    const match = pattern.exec(value);
    if (!match)
      throw new Error("Use a duration from 1m to 24h, for example 1m30.5s.");
    const [whole, fraction = ""] = match[1]!.split(".");
    const unit = units[match[2]!]!;
    // Go retains a bounded fractional accumulator, then rounds its float64
    // conversion before truncating nanoseconds. Exact rational truncation can
    // wrongly reject a valid value immediately below the one-minute boundary.
    let digits = 0n;
    let scale = 1;
    for (const digit of fraction) {
      if (digits > ((1n << 63n) - 1n) / 10n) break;
      const next = digits * 10n + BigInt(digit);
      if (next > 1n << 63n) break;
      digits = next;
      scale *= 10;
    }
    total +=
      BigInt(whole || "0") * unit +
      BigInt(Math.trunc(Number(digits) * (Number(unit) / scale)));
    offset = pattern.lastIndex;
  }
  if (total < 60000000000n || total > 86400000000000n)
    throw new Error("Use a duration from 1m to 24h, for example 1m30.5s.");
  return value;
}

export function settingsOverrides(draft: SettingsDraft): Overrides {
  atField("settings-enabled", () => {
    if (!["inherit", "enabled", "paused"].includes(draft.enabled))
      throw new Error("Choose Inherit, Enabled or Paused.");
  });
  atField("settings-interval", () => {
    if (!["inherit", "override"].includes(draft.intervalMode))
      throw new Error("Choose Inherit or Override for the interval.");
  });
  atField("settings-attempts", () => {
    if (!["inherit", "override"].includes(draft.attemptsMode))
      throw new Error("Choose Inherit or Override for attempts.");
  });
  const value: Overrides = {};
  if (draft.enabled !== "inherit")
    value.automaticSourceSyncEnabled = draft.enabled === "enabled";
  if (draft.intervalMode === "override")
    value.automaticSourceSyncInterval = atField("settings-interval", () =>
      interval(draft.interval),
    );
  if (draft.attemptsMode === "override") {
    value.newJobMaxAttempts = atField("settings-attempts", () => {
      if (!/^\d+$/.test(draft.attempts))
        throw new Error("Use 1 to 20 attempts.");
      const attempts = Number(draft.attempts);
      if (!Number.isSafeInteger(attempts) || attempts < 1 || attempts > 20)
        throw new Error("Use 1 to 20 attempts.");
      return attempts;
    });
  }
  return value;
}

export function settingText(value: boolean | string | number | undefined) {
  if (value === undefined) return "Inherit deployment default";
  if (typeof value === "boolean") return value ? "Enabled" : "Paused";
  return String(value);
}

/** Compare presence as well as values against one captured API response. */
export function settingsChanges(base: SettingsValue, proposed: Overrides) {
  return settingLabels
    .filter(([key]) => base.overrides[key] !== proposed[key])
    .map(([key, label]) => ({
      key,
      label,
      before: settingText(base.overrides[key]),
      after: settingText(proposed[key]),
      effectiveBefore: settingText(
        base.overrides[key] ?? base.deploymentDefaults[key],
      ),
      effectiveAfter: settingText(
        proposed[key] ?? base.deploymentDefaults[key],
      ),
    }));
}
