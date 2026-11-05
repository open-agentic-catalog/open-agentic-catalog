// Ordinary fields produce the exact native Card retained by the real API.
import AxeBuilder from "@axe-core/playwright";
import { test, expect, login, confirmCommand } from "./fixtures";
import { array, object, text } from "./json";
import { required } from "../required";
import { openDisclosure } from "./editor-disclosure";
import { doubleText } from "./text-layout";
import { tabTo } from "./keyboard";

for (const base of ["root", "subpath"] as const) {
  test(`${base}: create and read an Agent from ordinary fields without entering JSON`, async ({
    page,
    catalog,
  }, testInfo) => {
    const writes: { path: string; body: unknown; key: string | undefined }[] =
      [];
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
        writes.push({
          path: url.pathname,
          body: request.postDataJSON(),
          key: request.headers()["idempotency-key"],
        });
    });
    await login(page, catalog, "local-contributor", "/resources/new");
    await page
      .getByRole("combobox", { name: "Namespace", exact: true })
      .selectOption(catalog.receipt.namespaceId);
    const identifier = `urn:air:catalog.example.com:guided:ordinary-agent-${base}`;
    await page
      .getByLabel("Canonical resource identifier", { exact: true })
      .fill(identifier);
    await page
      .getByRole("button", { name: "Next: Content", exact: true })
      .click();
    await page
      .getByLabel("Version label", { exact: true })
      .fill("release 2026 Q4");
    const name = page.getByLabel("Agent name", { exact: true });
    await name.fill("Order status Agent");
    await page
      .getByLabel("Agent description", { exact: true })
      .fill("Explain an order and its next step.");
    await page
      .getByLabel("Interface 1 HTTPS URL", { exact: true })
      .fill("https://agents.example.test/orders");
    await page
      .getByRole("combobox", { name: "Input format 1", exact: true })
      .selectOption({ label: "JSON" });
    await page
      .getByLabel("Ability 1 name", { exact: true })
      .fill("Explain order status");
    await page
      .getByRole("button", {
        name: "Use suggested ability ID: explain-order-status",
        exact: true,
      })
      .click();
    await page
      .getByLabel("Ability 1 description", { exact: true })
      .fill("Explain order status without changing the order.");
    await page
      .getByLabel("Ability 1 tag 1", { exact: true })
      .fill("orders, status");
    await page
      .getByRole("button", { name: "Add ability 1 tag", exact: true })
      .click();
    await page.getByLabel("Ability 1 tag 2", { exact: true }).fill("注文");
    await page
      .getByRole("button", { name: "Add interface", exact: true })
      .press("Enter");
    await expect(
      page.getByLabel("Interface 2 HTTPS URL", { exact: true }),
    ).toBeFocused();
    await page
      .getByRole("button", { name: "Remove interface 2", exact: true })
      .press("Enter");
    await expect(
      page.getByLabel("Interface 1 HTTPS URL", { exact: true }),
    ).toBeFocused();
    await page
      .getByRole("button", { name: "Add ability", exact: true })
      .press("Enter");
    await expect(
      page.getByLabel("Ability 2 name", { exact: true }),
    ).toBeFocused();
    await page
      .getByRole("button", { name: "Remove ability 2", exact: true })
      .press("Enter");
    await expect(
      page.getByLabel("Ability 1 name", { exact: true }),
    ).toBeFocused();
    await openDisclosure(page, "Interface 1 protocol settings");
    await page
      .getByLabel("Interface 1 protocol binding", { exact: true })
      .selectOption("GRPC");
    await page
      .getByLabel("Interface 1 A2A protocol version", { exact: true })
      .selectOption("0.3");
    await page
      .getByRole("button", { name: "Add interface", exact: true })
      .click();
    await page
      .getByLabel("Interface 2 HTTPS URL", { exact: true })
      .fill("https://agents.example.test/custom");
    await openDisclosure(page, "Interface 2 protocol settings");
    await page
      .getByLabel("Interface 2 protocol binding", { exact: true })
      .selectOption("__custom__");
    await page
      .getByLabel("Interface 2 custom protocol binding", { exact: true })
      .fill("example-binding");
    await page
      .getByLabel("Interface 2 A2A protocol version", { exact: true })
      .selectOption("__custom__");
    await page
      .getByLabel("Interface 2 custom A2A protocol version", { exact: true })
      .fill("2.1");
    await page
      .getByRole("button", { name: "Move interface 2 up", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Move interface 1 down", exact: true })
      .click();
    const advanced = page
      .locator("details")
      .filter({
        has: page.locator(":scope > summary", {
          hasText: /^Advanced Agent Card JSON$/,
        }),
      })
      .first();
    await expect(advanced).not.toHaveAttribute("open");
    await page
      .getByRole("button", { name: "Back: Basics", exact: true })
      .click();
    await page
      .getByRole("button", { name: "Next: Content", exact: true })
      .click();
    await expect(name).toHaveValue("Order status Agent");
    expect(writes).toHaveLength(0);
    await page.evaluate(() => {
      (document.activeElement as HTMLElement).blur();
      window.scrollTo(0, 0);
    });
    await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(0);
    await page.screenshot({
      path: testInfo.outputPath("guided-agent-fields.png"),
      fullPage: true,
    });
    if (base === "root") {
      await page.setViewportSize({ width: 320, height: 844 });
      await doubleText(page);
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
      await page.evaluate(() => {
        (document.activeElement as HTMLElement).blur();
        window.scrollTo(0, 0);
      });
      await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(0);
      await page.screenshot({
        path: testInfo.outputPath("guided-agent-fields-narrow.png"),
        fullPage: true,
      });
    }
    const reply = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        /\/resources\/[^/]+\/versions$/.test(new URL(response.url()).pathname),
    );
    await page
      .getByRole("button", { name: "Save content draft", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toContainText("release 2026 Q4");
    expect(writes).toHaveLength(0);
    await confirmCommand(page);
    const response = await reply;
    expect(response.status()).toBe(201);
    const bundle = object(await response.json());
    expect(object(bundle.evidence).state).toBe("complete");
    expect(object(bundle.version).version).toBe("release 2026 Q4");
    expect(writes).toHaveLength(2);
    expect(required(writes[0]).key).toBeTruthy();
    expect(required(writes[1]).key).toBeTruthy();
    expect(required(writes[0]).key).not.toBe(required(writes[1]).key);
    const body = object(required(writes[1]).body);
    expect(body).toEqual({
      version: "release 2026 Q4",
      representations: [
        {
          role: "primary",
          mediaType: "application/a2a-agent-card+json",
          data: {
            name: "Order status Agent",
            description: "Explain an order and its next step.",
            version: "release 2026 Q4",
            capabilities: {},
            supportedInterfaces: [
              {
                url: "https://agents.example.test/orders",
                protocolBinding: "GRPC",
                protocolVersion: "0.3",
              },
              {
                url: "https://agents.example.test/custom",
                protocolBinding: "example-binding",
                protocolVersion: "2.1",
              },
            ],
            defaultInputModes: ["application/json"],
            defaultOutputModes: ["text/plain"],
            skills: [
              {
                id: "explain-order-status",
                name: "Explain order status",
                description: "Explain order status without changing the order.",
                tags: ["orders, status", "注文"],
              },
            ],
          },
        },
      ],
    });
    await page
      .getByRole("link", { name: "Open saved version", exact: true })
      .click();
    await page
      .getByRole("link", {
        name: "Inspect current revision and validation",
        exact: true,
      })
      .click();
    await page
      .getByRole("button", { name: "Read retained document", exact: true })
      .click();
    const retained = page.locator("pre").filter({ visible: true });
    await expect(retained).toContainText("explain-order-status");
    expect(JSON.parse(await retained.innerText())).toEqual(
      object(required(array(body.representations)[0])).data,
    );
    expect(text(object(bundle.version).version)).toBe("release 2026 Q4");
    expect(outside).toEqual([]);
  });
}

test("an existing Agent gets a new Version through fields without recreating its Resource or changing the earlier Version", async ({
  page,
  catalog,
}) => {
  const existing = required(
    catalog.receipt.items.find((item) => item.key === "draft-agent"),
  );
  const previousRevision = await catalog.admin(
    "GET",
    `/api/v1/version-revisions/${existing.versionRevisionId}`,
  );
  const previousVersionId = text(
    object(previousRevision.data.revision).resourceVersionId,
  );
  const before = await catalog.admin(
    "GET",
    `/api/v1/resource-versions/${previousVersionId}`,
  );
  const writes: { path: string; body: unknown }[] = [];
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (request.method() === "POST" && path.includes("/_catalog/api/v1/"))
      writes.push({ path, body: request.postDataJSON() });
  });
  await login(
    page,
    catalog,
    "local-contributor",
    `/resources/${existing.resourceId}`,
  );
  await page.getByRole("link", { name: "Add version", exact: true }).click();
  for (const [label, value] of [
    ["Version label", "2.0.0"],
    ["Agent name", "New version Agent"],
    ["Agent description", "Explain the revised order workflow."],
    ["Interface 1 HTTPS URL", "https://agents.example.test/v2"],
    ["Ability 1 name", "Explain order"],
    ["Ability 1 ID", "explain-order"],
    ["Ability 1 description", "Explain the new workflow."],
    ["Ability 1 tag 1", "orders"],
  ] as const)
    await page.getByRole("textbox", { name: label, exact: true }).fill(value);
  const raw = page.getByRole("textbox", {
    name: "Standards document (JSON)",
    exact: true,
    includeHidden: true,
  });
  const original = await raw.inputValue();
  await openDisclosure(page, "Import an existing Agent document");
  await page.getByLabel("Document file", { exact: true }).setInputFiles({
    name: "same-card.json",
    mimeType: "application/json",
    buffer: Buffer.from(original),
  });
  await page
    .getByRole("button", { name: "Use loaded document", exact: true })
    .click();
  await page
    .getByRole("textbox", { name: "Version label", exact: true })
    .fill("3.0.0");
  await expect(raw).toHaveValue(original);
  await page
    .locator("summary")
    .filter({ hasText: /^Advanced Agent Card JSON$/ })
    .click();
  await page
    .getByRole("button", {
      name: "Use Version label in this Card",
      exact: true,
    })
    .click();
  await page
    .getByRole("textbox", { name: "Version label", exact: true })
    .fill("2.0.0");
  await expect
    .poll(async () => object(JSON.parse(await raw.inputValue())).version)
    .toBe("2.0.0");
  expect(writes).toHaveLength(0);
  const response = page.waitForResponse(
    (reply) =>
      reply.request().method() === "POST" &&
      new URL(reply.url()).pathname.endsWith(
        `/resources/${existing.resourceId}/versions`,
      ),
  );
  await page
    .getByRole("button", { name: "Create version", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toContainText(
    "Existing Resource and new Version",
  );
  await confirmCommand(page);
  const saved = await response;
  expect(saved.status()).toBe(201);
  const bundle = object(await saved.json());
  expect(object(bundle.evidence).state).toBe("complete");
  expect(object(bundle.version)).toMatchObject({
    resourceId: existing.resourceId,
    version: "2.0.0",
  });
  expect(writes).toHaveLength(1);
  const body = object(required(writes[0]).body);
  const native = object(object(required(array(body.representations)[0])).data);
  expect(native.version).toBe("2.0.0");
  expect(native.supportedInterfaces).toEqual([
    {
      url: "https://agents.example.test/v2",
      protocolBinding: "JSONRPC",
      protocolVersion: "1.0",
    },
  ]);
  const after = await catalog.admin(
    "GET",
    `/api/v1/resource-versions/${previousVersionId}`,
  );
  expect(after.data).toEqual(before.data);
});

test("a signed Agent import stays unchanged until the author explicitly creates an unsigned copy", async ({
  page,
  catalog,
}) => {
  const outside: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (
      ["http:", "https:"].includes(url.protocol) &&
      url.origin !== catalog.origin
    )
      outside.push(url.origin);
  });
  const card = {
    name: "Signed example",
    description: "A declaration fixture",
    version: "1.0.0",
    capabilities: {
      extensions: [
        { uri: "https://example.test/extra", params: { ratio: 0.25 } },
      ],
    },
    supportedInterfaces: [
      {
        url: "https://example.test/grpc",
        protocolBinding: "GRPC",
        protocolVersion: "1.0",
        tenant: "preserved",
      },
      {
        url: "https://example.test/json",
        protocolBinding: "JSONRPC",
        protocolVersion: "1.0",
      },
    ],
    defaultInputModes: ["application/json"],
    defaultOutputModes: ["text/plain"],
    skills: [
      {
        id: "review",
        name: "Review",
        description: "Review a document",
        tags: ["reviews, private"],
        examples: ["Review this document"],
        inputModes: ["application/json"],
      },
    ],
    provider: { organization: "Example", url: "https://example.test" },
    documentationUrl: "https://example.test/docs",
    iconUrl: "https://example.test/icon.png",
    securitySchemes: {
      bearer: { httpAuthSecurityScheme: { scheme: "Bearer" } },
    },
    securityRequirements: [{ schemes: { bearer: { list: [] } } }],
    signatures: [
      {
        protected: "eyJhbGciOiJSUzI1NiIsImtpZCI6InRlc3QifQ",
        signature: "AQID",
      },
    ],
  };
  const original = JSON.stringify(card, null, 4);
  await login(page, catalog, "local-contributor", "/resources/new");
  await page
    .getByRole("combobox", { name: "Namespace", exact: true })
    .selectOption(catalog.receipt.namespaceId);
  await page
    .getByLabel("Canonical resource identifier", { exact: true })
    .fill("urn:air:catalog.example.com:guided:signed-copy");
  await page
    .getByRole("button", { name: "Next: Content", exact: true })
    .click();
  await page.getByLabel("Version label", { exact: true }).fill("1.0.0");
  await openDisclosure(page, "Import an existing Agent document");
  await page.getByLabel("Document file", { exact: true }).setInputFiles({
    name: "signed-agent.json",
    mimeType: "application/json",
    buffer: Buffer.from(original),
  });
  await page
    .getByRole("button", { name: "Use loaded document", exact: true })
    .click();
  await expect(page.getByLabel("Agent name", { exact: true })).toBeDisabled();
  await openDisclosure(page, "Advanced Agent Card JSON");
  const raw = page.getByRole("textbox", {
    name: "Standards document (JSON)",
    exact: true,
    includeHidden: true,
  });
  await expect(raw).toHaveValue(original);
  await expect(raw).toHaveAttribute("readonly", "");
  await page
    .locator("summary")
    .filter({ hasText: /^Advanced Agent Card JSON$/ })
    .click();
  const keep = page.waitForEvent("dialog").then(async (dialog) => {
    expect(dialog.message()).toContain("Remove all Card signatures");
    await dialog.dismiss();
  });
  await page
    .getByRole("button", { name: "Create unsigned copy", exact: true })
    .click();
  await keep;
  await expect(raw).toHaveValue(original);
  const remove = page.waitForEvent("dialog").then((dialog) => dialog.accept());
  await page
    .getByRole("button", { name: "Create unsigned copy", exact: true })
    .click();
  await remove;
  await expect(page.getByLabel("Agent name", { exact: true })).toBeEnabled();
  await expect(raw).not.toHaveAttribute("readonly", "");
  await page
    .getByLabel("Agent name", { exact: true })
    .fill("Unsigned edited example");
  const expected = { ...card, name: "Unsigned edited example" } as Partial<
    typeof card
  >;
  delete expected.signatures;
  expect(JSON.parse(await raw.inputValue())).toEqual(expected);
  const response = page.waitForResponse(
    (reply) =>
      reply.request().method() === "POST" &&
      /\/resources\/[^/]+\/versions$/.test(new URL(reply.url()).pathname),
  );
  await page
    .getByRole("button", { name: "Save content draft", exact: true })
    .click();
  await confirmCommand(page);
  const saved = await response;
  expect(saved.status()).toBe(201);
  expect(object(object(await saved.json()).evidence).state).toBe("complete");
  expect(outside).toEqual([]);
});
