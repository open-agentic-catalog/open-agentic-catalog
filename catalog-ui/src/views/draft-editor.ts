// Project only active authoring input. Destination identities and local bytes stay outside files.
import type { Kind } from "../api/client";
import { newRepresentation, type RepresentationDraft } from "./representations";
import {
  checkBrowserDraft,
  draftFileFormat,
  type DraftFile,
} from "./draft-file";

export type ListingSnapshot = NonNullable<DraftFile["listing"]>;

export function draftRepresentations(file: DraftFile): RepresentationDraft[] {
  checkBrowserDraft(file);
  return file.representations.map(({ input, ...fields }) => {
    const draft = { ...newRepresentation(file.kind), ...fields };
    switch (input.mode) {
      case "data":
      case "content":
        return { ...draft, mode: input.mode, content: input.text };
      case "url":
      case "plugin-archive":
        return {
          ...draft,
          mode: "url",
          url: input.url,
          ...(input.mode === "plugin-archive" &&
          input.expectedDigest !== undefined
            ? { expectedDigest: input.expectedDigest }
            : {}),
        };
      case "skill-package":
        return { ...draft, mode: "package", handoff: input.handoff };
      case "plugin-preparation":
        return {
          ...draft,
          mode: "url",
          pluginPreparation: {
            preparing: true,
            manifest: input.manifest,
            mcp: input.mcp,
            skills: input.skills.map((content) => ({
              id: crypto.randomUUID(),
              content,
            })),
            ...(input.sourceFiles
              ? { sourceFiles: structuredClone(input.sourceFiles) }
              : {}),
          },
        };
    }
  });
}

export function editorDraft(
  kind: Kind,
  canonicalIdentifier: string,
  version: string,
  rows: RepresentationDraft[],
  listing?: ListingSnapshot,
): DraftFile {
  return {
    format: draftFileFormat,
    kind,
    canonicalIdentifier,
    version,
    representations: rows.map((row) => {
      if (row.documentStaged)
        throw new Error(
          "Apply or discard each loaded document before downloading draft JSON.",
        );
      let input: DraftFile["representations"][number]["input"];
      const preparation = row.pluginPreparation;
      if (kind === "plugin" && preparation?.preparing) {
        input = {
          mode: "plugin-preparation",
          manifest: preparation.manifest,
          mcp: preparation.mcp,
          skills: preparation.skills.map((skill) => skill.content),
          ...(preparation.sourceFiles
            ? { sourceFiles: preparation.sourceFiles }
            : {}),
        };
      } else if (row.mode === "data" || row.mode === "content") {
        input = { mode: row.mode, text: row.content };
      } else if (row.mode === "package") {
        input = { mode: "skill-package", handoff: row.handoff };
      } else if (kind === "plugin") {
        input = {
          mode: "plugin-archive",
          url: row.url,
          ...(row.expectedDigest !== undefined
            ? { expectedDigest: row.expectedDigest }
            : {}),
        };
      } else input = { mode: "url", url: row.url };
      return {
        role: row.role,
        mediaType: row.mediaType,
        profileId: row.profileId,
        input,
      };
    }),
    ...(listing ? { listing } : {}),
  };
}

/** A decoded server object cannot recover uncertain numeric tokens. Use exact saved export instead. */
export function snapshotExtensions(
  initial: Record<string, unknown> | undefined,
  text: string,
  edited: boolean,
): string {
  if (!edited && initial)
    JSON.stringify(initial, (_key, value: unknown) => {
      if (typeof value === "number")
        throw new Error(
          "Stored Listing extensions contain numbers that need exact export. Download this saved draft from its revision page, or enter the extension text again.",
        );
      return value;
    });
  return text;
}
