import test from "node:test";
import { listenFixture } from "../../../test/vscode/listen.mjs";
import { fixtureWait } from "../../../test/vscode/wait.mjs";
import assert from "node:assert/strict";
import { createServer } from "node:http";
const { Browser, Session, connections, projection } = await import(
  process.env.CATALOG_VSCODE_LIB
);
const id = "00000000-0000-7000-8000-000000000001",
  identifier = "urn:air:owned.example:team:tool";
test("actual SDK exact references preserve version identity and fence each peer replay before HTTP", async (t) => {
  let version,
    refuse = false,
    held,
    entered,
    allow = true,
    prompts = 0,
    alterLookup;
  const seen = [];
  const native = {
    $schema:
      "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json",
    name: "io.example/owned",
    description: "Owned fixture",
    version: "1.0.0",
    remotes: [
      { type: "streamable-http", url: "https://uncontacted.invalid/mcp" },
    ],
  };
  const entry = () => ({
    identifier,
    ...(version === undefined ? {} : { version }),
    type: "application/mcp-server-card+json",
    data: native,
  });
  const local = {
    resource: {
      id,
      namespaceId: id,
      kind: "mcp_server",
      canonicalIdentifier: identifier,
    },
    version: { version: "1.0.0" },
    listingRevision: {
      id,
      publisherId: id,
      visibility: "public",
      displayName: "Owned",
      description: "Owned",
      tags: [],
    },
    release: { id, state: "published" },
    versionRevision: {
      representations: [
        { mediaType: "application/mcp-server-card+json", data: native },
      ],
    },
  };
  const server = createServer(async (request, response) => {
    let body = "";
    for await (const part of request) body += part;
    const url = new URL(request.url, "http://127.0.0.1");
    seen.push({
      path: url.pathname,
      query: url.searchParams,
      body,
      authorization: request.headers.authorization,
    });
    response.setHeader("content-type", "application/json");
    if (refuse) {
      response.writeHead(403);
      response.end("{}");
      return;
    }
    let value;
    if (url.pathname === "/api/v1/me")
      value = {
        tenantId: id,
        realmId: id,
        membershipId: id,
        subject: "reader",
        permissions: [],
        availablePermissions: ["resource.read"],
      };
    else if (url.pathname === "/api/v1/capabilities")
      value = { resourceKinds: ["agent", "mcp_server", "skill", "plugin"] };
    else if (url.pathname.endsWith("/indexed-record"))
      value = {
        id: "indexed:owned",
        type: "indexed",
        identifier,
        versionPresent: version !== undefined,
        ...(version === undefined ? {} : { version }),
        kind: "mcp_server",
        displayName: "Owned indexed",
        score: 1,
        indexed: {
          sourceIds: [id],
          sourceCount: 1,
          conflict: false,
          sameSourceConflict: false,
        },
      };
    else if (url.pathname === `/api/v1/catalog/resources/${id}`) value = local;
    else if (url.pathname === "/.well-known/ard.json") value = { entries: [] };
    else if (url.pathname === "/ard/v1/search")
      value = { results: [entry()], pageToken: "opaque-owned-cursor" };
    else {
      response.writeHead(404);
      value = {};
    }
    if (
      alterLookup &&
      (url.pathname.endsWith("/indexed-record") ||
        url.pathname === `/api/v1/catalog/resources/${id}`)
    )
      alterLookup(value);
    response.end(JSON.stringify(value));
  });
  await listenFixture(server);
  t.after(async () => {
    held?.resolve(false);
    server.closeAllConnections();
    await new Promise((resolve) => server.close(resolve));
  });
  const origin = `http://127.0.0.1:${server.address().port}`,
    secrets = new Map(),
    saved = new Map();
  const session = new Session(
    {
      get: async (key) => secrets.get(key),
      store: async (key, value) => {
        secrets.set(key, value);
      },
      delete: async (key) => {
        secrets.delete(key);
      },
    },
    "owned-host",
    async () => ({ fetch, close: async () => {} }),
  );
  const browser = new Browser(
    session,
    {
      get: (key) => saved.get(key),
      update: async (key, value) => {
        saved.set(key, value);
      },
    },
    "owned-host",
    async (request) => {
      prompts++;
      assert.equal(request.query.text, "owned");
      assert.equal(request.continuation, true);
      entered?.resolve();
      return held ? held.promise : allow;
    },
  );
  t.after(async () => {
    browser.dispose();
    await session.dispose();
  });
  const options = (mode) =>
    connections([
      {
        id: mode,
        label: mode,
        origin,
        mode,
        auth: mode === "control" ? "manual" : "anonymous",
        allowLoopbackHTTP: true,
      },
    ])[0];
  await session.select(options("control"));
  await session.manual("owned-local-token");
  await browser.openReference(
    JSON.stringify({
      origin: "local",
      identifier,
      versionPresent: true,
      version: "1.0.0",
      releaseId: id,
    }),
  );
  assert.equal(
    projection(browser, "control", 1).detail.documents[0].action,
    "mcp",
  );
  await browser.saveBookmark();
  for (version of [undefined, "", "🙂".repeat(128)]) {
    await browser.openReference(
      JSON.stringify({
        origin: "indexed",
        identifier,
        versionPresent: version !== undefined,
        ...(version === undefined ? {} : { version }),
      }),
    );
    assert.equal(browser.state.detail.item.version, version);
    const lookup = seen.findLast((value) =>
      value.path.endsWith("/indexed-record"),
    );
    assert.equal(
      lookup.query.get("versionPresent"),
      String(version !== undefined),
    );
    assert.equal(
      lookup.query.get("version"),
      version === undefined ? null : version,
    );
  }
  version = "1.0.0";
  const references = {
    local: {
      origin: "local",
      identifier,
      versionPresent: true,
      version,
      releaseId: id,
    },
    indexed: { origin: "indexed", identifier, versionPresent: true, version },
  };
  for (const [origin, field, alter] of [
    [
      "local",
      "identifier",
      (value) => {
        value.resource.canonicalIdentifier = "urn:air:owned.example:team:other";
      },
    ],
    [
      "local",
      "version",
      (value) => {
        value.version.version = "2.0.0";
      },
    ],
    [
      "local",
      "releaseId",
      (value) => {
        value.release.id = "00000000-0000-7000-8000-000000000002";
      },
    ],
    [
      "indexed",
      "identifier",
      (value) => {
        value.identifier = "urn:air:owned.example:team:other";
      },
    ],
    [
      "indexed",
      "version",
      (value) => {
        value.version = "2.0.0";
      },
    ],
    [
      "indexed",
      "versionPresent",
      (value) => {
        value.versionPresent = false;
        delete value.version;
      },
    ],
    [
      "indexed",
      "origin",
      (value) => {
        value.type = "live";
        value.live = {
          sourceId: id,
          source: "https://uncontacted.invalid",
          entry: entry(),
        };
        delete value.indexed;
      },
    ],
  ]) {
    // Each valid response first reaches detail. Only the returned identity
    // changes; the copied reference and exact lookup URL remain unchanged.
    const reference = JSON.stringify(references[origin]);
    alterLookup = undefined;
    await browser.openReference(reference);
    assert.equal(browser.state.detail.item.origin, origin);
    const before = seen.length;
    alterLookup = alter;
    await assert.rejects(
      browser.openReference(reference),
      {
        code: "selection_changed",
      },
      `${origin} ${field}`,
    );
    assert.equal(browser.state.page, undefined);
    assert.equal(browser.state.detail, undefined);
    assert.equal(
      seen
        .slice(before)
        .filter(
          (value) =>
            value.path.endsWith("/indexed-record") ||
            value.path === `/api/v1/catalog/resources/${id}`,
        ).length,
      1,
      "A mismatched lookup cannot trigger a later detail read",
    );
    alterLookup = undefined;
    // The local fixture is shared across requests, so restore its exact tuple.
    local.resource.canonicalIdentifier = identifier;
    local.version.version = version;
    local.release.id = id;
  }
  refuse = true;
  await assert.rejects(
    browser.openReference(
      JSON.stringify({
        origin: "local",
        identifier,
        versionPresent: true,
        version: "1.0.0",
        releaseId: id,
      }),
    ),
    { code: "access_refused" },
  );
  assert.equal(browser.state.detail, undefined);
  refuse = false;
  await session.select(options("published"));
  version = "1.0.0";
  await browser.search({ text: "owned", federation: "auto" });
  await browser.search(browser.state.query, true);
  await browser.select(browser.state.page.items[0].key);
  const read = browser.consumer(1),
    count = () =>
      seen.filter((value) => value.path === "/ard/v1/search").length;
  const before = count();
  allow = false;
  await assert.rejects(read(), { code: "peer_query_cancelled" });
  assert.equal(count(), before);
  allow = true;
  assert.ok((await read()).bytes);
  assert.ok((await read()).bytes);
  assert.equal(count(), before + 2);
  assert.equal(prompts, 3);
  held = Promise.withResolvers();
  entered = Promise.withResolvers();
  const controller = new AbortController();
  const pending = read(controller.signal),
    rejected = assert.rejects(pending, { code: "request_canceled" });
  try {
    await fixtureWait(entered.promise, "reference consent readiness", pending);
    controller.abort();
    await rejected;
    held.resolve(true);
    await Promise.resolve();
  } finally {
    controller.abort();
    held.resolve(false);
    await fixtureWait(
      Promise.allSettled([rejected]),
      "reference operation cleanup",
    );
  }
  assert.equal(count(), before + 2);
  assert.ok(
    seen.every(
      (value) =>
        value.path.startsWith("/api/v1/") ||
        value.path.startsWith("/ard/") ||
        value.path === "/.well-known/ard.json",
    ),
  );
  assert.ok(
    seen
      .filter((value) => value.path.startsWith("/ard/"))
      .every((value) => !value.authorization),
  );
});
