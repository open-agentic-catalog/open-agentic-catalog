import { required } from "../tests/required";
// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render as mount,
  screen,
} from "@testing-library/react";
import type { ReactNode } from "react";
import { Bootstrap } from "./shell/Bootstrap";
import { deferred } from "../tests/unit/fixtures";

const { render } = vi.hoisted(() => ({ render: vi.fn() }));
vi.mock("react-dom/client", () => ({ createRoot: vi.fn(() => ({ render })) }));
vi.mock("./shell/App", () => ({
  App: ({ runtime }: { runtime: { basePath: string } }) => (
    <h1>App at {runtime.basePath}</h1>
  ),
}));
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.clearAllMocks();
  document.body.replaceChildren();
});

it.each([
  { basePath: "", authMode: "development", documentationUrl: "" },
  {
    basePath: "/catalog-ui",
    authMode: "oidc",
    documentationUrl: "https://docs.example.com/",
  },
])("boots with fixed runtime inputs: %j", async (config) => {
  vi.resetModules();
  document.body.innerHTML = '<div id="root"></div>';
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(config)));
  vi.stubGlobal("fetch", fetch);
  await import("./main");
  expect(render).toHaveBeenCalledOnce();
  mount(required(render.mock.calls[0])[0] as ReactNode);
  await screen.findByRole("heading", {
    name: `App at ${config.basePath}`.trim(),
  });
  expect(fetch).toHaveBeenCalledWith(expect.any(URL), {
    cache: "no-store",
    credentials: "same-origin",
    redirect: "error",
    signal: expect.any(AbortSignal) as unknown,
  });
  expect((required(fetch.mock.calls[0])[0] as URL).pathname).toMatch(
    /\/runtime.json$/,
  );
});

it.each([
  () => Promise.reject(new Error("private transport detail")),
  () => Promise.resolve(new Response("private upstream body", { status: 503 })),
  () => Promise.resolve(new Response("invalid json")),
  () => Promise.resolve(new Response("null")),
  () => Promise.resolve(new Response(null, { status: 204 })),
  ...[
    {
      basePath: "/nested/../private",
      authMode: "development",
      documentationUrl: "",
    },
    {
      basePath: "",
      authMode: "development",
      documentationUrl: "https://user:private@docs.test",
    },
    { basePath: "", authMode: "development" },
  ].map((value) => () => Promise.resolve(Response.json(value))),
  () =>
    Promise.resolve(
      new Response(
        JSON.stringify({
          basePath: 1,
          authMode: "development",
          documentationUrl: "",
        }),
      ),
    ),
  () =>
    Promise.resolve(
      new Response(
        JSON.stringify({
          basePath: "",
          authMode: "unknown",
          documentationUrl: "",
        }),
      ),
    ),
])(
  "fails safely without mounting the application when config is unavailable or invalid: %#",
  async (fetch) => {
    vi.resetModules();
    document.body.innerHTML = '<div id="root"></div>';
    vi.stubGlobal("fetch", vi.fn(fetch));
    await import("./main");
    expect(render).toHaveBeenCalledOnce();
    mount(required(render.mock.calls[0])[0] as ReactNode);
    await screen.findByRole("heading", { name: "Catalog unavailable" });
    expect(screen.getByRole("alert").textContent).toBe(
      "The UI settings could not be loaded. Reload to try again.",
    );
    expect(document.body.textContent).not.toMatch(/private|App at/);
  },
);

it.each(["success", "failure"])(
  "shows held bootstrap progress, retries explicitly and fences unmounted %s",
  async (outcome) => {
    const held = deferred<Response>();
    const late = deferred<Response>();
    const fetch = vi
      .fn()
      .mockReturnValueOnce(held.promise)
      .mockResolvedValueOnce(
        Response.json({
          basePath: "",
          authMode: "development",
          documentationUrl: "",
        }),
      )
      .mockReturnValueOnce(late.promise);
    vi.stubGlobal("fetch", fetch);
    const url = new URL("http://localhost/runtime.json");
    const view = mount(<Bootstrap url={url} />);
    expect(screen.getByRole("status").textContent).toBe(
      "Loading UI settings...",
    );
    expect(screen.queryByText(/App at/)).toBeNull();
    await act(async () => held.reject(new Error("private")));
    expect(document.activeElement).toBe(
      screen.getByRole("heading", { name: "Catalog unavailable" }),
    );
    const reload = screen.getByRole("button", { name: "Reload" });
    expect(reload.querySelector("svg")?.getAttribute("aria-hidden")).toBe(
      "true",
    );
    expect(reload.querySelector("svg")?.getAttribute("focusable")).toBe(
      "false",
    );
    fireEvent.click(reload);
    await screen.findByRole("heading", { name: "App at" });
    view.unmount();
    const other = mount(<Bootstrap url={url} />);
    other.unmount();
    expect(
      (required(fetch.mock.calls[2])[1] as RequestInit).signal?.aborted,
    ).toBe(true);
    await act(async () => {
      if (outcome === "failure") late.reject(new Error("late private detail"));
      else
        late.resolve(
          Response.json({
            basePath: "",
            authMode: "development",
            documentationUrl: "",
          }),
        );
    });
    expect(document.body.textContent).not.toMatch(
      /App at|late private detail|Catalog unavailable/,
    );
  },
);
