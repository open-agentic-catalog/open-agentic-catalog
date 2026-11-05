import { readFileSync } from "node:fs";
import { createHash } from "node:crypto";
import { array, object, text } from "./json";
import { test, expect, login, confirmCommand } from "./fixtures";

// Owned synthetic pixels qualify browser delivery/rendering, not remote fetch policy.
const png = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jR1kAAAAASUVORK5CYII=",
  "base64",
);
const asset = {
  url: "https://images.example.test/owned.png",
  alt: "Owned test pixel",
  rights: { basis: "owned", attribution: "Catalog browser fixture" },
  digest: `sha256:${createHash("sha256").update(png).digest("hex")}`,
  mediaType: "image/png",
  size: png.length,
  width: 1,
  height: 1,
  rightsAssessment: "not_assessed",
};
const screenshot = readFileSync(
  new URL("../../../samples/artwork/screenshot.jpg", import.meta.url),
);
const screenshotAsset = {
  ...asset,
  url: "https://images.example.test/screenshot.jpg",
  digest: `sha256:${createHash("sha256").update(screenshot).digest("hex")}`,
  mediaType: "image/jpeg",
  size: screenshot.length,
  width: 320,
  height: 180,
};
const descriptor = {
  schemaVersion: 1,
  icon: asset,
  screenshots: [
    { ...screenshotAsset, alt: "First dashboard", caption: "First screenshot" },
    {
      ...screenshotAsset,
      alt: "Second dashboard",
      caption: "Second screenshot",
    },
  ],
};
const iri =
  "https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/artwork/v1#descriptor";

test("artwork uses authenticated Blob images with keyboard gallery navigation and no image-origin browser contact", async ({
  page,
  catalog,
}) => {
  const imageRequests: unknown[] = [];
  const forbidden: string[] = [];
  page.on("request", (request) => {
    if (request.url().startsWith("https://images.example.test"))
      forbidden.push(request.url());
  });
  await page.route("**/_catalog/api/v1/catalog/search", async (route) => {
    const response = await route.fetch();
    const data = object(await response.json());
    await route.fulfill({
      response,
      json: {
        ...data,
        items: array(data.items).map((value) => ({
          ...object(value),
          artwork: {
            descriptor,
            iconSource: "listing",
            screenshotsSource: "listing",
          },
        })),
      },
    });
  });
  await page.route("**/_catalog/api/v1/catalog/resources/*", async (route) => {
    const response = await route.fetch();
    const data = object(await response.json());
    const listing = object(data.listingRevision);
    await route.fulfill({
      response,
      json: {
        ...data,
        listingRevision: {
          ...listing,
          extensions: {
            ...object(listing.extensions || {}),
            [iri]: descriptor,
          },
        },
      },
    });
  });
  await page.route("**/_catalog/api/v1/artwork/read", async (route) => {
    expect(route.request().method()).toBe("POST");
    expect(route.request().headers()["x-catalog-request"]).toBe("1");
    const body = object(route.request().postDataJSON());
    expect(body.origin).toBe("local");
    expect(body).not.toHaveProperty("url");
    imageRequests.push(body);
    const bytes = body.slot === "screenshot" ? screenshot : png;
    await route.fulfill({
      status: 200,
      contentType: body.slot === "screenshot" ? "image/jpeg" : "image/png",
      headers: {
        "Content-Digest": `sha-256=:${createHash("sha256").update(bytes).digest("base64")}:`,
      },
      body: bytes,
    });
  });
  await login(page, catalog);
  await expect(page.locator("article.resource-card img").first()).toBeVisible();
  await expect(
    page.locator("article.resource-card img").first(),
  ).toHaveAttribute("src", /^blob:/);
  await page.locator("article.resource-card .title-button").first().click();
  const preview = page.getByRole("dialog", { name: /./ });
  await expect(preview.getByAltText("First dashboard")).toBeVisible();
  await expect(
    preview.getByText(/Image attribution: Catalog browser fixture/),
  ).toBeVisible();
  await preview.getByRole("button", { name: "Next screenshot" }).focus();
  await page.keyboard.press("Enter");
  await expect(preview.getByAltText("Second dashboard")).toBeVisible();
  await expect(preview.getByText("2 of 2")).toBeVisible();
  await page.setViewportSize({ width: 390, height: 740 });
  expect(
    await preview.evaluate(
      (element) => element.scrollWidth <= element.clientWidth,
    ),
  ).toBe(true);
  await preview.locator(".artwork-gallery").screenshot({
    path: test.info().outputPath("artwork-gallery-narrow.png"),
  });
  await page.keyboard.press("Escape");
  await expect(preview).not.toBeVisible();
  expect(imageRequests.length).toBeGreaterThan(3);
  expect(forbidden).toEqual([]);
});

test.describe("owned TLS artwork through the real Catalog boundary", () => {
  test.use({ artwork: true });
  test("author artwork edits save sealed bytes and preserve the active release", async ({
    page,
    catalog,
  }) => {
    const item = catalog.receipt.items.find(
      (entry) => entry.key === "artwork-fixture",
    )!;
    const before = await catalog.admin(
      "GET",
      `/api/v1/catalog-listings/${item.listingId}`,
    );
    const originalRevision = text(object(before.data).candidateRevisionId);
    const imageOrigin = new URL(catalog.archive!.url).origin;
    const browserImageRequests: string[] = [];
    page.on("request", (request) => {
      if (new URL(request.url()).origin === imageOrigin)
        browserImageRequests.push(request.url());
    });
    await login(
      page,
      catalog,
      "local-contributor",
      `/listings/${item.listingId}`,
    );
    await page
      .getByRole("button", { name: "New listing revision", exact: true })
      .click();
    await page.getByText("Icon and screenshots", { exact: true }).click();
    page.once("dialog", (dialog) => dialog.accept());
    await page
      .getByRole("button", { name: "Remove icon", exact: true })
      .click();
    await page.getByRole("button", { name: "Add icon", exact: true }).click();
    for (const [name, value] of [
      ["Icon HTTPS URL", imageOrigin + "/artwork-icon.png"],
      ["Icon alternative text", "Authored fixture icon"],
      ["Icon attribution", "Owned browser author"],
    ] as const)
      await page.getByRole("textbox", { name, exact: true }).fill(value);
    await page
      .getByRole("button", { name: "Add screenshot", exact: true })
      .click();
    for (const [name, value] of [
      ["Screenshot 2 HTTPS URL", imageOrigin + "/artwork-screen.jpg"],
      ["Screenshot 2 alternative text", "Authored fixture screenshot"],
      ["Screenshot 2 caption (optional)", "Edited before release"],
      ["Screenshot 2 attribution", "Owned browser author"],
    ] as const)
      await page.getByRole("textbox", { name, exact: true }).fill(value);
    await page
      .getByRole("button", { name: "Move screenshot 2 earlier", exact: true })
      .click();
    await expect(
      page.getByRole("textbox", {
        name: "Screenshot 1 alternative text",
        exact: true,
      }),
    ).toHaveValue("Authored fixture screenshot");
    page.once("dialog", (dialog) => dialog.accept());
    await page
      .getByRole("button", { name: "Remove screenshot 2", exact: true })
      .click();
    const written = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response
          .url()
          .endsWith(`/catalog-listings/${item.listingId}/revisions`),
    );
    await page
      .getByRole("button", { name: "Create listing revision", exact: true })
      .click();
    await confirmCommand(page);
    const saved = await written;
    expect(saved.status()).toBe(201);
    expect(saved.request().headers()["if-match"]).toBe(before.etag);
    const submitted = object(object(saved.request().postDataJSON()).extensions)[
      iri
    ];
    expect(object(object(submitted).icon)).not.toHaveProperty("digest");
    const current = await catalog.admin(
      "GET",
      `/api/v1/catalog-listings/${item.listingId}`,
    );
    expect(object(current.data).candidateRevisionId).not.toBe(originalRevision);
    expect(object(current.data).currentReleaseId).toBe(
      object(before.data).currentReleaseId,
    );
    const revision = await catalog.admin(
      "GET",
      `/api/v1/listing-revisions/${text(object(current.data).candidateRevisionId)}`,
    );
    const sealed = object(object(object(revision.data).extensions)[iri]);
    const iconBytes = readFileSync(
      new URL("../../../samples/artwork/icon.png", import.meta.url),
    );
    expect(sealed.icon).toMatchObject({
      url: imageOrigin + "/artwork-icon.png",
      alt: "Authored fixture icon",
      digest: `sha256:${createHash("sha256").update(iconBytes).digest("hex")}`,
      size: iconBytes.length,
      rights: { basis: "owned", attribution: "Owned browser author" },
    });
    expect(sealed.screenshots).toEqual([
      expect.objectContaining({
        url: imageOrigin + "/artwork-screen.jpg",
        alt: "Authored fixture screenshot",
        caption: "Edited before release",
        digest: `sha256:${createHash("sha256").update(screenshot).digest("hex")}`,
        size: screenshot.length,
        rights: { basis: "owned", attribution: "Owned browser author" },
      }),
    ]);
    await page.reload();
    await page
      .getByRole("button", { name: "New listing revision", exact: true })
      .click();
    await page.getByText("Icon and screenshots", { exact: true }).click();
    await expect(
      page.getByRole("textbox", { name: "Icon alternative text", exact: true }),
    ).toHaveValue("Authored fixture icon");
    await expect(
      page.getByRole("textbox", {
        name: "Screenshot 1 alternative text",
        exact: true,
      }),
    ).toHaveValue("Authored fixture screenshot");
    await expect(
      page.getByRole("textbox", {
        name: "Screenshot 2 alternative text",
        exact: true,
      }),
    ).toHaveCount(0);
    expect(browserImageRequests).toEqual([]);
  });
  test("renders admitted artwork and falls back after the owned image listener stops", async ({
    page,
    catalog,
    network,
  }) => {
    const remote: string[] = [];
    page.on("request", (request) => {
      if (request.url().startsWith("https://127.0.0.1:"))
        remote.push(request.url());
    });
    await login(page, catalog);
    const card = page.getByRole("article").filter({
      has: page.getByRole("button", { name: "Artwork fixture Agent" }),
    });
    await expect(
      card.getByAltText("Blue and gold geometric Catalog fixture"),
    ).toHaveAttribute("src", /^blob:/);
    await card.getByRole("button", { name: "Artwork fixture Agent" }).click();
    const preview = page.getByRole("dialog");
    await expect(
      preview.getByAltText("Synthetic blue and gold listing preview"),
    ).toBeVisible();
    await expect(
      preview.getByText(
        "Owned fixture image; no application or third-party content.",
      ),
    ).toBeVisible();
    await page.setViewportSize({ width: 390, height: 740 });
    await preview.locator(".artwork-gallery").screenshot({
      path: test.info().outputPath("artwork-gallery-real-narrow.png"),
    });
    await page.keyboard.press("Escape");
    await catalog.stopArchives!();
    network.expectFailure("POST", "/_catalog/api/v1/artwork/read", 503);
    const unavailable = page.waitForResponse(
      (response) =>
        response.url().endsWith("/_catalog/api/v1/artwork/read") &&
        response.status() === 503,
    );
    await page.reload();
    await page
      .getByRole("button", { name: "Artwork fixture Agent" })
      .scrollIntoViewIfNeeded();
    await unavailable;
    await expect(
      page.getByRole("button", { name: "Artwork fixture Agent" }),
    ).toBeVisible();
    await expect(
      page.locator("article.resource-card .kind-icon.agent"),
    ).toBeVisible();
    await expect(page.locator("article.resource-card img")).toHaveCount(0);
    expect(remote).toEqual([]);
  });
});

test("artwork editor exposes rights help and wraps screenshot actions in both themes", async ({
  page,
  catalog,
}) => {
  const item = catalog.receipt.items.find(
    (item) => item.key === "order-agent",
  )!;
  const writes: string[] = [];
  const references: string[] = [];
  const rightsReference = "https://rights.example.invalid/image-license";
  await page.route(rightsReference, async (route) => {
    references.push(route.request().url());
    await route.fulfill({ status: 204 });
  });
  page.on("request", (request) => {
    if (request.url().includes("/_catalog/api/") && request.method() !== "GET")
      writes.push(request.url());
  });
  await login(page, catalog, "local-admin", `/listings/${item.listingId}`);
  await page
    .getByRole("button", { name: "New listing revision", exact: true })
    .click();
  const editor = page.locator("details").filter({
    has: page.locator("summary", { hasText: "Icon and screenshots" }),
  });
  await editor.locator("summary").click();
  await editor.getByRole("button", { name: "Add icon", exact: true }).click();
  await editor
    .getByRole("button", { name: "Add screenshot", exact: true })
    .click();
  await editor
    .getByRole("button", { name: "Add screenshot", exact: true })
    .click();
  const icon = editor.getByRole("group", { name: "Icon", exact: true });
  await expect(
    icon.getByRole("textbox", { name: "Icon HTTPS URL", exact: true }),
  ).toHaveAccessibleDescription(
    /static PNG or JPEG from an allowed HTTPS origin.*credentials, spaces or a fragment/,
  );
  const reference = icon.getByRole("textbox", {
    name: "Icon rights reference (optional)",
    exact: true,
  });
  await reference.fill(rightsReference);
  await expect(
    icon.getByRole("textbox", { name: "Icon attribution", exact: true }),
  ).toHaveAccessibleDescription(/Required even for an owned image/);
  await expect(reference).toHaveAccessibleDescription(
    /Do not include private proof or secrets/,
  );
  await expect(
    editor.getByText(/does not establish legal clearance/),
  ).toBeVisible();
  const about = icon.getByRole("button", {
    name: "About icon rights reference (optional)",
    exact: true,
  });
  await about.focus();
  await page.keyboard.press("Enter");
  await expect(about).toHaveAttribute("aria-expanded", "true");
  await expect(reference).toHaveAccessibleDescription(
    /does not fetch it or verify your rights/,
  );
  await page.keyboard.press("Space");
  await expect(about).toHaveAttribute("aria-expanded", "false");
  await expect(reference).not.toHaveAccessibleDescription(
    /does not fetch it or verify your rights/,
  );
  await expect(reference).toHaveValue(rightsReference);
  const borders = new Set<string>();
  for (const theme of ["Light", "Dark"]) {
    for (const density of ["Comfortable", "Compact"]) {
      await page.getByLabel("Appearance", { exact: true }).click();
      await page.getByRole("radio", { name: theme, exact: true }).check();
      await page.getByRole("radio", { name: density, exact: true }).check();
      await page.keyboard.press("Escape");
      await expect(page.locator("html")).toHaveAttribute(
        "data-theme",
        theme.toLowerCase(),
      );
      await expect(page.locator("html")).toHaveAttribute(
        "data-density",
        density.toLowerCase(),
      );
      for (const width of [1440, 390]) {
        await page.setViewportSize({ width, height: 900 });
        const border = await icon.evaluate((element) => ({
          padding: parseFloat(getComputedStyle(element).paddingTop),
          space: parseFloat(
            getComputedStyle(document.documentElement).getPropertyValue(
              "--space",
            ),
          ),
          style: getComputedStyle(element).borderTopStyle,
          color: getComputedStyle(element).borderTopColor,
          panelColor: getComputedStyle(element.closest(".workspace-panel")!)
            .borderTopColor,
        }));
        expect(border.padding).toBe(border.space);
        expect(border.style).toBe("solid");
        expect(border.color).toBe(border.panelColor);
        borders.add(border.color);
        await expect(reference).toHaveValue(rightsReference);
        const actions = editor
          .getByRole("button", { name: "Remove screenshot 2", exact: true })
          .locator("..");
        await expect(actions).toHaveClass(/workspace-toolbar/);
        expect(
          await actions.evaluate(
            (element) => element.scrollWidth <= element.clientWidth,
          ),
        ).toBe(true);
        if (width === 390) {
          const remove = await actions
            .getByRole("button", { name: "Remove screenshot 2", exact: true })
            .boundingBox();
          const move = await actions
            .getByRole("button", {
              name: "Move screenshot 2 earlier",
              exact: true,
            })
            .boundingBox();
          expect(move!.y - remove!.y - remove!.height).toBeGreaterThanOrEqual(
            8,
          );
          const add = await editor
            .getByRole("button", { name: "Add screenshot", exact: true })
            .boundingBox();
          expect(add!.y - move!.y - move!.height).toBeGreaterThanOrEqual(16);
        }
      }
    }
  }
  expect(borders.size).toBe(2);
  expect(writes).toEqual([]);
  expect(references).toEqual([]);
});
