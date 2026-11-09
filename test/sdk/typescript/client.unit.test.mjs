import assert from "node:assert/strict";
import { test, mock } from "node:test";
import { pathToFileURL } from "node:url";
import { readFile } from "node:fs/promises";
import ts from "../../../sdk/typescript/node_modules/typescript/lib/typescript.js";
const { createCatalogClient, SDKError } = await import(
  pathToFileURL(process.env.CATALOG_SDK_ENTRY)
);
const origin = "https://catalog.example.com";
const make = (fetcher, extra = {}) =>
  createCatalogClient({ origin, ...extra }, fetcher);
const json = (value, init) => new Response(JSON.stringify(value), init);
const code = (name) => (error) =>
  error instanceof SDKError && error.code === name;
const manifest = "/.well-known/ard.json";

test("finite conditional version and delivery envelopes preserve their actual wire meaning", async () => {
  const uid = "00000000-0000-7000-8000-000000000001";
  for (let seed = 0; seed < 16; seed++) {
    const request = {
      sourceRevision: seed + 1,
      identifier: `urn:air:example.com:wire:item-${seed}`,
      versionPresent: seed % 2 === 0,
      mediaType: "application/json",
      delivery: ["data", "url"][seed % 2],
      exactEntryDigest: "sha256:" + "a".repeat(64),
      reason: "Check generated wire semantics",
      ...(seed % 2 === 0
        ? { version: ["", "1.2.3"][Math.floor(seed / 2) % 2] }
        : {}),
      ...(seed % 3 === 0
        ? { observationId: uid }
        : { text: `Entry ${seed} é 日本語` }),
    };
    const representation = { role: "primary", mediaType: "application/json" };
    if (seed % 4 === 0)
      representation.data = { nested: [null, false, "", seed] };
    else if (seed % 4 === 1) representation.content = `Native é ${seed}\n`;
    else if (seed % 4 === 2)
      representation.url = `https://resource.example/card-${seed}.json`;
    else
      Object.assign(representation, {
        mediaType: "application/agent-skills+zip",
        packageData: Buffer.from([0, 1, seed, 255]).toString("base64"),
        handoff: {
          git: {
            repository: "https://example.com/skill.git",
            commit: "a".repeat(40),
            directory: "skill",
          },
        },
      });
    const body = { version: `1.${seed}.0`, representations: [representation] };
    const expected = [request, body];
    const paths = [
      `/api/v1/federation/sources/${uid}/trust-assessments`,
      `/api/v1/resources/${uid}/versions`,
    ];
    let calls = 0;
    const client = make(async (actual) => {
      assert.equal(new URL(actual.url).origin, origin);
      assert.equal(new URL(actual.url).pathname, paths[calls]);
      assert.equal(actual.method, "POST");
      assert.equal(
        actual.headers.get("Idempotency-Key"),
        `conditional-${seed}-${calls}`,
      );
      assert.deepEqual(JSON.parse(await actual.text()), expected[calls]);
      calls++;
      return json({}, { status: 201 });
    });
    await client.api.POST(
      "/api/v1/federation/sources/{sourceId}/trust-assessments",
      {
        params: {
          path: { sourceId: uid },
          header: {
            "Idempotency-Key": `conditional-${seed}-0`,
            "If-Match": '"rev-1"',
          },
        },
        body: request,
      },
    );
    assert.equal(calls, 1);
    await client.api.POST("/api/v1/resources/{resourceId}/versions", {
      params: {
        path: { resourceId: uid },
        header: { "Idempotency-Key": `conditional-${seed}-1` },
      },
      body,
    });
    assert.equal(calls, 2);
  }
});

test("finite generated wire cases preserve decoded query/body meaning and one fixed-origin call", async () => {
  const words = [
    "",
    "é",
    "日本語",
    "😀",
    "%+&=",
    "line\nvalue",
    'quoted"value',
    "two words",
  ];
  const uid = "00000000-0000-7000-8000-000000000001";
  for (let seed = 0; seed < 32; seed++) {
    const label = `case-${seed}-${words[seed % words.length]}`;
    const selected = seed % 3 === 0 ? undefined : seed % 3 === 1 ? "" : label;
    const extensions = {
      additional_properties: { literal: words[seed % words.length] },
      model_extra: null,
      values: [null, false, "", label],
      count: seed * 65537,
    };
    const expected = {
      versionRevisionId: uid,
      publisherId: uid,
      displayName: label,
      description: "Wire corpus",
      visibility: "tenant",
      extensions,
    };
    const calls = [];
    const client = make(async (request) => {
      calls.push(request);
      assert.equal(new URL(request.url).origin, origin);
      if (request.method === "GET") {
        assert.equal(new URL(request.url).pathname, "/api/v1/personal/items");
        assert.deepEqual(
          [...new URL(request.url).searchParams],
          selected === undefined ? [] : [["label", selected]],
        );
        return json({ items: [] });
      }
      assert.equal(request.method, "POST");
      assert.equal(
        new URL(request.url).pathname,
        `/api/v1/catalog-listings/${uid}/revisions`,
      );
      assert.deepEqual(JSON.parse(await request.text()), expected);
      return json({}, { status: 201 });
    });
    await client.api.GET("/api/v1/personal/items", {
      params: { query: selected === undefined ? {} : { label: selected } },
    });
    assert.equal(calls.length, 1);
    await client.api.POST("/api/v1/catalog-listings/{listingId}/revisions", {
      params: {
        path: { listingId: uid },
        header: { "Idempotency-Key": `wire-${seed}`, "If-Match": '"rev-1"' },
      },
      body: expected,
    });
    assert.equal(calls.length, 2);
    const circular = {};
    circular.self = circular;
    await assert.rejects(() =>
      client.api.POST("/api/v1/catalog-listings/{listingId}/revisions", {
        params: {
          path: { listingId: uid },
          header: {
            "Idempotency-Key": `wire-refusal-${seed}`,
            "If-Match": '"rev-1"',
          },
        },
        body: { ...expected, extensions: circular },
      }),
    );
    assert.equal(calls.length, 2, "local JSON marshal refusal must not send");
  }
});

test("personal label query preserves exact spelling and optional presence", async () => {
  const requests = [];
  const client = make(async (request) => {
    requests.push(new URL(request.url));
    return json(
      { items: [] },
      { headers: { "Content-Type": "application/json" } },
    );
  });
  for (const label of [undefined, " Case é%_ ", ""]) {
    await client.api.GET("/api/v1/personal/items", {
      params: { query: { ...(label === undefined ? {} : { label }) } },
    });
    const query = requests.at(-1).searchParams;
    assert.equal(query.has("label"), label !== undefined);
    if (label !== undefined) assert.equal(query.get("label"), label);
  }
  assert.equal(requests.length, 3);
});

test("exact directory filters preserve spelling and omit unused criteria", async () => {
  const requests = [];
  const client = make(async (request) => {
    requests.push(new URL(request.url));
    return json(
      { items: [] },
      { headers: { "Content-Type": "application/json" } },
    );
  });
  await client.api.GET("/api/v1/namespaces", {
    params: {
      query: { slug: " Tools--1 ", displayName: "Mixed 日本語", limit: 1 },
    },
  });
  await client.api.GET("/api/v1/publishers", {
    params: { query: { declaredDomain: " DECLARED.Example. " } },
  });
  assert.deepEqual(
    [...requests[0].searchParams],
    [
      ["slug", " Tools--1 "],
      ["displayName", "Mixed 日本語"],
      ["limit", "1"],
    ],
  );
  assert.deepEqual(
    [...requests[1].searchParams],
    [["declaredDomain", " DECLARED.Example. "]],
  );
});

const readme = await readFile(
  new URL("../../../sdk/typescript/README.md", import.meta.url),
  "utf8",
);
const documented = new Map(
  [
    ...readme.matchAll(
      /<!-- sdk-example: ([a-z-]+) -->\s+```ts\n([\s\S]*?)\n```/gu,
    ),
  ].map((match) => [match[1], match[2]]),
);
async function readmeExample(name) {
  assert.ok(documented.has(name), `Missing README example: ${name}`);
  const emitted = ts.transpileModule(documented.get(name), {
    compilerOptions: {
      module: ts.ModuleKind.ES2022,
      target: ts.ScriptTarget.ES2022,
    },
  }).outputText;
  return import("data:text/javascript," + encodeURIComponent(emitted));
}

test("README Search helper bounds traversal and retains continuation and partial metadata", async () => {
  const { searchLocalPages } = await readmeExample("search-local-pages");
  const signal = new AbortController().signal;
  const requests = [];
  const pages = ["second", "third", "remaining", undefined].map(
    (nextCursor, index) => ({
      items: [],
      durableTotal: 0,
      liveCount: 0,
      profile: "core-lexical-v1",
      generation: "sha256:" + "a".repeat(64),
      tier: "core",
      partial: index === 1,
      ...(index === 1 ? { diagnostics: [{ status: "failed" }] } : {}),
      ...(nextCursor ? { nextCursor } : {}),
    }),
  );
  const client = make(async (request) => {
    requests.push(JSON.parse(await request.text()));
    return json(pages[requests.length - 1]);
  });
  const result = await searchLocalPages(client, signal);
  assert.deepEqual(result.pages, pages.slice(0, 3));
  assert.equal(result.nextCursor, "remaining");
  assert.deepEqual(
    requests,
    [undefined, "second", "third"].map((cursor) => ({
      text: "weather",
      origins: ["local"],
      limit: 10,
      ...(cursor ? { cursor } : {}),
    })),
  );
  const continued = await searchLocalPages(client, signal, result.nextCursor);
  assert.deepEqual(continued.pages, pages.slice(3));
  assert.equal(continued.nextCursor, undefined);
  assert.equal(requests[3].cursor, "remaining");
  const denied = make(async () => json({ code: "FORBIDDEN" }, { status: 403 }));
  await assert.rejects(searchLocalPages(denied, signal), /HTTP 403/u);
});

test("README local detail uses the release UUID and refuses external results before transport", async () => {
  const { readLocalResult } = await readmeExample("read-local-result");
  const release = "00000000-0000-7000-8000-000000000001";
  let calls = 0;
  const client = make(async (request) => {
    calls++;
    assert.equal(
      new URL(request.url).pathname,
      "/api/v1/catalog/resources/" + release,
    );
    return json({ release: { id: release } });
  });
  const result = await readLocalResult(client, {
    id: "local:search-result-key",
    type: "local",
    local: { release: { id: release } },
  });
  assert.equal(result.release.id, release);
  for (const item of [{ type: "indexed" }, { type: "live" }, { type: "local" }])
    await assert.rejects(
      readLocalResult(client, item),
      /Select a local result/u,
    );
  assert.equal(calls, 1);
  const hidden = make(async () => json({ code: "NOT_FOUND" }, { status: 404 }));
  await assert.rejects(
    readLocalResult(hidden, {
      type: "local",
      local: { release: { id: release } },
    }),
    /HTTP 404/u,
  );
});

test("README native helpers preserve envelope bytes and bind a deliberate command", async () => {
  const { createVersionFromBytes } = await readmeExample(
    "create-version-bytes",
  );
  const { readRevisionBytes } = await readmeExample("read-revision-bytes");
  const bytes = new TextEncoder().encode(
    '{"version":"1.0.0","representations":[{"role":"primary","mediaType":"application/json","data":{"large":9007199254740993}}]}',
  );
  const paths = [];
  const client = make(async (request) => {
    paths.push(new URL(request.url).pathname);
    if (request.method === "POST") {
      assert.equal(request.headers.get("Idempotency-Key"), "retained-key");
      assert.equal(request.headers.get("Content-Type"), "application/json");
      assert.deepEqual(new Uint8Array(await request.arrayBuffer()), bytes);
      return new Response('{"revision":{"id":"revision-id"}}', {
        status: 201,
        headers: { ETag: '"created"' },
      });
    }
    return new Response(bytes);
  });
  const created = await createVersionFromBytes(
    client,
    "resource-id",
    bytes,
    "retained-key",
  );
  assert.equal(created.headers.get("ETag"), '"created"');
  assert.deepEqual(await readRevisionBytes(client, "revision-id"), bytes);
  assert.deepEqual(paths, [
    "/api/v1/resources/resource-id/versions",
    "/api/v1/version-revisions/revision-id",
  ]);
  const refused = make(async () =>
    json({ code: "FORBIDDEN" }, { status: 403 }),
  );
  await assert.rejects(
    createVersionFromBytes(refused, "resource-id", bytes, "same-key"),
    /HTTP 403/u,
  );
  await assert.rejects(readRevisionBytes(refused, "revision-id"), /HTTP 403/u);
});

test("README command keeps original ETag and key and returns the new validator", async () => {
  const { updateNamespace } = await readmeExample("update-namespace");
  const requests = [];
  const client = make(async (request) => {
    requests.push({
      method: request.method,
      key: request.headers.get("Idempotency-Key"),
      etag: request.headers.get("If-Match"),
      body: await request.text(),
    });
    return json(
      { id: "namespace-id", displayName: "Examples" },
      { headers: { ETag: '"next"' } },
    );
  });
  const result = await updateNamespace(
    client,
    "namespace-id",
    '"original"',
    "original-key",
  );
  assert.equal(result.response.headers.get("ETag"), '"next"');
  assert.deepEqual(requests, [
    {
      method: "PATCH",
      key: "original-key",
      etag: '"original"',
      body: '{"displayName":"Examples","reason":"Update namespace name"}',
    },
  ]);
  let refusals = 0;
  const stale = make(async () => {
    refusals++;
    return json({ code: "PRECONDITION_FAILED" }, { status: 412 });
  });
  await assert.rejects(
    updateNamespace(stale, "namespace-id", '"stale"', "new-key"),
    /HTTP 412/u,
  );
  assert.equal(refusals, 1);
});

test("README cancellable read uses its supplied signal without a live request", async () => {
  const { readCapabilities } = await readmeExample("cancellable-read");
  const controller = new AbortController();
  controller.abort();
  const client = make(() => assert.fail("Cancelled before transport"));
  await assert.rejects(
    readCapabilities(client, controller.signal),
    code("cancelled"),
  );
  assert.deepEqual(
    await readCapabilities(
      make(async () => json({ service: "catalog" })),
      new AbortController().signal,
    ),
    { service: "catalog" },
  );
  await assert.rejects(
    readCapabilities(
      make(async () => json({ code: "UNAUTHENTICATED" }, { status: 401 })),
      new AbortController().signal,
    ),
    /HTTP 401/u,
  );
});

test("configuration rejects unsafe origins and invalid finite budgets", () => {
  for (const setting of [
    { origin: "http://localhost:8080", allowLoopbackHTTP: true },
    { origin: "https://user:secret@catalog.example.com" },
    { origin: origin + "/api" },
    { origin: "https://catalog.example.com:0" },
    { origin: origin + "?" },
    { origin: origin + "#" },
    { origin: origin + "\\path" },
    { token: "SECRET\n" },
    { token: "a", tokenProvider: () => "b" },
    { timeoutMs: Infinity },
    { timeoutMs: 0 },
    { timeoutMs: true },
    { requestBytes: 0 },
    { responseBytes: 1.5 },
    { headerBytes: 2 ** 40 },
  ]) {
    assert.throws(
      () => make(() => assert.fail("no traffic"), setting),
      code("invalid_configuration"),
    );
  }
  assert.ok(
    createCatalogClient({
      origin: "http://127.0.0.1:8080",
      allowLoopbackHTTP: true,
    }),
  );
  assert.ok(
    createCatalogClient({
      origin: "http://[::1]:8080",
      allowLoopbackHTTP: true,
    }),
  );
  // Plain JavaScript callers can pass truthy values despite the TypeScript type.
  // Only the literal boolean permits cleartext loopback credentials.
  for (const host of ["127.0.0.1", "[::1]"])
    for (const allowLoopbackHTTP of [undefined, false, "false", "true", 1])
      assert.throws(
        () =>
          createCatalogClient(
            {
              origin: `http://${host}:8080`,
              allowLoopbackHTTP,
              tokenProvider: () => assert.fail("no credentials"),
            },
            () => assert.fail("no traffic"),
          ),
        code("invalid_configuration"),
      );
});

test("origin and override refusal occurs before provider or transport", async () => {
  let calls = 0,
    providers = 0;
  const client = make(
    async () => {
      calls++;
      return json({});
    },
    {
      tokenProvider: () => {
        providers++;
        return "secret";
      },
    },
  );
  for (const path of [
    "https://outside.example.com",
    "//outside.example.com",
    "/x#fragment",
    "/x\\bad",
  ]) {
    await assert.rejects(client.raw(path), code("invalid_request"));
  }
  for (const key of [
    "baseUrl",
    "fetch",
    "middleware",
    "Request",
    "bodySerializer",
    "pathSerializer",
    "querySerializer",
    "redirect",
    "credentials",
  ]) {
    await assert.rejects(
      client.api.GET(manifest, { [key]: "unsafe" }),
      code("invalid_request"),
    );
    await assert.rejects(
      client.raw("/x", { [key]: "unsafe" }),
      code("invalid_request"),
    );
  }
  await assert.rejects(
    client.api.GET(manifest, { parseAs: "unsafe" }),
    code("invalid_request"),
  );
  for (const bad of [null, [], 42]) {
    await assert.rejects(client.raw("/x", bad), code("invalid_request"));
    await assert.rejects(
      client.api.GET(manifest, bad),
      code("invalid_request"),
    );
  }
  assert.equal(calls, 0);
  assert.equal(providers, 0);
});

test("credentials, exact bytes, command bindings and response metadata", async () => {
  const original = '{"n":9007199254740993,"unknown":[null,false,""]}';
  let calls = 0;
  const client = make(
    async (request) => {
      calls++;
      assert.equal(request.url, origin + "/api/v1/resources");
      assert.equal(await request.text(), original);
      assert.equal(request.headers.get("Authorization"), "Bearer configured");
      for (const h of [
        "cookie",
        "proxy-authorization",
        "host",
        "transfer-encoding",
      ])
        assert.equal(request.headers.get(h), null);
      assert.equal(request.headers.get("Accept-Encoding"), "identity");
      assert.equal(request.headers.get("Idempotency-Key"), "retained-key");
      assert.equal(request.headers.get("If-Match"), '"r1"');
      assert.equal(request.redirect, "manual");
      assert.equal(request.credentials, "omit");
      return new Response(original, {
        status: 202,
        headers: {
          ETag: '"r2"',
          Location: "/api/v1/jobs/id",
          "Retry-After": "2",
          "X-Request-ID": "request",
          "Idempotency-Replayed": "true",
        },
      });
    },
    { token: "configured" },
  );
  const result = await client.raw("/api/v1/resources", {
    method: "POST",
    body: new TextEncoder().encode(original),
    headers: {
      Authorization: "Bearer other",
      Cookie: "session=secret",
      "Proxy-Authorization": "secret",
      Host: "evil",
      "Idempotency-Key": "retained-key",
      "If-Match": '"r1"',
    },
  });
  assert.equal(result.status, 202);
  assert.equal(result.text(), original);
  for (const [name, value] of Object.entries({
    etag: '"r2"',
    location: "/api/v1/jobs/id",
    "retry-after": "2",
    "x-request-id": "request",
    "idempotency-replayed": "true",
  }))
    assert.equal(result.headers.get(name), value);
  assert.equal(calls, 1);
});

test("request size and serialization failures send nothing", async () => {
  let calls = 0,
    providers = 0;
  const client = make(
    async () => {
      calls++;
      return json({});
    },
    {
      requestBytes: 2,
      tokenProvider: () => {
        providers++;
        return "t";
      },
    },
  );
  await assert.rejects(
    client.raw("/x", { method: "POST", body: "€" }),
    code("request_too_large"),
  );
  await assert.rejects(
    client.api.POST("/api/v1/namespaces", { body: { value: 1n } }),
    code("invalid_request"),
  );
  await assert.rejects(
    client.raw("/x", { method: "POST", body: {} }),
    code("invalid_request"),
  );
  assert.equal(calls, 0);
  assert.equal(providers, 0);
  assert.equal(
    (await client.raw("/x", { method: "POST", body: "{}" })).status,
    200,
  );
  assert.equal(calls, 1);
  assert.equal(providers, 1);
});

test("stream bounds cancel unknown-length body before parsing", async () => {
  let cancelled = 0;
  const client = make(
    async () =>
      new Response(
        new ReadableStream({
          start(c) {
            c.enqueue(new Uint8Array([1, 2]));
            c.enqueue(new Uint8Array([3]));
          },
          cancel() {
            cancelled++;
          },
        }),
      ),
    { responseBytes: 2 },
  );
  await assert.rejects(client.api.GET(manifest), code("response_too_large"));
  assert.equal(cancelled, 1);
  const exact = make(async () => new Response("ab"), { responseBytes: 2 });
  assert.equal((await exact.raw("/x")).text(), "ab");
  await assert.rejects(
    exact.raw("/x", { responseBytes: 1 }),
    code("response_too_large"),
  );
  assert.equal((await exact.raw("/x", { responseBytes: 4 })).bytes.length, 2);
});

test("declared length, encoding and parsed header bounds cancel bodies", async () => {
  for (const [headers, extra, expected] of [
    [{ "content-length": "3" }, { responseBytes: 2 }, "response_too_large"],
    [{ "content-encoding": "gzip" }, {}, "unexpected_encoding"],
    [
      { "x-large": "secret".repeat(20) },
      { headerBytes: 8 },
      "headers_too_large",
    ],
  ]) {
    let cancelled = false;
    const client = make(
      async () =>
        new Response(
          new ReadableStream({
            cancel() {
              cancelled = true;
            },
          }),
          { headers },
        ),
      extra,
    );
    await assert.rejects(client.raw("/x"), code(expected));
    assert.ok(cancelled);
  }
});

test("Control and ARD errors, partial pages and bodyless statuses remain distinct", async () => {
  let calls = 0;
  const partial = {
    items: [],
    durableTotal: 0,
    liveCount: 0,
    profile: "core-lexical-v1",
    generation: "sha256:" + "a".repeat(64),
    tier: "core",
    partial: true,
    diagnostics: [
      {
        sourceId: "00000000-0000-7000-8000-000000000001",
        status: "failed",
        message: "Source unavailable",
      },
    ],
    nextCursor: "opaque",
  };
  const client = make(async (request) => {
    calls++;
    const data = JSON.parse(await request.text());
    assert.equal(data.cursor, "same");
    assert.deepEqual(data.kinds, ["agent"]);
    return json(partial);
  });
  const result = await client.api.POST("/api/v1/catalog/search", {
    body: { cursor: "same", kinds: ["agent"], limit: 1 },
  });
  assert.deepEqual(result.data, partial);
  assert.equal(calls, 1);
  assert.deepEqual(
    JSON.parse(new TextDecoder().decode(client.rawBytes(result.response))),
    partial,
  );
  for (const error of [
    { code: "FORBIDDEN", title: "Denied", status: 403 },
    { errorCode: "INVALID_CURSOR", message: "Denied" },
  ]) {
    const reply = await make(async () => json(error, { status: 403 })).api.GET(
      manifest,
    );
    assert.deepEqual(reply.error, error);
    assert.equal(reply.data, undefined);
  }
  for (const status of [204, 304]) {
    const reply = await make(
      async () =>
        new Response(null, { status, headers: { ETag: '"unchanged"' } }),
    ).api.GET(manifest);
    assert.equal(reply.response.status, status);
    assert.equal(reply.data, undefined);
    assert.equal(reply.response.headers.get("etag"), '"unchanged"');
  }
});

test("HTTP errors preserve retry hints without another request", async () => {
  for (const [path, status, error, mediaType] of [
    [
      "/api/v1/me",
      503,
      {
        type: "about:blank",
        title: "Service Unavailable",
        status: 503,
        detail: "Dependency unavailable",
        code: "DEPENDENCY_UNAVAILABLE",
        requestId: "http-error",
      },
      "application/problem+json",
    ],
    [
      manifest,
      429,
      { errorCode: "TOO_MANY_REQUESTS", message: "Try later" },
      "application/json",
    ],
  ]) {
    let calls = 0;
    const client = make(async (request) => {
      calls++;
      assert.equal(new URL(request.url).pathname, path);
      return json(error, {
        status,
        headers: {
          "Content-Type": mediaType,
          "Retry-After": "2",
          "X-Request-ID": "http-error",
        },
      });
    });
    const reply = await client.api.GET(path);
    assert.equal(reply.response.status, status);
    assert.equal(reply.response.headers.get("Retry-After"), "2");
    assert.equal(reply.response.headers.get("X-Request-ID"), "http-error");
    assert.deepEqual(reply.error, error);
    assert.equal(reply.data, undefined);
    assert.deepEqual(
      JSON.parse(new TextDecoder().decode(client.rawBytes(reply.response))),
      error,
    );
    assert.equal(calls, 1);
  }
});

test("malformed and null JSON produce safe errors; raw bytes remain available", async () => {
  for (const [status, body] of [200, 503].flatMap((status) =>
    ["{secret", "null", ""].map((body) => [status, body]),
  )) {
    const client = make(
      async () =>
        new Response(body, { status, headers: { "X-Request-ID": "id" } }),
    );
    const modes =
      status === 503
        ? [undefined, "json", "text", "arrayBuffer", "blob", "stream"]
        : [undefined, "json"];
    for (const parseAs of modes)
      await assert.rejects(
        client.api.GET(manifest, parseAs ? { parseAs } : {}),
        (e) =>
          code("invalid_response")(e) &&
          e.status === status &&
          e.requestID === "id" &&
          !String(e).includes("secret"),
        `${status} ${parseAs ?? "default"} ${JSON.stringify(body)}`,
      );
    assert.equal((await client.raw("/x")).text(), body);
  }
  await assert.rejects(
    make(async () => {
      throw new Error("SECRET_PROVIDER");
    }).raw("/x"),
    (e) => code("request_failed")(e) && !String(e).includes("SECRET_PROVIDER"),
  );
  const foreign = make(async () => json({}));
  assert.throws(
    () => foreign.rawBytes(new Response()),
    code("invalid_response"),
  );
});

test("cancelled body and cancelled provider close work without another request", async () => {
  let started;
  const ready = new Promise((r) => {
    started = r;
  });
  let cancelled = false,
    calls = 0;
  const controller = new AbortController();
  const client = make(async () => {
    calls++;
    return new Response(
      new ReadableStream(
        {
          pull() {
            started();
          },
          cancel() {
            cancelled = true;
          },
        },
        { highWaterMark: 0 },
      ),
    );
  });
  const pending = client.raw("/x", { signal: controller.signal });
  await ready;
  controller.abort();
  await assert.rejects(pending, code("cancelled"));
  assert.ok(cancelled);
  assert.equal(calls, 1);
  const stopped = new AbortController();
  stopped.abort();
  await assert.rejects(
    client.raw("/x", { signal: stopped.signal }),
    code("cancelled"),
  );
  assert.equal(calls, 1);
  let providerReady;
  const providerStarted = new Promise((r) => {
    providerReady = r;
  });
  const signal = new AbortController();
  const withProvider = make(
    async () => {
      calls++;
      return json({});
    },
    {
      tokenProvider: async (s) => {
        providerReady();
        await new Promise((r) =>
          s.addEventListener("abort", r, { once: true }),
        );
        return "later";
      },
    },
  );
  const waiting = withProvider.raw("/x", { signal: signal.signal });
  await providerStarted;
  signal.abort();
  await assert.rejects(waiting, code("cancelled"));
  assert.equal(calls, 1);
});

test("concurrent providers and response overrides stay request-local", async () => {
  let n = 0;
  const seen = [];
  const client = make(
    async (request) => {
      seen.push(request.headers.get("authorization"));
      return new Response("abcd");
    },
    { tokenProvider: () => String(++n), responseBytes: 2 },
  );
  const results = await Promise.allSettled([
    client.raw("/x", { responseBytes: 4 }),
    client.raw("/x"),
  ]);
  assert.equal(results[0].status, "fulfilled");
  assert.equal(results[1].reason.code, "response_too_large");
  assert.deepEqual(seen.sort(), ["Bearer 1", "Bearer 2"]);
});

test("strict raw text, anonymous credentials and bounded in-memory parse modes", async () => {
  const c = make(async (request) => {
    assert.equal(request.headers.get("authorization"), null);
    return new Response(new Uint8Array([255]));
  });
  const r = await c.raw("/x");
  assert.deepEqual(r.bytes, new Uint8Array([255]));
  assert.throws(() => r.text(), code("invalid_response"));
  for (const mode of ["text", "arrayBuffer", "blob", "stream"]) {
    const reply = await make(async () => new Response("bytes")).api.GET(
      manifest,
      { parseAs: mode },
    );
    const value =
      mode === "stream"
        ? await new Response(reply.data).text()
        : mode === "arrayBuffer"
          ? new TextDecoder().decode(reply.data)
          : mode === "blob"
            ? await reply.data.text()
            : reply.data;
    assert.equal(value, "bytes");
  }
});

test("invalid provider credentials fail before transport and later calls recover", async () => {
  let credential,
    calls = 0;
  const client = make(
    async (request) => {
      calls++;
      assert.equal(
        request.headers.get("Authorization"),
        credential ? `Bearer ${credential}` : null,
      );
      return json({ entries: [] });
    },
    { tokenProvider: () => credential },
  );
  for (const invalid of [
    "SECRET\n",
    "SECRET value",
    "\u00e9",
    42,
    null,
    false,
    {},
  ]) {
    credential = invalid;
    const before = calls;
    await assert.rejects(
      client.api.GET(manifest),
      (e) =>
        code("invalid_request")(e) &&
        e.status === undefined &&
        e.cause === undefined &&
        !String(e).includes("SECRET"),
    );
    assert.equal(calls, before);
    credential = "valid-token";
    assert.deepEqual((await client.api.GET(manifest)).data, { entries: [] });
    assert.equal(calls, before + 1);
  }
  for (credential of [undefined, ""]) {
    assert.deepEqual((await client.api.GET(manifest)).data, { entries: [] });
  }
  assert.equal(calls, 9);
});

test("provider failures are safe and elapsed provider time cannot trigger a late send", async () => {
  let now = 0,
    calls = 0;
  const clock = mock.method(performance, "now", () => now);
  try {
    const late = make(
      async () => {
        calls++;
        return json({});
      },
      {
        timeoutMs: 100,
        tokenProvider: () => {
          now = 101;
          return "SECRET";
        },
      },
    );
    await assert.rejects(late.raw("/x"), code("deadline_exceeded"));
    assert.equal(calls, 0);
    now = 0;
    const failing = make(
      async () => {
        calls++;
        return json({});
      },
      {
        tokenProvider: () => {
          throw new Error("SECRET_PROVIDER");
        },
      },
    );
    await assert.rejects(
      failing.raw("/x"),
      (e) =>
        code("request_failed")(e) && !String(e).includes("SECRET_PROVIDER"),
    );
    assert.equal(calls, 0);
  } finally {
    clock.mock.restore();
  }
});

test("late headers and body completion cannot bypass the total deadline", async () => {
  let now = 0;
  const clock = mock.method(performance, "now", () => now);
  try {
    for (const lateStage of ["headers", "body"]) {
      now = 0;
      const duration = lateStage === "headers" ? 100 : 50;
      let cancelled = 0,
        reads = 0;
      const client = make(
        async () => {
          if (lateStage === "headers") now = duration + 1;
          return new Response(
            new ReadableStream(
              {
                pull(controller) {
                  reads++;
                  if (lateStage === "body") now = duration + 1;
                  controller.enqueue(new TextEncoder().encode("{}"));
                  controller.close();
                },
                cancel() {
                  cancelled++;
                },
              },
              { highWaterMark: 0 },
            ),
            { headers: { "X-Request-ID": "late-body" } },
          );
        },
        { timeoutMs: 100 },
      );
      await assert.rejects(
        client.api.GET(manifest, {
          timeoutMs: lateStage === "headers" ? 1000 : 50,
        }),
        (e) =>
          code("deadline_exceeded")(e) &&
          e.status === 200 &&
          e.requestID === "late-body",
        lateStage,
      );
      assert.equal(reads, lateStage === "headers" ? 0 : 1);
      if (lateStage === "headers") assert.equal(cancelled, 1);
    }
    now = 0;
    const onTime = make(
      async () => {
        now = 99;
        return json({ entries: [] });
      },
      { timeoutMs: 100 },
    );
    assert.deepEqual((await onTime.api.GET(manifest)).data, { entries: [] });
  } finally {
    clock.mock.restore();
  }
});

test("body transport failures preserve safe HTTP metadata", async () => {
  const client = make(
    async () =>
      new Response(
        new ReadableStream({
          start(controller) {
            controller.error(new Error("SECRET_STREAM"));
          },
        }),
        { status: 200, headers: { "X-Request-ID": "body-id" } },
      ),
  );
  await assert.rejects(
    client.raw("/x"),
    (e) =>
      e instanceof SDKError &&
      e.code === "request_failed" &&
      e.status === 200 &&
      e.requestID === "body-id" &&
      !String(e).includes("SECRET_STREAM"),
  );
});
