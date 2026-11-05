import { object, array, text } from "./json";
import { readFile } from "node:fs/promises";
import { test, expect, login } from "./fixtures";

test("API reference recovers from unavailable and malformed replies and service details stay read-only", async ({
  page,
  catalog,
  network,
}) => {
  network.expectFailure("GET", "/_reference", 502);
  let attempts = 0;
  await page.route("**/_reference", async (route) => {
    attempts++;
    if (attempts === 1) {
      await route.fulfill({ status: 502, body: "not a contract" });
    } else if (attempts === 3) {
      // Break decoding, not the real service contract. A failed refresh must
      // retain the last loaded download and allow an explicit reload of real bytes.
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: "{",
      });
    } else await route.continue();
  });
  await login(page, catalog, "local-reader", "/api-reference");
  await expect(page.getByRole("alert")).toContainText(
    "The request could not be completed. Please try again.",
  );
  await expect(
    page.getByRole("link", { name: "Download OpenAPI YAML", exact: true }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "Reload", exact: true }).click();
  await expect(
    page.getByText("198 of 198 operations", { exact: true }),
  ).toBeVisible();
  const download = page.getByRole("link", {
    name: "Download OpenAPI YAML",
    exact: true,
  });
  await expect(download).toHaveAttribute("href", /^blob:/);
  const previousDownload = await download.getAttribute("href");
  await page
    .getByRole("button", { name: "Refresh contract", exact: true })
    .click();
  await expect(page.getByRole("alert")).toContainText(
    "The request could not be completed. Please try again.",
  );
  await expect(download).toHaveAttribute("href", previousDownload!);
  await expect(
    page.getByText(/Last loaded API contract; refresh failed/),
  ).toBeVisible();
  await page.getByRole("button", { name: "Reload", exact: true }).click();
  await expect(
    page.getByText("198 of 198 operations", { exact: true }),
  ).toBeVisible();
  await expect(download).toHaveAttribute("href", /^blob:/);
  await expect(download).toHaveAttribute("href", previousDownload!);
  expect(attempts).toBe(4);
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(
    page.getByText(/Expanding an operation shows its contract/),
  ).toBeVisible();

  const writes: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes("/_catalog/") && request.method() !== "GET")
      writes.push(request.url());
  });
  const capabilities = page.waitForResponse(
    (reply) =>
      new URL(reply.url()).pathname === "/_catalog/api/v1/capabilities",
  );
  const version = page.waitForResponse(
    (reply) =>
      new URL(reply.url()).pathname === "/_catalog/api/v1/service-information",
  );
  await page
    .locator(".api-overview")
    .getByRole("link", { name: "Service information", exact: true })
    .click();
  const info = object(await (await capabilities).json());
  const build = object(await (await version).json());
  const panel = (name: string) =>
    page
      .locator("section")
      .filter({ has: page.getByRole("heading", { name, exact: true }) });
  await page.getByText("API build details", { exact: true }).click();
  await page.getByText("UI build details", { exact: true }).click();
  await expect(
    page.getByText(
      "Uptime was measured with the last response. Refresh to update it.",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(panel("Catalog API")).toContainText(text(build.version));
  await expect(panel("Catalog API")).toContainText(text(build.ardProfile));
  await expect(panel("Catalog API")).toContainText(text(build.instanceId));
  await expect(panel("Catalog API").locator("time").first()).toHaveAttribute(
    "datetime",
    text(build.startedAt),
  );
  expect(build.uptimeSeconds).toBeGreaterThanOrEqual(0);
  expect(build.role).toBe("all");
  const host = await page.evaluate(async () => {
    const reply = await fetch("/_status", {
      headers: { "X-Catalog-Request": "1" },
      credentials: "same-origin",
    });
    if (!reply.ok) throw new Error("UI host read failed");
    return reply.json() as Promise<unknown>;
  });
  const hostBuild = object(host);
  expect(hostBuild.instanceId).not.toBe(build.instanceId);
  expect(hostBuild.role).toBe("ui");
  await expect(panel("UI host")).toContainText(text(hostBuild.instanceId));
  await expect(panel("UI host").locator("time").first()).toHaveAttribute(
    "datetime",
    text(hostBuild.startedAt),
  );
  await expect(panel("Catalog API")).toContainText("Less than a minute");
  await expect(panel("UI host")).toContainText(text(hostBuild.version));
  await expect(panel("UI host")).toContainText(text(hostBuild.commit));
  await expect(panel("UI host")).toContainText(text(hostBuild.sourceState));
  await page.setViewportSize({ width: 1440, height: 1600 });
  await panel("Catalog API").evaluate((node) =>
    window.scrollBy(0, node.getBoundingClientRect().top - 100),
  );
  const apiBox = await panel("Catalog API").boundingBox();
  const uiBox = await panel("UI host").boundingBox();
  if (!apiBox || !uiBox) throw new Error("Process information is not visible");
  await page.screenshot({
    path: test.info().outputPath("service-information-processes.png"),
    clip: {
      x: apiBox.x,
      y: apiBox.y,
      width: apiBox.width,
      height: uiBox.y + uiBox.height - apiBox.y,
    },
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await expect(panel("Capabilities")).toContainText(
    array(info.resourceKinds)
      .map(
        (kind) =>
          (
            ({
              agent: "Agent",
              mcp_server: "MCP Server",
              skill: "Skill",
              plugin: "Plugin",
            }) as Record<string, string>
          )[text(kind)],
      )
      .join(", "),
  );
  await expect(panel("Capabilities")).toContainText(
    text(object(info.search).profile),
  );
  await expect(panel("Capabilities")).toContainText("Webhooks");
  await expect(panel("Capabilities")).toContainText("Service activity API");
  expect(text(object(info.optionalCapabilities).serviceActivity)).toBe(
    "disabled",
  );
  await expect(page.getByText("serviceActivity", { exact: true })).toHaveCount(
    0,
  );
  await expect(
    panel("Capabilities")
      .locator("dt")
      .filter({ hasText: /^Webhooks$/ })
      .locator("+ dd"),
  ).toHaveText("Turned off");
  await expect(page.getByRole("textbox")).toHaveCount(0);
  await expect(page.getByRole("combobox")).toHaveCount(0);
  const refreshed = page.waitForResponse(
    (reply) =>
      new URL(reply.url()).pathname === "/_catalog/api/v1/capabilities",
  );
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  expect((await refreshed).status()).toBe(200);
  await expect(panel("Capabilities")).toContainText(
    text(object(info.search).profile),
  );
  expect(writes).toEqual([]);
});

test("reader browses the live API reference and downloads its exact contract under a subpath", async ({
  page,
  catalog,
}) => {
  await login(page, catalog);
  await expect(
    page.getByRole("link", { name: "Members", exact: true }),
  ).toHaveCount(0);
  await page.getByRole("link", { name: "API reference", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "API reference", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("198 of 198 operations", { exact: true }),
  ).toBeVisible();
  await page.reload();
  await expect(
    page.getByText("198 of 198 operations", { exact: true }),
  ).toBeVisible();
  const connected = object(
    await page.evaluate(async (basePath) => {
      const response = await fetch(`${basePath}/_reference`, {
        headers: { "X-Catalog-Request": "1" },
      });
      if (!response.ok) throw new Error("Connected contract unavailable");
      return response.json() as Promise<unknown>;
    }, catalog.basePath),
  );
  const upstreamRequests: string[] = [];
  page.on("request", (request) => {
    if (
      request.url().includes("/_catalog/") ||
      request.url().includes("/_reference")
    )
      upstreamRequests.push(request.url());
  });
  const paths = object(object(connected.document).paths);
  const expectedOperations = Object.entries(paths).flatMap(([path, item]) =>
    Object.entries(object(item))
      .filter(([method]) =>
        [
          "get",
          "put",
          "post",
          "delete",
          "patch",
          "head",
          "options",
          "trace",
        ].includes(method),
      )
      .map(([method, value]) => ({
        method: method.toUpperCase(),
        path,
        id: text(object(value).operationId),
        tags: array(object(value).tags).map(text),
      })),
  );
  const rendered = await page
    .locator(".api-operation")
    .evaluateAll((elements) =>
      elements.map((element) => ({
        method: element.querySelector(".api-method")!.textContent,
        path: element.querySelector("summary code")!.textContent,
        id: (element as HTMLElement).dataset.operationId,
      })),
    );
  const declared = array(object(connected.document).tags).map((value) =>
    text(object(value).name),
  );
  const homes = [...new Set(declared)];
  const displayed = homes.flatMap((home) =>
    expectedOperations.filter((operation) => operation.tags.at(-1) === home),
  );
  expect(rendered).toEqual(
    displayed.map(({ method, path, id }) => ({ method, path, id })),
  );
  expect(
    new Set(
      rendered.map((operation) => `${operation.method}:${operation.path}`),
    ).size,
  ).toBe(expectedOperations.length);
  expect(
    expectedOperations.filter((operation) => operation.tags.length > 1),
  ).toHaveLength(23);
  const sections = page.locator(".api-area-section");
  expect(
    await sections.locator(".api-area-summary h2").allTextContents(),
  ).toEqual(homes);
  for (const [position, home] of homes.entries()) {
    const expected = expectedOperations.filter(
      (operation) => operation.tags.at(-1) === home,
    );
    const section = sections.nth(position);
    expect(
      await section
        .locator(".api-operation")
        .evaluateAll((nodes) =>
          nodes.map((node) => (node as HTMLElement).dataset.operationId),
        ),
    ).toEqual(expected.map((operation) => operation.id));
    const declaration = array(object(connected.document).tags).find(
      (value) => text(object(value).name) === home,
    );
    await expect(section).toContainText(text(object(declaration).description));
  }
  const ids = await page
    .locator("[id]")
    .evaluateAll((nodes) => nodes.map((node) => node.id));
  expect(new Set(ids).size).toBe(ids.length);
  await page.evaluate(() => scrollTo(0, 0));
  await page.setViewportSize({ width: 1440, height: 1400 });
  await page.screenshot({
    path: test.info().outputPath("api-areas-overview.png"),
    fullPage: false,
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  for (const area of [
    "Identity",
    "Groups and members",
    "Roles",
    "Access assignments",
    "My access",
    "Catalog MCP",
  ]) {
    await page
      .getByRole("combobox", { name: "API area", exact: true })
      .selectOption(area);
    const expected = displayed.filter((operation) =>
      operation.tags.includes(area),
    );
    const ids = await page
      .locator(".api-operation")
      .evaluateAll((elements) =>
        elements.map((element) => (element as HTMLElement).dataset.operationId),
      );
    expect(ids).toEqual(expected.map((operation) => operation.id));
    expect(ids.length).toBeGreaterThan(0);
    if (area === "Catalog MCP") expect(ids).toHaveLength(7);
    if (area === "Identity") expect(ids).toHaveLength(49);
  }
  await page
    .getByRole("combobox", { name: "API area", exact: true })
    .selectOption("Groups and members");
  // Re-fetch the connected contract, then select its current Groups area.
  // Refresh retains the compatible exact area; browsing executes no group API.
  const refreshedContract = page.waitForResponse((response) =>
    new URL(response.url()).pathname.endsWith("/_reference"),
  );
  await page
    .getByRole("button", { name: "Refresh contract", exact: true })
    .click();
  expect((await refreshedContract).status()).toBe(200);
  await expect(
    page.getByText(
      `${expectedOperations.filter((operation) => operation.tags.includes("Groups and members")).length} of 198 operations`,
      { exact: true },
    ),
  ).toBeVisible();
  await expect(
    page.getByRole("combobox", { name: "API area", exact: true }),
  ).toHaveValue("Groups and members");
  await expect
    .poll(() =>
      page
        .locator(".api-operation")
        .evaluateAll((elements) =>
          elements.map(
            (element) => (element as HTMLElement).dataset.operationId,
          ),
        ),
    )
    .toEqual(
      expectedOperations
        .filter((operation) => operation.tags.includes("Groups and members"))
        .map((operation) => operation.id),
    );
  await page
    .getByLabel("Search API operations", { exact: true })
    .fill("listAccessGroupMembers");
  await expect(page.locator(".api-operation")).toHaveCount(1);
  await page.locator(".api-operation > summary").click();
  const groupOperation = page.locator(".api-operation");
  // The summary already contains groupId; wait for the lazy contract content.
  for (const name of ["Authentication", "Parameters", "Responses"]) {
    await expect(
      groupOperation.getByRole("heading", { name, exact: true }),
    ).toBeVisible();
  }
  await expect(groupOperation).toContainText("groupId");
  await page.evaluate(() => window.scrollTo(0, 0));
  await expect(
    page.getByRole("heading", { name: "API reference", exact: true }),
  ).toBeInViewport();
  await page.screenshot({
    path: test.info().outputPath("api-groups-reference.png"),
    fullPage: true,
  });
  await page.getByLabel("Search API operations", { exact: true }).fill("");
  await page
    .getByRole("combobox", { name: "API area", exact: true })
    .selectOption("Identity");
  await page
    .getByLabel("Search API operations", { exact: true })
    .fill("suspendMembership");
  await expect(
    page.getByText("1 of 198 operations", { exact: true }),
  ).toBeVisible();
  const operation = page.locator(".api-operation");
  await operation.locator(":scope > summary").click();
  await expect(operation).toContainText("membershipId");
  await expect(operation).toContainText("Idempotency-Key");
  await expect(operation).toContainText("If-Match");
  await operation
    .getByRole("button", {
      name: "View components/requestBodies/Reason",
      exact: true,
    })
    .click();
  await expect(
    page.getByRole("combobox", {
      name: "Schema or shared definition",
      exact: true,
    }),
  ).toHaveValue("#/components/requestBodies/Reason");
  await expect(
    page.getByRole("region", { name: "Selected definition" }),
  ).toBeFocused();
  // Repeated known links focus the selected detail without a duplicate history entry.
  await page
    .getByRole("link", { name: "Link to definition", exact: true })
    .click();
  await expect(
    page.getByRole("region", { name: "Selected definition" }),
  ).toBeFocused();
  // The selector itself keeps keyboard focus while browsing definitions.
  const selector = page.getByRole("combobox", {
    name: "Schema or shared definition",
    exact: true,
  });
  await page
    .getByRole("searchbox", { name: "Search definitions", exact: true })
    .fill("Resource");
  await page
    .getByRole("combobox", { name: "Component kind", exact: true })
    .selectOption("schemas");
  for (
    let index = 0;
    index < 20 &&
    !(await page
      .getByRole("button", { name: "Open schemas/Resource", exact: true })
      .count());
    index++
  )
    await page
      .getByRole("button", { name: "Next definitions", exact: true })
      .click();
  await selector.focus();
  await selector.selectOption("#/components/schemas/Resource");
  await expect(selector).toBeFocused();
  await expect(
    page.getByRole("heading", {
      name: "#/components/schemas/Resource",
      exact: true,
    }),
  ).toBeVisible();
  const definitions = page.getByRole("region", {
    name: "Selected definition",
  });
  const schemaCode = definitions.getByRole("group", { name: "JSON document" });
  const resourceText = (await schemaCode.textContent())!;
  expect(object(JSON.parse(resourceText)).properties).toHaveProperty(
    "canonicalIdentifier",
  );
  await page.setViewportSize({ width: 390, height: 740 });
  const wrap = definitions.getByRole("button", {
    name: "Wrap lines",
    exact: true,
  });
  await wrap.focus();
  await page.keyboard.press("Space");
  await expect(wrap).toHaveAttribute("aria-pressed", "false");
  await expect(schemaCode).toHaveCSS("white-space", "pre");
  await page.keyboard.press("Tab");
  await expect(schemaCode).toBeFocused();
  const initialScroll = await schemaCode.evaluate((element) => ({
    top: element.scrollTop,
    left: element.scrollLeft,
    verticalOverflow: element.scrollHeight - element.clientHeight,
    horizontalOverflow: element.scrollWidth - element.clientWidth,
  }));
  expect(initialScroll.verticalOverflow).toBeGreaterThan(0);
  expect(initialScroll.horizontalOverflow).toBeGreaterThan(0);
  await page.keyboard.press("ArrowDown");
  await expect
    .poll(() => schemaCode.evaluate((element) => element.scrollTop))
    .toBeGreaterThan(initialScroll.top);
  await page.keyboard.press("ArrowRight");
  await expect
    .poll(() => schemaCode.evaluate((element) => element.scrollLeft))
    .toBeGreaterThan(initialScroll.left);
  expect(await schemaCode.textContent()).toBe(resourceText);
  await page.keyboard.press("Tab");
  await expect(
    definitions.getByRole("button", {
      name: "View components/schemas/ResourceKind",
      exact: true,
    }),
  ).toBeFocused();
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await wrap.click();
  await expect(schemaCode).toHaveAttribute("data-wrap", "true");
  // Reusing the viewer for another definition must not leave old schema text.
  await page
    .getByRole("searchbox", { name: "Search definitions", exact: true })
    .fill("Reason");
  await page
    .getByRole("combobox", { name: "Component kind", exact: true })
    .selectOption("requestBodies");
  for (
    let index = 0;
    index < 20 &&
    !(await page
      .getByRole("button", { name: "Open requestBodies/Reason", exact: true })
      .count());
    index++
  )
    await page
      .getByRole("button", { name: "Next definitions", exact: true })
      .click();
  await selector.selectOption("#/components/requestBodies/Reason");
  await expect(schemaCode).not.toHaveText(resourceText);
  expect(JSON.parse((await schemaCode.textContent())!)).toHaveProperty(
    "content",
  );
  await page.setViewportSize({ width: 1440, height: 900 });
  await page
    .getByRole("button", { name: "Browse endpoints", exact: true })
    .click();
  await page
    .getByLabel("Search API operations", { exact: true })
    .fill("nothing-matches-this");
  await expect(
    page.getByText(
      "No operations match. Change or clear search, HTTP method or API area.",
    ),
  ).toBeVisible();
  const downloaded = page.waitForEvent("download");
  await page
    .getByRole("link", { name: "Download OpenAPI YAML", exact: true })
    .click();
  const download = await downloaded;
  expect(download.suggestedFilename()).toBe("control-v1.yaml");
  const path = await download.path();
  expect(path).not.toBeNull();
  expect(await readFile(path!, "utf8")).toBe(
    await readFile(
      new URL("../../../api/openapi/control-v1.yaml", import.meta.url),
      "utf8",
    ),
  );
  expect(upstreamRequests).toEqual([
    `${catalog.origin}${catalog.basePath}/_reference`,
  ]);
  await page.setViewportSize({ width: 390, height: 740 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  // Direct routes must load their split chunks without disclosing private data.
  // These are permission/routing checks, not federation lifecycle coverage.
  const privateReads: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes("/_catalog/")) privateReads.push(request.url());
  });
  for (const [route, heading] of [
    ["source-snapshots/snapshot-a", "Source snapshot"],
    ["observations", "Source observations"],
    [
      "observation-group?identifier=urn:example:agent",
      "Observation contributions",
    ],
    ["federation-search", "Federation diagnostics"],
  ] as const) {
    await page.goto(`${catalog.origin}${catalog.basePath}/${route}`);
    await expect(
      page.getByRole("heading", { name: heading, exact: true }),
    ).toBeVisible();
    await expect(page.getByRole("alert")).toHaveText(
      route === "federation-search"
        ? "The source.read and federation.discover permissions are required. When scopes are enforced, sources.read is also required."
        : "The source.read permission is required. When scopes are enforced, sources.read is also required.",
    );
  }
  expect(privateReads).toEqual([]);
});

test("administrator reaches management pages and disabled webhooks without hidden mutations", async ({
  page,
  catalog,
}) => {
  // Start in the workspace under test: the Catalog's initial POST Search is
  // a read and must not race this management-only no-write observation.
  await login(page, catalog, "local-admin", "/members");
  await expect(
    page.getByRole("heading", { name: "Members and access", exact: true }),
  ).toBeVisible();
  const navigation = page.getByRole("navigation", {
    name: "Workspace",
    exact: true,
  });
  for (const [width, height] of [
    [1024, 768],
    [1366, 768],
    [1366, 900],
  ] as const) {
    await page.setViewportSize({ width, height });
    // Expanded policy administration can exceed this viewport. Every
    // destination remains keyboard-reachable within the bounded navigation.
    await navigation
      .getByRole("link", { name: "API reference", exact: true })
      .focus();
    await expect(
      navigation.getByRole("link", { name: "API reference", exact: true }),
    ).toBeInViewport();
    await expect(page.getByLabel("Account", { exact: true })).toBeInViewport();
  }
  // Short windows retain scrolling and keyboard access to the last destination.
  await page.setViewportSize({ width: 1366, height: 480 });
  const lastLink = navigation.getByRole("link", {
    name: "API reference",
    exact: true,
  });
  await expect
    .poll(() =>
      navigation.evaluate(
        (element) => element.scrollHeight - element.clientHeight,
      ),
    )
    .toBeGreaterThan(0);
  await navigation
    .getByRole("link", { name: "Documentation", exact: true })
    .focus();
  for (const name of [
    "Getting started",
    "About",
    "Troubleshooting",
    "API reference",
  ]) {
    await page.keyboard.press("Tab");
    const destination = navigation.getByRole("link", { name, exact: true });
    await expect(destination).toBeFocused();
    await expect(destination).toBeInViewport();
  }
  await expect(lastLink).toBeFocused();
  await expect(page.getByLabel("Account", { exact: true })).toBeInViewport();
  await page.setViewportSize({ width: 1440, height: 900 });
  const writes: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes("/_catalog/") && request.method() !== "GET")
      writes.push(request.url());
  });
  for (const [link, heading] of [
    ["Members", "Members and access"],
    ["Federation sources", "Federation sources"],
    ["Discovery publication", "Discovery publication"],
    ["Audit", "Audit investigation"],
    ["Namespaces", "Namespaces"],
    ["Publishers", "Publishers"],
    ["Jobs", "Jobs"],
    ["Webhooks", "Webhooks"],
  ] as const) {
    await page
      .getByRole("navigation", { name: "Workspace", exact: true })
      .getByRole("link", { name: link, exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: heading, exact: true }).first(),
    ).toBeVisible();
    await expect(page.getByRole("alert")).toHaveCount(0);
  }
  await expect(
    page.getByText(
      "Webhooks are turned off. Ask your operator to enable them.",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Create webhook", exact: true }),
  ).toHaveCount(0);
  await navigation
    .getByRole("link", { name: "Search indexing", exact: true })
    .click();
  await expect(page).toHaveURL(catalog.origin + "/service#search-indexing");
  await expect(
    page.getByRole("heading", { name: "Search indexing", exact: true }),
  ).toBeInViewport();
  await expect(
    page.getByText(
      "Semantic indexing is turned off. Keyword search is available. Ask your operator to enable semantic indexing.",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Rebuild search index", exact: true }),
  ).toHaveCount(0);

  await page
    .getByRole("link", { name: "Browse API reference", exact: true })
    .click();
  await expect(
    page.getByText("198 of 198 operations", { exact: true }),
  ).toBeVisible();
  expect(writes).toEqual([]);
});
