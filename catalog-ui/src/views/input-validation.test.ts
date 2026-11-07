import { expect, it } from "vitest";
import {
  byteText,
  characterText,
  metadataTerms,
  atField,
  InputError,
  listingLink,
  trimGoWhitespace,
} from "./input-validation";

it("trims the complete Go White_Space set while retaining opaque Unicode text", () => {
  const whitespace =
    "\t\n\v\f\r \u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000";
  const opaque = "\ufeffCafé:e\u0301:🙂 \u0085 inner\ufeff";
  expect(trimGoWhitespace(whitespace + opaque + whitespace)).toBe(opaque);
  expect(trimGoWhitespace(whitespace)).toBe("");
  expect(trimGoWhitespace("")).toBe("");
  expect(trimGoWhitespace(" \u200b\u180e\ud800x\udfff ")).toBe(
    "\u200b\u180e\ud800x\udfff",
  );
  expect(
    trimGoWhitespace("\u0085" + "a" + " ".repeat(40_000) + "b\u0085"),
  ).toBe("a" + " ".repeat(40_000) + "b");
});

it.each([
  ["a".repeat(500), 500, true],
  ["a".repeat(501), 500, false],
  ["é".repeat(250), 500, true],
  ["é".repeat(251), 500, false],
  ["🙂".repeat(125), 500, true],
  ["🙂".repeat(126), 500, false],
  ["🙂".repeat(64), 256, true],
  ["🙂".repeat(65), 256, false],
  ["a\0b", 500, false],
] as const)("validates the actual UTF-8 byte budget", (value, limit, valid) => {
  if (valid) expect(byteText(value, "Text", limit)).toBe(value);
  else expect(() => byteText(value, "Text", limit)).toThrow(/UTF-8 bytes/);
});
it.each(["é", "🙂", "e\u0301"])(
  "counts code points without normalizing %s",
  (unit) => {
    const value = unit.repeat(80 / [...unit].length);
    expect(characterText(value, "Tag", 80)).toBe(value);
    expect(() => characterText(value + "a", "Tag", 80)).toThrow(/characters/);
    expect(() => characterText("ab", "Query", 240, 3)).toThrow();
    expect(() => characterText("a\0b", "Tag", 80)).toThrow();
  },
);
it("checks each collection before preparing and attributes the error to its field", () => {
  const values = ["literal,comma", "🙂".repeat(80)];
  expect(metadataTerms(values, "tags", 32)).toBe(values);
  expect(() =>
    atField("tags", () =>
      metadataTerms(
        Array.from({ length: 33 }, (_, i) => String(i)),
        "tags",
        32,
      ),
    ),
  ).toThrow(InputError);
  expect(() => metadataTerms(["a".repeat(81)], "categories", 16)).toThrow();
});

it.each([
  "http://example.com",
  "not a URL",
  "https://user:pass@example.com",
  "https://example.com?API_Key=secret",
  "https://example.com?X-Amz-Signature=secret",
  "https://example.com/\nprivate",
  "https://example.com/" + "a".repeat(2048),
])("rejects unsafe listing links", (value) =>
  expect(() => listingLink(value)).toThrow(),
);
it("retains an ordinary HTTPS link with harmless query and document fragment", () => {
  const value = "https://example.com/docs?page=2#help";
  expect(listingLink(value)).toBe(value);
});
