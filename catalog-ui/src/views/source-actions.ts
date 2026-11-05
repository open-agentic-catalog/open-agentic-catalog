// Source command hints combine independent tenant authority with loaded lifecycle state.
import type { Access } from "../api/client";
import { permits } from "../api/permissions";

export const sourceActionLabels = {
  test: "Test current policy and endpoint",
  sync: "Queue synchronization",
  pause: "Pause",
  resume: "Resume",
};
export type SourceAction = keyof typeof sourceActionLabels;

/** Backend policy and revisions still decide every command; these are loaded-state hints. */
export function sourceActionReason(
  access: Access,
  status: string,
  action: SourceAction,
) {
  const permission =
    action === "test"
      ? "source.inspect"
      : action === "sync"
        ? "source.sync"
        : "source.manage";
  if (!permits(access, permission, "sources.manage"))
    return `The ${permission} permission and sources.manage scope when enforced are required.`;
  if (status === "removed") return "This source is unavailable.";
  if (action === "resume" && status !== "paused")
    return "Resume requires a paused source.";
  if (action === "pause" && status === "paused")
    return "This source is already paused.";
  if (action === "sync" && status === "paused")
    return "Resume this source before synchronization.";
  return undefined;
}

/** Prefer the first permitted eligible command, without contacting the source. */
export function sourceActionChoices(access: Access, status: string) {
  const order: SourceAction[] =
    status === "paused"
      ? ["resume", "test", "sync", "pause"]
      : (Object.keys(sourceActionLabels) as SourceAction[]);
  return order.filter((action) => !sourceActionReason(access, status, action));
}
