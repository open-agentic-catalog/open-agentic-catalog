// Real governed records and inert configuration downloads. No discovered endpoint is contacted.
import { readFile } from "node:fs/promises";
import AxeBuilder from "@axe-core/playwright";
import { test, expect, login, signOut } from "./fixtures";
import type { Catalog } from "./fixtures";
import { array, object, text } from "./json";
import { doubleText } from "./text-layout";

async function publish(catalog: Catalog, document: Record<string, unknown>) {
  const resource = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/resources",
    {
      namespaceId: catalog.receipt.namespaceId,
      kind: "mcp_server",
      canonicalIdentifier: "custom:mcp-client-configuration",
    },
  );
  const version = await catalog.admin<{
    revision: { id: string };
    evidence: { state: string };
  }>("POST", `/api/v1/resources/${resource.data.id}/versions`, {
    version: text(document.version),
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
    { namespaceId: catalog.receipt.namespaceId, resourceId: resource.data.id },
  );
  const revision = await catalog.admin(
    "POST",
    `/api/v1/catalog-listings/${listing.data.id}/revisions`,
    {
      versionRevisionId: version.data.revision.id,
      publisherId: catalog.receipt.publisherId,
      displayName: "MCP client setup example",
      description: "A remote connection for client setup.",
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
    { reason: "Independent client setup review" },
    submission.etag,
    "local-reviewer",
  );
  const published = await catalog.admin<{ release: { id: string } }>(
    "POST",
    `/api/v1/catalog-listings/${listing.data.id}:publish`,
    { submissionId: submission.data.id },
    revision.etag,
  );
  return published.data.release.id;
}

for (const scope of ["root", "subpath"])
  test(`${scope}: MCP client export matches CLI formats without endpoint requests`, async ({
    page,
    catalog,
  }) => {
    const cases: unknown = JSON.parse(
      await readFile(
        new URL(
          "../../../cmd/catalogctl/testdata/mcp-client-export/cases.json",
          import.meta.url,
        ),
        "utf8",
      ),
    );
    const fixture = object(array(cases)[0]);
    const nativeDocument = object(fixture.document);
    const release = await publish(catalog, nativeDocument);
    let foreign = 0,
      mutations = 0,
      artifacts = 0;
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (url.origin !== catalog.origin && !url.protocol.startsWith("blob"))
        foreign++;
      if (url.pathname.includes("/_catalog/") && request.method() !== "GET")
        mutations++;
      if (url.pathname.includes("/artifacts/")) artifacts++;
    });
    await login(page, catalog, "local-reader", `/catalog/${release}`);
    const panel = page.getByRole("region", {
      name: "MCP client configuration",
      exact: true,
    });
    await expect(panel).toBeVisible();
    await expect(panel.getByRole("link")).toHaveCount(0);
    for (const format of ["mcp-vscode", "mcp-claude-code", "mcp-cursor"]) {
      await panel
        .getByRole("combobox", { name: "MCP client", exact: true })
        .selectOption(format);
      await panel
        .getByRole("button", {
          name: "Prepare client configuration",
          exact: true,
        })
        .click();
      await expect(
        panel.getByRole("link", { name: "Download client configuration" }),
      ).toBeVisible();
      await expect(panel).not.toContainText("PRIVATE_");
      await expect(panel).toContainText("Authorization (required)");
      const downloaded = page.waitForEvent("download");
      await panel
        .getByRole("link", { name: "Download client configuration" })
        .click();
      const download = await downloaded;
      expect(download.suggestedFilename()).toBe("mcp.json");
      const file = await download.path();
      expect(file).not.toBeNull();
      const actual: unknown = JSON.parse(await readFile(file!, "utf8"));
      const expected = object(object(fixture.expected)[format]);
      const wrapper = format === "mcp-vscode" ? "servers" : "mcpServers";
      const entries = object(expected[wrapper]);
      const original = Object.values(entries)[0];
      expect(actual).toEqual({
        [wrapper]: { [`catalog-${release}`]: original },
      });
    }
    await panel.screenshot({
      path: test.info().outputPath("mcp-client-configuration.png"),
    });
    await panel
      .getByRole("combobox", { name: "MCP client", exact: true })
      .selectOption("mcp-vscode");
    await expect(panel.getByRole("link")).toHaveCount(0);
    await panel
      .getByRole("button", {
        name: "Prepare client configuration",
        exact: true,
      })
      .focus();
    await page.keyboard.press("Enter");
    await expect(panel.getByRole("link")).toBeVisible();
    expect(
      await new AxeBuilder({ page }).include(".mcp-client-export").analyze(),
    ).toMatchObject({ violations: [], incomplete: [] });
    await page.getByLabel("Appearance", { exact: true }).click();
    await page.getByRole("radio", { name: "Dark", exact: true }).check();
    await page.keyboard.press("Escape");
    await page.setViewportSize({ width: 320, height: 1200 });
    await doubleText(page);
    await expect
      .poll(() => page.evaluate(() => document.documentElement.scrollWidth))
      .toBeLessThanOrEqual(320);
    expect(
      await new AxeBuilder({ page }).include(".mcp-client-export").analyze(),
    ).toMatchObject({ violations: [], incomplete: [] });
    expect(foreign).toBe(0);
    expect(mutations).toBe(0);
    expect(artifacts).toBe(0);
  });

test("stored MCP configuration verifies bytes, refuses denied reads and reuses verified content", async ({
  page,
  catalog,
  network,
}) => {
  const cases: unknown = JSON.parse(
    await readFile(
      new URL(
        "../../../cmd/catalogctl/testdata/mcp-client-export/cases.json",
        import.meta.url,
      ),
      "utf8",
    ),
  );
  const nativeDocument = object(object(array(cases)[0]).document);
  const release = await publish(catalog, nativeDocument);
  const { createHash } = await import("node:crypto");
  const bytes = Buffer.from(JSON.stringify(nativeDocument));
  const hash = createHash("sha256").update(bytes);
  const artifactID = hash.digest("hex");
  const digest = createHash("sha256").update(bytes).digest("base64");
  await page.route(
    `**/_catalog/api/v1/catalog/resources/${release}`,
    async (route) => {
      const response = await route.fetch();
      const body: unknown = await response.json();
      const record = object(body);
      const rep = object(
        array(object(record.versionRevision).representations)[0],
      );
      delete rep.data;
      Object.assign(rep, {
        artifactId: artifactID,
        contentDigest: `sha256:${artifactID}`,
        size: bytes.length,
      });
      await route.fulfill({ response, json: body });
    },
  );
  network.expectFailure(
    "GET",
    `/_catalog/api/v1/artifacts/${artifactID}/content`,
    403,
  );
  let reads = 0;
  let fault = "denied";
  await page.route("**/artifacts/*/content", async (route) => {
    reads++;
    if (fault === "denied") {
      await route.fulfill({
        status: 403,
        contentType: "application/problem+json",
        body: JSON.stringify({
          type: "about:blank",
          title: "Forbidden",
          status: 403,
          code: "FORBIDDEN",
        }),
      });
      return;
    }
    await route.fulfill({
      status: 200,
      headers: {
        "content-type":
          fault === "media" ? "text/plain" : "application/vnd.mcp.server+json",
        "content-length": String(bytes.length),
        "content-digest": `sha-256=:${fault === "digest" ? Buffer.alloc(32).toString("base64") : digest}:`,
      },
      body: bytes,
    });
  });
  await login(page, catalog, "local-reader", `/catalog/${release}`);
  const panel = page.getByRole("region", {
    name: "MCP client configuration",
    exact: true,
  });
  const prepare = panel.getByRole("button", {
    name: "Prepare client configuration",
    exact: true,
  });
  await expect(prepare).toBeVisible();
  expect(reads).toBe(0);
  for (fault of ["denied", "media", "digest"]) {
    await prepare.click();
    await expect(panel.getByRole("alert")).toBeVisible();
    await expect(panel.getByRole("link")).toHaveCount(0);
  }
  fault = "none";
  await prepare.click();
  await expect(
    panel.getByRole("link", { name: "Download client configuration" }),
  ).toBeVisible();
  expect(reads).toBe(4);
  await panel
    .getByRole("combobox", { name: "MCP client", exact: true })
    .selectOption("mcp-cursor");
  await expect(panel.getByRole("link")).toHaveCount(0);
  await prepare.click();
  await expect(panel.getByRole("link")).toBeVisible();
  expect(reads).toBe(4);
});

for (const scope of ["root", "subpath"])
  test(`${scope}: MCP export requires the selected remote and clears prepared files on sign-out`, async ({
    page,
    catalog,
  }) => {
    const cases: unknown = JSON.parse(
      await readFile(
        new URL(
          "../../../cmd/catalogctl/testdata/mcp-client-export/cases.json",
          import.meta.url,
        ),
        "utf8",
      ),
    );
    const nativeDocument = object(object(array(cases)[0]).document);
    const first = object(array(nativeDocument.remotes)[0]);
    const secondURL =
      "https://mcp.example.test/events?cursor=a%2Fb&tag=two+words";
    const release = await publish(catalog, {
      ...nativeDocument,
      remotes: [first, { ...first, type: "sse", url: secondURL }],
    });
    let foreign = 0,
      mutations = 0,
      artifacts = 0,
      downloads = 0;
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (url.origin !== catalog.origin && !url.protocol.startsWith("blob"))
        foreign++;
      if (url.pathname.includes("/_catalog/") && request.method() !== "GET")
        mutations++;
      if (url.pathname.includes("/artifacts/")) artifacts++;
    });
    page.on("download", () => downloads++);
    await login(page, catalog, "local-reader", `/catalog/${release}`);
    const panel = page.getByRole("region", {
      name: "MCP client configuration",
      exact: true,
    });
    const prepare = panel.getByRole("button", {
      name: "Prepare client configuration",
      exact: true,
    });
    const save = panel.getByRole("link", {
      name: "Download client configuration",
      exact: true,
    });
    await prepare.click();
    await expect(panel.getByRole("alert")).toContainText(
      "Choose a remote connection",
    );
    await expect(save).toHaveCount(0);
    expect(downloads).toBe(0);
    for (const position of [2, 1]) {
      await panel
        .getByRole("combobox", { name: "Remote connection", exact: true })
        .selectOption(String(position));
      await expect(save).toHaveCount(0);
      expect(downloads).toBe(position === 2 ? 0 : 1);
      await prepare.click();
      await expect(save).toBeVisible();
      const pending = page.waitForEvent("download");
      await save.click();
      const download = await pending;
      const path = await download.path();
      expect(path).not.toBeNull();
      const config: unknown = JSON.parse(await readFile(path!, "utf8"));
      expect(config).toEqual({
        servers: {
          [`catalog-${release}`]:
            position === 2
              ? { type: "sse", url: secondURL }
              : { type: "http", url: first.url },
        },
      });
      await expect(panel).not.toContainText("PRIVATE_");
    }
    await signOut(page);
    await expect(
      page.getByRole("region", {
        name: "MCP client configuration",
        exact: true,
      }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("link", {
        name: "Download client configuration",
        exact: true,
      }),
    ).toHaveCount(0);
    expect(downloads).toBe(2);
    expect(foreign).toBe(0);
    expect(mutations).toBe(0);
    expect(artifacts).toBe(0);
  });
