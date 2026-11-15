import assert from "node:assert/strict";
import { test } from "node:test";
import { createServer as httpServer } from "node:http";
import { createServer as httpsServer } from "node:https";
import { execFile, execFileSync } from "node:child_process";
import { promisify } from "node:util";
const execute = promisify(execFile);
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { once } from "node:events";
import { gzipSync } from "node:zlib";
import { pathToFileURL } from "node:url";
const { createCatalogClient } = await import(
  pathToFileURL(process.env.CATALOG_SDK_ENTRY)
);
async function serve(t, handler) {
  const server = httpServer(handler);
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  t.after(() => {
    server.closeAllConnections();
    return new Promise((r) => server.close(r));
  });
  return `http://127.0.0.1:${server.address().port}`;
}
const client = (origin) =>
  createCatalogClient({ origin, allowLoopbackHTTP: true, token: "explicit" });

test("snapshot comparison preserves selected membership, opaque continuation and refusal", async (t) => {
  const before = "00000000-0000-7000-8000-000000000001";
  const after = "00000000-0000-7000-8000-000000000002";
  const page = {
    sourceId: "00000000-0000-7000-8000-000000000003",
    sourceRevision: 7,
    before: {
      id: "00000000-0000-7000-8000-000000000001",
      memberCount: 2,
      startedAt: "2026-01-01T00:00:00Z",
      withdrawalAuthority: true,
    },
    after: {
      id: "00000000-0000-7000-8000-000000000002",
      memberCount: 1,
      startedAt: "2026-01-02T00:00:00Z",
      withdrawalAuthority: false,
    },
    items: [
      {
        position: 1,
        sourceKey: "a-missing-version",
        change: "absent",
        beforeCount: 1,
        afterCount: 0,
        before: {
          observationId: "00000000-0000-7000-8000-000000000004",
          identifier: "urn:air:example.com:demo:agent",
          version: "",
          versionPresent: false,
          kind: "agent",
          identityDigest:
            "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
          occurrence: {
            observationId: "00000000-0000-7000-8000-000000000004",
            sourceId: "00000000-0000-7000-8000-000000000003",
            sourceRevision: 7,
            snapshotId: "00000000-0000-7000-8000-000000000001",
            retrievalDigest:
              "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
            entryLocation: "/entries/0",
            exactEntryDigest:
              "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
            evidenceComplete: true,
            observedAt: "2026-01-01T00:00:00Z",
          },
        },
      },
      {
        position: 2,
        sourceKey: "b-empty-version",
        change: "added",
        beforeCount: 0,
        afterCount: 1,
        after: {
          observationId: "00000000-0000-7000-8000-000000000005",
          identifier: "urn:air:example.com:demo:agent",
          version: "",
          versionPresent: true,
          kind: "agent",
          identityDigest:
            "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
          occurrence: {
            observationId: "00000000-0000-7000-8000-000000000005",
            sourceId: "00000000-0000-7000-8000-000000000003",
            sourceRevision: 7,
            snapshotId: "00000000-0000-7000-8000-000000000002",
            retrievalDigest:
              "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
            entryLocation: "/entries/0",
            exactEntryDigest:
              "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
            evidenceComplete: true,
            observedAt: "2026-01-02T00:00:00Z",
          },
        },
      },
    ],
    nextCursor: "opaque+/=& caf\u00e9",
  };
  const cursor = "opaque+/=& café";
  const last = structuredClone(page.items[0]);
  last.position = 3;
  last.sourceKey = "c-other";
  last.before.observationId = "00000000-0000-7000-8000-000000000006";
  last.before.identifier = "urn:air:example.com:demo:other";
  last.before.identityDigest = "sha256:" + "e".repeat(64);
  last.before.occurrence.observationId = last.before.observationId;
  last.before.occurrence.entryLocation = "/entries/1";
  last.before.occurrence.exactEntryDigest = "sha256:" + "f".repeat(64);
  const terminal = {
    sourceId: page.sourceId,
    sourceRevision: page.sourceRevision,
    before: page.before,
    after: page.after,
    items: [last],
  };
  let calls = 0;
  const origin = await serve(t, (req, res) => {
    calls++;
    const url = new URL(req.url, "http://fixture");
    assert.equal(req.method, "GET");
    assert.equal(
      url.pathname,
      `/api/v1/federation/snapshots/${before}/comparison`,
    );
    assert.deepEqual(Object.fromEntries(url.searchParams), {
      otherSnapshotId: after,
      view: "changes",
      limit: "2",
      ...(calls === 1 ? {} : { cursor }),
    });
    assert.equal(req.headers.authorization, "Bearer explicit");
    assert.equal(req.headers["idempotency-key"], undefined);
    if (calls <= 2) {
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify(calls === 1 ? page : terminal));
    } else {
      res.writeHead(400, {
        "Content-Type": "application/problem+json",
        "X-Request-ID": "comparison-refusal",
      });
      res.end(
        '{"type":"about:blank","title":"Invalid argument","status":400,"code":"INVALID_ARGUMENT","detail":"Selected evidence unavailable","requestId":"comparison-refusal"}',
      );
    }
  });
  const sdk = client(origin);
  const query = { otherSnapshotId: after, view: "changes", limit: 2 };
  const selected = await sdk.api.GET(
    "/api/v1/federation/snapshots/{snapshotId}/comparison",
    { params: { path: { snapshotId: before }, query } },
  );
  assert.equal(selected.response.status, 200);
  assert.deepEqual(selected.data, page);
  assert.equal(selected.data.items[0].before.versionPresent, false);
  assert.equal(selected.data.items[0].after, undefined);
  assert.equal(selected.data.items[1].before, undefined);
  assert.equal(selected.data.items[1].after.versionPresent, true);
  assert.equal(calls, 1, "no automatic continuation");
  const completed = await sdk.api.GET(
    "/api/v1/federation/snapshots/{snapshotId}/comparison",
    {
      params: {
        path: { snapshotId: before },
        query: { ...query, cursor: selected.data.nextCursor },
      },
    },
  );
  assert.equal(completed.response.status, 200);
  assert.deepEqual(completed.data, terminal);
  assert.equal(completed.data.nextCursor, undefined);
  assert.equal(completed.data.items[0].position, 3);
  assert.equal(calls, 2, "terminal page does not trigger another read");
  const refused = await sdk.api.GET(
    "/api/v1/federation/snapshots/{snapshotId}/comparison",
    {
      params: {
        path: { snapshotId: before },
        query: { ...query, cursor: selected.data.nextCursor },
      },
    },
  );
  assert.equal(refused.response.status, 400);
  assert.equal(refused.error.code, "INVALID_ARGUMENT");
  assert.equal(
    refused.response.headers.get("X-Request-ID"),
    "comparison-refusal",
  );
  assert.equal(refused.data, undefined);
  assert.equal(calls, 3, "no automatic restart or retry");
});

test("real anonymous and protected ARD discovery preserve both response branches", async (t) => {
  const manifest = "/.well-known/ard.json";
  const anonymous = await serve(t, (req, res) => {
    assert.equal(req.url, manifest);
    assert.equal(req.headers.authorization, undefined);
    assert.equal(req.headers.cookie, undefined);
    res.setHeader("Content-Type", "application/json");
    res.end('{"entries":[]}');
  });
  const publicClient = createCatalogClient({
    origin: anonymous,
    allowLoopbackHTTP: true,
  });
  const publicReply = await publicClient.api.GET(manifest);
  assert.equal(publicReply.response.status, 200);
  assert.deepEqual(publicReply.data.entries, []);
  const protectedOrigin = await serve(t, (req, res) => {
    assert.equal(req.url, manifest);
    assert.equal(req.headers.cookie, undefined);
    res.setHeader("Content-Type", "application/json");
    if (req.headers.authorization !== "Bearer explicit") {
      res.writeHead(401, { "X-Request-ID": "ard-refusal" });
      res.end(
        '{"errorCode":"UNAUTHENTICATED","message":"Authentication required"}',
      );
      return;
    }
    res.end('{"entries":[]}');
  });
  const refusal = await createCatalogClient({
    origin: protectedOrigin,
    allowLoopbackHTTP: true,
  }).api.GET(manifest);
  assert.equal(refusal.response.status, 401);
  assert.equal(refusal.response.headers.get("X-Request-ID"), "ard-refusal");
  assert.deepEqual(refusal.error, {
    errorCode: "UNAUTHENTICATED",
    message: "Authentication required",
  });
  const authenticated = await client(protectedOrigin).api.GET(manifest);
  assert.equal(authenticated.response.status, 200);
  assert.deepEqual(authenticated.data.entries, []);
});

test("real HTTP preserves bindings, ignores proxy variables and never repeats a lost or malformed write", async (t) => {
  let writes = 0,
    sink = 0;
  const draft = {
    namespaceId: "00000000-0000-7000-8000-000000000001",
    kind: "agent",
    canonicalIdentifier: "urn:air:catalog.example.com:demo:agent",
  };
  const origin = await serve(t, async (req, res) => {
    if (req.url === "/redirect") {
      res.writeHead(302, { Location: "/sink" });
      res.end();
      return;
    }
    if (req.url === "/sink") sink++;
    if (req.url === "/lost") {
      writes++;
      req.resume();
      await once(req, "end");
      req.socket.destroy();
      return;
    }
    if (req.url === "/api/v1/resources") {
      assert.equal(req.method, "POST");
      assert.equal(req.headers.authorization, "Bearer explicit");
      assert.equal(req.headers["idempotency-key"], "original-command");
      let body = "";
      for await (const chunk of req) body += chunk;
      assert.equal(body, JSON.stringify(draft));
      writes++;
      res.writeHead(201, {
        "Content-Type": "application/json",
        "X-Request-ID": "write-reply",
      });
      res.end("{PRIVATE_WRITE_REPLY");
      return;
    }
    assert.equal(req.headers.cookie, undefined);
    assert.equal(req.headers.authorization, "Bearer explicit");
    res.setHeader("X-Request-ID", "real");
    res.end('{"ok":true}');
  });
  const proxyVariables = ["HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"];
  const prior = new Map(proxyVariables.map((key) => [key, process.env[key]]));
  for (const key of proxyVariables)
    process.env[key] = key === "NO_PROXY" ? "" : origin;
  t.after(() => {
    for (const [key, value] of prior) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  });
  const c = client(origin);
  assert.equal((await c.raw("/redirect")).status, 302);
  assert.equal(sink, 0);
  for (const headers of [{}, { "Idempotency-Key": "retained" }])
    await assert.rejects(
      c.raw("/lost", { method: "POST", body: "{}", headers }),
      (e) => e.code === "request_failed",
    );
  assert.equal(writes, 2);
  await assert.rejects(
    c.api.POST("/api/v1/resources", {
      params: { header: { "Idempotency-Key": "original-command" } },
      body: draft,
    }),
    (e) =>
      e.code === "invalid_response" &&
      e.status === 201 &&
      e.requestID === "write-reply" &&
      !String(e).includes("PRIVATE_WRITE_REPLY"),
  );
  assert.equal(writes, 3);
  assert.equal((await c.raw("/ok")).headers.get("x-request-id"), "real");
  assert.equal(writes, 3);
});

test("real HTTP errors retain Retry-After and never trigger another request", async (t) => {
  const cases = new Map([
    [
      "/api/v1/me",
      {
        status: 503,
        mediaType: "application/problem+json",
        error: {
          type: "about:blank",
          title: "Service Unavailable",
          status: 503,
          detail: "Dependency unavailable",
          code: "DEPENDENCY_UNAVAILABLE",
          requestId: "retry-hint",
        },
      },
    ],
    [
      "/.well-known/ard.json",
      {
        status: 429,
        mediaType: "application/json",
        error: { errorCode: "TOO_MANY_REQUESTS", message: "Try later" },
      },
    ],
  ]);
  let calls = 0;
  const origin = await serve(t, (req, res) => {
    calls++;
    assert.equal(req.headers.authorization, "Bearer explicit");
    const fixture = cases.get(req.url);
    assert.ok(fixture);
    res.writeHead(fixture.status, {
      "Content-Type": fixture.mediaType,
      "Retry-After": "2",
      "X-Request-ID": "retry-hint",
    });
    res.end(JSON.stringify(fixture.error));
  });
  const c = client(origin);
  for (const [path, fixture] of cases) {
    const before = calls;
    const reply = await c.api.GET(path);
    assert.equal(reply.response.status, fixture.status);
    assert.equal(reply.response.headers.get("Retry-After"), "2");
    assert.equal(reply.response.headers.get("X-Request-ID"), "retry-hint");
    assert.deepEqual(reply.error, fixture.error);
    assert.equal(reply.data, undefined);
    assert.equal(
      new TextDecoder().decode(c.rawBytes(reply.response)),
      JSON.stringify(fixture.error),
    );
    assert.equal(calls, before + 1);
  }
});

test("real malformed JSON fails safely without poisoning later exchanges", async (t) => {
  let status = 200,
    body = "{PRIVATE_RESPONSE_SECRET",
    calls = 0;
  const origin = await serve(t, (req, res) => {
    calls++;
    assert.equal(req.url, "/.well-known/ard.json");
    res.writeHead(status, {
      "Content-Type": "application/json",
      "X-Request-ID": "invalid-json",
    });
    res.end(body);
  });
  const c = client(origin);
  for (const fixture of [
    { status: 200, options: {} },
    { status: 503, options: {} },
    { status: 503, options: { parseAs: "text" } },
  ]) {
    status = fixture.status;
    const before = calls;
    await assert.rejects(
      c.api.GET("/.well-known/ard.json", fixture.options),
      (e) =>
        e.code === "invalid_response" &&
        e.status === status &&
        e.requestID === "invalid-json" &&
        e.cause === undefined &&
        !String(e).includes("PRIVATE_RESPONSE_SECRET") &&
        !JSON.stringify(e).includes("PRIVATE_RESPONSE_SECRET"),
    );
    assert.equal(calls, before + 1);
    // An explicit safe read can obtain original evidence; parsing never resends it.
    const raw = await c.raw("/.well-known/ard.json");
    assert.equal(raw.status, status);
    assert.equal(raw.text(), body);
    assert.equal(calls, before + 2);
  }
  status = 200;
  body = '{"entries":[]}';
  assert.deepEqual((await c.api.GET("/.well-known/ard.json")).data, {
    entries: [],
  });
  assert.equal(calls, 7);
});

test("real streamed limits, truncation, deadline, cancellation and recovery", async (t) => {
  let started;
  const ready = new Promise((r) => {
    started = r;
  });
  let closed;
  const closure = new Promise((r) => {
    closed = r;
  });
  const origin = await serve(t, (req, res) => {
    if (req.url === "/compressed") {
      assert.equal(req.headers["accept-encoding"], "identity");
      res.writeHead(200, {
        "Content-Encoding": "gzip",
        "X-Request-ID": "compressed-body",
      });
      res.end(gzipSync("decoded-body"));
      return;
    }
    if (req.url === "/large") {
      res.write("1234");
      res.end("5678");
      return;
    }
    if (req.url === "/short") {
      res.writeHead(200, { "Content-Length": "99", Connection: "close" });
      res.end("x");
      return;
    }
    if (req.url === "/slow" || req.url === "/cancel") {
      res.writeHead(200);
      res.write("x");
      if (req.url === "/cancel") {
        res.on("close", closed);
        started();
      }
      return;
    }
    res.end("ok");
  });
  const c = createCatalogClient({
    origin,
    allowLoopbackHTTP: true,
    responseBytes: 4,
    timeoutMs: 1000,
  });
  await assert.rejects(
    c.raw("/compressed", { responseBytes: 128 }),
    (e) =>
      e.code === "unexpected_encoding" &&
      e.status === 200 &&
      e.requestID === "compressed-body",
  );
  await assert.rejects(c.raw("/large"), (e) => e.code === "response_too_large");
  await assert.rejects(
    c.raw("/short", { responseBytes: 128 }),
    (e) => e.code === "request_failed" && e.status === 200,
  );
  await assert.rejects(
    c.raw("/slow", { timeoutMs: 500 }),
    (e) => e.code === "deadline_exceeded" && e.status === 200,
  );
  const controller = new AbortController();
  const pending = c.raw("/cancel", { signal: controller.signal });
  await ready;
  controller.abort();
  await assert.rejects(pending, (e) => e.code === "cancelled");
  await Promise.race([
    closure,
    new Promise((_, reject) => {
      const timer = setTimeout(
        () => reject(new Error("connection not closed")),
        3000,
      );
      timer.unref();
    }),
  ]);
  assert.equal((await c.raw("/ok")).text(), "ok");
});

test("real TLS trusts only the selected CA and verifies hostnames", async (t) => {
  const work = await mkdtemp(join(tmpdir(), "catalog-sdk-tls-"));
  t.after(() => rm(work, { recursive: true, force: true }));
  execFileSync(
    "openssl",
    [
      "req",
      "-x509",
      "-newkey",
      "rsa:2048",
      "-nodes",
      "-days",
      "1",
      "-subj",
      "/CN=localhost",
      "-addext",
      "subjectAltName=DNS:localhost,IP:127.0.0.1",
      "-keyout",
      join(work, "key"),
      "-out",
      join(work, "cert"),
    ],
    { stdio: "ignore", timeout: 15000 },
  );
  const server = httpsServer(
    {
      key: await readFile(join(work, "key")),
      cert: await readFile(join(work, "cert")),
    },
    (_, res) => res.end('{"tls":true}'),
  );
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  t.after(() => {
    server.closeAllConnections();
    return new Promise((r) => server.close(r));
  });
  const origin = `https://127.0.0.1:${server.address().port}`;
  await assert.rejects(
    createCatalogClient({ origin }).raw("/"),
    (e) => e.code === "request_failed",
  );
  const source = `import {createCatalogClient} from ${JSON.stringify(pathToFileURL(process.env.CATALOG_SDK_ENTRY).href)}; const r=await createCatalogClient({origin:process.argv[2]}).raw('/'); if(r.text()!=='{"tls":true}')throw Error('TLS body');`;
  await writeFile(join(work, "control.mjs"), source);
  await execute(process.execPath, [join(work, "control.mjs"), origin], {
    env: { ...process.env, NODE_EXTRA_CA_CERTS: join(work, "cert") },
    timeout: 15000,
  });
  // A different loopback IP still reaches this listener only if bound to all loopback;
  // hostname verification is qualified by the explicit wrong-name certificate below.
  execFileSync(
    "openssl",
    [
      "req",
      "-x509",
      "-newkey",
      "rsa:2048",
      "-nodes",
      "-days",
      "1",
      "-subj",
      "/CN=wrong.example",
      "-addext",
      "subjectAltName=DNS:wrong.example",
      "-keyout",
      join(work, "wrong-key"),
      "-out",
      join(work, "wrong-cert"),
    ],
    { stdio: "ignore", timeout: 15000 },
  );
  server.setSecureContext({
    key: await readFile(join(work, "wrong-key")),
    cert: await readFile(join(work, "wrong-cert")),
  });
  await assert.rejects(
    execute(process.execPath, [join(work, "control.mjs"), origin], {
      env: { ...process.env, NODE_EXTRA_CA_CERTS: join(work, "wrong-cert") },
      timeout: 15000,
    }),
  );
});
