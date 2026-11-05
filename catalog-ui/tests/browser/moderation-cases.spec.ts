import { openDisclosure, revealControl } from "./editor-disclosure";
// Owned API/browser cases; explicit read faults and lost replies after real committed writes.
import { test, expect, login, confirmCommand } from "./fixtures";
import { required } from "../required";
import { text } from "./json";
import AxeBuilder from "@axe-core/playwright";
import type { components } from "../../src/api/schema";

test.describe("exact moderation cases", () => {
  test.use({ curated: true });
  test("subpath rating lookup beyond page one recovers a real second-client conflict", async ({
    page,
    catalog,
    network,
  }) => {
    const votes: string[] = [];
    const ratedItems = catalog.receipt.items
      .filter((item) => item.releaseId)
      .slice(0, 9);
    for (const item of ratedItems) {
      const record = await catalog.admin<
        components["schemas"]["CatalogRecord"]
      >("GET", `/api/v1/catalog/resources/${item.releaseId}`);
      for (const actor of [
        "local-admin",
        "local-reviewer",
        "local-publisher",
      ] as const) {
        const receipt = await catalog.admin<{ id: string }>(
          "POST",
          "/api/v1/ratings",
          {
            target: {
              origin: "local",
              resourceId: item.resourceId,
              versionRevisionId: item.versionRevisionId,
              identifier: item.identifier,
              kind: record.data.resource.kind,
              versionPresent: true,
              version: record.data.version.version,
            },
            releaseId: item.releaseId,
            score: 4,
            consent: false,
          },
          undefined,
          actor,
        );
        votes.push(receipt.data.id);
      }
    }
    expect(votes).toHaveLength(27);
    votes.sort();
    const id = required(votes.at(-1));
    const memberships = await catalog.admin<{
      items: { id: string; subject: string }[];
    }>("GET", "/api/v1/memberships?limit=100");
    const member = required(
      memberships.data.items.find((value) => value.subject === "local-no-role"),
    );
    const role = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/access/roles",
      {
        name: "Case moderation only",
        permissions: ["ratings.moderate"],
        reason: "Owned exact case qualification",
      },
    );
    await catalog.admin("POST", "/api/v1/access/assignments", {
      membershipId: member.id,
      roleDefinitionId: role.data.id,
      reason: "Moderate without resource reads",
    });
    await login(page, catalog, "local-no-role", "/ratings");
    await page
      .getByRole("combobox", { name: "Rating state", exact: true })
      .selectOption("active");
    await expect(page).toHaveURL(
      `${catalog.origin}${catalog.basePath}/ratings?state=active`,
    );
    await expect(page.locator("article.case-summary")).toHaveCount(25);
    const summaries = page.locator("article.case-summary");
    const headings = summaries.getByRole("heading", { level: 2 });
    const exactIdentifiers = await headings.allTextContents();
    for (const identifier of exactIdentifiers)
      expect(ratedItems.map((item) => item.identifier)).toContain(identifier);
    await expect(summaries.locator("details[open]")).toHaveCount(0);
    const initialViewport = required(page.viewportSize() ?? undefined);
    for (const width of [320, 390]) {
      await page.setViewportSize({ width, height: 844 });
      await expect(headings).toHaveText(exactIdentifiers);
      const geometry = await headings.evaluateAll((nodes) =>
        nodes.map((node) => {
          const heading = node.getBoundingClientRect();
          const card = node.parentElement!.getBoundingClientRect();
          return {
            left: heading.left,
            right: heading.right,
            cardLeft: card.left,
            cardRight: card.right,
            clippedHorizontally: node.scrollWidth > node.clientWidth,
            clippedVertically: node.scrollHeight > node.clientHeight,
          };
        }),
      );
      for (const heading of geometry) {
        expect(heading.left).toBeGreaterThanOrEqual(heading.cardLeft);
        expect(heading.right).toBeLessThanOrEqual(heading.cardRight + 1);
        expect(heading.right).toBeLessThanOrEqual(width);
        expect(heading.clippedHorizontally).toBe(false);
        expect(heading.clippedVertically).toBe(false);
      }
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth),
      ).toBeLessThanOrEqual(width);
    }
    await page.setViewportSize(initialViewport);
    await expect(
      page
        .locator("article")
        .filter({ has: page.locator("code").filter({ hasText: id }) }),
    ).toHaveCount(0);
    await openDisclosure(page, "Open a case by ID");
    await page.getByLabel("Exact rating ID").fill(id.toUpperCase());
    await page
      .getByRole("button", { name: "Open rating case", exact: true })
      .click();
    await expect(page).toHaveURL(
      `${catalog.origin}${catalog.basePath}/ratings/${id}`,
    );
    await expect(
      page.getByRole("button", { name: "Copy rating ID", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Resource and versions", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByText("submitter membership ID", { exact: true }),
    ).toHaveCount(0);
    expect(await page.evaluate(() => history.state as unknown)).toEqual({
      caseNavigation: 2,
    });
    const prior = await catalog.admin<components["schemas"]["Rating"]>(
      "GET",
      `/api/v1/ratings/${id}`,
    );
    await catalog.admin(
      "POST",
      `/api/v1/ratings/${id}:remove`,
      { reason: "Independent second-client revision change" },
      prior.etag,
    );
    await page
      .getByRole("button", { name: "Remove rating", exact: true })
      .click();
    await page
      .getByRole("textbox", { name: "Moderation reason", exact: true })
      .fill("Preserve conflict reason");
    network.expectFailure("POST", `/_catalog/api/v1/ratings/${id}:remove`, 412);
    await page
      .getByRole("button", { name: "Remove rating", exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toContainText("The record changed");
    await page
      .getByRole("button", { name: "Back to form", exact: true })
      .click();
    await page.route(
      `**/_catalog/api/v1/ratings/${id}`,
      (route) =>
        route.fulfill({
          status: 503,
          json: { code: "DEPENDENCY_UNAVAILABLE" },
        }),
      { times: 1 },
    );
    network.expectFailure("GET", `/_catalog/api/v1/ratings/${id}`, 503);
    await page
      .getByRole("button", { name: "Read current rating", exact: true })
      .click();
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(
      page.getByRole("textbox", { name: "Moderation reason", exact: true }),
    ).toHaveValue("Preserve conflict reason");
    await page.getByRole("button", { name: "Reload", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "Permit new submission", exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Permit new submission", exact: true })
      .click();
    await page
      .getByRole("region", { name: "Selected rating moderation", exact: true })
      .getByRole("button", { name: "Permit new submission", exact: true })
      .click();
    await confirmCommand(page);
    await expect(page.getByText(/Version:.*Withdrawn/)).toBeVisible();
    await expect(page.getByRole("status")).toContainText(
      "Rating state updated.",
    );
    await page
      .getByRole("link", { name: "Return to rating list", exact: true })
      .click();
    await expect(
      page.getByRole("combobox", { name: "Rating state", exact: true }),
    ).toHaveValue("active");
    await expect(page.locator("article.case-summary")).toHaveCount(25);
    await page.getByRole("button", { name: "Next page", exact: true }).click();
    await expect(page.locator("article.case-summary")).toHaveCount(1);
    await catalog.admin(
      "PATCH",
      `/api/v1/access/roles/${role.data.id}`,
      {
        name: "Case moderation only",
        permissions: ["ratings.moderate", "feedback.moderate"],
        reason:
          "Invalidate the captured cursor through a real permission change",
      },
      role.etag,
    );
    await page
      .getByRole("button", { name: "Remove rating", exact: true })
      .first()
      .click();
    await page
      .getByRole("textbox", { name: "Moderation reason", exact: true })
      .fill("Keep reason through cursor rejection");
    network.expectFailure("GET", "/_catalog/api/v1/ratings/inbox", 400);
    await page
      .getByRole("button", { name: "Refresh ratings", exact: true })
      .click();
    await expect(page.getByRole("alert")).toBeVisible();
    await page.getByRole("button", { name: "First page", exact: true }).click();
    await expect(page.locator("article.case-summary")).toHaveCount(25);
    await expect(
      page.getByRole("combobox", { name: "Rating state", exact: true }),
    ).toHaveValue("active");
    await expect(
      page.getByRole("textbox", { name: "Moderation reason", exact: true }),
    ).toHaveValue("Keep reason through cursor rejection");
    const current = await catalog.admin<components["schemas"]["Rating"]>(
      "GET",
      `/api/v1/ratings/${id}`,
    );
    expect(current.data).toMatchObject({
      state: "withdrawn",
      score: 0,
      consent: false,
      revision: prior.data.revision + 2,
    });
    expect(current.data).not.toHaveProperty("principalKey");
    expect(current.data).not.toHaveProperty("submittedBy");
  });
});

for (const variant of ["root", "subpath"]) {
  test(`${variant} private feedback exact lookup retains filters and guards browser Back`, async ({
    page,
    catalog,
    network,
  }) => {
    const item = required(catalog.receipt.items[0]);
    const cases: string[] = [];
    for (let i = 0; i < 21; i++) {
      const receipt = await catalog.admin<{ id: string }>(
        "POST",
        "/api/v1/feedback",
        {
          resourceId: item.resourceId,
          category: "documentation",
          comment: `Synthetic case ${i + 1}`,
        },
      );
      cases.push(receipt.data.id);
    }
    cases.sort();
    const id = required(cases.at(-1));
    await login(page, catalog, "local-admin", "/feedback");
    await page
      .getByRole("combobox", { name: "Feedback category", exact: true })
      .selectOption("documentation");
    await page
      .getByRole("combobox", { name: "Feedback state", exact: true })
      .selectOption("open");
    await expect(page).toHaveURL(
      `${catalog.origin}${catalog.basePath}/feedback?state=open&category=documentation`,
    );
    await revealControl(page.getByLabel("Resource ID filter (optional)"));
    await page
      .getByLabel("Resource ID filter (optional)")
      .fill(item.resourceId);
    await page
      .getByRole("button", { name: "Apply resource filter", exact: true })
      .click();
    await expect(page.locator("article.workspace-panel")).toHaveCount(20);
    await openDisclosure(page, "Open a case by ID");
    await page.getByLabel("Exact feedback ID").fill(id.toUpperCase());
    await page
      .getByRole("button", { name: "Open feedback case", exact: true })
      .click();
    await expect(page).toHaveURL(
      `${catalog.origin}${catalog.basePath}/feedback/${id}`,
    );
    await openDisclosure(page, "Identifiers and revision");
    await expect(
      page.getByRole("button", {
        name: "Copy submitter membership ID",
        exact: true,
      }),
    ).toBeVisible();
    await page.getByText("Respond or manage feedback", { exact: true }).click();
    await page
      .getByLabel("Moderator response (optional)")
      .fill("The documentation now includes this example.");
    expect(
      await page.evaluate(() =>
        window.dispatchEvent(
          new Event("catalog-ui:leave", { cancelable: true }),
        ),
      ),
    ).toBe(false);
    expect(await page.evaluate(() => history.state as unknown)).toEqual({
      caseNavigation: 3,
    });
    const discard = page.waitForEvent("dialog").then(async (dialog) => {
      expect(dialog.type()).toBe("confirm");
      await dialog.dismiss();
    });
    await Promise.all([discard, page.evaluate(() => history.back())]);
    await expect(page).toHaveURL(
      `${catalog.origin}${catalog.basePath}/feedback/${id}`,
    );
    await expect(page.getByLabel("Moderator response (optional)")).toHaveValue(
      "The documentation now includes this example.",
    );
    const before = await catalog.admin<components["schemas"]["Feedback"]>(
      "GET",
      `/api/v1/feedback/${id}`,
    );
    expect(before.data.state).toBe("open");
    const auditPath = `/api/v1/audit-events?${new URLSearchParams({
      action: "feedback.address",
      targetType: "feedback",
      targetId: id,
      limit: "100",
    })}`;
    const auditBefore = await catalog.admin<
      components["schemas"]["AuditEventPage"]
    >("GET", auditPath);
    expect(auditBefore.data.items).toEqual([]);
    const commands: {
      method: string;
      url: string;
      key: string | undefined;
      etag: string | undefined;
      body: string | null;
    }[] = [];
    network.expectFailure(
      "POST",
      `/_catalog/api/v1/feedback/${id}:address`,
      503,
    );
    await page.route(
      `**/_catalog/api/v1/feedback/${id}:address`,
      async (route) => {
        const request = route.request();
        const headers = request.headers();
        commands.push({
          method: request.method(),
          url: request.url(),
          key: headers["idempotency-key"],
          etag: headers["if-match"],
          body: request.postData(),
        });
        const response = await route.fetch();
        expect(response.status()).toBe(200);
        // The real service commits first. Only its reply to the browser is lost.
        expect(await response.json()).toEqual({
          id,
          revision: before.data.revision + 1,
        });
        if (commands.length === 1)
          await route.fulfill({
            status: 503,
            contentType: "application/problem+json",
            body: JSON.stringify({ code: "UNAVAILABLE" }),
          });
        else {
          expect(response.headers()["idempotency-replayed"]).toBe("true");
          await route.fulfill({ response });
        }
      },
    );
    await page
      .getByRole("button", { name: "Address feedback", exact: true })
      .click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: "Confirm", exact: true }).click();
    await expect(dialog.getByRole("alert")).toContainText("outcome is unknown");
    expect(commands).toHaveLength(1);
    expect(required(commands[0]).key).toMatch(/^[0-9a-f-]{36}$/);
    expect(required(commands[0]).etag).toBe(before.etag);
    expect(JSON.parse(text(required(commands[0]).body))).toEqual({
      response: "The documentation now includes this example.",
    });
    await expect(
      dialog.getByRole("button", { name: "Back to form", exact: true }),
    ).toBeDisabled();
    // A separate current read proves the mutation succeeded before the retry.
    const committed = await catalog.admin<components["schemas"]["Feedback"]>(
      "GET",
      `/api/v1/feedback/${id}`,
    );
    expect(committed.data).toMatchObject({
      id,
      state: "addressed",
      revision: before.data.revision + 1,
      comment: before.data.comment,
      response: "The documentation now includes this example.",
    });
    await dialog
      .getByRole("button", { name: "Retry same command", exact: true })
      .click();
    await expect(dialog).toHaveCount(0);
    await expect(
      page.getByText("Feedback updated.", { exact: true }),
    ).toBeVisible();
    expect(commands).toHaveLength(2);
    expect(commands[1]).toEqual(commands[0]);
    const after = await catalog.admin<components["schemas"]["Feedback"]>(
      "GET",
      `/api/v1/feedback/${id}`,
    );
    expect(after.data).toEqual(committed.data);
    const auditAfter = await catalog.admin<
      components["schemas"]["AuditEventPage"]
    >("GET", auditPath);
    expect(auditAfter.data.items).toHaveLength(1);
    expect(required(auditAfter.data.items[0])).toMatchObject({
      action: "feedback.address",
      targetType: "feedback",
      targetId: id,
    });
    const auditText = JSON.stringify(auditAfter.data.items);
    expect(auditText).not.toContain(
      "The documentation now includes this example.",
    );
    expect(auditText).not.toContain(required(before.data.comment));
    expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
      violations: [],
      incomplete: [],
    });
    if (variant === "root") {
      await page.screenshot({
        path: test.info().outputPath("feedback-case.png"),
        fullPage: true,
      });
    }
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    await page
      .getByRole("link", { name: "Return to feedback list", exact: true })
      .click();
    await expect(
      page.getByRole("combobox", { name: "Feedback category", exact: true }),
    ).toHaveValue("documentation");
    await expect(
      page.getByRole("combobox", { name: "Feedback state", exact: true }),
    ).toHaveValue("open");
    await expect(page.getByLabel("Resource ID filter (optional)")).toHaveValue(
      item.resourceId,
    );
    await expect(page.locator("article.workspace-panel")).toHaveCount(20);
    await openDisclosure(page, "Open a case by ID");
    await page.getByLabel("Exact feedback ID").fill(id.toUpperCase());
    await page
      .getByRole("button", { name: "Open feedback case", exact: true })
      .click();
    await page.reload();
    await expect(
      page.getByRole("link", { name: "Open feedback list", exact: true }),
    ).toBeVisible();
    await page
      .getByRole("link", { name: "Open feedback list", exact: true })
      .click();
    await expect(
      page.getByRole("combobox", { name: "Feedback state", exact: true }),
    ).toHaveValue("");
    await expect(
      page.getByRole("combobox", { name: "Feedback category", exact: true }),
    ).toHaveValue("");
    await expect(page.getByLabel("Resource ID filter (optional)")).toHaveValue(
      "",
    );
  });
}
