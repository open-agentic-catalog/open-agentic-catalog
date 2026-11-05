// @vitest-environment jsdom
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { expect, it } from "vitest";
import Explore from "./Explore";
import Namespaces from "./Namespaces";
import Publishers from "./Publishers";
import {
  administrator,
  change,
  click,
  etagReply,
  namespace,
  publisher,
  readReplies,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";

screenLifecycle("/unit");
const labels = (name: string) =>
  within(screen.getByRole("region", { name }))
    .getAllByRole("row")
    .slice(1)
    .map((row) => within(row).getAllByRole("cell")[0]!.textContent);

it("sorts Browse by displayed labels and natural version text, preserving the applied page without requests", async () => {
  const w = screenWorkspace();
  const record = (
    id: string,
    name: string,
    kind: string,
    version: string,
    state: string,
  ) =>
    Object.freeze({
      release: { id, state },
      listingRevision: { displayName: name },
      resource: { kind },
      version: { version },
    });
  const items = Object.freeze([
    record("r10", "Tool 10", "agent", "2.10", "published"),
    record("r2", "Tool 2", "skill", "2.2", "deprecated"),
    record("r1", "Tool 1", "mcp_server", "2.1", "published"),
  ]);
  readReplies(w.get, {
    "/api/v1/catalog/resources": { items, nextCursor: "browse-next" },
  });
  render(<Explore {...w.props} />);
  await screen.findByRole("link", { name: "Tool 10" });
  expect(labels("Local browse results")).toEqual([
    "Tool 10",
    "Tool 2",
    "Tool 1",
  ]);
  for (const [column, ascending, descending] of [
    [
      "listing",
      ["Tool 1", "Tool 2", "Tool 10"],
      ["Tool 10", "Tool 2", "Tool 1"],
    ],
    ["kind", ["Tool 10", "Tool 1", "Tool 2"], ["Tool 2", "Tool 1", "Tool 10"]],
    [
      "version",
      ["Tool 1", "Tool 2", "Tool 10"],
      ["Tool 10", "Tool 2", "Tool 1"],
    ],
    ["state", ["Tool 2", "Tool 10", "Tool 1"], ["Tool 10", "Tool 1", "Tool 2"]],
  ] as const) {
    click(`Sort by ${column}, ascending`);
    expect(labels("Local browse results")).toEqual(ascending);
    click(`Sort by ${column}, descending`);
    expect(labels("Local browse results")).toEqual(descending);
  }
  expect(w.get).toHaveBeenCalledTimes(1);
  expect(w.post).not.toHaveBeenCalled();
  expect(items.map((item) => item.release.id)).toEqual(["r10", "r2", "r1"]);
  click("Sort by listing, ascending");
  w.get.mockResolvedValueOnce(
    etagReply({
      items: [record("r9", "Tool 9", "plugin", "1.0", "published")],
    }),
  );
  click("Next page");
  await screen.findByRole("link", { name: "Tool 9" });
  expect(labels("Local browse results")).toEqual(["Tool 9"]);
  expect(w.get).toHaveBeenLastCalledWith(
    "/api/v1/catalog/resources",
    expect.objectContaining({
      params: { query: { limit: 25, cursor: "browse-next" } },
    }),
  );
  expect(
    screen.getByRole("link", { name: "Tool 9" }).getAttribute("href"),
  ).toBe("/unit/catalog/r9");
  expect(
    screen
      .getByRole("columnheader", { name: "Listing" })
      .getAttribute("aria-sort"),
  ).toBe("ascending");
});

it("sorts namespace names and slugs independently, retaining ties, filter drafts and selected order after refresh", async () => {
  const w = screenWorkspace();
  const items = Object.freeze([
    { ...namespace, id: "n10", displayName: "Tools 10", slug: "slug-2" },
    { ...namespace, id: "n2", displayName: "Tools 2", slug: "slug-10" },
    { ...namespace, id: "n1", displayName: "TOOLS 2", slug: "slug-1" },
  ]);
  readReplies(w.get, { "/api/v1/namespaces": { items } });
  render(<Namespaces {...w.props} access={administrator} />);
  await screen.findByRole("link", { name: "Tools 10" });
  change("Exact display name", "Unsent name");
  for (const [column, ascending, descending] of [
    [
      "name",
      ["Tools 2", "TOOLS 2", "Tools 10"],
      ["Tools 10", "Tools 2", "TOOLS 2"],
    ],
    [
      "slug",
      ["TOOLS 2", "Tools 10", "Tools 2"],
      ["Tools 2", "Tools 10", "TOOLS 2"],
    ],
  ] as const) {
    click(`Sort by ${column}, ascending`);
    expect(labels("Namespaces")).toEqual(ascending);
    click(`Sort by ${column}, descending`);
    expect(labels("Namespaces")).toEqual(descending);
  }
  expect(w.get).toHaveBeenCalledTimes(1);
  expect(screen.getByLabelText("Exact display name")).toHaveProperty(
    "value",
    "Unsent name",
  );
  expect(
    within(
      screen.getByRole("columnheader", { name: "Description" }),
    ).queryByRole("button"),
  ).toBeNull();
  expect(
    screen.getByRole("link", { name: "TOOLS 2" }).getAttribute("href"),
  ).toBe("/unit/namespaces/n1");
  click("Refresh");
  await waitFor(() => expect(w.get).toHaveBeenCalledTimes(2));
  await screen.findByRole("link", { name: "Tools 10" });
  expect(labels("Namespaces")).toEqual(["Tools 2", "Tools 10", "TOOLS 2"]);
  expect(w.get).toHaveBeenLastCalledWith(
    "/api/v1/namespaces",
    expect.objectContaining({ params: { query: { limit: 25 } } }),
  );
  expect(w.post).not.toHaveBeenCalled();
});

it("sorts publisher domain counts numerically, shows their names and preserves stable ties", async () => {
  const w = screenWorkspace();
  const domains = Array.from({ length: 10 }, (_, i) => `domain-${i}.test`);
  const items = Object.freeze([
    {
      ...publisher,
      id: "p10",
      displayName: "Publisher 10",
      domains,
      approvedPublicationDomains: domains,
    },
    {
      ...publisher,
      id: "p2",
      displayName: "Publisher 2",
      domains: ["a.test", "b.test"],
      approvedPublicationDomains: [],
    },
    {
      ...publisher,
      id: "p1",
      displayName: "Publisher 1",
      domains: ["c.test", "d.test"],
      approvedPublicationDomains: ["c.test", "d.test"],
    },
  ]);
  readReplies(w.get, { "/api/v1/publishers": { items } });
  render(<Publishers {...w.props} access={administrator} />);
  await screen.findByRole("link", { name: "Publisher 10" });
  expect(labels("Publishers")).toEqual([
    "Publisher 10",
    "Publisher 2",
    "Publisher 1",
  ]);
  expect(screen.getAllByText("10 domains")).toHaveLength(2);
  expect(screen.getAllByText(domains.join(", "))).toHaveLength(2);
  for (const [column, ascending, descending] of [
    [
      "name",
      ["Publisher 1", "Publisher 2", "Publisher 10"],
      ["Publisher 10", "Publisher 2", "Publisher 1"],
    ],
    [
      "declared domains",
      ["Publisher 2", "Publisher 1", "Publisher 10"],
      ["Publisher 10", "Publisher 2", "Publisher 1"],
    ],
    [
      "approved domains",
      ["Publisher 2", "Publisher 1", "Publisher 10"],
      ["Publisher 10", "Publisher 1", "Publisher 2"],
    ],
  ] as const) {
    click(`Sort by ${column}, ascending`);
    expect(labels("Publishers")).toEqual(ascending);
    click(`Sort by ${column}, descending`);
    expect(labels("Publishers")).toEqual(descending);
  }
  expect(w.get).toHaveBeenCalledTimes(1);
  expect(w.post).not.toHaveBeenCalled();
  expect(w.patch).not.toHaveBeenCalled();
  expect(w.remove).not.toHaveBeenCalled();
});

it("sorts domain approvals without losing drafts or changing the captured command target", async () => {
  const w = screenWorkspace();
  const item = {
    ...publisher,
    domains: Object.freeze(["host-10.test", "host-2.test", "host-1.test"]),
    approvedPublicationDomains: ["host-10.test", "host-1.test"],
  };
  readReplies(w.get, { "/api/v1/publishers/{publisherId}": item });
  render(<Publishers {...w.props} access={administrator} id={publisher.id} />);
  await screen.findByRole("region", { name: "Publisher domains" });
  change("Publication approval reason", "Approve the selected domain");
  change("Display name", "Unsent publisher name");
  for (const [column, ascending, descending] of [
    [
      "domain",
      ["host-1.test", "host-2.test", "host-10.test"],
      ["host-10.test", "host-2.test", "host-1.test"],
    ],
    [
      "publication approval",
      ["host-10.test", "host-1.test", "host-2.test"],
      ["host-2.test", "host-10.test", "host-1.test"],
    ],
  ] as const) {
    click(`Sort by ${column}, ascending`);
    expect(labels("Publisher domains")).toEqual(ascending);
    click(`Sort by ${column}, descending`);
    expect(labels("Publisher domains")).toEqual(descending);
  }
  const row = within(
    screen.getByRole("region", { name: "Publisher domains" }),
  ).getByRole("row", { name: /host-2.test/ });
  expect(
    within(row).getByRole("button", { name: "Approve domain" }),
  ).toHaveProperty("disabled", true);
  expect(screen.getByLabelText("Display name")).toHaveProperty(
    "value",
    "Unsent publisher name",
  );
  expect(screen.getByLabelText("Publication approval reason")).toHaveProperty(
    "value",
    "Approve the selected domain",
  );
  expect(w.get).toHaveBeenCalledTimes(1);
  expect(w.post).not.toHaveBeenCalled();
  change("Display name", publisher.displayName);
  w.post.mockResolvedValue(
    etagReply({
      ...item,
      approvedPublicationDomains: [
        ...item.approvedPublicationDomains,
        "host-2.test",
      ],
    }),
  );
  fireEvent.click(within(row).getByRole("button", { name: "Approve domain" }));
  const dialog = await screen.findByRole("dialog");
  expect(within(dialog).getByText("host-2.test", { exact: true })).toBeTruthy();
  await act(async () => {
    fireEvent.click(within(dialog).getByRole("button", { name: "Confirm" }));
  });
  expect(w.post).toHaveBeenCalledExactlyOnceWith(
    "/api/v1/publishers/{publisherId}/publication-domains/{domain}:approve",
    expect.objectContaining({
      params: {
        path: { publisherId: publisher.id, domain: "host-2.test" },
        header: {
          "If-Match": '"revision-1"',
          "Idempotency-Key": expect.any(String) as unknown,
        },
      },
      body: { reason: "Approve the selected domain" },
    }),
  );
  expect(item.domains).toEqual(["host-10.test", "host-2.test", "host-1.test"]);
});

it("keeps domain sorting when Reload returns a newer Publisher revision", async () => {
  const w = screenWorkspace();
  w.get
    .mockResolvedValueOnce(
      etagReply({ ...publisher, domains: ["b.test", "a.test"] }),
    )
    .mockResolvedValueOnce(
      etagReply(
        { ...publisher, revision: 2, domains: ["c.test", "a.test"] },
        '"revision-2"',
      ),
    );
  render(<Publishers {...w.props} access={administrator} id={publisher.id} />);
  await screen.findByRole("region", { name: "Publisher domains" });
  click("Sort by domain, ascending");
  expect(labels("Publisher domains")).toEqual(["a.test", "b.test"]);
  click("Reload publisher");
  await screen.findByRole("row", { name: /c.test/ });
  expect(labels("Publisher domains")).toEqual(["a.test", "c.test"]);
  expect(
    screen
      .getByRole("columnheader", { name: "Domain" })
      .getAttribute("aria-sort"),
  ).toBe("ascending");
  expect(w.get).toHaveBeenCalledTimes(2);
  expect(w.post).not.toHaveBeenCalled();
});
