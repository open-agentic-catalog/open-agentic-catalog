// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { SchemaSummary } from "./SchemaSummary";
import { schemaSummary } from "./schema-summary";

afterEach(cleanup);

it("keeps declared properties, requiredness, annotations, unions, dictionaries and composition distinct", () => {
  const value = {
    type: ["object", "null"],
    description: "Inert <script>text</script>",
    required: ["enabled"],
    readOnly: false,
    writeOnly: true,
    properties: {
      enabled: { type: "boolean", default: false },
      optional: { type: "integer", const: 0 },
      recursive: { $ref: "#/components/schemas/Tree" },
    },
    additionalProperties: false,
    items: true,
    prefixItems: [{ type: "string" }],
    allOf: [{ type: "object" }],
    oneOf: [{ type: "integer" }],
    anyOf: [{ type: "null" }],
  };
  const summary = schemaSummary({}, value);
  expect(summary.facts).toContainEqual(["Type", "object or null"]);
  expect(summary.facts).toContainEqual(["Required properties", '["enabled"]']);
  expect(summary.facts).toContainEqual(["readOnly annotation", "false"]);
  expect(summary.facts).toContainEqual(["writeOnly annotation", "true"]);
  expect(summary.children.map((node) => node.label)).toEqual([
    "Property: enabled (required)",
    "Property: optional (not listed as required)",
    "Property: recursive (not listed as required)",
    "Array items",
    "Additional properties",
    "prefixItems: positional item (1)",
    "allOf: all subschemas (1)",
    "oneOf: exactly one subschema (1)",
    "anyOf: one or more subschemas (1)",
  ]);
  const onReference = vi.fn();
  const view = render(
    <SchemaSummary document={{}} value={value} onReference={onReference} />,
  );
  expect(view.container.querySelector("script")).toBeNull();
  expect(screen.getByText("Inert <script>text</script>")).toBeTruthy();
  expect(screen.getAllByText("false")).toHaveLength(2);
  fireEvent.click(
    screen.getByRole("button", {
      name: "Inspect schema reference #/components/schemas/Tree",
    }),
  );
  expect(onReference).toHaveBeenCalledWith("#/components/schemas/Tree");
  expect(screen.getByText("false: rejects every value")).toBeTruthy();
});

it("retains invalid consumed fields and external references without inventing facts", () => {
  const value = {
    description: 42,
    required: [1],
    readOnly: "yes",
    writeOnly: null,
    properties: [],
    prefixItems: {},
    allOf: null,
    oneOf: [],
    anyOf: [],
    items: [],
    additionalProperties: 42,
    $ref: "https://unfetched.invalid/schema",
  };
  const summary = schemaSummary({}, value);
  expect(summary.unavailable).toBe(true);
  expect(summary.facts).toEqual([
    ["oneOf", "[] (declared empty)"],
    ["anyOf", "[] (declared empty)"],
  ]);
  render(<SchemaSummary document={{}} value={value} onReference={vi.fn()} />);
  expect(screen.getByText(/External or malformed reference/)).toBeTruthy();
  expect(screen.queryByRole("button")).toBeNull();
  expect(schemaSummary({}, true).facts).toEqual([
    ["Boolean schema", "true: accepts any value"],
  ]);
  expect(schemaSummary({}, false).facts).toEqual([
    ["Boolean schema", "false: rejects every value"],
  ]);
  expect(schemaSummary({}, null).unavailable).toBe(true);
  expect(
    schemaSummary({}, { required: [], properties: { a: { type: "string" } } })
      .children[0]?.label,
  ).toBe("Property: a (not listed as required)");
  expect(schemaSummary({}, { properties: { a: {} } }).children[0]?.label).toBe(
    "Property: a",
  );
});

it("bounds depth, total nodes, child rows and required-name summaries without expanding recursive targets", () => {
  const nested = {
    properties: {
      a: {
        properties: {
          b: { properties: { c: { properties: { d: { type: "string" } } } } },
        },
      },
    },
  };
  const deep = schemaSummary({}, nested);
  expect(deep.children[0]?.children[0]?.children[0]?.omitted).toBe(true);
  const huge = {
    required: Array.from({ length: 30 }, (_, i) => `field${i}`),
    properties: Object.fromEntries(
      Array.from({ length: 30 }, (_, i) => [
        `field${i}`,
        {
          type: "object",
          properties: Object.fromEntries(
            Array.from({ length: 30 }, (_, j) => [`nested${j}`, {}]),
          ),
        },
      ]),
    ),
  };
  const summary = schemaSummary({}, huge);
  const flatten = (
    node: ReturnType<typeof schemaSummary>,
  ): ReturnType<typeof schemaSummary>[] => [
    node,
    ...node.children.flatMap(flatten),
  ];
  const nodes = flatten(summary);
  expect(nodes).toHaveLength(80);
  expect(nodes.every((node) => node.children.length <= 20)).toBe(true);
  expect(summary.omitted).toBe(true);
  expect(
    summary.facts.find(([name]) => name === "Required properties")?.[1],
  ).toContain("additional required names");
  const recursive = { $ref: "#/components/schemas/Tree" };
  const ref = schemaSummary(
    { components: { schemas: { Tree: recursive } } },
    recursive,
  );
  expect(ref.children).toEqual([]);
  render(<SchemaSummary document={{}} value={nested} onReference={vi.fn()} />);
  expect(screen.getByText(/Schema summary limit reached/)).toBeTruthy();
});
