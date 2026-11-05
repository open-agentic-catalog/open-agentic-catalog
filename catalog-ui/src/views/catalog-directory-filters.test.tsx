// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import Namespaces from "./Namespaces";
import Publishers from "./Publishers";
import Feedback from "./Feedback";
import Ratings from "./Ratings";
import Audit from "./Audit";
import { APIError } from "../api/client";
import { testAccess } from "../../tests/unit/access-fixtures";
import {
  administrator,
  change,
  click,
  etagReply,
  namespace,
  publisher,
  screenLifecycle,
  screenWorkspace,
} from "../../tests/unit/screens";

screenLifecycle("/unit/directory");

it.each([
  ["namespace", Namespaces, namespace, "slug", "Exact slug", "Tools"],
  [
    "publisher",
    Publishers,
    publisher,
    "declaredDomain",
    "Exact declared domain",
    "EXAMPLE.com.",
  ],
] as const)(
  "applies exact %s criteria only on submit, recovers a stale cursor and clears only this directory",
  async (kind, View, item, filter, label, key) => {
    const { props, get, post, patch, remove } = screenWorkspace();
    const queries: unknown[] = [];
    get.mockImplementation(async (_path, options) => {
      const query = (options as { params: { query: Record<string, unknown> } })
        .params.query;
      queries.push(query);
      if (query.cursor === "later") throw new APIError(400, "INVALID_ARGUMENT");
      return etagReply({ items: [item], nextCursor: "later" });
    });
    render(<View {...props} id="" access={administrator} />);
    await screen.findByRole("link", { name: item.displayName });
    expect(queries).toEqual([{ limit: 25 }]);
    change("Exact display name", " Mixed  Case ");
    change(label, key);
    expect(queries).toHaveLength(1);
    click("Apply filters");
    await waitFor(() =>
      expect(queries.at(-1)).toEqual({
        limit: 25,
        displayName: " Mixed  Case ",
        [filter]: key,
      }),
    );
    await waitFor(() =>
      expect(
        screen
          .getByRole("button", { name: "Next page" })
          .hasAttribute("disabled"),
      ).toBe(false),
    );
    const pager = screen.getByRole("group", {
      name: `${kind === "namespace" ? "Namespace" : "Publisher"} inventory pagination`,
    });
    fireEvent.click(within(pager).getByRole("button", { name: "Next page" }));
    await screen.findByRole("alert");
    expect(queries.at(-1)).toEqual({
      limit: 25,
      displayName: " Mixed  Case ",
      [filter]: key,
      cursor: "later",
    });
    expect(document.activeElement).toBe(pager);
    click("Reload");
    await waitFor(() =>
      expect(queries.at(-1)).toEqual({
        limit: 25,
        displayName: " Mixed  Case ",
        [filter]: key,
      }),
    );
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
    click("Refresh");
    await waitFor(() => expect(queries).toHaveLength(5));
    expect(queries.at(-1)).toEqual({
      limit: 25,
      displayName: " Mixed  Case ",
      [filter]: key,
    });
    click("Clear filters");
    await waitFor(() => expect(queries.at(-1)).toEqual({ limit: 25 }));
    expect(
      (
        screen.getByRole("textbox", {
          name: "Exact display name",
        }) as HTMLInputElement
      ).value,
    ).toBe("");
    expect(
      (screen.getByRole("textbox", { name: label }) as HTMLInputElement).value,
    ).toBe("");
    click("Apply filters");
    await waitFor(() => expect(queries).toHaveLength(7));
    expect(queries.at(-1)).toEqual({ limit: 25 });
    expect(post).not.toHaveBeenCalled();
    expect(patch).not.toHaveBeenCalled();
    expect(remove).not.toHaveBeenCalled();
  },
);

it.each([
  ["namespace", Namespaces, namespace],
  ["publisher", Publishers, publisher],
] as const)(
  "applying and clearing %s filters from later pages discards only the old continuation",
  async (kind, View, item) => {
    const { props, get, post, patch, remove } = screenWorkspace();
    const queries: Record<string, unknown>[] = [];
    const later = {
      ...item,
      id: "00000000-0000-7000-8000-000000000099",
      displayName: "Later directory item",
    };
    get.mockImplementation(async (_path, options) => {
      const query = (options as { params: { query: Record<string, unknown> } })
        .params.query;
      queries.push(query);
      return etagReply({
        items: [query.cursor || query.displayName ? later : item],
        ...(query.cursor
          ? {}
          : {
              nextCursor: query.displayName
                ? "filtered-next"
                : "unfiltered-next",
            }),
      });
    });
    render(<View {...props} id="" access={administrator} />);
    await screen.findByRole("link", { name: item.displayName });
    const pager = screen.getByRole("group", {
      name: `${kind === "namespace" ? "Namespace" : "Publisher"} inventory pagination`,
    });
    fireEvent.click(within(pager).getByRole("button", { name: "Next page" }));
    await screen.findByRole("link", { name: later.displayName });
    expect(queries).toEqual([
      { limit: 25 },
      { limit: 25, cursor: "unfiltered-next" },
    ]);
    change("Exact display name", " Exact  日本語 ");
    expect(queries).toHaveLength(2);
    click("Apply filters");
    await waitFor(() =>
      expect(queries.at(-1)).toEqual({
        limit: 25,
        displayName: " Exact  日本語 ",
      }),
    );
    await waitFor(() =>
      expect(
        within(pager)
          .getByRole("button", { name: "Next page" })
          .hasAttribute("disabled"),
      ).toBe(false),
    );
    fireEvent.click(within(pager).getByRole("button", { name: "Next page" }));
    await waitFor(() =>
      expect(queries.at(-1)).toEqual({
        limit: 25,
        displayName: " Exact  日本語 ",
        cursor: "filtered-next",
      }),
    );
    await waitFor(() =>
      expect(
        screen
          .getByRole("button", { name: "Clear filters" })
          .hasAttribute("disabled"),
      ).toBe(false),
    );
    click("Clear filters");
    await screen.findByRole("link", { name: item.displayName });
    expect(queries).toHaveLength(5);
    expect(queries.at(-1)).toEqual({ limit: 25 });
    expect(post).not.toHaveBeenCalled();
    expect(patch).not.toHaveBeenCalled();
    expect(remove).not.toHaveBeenCalled();
  },
);

it.each([
  [Feedback, "Feedback guide", "/docs/guides/feedback/"],
  [Ratings, "Ratings guide", "/docs/guides/ratings/"],
  [Audit, "Audit investigation guide", "/docs/operations/audit/"],
] as const)(
  "uses fixed bundled help, an exact external override, or no link on %s",
  (View, label, href) => {
    const { props, get } = screenWorkspace();
    const access = { ...administrator, ...testAccess([]) };
    const view = render(
      <View
        {...props}
        access={access}
        runtime={{ ...props.runtime, documentationUrl: "/docs/" }}
      />,
    );
    expect(screen.getByRole("link", { name: label }).getAttribute("href")).toBe(
      href,
    );
    view.rerender(
      <View
        {...props}
        access={access}
        runtime={{
          ...props.runtime,
          documentationUrl: "https://manual.example.test/private/#",
        }}
      />,
    );
    expect(
      screen.getByRole("link", { name: "Documentation" }).getAttribute("href"),
    ).toBe("https://manual.example.test/private/#");
    view.rerender(
      <View
        {...props}
        access={access}
        runtime={{ ...props.runtime, documentationUrl: "" }}
      />,
    );
    expect(
      screen.queryByRole("link", { name: /guide|Documentation/ }),
    ).toBeNull();
    expect(get).not.toHaveBeenCalled();
    vi.restoreAllMocks();
  },
);
