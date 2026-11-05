// Qualify the installed analyzer and maintained configuration in owned projects.
import assert from "node:assert/strict";
import test from "node:test";
import { spawnSync } from "node:child_process";
import {
  mkdtemp,
  mkdir,
  readFile,
  rm,
  symlink,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { runtimeEnvironment } from "./browser-fixture.mjs";

const ui = fileURLToPath(new URL("../", import.meta.url));
const repository = dirname(ui.replace(/\/$/, ""));
const manifest = JSON.parse(await readFile(join(ui, "package.json"), "utf8"));

async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), "catalog-ui-dead-code-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const cwd = join(root, "catalog-ui");
  async function put(path, source) {
    const target = join(cwd, path);
    await mkdir(dirname(target), { recursive: true });
    await writeFile(target, source);
  }
  const files = {
    "knip.json": await readFile(join(ui, "knip.json")),
    "package.json": JSON.stringify({
      name: "owned-dead-code-fixture",
      private: true,
      type: "module",
      scripts: {
        "dead-code:report": manifest.scripts["dead-code:report"],
        "dead-code:check": manifest.scripts["dead-code:check"],
      },
      dependencies: { react: manifest.dependencies.react },
      devDependencies: {
        knip: manifest.devDependencies.knip,
        "@playwright/test": manifest.devDependencies["@playwright/test"],
        vitest: manifest.devDependencies.vitest,
      },
    }),
    "src/main.tsx": `import { used } from "./shared";
import type { Contract } from "./api/schema";
const value: Contract = { name: used() };
console.log(value);
void import("./lazy").then(module => module.screen());`,
    "src/shared.ts": `export function local() { return "used locally"; }
export function used() { return local(); }`,
    "src/react-runtime.ts": await readFile(join(ui, "src/react-runtime.ts")),
    "src/lazy.ts": 'export function screen() { return "lazy screen"; }',
    "src/api/schema.d.ts": `export interface Contract { name: string }
export interface GeneratorOwnedUnused { value: string }`,
    "src/shared.test.ts": 'import { local } from "./shared"; local();',
    "scripts/helper.mjs": 'export const helper = "external tooling";',
    "scripts/helper.unit.test.mjs":
      'import { helper } from "./helper.mjs"; console.log(helper);',
    "tests/browser/visual.spec.ts":
      'import { helper } from "./visual-helper"; console.log(helper);',
    "tests/browser/visual-helper.ts": 'export const helper = "visual fixture";',
    "vitest.config.ts":
      'import { defineConfig } from "vitest/config"; export default defineConfig({ test: { setupFiles: ["./tests/unit/setup.ts"] } });',
    "tests/unit/setup.ts":
      'throw new Error("Unit setup must not execute during analysis");',
    // Loading this actual configuration without owned fixture inputs must fail.
    "playwright.config.ts": await readFile(join(ui, "playwright.config.ts")),
    "scripts/performance.mjs": await readFile(
      join(ui, "scripts/performance.mjs"),
    ),
    "scripts/performance.unit.test.mjs": await readFile(
      join(ui, "scripts/performance.unit.test.mjs"),
    ),
  };
  for (const name of ["dev", "dependencies", "licenses", "packaged-test"])
    files[`scripts/${name}.mjs`] =
      'import { helper } from "./helper.mjs"; throw new Error(helper);';
  for (const [path, source] of Object.entries(files)) await put(path, source);
  await symlink(join(ui, "node_modules"), join(cwd, "node_modules"));
  for (const name of ["Makefile", "go.mod"])
    await writeFile(join(root, name), await readFile(join(repository, name)));
  function run(command, args, directory = cwd) {
    const env = runtimeEnvironment();
    for (const key of ["MAKEFLAGS", "MFLAGS", "NODE_OPTIONS"]) delete env[key];
    const result = spawnSync(command, args, {
      cwd: directory,
      env,
      encoding: "utf8",
      timeout: 30000,
      maxBuffer: 2 * 1024 * 1024,
    });
    assert.ifError(result.error);
    assert.equal(result.signal, null, result.stderr);
    return result;
  }
  return {
    cwd,
    files,
    put,
    scan: (...args) =>
      run(process.execPath, [
        "node_modules/knip/bin/knip.js",
        "--config",
        "knip.json",
        "--no-progress",
        "--reporter",
        "json",
        ...args,
      ]),
    make: (mode) =>
      run(
        "make",
        [
          "--no-print-directory",
          "CATALOG_ENV_FILE=/dev/null",
          `ui-dead-code-${mode}`,
        ],
        root,
      ),
  };
}

test("both graphs preserve lazy, local, generated and external roots without executing fixtures", async (t) => {
  const f = await fixture(t);
  for (const args of [[], ["--production"]]) {
    const result = f.scan(...args);
    assert.equal(result.status, 0, result.stdout + result.stderr);
    assert.deepEqual(JSON.parse(result.stdout), { issues: [] });
  }
  // Qualify the generator exception: declarations still resolve and are inspected.
  const configuration = JSON.parse(f.files["knip.json"]);
  delete configuration.ignoreIssues["src/api/schema.d.ts"];
  await f.put("knip.json", JSON.stringify(configuration));
  const unfiltered = f.scan("--production");
  assert.equal(unfiltered.status, 1, unfiltered.stderr);
  assert.match(unfiltered.stdout, /GeneratorOwnedUnused/);
  // The compiler owns exactly the bridge exports; the file remains analyzed.
  const compiler = JSON.parse(f.files["knip.json"]);
  delete compiler.ignoreIssues["src/react-runtime.ts"];
  await f.put("knip.json", JSON.stringify(compiler));
  const compilerExports = f.scan("--production");
  assert.equal(compilerExports.status, 1, compilerExports.stderr);
  for (const name of [
    "Component",
    "Fragment",
    "Suspense",
    "cloneElement",
    "createContext",
    "createElement",
    "isValidElement",
    "lazy",
    "useCallback",
    "useContext",
    "useEffect",
    "useId",
    "useLayoutEffect",
    "useMemo",
    "useRef",
    "useState",
  ])
    assert.match(compilerExports.stdout, new RegExp(`"name":"${name}"`));
  for (const [path, source] of Object.entries(f.files)) {
    if (path !== "knip.json")
      assert.equal(
        await readFile(join(f.cwd, path), "utf8"),
        source.toString(),
      );
  }
});

test("actual analyzer finds unused code and dependencies, missing imports and test-only application modules", async (t) => {
  const f = await fixture(t);
  await f.put("src/orphan.ts", 'export const orphan = "unused file";');
  await f.put("scripts/orphan.mjs", 'export const orphan = "unused tooling";');
  await f.put(
    "tests/browser/orphan-helper.ts",
    'export const orphan = "unused helper";',
  );
  await f.put("src/test-only.ts", 'export const testOnly = "test support";');
  await f.put(
    "src/test-only.test.ts",
    'import { testOnly } from "./test-only"; console.log(testOnly);',
  );
  await f.put(
    "src/shared.ts",
    `${f.files["src/shared.ts"]}\nexport const unused = 1;\nimport "./missing";\nimport "undeclared-fixture-package";`,
  );
  const pkg = JSON.parse(f.files["package.json"]);
  pkg.devDependencies["unused-fixture-package"] = "1.0.0";
  await f.put("package.json", JSON.stringify(pkg));
  for (const args of [[], ["--production"]]) {
    const result = f.scan(...args);
    assert.equal(result.status, 1, result.stderr);
    const report = JSON.parse(result.stdout);
    const unusedFiles = report.issues
      .filter((issue) => issue.files.length > 0)
      .map((issue) => issue.file);
    assert.ok(unusedFiles.includes("src/orphan.ts"));
    const shared = report.issues.find(
      (issue) => issue.file === "src/shared.ts",
    );
    assert.ok(shared.exports.some((item) => item.name === "unused"));
    assert.ok(shared.unresolved.some((item) => item.name === "./missing"));
    assert.ok(
      shared.unlisted.some(
        (item) => item.name === "undeclared-fixture-package",
      ),
    );
    if (args.length) {
      assert.ok(unusedFiles.includes("src/test-only.ts"));
    } else {
      assert.ok(!unusedFiles.includes("src/test-only.ts"));
      assert.ok(unusedFiles.includes("scripts/orphan.mjs"));
      assert.ok(unusedFiles.includes("tests/browser/orphan-helper.ts"));
      assert.match(result.stdout, /unused-fixture-package/);
    }
    assert.ok(!unusedFiles.includes("src/lazy.ts"));
    assert.ok(!shared.exports.some((item) => item.name === "local"));
  }
});

test("Make report and check route both scopes and fail on invalid configuration or missing local tools", async (t) => {
  const f = await fixture(t);
  for (const mode of ["report", "check"]) {
    const result = f.make(mode);
    assert.equal(result.status, 0, result.stdout + result.stderr);
  }
  // Full graph stays clean: only the second, production scan can detect this.
  await f.put("src/test-only.ts", "export const testOnly = 1;");
  await f.put(
    "src/test-only.test.ts",
    'import { testOnly } from "./test-only"; console.log(testOnly);',
  );
  assert.equal(f.scan().status, 0);
  const report = f.make("report");
  assert.equal(report.status, 0, report.stderr);
  assert.match(report.stdout, /src\/test-only\.ts/);
  const check = f.make("check");
  assert.notEqual(check.status, 0);
  assert.match(check.stdout, /src\/test-only\.ts/);
  await f.put("knip.json", "{invalid");
  for (const mode of ["report", "check"])
    assert.notEqual(f.make(mode).status, 0);
  await f.put("knip.json", f.files["knip.json"]);
  await rm(join(f.cwd, "node_modules"));
  for (const mode of ["report", "check"]) {
    const missing = f.make(mode);
    assert.notEqual(missing.status, 0);
    assert.match(missing.stderr, /Cannot find module/);
  }
});
