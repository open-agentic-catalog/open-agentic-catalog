// Saved criteria retain exact values; validating or editing them never executes discovery.
import { isCatalogOrder } from "./catalog-order";
import type { components } from "../api/schema";
import { object } from "../api/values";
import { InputError } from "./input-validation";

export type PersonalCriteria = components["schemas"]["PersonalCriteria"];
const fields = [
  "order",
  "text",
  "origins",
  "namespaceId",
  "kinds",
  "tags",
  "categories",
  "license",
  "publisher",
  "visibility",
];
const origins = ["local", "indexed", "live"];
const kinds = ["agent", "mcp_server", "skill", "plugin"];

/** Shape checks permit unfinished field values, but never unsupported fields or unbounded rows. */
export function criteriaDraft(value: unknown): PersonalCriteria {
  if (!object(value) || Object.keys(value).some((key) => !fields.includes(key)))
    throw new Error(
      "Enter valid supported criteria. Only saved search fields are accepted.",
    );
  for (const key of [
    "text",
    "namespaceId",
    "license",
    "publisher",
    "visibility",
  ])
    if (value[key] !== undefined && typeof value[key] !== "string")
      throw new Error(`${key} must be text, not null or another value.`);
  for (const [key, maximum, choices] of [
    ["origins", 3, origins],
    ["kinds", 4, kinds],
    ["tags", 32, undefined],
    ["categories", 16, undefined],
  ] as const) {
    const items = value[key];
    if (
      items !== undefined &&
      (!Array.isArray(items) ||
        items.length > maximum ||
        items.some(
          (item) =>
            typeof item !== "string" ||
            (choices && !(choices as readonly string[]).includes(item)),
        ))
    )
      throw new Error(`Use up to ${maximum} supported ${key} values.`);
  }
  if (
    value.visibility !== undefined &&
    !["private", "tenant", "public"].includes(value.visibility as string)
  )
    throw new Error("Select a supported visibility.");
  if (value.order !== undefined && !isCatalogOrder(value.order))
    throw new Error("Select a supported sort order.");
  return value as PersonalCriteria;
}

/** The field-linked browser check mirrors the supported contract, including UTF-8 byte limits. */
export function checkedCriteria(value: unknown, prefix = ""): PersonalCriteria {
  let criteria: PersonalCriteria;
  try {
    criteria = criteriaDraft(value);
  } catch (cause) {
    throw new InputError(
      cause instanceof Error ? cause.message : "Check the saved criteria.",
      prefix,
    );
  }
  const fail = (field: string, message: string): never => {
    throw new InputError(message, `${prefix}-${field}`, prefix);
  };
  const text = (
    field: "text" | "publisher" | "license",
    maximum: number,
    bytes = false,
  ) => {
    const v = criteria[field];
    if (
      v !== undefined &&
      (v.includes("\0") ||
        /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(
          v,
        ) ||
        (bytes ? new TextEncoder().encode(v).length : [...v].length) > maximum)
    )
      fail(
        field,
        `${field} must fit ${maximum} ${bytes ? "UTF-8 bytes" : "characters"} without NUL or malformed Unicode.`,
      );
  };
  text("text", 500, true);
  text("publisher", 256, true);
  text("license", 256);
  if (criteria.license && !criteria.license.trim())
    fail("license", "Enter a nonblank license declaration or leave it empty.");
  if (
    criteria.namespaceId !== undefined &&
    !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
      criteria.namespaceId,
    )
  )
    fail("namespaceId", "Enter a Namespace UUID or remove the field.");
  if (
    criteria.origins !== undefined &&
    (!criteria.origins.length ||
      new Set(criteria.origins).size !== criteria.origins.length)
  )
    fail(
      "origins",
      "Select one to three different origins or use unspecified origins.",
    );
  if (
    criteria.namespaceId &&
    criteria.origins &&
    (criteria.origins.length !== 1 || criteria.origins[0] !== "local")
  )
    fail(
      "origins",
      "A Namespace requires unspecified origins or Local Catalog alone.",
    );
  if (criteria.kinds && new Set(criteria.kinds).size !== criteria.kinds.length)
    fail("kinds", "Select each resource kind once.");
  for (const field of ["tags", "categories"] as const)
    for (const v of criteria[field] ?? [])
      if (
        v.includes("\0") ||
        [...v].length > 80 ||
        (field === "categories" && !v.trim()) ||
        /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(
          v,
        )
      )
        fail(
          field,
          `${field} need values up to 80 characters without NUL or malformed Unicode; categories cannot be blank.`,
        );
  return criteria;
}

/** Consumed saved metadata must satisfy the bounds before it drives controls or summaries. */
export function validCriteria(value: unknown): boolean {
  try {
    checkedCriteria(value);
    return true;
  } catch {
    return false;
  }
}
