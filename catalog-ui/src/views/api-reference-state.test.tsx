// @vitest-environment jsdom
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { Client } from "../api/client";
import type { Document } from "../api/reference";
import APIReference from "./APIReference";

beforeEach(() => {
  let serial = 0;
  vi.spyOn(URL, "createObjectURL").mockImplementation(
    () => `blob:contract-${++serial}`,
  );
  vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  history.replaceState(null, "", "/api-reference");
});

function fixture() {
  return {
    yaml: "exact before YAML",
    document: {
      openapi: "3.1.0",
      info: { title: "Before", version: "1" },
      tags: [{ name: "Stable" }],
      components: {
        schemas: Object.fromEntries(
          Array.from({ length: 70 }, (_, i) => [
            `Field${String(i).padStart(2, "0")}`,
            {
              type: "string",
              description:
                i === 69 ? "Unique late description" : "Ordinary field",
            },
          ]),
        ),
        "x/~": { "custom/~": 0 },
        broken: null,
      },
      paths: {
        "/read": {
          get: {
            summary: "Read record",
            tags: ["Stable"],
            responses: {
              "200": {
                description: "Result",
                content: {
                  "application/json": {
                    schema: { $ref: "#/components/schemas/Field69" },
                  },
                },
              },
            },
          },
        },
      },
    },
  };
}
function mount(
  reference = vi.fn().mockResolvedValue(fixture()),
  onFailure = vi.fn(),
) {
  return {
    ...render(
      <APIReference
        client={{ reference } as unknown as Client}
        onFailure={onFailure}
      />,
    ),
    reference,
    onFailure,
  };
}
const endpoints = () =>
  fireEvent.click(screen.getByRole("button", { name: "Browse endpoints" }));
const definitions = () =>
  fireEvent.click(screen.getByRole("button", { name: /^Browse definitions/ }));
const search = () =>
  screen.getByRole("searchbox", { name: "Search definitions" });
const selected = () =>
  screen.getByRole("region", { name: "Selected definition" });
const change = (label: string, value: string) =>
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((a, b) => {
    resolve = a;
    reject = b;
  });
  return { promise, resolve, reject };
}

function parameterFixture(parameters: unknown[], inherited: unknown[] = []) {
  const value = fixture();
  return {
    ...value,
    document: {
      ...value.document,
      components: {
        ...value.document.components,
        parameters: {
          Beta: {
            in: "query",
            name: "beta",
            description: "Original beta",
            example: "  original example\r\n",
          },
        },
      },
      paths: {
        "/read": {
          parameters: inherited,
          get: { ...value.document.paths["/read"].get, parameters },
        },
      },
    },
  };
}
const parameterRows = () =>
  Array.from(
    screen
      .getByRole("region", { name: "Operation parameters" })
      .querySelectorAll<HTMLElement>(".api-contract-field"),
  );
const parameterDetails = (row: HTMLElement) =>
  within(row).getByText("Complete decoded data").closest("details")!;
async function openParameter(row: HTMLElement) {
  fireEvent.click(within(row).getByText("Complete decoded data"));
  await within(parameterDetails(row)).findByRole("group", {
    name: "JSON document",
  });
}

it("keeps parameter disclosures on their resolved identities through failed refresh, insertion, reorder and removal", async () => {
  const beta = { $ref: "#/components/parameters/Beta" };
  // A matching name in another location is a distinct parameter.
  const headerBeta = { in: "header", name: "beta" };
  const inheritedBeta = {
    in: "query",
    name: "beta",
    description: "Overridden",
  };
  const inserted = { in: "header", name: "inserted" };
  const first = parameterFixture([beta], [headerBeta, inheritedBeta]);
  const pending = deferred<ReturnType<typeof parameterFixture>>();
  const replacement = parameterFixture(
    [beta],
    [inserted, headerBeta, inheritedBeta],
  );
  replacement.document.info.title = "Inserted";
  replacement.document.components.parameters.Beta.description = "Current beta";
  const currentExample = "  current β example\r\n";
  replacement.document.components.parameters.Beta.example = currentExample;
  const reordered = parameterFixture(
    [beta],
    [inheritedBeta, inserted, headerBeta],
  );
  reordered.document.info.title = "Reordered";
  const removed = parameterFixture([beta], [inheritedBeta, headerBeta]);
  removed.document.info.title = "Removed";
  const reference = vi
    .fn()
    .mockResolvedValueOnce(first)
    .mockReturnValueOnce(pending.promise)
    .mockResolvedValueOnce(replacement)
    .mockResolvedValueOnce(reordered)
    .mockResolvedValueOnce(removed);
  const writeText = vi.fn().mockResolvedValue(undefined);
  const fetch = vi.fn().mockRejectedValue(new Error("Unexpected network"));
  vi.stubGlobal("navigator", { clipboard: { writeText } });
  vi.stubGlobal("fetch", fetch);
  const { onFailure } = mount(reference);
  await screen.findByText("Read record");
  change("Search API operations", "Read");
  change("HTTP method", "GET");
  change("API area", "Stable");
  fireEvent.click(screen.getByText("Read record").closest("summary")!);
  await screen.findByRole("heading", { name: "Parameters" });
  definitions();
  change("Search definitions", "Field00");
  change("Component kind", "schemas");
  fireEvent.click(screen.getByRole("button", { name: "Open schemas/Field00" }));
  endpoints();
  const [headerBetaRow, betaRow] = parameterRows() as [
    HTMLElement,
    HTMLElement,
  ];
  expect(parameterRows()).toHaveLength(2);
  expect(betaRow.textContent).toContain("Original beta");
  expect(betaRow.textContent).not.toContain("Overridden");
  await openParameter(betaRow);
  const betaDetails = parameterDetails(betaRow);
  const yaml = screen
    .getByRole("link", { name: "Download OpenAPI YAML" })
    .getAttribute("href");
  fireEvent.click(screen.getByRole("button", { name: "Refresh contract" }));
  expect(screen.getByText(/Last loaded API contract; refreshing/)).toBeTruthy();
  expect(parameterRows()).toEqual([headerBetaRow, betaRow]);
  expect(betaDetails.open).toBe(true);
  expect(
    screen
      .getByRole("link", { name: "Download OpenAPI YAML" })
      .getAttribute("href"),
  ).toBe(yaml);
  await act(async () =>
    pending.reject(new Error("private dependency details")),
  );
  expect(
    screen.getByText(/Last loaded API contract; refresh failed/),
  ).toBeTruthy();
  expect(parameterRows()).toEqual([headerBetaRow, betaRow]);
  expect(betaDetails.open).toBe(true);
  expect(screen.getByRole("alert").textContent).not.toContain(
    "private dependency details",
  );
  fireEvent.click(screen.getByRole("button", { name: "Refresh contract" }));
  await screen.findByText("Inserted");
  const insertedRow = parameterRows()[0]!;
  expect(parameterRows()).toEqual([insertedRow, headerBetaRow, betaRow]);
  expect(parameterDetails(insertedRow).open).toBe(false);
  expect(parameterDetails(headerBetaRow).open).toBe(false);
  expect(parameterDetails(betaRow)).toBe(betaDetails);
  expect(betaDetails.open).toBe(true);
  expect(within(betaRow).getByText("Current beta")).toBeTruthy();
  expect(
    within(betaDetails).getByRole("group", { name: "JSON document" })
      .textContent,
  ).toContain('"$ref": "#/components/parameters/Beta"');
  fireEvent.click(
    within(betaRow).getByRole("button", { name: "Copy beta example" }),
  );
  await within(betaRow).findByText("Copied.");
  expect(writeText).toHaveBeenCalledExactlyOnceWith(currentExample);
  await openParameter(insertedRow);
  fireEvent.click(screen.getByRole("button", { name: "Refresh contract" }));
  await screen.findByText("Reordered");
  expect(parameterRows()).toEqual([betaRow, insertedRow, headerBetaRow]);
  expect(betaDetails.open).toBe(true);
  expect(parameterDetails(insertedRow).open).toBe(true);
  expect(parameterDetails(headerBetaRow).open).toBe(false);
  fireEvent.click(screen.getByRole("button", { name: "Refresh contract" }));
  await screen.findByText("Removed");
  expect(parameterRows()).toEqual([betaRow, headerBetaRow]);
  expect(insertedRow.isConnected).toBe(false);
  expect(betaDetails.open).toBe(true);
  expect(parameterDetails(headerBetaRow).open).toBe(false);
  expect(screen.getByText("Read record").closest("details")).toHaveProperty(
    "open",
    true,
  );
  expect(screen.getByLabelText("Search API operations")).toHaveProperty(
    "value",
    "Read",
  );
  expect(screen.getByLabelText("HTTP method")).toHaveProperty("value", "GET");
  expect(screen.getByLabelText("API area")).toHaveProperty("value", "Stable");
  definitions();
  expect(search()).toHaveProperty("value", "Field00");
  expect(screen.getByLabelText("Component kind")).toHaveProperty(
    "value",
    "schemas",
  );
  expect(
    within(selected()).getByRole("heading", { level: 3 }).textContent,
  ).toBe("#/components/schemas/Field00");
  expect(reference).toHaveBeenCalledTimes(5);
  expect(onFailure).toHaveBeenCalledOnce();
  expect(fetch).not.toHaveBeenCalled();
});

it("retains unresolved occurrences during failed reads and resets their disclosures on a replacement projection", async () => {
  const missing = { $ref: "#/missing" };
  const beta = { $ref: "#/components/parameters/Beta" };
  const first = parameterFixture([
    beta,
    missing,
    missing,
    null,
    { marker: "old" },
    ["opaque"],
  ]);
  const pending = deferred<ReturnType<typeof parameterFixture>>();
  const raw = [missing, beta, { marker: "new" }, missing, false, ["different"]];
  const replacement = parameterFixture(raw);
  replacement.document.info.title = "Replacement";
  const reference = vi
    .fn()
    .mockResolvedValueOnce(first)
    .mockReturnValueOnce(pending.promise)
    .mockResolvedValueOnce(replacement);
  const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
  mount(reference);
  await screen.findByText("Read record");
  fireEvent.click(screen.getByText("Read record").closest("summary")!);
  await screen.findByRole("heading", { name: "Parameters" });
  const initial = parameterRows();
  expect(initial).toHaveLength(6);
  await openParameter(initial[0]!);
  await openParameter(initial[1]!);
  await openParameter(initial[4]!);
  expect(parameterDetails(initial[2]!).open).toBe(false);
  change("Search API operations", "Read");
  expect(parameterRows()).toEqual(initial);
  fireEvent.click(screen.getByRole("button", { name: "Refresh contract" }));
  expect(parameterRows()).toEqual(initial);
  expect(parameterDetails(initial[1]!).open).toBe(true);
  await act(async () => pending.reject(new Error("failed read")));
  expect(parameterRows()).toEqual(initial);
  expect(parameterDetails(initial[4]!).open).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "Refresh contract" }));
  await screen.findByText("Replacement");
  const current = parameterRows();
  expect(current).toHaveLength(6);
  expect(current[1]).toBe(initial[0]);
  expect(parameterDetails(current[1]!).open).toBe(true);
  expect(
    current
      .filter((_, index) => index !== 1)
      .every((row) => !parameterDetails(row).open),
  ).toBe(true);
  expect(initial.slice(1).every((row) => !row.isConnected)).toBe(true);
  for (const [index, row] of current.entries()) {
    if (index !== 1) await openParameter(row);
    const source: unknown = JSON.parse(
      within(parameterDetails(row)).getByRole("group", {
        name: "JSON document",
      }).textContent!,
    );
    expect(source).toEqual(raw[index]);
  }
  expect(consoleError).not.toHaveBeenCalled();
  expect(reference).toHaveBeenCalledTimes(3);
});

it("pages every definition after complete search and preserves arbitrary kinds, exact names and falsy data", async () => {
  const { reference } = mount();
  await screen.findByText("1 of 1 operations");
  expect(
    screen.queryByRole("searchbox", { name: "Search definitions" }),
  ).toBeNull();
  definitions();
  change("Component kind", "schemas");
  expect(screen.getByRole("status").textContent).toBe(
    "70 of 72 definitions; page 1 of 3.",
  );
  expect(
    screen.getByRole("button", { name: "Previous definitions" }),
  ).toHaveProperty("disabled", true);
  expect(
    screen.getAllByRole("button", { name: /^Open schemas\// }),
  ).toHaveLength(25);
  fireEvent.click(screen.getByRole("button", { name: "Next definitions" }));
  expect(
    screen.getByRole("button", { name: "Open schemas/Field25" }),
  ).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Next definitions" }));
  expect(
    screen.getAllByRole("button", { name: /^Open schemas\// }),
  ).toHaveLength(20);
  expect(
    screen.getByRole("button", { name: "Next definitions" }),
  ).toHaveProperty("disabled", true);
  fireEvent.click(screen.getByRole("button", { name: "Previous definitions" }));
  fireEvent.click(screen.getByRole("button", { name: "First definitions" }));
  change("Search definitions", " unique LATE description ");
  expect(screen.getByRole("status").textContent).toBe(
    "1 of 72 definitions; page 1 of 1.",
  );
  fireEvent.click(screen.getByRole("button", { name: "Open schemas/Field69" }));
  expect(document.activeElement).toBe(selected());
  expect(
    within(selected()).getByRole("heading", { level: 3 }).textContent,
  ).toBe("#/components/schemas/Field69");
  change("Search definitions", "absent private text");
  expect(screen.getByText(/No definitions match/)).toBeTruthy();
  expect(
    within(selected()).getByText(/outside the current index page or criteria/),
  ).toBeTruthy();
  expect(location.href + JSON.stringify(history.state)).not.toContain(
    "private",
  );
  fireEvent.click(
    screen.getByRole("button", { name: "Clear definition filters" }),
  );
  expect(search()).toHaveProperty("value", "");
  change("Component kind", "x/~");
  fireEvent.click(screen.getByRole("button", { name: "Open x/~/custom/~" }));
  expect(
    within(selected()).getByRole("group", { name: "JSON document" })
      .textContent,
  ).toBe("0");
  change("Component kind", "broken");
  fireEvent.click(
    screen.getByRole("button", { name: "Open broken/(complete collection)" }),
  );
  expect(
    within(selected()).getByRole("group", { name: "JSON document" })
      .textContent,
  ).toBe("null");
  expect(reference).toHaveBeenCalledOnce();
});

it("opens exact linked and nested false definitions outside index criteria and explicitly reveals the hidden origin", async () => {
  const value = fixture();
  const child: Document = { type: "object", properties: { child: false } };
  value.document.components.schemas.Field69 =
    child as (typeof value.document.components.schemas)[string];
  mount(vi.fn().mockResolvedValue(value));
  await screen.findByText("Read record");
  change("Search API operations", "Read");
  change("HTTP method", "GET");
  change("API area", "Stable");
  const summary = screen.getByText("Read record").closest("summary")!;
  fireEvent.click(summary);
  fireEvent.click(
    await screen.findByRole("button", {
      name: "View components/schemas/Field69",
    }),
  );
  change("Search definitions", "Field00");
  change("Component kind", "schemas");
  const origin = (summary.closest("details") as HTMLDetailsElement).id;
  const nested = "#/components/schemas/Field69/properties/child";
  history.replaceState(
    { apiReferenceOrigin: origin },
    "",
    `#${encodeURIComponent("api-definition-" + encodeURIComponent(nested))}`,
  );
  fireEvent(window, new PopStateEvent("popstate"));
  expect(
    within(selected()).getByText("false: rejects every value"),
  ).toBeTruthy();
  expect(search()).toHaveProperty("value", "Field00");
  expect(screen.getByLabelText("Schema or shared definition")).toHaveProperty(
    "value",
    nested,
  );
  endpoints();
  change("Search API operations", "private blocked query");
  change("HTTP method", "POST");
  definitions();
  expect(
    screen.getByRole("button", { name: "Return to originating operation" }),
  ).toHaveProperty("disabled", true);
  fireEvent.click(
    screen.getByRole("button", { name: "Reveal originating operation" }),
  );
  expect(screen.getByLabelText("Search API operations")).toHaveProperty(
    "value",
    "",
  );
  expect(screen.getByLabelText("HTTP method")).toHaveProperty("value", "");
  expect(screen.getByLabelText("API area")).toHaveProperty("value", "");
  expect(document.activeElement).toBe(
    screen.getByText("Read record").closest("summary"),
  );
  definitions();
  expect(search()).toHaveProperty("value", "Field00");
});

it("retains loaded data and YAML through pending/failed refresh, then replaces a same-version contract atomically", async () => {
  const pending = deferred<ReturnType<typeof fixture>>();
  const first = fixture();
  const replacement = fixture();
  replacement.yaml = "exact after YAML";
  replacement.document.info.title = "After";
  replacement.document.components.schemas.Field69 = {
    type: "integer",
    description: "Changed schema",
  };
  const reference = vi
    .fn()
    .mockResolvedValueOnce(first)
    .mockReturnValueOnce(pending.promise)
    .mockResolvedValueOnce(replacement);
  const view = mount(reference);
  await screen.findByText("Read record");
  fireEvent.click(screen.getByText("Read record").closest("summary")!);
  await screen.findByRole("heading", { name: "Authentication" });
  fireEvent.click(screen.getByRole("button", { name: "Collapse Stable" }));
  change("Search API operations", "typed zero-match input");
  change("HTTP method", "POST");
  change("API area", "Stable");
  definitions();
  change("Search definitions", "Field6");
  change("Component kind", "schemas");
  fireEvent.click(screen.getByRole("button", { name: "Open schemas/Field69" }));
  const before = screen
    .getByRole("link", { name: "Download OpenAPI YAML" })
    .getAttribute("href");
  fireEvent.click(screen.getByRole("button", { name: "Refresh contract" }));
  expect(screen.getByText(/Last loaded API contract; refreshing/)).toBeTruthy();
  expect(
    screen
      .getByRole("link", { name: "Download OpenAPI YAML" })
      .getAttribute("href"),
  ).toBe(before);
  await act(async () =>
    pending.reject(new Error("private dependency details")),
  );
  expect(screen.getByRole("alert").textContent).not.toContain(
    "private dependency details",
  );
  expect(
    screen.getByText(/Last loaded API contract; refresh failed/),
  ).toBeTruthy();
  expect(
    within(selected()).getByRole("group", { name: "JSON document" })
      .textContent,
  ).toContain('"type": "string"');
  fireEvent.click(screen.getByRole("button", { name: "Refresh contract" }));
  await screen.findByText("After");
  expect(search()).toHaveProperty("value", "Field6");
  expect(screen.getByLabelText("Component kind")).toHaveProperty(
    "value",
    "schemas",
  );
  expect(
    within(selected()).getByRole("group", { name: "JSON document" })
      .textContent,
  ).toContain('"type": "integer"');
  const after = (
    await screen.findByRole("link", { name: "Download OpenAPI YAML" })
  ).getAttribute("href");
  expect(after).not.toBe(before);
  expect(URL.revokeObjectURL).toHaveBeenCalledWith(before);
  endpoints();
  expect(screen.getByLabelText("Search API operations")).toHaveProperty(
    "value",
    "typed zero-match input",
  );
  expect(screen.getByLabelText("HTTP method")).toHaveProperty("value", "POST");
  expect(screen.getByLabelText("API area")).toHaveProperty("value", "Stable");
  expect(screen.getByText("0 of 1 operations")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
  expect(screen.getByText("Read record").closest("details")).toHaveProperty(
    "open",
    true,
  );
  expect(reference).toHaveBeenCalledTimes(3);
  view.unmount();
  expect(URL.revokeObjectURL).toHaveBeenCalledWith(after);
});

it("explains removed area, kind, definition and origin without silently substituting a new record", async () => {
  const first = fixture();
  const replacement = {
    yaml: "replacement YAML",
    document: {
      info: { version: "1" },
      components: { headers: { New: { description: "New header" } } },
      paths: {
        "/new": { get: { tags: ["New area"], summary: "New endpoint" } },
      },
    },
  };
  const reference = vi
    .fn()
    .mockResolvedValueOnce(first)
    .mockResolvedValueOnce(replacement);
  mount(reference);
  await screen.findByText("Read record");
  change("Search API operations", "Read");
  change("HTTP method", "GET");
  change("API area", "Stable");
  fireEvent.click(screen.getByText("Read record").closest("summary")!);
  fireEvent.click(
    await screen.findByRole("button", {
      name: "View components/schemas/Field69",
    }),
  );
  change("Search definitions", "unsent input");
  change("Component kind", "x/~");
  fireEvent.click(screen.getByRole("button", { name: "Refresh contract" }));
  await screen.findByText(/selected definition was removed/);
  expect(screen.getByText(/selected API area was removed/)).toBeTruthy();
  expect(screen.getByText(/selected component kind was removed/)).toBeTruthy();
  expect(screen.getByText(/originating operation was removed/)).toBeTruthy();
  expect(
    screen.queryByRole("region", { name: "Selected definition" }),
  ).toBeNull();
  expect(search()).toHaveProperty("value", "unsent input");
  expect(screen.getByLabelText("Component kind")).toHaveProperty("value", "");
  expect(screen.getByRole("status").textContent).toBe(
    "0 of 1 definitions; page 1 of 1.",
  );
  expect(screen.getByLabelText("Schema or shared definition")).toHaveProperty(
    "value",
    "",
  );
  endpoints();
  expect(screen.getByLabelText("Search API operations")).toHaveProperty(
    "value",
    "Read",
  );
  expect(screen.getByLabelText("API area")).toHaveProperty("value", "");
  expect(screen.getByText("0 of 1 operations")).toBeTruthy();
});

it("recovers hidden operation history only on explicit reveal and treats malformed/unknown anchors as unavailable", async () => {
  mount();
  await screen.findByText("Read record");
  const id = screen.getByText("Read record").closest("details")!.id;
  change("Search API operations", "no match");
  change("HTTP method", "POST");
  history.replaceState(null, "", `#${encodeURIComponent(id)}`);
  fireEvent(window, new PopStateEvent("popstate"));
  expect(screen.getByLabelText("Search API operations")).toHaveProperty(
    "value",
    "no match",
  );
  fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
  expect(
    screen.queryByRole("button", { name: "Reveal linked operation" }),
  ).toBeNull();
  change("Search API operations", "no match");
  change("HTTP method", "POST");
  const areaId = "Stable";
  history.replaceState(
    null,
    "",
    `#${encodeURIComponent("api-area-" + encodeURIComponent(JSON.stringify(areaId)))}`,
  );
  fireEvent(window, new PopStateEvent("popstate"));
  expect(
    screen.getByText(/API section has no operations matching/),
  ).toBeTruthy();
  history.replaceState(null, "", `#${encodeURIComponent(id)}`);
  fireEvent(window, new PopStateEvent("popstate"));
  fireEvent.click(
    screen.getByRole("button", { name: "Reveal linked operation" }),
  );
  expect(document.activeElement).toBe(
    screen.getByText("Read record").closest("summary"),
  );
  history.replaceState(null, "", "#%E0%A4%A");
  fireEvent(window, new Event("hashchange"));
  expect(screen.getByText(/This link is unavailable/)).toBeTruthy();
  fireEvent.click(
    screen.getByRole("button", { name: "Open definition index" }),
  );
  expect(search()).toBeTruthy();
  history.replaceState(null, "", "#unknown");
  fireEvent(window, new Event("hashchange"));
  fireEvent.click(screen.getByRole("button", { name: "Open endpoint index" }));
  expect(screen.getByRole("navigation", { name: "API areas" })).toBeTruthy();
});

it("fences late success and failure after unmount and starts a fresh workspace without retained private input", async () => {
  const old = deferred<ReturnType<typeof fixture>>();
  const onFailure = vi.fn();
  const reference = vi.fn().mockReturnValue(old.promise);
  const first = mount(reference, onFailure);
  await act(async () => {});
  const signal = (reference.mock.calls[0] as unknown as [AbortSignal])[0];
  first.unmount();
  expect(signal.aborted).toBe(true);
  history.replaceState(null, "", "/api-reference");
  const next = mount();
  await screen.findByText("Read record");
  await act(async () =>
    old.resolve({
      ...fixture(),
      yaml: "late bytes",
      document: {
        ...fixture().document,
        info: { title: "Late stale title", version: "9" },
      },
    }),
  );
  expect(screen.queryByText("Late stale title")).toBeNull();
  expect(onFailure).not.toHaveBeenCalled();
  next.unmount();
  const failed = deferred<ReturnType<typeof fixture>>();
  const stale = mount(vi.fn().mockReturnValue(failed.promise), onFailure);
  await act(async () => {});
  stale.unmount();
  await act(async () => failed.reject(new Error("late private failure")));
  expect(onFailure).not.toHaveBeenCalled();
});

it("limits reference links without discarding full data or making later definitions unreachable", async () => {
  const value = {
    yaml: "local data",
    document: {
      components: {
        schemas: Object.fromEntries(
          Array.from({ length: 40 }, (_, i) => [`Schema${i}`, {}]),
        ),
        extra: {
          Set: {
            refs: Array.from({ length: 40 }, (_, i) => ({
              $ref: `#/components/schemas/Schema${i}`,
            })),
          },
        },
      },
    },
  };
  const { reference } = mount(vi.fn().mockResolvedValue(value));
  await screen.findByText("0 of 0 operations");
  definitions();
  change("Search definitions", "Set");
  fireEvent.click(screen.getByRole("button", { name: "Open extra/Set" }));
  expect(
    within(selected()).getByText(/Additional local references remain/),
  ).toBeTruthy();
  expect(
    within(selected()).getAllByRole("button", { name: /^View components/ }),
  ).toHaveLength(32);
  const raw: unknown = JSON.parse(
    within(selected()).getByRole("group", { name: "JSON document" })
      .textContent!,
  );
  expect(raw).toHaveProperty(
    "refs",
    Array.from({ length: 40 }, (_, i) => ({
      $ref: `#/components/schemas/Schema${i}`,
    })),
  );
  expect(reference).toHaveBeenCalledOnce();
});

it("retains collapsed sections on compatible refresh and restores a known definition index history entry", async () => {
  const reference = vi.fn().mockImplementation(async () => fixture());
  mount(reference);
  await screen.findByText("Read record");
  const stable = screen
    .getByRole("heading", { name: "Stable" })
    .closest("details")!;
  fireEvent.click(screen.getByRole("button", { name: "Collapse Stable" }));
  await act(async () =>
    fireEvent.click(screen.getByRole("button", { name: "Refresh contract" })),
  );
  expect(stable).toHaveProperty("open", false);
  history.replaceState(null, "", "#api-definitions");
  fireEvent(window, new PopStateEvent("popstate"));
  expect(document.activeElement).toBe(
    screen.getByRole("region", { name: "Schema and shared definitions" }),
  );
  change("Search definitions", "Field00");
  fireEvent.click(screen.getByRole("button", { name: "Open schemas/Field00" }));
  const count = history.length;
  fireEvent.click(screen.getByRole("link", { name: "Link to definition" }));
  expect(history.length).toBe(count);
  expect(document.activeElement).toBe(selected());
  expect(reference).toHaveBeenCalledTimes(2);
});
