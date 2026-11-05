// Small unknown-value checks shared by consumed API projections.
export type Check = (value: unknown) => boolean;

/** Missing optional fields are allowed; present values keep their exact check. */
export function optional(value: unknown, check: Check): boolean {
  return value === undefined || check(value);
}

export function object(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
export function text(value: unknown): value is string {
  return typeof value === "string";
}
export function flag(value: unknown): value is boolean {
  return typeof value === "boolean";
}
export function number(
  value: unknown,
  min = 0,
  max = Infinity,
  integer = true,
): boolean {
  return (
    typeof value === "number" &&
    Number.isFinite(value) &&
    (!integer || Number.isSafeInteger(value)) &&
    value >= min &&
    value <= max
  );
}
export function array(value: unknown, item: Check): boolean {
  return Array.isArray(value) && value.every(item);
}
export function choice(value: unknown, allowed: readonly unknown[]): boolean {
  return allowed.includes(value);
}
/** Continuations are opaque. Check the contract bound without decoding or normalizing. */
export function cursor(value: unknown, maximum = 2048): boolean {
  return value === undefined || (text(value) && [...value].length <= maximum);
}
/** A malformed page must not turn into an empty successful result. */
export function page(item: Check): Check {
  return (value) =>
    object(value) && array(value.items, item) && cursor(value.nextCursor);
}

/** Identifiers placed in local links must remain one nonempty literal path segment. */
export function identifier(value: unknown): boolean {
  return (
    text(value) &&
    /^[a-z0-9_:.-]+$/i.test(value) &&
    value !== "." &&
    value !== ".."
  );
}
/** Dates drive expiry decisions and display; invalid Date must never grant usable state. */
export function timestamp(value: unknown): boolean {
  return (
    text(value) &&
    /^\d{4}-\d{2}-\d{2}T/.test(value) &&
    Number.isFinite(Date.parse(value))
  );
}
