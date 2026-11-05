import { describe, expect, it } from "vitest";
import {
  checkedCriteria,
  criteriaDraft,
  validCriteria,
} from "./personal-criteria";
import { InputError } from "./input-validation";

describe("private saved criteria", () => {
  const namespaceId = "00000000-0000-7000-8000-000000000001";
  it.each([
    {},
    { text: "", tags: ["", " spaced ", "a,b", "a,b"], kinds: [] },
    {
      text: "é".repeat(250),
      origins: ["local"],
      namespaceId,
      categories: ["é".repeat(80), " repeated ", " repeated "],
      publisher: "é".repeat(128),
      license: "🙂".repeat(256),
      visibility: "private",
    },
    { origins: ["live", "indexed", "local"] },
  ])("preserves supported values and origin omission: %j", (value) => {
    expect(checkedCriteria(value)).toEqual(value);
    expect(validCriteria(value)).toBe(true);
  });
  it.each([
    null,
    [],
    { text: null },
    { namespaceId: 1 },
    { visibility: "unknown" },
    { origins: ["unknown"] },
    { kinds: ["unknown"] },
    { tags: "tag" },
    { categories: [1] },
    { kinds: Array(5).fill("skill") },
    { tags: Array(33).fill("tag") },
    { categories: Array(17).fill("category") },
    { cursor: "opaque" },
    { limit: 25 },
    { sourceUrl: "https://source.example" },
    { token: "credential" },
  ])("rejects unsupported shapes: %j", (value) => {
    expect(() => criteriaDraft(value)).toThrow();
    expect(validCriteria(value)).toBe(false);
  });
  it.each([
    ["text", "é".repeat(251)],
    ["text", "\0"],
    ["text", "\ud800"],
    ["text", "\udc00"],
    ["publisher", "é".repeat(129)],
    ["license", "x".repeat(257)],
    ["license", "  "],
    ["namespaceId", "partial"],
    ["namespaceId", ""],
    ["origins", []],
    ["origins", ["local", "local"]],
    ["kinds", ["skill", "skill"]],
    ["tags", ["x".repeat(81)]],
    ["tags", ["\ud800"]],
    ["categories", [""]],
    ["categories", ["  "]],
    ["categories", ["\0"]],
  ])("links an invalid %s value to its owning field", (field, value) => {
    try {
      checkedCriteria({ [field as string]: value }, "editor");
      throw new Error("invalid value accepted");
    } catch (error) {
      expect(error).toBeInstanceOf(InputError);
      expect(error).toMatchObject({
        fieldId: `editor-${field}`,
        fallbackId: "editor",
      });
    }
  });
  it("rejects namespace/external combinations without rewriting them", () => {
    expect(validCriteria({ namespaceId, origins: ["indexed"] })).toBe(false);
    expect(validCriteria({ namespaceId })).toBe(true);
    expect(validCriteria({ namespaceId, origins: ["local"] })).toBe(true);
    expect(() => checkedCriteria({ cursor: "not-stored" }, "editor")).toThrow(
      "supported criteria",
    );
  });
});

it.each([
  "name_asc",
  "name_desc",
  "kind_asc",
  "kind_desc",
  "publisher_asc",
  "publisher_desc",
])("retains the saved %s ordering without executing search", (order) => {
  expect(checkedCriteria({ text: "tools", order })).toEqual({
    text: "tools",
    order,
  });
});
it.each(["", "rating_desc", null, 8])(
  "rejects unsupported saved ordering %j",
  (order) => {
    expect(validCriteria({ order })).toBe(false);
  },
);
