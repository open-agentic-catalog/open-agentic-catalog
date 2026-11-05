// Pure facet tests cover supported dimensions and bounds without service fixtures.
import { expect, it } from "vitest";
import { refineFacet } from "./facet-filters";
import type { FacetQuery } from "./facet-filters";

it("refines any-kind/all-tag criteria and replaces scalar filters without changing text", () => {
  const query: FacetQuery = {
    text: "orders",
    kinds: ["agent"],
    tags: ["one"],
    publisher: "old",
    visibility: "tenant",
  };
  expect(refineFacet(query, "kind", "skill")).toEqual({
    ...query,
    kinds: ["agent", "skill"],
  });
  expect(refineFacet(query, "tag", "two")).toEqual({
    ...query,
    tags: ["one", "two"],
  });
  expect(refineFacet(query, "publisher", "new")).toEqual({
    ...query,
    publisher: "new",
  });
  expect(refineFacet(query, "visibility", "public")).toEqual({
    ...query,
    visibility: "public",
  });
  expect(refineFacet({}, "visibility", "private")).toEqual({
    visibility: "private",
  });
  expect(refineFacet({}, "kind", "plugin")).toEqual({ kinds: ["plugin"] });
  expect(refineFacet({}, "tag", "\u{1f642}".repeat(80))).toEqual({
    tags: ["\u{1f642}".repeat(80)],
  });
  for (const [field, value] of [
    ["kind", "agent"],
    ["tag", "one"],
    ["publisher", "old"],
    ["visibility", "tenant"],
  ] as const)
    expect(refineFacet(query, field, value)).toBe(query);
  expect(query).toEqual({
    text: "orders",
    kinds: ["agent"],
    tags: ["one"],
    publisher: "old",
    visibility: "tenant",
  });
});

it("keeps unsupported values inert and enforces tag bounds without blocking selected tags", () => {
  for (const [field, value] of [
    ["namespace", "one"],
    ["trustState", "approved"],
    ["source", "one"],
    ["kind", "unknown"],
    ["visibility", "unknown"],
    ["publisher", ""],
    ["tag", ""],
    ["tag", "\u{1f642}".repeat(81)],
  ] as const)
    expect(refineFacet({}, field, value)).toBeUndefined();
  const query = { tags: Array.from({ length: 32 }, (_, i) => `tag-${i}`) };
  expect(refineFacet(query, "tag", "extra")).toBeUndefined();
  expect(refineFacet(query, "tag", "tag-0")).toBe(query);
});

it("refines only an exact local namespace UUID", () => {
  const id = "00000000-0000-7000-8000-000000000001";
  const query = { text: "saved", namespaceId: id };
  expect(refineFacet({}, "namespace", id)).toEqual({ namespaceId: id });
  expect(refineFacet(query, "namespace", id)).toBe(query);
  expect(refineFacet({}, "namespace", "foreign-label")).toBeUndefined();
});

it("refines exact categories without splitting literal commas or changing other criteria", () => {
  const query = { text: "saved", license: "MIT", categories: ["tools"] };
  expect(refineFacet(query, "category", "tools")).toBe(query);
  expect(refineFacet(query, "category", "operations, tools")).toEqual({
    ...query,
    categories: ["tools", "operations, tools"],
  });
  for (const value of ["", " ", "x\0", "🙂".repeat(81)])
    expect(refineFacet(query, "category", value)).toBeUndefined();
  const full = {
    categories: Array.from({ length: 16 }, (_, index) => `category-${index}`),
  };
  expect(refineFacet(full, "category", "extra")).toBeUndefined();
  expect(refineFacet(full, "category", "category-0")).toBe(full);
});
