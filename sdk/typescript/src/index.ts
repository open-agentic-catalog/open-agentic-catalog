import createClient from "openapi-fetch";
import type { FetchResponse, MaybeOptionalInit } from "openapi-fetch";
import type {
  HttpMethod,
  PathsWithMethod,
  RequiredKeysOf,
} from "openapi-typescript-helpers";
import type { paths } from "./schema.js";
export type { paths, components, operations } from "./schema.js";

export type ErrorCode =
  | "invalid_configuration"
  | "invalid_request"
  | "invalid_response"
  | "request_too_large"
  | "response_too_large"
  | "headers_too_large"
  | "unexpected_encoding"
  | "request_failed"
  | "deadline_exceeded"
  | "cancelled";
/** Fixed diagnostics only. HTTP bodies and dependency causes are never attached. */
export class SDKError extends Error {
  constructor(
    public readonly code: ErrorCode,
    public readonly status?: number,
    public readonly requestID?: string,
  ) {
    super(`Catalog SDK: ${code}`);
    this.name = "SDKError";
    for (const key of ["status", "requestID"])
      Object.defineProperty(this, key, { enumerable: false });
  }
}
export interface Config {
  origin: string;
  allowLoopbackHTTP?: boolean;
  token?: string;
  tokenProvider?: (
    signal: AbortSignal,
  ) => string | undefined | Promise<string | undefined>;
  timeoutMs?: number;
  requestBytes?: number;
  responseBytes?: number;
  /** Parsed headers only; native Fetch owns the transport-level header cap. */
  headerBytes?: number;
}
export interface CallOptions {
  signal?: AbortSignal;
  timeoutMs?: number;
  responseBytes?: number;
}
export interface RawOptions extends CallOptions {
  method?: "GET" | "HEAD" | "POST" | "PUT" | "PATCH" | "DELETE" | "OPTIONS";
  headers?: HeadersInit;
  body?: string | Uint8Array;
}
export interface RawResponse {
  status: number;
  headers: Headers;
  bytes: Uint8Array;
  text(): string;
}
type Method =
  "GET" | "POST" | "PUT" | "PATCH" | "DELETE" | "OPTIONS" | "HEAD" | "TRACE";
// Preserve upstream path/method inference. Runtime rejects transport escape hatches,
// including ones supplied by JavaScript or through a TypeScript cast.
type Forbidden =
  | "baseUrl"
  | "fetch"
  | "Request"
  | "middleware"
  | "bodySerializer"
  | "pathSerializer"
  | "querySerializer"
  | "redirect"
  | "credentials";
type SafeOptions<T> = Omit<NonNullable<T>, Forbidden> & {
  [K in Forbidden]?: never;
} & CallOptions;
type SafeMethod<M extends HttpMethod> = <
  P extends PathsWithMethod<paths, M>,
  I extends SafeOptions<MaybeOptionalInit<paths[P], M>>,
>(
  path: P,
  ...init: RequiredKeysOf<I> extends never ? [I?] : [I]
) => Promise<FetchResponse<NonNullable<paths[P][M]>, I, `${string}/${string}`>>;
export type API = { [M in Method]: SafeMethod<Lowercase<M>> };
export interface CatalogClient {
  api: API;
  raw(path: string, options?: RawOptions): Promise<RawResponse>;
  rawBytes(response: Response): Uint8Array;
}
const bytesByResponse = new WeakMap<Response, Uint8Array>();
const encoder = new TextEncoder();
function budget(value: number): number {
  if (!Number.isSafeInteger(value) || value <= 0 || value > 1024 ** 3)
    throw new SDKError("invalid_configuration");
  return value;
}
function timeout(value: number): number {
  if (!Number.isFinite(value) || value <= 0 || value > 2 ** 31 - 1)
    throw new SDKError("invalid_configuration");
  return value;
}
function unsafeText(value: string): boolean {
  return /[\s\\]/u.test(value) || [...value].some((c) => c.charCodeAt(0) < 32);
}
function originFor(config: Config): string {
  try {
    const u = new URL(config.origin);
    if (
      unsafeText(config.origin) ||
      u.username ||
      u.password ||
      /[?#]/u.test(config.origin) ||
      u.search ||
      u.hash ||
      u.pathname !== "/" ||
      (u.port !== "" && Number(u.port) === 0) ||
      (config.token !== undefined && config.tokenProvider !== undefined)
    )
      throw 0;
    const loopback =
      u.hostname === "[::1]" || /^127(?:\.\d{1,3}){3}$/u.test(u.hostname);
    if (
      u.protocol !== "https:" &&
      !(u.protocol === "http:" && config.allowLoopbackHTTP === true && loopback)
    )
      throw 0;
    return u.origin;
  } catch {
    throw new SDKError("invalid_configuration");
  }
}
function validPath(path: string): void {
  if (
    typeof path !== "string" ||
    !path.startsWith("/") ||
    path.startsWith("//") ||
    unsafeText(path) ||
    path.includes("#")
  )
    throw new SDKError("invalid_request");
}
async function readBounded(
  body: ReadableStream<Uint8Array> | null,
  limit: number,
  code: ErrorCode,
  signal: AbortSignal,
): Promise<Uint8Array<ArrayBuffer>> {
  if (!body) return new Uint8Array();
  const reader = body.getReader();
  const abort = () => {
    void reader.cancel().catch(() => {});
  };
  signal.addEventListener("abort", abort, { once: true });
  if (signal.aborted) abort();
  const chunks: Uint8Array[] = [];
  let size = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > limit) throw new SDKError(code);
      chunks.push(value);
    }
    const bytes = new Uint8Array(size);
    let offset = 0;
    for (const chunk of chunks) {
      bytes.set(chunk, offset);
      offset += chunk.byteLength;
    }
    return bytes;
  } finally {
    try {
      await reader.cancel();
    } catch {
      /* Preserve the classified failure. */
    }
    reader.releaseLock();
    signal.removeEventListener("abort", abort);
  }
}
/** Use native Fetch. Injected fetch is an explicit trusted test boundary, not a per-call option. */
export function createCatalogClient(
  config: Config,
  fetcher: typeof fetch = globalThis.fetch,
): CatalogClient {
  const origin = originFor(config);
  if (typeof fetcher !== "function")
    throw new SDKError("invalid_configuration");
  const timeoutMs = timeout(config.timeoutMs ?? 30_000);
  const requestBytes = budget(config.requestBytes ?? 16 * 1024 ** 2);
  const responseBytes = budget(config.responseBytes ?? 8 * 1024 ** 2);
  const headerBytes = budget(config.headerBytes ?? 32 * 1024);
  const token = config.token;
  const provider = config.tokenProvider;
  if (
    (provider !== undefined && typeof provider !== "function") ||
    (token !== undefined &&
      (typeof token !== "string" || /[^\x21-\x7e]/u.test(token)))
  )
    throw new SDKError("invalid_configuration");
  async function exchange(
    request: Request,
    options: CallOptions,
  ): Promise<Response> {
    if (new URL(request.url).origin !== origin)
      throw new SDKError("invalid_request");
    const limit = budget(options.responseBytes ?? responseBytes);
    const duration = Math.min(
      timeout(options.timeoutMs ?? timeoutMs),
      timeoutMs,
    );
    const started = performance.now();
    const controller = new AbortController();
    const abort = () => controller.abort();
    const caller = options.signal ?? request.signal;
    if (caller.aborted) throw new SDKError("cancelled");
    caller.addEventListener("abort", abort, { once: true });
    let expired = false;
    const timer = setTimeout(() => {
      expired = true;
      controller.abort();
    }, duration);
    let upstream: Response | undefined;
    const cancelled = new Promise<never>((_, reject) => {
      controller.signal.addEventListener(
        "abort",
        () => reject(new SDKError(expired ? "deadline_exceeded" : "cancelled")),
        { once: true },
      );
    });
    // Timers can run late after synchronous work or a suspended event loop.
    const checkActive = () => {
      if (
        !controller.signal.aborted &&
        performance.now() - started >= duration
      ) {
        expired = true;
        controller.abort();
      }
      if (controller.signal.aborted)
        throw new SDKError(expired ? "deadline_exceeded" : "cancelled");
    };
    const work = async () => {
      const body = await readBounded(
        request.body,
        requestBytes,
        "request_too_large",
        controller.signal,
      );
      checkActive();
      const credential = provider ? await provider(controller.signal) : token;
      checkActive();
      if (
        credential !== undefined &&
        (typeof credential !== "string" || /[^\x21-\x7e]/u.test(credential))
      )
        throw new SDKError("invalid_request");
      const headers = new Headers(request.headers);
      for (const name of [
        "authorization",
        "cookie",
        "proxy-authorization",
        "host",
        "accept-encoding",
        "content-length",
        "transfer-encoding",
      ])
        headers.delete(name);
      if (credential) headers.set("Authorization", `Bearer ${credential}`);
      headers.set("Accept-Encoding", "identity");
      upstream = await fetcher(
        new Request(request.url, {
          method: request.method,
          headers,
          ...(request.body ? { body } : {}),
          signal: controller.signal,
          credentials: "omit",
          redirect: "manual",
          cache: "no-store",
          referrerPolicy: "no-referrer",
        }),
      );
      checkActive();
      let count = 0;
      for (const [key, value] of upstream.headers)
        count += encoder.encode(key + value).length + 4;
      if (count > headerBytes) throw new SDKError("headers_too_large");
      const encoding = upstream.headers.get("content-encoding");
      if (encoding && encoding !== "identity")
        throw new SDKError("unexpected_encoding");
      const length = upstream.headers.get("content-length");
      if (length && /^\d+$/u.test(length) && Number(length) > limit)
        throw new SDKError("response_too_large");
      const bytes = await readBounded(
        upstream.body,
        limit,
        "response_too_large",
        controller.signal,
      );
      checkActive();
      const response = new Response(bytes.length ? bytes : null, {
        status: upstream.status,
        statusText: upstream.statusText,
        headers: upstream.headers,
      });
      bytesByResponse.set(response, bytes);
      return response;
    };
    try {
      return await Promise.race([work(), cancelled]);
    } catch (error) {
      controller.abort();
      try {
        await upstream?.body?.cancel();
      } catch {
        /* A locked stream is cancelled by its reader. */
      }
      if (error instanceof SDKError)
        throw upstream && error.status === undefined
          ? new SDKError(
              error.code,
              upstream.status,
              upstream.headers.get("X-Request-ID") ?? undefined,
            )
          : error;
      throw new SDKError(
        expired
          ? "deadline_exceeded"
          : caller.aborted
            ? "cancelled"
            : "request_failed",
        upstream?.status,
        upstream?.headers.get("X-Request-ID") ?? undefined,
      );
    } finally {
      clearTimeout(timer);
      caller.removeEventListener("abort", abort);
    }
  }
  const api = {} as API;
  for (const method of [
    "GET",
    "POST",
    "PUT",
    "PATCH",
    "DELETE",
    "OPTIONS",
    "HEAD",
    "TRACE",
  ] as const) {
    const call = async (
      path: string,
      options: Record<string, unknown> = {},
    ) => {
      validPath(path);
      if (!options || typeof options !== "object" || Array.isArray(options))
        throw new SDKError("invalid_request");
      // A whitelist also rejects inherited/custom fetch, middleware and Request constructors.
      if (
        Object.keys(options).some(
          (key) =>
            ![
              "params",
              "body",
              "headers",
              "signal",
              "parseAs",
              "timeoutMs",
              "responseBytes",
            ].includes(key),
        )
      )
        throw new SDKError("invalid_request");
      if (
        options.parseAs !== undefined &&
        !["json", "text", "arrayBuffer", "blob", "stream"].includes(
          String(options.parseAs),
        )
      )
        throw new SDKError("invalid_request");
      let response: Response | undefined;
      try {
        const { timeoutMs, responseBytes, ...init } = options;
        const local = createClient<paths>({
          baseUrl: origin,
          fetch: async (request) => {
            response = await exchange(request, {
              ...(timeoutMs !== undefined
                ? { timeoutMs: timeoutMs as number }
                : {}),
              ...(responseBytes !== undefined
                ? { responseBytes: responseBytes as number }
                : {}),
            });
            return response;
          },
        });
        const result = await (
          local[method] as (
            p: string,
            o: object,
          ) => Promise<{ data?: unknown; error?: unknown; response: Response }>
        )(path, init);
        const bodyless =
          response?.status === 204 ||
          response?.status === 304 ||
          method === "HEAD";
        if (
          !bodyless &&
          (options.parseAs === undefined ||
            options.parseAs === "json" ||
            !response?.ok)
        ) {
          // openapi-fetch falls back to raw text when error JSON is malformed.
          // Decode the bounded bytes so typed error responses cannot use that fallback.
          if (response && !response.ok)
            result.error = JSON.parse(
              new TextDecoder().decode(bytesByResponse.get(response)!),
            );
          if (
            result.data === null ||
            result.error === null ||
            (result.data === undefined && result.error === undefined)
          )
            throw new SDKError(
              "invalid_response",
              response?.status,
              response?.headers.get("X-Request-ID") ?? undefined,
            );
        }
        return result;
      } catch (error) {
        if (error instanceof SDKError) throw error;
        throw new SDKError(
          response ? "invalid_response" : "invalid_request",
          response?.status,
          response?.headers.get("X-Request-ID") ?? undefined,
        );
      }
    };
    Object.assign(api, { [method]: call });
  }
  return Object.freeze({
    api: Object.freeze(api),
    rawBytes(response: Response) {
      const bytes = bytesByResponse.get(response);
      if (!bytes) throw new SDKError("invalid_response");
      return bytes.slice();
    },
    async raw(path: string, options: RawOptions = {}): Promise<RawResponse> {
      validPath(path);
      if (!options || typeof options !== "object" || Array.isArray(options))
        throw new SDKError("invalid_request");
      if (
        Object.keys(options).some(
          (key) =>
            ![
              "method",
              "headers",
              "body",
              "signal",
              "timeoutMs",
              "responseBytes",
            ].includes(key),
        )
      )
        throw new SDKError("invalid_request");
      if (
        options.body !== undefined &&
        typeof options.body !== "string" &&
        !(options.body instanceof Uint8Array)
      )
        throw new SDKError("invalid_request");
      try {
        const response = await exchange(
          new Request(origin + path, {
            method: options.method ?? "GET",
            ...(options.headers ? { headers: options.headers } : {}),
            ...(options.signal ? { signal: options.signal } : {}),
            ...(options.body !== undefined
              ? {
                  body:
                    typeof options.body === "string"
                      ? options.body
                      : new Uint8Array(options.body),
                }
              : {}),
          }),
          options,
        );
        const bytes = bytesByResponse.get(response)!.slice();
        return {
          status: response.status,
          headers: new Headers(response.headers),
          bytes,
          text: () => {
            try {
              return new TextDecoder("utf-8", { fatal: true }).decode(bytes);
            } catch {
              throw new SDKError(
                "invalid_response",
                response.status,
                response.headers.get("X-Request-ID") ?? undefined,
              );
            }
          },
        };
      } catch (error) {
        if (error instanceof SDKError) throw error;
        throw new SDKError("invalid_request");
      }
    },
  });
}
