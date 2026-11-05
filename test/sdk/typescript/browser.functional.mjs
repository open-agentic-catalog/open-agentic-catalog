// One owned same-origin browser fixture, using the installed npm artifact.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { once } from "node:events";
import { pathToFileURL } from "node:url";
import { chromium } from "../../../sdk/typescript/node_modules/playwright/index.mjs";
async function qualifyBrowserControl(consumer) {
  const assets = new Map([
    [
      "/sdk.js",
      join(consumer, "node_modules/@open-agentic-catalog/client/dist/index.js"),
    ],
    ["/fetch.js", join(consumer, "node_modules/openapi-fetch/dist/index.mjs")],
  ]);
  let calls = 0;
  const server = createServer(async (req, res) => {
    try {
      if (assets.has(req.url)) {
        res.setHeader("Content-Type", "text/javascript");
        res.end(await readFile(assets.get(req.url)));
        return;
      }
      if (req.url === "/.well-known/ard.json") {
        calls++;
        assert.equal(req.headers.cookie, undefined);
        assert.equal(req.headers.authorization, "Bearer browser-token");
        res.setHeader("Content-Type", "application/json");
        res.setHeader("ETag", '"browser"');
        res.end('{"entries":[]}');
        return;
      }
      res.setHeader("Content-Type", "text/html");
      res.setHeader("Set-Cookie", "must-not-send=secret; SameSite=Lax");
      res.end(
        '<!doctype html><title>SDK qualification</title><script type="importmap">{"imports":{"openapi-fetch":"/fetch.js"}}</script>',
      );
    } catch {
      res.statusCode = 500;
      res.end();
    }
  });
  let browser;
  try {
    server.listen(0, "127.0.0.1");
    await once(server, "listening");
    browser = await chromium.launch({ headless: true, timeout: 10000 });
    const context = await browser.newContext();
    const ownedOrigin = `http://127.0.0.1:${server.address().port}`;
    await context.route("**/*", (route) =>
      new URL(route.request().url()).origin === ownedOrigin
        ? route.continue()
        : route.abort(),
    );
    const page = await context.newPage();
    await page.goto(`http://127.0.0.1:${server.address().port}`, {
      timeout: 10000,
    });
    const cookie = (await context.cookies(ownedOrigin)).filter(
      (value) => value.name === "must-not-send",
    );
    assert.equal(cookie.length, 1);
    assert.equal(cookie[0].value, "secret");
    const result = await page.evaluate(async () => {
      const { createCatalogClient } = await import("/sdk.js");
      const c = createCatalogClient({
        origin: location.origin,
        allowLoopbackHTTP: true,
        token: "browser-token",
      });
      const r = await c.api.GET("/.well-known/ard.json");
      let refused = false;
      try {
        await c.api.GET("/.well-known/ard.json", {
          fetch: () => fetch("https://example.invalid"),
        });
      } catch (e) {
        refused = e.code === "invalid_request";
      }
      return {
        status: r.response.status,
        data: r.data,
        etag: r.response.headers.get("ETag"),
        refused,
      };
    });
    assert.deepEqual(result, {
      status: 200,
      data: { entries: [] },
      etag: '"browser"',
      refused: true,
    });
    assert.equal(calls, 1);
    console.log(
      "Installed browser SDK passed: same-origin ESM, explicit bearer, cookie omission, metadata and override refusal",
    );
  } finally {
    try {
      await browser?.close();
    } finally {
      server.closeAllConnections();
      await new Promise((r) => server.close(r));
    }
  }
}

// The browser uses one owned origin. This fixture-only gateway forwards API
// requests to the already-owned Catalog listener; it does not add product CORS.
export async function qualifyCatalogBrowser(consumer, apiOrigin, releaseIDs) {
  const upstream = new URL(apiOrigin);
  assert.equal(upstream.protocol, "http:");
  assert.equal(upstream.hostname, "127.0.0.1");
  assert.equal(upstream.pathname, "/");
  const assets = new Map([
    [
      "/sdk.js",
      join(consumer, "node_modules/@open-agentic-catalog/client/dist/index.js"),
    ],
    ["/fetch.js", join(consumer, "node_modules/openapi-fetch/dist/index.mjs")],
  ]);
  let calls = 0;
  const failures = [];
  const server = createServer(async (req, res) => {
    try {
      if (assets.has(req.url)) {
        res.setHeader("Content-Type", "text/javascript");
        res.end(await readFile(assets.get(req.url)));
        return;
      }
      if (req.url.startsWith("/api/v1/")) {
        calls++;
        assert.equal(req.headers.cookie, undefined);
        assert.equal(req.headers.authorization, "Bearer reader");
        const chunks = [];
        let size = 0;
        for await (const chunk of req) {
          size += chunk.length;
          assert.ok(size <= 64 * 1024, "browser request exceeds fixture bound");
          chunks.push(chunk);
        }
        const response = await fetch(apiOrigin + req.url, {
          method: req.method,
          headers: {
            Authorization: req.headers.authorization,
            ...(req.headers["content-type"]
              ? { "Content-Type": req.headers["content-type"] }
              : {}),
            ...(req.headers["idempotency-key"]
              ? { "Idempotency-Key": req.headers["idempotency-key"] }
              : {}),
          },
          ...(size ? { body: Buffer.concat(chunks) } : {}),
          signal: AbortSignal.timeout(3000),
          redirect: "manual",
          credentials: "omit",
        });
        const bytes = [];
        let responseSize = 0;
        for await (const chunk of response.body) {
          responseSize += chunk.length;
          assert.ok(
            responseSize <= 1024 * 1024,
            "browser response exceeds fixture bound",
          );
          bytes.push(chunk);
        }
        res.statusCode = response.status;
        for (const name of ["content-type", "etag", "x-request-id"])
          if (response.headers.has(name))
            res.setHeader(name, response.headers.get(name));
        res.end(Buffer.concat(bytes));
        return;
      }
      assert.equal(req.url, "/");
      res.setHeader("Content-Type", "text/html");
      res.setHeader("Set-Cookie", "must-not-send=secret; SameSite=Lax");
      res.end(
        '<!doctype html><title>Installed SDK Catalog workflow</title><link rel="icon" href="data:,"><script type="importmap">{"imports":{"openapi-fetch":"/fetch.js"}}</script>',
      );
    } catch (error) {
      failures.push(error);
      res.statusCode = 500;
      res.end();
    }
  });
  let browser;
  try {
    server.listen(0, "127.0.0.1");
    await once(server, "listening");
    const ownedOrigin = `http://127.0.0.1:${server.address().port}`;
    browser = await chromium.launch({ headless: true, timeout: 10000 });
    const context = await browser.newContext();
    await context.route("**/*", (route) =>
      new URL(route.request().url()).origin === ownedOrigin
        ? route.continue()
        : route.abort(),
    );
    const page = await context.newPage();
    await page.goto(ownedOrigin, { timeout: 10000 });
    const cookie = (await context.cookies(ownedOrigin)).filter(
      (value) => value.name === "must-not-send",
    );
    assert.equal(cookie.length, 1);
    assert.equal(cookie[0].value, "secret");
    const result = await page.evaluate(async () => {
      const { createCatalogClient } = await import("/sdk.js");
      const client = createCatalogClient({
        origin: location.origin,
        allowLoopbackHTTP: true,
        token: "reader",
        timeoutMs: 3000,
      });
      const first = await client.api.POST("/api/v1/catalog/search", {
        body: { origins: ["local"], kinds: ["agent"], limit: 1 },
      });
      const second = await client.api.POST("/api/v1/catalog/search", {
        body: {
          origins: ["local"],
          kinds: ["agent"],
          limit: 1,
          cursor: first.data.nextCursor,
        },
      });
      const releaseID = first.data.items[0].local.release.id;
      const detail = await client.api.GET(
        "/api/v1/catalog/resources/{catalogRecordId}",
        {
          params: { path: { catalogRecordId: releaseID } },
        },
      );
      const denied = await client.api.POST("/api/v1/namespaces", {
        params: { header: { "Idempotency-Key": "ts-browser-denied" } },
        body: { slug: "browser-denied", displayName: "Must not exist" },
      });
      return {
        statuses: [
          first.response.status,
          second.response.status,
          detail.response.status,
          denied.response.status,
        ],
        ids: [releaseID, second.data.items[0].local.release.id],
        sizes: [first.data.items.length, second.data.items.length],
        continued: Boolean(first.data.nextCursor),
        finished: !second.data.nextCursor,
        partial: [first.data.partial, second.data.partial],
        detailID: detail.data.release.id,
        exact: new TextDecoder()
          .decode(client.rawBytes(detail.response))
          .includes("9007199254740993"),
        requestID: detail.response.headers.get("X-Request-ID"),
        denial: denied.error.code,
      };
    });
    assert.deepEqual(result.statuses, [200, 200, 200, 403]);
    assert.deepEqual(new Set(result.ids), new Set(releaseIDs));
    assert.deepEqual(result.sizes, [1, 1]);
    assert.equal(result.continued, true);
    assert.equal(result.finished, true);
    assert.deepEqual(result.partial, [false, false]);
    assert.equal(result.detailID, result.ids[0]);
    assert.equal(result.exact, true);
    assert.ok(result.requestID);
    assert.equal(result.denial, "FORBIDDEN");
    assert.equal(calls, 4);
    assert.deepEqual(failures, []);
    console.log(
      "Installed browser SDK against real Catalog passed: nonempty Search continuation, exact detail bytes, reader denial and cookie omission",
    );
  } finally {
    try {
      await browser?.close();
    } finally {
      server.closeAllConnections();
      await new Promise((resolve) => server.close(resolve));
    }
  }
}

if (process.argv[1] && pathToFileURL(process.argv[1]).href === import.meta.url)
  await qualifyBrowserControl(process.argv[2]);
