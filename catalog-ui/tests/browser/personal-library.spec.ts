// Owned API data proves library lookup and continuation recovery; each injected fault stays explicit.
import AxeBuilder from "@axe-core/playwright";
import { test, expect, login } from "./fixtures";
import { array, object, text } from "./json";
import { personalCommandOutcome } from "../../scripts/personal-evidence.mjs";

for (const base of ["root", "subpath"]) {
  test(`${base}: rejected library continuation recovers applied filters without submitting pending edits`, async ({
    page,
    catalog,
    network,
  }) => {
    const literal = " Cursor label ";
    const ids: string[] = [];
    for (let i = 0; i < 27; i++) {
      const created = await catalog.admin<{ id: string }>(
        "POST",
        "/api/v1/personal/items",
        {
          type: "search",
          label: i < 26 ? literal : "Unrelated saved label",
          search: { text: "Stored criteria, never executed" },
        },
        undefined,
        "local-contributor",
      );
      if (i < 26) ids.push(created.data.id);
    }
    ids.sort();
    const path = catalog.basePath + "/_catalog/api/v1/personal/items";
    const queries: URL[] = [];
    const forbidden: string[] = [];
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (url.pathname === path && request.method() === "GET")
        queries.push(url);
      if (
        url.pathname.endsWith("/api/v1/catalog/search") ||
        url.pathname.endsWith("/api/v1/catalog/explore") ||
        url.pathname.endsWith("/resolve") ||
        (url.pathname.startsWith(path) && request.method() !== "GET")
      )
        forbidden.push(url.pathname);
    });
    await login(page, catalog, "local-contributor", "/saved-items");
    const label = page.getByRole("textbox", {
      name: "Exact saved label",
      exact: true,
    });
    const kind = page.getByRole("combobox", {
      name: "Saved item type",
      exact: true,
    });
    const refresh = page.getByRole("button", {
      name: "Refresh saved items",
      exact: true,
    });
    const next = page.getByRole("button", { name: "Next page", exact: true });
    const rowLinks = page.locator("tbody td:first-child a");
    const visibleLinks = () =>
      rowLinks.evaluateAll((links) =>
        links.map((link) => link.getAttribute("href")),
      );
    await expect(rowLinks).toHaveCount(25);
    await label.fill(literal);
    await kind.selectOption("search");
    expect(queries).toHaveLength(1);
    const applied = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return url.pathname === path && url.searchParams.get("label") === literal;
    });
    await page
      .getByRole("button", { name: "Apply saved item filters", exact: true })
      .click();
    const first = await applied;
    expect(first.status()).toBe(200);
    const firstPage = object(await first.json());
    const firstCursor = text(firstPage.nextCursor);
    expect(firstCursor).not.toBe("");
    expect(array(firstPage.items).map((item) => text(object(item).id))).toEqual(
      ids.slice(0, 25),
    );
    const firstLinks = ids
      .slice(0, 25)
      .map((id) => catalog.basePath + "/saved-items/" + id);
    await expect.poll(visibleLinks).toEqual(firstLinks);
    await expect(next).toBeEnabled();
    const lastLinks = [catalog.basePath + "/saved-items/" + ids[25]!];
    await next.click();
    await expect.poll(visibleLinks).toEqual(lastLinks);
    expect(queries).toHaveLength(3);
    expect(queries[2]!.searchParams.get("cursor")).toBe(firstCursor);
    await label.fill("Pending label must stay private");
    await kind.selectOption("resource");
    expect(queries).toHaveLength(3);

    network.expectFailure("GET", "/_catalog/api/v1/personal/items", 400);
    let refusals = 0;
    await page.route(
      (url) =>
        url.origin === catalog.origin &&
        url.pathname === path &&
        url.searchParams.has("cursor"),
      async (route) => {
        const url = new URL(route.request().url());
        expect(url.searchParams.get("cursor")).toBe(firstCursor);
        url.searchParams.set("cursor", firstCursor + "tampered");
        const response = await route.fetch({ url: url.href });
        expect(response.status()).toBe(400);
        expect(object(await response.json()).code).toBe("INVALID_ARGUMENT");
        refusals++;
        await route.fulfill({ response });
      },
      { times: 1 },
    );
    await refresh.click();
    await expect(page.getByRole("alert")).toBeVisible();
    expect(refusals).toBe(1);
    expect(queries).toHaveLength(4);
    expect(queries[3]!.searchParams.get("label")).toBe(literal);
    expect(queries[3]!.searchParams.get("type")).toBe("search");
    await expect.poll(visibleLinks).toEqual(lastLinks);
    await expect(
      page.getByText("Last loaded saved library page", { exact: true }),
    ).toBeVisible();
    await expect(label).toHaveValue("Pending label must stay private");
    await expect(kind).toHaveValue("resource");

    const recovery = page.waitForResponse(
      (response) => new URL(response.url()).pathname === path,
    );
    await refresh.click();
    const recovered = await recovery;
    expect(recovered.status()).toBe(200);
    const recoveredPage = object(await recovered.json());
    const recoveredCursor = text(recoveredPage.nextCursor);
    expect(recoveredCursor).not.toBe("");
    await expect(next).toBeEnabled();
    expect(queries).toHaveLength(5);
    expect(Object.fromEntries(queries[4]!.searchParams)).toEqual({
      type: "search",
      label: literal,
      limit: "25",
    });
    await expect.poll(visibleLinks).toEqual(firstLinks);
    await expect(page.getByRole("alert")).toHaveCount(0);
    await expect(label).toHaveValue("Pending label must stay private");
    await expect(kind).toHaveValue("resource");

    await next.click();
    await expect.poll(visibleLinks).toEqual(lastLinks);
    expect(queries).toHaveLength(6);
    expect(Object.fromEntries(queries[5]!.searchParams)).toEqual({
      type: "search",
      label: literal,
      limit: "25",
      cursor: recoveredCursor,
    });
    await expect(label).toHaveValue("Pending label must stay private");
    await expect(kind).toHaveValue("resource");
    await page
      .getByRole("button", { name: "Clear saved item filters", exact: true })
      .click();
    await expect(rowLinks).toHaveCount(25);
    expect(queries).toHaveLength(7);
    expect(Object.fromEntries(queries[6]!.searchParams)).toEqual({
      limit: "25",
    });
    await expect(label).toHaveValue("");
    await expect(kind).toHaveValue("");
    expect(new URL(page.url()).search).toBe("");
    expect(forbidden).toEqual([]);
  });

  test(`${base}: literal library lookup and exact-command recovery preserve a private draft`, async ({
    page,
    catalog,
    network,
  }) => {
    const original = { text: "support helper", origins: ["local"] };
    const created: { id: string; etag: string | null; label: string }[] = [];
    for (let i = 0; i < 27; i++) {
      const label = `Private search ${i}`;
      const result = await catalog.admin<{ id: string }>(
        "POST",
        "/api/v1/personal/items",
        { type: "search", label, search: original },
        undefined,
        "local-contributor",
      );
      created.push({ id: result.data.id, etag: result.etag, label });
    }
    created.sort((a, b) => a.id.localeCompare(b.id));
    const targets = created.slice(-2);
    const literal = " Library exact ";
    for (const item of targets) {
      const result = await catalog.admin(
        "PATCH",
        `/api/v1/personal/items/${item.id}`,
        { type: "search", label: literal, search: original },
        item.etag,
        "local-contributor",
      );
      item.etag = result.etag;
    }
    await login(page, catalog, "local-contributor", "/saved-items");
    const forbidden: string[] = [];
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (
        path.endsWith("/api/v1/catalog/search") ||
        path.endsWith("/api/v1/catalog/explore") ||
        path.endsWith("/resolve")
      )
        forbidden.push(path);
    });
    await expect(page.locator("tbody tr")).toHaveCount(25);
    await expect(page.getByRole("link", { name: /Library exact/ })).toHaveCount(
      0,
    );
    const queries: string[] = [];
    page.on("request", (request) => {
      if (
        request.method() === "GET" &&
        new URL(request.url()).pathname.endsWith("/api/v1/personal/items")
      )
        queries.push(request.url());
    });
    await page
      .getByRole("textbox", { name: "Exact saved label", exact: true })
      .fill(literal);
    await page
      .getByRole("combobox", { name: "Saved item type", exact: true })
      .selectOption("search");
    expect(queries).toEqual([]);
    await page
      .getByRole("button", { name: "Apply saved item filters", exact: true })
      .click();
    await expect(page.locator("tbody tr")).toHaveCount(2);
    expect(new URL(queries[0]!).searchParams.get("label")).toBe(literal);
    await expect(
      page.getByRole("textbox", { name: "Exact saved label", exact: true }),
    ).toHaveValue(literal);
    const selected = targets[0]!;
    await page
      .locator(`a[href="${catalog.basePath}/saved-items/${selected.id}"]`)
      .click();
    const label = page.getByRole("textbox", {
      name: "Saved label",
      exact: true,
    });
    await expect(label).toHaveValue(literal);
    await label.fill("Retained private draft");
    const path = `${catalog.basePath}/_catalog/api/v1/personal/items/${selected.id}`;
    network.expectFailure(
      "GET",
      `/_catalog/api/v1/personal/items/${selected.id}`,
      503,
    );
    await page.route(
      catalog.origin + path,
      async (route) => {
        expect(route.request().method()).toBe("GET");
        await route.fulfill({
          status: 503,
          contentType: "application/problem+json",
          body: JSON.stringify({ code: "DEPENDENCY_UNAVAILABLE" }),
        });
      },
      { times: 1 },
    );
    page.once("dialog", async (dialog) => {
      expect(dialog.type()).toBe("confirm");
      await dialog.accept();
    });
    await page
      .getByRole("button", { name: "Read current saved item", exact: true })
      .click();
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(label).toHaveValue("Retained private draft");
    await expect(label).toBeEnabled();
    await expect(
      page.getByRole("button", { name: "Update saved item", exact: true }),
    ).toBeDisabled();
    await expect(
      page.getByText("Last loaded saved item", { exact: true }),
    ).toBeVisible();
    page.once("dialog", async (dialog) => {
      expect(dialog.type()).toBe("confirm");
      await dialog.accept();
    });
    await page
      .getByRole("button", { name: "Read current saved item", exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: "Update saved item", exact: true }),
    ).toBeEnabled();
    await expect(label).toHaveValue("Retained private draft");
    if (base === "subpath") {
      await page.getByLabel("Appearance", { exact: true }).click();
      await page.getByRole("radio", { name: "Dark", exact: true }).check();
      await page.getByLabel("Appearance", { exact: true }).click();
    }
    await page.setViewportSize({ width: 390, height: 844 });
    await page.evaluate(() => {
      document.documentElement.style.fontSize = "200%";
    });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await expect(label).toBeVisible();
    await page.evaluate(() => {
      document.documentElement.style.fontSize = "";
    });
    await page.setViewportSize({ width: 1440, height: 900 });
    const scan = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21aa"])
      .analyze();
    expect(scan.violations).toEqual([]);
    expect(scan.incomplete).toEqual([]);
    const writes: {
      body: unknown;
      key: string | undefined;
      tag: string | undefined;
    }[] = [];
    await page.route(catalog.origin + path, async (route) => {
      if (route.request().method() !== "PATCH") return route.continue();
      writes.push({
        body: route.request().postDataJSON(),
        key: route.request().headers()["idempotency-key"],
        tag: route.request().headers()["if-match"],
      });
      const response = await route.fetch();
      const body: unknown = await response.json();
      expect(
        response.status(),
        JSON.stringify(
          personalCommandOutcome({
            status: response.status(),
            method: "PATCH",
            problem: body,
            requestId: response.headers()["x-request-id"],
          }),
        ),
      ).toBe(200);
      const receipt = body as {
        id: string;
        revision: number;
      };
      if (writes.length === 2)
        expect(response.headers()["idempotency-replayed"]).toBe("true");
      await route.fulfill({
        response,
        json:
          writes.length === 1
            ? { ...receipt, revision: receipt.revision + 1 }
            : receipt,
      });
    });
    await page
      .getByRole("button", { name: "Update saved item", exact: true })
      .click();
    const dialog = page.getByRole("dialog", {
      name: "Update saved item",
      exact: true,
    });
    await expect(dialog.getByLabel("Command summary")).toContainText(
      selected.id,
    );
    await expect(dialog.getByLabel("Command summary")).toContainText(
      "Retained private draft",
    );
    expect(writes).toEqual([]);
    await dialog.getByRole("button", { name: "Confirm", exact: true }).click();
    await expect(dialog.getByRole("alert")).toContainText("outcome is unknown");
    await expect(
      page.getByRole("status").filter({ hasText: "Private item saved." }),
    ).toHaveCount(0);
    await dialog
      .getByRole("button", { name: "Retry same command", exact: true })
      .click();
    await expect(
      page.getByRole("status").filter({ hasText: "Private item saved." }),
    ).toBeVisible();
    expect(writes).toHaveLength(2);
    expect(writes[1]).toEqual(writes[0]);
    expect(writes[0]!.key).toBeTruthy();
    expect(writes[0]!.tag).toBe(selected.etag);
    expect(writes[0]!.body).toEqual({
      type: "search",
      label: "Retained private draft",
      search: original,
    });
    const retained = await catalog.admin<{
      label: string;
      revision: number;
      search: unknown;
    }>(
      "GET",
      `/api/v1/personal/items/${selected.id}`,
      undefined,
      undefined,
      "local-contributor",
    );
    expect(retained.data).toMatchObject({
      label: "Retained private draft",
      revision: 3,
      search: original,
    });
    expect(forbidden).toEqual([]);
  });
}
