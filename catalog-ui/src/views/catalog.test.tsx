import { editValues } from "../../tests/unit/screens";
import { testAccess } from "../../tests/unit/access-fixtures";
import { required } from "../../tests/required";
// @vitest-environment jsdom
import {
  evidence,
  versionRevision,
  listingRevision,
  release,
  representation,
} from "../../tests/unit/responses";
import { beforeEach, expect, it, vi } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import Catalog from "./Catalog";
import { APIError } from "../api/client";
import { deferred, reply } from "../../tests/unit/fixtures";
import {
  administrator,
  change,
  click,
  listing,
  resource,
  screenLifecycle,
  screenWorkspace as baseWorkspace,
  stamp,
  version,
} from "../../tests/unit/screens";

screenLifecycle("/unit/catalog");
beforeEach(() => {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
});
// Search assertions remain exact; category suggestions have a separate read contract.
function screenWorkspace() {
  const workspace = baseWorkspace();
  const explore = vi.fn(async () =>
    reply({ matched: 0, facets: { category: { buckets: [] } } }),
  );
  workspace.props.client.api.POST = ((path: string, options: unknown) =>
    path === "/api/v1/catalog/explore"
      ? explore()
      : workspace.post(
          path,
          options,
        )) as typeof workspace.props.client.api.POST;
  return { ...workspace, explore };
}

it("applies fixed origin presets after history navigation without retaining private search drafts in the URL", async () => {
  history.replaceState(null, "", "/unit/catalog?origin=indexed");
  const { props, post } = screenWorkspace();
  post.mockResolvedValue(
    reply({
      tier: "core",
      items: [],
      durableTotal: 0,
      liveCount: 0,
      partial: false,
    }),
  );
  render(<Catalog {...props} access={administrator} />);
  await waitFor(() => expect(post).toHaveBeenCalled());
  expect(post.mock.calls[0]?.[1]).toMatchObject({
    body: { text: "", origins: ["indexed"] },
  });
  change("Search Catalog", "private unsubmitted text");
  await act(async () => {
    history.pushState(
      { catalogNavigation: 1 },
      "",
      "/unit/catalog?origin=local",
    );
    window.dispatchEvent(
      new PopStateEvent("popstate", { state: { catalogNavigation: 1 } }),
    );
  });
  await waitFor(() =>
    expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
      body: { text: "", origins: ["local"] },
    }),
  );
  expect(
    screen.getByRole("searchbox", { name: "Search Catalog" }),
  ).toHaveProperty("value", "");
  expect(location.search).toBe("?origin=local");
  expect(history.state).toEqual({ catalogNavigation: 1 });
  post.mockResolvedValue(
    reply({
      ...results,
      items: [local],
      durableTotal: 2,
      liveCount: 0,
      partial: false,
      diagnostics: [],
      nextCursor: "private-origin-cursor",
    }),
  );
  change("Search Catalog", "private submitted text");
  click("Search");
  await screen.findByRole("button", { name: "Local helper" });
  change("Search Catalog", "private pending draft");
  click(/Next page/);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(4));
  expect((post.mock.calls.at(-1)?.[1] as { body: unknown }).body).toEqual({
    text: "private submitted text",
    origins: ["local"],
    limit: 24,
    cursor: "private-origin-cursor",
  });
  await act(async () => {
    history.pushState({ catalogNavigation: 2 }, "", "/unit/catalog");
    window.dispatchEvent(
      new PopStateEvent("popstate", { state: { catalogNavigation: 2 } }),
    );
  });
  await waitFor(() => expect(post).toHaveBeenCalledTimes(5));
  expect((post.mock.calls.at(-1)?.[1] as { body: unknown }).body).toEqual({
    text: "",
    limit: 24,
  });
  expect(
    screen.getByRole("searchbox", { name: "Search Catalog" }),
  ).toHaveProperty("value", "");
  expect(location.pathname).toBe("/unit/catalog");
  expect(location.search).toBe("");
  expect(location.hash).toBe("");
  expect(history.state).toEqual({ catalogNavigation: 2 });
  expect(JSON.stringify(history.state)).not.toMatch(/private|cursor|origins/);
});
/** Reuse semantic owners instead of rescanning every result for each toolbar action. */
function catalogControls() {
  const input = screen.getByRole("searchbox", { name: "Search Catalog" });
  return {
    search: within(required(input.closest("form") ?? undefined)),
    kinds: within(screen.getByRole("group", { name: "Resource kinds" })),
    visibility: within(screen.getByRole("group", { name: "Visibility" })),
  };
}

/** Drain the deliberately resolved transport before inspecting the next UI state. */
async function activate(element: HTMLElement) {
  await act(async () => {
    fireEvent.click(element);
  });
}

const record = {
  display: {
    publisherName: "Demo publisher",
    namespaceName: "Catalog demo",
    license: "Apache-2.0",
  },
  resource,
  version,
  listing,
  versionRevision: {
    ...versionRevision,
    id: "revision-1",
    representations: [],
  },
  listingRevision: {
    ...listingRevision,
    id: "listing-revision-1",
    versionRevisionId: "revision-1",
    displayName: "Local helper",
    description: "Governed metadata",
    publisherId: "publisher-1",
    visibility: "public",
    license: "Apache-2.0",
    documentationUrl: "https://example.com/docs",
    supportUrl: "https://example.com/support",
    termsUrl: "javascript:alert(1)",
  },
  release: {
    ...release,
    versionRevisionId: "revision-1",
    listingRevisionId: "listing-revision-1",
    id: "00000000-0000-7000-8000-000000000001",
    state: "published",
    activatedAt: stamp,
    submissionId: "review-1",
  },
  evidence,
};
const local = {
  id: "local",
  type: "local",
  identifier: resource.canonicalIdentifier,
  displayName: "Local helper",
  description: "Governed metadata",
  kind: "agent",
  versionPresent: true,
  version: "1.0.0",
  tags: ["tools", "a", "b", "c", "d"],
  publisher: "publisher-1",
  local: {
    listing,
    resource,
    version: { id: version.id, version: "1.0.0" },
    versionRevision: { id: record.versionRevision.id },
    listingRevision: { id: record.listingRevision.id, visibility: "public" },
    release: {
      id: record.release.id,
      state: "published",
      versionRevisionId: record.versionRevision.id,
      listingRevisionId: record.listingRevision.id,
    },
  },
};
const indexed = {
  id: "indexed",
  type: "indexed",
  identifier: "urn:air:external.test:mcp:tool",
  displayName: "External tool",
  kind: "mcp_server",
  versionPresent: true,
  version: "",
  tags: [],
  indexed: {
    observationGroupId: "observation-group-v1:sha256:" + "a".repeat(64),
    sourceIds: ["00000000-0000-7000-8000-000000000010"],
    sameSourceConflict: false,
    sourceCount: 2,
    sourcesTruncated: true,
    conflict: true,
    normalizedEntryDigest: "digest",
  },
};
const live = {
  id: "live",
  type: "live",
  identifier: "urn:air:live.test:skills:tool",
  displayName: "Live skill",
  kind: "skill",
  versionPresent: false,
  tags: [],
  live: {
    sourceId: "live-source",
    source: "https://live.test",
    entry: { description: "<script>untrusted</script>" },
  },
};
const results = {
  tier: "core",
  items: [local, indexed, live],
  durableTotal: 2,
  liveCount: 1,
  partial: true,
  diagnostics: [{ status: "failed", message: "One source timed out" }],
};

it("reports one non-live match with the exact singular count and no detail reads", async () => {
  const w = screenWorkspace();
  w.post.mockResolvedValue(
    reply({
      tier: "core",
      items: [local],
      durableTotal: 1,
      liveCount: 0,
      partial: false,
    }),
  );
  render(<Catalog {...w.props} />);
  await screen.findByText("Showing 1 of 1 match");
  expect(screen.getByRole("button", { name: "Local helper" })).toBeTruthy();
  expect(w.post).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/catalog/search",
    expect.objectContaining({ body: { text: "", limit: 24 } }),
  );
  expect(w.get).not.toHaveBeenCalled();
  expect(w.patch).not.toHaveBeenCalled();
  expect(w.remove).not.toHaveBeenCalled();
  expect(w.explore).not.toHaveBeenCalled();
  expect(w.props.onFailure).not.toHaveBeenCalled();
});

it("keeps a named-search draft when refresh is declined and reads again only after approval", async () => {
  const w = screenWorkspace();
  w.post.mockResolvedValue(reply(results));
  render(<Catalog {...w.props} access={administrator} />);
  await screen.findByRole("button", { name: "Local helper" });
  fireEvent.click(
    screen.getByText("Save this named search", { selector: "summary" }),
  );
  change("Saved label", "Private unsent library label");
  const confirmation = vi.spyOn(window, "confirm").mockReturnValue(false);
  click("Refresh results");
  expect(confirmation).toHaveBeenCalledOnce();
  expect(w.post).toHaveBeenCalledOnce();
  expect(screen.getByLabelText("Saved label")).toHaveProperty(
    "value",
    "Private unsent library label",
  );
  expect(screen.getByRole("button", { name: "Local helper" })).toBeTruthy();
  confirmation.mockReturnValue(true);
  click("Refresh results");
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(2));
  await screen.findByRole("button", { name: "Local helper" });
  expect(confirmation).toHaveBeenCalledTimes(2);
  expect(w.post.mock.calls.map(([path]) => path)).toEqual([
    "/api/v1/catalog/search",
    "/api/v1/catalog/search",
  ]);
  const refresh = w.post.mock.calls[1]?.[1] as { body: unknown };
  expect(refresh.body).toEqual({ text: "", limit: 24 });
  expect(screen.getByLabelText("Saved label")).toHaveProperty(
    "value",
    "Private unsent library label",
  );
  expect(w.get).not.toHaveBeenCalled();
  expect(w.patch).not.toHaveBeenCalled();
  expect(w.remove).not.toHaveBeenCalled();
  expect(location.pathname).toBe("/unit/catalog");
  expect(location.search).toBe("");
  expect(location.hash).toBe("");
  expect(history.state as unknown).toEqual({ catalogNavigation: 0 });
  expect(w.props.onFailure).not.toHaveBeenCalled();
});

it("rejects a different release in a local preview before enabling record actions", async () => {
  const w = screenWorkspace();
  w.post.mockResolvedValue(reply(results));
  w.get.mockResolvedValue(
    reply({
      ...record,
      release: {
        ...record.release,
        id: "00000000-0000-7000-8000-000000000099",
      },
    }),
  );
  render(<Catalog {...w.props} />);
  fireEvent.click(await screen.findByRole("button", { name: "Local helper" }));
  await screen.findByRole("button", { name: "Retry record" });
  expect(screen.queryByRole("button", { name: "Save resource" })).toBeNull();
  expect(
    screen.queryByRole("button", { name: "Ratings for this version" }),
  ).toBeNull();
});

it("offers one save owner per verified local preview", async () => {
  const w = screenWorkspace();
  w.post.mockResolvedValue(reply(results));
  w.get.mockResolvedValue(reply(record));
  render(<Catalog {...w.props} />);
  fireEvent.click(await screen.findByRole("button", { name: "Local helper" }));
  await screen.findByText("Use this resource");
  expect(screen.getAllByText("Save resource", { exact: true })).toHaveLength(1);
});
it("opens all clipped tags explicitly without applying a filter or enriching other results", async () => {
  const w = screenWorkspace();
  w.post.mockResolvedValue(reply(results));
  w.get.mockResolvedValue(reply(record));
  render(<Catalog {...w.props} />);
  await screen.findByRole("button", { name: "Show all tags for Local helper" });
  click("Show all tags for Local helper");
  const dialog = within(screen.getByRole("dialog"));
  expect(dialog.getByRole("button", { name: "Filter by tag d" })).toBeTruthy();
  await dialog.findByText("Use this resource");
  expect(w.get).toHaveBeenCalledTimes(1);
  expect(w.post).toHaveBeenCalledTimes(1);
});

it("refreshes applied criteria from page one while preserving text and open filter drafts", async () => {
  const w = screenWorkspace();
  w.post.mockResolvedValue(reply({ ...results, nextCursor: "page-two" }));
  render(<Catalog {...w.props} />);
  await screen.findByRole("button", { name: "Local helper" });
  change("Search Catalog", "applied");
  click("Search");
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(2));
  click(/Next page/);
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(3));
  change("Search Catalog", "unsubmitted");
  expect(
    screen.getByText(/Text changed. Choose Search to apply it/),
  ).toBeTruthy();
  click("Filters");
  change("License declaration", "private license draft");
  const pending = deferred<ReturnType<typeof reply>>();
  w.post.mockReturnValueOnce(pending.promise);
  click("Refresh applied results");
  expect(w.post.mock.calls[3]).toMatchObject([
    "/api/v1/catalog/search",
    { body: { text: "applied", limit: 24 } },
  ]);
  expect(
    (w.post.mock.calls[3]![1] as { body: unknown }).body,
  ).not.toHaveProperty("cursor");
  expect(screen.getByText(/Last loaded results are shown/)).toBeTruthy();
  expect(screen.getByRole("button", { name: "Local helper" })).toBeTruthy();
  expect(screen.getByLabelText("License declaration")).toHaveProperty(
    "value",
    "private license draft",
  );
  await act(async () => pending.reject(new APIError(503, "unavailable")));
  await screen.findByRole("button", { name: "Retry this page" });
  expect(screen.getByText(/Last loaded results are shown/)).toBeTruthy();
  click("Retry this page");
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(5));
  expect(w.post.mock.calls[4]).toMatchObject([
    "/api/v1/catalog/search",
    { body: { text: "applied", limit: 24 } },
  ]);
  expect(
    screen.getByRole("searchbox", { name: "Search Catalog" }),
  ).toHaveProperty("value", "unsubmitted");
  expect(screen.getByLabelText("License declaration")).toHaveProperty(
    "value",
    "private license draft",
  );
  click("Close filters");
  click("Refresh results");
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(6));
  expect(screen.queryByRole("button", { name: "Previous page" })).toBeNull();
});

it("compares two page-bound exact results without reads or silent replacement and restores a result control", async () => {
  const w = screenWorkspace();
  w.post.mockResolvedValue(reply(results));
  render(<Catalog {...w.props} />);
  await screen.findByRole("button", { name: "Local helper" });
  const first = screen.getByRole("checkbox", { name: /Compare Local helper/ });
  fireEvent.click(first);
  await waitFor(() => expect(first).toHaveProperty("checked", true));
  fireEvent.click(
    screen.getByRole("checkbox", { name: /Compare External tool/ }),
  );
  await waitFor(() =>
    expect(
      screen.getByRole("checkbox", { name: /Compare External tool/ }),
    ).toHaveProperty("checked", true),
  );
  fireEvent.click(screen.getByRole("checkbox", { name: /Compare Live skill/ }));
  expect(screen.getByText(/Choose at most two results/)).toBeTruthy();
  expect(
    screen.getByRole("checkbox", { name: /Compare Live skill/ }),
  ).toHaveProperty("checked", false);
  click("Compare selected (2/2)");
  const dialog = within(
    screen.getByRole("dialog", { name: "Compare selected resources" }),
  );
  expect(dialog.getByRole("heading", { name: "Local helper" })).toBeTruthy();
  expect(dialog.getByRole("heading", { name: "External tool" })).toBeTruthy();
  expect(dialog.queryByRole("heading", { name: "Live skill" })).toBeNull();
  expect(dialog.getByText("Local release: published")).toBeTruthy();
  expect(
    dialog.getByText("External metadata; no local review or approval"),
  ).toBeTruthy();
  expect(w.get).not.toHaveBeenCalled();
  expect(w.post).toHaveBeenCalledTimes(1);
  click("Close comparison");
  expect(document.activeElement).toBe(first);
  fireEvent.click(first);
  fireEvent.click(screen.getByRole("checkbox", { name: /Compare Live skill/ }));
  click("Compare selected (2/2)");
  expect(screen.getByText(/Transient live result/)).toBeTruthy();
  fireEvent(
    screen.getByRole("dialog", { name: "Compare selected resources" }),
    new Event("cancel", { cancelable: true }),
  );
  click("Table");
  expect(
    screen.getByRole("checkbox", { name: /Compare Live skill/ }),
  ).toHaveProperty("checked", true);
  click("Clear comparison selection");
  expect(screen.queryByRole("button", { name: /Compare selected/ })).toBeNull();
});

it("clears comparison on a new response, page or identity and distinguishes identical labels", async () => {
  const w = screenWorkspace();
  const duplicateNames = {
    ...results,
    items: results.items.map((item) => ({ ...item, displayName: "Same name" })),
    nextCursor: "next",
  };
  w.post.mockResolvedValue(reply(duplicateNames));
  const view = render(<Catalog {...w.props} />);
  await screen.findAllByRole("button", { name: "Same name" });
  const selectors = screen.getAllByRole("checkbox", {
    name: /Compare Same name/,
  });
  fireEvent.click(selectors[0]!);
  fireEvent.click(selectors[1]!);
  click("Refresh results");
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(2));
  expect(screen.queryByRole("button", { name: /Compare selected/ })).toBeNull();
  fireEvent.click(
    screen.getAllByRole("checkbox", { name: /Compare Same name/ })[0]!,
  );
  click(/Next page/);
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(3));
  expect(screen.queryByRole("button", { name: /Compare selected/ })).toBeNull();
  fireEvent.click(
    screen.getAllByRole("checkbox", { name: /Compare Same name/ })[1]!,
  );
  view.rerender(<Catalog {...w.props} access={administrator} />);
  expect(screen.queryByRole("button", { name: /Compare selected/ })).toBeNull();
});

async function ratingPreview() {
  const w = screenWorkspace();
  w.post.mockImplementation(async (path, options) =>
    path === "/api/v1/ratings/read"
      ? reply({
          target: (options as { body: { target: unknown } }).body.target,
          count: 0,
          changedEntry: false,
        })
      : reply(results),
  );
  w.get.mockResolvedValue(reply(record));
  const view = render(<Catalog {...w.props} access={administrator} />);
  await screen.findByRole("button", { name: "Local helper" });
  click("Local helper");
  await screen.findByRole("button", { name: "Ratings for this version" });
  click("Ratings for this version");
  await screen.findByRole("radio", { name: "4 stars" });
  fireEvent.click(screen.getByRole("radio", { name: "4 stars" }));
  return { ...w, view };
}

it.each(["close", "escape", "backdrop", "tag", "namespace", "detail"])(
  "protects a rating draft before preview %s",
  async (exit) => {
    const w = await ratingPreview();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const dialog = screen.getByRole("dialog");
    const leave = () => {
      if (exit === "close") click("Close preview");
      else if (exit === "escape")
        fireEvent(
          dialog,
          new Event("cancel", { bubbles: true, cancelable: true }),
        );
      else if (exit === "backdrop") {
        pointer(dialog, "pointerdown", 20);
        pointer(dialog, "pointerup", 20);
      } else if (exit === "tag")
        fireEvent.click(
          within(dialog).getByRole("button", { name: "Filter by tag tools" }),
        );
      else if (exit === "namespace")
        fireEvent.click(
          within(dialog).getByRole("button", {
            name: "Filter by namespace Catalog demo",
          }),
        );
      else
        fireEvent.click(
          within(dialog).getByRole("link", { name: /Open full record/ }),
        );
    };
    const requests = w.post.mock.calls.length;
    leave();
    expect(confirm).toHaveBeenCalledOnce();
    expect(screen.getByRole("radio", { name: "4 stars" })).toHaveProperty(
      "checked",
      true,
    );
    expect(screen.getByRole("dialog")).toBe(dialog);
    expect(location.pathname).toBe("/unit/catalog");
    expect(w.post).toHaveBeenCalledTimes(requests);
    confirm.mockReturnValue(true);
    leave();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  },
);

it.each(["back", "tag", "namespace"])(
  "preserves a full-record rating draft when its %s exit is declined",
  async (exit) => {
    const w = await ratingPreview();
    w.get.mockResolvedValue(
      reply({
        ...record,
        listingRevision: { ...record.listingRevision, tags: ["tools"] },
      }),
    );
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    fireEvent.click(screen.getByRole("link", { name: /Open full record/ }));
    await screen.findByRole("link", { name: /Back to Catalog/ });
    click("Ratings for this version");
    fireEvent.click(await screen.findByRole("radio", { name: "3 stars" }));
    confirm.mockClear().mockReturnValue(false);
    const reads = w.get.mock.calls.length;
    const requests = w.post.mock.calls.length;
    const leave = () => {
      if (exit === "back")
        fireEvent.click(screen.getByRole("link", { name: /Back to Catalog/ }));
      else
        click(
          exit === "tag"
            ? "Filter by tag tools"
            : "Filter by namespace Catalog demo",
        );
    };
    leave();
    expect(confirm).toHaveBeenCalledOnce();
    expect(location.pathname).toBe(`/unit/catalog/${record.release.id}`);
    expect(screen.getByRole("radio", { name: "3 stars" })).toHaveProperty(
      "checked",
      true,
    );
    expect(w.get).toHaveBeenCalledTimes(reads);
    expect(w.post).toHaveBeenCalledTimes(requests);
    confirm.mockReturnValue(true);
    leave();
    await screen.findByRole("button", { name: "Local helper" });
    expect(location.pathname).toBe("/unit/catalog");
    expect(screen.queryByRole("radio", { name: "3 stars" })).toBeNull();
    expect(w.post.mock.calls.some(([path]) => path === "/api/v1/ratings")).toBe(
      false,
    );
    if (exit !== "back")
      expect(w.post.mock.calls.at(-1)).toMatchObject([
        "/api/v1/catalog/search",
        {
          body:
            exit === "tag"
              ? { tags: ["tools"] }
              : { namespaceId: listing.namespaceId, origins: ["local"] },
        },
      ]);
  },
);

it("keeps a prepared or unresolved nested rating command inside its preview and replays exact inputs", async () => {
  const w = await ratingPreview();
  click("Submit rating");
  let nested = screen.getByRole("dialog", { name: "Submit your rating" });
  fireEvent(nested, new Event("cancel", { bubbles: true, cancelable: true }));
  expect(screen.getAllByRole("dialog")).toHaveLength(1);
  expect(screen.getByRole("radio", { name: "4 stars" })).toHaveProperty(
    "checked",
    true,
  );
  click("Submit rating");
  nested = screen.getByRole("dialog", { name: "Submit your rating" });
  const pending = deferred<ReturnType<typeof reply>>();
  w.post.mockReturnValueOnce(pending.promise);
  fireEvent.click(within(nested).getByRole("button", { name: "Confirm" }));
  const sent = w.post.mock.calls.at(-1);
  fireEvent(nested, new Event("cancel", { bubbles: true, cancelable: true }));
  expect(screen.getAllByRole("dialog")).toHaveLength(2);
  await act(async () =>
    pending.reject(new APIError(503, "DEPENDENCY_UNAVAILABLE")),
  );
  fireEvent(nested, new Event("cancel", { bubbles: true, cancelable: true }));
  expect(screen.getAllByRole("dialog")).toHaveLength(2);
  w.post.mockImplementation(async (path, options) =>
    path === "/api/v1/ratings"
      ? reply({ id: "rating-1", revision: 1 })
      : reply({
          target: (options as { body: { target: unknown } }).body.target,
          count: 0,
          changedEntry: false,
        }),
  );
  fireEvent.click(
    within(nested).getByRole("button", { name: "Retry same command" }),
  );
  await waitFor(() => expect(screen.getAllByRole("dialog")).toHaveLength(1));
  const attempts = w.post.mock.calls.filter(
    ([path]) => path === "/api/v1/ratings",
  );
  expect(attempts).toHaveLength(2);
  expect(attempts[1]).toEqual(sent);
});

it("keeps dirty Catalog input on declined browser history without persisting private content", async () => {
  const w = screenWorkspace();
  w.post.mockResolvedValue(reply(results));
  w.get.mockResolvedValue(reply(record));
  render(<Catalog {...w.props} />);
  await screen.findByRole("button", { name: "Local helper" });
  click("Local helper");
  fireEvent.click(screen.getByRole("link", { name: /Open full record/ }));
  await screen.findByRole("link", { name: /Back to Catalog/ });
  expect(history.state).toEqual({ catalogNavigation: 1 });
  const dirty = (event: Event) => event.preventDefault();
  window.addEventListener("catalog-ui:leave", dirty);
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
  const go = vi.spyOn(history, "go").mockImplementation(() => {});
  try {
    history.replaceState({ catalogNavigation: 0 }, "", "/unit/catalog");
    fireEvent(
      window,
      new PopStateEvent("popstate", { state: history.state as unknown }),
    );
    expect(go).toHaveBeenCalledExactlyOnceWith(1);
    expect(confirm).toHaveBeenCalledOnce();
    expect(screen.getByRole("link", { name: /Back to Catalog/ })).toBeTruthy();
    history.replaceState(
      { catalogNavigation: 1 },
      "",
      `/unit/catalog/${record.release.id}`,
    );
    fireEvent(
      window,
      new PopStateEvent("popstate", { state: history.state as unknown }),
    );
    expect(confirm).toHaveBeenCalledOnce();
    confirm.mockReturnValue(true);
    history.replaceState({ catalogNavigation: 0 }, "", "/unit/catalog");
    fireEvent(
      window,
      new PopStateEvent("popstate", { state: history.state as unknown }),
    );
    await screen.findByRole("button", { name: "Local helper" });
  } finally {
    window.removeEventListener("catalog-ui:leave", dirty);
  }
});

it("does not restore preview focus to an opener removed by navigation", async () => {
  const { props, post, get } = screenWorkspace();
  post.mockResolvedValue(reply(results));
  get.mockResolvedValue(reply(record));
  const view = render(<Catalog {...props} />);
  const opener = await screen.findByRole("button", { name: "Local helper" });
  opener.focus();
  click("Local helper");
  expect(screen.getByRole("dialog")).toBeTruthy();
  const restore = vi.spyOn(opener, "focus");
  view.unmount();
  expect(restore).not.toHaveBeenCalled();
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(post).toHaveBeenCalledOnce();
  expect(get).toHaveBeenCalledOnce();
});

it("fetches only the selected local preview and keeps external metadata inert", async () => {
  const { props, post, get } = screenWorkspace();
  post.mockResolvedValue(reply(results));
  get.mockResolvedValue(reply(record));
  const view = render(<Catalog {...props} />);
  await screen.findByRole("button", { name: "Local helper" });
  expect(screen.getByRole("group", { name: "Resource kinds" })).toBeTruthy();
  expect(screen.getByRole("group", { name: "Result layout" })).toBeTruthy();
  expect(view.container.querySelectorAll("article.resource-card")).toHaveLength(
    3,
  );
  expect(screen.getByText("+1 more")).toBeTruthy();
  expect(get).not.toHaveBeenCalled();
  click("Local helper");
  await screen.findByRole("link", { name: "Documentation" });
  let dialog = within(screen.getByRole("dialog"));
  expect(
    dialog.getByRole("link", { name: /Open full record/ }).getAttribute("href"),
  ).toContain(record.release.id);
  expect(
    dialog.getByRole("link", { name: "Documentation" }).getAttribute("rel"),
  ).toBe("noopener noreferrer");
  expect(dialog.queryByRole("link", { name: "Terms" })).toBeNull();
  expect(dialog.getByRole("region", { name: "Resource handoff" })).toBeTruthy();
  expect(
    dialog.getByRole("region", { name: "Resource assessment" }).textContent,
  ).toContain("Completed");
  expect(dialog.getAllByText("Not assessed")).toHaveLength(2);
  expect(
    dialog
      .getAllByRole("heading", { level: 3 })
      .map((heading) => heading.textContent),
  ).toEqual([
    "Overview",
    "Listing categories",
    "Validation and assessments",
    "Use this resource",
    "Technical details and evidence",
  ]);
  const identities = dialog
    .getByText("Exact release and revision IDs")
    .closest("details");
  expect(identities).toHaveProperty("open", false);
  fireEvent.click(dialog.getByText("Exact release and revision IDs"));
  for (const label of [
    "Release ID",
    "Version revision ID",
    "Listing revision ID",
  ])
    expect(dialog.getByRole("button", { name: `Copy ${label}` })).toBeTruthy();
  expect(
    dialog
      .getByRole("link", { name: "Inspect retained version revision" })
      .getAttribute("href"),
  ).toBe("/unit/version-revisions/revision-1");
  expect(identities?.textContent).toContain(record.release.id);
  expect(identities?.textContent).toContain(record.listingRevision.id);

  expect(
    dialog.queryByRole("navigation", { name: "Record workflows" }),
  ).toBeNull();
  expect(dialog.queryByText("Item feedback")).toBeNull();
  click("Close preview");
  click("External tool");
  dialog = within(screen.getByRole("dialog"));
  expect(dialog.getByText("Conflicting current variants")).toBeTruthy();
  expect(
    dialog.queryByRole("link", { name: "Compare source contributions" }),
  ).toBeNull();
  expect(
    dialog.getByText(/Upstream declarations are not local assessments/),
  ).toBeTruthy();
  view.rerender(
    <Catalog
      {...props}
      access={{
        ...props.access,
        ...testAccess(["source_admin"]),
        scopeEnforcement: true,
        scopes: ["sources.read", "catalog.read"],
      }}
    />,
  );
  expect(
    dialog.queryByRole("link", { name: "Compare source contributions" }),
  ).toBeNull();
  expect(
    dialog.getByRole("link", { name: /Open source/ }).getAttribute("href"),
  ).toBe(`/unit/sources/${indexed.indexed.sourceIds[0]}`);
  expect(
    dialog.getByText("Entry digest (not a document checksum)"),
  ).toBeTruthy();
  expect(dialog.getByText("digest")).toBeTruthy();
  expect(
    dialog
      .getAllByRole("heading", { level: 3 })
      .map((heading) => heading.textContent),
  ).toEqual([
    "Overview",
    "Source provenance",
    "Use this resource",
    "Technical details and evidence",
  ]);

  expect(
    dialog
      .getByRole("link", { name: /Open current indexed record/ })
      .getAttribute("href"),
  ).toContain("versionPresent=true&version=");
  fireEvent(screen.getByRole("dialog"), new Event("cancel", { bubbles: true }));
  click("Live skill");
  expect(
    screen.queryByRole("link", { name: /Open current indexed record/ }),
  ).toBeNull();
  fireEvent.click(screen.getByText("Live entry metadata"));
  expect(view.container.querySelector("script")).toBeNull();
  click("Close preview");
  click("Table");
  expect(screen.getByRole("table")).toBeTruthy();
  click("External tool");
  click("Close preview");
  click("Cards");
  expect(get).toHaveBeenCalledOnce();
  expect(post).toHaveBeenCalledTimes(1);
});

it.each([
  { url: "https://metadata.example.test/card.json" },
  {
    data: {
      url: "https://runtime.example.test/invoke",
      description: "<script>install()</script>",
    },
  },
])("keeps sparse canonical discovery delivery inert: %j", async (delivery) => {
  const entry = {
    identifier: "urn:air:live.test:agents:tool",
    type: "application/a2a-agent-card+json",
    ...delivery,
  };
  const { props, post, get } = screenWorkspace();
  post.mockResolvedValue(
    reply({
      ...results,
      items: [
        {
          ...live,
          kind: "agent",
          displayName: "Live agent",
          identifier: entry.identifier,
          live: { ...live.live, entry },
        },
      ],
      durableTotal: 0,
      liveCount: 1,
      partial: false,
      diagnostics: [],
    }),
  );
  const view = render(<Catalog {...props} />);
  await screen.findByRole("button", { name: "Live agent" });
  click("Live agent");
  const dialog = within(screen.getByRole("dialog"));
  expect(dialog.queryByRole("link")).toBeNull();
  expect(dialog.getByText("No description supplied.")).toBeTruthy();
  expect(dialog.getByText("Not specified")).toBeTruthy();
  expect(dialog.getByText("Not supplied")).toBeTruthy();
  fireEvent.click(dialog.getByText("Live entry metadata"));
  expect(JSON.parse(view.container.querySelector("pre")!.textContent!)).toEqual(
    entry,
  );
  expect(view.container.querySelector("script")).toBeNull();
  expect(get).not.toHaveBeenCalled();
  expect(post).toHaveBeenCalledTimes(1);
});

it("submits draft criteria, resets cursors and removes each applied filter", async () => {
  const { props, post } = screenWorkspace();
  post.mockResolvedValue(reply({ ...results, nextCursor: "next-1" }));
  render(<Catalog {...props} />);
  await screen.findByRole("button", { name: "Local helper" });
  const controls = catalogControls();
  change("Search Catalog", "draft");
  expect(post).toHaveBeenCalledTimes(1);
  await activate(controls.search.getByRole("button", { name: "Search" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  await act(async () => {
    click(/Next page/);
  });
  await waitFor(() =>
    expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
      body: { cursor: "next-1", text: "draft" },
    }),
  );
  await act(async () => {
    click("First page");
  });
  await waitFor(() =>
    expect(post.mock.calls.at(-1)?.[1]).not.toHaveProperty("body.cursor"),
  );
  await activate(controls.kinds.getByRole("button", { name: "Skills" }));
  await screen.findByRole("button", { name: "Remove kind Skill" });
  await act(async () => {
    click(/Filters/);
  });
  const filters = within(screen.getByRole("region", { name: "Filters" }));
  fireEvent.change(filters.getByRole("textbox", { name: "Add tag" }), {
    target: { value: "tools" },
  });
  await activate(filters.getByRole("button", { name: "Add exact tag" }));
  fireEvent.change(filters.getByRole("textbox", { name: "Add tag" }), {
    target: { value: "new" },
  });
  change("License declaration", " Apache-2.0 ");
  editValues("Categories", ["tools", "finance"]);
  await activate(filters.getByRole("checkbox", { name: "Live external" }));
  await activate(
    filters.getByRole("button", { name: "publisher-1 Local publisher" }),
  );
  await activate(filters.getByRole("radio", { name: "Private" }));
  await activate(filters.getByRole("button", { name: "Apply filters" }));
  await screen.findByRole("button", { name: "Remove tag new" });
  expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
    body: {
      license: "Apache-2.0",
      categories: ["tools", "finance"],
      origins: ["local", "indexed"],
    },
  });
  // Keep repeated chip queries within their accessible group instead of
  // recalculating accessible names across the complete result list each time.
  const applied = within(screen.getByRole("group", { name: "Applied search" }));
  const clearOrigin = applied.getByRole("button", {
    name: "Remove origin Local Catalog",
  });
  expect(clearOrigin).toHaveProperty("disabled", false);
  const chips = applied.getAllByRole("button");
  for (const name of [
    "Remove tag tools",
    "Remove tag new",
    "Remove license: Apache-2.0",
    "Remove category tools",
    "Remove category finance",
    "Remove publisher: publisher-1",
    "Remove visibility filter: private",
    "Remove kind Skill",
    "Remove search text draft",
    "Remove origin Local Catalog",
    "Remove origin Indexed external",
  ]) {
    const count = post.mock.calls.length;
    const matches = chips.filter(
      (chip) => chip.getAttribute("aria-label") === name,
    );
    expect(matches).toHaveLength(1);
    const chip = required(matches[0]);
    expect(chip.isConnected).toBe(true);
    await activate(chip);
    await waitFor(() => expect(post).toHaveBeenCalledTimes(count + 1));
  }
  await activate(
    controls.visibility.getByRole("button", { name: "Visibility: Public" }),
  );
  await within(
    screen.getByRole("group", { name: "Applied search" }),
  ).findByRole("button", { name: "Clear all" });
  const beforeAnyVisibility = post.mock.calls.length;
  await activate(
    controls.visibility.getByRole("button", {
      name: "Visibility: All",
    }),
  );
  await waitFor(() =>
    expect(post).toHaveBeenCalledTimes(beforeAnyVisibility + 1),
  );
  expect(
    (post.mock.calls.at(-1)?.[1] as { body: object }).body,
  ).not.toHaveProperty("visibility");
  expect(
    controls.visibility
      .getByRole("button", { name: "Visibility: All" })
      .getAttribute("aria-pressed"),
  ).toBe("true");
  await activate(
    controls.visibility.getByRole("button", { name: "Visibility: Public" }),
  );
  await within(
    screen.getByRole("group", { name: "Applied search" }),
  ).findByRole("button", { name: "Clear all" });
  await activate(
    within(screen.getByRole("group", { name: "Applied search" })).getByRole(
      "button",
      { name: "Clear all" },
    ),
  );
  await waitFor(() =>
    expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
      body: { text: "", limit: 24 },
    }),
  );
  expect(screen.getByLabelText("Search Catalog")).toBe(document.activeElement);
});

it("adds exact result tags to submitted criteria without opening a preview or submitting draft text", async () => {
  const { props, post } = screenWorkspace();
  post.mockResolvedValue(reply({ ...results, nextCursor: "next-1" }));
  render(<Catalog {...props} />);
  await screen.findByRole("button", { name: "Local helper" });
  // The controlled reply puts the local result first. Resolve its current
  // article after each reply: result nodes are deliberately replaced.
  const localCard = () => within(required(screen.getAllByRole("article")[0]));
  expect(
    localCard().getByRole("button", { name: "Local helper" }),
  ).toBeTruthy();
  const controls = catalogControls();
  change("Search Catalog", "submitted");
  await activate(controls.search.getByRole("button", { name: "Search" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  change("Search Catalog", "unsubmitted draft");
  await act(async () => {
    click(/Filters/);
  });
  const filters = within(screen.getByRole("region", { name: "Filters" }));
  await activate(filters.getByRole("checkbox", { name: "Agents" }));
  await activate(filters.getByRole("checkbox", { name: "Skills" }));
  await activate(
    filters.getByRole("button", { name: "publisher-1 Local publisher" }),
  );
  await activate(filters.getByRole("radio", { name: "Public" }));
  await activate(filters.getByRole("button", { name: "Apply filters" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(3));
  expect(
    controls.kinds
      .getByRole("button", { name: "Agents" })
      .getAttribute("aria-pressed"),
  ).toBe("false");
  await act(async () => {
    click(/Next page/);
  });
  await waitFor(() => expect(post).toHaveBeenCalledTimes(4));
  await activate(
    localCard().getByRole("button", { name: "Filter by tag tools" }),
  );
  await waitFor(() => expect(post).toHaveBeenCalledTimes(5));
  expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
    body: {
      text: "submitted",
      kinds: ["agent", "skill"],
      tags: ["tools"],
      publisher: "publisher-1",
      visibility: "public",
    },
  });
  expect(post.mock.calls.at(-1)?.[1]).not.toHaveProperty("body.cursor");
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(screen.getByLabelText("Search Catalog")).toHaveProperty(
    "value",
    "unsubmitted draft",
  );
  expect(screen.getByLabelText("Search Catalog")).toBe(document.activeElement);
  await activate(
    localCard().getByRole("button", { name: "Filter by tag tools" }),
  );
  expect(post).toHaveBeenCalledTimes(5);
  await activate(localCard().getByRole("button", { name: "Filter by tag a" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(6));
  expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
    body: { tags: ["tools", "a"] },
  });
  await activate(
    within(screen.getByRole("group", { name: "Applied search" })).getByRole(
      "button",
      { name: "Remove kind Agent" },
    ),
  );
  await waitFor(() => expect(post).toHaveBeenCalledTimes(7));
  expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
    body: { kinds: ["skill"], tags: ["tools", "a"] },
  });
  await activate(controls.kinds.getByRole("button", { name: "Agents" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(8));
  expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
    body: { text: "submitted", kinds: ["agent"] },
  });
  await act(async () => {
    click(/Next page/);
  });
  await waitFor(() => expect(post).toHaveBeenCalledTimes(9));
  expect(post.mock.calls.at(-1)?.[1]).toHaveProperty("body.cursor", "next-1");
  await activate(controls.kinds.getByRole("button", { name: "All kinds" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(10));
  expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
    body: {
      text: "submitted",
      tags: ["tools", "a"],
      publisher: "publisher-1",
      visibility: "public",
    },
  });
  expect(post.mock.calls.at(-1)?.[1]).not.toHaveProperty("body.kinds");
  expect(post.mock.calls.at(-1)?.[1]).not.toHaveProperty("body.cursor");
  expect(screen.getByLabelText("Search Catalog")).toHaveProperty(
    "value",
    "unsubmitted draft",
  );
  expect(
    controls.kinds.getByRole("button", { name: "All kinds", pressed: true }),
  ).toBeTruthy();
  await activate(
    within(screen.getByRole("group", { name: "Applied search" })).getByRole(
      "button",
      { name: "Clear all" },
    ),
  );
  await waitFor(() => expect(post).toHaveBeenCalledTimes(11));
  expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
    body: { text: "", limit: 24 },
  });
  for (const field of ["cursor", "kinds", "tags", "publisher", "visibility"]) {
    expect(post.mock.calls.at(-1)?.[1]).not.toHaveProperty(`body.${field}`);
  }
  expect(screen.getByLabelText("Search Catalog")).toHaveProperty("value", "");
});

it("disables result tags that would exceed exact-tag bounds", async () => {
  const { props, post } = screenWorkspace();
  const unicodeTag = "\u{1f642}".repeat(80);
  const oversizedTag = unicodeTag + "\u{1f642}";
  post.mockResolvedValue(
    reply({
      ...results,
      items: [
        { ...local, tags: ["tools", "x".repeat(81), unicodeTag, oversizedTag] },
      ],
    }),
  );
  render(<Catalog {...props} />);
  await screen.findByRole("button", { name: "Local helper" });
  expect(
    screen.getByRole("button", { name: `Filter by tag ${"x".repeat(81)}` }),
  ).toHaveProperty("disabled", true);
  expect(
    screen.getByRole("button", { name: `Filter by tag ${oversizedTag}` }),
  ).toHaveProperty("disabled", true);
  expect(
    screen.getByRole("button", { name: `Filter by tag ${unicodeTag}` }),
  ).toHaveProperty("disabled", false);
  click(/Filters/);
  const tagInput = screen.getByLabelText("Add tag");
  const addTag = screen.getByRole("button", { name: "Add exact tag" });
  for (const tag of [
    unicodeTag,
    ...Array.from({ length: 31 }, (_, index) => `tag-${index}`),
  ]) {
    fireEvent.change(tagInput, { target: { value: tag } });
    fireEvent.click(addTag);
  }
  click("Apply filters");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  expect(
    screen.getByRole("button", { name: "Filter by tag tools" }),
  ).toHaveProperty("disabled", true);
  // Already-applied tags remain harmless no-ops even at the collection limit.
  expect(
    screen.getByRole("button", { name: `Filter by tag ${unicodeTag}` }),
  ).toHaveProperty("disabled", false);
  click(`Filter by tag ${unicodeTag}`);
  expect(post).toHaveBeenCalledTimes(2);
});

it("keeps empty results distinct from failures and recovers without replaying old cursors", async () => {
  const { props, post } = screenWorkspace();
  post
    .mockResolvedValueOnce(
      reply({
        ...results,
        items: [],
        durableTotal: 0,
        liveCount: 0,
        partial: false,
      }),
    )
    .mockResolvedValueOnce({
      error: {},
      response: new Response(null, {
        status: 409,
        headers: { "X-Request-ID": "request-1" },
      }),
    })
    .mockResolvedValue(
      reply({
        ...results,
        items: [],
        durableTotal: 0,
        liveCount: 0,
        partial: false,
      }),
    );
  render(<Catalog {...props} />);
  await screen.findByText("No resources found");
  change("Search Catalog", "missing");
  click("Search");
  await screen.findByRole("alert");
  expect(screen.queryByText("No resources found")).toBeNull();
  expect(props.onFailure).toHaveBeenCalledTimes(1);
  click("Start a new search");
  await screen.findByText("No resources found");
  click("Clear search & filters");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(4));
});

it("ignores a late previous search even when the transport ignores cancellation", async () => {
  const { props, post } = screenWorkspace();
  const old = deferred<unknown>();
  post.mockReturnValueOnce(old.promise).mockResolvedValue(
    reply({
      ...results,
      items: [{ ...local, displayName: "New answer" }],
      durableTotal: 1,
    }),
  );
  render(<Catalog {...props} />);
  change("Search Catalog", "new");
  click("Search");
  await screen.findByRole("button", { name: "New answer" });
  await act(async () => old.resolve(reply(results)));
  expect(screen.queryByRole("button", { name: "Local helper" })).toBeNull();
  expect(props.onFailure).not.toHaveBeenCalled();
});

it("loads an exact governed record with workflow links and safe handoffs", async () => {
  const { props, get, post } = screenWorkspace();
  history.replaceState(null, "", `/unit/catalog/${record.release.id}`);
  get.mockResolvedValue(reply(record));
  render(<Catalog {...props} />);
  await screen.findByRole("heading", { name: "Local helper" });
  expect(get).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/catalog/resources/{catalogRecordId}",
    {
      params: { path: { catalogRecordId: record.release.id } },
      signal: expect.any(AbortSignal) as unknown,
    },
  );
  expect(
    screen.getByRole("link", { name: "Approved review" }).getAttribute("href"),
  ).toBe("/unit/reviews/review-1");
  expect(screen.getByText("Complete record metadata")).toBeTruthy();
  expect(
    screen.getByRole("link", { name: "Release history" }).getAttribute("href"),
  ).toBe(`/unit/listings/${record.listing.id}#release-history`);
  const handoff = screen.getByRole("region", { name: "Resource handoff" });
  const governance = screen.getByRole("navigation", {
    name: "Record workflows",
  });
  expect(
    handoff.compareDocumentPosition(governance) &
      Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
  expect(
    screen.getByText(
      "Returning in this tab restores your search; reloading starts a fresh search.",
    ),
  ).toBeTruthy();
  expect(post).not.toHaveBeenCalled();
});

it.each(["success", "failure"])(
  "discards a late detail %s after leaving the record",
  async (outcome) => {
    const { props, get, post } = screenWorkspace();
    const pending = deferred<unknown>();
    history.replaceState(null, "", `/unit/catalog/${record.release.id}`);
    get.mockReturnValue(pending.promise);
    const view = render(<Catalog {...props} />);
    expect(screen.getByText("Loading record...")).toBeTruthy();
    const signal = (required(get.mock.calls[0])[1] as { signal: AbortSignal })
      .signal;
    view.unmount();
    expect(signal.aborted).toBe(true);
    await act(async () => {
      if (outcome === "success") pending.resolve(reply(record));
      else pending.reject(new Error("Late private transport failure"));
    });
    expect(props.onFailure).not.toHaveBeenCalled();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(post).not.toHaveBeenCalled();
  },
);

it.each([
  [new Error("Private proxy response"), "The Catalog could not be reached."],
  [new APIError(404, "NOT_FOUND"), new APIError(404, "NOT_FOUND").message],
] as const)(
  "reports a failed record safely and leaves a Catalog recovery link: %s",
  async (error, message) => {
    const { props, get, post } = screenWorkspace();
    history.replaceState(null, "", `/unit/catalog/${record.release.id}`);
    get.mockRejectedValue(error);
    render(<Catalog {...props} />);
    expect((await screen.findByRole("alert")).textContent).toBe(message);
    expect(screen.queryByRole("heading", { name: "Local helper" })).toBeNull();
    expect(
      screen
        .getByRole("link", { name: /Back to Catalog/ })
        .getAttribute("href"),
    ).toBe("/unit/catalog");
    expect(props.onFailure).toHaveBeenCalledExactlyOnceWith(error);
    expect(post).not.toHaveBeenCalled();
    get.mockResolvedValue(reply(record));
    click("Retry record");
    await screen.findByRole("heading", { name: "Local helper" });
    expect(screen.queryByRole("alert")).toBeNull();
    expect(get).toHaveBeenCalledTimes(2);
  },
);

it("preserves absent indexed versions and private local metadata in preview links", async () => {
  const { props, get, post } = screenWorkspace();
  get.mockResolvedValue(
    reply({
      ...record,
      listingRevision: {
        ...record.listingRevision,
        visibility: "private",
        license: undefined,
      },
    }),
  );
  post.mockResolvedValue(
    reply({
      ...results,
      items: [
        {
          ...local,
          local: {
            ...record,
            listingRevision: {
              ...record.listingRevision,
              visibility: "private",
              license: undefined,
            },
          },
        },
        {
          ...indexed,
          versionPresent: false,
          version: undefined,
          indexed: {
            ...indexed.indexed,
            sourcesTruncated: false,
            conflict: false,
          },
        },
      ],
      durableTotal: 2,
    }),
  );
  render(<Catalog {...props} />);
  await screen.findByRole("button", { name: "Local helper" });
  click("Local helper");
  await screen.findByRole("link", { name: /Open full record/ });
  expect(
    within(screen.getByRole("dialog")).getByText("Not supplied"),
  ).toBeTruthy();
  click("Close preview");
  click("External tool");
  const dialog = within(screen.getByRole("dialog"));
  const link = new URL(
    dialog
      .getByRole("link", { name: /Open current indexed record/ })
      .getAttribute("href")!,
    location.origin,
  );
  expect(link.searchParams.get("versionPresent")).toBe("false");
  expect(link.searchParams.has("version")).toBe(false);
  expect(dialog.queryByText("Conflicting current variants")).toBeNull();
  expect(dialog.getByText("No conflict reported")).toBeTruthy();
  expect(get).toHaveBeenCalledOnce();
  expect(post).toHaveBeenCalledTimes(1);
});

it.each(["success", "failure"])(
  "cancels a closed preview and ignores its late %s",
  async (outcome) => {
    const { props, post, get } = screenWorkspace();
    post.mockResolvedValue(reply(results));
    const pending = deferred<ReturnType<typeof reply>>();
    get.mockReturnValue(pending.promise);
    render(<Catalog {...props} />);
    await screen.findByRole("button", { name: "Local helper" });
    expect(get).not.toHaveBeenCalled();
    click("Local helper");
    expect(
      within(screen.getByRole("dialog")).getByRole("status").textContent,
    ).toContain("Loading record");
    const options = required(get.mock.calls[0])[1] as {
      params: { path: { catalogRecordId: string } };
      signal: AbortSignal;
    };
    expect(required(get.mock.calls[0])[0]).toBe(
      "/api/v1/catalog/resources/{catalogRecordId}",
    );
    expect(options.params.path.catalogRecordId).toBe(record.release.id);
    click("Close preview");
    expect(options.signal.aborted).toBe(true);
    click("External tool");
    await act(async () => {
      if (outcome === "success") pending.resolve(reply(record));
      else pending.reject(new APIError(403, "FORBIDDEN", "late-preview"));
    });
    expect(
      within(screen.getByRole("dialog")).queryByRole("link", {
        name: "Documentation",
      }),
    ).toBeNull();
    expect(props.onFailure).not.toHaveBeenCalled();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(get).toHaveBeenCalledOnce();
  },
);

it.each([
  [
    new APIError(403, "FORBIDDEN", "request-preview"),
    "Your current access does not permit this action. Request: request-preview",
  ],
  [
    new APIError(404, "NOT_FOUND", "withdrawn-preview"),
    "This record is unavailable with your current access. Check the reference or return to its list. Request: withdrawn-preview",
  ],
  [
    new APIError(503, "UNAVAILABLE"),
    "The request could not be completed. Please try again.",
  ],
  [new Error("Private upstream response"), "The Catalog could not be reached."],
] as const)(
  "shows selected preview failures without stale detail",
  async (error, message) => {
    const { props, post, get } = screenWorkspace();
    post.mockResolvedValue(reply(results));
    get.mockRejectedValueOnce(error).mockResolvedValue(reply(record));
    render(<Catalog {...props} />);
    await screen.findByRole("button", { name: "Local helper" });
    click("Local helper");
    expect((await screen.findByRole("alert")).textContent).toBe(message);
    expect(props.onFailure).toHaveBeenCalledExactlyOnceWith(error);
    expect(screen.queryByRole("link", { name: "Documentation" })).toBeNull();
    click("Retry record");
    expect(
      (await screen.findByRole("link", { name: "Documentation" })).getAttribute(
        "href",
      ),
    ).toBe("https://example.com/docs");
    expect(screen.queryByRole("alert")).toBeNull();
    expect(get).toHaveBeenCalledTimes(2);
    expect(props.onFailure).toHaveBeenCalledExactlyOnceWith(error);
    expect(post).toHaveBeenCalledOnce();
  },
);

it("bounds previous-page cursors, rereads pages and clears history on criteria changes or restart", async () => {
  const { props, post } = screenWorkspace();
  const response = (page: number) =>
    reply({
      ...results,
      partial: false,
      liveCount: 0,
      items: [{ ...local, displayName: `Page ${page}` }],
      nextCursor: `page-${page + 1}`,
    });
  post.mockImplementation(async (_path, options) => {
    const { cursor } = (options as { body: { cursor?: string } }).body;
    const page = cursor ? Number(cursor.slice(5)) : 0;
    return response(page);
  });
  render(<Catalog {...props} />);
  await screen.findByRole("button", { name: "Page 0" });
  const pageResult = (page: number) =>
    within(screen.getByRole("article")).getByRole("button", {
      name: `Page ${page}`,
    });
  const controls = catalogControls();
  const pager = () =>
    within(
      required(
        screen.getByText(/Next page/, { selector: "button" }).parentElement ??
          undefined,
      ),
    );
  const turnPage = async (name: string | RegExp) => {
    const button = await waitFor(() => {
      const current = pager().getByRole("button", { name });
      expect(current).toHaveProperty("disabled", false);
      return current;
    });
    await act(async () => fireEvent.click(button));
  };
  for (let page = 1; page <= 12; page++) {
    await turnPage(/Next page/);

    expect(pageResult(page)).toBeTruthy();
  }
  expect(post).toHaveBeenCalledTimes(13);
  for (let page = 11; page >= 2; page--) {
    await turnPage("Previous page");

    expect(pageResult(page)).toBeTruthy();
    expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
      body: { cursor: `page-${page}` },
    });
  }
  expect(screen.queryByRole("button", { name: "Previous page" })).toBeNull();
  expect(post).toHaveBeenCalledTimes(23);
  const replacedNext = pager().getByRole("button", { name: /Next page/ });
  const firstPage = deferred<ReturnType<typeof reply>>();
  post.mockReturnValueOnce(firstPage.promise);
  await turnPage("First page");
  expect(replacedNext.isConnected).toBe(false);
  expect(screen.queryByRole("article")).toBeNull();
  await act(async () => firstPage.resolve(response(0)));

  expect(pageResult(0)).toBeTruthy();
  expect(post.mock.calls.at(-1)?.[1]).not.toHaveProperty("body.cursor");
  await turnPage(/Next page/);

  expect(pageResult(1)).toBeTruthy();
  await turnPage("Previous page");

  expect(pageResult(0)).toBeTruthy();
  expect(post.mock.calls.at(-1)?.[1]).not.toHaveProperty("body.cursor");
  await turnPage(/Next page/);

  expect(pageResult(1)).toBeTruthy();
  await act(async () => {
    fireEvent.click(controls.kinds.getByRole("button", { name: "Plugins" }));
  });

  expect(pageResult(0)).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Previous page" })).toBeNull();
  expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
    body: { kinds: ["plugin"] },
  });
  await turnPage(/Next page/);

  expect(pageResult(1)).toBeTruthy();
  await act(async () => {
    fireEvent.click(controls.search.getByRole("button", { name: "Search" }));
  });

  expect(pageResult(0)).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Previous page" })).toBeNull();
  expect(location.search).toBe("");
});

it("rejects a stale Previous cursor without silently replacing the result and recovers from a fresh search", async () => {
  const { props, post } = screenWorkspace();
  const page = { ...results, partial: false, nextCursor: "next-1" };
  post
    .mockResolvedValueOnce(reply(page))
    .mockResolvedValue(reply({ ...page, nextCursor: "next-2" }));
  render(<Catalog {...props} />);
  await screen.findByRole("button", { name: "Local helper" });
  click(/Next page/);
  await screen.findByRole("button", { name: "Previous page" });
  click(/Next page/);
  await waitFor(() =>
    expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
      body: { cursor: "next-2" },
    }),
  );
  await screen.findByRole("button", { name: "Local helper" });
  post.mockRejectedValueOnce(
    new APIError(400, "INVALID_ARGUMENT", "stale-page"),
  );
  click("Previous page");
  await screen.findByRole("alert");
  expect(screen.queryByRole("button", { name: "Previous page" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Local helper" })).toBeNull();
  expect(post).toHaveBeenCalledTimes(4);
  expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
    body: { cursor: "next-1" },
  });
  click("Start a new search");
  await screen.findByRole("button", { name: "Local helper" });
  expect(post.mock.calls.at(-1)?.[1]).not.toHaveProperty("body.cursor");
  expect(screen.queryByRole("button", { name: "Previous page" })).toBeNull();
});

it.each([
  new Error("private network failure"),
  new APIError(429, "RATE_LIMITED", "api-limit"),
  new APIError(502, "DEPENDENCY_UNAVAILABLE", "api-unavailable"),
  new APIError(503, "DEPENDENCY_UNAVAILABLE", "peer-outage"),
])(
  "keeps displayed results and retries the exact continuation after %s",
  async (failure) => {
    const { props, post } = screenWorkspace();
    const first = {
      ...results,
      partial: false,
      nextCursor: "signed-live-window",
    };
    post
      .mockResolvedValueOnce(reply(first))
      .mockRejectedValueOnce(failure)
      .mockResolvedValueOnce(
        reply({ ...first, nextCursor: undefined, items: [results.items[0]!] }),
      );
    render(<Catalog {...props} />);
    await screen.findByRole("button", { name: "Local helper" });
    click(/Next page/);
    await screen.findByRole("alert");
    expect(screen.getByRole("alert").textContent).toContain(
      "The previous page is still shown. Retry this page, or start a new search.",
    );
    expect(screen.getByRole("alert").textContent).not.toContain(
      "private network",
    );
    expect(post).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("button", { name: "Local helper" })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Next page/ })).toHaveProperty(
      "disabled",
      true,
    );
    expect(
      screen.getByRole("button", { name: "Previous page" }),
    ).toHaveProperty("disabled", true);
    const failed = post.mock.calls[1]?.[1];
    click("Retry this page");
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
    expect((post.mock.calls[2]?.[1] as { body: unknown }).body).toEqual(
      (failed as { body: unknown }).body,
    );
    expect((post.mock.calls[2]?.[1] as { body: unknown }).body).toMatchObject({
      cursor: "signed-live-window",
    });
    expect(screen.getByRole("button", { name: "Local helper" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Next page/ })).toBeNull();
  },
);

it("reuses the preview record for handoff and aborts its pending download on dismissal", async () => {
  const { props, post, get } = screenWorkspace();
  const pending = deferred<unknown>();
  post.mockResolvedValue(reply(results));
  get
    .mockResolvedValueOnce(
      reply({
        ...record,
        versionRevision: {
          ...record.versionRevision,
          representations: [{ ...representation, artifactId: "a".repeat(64) }],
        },
      }),
    )
    .mockReturnValue(pending.promise);
  render(<Catalog {...props} />);
  const opener = await screen.findByRole("button", { name: "Local helper" });
  opener.focus();
  click("Local helper");
  await screen.findByRole("button", {
    name: "Prepare retained document download",
  });
  expect(
    screen.getByRole("heading", { name: "Use this resource", level: 3 }),
  ).toBeTruthy();
  expect(
    screen.getByRole("heading", {
      name: "primary: application/json",
      level: 4,
    }),
  ).toBeTruthy();
  expect(get).toHaveBeenCalledOnce();
  click("Prepare retained document download");
  const signal = (required(get.mock.calls[1])[1] as { signal: AbortSignal })
    .signal;
  click("Close preview");
  expect(signal.aborted).toBe(true);
  expect(document.activeElement).toBe(opener);
  await act(async () => pending.reject(new Error("late private download")));
  expect(props.onFailure).not.toHaveBeenCalled();
  expect(screen.queryByRole("alert")).toBeNull();
  expect(
    screen.queryByRole("link", { name: "Save retained document" }),
  ).toBeNull();
  expect(post).toHaveBeenCalledOnce();
  expect(get).toHaveBeenCalledTimes(2);
});

it("offers honest empty and partial-result recovery without implying a tenant has no resources", async () => {
  const { props, post } = screenWorkspace();
  const { diagnostics: _diagnostics, ...withoutDiagnostics } = results;
  post
    .mockResolvedValueOnce(
      reply({
        ...results,
        items: [],
        durableTotal: 0,
        liveCount: 0,
        partial: false,
      }),
    )
    .mockResolvedValue(reply(withoutDiagnostics));
  const view = render(<Catalog {...props} />);
  await screen.findByText(/No discoverable resources were returned/);
  expect(screen.queryByRole("link", { name: "Create a resource" })).toBeNull();
  view.rerender(
    <Catalog
      {...props}
      access={{ ...props.access, ...testAccess(["reader", "contributor"]) }}
    />,
  );
  expect(
    screen
      .getByRole("link", { name: "Create a resource" })
      .getAttribute("href"),
  ).toBe("/unit/resources/new");
  view.rerender(
    <Catalog
      {...props}
      access={{
        ...props.access,
        availablePermissions: [
          ...props.access.availablePermissions,
          "resource.create",
        ],
        nextCursor: "more",
      }}
    />,
  );
  expect(screen.getByRole("link", { name: "Choose a namespace" })).toBeTruthy();
  click("Refresh results");
  await screen.findByRole("button", { name: "Local helper" });
  expect(
    screen.getByText(/Some source queries or results are incomplete/),
  ).toBeTruthy();
  expect(screen.getByRole("button", { name: "External tool" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Live skill" })).toBeTruthy();
  expect(screen.queryByText("Search diagnostics")).toBeNull();
  expect(post).toHaveBeenCalledTimes(2);
  click("Retry search");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(3));
  expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
    body: (post.mock.calls[1]?.[1] as { body: unknown }).body,
  });
  expect(post.mock.calls.at(-1)?.[1]).not.toHaveProperty("body.cursor");
});

it("presents all four kinds equally and uses wire values when selecting them", async () => {
  const { props, post, get } = screenWorkspace();
  post.mockResolvedValue(
    reply({
      ...results,
      items: [{ ...indexed, kind: "plugin", displayName: "Portable tools" }],
    }),
  );
  const view = render(<Catalog {...props} />);
  await screen.findByRole("button", { name: "Portable tools" });
  const kinds = within(screen.getByRole("group", { name: "Resource kinds" }));
  const expected = [
    ["Agents", "agent"],
    ["MCP Servers", "mcp_server"],
    ["Skills", "skill"],
    ["Plugins", "plugin"],
  ] as const;
  expect(kinds.getAllByRole("button")).toHaveLength(5);
  expect(
    view.container.querySelectorAll(".kind-picker .kind-icon"),
  ).toHaveLength(4);
  for (const [label, value] of expected) {
    expect(
      view.container.querySelector(`.kind-picker .kind-icon.${value} svg`),
    ).not.toBeNull();
    fireEvent.click(kinds.getByRole("button", { name: label }));
    await waitFor(() =>
      expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
        body: { kinds: [value] },
      }),
    );
    expect(
      kinds.getByRole("button", { name: label, pressed: true }),
    ).toBeTruthy();
    expect(
      kinds.getByRole("button", { name: "All kinds", pressed: false }),
    ).toBeTruthy();
  }
  expect(within(screen.getByRole("article")).getByText("Plugin")).toBeTruthy();
  expect(
    view.container.querySelector(
      'article[data-kind="plugin"] .kind-icon.plugin svg',
    ),
  ).not.toBeNull();
  click("Table");
  expect(
    within(screen.getByRole("table")).getByRole("cell", {
      name: "Plugin",
    }),
  ).toBeTruthy();
  expect(get).not.toHaveBeenCalled();
});

/** jsdom has no PointerEvent; dispatch its actual pointer fields through a bubbling event. */
function pointer(target: Element, type: string, x: number, id = 1, button = 0) {
  const event = new MouseEvent(type, {
    bubbles: true,
    cancelable: true,
    clientX: x,
    clientY: 50,
    button,
  });
  Object.defineProperties(event, {
    pointerId: { value: id },
    isPrimary: { value: true },
  });
  fireEvent(target, event);
  return event;
}

it("dismisses only a completed primary outside gesture and restores focus without rereading", async () => {
  const { props, post, get } = screenWorkspace();
  post.mockResolvedValue(reply(results));
  get.mockResolvedValue(reply(record));
  render(<Catalog {...props} />);
  const opener = await screen.findByRole("button", { name: "Local helper" });
  opener.focus();
  click("Local helper");
  await screen.findByRole("link", { name: "Documentation" });
  const dialog = screen.getByRole("dialog");
  vi.spyOn(dialog, "getBoundingClientRect").mockReturnValue(
    new DOMRect(200, 0, 400, 500),
  );
  pointer(dialog, "pointerup", 20); // No preceding press.
  expect(pointer(dialog, "pointerdown", 300).defaultPrevented).toBe(false);
  pointer(dialog, "pointerup", 20); // Inside to outside.
  expect(pointer(dialog, "pointerdown", 20).defaultPrevented).toBe(true);
  pointer(dialog, "pointerup", 300); // Outside to inside.
  pointer(dialog, "pointerdown", 20);
  pointer(dialog, "pointercancel", 20);
  pointer(dialog, "pointerup", 20);
  pointer(dialog, "pointerdown", 20);
  pointer(dialog, "pointerup", 20, 2); // Another pointer.
  pointer(dialog, "pointerdown", 20, 1, 2);
  pointer(dialog, "pointerup", 20, 1, 2);
  expect(screen.getByRole("dialog")).toBe(dialog);
  pointer(dialog, "pointerdown", 20);
  pointer(dialog, "pointerup", 20);
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(document.activeElement).toBe(opener);
  expect(post).toHaveBeenCalledOnce();
  expect(get).toHaveBeenCalledOnce();
});

it("filters an exact preview tag while preserving submitted criteria and discarding the cursor and text draft", async () => {
  const { props, post, get } = screenWorkspace();
  post.mockResolvedValue(reply({ ...results, nextCursor: "page-two" }));
  get.mockResolvedValue(reply(record));
  render(<Catalog {...props} />);
  await screen.findByRole("button", { name: "Local helper" });
  change("Search Catalog", "submitted");
  click("Search");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  await screen.findByRole("button", { name: "Local helper" });
  click("Next page →");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(3));
  await screen.findByRole("button", { name: "Local helper" });
  change("Search Catalog", "unsent draft");
  click("Local helper");
  const dialog = within(screen.getByRole("dialog"));
  fireEvent.click(dialog.getByRole("button", { name: "Filter by tag tools" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(4));
  expect(post.mock.calls[3]?.[1]).toEqual(
    expect.objectContaining({
      body: {
        text: "submitted",
        limit: 24,
        tags: ["tools"],
      },
    }),
  );
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(document.activeElement).toBe(
    screen.getByRole("searchbox", { name: "Search Catalog" }),
  );
});

it("navigates from a full-detail tag to an in-memory filtered Catalog with clean history and reversible reads", async () => {
  const { props, get, post } = screenWorkspace();
  history.replaceState(null, "", `/unit/catalog/${record.release.id}`);
  get.mockResolvedValue(
    reply({
      ...record,
      listingRevision: { ...record.listingRevision, tags: ["private,exact"] },
    }),
  );
  post.mockResolvedValue(reply(results));
  render(<Catalog {...props} />);
  await screen.findByRole("button", { name: "Filter by tag private,exact" });
  expect(post).not.toHaveBeenCalled();
  click("Filter by tag private,exact");
  await screen.findByRole("heading", { name: "Catalog" });
  expect(location.pathname).toBe("/unit/catalog");
  expect(location.search).toBe("");
  expect(history.state).toEqual({ catalogNavigation: 1 });
  expect(post).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/catalog/search",
    expect.objectContaining({
      body: { text: "", limit: 24, tags: ["private,exact"] },
    }),
  );
  await act(async () => {
    history.replaceState(null, "", `/unit/catalog/${record.release.id}`);
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await screen.findByRole("heading", { name: "Local helper" });
  expect(get).toHaveBeenCalledTimes(2);
  click("Filter by tag private,exact");
  await screen.findByRole("heading", { name: "Catalog" });
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
    body: { text: "", limit: 24, tags: ["private,exact"] },
  });
  expect(location.pathname).toBe("/unit/catalog");
});

it("presents current local labels without directory lookup or replacing exact publisher filter identity", async () => {
  const { props, post, get } = screenWorkspace();
  post.mockResolvedValue(
    reply({
      ...results,
      items: [{ ...local, local: { ...local.local, display: record.display } }],
    }),
  );
  render(<Catalog {...props} />);
  expect(await screen.findByText("Demo publisher")).toBeTruthy();
  expect(screen.getByText("Catalog demo")).toBeTruthy();
  expect(screen.queryByText("Apache-2.0")).toBeNull();
  expect(get).not.toHaveBeenCalled();
  expect(screen.queryByText("Declared remote interface")).toBeNull();
  expect(screen.getByText("Local Catalog")).toBeTruthy();
  click("Table");
  expect(await screen.findByText("Demo publisher")).toBeTruthy();
  expect(screen.getByText("Catalog demo")).toBeTruthy();
  expect(screen.getByText("Apache-2.0")).toBeTruthy();
  expect(screen.getByText("published")).toBeTruthy();
  expect(get).not.toHaveBeenCalled();
});

it.each([true, false])(
  "bounds a deprecated preview license independently of optional projection presence (%s)",
  async (withProjection) => {
    const { props, post, get } = screenWorkspace();
    const license = "𐐀".repeat(257);
    const retained = {
      ...record,
      display: withProjection
        ? { ...record.display, license: undefined, licenseOmitted: true }
        : undefined,
      listingRevision: { ...record.listingRevision, license },
      release: { ...record.release, state: "deprecated" },
    };
    post.mockResolvedValue(
      reply({
        ...results,
        items: [
          {
            ...local,
            local: {
              ...local.local,
              release: { ...local.local.release, state: "deprecated" },
            },
          },
        ],
      }),
    );
    get.mockResolvedValue(reply(retained));
    render(<Catalog {...props} />);
    await screen.findByRole("button", { name: "Local helper" });
    click("Table");
    expect(screen.getByText("deprecated")).toBeTruthy();
    click("Local helper");
    const dialog = within(screen.getByRole("dialog"));
    expect(
      await dialog.findByText("See full record for the complete declaration."),
    ).toBeTruthy();
    expect(dialog.queryByText(license)).toBeNull();
    expect(dialog.getByText("deprecated")).toBeTruthy();
    expect(dialog.getByText("Local Catalog")).toBeTruthy();
    // The full record keeps the exact declaration.
    act(() => {
      history.pushState(null, "", "/unit/catalog/" + record.release.id);
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    expect(await screen.findByText(license)).toBeTruthy();
    expect(screen.getByText("Local Catalog")).toBeTruthy();
  },
);

it.each(["auto", "manual"] as const)(
  "owns Catalog history scrolling and restores the caller's %s mode on unmount",
  async (mode) => {
    const previous = Object.getOwnPropertyDescriptor(
      history,
      "scrollRestoration",
    );
    Object.defineProperty(history, "scrollRestoration", {
      configurable: true,
      writable: true,
      value: mode,
    });
    let dispose: (() => void) | undefined;
    try {
      const { props, post } = screenWorkspace();
      post.mockResolvedValue(reply(results));
      const { unmount } = render(<Catalog {...props} />);
      dispose = unmount;
      await screen.findByRole("button", { name: "Local helper" });
      expect(history.scrollRestoration).toBe("manual");
      unmount();
      expect(history.scrollRestoration).toBe(mode);
    } finally {
      dispose?.();
      if (previous)
        Object.defineProperty(history, "scrollRestoration", previous);
      else Reflect.deleteProperty(history, "scrollRestoration");
    }
  },
);

it("restores submitted criteria and layout after same-tab full-record navigation", async () => {
  const { props, post, get } = screenWorkspace();
  post.mockResolvedValue(reply(results));
  get.mockResolvedValue(reply(record));
  const scroll = vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  vi.spyOn(window, "scrollY", "get").mockReturnValue(417);
  render(<Catalog {...props} />);
  await screen.findByRole("button", { name: "Local helper" });
  change("Search Catalog", "saved");
  click("Search");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  change("Search Catalog", "unsent draft");
  click("Table");
  click("Local helper");
  await screen.findByRole("dialog");
  fireEvent.click(screen.getByRole("link", { name: /Open full record/ }));
  await screen.findByRole("link", { name: /Back to Catalog/ });
  expect(location.pathname).toBe(`/unit/catalog/${record.release.id}`);
  fireEvent.click(screen.getByRole("link", { name: /Back to Catalog/ }));
  await screen.findByRole("button", { name: "Local helper" });
  expect(post).toHaveBeenCalledTimes(3);
  expect((post.mock.calls.at(-1)?.[1] as { body: unknown }).body).toEqual({
    text: "saved",
    limit: 24,
  });
  expect(
    screen.getByRole("searchbox", { name: "Search Catalog" }),
  ).toHaveProperty("value", "unsent draft");
  await waitFor(() => expect(scroll).toHaveBeenLastCalledWith(0, 417));
  expect(screen.getByRole("button", { name: "Table" })).toHaveProperty(
    "ariaPressed",
    "true",
  );
});

it("returns a full record to its applied Explore page without submitting Catalog search or private history state", async () => {
  history.replaceState(null, "", "/unit/explore");
  const { props, post, get, patch, remove, explore } = screenWorkspace();
  get.mockImplementation(async (path, options) => {
    if (path === "/api/v1/catalog/resources") {
      const { params } = options as {
        params: { query: { cursor?: string } };
      };
      return reply({
        items: [record],
        ...(params.query.cursor ? {} : { nextCursor: "private-browse-cursor" }),
      });
    }
    if (path === "/api/v1/catalog/resources/{catalogRecordId}")
      return reply(record);
    throw new Error("Unexpected Explore return request");
  });
  const browseQueries = () =>
    get.mock.calls
      .filter(([path]) => path === "/api/v1/catalog/resources")
      .map(
        ([, options]) =>
          (options as { params: { query: unknown } }).params.query,
      );
  const scroll = vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  const scrollY = vi.spyOn(window, "scrollY", "get").mockReturnValue(683);
  render(<Catalog {...props} access={administrator} />);
  await screen.findByRole("link", { name: "Local helper" });
  change("Kind", "agent");
  await waitFor(() => expect(browseQueries()).toHaveLength(2));
  change("Visibility", "public");
  await waitFor(() => expect(browseQueries()).toHaveLength(3));
  click(/Next page/);
  await waitFor(() =>
    expect(browseQueries()).toEqual([
      { limit: 25 },
      { kind: "agent", limit: 25 },
      { kind: "agent", visibility: "public", limit: 25 },
      {
        kind: "agent",
        visibility: "public",
        limit: 25,
        cursor: "private-browse-cursor",
      },
    ]),
  );
  fireEvent.click(screen.getByRole("link", { name: "Local helper" }));
  const back = await screen.findByRole("link", { name: /Back to Explore/ });
  expect(back.getAttribute("href")).toBe("/unit/explore");
  const returnToResults = await screen.findByRole("link", {
    name: "Return to results",
  });
  expect(returnToResults.getAttribute("href")).toBe("/unit/explore");
  expect(get).toHaveBeenCalledTimes(5);
  expect(location.pathname).toBe(`/unit/catalog/${record.release.id}`);
  expect(get.mock.calls.at(-1)).toMatchObject([
    "/api/v1/catalog/resources/{catalogRecordId}",
    { params: { path: { catalogRecordId: record.release.id } } },
  ]);
  expect(screen.queryByRole("link", { name: /Back to Catalog/ })).toBeNull();
  scrollY.mockReturnValue(17);
  fireEvent.click(back);
  await screen.findByRole("link", { name: "Local helper" });
  await waitFor(() => expect(scroll).toHaveBeenLastCalledWith(0, 683));
  expect(location.pathname).toBe("/unit/explore");
  expect(location.search).toBe("");
  expect(location.hash).toBe("");
  expect(screen.getByLabelText("Kind")).toHaveProperty("value", "agent");
  expect(screen.getByLabelText("Visibility")).toHaveProperty("value", "public");
  expect(screen.getByRole("button", { name: "First page" })).toHaveProperty(
    "disabled",
    false,
  );
  expect(browseQueries()).toHaveLength(4);
  expect(get).toHaveBeenCalledTimes(5);
  expect(history.state).toEqual({ catalogNavigation: 2 });
  expect(JSON.stringify(history.state)).not.toMatch(
    /private|agent|public|cursor/,
  );
  expect(post).not.toHaveBeenCalled();
  expect(patch).not.toHaveBeenCalled();
  expect(remove).not.toHaveBeenCalled();
  expect(explore).not.toHaveBeenCalled();
  expect(props.onFailure).not.toHaveBeenCalled();
});

it.each([false, true])(
  "restores indexed detail's submitted search, cursor and layout with exact version presence (present=%s)",
  async (present) => {
    const { props, post, get } = screenWorkspace();
    const external = {
      ...indexed,
      versionPresent: present,
      ...(present ? { version: "" } : { version: undefined }),
    };
    post.mockResolvedValue(
      reply({ ...results, items: [external], nextCursor: "next-page" }),
    );
    get.mockResolvedValue(reply(external));
    const scroll = vi.spyOn(window, "scrollTo").mockImplementation(() => {});
    const scrollY = vi.spyOn(window, "scrollY", "get").mockReturnValue(417);
    render(<Catalog {...props} access={administrator} />);
    await screen.findByRole("button", { name: "External tool" });
    change("Search Catalog", "submitted");
    click("Search");
    await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
    change("Search Catalog", "unsent query");
    click("Table");
    click(/Next page/);
    await waitFor(() => expect(post).toHaveBeenCalledTimes(3));
    click("External tool");
    const link = screen.getByRole("link", {
      name: /Open current indexed record/,
    });
    const href = link.getAttribute("href")!;
    fireEvent.click(link);
    await screen.findByRole("heading", { name: "External catalog record" });
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: {
        query: {
          identifier: indexed.identifier,
          versionPresent: present,
          ...(present ? { version: "" } : {}),
        },
      },
    });
    expect(JSON.stringify(history.state)).not.toMatch(/submitted|unsent/);
    fireEvent.click(screen.getByRole("link", { name: "Back to Catalog" }));
    await screen.findByRole("button", { name: "External tool" });
    expect(
      screen
        .getByRole("button", { name: "Table" })
        .getAttribute("aria-pressed"),
    ).toBe("true");
    expect(screen.getByLabelText("Search Catalog")).toHaveProperty(
      "value",
      "unsent query",
    );
    expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
      body: { text: "submitted", cursor: "next-page" },
    });
    await waitFor(() => expect(scroll).toHaveBeenCalledWith(0, 417));
    scrollY.mockReturnValue(683);
    act(() => {
      history.replaceState({ catalogNavigation: 1 }, "", href);
      window.dispatchEvent(
        new PopStateEvent("popstate", { state: history.state as unknown }),
      );
    });
    await screen.findByRole("heading", { name: "External catalog record" });
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
    get.mockRejectedValueOnce(new APIError(404, "NOT_FOUND"));
    act(() => {
      history.replaceState({ catalogNavigation: 0 }, "", "/unit/catalog");
      window.dispatchEvent(
        new PopStateEvent("popstate", { state: history.state as unknown }),
      );
    });
    await screen.findByRole("button", { name: "External tool" });
    await waitFor(() => expect(scroll).toHaveBeenLastCalledWith(0, 683));
    act(() => {
      history.replaceState({ catalogNavigation: 1 }, "", href);
      window.dispatchEvent(
        new PopStateEvent("popstate", { state: history.state as unknown }),
      );
    });
    await screen.findByRole("alert");
    expect(screen.queryByRole("heading", { name: "External tool" })).toBeNull();
  },
);

it("selects local namespace from a card and resets continuation", async () => {
  const { props, post } = screenWorkspace();
  post.mockResolvedValue(
    reply({
      ...results,
      items: [
        {
          ...local,
          local: {
            ...local.local,
            resource: {
              ...local.local.resource,
              namespaceId: "resource-namespace",
            },
            listing: { ...listing, namespaceId: "listing-namespace" },
            display: record.display,
          },
        },
      ],
      nextCursor: "next",
    }),
  );
  render(<Catalog {...props} />);
  await screen.findByRole("button", {
    name: "Filter by namespace Catalog demo",
  });
  fireEvent.click(screen.getByRole("button", { name: /Next page/ }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  click("Filter by namespace Catalog demo");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(3));
  expect((post.mock.calls.at(-1)?.[1] as { body: unknown }).body).toEqual({
    text: "",
    limit: 24,
    namespaceId: "listing-namespace",
    origins: ["local"],
  });
  click("Remove local Namespace: Catalog demo");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(4));
  expect(
    (post.mock.calls.at(-1)?.[1] as { body: unknown }).body,
  ).not.toHaveProperty("namespaceId");
  click("Remove origin Local Catalog");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(5));
  expect(
    (post.mock.calls.at(-1)?.[1] as { body: unknown }).body,
  ).not.toHaveProperty("origins");
});

it("explains empty live-only search and keeps origin criteria when text is submitted", async () => {
  const { props, post } = screenWorkspace();
  post.mockResolvedValue(reply({ ...results, items: [], durableTotal: 0 }));
  render(<Catalog {...props} />);
  await screen.findByRole("heading", { name: "No resources found" });
  click("Filters");
  fireEvent.click(screen.getByRole("checkbox", { name: "Local Catalog" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "Indexed external" }));
  click("Apply filters");
  const guidance =
    "Enter search text to request live external results. Live results may change. Start a new search if a page can no longer be loaded.";
  expect(await screen.findByText(guidance)).toBeTruthy();
  expect(
    screen.getByText(/Search text may also be sent/).textContent,
  ).toContain("when local or synced results are included");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  expect((post.mock.calls.at(-1)?.[1] as { body: unknown }).body).toEqual({
    text: "",
    limit: 24,
    origins: ["live"],
  });
  change("Search Catalog", "live agent");
  click("Search");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(3));
  expect(screen.queryByText(guidance)).toBeNull();
  expect((post.mock.calls.at(-1)?.[1] as { body: unknown }).body).toEqual({
    text: "live agent",
    limit: 24,
    origins: ["live"],
  });
});

it("uses the full record Listing namespace and discards the previous page cursor", async () => {
  const { props, post, get } = screenWorkspace();
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  post.mockResolvedValue(reply({ ...results, nextCursor: "next" }));
  get.mockResolvedValue(reply(record));
  render(<Catalog {...props} />);
  await screen.findByRole("button", { name: "Local helper" });
  fireEvent.click(screen.getByRole("button", { name: /Next page/ }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  click("Local helper");
  await screen.findByRole("dialog");
  fireEvent.click(screen.getByRole("link", { name: /Open full record/ }));
  await screen.findByRole("link", { name: /Back to Catalog/ });
  click("Filter by namespace Catalog demo");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(3));
  expect(location.pathname).toBe("/unit/catalog");
  expect((post.mock.calls.at(-1)?.[1] as { body: unknown }).body).toEqual({
    text: "",
    limit: 24,
    namespaceId: listing.namespaceId,
    origins: ["local"],
  });
  expect(screen.getByRole("searchbox", { name: "Search Catalog" })).toBe(
    document.activeElement,
  );
});

it.each(["ctrlKey", "metaKey", "shiftKey", "altKey"])(
  "keeps %s record navigation native without changing the current search",
  async (modifier) => {
    const { props, post, get } = screenWorkspace();
    post.mockResolvedValue(reply(results));
    get.mockResolvedValue(reply(record));
    render(<Catalog {...props} />);
    await screen.findByRole("button", { name: "Local helper" });
    click("Local helper");
    await screen.findByRole("dialog");
    const link = screen.getByRole("link", { name: /Open full record/ });
    let intercepted: boolean | undefined;
    // Observe the owner capture handler, then suppress jsdom's native navigation.
    link.addEventListener(
      "click",
      (event) => {
        intercepted = event.defaultPrevented;
        event.preventDefault();
      },
      { once: true },
    );
    fireEvent.click(link, { [modifier]: true });
    expect(intercepted).toBe(false);
    expect(location.pathname).toBe("/unit/catalog");
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(post).toHaveBeenCalledOnce();
    expect(get).toHaveBeenCalledOnce();
  },
);

it("leaves release-history navigation native when viewing a Catalog record", async () => {
  const { props, post, get } = screenWorkspace();
  const scroll = vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  post.mockResolvedValue(reply(results));
  get.mockResolvedValue(reply(record));
  render(<Catalog {...props} />);
  await screen.findByRole("button", { name: "Local helper" });
  click("Local helper");
  await screen.findByRole("dialog");
  fireEvent.click(screen.getByRole("link", { name: /Open full record/ }));
  const link = await screen.findByRole("link", { name: "Release history" });
  let intercepted: boolean | undefined;
  link.addEventListener(
    "click",
    (event) => {
      intercepted = event.defaultPrevented;
      event.preventDefault();
    },
    { once: true },
  );
  scroll.mockClear();
  fireEvent.click(link);
  expect(scroll).not.toHaveBeenCalled();
  expect(intercepted).toBe(false);
  expect(link.getAttribute("href")).toBe(
    `/unit/listings/${listing.id}#release-history`,
  );
  expect(location.pathname).toBe(`/unit/catalog/${record.release.id}`);
});

it.each([
  {
    item: { ...local, local: { ...local.local, display: record.display } },
    labels: [
      "Agent",
      "Local Catalog",
      "published",
      "Demo publisher",
      "1.0.0",
      "public",
    ],
  },
  {
    item: {
      ...local,
      kind: "plugin",
      local: {
        ...local.local,
        release: { ...local.local.release, state: "deprecated" },
        listingRevision: { visibility: "private" },
      },
    },
    labels: [
      "Plugin",
      "Local Catalog",
      "deprecated",
      "publisher-1",
      "1.0.0",
      "private",
    ],
  },
  {
    item: { ...indexed, publisher: "External provider" },
    labels: [
      "MCP Server",
      "Indexed external",
      "External provider",
      "Empty version",
    ],
  },
  {
    item: live,
    labels: ["Skill", "Live external", "Not supplied", "No version supplied"],
  },
])(
  "keeps card facts explicit without inventing external lifecycle or visibility: $item.kind",
  async ({ item, labels }) => {
    const { props, post, get } = screenWorkspace();
    post.mockResolvedValue(reply({ ...results, items: [item] }));
    render(<Catalog {...props} />);
    await screen.findByRole("button", { name: item.displayName });
    const card = within(screen.getByRole("article"));
    expect(
      card.getByRole("heading", { level: 2, name: item.displayName }),
    ).toBeTruthy();
    for (const label of labels)
      expect(card.getByText(label, { exact: true })).toBeTruthy();
    expect(card.getByText("Version:", { exact: true })).toBeTruthy();
    if (item.type !== "local") {
      for (const label of [
        "published",
        "deprecated",
        "public",
        "private",
        "Catalog publisher",
      ])
        expect(card.queryByText(label, { exact: true })).toBeNull();
      expect(card.getByText("No description provided.")).toBeTruthy();
    }
    expect(get).not.toHaveBeenCalled();
    expect(post).toHaveBeenCalledOnce();
  },
);

it.each([1, 2])(
  "keeps available cards, exact live counts (%i) and the partial warning when an upstream has another page",
  async (liveCount) => {
    const { props, post } = screenWorkspace();
    const message =
      "The upstream Registry has more results; this bounded live query does not continue them.";
    const items = [
      local,
      indexed,
      ...Array.from({ length: liveCount }, (_, index) => ({
        ...live,
        id: `live-${index}`,
        displayName: index ? `Live skill ${index + 1}` : live.displayName,
      })),
    ];
    post.mockResolvedValue(
      reply({
        ...results,
        items,
        liveCount,
        diagnostics: [{ status: "continuation_unavailable", message }],
      }),
    );
    render(<Catalog {...props} />);
    await screen.findByRole("button", { name: "Local helper" });
    expect(screen.getByRole("button", { name: "External tool" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Live skill" })).toBeTruthy();
    const summary = screen.getByText(
      `2 local and synced matches · ${liveCount} live on this page`,
    ).parentElement!;
    expect(summary.getAttribute("role")).toBe("status");
    expect(summary.textContent).toContain(
      `2 local and synced matches · ${liveCount} live on this page`,
    );
    expect(
      screen.getByText(/Some source queries or results are incomplete/),
    ).toBeTruthy();
    fireEvent.click(screen.getByText("Search diagnostics"));
    expect(screen.getByText(message)).toBeTruthy();
    expect(
      screen.getByText(
        "An upstream Registry has more results; its next page was not followed.",
      ),
    ).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(props.onFailure).not.toHaveBeenCalled();
    expect(post).toHaveBeenCalledOnce();
  },
);

it("explains all partial outcomes, preserves results and gates attributed source links by role and scope", async () => {
  const { props, post, get } = screenWorkspace();
  const sourceId = "00000000-0000-7000-8000-000000000010";
  const diagnostics = [
    { status: "failed", sourceId },
    { status: "invalid_response" },
    { status: "insufficient_evidence" },
    { status: "policy_stale" },
    { status: "fanout_limited" },
    { status: "diagnostics_truncated" },
    { status: "live_results_limited" },
    { status: "continuation_unavailable" },
  ];
  post.mockResolvedValue(reply({ ...results, diagnostics }));
  const view = render(<Catalog {...props} />);
  await screen.findByRole("button", { name: "Local helper" });
  fireEvent.click(screen.getByText("Search diagnostics"));
  for (const text of [
    "Source request failed. This status alone does not identify the cause.",
    "A source returned a response that could not be used.",
    "Some results lacked the declarations required by the selected filters.",
    "Source policy evidence is not current. A Source Administrator can reinspect the source.",
    "Live source selection was limited or a duplicate source was skipped.",
    "Additional source diagnostics were omitted from this bounded response.",
    "Additional live results could not be safely continued. Start a new search.",
    "An upstream Registry has more results; its next page was not followed.",
  ])
    expect(screen.getByText(text, { exact: false })).toBeTruthy();
  expect(screen.getAllByText("No source ID supplied.")).toHaveLength(7);
  expect(screen.getByText(sourceId)).toBeTruthy();
  expect(screen.getByRole("button", { name: "Copy source ID" })).toBeTruthy();
  expect(screen.queryByRole("link", { name: "Open source" })).toBeNull();
  for (const access of [
    {
      ...props.access,
      ...testAccess(["reader" as const]),
      scopeEnforcement: true,
      scopes: ["sources.read" as const],
    },
    {
      ...props.access,
      ...testAccess(["source_admin" as const]),
      scopeEnforcement: true,
      scopes: [],
    },
    {
      ...props.access,
      ...testAccess(["tenant_admin" as const]),
      scopeEnforcement: true,
      scopes: [],
    },
  ]) {
    view.rerender(<Catalog {...props} access={access} />);
    expect(screen.queryByRole("link", { name: "Open source" })).toBeNull();
  }
  for (const roles of [["source_admin" as const], ["tenant_admin" as const]]) {
    view.rerender(
      <Catalog
        {...props}
        access={{
          ...props.access,
          ...testAccess(roles),
          scopeEnforcement: true,
          scopes: ["sources.read" as const],
        }}
      />,
    );
    expect(
      screen.getByRole("link", { name: "Open source" }).getAttribute("href"),
    ).toBe(`/unit/sources/${sourceId}`);
  }
  expect(get).not.toHaveBeenCalled();
  expect(post).toHaveBeenCalledOnce();
  click("Retry search");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  expect(post.mock.calls.at(-1)?.[1]).not.toHaveProperty("body.cursor");
});

it("explains live disclosure before submission and excludes live peers without submitting draft text", async () => {
  const { props, post } = screenWorkspace();
  post.mockResolvedValue(reply({ ...results, nextCursor: "page-2" }));
  render(<Catalog {...props} />);
  const search = await screen.findByRole("searchbox", {
    name: "Search Catalog",
  });
  expect(search.getAttribute("aria-describedby")).toBe("search-disclosure");
  expect(
    screen.getByText(/Live search may send your search text, tags and filters/),
  ).toBeTruthy();
  change("Search Catalog", "submitted");
  click("Search");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  click("Filter by tag tools");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(3));
  click(/Next page/);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(4));
  change("Search Catalog", "private draft");
  click("Exclude live results");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(5));
  expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
    body: { text: "submitted", tags: ["tools"], origins: ["local", "indexed"] },
  });
  expect(post.mock.calls.at(-1)?.[1]).not.toHaveProperty("body.cursor");
  expect(search).toHaveProperty("value", "private draft");
  expect(document.activeElement).toBe(search);
  expect(
    screen.getByText(/Live sources are excluded by the current filters/),
  ).toBeTruthy();
  expect(screen.getByText(/configured semantic search provider/)).toBeTruthy();
  expect(screen.queryByText("How results are counted")).toBeNull();
  click("Remove origin Local Catalog");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(6));
  expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
    body: { origins: ["indexed"], text: "submitted", tags: ["tools"] },
  });
  click("Remove origin Indexed external");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(7));
  expect(
    screen.getByRole("button", { name: "Exclude live results" }),
  ).toBeTruthy();
});

it("keeps exact publisher identity with duplicate names, external publishers, sparse labels and later pages", async () => {
  const { props, post, get } = screenWorkspace();
  const labelled = {
    ...local,
    local: { ...local.local, display: { publisherName: "Shared name" } },
  };
  post
    .mockResolvedValueOnce(
      reply({
        ...results,
        items: [
          labelled,
          { ...labelled, id: "duplicate", publisher: "publisher-2" },
          { ...labelled, id: "same-publisher" },
          { ...indexed, publisher: "external.test" },
          { ...local, id: "sparse", publisher: "publisher-3" },
          { ...indexed, id: "no-publisher" },
        ],
      }),
    )
    .mockResolvedValue(
      reply({ ...results, items: [indexed], nextCursor: "next-1" }),
    );
  render(<Catalog {...props} />);
  await screen.findAllByRole("button", { name: "Local helper" });
  click("Filters");
  const suggestions = document.querySelectorAll("button.publisher-choice");
  expect(suggestions).toHaveLength(4);
  expect(
    screen.getByRole("button", {
      name: "Shared name Local publisher · publisher-1",
    }),
  ).toBeTruthy();
  click("Shared name Local publisher · publisher-2");
  expect(
    screen.getByRole("button", {
      name: "external.test External AIR publisher",
    }),
  ).toBeTruthy();
  expect(
    screen.getByRole("button", { name: "publisher-3 Local publisher" }),
  ).toBeTruthy();
  click("Apply filters");
  await screen.findByRole("button", { name: "External tool" });
  expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
    body: { publisher: "publisher-2" },
  });
  click(/Next page/);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(3));
  expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
    body: { publisher: "publisher-2", cursor: "next-1" },
  });
  click("Filters");
  expect(
    screen.getByRole("textbox", { name: "Publisher identifier" }),
  ).toHaveProperty("value", "publisher-2");
  expect(document.querySelectorAll("button.publisher-choice")).toHaveLength(0);
  change("Publisher identifier", "external.test");
  click("Apply filters");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(4));
  expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
    body: { publisher: "external.test" },
  });
  expect(post.mock.calls.at(-1)?.[1]).not.toHaveProperty("body.cursor");
  expect(get).not.toHaveBeenCalled();
});

it("keeps the existing eight exact publisher suggestions ceiling", async () => {
  const { props, post } = screenWorkspace();
  post.mockResolvedValue(
    reply({
      ...results,
      items: [
        { ...local, id: "repeated-publisher", publisher: "publisher-0" },
        ...Array.from({ length: 10 }, (_, i) => ({
          ...local,
          id: `local-${i}`,
          publisher: `publisher-${i}`,
        })),
      ],
    }),
  );
  render(<Catalog {...props} />);
  await screen.findAllByRole("button", { name: "Local helper" });
  click("Filters");
  expect(document.querySelectorAll("button.publisher-choice")).toHaveLength(8);
  expect(
    screen.getByRole("button", { name: "publisher-7 Local publisher" }),
  ).toBeTruthy();
  expect(
    screen.queryByRole("button", { name: "publisher-8 Local publisher" }),
  ).toBeNull();
});

it.each(["preview", "detail"] as const)(
  "withholds %s handoff on malformed local assessment and recovers only after a valid read",
  async (mode) => {
    const { props, get, post } = screenWorkspace();
    get
      .mockResolvedValueOnce(
        reply({
          ...record,
          evidence:
            mode === "preview"
              ? undefined
              : { ...evidence, trust: { state: "approved" } },
        }),
      )
      .mockResolvedValue(reply(record));
    post.mockResolvedValue(reply(results));
    if (mode === "detail")
      history.replaceState(null, "", `/unit/catalog/${record.release.id}`);
    render(<Catalog {...props} />);
    if (mode === "preview") {
      await screen.findByRole("button", { name: "Local helper" });
      click("Local helper");
    }
    await screen.findByRole("alert");
    expect(
      screen.queryByRole("region", { name: "Resource handoff" }),
    ).toBeNull();
    expect(
      screen.queryByRole("region", { name: "Resource assessment" }),
    ).toBeNull();
    expect(props.onFailure).toHaveBeenCalledOnce();
    click("Retry record");
    await screen.findByRole("region", { name: "Resource assessment" });
    expect(
      screen.getByRole("region", { name: "Resource handoff" }),
    ).toBeTruthy();
    expect(get).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole("alert")).toBeNull();
  },
);

it("keeps an explicit external query visible after permission loss and sends no forbidden search", async () => {
  const { props, post, get } = screenWorkspace();
  post.mockResolvedValue(
    reply({ ...results, partial: false, diagnostics: [] }),
  );
  const view = render(<Catalog {...props} />);
  await screen.findByRole("button", { name: "Local helper" });
  click("Filters");
  fireEvent.click(screen.getByRole("checkbox", { name: "Local Catalog" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "Live external" }));
  click("Apply filters");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  expect(post.mock.calls[1]?.[1]).toMatchObject({
    body: { origins: ["indexed"] },
  });
  view.rerender(
    <Catalog
      {...props}
      access={{
        ...props.access,
        permissions: ["resource.read"],
        availablePermissions: ["resource.read"],
      }}
    />,
  );
  expect((await screen.findByRole("alert")).textContent).toContain(
    "federation.discover",
  );
  expect(
    screen.getByText(/Your current access includes local Catalog results only/),
  ).toBeTruthy();
  expect(
    screen.queryByText(/Live sources are excluded by the current filters/),
  ).toBeNull();
  expect(post).toHaveBeenCalledTimes(2);
  expect(get).not.toHaveBeenCalled();
  click(/Filters/);
  expect(
    screen.getByRole("checkbox", { name: "Indexed external" }),
  ).toHaveProperty("checked", true);
  click("Clear filters");
  click("Apply filters");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(3));
  expect(post.mock.calls[2]?.[1]).not.toHaveProperty("body.origins");
});

it.each(["é".repeat(251), "🙂".repeat(126), "a\0b"])(
  "keeps invalid search text without issuing a request",
  async (value) => {
    const { props, post } = screenWorkspace();
    post.mockResolvedValue(reply(results));
    render(<Catalog {...props} />);
    await screen.findByRole("button", { name: "Local helper" });
    change("Search Catalog", value);
    click("Search");
    await screen.findByRole("alert");
    expect(post).toHaveBeenCalledOnce();
    expect(
      screen.getByRole("searchbox", { name: "Search Catalog" }),
    ).toHaveProperty("value", value);
    expect(document.activeElement).toBe(
      screen.getByRole("searchbox", { name: "Search Catalog" }),
    );
    change("Search Catalog", "🙂".repeat(125));
    click("Search");
    await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
    expect(post.mock.calls[1]?.[1]).toMatchObject({
      body: { text: "🙂".repeat(125) },
    });
  },
);
it("refreshes live artwork proof explicitly while preserving an administrator override draft", async () => {
  const w = screenWorkspace();
  const current = {
    ...live,
    live: {
      ...live.live,
      sourceId: "00000000-0000-7000-8000-000000000010",
      presentationGeneration: 2,
    },
    artwork: { receipt: "old-proof" },
  };
  w.post.mockResolvedValue(reply({ ...results, items: [current] }));
  w.get.mockResolvedValue({
    error: {},
    response: new Response(null, { status: 404 }),
  });
  render(<Catalog {...w.props} access={administrator} />);
  await screen.findByRole("button", { name: "Live skill" });
  click("Live skill");
  click("Manage local artwork");
  await screen.findByLabelText("Artwork override reason");
  change("Artwork override reason", "Preserve my reason");
  w.post.mockResolvedValue(
    reply({
      ...results,
      items: [{ ...current, artwork: { receipt: "new-proof" } }],
    }),
  );
  click("Refresh live artwork");
  await screen.findByText("Live artwork refreshed.");
  expect(screen.getByLabelText("Artwork override reason")).toHaveProperty(
    "value",
    "Preserve my reason",
  );
  expect(w.post).toHaveBeenCalledTimes(2);
  w.post.mockResolvedValue(reply({ ...results, items: [] }));
  click("Refresh live artwork");
  await screen.findByText(/This source entry is no longer/);
  expect(screen.getByLabelText("Artwork override reason")).toHaveProperty(
    "value",
    "Preserve my reason",
  );
  w.post.mockRejectedValue(new Error("Transient search failure"));
  click("Refresh live artwork");
  await screen.findByText(
    "Could not refresh live artwork. Try again or start a new search.",
  );
  expect(w.props.onFailure).toHaveBeenCalled();
});
it.each(["pending", "failed", "completed"])(
  "refreshes a retained result's rating from its displayed page while continuation is %s",
  async (outcome) => {
    const w = screenWorkspace();
    const current = {
      ...live,
      live: {
        ...live.live,
        sourceId: "00000000-0000-7000-8000-000000000010",
        presentationGeneration: 2,
      },
      ratings: { count: 0, receipt: "current-proof" },
    };
    const page = { ...results, items: [current], nextCursor: "next-live-page" };
    const pending = deferred<ReturnType<typeof reply>>();
    let searches = 0;
    w.post.mockImplementation((path) => {
      if (path === "/api/v1/ratings/read")
        return Promise.resolve(
          reply({
            target: {
              origin: "foreign",
              sourceId: current.live.sourceId,
              presentationGeneration: 2,
              identifier: current.identifier,
              kind: current.kind,
              versionPresent: false,
            },
            count: 0,
            changedEntry: false,
          }),
        );
      searches++;
      return searches === 2 ? pending.promise : Promise.resolve(reply(page));
    });
    render(<Catalog {...w.props} />);
    await screen.findByRole("button", { name: "Live skill" });
    const captured = (w.post.mock.calls[0]?.[1] as { body: unknown }).body;
    if (outcome === "failed") click("Table");
    click(/Next page/);
    await waitFor(() => expect(searches).toBe(2));
    if (outcome === "failed") {
      await act(async () =>
        pending.reject(new APIError(503, "DEPENDENCY_UNAVAILABLE")),
      );
      await screen.findByRole("alert");
    }
    click("Live skill");
    click("Ratings for this version");
    await screen.findByRole("radio", { name: "3 stars" });
    fireEvent.click(screen.getByRole("radio", { name: "3 stars" }));
    fireEvent.click(screen.getByRole("checkbox", { name: /Allow my score/ }));
    if (outcome === "completed") {
      await act(async () =>
        pending.resolve(reply({ ...page, items: [], nextCursor: undefined })),
      );
      expect(screen.getByRole("dialog")).toBeTruthy();
    }
    click("Refresh live ratings");
    await screen.findByText(/Live ratings refreshed/);
    expect(
      w.post.mock.calls
        .filter(([path]) => path === "/api/v1/catalog/search")
        .at(-1)?.[1],
    ).toEqual({ body: captured, signal: expect.any(AbortSignal) as unknown });
    expect(screen.getByRole("radio", { name: "3 stars" })).toHaveProperty(
      "checked",
      true,
    );
    expect(
      screen.getByRole("checkbox", { name: /Allow my score/ }),
    ).toHaveProperty("checked", true);
    expect(w.post.mock.calls.some(([path]) => path === "/api/v1/ratings")).toBe(
      false,
    );
  },
);

it.each(["reply", "failure"])(
  "aborts explicit live proof refresh on preview dismissal and ignores its late %s",
  async (outcome) => {
    const w = screenWorkspace();
    const attributed = {
      ...live,
      live: {
        ...live.live,
        sourceId: "00000000-0000-7000-8000-000000000194",
        presentationGeneration: 1,
      },
    };
    w.post.mockResolvedValue(reply({ ...results, items: [attributed] }));
    render(<Catalog {...w.props} />);
    await screen.findByRole("button", { name: "Live skill" });
    click("Live skill");
    const pending = deferred<ReturnType<typeof reply>>();
    w.post.mockReturnValue(pending.promise);
    click("Refresh live artwork");
    const signal = (w.post.mock.calls.at(-1)?.[1] as { signal: AbortSignal })
      .signal;
    click("Close preview");
    expect(signal.aborted).toBe(true);
    await act(async () => {
      if (outcome === "failure")
        pending.reject(new Error("Late proof transport failure"));
      else pending.resolve(reply({ ...results, items: [] }));
    });
    expect(screen.queryByText(/Your draft is preserved/)).toBeNull();
    expect(w.props.onFailure).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).toBeNull();
  },
);
it.each([false, true])(
  "refreshes the exact live rating target from its captured page (continuation: %s)",
  async (continuation) => {
    const w = screenWorkspace();
    const current = {
      ...live,
      versionPresent: true,
      version: "1.0.0",
      live: {
        ...live.live,
        sourceId: "00000000-0000-7000-8000-000000000010",
        presentationGeneration: 2,
      },
      ratings: { count: 0, receipt: "old-rating-proof" },
    };
    w.post.mockImplementation(async (path) =>
      path === "/api/v1/ratings/read"
        ? reply({
            target: {
              origin: "foreign",
              sourceId: current.live.sourceId,
              presentationGeneration: 2,
              identifier: current.identifier,
              kind: current.kind,
              versionPresent: true,
              version: "1.0.0",
            },
            count: 0,
            changedEntry: false,
          })
        : reply({ ...results, items: [current], nextCursor: "next-live-page" }),
    );
    render(<Catalog {...w.props} />);
    await screen.findByRole("button", { name: "Live skill" });
    change("Search Catalog", "live skill");
    click("Search");
    await waitFor(() => expect(w.post).toHaveBeenCalledTimes(2));
    click("Skills");
    await screen.findByRole("button", { name: "Remove kind Skill" });
    click("Filters");
    change("Add tag", "design, review");
    click("Apply filters");
    await screen.findByRole("button", { name: "Remove tag design, review" });
    if (continuation) {
      click(/Next page/);
      await waitFor(() =>
        expect(w.post.mock.calls.at(-1)?.[1]).toHaveProperty(
          "body.cursor",
          "next-live-page",
        ),
      );
    }
    const captured = (w.post.mock.calls.at(-1)?.[1] as { body: unknown }).body;
    click("Live skill");
    click("Ratings for this version");
    await screen.findByRole("radio", { name: "3 stars" });
    fireEvent.click(screen.getByRole("radio", { name: "3 stars" }));
    fireEvent.click(screen.getByRole("checkbox", { name: /Allow my score/ }));
    const changed = {
      ...current,
      live: {
        ...current.live,
        entry: { ...current.live.entry, displayName: "Changed metadata" },
      },
      ratings: { count: 0, receipt: "fresh-rating-proof" },
    };
    w.post.mockImplementation(async (path) =>
      path === "/api/v1/ratings/read"
        ? reply({
            target: {
              origin: "foreign",
              sourceId: current.live.sourceId,
              presentationGeneration: 2,
              identifier: current.identifier,
              kind: current.kind,
              versionPresent: true,
              version: "1.0.0",
            },
            count: 0,
            changedEntry: false,
          })
        : reply({
            ...results,
            items: [
              local,
              {
                ...changed,
                live: {
                  ...changed.live,
                  sourceId: "00000000-0000-7000-8000-000000000011",
                },
              },
              { ...changed, identifier: "urn:air:live.test:skills:other" },
              { ...changed, versionPresent: false, version: undefined },
              { ...changed, version: "other" },
              { ...changed, kind: "agent" },
              {
                ...changed,
                live: { ...changed.live, presentationGeneration: 3 },
              },
              changed,
            ],
          }),
    );
    click("Refresh live ratings");
    await waitFor(() =>
      expect(
        w.post.mock.calls.some(
          ([, options]) =>
            !!options &&
            typeof options === "object" &&
            "body" in options &&
            !!options.body &&
            typeof options.body === "object" &&
            "receipt" in options.body &&
            options.body.receipt === "fresh-rating-proof",
        ),
      ).toBe(true),
    );
    expect(
      w.post.mock.calls
        .filter(([path]) => path === "/api/v1/catalog/search")
        .at(-1)?.[1],
    ).toEqual({ body: captured, signal: expect.any(AbortSignal) as unknown });
    expect(screen.getByRole("radio", { name: "3 stars" })).toHaveProperty(
      "checked",
      true,
    );
    w.post.mockResolvedValue(reply({ ...results, items: [] }));
    click("Refresh live ratings");
    await screen.findByText(
      /This target is no longer on the captured result page/,
    );
    expect(w.post.mock.calls.at(-1)?.[1]).toEqual({
      body: captured,
      signal: expect.any(AbortSignal) as unknown,
    });
    expect(
      screen.getByRole("checkbox", { name: /Allow my score/ }),
    ).toHaveProperty("checked", true);
    w.post.mockRejectedValue(new APIError(400, "INVALID_ARGUMENT"));
    click("Refresh live ratings");
    await screen.findByText(/Start a new search if the result window expired/);
    expect(w.post.mock.calls.at(-1)?.[1]).toEqual({
      body: captured,
      signal: expect.any(AbortSignal) as unknown,
    });
    expect(screen.getByRole("radio", { name: "3 stars" })).toHaveProperty(
      "checked",
      true,
    );
    expect(
      screen.getByRole("checkbox", { name: /Allow my score/ }),
    ).toHaveProperty("checked", true);
    expect(w.post.mock.calls.some(([path]) => path === "/api/v1/ratings")).toBe(
      false,
    );
  },
);

it.each(["reply", "failure"])(
  "aborts live rating proof refresh when its preview closes and ignores a late %s",
  async (outcome) => {
    const w = screenWorkspace();
    const current = {
      ...live,
      live: {
        ...live.live,
        sourceId: "00000000-0000-7000-8000-000000000010",
        presentationGeneration: 2,
      },
      ratings: { count: 0, receipt: "old-rating-proof" },
    };
    w.post.mockImplementation(async (path) =>
      path === "/api/v1/ratings/read"
        ? reply({
            target: {
              origin: "foreign",
              sourceId: current.live.sourceId,
              presentationGeneration: 2,
              identifier: current.identifier,
              kind: current.kind,
              versionPresent: false,
            },
            count: 0,
            changedEntry: false,
          })
        : reply({ ...results, items: [current] }),
    );
    render(<Catalog {...w.props} />);
    await screen.findByRole("button", { name: "Live skill" });
    click("Live skill");
    click("Ratings for this version");
    await screen.findByRole("radio", { name: "3 stars" });
    const pending = deferred<ReturnType<typeof reply>>();
    w.post.mockReturnValue(pending.promise);
    click("Refresh live ratings");
    const signal = (w.post.mock.calls.at(-1)?.[1] as { signal: AbortSignal })
      .signal;
    click("Close preview");
    expect(signal.aborted).toBe(true);
    await act(async () => {
      if (outcome === "failure")
        pending.reject(new Error("Late rating proof transport failure"));
      else pending.resolve(reply({ ...results, items: [current] }));
    });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(w.props.onFailure).not.toHaveBeenCalled();
    expect(screen.queryByText(/Your opinion draft is preserved/)).toBeNull();
  },
);

it.each(["", "applied text"])(
  "transfers only applied Explore criteria to local Catalog (%s)",
  async (applied) => {
    const { props, post, get } = screenWorkspace();
    history.replaceState(null, "", "/unit/explore");
    vi.spyOn(window, "scrollTo").mockImplementation(() => {});
    get.mockResolvedValue(reply({ items: [] }));
    post.mockResolvedValue(reply(results));
    render(<Catalog {...props} access={administrator} />);
    fireEvent.click(await screen.findByRole("button", { name: "View counts" }));
    await screen.findByText("0 matching local releases");
    change("Search local metadata", applied);
    click("Apply filters");
    change("Search local metadata", "unsubmitted draft");
    click("Search local Catalog");
    await screen.findByRole("button", { name: "Local helper" });
    expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
      body: {
        ...(applied ? { text: applied } : {}),
        origins: ["local"],
        limit: 24,
      },
    });
    expect(screen.getByLabelText("Search Catalog")).toHaveProperty(
      "value",
      applied,
    );
    expect(location.pathname).toBe("/unit/catalog");
    expect(JSON.stringify(history.state)).not.toMatch(/applied|draft/);
  },
);

it("shows exact Listing categories and applies them without submitting pending text", async () => {
  const w = screenWorkspace();
  const category = "Finance, Résumé 🧭";
  w.post.mockResolvedValue(reply(results));
  w.get.mockResolvedValue(
    reply({
      ...record,
      listingRevision: {
        ...record.listingRevision,
        categories: [category, ""],
      },
    }),
  );
  render(<Catalog {...w.props} />);
  await screen.findByRole("button", { name: "Local helper" });
  change("Search Catalog", "unsubmitted draft");
  click("Local helper");
  await screen.findByRole("button", {
    name: `Filter by category ${category}`,
  });
  expect(
    screen.getByText("Empty category declaration; no filter available."),
  ).toBeTruthy();
  click(`Filter by category ${category}`);
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(2));
  expect(w.post.mock.calls.at(-1)).toMatchObject([
    "/api/v1/catalog/search",
    { body: { text: "", limit: 24, categories: [category] } },
  ]);
  expect(
    screen.getByRole("searchbox", { name: "Search Catalog" }),
  ).toHaveProperty("value", "unsubmitted draft");
  expect(document.activeElement).toBe(
    screen.getByRole("searchbox", { name: "Search Catalog" }),
  );
});

it.each([true, false])(
  "refreshes an exact present-empty live rating version across omitted/empty wire values (omit-first=%s)",
  async (omitFirst) => {
    const w = screenWorkspace();
    const first = {
      ...live,
      versionPresent: true,
      ...(omitFirst ? {} : { version: "" }),
      live: {
        ...live.live,
        sourceId: "00000000-0000-7000-8000-000000000194",
        presentationGeneration: 1,
      },
      ratings: { count: 0, receipt: "original" },
    };
    const next = { ...first, ratings: { count: 0, receipt: "fresh" } };
    if (omitFirst) Object.assign(next, { version: "" });
    else delete next.version;
    w.post.mockImplementation(async (path) =>
      path === "/api/v1/ratings/read"
        ? reply({
            target: {
              origin: "foreign",
              sourceId: first.live.sourceId,
              presentationGeneration: 1,
              identifier: first.identifier,
              kind: first.kind,
              versionPresent: true,
              version: "",
            },
            count: 0,
            changedEntry: false,
          })
        : reply({ ...results, items: [first] }),
    );
    render(<Catalog {...w.props} />);
    await screen.findByRole("button", { name: "Live skill" });
    click("Live skill");
    click("Ratings for this version");
    await within(
      screen.getByRole("region", { name: "Ratings for this version" }),
    ).findByText(/No ratings/);
    w.post.mockImplementation(async (path) =>
      path === "/api/v1/catalog/search"
        ? reply({ ...results, items: [next] })
        : reply({
            target: {
              origin: "foreign",
              sourceId: first.live.sourceId,
              presentationGeneration: 1,
              identifier: first.identifier,
              kind: first.kind,
              versionPresent: true,
              version: "",
            },
            count: 0,
            changedEntry: false,
          }),
    );
    click("Refresh live ratings");
    await screen.findByText("Live ratings refreshed.");
    expect(
      w.post.mock.calls.filter(([path]) => path === "/api/v1/catalog/search"),
    ).toHaveLength(2);
  },
);

it("never replaces an unbound live artwork result with another unbound entry", async () => {
  const w = screenWorkspace();
  const current = {
    ...live,
    id: "live:unbound-reader",
    score: 1,
    live: { ...live.live, sourceId: "00000000-0000-7000-8000-000000000194" },
    artwork: { receipt: "old-proof" },
  };
  w.post
    .mockResolvedValueOnce(reply({ ...results, items: [current] }))
    .mockResolvedValue(
      reply({
        ...results,
        items: [
          {
            ...current,
            identifier: "urn:air:another.test:skills:other",
            displayName: "Another entry",
            live: {
              ...current.live,
              sourceId: "00000000-0000-7000-8000-000000000195",
            },
            artwork: { receipt: "wrong-proof" },
          },
        ],
      }),
    );
  render(<Catalog {...w.props} />);
  await screen.findByRole("button", { name: "Live skill" });
  click("Live skill");
  await act(async () => click("Refresh live artwork"));
  await screen.findByText(
    "Exact source and generation are unavailable. Search again before refreshing artwork.",
  );
  expect(
    within(screen.getByRole("dialog")).getByRole("heading", {
      name: "Live skill",
    }),
  ).toBeTruthy();
  expect(
    screen.getByRole("button", { name: "Refresh live artwork" }),
  ).toHaveProperty("disabled", true);
  expect(w.post).toHaveBeenCalledOnce();
});

it("sorts through the API, resets paging, preserves drafts and restores default ordering", async () => {
  const { props, post } = screenWorkspace();
  post.mockResolvedValue(reply({ ...results, nextCursor: "next-page" }));
  render(<Catalog {...props} />);
  await screen.findByRole("button", { name: "Local helper" });
  change("Search Catalog", "unsubmitted draft");
  click(/Next page/);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  click("Table");
  click("Sort by kind, ascending");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(3));
  expect(post.mock.calls[2]?.[1]).toMatchObject({
    body: { order: "kind_asc", text: "", limit: 24 },
  });
  expect(post.mock.calls[2]?.[1]).not.toHaveProperty("body.cursor");
  expect(
    screen.getByRole("searchbox", { name: "Search Catalog" }),
  ).toHaveProperty("value", "unsubmitted draft");
  await waitFor(() =>
    expect(
      screen
        .getByRole("columnheader", { name: /Kind/ })
        .getAttribute("aria-sort"),
    ).toBe("ascending"),
  );
  click("Sort by kind, descending");
  await waitFor(() => expect(post).toHaveBeenCalledTimes(4));
  expect(post.mock.calls[3]?.[1]).toHaveProperty("body.order", "kind_desc");
  fireEvent.change(screen.getByRole("combobox", { name: "Sort results" }), {
    target: { value: "" },
  });
  await waitFor(() => expect(post).toHaveBeenCalledTimes(5));
  expect(post.mock.calls[4]?.[1]).not.toHaveProperty("body.order");
  expect(screen.getByRole("columnheader", { name: "Status" })).toBeTruthy();
  expect(screen.getAllByText("Not reported")).toHaveLength(2);
});

it("clears the previous result order when criteria change and retries failed sorting explicitly", async () => {
  const w = screenWorkspace();
  const pending = deferred<ReturnType<typeof reply>>();
  w.post.mockResolvedValue(reply(results));
  render(<Catalog {...w.props} />);
  await screen.findByRole("button", { name: "Local helper" });
  click("Table");
  w.post.mockReturnValueOnce(pending.promise);
  click("Sort by name, descending");
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(2));
  expect(screen.queryByRole("columnheader", { name: "Name" })).toBeNull();
  await act(async () => pending.reject(new Error("unavailable")));
  expect(await screen.findByRole("alert")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Local helper" })).toBeNull();
  expect(screen.getByRole("combobox", { name: "Sort results" })).toHaveProperty(
    "value",
    "name_desc",
  );
  click("Start a new search");
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(3));
  expect(w.post.mock.calls[2]?.[1]).toHaveProperty("body.order", "name_desc");
  await waitFor(() =>
    expect(
      screen
        .getByRole("columnheader", { name: "Name" })
        .getAttribute("aria-sort"),
    ).toBe("descending"),
  );
});
