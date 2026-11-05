import { required } from "../../tests/required";
import { describe, expect, it } from "vitest";
import {
  areaAnchor,
  localValue,
  matches,
  operationAreas,
  operations,
  referenceValue,
  references,
} from "./reference";

describe("Read-only API reference", () => {
  it("includes inherited parameters and security, preserving explicit overrides", () => {
    const document = {
      security: [{ bearerAuth: [] }],
      components: {
        parameters: { Id: { in: "path", name: "id", required: true } },
      },
      paths: {
        "/items/{id}": {
          parameters: [
            { $ref: "#/components/parameters/Id" },
            { in: "query", name: "limit", required: false },
          ],
          description: "Not an operation",
          get: {
            operationId: "getItem",
            tags: ["Catalog"],
            summary: "Read item",
            responses: { 200: {} },
          },
          post: {
            operationId: "changeItem",
            parameters: [{ in: "query", name: "limit", required: true }],
            security: [],
            requestBody: { $ref: "#/components/requestBodies/Change" },
          },
        },
      },
    };
    const items = operations(document);
    expect(items).toHaveLength(2);
    expect(required(items[0]).parameters).toEqual([
      { $ref: "#/components/parameters/Id" },
      { in: "query", name: "limit", required: false },
    ]);
    expect(required(items[0]).security).toEqual(document.security);
    expect(required(items[1]).parameters).toHaveLength(2);
    expect(required(items[1]).parameters[1]).toEqual({
      in: "query",
      name: "limit",
      required: true,
    });
    expect(required(items[1]).security).toEqual([]);
    expect(matches(required(items[0]), " GETitem ", "Catalog")).toBe(true);
    expect(matches(required(items[0]), "/items", "Webhooks")).toBe(false);
    expect(matches(required(items[0]), "nonexistent", "")).toBe(false);
    expect(matches(required(items[0]), "GET", "")).toBe(true);
  });
  it("resolves only own local pointers and leaves recursive or remote refs as data", () => {
    const document = {
      components: {
        schemas: {
          "a/b~c": { $ref: "#/components/schemas/a~1b~0c" },
          "repeat/~1/~~//": { type: "boolean" },
        },
      },
    };
    const ref = "#/components/schemas/a~1b~0c";
    expect(referenceValue(document, ref)).toEqual({ $ref: ref });
    expect(
      referenceValue(document, "#/components/schemas/repeat~1~01~1~0~0~1~1"),
    ).toEqual({ type: "boolean" });
    for (const input of [
      "https://example.com/private",
      "#/constructor",
      "#/components/missing",
      "#/components/schemas/toString",
    ])
      expect(referenceValue(document, input)).toBeUndefined();
    expect(
      references([
        { $ref: ref },
        { $ref: ref },
        { $ref: "https://example.com" },
        null,
      ]),
    ).toEqual([ref]);
    expect(operations({ paths: null })).toEqual([]);
  });
  it("retains unresolved parameters and defaults incomplete operation metadata safely", () => {
    const items = operations({
      paths: {
        "/empty": { get: {} },
        "/parameters": {
          parameters: [{ $ref: "#/missing" }, { $ref: "#/another" }],
          post: {
            tags: ["Valid", null, 42],
            parameters: [{ $ref: "#/missing" }],
          },
        },
      },
    });
    expect(items[0]).toMatchObject({
      id: "get:/empty",
      parameters: [],
      security: [],
      tags: [],
      responses: undefined,
    });
    expect(required(items[1]).parameters).toEqual([
      { $ref: "#/missing" },
      { $ref: "#/another" },
      { $ref: "#/missing" },
    ]);
    expect(required(items[1]).tags).toEqual(["Valid"]);
  });
  it("orders exact references independently of traversal order without merging Unicode spellings", () => {
    const names = [
      "\u00e9",
      "a_b",
      "a",
      "A",
      "e\u0301",
      "a/b",
      "a-b",
      "\uffff",
      "\ud83d\ude00",
    ];
    const values = names.map((name) => ({ $ref: `#/schemas/${name}` }));
    const expected = [
      "A",
      "a",
      "a-b",
      "a/b",
      "a_b",
      "e\u0301",
      "\u00e9",
      "\ud83d\ude00",
      "\uffff",
    ].map((name) => `#/schemas/${name}`);
    expect(references([values, values[0]])).toEqual(expected);
    expect(references([...values].reverse())).toEqual(expected);
    expect(values.map((value) => value.$ref)).toEqual(
      names.map((name) => `#/schemas/${name}`),
    );
  });

  it("gives each operation its last-tag home in declaration order with exact, defensive fallbacks", () => {
    const document = {
      tags: [
        { name: "Second", description: "Declared second" },
        { name: "First", description: "Declared first" },
        { name: "Second", description: "Do not replace the first declaration" },
        { name: "Unused", description: 42 },
        { name: "" },
        { name: null },
        null,
        "First",
      ],
      paths: {
        "/first": {
          post: { tags: ["First"] },
          get: { tags: ["Second", "First", "Second"], deprecated: true },
        },
        "/unicode": {
          get: { tags: ["\u00e9"] },
          post: { tags: ["e\u0301"], deprecated: "true" },
        },
        "/untagged": { get: { tags: ["", null, 42] }, post: { tags: "First" } },
        "/named-fallback": { get: { tags: ["Untagged operations"] } },
      },
    };
    const items = operations(document);
    const areas = operationAreas(document, items);
    expect(
      areas.map((area) => [area.tag, area.operations.map((item) => item.id)]),
    ).toEqual([
      ["Second", ["get:/first"]],
      ["First", ["post:/first"]],
      ["Unused", []],
      ["Untagged operations", ["get:/named-fallback"]],
      ["e\u0301", ["post:/unicode"]],
      ["\u00e9", ["get:/unicode"]],
      [undefined, ["get:/untagged", "post:/untagged"]],
    ]);
    expect(areas.map((area) => area.description)).toEqual([
      "Declared second",
      "Declared first",
      "",
      "",
      "",
      "",
      "These operations have no API area tag in this contract.",
    ]);
    expect(areas.flatMap((area) => area.operations)).toHaveLength(7);
    expect(new Set(areas.map((area) => area.id)).size).toBe(7);
    expect(items[1]?.deprecated).toBe(true);
    expect(items[3]?.deprecated).toBe(false);
    expect(operationAreas({ tags: null }, [])).toEqual([]);
    expect(
      operationAreas(
        {},
        operations({ paths: { "/plain": { get: { tags: ["First"] } } } }),
      )[0]?.name,
    ).toBe("First");
  });

  it("intersects local text, exact tag membership and HTTP methods without normalizing names", () => {
    const item = required(
      operations({
        paths: {
          "/items": {
            get: {
              operationId: "readItem",
              tags: ["Identity", " Exact area "],
              description: "A description",
            },
          },
        },
      })[0],
    );
    expect(matches(item, "identity", "", "GET")).toBe(true);
    expect(matches(item, " exact AREA ", "Identity", "GET")).toBe(true);
    expect(matches(item, "READITEM", " Exact area ", "GET")).toBe(true);
    expect(matches(item, "description", "Identity", "POST")).toBe(false);
    expect(matches(item, "", "Exact area", "GET")).toBe(false);
    expect(matches(item, "", "Identity", "get")).toBe(false);
    expect(matches(item, "absent", "Identity", "GET")).toBe(false);
  });

  it("keeps section anchors unique for exact names, fallback names and malformed Unicode", () => {
    expect(areaAnchor(undefined)).toBe("api-area-untagged");
    expect(areaAnchor("A/B")).toBe("api-area-%22A%2FB%22");
    const names = [
      "",
      "untagged",
      "Untagged operations",
      "a/b",
      "a~1b",
      "a b",
      "a-b",
      "é",
      "e\u0301",
      "\ud800",
      "\ufffd",
    ];
    expect(
      new Set([areaAnchor(undefined), ...names.map(areaAnchor)]).size,
    ).toBe(names.length + 1);
  });
  it("bounds local chains and preserves false, null and missing targets without fetching", () => {
    const document = {
      nodes: Object.fromEntries(
        Array.from({ length: 17 }, (_, index) => [
          String(index),
          index === 16 ? false : { $ref: `#/nodes/${index + 1}` },
        ]),
      ),
    };
    expect(localValue(document, { $ref: "#/nodes/1" })).toBe(false);
    expect(localValue(document, { $ref: "#/nodes/0" })).toBeUndefined();
    expect(
      localValue({ loop: { $ref: "#/loop" } }, { $ref: "#/loop" }),
    ).toBeUndefined();
    expect(
      localValue({}, { $ref: "https://unfetched.invalid/schema" }),
    ).toBeUndefined();
    expect(localValue({}, { $ref: 42 })).toBeUndefined();
    expect(localValue({}, { $ref: "#/missing" })).toBeUndefined();
    expect(localValue({}, null)).toBeNull();
    expect(localValue({}, 0)).toBe(0);
  });

  it("retains malformed declarations and distinguishes missing from explicit invalid authentication", () => {
    const malformed = operations({
      security: null,
      paths: {
        "/inherited": {
          parameters: null,
          get: { parameters: { name: 42 }, responses: null },
        },
        "/explicit": {
          parameters: [
            null,
            {},
            { in: "query", name: "Case" },
            { in: "query", name: "case" },
          ],
          post: {
            security: null,
            operationId: "declared",
            parameters: [false, { in: "query", name: "Case", required: false }],
          },
        },
      },
    });
    expect(malformed[0]).toMatchObject({
      declaredId: "",
      security: null,
      parameters: [null, { name: 42 }],
      responses: null,
    });
    expect(malformed[1]).toMatchObject({
      declaredId: "declared",
      security: null,
      parameters: [
        null,
        {},
        { in: "query", name: "Case", required: false },
        { in: "query", name: "case" },
        false,
      ],
    });
    expect(
      operations({ paths: { "/default": { get: {} } } })[0]?.security,
    ).toEqual([]);
    expect(
      operations({
        security: [{ bearer: [] }],
        paths: { "/override": { get: { security: [] } } },
      })[0]?.security,
    ).toEqual([]);
  });
});

it("resolves own array elements in composed-schema pointers without exposing array properties or noncanonical indices", () => {
  const document = {
    components: {
      schemas: {
        Choice: {
          oneOf: [
            false,
            { type: "object", properties: { value: { type: "integer" } } },
          ],
        },
      },
    },
    numeric: { "01": "literal object key" },
  };
  expect(referenceValue(document, "#/components/schemas/Choice/oneOf/0")).toBe(
    false,
  );
  expect(
    referenceValue(
      document,
      "#/components/schemas/Choice/oneOf/1/properties/value",
    ),
  ).toEqual({ type: "integer" });
  expect(referenceValue(document, "#/numeric/01")).toBe("literal object key");
  for (const index of [
    "01",
    "-1",
    "+1",
    "-",
    "length",
    "constructor",
    "999999999999999999",
  ])
    expect(
      referenceValue(document, `#/components/schemas/Choice/oneOf/${index}`),
    ).toBeUndefined();
});
