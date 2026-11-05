// Real local records qualify category filtering and fragment navigation through the current API.
import AxeBuilder from "@axe-core/playwright";
import { test, expect, login } from "./fixtures";
import { publishFixture } from "./published-fixture";

for (const base of ["root", "subpath"]) {
  test(`${base}: record sections focus below the header and categories preserve pending search text`, async ({
    page,
    catalog,
  }) => {
    const release = await publishFixture(catalog, 194, "", {
      displayName: "Order validation Agent",
      description:
        "Validate an order and inspect its declared client interface.",
    });
    const searches: unknown[] = [];
    let records = 0;
    page.on("request", (request) => {
      if (request.url().endsWith("/api/v1/catalog/search"))
        searches.push(request.postDataJSON());
      if (
        new URL(request.url()).pathname.endsWith(
          `/catalog/resources/${release}`,
        )
      )
        records++;
    });
    await login(page, catalog);
    const query = page.getByRole("searchbox", {
      name: "Search Catalog",
      exact: true,
    });
    await query.fill("pending unsent query");
    await page
      .getByRole("button", { name: "Order validation Agent", exact: true })
      .click();
    await page
      .getByRole("link", { name: "Open full record", exact: true })
      .click();
    const sections = page.getByRole("navigation", {
      name: "Record sections",
      exact: true,
    });
    await expect(sections).toBeVisible();
    await expect(page.getByRole("heading", { level: 1 })).toHaveCount(1);
    const before = records;
    for (const name of [
      "Use this resource",
      "Overview",
      "Technical details and evidence",
      "Validation and assessments",
    ]) {
      await sections.getByRole("link", { name, exact: true }).click();
      const heading = page.getByRole("heading", { name, exact: true });
      await expect(heading).toBeFocused();
      await expect
        .poll(async () =>
          heading.evaluate(
            (node) =>
              node.getBoundingClientRect().top >=
              document.querySelector(".topbar")!.getBoundingClientRect().bottom,
          ),
        )
        .toBe(true);
    }
    expect(records).toBe(before);
    await page.goBack();
    await expect(
      page.getByRole("heading", {
        name: "Technical details and evidence",
        exact: true,
      }),
    ).toBeFocused();
    await page.goForward();
    await expect(
      page.getByRole("heading", {
        name: "Validation and assessments",
        exact: true,
      }),
    ).toBeFocused();
    if (base === "subpath") {
      await page.getByLabel("Appearance", { exact: true }).click();
      await page.getByRole("radio", { name: "Dark", exact: true }).check();
      await page.getByLabel("Appearance", { exact: true }).click();
    }
    await page.setViewportSize({ width: 390, height: 844 });
    await page.evaluate(() => {
      document.documentElement.style.fontSize = "200%";
    });
    await sections
      .getByRole("link", { name: "Use this resource", exact: true })
      .click();
    const handoffHeading = page.getByRole("heading", {
      name: "Use this resource",
      exact: true,
    });
    await expect(handoffHeading).toBeFocused();
    await expect
      .poll(async () =>
        handoffHeading.evaluate(
          (node) =>
            node.getBoundingClientRect().top >=
            document.querySelector(".topbar")!.getBoundingClientRect().bottom,
        ),
      )
      .toBe(true);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.evaluate(() => {
      document.documentElement.style.fontSize = "";
    });
    await page.setViewportSize({ width: 1440, height: 900 });
    expect(
      await new AxeBuilder({ page })
        .withTags(["wcag2a", "wcag2aa", "wcag21aa"])
        .analyze(),
    ).toMatchObject({ violations: [], incomplete: [] });
    if (base === "root") {
      await page.setViewportSize({ width: 1440, height: 1100 });
      await page.evaluate(() => window.scrollTo(0, 0));
      await page.mouse.move(500, 20);
      await page.screenshot({
        path: test.info().outputPath("catalog-record.png"),
      });
    }
    await page
      .getByRole("button", { name: "Filter by category testing", exact: true })
      .click();
    await expect(query).toBeFocused();
    await expect(query).toHaveValue("pending unsent query");
    await expect(
      page.getByRole("button", {
        name: "Remove category testing",
        exact: true,
      }),
    ).toBeVisible();
    expect(searches.at(-1)).toEqual({
      text: "",
      limit: 24,
      categories: ["testing"],
    });
    expect(
      await page.evaluate(() => JSON.stringify(history.state)),
    ).not.toContain("pending unsent query");
    await page.goBack();
    await expect(sections).toBeVisible();
    await page
      .getByRole("link", { name: "Return to results", exact: true })
      .click();
    await expect(query).toHaveValue("pending unsent query");
    await expect(page).toHaveURL(
      catalog.origin + catalog.basePath + "/catalog",
    );
    await page.goto(
      catalog.origin + catalog.basePath + `/catalog/${release}#record-evidence`,
    );
    await expect(
      page.getByRole("heading", {
        name: "Technical details and evidence",
        exact: true,
      }),
    ).toBeFocused();
    await page.reload();
    await expect(
      page.getByRole("heading", {
        name: "Technical details and evidence",
        exact: true,
      }),
    ).toBeFocused();
  });
}
