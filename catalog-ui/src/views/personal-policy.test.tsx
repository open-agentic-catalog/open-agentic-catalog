// @vitest-environment jsdom
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { APIError } from "../api/client";
import { MyWork, PersonalLibrary } from "./Personal";
import DiscoveryWorkspace from "./DiscoveryWorkspace";
import { deferred } from "../../tests/unit/fixtures";
import {
  change,
  confirmCommand,
  etagReply,
  screenLifecycle,
  screenWorkspace,
  stamp,
} from "../../tests/unit/screens";

screenLifecycle("/unit/saved-items/saved-1");
const item = {
  id: "saved-1",
  type: "search" as const,
  label: "Previously loaded private label",
  search: {
    text: "Previously loaded private query",
    origins: ["local" as const],
  },
  revision: 1,
  createdAt: stamp,
  updatedAt: stamp,
};

it("does not direct an empty My work queue to unavailable Authoring", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply({ items: [] }));
  render(
    <MyWork
      {...w.props}
      access={{
        ...w.props.access,
        permissions: [],
        availablePermissions: [],
        namespaceGrants: [],
      }}
    />,
  );
  await screen.findByText("No drafts to continue.");
  expect(screen.queryByRole("link", { name: "Open Authoring" })).toBeNull();
  expect(screen.queryByText(/Start in Authoring/)).toBeNull();
  expect(w.get.mock.calls[0]?.[0]).toBe("/api/v1/me/work");
  expect(w.post).not.toHaveBeenCalled();
});

it("keeps create-only saved-search authority separate from reads and retains a frozen command on grant loss", async () => {
  const w = screenWorkspace();
  const ui = render(<PersonalLibrary {...w.props} id="new" />);
  change("Saved label", "New entered label");
  change("Saved search text", "New entered private query");
  const label = screen.getByLabelText("Saved label");
  fireEvent.click(screen.getByRole("button", { name: "Save named search" }));
  const summary = screen.getByLabelText("Command summary").textContent;
  ui.rerender(
    <PersonalLibrary
      {...w.props}
      id="new"
      access={{
        ...w.props.access,
        scopeEnforcement: true,
        scopes: ["personal.write"],
      }}
    />,
  );
  expect(screen.getByLabelText("Saved label")).toBe(label);
  expect(label).toHaveProperty("value", "New entered label");
  expect(label.matches(":disabled")).toBe(true);
  expect(screen.getByRole("button", { name: "Confirm" })).toHaveProperty(
    "disabled",
    false,
  );
  expect(screen.getByLabelText("Command summary").textContent).toBe(summary);
  expect(w.get).not.toHaveBeenCalled();
  ui.rerender(
    <PersonalLibrary
      {...w.props}
      id="new"
      access={{ ...w.props.access, permissions: [] }}
    />,
  );
  expect(screen.getByRole("button", { name: "Confirm" })).toHaveProperty(
    "disabled",
    true,
  );
  fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
  expect(w.post).not.toHaveBeenCalled();
  expect(w.get).not.toHaveBeenCalled();
  ui.rerender(<PersonalLibrary {...w.props} id="new" />);
  expect(screen.getByRole("button", { name: "Confirm" })).toHaveProperty(
    "disabled",
    false,
  );
  expect(screen.getByLabelText("Command summary").textContent).toBe(summary);
});

it("keeps entered criteria and a prepared update while read authority is lost, then requires a deliberate current read", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply(item));
  const ui = render(<PersonalLibrary {...w.props} id={item.id} />);
  await screen.findByRole("heading", { name: item.label });
  change("Saved label", "Entered private rename");
  change("Saved search text", "Entered private criteria");
  const label = screen.getByLabelText("Saved label");
  fireEvent.click(screen.getByRole("button", { name: "Update saved item" }));
  const captured = screen.getByLabelText("Command summary").textContent;
  for (const access of [
    { ...w.props.access, permissions: [] },
    {
      ...w.props.access,
      scopeEnforcement: true,
      scopes: ["personal.write" as const],
    },
  ]) {
    ui.rerender(<PersonalLibrary {...w.props} id={item.id} access={access} />);
    expect(screen.getByLabelText("Saved label")).toBe(label);
    expect(label).toHaveProperty("value", "Entered private rename");
    expect(label.matches(":disabled")).toBe(true);
    expect(screen.queryByRole("heading", { name: item.label })).toBeNull();
    expect(
      screen.queryByText("Last loaded saved criteria or reference"),
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Open search editor" }),
    ).toBeNull();
    expect(screen.getByLabelText("Command summary").textContent).toBe(captured);
    expect(screen.getByRole("button", { name: "Confirm" })).toHaveProperty(
      "disabled",
      true,
    );
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    expect(w.patch).not.toHaveBeenCalled();
    expect(w.get).toHaveBeenCalledTimes(1);
  }
  ui.rerender(<PersonalLibrary {...w.props} id={item.id} />);
  expect(screen.getByRole("button", { name: "Confirm" })).toHaveProperty(
    "disabled",
    true,
  );
  expect(screen.queryByRole("heading", { name: item.label })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  fireEvent.click(
    screen.getByRole("button", { name: "Read current saved item" }),
  );
  expect(w.get).toHaveBeenCalledTimes(1);
  discard.mockReturnValue(true);
  fireEvent.click(
    screen.getByRole("button", { name: "Read current saved item" }),
  );
  await screen.findByRole("heading", { name: item.label });
  expect(w.get).toHaveBeenCalledTimes(2);
  expect(screen.getByLabelText("Saved label")).toBe(label);
  expect(screen.getByLabelText("Saved search text")).toHaveProperty(
    "value",
    "Entered private criteria",
  );
  expect(
    screen.getByRole("button", { name: "Update saved item" }),
  ).toHaveProperty("disabled", false);
  expect(w.post).not.toHaveBeenCalled();
  expect(w.remove).not.toHaveBeenCalled();
});

it.each(["refused", "acknowledged"] as const)(
  "retains the exact uncertain update through read loss when backend recovery is %s",
  async (outcome) => {
    const w = screenWorkspace();
    w.get.mockResolvedValue(etagReply(item));
    w.patch.mockRejectedValueOnce(new TypeError("Reply lost"));
    if (outcome === "refused")
      w.patch.mockRejectedValueOnce(new APIError(403, "FORBIDDEN"));
    else w.patch.mockResolvedValueOnce(etagReply({ id: item.id, revision: 2 }));
    const ui = render(<PersonalLibrary {...w.props} id={item.id} />);
    await screen.findByRole("heading", { name: item.label });
    change("Saved label", "Captured rename");
    await confirmCommand("Update saved item");
    await screen.findByRole("button", { name: "Retry same command" });
    ui.rerender(
      <PersonalLibrary
        {...w.props}
        id={item.id}
        access={{ ...w.props.access, permissions: [] }}
      />,
    );
    const retry = screen.getByRole("button", { name: "Retry same command" });
    expect(retry).toHaveProperty("disabled", false);
    expect(w.patch).toHaveBeenCalledTimes(1);
    fireEvent.click(retry);
    if (outcome === "refused")
      await screen.findByText(
        /Your current access does not permit this action/,
      );
    else await screen.findByText("Private item saved.");
    expect(w.patch).toHaveBeenCalledTimes(2);
    expect(w.patch.mock.calls[1]).toEqual(w.patch.mock.calls[0]);
    expect(w.patch.mock.calls[1]).toMatchObject([
      "/api/v1/personal/items/{itemId}",
      {
        params: {
          path: { itemId: item.id },
          header: { "If-Match": '"revision-1"' },
        },
        body: { label: "Captured rename", search: item.search },
      },
    ]);
    expect(w.get).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("heading", { name: item.label })).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Open search editor" }),
    ).toBeNull();
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each(["success", "failure"] as const)(
  "ignores an initial private read %s after access changes away and back",
  async (outcome) => {
    const w = screenWorkspace();
    const pending = deferred<ReturnType<typeof etagReply<typeof item>>>();
    w.get
      .mockReturnValueOnce(pending.promise)
      .mockResolvedValue(etagReply(item));
    const ui = render(<PersonalLibrary {...w.props} id={item.id} />);
    await waitFor(() => expect(w.get).toHaveBeenCalledTimes(1));
    const request = w.get.mock.calls[0]?.[1] as { signal: AbortSignal };
    ui.rerender(
      <PersonalLibrary
        {...w.props}
        id={item.id}
        access={{ ...w.props.access, permissions: [] }}
      />,
    );
    expect(request.signal.aborted).toBe(true);
    ui.rerender(<PersonalLibrary {...w.props} id={item.id} />);
    await act(async () => {
      if (outcome === "success") pending.resolve(etagReply(item));
      else pending.reject(new APIError(403, "FORBIDDEN", "old-policy-request"));
    });
    expect(screen.queryByRole("heading", { name: item.label })).toBeNull();
    expect(screen.queryByLabelText("Saved label")).toBeNull();
    expect(w.get).toHaveBeenCalledTimes(1);
    expect(w.props.onFailure).not.toHaveBeenCalled();
    expect(screen.queryByText(/old-policy-request/)).toBeNull();
    fireEvent.click(
      screen.getByRole("button", { name: "Read current saved item" }),
    );
    await screen.findByRole("heading", { name: item.label });
    expect(w.get).toHaveBeenCalledTimes(2);
  },
);

it("clears resolved private targets immediately and ignores their late resolution after a policy round trip", async () => {
  const w = screenWorkspace();
  const reference = {
    origin: "local" as const,
    releaseId: "release-1",
    versionRevisionId: "revision-1",
  };
  const saved = { ...item, type: "resource", search: undefined, reference };
  w.get.mockResolvedValue(etagReply(saved));
  const pending = deferred<ReturnType<typeof etagReply>>();
  w.post
    .mockResolvedValueOnce(etagReply({ status: "available", reference }))
    .mockReturnValueOnce(pending.promise);
  const ui = render(<PersonalLibrary {...w.props} id={item.id} />);
  fireEvent.click(
    await screen.findByRole("button", { name: "Check saved reference" }),
  );
  await screen.findByRole("link", { name: "Open exact saved release" });
  fireEvent.click(
    screen.getByRole("button", { name: "Check saved reference" }),
  );
  const request = w.post.mock.calls[1]?.[1] as { signal: AbortSignal };
  ui.rerender(
    <PersonalLibrary
      {...w.props}
      id={item.id}
      access={{ ...w.props.access, permissions: [] }}
    />,
  );
  expect(request.signal.aborted).toBe(true);
  expect(
    screen.queryByRole("link", { name: "Open exact saved release" }),
  ).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Check saved reference" }),
  ).toBeNull();
  ui.rerender(<PersonalLibrary {...w.props} id={item.id} />);
  await act(async () =>
    pending.resolve(etagReply({ status: "available", reference })),
  );
  expect(
    screen.queryByRole("link", { name: "Open exact saved release" }),
  ).toBeNull();
  expect(w.post).toHaveBeenCalledTimes(2);
  expect(w.get).toHaveBeenCalledTimes(1);
  fireEvent.click(
    screen.getByRole("button", { name: "Read current saved item" }),
  );
  await screen.findByRole("button", { name: "Check saved reference" });
  expect(
    screen.queryByRole("link", { name: "Open exact saved release" }),
  ).toBeNull();
});

it("disarms a staged saved Search on library read loss without executing or rereading it", async () => {
  const w = screenWorkspace();
  w.get.mockResolvedValue(etagReply(item));
  const ui = render(<DiscoveryWorkspace {...w.props} id={item.id} />);
  fireEvent.click(
    await screen.findByRole("button", { name: "Open search editor" }),
  );
  expect(
    screen.getByRole("searchbox", { name: "Search Catalog" }),
  ).toHaveProperty("value", item.search.text);
  ui.rerender(
    <DiscoveryWorkspace
      {...w.props}
      id={item.id}
      access={{ ...w.props.access, permissions: [] }}
    />,
  );
  await act(async () => {});
  expect(screen.queryByRole("searchbox")).toBeNull();
  expect(screen.queryByLabelText("Saved label")).toBeNull();
  expect(w.get).toHaveBeenCalledTimes(1);
  expect(w.post).not.toHaveBeenCalled();
  ui.rerender(<DiscoveryWorkspace {...w.props} id={item.id} />);
  expect(screen.queryByRole("searchbox")).toBeNull();
  expect(w.get).toHaveBeenCalledTimes(1);
  fireEvent.click(
    screen.getByRole("button", { name: "Read current saved item" }),
  );
  fireEvent.click(
    await screen.findByRole("button", { name: "Open search editor" }),
  );
  expect(w.get).toHaveBeenCalledTimes(2);
  expect(w.post).not.toHaveBeenCalled();
});
