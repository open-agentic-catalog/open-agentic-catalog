// @vitest-environment jsdom
// Public page units use injected replies; no listener, Catalog binary or browser.
import { afterEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import PublicDiscovery from "./PublicDiscovery";
import { deferred } from "../../tests/unit/fixtures";
const runtime = {
  basePath: "",
  authMode: "development" as const,
  documentationUrl: "/docs/",
  publicDiscoveryEnabled: true,
};
const entry = {
  identifier: "urn:air:example.com:agent:public",
  displayName: "Public <script>text</script>",
  type: "application/a2a-agent-card+json",
  url: "https://never-contact.invalid/native",
  description: "Returned description",
  source: "https://publication.invalid/ard/v1/",
  tags: ["public"],
  capabilities: ["metadata"],
  representativeQueries: ["find published data"],
};
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
function replies(page: unknown = { items: [entry], pageToken: "next+/=" }) {
  const fetch = vi
    .fn()
    .mockImplementation(async (path: string) =>
      Response.json(path.endsWith("manifest") ? { entries: [entry] } : page),
    );
  vi.stubGlobal("fetch", fetch);
  return fetch;
}
it("loads public metadata without private bootstrap and renders locations as inert text", async () => {
  const fetch = replies();
  render(<PublicDiscovery runtime={runtime} />);
  await screen.findByRole("heading", { name: entry.displayName });
  fireEvent.click(screen.getByText("Inspect returned metadata"));
  expect(screen.getByText(entry.url)).not.toBeNull();
  expect(screen.getByText(entry.source)).not.toBeNull();
  expect(screen.getByText("Not declared")).not.toBeNull();
  expect(document.querySelector("script,img,iframe")).toBeNull();
  expect(fetch.mock.calls.map((call: unknown[]) => String(call[0]))).toEqual([
    "/_discovery/manifest",
    "/_discovery/agents?pageSize=20",
  ]);
  expect(
    screen.getByRole("link", { name: "Sign in" }).getAttribute("href"),
  ).toBe("/catalog");
});
it("requires query disclosure consent and submits only on explicit Search", async () => {
  const fetch = replies();
  render(<PublicDiscovery runtime={runtime} />);
  await screen.findByRole("heading", { name: entry.displayName });
  fireEvent.change(screen.getByRole("searchbox"), {
    target: { value: "tools" },
  });
  expect(
    (
      screen.getByRole("button", {
        name: "Search",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  expect(fetch).toHaveBeenCalledTimes(2);
  fireEvent.click(screen.getByRole("checkbox"));
  fetch.mockImplementation(async (path: string) =>
    Response.json(
      path.endsWith("manifest")
        ? { entries: [] }
        : path.endsWith("search")
          ? { results: [] }
          : { items: [] },
    ),
  );
  fireEvent.click(screen.getByRole("button", { name: "Search" }));
  await screen.findByText("No entries were returned on this page.");
  expect(fetch).toHaveBeenLastCalledWith(
    "/_discovery/search",
    expect.objectContaining({
      body: JSON.stringify({ text: "tools", pageSize: 20 }),
    }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Browse directory" }));
  await waitFor(() =>
    expect(fetch).toHaveBeenLastCalledWith(
      "/_discovery/agents?pageSize=20",
      expect.anything(),
    ),
  );
});
it("continues the applied selection and recovers stale tokens only on explicit refresh", async () => {
  const fetch = replies();
  render(<PublicDiscovery runtime={runtime} />);
  await screen.findByRole("heading", { name: entry.displayName });
  fireEvent.change(screen.getByRole("searchbox"), {
    target: { value: "unsent draft" },
  });
  fetch.mockResolvedValueOnce(
    new Response("private sentinel", { status: 400 }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  expect((await screen.findByRole("alert")).textContent).toContain(
    "Refresh from the first page",
  );
  expect(screen.queryByRole("heading", { name: entry.displayName })).toBeNull();
  expect(fetch).toHaveBeenCalledTimes(3);
  expect(fetch).toHaveBeenLastCalledWith(
    "/_discovery/agents?pageSize=20&pageToken=next%2B%2F%3D",
    expect.anything(),
  );
  fireEvent.click(screen.getByRole("button", { name: "Refresh publication" }));
  await screen.findByRole("heading", { name: entry.displayName });
  expect(fetch).toHaveBeenCalledTimes(5);
});
it("shows an unavailable feed without private fallback or old rows", async () => {
  const fetch = replies();
  render(<PublicDiscovery runtime={runtime} />);
  await screen.findByRole("heading", { name: entry.displayName });
  fetch.mockResolvedValueOnce(
    new Response("secret upstream body", { status: 404 }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Refresh publication" }));
  expect((await screen.findByRole("alert")).textContent).toContain(
    "Public discovery is unavailable",
  );
  expect(screen.queryByRole("heading", { name: entry.displayName })).toBeNull();
  expect(document.body.textContent).not.toContain("secret upstream body");
  expect(fetch).toHaveBeenCalledTimes(3);
});
it.each(["succeeds", "fails"] as const)(
  "keeps the current directory when the previous manifest read %s late",
  async (outcome) => {
    const old = deferred<Response>();
    let originalSignal: AbortSignal | undefined;
    const fetch = replies();
    fetch.mockImplementationOnce((_path: string, options: RequestInit) => {
      originalSignal = options.signal ?? undefined;
      return old.promise;
    });
    const view = render(<PublicDiscovery runtime={runtime} />);
    view.rerender(
      <PublicDiscovery runtime={{ ...runtime, basePath: "/nested" }} />,
    );
    await screen.findByRole("heading", { name: entry.displayName });
    expect(originalSignal?.aborted).toBe(true);
    await act(async () => {
      if (outcome === "fails") old.reject(new Error("cancelled read"));
      else old.resolve(Response.json({ entries: [] }));
    });
    expect(
      screen.getByRole("heading", { name: entry.displayName }),
    ).toBeTruthy();
    expect(screen.getByText(/Last read manifest: 1\b/)).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(fetch.mock.calls.map(([path]: unknown[]) => path)).toEqual([
      "/_discovery/manifest",
      "/nested/_discovery/manifest",
      "/nested/_discovery/agents?pageSize=20",
    ]);
  },
);
it("cancels a pending Search when provider consent is withdrawn", async () => {
  const fetch = replies();
  render(<PublicDiscovery runtime={runtime} />);
  await screen.findByRole("heading", { name: entry.displayName });
  const held = deferred<Response>();
  let signal: AbortSignal | undefined;
  fetch.mockImplementation(async (path: string, options: RequestInit) => {
    if (path.endsWith("manifest")) return Response.json({ entries: [] });
    signal = options.signal ?? undefined;
    return held.promise;
  });
  fireEvent.change(screen.getByRole("searchbox"), {
    target: { value: "pending" },
  });
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.click(screen.getByRole("button", { name: "Search" }));
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(4));
  fireEvent.click(screen.getByRole("checkbox"));
  expect(signal?.aborted).toBe(true);
  await act(async () => held.resolve(Response.json({ results: [entry] })));
  expect(screen.queryByRole("heading", { name: entry.displayName })).toBeNull();
});
it("keeps unsent page-size edits separate, distinguishes empty versions and shows an exhausted page", async () => {
  const fetch = replies({
    items: [{ ...entry, version: "", type: "application/unknown+json" }],
  });
  render(<PublicDiscovery runtime={{ ...runtime, documentationUrl: "" }} />);
  await screen.findByRole("heading", { name: entry.displayName });
  expect(screen.queryByRole("link", { name: "Documentation" })).toBeNull();
  expect(screen.getByText("Declared empty")).not.toBeNull();
  expect(screen.getByText("Other metadata")).not.toBeNull();
  expect(screen.queryByRole("button", { name: "Next page" })).toBeNull();
  fireEvent.change(screen.getByRole("combobox"), { target: { value: "5" } });
  expect(fetch).toHaveBeenCalledTimes(2);
  expect(
    screen.getByText("Displayed results use the last submitted selection."),
  ).not.toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Browse directory" }));
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(4));
  await screen.findByRole("heading", { name: entry.displayName });
  expect(fetch).toHaveBeenLastCalledWith(
    "/_discovery/agents?pageSize=5",
    expect.anything(),
  );
});
it("refuses malformed displayed values instead of retaining a previous valid page", async () => {
  const fetch = replies();
  render(<PublicDiscovery runtime={runtime} />);
  await screen.findByRole("heading", { name: entry.displayName });
  fetch.mockImplementation(async (path: string) =>
    Response.json(
      path.endsWith("manifest")
        ? { entries: [] }
        : { items: [{ ...entry, version: null }] },
    ),
  );
  fireEvent.click(screen.getByRole("button", { name: "Refresh publication" }));
  expect((await screen.findByRole("alert")).textContent).toContain(
    "Public discovery is unavailable",
  );
  expect(screen.queryByRole("heading", { name: entry.displayName })).toBeNull();
});
it("describes an empty continuing page without claiming the selection has no matches", async () => {
  const fetch = replies({ items: [], pageToken: "empty-page+/=" });
  render(<PublicDiscovery runtime={runtime} />);
  await screen.findByText("No entries were returned on this page.");
  expect(screen.getByRole("status").textContent).toContain(
    "More pages are available.",
  );
  expect(screen.queryByText("No entries match this selection.")).toBeNull();
  fetch.mockResolvedValueOnce(Response.json({ items: [entry] }));
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  await screen.findByRole("heading", { name: entry.displayName });
  expect(fetch).toHaveBeenCalledTimes(3);
  expect(fetch).toHaveBeenLastCalledWith(
    "/_discovery/agents?pageSize=20&pageToken=empty-page%2B%2F%3D",
    expect.anything(),
  );
});
