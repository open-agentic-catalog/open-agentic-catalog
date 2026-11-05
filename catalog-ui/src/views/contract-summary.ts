// Read-only presentation facts; references never confer network or execution authority.
import { localValue, object, text, type Document } from "../api/reference";

export const summaryLimit = 32;
export const exampleCopyLimit = 65536;

/** Consume object metadata without accepting null, arrays or scalar declarations. */
export function record(value: unknown): value is Document {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

/** Component names keep their exact JSON-pointer spelling. */
export function componentRef(kind: string, name: string): string {
  return `#/components/${kind.replaceAll("~", "~0").replaceAll("/", "~1")}/${name.replaceAll("~", "~0").replaceAll("/", "~1")}`;
}

/** Strings are copied verbatim; other decoded JSON examples use JSON encoding, never YAML source claims. */
export function decodedText(value: unknown): string | undefined {
  return typeof value === "string" ? value : JSON.stringify(value);
}

export type AuthScheme = {
  name: string;
  reference: string;
  label: string;
  description: string;
  values: string[];
  oauth: boolean;
};
export type Authentication = {
  kind: "none" | "optional" | "required" | "unavailable";
  alternatives: AuthScheme[][];
};

function requirementValues(value: unknown): value is string[] {
  return (
    Array.isArray(value) &&
    value.every((item: unknown) => typeof item === "string" && item.length > 0)
  );
}

/** Requirements are alternatives; every named scheme within one alternative is required together. */
export function authentication(
  document: Document,
  value: unknown,
): Authentication {
  const unavailable: Authentication = { kind: "unavailable", alternatives: [] };
  if (!Array.isArray(value)) return unavailable;
  const alternatives: AuthScheme[][] = [];
  for (const requirement of value) {
    if (!record(requirement)) return unavailable;
    const schemes: AuthScheme[] = [];
    for (const [name, values] of Object.entries(requirement)) {
      if (!name || !requirementValues(values)) return unavailable;
      const reference = componentRef("securitySchemes", name);
      const scheme = object(localValue(document, { $ref: reference }));
      let label: string;
      switch (scheme.type) {
        case "http":
          if (!text(scheme.scheme)) return unavailable;
          label =
            text(scheme.scheme).toLowerCase() === "bearer"
              ? "HTTP Bearer token"
              : `HTTP ${text(scheme.scheme)}`;
          break;
        case "apiKey":
          if (
            !["header", "query", "cookie"].includes(text(scheme.in)) ||
            !text(scheme.name)
          )
            return unavailable;
          label = `API key in ${text(scheme.in)}: ${text(scheme.name)}`;
          break;
        case "oauth2":
          label = "OAuth 2.0";
          break;
        case "openIdConnect":
          label = "OpenID Connect";
          break;
        case "mutualTLS":
          label = "Mutual TLS client certificate";
          break;
        default:
          return unavailable;
      }
      if (
        scheme.description !== undefined &&
        typeof scheme.description !== "string"
      )
        return unavailable;
      schemes.push({
        name,
        reference,
        label,
        description: text(scheme.description),
        values,
        oauth: scheme.type === "oauth2" || scheme.type === "openIdConnect",
      });
    }
    alternatives.push(schemes);
  }
  const anonymous = alternatives.some((schemes) => !schemes.length);
  const authenticated = alternatives.some((schemes) => schemes.length > 0);
  return {
    kind: !authenticated ? "none" : anonymous ? "optional" : "required",
    alternatives,
  };
}

const types = new Set([
  "string",
  "number",
  "integer",
  "boolean",
  "array",
  "object",
  "null",
]);
/** Only declared scalar constraints are summarized; object/composition interpretation remains separate. */
export function schemaFacts(
  document: Document,
  raw: unknown,
): { facts: [string, string][]; unavailable: boolean; referenced: boolean } {
  const referenced = Object.hasOwn(object(raw), "$ref");
  const schema = localValue(document, raw);
  const facts: [string, string][] = [];
  if (typeof schema === "boolean")
    return {
      facts: [
        [
          "Boolean schema",
          schema ? "true: accepts any value" : "false: rejects every value",
        ],
      ],
      unavailable: false,
      referenced,
    };
  if (!record(schema)) return { facts, unavailable: true, referenced };
  let unavailable = false;
  if (schema.type !== undefined) {
    const declared = Array.isArray(schema.type) ? schema.type : [schema.type];
    if (
      declared.length &&
      declared.every((v: unknown) => typeof v === "string" && types.has(v))
    )
      facts.push(["Type", declared.join(" or ")]);
    else unavailable = true;
  }
  for (const key of ["format", "pattern"]) {
    if (schema[key] !== undefined) {
      if (typeof schema[key] === "string")
        facts.push([key === "format" ? "Format" : "Pattern", schema[key]]);
      else unavailable = true;
    }
  }
  for (const key of [
    "minimum",
    "maximum",
    "exclusiveMinimum",
    "exclusiveMaximum",
    "multipleOf",
    "minLength",
    "maxLength",
    "minItems",
    "maxItems",
    "minProperties",
    "maxProperties",
  ]) {
    const value = schema[key];
    if (value === undefined) continue;
    const valid =
      typeof value === "number" &&
      Number.isFinite(value) &&
      (key === "multipleOf"
        ? value > 0
        : key.includes("Length") ||
            key.includes("Items") ||
            key.includes("Properties")
          ? Number.isSafeInteger(value) && value >= 0
          : true);
    if (valid) facts.push([key, String(value)]);
    else unavailable = true;
  }
  if (schema.uniqueItems !== undefined) {
    if (typeof schema.uniqueItems === "boolean")
      facts.push(["uniqueItems", String(schema.uniqueItems)]);
    else unavailable = true;
  }
  for (const key of ["enum", "const", "default"]) {
    if (!Object.hasOwn(schema, key)) continue;
    if (key === "enum" && !Array.isArray(schema[key])) {
      unavailable = true;
      continue;
    }
    const value = JSON.stringify(schema[key]);
    if (value === undefined) {
      unavailable = true;
      continue;
    }
    facts.push([
      key,
      value.length <= 400
        ? value
        : "Value exceeds the summary limit; inspect complete decoded data.",
    ]);
  }
  if (record(schema.items))
    facts.push([
      "Array items",
      text(schema.items.$ref) ||
        text(schema.items.type) ||
        "See complete schema",
    ]);
  return { facts, unavailable, referenced };
}
