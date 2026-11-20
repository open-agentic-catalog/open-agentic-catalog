import test from "node:test";
import assert from "node:assert/strict";
import { once } from "node:events";
import { request } from "node:http";
import {
  createBlockedProxy,
  listenFixture,
} from "../../../test/vscode/listen.mjs";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";

for (const mode of ["rotated", "revoked"])
  test(
    `real Linux editor windows handle ${mode} credentials in native SecretStorage`,
    { timeout: 110000 },
    async () => {
      assert.ok(
        process.env.CATALOG_VSCODE_HOST,
        "explicit installed editor is required",
      );
      const { stdout } = await promisify(execFile)(
        "xvfb-run",
        [
          "-a",
          process.execPath,
          fileURLToPath(
            new URL("../../../test/vscode/renewal.mjs", import.meta.url),
          ),
          ...(mode === "revoked" ? ["--revoked"] : []),
        ],
        { env: process.env, timeout: 100000, maxBuffer: 32768 },
      );
      const report = JSON.parse(
        stdout
          .trim()
          .split("\n")
          .find((line) => line.startsWith('{"refreshes"')),
      );
      assert.equal(report.refreshes, 1);
      assert.ok(
        report.results.every(
          (result) =>
            result.later ===
            (mode === "revoked" ? "sign_in_required" : "owned-renewal-reader"),
        ),
      );
    },
  );

test(
  "the editor proxy still refuses requests after a peer resets a rejected tunnel",
  { timeout: 5000 },
  async () => {
    const proxy = createBlockedProxy();
    await listenFixture(proxy);
    try {
      const port = proxy.address().port;
      const closed = new Promise((done) => {
        proxy.once("connect", (_, socket) => socket.once("close", done));
      });
      const connect = request({
        host: "127.0.0.1",
        port,
        method: "CONNECT",
        path: "example.invalid:443",
        agent: false,
      });
      const reply = once(connect, "connect");
      connect.end();
      const [response, socket] = await reply;
      socket.resetAndDestroy();
      assert.equal(response.statusCode, 403);
      await closed;
      const next = await fetch(`http://127.0.0.1:${port}/`);
      assert.equal(next.status, 403);
      await next.arrayBuffer();
    } finally {
      await new Promise((done) => proxy.close(done));
    }
  },
);
