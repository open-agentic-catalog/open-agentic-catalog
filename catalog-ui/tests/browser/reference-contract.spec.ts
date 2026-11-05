import AxeBuilder from "@axe-core/playwright";
import { test, expect, login } from "./fixtures";
import { object } from "./json";
import { doubleText } from "./text-layout";
import { required } from "../required";

for (const base of ["root", "subpath"]) {
  test(`${base}: readable API contracts and clipboard actions stay local`, async ({
    page,
    catalog,
  }) => {
    const loaded = page.waitForResponse((response) =>
      new URL(response.url()).pathname.endsWith("/_reference"),
    );
    await login(page, catalog, "local-reader", "/api-reference");
    const projection = object(await (await loaded).json());
    const spec = object(projection.document);
    const search = page.getByRole("searchbox", {
      name: "Search API operations",
    });
    const overview = page.locator(".api-overview");
    await expect(
      overview.getByRole("link", { name: "Service information", exact: true }),
    ).toHaveAttribute("href", `${catalog.basePath}/service`);
    await expect(
      overview.getByRole("link", { name: "My access", exact: true }),
    ).toHaveAttribute("href", `${catalog.basePath}/access`);
    for (const control of [
      page.getByRole("button", { name: "Refresh contract", exact: true }),
      page.getByRole("link", { name: "Download OpenAPI YAML", exact: true }),
    ]) {
      await expect(control.locator("svg")).toHaveAttribute(
        "aria-hidden",
        "true",
      );
      await expect(control.locator("svg")).toHaveAttribute(
        "focusable",
        "false",
      );
    }
    const calls: string[] = [];
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (path.includes("/_catalog/") || path.endsWith("/_reference"))
        calls.push(`${request.method()} ${path}`);
    });
    await page
      .getByRole("combobox", { name: "API area", exact: true })
      .selectOption("Discovery");
    await expect(page.locator(".api-operation")).toHaveCount(6);
    for (const operation of await page.locator(".api-operation").all()) {
      await operation.locator(":scope > summary").click();
      await expect(
        operation.getByText(
          "Anonymous or authenticated alternatives are declared.",
          { exact: true },
        ),
      ).toBeVisible();
      await expect(
        operation.getByText(/Anonymous admission still depends/),
      ).toBeVisible();
      await operation.locator(":scope > summary").click();
    }
    await page
      .getByRole("button", { name: "Clear filters", exact: true })
      .click();
    await search.fill("requestTrustAssessment");
    const operation = page.locator(".api-operation");
    await expect(operation).toHaveCount(1);
    await operation.locator(":scope > summary").click();
    await expect(
      operation.getByText("API authentication is required.", { exact: true }),
    ).toBeVisible();
    await expect(
      operation.getByText("HTTP Bearer token", { exact: true }),
    ).toBeVisible();
    await expect(
      operation.getByText(/Declared requirement values:/),
    ).toContainText("sources.manage");
    await expect(operation.getByText(/Declared OAuth scopes:/)).toHaveCount(0);
    await expect(
      operation.getByText("Idempotency-Key", { exact: true }),
    ).toBeVisible();
    await expect(
      operation.getByText("sourceId", { exact: true }),
    ).toBeVisible();
    await expect(operation.getByText("uuid", { exact: true })).toBeVisible();
    const method = operation.locator(":scope > summary .api-method");
    await expect(method).toHaveText("POST");
    await expect(method).toHaveClass("api-method api-method-post");
    for (const control of [
      operation.getByRole("link", { name: "Link to operation", exact: true }),
      operation.getByRole("button", {
        name: "Copy method and path",
        exact: true,
      }),
      operation.getByRole("button", { name: "Copy operation ID", exact: true }),
    ]) {
      await expect(control.locator("svg")).toHaveAttribute(
        "aria-hidden",
        "true",
      );
      await expect(control.locator("svg")).toHaveAttribute(
        "focusable",
        "false",
      );
    }
    const body = operation.getByRole("region", {
      name: "Operation request body",
      exact: true,
    });
    await expect(body.getByText("Required", { exact: true })).toBeVisible();
    await expect(
      body.getByText("application/json", { exact: true }),
    ).toBeVisible();
    const response = operation
      .locator(".api-response")
      .filter({ has: page.getByRole("heading", { name: "202", exact: true }) });
    await expect(response).toContainText("Job accepted");
    await expect(
      response.getByRole("heading", { name: "Response headers", exact: true }),
    ).toBeVisible();
    await expect(response.getByText("Location", { exact: true })).toBeVisible();
    await expect(operation.locator(".api-complete-data[open]")).toHaveCount(0);
    await page
      .context()
      .grantPermissions(["clipboard-read", "clipboard-write"]);
    await operation
      .getByRole("button", { name: "Copy method and path", exact: true })
      .click();
    const path = "/api/v1/federation/sources/{sourceId}/trust-assessments";
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
      `POST ${path}`,
    );
    await operation
      .getByRole("button", { name: "Copy operation ID", exact: true })
      .click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
      "requestTrustAssessment",
    );
    const request = object(object(object(spec.paths)[path]).post);
    const example = object(
      object(object(request.requestBody).content)["application/json"],
    ).example;
    await body
      .getByRole("button", {
        name: "Copy application/json example",
        exact: true,
      })
      .click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
      JSON.stringify(example),
    );
    const keyField = operation
      .locator(".api-contract-field")
      .filter({ has: page.getByText("Idempotency-Key", { exact: true }) });
    await keyField.locator(".api-complete-data > summary").click();
    await expect(
      keyField.getByRole("group", { name: "JSON document" }),
    ).toContainText("#/components/parameters/IdempotencyKey");
    await keyField.locator(".api-complete-data > summary").click();
    await expect(
      keyField.getByRole("group", { name: "JSON document" }),
    ).toHaveCount(0);
    await operation
      .getByRole("button", {
        name: "View components/securitySchemes/bearerAuth",
        exact: true,
      })
      .click();
    const definitions = page.getByRole("region", {
      name: "Selected definition",
      exact: true,
    });
    await expect(definitions).toBeFocused();
    await expect(
      definitions
        .getByRole("link", { name: "Link to definition", exact: true })
        .locator("svg"),
    ).toHaveAttribute("aria-hidden", "true");
    await expect(
      definitions.getByRole("group", { name: "JSON document" }),
    ).toContainText('"scheme": "bearer"');
    await page
      .getByRole("button", {
        name: "Return to originating operation",
        exact: true,
      })
      .click();
    await expect(operation.locator(":scope > summary")).toBeFocused();
    await expect(search).toHaveValue("requestTrustAssessment");
    await page.getByLabel("Appearance", { exact: true }).click();
    await page
      .getByRole("radio", {
        name: base === "root" ? "Light" : "Dark",
        exact: true,
      })
      .check();
    await page.getByRole("radio", { name: "Compact", exact: true }).check();
    await page.keyboard.press("Escape");
    await page.setViewportSize({ width: 390, height: 844 });
    await doubleText(page);
    await body.scrollIntoViewIfNeeded();
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(390);
    await body
      .getByRole("heading", { name: "Request body", exact: true })
      .scrollIntoViewIfNeeded();
    expect(
      (await body
        .getByRole("heading", { name: "Request body", exact: true })
        .boundingBox())!.y,
    ).toBeGreaterThanOrEqual(
      (await page.getByRole("banner").boundingBox())!.height,
    );
    await page.screenshot({
      path: test.info().outputPath(`api-contract-${base}.png`),
      fullPage: false,
    });
    // Reuse the suite's settled-page scan: sticky overlays can make axe's
    // background calculation indeterminate while the document is scrolled.
    await page.evaluate(async () => {
      await Promise.all(
        document
          .getAnimations()
          .filter(
            (animation) =>
              animation.effect?.getComputedTiming().iterations !== Infinity,
          )
          .map((animation) => animation.finished.catch(() => {})),
      );
      window.scrollTo(0, 0);
    });
    const accessibility = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21aa"])
      .analyze();
    expect(accessibility.violations).toEqual([]);
    expect(accessibility.incomplete).toEqual([]);
    expect(calls).toEqual([]);
  });
}

for (const base of ["root", "subpath"]) {
  test(`${base}: controlled API examples preserve copy boundaries and recover from clipboard denial without calls`, async ({
    page,
    catalog,
  }) => {
    const atLimit = "\u{1f680}".repeat(32768);
    const overLimit = atLimit + "x";
    const document = {
      openapi: "3.1.0",
      info: { title: "Controlled contract", version: "1" },
      components: {
        securitySchemes: { bearer: { type: "http", scheme: "bearer" } },
      },
      paths: {
        "/example/{id}": {
          post: {
            operationId: "exampleOperation",
            security: null,
            parameters: [
              {
                name: "id",
                in: "path",
                required: true,
                schema: { type: "string", pattern: "^example$" },
              },
            ],
            requestBody: {
              required: false,
              content: {
                "text/plain": { example: "  preserve surrounding spaces  " },
              },
            },
            responses: {
              "200": {
                description: "Supplied example",
                content: {
                  "application/json": {
                    examples: {
                      zero: { value: 0 },
                      atLimit: { value: atLimit },
                      overLimit: { value: overLimit },
                      external: {
                        externalValue: "https://unfetched.invalid/example",
                      },
                    },
                  },
                },
              },
              "204": { description: "No body declared" },
            },
          },
        },
      },
    };
    await page.route("**/_reference", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ document, yaml: JSON.stringify(document) }),
      }),
    );
    await login(page, catalog, "local-reader", "/api-reference");
    await expect(
      page.getByText("1 of 1 operations", { exact: true }),
    ).toBeVisible();
    const calls: string[] = [];
    page.on("request", (request) => calls.push(request.url()));
    const operation = page.locator(".api-operation");
    await operation.locator(":scope > summary").click();
    await expect(
      operation.getByText(
        "Authentication details unavailable. Do not assume anonymous access.",
        { exact: true },
      ),
    ).toBeVisible();
    await expect(
      operation.getByText("No API authentication requirement.", {
        exact: true,
      }),
    ).toHaveCount(0);
    await expect(
      operation.getByText(/External example location declared/),
    ).toBeVisible();
    await expect(
      operation.getByText("No response body declared.", { exact: true }),
    ).toBeVisible();
    await page
      .context()
      .grantPermissions(["clipboard-read", "clipboard-write"]);
    await operation
      .getByRole("button", { name: "Copy text/plain example", exact: true })
      .click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
      "  preserve surrounding spaces  ",
    );
    await operation
      .getByRole("button", { name: "Copy zero example", exact: true })
      .click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe("0");
    const atLimitCopy = operation.getByRole("button", {
      name: "Copy atLimit example",
      exact: true,
    });
    await expect(atLimitCopy).toBeEnabled();
    await atLimitCopy.click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
      atLimit,
    );
    const blocked = operation.locator(".api-example").filter({
      has: page.getByRole("button", {
        name: "Copy overLimit example",
        exact: true,
      }),
    });
    await expect(blocked.getByRole("button")).toBeDisabled();
    await expect(blocked).toContainText(
      "Example too large for clipboard controls.",
    );
    await expect(blocked.locator(".code-block")).toHaveCount(0);

    // Use actual Chromium permission refusal, then restore permission and retry.
    // No navigator/clipboard replacement hides this functional boundary.
    await page.context().clearPermissions();
    const permission = await page.context().newCDPSession(page);
    const { targetInfo } = await permission.send("Target.getTargetInfo");
    const browserContextId = required(targetInfo.browserContextId);
    expect(browserContextId).toBeTruthy();
    const copy = operation.getByRole("button", {
      name: "Copy method and path",
      exact: true,
    });
    const reference = operation.locator(".handoff-reference").filter({
      has: page.getByRole("button", {
        name: "Copy method and path",
        exact: true,
      }),
    });
    try {
      await expect(reference).toHaveCount(1);
      await permission.send("Browser.setPermission", {
        permission: { name: "clipboard-write" },
        setting: "denied",
        origin: catalog.origin,
        browserContextId,
      });
      expect(
        await page.evaluate(
          async () =>
            (
              await navigator.permissions.query({
                name: "clipboard-write" as PermissionName,
              })
            ).state,
        ),
      ).toBe("denied");
      await copy.click();
      await expect(reference.getByRole("status")).toHaveText(
        "Copy unavailable. Select the reference and copy it manually.",
      );
      await expect(reference.locator("code")).toHaveText("POST /example/{id}");
      await expect(reference.getByText("Copied.", { exact: true })).toHaveCount(
        0,
      );
      await permission.send("Browser.setPermission", {
        permission: { name: "clipboard-write" },
        setting: "granted",
        origin: catalog.origin,
        browserContextId,
      });
    } finally {
      await permission.detach();
    }
    await page
      .context()
      .grantPermissions(["clipboard-read", "clipboard-write"]);
    await copy.click();
    await expect(reference.getByRole("status")).toHaveText("Copied.");
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
      "POST /example/{id}",
    );
    expect(calls).toEqual([]);
  });
}
