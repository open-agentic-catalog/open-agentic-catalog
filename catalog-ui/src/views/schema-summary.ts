// A bounded presentation tree, never an effective validator or remote resolver.
import { object, text, type Document } from "../api/reference";
import { record, schemaFacts } from "./contract-summary";

const schemaBudget = { depth: 3, nodes: 80, rows: 20 };
export type SchemaNode = {
  label: string;
  description: string;
  facts: [string, string][];
  reference: string;
  unavailable: boolean;
  omitted: boolean;
  children: SchemaNode[];
};

/** Do not expand references or merge composition; omitted structure stays in complete data. */
export function schemaSummary(document: Document, value: unknown): SchemaNode {
  let remaining = schemaBudget.nodes;
  const visit = (raw: unknown, label: string, depth: number): SchemaNode => {
    remaining--;
    const fields = object(raw);
    const summary = schemaFacts(document, raw);
    const node: SchemaNode = {
      label,
      description: text(fields.description),
      facts: [...summary.facts],
      reference: text(fields.$ref),
      unavailable: summary.unavailable,
      omitted: false,
      children: [],
    };
    if (!record(raw)) return node;
    if (
      fields.description !== undefined &&
      typeof fields.description !== "string"
    )
      node.unavailable = true;
    for (const key of ["readOnly", "writeOnly"]) {
      if (fields[key] === undefined) continue;
      if (typeof fields[key] === "boolean")
        node.facts.push([key + " annotation", String(fields[key])]);
      else node.unavailable = true;
    }
    let required: string[] | undefined;
    if (fields.required !== undefined) {
      if (
        Array.isArray(fields.required) &&
        fields.required.every((item: unknown) => typeof item === "string")
      ) {
        required = fields.required as string[];
        node.facts.push([
          "Required properties",
          required.length
            ? JSON.stringify(required.slice(0, schemaBudget.rows)) +
              (required.length > schemaBudget.rows
                ? " (additional required names remain in complete data)"
                : "")
            : "[] (declared empty)",
        ]);
      } else node.unavailable = true;
    }
    const add = (child: unknown, name: string) => {
      if (
        depth >= schemaBudget.depth ||
        remaining <= 0 ||
        node.children.length >= schemaBudget.rows
      ) {
        node.omitted = true;
        return;
      }
      node.children.push(visit(child, name, depth + 1));
    };
    if (fields.properties !== undefined) {
      if (record(fields.properties)) {
        for (const [name, child] of Object.entries(fields.properties))
          add(
            child,
            `Property: ${name}${required ? (required.includes(name) ? " (required)" : " (not listed as required)") : ""}`,
          );
      } else node.unavailable = true;
    }
    for (const key of ["items", "additionalProperties"]) {
      if (fields[key] !== undefined)
        add(
          fields[key],
          key === "items" ? "Array items" : "Additional properties",
        );
    }
    for (const key of ["prefixItems", "allOf", "oneOf", "anyOf"]) {
      if (fields[key] === undefined) continue;
      const values = fields[key];
      if (!Array.isArray(values)) {
        node.unavailable = true;
        continue;
      }
      const meaning: Record<string, string> = {
        prefixItems: "positional item",
        allOf: "all subschemas",
        oneOf: "exactly one subschema",
        anyOf: "one or more subschemas",
      };
      if (!values.length) node.facts.push([key, "[] (declared empty)"]);
      for (let index = 0; index < values.length; index++)
        add(values[index], `${key}: ${meaning[key]} (${index + 1})`);
    }
    return node;
  };
  return visit(value, "Schema", 0);
}
