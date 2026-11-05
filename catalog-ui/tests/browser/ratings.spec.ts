import AxeBuilder from "@axe-core/playwright";
import type { components } from "../../src/api/schema";
import { required } from "../required";
import { object } from "./json";
import {
  test,
  expect,
  login,
  confirmCommand,
  signOut,
  openAccount,
} from "./fixtures";

test("preview protects rating drafts and keeps nested command recovery on the exact request", async ({
  page,
  catalog,
  network,
}) => {
  const item = required(catalog.receipt.items.find((entry) => entry.releaseId));
  const metadata = await catalog.admin(
    "GET",
    `/api/v1/catalog/resources/${item.releaseId}`,
  );
  const name = String(object(metadata.data.listingRevision).displayName);
  const namespace = String(object(metadata.data.display).namespaceName);
  await login(page, catalog);
  await page.getByRole("button", { name, exact: true }).click();
  const preview = page.getByRole("dialog", { name, exact: true });
  await preview
    .getByRole("button", { name: "Ratings for this version", exact: true })
    .click();
  await preview.getByRole("radio", { name: "4 stars", exact: true }).check();
  for (const leave of [
    () =>
      preview
        .getByRole("button", { name: "Close preview", exact: true })
        .click(),
    () => page.keyboard.press("Escape"),
    () => page.keyboard.press("Escape"),
    () => page.mouse.click(4, 4),
    () =>
      preview
        .getByRole("button", { name: /^Filter by tag / })
        .first()
        .click(),
    () =>
      preview
        .getByRole("button", {
          name: `Filter by namespace ${namespace}`,
          exact: true,
        })
        .click(),
    () => preview.getByRole("link", { name: /Open full record/ }).click(),
  ]) {
    const discard = page.waitForEvent("dialog").then(async (dialog) => {
      expect(dialog.type()).toBe("confirm");
      expect(dialog.message()).toContain("Discard unsaved input");
      await dialog.dismiss();
    });
    await Promise.all([discard, leave()]);
    await expect(preview).toBeVisible();
    await expect(
      preview.getByRole("radio", { name: "4 stars", exact: true }),
    ).toBeChecked();
    await expect(page).toHaveURL(catalog.origin + "/catalog");
  }
  await preview
    .getByRole("button", { name: "Submit rating", exact: true })
    .click();
  const confirmation = page.getByRole("dialog", {
    name: "Submit your rating",
    exact: true,
  });
  await expect(confirmation.getByLabel("Command summary")).toContainText(
    "4 / 5",
  );
  await expect(confirmation.getByLabel("Command summary")).toContainText(
    "Not allowed",
  );
  await page.keyboard.press("Escape");
  await expect(confirmation).toHaveCount(0);
  await expect(preview).toBeVisible();
  await expect(
    preview.getByRole("radio", { name: "4 stars", exact: true }),
  ).toBeChecked();
  await preview
    .getByRole("button", { name: "Submit rating", exact: true })
    .click();
  let releaseResponse: () => void = () => {};
  const gate = new Promise<void>((resolve) => {
    releaseResponse = resolve;
  });
  const requests: {
    body: string | null;
    key: string | undefined;
    etag: string | undefined;
  }[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname === "/_catalog/api/v1/ratings"
    )
      requests.push({
        body: request.postData(),
        key: request.headers()["idempotency-key"],
        etag: request.headers()["if-match"],
      });
  });
  await page.route(
    "**/_catalog/api/v1/ratings",
    async (route) => {
      await gate;
      await route.fulfill({
        status: 503,
        contentType: "application/problem+json",
        body: JSON.stringify({ code: "DEPENDENCY_UNAVAILABLE" }),
      });
    },
    { times: 1 },
  );
  network.expectFailure("POST", "/_catalog/api/v1/ratings", 503);
  try {
    await confirmation
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    await expect(
      confirmation.getByRole("button", { name: "Submitting...", exact: true }),
    ).toBeDisabled();
    for (let attempt = 0; attempt < 2; attempt++) {
      await page.keyboard.press("Escape");
      await expect(page.getByRole("dialog")).toHaveCount(2);
    }
    releaseResponse();
    await expect(confirmation.getByRole("alert")).toContainText(
      "The outcome is unknown",
    );
    await expect(confirmation.getByLabel("Command summary")).toContainText(
      "4 / 5",
    );
    await expect(confirmation.getByLabel("Command summary")).toContainText(
      "Not allowed",
    );
    for (let attempt = 0; attempt < 2; attempt++) {
      await page.keyboard.press("Escape");
      await expect(page.getByRole("dialog")).toHaveCount(2);
    }
    await confirmation
      .getByRole("button", { name: "Retry same command", exact: true })
      .click();
    await expect(confirmation).toHaveCount(0);
    await expect(
      preview.getByText("Ratings: 4.0 / 5 (1)", { exact: true }),
    ).toBeVisible();
    expect(requests).toHaveLength(2);
    expect(requests[0]?.key).toBeTruthy();
    expect(requests[1]).toEqual(requests[0]);
    await preview
      .getByRole("button", { name: "Close preview", exact: true })
      .click();
    await expect(preview).toHaveCount(0);
  } finally {
    releaseResponse();
  }
});

test("Catalog browser history preserves dirty detail input and the original search", async ({
  page,
  catalog,
}) => {
  const item = required(catalog.receipt.items.find((entry) => entry.releaseId));
  const metadata = await catalog.admin(
    "GET",
    `/api/v1/catalog/resources/${item.releaseId}`,
  );
  const name = String(object(metadata.data.listingRevision).displayName);
  const namespace = String(object(metadata.data.display).namespaceName);
  await login(page, catalog);
  await page.getByRole("button", { name: "Table", exact: true }).click();
  await page.getByRole("button", { name, exact: true }).click();
  await page.getByRole("link", { name: /Open full record/ }).click();
  await expect(page).toHaveURL(catalog.origin + `/catalog/${item.releaseId}`);
  await page
    .getByRole("button", { name: "Ratings for this version", exact: true })
    .click();
  await page.getByRole("radio", { name: "3 stars", exact: true }).check();
  const writes: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname === "/_catalog/api/v1/ratings"
    )
      writes.push(request.url());
  });
  for (const leave of [
    () => page.getByRole("link", { name: /Back to Catalog/ }).click(),
    () =>
      page
        .getByRole("button", { name: /^Filter by tag / })
        .first()
        .click(),
    () =>
      page
        .getByRole("button", {
          name: `Filter by namespace ${namespace}`,
          exact: true,
        })
        .click(),
    () => page.goBack(),
  ]) {
    const discard = page.waitForEvent("dialog").then(async (dialog) => {
      expect(dialog.type()).toBe("confirm");
      expect(dialog.message()).toContain("Discard unsaved input");
      await dialog.dismiss();
    });
    await Promise.all([discard, leave()]);
    await expect(page).toHaveURL(catalog.origin + `/catalog/${item.releaseId}`);
    await expect(
      page.getByRole("radio", { name: "3 stars", exact: true }),
    ).toBeChecked();
    expect(writes).toEqual([]);
  }
  page.once("dialog", (dialog) => dialog.accept());
  await page.goBack();
  await expect(page).toHaveURL(catalog.origin + "/catalog");
  await expect(
    page.getByRole("button", { name: "Table", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  await expect(page.locator("tbody tr")).toHaveCount(3);
  await page.goForward();
  await expect(page).toHaveURL(catalog.origin + `/catalog/${item.releaseId}`);
  await expect(
    page.getByRole("button", { name: "Ratings for this version", exact: true }),
  ).toHaveAttribute("aria-expanded", "false");
});

test("human version ratings support keyboard input, private consent, edits and withdrawal", async ({
  page,
  catalog,
}) => {
  const item = required(catalog.receipt.items[0]);
  const ratingReads: string[] = [];
  const ratingWrites: Record<string, unknown>[] = [];
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (path === "/_catalog/api/v1/ratings/read")
      ratingReads.push(request.url());
    if (request.method() === "POST" && path === "/_catalog/api/v1/ratings")
      ratingWrites.push(object(request.postDataJSON()));
  });
  await login(page, catalog, "local-reader", `/catalog/${item.releaseId}`);
  const ratingButton = page.getByRole("button", {
    name: "Ratings for this version",
    exact: true,
  });
  await expect(ratingButton).toHaveAttribute("aria-expanded", "false");
  await expect(
    page.getByRole("region", { name: "Ratings for this version", exact: true }),
  ).toHaveCount(0);
  const heading = required(
    await page.locator("main").getByRole("heading", { level: 1 }).boundingBox(),
  );
  const control = required(await ratingButton.boundingBox());
  expect(heading!.y + heading!.height).toBeLessThan(control!.y);
  expect(ratingReads).toEqual([]);
  await ratingButton.click();
  await expect(page.getByText("No ratings yet", { exact: true })).toBeVisible();
  await expect(
    page.getByRole("checkbox", { name: /Allow my score/ }),
  ).not.toBeChecked();
  await expect(
    page.getByText(/Published snapshots and copies cannot be recalled/),
  ).toBeVisible();
  expect(ratingReads).toHaveLength(1);
  await page.getByRole("radio", { name: "4 stars", exact: true }).focus();
  await page.keyboard.press("Space");
  await page
    .getByRole("button", { name: "Submit rating", exact: true })
    .click();
  await expect(
    page.getByRole("dialog").getByLabel("Command summary"),
  ).toContainText("Sharing consent");
  await expect(
    page.getByRole("dialog").getByLabel("Command summary"),
  ).toContainText("Not allowed");
  await confirmCommand(page);
  await expect(
    page.getByText("Ratings: 4.0 / 5 (1)", { exact: true }),
  ).toBeVisible();
  await page.getByRole("radio", { name: "2 stars", exact: true }).check();
  await page
    .getByRole("button", { name: "Update rating", exact: true })
    .click();
  await expect(
    page.getByRole("dialog").getByLabel("Command summary"),
  ).toContainText("2 / 5");
  await expect(
    page.getByRole("dialog").getByLabel("Command summary"),
  ).toContainText("Not allowed");
  await confirmCommand(page);
  await expect(
    page.getByText("Ratings: 2.0 / 5 (1)", { exact: true }),
  ).toBeVisible();
  const scoreBox = await page
    .getByRole("group", { name: "Your score", exact: true })
    .boundingBox();
  const withdrawBox = await page
    .getByRole("button", { name: "Withdraw rating", exact: true })
    .boundingBox();
  expect(
    withdrawBox!.y - scoreBox!.y - scoreBox!.height,
  ).toBeGreaterThanOrEqual(16);
  expect(
    await new AxeBuilder({ page }).include("main").analyze(),
  ).toMatchObject({ violations: [], incomplete: [] });
  // Keep the complete panel below the sticky header in the maintained guide
  // capture. Restore the ordinary viewport before continuing the workflow.
  const previousViewport = page.viewportSize()!;
  const ratingRegion = page.getByRole("region", {
    name: "Ratings for this version",
    exact: true,
  });
  await page.setViewportSize({ width: 1440, height: 1400 });
  await ratingRegion.evaluate((element) =>
    element.scrollIntoView({ block: "center" }),
  );
  const panelBox = await ratingRegion.boundingBox();
  const headerBox = await page.getByRole("banner").boundingBox();
  expect(panelBox!.y).toBeGreaterThanOrEqual(headerBox!.y + headerBox!.height);
  await expect(
    ratingRegion.getByRole("button", {
      name: "Refresh this rating",
      exact: true,
    }),
  ).toBeVisible();
  await ratingRegion.screenshot({
    path: test.info().outputPath("resource-ratings.png"),
  });
  await page.setViewportSize(previousViewport);
  // Exercise explicit opt-in, then an edit whose omitted consent preserves it.
  const consent = page.getByRole("checkbox", { name: /Allow my score/ });
  await consent.check();
  await page
    .getByRole("button", { name: "Update rating", exact: true })
    .click();
  await expect(
    page.getByRole("dialog").getByLabel("Command summary"),
  ).toContainText("Allowed");
  expect(ratingWrites).toHaveLength(2);
  const refreshed = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname === "/_catalog/api/v1/ratings/read",
  );
  await confirmCommand(page);
  const saved = object(object(await (await refreshed).json()).own);
  expect({
    score: saved.score,
    consent: saved.consent,
    state: saved.state,
  }).toEqual({ score: 2, consent: true, state: "active" });
  await expect(consent).toBeChecked();
  expect(ratingWrites).toHaveLength(3);
  expect(ratingWrites[2]).toMatchObject({ score: 2, consent: true });
  await page.getByRole("radio", { name: "3 stars", exact: true }).check();
  await page
    .getByRole("button", { name: "Update rating", exact: true })
    .click();
  await expect(
    page.getByRole("dialog").getByLabel("Command summary"),
  ).toContainText("3 / 5");
  await expect(
    page.getByRole("dialog").getByLabel("Command summary"),
  ).toContainText("Allowed");
  expect(ratingWrites).toHaveLength(3);
  await confirmCommand(page);
  await expect(
    page.getByText("Ratings: 3.0 / 5 (1)", { exact: true }),
  ).toBeVisible();
  await expect(consent).toBeChecked();
  expect(ratingWrites).toHaveLength(4);
  expect(ratingWrites[3]).toMatchObject({ score: 3 });
  expect(ratingWrites[3]).not.toHaveProperty("consent");
  await openAccount(page);
  await page.getByRole("link", { name: "My ratings", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: item.identifier, exact: true }),
  ).toBeVisible();
  await expect(
    page
      .locator("dt")
      .filter({ hasText: /^Saved sharing consent$/ })
      .locator("+ dd"),
  ).toHaveText("Yes");
  await expect(
    page
      .locator("dt")
      .filter({ hasText: /^Last rating update$/ })
      .locator("+ dd time"),
  ).toHaveAttribute("datetime", /Z$/);
  await expect(page.getByText(/future eligible/)).toBeVisible();
  const members = await catalog.admin<{
    items: { id: string; subject: string }[];
  }>("GET", "/api/v1/memberships?limit=100");
  const member = required(
    members.data.items.find((entry) => entry.subject === "local-reader"),
  );
  const assignments = await catalog.admin<{
    items: components["schemas"]["AccessAssignment"][];
    nextCursor?: string;
  }>("GET", `/api/v1/access/assignments?membershipId=${member.id}&limit=100`);
  expect(assignments.data.nextCursor).toBeUndefined();
  const active = assignments.data.items.filter(
    (assignment) => assignment.state === "active",
  );
  expect(active.length).toBeGreaterThan(0);
  for (const assignment of active) {
    await catalog.admin(
      "POST",
      `/api/v1/access/assignments/${assignment.id}:revoke`,
      { reason: "Qualify owner withdrawal after target and rating grant loss" },
      `"rev-${assignment.revision}"`,
    );
  }
  const ownerReads: string[] = [];
  const observeOwnerRead = (request: import("@playwright/test").Request) => {
    const path = new URL(request.url()).pathname;
    if (request.method() === "GET" && path.startsWith("/_catalog/api/v1/"))
      ownerReads.push(path);
  };
  page.on("request", observeOwnerRead);
  const self = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      new URL(response.url()).pathname === `${catalog.basePath}/auth/session`,
  );
  await page.reload();
  expect(await (await self).json()).toMatchObject({
    access: {
      permissions: [],
      availablePermissions: [],
      namespaceGrants: [],
    },
  });
  await expect(
    page.getByRole("heading", { name: item.identifier, exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Withdraw rating", exact: true })
    .click();
  await confirmCommand(page);
  await expect(page.getByText(/Version:.*Withdrawn/)).toBeVisible();
  page.off("request", observeOwnerRead);
  expect(ownerReads).toContain("/_catalog/api/v1/ratings");
  expect(
    ownerReads.every((path) =>
      ["/_catalog/api/v1/me", "/_catalog/api/v1/ratings"].includes(path),
    ),
  ).toBe(true);
  for (const assignment of active) {
    await catalog.admin("POST", "/api/v1/access/assignments", {
      membershipId: member.id,
      roleDefinitionId: assignment.roleDefinitionId,
      ...(assignment.namespaceId
        ? { namespaceId: assignment.namespaceId }
        : {}),
      reason: "Restore fixture grants for the existing aggregate control",
    });
  }
  await page.goto(
    `${catalog.origin}${catalog.basePath}/catalog/${item.releaseId}`,
  );
  await page
    .getByRole("button", { name: "Ratings for this version", exact: true })
    .click();
  await expect(page.getByText("No ratings yet", { exact: true })).toBeVisible();
});

test("moderation removes a score and permits only a fresh private submission", async ({
  page,
  catalog,
}) => {
  const item = required(catalog.receipt.items[0]);
  await login(page, catalog, "local-reader", `/catalog/${item.releaseId}`);
  await page
    .getByRole("button", { name: "Ratings for this version", exact: true })
    .click();
  await page.getByRole("radio", { name: "5 stars", exact: true }).check();
  await page
    .getByRole("button", { name: "Submit rating", exact: true })
    .click();
  await confirmCommand(page);
  await expect(
    page.getByText("Ratings: 5.0 / 5 (1)", { exact: true }),
  ).toBeVisible();
  await signOut(page);
  await login(page, catalog, "local-admin", "/ratings");
  await page
    .getByRole("button", { name: "Remove rating", exact: true })
    .click();
  await page
    .getByRole("textbox", { name: "Moderation reason", exact: true })
    .fill("Confirmed synthetic moderation case");
  page.once("dialog", async (dialog) => dialog.dismiss());
  await page
    .getByRole("navigation", { name: "Workspace", exact: true })
    .getByRole("link", { name: "Catalog", exact: true })
    .click();
  await expect(page).toHaveURL(/\/ratings$/);
  await expect(
    page.getByRole("textbox", { name: "Moderation reason", exact: true }),
  ).toHaveValue("Confirmed synthetic moderation case");
  await page.route(
    "**/_catalog/api/v1/ratings/inbox*",
    (route) => route.fulfill({ json: { items: [{}] } }),
    { times: 1 },
  );
  await page
    .getByRole("button", { name: "Remove rating", exact: true })
    .click();
  await expect(
    page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true }),
  ).toHaveClass("danger");
  await expect(page.getByRole("dialog")).toContainText(
    "cannot submit again until a moderator permits a new submission",
  );
  await confirmCommand(page);
  await expect(page.getByRole("status")).toContainText("Rating state updated.");
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(
    page.getByRole("textbox", { name: "Moderation reason", exact: true }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "Reload", exact: true }).click();
  await expect(page.getByText(/Version:.*Removed/)).toBeVisible();
  await expect(
    page.locator('.workspace-status[data-state="removed"]'),
  ).toHaveAttribute("data-tone", "neutral");
  await expect(page.getByRole("status")).toContainText("Rating state updated.");
  await expect(page.getByRole("radio")).toHaveCount(0);
  await page
    .getByRole("button", { name: "Permit new submission", exact: true })
    .click();
  await page
    .getByRole("textbox", { name: "Moderation reason", exact: true })
    .fill("Permit a fresh private opinion");
  await page
    .getByRole("button", { name: "Permit new submission", exact: true })
    .click();
  await confirmCommand(page);
  await expect(page.getByText(/Version:.*Withdrawn/)).toBeVisible();
  await signOut(page);
  await login(page, catalog, "local-reader", `/catalog/${item.releaseId}`);
  await page
    .getByRole("button", { name: "Ratings for this version", exact: true })
    .click();
  await expect(
    page.getByRole("checkbox", { name: /Allow my score/ }),
  ).not.toBeChecked();
  await page.getByRole("radio", { name: "3 stars", exact: true }).check();
  await page
    .getByRole("button", { name: "Submit rating", exact: true })
    .click();
  await confirmCommand(page);
  await expect(
    page.getByText("Ratings: 3.0 / 5 (1)", { exact: true }),
  ).toBeVisible();
});

test("a sharing-only administrator uses the exact host audience without publication or content reads", async ({
  page,
  catalog,
}) => {
  const members = await catalog.admin<{
    items: { id: string; subject: string }[];
  }>("GET", "/api/v1/memberships?limit=100");
  const member = required(
    members.data.items.find((entry) => entry.subject === "local-no-role"),
  );
  const role = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/access/roles",
    {
      name: "Rating sharing only",
      permissions: ["ratings.sharing.manage"],
      reason: "Qualify independent sharing authority",
    },
  );
  await catalog.admin("POST", "/api/v1/access/assignments", {
    membershipId: member.id,
    roleDefinitionId: role.data.id,
    reason: "Allow only aggregate sharing choices",
  });
  const reads: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "GET" &&
      new URL(request.url()).pathname.includes("/_catalog/api/v1/")
    )
      reads.push(new URL(request.url()).pathname);
  });
  await login(
    page,
    catalog,
    "local-no-role",
    "/publication?host=127.0.0.1&audience=anonymous",
  );
  await page
    .getByRole("button", { name: "Rating summary sharing", exact: true })
    .click();
  await expect(
    page.getByLabel("Sharing reason", { exact: true }),
  ).toBeVisible();
  await page
    .getByLabel("Sharing reason", { exact: true })
    .fill("Share consented opinions in future snapshots");
  const saved = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/api/v1/ratings/sharing"),
  );
  await page
    .getByRole("button", { name: "Enable sharing", exact: true })
    .click();
  await confirmCommand(page);
  const response = await saved;
  expect(response.status()).toBe(200);
  expect(response.request().postDataJSON()).toMatchObject({
    host: "127.0.0.1",
    audience: "anonymous",
    enabled: true,
  });
  await expect(
    page.locator(".workspace-status").filter({ hasText: /^Enabled$/ }),
  ).toHaveAttribute("data-tone", "neutral");
  expect(reads.filter((path) => path !== "/_catalog/api/v1/me")).toEqual([
    "/_catalog/api/v1/ratings/sharing",
    "/_catalog/api/v1/ratings/sharing",
  ]);
  await expect(
    page.getByRole("button", { name: "Refresh host choices", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("heading", { name: "127.0.0.1", exact: true }),
  ).toHaveCount(0);
});
