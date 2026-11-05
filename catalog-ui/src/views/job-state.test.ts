import { expect, it } from "vitest";
import { jobFilters, jobQueueURL, jobStates } from "./job-state";

it("retains only applied literal filters in root and subpath routes", () => {
  const selected = {
    kind: "unknown.job",
    state: "waiting",
    parentJobId: "parent",
  };
  expect(jobFilters("")).toEqual({ kind: "", state: "", parentJobId: "" });
  expect(
    jobFilters(
      "?kind=unknown.job&state=waiting&parentJobId=parent&cursor=private&draft=unsent",
    ),
  ).toEqual(selected);
  for (const base of ["", "/unit"]) {
    expect(jobQueueURL(base, selected)).toBe(
      `${base}/jobs?kind=unknown.job&state=waiting&parentJobId=parent`,
    );
    expect(jobQueueURL(base, { kind: "", state: "", parentJobId: "" })).toBe(
      `${base}/jobs`,
    );
  }
  expect(jobFilters("?state=unknown").state).toBe("unknown");
  for (const name of ["kind", "state", "parentJobId"] as const) {
    expect(jobFilters(`?${name}=&${name}=hidden`)[name]).toBe("unsupported");
  }
});

it("explains every public state without treating progress or acknowledgement as completion", () => {
  expect(Object.keys(jobStates)).toEqual([
    "queued",
    "running",
    "waiting",
    "succeeded",
    "failed",
    "cancelled",
    "dead_letter",
  ]);
  expect(jobStates.running).toContain("not a committed result");
  expect(jobStates.waiting).toContain("not a promised retry time");
  expect(jobStates.failed).toContain("separate child");
  expect(jobStates.cancelled).toContain("not undone");
});
