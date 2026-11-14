// Controlled compact replies qualify browser interaction. The packaged Registry,
// PostgreSQL and two-Catalog journeys independently qualify retained membership.
import { test, expect, login, signOut } from "./fixtures";
import type { components } from "../../src/api/schema";
import AxeBuilder from "@axe-core/playwright";
import { doubleText } from "./text-layout";

type Schema = components["schemas"];
const beforeID = "00000000-0000-7000-8000-000000005101";
const afterID = "00000000-0000-7000-8000-000000005102";
const sourceID = "00000000-0000-7000-8000-000000005103";
const at = "2026-09-01T12:00:00Z";
const digest = "sha256:" + "a".repeat(64),
  different = "sha256:" + "b".repeat(64);
const snapshot: Schema["FederationSnapshot"] = {
  id: beforeID,
  tenantId: sourceID,
  sourceId: sourceID,
  sourceRevision: 2,
  state: "complete",
  enumerationComplete: true,
  withdrawalAuthority: false,
  bodyDigest: digest,
  policyId: "browser",
  policyRevision: 1,
  policyDigest: digest,
  memberCount: 27,
  added: 0,
  updated: 0,
  withdrawn: 0,
  startedAt: at,
  completedAt: at,
};
function member(
  snapshotId: string,
  index: number,
): Schema["FederationSnapshotComparisonMember"] {
  const observationId = `00000000-0000-7000-8000-${String(5200 + index + (snapshotId === afterID && index < 26 ? 100 : 0)).padStart(12, "0")}`;
  return {
    observationId,
    identifier: `urn:air:peer.example:agent:${index}`,
    version: "",
    versionPresent: false,
    kind: "agent",
    identityDigest:
      snapshotId === beforeID || index === 26 ? digest : different,
    occurrence: {
      observationId,
      sourceId: sourceID,
      snapshotId,
      sourceRevision: 2,
      retrievalDigest: digest,
      entryLocation: `/entries/${index}${index === 25 ? "/nested-entry".repeat(24) : ""}`,
      exactEntryDigest:
        snapshotId === beforeID || index === 26 ? digest : different,
      evidenceComplete: true,
      observedAt: at,
    },
  };
}
function changes(all: boolean): Schema["FederationSnapshotEntryChange"][] {
  return Array.from({ length: all ? 27 : 26 }, (_, index) => ({
    position: index + 1,
    sourceKey: `key-${String(index).padStart(2, "0")}`,
    change: index === 26 ? "unchanged" : "changed",
    beforeCount: 1,
    afterCount: 1,
    before: member(beforeID, index),
    after: member(afterID, index),
  }));
}

for (const location of ["root", "subpath"]) {
  test(`${location}: source comparison keeps explicit selections through paging, outage recovery and accessible narrow layout`, async ({
    page,
    catalog,
    network,
  }) => {
    const queries: URLSearchParams[] = [],
      writes: string[] = [],
      rawReads: string[] = [];
    let failNext = true;
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (path.includes("/_catalog/api/") && request.method() !== "GET")
        writes.push(path);
      if (path.endsWith("/raw")) rawReads.push(path);
    });
    await page.route(
      `**/_catalog/api/v1/federation/snapshots/${beforeID}`,
      (route) => route.fulfill({ json: snapshot }),
    );
    await page.route(
      `**/_catalog/api/v1/federation/snapshots/${afterID}`,
      (route) => route.fulfill({ json: { ...snapshot, id: afterID } }),
    );
    await page.route(
      `**/_catalog/api/v1/federation/snapshots/${beforeID}/comparison?**`,
      async (route) => {
        const query = new URL(route.request().url()).searchParams;
        queries.push(query);
        expect(query.get("otherSnapshotId")).toBe(afterID);
        expect(query.get("limit")).toBe("25");
        if (query.has("cursor") && failNext) {
          failNext = false;
          await route.fulfill({
            status: 503,
            json: {
              type: "https://catalog.example.com/problems/dependency-unavailable",
              title: "Dependency unavailable",
              status: 503,
              code: "DEPENDENCY_UNAVAILABLE",
              detail: "Retained membership is unavailable.",
            },
          });
          return;
        }
        const all = query.get("view") === "all",
          items = changes(all),
          next = query.has("cursor");
        await route.fulfill({
          json: {
            sourceId: sourceID,
            sourceRevision: 2,
            before: {
              id: beforeID,
              memberCount: 27,
              startedAt: at,
              withdrawalAuthority: false,
            },
            after: {
              id: afterID,
              memberCount: 27,
              startedAt: at,
              withdrawalAuthority: false,
            },
            items: next ? items.slice(25) : items.slice(0, 25),
            ...(!next ? { nextCursor: all ? "all-next" : "changes-next" } : {}),
          },
        });
      },
    );
    await login(page, catalog, "local-admin", `/source-snapshots/${beforeID}`);
    const after = page.getByRole("textbox", {
      name: "After snapshot ID",
      exact: true,
    });
    await expect(after).toBeVisible();
    expect(queries).toHaveLength(0);
    await after.fill(afterID);
    await after.press("Enter");
    const panel = page.locator("#snapshot-comparison");
    await expect(panel.getByRole("heading", { level: 3 })).toHaveCount(25);
    await expect(
      panel.getByText(/Absence alone does not prove upstream deletion/),
    ).toBeVisible();
    await expect(
      panel
        .getByRole("link", {
          name: "Inspect before observation and occurrences",
        })
        .first(),
    ).toHaveAttribute(
      "href",
      `${catalog.basePath}/observations/${member(beforeID, 0).observationId}`,
    );
    network.expectFailure(
      "GET",
      `/_catalog/api/v1/federation/snapshots/${beforeID}/comparison`,
      503,
    );
    await panel.getByRole("button", { name: "Next page", exact: true }).click();
    await expect(panel.getByRole("alert")).toBeVisible();
    await expect(panel.getByRole("heading", { level: 3 })).toHaveCount(0);
    await panel
      .getByRole("button", { name: "Reload comparison", exact: true })
      .click();
    await expect(panel.getByRole("heading", { level: 3 })).toHaveCount(25);
    expect(queries.at(-1)?.has("cursor")).toBe(false);
    await panel.getByRole("button", { name: "Next page", exact: true }).click();
    await expect(panel.getByRole("heading", { level: 3 })).toHaveCount(1);
    await expect(
      panel.getByRole("heading", {
        name: "Changed: urn:air:peer.example:agent:25",
        exact: true,
      }),
    ).toBeVisible();
    await panel
      .getByLabel("Include unchanged entries", { exact: true })
      .check();
    await expect(panel.getByRole("heading", { level: 3 })).toHaveCount(25);
    expect(queries.at(-1)?.get("view")).toBe("all");
    expect(queries.at(-1)?.has("cursor")).toBe(false);
    await panel.getByRole("button", { name: "Next page", exact: true }).click();
    await expect(panel.getByRole("heading", { level: 3 })).toHaveCount(2);
    await expect(
      panel.getByRole("heading", {
        name: "Unchanged: urn:air:peer.example:agent:26",
        exact: true,
      }),
    ).toBeVisible();
    await after.evaluate((element) =>
      element.scrollIntoView({ block: "center" }),
    );
    await page.screenshot({
      path: test.info().outputPath("source-comparison-desktop-controls.png"),
    });
    await panel
      .locator("article")
      .first()
      .screenshot({
        path: test.info().outputPath("source-comparison-desktop-entry.png"),
      });
    const alignment = await panel
      .getByRole("form", { name: "Select source snapshots" })
      .evaluate((form) => {
        const input = form.querySelector("input")!.getBoundingClientRect();
        const button = form.querySelector("button")!.getBoundingClientRect();
        return { inputTop: input.top, buttonTop: button.top };
      });
    expect(
      Math.abs(alignment.inputTop - alignment.buttonTop),
      "The submit action should align with its snapshot input",
    ).toBeLessThanOrEqual(1);
    // Expanding exact occurrence evidence must not push long paths/digests
    // outside the entry or hide adjacent controls at narrower widths.
    const entry = panel.locator("article").first();
    await entry.getByText("Exact before occurrence", { exact: true }).click();
    for (const width of [900, 760, 390, 320]) {
      await page.setViewportSize({ width, height: 900 });
      const geometry = await panel.evaluate((element) => {
        const form = element.querySelector("form")!;
        const input = form.querySelector("input")!.getBoundingClientRect();
        const button = form.querySelector("button")!.getBoundingClientRect();
        const controls = element.querySelector(
          ":scope > div.workspace-toolbar",
        )!;
        const reload = controls
          .querySelector("button")!
          .getBoundingClientRect();
        const link = controls.querySelector("a")!.getBoundingClientRect();
        return {
          overflow:
            document.documentElement.scrollWidth -
            document.documentElement.clientWidth,
          formGap:
            button.top >= input.bottom
              ? button.top - input.bottom
              : Math.abs(button.top - input.top),
          stacked: button.top >= input.bottom,
          actionGap:
            link.top >= reload.bottom
              ? link.top - reload.bottom
              : link.left - reload.right,
          overflowingReferences: Array.from(
            element.querySelectorAll(".handoff-reference, article, pre"),
          ).filter((node) => node.scrollWidth > node.clientWidth + 1).length,
        };
      });
      expect(geometry.overflow).toBeLessThanOrEqual(1);
      expect(geometry.stacked).toBe(width <= 600);
      if (geometry.stacked) expect(geometry.formGap).toBeGreaterThanOrEqual(8);
      else expect(geometry.formGap).toBeLessThanOrEqual(1);
      expect(geometry.actionGap).toBeGreaterThanOrEqual(8);
      expect(geometry.overflowingReferences).toBe(0);
    }
    await entry.getByText("Exact before occurrence", { exact: true }).click();
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.screenshot({
      path: test.info().outputPath("source-comparison-light.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.emulateMedia({ colorScheme: "dark" });
    await page.getByLabel("Appearance", { exact: true }).click();
    await page.getByRole("radio", { name: "Compact", exact: true }).check();
    await page.keyboard.press("Escape");
    await doubleText(page);
    await entry.getByText("Exact before occurrence", { exact: true }).click();
    expect(
      await page.evaluate(
        () =>
          document.documentElement.scrollWidth <=
          document.documentElement.clientWidth + 1,
      ),
    ).toBe(true);
    expect(
      await new AxeBuilder({ page }).include("#snapshot-comparison").analyze(),
    ).toMatchObject({ violations: [], incomplete: [] });
    await page.screenshot({
      path: test.info().outputPath("source-comparison-narrow-dark.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 320, height: 900 });
    expect(
      await panel.evaluate(
        (element) => element.scrollWidth - element.clientWidth,
      ),
    ).toBeLessThanOrEqual(1);
    expect(
      await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      ),
    ).toBeLessThanOrEqual(1);
    await after.evaluate((element) =>
      element.scrollIntoView({ block: "center" }),
    );
    await page.screenshot({
      path: test.info().outputPath("source-comparison-320-controls.png"),
    });
    const entryHeading = entry.getByRole("heading", { level: 3 });
    await entryHeading.evaluate((element) =>
      element.scrollIntoView({ block: "center" }),
    );
    await page.screenshot({
      path: test.info().outputPath("source-comparison-320-entry.png"),
    });
    const occurrence = entry.locator("details[open] pre");
    await occurrence.evaluate((element) =>
      element.scrollIntoView({ block: "center" }),
    );
    expect(
      await occurrence.evaluate(
        (element) => element.scrollWidth - element.clientWidth,
      ),
    ).toBeLessThanOrEqual(1);
    await page.screenshot({
      path: test.info().outputPath("source-comparison-320-occurrence.png"),
    });
    // A history link arrives before its asynchronously loaded target exists.
    // The completed snapshot read must expose and focus that target itself.
    await page.goto(
      `${catalog.origin}${catalog.basePath}/source-snapshots/${beforeID}?compare=${afterID}#snapshot-comparison`,
    );
    await expect(panel).toBeFocused();
    const heading = panel.getByRole("heading", {
      name: "Compare saved entries",
      exact: true,
    });
    await expect(heading).toBeInViewport();
    await expect
      .poll(async () => {
        const target = await heading.boundingBox();
        const banner = await page.getByRole("banner").boundingBox();
        return !!target && !!banner && target.y >= banner.y + banner.height;
      })
      .toBe(true);
    await page.screenshot({
      path: test.info().outputPath("source-comparison-linked-viewport.png"),
    });
    expect(writes).toEqual([]);
    expect(rawReads).toEqual([]);
    await signOut(page);
    await expect(
      page.getByRole("heading", { name: /urn:air:peer.example:agent:/ }),
    ).toHaveCount(0);
  });

  test(`${location}: failed imports and changed source revisions show context without calculating an initial diff`, async ({
    page,
    catalog,
  }) => {
    let comparisons = 0;
    let selectedHeader = snapshot;
    const changedRevisionID = "00000000-0000-7000-8000-000000005104";
    await page.route(
      `**/_catalog/api/v1/federation/snapshots/${beforeID}`,
      (route) => route.fulfill({ json: selectedHeader }),
    );
    await page.route(
      `**/_catalog/api/v1/federation/snapshots/${afterID}`,
      (route) =>
        route.fulfill({
          json: {
            ...snapshot,
            id: afterID,
            state: "failed",
            enumerationComplete: false,
            memberCount: 0,
          },
        }),
    );
    await page.route(
      `**/_catalog/api/v1/federation/snapshots/${changedRevisionID}`,
      (route) =>
        route.fulfill({
          json: { ...snapshot, id: changedRevisionID, sourceRevision: 3 },
        }),
    );
    await page.route(
      `**/_catalog/api/v1/federation/snapshots/${beforeID}/comparison?**`,
      (route) => {
        comparisons++;
        return route.fulfill({ json: {} });
      },
    );
    await login(
      page,
      catalog,
      "local-admin",
      `/source-snapshots/${beforeID}?compare=${afterID}`,
    );
    const panel = page.locator("#snapshot-comparison");
    await expect(panel.getByRole("alert")).toContainText(
      "Comparable membership is unavailable",
    );
    await expect(panel.getByLabel("Ineligible after snapshot")).toContainText(
      "Failed",
    );
    await expect(panel.getByRole("heading", { level: 3 })).toHaveCount(0);
    const after = panel.getByRole("textbox", {
      name: "After snapshot ID",
      exact: true,
    });
    await after.fill(changedRevisionID);
    await after.press("Enter");
    await expect(panel.getByRole("alert")).toContainText(
      "different source revisions",
    );
    await expect(
      panel
        .getByText("Before source revision", { exact: true })
        .locator("xpath=following-sibling::dd[1]"),
    ).toHaveText("2");
    await expect(
      panel
        .getByText("After source revision", { exact: true })
        .locator("xpath=following-sibling::dd[1]"),
    ).toHaveText("3");
    expect(comparisons).toBe(0);
    selectedHeader = { ...snapshot, id: changedRevisionID };
    await page.reload();
    await expect(page.getByRole("alert")).toContainText(
      "This page could not be loaded",
    );
    await expect(panel).toHaveCount(0);
    expect(comparisons).toBe(0);
    selectedHeader = snapshot;
    await page
      .getByRole("button", { name: "Reload snapshot", exact: true })
      .click();
    await expect(panel.getByRole("alert")).toContainText(
      "Comparable membership is unavailable",
    );
    expect(comparisons).toBe(0);
  });
}
