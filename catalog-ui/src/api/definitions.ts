// The connected components object is the only definition inventory.
import {
  localValue,
  object,
  referenceValue,
  text,
  type Document,
} from "./reference";

export const definitionPageSize = 25;
export type Definition = {
  kind: string;
  name: string;
  reference: string;
  description: string;
};

/** Preserve exact names and all component kinds, including unsupported collection shapes. */
export function definitionIndex(document: Document): Definition[] {
  const escape = (value: string) =>
    value.replaceAll("~", "~0").replaceAll("/", "~1");
  return Object.entries(object(document.components))
    .flatMap(([kind, collection]) => {
      const prefix = `#/components/${escape(kind)}`;
      if (
        collection === null ||
        typeof collection !== "object" ||
        Array.isArray(collection)
      )
        return [
          {
            kind,
            name: "(complete collection)",
            reference: prefix,
            description:
              "Unsupported component collection; inspect complete data.",
          },
        ];
      return Object.entries(collection).map(([name, raw]) => ({
        kind,
        name,
        reference: `${prefix}/${escape(name)}`,
        description:
          text(object(raw).description) ||
          text(object(localValue(document, raw)).description),
      }));
    })
    .sort((a, b) =>
      a.kind === b.kind
        ? Number(a.name > b.name) - Number(a.name < b.name)
        : Number(a.kind > b.kind) - Number(a.kind < b.kind),
    );
}

/** Search the complete index before applying any local page limit. */
export function matchingDefinitions(
  index: Definition[],
  query: string,
  kind: string,
): Definition[] {
  const needle = query.trim().toLowerCase();
  return index.filter(
    (entry) =>
      (!kind || entry.kind === kind) &&
      `${entry.name} ${entry.description}`.toLowerCase().includes(needle),
  );
}

function encoded(value: string) {
  try {
    return encodeURIComponent(value);
  } catch {
    return `json-${encodeURIComponent(JSON.stringify(value))}`;
  }
}

/** Existing ordinary anchors stay unchanged; malformed Unicode cannot crash browsing. */
export function operationAnchor(operation: { method: string; path: string }) {
  return `api-operation-${encoded(operation.method + ":" + operation.path)}`;
}
export function definitionAnchor(reference: string) {
  return `api-definition-${encoded(reference)}`;
}

/** Validate exact local targets, including false schemas and nested pointers, before navigation. */
export function linkedDefinition(
  document: Document,
  anchor: string,
): string | undefined {
  const prefix = "api-definition-";
  if (!anchor.startsWith(prefix)) return undefined;
  try {
    const suffix = anchor.slice(prefix.length);
    const reference: unknown = suffix.startsWith("json-")
      ? JSON.parse(decodeURIComponent(suffix.slice(5)))
      : decodeURIComponent(suffix);
    return typeof reference === "string" &&
      reference.startsWith("#/") &&
      referenceValue(document, reference) !== undefined &&
      definitionAnchor(reference) === anchor
      ? reference
      : undefined;
  } catch {
    return undefined;
  }
}
