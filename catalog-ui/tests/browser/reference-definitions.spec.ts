import AxeBuilder from "@axe-core/playwright";
import { readFile } from "node:fs/promises";
import { test, expect, login } from "./fixtures";
import { object, text } from "./json";
import { doubleText } from "./text-layout";

for (const base of ["root", "subpath"]) {
  test(`${base}: definition index, schema facts and explicit origin recovery stay local`, async ({
    page,
    catalog,
  }) => {
    const loaded = page.waitForResponse((reply) =>
      new URL(reply.url()).pathname.endsWith("/_reference"),
    );
    await login(page, catalog, "local-reader", "/api-reference");
    const spec = object(object(await (await loaded).json()).document);
    const expected = Object.entries(object(spec.components))
      .flatMap(([kind, collection]) =>
        Object.keys(object(collection)).map((name) => ({ kind, name })),
      )
      .sort((a, b) =>
        a.kind === b.kind
          ? Number(a.name > b.name) - Number(a.name < b.name)
          : Number(a.kind > b.kind) - Number(a.kind < b.kind),
      );
    const calls: string[] = [];
    page.on("request", (request) => calls.push(request.url()));
    await page
      .getByRole("searchbox", { name: "Search API operations" })
      .fill("suspendMembership");
    const operation = page.locator(
      'details[data-operation-id="suspendMembership"]',
    );
    await operation.locator(":scope > summary").click();
    await operation
      .getByRole("button", {
        name: "View components/requestBodies/Reason",
        exact: true,
      })
      .click();
    const detail = page.getByRole("region", {
      name: "Selected definition",
      exact: true,
    });
    await expect(detail).toBeFocused();
    const seen: string[] = [];
    const rows = page.locator(".api-definition-groups button");
    for (let index = 0; index < Math.ceil(expected.length / 25); index++) {
      seen.push(...(await rows.allTextContents()));
      if (index + 1 < Math.ceil(expected.length / 25))
        await page
          .getByRole("button", { name: "Next definitions", exact: true })
          .click();
    }
    expect(seen).toEqual(
      expected.map(({ kind, name }) => `Open ${kind}/${name}`),
    );
    await expect(
      page.getByRole("button", { name: "Next definitions", exact: true }),
    ).toBeDisabled();
    await page
      .getByRole("searchbox", { name: "Search definitions" })
      .fill("Resource");
    await page
      .getByRole("combobox", { name: "Component kind", exact: true })
      .selectOption("schemas");
    const resource = page.getByRole("button", {
      name: "Open schemas/Resource",
      exact: true,
    });
    for (let index = 0; index < 20 && !(await resource.count()); index++)
      await page
        .getByRole("button", { name: "Next definitions", exact: true })
        .click();
    await resource.click();
    await expect(
      detail.getByRole("heading", {
        name: "#/components/schemas/Resource",
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      detail.getByRole("region", {
        name: "Readable schema summary",
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      detail.getByText(/^Property: canonicalIdentifier/),
    ).toBeVisible();
    const complete = object(
      JSON.parse(
        text(
          await detail
            .getByRole("group", { name: "JSON document" })
            .textContent(),
        ),
      ),
    );
    expect(object(complete.properties)).toHaveProperty("canonicalIdentifier");
    await page
      .getByRole("region", { name: "Definition index", exact: true })
      .evaluate((node) => {
        node.scrollTop = 0;
      });
    await page.setViewportSize({ width: 1440, height: 1800 });
    await page.evaluate(() => scrollTo(0, 0));
    await page.screenshot({
      path: test.info().outputPath("api-definition-workspace.png"),
      fullPage: false,
    });
    await page.setViewportSize({ width: 1440, height: 900 });
    const indexRegion = page.getByRole("region", {
      name: "Definition index",
      exact: true,
    });
    await indexRegion.evaluate((node) => {
      node.scrollTop = 0;
    });
    await indexRegion.focus();
    await page.keyboard.press("ArrowDown");
    await expect
      .poll(() => indexRegion.evaluate((node) => node.scrollTop))
      .toBeGreaterThan(0);
    await page
      .getByRole("button", {
        name: "Return to originating operation",
        exact: true,
      })
      .click();
    await expect(operation.locator(":scope > summary")).toBeFocused();
    await page
      .getByRole("searchbox", { name: "Search API operations" })
      .fill("reactivateMembership");
    await page.getByRole("button", { name: /^Browse definitions/ }).click();
    await expect(
      page.getByRole("searchbox", { name: "Search definitions" }),
    ).toHaveValue("Resource");
    await expect(
      page.getByRole("button", {
        name: "Return to originating operation",
        exact: true,
      }),
    ).toBeDisabled();
    await page
      .getByRole("button", {
        name: "Reveal originating operation",
        exact: true,
      })
      .focus();
    await page.keyboard.press("Enter");
    await expect(operation.locator(":scope > summary")).toBeFocused();
    await expect(
      page.getByRole("searchbox", { name: "Search API operations" }),
    ).toHaveValue("");
    await page.getByRole("button", { name: /^Browse definitions/ }).click();
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
    await expect(detail).toBeVisible();
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(390);
    await detail
      .getByRole("link", { name: "Link to definition", exact: true })
      .click();
    await expect(detail).toBeFocused();
    expect((await detail.boundingBox())!.y).toBeGreaterThanOrEqual(
      (await page.getByRole("banner").boundingBox())!.height,
    );
    await page.evaluate(async () => {
      await Promise.all(
        document
          .getAnimations()
          .filter((a) => Number.isFinite(a.effect?.getComputedTiming().endTime))
          .map((a) => a.finished.catch(() => {})),
      );
      window.scrollTo(0, 0);
    });
    const accessibility = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21aa"])
      .analyze();
    expect(accessibility.violations).toEqual([]);
    expect(accessibility.incomplete).toEqual([]);
    expect(calls).toEqual([]);
    await page
      .getByRole("searchbox", { name: "Search definitions" })
      .fill("private definition criteria");
    await page.getByTitle("Account", { exact: true }).click();
    await page.getByRole("button", { name: "Sign out", exact: true }).click();
    await expect(page.locator(".api-definitions")).toHaveCount(0);
    await expect(
      page.getByRole("group", { name: "JSON document" }),
    ).toHaveCount(0);
    await expect(
      page.getByText("private definition criteria", { exact: true }),
    ).toHaveCount(0);
  });
}

test("same-version refresh retains private criteria and exact YAML, and explains removed targets", async ({
  page,
  catalog,
  network,
}) => {
  network.expectFailure("GET", "/_reference", 502);
  const initial = {
    openapi: "3.1.0",
    info: { title: "Initial contract", version: "same" },
    tags: [{ name: "Kept" }],
    components: {
      schemas: {
        A: {
          type: "object",
          properties: {
            stop: false,
            flag: { type: "boolean", default: false },
            recursive: { $ref: "#/components/schemas/A" },
          },
          required: ["flag"],
          oneOf: [false, { type: "object" }],
        },
      },
      "extra/~": { "zero/~": 0 },
    },
    paths: {
      "/kept": {
        get: {
          summary: "Kept operation",
          tags: ["Kept"],
          responses: {
            "200": {
              description: "Result",
              content: {
                "application/json": {
                  schema: { $ref: "#/components/schemas/A" },
                },
              },
            },
          },
        },
      },
    },
  };
  const replacement = {
    ...initial,
    info: { title: "Replaced contract", version: "same" },
    components: { ...initial.components, schemas: { A: { type: "integer" } } },
  };
  const removed = {
    openapi: "3.1.0",
    info: { title: "Targets removed", version: "same" },
    components: {},
    paths: {
      "/new": { get: { tags: ["New area"], summary: "New operation" } },
    },
  };
  let attempts = 0;
  let release!: () => void;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/_reference", async (route) => {
    attempts++;
    if (attempts === 3) {
      await route.fulfill({ status: 502, body: "Unavailable" });
      return;
    }
    if (attempts === 2) await pending;
    const document =
      attempts === 1 ? initial : attempts === 2 ? replacement : removed;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ document, yaml: JSON.stringify(document) }),
    });
  });
  await login(page, catalog, "local-reader", "/api-reference");
  await expect(
    page.getByText("1 of 1 operations", { exact: true }),
  ).toBeVisible();
  const productCalls: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes("/_catalog/")) productCalls.push(request.url());
  });
  await page.locator(".api-operation > summary").click();
  await page
    .getByRole("button", { name: "View components/schemas/A", exact: true })
    .click();
  const detail = page.getByRole("region", {
    name: "Selected definition",
    exact: true,
  });
  await page.evaluate(() => {
    const id =
      "api-definition-" + encodeURIComponent("#/components/schemas/A/oneOf/0");
    history.pushState(
      history.state as unknown,
      "",
      `#${encodeURIComponent(id)}`,
    );
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await expect(
    detail.getByText("false: rejects every value", { exact: true }),
  ).toBeVisible();
  await page.goBack();
  await expect(
    detail.getByRole("heading", {
      name: "#/components/schemas/A",
      exact: true,
    }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Browse endpoints", exact: true })
    .click();
  await page
    .getByRole("searchbox", { name: "Search API operations" })
    .fill("private zero-match text");
  await page
    .getByRole("combobox", { name: "HTTP method", exact: true })
    .selectOption("POST");
  await page
    .getByRole("combobox", { name: "API area", exact: true })
    .selectOption("Kept");
  await page.getByRole("button", { name: /^Browse definitions/ }).click();
  await page
    .getByRole("searchbox", { name: "Search definitions" })
    .fill("unmatched definition text");
  await page
    .getByRole("combobox", { name: "Component kind", exact: true })
    .selectOption("schemas");
  const download = page.getByRole("link", {
    name: "Download OpenAPI YAML",
    exact: true,
  });
  const before = await download.getAttribute("href");
  await page
    .getByRole("button", { name: "Refresh contract", exact: true })
    .click();
  await expect(
    page.getByText(/Last loaded API contract; refreshing/),
  ).toBeVisible();
  await expect(download).toHaveAttribute("href", before!);
  release();
  await expect(
    page.getByRole("heading", { name: "Replaced contract", exact: true }),
  ).toBeVisible();
  await expect(
    detail.getByRole("group", { name: "JSON document" }),
  ).toContainText('"type": "integer"');
  await expect(
    page.getByRole("searchbox", { name: "Search definitions" }),
  ).toHaveValue("unmatched definition text");
  await expect(download).not.toHaveAttribute("href", before!);
  const after = await download.getAttribute("href");
  const file = page.waitForEvent("download");
  await download.click();
  const path = await (await file).path();
  expect(await readFile(path!, "utf8")).toBe(JSON.stringify(replacement));
  await page
    .getByRole("button", { name: "Refresh contract", exact: true })
    .click();
  await expect(
    page.getByText(/Last loaded API contract; refresh failed/),
  ).toBeVisible();
  await expect(download).toHaveAttribute("href", after!);
  await expect(
    detail.getByRole("group", { name: "JSON document" }),
  ).toContainText('"type": "integer"');
  await page
    .getByRole("button", { name: "Refresh contract", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Targets removed", exact: true }),
  ).toBeVisible();
  await expect(detail).toHaveCount(0);
  await expect(page.getByText(/selected definition was removed/)).toBeVisible();
  await expect(
    page.getByText(/originating operation was removed/),
  ).toBeVisible();
  await expect(page.getByText(/selected API area was removed/)).toBeVisible();
  await page
    .getByRole("button", { name: "Browse endpoints", exact: true })
    .click();
  await expect(
    page.getByRole("searchbox", { name: "Search API operations" }),
  ).toHaveValue("private zero-match text");
  await expect(
    page.getByRole("combobox", { name: "HTTP method", exact: true }),
  ).toHaveValue("POST");
  await expect(
    page.getByRole("combobox", { name: "API area", exact: true }),
  ).toHaveValue("");
  expect(
    page.url() +
      JSON.stringify(await page.evaluate(() => history.state as unknown)),
  ).not.toContain("private zero-match text");
  expect(productCalls).toEqual([]);
  expect(attempts).toBe(4);
});
