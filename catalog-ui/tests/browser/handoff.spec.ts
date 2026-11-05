import { required } from "../required";
// Reader handoff uses real authenticated bytes and clipboard actions, not intercepted successful responses.
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import AxeBuilder from "@axe-core/playwright";
import { test, expect, login, signOut, openAccount } from "./fixtures";
import { array, object, text } from "./json";

for (const kind of ["agent", "mcp_server"]) {
  test(`${kind}: decoded representation disclosure uses loaded metadata in preview and full detail`, async ({
    page,
    catalog,
  }) => {
    const inventory = await catalog.admin(
      "GET",
      "/api/v1/catalog/resources?limit=25",
    );
    const item = required(
      array(object(inventory.data).items)
        .map(object)
        .find((entry) => object(entry.resource).kind === kind),
    );
    const name = text(object(item.listingRevision).displayName);
    let records = 0;
    let documents = 0;
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (/\/catalog\/resources\//.test(path)) records++;
      if (path.includes("/artifacts/")) documents++;
    });
    await login(page, catalog);
    await page.getByRole("button", { name, exact: true }).click();
    const summary = page
      .getByRole("dialog")
      .getByText("View decoded metadata", { exact: true })
      .first();
    await summary.focus();
    await page.keyboard.press("Enter");
    const decoded = summary.locator("..");
    await expect(decoded).toHaveJSProperty("open", true);
    await expect(decoded.getByText(/not original bytes/)).toBeVisible();
    await expect(
      decoded.getByRole("group", { name: "JSON document", exact: true }),
    ).toContainText("{");
    expect(records).toBe(1);
    expect(documents).toBe(0);
    await expect(
      page.getByRole("button", { name: "Copy document text" }),
    ).toHaveCount(0);
    await page
      .getByRole("link", { name: "Open full record", exact: true })
      .click();
    const full = page
      .getByText("View decoded metadata", { exact: true })
      .first();
    await full.click();
    await expect(
      full
        .locator("..")
        .getByRole("group", { name: "JSON document", exact: true }),
    ).toContainText("{");
    expect(records).toBe(2);
    expect(documents).toBe(0);
    await expect(
      page.getByRole("button", { name: "Copy document text" }),
    ).toHaveCount(0);
  });
}

test("preview handoff preserves the submitted search, filters, layout and focus without refetching", async ({
  page,
  context,
  catalog,
}) => {
  const skill = catalog.receipt.items.find(
    (item) => item.key === "order-skill",
  )!;
  await context.grantPermissions(["clipboard-read", "clipboard-write"], {
    origin: catalog.origin,
  });
  const searches: string[] = [];
  const records: string[] = [];
  const artifacts: string[] = [];
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (path.endsWith("/catalog/search")) searches.push(path);
    if (/\/catalog\/resources\//.test(path)) records.push(path);
    if (path.includes("/artifacts/")) artifacts.push(path);
  });
  await login(page, catalog);
  await openAccount(page);
  await page.getByRole("link", { name: "My access", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "My access", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("definition").filter({ hasText: "local-reader" }),
  ).toBeVisible();
  const permissions = page
    .locator('section[aria-labelledby="identity-heading"]')
    .getByRole("definition")
    .filter({ has: page.locator("summary") });
  await permissions.locator("summary").click();
  await expect(
    permissions.getByText("resource.read", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("navigation", { name: "Workspace", exact: true })
    .getByRole("link", { name: "Catalog", exact: true })
    .click();
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  await page
    .getByRole("checkbox", { name: "Local Catalog", exact: true })
    .check();
  await page
    .getByRole("checkbox", { name: "Indexed external", exact: true })
    .uncheck();
  await page
    .getByRole("checkbox", { name: "Live external", exact: true })
    .uncheck();
  await page
    .getByRole("button", { name: "Apply filters", exact: true })
    .click();
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  const search = page.getByRole("searchbox", {
    name: "Search Catalog",
    exact: true,
  });
  await search.fill("Triage order exception");
  await search.press("Enter");
  await expect(page.locator("article.resource-card")).toHaveCount(1);
  await page.getByRole("button", { name: "Skills", exact: true }).click();
  await expect(page.locator("article.resource-card")).toHaveCount(1);
  await page.getByRole("button", { name: "Table", exact: true }).click();
  const opener = page.getByRole("button", {
    name: "Triage order exception Skill",
    exact: true,
  });
  const searchCount = searches.length;
  expect(records).toEqual([]);
  await opener.click();
  const dialog = page.getByRole("dialog");
  await expect(
    dialog.getByRole("region", { name: "Resource handoff" }),
  ).toBeVisible();

  await expect(dialog.getByRole("heading", { level: 3 })).toHaveText([
    "Overview",
    "Listing categories",
    "Validation and assessments",
    "Use this resource",
    "Technical details and evidence",
  ]);
  const evidence = dialog.locator("details").filter({
    has: page.locator("summary", { hasText: "Representation evidence" }),
  });
  await expect(evidence).not.toHaveAttribute("open");
  const disclosure = evidence.locator("summary");
  await disclosure.focus();
  await disclosure.press("Enter");
  await expect(evidence).toHaveAttribute("open");
  await expect(
    evidence.getByText("Digest scope", { exact: true }),
  ).toBeVisible();
  await disclosure.press("Enter");
  await expect(evidence).not.toHaveAttribute("open");
  await dialog
    .getByText("Exact release and revision IDs", { exact: true })
    .click();
  await expect(
    dialog.getByRole("button", { name: "Copy Release ID", exact: true }),
  ).toBeVisible();
  for (const [label, expected] of [
    ["Release ID", skill.releaseId!],
    ["Resource version", "1.0.0"],
  ]) {
    await dialog
      .getByRole("button", { name: `Copy ${label}`, exact: true })
      .click();
    await expect
      .poll(() => page.evaluate(() => navigator.clipboard.readText()))
      .toBe(expected);
  }
  await dialog
    .getByText("Exact release and revision IDs", { exact: true })
    .click();
  expect(records).toHaveLength(1);
  expect(artifacts).toEqual([]);
  await dialog
    .getByRole("button", { name: "Copy Resource identifier", exact: true })
    .click();
  await expect(
    dialog
      .locator(".handoff-reference")
      .filter({
        has: page.getByRole("button", {
          name: "Copy Resource identifier",
          exact: true,
        }),
      })
      .getByRole("status"),
  ).toHaveText("Copied.");
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
    skill.identifier,
  );
  const response = page.waitForResponse(
    (reply) =>
      reply.url().includes("/artifacts/") && reply.request().method() === "GET",
  );
  await dialog
    .getByRole("button", {
      name: "Prepare retained document download",
      exact: true,
    })
    .click();
  const exact = await (await response).body();
  const download = page.waitForEvent("download");
  await dialog
    .getByRole("link", { name: "Save retained document", exact: true })
    .click();
  expect((await readFile((await (await download).path())!)).equals(exact)).toBe(
    true,
  );
  const bounds = await dialog.boundingBox();
  expect(bounds).not.toBeNull();
  // A press inside followed by release outside must preserve selection/preview.
  await page.mouse.move(bounds!.x + 20, bounds!.y + 30);
  await page.mouse.down();
  await page.mouse.move(Math.max(1, bounds!.x - 20), bounds!.y + 30);
  await page.mouse.up();
  await expect(dialog).toBeVisible();
  await page.mouse.click(Math.max(1, bounds!.x - 20), bounds!.y + 30);
  await expect(dialog).toHaveCount(0);
  await expect(opener).toBeFocused();
  await expect(search).toHaveValue("Triage order exception");
  await expect(
    page.getByRole("button", { name: "Skills", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  await expect(
    page.getByRole("button", { name: "Table", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  expect(searches).toHaveLength(searchCount);
  expect(records).toHaveLength(1);
  expect(artifacts).toHaveLength(1);
  expect(new URL(page.url()).search).toBe("");
  await expect(
    page.getByRole("link", { name: "Save retained document", exact: true }),
  ).toHaveCount(0);
  await opener.click();
  await dialog
    .getByRole("link", { name: "Open full record", exact: true })
    .click();
  await expect(page).toHaveURL(
    `${catalog.origin}${catalog.basePath}/catalog/${skill.releaseId}`,
  );
  await expect(
    page.getByRole("heading", {
      name: "Triage order exception Skill",
      exact: true,
    }),
  ).toBeVisible();
  await page.goBack();
  await expect(search).toHaveValue("Triage order exception");
  await expect(
    page.getByRole("button", { name: "Skills", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  await expect(
    page.getByRole("button", { name: "Table", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  await expect(opener).toBeVisible();
});

test("reader copies an exact reference and downloads the verified retained Skill document", async ({
  page,
  context,
  catalog,
}) => {
  const skill = catalog.receipt.items.find(
    (item) => item.key === "order-skill",
  )!;
  await context.grantPermissions(["clipboard-read", "clipboard-write"], {
    origin: catalog.origin,
  });
  const reads: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes("/artifacts/")) reads.push(request.url());
  });
  await login(page, catalog, "local-reader", `/catalog/${skill.releaseId}`);
  await expect(
    page.getByRole("region", { name: "Resource handoff" }),
  ).toBeVisible();
  await expect(
    page.locator(".full-record").getByRole("heading", { level: 2 }),
  ).toHaveText([
    "Overview",
    "Validation and assessments",
    "Use this resource",
    "Technical details and evidence",
  ]);
  const assessment = page.getByRole("region", { name: "Resource assessment" });
  await expect(assessment).toContainText("Completed");
  await expect(
    assessment.getByText("Not assessed", { exact: true }),
  ).toHaveCount(2);
  expect(
    await assessment.evaluate((element) =>
      Boolean(
        element.compareDocumentPosition(
          document.querySelector('[aria-label="Resource handoff"]')!,
        ) & Node.DOCUMENT_POSITION_FOLLOWING,
      ),
    ),
  ).toBe(true);
  expect(reads).toHaveLength(0);
  await page
    .getByRole("button", { name: "Copy Resource identifier", exact: true })
    .click();
  await expect(page.getByText("Copied.", { exact: true })).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
    skill.identifier,
  );
  const reply = page.waitForResponse(
    (response) =>
      response.url().includes("/artifacts/") &&
      response.request().method() === "GET",
  );
  await page
    .getByRole("button", {
      name: "Prepare retained document download",
      exact: true,
    })
    .click();
  const response = await reply;
  expect(response.status()).toBe(200);
  const expected = await response.body();
  expect(response.headers()["content-type"]).toContain(
    "application/agent-skills+md",
  );
  const download = page.waitForEvent("download");
  await page
    .getByRole("link", { name: "Save retained document", exact: true })
    .click();
  const saved = await download;
  const bytes = await readFile((await saved.path())!);
  expect(bytes.equals(expected)).toBe(true);
  const hash = createHash("sha256").update(bytes);
  expect(response.headers()["content-digest"]).toBe(
    `sha-256=:${hash.digest("base64")}:`,
  );
  expect(saved.suggestedFilename()).toMatch(
    /^catalog-document-[a-f0-9]{64}\.md$/,
  );
  expect(reads).toHaveLength(1);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  expect(
    await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21aa"])
      .analyze(),
  ).toMatchObject({ violations: [], incomplete: [] });
  await page.getByRole("region", { name: "Resource handoff" }).screenshot({
    path: test.info().outputPath("handoff-narrow.png"),
  });
  await signOut(page);
  await expect(
    page.getByRole("link", { name: "Save retained document", exact: true }),
  ).toHaveCount(0);
});

test("corrupt retained documents never become downloads and real bytes recover with replaced object URLs", async ({
  page,
  catalog,
}) => {
  const skill = catalog.receipt.items.find(
    (item) => item.key === "order-skill",
  )!;
  await login(page, catalog, "local-reader", `/catalog/${skill.releaseId}`);
  const prepare = page.getByRole("button", {
    name: "Prepare retained document download",
    exact: true,
  });
  const save = page.getByRole("link", {
    name: "Save retained document",
    exact: true,
  });
  const downloads: string[] = [];
  page.on("download", (download) =>
    downloads.push(download.suggestedFilename()),
  );
  for (const fault of ["bytes", "digest", "media"]) {
    await page.route(
      "**/artifacts/*/content",
      async (route) => {
        const response = await route.fetch();
        const headers = response.headers();
        delete headers["content-length"];
        if (fault === "digest") headers["content-digest"] = "sha-256=:invalid:";
        if (fault === "media") headers["content-type"] = "invalid";
        await route.fulfill({
          response,
          headers,
          body:
            fault === "bytes"
              ? Buffer.from("tampered retained bytes")
              : await response.body(),
        });
      },
      { times: 1 },
    );
    await prepare.click();
    await expect(page.getByRole("alert")).toContainText(
      "Download could not be prepared or verified",
    );
    await expect(save).toHaveCount(0);
    expect(downloads).toEqual([]);
  }
  await prepare.click();
  await expect(save).toBeVisible();
  const previous = await save.getAttribute("href");
  await prepare.click();
  await expect(save).toBeVisible();
  await expect(save).not.toHaveAttribute("href", previous!);
  // Native CSP blocks fetching blob URLs; exact revocation calls are covered by component tests.
  const pending = page.waitForEvent("download");
  await save.click();
  const download = await pending;
  const bytes = await readFile((await download.path())!);
  expect(bytes.toString()).not.toContain("tampered");
  expect(createHash("sha256").update(bytes).digest("hex")).toBe(
    download
      .suggestedFilename()
      .match(/^catalog-document-([a-f0-9]{64})\.[a-z0-9]+$/)?.[1],
  );
  expect(downloads).toHaveLength(1);
});

test("clipboard permission denial leaves the exact reference available for manual copying", async ({
  page,
  context,
  catalog,
}) => {
  await login(
    page,
    catalog,
    "local-reader",
    `/catalog/${required(catalog.receipt.items[0]).releaseId}`,
  );
  const copy = page.getByRole("button", {
    name: "Copy Resource identifier",
    exact: true,
  });
  await context.grantPermissions(["clipboard-read", "clipboard-write"], {
    origin: catalog.origin,
  });
  await copy.click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
    required(catalog.receipt.items[0]).identifier,
  );
  await context.clearPermissions();
  const session = await context.newCDPSession(page);
  const { targetInfo } = await session.send("Target.getTargetInfo");
  await session.send("Browser.setPermission", {
    permission: { name: "clipboard-write" },
    setting: "denied",
    origin: catalog.origin,
    browserContextId: required(targetInfo.browserContextId),
  });
  expect(
    await page.evaluate(
      async () =>
        (
          await navigator.permissions.query({
            name: "clipboard-write" as PermissionName,
          })
        ).state,
    ),
  ).toBe("denied");
  await page
    .getByRole("button", { name: "Copy Resource identifier", exact: true })
    .click();
  await expect(
    page.getByText(
      "Copy unavailable. Select the reference and copy it manually.",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(
    page
      .locator(".handoff-reference")
      .filter({
        has: page.getByRole("button", {
          name: "Copy Resource identifier",
          exact: true,
        }),
      })
      .locator("code"),
  ).toHaveText(required(catalog.receipt.items[0]).identifier);
  await session.detach();
});

test("sign-out aborts a pending retained download and prevents a late attachment", async ({
  page,
  catalog,
}) => {
  const skill = catalog.receipt.items.find(
    (item) => item.key === "order-skill",
  )!;
  await login(page, catalog, "local-reader", `/catalog/${skill.releaseId}`);
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  let started!: () => void;
  const arrived = new Promise<void>((resolve) => {
    started = resolve;
  });
  const downloads: string[] = [];
  page.on("download", (download) =>
    downloads.push(download.suggestedFilename()),
  );
  await page.route("**/artifacts/*/content", async (route) => {
    const response = await route.fetch();
    started();
    await held;
    await route.fulfill({ response });
  });
  try {
    await page
      .getByRole("button", {
        name: "Prepare retained document download",
        exact: true,
      })
      .click();
    await arrived;
    await signOut(page);
    await expect(
      page.locator('[data-login-alias="local-reader"]'),
    ).toBeVisible();
    release();
    await page.unrouteAll({ behavior: "wait" });
    await expect(
      page.getByRole("link", { name: "Save retained document", exact: true }),
    ).toHaveCount(0);
    await page.locator('[data-login-alias="local-reader"]').click();
    await expect(
      page.getByRole("region", { name: "Resource handoff" }),
    ).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Save retained document", exact: true }),
    ).toHaveCount(0);
    expect(downloads).toEqual([]);
  } finally {
    release();
  }
});

test.describe("touch tag navigation", () => {
  test.use({ hasTouch: true });

  test("subpath preview and full-detail tags apply exact criteria without leaking drafts into URLs", async ({
    page,
    catalog,
  }) => {
    const searches: unknown[] = [];
    page.on("request", (request) => {
      if (request.url().endsWith("/catalog/search"))
        searches.push(request.postDataJSON());
    });
    await login(page, catalog);
    await expect(page.locator("article.resource-card")).toHaveCount(3);
    await page
      .locator("article.resource-card[data-kind=agent]")
      .getByRole("button", { name: "Filter by tag orders", exact: true })
      .tap();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "Remove tag orders", exact: true }),
    ).toBeVisible();
    expect(searches.at(-1)).toEqual({ text: "", limit: 24, tags: ["orders"] });
    await page
      .getByRole("button", { name: "Remove tag orders", exact: true })
      .click();
    await expect(page.locator("article.resource-card")).toHaveCount(3);
    const opener = page
      .locator("article.resource-card[data-kind=agent]")
      .getByRole("button", { name: "Order exception Agent", exact: true });
    await opener.click();
    const dialog = page.getByRole("dialog");
    const bounds = await dialog.boundingBox();
    expect(bounds).not.toBeNull();
    await page.touchscreen.tap(Math.max(1, bounds!.x - 20), bounds!.y + 30);
    await expect(dialog).toHaveCount(0);
    await expect(opener).toBeFocused();
    await opener.press("Enter");
    const tag = dialog.getByRole("button", {
      name: "Filter by tag orders",
      exact: true,
    });
    await tag.tap();
    await expect(dialog).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "Remove tag orders", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("searchbox", { name: "Search Catalog", exact: true }),
    ).toBeFocused();
    expect(searches.at(-1)).toEqual({ text: "", limit: 24, tags: ["orders"] });
    const item = catalog.receipt.items.find((i) => i.key === "order-agent")!;
    await page.goto(
      catalog.origin + catalog.basePath + `/catalog/${item.releaseId}`,
    );
    await expect(
      page.getByRole("heading", {
        name: "Order exception Agent",
        exact: true,
      }),
    ).toBeVisible();
    const count = searches.length;
    await page
      .getByRole("button", { name: "Filter by tag orders", exact: true })
      .click();
    await expect(page).toHaveURL(
      catalog.origin + catalog.basePath + "/catalog",
    );
    await expect(
      page.getByRole("button", { name: "Remove tag orders", exact: true }),
    ).toBeVisible();
    expect(searches).toHaveLength(count + 1);
    expect(searches.at(-1)).toEqual({ text: "", limit: 24, tags: ["orders"] });
    // Only a navigation ordinal is retained, never criteria, results or drafts.
    expect(
      await page.evaluate(() => {
        const state: unknown = history.state;
        return state;
      }),
    ).toEqual({ catalogNavigation: 1 });
    await page.goBack();
    await expect(
      page.getByRole("heading", {
        name: "Order exception Agent",
        exact: true,
      }),
    ).toBeVisible();
    await page.goForward();
    await expect(
      page.getByRole("button", { name: "Remove tag orders", exact: true }),
    ).toBeVisible();
  });
});

test("consumer summaries distinguish real Agent, MCP and Skill metadata without fetching destinations", async ({
  page,
  catalog,
}) => {
  const external: string[] = [];
  const artifacts: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.origin !== catalog.origin) external.push(request.url());
    if (url.pathname.includes("/artifacts/")) artifacts.push(request.url());
  });
  await login(page, catalog);
  for (const [key, title, facts] of [
    [
      "order-agent",
      "Agent interfaces",
      ["Declared Agent version", "Endpoint 1 protocol version", "JSONRPC"],
    ],
    [
      "order-mcp",
      "MCP remote connections",
      ["Declared MCP server version", "streamable-http"],
    ],
    [
      "order-skill",
      "Skill package and source",
      ["A Skill document may omit supporting files."],
    ],
  ] as const) {
    const item = required(
      catalog.receipt.items.find((entry) => entry.key === key),
    );
    await page.goto(catalog.origin + "/catalog/" + item.releaseId);
    const handoff = page.getByRole("region", { name: "Resource handoff" });
    await expect(
      handoff.getByRole("heading", { name: "primary: " + title, exact: true }),
    ).toBeVisible();
    for (const fact of facts)
      await expect(
        handoff.getByText(fact, { exact: key !== "order-skill" }),
      ).toBeVisible();
    await expect(
      handoff.getByText(/does not grant permission to install/),
    ).toBeVisible();
    await expect(
      handoff.getByRole("button", { name: /^(Install|Run|Execute)$/ }),
    ).toHaveCount(0);
  }
  expect(external).toEqual([]);
  expect(artifacts).toEqual([]);
});

test("published Registry packages retain distinct versions and safe consumer summaries", async ({
  page,
  catalog,
}) => {
  // API calls establish independently reviewed prerequisites. The consumer read
  // below uses the real host/API and persisted projection, without interception.
  const document = {
    name: "example.test/browser-mcp",
    description: "Synthetic Registry package handoff",
    version: "2.3.0",
    packages: [
      {
        registryType: "npm",
        identifier: "@example/browser-mcp",
        version: "4.5.0",
        transport: { type: "stdio" },
        environmentVariables: [
          {
            name: "EXAMPLE_INPUT",
            default: "synthetic-default-not-for-summary",
          },
        ],
      },
      {
        registryType: "oci",
        identifier: "registry.example.test/browser-mcp",
        version: "6.7.0",
        transport: { type: "stdio" },
      },
    ],
    remotes: [
      {
        type: "streamable-http",
        url: "https://mcp.example.test/mcp",
        headers: [
          { name: "X-Example", value: "synthetic-header-not-for-summary" },
        ],
      },
    ],
  };
  const resource = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/resources",
    {
      namespaceId: catalog.receipt.namespaceId,
      kind: "mcp_server",
      canonicalIdentifier:
        "urn:air:demo.catalog.example.com:demo:registry-handoff",
    },
  );
  const version = await catalog.admin<{
    revision: { id: string };
    evidence: { state: string };
  }>("POST", `/api/v1/resources/${resource.data.id}/versions`, {
    version: "2.3.0",
    representations: [
      {
        role: "primary",
        mediaType: "application/vnd.mcp.server+json",
        data: document,
      },
    ],
  });
  expect(version.data.evidence.state).toBe("complete");
  const listing = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/catalog-listings",
    {
      namespaceId: catalog.receipt.namespaceId,
      resourceId: resource.data.id,
    },
  );
  const revision = await catalog.admin(
    "POST",
    `/api/v1/catalog-listings/${listing.data.id}/revisions`,
    {
      versionRevisionId: version.data.revision.id,
      publisherId: catalog.receipt.publisherId,
      displayName: "Registry handoff MCP Server",
      description: "Synthetic Registry package handoff",
      visibility: "private",
    },
    listing.etag,
  );
  const submission = await catalog.admin<{ id: string }>(
    "POST",
    `/api/v1/catalog-listings/${listing.data.id}/submissions`,
    {},
  );
  await catalog.admin(
    "POST",
    `/api/v1/review-submissions/${submission.data.id}:approve`,
    { reason: "Independent Registry handoff review" },
    submission.etag,
    "local-reviewer",
  );
  const published = await catalog.admin<{ release: { id: string } }>(
    "POST",
    `/api/v1/catalog-listings/${listing.data.id}:publish`,
    { submissionId: submission.data.id },
    revision.etag,
  );
  const foreign: string[] = [];
  page.on("request", (request) => {
    if (new URL(request.url()).origin !== catalog.origin)
      foreign.push(request.url());
  });
  await login(
    page,
    catalog,
    "local-reader",
    `/catalog/${published.data.release.id}`,
  );
  const handoff = page.getByRole("region", { name: "Resource handoff" });
  await expect(
    handoff.getByRole("heading", {
      name: "primary: MCP transports and packages",
      exact: true,
    }),
  ).toBeVisible();
  for (const [label, value] of [
    ["Declared MCP server version", "2.3.0"],
    ["Package 1 registry", "npm"],
    ["Package 1 identifier", "@example/browser-mcp"],
    ["Package 1 version", "4.5.0"],
    ["Package 1 transport", "stdio"],
    ["Package 2 registry", "oci"],
    ["Package 2 identifier", "registry.example.test/browser-mcp"],
    ["Package 2 version", "6.7.0"],
  ] as const) {
    await expect(
      handoff
        .locator("dt")
        .filter({ hasText: new RegExp(`^${label}$`) })
        .locator("+ dd"),
    ).toHaveText(value);
  }
  await expect(
    handoff.getByText("OCI distribution", { exact: true }),
  ).toBeVisible();
  await expect(
    handoff.getByText("Declared remote interface", { exact: true }),
  ).toBeVisible();
  await expect(
    handoff.getByText(/Connection headers are declared/),
  ).toBeVisible();
  const decoded = handoff
    .locator("details")
    .filter({ has: page.getByText("View decoded metadata", { exact: true }) });
  await expect(decoded).not.toHaveAttribute("open");
  // The complete authorized document remains available only through its
  // explicit disclosure. Summary facts and copy references still omit values.
  const summary = await handoff.evaluate((element) => {
    const copy = element.cloneNode(true) as HTMLElement;
    for (const details of copy.querySelectorAll("details")) {
      if (
        details.querySelector("summary")?.textContent ===
        "View decoded metadata"
      )
        details.remove();
    }
    return copy.textContent;
  });
  expect(summary).not.toContain("synthetic-default-not-for-summary");
  expect(summary).not.toContain("synthetic-header-not-for-summary");
  await expect(
    decoded.getByRole("group", { name: "JSON document" }),
  ).not.toBeVisible();
  for (const reference of await handoff.locator(".handoff-reference").all()) {
    await expect(reference).not.toContainText(
      "synthetic-default-not-for-summary",
    );
    await expect(reference).not.toContainText(
      "synthetic-header-not-for-summary",
    );
  }
  await decoded.locator("summary").click();
  await expect(
    decoded.getByRole("group", { name: "JSON document" }),
  ).toContainText("synthetic-default-not-for-summary");
  await expect(
    decoded.getByRole("button", { name: /Copy|Export/ }),
  ).toHaveCount(0);
  await expect(
    handoff.getByRole("button", { name: /^(Install|Run|Execute)$/ }),
  ).toHaveCount(0);
  await handoff.getByText("Representation evidence", { exact: true }).click();
  await expect(
    handoff.locator("details").filter({
      has: page.getByText("Representation evidence", { exact: true }),
    }),
  ).toContainText("mcp-registry-server-1.8.1-f52dc85");
  // Omitted summary inputs must still be retained exactly, not erased at ingestion.
  const retained = await catalog.admin<{
    revision: { representations: { data: unknown }[] };
  }>("GET", `/api/v1/version-revisions/${version.data.revision.id}`);
  expect(required(retained.data.revision.representations[0]).data).toEqual(
    document,
  );
  expect(foreign).toEqual([]);
});
