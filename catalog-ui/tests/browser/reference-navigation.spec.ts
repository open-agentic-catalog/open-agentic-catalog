import { test, expect, login } from "./fixtures";

for (const deployment of ["root", "subpath"]) {
  test(`connected reference schema history and keyboard return at ${deployment}`, async ({
    page,
    catalog,
  }) => {
    await login(page, catalog, "local-reader", "/api-reference");
    const query = page.getByRole("searchbox", {
      name: "Search API operations",
      includeHidden: true,
    });
    await query.fill("suspendMembership");
    const operation = page.locator("details.api-operation");
    await expect(operation).toHaveCount(1);
    await operation.locator(":scope > summary").click();
    const sourceLink = operation.getByRole("link", {
      name: "Link to operation",
    });
    const operationHash = await sourceLink.getAttribute("href");
    const view = operation.getByRole("button", {
      name: "View components/requestBodies/Reason",
      exact: true,
    });
    await view.click();
    const definitions = page.getByRole("region", {
      name: "Selected definition",
    });
    const firstHash = new URL(page.url()).hash;
    const historyLength = await page.evaluate(() => history.length);
    await definitions.getByRole("link", { name: "Link to definition" }).click();
    expect(await page.evaluate(() => history.length)).toBe(historyLength);
    await expect(definitions).toBeFocused();
    const nested = definitions
      .getByRole("button", { name: /^View components\// })
      .first();
    await nested.click();
    await expect(page).not.toHaveURL(new RegExp(`${firstHash}$`));
    await page.goBack();
    expect(new URL(page.url()).hash).toBe(firstHash);
    await expect(
      definitions.getByRole("heading", {
        name: "#/components/requestBodies/Reason",
        exact: true,
      }),
    ).toBeVisible();
    await page.goBack();
    expect(new URL(page.url()).hash).toBe(operationHash);
    await expect(operation.locator(":scope > summary")).toBeFocused();
    await page.goForward();
    expect(new URL(page.url()).hash).toBe(firstHash);
    const back = page.getByRole("button", {
      name: "Return to originating operation",
    });
    await back.focus();
    await page.keyboard.press("Enter");
    await expect(operation.locator(":scope > summary")).toBeFocused();
    await expect(operation).toHaveJSProperty("open", true);
    await expect(query).toHaveValue("suspendMembership");
    expect(page.url()).not.toContain("suspendMembership");
    // A direct known operation anchor and a schema anchor both survive a full reload.
    await page.reload();
    await expect(
      page.locator("details.api-operation[open] > summary"),
    ).toHaveCount(1);
    await page.goto(
      `${catalog.origin}${catalog.basePath}/api-reference${firstHash}`,
    );
    await expect(
      definitions.getByRole("heading", {
        name: "#/components/requestBodies/Reason",
        exact: true,
      }),
    ).toBeVisible();
    await page.reload();
    await expect(
      definitions.getByRole("heading", {
        name: "#/components/requestBodies/Reason",
        exact: true,
      }),
    ).toBeVisible();
    // The same schema can belong to different history entries and operations.
    await page
      .getByRole("button", { name: "Browse endpoints", exact: true })
      .click();
    await query.fill("suspendMembership");
    await operation.locator(":scope > summary").click();
    await view.click();
    await page
      .getByRole("button", { name: "Browse endpoints", exact: true })
      .click();
    await query.fill("reactivateMembership");
    const second = page.locator(
      'details[data-operation-id="reactivateMembership"]',
    );
    await second.locator(":scope > summary").click();
    await second
      .getByRole("button", {
        name: "View components/requestBodies/Reason",
        exact: true,
      })
      .click();
    expect(new URL(page.url()).hash).toBe(firstHash);
    await page.goBack();
    await page.goBack();
    await page.goBack();
    expect(new URL(page.url()).hash).toBe(firstHash);
    await expect(query).toHaveValue("reactivateMembership");
    await expect(back).toBeDisabled();
    await page
      .getByRole("button", {
        name: "Reveal originating operation",
        exact: true,
      })
      .focus();
    await page.keyboard.press("Enter");
    await expect(query).toHaveValue("");
    await expect(
      page.locator('details[data-operation-id="suspendMembership"] > summary'),
    ).toBeFocused();
    expect(new URL(page.url()).hash).toBe(operationHash);
  });
}
