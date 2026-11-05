import js from "@eslint/js";
import ts from "typescript-eslint";
import globals from "globals";
export default ts.config(
  { ignores: ["node_modules/**", "dist/**"] },
  js.configs.recommended,
  ...ts.configs.recommended,
  { languageOptions: { globals: { ...globals.node } } },
  {
    files: ["**/media/*.js"],
    languageOptions: {
      globals: { ...globals.browser, acquireVsCodeApi: "readonly" },
    },
  },
  // The native VS Code test entrypoint is loaded as CommonJS by the official host runner.
  {
    files: ["**/*.cjs"],
    rules: { "@typescript-eslint/no-require-imports": "off" },
  },
);
