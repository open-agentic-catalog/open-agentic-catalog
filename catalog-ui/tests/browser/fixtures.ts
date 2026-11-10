// Real browser fixtures own their service lifetime and require every failure to be accounted for.
import { required } from "../required";
import {
  test as base,
  expect,
  type Page,
  type Response,
} from "@playwright/test";
import { fileURLToPath } from "node:url";
import { writeFile } from "node:fs/promises";
import { startCatalog } from "../../scripts/browser-fixture.mjs";
import { networkObserver } from "../../scripts/network-observer.mjs";
import { personalCommandOutcome } from "../../scripts/personal-evidence.mjs";

import { collectBrowserCoverage } from "../../scripts/e2e-coverage.mjs";

type Item = {
  key: string;
  identifier: string;
  listingId: string;
  resourceId: string;
  versionRevisionId: string;
  submissionId?: string;
  releaseId?: string;
};
/** One disposable service invocation; admin helpers prepare prerequisites, not UI assertions. */
export type Catalog = {
  origin: string;
  basePath: string;
  receipt: { namespaceId: string; publisherId: string; items: Item[] };
  archive?: {
    url: string;
    digest: string;
    size: number;
    documents: { path: string; content: string }[];
    files: {
      path: string;
      size: number;
      digest: string;
      executable: boolean;
    }[];
    components: { kind: string; name: string; path: string }[];
  };
  stopArchives?(): Promise<void>;
  generatedPlugin?: {
    url: string;
    digest: string;
    size: number;
    verifyDownloads(
      documents: { path: string; content: string }[],
    ): Promise<void>;
  };
  close(): Promise<void>;
  diagnostics(): unknown;
  admin<T = Record<string, unknown>>(
    method: string,
    path: string,
    body?: unknown,
    etag?: string | null,
    actor?:
      | "local-admin"
      | "local-contributor"
      | "local-reviewer"
      | "local-publisher"
      | "other-tenant-admin",
  ): Promise<{ data: T; etag: string | null; date: string | null }>;
};

/** Own a fresh Catalog per test and fail teardown on unexpected browser/network outcomes. */
export const test = base.extend<{
  catalog: Catalog;
  browserErrors: string[];
  sourceCoverage: {
    ready(page: Page): Promise<void>;
    flush(page: Page): Promise<void>;
    finish(): Promise<void>;
  };
  webhooks: boolean;
  publisherVerification: boolean;
  demoArchives: boolean;
  authoredPlugin: boolean;
  artwork: boolean;
  curated: boolean;
  publicationAudiences: boolean;
  maintainedIdentities: boolean;
  serviceActivity: boolean;
  network: ReturnType<typeof networkObserver>;
}>({
  webhooks: [false, { option: true }],
  publisherVerification: [false, { option: true }],
  demoArchives: [false, { option: true }],
  authoredPlugin: [false, { option: true }],
  artwork: [false, { option: true }],
  curated: [false, { option: true }],
  publicationAudiences: [false, { option: true }],
  maintainedIdentities: [false, { option: true }],
  serviceActivity: [false, { option: true }],
  sourceCoverage: [
    async ({ context }, use) => {
      const collector = await collectBrowserCoverage(
        context,
        process.env.CATALOG_UI_E2E_COVERAGE,
      );
      await use(collector);
      await collector.finish();
    },
    { auto: true },
  ],
  page: async ({ context, sourceCoverage }, use) => {
    const page = await context.newPage();
    await sourceCoverage.ready(page);
    await use(page);
  },
  network: [
    async ({ context, catalog }, use, info) => {
      const ledger = networkObserver(catalog.origin, catalog.basePath);
      const request = (request: import("@playwright/test").Request) =>
        ledger.request(request.method(), request.url());
      const finished = (request: import("@playwright/test").Request) =>
        ledger.finished(request.method(), request.url());
      const response = (reply: import("@playwright/test").Response) =>
        ledger.observe(reply.request().method(), reply.url(), reply.status());
      const failure = (request: import("@playwright/test").Request) =>
        ledger.failure(
          request.method(),
          request.url(),
          request.failure()?.errorText,
        );
      context.on("response", response);
      context.on("requestfailed", failure);
      context.on("request", request);
      context.on("requestfinished", finished);
      await use(ledger);
      if (
        info.status !== info.expectedStatus ||
        ledger.diagnostics().assets.length ||
        ledger.diagnostics().transports.length
      ) {
        console.log(
          "Owned process diagnostics: " + JSON.stringify(catalog.diagnostics()),
        );
        const evidence = info.outputPath("owned-fixture-state.json");
        await writeFile(
          evidence,
          JSON.stringify({
            network: ledger.diagnostics(),
            processes: catalog.diagnostics(),
          }),
          { mode: 0o600 },
        );
        await info.attach("owned-fixture-state", {
          path: evidence,
          contentType: "application/json",
        });
      }
      context.off("response", response);
      context.off("requestfailed", failure);
      context.off("request", request);
      context.off("requestfinished", finished);
      expect(
        ledger.results(),
        "Unexpected or missing UI network outcomes",
      ).toEqual([]);
    },
    { auto: true },
  ],
  catalog: async (
    {
      webhooks,
      publisherVerification,
      demoArchives,
      authoredPlugin,
      artwork,
      curated,
      publicationAudiences,
      maintainedIdentities,
      serviceActivity,
    },
    use,
    info,
  ) => {
    const catalog = await startCatalog(
      required(process.env.CATALOG_UI_TEST_BUILD),
      fileURLToPath(new URL("../../../", import.meta.url)),
      info.title.includes("subpath") ? "/browser-test" : "",
      {
        webhooks,
        publisherVerification,
        demoArchives,
        authoredPlugin,
        artwork,
        curated,
        publicationAudiences,
        maintainedIdentities,
        serviceActivity,
        ...(process.env.CATALOG_UI_PERFORMANCE
          ? {
              performanceScale: process.env.CATALOG_UI_PERFORMANCE_SCALE ?? "3",
            }
          : {}),
      },
    );
    try {
      await use(catalog);
    } finally {
      await catalog.close();
    }
  },
  browserErrors: [
    async ({ page, context }, use) => {
      const errors: string[] = [];
      const observe = (page: Page) => {
        page.on("pageerror", (error) => errors.push(error.message));
        page.on("console", (message) => {
          if (
            message.type() === "error" &&
            !message.text().startsWith("Failed to load resource:")
          )
            errors.push(message.text());
        });
      };
      observe(page);
      context.on("page", observe);
      await use(errors);
      context.off("page", observe);
      expect(errors, "Unexpected JavaScript/React errors").toEqual([]);
    },
    { auto: true },
  ],
});
export { expect };

/** Sign in through the real development chooser at a root or nested UI path. */
export async function login(
  page: Page,
  catalog: Catalog,
  alias = "local-reader",
  path = "/catalog",
) {
  await page.goto(catalog.origin + catalog.basePath + path);
  await page.locator(`[data-login-alias="${alias}"]`).click();
  await expect(page.getByTitle("Account", { exact: true })).toBeVisible();
}

/** Confirm the visible prepared command and wait for its dialog to close. */
export async function confirmCommand(page: Page) {
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  await expect(page.getByRole("dialog")).not.toBeVisible();
}

/** Check a prepared personal command before a refused command's still-open dialog can time out. */
export async function confirmPersonalCommand(
  page: Page,
  response: Promise<Response>,
  status: number,
) {
  const [reply] = await Promise.all([
    response,
    page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click(),
  ]);
  const problem: unknown = await reply.json().catch(() => undefined);
  expect(
    reply.status(),
    JSON.stringify(
      personalCommandOutcome({
        status: reply.status(),
        method: reply.request().method(),
        problem,
        requestId: reply.headers()["x-request-id"],
      }),
    ),
  ).toBe(status);
  await expect(page.getByRole("dialog")).not.toBeVisible();
  return reply;
}

/** Trigger a real reload without waiting for the navigation deliberately cancelled by the user. */
export async function dismissNativeReload(page: Page) {
  const dialog = page.waitForEvent("dialog").then(async (dialog) => {
    const type = dialog.type();
    await dialog.dismiss();
    expect(type).toBe("beforeunload");
  });
  await Promise.all([dialog, page.evaluate(() => window.location.reload())]);
}

/** Open the native account disclosure only when the journey needs a personal action. */
export async function openAccount(page: Page) {
  if ((await page.locator("details.account").getAttribute("open")) === null)
    await page.getByTitle("Account", { exact: true }).click();
}

/** Use the real sign-out action without leaving Account open in every login fixture. */
export async function signOut(page: Page) {
  await openAccount(page);
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
}
