import test from "node:test";
import { createHash } from "node:crypto";
import CoverageReporter from "./e2e-reporter.mjs";
import assert from "node:assert/strict";
import { mkdtemp, mkdir, writeFile, rm, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createInstrumenter } from "istanbul-lib-instrument";
import { coveragePlugin, coverageSourceFiles } from "./e2e-instrument.mjs";
import {
  reconcileCoverage,
  reportBrowserCoverage,
  combineBrowserCoverage,
} from "./e2e-coverage.mjs";

const instrumenter = createInstrumenter();
instrumenter.instrumentSync(
  "function answer(yes) { if (yes) return 1; return 2; } answer(true);",
  "src/probe.ts",
);
const inventory = { "src/probe.ts": instrumenter.lastFileCoverage() };
const manifest = {
  build: "1".repeat(64),
  javascriptSha256: "0".repeat(64),
  inputs: {},
  inventory,
  sources: {
    "src/probe.ts":
      "function answer(yes) { if (yes) return 1; return 2; } answer(true);",
  },
};
const document = {
  id: "one",
  build: "1".repeat(64),
  coverage: structuredClone(inventory),
};
document.coverage["src/probe.ts"].s["0"] = 2;

async function reportFixture(directory) {
  await mkdir(join(directory, "src"), { recursive: true });
  await mkdir(join(directory, "scripts"), { recursive: true });
  const sources = {
    ...manifest.sources,
    "package-lock.json": "{}",
    "vite.config.ts": "export default {}",
    "scripts/e2e-instrument.mjs": "export default {}",
  };
  const inputs = {};
  for (const [path, source] of Object.entries(sources)) {
    await writeFile(join(directory, path), source);
    inputs[path] = createHash("sha256").update(source).digest("hex");
  }
  return { ...manifest, inputs };
}

test("reconciliation rejects lost maps, counters, mixed builds and duplicate documents", () => {
  assert.throws(() => reconcileCoverage(manifest, []), /No browser coverage/);
  assert.throws(
    () => reconcileCoverage(manifest, [document, document]),
    /duplicate document/,
  );
  assert.throws(
    () => reconcileCoverage(manifest, [{ ...document, build: "other" }]),
    /build mismatch/,
  );
  assert.throws(
    () => reconcileCoverage(manifest, [{ ...document, coverage: {} }]),
    /Missing application/,
  );
  for (const corrupt of [
    (file) => {
      delete file.statementMap["0"];
    },
    (file) => {
      delete file.s["0"];
    },
    (file) => {
      file.s["0"] = -1;
    },
    (file) => {
      file.b["0"] = [];
    },
    (file) => {
      file.s["0"] = [1];
    },
    (file) => {
      file.f["0"] = [];
    },
  ]) {
    const broken = structuredClone(document);
    corrupt(broken.coverage["src/probe.ts"]);
    assert.throws(() => reconcileCoverage(manifest, [broken]));
  }
  const first = reconcileCoverage(manifest, [document]);
  const second = reconcileCoverage(manifest, [document]);
  assert.deepEqual(first.toJSON(), second.toJSON());
  assert.equal(
    inventory["src/probe.ts"].s["0"],
    0,
    "Merging must not alter the zero-hit denominator",
  );
});

test("reports refuse omitted contexts, skipped execution, failed cleanup and changed inputs", async () => {
  const directory = await mkdtemp(join(tmpdir(), "catalog-coverage-report-"));
  try {
    await mkdir(join(directory, "contexts"));
    const fixture = await reportFixture(directory);
    await writeFile(join(directory, "inventory.json"), JSON.stringify(fixture));
    await writeFile(
      join(directory, "contexts/one.json"),
      JSON.stringify({ browser: "test", documents: [document] }),
    );
    const execution = {
      status: "passed",
      passed: 1,
      failed: 0,
      skipped: 0,
      contexts: 1,
      cleanup: "passed",
      profile: "calibration",
    };
    for (const change of [
      { contexts: 2 },
      { skipped: 1 },
      { failed: 1 },
      { status: "interrupted" },
      { cleanup: "failed" },
    ])
      await assert.rejects(
        reportBrowserCoverage(
          directory,
          { ...execution, ...change },
          directory,
        ),
      );
    // Two independently declared contributions pass; an undeclared extra still
    // fails rather than being used to manufacture the expected count.
    await writeFile(
      join(directory, "contexts/two.json"),
      JSON.stringify({
        browser: "test",
        documents: [{ ...document, id: "second-owned" }],
      }),
    );
    await reportBrowserCoverage(
      directory,
      { ...execution, contexts: 2 },
      directory,
    );
    await assert.rejects(
      reportBrowserCoverage(directory, execution, directory),
      /Context contribution count does not match declared execution/,
    );
    await rm(join(directory, "contexts/two.json"));
    await writeFile(
      join(directory, "contexts/empty.json"),
      JSON.stringify({
        browser: "test",
        documents: [{ id: "provider", coverage: {} }],
      }),
    );
    await assert.rejects(
      reportBrowserCoverage(
        directory,
        { ...execution, contexts: 2 },
        directory,
      ),
      /Missing context application contribution/,
    );
    await rm(join(directory, "contexts/empty.json"));
    await reportBrowserCoverage(directory, execution, directory);
    const report = JSON.parse(await readFile(join(directory, "summary.json")));
    await writeFile(
      join(directory, "inventory.json"),
      JSON.stringify({ ...fixture, inputs: {} }),
    );
    await assert.rejects(
      reportBrowserCoverage(directory, execution, directory),
      /Incomplete coverage input inventory/,
    );
    await writeFile(join(directory, "inventory.json"), JSON.stringify(fixture));

    await writeFile(
      join(directory, "inventory.json"),
      JSON.stringify({ ...fixture, build: undefined }),
    );
    await assert.rejects(
      reportBrowserCoverage(directory, execution, directory),
      /Incomplete instrumented build/,
    );
    await writeFile(join(directory, "inventory.json"), JSON.stringify(fixture));

    for (const browser of [undefined, "", 42]) {
      await writeFile(
        join(directory, "contexts/one.json"),
        JSON.stringify({ browser, documents: [document] }),
      );
      await assert.rejects(
        reportBrowserCoverage(directory, execution, directory),
        /Missing browser identity/,
      );
    }
    await writeFile(
      join(directory, "contexts/one.json"),
      JSON.stringify({ browser: "test", documents: [document] }),
    );

    assert.equal(report.files.length, 1);
    assert.equal(report.totals.statements.covered, 1);
    assert.match(
      await readFile(join(directory, "lcov.info"), "utf8"),
      /SF:src\/probe.ts/,
    );
    assert.match(
      await readFile(join(directory, "index.html"), "utf8"),
      /All files/,
    );
    await writeFile(
      join(directory, "src/new.ts"),
      "export const untouched = 1;",
    );
    await assert.rejects(
      reportBrowserCoverage(directory, execution, directory),
      /source inventory changed/,
    );
    await rm(join(directory, "src/new.ts"));
    await writeFile(
      join(directory, "inventory.json"),
      JSON.stringify({
        ...fixture,
        inputs: { ...fixture.inputs, "package-lock.json": "wrong" },
      }),
    );
    await assert.rejects(
      reportBrowserCoverage(directory, execution, directory),
      /source changed/,
    );
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("source identities and coverage inventories preserve exact path ordering", async (t) => {
  const root = await mkdtemp(join(tmpdir(), "catalog-coverage-order-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  await reportFixture(root);
  const names = [
    "\u00e9",
    "a_b",
    "A",
    "a",
    "e\u0301",
    "a-b",
    "\uffff",
    "\ud83d\ude00",
  ];
  for (const name of names)
    await writeFile(join(root, `src/${name}.ts`), "export const value = 1;");
  const expected = [
    "A",
    "a-b",
    "a",
    "a_b",
    "e\u0301",
    "probe",
    "\u00e9",
    "\ud83d\ude00",
    "\uffff",
  ].map((name) => `src/${name}.ts`);
  assert.deepEqual(coverageSourceFiles(root), expected);
  const [plugin] = coveragePlugin(root, root);
  plugin.writeBundle(
    {},
    { main: { type: "chunk", fileName: "main.js", code: "void 0;" } },
  );
  const fixture = JSON.parse(await readFile(join(root, "inventory.json")));
  const hash = createHash("sha256");
  for (const name of [
    ...expected,
    "package-lock.json",
    "vite.config.ts",
    "scripts/e2e-instrument.mjs",
  ])
    hash.update(name).update(await readFile(join(root, name)));
  assert.equal(fixture.build, hash.digest("hex"));
  // A different serialization order must still match the same exact file set.
  fixture.inventory = Object.fromEntries(
    Object.entries(fixture.inventory).reverse(),
  );
  fixture.inputs = Object.fromEntries(Object.entries(fixture.inputs).reverse());
  await writeFile(join(root, "inventory.json"), JSON.stringify(fixture));
  await mkdir(join(root, "contexts"));
  await writeFile(
    join(root, "contexts/one.json"),
    JSON.stringify({
      browser: "test",
      documents: [
        { id: "one", build: fixture.build, coverage: fixture.inventory },
      ],
    }),
  );
  const execution = {
    status: "passed",
    cleanup: "passed",
    passed: 1,
    failed: 0,
    skipped: 0,
    contexts: 1,
    profile: "local",
  };
  const report = await reportBrowserCoverage(root, execution, root);
  assert.equal(report.files.length, expected.length);
  delete fixture.inputs["src/A.ts"];
  await writeFile(join(root, "inventory.json"), JSON.stringify(fixture));
  await assert.rejects(
    reportBrowserCoverage(root, execution, root),
    /Incomplete coverage input inventory/,
  );
});

test("execution evidence counts skipped, repeated and expected-failure cases without private payloads", async () => {
  const directory = await mkdtemp(
    join(tmpdir(), "catalog-coverage-execution-"),
  );
  const previous = process.env.CATALOG_UI_E2E_COVERAGE;
  try {
    process.env.CATALOG_UI_E2E_COVERAGE = directory;
    const reporter = new CoverageReporter();
    reporter.onBegin(
      {},
      {
        allTests: () => [
          {
            title: "private sentinel",
            expectedStatus: "passed",
            results: [{ status: "passed", stdout: ["private sentinel"] }],
          },
          { expectedStatus: "passed", results: [{ status: "skipped" }] },
          { expectedStatus: "passed", results: [] },
          { expectedStatus: "failed", results: [{ status: "failed" }] },
          {
            expectedStatus: "passed",
            results: [{ status: "failed" }, { status: "passed" }],
          },
        ],
      },
    );
    reporter.onEnd({ status: "failed" });
    const raw = await readFile(join(directory, "execution.json"), "utf8");
    assert.doesNotMatch(raw, /private sentinel/);
    assert.deepEqual(JSON.parse(raw), {
      status: "failed",
      passed: 1,
      failed: 2,
      skipped: 2,
      contexts: 1,
    });
  } finally {
    if (previous === undefined) delete process.env.CATALOG_UI_E2E_COVERAGE;
    else process.env.CATALOG_UI_E2E_COVERAGE = previous;
    await rm(directory, { recursive: true, force: true });
  }
});

test("combined reports validate constituents and merge overlapping hits without shrinking the denominator", async () => {
  const root = await mkdtemp(join(tmpdir(), "catalog-coverage-union-"));
  try {
    const fixture = await reportFixture(root);
    const paths = [join(root, "local"), join(root, "oidc")];
    const output = join(root, "combined");
    for (const [index, path] of paths.entries()) {
      await mkdir(join(path, "contexts"), { recursive: true });
      await writeFile(join(path, "inventory.json"), JSON.stringify(fixture));
      const doc = structuredClone(document);
      doc.id = `doc-${index}`;
      if (index) doc.coverage["src/probe.ts"].s["1"] = 1;
      await writeFile(
        join(path, "contexts/one.json"),
        JSON.stringify({ browser: "test", documents: [doc] }),
      );
      await reportBrowserCoverage(
        path,
        {
          status: "passed",
          cleanup: "passed",
          passed: 1,
          failed: 0,
          skipped: 0,
          contexts: 1,
          profile: index ? "oidc" : "local",
        },
        root,
      );
    }
    const before = await readFile(join(paths[0], "summary.json"), "utf8");
    const combined = await combineBrowserCoverage(
      [...paths].reverse(),
      output,
      root,
    );
    assert.deepEqual(combined.profiles, ["local", "oidc"]);
    assert.equal(combined.totals.statements.total, 4);
    assert.equal(combined.totals.statements.covered, 2);
    assert.equal(combined.documents, 2);
    assert.equal(
      await readFile(join(paths[0], "summary.json"), "utf8"),
      before,
    );
    assert.equal(
      JSON.parse(await readFile(join(output, "gaps.json")))[0].statements
        .length,
      2,
    );
    await assert.rejects(
      combineBrowserCoverage([], output, root),
      /explicitly/,
    );
    await assert.rejects(
      combineBrowserCoverage([paths[0], paths[0]], output, root),
      /Duplicate/,
    );
    await assert.rejects(
      combineBrowserCoverage(paths, paths[0], root),
      /replace/,
    );
    for (const change of [
      { passed: 0 },
      { contexts: 2 },
      { documents: 3 },
      { browser: "other" },
      { totals: {} },
      { profile: "unknown" },
      { skipped: 1 },
      { cleanup: "failed" },
    ]) {
      await writeFile(
        join(paths[0], "summary.json"),
        JSON.stringify({ ...JSON.parse(before), ...change }),
      );
      await assert.rejects(combineBrowserCoverage(paths, output, root));
    }
    await writeFile(join(paths[0], "summary.json"), before);
    await writeFile(join(paths[0], "failed.json"), "{}");
    await assert.rejects(
      combineBrowserCoverage(paths, output, root),
      /Failed browser/,
    );
    await rm(join(paths[0], "failed.json"));
    const context = JSON.parse(
      await readFile(join(paths[1], "contexts/one.json")),
    );
    const otherSummary = await readFile(join(paths[1], "summary.json"), "utf8");
    context.browser = "other-browser";
    await writeFile(
      join(paths[1], "contexts/one.json"),
      JSON.stringify(context),
    );
    await writeFile(
      join(paths[1], "summary.json"),
      JSON.stringify({ ...JSON.parse(otherSummary), browser: "other-browser" }),
    );
    await assert.rejects(
      combineBrowserCoverage(paths, output, root),
      /Incompatible/,
    );
    context.browser = "test";
    await writeFile(join(paths[1], "summary.json"), otherSummary);
    context.documents[0].id = "doc-0";
    await writeFile(
      join(paths[1], "contexts/one.json"),
      JSON.stringify(context),
    );
    await assert.rejects(
      combineBrowserCoverage(paths, output, root),
      /duplicate document/,
    );
    await rm(join(paths[1], "contexts/one.json"));
    await assert.rejects(
      combineBrowserCoverage(paths, output, root),
      /Context contribution count does not match declared execution/,
    );
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("execution evidence declares additional independent contexts without retaining annotations", async () => {
  const directory = await mkdtemp(join(tmpdir(), "catalog-coverage-contexts-"));
  const previous = process.env.CATALOG_UI_E2E_COVERAGE;
  try {
    process.env.CATALOG_UI_E2E_COVERAGE = directory;
    const reporter = new CoverageReporter();
    reporter.onBegin(
      {},
      {
        allTests: () => [
          {
            expectedStatus: "passed",
            results: [
              {
                status: "passed",
                annotations: [
                  {
                    type: "coverage-extra-context",
                    description: "private sentinel",
                  },
                  { type: "unrelated", description: "private sentinel" },
                ],
              },
            ],
          },
          {
            expectedStatus: "passed",
            results: [{ status: "passed", annotations: [] }],
          },
        ],
      },
    );
    reporter.onEnd({ status: "passed" });
    const raw = await readFile(join(directory, "execution.json"), "utf8");
    assert.doesNotMatch(raw, /private sentinel|annotations|unrelated/);
    assert.deepEqual(JSON.parse(raw), {
      status: "passed",
      passed: 2,
      failed: 0,
      skipped: 0,
      contexts: 3,
    });
  } finally {
    if (previous === undefined) delete process.env.CATALOG_UI_E2E_COVERAGE;
    else process.env.CATALOG_UI_E2E_COVERAGE = previous;
    await rm(directory, { recursive: true, force: true });
  }
});
