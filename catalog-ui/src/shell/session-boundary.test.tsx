// @vitest-environment jsdom
// Real client-to-shell checks use inert fetches; no host, issuer or database runs here.
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { APIError } from "../api/client";
import type { Access } from "../api/client";
import { deferred, sessionAccess } from "../../tests/unit/fixtures";
import { App } from "./App";

vi.mock("../views/Catalog", () => ({
  default: ({
    access,
    onFailure,
  }: {
    access: Access;
    onFailure: (error: unknown) => void;
  }) => (
    <section>
      <h1>Private workspace: {access.subject}</h1>
      <button onClick={() => onFailure(new APIError(403, "FORBIDDEN"))}>
        Denied read
      </button>
    </section>
  ),
}));
const runtime = {
  basePath: "/unit",
  authMode: "development" as const,
  documentationUrl: "",
};
const session = (subject = "unit-reader") => ({
  access: { ...sessionAccess, subject },
  expiresAt: Math.floor(Date.now() / 1000) + 600,
});
const sessionFetch = vi.fn<() => Promise<Response>>();
const loginFetch = vi.fn<() => Promise<Response>>();
const optionsFetch = vi.fn<() => Promise<Response>>();
const channels: {
  onmessage?: (event: { data: { sender: string } }) => void;
}[] = [];

beforeEach(() => {
  history.replaceState(null, "", "/unit/catalog");
  localStorage.clear();
  channels.length = 0;
  sessionFetch
    .mockReset()
    .mockImplementation(async () => Response.json(session()));
  loginFetch
    .mockReset()
    .mockImplementation(async () =>
      Response.json({ location: "/unit/catalog" }),
    );
  optionsFetch.mockReset().mockImplementation(async () =>
    Response.json({
      mode: "development",
      users: [{ alias: "reader", label: "Reader login" }],
    }),
  );
  vi.stubGlobal(
    "BroadcastChannel",
    class {
      onmessage?: (event: { data: { sender: string } }) => void;
      constructor() {
        channels.push(this);
      }
      close() {}
      postMessage() {}
    },
  );
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      switch (new URL(request.url).pathname) {
        case "/unit/auth/session":
          return sessionFetch();
        case "/unit/auth/options":
          return optionsFetch();
        case "/unit/auth/login":
          return loginFetch();
        case "/unit/auth/logout":
          return new Response(null, { status: 204 });
        default:
          throw new Error("Unexpected unit request");
      }
    }),
  );
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

async function mount() {
  render(<App runtime={runtime} />);
  await screen.findByRole("heading", {
    name: "Private workspace: unit-reader",
  });
}
function changeIdentity() {
  act(() => channels[0]?.onmessage?.({ data: { sender: "another-tab" } }));
}

it("does not install malformed initial access and recovers through an explicit valid login", async () => {
  sessionFetch.mockResolvedValueOnce(
    Response.json({
      ...session(),
      access: {
        ...sessionAccess,
        permissions: "access.manage",
        scopeEnforcement: false,
      },
    }),
  );
  render(<App runtime={runtime} />);
  expect((await screen.findByRole("alert")).textContent).toBe(
    "Your session could not be checked. Reload the page to try again.",
  );
  expect(screen.queryByRole("navigation", { name: "Workspace" })).toBeNull();
  fireEvent.click(await screen.findByRole("button", { name: "Reader login" }));
  await screen.findByRole("heading", {
    name: "Private workspace: unit-reader",
  });
  expect(loginFetch).toHaveBeenCalledOnce();
  expect(sessionFetch).toHaveBeenCalledTimes(2);
});

it("does not navigate or fetch a replacement session after an unsafe login reply", async () => {
  sessionFetch.mockResolvedValueOnce(
    Response.json({ access: null, expiresAt: 0 }),
  );
  loginFetch.mockResolvedValueOnce(
    Response.json({ location: "javascript:privateSentinel()" }),
  );
  render(<App runtime={runtime} />);
  fireEvent.click(await screen.findByRole("button", { name: "Reader login" }));
  expect((await screen.findByRole("alert")).textContent).toBe(
    "The request could not be completed. Please try again.",
  );
  expect(document.body.textContent).not.toContain("privateSentinel");
  expect(screen.queryByRole("navigation", { name: "Workspace" })).toBeNull();
  expect(sessionFetch).toHaveBeenCalledOnce();
});

it.each(["invalid", "syntax"] as const)(
  "keeps a successful login with a %s session reply signed out until explicit recovery",
  async (outcome) => {
    sessionFetch
      .mockResolvedValueOnce(Response.json({ access: null, expiresAt: 0 }))
      .mockResolvedValueOnce(
        outcome === "invalid"
          ? Response.json({
              ...session(),
              access: {
                ...sessionAccess,
                subject: "private identity",
                permissions: "access.manage",
              },
            })
          : new Response("private malformed session"),
      );
    render(<App runtime={runtime} />);
    fireEvent.click(
      await screen.findByRole("button", { name: "Reader login" }),
    );
    expect((await screen.findByRole("alert")).textContent).toBe(
      "The request could not be completed. Please try again.",
    );
    expect(screen.queryByRole("navigation", { name: "Workspace" })).toBeNull();
    expect(document.body.textContent).not.toMatch(
      /private identity|private malformed session|tenant_admin/,
    );
    expect(loginFetch).toHaveBeenCalledOnce();
    expect(sessionFetch).toHaveBeenCalledTimes(2);

    fireEvent.click(screen.getByRole("button", { name: "Reader login" }));
    await screen.findByRole("heading", {
      name: "Private workspace: unit-reader",
    });
    expect(screen.queryByRole("alert")).toBeNull();
    expect(loginFetch).toHaveBeenCalledTimes(2);
    expect(sessionFetch).toHaveBeenCalledTimes(3);
  },
);

it.each(["401", "invalid", "syntax", "503", "transport"] as const)(
  "handles a current refresh %s without granting new hints",
  async (outcome) => {
    await mount();
    if (outcome === "transport")
      sessionFetch.mockRejectedValueOnce(
        new TypeError("private transport detail"),
      );
    else
      sessionFetch.mockResolvedValueOnce(
        outcome === "invalid"
          ? Response.json({
              access: { ...sessionAccess, permissions: "access.manage" },
              expiresAt: session().expiresAt,
            })
          : new Response(
              outcome === "syntax"
                ? "private parse detail"
                : "private upstream",
              { status: outcome === "syntax" ? 200 : Number(outcome) },
            ),
      );
    await act(async () =>
      fireEvent.click(screen.getByRole("button", { name: "Denied read" })),
    );
    expect(sessionFetch).toHaveBeenCalledTimes(2);
    if (outcome === "503" || outcome === "transport") {
      expect(
        screen.getByRole("heading", { name: "Private workspace: unit-reader" }),
      ).toBeTruthy();
      expect(screen.queryByRole("alert")).toBeNull();
      expect(document.body.textContent).not.toContain("private");
      // A failed refresh releases its coalescing guard; only a later explicit
      // denied read requests fresh context, without repeating the login.
      await act(async () =>
        fireEvent.click(screen.getByRole("button", { name: "Denied read" })),
      );
      expect(sessionFetch).toHaveBeenCalledTimes(3);
      expect(loginFetch).not.toHaveBeenCalled();
    } else {
      await screen.findByRole("button", { name: "Reader login" });
      expect(
        screen.queryByRole("navigation", { name: "Workspace" }),
      ).toBeNull();
      expect(screen.getByRole("alert").textContent).not.toMatch(
        /private|tenant_admin/,
      );
    }
    expect(
      screen.queryByRole("link", { name: "Members and access" }),
    ).toBeNull();
  },
);

it.each(["invalid", "rejected", "401"] as const)(
  "does not let an old refresh %s clear a newer session",
  async (outcome) => {
    await mount();
    const started = deferred<void>();
    const body = deferred<unknown>();
    const headers = deferred<Response>();
    const response = new Response("{}");
    response.json = () => {
      started.resolve();
      return body.promise;
    };
    sessionFetch.mockImplementationOnce(() => {
      if (outcome === "401") {
        started.resolve();
        return headers.promise;
      }
      return Promise.resolve(response);
    });
    fireEvent.click(screen.getByRole("button", { name: "Denied read" }));
    await started.promise;
    changeIdentity();
    sessionFetch.mockResolvedValueOnce(Response.json(session("new-reader")));
    fireEvent.click(
      await screen.findByRole("button", { name: "Reader login" }),
    );
    await screen.findByRole("heading", {
      name: "Private workspace: new-reader",
    });
    const currentRefresh = deferred<Response>();
    sessionFetch.mockReturnValueOnce(currentRefresh.promise);
    fireEvent.click(screen.getByRole("button", { name: "Denied read" }));
    await waitFor(() => expect(sessionFetch).toHaveBeenCalledTimes(4));
    await act(async () => {
      if (outcome === "invalid")
        body.resolve({ access: "private old identity" });
      else if (outcome === "rejected")
        body.reject(new SyntaxError("private old body"));
      else headers.resolve(new Response(null, { status: 401 }));
    });
    fireEvent.click(screen.getByRole("button", { name: "Denied read" }));
    expect(sessionFetch).toHaveBeenCalledTimes(4);
    expect(
      screen.getByRole("heading", { name: "Private workspace: new-reader" }),
    ).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
    await act(async () =>
      currentRefresh.resolve(Response.json(session("new-reader"))),
    );
  },
);

it("keeps an expired but otherwise valid initial session signed out without an outage message", async () => {
  sessionFetch.mockResolvedValueOnce(
    Response.json({
      ...session(),
      expiresAt: Math.floor(Date.now() / 1000) - 1,
    }),
  );
  render(<App runtime={runtime} />);
  await screen.findByRole("button", { name: "Reader login" });
  expect(screen.queryByRole("alert")).toBeNull();
  expect(screen.queryByRole("navigation", { name: "Workspace" })).toBeNull();
});

it.each([false, true])(
  "uses the real decoder before provider navigation (unsafe=%s)",
  async (unsafe) => {
    const assign = vi.fn();
    vi.stubGlobal("location", {
      origin: "http://127.0.0.1:4180",
      pathname: "/unit/catalog",
      assign,
    });
    optionsFetch.mockResolvedValueOnce(
      Response.json({ mode: "oidc", users: [] }),
    );
    sessionFetch.mockResolvedValueOnce(
      Response.json({ access: null, expiresAt: 0 }),
    );
    const location = unsafe
      ? "javascript:privateSentinel()"
      : "https://issuer.example.test/authorize?state=a%2Bb&scope=openid+profile";
    loginFetch.mockResolvedValueOnce(Response.json({ location }));
    render(<App runtime={{ ...runtime, authMode: "oidc" }} />);
    fireEvent.click(await screen.findByRole("button", { name: "Sign in" }));
    if (unsafe) {
      expect((await screen.findByRole("alert")).textContent).toBe(
        "The request could not be completed. Please try again.",
      );
      expect(assign).not.toHaveBeenCalled();
      expect(document.body.textContent).not.toContain("privateSentinel");
    } else
      await waitFor(() =>
        expect(assign).toHaveBeenCalledExactlyOnceWith(location),
      );
    expect(sessionFetch).toHaveBeenCalledOnce();
    expect(loginFetch).toHaveBeenCalledOnce();
  },
);
