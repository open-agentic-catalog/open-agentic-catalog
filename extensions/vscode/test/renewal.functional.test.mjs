import test from "node:test";
import assert from "node:assert/strict";
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
