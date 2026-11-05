import assert from "node:assert/strict";
import { readdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { execFileSync } from "node:child_process";
import { pathToFileURL } from "node:url";
const root = process.argv[2];
const pkg = join(root, "node_modules/@open-agentic-catalog/client");
const metadata = JSON.parse(await readFile(join(pkg, "package.json"), "utf8"));
assert.deepEqual(Object.keys(metadata.dependencies), [
  "openapi-fetch",
  "openapi-typescript-helpers",
]);
assert.ok(metadata.exports["."].types);
for (const name of [
  "LICENSE",
  "NOTICE",
  "README.md",
  "dist/index.js",
  "dist/index.d.ts",
  "dist/schema.d.ts",
])
  assert.ok((await readFile(join(pkg, name))).length);
async function inventory(path, prefix = "") {
  const result = [];
  for (const item of await readdir(path, { withFileTypes: true })) {
    if (item.isDirectory())
      result.push(
        ...(await inventory(join(path, item.name), prefix + item.name + "/")),
      );
    else result.push(prefix + item.name);
  }
  return result;
}
const files = await inventory(pkg);
assert.ok(
  files.every((name) => !/\.pem$|\.key$|\.log$|^test\/|^src\//u.test(name)),
  files.join("\n"),
);
assert.deepEqual(
  (await readdir(join(root, "node_modules")))
    .filter((n) => !n.startsWith(".") && !n.startsWith("@"))
    .sort(),
  ["openapi-fetch", "openapi-typescript-helpers"],
);
const { createCatalogClient } = await import(
  pathToFileURL(join(pkg, "dist/index.js"))
);
assert.ok(
  createCatalogClient({ origin: "https://catalog.example.com" }).api.GET,
);
console.log(
  "Clean JavaScript tarball consumer imported runtime packages only; declarations, notices and package contents verified",
);

// Compile against the installed declarations, with no compiler in the consumer.
await writeFile(
  join(root, "consumer.ts"),
  `import {createCatalogClient} from '@open-agentic-catalog/client';
const c=createCatalogClient({origin:'https://catalog.example.com'});
void c.api.GET('/.well-known/ard.json');
void c.api.POST('/api/v1/catalog/search',{body:{text:'weather',limit:10}});
`,
);
execFileSync(
  process.execPath,
  [
    new URL(
      "../../../sdk/typescript/node_modules/typescript/bin/tsc",
      import.meta.url,
    ).pathname,
    "--noEmit",
    "--strict",
    "--module",
    "NodeNext",
    "--moduleResolution",
    "NodeNext",
    "--target",
    "ES2022",
    join(root, "consumer.ts"),
  ],
  { cwd: root, stdio: "inherit", timeout: 30000 },
);
console.log(
  "Installed TypeScript exports and declarations compile without source links",
);

const installedLock = JSON.parse(
  await readFile(join(root, "package-lock.json"), "utf8"),
);
assert.deepEqual(
  Object.keys(installedLock.packages).sort(),
  [
    "",
    "node_modules/@open-agentic-catalog/client",
    "node_modules/openapi-fetch",
    "node_modules/openapi-typescript-helpers",
  ].sort(),
);
assert.deepEqual(
  files.sort(),
  [
    "package.json",
    "README.md",
    "LICENSE",
    "NOTICE",
    "dist/index.js",
    "dist/index.d.ts",
    "dist/schema.d.ts",
  ].sort(),
);
