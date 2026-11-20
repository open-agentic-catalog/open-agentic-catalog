// Real API prerequisites feed a read-only maintenance list and explicit renewal handoff.
import AxeBuilder from "@axe-core/playwright";
import { test, expect, login, confirmCommand } from "./fixtures";
import { doubleText } from "./text-layout";
import { paintedContrast } from "./painted-contrast";

for (const location of ["root", "subpath"]) {
  test(`${location}: validation maintenance filters current evidence and renews only after confirmation`, async ({
    page,
    catalog,
  }, testInfo) => {
    const target = catalog.receipt.items.find(
      (item) => item.key === "draft-agent",
    )!;
    const { data: created } = await catalog.admin<{
      revision: { id: string; contentSetDigest: string };
      evidence: { id: string; state: string };
    }>("POST", `/api/v1/resources/${target.resourceId}/versions`, {
      version: "79.0.0",
      representations: [
        {
          role: "primary",
          mediaType: "application/a2a-agent-card+json",
          data: { name: "Incomplete maintenance card" },
        },
      ],
    });
    expect(created.evidence.state).toBe("blocked");
    await login(page, catalog, "local-contributor", "/resources");
    const writes: string[] = [];
    const queries: URL[] = [];
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (url.pathname.includes("/_catalog/") && request.method() === "POST")
        writes.push(url.pathname);
      if (
        request.method() === "GET" &&
        url.pathname.endsWith("/api/v1/validation-maintenance")
      )
        queries.push(url);
    });
    await page
      .getByRole("link", { name: "Validation maintenance", exact: true })
      .click();
    await expect(
      page.getByRole("heading", {
        name: "Validation maintenance",
        exact: true,
      }),
    ).toBeVisible();
    const row = page.getByRole("row").filter({ hasText: created.evidence.id });
    await expect(row).toBeVisible();
    await expect(
      row.getByRole("cell", { name: /No time expiry/ }),
    ).toBeVisible();
    await expect(
      row.getByRole("cell", { name: /Blocked.*Not applicable at observation/ }),
    ).toBeVisible();
    expect(queries.at(-1)?.searchParams.get("state")).toBe("attention");
    expect(writes).toEqual([]);
    const before = queries.length;
    await page
      .getByRole("combobox", { name: "Evidence condition" })
      .selectOption("blocked");
    await page
      .getByRole("spinbutton", { name: "Expiry horizon (hours)" })
      .fill("048");
    expect(queries).toHaveLength(before);
    await page
      .getByRole("button", { name: "Apply filters", exact: true })
      .click();
    await expect(row).toBeVisible();
    expect(queries.at(-1)?.searchParams.get("state")).toBe("blocked");
    expect(queries.at(-1)?.searchParams.get("horizonHours")).toBe("48");
    await expect(
      page.getByText(
        "Filter edits are pending. Apply filters to update the list.",
      ),
    ).toHaveCount(0);
    await page
      .getByRole("combobox", { name: "Evidence condition" })
      .selectOption("expired");
    await page
      .getByRole("button", { name: "Refresh from first page", exact: true })
      .click();
    await expect(row).toBeVisible();
    expect(queries.at(-1)?.searchParams.get("state")).toBe("blocked");
    await expect(
      page.getByRole("combobox", { name: "Evidence condition" }),
    ).toHaveValue("expired");
    await page
      .getByRole("button", { name: "Apply filters", exact: true })
      .click();
    await expect(
      page.getByRole("row").filter({ hasText: created.evidence.id }),
    ).toHaveCount(0);
    await page
      .getByRole("combobox", { name: "Evidence condition" })
      .selectOption("blocked");
    await page
      .getByRole("button", { name: "Apply filters", exact: true })
      .click();
    await expect(row).toBeVisible();
    await page.screenshot({
      path: testInfo.outputPath("validation-maintenance-wide.png"),
    });
    await page.setViewportSize({ width: 320, height: 900 });
    await page.emulateMedia({ colorScheme: "dark" });
    await doubleText(page);
    await expect
      .poll(() =>
        page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth + 1,
        ),
      )
      .toBe(true);
    const region = page.getByRole("region", {
      name: "Validation maintenance revisions",
      exact: true,
    });
    await expect(region).toHaveAttribute("tabindex", "0");
    await region.focus();
    await page.keyboard.press("ArrowRight");
    await expect
      .poll(() => region.evaluate((node) => node.scrollLeft))
      .toBeGreaterThan(0);
    const accessibility = await new AxeBuilder({ page }).analyze();
    expect(accessibility.violations).toEqual([]);
    for (const item of accessibility.incomplete) {
      // At 320px with doubled text Axe cannot resolve this wrapped flex label.
      // Verify its actual text rectangles are unobscured and exceed 4.5:1;
      // every other incomplete rule, target or reason remains a failure.
      expect(item.id).toBe("color-contrast");
      for (const node of item.nodes) {
        expect(node.target).toEqual([".environment-label"]);
        expect(node.failureSummary).toContain(
          "partially overlaps other elements",
        );
        const target = page.locator(".environment-label");
        await expect(target).toHaveCount(1);
        const measured = await paintedContrast(target);
        await testInfo.attach("maintenance-environment-painted-contrast", {
          body: JSON.stringify(measured),
          contentType: "application/json",
        });
      }
    }
    await page.screenshot({
      path: testInfo.outputPath("validation-maintenance-narrow.png"),
    });
    expect(writes).toEqual([]);
    await page.setViewportSize({ width: 1440, height: 900 });
    await row
      .getByRole("link", { name: "Inspect evidence and renewal" })
      .click();
    await expect(
      page.getByRole("heading", { name: "Version revision", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText(
        /successful renewal does not automatically restore or activate publication/,
      ),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Revalidate revision", exact: true })
      .click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible();
    await expect(
      dialog.getByText(/A new validation run can make a pending review stale/),
    ).toBeVisible();
    await expect(
      dialog.getByText(
        /A blocked external check can withdraw entire affected discovery snapshots/,
      ),
    ).toBeVisible();
    expect(writes).toEqual([]);
    const validated = page.waitForResponse(
      (reply) =>
        new URL(reply.url()).pathname.endsWith(
          `/api/v1/version-revisions/${created.revision.id}:validate`,
        ) && reply.request().method() === "POST",
    );
    await confirmCommand(page);
    const response = await validated;
    expect(response.status()).toBe(201);
    const fresh = (await response.json()) as {
      revision: { id: string; contentSetDigest: string };
      evidence: { id: string; state: string };
    };
    expect(fresh.revision.id).toBe(created.revision.id);
    expect(fresh.revision.contentSetDigest).toBe(
      created.revision.contentSetDigest,
    );
    expect(fresh.evidence.id).not.toBe(created.evidence.id);
    expect(fresh.evidence.state).toBe("blocked");
    expect(writes).toEqual([
      `${catalog.basePath}/_catalog/api/v1/version-revisions/${created.revision.id}:validate`,
    ]);
    await page.goto(
      catalog.origin + catalog.basePath + "/validation-maintenance",
    );
    await expect(
      page.getByRole("row").filter({ hasText: fresh.evidence.id }),
    ).toBeVisible();
    await expect(
      page.getByRole("row").filter({ hasText: created.evidence.id }),
    ).toHaveCount(0);
  });
}
