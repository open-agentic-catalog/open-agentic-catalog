// @vitest-environment jsdom
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { expect, it } from "vitest";
import Collections from "./Collections";
import { APIError } from "../api/client";
import { deferred } from "../../tests/unit/fixtures";
import {
  administrator,
  etagReply,
  namespace,
  screenLifecycle,
  screenWorkspace,
  stamp,
} from "../../tests/unit/screens";

screenLifecycle("/unit/collections/one");
const collection = {
  id: "one",
  namespaceId: namespace.id,
  name: "Finance tools",
  description: "Curated",
  revision: 1,
  createdAt: stamp,
  updatedAt: stamp,
};
const member = {
  source: "local",
  resource: {
    id: "resource",
    tenantId: "tenant-a",
    namespaceId: namespace.id,
    canonicalIdentifier: "urn:air:example.com:agents:invoice",
    kind: "agent",
  },
  version: { id: "version", version: "1.0.0" },
  versionRevision: { id: "version-revision" },
  listing: { id: "listing", namespaceId: namespace.id },
  listingRevision: {
    id: "listing-revision",
    displayName: "Invoice entry",
    description: "Released metadata",
    tags: [],
    publisherId: "publisher",
    visibility: "tenant",
  },
  release: {
    id: "release",
    listingRevisionId: "listing-revision",
    versionRevisionId: "version-revision",
    state: "published",
    activatedAt: stamp,
  },
};

it.each(["collection metadata", "the member page"] as const)(
  "loads one member page when %s arrives first",
  async (first) => {
    const w = screenWorkspace();
    const metadata = deferred<void>();
    const members = deferred<void>();
    let memberReads = 0;
    w.get.mockImplementation(async (path) => {
      if (String(path).endsWith("/members")) {
        memberReads++;
        await members.promise;
        return etagReply({ items: [member] });
      }
      if (String(path).includes("namespaces")) return etagReply(namespace);
      await metadata.promise;
      return etagReply(collection);
    });
    render(<Collections {...w.props} id="one" />);
    const order =
      first === "collection metadata"
        ? [metadata, members]
        : [members, metadata];
    for (const response of order) {
      await act(async () => response.resolve());
    }
    const entry = await screen.findByRole("link", { name: "Invoice entry" });
    expect(entry.getAttribute("href")).toBe("/unit/catalog/release");
    expect(memberReads).toBe(1);
    await act(async () => {
      fireEvent.click(
        screen.getByRole("button", { name: "Reload collection" }),
      );
    });
    expect(memberReads).toBe(2);
  },
);

it("stages criteria, pages and refreshes applied filters, recovers explicitly and preserves metadata drafts", async () => {
  const w = screenWorkspace();
  w.props.access = administrator;
  const queries: Record<string, unknown>[] = [];
  w.get.mockImplementation(async (path, options) => {
    if (!String(path).endsWith("/members"))
      return etagReply(
        String(path).includes("namespaces") ? namespace : collection,
        '"rev-1"',
      );
    const query = (options as { params: { query: Record<string, unknown> } })
      .params.query;
    queries.push(query);
    if (query.cursor) throw new APIError(400, "INVALID_ARGUMENT");
    return etagReply({
      items: query.text === "none" ? [] : [member],
      ...(query.text === "none" ? {} : { nextCursor: "later" }),
    });
  });
  render(<Collections {...w.props} id="one" />);
  await screen.findByRole("link", { name: "Invoice entry" });
  const initial = queries.length;
  fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
    target: { value: "Unsent metadata" },
  });
  fireEvent.change(
    screen.getByRole("searchbox", { name: /Search collection entries/ }),
    { target: { value: "invoice" } },
  );
  fireEvent.click(screen.getByRole("checkbox", { name: "Agents" }));
  expect(queries).toHaveLength(initial);
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  await screen.findByText("Applied filters: invoice; Agent.");
  await waitFor(() =>
    expect(queries.at(-1)).toEqual({
      limit: 20,
      text: "invoice",
      kinds: ["agent"],
    }),
  );
  fireEvent.change(
    screen.getByRole("searchbox", { name: /Search collection entries/ }),
    { target: { value: "unsent" } },
  );
  fireEvent.click(screen.getByRole("checkbox", { name: "Skills" }));
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh current entries" }),
  );
  await screen.findByRole("link", { name: "Invoice entry" });
  expect(queries.at(-1)).toEqual({
    limit: 20,
    text: "invoice",
    kinds: ["agent"],
  });
  fireEvent.click(screen.getByRole("button", { name: "Next page" }));
  await screen.findByText(/The request is no longer valid/);
  expect(queries.at(-1)).toEqual({
    limit: 20,
    text: "invoice",
    kinds: ["agent"],
    cursor: "later",
  });
  const failedCount = queries.length;
  expect(
    screen.getByRole("searchbox", { name: /Search collection entries/ }),
  ).toHaveProperty("value", "unsent");
  expect(screen.getByRole("checkbox", { name: "Skills" })).toHaveProperty(
    "checked",
    true,
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh current entries" }),
  );
  await screen.findByRole("link", { name: "Invoice entry" });
  expect(queries).toHaveLength(failedCount + 1);
  expect(screen.getByRole("checkbox", { name: "Skills" })).toHaveProperty(
    "checked",
    true,
  );
  expect(queries.at(-1)).toEqual({
    limit: 20,
    text: "invoice",
    kinds: ["agent"],
  });
  expect(screen.getByRole("textbox", { name: "Name" })).toHaveProperty(
    "value",
    "Unsent metadata",
  );
  fireEvent.change(
    screen.getByRole("searchbox", { name: /Search collection entries/ }),
    { target: { value: "none" } },
  );
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  await screen.findByText(/No currently readable entries match these filters/);
  fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
  await screen.findByRole("link", { name: "Invoice entry" });
  expect(queries.at(-1)).toEqual({ limit: 20 });
  expect(
    screen.getByRole("searchbox", { name: /Search collection entries/ }),
  ).toHaveProperty("value", "");
  expect(w.post).not.toHaveBeenCalled();
  expect(w.patch).not.toHaveBeenCalled();
  expect(w.remove).not.toHaveBeenCalled();
});

it("bounds filter text in Unicode code points before sending a request", async () => {
  const w = screenWorkspace();
  w.get.mockImplementation(async (path) =>
    etagReply(
      String(path).endsWith("/members") ? { items: [] } : collection,
      '"rev-1"',
    ),
  );
  render(<Collections {...w.props} id="one" />);
  await screen.findByText(/No currently readable entries\./);
  const calls = w.get.mock.calls.length;
  fireEvent.change(
    screen.getByRole("searchbox", { name: /Search collection entries/ }),
    { target: { value: "📚".repeat(257) } },
  );
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  const error = await screen.findByRole("alert");
  expect(
    screen
      .getByRole("searchbox", { name: /Search collection entries/ })
      .getAttribute("aria-describedby"),
  ).toBe(error.id);
  expect(w.get).toHaveBeenCalledTimes(calls);
  fireEvent.change(
    screen.getByRole("searchbox", { name: /Search collection entries/ }),
    { target: { value: "📚".repeat(256) } },
  );
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  await waitFor(() =>
    expect(w.get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { text: "📚".repeat(256) } },
    }),
  );
});
