import { editValues } from "../../tests/unit/screens";
// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { workspace, reply, installDialog } from "../../tests/unit/fixtures";
import SearchFilters from "./SearchFilters";

let restoreDialog: () => void;
beforeEach(() => {
  restoreDialog = installDialog();
});
afterEach(() => {
  cleanup();
  restoreDialog();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

it("counts each applied origin and namespace rather than an unsubmitted draft", () => {
  const view = render(
    <SearchFilters
      value={{ origins: ["local", "indexed"], tags: ["applied"] }}
      tags={[]}
      publishers={[]}
      onApply={vi.fn()}
    />,
  );
  const button = screen.getByRole("button", { name: /Filters/ });
  expect(button.textContent).toContain("3");
  view.rerender(
    <SearchFilters
      value={{ origins: ["local"], namespaceId: "selected", tags: ["applied"] }}
      tags={[]}
      publishers={[]}
      onApply={vi.fn()}
    />,
  );
  expect(button.textContent).toContain("3");
  fireEvent.click(button);
  fireEvent.click(screen.getByRole("checkbox", { name: "Agents" }));
  expect(button.textContent).toContain("3");
});

it("stages multiple kinds and searches literal page suggestions without hiding selected tags", () => {
  const apply = vi.fn();
  const value = { kinds: ["agent" as const], tags: ["selected"] };
  render(
    <SearchFilters
      value={value}
      tags={["TOOLS", "a.b", "axb"]}
      publishers={[]}
      onApply={apply}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: /Filters/ }));
  fireEvent.click(screen.getByRole("checkbox", { name: "Skills" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "Agents" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "MCP Servers" }));
  const suggestions = screen.getByRole("searchbox", {
    name: "Search tag suggestions",
  });
  fireEvent.change(suggestions, { target: { value: "tools" } });
  expect(screen.getByRole("checkbox", { name: "TOOLS" })).toBeTruthy();
  expect(screen.getByRole("checkbox", { name: "selected" })).toHaveProperty(
    "checked",
    true,
  );
  expect(screen.queryByRole("checkbox", { name: "axb" })).toBeNull();
  fireEvent.change(suggestions, { target: { value: "a.b" } });
  fireEvent.click(screen.getByRole("checkbox", { name: "a.b" }));
  expect(screen.queryByRole("checkbox", { name: "axb" })).toBeNull();
  fireEvent.change(suggestions, { target: { value: "absent" } });
  expect(
    screen.getByText(
      "No matching tag suggestions. Selected tags remain available.",
    ),
  ).toBeTruthy();
  expect(screen.getByRole("checkbox", { name: "a.b" })).toHaveProperty(
    "checked",
    true,
  );
  expect(apply).not.toHaveBeenCalled();
  expect(value).toEqual({ kinds: ["agent"], tags: ["selected"] });
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(apply).toHaveBeenCalledExactlyOnceWith({
    kinds: ["skill", "mcp_server"],
    tags: ["selected", "a.b"],
  });
  fireEvent.click(screen.getByRole("button", { name: /Filters/ }));
  expect(
    screen.getByRole("searchbox", { name: "Search tag suggestions" }),
  ).toHaveProperty("value", "");
  fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
  expect(screen.getByRole("checkbox", { name: "Agents" })).toHaveProperty(
    "checked",
    false,
  );
});

it("stages category choices and typed labels without fetching unapplied criteria", async () => {
  const { props } = workspace();
  const post = vi.fn().mockResolvedValue(
    reply({
      matched: 2,
      facets: { category: { buckets: [{ value: "data, tools", count: 2 }] } },
    }),
  );
  Object.assign(props.client.api, { POST: post });
  const apply = vi.fn();
  render(
    <SearchFilters
      value={{ tags: ["saved"] }}
      searchText="applied"
      tags={[]}
      publishers={[]}
      client={props.client}
      onFailure={props.onFailure}
      onApply={apply}
    />,
  );
  const trigger = screen.getByRole("button", { name: "Categories" });
  fireEvent.click(trigger);
  expect(document.activeElement).toBe(
    screen.getByRole("button", { name: "Add category" }),
  );
  editValues("Categories", ["typed"]);
  const input = screen.getByLabelText("Category 1");
  const choice = await screen.findByRole("checkbox", {
    name: "data, tools",
  });
  expect(screen.getByLabelText("License declaration")).toBeTruthy();
  fireEvent.change(input, { target: { value: "typed" } });
  fireEvent.click(choice);
  expect(input).toHaveProperty("value", "typed");
  expect(screen.getByLabelText("Category 2")).toHaveProperty(
    "value",
    "data, tools",
  );
  fireEvent.click(choice);
  expect(input).toHaveProperty("value", "typed");
  fireEvent.click(choice);
  expect(input).toHaveProperty("value", "typed");
  expect(screen.getByLabelText("Category 2")).toHaveProperty(
    "value",
    "data, tools",
  );
  const signal = (post.mock.calls[0]?.[1] as { signal: AbortSignal }).signal;
  expect(signal).toBeInstanceOf(AbortSignal);
  expect(post).toHaveBeenCalledExactlyOnceWith("/api/v1/catalog/explore", {
    body: {
      query: { text: "applied", tags: ["saved"] },
      facets: [{ field: "category", limit: 24, minCount: 1 }],
    },
    signal,
  });
  expect(apply).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(apply).toHaveBeenCalledExactlyOnceWith({
    tags: ["saved"],
    categories: ["typed", "data, tools"],
  });
  expect(document.activeElement).toBe(trigger);
});

it("makes no local facet request for external-only searches and preserves manual filtering", () => {
  const { props } = workspace();
  const post = vi.fn();
  Object.assign(props.client.api, { POST: post });
  const apply = vi.fn();
  render(
    <SearchFilters
      value={{ origins: ["indexed"] }}
      tags={[]}
      publishers={[]}
      client={props.client}
      onFailure={props.onFailure}
      onApply={apply}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Origin" }));
  expect(document.activeElement).toBe(
    screen.getByRole("checkbox", { name: "Local Catalog" }),
  );
  expect(post).not.toHaveBeenCalled();
  editValues("Categories", ["development"]);
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(apply).toHaveBeenCalledExactlyOnceWith({
    origins: ["indexed"],
    tags: [],
    categories: ["development"],
  });
});

it("focuses common controls and keeps exact namespace entry independent of directory permission", async () => {
  const { props, get } = workspace();
  const post = vi
    .fn()
    .mockResolvedValue(
      reply({ matched: 0, facets: { category: { buckets: [] } } }),
    );
  Object.assign(props.client.api, { POST: post });
  const apply = vi.fn();
  const inputs = {
    value: {},
    tags: [],
    publishers: [],
    client: props.client,
    onFailure: props.onFailure,
    onApply: apply,
  };
  const view = render(<SearchFilters {...inputs} />);
  fireEvent.click(screen.getByRole("button", { name: "Namespace" }));
  expect(document.activeElement).toBe(screen.getByLabelText("Local namespace"));
  await screen.findByText(/No local category suggestions/);
  editValues("Categories", ["draft"]);
  fireEvent.click(screen.getByRole("button", { name: "Origin" }));
  expect(document.activeElement).toBe(
    screen.getByRole("checkbox", { name: "Local Catalog" }),
  );
  expect(screen.getByLabelText("Category 1")).toHaveProperty("value", "draft");
  fireEvent.click(screen.getByRole("button", { name: "Filters" }));
  expect(screen.queryByRole("region", { name: "Filters" })).toBeNull();
  expect(apply).not.toHaveBeenCalled();
  view.rerender(<SearchFilters {...inputs} namespaceAllowed={false} />);
  expect(screen.getByRole("button", { name: "Namespace" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Load namespaces" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Namespace" }));
  const exact = screen.getByLabelText("Exact namespace ID");
  expect(document.activeElement).toBe(exact);
  fireEvent.change(exact, {
    target: { value: "00000000-0000-7000-8000-000000000001" },
  });
  expect(
    screen.getByRole("checkbox", { name: "Indexed external" }),
  ).toHaveProperty("checked", false);
  fireEvent.change(exact, { target: { value: "" } });
  expect(
    screen.getByRole("checkbox", { name: "Indexed external" }),
  ).toHaveProperty("checked", false);
  fireEvent.change(exact, {
    target: { value: "00000000-0000-7000-8000-000000000001" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(apply).toHaveBeenCalledWith(
    expect.objectContaining({
      namespaceId: "00000000-0000-7000-8000-000000000001",
      origins: ["local"],
    }),
  );
  expect(get).not.toHaveBeenCalled();
});

it("adds a suggested tag when the applied request has no tag field", () => {
  const apply = vi.fn();
  const value = {};
  render(
    <SearchFilters
      value={value}
      tags={["tools"]}
      publishers={[]}
      onApply={apply}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Filters" }));
  const tag = screen.getByRole("checkbox", { name: "tools" });
  expect(tag).toHaveProperty("checked", false);
  fireEvent.click(tag);
  expect(tag).toHaveProperty("checked", true);
  expect(value).toEqual({});
  expect(apply).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(apply).toHaveBeenCalledExactlyOnceWith({ tags: ["tools"] });
  fireEvent.click(screen.getByRole("button", { name: "Filters" }));
  fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "tools" }));
  expect(apply).toHaveBeenCalledTimes(1);
  expect(value).toEqual({});
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(apply).toHaveBeenCalledTimes(2);
  expect(apply).toHaveBeenLastCalledWith({ tags: ["tools"] });
});

it("orders exact tag suggestions without changing their spelling, selection or caller input", () => {
  const tags = ["\u00e9", "a_b", "A", "a", "e\u0301", "a/b", "a-b", "A"];
  const original = [...tags];
  const apply = vi.fn();
  const view = render(
    <SearchFilters
      value={{ tags: ["\u00e9"] }}
      tags={tags}
      publishers={[]}
      onApply={apply}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Filters" }));
  const choices = () =>
    within(screen.getByRole("region", { name: "Tags" })).getAllByRole(
      "checkbox",
    );
  expect(
    choices().map(
      (choice) => (choice as HTMLInputElement).labels?.[0]?.textContent,
    ),
  ).toEqual(["A", "a", "a-b", "a/b", "a_b", "e\u0301", "\u00e9"]);
  expect(screen.getByRole("checkbox", { name: "\u00e9" })).toHaveProperty(
    "checked",
    true,
  );
  expect(screen.getByRole("checkbox", { name: "e\u0301" })).toHaveProperty(
    "checked",
    false,
  );
  view.rerender(
    <SearchFilters
      value={{ tags: ["\u00e9"] }}
      tags={[...tags].reverse()}
      publishers={[]}
      onApply={apply}
    />,
  );
  expect(
    choices().map(
      (choice) => (choice as HTMLInputElement).labels?.[0]?.textContent,
    ),
  ).toEqual(["A", "a", "a-b", "a/b", "a_b", "e\u0301", "\u00e9"]);
  fireEvent.click(screen.getByRole("checkbox", { name: "e\u0301" }));
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(apply).toHaveBeenCalledExactlyOnceWith({
    tags: ["\u00e9", "e\u0301"],
  });
  expect(tags).toEqual(original);
});

it("edits a copied filter draft and applies only explicit changes", () => {
  const value = {
    tags: ["existing"],
    publisher: "pub-a",
    visibility: "public" as const,
  };
  const apply = vi.fn();
  const view = render(
    <SearchFilters
      value={value}
      tags={["suggested"]}
      publishers={[
        { value: "pub-a", label: "pub-a", origin: "local" },
        { value: "pub-b", label: "pub-b", origin: "local" },
      ]}
      onApply={apply}
    />,
  );
  const trigger = screen.getByRole("button", { name: /Filters/ });
  expect(trigger.textContent).toContain("3");
  expect(
    screen.getByRole("button", {
      name: "Filters",
      description: "3 applied filters",
    }),
  ).toBe(trigger);
  fireEvent.click(trigger);
  expect(document.activeElement).toBe(
    screen.getByRole("button", { name: "Close filters" }),
  );
  fireEvent.click(screen.getByRole("checkbox", { name: "existing" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "suggested" }));
  fireEvent.change(screen.getByRole("textbox", { name: "Add tag" }), {
    target: { value: "added" },
  });
  fireEvent.click(screen.getByRole("radio", { name: "Private" }));
  fireEvent.click(
    screen.getByRole("button", { name: "pub-b Local publisher" }),
  );
  expect(
    screen.getByRole("button", {
      name: "Filters",
      description: "3 applied filters",
    }),
  ).toBe(trigger);
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(apply).toHaveBeenCalledWith({
    tags: ["suggested", "added"],
    publisher: "pub-b",
    visibility: "private",
  });
  expect(value).toEqual({
    tags: ["existing"],
    publisher: "pub-a",
    visibility: "public",
  });
  expect(screen.queryByRole("region", { name: "Filters" })).toBeNull();
  expect(document.activeElement).toBe(trigger);
  for (const { value, description } of [
    {
      value: {
        tags: ["suggested", "added"],
        publisher: "pub-b",
        visibility: "private" as const,
      },
      description: "4 applied filters",
    },
    { value: {}, description: "0 applied filters" },
  ]) {
    view.rerender(
      <SearchFilters value={value} tags={[]} publishers={[]} onApply={apply} />,
    );
    expect(screen.getByRole("button", { name: "Filters", description })).toBe(
      trigger,
    );
  }
});

it("clears groups and all filters without applying until submitted", () => {
  const apply = vi.fn();
  render(
    <SearchFilters
      value={{
        tags: ["tag"],
        publisher: "pub",
        license: "MIT",
        categories: ["tools"],
      }}
      tags={[]}
      publishers={[{ value: "pub", label: "pub", origin: "local" }]}
      onApply={apply}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: /Filters/ }));
  fireEvent.click(screen.getByRole("button", { name: "Clear tag filters" }));
  fireEvent.click(
    screen.getByRole("button", { name: "Clear publisher filter" }),
  );
  fireEvent.click(screen.getByRole("button", { name: "pub Local publisher" }));
  fireEvent.click(screen.getByRole("button", { name: "pub Local publisher" }));
  fireEvent.change(
    screen.getByRole("textbox", { name: "Publisher identifier" }),
    { target: { value: "another" } },
  );
  fireEvent.click(screen.getByRole("radio", { name: "Public" }));
  fireEvent.click(screen.getByRole("radio", { name: "Any visibility" }));
  fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
  expect(screen.getByText("No suggestions on this page.")).toBeTruthy();
  expect(screen.getByLabelText("License declaration")).toHaveProperty(
    "value",
    "",
  );
  expect(screen.queryByLabelText("Category 1")).toBeNull();
  expect(apply).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(apply).toHaveBeenCalledWith({ tags: [] });
});

it("reports invalid tag input and clears the error when corrected", () => {
  const apply = vi.fn();
  render(
    <SearchFilters value={{}} tags={[]} publishers={[]} onApply={apply} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Filters" }));
  fireEvent.change(screen.getByRole("textbox", { name: "Add tag" }), {
    target: { value: "x".repeat(81) },
  });
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(screen.getByRole("alert").textContent).toContain("80 characters");
  expect(apply).not.toHaveBeenCalled();
  fireEvent.change(screen.getByRole("textbox", { name: "Add tag" }), {
    target: { value: "valid" },
  });
  expect(screen.queryByRole("alert")).toBeNull();
  apply.mockImplementationOnce(() => {
    throw "invalid";
  });
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(screen.getByRole("alert").textContent).toBe(
    "Check the filter values.",
  );
});

it("allows the full Unicode tag budget through the HTML input before applying code-point limits", () => {
  const apply = vi.fn();
  render(
    <SearchFilters value={{}} tags={[]} publishers={[]} onApply={apply} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Filters" }));
  const input = screen.getByRole("textbox", {
    name: "Add tag",
  }) as HTMLInputElement;
  const tags = Array.from({ length: 32 }, (_, index) =>
    String.fromCodePoint(0x1f600 + index).repeat(80),
  );
  const addTag = screen.getByRole("button", { name: "Add exact tag" });
  expect(input.maxLength).toBeGreaterThanOrEqual(tags[0]!.length);
  for (const tag of tags) {
    fireEvent.change(input, { target: { value: tag } });
    fireEvent.click(addTag);
  }
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(apply).toHaveBeenCalledExactlyOnceWith({ tags });
});

it("dismisses on explicit close, Escape, outside pointer or focus without applying", () => {
  const apply = vi.fn();
  const view = render(
    <>
      <SearchFilters
        value={{}}
        tags={["tag"]}
        publishers={[]}
        onApply={apply}
      />
      <button>Outside</button>
    </>,
  );
  const trigger = screen.getByRole("button", { name: "Filters" });
  fireEvent.keyDown(trigger, { key: "Escape" });
  for (const dismiss of [
    () =>
      fireEvent.click(screen.getByRole("button", { name: "Close filters" })),
    () =>
      fireEvent.keyDown(screen.getByRole("region", { name: "Filters" }), {
        key: "Escape",
      }),
    () => fireEvent.click(trigger),
    () =>
      fireEvent.pointerDown(screen.getByRole("button", { name: "Outside" })),
    () =>
      fireEvent.blur(screen.getByRole("textbox", { name: "Add tag" }), {
        relatedTarget: screen.getByRole("button", { name: "Outside" }),
      }),
  ]) {
    fireEvent.click(trigger);
    fireEvent.keyDown(screen.getByRole("region", { name: "Filters" }), {
      key: "Tab",
    });
    fireEvent.pointerDown(screen.getByRole("checkbox", { name: "tag" }));
    fireEvent.blur(screen.getByRole("textbox", { name: "Add tag" }), {
      relatedTarget: trigger,
    });
    fireEvent.blur(trigger, { relatedTarget: null });
    fireEvent.resize(window);
    fireEvent.scroll(window);
    expect(screen.getByRole("region", { name: "Filters" })).toBeTruthy();
    dismiss();
    expect(screen.queryByRole("region", { name: "Filters" })).toBeNull();
  }
  expect(apply).not.toHaveBeenCalled();
  view.unmount();
  fireEvent.resize(window);
});

it("clears an existing visibility choice without clearing other draft filters", () => {
  const apply = vi.fn();
  const value = { visibility: "private" as const, tags: ["tools"] };
  render(
    <SearchFilters value={value} tags={[]} publishers={[]} onApply={apply} />,
  );
  fireEvent.click(screen.getByRole("button", { name: /Filters/ }));
  fireEvent.click(screen.getByRole("radio", { name: "Any visibility" }));
  expect(screen.getByRole("radio", { name: "Any visibility" })).toHaveProperty(
    "checked",
    true,
  );
  expect(apply).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(apply).toHaveBeenCalledExactlyOnceWith({
    tags: ["tools"],
    visibility: undefined,
  });
  expect(value).toEqual({ visibility: "private", tags: ["tools"] });
});

it("stages provenance, rejects an empty set and restricts namespace selection to local", async () => {
  const apply = vi.fn();
  const { props, get } = workspace();
  Object.assign(props.client.api, {
    POST: vi
      .fn()
      .mockResolvedValue(
        reply({ matched: 0, facets: { category: { buckets: [] } } }),
      ),
  });
  get.mockResolvedValue(
    reply({
      items: [
        {
          id: "namespace",
          tenantId: "tenant",
          slug: "demo",
          displayName: "Demo",
          revision: 1,
          createdAt: "2026-09-01T12:00:00Z",
          updatedAt: "2026-09-01T12:00:00Z",
        },
      ],
    }),
  );
  render(
    <SearchFilters
      value={{}}
      tags={[]}
      publishers={[]}
      onApply={apply}
      client={props.client}
      onFailure={props.onFailure}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Filters" }));
  for (const name of ["Local Catalog", "Indexed external", "Live external"])
    fireEvent.click(screen.getByRole("checkbox", { name }));
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(screen.getByRole("alert").textContent).toContain(
    "Select at least one origin",
  );
  expect(apply).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("checkbox", { name: "Live external" }));
  fireEvent.click(screen.getByRole("button", { name: "Load namespaces" }));
  await screen.findByRole("option", { name: "Demo" });
  fireEvent.change(screen.getByLabelText("Local namespace"), {
    target: { value: "namespace" },
  });
  expect(
    screen.getByRole("checkbox", { name: "Indexed external" }),
  ).toHaveProperty("disabled", true);
  fireEvent.change(screen.getByLabelText("Local namespace"), {
    target: { value: "" },
  });
  expect(
    screen.getByRole("checkbox", { name: "Indexed external" }),
  ).toHaveProperty("disabled", false);
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(apply).toHaveBeenCalledWith({
    namespaceId: undefined,
    origins: ["local"],
    tags: [],
  });
});

it("preserves one unapplied draft and focused control across desktop/drawer resizing", () => {
  const apply = vi.fn();
  render(
    <SearchFilters
      value={{ tags: ["saved"] }}
      tags={[]}
      publishers={[]}
      onApply={apply}
    />,
  );
  const trigger = screen.getByRole("button", { name: "Filters" });
  fireEvent.click(trigger);
  const panel = screen.getByRole("region", { name: "Filters" });
  const input = screen.getByRole("textbox", { name: "Add tag" });
  input.focus();
  fireEvent.change(input, { target: { value: "pending" } });
  vi.stubGlobal("innerWidth", 390);
  fireEvent.resize(window);
  expect(screen.getByRole("dialog", { name: "Filters" })).toBe(panel);
  expect(panel.getAttribute("aria-modal")).toBe("true");
  expect(document.activeElement).toBe(input);
  expect(apply).not.toHaveBeenCalled();
  vi.stubGlobal("innerWidth", 1440);
  fireEvent.resize(window);
  expect(screen.getByRole("region", { name: "Filters" })).toBe(panel);
  expect(document.activeElement).toBe(input);
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(apply).toHaveBeenCalledExactlyOnceWith({ tags: ["saved", "pending"] });
  expect(document.activeElement).toBe(trigger);
});

it("opens the narrow namespace shortcut as a modal and restores focus without applying changes", async () => {
  vi.stubGlobal("innerWidth", 320);
  const { props } = workspace();
  const post = vi
    .fn()
    .mockResolvedValue(
      reply({ matched: 0, facets: { category: { buckets: [] } } }),
    );
  Object.assign(props.client.api, { POST: post });
  const apply = vi.fn();
  render(
    <SearchFilters
      value={{ namespaceId: "namespace-draft" }}
      tags={[]}
      publishers={[]}
      client={props.client}
      onFailure={props.onFailure}
      onApply={apply}
    />,
  );
  const trigger = screen.getByRole("button", { name: "Namespace" });
  expect(trigger.getAttribute("aria-haspopup")).toBe("dialog");
  fireEvent.click(trigger);
  expect(screen.getByRole("dialog", { name: "Filters" })).toHaveProperty(
    "open",
    true,
  );
  expect(document.activeElement).toBe(screen.getByLabelText("Local namespace"));
  await screen.findByText(/No local category suggestions/);
  fireEvent.change(screen.getByLabelText("Local namespace"), {
    target: { value: "" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Close filters" }));
  expect(document.activeElement).toBe(trigger);
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(apply).not.toHaveBeenCalled();
});

it("opens narrow filters as a modal and cancels without publishing its draft", () => {
  vi.stubGlobal("innerWidth", 320);
  const apply = vi.fn();
  render(
    <SearchFilters value={{}} tags={[]} publishers={[]} onApply={apply} />,
  );
  const trigger = screen.getByRole("button", { name: "Filters" });
  fireEvent.click(trigger);
  fireEvent.change(screen.getByRole("textbox", { name: "Add tag" }), {
    target: { value: "discard" },
  });
  fireEvent.change(screen.getByLabelText("License declaration"), {
    target: { value: "MIT" },
  });
  editValues("Categories", ["tools"]);
  fireEvent(
    screen.getByRole("dialog", { name: "Filters" }),
    new Event("cancel", { bubbles: true, cancelable: true }),
  );
  expect(apply).not.toHaveBeenCalled();
  expect(trigger.getAttribute("aria-expanded")).toBe("false");
  expect(document.activeElement).toBe(trigger);
  fireEvent.click(trigger);
  expect(screen.getByLabelText("License declaration")).toHaveProperty(
    "value",
    "",
  );
  expect(screen.queryByLabelText("Category 1")).toBeNull();
  expect(screen.getByRole("textbox", { name: "Add tag" })).toHaveProperty(
    "value",
    "",
  );
});

it("dismisses on document Escape after focus loss, respects handled keys and removes the listener", () => {
  const apply = vi.fn();
  const view = render(
    <SearchFilters value={{}} tags={[]} publishers={[]} onApply={apply} />,
  );
  const trigger = screen.getByRole("button", { name: "Filters" });
  fireEvent.click(trigger);
  const handled = new KeyboardEvent("keydown", {
    key: "Escape",
    bubbles: true,
    cancelable: true,
  });
  handled.preventDefault();
  document.dispatchEvent(handled);
  expect(screen.getByRole("region", { name: "Filters" })).toBeTruthy();
  fireEvent.keyDown(document.body, { key: "x" });
  expect(screen.getByRole("region", { name: "Filters" })).toBeTruthy();
  fireEvent.keyDown(document.body, { key: "Escape" });
  expect(screen.queryByRole("region", { name: "Filters" })).toBeNull();
  expect(document.activeElement).toBe(trigger);
  expect(apply).not.toHaveBeenCalled();
  const afterClose = new KeyboardEvent("keydown", {
    key: "Escape",
    bubbles: true,
    cancelable: true,
  });
  document.dispatchEvent(afterClose);
  expect(afterClose.defaultPrevented).toBe(false);
  fireEvent.click(trigger);
  view.unmount();
  const afterUnmount = new KeyboardEvent("keydown", {
    key: "Escape",
    bubbles: true,
    cancelable: true,
  });
  document.dispatchEvent(afterUnmount);
  expect(afterUnmount.defaultPrevented).toBe(false);
  expect(apply).not.toHaveBeenCalled();
});

it("keeps unauthorized saved external filters explicit until corrected, while local defaults remain usable", () => {
  const apply = vi.fn();
  const view = render(
    <SearchFilters
      value={{}}
      tags={[]}
      publishers={[]}
      externalAllowed={false}
      onApply={apply}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Filters" }));
  expect(
    screen.getByRole("checkbox", { name: "Local Catalog" }),
  ).toHaveProperty("checked", true);
  expect(
    screen.getByRole("checkbox", { name: "Indexed external" }),
  ).toHaveProperty("disabled", true);
  expect(
    screen.getByRole("checkbox", { name: "Live external" }),
  ).toHaveProperty("disabled", true);
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(apply).toHaveBeenLastCalledWith({ tags: [] });
  view.rerender(
    <SearchFilters
      value={{ origins: ["indexed"] }}
      tags={[]}
      publishers={[]}
      externalAllowed={false}
      onApply={apply}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: /Filters/ }));
  expect(
    screen.getByRole("checkbox", { name: "Indexed external" }),
  ).toHaveProperty("checked", true);
  expect(
    screen.getByRole("checkbox", { name: "Indexed external" }),
  ).toHaveProperty("disabled", false);
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(screen.getByRole("alert").textContent).toContain(
    "federation.discover",
  );
  expect(apply).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("checkbox", { name: "Indexed external" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "Local Catalog" }));
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(apply).toHaveBeenLastCalledWith({ origins: ["local"], tags: [] });
});

it.each([
  ["Publisher identifier", "🙂".repeat(65)],
  ["Publisher identifier", "a\0b"],
  ["Add tag", "🙂".repeat(81)],
  ["Add tag", "a\0b"],
  ["License declaration", "🙂".repeat(257)],
  ["License declaration", "a\0b"],
  ["Category 1", "🙂".repeat(81)],
  ["Category 1", "a\0b"],
  ["Category 1", "   "],
])("keeps invalid %s focused and never applies the draft", (label, value) => {
  const apply = vi.fn();
  render(
    <SearchFilters value={{}} tags={[]} publishers={[]} onApply={apply} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Filters" }));
  if (label === "Category 1") editValues("Categories", [value]);
  const input = screen.getByLabelText(label);
  fireEvent.change(input, { target: { value } });
  fireEvent.click(screen.getByRole("button", { name: /Apply filters/ }));
  expect(screen.getByRole("alert")).toBeTruthy();
  expect(apply).not.toHaveBeenCalled();
  expect(document.activeElement).toBe(input);
  expect(input).toHaveProperty("value", value);
});

it("preserves exact category buckets containing commas and permits clearing local metadata", () => {
  const apply = vi.fn();
  render(
    <SearchFilters
      value={{
        license: "MIT",
        categories: ["tools, runtime"],
        origins: ["local"],
      }}
      tags={[]}
      publishers={[]}
      onApply={apply}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: /Filters/ }));
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(apply).toHaveBeenLastCalledWith({
    license: "MIT",
    categories: ["tools, runtime"],
    origins: ["local"],
    tags: [],
  });
  fireEvent.click(screen.getByRole("button", { name: /Filters/ }));
  fireEvent.change(screen.getByLabelText("License declaration"), {
    target: { value: "" },
  });
  editValues("Categories", []);
  const indexed = screen.getByRole("checkbox", { name: "Indexed external" });
  expect(indexed).toHaveProperty("disabled", false);
  fireEvent.click(indexed);
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(apply).toHaveBeenLastCalledWith({
    license: "",
    categories: [],
    origins: ["local", "indexed"],
    tags: [],
  });
});

it("stages license/category values without changing origins and retains bounds", () => {
  const apply = vi.fn();
  render(
    <SearchFilters
      value={{ origins: ["indexed", "live"], tags: ["saved"] }}
      tags={[]}
      publishers={[]}
      onApply={apply}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: /Filters/ }));
  const license = screen.getByRole("textbox", { name: "License declaration" });
  editValues("Categories", ["tools"]);
  const categories = screen.getByRole("textbox", {
    name: "Category 1",
  });
  fireEvent.change(license, { target: { value: "MIT" } });
  expect(
    screen.getByRole("checkbox", { name: "Local Catalog" }),
  ).toHaveProperty("checked", false);
  expect(
    screen.getByRole("checkbox", { name: "Indexed external" }),
  ).toHaveProperty("disabled", false);
  fireEvent.change(categories, { target: { value: "tools, " } });
  expect(categories).toHaveProperty("value", "tools, ");
  expect(apply).not.toHaveBeenCalled();
  fireEvent.change(license, { target: { value: "🙂".repeat(257) } });
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(screen.getByRole("alert")).toHaveProperty(
    "textContent",
    expect.stringContaining("256"),
  );
  expect(document.activeElement).toBe(license);
  expect(apply).not.toHaveBeenCalled();
  fireEvent.change(license, { target: { value: " Apache-2.0 " } });
  fireEvent.change(categories, { target: { value: "tools, finance" } });
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(apply).toHaveBeenCalledExactlyOnceWith({
    origins: ["indexed", "live"],
    tags: ["saved"],
    license: "Apache-2.0",
    categories: ["tools, finance"],
  });
});

it("accepts the exact Unicode license/category limits without cutting off values", () => {
  const apply = vi.fn();
  const license = "🙂".repeat(256);
  const categories = Array.from(
    { length: 16 },
    (_, index) => "🙂".repeat(79) + String.fromCharCode(97 + index),
  );
  render(
    <SearchFilters value={{}} tags={[]} publishers={[]} onApply={apply} />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Filters" }));
  fireEvent.change(screen.getByLabelText("License declaration"), {
    target: { value: license },
  });
  editValues("Categories", categories);
  expect(screen.getByRole("button", { name: "Add category" })).toHaveProperty(
    "disabled",
    true,
  );
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(apply).toHaveBeenCalledExactlyOnceWith({
    license,
    categories,
    tags: [],
  });
  expect(screen.queryByRole("alert")).toBeNull();
});

it("requires an explicit origin correction after an empty-origin draft without federation permission", () => {
  const apply = vi.fn();
  render(
    <SearchFilters
      value={{}}
      tags={[]}
      publishers={[]}
      externalAllowed={false}
      onApply={apply}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Filters" }));
  const local = screen.getByRole("checkbox", { name: "Local Catalog" });
  fireEvent.click(local);
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(screen.getByRole("alert")).toHaveProperty(
    "textContent",
    "Select at least one origin.",
  );
  expect(apply).not.toHaveBeenCalled();
  editValues("Categories", ["tools"]);
  expect(local).toHaveProperty("checked", false);
  expect(
    screen.getByRole("checkbox", { name: "Indexed external" }),
  ).toHaveProperty("disabled", true);
  expect(
    screen.getByRole("checkbox", { name: "Live external" }),
  ).toHaveProperty("disabled", true);
  fireEvent.click(local);
  fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
  expect(apply).toHaveBeenCalledExactlyOnceWith({
    categories: ["tools"],
    origins: ["local"],
    tags: [],
  });
});
