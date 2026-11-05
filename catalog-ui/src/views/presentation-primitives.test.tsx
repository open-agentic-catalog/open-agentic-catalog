// @vitest-environment jsdom
import { useState } from "react";
import { InvestigationProvider, useInvestigation } from "./audit-handoff";
import { afterEach, beforeEach, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { DateValue } from "./date-value";
import { PermissionDenied, Field } from "./workspace";
import { ActionForm } from "./authoring";
import { vi } from "vitest";
import { workspace, installDialog } from "../../tests/unit/fixtures";
const runtime = workspace().props.runtime;
let restoreDialog: () => void;
beforeEach(() => {
  restoreDialog = installDialog();
});
afterEach(() => {
  cleanup();
  restoreDialog();
});
it("shows a readable date while retaining its exact original timestamp", () => {
  const value = "2026-10-21T12:34:56.123Z";
  const { container } = render(<DateValue value={value} />);
  const time = container.querySelector("time")!;
  expect(time.dateTime).toBe(value);
  expect(time.title).toBe(value);
  expect(time.textContent).toBe(new Date(value).toLocaleString());
});
it("orients denied pages without naming a hidden object", () => {
  render(
    <PermissionDenied
      title="Tenant settings"
      runtime={{ ...runtime, basePath: "/catalog-ui" }}
    />,
  );
  expect(
    screen.getByRole("heading", { level: 1, name: "Tenant settings" }),
  ).toBeTruthy();
  expect(
    screen.getByRole("link", { name: "View my access" }).getAttribute("href"),
  ).toBe("/catalog-ui/access");
  expect(screen.getByText("You do not have access to this page.")).toBeTruthy();
});
it("can explain the required safe capability", () => {
  render(
    <PermissionDenied
      title="Audit"
      runtime={runtime}
      description="Audit export requires separate access."
    />,
  );
  expect(
    screen.getByText("Audit export requires separate access."),
  ).toBeTruthy();
});

it("hides an empty read-only editor while keeping authorized preparation usable", () => {
  const props = {
    title: "Change state",
    description: "A deliberate change",
    allowed: false,
    onFailure: vi.fn(),
    prepare: () => async () => "Done",
  };
  const ui = render(
    <ActionForm {...props} editable={false}>
      <Field label="Reason">
        <input required />
      </Field>
    </ActionForm>,
  );
  expect(screen.queryByRole("textbox", { name: "Reason" })).toBeNull();
  ui.rerender(
    <ActionForm {...props} editable>
      <Field label="Reason">
        <input required />
      </Field>
    </ActionForm>,
  );
  expect(screen.getByRole("textbox", { name: "Reason" })).toHaveProperty(
    "disabled",
    false,
  );
  expect(screen.getByRole("button", { name: "Change state" })).toHaveProperty(
    "disabled",
    true,
  );
});
it("retains actual parent-owned input after a readback remount removes editing authority", () => {
  render(
    <ActionForm
      title="Change state"
      description="A deliberate change"
      allowed={false}
      editable={false}
      dirty
      retainDraft
      prepare={() => async () => "Done"}
      onFailure={vi.fn()}
    >
      <Field label="Reason">
        <input value="Unsent explanation" readOnly />
      </Field>
    </ActionForm>,
  );
  expect(screen.getByRole("textbox", { name: "Reason" })).toHaveProperty(
    "value",
    "Unsent explanation",
  );
  expect(screen.getByRole("button", { name: "Change state" })).toHaveProperty(
    "disabled",
    true,
  );
});

function InvestigationProbe() {
  const handoff = useInvestigation()!;
  const [draft, setDraft] = useState("");
  return (
    <>
      <input
        aria-label="Unsent draft"
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
      />
      <button
        onClick={() =>
          handoff.stage({
            criteria: { actor: "private-actor" },
            note: "Captured change",
          })
        }
      >
        Stage investigation
      </button>
      <p>
        {handoff.pending ? "Investigation staged" : "No staged investigation"}
      </p>
    </>
  );
}
it("invalidates audit criteria on policy changes without losing an unrelated draft or resurrecting old criteria", () => {
  const ui = render(
    <InvestigationProvider policyKey="policy-a">
      <InvestigationProbe />
    </InvestigationProvider>,
  );
  fireEvent.change(screen.getByLabelText("Unsent draft"), {
    target: { value: "Retain my correction" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Stage investigation" }));
  expect(screen.getByText("Investigation staged")).toBeTruthy();
  ui.rerender(
    <InvestigationProvider policyKey="policy-b">
      <InvestigationProbe />
    </InvestigationProvider>,
  );
  expect(screen.getByText("No staged investigation")).toBeTruthy();
  expect(screen.getByLabelText("Unsent draft")).toHaveProperty(
    "value",
    "Retain my correction",
  );
  ui.rerender(
    <InvestigationProvider policyKey="policy-a">
      <InvestigationProbe />
    </InvestigationProvider>,
  );
  expect(screen.getByText("No staged investigation")).toBeTruthy();
});

it("retains captured input but blocks a new send after edit authority is lost", () => {
  const run = vi.fn(async () => "Done");
  const props = {
    title: "Save correction",
    description: "Confirm the exact correction",
    dirty: true,
    onFailure: vi.fn(),
    prepare: () => ({
      run,
      summary: [{ label: "Correction", value: "Captured input" }],
    }),
  };
  const ui = render(
    <ActionForm {...props} allowed editable>
      <Field label="Reason">
        <input defaultValue="My staged reason" />
      </Field>
    </ActionForm>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Save correction" }));
  expect(screen.getByRole("dialog").textContent).toContain("Captured input");
  ui.rerender(
    <ActionForm {...props} allowed={false} editable={false}>
      <Field label="Reason">
        <input defaultValue="My staged reason" />
      </Field>
    </ActionForm>,
  );
  expect(screen.getByRole("button", { name: "Confirm" })).toHaveProperty(
    "disabled",
    true,
  );
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
  expect(run).not.toHaveBeenCalled();
  expect(screen.getByRole("dialog").textContent).toContain("Captured input");
});
