import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import {
  mkdtemp,
  mkdir,
  readFile,
  rm,
  symlink,
  writeFile,
  readdir,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { stageDocumentation } from "./dev-documentation.mjs";

async function fixture(t) {
  const directory = await mkdtemp(join(tmpdir(), "manual-stage-unit-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const source = join(directory, "source"),
    assets = join(directory, "assets");
  await mkdir(source);
  await mkdir(assets);
  const content = "<h1>Prepared manual</h1>";
  const manifest = {
    schemaVersion: 1,
    base: "/docs/",
    files: [
      {
        path: "index.html",
        size: Buffer.byteLength(content),
        sha256: createHash("sha256").update(content).digest("hex"),
      },
    ],
  };
  await writeFile(join(source, "index.html"), content);
  const seal = () =>
    writeFile(
      join(source, "documentation-bundle.json"),
      JSON.stringify(manifest),
    );
  await seal();
  return { source, assets, manifest, seal, content, directory };
}

test("stages only admitted exact manual bytes without modifying the source", async (t) => {
  const f = await fixture(t);
  await writeFile(join(f.source, "unlisted.txt"), "excluded");
  const before = await readFile(join(f.source, "documentation-bundle.json"));
  await stageDocumentation(f.source, f.assets);
  assert.deepEqual((await readdir(join(f.assets, "docs"))).sort(), [
    "documentation-bundle.json",
    "index.html",
  ]);
  assert.equal(
    await readFile(join(f.assets, "docs/index.html"), "utf8"),
    f.content,
  );
  assert.deepEqual(
    await readFile(join(f.assets, "docs/documentation-bundle.json")),
    before,
  );
  assert.deepEqual(
    await readFile(join(f.source, "documentation-bundle.json")),
    before,
  );
});

test("rejects wrong bases, unsafe/duplicate paths and invalid work bounds", async (t) => {
  for (const mutate of [
    (m) => (m.base = "/"),
    (m) => (m.files = []),
    (m) => m.files.push(m.files[0]),
    ...[
      "../index.html",
      "/index.html",
      ".hidden",
      "a//index.html",
      "a/../index.html",
      "documentation-bundle.json",
    ].map((path) => (m) => (m.files[0].path = path)),
    (m) => (m.files[0].size = 33 * 1024 * 1024),
    (m) => (m.files[0].size = -1),
    (m) => (m.files[0].size = 1.5),
    (m) => (m.files[0].sha256 = "invalid"),
  ]) {
    const f = await fixture(t);
    mutate(f.manifest);
    await f.seal();
    await assert.rejects(
      stageDocumentation(f.source, f.assets),
      /Invalid prepared manual/,
    );
  }
});

test("refuses changed files and symlinked inputs", async (t) => {
  for (const kind of [
    "changed",
    "same-size-change",
    "file-link",
    "directory-link",
    "manifest-link",
    "root-link",
  ]) {
    const f = await fixture(t);
    let source = f.source;
    if (kind === "directory-link") {
      await mkdir(join(source, "guide"));
      await writeFile(join(source, "guide/index.html"), f.content);
      f.manifest.files.push({
        ...f.manifest.files[0],
        path: "guide/index.html",
      });
      await f.seal();
    }
    const control = join(f.directory, "control");
    await mkdir(control);
    await stageDocumentation(source, control);
    assert.equal(
      await readFile(join(control, "docs/index.html"), "utf8"),
      f.content,
    );
    if (kind === "changed")
      await writeFile(join(source, "index.html"), "different bytes");
    if (kind === "same-size-change")
      await writeFile(
        join(source, "index.html"),
        f.content.replace("manual", "tamper"),
      );
    if (kind === "file-link" || kind === "manifest-link") {
      const name =
        kind === "file-link" ? "index.html" : "documentation-bundle.json";
      const target = join(source, name);
      const bytes = await readFile(target);
      await rm(target);
      await writeFile(join(f.directory, "outside"), bytes);
      await symlink(join(f.directory, "outside"), target);
    }
    if (kind === "root-link") {
      source = join(f.directory, "linked");
      await symlink(f.source, source);
    }
    if (kind === "directory-link") {
      await rm(join(source, "guide"), { recursive: true });
      await mkdir(join(f.directory, "outside"));
      await writeFile(join(f.directory, "outside/index.html"), f.content);
      await symlink(join(f.directory, "outside"), join(source, "guide"));
    }
    await assert.rejects(
      stageDocumentation(source, f.assets),
      /Invalid prepared manual/,
    );
  }
});
