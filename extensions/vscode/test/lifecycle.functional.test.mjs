import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, readdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { promisify } from "node:util";
import { execFile } from "node:child_process";
import { peers } from "../../../test/vscode/host-peers.mjs";

test("a missing owned document TLS input closes peers created during setup", async () => {
  const work = await mkdtemp(join(tmpdir(), "catalog-peer-failure-"));
  const names = [
    "CATALOG_VSCODE_RUN_DIR",
    "CATALOG_VSCODE_PUBLIC_CA",
    "CATALOG_VSCODE_DOCUMENT_KEY",
  ];
  const original = names.map((name) => process.env[name]);
  process.env.CATALOG_VSCODE_RUN_DIR = work;
  process.env.CATALOG_VSCODE_PUBLIC_CA = join(work, "missing-cert");
  process.env.CATALOG_VSCODE_DOCUMENT_KEY = join(work, "missing-key");
  try {
    await assert.rejects(peers(), { code: "ENOENT" });
    assert.deepEqual(await readdir(work), []);
  } finally {
    for (let i = 0; i < names.length; i++) {
      if (original[i] === undefined) delete process.env[names[i]];
      else process.env[names[i]] = original[i];
    }
    await rm(work, { recursive: true, force: true });
  }
});

test("a failed VSIX install cleans its owned profile and local peers", async () => {
  const work = await mkdtemp(join(tmpdir(), "catalog-vscode-failure-"));
  try {
    await assert.rejects(
      promisify(execFile)(
        process.execPath,
        [
          resolve(import.meta.dirname, "../../../test/vscode/host.mjs"),
          "http://127.0.0.1:1",
        ],
        {
          env: {
            ...process.env,
            CATALOG_VSCODE_VSIX: join(work, "absent.vsix"),
            CATALOG_VSCODE_RUN_DIR: work,
          },
          timeout: 30000,
          maxBuffer: 1024 * 1024,
        },
      ),
      (error) => {
        assert.match(error.stdout, /Owned VS Code profile cleaned/u);
        assert.ok(error.code);
        return true;
      },
    );
    assert.deepEqual(await readdir(work), []);
  } finally {
    await rm(work, { recursive: true, force: true });
  }
});
