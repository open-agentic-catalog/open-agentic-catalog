// MCP fields patch native metadata; pinned backend schemas remain authoritative.
import { object } from "../api/values";
import { listingLink } from "./input-validation";
import { parseJSONObject } from "./json-input";

export const mcpFormats = {
  card: {
    media: "application/mcp-server-card+json",
    schema:
      "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json",
  },
  registry: {
    media: "application/vnd.mcp.server+json",
    schema:
      "https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json",
  },
} as const;
export type MCPFormat = keyof typeof mcpFormats;
export type MCPRequirement = Record<string, unknown> & {
  name?: string;
  description?: string;
  isRequired?: boolean;
  isSecret?: boolean;
};
export type MCPRemote = Record<string, unknown> & {
  type?: string;
  url?: string;
  headers?: MCPRequirement[];
  supportedProtocolVersions?: string[];
};
export type MCPPackage = Record<string, unknown> & {
  registryType?: string;
  identifier?: string;
  version?: string;
  fileSha256?: string;
  transport?: MCPRemote;
  environmentVariables?: MCPRequirement[];
};
export type MCPDocument = Record<string, unknown> & {
  name?: string;
  title?: string;
  description?: string;
  version?: string;
  remotes?: MCPRemote[];
  packages?: MCPPackage[];
};

/** Match Go whitespace rules while retaining arbitrary resource version labels. */
export const mcpVersion = (value: string) =>
  value.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, "");

export function mcpFormat(media: string): MCPFormat | undefined {
  return media === mcpFormats.card.media
    ? "card"
    : media === mcpFormats.registry.media
      ? "registry"
      : undefined;
}

/** New-item defaults do not claim endpoints, protocol support or package versions. */
export function newMCPDocument(
  format: MCPFormat,
  version: string,
): MCPDocument {
  return {
    $schema: mcpFormats[format].schema,
    name: "",
    description: "",
    version: mcpVersion(version),
  };
}

const textFields = (value: Record<string, unknown>, names: string[]) =>
  names.every(
    (name) => value[name] === undefined || typeof value[name] === "string",
  );
const requirements = (value: unknown) =>
  value === undefined ||
  (Array.isArray(value) &&
    value.every(
      (item) =>
        object(item) &&
        textFields(item, ["name", "description"]) &&
        ["isRequired", "isSecret"].every(
          (key) => item[key] === undefined || typeof item[key] === "boolean",
        ),
    ));
const remote = (value: unknown, protocols: boolean): boolean =>
  object(value) &&
  textFields(value, ["type", "url"]) &&
  (value.type === undefined ||
    ["streamable-http", "sse", "stdio"].includes(value.type as string)) &&
  requirements(value.headers) &&
  (!protocols ||
    value.supportedProtocolVersions === undefined ||
    (Array.isArray(value.supportedProtocolVersions) &&
      value.supportedProtocolVersions.every(
        (item) => typeof item === "string",
      )));

/** Recognize only consumed shapes. Preserve every untouched advanced value. */
export function readMCPDocument(
  content: string,
  media: string,
  version: string,
): { document?: MCPDocument; reason?: string } {
  const format = mcpFormat(media);
  if (!format)
    return {
      reason:
        "This MCP format uses Advanced JSON. Its current input stays unchanged.",
    };
  if (!content) return { document: newMCPDocument(format, version) };
  let value: Record<string, unknown>;
  try {
    value = parseJSONObject(content);
  } catch (error) {
    return { reason: (error as Error).message };
  }
  if (
    (format === "card" && value.$schema !== mcpFormats.card.schema) ||
    (format === "registry" &&
      (value.$schema === mcpFormats.card.schema ||
        !textFields(value, ["$schema"])))
  )
    return {
      reason:
        "The document schema does not match this MCP editor. Select the matching format before importing, or use Advanced MCP JSON.",
    };
  if (
    !textFields(value, ["name", "title", "description", "version"]) ||
    (value.remotes !== undefined &&
      (!Array.isArray(value.remotes) ||
        !value.remotes.every(
          (item) =>
            remote(item, format === "card") &&
            (item as MCPRemote).type !== "stdio",
        ))) ||
    (format === "registry" &&
      value.packages !== undefined &&
      (!Array.isArray(value.packages) ||
        !value.packages.every(
          (item) =>
            object(item) &&
            textFields(item, [
              "registryType",
              "identifier",
              "version",
              "fileSha256",
            ]) &&
            (item.transport === undefined || remote(item.transport, false)) &&
            requirements(item.environmentVariables),
        )))
  )
    return {
      reason:
        "This document cannot be safely edited through these fields. Keep it in Advanced MCP JSON or import a different document.",
    };
  return { document: value as MCPDocument };
}

/** Clear an optional value only when the author explicitly empties its field. */
export function mcpOptional<T extends Record<string, unknown>>(
  value: T,
  name: string,
  next: string,
): T {
  const result = { ...value };
  if (next === "") delete result[name];
  else Object.assign(result, { [name]: next });
  return result;
}

/** The two formats share spelling rules, not a conversion or execution contract. */
export function mcpFieldError(
  value: string,
  type:
    | "text"
    | "name"
    | "description"
    | "title"
    | "package-version"
    | "checksum" = "text",
  required = true,
): string {
  if (!value && !required) return "";
  if (!mcpVersion(value)) return "Enter a value.";
  if (value.includes("\0")) return "Remove the NUL character.";
  const maximum =
    type === "name"
      ? 200
      : type === "description" || type === "title"
        ? 100
        : 255;
  if (type !== "text" && [...value].length > maximum)
    return `Use no more than ${maximum} characters.`;
  if (
    type === "name" &&
    (!/^[a-zA-Z0-9.-]+\/[a-zA-Z0-9._-]+$/.test(value) || /\s/u.test(value))
  )
    return "Use a namespace/server name, such as com.example/my-server.";
  if (
    type === "package-version" &&
    (/^[~^<>=]/.test(value) ||
      /(?:^|\.)[xX*](?:\.|$)/.test(value) ||
      value === "latest")
  )
    return "Enter one specific version, not latest or a version range.";
  if (
    type === "checksum" &&
    (value.length !== 64 || !/^[a-f0-9]{64}$/.test(value))
  )
    return "Use 64 lowercase hexadecimal characters.";
  return "";
}

/** URL templates are metadata. No validation path retrieves or expands them. */
export function mcpEndpointError(
  value: string,
  context: "card" | "registry" | "package",
): string {
  if (!value) return "Enter an endpoint URL.";
  const template = /^(?:https?:\/\/[^\s]+|\{[a-zA-Z_][a-zA-Z0-9_]*\}[^\s]*)$/;
  const matches = template.test(value) && !/\s/u.test(value);
  if (context === "package")
    return matches ? "" : "Use an HTTP(S) URL or a declared URL template.";
  if (context === "card" && value.startsWith("{"))
    return matches ? "" : "Use a URL template such as {baseUrl}/mcp.";
  try {
    listingLink(value);
    const url = new URL(value);
    const authority = value.match(/^https:\/\/([^/?#]+)/iu)?.[1];
    if (
      !authority ||
      /[^\u0021-\u007e]/u.test(authority) ||
      url.hash ||
      url.port === "0" ||
      value.includes("\\")
    )
      throw new Error();
    if (!matches) throw new Error();
    return "";
  } catch {
    return "Use an absolute HTTPS URL without credentials, a fragment or secret query parameters.";
  }
}
