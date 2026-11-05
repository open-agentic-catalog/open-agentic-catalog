// Session-bound typed transport is shared by workflow reads, commands and host authentication.
import createClient from "openapi-fetch";
import { isUIProcessInfo, type UIProcessInfo, isBuildInfo } from "./build-info";
import type { BuildInfo } from "./build-info";
import type { Check } from "./values";
import { retryAfter } from "./retry-after";
import {
  loginFailureMessage,
  decodeSession,
  decodeLoginOptions,
  decodeLoginLocation,
} from "./session";
import type {
  Runtime,
  SessionReply,
  LoginOptions,
  LoginLocation,
} from "./session";
export type { Runtime, Session, SessionReply } from "./session";
import type { FetchResponse } from "openapi-fetch";
import type { paths, components, operations } from "./schema";

export type Access = components["schemas"]["CurrentAccess"];
export type SearchResponse = NonNullable<
  FetchResponse<operations["searchCatalog"], object, "application/json">["data"]
>;
export type SearchResult = SearchResponse["items"][number];
export type RecordDetail = NonNullable<
  FetchResponse<
    operations["getCatalogRecord"],
    object,
    "application/json"
  >["data"]
>;
export type Kind = components["schemas"]["ResourceKind"];

// Parsing a streamed response can finish after fetch resolves. Check the
// identity again when a decoded result is consumed, not only at the headers.
const responseMetadata = new WeakMap<
  Response,
  { isCurrent: () => boolean; retryAt: number | undefined }
>();
/** Abort consumption when a response belongs to a superseded identity lifetime. */
function sessionChanged(): never {
  throw new DOMException("Session changed", "AbortError");
}

/** Expose fixed user-facing failure text while retaining status, code and request ID. */
export class APIError extends Error {
  status: number;
  code: string;
  requestId: string;
  retryAt: number | undefined;
  constructor(status: number, code: string, requestId = "", retryAt?: number) {
    super(
      loginFailureMessage(code) ||
        (code === "WEBHOOK_TEST_BUSY" && status === 503
          ? "The API test slot is busy. No request was sent. Return to the form and prepare a later test with a new key."
          : code === "PUBLICATION_NOT_READY"
            ? "Publication requirements are not met. Check current validation, the approved publisher domain, and two to five representative queries for a public listing."
            : status === 401
              ? "Your session is unavailable. Sign in again."
              : status === 403
                ? "Your current access does not permit this action."
                : status === 412
                  ? "The record changed. Reload it before trying again."
                  : status === 409
                    ? "This action conflicts with the current state. Check the record and the action requirements."
                    : status === 404
                      ? "This record is unavailable with your current access. Check the reference or return to its list."
                      : status === 400
                        ? "The request is no longer valid. Start a new search or check your input."
                        : "The request could not be completed. Please try again."),
    );
    this.status = status;
    this.code = /^[A-Za-z][A-Za-z0-9_]{0,79}$/.test(code) ? code : "";
    this.requestId = /^[A-Za-z0-9._-]{1,128}$/.test(requestId) ? requestId : "";
    this.retryAt = retryAt;
    if (retryAt !== undefined) {
      this.message += ` The server suggests retrying after ${new Date(retryAt).toLocaleString()}. Retry manually when ready.`;
    }
  }
}

/** Project safe timing without decoding a direct response's private error body. */
function responseError(response: Response, code: string): APIError {
  return new APIError(
    response.status,
    code,
    response.headers.get("X-Request-ID") ?? "",
    responseMetadata.get(response)?.retryAt,
  );
}

/** One request lifetime per identity. Private responses from an old session
 * cannot repopulate a new workspace, even if the transport ignores abort. */
export function makeAPI(runtime: Runtime) {
  let generation = 0;
  let lifetime = new AbortController();
  const invalidate = () => {
    generation++;
    lifetime.abort();
    lifetime = new AbortController();
  };
  const request: typeof fetch = async (input, init) => {
    const epoch = generation;
    const source = new Request(input, init);
    const headers = new Headers(source.headers);
    headers.set("X-Catalog-Request", "1");
    const response = await fetch(
      new Request(source, {
        headers,
        credentials: "same-origin",
        cache: "no-store",
        redirect: "error",
        signal: AbortSignal.any([source.signal, lifetime.signal]),
      }),
    ).catch((error: unknown) => {
      if (epoch !== generation) sessionChanged();
      throw error;
    });
    if (epoch !== generation) sessionChanged();
    responseMetadata.set(response, {
      isCurrent: () => epoch === generation,
      retryAt:
        response.status === 429 || response.status === 503
          ? retryAfter(response.headers.get("Retry-After"), Date.now())
          : undefined,
    });
    return response;
  };
  const api = createClient<paths>({
    baseUrl: location.origin + runtime.basePath + "/_catalog",
    fetch: request,
  });
  // Keep openapi-fetch serialization and parseAs handling. A request-local fetch
  // captures only its response, so parse failures retain status/retry advice and
  // cannot expose parser text or cross an invalidated identity lifetime.
  function guarded<F extends (...args: never[]) => Promise<unknown>>(
    send: F,
  ): F {
    return (async (...args: Parameters<F>) => {
      const epoch = generation;
      let response: Response | undefined;
      const fetchReply: typeof fetch = async (input, init) => {
        response = await request(input, init);
        return response;
      };
      // Preserve the library's generic path/options/result relationship. The
      // sole changed option is its documented per-call fetch implementation.
      const [path, options]: unknown[] = args;
      const forwarded = [
        path,
        { ...(typeof options === "object" ? options : {}), fetch: fetchReply },
      ] as Parameters<F>;
      try {
        const reply = await send(...forwarded);
        if (epoch !== generation) sessionChanged();
        return reply;
      } catch {
        if (epoch !== generation) sessionChanged();
        if (response) throw responseError(response, "invalid_api_response");
        throw new APIError(0, "request_failed");
      }
    }) as F;
  }
  const checkedAPI = {
    ...api,
    GET: guarded(api.GET),
    POST: guarded(api.POST),
    PATCH: guarded(api.PATCH),
    DELETE: guarded(api.DELETE),
  };
  function auth(path: "session"): Promise<SessionReply>;
  function auth(path: "options"): Promise<LoginOptions>;
  function auth(
    path: "login",
    method: "POST",
    body: { alias?: string },
  ): Promise<LoginLocation>;
  function auth(path: "logout", method: "POST", body?: object): Promise<void>;
  /** Endpoint-specific decoding prevents caller-selected assertions about host JSON. */
  async function auth(
    path: "session" | "options" | "login" | "logout",
    method = "GET",
    body?: object,
  ): Promise<SessionReply | LoginOptions | LoginLocation | void> {
    const epoch = generation;
    const response = await request(
      location.origin + runtime.basePath + "/auth/" + path,
      {
        method,
        headers: { "Content-Type": "application/json" },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      },
    );
    if (epoch !== generation) sessionChanged();
    if (!response.ok)
      throw responseError(
        response,
        path === "login" && response.status === 503
          ? "sign_in_unavailable"
          : "session_request_failed",
      );
    const invalidCode =
      path === "session" ? "invalid_session_response" : "invalid_auth_response";
    if (response.status !== (path === "logout" ? 204 : 200))
      throw responseError(response, invalidCode);
    if (path === "logout") return;
    try {
      const value: unknown = await response.json();
      if (epoch !== generation) sessionChanged();
      if (path === "session") return decodeSession(value);
      if (path === "options") return decodeLoginOptions(value, runtime);
      return decodeLoginLocation(value, runtime);
    } catch {
      if (epoch !== generation) sessionChanged();
      throw responseError(response, invalidCode);
    }
  }
  const reference = async (signal: AbortSignal) => {
    const response = await request(
      location.origin + runtime.basePath + "/_reference",
      { signal },
    );
    if (!response.ok)
      throw responseError(response, "catalog_contract_unavailable");
    try {
      const data: unknown = await response.json();
      return result({ data, response });
    } catch {
      if (responseMetadata.get(response)?.isCurrent() === false)
        sessionChanged();
      throw responseError(response, "catalog_contract_unavailable");
    }
  };
  const buildInfo = async (signal: AbortSignal): Promise<BuildInfo> => {
    const response = await request(
      location.origin + runtime.basePath + "/_build",
      { signal },
    );
    if (response.status !== 200)
      throw responseError(response, "ui_build_unavailable");
    try {
      const data: unknown = await response.json();
      const value = result({ data, response });
      if (!isBuildInfo(value)) throw new Error("invalid build identity");
      return value;
    } catch {
      if (responseMetadata.get(response)?.isCurrent() === false)
        sessionChanged();
      throw responseError(response, "ui_build_unavailable");
    }
  };
  const processInfo = async (signal: AbortSignal): Promise<UIProcessInfo> => {
    const response = await request(
      location.origin + runtime.basePath + "/_status",
      { signal },
    );
    if (response.status !== 200)
      throw responseError(response, "ui_status_unavailable");
    try {
      const data: unknown = await response.json();
      const value = result({ data, response });
      if (!isUIProcessInfo(value))
        throw new Error("invalid UI process information");
      return value;
    } catch {
      if (responseMetadata.get(response)?.isCurrent() === false)
        sessionChanged();
      throw responseError(response, "ui_status_unavailable");
    }
  };
  return {
    processInfo,
    buildInfo,
    api: checkedAPI,
    auth,
    reference,
    invalidate,
    get signal() {
      return lifetime.signal;
    },
  };
}

export type Client = ReturnType<typeof makeAPI>;

/** Consume a decoded API reply only while its session is current. Screens use
 * this instead of reading reply.data directly, including after body parsing. */
export function result<T>(
  reply: {
    data?: T;
    error?: unknown;
    response: Response;
  },
  check?: Check,
): T {
  if (responseMetadata.get(reply.response)?.isCurrent() === false)
    sessionChanged();
  if (!reply.response.ok || reply.data === undefined) {
    const error = reply.error;
    const code =
      error &&
      typeof error === "object" &&
      "code" in error &&
      typeof error.code === "string"
        ? error.code
        : "request_failed";
    throw responseError(reply.response, code);
  }
  if (check && !check(reply.data))
    throw responseError(reply.response, "invalid_api_response");
  return reply.data;
}

/** Local browsing is separate from tenant-wide external discovery. */
export function canReadLocal(access: Access): boolean {
  return (
    access.availablePermissions.includes("resource.read") &&
    (!access.scopeEnforcement || access.scopes.includes("catalog.read"))
  );
}

/** Offer unified Catalog navigation for the caller's authorized population. */
export function canRead(access: Access): boolean {
  return (
    (access.availablePermissions.includes("resource.read") ||
      access.permissions.includes("federation.discover")) &&
    (!access.scopeEnforcement || access.scopes.includes("catalog.read"))
  );
}

/** Metadata links never execute code or carry this application's credentials. */
export function safeLink(value?: string): string | undefined {
  if (!value) return undefined;
  try {
    const url = new URL(value);
    return url.protocol === "https:" && !url.username && !url.password
      ? url.href
      : undefined;
  } catch {
    return undefined;
  }
}

/** Validate the documented bodyless acknowledgement while retaining error/lifetime handling. */
export function acknowledge(reply: {
  error?: unknown;
  response: Response;
}): void {
  result({ ...reply, data: true });
  if (reply.response.status !== 204)
    throw responseError(reply.response, "invalid_api_response");
}
