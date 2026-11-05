import type { components } from "../api/schema";
import { deferred } from "../../tests/unit/fixtures";
// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import {
  render,
  screen,
  waitFor,
  fireEvent,
  act,
} from "@testing-library/react";
import {
  MyWork,
  PersonalLibrary,
  SavePersonal,
  SaveResource,
  SaveSearch,
} from "./Personal";
import Catalog from "./Catalog";
import { PersonalSummary } from "./PersonalSummary";
import * as checks from "./responses";
import {
  screenLifecycle,
  screenWorkspace,
  readReplies,
  etagReply,
  stamp,
  change,
  confirmCommand,
} from "../../tests/unit/screens";

screenLifecycle("/unit/saved-items");
it.each([
  ["name_asc", "Name (A–Z)"],
  ["name_desc", "Name (Z–A)"],
  ["kind_asc", "Kind (A–Z)"],
  ["kind_desc", "Kind (Z–A)"],
  ["publisher_asc", "Publisher (A–Z)"],
  ["publisher_desc", "Publisher (Z–A)"],
] as const)("labels saved search order %s", (order, label) => {
  render(
    <PersonalSummary
      input={{ type: "search", label: "Ordered resources", search: { order } }}
    />,
  );
  expect(screen.getByText("Sort by").nextElementSibling?.textContent).toBe(
    label,
  );
  expect(screen.queryByText(order)).toBeNull();
});
const item = {
  id: "saved-1",
  type: "search" as const,
  label: "Research helpers",
  search: { text: "private-sentinel", origins: ["local"] as "local"[] },
  revision: 1,
  createdAt: stamp,
  updatedAt: stamp,
};
it("checks exact identities, presence and link-bearing fields", () => {
  expect(checks.PersonalItem(item)).toBe(true);
  expect(
    checks.PersonalInput({
      ...item,
      search: { text: "private", cursor: "unsafe" },
    }),
  ).toBe(false);
  expect(checks.PersonalItem({ ...item, id: "../escape" })).toBe(false);
  expect(checks.PersonalItem({ ...item, label: "\n" })).toBe(false);
  expect(checks.PersonalReceipt({ id: "id", revision: 1 })).toBe(true);
  expect(checks.PersonalReceipt({ id: "id", revision: 0 })).toBe(false);
  const foreign = {
    origin: "foreign",
    kind: "agent",
    sourceId: "source-1",
    presentationGeneration: 2,
    identifier: "urn:air:example.com:default:agent",
    versionPresent: false,
  };
  expect(checks.PersonalReference(foreign)).toBe(true);
  expect(checks.PersonalReference({ ...foreign, version: "1" })).toBe(false);
  expect(
    checks.PersonalReference({ ...foreign, versionPresent: true, version: "" }),
  ).toBe(true);
  expect(checks.PersonalResolution({ status: "unavailable" })).toBe(true);
  expect(
    checks.PersonalResolution({ status: "unavailable", reference: foreign }),
  ).toBe(false);
  expect(checks.PersonalResolution({ status: "available" })).toBe(false);
});
it("lists bounded private items without executing a named search", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/personal/items": { items: [item], nextCursor: "next" },
  });
  render(<PersonalLibrary {...w.props} />);
  expect(
    (await screen.findByRole("link", { name: item.label })).getAttribute(
      "href",
    ),
  ).toBe("/unit/saved-items/saved-1");
  expect(w.get).toHaveBeenCalledWith(
    "/api/v1/personal/items",
    expect.objectContaining({ params: { query: { limit: 25 } } }),
  );
  expect(w.post).not.toHaveBeenCalled();
});
it("saves a private query only after confirmation and accepts a receipt without echoing text", async () => {
  const w = screenWorkspace();
  w.post.mockResolvedValue(etagReply({ id: "created-1", revision: 1 }));
  render(
    <SavePersonal
      {...w.props}
      basePath="/unit"
      initial={{
        type: "search",
        label: "",
        search: { text: "private-sentinel" },
      }}
    />,
  );
  change("Saved label", "Research");
  expect(w.post).not.toHaveBeenCalled();
  await confirmCommand("Save named search");
  await screen.findByText("Private item saved.");
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/personal/items",
    expect.objectContaining({
      body: {
        type: "search",
        label: "Research",
        search: { text: "private-sentinel" },
      },
      params: { header: { "Idempotency-Key": expect.any(String) as unknown } },
    }),
  );
  expect(history.state).toBeNull();
});
it("binds the rename to the loaded identity and revision", async () => {
  const w = screenWorkspace();
  readReplies(w.get, { "/api/v1/personal/items/{itemId}": item });
  w.patch.mockResolvedValue(etagReply({ id: item.id, revision: 2 }));
  render(<PersonalLibrary {...w.props} id={item.id} />);
  await screen.findByRole("heading", { name: item.label });
  change("Saved label", "Renamed");
  await confirmCommand("Update saved item");
  await screen.findByText("Private item saved.");
  expect(w.patch).toHaveBeenCalledWith(
    "/api/v1/personal/items/{itemId}",
    expect.objectContaining({
      params: {
        path: { itemId: item.id },
        header: {
          "If-Match": '"revision-1"',
          "Idempotency-Key": expect.any(String) as unknown,
        },
      },
    }),
  );
});
it("keeps saved query text out of category requests before provider consent", async () => {
  const w = screenWorkspace();
  w.post.mockResolvedValue(
    etagReply({ matched: 0, facets: { category: { buckets: [] } } }),
  );
  render(
    <Catalog
      {...w.props}
      stagedSearch={{ text: "private-sentinel", origins: ["local"] }}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Categories" }));
  await act(async () => {});
  expect(w.post).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "Add category" })).toBeTruthy();
});
it("loads saved-query suggestions only on a consented panel action and rejects revoked responses", async () => {
  const w = screenWorkspace();
  const pending = deferred<ReturnType<typeof etagReply>>();
  w.post.mockReturnValueOnce(pending.promise).mockResolvedValue(
    etagReply({
      matched: 1,
      facets: { category: { buckets: [{ value: "current", count: 1 }] } },
    }),
  );
  render(
    <Catalog
      {...w.props}
      stagedSearch={{ text: "applied saved text", origins: ["local"] }}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Categories" }));
  const consent = screen.getByRole("checkbox", {
    name: /configured embedding provider/,
  });
  fireEvent.click(consent);
  expect(w.post).not.toHaveBeenCalled();
  fireEvent.change(screen.getByRole("searchbox", { name: "Search Catalog" }), {
    target: { value: "unsubmitted private text" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Categories" }));
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(1));
  expect(w.post.mock.calls[0]).toMatchObject([
    "/api/v1/catalog/explore",
    { body: { query: { text: "applied saved text" } } },
  ]);
  fireEvent.click(consent);
  expect(
    (w.post.mock.calls[0]![1] as { signal: AbortSignal }).signal.aborted,
  ).toBe(true);
  await act(async () =>
    pending.resolve(
      etagReply({
        matched: 1,
        facets: { category: { buckets: [{ value: "obsolete", count: 1 }] } },
      }),
    ),
  );
  expect(screen.queryByRole("checkbox", { name: "obsolete" })).toBeNull();
  fireEvent.click(consent);
  expect(w.post).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("button", { name: "Categories" }));
  await screen.findByRole("checkbox", { name: "current" });
  expect(
    w.post.mock.calls.every(([path]) => path === "/api/v1/catalog/explore"),
  ).toBe(true);
});

it("does not transfer consent or armed Search to a replacement identity", async () => {
  const w = screenWorkspace();
  w.post.mockResolvedValue(
    etagReply({
      tier: "core",
      items: [],
      durableTotal: 0,
      liveCount: 0,
      partial: false,
    }),
  );
  const stagedSearch = { text: "saved", origins: ["local" as const] };
  const view = render(<Catalog {...w.props} stagedSearch={stagedSearch} />);
  fireEvent.click(
    screen.getByRole("checkbox", { name: /configured embedding provider/ }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Search" }));
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(1));
  view.rerender(
    <Catalog
      {...w.props}
      access={{
        ...w.props.access,
        subject: "replacement",
        tenantId: "other-tenant",
      }}
      stagedSearch={stagedSearch}
    />,
  );
  expect(
    screen.getByRole("checkbox", { name: /configured embedding provider/ }),
  ).toHaveProperty("checked", false);
  fireEvent.click(screen.getByRole("button", { name: "Categories" }));
  await act(async () => {});
  expect(w.post).toHaveBeenCalledTimes(1);
});
it("opens saved criteria without search or disclosure until explicit consent and Search", async () => {
  const w = screenWorkspace();
  w.post.mockResolvedValue(
    etagReply({ matched: 0, facets: { category: { buckets: [] } } }),
  );
  render(
    <Catalog
      {...w.props}
      stagedSearch={{ text: "private-sentinel", origins: ["local"] }}
    />,
  );
  expect(screen.getByRole("searchbox")).toHaveProperty(
    "value",
    "private-sentinel",
  );
  await waitFor(() => expect(screen.queryByText("Loading...")).toBeNull());
  expect(w.post).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Search" }));
  expect(w.post).not.toHaveBeenCalled();
  fireEvent.click(
    screen.getByRole("checkbox", { name: /configured embedding provider/ }),
  );
  fireEvent.change(screen.getByRole("searchbox"), {
    target: { value: "🙂".repeat(126) },
  });
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
  });
  expect(w.post).not.toHaveBeenCalled();
  expect(screen.getByRole("alert").textContent).toContain("500");
  fireEvent.change(screen.getByRole("searchbox"), {
    target: { value: "private-sentinel" },
  });
  w.post.mockResolvedValue(
    etagReply({ items: [], durableTotal: 0, diagnostics: [], liveTotal: 0 }),
  );
  fireEvent.click(screen.getByRole("button", { name: "Search" }));
  await waitFor(() =>
    expect(w.post).toHaveBeenCalledWith(
      "/api/v1/catalog/search",
      expect.objectContaining({
        body: { text: "private-sentinel", origins: ["local"], limit: 24 },
      }),
    ),
  );
  expect(location.search).toBe("");
  expect(JSON.stringify(history.state)).not.toContain("private-sentinel");
});
it("pages My work separately with exact onward links and no assignment claim", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/me/work": {
      items: [
        {
          id: "resource:r1",
          type: "resource",
          state: "empty_resource",
          resourceId: "r1",
          namespaceId: "ns-1",
          kind: "agent",
          canonicalIdentifier: "urn:air:example.com:default:agent",
        },
      ],
    },
  });
  render(<MyWork {...w.props} />);
  expect(
    (await screen.findByRole("link", { name: "Open resource" })).getAttribute(
      "href",
    ),
  ).toBe("/unit/resources/r1");
  change("Work queue", "approved");
  await waitFor(() =>
    expect(w.get).toHaveBeenLastCalledWith(
      "/api/v1/me/work",
      expect.objectContaining({
        params: { query: { queue: "approved", limit: 25 } },
      }),
    ),
  );
});
it("does not offer saves for service principals or missing personal.write", () => {
  const w = screenWorkspace();
  const access = {
    ...w.props.access,
    scopeEnforcement: true,
    scopes: ["catalog.read"] as "catalog.read"[],
  };
  const v = render(
    <SaveResource
      {...w.props}
      access={access}
      basePath="/unit"
      label="A release"
      reference={{
        origin: "local",
        releaseId: "release",
        versionRevisionId: "revision",
      }}
    />,
  );
  expect(v.container.innerHTML).toBe("");
});

it("opens a new named search from the routed library editor", () => {
  const w = screenWorkspace();
  render(<PersonalLibrary {...w.props} id="new" />);
  expect(screen.getByLabelText("Saved search text")).toHaveProperty(
    "value",
    "",
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Edit private saved criteria as JSON" }),
  );
  expect(screen.getByLabelText("Private saved criteria JSON")).toHaveProperty(
    "value",
    '{\n  "text": ""\n}',
  );
});
it("opens saved criteria through the private detail without executing Search", async () => {
  const w = screenWorkspace();
  readReplies(w.get, { "/api/v1/personal/items/{itemId}": item });
  const onOpenSearch = vi.fn();
  render(
    <PersonalLibrary {...w.props} id={item.id} onOpenSearch={onOpenSearch} />,
  );
  await screen.findByRole("heading", { name: item.label });
  fireEvent.click(screen.getByRole("button", { name: "Open search editor" }));
  expect(onOpenSearch).toHaveBeenCalledWith(item.search);
  expect(w.post).not.toHaveBeenCalled();
});
it("changes the library filter, follows a cursor and refreshes explicitly", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/personal/items": { items: [], nextCursor: "next" },
  });
  render(<PersonalLibrary {...w.props} />);
  await screen.findByText(
    "Your library is empty. Save a resource from Catalog or create a named search.",
  );
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  await waitFor(() =>
    expect(w.get).toHaveBeenLastCalledWith(
      "/api/v1/personal/items",
      expect.objectContaining({
        params: { query: { limit: 25, cursor: "next" } },
      }),
    ),
  );
  fireEvent.click(screen.getByRole("button", { name: "Refresh saved items" }));
  await waitFor(() => expect(w.get.mock.calls.length).toBe(3));
  change("Saved item type", "search");
  fireEvent.click(
    screen.getByRole("button", { name: "Apply saved item filters" }),
  );
  await waitFor(() =>
    expect(w.get).toHaveBeenLastCalledWith(
      "/api/v1/personal/items",
      expect.objectContaining({
        params: { query: { type: "search", limit: 25 } },
      }),
    ),
  );
});
it("recovers an invalid library continuation by returning to the first page", async () => {
  const w = screenWorkspace();
  w.get
    .mockResolvedValueOnce(etagReply({ items: [], nextCursor: "next" }))
    .mockRejectedValueOnce(new Error("expired cursor"))
    .mockResolvedValue(etagReply({ items: [] }));
  render(<PersonalLibrary {...w.props} />);
  await screen.findByRole("button", { name: "Next page" });
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  await screen.findByRole("alert");
  fireEvent.click(screen.getByRole("button", { name: "Refresh saved items" }));
  await waitFor(() =>
    expect(w.get).toHaveBeenLastCalledWith(
      "/api/v1/personal/items",
      expect.objectContaining({ params: { query: { limit: 25 } } }),
    ),
  );
});
it("recovers My work continuation failures without reusing another queue", async () => {
  const w = screenWorkspace();
  w.get
    .mockResolvedValueOnce(etagReply({ items: [], nextCursor: "next" }))
    .mockRejectedValueOnce(new Error("expired cursor"))
    .mockResolvedValue(etagReply({ items: [] }));
  render(<MyWork {...w.props} />);
  await screen.findByRole("button", { name: "Next page" });
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  await screen.findByRole("alert");
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await screen.findByText("No drafts to continue. Start in Authoring.");
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await waitFor(() => expect(w.get.mock.calls.length).toBe(4));
});
it("projects independently permitted work links, exact decisions and current correction targets", async () => {
  const w = screenWorkspace();
  const base = {
    resourceId: "resource-1",
    namespaceId: "ns-1",
    kind: "agent",
    listingId: "listing-1",
    versionRevisionId: "version-revision-1",
    submissionId: "submission-1",
    listingRevisionId: "listing-revision-1",
    currentCandidateRevisionId: "later-candidate",
  };
  readReplies(w.get, {
    "/api/v1/me/work": {
      items: [
        {
          ...base,
          id: "submission:1",
          type: "submission",
          state: "approved",
          displayName: "Original submitted name",
          canonicalIdentifier: "urn:air:example.com:default:helper",
        },
        {
          ...base,
          id: "submission:2",
          type: "submission",
          state: "changes_required",
        },
      ],
    },
  });
  render(<MyWork {...w.props} />);
  await screen.findByText("Original submitted name");
  expect(
    screen.getAllByRole("link", { name: "Open submitted review" }),
  ).toHaveLength(2);
  expect(
    screen.getAllByRole("link", { name: "Inspect current candidate" }),
  ).toHaveLength(2);
  expect(screen.getByText(/A publisher must recheck/)).toBeTruthy();
  expect(
    screen.getByText("The current correction target is a newer candidate."),
  ).toBeTruthy();
});
it("omits onward links without their independent permissions", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/me/work": {
      items: [
        {
          id: "version:1",
          type: "version",
          state: "unsealed_version",
          resourceId: "r",
          namespaceId: "ns",
          kind: "agent",
          versionRevisionId: "revision",
        },
      ],
    },
  });
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
  await screen.findByText("Identifier unavailable");
  expect(screen.queryByRole("link")).toBeNull();
});
it("requires the loaded saved identity before offering an editor", async () => {
  const w = screenWorkspace();
  readReplies(w.get, {
    "/api/v1/personal/items/{itemId}": { ...item, id: "another-item" },
  });
  render(<PersonalLibrary {...w.props} id={item.id} />);
  await screen.findByRole("alert");
  expect(
    screen.queryByRole("button", { name: "Update saved item" }),
  ).toBeNull();
});
it.each(["update", "remove"])(
  "requires a current ETag before %s",
  async (action) => {
    const w = screenWorkspace();
    w.get.mockResolvedValue(etagReply(item, null));
    render(<PersonalLibrary {...w.props} id={item.id} />);
    await screen.findByRole("heading", { name: item.label });
    const actionButton = screen.getByRole("button", {
      name: action === "update" ? "Update saved item" : "Remove saved item",
    });
    expect(actionButton).toHaveProperty("disabled", true);
    fireEvent.click(actionButton);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByLabelText("Saved label").matches(":disabled")).toBe(
      false,
    );
    expect(
      screen.getByText(/successfully loaded current item validator/),
    ).toBeTruthy();
    expect(w.patch).not.toHaveBeenCalled();
    expect(w.remove).not.toHaveBeenCalled();
  },
);
it("removes only the loaded private item and acknowledges completion", async () => {
  const w = screenWorkspace();
  readReplies(w.get, { "/api/v1/personal/items/{itemId}": item });
  w.remove.mockResolvedValue(etagReply({ id: item.id, revision: 2 }));
  render(<PersonalLibrary {...w.props} id={item.id} />);
  await screen.findByRole("heading", { name: item.label });
  await confirmCommand("Remove saved item");
  expect(await screen.findByText("Saved item removed.")).toBeTruthy();
  expect(w.remove).toHaveBeenCalledWith(
    "/api/v1/personal/items/{itemId}",
    expect.objectContaining({
      params: {
        path: { itemId: item.id },
        header: {
          "If-Match": '"revision-1"',
          "Idempotency-Key": expect.any(String) as unknown,
        },
      },
    }),
  );
});
it.each(["update", "remove"])(
  "keeps a malformed %s acknowledgement uncertain",
  async (action) => {
    const w = screenWorkspace();
    readReplies(w.get, { "/api/v1/personal/items/{itemId}": item });
    w.patch.mockResolvedValue(etagReply({ id: "another-item", revision: 2 }));
    w.remove.mockResolvedValue(etagReply({ id: "another-item", revision: 2 }));
    render(<PersonalLibrary {...w.props} id={item.id} />);
    await screen.findByRole("heading", { name: item.label });
    await confirmCommand(
      action === "update" ? "Update saved item" : "Remove saved item",
    );
    expect(screen.getByRole("dialog").textContent).toContain(
      "The outcome is unknown.",
    );
    expect(screen.queryByText("Saved item removed.")).toBeNull();
  },
);
const reference = {
  origin: "local" as const,
  releaseId: "release-1",
  versionRevisionId: "revision-1",
};
const resourceItem = {
  ...item,
  type: "resource" as const,
  search: undefined,
  reference,
};
it.each(["available", "unavailable", "failure", "substitution"])(
  "resolves an exact saved reference: %s",
  async (mode) => {
    const w = screenWorkspace();
    readReplies(w.get, { "/api/v1/personal/items/{itemId}": resourceItem });
    if (mode === "failure") w.post.mockRejectedValue(new Error("read failed"));
    else
      w.post.mockResolvedValue(
        etagReply(
          mode === "unavailable"
            ? { status: "unavailable" }
            : {
                status: "available",
                reference:
                  mode === "substitution"
                    ? { ...reference, releaseId: "another-release" }
                    : reference,
              },
        ),
      );
    render(<PersonalLibrary {...w.props} id={item.id} />);
    await screen.findByRole("button", { name: "Check saved reference" });
    fireEvent.click(
      screen.getByRole("button", { name: "Check saved reference" }),
    );
    if (mode === "available") {
      expect(
        (
          await screen.findByRole("link", { name: "Open exact saved release" })
        ).getAttribute("href"),
      ).toBe("/unit/catalog/release-1");
    } else if (mode === "unavailable") {
      await screen.findByText(/This exact saved target is unavailable/);
    } else {
      await screen.findByRole("alert");
      expect(
        screen.queryByRole("link", { name: "Open exact saved release" }),
      ).toBeNull();
    }
    expect(w.post).toHaveBeenCalledWith(
      "/api/v1/personal/items/{itemId}/resolve",
      expect.objectContaining({
        body: {},
        params: { path: { itemId: item.id } },
      }),
    );
  },
);
it("cancels an in-flight reference check on unmount without stale identity diagnostics", async () => {
  const w = screenWorkspace();
  readReplies(w.get, { "/api/v1/personal/items/{itemId}": resourceItem });
  const pending = deferred<ReturnType<typeof etagReply>>();
  w.post.mockReturnValue(pending.promise);
  const v = render(<PersonalLibrary {...w.props} id={item.id} />);
  await screen.findByRole("button", { name: "Check saved reference" });
  fireEvent.click(
    screen.getByRole("button", { name: "Check saved reference" }),
  );
  const options = w.post.mock.calls[0]![1] as { signal: AbortSignal };
  v.unmount();
  expect(options.signal.aborted).toBe(true);
  await act(async () => pending.reject(new Error("aborted")));
  expect(w.props.onFailure).not.toHaveBeenCalled();
});
it("captures a named search only when opened and protects its private draft", async () => {
  const w = screenWorkspace();
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  const v = render(
    <SaveSearch
      {...w.props}
      basePath="/unit"
      criteria={{
        text: "original",
        order: "publisher_desc",
        limit: 24,
        cursor: "opaque",
      }}
    />,
  );
  expect(screen.queryByLabelText("Saved label")).toBeNull();
  fireEvent.click(screen.getByText("Save this named search"));
  change("Saved label", "Keep my draft");
  fireEvent.click(screen.getByText("More saved filters"));
  change("Saved sort order", "");
  change("Saved sort order", "kind_desc");
  fireEvent.click(
    screen.getByRole("button", { name: "Edit private saved criteria as JSON" }),
  );
  v.rerender(
    <SaveSearch
      {...w.props}
      basePath="/unit"
      criteria={{ text: "newly submitted" }}
    />,
  );
  expect(
    JSON.parse(
      screen.getByLabelText<HTMLTextAreaElement>("Private saved criteria JSON")
        .value,
    ),
  ).toEqual({ text: "original", order: "kind_desc" });
  fireEvent.click(screen.getByText("Save this named search"));
  expect(screen.getByLabelText("Saved label")).toHaveProperty(
    "value",
    "Keep my draft",
  );
  discard.mockReturnValue(true);
  fireEvent.click(screen.getByText("Save this named search"));
  expect(screen.queryByLabelText("Saved label")).toBeNull();
});
it("does not offer a named search before any submitted criteria exist", () => {
  const w = screenWorkspace();
  const v = render(<SaveSearch {...w.props} basePath="/unit" />);
  expect(v.container.innerHTML).toBe("");
});
it.each(["local", "foreign"])(
  "opens an exact %s save without an eager command",
  async (origin) => {
    const w = screenWorkspace();
    const props =
      origin === "local"
        ? { reference }
        : {
            item: {
              type: "indexed",
              kind: "agent",
              identifier: "urn:air:example.com:default:agent",
              versionPresent: false,
              displayName: "Source helper",
              indexed: {
                presentationSourceId: "source-1",
                presentationGeneration: 2,
              },
            } as components["schemas"]["CatalogSearchResult"],
          };
    render(
      <SaveResource {...w.props} {...props} basePath="/unit" label="Helper" />,
    );
    fireEvent.click(screen.getByText("Save resource"));
    await screen.findByLabelText("Saved label");
    expect(screen.queryByLabelText("Exact resource reference JSON")).toBeNull();
    expect(
      screen.getByRole("heading", { name: "Captured resource reference" }),
    ).toBeTruthy();
    expect(
      screen.getByText(origin === "local" ? reference.releaseId! : "source-1"),
    ).toBeTruthy();
    expect(w.post).not.toHaveBeenCalled();
  },
);
it("keeps invalid private JSON in the editor without preparing a command", () => {
  const w = screenWorkspace();
  render(
    <SavePersonal
      {...w.props}
      basePath="/unit"
      initial={{ type: "search", label: "A label", search: { text: "" } }}
    />,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Edit private saved criteria as JSON" }),
  );
  change("Private saved criteria JSON", '{"cursor":"unsafe"}');
  fireEvent.click(screen.getByRole("button", { name: "Save named search" }));
  expect(screen.getByRole("alert").textContent).toContain(
    "valid supported criteria",
  );
  expect(w.post).not.toHaveBeenCalled();
});

it("submits a captured resource reference and protects it when closing is declined", async () => {
  const w = screenWorkspace();
  w.post.mockResolvedValue(etagReply({ id: "saved-resource", revision: 1 }));
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  render(
    <SaveResource
      {...w.props}
      basePath="/unit"
      label="Helper"
      reference={reference}
    />,
  );
  fireEvent.click(screen.getByText("Save resource"));
  await screen.findByLabelText("Saved label");
  change("Saved label", "Keep this reference");
  fireEvent.click(screen.getAllByText("Save resource")[0]!);
  expect(screen.getByLabelText("Saved label")).toHaveProperty(
    "value",
    "Keep this reference",
  );
  expect(discard).toHaveBeenCalled();
  await confirmCommand("Save resource");
  await screen.findByText("Private item saved.");
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/personal/items",
    expect.objectContaining({
      body: { type: "resource", label: "Keep this reference", reference },
    }),
  );
});
it("renders an exact indexed resolution without replacing its source identity", async () => {
  const w = screenWorkspace();
  const ref = {
    origin: "foreign" as const,
    kind: "agent" as const,
    sourceId: "00000000-0000-7000-8000-000000000001",
    presentationGeneration: 2,
    identifier: "urn:air:example.com:default:agent",
    versionPresent: false,
  };
  const saved = { ...resourceItem, reference: ref };
  const indexed = {
    id: "indexed-1",
    type: "indexed",
    identifier: ref.identifier,
    kind: "agent",
    versionPresent: false,
    displayName: "Exact source helper",
    tags: [],
    indexed: {
      observationGroupId: "observation-group-v1:sha256:" + "a".repeat(64),
      normalizedEntryDigest: "sha256:abc",
      sourceIds: [ref.sourceId],
      sourceCount: 1,
      sourcesTruncated: false,
      conflict: false,
      sameSourceConflict: false,
      presentationSourceId: ref.sourceId,
      presentationGeneration: 2,
    },
  };
  readReplies(w.get, { "/api/v1/personal/items/{itemId}": saved });
  w.post.mockResolvedValue(
    etagReply({ status: "available", reference: ref, indexed }),
  );
  render(<PersonalLibrary {...w.props} id={item.id} />);
  await screen.findByRole("button", { name: "Check saved reference" });
  fireEvent.click(
    screen.getByRole("button", { name: "Check saved reference" }),
  );
  await screen.findByRole("heading", { name: "Current exact source metadata" });
  expect(screen.getByText("Exact source helper")).toBeTruthy();
});
it.each([undefined, "1.2.3"])(
  "preserves an explicitly present foreign version (%s)",
  async (version) => {
    const w = screenWorkspace();
    const item = {
      type: "live",
      kind: "agent",
      identifier: "urn:air:example.com:default:agent",
      versionPresent: true,
      ...(version === undefined ? {} : { version }),
      displayName: "Live helper",
      live: { sourceId: "source-1", presentationGeneration: 2 },
    } as components["schemas"]["CatalogSearchResult"];
    render(
      <SaveResource
        {...w.props}
        basePath="/unit"
        label="Live helper"
        item={item}
      />,
    );
    fireEvent.click(screen.getByText("Save resource"));
    expect(screen.queryByLabelText("Exact resource reference JSON")).toBeNull();
    expect(await screen.findByText("Present")).toBeTruthy();
    expect(screen.getByText(version || "Empty version")).toBeTruthy();
  },
);
it("offers a save for a complete local result projection", async () => {
  const w = screenWorkspace();
  const item = {
    type: "local",
    local: { release: { id: "release" }, versionRevision: { id: "revision" } },
  } as components["schemas"]["CatalogSearchResult"];
  render(
    <SaveResource
      {...w.props}
      basePath="/unit"
      label="Local helper"
      item={item}
    />,
  );
  fireEvent.click(screen.getByText("Save resource"));
  expect(await screen.findByLabelText("Saved label")).toHaveProperty(
    "value",
    "Local helper",
  );
});
it("clears a closed saved-search draft before capturing the next submitted query", () => {
  const w = screenWorkspace();
  render(<SaveSearch {...w.props} basePath="/unit" criteria={{}} />);
  fireEvent.click(screen.getByText("Save this named search"));
  fireEvent.click(
    screen.getByRole("button", { name: "Edit private saved criteria as JSON" }),
  );
  expect(screen.getByLabelText("Private saved criteria JSON")).toHaveProperty(
    "value",
    '{\n  "text": ""\n}',
  );
  fireEvent.click(screen.getByText("Save this named search"));
  expect(screen.queryByLabelText("Saved label")).toBeNull();
});

it("requires live disclosure consent and a separate explicit Search after changing consent", async () => {
  const w = screenWorkspace();
  w.post.mockResolvedValue(
    etagReply({
      items: [],
      durableTotal: 0,
      liveCount: 0,
      partial: false,
      diagnostics: [],
    }),
  );
  render(
    <Catalog
      {...w.props}
      stagedSearch={{ text: "current query", origins: ["live"] }}
    />,
  );
  fireEvent.click(
    screen.getByRole("checkbox", {
      name: /administrator-managed live sources/,
    }),
  );
  expect(w.post).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Search" }));
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(1));
  fireEvent.click(
    screen.getByRole("checkbox", {
      name: /administrator-managed live sources/,
    }),
  );
  fireEvent.click(
    screen.getByRole("checkbox", {
      name: /administrator-managed live sources/,
    }),
  );
  expect(w.post).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("button", { name: "Search" }));
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(2));
  fireEvent.click(screen.getByRole("button", { name: "Exclude live results" }));
  await act(async () => {});
  expect(w.post).toHaveBeenCalledTimes(2);
  fireEvent.click(screen.getByRole("button", { name: "Search" }));
  expect(w.post).toHaveBeenCalledTimes(2);
  fireEvent.click(
    screen.getByRole("checkbox", { name: /configured embedding provider/ }),
  );
  expect(w.post).toHaveBeenCalledTimes(2);
  fireEvent.click(screen.getByRole("button", { name: "Search" }));
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(3));
});

it.each(["success", "failure"])(
  "ignores late %s from a canceled reference check after an acknowledged update",
  async (outcome) => {
    const w = screenWorkspace();
    const old = { ...resourceItem, revision: 1 };
    w.get.mockResolvedValue(etagReply(old));
    const pending = deferred<ReturnType<typeof etagReply>>();
    w.post.mockReturnValue(pending.promise);
    w.patch.mockResolvedValue(etagReply({ id: item.id, revision: 2 }));
    render(<PersonalLibrary {...w.props} id={item.id} />);
    await screen.findByRole("button", { name: "Check saved reference" });
    fireEvent.click(
      screen.getByRole("button", { name: "Check saved reference" }),
    );
    const options = w.post.mock.calls[0]![1] as { signal: AbortSignal };
    const replacement = { ...reference, releaseId: "replacement-release" };
    change("Saved label", "Updated while checking");
    change("Exact resource reference JSON", JSON.stringify(replacement));
    w.get.mockResolvedValue(
      etagReply({
        ...old,
        reference: replacement,
        label: "Updated while checking",
        revision: 2,
      }),
    );
    await confirmCommand("Update saved item");
    await screen.findByText("Private item saved.");
    expect(options.signal.aborted).toBe(true);
    await screen.findByRole("heading", {
      name: "Updated while checking",
    });
    await act(async () => {
      if (outcome === "success")
        pending.resolve(etagReply({ status: "available", reference }));
      else pending.reject(new Error("aborted"));
    });
    expect(
      screen.queryByRole("link", { name: "Open exact saved release" }),
    ).toBeNull();
    w.post.mockResolvedValue(
      etagReply({ status: "available", reference: replacement }),
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Check saved reference" }),
    );
    expect(
      (
        await screen.findByRole("link", { name: "Open exact saved release" })
      ).getAttribute("href"),
    ).toBe("/unit/catalog/replacement-release");
  },
);

it.each([
  ["drafts", "No drafts to continue. Start in Authoring."],
  ["submitted", "No submitted work is awaiting review."],
  ["changes_required", "No reviewed work needs changes."],
  ["approved", "No approved work without a release."],
])(
  "explains the empty %s queue without promising publication",
  async (queue, message) => {
    const w = screenWorkspace();
    readReplies(w.get, { "/api/v1/me/work": { items: [] } });
    render(<MyWork {...w.props} />);
    fireEvent.change(screen.getByLabelText("Work queue"), {
      target: { value: queue },
    });
    await screen.findByText(message);
    expect(
      screen.getByText(
        "View drafts, submitted reviews, requested changes and approved work with no release.",
      ),
    ).toBeTruthy();
    expect(w.post).not.toHaveBeenCalled();
  },
);

it.each([
  ["name_asc", "Name (A–Z)"],
  ["name_desc", "Name (Z–A)"],
  ["kind_asc", "Kind (A–Z)"],
  ["kind_desc", "Kind (Z–A)"],
  ["publisher_asc", "Publisher (A–Z)"],
  ["publisher_desc", "Publisher (Z–A)"],
] as const)(
  "summarizes saved order %s without resolving a target",
  async (order, label) => {
    const w = screenWorkspace();
    const saved = {
      ...item,
      search: { text: "", kinds: ["mcp_server"], order },
    };
    readReplies(w.get, { "/api/v1/personal/items": { items: [saved] } });
    render(<PersonalLibrary {...w.props} />);
    const link = await screen.findByRole("link", {
      name: saved.label,
    });
    expect(link.closest("tr")!.textContent).toContain(label);
    expect(link.closest("tr")!.textContent).toContain("MCP Server");
    expect(link.closest("tr")!.textContent).toContain("Any search text");
    expect(
      w.get.mock.calls.every((call) => call[0] === "/api/v1/personal/items"),
    ).toBe(true);
    expect(w.post).not.toHaveBeenCalled();
  },
);
