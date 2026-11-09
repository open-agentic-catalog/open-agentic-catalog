import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, readdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { promisify } from "node:util";
import { execFile } from "node:child_process";
import { createServer } from "node:http";
import { peers } from "../../../test/vscode/host-peers.mjs";
import { listenFixture } from "../../../test/vscode/listen.mjs";
import { fixtureWait } from "../../../test/vscode/wait.mjs";

test(
  "owned response waits identify an early HTTP failure and an unobserved close with cleanup",
  { timeout: 5000 },
  async () => {
    let mode = "early";
    const ready = Promise.withResolvers(),
      actualClose = Promise.withResolvers(),
      never = new Promise(() => {}),
      cancellation = new AbortController();
    const server = createServer((_, response) => {
      if (mode === "early") {
        response.writeHead(503);
        response.end("owned refusal");
      } else {
        response.once("close", actualClose.resolve);
        response.write("owned partial body");
        ready.resolve();
      }
    });
    let early, earlyRejected, pending, heldRejected;
    try {
      await listenFixture(server);
      const url = `http://127.0.0.1:${server.address().port}/owned-wait`;
      early = fetch(url, { signal: cancellation.signal }).then(
        async (response) => {
          assert.equal(response.status, 503);
          await response.text();
          throw new Error("Owned early HTTP refusal");
        },
      );
      earlyRejected = assert.rejects(early, /Owned early HTTP refusal/u);
      await assert.rejects(fixtureWait(never, "early HTTP readiness", early), {
        message: "Owned fixture early HTTP readiness failed before readiness",
      });
      await earlyRejected;
      mode = "held";
      pending = fetch(url, { signal: cancellation.signal }).then((response) =>
        response.text(),
      );
      heldRejected = assert.rejects(pending, { name: "AbortError" });
      await fixtureWait(ready.promise, "held HTTP readiness", pending);
      // The real peer still holds its response open. Missing closure must fail
      // locally, before cleanup deliberately aborts that owned request.
      await assert.rejects(
        fixtureWait(
          actualClose.promise,
          "unobserved HTTP close",
          undefined,
          30,
        ),
        {
          message: "Owned fixture unobserved HTTP close timed out",
        },
      );
      cancellation.abort();
      await heldRejected;
      await fixtureWait(actualClose.promise, "actual HTTP response close");
    } finally {
      cancellation.abort();
      server.closeAllConnections();
      await fixtureWait(
        Promise.allSettled(
          [early, earlyRejected, pending, heldRejected].filter(Boolean),
        ),
        "HTTP calibration operation cleanup",
      );
      await fixtureWait(
        new Promise((resolve) => server.close(resolve)),
        "HTTP calibration listener cleanup",
      );
      assert.equal(server.listening, false);
    }
  },
);

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
