import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { writeFixtureInput } from "./fixture-inputs.mjs";

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
