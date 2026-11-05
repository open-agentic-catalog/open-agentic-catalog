import test from "node:test";
import assert from "node:assert/strict";
import {
  mkdtemp,
  rm,
  readFile,
  writeFile,
  mkdir,
  symlink,
  link,
  chmod,
  rename,
  stat,
  readdir,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { gzipSync } from "node:zlib";
import {
  files,
  representation,
  content,
  zip,
  tar,
  hash,
} from "./package-fixtures.mjs";
const lib = await import(process.env.CATALOG_VSCODE_LIB);
const {
  skillPlan,
  packagePath,
  unpackPackage,
  verifyPackage,
  packageTools,
  packageChanges,
  SkillStore,
} = lib;
const binding = hash("account on execution host"),
  plan = (entries = files()) => skillPlan(representation(entries), binding);
async function workspace(t) {
  const root = await mkdtemp(join(tmpdir(), "oac-skill-unit-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  return root;
}
const reject = (action, code) => assert.rejects(action, (e) => e.code === code);

test("complete inventory, immutable locators and exact modes define the installation", () => {
  const p = plan();
  assert.equal(p.files.length, 3);
  assert.equal(p.sources.length, 2);
  verifyPackage(p, content());
  assert.deepEqual(packageChanges(p.files, plan(files("Updated")).files), [
    "Change owned-skill/SKILL.md",
  ]);
  for (const mutate of [
    (r) => r.package.fileCount++,
    (r) => r.package.expandedSize++,
    (r) => (r.package.files[0].digest = "sha256:bad"),
    (r) => (r.handoff.git.commit = "main"),
    (r) =>
      (r.handoff.git.repository = "https://user:password@git.example/repo"),
    (r) => (r.handoff.git.directory = "other"),
    (r) => (r.handoff.oci.reference = "registry.example/skills:latest"),
    (r) => (r.package.structureState = "failed"),
    (r) => delete r.package,
  ]) {
    const r = representation();
    mutate(r);
    assert.throws(() => skillPlan(r, binding));
  }
  const edited = content();
  edited.get("owned-skill/SKILL.md").bytes = Buffer.from("edited");
  assert.throws(() => verifyPackage(p, edited), {
    code: "package_integrity_failed",
  });
  for (const registry of [
    "[::1]:5000",
    "registry.example:443",
    "registry:443",
    "localhost:5000",
  ]) {
    const r = representation();
    r.handoff.oci.reference = registry + "/skills/a__b--c@" + hash("manifest");
    assert.equal(
      skillPlan(r, binding).sources[1].reference,
      r.handoff.oci.reference,
    );
  }
  for (const registry of [
    "[not-ip]",
    "registry.example:0",
    "registry.example:65536",
    "-invalid.example",
    "ambiguous",
  ]) {
    const r = representation();
    r.handoff.oci.reference = registry + "/skills/owned@" + hash("manifest");
    assert.throws(() => skillPlan(r, binding), {
      code: "immutable_package_source_required",
    });
  }
  for (const path of [
    "../a",
    "a/../b",
    "/root/a",
    "a\\b",
    "a/CON.txt",
    "a/.git/config",
    "a/x.",
    "a//b",
    "a/" + "d/".repeat(17) + "x",
  ])
    assert.throws(() => packagePath(path));
  for (const extra of [
    {
      path: "owned-skill/SKILL.md",
      bytes: Buffer.from("collision"),
      executable: false,
    },
    {
      path: "owned-skill/References/other",
      bytes: Buffer.alloc(0),
      executable: false,
    },
    {
      path: "owned-skill/references",
      bytes: Buffer.alloc(0),
      executable: false,
    },
  ])
    assert.throws(() => plan([...files(), extra]));
  assert.deepEqual(packageTools({}), {
    gitPath: "git",
    orasPath: "oras",
    timeoutMs: 60000,
  });
  for (const tools of [
    { gitPath: "./git" },
    { timeoutMs: 0 },
    { caFile: "relative" },
    { shell: true },
  ])
    assert.throws(() => packageTools(tools));
});

test("portable ZIP and gzip/tar preserve supporting files and executable modes", () => {
  for (const type of [
    "application/agent-skills+zip",
    "application/agent-skills+gzip",
  ]) {
    const p = skillPlan(representation(files(), type), binding);
    for (const archive of type.endsWith("+zip")
      ? [zip(files()), zip(files(), true)]
      : [gzipSync(tar(files()))]) {
      const got = unpackPackage(p, archive);
      assert.deepEqual(
        [...got.keys()].sort(),
        files()
          .map((f) => f.path)
          .sort(),
      );
      assert.equal(
        got.get("owned-skill/scripts/never-run.sh").executable,
        true,
      );
    }
  }
});

test("archives reject corruption, links, traversal, collisions, missing files and concatenation", () => {
  const p = plan();
  for (const entries of [
    files().slice(0, 2),
    [...files(), files()[0]],
    [...files(), { ...files()[0], path: "../escape" }],
    [...files(), { ...files()[0], path: "owned-skill/References/other" }],
    files().map((f, i) => (i ? f : { ...f, mode: 0o120777 })),
  ])
    assert.throws(() => unpackPackage(p, zip(entries)));
  const wrong = zip(files());
  wrong[40] ^= 1;
  assert.throws(() => unpackPackage(p, wrong));
  assert.throws(() =>
    unpackPackage(p, Buffer.concat([zip(files()), Buffer.from("junk")])),
  );
  const gz = skillPlan(
      representation(files(), "application/agent-skills+gzip"),
      binding,
    ),
    raw = gzipSync(tar(files()));
  assert.throws(() => unpackPackage(gz, Buffer.concat([raw, raw])));
  const bad = Buffer.from(raw);
  bad[bad.length - 8] ^= 1;
  assert.throws(() => unpackPackage(gz, bad));
  assert.throws(() =>
    unpackPackage(
      gz,
      gzipSync(tar(files().map((f, i) => (i ? f : { ...f, type: 50 })))),
    ),
  );
  assert.throws(() => unpackPackage(gz, gzipSync(Buffer.alloc(1024, 1))));
  assert.throws(() => unpackPackage(p, Buffer.alloc(0)));
});

test("install, no-op, update and offline removal preserve exact trees and permissions", async (t) => {
  const root = await workspace(t),
    store = new SkillStore(root),
    p = plan();
  let checks = 0;
  assert.deepEqual(await store.list(), []);
  await store.install(p, content(), async () => {
    checks++;
  });
  assert.equal(checks, 1);
  assert.deepEqual(
    (await store.list()).map((r) => r.name),
    [p.name],
  );
  assert.equal((await store.inspect(p)).identical, true);
  assert.equal(
    (await stat(join(root, ".agents/skills/owned-skill/scripts/never-run.sh")))
      .mode & 0o777,
    0o700,
  );
  await store.install(p, content(), async () => {
    throw Error("no-op must not authorize/write");
  });
  const updated = files("Updated instructions"),
    next = plan(updated);
  await store.install(next, content(updated), async () => {});
  assert.match(
    await readFile(join(root, ".agents/skills/owned-skill/SKILL.md"), "utf8"),
    /Updated/,
  );
  await store.remove(p.name);
  assert.deepEqual(await store.list(), []);
  assert.deepEqual(await readdir(join(root, ".agents/skills")), []);
  assert.equal(
    (await readdir(store.state)).some(
      (p) => p.startsWith("stage-") || p.startsWith("backup-"),
    ),
    false,
  );
});

test("removal refuses a replacement after consent before any removal hook or journal", async (t) => {
  const root = await workspace(t),
    store = new SkillStore(root),
    original = plan();
  await store.install(original, content(), async () => {});
  const confirmed = await store.get(original.name);
  const nextFiles = files("Replacement after consent"),
    next = plan(nextFiles);
  await store.install(next, content(nextFiles), async () => {});
  let mutations = 0;
  await reject(
    store.remove(
      original.name,
      () => {},
      confirmed,
      async () => {
        mutations++;
      },
    ),
    "selection_changed",
  );
  assert.equal(mutations, 0);
  assert.equal((await store.inspect(next)).identical, true);
  assert.equal((await readdir(store.state)).includes("journal.json"), false);
  await store.remove(
    next.name,
    () => {},
    await store.get(next.name),
    async () => {
      mutations++;
    },
  );
  assert.equal(
    mutations,
    1,
    "the unchanged confirmed receipt remains removable",
  );
});

test("local edits, added files, mode changes, symlinks and account changes prevent replacement/removal", async (t) => {
  for (const mutation of [
    async (p) => writeFile(join(p, "SKILL.md"), "local edit"),
    async (p) => writeFile(join(p, "extra"), "local file"),
    async (p) => chmod(join(p, "SKILL.md"), 0o644),
    async (p) => {
      await rm(join(p, "SKILL.md"));
      await symlink("/etc/passwd", join(p, "SKILL.md"));
    },
    async (p) => link(join(p, "SKILL.md"), join(p, "..", "owned-hardlink")),
  ]) {
    const root = await workspace(t),
      store = new SkillStore(root),
      p = plan();
    await store.install(p, content(), async () => {});
    await mutation(join(store.destination, p.name));
    await assert.rejects(store.inspect(p));
    await assert.rejects(store.remove(p.name));
    assert.equal((await store.list()).length, 1);
  }
  const root = await workspace(t),
    store = new SkillStore(root);
  await store.install(plan(), content(), async () => {});
  await reject(
    () => store.inspect(skillPlan(representation(), hash("different account"))),
    "installation_account_changed",
  );
});

test("a damaged receipt cannot turn changed contents into an identical no-op", async (t) => {
  const root = await workspace(t),
    store = new SkillStore(root),
    p = plan();
  await store.install(p, content(), async () => {});
  const changed = files("Changed locally"),
    retained = await store.get(p.name);
  await writeFile(
    join(store.destination, p.name, "SKILL.md"),
    changed[0].bytes,
  );
  retained.files = plan(changed).files;
  await writeFile(
    join(store.state, "receipts", p.name + ".json"),
    JSON.stringify(retained),
  );
  await assert.rejects(store.inspect(p), { code: "local_skill_changed" });
  assert.equal(
    await readFile(join(store.destination, p.name, "SKILL.md"), "utf8"),
    changed[0].bytes.toString(),
  );
});

test("unowned destinations, unsafe ancestors, denied final access and locks fail without activation", async (t) => {
  const root = await workspace(t),
    store = new SkillStore(root),
    p = plan();
  await mkdir(join(root, ".agents/skills/owned-skill"), { recursive: true });
  await reject(() => store.inspect(p), "skill_destination_exists");
  await rm(join(root, ".agents"), { recursive: true });
  await symlink(tmpdir(), join(root, ".agents"));
  await reject(() => store.inspect(p), "unsafe_installation_path");
  await rm(join(root, ".agents"));
  await assert.rejects(
    store.install(p, content(), async () => {
      throw Error("access revoked");
    }),
    /access revoked/,
  );
  assert.deepEqual(await store.list(), []);
  assert.equal(
    (await readdir(store.state)).some((p) => p.startsWith("stage-")),
    false,
  );
  await writeFile(join(store.state, "lock"), "");
  await reject(
    () => store.install(p, content(), async () => {}),
    "installation_busy_or_interrupted",
  );
  await store.recover();
  await store.install(p, content(), async () => {});
});

test("interrupted replacement recovers either old or installed tree and refuses changed evidence", async (t) => {
  for (const phase of ["before-switch", "gap", "after-switch", "changed"]) {
    const root = await workspace(t),
      store = new SkillStore(root),
      p = plan();
    await store.install(p, content(), async () => {});
    const before = await store.get(p.name),
      next = plan(files("Next"));
    const stage = "stage-11111111-1111-1111-1111-111111111111",
      backup = "backup-22222222-2222-2222-2222-222222222222";
    const otherRoot = await workspace(t),
      other = new SkillStore(otherRoot);
    await other.install(next, content(files("Next")), async () => {});
    const after = await other.get(p.name);
    await rename(join(other.destination, p.name), join(store.state, stage));
    await writeFile(
      join(store.state, "journal.json"),
      JSON.stringify({ stage, backup, before, after }),
    );
    if (phase !== "before-switch")
      await rename(join(store.destination, p.name), join(store.state, backup));
    if (phase === "after-switch" || phase === "changed")
      await rename(join(store.state, stage), join(store.destination, p.name));
    if (phase === "changed") {
      await writeFile(join(store.destination, p.name, "SKILL.md"), "user edit");
      await assert.rejects(store.recover());
      assert.equal(
        await readFile(join(store.destination, p.name, "SKILL.md"), "utf8"),
        "user edit",
      );
      continue;
    }
    await store.recover();
    assert.equal(
      (await store.list())[0].identity,
      phase === "after-switch" ? after.identity : before.identity,
    );
    assert.equal(
      (await store.inspect(phase === "after-switch" ? next : p)).identical,
      true,
    );
  }
});

test("interrupted first installation and removal leave recoverable owned state", async (t) => {
  const root = await workspace(t),
    store = new SkillStore(root),
    p = plan();
  await store.install(p, content(), async () => {});
  const before = await store.get(p.name);
  const stage = "stage-11111111-1111-1111-1111-111111111111",
    backup = "backup-22222222-2222-2222-2222-222222222222";
  await writeFile(
    join(store.state, "journal.json"),
    JSON.stringify({ stage, backup, before }),
  );
  await rename(join(store.destination, p.name), join(store.state, backup));
  await store.recover();
  assert.deepEqual(await store.list(), []);
  const source = await workspace(t),
    sourceStore = new SkillStore(source);
  await sourceStore.install(p, content(), async () => {});
  const after = await sourceStore.get(p.name);
  await rename(join(sourceStore.destination, p.name), join(store.state, stage));
  await writeFile(
    join(store.state, "journal.json"),
    JSON.stringify({ stage, backup, after }),
  );
  await store.recover();
  assert.deepEqual(await store.list(), []);
  assert.equal((await readdir(store.state)).includes(stage), false);
  await writeFile(
    join(store.state, "journal.json"),
    JSON.stringify({ stage: "../../escape", backup, after }),
  );
  await assert.rejects(store.recover(), { code: "unsafe_installation_path" });
});

test("archive budgets and header agreement fail before materialization", () => {
  const huge = [
      ...files(),
      {
        path: "owned-skill/huge",
        bytes: Buffer.alloc(2 * 1024 * 1024),
        executable: false,
      },
    ],
    p = plan(huge);
  assert.throws(() => unpackPackage(p, zip(huge)), {
    code: "package_size_limit",
  });
  const good = zip(files()),
    localMismatch = Buffer.from(good);
  localMismatch.writeUInt32LE(1, 14);
  assert.throws(() => unpackPackage(plan(), localMismatch));
  const dir = {
    path: "owned-skill/References/",
    bytes: Buffer.alloc(0),
    executable: false,
    mode: 0o40755,
  };
  assert.throws(() => unpackPackage(plan(), zip([dir, ...files()])));
  assert.throws(() => packagePath("owned-skill/" + "a".repeat(256)));
});

test("observed cancellation after journaling prevents activation and permits exact recovery", async (t) => {
  const root = await workspace(t),
    store = new SkillStore(root),
    p = plan();
  let calls = 0;
  await assert.rejects(
    store.install(
      p,
      content(),
      async () => {},
      () => {
        if (++calls === 2) throw Error("authority changed");
      },
    ),
    /authority changed/,
  );
  await assert.rejects(readFile(join(store.destination, p.name, "SKILL.md")));
  await assert.rejects(store.inspect(p), {
    code: "installation_recovery_required",
  });
  await store.recover();
  assert.deepEqual(await store.list(), []);
  assert.equal(
    (await readdir(store.state)).some((f) => f.startsWith("stage-")),
    false,
  );
});

test("an interrupted release-only update recovers the old identity when package files are identical", async (t) => {
  const root = await workspace(t),
    store = new SkillStore(root),
    original = plan(),
    next = { ...original, identity: hash("new release with identical files") };
  await store.install(original, content(), async () => {});
  const before = await store.get(original.name);
  let calls = 0;
  await assert.rejects(
    store.install(
      next,
      content(),
      async () => {},
      () => {
        if (++calls === 2) throw Error("authority changed");
      },
    ),
    /authority changed/,
  );
  // Recovery itself can stop after discarding the inert stage. That must not
  // turn the still-active old release into the new one on the next attempt.
  let recoveryChecks = 0;
  await assert.rejects(
    store.recover(() => {
      if (++recoveryChecks === 5) throw Error("workspace removed");
    }),
    /workspace removed/,
  );
  await store.recover();
  assert.equal((await store.inspect(original)).identical, true);
  assert.equal((await store.inspect(next)).identical, false);
  assert.equal(
    (await readdir(store.state)).some((name) => /^(stage|backup)-/u.test(name)),
    false,
  );
  await store.install(next, content(), async () => {});
  assert.equal((await store.inspect(next)).identical, true);
  // A completed receipt also identifies an activated update if interruption
  // leaves the journal after the backup has already been removed.
  await writeFile(
    join(store.state, "journal.json"),
    JSON.stringify({
      stage: "stage-11111111-1111-1111-1111-111111111111",
      backup: "backup-22222222-2222-2222-2222-222222222222",
      before,
      after: await store.get(next.name),
    }),
  );
  await store.recover();
  assert.equal((await store.inspect(next)).identical, true);
});

test("explicit recovery discards abandoned inert work without touching other workspace files", async (t) => {
  const root = await workspace(t),
    store = new SkillStore(root);
  const work = await store.temporary();
  await writeFile(join(work, "partial"), "download");
  const backup = join(
    store.state,
    "backup-33333333-3333-3333-3333-333333333333",
  );
  await mkdir(backup, { mode: 0o700 });
  await writeFile(join(backup, "keep"), "possible only copy");
  await writeFile(join(root, "keep"), "user file");
  await store.recover();
  assert.equal(
    (await readdir(store.state)).some((p) => p.startsWith("acquire-")),
    false,
  );
  assert.equal(await readFile(join(root, "keep"), "utf8"), "user file");
  assert.equal(
    await readFile(join(backup, "keep"), "utf8"),
    "possible only copy",
  );
});

test("recovery stops before changing staged files when workspace authority is withdrawn", async (t) => {
  const root = await workspace(t),
    store = new SkillStore(root),
    original = plan(),
    nextFiles = files("Next release"),
    next = plan(nextFiles);
  await store.install(original, content(), async () => {});
  let installChecks = 0;
  await assert.rejects(
    store.install(
      next,
      content(nextFiles),
      async () => {},
      () => {
        if (++installChecks === 2) throw Error("installation cancelled");
      },
    ),
    /installation cancelled/,
  );
  const journalPath = join(store.state, "journal.json"),
    journalBytes = await readFile(journalPath),
    journal = JSON.parse(journalBytes.toString());
  let recoveryChecks = 0;
  await assert.rejects(
    store.recover(() => {
      if (++recoveryChecks === 4) throw Error("workspace removed");
    }),
    /workspace removed/,
  );
  assert.deepEqual(await readFile(journalPath), journalBytes);
  assert.equal(
    (await readFile(join(store.state, journal.stage, "SKILL.md"))).equals(
      nextFiles[0].bytes,
    ),
    true,
  );
  assert.deepEqual(
    await readFile(join(store.destination, original.name, "SKILL.md")),
    content().get("owned-skill/SKILL.md").bytes,
  );
  await store.recover();
  assert.equal((await store.inspect(original)).identical, true);
  await writeFile(join(store.state, "lock"), "");
  await assert.rejects(
    store.recover(() => {
      throw Error("trust withdrawn");
    }),
    /trust withdrawn/,
  );
  assert.equal(await readFile(join(store.state, "lock"), "utf8"), "");
  await store.recover();
});

test("large valid inventories remain readable without retaining them in navigation summaries", async (t) => {
  const root = await workspace(t),
    store = new SkillStore(root),
    p = plan();
  await store.temporary();
  const entries = [p.files.find((f) => f.path.endsWith("/SKILL.md"))];
  for (let i = 0; i < 1023; i++)
    entries.push({
      path: "owned-skill/" + ("a".repeat(240) + "/").repeat(3) + String(i),
      size: 0,
      digest: hash(""),
      executable: false,
    });
  const retained = {
    name: p.name,
    identity: p.identity,
    binding: p.binding,
    files: entries,
  };
  const encoded = JSON.stringify(retained);
  assert.ok(Buffer.byteLength(encoded) > 512 * 1024);
  await writeFile(join(store.state, "receipts", p.name + ".json"), encoded);
  assert.equal((await store.get(p.name)).files.length, 1024);
  assert.deepEqual(await store.list(), [
    {
      name: p.name,
      identity: p.identity,
      binding: p.binding,
      fingerprint: hash(
        JSON.stringify({
          ...retained,
          files: [...entries].sort((a, b) =>
            a.path < b.path ? -1 : a.path > b.path ? 1 : 0,
          ),
        }),
      ),
    },
  ]);
  assert.equal(await store.get("other-skill"), undefined);
  await writeFile(join(store.state, "receipts", "other-skill.json"), encoded);
  await assert.rejects(store.get("other-skill"), {
    code: "unsafe_installation_path",
  });
  await assert.rejects(store.list(), { code: "unsafe_installation_path" });
});
