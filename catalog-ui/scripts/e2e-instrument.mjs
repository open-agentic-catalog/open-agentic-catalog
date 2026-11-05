// Private builds instrument original TS/TSX before Vite transforms or tree shakes it.
import { createInstrumenter } from "istanbul-lib-instrument";
import { createHash } from "node:crypto";
import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { resolve, relative } from "node:path";

export function coverageSourceFiles(ui) {
  const files = [];
  const walk = (path) => {
    for (const entry of readdirSync(resolve(ui, path), {
      withFileTypes: true,
    })) {
      const name = `${path}/${entry.name}`;
      if (entry.isDirectory()) walk(name);
      else if (/\.tsx?$/.test(name) && !/\.(test|d)\.tsx?$/.test(name))
        files.push(name);
    }
  };
  walk("src");
  // Source identities require exact code-unit order, independent of locale.
  return files.sort((a, b) => Number(a > b) - Number(a < b));
}

export function coveragePlugin(directory, ui = process.cwd()) {
  if (!directory) return [];
  const files = coverageSourceFiles(ui);
  const inputs = {};
  const hash = createHash("sha256");
  for (const name of [
    ...files,
    "package-lock.json",
    "vite.config.ts",
    "scripts/e2e-instrument.mjs",
  ]) {
    const bytes = readFileSync(resolve(ui, name));
    inputs[name] = createHash("sha256").update(bytes).digest("hex");
    hash.update(name).update(bytes);
  }
  const build = hash.digest("hex");
  const code = new Map();
  const inventory = {};
  const sources = {};
  for (const name of files) {
    const instrumenter = createInstrumenter({
      esModules: true,
      coverageGlobalScope: "globalThis",
      coverageGlobalScopeFunc: false,
      parserPlugins: ["typescript", "jsx"],
      produceSourceMap: true,
      compact: false,
    });
    sources[name] = readFileSync(resolve(ui, name), "utf8");
    code.set(name, {
      code:
        instrumenter.instrumentSync(sources[name], name) +
        `\nif (globalThis.__catalogCoverageBuild && globalThis.__catalogCoverageBuild !== ${JSON.stringify(build)}) throw new Error("Mixed private coverage builds");\nglobalThis.__catalogCoverageBuild = ${JSON.stringify(build)};`,
      map: instrumenter.lastSourceMap(),
    });
    inventory[name] = instrumenter.lastFileCoverage();
  }
  writeFileSync(
    resolve(directory, "inventory.json"),
    JSON.stringify({ build, inputs, inventory, sources }),
  );
  return [
    {
      name: "catalog-private-e2e-coverage",
      enforce: "pre",
      transform(_source, id) {
        return code.get(relative(ui, id));
      },
      writeBundle(_options, bundle) {
        const javascript = Object.values(bundle)
          .filter((item) => item.type === "chunk")
          .sort((a, b) => a.fileName.localeCompare(b.fileName));
        const digest = createHash("sha256");
        for (const item of javascript)
          digest.update(item.fileName).update(item.code);
        writeFileSync(
          resolve(directory, "inventory.json"),
          JSON.stringify({
            build,
            inputs,
            inventory,
            sources,
            javascriptSha256: digest.digest("hex"),
          }),
        );
      },
    },
  ];
}
