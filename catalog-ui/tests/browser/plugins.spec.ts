import type { RecordDetail } from "../../src/api/client";
import { required } from "../required";
import { test, expect, login, confirmCommand, signOut } from "./fixtures";
import { createHash } from "node:crypto";
import type { components, operations } from "../../src/api/schema";
import type { FetchResponse } from "openapi-fetch";

// The default fixture exercises production public-download policy with no demo route.
test("Plugin authoring guides archive inputs and rejects private downloads", async ({
  page,
  catalog,
  network,
}) => {
  await login(page, catalog, "local-contributor", "/resources/new");
  await page
    .getByRole("combobox", { name: "Namespace", exact: true })
    .selectOption(catalog.receipt.namespaceId);
  await page
    .getByRole("textbox", { name: "Canonical resource identifier" })
    .fill("urn:air:catalog.example.com:demo:browser-plugin");
  await page
    .getByRole("combobox", { name: "Kind", exact: true })
    .selectOption("plugin");
  await page
    .getByRole("button", { name: "Create resource", exact: true })
    .click();
  await confirmCommand(page);
  await page.getByRole("link", { name: "Open created resource" }).click();
  await page.reload();
  await page.getByRole("link", { name: "Add version", exact: true }).click();
  await page.getByRole("textbox", { name: "Version label" }).fill("1.0.0");
  await expect(
    page.getByRole("option", { name: "Use an existing archive" }),
  ).toHaveCount(1);
  await expect(
    page.getByRole("textbox", {
      name: "Standards document (JSON)",
      exact: true,
    }),
  ).toHaveCount(0);
  await page
    .getByRole("textbox", { name: "Plugin archive URL" })
    .fill("https://127.0.0.1/plugin.zip");
  await page
    .getByRole("textbox", { name: "Expected archive checksum (optional)" })
    .fill("not-a-checksum");
  await page
    .getByRole("button", { name: "Create version", exact: true })
    .click();
  await expect(page.getByRole("alert")).toContainText("SHA-256");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page
    .getByRole("textbox", { name: "Expected archive checksum (optional)" })
    .fill("");
  const resourceID = new URL(page.url()).searchParams.get("resource");
  network.expectFailure(
    "POST",
    `/_catalog/api/v1/resources/${resourceID}/versions`,
    422,
  );
  await page
    .getByRole("button", { name: "Create version", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  await expect(page.getByRole("dialog").getByRole("alert")).toContainText(
    "VALIDATION_FAILED",
  );
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Back to form", exact: true })
    .click();
  await expect(page.getByRole("dialog")).not.toBeVisible();
  await expect(
    page.getByRole("link", { name: "Open saved version" }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("textbox", { name: "Plugin archive URL" }),
  ).toHaveValue("https://127.0.0.1/plugin.zip");
});

// Rendering-only fixture: successful archive retrieval/validation is covered by
// Go runtime tests. Do not mistake this intercepted read for saved Plugin state.
test("Plugin inspection rendering preserves documents and inert external handoffs", async ({
  page,
  catalog,
  network,
}) => {
  const item = catalog.receipt.items.find(
    (item) => item.key === "order-agent",
  )!;
  type Bundle = NonNullable<
    FetchResponse<
      operations["getVersionRevision"],
      object,
      "application/json"
    >["data"]
  >;
  const path = `/api/v1/version-revisions/${item.versionRevisionId}`;
  const { data: bundle } = await catalog.admin<Bundle>("GET", path);
  const documents = [
    {
      path: "plugin.json",
      content:
        '{\n  "name": "fixture-tools",\n  "description": "<img src=x onerror=alert(1)>"\n}\n',
    },
    {
      path: "mcp.json",
      content:
        '{ "mcpServers": { "tools": { "url": "https://tools.example.com/mcp" } } }\n',
    },
    {
      path: "skills/read/SKILL.md",
      content:
        "---\nname: read\ndescription: Read a document\n---\n\n<script>alert(1)</script>\n  Keep this indentation.\n",
    },
  ];
  const plugin: components["schemas"]["PluginInspection"] = {
    rootDirectory: "",
    expandedSize: documents.reduce(
      (size, doc) => size + Buffer.byteLength(doc.content),
      0,
    ),
    directories: ["skills", "skills/read"],
    files: documents.map((doc) => ({
      path: doc.path,
      size: Buffer.byteLength(doc.content),
      digest:
        "sha256:" + createHash("sha256").update(doc.content).digest("hex"),
      executable: false,
    })),
    documents,
    components: [
      { kind: "skill", name: "read", path: "skills/read/SKILL.md" },
      { kind: "mcp_server", name: "tools", path: "mcp.json" },
    ],
    assessments: {
      malware: "not_assessed",
      secrets: "not_assessed",
      license: "not_assessed",
      sbom: "not_assessed",
      provenance: "not_assessed",
    },
  };
  const representation: components["schemas"]["Representation"] = {
    id: required(bundle.revision.representations[0]).id,
    role: "primary",
    mediaType: "application/agent-plugins+zip",
    profileId: "agent-plugins-1.0.0-1fc1b627",
    url: "https://packages.example.com/tools.zip",
    contentDigest: "sha256:" + "a".repeat(64),
    size: 500,
    validationState: "valid",
    plugin,
  };
  await page.route(`**/_catalog${path}`, async (route) => {
    expect(route.request().method()).toBe("GET");
    await route.fulfill({
      json: {
        ...bundle,
        revision: { ...bundle.revision, representations: [representation] },
      },
    });
  });
  // Keep the related resource read consistent without changing the seed database.
  await page.route(
    `**/_catalog/api/v1/resources/${item.resourceId}`,
    async (route) => {
      const response = await route.fetch();
      const resource =
        (await response.json()) as components["schemas"]["Resource"];
      await route.fulfill({ response, json: { ...resource, kind: "plugin" } });
    },
  );
  const externalRequests: string[] = [];
  page.on("request", (request) => {
    if (new URL(request.url()).hostname.endsWith("example.com"))
      externalRequests.push(request.url());
  });
  await login(
    page,
    catalog,
    "local-reader",
    `/version-revisions/${item.versionRevisionId}`,
  );
  await expect(
    page.getByRole("heading", {
      name: "primary: application/agent-plugins+zip",
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    page.getByText("Archive checksum:", { exact: false }),
  ).toContainText(representation.contentDigest);
  await expect(
    page.getByText("Archive checksum:", { exact: false }),
  ).toContainText("500 bytes");
  await expect(
    page.getByText("Metadata inspection is not a security assessment.", {
      exact: false,
    }),
  ).toBeVisible();
  const handoff = page.getByRole("link", {
    name: "Open external archive",
    exact: true,
  });
  await expect(handoff).toHaveAttribute("href", representation.url!);
  await expect(handoff).toHaveAttribute("target", "_blank");
  await expect(handoff).toHaveAttribute("rel", "noreferrer");
  await expect(page.getByText("read", { exact: true })).toBeVisible();
  await expect(page.getByText("tools", { exact: true })).toBeVisible();
  const inventory = page.locator("details").filter({
    has: page.locator("summary", { hasText: "File inventory (3)" }),
  });
  await inventory.locator("summary").click();
  await expect(inventory.locator("pre")).toContainText(
    required(plugin.files[0]).digest,
  );
  for (const doc of documents) {
    const details = page.locator("details").filter({
      has: page.locator("summary").filter({
        hasText: new RegExp(
          `^${doc.path.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`,
        ),
      }),
    });
    await details.locator("summary").click();
    await expect
      .poll(() => details.locator("pre").textContent())
      .toBe(doc.content);
  }
  await expect(page.locator("main img, main script")).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: /^(Install|Run|Execute)$/ }),
  ).toHaveCount(0);
  // Catalog consumer rendering uses the same inspected evidence. This is still
  // a response fixture, not evidence that this Plugin was ingested or released.
  await page.route(
    `**/_catalog/api/v1/catalog/resources/${item.releaseId}`,
    async (route) => {
      const response = await route.fetch();
      const record = (await response.json()) as RecordDetail;
      await route.fulfill({
        response,
        json: {
          ...record,
          resource: { ...record.resource, kind: "plugin" },
          versionRevision: {
            ...record.versionRevision,
            representations: [representation],
          },
        },
      });
    },
  );
  await page.goto(catalog.origin + "/catalog/" + item.releaseId);
  const consumer = page.getByRole("region", { name: "Resource handoff" });
  await expect(
    consumer.getByRole("heading", {
      name: "primary: Plugin contents",
      exact: true,
    }),
  ).toBeVisible();
  await expect(consumer.getByText("read", { exact: true })).toBeVisible();
  await expect(consumer.getByText("tools", { exact: true })).toBeVisible();
  await expect(
    consumer.getByText("skills/read/SKILL.md", { exact: true }),
  ).toBeVisible();
  await expect(consumer.getByText("mcp.json", { exact: true })).toBeVisible();
  await expect(
    consumer.getByRole("button", {
      name: "Copy External archive URL",
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    consumer.getByRole("button", { name: /^(Install|Run|Execute)$/ }),
  ).toHaveCount(0);
  expect(externalRequests).toEqual([]);
  await expect
    .poll(
      () =>
        network
          .diagnostics()
          .assets.filter(
            (event) =>
              event.asset === "discovery" && event.event === "finished",
          ).length,
    )
    .toBe(1);
  expect(
    network
      .diagnostics()
      .assets.filter(
        (event) =>
          event.asset === "discovery" &&
          event.event === "response" &&
          event.status === 200,
      ),
  ).toHaveLength(1);
});

test.describe("prepared development Plugin archive", () => {
  test.use({ demoArchives: true });
  test("Plugin archive persists through browser authoring governance and Reader discovery", async ({
    page,
    catalog,
    network,
    context,
  }) => {
    const archive = required(catalog.archive);
    const outside: string[] = [];
    context.on("request", (request) => {
      if (new URL(request.url()).origin !== catalog.origin)
        outside.push(request.url());
    });
    await login(page, catalog, "local-contributor", "/resources/new");
    await page
      .getByRole("combobox", { name: "Namespace", exact: true })
      .selectOption(catalog.receipt.namespaceId);
    await page
      .getByRole("textbox", { name: "Canonical resource identifier" })
      .fill("urn:air:catalog.example.com:demo:persisted-plugin");
    await page
      .getByRole("combobox", { name: "Kind", exact: true })
      .selectOption("plugin");
    await page
      .getByRole("button", { name: "Create resource", exact: true })
      .click();
    await confirmCommand(page);
    await page.getByRole("link", { name: "Open created resource" }).click();
    const resourceURL = page.url();
    const resourceID = required(
      new URL(resourceURL).pathname.split("/").at(-1),
    );
    const historyPath = `/api/v1/resources/${resourceID}/versions`;
    const before = (await catalog.admin("GET", historyPath)).data;
    await page.getByRole("link", { name: "Add version", exact: true }).click();
    await page.getByRole("textbox", { name: "Version label" }).fill("1.0.0");
    await page
      .getByRole("textbox", { name: "Plugin archive URL" })
      .fill(archive.url);
    const checksum = page.getByRole("textbox", {
      name: "Expected archive checksum (optional)",
    });
    await checksum.fill("sha256:" + "0".repeat(64));
    network.expectFailure(
      "POST",
      `/_catalog/api/v1/resources/${resourceID}/versions`,
      422,
    );
    await page
      .getByRole("button", { name: "Create version", exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    await expect(page.getByRole("dialog").getByRole("alert")).toContainText(
      "VALIDATION_FAILED",
    );
    expect((await catalog.admin("GET", historyPath)).data).toEqual(before);
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Back to form", exact: true })
      .click();
    await expect(
      page.getByRole("textbox", { name: "Plugin archive URL" }),
    ).toHaveValue(archive.url);
    await checksum.fill(archive.digest);
    await page
      .getByRole("button", { name: "Create version", exact: true })
      .click();
    await confirmCommand(page);
    await expect(page.getByRole("status")).toContainText(
      "Draft saved. Version 1.0.0.",
    );
    await page.getByRole("link", { name: "Open saved version" }).click();
    await page
      .getByRole("link", { name: "Inspect current revision and validation" })
      .click();
    const revisionURL = page.url();
    const revisionID = required(
      new URL(revisionURL).pathname.split("/").at(-1),
    );
    type Bundle = NonNullable<
      FetchResponse<
        operations["getVersionRevision"],
        object,
        "application/json"
      >["data"]
    >;
    const retained = async () => {
      const { data } = await catalog.admin<Bundle>(
        "GET",
        `/api/v1/version-revisions/${revisionID}`,
      );
      const representation = required(data.revision.representations[0]);
      expect(representation.contentDigest).toBe(archive.digest);
      expect(representation.size).toBe(archive.size);
      expect(representation.url).toBe(archive.url);
      const plugin = required(representation.plugin);
      expect(plugin.documents).toEqual(archive.documents);
      expect(plugin.files).toEqual(archive.files);
      expect(plugin.components).toEqual(archive.components);
      expect(plugin.directories).toEqual(["skills", "skills/prepare-release"]);
      expect(plugin.assessments).toEqual({
        malware: "not_assessed",
        secrets: "not_assessed",
        license: "not_assessed",
        sbom: "not_assessed",
        provenance: "not_assessed",
      });
    };
    await retained();
    await page.reload();
    await expect(
      page.getByText("Archive checksum:", { exact: false }),
    ).toContainText(archive.digest);
    for (const doc of archive.documents) {
      const details = page.locator("details").filter({
        has: page.locator("summary").filter({
          hasText: new RegExp(
            `^${doc.path.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`,
          ),
        }),
      });
      await details.locator("summary").click();
      await expect
        .poll(() => details.locator("pre").textContent())
        .toBe(doc.content);
    }
    await retained();
    await expect(
      page.getByText("Metadata inspection is not a security assessment.", {
        exact: false,
      }),
    ).toBeVisible();
    // A separate signed-out context is unnecessary: the host API rejects omitted credentials directly.
    const unauthenticated = await fetch(
      catalog.origin + "/_catalog/api/v1/version-revisions/" + revisionID,
      {
        redirect: "manual",
        headers: { "X-Catalog-Request": "1" },
        signal: AbortSignal.timeout(1000),
      },
    );
    expect(unauthenticated.status).toBe(401);
    await page.goto(resourceURL);
    await page.getByRole("button", { name: "Listings", exact: true }).click();
    await page
      .getByRole("button", { name: "Create listing", exact: true })
      .click();
    await confirmCommand(page);
    await page
      .getByRole("link", { name: "Open existing listing", exact: true })
      .click();
    await page.getByRole("button", { name: "New listing revision" }).click();
    await page
      .getByRole("combobox", { name: "Resource version", exact: true })
      .selectOption({ label: "1.0.0" });
    await page
      .getByRole("combobox", { name: "Version revision", exact: true })
      .selectOption(revisionID);
    await page
      .getByRole("textbox", { name: "Display name", exact: true })
      .fill("Persisted browser Plugin");
    await page
      .getByRole("textbox", { name: "Description", exact: true })
      .fill("Inert prepared release metadata.");
    await page
      .getByRole("combobox", { name: "Publisher", exact: true })
      .selectOption(catalog.receipt.publisherId);
    await page
      .getByRole("button", { name: "Create listing revision", exact: true })
      .click();
    await confirmCommand(page);
    await page.reload();
    await page
      .getByRole("button", { name: "Submit for review", exact: true })
      .click();
    await confirmCommand(page);
    await page.getByRole("link", { name: "Open review submission" }).click();
    const reviewPath = new URL(page.url()).pathname;
    await signOut(page);
    await login(page, catalog, "local-reviewer", reviewPath);
    await page
      .getByRole("button", { name: "Inspect submitted evidence" })
      .click();
    await page
      .getByRole("combobox", { name: "Decision", exact: true })
      .selectOption("approve");
    await page
      .getByRole("textbox", { name: "Reason", exact: true })
      .fill("Independent Plugin metadata review");
    await page.getByRole("button", { name: "Record review decision" }).click();
    await confirmCommand(page);
    await signOut(page);
    await login(page, catalog, "local-publisher", reviewPath);
    await page
      .getByRole("button", { name: "Publish approved listing", exact: true })
      .click();
    await confirmCommand(page);
    await page.getByRole("link", { name: "Open published release" }).click();
    const releaseID = required(new URL(page.url()).pathname.split("/").at(-1));
    await required(catalog.stopArchives)();
    await retained();
    await page.goto(revisionURL);
    await page.reload();
    await expect(
      page.getByText("Archive checksum:", { exact: false }),
    ).toContainText(archive.digest);
    await signOut(page);
    await login(page, catalog, "local-reader", "/catalog");
    await page
      .getByRole("searchbox", { name: "Search Catalog", exact: true })
      .fill("Persisted browser Plugin");
    await page.getByRole("button", { name: "Search", exact: true }).click();
    await expect(
      page.getByRole("heading", {
        name: "Persisted browser Plugin",
        exact: true,
      }),
    ).toBeVisible();
    await page.goto(catalog.origin + "/catalog/" + releaseID);
    await expect(
      page.getByRole("heading", {
        name: "Persisted browser Plugin",
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      page.getByText("prepare-release", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("documentation", { exact: true }),
    ).toBeVisible();
    await expect(page.locator("main img, main script")).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: /^(Install|Run|Execute)$/ }),
    ).toHaveCount(0);
    expect(outside).toEqual([]);
  });
});
