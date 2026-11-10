import { readFile } from "node:fs/promises";
import { test, expect, login, signOut } from "./fixtures";
import { tabTo } from "./keyboard";

for (const deployment of ["root", "subpath"]) {
  test(`startup shows held progress and public Help/legal works at ${deployment}`, async ({
    page,
    catalog,
  }) => {
    let release!: () => void;
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    await page.route(
      "**/runtime.json",
      async (route) => {
        const response = await route.fetch();
        await held;
        await route.fulfill({ response });
      },
      { times: 1 },
    );
    try {
      await page.goto(catalog.origin + catalog.basePath + "/about");
      await expect(
        page.getByRole("heading", { name: "Starting Catalog" }),
      ).toBeVisible();
      await expect(page.getByRole("status")).toHaveText(
        "Loading UI settings...",
      );
      await expect(
        page.getByRole("link", { name: "Documentation", exact: true }),
      ).toHaveCount(0);
    } finally {
      release();
    }
    await expect(
      page.getByRole("heading", { name: "About", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("Author: Mihai Criveti", { exact: true }),
    ).toHaveCount(1);
    await expect(
      page.getByRole("heading", { name: "Connected builds" }),
    ).toHaveCount(0);
    await expect(page.getByTitle("Account", { exact: true })).toHaveCount(0);
    const manual = page.getByRole("link", {
      name: "Documentation",
      exact: true,
    });
    await expect(manual.locator("svg")).toHaveAttribute("aria-hidden", "true");
    await expect(manual.locator("svg")).toHaveAttribute("focusable", "false");
    await expect(manual).toHaveAccessibleDescription("Opens in a new tab");
    const license = await page.request.get(
      catalog.origin + catalog.basePath + "/legal/LICENSE.txt",
    );
    expect(license.status()).toBe(200);
    expect(await license.body()).toEqual(
      await readFile(new URL("../../../LICENSE", import.meta.url)),
    );
    const notices = await page.request.get(
      catalog.origin + catalog.basePath + "/legal/third-party-notices.txt",
    );
    expect(notices.status()).toBe(200);
    expect(notices.headers()["content-type"]).toBe("text/plain; charset=utf-8");
    expect(await notices.text()).toContain(
      await readFile(
        new URL("../../node_modules/react/LICENSE", import.meta.url),
        "utf8",
      ),
    );
    await expect(
      page.locator("main li > svg[aria-hidden=true][focusable=false]"),
    ).toHaveCount(4);
    await page.screenshot({
      path: test.info().outputPath("about.png"),
      fullPage: true,
    });
    await page
      .getByRole("link", { name: "Getting started", exact: true })
      .first()
      .click();
    await expect(
      page.getByRole("heading", { name: "Getting started", exact: true }),
    ).toBeVisible();
    await expect(page).toHaveTitle("Getting started | Open Agentic Catalog");
    await expect(
      page.getByRole("link", { name: "Open Catalog", exact: true }),
    ).toHaveCount(0);
    await page.screenshot({
      path: test.info().outputPath("getting-started.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 320, height: 740 });
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(320);
  });
}

test("delayed startup options cannot strand logout or replace the current roster", async ({
  page,
  catalog,
}) => {
  await login(page, catalog);
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  let optionsStarted!: () => void;
  const started = new Promise<void>((resolve) => {
    optionsStarted = resolve;
  });
  await page.route(
    "**/auth/options",
    async (route) => {
      const response = await route.fetch();
      optionsStarted();
      await held;
      await route.fulfill({ response });
    },
    { times: 1 },
  );
  try {
    await page.reload();
    await started;
    await expect(page.getByTitle("Account", { exact: true })).toBeVisible();
    await signOut(page);
    await expect(
      page.getByRole("heading", {
        name: "Find Agents, MCP Servers, Skills and Plugins.",
      }),
    ).toBeFocused();
    await expect(
      page.locator('[data-login-alias="local-reader"]'),
    ).toBeEnabled();
  } finally {
    release();
  }
  await page.locator('[data-login-alias="local-reader"]').focus();
  await page.keyboard.press("Enter");
  await expect(page.getByTitle("Account", { exact: true })).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Catalog", exact: true }),
  ).toBeFocused();
});

test("quick navigation supports keyboard, narrow layout and ordinary guarded links under a subpath", async ({
  page,
  catalog,
}) => {
  await login(page, catalog);
  const opener = page.getByRole("button", {
    name: "Quick navigation",
    exact: true,
  });
  // The shortcut deliberately ignores editable fields. Establish its actual
  // keyboard target instead of depending on incidental post-login focus.
  await opener.focus();
  await expect(opener).toBeFocused();
  await page.keyboard.press("Control+k");
  const chooser = page.getByRole("dialog", { name: "Quick navigation" });
  await expect(chooser).toBeVisible();
  await expect(
    chooser.getByRole("textbox", { name: "Find a page" }),
  ).toBeFocused();
  await chooser.getByRole("textbox").fill("no such private record");
  await expect(chooser.getByRole("status")).toHaveText("No matching pages.");
  await page.keyboard.press("Escape");
  await expect(
    page.getByRole("button", { name: "Quick navigation", exact: true }),
  ).toBeFocused();
  const search = page.getByRole("searchbox", {
    name: "Search Catalog",
    exact: true,
  });
  await search.focus();
  await page.keyboard.press("Control+k");
  await expect(chooser).not.toBeVisible();
  await page.setViewportSize({ width: 320, height: 740 });
  await tabTo(
    page,
    page.getByRole("button", { name: "Quick navigation", exact: true }),
  );
  await page.keyboard.press("Enter");
  await chooser.getByRole("textbox").fill("getting started");
  await expect(chooser.getByRole("link", { name: /Settings/ })).toHaveCount(0);
  const destination = chooser.getByRole("link", { name: /Getting started/ });
  const symbol = await destination.locator("svg").innerHTML();
  await expect(
    chooser
      .getByRole("button", { name: "Close quick navigation" })
      .locator("svg"),
  ).toHaveAttribute("aria-hidden", "true");
  await destination.click();
  await expect(page).toHaveURL(
    catalog.origin + catalog.basePath + "/getting-started",
  );
  await expect(page).toHaveTitle("Getting started | Open Agentic Catalog");
  await expect(
    page.getByRole("link", { name: "Open Catalog", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Open Reviews", exact: true }),
  ).toHaveCount(0);
  expect(
    await page
      .getByRole("heading", { name: "Getting started", exact: true })
      .locator("svg")
      .innerHTML(),
  ).toBe(symbol);
  const catalogLink = page.getByRole("link", {
    name: "Open Catalog",
    exact: true,
  });
  await expect(catalogLink.locator("svg")).toHaveAttribute(
    "aria-hidden",
    "true",
  );
  await expect(catalogLink.locator("svg")).toHaveAttribute(
    "focusable",
    "false",
  );
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Getting started", exact: true }),
  ).toBeVisible();
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(320);
});

test("authenticated About keeps its static content and UI build when the API is unavailable", async ({
  page,
  catalog,
  network,
}) => {
  await login(page, catalog);
  network.expectFailure("GET", "/_catalog/api/v1/service-information", 503);
  await page.route(
    "**/_catalog/api/v1/service-information",
    (route) =>
      route.fulfill({
        status: 503,
        contentType: "application/problem+json",
        json: { code: "unavailable", status: 503, title: "Unavailable" },
      }),
    { times: 1 },
  );
  await page.getByRole("link", { name: "About", exact: true }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(
    page.getByText("Apache License 2.0", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("UI host version", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Service information", exact: true }),
  ).toHaveCount(2);
  await signOut(page);
  await expect(
    page.getByRole("heading", { name: "About", exact: true }),
  ).toBeFocused();
  await expect(
    page.getByRole("heading", { name: "Connected builds" }),
  ).toHaveCount(0);
});

test("quick navigation preserves a dirty authoring form when native departure is declined", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-admin", "/resources/new");
  const identifier = page.getByRole("textbox", {
    name: "Canonical resource identifier",
    exact: true,
  });
  await identifier.fill("urn:air:catalog.example.com:demo:unsaved-navigation");
  await page
    .getByRole("button", { name: "Quick navigation", exact: true })
    .click();
  const chooser = page.getByRole("dialog", { name: "Quick navigation" });
  await chooser.getByRole("textbox", { name: "Find a page" }).fill("About");
  const canceled = page.waitForEvent("dialog").then(async (dialog) => {
    expect(dialog.type()).toBe("beforeunload");
    await dialog.dismiss();
  });
  await Promise.all([
    canceled,
    chooser.getByRole("link", { name: /About/ }).click(),
  ]);
  await expect(page).toHaveURL(
    catalog.origin + catalog.basePath + "/resources/new",
  );
  await expect(identifier).toHaveValue(
    "urn:air:catalog.example.com:demo:unsaved-navigation",
  );
  await expect(chooser).not.toBeVisible();
  await expect(
    page.getByRole("button", { name: "Quick navigation", exact: true }),
  ).toBeFocused();
});

test("cross-tab cancellation of pending login preserves surviving header focus", async ({
  page,
  catalog,
}) => {
  let release!: () => void;
  let started!: () => void;
  let finished!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const pending = new Promise<void>((resolve) => {
    started = resolve;
  });
  const completed = new Promise<void>((resolve) => {
    finished = resolve;
  });
  await page.route(
    "**/auth/login",
    async (route) => {
      const response = await route.fetch();
      started();
      await held;
      await route.fulfill({ response });
      finished();
    },
    { times: 1 },
  );
  await page.goto(catalog.origin + catalog.basePath + "/catalog");
  try {
    await page.locator('[data-login-alias="local-reader"]').click();
    await pending;
    const appearance = page.getByLabel("Appearance", { exact: true });
    await appearance.focus();
    await page.evaluate((base) => {
      const peer = new BroadcastChannel(`catalog-ui:${base}`);
      peer.postMessage({ sender: "owned-cross-tab-control" });
      peer.close();
    }, catalog.basePath);
    await expect(page.getByRole("alert")).toContainText(
      "session changed in another tab",
    );
    await expect(appearance).toBeFocused();
  } finally {
    release();
  }
  await completed;
  await expect(page.getByTitle("Account", { exact: true })).toHaveCount(0);
  await expect(page.locator("article.resource-card")).toHaveCount(0);
  await expect(page.getByLabel("Appearance", { exact: true })).toBeFocused();
});

test("quick navigation closes when selecting the current page section under a subpath", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-admin", "/service");
  await expect(
    page.getByRole("heading", { name: "Search indexing", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Quick navigation", exact: true })
    .click();
  const chooser = page.getByRole("dialog", { name: "Quick navigation" });
  await chooser
    .getByRole("textbox", { name: "Find a page" })
    .fill("Search indexing");
  await chooser.getByRole("link", { name: /Search indexing/ }).click();
  await expect(chooser).not.toBeVisible();
  await expect(page).toHaveURL(
    catalog.origin + catalog.basePath + "/service#search-indexing",
  );
  await expect(page.locator("#search-indexing")).toBeInViewport();
  await expect(page.getByTitle("Account", { exact: true })).toBeVisible();
});
