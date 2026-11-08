import { test } from "node:test";
import assert from "node:assert/strict";
import {
  chmod,
  mkdir,
  mkdtemp,
  readFile,
  rm,
  stat,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  createFixtureStateDirectory,
  writeFixtureInput,
} from "./fixture-inputs.mjs";

test("fixture inputs preserve bytes and explicitly restore read-only container access", async (t) => {
  const parent = await mkdtemp(join(tmpdir(), "catalog-fixture-input-"));
  t.after(() => rm(parent, { recursive: true, force: true }));
  const path = join(parent, "input");
  await writeFile(path, "old", { mode: 0o600 });
  const bytes = Buffer.from("exact fixture bytes\n");
  await writeFixtureInput(path, bytes);
  assert.deepEqual(await readFile(path), bytes);
  // Native POSIX modes are separate from Windows filesystem semantics.
  if (process.platform !== "win32") {
    assert.equal((await stat(path)).mode & 0o777, 0o444);
    assert.equal((await stat(parent)).mode & 0o777, 0o700);
  }
});

test("fixture input failures do not create missing parents or change a conflicting leaf", async (t) => {
  const parent = await mkdtemp(join(tmpdir(), "catalog-fixture-input-"));
  t.after(() => rm(parent, { recursive: true, force: true }));
  const path = join(parent, "input");
  await writeFile(path, "retained fixture bytes", { mode: 0o600 });
  await assert.rejects(writeFixtureInput(join(path, "child"), "invalid"));
  await assert.rejects(
    writeFixtureInput(join(parent, "absent", "child"), "invalid"),
  );
  assert.equal(await readFile(path, "utf8"), "retained fixture bytes");
  await assert.rejects(stat(join(parent, "absent")));
});

test("fresh container-writable state remains beneath a supervisor-private parent", async (t) => {
  const parent = await mkdtemp(join(tmpdir(), "catalog-fixture-state-"));
  t.after(() => rm(parent, { recursive: true, force: true }));
  const fixture = join(parent, "fixture");
  await mkdir(fixture, { mode: 0o755 });
  await chmod(fixture, 0o755);
  const directory = join(fixture, "seed-state");
  await createFixtureStateDirectory(directory);
  assert.equal((await stat(directory)).isDirectory(), true);
  if (process.platform !== "win32") {
    assert.equal((await stat(directory)).mode & 0o777, 0o777);
    assert.equal((await stat(fixture)).mode & 0o777, 0o755);
    assert.equal((await stat(parent)).mode & 0o777, 0o700);
  }
});

test("state-directory preparation refuses existing paths without widening retained data", async (t) => {
  const parent = await mkdtemp(join(tmpdir(), "catalog-fixture-state-"));
  t.after(() => rm(parent, { recursive: true, force: true }));
  const directory = join(parent, "existing-state");
  await mkdir(directory, { mode: 0o700 });
  const file = join(directory, "retained");
  await writeFile(file, "owned private bytes", { mode: 0o600 });
  await assert.rejects(createFixtureStateDirectory(directory), {
    code: "EEXIST",
  });
  await assert.rejects(createFixtureStateDirectory(file), { code: "EEXIST" });
  await assert.rejects(
    createFixtureStateDirectory(join(parent, "missing", "child")),
    { code: "ENOENT" },
  );
  assert.equal(await readFile(file, "utf8"), "owned private bytes");
  if (process.platform !== "win32") {
    assert.equal((await stat(directory)).mode & 0o777, 0o700);
    assert.equal((await stat(file)).mode & 0o777, 0o600);
    assert.equal((await stat(parent)).mode & 0o777, 0o700);
  }
});
