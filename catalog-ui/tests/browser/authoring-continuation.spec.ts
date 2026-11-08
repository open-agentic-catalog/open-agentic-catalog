// Real authoring commands use one confirmation and exact acknowledged references.
import AxeBuilder from "@axe-core/playwright";
import { test, expect, login, confirmCommand } from "./fixtures";
import { array, object, text } from "./json";
import { agentDocument, publishFixture } from "./published-fixture";
import { openDisclosure } from "./editor-disclosure";
import { required } from "../required";
import {
  fillAgentFields,
  fillHostedMCPFields,
  fillSkillFields,
} from "./authoring-fields";

for (const base of ["root", "subpath"] as const) {
  for (const kind of ["agent", "mcp_server", "skill"] as const) {
    test(`${base}: save guided ${kind} content and Listing in one workspace, then correct the exact draft`, async ({
      page,
      catalog,
    }, testInfo) => {
      const writes: {
        path: string;
        body: Record<string, unknown>;
        key: string | undefined;
      }[] = [];
      page.on("request", (request) => {
        if (
          request.method() === "POST" &&
          request.url().includes("/_catalog/api/v1/")
        )
          writes.push({
            path: new URL(request.url()).pathname,
            body: object(request.postDataJSON()),
            key: request.headers()["idempotency-key"],
          });
      });
      await login(page, catalog, "local-contributor", "/resources/new");
      await page
        .getByRole("combobox", { name: "Namespace", exact: true })
        .selectOption(catalog.receipt.namespaceId);
      await page
        .getByLabel("Canonical resource identifier", { exact: true })
        .fill(`urn:air:catalog.example.com:authoring:listing-${kind}-${base}`);
      await page
        .getByRole("combobox", { name: "Kind", exact: true })
        .selectOption(kind);
      await page
        .getByRole("button", { name: "Next: Content", exact: true })
        .click();
      await page.getByLabel("Version label", { exact: true }).fill("1.0.0");
      const sample = {
        agent: {
          fill: fillAgentFields,
          field: "Agent name",
          name: "Order status Agent",
          descriptionField: "Agent description",
          description: "Explain an order and its next step.",
        },
        mcp_server: {
          fill: fillHostedMCPFields,
          field: "MCP server name",
          name: "com.example/documentation",
          descriptionField: "MCP description",
          description: "Search documentation without changing it.",
        },
        skill: {
          fill: fillSkillFields,
          field: "Skill name",
          name: "review-notes",
          descriptionField: "Skill description",
          description: "Prepare release review notes.",
        },
      }[kind];
      await sample.fill(page);
      if (base === "root") {
        await page.evaluate(() => {
          (document.activeElement as HTMLElement).blur();
          window.scrollTo(0, 0);
        });
        await page.screenshot({
          path: testInfo.outputPath(`tutorial-${kind}-fields.png`),
          fullPage: true,
        });
      }
      await page.getByLabel("Include Listing details", { exact: true }).check();
      const check = await page
        .getByLabel("Include Listing details", { exact: true })
        .boundingBox();
      if (!check) throw new Error("Listing checkbox is not rendered");
      expect(check.width).toBeLessThanOrEqual(20);
      expect(check.height).toBeLessThanOrEqual(20);
      await page
        .getByRole("button", { name: "Next: Listing details", exact: true })
        .click();
      await page
        .getByRole("combobox", { name: "Publisher", exact: true })
        .selectOption(catalog.receipt.publisherId);
      await page
        .getByRole("button", { name: "Back: Content", exact: true })
        .click();
      await expect(page.getByLabel(sample.field, { exact: true })).toHaveValue(
        sample.name,
      );
      await page
        .getByRole("button", { name: "Next: Listing details", exact: true })
        .click();
      await expect(
        page.getByRole("combobox", { name: "Publisher", exact: true }),
      ).toHaveValue(catalog.receipt.publisherId);
      if (base === "root" && kind === "agent") {
        await page
          .getByLabel("Display name", { exact: true })
          .fill("Order status Agent");
        await page
          .getByRole("textbox", { name: "Description", exact: true })
          .fill(sample.description);
        await page.evaluate(() => {
          (document.activeElement as HTMLElement).blur();
          window.scrollTo(0, 0);
        });
        await page.screenshot({
          path: testInfo.outputPath("guided-listing-details.png"),
          fullPage: true,
        });
      }
      expect(writes).toHaveLength(0);
      await page
        .getByRole("button", { name: "Save content draft", exact: true })
        .click();
      await expect(page.getByRole("dialog")).toContainText(
        "save Listing and ListingRevision",
      );
      await confirmCommand(page);
      await expect(
        page.getByRole("heading", { name: "Listing draft saved", exact: true }),
      ).toBeVisible();
      expect(writes).toHaveLength(4);
      if (kind === "agent") {
        expect(
          array(required(writes[1]).body.representations)[0],
        ).toMatchObject({
          data: {
            defaultInputModes: ["application/json"],
            defaultOutputModes: ["text/plain"],
          },
        });
      }
      expect(new Set(writes.map((write) => write.key)).size).toBe(4);
      const listingBody = required(writes.at(-1)).body;
      expect(listingBody).toMatchObject({
        description: sample.description,
        publisherId: catalog.receipt.publisherId,
      });
      const firstRevision = text(listingBody.versionRevisionId);
      const first = await catalog.admin(
        "GET",
        `/api/v1/version-revisions/${firstRevision}`,
      );
      expect(object(first.data.evidence).state).toBe("complete");
      expect(listingBody.displayName).toBe(
        text(
          object(
            object(
              required(array(object(first.data.revision).representations)[0]),
            ).normalizedFacts,
          ).name,
        ),
      );
      if (base === "root" && kind === "agent") {
        await page.setViewportSize({ width: 1440, height: 1200 });
        await page
          .getByRole("region", { name: "Saved draft validation", exact: true })
          .evaluate((node) => {
            window.scrollTo(0, node.getBoundingClientRect().top + scrollY - 80);
          });
        await page.screenshot({
          path: testInfo.outputPath("authoring-saved-draft.png"),
        });
        await page.setViewportSize({ width: 1440, height: 900 });
      }
      await page
        .getByRole("button", { name: "Correct this draft", exact: true })
        .click();
      await expect(
        page.getByLabel("Choose draft JSON file", { exact: true }),
      ).toBeDisabled();
      const correctionField = page.getByRole("textbox", {
        name: sample.descriptionField,
        exact: true,
      });
      await expect(correctionField).toBeEditable();
      await correctionField.fill(
        "A clearer description for the corrected draft.",
      );
      await expect(correctionField).toHaveValue(
        "A clearer description for the corrected draft.",
      );
      await page
        .getByRole("button", { name: "Next: Listing details", exact: true })
        .click();
      await expect(
        page.getByRole("textbox", { name: "Description", exact: true }),
      ).toHaveValue(sample.description);
      await page
        .getByRole("button", { name: "Save corrected draft", exact: true })
        .click();
      const listingId = required(
        required(writes.at(-1)).path.split("/").at(-2),
      );
      await expect(page.getByRole("dialog")).toContainText(listingId);
      const correctionReply = page.waitForResponse(
        (response) =>
          response.request().method() === "POST" &&
          response.url().includes("/resource-versions/"),
      );
      await page
        .getByRole("dialog")
        .getByRole("button", { name: "Confirm", exact: true })
        .click();
      const reply = await correctionReply;
      expect(
        object(reply.request().postDataJSON()).representations,
      ).not.toEqual(object(required(writes[1]).body).representations);
      expect(reply.status(), JSON.stringify(await reply.json())).toBe(201);
      await expect(page.getByRole("dialog")).not.toBeVisible();
      await expect(page.getByRole("status")).toContainText(
        "Draft and Listing saved.",
      );
      expect(writes).toHaveLength(6);
      await expect(
        page.getByLabel("Choose draft JSON file", { exact: true }),
      ).toBeDisabled();
      const corrected = text(required(writes.at(-1)).body.versionRevisionId);
      expect(corrected).not.toBe(firstRevision);
      const correctedContent = await catalog.admin(
        "GET",
        `/api/v1/version-revisions/${corrected}`,
      );
      expect(object(correctedContent.data.evidence).state).toBe("complete");
      expect(
        object(
          object(
            required(
              array(object(correctedContent.data.revision).representations)[0],
            ),
          ).normalizedFacts,
        ).description,
      ).toBe("A clearer description for the corrected draft.");
      const old = await catalog.admin(
        "GET",
        `/api/v1/version-revisions/${firstRevision}`,
      );
      expect(old.data.revision).toEqual(first.data.revision);
      expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
        violations: [],
        incomplete: [],
      });
    });
  }
}

test("root: create a new Version from a sealed source through populated fields", async ({
  page,
  catalog,
}, testInfo) => {
  const nativeDocument = {
    ...agentDocument,
    provider: {
      organization: "Example team",
      url: "https://example.test/provider",
    },
    capabilities: { streaming: true },
  };
  const releaseId = await publishFixture(catalog, 812, "", {
    nativeDocument,
    additionalRepresentations: [
      {
        role: "metadata",
        mediaType: "application/vnd.open-agentic-catalog.agent+json",
        data: { name: "Supplementary metadata", framework: "external" },
      },
    ],
  });
  const release = await catalog.admin(
    "GET",
    `/api/v1/listing-releases/${releaseId}`,
  );
  const seeded = { versionRevisionId: text(release.data.versionRevisionId) };
  const source = await catalog.admin(
    "GET",
    `/api/v1/version-revisions/${seeded.versionRevisionId}`,
  );
  const sourceVersion = text(object(source.data.revision).resourceVersionId);
  const version = await catalog.admin(
    "GET",
    `/api/v1/resource-versions/${sourceVersion}`,
  );
  expect(version.data.sealedRevisionId).toBe(seeded.versionRevisionId);
  const resourceId = text(version.data.resourceId);
  await login(page, catalog, "local-contributor", `/versions/${sourceVersion}`);
  await page
    .getByRole("link", {
      name: "Create new Version from this revision",
      exact: true,
    })
    .click();
  await expect(
    page.getByText(`Source revision: ${seeded.versionRevisionId}.`, {
      exact: true,
    }),
  ).toBeVisible();
  await expect(page.getByLabel("Agent name", { exact: true })).toHaveValue(
    text(agentDocument.name),
  );
  await page.getByLabel("Version label", { exact: true }).fill("release two");
  await page
    .getByRole("textbox", { name: "Agent description", exact: true })
    .fill("A clearer description for the next version.");
  await page.evaluate(() => {
    (document.activeElement as HTMLElement).blur();
    window.scrollTo(0, 0);
  });
  await page.setViewportSize({ width: 1440, height: 1200 });
  await page.screenshot({
    path: testInfo.outputPath("authoring-new-version.png"),
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByLabel("Include Listing details", { exact: true }).check();
  await page
    .getByRole("button", { name: "Next: Listing details", exact: true })
    .click();
  await expect(
    page.getByRole("textbox", { name: "Display name", exact: true }),
  ).toHaveValue("Paged agent 812");
  await expect(
    page.getByRole("combobox", { name: "Publisher", exact: true }),
  ).toHaveValue(catalog.receipt.publisherId);
  await page
    .getByRole("button", { name: "Create version", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toContainText("Native version update");
  const created = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname.endsWith(
        `/resources/${resourceId}/versions`,
      ),
  );
  await confirmCommand(page);
  const response = object(await (await created).json());
  expect(object(response.evidence).state).toBe("complete");
  const persisted = await catalog.admin(
    "GET",
    `/api/v1/version-revisions/${text(object(response.revision).id)}`,
  );
  const retained = object(
    required(array(object(persisted.data.revision).representations)[0]),
  );
  let doc: Record<string, unknown>;
  if (retained.artifactId) {
    const artifact = text(retained.artifactId);
    const bytes = await page.request.get(
      `${catalog.origin}${catalog.basePath}/_catalog/api/v1/artifacts/${artifact}/content`,
    );
    expect(bytes.ok()).toBe(true);
    doc = object(JSON.parse(await bytes.text()));
  } else doc = object(retained.data);
  expect(doc).toEqual({
    ...nativeDocument,
    version: "release two",
    description: "A clearer description for the next version.",
  });
  const supplementary = object(
    required(array(object(persisted.data.revision).representations)[1]),
  );
  const oldSupplementary = object(
    required(array(object(source.data.revision).representations)[1]),
  );
  for (const field of [
    "role",
    "mediaType",
    "digest",
    "normalizedFacts",
    "validationState",
  ])
    expect(supplementary[field]).toEqual(oldSupplementary[field]);
  const unchanged = await catalog.admin(
    "GET",
    `/api/v1/version-revisions/${seeded.versionRevisionId}`,
  );
  expect(unchanged.data.revision).toEqual(source.data.revision);
  const stillReleased = await catalog.admin(
    "GET",
    `/api/v1/catalog/resources/${releaseId}`,
  );
  expect(object(stillReleased.data.version).version).toBe("1.0.0");
  const listings = await catalog.admin(
    "GET",
    `/api/v1/resources/${resourceId}/listings`,
  );
  const listing = object(required(array(listings.data.items)[0]));
  const candidate = await catalog.admin(
    "GET",
    `/api/v1/listing-revisions/${text(listing.candidateRevisionId)}`,
  );
  expect(candidate.data).toMatchObject({
    versionRevisionId: text(object(response.revision).id),
    publisherId: catalog.receipt.publisherId,
    displayName: "Paged agent 812",
    tags: ["paged"],
    categories: ["testing", "tools"],
    representativeQueries: ["Find paged agents", "Browse synthetic agents"],
    license: "Apache-2.0",
    visibility: "public",
  });
});

test("subpath: browse source history and explicitly reuse the selected retained revision", async ({
  page,
  catalog,
}) => {
  const profileId = "agent-skills-69ef37e";
  const firstDocument =
    '---\nname: history-first\ndescription: "The first immutable source document."\n---\n# First\n\nKeep the first source.\n';
  const selectedDocument =
    '---\nname: history-selected\ndescription: "The second immutable revision of the first Version."\n---\n# Selected\n\nRead the retained second revision.\n';
  const otherDocument =
    '---\nname: history-other\ndescription: "A distinct source from another Version."\n---\n# Other\n\nRead the other Version.\n';
  const resource = await catalog.admin("POST", "/api/v1/resources", {
    namespaceId: catalog.receipt.namespaceId,
    kind: "skill",
    canonicalIdentifier: "urn:air:catalog.example.com:authoring:source-history",
  });
  const resourceId = text(resource.data.id);
  const representation = (document: string) => ({
    role: "primary",
    mediaType: "application/agent-skills+md",
    profileId,
    content: document,
  });
  const first = await catalog.admin(
    "POST",
    `/api/v1/resources/${resourceId}/versions`,
    {
      version: "history one",
      representations: [representation(firstDocument)],
    },
  );
  const firstVersion = text(object(first.data.version).id);
  const selected = await catalog.admin(
    "POST",
    `/api/v1/resource-versions/${firstVersion}/revisions`,
    { representations: [representation(selectedDocument)] },
    first.etag,
  );
  const other = await catalog.admin(
    "POST",
    `/api/v1/resources/${resourceId}/versions`,
    {
      version: "history two",
      representations: [representation(otherDocument)],
    },
  );
  for (const bundle of [first, selected, other])
    expect(object(bundle.data.evidence).state).toBe("complete");
  const selectedRevision = text(object(selected.data.revision).id);
  const otherVersion = text(object(other.data.version).id);
  const otherRevision = text(object(other.data.revision).id);
  const artifactId = text(
    object(required(array(object(selected.data.revision).representations)[0]))
      .artifactId,
  );
  const sourceReads: string[] = [];
  const parentReads: string[] = [];
  const artifactReads: string[] = [];
  const writes: { path: string; body: unknown; key: string | undefined }[] = [];
  const outside: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (
      ["http:", "https:"].includes(url.protocol) &&
      url.origin !== catalog.origin
    )
      outside.push(url.origin);
    if (!url.pathname.includes("/_catalog/api/v1/")) return;
    if (request.method() === "GET") {
      if (url.pathname.includes("/version-revisions/"))
        sourceReads.push(url.pathname);
      if (/\/resource-versions\/[^/]+$/.test(url.pathname))
        parentReads.push(url.pathname);
      if (url.pathname.includes("/artifacts/"))
        artifactReads.push(url.pathname);
    } else
      writes.push({
        path: url.pathname,
        body: object(request.postDataJSON()),
        key: request.headers()["idempotency-key"],
      });
  });
  await login(
    page,
    catalog,
    "local-contributor",
    `/versions/new?resource=${resourceId}`,
  );
  await page.getByLabel("Version label", { exact: true }).fill("history three");
  await page
    .getByLabel("Skill name", { exact: true })
    .fill("keep-unsaved-name");
  await page
    .getByLabel("Skill description", { exact: true })
    .fill("Keep my unsaved description.");
  await openDisclosure(page, "Start from an existing revision");
  const history = page.getByRole("region", {
    name: "Start from an existing revision",
    exact: true,
  });
  const [versionsResponse] = await Promise.all([
    page.waitForResponse(
      (response) =>
        response.request().method() === "GET" &&
        new URL(response.url()).pathname.endsWith(
          `/resources/${resourceId}/versions`,
        ),
    ),
    history
      .getByRole("button", { name: "Browse source history", exact: true })
      .click(),
  ]);
  expect(versionsResponse.status()).toBe(200);
  expect(
    Object.fromEntries(new URL(versionsResponse.url()).searchParams),
  ).toEqual({ limit: "25" });
  expect(array(object(await versionsResponse.json()).items)).toHaveLength(2);
  const versions = history.getByRole("combobox", {
    name: "Source Version",
    exact: true,
  });
  await expect(
    versions.getByRole("option", { name: "history one (draft)", exact: true }),
  ).toHaveAttribute("value", firstVersion);
  await expect(
    versions.getByRole("option", { name: "history two (draft)", exact: true }),
  ).toHaveAttribute("value", otherVersion);
  const revisionHistory = async (versionId: string, expectedIds: string[]) => {
    const [response] = await Promise.all([
      page.waitForResponse(
        (response) =>
          response.request().method() === "GET" &&
          new URL(response.url()).pathname.endsWith(
            `/resource-versions/${versionId}/revisions`,
          ),
      ),
      versions.selectOption(versionId),
    ]);
    expect(response.status()).toBe(200);
    expect(Object.fromEntries(new URL(response.url()).searchParams)).toEqual({
      limit: "25",
    });
    expect(
      array(object(await response.json()).items)
        .map((value) => text(object(value).id))
        .sort(),
    ).toEqual([...expectedIds].sort());
  };
  await revisionHistory(otherVersion, [otherRevision]);
  await history
    .getByRole("combobox", { name: "Source Revision", exact: true })
    .selectOption(otherRevision);
  await expect(page.getByLabel("Skill name", { exact: true })).toHaveValue(
    "keep-unsaved-name",
  );
  expect(sourceReads).toEqual([]);
  await revisionHistory(firstVersion, [
    text(object(first.data.revision).id),
    selectedRevision,
  ]);
  const revisions = history.getByRole("combobox", {
    name: "Source Revision",
    exact: true,
  });
  await expect(revisions).toHaveValue("");
  await expect(
    revisions.getByRole("option", {
      name: "Revision 1 by local-admin",
      exact: true,
    }),
  ).toHaveAttribute("value", text(object(first.data.revision).id));
  await expect(
    revisions.getByRole("option", {
      name: "Revision 2 by local-admin",
      exact: true,
    }),
  ).toHaveAttribute("value", selectedRevision);
  await revisions.selectOption(selectedRevision);
  await history
    .getByLabel(
      "Use retained document bytes instead of HTTPS references, where available",
      { exact: true },
    )
    .check();
  expect(sourceReads).toEqual([]);
  expect(artifactReads).toEqual([]);
  expect(writes).toEqual([]);
  const replacementMessage =
    "Replace the current content and staged files with this exact revision?";
  page.once("dialog", async (dialog) => {
    expect(dialog.message()).toBe(replacementMessage);
    await dialog.dismiss();
  });
  await history
    .getByRole("button", { name: "Load source revision", exact: true })
    .click();
  await expect(page.getByLabel("Skill name", { exact: true })).toHaveValue(
    "keep-unsaved-name",
  );
  await expect(
    page.getByLabel("Skill description", { exact: true }),
  ).toHaveValue("Keep my unsaved description.");
  expect(sourceReads).toEqual([]);
  expect(artifactReads).toEqual([]);
  expect(writes).toEqual([]);
  page.once("dialog", async (dialog) => {
    expect(dialog.message()).toBe(replacementMessage);
    await dialog.accept();
  });
  await history
    .getByRole("button", { name: "Load source revision", exact: true })
    .click();
  await expect(
    page.getByLabel("Selected source", { exact: true }),
  ).toContainText(selectedRevision);
  await expect(page.getByLabel("Skill name", { exact: true })).toHaveValue(
    "history-selected",
  );
  await expect(
    page.getByLabel("Skill description", { exact: true }),
  ).toHaveValue("The second immutable revision of the first Version.");
  await expect(page.getByLabel("Version label", { exact: true })).toHaveValue(
    "history three",
  );
  expect(sourceReads).toEqual([
    `${catalog.basePath}/_catalog/api/v1/version-revisions/${selectedRevision}`,
  ]);
  expect(artifactReads).toEqual([
    `${catalog.basePath}/_catalog/api/v1/artifacts/${artifactId}/content`,
  ]);
  expect(parentReads).toEqual([
    `${catalog.basePath}/_catalog/api/v1/resource-versions/${firstVersion}`,
  ]);
  expect(writes).toEqual([]);
  await page
    .getByRole("button", { name: "Create version", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toContainText(selectedRevision);
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Cancel", exact: true })
    .click();
  await expect(page.getByLabel("Skill name", { exact: true })).toHaveValue(
    "history-selected",
  );
  expect(writes).toEqual([]);
  await page
    .getByRole("button", { name: "Create version", exact: true })
    .click();
  const [response] = await Promise.all([
    page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        new URL(response.url()).pathname.endsWith(
          `/resources/${resourceId}/versions`,
        ),
    ),
    confirmCommand(page),
  ]);
  expect(response.status()).toBe(201);
  const saved = object(await response.json());
  expect(object(saved.evidence).state).toBe("complete");
  expect(writes).toEqual([
    {
      path: `${catalog.basePath}/_catalog/api/v1/resources/${resourceId}/versions`,
      body: {
        version: "history three",
        representations: [
          {
            role: "primary",
            mediaType: "application/agent-skills+md",
            profileId,
            content: selectedDocument,
          },
        ],
      },
      key: expect.any(String),
    },
  ]);
  expect(required(writes[0]).key).toBeTruthy();
  const savedRevision = await catalog.admin(
    "GET",
    `/api/v1/version-revisions/${text(object(saved.revision).id)}`,
  );
  const savedRepresentation = object(
    required(array(object(savedRevision.data.revision).representations)[0]),
  );
  expect(savedRepresentation).toMatchObject({
    artifactId,
    size: Buffer.byteLength(selectedDocument),
    validationState: "valid",
  });
  const retainedContent = await page.request.get(
    `${catalog.origin}${catalog.basePath}/_catalog/api/v1/artifacts/${artifactId}/content`,
    { headers: { "X-Catalog-Request": "1" } },
  );
  expect(retainedContent.status()).toBe(200);
  expect(await retainedContent.text()).toBe(selectedDocument);
  for (const bundle of [first, selected, other]) {
    const unchanged = await catalog.admin(
      "GET",
      `/api/v1/version-revisions/${text(object(bundle.data.revision).id)}`,
    );
    expect(unchanged.data.revision).toEqual(bundle.data.revision);
  }
  const unchangedVersion = await catalog.admin(
    "GET",
    `/api/v1/resource-versions/${firstVersion}`,
  );
  expect(unchangedVersion.data).toEqual(selected.data.version);
  expect(unchangedVersion.etag).toBe(selected.etag);
  const unchangedOtherVersion = await catalog.admin(
    "GET",
    `/api/v1/resource-versions/${otherVersion}`,
  );
  expect(unchangedOtherVersion.data).toEqual(other.data.version);
  expect(unchangedOtherVersion.etag).toBe(other.etag);
  for (const source of [
    {
      versionId: firstVersion,
      revisions: [text(object(first.data.revision).id), selectedRevision],
    },
    { versionId: otherVersion, revisions: [otherRevision] },
  ]) {
    const history = await catalog.admin(
      "GET",
      `/api/v1/resource-versions/${source.versionId}/revisions?limit=25`,
    );
    expect(history.data.nextCursor).toBeUndefined();
    expect(
      array(history.data.items)
        .map((revision) => text(object(revision).id))
        .sort(),
    ).toEqual([...source.revisions].sort());
  }
  const finalHistory = await catalog.admin(
    "GET",
    `/api/v1/resources/${resourceId}/versions?limit=25`,
  );
  expect(finalHistory.data.nextCursor).toBeUndefined();
  expect(
    array(finalHistory.data.items)
      .map((version) => text(object(version).id))
      .sort(),
  ).toEqual(
    [firstVersion, otherVersion, text(object(saved.version).id)].sort(),
  );
  expect(
    array(finalHistory.data.items)
      .map((value) => text(object(value).version))
      .sort(),
  ).toEqual(["history one", "history three", "history two"]);
  expect(outside).toEqual([]);
});

test("subpath: retain a native-version finding and correct it without replacing the Version", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-contributor", "/resources/new");
  await page
    .getByRole("combobox", { name: "Namespace", exact: true })
    .selectOption(catalog.receipt.namespaceId);
  await page
    .getByLabel("Canonical resource identifier", { exact: true })
    .fill("urn:air:catalog.example.com:authoring:correct-native");
  await page
    .getByRole("button", { name: "Next: Content", exact: true })
    .click();
  await page.getByLabel("Version label", { exact: true }).fill("2.0.0");
  await openDisclosure(page, "Advanced Agent Card JSON");
  await page
    .getByRole("textbox", { name: "Standards document (JSON)", exact: true })
    .fill(JSON.stringify(agentDocument));
  await page
    .getByRole("button", { name: "Save content draft", exact: true })
    .click();
  const first = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      /\/resources\/[^/]+\/versions$/.test(new URL(response.url()).pathname),
  );
  await confirmCommand(page);
  const blocked = object(await (await first).json());
  await expect(
    page.getByRole("heading", {
      name: "Draft saved, corrections needed",
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    page.getByText("NATIVE_VERSION_MISMATCH", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Correct native version", exact: true })
    .click();
  await expect(
    page.getByRole("textbox", {
      name: "Standards document (JSON)",
      exact: true,
    }),
  ).toBeFocused();
  await page
    .getByRole("textbox", { name: "Standards document (JSON)", exact: true })
    .fill(JSON.stringify({ ...agentDocument, version: "2.0.0" }));
  await page
    .getByRole("button", { name: "Save corrected draft", exact: true })
    .click();
  const correction = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().includes("/resource-versions/") &&
      new URL(response.url()).pathname.endsWith("/revisions"),
  );
  await confirmCommand(page);
  const corrected = object(await (await correction).json());
  expect(object(corrected.version).id).toBe(object(blocked.version).id);
  expect(object(corrected.evidence).state).toBe("complete");
  expect(object(corrected.revision).id).not.toBe(object(blocked.revision).id);
});
