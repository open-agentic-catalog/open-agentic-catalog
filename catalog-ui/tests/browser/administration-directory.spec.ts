import { openDisclosure, revealControl } from "./editor-disclosure";
import { test, expect, login } from "./fixtures";
import AxeBuilder from "@axe-core/playwright";
import { required } from "../required";

for (const kind of ["namespace", "publisher"] as const) {
  test(`exact ${kind} lookup finds off-page duplicates and intersects criteria without writes`, async ({
    page,
    catalog,
  }) => {
    const path = `/api/v1/${kind}s`;
    const exactName = "Zebra directory 日本語";
    const targets: string[] = [];
    for (let index = 0; index < 27; index++) {
      const reply = await catalog.admin<{ id: string }>("POST", path, {
        displayName: index < 25 ? `Directory ${index}` : exactName,
        ...(kind === "namespace"
          ? { slug: `directory-${String(index).padStart(2, "0")}` }
          : { domains: [`directory-${index}.example`] }),
      });
      if (index >= 25) targets.push(reply.data.id);
    }
    await login(page, catalog, "local-admin", `/${kind}s`);
    await expect(page.locator("tbody tr")).toHaveCount(25);
    await expect(
      page.locator("tbody").getByRole("link", { name: exactName }),
    ).toHaveCount(0);
    const reads: string[] = [],
      writes: string[] = [];
    page.on("request", (request) => {
      if (new URL(request.url()).pathname === `/_catalog${path}`) {
        if (request.method() === "GET") reads.push(request.url());
        else writes.push(request.url());
      }
    });
    expect(reads).toEqual([]);
    const pager = page.getByRole("group", {
      name: `${kind === "namespace" ? "Namespace" : "Publisher"} inventory pagination`,
      exact: true,
    });
    await pager.getByRole("button", { name: "Next page", exact: true }).click();
    await expect(
      page.locator("tbody").getByRole("link", { name: exactName }),
    ).toHaveCount(2);
    expect(
      new URL(required(reads.at(-1))).searchParams.get("cursor"),
    ).toBeTruthy();
    const beforeTyping = [...reads];
    await page
      .getByRole("textbox", { name: "Exact display name", exact: true })
      .fill(exactName);
    expect(reads).toEqual(beforeTyping);
    await page
      .getByRole("button", { name: "Apply filters", exact: true })
      .focus();
    await page.keyboard.press("Enter");
    await expect(page.locator("tbody tr")).toHaveCount(2);
    const firstFiltered = new URL(required(reads.at(-1))).searchParams;
    expect(firstFiltered.get("displayName")).toBe(exactName);
    expect(firstFiltered.has("cursor")).toBe(false);
    const hrefs = await page
      .locator("tbody a")
      .evaluateAll((links) =>
        links.map((link) =>
          new URL((link as HTMLAnchorElement).href).pathname.split("/").pop()!,
        ),
      );
    expect(hrefs.sort()).toEqual(targets.sort());
    const keyLabel =
      kind === "namespace" ? "Exact slug" : "Exact declared domain";
    await page
      .getByRole("textbox", { name: keyLabel, exact: true })
      .fill(
        kind === "namespace" ? " DIRECTORY-26 " : " DIRECTORY-26.Example. ",
      );
    await page
      .getByRole("button", { name: "Apply filters", exact: true })
      .click();
    await expect(page.locator("tbody tr")).toHaveCount(1);
    const query = new URL(required(reads.at(-1))).searchParams;
    expect(query.get("displayName")).toBe(exactName);
    expect(
      query.get(kind === "namespace" ? "slug" : "declaredDomain"),
    ).toBeTruthy();
    expect(query.has("cursor")).toBe(false);
    await page
      .getByRole("textbox", { name: "Exact display name", exact: true })
      .fill(exactName.toLowerCase());
    await page
      .getByRole("button", { name: "Apply filters", exact: true })
      .click();
    await expect(page.locator("tbody tr")).toHaveCount(0);
    await expect(
      page.getByText(`No ${kind}s match the applied filters.`, { exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Clear filters", exact: true })
      .click();
    await expect(page.locator("tbody tr")).toHaveCount(25);
    expect(new URL(required(reads.at(-1))).searchParams.toString()).toBe(
      "limit=25",
    );
    await page.getByLabel("Appearance", { exact: true }).click();
    await page.getByRole("radio", { name: "Dark", exact: true }).check();
    await page.getByRole("radio", { name: "Compact", exact: true }).check();
    await page.keyboard.press("Escape");
    await page.setViewportSize({ width: 320, height: 780 });
    await expect(
      page.getByRole("form", {
        name: `${kind === "namespace" ? "Namespace" : "Publisher"} filters`,
      }),
    ).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
      violations: [],
      incomplete: [],
    });
    expect(writes).toEqual([]);
  });
}

test("populated assignment and choice pagers keep independent cursors, selection, draft and keyboard focus", async ({
  page,
  catalog,
  network,
}) => {
  const roles: string[] = [];
  for (let index = 0; index < 26; index++) {
    const membership = await catalog.admin<{ membershipId: string }>(
      "POST",
      "/api/v1/role-assignments",
      {
        subject: `pager-member-${String(index).padStart(2, "0")}`,
        actorType: "human",
        role: "service_client",
        reason: "Owned picker population",
      },
    );
    const role = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/access/roles",
      {
        name: `Pager role ${index}`,
        permissions: ["namespace.read"],
        reason: "Owned picker population",
      },
    );
    roles.push(role.data.id);
    await catalog.admin("POST", "/api/v1/access/assignments", {
      roleDefinitionId: role.data.id,
      membershipId: membership.data.membershipId,
      reason: "Owned assignment population",
    });
    await catalog.admin("POST", "/api/v1/namespaces", {
      slug: `pager-${index}`,
      displayName: `Pager ${index}`,
    });
  }
  await login(page, catalog, "local-admin", "/access-assignments");
  await openDisclosure(page, "Grant access");
  const inventory = page.getByRole("group", {
    name: "Assignment inventory pagination",
    exact: true,
  });
  const roleChoices = page.getByRole("group", {
    name: "Role choices pagination",
    exact: true,
  });
  const memberChoices = page.getByRole("group", {
    name: "Member choices pagination",
    exact: true,
  });
  await expect(
    inventory.getByRole("button", { name: "Next page", exact: true }),
  ).toBeEnabled();
  const roleSelect = page.getByRole("combobox", { name: "Role", exact: true });
  await expect
    .poll(async () => {
      const values = await roleSelect
        .locator("option")
        .evaluateAll((items) =>
          items.map((item) => (item as HTMLOptionElement).value),
        );
      return roles.some((id) => values.includes(id));
    })
    .toBe(true);
  const options = await roleSelect
    .locator("option")
    .evaluateAll((items) =>
      items.map((item) => (item as HTMLOptionElement).value),
    );
  const chosen = required(roles.find((id) => options.includes(id)));
  await roleSelect.selectOption(chosen);
  await page
    .getByRole("combobox", { name: "Assignment scope", exact: true })
    .selectOption("namespace");
  const namespaces = page.getByRole("group", {
    name: "Namespace choices pagination",
    exact: true,
  });
  const reason = page.getByRole("textbox", { name: "Reason", exact: true });
  await reason.fill("Keep this private unsent grant");
  const writes: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().includes("/_catalog/"))
      writes.push(request.url());
  });
  await roleChoices
    .getByRole("button", { name: "Next page", exact: true })
    .focus();
  await page.keyboard.press("Enter");
  await expect(roleChoices).toBeFocused();
  await expect(
    roleChoices.getByRole("button", { name: "First page", exact: true }),
  ).toBeEnabled();
  await expect(roleSelect).toHaveValue(chosen);
  await expect(
    memberChoices.getByRole("button", { name: "First page", exact: true }),
  ).toHaveCount(0);
  await memberChoices
    .getByRole("button", { name: "Next page", exact: true })
    .focus();
  await page.keyboard.press("Enter");
  await expect(memberChoices).toBeFocused();
  await namespaces
    .getByRole("button", { name: "Next page", exact: true })
    .focus();
  await page.keyboard.press("Enter");
  await expect(namespaces).toBeFocused();
  // Corrupt one real continuation; successful other directories and the
  // private grant must remain untouched when that picker restarts.
  await memberChoices
    .getByRole("button", { name: "First page", exact: true })
    .click();
  network.expectFailure("GET", "/_catalog/api/v1/memberships", 400);
  await page.route("**/_catalog/api/v1/memberships?*", async (route) => {
    const url = new URL(route.request().url());
    if (url.searchParams.has("cursor")) {
      url.searchParams.set("cursor", "invalid");
      await route.continue({ url: url.href });
      await page.unroute("**/_catalog/api/v1/memberships?*");
    } else await route.continue();
  });
  await memberChoices
    .getByRole("button", { name: "Next page", exact: true })
    .click();
  await expect(page.getByRole("alert")).toHaveCount(1);
  await memberChoices
    .getByRole("button", { name: "First page", exact: true })
    .focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(memberChoices).toBeFocused();
  await expect(
    roleChoices.getByRole("button", { name: "First page", exact: true }),
  ).toBeEnabled();
  await expect(
    namespaces.getByRole("button", { name: "First page", exact: true }),
  ).toBeEnabled();
  await expect(
    inventory.getByRole("button", { name: "First page", exact: true }),
  ).toHaveCount(0);
  await expect(roleSelect).toHaveValue(chosen);
  await expect(reason).toHaveValue("Keep this private unsent grant");
  expect(writes).toEqual([]);
  await reason.fill("");
});

test("group member and Add-member choice pagers preserve an unsent addition independently", async ({
  page,
  catalog,
}) => {
  const group = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/access/groups",
    { name: "Owned pager group", reason: "Independent pager controls" },
  );
  for (let index = 0; index < 26; index++) {
    const member = await catalog.admin<{ membershipId: string }>(
      "POST",
      "/api/v1/role-assignments",
      {
        subject: `group-pager-${index}`,
        actorType: "human",
        role: "service_client",
        reason: "Owned group picker population",
      },
    );
    await catalog.admin(
      "POST",
      `/api/v1/access/groups/${group.data.id}/members`,
      {
        membershipId: member.data.membershipId,
        reason: "Owned group population",
      },
    );
  }
  await login(page, catalog, "local-admin", `/access-groups/${group.data.id}`);
  await openDisclosure(page, "Add group member");
  const existing = page.getByRole("group", {
    name: "Group members pagination",
    exact: true,
  });
  const choices = page.getByRole("group", {
    name: "Add-member choices pagination",
    exact: true,
  });
  await expect(
    existing.getByRole("button", { name: "Next page", exact: true }),
  ).toBeEnabled();
  await expect(
    choices.getByRole("button", { name: "Next page", exact: true }),
  ).toBeEnabled();
  const form = page
    .getByRole("heading", { name: "Add group member", exact: true })
    .locator("..");
  const member = form.getByRole("combobox", { name: "Member", exact: true });
  const selected = required(
    (await member.locator("option").nth(1).getAttribute("value")) ?? undefined,
  );
  await member.selectOption(selected);
  const reason = form.getByRole("textbox", { name: "Reason", exact: true });
  await reason.fill("Private addition remains unsent");
  const writes: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname.startsWith(
        "/_catalog/api/v1/access/groups/",
      )
    )
      writes.push(request.url());
  });
  await choices.getByRole("button", { name: "Next page", exact: true }).focus();
  await page.keyboard.press("Enter");
  await expect(choices).toBeFocused();
  await expect(
    choices.getByRole("button", { name: "First page", exact: true }),
  ).toBeEnabled();
  await expect(member).toHaveValue(selected);
  await expect(
    existing.getByRole("button", { name: "First page", exact: true }),
  ).toHaveCount(0);
  page.once("dialog", (dialog) => dialog.accept());
  await existing
    .getByRole("button", { name: "Next page", exact: true })
    .focus();
  await page.keyboard.press("Enter");
  await expect(existing).toBeFocused();
  await expect(
    existing.getByRole("button", { name: "First page", exact: true }),
  ).toBeEnabled();
  await expect(
    choices.getByRole("button", { name: "First page", exact: true }),
  ).toBeEnabled();
  await expect(reason).toHaveValue("Private addition remains unsent");
  await expect(member).toHaveValue(selected);
  expect(writes).toEqual([]);
});

test("feedback Resource choices and case pages retain independent focus and a dirty response", async ({
  page,
  catalog,
  network,
}) => {
  for (let index = 0; index < 21; index++) {
    const resource = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/resources",
      {
        namespaceId: catalog.receipt.namespaceId,
        canonicalIdentifier: `urn:air:catalog.example.com:demo:feedback-pager-${index}`,
        kind: "agent",
      },
    );
    await catalog.admin("POST", "/api/v1/feedback", {
      resourceId: resource.data.id,
      category: "documentation",
      comment: `Owned feedback pager ${index}`,
    });
  }
  await login(page, catalog, "local-admin", "/feedback");
  const resources = page.getByRole("group", {
    name: "Feedback Resource choices pagination",
    exact: true,
  });
  const cases = page.getByRole("group", {
    name: "Feedback cases pagination",
    exact: true,
  });
  await expect(
    cases.getByRole("button", { name: "Next page", exact: true }),
  ).toBeEnabled();
  await expect(
    resources.getByRole("button", { name: "Next page", exact: true }),
  ).toBeEnabled();
  await cases.getByRole("button", { name: "Next page", exact: true }).focus();
  await page.keyboard.press("Enter");
  await expect(cases).toBeFocused();
  await expect(
    cases.getByRole("button", { name: "Next page", exact: true }),
  ).toHaveCount(0);
  await expect(
    resources.getByRole("button", { name: "First page", exact: true }),
  ).toHaveCount(0);
  await cases.getByRole("button", { name: "First page", exact: true }).click();
  await expect(
    page.getByText("Respond or manage feedback", { exact: true }),
  ).toHaveCount(20);
  await revealControl(
    page.getByText("Respond or manage feedback", { exact: true }).first(),
  );
  await page
    .getByText("Respond or manage feedback", { exact: true })
    .first()
    .click();
  const response = page
    .getByRole("textbox", {
      name: "Moderator response (optional)",
      exact: true,
    })
    .first();
  await response.fill("Keep this unsent private response");
  const writes: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname.startsWith("/_catalog/api/v1/feedback")
    )
      writes.push(request.url());
  });
  network.expectFailure("GET", "/_catalog/api/v1/resources", 400);
  await page.route("**/_catalog/api/v1/resources?*", async (route) => {
    const url = new URL(route.request().url());
    if (url.searchParams.has("cursor")) {
      url.searchParams.set("cursor", "invalid");
      await route.continue({ url: url.href });
      await page.unroute("**/_catalog/api/v1/resources?*");
    } else await route.continue();
  });
  await resources
    .getByRole("button", { name: "Next page", exact: true })
    .focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("alert")).toHaveCount(1);
  await expect(resources).toBeFocused();
  await resources
    .getByRole("button", { name: "First page", exact: true })
    .focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(resources).toBeFocused();
  await expect(
    cases.getByRole("button", { name: "First page", exact: true }),
  ).toHaveCount(0);
  await expect(response).toHaveValue("Keep this unsent private response");
  await expect(
    page.getByText("Respond or manage feedback", { exact: true }),
  ).toHaveCount(20);
  expect(writes).toEqual([]);
});
