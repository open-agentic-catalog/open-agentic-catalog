// Pure accounting and policy for the opt-in production browser measurement.
export const journeys = [
  "bootstrap",
  "catalog-cold",
  "catalog-warm",
  "page",
  "search",
  "filter",
  "detail",
];

export function performancePolicy(value = "3") {
  if (typeof value !== "string" || !/^(?:[1-9]|10)$/.test(value))
    throw new Error(
      "UI_PERFORMANCE_TIMEOUT_SCALE must be an integer from 1 to 10",
    );
  const scale = Number(value);
  return Object.fromEntries(
    Object.entries({
      scale: 1,
      assertion: 10000,
      test: 60000,
      suite: 600000,
      phase: 720000,
      readiness: 30000,
      probe: 1000,
      ownership: 1000,
      seed: 30000,
      admin: 10000,
    }).map(([key, n]) => [key, n * scale]),
  );
}

function count(value) {
  if (!Number.isSafeInteger(value) || value < 0)
    throw new Error("Invalid performance count");
  return value;
}
function path(value) {
  if (
    typeof value !== "string" ||
    !value ||
    value.startsWith("/") ||
    value.split("/").some((p) => !p || p === "." || p === "..") ||
    /[\\?#]/.test(value) ||
    [...value].some((c) => c.charCodeAt(0) < 32)
  )
    throw new Error("Invalid asset path");
  return value;
}

/** Follow static dependencies for each route, counting each shared asset once. */
export function assetInventory(manifest, files) {
  if (
    !manifest ||
    Array.isArray(manifest) ||
    typeof manifest !== "object" ||
    !Object.keys(manifest).length
  )
    throw new Error("Missing asset manifest");
  const available = new Map();
  for (const file of files) {
    if (available.has(path(file.path))) throw new Error("Duplicate asset");
    count(file.bytes);
    count(file.gzipBytes);
    available.set(file.path, file);
  }
  if (
    !available.get("index.html")?.bytes ||
    files.some((f) => f.path.endsWith(".map"))
  )
    throw new Error("Wrong production build");
  const entryKeys = Object.keys(manifest).filter((k) => manifest[k].isEntry);
  if (entryKeys.length !== 1 || entryKeys[0] !== "index.html")
    throw new Error("Missing HTML entry");
  const closure = (key, seen = new Set(), assets = new Set()) => {
    if (seen.has(key)) return assets;
    const chunk = manifest[key];
    if (!chunk || typeof chunk !== "object")
      throw new Error("Missing manifest dependency");
    seen.add(key);
    const lists = [
      chunk.css ?? [],
      chunk.assets ?? [],
      chunk.imports ?? [],
      chunk.dynamicImports ?? [],
    ];
    if (lists.some((list) => !Array.isArray(list)))
      throw new Error("Invalid manifest dependency list");
    for (const name of [chunk.file, ...lists[0], ...lists[1]]) {
      if (!available.has(path(name))) throw new Error("Missing emitted asset");
      assets.add(name);
    }
    for (const dependency of lists[2]) closure(dependency, seen, assets);
    for (const dependency of lists[3])
      if (!Object.hasOwn(manifest, dependency))
        throw new Error("Missing dynamic dependency");
    return assets;
  };
  const sum = (names) => {
    const summary = {
      javaScript: 0,
      css: 0,
      other: 0,
      gzipEstimate: 0,
      files: names.size,
    };
    for (const name of names) {
      const file = available.get(name);
      summary[
        name.endsWith(".js")
          ? "javaScript"
          : name.endsWith(".css")
            ? "css"
            : "other"
      ] += file.bytes;
      summary.gzipEstimate += file.gzipBytes;
    }
    return summary;
  };
  const entry = closure(entryKeys[0]);
  if (!sum(entry).javaScript) throw new Error("Missing entry JavaScript");
  const routes = Object.fromEntries(
    Object.keys(manifest)
      .sort()
      .map((key) => [key, sum(new Set([...entry, ...closure(key)]))]),
  );
  return {
    entry: sum(entry),
    routes,
    total: sum(new Set(available.keys())),
    files,
  };
}

export function checkAssets(assets, budgets) {
  const actual = {
    entryJavaScript: assets.entry.javaScript,
    entryCSS: assets.entry.css,
    totalJavaScript: assets.total.javaScript,
    totalCSS: assets.total.css,
  };
  for (const [key, value] of Object.entries(actual)) {
    count(value);
    if (!count(budgets[key]) || value > budgets[key])
      throw new Error(`Asset budget exceeded: ${key}`);
  }
}

/** Require all six samples per journey, including the discarded timing warm-up. */
export function summarizeSamples(samples, budgets) {
  if (!Array.isArray(samples) || samples.length !== journeys.length * 6)
    throw new Error("Incomplete performance samples");
  const result = {};
  for (const journey of journeys) {
    const group = samples
      .filter((s) => s.journey === journey)
      .sort((a, b) => a.iteration - b.iteration);
    if (group.length !== 6 || group.some((s, i) => s.iteration !== i))
      throw new Error("Missing or duplicate journey sample");
    for (const sample of group) {
      if (
        typeof sample.durationMs !== "number" ||
        !Number.isFinite(sample.durationMs) ||
        sample.durationMs <= 0
      )
        throw new Error("Invalid duration");
      const { search, detail, assets, other } = sample.requests;
      for (const n of [
        search,
        detail,
        assets,
        other,
        sample.searchBytes,
        ...[
          "entries",
          "transferBytes",
          "encodedBytes",
          "decodedBytes",
          "zeroTransferEntries",
          "assetTransferBytes",
          "cachedJavaScriptEntries",
        ].map((key) => sample.resources[key]),
      ])
        count(n);
      if (
        search !== (journey === "bootstrap" || journey === "detail" ? 0 : 1) ||
        detail !== (journey === "detail" ? 1 : 0)
      )
        throw new Error("Unexpected operation requests");
      if (
        sample.searchBytes >= count(budgets.searchResponse) ||
        (search && !sample.searchBytes)
      )
        throw new Error("Search response budget exceeded");
      if (!sample.resources.entries) throw new Error("Missing resource timing");
      if (
        journey === "catalog-cold" &&
        (!assets || !sample.resources.assetTransferBytes)
      )
        throw new Error("Missing cold asset transfer");
      if (
        journey === "catalog-warm" &&
        (!assets || !sample.resources.cachedJavaScriptEntries)
      )
        throw new Error("Missing warm cache observations");
    }
    const values = group
      .slice(1)
      .map((s) => s.durationMs)
      .sort((a, b) => a - b);
    result[journey] = {
      medianMs: values[2],
      minMs: values[0],
      maxMs: values[4],
      samples: 5,
    };
  }
  return result;
}
