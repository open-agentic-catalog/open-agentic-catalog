// Real UI/host/API round trips. Files never carry destination or publication authority.
import AxeBuilder from "@axe-core/playwright";
import { test, expect, login, confirmCommand, signOut } from "./fixtures";
import { agentDocument } from "./published-fixture";
import { object, array, text } from "./json";
import { readFile } from "node:fs/promises";
import { required } from "../required";
import { openDisclosure } from "./editor-disclosure";
import { observeAttachments, attachments } from "./blob-observation";
import type { Page } from "@playwright/test";

test.use({ demoArchives: true });
async function downloaded(page: Page) {
  await openDisclosure(page, "Import or download draft JSON");
  await page
    .getByRole("button", { name: "Download draft JSON", exact: true })
    .click();
  const pending = page.waitForEvent("download");
  await page
    .getByRole("link", { name: "Save catalog-draft.json", exact: true })
    .click();
  const file = await pending;
  return object(
    JSON.parse(await readFile(required(await file.path()), "utf8")),
  );
}
for (const kind of ["agent", "mcp_server", "skill", "plugin"] as const) {
  test(`subpath: reusable ${kind} JSON creates a draft and downloads exact saved content`, async ({
    page,
    catalog,
  }, info) => {
    const original =
      kind === "agent"
        ? JSON.stringify(agentDocument)
        : kind === "mcp_server"
          ? JSON.stringify({
              name: "io.example/draft-files",
              description: "Read local test documents.",
              version: "1.0.0",
              remotes: [
                { type: "streamable-http", url: "https://example.test/mcp" },
              ],
            })
          : "---\nname: file-skill\ndescription: Read supplied local documents.\n---\n\n# Instructions\n\nRead the supplied documents.\n";
    const file = {
      format: "open-agentic-catalog-draft-v1",
      kind,
      canonicalIdentifier: `urn:air:catalog.example.com:files:${kind}`,
      version: "1.0.0",
      representations: [
        {
          role: "primary",
          profileId: "",
          mediaType: {
            agent: "application/a2a-agent-card+json",
            mcp_server: "application/vnd.mcp.server+json",
            skill: "application/agent-skills+md",
            plugin: "application/agent-plugins+zip",
          }[kind],
          input:
            kind === "plugin"
              ? {
                  mode: "plugin-archive",
                  url: required(catalog.archive).url,
                  expectedDigest: required(catalog.archive).digest,
                }
              : { mode: kind === "skill" ? "content" : "data", text: original },
        },
      ],
    };
    const writes: string[] = [];
    page.on("request", (request) => {
      if (
        request.method() === "POST" &&
        request.url().includes("/_catalog/api/v1/")
      )
        writes.push(new URL(request.url()).pathname);
    });
    await login(page, catalog, "local-contributor", "/resources/new");
    const picker = page.getByLabel("Choose draft JSON file", { exact: true });
    await picker.setInputFiles({
      name: "draft.json",
      mimeType: "application/json",
      buffer: Buffer.from(JSON.stringify(file)),
    });
    await expect(
      page.getByRole("region", { name: "Draft import preview", exact: true }),
    ).toBeFocused();
    if (kind === "agent")
      await page
        .getByRole("region", { name: "Reusable draft JSON", exact: true })
        .screenshot({
          path: info.outputPath("draft-import-preview.png"),
        });
    if (kind === "agent") {
      expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
        violations: [],
        incomplete: [],
      });
      await page.getByLabel("Appearance", { exact: true }).click();
      await page.getByRole("radio", { name: "Dark", exact: true }).check();
      await page.keyboard.press("Escape");
      await page.setViewportSize({ width: 375, height: 812 });
      expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
        violations: [],
        incomplete: [],
      });
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth),
      ).toBeLessThanOrEqual(375);
    }
    await page
      .getByRole("button", { name: "Use this draft", exact: true })
      .click();
    await expect(
      page.getByText(
        "Draft imported into the editor. Check its fields before saving.",
        { exact: true },
      ),
    ).toBeFocused();
    await page
      .getByRole("combobox", { name: "Namespace", exact: true })
      .selectOption(catalog.receipt.namespaceId);
    expect(writes).toHaveLength(0);
    expect(await downloaded(page)).toEqual(file);
    await page
      .getByRole("button", { name: "Next: Content", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Save content draft", exact: true })
      .click();
    await expect(
      page.getByLabel("Choose draft JSON file", { exact: true }),
    ).toBeDisabled();
    await confirmCommand(page);
    await expect(
      page.getByRole("link", { name: "Open saved version", exact: true }),
    ).toBeVisible();
    expect(writes).toHaveLength(2);
    await expect(
      page.getByLabel("Choose draft JSON file", { exact: true }),
    ).toBeDisabled();
    await page
      .getByRole("link", { name: "Open saved version", exact: true })
      .click();
    await page
      .getByRole("link", {
        name: "Inspect current revision and validation",
        exact: true,
      })
      .click();
    const revision = required(new URL(page.url()).pathname.split("/").at(-1));
    await page
      .getByRole("button", { name: "Prepare draft download", exact: true })
      .click();
    const link = page.getByRole("link", {
      name: "Save catalog-draft.json",
      exact: true,
    });
    await expect(link).toBeFocused();
    const download = page.waitForEvent("download");
    await link.click();
    const bytes = await readFile(
      required(await (await download).path()),
      "utf8",
    );
    const saved = object(JSON.parse(bytes));
    expect(saved).toMatchObject({
      kind,
      canonicalIdentifier: file.canonicalIdentifier,
      version: file.version,
    });
    const row = object(required(array(saved.representations)[0]));
    if (kind === "plugin")
      expect(object(row.input)).toEqual(file.representations[0]!.input);
    else if (kind === "skill") expect(object(row.input).text).toBe(original);
    else
      expect(JSON.parse(text(object(row.input).text))).toEqual(
        JSON.parse(original),
      );
    expect(saved).not.toHaveProperty("listing");
    expect(saved).not.toHaveProperty("tenantId");
    const retained = await catalog.admin(
      "GET",
      `/api/v1/version-revisions/${revision}`,
    );
    expect(object(retained.data.evidence).state).toBe("complete");
    await page.setViewportSize({ width: 375, height: 812 });
    const bounds = await page.evaluate(() => ({
      width: document.documentElement.scrollWidth,
      viewport: innerWidth,
    }));
    expect(bounds.width).toBeLessThanOrEqual(bounds.viewport);
  });
}

test("root: malformed enclosing files preserve fields and imported incomplete input sends no command", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-contributor", "/resources/new");
  const writes: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname.includes("/_catalog/api/v1/")
    )
      writes.push(request.url());
  });
  const identity = page.getByLabel("Canonical resource identifier", {
    exact: true,
  });
  await identity.fill("urn:example:preserve");
  const picker = page.getByLabel("Choose draft JSON file", { exact: true });
  await picker.setInputFiles({
    name: "bad.json",
    mimeType: "application/json",
    buffer: Buffer.from('{"format":'),
  });
  await expect(page.getByRole("alert")).toContainText("file contract");
  await expect(identity).toHaveValue("urn:example:preserve");
  const file = {
    format: "open-agentic-catalog-draft-v1",
    kind: "agent",
    canonicalIdentifier: "urn:example:incomplete",
    version: "",
    representations: [
      {
        role: "primary",
        mediaType: "application/a2a-agent-card+json",
        profileId: "",
        input: { mode: "data", text: '{"n":9007199254740993' },
      },
    ],
  };
  await picker.setInputFiles({
    name: "incomplete.json",
    mimeType: "application/json",
    buffer: Buffer.from(JSON.stringify(file)),
  });
  page.once("dialog", (dialog) => dialog.dismiss());
  await page
    .getByRole("button", { name: "Use this draft", exact: true })
    .click();
  await expect(identity).toHaveValue("urn:example:preserve");
  page.once("dialog", (dialog) => dialog.accept());
  await page
    .getByRole("button", { name: "Use this draft", exact: true })
    .click();
  expect(await downloaded(page)).toEqual(file);
  await page
    .getByRole("combobox", { name: "Namespace", exact: true })
    .selectOption(catalog.receipt.namespaceId);
  await page
    .getByRole("button", { name: "Next: Content", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Save content draft", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(
    page.getByRole("textbox", { name: "Version label", exact: true }),
  ).toBeFocused();
  await page
    .getByRole("textbox", { name: "Version label", exact: true })
    .fill("1.0.0");
  await page
    .getByRole("button", { name: "Save content draft", exact: true })
    .click();
  await expect(
    page.getByRole("link", { name: /Enter valid JSON/ }),
  ).toBeVisible();
  await openDisclosure(page, "Advanced Agent Card JSON");
  const native =
    JSON.stringify(agentDocument).slice(0, -1) +
    ',"unknownNumber":9007199254740993}';
  await page
    .getByRole("textbox", { name: "Standards document (JSON)", exact: true })
    .fill(native);
  expect(
    object(array((await downloaded(page)).representations)[0]).input,
  ).toEqual({ mode: "data", text: native });
  await page
    .getByRole("button", { name: "Save content draft", exact: true })
    .click();
  await expect(
    page.getByRole("link", { name: /Whole numbers must be between/ }),
  ).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(writes).toEqual([]);
});

test("root: new-Version import selects Listing metadata, resets reuse and requires explicit rearming", async ({
  page,
  catalog,
}, info) => {
  const item = required(catalog.receipt.items[0]);
  const currentListing = await catalog.admin(
    "GET",
    `/api/v1/catalog-listings/${item.listingId}`,
  );
  const previousMetadata = await catalog.admin(
    "GET",
    `/api/v1/listing-revisions/${text(currentListing.data.candidateRevisionId)}`,
  );
  expect(array(previousMetadata.data.tags).length).toBeGreaterThan(0);
  const response = await catalog.admin(
    "GET",
    `/api/v1/version-revisions/${item.versionRevisionId}/export`,
  );
  const file = object(response.data);
  // The admin helper decodes the enclosing file only; native documents stay opaque strings.
  const metadata = {
    displayName: "Imported destination name",
    description: "Describe a local draft.",
    visibility: "private",
  };
  file.listing = metadata;
  file.version = "file-copy";
  await login(
    page,
    catalog,
    "local-contributor",
    `/versions/new?resource=${item.resourceId}&sourceRevision=${item.versionRevisionId}`,
  );
  const choose = async () => {
    await page
      .getByLabel("Choose draft JSON file", { exact: true })
      .setInputFiles({
        name: "draft.json",
        mimeType: "application/json",
        buffer: Buffer.from(JSON.stringify(file)),
      });
  };
  await choose();
  await expect(
    page.getByRole("option", {
      name: "Existing target Listing metadata",
      exact: true,
    }),
  ).toHaveCount(1);
  await page
    .getByRole("combobox", { name: "Listing metadata to use", exact: true })
    .selectOption("imported");
  await page.evaluate(() => {
    (document.activeElement as HTMLElement).blur();
  });
  await page
    .getByRole("region", { name: "Reusable draft JSON", exact: true })
    .screenshot({
      path: info.outputPath("draft-import-destination.png"),
    });
  page.once("dialog", (dialog) => dialog.accept());
  await page
    .getByRole("button", { name: "Use this draft", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Next: Listing details", exact: true })
    .click();
  await expect(page.getByLabel("Display name", { exact: true })).toHaveValue(
    metadata.displayName,
  );
  await expect(
    page.getByRole("combobox", { name: "Publisher", exact: true }),
  ).toHaveValue("");
  const imported = object((await downloaded(page)).listing);
  expect(imported.tags).toEqual([]);
  expect(imported.categories).toEqual([]);
  expect(imported.relationships).toEqual([]);
  await page
    .getByRole("combobox", { name: "Publisher", exact: true })
    .selectOption(catalog.receipt.publisherId);
  const request = page.waitForRequest(
    (request) =>
      request.method() === "POST" &&
      new URL(request.url()).pathname.endsWith(
        `/resources/${item.resourceId}/versions`,
      ),
  );
  await page
    .getByRole("button", { name: "Create version", exact: true })
    .click();
  await confirmCommand(page);
  const sent = object((await request).postDataJSON());
  const nativeBefore = object(array(file.representations)[0]).input;
  const native = object(nativeBefore);
  if (native.mode === "data")
    expect(object(array(sent.representations)[0]).data).toEqual(
      JSON.parse(text(native.text)),
    );
  await expect(
    page.getByRole("button", { name: "Create another version", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByLabel("Choose draft JSON file", { exact: true }),
  ).toBeDisabled();
  await page
    .getByRole("button", { name: "Create another version", exact: true })
    .click();
  await expect(
    page.getByLabel("Choose draft JSON file", { exact: true }),
  ).toBeEnabled();
  await choose();
  await page
    .getByRole("combobox", { name: "Listing metadata to use", exact: true })
    .selectOption("existing");
  await page
    .getByRole("button", { name: "Use this draft", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Next: Listing details", exact: true })
    .click();
  await expect(page.getByLabel("Display name", { exact: true })).toHaveValue(
    metadata.displayName,
  );
});

test("root: a Reader downloads exact local Catalog content and its selected Listing metadata", async ({
  page,
  catalog,
}) => {
  await observeAttachments(page);
  const item = required(
    catalog.receipt.items.find(
      (item) => item.key === "order-agent" && item.releaseId,
    ),
  );
  await login(page, catalog, "local-reader", `/catalog/${item.releaseId}`);
  const section = page.getByRole("region", {
    name: "Download saved draft JSON",
    exact: true,
  });
  await section.getByLabel("Include Listing metadata", { exact: true }).check();
  const selected = await section
    .getByLabel("Exact ListingRevision ID", { exact: true })
    .inputValue();
  expect(selected).not.toBe("");
  await section
    .getByRole("button", { name: "Prepare draft download", exact: true })
    .click();
  const link = section.getByRole("link", {
    name: "Save catalog-draft.json",
    exact: true,
  });
  await expect(link).toBeFocused();
  const pending = page.waitForEvent("download");
  await link.click();
  const firstDownload = await pending;
  expect(firstDownload.suggestedFilename()).toBe("catalog-draft.json");
  const expectedBytes = await readFile(required(await firstDownload.path()));
  const file = object(JSON.parse(expectedBytes.toString("utf8")));
  expect(file).toMatchObject({
    format: "open-agentic-catalog-draft-v1",
    listing: expect.any(Object),
  });
  expect(object(file.listing)).not.toHaveProperty("publisherId");
  const revision = await catalog.admin(
    "GET",
    `/api/v1/listing-revisions/${selected}`,
  );
  expect(object(file.listing).displayName).toBe(revision.data.displayName);
  expect(object(file.listing).visibility).toBe(revision.data.visibility);
  expect(file.representations).not.toEqual([]);
  expect(file.kind).toBe("agent");
  expect(
    object(object(required(array(file.representations)[0])).input).mode,
  ).toBe("data");

  const original = await attachments(page);
  const oldUrl = await link.getAttribute("href");
  const prepare = section.getByRole("button", {
    name: "Prepare draft download",
    exact: true,
  });
  const include = section.getByLabel("Include Listing metadata", {
    exact: true,
  });
  let mode: "checksum" | "healthy" | "held" = "checksum";
  let ready = false;
  let settled = () => {};
  const completed = new Promise<void>((resolve) => {
    settled = resolve;
  });
  let release = () => {};
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const requests: (string | null)[] = [];
  const exportPath = `/_catalog/api/v1/version-revisions/${item.versionRevisionId}/export`;
  await page.route(`**${exportPath}*`, async (route) => {
    requests.push(
      new URL(route.request().url()).searchParams.get("listingRevisionId"),
    );
    const behavior = mode;
    const response = await route.fetch();
    if (behavior === "held") {
      ready = true;
      await gate;
    }
    if (behavior === "checksum") {
      const headers = { ...response.headers() };
      delete headers["content-digest"];
      await route.fulfill({ response, headers });
    } else {
      try {
        await route.fulfill({ response });
      } finally {
        if (behavior === "held") settled();
      }
    }
  });
  try {
    await prepare.click();
    await expect(section.getByRole("alert")).toHaveText(
      "Draft checksum verification failed.",
    );
    await expect(section.getByRole("alert")).toBeFocused();
    await expect(link).toHaveCount(0);
    expect((await attachments(page)).revoked).toContain(oldUrl);
    mode = "healthy";
    await prepare.click();
    await expect(link).toBeFocused();
    const recovered = page.waitForEvent("download");
    await link.click();
    const recoveredFile = await recovered;
    expect(recoveredFile.suggestedFilename()).toBe("catalog-draft.json");
    expect(await readFile(required(await recoveredFile.path()))).toEqual(
      expectedBytes,
    );
    const recoveredUrl = await link.getAttribute("href");
    mode = "held";
    const held = page.waitForRequest(
      (request) =>
        new URL(request.url()).pathname === catalog.basePath + exportPath,
    );
    await prepare.click();
    const request = await held;
    await expect.poll(() => ready).toBe(true);
    expect(requests.length).toBe(3);
    const aborted = page.waitForEvent("requestfailed", {
      predicate: (value) => value === request,
    });
    await include.uncheck();
    expect((await aborted).failure()?.errorText).toBe("net::ERR_ABORTED");
    await expect(link).toHaveCount(0);
    release();
    await completed;
    expect(
      (await attachments(page)).created.filter(
        (entry) => entry.type === "application/json",
      ),
    ).toHaveLength(2);
    mode = "healthy";
    await prepare.click();
    await expect(link).toBeFocused();
    const withoutListing = page.waitForEvent("download");
    await link.click();
    const noListing = object(
      JSON.parse(
        await readFile(required(await (await withoutListing).path()), "utf8"),
      ),
    );
    const { listing: _listing, ...expectedWithoutListing } = file;
    expect(noListing).toEqual(expectedWithoutListing);
    expect(requests).toEqual([selected, selected, selected, null]);
    const final = await attachments(page);
    expect(final.document).toBe(original.document);
    expect(final.revoked).toContain(recoveredUrl);
    expect(
      final.created.filter((entry) => entry.type === "application/json"),
    ).toHaveLength(3);
  } finally {
    release();
  }
});

test("root: changing a new Resource's Namespace preserves edited Listing fields through Save", async ({
  page,
  catalog,
}) => {
  const second = await catalog.admin("POST", "/api/v1/namespaces", {
    slug: "draft-move",
    displayName: "Draft move",
  });
  const namespaceId = text(second.data.id);
  const file = {
    format: "open-agentic-catalog-draft-v1",
    kind: "agent",
    canonicalIdentifier: "urn:example:namespace-move",
    version: "1.0.0",
    representations: [
      {
        role: "primary",
        mediaType: "application/a2a-agent-card+json",
        profileId: "",
        input: { mode: "data", text: JSON.stringify(agentDocument) },
      },
    ],
    listing: {
      displayName: "Imported name",
      description: "Imported description",
      visibility: "private",
    },
  };
  await login(page, catalog, "local-contributor", "/resources/new");
  await page
    .getByLabel("Choose draft JSON file", { exact: true })
    .setInputFiles({
      name: "draft.json",
      mimeType: "application/json",
      buffer: Buffer.from(JSON.stringify(file)),
    });
  await page
    .getByRole("button", { name: "Use this draft", exact: true })
    .click();
  await page
    .getByRole("combobox", { name: "Namespace", exact: true })
    .selectOption(catalog.receipt.namespaceId);
  await page
    .getByRole("button", { name: "Next: Content", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Next: Listing details", exact: true })
    .click();
  await page.getByLabel("Display name", { exact: true }).fill("Edited name");
  await page
    .getByRole("textbox", { name: "Description", exact: true })
    .fill("Edited description");
  await page
    .getByRole("combobox", { name: "Publisher", exact: true })
    .selectOption(catalog.receipt.publisherId);
  await page
    .getByRole("button", { name: "Back: Content", exact: true })
    .click();
  await page.getByRole("button", { name: "Back: Basics", exact: true }).click();
  await page
    .getByRole("combobox", { name: "Namespace", exact: true })
    .selectOption(namespaceId);
  await page
    .getByRole("button", { name: "Next: Content", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Next: Listing details", exact: true })
    .click();
  await expect(page.getByLabel("Display name", { exact: true })).toHaveValue(
    "Edited name",
  );
  await expect(
    page.getByRole("textbox", { name: "Description", exact: true }),
  ).toHaveValue("Edited description");
  await expect(
    page.getByRole("combobox", { name: "Publisher", exact: true }),
  ).toHaveValue(catalog.receipt.publisherId);
  const created = page.waitForRequest(
    (request) =>
      request.method() === "POST" &&
      new URL(request.url()).pathname.endsWith("/api/v1/resources"),
  );
  const revision = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      /\/catalog-listings\/[^/]+\/revisions$/.test(
        new URL(response.url()).pathname,
      ),
  );
  await page
    .getByRole("button", { name: "Save content draft", exact: true })
    .click();
  await confirmCommand(page);
  expect(object((await created).postDataJSON()).namespaceId).toBe(namespaceId);
  const saved = object(await (await revision).json());
  const parent = await catalog.admin(
    "GET",
    `/api/v1/catalog-listings/${text(object(saved.listing).id)}`,
  );
  expect(parent.data.namespaceId).toBe(namespaceId);
  const metadata = await catalog.admin(
    "GET",
    `/api/v1/listing-revisions/${text(object(saved.revision).id)}`,
  );
  expect(metadata.data).toMatchObject({
    displayName: "Edited name",
    description: "Edited description",
    publisherId: catalog.receipt.publisherId,
  });
});

test("root: existing numeric metadata requires explicit input before preparing a write", async ({
  page,
  catalog,
}) => {
  const item = required(catalog.receipt.items[0]);
  const listing = await catalog.admin(
    "GET",
    `/api/v1/catalog-listings/${item.listingId}`,
  );
  const revisionId = text(listing.data.candidateRevisionId);
  // Controlled GET bytes qualify browser refusal. The real stored metadata stays unchanged.
  await page.route(
    `**/_catalog/api/v1/listing-revisions/${revisionId}`,
    async (route) => {
      const response = await route.fetch();
      const data = object(await response.json());
      const body = JSON.stringify({
        ...data,
        extensions: { amount: "NUMERIC_TOKEN_SENTINEL" },
      }).replace('"NUMERIC_TOKEN_SENTINEL"', "0.10000000000000000000001");
      await route.fulfill({ response, body });
    },
  );
  const exported = await catalog.admin(
    "GET",
    `/api/v1/version-revisions/${item.versionRevisionId}/export`,
  );
  const file = { ...object(exported.data), version: "numeric-copy" };
  const writes: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      request.url().includes("/_catalog/api/v1/")
    )
      writes.push(request.url());
  });
  await login(
    page,
    catalog,
    "local-contributor",
    `/versions/new?resource=${item.resourceId}`,
  );
  await page
    .getByLabel("Choose draft JSON file", { exact: true })
    .setInputFiles({
      name: "draft.json",
      mimeType: "application/json",
      buffer: Buffer.from(JSON.stringify(file)),
    });
  await page
    .getByRole("combobox", { name: "Listing metadata to use", exact: true })
    .selectOption("existing");
  await page
    .getByRole("button", { name: "Use this draft", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Next: Listing details", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Create version", exact: true })
    .click();
  await expect(page.getByRole("alert")).toContainText("exact export");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(writes).toEqual([]);
  await page
    .getByRole("link", { name: /Stored Listing extensions contain numbers/ })
    .click();
  const extensions = page.getByRole("textbox", {
    name: "Extensions (JSON object)",
    exact: true,
  });
  await expect(extensions).toBeFocused();
  await extensions.fill('{"amount":0.1}');
  await page
    .getByRole("button", { name: "Create version", exact: true })
    .click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  expect(writes).toEqual([]);
});

test("root: draft reads cannot replace a newer file or survive Namespace changes and sign-out", async ({
  page,
  catalog,
}) => {
  const second = await catalog.admin("POST", "/api/v1/namespaces", {
    slug: "file-fences",
    displayName: "File fences",
  });
  await page.addInitScript(() => {
    const original = File.prototype.arrayBuffer;
    File.prototype.arrayBuffer = function () {
      if (!this.name.startsWith("delayed-")) return original.call(this);
      return new Promise<ArrayBuffer>((resolve) => {
        const release = (event: Event) => {
          if ((event as CustomEvent<string>).detail !== this.name) return;
          window.removeEventListener("release-draft-test-read", release);
          void original.call(this).then((bytes) => {
            resolve(bytes);
            window.dispatchEvent(new CustomEvent("draft-test-read-released"));
          });
        };
        window.addEventListener("release-draft-test-read", release);
      });
    };
  });
  await login(page, catalog, "local-contributor", "/resources/new");
  const writes: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname.includes("/_catalog/api/v1/")
    )
      writes.push(request.url());
  });
  const choose = async (name: string, identifier: string) => {
    await page
      .getByLabel("Choose draft JSON file", { exact: true })
      .setInputFiles({
        name,
        mimeType: "application/json",
        buffer: Buffer.from(
          JSON.stringify({
            format: "open-agentic-catalog-draft-v1",
            kind: "agent",
            canonicalIdentifier: identifier,
            version: "1.0.0",
            representations: [
              {
                role: "primary",
                mediaType: "application/a2a-agent-card+json",
                profileId: "",
                input: { mode: "data", text: JSON.stringify(agentDocument) },
              },
            ],
          }),
        ),
      });
  };
  const release = async (name: string) =>
    page.evaluate(
      (name) =>
        new Promise<void>((resolve) => {
          window.addEventListener("draft-test-read-released", () => resolve(), {
            once: true,
          });
          window.dispatchEvent(
            new CustomEvent("release-draft-test-read", { detail: name }),
          );
        }),
      name,
    );
  await choose("delayed-first.json", "urn:example:obsolete-file");
  await expect(
    page.getByText("Read draft JSON...", { exact: true }),
  ).toBeVisible();
  await choose("second.json", "urn:example:current-file");
  await page
    .getByRole("button", { name: "Use this draft", exact: true })
    .click();
  await release("delayed-first.json");
  expect((await downloaded(page)).canonicalIdentifier).toBe(
    "urn:example:current-file",
  );
  await expect(
    page.getByLabel("Canonical resource identifier", { exact: true }),
  ).toHaveValue("urn:example:current-file");
  await expect(
    page.getByRole("region", { name: "Draft import preview", exact: true }),
  ).toHaveCount(0);
  await choose("delayed-namespace.json", "urn:example:wrong-namespace-file");
  await expect(
    page.getByText("Read draft JSON...", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("combobox", { name: "Namespace", exact: true })
    .selectOption(text(second.data.id));
  await release("delayed-namespace.json");
  expect((await downloaded(page)).canonicalIdentifier).toBe(
    "urn:example:current-file",
  );
  await expect(
    page.getByLabel("Canonical resource identifier", { exact: true }),
  ).toHaveValue("urn:example:current-file");
  await expect(
    page.getByRole("region", { name: "Draft import preview", exact: true }),
  ).toHaveCount(0);
  await choose("delayed-session.json", "urn:example:private-file");
  await expect(
    page.getByText("Read draft JSON...", { exact: true }),
  ).toBeVisible();
  page.once("dialog", (dialog) => dialog.accept());
  await signOut(page);
  await expect(
    page.locator('[data-login-alias="local-contributor"]'),
  ).toBeVisible();
  await release("delayed-session.json");
  await page.locator('[data-login-alias="local-contributor"]').click();
  await expect(page.getByLabel("Account", { exact: true })).toBeVisible();
  await expect(
    page.getByLabel("Canonical resource identifier", { exact: true }),
  ).toHaveValue("");
  await expect(
    page.getByRole("region", { name: "Draft import preview", exact: true }),
  ).toHaveCount(0);
  expect(writes).toEqual([]);
});

test("root: an incomplete Listing round-trips without normalization or commands", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-contributor", "/resources/new");
  const writes: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname.includes("/_catalog/api/v1/")
    )
      writes.push(request.url());
  });
  const metadata = {
    displayName: "",
    description: "",
    visibility: "private",
    tags: [" same ", " same "],
    categories: [""],
    representativeQueries: ["", " last "],
    relationships: [{ type: "", targetIdentifier: "", targetVersion: "" }],
    extensions: '{"unfinished":9007199254740993',
  };
  await page
    .getByLabel("Choose draft JSON file", { exact: true })
    .setInputFiles({
      name: "incomplete-listing.json",
      mimeType: "application/json",
      buffer: Buffer.from(
        JSON.stringify({
          format: "open-agentic-catalog-draft-v1",
          kind: "agent",
          canonicalIdentifier: "urn:example:incomplete-listing",
          version: "1.0.0",
          representations: [
            {
              role: "primary",
              mediaType: "application/a2a-agent-card+json",
              profileId: "",
              input: { mode: "data", text: JSON.stringify(agentDocument) },
            },
          ],
          listing: metadata,
        }),
      ),
    });
  await page
    .getByRole("button", { name: "Use this draft", exact: true })
    .click();
  await page
    .getByRole("combobox", { name: "Namespace", exact: true })
    .selectOption(catalog.receipt.namespaceId);
  await page
    .getByRole("button", { name: "Next: Content", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Next: Listing details", exact: true })
    .click();
  await expect(
    page.getByRole("combobox", { name: "Publisher", exact: true }),
  ).toHaveValue("");
  const captured = object((await downloaded(page)).listing);
  expect(captured).toMatchObject(metadata);
  expect(captured).not.toHaveProperty("publisherId");
  await page
    .getByRole("button", { name: "Save content draft", exact: true })
    .click();
  await expect(
    page.getByRole("combobox", { name: "Publisher", exact: true }),
  ).toBeFocused();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(writes).toEqual([]);
});

test("root: Plugin draft downloads keep only active preparation or archive inputs", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-contributor", "/resources/new");
  const writes: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname.includes("/_catalog/api/v1/")
    )
      writes.push(request.url());
  });
  const input = {
    mode: "plugin-preparation",
    manifest: '{"name":"Unfinished preparation","number":9007199254740993',
    mcp: "",
    skills: [],
  };
  const file = {
    format: "open-agentic-catalog-draft-v1",
    kind: "plugin",
    canonicalIdentifier: "urn:example:plugin-preparation",
    version: "1.0.0",
    representations: [
      {
        role: "primary",
        mediaType: "application/agent-plugins+zip",
        profileId: "",
        input,
      },
    ],
  };
  await page
    .getByLabel("Choose draft JSON file", { exact: true })
    .setInputFiles({
      name: "preparation.json",
      mimeType: "application/json",
      buffer: Buffer.from(JSON.stringify(file)),
    });
  await page
    .getByRole("button", { name: "Use this draft", exact: true })
    .click();
  await page
    .getByRole("combobox", { name: "Namespace", exact: true })
    .selectOption(catalog.receipt.namespaceId);
  await page
    .getByRole("button", { name: "Next: Content", exact: true })
    .click();
  const mode = page.getByRole("combobox", {
    name: "Plugin input",
    exact: true,
  });
  await expect(mode).toHaveValue("prepare");
  await mode.selectOption("archive");
  await page
    .getByRole("textbox", { name: "Plugin archive URL", exact: true })
    .fill("https://example.test/inactive-archive.zip");
  const checksum = "sha256:" + "b".repeat(64);
  await page
    .getByRole("textbox", {
      name: "Expected archive checksum (optional)",
      exact: true,
    })
    .fill(checksum);
  await mode.selectOption("prepare");
  const prepared = await downloaded(page);
  expect(object(array(prepared.representations)[0]).input).toEqual(input);
  expect(JSON.stringify(prepared)).not.toContain("inactive-archive");
  await mode.selectOption("archive");
  const archive = await downloaded(page);
  expect(object(array(archive.representations)[0]).input).toEqual({
    mode: "plugin-archive",
    url: "https://example.test/inactive-archive.zip",
    expectedDigest: checksum,
  });
  expect(JSON.stringify(archive)).not.toContain("Unfinished preparation");
  await page
    .getByLabel("Choose draft JSON file", { exact: true })
    .setInputFiles({
      name: "archive.json",
      mimeType: "application/json",
      buffer: Buffer.from(JSON.stringify(archive)),
    });
  page.once("dialog", (dialog) => dialog.accept());
  await page
    .getByRole("button", { name: "Use this draft", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Next: Content", exact: true })
    .click();
  await page
    .getByRole("combobox", { name: "Plugin input", exact: true })
    .selectOption("prepare");
  expect(JSON.stringify(await downloaded(page))).not.toContain(
    "Unfinished preparation",
  );
  expect(writes).toEqual([]);
});
