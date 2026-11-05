import { describe, expect, it } from "vitest";
import { applyFilters, categoryFacetQuery, mergeTags } from "./search-filters";

it("projects applied local criteria without Search origins, limit or cursor", () => {
  const query = {
    text: "applied",
    tags: ["tag"],
    categories: ["data, tools"],
    license: "MIT",
    publisher: "publisher",
    visibility: "public" as const,
    namespaceId: "namespace",
    kinds: ["plugin" as const],
    origins: ["local" as const, "live" as const],
    limit: 24,
    cursor: "old",
  };
  expect(categoryFacetQuery(query)).toEqual({
    text: "applied",
    tags: ["tag"],
    categories: ["data, tools"],
    license: "MIT",
    publisher: "publisher",
    visibility: "public",
    namespaceId: "namespace",
    kinds: ["plugin"],
  });
  expect(query.cursor).toBe("old");
});

describe("Search filter state", () => {
  it("replaces only metadata filters and invalidates the previous cursor", () => {
    const query = {
      text: "orders",
      kinds: ["skill" as const],
      limit: 24,
      cursor: "old",
      tags: ["old"],
      publisher: "old",
      visibility: "private" as const,
    };
    expect(
      applyFilters(query, {
        kinds: ["agent", "skill"],
        tags: ["new"],
        publisher: " publisher ",
        visibility: "public",
      }),
    ).toEqual({
      text: "orders",
      kinds: ["agent", "skill"],
      limit: 24,
      tags: ["new"],
      publisher: "publisher",
      visibility: "public",
    });
    expect(applyFilters(query, {})).toEqual({
      text: "orders",
      limit: 24,
    });
    expect(applyFilters(query, { kinds: [] })).toEqual({
      text: "orders",
      limit: 24,
    });
    expect(query.kinds).toEqual(["skill"]);
    expect(query.tags).toEqual(["old"]);
  });
  it("normalizes entered tags without losing selected tags or changing case", () => {
    expect(mergeTags(["orders", "support"], " Orders ")).toEqual([
      "orders",
      "support",
      "Orders",
    ]);
    expect(mergeTags([], "")).toEqual([]);
    expect(mergeTags([], "🙂".repeat(80))).toHaveLength(1);
    const unicodeTags = Array.from({ length: 32 }, (_, index) =>
      String.fromCodePoint(0x1f600 + index).repeat(80),
    );
    expect(mergeTags(unicodeTags.slice(0, 31), unicodeTags[31]!)).toEqual(
      unicodeTags,
    );
    expect(mergeTags(["orders"], "design, review")).toEqual([
      "orders",
      "design, review",
    ]);
    expect(() => mergeTags([], "🙂".repeat(81))).toThrow("80 characters");
    expect(() => mergeTags([], "x".repeat(81))).toThrow("80 characters");
    expect(() =>
      mergeTags(
        Array.from({ length: 32 }, (_, n) => String(n)),
        "extra",
      ),
    ).toThrow("32 tags");
  });
});

it("applies local namespace and explicit origins without retaining a cursor", () => {
  expect(
    applyFilters(
      {
        text: "saved",
        cursor: "old",
        namespaceId: "previous",
        origins: ["live"],
      },
      { namespaceId: "selected", origins: ["indexed"] },
    ),
  ).toEqual({ text: "saved", namespaceId: "selected", origins: ["local"] });
  expect(
    applyFilters(
      { text: "saved", namespaceId: "old", origins: ["local"] },
      { origins: ["indexed", "live"] },
    ),
  ).toEqual({ text: "saved", origins: ["indexed", "live"] });
});

it("declaration filters preserve selected origins, retain text and reset continuation without mutating inputs", () => {
  const query = {
    text: "saved",
    cursor: "previous",
    license: "old",
    categories: ["old"],
    tags: ["exact"],
    origins: ["indexed" as const],
  };
  const filters = {
    license: " Apache-2.0 ",
    categories: ["tools"],
    tags: ["exact"],
    origins: ["live" as const],
  };
  expect(applyFilters(query, filters)).toEqual({
    text: "saved",
    license: "Apache-2.0",
    categories: ["tools"],
    tags: ["exact"],
    origins: ["live"],
  });
  expect(applyFilters(query, { categories: ["tools"] })).toEqual({
    text: "saved",
    categories: ["tools"],
  });
  expect(
    applyFilters(query, { license: "", categories: [], origins: ["indexed"] }),
  ).toEqual({ text: "saved", origins: ["indexed"] });
  expect(query.categories).toEqual(["old"]);
  expect(filters.categories).toEqual(["tools"]);
});
