import { test } from "node:test";
import assert from "node:assert/strict";
import {
  mkdtemp,
  mkdir,
  writeFile,
  readFile,
  rm,
  symlink,
  copyFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createHash } from "node:crypto";
import { collectLicenses, legalAssets } from "./licenses.mjs";

async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), "ui-licenses-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  await mkdir(join(root, "node_modules/react"), { recursive: true });
  await mkdir(join(root, "dist/assets"), { recursive: true });
  await writeFile(join(root, "node_modules/react/LICENSE"), "MIT fixture");
  await writeFile(
    join(root, "dist/index.html"),
    "<script src='assets/main.js'></script>",
  );
  await writeFile(join(root, "dist/assets/main.js"), "void 0;");
  const lock = {
    packages: {
      "": {},
      "node_modules/react": { version: "1.0.0", license: "MIT" },
      "node_modules/build-only": { version: "2.0.0", dev: true },
    },
  };
  await writeFile(join(root, "package-lock.json"), JSON.stringify(lock));
  return { root, output: join(root, "licenses"), lock };
}

test("license inventory is deterministic, build-bound and excludes build tools", async (t) => {
  const { root, output } = await fixture(t);
  await collectLicenses(root, output);
  const original = await readFile(join(output, "inventory.json"), "utf8");
  const value = JSON.parse(original);
  assert.equal(value.packages.length, 1);
  assert.deepEqual(value.packages[0], {
    path: "node_modules/react",
    name: "react",
    version: "1.0.0",
    license: "MIT",
    notices: ["node_modules/react/LICENSE"],
  });
  assert.equal(
    value.lockSha256,
    createHash("sha256")
      .update(await readFile(join(root, "package-lock.json")))
      .digest("hex"),
  );
  assert.equal(value.assets.length, 2);
  assert.equal(
    await readFile(join(output, value.packages[0].notices[0]), "utf8"),
    "MIT fixture",
  );
  await collectLicenses(root, output);
  assert.equal(
    await readFile(join(output, "inventory.json"), "utf8"),
    original,
  );
  await writeFile(join(root, "dist/assets/main.js"), "void 1;");
  await collectLicenses(root, output);
  assert.notEqual(
    await readFile(join(output, "inventory.json"), "utf8"),
    original,
  );
});

test("nested dependency versions retain distinct notices and package identities", async (t) => {
  const { root, output, lock } = await fixture(t);
  const nested = "node_modules/widget/node_modules/react";
  lock.packages[nested] = { version: "2.0.0", license: "MIT" };
  await mkdir(join(root, nested), { recursive: true });
  await writeFile(join(root, nested, "LICENSE"), "Nested version notice");
  await writeFile(join(root, "package-lock.json"), JSON.stringify(lock));

  await collectLicenses(root, output);
  const { packages } = JSON.parse(
    await readFile(join(output, "inventory.json"), "utf8"),
  );
  assert.deepEqual(
    packages.map(({ path, name, version }) => ({ path, name, version })),
    [
      { path: "node_modules/react", name: "react", version: "1.0.0" },
      { path: nested, name: "react", version: "2.0.0" },
    ],
  );
  assert.deepEqual(
    await Promise.all(
      packages.map((entry) => readFile(join(output, entry.notices[0]), "utf8")),
    ),
    ["MIT fixture", "Nested version notice"],
  );
});

test("package and notice ordering depends on exact paths, not entry metadata or insertion order", async (t) => {
  const { root, output, lock } = await fixture(t);
  const names = ["widget_z", "widget", "widget-a"];
  for (const name of names) {
    const path = `node_modules/${name}`;
    await mkdir(join(root, path));
    lock.packages[path] = {
      version: "1.0.0",
      license: "MIT",
      toString: "retained metadata",
    };
    for (const notice of ["license.z", "LICENSE", "license.a", "NOTICE"])
      await writeFile(join(root, path, notice), `${path}/${notice}`);
  }
  const expected = [
    "node_modules/react",
    "node_modules/widget",
    "node_modules/widget-a",
    "node_modules/widget_z",
  ];
  let original;
  for (const packages of [
    lock.packages,
    Object.fromEntries(Object.entries(lock.packages).reverse()),
  ]) {
    await writeFile(
      join(root, "package-lock.json"),
      JSON.stringify({ ...lock, packages }),
    );
    await collectLicenses(root, output);
    const inventory = JSON.parse(
      await readFile(join(output, "inventory.json")),
    );
    assert.deepEqual(
      inventory.packages.map((entry) => entry.path),
      expected,
    );
    if (original) assert.deepEqual(inventory.packages, original);
    original = inventory.packages;
    for (const entry of inventory.packages.slice(1)) {
      assert.deepEqual(
        entry.notices,
        ["LICENSE", "NOTICE", "license.a", "license.z"].map(
          (notice) => `${entry.path}/${notice}`,
        ),
      );
      for (const notice of entry.notices)
        assert.equal(await readFile(join(output, notice), "utf8"), notice);
    }
  }
});

test("license collection rejects missing, linked and malformed inputs", async (t) => {
  for (const invalid of [
    "missing",
    "symlink",
    "path",
    "license",
    "index",
    "asset-link",
  ]) {
    await t.test(invalid, async (t) => {
      const { root, output, lock } = await fixture(t);
      if (invalid === "missing")
        await rm(join(root, "node_modules/react/LICENSE"));
      if (invalid === "symlink") {
        await rm(join(root, "node_modules/react/LICENSE"));
        await symlink(
          join(root, "package-lock.json"),
          join(root, "node_modules/react/LICENSE"),
        );
      }
      if (invalid === "path")
        lock.packages["node_modules/../escape"] = {
          version: "1",
          license: "MIT",
        };
      if (invalid === "license")
        delete lock.packages["node_modules/react"].license;
      if (invalid === "index") await rm(join(root, "dist/index.html"));
      if (invalid === "asset-link")
        await symlink(join(root, "package-lock.json"), join(root, "dist/link"));
      await writeFile(join(root, "package-lock.json"), JSON.stringify(lock));
      await assert.rejects(collectLicenses(root, output));
    });
  }
});

test("public legal assets preserve the project license and original runtime notices", async (t) => {
  const { root } = await fixture(t);
  // Legal generation reads the repository license beside the UI project.
  const ui = join(root, "ui");
  await mkdir(ui);
  await copyFile(
    join(root, "package-lock.json"),
    join(ui, "package-lock.json"),
  );
  await mkdir(join(ui, "node_modules/react"), { recursive: true });
  const bytes = Buffer.from("Original copyright\r\nMIT license\n");
  await writeFile(join(ui, "node_modules/react/LICENSE"), bytes);
  await writeFile(join(root, "LICENSE"), "Exact Apache License 2.0\n");
  const emitted = [];
  const plugin = legalAssets(ui);
  await plugin.generateBundle.call({
    emitFile: (asset) => emitted.push(asset),
  });
  assert.deepEqual(
    emitted.map((asset) => asset.fileName),
    ["legal/LICENSE.txt", "legal/third-party-notices.txt"],
  );
  assert.equal(emitted[0].source.toString(), "Exact Apache License 2.0\n");
  assert.ok(emitted[1].source.includes(bytes));
  assert.ok(
    emitted[1].source.includes(Buffer.from("node_modules/react@1.0.0")),
  );
});
