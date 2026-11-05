// Assert JSON values actually consumed by real-boundary tests. Keep documents unknown.
export function object(value: unknown): Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value))
    throw new Error("Expected a JSON object");
  return value as Record<string, unknown>;
}
export function array(value: unknown): unknown[] {
  if (!Array.isArray(value)) throw new Error("Expected a JSON array");
  return value;
}
export function text(value: unknown): string {
  if (typeof value !== "string") throw new Error("Expected JSON text");
  return value;
}
