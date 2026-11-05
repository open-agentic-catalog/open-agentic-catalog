import { ClientError } from "./config.js";
// JSON.parse's Node 24 source context preserves numeric tokens for explicit
// metadata exports. Values used for UI decisions still have ordinary types.
const numbers = new WeakMap<object, Map<string, string>>();
export function decodeJSON(source: string): unknown {
  try {
    return JSON.parse(
      source,
      function (
        this: object,
        key: string,
        value: unknown,
        context?: { source?: string },
      ) {
        if (typeof value === "number" && context?.source) {
          let fields = numbers.get(this);
          if (!fields) {
            fields = new Map();
            numbers.set(this, fields);
          }
          fields.set(key, context.source);
        }
        return value;
      },
    );
  } catch {
    throw new ClientError("invalid_response");
  }
}
export function encodeProjection(value: unknown): string {
  const raw = Reflect.get(JSON, "rawJSON") as (source: string) => unknown;
  return JSON.stringify(
    value,
    function (this: object, key: string, value: unknown) {
      const source = numbers.get(this)?.get(key);
      return source !== undefined &&
        typeof value === "number" &&
        Object.is(value, Number(source))
        ? raw(source)
        : value;
    },
    2,
  );
}

// JSON.parse accepts duplicate keys. Native execution decisions must not.
export function uniqueJSON(
  source: string,
  code = "invalid_mcp_document",
): unknown {
  const value = decodeJSON(source);
  const tokens =
    source.match(/"(?:\\.|[^"\\])*"|[{}[\]:,]|[^\s{}[\]:,]+/gu) ?? [];
  let cursor = 0;
  function visit(depth: number): void {
    if (depth > 64) throw new ClientError(code);
    const token = tokens[cursor++];
    if (token === "{") {
      const names = new Set<string>();
      while (tokens[cursor] !== "}") {
        const key = JSON.parse(tokens[cursor++]!) as string;
        if (names.has(key)) throw new ClientError(code);
        names.add(key);
        cursor++;
        visit(depth + 1);
        if (tokens[cursor] !== ",") break;
        cursor++;
      }
      cursor++;
    } else if (token === "[") {
      while (tokens[cursor] !== "]") {
        visit(depth + 1);
        if (tokens[cursor] !== ",") break;
        cursor++;
      }
      cursor++;
    }
  }
  visit(0);
  return value;
}
