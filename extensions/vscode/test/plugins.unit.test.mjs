import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile, readFile, readdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { zip, tar, hash } from "./package-fixtures.mjs";
import { gzipSync } from "node:zlib";
const {
  pluginPlan,
  pluginPreview,
  pluginName,
  PluginLocations,
  locationMap,
  SkillStore,
  unpackPackage,
  acquireArchive,
} = await import(process.env.CATALOG_VSCODE_LIB);
import { fixture } from "./plugin-fixtures.mjs";
const binding = hash("owned identity and host");

test("Plugin root and wrapper archives retain exact bytes and normalize only the storage path", () => {
  for (const root of ["", "different-wrapper"]) {
    const f = fixture({}, root);
    assert.equal(f.plan.name, "owned.tools");
    const bytes = unpackPackage(f.plan, f.archive);
    assert.equal(
      bytes.get("owned.tools/plugin.json").bytes.toString(),
      f.entries[0].bytes.toString(),
    );
    assert.equal(pluginPreview(f.plan, bytes).refusals.length, 0);
    const gzip = gzipSync(tar(f.entries));
    assert.equal(
      unpackPackage(
        { ...f.plan, mediaType: "application/agent-plugins+gzip" },
        gzip,
      ).size,
      2,
    );
    assert.throws(() =>
      unpackPackage(
        f.plan,
        zip([
          ...f.entries,
          {
            path: "outside/extra",
            bytes: Buffer.from("bad"),
            executable: false,
          },
        ]),
      ),
    );
  }
  for (const name of [
    "../bad",
    "Bad",
    "bad--name",
    "bad..name",
    "con",
    "a/evil",
  ])
    assert.throws(() => pluginName(name));
  assert.equal(pluginName("a.b-c"), "a.b-c");
});
test("Plugin plans refuse missing, corrupt, mismatched, unsafe and mutable evidence", () => {
  for (const change of [
    (r) => (r.validationState = "invalid"),
    (r) => (r.mediaType = "application/zip"),
    (r) => (r.url = "http://packages.example/plugin.zip"),
    (r) => (r.url = "https://user:password@packages.example/plugin.zip"),
    (r) => (r.size = 17000000),
    (r) => (r.contentDigest = "sha256:bad"),
    (r) => r.plugin.expandedSize++,
    (r) => (r.plugin.rootDirectory = "../unsafe"),
    (r) => (r.plugin.documents = []),
    (r) => r.plugin.documents.push(r.plugin.documents[0]),
    (r) => (r.plugin.files[0].digest = hash("wrong")),
    (r) => (r.plugin.documents[0].content = "{}"),
    (r) => r.plugin.files.push({ ...r.plugin.files[0], path: "../outside" }),
    (r) =>
      r.plugin.documents.push({ path: "not-present", content: "unretained" }),
  ]) {
    const r = fixture().representation;
    change(r);
    assert.throws(() => pluginPlan(r, binding));
  }
});
test("whole Plugin preview shows actual instructions, hooks, MCP processes and unsupported semantics", () => {
  const schema = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json";
  const f = fixture({
    "com.github.copilot/hooks/hooks.json": JSON.stringify({
      hooks: {
        UserPromptSubmit: [{ type: "command", command: "printf harmless" }],
      },
    }),
    "mcp.json": JSON.stringify({
      $schema: schema,
      mcpServers: {
        owned: { type: "stdio", command: "node", args: ["--version"] },
      },
    }),
    "com.github.copilot/commands/hello.md": "Owned command.",
  });
  const preview = pluginPreview(f.plan, unpackPackage(f.plan, f.archive));
  assert.deepEqual(preview.refusals, []);
  for (const text of [
    "Harmless instructions",
    "printf harmless",
    "node",
    "Owned command",
    "ignores hook matcher",
  ])
    assert.ok(preview.text.includes(text));
  for (const [path, value, match] of [
    [
      "com.github.copilot/hooks/hooks.json",
      {
        hooks: {
          UserPromptSubmit: [
            { matcher: "limited", hooks: [{ command: "do work" }] },
          ],
        },
      },
      /matchers/,
    ],
    [
      "mcp.json",
      { mcpServers: { a: { type: "stdio", command: "./bin/tool" } } },
      /relative/,
    ],
    [
      "mcp.json",
      { mcpServers: { a: { type: "stdio", command: "bin/tool" } } },
      /relative/,
    ],
    [
      "mcp.json",
      { mcpServers: { a: { type: "stdio", command: "bin\\tool" } } },
      /relative/,
    ],
    [
      "mcp.json",
      { mcpServers: { a: { type: "stdio", command: "node", cwd: "./data" } } },
      /cwd/,
    ],
    [
      "mcp.json",
      {
        mcpServers: {
          a: { type: "stdio", command: "node", args: ["${PLUGIN_ROOT}/file"] },
        },
      },
      /placeholders/,
    ],
    [
      "mcp.json",
      {
        mcpServers: {
          a: { type: "stdio", command: "node", env: { API_TOKEN: "embedded" } },
        },
      },
      /secret/,
    ],
    [
      "mcp.json",
      {
        mcpServers: {
          a: {
            type: "streamable-http",
            url: "https://peer.example/mcp",
            headers: { Authorization: "secret" },
          },
        },
      },
      /credentials/,
    ],
    ["mcp.json", { mcpServers: { a: { type: "sse" } } }, /SSE/],
    [
      "plugin.json",
      {
        $schema: "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
        name: "owned.tools",
        extensions: { "com.github.copilot": { hooks: "./extra" } },
      },
      /overrides/,
    ],
  ]) {
    const f = fixture({ [path]: JSON.stringify(value) });
    assert.match(
      pluginPreview(f.plan, unpackPackage(f.plan, f.archive)).refusals.join(
        " ",
      ),
      match,
    );
  }
  for (const command of ["node", "/usr/bin/node"]) {
    const accepted = fixture({
      "mcp.json": JSON.stringify({
        mcpServers: { owned: { type: "stdio", command, args: ["--version"] } },
      }),
    });
    assert.deepEqual(
      pluginPreview(
        accepted.plan,
        unpackPackage(accepted.plan, accepted.archive),
      ).refusals,
      [],
    );
  }
  // Check decoded keys/values, so JSON escapes cannot hide native semantics.
  for (const [path, body, expected] of [
    [
      "com.github.copilot/hooks/hooks.json",
      String.raw`{"hooks":{"UserPromptSubmit":[{"mat\u0063her":"limited","hooks":[]}]}}`,
      /matchers/u,
    ],
    [
      "mcp.json",
      String.raw`{"mcpServers":{"owned":{"type":"stdio","command":"node","args":["\u0024{PLUGIN_ROOT}/tool"]}}}`,
      /placeholders/u,
    ],
  ]) {
    const escaped = fixture({ [path]: body });
    assert.match(
      pluginPreview(
        escaped.plan,
        unpackPackage(escaped.plan, escaped.archive),
      ).refusals.join(" "),
      expected,
    );
  }
  const unknown = fixture({
    "plugin.json": JSON.stringify({
      $schema: "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
      name: "owned.tools",
      extensions: { "com.other.client": {} },
    }),
  });
  assert.match(
    pluginPreview(unknown.plan, unpackPackage(unknown.plan, unknown.archive))
      .text,
    /does not load/,
  );
});
test("native configuration writes preserve unrelated keys and refuse stale consent, invalid maps and shadowed settings", async () => {
  let target = { "/other/plugin": true },
    shadowed = false,
    writes = 0;
  const path = "/private/plugin",
    locations = new PluginLocations({
      read: () => ({ target, effective: target, shadowed }),
      write: async (value) => {
        writes++;
        target = value;
      },
    });
  assert.equal(locations.value(path), undefined);
  target["/concurrent/plugin"] = false;
  await locations.set(path, undefined, false, () => {});
  assert.deepEqual(target, {
    "/other/plugin": true,
    "/concurrent/plugin": false,
    [path]: false,
  });
  await assert.rejects(
    locations.set(path, undefined, true, () => {}),
    { code: "plugin_configuration_changed" },
  );
  await assert.rejects(
    locations.set(path, false, true, () => {
      throw Error("identity changed");
    }),
  );
  assert.equal(writes, 1);
  await locations.set(path, false, true, () => {});
  await locations.set(path, true, false, () => {});
  await locations.set(path, false, undefined, () => {});
  assert.deepEqual(target, {
    "/other/plugin": true,
    "/concurrent/plugin": false,
  });
  shadowed = true;
  assert.throws(() => locations.value(path), {
    code: "plugin_configuration_scope_conflict",
  });
  assert.deepEqual(locationMap(undefined), {});
  assert.throws(() => locationMap({ bad: "true" }));
  assert.throws(() =>
    new PluginLocations({
      read: () => ({
        target: {},
        effective: { [path]: true },
        shadowed: false,
      }),
    }).value(path),
  );
  const interrupted = new PluginLocations({
    read: () => ({ target: {}, effective: {}, shadowed: false }),
    write: async () => {
      throw Error("interrupted native write");
    },
  });
  await assert.rejects(interrupted.set(path, undefined, false, () => {}));
  const overwritten = new PluginLocations({
    read: () => ({ target, effective: target, shadowed: false }),
    write: async () => {},
  });
  await assert.rejects(
    overwritten.set(path, undefined, true, () => {}),
    { code: "plugin_configuration_changed" },
  );
});
test("Plugin filesystem ownership shares Skill atomicity without automatic discovery", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "oac-plugin-unit-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const store = new SkillStore(root, "plugin"),
    f = fixture(),
    bytes = unpackPackage(f.plan, f.archive);
  await store.install(f.plan, bytes, async () => {});
  assert.equal(store.destination, join(root, ".oac-catalog/plugins/installed"));
  assert.equal((await store.inspect(f.plan)).identical, true);
  assert.deepEqual(await readdir(root), [".oac-catalog"]);
  assert.equal((await store.content(f.plan.name)).size, 2);
  await assert.rejects(
    new SkillStore(root).install(f.plan, bytes, async () => {}),
    { code: "package_kind_mismatch" },
  );
  const changed = fixture({ "readme.txt": "updated support" });
  let guards = 0;
  await assert.rejects(
    store.install(
      changed.plan,
      unpackPackage(changed.plan, changed.archive),
      async () => {},
      () => {
        if (++guards === 2) throw Error("interrupted after journal");
      },
    ),
  );
  await store.recover();
  assert.equal((await store.inspect(f.plan)).identical, true);
  await store.install(
    changed.plan,
    unpackPackage(changed.plan, changed.archive),
    async () => {},
  );
  assert.equal((await store.inspect(changed.plan)).identical, true);
  const file = join(store.destination, f.plan.name, "plugin.json"),
    original = await readFile(file);
  await writeFile(file, "local edit");
  await assert.rejects(store.remove(f.plan.name), {
    code: "local_skill_changed",
  });
  await writeFile(file, original);
  await store.recover();
  await store.remove(f.plan.name);
  assert.deepEqual(await store.list(), []);
});
test("anonymous archive acquisition closes responses, bounds bytes and rejects redirects, digest mismatch and cancellation", async () => {
  const f = fixture(),
    tools = { timeoutMs: 1000 },
    requests = [];
  let closed = 0;
  const network = (response) => async (settings) => {
    assert.equal(settings.auth, "anonymous");
    assert.equal(settings.caFile, undefined);
    assert.equal(settings.proxyURL, undefined);
    assert.equal(settings.oidc, undefined);
    return {
      fetch: async (url, options) => {
        requests.push({ url, options });
        return response();
      },
      close: async () => {
        closed++;
      },
    };
  };
  const acquire = (
    response,
    plan = f.plan,
    signal = AbortSignal.timeout(1000),
  ) =>
    acquireArchive(
      plan,
      f.plan.sources[0].url,
      tools,
      signal,
      network(response),
    );
  assert.equal(
    (
      await acquire(
        () =>
          new Response(f.archive, {
            headers: {
              "content-type": "application/zip",
              "content-length": String(f.archive.length),
            },
          }),
      )
    ).size,
    2,
  );
  assert.equal(requests[0].options.redirect, "manual");
  assert.deepEqual(requests[0].options.headers, { accept: f.plan.mediaType });
  for (const response of [
    () => new Response(null, { status: 302 }),
    () => new Response(f.archive, { headers: { "content-type": "text/html" } }),
    () =>
      new Response(f.archive, {
        headers: {
          "content-type": "application/zip",
          "content-length": "99999",
        },
      }),
    () =>
      new Response(null, { headers: { "content-type": "application/zip" } }),
  ])
    await assert.rejects(acquire(response));
  await assert.rejects(
    acquire(
      () =>
        new Response(Buffer.concat([f.archive, Buffer.from("extra")]), {
          headers: { "content-type": "application/zip" },
        }),
    ),
    { code: "package_size_limit" },
  );
  await assert.rejects(
    acquire(
      () =>
        new Response(f.archive, {
          headers: { "content-type": "application/zip" },
        }),
      { ...f.plan, archiveDigest: hash("wrong") },
    ),
    { code: "package_integrity_failed" },
  );
  await assert.rejects(
    acquire(() => {
      throw Error("secret upstream error");
    }),
    { code: "package_archive_unavailable" },
  );
  await assert.rejects(
    acquire(() => {}, f.plan, AbortSignal.abort()),
    { code: "package_cancelled" },
  );
  assert.equal(closed, 8);
});

test("late identity loss and uncertain activation writes leave the owned Plugin disabled", async () => {
  const path = "/owned/plugin";
  for (const fail of ["identity", "write"]) {
    let target = { [path]: false, "/other/plugin": true },
      guards = 0,
      writes = 0;
    const locations = new PluginLocations({
      read: () => ({ target, effective: target, shadowed: false }),
      write: async (value) => {
        writes++;
        target = value;
        if (fail === "write" && writes === 1) throw Error("uncertain write");
      },
    });
    await assert.rejects(
      locations.set(path, false, true, () => {
        if (fail === "identity" && ++guards === 2)
          throw Error("identity changed");
      }),
    );
    assert.equal(target[path], false);
    assert.equal(target["/other/plugin"], true);
    assert.equal(writes, 2);
  }
  let ignoredTarget = { [path]: false },
    ignoredWrites = 0;
  const ignoredRollback = new PluginLocations({
    read: () => ({
      target: ignoredTarget,
      effective: ignoredTarget,
      shadowed: false,
    }),
    write: async (value) => {
      if (++ignoredWrites === 1) {
        ignoredTarget = value;
        throw Error("uncertain enable");
      }
      // A successful native promise does not establish that rollback took effect.
    },
  });
  await assert.rejects(
    ignoredRollback.set(path, false, true, () => {}),
    { code: "plugin_disable_required" },
  );
  let target = { [path]: false };
  const broken = new PluginLocations({
    read: () => ({ target, effective: target, shadowed: false }),
    write: async (value) => {
      target = value;
      throw Error("native store unavailable");
    },
  });
  await assert.rejects(
    broken.set(path, false, true, () => {}),
    { code: "plugin_disable_required" },
  );
});

test("Plugin activation excludes concurrent replacements/removal and rechecks the exact selected package", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "oac-plugin-lock-unit-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const store = new SkillStore(root, "plugin"),
    otherWindow = new SkillStore(root, "plugin"),
    original = fixture(),
    changed = fixture({ "readme.txt": "new package" });
  await store.install(
    original.plan,
    unpackPackage(original.plan, original.archive),
    async () => {},
  );
  let enables = 0;
  const enable = async () => {
      enables++;
    },
    entered = Promise.withResolvers(),
    release = Promise.withResolvers();
  const activation = store.activatePlugin(
    original.plan,
    async () => {
      entered.resolve();
      await release.promise;
    },
    enable,
  );
  await entered.promise;
  try {
    await assert.rejects(
      otherWindow.install(
        changed.plan,
        unpackPackage(changed.plan, changed.archive),
        async () => {},
      ),
      { code: "installation_busy_or_interrupted" },
    );
    await assert.rejects(otherWindow.remove(original.plan.name), {
      code: "installation_busy_or_interrupted",
    });
  } finally {
    release.resolve();
  }
  await activation;
  assert.equal(enables, 1);
  assert.equal((await store.inspect(original.plan)).identical, true);
  const updating = Promise.withResolvers(),
    finish = Promise.withResolvers();
  const update = otherWindow.install(
    changed.plan,
    unpackPackage(changed.plan, changed.archive),
    async () => {
      updating.resolve();
      await finish.promise;
    },
  );
  await updating.promise;
  try {
    await assert.rejects(
      store.activatePlugin(original.plan, async () => {}, enable),
      { code: "installation_busy_or_interrupted" },
    );
  } finally {
    finish.resolve();
  }
  await update;
  await assert.rejects(
    store.activatePlugin(original.plan, async () => {}, enable),
    { code: "install_selected_plugin_first" },
  );
  await assert.rejects(
    store.activatePlugin(
      changed.plan,
      async () => {
        throw Error("access revoked");
      },
      enable,
    ),
    /access revoked/u,
  );
  assert.equal(enables, 1);
  await store.activatePlugin(changed.plan, async () => {}, enable);
  assert.equal(enables, 2, "failed operations must release the workspace lock");
  await assert.rejects(
    new SkillStore(root).activatePlugin(changed.plan, async () => {}, enable),
    { code: "package_kind_mismatch" },
  );
});
