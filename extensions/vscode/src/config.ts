import { nativeAbsolute } from "./platform.js";
import { createHash } from "node:crypto";

export class ClientError extends Error {
  constructor(
    readonly code: string,
    readonly status?: number,
    readonly requestId?: string,
  ) {
    super(`Catalog: ${code}`);
    this.name = "ClientError";
  }
}
/** Stop this caller promptly; a late UI/storage completion cannot resume it. */
export function abortable<T>(
  work: PromiseLike<T>,
  signal?: AbortSignal,
  code = "request_canceled",
): Promise<T> {
  if (!signal) return Promise.resolve(work);
  return new Promise<T>((resolve, reject) => {
    const cancel = () => reject(new ClientError(code));
    if (signal.aborted) cancel();
    else signal.addEventListener("abort", cancel, { once: true });
    void Promise.resolve(work)
      .then(resolve, reject)
      .finally(() => signal.removeEventListener("abort", cancel));
  });
}
export function connectionLabel(value: unknown): string {
  if (
    typeof value !== "string" ||
    !value ||
    [...value].length > 80 ||
    /[\p{Cc}\p{Cs}]/u.test(value)
  )
    throw new ClientError("invalid_configuration");
  return value;
}
/** Only bounded, credential-free correlation fields may leave the transport. */
export function requestCorrelation(
  value: unknown,
  privateValues: readonly (string | undefined)[] = [],
): string | undefined {
  return typeof value === "string" &&
    /^[A-Za-z0-9._-]{1,128}$/u.test(value) &&
    !privateValues.some((secret) => secret && value.includes(secret))
    ? value
    : undefined;
}
export function responseError(
  code: string,
  status: number,
  headers: Headers,
  privateValues: readonly (string | undefined)[] = [],
): ClientError {
  const id = headers.get("x-request-id");
  return new ClientError(code, status, requestCorrelation(id, privateValues));
}
export interface Connection {
  id: string;
  label: string;
  origin: string;
  mode: "control" | "published";
  auth: "manual" | "oidc" | "anonymous";
  allowLoopbackHTTP: boolean;
  timeoutMs: number;
  requestBytes: number;
  responseBytes: number;
  headerBytes: number;
  caFile?: string;
  proxyURL?: string;
  oidc?: {
    issuer: string;
    clientId: string;
    audience: string;
    scopes: string[];
    refresh: boolean;
  };
}
export function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value))
    throw new ClientError("invalid_response");
  return value as Record<string, unknown>;
}
export function text(value: unknown, max = 512): string {
  if (
    typeof value !== "string" ||
    !value ||
    value.length > max ||
    [...value].some((c) => c.charCodeAt(0) < 32 || c.charCodeAt(0) === 127)
  )
    throw new ClientError("invalid_response");
  return value;
}
export function safeURL(
  value: unknown,
  loopback: boolean,
  root = true,
): string {
  const input = text(value, 2048);
  let url: URL;
  try {
    url = new URL(input);
  } catch {
    throw new ClientError("invalid_configuration");
  }
  if (
    /[\s\\?#]/u.test(input) ||
    url.username ||
    url.password ||
    url.port === "0" ||
    (root && url.pathname !== "/") ||
    (url.protocol !== "https:" &&
      !(
        loopback &&
        url.protocol === "http:" &&
        ["127.0.0.1", "[::1]"].includes(url.hostname)
      ))
  )
    throw new ClientError("invalid_configuration");
  return root ? url.origin : input;
}
function bool(value: unknown, fallback = false): boolean {
  if (value === undefined) return fallback;
  if (typeof value !== "boolean")
    throw new ClientError("invalid_configuration");
  return value;
}
function integer(
  value: unknown,
  fallback: number,
  max: number,
  min = 1024,
): number {
  if (value === undefined) return fallback;
  if (
    !Number.isSafeInteger(value) ||
    (value as number) < min ||
    (value as number) > max
  )
    throw new ClientError("invalid_configuration");
  return value as number;
}
function parseConnections(input: unknown): Connection[] {
  if (!Array.isArray(input) || input.length > 20)
    throw new ClientError("invalid_configuration");
  const ids = new Set<string>();
  return input.map((value: unknown) => {
    const item = record(value);
    if (
      Object.keys(item).some(
        (k) =>
          ![
            "id",
            "label",
            "origin",
            "mode",
            "auth",
            "allowLoopbackHTTP",
            "timeoutMs",
            "requestBytes",
            "responseBytes",
            "headerBytes",
            "caFile",
            "proxyURL",
            "oidc",
          ].includes(k),
      )
    )
      throw new ClientError("invalid_configuration");
    const id = text(item.id, 40);
    if (!/^[a-z][a-z0-9-]{0,39}$/u.test(id) || ids.has(id))
      throw new ClientError("invalid_configuration");
    ids.add(id);
    if (
      !["control", "published"].includes(String(item.mode)) ||
      !["manual", "oidc", "anonymous"].includes(String(item.auth)) ||
      (item.mode === "control" && item.auth === "anonymous")
    )
      throw new ClientError("invalid_configuration");
    const allowLoopbackHTTP = bool(item.allowLoopbackHTTP);
    const result: Connection = {
      id,
      label: connectionLabel(item.label),
      origin: safeURL(item.origin, allowLoopbackHTTP),
      mode: item.mode as Connection["mode"],
      auth: item.auth as Connection["auth"],
      allowLoopbackHTTP,
      timeoutMs: integer(item.timeoutMs, 30000, 120000, 1000),
      requestBytes: integer(item.requestBytes, 16 * 1024 ** 2, 16 * 1024 ** 2),
      responseBytes: integer(item.responseBytes, 8 * 1024 ** 2, 32 * 1024 ** 2),
      headerBytes: integer(item.headerBytes, 32768, 65536),
    };
    if (item.caFile !== undefined) {
      result.caFile = text(item.caFile, 4096);
      if (!nativeAbsolute(result.caFile))
        throw new ClientError("invalid_configuration");
    }
    if (item.proxyURL !== undefined) {
      const proxy = new URL(text(item.proxyURL, 2048));
      if (
        !["http:", "https:"].includes(proxy.protocol) ||
        proxy.username ||
        proxy.password ||
        proxy.port === "0" ||
        proxy.pathname !== "/" ||
        proxy.search ||
        proxy.hash ||
        /[\s\\?#]/u.test(String(item.proxyURL))
      )
        throw new ClientError("invalid_configuration");
      result.proxyURL = proxy.origin;
    }
    if (result.auth === "oidc") {
      const oidc = record(item.oidc);
      if (
        Object.keys(oidc).some(
          (k) =>
            !["issuer", "clientId", "audience", "scopes", "refresh"].includes(
              k,
            ),
        )
      )
        throw new ClientError("invalid_configuration");
      if (!Array.isArray(oidc.scopes) || oidc.scopes.length > 20)
        throw new ClientError("invalid_configuration");
      const scopes = oidc.scopes.map((s) => text(s, 128));
      if (
        !scopes.includes("openid") ||
        scopes.some((s) => !/^[\x21\x23-\x5B\x5D-\x7E]+$/u.test(s)) ||
        new Set(scopes).size !== scopes.length
      )
        throw new ClientError("invalid_configuration");
      const refresh = bool(oidc.refresh);
      if (!refresh && scopes.includes("offline_access"))
        throw new ClientError("invalid_configuration");
      result.oidc = {
        issuer: safeURL(oidc.issuer, allowLoopbackHTTP, false),
        clientId: text(oidc.clientId),
        audience: text(oidc.audience),
        scopes,
        refresh,
      };
    } else if (item.oidc !== undefined)
      throw new ClientError("invalid_configuration");
    return result;
  });
}
export function connections(input: unknown): Connection[] {
  try {
    return parseConnections(input);
  } catch {
    throw new ClientError("invalid_configuration");
  }
}
export function binding(connection: Connection, host: string): string {
  return createHash("sha256")
    .update(JSON.stringify([host, connection]))
    .digest("hex");
}
export function diagnostic(error: unknown): string {
  const advice: Record<string, string> = {
    selection_changed:
      ". The selection changed. Refresh the view and reselect the current resource or installed package before trying again.",
    owned_package_required:
      ". Refresh My tools and select a verified owned installation. No files or native settings were changed.",
    native_mcp_document_required:
      ". This result has no native Server Card or Registry server.json document. Inspect its metadata; an indexed reader projection cannot register an MCP server.",
    native_agent_document_required:
      ". This result has no native A2A Agent Card. Inspect its metadata; an indexed reader projection cannot open an Agent conversation.",
    close_evidence_documents:
      ". Close one or more read-only Catalog evidence tabs, then inspect again. At most sixteen may remain open.",
    help_unavailable:
      ". The maintained offline README is unavailable in this extension installation. Reinstall the local VSIX or restore the source README.md.",
    peer_consent_required:
      ". This consumer requires explicit consent before repeating a peer-capable Catalog query.",
    peer_query_cancelled:
      ". This declined revalidation sent no peer replay or resource target request.",
    mcp_start_cancelled:
      ". Catalog preparation was canceled. No new server definition was returned. Native MCP controls own running processes.",
    credential_in_use:
      ". Another Catalog window is updating these credentials. Retry after it finishes; use Recover Catalog Login only after closing other Catalog windows if the owner stopped.",
    credential_update_pending:
      ". This window has not observed the credential update. Wait for its SecretStorage change, then reselect this connection. If the update was interrupted, close other Catalog windows and use Recover Catalog Login; do not repeat the old refresh token.",
    connection_removal_incomplete:
      ". Removal is incomplete. Check the connection settings and retry Remove Connection.",
    settings_write_unconfirmed:
      ". Check native user or remote settings before retrying; the wizard does not enter credentials.",
  };
  if (error instanceof ClientError)
    return (
      error.message +
      (advice[error.code] ?? "") +
      (error.status
        ? ` (HTTP ${error.status}${error.requestId ? `; request ${error.requestId}` : ""})`
        : "")
    );
  return "Catalog: request_failed. Check the connection, trust settings and permissions.";
}
