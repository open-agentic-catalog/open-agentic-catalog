import { object, array, text } from "./json";
import { required } from "../required";
// Local-file and prefill journeys save through real API commands and inspect persisted revisions.
import AxeBuilder from "@axe-core/playwright";
import { test, expect, login, confirmCommand } from "./fixtures";
import { agentDocument } from "./published-fixture";
import { openDisclosure } from "./editor-disclosure";

test("author loads an Agent Card and explicitly prefills a listing from its saved revision", async ({
  page,
  catalog,
  network,
}) => {
  const item = catalog.receipt.items.find(
    (entry) => entry.key === "draft-agent",
  )!;
  const document = {
    ...agentDocument,
    name: "Uploaded travel agent",
    description: "Helps plan a trip.",
    version: "3.0.0",
  };
  await login(
    page,
    catalog,
    "local-contributor",
    `/resources/${item.resourceId}`,
  );
  await page.getByRole("link", { name: "Add version", exact: true }).click();
  await openDisclosure(page, "Advanced Agent Card JSON");
  await openDisclosure(page, "Import an existing Agent document");
  await page.getByLabel("Version label", { exact: true }).fill("3.0.0");
  const editor = page.getByRole("textbox", {
    name: "Standards document (JSON)",
    exact: true,
  });
  await editor.fill('{"unsaved":"keep me"}');
  const writes: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().includes("/_catalog/"))
      writes.push(request.url());
  });
  for (const [name, bytes] of [
    ["empty.json", Buffer.alloc(0)],
    ["oversized.json", Buffer.alloc(2 * 1024 * 1024 + 1, 32)],
    ["blank.json", Buffer.from("   ")],
    ["nul.json", Buffer.from("x\0y")],
  ] as const) {
    await page
      .getByLabel("Document file", { exact: true })
      .setInputFiles({ name, mimeType: "application/json", buffer: bytes });
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Use loaded document", exact: true }),
    ).toHaveCount(0);
    await expect(editor).toHaveValue('{"unsaved":"keep me"}');
    expect(writes).toHaveLength(0);
  }
  await page.getByLabel("Document file", { exact: true }).setInputFiles({
    name: "card.json",
    mimeType: "application/json",
    buffer: Buffer.from(JSON.stringify(document)),
  });
  await expect(page.getByText("Ready to load:")).toBeVisible();
  await expect(editor).toHaveValue('{"unsaved":"keep me"}');
  await page
    .getByRole("button", { name: "Create version", exact: true })
    .click();
  await expect(page.getByRole("alert")).toContainText(
    "Use or discard the selected document",
  );
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(writes).toHaveLength(0);
  page.once("dialog", (dialog) => dialog.dismiss());
  await page
    .getByRole("button", { name: "Use loaded document", exact: true })
    .click();
  await expect(editor).toHaveValue('{"unsaved":"keep me"}');
  page.once("dialog", (dialog) => dialog.accept());
  await page
    .getByRole("button", { name: "Use loaded document", exact: true })
    .click();
  await expect(editor).toHaveValue(JSON.stringify(document));
  await expect(editor).toBeFocused();
  expect(writes).toHaveLength(0);
  const created = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith(`/resources/${item.resourceId}/versions`),
  );
  await page
    .getByRole("button", { name: "Create version", exact: true })
    .click();
  await confirmCommand(page);
  const bundle = object(await (await created).json());
  const revision = object(bundle.revision);
  const revisionId = text(revision.id);
  expect(object(bundle.evidence).state).toBe("complete");
  await page
    .getByRole("link", { name: "Open saved version", exact: true })
    .click();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Version 3.0.0", exact: true }),
  ).toBeVisible();
  const retained = await catalog.admin<{
    revision: { representations: { data: unknown }[] };
  }>("GET", `/api/v1/version-revisions/${revisionId}`);
  expect(required(retained.data.revision.representations[0]).data).toEqual(
    document,
  );

  await page.goto(
    `${catalog.origin}${catalog.basePath}/listings/${item.listingId}`,
  );
  await page
    .getByRole("button", { name: "New listing revision", exact: true })
    .click();
  await page
    .getByRole("checkbox", { name: "Keep the current version revision" })
    .uncheck();
  await expect(
    page.getByRole("option", { name: "3.0.0", exact: true }),
  ).toHaveCount(1);
  await page
    .getByRole("combobox", { name: "Resource version", exact: true })
    .selectOption(text(object(bundle.version).id));
  await expect(
    page
      .getByRole("combobox", { name: "Version revision", exact: true })
      .locator(`option[value="${revisionId}"]`),
  ).toHaveCount(1);
  await page
    .getByRole("combobox", { name: "Version revision", exact: true })
    .selectOption(revisionId);
  const name = page.getByLabel("Display name", { exact: true });
  await name.fill("My edited title");
  const publisher = await page
    .getByRole("combobox", { name: "Publisher", exact: true })
    .inputValue();
  const visibility = await page
    .getByRole("combobox", { name: "Visibility", exact: true })
    .inputValue();
  const before = writes.length;
  network.expectFailure(
    "GET",
    `/_catalog/api/v1/version-revisions/${revisionId}`,
    503,
  );
  await page.route(
    `**/version-revisions/${revisionId}`,
    (route) =>
      route.fulfill({
        status: 503,
        contentType: "application/problem+json",
        body: '{"status":503,"code":"UNAVAILABLE","title":"Unavailable"}',
      }),
    { times: 1 },
  );
  await page
    .getByRole("button", {
      name: "Load name and description suggestions",
      exact: true,
    })
    .click();
  await expect(
    page.getByRole("region", { name: "Listing prefill" }).getByRole("alert"),
  ).toBeVisible();
  await expect(name).toHaveValue("My edited title");
  expect(writes).toHaveLength(before);

  await page
    .getByRole("button", {
      name: "Load name and description suggestions",
      exact: true,
    })
    .click();
  await page
    .getByRole("combobox", { name: "Prefill representation", exact: true })
    .selectOption(text(object(array(revision.representations)[0]).id));
  await expect(name).toHaveValue("My edited title");
  page.once("dialog", (dialog) => dialog.dismiss());
  await page
    .getByRole("button", {
      name: "Use suggested name and description",
      exact: true,
    })
    .click();
  await expect(name).toHaveValue("My edited title");
  page.once("dialog", (dialog) => dialog.accept());
  await page
    .getByRole("button", {
      name: "Use suggested name and description",
      exact: true,
    })
    .click();
  await expect(name).toHaveValue(document.name);
  await expect(
    page.getByRole("textbox", { name: "Description", exact: true }),
  ).toHaveValue(document.description);
  expect(writes).toHaveLength(before);
  await expect(
    page.getByRole("combobox", { name: "Publisher", exact: true }),
  ).toHaveValue(publisher);
  await expect(
    page.getByRole("combobox", { name: "Visibility", exact: true }),
  ).toHaveValue(visibility);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => globalThis.document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  expect(
    await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21aa"])
      .analyze(),
  ).toMatchObject({ violations: [], incomplete: [] });
  await page
    .getByRole("region", { name: "Listing prefill" })
    .screenshot({ path: test.info().outputPath("prefill-narrow.png") });
  const saved = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith(`/catalog-listings/${item.listingId}/revisions`),
  );
  await page
    .getByRole("button", { name: "Create listing revision", exact: true })
    .click();
  await confirmCommand(page);
  const listing = object(await (await saved).json());
  expect(listing.revision).toMatchObject({
    displayName: document.name,
    description: document.description,
    publisherId: publisher,
    visibility,
    versionRevisionId: revisionId,
  });
  await page
    .getByRole("link", {
      name: "Reload listing to review saved revision",
      exact: true,
    })
    .click();
  await page.reload();
  await page
    .getByRole("link", { name: "Inspect candidate revision", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: document.name, exact: true }),
  ).toBeVisible();
});

test("author rejects malformed local text and saves a UTF-8 Skill Markdown document", async ({
  page,
  catalog,
}) => {
  const item = catalog.receipt.items.find(
    (entry) => entry.key === "order-skill",
  )!;
  await login(
    page,
    catalog,
    "local-contributor",
    `/resources/${item.resourceId}`,
  );
  await page.getByRole("link", { name: "Add version", exact: true }).click();
  await page.getByLabel("Version label", { exact: true }).fill("3.0.0");
  await openDisclosure(page, "Import an existing Skill document");
  const input = page.getByLabel("Document file", { exact: true });
  await input.setInputFiles({
    name: "broken.md",
    mimeType: "text/markdown",
    buffer: Buffer.from([255]),
  });
  await expect(page.getByRole("alert")).toContainText("UTF-8");
  await expect(
    page.getByRole("button", { name: "Use loaded document" }),
  ).toHaveCount(0);
  const markdown =
    "---\nname: travel-helper\ndescription: Helps plan a café visit.\n---\n\n# Travel helper\nAsk about travel preferences.\n";
  await input.setInputFiles({
    name: "SKILL.md",
    mimeType: "text/markdown",
    buffer: Buffer.from(markdown),
  });
  await page
    .getByRole("button", { name: "Use loaded document", exact: true })
    .click();
  await expect(
    page.getByRole("textbox", { name: "Text content", exact: true }),
  ).toHaveValue(markdown);
  const saved = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith(`/resources/${item.resourceId}/versions`),
  );
  await page
    .getByRole("button", { name: "Create version", exact: true })
    .click();
  await confirmCommand(page);
  const bundle = object(await (await saved).json());
  expect(object(bundle.evidence).state).toBe("complete");
  await page
    .getByRole("link", { name: "Open saved version", exact: true })
    .click();
  await page.reload();
  await page
    .getByRole("link", {
      name: "Inspect current revision and validation",
      exact: true,
    })
    .click();
  await page
    .getByRole("button", { name: "Read retained document", exact: true })
    .click();
  await expect(page.locator("pre:visible")).toContainText(markdown.trim());
});

test("creation-only users reach Authoring, enter an exact namespace and retain a real command receipt without resource reads", async ({
  page,
  catalog,
}) => {
  const memberships = await catalog.admin<{
    items: { id: string; subject: string }[];
  }>("GET", "/api/v1/memberships?limit=100");
  const member = required(
    memberships.data.items.find((item) => item.subject === "local-no-role"),
  );
  const role = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/access/roles",
    {
      name: "Browser creation only",
      permissions: ["namespace.read", "resource.create"],
      reason: "Qualify the creation-only workflow",
    },
  );
  await catalog.admin("POST", "/api/v1/access/assignments", {
    membershipId: member.id,
    roleDefinitionId: role.data.id,
    namespaceId: catalog.receipt.namespaceId,
    reason: "Limit creation to one namespace",
  });
  const other = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/namespaces",
    {
      slug: "other-creation-destination",
      displayName: "Unassigned destination",
    },
  );
  const resourceReads: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "GET" &&
      new URL(request.url()).pathname.startsWith("/_catalog/api/v1/resources")
    )
      resourceReads.push(request.url());
  });
  await login(page, catalog, "local-no-role", "/access");
  await page
    .getByRole("navigation", { name: "Workspace", exact: true })
    .getByRole("link", { name: "Authoring", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Authoring", level: 1 }),
  ).toBeVisible();
  await page
    .getByRole("link", { name: "Create resource", exact: true })
    .click();
  await page.getByLabel("Namespace ID", { exact: true }).fill(other.data.id);
  await page
    .getByLabel("Canonical resource identifier", { exact: true })
    .fill("urn:air:catalog.example.com:plugins:creation-only-browser");
  await expect(
    page.getByRole("button", { name: "Create resource", exact: true }),
  ).toBeDisabled();
  await page
    .getByLabel("Namespace ID", { exact: true })
    .fill(catalog.receipt.namespaceId);
  await page
    .getByRole("combobox", { name: "Kind", exact: true })
    .selectOption("plugin");
  const saved = page.waitForResponse(
    (reply) =>
      reply.request().method() === "POST" &&
      new URL(reply.url()).pathname === "/_catalog/api/v1/resources",
  );
  await page
    .getByRole("button", { name: "Create resource", exact: true })
    .click();
  await confirmCommand(page);
  const response = await saved;
  expect(response.status()).toBe(201);
  const id = text(object(await response.json()).id);
  await page.getByText("Created resource references", { exact: true }).click();
  await expect(page.locator("details[open]")).toContainText(id);
  await expect(
    page.getByRole("link", { name: "Open created resource", exact: true }),
  ).toHaveCount(0);
  expect(resourceReads).toEqual([]);
  await page.setViewportSize({ width: 320, height: 800 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
  await page.goto(`${catalog.origin}${catalog.basePath}/resources/${id}`);
  await expect(page.getByRole("alert")).toContainText(
    "This page is unavailable with your current access",
  );
  expect(resourceReads).toEqual([]);
});

test("validation-only users inspect and revalidate the canonical revision without parent metadata reads", async ({
  page,
  catalog,
}) => {
  const item = required(
    catalog.receipt.items.find((entry) => entry.key === "draft-agent"),
  );
  const members = await catalog.admin<{
    items: { id: string; subject: string }[];
  }>("GET", "/api/v1/memberships?limit=100");
  const member = required(
    members.data.items.find((entry) => entry.subject === "local-no-role"),
  );
  const role = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/access/roles",
    {
      name: "Validation without metadata",
      permissions: ["resource.read_content", "resource.validate"],
      reason: "Qualify exact revision validation",
    },
  );
  await catalog.admin("POST", "/api/v1/access/assignments", {
    membershipId: member.id,
    roleDefinitionId: role.data.id,
    namespaceId: catalog.receipt.namespaceId,
    reason: "Validate only the fixture namespace",
  });
  const reads: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "GET" &&
      new URL(request.url()).pathname.includes("/_catalog/api/v1/")
    )
      reads.push(new URL(request.url()).pathname);
  });
  const initialRead = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      response
        .url()
        .endsWith(`/api/v1/version-revisions/${item.versionRevisionId}`),
  );
  await login(
    page,
    catalog,
    "local-no-role",
    `/version-revisions/${item.versionRevisionId}`,
  );
  const original = object(await (await initialRead).json());
  const action = page.getByRole("button", {
    name: "Revalidate revision",
    exact: true,
  });
  await expect(action).toBeEnabled();
  await expect(
    page.getByRole("link", { name: "Back to version", exact: true }),
  ).toHaveCount(0);
  const validated = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response
        .url()
        .endsWith(
          `/api/v1/version-revisions/${item.versionRevisionId}:validate`,
        ),
  );
  await action.click();
  await confirmCommand(page);
  const response = await validated;
  expect(response.status()).toBe(201);
  const body = object(await response.json());
  expect(body.namespaceId).toBe(catalog.receipt.namespaceId);
  expect(object(body.revision).id).toBe(item.versionRevisionId);
  expect(object(body.revision).contentSetDigest).toBe(
    object(original.revision).contentSetDigest,
  );
  expect(object(body.evidence).id).not.toBe(object(original.evidence).id);
  expect(response.request().postDataJSON()).toEqual({});
  await expect(page.getByText(/Validation:/).last()).toBeVisible();
  const retainedRead = page.waitForResponse(
    (reply) =>
      reply.request().method() === "GET" &&
      reply
        .url()
        .endsWith(`/api/v1/version-revisions/${item.versionRevisionId}`),
  );
  await page.reload();
  const retained = await retainedRead;
  expect(retained.status()).toBe(200);
  expect(await retained.json()).toMatchObject({
    namespaceId: catalog.receipt.namespaceId,
    revision: {
      id: item.versionRevisionId,
      contentSetDigest: object(original.revision).contentSetDigest,
      evidenceSetId: object(body.evidence).id,
    },
    evidence: { id: object(body.evidence).id, state: "complete" },
  });
  expect(
    reads.some((path) =>
      /\/api\/v1\/(resources|resource-versions|artifacts)(\/|$)/.test(path),
    ),
  ).toBe(false);
  expect(reads).toContain(
    `/_catalog/api/v1/version-revisions/${item.versionRevisionId}`,
  );
  await expect(
    page.getByRole("heading", {
      name: "Latest validation: Complete",
      exact: true,
    }),
  ).toBeVisible();
});

test("history-only users read revision summaries and releases without content links or reads", async ({
  page,
  catalog,
}) => {
  const item = required(
    catalog.receipt.items.find((entry) => entry.key === "order-agent"),
  );
  const retained = await catalog.admin<{
    revision: { resourceVersionId: string };
  }>("GET", `/api/v1/version-revisions/${item.versionRevisionId}`);
  const members = await catalog.admin<{
    items: { id: string; subject: string }[];
  }>("GET", "/api/v1/memberships?limit=100");
  const member = required(
    members.data.items.find((entry) => entry.subject === "local-no-role"),
  );
  const role = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/access/roles",
    {
      name: "History without content",
      permissions: ["resource.read", "resource.read_history"],
      reason: "Qualify metadata and summary access without document reads",
    },
  );
  await catalog.admin("POST", "/api/v1/access/assignments", {
    membershipId: member.id,
    roleDefinitionId: role.data.id,
    namespaceId: catalog.receipt.namespaceId,
    reason: "Read history only in the fixture namespace",
  });
  const contentReads: string[] = [];
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (
      request.method() === "GET" &&
      (/\/_catalog\/api\/v1\/(version-revisions|listing-revisions|artifacts)(\/|$)/.test(
        path,
      ) ||
        /\/_catalog\/api\/v1\/review-submissions\/[^/]+\/evidence$/.test(path))
    )
      contentReads.push(path);
  });
  await login(
    page,
    catalog,
    "local-no-role",
    `/versions/${retained.data.revision.resourceVersionId}`,
  );
  await expect(
    page.getByRole("heading", { name: "Revision history", exact: true }),
  ).toBeVisible();
  await expect(page.getByText("Revision 1", { exact: true })).toBeVisible();
  await expect(page.locator('a[href*="/version-revisions/"]')).toHaveCount(0);
  await page.goto(
    `${catalog.origin}${catalog.basePath}/listings/${item.listingId}`,
  );
  await expect(page.getByText(/^Revision 1 by /)).toBeVisible();
  await expect(page.locator('a[href*="/listing-revisions/"]')).toHaveCount(0);
  await page
    .getByRole("button", { name: "Release history", exact: true })
    .click();
  const releases = page.locator(".workspace-panel").filter({
    has: page.getByRole("heading", { name: "Release history", exact: true }),
  });
  await expect(releases.locator('a[href*="/releases/"]').first()).toBeVisible();
  await releases.locator('a[href*="/releases/"]').first().click();
  await expect(
    page.getByRole("heading", { name: "Release published", exact: true }),
  ).toBeVisible();
  await expect(
    page.locator(
      'a[href*="/version-revisions/"], a[href*="/listing-revisions/"]',
    ),
  ).toHaveCount(0);
  await expect(
    page.getByText(item.versionRevisionId, { exact: true }).first(),
  ).toBeVisible();
  await page
    .getByRole("link", { name: "Approved submission", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Review approved", exact: true }),
  ).toBeVisible();
  await expect(
    page.locator(
      'a[href*="/version-revisions/"], a[href*="/listing-revisions/"]',
    ),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", {
      name: "Inspect submitted evidence",
      exact: true,
    }),
  ).toHaveCount(0);
  await expect(
    page.getByText(
      "Submitted documents and evidence require content access to this namespace.",
      { exact: true },
    ),
  ).toBeVisible();
  expect(contentReads).toEqual([]);
});
