import test from "node:test";
import { listenFixture } from "../../../test/vscode/listen.mjs";
import { fixtureWait } from "../../../test/vscode/wait.mjs";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { createServer as httpsServer } from "node:https";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFileSync } from "node:child_process";
const { mcpDocument, mcpAlternatives, McpSelections } = await import(
  process.env.CATALOG_VSCODE_LIB
);
test("real native-document acquisition refuses redirects, private CA reuse, excess bytes and outages, and cancels a pending body", async (t) => {
  let mode = "valid";
  const seen = [];
  let pending = Promise.withResolvers(),
    disconnected = Promise.withResolvers();
  const document = {
    $schema:
      "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json",
    name: "io.example/owned",
    description: "Owned MCP fixture",
    version: "1.0.0",
    remotes: [
      { type: "streamable-http", url: "https://not-contacted.invalid/mcp" },
    ],
  };
  const handler = (req, res) => {
    seen.push(req.headers);
    res.setHeader("content-type", "application/json");
    if (mode === "redirect") {
      res.writeHead(302, { location: "/unexpected" });
      res.end();
    } else if (mode === "pending") {
      res.on("close", disconnected.resolve);
      res.write("{");
      pending.resolve();
    } else if (mode === "large") res.end(Buffer.alloc(1024 ** 2 + 1));
    else if (mode === "outage") {
      res.writeHead(503);
      res.end("private-error");
    } else res.end(JSON.stringify(document));
  };
  const server = createServer(handler);
  await listenFixture(server);
  t.after(
    () =>
      new Promise((resolve) => {
        server.closeAllConnections();
        server.close(resolve);
      }),
  );
  const connection = {
    origin: "https://catalog.invalid",
    timeoutMs: 1000,
    allowLoopbackHTTP: true,
    headerBytes: 32768,
  };
  const read = () =>
    mcpDocument(
      { url: `http://127.0.0.1:${server.address().port}/card` },
      connection,
      async () => true,
    );
  const bytes = await read();
  assert.ok(mcpAlternatives(bytes)[0].recipe);
  const secrets = new Map(),
    selections = new McpSelections({
      trusted: () => true,
      acquire: (ref, signal) =>
        mcpDocument(ref, connection, async () => true, undefined, signal),
      confirm: async () => true,
      input: async () => "",
      secrets: {
        get: async (k) => secrets.get(k),
        store: async (k, v) => {
          secrets.set(k, v);
        },
        delete: async (k) => {
          secrets.delete(k);
        },
      },
      changed: () => {},
    });
  const selected = await selections.add(
    async () => ({ url: `http://127.0.0.1:${server.address().port}/card` }),
    () => {},
    async () => 0,
    true,
    "account",
  );
  await selections.resolve(selected.id);
  for (mode of ["redirect", "large", "outage"])
    await assert.rejects(read(), /mcp_document_(unavailable|too_large)/u);
  await assert.rejects(
    selections.resolve(selected.id),
    /mcp_document_unavailable/u,
  );
  assert.ok(seen.every((headers) => !headers.authorization && !headers.cookie));
  mode = "pending";
  const cancelled = new AbortController(),
    acquisition = mcpDocument(
      { url: `http://127.0.0.1:${server.address().port}/card` },
      { ...connection, timeoutMs: 30000 },
      async () => true,
      undefined,
      cancelled.signal,
    );
  const acquisitionRejected = assert.rejects(
    acquisition,
    /mcp_start_cancelled/u,
  );
  try {
    // Observe a real response before cancellation, rather than an unstarted fetch.
    await fixtureWait(pending.promise, "MCP document readiness", acquisition);
    cancelled.abort();
    await acquisitionRejected;
    await fixtureWait(disconnected.promise, "MCP document response close");
  } finally {
    cancelled.abort();
    await fixtureWait(
      Promise.allSettled([acquisitionRejected]),
      "MCP document operation cleanup",
    );
  }
  mode = "valid";
  assert.deepEqual(await read(), bytes);
  pending = Promise.withResolvers();
  disconnected = Promise.withResolvers();
  mode = "pending";
  const nativeCancelled = new AbortController();
  const resolving = selections.resolve(
    selected.id,
    () => {},
    nativeCancelled.signal,
  );
  const resolvingRejected = assert.rejects(resolving, /mcp_start_cancelled/u);
  try {
    await fixtureWait(pending.promise, "MCP resolution readiness", resolving);
    nativeCancelled.abort();
    await resolvingRejected;
    await fixtureWait(disconnected.promise, "MCP resolution response close");
  } finally {
    nativeCancelled.abort();
    await fixtureWait(
      Promise.allSettled([resolvingRejected]),
      "MCP resolution operation cleanup",
    );
  }
  assert.equal(secrets.size, 0);
  mode = "valid";
  assert.equal(
    (await selections.resolve(selected.id)).recipe.name,
    document.name,
  );
  const work = await mkdtemp(join(tmpdir(), "catalog-mcp-tls-"));
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
      "subjectAltName=IP:127.0.0.1",
      "-keyout",
      join(work, "key"),
      "-out",
      join(work, "cert"),
    ],
    { stdio: "ignore", timeout: 15000 },
  );
  const tls = httpsServer(
    {
      key: await readFile(join(work, "key")),
      cert: await readFile(join(work, "cert")),
    },
    handler,
  );
  await listenFixture(tls);
  t.after(
    () =>
      new Promise((resolve) => {
        tls.closeAllConnections();
        tls.close(resolve);
      }),
  );
  await assert.rejects(
    mcpDocument(
      { url: `https://127.0.0.1:${tls.address().port}/card` },
      { ...connection, caFile: join(work, "cert") },
      async () => true,
    ),
    /mcp_document_unavailable/u,
  );
});

test("real chunked MCP documents preserve UTF-8 bytes, close overflowing streams and recover after truncation", async (t) => {
  const prefix = Buffer.from('{"text":"Café 😀","padding":"');
  const suffix = Buffer.from('"}\n');
  const exact = Buffer.concat([
    prefix,
    Buffer.alloc(1024 ** 2 - prefix.length - suffix.length, "x"),
    suffix,
  ]);
  let mode = "exact";
  const closed = Promise.withResolvers();
  const requests = [];
  const server = createServer((req, res) => {
    requests.push({
      path: req.url,
      authorization: req.headers.authorization,
      cookie: req.headers.cookie,
    });
    res.writeHead(200, { "content-type": "application/json" });
    if (mode === "overflow") {
      res.on("close", closed.resolve);
      res.write(Buffer.concat([exact, Buffer.from("\n")]));
      return; // The reader must close this response; the server never ends it.
    }
    if (mode === "truncated") {
      res.flushHeaders();
      res.write('{"PRIVATE":"unfinished response');
      setImmediate(() => res.destroy());
      return;
    }
    const boundary = exact.indexOf(0xc3) + 1;
    res.write(exact.subarray(0, boundary));
    setImmediate(() => res.end(exact.subarray(boundary)));
  });
  await listenFixture(server);
  t.after(async () => {
    server.closeAllConnections();
    await new Promise((resolve) => server.close(resolve));
  });
  const read = () =>
    mcpDocument(
      { url: `http://127.0.0.1:${server.address().port}/card` },
      {
        origin: "https://uncontacted-catalog.invalid",
        allowLoopbackHTTP: true,
        timeoutMs: 1000,
      },
      async () => true,
    );
  assert.equal(exact.length, 1024 ** 2);
  assert.deepEqual(await read(), exact);
  mode = "overflow";
  await assert.rejects(read(), { code: "mcp_document_too_large" });
  let timer;
  try {
    await Promise.race([
      closed.promise,
      new Promise((_, reject) => {
        timer = setTimeout(
          () => reject(new Error("overflowing MCP response did not close")),
          3000,
        );
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
  mode = "truncated";
  await assert.rejects(read(), (error) => {
    assert.equal(error.code, "mcp_document_unavailable");
    assert.doesNotMatch(error.message, /PRIVATE|unfinished/u);
    return true;
  });
  mode = "exact";
  assert.deepEqual(await read(), exact);
  assert.equal(requests.length, 4, "each explicit read makes one request");
  assert.ok(
    requests.every(
      ({ path, authorization, cookie }) =>
        path === "/card" && !authorization && !cookie,
    ),
  );
});
