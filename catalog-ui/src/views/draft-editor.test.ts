import { readFileSync } from "node:fs";
import { it, expect } from "vitest";
import {
  draftRepresentations,
  editorDraft,
  snapshotExtensions,
} from "./draft-editor";
import { readDraftFile, writeDraftFile, type DraftFile } from "./draft-file";
import { newRepresentation } from "./representations";
import { draftArtwork, editorArtwork } from "./draft-artwork";

const cases = JSON.parse(
  readFileSync(
    new URL("../../../api/testdata/catalog-draft/cases.json", import.meta.url),
    "utf8",
  ),
) as { name: string; document: string; valid: boolean }[];
it.each(cases.filter((value) => value.valid))(
  "round trips active inputs from $name without Save",
  ({ document }) => {
    const file = readDraftFile(document);
    const rows = draftRepresentations(file);
    expect(new Set(rows.map((row) => row.editorID)).size).toBe(rows.length);
    expect(rows.map((row) => row.editorID)).not.toEqual(
      draftRepresentations(file).map((row) => row.editorID),
    );
    expect(
      readDraftFile(
        writeDraftFile(
          editorDraft(
            file.kind,
            file.canonicalIdentifier,
            file.version,
            rows,
            file.listing,
          ),
        ),
      ),
    ).toEqual(file);
  },
);
it("excludes inactive inputs, local package bytes, preparation and browser IDs", () => {
  const base = {
    ...newRepresentation("plugin"),
    url: "https://example.test/plugin.zip",
    expectedDigest: "sha256:" + "a".repeat(64),
    content: "INACTIVE",
    packageData: "SECRET_BYTES",
    handoff: "INACTIVE",
    pluginPreparation: {
      preparing: false,
      manifest: "INACTIVE",
      mcp: "INACTIVE",
      skills: [{ id: "SECRET_ROW", content: "INACTIVE" }],
    },
  };
  const archive = writeDraftFile(editorDraft("plugin", "", "", [base]));
  expect(archive).not.toMatch(/INACTIVE|SECRET/);
  expect(archive).toContain(base.expectedDigest);
  const {
    expectedDigest: _digest,
    pluginPreparation: _preparation,
    ...withoutArchive
  } = base;
  expect(
    editorDraft("agent", "", "", [withoutArchive]).representations[0]!.input,
  ).toEqual({ mode: "url", url: base.url });
  const preparing = {
    ...base,
    pluginPreparation: {
      ...base.pluginPreparation,
      preparing: true,
      manifest: "unfinished",
      sourceFiles: [],
    },
  };
  expect(
    editorDraft("plugin", "", "", [preparing]).representations[0]!.input,
  ).toEqual({
    mode: "plugin-preparation",
    manifest: "unfinished",
    mcp: "INACTIVE",
    skills: ["INACTIVE"],
    sourceFiles: [],
  });
  expect(
    editorDraft("plugin", "", "", [withoutArchive]).representations[0]!.input,
  ).toEqual({ mode: "plugin-archive", url: base.url });
  expect(
    editorDraft("agent", "", "", [
      { ...base, mode: "data", content: '{"n":9007199254740993' },
    ]).representations[0]!.input,
  ).toEqual({ mode: "data", text: '{"n":9007199254740993' });
  expect(() =>
    editorDraft("agent", "", "", [{ ...base, documentStaged: true }]),
  ).toThrow("Apply or discard");
});
it("strips sealed artwork without applying validation to incomplete declarations", () => {
  expect(
    draftArtwork({
      schemaVersion: 1,
      icon: {
        url: "",
        alt: "",
        size: 40,
        width: 2,
        height: 2,
        mediaType: "image/png",
        digest: "sealed",
        rights: { basis: "owned", attribution: "", reference: "" },
      },
      screenshots: [],
    }),
  ).toEqual({
    schemaVersion: 1,
    icon: {
      url: "",
      alt: "",
      rights: { basis: "owned", attribution: "", reference: "" },
    },
    screenshots: [],
  });
  expect(draftArtwork({ schemaVersion: 1 })).toEqual({ schemaVersion: 1 });
  expect(
    draftArtwork({
      schemaVersion: 1,
      icon: {
        url: "",
        alt: "",
        caption: "",
        rights: { basis: "licensed", attribution: "author" },
      },
    }),
  ).toEqual({
    schemaVersion: 1,
    icon: {
      url: "",
      alt: "",
      caption: "",
      rights: { basis: "licensed", attribution: "author" },
    },
  });
});

it("opens incomplete artwork without claiming rights or retaining sealed facts", () => {
  expect(editorArtwork(undefined)).toEqual({ schemaVersion: 1 });
  expect(
    editorArtwork({
      schemaVersion: 1,
      icon: { url: "", alt: "" },
      screenshots: [
        {
          url: "",
          alt: "",
          rights: { basis: "custom", attribution: "", reference: "" },
        },
      ],
    }),
  ).toEqual({
    schemaVersion: 1,
    icon: { url: "", alt: "", rights: { basis: "", attribution: "" } },
    screenshots: [
      {
        url: "",
        alt: "",
        rights: { basis: "custom", attribution: "", reference: "" },
      },
    ],
  });
  expect(
    draftArtwork({ schemaVersion: 1, icon: { url: "", alt: "" } as never }),
  ).toEqual({ schemaVersion: 1, icon: { url: "", alt: "" } });
});

it("refuses lossy exports of decoded server numbers until exact editor text replaces them", () => {
  expect(snapshotExtensions(undefined, "unfinished", false)).toBe("unfinished");
  expect(snapshotExtensions({ nested: { text: "kept" } }, "{}", false)).toBe(
    "{}",
  );
  for (const n of [
    0.1,
    9007199254740992,
    JSON.parse("1.0000000000000000001") as number,
  ])
    expect(() => snapshotExtensions({ nested: { n } }, "{}", false)).toThrow(
      "exact export",
    );
  expect(
    snapshotExtensions({ n: 0.1 }, '{"n":0.1000000000000000001}', true),
  ).toContain("0.1000000000000000001");
});

it("opens Plugin preparation without an inventory and an archive without a checksum", () => {
  const base = editorDraft("plugin", "", "", [newRepresentation("plugin")]);
  const inputs: DraftFile["representations"][number]["input"][] = [
    { mode: "plugin-preparation", manifest: "", mcp: "", skills: [] },
    { mode: "plugin-archive", url: "" },
  ];
  for (const input of inputs) {
    const file = {
      ...base,
      representations: [{ ...base.representations[0]!, input }],
    };
    const row = draftRepresentations(file)[0]!;
    expect(
      editorDraft("plugin", "", "", [row]).representations[0]!.input,
    ).toEqual(input);
  }
});
