// @vitest-environment jsdom
import { afterEach, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { ValidationFindings, ReviewDecisions } from "./ValidationFindings";
import { evidence, submission } from "../../tests/unit/responses";
import type { components } from "../api/schema";
afterEach(cleanup);
const e = evidence as components["schemas"]["EvidenceSet"];
const s = submission as components["schemas"]["ReviewSubmission"];
it("shows exact current evidence, every severity and honest assessment boundaries", () => {
  render(
    <ValidationFindings
      revisionId="exact-revision"
      evidence={{
        ...e,
        state: "blocked",
        findings: [
          {
            severity: "error",
            code: "missing_name",
            path: "/name",
            message: "Supply a name.",
          },
          {
            severity: "warning",
            code: "optional",
            message: "Optional content missing.",
          },
          { severity: "info", code: "retained", message: "Retained document." },
        ],
      }}
    />,
  );
  expect(screen.getByText("Blocking error")).toBeTruthy();
  expect(screen.getByText("Warning")).toBeTruthy();
  expect(screen.getByText("Information")).toBeTruthy();
  expect(screen.getByText("/name")).toBeTruthy();
  expect(screen.getByText(/This evidence is blocked/)).toBeTruthy();
  expect(screen.getByText(/Trust: Not assessed/)).toBeTruthy();
  expect(screen.getByText("exact-revision")).toBeTruthy();
  expect(screen.getByText(e.validationProfile)).toBeTruthy();
  expect(screen.queryByRole("link")).toBeNull();
});
it("distinguishes submission-bound, expired and empty evidence", () => {
  const view = render(
    <ValidationFindings
      submitted
      revisionId="submitted-exact"
      evidence={{ ...e, expiresAt: "2000-01-01T00:00:00Z", findings: [] }}
    />,
  );
  expect(
    screen.getByRole("region", { name: "Submitted validation findings" }),
  ).toBeTruthy();
  expect(screen.getByText(/2000.*expired/)).toBeTruthy();
  expect(screen.getByText("No validation findings recorded.")).toBeTruthy();
  view.rerender(
    <ValidationFindings
      revisionId="new-exact"
      evidence={{ ...e, expiresAt: "2999-01-01T00:00:00Z" }}
    />,
  );
  expect(screen.queryByText(/This evidence is/)).toBeNull();
  expect(screen.getByText("2999-01-01T00:00:00Z")).toBeTruthy();
});
it("reads immutable reviewer reasons without inventing omitted reasons or rendering markup", () => {
  const view = render(<ReviewDecisions submission={s} />);
  expect(screen.getByText("No reviewer decision recorded.")).toBeTruthy();
  view.rerender(
    <ReviewDecisions
      submission={{
        ...s,
        decisions: [
          {
            action: "approve",
            actor: "reviewer-a",
            at: "2026-01-01T00:00:00Z",
          },
          {
            action: "request_changes",
            actor: "reviewer-b",
            at: "2026-01-02T00:00:00Z",
            reason: "<script>literal</script>",
          },
          {
            action: "reject",
            actor: "reviewer-c",
            at: "2026-01-03T00:00:00Z",
            reason: "  ",
          },
        ],
      }}
    />,
  );
  expect(screen.getByText("Approved")).toBeTruthy();
  expect(screen.getByText("Changes requested")).toBeTruthy();
  expect(screen.getByText("Rejected")).toBeTruthy();
  for (const [label, tone] of [
    ["Approved", "success"],
    ["Changes requested", "warning"],
    ["Rejected", "danger"],
  ])
    expect(screen.getByText(label!).getAttribute("data-tone")).toBe(tone);
  expect(screen.getAllByText("No reason was recorded.")).toHaveLength(2);
  expect(screen.getByText("<script>literal</script>")).toBeTruthy();
  expect(document.querySelector("script")).toBeNull();
});
