import { expect, it } from "vitest";
import {
  durationLabel,
  jobKindLabel,
  jobKinds,
  stateLabel,
} from "./operational-labels";

it("labels known states without interpreting unknown operational codes", () => {
  expect(stateLabel("open")).toBe("Open");
  expect(stateLabel("addressed")).toBe("Addressed");
  expect(stateLabel("not_assessed")).toBe("Not assessed");
  expect(stateLabel("negative_or_insufficient")).toBe(
    "Negative or insufficient evidence",
  );
  expect(stateLabel("changes_required")).toBe("Changes required");
  expect(stateLabel("blocked")).toBe("Blocked");
  expect(stateLabel("dead_letter")).toBe("Automatic retries stopped");
  expect(stateLabel("future_healthy")).toBe("future_healthy");
  expect(stateLabel("constructor")).toBe("constructor");
  expect(jobKinds.map(jobKindLabel)).toEqual([
    "Source synchronization",
    "Discovery snapshot generation",
    "Search indexing",
    "ANS registration assessment",
  ]);
  expect(jobKindLabel("future.kind")).toBe("future.kind");
});

it("formats sampled whole-second uptime with readable units", () => {
  expect(durationLabel(0)).toBe("0 seconds");
  expect(durationLabel(90)).toBe("1 minute 30 seconds");
  expect(durationLabel(86401)).toBe("1 day 1 second");
  expect(durationLabel(90060)).toBe("1 day 1 hour 1 minute");
});

it.each([
  ["open", "Open"],
  ["addressed", "Addressed"],
  ["blocked", "Blocked"],
  ["changes_required", "Changes required"],
  ["negative", "Negative"],
  ["withdrawn", "Withdrawn"],
  ["suspended", "Suspended"],
  ["archived", "Archived"],
  ["approved", "Approved"],
  ["rejected", "Rejected"],
  ["draft", "Draft"],
  ["submitted", "Submitted"],
  ["retired", "Retired"],
  ["healthy", "Healthy"],
  ["degraded", "Degraded"],
  ["passed", "Passed"],
  ["valid", "Valid"],
  ["invalid", "Invalid"],
  ["queued", "Queued"],
  ["running", "Running"],
  ["waiting", "Waiting"],
  ["succeeded", "Succeeded"],
  ["failed", "Failed"],
  ["cancelled", "Cancelled"],
  ["dead_letter", "Automatic retries stopped"],
  ["active", "Active"],
  ["paused", "Paused"],
  ["error", "Error"],
  ["removed", "Removed"],
  ["disabled", "Turned off"],
  ["enabled", "Enabled"],
  ["unavailable", "Unavailable"],
  ["unknown", "Unknown"],
  ["complete", "Complete"],
  ["projection", "Projection"],
  ["unresolved", "Unresolved"],
  ["supported", "Supported"],
  ["unsupported", "Unsupported"],
  ["incomplete", "Incomplete"],
  ["current", "Current"],
  ["historical", "Historical"],
  ["expired", "Expired"],
  ["pending", "Pending"],
  ["not_assessed", "Not assessed"],
  ["administrator_approved", "Administrator approved"],
  ["negative_or_insufficient", "Negative or insufficient evidence"],
])("preserves the existing %s label", (code, label) => {
  expect(stateLabel(code)).toBe(label);
});

it.each([
  "",
  "__proto__",
  "toString",
  "VALID",
  "future_code",
  "<script>literal</script>",
])("keeps the unknown code %s exact", (code) => {
  expect(stateLabel(code)).toBe(code);
});
