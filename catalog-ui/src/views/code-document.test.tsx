import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { CodeBlock, JSONBlock } from "./CodeBlock";
import { jsonDocument } from "./code-document";

describe("JSON presentation", () => {
  it("indents objects and arrays and classifies escaped keys and scalar values", () => {
    const source = '{"a\\"b":[true,false,null,-1.2e+3,"a\\nb"],"empty":{}}';
    const document = jsonDocument(source)!;
    const formatted = document.formatted.map((token) => token.text).join("");
    expect(document.original.map((token) => token.text).join("")).toBe(source);
    expect(JSON.parse(formatted)).toEqual(JSON.parse(source));
    expect(formatted).toContain('\n  "empty": {}\n}');
    expect(
      document.original
        .filter((token) => token.kind === "key")
        .map((token) => token.text),
    ).toEqual(['"a\\"b"', '"empty"']);
    expect(new Set(document.formatted.map((token) => token.kind))).toEqual(
      new Set(["key", "string", "number", "literal", "punctuation", "space"]),
    );
  });
  it("preserves duplicate keys, exact numbers, whitespace and escape spellings", () => {
    const source =
      ' \r\n{"n":9007199254740993,"n":1.00e+99,"zero":-0,"s":"\\u0061"}\t';
    const document = jsonDocument(source)!;
    expect(document.original.map((token) => token.text).join("")).toBe(source);
    const output = document.formatted.map((token) => token.text).join("");
    expect(output).toContain('"n": 9007199254740993,\n  "n": 1.00e+99');
    expect(output).toContain('"zero": -0');
    expect(output).toContain('"s": "\\u0061"');
  });
  it.each(["null", "false", "0", '"text"', "[]", "{}", "[{},[],[1]]"])(
    "handles scalar and empty JSON: %s",
    (source) => {
      const output = jsonDocument(source)!
        .formatted.map((token) => token.text)
        .join("");
      expect(JSON.parse(output)).toEqual(JSON.parse(source));
    },
  );
  it("falls back for invalid, large, deep and token-heavy documents without truncating", () => {
    for (const source of [
      "",
      "# Skill\nPlain Markdown",
      '{"x":}',
      "{} trailing",
      '"' + "a".repeat(128 * 1024) + '"',
      "[".repeat(41) + "0" + "]".repeat(41),
      JSON.stringify(Array(3001).fill(0)),
      "[".repeat(39) +
        JSON.stringify(Array(2900).fill("a".repeat(30))) +
        "]".repeat(39),
    ]) {
      expect(jsonDocument(source)).toBeUndefined();
    }
    const html = renderToStaticMarkup(
      <CodeBlock text={"# Skill\nKeep this text"} />,
    );
    expect(html).toContain("# Skill\nKeep this text");
    expect(html).not.toContain("Format JSON");
  });
  it("keeps empty arrays compact inside nested containers", () => {
    const document = jsonDocument('[[],[[]],{"x":[]}]')!;
    expect(document.formatted.map((token) => token.text).join("")).toBe(
      '[\n  [],\n  [\n    []\n  ],\n  {\n    "x": []\n  }\n]',
    );
  });
  it("accepts the supported depth boundary", () => {
    expect(jsonDocument("[".repeat(40) + "0" + "]".repeat(40))).toBeDefined();
  });
  it("accepts the exact source and token limits and rejects the next unit", () => {
    const maximumSource = '"' + "a".repeat(128 * 1024 - 2) + '"';
    expect(jsonDocument(maximumSource)).toBeDefined();
    expect(jsonDocument(maximumSource + " ")).toBeUndefined();
    const maximumTokens = " " + JSON.stringify(Array(2999).fill(0));
    expect(jsonDocument(maximumTokens)?.original).toHaveLength(6000);
    // One whitespace run remains one token even at the token ceiling.
    expect(
      jsonDocument(" ".repeat(100) + maximumTokens)?.original,
    ).toHaveLength(6000);
    expect(jsonDocument(maximumTokens + " ")).toBeUndefined();
  });
  it("keeps string punctuation opaque and formatting idempotent", () => {
    for (const source of [
      '{"text":"{},[]:true null -3","quote":"\\"","slash":"\\\\"}',
      '{"unicode":"\\u0061\\uD83D\\uDE00","empty":{"list":[]}}',
      '[1e400,-0,9007199254740993,{"x":1,"x":2}]',
    ]) {
      const document = jsonDocument(source)!;
      const formatted = document.formatted.map((token) => token.text).join("");
      expect(document.original.map((token) => token.text).join("")).toBe(
        source,
      );
      expect(
        document.formatted.filter((token) => token.kind !== "space"),
      ).toEqual(document.original.filter((token) => token.kind !== "space"));
      expect(jsonDocument(formatted)!.formatted).toEqual(document.formatted);
    }
  });
  it("renders oversized untrusted content completely as escaped plain text", () => {
    const source =
      "<script data-hostile>" + "x".repeat(128 * 1024) + "</script>";
    const html = renderToStaticMarkup(<CodeBlock text={source} />);
    expect(html).toContain("&lt;script data-hostile&gt;");
    expect(html).toContain("x".repeat(128 * 1024));
    expect(html).not.toContain("<script");
    expect(html).not.toContain("syntax-");
    expect(html).not.toContain("Format JSON");
    expect(html).toContain('aria-label="Text document"');
  });
  it("escapes hostile markup and renders readable themed tokens, never HTML", () => {
    const html = renderToStaticMarkup(
      <JSONBlock
        value={{ "<script>": '<img src=x onerror="alert(1)">', number: 42 }}
      />,
    );
    expect(html).not.toContain("<script>");
    expect(html).not.toContain("<img");
    expect(html).toContain("&lt;script&gt;");
    expect(html).toContain('class="syntax-key"');
    expect(html).toContain('class="syntax-number"');
    expect(html).toContain('aria-label="JSON document"');
  });
  it("defaults exact evidence reads to original whitespace", () => {
    const html = renderToStaticMarkup(<CodeBlock text={'{"x":1}'} original />);
    expect(html).toContain('aria-pressed="false"');
    expect(html).toContain('data-wrap="true"');
    expect(html).not.toContain("\n");
  });
});
