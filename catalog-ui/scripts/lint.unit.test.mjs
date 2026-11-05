import assert from "node:assert/strict";
import test from "node:test";
import { ESLint } from "eslint";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";

// Typed overlays must use in-memory text even when the launcher requests a single run.
process.env.TSESTREE_SINGLE_RUN = "false";

const cwd = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const lint = new ESLint({
  cwd,
  overrideConfigFile: resolve(cwd, "eslint.config.mjs"),
});
const semanticLint = new ESLint({
  cwd: resolve(cwd, ".."),
  overrideConfigFile: resolve(cwd, "eslint.config.mjs"),
});

test("all typed scopes reject unsafe values and accept narrowed unknown values", async () => {
  const invalid = `
    export function probe(input: string) {
      const value = JSON.parse(input);
      const title: string = value.title;
      const consume = (text: string) => text;
      consume(value);
      value();
      return value;
    }
  `;
  const valid = `
    export function probe(input: string): string {
      const value: unknown = JSON.parse(input);
      if (value !== null && typeof value === "object" && "title" in value && typeof value.title === "string") return value.title;
      return "";
    }
  `;
  for (const filePath of [
    "src/api/client.ts",
    "src/views/plugin.test.tsx",
    "tests/browser/catalog.spec.ts",
    "vite.config.ts",
  ]) {
    const [bad] = await lint.lintText(invalid, { filePath });
    for (const rule of [
      "assignment",
      "argument",
      "call",
      "member-access",
      "return",
    ])
      assert.ok(
        bad.messages.some(
          (m) => m.ruleId === `@typescript-eslint/no-unsafe-${rule}`,
        ),
        `${filePath}: missing unsafe-${rule}`,
      );
    const [good] = await lint.lintText(valid, { filePath });
    assert.deepEqual(good.messages, [], filePath);
  }
});

test("all compiler profiles enforce indexed access and exact optional fields", () => {
  const file = resolve(cwd, "src/compiler-probe.ts");
  const invalid = `
    const values: string[] = [];
    export const first: string = values[0];
    export const option: { cursor?: string } = { cursor: undefined };
  `;
  const valid = `
    const values: string[] = [];
    export const first: string = values[0] ?? "";
    export const option: { cursor?: string } = {};
  `;
  const check = (source, options) => {
    const host = ts.createCompilerHost(options);
    const original = host.getSourceFile.bind(host);
    host.getSourceFile = (name, ...args) =>
      name === file
        ? ts.createSourceFile(file, source, ts.ScriptTarget.ES2022, true)
        : original(name, ...args);
    return ts.getPreEmitDiagnostics(ts.createProgram([file], options, host));
  };
  for (const profile of [
    "tsconfig.json",
    "tsconfig.build.json",
    "tsconfig.browser.json",
  ]) {
    const parsed = ts.getParsedCommandLineOfConfigFile(
      resolve(cwd, profile),
      {},
      {
        ...ts.sys,
        onUnRecoverableConfigFileDiagnostic: () =>
          assert.fail(`Invalid ${profile}`),
      },
    );
    assert.deepEqual(parsed.errors, []);
    assert.equal(parsed.options.noUncheckedIndexedAccess, true, profile);
    assert.equal(parsed.options.exactOptionalPropertyTypes, true, profile);
    assert.deepEqual(
      check(invalid, parsed.options).map((d) => d.code),
      [2322, 2375],
      profile,
    );
    assert.deepEqual(check(valid, parsed.options), [], profile);
    // Prove the negative controls depend on these two checks, not another error.
    assert.deepEqual(
      check(invalid, {
        ...parsed.options,
        noUncheckedIndexedAccess: false,
        exactOptionalPropertyTypes: false,
      }),
      [],
      profile,
    );
  }
});

// Overlay existing project members in memory; typed probes never edit source files.
test("UI lint catches conditional hooks, missing dependencies and unsafe any", async () => {
  const [result] = await lint.lintText(
    `
    import { useEffect } from "react";
    export function Example({ active, value }: { active: boolean; value: any }) {
      if (active) useEffect(() => { document.title = value; }, []);
      return null;
    }
  `,
    { filePath: "src/shell/App.tsx" },
  );
  const rules = new Set(result.messages.map((message) => message.ruleId));
  for (const rule of [
    "react-hooks/rules-of-hooks",
    "react-hooks/exhaustive-deps",
    "@typescript-eslint/no-explicit-any",
  ])
    assert.ok(rules.has(rule), `Missing enforced rule: ${rule}`);
  assert.ok(result.errorCount >= 3);
});

test("UI lint checks Node helpers with Node globals and accepts valid hooks", async () => {
  const [invalid] = await lint.lintText("missingFunction();", {
    filePath: "scripts/lint-probe.mjs",
  });
  assert.ok(invalid.messages.some((message) => message.ruleId === "no-undef"));
  for (const [filePath, source] of [
    ["scripts/lint-probe.mjs", "export const runtime = process.version;"],
    [
      "src/shell/App.tsx",
      'import { useEffect } from "react"; export function Example({ title }: { title: string }) { useEffect(() => { document.title = title; }, [title]); return null; }',
    ],
  ]) {
    const [valid] = await lint.lintText(source, { filePath });
    assert.deepEqual(valid.messages, []);
  }
});

test("typed browser lint catches lost promises and invalid async callbacks", async () => {
  const [result] = await lint.lintText(
    `import { test, expect } from "./fixtures";
     test("probe", async ({ page }) => {
       page.getByRole("button").click();
       expect(page.getByRole("button")).toBeVisible();
       [1].forEach(async () => { await page.title(); });
       await 1;
     });`,
    { filePath: "tests/browser/catalog.spec.ts" },
  );
  const rules = result.messages.map((m) => m.ruleId);
  assert.equal(
    rules.filter((r) => r === "@typescript-eslint/no-floating-promises").length,
    2,
  );
  for (const rule of [
    "@typescript-eslint/no-misused-promises",
    "@typescript-eslint/await-thenable",
    "playwright/missing-playwright-await",
  ])
    assert.ok(rules.includes(rule), rule);
  const [valid] = await lint.lintText(
    `import { test, expect } from "./fixtures";
     test("probe", async ({ page }) => {
       await page.getByRole("button").click();
       await expect(page.getByRole("button")).toBeVisible();
     });`,
    { filePath: "tests/browser/catalog.spec.ts" },
  );
  assert.deepEqual(valid.messages, []);
});

test("Playwright lint rejects focused, skipped, forced and fixed-wait journeys", async () => {
  const [result] = await lint.lintText(
    `import { test, expect } from "./fixtures";
     test.only("probe", async ({ page }) => {
       test.skip();
       await page.getByRole("button").click({ force: true });
       await page.waitForTimeout(10);
       expect(true);
     });`,
    { filePath: "tests/browser/catalog.spec.ts" },
  );
  for (const rule of [
    "no-focused-test",
    "no-skipped-test",
    "no-force-option",
    "no-wait-for-timeout",
    "valid-expect",
  ])
    assert.ok(
      result.messages.some((m) => m.ruleId === `playwright/${rule}`),
      rule,
    );
  for (const filePath of [
    "scripts/packaged-journeys.mjs",
    "test/embeddings/browser.mjs",
  ]) {
    const [packaged] = await (
      filePath.startsWith("test/") ? semanticLint : lint
    ).lintText(
      'import { expect } from "@playwright/test"; export function run(page) { expect(page.getByRole("button")).toBeVisible(); }',
      { filePath },
    );
    assert.ok(
      packaged.messages.some(
        (m) => m.ruleId === "playwright/missing-playwright-await",
      ),
      filePath,
    );
  }
  const [semantic] = await semanticLint.lintText(
    "export const runtime = process.version; missingFunction();",
    { filePath: "test/embeddings/coverage.mjs" },
  );
  assert.deepEqual(
    semantic.messages.map((m) => m.ruleId),
    ["no-undef"],
  );
});

test("component lint rejects asynchronous query and wait misuse", async () => {
  const [result] = await lint.lintText(
    `import { screen, waitFor, fireEvent } from "@testing-library/react";
     export async function probe() {
       screen.findByRole("button");
       waitFor(() => screen.getByRole("button"));
       await screen.getByRole("button");
       await waitFor(() => fireEvent.click(screen.getByRole("button")));
       fireEvent.click(screen.findByRole("button"));
     }`,
    { filePath: "src/views/plugin.test.tsx" },
  );
  for (const rule of [
    "await-async-queries",
    "await-async-utils",
    "no-await-sync-queries",
    "no-wait-for-side-effects",
    "no-promise-in-fire-event",
  ])
    assert.ok(
      result.messages.some((m) => m.ruleId === `testing-library/${rule}`),
      rule,
    );
});

test("UI lint includes maintained source and tests, excluding only generated inputs and output", async () => {
  for (const path of [
    "src/shell/App.tsx",
    "src/views/workspace.test.tsx",
    "tests/browser/catalog.spec.ts",
    "scripts/browser-fixture.mjs",
    "eslint.config.mjs",
    "vite.config.ts",
    "test/embeddings/browser.mjs",
    "test/embeddings/coverage.mjs",
  ])
    assert.equal(
      await (path.startsWith("test/") ? semanticLint : lint).isPathIgnored(
        path,
      ),
      false,
      path,
    );
  for (const path of [
    "src/api/schema.d.ts",
    "dist/assets/example.js",
    "node_modules/example/index.js",
  ])
    assert.equal(await lint.isPathIgnored(path), true, path);
});
