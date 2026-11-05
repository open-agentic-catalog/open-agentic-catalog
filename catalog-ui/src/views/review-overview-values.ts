import { object, number, timestamp, choice } from "../api/values";
import { scope, boundedCount } from "./overview-values";
import { PublicationSnapshotSummary } from "./responses";

export function reviewOverview(value: unknown, tenant: string): boolean {
  if (
    !scope(value, tenant) ||
    !boundedCount(value.pending) ||
    !object(value.pending) ||
    !boundedCount(value.approvedUnreleased) ||
    !boundedCount(value.submissions) ||
    !object(value.submissions) ||
    !boundedCount(value.decisions) ||
    !object(value.decisions) ||
    !timestamp(value.startAt) ||
    !timestamp(value.endAt) ||
    value.endAt !== value.observedAt ||
    !Array.isArray(value.days) ||
    value.days.length !== 30
  )
    return false;
  const end = new Date(value.endAt as string);
  const start = new Date(end);
  start.setUTCHours(0, 0, 0, 0);
  start.setUTCDate(start.getUTCDate() - 29);
  if (new Date(value.startAt as string).getTime() !== start.getTime())
    return false;
  let submitted = 0,
    decisions = 0;
  for (const [index, day] of value.days.entries()) {
    const date = new Date(start);
    date.setUTCDate(date.getUTCDate() + index);
    if (
      !object(day) ||
      day.date !== date.toISOString().slice(0, 10) ||
      day.partial !== (index === 29)
    )
      return false;
    if (value.submissions.limited) {
      if (day.submitted !== undefined) return false;
    } else {
      if (!number(day.submitted, 0, 10000)) return false;
      submitted += day.submitted as number;
    }
    for (const key of ["approved", "changesRequired", "rejected"]) {
      if (value.decisions.limited) {
        if (day[key] !== undefined) return false;
      } else {
        if (!number(day[key], 0, 10000)) return false;
        decisions += day[key] as number;
      }
    }
  }
  if (
    (!value.submissions.limited && submitted !== value.submissions.value) ||
    (!value.decisions.limited && decisions !== value.decisions.value)
  )
    return false;
  if (
    value.pending.value === 0
      ? value.oldestPendingAt !== undefined
      : !timestamp(value.oldestPendingAt)
  )
    return false;
  if (value.pending.limited) {
    if (
      value.pendingAge !== undefined ||
      value.pendingAgeUnavailable !== "limited"
    )
      return false;
  } else if (value.pendingAgeUnavailable !== undefined) {
    if (
      value.pendingAgeUnavailable !== "invalid_timestamps" ||
      value.pendingAge !== undefined
    )
      return false;
  } else {
    if (!object(value.pendingAge)) return false;
    const counts = [
      value.pendingAge.under24Hours,
      value.pendingAge.under7Days,
      value.pendingAge.atLeast7Days,
    ];
    if (
      !counts.every((count) => number(count, 0, 10000)) ||
      (counts as number[]).reduce((a, b) => a + b, 0) !== value.pending.value
    )
      return false;
  }
  if (value.decisions.limited)
    return (
      value.medianSeconds === undefined && value.medianUnavailable === "limited"
    );
  if (value.medianUnavailable !== undefined)
    return (
      value.medianUnavailable === "invalid_timestamps" &&
      value.medianSeconds === undefined
    );
  return value.decisions.value === 0
    ? value.medianSeconds === undefined
    : typeof value.medianSeconds === "number" &&
        Number.isFinite(value.medianSeconds) &&
        value.medianSeconds >= 0 &&
        value.medianSeconds <= Number.MAX_SAFE_INTEGER;
}

export function publicationOverview(value: unknown, tenant: string): boolean {
  if (
    !scope(value, tenant, 64) ||
    !Array.isArray(value.hosts) ||
    value.hosts.length > 64
  )
    return false;
  const seen = new Set();
  return value.hosts.every((host) => {
    if (
      !object(host) ||
      typeof host.host !== "string" ||
      host.host.length === 0 ||
      /[/\\@,\s?#]/u.test(host.host) ||
      host.host.includes(String.fromCharCode(0)) ||
      host.host.length > 253 ||
      seen.has(host.host) ||
      !choice(host.mode, ["disabled", "anonymous", "authenticated"]) ||
      !choice(host.status, ["disabled", "unpublished", "recorded"]) ||
      !Array.isArray(host.mismatches) ||
      host.mismatches.length > 3 ||
      new Set(host.mismatches).size !== host.mismatches.length ||
      !host.mismatches.every((item) =>
        choice(item, ["audience", "ard_profile", "ai_catalog_profile"]),
      )
    )
      return false;
    seen.add(host.host);
    if (host.status === "disabled")
      return (
        host.mode === "disabled" &&
        host.active === undefined &&
        host.mismatches.length === 0
      );
    if (host.mode === "disabled") return false;
    if (host.status === "unpublished")
      return host.active === undefined && host.mismatches.length === 0;
    return (
      PublicationSnapshotSummary(host.active) &&
      object(host.active) &&
      host.active.tenantId === tenant &&
      host.active.host === host.host &&
      host.active.state === "active" &&
      (host.active.audience === host.mode ||
        host.mismatches.includes("audience"))
    );
  });
}
