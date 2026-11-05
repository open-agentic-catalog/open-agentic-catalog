import { isUtf8 } from "node:buffer";
import { constants } from "node:fs";
import { open } from "node:fs/promises";
import { parseArgs } from "node:util";
import { pathToFileURL } from "node:url";
import { createCatalogClient, SDKError } from "@open-agentic-catalog/client";
import type { CatalogClient } from "@open-agentic-catalog/client";

export interface Options {
  origin: string;
  tokenFile?: string;
  pageSize: number;
  maxPages: number;
  timeoutMs: number;
  allowLoopbackHTTP: boolean;
}

class Failure extends Error {
  readonly code: string;
  readonly status: number | undefined;
  constructor(code: string, status?: number) {
    super(code);
    this.code = code;
    this.status = status;
  }
}

function integer(
  value: string | undefined,
  fallback: number,
  maximum: number,
): number {
  if (value === undefined) return fallback;
  if (!/^[1-9][0-9]*$/u.test(value)) throw new Failure("invalid_arguments");
  const parsed = Number(value);
  if (!Number.isSafeInteger(parsed) || parsed > maximum)
    throw new Failure("invalid_arguments");
  return parsed;
}

export function options(args: string[]): Options {
  const { values } = parseArgs({
    args,
    options: {
      origin: { type: "string" },
      "token-file": { type: "string" },
      "page-size": { type: "string" },
      "max-pages": { type: "string" },
      "timeout-ms": { type: "string" },
      "allow-loopback-http": { type: "boolean", default: false },
    },
  });
  if (!values.origin || values["token-file"] === "")
    throw new Failure("invalid_arguments");
  return {
    origin: values.origin,
    ...(values["token-file"] === undefined
      ? {}
      : { tokenFile: values["token-file"] }),
    pageSize: integer(values["page-size"], 20, 100),
    maxPages: integer(values["max-pages"], 10, 100),
    timeoutMs: integer(values["timeout-ms"], 5000, 30000),
    allowLoopbackHTTP: values["allow-loopback-http"],
  };
}

export async function tokenFromFile(
  path: string,
  signal: AbortSignal,
): Promise<string> {
  signal.throwIfAborted();
  const file = await open(
    path,
    constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK,
  );
  try {
    const stat = await file.stat();
    if (!stat.isFile() || (stat.mode & 0o077) !== 0 || stat.size > 8192)
      throw new Failure("credential_unavailable");
    const data = Buffer.alloc(8193);
    const { bytesRead } = await file.read(data, 0, data.length, 0);
    const raw = data.subarray(0, bytesRead);
    if (bytesRead > 8192 || !isUtf8(raw))
      throw new Failure("credential_unavailable");
    const token = raw.toString("utf8").trim();
    signal.throwIfAborted();
    if (!token || /\s/u.test(token))
      throw new Failure("credential_unavailable");
    return token;
  } finally {
    await file.close();
  }
}

function object(value: unknown): Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value))
    throw new Failure("invalid_response");
  return value as Record<string, unknown>;
}

function text(value: unknown, maximum: number): string {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    [...value].length > maximum
  )
    throw new Failure("invalid_response");
  return value;
}

export function page(value: unknown, pageSize: number) {
  const body = object(value);
  if (
    !Array.isArray(body.items) ||
    body.items.length > pageSize ||
    !Number.isSafeInteger(body.total) ||
    (body.total as number) < 0
  )
    throw new Failure("invalid_response");
  let next: string | undefined;
  if (Object.hasOwn(body, "pageToken")) {
    if (
      typeof body.pageToken !== "string" ||
      Buffer.byteLength(body.pageToken, "utf8") > 2048
    )
      throw new Failure("invalid_response");
    next = body.pageToken || undefined;
  }
  const items = body.items.map((value: unknown) => {
    const item = object(value);
    return {
      identifier: text(item.identifier, 2048),
      displayName: text(item.displayName, 512),
      type: text(item.type, 256),
    };
  });
  return { items, total: body.total as number, next };
}

// JSON lines keep untrusted metadata out of terminal control sequences.
export function line(value: unknown): string {
  return JSON.stringify(value).replace(
    /[\u007f-\u009f\u2028\u2029]/gu,
    (char) => "\\u" + char.charCodeAt(0).toString(16).padStart(4, "0"),
  );
}

export async function list(
  client: CatalogClient,
  input: Options,
  signal: AbortSignal,
  emit: (value: string) => void,
): Promise<number> {
  let pages = 0,
    entries = 0,
    resources = 0,
    registries = 0;
  let total: number | undefined;
  let next: string | undefined;
  const tokens = new Set<string>();
  const finish = (exit: number, reason: string, status?: number) => {
    emit(
      line({
        kind: "summary",
        complete: exit === 0,
        reason,
        pages,
        resourceEntries: resources,
        registryEntriesSkipped: registries,
        ...(total === undefined ? {} : { reportedTotal: total }),
        ...(status === undefined ? {} : { status }),
      }),
    );
    return exit;
  };
  try {
    do {
      signal.throwIfAborted();
      const reply = await client.api.GET("/ard/v1/agents", {
        params: {
          query: {
            pageSize: input.pageSize,
            orderBy: "displayName",
            ...(next === undefined ? {} : { pageToken: next }),
          },
        },
        signal,
        timeoutMs: input.timeoutMs,
      });
      if (reply.response.status !== 200)
        throw new Failure("http_error", reply.response.status);
      const current = page(reply.data, input.pageSize);
      if (
        (total !== undefined && total !== current.total) ||
        entries + current.items.length > current.total
      )
        throw new Failure("invalid_response");
      if (current.next && tokens.has(current.next))
        throw new Failure("repeated_page_token");
      total = current.total;
      next = current.next;
      if (next) tokens.add(next);
      entries += current.items.length;
      pages++;
      for (const item of current.items) {
        if (item.type === "application/ai-registry+json") {
          registries++;
          continue;
        }
        emit(line({ kind: "entry", ...item }));
        resources++;
      }
      if (next && pages >= input.maxPages) return finish(2, "page_limit");
    } while (next);
    if (entries !== total) throw new Failure("invalid_response");
    return finish(0, "exhausted");
  } catch (error) {
    if (signal.aborted) return finish(1, "cancelled");
    if (error instanceof Failure) return finish(1, error.code, error.status);
    return finish(1, error instanceof SDKError ? error.code : "read_failed");
  }
}

async function main(): Promise<number> {
  const cancel = new AbortController();
  const stop = () => cancel.abort();
  process.once("SIGINT", stop);
  process.once("SIGTERM", stop);
  try {
    const input = options(process.argv.slice(2));
    const tokenFile = input.tokenFile;
    const client = createCatalogClient({
      origin: input.origin,
      allowLoopbackHTTP: input.allowLoopbackHTTP,
      timeoutMs: input.timeoutMs,
      responseBytes: 256 * 1024,
      ...(tokenFile === undefined
        ? {}
        : {
            tokenProvider: (signal: AbortSignal) =>
              tokenFromFile(tokenFile, signal),
          }),
    });
    const result = await list(client, input, cancel.signal, (value) =>
      process.stdout.write(value + "\n"),
    );
    if (result === 1)
      process.stderr.write(
        "Discovery failed. Check the origin, access, trust and continuation. Start a new run explicitly.\n",
      );
    return result;
  } catch {
    process.stderr.write(
      "Cannot start discovery. Check the arguments and protected credential file.\n",
    );
    return 1;
  } finally {
    process.removeListener("SIGINT", stop);
    process.removeListener("SIGTERM", stop);
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href)
  process.exitCode = await main();
