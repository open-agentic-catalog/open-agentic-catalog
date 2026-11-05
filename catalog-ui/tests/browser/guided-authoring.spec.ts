// Guided authoring uses the real UI/API and reads retained results independently.
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import AxeBuilder from "@axe-core/playwright";
import { test, expect, login, confirmCommand } from "./fixtures";
import { object, array, text } from "./json";
import { required } from "../required";
import { openDisclosure } from "./editor-disclosure";
import { tabTo } from "./keyboard";
import { doubleText } from "./text-layout";

const journeys = [
  {
    base: "root",
    kind: "agent",
    example: "agent-card",
    mediaType: "application/a2a-agent-card+json",
    sample: "samples/cards/order-agent.json",
  },
  {
    base: "subpath",
    kind: "mcp_server",
    example: "mcp-card",
    mediaType: "application/mcp-server-card+json",
    sample: "samples/cards/documentation-mcp.json",
  },
  {
    base: "root",
    kind: "skill",
    example: "skill-markdown",
    mediaType: "application/agent-skills+md",
    sample: "examples/native-import/skills/review-notes/SKILL.md",
  },
] as const;

for (const journey of journeys) {
  test(`${journey.base}: guided ${journey.kind} draft saves identity and content after one confirmation`, async ({
    page,
    catalog,
  }, testInfo) => {
    const mutations: {
      path: string;
      key: string | undefined;
      body: unknown;
    }[] = [];
    const outsideRequests: string[] = [];
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (
        (url.protocol === "https:" || url.protocol === "http:") &&
        url.origin !== catalog.origin
      )
        outsideRequests.push(url.origin);
      if (
        request.method() === "POST" &&
        url.pathname.includes("/_catalog/api/v1/")
      )
        mutations.push({
          path: url.pathname,
          key: request.headers()["idempotency-key"],
          body: request.postDataJSON(),
        });
    });
    const sample = readFileSync(
      new URL(`../../../${journey.sample}`, import.meta.url),
      "utf8",
    );
    const resourceName = `guided-${journey.kind.replaceAll("_", "-")}-${journey.base}`;
    const identifier = `urn:air:catalog.example.com:guided:${resourceName}`;
    await login(page, catalog, "local-contributor", "/resources/new");
    await page
      .getByRole("combobox", { name: "Namespace", exact: true })
      .selectOption(catalog.receipt.namespaceId);
    await page
      .getByRole("combobox", { name: "Kind", exact: true })
      .selectOption(journey.kind);
    if (journey.kind === "agent") {
      await openDisclosure(page, "Build an AIR identifier");
      await page
        .getByLabel("Publisher domain", { exact: true })
        .fill("catalog.example.com");
      await page
        .getByLabel("AIR namespace segments (optional)", { exact: true })
        .fill("guided");
      await page
        .getByLabel("AIR resource name", { exact: true })
        .fill(resourceName);
      await expect(
        page.getByLabel("AIR identifier preview", { exact: true }),
      ).toHaveText(identifier);
      await tabTo(
        page,
        page.getByRole("button", { name: "Use AIR identifier", exact: true }),
      );
      await page.keyboard.press("Enter");
    } else {
      await page
        .getByLabel("Canonical resource identifier", { exact: true })
        .fill(identifier);
    }
    await expect(
      page.getByLabel("Canonical resource identifier", { exact: true }),
    ).toHaveValue(identifier);
    const next = page.getByRole("button", {
      name: "Next: Content",
      exact: true,
    });
    await expect(next).toBeEnabled();
    if (journey.kind === "agent") {
      await page.evaluate(() => {
        (document.activeElement as HTMLElement).blur();
        window.scrollTo(0, 0);
      });
      await page.screenshot({
        path: testInfo.outputPath("authoring-basics.png"),
        fullPage: true,
      });
    }
    if (journey.kind === "agent") {
      await tabTo(page, next);
      await page.keyboard.press("Enter");
    } else await next.click();
    expect(mutations).toHaveLength(0);
    await expect(
      page.getByLabel("Version label", { exact: true }),
    ).toBeVisible();
    await openDisclosure(page, "Use a local example");
    await page
      .getByRole("combobox", { name: "Native example", exact: true })
      .selectOption(journey.example);
    await page
      .getByRole("button", { name: "Use example", exact: true })
      .click();
    if (journey.kind === "agent")
      await openDisclosure(page, "Advanced Agent Card JSON");
    if (journey.kind === "mcp_server")
      await openDisclosure(page, "Advanced MCP JSON");
    if (journey.kind === "skill")
      await openDisclosure(page, "Advanced SKILL.md");
    const editor = page
      .getByRole("textbox", {
        name:
          journey.kind === "skill"
            ? "Text content"
            : "Standards document (JSON)",
        exact: true,
        includeHidden: true,
      })
      .first();
    await expect(editor).toHaveValue(sample);
    if (journey.kind === "skill")
      await page.getByLabel("Version label", { exact: true }).fill("1.0.0");
    else
      await expect(
        page.getByLabel("Version label", { exact: true }),
      ).toHaveValue("1.0.0");
    let registryContent: string | undefined;
    if (journey.kind === "mcp_server") {
      await page
        .getByRole("button", { name: "Add representation", exact: true })
        .click();
      const registry = page.getByRole("group", {
        name: /content 2$/,
        exact: true,
      });
      await registry.getByText("Use a local example", { exact: true }).click();
      await registry
        .getByRole("combobox", { name: "Native example", exact: true })
        .selectOption("mcp-registry-npm");
      const keepVersion = page.waitForEvent("dialog").then(async (dialog) => {
        expect(dialog.type()).toBe("confirm");
        expect(dialog.message()).toContain("Replace Version label 1.0.0");
        expect(dialog.message()).toContain("1.0.2");
        await dialog.dismiss();
      });
      await registry
        .getByRole("button", { name: "Use example", exact: true })
        .click();
      await keepVersion;
      await registry.getByText("Advanced MCP JSON", { exact: true }).click();
      const registryEditor = registry.getByRole("textbox", {
        name: "Standards document (JSON)",
        exact: true,
      });
      const generated = object(JSON.parse(await registryEditor.inputValue()));
      expect(generated).toMatchObject({
        version: "1.0.2",
        packages: [
          {
            registryType: "npm",
            identifier: "@modelcontextprotocol/server-brave-search",
            version: "1.0.2",
            transport: { type: "stdio" },
          },
        ],
      });
      // Change only the native document version to agree with this Catalog
      // Version. Its independently declared npm package version stays 1.0.2.
      registryContent = JSON.stringify(
        { ...generated, version: "1.0.0" },
        null,
        2,
      );
      await registryEditor.fill(registryContent);
      await expect(
        page.getByLabel("Version label", { exact: true }),
      ).toHaveValue("1.0.0");
      await expect(editor).toHaveValue(sample);
    }
    expect(mutations).toHaveLength(0);
    await page
      .getByRole("button", { name: "Back: Basics", exact: true })
      .click();
    await expect(
      page.getByLabel("Canonical resource identifier", { exact: true }),
    ).toHaveValue(identifier);
    await expect(
      page.getByRole("combobox", { name: "Namespace", exact: true }),
    ).toHaveValue(catalog.receipt.namespaceId);
    await next.click();
    await expect(editor).toHaveValue(sample);
    await expect(page.getByLabel("Version label", { exact: true })).toHaveValue(
      "1.0.0",
    );
    if (registryContent)
      await expect(
        page
          .getByRole("group", { name: /content 2$/, exact: true })
          .getByRole("textbox", {
            name: "Standards document (JSON)",
            exact: true,
          }),
      ).toHaveValue(registryContent);
    let expectedContent = sample;
    if (journey.kind === "skill") {
      await openDisclosure(page, "Import an existing Skill document");
      expectedContent += "\nKeep the staged review note.\n";
      await page.getByLabel("Document file", { exact: true }).setInputFiles({
        name: "SKILL.md",
        mimeType: "text/markdown",
        buffer: Buffer.from(expectedContent),
      });
      await expect(page.getByText("Ready to load:")).toBeVisible();
      await page
        .getByRole("button", { name: "Back: Basics", exact: true })
        .click();
      await next.click();
      await expect(page.getByText("Ready to load:")).toBeVisible();
      await expect(editor).toHaveValue(sample);
      await page
        .getByRole("button", { name: "Save content draft", exact: true })
        .click();
      await expect(page.getByRole("alert")).toContainText(
        "Use or discard the selected document",
      );
      await expect(page.getByRole("dialog")).toHaveCount(0);
      expect(mutations).toHaveLength(0);
      const replacement = page.waitForEvent("dialog").then(async (dialog) => {
        expect(dialog.type()).toBe("confirm");
        expect(dialog.message()).toContain("Replace the current document text");
        await dialog.accept();
      });
      await page
        .getByRole("button", { name: "Use loaded document", exact: true })
        .click();
      await replacement;
      await expect(editor).toHaveValue(expectedContent);
      await expect(editor).toBeFocused();
      await expect(
        page.getByRole("button", { name: "Use loaded document", exact: true }),
      ).toHaveCount(0);
    }
    if (journey.kind === "agent") {
      await page.setViewportSize({ width: 1440, height: 1000 });
      await page
        .getByRole("button", {
          name: "Preview and download document",
          exact: true,
        })
        .click();
      const preview = page.getByRole("region", {
        name: "Current document preview",
      });
      await expect(preview).toBeVisible();
      const controls = page.locator(".representation-controls");
      const fieldsBox = required((await controls.boundingBox()) ?? undefined);
      const previewBox = required((await preview.boundingBox()) ?? undefined);
      expect(previewBox.x).toBeGreaterThanOrEqual(
        fieldsBox.x + fieldsBox.width,
      );
      await page.evaluate(() => {
        if (document.activeElement instanceof HTMLElement)
          document.activeElement.blur();
        window.scrollTo(0, 0);
      });
      await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(0);
      await page.screenshot({
        path: testInfo.outputPath("guided-authoring-wide.png"),
        fullPage: true,
      });
      await page.setViewportSize({ width: 320, height: 844 });
      await doubleText(page);
      const narrowFields = required(
        (await controls.boundingBox()) ?? undefined,
      );
      const narrowPreview = required(
        (await preview.boundingBox()) ?? undefined,
      );
      expect(narrowPreview.y).toBeGreaterThanOrEqual(
        narrowFields.y + narrowFields.height,
      );
      await expect
        .poll(() =>
          page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth,
          ),
        )
        .toBe(true);
      await tabTo(
        page,
        page.getByRole("button", { name: "Save content draft", exact: true }),
      );
      await page.evaluate(() => window.scrollTo(0, 0));
      const accessibility = await new AxeBuilder({ page })
        .withTags(["wcag2a", "wcag2aa", "wcag21aa"])
        .analyze();
      expect(accessibility.violations).toEqual([]);
      expect(accessibility.incomplete).toEqual([]);
    }
    if (journey.kind === "agent") {
      // Match Go's strings.TrimSpace before capturing the command and its acknowledgement.
      await page
        .getByLabel("Version label", { exact: true })
        .fill("\u00851.0.0\u0085");
    }
    expect(mutations).toHaveLength(0);
    expect(outsideRequests).toEqual([]);
    const savedReply = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        /\/api\/v1\/resources\/[^/]+\/versions$/.test(
          new URL(response.url()).pathname,
        ),
    );
    await page
      .getByRole("button", { name: "Save content draft", exact: true })
      .click();
    const confirmation = page.getByRole("dialog");
    await expect(confirmation).toContainText(identifier);
    await expect(confirmation).toContainText("New Resource and new Version");
    expect(mutations).toHaveLength(0);
    await confirmCommand(page);
    const response = await savedReply;
    expect(response.status()).toBe(201);
    const bundle = object(await response.json());
    const version = object(bundle.version);
    const revision = object(bundle.revision);
    const resourceId = text(version.resourceId);
    const versionId = text(version.id);
    const revisionId = text(revision.id);
    expect(object(bundle.evidence).state).toBe("complete");
    expect(mutations).toHaveLength(2);
    const resourceCommand = required(mutations[0]);
    const versionCommand = required(mutations[1]);
    expect(resourceCommand.path).toBe(
      `${catalog.basePath}/_catalog/api/v1/resources`,
    );
    expect(resourceCommand.body).toEqual({
      namespaceId: catalog.receipt.namespaceId,
      canonicalIdentifier: identifier,
      kind: journey.kind,
    });
    expect(versionCommand.path).toBe(
      `${catalog.basePath}/_catalog/api/v1/resources/${resourceId}/versions`,
    );
    expect(resourceCommand.key).toBeTruthy();
    expect(versionCommand.key).toBeTruthy();
    expect(versionCommand.key).not.toBe(resourceCommand.key);
    const versionRequest = object(versionCommand.body);
    expect(versionRequest.version).toBe("1.0.0");
    expect(array(versionRequest.representations)).toHaveLength(
      registryContent ? 2 : 1,
    );
    const submitted = object(
      required(array(versionRequest.representations)[0]),
    );
    expect(submitted.role).toBe("primary");
    expect(submitted.mediaType).toBe(journey.mediaType);
    if (journey.kind === "skill")
      expect(submitted.content).toBe(expectedContent);
    else expect(submitted.data).toEqual(JSON.parse(sample));
    if (registryContent)
      expect(
        object(required(array(versionRequest.representations)[1])),
      ).toEqual({
        role: "metadata",
        mediaType: "application/vnd.mcp.server+json",
        data: JSON.parse(registryContent) as unknown,
      });
    const retainedResource = await catalog.admin(
      "GET",
      `/api/v1/resources/${resourceId}`,
    );
    expect(retainedResource.data).toMatchObject({
      id: resourceId,
      namespaceId: catalog.receipt.namespaceId,
      canonicalIdentifier: identifier,
      kind: journey.kind,
    });
    const retainedVersion = await catalog.admin(
      "GET",
      `/api/v1/resource-versions/${versionId}`,
    );
    expect(retainedVersion.data).toMatchObject({
      id: versionId,
      resourceId,
      version: "1.0.0",
      candidateRevisionId: revisionId,
    });
    const retainedRevision = await catalog.admin(
      "GET",
      `/api/v1/version-revisions/${revisionId}`,
    );
    expect(retainedRevision.data.namespaceId).toBe(catalog.receipt.namespaceId);
    const persisted = object(retainedRevision.data.revision);
    expect(persisted.id).toBe(revisionId);
    expect(persisted.resourceVersionId).toBe(versionId);
    const representations = array(persisted.representations);
    expect(representations).toHaveLength(registryContent ? 2 : 1);
    for (const representation of representations)
      expect(object(representation).validationState).toBe("valid");
    const retainedDocument = object(required(representations[0]));
    expect(retainedDocument.role).toBe("primary");
    expect(retainedDocument.mediaType).toBe(journey.mediaType);
    if (journey.kind === "skill") {
      const artifactId = text(retainedDocument.artifactId);
      const document = await page.request.get(
        `${catalog.origin}${catalog.basePath}/_catalog/api/v1/artifacts/${artifactId}/content`,
        {
          headers: { "X-Catalog-Request": "1" },
        },
      );
      expect(document.status()).toBe(200);
      const bytes = await document.body();
      expect(bytes.toString("utf8")).toBe(expectedContent);
      expect(createHash("sha256").update(bytes).digest("hex")).toBe(artifactId);
    } else expect(retainedDocument.data).toEqual(JSON.parse(sample));
    if (registryContent) {
      const retainedRegistry = object(required(representations[1]));
      expect(retainedRegistry.role).toBe("metadata");
      expect(retainedRegistry.mediaType).toBe(
        "application/vnd.mcp.server+json",
      );
      expect(retainedRegistry.data).toEqual(JSON.parse(registryContent));
      expect(object(retainedRegistry.data).version).toBe("1.0.0");
      expect(
        object(required(array(object(retainedRegistry.data).packages)[0]))
          .version,
      ).toBe("1.0.2");
    }
    await expect(
      page.getByRole("status").filter({ hasText: "Draft saved." }),
    ).toBeVisible();
    const savedLink = page.getByRole("link", {
      name: "Open saved version",
      exact: true,
    });
    await expect(savedLink).toHaveAttribute(
      "href",
      `${catalog.basePath}/versions/${versionId}`,
    );
    await page
      .getByRole("button", { name: "Add another item", exact: true })
      .click();
    await expect(
      page.getByRole("combobox", { name: "Namespace", exact: true }),
    ).toHaveValue(catalog.receipt.namespaceId);
    await expect(
      page.getByLabel("Canonical resource identifier", { exact: true }),
    ).toHaveValue("");
    await expect(savedLink).toHaveAttribute(
      "href",
      `${catalog.basePath}/versions/${versionId}`,
    );
    if (journey.kind === "agent") {
      await openDisclosure(page, "Build an AIR identifier");
      await expect(
        page.getByLabel("Publisher domain", { exact: true }),
      ).toHaveValue("");
      await expect(
        page.getByLabel("AIR resource name", { exact: true }),
      ).toHaveValue("");
    }
    await page
      .getByLabel("Canonical resource identifier", { exact: true })
      .fill(`${identifier}-next`);
    await next.click();
    await expect(page.getByLabel("Version label", { exact: true })).toHaveValue(
      "",
    );
    await expect(editor).toHaveValue("");
    await expect(page.getByText("Ready to load:")).toHaveCount(0);
    expect(mutations).toHaveLength(2);
    expect(outsideRequests).toEqual([]);
  });
}

test("identity-only checkpoint keeps unsaved native content protected until the Version is saved", async ({
  page,
  catalog,
}) => {
  const mutations: {
    path: string;
    key: string | undefined;
    body: unknown;
  }[] = [];
  const outsideRequests: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (
      (url.protocol === "https:" || url.protocol === "http:") &&
      url.origin !== catalog.origin
    )
      outsideRequests.push(url.origin);
    if (
      request.method() === "POST" &&
      url.pathname.includes("/_catalog/api/v1/")
    )
      mutations.push({
        path: url.pathname,
        key: request.headers()["idempotency-key"],
        body: request.postDataJSON(),
      });
  });
  const identifier = "urn:air:catalog.example.com:guided:identity-checkpoint";
  const sample = readFileSync(
    new URL("../../../samples/cards/order-agent.json", import.meta.url),
    "utf8",
  );
  await login(page, catalog, "local-contributor", "/resources/new");
  await page
    .getByRole("combobox", { name: "Namespace", exact: true })
    .selectOption(catalog.receipt.namespaceId);
  await page
    .getByLabel("Canonical resource identifier", { exact: true })
    .fill(identifier);
  await page
    .getByRole("button", { name: "Next: Content", exact: true })
    .click();
  await openDisclosure(page, "Use a local example");
  await page.getByRole("button", { name: "Use example", exact: true }).click();
  await openDisclosure(page, "Advanced Agent Card JSON");
  const editor = page.getByRole("textbox", {
    name: "Standards document (JSON)",
    exact: true,
    includeHidden: true,
  });
  const label = page.getByLabel("Version label", { exact: true });
  await expect(editor).toHaveValue(sample);
  await expect(label).toHaveValue("1.0.0");
  await page.getByRole("button", { name: "Back: Basics", exact: true }).click();
  expect(mutations).toHaveLength(0);
  const resourceReply = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname ===
        `${catalog.basePath}/_catalog/api/v1/resources`,
  );
  await page
    .getByRole("button", { name: "Create resource", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toContainText(
    "Resource identity only",
  );
  expect(mutations).toHaveLength(0);
  await confirmCommand(page);
  const resourceResponse = await resourceReply;
  expect(resourceResponse.status()).toBe(201);
  const resourceId = text(object(await resourceResponse.json()).id);
  expect(mutations).toHaveLength(1);
  expect(required(mutations[0]).body).toEqual({
    namespaceId: catalog.receipt.namespaceId,
    canonicalIdentifier: identifier,
    kind: "agent",
  });
  const editorURL = page.url();
  const cancelledLeave = page.waitForEvent("dialog").then(async (dialog) => {
    expect(dialog.type()).toBe("beforeunload");
    await dialog.dismiss();
  });
  await page
    .getByRole("link", { name: "Open created resource", exact: true })
    .click();
  await cancelledLeave;
  await expect(page).toHaveURL(editorURL);
  await expect(editor).toHaveValue(sample);
  await expect(label).toHaveValue("1.0.0");
  expect(mutations).toHaveLength(1);
  const continueContent = page.getByRole("button", {
    name: "Continue to content",
    exact: true,
  });
  await expect(continueContent).toBeEnabled();
  await continueContent.click();
  await expect(editor).toBeVisible();
  await expect(editor).toHaveValue(sample);
  await expect(label).toHaveValue("1.0.0");
  const savedReply = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname ===
        `${catalog.basePath}/_catalog/api/v1/resources/${resourceId}/versions`,
  );
  await page
    .getByRole("button", { name: "Save content draft", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toContainText(
    "Existing Resource and new Version",
  );
  expect(mutations).toHaveLength(1);
  await confirmCommand(page);
  const savedResponse = await savedReply;
  expect(savedResponse.status()).toBe(201);
  const bundle = object(await savedResponse.json());
  const version = object(bundle.version);
  const versionId = text(version.id);
  const revisionId = text(object(bundle.revision).id);
  expect(version.resourceId).toBe(resourceId);
  expect(object(bundle.evidence).state).toBe("complete");
  expect(mutations.map((item) => item.path)).toEqual([
    `${catalog.basePath}/_catalog/api/v1/resources`,
    `${catalog.basePath}/_catalog/api/v1/resources/${resourceId}/versions`,
  ]);
  expect(required(mutations[0]).key).toBeTruthy();
  expect(required(mutations[1]).key).toBeTruthy();
  expect(required(mutations[1]).key).not.toBe(required(mutations[0]).key);
  const persistedResource = await catalog.admin(
    "GET",
    `/api/v1/resources/${resourceId}`,
  );
  expect(persistedResource.data).toMatchObject({
    id: resourceId,
    canonicalIdentifier: identifier,
    namespaceId: catalog.receipt.namespaceId,
  });
  const persistedRevision = await catalog.admin(
    "GET",
    `/api/v1/version-revisions/${revisionId}`,
  );
  const revision = object(persistedRevision.data.revision);
  expect(revision.resourceVersionId).toBe(versionId);
  expect(object(required(array(revision.representations)[0])).data).toEqual(
    JSON.parse(sample),
  );
  let prompts = 0;
  page.on("dialog", async (dialog) => {
    prompts++;
    await dialog.dismiss();
  });
  await page
    .getByRole("link", { name: "Open saved version", exact: true })
    .click();
  await expect(page).toHaveURL(
    `${catalog.origin}${catalog.basePath}/versions/${versionId}`,
  );
  await expect(
    page.getByRole("heading", { name: "Version 1.0.0", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Revision 1", exact: true }),
  ).toBeVisible();
  expect(prompts).toBe(0);
  expect(mutations).toHaveLength(2);
  expect(outsideRequests).toEqual([]);
});
