const simpleStates = new Set([
  "open",
  "addressed",
  "blocked",
  "negative",
  "withdrawn",
  "suspended",
  "archived",
  "approved",
  "rejected",
  "draft",
  "submitted",
  "retired",
  "healthy",
  "degraded",
  "passed",
  "valid",
  "invalid",
  "queued",
  "running",
  "waiting",
  "succeeded",
  "failed",
  "cancelled",
  "active",
  "paused",
  "error",
  "removed",
  "enabled",
  "unavailable",
  "unknown",
  "complete",
  "projection",
  "unresolved",
  "supported",
  "unsupported",
  "incomplete",
  "current",
  "historical",
  "expired",
  "pending",
  "offboarded",
  "revoked",
]);
const stateLabels: Record<string, string> = {
  changes_required: "Changes required",
  dead_letter: "Automatic retries stopped",
  disabled: "Turned off",
  not_assessed: "Not assessed",
  administrator_approved: "Administrator approved",
  negative_or_insufficient: "Negative or insufficient evidence",
};

// Presentation labels do not change wire values, command bindings or retained evidence.
export const retrievalLabels = {
  indexed: "Indexed copy",
  live: "Live requests",
  hybrid: "Indexed copy and live requests",
};

export const jobKinds = [
  "source.sync",
  "publication.generate",
  "semantic.reindex",
  "source.trust_verify",
] as const;

/** Known operational wire values get readable labels; unknown codes remain exact. */
export function stateLabel(value: string): string {
  if (simpleStates.has(value)) return value[0]!.toUpperCase() + value.slice(1);
  return Object.hasOwn(stateLabels, value) ? stateLabels[value]! : value;
}

/** Format the server's elapsed duration without deriving it from wall-clock dates. */
export function durationLabel(seconds: number): string {
  const units: [number, string][] = [
    [86400, "day"],
    [3600, "hour"],
    [60, "minute"],
    [1, "second"],
  ];
  let remaining = seconds;
  const parts: string[] = [];
  for (const [size, label] of units) {
    const value = Math.floor(remaining / size);
    remaining %= size;
    if (value) parts.push(`${value} ${label}${value === 1 ? "" : "s"}`);
  }
  return parts.join(" ") || "0 seconds";
}

export function jobKindLabel(kind: string): string {
  switch (kind) {
    case "source.sync":
      return "Source synchronization";
    case "publication.generate":
      return "Discovery snapshot generation";
    case "semantic.reindex":
      return "Search indexing";
    case "source.trust_verify":
      return "ANS registration assessment";
    default:
      return kind;
  }
}

/** The caller selects meaning; this label never infers authority or health. */
export function StatusLabel({
  state,
  tone,
}: {
  state: string;
  tone: "neutral" | "info" | "success" | "warning" | "danger";
}) {
  return (
    <span className="workspace-status" data-state={state} data-tone={tone}>
      {stateLabel(state)}
    </span>
  );
}

/** Job and delivery outcomes describe the recorded work, not its target's health. */
export function WorkState({ state }: { state: string }) {
  return (
    <StatusLabel
      state={state}
      tone={
        state === "succeeded"
          ? "success"
          : state === "failed" || state === "dead_letter"
            ? "danger"
            : state === "waiting"
              ? "warning"
              : state === "running"
                ? "info"
                : "neutral"
      }
    />
  );
}

/** A retained active policy record is not proof of effective permission. */
export function AccessState({ state }: { state: string }) {
  return (
    <StatusLabel
      state={state}
      tone={
        state === "suspended"
          ? "warning"
          : state === "active"
            ? "info"
            : "neutral"
      }
    />
  );
}

/** Active source configuration is informational, not remote availability. */
export function SourceState({ state }: { state: string }) {
  return (
    <StatusLabel
      state={state}
      tone={
        state === "paused"
          ? "warning"
          : state === "error"
            ? "danger"
            : state === "active"
              ? "info"
              : "neutral"
      }
    />
  );
}
/** Complete evidence does not approve its contents. */
export function CompletenessState({ state }: { state: string }) {
  return (
    <StatusLabel
      state={state}
      tone={
        state === "complete"
          ? "info"
          : state === "projection" || state === "unresolved"
            ? "warning"
            : "neutral"
      }
    />
  );
}
/** A structural check has its own outcome, separate from native support or trust. */
export function StructureState({ state }: { state: string }) {
  return (
    <StatusLabel
      state={state}
      tone={
        state === "valid"
          ? "success"
          : state === "invalid"
            ? "danger"
            : "neutral"
      }
    />
  );
}
/** Failed or incomplete synchronization preserves the last complete source view. */
export function SourceSnapshotState({ state }: { state: string }) {
  return (
    <StatusLabel
      state={state}
      tone={
        state === "failed"
          ? "danger"
          : state === "incomplete"
            ? "warning"
            : state === "complete"
              ? "info"
              : "neutral"
      }
    />
  );
}
