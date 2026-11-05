// Real root/subpath UI sessions; intercepted Job data tests presentation and receipt binding, not SQL admission.
import { test, expect, login } from "./fixtures";
import { required } from "../required";
import { doubleText } from "./text-layout";
import { expectAlignedOperationControls } from "./operations-layout";

for (const path of ["root", "subpath"]) {
  test(`${path}: Job filters, exact navigation and retry recovery preserve their bindings`, async ({
    page,
    catalog,
    network,
  }) => {
    const ancestor = "00000000-0000-7000-8000-000000003000";
    const parent = "00000000-0000-7000-8000-000000003001";
    const child = "00000000-0000-7000-8000-000000003002";
    const stamp = "2026-10-20T00:00:00Z";
    const job = {
      id: parent,
      tenantId: "00000000-0000-7000-8000-000000000001",
      kind: "semantic.reindex",
      state: "failed",
      progress: 100,
      priority: 0,
      attempt: 4,
      attemptLimit: 3,
      notBefore: stamp,
      createdAt: stamp,
      updatedAt: stamp,
      cancellationRequested: false,
      parentJobId: ancestor,
    };
    const reads: URL[] = [];
    let refuseNext = false;
    await page.route("**/_catalog/api/v1/jobs?**", async (route) => {
      const url = new URL(route.request().url());
      reads.push(url);
      expect(url.searchParams.get("limit")).toBe("25");
      if (refuseNext && url.searchParams.has("cursor")) {
        await route.fulfill({
          status: 400,
          contentType: "application/problem+json",
          json: {
            type: "about:blank",
            title: "Invalid argument",
            status: 400,
            detail: "Job continuation changed.",
            code: "INVALID_ARGUMENT",
          },
        });
        return;
      }
      await route.fulfill({
        json: {
          items: url.searchParams.has("cursor") ? [] : [job],
          ...(url.searchParams.has("cursor")
            ? {}
            : { nextCursor: "opaque-next" }),
        },
      });
    });
    await page.route(`**/_catalog/api/v1/jobs/${parent}`, async (route) => {
      await route.fulfill({ json: job });
    });
    let childReads = 0;
    await page.route(`**/_catalog/api/v1/jobs/${child}`, async (route) => {
      childReads++;
      await route.fulfill({
        json: {
          ...job,
          id: child,
          state: childReads === 1 ? "queued" : "running",
          progress: childReads === 1 ? 0 : 25,
          attempt: childReads === 1 ? 0 : 1,
          parentJobId: parent,
        },
      });
    });
    const commands: { key: string | undefined; body: unknown }[] = [];
    await page.route(
      `**/_catalog/api/v1/jobs/${parent}:retry`,
      async (route) => {
        expect(route.request().headers()["if-match"]).toBeUndefined();
        commands.push({
          key: route.request().headers()["idempotency-key"],
          body: route.request().postDataJSON() as unknown,
        });
        await route.fulfill({
          status: 202,
          json: {
            ...job,
            id: child,
            state: "queued",
            parentJobId: commands.length === 1 ? child : parent,
          },
        });
      },
    );
    await login(page, catalog, "local-admin", "/jobs");
    await expect(page.getByRole("table")).toContainText(parent);
    await page.setViewportSize({ width: 1440, height: 1000 });
    await expectAlignedOperationControls([
      page.getByRole("combobox", { name: "Job type", exact: true }),
      page.getByRole("combobox", { name: "Job state", exact: true }),
      page.getByLabel("Retries of job ID", { exact: true }),
    ]);
    await expectAlignedOperationControls([
      page.getByLabel("Exact Job ID", { exact: true }),
      page.getByRole("button", { name: "Open Job", exact: true }),
    ]);
    await expect(
      page.getByRole("option", {
        name: "Search indexing (semantic.reindex)",
        exact: true,
      }),
    ).toHaveCount(1);
    await page
      .getByRole("combobox", { name: "Job type", exact: true })
      .selectOption("custom");
    await page
      .getByLabel("Exact job type code", { exact: true })
      .fill("future.kind");
    expect(reads).toHaveLength(1);
    await page
      .getByRole("combobox", { name: "Job type", exact: true })
      .selectOption("semantic.reindex");
    await page
      .getByRole("combobox", { name: "Job state", exact: true })
      .selectOption("failed");
    await page.getByLabel("Retries of job ID", { exact: true }).fill(ancestor);
    expect(reads).toHaveLength(1);
    await page
      .getByRole("button", { name: "Apply Job filters", exact: true })
      .click();
    await expect(page).toHaveURL(
      new RegExp(
        `jobs\\?kind=semantic.reindex&state=failed&parentJobId=${ancestor}$`,
      ),
    );
    await expect.poll(() => reads.length).toBe(2);
    expect(Object.fromEntries(required(reads.at(-1)).searchParams)).toEqual({
      limit: "25",
      kind: "semantic.reindex",
      state: "failed",
      parentJobId: ancestor,
    });
    await page.screenshot({
      path: test.info().outputPath("jobs-filtered.png"),
      fullPage: true,
    });
    await page.getByRole("button", { name: "Next page", exact: true }).click();
    await expect(
      page.getByText("No jobs match these filters.", { exact: true }),
    ).toBeVisible();
    expect(required(reads.at(-1)).searchParams.get("cursor")).toBe(
      "opaque-next",
    );
    await page.getByRole("button", { name: "First page", exact: true }).click();
    await expect(page.getByRole("table")).toContainText(parent);
    // Recovery must retain applied filters without submitting a new draft.
    await page
      .getByRole("combobox", { name: "Job type", exact: true })
      .selectOption("publication.generate");
    refuseNext = true;
    network.expectFailure("GET", "/_catalog/api/v1/jobs", 400);
    await page.getByRole("button", { name: "Next page", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText(
      "The request is no longer valid. Start a new search or check your input.",
    );
    await expect(page.getByRole("alert")).not.toContainText(
      "Job continuation changed.",
    );
    expect(Object.fromEntries(required(reads.at(-1)).searchParams)).toEqual({
      limit: "25",
      kind: "semantic.reindex",
      state: "failed",
      parentJobId: ancestor,
      cursor: "opaque-next",
    });
    await page.getByRole("button", { name: "Reload", exact: true }).click();
    await expect(page.getByRole("table")).toContainText(parent);
    expect(Object.fromEntries(required(reads.at(-1)).searchParams)).toEqual({
      limit: "25",
      kind: "semantic.reindex",
      state: "failed",
      parentJobId: ancestor,
    });
    await expect(
      page.getByRole("combobox", { name: "Job type", exact: true }),
    ).toHaveValue("publication.generate");
    expect(new URL(page.url()).searchParams.get("kind")).toBe(
      "semantic.reindex",
    );
    await page
      .getByRole("button", { name: "Clear Job filters", exact: true })
      .click();
    await expect(page.getByRole("table")).toContainText(parent);
    expect(required(reads.at(-1)).search).toBe("?limit=25");
    await page.goBack();
    await expect(
      page.getByRole("combobox", { name: "Job state", exact: true }),
    ).toHaveValue("failed");
    await expect(page.getByRole("table")).toContainText(parent);
    expect(required(reads.at(-1)).searchParams.has("cursor")).toBe(false);
    await page
      .getByLabel("Exact Job ID", { exact: true })
      .fill(parent.toUpperCase());
    await page.getByRole("button", { name: "Open Job", exact: true }).click();
    await expect(page).toHaveURL(
      (url) =>
        url.origin === catalog.origin &&
        url.pathname === `${catalog.basePath}/jobs/${parent}` &&
        !url.search,
    );
    await expect(
      page.getByRole("progressbar", {
        name: "Reported Job progress",
        exact: true,
      }),
    ).toHaveAttribute("value", "100");
    await expect(
      page.getByText(/Check the job state to confirm completion/),
    ).toBeVisible();
    await expect(page.getByText("4 / 3", { exact: true })).toBeVisible();
    await expect(
      page.locator('[data-state="failed"][data-tone="danger"]'),
    ).toHaveText("Failed");
    await expect(
      page.getByRole("button", { name: "Retry job", exact: true }),
    ).toHaveClass("warning");
    await expect(
      page
        .getByRole("button", { name: "Retry job", exact: true })
        .locator('svg[aria-hidden="true"]'),
    ).toHaveCount(1);
    await page.setViewportSize({ width: 320, height: 900 });
    await doubleText(page);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page
      .getByRole("textbox", { name: "Reason", exact: true })
      .fill("Recover this exact Job");
    await page.getByRole("button", { name: "Retry job", exact: true }).click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    await expect(page.getByRole("dialog").getByRole("alert")).toContainText(
      "outcome is unknown",
    );
    await expect(
      page.getByRole("link", { name: "Open retry job", exact: true }),
    ).toHaveCount(0);
    await page
      .getByRole("button", { name: "Retry same command", exact: true })
      .click();
    await expect(
      page.getByRole("link", { name: "Open retry job", exact: true }),
    ).toHaveAttribute("href", `${catalog.basePath}/jobs/${child}`);
    expect(commands).toHaveLength(2);
    expect(commands[1]).toEqual(commands[0]);
    expect(commands[0]?.key).toMatch(/^[a-f0-9-]{36}$/);
    const receipt = page.getByRole("region", {
      name: "Acknowledged administration change",
      exact: true,
    });
    await expect(receipt).toContainText("job.retried");
    await expect(receipt).toContainText(parent);
    await expect(
      receipt.getByRole("link", { name: "Investigate change", exact: true }),
    ).toBeVisible();
    expect(childReads).toBe(0); // Showing the receipt must not prefetch its child.
    await page
      .getByRole("link", { name: "Open retry job", exact: true })
      .click();
    await expect(page).toHaveURL(
      `${catalog.origin}${catalog.basePath}/jobs/${child}`,
    );
    await expect(
      page.getByRole("heading", { name: "Search indexing", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("link", { name: parent, exact: true }),
    ).toHaveAttribute("href", `${catalog.basePath}/jobs/${parent}`);
    await expect(page.getByText("Queued", { exact: true })).toBeVisible();
    expect(childReads).toBe(1);
    await page.getByRole("button", { name: "Reload job", exact: true }).click();
    await expect.poll(() => childReads).toBe(2);
    await expect(page.getByText("Running", { exact: true })).toBeVisible();
    await expect(
      page.getByRole("progressbar", {
        name: "Reported Job progress",
        exact: true,
      }),
    ).toHaveAttribute("value", "25");
    if (path === "root") {
      await page.setViewportSize({ width: 1440, height: 1150 });
      await page.reload();
      await expect(page.getByText("Running", { exact: true })).toBeVisible();
      const detail = page.locator("section.workspace-panel").filter({
        has: page.getByRole("heading", {
          name: "Search indexing",
          exact: true,
        }),
      });
      const box = await detail.boundingBox();
      const parentBox = await page
        .getByRole("link", { name: parent, exact: true })
        .boundingBox();
      if (!box || !parentBox)
        throw new Error("The Job references are not visible");
      await page.screenshot({
        path: test.info().outputPath("job-detail-recovery.png"),
        clip: {
          x: box.x,
          y: box.y,
          width: box.width,
          height: parentBox.y + parentBox.height - box.y + 12,
        },
      });
    }
    expect(commands).toHaveLength(2);
  });
}
