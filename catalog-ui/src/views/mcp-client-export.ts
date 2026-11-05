// Derived client input, never a command, metadata fetch or authentication configuration.
import { object } from "./handoff";
import type { Representation } from "./handoff";

export const mcpClientFormats = {
  "mcp-vscode": {
    label: "VS Code",
    path: ".vscode/mcp.json",
  },
  "mcp-claude-code": {
    label: "Claude Code",
    path: ".mcp.json",
  },
  "mcp-cursor": {
    label: "Cursor",
    path: ".cursor/mcp.json",
  },
} as const;
export type MCPClientFormat = keyof typeof mcpClientFormats;
export const mcpExportByteLimit = 8 * 1024 * 1024;

export function supportsMCPExport(rep: Representation) {
  const media = rep.mediaType.split(";", 1)[0]?.trim().toLowerCase();
  return (
    rep.validationState === "valid" &&
    ((rep.profileId === "mcp-registry-server-1.8.1-f52dc85" &&
      media === "application/vnd.mcp.server+json") ||
      (rep.profileId === "mcp-server-card-v1-experimental-526201b" &&
        media === "application/mcp-server-card+json"))
  );
}

function endpoint(value: unknown): value is string {
  if (
    typeof value !== "string" ||
    !/^https:\/\//i.test(value) ||
    /[\uD800-\uDFFF]/u.test(value) ||
    /[\p{Cc}\p{White_Space}\\{}]/u.test(value) ||
    value.includes("#") ||
    /%(?![a-f\d]{2})/i.test(value)
  )
    return false;
  const authority = value.slice(value.indexOf("://") + 3).split(/[/?#]/, 1)[0]!;
  if (!authority || authority.includes("@") || authority.endsWith(":"))
    return false;
  try {
    const url = new URL(value);
    return (
      url.protocol === "https:" &&
      !!url.hostname &&
      !url.username &&
      !url.password &&
      (!url.port || (Number(url.port) >= 1 && Number(url.port) <= 65535))
    );
  } catch {
    return false;
  }
}

function headers(value: unknown) {
  if (value === undefined) return [];
  if (!Array.isArray(value))
    throw new Error(
      "Invalid header descriptors. Use the native document to inspect this connection.",
    );
  const seen = new Set<string>();
  return value.map((input: unknown) => {
    const header = object(input);
    if (
      typeof header.name !== "string" ||
      !/^[!#$%&'*+\-.^_`|~\da-z]+$/i.test(header.name) ||
      seen.has(header.name.toLowerCase()) ||
      (header.isRequired !== undefined &&
        typeof header.isRequired !== "boolean")
    )
      throw new Error(
        "Invalid header descriptors. Use the native document to inspect this connection.",
      );
    seen.add(header.name.toLowerCase());
    return { name: header.name, required: header.isRequired === true };
  });
}

function connection(value: unknown) {
  const remote = object(value);
  if (remote.type !== "streamable-http" && remote.type !== "sse")
    throw new Error("Unsupported transport. Use Streamable HTTP or SSE.");
  if (!endpoint(remote.url))
    throw new Error(
      "This connection needs a literal HTTPS URL without credentials, fragments or placeholders.",
    );
  return {
    url: remote.url,
    transport: remote.type,
    omittedHeaders: headers(remote.headers),
  };
}

export function mcpRemoteChoices(data: unknown, rep: Representation) {
  const doc = object(data);
  if (
    !supportsMCPExport(rep) ||
    (rep.profileId === "mcp-server-card-v1-experimental-526201b" &&
      doc.$schema !==
        "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json") ||
    !Array.isArray(doc.remotes) ||
    !doc.remotes.length
  )
    throw new Error(
      "No supported remote connection is available. Inspect the native document for package or other setup options.",
    );
  return doc.remotes.map((remote: unknown, index: number) => {
    try {
      const selected = connection(remote);
      return {
        position: index + 1,
        label: `Remote ${index + 1}: ${selected.transport === "sse" ? "SSE" : "Streamable HTTP"}`,
        url: selected.url,
        reason: "",
      };
    } catch (error) {
      return {
        position: index + 1,
        label: `Remote ${index + 1}: unavailable`,
        url: "",
        reason:
          error instanceof Error ? error.message : "Unsupported connection.",
      };
    }
  });
}

export function projectMCPConfiguration(
  data: unknown,
  rep: Representation,
  releaseID: string,
  format: MCPClientFormat,
  position: number,
) {
  if (!/^[a-f\d]{8}(?:-[a-f\d]{4}){3}-[a-f\d]{12}$/i.test(releaseID))
    throw new Error("The release identity is invalid.");
  const choices = mcpRemoteChoices(data, rep);
  if (!position && choices.length === 1) position = 1;
  if (!choices.some((choice) => choice.position === position))
    throw new Error("Choose a remote connection.");
  const selected = connection(
    (object(data).remotes as unknown[])[position - 1],
  );
  const entry =
    format === "mcp-cursor"
      ? { url: selected.url }
      : {
          type: selected.transport === "streamable-http" ? "http" : "sse",
          url: selected.url,
        };
  const wrapper = format === "mcp-vscode" ? "servers" : "mcpServers";
  return {
    text:
      JSON.stringify(
        { [wrapper]: { [`catalog-${releaseID.toLowerCase()}`]: entry } },
        null,
        2,
      ) + "\n",
    ...selected,
    position,
  };
}
