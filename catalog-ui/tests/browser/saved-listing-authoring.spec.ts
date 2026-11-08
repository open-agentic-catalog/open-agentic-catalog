// Saved-content Listing preparation uses real immutable revisions and command validators.
import type { Catalog } from "./fixtures";
import { test, expect, login, confirmCommand } from "./fixtures";
import { array, object, text } from "./json";
import { agentDocument } from "./published-fixture";
import { openDisclosure } from "./editor-disclosure";
import { fillAgentFields } from "./authoring-fields";
import { required } from "../required";

/** Each owned population is small; reject truncation before comparing retained state. */
async function completePage(catalog: Catalog, path: string) {
  const reply = await catalog.admin("GET", path);
  expect(reply.data.nextCursor).toBeUndefined();
  return array(reply.data.items);
}

test("root: prepare a Listing after saving content alone without repeating the saved content", async ({
  page,
  catalog,
}) => {
  const mutations: {
    path: string;
    method: string;
    body: unknown;
    key: string | undefined;
    etag: string | undefined;
  }[] = [];
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (path.startsWith("/_catalog/api/v1/") && request.method() !== "GET")
      mutations.push({
        path,
        method: request.method(),
        body: request.postDataJSON() as unknown,
        key: request.headers()["idempotency-key"],
        etag: request.headers()["if-match"],
      });
  });
  const identifier =
    "urn:air:catalog.example.com:authoring:content-first-listing";
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
  await page.getByLabel("Version label", { exact: true }).fill("1.0.0");
  await fillAgentFields(page);
  await expect(
    page.getByLabel("Include Listing details", { exact: true }),
  ).not.toBeChecked();
  await page
    .getByRole("button", { name: "Save content draft", exact: true })
    .click();
  expect(mutations).toEqual([]);
  const [resourceResponse, contentResponse] = await Promise.all([
    page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        new URL(response.url()).pathname === "/_catalog/api/v1/resources",
    ),
    page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        /\/_catalog\/api\/v1\/resources\/[^/]+\/versions$/.test(
          new URL(response.url()).pathname,
        ),
    ),
    confirmCommand(page),
  ]);
  expect(resourceResponse.status()).toBe(201);
  expect(contentResponse.status()).toBe(201);
  const resourceId = text(object(await resourceResponse.json()).id);
  const content = object(await contentResponse.json());
  const versionId = text(object(content.version).id);
  const revisionId = text(object(content.revision).id);
  expect(object(content.evidence).state).toBe("complete");
  await expect(
    page.getByRole("heading", { name: "Draft saved", exact: true }),
  ).toBeVisible();
  expect(mutations).toHaveLength(2);
  expect(required(mutations[0])).toMatchObject({
    method: "POST",
    path: "/_catalog/api/v1/resources",
    body: {
      namespaceId: catalog.receipt.namespaceId,
      kind: "agent",
      canonicalIdentifier: identifier,
    },
    etag: undefined,
  });
  expect(required(mutations[1])).toMatchObject({
    method: "POST",
    path: `/_catalog/api/v1/resources/${resourceId}/versions`,
    body: { version: "1.0.0" },
    etag: undefined,
  });
  const versions = [{ id: versionId, revisions: [revisionId] }];
  const before = await contentState(catalog, resourceId, versions);
  expect(before.versions).toHaveLength(1);
  expect(before.histories[0]?.revisions).toHaveLength(1);
  const listingsPath = `/api/v1/resources/${resourceId}/listings?limit=2`;
  expect(await completePage(catalog, listingsPath)).toEqual([]);
  await page
    .getByRole("button", {
      name: "Prepare Listing for saved content",
      exact: true,
    })
    .click();
  await expect(
    page.getByRole("heading", { name: "Save Listing draft", exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("Display name", { exact: true })).toHaveValue(
    "Order status Agent",
  );
  await expect(
    page.getByRole("textbox", { name: "Description", exact: true }),
  ).toHaveValue("Explain an order and its next step.");
  await page
    .getByRole("combobox", { name: "Publisher", exact: true })
    .selectOption(catalog.receipt.publisherId);
  const displayName = "Listing for content saved first";
  await page.getByLabel("Display name", { exact: true }).fill(displayName);
  await page
    .getByRole("button", { name: "Save Listing draft", exact: true })
    .click();
  const dialog = page.getByRole("dialog");
  const facts = dialog.getByLabel("Command summary", { exact: true });
  await expect(facts).toContainText(
    "Existing content; save Listing and ListingRevision",
  );
  for (const value of [identifier, revisionId, catalog.receipt.publisherId])
    await expect(facts).toContainText(value);
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  expect(mutations).toHaveLength(2);
  expect(await completePage(catalog, listingsPath)).toEqual([]);
  await expect(page.getByLabel("Display name", { exact: true })).toHaveValue(
    displayName,
  );
  await page
    .getByRole("button", { name: "Save Listing draft", exact: true })
    .click();
  const [listingResponse, revisionResponse] = await Promise.all([
    page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        new URL(response.url()).pathname ===
          "/_catalog/api/v1/catalog-listings",
    ),
    page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        /\/_catalog\/api\/v1\/catalog-listings\/[^/]+\/revisions$/.test(
          new URL(response.url()).pathname,
        ),
    ),
    confirmCommand(page),
  ]);
  expect(listingResponse.status()).toBe(201);
  expect(revisionResponse.status()).toBe(201);
  const listingId = text(object(await listingResponse.json()).id);
  await expect(
    page.getByRole("heading", { name: "Listing draft saved", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Open saved Listing", exact: true }),
  ).toHaveAttribute("href", `/listings/${listingId}`);
  expect(mutations).toHaveLength(4);
  expect(required(mutations[2])).toMatchObject({
    method: "POST",
    path: "/_catalog/api/v1/catalog-listings",
    body: { resourceId, namespaceId: catalog.receipt.namespaceId },
    etag: undefined,
  });
  expect(required(mutations[3])).toMatchObject({
    method: "POST",
    path: `/_catalog/api/v1/catalog-listings/${listingId}/revisions`,
    body: {
      versionRevisionId: revisionId,
      publisherId: catalog.receipt.publisherId,
      displayName,
      description: "Explain an order and its next step.",
      visibility: "private",
    },
    etag: listingResponse.headers()["etag"],
  });
  expect(new Set(mutations.map((mutation) => mutation.key)).size).toBe(4);
  for (const mutation of mutations) expect(mutation.key).toBeTruthy();
  expect(await contentState(catalog, resourceId, versions)).toEqual(before);
  const listings = await completePage(catalog, listingsPath);
  expect(listings).toHaveLength(1);
  expect(object(required(listings[0])).id).toBe(listingId);
  const listing = await catalog.admin(
    "GET",
    `/api/v1/catalog-listings/${listingId}`,
  );
  const listingRevisionId = text(listing.data.candidateRevisionId);
  const saved = await catalog.admin(
    "GET",
    `/api/v1/listing-revisions/${listingRevisionId}`,
  );
  expect(saved.data).toMatchObject({
    listingId,
    versionRevisionId: revisionId,
    publisherId: catalog.receipt.publisherId,
    displayName,
    description: "Explain an order and its next step.",
    visibility: "private",
  });
  const history = await completePage(
    catalog,
    `/api/v1/catalog-listings/${listingId}/revisions?limit=25`,
  );
  expect(history.map((revision) => text(object(revision).id))).toEqual([
    listingRevisionId,
  ]);
  expect(listing.data.currentReleaseId).toBeUndefined();
  expect(
    await completePage(
      catalog,
      `/api/v1/review-submissions?listingId=${listingId}&limit=25`,
    ),
  ).toEqual([]);
  expect(
    await completePage(
      catalog,
      `/api/v1/catalog-listings/${listingId}/releases?limit=25`,
    ),
  ).toEqual([]);
});

/** HTTP Date is an observation, not retained application state. */
async function retainedRecord(catalog: Catalog, path: string) {
  const { data, etag } = await catalog.admin("GET", path);
  return { data, etag };
}

async function contentState(
  catalog: Catalog,
  resourceId: string,
  versions: { id: string; revisions: string[] }[],
) {
  return {
    resource: await retainedRecord(catalog, `/api/v1/resources/${resourceId}`),
    versions: await completePage(
      catalog,
      `/api/v1/resources/${resourceId}/versions?limit=100`,
    ),
    histories: await Promise.all(
      versions.map(async (version) => ({
        version: await retainedRecord(
          catalog,
          `/api/v1/resource-versions/${version.id}`,
        ),
        revisions: await completePage(
          catalog,
          `/api/v1/resource-versions/${version.id}/revisions?limit=100`,
        ),
        retained: await Promise.all(
          version.revisions.map(async (revision) => {
            const reply = await catalog.admin(
              "GET",
              `/api/v1/version-revisions/${revision}`,
            );
            return reply.data.revision;
          }),
        ),
      })),
    ),
  };
}

for (const base of ["root", "subpath"] as const) {
  test(`${base}: prepare saved-content Listings immediately and after revisiting another Version`, async ({
    page,
    catalog,
  }) => {
    const resource = await catalog.admin("POST", "/api/v1/resources", {
      namespaceId: catalog.receipt.namespaceId,
      kind: "agent",
      canonicalIdentifier: `urn:air:catalog.example.com:authoring:saved-listing-${base}`,
    });
    const resourceId = text(resource.data.id);
    const first = await catalog.admin(
      "POST",
      `/api/v1/resources/${resourceId}/versions`,
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
    const versionId = text(object(first.data.version).id);
    const originalRevision = text(object(first.data.revision).id);
    expect(object(first.data.evidence).state).toBe("complete");
    expect(
      await completePage(
        catalog,
        `/api/v1/resources/${resourceId}/listings?limit=2`,
      ),
    ).toEqual([]);
    const hosts = await completePage(catalog, "/api/v1/publication/hosts");
    const publicationState = async () =>
      Promise.all(
        hosts.map((host) =>
          completePage(
            catalog,
            `/api/v1/publication/snapshots?host=${encodeURIComponent(text(object(host).host))}&limit=100`,
          ),
        ),
      );
    const originalPublication = await publicationState();
    const mutations: {
      method: string;
      path: string;
      body: unknown;
      etag: string | undefined;
      key: string | undefined;
    }[] = [];
    const reads: string[] = [];
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (!path.startsWith(`${catalog.basePath}/_catalog/api/v1/`)) return;
      if (request.method() === "GET") reads.push(path);
      else
        mutations.push({
          method: request.method(),
          path,
          body: request.postDataJSON() as unknown,
          etag: request.headers()["if-match"],
          key: request.headers()["idempotency-key"],
        });
    });
    await login(page, catalog, "local-contributor", `/versions/${versionId}`);
    await page
      .getByRole("button", { name: "New revision", exact: true })
      .click();
    await openDisclosure(page, "Advanced Agent Card JSON");
    const revisedDocument = {
      ...agentDocument,
      name: "Saved content Agent",
      description:
        "Describe the exact saved content before preparing its Listing.",
    };
    await page
      .getByRole("textbox", { name: "Standards document (JSON)", exact: true })
      .fill(JSON.stringify(revisedDocument));
    await page
      .getByRole("button", { name: "Create version revision", exact: true })
      .click();
    expect(mutations).toHaveLength(0);
    const revisionReply = page.waitForResponse(
      (reply) =>
        reply.request().method() === "POST" &&
        new URL(reply.url()).pathname.endsWith(
          `/resource-versions/${versionId}/revisions`,
        ),
    );
    const [revisionResponse] = await Promise.all([
      revisionReply,
      confirmCommand(page),
    ]);
    expect(revisionResponse.status()).toBe(201);
    const savedBundle = object(await revisionResponse.json());
    const savedRevision = text(object(savedBundle.revision).id);
    expect(savedRevision).not.toBe(originalRevision);
    expect(object(savedBundle.evidence).state).toBe("complete");
    expect(mutations).toHaveLength(1);
    expect(required(mutations[0])).toMatchObject({
      method: "POST",
      path: `${catalog.basePath}/_catalog/api/v1/resource-versions/${versionId}/revisions`,
      body: {
        representations: [
          {
            role: "primary",
            mediaType: "application/a2a-agent-card+json",
            data: revisedDocument,
          },
        ],
      },
      etag: first.etag,
    });
    const contentBefore = await contentState(catalog, resourceId, [
      { id: versionId, revisions: [originalRevision, savedRevision] },
    ]);
    expect(contentBefore.histories[0]?.revisions).toHaveLength(2);
    const savedRevisionPath = `${catalog.basePath}/_catalog/api/v1/version-revisions/${savedRevision}`;
    const readsBeforePreparation = reads.filter(
      (path) => path === savedRevisionPath,
    ).length;
    await page
      .getByRole("button", {
        name: "Prepare Listing for this revision",
        exact: true,
      })
      .click();
    await expect(
      page.getByRole("heading", {
        name: "Prepare Listing for saved content",
        exact: true,
      }),
    ).toBeVisible();
    await expect(page.getByLabel("Display name", { exact: true })).toHaveValue(
      revisedDocument.name,
    );
    await expect(
      page.getByRole("textbox", { name: "Description", exact: true }),
    ).toHaveValue(revisedDocument.description);
    await page
      .getByRole("combobox", { name: "Publisher", exact: true })
      .selectOption(catalog.receipt.publisherId);
    const displayName = "Prepared saved-content Listing";
    await page.getByLabel("Display name", { exact: true }).fill(displayName);
    await page
      .getByRole("button", { name: "Save Listing draft", exact: true })
      .click();
    const dialog = page.getByRole("dialog");
    await expect(
      dialog.getByLabel("Command summary", { exact: true }),
    ).toContainText(savedRevision);
    await expect(dialog).toContainText("Create for this Resource");
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(mutations).toHaveLength(1);
    expect(
      await completePage(
        catalog,
        `/api/v1/resources/${resourceId}/listings?limit=2`,
      ),
    ).toEqual([]);
    await expect(page.getByLabel("Display name", { exact: true })).toHaveValue(
      displayName,
    );
    await page
      .getByRole("button", { name: "Save Listing draft", exact: true })
      .click();
    const createdReply = page.waitForResponse(
      (reply) =>
        reply.request().method() === "POST" &&
        new URL(reply.url()).pathname ===
          `${catalog.basePath}/_catalog/api/v1/catalog-listings`,
    );
    const listingRevisionReply = page.waitForResponse(
      (reply) =>
        reply.request().method() === "POST" &&
        new URL(reply.url()).pathname.includes("/catalog-listings/") &&
        new URL(reply.url()).pathname.endsWith("/revisions"),
    );
    const [createdResponse, listingRevisionResponse] = await Promise.all([
      createdReply,
      listingRevisionReply,
      confirmCommand(page),
    ]);
    expect(createdResponse.status()).toBe(201);
    const listingId = text(object(await createdResponse.json()).id);
    expect(listingRevisionResponse.status()).toBe(201);
    await expect(
      page.getByRole("link", { name: "Open saved Listing", exact: true }),
    ).toHaveAttribute("href", `${catalog.basePath}/listings/${listingId}`);
    expect(mutations).toHaveLength(3);
    expect(required(mutations[1])).toMatchObject({
      method: "POST",
      path: `${catalog.basePath}/_catalog/api/v1/catalog-listings`,
      body: { resourceId, namespaceId: catalog.receipt.namespaceId },
      etag: undefined,
    });
    expect(required(mutations[2])).toMatchObject({
      method: "POST",
      path: `${catalog.basePath}/_catalog/api/v1/catalog-listings/${listingId}/revisions`,
      body: {
        versionRevisionId: savedRevision,
        publisherId: catalog.receipt.publisherId,
        displayName,
        description: revisedDocument.description,
        visibility: "private",
      },
      etag: createdResponse.headers()["etag"],
    });
    expect(reads.filter((path) => path === savedRevisionPath)).toHaveLength(
      readsBeforePreparation,
    );
    expect(
      await contentState(catalog, resourceId, [
        { id: versionId, revisions: [originalRevision, savedRevision] },
      ]),
    ).toEqual(contentBefore);
    const listingBefore = await retainedRecord(
      catalog,
      `/api/v1/catalog-listings/${listingId}`,
    );
    const firstListingRevision = text(listingBefore.data.candidateRevisionId);
    const firstListingMetadata = await retainedRecord(
      catalog,
      `/api/v1/listing-revisions/${firstListingRevision}`,
    );
    expect(firstListingMetadata.data).toMatchObject({
      listingId,
      versionRevisionId: savedRevision,
      displayName,
      description: revisedDocument.description,
      publisherId: catalog.receipt.publisherId,
      visibility: "private",
    });

    // Revisit a distinct Version of the same Resource; its one Listing still points to Version 1.
    const second = await catalog.admin(
      "POST",
      `/api/v1/resources/${resourceId}/versions`,
      {
        version: "2.0.0",
        representations: [
          {
            role: "primary",
            mediaType: "application/a2a-agent-card+json",
            data: {
              ...agentDocument,
              version: "2.0.0",
              name: "Next content Agent",
            },
          },
        ],
      },
    );
    expect(object(second.data.evidence).state).toBe("complete");
    const secondVersion = text(object(second.data.version).id);
    const secondRevision = text(object(second.data.revision).id);
    expect(secondVersion).not.toBe(versionId);
    expect(secondRevision).not.toBe(savedRevision);
    const versions = [
      { id: versionId, revisions: [originalRevision, savedRevision] },
      { id: secondVersion, revisions: [secondRevision] },
    ];
    const beforeRevisit = await contentState(catalog, resourceId, versions);
    await page.goto(
      `${catalog.origin}${catalog.basePath}/versions/${secondVersion}`,
    );
    await expect(
      page.getByRole("heading", { name: "Version 2.0.0", exact: true }),
    ).toBeVisible();
    const currentListingReply = page.waitForResponse(
      (reply) =>
        reply.request().method() === "GET" &&
        new URL(reply.url()).pathname ===
          `${catalog.basePath}/_catalog/api/v1/catalog-listings/${listingId}`,
    );
    const currentRevisionReply = page.waitForResponse(
      (reply) =>
        reply.request().method() === "GET" &&
        new URL(reply.url()).pathname ===
          `${catalog.basePath}/_catalog/api/v1/version-revisions/${secondRevision}`,
    );
    const [currentListingResponse, currentRevisionResponse] = await Promise.all(
      [
        currentListingReply,
        currentRevisionReply,
        page
          .getByRole("button", {
            name: "Prepare listing metadata",
            exact: true,
          })
          .click(),
      ],
    );
    expect(currentRevisionResponse.status()).toBe(200);
    expect(currentListingResponse.headers()["etag"]).toBe(listingBefore.etag);
    await expect(
      page.getByText(`Update existing Listing ${listingId}.`, { exact: false }),
    ).toBeVisible();
    await expect(page.getByLabel("Display name", { exact: true })).toHaveValue(
      displayName,
    );
    await expect(
      page.getByRole("textbox", { name: "Description", exact: true }),
    ).toHaveValue(revisedDocument.description);
    await expect(
      page.getByRole("combobox", { name: "Publisher", exact: true }),
    ).toHaveValue(catalog.receipt.publisherId);
    await expect(
      page.getByRole("combobox", { name: "Resource version", exact: true }),
    ).toHaveCount(0);
    const updatedName = "Listing for the next saved Version";
    await page.getByLabel("Display name", { exact: true }).fill(updatedName);
    await page
      .getByRole("button", { name: "Save Listing draft", exact: true })
      .click();
    const facts = dialog.getByLabel("Command summary", { exact: true });
    for (const value of [
      resourceId,
      secondRevision,
      listingId,
      catalog.receipt.publisherId,
    ])
      await expect(facts).toContainText(value);
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(mutations).toHaveLength(3);
    expect(
      await retainedRecord(catalog, `/api/v1/catalog-listings/${listingId}`),
    ).toEqual(listingBefore);
    await expect(page.getByLabel("Display name", { exact: true })).toHaveValue(
      updatedName,
    );
    await page
      .getByRole("button", { name: "Save Listing draft", exact: true })
      .click();
    const updatedReply = page.waitForResponse(
      (reply) =>
        reply.request().method() === "POST" &&
        new URL(reply.url()).pathname ===
          `${catalog.basePath}/_catalog/api/v1/catalog-listings/${listingId}/revisions`,
    );
    const [updatedResponse] = await Promise.all([
      updatedReply,
      confirmCommand(page),
    ]);
    expect(updatedResponse.status()).toBe(201);
    await expect(
      page.getByRole("link", { name: "Open saved Listing", exact: true }),
    ).toHaveAttribute("href", `${catalog.basePath}/listings/${listingId}`);
    expect(mutations).toHaveLength(4);
    expect(required(mutations[3])).toMatchObject({
      method: "POST",
      path: `${catalog.basePath}/_catalog/api/v1/catalog-listings/${listingId}/revisions`,
      etag: listingBefore.etag,
      body: {
        versionRevisionId: secondRevision,
        displayName: updatedName,
        description: revisedDocument.description,
        publisherId: catalog.receipt.publisherId,
        visibility: "private",
      },
    });
    expect(new Set(mutations.map((mutation) => mutation.key)).size).toBe(4);
    for (const mutation of mutations) expect(mutation.key).toBeTruthy();
    const listingAfter = await catalog.admin(
      "GET",
      `/api/v1/catalog-listings/${listingId}`,
    );
    expect(listingAfter.etag).not.toBe(listingBefore.etag);
    const secondListingRevision = text(listingAfter.data.candidateRevisionId);
    expect(secondListingRevision).not.toBe(firstListingRevision);
    const savedMetadata = await catalog.admin(
      "GET",
      `/api/v1/listing-revisions/${secondListingRevision}`,
    );
    expect(savedMetadata.data).toMatchObject({
      listingId,
      versionRevisionId: secondRevision,
      displayName: updatedName,
      description: revisedDocument.description,
      publisherId: catalog.receipt.publisherId,
      visibility: "private",
    });
    expect(
      await retainedRecord(
        catalog,
        `/api/v1/listing-revisions/${firstListingRevision}`,
      ),
    ).toEqual(firstListingMetadata);
    const listingHistory = await completePage(
      catalog,
      `/api/v1/catalog-listings/${listingId}/revisions?limit=100`,
    );
    expect(listingHistory.map((revision) => text(object(revision).id))).toEqual(
      [firstListingRevision, secondListingRevision],
    );
    const listings = await completePage(
      catalog,
      `/api/v1/resources/${resourceId}/listings?limit=2`,
    );
    expect(listings).toHaveLength(1);
    expect(object(required(listings[0])).id).toBe(listingId);
    expect(listingAfter.data.currentReleaseId).toBeUndefined();
    expect(
      await completePage(
        catalog,
        `/api/v1/review-submissions?listingId=${listingId}&limit=100`,
      ),
    ).toEqual([]);
    expect(
      await completePage(
        catalog,
        `/api/v1/catalog-listings/${listingId}/releases?limit=100`,
      ),
    ).toEqual([]);
    expect(await publicationState()).toEqual(originalPublication);
    expect(await contentState(catalog, resourceId, versions)).toEqual(
      beforeRevisit,
    );
  });
}
