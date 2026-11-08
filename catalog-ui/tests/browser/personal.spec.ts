import {
  test,
  expect,
  login,
  confirmCommand,
  confirmPersonalCommand,
} from "./fixtures";
import type { components } from "../../src/api/schema";

for (const base of ["root", "subpath"]) {
  test(`${base}: personal work and saved searches preserve ownership and explicit execution`, async ({
    page,
    catalog,
  }, testInfo) => {
    const draft = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/resources",
      {
        namespaceId: catalog.receipt.namespaceId,
        canonicalIdentifier:
          "urn:air:demo.catalog.example.com:demo:draft-helper",
        kind: "agent",
      },
      undefined,
      "local-contributor",
    );
    await login(page, catalog, "local-contributor", "/my-work");
    const basePath = catalog.basePath;
    const origin = catalog.origin;
    // The login helper selects its base from test title. Account routes survive reload.
    await expect(
      page.getByRole("heading", { name: "My work", exact: true }),
    ).toBeVisible();
    await page.reload();
    await expect(
      page.getByRole("heading", { name: "My work", exact: true }),
    ).toBeVisible();
    await expect(
      page
        .getByRole("row")
        .filter({
          hasText: "urn:air:demo.catalog.example.com:demo:draft-helper",
        })
        .getByRole("link", { name: "Open resource", exact: true }),
    ).toBeVisible();
    await expect(
      page
        .getByRole("row")
        .filter({
          hasText: "urn:air:demo.catalog.example.com:demo:draft-helper",
        })
        .getByRole("link", { name: "Open resource", exact: true }),
    ).toHaveAttribute("href", basePath + "/resources/" + draft.data.id);
    await page.screenshot({
      path: testInfo.outputPath("my-work.png"),
      fullPage: true,
    });
    await page
      .getByRole("combobox", { name: "Work queue", exact: true })
      .selectOption("approved");
    await expect(
      page.getByText("No approved work without a release.", { exact: true }),
    ).toBeVisible();
    const searches: string[] = [];
    const suggestions: string[] = [];
    page.on("request", (r) => {
      if (new URL(r.url()).pathname.endsWith("/api/v1/catalog/search"))
        searches.push(r.postData() ?? "");
      if (new URL(r.url()).pathname.endsWith("/api/v1/catalog/explore"))
        suggestions.push(r.postData() ?? "");
    });
    await page.goto(origin + basePath + "/saved-items/new");
    await page
      .getByRole("textbox", { name: "Saved label", exact: true })
      .fill("Research helpers");
    await page
      .getByRole("textbox", { name: "Saved search text", exact: true })
      .fill("support helper");
    await page.getByText("More saved filters", { exact: true }).click();
    await page
      .getByRole("checkbox", { name: "Specify origins", exact: true })
      .check();
    const savedReply = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        new URL(r.url()).pathname.endsWith("/_catalog/api/v1/personal/items"),
    );
    await page
      .getByRole("button", { name: "Save named search", exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    const response = await savedReply;
    expect(response.status()).toBe(201);
    const saved = (await response.json()) as { id: string; revision: number };
    expect(saved.revision).toBe(1);
    await page
      .getByRole("link", { name: "Open saved item", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: "Research helpers", exact: true }),
    ).toBeVisible();
    await page.reload();
    await expect(
      page.getByRole("textbox", { name: "Saved label", exact: true }),
    ).toHaveValue("Research helpers");
    await page.screenshot({
      path: testInfo.outputPath("saved-search.png"),
      fullPage: true,
    });
    expect(searches).toEqual([]);
    expect(suggestions).toEqual([]);
    await page
      .getByRole("button", { name: "Open search editor", exact: true })
      .click();
    await expect(page.getByRole("searchbox")).toHaveValue("support helper");
    await expect(page.locator("h1")).toHaveCount(1);
    await page.getByRole("button", { name: "Categories", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "Add category", exact: true }),
    ).toBeVisible();
    expect(suggestions).toEqual([]);
    await page
      .getByRole("button", { name: "Close filters", exact: true })
      .click();
    expect(searches).toEqual([]);
    expect(new URL(page.url()).search).toBe("");
    expect(
      await page.evaluate(() => JSON.stringify(history.state)),
    ).not.toContain("support helper");
    await page
      .getByRole("checkbox", { name: /configured embedding provider/ })
      .check();
    expect(suggestions).toEqual([]);
    expect(searches).toEqual([]);
    await page.getByRole("button", { name: "Categories", exact: true }).click();
    await expect(
      page.getByText(
        "Local Catalog matches for the applied search. External results are not counted.",
        { exact: true },
      ),
    ).toBeVisible();
    await expect.poll(() => suggestions.length).toBe(1);
    expect(JSON.parse(suggestions[0]!)).toMatchObject({
      query: { text: "support helper" },
    });
    await page
      .getByRole("button", { name: "Close filters", exact: true })
      .click();
    // Fit the native input's code-unit limit while exceeding the UTF-8 byte budget.
    await page.getByRole("searchbox").fill("🙂".repeat(126));
    await page.getByRole("button", { name: "Search", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("500");
    expect(searches).toEqual([]);
    await page.getByRole("searchbox").fill("support helper");
    await page.getByRole("button", { name: "Search", exact: true }).click();
    await expect.poll(() => searches.length).toBe(1);
    await page.getByText("Save this named search", { exact: true }).click();
    await page.setViewportSize({ width: 320, height: 900 });
    const toolbar = page.locator("details[open]").filter({
      has: page.getByText("Save this named search", { exact: true }),
    });
    const toolbarLabel = toolbar.getByRole("textbox", {
      name: "Saved label",
      exact: true,
    });
    await toolbarLabel.fill("Search toolbar copy");
    const labelBounds = await toolbarLabel.boundingBox();
    expect(labelBounds).not.toBeNull();
    expect(labelBounds!.x).toBeGreaterThanOrEqual(0);
    expect(labelBounds!.x + labelBounds!.width).toBeLessThanOrEqual(320);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(320);
    const toolbarSave = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        new URL(r.url()).pathname.endsWith("/_catalog/api/v1/personal/items"),
    );
    await toolbar
      .getByRole("button", { name: "Save named search", exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    const toolbarResponse = await toolbarSave;
    expect(toolbarResponse.status()).toBe(201);
    expect(JSON.parse(toolbarResponse.request().postData()!)).toMatchObject({
      type: "search",
      label: "Search toolbar copy",
      search: { text: "support helper", origins: ["local"] },
    });
    await expect(
      toolbar.getByText("Private item saved.", { exact: true }),
    ).toBeVisible();
    expect(searches).toHaveLength(1);
    await page.setViewportSize({ width: 1440, height: 900 });
    // A new identity gets its own library. The route never reuses a previous result.
    await page.getByLabel("Account", { exact: true }).click();
    await page.getByRole("button", { name: "Sign out", exact: true }).click();
    await page.locator('[data-login-alias="local-admin"]').click();
    await page.goto(origin + basePath + "/saved-items");
    await expect(
      page.getByText(
        "Your library is empty. Save a resource from Catalog or create a named search.",
        { exact: true },
      ),
    ).toBeVisible();
    await page.getByLabel("Account", { exact: true }).click();
    await page.getByRole("button", { name: "Sign out", exact: true }).click();
    await page.locator('[data-login-alias="local-contributor"]').click();
    await page.goto(origin + basePath + "/saved-items/" + saved.id);
    await expect(
      page.getByRole("heading", { name: "Research helpers", exact: true }),
    ).toBeVisible();
  });

  test(`${base}: native saved-search filter edits persist and clear without discovery`, async ({
    page,
    catalog,
  }) => {
    const label = "Private advanced criteria";
    const text = "private native filters Ω";
    const criteria: components["schemas"]["PersonalCriteria"] = {
      text,
      order: "publisher_desc",
      origins: ["local"],
      namespaceId: catalog.receipt.namespaceId,
      publisher: catalog.receipt.publisherId,
      kinds: ["agent", "plugin"],
      tags: ["Ops, support", "Escalation"],
      categories: ["Support, tools", "Operations"],
      license: "Apache-2.0",
      visibility: "tenant",
    };
    await login(page, catalog, "local-contributor", "/saved-items/new");
    const discovery: string[] = [];
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (
        path.endsWith("/api/v1/catalog/search") ||
        path.endsWith("/api/v1/catalog/explore")
      )
        discovery.push(request.method() + " " + path);
    });
    const editor = page.getByRole("group", {
      name: "Private saved criteria",
      exact: true,
    });
    const kinds = editor.getByRole("group", {
      name: "Resource kinds",
      exact: true,
    });
    const field = (name: string) =>
      editor.getByRole("textbox", { name, exact: true });
    const choice = (name: string) =>
      editor.getByRole("combobox", { name, exact: true });
    const checkbox = (name: string) =>
      editor.getByRole("checkbox", { name, exact: true });
    let expectedPath = catalog.basePath + "/saved-items/new";
    const assertPrivate = async () => {
      expect(discovery).toEqual([]);
      const url = new URL(page.url());
      expect(url.pathname).toBe(expectedPath);
      expect(url.search).toBe("");
      expect(url.hash).toBe("");
      const retainedHistory = await page.evaluate(() =>
        JSON.stringify(history.state),
      );
      for (const privateValue of [
        label,
        text,
        ...criteria.kinds!,
        ...criteria.origins!,
        ...criteria.tags!,
        ...criteria.categories!,
        catalog.receipt.namespaceId,
        catalog.receipt.publisherId,
        "Apache-2.0",
        "publisher_desc",
        criteria.visibility!,
      ])
        // Values also remain detectable when criteria are stored as a JSON string.
        expect(retainedHistory).not.toContain(privateValue);
    };
    await page
      .getByRole("textbox", { name: "Saved label", exact: true })
      .fill(label);
    await field("Saved search text").fill(text);
    await editor.getByText("More saved filters", { exact: true }).click();
    await choice("Saved sort order").selectOption("publisher_desc");
    await kinds.getByRole("checkbox", { name: "Agents", exact: true }).check();
    await kinds.getByRole("checkbox", { name: "Plugins", exact: true }).check();
    for (const [name, values] of [
      ["tag", ["Ops, support", "Escalation"]],
      ["category", ["Support, tools", "Operations"]],
    ] as const) {
      for (const [index, value] of values.entries()) {
        await editor
          .getByRole("button", { name: `Add ${name}`, exact: true })
          .click();
        await field(`${name === "tag" ? "Tag" : "Category"} ${index + 1}`).fill(
          value,
        );
      }
    }
    await checkbox("Specify origins").check();
    // Staging external origins must not execute discovery. The final criteria
    // select Local Catalog alone so they can also bind an exact Namespace.
    await checkbox("Indexed external").check();
    await checkbox("Live external").check();
    await checkbox("Indexed external").uncheck();
    await checkbox("Live external").uncheck();
    for (const [name, value] of [
      ["Saved Namespace ID", catalog.receipt.namespaceId],
      ["Saved Publisher", catalog.receipt.publisherId],
      ["Saved license declaration", "Apache-2.0"],
    ] as const)
      await field(name).fill(value);
    await choice("Saved visibility").selectOption("tenant");
    await assertPrivate();
    const createdReply = page.waitForResponse(
      (reply) =>
        reply.request().method() === "POST" &&
        new URL(reply.url()).pathname.endsWith(
          "/_catalog/api/v1/personal/items",
        ),
    );
    await page
      .getByRole("button", { name: "Save named search", exact: true })
      .click();
    await confirmCommand(page);
    const created = await createdReply;
    expect(created.status()).toBe(201);
    expect(created.request().postDataJSON()).toEqual({
      type: "search",
      label,
      search: criteria,
    });
    const receipt =
      (await created.json()) as components["schemas"]["PersonalReceipt"];
    expect(receipt.revision).toBe(1);
    const path = `/api/v1/personal/items/${receipt.id}`;
    const read = () =>
      catalog.admin<components["schemas"]["PersonalItem"]>(
        "GET",
        path,
        undefined,
        undefined,
        "local-contributor",
      );
    expect((await read()).data.search).toEqual(criteria);
    await page
      .getByRole("link", { name: "Open saved item", exact: true })
      .click();
    expectedPath = catalog.basePath + "/saved-items/" + receipt.id;
    await page.reload();
    await expect(
      page.getByRole("textbox", { name: "Saved label", exact: true }),
    ).toHaveValue(label);
    await editor.getByText("More saved filters", { exact: true }).click();
    await expect(choice("Saved sort order")).toHaveValue("publisher_desc");
    await expect(
      kinds.getByRole("checkbox", { name: "Agents", exact: true }),
    ).toBeChecked();
    await expect(
      kinds.getByRole("checkbox", { name: "Plugins", exact: true }),
    ).toBeChecked();
    await expect(field("Tag 1")).toHaveValue("Ops, support");
    await expect(field("Category 1")).toHaveValue("Support, tools");
    await expect(field("Saved Namespace ID")).toHaveValue(
      catalog.receipt.namespaceId,
    );
    await expect(field("Saved Publisher")).toHaveValue(
      catalog.receipt.publisherId,
    );
    await expect(field("Saved license declaration")).toHaveValue("Apache-2.0");
    await expect(choice("Saved visibility")).toHaveValue("tenant");
    await assertPrivate();
    await choice("Saved sort order").selectOption("");
    await kinds
      .getByRole("checkbox", { name: "Agents", exact: true })
      .uncheck();
    await kinds
      .getByRole("checkbox", { name: "Plugins", exact: true })
      .uncheck();
    for (const name of ["tag", "category"])
      for (let index = 0; index < 2; index++)
        await editor
          .getByRole("button", { name: `Remove ${name} 1`, exact: true })
          .click();
    await checkbox("Specify origins").uncheck();
    for (const name of [
      "Saved Namespace ID",
      "Saved Publisher",
      "Saved license declaration",
    ])
      await field(name).fill("");
    await choice("Saved visibility").selectOption("");
    await assertPrivate();
    const updatedReply = page.waitForResponse(
      (reply) =>
        reply.request().method() === "PATCH" &&
        new URL(reply.url()).pathname.endsWith("/_catalog" + path),
    );
    await page
      .getByRole("button", { name: "Update saved item", exact: true })
      .click();
    const updated = await confirmPersonalCommand(page, updatedReply, 200);
    expect(updated.status()).toBe(200);
    expect(updated.request().headers()["if-match"]).toBe('"rev-1"');
    expect(updated.request().headers()["idempotency-key"]).toBeTruthy();
    expect(updated.request().postDataJSON()).toEqual({
      type: "search",
      label,
      search: { text: criteria.text, kinds: [], tags: [], categories: [] },
    });
    expect(await updated.json()).toMatchObject({ id: receipt.id, revision: 2 });
    const cleared = (await read()).data.search!;
    expect(cleared.text).toBe(criteria.text);
    for (const key of [
      "order",
      "origins",
      "namespaceId",
      "publisher",
      "license",
      "visibility",
    ])
      expect(cleared).not.toHaveProperty(key);
    expect(cleared.kinds ?? []).toEqual([]);
    expect(cleared.tags ?? []).toEqual([]);
    expect(cleared.categories ?? []).toEqual([]);
    await page.reload();
    await editor.getByText("More saved filters", { exact: true }).click();
    await expect(choice("Saved sort order")).toHaveValue("");
    await expect(checkbox("Specify origins")).not.toBeChecked();
    await expect(
      kinds.getByRole("checkbox", { name: "Agents", exact: true }),
    ).not.toBeChecked();
    await expect(
      kinds.getByRole("checkbox", { name: "Plugins", exact: true }),
    ).not.toBeChecked();
    await expect(field("Tag 1")).toHaveCount(0);
    await expect(field("Category 1")).toHaveCount(0);
    await assertPrivate();
  });
}
