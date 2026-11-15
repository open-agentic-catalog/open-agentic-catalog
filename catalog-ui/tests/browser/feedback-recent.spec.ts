// Recent private outcomes use real owned API state, not intercepted responses.
import { test, expect, login } from "./fixtures";
import type { Catalog } from "./fixtures";
import { required } from "../required";
import AxeBuilder from "@axe-core/playwright";
import { doubleText } from "./text-layout";

/** Retain owned cases through ordinary human writes before observing browser reads. */
async function cases(
  catalog: Catalog,
  count: number,
  alias: "local-reader" | "local-no-role" = "local-reader",
) {
  const resourceId = required(catalog.receipt.items[0]).resourceId;
  const rows = [];
  for (let i = 0; i < count; i++) {
    const reply = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/feedback",
      {
        resourceId,
        category: "problem",
        comment: `Owned feedback ${i}`,
      },
      undefined,
      alias,
    );
    rows.push({ id: reply.data.id, etag: reply.etag });
  }
  return rows.sort((a, b) => a.id.localeCompare(b.id));
}

for (const location of ["root", "subpath"]) {
  test(`${location}: recent addressed outcomes find old replies across pages and refresh current facts`, async ({
    page,
    catalog,
  }, testInfo) => {
    const rows = await cases(catalog, 42);
    const target = required(rows[25]);
    const first = await catalog.admin<{ items: { id: string }[] }>(
      "GET",
      "/api/v1/feedback?limit=20",
      undefined,
      undefined,
      "local-reader",
    );
    expect(first.data.items).toHaveLength(20);
    expect(first.data.items.some((row) => row.id === target.id)).toBe(false);
    for (const [index, row] of rows.entries()) {
      if (row.id === target.id) continue;
      await catalog.admin(
        "POST",
        `/api/v1/feedback/${row.id}:address`,
        { response: index === 0 ? "" : `Current outcome ${index}` },
        row.etag,
      );
    }
    await catalog.admin(
      "POST",
      `/api/v1/feedback/${target.id}:address`,
      { response: "Latest moderator reply to an older submission" },
      target.etag,
    );
    await login(page, catalog, "local-reader", "/my-feedback");
    const reads: string[] = [],
      writes: string[] = [];
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (
        url.pathname.endsWith("/api/v1/feedback") &&
        request.method() === "GET"
      )
        reads.push(request.url());
      if (
        url.pathname.includes("/_catalog/api/") &&
        !["GET", "HEAD", "OPTIONS"].includes(request.method())
      )
        writes.push(request.url());
    });
    const records = page.locator(".case-summary");
    await expect(records).toHaveCount(20);
    const response = page.waitForResponse(
      (r) =>
        new URL(r.url()).searchParams.get("order") === "updated_desc" &&
        new URL(r.url()).pathname.endsWith("/api/v1/feedback"),
    );
    await page
      .getByRole("button", { name: "Recent addressed outcomes", exact: true })
      .click();
    expect((await response).status()).toBe(200);
    await expect(records.first()).toContainText(
      "Latest moderator reply to an older submission",
    );
    await expect(
      records
        .first()
        .getByRole("link", { name: "Open feedback case", exact: true }),
    ).toHaveAttribute("href", `${catalog.basePath}/my-feedback/${target.id}`);
    await expect(page).toHaveURL(/state=addressed&order=updated_desc$/);
    const seen: string[] = [];
    for (const count of [20, 20, 2]) {
      await expect(records).toHaveCount(count);
      const hrefs = await records
        .getByRole("link", { name: "Open feedback case", exact: true })
        .evaluateAll((links) =>
          links.map((link) => link.getAttribute("href") ?? ""),
        );
      seen.push(...hrefs.map((href) => required(href.split("/").at(-1))));
      if (count === 20)
        await page
          .getByRole("button", { name: "Next page", exact: true })
          .click();
    }
    expect(new Set(seen).size).toBe(42);
    expect([...seen].sort()).toEqual(rows.map((row) => row.id).sort());
    await expect(
      page
        .getByText("Addressed without a written response.", { exact: false })
        .first(),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Refresh feedback", exact: true })
      .click();
    await expect(records).toHaveCount(20);
    await expect(records.first()).toContainText(
      "Latest moderator reply to an older submission",
    );
    expect(new URL(required(reads.at(-1))).searchParams.has("cursor")).toBe(
      false,
    );
    await page.getByRole("button", { name: "Next page", exact: true }).click();
    await expect(records).toHaveCount(20);
    const opened = records
      .first()
      .getByRole("link", { name: "Open feedback case", exact: true });
    const openedHref = required(
      (await opened.getAttribute("href")) ?? undefined,
    );
    const openedID = required(openedHref.split("/").at(-1));
    await opened.click();
    await expect(page).toHaveURL(new RegExp(`/my-feedback/${openedID}$`));
    await expect(
      page.getByRole("heading", { name: "Moderator response", exact: true }),
    ).toBeVisible();
    const current = page.waitForResponse(
      (response) =>
        response.request().method() === "GET" &&
        new URL(response.url()).pathname.endsWith(
          `/api/v1/feedback/${openedID}`,
        ),
    );
    await page
      .getByRole("button", { name: "Read current feedback", exact: true })
      .click();
    expect((await current).status()).toBe(200);
    await page
      .getByRole("link", { name: "Return to feedback list", exact: true })
      .click();
    await expect(page).toHaveURL(/state=addressed&order=updated_desc$/);
    await expect(records).toHaveCount(20);
    expect(new URL(required(reads.at(-1))).searchParams.has("cursor")).toBe(
      true,
    );
    await page
      .getByRole("button", { name: "Refresh feedback", exact: true })
      .click();
    await expect(records.first()).toContainText(
      "Latest moderator reply to an older submission",
    );
    await page.reload();
    await expect(records.first()).toContainText(
      "Latest moderator reply to an older submission",
    );
    await page
      .getByRole("combobox", { name: "Feedback category", exact: true })
      .selectOption("documentation");
    await expect(
      page.getByText("No feedback matches these filters.", { exact: true }),
    ).toBeVisible();
    await page.goBack();
    await expect(records).toHaveCount(20);
    const controls = await Promise.all([
      page
        .getByRole("combobox", { name: "Feedback category", exact: true })
        .boundingBox(),
      page
        .getByRole("combobox", { name: "Feedback state", exact: true })
        .boundingBox(),
      page
        .getByRole("button", { name: "Refresh feedback", exact: true })
        .boundingBox(),
    ]);
    const firstControl = required(controls[0] ?? undefined);
    for (const control of controls) {
      expect(
        Math.abs(required(control ?? undefined).y - firstControl.y),
      ).toBeLessThanOrEqual(2);
    }
    await page.setViewportSize({ width: 320, height: 800 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
      violations: [],
      incomplete: [],
    });
    await page.screenshot({
      path: testInfo.outputPath("recent-feedback-mobile.png"),
      fullPage: true,
    });
    await page.emulateMedia({ colorScheme: "dark" });
    await doubleText(page);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page
      .getByRole("button", { name: "Submission order", exact: true })
      .click();
    await expect(records).toHaveCount(20);
    await expect(
      records
        .first()
        .getByRole("link", { name: "Open feedback case", exact: true }),
    ).toHaveAttribute(
      "href",
      `${catalog.basePath}/my-feedback/${required(rows[0]).id}`,
    );
    expect(writes).toEqual([]);
    expect(
      reads.every((url) => new URL(url).searchParams.get("limit") === "20"),
    ).toBe(true);
  });
}

test("recent own feedback stays readable without Resource authority and refuses revoked own authority", async ({
  page,
  catalog,
  network,
}) => {
  const members = await catalog.admin<{
    items: { id: string; subject: string }[];
  }>("GET", "/api/v1/memberships?limit=100");
  const member = required(
    members.data.items.find((item) => item.subject === "local-no-role"),
  );
  const ownRole = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/access/roles",
    {
      name: "Recent feedback owner",
      permissions: ["feedback.own"],
      reason: "Owned browser feedback test",
    },
  );
  const readRole = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/access/roles",
    {
      name: "Temporary feedback Resource reader",
      permissions: ["resource.read"],
      reason: "Owned creation prerequisite",
    },
  );
  const own = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/access/assignments",
    {
      membershipId: member.id,
      roleDefinitionId: ownRole.data.id,
      reason: "Read own cases only",
    },
  );
  const read = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/access/assignments",
    {
      membershipId: member.id,
      roleDefinitionId: readRole.data.id,
      reason: "Create the owned feedback prerequisites",
    },
  );
  const rows = await cases(catalog, 26, "local-no-role");
  for (const row of rows)
    await catalog.admin(
      "POST",
      `/api/v1/feedback/${row.id}:address`,
      { response: "Private owned recent reply" },
      row.etag,
    );
  await catalog.admin(
    "POST",
    `/api/v1/access/assignments/${read.data.id}:revoke`,
    { reason: "Withdraw independent Resource-read authority" },
    read.etag,
  );
  const resourceReads: string[] = [];
  page.on("request", (request) => {
    if (new URL(request.url()).pathname.endsWith("/api/v1/resources"))
      resourceReads.push(request.url());
  });
  await login(
    page,
    catalog,
    "local-no-role",
    "/my-feedback?state=addressed&order=updated_desc",
  );
  await expect(page.locator(".case-summary")).toHaveCount(20);
  await expect(page.locator(".case-summary").first()).toContainText(
    "Private owned recent reply",
  );
  expect(resourceReads).toEqual([]);
  await catalog.admin(
    "POST",
    `/api/v1/access/assignments/${own.data.id}:revoke`,
    { reason: "Withdraw feedback authority between pages" },
    own.etag,
  );
  network.expectFailure("GET", "/_catalog/api/v1/feedback", 400);
  await page.getByRole("button", { name: "Next page", exact: true }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(page.locator(".case-summary")).toHaveCount(0);
  const denied = await page.request.get(
    `${catalog.origin}${catalog.basePath}/_catalog/api/v1/feedback?order=updated_desc&state=addressed&limit=20`,
  );
  expect(denied.status()).toBe(403);
  expect(denied.headers()["cache-control"]).toContain("no-store");
  expect(await denied.text()).not.toContain("Private owned recent reply");
});
