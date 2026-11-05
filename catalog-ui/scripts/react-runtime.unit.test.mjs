import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import test from "node:test";

// Load configuration without enabling a private instrumented build.
const coverage = process.env.CATALOG_UI_E2E_COVERAGE;
delete process.env.CATALOG_UI_E2E_COVERAGE;
let config;
try {
  ({ default: config } = await import("../vite.config.ts"));
} finally {
  if (coverage !== undefined) process.env.CATALOG_UI_E2E_COVERAGE = coverage;
}
const resolver = config.plugins.find(
  (plugin) => plugin.name === "catalog-react-bindings",
).resolveId;
const source = fileURLToPath(new URL("../src/", import.meta.url));
const runtime = `${source}react-runtime.ts`.replaceAll("\\", "/");

test("shares React bindings only for application imports", () => {
  for (const importer of [
    `${source}main.tsx`,
    `${source}views/Overview.tsx`,
    `${source}views/Overview.tsx?v=1`,
  ])
    assert.equal(resolver("react", importer), runtime);
  for (const importer of [
    undefined,
    runtime,
    `${runtime}?v=1`,
    `${source}react-runtime.test.tsx`,
    `${source}views/Overview.test.tsx`,
    `${source}views/Overview.test.tsx?v=1`,
    fileURLToPath(
      new URL("../node_modules/react-dom/client.js", import.meta.url),
    ),
    fileURLToPath(new URL("../node_modules/react/index.js", import.meta.url)),
    `${source.slice(0, -1)}-other/main.tsx`,
  ])
    assert.equal(resolver("react", importer), undefined, importer);
  for (const name of ["react-dom", "react/jsx-runtime", "./react"])
    assert.equal(resolver(name, `${source}main.tsx`), undefined);
});
