// Lint maintained client/tooling source with separate browser and Node globals and explicit hook rules.
import js from "@eslint/js";
import { defineConfig, globalIgnores } from "eslint/config";
import tseslint from "typescript-eslint";
import hooks from "eslint-plugin-react-hooks";
import globals from "globals";
import playwright from "eslint-plugin-playwright";
import testingLibrary from "eslint-plugin-testing-library";
import { fileURLToPath } from "node:url";

const tsconfigRootDir = fileURLToPath(new URL(".", import.meta.url));
const typedRules = {
  "@typescript-eslint/no-floating-promises": "error",
  "@typescript-eslint/no-misused-promises": "error",
  "@typescript-eslint/await-thenable": "error",
  "@typescript-eslint/no-unsafe-assignment": "error",
  "@typescript-eslint/no-unsafe-argument": "error",
  "@typescript-eslint/no-unsafe-call": "error",
  "@typescript-eslint/no-unsafe-member-access": "error",
  "@typescript-eslint/no-unsafe-return": "error",
};

const playwrightRules = {
  "playwright/missing-playwright-await": "error",
  "playwright/no-focused-test": "error",
  "playwright/no-skipped-test": "error",
  "playwright/no-wait-for-timeout": "error",
  "playwright/no-force-option": "error",
  "playwright/valid-expect": "error",
};

export default defineConfig(
  {
    basePath: fileURLToPath(new URL("../", import.meta.url)),
    files: ["test/embeddings/*.mjs"],
    extends: [js.configs.recommended],
    languageOptions: { globals: globals.node },
    plugins: { playwright },
    rules: playwrightRules,
  },
  globalIgnores(["dist/**", "src/api/schema.d.ts"]),
  {
    files: ["**/*.{ts,tsx,mjs}"],
    extends: [js.configs.recommended],
  },
  {
    files: ["**/*.{ts,tsx}"],
    extends: [tseslint.configs.recommended],
    rules: {
      // Object-rest omission deliberately binds fields that must be discarded.
      "@typescript-eslint/no-unused-vars": [
        "error",
        { argsIgnorePattern: "^_", ignoreRestSiblings: true },
      ],
    },
  },
  {
    files: ["src/**/*.{ts,tsx}"],
    languageOptions: {
      parserOptions: { project: "./tsconfig.json", tsconfigRootDir },
    },
    rules: typedRules,
  },
  {
    files: [
      "tests/browser/**/*.ts",
      "tests/unit/setup.ts",
      "playwright.config.ts",
      "vitest.config.ts",
      "vite.config.ts",
    ],
    languageOptions: {
      parserOptions: { project: "./tsconfig.browser.json", tsconfigRootDir },
    },
    rules: typedRules,
  },
  {
    files: [
      "tests/browser/**/*.ts",
      "scripts/packaged-journeys.mjs",
      "scripts/manual-layout.mjs",
      "scripts/publisher-verification-journey.mjs",
      "scripts/mcp-journey.mjs",
      "scripts/ratings-oidc-journey.mjs",
      "scripts/demo-journeys.mjs",
      "scripts/packaged-test.mjs",
      "scripts/local-password-journey.mjs",
      "scripts/peer-journeys.mjs",
      "scripts/contextforge-journeys.mjs",
      "scripts/ard-explorer-startup.mjs",
      "scripts/ard-explorer-journeys.mjs",
    ],
    plugins: { playwright },
    rules: playwrightRules,
  },
  {
    files: ["src/**/*.test.{ts,tsx}"],
    plugins: { "testing-library": testingLibrary },
    rules: {
      "testing-library/await-async-queries": "error",
      "testing-library/await-async-utils": "error",
      "testing-library/no-await-sync-queries": "error",
      "testing-library/no-wait-for-side-effects": "error",
      "testing-library/no-promise-in-fire-event": "error",
    },
  },
  {
    files: ["src/**/*.{ts,tsx}"],
    languageOptions: { globals: globals.browser },
    plugins: { "react-hooks": hooks },
    // Enforce hook correctness without opting into React Compiler diagnostics.
    rules: {
      "react-hooks/rules-of-hooks": "error",
      "react-hooks/exhaustive-deps": "error",
    },
  },
  {
    files: ["scripts/**/*.mjs", "*.{mjs,ts}", "tests/**/*.{ts,tsx,mjs}"],
    languageOptions: { globals: globals.node },
  },
);
