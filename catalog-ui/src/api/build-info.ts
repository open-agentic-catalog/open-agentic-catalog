// UI-host identity is independent of the connected Catalog API.
import { object, text, choice, number, timestamp } from "./values";

export type BuildInfo = {
  version: string;
  commit: string;
  sourceState: "clean" | "modified" | "unknown";
};

/** Admit bounded embedded identifiers and the fixed provenance vocabulary. */
export function isBuildInfo(value: unknown): value is BuildInfo {
  const identifier = (item: unknown) =>
    text(item) && /^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$/.test(item);
  return (
    object(value) &&
    identifier(value.version) &&
    identifier(value.commit) &&
    choice(value.sourceState, ["clean", "modified", "unknown"])
  );
}

export type ProcessInfo = {
  instanceId: string;
  startedAt: string;
  observedAt: string;
  uptimeSeconds: number;
};
export type UIProcessInfo = BuildInfo & ProcessInfo & { role: "ui" };

/** Wall-clock timestamps need not agree with monotonic elapsed time. */
export function isProcessInfo(value: unknown): value is ProcessInfo {
  return (
    object(value) &&
    text(value.instanceId) &&
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
      value.instanceId,
    ) &&
    timestamp(value.startedAt) &&
    timestamp(value.observedAt) &&
    number(value.uptimeSeconds)
  );
}
export function isUIProcessInfo(value: unknown): value is UIProcessInfo {
  return (
    object(value) &&
    value.role === "ui" &&
    isBuildInfo(value) &&
    isProcessInfo(value)
  );
}
