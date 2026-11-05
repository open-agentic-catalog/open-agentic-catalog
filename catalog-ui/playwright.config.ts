// Browser tests require invocation-owned build/output paths and keep retries and sensitive traces off.
import { performancePolicy } from "./scripts/performance.mjs";
import { defineConfig } from "@playwright/test";

if (!process.env.CATALOG_UI_TEST_BUILD || !process.env.CATALOG_UI_TEST_OUTPUT)
  throw new Error(
    "Use make test-ui or npm run test:browser for owned fixtures",
  );

const measuring = !!process.env.CATALOG_UI_PERFORMANCE;
const policy = measuring
  ? performancePolicy(process.env.CATALOG_UI_PERFORMANCE_SCALE)
  : undefined;
if (
  measuring &&
  (process.env.CATALOG_UI_VISUAL || process.env.CATALOG_UI_E2E_COVERAGE)
)
  throw new Error("Performance requires uninstrumented exclusive selection");
const visual = process.env.CATALOG_UI_VISUAL === "1";

export default defineConfig({
  testDir: "./tests/browser",
  testMatch: measuring
    ? "**/performance.spec.ts"
    : visual
      ? "**/visual.spec.ts"
      : "**/*.spec.ts",
  testIgnore: measuring
    ? []
    : visual
      ? ["**/performance.spec.ts"]
      : ["**/visual.spec.ts", "**/performance.spec.ts"],
  snapshotPathTemplate: "{testDir}/snapshots/{arg}{ext}",
  updateSnapshots: "none",
  timeout: policy?.test ?? 60000,
  globalTimeout: policy?.suite ?? 10 * 60 * 1000,
  expect: {
    timeout: policy?.assertion ?? 10000,
    toHaveScreenshot: {
      animations: "disabled",
      caret: "hide",
      maxDiffPixels: 0,
      // Native select corners can vary by a few color levels between captures.
      // No pixels may differ beyond this small perceptual-color tolerance.
      threshold: 0.05,
    },
  },
  // Ordinary journeys own independent processes, ports, state and browser contexts.
  // Keep timing and pixel measurements exclusive. Coverage contexts own unique
  // counter files; four independent workers retain the full ten-minute deadline.
  workers:
    measuring || visual ? 1 : process.env.CATALOG_UI_E2E_COVERAGE ? 4 : 2,
  retries: 0,
  forbidOnly: true,
  reporter:
    measuring || process.env.CATALOG_UI_E2E_COVERAGE
      ? [["list"], ["./scripts/e2e-reporter.mjs"]]
      : "list",
  outputDir: process.env.CATALOG_UI_TEST_OUTPUT,
  use: {
    browserName: "chromium",
    viewport: { width: 1440, height: 900 },
    screenshot: measuring ? "off" : "only-on-failure",
    // Traces/storageState may retain tokens and private response bodies.
    trace: "off",
    video: "off",
    ...(visual
      ? {
          locale: "en-US",
          timezoneId: "UTC",
          colorScheme: "light" as const,
          reducedMotion: "reduce" as const,
          deviceScaleFactor: 1,
        }
      : {}),
  },
});
