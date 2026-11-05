import { expect, it } from "vitest";
import {
  definitionAnchor,
  definitionIndex,
  linkedDefinition,
  matchingDefinitions,
  operationAnchor,
} from "./definitions";
import { referenceValue } from "./reference";

it("indexes exact names, all kinds and unsupported collections, then searches before paging", () => {
  const document = {
    components: {
      schemas: {
        é: false,
        "e\u0301": { description: "Unicode separate" },
        "a/~": { $ref: "#/components/parameters/Shared" },
        ...Object.fromEntries(
          Array.from({ length: 60 }, (_, i) => [
            `Entry${i}`,
            { description: i === 59 ? "Last searchable description" : "" },
          ]),
        ),
      },
      parameters: { Shared: { description: "Alias description" } },
      "extra/~": { "named/~": 0 },
      malformed: null,
      list: [],
      empty: {},
      scalar: 42,
    },
  };
  const index = definitionIndex(document);
  expect(index).toHaveLength(68);
  expect(
    matchingDefinitions(index, "LAST SEARCHABLE", "schemas").map(
      (entry) => entry.name,
    ),
  ).toEqual(["Entry59"]);
  expect(
    matchingDefinitions(index, "Alias", "schemas").map((entry) => entry.name),
  ).toEqual(["a/~"]);
  expect(matchingDefinitions(index, "Alias", "parameters")).toHaveLength(1);
  expect(matchingDefinitions(index, "absent", "")).toEqual([]);
  expect(matchingDefinitions(index, "", "")).toEqual(index);
  const exact = index.find((entry) => entry.kind === "extra/~")!;
  expect(exact.reference).toBe("#/components/extra~1~0/named~1~0");
  expect(referenceValue(document, exact.reference)).toBe(0);
  expect(
    index
      .filter((entry) => entry.kind === "schemas")
      .slice(-2)
      .map((entry) => entry.name),
  ).toEqual(["e\u0301", "é"]);
  expect(
    index
      .filter((entry) => entry.name === "(complete collection)")
      .map((entry) => entry.kind),
  ).toEqual(["list", "malformed", "scalar"]);
  expect(definitionIndex({ components: null })).toEqual([]);
});

it("retains ordinary anchors, rejects unknown or malformed destinations and handles nested/falsy targets", () => {
  const document = {
    components: {
      schemas: {
        "a/~": { properties: { child: false } },
        zero: 0,
        nil: null,
        "\ud800": false,
      },
    },
  };
  expect(operationAnchor({ method: "GET", path: "/items" })).toBe(
    "api-operation-GET%3A%2Fitems",
  );
  expect(operationAnchor({ method: "GET", path: "/\ud800" })).toMatch(
    /^api-operation-json-/,
  );
  for (const reference of [
    "#/components/schemas/a~1~0",
    "#/components/schemas/a~1~0/properties/child",
    "#/components/schemas/zero",
    "#/components/schemas/nil",
    "#/components/schemas/\ud800",
  ]) {
    expect(linkedDefinition(document, definitionAnchor(reference))).toBe(
      reference,
    );
  }
  for (const anchor of [
    "other",
    "api-definition-%",
    "api-definition-json-%",
    "api-definition-json-42",
    definitionAnchor("https://unfetched.invalid"),
    definitionAnchor("#/missing"),
    "api-definition-%23%2fcomponents%2fschemas%2fzero",
  ])
    expect(linkedDefinition(document, anchor)).toBeUndefined();
});
