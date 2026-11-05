// @vitest-environment jsdom
import { useEffect, useState } from "react";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { App } from "../shell/App";
import type { Access, Client, Runtime } from "../api/client";
const appTransport = vi.hoisted(() => ({
  current: undefined as Client | undefined,
  access: undefined as Access | undefined,
}));
vi.mock("../api/client", async (original) => {
  const actual = await original<typeof import("../api/client")>();
  return {
    ...actual,
    makeAPI: (runtime: Runtime) => ({
      ...actual.makeAPI(runtime),
      ...appTransport.current,
      auth: async (path: string) =>
        path === "session" ? { access: appTransport.access } : { users: [] },
    }),
  };
});
import DiscoveryWorkspace from "./DiscoveryWorkspace";
import {
  evidence,
  versionRevision,
  listingRevision,
  release,
} from "../../tests/unit/responses";
import {
  resource,
  version,
  listing,
  screenLifecycle,
  screenWorkspace,
  stamp,
  click,
} from "../../tests/unit/screens";
import { reply } from "../../tests/unit/fixtures";
import type { WorkspaceProps } from "./workspace";

screenLifecycle("/unit/saved-items/saved-1");
const record = {
  resource,
  version,
  listing,
  versionRevision,
  listingRevision,
  evidence,
  release: { ...release, id: "00000000-0000-7000-8000-000000000001" },
};
const local = {
  id: "local",
  type: "local",
  identifier: resource.canonicalIdentifier,
  kind: resource.kind,
  versionPresent: true,
  version: version.version,
  displayName: "Helper",
  tags: [],
  local: record,
};
const item = {
  id: "saved-1",
  tenantId: "tenant-a",
  type: "search",
  label: "Private search",
  search: { text: "saved", origins: ["local"] },
  revision: 1,
  createdAt: stamp,
  updatedAt: stamp,
};

// The shell's existing event protocol, with no application server or browser process.
function Shell(props: WorkspaceProps) {
  const [path, setPath] = useState(location.pathname);
  useEffect(() => {
    const moved = (event: PopStateEvent) => {
      if (
        window.dispatchEvent(
          new CustomEvent("catalog-ui:navigate", {
            cancelable: true,
            detail: event.state as unknown,
          }),
        )
      )
        setPath(location.pathname);
    };
    window.addEventListener("popstate", moved);
    return () => window.removeEventListener("popstate", moved);
  }, []);
  return (
    <DiscoveryWorkspace
      key={JSON.stringify(props.access)}
      {...props}
      id={path.split("/").at(-1)!}
    />
  );
}

function setup() {
  const w = screenWorkspace();
  w.get.mockImplementation(async (path) =>
    reply(path === "/api/v1/personal/items/{itemId}" ? item : record),
  );
  w.post.mockResolvedValue(
    reply({
      tier: "core",
      items: [local],
      durableTotal: 1,
      liveCount: 0,
      partial: false,
    }),
  );
  return w;
}

it.each(["event fixture", "actual App"])(
  "%s retains one staged search owner through exact detail and Back without reloading saved criteria",
  async (shell) => {
    const w = setup();
    w.post.mockResolvedValue(
      reply({
        tier: "core",
        items: [local],
        durableTotal: 2,
        liveCount: 0,
        partial: false,
        nextCursor: "second-page",
      }),
    );
    // jsdom has no scrolling; the real-browser saved-search journey owns geometry/history.
    vi.spyOn(window, "scrollTo").mockImplementation(() => {});
    appTransport.current = w.props.client;
    appTransport.access = w.props.access;
    render(
      shell === "actual App" ? (
        <App runtime={w.props.runtime} />
      ) : (
        <Shell {...w.props} />
      ),
    );
    await screen.findByText("Private search");
    click("Open search editor");
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe(
      "Saved search",
    );
    expect(w.post).not.toHaveBeenCalled();
    fireEvent.click(
      screen.getByRole("checkbox", { name: /configured embedding provider/ }),
    );
    click("Search");
    await screen.findByRole("button", { name: "Helper" });
    click(/Next page/);
    await waitFor(() => expect(w.post).toHaveBeenCalledTimes(2));
    click("Table");
    fireEvent.change(
      screen.getByRole("searchbox", { name: "Search Catalog" }),
      {
        target: { value: "unsubmitted" },
      },
    );
    click("Helper");
    await screen.findByText("Use this resource");
    fireEvent.click(screen.getByRole("link", { name: /Open full record/ }));
    await screen.findByRole("heading", { level: 1, name: "Helper" });
    expect(w.post).toHaveBeenCalledTimes(2);
    await act(async () => history.back());
    await screen.findByRole("heading", { level: 1, name: "Saved search" });
    await screen.findByRole("table");
    expect(
      screen.getByRole("searchbox", { name: "Search Catalog" }),
    ).toHaveProperty("value", "unsubmitted");
    expect(w.post).toHaveBeenCalledTimes(3);
    expect(w.post.mock.calls[2]).toMatchObject([
      "/api/v1/catalog/search",
      {
        body: {
          text: "saved",
          origins: ["local"],
          limit: 24,
          cursor: "second-page",
        },
      },
    ]);
    expect(screen.getByRole("button", { name: "Previous page" })).toBeTruthy();
    expect(
      w.get.mock.calls.filter(
        ([path]) => path === "/api/v1/personal/items/{itemId}",
      ),
    ).toHaveLength(1);
    fireEvent.click(
      screen.getByRole("checkbox", { name: /configured embedding provider/ }),
    );
    await act(async () => history.forward());
    await screen.findByRole("heading", { level: 1, name: "Helper" });
    await act(async () => history.back());
    await screen.findByRole("heading", { level: 1, name: "Saved search" });
    expect(w.post).toHaveBeenCalledTimes(3);
    expect(screen.queryByRole("table")).toBeNull();
    fireEvent.click(
      screen.getByRole("checkbox", { name: /configured embedding provider/ }),
    );
    expect(w.post).toHaveBeenCalledTimes(3);
    click("Search");
    await waitFor(() => expect(w.post).toHaveBeenCalledTimes(4));
    expect(
      (w.post.mock.calls[3]![1] as { body: unknown }).body,
    ).not.toHaveProperty("cursor");
  },
);

it("stages fresh criteria after another library item or access replacement", async () => {
  const w = setup();
  const view = render(<Shell {...w.props} />);
  await screen.findByText("Private search");
  click("Open search editor");
  fireEvent.click(
    screen.getByRole("checkbox", { name: /configured embedding provider/ }),
  );
  view.rerender(
    <Shell
      {...w.props}
      access={{ ...w.props.access, membershipId: "new-membership" }}
    />,
  );
  await screen.findByRole("button", { name: "Open search editor" });
  expect(screen.queryByRole("searchbox")).toBeNull();
  click("Open search editor");
  expect(
    screen.getByRole("checkbox", { name: /configured embedding provider/ }),
  ).toHaveProperty("checked", false);
  w.get.mockResolvedValue(
    reply({ ...item, id: "saved-2", label: "Other saved search" }),
  );
  await act(async () => {
    history.pushState(null, "", "/unit/saved-items/saved-2");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await screen.findByText("Other saved search");
  expect(screen.queryByRole("searchbox")).toBeNull();
  click("Open search editor");
  expect(w.post).not.toHaveBeenCalled();
});
