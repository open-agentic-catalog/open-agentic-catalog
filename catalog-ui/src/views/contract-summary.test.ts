import { describe, expect, it } from "vitest";
import {
  authentication,
  componentRef,
  decodedText,
  record,
  schemaFacts,
} from "./contract-summary";

const document = {
  components: {
    securitySchemes: {
      bearer: {
        type: "http",
        scheme: "bearer",
        description: "An access token",
      },
      basic: { type: "http", scheme: "Basic" },
      key: { type: "apiKey", in: "header", name: "X-Key" },
      oauth: { type: "oauth2", flows: {} },
      oidc: {
        type: "openIdConnect",
        openIdConnectUrl: "https://unfetched.invalid",
      },
      tls: { type: "mutualTLS" },
      alias: { $ref: "#/components/securitySchemes/bearer" },
    },
  },
};

describe("Readable operation facts", () => {
  it("separates no credentials, anonymous alternatives, conjunctions and required schemes", () => {
    expect(authentication(document, []).kind).toBe("none");
    expect(authentication(document, [{}]).kind).toBe("none");
    const optional = authentication(document, [{}, { bearer: [] }]);
    expect(optional.kind).toBe("optional");
    expect(optional.alternatives[1]).toEqual([
      {
        name: "bearer",
        reference: "#/components/securitySchemes/bearer",
        label: "HTTP Bearer token",
        description: "An access token",
        values: [],
        oauth: false,
      },
    ]);
    const combined = authentication(document, [
      { bearer: ["sources.manage"], key: [] },
      { oauth: ["read"] },
    ]);
    expect(combined.kind).toBe("required");
    expect(
      combined.alternatives.map((alt) =>
        alt.map((scheme) => [scheme.name, scheme.values, scheme.oauth]),
      ),
    ).toEqual([
      [
        ["bearer", ["sources.manage"], false],
        ["key", [], false],
      ],
      [["oauth", ["read"], true]],
    ]);
    expect(
      authentication(document, [
        { basic: [], key: [], oidc: [], tls: [], alias: [] },
      ]).alternatives[0]?.map((scheme) => scheme.label),
    ).toEqual([
      "HTTP Basic",
      "API key in header: X-Key",
      "OpenID Connect",
      "Mutual TLS client certificate",
      "HTTP Bearer token",
    ]);
  });

  it("never claims anonymous access from invalid requirements or unresolved scheme metadata", () => {
    for (const value of [
      null,
      {},
      "[]",
      [null],
      [42],
      [[]],
      [{ missing: [] }],
      [{ bearer: null }],
      [{ bearer: [42] }],
      [{ bearer: [""] }],
      [{ "": [] }],
      [{}, { missing: [] }],
    ]) {
      expect(authentication(document, value)).toEqual({
        kind: "unavailable",
        alternatives: [],
      });
    }
    for (const scheme of [
      null,
      {},
      { type: "unknown" },
      { type: "http", scheme: 42 },
      { type: "apiKey", in: "path", name: "key" },
      { type: "apiKey", in: "header" },
      { type: "http", scheme: "bearer", description: 42 },
    ]) {
      expect(
        authentication({ components: { securitySchemes: { bad: scheme } } }, [
          { bad: [] },
        ]).kind,
      ).toBe("unavailable");
    }
  });

  it("keeps escaped component names exact and copies supplied strings without interpretation", () => {
    expect(componentRef("extra/~", "name/~")).toBe(
      "#/components/extra~1~0/name~1~0",
    );
    expect(componentRef("schemas", "A/~B")).toBe("#/components/schemas/A~1~0B");
    expect(decodedText("  <script>inert()</script>\r\n")).toBe(
      "  <script>inert()</script>\r\n",
    );
    expect(decodedText(false)).toBe("false");
    expect(decodedText(0)).toBe("0");
    expect(decodedText(null)).toBe("null");
    expect(decodedText({ x: 1 })).toBe('{"x":1}');
    expect(decodedText(undefined)).toBeUndefined();
    expect([null, [], 0, "x", undefined].map(record)).toEqual([
      false,
      false,
      false,
      false,
      false,
    ]);
    expect(record({})).toBe(true);
  });

  it("reports declared scalar facts without inventing defaults, flattening composition or losing zero/false/null", () => {
    const raw = {
      type: ["integer", "null"],
      format: "int32",
      minimum: -2,
      maximum: 8,
      exclusiveMinimum: -3,
      exclusiveMaximum: 9,
      multipleOf: 2,
      minLength: 0,
      maxLength: 8,
      minItems: 0,
      maxItems: 3,
      minProperties: 0,
      maxProperties: 4,
      uniqueItems: false,
      pattern: "^a",
      enum: [false, 0, null],
      const: false,
      default: 0,
      items: { type: "string" },
    };
    expect(schemaFacts({}, raw)).toEqual({
      unavailable: false,
      referenced: false,
      facts: [
        ["Type", "integer or null"],
        ["Format", "int32"],
        ["Pattern", "^a"],
        ["minimum", "-2"],
        ["maximum", "8"],
        ["exclusiveMinimum", "-3"],
        ["exclusiveMaximum", "9"],
        ["multipleOf", "2"],
        ["minLength", "0"],
        ["maxLength", "8"],
        ["minItems", "0"],
        ["maxItems", "3"],
        ["minProperties", "0"],
        ["maxProperties", "4"],
        ["uniqueItems", "false"],
        ["enum", "[false,0,null]"],
        ["const", "false"],
        ["default", "0"],
        ["Array items", "string"],
      ],
    });
    expect(schemaFacts({}, { allOf: [{ type: "string" }] }).facts).toEqual([]);
    expect(schemaFacts({}, true).facts).toEqual([
      ["Boolean schema", "true: accepts any value"],
    ]);
    expect(schemaFacts({}, false).facts).toEqual([
      ["Boolean schema", "false: rejects every value"],
    ]);
    expect(
      schemaFacts({ item: { type: "string" } }, { $ref: "#/item" }),
    ).toMatchObject({ referenced: true, facts: [["Type", "string"]] });
    expect(schemaFacts({}, { items: { $ref: "#/recur" } }).facts).toEqual([
      ["Array items", "#/recur"],
    ]);
    expect(schemaFacts({}, { items: {} }).facts).toEqual([
      ["Array items", "See complete schema"],
    ]);
    expect(schemaFacts({}, { default: "a".repeat(401) }).facts).toEqual([
      [
        "default",
        "Value exceeds the summary limit; inspect complete decoded data.",
      ],
    ]);
  });

  it("marks malformed consumed schema fields unavailable while keeping supported facts", () => {
    for (const schema of [undefined, null, [], 42, { $ref: "#/missing" }])
      expect(schemaFacts({}, schema).unavailable).toBe(true);
    for (const schema of [
      { type: [] },
      { type: ["string", "unknown"] },
      { type: 42 },
      { format: [] },
      { pattern: true },
      { minimum: "0" },
      { maximum: Infinity },
      { multipleOf: 0 },
      { minLength: -1 },
      { maxItems: 1.5 },
      { uniqueItems: "false" },
      { enum: null },
      { default: undefined },
    ])
      expect(schemaFacts({}, schema).unavailable).toBe(true);
    expect(schemaFacts({}, { type: "boolean", format: null })).toMatchObject({
      unavailable: true,
      facts: [["Type", "boolean"]],
    });
  });
});
