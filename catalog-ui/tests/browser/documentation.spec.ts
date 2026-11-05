// Presentation-only bootstrap/HTML controls. Packaged gates own real manual bytes.
import { test, expect, login } from "./fixtures";

test("Help opens the host-selected manual in a separate tab and preserves the workspace", async ({
  page,
  context,
  catalog,
}) => {
  await page.route(
    catalog.origin + catalog.basePath + "/runtime.json",
    async (route) => {
      const response = await route.fetch();
      const runtime = (await response.json()) as Record<string, unknown>;
      await route.fulfill({
        response,
        json: { ...runtime, documentationUrl: "/docs/" },
      });
    },
  );
  await context.route(catalog.origin + "/docs/", (route) =>
    route.fulfill({
      contentType: "text/html",
      body: "<!doctype html><h1>Manual navigation control</h1>",
    }),
  );
  await login(page, catalog);
  const help = page.getByRole("group", { name: "Help" });
  const link = help.getByRole("link", { name: "Documentation", exact: true });
  await expect(
    page.getByRole("link", { name: "Documentation", exact: true }),
  ).toHaveCount(1);
  await expect(link).toHaveAttribute("href", "/docs/");
  await expect(link).toHaveAccessibleDescription("Opens in a new tab");
  await link.focus();
  await expect(link).toBeFocused();
  const [manual] = await Promise.all([
    context.waitForEvent("page"),
    link.press("Enter"),
  ]);
  await expect(
    manual.getByRole("heading", { name: "Manual navigation control" }),
  ).toBeVisible();
  expect(await manual.evaluate(() => globalThis.opener === null)).toBe(true);
  await manual.close();
  await expect(page.getByLabel("Account", { exact: true })).toBeVisible();
  await expect(
    help.getByRole("link", { name: "API reference", exact: true }),
  ).toBeVisible();
  await help.screenshot({
    path: test.info().outputPath("help-documentation.png"),
  });
});
