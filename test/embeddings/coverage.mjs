// The owning semantic supervisor calls this only after both workflows and cleanup pass.
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { reportBrowserCoverage } from "../../catalog-ui/scripts/e2e-coverage.mjs";

const [directory, ...extra] = process.argv.slice(2);
if (!directory || extra.length)
  throw new Error("One semantic coverage directory required");
const execution = JSON.parse(
  await readFile(join(directory, "execution.json"), "utf8"),
);
if (
  execution.profile !== "semantic" ||
  execution.passed !== 2 ||
  execution.contexts !== 2
)
  throw new Error("Both semantic browser workflows are required");
await reportBrowserCoverage(directory, execution);
