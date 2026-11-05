// Relative asset URLs let the same browser build run at root or a configured host base path.
import { defineConfig, normalizePath } from "vite";
import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";

import { legalAssets } from "./scripts/licenses.mjs";
import { coveragePlugin } from "./scripts/e2e-instrument.mjs";

const sourceDirectory = normalizePath(
  fileURLToPath(new URL("./src/", import.meta.url)),
);
const reactRuntime = normalizePath(
  fileURLToPath(new URL("./src/react-runtime.ts", import.meta.url)),
);

// Group small shared helpers without pulling lazy screen code into startup.
const sharedHelpers = {
  test: (id: string) =>
    normalizePath(id).startsWith(sourceDirectory) &&
    normalizePath(id) !== `${sourceDirectory}views/skill-markdown.ts`,
  minShareCount: 2,
  maxModuleSize: 30000,
  includeDependenciesRecursively: false,
};

export default defineConfig({
  plugins: [
    ...coveragePlugin(
      process.env.CATALOG_UI_E2E_COVERAGE,
      fileURLToPath(new URL(".", import.meta.url)),
    ),
    // Bind application imports once, without redirecting React's own dependencies.
    {
      name: "catalog-react-bindings",
      enforce: "pre",
      resolveId(source, importer) {
        const path = importer
          ? normalizePath(importer.replace(/\?.*$/, ""))
          : undefined;
        if (
          source === "react" &&
          path &&
          path.startsWith(sourceDirectory) &&
          path !== reactRuntime &&
          !/\.test\.[cm]?[jt]sx?$/.test(path)
        )
          return reactRuntime;
      },
    },
    react({ jsxRuntime: "classic" }),
    legalAssets(fileURLToPath(new URL(".", import.meta.url))),
  ],
  oxc: {
    jsx: {
      runtime: "classic",
      pragma: "__catalogElement",
      pragmaFrag: "__catalogFragment",
    },
    jsxInject: `import { createElement as __catalogElement, Fragment as __catalogFragment } from ${JSON.stringify(reactRuntime)}`,
  },
  base: "./",
  build: {
    sourcemap: false,
    assetsInlineLimit: 0,
    rolldownOptions: {
      output: {
        codeSplitting: {
          // Instrumentation changes module sizes and can introduce manual-chunk cycles.
          // Keep coverage private and use automatic splitting for that build.
          groups: process.env.CATALOG_UI_E2E_COVERAGE
            ? []
            : [
                {
                  ...sharedHelpers,
                  name: "shared-initial",
                  tags: ["$initial"],
                },
                { ...sharedHelpers, name: "shared-lazy" },
              ],
        },
      },
    },
  },
});
