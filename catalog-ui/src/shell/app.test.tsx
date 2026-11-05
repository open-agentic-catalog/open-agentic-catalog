import { testAccess, testPermissions } from "../../tests/unit/access-fixtures";
import { required } from "../../tests/required";
// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { StrictMode } from "react";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
  waitFor,
} from "@testing-library/react";
import { APIError } from "../api/client";
import type { Access } from "../api/client";
import { sessionAccess as reader, deferred } from "../../tests/unit/fixtures";
import { App } from "./App";

vi.mock("../views/Service", () => ({
  default: () => <h1>Service information content</h1>,
}));

const { auth, invalidate } = vi.hoisted(() => ({
  auth: vi.fn<(...args: [string, ...unknown[]]) => Promise<unknown>>(),
  invalidate: vi.fn(),
}));
vi.mock("../api/client", async (original) => {
  const actual = await original<typeof import("../api/client")>();
  return {
    ...actual,
    makeAPI: (runtime: import("../api/client").Runtime) => {
      const client = actual.makeAPI(runtime);
      return {
        auth,
        get signal() {
          return client.signal;
        },
        invalidate: () => {
          invalidate();
          client.invalidate();
        },
      };
    },
  };
});
// Test shell-owned session/navigation behavior independently of page requests.
vi.mock("../views/Catalog", () => ({
  default: ({ onFailure }: { onFailure: (e: unknown) => void }) => (
    <section>
      <h1>Catalog content</h1>
      <button onClick={() => onFailure(new APIError(401, "expired"))}>
        Expired read
      </button>
      <button onClick={() => onFailure(new APIError(403, "denied"))}>
        Denied read
      </button>
      <button
        onClick={() =>
          onFailure(
            new APIError(503, "DEPENDENCY_UNAVAILABLE", "response-local-1"),
          )
        }
      >
        Dependency failure
      </button>
    </section>
  ),
}));
vi.mock("../views/Help", () => ({
  default: ({
    page,
    recent,
  }: {
    page: string;
    recent?: { requestId: string };
  }) => (
    <>
      <h1 tabIndex={-1}>
        {page === "about"
          ? "About"
          : page === "troubleshooting"
            ? "Troubleshooting"
            : "Getting started"}
      </h1>
      {recent && <p>Diagnostic reference: {recent.requestId}</p>}
    </>
  ),
}));

const runtime = {
  basePath: "/unit",
  authMode: "development" as const,
  documentationUrl: "https://docs.example.com/",
};
const signedIn = () => ({
  access: reader,
  expiresAt: Math.floor(Date.now() / 1000) + 600,
});
const channels: {
  onmessage?: (event: { data: { sender?: string } }) => void;
  close: ReturnType<typeof vi.fn>;
  postMessage: ReturnType<typeof vi.fn<(message: { sender: string }) => void>>;
}[] = [];

beforeEach(() => {
  history.replaceState(null, "", "/unit/catalog");
  localStorage.clear();
  channels.length = 0;
  vi.stubGlobal(
    "BroadcastChannel",
    class {
      onmessage?: (event: { data: { sender?: string } }) => void;
      close = vi.fn();
      postMessage = vi.fn<(message: { sender: string }) => void>();
      constructor() {
        channels.push(this);
      }
    },
  );
  auth.mockImplementation(async (path: string) => {
    if (path === "session") return { access: null, expiresAt: 0 };
    if (path === "options")
      return {
        mode: "development",
        users: [{ alias: "reader", label: "Reader login" }],
      };
    if (path === "login") return { location: "/unit/catalog" };
    if (path === "logout") return undefined;
    throw new Error("Unexpected auth path");
  });
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  auth.mockReset();
  invalidate.mockReset();
});

function openAccount() {
  const trigger = screen.getByLabelText("Account");
  if (!trigger.closest("details")!.open) fireEvent.click(trigger);
}

function signOut() {
  openAccount();
  fireEvent.click(screen.getByRole("button", { name: "Sign out" }));
}

async function mountAuthenticated(access = reader, config = runtime) {
  auth.mockImplementation(async (path: string) =>
    path === "options"
      ? { mode: config.authMode, users: [] }
      : { ...signedIn(), access },
  );
  let view: ReturnType<typeof render> | undefined;
  // Flush immediate session/lazy-view work instead of racing Suspense's first load.
  await act(async () => {
    view = render(<App runtime={config} />);
  });
  await screen.findByRole("heading", { name: "Catalog content" });
  return required(view);
}

function headerMeasurements() {
  let height = 56;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(
    () => new DOMRect(0, 0, 701, height),
  );
  const observers: HeaderObserver[] = [];
  class HeaderObserver implements ResizeObserver {
    private callback: ResizeObserverCallback;
    observe = vi.fn();
    unobserve = vi.fn();
    disconnect = vi.fn();
    constructor(callback: ResizeObserverCallback) {
      this.callback = callback;
      observers.push(this);
    }
    notify() {
      this.callback([], this);
    }
  }
  vi.stubGlobal("ResizeObserver", HeaderObserver);
  return {
    observers,
    setHeight(value: number) {
      height = value;
    },
  };
}

describe("header measurements", () => {
  it("measures and observes the header within its workspace and clears stale observations on unmount", async () => {
    const { observers, setHeight } = headerMeasurements();
    const rootStyle = document.documentElement.style;
    const previous = rootStyle.getPropertyValue("--header-height");
    const priority = rootStyle.getPropertyPriority("--header-height");
    rootStyle.setProperty("--header-height", "17px", "important");
    try {
      const view = await mountAuthenticated();
      const workspace =
        view.container.querySelector<HTMLElement>(".workspace")!;
      const observer = required(observers[0]);
      expect(observers).toHaveLength(1);
      expect(observer.observe).toHaveBeenCalledWith(screen.getByRole("banner"));
      expect(workspace.style.getPropertyValue("--header-height")).toBe("56px");
      const requests = auth.mock.calls.length;
      const lastLink = screen
        .getByRole("navigation", { name: "Workspace" })
        .querySelector<HTMLElement>('a[href="/unit/api-reference"]')!;
      lastLink.scrollIntoView = vi.fn();
      lastLink.focus();
      setHeight(145.75);
      observer.notify();
      expect(workspace.style.getPropertyValue("--header-height")).toBe(
        "145.75px",
      );
      expect(auth).toHaveBeenCalledTimes(requests);
      expect(lastLink.scrollIntoView).toHaveBeenCalledWith({
        block: "nearest",
        inline: "nearest",
      });
      expect(document.activeElement).toBe(lastLink);
      expect(view.container.querySelector(".workspace")).toBe(workspace);
      expect(rootStyle.getPropertyValue("--header-height")).toBe("17px");
      expect(rootStyle.getPropertyPriority("--header-height")).toBe(
        "important",
      );
      view.unmount();
      expect(observer.disconnect).toHaveBeenCalledOnce();
      expect(workspace.style.getPropertyValue("--header-height")).toBe("");
      observer.notify();
      expect(workspace.style.getPropertyValue("--header-height")).toBe("");
      expect(rootStyle.getPropertyValue("--header-height")).toBe("17px");
      expect(rootStyle.getPropertyPriority("--header-height")).toBe(
        "important",
      );
    } finally {
      if (previous)
        rootStyle.setProperty("--header-height", previous, priority);
      else rootStyle.removeProperty("--header-height");
    }
  });

  it("retains an initial measurement without the optional observer API", async () => {
    const { observers, setHeight } = headerMeasurements();
    setHeight(72.5);
    vi.stubGlobal("ResizeObserver", undefined);
    const view = await mountAuthenticated();
    const workspace = view.container.querySelector<HTMLElement>(".workspace")!;
    expect(workspace.style.getPropertyValue("--header-height")).toBe("72.5px");
    expect(observers).toHaveLength(0);
    view.unmount();
    expect(workspace.style.getPropertyValue("--header-height")).toBe("");
  });

  it("owns one observer per signed-in workspace through StrictMode login and logout", async () => {
    const { observers, setHeight } = headerMeasurements();
    const initial = auth.getMockImplementation()!;
    const view = render(
      <StrictMode>
        <App runtime={runtime} />
      </StrictMode>,
    );
    const login = await screen.findByRole("button", { name: "Reader login" });
    expect(observers).toHaveLength(0);
    auth.mockImplementation(async (path: string, ...args: unknown[]) =>
      path === "session" ? signedIn() : initial(path, ...args),
    );
    setHeight(91.375);
    await act(async () => fireEvent.click(login));
    const first = view.container.querySelector<HTMLElement>(".workspace")!;
    expect(first.style.getPropertyValue("--header-height")).toBe("91.375px");
    expect(observers).toHaveLength(1);
    await act(async () => signOut());
    expect(required(observers[0]).disconnect).toHaveBeenCalledOnce();
    expect(first.style.getPropertyValue("--header-height")).toBe("");
    expect(view.container.querySelector(".workspace")).toBeNull();
    setHeight(56);
    await act(async () =>
      fireEvent.click(screen.getByRole("button", { name: "Reader login" })),
    );
    const second = view.container.querySelector<HTMLElement>(".workspace")!;
    expect(second).not.toBe(first);
    expect(second.style.getPropertyValue("--header-height")).toBe("56px");
    expect(observers).toHaveLength(2);
    view.unmount();
    expect(required(observers[1]).disconnect).toHaveBeenCalledOnce();
    expect(second.style.getPropertyValue("--header-height")).toBe("");
  });
});

describe("shell authentication", () => {
  it("updates read-only Catalog context on same-document history navigation", async () => {
    await mountAuthenticated();
    expect(document.title).toBe("Catalog | Open Agentic Catalog");
    const calls = auth.mock.calls.length;
    act(() => {
      history.pushState(
        null,
        "",
        "/unit/catalog/01900000-0000-7000-8000-000000000001",
      );
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await waitFor(() =>
      expect(document.title).toBe("Catalog record | Open Agentic Catalog"),
    );
    expect(history.state).toBeNull();
    expect(auth.mock.calls).toHaveLength(calls);
    expect(invalidate).not.toHaveBeenCalled();
    act(() => {
      history.replaceState(null, "", "/unit/catalog");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    await waitFor(() =>
      expect(document.title).toBe("Catalog | Open Agentic Catalog"),
    );
  });

  it("retains a valid session without irrelevant login-options alerts", async () => {
    const options = deferred<unknown>();
    auth.mockImplementation(async (path: string) => {
      if (path === "session") return signedIn();
      if (path === "options") return options.promise;
      throw new Error("Unexpected authentication command");
    });
    // Flush session updates and the first lazy view before testing the
    // independent options failure; a synchronous render leaves Suspense work.
    await act(async () => {
      render(<App runtime={runtime} />);
    });
    await screen.findByRole("heading", { name: "Catalog content" });
    await act(async () => options.reject(new Error("Private provider detail")));
    expect(screen.queryByRole("alert")).toBeNull();
    expect(
      screen.getByRole("heading", { name: "Catalog content" }),
    ).toBeTruthy();
    expect(screen.getByLabelText("Account")).toBeTruthy();
    expect(invalidate).not.toHaveBeenCalled();
    expect(auth.mock.calls.map(([path]) => path).sort()).toEqual([
      "options",
      "session",
    ]);
  });

  it("loads session state, signs in through fixed aliases and signs out", async () => {
    const initialOptions = auth.getMockImplementation()!;
    auth.mockImplementation(async (path: string, ...args: unknown[]) =>
      path === "options"
        ? {
            mode: "development",
            users: [
              {
                alias: "reader",
                label: "Reader login",
                description: "Read local resources",
              },
            ],
          }
        : initialOptions(path, ...args),
    );
    const view = render(<App runtime={runtime} />);
    expect(screen.getByRole("status").textContent).toBe(
      "Loading your workspace...",
    );
    await screen.findByRole("button", {
      name: "Reader login",
    });
    expect(screen.getByText("Read local resources")).toBeTruthy();
    const artwork = view.container.querySelector(
      '.login-symbols[aria-hidden="true"]',
    );
    expect(artwork?.querySelectorAll(".kind-icon")).toHaveLength(4);
    for (const kind of ["agent", "mcp_server", "skill", "plugin"]) {
      expect(artwork?.querySelector(`.kind-icon.${kind} svg`)).not.toBeNull();
    }
    const initial = auth.getMockImplementation()!;
    auth.mockImplementation(async (path: string, ...args: unknown[]) =>
      path === "session" ? signedIn() : initial(path, ...args),
    );
    fireEvent.click(screen.getByRole("button", { name: "Reader login" }));
    await screen.findByRole("heading", { name: "Catalog content" });
    expect(auth).toHaveBeenCalledWith("login", "POST", { alias: "reader" });
    // A separate BroadcastChannel in this same tab also receives the sign-in
    // notification. Only other tabs should discard their session in response.
    const ownNotification = required(
      required(channels[1]).postMessage.mock.calls[0],
    )[0];
    expect(ownNotification).toEqual({ sender: expect.any(String) as unknown });
    const invalidations = invalidate.mock.calls.length;
    await act(async () =>
      required(channels[0]).onmessage?.({ data: ownNotification }),
    );
    expect(invalidate).toHaveBeenCalledTimes(invalidations);
    expect(
      screen.getByRole("heading", { name: "Catalog content" }),
    ).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByRole("link", { name: "Members" })).toBeNull();
    expect(
      screen.getByRole("link", { name: "Documentation" }).getAttribute("href"),
    ).toBe(runtime.documentationUrl);
    await act(async () => signOut());
    expect(auth).toHaveBeenCalledWith("logout", "POST", {});
    expect(
      screen.queryByRole("heading", { name: "Catalog content" }),
    ).toBeNull();
    expect(invalidate).toHaveBeenCalled();
    view.unmount();
    expect(
      channels.every((channel) => channel.close.mock.calls.length > 0),
    ).toBe(true);
  });

  it.each([new APIError(401, "invalid"), new Error("private cause")])(
    "shows a safe failed login and releases its busy state: %s",
    async (error) => {
      render(<App runtime={runtime} />);
      const button = await screen.findByRole("button", {
        name: "Reader login",
      });
      auth.mockRejectedValueOnce(error);
      await act(async () => fireEvent.click(button));
      expect(screen.getByRole("alert").textContent).toBe(
        error instanceof APIError
          ? error.message
          : "Sign-in failed. Please try again.",
      );
      expect((button as HTMLButtonElement).disabled).toBe(false);
    },
  );

  it("does not treat a successful login response without membership as authenticated", async () => {
    render(<App runtime={runtime} />);
    const button = await screen.findByRole("button", { name: "Reader login" });
    await act(async () => fireEvent.click(button));
    expect(screen.getByRole("alert").textContent).toContain(
      "session is unavailable",
    );
    expect(screen.queryByRole("button", { name: "Sign out" })).toBeNull();
  });

  it.each(["options", "session"])(
    "shows a bounded startup failure for %s",
    async (failed) => {
      const initial = auth.getMockImplementation()!;
      auth.mockImplementation(async (path: string) => {
        if (path === failed) throw new Error("private transport");
        return initial(path);
      });
      render(<App runtime={runtime} />);
      expect((await screen.findByRole("alert")).textContent).toBe(
        failed === "options"
          ? "Sign-in options could not be loaded. Reload sign-in options to try again."
          : "Your session could not be checked. Reload the page to try again.",
      );
    },
  );

  it("treats an expired initial session as signed out, without an outage warning", async () => {
    const initial = auth.getMockImplementation()!;
    auth.mockImplementation(async (path: string) => {
      if (path === "session") throw new APIError(401, "expired");
      return initial(path);
    });
    render(<App runtime={runtime} />);
    await screen.findByRole("button", { name: "Reader login" });
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("warns when logout fails and retries without restoring private content", async () => {
    await mountAuthenticated();
    auth.mockRejectedValueOnce(new Error("offline"));
    await act(async () => signOut());
    expect(screen.getByRole("alert").textContent).toContain(
      "session cookie may still be active",
    );
    expect(
      screen.queryByRole("heading", { name: "Catalog content" }),
    ).toBeNull();
    auth.mockResolvedValueOnce(undefined);
    await act(async () =>
      fireEvent.click(screen.getByRole("button", { name: "Retry sign-out" })),
    );
    expect(screen.queryByRole("button", { name: "Retry sign-out" })).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("honors an unsaved editor veto before sign-out", async () => {
    await mountAuthenticated();
    const veto = (event: Event) => event.preventDefault();
    window.addEventListener("catalog-ui:leave", veto);
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    try {
      signOut();
      expect(auth).not.toHaveBeenCalledWith("logout", "POST", {});
      expect(
        screen.getByRole("heading", { name: "Catalog content" }),
      ).toBeTruthy();
      confirm.mockReturnValue(true);
      await act(async () => signOut());
      expect(auth).toHaveBeenCalledWith("logout", "POST", {});
    } finally {
      window.removeEventListener("catalog-ui:leave", veto);
    }
  });

  it("clears private content on cross-tab changes", async () => {
    await mountAuthenticated();
    await act(async () =>
      required(channels[0]).onmessage?.({ data: { sender: "another-tab" } }),
    );
    expect(screen.getByRole("alert").textContent).toContain(
      "changed in another tab",
    );
    expect(
      screen.queryByRole("heading", { name: "Catalog content" }),
    ).toBeNull();
  });

  it("clears a session immediately on a 401 response", async () => {
    await mountAuthenticated();
    fireEvent.click(screen.getByRole("button", { name: "Expired read" }));
    expect(screen.getByRole("alert").textContent).toContain(
      "session is unavailable",
    );
    expect(
      screen.queryByRole("heading", { name: "Catalog content" }),
    ).toBeNull();
  });

  it("coalesces forbidden-read context refresh and removes revoked membership", async () => {
    await mountAuthenticated();
    const pending = deferred<unknown>();
    auth.mockReturnValueOnce(pending.promise);
    const before = auth.mock.calls.length;
    fireEvent.click(screen.getByRole("button", { name: "Denied read" }));
    fireEvent.click(screen.getByRole("button", { name: "Denied read" }));
    expect(auth.mock.calls.length).toBe(before + 1);
    await act(async () => pending.resolve({ access: null, expiresAt: 0 }));
    expect(
      screen.queryByRole("heading", { name: "Catalog content" }),
    ).toBeNull();
  });

  it("keeps denied reads fail-closed if context refresh fails and permits a later refresh", async () => {
    await mountAuthenticated();
    auth.mockRejectedValueOnce(new Error("offline"));
    await act(async () =>
      fireEvent.click(screen.getByRole("button", { name: "Denied read" })),
    );
    expect(screen.queryByRole("link", { name: "Members" })).toBeNull();
    auth.mockResolvedValueOnce({
      ...signedIn(),
      access: { ...reader, ...testAccess(["tenant_admin"]) },
    });
    await act(async () =>
      fireEvent.click(screen.getByRole("button", { name: "Denied read" })),
    );
    expect(screen.getByRole("link", { name: "Members" })).toBeTruthy();
  });

  it("expires the client session with an injected timer, without changing the host clock", async () => {
    vi.useFakeTimers();
    auth.mockImplementation(async (path: string) =>
      path === "options"
        ? { mode: "development", users: [] }
        : { ...signedIn(), expiresAt: Math.floor(Date.now() / 1000) + 1 },
    );
    render(<App runtime={runtime} />);
    await act(async () => {});
    await act(async () => vi.advanceTimersByTime(1001));
    expect(screen.getByRole("alert").textContent).toBe(
      "Your session expired. Sign in to continue.",
    );
    expect(
      screen.queryByRole("heading", { name: "Catalog content" }),
    ).toBeNull();
  });

  it.each([false, true])(
    "ignores startup results after unmount, rejected=%s",
    async (reject) => {
      const pending = deferred<unknown>();
      auth.mockReturnValue(pending.promise);
      const view = render(<App runtime={runtime} />);
      view.unmount();
      await act(async () => {
        if (reject) pending.reject(new Error("late"));
        else pending.resolve(signedIn());
      });
      expect(view.container.textContent).toBe("");
    },
  );
});

it("uses appearance fallbacks when browser storage is unavailable", async () => {
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
    throw new Error("disabled");
  });
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
    throw new Error("disabled");
  });
  render(<App runtime={runtime} />);
  await screen.findByRole("button", { name: "Reader login" });
  expect(document.documentElement.dataset.theme).toBe("system");
  expect(document.documentElement.dataset.density).toBe("comfortable");
});

describe("shell controls", () => {
  it.each([
    {
      ...testAccess(["reader"]),
      scopes: ["catalog.read"],
      group: "Manage content",
      link: "Authoring",
    },
    {
      ...testAccess(["reader", "reviewer"]),
      scopes: ["catalog.read", "catalog.review"],
      group: "Manage content",
      link: "Reviews",
    },
    {
      ...testAccess(["publisher"]),
      scopes: ["catalog.read"],
      group: "Manage content",
      link: "Discovery publication",
    },
    {
      ...testAccess(["source_admin"]),
      scopes: ["sources.read"],
      group: "Operations",
      link: "Federation sources",
    },
    {
      ...testAccess(["security_reviewer"]),
      scopes: ["audit.export"],
      group: "Administration",
      link: "Audit",
    },
    {
      ...testAccess(["contributor"]),
      scopes: ["publishers.manage"],
      group: "Manage content",
      link: "Publishers",
    },
    {
      ...testAccess(["tenant_admin"]),
      scopes: ["tenant.admin"],
      group: "Administration",
      link: "Members",
    },
    {
      ...testAccess([]),
      scopes: [],
      group: "Account",
      link: "My access",
    },
  ] satisfies {
    permissions: Access["permissions"];
    availablePermissions: Access["availablePermissions"];
    authorizationRevision: number;
    scopes: Access["scopes"];
    group: string;
    link: string;
  }[])(
    "keeps $link in $group without adding authority requirements",
    async ({
      permissions,
      availablePermissions,
      authorizationRevision,
      scopes,
      group,
      link,
    }: {
      permissions: Access["permissions"];
      availablePermissions: Access["availablePermissions"];
      authorizationRevision: number;
      scopes: Access["scopes"];
      group: string;
      link: string;
    }) => {
      auth.mockImplementation(async (path) =>
        path === "options"
          ? { users: [] }
          : {
              ...signedIn(),
              access: {
                ...reader,
                permissions,
                availablePermissions,
                authorizationRevision,
                scopes,
                scopeEnforcement: true,
              },
            },
      );
      render(<App runtime={runtime} />);
      const nav = await screen.findByRole("navigation", { name: "Workspace" });
      openAccount();
      const destinationGroup =
        group === "Account"
          ? screen.getByRole("navigation", { name: "Account pages" })
          : within(nav).getByRole("group", { name: group });
      expect(
        within(destinationGroup).getByRole("link", { name: link }),
      ).toBeTruthy();
      expect(screen.getByText("Human account")).toBeTruthy();
      expect(screen.queryByText("Personal workspace")).toBeNull();
      if (
        !availablePermissions.includes("resource.read") &&
        !scopes.includes("catalog.read")
      ) {
        expect(
          within(
            screen.getByRole("navigation", { name: "Account pages" }),
          ).queryByRole("link", { name: "My ratings" }),
        ).toBeNull();
        expect(within(nav).queryByRole("link", { name: "Catalog" })).toBeNull();
        await waitFor(() =>
          expect(document.title).toBe("My access | Open Agentic Catalog"),
        );
        expect(
          within(screen.getByRole("navigation", { name: "Account pages" }))
            .getByRole("link", { name: "My access" })
            .getAttribute("aria-current"),
        ).toBe("page");
      }
      const indexing = within(nav).queryByRole("link", {
        name: "Search indexing",
      });
      if (
        permissions.includes("access.manage") &&
        scopes.includes("tenant.admin")
      ) {
        expect(indexing?.getAttribute("href")).toBe(
          "/unit/service#search-indexing",
        );
        expect(indexing?.hasAttribute("aria-current")).toBe(false);
      } else expect(indexing).toBeNull();
      if (!permissions.includes("access.manage"))
        expect(within(nav).queryByRole("link", { name: "Members" })).toBeNull();
    },
  );
  it("restores saved appearance and persists changes without reloading the session", async () => {
    localStorage.setItem("catalog-ui:theme", "dark");
    localStorage.setItem("catalog-ui:density", "compact");
    const view = await mountAuthenticated();
    const appearance = screen.getByLabelText("Appearance");
    fireEvent.click(appearance);
    expect(
      (screen.getByRole("radio", { name: "Dark" }) as HTMLInputElement).checked,
    ).toBe(true);
    expect(
      (screen.getByRole("radio", { name: "Compact" }) as HTMLInputElement)
        .checked,
    ).toBe(true);
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(document.documentElement.dataset.density).toBe("compact");
    const authCount = auth.mock.calls.length;
    fireEvent.click(screen.getByRole("radio", { name: "Light" }));
    fireEvent.click(screen.getByRole("radio", { name: "Comfortable" }));
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(document.documentElement.dataset.density).toBe("comfortable");
    expect(localStorage.getItem("catalog-ui:theme")).toBe("light");
    expect(localStorage.getItem("catalog-ui:density")).toBe("comfortable");
    expect(auth).toHaveBeenCalledTimes(authCount);
    view.unmount();
    await mountAuthenticated();
    fireEvent.click(screen.getByLabelText("Appearance"));
    expect(
      (screen.getByRole("radio", { name: "Light" }) as HTMLInputElement)
        .checked,
    ).toBe(true);
    fireEvent.click(screen.getByRole("radio", { name: "System" }));
    expect(document.documentElement.dataset.theme).toBe("system");
    expect(localStorage.getItem("catalog-ui:theme")).toBe("system");
  });

  it("dismisses appearance on Escape or an outside pointer, keeping internal interactions open", async () => {
    await mountAuthenticated();
    const summary = screen.getByLabelText("Appearance");
    const details = summary.closest("details")!;
    fireEvent.click(summary);
    const radio = screen.getByRole("radio", { name: "Dark" });
    fireEvent.pointerDown(radio);
    expect(details.open).toBe(true);
    radio.focus();
    fireEvent.keyDown(radio, { key: "ArrowLeft" });
    expect(details.open).toBe(true);
    fireEvent.keyDown(radio, { key: "Escape" });
    expect(details.open).toBe(false);
    expect(document.activeElement).toBe(summary);
    fireEvent.click(summary);
    fireEvent.pointerDown(
      screen.getByRole("heading", { name: "Catalog content" }),
    );
    expect(details.open).toBe(false);
  });

  it("toggles the workspace menu and returns focus to its toggle when Escape closes it", async () => {
    await mountAuthenticated();
    const toggle = screen.getByRole("button", { name: "Workspace menu" });
    const nav = screen.getByRole("navigation", { name: "Workspace" });
    expect(toggle.getAttribute("aria-controls")).toBe(nav.id);
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(nav.closest("aside")?.dataset.menuOpen).toBe("true");
    const catalog = within(nav).getByRole("link", { name: "Catalog" });
    catalog.focus();
    fireEvent.keyDown(catalog, { key: "Enter" });
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    fireEvent.keyDown(catalog, { key: "Escape" });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(document.activeElement).toBe(toggle);
    fireEvent.click(toggle);
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
  });

  it("keeps navigation within the configured base and hides unsafe documentation links", async () => {
    history.replaceState(null, "", "/nested/catalog/");
    await mountAuthenticated(reader, {
      ...runtime,
      basePath: "/nested/catalog",
      documentationUrl: "javascript:alert(1)",
    });
    const nav = within(screen.getByRole("navigation", { name: "Workspace" }));
    for (const [label, path] of [
      ["Catalog", "catalog"],
      ["Explore local catalog", "explore"],
      ["Authoring", "resources"],
      ["Reviews", "reviews"],
      ["Namespaces", "namespaces"],
      ["Publishers", "publishers"],
      ["API reference", "api-reference"],
      ["Service information", "service"],
    ] as const)
      expect(nav.getByRole("link", { name: label }).getAttribute("href")).toBe(
        `/nested/catalog/${path}`,
      );
    expect(nav.queryByRole("link", { name: "Jobs" })).toBeNull();
    openAccount();
    const account = within(
      screen.getByRole("navigation", { name: "Account pages" }),
    );
    for (const [label, path] of [
      ["My access", "access"],
      ["My ratings", "my-ratings"],
      ["My feedback", "my-feedback"],
    ] as const)
      expect(
        account.getByRole("link", { name: label }).getAttribute("href"),
      ).toBe(`/nested/catalog/${path}`);
    expect(
      nav.getByRole("link", { name: "Catalog" }).getAttribute("aria-current"),
    ).toBe("page");
    expect(
      nav
        .getAllByRole("link")
        .filter((link) => link.hasAttribute("aria-current")),
    ).toHaveLength(1);
    expect(screen.queryByRole("link", { name: "Documentation" })).toBeNull();
    expect(
      screen
        .getByRole("link", { name: "Skip to content" })
        .getAttribute("href"),
    ).toBe("#main");
  });

  it.each(["/docs/", "https://docs.example.test/manual/"])(
    "places %s once in Help before API reference",
    async (documentationUrl) => {
      history.replaceState(null, "", "/nested/catalog");
      await mountAuthenticated(reader, {
        ...runtime,
        basePath: "/nested",
        documentationUrl,
      });
      const help = within(screen.getByRole("group", { name: "Help" }));
      const link = help.getByRole("link", {
        name: "Documentation",
      });
      expect(
        screen.getAllByRole("link", { name: "Documentation" }),
      ).toHaveLength(1);
      expect(link.getAttribute("href")).toBe(documentationUrl);
      expect(link.getAttribute("target")).toBe("_blank");
      expect(link.getAttribute("rel")).toBe("noopener noreferrer");
      expect(link.getAttribute("aria-describedby")).toBe(
        "documentation-new-tab",
      );
      expect(screen.getByText("Opens in a new tab")).toBeTruthy();
      expect(help.getAllByRole("link").indexOf(link)).toBeLessThan(
        help
          .getAllByRole("link")
          .indexOf(help.getByRole("link", { name: "API reference" })),
      );
    },
  );

  it("keeps API reference available without a documentation destination", async () => {
    await mountAuthenticated(reader, { ...runtime, documentationUrl: "" });
    expect(screen.queryByRole("link", { name: "Documentation" })).toBeNull();
    expect(screen.getByRole("link", { name: "API reference" })).toBeTruthy();
  });

  it("keeps a namespace Publisher out of tenant-wide publication and administration", async () => {
    await mountAuthenticated({
      ...reader,
      namespaceGrants: [
        { namespaceId: "ns-1", permissions: testPermissions(["publisher"]) },
      ],
    });
    const nav = within(screen.getByRole("navigation", { name: "Workspace" }));
    expect(nav.getByRole("link", { name: "Authoring" })).toBeTruthy();
    for (const name of [
      "Discovery publication",
      "Members",
      "Federation sources",
      "Webhooks",
      "Audit",
    ])
      expect(nav.queryByRole("link", { name })).toBeNull();
  });

  it("uses token scope ceilings for admin links while retaining export-only audit access", async () => {
    await mountAuthenticated({
      ...reader,
      ...testAccess(["tenant_admin"]),
      scopeEnforcement: true,
      scopes: ["catalog.read", "audit.export"],
      actorType: "service",
    });
    const nav = within(screen.getByRole("navigation", { name: "Workspace" }));
    expect(nav.getByRole("link", { name: "Audit" }).getAttribute("href")).toBe(
      "/unit/audit",
    );
    expect(
      nav.getByRole("link", { name: "Discovery publication" }),
    ).toBeTruthy();
    for (const name of [
      "Members",
      "Federation sources",
      "Webhooks",
      "Publishers",
    ])
      expect(nav.queryByRole("link", { name })).toBeNull();
    openAccount();
    expect(screen.getByText("Service identity")).toBeTruthy();
    const account = within(
      screen.getByRole("navigation", { name: "Account pages" }),
    );
    expect(account.getByRole("link", { name: "My access" })).toBeTruthy();
    for (const name of ["My ratings", "My feedback"])
      expect(account.queryByRole("link", { name })).toBeNull();
    expect(screen.queryByText("Personal workspace")).toBeNull();
  });

  it.each([
    ["my-work", "service", false, ["catalog.read"], []],
    ["my-work", "human", true, [], []],
    ["my-work", "human", true, ["catalog.read"], []],
    ["saved-items", "human", true, ["catalog.read"], []],
    ["saved-items", "service", false, ["catalog.read"], ["library.own"]],
    ["saved-items", "human", true, [], ["library.own"]],
  ] as const)(
    "gates personal route %s for %s with scope enforcement %s",
    async (view, actorType, scopeEnforcement, scopes, permissions) => {
      history.replaceState(null, "", `/unit/${view}`);
      auth.mockImplementation(async (path: string) =>
        path === "options"
          ? { mode: "development", users: [] }
          : {
              ...signedIn(),
              access: {
                ...reader,
                actorType,
                scopeEnforcement,
                scopes: [...scopes],
                permissions: [...permissions],
                availablePermissions: [...permissions],
              },
            },
      );
      render(<App runtime={runtime} />);
      if (
        view === "my-work" &&
        actorType === "human" &&
        scopes.includes("catalog.read" as never)
      ) {
        await screen.findByRole("heading", { name: "My work" });
      } else {
        expect((await screen.findByRole("alert")).textContent).toContain(
          "This page is unavailable with your current access",
        );
      }
    },
  );

  it.each(["version-revisions", "indexed-record"])(
    "denies a direct %s bookmark without its required permission rather than mounting a private screen",
    async (view) => {
      history.replaceState(null, "", `/unit/${view}/private-record`);
      auth.mockImplementation(async (path: string) =>
        path === "options"
          ? { mode: "development", users: [] }
          : {
              ...signedIn(),
              access: { ...reader, ...testAccess(["security_reviewer"]) },
            },
      );
      render(<App runtime={runtime} />);
      expect((await screen.findByRole("alert")).textContent).toContain(
        "This page is unavailable with your current access",
      );
      expect(screen.getByRole("alert").textContent).toContain(
        "Check My access for your permissions and token scopes.",
      );
      const nav = within(screen.getByRole("navigation", { name: "Workspace" }));
      expect(nav.queryByRole("link", { name: "Authoring" })).toBeNull();
      expect(nav.getByRole("link", { name: "Audit" })).toBeTruthy();
      expect(
        screen.queryByRole("heading", { name: "Catalog content" }),
      ).toBeNull();
      expect(auth.mock.calls.map(([path]) => path).sort()).toEqual([
        "options",
        "session",
      ]);
    },
  );

  it.each(["oidc", "development"] as const)(
    "shows only the intended %s login controls and safe callback notice",
    async (mode) => {
      history.replaceState(
        null,
        "",
        "/unit/?sign_in_error=catalog_login_denied",
      );
      auth.mockImplementation(async (path: string) =>
        path === "options"
          ? {
              mode,
              users:
                mode === "development"
                  ? [{ alias: "reader", label: "Reader login" }]
                  : [],
            }
          : { access: null, expiresAt: 0 },
      );
      render(<App runtime={{ ...runtime, authMode: mode }} />);
      if (mode === "oidc") {
        expect(
          await screen.findByRole("button", { name: "Sign in" }),
        ).toBeTruthy();
        expect(
          screen.getByText("Use your account to access the Catalog."),
        ).toBeTruthy();
        expect(screen.getByRole("alert").textContent).toBe(
          "The Catalog did not accept this account. Ask an administrator to check your access.",
        );
        expect(screen.queryByText(/Local development identities/)).toBeNull();
        expect(screen.queryByText(/These are local shortcuts/)).toBeNull();
        expect(
          screen.queryByRole("searchbox", { name: "Find a sign-in identity" }),
        ).toBeNull();
        expect(
          screen.queryByRole("button", { name: "Reader login" }),
        ).toBeNull();
      } else {
        expect(
          await screen.findByRole("button", { name: "Reader login" }),
        ).toBeTruthy();
        expect(screen.getByText(/Local development identities/)).toBeTruthy();
        expect(screen.getByText(/These are local shortcuts/)).toBeTruthy();
        expect(screen.queryByRole("alert")).toBeNull();
      }
    },
  );

  it("hands OIDC sign-in to the host-returned location without submitting a development alias", async () => {
    // Only navigation is substituted: jsdom cannot follow a document redirect.
    const assign = vi.fn();
    vi.stubGlobal("location", { pathname: "/unit/catalog", assign });
    auth.mockImplementation(async (path: string) =>
      path === "options"
        ? { mode: "oidc", users: [] }
        : { access: null, expiresAt: 0 },
    );
    render(<App runtime={{ ...runtime, authMode: "oidc" }} />);
    const button = await screen.findByRole("button", {
      name: "Sign in",
    });
    expect(screen.queryByRole("button", { name: "Reader login" })).toBeNull();
    const pending = deferred<{ location: string }>();
    auth.mockReturnValueOnce(pending.promise);
    fireEvent.click(button);
    expect((button as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByRole("status").textContent).toBe("Signing in...");
    expect(auth).toHaveBeenCalledWith("login", "POST", {});
    expect(assign).not.toHaveBeenCalled();
    await act(async () =>
      pending.resolve({
        location: "https://issuer.example.test/authorize?state=example",
      }),
    );
    expect(assign).toHaveBeenCalledExactlyOnceWith(
      "https://issuer.example.test/authorize?state=example",
    );
    expect(auth.mock.calls.filter(([path]) => path === "session")).toHaveLength(
      1,
    );
    expect((button as HTMLButtonElement).disabled).toBe(false);
  });
});

it.each(["login", "session", "rejected"])(
  "discards a superseded sign-in during %s without replacing the cross-tab notice",
  async (phase) => {
    render(<App runtime={runtime} />);
    const button = await screen.findByRole("button", { name: "Reader login" });
    const pending = deferred<unknown>();
    const startup = auth.getMockImplementation()!;
    auth.mockImplementation(async (path: string, ...args: unknown[]) => {
      if (path === "options") return startup(path, ...args);
      if (phase === "session" && path === "login")
        return { location: "/unit/catalog" };
      return pending.promise;
    });
    await act(async () => fireEvent.click(button));
    const appearance = screen.getByLabelText("Appearance");
    appearance.focus();
    act(() => required(channels[0]).onmessage?.({ data: { sender: "other" } }));
    expect(document.activeElement).toBe(appearance);
    await act(async () => {
      if (phase === "rejected")
        pending.reject(new Error("private stale failure"));
      else
        pending.resolve(
          phase === "session" ? signedIn() : { location: "/unit/catalog" },
        );
    });
    expect(screen.getByRole("alert").textContent).toBe(
      "Your session changed in another tab. Sign in to continue.",
    );
    expect(
      screen.queryByRole("heading", { name: "Catalog content" }),
    ).toBeNull();
    expect(
      (
        screen.getByRole("button", {
          name: "Reader login",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(false);
  },
);

it.each([false, true])(
  "ignores superseded logout completion (rejected=%s)",
  async (rejected) => {
    await mountAuthenticated();
    const pending = deferred<void>();
    auth.mockReturnValueOnce(pending.promise);
    signOut();
    act(() => required(channels[0]).onmessage?.({ data: { sender: "other" } }));
    await act(async () => {
      if (rejected) pending.reject(new Error("private logout failure"));
      else pending.resolve();
    });
    expect(screen.getByRole("alert").textContent).toBe(
      "Your session changed in another tab. Sign in to continue.",
    );
    expect(screen.queryByRole("button", { name: "Retry sign-out" })).toBeNull();
  },
);

it("discards an already-decoded old refresh after cross-tab invalidation", async () => {
  await mountAuthenticated();
  const pending = deferred<unknown>();
  auth.mockReturnValueOnce(pending.promise);
  fireEvent.click(screen.getByRole("button", { name: "Denied read" }));
  act(() => required(channels[0]).onmessage?.({ data: { sender: "other" } }));
  await act(async () => pending.resolve(signedIn()));
  expect(screen.queryByRole("heading", { name: "Catalog content" })).toBeNull();
  expect(screen.getByRole("alert").textContent).toBe(
    "Your session changed in another tab. Sign in to continue.",
  );
});

it("keeps personal links in Account, dismisses without stealing focus and opens only one header panel", async () => {
  await mountAuthenticated();
  const trigger = screen.getByLabelText("Account");
  const details = trigger.closest("details")!;
  const appearance = screen.getByLabelText("Appearance");
  const preferences = appearance.closest("details")!;
  const workspace = within(
    screen.getByRole("navigation", { name: "Workspace" }),
  );
  for (const name of ["My access", "My ratings", "My feedback"])
    expect(workspace.queryByRole("link", { name })).toBeNull();
  expect(details.open).toBe(false);
  const calls = auth.mock.calls.length;
  fireEvent.click(trigger);
  const access = within(
    screen.getByRole("navigation", { name: "Account pages" }),
  ).getByRole("link", { name: "My access" });
  fireEvent.pointerDown(access);
  fireEvent.blur(trigger, { relatedTarget: access });
  expect(details.open).toBe(true);
  access.focus();
  fireEvent.keyDown(access, { key: "ArrowDown" });
  expect(details.open).toBe(true);
  fireEvent.keyDown(access, { key: "Escape" });
  expect(details.open).toBe(false);
  expect(document.activeElement).toBe(trigger);
  fireEvent.click(trigger);
  fireEvent.click(appearance);
  expect(details.open).toBe(false);
  expect(preferences.open).toBe(true);
  fireEvent.click(trigger);
  expect(preferences.open).toBe(false);
  expect(details.open).toBe(true);
  appearance.focus();
  expect(details.open).toBe(false);
  expect(document.activeElement).toBe(appearance);
  fireEvent.click(trigger);
  fireEvent.pointerDown(
    screen.getByRole("heading", { name: "Catalog content" }),
  );
  expect(details.open).toBe(false);
  expect(auth).toHaveBeenCalledTimes(calls);
});

it("clears an open Account on refreshed identity and removes it on session loss", async () => {
  await mountAuthenticated();
  openAccount();
  const details = screen.getByLabelText("Account").closest("details")!;
  auth.mockResolvedValueOnce({
    ...signedIn(),
    access: { ...reader, subject: "replacement subject" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Denied read" }));
  await waitFor(() => expect(details.open).toBe(false));
  openAccount();
  expect(
    screen.getByText("replacement subject", { selector: "strong" }),
  ).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Expired read" }));
  expect(screen.queryByLabelText("Account")).toBeNull();
  expect(screen.getByLabelText("Appearance")).toBeTruthy();
});

it("keeps Appearance usable before login and closes it when keyboard focus leaves", async () => {
  render(<App runtime={runtime} />);
  await screen.findByRole("button", { name: "Reader login" });
  expect(screen.queryByLabelText("Account")).toBeNull();
  const appearance = screen.getByLabelText("Appearance");
  const details = appearance.closest("details")!;
  fireEvent.click(appearance);
  const radio = screen.getByRole("radio", { name: "Dark" });
  radio.focus();
  expect(details.open).toBe(true);
  screen.getByRole("button", { name: "Reader login" }).focus();
  expect(details.open).toBe(false);
  expect(document.activeElement).toBe(
    screen.getByRole("button", { name: "Reader login" }),
  );
});

it("fences delayed startup options across logout and explicitly reloads current sign-in choices", async () => {
  const oldOptions = deferred<unknown>();
  const logout = deferred<unknown>();
  let optionsCalls = 0;
  auth.mockImplementation(async (path: string) => {
    if (path === "session") return signedIn();
    if (path === "logout") return logout.promise;
    if (path === "options") {
      optionsCalls++;
      if (optionsCalls === 1) return oldOptions.promise;
      if (optionsCalls === 2) throw new Error("private");
      return {
        mode: "development",
        users: [{ alias: "new", label: "Current login" }],
      };
    }
    throw new Error("Unexpected call");
  });
  render(<App runtime={runtime} />);
  await screen.findByRole("heading", { name: "Catalog content" });
  const out = screen.getByLabelText("Account");
  out.focus();
  signOut();
  expect(screen.getByText("Signing out...")).toBeTruthy();
  expect(screen.queryByText("Signing in...")).toBeNull();
  await act(async () => logout.resolve(undefined));
  await screen.findByRole("button", { name: "Reload sign-in options" });
  expect(document.activeElement).toBe(
    screen.getByRole("heading", {
      name: "Find Agents, MCP Servers, Skills and Plugins.",
    }),
  );
  await act(async () =>
    oldOptions.resolve({
      users: [{ alias: "old", label: "Canceled identity" }],
    }),
  );
  expect(
    screen.queryByRole("button", { name: "Canceled identity" }),
  ).toBeNull();
  fireEvent.click(
    screen.getByRole("button", { name: "Reload sign-in options" }),
  );
  await screen.findByRole("button", { name: "Current login" });
  expect(screen.queryByRole("alert")).toBeNull();
});

it.each(["about", "getting-started", "troubleshooting"])(
  "makes %s public after bootstrap and keeps the validated manual link",
  async (path) => {
    history.replaceState(null, "", `/unit/${path}`);
    render(<App runtime={runtime} />);
    await screen.findByRole("heading", {
      name:
        path === "about"
          ? "About"
          : path === "troubleshooting"
            ? "Troubleshooting"
            : "Getting started",
    });
    expect(document.title).toBe(
      `${path === "about" ? "About" : path === "troubleshooting" ? "Troubleshooting" : "Getting started"} | Open Agentic Catalog`,
    );
    expect(
      screen.getByRole("link", { name: "Documentation" }).getAttribute("href"),
    ).toBe(runtime.documentationUrl);
    const manual = screen.getByRole("link", { name: "Documentation" });
    expect(manual.querySelector("svg")?.getAttribute("aria-hidden")).toBe(
      "true",
    );
    expect(manual.querySelector("svg")?.getAttribute("focusable")).toBe(
      "false",
    );
    expect(manual.getAttribute("target")).toBe("_blank");
    expect(manual.getAttribute("rel")).toBe("noopener noreferrer");
    expect(screen.queryByLabelText("Account")).toBeNull();
  },
);

it("routes authenticated Help without replacing the workspace and hides absent public Documentation", async () => {
  history.replaceState(null, "", "/unit/about");
  auth.mockImplementation(async (path: string) =>
    path === "options" ? { users: [] } : signedIn(),
  );
  render(<App runtime={runtime} />);
  await screen.findByRole("heading", { name: "About" });
  expect(screen.getByRole("navigation", { name: "Workspace" })).toBeTruthy();
  cleanup();
  auth.mockResolvedValue({ access: null, expiresAt: 0, users: [] });
  history.replaceState(null, "", "/unit/catalog");
  render(<App runtime={{ ...runtime, documentationUrl: "" }} />);
  await screen.findByRole("heading", {
    name: "Find Agents, MCP Servers, Skills and Plugins.",
  });
  expect(screen.queryByRole("link", { name: "Documentation" })).toBeNull();
});

it("focuses the visible workspace destination after explicit keyboard login", async () => {
  render(<App runtime={runtime} />);
  const login = await screen.findByRole("button", { name: "Reader login" });
  auth.mockImplementation(async (path: string) =>
    path === "options"
      ? { users: [] }
      : path === "login"
        ? { location: "/unit/catalog" }
        : signedIn(),
  );
  login.focus();
  fireEvent.click(login);
  await screen.findByRole("heading", { name: "Catalog content" });
  expect(document.activeElement).toBe(
    screen.getByRole("heading", { name: "Catalog content" }),
  );
});

it.each(["workspace", "appearance"])(
  "warns once, preserves surviving %s focus and clears private state at absolute expiry",
  async (target) => {
    vi.useFakeTimers();
    const expiresAt = Math.floor(Date.now() / 1000) + 300;
    auth.mockImplementation(async (path: string) =>
      path === "options" ? { users: [] } : { ...signedIn(), expiresAt },
    );
    await act(async () => {
      render(<App runtime={runtime} />);
    });
    const focus =
      target === "appearance"
        ? screen.getByLabelText("Appearance")
        : screen.getByRole("button", { name: "Denied read" });
    focus.focus();
    expect(screen.queryByText(/session expires within two minutes/)).toBeNull();
    await act(async () => vi.advanceTimersByTime(181_000));
    const warning = screen.getByRole("status");
    expect(warning.textContent).toContain("Expiry clears unsaved input");
    fireEvent.click(screen.getByRole("button", { name: "Denied read" }));
    await act(async () => {});
    expect(screen.getByRole("status")).toBe(warning);
    expect(document.activeElement).toBe(focus);
    await act(async () => vi.advanceTimersByTime(120_000));
    expect(screen.queryByText(/session expires within two minutes/)).toBeNull();
    if (target === "appearance") expect(document.activeElement).toBe(focus);
    else
      expect(document.activeElement).toBe(
        screen.getByRole("heading", {
          name: "Find Agents, MCP Servers, Skills and Plugins.",
        }),
      );
  },
);

it("does not replace a surviving header focus target on cross-tab sign-out", async () => {
  await mountAuthenticated();
  fireEvent.focusIn(document.body);
  fireEvent.focusIn(document);
  const appearance = screen.getByLabelText("Appearance");
  appearance.focus();
  act(() => required(channels[0]).onmessage?.({ data: { sender: "other" } }));
  expect(document.activeElement).toBe(appearance);
});

it.each(["about", "getting-started", "troubleshooting"])(
  "shows public %s while the API session check is still pending",
  async (page) => {
    history.replaceState(null, "", `/unit/${page}`);
    const session = deferred<unknown>();
    auth.mockImplementation(async (path: string) =>
      path === "session" ? session.promise : { users: [] },
    );
    render(<App runtime={runtime} />);
    await screen.findByRole("heading", {
      name:
        page === "about"
          ? "About"
          : page === "troubleshooting"
            ? "Troubleshooting"
            : "Getting started",
    });
    expect(screen.queryByLabelText("Account")).toBeNull();
    expect(screen.getByRole("link", { name: "Documentation" })).toBeTruthy();
    await act(async () => session.reject(new Error("private backend outage")));
    expect(
      screen.getByRole("heading", {
        name:
          page === "about"
            ? "About"
            : page === "troubleshooting"
              ? "Troubleshooting"
              : "Getting started",
      }),
    ).toBeTruthy();
  },
);

it("resets a short-session warning on deliberate replacement and clears it on cross-tab sign-out", async () => {
  vi.useFakeTimers();
  const original = Math.floor(Date.now() / 1000);
  let expiry = original + 60;
  auth.mockImplementation(async (path: string) => {
    if (path === "options")
      return { users: [{ alias: "reader", label: "Reader login" }] };
    if (path === "login") return { location: "/unit/catalog" };
    if (path === "logout") return undefined;
    return { ...signedIn(), expiresAt: expiry };
  });
  await act(async () => {
    render(<App runtime={runtime} />);
  });
  expect(screen.getByRole("status").textContent).toContain(
    "expires within two minutes",
  );
  await act(async () => signOut());
  expect(screen.queryByText(/expires within two minutes/)).toBeNull();
  expiry = original + 600;
  await act(async () =>
    fireEvent.click(screen.getByRole("button", { name: "Reader login" })),
  );
  expect(screen.queryByText(/expires within two minutes/)).toBeNull();
  await act(async () => vi.advanceTimersByTime(480_001));
  expect(screen.getByRole("status").textContent).toContain(
    "expires within two minutes",
  );
  act(() => required(channels[0]).onmessage?.({ data: { sender: "other" } }));
  expect(screen.queryByText(/expires within two minutes/)).toBeNull();
  expect(screen.getByRole("alert").textContent).toContain(
    "session changed in another tab",
  );
});

it("keeps only safe diagnostic correlation for the current identity across local Help navigation", async () => {
  await mountAuthenticated();
  fireEvent.click(screen.getByRole("button", { name: "Dependency failure" }));
  act(() => {
    history.pushState(null, "", "/unit/troubleshooting");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await screen.findByRole("heading", { name: "Troubleshooting" });
  expect(
    screen.getByText("Diagnostic reference: response-local-1"),
  ).toBeTruthy();
  await act(async () => signOut());
  expect(screen.queryByText(/Diagnostic reference:/)).toBeNull();
});

it("updates fixed indexing navigation and title on same-document fragment changes", async () => {
  await mountAuthenticated({ ...reader, ...testAccess(["tenant_admin"]) });
  act(() => {
    history.pushState(null, "", "/unit/service#search-indexing");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await screen.findByRole("heading", { name: "Service information content" });
  expect(document.title).toBe("Search indexing | Open Agentic Catalog");
  expect(
    screen
      .getByRole("link", { name: "Search indexing" })
      .getAttribute("aria-current"),
  ).toBe("page");
  expect(
    screen
      .getByRole("link", { name: "Service information" })
      .getAttribute("aria-current"),
  ).toBeNull();
  act(() => {
    history.replaceState(null, "", "/unit/service#private-title");
    window.dispatchEvent(new HashChangeEvent("hashchange"));
  });
  expect(document.title).toBe("Service information | Open Agentic Catalog");
  expect(document.title).not.toContain("private-title");
});
