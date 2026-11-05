// Native fields cross the actual UI/API boundary without contacting described servers or packages.
import AxeBuilder from "@axe-core/playwright";
import type { Page } from "@playwright/test";
import { test, expect, login, confirmCommand, type Catalog } from "./fixtures";
import { array, object, text } from "./json";
import { required } from "../required";
import { openDisclosure } from "./editor-disclosure";
import { doubleText } from "./text-layout";

const cardSchema =
  "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json";
const registrySchema =
  "https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json";
function observe(page: Page, origin: string) {
  const writes: { path: string; body: unknown; key: string | undefined }[] = [];
  const outside: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (["http:", "https:"].includes(url.protocol) && url.origin !== origin)
      outside.push(url.origin);
    if (
      request.method() === "POST" &&
      url.pathname.includes("/_catalog/api/v1/")
    )
      writes.push({
        path: url.pathname,
        body: request.postDataJSON(),
        key: request.headers()["idempotency-key"],
      });
  });
  return { writes, outside };
}
async function start(page: Page, catalog: Catalog, identifier: string) {
  await login(page, catalog, "local-contributor", "/resources/new");
  await page
    .getByRole("combobox", { name: "Namespace", exact: true })
    .selectOption(catalog.receipt.namespaceId);
  await page
    .getByRole("combobox", { name: "Kind", exact: true })
    .selectOption("mcp_server");
  await page
    .getByLabel("Canonical resource identifier", { exact: true })
    .fill(identifier);
  await page
    .getByRole("button", { name: "Next: Content", exact: true })
    .click();
  await page.getByLabel("Version label", { exact: true }).fill("1.0.0");
}
async function save(page: Page, action = "Save content draft") {
  const pending = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      /\/resources\/[^/]+\/versions$/.test(new URL(response.url()).pathname),
  );
  await page.getByRole("button", { name: action, exact: true }).click();
  await confirmCommand(page);
  const response = await pending;
  expect(response.status()).toBe(201);
  return object(await response.json());
}
async function assertRetained(
  catalog: Catalog,
  bundle: Record<string, unknown>,
  expected: object,
  media: string,
) {
  const revisionId = text(object(bundle.revision).id);
  const stored = await catalog.admin(
    "GET",
    `/api/v1/version-revisions/${revisionId}`,
  );
  const representations = array(object(stored.data.revision).representations);
  expect(representations).toHaveLength(1);
  expect(object(required(representations[0]))).toMatchObject({
    role: "primary",
    mediaType: media,
    validationState: "valid",
    data: expected,
  });
  expect(object(required(representations[0])).data).toEqual(expected);
  expect(object(bundle.evidence).state).toBe("complete");
}

for (const base of ["root", "subpath"] as const) {
  for (const purpose of ["card", "remote", "package"] as const) {
    test(`${base}: create MCP ${purpose} metadata from ordinary fields without JSON entry`, async ({
      page,
      catalog,
    }, testInfo) => {
      const observed = observe(page, catalog.origin);
      const identifier = `urn:air:catalog.example.com:guided:fields-mcp-${purpose}-${base}`;
      await start(page, catalog, identifier);
      const format = page.getByRole("combobox", {
        name: "MCP document",
        exact: true,
      });
      await expect(format).toHaveValue("card");
      if (purpose !== "card") await format.selectOption("registry");
      await page
        .getByLabel("MCP server name", { exact: true })
        .fill("com.example/documentation");
      await page
        .getByLabel("MCP display title (optional)", { exact: true })
        .fill("Documentation search");
      await page
        .getByLabel("MCP description", { exact: true })
        .fill("Search documentation without changing it.");
      const native: Record<string, unknown> = {
        $schema: purpose === "card" ? cardSchema : registrySchema,
        name: "com.example/documentation",
        title: "Documentation search",
        description: "Search documentation without changing it.",
        version: "release 2026 Q4",
      };
      if (purpose === "card" || purpose === "remote") {
        await page
          .getByRole("button", { name: "Add endpoint", exact: true })
          .press("Enter");
        await expect(
          page.getByLabel("Endpoint 1 URL", { exact: true }),
        ).toBeFocused();
        await page
          .getByLabel("Endpoint 1 URL", { exact: true })
          .fill("https://mcp.example.test/documentation");
        await page
          .getByRole("combobox", { name: "Endpoint 1 transport", exact: true })
          .selectOption(base === "root" ? "streamable-http" : "sse");
        const endpoint: Record<string, unknown> = {
          type: base === "root" ? "streamable-http" : "sse",
          url: "https://mcp.example.test/documentation",
        };
        if (purpose === "card") {
          await openDisclosure(page, "Endpoint 1 protocol versions (optional)");
          await page
            .getByRole("button", {
              name: "Add endpoint 1 protocol version",
              exact: true,
            })
            .click();
          await page
            .getByLabel("Endpoint 1 protocol version 1", { exact: true })
            .selectOption("2025-11-25");
          await page
            .getByRole("button", {
              name: "Add endpoint 1 protocol version",
              exact: true,
            })
            .click();
          await page
            .getByLabel("Endpoint 1 custom protocol version 2", { exact: true })
            .fill("vendor-experimental");
          endpoint.supportedProtocolVersions = [
            "2025-11-25",
            "vendor-experimental",
          ];
        }
        await openDisclosure(page, "Endpoint 1 headers");
        await page
          .getByRole("button", { name: "Add header requirement", exact: true })
          .click();
        await page
          .getByLabel("Endpoint 1 headers 1 name", { exact: true })
          .fill("Authorization");
        await page
          .getByLabel("Endpoint 1 headers 1 description (optional)", {
            exact: true,
          })
          .fill("Caller supplies a token at runtime.");
        endpoint.headers = [
          {
            name: "Authorization",
            description: "Caller supplies a token at runtime.",
            isRequired: true,
            isSecret: true,
          },
        ];
        native.remotes = [endpoint];
        await page
          .getByRole("button", { name: "Add endpoint", exact: true })
          .press("Enter");
        await expect(
          page.getByLabel("Endpoint 2 URL", { exact: true }),
        ).toBeFocused();
        await page
          .getByRole("button", { name: "Remove endpoint 2", exact: true })
          .press("Enter");
        await expect(
          page.getByLabel("Endpoint 1 URL", { exact: true }),
        ).toBeFocused();
      } else {
        await page
          .getByRole("button", { name: "Add package", exact: true })
          .press("Enter");
        await expect(
          page.getByLabel("Package 1 identifier", { exact: true }),
        ).toBeFocused();
        await page
          .getByLabel("Package 1 identifier", { exact: true })
          .fill("@example/documentation-mcp");
        const pkg: Record<string, unknown> = {
          registryType: "npm",
          identifier: "@example/documentation-mcp",
          transport: { type: "stdio" },
        };
        if (base === "root") {
          await page
            .getByLabel("Package 1 version (optional)", { exact: true })
            .fill("2.3.4");
          pkg.version = "2.3.4";
        } else {
          await page
            .getByRole("combobox", { name: "Package 1 transport", exact: true })
            .selectOption("streamable-http");
          await page
            .getByLabel("Package 1 transport URL or template", { exact: true })
            .fill("http://localhost:{port}/mcp");
          pkg.transport = {
            type: "streamable-http",
            url: "http://localhost:{port}/mcp",
          };
        }
        await openDisclosure(page, "Package 1 environment");
        await page
          .getByRole("button", {
            name: "Add environment requirement",
            exact: true,
          })
          .click();
        await page
          .getByLabel("Package 1 environment 1 name", { exact: true })
          .fill("API_KEY");
        pkg.environmentVariables = [
          { name: "API_KEY", isSecret: true, isRequired: true },
        ];
        native.packages = [pkg];
      }
      await page
        .getByLabel("Version label", { exact: true })
        .fill("release 2026 Q4");
      await page
        .getByRole("button", { name: "Back: Basics", exact: true })
        .click();
      await page
        .getByRole("button", { name: "Next: Content", exact: true })
        .click();
      await expect(
        page.getByLabel("MCP server name", { exact: true }),
      ).toHaveValue("com.example/documentation");
      const raw = page.getByRole("textbox", {
        name: "Standards document (JSON)",
        exact: true,
        includeHidden: true,
      });
      await expect
        .poll(async () => object(JSON.parse(await raw.inputValue())).version)
        .toBe("release 2026 Q4");
      expect(JSON.parse(await raw.inputValue())).toEqual(native);
      expect(observed.writes).toHaveLength(0);
      const advanced = page
        .locator("details")
        .filter({
          has: page.locator(":scope > summary", {
            hasText: /^Advanced MCP JSON$/,
          }),
        })
        .first();
      await expect(advanced).not.toHaveAttribute("open");
      if (purpose === "card" && base === "root") {
        const requiredHeader = page.getByRole("checkbox", {
          name: "Endpoint 1 headers 1: Required",
          exact: true,
        });
        const assertCheckboxLayout = async () => {
          const box = required(await requiredHeader.boundingBox());
          const label = required(
            await requiredHeader.locator("..").locator("span").boundingBox(),
          );
          if (!box || !label) throw new Error("Missing header control layout");
          expect(box.width).toBeLessThanOrEqual(24);
          expect(box.x + box.width).toBeLessThanOrEqual(label.x);
          expect(
            Math.abs(box.y + box.height / 2 - label.y - label.height / 2),
          ).toBeLessThanOrEqual(2);
        };
        await assertCheckboxLayout();
        await page.evaluate(() => {
          (document.activeElement as HTMLElement).blur();
          window.scrollTo(0, 0);
        });
        await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(0);
        await page.screenshot({
          path: testInfo.outputPath("guided-mcp-fields.png"),
          fullPage: true,
        });
        await page.setViewportSize({ width: 320, height: 844 });
        await doubleText(page);
        await assertCheckboxLayout();
        await expect
          .poll(() =>
            page.evaluate(
              () => document.documentElement.scrollWidth <= innerWidth,
            ),
          )
          .toBe(true);
        const accessibility = await new AxeBuilder({ page })
          .withTags(["wcag2a", "wcag2aa", "wcag21aa"])
          .analyze();
        expect(accessibility.violations).toEqual([]);
        expect(accessibility.incomplete).toEqual([]);
      }
      const bundle = await save(page);
      expect(object(bundle.version).version).toBe("release 2026 Q4");
      expect(observed.writes).toHaveLength(2);
      expect(required(observed.writes[0]).key).toBeTruthy();
      expect(required(observed.writes[1]).key).not.toBe(
        required(observed.writes[0]).key,
      );
      expect(required(observed.writes[1]).body).toEqual({
        version: "release 2026 Q4",
        representations: [
          {
            role: "primary",
            mediaType:
              purpose === "card"
                ? "application/mcp-server-card+json"
                : "application/vnd.mcp.server+json",
            data: native,
          },
        ],
      });
      await assertRetained(
        catalog,
        bundle,
        native,
        purpose === "card"
          ? "application/mcp-server-card+json"
          : "application/vnd.mcp.server+json",
      );
      expect(observed.outside).toEqual([]);
    });
  }
}

test("MCP metadata without endpoints or packages remains a valid authoring choice", async ({
  page,
  catalog,
}) => {
  const observed = observe(page, catalog.origin);
  await start(
    page,
    catalog,
    "urn:air:catalog.example.com:guided:mcp-no-distribution",
  );
  await page
    .getByRole("combobox", { name: "MCP document", exact: true })
    .selectOption("registry");
  await page
    .getByLabel("MCP server name", { exact: true })
    .fill("com.example/metadata-only");
  await page
    .getByLabel("MCP description", { exact: true })
    .fill("Metadata before a distribution is declared.");
  await expect(
    page.getByText("No distribution is declared.", { exact: false }),
  ).toBeVisible();
  const result = await save(page);
  expect(object(result.evidence).state).toBe("complete");
  expect(
    array(object(result.evidence).findings).some(
      (finding) => object(finding).code === "MCP_DISTRIBUTION_NOT_DECLARED",
    ),
  ).toBe(true);
  expect(observed.writes).toHaveLength(2);
  expect(observed.outside).toEqual([]);
});

for (const format of ["card", "registry"] as const) {
  test(`imported MCP ${format} retains advanced settings and its independent native version`, async ({
    page,
    catalog,
  }) => {
    const observed = observe(page, catalog.origin);
    await start(
      page,
      catalog,
      `urn:air:catalog.example.com:guided:mcp-import-${format}`,
    );
    await page
      .getByRole("combobox", { name: "MCP document", exact: true })
      .selectOption(format);
    const original: Record<string, unknown> = {
      $schema: format === "card" ? cardSchema : registrySchema,
      name: "com.example/imported",
      description: "Imported metadata.",
      version: "external",
      repository: {
        source: "custom",
        url: "https://example.test/source",
        subfolder: "servers/docs",
        id: "kept",
      },
      _meta: { "com.example/extra": { ratio: 0.25 } },
      remotes: [
        {
          type: "sse",
          url: format === "card" ? "{baseUrl}/mcp" : "https://example.test/mcp",
          variables: { baseUrl: { default: "https://example.test" } },
          headers: [
            {
              name: "Authorization",
              value: "Bearer {token}",
              variables: { token: { isSecret: true } },
            },
          ],
        },
      ],
    };
    if (format === "registry")
      original.packages = [
        {
          registryType: "cargo",
          identifier: "docs-mcp",
          version: "2.3.4",
          registryBaseUrl: "https://example.test/crates",
          runtimeHint: "custom",
          runtimeArguments: [
            { type: "named", name: "--mode", value: "two words" },
          ],
          packageArguments: [{ type: "positional", value: "comma, literal" }],
          transport: { type: "stdio" },
          environmentVariables: [{ name: "API_KEY", isSecret: true }],
        },
      ];
    const content = JSON.stringify(original, null, 4);
    await openDisclosure(page, "Import an existing MCP document");
    await page.getByLabel("Document file", { exact: true }).setInputFiles({
      name: "server.json",
      mimeType: "application/json",
      buffer: Buffer.from(content),
    });
    await page
      .getByRole("button", { name: "Use loaded document", exact: true })
      .click();
    const raw = page.getByRole("textbox", {
      name: "Standards document (JSON)",
      exact: true,
    });
    await expect(raw).toBeFocused();
    await expect(raw).toHaveValue(content);
    await page.getByLabel("Version label", { exact: true }).fill("other");
    await expect(raw).toHaveValue(content);
    await page
      .locator("summary")
      .filter({ hasText: /^Advanced MCP JSON$/ })
      .click();
    await page
      .getByRole("textbox", { name: "MCP description", exact: true })
      .fill("Changed description.");
    await page.getByLabel("Version label", { exact: true }).fill("external");
    const expected = { ...original, description: "Changed description." };
    const bundle = await save(page);
    await assertRetained(
      catalog,
      bundle,
      expected,
      format === "card"
        ? "application/mcp-server-card+json"
        : "application/vnd.mcp.server+json",
    );
    expect(observed.writes).toHaveLength(2);
    expect(observed.outside).toEqual([]);
  });
}

test("a released MCP resource gets a new field-generated Version without changing the earlier Version", async ({
  page,
  catalog,
}) => {
  const existing = required(
    catalog.receipt.items.find((item) => item.key === "order-mcp"),
  );
  const oldRevision = await catalog.admin(
    "GET",
    `/api/v1/version-revisions/${existing.versionRevisionId}`,
  );
  const oldVersionId = text(
    object(oldRevision.data.revision).resourceVersionId,
  );
  const before = await catalog.admin(
    "GET",
    `/api/v1/resource-versions/${oldVersionId}`,
  );
  const observed = observe(page, catalog.origin);
  await login(
    page,
    catalog,
    "local-contributor",
    `/resources/${existing.resourceId}`,
  );
  await page.getByRole("link", { name: "Add version", exact: true }).click();
  for (const [label, value] of [
    ["Version label", "2.0.0"],
    ["MCP server name", "com.example/order-mcp"],
    ["MCP description", "New documentation metadata."],
  ] as const)
    await page.getByLabel(label, { exact: true }).fill(value);
  const bundle = await save(page, "Create version");
  expect(observed.writes).toHaveLength(1);
  expect(required(observed.writes[0]).path).toContain(
    `/resources/${existing.resourceId}/versions`,
  );
  expect(object(bundle.version).resourceId).toBe(existing.resourceId);
  await assertRetained(
    catalog,
    bundle,
    {
      $schema: cardSchema,
      name: "com.example/order-mcp",
      description: "New documentation metadata.",
      version: "2.0.0",
    },
    "application/mcp-server-card+json",
  );
  const after = await catalog.admin(
    "GET",
    `/api/v1/resource-versions/${oldVersionId}`,
  );
  expect(after.data).toEqual(before.data);
  expect(observed.outside).toEqual([]);
});
