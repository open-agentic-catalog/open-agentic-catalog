// OpenAPI stays data: references are local JSON pointers, never network fetches
// or rendered HTML. Keep recursive schemas as links rather than expanding them.
export type Document = Record<string, unknown>;
export type Operation = {
  id: string;
  declaredId: string;
  method: string;
  path: string;
  tags: string[];
  summary: string;
  description: string;
  deprecated: boolean;
  parameters: unknown[];
  body: unknown;
  responses: unknown;
  security: unknown;
};

/** Treat non-object contract values as empty objects for defensive read-only rendering. */
export const object = (value: unknown): Document =>
  value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Document)
    : {};
/** Render contract text only when it is a string; never stringify arbitrary values. */
export const text = (value: unknown): string =>
  typeof value === "string" ? value : "";

/** Resolve own properties along a local JSON pointer without fetching external references. */
export function referenceValue(document: Document, reference: string): unknown {
  if (!reference.startsWith("#/")) return undefined;
  let value: unknown = document;
  for (const part of reference.slice(2).split("/")) {
    const key = part.replaceAll("~1", "/").replaceAll("~0", "~");
    if (Array.isArray(value)) {
      if (!/^(0|[1-9][0-9]*)$/.test(key) || !Object.hasOwn(value, key))
        return undefined;
      value = value[Number(key)];
      continue;
    }
    const current = object(value);
    if (!Object.hasOwn(current, key)) return undefined;
    value = current[key];
  }
  return value;
}

/** Follow a finite local reference chain; cycles, missing and external targets stay unavailable. */
export function localValue(document: Document, input: unknown): unknown {
  let value = input;
  const seen = new Set<string>();
  for (let depth = 0; depth < 16; depth++) {
    const fields = object(value);
    if (!Object.hasOwn(fields, "$ref")) return value;
    const ref = text(fields.$ref);
    if (!ref.startsWith("#/") || seen.has(ref)) return undefined;
    seen.add(ref);
    value = referenceValue(document, ref);
  }
  return Object.hasOwn(object(value), "$ref") ? undefined : value;
}

/** Collect sorted, unique local references without recursively resolving their targets. */
export function references(value: unknown): string[] {
  const found = new Set<string>();
  // Iterative traversal avoids recursive schema or nesting stack exhaustion.
  const pending = [value];
  while (pending.length) {
    const item = pending.pop();
    if (Array.isArray(item)) {
      for (const value of item) pending.push(value);
    } else if (item !== null && typeof item === "object") {
      const ref = object(item).$ref;
      if (typeof ref === "string" && ref.startsWith("#/")) found.add(ref);
      for (const value of Object.values(item)) pending.push(value);
    }
  }
  // Exact JSON pointers retain code-unit order, not locale-equivalent names.
  return [...found].sort((a, b) => Number(a > b) - Number(a < b));
}

/** Project operations with inherited security and operation-level parameter overrides. */
export function operations(document: Document): Operation[] {
  return Object.entries(object(document.paths)).flatMap(([path, value]) => {
    const entry = object(value);
    return Object.entries(entry).flatMap(([method, value]) => {
      if (
        ![
          "get",
          "post",
          "put",
          "patch",
          "delete",
          "head",
          "options",
          "trace",
        ].includes(method)
      )
        return [];
      const operation = object(value);
      const parameters = new Map<string, unknown>();
      const inherited: unknown[] = Array.isArray(entry.parameters)
        ? entry.parameters
        : entry.parameters === undefined
          ? []
          : [entry.parameters];
      const own: unknown[] = Array.isArray(operation.parameters)
        ? operation.parameters
        : operation.parameters === undefined
          ? []
          : [operation.parameters];
      for (const parameter of [...inherited, ...own]) {
        const resolved = localValue(document, parameter);
        const fields = object(resolved);
        parameters.set(
          text(fields.in) && text(fields.name)
            ? JSON.stringify([fields.in, fields.name])
            : `unresolved:${parameters.size}`,
          parameter,
        );
      }
      return [
        {
          id: text(operation.operationId) || `${method}:${path}`,
          declaredId: text(operation.operationId),
          method: method.toUpperCase(),
          path,
          tags: Array.isArray(operation.tags)
            ? operation.tags.map(text).filter(Boolean)
            : [],
          summary: text(operation.summary),
          description: text(operation.description),
          deprecated: operation.deprecated === true,
          parameters: [...parameters.values()],
          body: operation.requestBody,
          responses: operation.responses,
          security: Object.hasOwn(operation, "security")
            ? operation.security
            : Object.hasOwn(document, "security")
              ? document.security
              : [],
        },
      ];
    });
  });
}

/** Stable section identities keep exact tag names separate from the untagged fallback. */
export function areaAnchor(tag: string | undefined): string {
  // JSON escapes lone surrogates before URI encoding, without merging their spelling.
  return tag === undefined
    ? "api-area-untagged"
    : `api-area-${encodeURIComponent(JSON.stringify(tag))}`;
}

export type OperationArea = {
  id: string;
  tag: string | undefined;
  name: string;
  description: string;
  operations: Operation[];
};

/** Use declaration order, then exact undeclared names; each operation has one last-tag home. */
export function operationAreas(
  document: Document,
  items: Operation[],
): OperationArea[] {
  const declared = new Map<string, string>();
  for (const value of Array.isArray(document.tags) ? document.tags : []) {
    const fields = object(value);
    const name = text(fields.name);
    if (name && !declared.has(name))
      declared.set(name, text(fields.description));
  }
  const extra = [...new Set(items.flatMap((item) => item.tags))]
    .filter((name) => !declared.has(name))
    .sort();
  const areas: OperationArea[] = [...declared.keys(), ...extra].map((tag) => ({
    id: areaAnchor(tag),
    tag,
    name: tag,
    description: declared.get(tag) ?? "",
    operations: [],
  }));
  const byName = new Map(areas.map((area) => [area.tag, area]));
  const untagged: OperationArea = {
    id: areaAnchor(undefined),
    tag: undefined,
    name: "Untagged operations",
    description: "These operations have no API area tag in this contract.",
    operations: [],
  };
  for (const item of items) {
    const tag = item.tags.at(-1);
    const home = tag === undefined ? untagged : byName.get(tag)!;
    home.operations.push(item);
  }
  return untagged.operations.length ? [...areas, untagged] : areas;
}

/** Intersect exact tag/method filters with local case-insensitive operation and tag text. */
export function matches(
  operation: Operation,
  query: string,
  tag: string,
  method = "",
): boolean {
  return (
    (!tag || operation.tags.includes(tag)) &&
    (!method || operation.method === method) &&
    `${operation.method} ${operation.path} ${operation.id} ${operation.summary} ${operation.description} ${operation.tags.join(" ")}`
      .toLowerCase()
      .includes(query.trim().toLowerCase())
  );
}
