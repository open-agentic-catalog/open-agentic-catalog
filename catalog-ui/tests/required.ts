/** Fail a fixture at the missing value, before a later assertion can hide it. */
export function required<T>(value: T | undefined): T {
  if (value === undefined) throw new Error("Required test value is missing");
  return value;
}
