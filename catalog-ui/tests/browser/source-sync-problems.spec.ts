// Browser controls use finite metadata replies; real SQL and packaged tests own
// synchronization, current-revision selection and retained history assertions.
import { test, expect, login } from "./fixtures";
import AxeBuilder from "@axe-core/playwright";
import { doubleText } from "./text-layout";

for (const location of ["root", "subpath"]) {
  test(`${location}: Overview problems page through current Source outcomes without starting work`, async ({
    page,
    catalog,
  }, info) => {
    const tenantId = "00000000-0000-7000-8000-000000000001";
    const at = "2026-11-01T12:00:00Z";
    const key = (n: number) =>
      `00000000-0000-7000-8000-${String(n).padStart(12, "0")}`;
    const rows = Array.from({ length: 30 }, (_, i) => ({
      id: key(8000 + i),
      tenantId,
      revision: 2,
      displayName: `Partner ${String(i).padStart(2, "0")}`,
      status: "active",
      retrievalMode: "indexed",
      updateMode: "manual_review",
      profileId: "ard",
      endpoint: "https://peer.invalid/catalog",
      scope: { kinds: ["agent"] },
      live: {},
      referrals: {},
      nesting: {},
      lastSyncAt: at,
      lastCompleteSnapshotId: key((i === 29 ? 10000 : 9000) + i),
      latestCompletedSync: {
        snapshotId: key(10000 + i),
        revision: 2,
        state: i < 27 ? "failed" : i < 29 ? "incomplete" : "complete",
        completedAt: at,
      },
    }));
    const summary = {
      tenantId,
      scope: "current_access",
      observedAt: at,
      selectionLimit: 10000,
      total: { value: 30, limited: false },
      states: [
        { state: "active", count: 30 },
        { state: "paused", count: 0 },
        { state: "error", count: 0 },
      ],
      sync: [
        { state: "not_applicable", count: 0 },
        { state: "not_yet_synced", count: 0 },
        { state: "complete", count: 1 },
        { state: "incomplete", count: 2 },
        { state: "failed", count: 27 },
      ],
      historicalComplete: 0,
      problems: rows.slice(0, 5).map((row) => ({
        sourceId: row.id,
        displayName: row.displayName,
        revision: row.revision,
        latest: { revision: 2, state: "failed", completedAt: at },
      })),
    };
    const reads: URL[] = [],
      writes: string[] = [],
      evidence: string[] = [];
    await page.route("**/_catalog/api/v1/overview/sources", (route) =>
      route.fulfill({ json: summary }),
    );
    await page.route("**/_catalog/api/v1/federation/sources?*", (route) => {
      const url = new URL(route.request().url());
      reads.push(url);
      const q = url.searchParams;
      const after = q.get("cursor");
      const selected = rows.filter(
        (row) =>
          (!q.has("syncState") ||
            row.latestCompletedSync.state === q.get("syncState")) &&
          (!q.has("displayName") || row.displayName === q.get("displayName")) &&
          (!q.has("status") || row.status === q.get("status")) &&
          (!after || row.id > after),
      );
      const items = selected.slice(0, 25);
      return route.fulfill({
        json: {
          items,
          ...(selected.length > 25 ? { nextCursor: items[24]!.id } : {}),
        },
      });
    });
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (!url.pathname.includes("/_catalog/api/")) return;
      if (!["GET", "HEAD", "OPTIONS"].includes(request.method()))
        writes.push(url.pathname);
      if (
        /\/federation\/(sources\/|snapshots\/)|\/observations/.test(
          url.pathname,
        )
      )
        evidence.push(url.pathname);
    });
    await login(page, catalog, "local-admin", "/overview?view=operations");
    const overview = page.getByRole("region", {
      name: "Federation sources",
      exact: true,
    });
    await expect(
      overview
        .getByRole("table", { name: "Sync problems", exact: true })
        .locator("tbody tr"),
    ).toHaveCount(5);
    await overview.getByRole("link", { name: "Failed", exact: true }).click();
    const panels = page.locator("section.workspace-panel").filter({
      has: page.getByRole("link", {
        name: "Synchronization history",
        exact: true,
      }),
    });
    await expect(panels).toHaveCount(25);
    expect(reads[0]!.searchParams.get("syncState")).toBe("failed");
    await expect(
      panels.first().getByRole("link", { name: "Inspect completed attempt" }),
    ).toHaveAttribute(
      "href",
      `${catalog.basePath}/source-snapshots/${key(10000)}`,
    );
    await expect(
      panels
        .first()
        .getByRole("link", { name: "Inspect last complete snapshot" }),
    ).toHaveAttribute(
      "href",
      `${catalog.basePath}/source-snapshots/${key(9000)}`,
    );
    const firstIds = await panels
      .locator("h2 a")
      .evaluateAll((links) => links.map((link) => link.getAttribute("href")));
    await page.getByRole("button", { name: "Next page", exact: true }).click();
    await expect(panels).toHaveCount(2);
    const allIds = [
      ...firstIds,
      ...(await panels
        .locator("h2 a")
        .evaluateAll((links) =>
          links.map((link) => link.getAttribute("href")),
        )),
    ];
    expect(new Set(allIds).size).toBe(27);
    expect(
      reads.every((url) => url.searchParams.get("syncState") === "failed"),
    ).toBe(true);
    const recoveredAt = "2026-11-01T12:01:00Z";
    rows[0]!.latestCompletedSync = {
      snapshotId: key(11000),
      revision: 2,
      state: "complete",
      completedAt: recoveredAt,
    };
    rows[0]!.lastCompleteSnapshotId = key(11000);
    rows[0]!.lastSyncAt = recoveredAt;
    const refresh = page.getByRole("button", { name: "Refresh", exact: true });
    await refresh.click();
    await expect(panels).toHaveCount(25);
    await expect(panels.first()).toContainText("Partner 01");
    expect(reads.at(-1)!.searchParams.has("cursor")).toBe(false);
    const readsBeforeDraft = reads.length;
    await page
      .getByRole("combobox", { name: "Synchronization outcome", exact: true })
      .selectOption("incomplete");
    await page
      .getByRole("combobox", { name: "Source status", exact: true })
      .selectOption("active");
    await page
      .getByLabel("Source name (exact match)", { exact: true })
      .fill("Partner 27");
    expect(reads).toHaveLength(readsBeforeDraft);
    await page
      .getByRole("button", { name: "Apply source filters", exact: true })
      .click();
    await expect(panels).toHaveCount(1);
    await expect(panels.first()).toContainText("Incomplete");
    expect(Object.fromEntries(reads.at(-1)!.searchParams)).toEqual({
      limit: "25",
      displayName: "Partner 27",
      status: "active",
      syncState: "incomplete",
    });
    expect(new URL(page.url()).searchParams.has("displayName")).toBe(false);
    await page.reload();
    await expect(panels).toHaveCount(2);
    await expect(
      page.getByRole("textbox", {
        name: "Source name (exact match)",
        exact: true,
      }),
    ).toHaveValue("");
    await expect(
      page.getByRole("combobox", {
        name: "Synchronization outcome",
        exact: true,
      }),
    ).toHaveValue("incomplete");
    await page.goBack();
    await expect(
      page.getByRole("combobox", {
        name: "Synchronization outcome",
        exact: true,
      }),
    ).toHaveValue("failed");
    await page.goForward();
    await expect(
      page.getByRole("combobox", {
        name: "Synchronization outcome",
        exact: true,
      }),
    ).toHaveValue("incomplete");
    await expect(panels).toHaveCount(2);
    const controls = [
      page.getByRole("textbox", {
        name: "Source name (exact match)",
        exact: true,
      }),
      page.getByRole("combobox", { name: "Source status", exact: true }),
      page.getByRole("combobox", {
        name: "Synchronization outcome",
        exact: true,
      }),
    ];
    const boxes = await Promise.all(
      controls.map((control) => control.boundingBox()),
    );
    for (const box of boxes) {
      expect(box).not.toBeNull();
      expect(Math.abs(box!.y - boxes[0]!.y)).toBeLessThanOrEqual(2);
    }
    await page.screenshot({
      path: info.outputPath("source-outcomes-desktop.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 320, height: 900 });
    await page.emulateMedia({ colorScheme: "dark", reducedMotion: "reduce" });
    await doubleText(page);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth + 1,
      ),
    ).toBe(true);
    expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
      violations: [],
      incomplete: [],
    });
    await page.screenshot({
      path: info.outputPath("source-outcomes-narrow.png"),
      fullPage: true,
    });
    await page
      .getByRole("button", { name: "Clear source filters", exact: true })
      .click();
    await expect(
      page.getByRole("combobox", {
        name: "Synchronization outcome",
        exact: true,
      }),
    ).toHaveValue("");
    expect(reads.at(-1)!.searchParams.has("syncState")).toBe(false);
    expect(writes).toEqual([]);
    expect(evidence).toEqual([]);
  });
}
