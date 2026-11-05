import test from "node:test";
import { listenFixture } from "../../../test/vscode/listen.mjs";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { createHash } from "node:crypto";
const { Browser, Session, connections } = await import(
  process.env.CATALOG_VSCODE_LIB
);
const id = "00000000-0000-7000-8000-000000000001";
const identifier = "urn:air:owned.example:team:tool";
const png = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jQ1kAAAAASUVORK5CYII=",
  "base64",
);
test("real SDK exchanges preserve partial live evidence, exact indexed identity, artwork fences and safe failures", async (t) => {
  let mode = "indexed",
    badImage = false,
    badJSON = false,
    refused = false,
    membershipId = id;
  const seen = [];
  const descriptor = {
    schemaVersion: 1,
    icon: {
      url: "https://uncontacted.invalid/icon.png",
      alt: "Owned image",
      caption: "Owned caption",
      rights: { basis: "owned", attribution: "Owned fixture" },
    },
  };
  const result = () => ({
    id: `${mode}:one`,
    type: mode,
    identifier,
    versionPresent: true,
    version: "",
    kind: "mcp_server",
    displayName: "Owned <script>unsafe()</script>",
    description: "Owned peer metadata",
    tags: ["owned"],
    score: 30,
    ratings: { count: 2, average: 4.5 },
    ...(mode === "indexed"
      ? {
          indexed: {
            sourceIds: [id],
            sourceCount: 1,
            conflict: false,
            sameSourceConflict: false,
            presentationSourceId: id,
            presentationGeneration: 1,
          },
          artwork: { descriptor },
        }
      : {
          live: {
            sourceId: id,
            source: "https://uncontacted.invalid/registry",
            entry: {
              identifier,
              version: "",
              url: "https://uncontacted.invalid/card",
              type: "application/mcp-server-card+json",
            },
          },
        }),
  });
  const server = createServer(async (req, res) => {
    let body = "";
    for await (const chunk of req) body += chunk;
    const url = new URL(req.url, "http://127.0.0.1");
    seen.push({
      path: url.pathname,
      params: url.searchParams,
      body,
      auth: req.headers.authorization,
    });
    res.setHeader("content-type", "application/json");
    if (refused) {
      res.writeHead(403);
      res.end("{}");
      return;
    }
    if (url.pathname === "/api/v1/me") {
      res.end(
        JSON.stringify({
          tenantId: id,
          realmId: id,
          membershipId,
          subject: "reader",
          permissions: [],
          availablePermissions: ["resource.read", "federation.discover"],
        }),
      );
      return;
    }
    if (url.pathname === "/api/v1/capabilities") {
      res.end(
        JSON.stringify({
          resourceKinds: ["agent", "mcp_server", "skill", "plugin"],
        }),
      );
      return;
    }
    if (badJSON) {
      res.end('{"items":');
      return;
    }
    if (url.pathname === "/api/v1/catalog/search") {
      res.end(
        JSON.stringify({
          items: [result()],
          partial: mode === "live",
          durableTotal: mode === "indexed" ? 1 : 0,
          liveCount: mode === "live" ? 1 : 0,
          diagnostics:
            mode === "live" ? [{ status: "insufficient_evidence" }] : [],
        }),
      );
      return;
    }
    if (url.pathname === "/api/v1/catalog/indexed-record") {
      res.end(JSON.stringify(result()));
      return;
    }
    if (url.pathname === "/api/v1/catalog/explore") {
      res.end(JSON.stringify({ facets: {} }));
      return;
    }
    if (url.pathname === "/api/v1/artwork/read") {
      assert.deepEqual(JSON.parse(body), {
        origin: "indexed",
        target: {
          sourceId: id,
          presentationGeneration: 1,
          identifier,
          versionPresent: true,
          version: "",
          kind: "mcp_server",
        },
        slot: "icon",
      });
      res.setHeader("content-type", "image/png");
      res.setHeader(
        "content-digest",
        `sha-256=:${createHash("sha256").update(png).digest("base64")}:`,
      );
      res.end(badImage ? Buffer.from("not an image") : png);
      return;
    }
    res.writeHead(404);
    res.end("{}");
  });
  await listenFixture(server);
  t.after(async () => {
    server.closeAllConnections();
    await new Promise((resolve) => server.close(resolve));
  });
  const session = new Session(
    {
      get: async () => undefined,
      store: async () => {},
      delete: async () => {},
    },
    "owned-host",
    async () => ({ fetch, close: async () => {} }),
  );
  const store = new Map(),
    browser = new Browser(
      session,
      {
        get: (k) => store.get(k),
        update: async (k, v) => {
          store.set(k, v);
        },
      },
      "owned-host",
    );
  t.after(async () => {
    browser.dispose();
    await session.dispose();
  });
  await session.select(
    connections([
      {
        id: "owned",
        label: "Owned",
        origin: `http://127.0.0.1:${server.address().port}`,
        allowLoopbackHTTP: true,
        mode: "control",
        auth: "manual",
      },
    ])[0],
  );
  await session.manual("owned-reader-token");
  await browser.search({ origin: "indexed" });
  await browser.select(browser.state.page.items[0].key);
  const image = await browser.image(0);
  assert.equal(image.value, `data:image/png;base64,${png.toString("base64")}`);
  assert.equal(image.alt, "Owned image");
  assert.equal(image.caption, "Owned caption · Owned fixture");
  await browser.explore({});
  assert.ok(seen.some((r) => r.path.endsWith("/explore")));
  assert.equal(
    browser.state.image,
    image,
    "ordinary HTTP reads retain artwork",
  );
  assert.equal(await browser.image(0), image);
  assert.equal(
    seen.filter((r) => r.path.endsWith("/artwork/read")).length,
    1,
    "repeated explicit loading must not download the same image again",
  );
  await browser.saveBookmark();
  await browser.reopen(0);
  assert.equal(browser.state.image, undefined, "reselection clears artwork");
  assert.equal(
    seen.find((r) => r.path.endsWith("indexed-record")).params.get("version"),
    "",
  );
  badImage = true;
  await assert.rejects(browser.image(0), /invalid_response/u);
  assert.equal(browser.state.detail, undefined);
  assert.equal(browser.state.image, undefined);
  badImage = false;
  mode = "live";
  const p = await browser.search({ origin: "live", text: "owned" });
  assert.equal(p.partial, true);
  assert.match(
    p.summary,
    /0 durable matches; 1 live results on this page; partial results/u,
  );
  await browser.select(p.items[0].key);
  assert.equal(
    browser.state.detail.documents[0].label,
    "Registry-declared entry (not original signed bytes)",
  );
  await assert.rejects(browser.saveBookmark(), /bookmark_unavailable/u);
  badJSON = true;
  await assert.rejects(
    browser.search({ origin: "live", text: "owned" }),
    /invalid_response/u,
  );
  assert.equal(browser.state.page, undefined);
  badJSON = false;
  refused = true;
  await assert.rejects(browser.search(), /access_refused/u);
  assert.equal(browser.state.detail, undefined);
  refused = false;
  await browser.search();
  let invalidated = false;
  const listener = session.onChange(() => {
    invalidated = true;
  });
  t.after(() => listener.dispose());
  const guard = session.guard(session.connection());
  membershipId = "00000000-0000-7000-8000-000000000002";
  await assert.rejects(browser.search(), /identity_changed/u);
  assert.equal(invalidated, true);
  assert.throws(guard, /connection_changed/u);
  assert.equal(session.snapshot(), undefined);
  assert.equal(browser.state.page, undefined);
  assert.equal(browser.state.detail, undefined);
  const calls = seen.length;
  await assert.rejects(session.test(), /sign_in_required/u);
  assert.equal(seen.length, calls, "changed identity cannot reuse old tokens");
  assert.ok(seen.every((r) => r.auth === "Bearer owned-reader-token"));
  assert.ok(
    seen.every((r) => r.path.startsWith("/api/v1/")),
    "metadata URLs must never be contacted",
  );
});

test("actual HTTP browse cancellation and timeout stop at verification or API read and permit explicit retry", async (t) => {
  let hold,
    reached,
    release,
    failure = 0;
  const seen = [];
  const server = createServer(async (req, res) => {
    const path = new URL(req.url, "http://127.0.0.1").pathname;
    seen.push(path);
    if (path === "/api/v1/catalog/resources" && failure) {
      res.writeHead(failure, { "content-type": "application/json" });
      res.end("{}");
      return;
    }
    if (path === hold) {
      reached();
      await new Promise((resolve) => {
        release = resolve;
        res.on("close", resolve);
      });
      if (res.destroyed) return;
    }
    res.setHeader("content-type", "application/json");
    res.end(
      JSON.stringify(
        path === "/api/v1/me"
          ? {
              tenantId: id,
              realmId: id,
              membershipId: id,
              subject: "reader",
              permissions: [],
              availablePermissions: ["resource.read"],
            }
          : path === "/api/v1/capabilities"
            ? { resourceKinds: ["agent"] }
            : { items: [], total: 0, nextCursor: "owned-cursor" },
      ),
    );
  });
  await listenFixture(server);
  t.after(async () => {
    release?.();
    server.closeAllConnections();
    await new Promise((resolve) => server.close(resolve));
  });
  const session = new Session(
    {
      get: async () => undefined,
      store: async () => {},
      delete: async () => {},
    },
    "owned",
    async () => ({ fetch, close: async () => {} }),
  );
  const browser = new Browser(
    session,
    { get: () => undefined, update: async () => {} },
    "owned",
  );
  t.after(async () => {
    browser.dispose();
    await session.dispose();
  });
  await session.select(
    connections([
      {
        id: "cancel",
        label: "Cancel",
        origin: `http://127.0.0.1:${server.address().port}`,
        allowLoopbackHTTP: true,
        mode: "control",
        auth: "manual",
        timeoutMs: 1000,
      },
    ])[0],
  );
  await session.manual("owned-reader");
  const order = [
    "/api/v1/me",
    "/api/v1/capabilities",
    "/api/v1/catalog/resources",
  ];
  for (const path of order) {
    hold = undefined;
    await browser.search();
    const count = seen.length;
    hold = path;
    const arrived = new Promise((resolve) => {
      reached = resolve;
    });
    const pending = assert.rejects(browser.search(browser.state.query, true), {
      code: "request_canceled",
    });
    await arrived;
    browser.cancel();
    await pending;
    assert.equal(browser.state.busy, false);
    assert.equal(browser.state.canNext, true);
    assert.deepEqual(
      seen.slice(count),
      order.slice(0, order.indexOf(path) + 1),
    );
    assert.equal(session.cancellation().aborted, false);
    hold = undefined;
    release?.();
    await browser.search(browser.state.query, true);
    assert.equal(browser.state.page.items.length, 0);
  }
  failure = 503;
  await assert.rejects(browser.search(browser.state.query, true), {
    code: "service_unavailable",
  });
  assert.equal(browser.state.page, undefined);
  assert.equal(browser.state.canNext, true);
  failure = 0;
  await browser.search(browser.state.query, true);
  assert.equal(browser.state.page.items.length, 0);
  hold = "/api/v1/me";
  const timedOut = assert.rejects(browser.search(browser.state.query, true), {
    code: "deadline_exceeded",
  });
  await timedOut;
  assert.equal(browser.state.error, "deadline_exceeded");
  assert.equal(browser.state.canNext, true);
  hold = undefined;
  release?.();
  await browser.search(browser.state.query, true);
  assert.equal(browser.state.error, undefined);
});
