import { test, expect, login, openAccount, signOut } from "./fixtures";
import { expectNaturalText } from "./text-layout";
import { required } from "../required";
import type { components } from "../../src/api/schema";

const maintainedTest = test.extend({ maintainedIdentities: true });

maintainedTest(
  "the six development bundles retain exact grants through real sign-in and sign-out",
  async ({ page, catalog }) => {
    for (const [alias, expectedKeys, task] of [
      ["local-admin", ["tenant_admin"], "Roles"],
      [
        "local-reader",
        ["reader", "discovery_reader", "feedback_member"],
        "Explore local catalog",
      ],
      [
        "local-contributor",
        [
          "contributor",
          "discovery_reader",
          "feedback_member",
          "publisher_creator",
        ],
        "Authoring",
      ],
      [
        "local-reviewer",
        ["reviewer", "discovery_reader", "feedback_member"],
        "Reviews",
      ],
      [
        "local-publisher",
        [
          "publisher",
          "discovery_reader",
          "feedback_member",
          "publication_admin",
        ],
        "Discovery publication",
      ],
      ["other-tenant-admin", ["tenant_admin"], "Roles"],
    ] as const) {
      await login(page, catalog, alias, "/access");
      const paths = await page.evaluate(async () => {
        const reply = await fetch("/_catalog/api/v1/access/self?limit=100", {
          headers: { "X-Catalog-Request": "1" },
        });
        if (reply.status !== 200)
          throw new Error("Bundle self access unavailable");
        return (await reply.json()) as components["schemas"]["EffectiveAccessPage"];
      });
      expect(paths.nextCursor).toBeUndefined();
      expect(paths.items.map((item) => item.role.key).sort()).toEqual(
        [...expectedKeys].sort(),
      );
      expect(
        paths.items.every(
          (item) => !item.assignment.namespaceId && !item.assignment.groupId,
        ),
      ).toBe(true);
      await expect(
        page.getByRole("definition").filter({
          hasText: alias === "other-tenant-admin" ? "other-admin" : alias,
        }),
      ).toBeVisible();
      await expect(
        page.getByRole("definition").filter({
          hasText:
            alias === "other-tenant-admin"
              ? "00000000-0000-7000-8000-000000000002"
              : "00000000-0000-7000-8000-000000000001",
        }),
      ).toBeVisible();
      await page
        .getByRole("navigation", { name: "Available tasks", exact: true })
        .getByRole("link", { name: task, exact: true })
        .click();
      await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
      if (!alias.includes("admin")) {
        await page.goto(catalog.origin + "/members");
        await expect(page.getByRole("alert")).toContainText("access.manage");
      }
      await signOut(page);
      await expect(
        page.getByRole("heading", { name: "Sign in", exact: true }),
      ).toBeVisible();
      await expect(page.locator(".login-users [data-login-alias]")).toHaveCount(
        20,
      );
    }
  },
);

test("the fourteen configured exact templates authenticate and reach permission-based tasks without broad companion grants", async ({
  page,
  catalog,
}) => {
  test.setTimeout(120_000);
  for (const [key, task, denied] of [
    ["reader", "Explore local catalog", "Discovery publication"],
    ["contributor", "Authoring", "Publishers"],
    ["reviewer", "Reviews", "Discovery publication"],
    ["publisher", "Reviews", "Discovery publication"],
    ["tenant-admin", "Roles", ""],
    ["discovery-reader", "Catalog", "Explore local catalog"],
    ["publisher-creator", "Publishers", "Authoring"],
    ["publication-admin", "Discovery publication", "Authoring"],
    ["source-admin", "Federation sources", "Authoring"],
    ["security-reviewer", "Audit", "Catalog"],
    ["webhook-admin", "Webhooks", "Catalog"],
    ["feedback-member", "My feedback", "Catalog"],
    ["library-member", "Saved items and searches", "Catalog"],
    ["service-client", "API reference", "My feedback"],
  ] as const) {
    const alias = `local-template-${key}`;
    await login(page, catalog, alias, "/access");
    await expect(
      page.getByRole("heading", { name: "My access", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("definition").filter({ hasText: alias }),
    ).toBeVisible();
    await expect(
      page.getByRole("definition").filter({
        hasText: key === "service-client" ? "Service (nonhuman)" : "Human",
      }),
    ).toBeVisible();
    const tasks = page.getByRole("navigation", {
      name: "Available tasks",
      exact: true,
    });
    if (key === "reader") {
      await expect(
        tasks.getByRole("link", { name: "Authoring Read-only", exact: true }),
      ).toBeVisible();
      await expect(
        tasks.getByRole("link", { name: "Reviews Read-only", exact: true }),
      ).toBeVisible();
      await page
        .locator("details.policy-summary")
        .first()
        .locator(":scope > summary")
        .click();
      await page
        .getByText("Role template and permission keys", { exact: true })
        .click();
      await expect(
        page.getByRole("region", { name: "Built-in template help" }),
      ).toContainText("Eligible humans");
      await page.evaluate(() => {
        if (document.activeElement instanceof HTMLElement)
          document.activeElement.blur();
        window.scrollTo(0, 0);
      });
      await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(0);
      await page.screenshot({
        path: test.info().outputPath("my-access-reader.png"),
        fullPage: true,
      });
    }
    await expect(
      tasks.getByRole("link", { name: task, exact: true }),
    ).toBeVisible();
    if (denied)
      await expect(
        tasks.getByRole("link", { name: denied, exact: true }),
      ).toHaveCount(0);
    await tasks.getByRole("link", { name: task, exact: true }).click();
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    await expect(page.getByTitle("Account", { exact: true })).toBeVisible();
    if (key === "discovery-reader")
      await expect(
        page.getByText(
          /Your current permissions do not include local resources/,
        ),
      ).toBeVisible();
    const workspace = page.getByRole("navigation", {
      name: "Workspace",
      exact: true,
    });
    for (const name of [
      "My access",
      "My work",
      "Saved items and searches",
      "My ratings",
      "My feedback",
    ])
      await expect(
        workspace.getByRole("link", { name, exact: true }),
      ).toHaveCount(0);
    await expect(
      workspace.getByRole("link", { name: "Service information", exact: true }),
    ).toBeVisible();
    await expect(
      workspace.getByRole("link", { name: "Jobs", exact: true }),
    ).toHaveCount(
      ["tenant-admin", "publication-admin", "source-admin"].includes(key)
        ? 1
        : 0,
    );
    if (
      [
        "reader",
        "contributor",
        "reviewer",
        "publisher",
        "discovery-reader",
      ].includes(key)
    )
      await expect(workspace.getByRole("link").first()).toHaveText("Catalog");
    await openAccount(page);
    const account = page.getByRole("navigation", {
      name: "Account pages",
      exact: true,
    });
    await expect(
      account.getByRole("link", { name: "My access", exact: true }),
    ).toBeVisible();
    await expect(
      account.getByRole("link", { name: "My work", exact: true }),
    ).toHaveCount(key === "service-client" ? 0 : 1);
    await expect(
      account.getByRole("link", {
        name: "Saved items and searches",
        exact: true,
      }),
    ).toHaveCount(
      ["tenant-admin", "discovery-reader", "library-member"].includes(key)
        ? 1
        : 0,
    );
    await expect(
      account.getByRole("link", { name: "My ratings", exact: true }),
    ).toHaveCount(key === "service-client" ? 0 : 1);
    if (key === "feedback-member")
      await expect(
        account.getByRole("link", { name: "My feedback", exact: true }),
      ).toBeVisible();
    if (key === "service-client")
      await expect(
        account.getByRole("link", { name: "My feedback", exact: true }),
      ).toHaveCount(0);
    await page.keyboard.press("Escape");
    if (key !== "tenant-admin") {
      await page.goto(catalog.origin + "/members");
      await expect(page.getByRole("alert")).toContainText("access.manage");
    }
    await signOut(page);
    await expect(
      page.getByRole("heading", { name: "Sign in", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("navigation", { name: "Workspace", exact: true }),
    ).toHaveCount(0);
  }
});

maintainedTest(
  "development sign-in and compact permissions wrap, use keyboard controls and preserve explicit companion choices",
  async ({ page, catalog }) => {
    await page.goto(catalog.origin + "/catalog");
    await expect(
      page.locator('[data-login-alias^="local-template-"]'),
    ).toHaveCount(14);
    await expect(
      page
        .getByRole("region", { name: "Development bundles" })
        .getByRole("button"),
    ).toHaveCount(6);
    await expect(
      page.getByRole("region", { name: "Exact templates" }).getByRole("button"),
    ).toHaveCount(14);
    await expect(page.getByRole("status")).toHaveText(
      "Showing 20 of 20 identities.",
    );
    const identitySearch = page.getByRole("searchbox", {
      name: "Find a development identity",
      exact: true,
    });
    const choiceHelp = page.getByText("About bundles and exact templates", {
      exact: true,
    });
    await choiceHelp.click();
    await expect(
      page.getByText(/Retained grants can differ from this setup/),
    ).toBeVisible();
    await expect(page.getByText(/It is not published-only/)).toBeVisible();
    await choiceHelp.click();
    await identitySearch.fill("local-template-service-client");
    await expect(page.getByRole("status")).toHaveText(
      "Showing 1 of 20 identities.",
    );
    await expect(page.locator(".login-users [data-login-alias]")).toHaveCount(
      1,
    );
    await expect(page.locator(".login-users")).toContainText(
      "Nonhuman identity with an empty template",
    );
    await page
      .getByRole("button", { name: "Clear identity search", exact: true })
      .click();
    await identitySearch.focus();
    for (const alias of await page
      .locator(".login-users [data-login-alias]")
      .evaluateAll((choices) =>
        choices.map((choice) => choice.getAttribute("data-login-alias")),
      )) {
      await page.keyboard.press("Tab");
      await expect(page.locator(`[data-login-alias="${alias}"]`)).toBeFocused();
      await expect(
        page.locator(`[data-login-alias="${alias}"]`),
      ).toBeInViewport();
    }
    await page.evaluate(() => {
      const choices = document.querySelector(".login-users");
      if (choices) choices.scrollTop = 0;
      window.scrollTo(0, 0);
    });
    await expect(
      page.locator('[data-login-alias="other-tenant-admin"]'),
    ).toContainText(/separate tenant/i);
    const introduction = page.locator(".login > p:not(.eyebrow)");
    await expectNaturalText(introduction, page.locator(".login"), 1);
    await expect(page.locator(".login h1")).toHaveText(
      "Find Agents, MCP Servers, Skills and Plugins.",
    );
    await expect(page.locator(".login h1 br")).toHaveCount(0);
    await page.screenshot({
      path: test.info().outputPath("development-sign-in.png"),
      fullPage: true,
    });
    for (const theme of ["Light", "Dark"]) {
      await page.getByLabel("Appearance", { exact: true }).click();
      await page.getByRole("radio", { name: theme, exact: true }).check();
      await page.keyboard.press("Escape");
      await page.setViewportSize({ width: 320, height: 800 });
      await expectNaturalText(introduction, page.locator(".login"), "wrapped");
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= window.innerWidth,
        ),
      ).toBe(true);
      const choice = page.locator(
        '[data-login-alias="local-template-service-client"]',
      );
      await choice.focus();
      await expect(choice).toBeFocused();
    }
    await login(page, catalog, "local-admin", "/access-roles/new");
    await expect(
      page.getByRole("heading", { name: "Create role", exact: true }),
    ).toBeVisible();
    const check = page.getByRole("checkbox", {
      name: "resource.create",
      exact: true,
    });
    await check.focus();
    await page.keyboard.press("Space");
    await expect(check).toBeChecked();
    await expect(
      page.getByRole("checkbox", { name: "namespace.read", exact: true }),
    ).not.toBeChecked();
    await expect(page.getByText(/Required companions missing/)).toContainText(
      "namespace.read",
    );
    await expect(
      page.getByRole("button", { name: "Create role", exact: true }),
    ).toBeDisabled();
    const descriptors = await catalog.admin<{ key: string }[]>(
      "GET",
      "/api/v1/access/permissions",
    );
    const permissionBoxes = page.locator(".permission-choice input");
    await expect(permissionBoxes).toHaveCount(descriptors.data.length);
    const writes: string[] = [];
    page.on("request", (request) => {
      if (
        request.method() !== "GET" &&
        new URL(request.url()).pathname.includes("/_catalog/api/v1/access/")
      )
        writes.push(request.url());
    });
    await page
      .getByRole("textbox", { name: "Role name", exact: true })
      .fill("Unsent searchable role");
    await page
      .getByRole("textbox", { name: "Metadata change reason", exact: true })
      .fill("Inspect permission selection without a write");
    await page
      .getByRole("checkbox", { name: "namespace.read", exact: true })
      .check();
    const search = page.getByRole("searchbox", {
      name: "Find permissions",
      exact: true,
    });
    const selectedOnly = page.getByRole("checkbox", {
      name: "Show selected permissions only",
      exact: true,
    });
    await search.fill("namespace.read");
    await selectedOnly.check();
    await expect(permissionBoxes).toHaveCount(1);
    await expect(
      page.getByRole("checkbox", { name: "namespace.read", exact: true }),
    ).toBeChecked();
    await expect(page.getByText(/2 permissions selected/)).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Create role", exact: true }),
    ).toBeEnabled();
    await search.press("Enter");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await search.fill("rating summary sharing");
    await expect(
      page.getByText(/No permissions match these filters/),
    ).toBeVisible();
    await selectedOnly.uncheck();
    await expect(
      page.getByText("Manage rating summary sharing", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("checkbox", {
        name: "ratings.sharing.manage",
        exact: true,
      }),
    ).not.toBeChecked();
    await search.fill("");
    await expect(permissionBoxes).toHaveCount(descriptors.data.length);
    await expect(check).toBeChecked();
    expect(writes).toEqual([]);
    for (const [width, theme] of [
      [1440, "Light"],
      [320, "Dark"],
      [320, "Light"],
    ] as const) {
      await page.setViewportSize({ width, height: 900 });
      await page.getByLabel("Appearance", { exact: true }).click();
      await page.getByRole("radio", { name: theme, exact: true }).check();
      await page.keyboard.press("Escape");
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= window.innerWidth,
        ),
      ).toBe(true);
      for (const box of await page.getByRole("checkbox").all()) {
        const size = await box.boundingBox();
        expect(size?.width).toBe(18);
        expect(size?.height).toBe(18);
      }
    }
    await page.screenshot({
      path: test.info().outputPath("permission-editor-narrow.png"),
      fullPage: true,
    });
    // Clear only this unsent draft before inspecting a real built-in definition.
    await check.uncheck();
    await page
      .getByRole("checkbox", { name: "namespace.read", exact: true })
      .uncheck();
    await page
      .getByRole("textbox", { name: "Role name", exact: true })
      .fill("");
    await page
      .getByRole("textbox", { name: "Metadata change reason", exact: true })
      .fill("");
    const roles = await catalog.admin<{
      items: {
        id: string;
        key?: string;
        builtin: boolean;
        permissions: string[];
      }[];
    }>("GET", "/api/v1/access/roles?limit=100");
    const reader = required(
      roles.data.items.find((role) => role.builtin && role.key === "reader"),
    );
    await page.goto(
      `${catalog.origin}${catalog.basePath}/access-roles/${reader.id}`,
    );
    await expect(
      page.getByRole("heading", { name: "Role permissions", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("textbox", { name: "Role name", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("textbox", {
        name: "Metadata change reason",
        exact: true,
      }),
    ).toHaveCount(0);
    const selected = page.getByRole("checkbox", {
      name: "Show selected permissions only",
      exact: true,
    });
    await expect(selected).toBeChecked();
    // Role metadata and permission descriptors load separately. Wait for the
    // known Reader permissions before inspecting the copied selection.
    const selectedCount = reader.permissions.length + 1;
    await expect(page.getByRole("checkbox")).toHaveCount(selectedCount);
    await selected.uncheck();
    await expect(page.getByRole("checkbox")).toHaveCount(
      descriptors.data.length + 1,
    );
    const permissions = page.locator(".permission-choice input[type=checkbox]");
    expect(await permissions.count()).toBeGreaterThan(0);
    for (const box of await permissions.all()) await expect(box).toBeDisabled();
    await expect(
      page.getByRole("link", { name: "Back to roles", exact: true }),
    ).toHaveAttribute("href", `${catalog.basePath}/access-roles`);
    await page
      .getByRole("button", { name: "Copy to custom role", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: "Create role", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("textbox", { name: "Role name", exact: true }),
    ).toHaveValue("");
    await expect(
      page.getByRole("textbox", {
        name: "Metadata change reason",
        exact: true,
      }),
    ).toHaveValue("");
    await expect(page.locator(".permission-choice input:checked")).toHaveCount(
      selectedCount - 1,
    );
    const createCopy = page.getByRole("button", {
      name: "Create role",
      exact: true,
    });
    await expect(createCopy).toBeEnabled();
    await createCopy.click();
    await expect(
      page.getByRole("textbox", { name: "Role name", exact: true }),
    ).toBeFocused();
    expect(
      await page
        .getByRole("textbox", { name: "Role name", exact: true })
        .evaluate((input) => (input as HTMLInputElement).validity.valueMissing),
    ).toBe(true);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(writes).toEqual([]);
    page.once("dialog", (dialog) => dialog.accept());
    await page
      .getByRole("button", { name: "Cancel role copy", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: "Role permissions", exact: true }),
    ).toBeVisible();
    expect(writes).toEqual([]);
  },
);
