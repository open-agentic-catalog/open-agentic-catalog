import { safeLink } from "../api/client";

const goWhitespace = /\p{White_Space}/u;

/** Trim Go's Unicode whitespace in linear time, preserving BOM and opaque interior text. */
export function trimGoWhitespace(value: string): string {
  let start = 0;
  let end = value.length;
  while (start < end && goWhitespace.test(value[start]!)) start++;
  while (end > start && goWhitespace.test(value[end - 1]!)) end--;
  return value.slice(start, end);
}

const documentErrors = {
  size: "Choose a non-empty document up to 2 MiB.",
  encoding: "Choose a document encoded as UTF-8.",
  text: "Choose non-empty text without NUL characters.",
  limit: "JSON input exceeds the browser editor limit.",
  syntax: "Enter valid JSON.",
  depth: "JSON nesting exceeds 64 levels.",
  duplicate: "Duplicate JSON object keys are not allowed.",
  number:
    "JSON numbers must be finite. Whole numbers must be between -9007199254740991 and 9007199254740991.",
  precision:
    "The browser cannot preserve every number in this JSON. Use values it can represent exactly.",
  object: "Enter a JSON object.",
};

/** Only reviewed local validation categories may be displayed after a file read. */
export class DocumentInputError extends Error {
  constructor(category: keyof typeof documentErrors) {
    super(documentErrors[category]);
  }
}
// Only local validators can attribute an error to a known editor control.
export class InputError extends Error {
  readonly fieldId: string;
  readonly fallbackId?: string;
  constructor(message: string, fieldId: string, fallbackId?: string) {
    super(message);
    this.fieldId = fieldId;
    if (fallbackId) this.fallbackId = fallbackId;
  }
}

/** Attach an editor control to a synchronous local validation failure. */
export function atField<T>(fieldId: string, validate: () => T): T {
  try {
    return validate();
  } catch (error) {
    if (error instanceof InputError) throw error;
    throw new InputError(
      error instanceof Error ? error.message : "Check the input.",
      fieldId,
    );
  }
}

/** JSON Schema lengths count Unicode code points; request byte budgets are separate. */
export function characterText(
  value: string,
  label: string,
  maximum: number,
  minimum = 0,
): string {
  const length = [...value].length;
  if (value.includes("\0") || length < minimum || length > maximum)
    throw new Error(
      `${label} needs ${minimum} to ${maximum} characters without NUL.`,
    );
  return value;
}

/** Search transport budgets measure UTF-8 bytes, including supplementary characters. */
export function byteText(
  value: string,
  label: string,
  maximum: number,
): string {
  if (value.includes("\0") || new TextEncoder().encode(value).length > maximum)
    throw new Error(`${label} must fit ${maximum} UTF-8 bytes without NUL.`);
  return value;
}

/** Tags and categories have independent collection and Unicode character limits. */
export function metadataTerms(
  values: string[],
  label: string,
  maximum: number,
  minimum = 0,
): string[] {
  if (values.length > maximum)
    throw new Error(`Use up to ${maximum} ${label}.`);
  for (const value of values) characterText(value.trim(), label, 80, minimum);
  return values;
}

/** Listing references must not carry embedded credentials or signed URL parameters. */
export function listingLink(value: string): string {
  byteText(value, "Listing link", 2048);
  const safe = safeLink(value);
  if (!safe || /[\r\n]/u.test(value))
    throw new Error("Use a credential-free HTTPS link.");
  for (const name of new URL(safe).searchParams.keys()) {
    if (
      [
        "accesstoken",
        "apikey",
        "authorization",
        "credential",
        "password",
        "secret",
        "sig",
        "signature",
        "token",
        "xamzcredential",
        "xamzsecuritytoken",
        "xamzsignature",
      ].includes(name.toLowerCase().replaceAll(/[-_]/gu, ""))
    )
      throw new Error(
        "Listing links must not contain credentials or signatures.",
      );
  }
  return value;
}
