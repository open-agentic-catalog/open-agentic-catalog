// A supported native version update changes one field, never protocol or package versions.
import type { Kind } from "../api/client";
import type { RepresentationDraft } from "./representations";
import { readAgentCard, signedAgentCard } from "./agent-card";
import { readMCPDocument } from "./mcp-metadata";

export function newVersionContent(
  kind: Kind,
  rows: RepresentationDraft[],
  version: string,
) {
  const changes: string[] = [];
  const drafts = rows.map((row, index) => {
    if (row.mode !== "data" || !row.content) return row;
    let doc: Record<string, unknown> | undefined;
    if (
      kind === "agent" &&
      row.mediaType === "application/a2a-agent-card+json"
    ) {
      doc = readAgentCard(row.content, version).card;
      if (doc && doc.version !== version && signedAgentCard(doc))
        throw new Error(
          "Create an unsigned Agent Card copy or import an externally re-signed Card before changing its version.",
        );
    } else if (
      kind === "mcp_server" &&
      [
        "application/mcp-server-card+json",
        "application/vnd.mcp.server+json",
      ].includes(row.mediaType)
    ) {
      doc = readMCPDocument(row.content, row.mediaType, version).document;
    }
    if (!doc || doc.version === version) return row;
    changes.push(
      `Representation ${index + 1}: native version ${String(doc.version ?? "(not set)")} to ${version}`,
    );
    return {
      ...row,
      content: JSON.stringify({ ...doc, version }, null, 2) + "\n",
    };
  });
  return { drafts, changes };
}
