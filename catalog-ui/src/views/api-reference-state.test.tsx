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
