import { afterEach, expect, it, vi } from "vitest";
import { acknowledge, APIError, makeAPI, result } from "./client";
import { uncertain } from "./commands";
import { object, page, text } from "./values";
import { deferred } from "../../tests/unit/fixtures";

const runtime = {
  basePath: "/unit",
  authMode: "development" as const,
  documentationUrl: "",
};
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});
function clientWith(response: Response) {
  vi.stubGlobal("location", new URL("http://localhost/unit"));
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => response),
  );
  return makeAPI(runtime);
}
it.each([
  null,
  [],
  { items: null },
  { items: [null] },
  { items: [], nextCursor: 1 },
])("rejects unusable successful pages without echoing data: %j", (data) => {
  const response = new Response(null, {
    status: 200,
    headers: { "X-Request-ID": "request-1" },
  });
  expect(() => result({ data, response }, page(text))).toThrow(APIError);
  try {
    result({ data, response }, page(text));
  } catch (error) {
    expect(error).toMatchObject({
      status: 200,
      code: "invalid_api_response",
      requestId: "request-1",
    });
    expect(uncertain(error)).toBe(true);
  }
});
it.each([null, false, 0, ""])(
  "preserves contract-allowed falsy values: %j",
  (data) => {
    expect(result({ data, response: new Response() }, (v) => v === data)).toBe(
      data,
    );
  },
);
it.each([undefined, "15"])(
  "sanitizes both library JSON decode paths (content length %s)",
  async (length) => {
    const response = new Response('{"private":"secret sentinel"', {
      status: 201,
      headers: {
        ...(length ? { "Content-Length": length } : {}),
        "X-Request-ID": "write-1",
      },
    });
    const client = clientWith(response);
    await expect(
      client.api.POST("/api/v1/namespaces", {
        params: { header: { "Idempotency-Key": "stable" } },
        body: { displayName: "Tools", slug: "tools" },
      }),
    ).rejects.toMatchObject({
      status: 201,
      code: "invalid_api_response",
      requestId: "write-1",
    });
  },
);
it.each([200, 503])(
  "fences a failed streamed decode after identity invalidation, status %s",
  async (status) => {
    let controller!: ReadableStreamDefaultController<Uint8Array>;
    const response = new Response(
      new ReadableStream<Uint8Array>({
        start(c) {
          controller = c;
        },
      }),
      { status },
    );
    const client = clientWith(response);
    const settled = deferred<void>();
    const read = vi.spyOn(response, "text");
    // Hold the read rather than relying on transport timing.
    const body = deferred<string>();
    read.mockImplementation(async () => {
      settled.resolve();
      return body.promise;
    });
    const pending = client.api.GET("/version");
    const assertion = expect(pending).rejects.toMatchObject({
      name: "AbortError",
    });
    await settled.promise;
    client.invalidate();
    body.reject(new Error("private body"));
    await assertion;
    controller.close();
  },
);
it("retains response-local retry advice when error-body reading fails", async () => {
  const response = new Response("", {
    status: 503,
    headers: { "Retry-After": "60", "X-Request-ID": "busy-1" },
  });
  vi.spyOn(response, "text").mockRejectedValue(new Error("secret sentinel"));
  const client = clientWith(response);
  await expect(client.api.GET("/version")).rejects.toMatchObject({
    status: 503,
    requestId: "busy-1",
    retryAt: expect.any(Number) as unknown,
  });
});
it("keeps concurrent decoding outcomes bound to their own response", async () => {
  const received = Date.UTC(2026, 8, 28, 12);
  vi.spyOn(Date, "now").mockReturnValue(received);
  const client = clientWith(new Response());
  const bodies = [deferred<string>(), deferred<string>(), deferred<string>()];
  const started = [deferred<void>(), deferred<void>(), deferred<void>()];
  const replies = [
    new Response("", {
      status: 201,
      headers: { "X-Request-ID": "write-1" },
    }),
    new Response("", {
      status: 503,
      headers: { "X-Request-ID": "busy-2", "Retry-After": "60" },
    }),
    new Response("", {
      headers: { "X-Request-ID": "read-3" },
    }),
  ];
  const reads = replies.map((response, index) =>
    vi.spyOn(response, "text").mockImplementation(() => {
      started[index]!.resolve();
      return bodies[index]!.promise;
    }),
  );
  const transport = vi
    .fn()
    .mockResolvedValueOnce(replies[0])
    .mockResolvedValueOnce(replies[1])
    .mockResolvedValueOnce(replies[2]);
  vi.stubGlobal("fetch", transport);
  const write = client.api.POST("/api/v1/namespaces", {
    params: { header: { "Idempotency-Key": "stable" } },
    body: { displayName: "Tools", slug: "tools" },
  });
  const busy = client.api.GET("/version");
  const success = client.api.GET("/api/v1/namespaces");
  const writeCheck = expect(write).rejects.toMatchObject({
    message: "The request could not be completed. Please try again.",
    status: 201,
    code: "invalid_api_response",
    requestId: "write-1",
    retryAt: undefined,
  });
  const busyCheck = expect(busy).rejects.toMatchObject({
    status: 503,
    code: "invalid_api_response",
    requestId: "busy-2",
    retryAt: received + 60000,
  });
  await Promise.all(started.map((read) => read.promise));
  // Finish out of order, mixing successful decoding, a syntax error and a
  // rejected stream. Neither another request nor delayed parsing owns advice.
  bodies[2]!.resolve('{"items":[],"nextCursor":"opaque"}');
  expect(result(await success, page(text))).toEqual({
    items: [],
    nextCursor: "opaque",
  });
  vi.spyOn(Date, "now").mockReturnValue(received + 30000);
  bodies[0]!.resolve('{"private":"syntax sentinel"');
  await writeCheck;
  bodies[1]!.reject(new Error("private stream sentinel"));
  await busyCheck;
  for (const read of reads) expect(read).toHaveBeenCalledTimes(1);
  expect(transport).toHaveBeenCalledTimes(3);
});
it.each([
  [400, false],
  [401, false],
  [403, false],
  [409, false],
  [412, false],
  [429, true],
  [503, true],
] as const)(
  "preserves write recovery classification when status %s has malformed error JSON",
  async (status, isUncertain) => {
    const client = clientWith(
      new Response('{"private":"syntax sentinel"', {
        status,
        headers: { "X-Request-ID": "failure-1" },
      }),
    );
    const error: unknown = await client.api
      .POST("/api/v1/namespaces", {
        params: { header: { "Idempotency-Key": "stable" } },
        body: { displayName: "Tools", slug: "tools" },
      })
      .then((reply) => result(reply, object))
      .catch((error: unknown) => error);
    expect(error).toBeInstanceOf(APIError);
    expect(error).toMatchObject({
      status,
      code: "request_failed",
      requestId: "failure-1",
    });
    expect(uncertain(error)).toBe(isUncertain);
    expect(String(error)).not.toContain("sentinel");
  },
);
it("sanitizes current transport failures and current/stale reference decoding failures", async () => {
  const client = clientWith(new Response("not JSON"));
  await expect(
    client.reference(new AbortController().signal),
  ).rejects.toMatchObject({
    code: "catalog_contract_unavailable",
    status: 200,
  });
  vi.stubGlobal(
    "fetch",
    vi.fn().mockRejectedValue(new Error("secret sentinel")),
  );
  await expect(client.api.GET("/version")).rejects.toMatchObject({
    message: "The request could not be completed. Please try again.",
    status: 0,
  });
  const response = new Response("{}");
  const body = deferred<unknown>();
  const started = deferred<void>();
  vi.spyOn(response, "json").mockImplementation(() => {
    started.resolve();
    return body.promise;
  });
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response));
  const pending = client.reference(new AbortController().signal);
  const assertion = expect(pending).rejects.toMatchObject({
    name: "AbortError",
  });
  await started.promise;
  client.invalidate();
  body.reject(new Error("secret sentinel"));
  await assertion;
});
it("keeps raw parseAs bytes, blobs and text outside JSON checks", async () => {
  const bytes = new Uint8Array([0, 255, 128, 42]);
  for (const parseAs of ["arrayBuffer", "blob", "text"] as const) {
    const client = clientWith(new Response(bytes));
    const reply = await client.api.GET(
      "/api/v1/artifacts/{artifactId}/content",
      { params: { path: { artifactId: "digest" } }, parseAs },
    );
    const data = result(reply);
    if (data instanceof ArrayBuffer)
      expect(new Uint8Array(data)).toEqual(bytes);
    else if (data instanceof Blob)
      expect(new Uint8Array(await data.arrayBuffer())).toEqual(bytes);
    else expect(data).toBe(new TextDecoder().decode(bytes));
  }
});
it("requires exact bodyless acknowledgements and keeps status/lifetime checks", async () => {
  acknowledge({ response: new Response(null, { status: 204 }) });
  for (const status of [200, 202, 403])
    expect(() =>
      acknowledge({ response: new Response(null, { status }) }),
    ).toThrow(APIError);
  const client = clientWith(new Response(null, { status: 204 }));
  const reply = await client.api.DELETE("/api/v1/webhooks/{webhookId}", {
    params: {
      path: { webhookId: "hook" },
      header: { "Idempotency-Key": "key", "If-Match": "etag" },
    },
  });
  client.invalidate();
  expect(() => acknowledge(reply)).toThrowError("Session changed");
});
it("checks successful objects without dropping unknown evidence", () => {
  const data = { extension: { private: "exact" } };
  expect(result({ data, response: new Response() }, object)).toBe(data);
});
