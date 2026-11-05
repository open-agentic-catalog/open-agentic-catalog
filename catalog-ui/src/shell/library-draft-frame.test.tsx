// @vitest-environment jsdom
import { useState } from "react";
import { afterEach, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { APIError, type Access } from "../api/client";
import { sessionAccess, workspace } from "../../tests/unit/fixtures";
import { App } from "./App";
const { auth } = vi.hoisted(() => ({ auth: vi.fn() }));
vi.mock("../api/client", async (original) => {
  const actual = await original<typeof import("../api/client")>();
  return {
    ...actual,
    makeAPI: (runtime: import("../api/client").Runtime) => ({
      ...actual.makeAPI(runtime),
      auth,
    }),
  };
});
// Real shell lifetime with a small stateful editor; owning component suites
// separately verify current read/action guards and private-evidence clearing.
function DraftWorkspace({
  access,
  onFailure,
}: {
  access: Access;
  onFailure: (error: unknown) => void;
}) {
  const [draft, setDraft] = useState("");
  return (
    <>
      <h1>Entered editor</h1>
      <input
        aria-label="Saved label"
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
      />
      <p>Current scopes: {access.scopes.join(",")}</p>
      <p>Current permissions: {access.permissions.join(",")}</p>
      <button onClick={() => onFailure(new APIError(403, "FORBIDDEN"))}>
        Check current access
      </button>
    </>
  );
}
vi.mock("../views/DiscoveryWorkspace", () => ({ default: DraftWorkspace }));
vi.mock("../views/Resources", () => ({ default: DraftWorkspace }));
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
function start(path: string) {
  history.replaceState(null, "", `/unit${path}`);
  vi.stubGlobal(
    "BroadcastChannel",
    class {
      close() {}
      postMessage() {}
    },
  );
  let access: Access = path.startsWith("/resources")
    ? {
        ...sessionAccess,
        permissions: ["resource.create"],
        availablePermissions: ["resource.create"],
        scopeEnforcement: true,
        scopes: ["catalog.write"],
      }
    : {
        ...sessionAccess,
        scopeEnforcement: true,
        scopes: ["catalog.read", "personal.write"],
      };
  auth.mockImplementation(async (path: string) =>
    path === "session" ? { access } : { users: [] },
  );
  return {
    get access() {
      return access;
    },
    set access(value: Access) {
      access = value;
    },
  };
}
it.each([
  ["/saved-items/saved-1", "personal.write"],
  ["/saved-items/saved-1", "catalog.read"],
  ["/saved-items/new", "catalog.read"],
  ["/resources/new", "catalog.write"],
] as const)(
  "retains an entered %s draft when %s is lost, and clears it on identity replacement",
  async (path, removedScope) => {
    const current = start(path);
    render(<App runtime={workspace().props.runtime} />);
    await screen.findByRole("heading", { name: "Entered editor" });
    fireEvent.change(screen.getByLabelText("Saved label"), {
      target: { value: "My unsent label" },
    });
    current.access = {
      ...current.access,
      scopes: current.access.scopes.filter((scope) => scope !== removedScope),
    };
    fireEvent.click(
      screen.getByRole("button", { name: "Check current access" }),
    );
    await waitFor(() =>
      expect(
        screen.getByText(
          `Current scopes: ${current.access.scopes.join(",")}`.trimEnd(),
        ),
      ).toBeTruthy(),
    );
    expect(screen.getByLabelText("Saved label")).toHaveProperty(
      "value",
      "My unsent label",
    );
    current.access = { ...current.access, tenantId: "other-tenant" };
    fireEvent.click(
      screen.getByRole("button", { name: "Check current access" }),
    );
    if (removedScope === "personal.write") {
      await waitFor(() =>
        expect(screen.getByLabelText("Saved label")).toHaveProperty(
          "value",
          "",
        ),
      );
    } else {
      await screen.findByRole("alert");
      expect(screen.queryByLabelText("Saved label")).toBeNull();
    }
  },
);
it.each(["/saved-items/saved-1", "/resources/new"])(
  "retains an entered %s editor on grant loss, but denies a different target",
  async (path) => {
    const current = start(path);
    render(<App runtime={workspace().props.runtime} />);
    await screen.findByRole("heading", { name: "Entered editor" });
    fireEvent.change(screen.getByLabelText("Saved label"), {
      target: { value: "Unsent protected input" },
    });
    current.access = {
      ...current.access,
      permissions: [],
      availablePermissions: [],
    };
    fireEvent.click(
      screen.getByRole("button", { name: "Check current access" }),
    );
    await waitFor(() =>
      expect(screen.getByText("Current permissions:")).toBeTruthy(),
    );
    expect(screen.getByLabelText("Saved label")).toHaveProperty(
      "value",
      "Unsent protected input",
    );
    history.pushState(
      null,
      "",
      `/unit${path.startsWith("/resources") ? "/resources/another-resource" : "/saved-items/another-item"}`,
    );
    fireEvent(window, new PopStateEvent("popstate"));
    await screen.findByRole("alert");
    expect(screen.queryByLabelText("Saved label")).toBeNull();
  },
);
it.each(["/saved-items/saved-1", "/saved-items/new", "/resources/new"])(
  "never admits an initially denied %s editor",
  async (path) => {
    const current = start(path);
    current.access = {
      ...current.access,
      permissions: [],
      availablePermissions: [],
    };
    render(<App runtime={workspace().props.runtime} />);
    await screen.findByRole("alert");
    expect(screen.queryByLabelText("Saved label")).toBeNull();
  },
);
it("does not retain a private saved-items list after its read grant is lost", async () => {
  const current = start("/saved-items");
  render(<App runtime={workspace().props.runtime} />);
  await screen.findByRole("heading", { name: "Entered editor" });
  current.access = {
    ...current.access,
    permissions: [],
    availablePermissions: [],
  };
  fireEvent.click(screen.getByRole("button", { name: "Check current access" }));
  await screen.findByRole("alert");
  expect(screen.queryByLabelText("Saved label")).toBeNull();
});
