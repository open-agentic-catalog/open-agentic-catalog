import { revealEditor } from "../../tests/unit/reveal-editor";
// @vitest-environment jsdom
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { expect, it, vi } from "vitest";
import Audit, { auditLocalTime } from "./Audit";
import {
  AdministrationAcknowledgement,
  InvestigationProvider,
  acknowledged,
  useInvestigation,
} from "./audit-handoff";
import { OperationProvider, useOperationContext } from "./operation-context";
import { useState } from "react";
import { CommandDialog, useUnsaved } from "./workspace";
import {
  administrator,
  change as baseChange,
  click as baseClick,
  etagReply,
  screenLifecycle,
  screenWorkspace,
  stamp,
} from "../../tests/unit/screens";
function change(name: string, value: string) {
  revealEditor(screen.getByLabelText(name));
  baseChange(name, value);
}
function click(name: string | RegExp) {
  const target = screen.getAllByText(name, { selector: "button" })[0];
  if (target) revealEditor(target);
  baseClick(name);
}

screenLifecycle("/unit/audit");
const receipt = {
  action: "tenant.settings.changed",
  targetType: "tenant_settings",
  targetId: administrator.tenantId,
  revision: 7,
  acknowledgedAt: stamp,
  replayed: true,
  requestId: "retry-attempt",
};
const event = {
  id: "event-literal",
  action: "historical.exact",
  actor: "exact-principal",
  targetType: "tenant_settings",
  targetId: administrator.tenantId,
  requestId: "original-request",
  at: stamp,
  reason: "<script>hostile()</script>",
};

it("projects only a safe attempt ID, code-owned binding and replay acknowledgement", () => {
  for (const id of [undefined, "unsafe/request", "x".repeat(129)]) {
    const headers = id ? { "X-Request-ID": id } : {};
    expect(
      acknowledged("Saved", new Response(null, { headers }), receipt),
    ).toMatchObject({
      message: "Saved",
      receipt: { requestId: undefined, replayed: false },
    });
  }
  expect(
    acknowledged(
      "Saved",
      new Response(null, {
        headers: {
          "X-Request-ID": "attempt_1.ok",
          "Idempotency-Replayed": "true",
        },
      }),
      receipt,
    ).receipt,
  ).toMatchObject({ requestId: "attempt_1.ok", replayed: true, revision: 7 });
});
it("converts an instant to the browser local datetime without losing milliseconds", () => {
  expect(new Date(auditLocalTime(stamp)).toISOString()).toBe(
    new Date(stamp).toISOString(),
  );
  expect(
    new Date(auditLocalTime("2026-01-02T02:03:04.123Z")).toISOString(),
  ).toBe("2026-01-02T02:03:04.123Z");
});

it("stages several literal event criteria against the applied range, requires Search and guards export drafts", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply({ items: [event], nextCursor: "opaque" }));
  vi.spyOn(Date, "now").mockReturnValue(Date.parse(stamp));
  render(<Audit {...w.props} access={administrator} />);
  click("Search audit events");
  await screen.findByText(event.reason);
  expect(document.querySelector("script")).toBeNull();
  const original = w.get.mock.calls[0]?.[1];
  click("Next page");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
  change("Reason", "Keep this export reason");
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Use target as filter");
  expect(screen.getByLabelText("Time range")).toHaveProperty("value", "day");
  confirm.mockReturnValue(true);
  click("Use target as filter");
  click("Use action as filter");
  click("Use principal as filter");
  click("Use request as filter");
  expect(w.get).toHaveBeenCalledTimes(2);
  expect(screen.getByLabelText("Time range")).toHaveProperty("value", "custom");
  expect(screen.getByLabelText("Exact action")).toHaveProperty(
    "value",
    event.action,
  );
  expect(screen.getByText(/Unsubmitted filter changes/)).toBeTruthy();
  expect(screen.getByLabelText("Reason")).toHaveProperty(
    "value",
    "Keep this export reason",
  );
  fireEvent.click(
    screen.getByText("Advanced filters: person, request or object"),
  );
  expect(screen.getByLabelText("Target type")).toHaveProperty(
    "value",
    event.targetType,
  );
  expect(screen.getByLabelText("Target ID")).toHaveProperty(
    "value",
    event.targetId,
  );
  click("Search audit events");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(3));
  expect(w.get.mock.calls[2]?.[1]).toMatchObject({
    params: {
      query: {
        ...(original as { params: { query: object } }).params.query,
        actor: event.actor,
        action: event.action,
        requestId: event.requestId,
        targetType: event.targetType,
        targetId: event.targetId,
      },
    },
  });
  expect(w.get.mock.calls[2]?.[1]).not.toHaveProperty("params.query.cursor");
  expect(screen.getByLabelText("Reason")).toHaveProperty("value", "");
  expect(w.post).not.toHaveBeenCalled();
});

function HandoffProbe() {
  const handoff = useInvestigation();
  return (
    <output aria-label="pending handoff">
      {JSON.stringify(handoff?.pending)}
    </output>
  );
}
it("asks before leaving a dirty administration draft and stages nothing when navigation is declined", () => {
  const w = screenWorkspace();
  function Dirty() {
    useUnsaved(true);
    return null;
  }
  vi.spyOn(window, "confirm").mockReturnValue(false);
  render(
    <InvestigationProvider>
      <Dirty />
      <AdministrationAcknowledgement
        receipt={receipt}
        access={administrator}
        runtime={w.props.runtime}
      />
      <HandoffProbe />
    </InvestigationProvider>,
  );
  fireEvent.click(screen.getByRole("link", { name: "Investigate change" }));
  expect(window.confirm).toHaveBeenCalledOnce();
  expect(screen.getByLabelText("pending handoff").textContent).toBe("");
  expect(w.get).not.toHaveBeenCalled();
});
it("keeps receipts for non-auditors and stages replay criteria privately only after deliberate navigation", async () => {
  const w = screenWorkspace();
  const ui = render(
    <InvestigationProvider>
      <AdministrationAcknowledgement
        receipt={receipt}
        access={administrator}
        runtime={w.props.runtime}
      />
      <HandoffProbe />
    </InvestigationProvider>,
  );
  expect(screen.getByText(/Replayed command acknowledgement/)).toBeTruthy();
  expect(
    screen.getByRole("button", { name: "Copy HTTP attempt request ID" }),
  ).toBeTruthy();
  const link = screen.getByRole("link", { name: "Investigate change" });
  fireEvent.click(link, { ctrlKey: true });
  expect(screen.getByLabelText("pending handoff").textContent).toBe("");
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  // No dirty form means no discard prompt. The regular link performs a private handoff.
  fireEvent.click(link);
  expect(confirm).not.toHaveBeenCalled();
  expect(location.pathname).toBe("/unit/audit");
  expect(location.search).toBe("");
  expect(screen.getByLabelText("pending handoff").textContent).toContain(
    '"requestId":""',
  );
  expect(screen.getByLabelText("pending handoff").textContent).not.toContain(
    receipt.requestId,
  );
  expect(localStorage.length).toBe(0);
  expect(sessionStorage.length).toBe(0);
  ui.rerender(
    <InvestigationProvider>
      <AdministrationAcknowledgement
        receipt={{
          action: "operation",
          acknowledgedAt: stamp,
          replayed: false,
        }}
        access={{ ...administrator, permissions: ["settings.read"] }}
        runtime={w.props.runtime}
      />
    </InvestigationProvider>,
  );
  expect(screen.queryByRole("link", { name: "Investigate change" })).toBeNull();
  expect(screen.getByText("Command acknowledged.")).toBeTruthy();
  expect(
    screen.queryByRole("button", { name: "Copy acknowledged revision" }),
  ).toBeNull();
  expect(w.get).not.toHaveBeenCalled();
});

it.each([
  { permissions: ["audit.read"], scopes: [], enforced: false, allowed: true },
  {
    permissions: ["audit.read"],
    scopes: ["audit.read"],
    enforced: true,
    allowed: true,
  },
  {
    permissions: ["audit.read"],
    scopes: ["audit.export"],
    enforced: true,
    allowed: false,
  },
  {
    permissions: ["audit.export"],
    scopes: ["audit.read"],
    enforced: true,
    allowed: false,
  },
] as const)(
  "keeps literal receipts while requiring independent audit permission and OAuth admission: %j",
  ({ permissions, scopes, enforced, allowed }) => {
    const w = screenWorkspace();
    render(
      <InvestigationProvider>
        <AdministrationAcknowledgement
          receipt={receipt}
          access={{
            ...administrator,
            permissions: [...permissions],
            scopeEnforcement: enforced,
            scopes: [...scopes],
          }}
          runtime={w.props.runtime}
        />
      </InvestigationProvider>,
    );
    expect(
      screen.queryByRole("link", { name: "Investigate change" }) !== null,
    ).toBe(allowed);
    expect(
      screen.getByRole("button", { name: "Copy HTTP attempt request ID" }),
    ).toBeTruthy();
    expect(
      screen.getByRole("button", { name: "Copy acknowledged target ID" }),
    ).toBeTruthy();
    expect(w.get).not.toHaveBeenCalled();
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each([
  {
    timezone: "UTC",
    occurredFrom: "2026-09-01T11:55:00Z",
    occurredBefore: stamp,
  },
  {
    timezone: "America/New_York",
    occurredFrom: "2026-11-01T06:25:00.123Z",
    occurredBefore: "2026-11-01T06:30:00.123Z",
  },
])(
  "preserves exact staged UTC bounds through Search and access change in $timezone",
  async ({ timezone, occurredFrom, occurredBefore }) => {
    vi.stubEnv("TZ", timezone);
    try {
      if (timezone === "America/New_York") {
        expect(new Date(occurredBefore).getTimezoneOffset()).toBe(300);
        expect(new Date("2026-11-01T01:30:00.123").toISOString()).toBe(
          "2026-11-01T05:30:00.123Z",
        );
      }
      const w = screenWorkspace();
      w.get.mockResolvedValue(etagReply({ items: [] }));
      function Controls() {
        const handoff = useInvestigation()!;
        return (
          <button
            onClick={() =>
              handoff.stage({
                criteria: {
                  action: receipt.action,
                  targetType: receipt.targetType,
                  targetId: receipt.targetId,
                  occurredFrom,
                  occurredBefore,
                },
                note: "Explicit handoff",
              })
            }
          >
            Stage receipt
          </button>
        );
      }
      const ui = render(
        <InvestigationProvider>
          <Controls />
          <Audit {...w.props} access={administrator} />
        </InvestigationProvider>,
      );
      click("Stage receipt");
      await screen.findByText(/Explicit handoff/);
      expect(w.get).not.toHaveBeenCalled();
      expect(screen.getByLabelText("Time range")).toHaveProperty(
        "value",
        "custom",
      );
      if (timezone === "America/New_York") {
        expect(
          screen.getByLabelText("Occurred before (exclusive, local time)"),
        ).toHaveProperty("value", "2026-11-01T01:30:00.123");
      }
      click("Search audit events");
      await screen.findByText(/No matching events/);
      expect(w.get.mock.calls[0]?.[1]).toMatchObject({
        params: {
          query: {
            action: receipt.action,
            targetType: receipt.targetType,
            targetId: receipt.targetId,
            occurredFrom: new Date(occurredFrom).toISOString(),
            occurredBefore: new Date(occurredBefore).toISOString(),
          },
        },
      });
      ui.rerender(
        <InvestigationProvider>
          <Audit {...w.props} access={{ ...administrator, permissions: [] }} />
        </InvestigationProvider>,
      );
      expect(screen.queryByText(/Explicit handoff/)).toBeNull();
      expect(screen.queryByText(/No matching events/)).toBeNull();
      expect(
        screen.getByRole("link", { name: "Review My access" }).parentElement
          ?.textContent,
      ).toContain("enforced. Review My access");
      expect(w.get).toHaveBeenCalledTimes(1);
    } finally {
      vi.unstubAllEnvs();
    }
  },
);

it("retains structured historical acknowledgement independently of the dialog completion callback", async () => {
  const w = screenWorkspace();
  const done = vi.fn();
  render(
    <OperationProvider
      access={administrator}
      view="settings"
      runtime={w.props.runtime}
    >
      <CommandDialog
        onFailure={w.props.onFailure}
        command={{
          title: "Save policy",
          description: "Save",
          run: async () => ({ message: "Saved once", receipt }),
        }}
        onClose={vi.fn()}
        onDone={done}
      />
    </OperationProvider>,
  );
  await act(async () => click("Confirm"));
  expect(done).toHaveBeenCalledWith("Saved once");
  expect(
    screen.getByRole("region", { name: "Acknowledged administration change" })
      .textContent,
  ).toContain("retry-attempt");
  expect(w.post).not.toHaveBeenCalled();
});
it("changes the receipt scope without remounting unrelated route state", () => {
  const w = screenWorkspace();
  function Workspace() {
    const context = useOperationContext()!;
    const [draft, setDraft] = useState("");
    return (
      <>
        <input
          aria-label="Preserved route draft"
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
        />
        <button onClick={() => context.acknowledge(receipt)}>
          Acknowledge change
        </button>
      </>
    );
  }
  const ui = render(
    <OperationProvider
      access={administrator}
      view="settings"
      runtime={w.props.runtime}
    >
      <Workspace />
    </OperationProvider>,
  );
  change("Preserved route draft", "Keep this private draft");
  click("Acknowledge change");
  expect(
    screen.getByRole("region", { name: "Acknowledged administration change" }),
  ).toBeTruthy();
  ui.rerender(
    <OperationProvider
      access={administrator}
      view="audit"
      runtime={w.props.runtime}
    >
      <Workspace />
    </OperationProvider>,
  );
  expect(screen.getByLabelText("Preserved route draft")).toHaveProperty(
    "value",
    "Keep this private draft",
  );
  expect(
    screen.queryByRole("region", {
      name: "Acknowledged administration change",
    }),
  ).toBeNull();
});
