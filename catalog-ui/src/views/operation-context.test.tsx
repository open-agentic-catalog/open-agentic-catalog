// @vitest-environment jsdom
import { useState } from "react";
import { afterEach, beforeEach, expect, it } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { OperationProvider } from "./operation-context";
import { CommandDialog } from "./workspace";
import { installDialog, reader } from "../../tests/unit/fixtures";

let restore: () => void;
beforeEach(() => {
  restore = installDialog();
});
afterEach(() => {
  cleanup();
  restore();
});
function Workflow() {
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState("");
  return (
    <>
      <input
        aria-label="Private draft"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
      />
      <button onClick={() => setOpen(true)}>Prepare private command</button>
      {open && (
        <CommandDialog
          command={{
            title: "Private command",
            description: "Confirm captured input",
            summary: [{ label: "Private text", value: draft }],
            run: async () => "Done",
          }}
          onDone={() => setOpen(false)}
          onClose={() => setOpen(false)}
          onFailure={() => {}}
        />
      )}
    </>
  );
}
it("keeps unrelated authoring confirmations unchanged", () => {
  render(
    <OperationProvider access={reader} view="resources">
      <Workflow />
    </OperationProvider>,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Prepare private command" }),
  );
  expect(screen.getByRole("dialog").textContent).not.toContain(
    "Current tenant ID",
  );
});
it.each(["constructor", "toString", "__proto__", "hasOwnProperty"])(
  "does not treat inherited %s properties as Administration pages",
  (view) => {
    render(
      <OperationProvider access={reader} view={view}>
        <Workflow />
      </OperationProvider>,
    );
    expect(
      screen.queryByRole("region", { name: "Current administration context" }),
    ).toBeNull();
    fireEvent.click(
      screen.getByRole("button", { name: "Prepare private command" }),
    );
    expect(screen.getByRole("dialog").textContent).not.toContain(
      "Current tenant ID",
    );
  },
);
it.each([
  "settings",
  "members",
  "access-groups",
  "access-roles",
  "access-assignments",
  "feedback",
  "ratings",
  "audit",
])(
  "shows current self IDs on %s independently of alias and selected targets",
  (view) => {
    const subject = "u".repeat(128);
    render(
      <OperationProvider
        access={{ ...reader, subject, permissions: [] }}
        view={view}
      >
        <Workflow />
      </OperationProvider>,
    );
    const context = screen.getByRole("region", {
      name: "Current administration context",
    });
    expect(
      context.querySelector(".operation-summary strong")?.textContent,
    ).toBe(`Acting as ${subject}`);
    expect(context.textContent).toContain("tenant-a");
    expect(context.textContent).toContain("realm-a");
    expect(
      within(context).getByRole("button", { name: "Copy Current tenant ID" }),
    ).toBeTruthy();
    fireEvent.click(
      screen.getByRole("button", { name: "Prepare private command" }),
    );
    const facts = within(screen.getByRole("dialog")).getByLabelText(
      "Command summary",
    );
    expect(facts?.textContent).toContain("tenant-a");
    expect(screen.getByRole("dialog").textContent).toContain("realm-a");
  },
);
it.each([
  ["members", "Membership inventory and lifecycle are tenant-wide."],
  [
    "access-groups",
    "Group inventory and operations are limited to the operator's realm",
  ],
])(
  "explains the actual inventory and lifecycle boundary on %s",
  (view, scope) => {
    render(
      <OperationProvider access={reader} view={view}>
        <Workflow />
      </OperationProvider>,
    );
    const context = screen.getByRole("region", {
      name: "Current administration context",
    });
    expect(context.textContent).toContain(scope);
    fireEvent.click(
      screen.getByRole("button", { name: "Prepare private command" }),
    );
    expect(screen.getByRole("dialog").textContent).toContain(scope);
  },
);
it.each([
  "tenant",
  "realm",
  "membership",
  "subject",
  "actor",
  "permission",
  "scope",
])(
  "fences identity changes and retains prepared recovery on same-identity %s updates",
  (change) => {
    const access = { ...reader, subject: "same-subject" };
    const ui = render(
      <OperationProvider access={access} view="settings">
        <Workflow />
      </OperationProvider>,
    );
    fireEvent.change(screen.getByLabelText("Private draft"), {
      target: { value: "private sentinel" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Prepare private command" }),
    );
    const next = {
      ...access,
      ...(change === "tenant"
        ? { tenantId: "tenant-b" }
        : change === "realm"
          ? { realmId: "realm-b" }
          : change === "membership"
            ? { membershipId: "other-member" }
            : change === "subject"
              ? { subject: "other-subject" }
              : change === "actor"
                ? { actorType: "service" as const }
                : change === "permission"
                  ? { permissions: [] }
                  : {
                      scopes: ["tenant.admin" as const],
                      scopeEnforcement: true,
                    }),
    };
    ui.rerender(
      <OperationProvider access={next} view="settings">
        <Workflow />
      </OperationProvider>,
    );
    if (change === "permission" || change === "scope") {
      expect(screen.getByRole("dialog").textContent).toContain(
        "private sentinel",
      );
      expect(screen.getByLabelText("Private draft")).toHaveProperty(
        "value",
        "private sentinel",
      );
    } else {
      expect(screen.queryByRole("dialog")).toBeNull();
      expect(screen.getByLabelText("Private draft")).toHaveProperty(
        "value",
        "",
      );
    }
    const context = screen.getByRole("region", {
      name: "Current administration context",
    });
    expect(context.textContent).toContain(next.tenantId);
    expect(context.textContent).toContain(next.realmId);
    expect(context.textContent).toContain(`Acting as ${next.subject}`);
  },
);
it.each(["namespaces", "publishers", "service", "jobs"])(
  "captures %s command context without an Administration banner",
  (view) => {
    const ui = render(
      <OperationProvider access={reader} view={view}>
        <Workflow />
      </OperationProvider>,
    );
    expect(
      screen.queryByRole("region", { name: "Current administration context" }),
    ).toBeNull();
    fireEvent.click(
      screen.getByRole("button", { name: "Prepare private command" }),
    );
    ui.rerender(
      <OperationProvider access={reader} view="settings">
        <Workflow />
      </OperationProvider>,
    );
    expect(screen.getByRole("dialog").textContent).toContain(
      "Use the exact target and scope captured below",
    );
  },
);
