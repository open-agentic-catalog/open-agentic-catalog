// Compare a disposable generated contract without rewriting checked-in types.
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFileSync } from "node:child_process";

const directory = await mkdtemp(join(tmpdir(), "catalog-ui-client-"));
try {
  const output = join(directory, "schema.d.ts");
  execFileSync(
    process.execPath,
    [
      "node_modules/openapi-typescript/bin/cli.js",
      "../api/openapi/control-v1.yaml",
      "-o",
      output,
    ],
    { stdio: "pipe" },
  );
  if (
    (await readFile(output, "utf8")) !==
    (await readFile("src/api/schema.d.ts", "utf8"))
  ) {
    throw new Error(
      "UI API types have drifted; run npm run generate in catalog-ui/",
    );
  }
} finally {
  await rm(directory, { recursive: true, force: true });
}
