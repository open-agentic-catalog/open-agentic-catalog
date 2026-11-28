import test from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { CatalogMcp, definitions, window } from "./mcp-editor.mjs";

const accounts = ["a".repeat(64), "b".repeat(64)];
const key = "catalogMcpReferences";
function fixture(t) {
  let account = accounts[0],
    epoch = 0;
  const listeners = new Set(),
    data = new Map([["other", "unrelated"]]),
    secretValues = new Map([["mcp-target:unrelated", "keep"]]);
  const store = {
    get: (k) => data.get(k),
    update: async (k, v) => {
      if (v === undefined) data.delete(k);
      else data.set(k, structuredClone(v));
    },
  };
  const item = {
    origin: "local",
    identifier: "urn:air:example:owned",
    key: "owned",
    versionPresent: true,
    version: "1",
    releaseId: "00000000-0000-7000-8000-000000000001",
  };
  const card = {
    name: "io.example/owned",
    description: "Owned fixture",
    version: "1",
    remotes: [{ type: "streamable-http", url: "https://owned.invalid/mcp" }],
    $schema:
      "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json",
  };
  const browser = {
    state: { detail: { item } },
    consumer: () => async () => ({
      mediaType: "application/mcp-server-card+json",
      bytes: Buffer.from(JSON.stringify(card)),
    }),
    openReference: async () => assert.fail("unexpected release read"),
  };
  const session = {
    connection: () => ({ mode: "control", allowLoopbackHTTP: false }),
    test: async () => {},
    guard: () => {
      const saved = epoch;
      return () => assert.equal(epoch, saved, "connection_changed");
    },
    consumerBinding: () => account,
    cancellation: () => new AbortController().signal,
    onChange: (callback) => {
      listeners.add(callback);
      return { dispose: () => listeners.delete(callback) };
    },
  };
  const secrets = {
    get: async (k) => secretValues.get(k),
    delete: async (k) => secretValues.delete(k),
    store: async (k, v) => secretValues.set(k, v),
  };
  Object.assign(window, {
    showQuickPick: async (items) => items[0],
    showWarningMessage: async (_text, _options, yes) => yes,
    showInformationMessage: () => {},
  });
  const host = new CatalogMcp(
    { globalState: store, secrets },
    browser,
    session,
  );
  t.after(() => host.dispose());
  return {
    host,
    store,
    data,
    secretValues,
    card,
    item,
    session,
    listeners,
    switchAccount() {
      epoch++;
      account = account === accounts[0] ? accounts[1] : accounts[0];
      for (const fn of listeners) fn();
    },
  };
}

test("a refused or pending save never exposes a new definition or displaces an existing one", async (t) => {
  const f = fixture(t);
  f.card.remotes[0].headers = [{ name: "Authorization", isSecret: true }];
  await f.host.select(0);
  const original = definitions(),
    originalReferences = structuredClone(f.data.get(key));
  const targetKey = "mcp-target:" + original[0].version + ":Authorization";
  f.secretValues.set(targetKey, "existing target input");
  const entered = Promise.withResolvers(),
    release = Promise.withResolvers(),
    write = f.store.update;
  f.store.update = async () => {
    entered.resolve();
    return release.promise;
  };
  f.card.description = "Another declaration";
  const attempt = f.host.select(0);
  await entered.promise;
  assert.deepEqual(definitions(), original);
  release.reject(new Error("write refused"));
  await assert.rejects(attempt, /write refused/);
  assert.deepEqual(definitions(), original);
  assert.deepEqual(f.data.get(key), originalReferences);
  f.card.description = "Owned fixture";
  await assert.rejects(f.host.select(0), /write refused/);
  assert.deepEqual(definitions(), original);
  f.store.update = write;
  const ref = originalReferences[accounts[0]][0];
  const refs = Array.from({ length: 19 }, (_, i) => ({
    ...ref,
    digest: i.toString(16).padStart(64, "0"),
  }));
  f.data.set(key, { [accounts[0]]: [ref, ...refs] });
  f.card.description = "Another declaration";
  await assert.rejects(f.host.select(0), { code: "mcp_reference_limit" });
  assert.deepEqual(definitions(), original);
  assert.equal(f.data.get(key)[accounts[0]].length, 20);
  f.data.set(key, originalReferences);
  await f.host.select(0);
  assert.equal(definitions().length, 2);
  assert.deepEqual(definitions()[0], original[0]);
  assert.equal(f.data.get(key)[accounts[0]].length, 2);
  assert.deepEqual(
    [...f.secretValues],
    [
      ["mcp-target:unrelated", "keep"],
      [targetKey, "existing target input"],
    ],
  );
});

test("a late save cannot register a definition for the next account", async (t) => {
  const f = fixture(t),
    entered = Promise.withResolvers(),
    release = Promise.withResolvers(),
    write = f.store.update;
  f.store.update = async (...args) => {
    entered.resolve();
    await release.promise;
    await write(...args);
  };
  const saving = f.host.select(0);
  await entered.promise;
  f.switchAccount();
  release.resolve();
  await assert.rejects(saving, /connection_changed/);
  assert.deepEqual(definitions(), []);
  assert.deepEqual(Object.keys(f.data.get(key)), [accounts[0]]);
});

test("saved private pickers cancel at either step without reading or changing references", async (t) => {
  const f = fixture(t);
  await f.host.select(0);
  const before = structuredClone(f.data.get(key));
  for (const stage of [0, 1]) {
    let picks = 0;
    const entered = Promise.withResolvers();
    window.showQuickPick = async (items, _options, token) => {
      if (picks++ < stage) return items[0];
      const closed = Promise.withResolvers();
      token.onCancellationRequested(() => closed.resolve(undefined));
      entered.resolve();
      return closed.promise;
    };
    const result = f.host.saved();
    await entered.promise;
    f.switchAccount();
    await assert.rejects(result, /connection_changed/);
    assert.deepEqual(f.data.get(key), before);
    assert.equal(f.listeners.size, 1);
    assert.deepEqual(definitions(), []);
    f.switchAccount();
  }
  f.session.test = async () => {
    throw Error("access refused");
  };
  window.showQuickPick = () => assert.fail("private picker after refusal");
  await assert.rejects(f.host.saved(), /access refused/);
});

test("removing one row and offline forgetting preserve other owned state", async (t) => {
  const f = fixture(t);
  await f.host.select(0);
  f.card.description = "Second";
  await f.host.select(0);
  f.switchAccount();
  await f.host.select(0);
  f.switchAccount();
  const before = structuredClone(f.data.get(key));
  window.showQuickPick = async (items) =>
    items.find((i) => i.value === "remove") ?? items[0];
  await f.host.saved();
  assert.deepEqual(f.data.get(key), {
    [accounts[0]]: before[accounts[0]].slice(1),
    [accounts[1]]: before[accounts[1]],
  });
  await f.host.select(0);
  const active = definitions();
  f.data.set(key, { bad: [] });
  await assert.rejects(f.host.saved(), { code: "invalid_mcp_references" });
  await assert.rejects(f.host.select(0), { code: "invalid_mcp_references" });
  f.session.test = () => assert.fail("offline forget contacted the Catalog");
  window.showWarningMessage = async () => undefined;
  await f.host.forgetReferences();
  assert.deepEqual(f.data.get(key), { bad: [] });
  window.showWarningMessage = async (_text, _options, confirm) => confirm;
  const write = f.store.update;
  f.store.update = async () => {
    throw Error("clear refused");
  };
  await assert.rejects(f.host.forgetReferences(), /clear refused/);
  f.store.update = write;
  await f.host.forgetReferences();
  assert.equal(f.data.has(key), false);
  assert.equal(f.data.get("other"), "unrelated");
  assert.deepEqual(definitions(), active);
  assert.deepEqual([...f.secretValues], [["mcp-target:unrelated", "keep"]]);
});

test("a Control live choice retains only enough metadata to require a fresh search", async (t) => {
  const f = fixture(t);
  f.item.origin = "live";
  await f.host.select(0);
  const ref = {
    mode: "published",
    reference: "fresh-search-required",
    document: 0,
    alternative: 0,
    digest: createHash("sha256").update(JSON.stringify(f.card)).digest("hex"),
  };
  assert.deepEqual(f.data.get(key), { [accounts[0]]: [ref] });
  const active = definitions(),
    messages = [];
  window.showInformationMessage = (text) => messages.push(text);
  await f.host.saved();
  assert.match(messages[0], /Search explicitly/);
  assert.deepEqual(definitions(), active);
});

test("confirmed offline forgetting completes after an earlier write", async (t) => {
  const f = fixture(t),
    entered = Promise.withResolvers(),
    release = Promise.withResolvers(),
    write = f.store.update;
  f.store.update = async (k, v) => {
    if (v !== undefined) {
      entered.resolve();
      await release.promise;
    }
    await write(k, v);
  };
  const saving = f.host.select(0);
  await entered.promise;
  const forgetting = f.host.forgetReferences();
  release.resolve();
  await Promise.all([saving, forgetting]);
  assert.equal(f.data.has(key), false);
  assert.equal(definitions().length, 1);
});
