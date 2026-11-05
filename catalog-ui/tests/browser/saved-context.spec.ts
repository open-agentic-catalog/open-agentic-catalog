import { test, expect, login } from "./fixtures";
import { object, text } from "./json";

for (const base of ["root", "subpath"]) {
  test(`${base}: saved searches preserve consent, drafts and history through local details`, async ({
    page,
    catalog,
  }) => {
    const saved = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/personal/items",
      {
        type: "search",
        label: "Reader context",
        search: { text: "", origins: ["local"] },
      },
    );
    const queries: unknown[] = [];
    page.on("request", (request) => {
      if (new URL(request.url()).pathname.endsWith("/api/v1/catalog/search"))
        queries.push(request.postDataJSON());
    });
    const path = catalog.basePath + "/saved-items/" + saved.data.id;
    await login(page, catalog, "local-admin", "/saved-items/" + saved.data.id);
    await page
      .getByRole("button", { name: "Open search editor", exact: true })
      .click();
    await expect(page.locator("h1")).toHaveText("Saved search");
    expect(queries).toEqual([]);
    const consent = page.getByRole("checkbox", {
      name: /configured embedding provider/,
    });
    await consent.check();
    expect(queries).toEqual([]);
    await page.getByRole("button", { name: "Search", exact: true }).click();
    await expect(page.locator("article.resource-card")).not.toHaveCount(0);
    await page.getByRole("button", { name: "Table", exact: true }).click();
    const name = text(
      await page.locator("tbody .title-button").first().textContent(),
    );
    await page
      .getByRole("searchbox", { name: "Search Catalog", exact: true })
      .fill("unsubmitted reader draft");
    const open = async () => {
      await page.getByRole("button", { name, exact: true }).click();
      const preview = page.getByRole("dialog", { name, exact: true });
      await expect(
        preview.getByText("Use this resource", { exact: true }),
      ).toBeVisible();
      await expect(
        preview.locator("summary").filter({ hasText: /^Save resource$/ }),
      ).toHaveCount(1);
      await preview.getByRole("link", { name: /Open full record/ }).click();
      await expect(
        page.getByRole("heading", { level: 1, name, exact: true }),
      ).toBeVisible();
    };
    await open();
    expect(queries).toHaveLength(1);
    await page.getByRole("link", { name: /Back to saved search/ }).click();
    await expect(page).toHaveURL(catalog.origin + path);
    await expect(page.getByRole("table")).toBeVisible();
    await expect(
      page.getByRole("searchbox", { name: "Search Catalog", exact: true }),
    ).toHaveValue("unsubmitted reader draft");
    expect(queries).toHaveLength(2);
    expect(queries[1]).toMatchObject({
      text: "",
      origins: ["local"],
      limit: 24,
    });
    await open();
    await page
      .locator("summary")
      .filter({ hasText: /^Save resource$/ })
      .click();
    await page
      .getByRole("textbox", { name: "Saved label", exact: true })
      .fill("unsaved private label");
    const detailURL = page.url();
    const refused = page.waitForEvent("dialog");
    await page.evaluate(() => history.back());
    await (await refused).dismiss();
    await expect(page).toHaveURL(detailURL);
    await expect(
      page.getByRole("textbox", { name: "Saved label", exact: true }),
    ).toHaveValue("unsaved private label");
    expect(queries).toHaveLength(2);
    const accepted = page.waitForEvent("dialog");
    await page.evaluate(() => history.back());
    await (await accepted).accept();
    await expect(page).toHaveURL(catalog.origin + path);
    await expect(page.getByRole("table")).toBeVisible();
    expect(queries).toHaveLength(3);
    await page.evaluate(() => history.forward());
    await expect(
      page.getByRole("heading", { level: 1, name, exact: true }),
    ).toBeVisible();
    expect(queries).toHaveLength(3);
    await page.evaluate(() => history.back());
    await expect(page.getByRole("table")).toBeVisible();
    expect(queries).toHaveLength(4);
    await consent.uncheck();
    await page.evaluate(() => history.forward());
    await expect(
      page.getByRole("heading", { level: 1, name, exact: true }),
    ).toBeVisible();
    await page.evaluate(() => history.back());
    await expect(page.locator("h1")).toHaveText("Saved search");
    await expect(page.getByRole("table")).toHaveCount(0);
    expect(queries).toHaveLength(4);
    await consent.check();
    expect(queries).toHaveLength(4);
    await page.getByRole("button", { name: "Search", exact: true }).click();
    await expect.poll(() => queries.length).toBe(5);
    expect(queries[4]).toMatchObject({ text: "unsubmitted reader draft" });
    await page.reload();
    await expect(
      page.getByRole("button", { name: "Open search editor", exact: true }),
    ).toBeVisible();
    expect(queries).toHaveLength(5);
    await page
      .getByRole("button", { name: "Open search editor", exact: true })
      .click();
    await expect(consent).not.toBeChecked();
    await expect(
      page.getByRole("searchbox", { name: "Search Catalog", exact: true }),
    ).toHaveValue("");
    expect(queries).toHaveLength(5);
    expect(
      await page.evaluate(() => JSON.stringify(history.state)),
    ).not.toContain("reader draft");
  });

  test(`${base}: local evidence and rating receipts remain bound to the requested context`, async ({
    page,
    catalog,
  }) => {
    const releases = catalog.receipt.items.filter((item) => item.releaseId);
    expect(releases.length).toBeGreaterThan(1);
    const release = releases[0]!.releaseId!;
    const other = await catalog.admin(
      "GET",
      "/api/v1/catalog/resources/" + releases[1]!.releaseId!,
    );
    let substitute = true;
    await page.route(
      `**/_catalog/api/v1/catalog/resources/${release}`,
      async (route) => {
        const response = await route.fetch();
        if (substitute) await route.fulfill({ response, json: other.data });
        else await route.fulfill({ response });
      },
    );
    await login(page, catalog, "local-admin", "/catalog/" + release);
    await expect(
      page.getByRole("button", { name: "Retry record", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Use this resource", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.locator("summary").filter({ hasText: /^Save resource$/ }),
    ).toHaveCount(0);
    substitute = false;
    await page
      .getByRole("button", { name: "Retry record", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: "Use this resource", exact: true }),
    ).toBeVisible();
    let wrongTarget = true;
    await page.route("**/_catalog/api/v1/ratings/read", async (route) => {
      const response = await route.fetch();
      const data = object(await response.json());
      if (wrongTarget)
        await route.fulfill({
          response,
          json: {
            ...data,
            target: {
              ...object(data.target),
              versionRevisionId: releases[1]!.versionRevisionId,
            },
          },
        });
      else await route.fulfill({ response });
    });
    await page
      .getByRole("button", { name: "Ratings for this version", exact: true })
      .click();
    const panel = page.getByRole("region", {
      name: "Ratings for this version",
      exact: true,
    });
    await expect(panel.getByRole("alert")).toBeVisible();
    await expect(panel.getByRole("radio")).toHaveCount(0);
    wrongTarget = false;
    await panel.getByRole("button", { name: "Reload", exact: true }).click();
    await expect(
      panel.getByText("No ratings yet", { exact: true }),
    ).toBeVisible();
    const writes: { body: unknown; key: string | undefined }[] = [];
    await page.route("**/_catalog/api/v1/ratings", async (route) => {
      writes.push({
        body: route.request().postDataJSON(),
        key: route.request().headers()["idempotency-key"],
      });
      const response = await route.fetch();
      const receipt = object(await response.json());
      expect(receipt.revision).toBe(1);
      await route.fulfill({
        response,
        json: writes.length === 1 ? { ...receipt, revision: 2 } : receipt,
      });
    });
    await panel.getByRole("radio", { name: "3 stars", exact: true }).check();
    await panel
      .getByRole("button", { name: "Submit rating", exact: true })
      .click();
    const dialog = page.getByRole("dialog", {
      name: "Submit your rating",
      exact: true,
    });
    await dialog.getByRole("button", { name: "Confirm", exact: true }).click();
    await expect(dialog.getByRole("alert")).toContainText("outcome is unknown");
    await expect(page.getByText("Rating saved.", { exact: true })).toHaveCount(
      0,
    );
    await dialog
      .getByRole("button", { name: "Retry same command", exact: true })
      .click();
    await expect(dialog).not.toBeVisible();
    await expect(
      page.getByText("Rating saved.", { exact: true }),
    ).toBeVisible();
    expect(writes).toHaveLength(2);
    expect(writes[0]!.key).toBeTruthy();
    expect(writes[1]).toEqual(writes[0]);
    await expect(
      panel.getByRole("button", { name: "Update rating", exact: true }),
    ).toBeVisible();
  });
}
