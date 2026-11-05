import test from "node:test";
import assert from "node:assert/strict";
import {
  mkdtemp,
  mkdir,
  readFile,
  readdir,
  rm,
  stat,
  symlink,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
const { executionHost, credentialAccess } = await import(
  new URL("host.js", pathToFileURL(process.env.CATALOG_VSCODE_LIB))
);

test("host-local credential access excludes competing writes and requires explicit abandoned-lock recovery", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "catalog-credential-access-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const first = credentialAccess(directory),
    second = credentialAccess(directory);
  let enter, release;
  const entered = new Promise((done) => {
      enter = done;
    }),
    held = new Promise((done) => {
      release = done;
    });
  let changed = 0;
  const writing = first.run("owned-key", async () => {
    enter();
    await held;
    changed++;
  });
  await entered;
  await assert.rejects(
    second.run("owned-key", async () => {
      changed++;
    }),
    /credential_in_use/u,
  );
  assert.equal(changed, 0);
  assert.equal((await readdir(directory)).length, 1);
  release();
  await writing;
  await second.run("owned-key", async () => {
    changed++;
  });
  assert.equal(changed, 2);
  assert.deepEqual(await readdir(directory), []);
  await assert.rejects(
    first.run(
      "owned-key",
      async () => {
        throw new Error("refused");
      },
      true,
    ),
    /refused/u,
  );
  const lock = (await readdir(directory))[0];
  assert.match(
    lock,
    /\.lock$/u,
    "an uncertain writer is never automatically stolen",
  );
  let cleared = false;
  await second.recover("owned-key", async () => {
    cleared = true;
    assert.equal((await readdir(directory)).length, 1);
  });
  assert.equal(cleared, true);
  assert.ok(
    (await readdir(directory)).every((name) => name.endsWith(".state")),
  );
  assert.equal(await second.current("owned-key", undefined), true);
  assert.equal(await second.current("owned-key", "stale-secret"), false);
  await first.run("owned-key", async () =>
    first.record("owned-key", "private-token-sentinel"),
  );
  assert.equal(
    await second.current("owned-key", "private-token-sentinel"),
    true,
  );
  assert.equal(await second.current("owned-key", "stale-secret"), false);
  const state = (await readdir(directory)).find((name) =>
    name.endsWith(".state"),
  );
  assert.match(
    await readFile(join(directory, state), "utf8"),
    /^[a-f0-9]{64}$/u,
  );
  await writeFile(join(directory, state), "x".repeat(64));
  await assert.rejects(
    second.current("owned-key", "private-token-sentinel"),
    /host_storage_unavailable/u,
  );
  await writeFile(join(directory, lock), "not an owned lock");
  await assert.rejects(
    second.recover("owned-key", async () => assert.fail()),
    /host_storage_unavailable/u,
  );
});
const { Session, connections, binding } = await import(
  pathToFileURL(process.env.CATALOG_VSCODE_LIB)
);
test("native notifications distinguish stale reads from committed credential changes", async (t) => {
  const directory = await mkdtemp(
    join(tmpdir(), "catalog-native-notification-"),
  );
  t.after(() => rm(directory, { recursive: true, force: true }));
  const access = credentialAccess(directory),
    data = new Map(),
    requests = [];
  const connection = connections([
    {
      id: "local",
      label: "Local",
      origin: "https://catalog.example",
      mode: "control",
      auth: "manual",
    },
  ])[0];
  const key = "openAgenticCatalog:host:local";
  let stale = false,
    cached,
    writes = 0;
  const session = new Session(
    {
      get: async (key) => (stale ? cached : data.get(key)),
      store: async (key, value) => {
        writes++;
        data.set(key, value);
      },
      delete: async (key) => {
        writes++;
        data.delete(key);
      },
    },
    "host",
    async () => ({
      fetch: async (url, init) => {
        const request = url instanceof Request ? url : new Request(url, init);
        requests.push(request.headers.get("authorization"));
        return new Response(
          JSON.stringify(
            request.url.endsWith("/me")
              ? {
                  tenantId: "00000000-0000-7000-8000-000000000001",
                  realmId: "00000000-0000-7000-8000-000000000002",
                  membershipId: "00000000-0000-7000-8000-000000000003",
                  subject: "reader",
                  permissions: ["resource.read"],
                  availablePermissions: ["resource.read"],
                }
              : { resourceKinds: ["agent"] },
          ),
          { headers: { "content-type": "application/json" } },
        );
      },
      close: async () => {},
    }),
    access,
  );
  t.after(() => session.dispose());
  await session.select(connection);
  await session.manual("accepted-token");
  await session.test();
  const accepted = data.get(key),
    guard = session.guard(connection),
    signal = session.cancellation();
  stale = true;
  for (cached of [
    undefined,
    JSON.stringify({
      binding: binding(connection, "host"),
      accessToken: "previous-token",
    }),
  ]) {
    await session.secretsChanged(key);
    guard();
    assert.equal(
      signal.aborted,
      false,
      "A stale native read must not cancel current work",
    );
    assert.equal(session.snapshot().identity.subject, "reader");
    await session.test();
    assert.equal(requests.at(-1), "Bearer accepted-token");
    assert.equal(data.get(key), accepted);
    assert.equal(
      writes,
      1,
      "Notification handling must not rewrite credentials",
    );
  }
  stale = false;
  const replacement = JSON.stringify({
    binding: binding(connection, "host"),
    accessToken: "replacement-token",
  });
  data.set(key, replacement);
  await access.record(key, replacement);
  await session.secretsChanged(key);
  assert.throws(guard, /connection_changed/);
  assert.equal(signal.aborted, true);
  await session.test();
  assert.equal(requests.at(-1), "Bearer replacement-token");
  const replacedGuard = session.guard(connection);
  // Another window has committed again, while this native cache still has an
  // earlier token. Neither stale value may become current or erase that write.
  stale = true;
  cached = replacement;
  const latest = JSON.stringify({
    binding: binding(connection, "host"),
    accessToken: "latest-token",
  });
  data.set(key, latest);
  await access.record(key, latest);
  await session.secretsChanged(key);
  assert.throws(replacedGuard, /connection_changed/);
  await assert.rejects(session.test(), /sign_in_required/);
  assert.equal(data.get(key), latest);
  assert.equal(await access.current(key, latest), true);
  stale = false;
  await session.select(connection);
  const latestGuard = session.guard(connection);
  data.delete(key);
  await access.record(key, undefined);
  await session.secretsChanged(key);
  assert.throws(latestGuard, /connection_changed/);
  await assert.rejects(session.test(), /sign_in_required/);
  assert.equal(writes, 1);

  // A first-use store without a generation file must still honor deletion.
  const [generation] = (await readdir(directory)).filter((name) =>
    name.endsWith(".state"),
  );
  await rm(join(directory, generation));
  data.set(key, accepted);
  await session.select(connection);
  const unrecordedGuard = session.guard(connection);
  data.delete(key);
  await session.secretsChanged(key);
  assert.throws(unrecordedGuard, /connection_changed/);
  await assert.rejects(session.test(), /sign_in_required/);

  // A broken generation record is a storage failure, not permission to retain
  // an active credential after an unverified notification.
  await session.manual("accepted-again");
  const brokenGuard = session.guard(connection);
  await writeFile(join(directory, generation), "broken");
  await assert.rejects(session.secretsChanged(key), /host_storage_unavailable/);
  assert.throws(brokenGuard, /connection_changed/);
  await assert.rejects(session.test(), /sign_in_required/);
});
test("a stale native credential cache cannot refresh after another window committed a generation", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "catalog-native-generation-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const access = credentialAccess(directory),
    data = new Map();
  const connection = connections([
    {
      id: "local",
      label: "Local",
      origin: "https://catalog.example",
      mode: "control",
      auth: "oidc",
      oidc: {
        issuer: "https://issuer.example",
        clientId: "client",
        audience: "catalog",
        scopes: ["openid"],
        refresh: true,
      },
    },
  ])[0];
  const key = "openAgenticCatalog:host:local";
  const stale = JSON.stringify({
    binding: binding(connection, "host"),
    accessToken: "old-token",
    refreshToken: "old-refresh",
    subject: "reader",
    nonce: "nonce",
    expiresAt: Date.now() + 30000,
  });
  data.set(key, stale);
  await access.run(key, () => access.record(key, "newer-native-storage-value"));
  const session = new Session(
    {
      get: async (key) => data.get(key),
      store: async (key, value) => {
        data.set(key, value);
      },
      delete: async (key) => {
        data.delete(key);
      },
    },
    "host",
    async () => ({
      fetch: async () =>
        assert.fail("a stale cache must not spend the old refresh token"),
      close: async () => {},
    }),
    access,
  );
  t.after(() => session.dispose());
  await session.select(connection);
  await assert.rejects(session.test(), /credential_update_pending/u);
  assert.equal(
    data.get(key),
    stale,
    "never delete another window's current storage through a stale cache",
  );
  assert.equal(await access.current(key, "newer-native-storage-value"), true);
  await session.recoverCredentials();
  assert.equal(data.size, 0);
  assert.equal(await access.current(key, undefined), true);
});

test("invalid cached credentials cannot clear another window's accepted replacement", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "catalog-stale-cleanup-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const access = credentialAccess(directory),
    data = new Map();
  const current = connections([
    {
      id: "local",
      label: "Current",
      origin: "https://catalog.example",
      mode: "control",
      auth: "manual",
    },
  ])[0];
  const previous = { ...current, origin: "https://previous.example" };
  const key = "openAgenticCatalog:host:local";
  const native = {
    get: async (key) => data.get(key),
    store: async (key, value) => {
      data.set(key, value);
    },
    delete: async (key) => {
      data.delete(key);
    },
  };
  const writer = new Session(
    native,
    "host",
    async () => ({
      fetch: async (url, options) => {
        const request = new Request(url, options);
        assert.equal(
          request.headers.get("authorization"),
          "Bearer accepted-replacement",
        );
        return new Response(
          JSON.stringify(
            new URL(request.url).pathname === "/api/v1/me"
              ? {
                  tenantId: "00000000-0000-7000-8000-000000000001",
                  realmId: "00000000-0000-7000-8000-000000000002",
                  membershipId: "00000000-0000-7000-8000-000000000003",
                  subject: "replacement-reader",
                  permissions: ["resource.read"],
                  availablePermissions: ["resource.read"],
                }
              : { resourceKinds: ["agent"] },
          ),
          { headers: { "content-type": "application/json" } },
        );
      },
      close: async () => {},
    }),
    access,
  );
  t.after(() => writer.dispose());
  await writer.select(current);
  await writer.manual("accepted-replacement");
  const accepted = data.get(key);
  assert.equal(typeof accepted, "string");
  assert.equal((await writer.test()).identity.subject, "replacement-reader");
  for (const stale of [
    JSON.stringify({
      binding: binding(previous, "host"),
      accessToken: "old-token",
    }),
    "{invalid-json",
  ]) {
    const lagging = new Session(
      { ...native, get: async () => stale },
      "host",
      async () => ({
        fetch: async () =>
          assert.fail("invalid cached credentials must not make a request"),
        close: async () => {},
      }),
      credentialAccess(directory),
    );
    try {
      await assert.rejects(lagging.select(current), (error) => {
        assert.equal(
          data.get(key),
          accepted,
          "stale cleanup must preserve the accepted replacement",
        );
        assert.equal(error.code, "credential_update_pending");
        return true;
      });
      assert.equal(data.get(key), accepted);
      assert.equal(await access.current(key, accepted), true);
      assert.equal(
        (await writer.test()).identity.subject,
        "replacement-reader",
      );
    } finally {
      await lagging.dispose();
    }
  }
  // A genuinely invalid current credential still clears and requires login.
  const invalidCurrent = "{invalid-json";
  data.set(key, invalidCurrent);
  await access.run(key, () => access.record(key, invalidCurrent));
  const invalid = new Session(
    native,
    "host",
    async () => ({
      fetch: async () =>
        assert.fail("invalid credentials must not make a request"),
      close: async () => {},
    }),
    access,
  );
  try {
    await assert.rejects(invalid.select(current), /sign_in_required/u);
    assert.equal(data.size, 0);
    assert.equal(await access.current(key, undefined), true);
  } finally {
    await invalid.dispose();
  }
});

test("host identifiers are stable, atomically shared and separate private credentials", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "catalog-host-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const directory = join(root, "first");
  const identifiers = await Promise.all(
    Array.from({ length: 8 }, () => executionHost(directory, "ssh-remote")),
  );
  const host = identifiers[0];
  assert.match(host, /^[a-f0-9]{64}$/u);
  assert.ok(identifiers.every((id) => id === host));
  assert.deepEqual(await readdir(directory), ["connection-host-id"]);
  assert.equal(
    (await stat(join(directory, "connection-host-id"))).mode & 0o777,
    0o600,
  );
  assert.equal(await executionHost(directory, "ssh-remote"), host);
  assert.notEqual(await executionHost(directory, "local"), host);
  const other = await executionHost(join(root, "second"), "ssh-remote");
  assert.notEqual(other, host);
  assert.notEqual(
    await readFile(join(directory, "connection-host-id"), "utf8"),
    await readFile(join(root, "second/connection-host-id"), "utf8"),
  );
  const stored = new Map();
  const secrets = {
    get: async (key) => stored.get(key),
    store: async (key, value) => {
      stored.set(key, value);
    },
    delete: async (key) => {
      stored.delete(key);
    },
  };
  const network = async () => ({
    fetch: async () => {
      assert.fail("an unsigned-in host must not contact the Catalog");
    },
    close: async () => {},
  });
  const first = new Session(
    secrets,
    host,
    network,
    credentialAccess(directory),
  );
  const second = new Session(secrets, other, network);
  t.after(() => Promise.all([first.dispose(), second.dispose()]));
  const connection = connections([
    {
      id: "same",
      label: "Same",
      origin: "https://catalog.example",
      mode: "control",
      auth: "manual",
    },
  ])[0];
  await first.select(connection);
  await first.manual("private-token");
  await second.select(connection);
  await assert.rejects(second.test(), /sign_in_required/u);
  await second.signOut();
  assert.equal(stored.size, 1, "another host cannot sign out the first host");
  await first.signOut();
  assert.equal(stored.size, 0);
});

test("invalid or unavailable host storage fails closed without replacement or staging residue", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "catalog-host-refusal-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const path = join(root, "connection-host-id");
  for (const value of [
    "",
    "not-an-identifier",
    "z".repeat(36),
    "00000000-0000-4000-8000-000000000000\n",
  ]) {
    await writeFile(path, value);
    await assert.rejects(
      executionHost(root, "local"),
      /^ClientError: Catalog: host_storage_unavailable$/u,
    );
    assert.equal(await readFile(path, "utf8"), value);
    assert.deepEqual(await readdir(root), ["connection-host-id"]);
  }
  const invalidUTF8 = Buffer.from("00000000-0000-4000-8000-000000000000");
  invalidUTF8[0] |= 0x80;
  await writeFile(path, invalidUTF8);
  await assert.rejects(
    executionHost(root, "local"),
    /host_storage_unavailable/u,
  );
  assert.deepEqual(await readFile(path), invalidUTF8);
  await rm(path);
  await mkdir(path);
  await assert.rejects(
    executionHost(root, "local"),
    /host_storage_unavailable/u,
  );
  await rm(path, { recursive: true });
  await writeFile(join(root, "target"), "00000000-0000-4000-8000-000000000000");
  await symlink("target", path);
  await assert.rejects(
    executionHost(root, "local"),
    /host_storage_unavailable/u,
  );
  await rm(path);
  await rm(join(root, "target"));
  await writeFile(path, "not-a-directory");
  await assert.rejects(
    executionHost(path, "local"),
    /host_storage_unavailable/u,
  );
  assert.deepEqual(await readdir(root), ["connection-host-id"]);
});
