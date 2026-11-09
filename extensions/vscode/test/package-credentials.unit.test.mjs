import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, readdir, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

const { PackageCredentials, credentialAccess } = await import(
  process.env.CATALOG_VSCODE_LIB
);
const account = "a".repeat(64);
const credential = (index) => ({
  type: "git",
  origin: `https://package${index}.owned.test`,
  scheme: "basic",
  username: "reader",
  token: "synthetic-package-secret",
});
function deferred() {
  let resolve;
  const promise = new Promise((done) => {
    resolve = done;
  });
  return { promise, resolve };
}
async function fixture(t) {
  const directory = await mkdtemp(join(tmpdir(), "catalog-package-index-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const data = new Map([["openAgenticCatalog.login", "preserve-login"]]);
  let beforeStore = async () => {},
    beforeDelete = async () => {};
  const secrets = {
    keys: async () => [...data.keys()],
    get: async (key) => data.get(key),
    store: async (key, value) => {
      await beforeStore(key, value);
      data.set(key, value);
    },
    delete: async (key) => {
      await beforeDelete(key);
      data.delete(key);
    },
  };
  const access = credentialAccess(directory);
  return {
    data,
    secrets,
    directory,
    access,
    first: new PackageCredentials(secrets, access),
    second: new PackageCredentials(secrets, credentialAccess(directory)),
    store(action) {
      beforeStore = action;
    },
    delete(action) {
      beforeDelete = action;
    },
    size: () =>
      [...data.keys()].filter((key) => key.includes(".package:")).length,
  };
}

test("package quota admission excludes concurrent writers in one store and across host clients", async (t) => {
  for (const secondClient of [false, true]) {
    await t.test(
      secondClient ? "shared host clients" : "one store",
      async (t) => {
        const f = await fixture(t);
        for (let index = 0; index < 49; index++)
          await f.first.save(account, credential(index));
        const entered = deferred(),
          release = deferred();
        let held = false;
        f.store(async () => {
          if (!held) {
            held = true;
            entered.resolve();
            await release.promise;
          }
        });
        const writing = f.first.save(account, credential(49));
        const second = secondClient ? f.second : f.first;
        try {
          await entered.promise;
          await assert.rejects(second.save(account, credential(50)), {
            code: "credential_in_use",
          });
          assert.equal(f.size(), 49);
        } finally {
          release.resolve();
          await writing;
        }
        assert.equal(f.size(), 50);
        await assert.rejects(second.save(account, credential(50)), {
          code: "package_credential_limit",
        });
        await second.save(account, {
          ...credential(0),
          token: "replacement-package-secret",
        });
        assert.equal(f.size(), 50, "a quota refusal must release the writer");
        assert.equal(
          (
            await f.first.get(account, {
              type: "git",
              repository: credential(0).origin + "/repo",
            })
          ).token,
          "replacement-package-secret",
        );
        await second.forget(account, "git", credential(49).origin);
        await f.first.save(account, credential(50));
        assert.equal(f.size(), 50);
        assert.equal(f.data.get("openAgenticCatalog.login"), "preserve-login");
      },
    );
  }
});

test("stale native package indexes refuse admission before storage and recover after propagation", async (t) => {
  const f = await fixture(t);
  for (let index = 0; index < 49; index++)
    await f.first.save(account, credential(index));
  const stale = [...f.data.keys()];
  let writes = 0,
    lagging = true;
  const second = new PackageCredentials(
    {
      ...f.secrets,
      keys: async () => (lagging ? stale : [...f.data.keys()]),
      store: async (...args) => {
        writes++;
        return f.secrets.store(...args);
      },
    },
    credentialAccess(f.directory),
  );
  await f.first.save(account, credential(49));
  await assert.rejects(second.save(account, credential(50)), {
    code: "credential_update_pending",
  });
  assert.equal(writes, 0);
  assert.equal(f.size(), 50);
  lagging = false;
  await assert.rejects(second.save(account, credential(50)), {
    code: "package_credential_limit",
  });
  await second.save(account, credential(0));
  assert.equal(writes, 1);
  for (const name of await readdir(f.directory)) {
    assert.match(name, /^\.credential-[a-f0-9]{64}\.state$/u);
    assert.match(
      await readFile(join(f.directory, name), "utf8"),
      /^[a-f0-9]{64}$/u,
    );
  }
});

test("package recovery cannot steal its own active writer or admit a write while cleanup is held", async (t) => {
  const f = await fixture(t),
    writing = deferred(),
    releaseWrite = deferred();
  f.store(async () => {
    writing.resolve();
    await releaseWrite.promise;
  });
  const save = f.first.save(account, credential(0));
  try {
    await writing.promise;
    await assert.rejects(
      f.first.forgetAll(() => false, true),
      {
        code: "credential_in_use",
      },
    );
    assert.equal(f.size(), 0);
  } finally {
    releaseWrite.resolve();
    await save;
  }
  const clearing = deferred(),
    releaseClear = deferred();
  f.delete(async () => {
    clearing.resolve();
    await releaseClear.promise;
  });
  const recovery = f.first.forgetAll(() => false, true);
  try {
    await clearing.promise;
    await assert.rejects(f.first.save(account, credential(1)), {
      code: "credential_in_use",
    });
    assert.equal(f.size(), 1);
  } finally {
    releaseClear.resolve();
    assert.deepEqual(await recovery, { deleted: 1, failed: 0, remaining: 0 });
  }
  await f.first.save(account, credential(1));
  assert.equal(f.size(), 1);
});

test("uncertain package writes retain ownership until explicit offline recovery and permit subsequent admission", async (t) => {
  for (const committed of [false, true]) {
    await t.test(
      committed ? "committed before refusal" : "refused before commit",
      async (t) => {
        const f = await fixture(t);
        await f.first.save(account, credential(0));
        f.store(async (key, value) => {
          if (committed) f.data.set(key, value);
          throw new Error("private-storage-refusal");
        });
        await assert.rejects(
          f.first.save(account, credential(1)),
          /private-storage-refusal/u,
        );
        await assert.rejects(f.second.save(account, credential(2)), {
          code: "credential_in_use",
        });
        assert.equal(f.size(), committed ? 2 : 1);
        f.store(async () => {});
        assert.deepEqual(await f.second.forgetAll(() => false, true), {
          deleted: committed ? 2 : 1,
          failed: 0,
          remaining: 0,
        });
        assert.equal(f.data.get("openAgenticCatalog.login"), "preserve-login");
        await f.first.save(account, credential(3));
        assert.equal(f.size(), 1);
        assert.deepEqual(
          (await readdir(f.directory)).filter((name) => name.endsWith(".lock")),
          [],
        );
      },
    );
  }
});

test("a failed package generation publication retains the committed write and exclusive recovery ownership", async (t) => {
  const f = await fixture(t);
  await f.first.save(account, credential(0));
  const generation = (await readdir(f.directory)).find((name) =>
      name.endsWith(".state"),
    ),
    before = await readFile(join(f.directory, generation));
  let refuse = true;
  const writer = new PackageCredentials(f.secrets, {
    ...f.access,
    record: async (...args) => {
      if (refuse) {
        refuse = false;
        throw new Error("private-generation-refusal");
      }
      return f.access.record(...args);
    },
  });
  await assert.rejects(
    writer.save(account, credential(1)),
    /private-generation-refusal/u,
  );
  assert.equal(f.size(), 2, "the secret write already committed");
  assert.deepEqual(await readFile(join(f.directory, generation)), before);
  await assert.rejects(f.second.save(account, credential(2)), {
    code: "credential_in_use",
  });
  assert.deepEqual(await f.second.forgetAll(() => false, true), {
    deleted: 2,
    failed: 0,
    remaining: 0,
  });
  await writer.save(account, credential(3));
  assert.equal(f.size(), 1);
  assert.equal(f.data.get("openAgenticCatalog.login"), "preserve-login");
});

test("bounded partial package recovery preserves its remaining generation and canceled recovery writes no secrets", async (t) => {
  const f = await fixture(t);
  for (let index = 0; index < 51; index++)
    f.data.set(
      `openAgenticCatalog.package:${account}:${index.toString(16).padStart(64, "0")}`,
      "inert-package-entry",
    );
  assert.deepEqual(await f.first.forgetAll(() => true, true), {
    deleted: 0,
    failed: 0,
    remaining: 51,
  });
  assert.equal(f.size(), 51);
  assert.deepEqual(await f.first.forgetAll(() => false, true), {
    deleted: 50,
    failed: 0,
    remaining: 1,
  });
  await f.second.save(account, credential(0));
  assert.equal(
    f.size(),
    2,
    "partial Stop must not leave a mismatched generation",
  );
  let failures = 0;
  f.delete(async () => {
    failures++;
    throw new Error("private-delete-refusal");
  });
  assert.deepEqual(await f.first.forgetAll(() => false, true), {
    deleted: 0,
    failed: 2,
    remaining: 2,
  });
  assert.equal(failures, 2);
  f.delete(async () => {});
  await f.second.save(account, credential(1));
  assert.equal(f.size(), 3);
  await f.first.forgetAll(() => false, true);
  await f.second.save(account, credential(2));
  assert.equal(f.size(), 1);
});
