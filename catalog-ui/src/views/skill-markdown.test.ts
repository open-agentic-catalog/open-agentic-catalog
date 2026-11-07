import { describe, expect, it } from "vitest";
import {
  readSkillDocument,
  skillFieldError,
  writeSkillDocument,
  type SkillDocument,
} from "./skill-markdown";
const document: SkillDocument = {
  name: "review-notes",
  description: "Review changes.",
  body: "\n# Review\n\nRead the evidence.\n",
};

it("generates real YAML frontmatter, retains the Markdown body and never adds a top-level version", () => {
  expect(writeSkillDocument(document)).toBe(
    '---\nname: "review-notes"\ndescription: "Review changes."\n---\n\n# Review\n\nRead the evidence.\n',
  );
  expect(readSkillDocument("")).toEqual({
    document: { name: "", description: "", body: "" },
  });
});
it("quotes YAML-looking strings, multiline values, Unicode separators and string metadata", () => {
  const native: SkillDocument = {
    ...document,
    description: 'true: "日本語"\nnext\tline\u0085NEL\u2028LS\u2029PS 😀',
    license: "null",
    compatibility: "Requires git.",
    "allowed-tools": "Bash(git:*) Read",
    metadata: [
      ["version", "1.0"],
      ["true", "false"],
      ["__proto__", "safe"],
      ['quote"key', "value\nnext"],
    ],
  };
  const content = writeSkillDocument(native);
  expect(content).toContain('license: "null"');
  expect(content).toContain('  "version": "1.0"');
  expect(content).toContain("\\u0085");
  expect(content).toContain("\\u2028");
  expect(content).toContain("\\u2029");
  expect(readSkillDocument(content).document).toEqual(native);
});
it("recognizes simple imported YAML, comments and single quotes without altering input on read", () => {
  const imported =
    "---\r\n# retained until an edit\r\nname: review-notes\r\ndescription: 'Review a contributor''s changes.'\r\nmetadata:\r\n  author: Example team\r\n  version: '1.0'\r\n---\r\n\r\n# Body\r\nKeep line endings.\r\n";
  expect(readSkillDocument(imported).document).toEqual({
    name: "review-notes",
    description: "Review a contributor's changes.",
    metadata: [
      ["author", "Example team"],
      ["version", "1.0"],
    ],
    body: "\r\n# Body\r\nKeep line endings.\r\n",
  });
  expect(
    readSkillDocument('---\nname: "a"\ndescription: "b"\nmetadata: {}\n---')
      .document?.metadata,
  ).toEqual([]);
});
it("preserves skipped CR lines and quoted metadata keys while keeping raw value CR in Advanced", () => {
  const body = "\r\n# Body\u0085\u2028\u2029\rraw\r\n";
  const content =
    "---\n \r \n # comment\rcontinues\nname: a\ndescription: 'Keep this.'\nmetadata:\n  'raw\rkey: suffix': 'value'\n  \"escaped\\rkey\": \"escaped\\rvalue\\u0085\\u2028\\u2029\"\n---\n" +
    body;
  expect(readSkillDocument(content)).toEqual({
    document: {
      name: "a",
      description: "Keep this.",
      metadata: [
        ["raw\rkey: suffix", "value"],
        ["escaped\rkey", "escaped\rvalue\u0085\u2028\u2029"],
      ],
      body,
    },
  });
  for (const line of [
    "description: 'raw\rvalue'",
    "metadata:\n  key: 'raw\rvalue'",
  ]) {
    const invalid = `---\nname: a\n${line.startsWith("metadata:") ? "description: valid\n" : ""}${line}\n---\n${body}`;
    expect(readSkillDocument(invalid)).toEqual(
      readSkillDocument("plain Markdown"),
    );
  }
});

it.each(["ordinary", "metadata"])(
  "keeps a long malformed %s field in Advanced without changing valid long space values",
  (kind) => {
    const spaces = " ".repeat(40_000);
    const field =
      kind === "ordinary"
        ? `description:${spaces}\rtext`
        : `metadata:\n  key:${spaces}\rtext`;
    const content = `---\nname: a\n${kind === "metadata" ? "description: valid\n" : ""}${field}\n---\nBody\r\n`;
    expect(readSkillDocument(content)).toEqual(
      readSkillDocument("plain Markdown"),
    );
    const value = `left${spaces}right`;
    const valid =
      kind === "ordinary"
        ? `description: '${value}'`
        : `description: valid\nmetadata:\n  key: '${value}'`;
    expect(
      readSkillDocument(`---\nname: a\n${valid}\n---\nBody\r\n`).document,
    ).toEqual({
      name: "a",
      description: kind === "ordinary" ? value : "valid",
      ...(kind === "metadata" ? { metadata: [["key", value]] } : {}),
      body: "Body\r\n",
    });
  },
);

it.each(["\u0085", "\u2028", "\u2029"])(
  "retains raw header separator %j in Advanced while allowing it in body text",
  (separator) => {
    const header = `---\nname: a\ndescription: 'before${separator}after'\n---\nbody`;
    expect(readSkillDocument(header)).toEqual(
      readSkillDocument("plain Markdown"),
    );
    expect(
      readSkillDocument(`---\nname: a\ndescription: valid\n---\n${separator}`)
        .document?.body,
    ).toBe(separator);
  },
);

it("keeps a BOM before the header in Advanced", () => {
  expect(
    readSkillDocument("\ufeff---\nname: a\ndescription: valid\n---\nbody"),
  ).toEqual(readSkillDocument("plain Markdown"));
});
describe("advanced input remains available without guessed YAML conversions", () => {
  it.each([
    "plain Markdown",
    '---\nname: a\ndescription: "b"\nmetadata:\n  author: "one"\n    nested: "two"\n---\n',
    '---\nname: a\ndescription: "b"\nmetadata:\n   author: "one"\n---\n',
    '---\nname: a\ndescription: "b"\ncompatibility: ""\n---\n',
    '---\nname: a\ndescription: "b"\nmetadata: {}\n  key: "value"\n---\n',
    '---\nname: a\ndescription: "b"\nmetadata:\n---\n',
    '---\nname: a\ndescription: "b"\nmetadata:\n  invalid\n---\n',
    "---\nname: a\ndescription:\n---\n",
    '---\nname: a\ndescription: "\\uD83D\\uDE00"\n---\n',
    "prefix\n---\nname: a\ndescription: b\n---\nbody",
    "---\nname: a\ndescription: b\n",
    "---\nname: a\nname: b\ndescription: c\n---\n",
    "---\nname: a\ndescription: true\n---\n",
    "---\nname: a\ndescription: 123\n---\n",
    "---\nname: a\ndescription: |\n  multiple lines\n---\n",
    '---\nname: a\ndescription: "b"\nversion: "1.0"\n---\n',
    "---\nname: &n a\ndescription: *n\n---\n",
    "---\nname: !!str a\ndescription: b\n---\n",
    '---\nname: a\ndescription: "b"\nmetadata: {a: b}\n---\n',
    '---\nname: a\ndescription: "b"\nmetadata:\n  key: "a"\n  key: "b"\n---\n',
    '---\nname: a\ndescription: "b"\nmetadata:\n  key: true\n---\n',
    '---\nname: a\ndescription: "b"\nmetadata:\n  123: "b"\n---\n',
    "---\nname: a\n---\n",
    '---\nname: a\ndescription: "b\u0085c"\n---\n',
    '---\nname: a\ndescription: "\\u0000"\n---\n',
  ])("preserves unsupported or invalid input: %s", (content) => {
    expect(readSkillDocument(content).reason).toContain("unchanged");
    expect(readSkillDocument(content).document).toBeUndefined();
  });
});
it("uses decoded code points and the backend's exact name and compatibility rules", () => {
  expect(skillFieldError("description", "😀".repeat(1024))).toBe("");
  expect(skillFieldError("description", "😀".repeat(1025))).not.toBe("");
  expect(skillFieldError("description", "\n\u0085")).not.toBe("");
  expect(skillFieldError("compatibility", "")).toBe("");
  expect(skillFieldError("compatibility", " ")).not.toBe("");
  expect(skillFieldError("compatibility", "😀".repeat(500))).toBe("");
  expect(skillFieldError("compatibility", "😀".repeat(501))).not.toBe("");
  for (const name of ["a", "notes-123", "a".repeat(64)])
    expect(skillFieldError("name", name)).toBe("");
  for (const name of ["", "A", "a--b", "-a", "a-", "日本語", "a".repeat(65)])
    expect(skillFieldError("name", name)).not.toBe("");
  expect(skillFieldError("body", "\uD800")).not.toBe("");
  expect(skillFieldError("body", "😀")).toBe("");
});
it("enforces the complete document byte limit and duplicate metadata keys before draft replacement", () => {
  expect(writeSkillDocument({ ...document, metadata: [] })).toContain(
    "metadata: {}",
  );
  const overhead = new TextEncoder().encode(
    writeSkillDocument({ ...document, body: "" }),
  ).length;
  expect(
    writeSkillDocument({ ...document, body: "a".repeat(500_000 - overhead) }),
  ).toHaveLength(500_000);
  expect(
    readSkillDocument(
      writeSkillDocument({ ...document, body: "a".repeat(500_000 - overhead) }),
    ).document?.body,
  ).toHaveLength(500_000 - overhead);
  expect(() =>
    writeSkillDocument({ ...document, body: "a".repeat(500_001 - overhead) }),
  ).toThrow("500,000");
  expect(readSkillDocument("x".repeat(500_001)).document).toBeUndefined();
  expect(() => writeSkillDocument({ ...document, body: "\0" })).toThrow("NUL");
  expect(() =>
    writeSkillDocument({
      ...document,
      metadata: [
        ["a", "1"],
        ["a", "2"],
      ],
    }),
  ).toThrow("different key");
});

it("recognizes a large string map in source order", () => {
  const metadata: [string, string][] = Array.from({ length: 1000 }, (_, at) => [
    `key-${at}`,
    `value-${at}`,
  ]);
  const content = writeSkillDocument({ ...document, metadata });
  const result = readSkillDocument(content);
  expect(result.reason).toBeUndefined();
  expect(result.document!.metadata).toEqual(metadata);
});

it("keeps literal Unicode escape spellings editable without accepting YAML surrogate escapes", () => {
  const content = String.raw`---
name: "escape-notes"
description: "Explain \\uD800 and \\uD83D\\uDE00 literally."
---
Read the notation.
`;
  expect(readSkillDocument(content).document).toEqual({
    name: "escape-notes",
    description: String.raw`Explain \uD800 and \uD83D\uDE00 literally.`,
    body: "Read the notation.\n",
  });
  expect(
    readSkillDocument(String.raw`---
name: "escape-notes"
description: "Invalid \uD83D\uDE00 YAML escapes."
---
`).document,
  ).toBeUndefined();
});
