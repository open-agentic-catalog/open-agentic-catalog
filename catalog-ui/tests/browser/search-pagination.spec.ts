import { object, array, text } from "./json";
import { test, expect, login } from "./fixtures";
import { publishFixture } from "./published-fixture";
import { doubleText } from "./text-layout";
import { visibleFocus } from "./keyboard";
import { searchContinuationRefusal } from "../../scripts/search-continuation-evidence.mjs";
import type { Response } from "@playwright/test";

async function requireSearchPage(
  response: Response,
  step: "retry" | "next" | "previous" | "first",
) {
  if (response.status() !== 200) {
    let refusal = "unclassified";
    try {
      refusal = searchContinuationRefusal(
        response.status(),
        await response.json(),
      );
    } catch {
      // The original status assertion remains authoritative if decoding fails.
    }
    console.log(
      "Owned search continuation refusal: " +
        JSON.stringify({ step, status: response.status(), refusal }),
    );
  }
  expect(response.status()).toBe(200);
}

test("search pages contain disjoint real releases and submitting filters resets the cursor", async ({
  page,
  catalog,
  network,
}) => {
  const releases = catalog.receipt.items.flatMap((i) =>
    i.releaseId ? [i.releaseId] : [],
  );
  const padding = "large-retained-card:".padEnd(1 << 20, "x");
  // Three pages expose First, Previous and Next together on the middle page.
  for (let i = 0; i < 46; i++)
    releases.push(await publishFixture(catalog, i, i === 0 ? padding : ""));
  const detailRequests: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "GET" &&
      /\/api\/v1\/catalog\/resources\/[^/]+$/.test(
        new URL(request.url()).pathname,
      )
    )
      detailRequests.push(request.url());
  });
  const initialSearch = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/api/v1/catalog/search"),
  );
  const [searchResponse] = await Promise.all([
    initialSearch,
    login(page, catalog),
  ]);
  expect(searchResponse.status()).toBe(200);
  const cards = page.locator("article.resource-card");
  await expect(cards).toHaveCount(24);
  const searchBytes = await searchResponse.body();
  expect(searchBytes.length).toBeLessThan(64 << 10);
  expect(searchBytes.toString()).not.toContain("large-retained-card:");
  expect(detailRequests).toEqual([]);
  const previewResponse = page.waitForResponse((response) =>
    response.url().endsWith("/api/v1/catalog/resources/" + releases[3]),
  );
  const [detail] = await Promise.all([
    previewResponse,
    page.getByRole("button", { name: "Paged agent 00", exact: true }).click(),
  ]);
  expect(detail.status()).toBe(200);
  const revision = object(object(await detail.json()).versionRevision);
  const representation = object(array(revision.representations)[0]);
  const firstSkill = object(array(object(representation.data).skills)[0]);
  expect(firstSkill.description).toBe(padding);
  await expect(
    page.getByRole("dialog").getByText("Apache-2.0", { exact: true }),
  ).toBeVisible();
  expect(detailRequests).toHaveLength(1);
  await page
    .getByRole("button", { name: "Close preview", exact: true })
    .click();
  const names = () => cards.locator("h2").allTextContents();
  const first = await names();
  expect(first).toHaveLength(24);
  // Inject one temporary transport failure, then retry against the actual
  // API with exactly the same captured signed cursor and criteria.
  let failedBody: unknown;
  network.expectFailure("POST", "/_catalog/api/v1/catalog/search", 503);
  await page.route("**/_catalog/api/v1/catalog/search", async (route) => {
    failedBody = route.request().postDataJSON();
    await route.fulfill({
      status: 503,
      contentType: "application/problem+json",
      body: JSON.stringify({
        type: "about:blank",
        title: "Dependency unavailable",
        status: 503,
        code: "DEPENDENCY_UNAVAILABLE",
      }),
    });
    await page.unroute("**/_catalog/api/v1/catalog/search");
  });
  await page.getByRole("button", { name: /Next page/ }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(cards).toHaveCount(24);
  await expect(page.getByRole("button", { name: /Next page/ })).toBeDisabled();
  const retryRead = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/catalog/search") &&
      response.request().method() === "POST",
  );
  const [retryResponse] = await Promise.all([
    retryRead,
    page.getByRole("button", { name: "Retry this page", exact: true }).click(),
  ]);
  expect(retryResponse.request().postDataJSON()).toEqual(failedBody);
  await requireSearchPage(retryResponse, "retry");
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect.poll(names).not.toEqual(first);
  await expect(cards).toHaveCount(24);
  const second = await names();
  expect(new Set([...first, ...second]).size).toBe(48);
  await page.setViewportSize({ width: 320, height: 740 });
  await doubleText(page);
  const pager = page.locator(".pagination");
  const buttons = pager.getByRole("button");
  await expect(buttons).toHaveCount(3);
  for (const button of await buttons.all()) {
    const box = await button.boundingBox();
    expect(box).not.toBeNull();
    expect(
      box!.x,
      `${await button.innerText()} left edge`,
    ).toBeGreaterThanOrEqual(0);
    expect(
      box!.x + box!.width,
      `${await button.innerText()} right edge`,
    ).toBeLessThanOrEqual(320);
  }
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(320);
  const next = page.getByRole("button", { name: /Next page/ });
  const previous = page.getByRole("button", {
    name: "Previous page",
    exact: true,
  });
  await next.focus();
  await page.keyboard.press("Shift+Tab");
  await visibleFocus(previous);
  const [previousResponse] = await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url().endsWith("/api/v1/catalog/search") &&
        response.request().method() === "POST",
    ),
    page.keyboard.press("Enter"),
  ]);
  await requireSearchPage(previousResponse, "previous");
  expect(
    object(previousResponse.request().postDataJSON()).cursor,
  ).toBeUndefined();
  await expect(cards).toHaveCount(24);
  await expect.poll(names).toEqual(first);
  await expect(
    page.getByRole("button", { name: "Previous page", exact: true }),
  ).toHaveCount(0);
  await next.focus();
  await visibleFocus(next);
  const [repeatedNextResponse] = await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url().endsWith("/api/v1/catalog/search") &&
        response.request().method() === "POST",
    ),
    page.keyboard.press("Enter"),
  ]);
  await requireSearchPage(repeatedNextResponse, "next");
  await expect.poll(names).toEqual(second);
  await next.focus();
  const thirdPageRead = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/catalog/search") &&
      response.request().method() === "POST",
  );
  const [thirdPageResponse] = await Promise.all([
    thirdPageRead,
    page.keyboard.press("Enter"),
  ]);
  await requireSearchPage(thirdPageResponse, "next");
  await expect(cards).toHaveCount(1);
  const third = await names();
  expect(new Set([...first, ...second, ...third]).size).toBe(releases.length);
  await expect(next).toHaveCount(0);
  const [secondPreviousResponse] = await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url().endsWith("/api/v1/catalog/search") &&
        response.request().method() === "POST",
    ),
    previous.click(),
  ]);
  await requireSearchPage(secondPreviousResponse, "previous");
  await expect.poll(names).toEqual(second);
  await previous.focus();
  await page.keyboard.press("Shift+Tab");
  await visibleFocus(
    page.getByRole("button", { name: "First page", exact: true }),
  );
  const [firstResponse] = await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url().endsWith("/api/v1/catalog/search") &&
        response.request().method() === "POST",
    ),
    page.keyboard.press("Enter"),
  ]);
  await requireSearchPage(firstResponse, "first");
  expect(object(firstResponse.request().postDataJSON()).cursor).toBeUndefined();
  await expect(cards).toHaveCount(24);
  await expect.poll(names).toEqual(first);
  // Restore ordinary presentation for the existing filter and error checks.
  await page.evaluate(() => {
    for (const element of document.querySelectorAll<HTMLElement>("body *"))
      element.style.removeProperty("font-size");
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  // Corrupt only the next real continuation request. The actual server must
  // reject it; the browser must restart without retaining the failed cursor.
  network.expectFailure("POST", "/_catalog/api/v1/catalog/search", 400);
  await page.route("**/_catalog/api/v1/catalog/search", async (route) => {
    const body = object(route.request().postDataJSON());
    expect(body.cursor).toBeTruthy();
    await route.continue({
      postData: JSON.stringify({
        ...body,
        cursor: text(body.cursor) + "invalid",
      }),
    });
    await page.unroute("**/_catalog/api/v1/catalog/search");
  });
  const malformedRead = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/catalog/search") &&
      response.request().method() === "POST",
  );
  const [malformedResponse] = await Promise.all([
    malformedRead,
    page.getByRole("button", { name: /Next page/ }).click(),
  ]);
  const malformedProblem = object(await malformedResponse.json());
  expect(malformedResponse.status()).toBe(400);
  expect(malformedProblem.code).toBe("INVALID_ARGUMENT");
  expect(
    searchContinuationRefusal(malformedResponse.status(), malformedProblem),
  ).toBe("live_cursor_validation");
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(cards).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Previous page", exact: true }),
  ).toHaveCount(0);
  const restarted = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/api/v1/catalog/search"),
  );
  const [response] = await Promise.all([
    restarted,
    page
      .getByRole("button", { name: "Start a new search", exact: true })
      .click(),
  ]);
  expect(response.status()).toBe(200);
  expect(object(response.request().postDataJSON()).cursor).toBeUndefined();
  await expect(cards).toHaveCount(24);
  expect(await names()).toEqual(first);

  const [restartedNextResponse] = await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url().endsWith("/api/v1/catalog/search") &&
        response.request().method() === "POST",
    ),
    page.getByRole("button", { name: /Next page/ }).click(),
  ]);
  await requireSearchPage(restartedNextResponse, "next");
  await expect.poll(names).toEqual(second);
  await page
    .getByRole("searchbox", { name: "Search Catalog", exact: true })
    .fill("Paged agent");
  const [searchedResponse] = await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url().endsWith("/api/v1/catalog/search") &&
        response.request().method() === "POST",
    ),
    page.getByRole("button", { name: "Search", exact: true }).click(),
  ]);
  expect(searchedResponse.status()).toBe(200);
  expect(object(await searchedResponse.json()).durableTotal).toBe(46);
  await expect(cards).toHaveCount(24);
  await expect(
    page.getByRole("button", { name: "First page", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Previous page", exact: true }),
  ).toHaveCount(0);
  expect(new URL(page.url()).search).toBe("");
  expect(releases.length).toBe(49);

  // A cursor that already succeeded for the original query stays opaque.
  // Reuse after a different query succeeds must fail. The default mixed-origin
  // path deliberately shares one public invalid/expired/binding diagnostic.
  const originalCursor = text(
    object(retryResponse.request().postDataJSON()).cursor,
  );
  const appliedRequest = object(searchedResponse.request().postDataJSON());
  const appliedNames = await names();
  network.expectFailure("POST", "/_catalog/api/v1/catalog/search", 400);
  await page.route("**/_catalog/api/v1/catalog/search", async (route) => {
    await route.continue({
      postData: JSON.stringify({
        ...object(route.request().postDataJSON()),
        cursor: originalCursor,
      }),
    });
    await page.unroute("**/_catalog/api/v1/catalog/search");
  });
  const reboundRead = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/catalog/search") &&
      response.request().method() === "POST",
  );
  const [reboundResponse] = await Promise.all([
    reboundRead,
    page.getByRole("button", { name: /Next page/ }).click(),
  ]);
  const reboundProblem = object(await reboundResponse.json());
  expect(reboundResponse.status()).toBe(400);
  expect(
    searchContinuationRefusal(reboundResponse.status(), reboundProblem),
  ).toBe("live_cursor_validation");
  expect(JSON.stringify(reboundProblem).includes(originalCursor)).toBe(false);
  expect(JSON.stringify(reboundProblem).includes("Paged agent")).toBe(false);
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(cards).toHaveCount(0);
  const recoveredRead = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/catalog/search") &&
      response.request().method() === "POST",
  );
  const [recoveredResponse] = await Promise.all([
    recoveredRead,
    page
      .getByRole("button", { name: "Start a new search", exact: true })
      .click(),
  ]);
  expect(recoveredResponse.status()).toBe(200);
  expect(object(recoveredResponse.request().postDataJSON())).toEqual(
    appliedRequest,
  );
  await expect(cards).toHaveCount(24);
  await expect.poll(names).toEqual(appliedNames);
  expect(detailRequests).toHaveLength(1);

  // The actual API accepts declaration filters across origins. Panel edits
  // stay staged, and detail/back navigation retains the submitted predicate.
  await page.getByRole("button", { name: "Filters", exact: true }).click();
  await page.getByLabel("License declaration").fill(" Apache-2.0 ");
  await page.getByRole("button", { name: "Add category", exact: true }).click();
  await page.getByLabel("Category 1", { exact: true }).fill(" TESTING ");
  await page.getByRole("button", { name: "Add category", exact: true }).click();
  await page.getByLabel("Category 2", { exact: true }).fill("tools");
  const indexed = page.getByRole("checkbox", {
    name: "Indexed external",
    exact: true,
  });
  await expect(indexed).toBeEnabled();
  await expect(indexed).toBeChecked();
  await page
    .getByRole("checkbox", { name: "Live external", exact: true })
    .uncheck();
  await expect(cards).toHaveCount(24);
  const filtered = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/catalog/search") &&
      response.request().method() === "POST",
  );
  const [selected] = await Promise.all([
    filtered,
    page.getByRole("button", { name: "Apply filters", exact: true }).click(),
  ]);
  expect(selected.status()).toBe(200);
  expect(object(selected.request().postDataJSON())).toMatchObject({
    license: "Apache-2.0",
    categories: ["TESTING", "tools"],
    origins: ["local", "indexed"],
  });
  expect(object(await selected.json()).durableTotal).toBe(46);
  await expect(cards).toHaveCount(24);
  await page
    .getByRole("button", { name: "Paged agent 01", exact: true })
    .click();
  await page
    .getByRole("link", { name: "Open full record", exact: true })
    .click();
  await expect(
    page.getByRole("navigation", { name: "Record workflows" }),
  ).toBeVisible();
  await page.goBack();
  await expect(cards).toHaveCount(24);
  await expect(
    page.getByRole("button", { name: "Remove category TESTING", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", {
      name: "Remove origin Local Catalog",
      exact: true,
    }),
  ).toBeEnabled();
  await page
    .getByRole("button", { name: "Remove origin Local Catalog", exact: true })
    .click();
  await page
    .getByRole("button", {
      name: "Remove origin Indexed external",
      exact: true,
    })
    .click();
  await expect(cards).toHaveCount(24);
  await expect(
    page.getByRole("button", { name: "Remove license filter", exact: true }),
  ).toBeVisible();
  expect(new URL(page.url()).search).toBe("");
});
