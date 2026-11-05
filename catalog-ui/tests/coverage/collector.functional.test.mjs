import test from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import {
  mkdtemp,
  rm,
  readFile,
  readdir,
  mkdir,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "@playwright/test";
import { build } from "vite";
import { coveragePlugin } from "../../scripts/e2e-instrument.mjs";
import { createInstrumenter } from "istanbul-lib-instrument";
import {
  collectBrowserCoverage,
  reconcileCoverage,
} from "../../scripts/e2e-coverage.mjs";

test("coverage preserves navigations, lazy code, second pages and closed contexts", async () => {
  const directory = await mkdtemp(
    join(tmpdir(), "catalog-coverage-calibration-"),
  );
  const inventory = {};
  const code = {};
  for (const name of ["a", "b", "lazy", "unused", "popup"]) {
    const i = createInstrumenter({ esModules: true });
    code[name] = i.instrumentSync(
      `globalThis.${name} = (flag) => { if (flag) return 7; return 11; };`,
      `${name}.js`,
    );
    inventory[`${name}.js`] = i.lastFileCoverage();
  }
  const server = createServer((request, response) => {
    const name = request.url.slice(1);
    if (name.endsWith(".js")) {
      response.setHeader("Content-Type", "application/javascript");
      response.end(code[name.slice(0, -3)]);
    } else
      response.end(
        `<script>globalThis.__catalogCoverageBuild='calibration'</script><script src='/${name}.js'></script>`,
      );
  });
  await new Promise((done) => server.listen(0, "127.0.0.1", done));
  const origin = `http://127.0.0.1:${server.address().port}`;
  let browser;
  try {
    browser = await chromium.launch();
    const context = await browser.newContext();
    const collector = await collectBrowserCoverage(context, directory);
    const page = await context.newPage();
    await collector.ready(page);
    await page.goto(`${origin}/a`);
    assert.equal(await page.evaluate(() => globalThis.a(true)), 7);
    await page.evaluate(async (path) => {
      await import(path);
      globalThis.lazy(false);
    }, "/lazy.js");
    await page.goto(`${origin}/b`);
    await page.evaluate(() => globalThis.b(true));
    // A second origin models leaving for an issuer and returning to the app.
    await page.goto(origin.replace("127.0.0.1", "localhost") + "/a");
    await page.goto(`${origin}/b`);
    const second = await context.newPage();
    await collector.ready(second);
    await second.goto(`${origin}/a`);
    await second.evaluate(() => globalThis.a(false));
    await collector.flush(second);
    await second.close();
    // A browser-created tab navigates immediately, before the caller can await ready.
    const opened = context.waitForEvent("page");
    await page.evaluate((url) => {
      globalThis.open(url);
    }, `${origin}/popup`);
    const popup = await opened;
    await collector.ready(popup);
    await popup.waitForFunction(() => typeof globalThis.popup === "function");
    assert.equal(await popup.evaluate(() => globalThis.popup(false)), 11);
    assert.deepEqual(await popup.pageErrors(), []);
    await collector.flush(popup);
    await popup.close();
    await collector.finish();
    await context.close();
    const lateContext = await browser.newContext();
    const latePage = await lateContext.newPage();
    await latePage.goto(`${origin}/a`);
    // Installing after navigation must not silently omit this executed document.
    const late = await collectBrowserCoverage(lateContext, directory);
    await late.ready(latePage);
    await assert.rejects(late.ready(page), /Unobserved browser coverage page/);
    await assert.rejects(late.finish(), /Missing browser coverage checkpoint/);
    await lateContext.close();
    const lostContext = await browser.newContext();
    const lost = await collectBrowserCoverage(lostContext, directory);
    const lostPage = await lostContext.newPage();
    await lost.ready(lostPage);
    await lostPage.goto(`${origin}/a`);
    // Deliberately drop the document's final event; it must not become a valid zero.
    await lostPage.evaluate(() => {
      globalThis.__catalogCoverageReport = () => {};
    });
    await lostPage.close();
    await assert.rejects(lost.finish(), /Incomplete browser coverage/);
    await lostContext.close();
    const documents = [];
    for (const name of await readdir(join(directory, "contexts")))
      documents.push(
        ...JSON.parse(await readFile(join(directory, "contexts", name)))
          .documents,
      );
    const manifest = { build: "calibration", inventory };
    const map = reconcileCoverage(manifest, documents);
    // These separate page contributions overlap statements but add the other branch.
    const pieces = documents.filter((doc) => doc.coverage["a.js"]);
    const firstOnly = reconcileCoverage(manifest, [pieces[0]]);
    assert.equal(
      firstOnly.fileCoverageFor("a.js").toSummary().branches.pct,
      50,
    );
    assert.equal(map.files().length, firstOnly.files().length);
    assert.equal(map.fileCoverageFor("a.js").toSummary().branches.pct, 100);
    assert.equal(map.fileCoverageFor("b.js").toSummary().branches.pct, 50);
    assert.equal(map.fileCoverageFor("lazy.js").toSummary().branches.pct, 50);
    assert.equal(map.fileCoverageFor("popup.js").toSummary().branches.pct, 50);
    assert.ok(
      map.fileCoverageFor("popup.js").toSummary().statements.covered > 0,
    );
    assert.equal(
      map.fileCoverageFor("unused.js").toSummary().statements.pct,
      0,
    );
    assert.throws(
      () => reconcileCoverage({ ...manifest, build: "wrong" }, documents),
      /build mismatch/,
    );
    const broken = structuredClone(documents);
    const app = broken.find((doc) => doc.coverage["a.js"]);
    delete app.coverage["a.js"].statementMap["0"];
    assert.throws(
      () => reconcileCoverage(manifest, broken),
      /source map mismatch/,
    );
  } finally {
    await browser?.close();
    await new Promise((done) => server.close(done));
    await rm(directory, { recursive: true, force: true });
  }
});

test("private Vite instrumentation maps original TypeScript and retains untouched and type-only files", async () => {
  const directory = await mkdtemp(
    join(tmpdir(), "catalog-instrument-calibration-"),
  );
  let browser, server;
  try {
    for (const path of ["src", "scripts"]) await mkdir(join(directory, path));
    const sources = {
      "src/main.ts":
        "import { choose } from './used'; globalThis.result = choose(true);",
      "src/used.ts":
        "export function choose(flag: boolean): number {\n  if (flag) return 7;\n  return 11;\n}\n",
      "src/unused.ts": "export const unused = () => 12;",
      "src/types.ts": "export interface OnlyType { name: string }",
      "src/ignored.test.ts": "throw new Error('not application code');",
      "src/ignored.d.ts": "declare const something: string;",
      "package-lock.json": "{}",
      "vite.config.ts": "",
      "scripts/e2e-instrument.mjs": "calibration",
    };
    for (const [path, source] of Object.entries(sources))
      await writeFile(join(directory, path), source);
    const options = {
      root: directory,
      configFile: false,
      logLevel: "silent",
      build: {
        write: false,
        minify: true,
        lib: { entry: join(directory, "src/main.ts"), formats: ["es"] },
      },
    };
    const normal = await build({
      ...options,
      plugins: coveragePlugin(undefined, directory),
    });
    assert.doesNotMatch(
      normal[0].output[0].code,
      /__coverage__|__catalogCoverageBuild/,
    );
    const instrumented = await build({
      ...options,
      build: {
        ...options.build,
        write: true,
        outDir: join(directory, "assets"),
      },
      plugins: coveragePlugin(directory, directory),
    });
    const code = instrumented[0].output[0].code;
    assert.match(code, /__coverage__/);
    server = createServer((request, response) => {
      response.setHeader("Content-Security-Policy", "script-src 'self'");
      if (request.url === "/bundle.js") {
        response.setHeader("Content-Type", "application/javascript");
        response.end(code);
      } else response.end('<script type="module" src="/bundle.js"></script>');
    });
    await new Promise((done) => server.listen(0, "127.0.0.1", done));
    browser = await chromium.launch();
    const context = await browser.newContext();
    const collector = await collectBrowserCoverage(context, directory);
    const page = await context.newPage();
    await collector.ready(page);
    await page.goto(`http://127.0.0.1:${server.address().port}`);
    assert.equal(await page.evaluate(() => globalThis.result), 7);
    await collector.finish();
    const manifest = JSON.parse(
      await readFile(join(directory, "inventory.json")),
    );
    assert.match(manifest.javascriptSha256, /^[a-f0-9]{64}$/);
    const [record] = await readdir(join(directory, "contexts"));
    const { documents } = JSON.parse(
      await readFile(join(directory, "contexts", record)),
    );
    const map = reconcileCoverage(manifest, documents);
    assert.deepEqual(map.files().sort(), [
      "src/main.ts",
      "src/types.ts",
      "src/unused.ts",
      "src/used.ts",
    ]);
    assert.equal(
      map.fileCoverageFor("src/unused.ts").toSummary().statements.covered,
      0,
    );
    assert.equal(
      map.fileCoverageFor("src/types.ts").toSummary().statements.total,
      0,
    );
    const used = map.fileCoverageFor("src/used.ts");
    assert.equal(used.toSummary().branches.pct, 50);
    const uncovered = Object.keys(used.s)
      .filter((id) => used.s[id] === 0)
      .map((id) => used.statementMap[id].start.line);
    assert.deepEqual(uncovered, [3]);
  } finally {
    await browser?.close();
    if (server?.listening) await new Promise((done) => server.close(done));
    await rm(directory, { recursive: true, force: true });
  }
});

test("the complete instrumented application starts React and loads the public About screen", async () => {
  const directory = await mkdtemp(
    join(tmpdir(), "catalog-full-coverage-calibration-"),
  );
  const ui = fileURLToPath(new URL("../..", import.meta.url));
  const previous = process.env.CATALOG_UI_E2E_COVERAGE;
  let browser, server;
  try {
    process.env.CATALOG_UI_E2E_COVERAGE = directory;
    const assets = join(directory, "dist");
    await build({ root: ui, logLevel: "silent", build: { outDir: assets } });
    server = createServer(async (request, response) => {
      const path = new URL(request.url, "http://127.0.0.1").pathname;
      const reply =
        path === "/runtime.json"
          ? { basePath: "", authMode: "development", documentationUrl: "" }
          : path === "/auth/session"
            ? { access: null, expiresAt: 0 }
            : path === "/auth/options"
              ? {
                  mode: "development",
                  users: [{ alias: "calibration", label: "Calibration" }],
                }
              : undefined;
      if (reply) {
        response.setHeader("Content-Type", "application/json");
        response.end(JSON.stringify(reply));
        return;
      }
      try {
        const file = path.startsWith("/assets/")
          ? resolve(assets, "." + path)
          : join(assets, "index.html");
        if (!file.startsWith(assets + "/")) throw new Error("invalid asset");
        response.setHeader(
          "Content-Type",
          file.endsWith(".js")
            ? "application/javascript"
            : file.endsWith(".css")
              ? "text/css"
              : "text/html",
        );
        response.end(await readFile(file));
      } catch {
        response.writeHead(404).end();
      }
    });
    await new Promise((done) => server.listen(0, "127.0.0.1", done));
    browser = await chromium.launch();
    const context = await browser.newContext();
    const collector = await collectBrowserCoverage(context, directory);
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await collector.ready(page);
    await page.goto(`http://127.0.0.1:${server.address().port}/about`);
    await page.getByRole("heading", { name: "About", exact: true }).waitFor();
    assert.deepEqual(errors, []);
    await collector.finish();
    await context.close();
    const manifest = JSON.parse(
      await readFile(join(directory, "inventory.json"), "utf8"),
    );
    const documents = (
      await Promise.all(
        (await readdir(join(directory, "contexts"))).map(
          async (name) =>
            JSON.parse(
              await readFile(join(directory, "contexts", name), "utf8"),
            ).documents,
        ),
      )
    ).flat();
    const map = reconcileCoverage(manifest, documents);
    for (const file of [
      "src/main.tsx",
      "src/shell/App.tsx",
      "src/views/Help.tsx",
      "src/views/workspace.tsx",
    ])
      assert.ok(
        Object.values(map.fileCoverageFor(file).toJSON().s).some(
          (count) => count > 0,
        ),
        `${file} executes`,
      );
  } finally {
    if (previous === undefined) delete process.env.CATALOG_UI_E2E_COVERAGE;
    else process.env.CATALOG_UI_E2E_COVERAGE = previous;
    await browser?.close();
    if (server) await new Promise((done) => server.close(done));
    await rm(directory, { recursive: true, force: true });
  }
});
