import { execFileSync } from "node:child_process";
import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
execFileSync(
  process.execPath,
  [
    new URL(
      "../../../sdk/typescript/node_modules/typescript/bin/tsc",
      import.meta.url,
    ).pathname,
    "--noEmit",
    "--strict",
    "--noUncheckedIndexedAccess",
    "--exactOptionalPropertyTypes",
    "--module",
    "NodeNext",
    "--moduleResolution",
    "NodeNext",
    "--target",
    "ES2022",
    new URL("./types.ts", import.meta.url).pathname,
  ],
  { stdio: "inherit", timeout: 30000 },
);

// Compile the actual complete README examples, not a second handwritten copy.
const sdk = fileURLToPath(new URL("../../../sdk/typescript/", import.meta.url));
const readme = await readFile(join(sdk, "README.md"), "utf8");
const examples = [
  ...readme.matchAll(
    /<!-- sdk-example: ([a-z-]+) -->\s+```ts\n([\s\S]*?)\n```/gu,
  ),
];
assert.deepEqual(
  examples.map((match) => match[1]),
  [
    "first-request",
    "cancellable-read",
    "create-version-bytes",
    "read-revision-bytes",
    "search-local-pages",
    "read-local-result",
    "update-namespace",
    "operation-types",
  ],
);
const directory = await mkdtemp(join(tmpdir(), "catalog-sdk-readme-types-"));
try {
  for (const [, name, source] of examples)
    await writeFile(join(directory, name + ".mts"), source);
  await writeFile(
    join(directory, "tsconfig.json"),
    JSON.stringify({
      compilerOptions: {
        noEmit: true,
        strict: true,
        noUncheckedIndexedAccess: true,
        exactOptionalPropertyTypes: true,
        module: "NodeNext",
        moduleResolution: "NodeNext",
        target: "ES2022",
        lib: ["ES2022", "DOM", "DOM.Iterable"],
        types: ["node"],
        typeRoots: [join(sdk, "node_modules/@types")],
        paths: {
          "@open-agentic-catalog/client": [join(sdk, "src/index.ts")],
        },
      },
      include: ["*.mts"],
    }),
  );
  execFileSync(
    process.execPath,
    [join(sdk, "node_modules/typescript/bin/tsc"), "-p", directory],
    { stdio: "inherit", timeout: 30000 },
  );
  console.log(`TypeScript README examples compiled: ${examples.length}`);
} finally {
  await rm(directory, { recursive: true, force: true });
}
