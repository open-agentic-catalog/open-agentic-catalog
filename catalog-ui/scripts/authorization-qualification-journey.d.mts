import type { Page } from "@playwright/test";

/** Requires an administrator page in an invocation-owned Catalog fixture. */
export function authorizationQualificationJourney(options: {
  page: Page;
  origin: string;
  basePath?: string;
}): Promise<void>;
