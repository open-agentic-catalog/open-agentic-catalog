// Reusable files preserve authored text. Native validity and command authority
// remain with the existing editors, serializers and backend admission.
import type { components } from "../api/schema";
import { object } from "../api/values";
import { parseJSONObject } from "./json-input";

export type DraftFile = components["schemas"]["CatalogDraftFile"];
export const draftFileFormat = "open-agentic-catalog-draft-v1";
export const maximumDraftFileBytes = 64 * 1024 * 1024;
const documentBytes = 16 * 1024 * 1024;
const browserTextBytes = 2 * 1024 * 1024;
const invalid = () => new Error("Draft JSON does not match the file contract.");
const limit = () =>
  new Error("Draft JSON exceeds a supported byte or structure limit.");
const bytes = (value: string) => new TextEncoder().encode(value).length;
type ObjectValue = Record<string, unknown>;

function closed(
  value: unknown,
  required: string[],
  optional: string[] = [],
): value is ObjectValue {
  return (
    object(value) &&
    required.every((key) => Object.hasOwn(value, key)) &&
    Object.keys(value).every((key) => [...required, ...optional].includes(key))
  );
}

function text(value: unknown, maximum: number): value is string {
  if (typeof value !== "string") return false;
  let length = 0;
  for (const character of value) {
    const point = character.codePointAt(0)!;
    if ((point >= 0xd800 && point <= 0xdfff) || ++length > maximum)
      return false;
  }
  return true;
}
const optional = (value: unknown, check: (value: unknown) => boolean) =>
  value === undefined || check(value);
const texts = (value: unknown, maximum: number, length: number) =>
  Array.isArray(value) &&
  value.length <= maximum &&
  Array.from(value).every((item) => text(item, length));

function asset(value: unknown): boolean {
  return (
    closed(value, ["url", "alt"], ["caption", "rights"]) &&
    text(value.url, 2048) &&
    text(value.alt, 256) &&
    optional(value.caption, (item) => text(item, 512)) &&
    optional(
      value.rights,
      (item) =>
        closed(item, ["basis", "attribution"], ["reference"]) &&
        text(item.basis, 80) &&
        text(item.attribution, 1024) &&
        optional(item.reference, (item) => text(item, 2048)),
    )
  );
}
function artwork(value: unknown): boolean {
  return (
    closed(value, ["schemaVersion"], ["icon", "screenshots"]) &&
    value.schemaVersion === 1 &&
    optional(value.icon, asset) &&
    optional(
      value.screenshots,
      (items) =>
        Array.isArray(items) &&
        items.length <= 8 &&
        Array.from(items).every(asset),
    )
  );
}
function listing(value: unknown): boolean {
  return (
    closed(
      value,
      ["displayName", "description", "visibility"],
      [
        "tags",
        "categories",
        "representativeQueries",
        "documentationUrl",
        "supportUrl",
        "termsUrl",
        "license",
        "relationships",
        "extensions",
        "artwork",
      ],
    ) &&
    text(value.displayName, 160) &&
    text(value.description, 4000) &&
    ["private", "tenant", "public"].includes(value.visibility as string) &&
    optional(value.tags, (item) => texts(item, 32, 80)) &&
    optional(value.categories, (item) => texts(item, 16, 80)) &&
    optional(value.representativeQueries, (item) => texts(item, 5, 240)) &&
    ["documentationUrl", "supportUrl", "termsUrl"].every((key) =>
      optional(value[key], (item) => text(item, 2048)),
    ) &&
    optional(value.license, (item) => text(item, maximumDraftFileBytes)) &&
    optional(value.extensions, (item) => text(item, documentBytes)) &&
    optional(value.artwork, artwork) &&
    optional(
      value.relationships,
      (items) =>
        Array.isArray(items) &&
        items.length <= 64 &&
        Array.from(items).every(
          (item) =>
            closed(item, ["type", "targetIdentifier"], ["targetVersion"]) &&
            text(item.type, 80) &&
            text(item.targetIdentifier, 512) &&
            optional(item.targetVersion, (item) => text(item, 128)),
        ),
    )
  );
}
function input(value: unknown): boolean {
  if (!object(value)) return false;
  switch (value.mode) {
    case "data":
    case "content":
      return closed(value, ["mode", "text"]) && text(value.text, documentBytes);
    case "url":
      return closed(value, ["mode", "url"]) && text(value.url, 2048);
    case "skill-package":
      return closed(value, ["mode", "handoff"]) && text(value.handoff, 16384);
    case "plugin-archive":
      return (
        closed(value, ["mode", "url"], ["expectedDigest"]) &&
        text(value.url, 2048) &&
        optional(value.expectedDigest, (item) => text(item, 71))
      );
    case "plugin-preparation":
      return (
        closed(value, ["mode", "manifest", "mcp", "skills"], ["sourceFiles"]) &&
        text(value.manifest, documentBytes) &&
        text(value.mcp, documentBytes) &&
        texts(value.skills, 1024, documentBytes) &&
        optional(
          value.sourceFiles,
          (items) =>
            Array.isArray(items) &&
            items.length <= 1024 &&
            Array.from(items).every(
              (item) =>
                closed(item, ["path", "retained"]) &&
                text(item.path, 1024) &&
                packagePath(item.path) &&
                typeof item.retained === "boolean",
            ),
        )
      );
    default:
      return false;
  }
}

function packagePath(value: string): boolean {
  const parts = value.split("/");
  return (
    bytes(value) <= 1024 &&
    parts.length <= 17 &&
    parts.every(
      (part) =>
        part !== "" &&
        part !== "." &&
        part !== ".." &&
        bytes(part) <= 255 &&
        !/[\\:\p{Cc}]/u.test(part) &&
        !part.endsWith(".") &&
        !part.endsWith(" "),
    )
  );
}
function file(value: unknown): value is DraftFile {
  return (
    closed(
      value,
      ["format", "kind", "canonicalIdentifier", "version", "representations"],
      ["listing"],
    ) &&
    value.format === draftFileFormat &&
    ["agent", "mcp_server", "skill", "plugin"].includes(value.kind as string) &&
    text(value.canonicalIdentifier, 512) &&
    text(value.version, 128) &&
    Array.isArray(value.representations) &&
    value.representations.length >= 1 &&
    value.representations.length <= 16 &&
    Array.from(value.representations).every(
      (item) =>
        closed(item, ["role", "mediaType", "profileId", "input"]) &&
        ["primary", "distribution", "metadata", "documentation"].includes(
          item.role as string,
        ) &&
        text(item.mediaType, 255) &&
        text(item.profileId, 160) &&
        input(item.input),
    ) &&
    optional(value.listing, listing)
  );
}

function checkTextBytes(value: DraftFile, maximum: number) {
  let documents = 0;
  let preparation = 0;
  for (const rep of value.representations) {
    if (rep.input.mode === "data" || rep.input.mode === "content") {
      const size = bytes(rep.input.text);
      documents += size;
      if (size > maximum) throw limit();
    } else if (rep.input.mode === "plugin-preparation") {
      for (const item of [
        rep.input.manifest,
        rep.input.mcp,
        ...rep.input.skills,
      ]) {
        const size = bytes(item);
        preparation += size;
        if (size > maximum) throw limit();
      }
    }
  }
  if (
    documents > documentBytes ||
    preparation > documentBytes ||
    (value.listing?.extensions !== undefined &&
      bytes(value.listing.extensions) > maximum)
  )
    throw limit();
}

/** Validate only the shared file contract and work bounds. No network or Save occurs. */
export function readDraftFile(source: string | Uint8Array): DraftFile {
  if (source.length > maximumDraftFileBytes) throw limit();
  let raw: string;
  try {
    raw =
      typeof source === "string"
        ? source
        : new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(
            source,
          );
  } catch {
    throw invalid();
  }
  if (!text(raw, maximumDraftFileBytes)) throw invalid();
  if (bytes(raw) > maximumDraftFileBytes) throw limit();
  let value: unknown;
  try {
    // The file's only numeric control is artwork.schemaVersion. Native numeric
    // tokens stay inside strings; control integers match Go's strict decoder.
    value = parseJSONObject(raw, maximumDraftFileBytes, true);
  } catch {
    throw invalid();
  }
  if (!file(value)) throw invalid();
  checkTextBytes(value, documentBytes);
  return value;
}

/** Reject extra state before producing a complete, bounded download. */
export function writeDraftFile(value: DraftFile): string {
  if (!file(value)) throw invalid();
  checkTextBytes(value, documentBytes);
  const raw = JSON.stringify(value);
  if (bytes(raw) > maximumDraftFileBytes) throw limit();
  return raw;
}

/** Existing raw editors can load invalid native text, but not oversized input. */
export function checkBrowserDraft(value: DraftFile): void {
  checkTextBytes(value, browserTextBytes);
}
