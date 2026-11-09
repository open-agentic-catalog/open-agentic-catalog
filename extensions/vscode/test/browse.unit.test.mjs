import test from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
const {
  Browser,
  ClientError,
  Session,
  connections,
  emptyQuery,
  query,
  requestFor,
  page,
  localItem,
  searchItem,
  publicItem,
  projection,
  message,
  html,
  link,
  decodeJSON,
  encodeProjection,
  literal,
  paragraph,
} = await import(process.env.CATALOG_VSCODE_LIB);
const id = "00000000-0000-7000-8000-000000000001";
const { exactReference, bookmarkLabel, documentAction } = await import(
  process.env.CATALOG_VSCODE_LIB
);
test("consumer action guards fence an approved peer replay before HTTP", async (t) => {
  let active = true,
    expire = false;
  const entry = {
    identifier: "urn:air:example.com:test:mcp",
    type: "application/mcp-server-card+json",
    data: { name: "io.example/owned", version: "1.0.0" },
  };
  const f = setup({
    mode: "published",
    peerReplay: async () => {
      if (expire) active = false;
      return true;
    },
    handler: (_u, _o, reply) => reply({ results: [entry] }),
  });
  t.after(() => f.close());
  await f.start();
  await f.browser.search({ text: "owned", federation: "auto" });
  await f.browser.select(f.browser.state.page.items[0].key);
  const read = f.browser.consumer(1, () => {
    if (!active) throw new ClientError("selection_changed");
  });
  await read();
  const requests = f.calls.length;
  expire = true;
  await assert.rejects(read(), { code: "selection_changed" });
  assert.equal(
    f.calls.length,
    requests + 1,
    "Only manifest verification is allowed before the stale action is refused",
  );
});
test("first-use and read recovery guidance keeps empty, partial and failed states distinct", async () => {
  const { guidance, message } = await import(process.env.CATALOG_VSCODE_LIB);
  for (const [connection, loaded, count, partial, error, phase, action] of [
    ["none", false, 0, false, undefined, "no_connection", "addConnection"],
    ["signed_out", false, 0, false, undefined, "signed_out", "signIn"],
    [
      "none",
      false,
      0,
      false,
      "workspace_settings_refused",
      "failed",
      "settings",
    ],
    ["anonymous", false, 0, false, "unauthorized", "failed", "settings"],
    ["unverified", false, 0, false, "unauthenticated", "signed_out", "signIn"],
    ["anonymous", false, 0, false, undefined, "not_loaded", "restart"],
    ["verified", true, 0, false, undefined, "empty", "restart"],
    ["verified", true, 0, true, undefined, "partial", "restart"],
    ["verified", true, 1, false, undefined, "loaded", undefined],
    ["unverified", false, 0, false, "invalid_cursor", "failed", "restart"],
    [
      "unverified",
      false,
      0,
      false,
      "workspace_trust_required",
      "failed",
      "trust",
    ],
    ["unverified", false, 0, false, "forbidden", "failed", "connect"],
    ["unverified", false, 0, false, "invalid_query", "failed", "help"],
    ["unverified", false, 0, false, "unsupported_filter", "failed", "help"],
    ["unverified", false, 0, false, "namespace_is_local", "failed", "help"],
    ["unverified", false, 0, false, "request_failed", "failed", "settings"],
  ]) {
    const value = guidance(connection, loaded, count, partial, error);
    assert.equal(value.phase, phase);
    assert.equal(value.actions[0], action);
    for (const action of value.actions)
      assert.equal(
        message({ type: "recover", revision: 1, action }).action,
        action,
      );
  }
  for (const action of [
    "workbench.action.terminal.new",
    "signIn;unsafe",
    undefined,
    {},
  ])
    assert.throws(() => message({ type: "recover", revision: 1, action }));
  assert.throws(() =>
    message({
      type: "recover",
      revision: 1,
      action: "signIn",
      token: "sentinel",
    }),
  );
});
const summary = (name = "Agent") => ({
  resource: {
    id,
    namespaceId: id,
    canonicalIdentifier: "urn:air:example.com:test:agent",
    kind: "agent",
  },
  version: { version: "1.0.0" },
  listingRevision: {
    id,
    displayName: name,
    description: "<script>hostile()</script>\nA valid description.",
    tags: ["safe", "tag"],
    publisherId: id,
    visibility: "public",
  },
  release: { id, state: "published" },
});
const bytes = Buffer.from(
  '{"large":9007199254740993,"message":"<img src=x onerror=alert(1)>"}',
);
const digest = createHash("sha256").update(bytes).digest("hex");
test("denied SDK reads offer access recovery without authentication fallback", async () => {
  for (const mode of ["control", "published"]) {
    const f = setup({
      mode,
      handler: (_url, _options, response) =>
        response({ code: "forbidden" }, 403),
    });
    try {
      await f.start();
      await assert.rejects(f.browser.search(), { code: "access_refused" });
      const view = projection(f.browser, mode, 1, f.session.connectionState());
      assert.equal(view.guidance.phase, "failed");
      assert.deepEqual(view.guidance.actions, ["connect", "help"]);
      assert.match(
        view.guidance.message,
        mode === "control" ? /permissions/u : /publication/u,
      );
      assert.equal(view.canBrowse, true);
    } finally {
      await f.close();
    }
  }
});
test("failed searches retain submitted filters and clear the previous page", async () => {
  let fail = false;
  const f = setup({
    handler: (_url, _options, response) =>
      fail
        ? response({ code: "service_unavailable" }, 503)
        : response({ items: [summary()], total: 1 }),
  });
  await f.start();
  await f.browser.search();
  fail = true;
  await assert.rejects(
    f.browser.search({ text: "new filters", kind: "agent", tag: "safe" }),
  );
  assert.equal(f.browser.state.query.text, "new filters");
  assert.equal(f.browser.state.query.tag, "safe");
  assert.equal(f.browser.state.page, undefined);
  assert.equal(f.browser.state.detail, undefined);
  assert.ok(f.browser.state.error);
  await f.session.dispose();
});
function setup({ mode = "control", handler, verify, peerReplay } = {}) {
  const calls = [],
    saved = new Map(),
    secrets = new Map();
  const principal = {
    tenantId: id,
    realmId: id,
    membershipId: id,
    subject: "reader",
    permissions: [],
    availablePermissions: ["resource.read"],
  };
  const response = (value, status = 200) =>
    new Response(JSON.stringify(value), {
      status,
      headers: { "content-type": "application/json" },
    });
  const network = async (request) => {
    const url = request.url,
      options = {
        headers: request.headers,
        body: await request.text(),
        signal: request.signal,
      };
    calls.push({ url, options });
    const path = new URL(url).pathname;
    if (verify && ["/api/v1/me", "/api/v1/capabilities"].includes(path))
      return verify(path, request, response, principal);
    if (path === "/api/v1/me") return response(principal);
    if (path === "/api/v1/capabilities")
      return response({
        resourceKinds: ["agent", "mcp_server", "skill", "plugin"],
      });
    if (path === "/.well-known/ard.json") return response({ entries: [] });
    if (handler) return handler(url, options, response);
    if (path === "/api/v1/catalog/resources")
      return response({
        items: [summary()],
        total: 2,
        nextCursor: "opaque+/=cursor",
      });
    if (path === `/api/v1/catalog/resources/${id}`)
      return response({
        ...summary(),
        versionRevision: {
          representations: [
            {
              mediaType: "application/a2a-agent-card+json",
              size: bytes.length,
              artifactId: digest,
            },
          ],
        },
      });
    if (path === `/api/v1/artifacts/${digest}/content`)
      return new Response(bytes);
    return response({}, 404);
  };
  const connection = connections([
    {
      id: "test",
      label: "Test",
      origin: "https://catalog.example",
      mode,
      auth: mode === "control" ? "manual" : "anonymous",
    },
  ])[0];
  const session = new Session(
    {
      get: async (k) => secrets.get(k),
      store: async (k, v) => {
        secrets.set(k, v);
      },
      delete: async (k) => {
        secrets.delete(k);
      },
    },
    "host",
    async () => ({ fetch: network, close: async () => {} }),
  );
  const browser = new Browser(
    session,
    {
      get: (k) => saved.get(k),
      update: async (k, v) => {
        saved.set(k, v);
      },
    },
    "host",
    peerReplay,
  );
  return {
    session,
    browser,
    calls,
    saved,
    principal,
    connection,
    async start() {
      await session.select(connection);
      if (mode === "control") await session.manual("reader");
    },
    async close() {
      browser.dispose();
      await session.dispose();
    },
  };
}
test("copied references are closed duplicate-safe exact identities, never URLs or guessed versions", () => {
  const identifier = "urn:air:example.com:test:agent";
  for (const version of [undefined, "", "🙂".repeat(128), "\n".repeat(128)]) {
    const value = {
      origin: "indexed",
      identifier,
      versionPresent: version !== undefined,
      ...(version === undefined ? {} : { version }),
    };
    assert.deepEqual(exactReference(JSON.stringify(value)), value);
    assert.match(
      bookmarkLabel(value),
      version === undefined
        ? /version not supplied/
        : version === ""
          ? /empty version/
          : /version /,
    );
  }
  const local = {
    origin: "local",
    identifier,
    versionPresent: true,
    version: "1.0.0",
    releaseId: id,
  };
  assert.deepEqual(exactReference(JSON.stringify(local)), local);
  for (const value of [
    { ...local, url: "https://uncontacted.invalid" },
    { ...local, origin: "live" },
    { ...local, origin: "published" },
    { ...local, releaseId: "other" },
    { ...local, versionPresent: false },
    { ...local, version: null },
    { ...local, identifier: "https://uncontacted.invalid" },
    { ...local, version: "\0" },
    { ...local, version: "\ud800" },
    { ...local, version: "x".repeat(129) },
  ])
    assert.throws(() => exactReference(JSON.stringify(value)));
  for (const replacement of [
    '"origin":"indexed","origin":"local"',
    '"or\\u0069gin":"indexed","origin":"local"',
  ]) {
    const duplicate = JSON.stringify(local).replace(
      '"origin":"local"',
      replacement,
    );
    assert.deepEqual(JSON.parse(duplicate), local);
    assert.throws(() => exactReference(duplicate), {
      code: "invalid_reference",
    });
  }
  assert.throws(() => exactReference(" ".repeat(8193)));
});
test("opening an exact reference checks every identity field and a later read cannot retarget it", async (t) => {
  const resource = summary();
  const f = setup({
    handler: (url, _, response) =>
      new URL(url).pathname === `/api/v1/catalog/resources/${id}`
        ? response({ ...resource, versionRevision: { representations: [] } })
        : response({}, 404),
  });
  t.after(() => f.close());
  await f.start();
  const value = {
    origin: "local",
    identifier: resource.resource.canonicalIdentifier,
    versionPresent: true,
    version: "1.0.0",
    releaseId: id,
  };
  await f.browser.openReference(JSON.stringify(value));
  assert.equal(f.browser.state.detail.item.identifier, value.identifier);
  assert.equal(f.browser.state.canNext, false);
  await assert.rejects(
    f.browser.openReference(
      JSON.stringify({
        ...value,
        identifier: "urn:air:example.com:test:other",
      }),
    ),
    { code: "selection_changed" },
  );
  assert.equal(f.browser.state.detail, undefined);
  const requests = f.calls.length;
  await assert.rejects(
    f.browser.openReference(
      JSON.stringify({ ...value, url: "https://uncontacted.invalid" }),
    ),
  );
  assert.equal(f.calls.length, requests);
});
test("reference removal refuses a changed position during account verification", async (t) => {
  let hold = false;
  const entered = Promise.withResolvers(),
    release = Promise.withResolvers();
  const f = setup({
    verify: async (path, _request, reply, principal) => {
      if (path === "/api/v1/me" && hold) {
        hold = false;
        entered.resolve();
        await release.promise;
      }
      return reply(
        path === "/api/v1/me" ? principal : { resourceKinds: ["agent"] },
      );
    },
  });
  t.after(() => f.close());
  await f.start();
  await f.browser.search();
  await f.browser.select(f.browser.state.page.items[0].key);
  await f.browser.saveBookmark();
  const key = f.browser.bookmarkAccounts()[0].key;
  const replacement = { releaseId: "00000000-0000-7000-8000-000000000002" };
  hold = true;
  const removing = f.browser.removeBookmark(0);
  await entered.promise;
  f.saved.set("catalogBookmarks", { [key]: [replacement] });
  release.resolve();
  await assert.rejects(removing, { code: "selection_changed" });
  assert.deepEqual(f.saved.get("catalogBookmarks")[key], [replacement]);
  await f.browser.removeBookmark(0);
  assert.equal(f.saved.get("catalogBookmarks")[key], undefined);
});
test("offline reference forgetting is exact, bounded and preserves other account bindings", async (t) => {
  const f = setup();
  t.after(() => f.close());
  await f.start();
  await f.browser.search();
  await f.browser.select(f.browser.state.page.items[0].key);
  await f.browser.saveBookmark();
  const current = f.browser.bookmarkAccounts()[0];
  const other = "f".repeat(64),
    all = f.saved.get("catalogBookmarks");
  f.saved.set("catalogBookmarks", { ...all, [other]: [{ releaseId: id }] });
  await f.session.signOut();
  const calls = f.calls.length;
  await f.browser.forgetBookmarks(current.key);
  assert.equal(f.calls.length, calls);
  assert.deepEqual(f.saved.get("catalogBookmarks"), {
    [other]: [{ releaseId: id }],
  });
  assert.equal(
    f.browser.bookmarkAccounts().find((v) => v.key === current.key),
    undefined,
  );
  await assert.rejects(f.browser.forgetBookmarks("../other"));
  assert.equal(f.browser.bookmarkAccounts()[0].count, 1);
});
test("peer consumer replay asks for every frozen continuation, and decline, cancellation and stale sessions send no replay", async (t) => {
  let consent = true,
    prompts = 0,
    held,
    entered;
  const entry = {
    identifier: "urn:air:example.com:test:mcp",
    version: "",
    type: "application/vnd.mcp.server+json",
    data: { name: "io.example/owned", version: "1.0.0" },
  };
  const f = setup({
    mode: "published",
    peerReplay: async (request) => {
      prompts++;
      assert.equal(request.query.text, "owned");
      assert.equal(request.continuation, true);
      entered?.resolve();
      return held ? held.promise : consent;
    },
    handler: (url, _, response) =>
      new URL(url).pathname === "/ard/v1/search"
        ? response({ results: [entry], pageToken: "owned-cursor" })
        : response({}, 404),
  });
  t.after(() => f.close());
  await f.start();
  await f.browser.search({ text: "owned", federation: "auto" });
  await f.browser.search(f.browser.state.query, true);
  await f.browser.select(f.browser.state.page.items[0].key);
  const read = f.browser.consumer(1),
    count = () =>
      f.calls.filter((v) => v.url.endsWith("/ard/v1/search")).length;
  const before = count();
  consent = false;
  await assert.rejects(read(), { code: "peer_query_cancelled" });
  assert.equal(count(), before);
  consent = true;
  await read();
  await read();
  assert.equal(count(), before + 2);
  assert.equal(prompts, 3);
  held = Promise.withResolvers();
  entered = Promise.withResolvers();
  const controller = new AbortController();
  const reading = read(controller.signal);
  await entered.promise;
  controller.abort();
  await assert.rejects(reading, { code: "request_canceled" });
  held.resolve(true);
  await Promise.resolve();
  assert.equal(count(), before + 2);
  held = Promise.withResolvers();
  entered = Promise.withResolvers();
  const stale = read();
  await entered.promise;
  await f.session.signOut();
  held.resolve(true);
  await assert.rejects(stale, { code: "connection_changed" });
  assert.equal(count(), before + 2);
});
test("native action hints use explicit document families and existing metadata without fetching", () => {
  assert.equal(
    documentAction({ label: "Registry projection", value: {} }, "agent").action,
    undefined,
  );
  assert.match(
    documentAction({ label: "Registry projection", value: {} }, "agent").reason,
    /Inspect-only/,
  );
  assert.equal(
    documentAction(
      {
        label: "Card",
        mediaType: "application/a2a-agent-card+json",
        url: "https://uncontacted.invalid/card",
      },
      "agent",
    ).action,
    "a2a",
  );
  assert.equal(
    documentAction(
      {
        label: "Card",
        mediaType: "application/a2a-agent-card+json",
        size: 1024 ** 2 + 1,
      },
      "agent",
    ).action,
    undefined,
  );
  const server = {
    $schema:
      "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json",
    name: "io.example/owned",
    description: "Owned",
    version: "1.0.0",
    remotes: [{ type: "sse", url: "https://uncontacted.invalid" }],
  };
  assert.match(
    documentAction(
      {
        label: "Server Card",
        mediaType: "application/mcp-server-card+json",
        value: server,
      },
      "mcp_server",
    ).reason,
    /Inspect-only/,
  );
  server.remotes[0].type = "streamable-http";
  assert.equal(
    documentAction(
      {
        label: "Server Card",
        mediaType: "application/mcp-server-card+json",
        value: server,
      },
      "mcp_server",
    ).action,
    "mcp",
  );
});
test("MCP consumer reads reauthorize a frozen release independently of the current browser view", async (t) => {
  let denied = false;
  const resource = {
    ...summary(),
    resource: { ...summary().resource, kind: "mcp_server" },
  };
  const f = setup({
    handler: (url, _, response) => {
      if (new URL(url).pathname === "/api/v1/catalog/resources")
        return response({ items: [resource], total: 1 });
      if (new URL(url).pathname === `/api/v1/catalog/resources/${id}`)
        return response(
          {
            ...resource,
            versionRevision: {
              representations: [
                {
                  mediaType: "application/mcp-server-card+json",
                  data: { name: "io.example/owned", version: "1.0.0" },
                },
              ],
            },
          },
          denied ? 403 : 200,
        );
      return response({}, 404);
    },
  });
  t.after(() => f.close());
  await f.start();
  await f.browser.search();
  await f.browser.select(f.browser.state.page.items[0].key);
  const read = f.browser.consumer(0);
  assert.match(
    Buffer.from((await read()).bytes).toString(),
    /io.example\/owned/u,
  );
  await f.browser.search();
  assert.equal(f.browser.state.detail, undefined);
  assert.match(
    Buffer.from((await read()).bytes).toString(),
    /io.example\/owned/u,
  );
  denied = true;
  await assert.rejects(read(), /access_refused/u);
  await f.session.signOut();
  await assert.rejects(read(), /connection_changed/u);
});
test("published MCP consumption rereads the exact selected page and rejects changed evidence", async (t) => {
  let changed = false;
  const entry = () => ({
    identifier: "urn:air:example.com:test:mcp",
    version: "1",
    type: "application/vnd.mcp.server+json",
    data: {
      name: changed ? "io.example/changed" : "io.example/owned",
      version: "1.0.0",
    },
  });
  const f = setup({
    mode: "published",
    handler: (url, _, response) =>
      new URL(url).pathname === "/ard/v1/agents"
        ? response({ items: [entry()], total: 1 })
        : response({}, 404),
  });
  t.after(() => f.close());
  await f.start();
  await f.browser.search();
  await f.browser.select(f.browser.state.page.items[0].key);
  const read = f.browser.consumer(1);
  assert.equal((await read()).mediaType, "application/vnd.mcp.server+json");
  changed = true;
  await assert.rejects(read(), /selection_changed/u);
});
test("a changed verified account invalidates private document guards as well as browser state", async (t) => {
  const f = setup();
  t.after(() => f.close());
  await f.start();
  await f.browser.search();
  await f.browser.select(f.browser.state.page.items[0].key);
  assert.deepEqual(Buffer.from((await f.browser.document(0)).bytes), bytes);
  await f.browser.saveBookmark();
  const before = f.session.guard(f.connection);
  let invalidated = 0;
  const listener = f.session.onChange(() => invalidated++);
  t.after(() => listener.dispose());
  f.principal.membershipId = "00000000-0000-7000-8000-000000000002";
  await assert.rejects(f.browser.search(), /identity_changed/u);
  assert.equal(
    invalidated,
    1,
    "native evidence views must receive invalidation",
  );
  assert.throws(before, /connection_changed/u);
  assert.equal(f.session.snapshot(), undefined);
  assert.equal(f.browser.state.page, undefined);
  assert.equal(f.browser.state.detail, undefined);
  assert.equal(f.browser.state.facets, undefined);
  assert.deepEqual(f.browser.state.bookmarks, []);
  const calls = f.calls.length;
  await assert.rejects(f.session.test(), /sign_in_required/u);
  assert.equal(f.calls.length, calls, "old credentials cannot be sent again");
});
test("endpoint allowlists preserve local origins, public List and exact declared media families", () => {
  assert.match(
    requestFor(query({}, false), false).path,
    /catalog\/resources\?limit=20/u,
  );
  const body = requestFor(
    query(
      {
        text: "find",
        license: "MIT",
        category: "tools",
        tag: "tag",
        namespace: id,
      },
      false,
    ),
    false,
  ).body;
  assert.deepEqual(body.origins, ["local"]);
  assert.equal(body.namespaceId, id);
  assert.deepEqual(body.categories, ["tools"]);
  assert.throws(
    () => query({ namespace: id, origin: "indexed" }, false),
    /namespace_is_local/u,
  );
  assert.throws(() => query({ namespace: id }, true), /unsupported_filter/u);
  assert.throws(
    () => query({ tag: "tag" }, true),
    /published_search_needs_text/u,
  );
  assert.throws(() => query({ limit: 101 }, false));
  assert.throws(() => query({ text: "é".repeat(251) }, false));
  assert.throws(() => query({ sources: ["arbitrary"] }, false));
  const list = new URL(
    requestFor(
      query({ kind: "mcp_server", publisher: "example.com" }, true),
      true,
    ).path,
    "https://catalog.example",
  );
  assert.match(
    list.searchParams.get("filter"),
    /application\/mcp-server-card\+json,application\/vnd.mcp.server\+json/u,
  );
  assert.throws(
    () => requestFor(query({ publisher: "x' OR type = 'evil" }, true), true),
    /invalid_publisher_filter/u,
  );
  const search = requestFor(
    query({ text: "tool", kind: "skill", license: "MIT" }, true),
    true,
  ).body;
  assert.equal(search.federation, "none");
  assert.ok(search.query.filter.type.includes("application/agent-skills+zip"));
  assert.ok(
    search.query.filter.type.includes(
      'text/markdown; profile="urn:air:agent-skills"',
    ),
  );
  assert.equal(search.query.filter["metadata.license"], "MIT");
});
test("metadata JSON exports preserve numeric tokens without claiming original document bytes", () => {
  const original =
    '{ "large":9007199254740993,"negative":-0,"fraction":1.234567890123456789,"nested":[1e+99,{"n":9007199254740995}] }';
  const value = decodeJSON(original);
  assert.equal(typeof value.large, "number");
  const projected = encodeProjection(value);
  for (const token of [
    "9007199254740993",
    "-0",
    "1.234567890123456789",
    "1e+99",
    "9007199254740995",
  ])
    assert.ok(projected.includes(token), token);
  assert.notEqual(
    projected,
    original,
    "Metadata projection does not promise original whitespace or signed bytes",
  );
  value.large = 7;
  assert.equal(JSON.parse(encodeProjection(value)).large, 7);
  assert.throws(() => decodeJSON('{"invalid":'), /invalid_response/u);
  assert.equal(literal("😀".repeat(80), 80).length, 160);
  assert.throws(() => literal("\ud800"));
  assert.equal(paragraph("\u0001"), "\u0001");
  assert.throws(() => paragraph("\0"));
  assert.throws(() => paragraph("\ud800"));
  assert.equal(paragraph("Line one\nLine two"), "Line one\nLine two");
  assert.equal(
    publicItem({
      identifier: "urn:air:x:y:z",
      type: "application/unknown-mcp+json",
    }).kind,
    "unknown",
  );
});
test("local Skill text, Plugin component bytes and projection exports use current release authority", async (t) => {
  const content =
    "---\nname: owned\ndescription: Owned Skill\n---\nInstruction.\n";
  const hash = createHash("sha256").update(content).digest("hex");
  let denied = false,
    native = "skill";
  const nativeSummary = () => ({
    ...summary(),
    resource: { ...summary().resource, kind: native },
  });
  const f = setup({
    handler: (url, _o, response) => {
      if (new URL(url).pathname === `/api/v1/artifacts/${hash}/content`)
        return new Response(content);
      if (new URL(url).pathname.endsWith(id)) {
        if (denied) return response({}, 403);
        return response({
          ...nativeSummary(),
          versionRevision: {
            representations:
              native === "skill"
                ? [
                    {
                      mediaType: "application/agent-skills+md",
                      artifactId: hash,
                      size: Buffer.byteLength(content),
                    },
                  ]
                : [
                    {
                      mediaType: "application/agent-plugins+zip",
                      plugin: {
                        documents: [
                          {
                            path: "plugin.json",
                            content: '{"name":"owned"}\n',
                          },
                        ],
                      },
                    },
                  ],
          },
        });
      }
      return response({ items: [nativeSummary()], total: 1 });
    },
  });
  t.after(() => f.close());
  await f.start();
  await f.browser.search();
  await f.browser.select(f.browser.state.page.items[0].key);
  assert.equal(
    Buffer.from((await f.browser.document(0)).bytes).toString(),
    content,
  );
  native = "plugin";
  await f.browser.search();
  await f.browser.select(f.browser.state.page.items[0].key);
  assert.equal(
    Buffer.from((await f.browser.document(1)).bytes).toString(),
    '{"name":"owned"}\n',
  );
  const shown = projection(f.browser, "control", 1);
  assert.equal(shown.detail.documents[1].exact, true);
  assert.ok(!JSON.stringify(shown).includes("Instruction."));
  denied = true;
  await assert.rejects(f.browser.document(1), /access_refused/u);
  assert.equal(f.browser.state.detail, undefined);
});
test("facets stay local, accept only supported fields and do not mix peer counts", async (t) => {
  let malformed = false;
  const f = setup({
    handler: (url, options, response) => {
      assert.equal(new URL(url).pathname, "/api/v1/catalog/explore");
      const body = JSON.parse(options.body);
      assert.equal(body.query.namespaceId, id);
      assert.equal(body.query.origins, undefined);
      return response({
        facets: malformed
          ? { untrusted: { buckets: [] } }
          : {
              kind: { buckets: [{ value: "agent", count: 2 }], otherCount: 1 },
            },
      });
    },
  });
  t.after(() => f.close());
  await f.start();
  f.browser.state.query = { ...emptyQuery, namespace: id };
  assert.deepEqual(await f.browser.explore(), [
    {
      field: "kind",
      buckets: [
        { value: "agent", count: 2, filter: { field: "kind", value: "agent" } },
      ],
      other: 1,
    },
  ]);
  malformed = true;
  await assert.rejects(f.browser.explore(), /invalid_response/u);
  assert.equal(f.browser.state.facets, undefined);
  f.browser.state.query = { ...emptyQuery, origin: "live" };
  const before = f.calls.length;
  await assert.rejects(f.browser.explore(), /facets_are_local/u);
  assert.equal(f.calls.length, before);
});
test("integrity failures and refused continuations clear detail without replay", async (t) => {
  let corrupt = false;
  const f = setup({
    handler: (url, _o, response) => {
      const path = new URL(url).pathname;
      if (path.includes("artifacts"))
        return new Response(corrupt ? Buffer.from("wrong") : bytes);
      if (path.endsWith(id))
        return response({
          ...summary(),
          versionRevision: {
            representations: [
              {
                mediaType: "application/json",
                size: bytes.length,
                artifactId: digest,
              },
            ],
          },
        });
      return response({ items: [summary()], total: 1 });
    },
  });
  t.after(() => f.close());
  await f.start();
  await f.browser.search();
  await assert.rejects(
    f.browser.search(f.browser.state.query, true),
    /no_next_page/u,
  );
  await f.browser.select(f.browser.state.page.items[0].key);
  corrupt = true;
  await assert.rejects(f.browser.document(0), /document_integrity_failed/u);
  assert.equal(f.browser.state.detail, undefined);
  assert.equal(f.calls.filter((c) => c.url.includes("artifacts")).length, 1);
});
test("consumed projections distinguish missing and empty versions, counts, unsafe links and malformed values", () => {
  const sparse = {
    identifier: "urn:air:example.com:team:sparse",
    score: 10,
    source: "https://peer.example",
  };
  assert.equal(publicItem(sparse).versionPresent, false);
  assert.equal(publicItem({ ...sparse, version: "" }).versionPresent, true);
  assert.equal(
    publicItem({ ...sparse, type: "application/agent-skills+md" }).kind,
    "skill",
  );
  for (const url of [
    "command:evil",
    "javascript:evil",
    "https://name:secret@example.com",
    "https://example.com\\evil",
    "file:///private",
  ]) {
    assert.equal(link(url), undefined);
  }
  assert.equal(
    link("https://docs.example/path?q=x"),
    "https://docs.example/path?q=x",
  );
  assert.throws(
    () =>
      localItem({
        ...summary(),
        listingRevision: { ...summary().listingRevision, tags: [42] },
      }),
    /invalid_response/u,
  );
  const indexed = {
    id: "indexed:abc",
    type: "indexed",
    identifier: sparse.identifier,
    versionPresent: false,
    kind: "plugin",
    displayName: "Plugin",
    tags: [],
    score: 0,
    indexed: {
      sourceIds: [id],
      sourceCount: 1,
      conflict: false,
      sameSourceConflict: false,
    },
  };
  assert.equal(searchItem(indexed).origin, "indexed");
  assert.throws(
    () => searchItem({ ...indexed, version: "" }),
    /invalid_response/u,
  );
  const p = page(
    {
      items: [indexed],
      partial: true,
      diagnostics: [
        { status: "insufficient_evidence", message: "secret ignored" },
      ],
      durableTotal: 4,
      liveCount: 0,
    },
    "/api/v1/catalog/search",
  );
  assert.match(
    p.summary,
    /4 durable matches; 0 live results on this page; partial/u,
  );
  assert.ok(!p.summary.includes("secret"));
  assert.throws(
    () =>
      page(
        { items: [summary(), summary()], total: 2 },
        "/api/v1/catalog/resources",
      ),
    /invalid_response/u,
  );
  assert.throws(() =>
    page(
      { items: [indexed], partial: false, durableTotal: -1, liveCount: 0 },
      "/api/v1/catalog/search",
    ),
  );
});
test("bounded page state, exact lazy reads, raw integrity, reference-only bookmarks and session cleanup", async (t) => {
  const f = setup();
  t.after(() => f.close());
  await f.start();
  await f.browser.search({ ...emptyQuery, limit: 1 });
  assert.equal(
    f.calls.filter((c) => new URL(c.url).pathname.includes(id)).length,
    0,
    "browse must not hydrate detail",
  );
  await f.browser.search(f.browser.state.query, true);
  assert.match(f.calls.at(-1).url, /cursor=opaque%2B%2F%3Dcursor/u);
  assert.equal(f.browser.state.page.items.length, 1);
  await f.browser.select(f.browser.state.page.items[0].key);
  const doc = await f.browser.document(0);
  assert.deepEqual(Buffer.from(doc.bytes), bytes);
  await f.browser.saveBookmark();
  await f.browser.saveBookmark();
  assert.deepEqual(f.browser.state.bookmarks, [{ releaseId: id }]);
  const serialized = JSON.stringify([...f.saved]);
  assert.ok(!serialized.includes("Agent"));
  assert.ok(!serialized.includes("reader"));
  assert.ok(!serialized.includes("script"));
  await f.browser.reopen(0);
  assert.equal(f.browser.state.detail.item.releaseId, id);
  await f.browser.removeBookmark(0);
  assert.equal(f.browser.state.bookmarks.length, 0);
  assert.equal(f.browser.state.detail.item.releaseId, id);
  assert.equal(f.browser.state.detail.item.origin, "local");
  await f.browser.saveBookmark();
  assert.equal(f.browser.state.detail.item.releaseId, id);
  assert.deepEqual(f.browser.state.bookmarks, [{ releaseId: id }]);
  assert.deepEqual(Object.values(f.saved.get("catalogBookmarks")), [
    [{ releaseId: id }],
  ]);
  await f.session.signOut();
  assert.equal(f.browser.state.page, undefined);
  assert.equal(f.browser.state.detail, undefined);
  await assert.rejects(f.browser.search(), /sign_in_required/u);
  assert.equal(f.browser.state.page, undefined);
});
test("late reads cannot reappear after a filter change, session switch or refusal", async (t) => {
  let delayed,
    release,
    refuse = false;
  const f = setup({
    handler: async (url, _options, response) => {
      if (refuse) return response({}, 403);
      if (new URL(url).searchParams.get("kind") === "agent") {
        delayed();
        return new Promise((resolve) => {
          release = () => resolve(response({ items: [summary()], total: 1 }));
        });
      }
      return response({ items: [], total: 0 });
    },
  });
  t.after(() => f.close());
  await f.start();
  const waiting = new Promise((resolve) => {
    delayed = resolve;
  });
  const first = f.browser.search({ ...emptyQuery, kind: "agent" });
  await waiting;
  await f.browser.search({ ...emptyQuery, kind: "plugin" });
  release();
  await assert.rejects(first);
  assert.deepEqual(f.browser.state.page.items, []);
  assert.equal(f.browser.state.query.kind, "plugin");
  refuse = true;
  await assert.rejects(f.browser.search(), /access_refused/u);
  assert.equal(f.browser.state.page, undefined);
});
test("indexed bookmarks reopen the exact absent or empty version and never use administrator APIs", async (t) => {
  let version = "";
  const external = () => ({
    id: "indexed:one",
    type: "indexed",
    identifier: "urn:air:peer.example:team:tool",
    versionPresent: version !== undefined,
    ...(version === undefined ? {} : { version }),
    kind: "mcp_server",
    displayName: "Sparse",
    tags: [],
    score: 0,
    indexed: {
      sourceIds: [id],
      sourceCount: 1,
      conflict: false,
      sameSourceConflict: false,
    },
  });
  const f = setup({
    handler: (url, _options, response) =>
      new URL(url).pathname.endsWith("indexed-record")
        ? response(external())
        : response({
            items: [external()],
            partial: false,
            durableTotal: 1,
            liveCount: 0,
          }),
  });
  t.after(() => f.close());
  await f.start();
  await f.browser.search({ ...emptyQuery, origin: "indexed" });
  await f.browser.select(f.browser.state.page.items[0].key);
  await f.browser.saveBookmark();
  await f.browser.reopen(0);
  const lookup = f.calls.find((c) => c.url.includes("indexed-record"));
  assert.equal(new URL(lookup.url).searchParams.has("version"), true);
  assert.equal(new URL(lookup.url).searchParams.get("version"), "");
  version = undefined;
  await assert.rejects(f.browser.reopen(0), /selection_changed/u);
  assert.equal(f.browser.state.detail, undefined);
  await f.browser.search({ ...emptyQuery, origin: "indexed" });
  await f.browser.select(f.browser.state.page.items[0].key);
  await f.browser.saveBookmark();
  await f.browser.reopen(1);
  const absent = new URL(
    f.calls.filter((c) => c.url.includes("indexed-record")).at(-1).url,
  );
  assert.equal(absent.searchParams.get("versionPresent"), "false");
  assert.equal(absent.searchParams.has("version"), false);
  assert.ok(
    f.calls.every(
      (c) => !c.url.includes("/federation/") && !c.url.includes("/namespaces"),
    ),
  );
});
test("public mode has no Control calls, default peer disclosure, private bookmarks or inferred artifacts", async (t) => {
  const entry = {
    identifier: "urn:air:example.com:team:agent",
    displayName: "Published",
    type: "application/a2a-agent-card+json",
    url: "https://external.example/card",
  };
  const f = setup({
    mode: "published",
    handler: (url, _options, response) =>
      response(
        new URL(url).pathname.endsWith("search")
          ? { results: [entry] }
          : { items: [entry], total: 1 },
      ),
  });
  t.after(() => f.close());
  await f.start();
  await f.browser.search();
  assert.match(f.calls.at(-1).url, /\/ard\/v1\/agents/u);
  await f.browser.select(f.browser.state.page.items[0].key);
  assert.equal(f.browser.state.detail.documents.at(-1).url, entry.url);
  await assert.rejects(
    f.browser.document(1),
    /external_document_needs_confirmation/u,
  );
  await assert.rejects(f.browser.saveBookmark(), /bookmark_unavailable/u);
  assert.ok(f.calls.every((c) => !c.url.includes("/api/v1/")));
  assert.ok(
    f.calls.every((c) => new URL(c.url).hostname === "catalog.example"),
  );
  await f.browser.search({ ...emptyQuery, text: "Published" });
  assert.equal(JSON.parse(f.calls.at(-1).options.body).federation, "none");
});
test("webview boundary rejects extra actions and projects no raw evidence or credentials", () => {
  const b = {
    state: {
      query: emptyQuery,
      busy: false,
      bookmarks: [],
      page: {
        items: [{ ...localItem(summary()), raw: { secret: "sentinel" } }],
        summary: "one",
      },
      detail: {
        item: localItem(summary()),
        references: [
          {
            label: "Site",
            value: "https://docs.example",
            url: "https://docs.example",
          },
        ],
        facts: [],
        documents: [{ label: "Card", value: { token: "sentinel" } }],
      },
    },
  };
  assert.ok(!JSON.stringify(projection(b, "control", 1)).includes("sentinel"));
  assert.deepEqual(message({ type: "ready" }), { type: "ready" });
  assert.equal(
    message({ type: "select", revision: 1, key: "local:one" }).key,
    "local:one",
  );
  for (const input of [
    { type: "fetch", revision: 1, url: "https://evil" },
    { type: "ready", url: "x" },
    { type: "open", revision: 1, index: 0, url: "x" },
    { type: "inspect", revision: 1, index: -1 },
    { type: "search", revision: 1, query: null },
  ])
    assert.throws(() => message(input));
  const markup = html(
    "vscode-webview://local/script.js",
    "vscode-webview://local/style.css",
    "vscode-webview:",
  );
  assert.match(markup, /default-src 'none'/u);
  assert.ok(!markup.includes("unsafe-inline"));
  assert.ok(!markup.includes("https:"));
  b.state.detail.item.artworkTarget = {
    origin: "local",
    listingRevisionId: id,
  };
  b.state.detail.item.artwork = { descriptor: { screenshots: [{}] } };
  assert.deepEqual(projection(b, "control", 2).detail.artwork, [
    { index: 1, label: "Load screenshot 1 from Catalog" },
  ]);
  b.state.detail.item.artwork = {};
  assert.deepEqual(projection(b, "control", 2).detail.artwork, []);
});
test("public rating opinions bind the exact version and stay separate from local metrics", () => {
  const entry = { identifier: "urn:air:peer.example:team:tool", version: "" },
    iri =
      "https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/ratings/v1#summary";
  const rating = {
    reporter: "https://peer.example",
    identifier: entry.identifier,
    versionPresent: true,
    minimum: 1,
    maximum: 5,
    count: 2,
    sum: 9,
  };
  assert.equal(
    publicItem({ ...entry, [iri]: rating }).ratings,
    "Publisher-declared: 4.5 / 5 (2 ratings)",
  );
  for (const invalid of [
    { ...rating, identifier: "urn:air:other:team:tool" },
    { ...rating, versionPresent: false },
    { ...rating, sum: 11 },
    { ...rating, reporter: "javascript:unsafe" },
  ])
    assert.equal(publicItem({ ...entry, [iri]: invalid }).ratings, undefined);
});
test("bookmark ceilings refuse new references and removing the last reference frees its account slot", async (t) => {
  const f = setup();
  t.after(() => f.close());
  await f.start();
  await f.browser.search();
  await f.browser.select(f.browser.state.page.items[0].key);
  await f.browser.saveBookmark();
  const all = f.saved.get("catalogBookmarks"),
    account = Object.keys(all)[0];
  all[account] = Array.from({ length: 100 }, (_, i) => ({
    releaseId: `00000000-0000-7000-8000-${String(i + 2).padStart(12, "0")}`,
  }));
  await assert.rejects(f.browser.saveBookmark(), /bookmark_limit/u);
  all[account] = [{ releaseId: id }];
  await f.browser.search();
  await f.browser.removeBookmark(0);
  assert.equal(Object.hasOwn(f.saved.get("catalogBookmarks"), account), false);
  for (let i = 0; i < 20; i++) all[`other${i}`] = [{ releaseId: id }];
  delete all[account];
  f.saved.set("catalogBookmarks", all);
  await f.browser.search();
  await f.browser.select(f.browser.state.page.items[0].key);
  await assert.rejects(f.browser.saveBookmark(), /bookmark_account_limit/u);
});
test("explicit detail budgets cannot raise the selected connection response ceiling", async (t) => {
  const f = setup({
    handler: (url, _o, response) =>
      response(
        new URL(url).pathname.endsWith(id)
          ? {
              ...summary(),
              versionRevision: {
                representations: [
                  {
                    mediaType: "application/json",
                    data: { large: "x".repeat(2000) },
                  },
                ],
              },
            }
          : { items: [summary()], total: 1 },
      ),
  });
  t.after(() => f.close());
  f.connection.responseBytes = 1024;
  await f.start();
  await f.browser.search();
  await assert.rejects(
    f.browser.select(f.browser.state.page.items[0].key),
    /response_too_large/u,
  );
  assert.equal(f.browser.state.detail, undefined);
});
test("a bookmark reopen cannot replace a newer search between its exact lookup and selection", async (t) => {
  const f = setup({
    handler: (url, _o, response) =>
      new URL(url).pathname.endsWith(id)
        ? response({
            ...summary(),
            versionRevision: {
              representations: [{ mediaType: "application/json", data: {} }],
            },
          })
        : response({
            items:
              new URL(url).searchParams.get("kind") === "plugin"
                ? []
                : [summary()],
            total: 1,
          }),
  });
  t.after(() => f.close());
  await f.start();
  await f.browser.search();
  await f.browser.select(f.browser.state.page.items[0].key);
  await f.browser.saveBookmark();
  let newer,
    once = false;
  const listener = f.browser.onChange(() => {
    if (
      !once &&
      !f.browser.state.busy &&
      f.browser.state.page?.summary === "Exact saved reference"
    ) {
      once = true;
      newer = f.browser.search({ kind: "plugin" });
    }
  });
  t.after(() => listener.dispose());
  await assert.rejects(f.browser.reopen(0), /selection_changed/u);
  await newer;
  assert.equal(f.browser.state.query.kind, "plugin");
  assert.deepEqual(f.browser.state.page.items, []);
});

test("private Published tokens have separate consumer credential bindings without exposing them", async (t) => {
  const f = setup({ mode: "published" });
  t.after(() => f.close());
  const connection = { ...f.connection, auth: "manual" };
  await f.session.select(connection);
  await f.session.manual("owned-private-one");
  const first = f.session.consumerBinding();
  assert.match(first, /^[a-f0-9]{64}$/u);
  await f.session.select(connection);
  assert.equal(f.session.consumerBinding(), first);
  const signal = f.session.cancellation();
  await f.session.manual("owned-private-two");
  assert.notEqual(f.session.consumerBinding(), first);
  assert.equal(signal.aborted, true);
  assert.equal(f.session.cancellation().aborted, false);
});

for (const kind of ["skill", "plugin"])
  test(`complete ${kind} selection rereads current release access and rejects changed or sparse evidence`, async (t) => {
    const { representation } = await import("./package-fixtures.mjs");
    const retained = () =>
      kind === "skill"
        ? representation()
        : {
            ...representation(),
            mediaType: "application/agent-plugins+zip",
            package: undefined,
            plugin: {
              documents: [
                { path: "plugin.json", content: '{"name":"owned.tools"}' },
              ],
            },
          };
    let denied = false,
      changed = false;
    const base = () => ({
      ...summary(),
      resource: { ...summary().resource, kind },
    });
    const f = setup({
      handler: (url, _o, response) =>
        new URL(url).pathname.endsWith(id)
          ? response(
              denied
                ? {}
                : {
                    ...base(),
                    versionRevision: {
                      representations: [
                        { ...retained(), ...(changed ? { size: 1 } : {}) },
                      ],
                    },
                  },
              denied ? 403 : 200,
            )
          : response({ items: [base()], total: 1 }),
    });
    t.after(() => f.close());
    await f.start();
    await f.browser.search();
    await f.browser.select(f.browser.state.page.items[0].key);
    const read = f.browser.packageConsumer(0);
    assert.equal((await read()).size, representation().size);
    changed = true;
    await assert.rejects(read(), /selection_changed/);
    changed = false;
    denied = true;
    await assert.rejects(read(), /access_refused/);
    denied = false;
    const projectionValue = projection(f.browser, "control", 1);
    assert.deepEqual(projectionValue.detail.packages, [
      {
        index: 0,
        label:
          kind === "skill"
            ? "Review and install complete Skill"
            : "Review and install Plugin disabled",
        type: kind,
      },
    ]);
    assert.equal(
      JSON.stringify(projectionValue).includes("expandedSize"),
      false,
    );
    await f.session.signOut();
    await assert.rejects(read(), /connection_changed/);
    assert.throws(
      () => f.browser.packageConsumer(0),
      /complete_skill_package_required/,
    );
  });

test("contract-valid metadata controls and supplementary limits preserve mixed pages and exact evidence", async () => {
  const item = summary("Name\nwith newline");
  item.listingRevision.description = "Description\fwith control";
  item.resource.kind = "plugin";
  item.version.version = "one\ntwo";
  item.listingRevision.tags = ["safe\u001ftext"];
  const p = page(
    {
      items: [
        summary(),
        { ...item, release: { id: id.slice(0, -1) + "2", state: "published" } },
      ],
      total: 2,
    },
    "/api/v1/catalog/resources",
  );
  assert.equal(p.items.length, 2);
  assert.equal(p.items[1].name, item.listingRevision.displayName);
  assert.equal(p.items[1].description, item.listingRevision.description);
  assert.equal(p.items[1].version, item.version.version);
  assert.deepEqual(p.items[1].tags, item.listingRevision.tags);
  assert.equal(localItem(summary("😀".repeat(160))).name, "😀".repeat(160));
  for (const text of ["bad\0name", "bad\ud800name", "a".repeat(161)])
    assert.throws(() => localItem(summary(text)), /invalid_response/);
  for (const text of [
    "bad\0description",
    "bad\ud800description",
    "a".repeat(4001),
  ]) {
    const v = summary();
    v.listingRevision.description = text;
    assert.throws(() => localItem(v), /invalid_response/);
  }
  const external = {
    identifier: "urn:air:example.com:test:tool",
    version: "v\n1",
    displayName: "Peer\nname",
    description: "Peer\fdescription",
    tags: ["tag\u001f"],
    type: "application/mcp-server-card+json",
    metadata: { license: "Declared\flicense" },
  };
  assert.equal(publicItem(external).license, external.metadata.license);
  assert.equal(publicItem(external).version, external.version);
  assert.throws(() =>
    publicItem({ ...external, identifier: "urn:air:example.com:test:bad\n" }),
  );
  const f = setup({
    handler: (url, options, response) => {
      if (url.endsWith("/explore"))
        return response({
          facets: { tag: { buckets: [{ value: "tag\u001f", count: 1 }] } },
        });
      if (url.endsWith(id))
        return response({
          ...item,
          listingRevision: {
            ...item.listingRevision,
            license: "License\ftext",
          },
          versionRevision: {
            representations: [
              {
                mediaType: "application/agent-plugins+zip",
                plugin: {
                  documents: [
                    {
                      path: "skills/owned/SKILL.md",
                      content: "# Header\nBody\f text",
                    },
                  ],
                },
              },
            ],
          },
        });
      return response({ items: [item], total: 1 });
    },
  });
  try {
    await f.start();
    await f.browser.search();
    const detail = await f.browser.select(f.browser.state.page.items[0].key);
    assert.equal(detail.facts[0].value, "License\ftext");
    const doc = await f.browser.document(1);
    assert.equal(Buffer.from(doc.bytes).toString(), "# Header\nBody\f text");
    assert.equal((await f.browser.explore())[0].buckets[0].value, "tag\u001f");
  } finally {
    await f.close();
  }
});

test("one explicit continuation survives temporary next-page and facet failures but not changed authority or evidence", async () => {
  for (const mode of ["control", "published"]) {
    let failure = 0,
      empty = false,
      calls = [];
    const f = setup({
      mode,
      handler: (url, options, response) => {
        if (url.includes("/explore")) return response({}, failure || 503);
        const request = new URL(url),
          cursor = request.searchParams.get(
            mode === "control" ? "cursor" : "pageToken",
          );
        calls.push(cursor);
        if (failure) return response({}, failure);
        return response({
          items: empty
            ? []
            : mode === "control"
              ? [summary()]
              : [{ identifier: "urn:air:example.com:test:agent" }],
          total: 2,
          ...(mode === "control"
            ? { nextCursor: "private-opaque" }
            : { pageToken: "private-opaque" }),
        });
      },
    });
    try {
      await f.start();
      await f.browser.search();
      failure = 503;
      await assert.rejects(
        f.browser.search(f.browser.state.query, true),
        /service_unavailable/,
      );
      assert.equal(f.browser.state.page, undefined);
      assert.equal(f.browser.state.canNext, true);
      assert.equal(
        JSON.stringify(projection(f.browser, mode, 0)).includes(
          "private-opaque",
        ),
        false,
      );
      failure = 0;
      empty = true;
      assert.equal(
        (await f.browser.search(f.browser.state.query, true)).items.length,
        0,
      );
      assert.deepEqual(calls, [null, "private-opaque", "private-opaque"]);
      assert.equal(
        f.browser.state.canNext,
        true,
        "empty page continuation remains usable",
      );
      await assert.rejects(f.browser.explore(), /service_unavailable/);
      assert.equal(f.browser.state.canNext, true);
      failure = 410;
      await assert.rejects(
        f.browser.search(f.browser.state.query, true),
        /query_or_continuation_changed/,
      );
      assert.equal(f.browser.state.canNext, false);
      await assert.rejects(
        f.browser.search(f.browser.state.query, true),
        /no_next_page/,
      );
      failure = 0;
      await f.browser.search();
      failure = 403;
      await assert.rejects(
        f.browser.search(f.browser.state.query, true),
        /access_refused/,
      );
      assert.equal(f.browser.state.canNext, false);
      failure = 0;
      await f.browser.search();
      await f.session.signOut();
      assert.equal(f.browser.state.canNext, false);
    } finally {
      await f.close();
    }
  }
});

test("Explore uses the explicit draft without changing the submitted paging query", async () => {
  for (const mode of ["control", "published"]) {
    let body;
    const f = setup({
      mode,
      handler: (url, options, response) => {
        if (url.endsWith("/explore")) {
          body = JSON.parse(options.body);
          return response({
            facets: {
              [mode === "control" ? "tag" : "tags"]: {
                buckets: [{ value: "draft", count: 2 }],
              },
            },
          });
        }
        return response({ items: [], total: 0 });
      },
    });
    try {
      await f.start();
      await f.browser.search();
      await f.browser.explore({ ...emptyQuery, tag: "draft" });
      assert.deepEqual(
        body.query[mode === "control" ? "tags" : "filter"],
        mode === "control" ? ["draft"] : { tags: "draft" },
      );
      assert.equal(f.browser.state.query.tag, "");
      assert.equal(f.browser.state.facetQuery.tag, "draft");
      const before = f.calls.length;
      await assert.rejects(
        f.browser.explore({ ...emptyQuery, origin: "live" }),
      );
      assert.equal(f.calls.length, before);
    } finally {
      await f.close();
    }
  }
});

test("browse cancellation reaches held verification and sends no later capabilities or resource request", async () => {
  let entered, release;
  const held = new Promise((resolve) => {
    entered = resolve;
  });
  const f = setup({
    verify: async (path, request, response, principal) => {
      if (path.endsWith("/me")) {
        entered();
        await new Promise((resolve) => {
          release = resolve;
          request.signal.addEventListener("abort", resolve, { once: true });
        });
        if (request.signal.aborted) throw request.signal.reason;
        return response(principal);
      }
      return response({ resourceKinds: ["agent"] });
    },
  });
  try {
    await f.start();
    const pending = f.browser.search();
    await held;
    f.browser.cancel();
    await assert.rejects(pending, { code: "request_canceled" });
    assert.equal(f.browser.state.busy, false);
    assert.equal(f.browser.state.error, "request_canceled");
    assert.equal(f.calls.length, 1);
    assert.equal(f.session.connectionState(), "unverified");
  } finally {
    release?.();
    await f.close();
  }
});

test("verified artwork is bounded to the current selection, survives ordinary reads and avoids another download", async () => {
  const png = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]);
  let downloads = 0;
  const f = setup({
    handler: (url, options, response) => {
      if (url.endsWith("/artwork/read")) {
        downloads++;
        return new Response(png, {
          headers: {
            "content-type": "image/png",
            "content-digest": `sha-256=:${createHash("sha256").update(png).digest("base64")}:`,
          },
        });
      }
      if (url.endsWith("/explore")) return response({ facets: {} });
      return response(
        url.endsWith(id)
          ? { ...summary(), versionRevision: { representations: [] } }
          : { items: [summary()], total: 1 },
      );
    },
  });
  try {
    await f.start();
    await f.browser.search();
    await f.browser.select(f.browser.state.page.items[0].key);
    f.browser.state.detail.item.artwork = {
      descriptor: {
        icon: {
          alt: "Alt",
          caption: "Caption",
          rights: { attribution: "Author" },
        },
      },
    };
    f.browser.state.detail.item.artworkTarget = {
      origin: "local",
      listingRevisionId: id,
    };
    const image = await f.browser.image(0);
    await f.browser.explore();
    assert.equal(f.browser.state.image, image);
    assert.equal(await f.browser.image(0), image);
    assert.equal(downloads, 1);
    assert.equal(
      projection(f.browser, "control", 0).image.caption,
      "Caption · Author",
    );
    f.browser.back();
    assert.equal(f.browser.state.image, undefined);
    assert.equal(f.browser.state.detail, undefined);
    await f.session.signOut();
    assert.equal(f.browser.state.image, undefined);
  } finally {
    await f.close();
  }
});

test("metadata directory labels, license declarations and public diagnostics retain their own contracts", () => {
  const value = {
    ...summary(),
    display: {
      publisherName: "Publisher\nname",
      namespaceName: "Namespace\ftext",
      license: "License\ftext",
    },
  };
  const item = localItem(value);
  assert.equal(item.publisher, value.display.publisherName);
  assert.equal(item.namespace, value.display.namespaceName);
  assert.equal(item.license, value.display.license);
  assert.equal(
    publicItem({
      identifier: "urn:air:example.com:test:tool",
      metadata: { license: "x".repeat(9000) },
    }).license.length,
    9000,
  );
  assert.equal(
    publicItem({
      identifier: "urn:air:example.com:test:tool",
      metadata: { license: "" },
    }).license,
    "",
  );
  assert.throws(() =>
    localItem({
      ...value,
      display: { ...value.display, namespaceName: "bad\0" },
    }),
  );
  for (const field of ["license", "category"])
    assert.throws(() => query({ [field]: "   " }, false));
  const result = page(
    {
      items: [],
      partial: true,
      durableTotal: 2,
      liveCount: 0,
      diagnostics: [
        { status: "insufficient_evidence", message: "PRIVATE-SOURCE-CAUSE" },
      ],
      fallbackReason: "indexing",
    },
    "/api/v1/catalog/search",
  );
  assert.match(
    result.summary,
    /insufficient_evidence: live metadata lacks declarations required by these filters/,
  );
  assert.match(result.summary, /2 durable matches; 0 live results/);
  assert.match(result.summary, /lexical fallback: indexing/);
  assert.ok(!result.summary.includes("PRIVATE-SOURCE-CAUSE"));
});

test("Published facets select only exact supported media and keep unsupported buckets inspectable", async () => {
  const f = setup({
    mode: "published",
    handler: (url, options, response) =>
      response({
        facets: {
          type: {
            buckets: [
              { value: "application/mcp-server-card+json", count: 2 },
              { value: "application/fake-mcp+json", count: 1 },
            ],
          },
          tags: { buckets: [{ value: "a".repeat(81), count: 1 }] },
        },
      }),
  });
  try {
    await f.start();
    const facets = await f.browser.explore();
    assert.deepEqual(facets[0].buckets[0].filter, {
      field: "kind",
      value: "mcp_server",
    });
    assert.equal(facets[0].buckets[1].filter, undefined);
    assert.equal(facets[0].buckets[1].value, "application/fake-mcp+json");
    assert.equal(facets[1].buckets[0].filter, undefined);
  } finally {
    await f.close();
  }
});

test("bounded editing drafts may be invalid without weakening submitted query limits", () => {
  const value = { text: "😀".repeat(126), namespace: "partially typed" };
  assert.equal(query(value, false, true, true).text, value.text);
  assert.throws(() => query(value, false));
  assert.throws(() => query({ text: "x".repeat(1001) }, false, true, true));
  assert.throws(() => query({ text: "bad\0" }, false, true, true));
});

test("draft messages require a bounded identity scope; resource actions retain their closed revision contract", () => {
  assert.equal(
    message({ type: "draft", revision: 1, scope: 0, query: {} }).scope,
    0,
  );
  for (const scope of [undefined, -1, 0.5, Number.MAX_SAFE_INTEGER + 1, "0"])
    assert.throws(() =>
      message({ type: "draft", revision: 1, scope, query: {} }),
    );
  assert.throws(() =>
    message({ type: "select", revision: 1, scope: 0, key: "row" }),
  );
});

test("current-page identities and retained document arrays are not shortened by bridge clamps", async () => {
  const prefix = "urn:air:example.com:test:",
    identifier = prefix + "x".repeat(512 - prefix.length);
  for (const version of [
    undefined,
    "",
    "😀".repeat(128),
    "\u0001".repeat(128),
  ]) {
    const item = publicItem({
      identifier,
      ...(version === undefined ? {} : { version }),
    });
    assert.equal(
      message({ type: "select", revision: 1, key: item.key }).key,
      item.key,
    );
    assert.equal(item.versionPresent, version !== undefined);
    if (version !== undefined) assert.equal(item.version, version);
  }
  assert.equal(
    message({ type: "inspect", revision: 1, index: 151 }).index,
    151,
  );
  assert.throws(() =>
    message({
      type: "inspect",
      revision: 1,
      index: Number.MAX_SAFE_INTEGER + 1,
    }),
  );
  assert.throws(() =>
    message({ type: "select", revision: 1, key: "x".repeat(1400) }),
  );
  const value = summary();
  value.resource.kind = "plugin";
  const representations = ["first", "second"].map((group) => ({
    mediaType: "application/agent-plugins+zip",
    plugin: {
      documents: Array.from({ length: 82 }, (_, index) => ({
        path: `skills/s${index}/SKILL.md`,
        content: `Exact ${group} ${index}\nBody`,
      })),
    },
  }));
  const f = setup({
    handler: (url, options, response) =>
      response(
        url.endsWith(id)
          ? { ...value, versionRevision: { representations } }
          : { items: [value], total: 1 },
      ),
  });
  try {
    await f.start();
    await f.browser.search();
    const detail = await f.browser.select(f.browser.state.page.items[0].key);
    assert.equal(detail.documents.length, 166);
    const document = await f.browser.document(151);
    assert.equal(document.label, "skills/s67/SKILL.md");
    assert.equal(
      Buffer.from(document.bytes).toString(),
      "Exact second 67\nBody",
    );
    await assert.rejects(f.browser.document(166), {
      code: "selection_changed",
    });
  } finally {
    await f.close();
  }
});

test("local Search evidence preserves presentation and ratings only for its exact embedded identity", () => {
  const releaseId = "00000000-0000-7000-8000-000000000002",
    listingRevisionId = "00000000-0000-7000-8000-000000000003",
    namespaceId = "00000000-0000-7000-8000-000000000004";
  const local = {
    ...summary(),
    resource: { ...summary().resource, namespaceId },
    listingRevision: { ...summary().listingRevision, id: listingRevisionId },
    release: { ...summary().release, id: releaseId },
    display: {
      publisherName: "Reviewed publisher",
      namespaceName: "Namespace label",
      license: "Apache-2.0",
    },
  };
  const value = {
    id: "local:search-result",
    type: "local",
    identifier: "urn:air:example.com:test:agent",
    version: "1.0.0",
    kind: "agent",
    displayName: "Search title",
    score: 80,
    local,
    publisher: "must not replace the directory label",
    ratings: { count: 2, average: 4.5 },
    artwork: { descriptor: { icon: { alt: "Catalog icon" } } },
  };
  const item = searchItem(value);
  assert.equal(item.releaseId, releaseId);
  assert.equal(item.listingRevisionId, listingRevisionId);
  assert.equal(item.namespaceId, namespaceId);
  assert.equal(item.visibility, "public");
  assert.equal(item.publisher, "Reviewed publisher");
  assert.equal(item.namespace, "Namespace label");
  assert.equal(item.license, "Apache-2.0");
  assert.equal(item.ratings, "4.5 / 5 (2 ratings)");
  assert.deepEqual(item.artworkTarget, {
    origin: "local",
    listingRevisionId,
  });
  for (const changed of [
    { ...local, version: { version: "2.0.0" } },
    {
      ...local,
      resource: {
        ...local.resource,
        canonicalIdentifier: "urn:air:example.com:test:other",
      },
    },
    { ...local, resource: { ...local.resource, kind: "skill" } },
  ]) {
    assert.throws(() => searchItem({ ...value, local: changed }), {
      code: "invalid_response",
    });
  }
  assert.equal(
    localItem({ ...summary(), ratings: { count: 0 } }).ratings,
    "No ratings",
  );
  assert.equal(
    localItem({ ...summary(), ratings: { count: 2, average: 4.5 } }).ratings,
    "4.5 / 5 (2 ratings)",
  );
  for (const ratings of [
    { count: -1, average: 4.5 },
    { count: 2, average: 0 },
    { count: 2, average: 6 },
    { count: 2, average: "4.5" },
    { count: 2, average: Number.NaN },
  ])
    assert.throws(() => localItem({ ...summary(), ratings }), {
      code: "invalid_response",
    });
});

test("live Search projections bind source, version and receipt while labeling peer ratings separately", () => {
  const identifier = "urn:air:peer.example:test:agent";
  for (const ver of [{}, { version: "" }, { version: "1.0.0" }]) {
    const entry = {
      identifier,
      ...ver,
      url: "https://peer.example/metadata.json",
    };
    const value = {
      id: "live:result",
      type: "live",
      identifier,
      ...ver,
      kind: "agent",
      displayName: "Peer agent",
      score: 1,
      live: { sourceId: id, source: "https://peer.example", entry },
      artwork: {
        receipt: "bounded-receipt",
        descriptor: { icon: { alt: "Peer" } },
      },
      ratings: { count: 1, average: 3 },
      peerRatings: { count: 2, average: 4.5, reporter: "https://peer.example" },
    };
    const item = searchItem(value);
    assert.deepEqual(item.raw, entry);
    assert.equal(item.versionPresent, Object.hasOwn(ver, "version"));
    assert.equal(item.version, ver.version);
    assert.equal(
      item.ratings,
      "3.0 / 5 (1 ratings); Peer-reported: 4.5 / 5 (2 ratings)",
    );
    assert.deepEqual(item.references, [
      {
        label: "Responding source",
        value: "https://peer.example",
        url: "https://peer.example/",
      },
      {
        label: "Metadata document",
        value: "https://peer.example/metadata.json",
        url: "https://peer.example/metadata.json",
      },
      {
        label: "Peer rating reporter",
        value: "https://peer.example",
        url: "https://peer.example/",
      },
    ]);
    assert.deepEqual(item.artworkTarget, {
      origin: "live",
      receipt: "bounded-receipt",
      descriptor: { icon: { alt: "Peer" } },
    });
    const changedEntry = Object.hasOwn(ver, "version")
      ? { identifier }
      : { identifier, version: "" };
    for (const live of [
      { ...value.live, source: "https://user:secret@peer.example" },
      { ...value.live, sourceId: "not-an-id" },
      {
        ...value.live,
        entry: { ...entry, identifier: "urn:air:other.example:test:agent" },
      },
      { ...value.live, entry: changedEntry },
    ])
      assert.throws(() => searchItem({ ...value, live }), {
        code: "invalid_response",
      });
  }
});

test("indexed artwork targets require the retained source and generation and preserve absent or empty versions", () => {
  const identifier = "urn:air:peer.example:test:plugin";
  for (const ver of [
    { versionPresent: false },
    { versionPresent: true, version: "" },
  ]) {
    const value = {
      id: "indexed:artwork",
      type: "indexed",
      identifier,
      ...ver,
      kind: "plugin",
      displayName: "Plugin",
      score: 1,
      artwork: { descriptor: {} },
      indexed: {
        sourceIds: [id],
        sourceCount: 1,
        conflict: false,
        sameSourceConflict: false,
        presentationSourceId: id,
        presentationGeneration: 7,
      },
    };
    assert.deepEqual(searchItem(value).artworkTarget, {
      origin: "indexed",
      target: {
        sourceId: id,
        presentationGeneration: 7,
        identifier,
        ...ver,
        kind: "plugin",
      },
    });
    for (const missing of ["presentationSourceId", "presentationGeneration"]) {
      const indexed = { ...value.indexed };
      delete indexed[missing];
      assert.equal(searchItem({ ...value, indexed }).artworkTarget, undefined);
    }
    for (const indexed of [
      { ...value.indexed, presentationSourceId: "not-an-id" },
      { ...value.indexed, presentationGeneration: 1.5 },
    ])
      assert.throws(() => searchItem({ ...value, indexed }), {
        code: "invalid_response",
      });
  }
});

test("artwork screenshot reads use the exact slot and return JPEG bytes with bounded attribution", async (t) => {
  const jpeg = Buffer.from([255, 216, 255, 224, 1, 2, 3]);
  const descriptor = {
    screenshots: [
      {
        alt: "Screenshot alt",
        caption: "Overview",
        rights: { attribution: "Example author" },
      },
    ],
  };
  const f = setup({
    handler: (url, options, response) => {
      if (url.endsWith("/artwork/read")) {
        assert.deepEqual(JSON.parse(options.body), {
          origin: "local",
          listingRevisionId: id,
          slot: "screenshot",
          index: 0,
        });
        return new Response(jpeg, {
          headers: {
            "content-type": "image/jpeg",
            "content-digest": `sha-256=:${createHash("sha256").update(jpeg).digest("base64")}:`,
          },
        });
      }
      return response(
        url.endsWith(id)
          ? { ...summary(), versionRevision: { representations: [] } }
          : { items: [{ ...summary(), artwork: { descriptor } }], total: 1 },
      );
    },
  });
  t.after(() => f.close());
  await f.start();
  await f.browser.search();
  await f.browser.select(f.browser.state.page.items[0].key);
  assert.deepEqual(await f.browser.image(1), {
    index: 1,
    value: `data:image/jpeg;base64,${jpeg.toString("base64")}`,
    alt: "Screenshot alt",
    caption: "Overview · Example author",
  });
  const calls = f.calls.length;
  for (const index of [-1, 9, 1.5, Number.NaN])
    await assert.rejects(f.browser.image(index), {
      code: "artwork_unavailable",
    });
  assert.equal(
    f.calls.length,
    calls,
    "invalid slots send no verification or image request",
  );
});

test("artwork media, magic bytes and response digest are checked before caching a data URL", async (t) => {
  const png = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]);
  let imageBytes = png,
    mime = "image/png",
    digest = `sha-256=:${createHash("sha256").update(png).digest("base64")}:`;
  const f = setup({
    handler: (url, _options, response) => {
      if (url.endsWith("/artwork/read"))
        return new Response(imageBytes, {
          headers: { "content-type": mime, "content-digest": digest },
        });
      return response(
        url.endsWith(id)
          ? { ...summary(), versionRevision: { representations: [] } }
          : {
              items: [
                {
                  ...summary(),
                  artwork: { descriptor: { icon: { alt: "Icon" } } },
                },
              ],
              total: 1,
            },
      );
    },
  });
  t.after(() => f.close());
  await f.start();
  const select = async () => {
    await f.browser.search();
    await f.browser.select(f.browser.state.page.items[0].key);
  };
  await select();
  assert.equal((await f.browser.image(0)).caption, "Native icon declaration");
  for (const [label, body, type, contentDigest, code] of [
    ["unsupported media", png, "image/svg+xml", digest, "invalid_response"],
    [
      "PNG signature",
      Buffer.from("not a PNG"),
      "image/png",
      `sha-256=:${createHash("sha256").update("not a PNG").digest("base64")}:`,
      "invalid_response",
    ],
    ["JPEG signature", png, "image/jpeg", digest, "invalid_response"],
    [
      "digest mismatch",
      png,
      "image/png",
      `sha-256=:${createHash("sha256").update("different image bytes").digest("base64")}:`,
      "document_integrity_failed",
    ],
  ]) {
    await select();
    imageBytes = body;
    mime = type;
    digest = contentDigest;
    await assert.rejects(f.browser.image(0), { code }, label);
    assert.equal(f.browser.state.image, undefined, label);
    assert.equal(f.browser.state.detail, undefined, label);
    assert.equal(f.browser.state.error, code, label);
    assert.equal(f.browser.state.busy, false, label);
  }
});

test("a late artwork response cannot attach private image bytes to a newer search", async (t) => {
  const png = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]);
  let enter, release;
  const entered = new Promise((resolve) => {
    enter = resolve;
  });
  const gate = new Promise((resolve) => {
    release = resolve;
  });
  const f = setup({
    handler: async (url, _options, response) => {
      if (url.endsWith("/artwork/read")) {
        enter();
        await gate;
        return new Response(png, {
          headers: {
            "content-type": "image/png",
            "content-digest": `sha-256=:${createHash("sha256").update(png).digest("base64")}:`,
          },
        });
      }
      if (new URL(url).searchParams.get("kind") === "plugin")
        return response({ items: [], total: 0 });
      return response(
        url.endsWith(id)
          ? { ...summary(), versionRevision: { representations: [] } }
          : {
              items: [
                {
                  ...summary(),
                  artwork: { descriptor: { icon: { alt: "Old icon" } } },
                },
              ],
              total: 1,
            },
      );
    },
  });
  t.after(async () => {
    release();
    await f.close();
  });
  await f.start();
  await f.browser.search();
  await f.browser.select(f.browser.state.page.items[0].key);
  const pending = f.browser.image(0);
  const rejected = assert.rejects(pending, { code: "cancelled" });
  await entered;
  const replacement = await f.browser.search({ ...emptyQuery, kind: "plugin" });
  release();
  await rejected;
  assert.equal(f.browser.state.page, replacement);
  assert.deepEqual(replacement.items, []);
  assert.equal(f.browser.state.query.kind, "plugin");
  assert.equal(f.browser.state.image, undefined);
  assert.equal(f.browser.state.detail, undefined);
  assert.equal(f.browser.state.error, undefined);
  assert.equal(f.browser.state.busy, false);
});
