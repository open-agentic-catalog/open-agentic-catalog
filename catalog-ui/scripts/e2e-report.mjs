// Report existing evidence without launching services or modifying constituent runs.
import { mkdtemp, mkdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { join } from "node:path";
import { combineBrowserCoverage } from "./e2e-coverage.mjs";
const inputs = process.argv.slice(2);
if (!inputs.length) throw new Error("Pass explicit coverage run directories");
const root = fileURLToPath(
  new URL("../../output/coverage/e2e/combined/", import.meta.url),
);
await mkdir(root, { recursive: true });
await combineBrowserCoverage(inputs, await mkdtemp(join(root, "run-")));
