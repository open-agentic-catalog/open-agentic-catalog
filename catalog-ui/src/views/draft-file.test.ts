import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import {
  readDraftFile,
  writeDraftFile,
  checkBrowserDraft,
  maximumDraftFileBytes,
  type DraftFile,
} from "./draft-file";
import { parseJSONObject } from "./json-input";

const cases = JSON.parse(
  readFileSync(
    new URL("../../../api/testdata/catalog-draft/cases.json", import.meta.url),
    "utf8",
  ),
) as { name: string; document: string; valid: boolean }[];
const validCase = (name: string) => cases.find((item) => item.name === name)!;
const sample = () =>
  readDraftFile(validCase("agent-exact-native-text").document);

describe("shared Catalog draft file fixtures", () => {
  it.each(cases)("$name", (test) => {
    if (!test.valid) {
      expect(() => readDraftFile(test.document)).toThrow("file contract");
      return;
    }
    const value = readDraftFile(new TextEncoder().encode(test.document));
    expect(JSON.parse(writeDraftFile(value))).toEqual(
      JSON.parse(test.document),
    );
    expect(() => checkBrowserDraft(value)).not.toThrow();
  });
});

it("bounds structure before parsing large object graphs", () => {
  for (const raw of [
    "[".repeat(65) + "]".repeat(65),
    '{"unknown":[' + "{},".repeat(131072) + "{}]}",
    '{"unknown":{' + '"a":0,'.repeat(131072) + '"last":0}}',
    '"\\',
  ]) {
    expect(() => readDraftFile(raw)).toThrow("file contract");
  }
});

it("preserves raw native text, unknown fields, signatures and decimals without Save", () => {
  const value = sample();
  const source = value.representations[0]!.input;
  expect(source).toMatchObject({ mode: "data" });
  if (source.mode !== "data") throw new Error("Invalid fixture mode");
  const raw = source.text;
  expect(raw).toContain("9007199254740993");
  expect(raw).toContain("0.123456789012345678901");
  expect(
    readDraftFile(writeDraftFile(value)).representations[0]!.input,
  ).toEqual(source);
  expect(() => parseJSONObject(raw)).toThrow();
  source.text = '{"unfinished":';
  expect(
    readDraftFile(writeDraftFile(value)).representations[0]!.input,
  ).toEqual(source);
  expect(() => parseJSONObject(source.text)).toThrow();
});

it.each([
  new Uint8Array([0xff]),
  new Uint8Array([0xef, 0xbb, 0xbf, 123, 125]),
  "\ud800",
])("rejects invalid transport text without changing it", (raw) => {
  expect(() => readDraftFile(raw)).toThrow("file contract");
});

it("does not serialize inactive inputs, file handles or server identity", () => {
  const value = sample();
  for (const patch of [
    { tenantId: "private-secret-value" },
    { representations: [{ ...value.representations[0], editorID: "old-row" }] },
    {
      representations: [
        {
          ...value.representations[0],
          input: {
            mode: "data",
            text: "",
            url: "https://inactive.example.test",
          },
        },
      ],
    },
  ]) {
    expect(() => writeDraftFile({ ...value, ...patch } as DraftFile)).toThrow(
      "file contract",
    );
  }
  value.canonicalIdentifier = "\ud800";
  expect(() => writeDraftFile(value)).toThrow("file contract");
});

it("rejects sparse arrays before producing invalid JSON null entries", () => {
  const listing = { displayName: "", description: "", visibility: "private" };
  for (const patch of [
    ...["tags", "categories", "representativeQueries", "relationships"].map(
      (key) => ({ listing: { ...listing, [key]: new Array(1) } }),
    ),
    { representations: new Array(1) },
    {
      listing: {
        ...listing,
        artwork: { schemaVersion: 1, screenshots: new Array(1) },
      },
    },
    ...["skills", "sourceFiles"].map((key) => ({
      representations: [
        {
          role: "primary",
          mediaType: "application/vnd.claude-agent-plugin+zip",
          profileId: "",
          input: {
            mode: "plugin-preparation",
            manifest: "",
            mcp: "",
            skills: [],
            [key]: new Array(1),
          },
        },
      ],
    })),
  ]) {
    expect(() =>
      writeDraftFile({ ...sample(), ...patch } as DraftFile),
    ).toThrow("file contract");
  }
});

it("keeps file validity separate from editor byte bounds", () => {
  const value = sample();
  const source = value.representations[0]!.input;
  if (source.mode !== "data") throw new Error("Invalid fixture mode");
  source.text = "x".repeat(2 * 1024 * 1024);
  expect(() => checkBrowserDraft(value)).not.toThrow();
  source.text += "x";
  expect(() => checkBrowserDraft(value)).toThrow("byte or structure limit");
  expect(() => readDraftFile(writeDraftFile(value))).not.toThrow();
  source.text = "\u{1f642}".repeat(4 * 1024 * 1024);
  expect(() => readDraftFile(writeDraftFile(value))).not.toThrow();
  value.representations.push({
    role: "metadata",
    mediaType: "text/plain",
    profileId: "",
    input: { mode: "content", text: "x" },
  });
  expect(() => writeDraftFile(value)).toThrow("byte or structure limit");
});

it("counts encoded overhead before producing a complete download", () => {
  expect(() =>
    readDraftFile(new Uint8Array(maximumDraftFileBytes + 1)),
  ).toThrow("byte or structure limit");
  const value = sample();
  value.representations[0]!.input = {
    mode: "data",
    text: "\x01".repeat(11 * 1024 * 1024),
  };
  expect(() => writeDraftFile(value)).toThrow("byte or structure limit");
  expect(() => readDraftFile(" ".repeat(maximumDraftFileBytes + 1))).toThrow(
    "byte or structure limit",
  );
});

it("keeps Plugin preparation independent of the archive and editor IDs", () => {
  const value = readDraftFile(validCase("plugin-plugin-preparation").document);
  expect(value.representations[0]!.input).toMatchObject({
    mode: "plugin-preparation",
    mcp: "{",
  });
  const input = value.representations[0]!.input;
  if (input.mode !== "plugin-preparation")
    throw new Error("Invalid fixture mode");
  input.manifest = "x".repeat(2 * 1024 * 1024 + 1);
  expect(() => checkBrowserDraft(value)).toThrow("byte or structure limit");
  input.manifest = "";
  input.skills = ["x".repeat(2 * 1024 * 1024 + 1)];
  expect(() => checkBrowserDraft(value)).toThrow("byte or structure limit");
  input.skills = ["x".repeat(16 * 1024 * 1024)];
  input.mcp = "x";
  expect(() => writeDraftFile(value)).toThrow("byte or structure limit");
});

it("preserves incomplete Listing rows and limits the extension editor separately", () => {
  const value = readDraftFile(
    validCase("incomplete-editable-listing").document,
  );
  expect(value.listing).toMatchObject({
    documentationUrl: "https://",
    representativeQueries: ["x", ""],
    relationships: [
      { type: "depends_on", targetIdentifier: "", targetVersion: "" },
    ],
  });
  value.listing!.extensions = "x".repeat(2 * 1024 * 1024);
  expect(() => checkBrowserDraft(value)).not.toThrow();
  value.listing!.extensions += "x";
  expect(() => checkBrowserDraft(value)).toThrow("byte or structure limit");
});

it("uses Unicode character bounds for metadata", () => {
  const value = sample();
  value.canonicalIdentifier = "\u{1f642}".repeat(512);
  expect(() => readDraftFile(writeDraftFile(value))).not.toThrow();
  value.canonicalIdentifier += "x";
  expect(() => writeDraftFile(value)).toThrow("file contract");
});

it.each([null, true, [], {}, { mode: "unknown" }])(
  "rejects an unknown input shape %j",
  (input) => {
    const value = sample();
    expect(() =>
      writeDraftFile({
        ...value,
        representations: [{ ...value.representations[0], input }],
      } as DraftFile),
    ).toThrow("file contract");
  },
);

it("rejects sealed artwork evidence and nested unknown control fields", () => {
  const source = validCase("incomplete-editable-listing").document;
  const mutate = (change: (value: Record<string, unknown>) => void) => {
    const value = JSON.parse(source) as Record<string, unknown>;
    change(value);
    expect(() => readDraftFile(JSON.stringify(value))).toThrow("file contract");
  };
  for (const key of ["digest", "width", "rightsAssessment", "mediaType"]) {
    mutate((value) => {
      const listing = value.listing as Record<string, unknown>;
      const artwork = listing.artwork as Record<string, unknown>;
      (artwork.icon as Record<string, unknown>)[key] = "private-sealed-value";
    });
  }
  for (const [key, invalid] of [
    ["tags", [1]],
    ["categories", null],
    ["representativeQueries", [false]],
    ["relationships", [null]],
    ["artwork", { schemaVersion: 2 }],
    ["extensions", {}],
  ] as const) {
    mutate((value) => {
      (value.listing as Record<string, unknown>)[key] = invalid;
    });
  }
});
