import test from "node:test";
import assert from "node:assert/strict";
const {
  query,
  requestFor,
  localOnly,
  hasLive,
  PackageCredentials,
  packageCredential,
  packageAuthorization,
  packageOrigin,
  McpReferences,
  nativeAbsolute,
  sameNativePath,
  privateMode,
  supportedPlatform,
  windowsDirectory,
  nativeEnvironmentAllowed,
  stopPackageProcess,
} = await import(process.env.CATALOG_VSCODE_LIB);
const account = "a".repeat(64),
  other = "b".repeat(64);
test("combined Control filters preserve API arrays, all-category matching and local constraints", () => {
  const q = query(
    {
      origins: ["local", "indexed", "live"],
      kinds: ["skill", "plugin"],
      tags: ["one", "two"],
      categories: ["finance", "support"],
    },
    false,
  );
  const request = requestFor(q, false, "cursor");
  assert.equal(request.path, "/api/v1/catalog/search");
  assert.deepEqual(request.body, {
    text: "",
    origins: ["local", "indexed", "live"],
    kinds: ["skill", "plugin"],
    tags: ["one", "two"],
    categories: ["finance", "support"],
    limit: 20,
    cursor: "cursor",
  });
  assert.equal(localOnly(q), false);
  assert.equal(hasLive(q), true);
  assert.equal(localOnly(query({}, false)), true);
  assert.equal(hasLive(query({}, false)), false);
  for (const value of [
    { origins: [] },
    { origins: ["local", "local"] },
    { origins: ["foreign"] },
    { kinds: ["unknown"] },
    { tags: Array(33).fill("x") },
    { categories: [" "] },
    { categories: Array.from({ length: 17 }, (_, i) => String(i)) },
    { tags: ["😀".repeat(81)] },
  ])
    assert.throws(() => query(value, false));
  assert.throws(
    () =>
      query(
        {
          origins: ["local", "live"],
          namespace: "00000000-0000-7000-8000-000000000001",
        },
        false,
      ),
    /namespace_is_local/u,
  );
  assert.throws(
    () => query({ origins: ["local"] }, true),
    /unsupported_filter/u,
  );
  assert.equal(
    requestFor(query({ kinds: ["agent", "skill"] }, false), false).path,
    "/api/v1/catalog/search",
  );
});
test("package credentials bind the source family, HTTPS origin and account without settings storage", async () => {
  const saved = new Map(),
    credentials = new PackageCredentials({
      keys: async () => [...saved.keys()],
      get: async (key) => saved.get(key),
      store: async (key, value) => saved.set(key, value),
      delete: async (key) => saved.delete(key),
    });
  const git = { type: "git", repository: "https://git.owned.test/repo" },
    oci = {
      type: "oci",
      reference: "registry.owned.test:5443/repo@sha256:" + "a".repeat(64),
    },
    https = { type: "https", url: "https://plugin.owned.test/archive" };
  for (const source of [git, oci, https]) {
    assert.equal(await credentials.get(account, source), undefined);
    const credential = {
      type: source.type,
      origin: packageOrigin(source),
      scheme: "basic",
      username: "reader",
      token: "synthetic-secret",
    };
    await credentials.save(account, credential);
    assert.deepEqual(await credentials.get(account, source), credential);
    assert.equal(await credentials.get(other, source), undefined);
    assert.equal(
      packageAuthorization(credential, source),
      "Basic " + Buffer.from("reader:synthetic-secret").toString("base64"),
    );
    assert.throws(
      () =>
        packageAuthorization(
          { ...credential, origin: "https://another.test" },
          source,
        ),
      /origin_mismatch/u,
    );
    await credentials.save(account, { ...credential, token: "replacement" });
    assert.equal((await credentials.get(account, source)).token, "replacement");
    await credentials.forget(account, source.type, credential.origin);
    assert.equal(await credentials.get(account, source), undefined);
  }
  const bearer = {
    type: "https",
    origin: "https://plugin.owned.test",
    scheme: "bearer",
    token: "synthetic.token",
  };
  assert.equal(packageAuthorization(bearer, https), "Bearer synthetic.token");
  for (const value of [
    { ...bearer, type: "git" },
    { ...bearer, username: "forbidden" },
    { ...bearer, token: "bad\r\n" },
    { ...bearer, origin: "http://localhost" },
    { ...bearer, unknown: true },
    { ...bearer, scheme: "basic", username: "bad:user" },
    { ...bearer, token: "a".repeat(8193) },
  ])
    assert.throws(() => packageCredential(value));
  assert.throws(() =>
    packageOrigin({
      ...https,
      url: "https://user:secret@example.test/archive",
    }),
  );
  await assert.rejects(credentials.save("bad", bearer));
  assert.equal(saved.size, 0);
});
test("inert MCP references survive reload without storing documents, queries or executable recipes", async () => {
  const store = new Map(),
    state = {
      get: (key) => store.get(key),
      update: async (key, value) => store.set(key, structuredClone(value)),
    };
  const references = new McpReferences(state),
    ref = {
      reference: JSON.stringify({
        origin: "local",
        identifier: "urn:air:owned.test:team:mcp",
        versionPresent: true,
        version: "1",
        releaseId: "00000000-0000-7000-8000-000000000001",
      }),
      document: 0,
      alternative: 0,
      digest: "a".repeat(64),
      mode: "control",
    };
  await references.add(account, ref, () => {});
  await references.add(account, ref, () => {});
  const reloaded = new McpReferences(state);
  assert.deepEqual(reloaded.list(account), [ref]);
  assert.deepEqual(reloaded.list(other), []);
  const copy = reloaded.list(account);
  copy[0].digest = "changed";
  assert.equal(reloaded.list(account)[0].digest, ref.digest);
  assert.throws(() =>
    references.add(account, { ...ref, query: "private" }, () => {}),
  );
  assert.throws(() =>
    references.add(account, { ...ref, alternative: 64 }, () => {}),
  );
  await assert.rejects(
    references.add(account, ref, () => {
      throw new Error("account changed");
    }),
  );
  await references.remove(account, ref, () => {});
  assert.deepEqual(references.list(account), []);
  await references.add(
    account,
    { ...ref, mode: "published", reference: "fresh-search-required" },
    () => {},
  );
  await references.forget();
  assert.deepEqual(references.list(account), []);
  store.set("catalogMcpReferences", { bad: [] });
  assert.throws(() => references.list(account));
});
test("removing the last inert MCP reference frees its account slot without losing other references", async () => {
  const store = new Map(),
    state = {
      get: (key) => store.get(key),
      update: async (key, value) => store.set(key, structuredClone(value)),
    },
    references = new McpReferences(state),
    ref = {
      reference: "fresh-search-required",
      document: 0,
      alternative: 0,
      digest: "a".repeat(64),
      mode: "published",
    },
    another = { ...ref, alternative: 1 },
    accounts = Array.from({ length: 21 }, (_, i) =>
      i.toString(16).padStart(64, "0"),
    );
  for (const binding of accounts.slice(0, 20))
    await references.add(binding, ref, () => {});
  await references.add(accounts[0], another, () => {});
  await assert.rejects(
    references.add(accounts[20], ref, () => {}),
    { code: "mcp_reference_account_limit" },
  );
  await references.remove(accounts[0], ref, () => {});
  assert.deepEqual(references.list(accounts[0]), [another]);
  await assert.rejects(
    references.add(accounts[20], ref, () => {}),
    { code: "mcp_reference_account_limit" },
  );
  await assert.rejects(
    references.remove(accounts[0], another, () => {
      throw new Error("account changed");
    }),
    /account changed/u,
  );
  assert.deepEqual(references.list(accounts[0]), [another]);
  await references.remove(accounts[0], another, () => {});
  await references.remove(accounts[0], another, () => {});
  await references.add(accounts[20], ref, () => {});
  const reloaded = new McpReferences(state);
  assert.deepEqual(reloaded.list(accounts[0]), []);
  for (const binding of accounts.slice(1))
    assert.deepEqual(reloaded.list(binding), [ref]);
  // Empty bindings left by an earlier removal must not exhaust the capacity.
  store.set(
    "catalogMcpReferences",
    Object.fromEntries(accounts.slice(0, 20).map((binding) => [binding, []])),
  );
  await reloaded.add(accounts[20], ref, () => {});
  assert.deepEqual(reloaded.list(accounts[20]), [ref]);
});
test("portable platform policy admits native paths and keeps archive paths separate", async () => {
  for (const value of [
    "C:\\Tools\\git.exe",
    "D:/Trust/ca.pem",
    "\\\\server\\share\\tools\\oras.exe",
  ])
    assert.equal(nativeAbsolute(value, "win32"), true);
  for (const value of [
    "C:relative",
    "\\root-relative",
    "/root-relative",
    "\\\\?\\C:\\device",
    "\\\\.\\pipe\\named",
    "git.exe",
  ])
    assert.equal(nativeAbsolute(value, "win32"), false);
  assert.equal(nativeAbsolute("/opt/tools/git", "darwin"), true);
  assert.equal(nativeAbsolute("relative", "linux"), false);
  assert.equal(sameNativePath("C:\\Tools\\Git", "c:/tools/git", "win32"), true);
  assert.equal(sameNativePath("/Tools", "/tools", "darwin"), false);
  assert.equal(
    privateMode({ mode: 0o600, uid: process.getuid() }, false, false, "linux"),
    true,
  );
  assert.equal(
    privateMode({ mode: 0o666, uid: process.getuid() }, false, false, "linux"),
    false,
  );
  assert.equal(
    privateMode({ mode: 0o666, uid: 0 }, false, false, "win32"),
    true,
    "Windows uses a separate DACL boundary",
  );
  for (const platform of ["linux", "darwin", "win32"])
    assert.equal(supportedPlatform(platform), true);
  assert.equal(supportedPlatform("freebsd"), false);
  await windowsDirectory("/never-executed-on-Windows", false);
});

test("validated filters and persisted references capture immutable selections", async () => {
  const values = ["local", "live"],
    tags = ["one"];
  const q = query({ origins: values, tags }, false);
  values.pop();
  tags[0] = "changed";
  assert.deepEqual(q.origins, ["local", "live"]);
  assert.deepEqual(q.tags, ["one"]);
  let stored;
  const references = new McpReferences({
    get: () => stored,
    update: async (_, v) => {
      stored = structuredClone(v);
    },
  });
  const ref = {
    reference: "fresh-search-required",
    mode: "published",
    document: 0,
    alternative: 0,
    digest: "a".repeat(64),
  };
  const saving = references.add(account, ref, () => {});
  ref.digest = "b".repeat(64);
  await saving;
  assert.equal(references.list(account)[0].digest, "a".repeat(64));
  for (const value of ["\\\\server\\share", "//server/share"])
    assert.equal(nativeAbsolute(value, "win32"), true);
  for (const key of ["Path", "SystemRoot", "TEMP", "Pathext", "ComSpec"])
    assert.equal(nativeEnvironmentAllowed(key, "win32"), true);
  for (const key of ["CATALOG_TOKEN", "SSH_AUTH_SOCK", "DOCKER_AUTH_CONFIG"])
    assert.equal(nativeEnvironmentAllowed(key, "win32"), false);
});
test("offline bounded package forgetting keeps other credential namespaces and reports partial failures", async () => {
  const data = new Map([["openAgenticCatalog.login", "Catalog sentinel"]]);
  let fail;
  const secrets = {
    keys: async () => [...data.keys()],
    get: async (key) => data.get(key),
    store: async (key, value) => data.set(key, value),
    delete: async (key) => {
      if (key === fail) throw new Error("denied");
      data.delete(key);
    },
  };
  const store = new PackageCredentials(secrets);
  for (let i = 0; i < 50; i++)
    await store.save(account, {
      type: "git",
      origin: `https://host${i}.owned.test`,
      scheme: "basic",
      username: "reader",
      token: "synthetic",
    });
  await assert.rejects(
    store.save(account, {
      type: "git",
      origin: "https://extra.owned.test",
      scheme: "basic",
      username: "reader",
      token: "synthetic",
    }),
    /package_credential_limit/u,
  );
  await store.save(account, {
    type: "git",
    origin: "https://host0.owned.test",
    scheme: "basic",
    username: "reader",
    token: "replacement",
  });
  fail = [...data.keys()][1];
  assert.deepEqual(await store.forgetAll(() => true), {
    deleted: 0,
    failed: 0,
    remaining: 50,
  });
  assert.deepEqual(await store.forgetAll(), {
    deleted: 49,
    failed: 1,
    remaining: 1,
  });
  fail = undefined;
  assert.deepEqual(await store.forgetAll(), {
    deleted: 1,
    failed: 0,
    remaining: 0,
  });
  assert.deepEqual(
    [...data],
    [["openAgenticCatalog.login", "Catalog sentinel"]],
  );
});

test("owned POSIX process cleanup tolerates an exited group but reports other failures", async (t) => {
  const calls = [];
  t.mock.method(process, "kill", (pid, signal) => {
    calls.push([pid, signal]);
    throw Object.assign(new Error("gone"), { code: "ESRCH" });
  });
  await stopPackageProcess(987654321);
  assert.deepEqual(calls, [[-987654321, "SIGKILL"]]);
  process.kill.mock.mockImplementation(() => {
    throw Object.assign(new Error("denied"), { code: "EPERM" });
  });
  await assert.rejects(stopPackageProcess(987654321), { code: "EPERM" });
});
