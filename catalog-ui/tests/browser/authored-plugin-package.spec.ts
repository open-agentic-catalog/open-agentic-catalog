// Actual downloads are packed and matched byte for byte to the immutable HTTPS fixture.
import { test, expect, login, confirmCommand } from "./fixtures";
import { required } from "../required";
import { array, object, text } from "./json";
import { openDisclosure } from "./editor-disclosure";
import {
  downloadPluginFiles,
  fillPluginFields,
} from "./authoring-plugin-fields";

test.use({ demoArchives: true, authoredPlugin: true });
test("Plugin tutorial downloads pass real archive admission with their exact files", async ({
  page,
  catalog,
}, testInfo) => {
  const archive = required(catalog.generatedPlugin);
  const writes: string[] = [];
  const outside: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (
      ["http:", "https:"].includes(url.protocol) &&
      url.origin !== catalog.origin
    )
      outside.push(url.origin);
    if (
      request.method() === "POST" &&
      url.pathname.includes("/_catalog/api/v1/")
    )
      writes.push(url.pathname);
  });
  await login(page, catalog, "local-contributor", "/resources/new");
  await page
    .getByRole("combobox", { name: "Namespace", exact: true })
    .selectOption(catalog.receipt.namespaceId);
  await page
    .getByRole("combobox", { name: "Kind", exact: true })
    .selectOption("plugin");
  await page
    .getByLabel("Canonical resource identifier", { exact: true })
    .fill("urn:air:demo.catalog.example.com:plugins:review-tools-tutorial");
  await page
    .getByRole("button", { name: "Next: Content", exact: true })
    .click();
  await page.getByLabel("Version label", { exact: true }).fill("1.0.0");
  await fillPluginFields(page);
  await page.evaluate(() => {
    (document.activeElement as HTMLElement).blur();
    window.scrollTo(0, 0);
  });
  await page.screenshot({
    path: testInfo.outputPath("plugin-tutorial-fields.png"),
    fullPage: true,
  });
  const documents = await downloadPluginFiles(page);
  await archive.verifyDownloads(documents);
  expect(writes).toEqual([]);
  expect(outside).toEqual([]);
  await page
    .getByRole("button", { name: "Continue with archive inputs", exact: true })
    .click();
  await page
    .getByLabel("Plugin archive URL", { exact: true })
    .fill(archive.url);
  await page
    .getByLabel("Expected archive checksum (optional)", { exact: true })
    .fill(archive.digest);
  await page.getByLabel("Include Listing details", { exact: true }).check();
  await page
    .getByRole("button", { name: "Next: Listing details", exact: true })
    .click();
  await page
    .getByRole("combobox", { name: "Publisher", exact: true })
    .selectOption(catalog.receipt.publisherId);
  const saved = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      /\/resources\/[^/]+\/versions$/.test(new URL(response.url()).pathname),
  );
  await page
    .getByRole("button", { name: "Save content draft", exact: true })
    .click();
  await confirmCommand(page);
  const response = await saved;
  expect(response.status()).toBe(201);
  const bundle = object(await response.json());
  expect(object(bundle.evidence).state).toBe("complete");
  await expect(
    page.getByRole("heading", { name: "Listing draft saved", exact: true }),
  ).toBeVisible();
  const retained = await catalog.admin(
    "GET",
    `/api/v1/version-revisions/${text(object(bundle.revision).id)}`,
  );
  const representation = object(
    required(array(object(retained.data.revision).representations)[0]),
  );
  expect(representation.contentDigest).toBe(archive.digest);
  expect(representation.size).toBe(archive.size);
  const plugin = object(representation.plugin);
  expect(
    array(plugin.documents)
      .map((value) => {
        const item = object(value);
        return { path: item.path, content: item.content };
      })
      .sort((a, b) => text(a.path).localeCompare(text(b.path))),
  ).toEqual([...documents].sort((a, b) => a.path.localeCompare(b.path)));
  expect(array(plugin.components)).toHaveLength(2);
  expect(writes).toHaveLength(4);
  expect(outside).toEqual([]);

  // Reusing the exact archive must preserve native bytes, including its declared
  // 1.0.0 version. A different Catalog label remains a nonpublishable draft until
  // the author supplies a complete replacement archive with matching metadata.
  const sourceRevisionId = text(object(bundle.revision).id);
  const sourceVersionId = text(object(bundle.version).id);
  const resourceId = text(object(bundle.version).resourceId);
  const listingsPath = `/api/v1/resources/${resourceId}/listings?limit=2`;
  const listingsBefore = await catalog.admin("GET", listingsPath);
  expect(listingsBefore.data.nextCursor).toBeUndefined();
  expect(array(listingsBefore.data.items)).toHaveLength(1);
  const listingId = text(
    object(required(array(listingsBefore.data.items)[0])).id,
  );
  const listingPath = `/api/v1/catalog-listings/${listingId}`;
  const listingBefore = await catalog.admin("GET", listingPath);
  const listingRevisionPath = `/api/v1/listing-revisions/${text(listingBefore.data.candidateRevisionId)}`;
  const listingMetadataBefore = await catalog.admin("GET", listingRevisionPath);
  expect(listingMetadataBefore.data).toMatchObject({
    listingId,
    versionRevisionId: sourceRevisionId,
  });
  const listingHistoryPath = `${listingPath}/revisions?limit=25`;
  const listingHistoryBefore = await catalog.admin("GET", listingHistoryPath);
  expect(listingHistoryBefore.data.nextCursor).toBeUndefined();
  expect(
    array(listingHistoryBefore.data.items).map((revision) =>
      text(object(revision).id),
    ),
  ).toEqual([listingBefore.data.candidateRevisionId]);
  expect(plugin.rootDirectory).toBe("");
  expect(representation.url).toBe(archive.url);
  const reuseRequests: {
    method: string;
    path: string;
    body: unknown;
    key: string | undefined;
  }[] = [];
  const artifactReads: string[] = [];
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (!path.includes("/_catalog/api/v1/")) return;
    if (request.method() === "GET") {
      if (path.includes("/artifacts/")) artifactReads.push(path);
    } else
      reuseRequests.push({
        method: request.method(),
        path,
        body: object(request.postDataJSON()),
        key: request.headers()["idempotency-key"],
      });
  });
  await page
    .getByRole("link", { name: "Open saved version", exact: true })
    .click();
  await expect(page).toHaveURL(
    `${catalog.origin}${catalog.basePath}/versions/${sourceVersionId}`,
  );
  await page
    .getByRole("link", {
      name: "Create new Version from this revision",
      exact: true,
    })
    .click();
  await expect(
    page.getByLabel("Selected source", { exact: true }),
  ).toContainText(sourceRevisionId);
  await expect(
    page.getByRole("textbox", { name: "Plugin archive URL", exact: true }),
  ).toHaveValue(archive.url);
  await expect(
    page.getByRole("textbox", {
      name: "Expected archive checksum (optional)",
      exact: true,
    }),
  ).toHaveValue(archive.digest);
  await expect(
    page.getByRole("combobox", { name: "Plugin archive format", exact: true }),
  ).toHaveValue("application/agent-plugins+zip");
  await page
    .getByRole("textbox", { name: "Version label", exact: true })
    .fill("1.1.0");
  await openDisclosure(page, "Original Plugin files");
  const originalFiles = page.locator("details").filter({
    has: page.locator("summary", { hasText: "Original Plugin files" }),
  });
  await expect(originalFiles).toContainText(
    "Only selected documents are retained. Restore scripts, assets and other files from the original package before building a replacement archive.",
  );
  await expect(originalFiles).toContainText(
    "Prepared files do not recreate the original archive. Submit a replacement HTTPS archive after any manifest or component change.",
  );
  await expect(originalFiles.locator("li")).toHaveCount(3);
  for (const document of documents)
    await expect(
      originalFiles
        .locator("li")
        .filter({ has: page.getByText(document.path, { exact: true }) }),
    ).toHaveText(`${document.path}: Retained document`);
  await page
    .getByRole("combobox", { name: "Plugin input", exact: true })
    .selectOption("prepare");
  await expect(
    page.getByRole("textbox", { name: "Plugin name", exact: true }),
  ).toHaveValue("review-tools");
  await expect(
    page.getByRole("textbox", {
      name: "Plugin version (optional)",
      exact: true,
    }),
  ).toHaveValue("1.0.0");
  await expect(
    page.getByRole("textbox", {
      name: "Plugin description (optional)",
      exact: true,
    }),
  ).toHaveValue("Prepare a release review.");
  await expect(
    page.getByRole("textbox", { name: "Skill name", exact: true }),
  ).toHaveValue("review-notes");
  await expect(
    page.getByRole("textbox", {
      name: "Skill instructions (Markdown)",
      exact: true,
    }),
  ).toHaveValue("# Review\n\nRead the release notes.");
  await expect(
    page.getByRole("textbox", { name: "Server 1 name", exact: true }),
  ).toHaveValue("documentation");
  await expect(
    page.getByRole("textbox", { name: "MCP endpoint URL", exact: true }),
  ).toHaveValue("https://mcp.example.test/documentation");
  const reusedDocuments = await downloadPluginFiles(page);
  expect(reusedDocuments).toEqual(documents);
  expect(reuseRequests).toEqual([]);
  expect(artifactReads).toEqual([]);
  expect(writes).toHaveLength(4);
  expect(outside).toEqual([]);
  await page
    .getByRole("button", { name: "Continue with archive inputs", exact: true })
    .click();
  await expect(
    page.getByRole("textbox", { name: "Plugin archive URL", exact: true }),
  ).toHaveValue(archive.url);
  await expect(
    page.getByRole("textbox", {
      name: "Expected archive checksum (optional)",
      exact: true,
    }),
  ).toHaveValue(archive.digest);
  await page
    .getByRole("button", { name: "Create version", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toContainText(sourceRevisionId);
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Cancel", exact: true })
    .click();
  expect(reuseRequests).toEqual([]);
  expect(writes).toHaveLength(4);
  await expect(
    page.getByRole("textbox", { name: "Plugin archive URL", exact: true }),
  ).toHaveValue(archive.url);
  await expect(
    page.getByRole("textbox", {
      name: "Expected archive checksum (optional)",
      exact: true,
    }),
  ).toHaveValue(archive.digest);
  await page
    .getByRole("button", { name: "Create version", exact: true })
    .click();
  const [reusedResponse] = await Promise.all([
    page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        new URL(response.url()).pathname.endsWith(
          `/resources/${resourceId}/versions`,
        ),
    ),
    confirmCommand(page),
  ]);
  expect(reusedResponse.status()).toBe(201);
  const reusedBundle = object(await reusedResponse.json());
  expect(object(reusedBundle.evidence).state).toBe("blocked");
  expect(array(object(reusedBundle.evidence).findings)).toEqual(
    expect.arrayContaining([
      expect.objectContaining({ code: "NATIVE_VERSION_MISMATCH" }),
    ]),
  );
  await expect(
    page.getByRole("heading", {
      name: "Draft saved, corrections needed",
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    page.getByText("NATIVE_VERSION_MISMATCH", { exact: true }),
  ).toBeVisible();
  expect(reuseRequests).toEqual([
    {
      method: "POST",
      path: `${catalog.basePath}/_catalog/api/v1/resources/${resourceId}/versions`,
      body: {
        version: "1.1.0",
        representations: [
          {
            role: "primary",
            mediaType: "application/agent-plugins+zip",
            profileId: "agent-plugins-1.0.0-1fc1b627",
            url: archive.url,
            expectedDigest: archive.digest,
          },
        ],
      },
      key: expect.any(String),
    },
  ]);
  expect(required(reuseRequests[0]).key).toBeTruthy();
  const reusedRevisionId = text(object(reusedBundle.revision).id);
  expect(reusedRevisionId).not.toBe(sourceRevisionId);
  expect(object(reusedBundle.version).id).not.toBe(sourceVersionId);
  expect(object(reusedBundle.version).version).toBe("1.1.0");
  const readback = await catalog.admin(
    "GET",
    `/api/v1/version-revisions/${reusedRevisionId}`,
  );
  const reusedRepresentation = object(
    required(array(object(readback.data.revision).representations)[0]),
  );
  expect(object(readback.data.evidence).state).toBe("blocked");
  expect(reusedRepresentation).toMatchObject({
    url: archive.url,
    contentDigest: archive.digest,
    size: archive.size,
    validationState: "invalid",
    normalizedFacts: { version: "1.0.0" },
    plugin,
  });
  const unchangedSource = await catalog.admin(
    "GET",
    `/api/v1/version-revisions/${sourceRevisionId}`,
  );
  expect(unchangedSource.data.revision).toEqual(retained.data.revision);
  const unchangedVersion = await catalog.admin(
    "GET",
    `/api/v1/resource-versions/${sourceVersionId}`,
  );
  expect(unchangedVersion.data).toEqual(bundle.version);
  const versions = await catalog.admin(
    "GET",
    `/api/v1/resources/${resourceId}/versions?limit=25`,
  );
  expect(versions.data.nextCursor).toBeUndefined();
  expect(
    array(versions.data.items)
      .map((value) => text(object(value).version))
      .sort(),
  ).toEqual(["1.0.0", "1.1.0"]);
  const listingAfter = await catalog.admin("GET", listingPath);
  expect(listingAfter.data).toEqual(listingBefore.data);
  expect(listingAfter.etag).toBe(listingBefore.etag);
  expect((await catalog.admin("GET", listingRevisionPath)).data).toEqual(
    listingMetadataBefore.data,
  );
  expect((await catalog.admin("GET", listingHistoryPath)).data).toEqual(
    listingHistoryBefore.data,
  );
  expect((await catalog.admin("GET", listingsPath)).data).toEqual(
    listingsBefore.data,
  );
  expect(listingAfter.data.currentReleaseId).toBeUndefined();
  for (const path of [
    `/api/v1/review-submissions?listingId=${listingId}&limit=25`,
    `${listingPath}/releases?limit=25`,
  ]) {
    const history = await catalog.admin("GET", path);
    expect(history.data.nextCursor).toBeUndefined();
    expect(array(history.data.items)).toEqual([]);
  }
  expect(writes).toHaveLength(5);
  expect(artifactReads).toEqual([]);
  expect(outside).toEqual([]);
});
