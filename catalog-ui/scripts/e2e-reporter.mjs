// Deliberately omit titles, stdout, errors and attachments from browser execution evidence.
import { writeFileSync } from "node:fs";
import { join } from "node:path";
export default class CoverageReporter {
  onBegin(_config, suite) {
    this.tests = suite.allTests();
  }
  onEnd(result) {
    const counts = { passed: 0, failed: 0, skipped: 0 };
    let contexts = 0;
    for (const test of this.tests) {
      if (
        test.results.length === 1 &&
        test.results[0].status === "passed" &&
        test.expectedStatus === "passed"
      ) {
        counts.passed++;
        // The auto fixture owns one context per case. Additional independent
        // contexts are declared before collection, never inferred from files.
        contexts +=
          1 +
          (test.results[0].annotations ?? []).filter(
            (annotation) => annotation.type === "coverage-extra-context",
          ).length;
      } else if (
        test.results.some((r) => r.status === "skipped") ||
        !test.results.length
      )
        counts.skipped++;
      else counts.failed++;
    }
    writeFileSync(
      join(
        process.env.CATALOG_UI_PERFORMANCE ??
          process.env.CATALOG_UI_E2E_COVERAGE,
        "execution.json",
      ),
      JSON.stringify({
        status: result.status,
        ...counts,
        ...(process.env.CATALOG_UI_E2E_COVERAGE ? { contexts } : {}),
      }),
    );
  }
}
