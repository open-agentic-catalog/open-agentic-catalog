// Exact document comparison crosses the real authenticated artifact boundary on explicit inspection.
import { createHash } from "node:crypto";
import { test, expect, login, signOut } from "./fixtures";
import { array, object, text } from "./json";
import { required } from "../required";
import AxeBuilder from "@axe-core/playwright";
import { doubleText } from "./text-layout";

test("subpath: compare selected retained documents without normalizing their exact text", async ({
  page,
  catalog,
  network,
}) => {
  const profileId = "agent-skills-69ef37e";
  const primary =
    '---\nname: comparison-primary\ndescription: "Read the primary Skill before use."\n---\n# Primary\n\nKeep this document unselected.\n';
  const frontmatter =
    '---\nname: comparison-notes\ndescription: "Compare the retained Skill notes without rewriting whitespace."\n---\n';
  const originals = [
    frontmatter +
      "# Exact retained notes\n\nEndpoint: https://old.example/mcp\n\nRead the Skill before use.\n",
    frontmatter +
      "# Exact retained notes\n\nEndpoint: https://new.example/mcp\n\nRead the Skill before use.  \n\n",
  ];
  const documents = originals.map((content) => {
    const bytes = Buffer.from(content, "utf8");
    return {
      content,
      bytes,
      id: createHash("sha256").update(bytes).digest("hex"),
      header: `sha-256=:${createHash("sha256").update(bytes).digest("base64")}:`,
    };
  });
  expect(required(documents[0]).id).not.toBe(required(documents[1]).id);
  const resource = await catalog.admin("POST", "/api/v1/resources", {
    namespaceId: catalog.receipt.namespaceId,
    kind: "skill",
    canonicalIdentifier:
      "urn:air:catalog.example.com:authoring:exact-comparison",
  });
  const resourceId = text(resource.data.id);
  // A valid primary plus separate retained documentation makes selection observable.
  const representations = (content: string) => [
    {
      role: "primary",
      mediaType: "application/agent-skills+md",
      profileId,
      content: primary,
    },
    {
      role: "documentation",
      mediaType: "application/agent-skills+md",
      profileId,
      content,
    },
  ];
  const before = await catalog.admin(
    "POST",
    `/api/v1/resources/${resourceId}/versions`,
    {
      version: "1.0.0",
      representations: representations(required(documents[0]).content),
    },
  );
  const versionId = text(object(before.data.version).id);
  const after = await catalog.admin(
    "POST",
    `/api/v1/resource-versions/${versionId}/revisions`,
    { representations: representations(required(documents[1]).content) },
    before.etag,
  );
  const bundles = [before, after];
  const revisionIds = bundles.map((bundle) =>
    text(object(bundle.data.revision).id),
  );
  for (let index = 0; index < bundles.length; index++) {
    const bundle = required(bundles[index]);
    const document = required(documents[index]);
    const evidence = object(bundle.data.evidence);
    const findingCodes = array(evidence.findings ?? []).map((finding) =>
      text(object(finding).code),
    );
    expect(
      evidence.state,
      `Native Skill fixture must be complete; findings: ${findingCodes.join(", ")}`,
    ).toBe("complete");
    expect(array(object(bundle.data.revision).representations)).toHaveLength(2);
    expect(
      object(required(array(object(bundle.data.revision).representations)[0])),
    ).toMatchObject({
      role: "primary",
      mediaType: "application/agent-skills+md",
      profileId,
      validationState: "valid",
    });
    expect(
      object(required(array(object(bundle.data.revision).representations)[1])),
    ).toMatchObject({
      role: "documentation",
      mediaType: "application/agent-skills+md",
      profileId,
      artifactId: document.id,
      contentDigest: `sha256:${document.id}`,
      size: document.bytes.byteLength,
      validationState: "valid",
    });
  }
  const historyPath = `/api/v1/resource-versions/${versionId}/revisions?limit=100`;
  const historyBefore = await catalog.admin("GET", historyPath);
  expect(historyBefore.data.nextCursor).toBeUndefined();
  expect(
    array(historyBefore.data.items).map((revision) =>
      text(object(revision).id),
    ),
  ).toEqual(revisionIds);
  const versionBefore = await catalog.admin(
    "GET",
    `/api/v1/resource-versions/${versionId}`,
  );
  const artifactReads: string[] = [];
  const revisionReads: string[] = [];
  const writes: string[] = [];
  const outside: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (
      ["http:", "https:"].includes(url.protocol) &&
      url.origin !== catalog.origin
    )
      outside.push(url.origin);
    if (!url.pathname.startsWith(`${catalog.basePath}/_catalog/api/v1/`))
      return;
    if (request.method() !== "GET")
      writes.push(request.method() + " " + url.pathname);
    else if (url.pathname.includes("/artifacts/"))
      artifactReads.push(url.pathname);
    else if (url.pathname.includes("/version-revisions/"))
      revisionReads.push(url.pathname);
  });
  await login(page, catalog, "local-contributor", `/versions/${versionId}`);
  const comparison = page.getByRole("region", {
    name: "Revision comparison",
    exact: true,
  });
  await comparison
    .getByRole("button", { name: "Choose revisions to compare", exact: true })
    .click();
  await comparison
    .getByRole("combobox", { name: "Before revision", exact: true })
    .selectOption(required(revisionIds[0]));
  await comparison
    .getByRole("combobox", { name: "After revision", exact: true })
    .selectOption(required(revisionIds[1]));
  expect(revisionReads).toEqual([]);
  expect(artifactReads).toEqual([]);
  await comparison
    .getByRole("button", { name: "Compare selected revisions", exact: true })
    .click();
  const compared = page.getByLabel("Compared revisions", { exact: true });
  for (const id of revisionIds) await expect(compared).toContainText(id);
  const selections = page.getByRole("region", {
    name: "Document comparison",
    exact: true,
  });
  for (const [index, side] of ["Before", "After"].entries()) {
    const select = selections.getByRole("combobox", {
      name: `${side} representation`,
      exact: true,
    });
    await expect(
      select.getByRole("option", {
        name: `documentation / application/agent-skills+md / sha256:${required(documents[index]).id}`,
        exact: true,
      }),
    ).toHaveAttribute("value", "1");
    await select.selectOption("1");
  }
  expect(revisionReads).toEqual(
    revisionIds.map(
      (id) => `${catalog.basePath}/_catalog/api/v1/version-revisions/${id}`,
    ),
  );
  expect(artifactReads).toEqual([]);
  await expect(
    selections.getByRole("group", { name: "Text document", exact: true }),
  ).toHaveCount(0);
  expect(writes).toEqual([]);
  await selections
    .getByRole("button", { name: "Inspect selected documents", exact: true })
    .click();
  await expect(selections).toContainText(
    "Exact retained documents differ, including any whitespace or numeric token changes.",
  );
  const changes = selections.getByRole("region", {
    name: "Document changes",
    exact: true,
  });
  const next = changes.getByRole("button", {
    name: "Next change",
    exact: true,
  });
  const previous = changes.getByRole("button", {
    name: "Previous change",
    exact: true,
  });
  await expect(changes.getByRole("status")).toHaveText("3 changed regions.");
  await expect(previous).toBeDisabled();
  await next.focus();
  await page.keyboard.press("Enter");
  const firstChange = changes.getByRole("region", {
    name: "Change 1: Replaced text",
    exact: true,
  });
  await expect(firstChange).toBeFocused();
  await page.keyboard.press("Alt+ArrowDown");
  await expect(
    changes.getByRole("region", { name: "Change 2: Added text", exact: true }),
  ).toBeFocused();
  await page.keyboard.press("Alt+ArrowUp");
  await expect(firstChange).toBeFocused();
  expect(
    await firstChange.evaluate(
      (node) =>
        node.getBoundingClientRect().top -
        document.querySelector(".topbar")!.getBoundingClientRect().bottom,
    ),
  ).toBeGreaterThanOrEqual(0);
  await expect(firstChange.locator("del")).toHaveText("old");
  await expect(firstChange.locator("ins")).toHaveText("new");
  await expect(changes.getByRole("status")).toHaveText("Change 1 of 3.");
  await next.focus();
  await page.keyboard.press("Space");
  await expect(
    changes.getByRole("region", { name: "Change 2: Added text", exact: true }),
  ).toBeFocused();
  await expect(changes.locator("ins").nth(1)).toHaveText("\u00b7\u00b7");
  await next.focus();
  await page.keyboard.press("Enter");
  await expect(
    changes.getByRole("region", { name: "Change 3: Added text", exact: true }),
  ).toBeFocused();
  await expect(next).toBeDisabled();
  await expect(changes.locator("ins").last()).toHaveText("\\n");
  await previous.focus();
  await page.keyboard.press("Enter");
  await expect(
    changes.getByRole("region", { name: "Change 2: Added text", exact: true }),
  ).toBeFocused();
  await previous.focus();
  await page.keyboard.press("Enter");
  await expect(firstChange).toBeFocused();
  await expect(compared).toContainText(required(revisionIds[0]));
  await expect(compared).toContainText(required(revisionIds[1]));
  expect(
    await new AxeBuilder({ page }).include(".document-changes").analyze(),
  ).toMatchObject({ violations: [], incomplete: [] });
  for (const [index, side] of ["Before", "After"].entries()) {
    const panel = selections
      .getByRole("heading", { name: `${side} document`, exact: true })
      .locator("..");
    const original = panel.getByRole("group", {
      name: "Text document",
      exact: true,
    });
    // toHaveText normalizes whitespace; textContent must retain every original byte-decoded character.
    await expect(original).toBeVisible();
    expect(await original.textContent()).toBe(
      required(documents[index]).content,
    );
    await expect(
      panel.getByRole("button", { name: "Format JSON", exact: true }),
    ).toHaveCount(0);
  }
  expect(artifactReads).toEqual(
    documents.map(
      (document) =>
        `${catalog.basePath}/_catalog/api/v1/artifacts/${document.id}/content`,
    ),
  );
  expect(writes).toEqual([]);
  expect(outside).toEqual([]);

  await page.getByLabel("Appearance", { exact: true }).click();
  await page.getByRole("radio", { name: "Dark", exact: true }).check();
  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 390, height: 900 });
  await doubleText(page);
  await next.focus();
  await page.keyboard.press("Enter");
  await expect(
    changes.getByRole("region", { name: "Change 2: Added text", exact: true }),
  ).toBeFocused();
  const bounds = await changes.evaluate((element) => ({
    width: element.clientWidth,
    scroll: element.scrollWidth,
  }));
  expect(bounds.scroll).toBeLessThanOrEqual(bounds.width + 1);
  expect(
    await new AxeBuilder({ page }).include(".document-changes").analyze(),
  ).toMatchObject({ violations: [], incomplete: [] });

  // Separate authenticated reads verify retained bytes and wire digests independently of the UI display.
  for (const document of documents) {
    const response = await page.request.get(
      `${catalog.origin}${catalog.basePath}/_catalog/api/v1/artifacts/${document.id}/content`,
      { headers: { "X-Catalog-Request": "1" } },
    );
    expect(response.status()).toBe(200);
    expect(response.headers()["content-digest"]).toBe(document.header);
    expect(await response.body()).toEqual(document.bytes);
  }
  for (const bundle of bundles) {
    const retained = await catalog.admin(
      "GET",
      `/api/v1/version-revisions/${text(object(bundle.data.revision).id)}`,
    );
    expect(retained.data.revision).toEqual(bundle.data.revision);
  }
  const versionAfter = await catalog.admin(
    "GET",
    `/api/v1/resource-versions/${versionId}`,
  );
  expect(versionAfter.data).toEqual(versionBefore.data);
  expect(versionAfter.etag).toBe(versionBefore.etag);
  expect((await catalog.admin("GET", historyPath)).data).toEqual(
    historyBefore.data,
  );
  const inspectedReads = artifactReads.length;
  await signOut(page);
  await expect(comparison).toHaveCount(0);
  network.expectFailure(
    "GET",
    `/_catalog/api/v1/resource-versions/${versionId}`,
    404,
  );
  await page.locator('[data-login-alias="other-tenant-admin"]').click();
  await expect(page.getByRole("alert")).toContainText(
    "This record is unavailable with your current access.",
  );
  await expect(comparison).toHaveCount(0);
  expect(artifactReads).toHaveLength(inspectedReads);
  expect(writes).toEqual([]);
  expect(outside).toEqual([]);
});

test("root: retained BOM, CRLF and numeric tokens survive comparison and integrity refusal", async ({
  page,
  catalog,
  network,
}) => {
  const primary =
    '---\nname: comparison-bom\ndescription: "Inspect exact retained documentation."\n---\n# Notes\n';
  const contents = [
    "\uFEFF" + primary.replaceAll("\n", "\r\n") + "Exact 1.0\r\n",
    primary + "Exact 1e0\n",
  ];
  const resource = await catalog.admin("POST", "/api/v1/resources", {
    namespaceId: catalog.receipt.namespaceId,
    kind: "skill",
    canonicalIdentifier: "urn:air:catalog.example.com:authoring:bom-comparison",
  });
  const representations = (content: string) => [
    {
      role: "primary",
      mediaType: "application/agent-skills+md",
      profileId: "agent-skills-69ef37e",
      content: primary,
    },
    {
      role: "documentation",
      mediaType: "application/agent-skills+md",
      profileId: "agent-skills-69ef37e",
      content,
    },
  ];
  const before = await catalog.admin(
    "POST",
    `/api/v1/resources/${text(resource.data.id)}/versions`,
    {
      version: "1.0.0",
      representations: representations(required(contents[0])),
    },
  );
  const versionId = text(object(before.data.version).id);
  const after = await catalog.admin(
    "POST",
    `/api/v1/resource-versions/${versionId}/revisions`,
    { representations: representations(required(contents[1])) },
    before.etag,
  );
  const revisions = [before, after].map((bundle) =>
    text(object(bundle.data.revision).id),
  );
  // Native validation must not rewrite retained evidence to make comparison succeed.
  for (const [index, bundle] of [before, after].entries()) {
    const representation = object(
      required(array(object(bundle.data.revision).representations)[1]),
    );
    expect(representation.artifactId).toBe(
      createHash("sha256").update(required(contents[index])).digest("hex"),
    );
  }
  await login(page, catalog, "local-contributor", `/versions/${versionId}`);
  const comparison = page.getByRole("region", {
    name: "Revision comparison",
    exact: true,
  });
  await comparison
    .getByRole("button", { name: "Choose revisions to compare", exact: true })
    .click();
  await comparison
    .getByRole("combobox", { name: "Before revision", exact: true })
    .selectOption(required(revisions[0]));
  await comparison
    .getByRole("combobox", { name: "After revision", exact: true })
    .selectOption(required(revisions[1]));
  await comparison
    .getByRole("button", { name: "Compare selected revisions", exact: true })
    .click();
  const documents = comparison.getByRole("region", {
    name: "Document comparison",
    exact: true,
  });
  for (const side of ["Before", "After"])
    await documents
      .getByRole("combobox", { name: `${side} representation`, exact: true })
      .selectOption("1");
  await documents
    .getByRole("button", { name: "Inspect selected documents", exact: true })
    .click();
  await expect(documents).toContainText("Exact retained documents differ");
  const changes = documents.getByRole("region", {
    name: "Document changes",
    exact: true,
  });
  await expect(changes.locator("del").first()).toHaveText("[BOM]");
  await expect(
    changes.locator("del").filter({ hasText: "\\r" }),
  ).not.toHaveCount(0);
  for (const [index, side] of ["Before", "After"].entries()) {
    const panel = documents
      .getByRole("heading", { name: `${side} document`, exact: true })
      .locator("..");
    expect(
      await panel
        .getByRole("group", { name: "Text document", exact: true })
        .textContent(),
    ).toBe(required(contents[index]));
  }
  const artifact = text(
    object(required(array(object(before.data.revision).representations)[1]))
      .artifactId,
  );
  const url = `${catalog.origin}${catalog.basePath}/_catalog/api/v1/artifacts/${artifact}/content`;
  const exact = await page.request.get(url, {
    headers: { "X-Catalog-Request": "1" },
  });
  expect(exact.status()).toBe(200);
  expect(await exact.body()).toEqual(Buffer.from(required(contents[0])));
  // Corrupt a successful wire reply while preserving its authentic digest header.
  const tampered = Buffer.from("tampered document");
  await page.route(url, (route) =>
    route.fulfill({
      status: 200,
      headers: {
        ...exact.headers(),
        "content-length": String(tampered.byteLength),
      },
      body: tampered,
    }),
  );
  // A new explicit representation selection owns a new read lifetime.
  for (const side of ["Before", "After"])
    await documents
      .getByRole("combobox", { name: `${side} representation`, exact: true })
      .selectOption("0");
  await expect(changes).toHaveCount(0);
  await expect(
    documents.getByRole("group", { name: "Text document", exact: true }),
  ).toHaveCount(0);
  await documents
    .getByRole("button", { name: "Inspect selected documents", exact: true })
    .click();
  await expect(changes.getByRole("status")).toHaveText("No document changes.");
  for (const side of ["Before", "After"])
    await documents
      .getByRole("combobox", { name: `${side} representation`, exact: true })
      .selectOption("1");
  const refusedResponse = page.waitForResponse(url);
  await documents
    .getByRole("button", { name: "Inspect selected documents", exact: true })
    .click();
  const refused = await refusedResponse;
  expect(refused.status()).toBe(200);
  expect(await refused.body()).toEqual(tampered);
  expect(refused.headers()["content-digest"]).toBe(
    exact.headers()["content-digest"],
  );
  expect(createHash("sha256").update(tampered).digest("hex")).not.toBe(
    artifact,
  );
  await expect(documents.getByRole("alert")).toContainText(
    "Document integrity could not be verified.",
  );
  await expect(changes).toHaveCount(0);
  await expect(
    documents.getByText("Exact retained documents are identical.", {
      exact: true,
    }),
  ).toHaveCount(0);
  await expect(
    documents.getByRole("group", { name: "Text document", exact: true }),
  ).toHaveCount(0);
  await page.unroute(url);
  await documents.getByRole("button", { name: "Reload", exact: true }).click();
  await expect(changes).toBeVisible();
  // Changing the selected pair resets navigation and compares identical bytes.
  await comparison
    .getByRole("combobox", { name: "After revision", exact: true })
    .selectOption(required(revisions[0]));
  await comparison
    .getByRole("button", { name: "Compare selected revisions", exact: true })
    .click();
  for (const side of ["Before", "After"])
    await documents
      .getByRole("combobox", { name: `${side} representation`, exact: true })
      .selectOption("1");
  await documents
    .getByRole("button", { name: "Inspect selected documents", exact: true })
    .click();
  await expect(documents).toContainText(
    "Exact retained documents are identical.",
  );
  await expect(changes.getByRole("status")).toHaveText("No document changes.");
  await expect(changes.getByRole("button")).toHaveCount(0);

  const largeContent = primary + "x".repeat(65536);
  const large = await catalog.admin(
    "POST",
    `/api/v1/resource-versions/${versionId}/revisions`,
    { representations: representations(largeContent) },
    after.etag,
  );
  await comparison
    .getByRole("button", {
      name: "Refresh after revision choices",
      exact: true,
    })
    .click();
  await comparison
    .getByRole("combobox", { name: "After revision", exact: true })
    .selectOption(text(object(large.data.revision).id));
  await comparison
    .getByRole("button", { name: "Compare selected revisions", exact: true })
    .click();
  for (const side of ["Before", "After"])
    await documents
      .getByRole("combobox", { name: `${side} representation`, exact: true })
      .selectOption("1");
  await documents
    .getByRole("button", { name: "Inspect selected documents", exact: true })
    .click();
  await expect(documents).toContainText(
    "Detailed change navigation is unavailable",
  );
  await expect(
    documents.getByRole("button", { name: "Next change", exact: true }),
  ).toHaveCount(0);
  const afterPanel = documents
    .getByRole("heading", { name: "After document", exact: true })
    .locator("..");
  expect(
    await afterPanel
      .getByRole("group", { name: "Text document", exact: true })
      .textContent(),
  ).toBe(largeContent);

  // Hold a real authorized artifact reply, then invalidate its browser identity.
  // The replacement tenant must never receive that late document or its diff.
  for (const side of ["Before", "After"])
    await documents
      .getByRole("combobox", { name: `${side} representation`, exact: true })
      .selectOption("0");
  await documents
    .getByRole("button", { name: "Inspect selected documents", exact: true })
    .click();
  await expect(changes.getByRole("status")).toHaveText("No document changes.");
  let captured!: () => void;
  const pending = new Promise<void>((resolve) => {
    captured = resolve;
  });
  let release!: () => void;
  const released = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route(url, async (route) => {
    const response = await route.fetch();
    expect(response.status()).toBe(200);
    captured();
    await released;
    await route.fulfill({ response });
  });
  try {
    const cancelled = page.waitForEvent(
      "requestfailed",
      (request) => request.url() === url,
    );
    for (const side of ["Before", "After"])
      await documents
        .getByRole("combobox", { name: `${side} representation`, exact: true })
        .selectOption("1");
    await documents
      .getByRole("button", { name: "Inspect selected documents", exact: true })
      .click();
    await pending;
    await signOut(page);
    expect((await cancelled).failure()?.errorText).toBe("net::ERR_ABORTED");
    await expect(comparison).toHaveCount(0);
    network.expectFailure(
      "GET",
      `/_catalog/api/v1/resource-versions/${versionId}`,
      404,
    );
    await page.locator('[data-login-alias="other-tenant-admin"]').click();
    await expect(page.getByRole("alert")).toContainText(
      "This record is unavailable with your current access.",
    );
    release();
    await page.unrouteAll({ behavior: "wait" });
    await expect(comparison).toHaveCount(0);
    const denied = await page.request.get(url, {
      headers: { "X-Catalog-Request": "1" },
    });
    expect(denied.status()).toBe(404);
    expect(await denied.text()).not.toContain(required(contents[0]));
  } finally {
    release();
  }
});

test("review comparison keeps submitted cross-Version documents when the Listing candidate moves", async ({
  page,
  catalog,
}) => {
  const resource = await catalog.admin("POST", "/api/v1/resources", {
    namespaceId: catalog.receipt.namespaceId,
    kind: "skill",
    canonicalIdentifier:
      "urn:air:catalog.example.com:authoring:review-comparison",
  });
  const resourceId = text(resource.data.id);
  const versions: { id: string; revisionId: string }[] = [];
  for (const [index, host] of ["old", "new", "later"].entries()) {
    const content = `---\nname: review-comparison\ndescription: "Inspect the submitted connection change."\n---\n# Connection\n\nEndpoint: https://${host}.example/mcp\n`;
    const created = await catalog.admin(
      "POST",
      `/api/v1/resources/${resourceId}/versions`,
      {
        version: `${index + 1}.0.0`,
        representations: [
          {
            role: "primary",
            mediaType: "application/agent-skills+md",
            profileId: "agent-skills-69ef37e",
            content,
          },
        ],
      },
    );
    expect(object(created.data.evidence).state).toBe("complete");
    versions.push({
      id: text(object(created.data.version).id),
      revisionId: text(object(created.data.revision).id),
    });
  }
  expect(new Set(versions.map((version) => version.id)).size).toBe(3);
  const listing = await catalog.admin("POST", "/api/v1/catalog-listings", {
    namespaceId: catalog.receipt.namespaceId,
    resourceId,
  });
  const listingId = text(listing.data.id);
  const input = (index: number) => ({
    versionRevisionId: required(versions[index]).revisionId,
    publisherId: catalog.receipt.publisherId,
    displayName: "Review connection changes",
    description: "Compare the submitted definition with the earlier Version.",
    visibility: "public",
    license: "Apache-2.0",
    representativeQueries: [
      "Find a connection Skill",
      "Review connection metadata",
    ],
  });
  const before = await catalog.admin(
    "POST",
    `/api/v1/catalog-listings/${listingId}/revisions`,
    input(0),
    listing.etag,
  );
  const submitted = await catalog.admin(
    "POST",
    `/api/v1/catalog-listings/${listingId}/revisions`,
    input(1),
    before.etag,
  );
  const submission = await catalog.admin(
    "POST",
    `/api/v1/catalog-listings/${listingId}/submissions`,
    {},
  );
  const submissionId = text(submission.data.id);
  const submittedId = text(object(submitted.data.revision).id);
  expect(submission.data.listingRevisionId).toBe(submittedId);
  const later = await catalog.admin(
    "POST",
    `/api/v1/catalog-listings/${listingId}/revisions`,
    input(2),
    submitted.etag,
  );
  const current = await catalog.admin(
    "GET",
    `/api/v1/catalog-listings/${listingId}`,
  );
  expect(current.data.candidateRevisionId).toBe(object(later.data.revision).id);
  expect(current.data.candidateRevisionId).not.toBe(submittedId);
  const retainedSubmission = await catalog.admin(
    "GET",
    `/api/v1/review-submissions/${submissionId}`,
  );
  const nativeReads: string[] = [],
    artifactReads: string[] = [],
    writes: string[] = [];
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (!path.startsWith("/_catalog/api/v1/")) return;
    if (request.method() !== "GET") writes.push(path);
    if (path.includes("/version-revisions/")) nativeReads.push(path);
    if (path.includes("/artifacts/")) artifactReads.push(path);
  });
  await login(page, catalog, "local-reviewer", `/reviews/${submissionId}`);
  const comparison = page.getByRole("region", {
    name: "Revision comparison",
    exact: true,
  });
  await comparison
    .getByRole("button", { name: "Choose revisions to compare", exact: true })
    .click();
  await comparison
    .getByRole("combobox", { name: "Before revision", exact: true })
    .selectOption(text(object(before.data.revision).id));
  await expect(
    comparison.getByRole("combobox", { name: "After revision", exact: true }),
  ).toHaveCount(0);
  await expect(comparison).toContainText(
    `After is fixed to the submitted revision ${submittedId}`,
  );
  await comparison
    .getByRole("button", { name: "Compare selected revisions", exact: true })
    .click();
  const compared = comparison.getByLabel("Compared revisions", { exact: true });
  await expect(compared).toContainText(submittedId);
  await expect(compared).not.toContainText(
    text(object(later.data.revision).id),
  );
  expect(artifactReads).toEqual([]);
  await comparison
    .getByRole("button", {
      name: "Compare linked Version revisions",
      exact: true,
    })
    .click();
  const linked = comparison.getByRole("region", {
    name: "Linked Version comparison",
    exact: true,
  });
  for (const version of versions.slice(0, 2))
    await expect(linked).toContainText(version.revisionId);
  await expect(linked).not.toContainText(required(versions[2]).revisionId);
  await linked
    .getByRole("button", { name: "Inspect selected documents", exact: true })
    .click();
  const changes = linked.getByRole("region", {
    name: "Document changes",
    exact: true,
  });
  await expect(changes.getByRole("status")).toHaveText("1 changed region.");
  await expect(changes.locator("del")).toHaveText("old");
  await expect(changes.locator("ins")).toHaveText("new");
  expect(nativeReads).toEqual(
    versions
      .slice(0, 2)
      .map(
        (version) => `/_catalog/api/v1/version-revisions/${version.revisionId}`,
      ),
  );
  expect(artifactReads).toHaveLength(2);
  expect(writes).toEqual([]);
  expect(
    (await catalog.admin("GET", `/api/v1/review-submissions/${submissionId}`))
      .data,
  ).toEqual(retainedSubmission.data);
  const after = await catalog.admin(
    "GET",
    `/api/v1/catalog-listings/${listingId}`,
  );
  expect(after.data).toEqual(current.data);
  expect(after.etag).toBe(current.etag);
});
