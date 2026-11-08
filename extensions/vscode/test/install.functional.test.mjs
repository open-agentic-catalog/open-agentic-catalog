// Qualify installation lock ownership and recovery across real writer processes.
import test from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, readFile, readdir, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { content, files, hash, representation } from "./package-fixtures.mjs";

const { SkillStore, skillPlan } = await import(process.env.CATALOG_VSCODE_LIB);
const account = hash("owned cross-process installation actor");

async function bounded(work, label) {
  let timer;
  try {
    return await Promise.race([
      work,
      new Promise((_, reject) => {
        timer = setTimeout(
          () => reject(new Error(`Owned writer ${label} timed out`)),
          5000,
        );
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

// IPC holds the existing authorization boundary, after the real writer has
// staged and verified files but before it may publish its journal or tree.
const writerProgram = `
import { pathToFileURL } from "node:url";
const { SkillStore } = await import(new URL("install.js", pathToFileURL(process.env.CATALOG_VSCODE_LIB)));
const { skillPlan } = await import(new URL("packages.js", pathToFileURL(process.env.CATALOG_VSCODE_LIB)));
const { files, hash, representation, content } = await import(process.env.CATALOG_VSCODE_PACKAGE_FIXTURES);
const [workspace, operation, text] = process.argv.slice(1);
const store = new SkillStore(workspace);
let authorized = 0;
try {
  if (operation === "recover") await store.recover();
  else {
    const entries = files(text);
    const plan = skillPlan(representation(entries), hash("owned cross-process installation actor"));
    await store.install(plan, content(entries), async () => {
      authorized++;
      if (operation === "hold") {
        process.send({ type: "staged", authorized });
        await new Promise((resolve) => process.once("message", resolve));
      }
    });
  }
  process.send({ type: "result", authorized });
} catch (error) {
  process.send({ type: "result", code: error.code ?? error.name, authorized });
} finally {
  process.disconnect();
}
`;

test("actual installation writers exclude another process and recover abandoned staged work after SIGKILL", async (t) => {
  const workspace = await mkdtemp(join(tmpdir(), "oac-store-process-")),
    children = [];
  t.after(async () => {
    try {
      for (const entry of children)
        if (entry.child.exitCode === null && entry.child.signalCode === null)
          entry.child.kill("SIGKILL");
      await bounded(
        Promise.all(children.map((entry) => entry.closed)),
        "cleanup",
      );
    } finally {
      await rm(workspace, { recursive: true, force: true });
    }
  });

  const start = (operation, text = "Updated after writer interruption.") => {
    const child = spawn(
      process.execPath,
      ["--input-type=module", "-e", writerProgram, workspace, operation, text],
      {
        cwd: workspace,
        env: {
          ...process.env,
          CATALOG_VSCODE_PACKAGE_FIXTURES: new URL(
            "./package-fixtures.mjs",
            import.meta.url,
          ).href,
        },
        stdio: ["ignore", "pipe", "pipe", "ipc"],
      },
    );
    let diagnostics = "";
    for (const stream of [child.stdout, child.stderr])
      stream.on("data", (chunk) => {
        diagnostics = (diagnostics + chunk).slice(-8192);
      });
    const message = once(child, "message").then(([value]) => value),
      closed = new Promise((resolve) =>
        child.once("close", (code, signal) => resolve({ code, signal })),
      );
    const entry = {
      child,
      closed,
      message: bounded(
        Promise.race([
          message,
          closed.then((exit) => {
            throw new Error(
              `Owned writer exited before IPC: ${JSON.stringify(exit)} ${diagnostics}`,
            );
          }),
        ]),
        "IPC readiness",
      ),
    };
    children.push(entry);
    return entry;
  };
  const run = async (operation) => {
    const entry = start(operation),
      result = await entry.message;
    assert.deepEqual(await bounded(entry.closed, "exit"), {
      code: 0,
      signal: null,
    });
    return result;
  };
  const store = new SkillStore(workspace),
    originalEntries = files("Initial owned instructions."),
    updatedEntries = files("Updated after writer interruption."),
    original = skillPlan(representation(originalEntries), account),
    updated = skillPlan(representation(updatedEntries), account);
  await store.install(original, content(originalEntries), async () => {});
  const receiptPath = join(store.state, "receipts", "owned-skill.json"),
    receipt = await readFile(receiptPath);
  const expectTree = async (
    entries,
    location = join(store.destination, "owned-skill"),
  ) => {
    const inventory = [];
    const walk = async (prefix = "") => {
      for (const entry of await readdir(join(location, prefix), {
        withFileTypes: true,
      })) {
        assert.equal(entry.isSymbolicLink(), false);
        const relative = prefix + entry.name;
        if (entry.isDirectory()) {
          inventory.push(relative + "/");
          await walk(relative + "/");
        } else {
          assert.equal(entry.isFile(), true);
          inventory.push(relative);
        }
      }
    };
    await walk();
    assert.deepEqual(inventory.sort(), [
      "SKILL.md",
      "references/",
      "references/help.txt",
      "scripts/",
      "scripts/never-run.sh",
    ]);
    for (const entry of entries) {
      const path = join(location, entry.path.slice("owned-skill/".length));
      assert.deepEqual(await readFile(path), entry.bytes);
      assert.equal(
        (await stat(path)).mode & 0o777,
        entry.executable ? 0o700 : 0o600,
      );
    }
  };
  await expectTree(originalEntries);
  const held = start("hold");
  assert.deepEqual(await held.message, { type: "staged", authorized: 1 });
  const staged = (await readdir(store.state)).filter((name) =>
    name.startsWith("stage-"),
  );
  assert.equal(staged.length, 1);
  assert.equal((await stat(join(store.state, "lock"))).size, 0);
  assert.equal((await stat(join(store.state, "lock"))).mode & 0o777, 0o600);
  await expectTree(updatedEntries, join(store.state, staged[0]));
  await assert.rejects(readFile(join(store.state, "journal.json")), {
    code: "ENOENT",
  });
  assert.deepEqual(await run("install"), {
    type: "result",
    code: "installation_busy_or_interrupted",
    authorized: 0,
  });
  await expectTree(originalEntries);
  assert.deepEqual(await readFile(receiptPath), receipt);

  assert.equal(held.child.kill("SIGKILL"), true);
  assert.deepEqual(await bounded(held.closed, "killed writer exit"), {
    code: null,
    signal: "SIGKILL",
  });
  assert.deepEqual(await run("install"), {
    type: "result",
    code: "installation_busy_or_interrupted",
    authorized: 0,
  });
  assert.deepEqual(
    (await readdir(store.state)).filter((name) => name.startsWith("stage-")),
    staged,
  );
  await expectTree(originalEntries);
  assert.deepEqual(await readFile(receiptPath), receipt);

  // Only after every other writer has exited may explicit recovery release the
  // abandoned ownership. Ordinary installation never steals a dead writer lock.
  assert.deepEqual(await run("recover"), { type: "result", authorized: 0 });
  assert.deepEqual(await readdir(store.state), ["receipts"]);
  await expectTree(originalEntries);
  assert.deepEqual(await readFile(receiptPath), receipt);
  assert.equal((await store.inspect(original)).identical, true);
  assert.deepEqual(await run("install"), { type: "result", authorized: 1 });
  await expectTree(updatedEntries);
  assert.equal((await store.inspect(updated)).identical, true);
  assert.deepEqual(await readdir(store.state), ["receipts"]);
});
