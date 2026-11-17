// @vitest-environment jsdom
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { expect, it, vi } from "vitest";
import Collections from "./Collections";
import { AddToCollection } from "./AddToCollection";
import * as checks from "./collection-responses";
import { APIError } from "../api/client";
import {
  screenWorkspace,
  screenLifecycle,
  etagReply,
  stamp,
  administrator,
  confirmCommand,
  namespace as namespaceFixture,
} from "../../tests/unit/screens";

screenLifecycle("/unit/collections");
const collection = {
  id: "collection-1",
  namespaceId: "ns-1",
  name: "Finance tools",
  description: "Shared metadata",
  revision: 3,
  createdAt: stamp,
  updatedAt: stamp,
};

it("rejects malformed member identities before they can drive links or removal", () => {
  const metadata = { id: "entry-1", namespaceId: "ns-1" };
  const record = {
    source: "local",
    resource: {
      id: "resource-1",
      namespaceId: "ns-1",
      tenantId: "tenant-a",
      canonicalIdentifier: "urn:air:example.com:agents:one",
      kind: "agent",
    },
    version: { id: "version-1", version: "1.0.0" },
    listing: metadata,
    listingRevision: {
      id: "display-1",
      displayName: "Entry",
      description: "Description",
      tags: [],
      publisherId: "publisher-1",
      visibility: "tenant",
    },
    versionRevision: { id: "revision-1" },
    release: {
      id: "release-1",
      listingRevisionId: "display-1",
      versionRevisionId: "revision-1",
      state: "published",
      activatedAt: stamp,
    },
  };
  expect(checks.member(record)).toBe(true);
  for (const value of [
    { ...record, source: "indexed" },
    { ...record, listing: null },
    { ...record, listing: { ...metadata, id: "../escape" } },
    { ...record, versionRevision: null },
    { ...record, release: { ...record.release, id: "" } },
    {
      ...record,
      release: { ...record.release, listingRevisionId: "another-display" },
    },
    {
      ...record,
      release: { ...record.release, versionRevisionId: "another-revision" },
    },
    { ...record, resource: { ...record.resource, namespaceId: "another-ns" } },
  ])
    expect(checks.member(value)).toBe(false);
  expect(checks.memberPage({ items: Array(100).fill(record) })).toBe(true);
  expect(checks.memberPage({ items: Array(101).fill(record) })).toBe(false);
  expect(checks.collection(collection)).toBe(true);
  expect(checks.collection({ ...collection, revision: 0 })).toBe(false);
  expect(checks.collection({ ...collection, name: "x".repeat(161) })).toBe(
    false,
  );
  expect(checks.collectionPage({ items: [collection], nextCursor: 42 })).toBe(
    false,
  );
  expect(checks.memberPage({ items: [{ listing: metadata }] })).toBe(false);
});

it("bounds collection metadata by Unicode code points and caps directory pages", () => {
  for (const length of [159, 160, 161])
    expect(
      checks.collection({ ...collection, name: "\u{1f4da}".repeat(length) }),
    ).toBe(length <= 160);
  for (const length of [1999, 2000, 2001])
    expect(
      checks.collection({
        ...collection,
        description: "\u{1f4da}".repeat(length),
      }),
    ).toBe(length <= 2000);
  expect(checks.collection({ ...collection, revision: 1.5 })).toBe(false);
  expect(
    checks.collection({ ...collection, revision: Number.MAX_SAFE_INTEGER + 1 }),
  ).toBe(false);
  expect(checks.collection({ ...collection, name: " \t " })).toBe(false);
  expect(checks.collectionPage({ items: Array(100).fill(collection) })).toBe(
    true,
  );
  expect(checks.collectionPage({ items: Array(101).fill(collection) })).toBe(
    false,
  );
});

it("does not offer curation commands to a collection reader", async () => {
  const w = screenWorkspace();
  w.get.mockImplementation(async (path) =>
    etagReply(
      String(path).endsWith("/members") ? { items: [] } : collection,
      '"rev-3"',
    ),
  );
  render(
    <Collections
      {...w.props}
      id="collection-1"
      access={{
        ...w.props.access,
        permissions: ["collection.read"],
        availablePermissions: ["collection.read"],
        namespaceGrants: [],
      }}
    />,
  );
  await screen.findByRole("heading", { name: "Finance tools" });
  expect(
    screen.queryByRole("button", { name: "Delete collection" }),
  ).toBeNull();
  expect(screen.queryByRole("button", { name: "Review changes" })).toBeNull();
  expect(
    screen.getByText(/Entries appear here when you can read them/),
  ).not.toBeNull();
});

it.each([
  { compact: false, level: 2 },
  { compact: true, level: 3 },
])(
  "uses h$level for the collection action in its record context",
  async ({ compact, level }) => {
    const w = screenWorkspace();
    w.props.access = administrator;
    render(
      <AddToCollection
        {...w.props}
        basePath="/unit"
        listingId="listing-1"
        compact={compact}
      />,
    );
    expect(
      await screen.findByRole("heading", { name: "Shared collections", level }),
    ).toBeTruthy();
  },
);

it("retains a deliberate retry's exact member, parent revision and key", async () => {
  const w = screenWorkspace();
  w.props.access = administrator;
  w.get.mockImplementation(async (path) =>
    etagReply(
      path === "/api/v1/collections" ? { items: [collection] } : collection,
      '"rev-3"',
    ),
  );
  w.post
    .mockRejectedValueOnce(new Error("reply lost"))
    .mockResolvedValueOnce(etagReply({ id: collection.id, revision: 4 }));
  const view = render(
    <AddToCollection {...w.props} basePath="/unit" listingId="listing-1" />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Add to collection" }));
  const selector = await screen.findByRole("combobox", {
    name: "Destination collection",
  });
  await waitFor(() =>
    expect(
      within(selector).getByRole("option", { name: /Finance tools/ }),
    ).not.toBeNull(),
  );
  fireEvent.change(selector, { target: { value: collection.id } });
  const review = await screen.findByRole("button", { name: "Review addition" });
  await waitFor(() =>
    expect((review as HTMLButtonElement).disabled).toBe(false),
  );
  fireEvent.click(review);
  fireEvent.click(
    within(screen.getByRole("dialog")).getByRole("button", {
      name: "Confirm",
    }),
  );
  const retry = await screen.findByRole("button", {
    name: "Retry same command",
  });
  fireEvent.click(retry);
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(2));
  expect(w.post.mock.calls[0]).toEqual(w.post.mock.calls[1]);
  expect(w.post.mock.calls[0]).toEqual([
    "/api/v1/collections/{collectionId}/members",
    expect.objectContaining({
      params: {
        path: { collectionId: collection.id },
        header: {
          "If-Match": '"rev-3"',
          "Idempotency-Key": expect.any(String) as unknown,
        },
      },
      body: { listingId: "listing-1" },
    }),
  ]);
  await screen.findByText("Entry added to collection.");
  view.rerender(
    <AddToCollection
      {...w.props}
      basePath="/unit"
      listingId="listing-1"
      access={{
        ...w.props.access,
        permissions: ["collection.read"],
        availablePermissions: ["collection.read"],
        namespaceGrants: [],
      }}
    />,
  );
  expect(
    screen.getByRole("button", { name: "Add to collection" }),
  ).toHaveProperty("disabled", true);
});

it("refuses a destination without exact owner management despite navigation hints", async () => {
  const w = screenWorkspace();
  w.get.mockImplementation(async (path) =>
    etagReply(
      path === "/api/v1/collections" ? { items: [collection] } : collection,
      '"rev-3"',
    ),
  );
  render(
    <AddToCollection
      {...w.props}
      basePath="/unit"
      listingId="listing-1"
      access={{
        ...w.props.access,
        permissions: ["collection.read"],
        availablePermissions: ["collection.read", "collection.manage"],
        namespaceGrants: [
          {
            namespaceId: "another-ns",
            permissions: ["collection.read", "collection.manage"],
          },
        ],
      }}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Add to collection" }));
  const selector = await screen.findByRole("combobox", {
    name: "Destination collection",
  });
  await waitFor(() =>
    expect(
      within(selector).getByRole("option", { name: /Finance tools/ }),
    ).not.toBeNull(),
  );
  fireEvent.change(selector, { target: { value: collection.id } });
  await waitFor(() =>
    expect(
      (
        screen.getByRole("button", {
          name: "Review addition",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true),
  );
  expect(w.post).not.toHaveBeenCalled();
});

it("offers explicit first-page recovery after a stale member continuation", async () => {
  const w = screenWorkspace();
  w.get.mockImplementation(async (path, options) => {
    if (!String(path).endsWith("/members"))
      return etagReply(collection, '"rev-3"');
    const query = (options as { params: { query: { cursor?: string } } }).params
      .query;
    if (query.cursor)
      throw new APIError(400, "INVALID_ARGUMENT", "Collection changed.");
    return etagReply({ items: [], nextCursor: "stale-cursor" });
  });
  render(<Collections {...w.props} id="collection-1" />);
  fireEvent.click(await screen.findByRole("button", { name: "Next page" }));
  await screen.findByText(/The request is no longer valid/);
  const recovery = await screen.findByRole("button", {
    name: "Start from first page",
  });
  fireEvent.click(recovery);
  await waitFor(() =>
    expect(screen.queryByText(/The request is no longer valid/)).toBeNull(),
  );
  expect(
    (
      (await screen.findByRole("button", {
        name: "Next page",
      })) as HTMLButtonElement
    ).disabled,
  ).toBe(false);
});

it("pages readable collections without granting management from list visibility", async () => {
  const w = screenWorkspace();
  w.get.mockImplementation(async (path, options) => {
    if (String(path).includes("namespaces")) return etagReply(namespaceFixture);
    const cursor = (options as { params: { query: { cursor?: string } } })
      .params.query.cursor;
    return etagReply(
      cursor
        ? {
            items: [
              { ...collection, id: "collection-2", name: "Second collection" },
            ],
          }
        : { items: [collection], nextCursor: "next-collections" },
    );
  });
  render(<Collections {...w.props} id="" />);
  expect(
    (await screen.findByRole("link", { name: "Finance tools" })).getAttribute(
      "href",
    ),
  ).toBe("/unit/collections/collection-1");
  expect(screen.queryByRole("link", { name: "Create collection" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  await screen.findByRole("link", { name: "Second collection" });
  fireEvent.click(screen.getByRole("button", { name: "First page" }));
  await screen.findByRole("link", { name: "Finance tools" });
  fireEvent.click(screen.getByRole("button", { name: "Refresh collections" }));
  await screen.findByRole("link", { name: "Finance tools" });
});

it("validates a manual owner and acknowledges creation before linking to the result", async () => {
  const w = screenWorkspace();
  w.props.access = {
    ...w.props.access,
    permissions: ["collection.read", "collection.manage"],
    availablePermissions: ["collection.read", "collection.manage"],
    namespaceGrants: [],
  };
  w.post.mockResolvedValue(etagReply({ id: collection.id, revision: 1 }));
  render(<Collections {...w.props} id="new" />);
  fireEvent.change(
    screen.getByRole("textbox", { name: "Owner namespace ID" }),
    { target: { value: collection.namespaceId } },
  );
  fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
    target: { value: "   " },
  });
  const submit = screen.getByRole("button", { name: "Review collection" });
  await waitFor(() =>
    expect((submit as HTMLButtonElement).disabled).toBe(false),
  );
  fireEvent.submit(submit.closest("form")!);
  await screen.findByRole("alert");
  expect(w.post).not.toHaveBeenCalled();
  fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
    target: { value: " New team collection " },
  });
  fireEvent.change(screen.getByRole("textbox", { name: "Description" }), {
    target: { value: " Shared text " },
  });
  await confirmCommand("Review collection");
  expect(
    (await screen.findByRole("link", { name: "Open collection" })).getAttribute(
      "href",
    ),
  ).toBe("/unit/collections/collection-1");
  expect(w.post).toHaveBeenCalledWith(
    "/api/v1/collections",
    expect.objectContaining({
      params: { header: { "Idempotency-Key": expect.any(String) as unknown } },
      body: {
        namespaceId: collection.namespaceId,
        name: "New team collection",
        description: "Shared text",
      },
    }),
  );
});

it("uses confirmed metadata after an edit and preserves the captured ETag", async () => {
  const w = screenWorkspace();
  w.props.access = administrator;
  let current = collection;
  w.get.mockImplementation(async (path) =>
    etagReply(
      String(path).endsWith("/members")
        ? { items: [] }
        : String(path).includes("namespaces")
          ? namespaceFixture
          : current,
      `"rev-${current.revision}"`,
    ),
  );
  w.patch.mockImplementation(async () => {
    current = {
      ...collection,
      name: "Confirmed finance tools",
      description: "Server text",
      revision: 4,
    };
    return etagReply({ id: collection.id, revision: 4 });
  });
  render(<Collections {...w.props} id={collection.id} />);
  const name = await screen.findByRole("textbox", { name: "Name" });
  fireEvent.change(name, { target: { value: "Draft name" } });
  fireEvent.change(screen.getByRole("textbox", { name: "Description" }), {
    target: { value: "" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Review changes" }));
  fireEvent.click(
    within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }),
  );
  expect(w.patch).not.toHaveBeenCalled();
  await confirmCommand("Review changes");
  await screen.findByRole("heading", { name: "Confirmed finance tools" });
  expect(
    (screen.getByRole("textbox", { name: "Name" }) as HTMLInputElement).value,
  ).toBe("Confirmed finance tools");
  expect(w.patch).toHaveBeenCalledWith(
    "/api/v1/collections/{collectionId}",
    expect.objectContaining({
      params: {
        path: { collectionId: collection.id },
        header: {
          "If-Match": '"rev-3"',
          "Idempotency-Key": expect.any(String) as unknown,
        },
      },
      body: { name: "Draft name", description: "" },
    }),
  );
});

it("retains exact-key recovery when collection management hints disappear", async () => {
  const w = screenWorkspace();
  w.props.access = administrator;
  w.get.mockImplementation(async (path) =>
    etagReply(
      path === "/api/v1/collections" ? { items: [collection] } : collection,
      '"rev-3"',
    ),
  );
  w.post.mockRejectedValue(new Error("reply lost"));
  const view = render(
    <AddToCollection {...w.props} basePath="/unit" listingId="listing-1" />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Add to collection" }));
  const selector = await screen.findByRole("combobox", {
    name: "Destination collection",
  });
  await waitFor(() =>
    expect(
      within(selector).getByRole("option", { name: /Finance tools/ }),
    ).not.toBeNull(),
  );
  fireEvent.change(selector, { target: { value: collection.id } });
  const review = await screen.findByRole("button", { name: "Review addition" });
  await waitFor(() =>
    expect((review as HTMLButtonElement).disabled).toBe(false),
  );
  fireEvent.click(review);
  fireEvent.click(
    within(screen.getByRole("dialog")).getByRole("button", { name: "Confirm" }),
  );
  await screen.findByRole("button", { name: "Retry same command" });
  view.rerender(
    <AddToCollection
      {...w.props}
      basePath="/unit"
      listingId="listing-1"
      access={{
        ...w.props.access,
        permissions: ["collection.read"],
        availablePermissions: ["collection.read"],
        namespaceGrants: [],
      }}
    />,
  );
  fireEvent.click(
    await screen.findByRole("button", { name: "Retry same command" }),
  );
  await waitFor(() => expect(w.post).toHaveBeenCalledTimes(2));
  expect(w.post.mock.calls[1]).toEqual(w.post.mock.calls[0]);
});

it("does not load collection destinations without management and cancels a prepared addition", async () => {
  const w = screenWorkspace();
  w.props.access = administrator;
  w.get.mockImplementation(async (path) =>
    etagReply(
      path === "/api/v1/collections" ? { items: [collection] } : collection,
      '"rev-3"',
    ),
  );
  const view = render(
    <AddToCollection
      {...w.props}
      basePath="/unit"
      listingId="listing-1"
      access={{
        ...w.props.access,
        permissions: [],
        availablePermissions: [],
        namespaceGrants: [],
      }}
    />,
  );
  expect(
    screen.queryByRole("button", { name: "Add to collection" }),
  ).toBeNull();
  expect(w.get).not.toHaveBeenCalled();
  view.rerender(
    <AddToCollection {...w.props} basePath="/unit" listingId="listing-1" />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Add to collection" }));
  const selector = await screen.findByRole("combobox", {
    name: "Destination collection",
  });
  await waitFor(() =>
    expect(
      within(selector).getByRole("option", { name: /Finance tools/ }),
    ).not.toBeNull(),
  );
  fireEvent.change(selector, { target: { value: collection.id } });
  const review = await screen.findByRole("button", { name: "Review addition" });
  await waitFor(() =>
    expect((review as HTMLButtonElement).disabled).toBe(false),
  );
  fireEvent.click(review);
  fireEvent.click(
    within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }),
  );
  expect(w.post).not.toHaveBeenCalled();
  fireEvent.click(
    screen.getByRole("button", { name: "Close collection picker" }),
  );
  expect(
    screen.queryByRole("combobox", { name: "Destination collection" }),
  ).toBeNull();
});

it("removes only the selected reference and acknowledges whole collection deletion", async () => {
  const w = screenWorkspace();
  w.props.access = administrator;
  const record = {
    source: "local",
    resource: {
      id: "resource-1",
      namespaceId: "ns-1",
      tenantId: "tenant-a",
      canonicalIdentifier: "urn:air:example.com:agents:one",
      kind: "agent",
    },
    version: { id: "version-1", version: "1.0.0" },
    listing: { id: "listing-1", namespaceId: "ns-1" },
    listingRevision: {
      id: "display-1",
      displayName: "Selected entry",
      description: "Entry metadata",
      tags: [],
      publisherId: "publisher-1",
      visibility: "tenant",
    },
    versionRevision: { id: "revision-1" },
    release: {
      id: "release-1",
      listingRevisionId: "display-1",
      versionRevisionId: "revision-1",
      state: "published",
      activatedAt: stamp,
    },
  };
  let revision = 3;
  let entries = [record];
  w.get.mockImplementation(async (path) =>
    etagReply(
      String(path).endsWith("/members")
        ? { items: entries }
        : String(path).includes("namespaces")
          ? namespaceFixture
          : { ...collection, revision },
      `"rev-${revision}"`,
    ),
  );
  w.remove.mockImplementation(async () => {
    entries = [];
    revision++;
    return etagReply({ id: collection.id, revision });
  });
  render(<Collections {...w.props} id={collection.id} />);
  const selected = await screen.findByRole("link", { name: "Selected entry" });
  expect(selected.getAttribute("href")).toBe("/unit/catalog/release-1");
  fireEvent.click(
    screen.getByRole("button", { name: "Remove from collection" }),
  );
  fireEvent.click(
    within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }),
  );
  expect(w.remove).not.toHaveBeenCalled();
  await confirmCommand("Remove from collection");
  await waitFor(() =>
    expect(screen.queryByRole("link", { name: "Selected entry" })).toBeNull(),
  );
  expect(w.remove).toHaveBeenNthCalledWith(
    1,
    "/api/v1/collections/{collectionId}/members/{listingId}",
    expect.objectContaining({
      params: {
        path: { collectionId: collection.id, listingId: "listing-1" },
        header: {
          "If-Match": '"rev-3"',
          "Idempotency-Key": expect.any(String) as unknown,
        },
      },
    }),
  );
  const deleteButton = screen.getByRole("button", {
    name: "Delete collection",
  });
  await waitFor(() =>
    expect((deleteButton as HTMLButtonElement).disabled).toBe(false),
  );
  await confirmCommand("Delete collection");
  await screen.findByText(
    "Collection deleted. Source entries remain unchanged.",
  );
  expect(
    screen
      .getByRole("link", { name: "Back to collections" })
      .getAttribute("href"),
  ).toBe("/unit/collections");
  expect(w.remove).toHaveBeenNthCalledWith(
    2,
    "/api/v1/collections/{collectionId}",
    expect.objectContaining({
      params: {
        path: { collectionId: collection.id },
        header: {
          "If-Match": '"rev-4"',
          "Idempotency-Key": expect.any(String) as unknown,
        },
      },
    }),
  );
});

it("preserves edits and exact uncertain-command recovery across access updates", async () => {
  const w = screenWorkspace();
  w.props.access = administrator;
  w.get.mockImplementation(async (path) =>
    etagReply(
      String(path).endsWith("/members")
        ? { items: [] }
        : String(path).includes("namespaces")
          ? namespaceFixture
          : collection,
      '"rev-3"',
    ),
  );
  w.patch
    .mockRejectedValueOnce(new Error("reply lost"))
    .mockRejectedValueOnce(new APIError(403, "FORBIDDEN", "request-refused"));
  const view = render(<Collections {...w.props} id={collection.id} />);
  const name = await screen.findByRole("textbox", { name: "Name" });
  fireEvent.change(name, { target: { value: "Preserve this edit" } });
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh current entries" }),
  );
  expect(screen.getByRole("textbox", { name: "Name" })).toHaveProperty(
    "value",
    "Preserve this edit",
  );
  const discard = vi.spyOn(window, "confirm").mockReturnValue(false);
  fireEvent.click(screen.getByRole("button", { name: "Reload collection" }));
  expect(discard).toHaveBeenCalledWith(
    "Discard unsaved changes and reload this collection?",
  );
  expect(screen.getByRole("textbox", { name: "Name" })).toHaveProperty(
    "value",
    "Preserve this edit",
  );
  discard.mockReturnValue(true);
  fireEvent.click(screen.getByRole("button", { name: "Reload collection" }));
  await waitFor(() =>
    expect(screen.getByRole("textbox", { name: "Name" })).toHaveProperty(
      "value",
      collection.name,
    ),
  );
  fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
    target: { value: "Retain uncertain edit" },
  });
  await confirmCommand("Review changes");
  await screen.findByRole("button", { name: "Retry same command" });
  view.rerender(
    <Collections
      {...w.props}
      id={collection.id}
      access={{
        ...w.props.access,
        permissions: ["collection.read"],
        availablePermissions: ["collection.read"],
        namespaceGrants: [],
      }}
    />,
  );
  expect(screen.getByRole("textbox", { name: "Name" })).toHaveProperty(
    "value",
    "Retain uncertain edit",
  );
  fireEvent.click(
    await screen.findByRole("button", { name: "Retry same command" }),
  );
  await waitFor(() => expect(w.patch).toHaveBeenCalledTimes(2));
  expect(w.patch.mock.calls[1]).toEqual(w.patch.mock.calls[0]);
});
