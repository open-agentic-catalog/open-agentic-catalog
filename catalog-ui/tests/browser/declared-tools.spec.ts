import { readFile } from "node:fs/promises";
import { test, expect, login, confirmCommand } from "./fixtures";
import { publishFixture } from "./published-fixture";
import { array, object, text } from "./json";
import { openDisclosure } from "./editor-disclosure";
const mediaType = "application/vnd.open-agentic-catalog.mcp-tools+json";
const card = object(
  JSON.parse(
    await readFile(
      new URL("../../../samples/cards/documentation-mcp.json", import.meta.url),
      "utf8",
    ),
  ),
);

for (const base of ["root", "subpath"]) {
  test(`${base}: declared MCP tool discovery preserves saved criteria, exact evidence and narrow layout`, async ({
    page,
    catalog,
  }, testInfo) => {
    const declaration = {
      tools: [
        {
          name: "extract_pdf_tables",
          description: "PDF tables <script>untrusted</script>",
        },
        { name: "read_pdf_tables" },
      ],
    };
    const release = await publishFixture(catalog, 91, "", {
      kind: "mcp_server",
      nativeDocument: card,
      displayName: "Document Utilities",
      description: "Processes documents",
      additionalRepresentations: [
        {
          role: "metadata",
          mediaType,
          profileId: "open-agentic-catalog-mcp-tools-1",
          data: declaration,
        },
      ],
    });
    await login(page, catalog, "local-admin");
    await page
      .getByRole("searchbox", { name: "Search Catalog", exact: true })
      .fill("pdf tables");
    await page.getByRole("button", { name: "Search", exact: true }).click();
    await expect(page.locator("article.resource-card")).toHaveCount(0);
    await page.getByRole("button", { name: /^Filters/ }).click();
    await page
      .getByRole("checkbox", {
        name: "Include declared MCP tools",
        exact: true,
      })
      .check();
    await expect(
      page.getByLabel("Agent protocol binding", { exact: true }),
    ).toBeDisabled();
    await page
      .getByRole("button", { name: "Apply filters", exact: true })
      .click();
    const result = page.locator("article.resource-card");
    await expect(result).toHaveCount(1);
    await expect(result).toContainText("extract_pdf_tables");
    await expect(result).toContainText("<script>untrusted</script>");
    await result
      .getByRole("button", {
        name: "Inspect released declaration",
        exact: true,
      })
      .click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText("Declared MCP tools");
    await dialog
      .getByText("Exact release and revision IDs", { exact: true })
      .click();
    await expect(dialog).toContainText(release);
    await expect(dialog).toContainText("Private author declaration");
    await dialog
      .getByRole("button", { name: "Close preview", exact: true })
      .click();
    await page.getByRole("button", { name: "Table", exact: true }).click();
    await expect(page.locator("tbody")).toContainText("extract_pdf_tables");
    await page.getByText("Save this named search", { exact: true }).click();
    await page
      .getByRole("textbox", { name: "Saved label", exact: true })
      .fill("Declared document tools");
    const savedReply = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        r.url().endsWith("/api/v1/personal/items"),
    );
    await page
      .getByRole("button", { name: "Save named search", exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    const saved = await savedReply;
    expect(saved.status()).toBe(201);
    expect(object(saved.request().postDataJSON()).search).toMatchObject({
      includeMcpTools: true,
      origins: ["local"],
      kinds: ["mcp_server"],
      text: "pdf tables",
    });
    // SDK/API callers may omit kinds: the selected mode still means MCP only.
    const savedItem = object(await saved.json());
    await catalog.admin(
      "PATCH",
      `/api/v1/personal/items/${text(savedItem.id)}`,
      {
        type: "search",
        label: "Declared document tools",
        search: {
          includeMcpTools: true,
          text: "pdf tables",
          origins: ["local"],
        },
      },
      `"rev-${savedItem.revision}"`,
    );
    await page
      .getByRole("link", { name: "Open saved item", exact: true })
      .click();
    await page.reload();
    await page
      .getByRole("button", { name: "Open search editor", exact: true })
      .click();
    await expect(
      page.getByRole("checkbox", { name: /configured embedding provider/ }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "MCP Servers", exact: true }),
    ).toHaveAttribute("aria-pressed", "true");
    await expect(
      page.getByRole("button", { name: "All kinds", exact: true }),
    ).toHaveAttribute("aria-pressed", "false");
    await page.getByRole("button", { name: "Search", exact: true }).click();
    await expect(
      page.getByText("extract_pdf_tables", { exact: true }),
    ).toBeVisible();
    await page.setViewportSize({ width: 375, height: 900 });
    await page.getByRole("button", { name: "Cards", exact: true }).click();
    await expect(page.locator("article.resource-card")).toHaveCount(1);
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    );
    expect(overflow).toBe(false);
    await page.screenshot({
      path: testInfo.outputPath("declared-tools-mobile.png"),
    });
  });
  test(`${base}: author a private MCP declaration with the existing JSON version editor`, async ({
    page,
    catalog,
  }, testInfo) => {
    const resource = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/resources",
      {
        namespaceId: catalog.receipt.namespaceId,
        kind: "mcp_server",
        canonicalIdentifier:
          "urn:air:demo.catalog.example.com:demo:declared-authoring",
      },
    );
    await login(
      page,
      catalog,
      "local-contributor",
      `/versions/new?resource=${resource.data.id}`,
    );
    await page
      .getByRole("textbox", { name: "Version label", exact: true })
      .fill("1.0.0");
    await openDisclosure(page, "Advanced MCP JSON");
    await page
      .getByRole("textbox", { name: "Standards document (JSON)", exact: true })
      .fill(JSON.stringify(card));
    const addRepresentation = page.getByRole("button", {
      name: "Add representation",
      exact: true,
    });
    const addTools = page.getByRole("button", {
      name: "Add declared tools",
      exact: true,
    });
    await addTools.scrollIntoViewIfNeeded();
    const firstBox = await addRepresentation.boundingBox();
    const secondBox = await addTools.boundingBox();
    expect(firstBox).not.toBeNull();
    expect(secondBox).not.toBeNull();
    expect(
      secondBox!.x >= firstBox!.x + firstBox!.width + 8 ||
        secondBox!.y >= firstBox!.y + firstBox!.height + 8,
    ).toBe(true);
    await page
      .getByRole("button", { name: "Add declared tools", exact: true })
      .click();
    await page
      .getByRole("textbox", { name: "Declared tools (JSON)", exact: true })
      .fill('{"tools":[]}');
    await expect(
      page.getByRole("button", { name: "Add declared tools", exact: true }),
    ).toBeDisabled();
    await page.screenshot({
      path: testInfo.outputPath("declared-tools-authoring.png"),
    });
    const response = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        r.url().endsWith(`/api/v1/resources/${resource.data.id}/versions`),
    );
    await page
      .getByRole("button", { name: "Create version", exact: true })
      .click();
    await confirmCommand(page);
    const created = await response;
    expect(created.status()).toBe(201);
    const bundle = object(await created.json());
    const revisionId = text(object(bundle.revision).id);
    const stored = await catalog.admin(
      "GET",
      `/api/v1/version-revisions/${revisionId}`,
    );
    const reps = array(object(stored.data.revision).representations).map(
      object,
    );
    expect(reps).toHaveLength(2);
    expect(reps[0]!.data).toEqual(card);
    expect(reps[1]).toMatchObject({
      role: "metadata",
      mediaType,
      profileId: "open-agentic-catalog-mcp-tools-1",
      data: { tools: [] },
      validationState: "valid",
    });
    expect(object(bundle.evidence).state).toBe("complete");
  });
}
