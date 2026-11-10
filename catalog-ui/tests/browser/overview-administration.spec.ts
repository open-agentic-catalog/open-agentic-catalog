import { openDisclosure, revealControl } from "./editor-disclosure";
import AxeBuilder from "@axe-core/playwright";
import { test, expect, login, type Catalog } from "./fixtures";
import type { components } from "../../src/api/schema";
import { object } from "./json";
import { required } from "../required";

async function seedModeration(catalog: Catalog) {
  const item = required(catalog.receipt.items.find((row) => row.releaseId));
  const metadata = (
    await catalog.admin("GET", `/api/v1/catalog/resources/${item.releaseId}`)
  ).data;
  for (const category of ["problem", "problem", "documentation"]) {
    await catalog.admin("POST", "/api/v1/feedback", {
      resourceId: item.resourceId,
      category,
      comment: "Clarify this sample's setup instructions.",
    });
  }
  await catalog.admin("POST", "/api/v1/ratings", {
    target: {
      origin: "local",
      resourceId: item.resourceId,
      versionRevisionId: item.versionRevisionId,
      identifier: item.identifier,
      kind: object(metadata.resource).kind,
      versionPresent: true,
      version: object(metadata.version).version,
    },
    releaseId: item.releaseId,
    score: 4,
    consent: false,
  });
}

for (const base of ["root", "subpath"]) {
  test(`${base}: Members keeps its applied URL through a current access refresh`, async ({
    page,
    catalog,
    network,
  }) => {
    const initial = `${catalog.origin}${catalog.basePath}/members?state=active&actorType=human`;
    const before = await catalog.admin<{ authorizationRevision: number }>(
      "GET",
      "/api/v1/me",
    );
    let reads = 0;
    page.on("request", (request) => {
      if (
        new URL(request.url()).pathname ===
        `${catalog.basePath}/_catalog/api/v1/memberships`
      )
        reads++;
    });
    await login(
      page,
      catalog,
      "local-admin",
      "/members?state=active&actorType=human",
    );
    await expect(
      page.locator(".workspace-table tbody tr").first(),
    ).toBeVisible();
    await catalog.admin("POST", "/api/v1/access/roles", {
      name: "Directory refresh policy",
      permissions: ["resource.read"],
      reason: "Qualify a real authorization revision change",
    });
    // A controlled denial requests real current hints; this tests UI custody, not API admission.
    await page.route(
      (url) =>
        url.pathname === `${catalog.basePath}/_catalog/api/v1/memberships`,
      (route) =>
        route.fulfill({
          status: 403,
          contentType: "application/problem+json",
          json: { title: "Forbidden", status: 403, code: "FORBIDDEN" },
        }),
      { times: 1 },
    );
    network.expectFailure("GET", "/_catalog/api/v1/memberships", 403);
    const refreshed = page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname === `${catalog.basePath}/auth/session`,
    );
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    const session = object(await (await refreshed).json());
    expect(object(session.access).authorizationRevision).toBeGreaterThan(
      before.data.authorizationRevision,
    );
    await expect(page).toHaveURL(initial);
    await expect(
      page.getByRole("combobox", { name: "State", exact: true }),
    ).toHaveValue("active");
    await expect(
      page.getByRole("combobox", { name: "Identity type filter", exact: true }),
    ).toHaveValue("human");
    expect(reads).toBe(2);
    const loaded = page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname ===
        `${catalog.basePath}/_catalog/api/v1/memberships`,
    );
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    expect((await loaded).status()).toBe(200);
    await expect(
      page.locator(".workspace-table tbody tr").first(),
    ).toBeVisible();
    await expect(page).toHaveURL(initial);
    expect(reads).toBe(3);
  });
  test(`${base}: Administration summaries match real counts and restore exact list filters`, async ({
    page,
    catalog,
    network,
  }) => {
    await seedModeration(catalog);
    const mutations: string[] = [],
      overviewReads: string[] = [];
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (
        path.includes("/_catalog/api/v1/") &&
        !["GET", "HEAD"].includes(request.method())
      )
        mutations.push(path);
      if (path.includes("/_catalog/api/v1/overview/"))
        overviewReads.push(path.split("/").at(-1)!);
    });
    const read = (name: string) =>
      page.waitForResponse((response) =>
        new URL(response.url()).pathname.endsWith(`/api/v1/overview/${name}`),
      );
    const accessRead = read("administration"),
      feedbackRead = read("feedback"),
      ratingsRead = read("ratings");
    await login(page, catalog, "local-admin", "/overview?view=administration");
    const access = (await (
      await accessRead
    ).json()) as components["schemas"]["AdministrationOverview"];
    const feedback = (await (
      await feedbackRead
    ).json()) as components["schemas"]["FeedbackOverview"];
    const ratings = (await (
      await ratingsRead
    ).json()) as components["schemas"]["RatingOverview"];
    expect((await accessRead).headers()["cache-control"]).toContain("no-store");
    await expect(
      page.getByRole("link", { name: "Administration", exact: true }),
    ).toHaveAttribute("aria-current", "page");
    const panel = page.getByRole("region", {
      name: "Catalog access",
      exact: true,
    });
    await expect(
      panel
        .getByRole("row", { name: /^People/ })
        .getByRole("cell")
        .first(),
    ).toHaveText(String(access.human.total.value));
    await expect(
      panel
        .getByRole("row", { name: /^Service identities/ })
        .getByRole("cell")
        .first(),
    ).toHaveText(String(access.service.total.value));
    await expect(panel.locator(".overview-count")).toHaveText(
      [
        access.activeGroups.value,
        access.activeRoles.total.value,
        access.activeAssignments.value,
      ].map(String),
    );
    const members = (
      await catalog.admin<components["schemas"]["MembershipPage"]>(
        "GET",
        "/api/v1/memberships?limit=100",
      )
    ).data;
    expect(members.nextCursor).toBeUndefined();
    expect(access.human.total.value).toBe(
      members.items.filter((row) => row.actorType === "human").length,
    );
    expect(access.service.total.value).toBe(
      members.items.filter((row) => row.actorType === "service").length,
    );
    const feedbackPanel = page.getByRole("region", {
      name: "Open feedback",
      exact: true,
    });
    await expect(feedbackPanel.locator(".overview-number")).toHaveText("3");
    expect(feedback.open.value).toBe(3);
    const ratingsPanel = page.getByRole("region", {
      name: "Ratings",
      exact: true,
    });
    await expect(ratingsPanel.locator(".overview-number")).toHaveText("1");
    expect(ratings.total.value).toBe(1);
    await expect.poll(() => overviewReads.length).toBe(3);
    expect(overviewReads.sort()).toEqual([
      "administration",
      "feedback",
      "ratings",
    ]);
    expect(mutations).toEqual([]);
    expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
      violations: [],
      incomplete: [],
    });
    const selection = page.waitForRequest((request) =>
      new URL(request.url()).pathname.endsWith("/api/v1/memberships"),
    );
    await panel
      .getByRole("row", { name: /^People/ })
      .getByRole("link", { name: "People: Suspended: 0", exact: true })
      .first()
      .click();
    const memberQuery = new URL((await selection).url()).searchParams;
    expect(memberQuery.get("state")).toBe("suspended");
    expect(memberQuery.get("actorType")).toBe("human");
    await expect(
      page.getByRole("combobox", { name: "State", exact: true }),
    ).toHaveValue("suspended");
    await page.reload();
    await expect(
      page.getByRole("combobox", { name: "Identity type filter", exact: true }),
    ).toHaveValue("human");
    await page
      .getByRole("combobox", { name: "State", exact: true })
      .selectOption("active");
    await page
      .getByRole("button", { name: "Apply filters", exact: true })
      .click();
    await expect(page).toHaveURL(/state=active&actorType=human$/);
    await openDisclosure(page, "Add Catalog member");
    const subject = page.getByRole("textbox", {
      name: "Subject",
      exact: true,
      includeHidden: true,
    });
    await revealControl(subject);
    await subject.fill("unsaved-overview-member");
    const declined = page.waitForEvent("dialog").then(async (dialog) => {
      expect(dialog.type()).toBe("confirm");
      expect(dialog.message()).toContain("Discard unsaved input");
      await dialog.dismiss();
    });
    await Promise.all([declined, page.goBack()]);
    await expect(page).toHaveURL(/state=active&actorType=human$/);
    await expect(subject).toHaveValue("unsaved-overview-member");
    await subject.fill("");
    await page
      .getByRole("button", { name: "Clear filters", exact: true })
      .click();
    await expect(page).toHaveURL(
      catalog.origin + catalog.basePath + "/members",
    );
    await page.goBack();
    await expect(
      page.getByRole("combobox", { name: "State", exact: true }),
    ).toHaveValue("active");
    await page.goForward();
    await expect(
      page.getByRole("combobox", { name: "State", exact: true }),
    ).toHaveValue("");
    for (const section of ["access-groups", "access-roles"]) {
      await page.goto(
        catalog.origin + catalog.basePath + "/overview?view=administration",
      );
      await page
        .locator(`a[href="${catalog.basePath}/${section}?state=active"]`)
        .click();
      await expect(
        page.getByRole("combobox", { name: "State", exact: true }),
      ).toHaveValue("active");
      await page.reload();
      await expect(
        page.getByRole("combobox", { name: "State", exact: true }),
      ).toHaveValue("active");
    }
    await page.goto(
      catalog.origin + catalog.basePath + "/overview?view=administration",
    );
    await page
      .getByRole("region", { name: "Open feedback", exact: true })
      .getByRole("link", { name: "Problem", exact: true })
      .first()
      .click();
    await expect(
      page.getByRole("combobox", { name: "Feedback state", exact: true }),
    ).toHaveValue("open");
    await expect(
      page.getByRole("combobox", { name: "Feedback category", exact: true }),
    ).toHaveValue("problem");
    await page
      .getByRole("combobox", { name: "Feedback category", exact: true })
      .selectOption("documentation");
    await expect(page).toHaveURL(/state=open&category=documentation$/);
    await page.reload();
    await expect(
      page.getByRole("combobox", { name: "Feedback category", exact: true }),
    ).toHaveValue("documentation");
    await page.goBack();
    await expect(
      page.getByRole("combobox", { name: "Feedback category", exact: true }),
    ).toHaveValue("problem");
    await page.goto(
      catalog.origin + catalog.basePath + "/overview?view=administration",
    );
    await page
      .getByRole("region", { name: "Ratings", exact: true })
      .getByRole("link", { name: "Active", exact: true })
      .first()
      .click();
    await expect(
      page.getByRole("combobox", { name: "Rating state", exact: true }),
    ).toHaveValue("active");
    await page.reload();
    await expect(
      page.getByRole("combobox", { name: "Rating state", exact: true }),
    ).toHaveValue("active");
    network.expectFailure("GET", "/_catalog/api/v1/ratings/inbox", 400);
    await page.goto(
      catalog.origin + catalog.basePath + "/ratings?state=active&state=removed",
    );
    await expect(page.getByRole("alert")).toBeVisible();
    await page
      .getByRole("combobox", { name: "Rating state", exact: true })
      .selectOption("");
    await expect(page.getByRole("alert")).toHaveCount(0);
    await page.setViewportSize({ width: 390, height: 1000 });
    await expect
      .poll(() => page.evaluate(() => document.documentElement.scrollWidth))
      .toBeLessThanOrEqual(390);
    expect(mutations).toEqual([]);
  });
}

for (const kind of ["feedback", "ratings"] as const) {
  test(`${kind}: a moderator without Resource reads defaults to its own Administration panel and loses it after revocation`, async ({
    page,
    catalog,
    network,
  }) => {
    await seedModeration(catalog);
    const permission =
      kind === "feedback" ? "feedback.moderate" : "ratings.moderate";
    const role = await catalog.admin<components["schemas"]["RoleDefinition"]>(
      "POST",
      "/api/v1/access/roles",
      {
        name: `${kind} summaries`,
        permissions: [permission],
        reason: "Qualify a minimal moderator",
      },
    );
    const members = await catalog.admin<
      components["schemas"]["MembershipPage"]
    >("GET", "/api/v1/memberships?subject=local-no-role&limit=25");
    const membership = required(members.data.items[0]);
    const assignment = await catalog.admin<
      components["schemas"]["AccessAssignment"]
    >("POST", "/api/v1/access/assignments", {
      roleDefinitionId: role.data.id,
      membershipId: membership.id,
      reason: "Qualify summary access",
    });
    const reads: string[] = [];
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (path.includes("/_catalog/api/v1/overview/"))
        reads.push(path.split("/").at(-1)!);
    });
    await login(page, catalog, "local-no-role", "/overview");
    await expect(
      page.getByRole("link", { name: "Administration", exact: true }),
    ).toHaveAttribute("aria-current", "page");
    const title = kind === "feedback" ? "Open feedback" : "Ratings";
    await expect(
      page
        .getByRole("region", { name: title, exact: true })
        .locator(".overview-number"),
    ).toHaveText(kind === "feedback" ? "3" : "1");
    expect(reads).toEqual([kind]);
    await expect(
      page.getByRole("region", { name: "Catalog access", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("link", { name: "Content", exact: true }),
    ).toHaveCount(0);
    await catalog.admin(
      "POST",
      `/api/v1/access/assignments/${assignment.data.id}:revoke`,
      { reason: "Qualify revoked summary access" },
      assignment.etag,
    );
    network.expectFailure("GET", `/_catalog/api/v1/overview/${kind}`, 403);
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect(
      page.getByRole("region", { name: title, exact: true }),
    ).toHaveCount(0);
    await expect(page.getByRole("alert")).toContainText(
      "does not include Overview",
    );
  });
}
