import { test, expect, login, type Catalog } from "./fixtures";
import type { Page } from "@playwright/test";
import { required } from "../required";
import { doubleText } from "./text-layout";

test.use({ webhooks: true });

function inputOffsets(rows: { x: number; y: number }[][]) {
  return rows.flatMap((fields) => {
    let previousX = Infinity;
    let firstY = 0;
    return fields.map((field) => {
      if (field.x <= previousX + 2) firstY = field.y;
      previousX = field.x;
      return Math.abs(field.y - firstY);
    });
  });
}

const groups = [
  [
    "content",
    ["resources", "reviews", "namespaces", "publishers", "publication"],
  ],
  [
    "operations",
    [
      "service",
      "jobs",
      "sources",
      "observations",
      "federation-search",
      "webhooks",
      "trust-assessments",
    ],
  ],
  [
    "administration",
    [
      "settings",
      "members",
      "access-groups",
      "access-roles",
      "access-assignments",
      "audit",
    ],
  ],
  [
    "account",
    [
      "access",
      "my-work",
      "saved-items",
      "feedback",
      "my-feedback",
      "ratings",
      "my-ratings",
    ],
  ],
  [
    "discovery and help",
    [
      "catalog",
      "explore",
      "api-reference",
      "getting-started",
      "about",
      "troubleshooting",
    ],
  ],
] as const;

for (const [group, paths] of groups) {
  test(`${group} pages align controls and contain enlarged narrow layouts`, async ({
    page,
    catalog,
  }) => {
    await login(page, catalog, "local-admin", `/${paths[0]}`);
    for (const path of paths) {
      await auditPage(page, catalog, path);
    }
  });
}

async function auditPage(page: Page, catalog: Catalog, path: string) {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto(`${catalog.origin}${catalog.basePath}/${path}`);
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await expect(
    page
      .locator("main [role=status]")
      .filter({ hasText: /^Loading(?:\.\.\.|\s)/ }),
  ).toHaveCount(0);
  if (path === "reviews") {
    await page
      .locator(".workspace-toolbar > .workspace-field")
      .first()
      .evaluate((field) => {
        field.querySelector("label > span")!.textContent =
          "Exact listing identifier for a submitted resource review and its retained revision";
        const hint = field.querySelector(".field-hint");
        if (hint)
          hint.textContent =
            "This deliberately long helper text checks that wrapped lines do not shift the neighboring input. The value remains an exact identifier and this layout probe sends no request.";
      });
  }
  const readRows = () =>
    page
      .locator(".workspace-toolbar:has(> .workspace-field)")
      .evaluateAll((toolbars) =>
        toolbars.map((toolbar) =>
          Array.from(toolbar.children)
            .filter((field) => field.classList.contains("workspace-field"))
            .map((field) => {
              const input = field.querySelector(
                "input:not([type=checkbox]):not([type=radio]),select,textarea",
              );
              if (!input) return null;
              const box = input.getBoundingClientRect();
              return box.width && box.height ? { x: box.x, y: box.y } : null;
            })
            .filter((item) => item !== null),
        ),
      );
  if (path === "reviews") {
    const second = page.locator(".workspace-toolbar > .workspace-field").nth(1);
    await second.evaluate((field) => {
      (field as HTMLElement).style.transform = "translateY(12px)";
    });
    expect(
      Math.max(...inputOffsets(await readRows())),
      "detect staggered whole fields, even when their labels also move",
    ).toBeGreaterThanOrEqual(10);
    await second.evaluate((field) => {
      (field as HTMLElement).style.removeProperty("transform");
    });
  }
  for (const offset of inputOffsets(await readRows()))
    expect(offset, `${path}: corresponding input rows`).toBeLessThanOrEqual(2);
  const lookups = await page
    .locator('.workspace-lookup, form[aria-label^="Open exact"]')
    .evaluateAll((forms) =>
      forms.flatMap((form) => {
        const input = form.querySelector(".workspace-field input");
        const action = form.querySelector(
          ".workspace-actions > button, .form-actions > button, :scope > button",
        );
        if (!input || !action) return [];
        const control = input.getBoundingClientRect();
        const button = action.getBoundingClientRect();
        return control.width && button.width
          ? [
              {
                offset: Math.abs(control.y - button.y),
                gap: button.x - control.right,
              },
            ]
          : [];
      }),
    );
  for (const lookup of lookups) {
    expect(
      lookup.offset,
      `${path}: lookup action shares the input row`,
    ).toBeLessThanOrEqual(2);
    expect(
      lookup.gap,
      `${path}: lookup has an explicit column gap`,
    ).toBeGreaterThanOrEqual(8);
  }
  const actingIdentity = page.locator(".operation-summary > strong");
  const longIdentity = `Acting as ${"u".repeat(128)}`;
  if (path === "settings") {
    // Stress the same text node rendered from an opaque OIDC subject;
    // the isolated context cases separately verify its exact full value.
    await actingIdentity.evaluate((element, text) => {
      element.textContent = text;
    }, longIdentity);
  }
  for (const width of [768, 320]) {
    await page.setViewportSize({ width, height: 844 });
    if (width === 320) {
      await page.evaluate(() => {
        document.documentElement.dataset.theme = "dark";
        document.documentElement.dataset.density = "compact";
      });
      await doubleText(page);
    }
    await expect
      .poll(() => page.evaluate(() => document.documentElement.scrollWidth), {
        message: `${path}: page fits ${width}px`,
      })
      .toBeLessThanOrEqual(width);
    if (path.startsWith("catalog/")) {
      const clippedBadges = await page
        .locator("main .badge")
        .evaluateAll((elements) =>
          elements
            .filter((element) => {
              const box = element.getBoundingClientRect();
              return (
                box.width > 0 &&
                (box.left < -1 ||
                  box.right > innerWidth + 1 ||
                  element.scrollWidth > element.clientWidth + 1 ||
                  element.scrollHeight > element.clientHeight + 1)
              );
            })
            .map((element) => element.textContent),
        );
      expect(
        clippedBadges,
        `${path}: full badges wrap without clipping`,
      ).toEqual([]);
    }
    if (path === "settings") {
      await expect(actingIdentity).toHaveText(longIdentity);
      const identity = await actingIdentity.evaluate((element) => {
        const box = element.getBoundingClientRect();
        return {
          left: box.left,
          right: box.right,
          horizontalClip: element.scrollWidth > element.clientWidth,
          verticalClip: element.scrollHeight > element.clientHeight,
        };
      });
      expect(identity.left).toBeGreaterThanOrEqual(0);
      expect(identity.right).toBeLessThanOrEqual(width);
      expect(identity.horizontalClip).toBe(false);
      expect(identity.verticalClip).toBe(false);
    }
    const clipped = await page
      .locator(
        "main input:not([type=checkbox]):not([type=radio]), main select, main textarea",
      )
      .evaluateAll((elements) =>
        elements
          .filter((element) => {
            const box = element.getBoundingClientRect();
            return (
              box.width > 0 &&
              box.height > 0 &&
              (box.x < -1 || box.right > innerWidth + 1)
            );
          })
          .map(
            (element) =>
              element.getAttribute("aria-label") ||
              element.id ||
              element.tagName,
          ),
      );
    expect(clipped, `${path}: no clipped native controls`).toEqual([]);
  }
}

// Real retained records supplement the directory checks: a generic SPA response
// cannot establish that a linked editor/detail route rendered the right record.
for (const base of ["root", "subpath"]) {
  test(`${base}: linked content and administration pages fit enlarged narrow layouts`, async ({
    page,
    catalog,
  }) => {
    const published = required(
      catalog.receipt.items.find((item) => item.key === "order-agent"),
    );
    const pending = required(
      catalog.receipt.items.find((item) => item.key === "pending-mcp"),
    );
    const version = await catalog.admin<{
      revision: { resourceVersionId: string };
    }>("GET", `/api/v1/version-revisions/${published.versionRevisionId}`);
    const listing = await catalog.admin<{ candidateRevisionId: string }>(
      "GET",
      `/api/v1/catalog-listings/${published.listingId}`,
    );
    const snapshot = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/publication/snapshots",
      { host: "127.0.0.1" },
    );
    const members = await catalog.admin<{
      items: { id: string; subject: string }[];
    }>("GET", "/api/v1/memberships?limit=100");
    const member = required(
      members.data.items.find((item) => item.subject === "local-reader"),
    );
    const roles = await catalog.admin<{
      items: { id: string; key?: string; builtin: boolean }[];
    }>("GET", "/api/v1/access/roles?limit=100");
    const role = required(
      roles.data.items.find((item) => item.builtin && item.key === "reader"),
    );
    const group = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/access/groups",
      {
        name: "Layout review group",
        reason: "Owned linked-route layout fixture",
      },
    );
    await catalog.admin<{ id: string }>("POST", "/api/v1/access/assignments", {
      roleDefinitionId: role.id,
      groupId: group.data.id,
      reason: "Owned linked assignment layout fixture",
    });
    const saved = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/personal/items",
      {
        type: "search",
        label: "Saved layout review",
        search: { text: "Owned criteria, never executed" },
      },
    );
    const paths = [
      "resources/new",
      `resources/${published.resourceId}#listings`,
      `versions/new?resource=${published.resourceId}`,
      `versions/${version.data.revision.resourceVersionId}`,
      `version-revisions/${published.versionRevisionId}`,
      `listings/${published.listingId}#release-history`,
      `listing-revisions/${listing.data.candidateRevisionId}`,
      `reviews/${required(pending.submissionId)}`,
      `releases/${required(published.releaseId)}`,
      `catalog/${required(published.releaseId)}`,
      "namespaces/new",
      `namespaces/${catalog.receipt.namespaceId}`,
      "publishers/new",
      `publishers/${catalog.receipt.publisherId}`,
      "publication?host=127.0.0.1",
      `publication/${snapshot.data.id}?host=127.0.0.1`,
      `members/${member.id}`,
      `access-roles/${role.id}`,
      `access-groups/${group.data.id}`,
      `access-assignments?groupId=${group.data.id}`,
      "access-roles/new",
      "access-groups/new",
      "saved-items/new",
      `saved-items/${saved.data.id}`,
      "webhooks/new",
      "sources/new",
    ];
    await login(page, catalog, "local-admin", `/${paths[0]}`);
    const writes: string[] = [];
    page.on("request", (request) => {
      if (
        request.method() !== "GET" &&
        new URL(request.url()).pathname.includes("/_catalog/api/")
      )
        writes.push(request.url());
    });
    for (const path of paths) {
      await test.step(path, async () => {
        await auditPage(page, catalog, path);
        await expect(page.locator("main [role=alert]")).toHaveCount(0);
        await expect(page.locator("main")).not.toContainText(
          "This page is unavailable with your current access",
        );
      });
    }
    expect(writes).toEqual([]);
  });
}
