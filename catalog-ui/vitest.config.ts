// Unit coverage includes all maintained client modules and applies explicit per-file floors.
import { defineConfig, mergeConfig } from "vitest/config";
import viteConfig from "./vite.config.ts";

export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      include: ["src/**/*.test.{ts,tsx}"],
      // Bound DOM/V8 workers when the host also runs backend verification.
      maxWorkers: 4,
      coverage: {
        provider: "v8",
        // Include untouched views too; imported-only coverage hides real gaps.
        include: ["src/**/*.{ts,tsx}"],
        exclude: ["src/**/*.test.{ts,tsx}", "src/**/*.d.ts"],
        reportsDirectory: "../output/coverage/catalog-ui",
        reporter: [
          "text",
          "text-summary",
          "html",
          "lcov",
          "json-summary",
          "json",
        ],
        skipFull: false,
        reportOnFailure: true,
        // Current whole-client floor, not the 100% completion target. Raise
        // with new tests; test:coverage:100 checks the full target explicitly.
        thresholds: {
          statements: 99,
          branches: 99,
          functions: 100,
          lines: 99,
          "src/api/*.ts": {
            statements: 100,
            branches: 100,
            functions: 100,
            lines: 100,
            perFile: true,
          },
          "src/views/{SearchMode,SemanticAdmin}.tsx": {
            statements: 100,
            branches: 100,
            functions: 100,
            lines: 100,
            perFile: true,
          },
          "src/{main.tsx,shell/App.tsx,shell/ErrorBoundary.tsx,shell/Icon.tsx,views/APIReference.tsx,views/Access.tsx,views/CodeBlock.tsx,views/Explore.tsx,views/Federation.tsx,views/FederationSearch.tsx,views/IndexedRecord.tsx,views/Listings.tsx,views/Members.tsx,views/Observations.tsx,views/Publication.tsx,views/Resources.tsx,views/Reviews.tsx,views/Service.tsx,views/SourceSnapshots.tsx,views/Versions.tsx,views/Webhooks.tsx,views/representations.tsx,views/authoring.tsx,views/federation-inputs.ts,views/listing-metadata.tsx,views/namespace-access.ts,views/search-filters.ts}":
            {
              statements: 100,
              branches: 100,
              functions: 100,
              lines: 100,
              perFile: true,
            },
        },
      },
    },
  }),
);
