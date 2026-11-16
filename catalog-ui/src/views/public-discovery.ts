// Public ARD reads have no session, private API client or resource-fetch authority.
import type { Runtime } from "../api/session";

export type PublicEntry = {
  identifier: string;
  name: string;
  description: string;
  type: string;
  version?: string;
  url?: string;
  source?: string;
  tags: string[];
  capabilities: string[];
  representativeQueries: string[];
};
export type PublicPage = {
  entries: PublicEntry[];
  pageToken?: string;
  total?: number;
};
export type PublicSelection = { text: string; pageSize: number };
const maximumBytes = 16 * 1024 * 1024;

/** A fixed diagnostic avoids reflecting untrusted upstream bodies or URLs. */
export class PublicDiscoveryError extends Error {
  readonly code: string;
  constructor(code: string) {
    super(code);
    this.code = code;
  }
}
function invalid(): never {
  throw new PublicDiscoveryError("invalid_publication_response");
}
function object(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
function text(value: unknown, max: number, empty = false): value is string {
  return (
    typeof value === "string" &&
    (empty || value.length > 0) &&
    [...value].length <= max &&
    !value.includes("\0") &&
    !/[\uD800-\uDFFF]/u.test(value)
  );
}
function strings(value: unknown): string[] {
  if (value === undefined) return [];
  if (
    !Array.isArray(value) ||
    value.length > 64 ||
    !value.every((v) => text(v, 1024))
  )
    return invalid();
  return value as string[];
}

/** Validate only returned fields used for display; unknown standards metadata stays inert. */
export function publicEntry(value: unknown): PublicEntry {
  if (!object(value) || !text(value.identifier, 4096)) return invalid();
  for (const [key, max] of [
    ["displayName", 4096],
    ["description", 32768],
    ["type", 1024],
    ["version", 1024],
    ["url", 8192],
    ["source", 8192],
  ] as const) {
    if (value[key] !== undefined && !text(value[key], max, true))
      return invalid();
  }
  return {
    identifier: value.identifier,
    name:
      typeof value.displayName === "string" && value.displayName
        ? value.displayName
        : value.identifier,
    description: typeof value.description === "string" ? value.description : "",
    type: typeof value.type === "string" ? value.type : "",
    ...(typeof value.version === "string" ? { version: value.version } : {}),
    ...(typeof value.url === "string" ? { url: value.url } : {}),
    ...(typeof value.source === "string" ? { source: value.source } : {}),
    tags: strings(value.tags),
    capabilities: strings(value.capabilities),
    representativeQueries: strings(value.representativeQueries),
  };
}

/** List and Search keep their distinct ARD envelopes and opaque continuation. */
export function publicPage(
  value: unknown,
  search: boolean,
  limit: number,
): PublicPage {
  if (!object(value)) return invalid();
  const entries: unknown = value[search ? "results" : "items"];
  if (
    !Array.isArray(entries) ||
    entries.length > limit ||
    (value.pageToken !== undefined && !text(value.pageToken, 4096, true)) ||
    (value.total !== undefined &&
      (typeof value.total !== "number" ||
        !Number.isSafeInteger(value.total) ||
        value.total < entries.length)) ||
    (value.referrals !== undefined &&
      (!Array.isArray(value.referrals) || value.referrals.length))
  )
    return invalid();
  return {
    entries: entries.map(publicEntry),
    ...(typeof value.pageToken === "string" && value.pageToken
      ? { pageToken: value.pageToken }
      : {}),
    ...(typeof value.total === "number" ? { total: value.total } : {}),
  };
}

/** The manifest count is an independent observation, not a generation guarantee for List. */
export function manifestCount(value: unknown): number {
  if (
    !object(value) ||
    !Array.isArray(value.entries) ||
    value.entries.length > 100001
  )
    return invalid();
  return value.entries.length;
}

/** Kind captions do not invent compatibility for Registry or unfamiliar media types. */
export function publicKind(type: string): string {
  const base = type.split(";")[0]?.trim().toLowerCase();
  if (
    [
      "application/a2a-agent-card+json",
      "application/vnd.open-agentic-catalog.agent+json",
    ].includes(base ?? "")
  )
    return "Agent";
  if (
    [
      "application/mcp-server-card+json",
      "application/vnd.mcp.server+json",
      "application/vnd.open-agentic-catalog.mcp-registration+json",
    ].includes(base ?? "")
  )
    return "MCP Server";
  if (
    [
      "application/agent-skills+md",
      "application/agent-skills+json",
      "application/agent-skills+zip",
      "application/agent-skills+gzip",
      "text/markdown",
    ].includes(base ?? "")
  )
    return "Skill";
  if (
    [
      "application/agent-plugins+zip",
      "application/agent-plugins+gzip",
    ].includes(base ?? "")
  )
    return "Plugin";
  return base === "application/ai-registry+json"
    ? "Registry metadata"
    : "Other metadata";
}

/** Buffer a finite UTF-8 response before parsing or displaying any upstream data. */
async function boundedJSON(response: Response): Promise<unknown> {
  if (
    !response.body ||
    response.headers
      .get("content-type")
      ?.split(";")[0]
      ?.trim()
      .toLowerCase() !== "application/json"
  ) {
    await response.body?.cancel().catch(() => {});
    return invalid();
  }
  const reader = response.body.getReader();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let size = 0,
    raw = "";
  try {
    for (;;) {
      const part = await reader.read();
      if (part.done) break;
      size += part.value.byteLength;
      if (size > maximumBytes) return invalid();
      raw += decoder.decode(part.value, { stream: true });
    }
    raw += decoder.decode();
    return JSON.parse(raw) as unknown;
  } finally {
    await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}

/** Fixed same-origin routes omit cookies and never follow redirects or declared URLs. */
export async function publicRead(
  runtime: Runtime,
  operation: "manifest" | "agents" | "search",
  selection: PublicSelection,
  pageToken: string,
  signal: AbortSignal,
): Promise<unknown> {
  if (
    !runtime.publicDiscoveryEnabled ||
    !/^(?:\/[a-zA-Z0-9_-]+)*$/.test(runtime.basePath) ||
    runtime.basePath === "/docs" ||
    runtime.basePath.startsWith("/docs/") ||
    !["manifest", "agents", "search"].includes(operation) ||
    !Number.isSafeInteger(selection.pageSize) ||
    selection.pageSize < 1 ||
    selection.pageSize > 100 ||
    !text(selection.text, 500, true) ||
    (operation === "search" && !selection.text.trim()) ||
    !text(pageToken, 4096, true)
  )
    throw new PublicDiscoveryError("invalid_publication_request");
  let path = `${runtime.basePath}/_discovery/${operation}`;
  const headers: Record<string, string> = {
    Accept: "application/json",
    "X-Catalog-Request": "1",
  };
  let body: string | undefined;
  if (operation === "agents") {
    const query = new URLSearchParams({ pageSize: String(selection.pageSize) });
    if (pageToken) query.set("pageToken", pageToken);
    path += `?${query}`;
  }
  if (operation === "search") {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify({
      text: selection.text,
      pageSize: selection.pageSize,
      ...(pageToken ? { pageToken } : {}),
    });
  }
  try {
    const response = await fetch(path, {
      method: operation === "search" ? "POST" : "GET",
      headers,
      ...(body === undefined ? {} : { body }),
      credentials: "omit",
      cache: "no-store",
      redirect: "error",
      referrerPolicy: "no-referrer",
      signal: AbortSignal.any([signal, AbortSignal.timeout(30000)]),
    });
    if (response.status !== 200) {
      await response.body?.cancel();
      throw new PublicDiscoveryError(
        response.status === 400
          ? "continuation_or_selection_refused"
          : [401, 403, 404, 503].includes(response.status)
            ? "publication_unavailable"
            : "publication_read_failed",
      );
    }
    return await boundedJSON(response);
  } catch (error) {
    if (error instanceof PublicDiscoveryError) throw error;
    throw new PublicDiscoveryError("publication_read_failed");
  }
}
