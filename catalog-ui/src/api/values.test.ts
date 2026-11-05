import { expect, it } from "vitest";
import {
  array,
  choice,
  cursor,
  flag,
  identifier,
  number,
  object,
  optional,
  page,
  text,
  timestamp,
} from "./values";

it("allows missing optional values without evaluating their predicate", () => {
  let calls = 0;
  const check = (value: unknown) => {
    calls++;
    return text(value);
  };
  expect(optional(undefined, check)).toBe(true);
  expect(calls).toBe(0);
  expect(optional("present", check)).toBe(true);
  expect(calls).toBe(1);
  expect(optional(3, check)).toBe(false);
  expect(calls).toBe(2);
  expect(optional(null, check)).toBe(false);
  expect(calls).toBe(3);
  expect(optional(null, (value) => value === null)).toBe(true);
  expect(optional(false, flag)).toBe(true);
  expect(optional(0, number)).toBe(true);
  expect(optional(NaN, number)).toBe(false);
});

it("preserves optional-predicate errors", () => {
  const failure = new Error("predicate failed");
  expect(() =>
    optional("present", () => {
      throw failure;
    }),
  ).toThrow(failure);
});

it("distinguishes JSON control values without coercing them", () => {
  for (const bad of [undefined, null, [], true, 3, "text"])
    expect(object(bad)).toBe(false);
  expect(object({})).toBe(true);
  expect(text(3)).toBe(false);
  expect(text("")).toBe(true);
  expect(flag("false")).toBe(false);
  expect(flag(false)).toBe(true);
  expect(array({}, text)).toBe(false);
  expect(array([3], text)).toBe(false);
  expect(array([], text)).toBe(true);
  expect(choice("reader", ["reader"])).toBe(true);
  expect(choice({}, ["reader"])).toBe(false);
});
it("checks finite counts and numeric bounds without accepting numeric strings", () => {
  for (const bad of [
    undefined,
    "1",
    NaN,
    Infinity,
    -1,
    0.1,
    Number.MAX_SAFE_INTEGER + 1,
  ])
    expect(number(bad)).toBe(false);
  expect(number(0)).toBe(true);
  expect(number(2, 1, 2)).toBe(true);
  expect(number(3, 1, 2)).toBe(false);
  expect(number(0.5, 0, 1, false)).toBe(true);
});
it("checks page and literal cursor types while preserving empty pages and the full character bound", () => {
  for (const value of [undefined, "", "opaque +/=.%", "😀".repeat(2048)])
    expect(cursor(value)).toBe(true);
  for (const value of [null, [], {}, 3, "x".repeat(2049)])
    expect(cursor(value)).toBe(false);
  const check = page(text);
  for (const value of [
    null,
    { items: null },
    { items: {} },
    { items: [1] },
    { items: [], nextCursor: [] },
  ])
    expect(check(value)).toBe(false);
  expect(check({ items: [] })).toBe(true);
  expect(check({ items: ["row"], nextCursor: "opaque" })).toBe(true);
});
it("accepts literal local identifiers and usable timestamps without executable path characters", () => {
  for (const bad of [
    null,
    "",
    ".",
    "..",
    "../auth/logout",
    "a?b",
    "a#b",
    "a%2fb",
    "a\nb",
  ])
    expect(identifier(bad)).toBe(false);
  for (const good of [
    "00000000-0000-0000-0000-000000000001",
    "indexed:abc",
    "sha256:abc",
  ])
    expect(identifier(good)).toBe(true);
  for (const bad of [null, "today", "2026-99-00T00:00:00Z"])
    expect(timestamp(bad)).toBe(false);
  expect(timestamp("2026-09-01T12:00:00.123Z")).toBe(true);
});
