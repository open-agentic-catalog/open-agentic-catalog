// Batch 4 uses owned local API prerequisites; all changes under test go through the browser.
import { test, expect, login, confirmCommand } from "./fixtures";
import { doubleText } from "./text-layout";
import { agentDocument } from "./published-fixture";
import { openDisclosure, revealControl } from "./editor-disclosure";

test("current draft reuse stages all representations and compares exact immutable revisions", async ({
  page,
  catalog,
}, testInfo) => {
  const item = catalog.receipt.items.find((i) => i.key === "draft-agent")!;
  const created = await catalog.admin<{
    version: { id: string };
    revision: { id: string };
  }>("POST", `/api/v1/resources/${item.resourceId}/versions`, {
    version: "4.0.0",
    representations: [
      {
        role: "primary",
        mediaType: "application/a2a-agent-card+json",
        data: {
          ...agentDocument,
          version: "4.0.0",
          name: "Batch 4 travel agent",
        },
      },
      {
        role: "documentation",
        mediaType: "text/markdown",
        content: "# Travel agent\n\nRead the metadata before use.\n",
      },
    ],
  });
  await login(
    page,
    catalog,
    "local-contributor",
    `/versions/${created.data.version.id}`,
  );
  await page.getByRole("button", { name: "New revision", exact: true }).click();
  await openDisclosure(page, "Advanced Agent Card JSON");
  const primary = page.getByRole("group", {
    name: /content 1$/,
    exact: true,
  });
  await expect(
    primary.getByRole("textbox", {
      name: "Standards document (JSON)",
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    primary.getByRole("textbox", { name: "Media type", exact: true }),
  ).toHaveCount(0);
  const writes: string[] = [];
  page.on("request", (r) => {
    if (r.method() === "POST" && r.url().includes("/_catalog/"))
      writes.push(r.url());
  });
  await page
    .getByRole("button", { name: "Start from current draft", exact: true })
    .click();
  await expect(page.getByText(/Starting from revision/)).toContainText(
    created.data.revision.id,
  );
  const secondary = page.getByRole("group", {
    name: /content 2$/,
    exact: true,
  });
  await expect(
    secondary.getByRole("textbox", { name: "Text content", exact: true }),
  ).toHaveValue("# Travel agent\n\nRead the metadata before use.\n");
  expect(writes).toHaveLength(0);
  const editor = primary.getByRole("textbox", {
    name: "Standards document (JSON)",
    exact: true,
    includeHidden: true,
  });
  await revealControl(editor);
  const data = JSON.parse(await editor.inputValue()) as Record<string, unknown>;
  await editor.fill(
    JSON.stringify({
      ...data,
      description: "A revised travel planning description.",
    }),
  );
  await page
    .getByRole("button", { name: "Create version revision", exact: true })
    .click();
  await confirmCommand(page);
  await page
    .getByRole("link", { name: "Open saved version", exact: true })
    .click();
  const current = await catalog.admin<{ candidateRevisionId: string }>(
    "GET",
    `/api/v1/resource-versions/${created.data.version.id}`,
  );
  expect(current.data.candidateRevisionId).not.toBe(created.data.revision.id);
  await page
    .getByRole("button", { name: "Choose revisions to compare", exact: true })
    .click();
  await page
    .getByRole("combobox", { name: "Before revision", exact: true })
    .selectOption(created.data.revision.id);
  await page
    .getByRole("combobox", { name: "After revision", exact: true })
    .selectOption(current.data.candidateRevisionId);
  await page
    .getByRole("button", { name: "Compare selected revisions", exact: true })
    .click();
  const compared = page.getByLabel("Compared revisions", { exact: true });
  await expect(compared).toContainText(created.data.revision.id);
  await expect(compared).toContainText(current.data.candidateRevisionId);
  await expect(
    compared.getByRole("heading", {
      name: "Changed: representations",
      exact: true,
    }),
  ).toBeVisible();
  const pair = compared
    .locator(".revision-comparison-grid")
    .first()
    .locator(":scope > div");
  const beforeBox = (await pair.nth(0).boundingBox())!;
  const afterBox = (await pair.nth(1).boundingBox())!;
  expect(beforeBox.y).toBe(afterBox.y);
  expect(beforeBox.x).toBeLessThan(afterBox.x);
  await page.setViewportSize({ width: 390, height: 844 });
  expect((await pair.nth(0).boundingBox())!.y).toBeLessThan(
    (await pair.nth(1).boundingBox())!.y,
  );
  await page.setViewportSize({ width: 1440, height: 900 });
  await page
    .getByRole("button", { name: "Inspect selected documents", exact: true })
    .click();
  await expect(
    page.getByText(/Lossless document comparison is unavailable/),
  ).toBeVisible();
  await page.evaluate(() => {
    if (document.activeElement instanceof HTMLElement)
      document.activeElement.blur();
    window.scrollTo(0, 0);
  });
  await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(0);
  await page.screenshot({
    path: testInfo.outputPath("version-comparison.png"),
    fullPage: true,
  });
  expect(writes).toHaveLength(1);
});

test("artwork keyboard edits preserve asset identity and focus exact local errors without writes", async ({
  page,
  catalog,
}, testInfo) => {
  const item = catalog.receipt.items.find((i) => i.key === "draft-agent")!;
  await login(
    page,
    catalog,
    "local-contributor",
    `/listings/${item.listingId}`,
  );
  await page
    .getByRole("button", { name: "New listing revision", exact: true })
    .click();
  await page.getByText("Icon and screenshots", { exact: true }).click();
  const writes: string[] = [];
  page.on("request", (r) => {
    if (r.method() === "POST" && r.url().includes("/_catalog/"))
      writes.push(r.url());
  });
  await page.getByRole("button", { name: "Add icon", exact: true }).focus();
  await page.keyboard.press("Enter");
  await expect(
    page.getByLabel("Icon HTTPS URL", { exact: true }),
  ).toBeFocused();
  await page
    .getByLabel("Icon HTTPS URL", { exact: true })
    .fill("https://images.example/icon.png");
  await page
    .getByLabel("Icon attribution", { exact: true })
    .fill("Example team");
  for (const [index, alt] of ["A", "B", "C"].entries()) {
    await page
      .getByRole("button", { name: "Add screenshot", exact: true })
      .click();
    await expect(
      page.getByLabel(`Screenshot ${index + 1} HTTPS URL`, { exact: true }),
    ).toBeFocused();
    await page
      .getByLabel(`Screenshot ${index + 1} HTTPS URL`, { exact: true })
      .fill("https://images.example/duplicate.png");
    await page
      .getByLabel(`Screenshot ${index + 1} alternative text`, { exact: true })
      .fill(alt);
    await page
      .getByLabel(`Screenshot ${index + 1} attribution`, { exact: true })
      .fill("Example team");
  }
  await page
    .getByRole("button", { name: "Move screenshot 3 earlier", exact: true })
    .focus();
  await page.keyboard.press("Enter");
  await expect(
    page.getByRole("button", {
      name: "Move screenshot 2 earlier",
      exact: true,
    }),
  ).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(
    page.getByLabel("Screenshot 1 alternative text", { exact: true }),
  ).toHaveValue("C");
  page.once("dialog", (d) => d.dismiss());
  await page
    .getByRole("button", { name: "Remove screenshot 1", exact: true })
    .click();
  await expect(
    page.getByLabel("Screenshot 1 alternative text", { exact: true }),
  ).toHaveValue("C");
  await page.getByText("Icon and screenshots", { exact: true }).click();
  await page
    .getByRole("button", { name: "Create listing revision", exact: true })
    .click();
  await expect(page.getByRole("alert")).toContainText(
    "Artwork alternative text",
  );
  await page.getByRole("alert").getByRole("link").click();
  await expect(
    page.getByLabel("Icon alternative text", { exact: true }),
  ).toBeFocused();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(writes).toHaveLength(0);
  for (const width of [390, 320]) {
    await page.setViewportSize({ width, height: 844 });
    await expect
      .poll(() =>
        page.evaluate(
          () =>
            document.documentElement.scrollWidth <=
            document.documentElement.clientWidth,
        ),
      )
      .toBe(true);
  }
  await page.emulateMedia({ colorScheme: "dark" });
  await doubleText(page);
  await expect
    .poll(() =>
      page.evaluate(
        () =>
          document.documentElement.scrollWidth <=
          document.documentElement.clientWidth,
      ),
    )
    .toBe(true);
  await page
    .getByLabel("Icon alternative text", { exact: true })
    .scrollIntoViewIfNeeded();
  await page.screenshot({
    path: testInfo.outputPath("artwork-errors-narrow.png"),
    fullPage: false,
  });
});

test("subpath lifecycle names exact version, submission and release without assuming discovery", async ({
  page,
  catalog,
}, testInfo) => {
  const item = catalog.receipt.items.find((i) => i.releaseId)!;
  expect(item).toBeDefined();
  await login(
    page,
    catalog,
    "local-contributor",
    `/listings/${item.listingId}`,
  );
  const listing = await catalog.admin<{
    candidateRevisionId: string;
    resourceId: string;
  }>("GET", `/api/v1/catalog-listings/${item.listingId}`);
  const vr = await catalog.admin<{ revision: { resourceVersionId: string } }>(
    "GET",
    `/api/v1/version-revisions/${item.versionRevisionId}`,
  );
  const overview = page.getByRole("region", {
    name: "Lifecycle overview",
    exact: true,
  });
  await expect(overview).toContainText("Validation: not inspected.");
  await overview
    .getByRole("button", { name: "Choose lifecycle context", exact: true })
    .click();
  await overview
    .getByRole("combobox", { name: "Lifecycle Version", exact: true })
    .selectOption(vr.data.revision.resourceVersionId);
  await overview
    .getByRole("combobox", { name: "Lifecycle Listing revision", exact: true })
    .selectOption(listing.data.candidateRevisionId);
  let contextReads = 0;
  page.on("request", (request) => {
    if (
      request.method() === "GET" &&
      new URL(request.url()).pathname.endsWith(
        `/api/v1/resource-versions/${vr.data.revision.resourceVersionId}`,
      )
    )
      contextReads++;
  });
  await overview
    .getByRole("button", { name: "Inspect selected lifecycle", exact: true })
    .click();
  const context = page.getByRole("region", {
    name: "Selected lifecycle context",
    exact: true,
  });
  await expect(context).toContainText(item.versionRevisionId);
  await expect(
    context.getByRole("link", {
      name: `published / ${item.releaseId}`,
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    context.getByRole("region", { name: "Reviewer decisions", exact: true }),
  ).toContainText("Approved");
  await expect(
    context.getByRole("region", { name: "Discovery observation", exact: true }),
  ).toContainText("Not inspected for a host and audience");
  expect(contextReads).toBe(1);
  await overview
    .getByRole("button", { name: "Inspect selected lifecycle", exact: true })
    .click();
  await expect.poll(() => contextReads).toBe(2);
  await expect(
    context.getByRole("link", {
      name: `published / ${item.releaseId}`,
      exact: true,
    }),
  ).toBeVisible();
  await page.evaluate(() => {
    if (document.activeElement instanceof HTMLElement)
      document.activeElement.blur();
    window.scrollTo(0, 0);
  });
  await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(0);
  await page.screenshot({
    path: testInfo.outputPath("listing-lifecycle.png"),
    fullPage: true,
  });
});

test("guided Agent authoring fits the initial viewport and retains imports and advanced controls", async ({
  page,
  catalog,
}, testInfo) => {
  const item = catalog.receipt.items.find((i) => i.key === "draft-agent")!;
  await page.setViewportSize({ width: 1440, height: 1000 });
  await login(
    page,
    catalog,
    "local-contributor",
    `/versions/new?resource=${item.resourceId}`,
  );
  const upload = page.getByLabel("Document file", { exact: true });
  const document = page.getByRole("textbox", {
    name: "Standards document (JSON)",
    exact: true,
    includeHidden: true,
  });
  const help = page.getByRole("region", {
    name: "Native input guidance",
    exact: true,
  });
  await expect(help).toContainText("A2A Agent Card");
  await expect(help.getByRole("link")).toHaveAttribute(
    "href",
    /^https:\/\/fixture\.invalid\/[^/#]+$/,
  );
  const name = page.getByRole("textbox", { name: "Agent name", exact: true });
  await expect(name).toBeVisible();
  await expect(upload).toBeHidden();
  await expect(document).toBeHidden();
  expect((await help.boundingBox())!.y).toBeLessThan(
    (await name.boundingBox())!.y,
  );
  expect((await name.boundingBox())!.y).toBeLessThan(900);
  await expect(
    page.getByRole("textbox", { name: "Media type", exact: true }),
  ).toHaveCount(0);
  await page.screenshot({
    path: testInfo.outputPath("document-first-authoring.png"),
  });
  await revealControl(upload);
  await expect(upload).toBeVisible();
  await revealControl(document);
  await expect(document).toBeVisible();
  await page
    .getByText("Advanced representation settings", { exact: true })
    .click();
  await expect(
    page.getByRole("textbox", { name: "Media type", exact: true }),
  ).toHaveValue("application/a2a-agent-card+json");
});

test("opaque private and tenant releases stay usable without invalid rating requests", async ({
  page,
  catalog,
}) => {
  const releases: { id: string; identifier: string; displayName: string }[] =
    [];
  for (const visibility of ["private", "tenant", "public"] as const) {
    const identifier = `internal-${visibility}-travel-agent`;
    const resource = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/resources",
      {
        namespaceId: catalog.receipt.namespaceId,
        canonicalIdentifier: identifier,
        kind: "agent",
      },
    );
    const version = await catalog.admin<{ revision: { id: string } }>(
      "POST",
      `/api/v1/resources/${resource.data.id}/versions`,
      {
        version: "1.0.0",
        representations: [
          {
            role: "primary",
            mediaType: "application/a2a-agent-card+json",
            data: agentDocument,
          },
        ],
      },
    );
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
        displayName: `Internal ${visibility} travel agent`,
        description: "Locally governed travel planning metadata.",
        visibility,
        representativeQueries: ["Find travel agents", "Browse travel metadata"],
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
      { reason: "Reviewed exact local metadata" },
      submission.etag,
      "local-reviewer",
    );
    if (visibility === "public")
      await expect(
        catalog.admin(
          "POST",
          `/api/v1/catalog-listings/${listing.data.id}:publish`,
          { submissionId: submission.data.id },
          revision.etag,
        ),
      ).rejects.toThrow(/422, PUBLICATION_NOT_READY/);
    else {
      const published = await catalog.admin<{ release: { id: string } }>(
        "POST",
        `/api/v1/catalog-listings/${listing.data.id}:publish`,
        { submissionId: submission.data.id },
        revision.etag,
      );
      releases.push({
        id: published.data.release.id,
        identifier,
        displayName: `Internal ${visibility} travel agent`,
      });
    }
  }
  const ratings: string[] = [];
  page.on("request", (r) => {
    if (new URL(r.url()).pathname.includes("/ratings")) ratings.push(r.url());
  });
  await login(page, catalog, "local-admin", `/catalog/${releases[0]!.id}`);
  for (const release of releases) {
    await page.goto(
      `${catalog.origin}${catalog.basePath}/catalog/${release.id}`,
    );
    await expect(
      page.getByRole("heading", { name: release.displayName, exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("Ratings require a normalized AIR identifier.", {
        exact: false,
      }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", {
        name: "Ratings for this version",
        exact: true,
      }),
    ).toHaveCount(0);
  }
  expect(ratings).toEqual([]);
});
