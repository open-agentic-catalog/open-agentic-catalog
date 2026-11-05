import { revealEditor } from "../../tests/unit/reveal-editor";
// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { APIError } from "../api/client";
import { deferred } from "../../tests/unit/fixtures";
import Audit from "./Audit";
import { OperationProvider } from "./operation-context";
import {
  administrator,
  change as baseChange,
  click as baseClick,
  confirmCommand as baseConfirmCommand,
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
async function confirmCommand(name: string) {
  const target = screen.getAllByText(name, { selector: "button" })[0];
  if (target) revealEditor(target);
  await baseConfirmCommand(name);
}
screenLifecycle("/unit/audit");

it("requires audit permission and never fetches private events for a reader", async () => {
  const { props, get } = screenWorkspace();
  render(<Audit {...props} />);
  expect(screen.getByRole("alert").textContent).toContain(
    "audit.read or audit.export permission",
  );
  expect(get).not.toHaveBeenCalled();
});

it("captures filters on explicit search, retains them through paging and resets the cursor on re-search", async () => {
  const { props, get } = screenWorkspace();
  vi.spyOn(Date, "now").mockReturnValue(Date.parse(stamp));
  get.mockResolvedValue(
    etagReply({
      items: [
        {
          id: "event-1",
          action: "resource.created",
          at: stamp,
          targetType: "resource",
          targetId: "resource-1",
          actor: "principal",
          reason: "Created helper",
        },
        {
          id: "event-2",
          action: "custom.event",
          at: stamp,
          targetType: "listing",
          targetId: "listing-1",
          actor: "principal",
        },
      ],
      nextCursor: "next",
    }),
  );
  render(<Audit {...props} access={administrator} />);
  expect(get).not.toHaveBeenCalled();
  change("Activity", "resource.created");
  change("Activity", "");
  click("Search audit events");
  await screen.findByRole("heading", { name: "Audit events" });
  expect(screen.getByText("custom event")).toBeTruthy();
  click("Next page");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toEqual({
      params: {
        query: {
          cursor: "next",
          limit: 25,
          occurredFrom: new Date(
            Date.parse(stamp) - 24 * 3600000,
          ).toISOString(),
          occurredBefore: new Date(stamp).toISOString(),
        },
      },
      signal: expect.any(AbortSignal) as unknown,
    }),
  );
  change("Activity", "custom");
  change("Exact action", "custom.event");
  change("Time range", "custom");
  change("Occurred from (inclusive, local time)", "2026-08-01T00:00");
  change("Occurred before (exclusive, local time)", "2026-08-02T00:00");
  screen.getByText("Advanced filters: person, request or object").click();
  change("Attributed principal", "principal");
  change("Request ID", "request-1");
  change("Target type", "resource");
  click("Search audit events");
  await screen.findByText("Supply both target type and target ID.");
  expect(get).toHaveBeenCalledTimes(2);
  change("Target ID", "resource-1");
  get.mockResolvedValue(etagReply({ items: [] }));
  click("Search audit events");
  await screen.findByText(/No matching events/);
  expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
    params: {
      query: {
        action: "custom.event",
        actor: "principal",
        requestId: "request-1",
        targetType: "resource",
        targetId: "resource-1",
        occurredBefore: new Date("2026-08-02T00:00").toISOString(),
      },
    },
  });
  expect(get.mock.calls.at(-1)?.[1]).not.toHaveProperty("params.query.cursor");
});

it("exports exact server bytes with export-only access and releases its object URL on unmount", async () => {
  const { props, get, post } = screenWorkspace();
  const bytes = new Blob(['{"events":[]}']);
  post
    .mockRejectedValueOnce(new TypeError("Connection closed after send"))
    .mockResolvedValue(etagReply(bytes));
  const create = vi.fn(() => "blob:unit-audit"),
    revoke = vi.fn();
  const originalCreate = Object.getOwnPropertyDescriptor(
      URL,
      "createObjectURL",
    ),
    originalRevoke = Object.getOwnPropertyDescriptor(URL, "revokeObjectURL");
  Object.defineProperty(URL, "createObjectURL", {
    configurable: true,
    value: create,
  });
  Object.defineProperty(URL, "revokeObjectURL", {
    configurable: true,
    value: revoke,
  });
  let view: ReturnType<typeof render> | undefined;
  try {
    view = render(
      <Audit
        {...props}
        access={{
          ...administrator,
          scopeEnforcement: true,
          scopes: ["audit.export"],
        }}
      />,
    );
    change("Time range", "custom");
    click("Select export range");
    await screen.findByRole("button", { name: "Prepare audit export" });
    expect(
      (
        screen.getByRole("button", {
          name: "Prepare audit export",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    change("Occurred from (inclusive, local time)", "2026-08-01T00:00");
    change("Occurred before (exclusive, local time)", "2026-08-02T00:00");
    click("Select export range");
    change("Reason", " Investigation ");
    const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
    click("Select export range");
    expect((screen.getByLabelText("Reason") as HTMLTextAreaElement).value).toBe(
      " Investigation ",
    );
    expect(discard).toHaveBeenCalledTimes(1);
    expect(post).not.toHaveBeenCalled();
    click("Prepare audit export");
    const summary = screen.getByLabelText("Command summary");
    expect(summary.textContent).toContain(
      new Date("2026-08-01T00:00").toISOString(),
    );
    expect(summary.textContent).toContain(
      new Date("2026-08-02T00:00").toISOString(),
    );
    expect(summary.textContent).toContain("Investigation");
    expect(post).not.toHaveBeenCalled();
    click("Cancel");
    await confirmCommand("Prepare audit export");
    await screen.findByRole("button", { name: "Retry same command" });
    const uncertain = post.mock.calls[0];
    view.rerender(
      <Audit {...props} access={{ ...administrator, permissions: [] }} />,
    );
    expect(
      screen.getByRole("button", { name: "Retry same command" }),
    ).toHaveProperty("disabled", false);
    expect(post).toHaveBeenCalledTimes(1);
    expect(get).not.toHaveBeenCalled();
    view.rerender(
      <Audit
        {...props}
        access={{ ...administrator, authorizationRevision: 99 }}
      />,
    );
    expect(post).toHaveBeenCalledTimes(1);
    click("Retry same command");
    const link = await screen.findByRole("link", {
      name: "Download exact audit export",
    });
    expect(link.getAttribute("href")).toBe("blob:unit-audit");
    expect(link.getAttribute("download")).toBe("catalog-audit-export.json");
    expect(create).toHaveBeenCalledWith(bytes);
    expect(post.mock.calls[1]).toEqual(uncertain);
    expect(get).not.toHaveBeenCalled();
    expect(post.mock.calls[0]).toMatchObject([
      "/api/v1/audit-exports",
      { body: { reason: "Investigation" }, parseAs: "blob" },
    ]);
    view.rerender(
      <Audit {...props} access={{ ...administrator, permissions: [] }} />,
    );
    expect(
      screen.queryByRole("link", { name: "Download exact audit export" }),
    ).toBeNull();
    await waitFor(() => expect(revoke).toHaveBeenCalledWith("blob:unit-audit"));
    view.rerender(<Audit {...props} access={administrator} />);
    expect(
      screen.queryByRole("link", { name: "Download exact audit export" }),
    ).toBeNull();
    expect(create).toHaveBeenCalledTimes(1);
    expect(post).toHaveBeenCalledTimes(2);
    expect(get).not.toHaveBeenCalled();
    view.unmount();
    expect(revoke).toHaveBeenCalledWith("blob:unit-audit");
  } finally {
    // Effects must release the URL before restoring the browser API, even
    // when an earlier assertion fails.
    view?.unmount();
    if (originalCreate)
      Object.defineProperty(URL, "createObjectURL", originalCreate);
    else Reflect.deleteProperty(URL, "createObjectURL");
    if (originalRevoke)
      Object.defineProperty(URL, "revokeObjectURL", originalRevoke);
    else Reflect.deleteProperty(URL, "revokeObjectURL");
  }
});

it("retains a protected export draft and prepared range through access loss without rereading private evidence", async () => {
  const { props, get, post } = screenWorkspace();
  const event = {
    id: "old-event",
    action: "resource.created",
    at: stamp,
    targetType: "resource",
    targetId: "private-target",
    actor: "private-principal",
    reason: "Private event evidence",
  };
  get.mockResolvedValue(
    etagReply({ items: [event], nextCursor: "old-policy-cursor" }),
  );
  const ui = render(<Audit {...props} access={administrator} />);
  click("Search audit events");
  await screen.findByText(event.reason);
  click("Next page");
  await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
  change("Reason", "Retain this separate export");
  click("Prepare audit export");
  const summary = screen.getByLabelText("Command summary").textContent;
  ui.rerender(
    <Audit
      {...props}
      access={{ ...administrator, permissions: [], authorizationRevision: 8 }}
    />,
  );
  expect(screen.queryByText(event.reason)).toBeNull();
  expect(screen.queryByText(event.actor)).toBeNull();
  expect(screen.getByLabelText("Reason")).toHaveProperty(
    "value",
    "Retain this separate export",
  );
  expect(screen.getByLabelText("Reason").matches(":disabled")).toBe(true);
  expect(screen.getByLabelText("Command summary").textContent).toBe(summary);
  expect(screen.getByRole("button", { name: "Confirm" })).toHaveProperty(
    "disabled",
    true,
  );
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
  expect(post).not.toHaveBeenCalled();
  expect(get).toHaveBeenCalledTimes(2);
  ui.rerender(
    <Audit
      {...props}
      access={{ ...administrator, authorizationRevision: 9 }}
    />,
  );
  expect(screen.queryByText(event.reason)).toBeNull();
  expect(screen.getByLabelText("Reason")).toHaveProperty(
    "value",
    "Retain this separate export",
  );
  expect(screen.getByLabelText("Command summary").textContent).toBe(summary);
  expect(screen.getByRole("button", { name: "Confirm" })).toHaveProperty(
    "disabled",
    false,
  );
  expect(get).toHaveBeenCalledTimes(2);
  click("Cancel");
  vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Search audit events");
  expect(get).toHaveBeenCalledTimes(2);
  expect(screen.getByLabelText("Reason")).toHaveProperty(
    "value",
    "Retain this separate export",
  );
  vi.mocked(window.confirm).mockReturnValue(true);
  get.mockResolvedValue(etagReply({ items: [] }));
  click("Search audit events");
  await screen.findByText(/No matching events/);
  expect(get).toHaveBeenCalledTimes(3);
  expect(get.mock.calls[2]?.[1]).not.toHaveProperty("params.query.cursor");
  expect(screen.getByLabelText("Reason")).toHaveProperty("value", "");
  change("Reason", "Clear this only for a replacement identity");
  click("Prepare audit export");
  ui.rerender(
    <Audit {...props} access={{ ...administrator, subject: "other-user" }} />,
  );
  expect(screen.queryByLabelText("Reason")).toBeNull();
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(screen.queryByText(/No matching events/)).toBeNull();
  expect(get).toHaveBeenCalledTimes(3);
});

it("keeps uncertain export recovery exact while current backend authorization can refuse it", async () => {
  const { props, get, post } = screenWorkspace();
  get.mockResolvedValue(etagReply({ items: [] }));
  post
    .mockRejectedValueOnce(new TypeError("Reply lost"))
    .mockRejectedValueOnce(new APIError(403, "FORBIDDEN"));
  const ui = render(<Audit {...props} access={administrator} />);
  click("Search audit events");
  await screen.findByText(/No matching events/);
  change("Reason", "Recover only this captured command");
  await confirmCommand("Prepare audit export");
  await screen.findByRole("button", { name: "Retry same command" });
  ui.rerender(
    <Audit {...props} access={{ ...administrator, permissions: [] }} />,
  );
  click("Retry same command");
  await screen.findByText(/Your current access does not permit this action/);
  expect(post).toHaveBeenCalledTimes(2);
  expect(post.mock.calls[1]).toEqual(post.mock.calls[0]);
  expect(
    screen.queryByRole("button", { name: "Retry same command" }),
  ).toBeNull();
  expect(
    screen.queryByRole("link", { name: "Download exact audit export" }),
  ).toBeNull();
  expect(get).toHaveBeenCalledTimes(1);
});

it("withholds an export response received after its access context changes", async () => {
  const { props, get, post } = screenWorkspace();
  const pending = deferred<ReturnType<typeof etagReply<Blob>>>();
  get.mockResolvedValue(etagReply({ items: [] }));
  post.mockReturnValue(pending.promise);
  const ui = render(
    <OperationProvider
      access={administrator}
      runtime={props.runtime}
      view="audit"
    >
      <Audit {...props} access={administrator} />
    </OperationProvider>,
  );
  click("Search audit events");
  await screen.findByText(/No matching events/);
  change("Reason", "Pending export");
  await confirmCommand("Prepare audit export");
  ui.rerender(
    <OperationProvider
      access={{ ...administrator, permissions: [] }}
      runtime={props.runtime}
      view="audit"
    >
      <Audit {...props} access={{ ...administrator, permissions: [] }} />
    </OperationProvider>,
  );
  ui.rerender(
    <OperationProvider
      access={administrator}
      runtime={props.runtime}
      view="audit"
    >
      <Audit {...props} access={administrator} />
    </OperationProvider>,
  );
  await act(async () =>
    pending.resolve(etagReply(new Blob(["private bytes"]))),
  );
  await screen.findByText(/Audit export completed. Access changed/);
  expect(
    screen.queryByRole("link", { name: "Download exact audit export" }),
  ).toBeNull();
  expect(
    screen.queryByRole("region", {
      name: "Acknowledged administration change",
    }),
  ).toBeNull();
  ui.rerender(
    <OperationProvider
      access={administrator}
      runtime={props.runtime}
      view="audit"
    >
      <Audit {...props} access={administrator} />
    </OperationProvider>,
  );
  expect(
    screen.queryByRole("link", { name: "Download exact audit export" }),
  ).toBeNull();
  expect(post).toHaveBeenCalledTimes(1);
  expect(get).toHaveBeenCalledTimes(1);
});
