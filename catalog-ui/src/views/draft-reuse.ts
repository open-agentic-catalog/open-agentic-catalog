// Reuse stages a complete bounded revision, never reconstructed archive bytes.
import { result, type Client } from "../api/client";
import type { components } from "../api/schema";
import { verifyArtifact } from "./ArtifactDownload";
import { parseJSONObject } from "./json-input";
import { newRepresentation, type RepresentationDraft } from "./representations";

/** Refuse numeric values that cannot survive the existing JSON-object command transport. */
export function reusableJSON(text: string) {
  return parseJSONObject(text);
}
function hasNumber(value: unknown, depth = 0): boolean {
  if (depth > 64)
    throw new Error(
      "Decoded JSON exceeds the supported editor depth. Reupload an exact document.",
    );
  if (typeof value === "number") return true;
  if (!value || typeof value !== "object") return false;
  return Object.values(value).some((item) => hasNumber(item, depth + 1));
}
export async function reuseDraft(
  client: Client,
  kind: components["schemas"]["ResourceKind"],
  revision: {
    representations: (Pick<
      components["schemas"]["Representation"],
      | "role"
      | "mediaType"
      | "profileId"
      | "url"
      | "contentDigest"
      | "artifactId"
      | "data"
      | "plugin"
    > & { package?: unknown })[];
  },
  signal: AbortSignal,
  preferRetained = false,
): Promise<RepresentationDraft[]> {
  if (!revision.representations.length)
    throw new Error("The current revision has no reusable representations.");
  if (revision.representations.length > 16)
    throw new Error(
      "Reuse supports at most sixteen representations. Current input stays unchanged.",
    );
  const drafts: RepresentationDraft[] = [];
  for (const item of revision.representations) {
    const draft = {
      ...newRepresentation(kind),
      role: item.role,
      mediaType: item.mediaType,
      profileId: item.profileId,
    };
    if (item.package)
      throw new Error(
        "An inspected Skill package cannot be reconstructed. Start with a blank replacement and upload the complete archive with its immutable handoff.",
      );
    if (item.plugin || (item.url && !preferRetained)) {
      if (!item.url)
        throw new Error(
          "This archive requires a new supported upload or delivery reference.",
        );
      draft.mode = "url";
      draft.url = item.url;
      if (item.plugin) {
        draft.expectedDigest = item.contentDigest;
        const docs = item.plugin.documents;
        const root = item.plugin.rootDirectory
          ? item.plugin.rootDirectory + "/"
          : "";
        if (
          !docs.every(
            (doc) =>
              typeof doc.content === "string" && typeof doc.path === "string",
          ) ||
          !item.plugin.files.every((file) => typeof file.path === "string")
        )
          throw new Error(
            "The retained Plugin documents are incomplete. Supply the original archive.",
          );
        const manifest = docs.find((doc) => doc.path === root + "plugin.json");
        if (manifest) {
          const mcp = docs.find((doc) => doc.path === root + "mcp.json");
          draft.pluginPreparation = {
            preparing: false,
            manifest: manifest.content,
            mcp: mcp?.content ?? "",
            skills: docs
              .filter((doc) =>
                /^skills\/[^/]+\/SKILL\.md$/.test(doc.path.slice(root.length)),
              )
              .map((doc) => ({
                id: crypto.randomUUID(),
                content: doc.content,
              })),
            sourceFiles: item.plugin.files.map((file) => ({
              path: file.path,
              retained: docs.some((doc) => doc.path === file.path),
            })),
          };
        }
      }
    } else if (item.artifactId) {
      const reply = await client.api.GET(
        "/api/v1/artifacts/{artifactId}/content",
        {
          params: { path: { artifactId: item.artifactId } },
          parseAs: "arrayBuffer",
          signal,
        },
      );
      const bytes = result(reply);
      await verifyArtifact(bytes, reply.response, item.artifactId);
      result(reply);
      if (signal.aborted) throw new Error("Draft read cancelled.");
      const text = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
      if (bytes.byteLength > 2 * 1024 * 1024 || text.includes("\0"))
        throw new Error(
          "The retained document exceeds the editor's supported text bounds.",
        );
      const json = /(?:\+|\/)json$/.test(item.mediaType);
      if (json) reusableJSON(text);
      draft.mode = json ? "data" : "content";
      draft.content = text;
    } else if (item.data !== undefined) {
      if (hasNumber(item.data))
        throw new Error(
          "Decoded inline JSON contains numbers with no original text guarantee. Reupload an exact document or use an allowed HTTPS URL.",
        );
      draft.mode = "data";
      draft.content = JSON.stringify(item.data, null, 2);
      reusableJSON(draft.content);
    } else
      throw new Error(
        "A representation has no reusable document. Supply a complete replacement; no fields have been changed.",
      );
    drafts.push(draft);
  }
  return drafts;
}
