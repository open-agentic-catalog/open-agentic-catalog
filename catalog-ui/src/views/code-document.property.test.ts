/// <reference types="node" />
import { env } from "node:process";
import { expect, it } from "vitest";
import fc from "fast-check";
import { jsonDocument } from "./code-document";

type Value =
  | { type: "scalar"; text: string; kind: string }
  | { type: "array"; values: Value[] }
  | { type: "object"; entries: { key: string; value: Value }[] };

const scalar: fc.Arbitrary<Value> = fc.oneof(
  fc
    .constantFrom("true", "false", "null")
    .map((text) => ({ type: "scalar" as const, text, kind: "literal" })),
  fc
    .constantFrom(
      "-0",
      "9007199254740993",
      "12.345",
      "1e23",
      "1.00E-123",
      "1e+400",
    )
    .map((text) => ({ type: "scalar" as const, text, kind: "number" })),
  fc
    .oneof(
      fc.string({ maxLength: 20 }).map((s) => JSON.stringify(s)),
      fc.constantFrom(
        '"\\u0061"',
        '"\\uD83D\\uDE00"',
        JSON.stringify('{},[]:"\\'),
      ),
    )
    .map((text) => ({ type: "scalar" as const, text, kind: "string" })),
);
function value(depth: number): fc.Arbitrary<Value> {
  if (!depth) return scalar;
  const child = value(depth - 1);
  return fc.oneof(
    scalar,
    fc
      .array(child, { maxLength: 4 })
      .map((values) => ({ type: "array" as const, values })),
    // Arrays of entries deliberately retain duplicate names and their order.
    fc
      .array(
        fc.record({
          key: fc.constantFrom('"n"', '"n"', '"a\\"b"', '"\\u0061"'),
          value: child,
        }),
        { maxLength: 4 },
      )
      .map((entries) => ({ type: "object" as const, entries })),
  );
}
function tokens(node: Value): { text: string; kind: string }[] {
  if (node.type === "scalar") return [{ text: node.text, kind: node.kind }];
  const punctuation = (text: string) => ({ text, kind: "punctuation" });
  const entries =
    node.type === "array"
      ? node.values.map(tokens)
      : node.entries.map(({ key, value }) => [
          { text: key, kind: "key" },
          punctuation(":"),
          ...tokens(value),
        ]);
  return [
    punctuation(node.type === "array" ? "[" : "{"),
    ...entries.flatMap((entry, index) =>
      index ? [punctuation(","), ...entry] : entry,
    ),
    punctuation(node.type === "array" ? "]" : "}"),
  ];
}
// Build the expected display from the generated tree, independently of the lexer.
function pretty(node: Value, depth = 0): string {
  if (node.type === "scalar") return node.text;
  const [open, close] = node.type === "array" ? ["[", "]"] : ["{", "}"];
  const entries =
    node.type === "array"
      ? node.values.map((v) => pretty(v, depth + 1))
      : node.entries.map(
          ({ key, value }) => key + ": " + pretty(value, depth + 1),
        );
  return !entries.length
    ? open + close
    : open +
        "\n" +
        entries.map((entry) => "  ".repeat(depth + 1) + entry).join(",\n") +
        "\n" +
        "  ".repeat(depth) +
        close;
}
const runs = env.UI_PROPERTY_RUNS ?? "100";
const seed = env.UI_PROPERTY_SEED ?? "42";
const path = env.UI_PROPERTY_PATH;
if (
  !/^[1-9][0-9]{0,4}$/.test(runs) ||
  Number(runs) > 10000 ||
  !/^[0-9]{1,10}$/.test(seed) ||
  Number(seed) > 2147483647 ||
  (path !== undefined && !/^[0-9]+(?::[0-9]+)*$/.test(path))
)
  throw new Error("Invalid UI property runs, seed or replay path");

it("preserves generated JSON literals, token categories and independent formatting", () => {
  fc.assert(
    fc.property(
      value(3),
      fc.array(fc.constantFrom("", " ", "\r\n", "\t "), {
        minLength: 1,
        maxLength: 8,
      }),
      (node, spaces) => {
        const expected = tokens(node);
        const source =
          expected
            .map((token, index) => spaces[index % spaces.length] + token.text)
            .join("") + spaces[0];
        const document = jsonDocument(source);
        expect(document).toBeDefined();
        expect(document!.original.map((token) => token.text).join("")).toBe(
          source,
        );
        expect(
          document!.original.filter((token) => token.kind !== "space"),
        ).toEqual(expected);
        expect(
          document!.formatted.filter((token) => token.kind !== "space"),
        ).toEqual(expected);
        expect(document!.formatted.map((token) => token.text).join("")).toBe(
          pretty(node),
        );
        expect(jsonDocument(pretty(node))!.formatted).toEqual(
          document!.formatted,
        );
      },
    ),
    {
      numRuns: Number(runs),
      seed: Number(seed),
      ...(path === undefined ? {} : { path }),
    },
  );
});
