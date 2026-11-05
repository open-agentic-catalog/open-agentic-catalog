// Browser object commands must preserve every submitted decimal value.
import { DocumentInputError } from "./input-validation";

function decimalIdentity(token: string): string {
  const [base = "", exponent = "0"] = token.toLowerCase().split("e");
  const negative = base.startsWith("-");
  const [integer = "", fraction = ""] = base.replace(/^-/, "").split(".");
  let digits = (integer + fraction).replace(/^0+/, "");
  if (!digits) return "0";
  let power = Number(exponent) - fraction.length;
  while (digits.endsWith("0")) {
    digits = digits.slice(0, -1);
    power++;
  }
  return `${negative ? "-" : ""}${digits}:${power}`;
}

/** Reject oversized, deeply nested, duplicate-key or numerically lossy browser JSON input. */
function parseJSON(
  text: string,
  maximumLength: number,
  controlFile: boolean,
  nativeProjection = false,
): unknown {
  if (text.length > maximumLength) throw new DocumentInputError("limit");
  if (controlFile || nativeProjection) {
    // Refuse excessive structure before JSON.parse allocates objects. Quoted
    // native text is opaque and does not consume this structural work budget.
    let quoted = false;
    let depth = 0;
    let structure = 0;
    for (let index = 0; index < text.length; index++) {
      const character = text[index];
      if (quoted) {
        if (character === "\\") index++;
        else if (character === '"') quoted = false;
      } else if (character === '"') quoted = true;
      else if (character === "{" || character === "[") {
        if (++depth > 64 || ++structure > 262144)
          throw new DocumentInputError("depth");
      } else if (character === "}" || character === "]") {
        depth--;
        if (++structure > 262144) throw new DocumentInputError("depth");
      } else if (character === ":" || character === ",") {
        if (++structure > 262144) throw new DocumentInputError("depth");
      }
    }
  }
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    throw new DocumentInputError("syntax");
  }
  // Walk validated syntax without a backtracking string regex or a second
  // array of all tokens. A draft file can contain large opaque text values.
  let at = 0;
  const whitespace = () => {
    while (text.charCodeAt(at) <= 32) at++;
  };
  const stringToken = () => {
    const start = at++;
    while (text[at] !== '"') {
      if (text[at] === "\\") at++;
      at++;
    }
    at++;
    const literal = text.slice(start, at);
    if (
      nativeProjection &&
      /[\uD800-\uDFFF]/u.test(JSON.parse(literal) as string)
    )
      throw new DocumentInputError("encoding");
    return literal;
  };
  const numeric = /-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/y;
  const visit = (depth: number) => {
    if (depth > 64) throw new DocumentInputError("depth");
    whitespace();
    const token = text[at];
    if (token === "{") {
      at++;
      whitespace();
      const keys = new Set<string>();
      while (text[at] !== "}") {
        const key = JSON.parse(stringToken()) as string;
        if (keys.has(key)) throw new DocumentInputError("duplicate");
        keys.add(key);
        whitespace();
        at++;
        visit(depth + 1);
        whitespace();
        if (text[at] !== ",") break;
        at++;
        whitespace();
      }
      at++;
    } else if (token === "[") {
      at++;
      whitespace();
      while (text[at] !== "]") {
        visit(depth + 1);
        whitespace();
        if (text[at] !== ",") break;
        at++;
      }
      at++;
    } else if (token === '"') {
      stringToken();
    } else if (token === "-" || (token! >= "0" && token! <= "9")) {
      numeric.lastIndex = at;
      const literal = numeric.exec(text)![0];
      at = numeric.lastIndex;
      if (controlFile && /[.eE]/.test(literal))
        throw new DocumentInputError("number");
      if (nativeProjection) return;
      const number = Number(literal);
      if (
        !Number.isFinite(number) ||
        (Number.isInteger(number) && !Number.isSafeInteger(number))
      )
        throw new DocumentInputError("number");
      if (decimalIdentity(literal) !== decimalIdentity(JSON.stringify(number)))
        throw new DocumentInputError("precision");
    } else {
      at += token === "f" ? 5 : 4;
    }
  };
  visit(0);
  return value;
}

/** Require an object after the shared bounded, duplicate-key and numeric-fidelity checks. */
export function parseJSONObject(
  text: string,
  maximumLength = 2 * 1024 * 1024,
  controlFile = false,
): Record<string, unknown> {
  const value = parseJSON(text, maximumLength, controlFile);
  if (!value || typeof value !== "object" || Array.isArray(value))
    throw new DocumentInputError("object");
  return value as Record<string, unknown>;
}

/** Read native metadata for string-only projection, never numeric reserialization. */
export function parseNativeJSONObject(text: string, maximumLength: number) {
  const value = parseJSON(text, maximumLength, false, true);
  if (!value || typeof value !== "object" || Array.isArray(value))
    throw new DocumentInputError("object");
  return value as Record<string, unknown>;
}
