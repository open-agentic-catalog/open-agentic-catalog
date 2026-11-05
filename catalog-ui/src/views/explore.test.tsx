import { editValues } from "../../tests/unit/screens";
import { required } from "../../tests/required";
// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import type { components } from "../api/schema";
import Explore from "./Explore";
import { deferred, reply, workspace } from "../../tests/unit/fixtures";

afterEach(cleanup);

it("transfers only compatible applied browse/count filters and keeps each mode's draft", async () => {
  const { props, get } = workspace();
  const post = vi.fn().mockResolvedValue(reply({ matched: 0, facets: {} }));
  Object.assign(props.client.api, { POST: post });
  get.mockResolvedValue(reply({ items: [] }));
  render(<Explore {...props} />);
  await screen.findByText("No local releases on this page.");
  fireEvent.change(screen.getByLabelText("Kind"), {
    target: { value: "skill" },
  });
  fireEvent.change(screen.getByLabelText("Visibility"), {
    target: { value: "public" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Use these filters for counts" }),
  );
  await waitFor(() =>
    expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
      body: { query: { kinds: ["skill"], visibility: "public" } },
    }),
  );
  fireEvent.change(screen.getByLabelText("Search local metadata"), {
    target: { value: "unsubmitted" },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Use these filters for browse" }),
  );
  expect(screen.getByLabelText("Kind")).toHaveProperty("value", "skill");
  expect(screen.getByLabelText("Visibility")).toHaveProperty("value", "public");
  fireEvent.click(screen.getByRole("button", { name: "View counts" }));
  expect(screen.getByLabelText("Search local metadata")).toHaveProperty(
    "value",
    "unsubmitted",
  );
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(
    screen.getByRole("button", { name: "Use these filters for browse" }),
  ).toHaveProperty("disabled", true);
});

it("hands applied text/tag/category conjunctions to local Catalog search without submitting later edits", async () => {
  const { props, get } = workspace();
  const post = vi.fn().mockResolvedValue(reply({ matched: 0, facets: {} }));
  const open = vi.fn();
  Object.assign(props.client.api, { POST: post });
  get.mockResolvedValue(reply({ items: [] }));
  render(<Explore {...props} onCatalogSearch={open} />);
  fireEvent.click(screen.getByRole("button", { name: "View counts" }));
  await screen.findByText("0 matching local releases");
  fireEvent.change(screen.getByLabelText("Search local metadata"), {
    target: { value: "applied" },
  });
  editValues("Tags", ["one, literal", "two"]);
  editValues("Categories", ["a", "b"]);
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  fireEvent.change(screen.getByLabelText("Search local metadata"), {
    target: { value: "unsent secret draft" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Search local Catalog" }));
  expect(open).toHaveBeenCalledExactlyOnceWith({
    text: "applied",
    tags: ["one, literal", "two"],
    categories: ["a", "b"],
  });
  expect(
    screen.getByText(/Matching and ordering can differ from counts/),
  ).toBeTruthy();
  expect(
    screen.getByRole("button", { name: "Use these filters for browse" }),
  ).toHaveProperty("disabled", true);
});

it("retains separate browse and counts criteria and unsent drafts when switching modes", async () => {
  const { props, get } = workspace();
  const post = vi.fn().mockResolvedValue(reply({ matched: 0, facets: {} }));
  Object.assign(props.client.api, { POST: post });
  get.mockResolvedValue(reply({ items: [], nextCursor: "browse-next" }));
  render(<Explore {...props} />);
  await screen.findByText("No local releases on this page.");
  fireEvent.change(screen.getByLabelText("Kind"), {
    target: { value: "mcp_server" },
  });
  const nextPage = await screen.findByRole("button", { name: "Next page" });
  await waitFor(() => expect(nextPage).toHaveProperty("disabled", false));
  fireEvent.click(nextPage);
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { kind: "mcp_server", cursor: "browse-next" } },
    }),
  );
  fireEvent.click(screen.getByRole("button", { name: "View counts" }));
  await screen.findByText("0 matching local releases");
  fireEvent.change(screen.getByLabelText("Search local metadata"), {
    target: { value: "submitted count text" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  await waitFor(() =>
    expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
      body: { query: { text: "submitted count text" } },
    }),
  );
  fireEvent.change(screen.getByLabelText("Search local metadata"), {
    target: { value: "unsent draft" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Browse releases" }));
  expect(screen.getByLabelText("Kind")).toHaveProperty("value", "mcp_server");
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { cursor: "browse-next", kind: "mcp_server" } },
    }),
  );
  fireEvent.click(screen.getByRole("button", { name: "View counts" }));
  expect(screen.getByLabelText("Search local metadata")).toHaveProperty(
    "value",
    "unsent draft",
  );
  await waitFor(() =>
    expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
      body: { query: { text: "submitted count text" } },
    }),
  );
});

describe("local browse and facets", () => {
  it("names known facets without changing filter values, counts or unknown declarations", async () => {
    const { props, get } = workspace();
    const post = vi.fn();
    Object.assign(props.client.api, { POST: post });
    get.mockResolvedValue(reply({ items: [] }));
    post.mockResolvedValue(
      reply({
        matched: 4,
        facets: {
          kind: {
            buckets: [
              { value: "mcp_server", count: 4 },
              { value: "future_kind", count: 1 },
            ],
          },
          trustState: {
            buckets: [
              "not_assessed",
              "unsupported",
              "unverified",
              "verified",
              "failed",
              "expired",
              "future_assessment",
            ].map((value) => ({ value, count: 1 })),
          },
          visibility: {
            buckets: ["public", "private", "tenant", "future_visibility"].map(
              (value) => ({ value, count: 1 }),
            ),
          },
          "urn:example:facet": { buckets: [{ value: "__proto__", count: 1 }] },
        },
      }),
    );
    render(<Explore {...props} />);
    fireEvent.click(screen.getByRole("button", { name: "View counts" }));
    await screen.findByText("4 matching local releases");
    for (const title of [
      "Resource kind",
      "Trust assessment",
      "Visibility",
      "urn:example:facet",
    ])
      expect(screen.getByRole("heading", { name: title })).toBeTruthy();
    const assessment = within(
      screen
        .getByRole("heading", { name: "Trust assessment" })
        .closest("section")!,
    );
    for (const label of [
      "Not assessed",
      "Unsupported",
      "Unverified",
      "Reported verified",
      "Failed",
      "Expired",
      "future_assessment",
    ])
      expect(assessment.getByText(label, { exact: true })).toBeTruthy();
    expect(screen.getByText("__proto__", { exact: true })).toBeTruthy();
    expect(
      screen.getByRole("button", {
        name: "Filter by resource kind future_kind",
      }),
    ).toHaveProperty("disabled", true);
    expect(
      screen.getByRole("button", {
        name: "Filter by visibility future_visibility",
      }),
    ).toHaveProperty("disabled", true);
    const kind = screen.getByRole("button", {
      name: "Filter by resource kind MCP Server",
    });
    expect(within(kind).getByText("4", { exact: true })).toBeTruthy();
    for (const summary of [
      "Tags and categories",
      "Publisher, license and visibility",
    ]) {
      const disclosure = screen.getByText(summary).closest("details")!;
      expect(disclosure.open).toBe(false);
      fireEvent.click(screen.getByText(summary));
      expect(disclosure.open).toBe(true);
    }
    expect(post).toHaveBeenCalledOnce();
    expect(get).toHaveBeenCalledOnce();
    fireEvent.click(kind);
    await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
    expect(post.mock.calls[1]?.[1]).toMatchObject({
      body: { query: { kinds: ["mcp_server"] } },
    });
    expect(get).toHaveBeenCalledOnce();
  });

  it("pages browse results and clears the cursor when changing either filter", async () => {
    const { props, get } = workspace();
    const first = {
      items: [
        {
          release: { id: "release-1", state: "published" },
          listingRevision: { displayName: "Local agent" },
          resource: { kind: "agent" },
          version: { version: "1.0.0" },
        },
      ],
      nextCursor: "page-2",
    };
    get.mockImplementation(async (_path, options) =>
      reply(
        (options as { params: { query: { cursor?: string } } }).params.query
          .cursor
          ? { items: [] }
          : first,
      ),
    );
    render(<Explore {...props} />);
    const link = await screen.findByRole("link", { name: "Local agent" });
    for (const [label, value] of [
      ["Agents", "agent"],
      ["MCP Servers", "mcp_server"],
      ["Skills", "skill"],
      ["Plugins", "plugin"],
    ] as const) {
      expect(screen.getByRole("option", { name: label })).toHaveProperty(
        "value",
        value,
      );
    }
    expect(screen.getByRole("cell", { name: "Agent" })).toBeTruthy();
    expect(link.getAttribute("href")).toBe("/unit/catalog/release-1");
    fireEvent.click(screen.getByRole("button", { name: "Next page" }));
    await screen.findByText("No local releases on this page.");
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { limit: 25, cursor: "page-2" } },
    });
    fireEvent.change(screen.getByLabelText("Kind"), {
      target: { value: "skill" },
    });
    await screen.findByRole("link", { name: "Local agent" });
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { kind: "skill" } },
    });
    expect(get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
      "params.query.cursor",
    );
    fireEvent.click(screen.getByRole("button", { name: "Next page" }));
    await screen.findByText("No local releases on this page.");
    fireEvent.change(screen.getByLabelText("Visibility"), {
      target: { value: "tenant" },
    });
    await screen.findByRole("link", { name: "Local agent" });
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: {
        query: { kind: "skill", visibility: "tenant" },
      },
    });
    expect(get.mock.calls.at(-1)?.[1]).not.toHaveProperty(
      "params.query.cursor",
    );
  });

  it("submits normalized local facet filters and restores browse without replaying drafts", async () => {
    const { props, get } = workspace();
    get.mockResolvedValue(reply({ items: [] }));
    const post = vi
      .fn<
        (
          path: string,
          options: {
            body: components["schemas"]["CatalogExplore"];
            signal: AbortSignal;
          },
        ) => Promise<unknown>
      >()
      .mockResolvedValue(
        reply({
          matched: 3,
          facets: {
            kind: { buckets: [{ value: "skill", count: 1 }], otherCount: 2 },
            tag: { buckets: [] },
          },
        }),
      );
    Object.assign(props.client.api, { POST: post });
    render(<Explore {...props} />);
    await screen.findByText("No local releases on this page.");
    fireEvent.click(screen.getByRole("button", { name: "View counts" }));
    await screen.findByText("3 matching local releases");
    expect(
      screen.getByText(
        /2 omitted bucket memberships \(not distinct releases\)/,
      ),
    ).toBeTruthy();
    expect(screen.getByText("No matching values.")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Search local metadata"), {
      target: { value: "order" },
    });
    fireEvent.click(screen.getByRole("checkbox", { name: "Skills" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "Agents" }));
    fireEvent.change(screen.getByLabelText("Visibility"), {
      target: { value: "public" },
    });
    editValues("Tags", ["tools", "review"]);
    fireEvent.change(screen.getByLabelText("License declaration"), {
      target: { value: " MIT " },
    });
    editValues("Categories", ["tools", "finance"]);
    fireEvent.change(screen.getByLabelText("Publisher ID (optional)"), {
      target: { value: "publisher-1" },
    });
    expect(post).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
    expect(post.mock.calls[1]).toMatchObject([
      "/api/v1/catalog/explore",
      {
        body: {
          query: {
            text: "order",
            kinds: ["skill", "agent"],
            visibility: "public",
            publisher: "publisher-1",
            tags: ["tools", "review"],
            license: "MIT",
            categories: ["tools", "finance"],
          },
        },
      },
    ]);
    expect(required(post.mock.calls[1])[1].body.facets).toHaveLength(8);
    fireEvent.change(screen.getByLabelText("Search local metadata"), {
      target: { value: "unsubmitted" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Remove resource kind Agent" }),
    );
    await waitFor(() => expect(post).toHaveBeenCalledTimes(3));
    expect(required(post.mock.calls[2])[1].body.query).toMatchObject({
      text: "order",
      kinds: ["skill"],
      tags: ["tools", "review"],
      visibility: "public",
      publisher: "publisher-1",
      license: "MIT",
      categories: ["tools", "finance"],
    });
    expect(screen.getByRole("checkbox", { name: "Agents" })).toHaveProperty(
      "checked",
      false,
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Remove resource kind Skill" }),
    );
    await waitFor(() => expect(post).toHaveBeenCalledTimes(4));
    expect(
      required(required(post.mock.calls[3])[1].body.query).kinds,
    ).toBeUndefined();
    for (const name of [
      "Search local metadata",
      "Visibility",
      "License declaration",
      "Publisher ID (optional)",
    ])
      fireEvent.change(screen.getByLabelText(name), { target: { value: "" } });
    editValues("Tags", []);
    editValues("Categories", []);
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(5));
    expect(required(post.mock.calls[4])[1].body.query).toEqual({ text: "" });
    fireEvent.click(screen.getByRole("checkbox", { name: "Plugins" }));
    editValues("Tags", ["draft"]);
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(6));
    expect(required(post.mock.calls[5])[1].body.query).toEqual({});
    expect(screen.getByRole("checkbox", { name: "Plugins" })).toHaveProperty(
      "checked",
      false,
    );
    expect(screen.queryByLabelText("Tag 1")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Browse releases" }));
    await screen.findByText("No local releases on this page.");
    expect((screen.getByLabelText("Kind") as HTMLSelectElement).value).toBe("");
    expect(
      (screen.getByLabelText("Visibility") as HTMLSelectElement).value,
    ).toBe("");
  });

  it("uses only supported buckets, preserves unsubmitted text and removes every applied criterion", async () => {
    const { props, get } = workspace();
    get.mockResolvedValue(reply({ items: [] }));
    const post = vi
      .fn<
        (
          path: string,
          options: {
            body: components["schemas"]["CatalogExplore"];
            signal: AbortSignal;
          },
        ) => Promise<unknown>
      >()
      .mockResolvedValue(
        reply({
          matched: 2,
          facets: Object.fromEntries(
            (
              [
                ["kind", "agent"],
                ["tag", "tools"],
                ["publisher", "publisher-1"],
                ["visibility", "public"],
                ["category", "development"],
                ["namespace", "namespace-1"],
                ["trustState", "not_assessed"],
                ["source", "local"],
              ] as const
            ).map(
              ([field, value]) =>
                [
                  field,
                  {
                    buckets: [
                      { value, count: 2 },
                      ...(field === "tag"
                        ? [{ value: "x".repeat(81), count: 1 }]
                        : []),
                    ],
                  },
                ] as const,
            ),
          ),
        }),
      );
    Object.assign(props.client.api, { POST: post });
    render(<Explore {...props} />);
    fireEvent.click(screen.getByRole("button", { name: "View counts" }));
    await screen.findByText("2 matching local releases");
    expect(
      screen.getByRole("button", { name: `Filter by tag ${"x".repeat(81)}` }),
    ).toHaveProperty("disabled", true);
    const unavailable = screen.getByRole("button", {
      name: `Filter by tag ${"x".repeat(81)}`,
    });
    const explanation = document.getElementById(
      unavailable.getAttribute("aria-describedby")!,
    );
    expect(explanation?.textContent).toBe(
      "Unavailable values exceed the supported filter limits.",
    );
    for (const name of ["trustState", "source"])
      expect(
        screen.queryByRole("button", { name: new RegExp(`Filter by ${name}`) }),
      ).toBeNull();
    expect(
      screen.getByRole("button", { name: /Filter by local namespace/ }),
    ).toHaveProperty("disabled", true);
    fireEvent.change(screen.getByLabelText("Search local metadata"), {
      target: { value: "submitted" },
    });
    fireEvent.change(screen.getByLabelText("License declaration"), {
      target: { value: "MIT" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
    fireEvent.change(screen.getByLabelText("Search local metadata"), {
      target: { value: "draft" },
    });
    let calls = 2;
    for (const name of [
      "resource kind Agent",
      "tag tools",
      "category development",
      "publisher publisher-1",
      "visibility Public",
    ]) {
      fireEvent.click(
        await screen.findByRole("button", { name: `Filter by ${name}` }),
      );
      calls++;
      await waitFor(() => expect(post).toHaveBeenCalledTimes(calls));
      const selected = await screen.findByRole("button", {
        name: `Filter by ${name}`,
      });
      expect(selected.getAttribute("aria-pressed")).toBe("true");
      fireEvent.click(selected);
      expect(post).toHaveBeenCalledTimes(calls);
      expect(document.activeElement).toBe(
        screen.getByLabelText("Search local metadata"),
      );
    }
    expect(post.mock.calls.at(-1)?.[1].body.query).toEqual({
      text: "submitted",
      license: "MIT",
      kinds: ["agent"],
      tags: ["tools"],
      categories: ["development"],
      publisher: "publisher-1",
      visibility: "public",
    });
    expect(screen.getByLabelText("Search local metadata")).toHaveProperty(
      "value",
      "draft",
    );
    for (const name of [
      "resource kind Agent",
      "tag tools",
      "category development",
      "license MIT",
      "publisher publisher-1",
      "visibility Public",
      "text submitted",
    ]) {
      fireEvent.click(screen.getByRole("button", { name: `Remove ${name}` }));
      calls++;
      await waitFor(() => expect(post).toHaveBeenCalledTimes(calls));
    }
    expect(post.mock.calls.at(-1)?.[1].body.query).toEqual({});
    expect(screen.getByLabelText("Search local metadata")).toHaveProperty(
      "value",
      "",
    );
  });

  it.each(["tag", "category"] as const)(
    "preserves a literal comma %s when applying unrelated drafts and removing another value",
    async (field) => {
      const terms = field === "tag" ? "tags" : "categories";
      const { props, get } = workspace();
      get.mockResolvedValue(reply({ items: [] }));
      const post = vi
        .fn<
          (
            path: string,
            options: {
              body: components["schemas"]["CatalogExplore"];
              signal: AbortSignal;
            },
          ) => Promise<unknown>
        >()
        .mockResolvedValue(
          reply({
            matched: 1,
            facets: {
              [field]: {
                buckets: [
                  { value: "design, review", count: 1 },
                  { value: "tools", count: 1 },
                ],
              },
            },
          }),
        );
      Object.assign(props.client.api, { POST: post });
      render(<Explore {...props} />);
      fireEvent.click(screen.getByRole("button", { name: "View counts" }));
      fireEvent.click(
        await screen.findByRole("button", {
          name: `Filter by ${field} design, review`,
        }),
      );
      await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
      expect(
        required(required(post.mock.calls.at(-1))[1].body.query)[terms],
      ).toEqual(["design, review"]);
      fireEvent.change(screen.getByLabelText("Search local metadata"), {
        target: { value: "new text" },
      });
      fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
      await waitFor(() => expect(post).toHaveBeenCalledTimes(3));
      expect(post.mock.calls.at(-1)?.[1].body.query).toEqual({
        text: "new text",
        [terms]: ["design, review"],
      });
      fireEvent.click(
        await screen.findByRole("button", { name: `Filter by ${field} tools` }),
      );
      await waitFor(() => expect(post).toHaveBeenCalledTimes(4));
      expect(
        required(required(post.mock.calls.at(-1))[1].body.query)[terms],
      ).toEqual(["design, review", "tools"]);
      fireEvent.click(
        screen.getByRole("button", { name: `Remove ${field} tools` }),
      );
      await waitFor(() => expect(post).toHaveBeenCalledTimes(5));
      expect(
        required(required(post.mock.calls.at(-1))[1].body.query)[terms],
      ).toEqual(["design, review"]);
      fireEvent.change(
        screen.getByLabelText(field === "tag" ? "Tag 1" : "Category 1"),
        {
          target: { value: "replacement, tools" },
        },
      );
      fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
      await waitFor(() => expect(post).toHaveBeenCalledTimes(6));
      expect(post.mock.calls.at(-1)?.[1].body.query).toEqual({
        text: "new text",
        [terms]: ["replacement, tools"],
      });
    },
  );

  it("fences old counts on query and identity changes, and recovers from errors", async () => {
    const { props, get } = workspace();
    get.mockResolvedValue(reply({ items: [] }));
    const old = deferred<ReturnType<typeof reply>>();
    const next = deferred<ReturnType<typeof reply>>();
    const post = vi
      .fn<
        (
          path: string,
          options: {
            body: components["schemas"]["CatalogExplore"];
            signal: AbortSignal;
          },
        ) => Promise<unknown>
      >()
      .mockReturnValueOnce(old.promise)
      .mockReturnValueOnce(next.promise)
      .mockRejectedValueOnce(new Error("unavailable"))
      .mockResolvedValue(reply({ matched: 0, facets: {} }));
    Object.assign(props.client.api, { POST: post });
    const view = render(<Explore {...props} />);
    fireEvent.click(screen.getByRole("button", { name: "View counts" }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    fireEvent.change(screen.getByLabelText("Search local metadata"), {
      target: { value: "new" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
    expect(required(post.mock.calls[0])[1].signal.aborted).toBe(true);
    await act(async () => {
      old.resolve(reply({ matched: 999, facets: {} }));
    });
    expect(screen.queryByText("999 matching local releases")).toBeNull();
    await act(async () => {
      next.resolve(reply({ matched: 1, facets: {} }));
    });
    await screen.findByText("1 matching local release");
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    await screen.findByRole("alert");
    expect(screen.queryByText("1 matching local release")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    await screen.findByText("0 matching local releases");
    const pending = deferred<ReturnType<typeof reply>>();
    post.mockReturnValueOnce(pending.promise);
    fireEvent.change(screen.getByLabelText("Search local metadata"), {
      target: { value: "private draft" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(5));
    view.rerender(
      <Explore {...props} access={{ ...props.access, tenantId: "tenant-b" }} />,
    );
    await waitFor(() => expect(post).toHaveBeenCalledTimes(6));
    expect(required(post.mock.calls[4])[1].signal.aborted).toBe(true);
    expect(required(post.mock.calls[5])[1].body.query).toEqual({});
    expect(screen.getByLabelText("Search local metadata")).toHaveProperty(
      "value",
      "",
    );
    await act(async () => {
      pending.resolve(reply({ matched: 444, facets: {} }));
    });
    expect(screen.queryByText("444 matching local releases")).toBeNull();
    await screen.findByText("0 matching local releases");
  });
});

it("applies and clears a local namespace consistently in browse and facets", async () => {
  const { props, get } = workspace();
  const id = "00000000-0000-7000-8000-000000000001";
  const namespaces = {
    nextCursor: "later-namespaces",
    items: [
      {
        id,
        tenantId: "tenant-a",
        slug: "demo",
        displayName: "Demo",
        revision: 1,
        createdAt: "2026-09-01T12:00:00Z",
        updatedAt: "2026-09-01T12:00:00Z",
      },
    ],
  };
  get.mockImplementation(async (path, options) => {
    const request = options as { params?: { query?: { cursor?: string } } };
    return reply(
      path === "/api/v1/namespaces" && !request.params?.query?.cursor
        ? namespaces
        : { items: [] },
    );
  });
  const post = vi.fn(async () =>
    reply({
      matched: 1,
      facets: { namespace: { buckets: [{ value: id, count: 1 }] } },
    }),
  );
  Object.assign(props.client.api, { POST: post });
  render(<Explore {...props} />);
  fireEvent.click(screen.getByRole("button", { name: "Load namespaces" }));
  await screen.findByRole("option", { name: "Demo" });
  fireEvent.change(screen.getByLabelText("Local namespace"), {
    target: { value: id },
  });
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { namespaceId: id, limit: 25 } },
    }),
  );
  fireEvent.change(screen.getByLabelText("Local namespace"), {
    target: { value: "" },
  });
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { limit: 25 } },
    }),
  );
  fireEvent.click(screen.getByRole("button", { name: "View counts" }));
  await screen.findByRole("button", {
    name: `Filter by local namespace ${id}`,
  });
  fireEvent.click(
    screen.getByRole("button", { name: `Filter by local namespace ${id}` }),
  );
  await screen.findByRole("button", { name: `Remove local namespace ${id}` });
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(3));
  fireEvent.click(
    screen.getByRole("button", { name: `Remove local namespace ${id}` }),
  );
  await waitFor(() => expect(post).toHaveBeenCalledTimes(4));
  fireEvent.click(screen.getByRole("button", { name: "Load namespaces" }));
  await screen.findByRole("option", { name: "Demo" });
  await screen.findByRole("button", { name: "Filter by local namespace Demo" });
  expect(post).toHaveBeenCalledTimes(4);
  expect(
    get.mock.calls.filter((call) => call[0] === "/api/v1/namespaces"),
  ).toHaveLength(2);
  fireEvent.click(screen.getByRole("button", { name: "Next namespace page" }));
  await waitFor(() =>
    expect(
      get.mock.calls.filter((call) => call[0] === "/api/v1/namespaces"),
    ).toHaveLength(3),
  );
  await waitFor(() =>
    expect(
      screen.queryByRole("button", { name: "Next namespace page" }),
    ).toBeNull(),
  );
  expect(
    screen.getByRole("button", { name: "Filter by local namespace Demo" }),
  ).toBeTruthy();
  expect(post).toHaveBeenCalledTimes(4);
  fireEvent.click(screen.getByRole("button", { name: "First namespace page" }));
  await screen.findByRole("option", { name: "Demo" });
  fireEvent.change(screen.getByLabelText("Local namespace"), {
    target: { value: id },
  });
  await waitFor(() => expect(post).toHaveBeenCalledTimes(5));
  fireEvent.change(screen.getByLabelText("Local namespace"), {
    target: { value: "" },
  });
  await waitFor(() => expect(post).toHaveBeenCalledTimes(6));
});

it("refreshes successful browse and facets once, preserving staged form edits through failed refresh recovery", async () => {
  const { props, get } = workspace();
  get.mockResolvedValue(reply({ items: [] }));
  const post = vi.fn().mockResolvedValue(reply({ matched: 3, facets: {} }));
  Object.assign(props.client.api, { POST: post });
  render(<Explore {...props} />);
  await screen.findByText("No local releases on this page.");
  const before = get.mock.calls.length;
  fireEvent.click(
    screen.getByRole("button", { name: "Refresh browse results" }),
  );
  await waitFor(() => expect(get).toHaveBeenCalledTimes(before + 1));
  fireEvent.click(screen.getByRole("button", { name: "View counts" }));
  await screen.findByText("3 matching local releases");
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  post.mockResolvedValueOnce(reply({ matched: 4, facets: {} }));
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  await screen.findByText("4 matching local releases");
  expect(post).toHaveBeenCalledTimes(3);
  fireEvent.change(screen.getByLabelText("Search local metadata"), {
    target: { value: "unapplied draft" },
  });
  post.mockRejectedValueOnce(new Error("private failure"));
  fireEvent.click(screen.getByRole("button", { name: "Refresh counts" }));
  await screen.findByRole("alert");
  expect(post).toHaveBeenCalledTimes(4);
  expect(post.mock.calls[3]?.[1]).toMatchObject({
    body: { query: { text: "" } },
  });
  expect(screen.getByLabelText("Search local metadata")).toHaveProperty(
    "value",
    "unapplied draft",
  );
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  await screen.findByText("3 matching local releases");
  expect(post).toHaveBeenCalledTimes(5);
  expect(screen.getByLabelText("Search local metadata")).toHaveProperty(
    "value",
    "unapplied draft",
  );
});

it("loads namespace choices while counts are pending and without a returned namespace facet", async () => {
  const { props, get } = workspace();
  const waiting = deferred<ReturnType<typeof reply>>();
  const post = vi.fn().mockReturnValueOnce(waiting.promise);
  Object.assign(props.client.api, { POST: post });
  const item = {
    id: "00000000-0000-7000-8000-000000000001",
    tenantId: "tenant-a",
    slug: "demo",
    displayName: "Demo",
    revision: 1,
    createdAt: "2026-09-01T12:00:00Z",
    updatedAt: "2026-09-01T12:00:00Z",
  };
  get.mockImplementation(async (path) =>
    reply(path === "/api/v1/namespaces" ? { items: [item] } : { items: [] }),
  );
  render(<Explore {...props} />);
  fireEvent.click(screen.getByRole("button", { name: "View counts" }));
  fireEvent.click(screen.getByRole("button", { name: "Load namespaces" }));
  await screen.findByRole("option", { name: "Demo" });
  expect(post).toHaveBeenCalledOnce();
  await act(async () => waiting.resolve(reply({ matched: 0, facets: {} })));
  await screen.findByText("0 matching local releases");
  fireEvent.click(screen.getByRole("button", { name: "First namespace page" }));
  await waitFor(() =>
    expect(
      get.mock.calls.filter((call) => call[0] === "/api/v1/namespaces"),
    ).toHaveLength(2),
  );
  expect(screen.getByRole("option", { name: "Demo" })).toBeTruthy();
  expect(post).toHaveBeenCalledOnce();
});
it.each([
  ["Search local metadata", "🙂".repeat(126)],
  ["Search local metadata", "a\0b"],
  ["Publisher ID (optional)", "é".repeat(129)],
  ["Publisher ID (optional)", "a\0b"],
  ["License declaration", "🙂".repeat(257)],
  ["License declaration", "a\0b"],
  ["Category 1", "🙂".repeat(81)],
  ["Tag 1", "🙂".repeat(81)],
])(
  "rejects invalid facet %s before a request and focuses its preserved input",
  async (label, value) => {
    const { props, get } = workspace();
    get.mockResolvedValue(reply({ items: [] }));
    const post = vi.fn().mockResolvedValue(reply({ matched: 0, facets: {} }));
    Object.assign(props.client.api, { POST: post });
    render(<Explore {...props} />);
    await screen.findByText("No local releases on this page.");
    fireEvent.click(screen.getByRole("button", { name: "View counts" }));
    await screen.findByText("0 matching local releases");
    if (label === "Category 1") editValues("Categories", [value]);
    if (label === "Tag 1") editValues("Tags", [value]);
    const field = screen.getByLabelText(label);
    const disclosure = field.closest("details");
    if (disclosure && !disclosure.open)
      fireEvent.click(disclosure.querySelector("summary")!);
    fireEvent.change(field, { target: { value } });
    if (disclosure) {
      fireEvent.click(disclosure.querySelector("summary")!);
      expect(disclosure.open).toBe(false);
    }
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await screen.findByRole("alert");
    expect(document.activeElement).toBe(field);
    if (field.closest("details"))
      expect(field.closest("details")).toHaveProperty("open", true);
    expect(field).toHaveProperty("value", value);
    expect(post).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(screen.queryByRole("alert")).toBeNull();
  },
);

it("opens a collapsed filter section when native required-field validation rejects its empty row", async () => {
  const { props, get } = workspace();
  const post = vi.fn();
  Object.assign(props.client.api, { POST: post });
  get.mockResolvedValue(reply({ items: [] }));
  post.mockResolvedValue(reply({ matched: 0, facets: {} }));
  render(<Explore {...props} />);
  fireEvent.click(screen.getByRole("button", { name: "View counts" }));
  await screen.findByText("0 matching local releases");
  const summary = screen.getByText("Tags and categories");
  fireEvent.click(summary);
  fireEvent.click(screen.getByRole("button", { name: "Add tag" }));
  const input = screen.getByLabelText("Tag 1");
  expect(input).toHaveProperty("required", true);
  fireEvent.click(summary);
  expect(summary.closest("details")).toHaveProperty("open", false);
  fireEvent.invalid(input);
  expect(summary.closest("details")).toHaveProperty("open", true);
  expect(post).toHaveBeenCalledOnce();
});

it("explicitly transfers empty criteria in both directions without retaining incompatible count fields", async () => {
  const { props, get } = workspace();
  const post = vi.fn().mockResolvedValue(reply({ matched: 0, facets: {} }));
  Object.assign(props.client.api, { POST: post });
  get.mockResolvedValue(reply({ items: [] }));
  render(<Explore {...props} />);
  await screen.findByText("No local releases on this page.");
  fireEvent.click(
    screen.getByRole("button", { name: "Use these filters for counts" }),
  );
  await screen.findByText("0 matching local releases");
  expect(post.mock.calls.at(-1)?.[1]).toMatchObject({ body: { query: {} } });
  fireEvent.click(
    screen.getByRole("button", { name: "Use these filters for browse" }),
  );
  expect(screen.getByLabelText("Kind")).toHaveProperty("value", "");
  expect(screen.getByLabelText("Visibility")).toHaveProperty("value", "");
});

it("explicitly transfers the applied local namespace in both directions", async () => {
  const { props, get } = workspace();
  const id = "00000000-0000-7000-8000-000000000001";
  get.mockImplementation(async (path) =>
    reply(
      path === "/api/v1/namespaces"
        ? {
            items: [
              {
                id,
                tenantId: "tenant-a",
                slug: "demo",
                displayName: "Demo",
                revision: 1,
                createdAt: "2026-09-01T12:00:00Z",
                updatedAt: "2026-09-01T12:00:00Z",
              },
            ],
          }
        : { items: [] },
    ),
  );
  const post = vi.fn().mockResolvedValue(reply({ matched: 0, facets: {} }));
  Object.assign(props.client.api, { POST: post });
  render(<Explore {...props} />);
  fireEvent.click(screen.getByRole("button", { name: "Load namespaces" }));
  await screen.findByRole("option", { name: "Demo" });
  fireEvent.change(screen.getByLabelText("Local namespace"), {
    target: { value: id },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Use these filters for counts" }),
  );
  await screen.findByText("0 matching local releases");
  expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
    body: { query: { namespaceId: id } },
  });
  fireEvent.click(
    screen.getByRole("button", { name: "Use these filters for browse" }),
  );
  await waitFor(() =>
    expect(get.mock.calls.at(-1)?.[1]).toMatchObject({
      params: { query: { namespaceId: id, limit: 25 } },
    }),
  );
});
