import { expect, it, vi } from "vitest";
import fc from "fast-check";
import { documentChanges, visibleChangeText } from "./document-changes";

it("finds the changed address and exact number token inside short JSON", () => {
  const result = documentChanges(
    '{"url":"https://old.example/mcp","n":1.0}',
    '{"url":"https://new.example/mcp","n":1e0}',
  );
  expect(result).toMatchObject({
    available: true,
    changes: [
      {
        before: { value: "old", line: 1, column: 17 },
        after: { value: "new", line: 1, column: 17 },
      },
      { before: { value: "." }, after: { value: "e" } },
    ],
  });
});

it("keeps additions, removals, Unicode positions and ordered multiline context", () => {
  const result = documentChanges(
    "# Notes\nKeep\nRemove\nEnd\n",
    "# Notes\nKeep\nEnd\nAppend\n",
  );
  expect(result).toMatchObject({
    available: true,
    changes: [
      {
        before: { value: "Remove\n", line: 3, column: 1 },
        after: { value: "", line: 3, column: 1 },
      },
      {
        before: { value: "", line: 5, column: 1 },
        after: { value: "Append\n", line: 4, column: 1 },
      },
    ],
  });
  expect(documentChanges("😀old\n", "😀new\n")).toMatchObject({
    available: true,
    changes: [
      {
        before: { value: "old", column: 2 },
        after: { value: "new", column: 2 },
      },
    ],
  });
});

it.each([
  ["hello", "\uFEFFhello", "", "\uFEFF"],
  ["one\r\ntwo\r\n", "one\ntwo\n", "\r", ""],
  ["one\n", "one", "\n", ""],
  ["a b", "a\tb", " ", "\t"],
])(
  "preserves invisible differences: %j -> %j",
  (before, after, removed, added) => {
    const result = documentChanges(before, after);
    expect(result).toMatchObject({ available: true });
    if (!result.available) throw Error("Expected a complete comparison");
    expect(result.changes[0]).toMatchObject({
      before: { value: removed },
      after: { value: added },
    });
  },
);

it("reports identical text without invented changes, including large originals", () => {
  expect(documentChanges("", "")).toEqual({ available: true, changes: [] });
  expect(documentChanges("x".repeat(100000), "x".repeat(100000))).toEqual({
    available: true,
    changes: [],
  });
});

it("renders explicit whitespace markers only in the excerpt presentation", () => {
  expect(visibleChangeText(" \t\r\n\uFEFF<script>")).toBe(
    "\u00b7\\t\\r\\n\n[BOM]<script>",
  );
});

it.each([
  ["x".repeat(65537), "y"],
  ["\n".repeat(2000), "\n"],
  ["a\n".repeat(200), "b\n".repeat(200)],
  ["a".repeat(5000), "b".repeat(5000)],
  ["a".repeat(200), "b".repeat(200)],
  ["a-".repeat(101), "b-".repeat(101)],
  [
    Array.from(
      { length: 7 },
      (_, i) => `a${"s".repeat(2500)}\nsame${i}\n`,
    ).join(""),
    Array.from(
      { length: 7 },
      (_, i) => `b${"s".repeat(2500)}\nsame${i}\n`,
    ).join(""),
  ],
])(
  "refuses expensive input without disclosing a partial result %#",
  (before, after) => {
    expect(documentChanges(before, after)).toEqual({ available: false });
  },
);

it("discards a comparison when the algorithm deadline expires", () => {
  const abort = vi
    .spyOn(Date, "now")
    .mockReturnValueOnce(0)
    .mockReturnValue(1000);
  try {
    expect(documentChanges("before", "after")).toEqual({ available: false });
  } finally {
    abort.mockRestore();
  }
});

it("bounds excerpt context around long unchanged text", () => {
  const result = documentChanges(
    "p".repeat(200) + "old" + "s".repeat(200),
    "p".repeat(200) + "new" + "s".repeat(200),
  );
  expect(result).toMatchObject({
    available: true,
    changes: [
      {
        before: {
          prefix: "..." + "p".repeat(60),
          value: "old",
          suffix: "s".repeat(60) + "...",
        },
        after: {
          prefix: "..." + "p".repeat(60),
          value: "new",
          suffix: "s".repeat(60) + "...",
        },
      },
    ],
  });
});

it("accounts for every original character across generated Unicode edits", () => {
  const text = fc
    .array(
      fc.constantFrom(
        "a",
        "b",
        "😀",
        "\uFEFF",
        "\r",
        "\n",
        " ",
        "\t",
        "<",
        "1",
        ".",
      ),
      { maxLength: 60 },
    )
    .map((parts) => parts.join(""));
  const offset = (source: string, line: number, column: number) => {
    const lines = source.split("\n");
    return (
      lines
        .slice(0, line - 1)
        .reduce((size, value) => size + value.length + 1, 0) +
      Array.from(lines[line - 1]!)
        .slice(0, column - 1)
        .join("").length
    );
  };
  fc.assert(
    fc.property(text, text, (before, after) => {
      const result = documentChanges(before, after);
      expect(result.available).toBe(true);
      if (!result.available)
        throw Error("Small inputs must compare completely");
      let oldEnd = 0,
        newEnd = 0;
      for (const change of result.changes) {
        const oldStart = offset(
          before,
          change.before.line,
          change.before.column,
        );
        const newStart = offset(after, change.after.line, change.after.column);
        expect(before.slice(oldEnd, oldStart)).toBe(
          after.slice(newEnd, newStart),
        );
        expect(
          before.slice(oldStart, oldStart + change.before.value.length),
        ).toBe(change.before.value);
        expect(
          after.slice(newStart, newStart + change.after.value.length),
        ).toBe(change.after.value);
        oldEnd = oldStart + change.before.value.length;
        newEnd = newStart + change.after.value.length;
      }
      expect(before.slice(oldEnd)).toBe(after.slice(newEnd));
    }),
    { numRuns: 100 },
  );
});
