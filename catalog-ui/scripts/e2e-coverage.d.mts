// Only the collector crosses into typed browser fixtures; reporters remain native Node tools.
import type { BrowserContext, Page } from "@playwright/test";
export function collectBrowserCoverage(
  context: BrowserContext,
  directory: string | undefined,
): Promise<{
  ready(page: Page): Promise<void>;
  flush(page: Page): Promise<void>;
  finish(): Promise<void>;
}>;
