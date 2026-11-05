// Real publication reads; controlled capability availability qualifies presentation, not model execution.
import { test, expect, login } from "./fixtures";
import { doubleText } from "./text-layout";

for (const base of ["root", "subpath"]) {
  test(`${base}: indexing arrival focuses its section and active choices read only one selected host`, async ({
    page,
    catalog,
  }, info) => {
    const hosts = await catalog.admin<{
      items: { host: string; mode: string }[];
    }>("GET", "/api/v1/publication/hosts");
    const host = hosts.data.items.find(
      (item) => item.mode === "anonymous",
    )!.host;
    const snapshots = await catalog.admin<{
      items: { id: string; audience: string }[];
    }>(
      "GET",
      `/api/v1/publication/snapshots?host=${encodeURIComponent(host)}&state=active&limit=1`,
    );
    const snapshot = snapshots.data.items[0]!;
    let reads = 0;
    let writes = 0;
    page.on("request", (request) => {
      if (request.url().includes("/_catalog/api/v1/publication/snapshots")) {
        reads++;
        const query = new URL(request.url()).searchParams;
        expect(query.get("host")).toBe(host);
        expect(query.get("state")).toBe("active");
        expect(query.get("limit")).toBe("1");
        expect(query.has("cursor")).toBe(false);
      }
      if (request.url().includes("/_catalog/") && request.method() !== "GET")
        writes++;
    });
    await page.route("**/_catalog/api/v1/capabilities", async (route) => {
      const reply = await route.fetch();
      const value = (await reply.json()) as { search: object };
      await route.fulfill({
        response: reply,
        json: {
          ...value,
          search: {
            ...value.search,
            semantic: "enabled",
            providerState: "unknown",
          },
        },
      });
    });
    await login(page, catalog, "local-admin", "/service");
    const navigation = page.getByRole("navigation", {
      name: "Workspace",
      exact: true,
    });
    await navigation
      .getByRole("link", { name: "Search indexing", exact: true })
      .press("Enter");
    await expect(
      page.getByRole("region", { name: "Search indexing", exact: true }),
    ).toBeFocused();
    await expect(
      navigation.getByRole("link", { name: "Search indexing", exact: true }),
    ).toHaveAttribute("aria-current", "page");
    await expect(
      navigation.getByRole("link", {
        name: "Service information",
        exact: true,
      }),
    ).not.toHaveAttribute("aria-current");
    await expect(page).toHaveTitle("Search indexing | Open Agentic Catalog");
    await page
      .getByRole("combobox", { name: "Search index", exact: true })
      .selectOption("public");
    await page
      .getByRole("button", { name: "Choose active snapshot", exact: true })
      .click();
    await page
      .getByRole("combobox", { name: "Publication host", exact: true })
      .selectOption(host);
    expect(reads).toBe(0);
    await page
      .getByRole("button", { name: "Read active snapshot", exact: true })
      .click();
    await expect(
      page.getByRole("region", { name: "Active snapshot lookup" }),
    ).toContainText(snapshot.id);
    expect(reads).toBe(1);
    await page
      .getByRole("button", {
        name: "Use observed snapshot for reindexing",
        exact: true,
      })
      .click();
    await expect(
      page.getByLabel("Active snapshot ID", { exact: true }),
    ).toHaveValue(snapshot.id);
    await page
      .getByRole("textbox", { name: "Reason", exact: true })
      .fill("Inspect captured public population without sending");
    await page
      .getByRole("button", { name: "Rebuild search index", exact: true })
      .click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByLabel("Command summary")).toContainText(host);
    await expect(dialog.getByLabel("Command summary")).toContainText(
      snapshot.audience,
    );
    await expect(dialog.getByLabel("Command summary")).toContainText(
      snapshot.id,
    );
    await dialog.screenshot({
      path: info.outputPath("search-indexing-confirmation.png"),
    });
    await page.setViewportSize({ width: 390, height: 844 });
    await doubleText(page);
    await expect(
      dialog.getByRole("button", { name: "Cancel", exact: true }),
    ).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: info.outputPath("search-indexing-narrow.png"),
      fullPage: true,
    });
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(writes).toBe(0);
    expect(reads).toBe(1);
    await page.getByRole("textbox", { name: "Reason", exact: true }).fill("");
    await page
      .getByRole("combobox", { name: "Search index", exact: true })
      .selectOption("control");
    await expect(
      page.getByLabel("Active snapshot ID", { exact: true }),
    ).toHaveCount(0);
  });
}
