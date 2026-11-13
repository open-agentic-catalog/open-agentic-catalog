// Controlled metadata replies qualify rendered workflows; SQL and retained authority have separate repository tests.
import { test, expect, login } from "./fixtures";
import { expectAlignedOperationControls } from "./operations-layout";
import { doubleText } from "./text-layout";
import { openDisclosure } from "./editor-disclosure";

const sourceId = "00000000-0000-7000-8000-000000004001";
const otherId = "00000000-0000-7000-8000-000000004002";
const tenantId = "00000000-0000-7000-8000-000000000001";
const source = {
  id: sourceId,
  tenantId,
  revision: 1,
  displayName: "Documentation partner",
  status: "active",
  retrievalMode: "indexed",
  updateMode: "manual_review",
  profileId: "ard",
  endpoint: "https://peer.invalid/catalog",
  scope: { kinds: ["agent"] },
  live: {},
  referrals: {},
  nesting: {},
};

for (const path of ["root", "subpath"]) {
  test(`${path}: expired inspection can be refreshed without discarding connection choices`, async ({
    page,
    catalog,
  }) => {
    const inspections: unknown[] = [];
    let connects = 0;
    const retained = {
      id: otherId,
      tenantId,
      requestedUrl: source.endpoint,
      resolvedUrl: source.endpoint,
      profileId: "ard",
      entryCount: 0,
      expiresAt: new Date(Date.now() - 60000).toISOString(),
    };
    await page.route(
      `**/_catalog/api/v1/federation/source-inspections/${otherId}`,
      (route) => route.fulfill({ json: retained }),
    );
    await page.route(
      "**/_catalog/api/v1/federation/source-inspections",
      async (route) => {
        inspections.push(route.request().postDataJSON() as unknown);
        await route.fulfill({
          json: {
            ...retained,
            id: sourceId,
            expiresAt: new Date(Date.now() + 60000).toISOString(),
          },
        });
      },
    );
    await page.route("**/_catalog/api/v1/federation/sources", async (route) => {
      connects++;
      await route.fulfill({ json: source });
    });
    await login(page, catalog, "local-admin", `/inspections/${otherId}`);
    const connect = page.getByRole("button", {
      name: "Connect inspected source",
      exact: true,
    });
    await expect(connect).toBeDisabled();
    await page
      .getByLabel("Display name", { exact: true })
      .fill("Preserve reviewed choices");
    await page
      .getByRole("combobox", { name: "Synchronization", exact: true })
      .selectOption("automatic");
    await page
      .getByRole("textbox", { name: "Reason", exact: true })
      .fill("Retain this reason");
    const reinspect = page.getByRole("button", {
      name: "Reinspect the same interface",
      exact: true,
    });
    await reinspect.click();
    await expect(page.getByRole("dialog")).toContainText(source.endpoint);
    expect(inspections).toHaveLength(0);
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await reinspect.click();
    await page.getByRole("button", { name: "Confirm", exact: true }).click();
    await expect(connect).toBeEnabled();
    await expect(page.getByLabel("Display name", { exact: true })).toHaveValue(
      "Preserve reviewed choices",
    );
    await expect(
      page.getByRole("combobox", { name: "Synchronization", exact: true }),
    ).toHaveValue("automatic");
    await expect(
      page.getByRole("textbox", { name: "Reason", exact: true }),
    ).toHaveValue("Retain this reason");
    expect(inspections).toEqual([{ url: source.endpoint }]);
    await connect.click();
    await expect(page.getByRole("dialog")).toContainText(sourceId);
    await page.setViewportSize({ width: 1440, height: 2400 });
    await page
      .getByRole("dialog")
      .evaluate((element) => element.scrollTo(0, 0));
    await page.getByRole("dialog").screenshot({
      path: test.info().outputPath("source-confirmation.png"),
    });
    await page.setViewportSize({ width: 390, height: 844 });
    await doubleText(page);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    expect(
      await page
        .getByRole("dialog")
        .evaluate((element) => element.scrollWidth <= element.clientWidth),
    ).toBe(true);
    await page.getByRole("dialog").screenshot({
      path: test.info().outputPath("source-confirmation-enlarged-narrow.png"),
    });
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(connects).toBe(0);
  });

  test(`${path}: source filters and exact retained evidence preserve separate parent admission`, async ({
    page,
    catalog,
    network,
  }) => {
    const reads: URL[] = [];
    await page.route(
      "**/_catalog/api/v1/federation/sources?**",
      async (route) => {
        const url = new URL(route.request().url());
        reads.push(url);
        await route.fulfill({
          json: {
            items:
              url.searchParams.has("displayName") ||
              url.searchParams.has("status")
                ? []
                : [source],
          },
        });
      },
    );
    let metadataReads = 0;
    await page.route(
      `**/_catalog/api/v1/federation/sources/${sourceId}`,
      async (route) => {
        const reply =
          metadataReads === 0
            ? { ...source, id: otherId }
            : metadataReads === 1
              ? { ...source, tenantId: otherId }
              : source;
        metadataReads++;
        await route.fulfill({ json: reply, headers: { ETag: '"rev-1"' } });
      },
    );
    await login(page, catalog, "local-admin", "/sources");
    await expect(
      page.getByRole("heading", { name: source.displayName }),
    ).toBeVisible();
    await page.setViewportSize({ width: 1440, height: 1000 });
    await expectAlignedOperationControls([
      page.getByLabel("Source name (exact match)", { exact: true }),
      page.getByRole("combobox", { name: "Source status", exact: true }),
    ]);
    const initial = reads.length;
    await page
      .getByLabel("Source name (exact match)", { exact: true })
      .fill(" Exact Name ");
    await page
      .getByRole("combobox", { name: "Source status", exact: true })
      .selectOption("paused");
    expect(reads).toHaveLength(initial);
    await page
      .getByRole("button", { name: "Apply source filters", exact: true })
      .click();
    await expect.poll(() => reads.length).toBe(initial + 1);
    expect(reads.at(-1)?.searchParams.get("displayName")).toBe(" Exact Name ");
    expect(reads.at(-1)?.searchParams.get("status")).toBe("paused");
    await page
      .getByRole("button", { name: "Clear source filters", exact: true })
      .click();
    await expect.poll(() => reads.length).toBe(initial + 2);
    expect(reads.at(-1)?.searchParams.has("displayName")).toBe(false);
    await page.getByLabel("Exact Source ID", { exact: true }).fill(sourceId);
    await page
      .getByRole("button", { name: "Open Source", exact: true })
      .click();
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Test source", exact: true }),
    ).toHaveCount(0);
    await page
      .getByRole("button", { name: "Reload source", exact: true })
      .click();
    await expect.poll(() => metadataReads).toBe(2);
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Test source", exact: true }),
    ).toHaveCount(0);
    await page
      .getByRole("button", { name: "Reload source", exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: "Test source", exact: true }),
    ).toBeVisible();
    expect(metadataReads).toBe(3);
    expect(new URL(page.url()).pathname).toBe(
      `${catalog.basePath}/sources/${sourceId}`,
    );

    const observations: URL[] = [];
    await page.route("**/_catalog/api/v1/observations?**", async (route) => {
      const url = new URL(route.request().url());
      observations.push(url);
      if (url.searchParams.has("sourceId")) {
        await route.fulfill({
          status: 404,
          contentType: "application/problem+json",
          json: {
            type: "about:blank",
            title: "Not found",
            status: 404,
            code: "NOT_FOUND",
          },
        });
      } else await route.fulfill({ json: { items: [] } });
    });
    await page.route(
      `**/_catalog/api/v1/federation/snapshots/${otherId}`,
      async (route) => {
        await route.fulfill({
          json: {
            id: otherId,
            sourceId,
            sourceRevision: 1,
            state: "complete",
            startedAt: "2026-09-01T00:00:00Z",
            completedAt: "2026-09-01T00:00:01Z",
            memberCount: 0,
            added: 0,
            updated: 0,
            withdrawn: 0,
            enumerationComplete: true,
            reconcileCurrentView: true,
            withdrawalAuthority: false,
          },
        });
      },
    );
    network.expectFailure("GET", "/_catalog/api/v1/observations", 404);
    await page.goto(
      `${catalog.origin}${catalog.basePath}/observations?source=${sourceId}`,
    );
    await expect(page.getByRole("alert")).toBeVisible();
    await openDisclosure(page, "Find evidence after source removal");
    await expect(
      page.getByText(
        /Source removal does not mean its saved evidence was deleted/,
      ),
    ).toBeVisible();
    await page
      .getByRole("combobox", { name: "Show entries", exact: true })
      .selectOption("all");
    expect(observations).toHaveLength(1);
    await page
      .getByRole("button", { name: "Clear observation filters", exact: true })
      .click();
    await expect(
      page.getByText("No matching observations.", { exact: true }),
    ).toBeVisible();
    expect(observations.at(-1)?.searchParams.has("sourceId")).toBe(false);
    await page.getByLabel("Exact Snapshot ID", { exact: true }).fill(otherId);
    await page
      .getByRole("button", { name: "Open Snapshot", exact: true })
      .click();
    await expect(
      page.getByText(/updates the current indexed view/),
    ).toBeVisible();
    await expect(
      page.getByText(/does not mean the evidence was deleted/),
    ).toBeVisible();
  });

  test(`${path}: source preview binding, complete confirmation and idle expiry prevent a new send`, async ({
    page,
    catalog,
  }) => {
    const commands: { key: string | undefined; body: unknown }[] = [];
    let writes = 0;
    await page.route(
      `**/_catalog/api/v1/federation/sources/${sourceId}`,
      async (route) => {
        if (route.request().method() !== "GET") writes++;
        await route.fulfill({ json: source, headers: { ETag: '"rev-1"' } });
      },
    );
    await page.route(
      `**/_catalog/api/v1/federation/sources/${sourceId}/change-previews`,
      async (route) => {
        commands.push({
          key: route.request().headers()["idempotency-key"],
          body: route.request().postDataJSON() as unknown,
        });
        await route.fulfill({
          json: {
            id: otherId,
            tenantId,
            sourceId: commands.length === 1 ? otherId : sourceId,
            sourceRevision: 1,
            intent: "update",
            currentMatched: 3,
            proposedMatched: 2,
            excludedObservations: 1,
            artworkOverridesAffected: 0,
            sendsQueriesUpstream: false,
            configurationDigest: "sha256:" + "a".repeat(64),
            observationSetDigest: "sha256:" + "b".repeat(64),
            digest: "sha256:" + "c".repeat(64),
            createdAt: new Date().toISOString(),
            expiresAt: new Date(Date.now() + 60000).toISOString(),
          },
        });
      },
    );
    await login(page, catalog, "local-admin", `/sources/${sourceId}`);
    await page.clock.install();
    await page
      .getByRole("button", {
        name: "Preview configuration change or removal",
        exact: true,
      })
      .click();
    await page
      .getByRole("combobox", { name: "Synchronization", exact: true })
      .selectOption("automatic");
    await page
      .getByRole("button", { name: "Preview source change", exact: true })
      .click();
    await expect(page.getByRole("dialog").getByRole("table")).toContainText(
      "automatic",
    );
    await page.getByRole("button", { name: "Confirm", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("outcome is unknown");
    await expect(
      page.getByRole("button", { name: "Apply source change", exact: true }),
    ).toHaveCount(0);
    await page
      .getByRole("button", { name: "Retry same command", exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: "Apply source change", exact: true }),
    ).toBeVisible();
    expect(commands[1]).toEqual(commands[0]);
    await page
      .getByRole("textbox", { name: "Reason", exact: true })
      .fill("Reviewed exact settings");
    await page
      .getByRole("button", { name: "Apply source change", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toContainText(
      "Current / proposed / excluded observations",
    );
    await expect(page.getByRole("dialog")).toContainText("3 / 2 / 1");
    await expect(page.getByRole("dialog").getByRole("table")).toContainText(
      "automatic",
    );
    await page.clock.runFor(61000);
    await expect(
      page
        .getByRole("dialog")
        .getByRole("button", { name: "Confirm", exact: true }),
    ).toBeDisabled();
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "Apply source change", exact: true }),
    ).toBeDisabled();
    await expect(
      page.getByRole("textbox", { name: "Reason", exact: true }),
    ).toHaveValue("Reviewed exact settings");
    expect(commands).toHaveLength(2);
    expect(writes).toBe(0);
  });
}
