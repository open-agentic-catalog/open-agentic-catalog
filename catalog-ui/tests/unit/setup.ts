import { vi } from "vitest";

// DOM workflows include many renders and accessibility queries under coverage.
// Keep logic-only files on the shorter deadline from vitest.config.ts.
if (typeof document !== "undefined") {
  const { configure } = await import("@testing-library/dom");
  configure({ asyncUtilTimeout: 5000 });
  vi.setConfig({ testTimeout: 15000 });
}
