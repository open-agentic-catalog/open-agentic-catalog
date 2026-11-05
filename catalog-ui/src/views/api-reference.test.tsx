// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
  waitFor,
} from "@testing-library/react";
import type { Client } from "../api/client";
import APIReference from "./APIReference";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  history.replaceState(null, "", "/api-reference");
});

it("filters connected operations, resolves definitions locally and owns its download URL", async () => {
  const create = vi.fn(() => "blob:unit-contract");
  const revoke = vi.fn();
  vi.stubGlobal(
    "URL",
    Object.assign(class extends URL {}, {
      createObjectURL: create,
      revokeObjectURL: revoke,
    }),
  );
  const value = {
    yaml: "openapi: 3.1.0\n",
    document: {
      openapi: "3.1.0",
      info: { title: "Fixture", version: "v1", description: "Contract" },
      components: {
        schemas: {
          "\u00e9": {},
          "e\u0301": {},
          a_b: {},
          "a-b": {},
          a: {},
          "Name/~01/~~//": { type: "string" },
        },
        parameters: { Repeated: {} },
      },
      paths: {
        "/read": {
          get: {
            tags: ["Reads", "\u00e9", "e\u0301", "a_b", "a-b", "a"],
            summary: "Read",
            description: "Read description",
            security: [],
            responses: { 200: { description: "Done" } },
          },
        },
        "/write": {
          post: {
            tags: ["Writes"],
            security: [{ bearer: [] }],
            parameters: [{ name: "item", in: "query" }],
            requestBody: { $ref: "#/components/schemas/Name~1~001~1~0~0~1~1" },
            responses: { 201: { $ref: "#/missing" } },
          },
        },
        "/opaque": { get: { security: { unknown: true } } },
      },
    },
  };
  const reference = vi.fn().mockResolvedValue(value);
  const view = render(
    <APIReference
      client={{ reference } as unknown as Client}
      basePath="/reference-test"
      onFailure={vi.fn()}
    />,
  );
  await act(async () => {});
  expect(screen.getByRole("status").textContent).toBe("3 of 3 operations");
  expect(
    screen
      .getByRole("link", { name: "Service information" })
      .getAttribute("href"),
  ).toBe("/reference-test/service");
  expect(
    screen.getByRole("link", { name: "My access" }).getAttribute("href"),
  ).toBe("/reference-test/access");
  fireEvent.click(screen.getByRole("button", { name: /^Browse definitions/ }));
  expect(
    within(
      screen.getByRole("combobox", { name: "Schema or shared definition" }),
    )
      .getAllByRole("option")
      .map((option) => (option as HTMLOptionElement).value),
  ).toEqual(
    [
      "",
      "parameters/Repeated",
      "schemas/Name~1~001~1~0~0~1~1",
      "schemas/a",
      "schemas/a-b",
      "schemas/a_b",
      "schemas/e\u0301",
      "schemas/\u00e9",
    ].map((name) => (name ? `#/components/${name}` : "")),
  );
  fireEvent.click(screen.getByRole("button", { name: "Browse endpoints" }));
  expect(
    within(screen.getByRole("combobox", { name: "API area" }))
      .getAllByRole("option")
      .map((option) => option.textContent),
  ).toEqual([
    "All areas",
    "Reads",
    "Writes",
    "a",
    "a-b",
    "a_b",
    "e\u0301",
    "\u00e9",
  ]);
  const link = screen.getByRole("link", { name: "Download OpenAPI YAML" });
  expect(link.getAttribute("href")).toBe("blob:unit-contract");
  expect(link.getAttribute("download")).toBe("control-v1.yaml");
  for (const control of [
    link,
    screen.getByRole("button", { name: "Refresh contract" }),
  ]) {
    expect(control.querySelector("svg")?.getAttribute("aria-hidden")).toBe(
      "true",
    );
    expect(control.querySelector("svg")?.getAttribute("focusable")).toBe(
      "false",
    );
  }
  const blob = create.mock.calls[0] as unknown as [Blob];
  expect(blob[0].type).toBe("application/yaml");
  const panels = view.container.querySelectorAll<HTMLDetailsElement>(
    "details.api-operation",
  );
  for (const panel of panels) {
    panel.open = true;
    fireEvent(panel, new Event("toggle"));
  }
  expect(screen.getByText("No API authentication requirement.")).toBeTruthy();
  expect(screen.getAllByText("No parameters.")).toHaveLength(2);
  expect(screen.getAllByText("No request body.")).toHaveLength(2);
  fireEvent.click(
    screen.getByRole("button", {
      name: "View components/schemas/Name~1~001~1~0~0~1~1",
    }),
  );
  const definitions = screen.getByRole("region", {
    name: "Selected definition",
  });
  expect(document.activeElement).toBe(definitions);
  expect(
    within(definitions).getByRole("heading", { level: 3 }).textContent,
  ).toBe("#/components/schemas/Name~1~001~1~0~0~1~1");
  expect(
    within(definitions).getByRole("group", { name: "JSON document" })
      .textContent,
  ).toBe('{\n  "type": "string"\n}');
  fireEvent.click(screen.getByRole("button", { name: "Browse endpoints" }));
  fireEvent.click(screen.getByRole("button", { name: "View missing" }));
  fireEvent.change(screen.getByLabelText("Schema or shared definition"), {
    target: { value: "#/missing" },
  });
  expect(
    screen.getByText("Definition unavailable in this contract."),
  ).toBeTruthy();
  fireEvent.change(
    screen.getByRole("combobox", { name: "Schema or shared definition" }),
    { target: { value: "" } },
  );
  expect(
    screen.queryByText("Definition unavailable in this contract."),
  ).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Browse endpoints" }));
  fireEvent.change(screen.getByRole("combobox", { name: "API area" }), {
    target: { value: "Writes" },
  });
  expect(screen.getByRole("status").textContent).toBe("1 of 3 operations");
  fireEvent.change(
    screen.getByRole("searchbox", { name: "Search API operations" }),
    { target: { value: "does-not-exist" } },
  );
  expect(
    screen.getByText(
      "No operations match. Change or clear search, HTTP method or API area.",
    ),
  ).toBeTruthy();
  expect(reference).toHaveBeenCalledOnce();
  await act(async () =>
    fireEvent.click(screen.getByRole("button", { name: "Refresh contract" })),
  );
  expect(reference).toHaveBeenCalledTimes(2);
  view.unmount();
  expect(revoke).toHaveBeenCalledWith("blob:unit-contract");
});

it("recovers from an unavailable contract without rendering its private error", async () => {
  vi.stubGlobal(
    "URL",
    Object.assign(class extends URL {}, {
      createObjectURL: vi.fn(() => "blob:empty"),
      revokeObjectURL: vi.fn(),
    }),
  );
  const reference = vi
    .fn()
    .mockRejectedValueOnce(new Error("private contract"))
    .mockResolvedValueOnce(null);
  const failure = vi.fn();
  render(
    <APIReference
      client={{ reference } as unknown as Client}
      onFailure={failure}
    />,
  );
  await act(async () => {});
  expect(screen.getByRole("alert").textContent).not.toContain(
    "private contract",
  );
  expect(failure).toHaveBeenCalledOnce();
  await act(async () =>
    fireEvent.click(screen.getByRole("button", { name: "Reload" })),
  );
  expect(screen.getByRole("status").textContent).toBe("0 of 0 operations");
});

it("groups access operations from connected metadata and refreshes areas, operations and definitions without stale entries", async () => {
  vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:contract");
  vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
  const document = {
    info: { title: "Connected" },
    tags: [
      { name: "Identity", description: "All identity operations" },
      { name: "Roles", description: "Role definitions" },
      { name: "Groups and members", description: "Group membership" },
      { name: "Access assignments", description: "Assignment grants" },
      { name: "My access", description: "Caller provenance" },
    ],
    components: { schemas: { Group: { type: "object" } } },
    paths: {
      "/api/v1/access/groups/{groupId}/members": {
        get: {
          operationId: "listAccessGroupMembers",
          tags: ["Identity", "Groups and members"],
        },
      },
      "/api/v1/access/roles": {
        get: {
          operationId: "listRoleDefinitions",
          tags: ["Identity", "Roles"],
        },
      },
      "/api/v1/access/assignments": {
        get: {
          operationId: "listAccessAssignments",
          tags: ["Identity", "Access assignments"],
        },
      },
      "/api/v1/access/self": {
        get: {
          operationId: "listCurrentAccess",
          tags: ["Identity", "My access"],
        },
      },
    },
  };
  const reference = vi
    .fn()
    .mockResolvedValueOnce({ yaml: "exact initial bytes", document })
    .mockResolvedValueOnce({
      yaml: "exact refreshed bytes",
      document: {
        info: { title: "Replacement" },
        components: { schemas: { New: {} } },
        paths: {
          "/replacement": {
            get: { operationId: "replacement", tags: ["Replacement"] },
          },
        },
      },
    });
  render(
    <APIReference
      client={{ reference } as unknown as Client}
      onFailure={vi.fn()}
    />,
  );
  await screen.findByText("4 of 4 operations");
  expect(
    Array.from(
      window.document.querySelectorAll(".api-area-summary h2"),
      (node) => node.textContent,
    ),
  ).toEqual(["Roles", "Groups and members", "Access assignments", "My access"]);
  expect(screen.getByText("Role definitions")).toBeTruthy();
  expect(screen.getByText("Group membership")).toBeTruthy();
  expect(
    screen.queryAllByRole("group", { name: "JSON document" }),
  ).toHaveLength(0);
  expect(
    new Set(
      Array.from(window.document.querySelectorAll("[id]"), (node) => node.id),
    ).size,
  ).toBe(window.document.querySelectorAll("[id]").length);
  fireEvent.change(screen.getByLabelText("API area"), {
    target: { value: "Identity" },
  });
  expect(screen.getByText("4 of 4 operations")).toBeTruthy();
  for (const area of [
    "Groups and members",
    "Roles",
    "Access assignments",
    "My access",
  ]) {
    fireEvent.change(screen.getByLabelText("API area"), {
      target: { value: area },
    });
    expect(screen.getByText("1 of 4 operations")).toBeTruthy();
  }
  fireEvent.change(screen.getByLabelText("API area"), {
    target: { value: "Groups and members" },
  });
  fireEvent.change(screen.getByLabelText("Search API operations"), {
    target: { value: "listAccessGroupMembers" },
  });
  expect(screen.getByText("1 of 4 operations")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Refresh contract" }));
  await screen.findByText("0 of 1 operations");
  expect(screen.getByLabelText("Search API operations")).toHaveProperty(
    "value",
    "listAccessGroupMembers",
  );
  expect(await screen.findByText(/selected API area was removed/)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
  expect(
    screen.queryByRole("option", { name: "Groups and members" }),
  ).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: /^Browse definitions/ }));
  expect(screen.queryByRole("option", { name: "schemas/Group" })).toBeNull();
  expect(screen.getByRole("option", { name: "schemas/New" })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Browse endpoints" }));
  expect(screen.getByText("/replacement")).toBeTruthy();
  expect(reference).toHaveBeenCalledTimes(2);
});

it.each(["", "/nested/ui"])(
  "retains filters and operation context through known schema anchors at %s",
  async (basePath) => {
    history.replaceState(null, "", `${basePath}/api-reference`);
    vi.stubGlobal(
      "URL",
      Object.assign(class extends URL {}, {
        createObjectURL: vi.fn(() => "blob:nav"),
        revokeObjectURL: vi.fn(),
      }),
    );
    const contract = {
      yaml: "openapi: 3.1.0",
      document: {
        info: { title: "Navigation", version: "1" },
        components: {
          schemas: {
            A: { $ref: "#/components/schemas/B" },
            B: { type: "string" },
          },
        },
        paths: {
          "/items": {
            get: {
              operationId: "readItems",
              summary: "Read items",
              responses: { 200: { $ref: "#/components/schemas/A" } },
            },
          },
        },
      },
    };
    const reference = vi.fn().mockResolvedValue(contract);
    render(
      <APIReference
        client={{ reference } as unknown as Client}
        onFailure={vi.fn()}
      />,
    );
    await screen.findByText("Read items");
    const search = screen.getByRole("searchbox", {
      name: "Search API operations",
    });
    fireEvent.change(search, { target: { value: "Read" } });
    const summary = screen.getByText("Read items").closest("summary")!;
    fireEvent.click(summary);
    const view = await screen.findByRole("button", {
      name: "View components/schemas/A",
    });
    fireEvent.click(view);
    const firstHash = location.hash;
    const historyLength = history.length;
    expect(firstHash).not.toContain("Read");
    expect(location.pathname).toBe(`${basePath}/api-reference`);
    fireEvent.click(view);
    expect(history.length).toBe(historyLength);
    const region = screen.getByRole("region", {
      name: "Selected definition",
    });
    expect(document.activeElement).toBe(region);
    fireEvent.click(
      within(region).getByRole("button", { name: "View components/schemas/B" }),
    );
    expect(location.hash).not.toBe(firstHash);
    expect(within(region).getByRole("heading", { level: 3 }).textContent).toBe(
      "#/components/schemas/B",
    );
    fireEvent.change(search, {
      target: { value: "unsubmitted-private-filter" },
    });
    expect(
      screen.getByRole("button", { name: "Return to originating operation" }),
    ).toHaveProperty("disabled", true);
    expect(location.hash).not.toContain("private");
    fireEvent.change(search, { target: { value: "Read" } });
    fireEvent.click(
      screen.getByRole("button", { name: "Return to originating operation" }),
    );
    const returnedSummary = screen.getByText("Read items").closest("summary")!;
    expect(document.activeElement).toBe(returnedSummary);
    expect(returnedSummary.closest("details")).toHaveProperty("open", true);
    expect(search).toHaveProperty("value", "Read");
    expect(location.hash).toBe("#api-operation-GET%253A%252Fitems");
    await act(async () => fireEvent.click(returnedSummary));
    expect(returnedSummary.closest("details")).toHaveProperty("open", false);
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: "View components/schemas/A" }),
      ).toBeNull(),
    );
    history.replaceState(null, "", firstHash);
    fireEvent(window, new PopStateEvent("popstate"));
    expect(document.activeElement).toBe(region);
    expect(within(region).getByRole("heading", { level: 3 }).textContent).toBe(
      "#/components/schemas/A",
    );
    history.replaceState(null, "", "#%E0%A4%A");
    fireEvent(window, new PopStateEvent("popstate"));
    expect(reference).toHaveBeenCalledOnce();
    history.replaceState(null, "", `${basePath}/api-reference`);
  },
);

it("restores each schema history entry's own originating operation", async () => {
  history.replaceState(null, "", "/api-reference");
  vi.stubGlobal(
    "URL",
    Object.assign(class extends URL {}, {
      createObjectURL: vi.fn(() => "blob:origins"),
      revokeObjectURL: vi.fn(),
    }),
  );
  const reference = vi.fn().mockResolvedValue({
    yaml: "openapi: 3.1.0",
    document: {
      info: {},
      components: {
        schemas: {
          Shared: { type: "string", allOf: [{ $ref: "#/unavailable" }] },
        },
      },
      paths: {
        "/first": {
          get: {
            summary: "First origin",
            responses: { 200: { $ref: "#/components/schemas/Shared" } },
          },
        },
        "/second": {
          get: {
            summary: "Second origin",
            responses: { 200: { $ref: "#/components/schemas/Shared" } },
          },
        },
      },
    },
  });
  render(
    <APIReference
      client={{ reference } as unknown as Client}
      onFailure={vi.fn()}
    />,
  );
  const first = (await screen.findByText("First origin")).closest("summary")!;
  const second = screen.getByText("Second origin").closest("summary")!;
  const inspect = async (summary: HTMLElement) => {
    fireEvent.click(screen.getByRole("button", { name: "Browse endpoints" }));
    fireEvent.click(summary);
    fireEvent.click(
      await within(summary.closest("details")!).findByRole("button", {
        name: "View components/schemas/Shared",
      }),
    );
    return { state: history.state as unknown, hash: location.hash };
  };
  const earlier = await inspect(first);
  const later = await inspect(second);
  expect(earlier.hash).toBe(later.hash);
  history.replaceState(earlier.state, "", earlier.hash);
  fireEvent(window, new PopStateEvent("popstate"));
  fireEvent.click(screen.getByRole("button", { name: "Browse endpoints" }));
  fireEvent.click(
    screen.getByRole("button", { name: "Collapse Untagged operations" }),
  );
  expect(first.closest(".api-area-disclosure")).toHaveProperty("open", false);
  fireEvent.click(screen.getByRole("button", { name: /^Browse definitions/ }));
  fireEvent.click(
    screen.getByRole("button", { name: "Return to originating operation" }),
  );
  expect(document.activeElement).toBe(first);
  history.replaceState(later.state, "", later.hash);
  fireEvent(window, new PopStateEvent("popstate"));
  fireEvent.click(
    screen.getByRole("button", { name: "Return to originating operation" }),
  );
  expect(document.activeElement).toBe(second);
  history.replaceState(
    { apiReferenceOrigin: "unknown-private-input" },
    "",
    earlier.hash,
  );
  fireEvent(window, new PopStateEvent("popstate"));
  expect(
    screen.queryByRole("button", { name: "Return to originating operation" }),
  ).toBeNull();
  expect(location.hash).not.toContain("private");
  history.replaceState(null, "", "#unknown-contract-anchor");
  fireEvent(window, new PopStateEvent("popstate"));
  expect(
    screen.getByText(/This link is unavailable in the current contract/),
  ).toBeTruthy();
  expect(
    screen.queryByRole("heading", { name: "#/components/schemas/Shared" }),
  ).toBeNull();
  history.replaceState(null, "", earlier.hash);
  fireEvent(window, new PopStateEvent("popstate"));
  fireEvent.click(
    within(
      screen.getByRole("region", { name: "Selected definition" }),
    ).getByRole("button", { name: "View unavailable" }),
  );
  expect(
    screen.getByText("Definition unavailable in this contract."),
  ).toBeTruthy();
  expect(location.hash).toBe("#api-definitions");
  expect(reference).toHaveBeenCalledOnce();
  history.replaceState(null, "", "/api-reference");
});

it("restores known deep links, ignores unknown fragments and clears a definition selection deliberately", async () => {
  const operationHash = "#api-operation-GET%253A%252Fdeep";
  history.replaceState(null, "", `/api-reference${operationHash}`);
  vi.stubGlobal(
    "URL",
    Object.assign(class extends URL {}, {
      createObjectURL: vi.fn(() => "blob:deep"),
      revokeObjectURL: vi.fn(),
    }),
  );
  const reference = vi.fn().mockResolvedValue({
    yaml: "openapi: 3.1.0",
    document: {
      info: {},
      components: {
        schemas: { A: { type: "string", allOf: [{ $ref: "#/missing" }] } },
      },
      paths: {
        "/deep": {
          get: {
            summary: "Deep read",
            responses: { 200: { $ref: "#/components/schemas/A" } },
          },
        },
      },
    },
  });
  const view = render(
    <APIReference
      client={{ reference } as unknown as Client}
      onFailure={vi.fn()}
    />,
  );
  const summary = (await screen.findByText("Deep read")).closest("summary")!;
  await act(async () => {});
  expect(document.activeElement).toBe(summary);
  expect(summary.closest("details")).toHaveProperty("open", true);
  fireEvent.click(screen.getByRole("button", { name: /^Browse definitions/ }));
  const selector = screen.getByLabelText("Schema or shared definition");
  fireEvent.change(selector, { target: { value: "#/components/schemas/A" } });
  const hash = location.hash;
  view.unmount();
  render(
    <APIReference
      client={{ reference } as unknown as Client}
      onFailure={vi.fn()}
    />,
  );
  await screen.findByRole("heading", { name: "#/components/schemas/A" });
  await waitFor(() =>
    expect(document.activeElement).toBe(
      screen.getByRole("region", { name: "Selected definition" }),
    ),
  );
  fireEvent.click(screen.getByRole("button", { name: "View missing" }));
  expect(
    screen.getByText("Definition unavailable in this contract."),
  ).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Schema or shared definition"), {
    target: { value: "#/components/schemas/A" },
  });
  history.replaceState(null, "", "#unknown");
  fireEvent(window, new Event("hashchange"));
  expect(screen.getByLabelText("Schema or shared definition")).toHaveProperty(
    "value",
    "",
  );
  history.replaceState(null, "", hash);
  fireEvent(window, new Event("hashchange"));
  fireEvent.change(screen.getByLabelText("Schema or shared definition"), {
    target: { value: "" },
  });
  expect(location.hash).toBe("#api-definitions");
  history.replaceState(null, "", "#unknown");
  history.replaceState(null, "", "/api-reference");
  fireEvent(window, new PopStateEvent("popstate"));
  expect(
    screen.queryByRole("heading", { name: "#/components/schemas/A" }),
  ).toBeNull();
});

it("keeps one operation home, explicit filters, lazy disclosures and keyboard recovery without requests", async () => {
  vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:sections");
  vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
  const reference = vi.fn().mockResolvedValue({
    yaml: "exact bytes",
    document: {
      info: { title: "Sections", description: "A long contract overview" },
      tags: [
        { name: "Beta", description: "Beta description" },
        { name: "Alpha" },
      ],
      paths: {
        "/alpha": {
          get: {
            summary: "Read alpha",
            operationId: "readAlpha",
            tags: ["Beta", "Alpha"],
            deprecated: true,
          },
          post: {
            summary: "Write alpha",
            operationId: "writeAlpha",
            tags: ["Alpha"],
            deprecated: "true",
          },
        },
        "/beta": { get: { summary: "Read beta", tags: ["Beta"] } },
      },
    },
  });
  const view = render(
    <APIReference
      client={{ reference } as unknown as Client}
      onFailure={vi.fn()}
    />,
  );
  await screen.findByText("3 of 3 operations");
  const query = screen.getByRole("searchbox", {
    name: "Search API operations",
  });
  const method = screen.getByRole("combobox", { name: "HTTP method" });
  const tag = screen.getByRole("combobox", { name: "API area" });
  const index = screen.getByRole("navigation", { name: "API areas" });
  const alpha = screen.getByRole("heading", { name: "Alpha" });
  const area = alpha.closest("details")!;
  const read = screen.getByText("Read alpha").closest("details")!;
  const summary = read.querySelector("summary")!;
  const scroll = vi.fn();
  alpha.scrollIntoView = scroll;
  index.scrollIntoView = scroll;
  expect(
    screen.getByText("A long contract overview").closest("details"),
  ).toHaveProperty("open", false);
  expect(screen.getAllByText("Deprecated")).toHaveLength(1);
  expect(view.container.querySelectorAll(".api-operation[open]")).toHaveLength(
    0,
  );
  expect(
    query.compareDocumentPosition(
      screen.getByLabelText("Schema or shared definition"),
    ) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
  fireEvent.click(
    within(index).getByRole("link", { name: "Alpha: 2 operations" }),
  );
  expect(document.activeElement).toBe(alpha);
  expect(scroll).toHaveBeenLastCalledWith({ block: "start" });
  expect(tag).toHaveProperty("value", "");
  fireEvent.click(alpha.closest("summary")!);
  await waitFor(() => expect(area).toHaveProperty("open", false));
  fireEvent.click(
    within(index).getByRole("link", { name: "Alpha: 2 operations" }),
  );
  expect(area).toHaveProperty("open", true);
  expect(
    within(index)
      .getByRole("link", { name: "Alpha: 2 operations" })
      .getAttribute("aria-current"),
  ).toBe("location");
  fireEvent.click(summary);
  await within(read).findByRole("heading", { name: "Authentication" });
  const operationHash = within(read)
    .getByRole("link", { name: "Link to operation" })
    .getAttribute("href")!;
  fireEvent.click(screen.getByRole("button", { name: "Collapse Alpha" }));
  expect(area).toHaveProperty("open", false);
  expect(document.activeElement).toBe(alpha.closest("summary"));
  expect(read).toHaveProperty("open", true);
  history.replaceState(null, "", operationHash);
  fireEvent(window, new HashChangeEvent("hashchange"));
  expect(area).toHaveProperty("open", true);
  expect(document.activeElement).toBe(summary);
  fireEvent.change(query, { target: { value: "read beta" } });
  expect(document.activeElement).toBe(query);
  expect(screen.queryByText("Read alpha")).toBeNull();
  fireEvent.change(query, { target: { value: "" } });
  const restored = screen.getByText("Read alpha").closest("details")!;
  expect(restored).toHaveProperty("open", true);
  fireEvent.change(method, { target: { value: "POST" } });
  fireEvent.change(tag, { target: { value: "Beta" } });
  expect(screen.getByText("0 of 3 operations")).toBeTruthy();
  expect(screen.getByText(/Applied filters:/).textContent).toContain(
    'method POST; API area "Beta"',
  );
  expect(
    within(index).getByRole("option", { name: "No matching sections" }),
  ).toBeTruthy();
  fireEvent.change(query, { target: { value: "private local input" } });
  expect(
    location.href +
      JSON.stringify(history.state) +
      JSON.stringify(localStorage),
  ).not.toContain("private local input");
  // A known but filtered operation does not turn a fragment into a filter reset.
  history.replaceState(null, "", operationHash);
  fireEvent(window, new PopStateEvent("popstate"));
  expect(query).toHaveProperty("value", "private local input");
  fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
  expect(document.activeElement).toBe(query);
  expect(screen.getByText("3 of 3 operations")).toBeTruthy();
  expect(method).toHaveProperty("value", "");
  expect(tag).toHaveProperty("value", "");
  const selector = screen.getByRole("combobox", {
    name: "Jump to API section",
  });
  const beta = screen.getByRole("heading", { name: "Beta" });
  fireEvent.change(selector, { target: { value: beta.id } });
  expect(document.activeElement).toBe(beta);
  const back = within(beta.closest("section")!).getByRole("link", {
    name: "Back to API areas",
  });
  fireEvent.click(back);
  expect(document.activeElement).toBe(index);
  fireEvent.click(screen.getByRole("button", { name: "Collapse Alpha" }));
  history.replaceState(null, "", `#${encodeURIComponent(alpha.id)}`);
  fireEvent(window, new PopStateEvent("popstate"));
  expect(document.activeElement).toBe(
    screen.getByRole("heading", { name: "Alpha" }),
  );
  history.replaceState(null, "", "#api-area-index");
  fireEvent(window, new HashChangeEvent("hashchange"));
  expect(document.activeElement).toBe(index);
  fireEvent.click(
    screen.getByRole("button", {
      name: "Browse schemas and shared definitions",
    }),
  );
  expect(document.activeElement).toBe(
    screen.getByRole("region", { name: "Schema and shared definitions" }),
  );
  // Native toggles close and reopen the section without opening other operations.
  const betaDetails = beta.closest("details")!;
  betaDetails.open = false;
  fireEvent(betaDetails, new Event("toggle"));
  expect(betaDetails).toHaveProperty("open", false);
  betaDetails.open = true;
  fireEvent(betaDetails, new Event("toggle"));
  expect(betaDetails).toHaveProperty("open", true);
  expect(reference).toHaveBeenCalledOnce();
});

it("tracks only displayed area intersections and disconnects its passive observer", async () => {
  vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:observer");
  vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
  let notify!: IntersectionObserverCallback;
  const observe = vi.fn<(target: Element) => void>();
  const disconnect = vi.fn();
  const options: IntersectionObserverInit[] = [];
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      constructor(
        callback: IntersectionObserverCallback,
        value: IntersectionObserverInit,
      ) {
        notify = callback;
        options.push(value);
      }
      observe = observe;
      disconnect = disconnect;
    },
  );
  const computed = window.getComputedStyle;
  const style = window.document.createElement("div").style;
  style.setProperty("--header-height", "104px");
  vi.spyOn(window, "getComputedStyle").mockImplementation((element, pseudo) =>
    element.classList.contains("api-endpoints")
      ? style
      : computed(element, pseudo),
  );
  const reference = vi.fn().mockResolvedValue({
    yaml: "",
    document: {
      paths: {
        "/one": { get: { tags: ["One"] } },
        "/two": { get: { tags: ["Two"] } },
      },
    },
  });
  const view = render(
    <div className="workspace">
      <APIReference
        client={{ reference } as unknown as Client}
        onFailure={vi.fn()}
      />
    </div>,
  );
  await screen.findByText("2 of 2 operations");
  const nodes = Array.from(
    view.container.querySelectorAll(".api-area-section"),
  );
  await waitFor(() =>
    expect(observe.mock.calls.map(([node]) => node)).toEqual(nodes),
  );
  expect(options).toEqual([{ rootMargin: "-120px 0px -25% 0px" }]);
  const index = screen.getByRole("navigation", { name: "API areas" });
  await act(async () =>
    notify(
      [
        { target: nodes[1], isIntersecting: true },
      ] as IntersectionObserverEntry[],
      {} as IntersectionObserver,
    ),
  );
  expect(
    within(index)
      .getByRole("link", { name: "Two: 1 operation" })
      .getAttribute("aria-current"),
  ).toBe("location");
  await act(async () =>
    notify(
      [
        { target: nodes[1], isIntersecting: false },
      ] as IntersectionObserverEntry[],
      {} as IntersectionObserver,
    ),
  );
  const workspace = view.container.querySelector<HTMLElement>(".workspace")!;
  await act(async () => {
    style.setProperty("--header-height", "196px");
    workspace.style.setProperty("--header-height", "196px");
  });
  expect(options.at(-1)).toEqual({ rootMargin: "-212px 0px -25% 0px" });
  expect(disconnect).toHaveBeenCalledOnce();
  expect(observe).toHaveBeenCalledTimes(4);
  // Unrelated style changes do not rebuild an observer with the same offset.
  await act(async () => workspace.style.setProperty("color", "red"));
  expect(options).toHaveLength(2);
  style.removeProperty("--header-height");
  fireEvent.change(
    screen.getByRole("searchbox", { name: "Search API operations" }),
    { target: { value: "/one" } },
  );
  expect(options.at(-1)).toEqual({ rootMargin: "-72px 0px -25% 0px" });
  expect(disconnect).toHaveBeenCalledTimes(2);
  expect(
    within(index)
      .getByRole("link", { name: "One: 1 operation" })
      .getAttribute("aria-current"),
  ).toBe("location");
  view.unmount();
  expect(disconnect).toHaveBeenCalledTimes(3);
  await act(async () =>
    workspace.style.setProperty("--header-height", "300px"),
  );
  expect(options).toHaveLength(3);
  expect(reference).toHaveBeenCalledOnce();
  // Standalone rendering still uses the default offset without a shell owner.
  const standalone = render(
    <APIReference
      client={{ reference } as unknown as Client}
      onFailure={vi.fn()}
    />,
  );
  await screen.findByText("2 of 2 operations");
  expect(options.at(-1)).toEqual({ rootMargin: "-72px 0px -25% 0px" });
  standalone.unmount();
  expect(disconnect).toHaveBeenCalledTimes(4);
});
