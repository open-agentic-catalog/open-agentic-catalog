import assert from "node:assert/strict";
import { test } from "node:test";
import {
  assetInventory,
  checkAssets,
  performancePolicy,
  summarizeSamples,
  journeys,
} from "./performance.mjs";

const files = [
  "index.html",
  "entry.js",
  "shared.js",
  "route.js",
  "shared.css",
  "icon.svg",
].map((path) => ({ path, bytes: 100, gzipBytes: 20 }));
const manifest = {
  "index.html": {
    file: "entry.js",
    isEntry: true,
    imports: ["shared"],
    dynamicImports: ["route"],
  },
  shared: { file: "shared.js", css: ["shared.css"], assets: ["icon.svg"] },
  route: { file: "route.js", imports: ["shared"] },
};
const budgets = {
  entryJavaScript: 200,
  entryCSS: 100,
  totalJavaScript: 300,
  totalCSS: 100,
  searchResponse: 65536,
};
const samples = () =>
  journeys.flatMap((journey) =>
    Array.from({ length: 6 }, (_, iteration) => ({
      journey,
      iteration,
      durationMs: iteration === 0 ? 99999 : iteration * 10,
      requests: {
        search: ["bootstrap", "detail"].includes(journey) ? 0 : 1,
        detail: journey === "detail" ? 1 : 0,
        assets: 2,
        other: 1,
      },
      searchBytes: ["bootstrap", "detail"].includes(journey) ? 0 : 4000,
      resources: {
        entries: 2,
        transferBytes: 500,
        encodedBytes: 600,
        decodedBytes: 700,
        zeroTransferEntries: 1,
        assetTransferBytes: 100,
        cachedJavaScriptEntries: 1,
      },
    })),
  );

test("performance deadlines are explicit, bounded and proportional", () => {
  const defaults = performancePolicy();
  assert.equal(defaults.scale, 3);
  assert.equal(defaults.assertion, 30000);
  assert.equal(defaults.test, 180000);
  assert.equal(defaults.suite, 1800000);
  assert.ok(defaults.phase > defaults.suite);
  assert.equal(defaults.seed, 90000);
  assert.equal(defaults.readiness, 90000);
  for (const key of Object.keys(defaults))
    assert.equal(performancePolicy("6")[key], defaults[key] * 2);
  assert.equal(performancePolicy("1").ownership, 1000);
  assert.equal(performancePolicy("10").admin, 100000);
  for (const value of [
    "",
    "0",
    "11",
    "-1",
    "1.5",
    "03",
    " 3",
    "3 ",
    "Infinity",
    "1e1",
    3,
    null,
  ])
    assert.throws(() => performancePolicy(value), /integer/);
});
test("asset accounting deduplicates shared route dependencies and includes every emitted file", () => {
  const value = assetInventory(manifest, files);
  assert.equal(value.entry.javaScript, 200);
  assert.equal(value.entry.css, 100);
  assert.equal(value.routes.route.javaScript, 300);
  assert.equal(value.routes.route.css, 100);
  assert.equal(value.total.files, 6);
  assert.equal(value.total.other, 200);
  assert.equal(value.total.gzipEstimate, 120);
  checkAssets(value, budgets);
  for (const key of Object.keys(budgets).filter(
    (key) => key !== "searchResponse",
  )) {
    checkAssets(value, { ...budgets, [key]: budgets[key] + 1 });
    assert.throws(
      () => checkAssets(value, { ...budgets, [key]: budgets[key] - 1 }),
      /Asset budget/,
    );
    for (const invalid of [undefined, -1, NaN, Infinity, "100", 0])
      assert.throws(() => checkAssets(value, { ...budgets, [key]: invalid }));
  }
});
test("asset inventory rejects missing inputs, traversal, duplicate files and incomplete dependency graphs", () => {
  for (const input of [
    null,
    [],
    {},
    { entry: { file: "entry.js", isEntry: true } },
    { ...manifest, bad: { file: "missing.js" } },
    { ...manifest, route: { file: "route.js", imports: ["missing"] } },
    { ...manifest, route: { file: "route.js", dynamicImports: ["missing"] } },
    { ...manifest, route: { file: "route.js", css: "shared.css" } },
  ])
    assert.throws(() => assetInventory(input, files));
  for (const input of [
    files.slice(1),
    files.map((f) => ({ ...f, bytes: 0 })),
    files.slice(0, -1),
    [...files, files[0]],
    [...files, { path: "app.map", bytes: 1, gzipBytes: 1 }],
    [...files, { path: "../escape", bytes: 1, gzipBytes: 1 }],
    [...files, { path: "/absolute", bytes: 1, gzipBytes: 1 }],
    [...files, { path: "x.js", bytes: NaN, gzipBytes: 1 }],
  ])
    assert.throws(() => assetInventory(manifest, input));
  // A cycle terminates with unique accounting, rather than double-counting shared chunks.
  assert.equal(
    assetInventory(
      { ...manifest, shared: { ...manifest.shared, imports: ["index.html"] } },
      files,
    ).entry.javaScript,
    200,
  );
});
test("complete samples retain slow runs and summarize five observations, excluding warm-up", () => {
  const input = samples();
  const summary = summarizeSamples(input, budgets);
  assert.deepEqual(summary.search, {
    medianMs: 30,
    minMs: 10,
    maxMs: 50,
    samples: 5,
  });
  input.find((s) => s.journey === "search" && s.iteration === 1).durationMs =
    600000;
  assert.equal(summarizeSamples(input, budgets).search.maxMs, 600000);
  input.find((s) => s.journey === "search").searchBytes = 65535;
  summarizeSamples(input, budgets);
});
test("incomplete, malformed, over-budget or duplicate work never becomes a successful report", () => {
  assert.throws(
    () => summarizeSamples(samples().slice(1), budgets),
    /Incomplete/,
  );
  const changes = [
    (s) => {
      s[1].iteration = 0;
    },
    (s) => {
      s[0].journey = "unknown";
    },
    (s) => {
      s[1].durationMs = NaN;
    },
    (s) => {
      s[1].durationMs = 0;
    },
    (s) => {
      s[1].durationMs = "1";
    },
    (s) => {
      s[7].requests.search = 2;
    },
    (s) => {
      s[7].requests.detail = 1;
    },
    (s) => {
      s[7].searchBytes = 65536;
    },
    (s) => {
      s[7].searchBytes = 0;
    },
    (s) => {
      s[7].resources.entries = 0;
    },
    (s) => {
      s[7].resources.assetTransferBytes = 0;
    },
    (s) => {
      s[13].resources.cachedJavaScriptEntries = 0;
    },
    (s) => {
      s[13].requests.other = -1;
    },
  ];
  for (const change of changes) {
    const input = samples();
    change(input);
    assert.throws(() => summarizeSamples(input, budgets));
  }
});
