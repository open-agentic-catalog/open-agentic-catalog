// Shared real-browser qualification; setup and commands target owned fixtures only.
import { expect } from "@playwright/test";

/** Open only the named native disclosure and its containing disclosures through real summary clicks. */
async function openDisclosure(root, title) {
  const summary = root.locator("summary").filter({
    hasText: new RegExp(
      `^\\s*${title.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*$`,
    ),
  });
  await expect(summary).toHaveCount(1);
  const ancestors = summary.locator("xpath=ancestor::details");
  for (let index = 0; index < (await ancestors.count()); index++) {
    const details = ancestors.nth(index);
    if ((await details.getAttribute("open")) === null)
      await details.locator(":scope > summary").click();
  }
  await expect(summary.locator("..")).toHaveJSProperty("open", true);
}

/** Qualify membership-only grants and real filtered directory continuation. */
export async function authorizationQualificationJourney({
  page,
  origin,
  basePath = "",
}) {
  const at = (path) => origin + basePath + path;
  const suffix = basePath ? "subpath" : "root";
  const read = async (path) => {
    const value = await page.evaluate(
      async (url) => {
        const response = await fetch(url, {
          headers: { "X-Catalog-Request": "1" },
        });
        return { status: response.status, data: await response.json() };
      },
      at("/_catalog" + path),
    );
    expect(value.status).toBe(200);
    return value.data;
  };
  const panel = (title) =>
    page.locator("section.workspace-panel").filter({
      has: page.getByRole("heading", { name: title, exact: true }),
    });
  const confirm = async (title, route) => {
    await panel(title)
      .getByRole("textbox", { name: "Reason", exact: true })
      .fill("Qualify explicit membership and specialist access");
    await panel(title)
      .getByRole("button", { name: title, exact: true })
      .click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText(
      "Qualify explicit membership and specialist access",
    );
    const written = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        new URL(response.url()).pathname === basePath + "/_catalog" + route,
    );
    await dialog.getByRole("button", { name: "Confirm", exact: true }).click();
    const response = await written;
    expect(response.status()).toBe(201);
    await expect(dialog).not.toBeVisible();
    return response.json();
  };
  const administrator = await read("/api/v1/me");
  const library = await read(
    "/api/v1/access/roles?key=library_member&state=active&limit=1",
  );
  expect(library.items).toHaveLength(1);

  for (const actorType of ["human", "service"]) {
    const subject = `qualified-${actorType}-${suffix}`;
    await page.goto(at("/members"));
    await openDisclosure(page, "Add Catalog member");
    await panel("Add Catalog member")
      .getByRole("textbox", { name: "Subject", exact: true })
      .fill(subject);
    await panel("Add Catalog member")
      .getByRole("combobox", { name: "Identity type", exact: true })
      .selectOption(actorType);
    await panel("Add Catalog member")
      .getByRole("combobox", { name: "Catalog role", exact: true })
      .selectOption("service_client");
    await panel("Add Catalog member")
      .getByRole("combobox", { name: "Assignment scope", exact: true })
      .selectOption("tenant");
    const membership = await confirm(
      "Add Catalog member",
      "/api/v1/role-assignments",
    );
    const member = await read(`/api/v1/memberships/${membership.membershipId}`);
    expect(member).toMatchObject({
      subject,
      actorType,
      state: "active",
      realmId: administrator.realmId,
    });
    const before = await read(
      `/api/v1/memberships/${member.id}/access?limit=100`,
    );
    expect(before.nextCursor).toBeUndefined();
    expect(before.items).toHaveLength(1);
    expect(before.items[0].role).toMatchObject({
      key: "service_client",
      permissions: [],
    });
    await page.goto(at(`/access-assignments?membershipId=${member.id}`));
    await openDisclosure(page, "Grant access");
    await panel("Grant access assignment")
      .getByLabel("Role definition ID", { exact: true })
      .fill(library.items[0].id);
    await panel("Grant access assignment")
      .getByRole("combobox", { name: "Assignment scope", exact: true })
      .selectOption("tenant");
    const grant = await confirm(
      "Grant access assignment",
      "/api/v1/access/assignments",
    );
    expect(grant).toMatchObject({
      membershipId: member.id,
      roleDefinitionId: library.items[0].id,
      state: "active",
    });
    const after = await read(
      `/api/v1/memberships/${member.id}/access?limit=100`,
    );
    expect(after.nextCursor).toBeUndefined();
    expect(after.items.map((item) => item.role.key).sort()).toEqual([
      "library_member",
      "service_client",
    ]);
    expect(
      after.items.every(
        (item) =>
          item.effective && !item.role.permissions.includes("resource.read"),
      ),
    ).toBe(true);
    await page.goto(at(`/members/${member.id}`));
    await expect(
      page.getByRole("region", {
        name: "Member access contributions",
        exact: true,
      }),
    ).toContainText(grant.id);
    await expect(panel("Direct role assignments")).toContainText(grant.id);
    const legacy = await read(
      `/api/v1/role-assignments?membershipId=${member.id}&limit=100`,
    );
    expect(legacy.nextCursor).toBeUndefined();
    expect(legacy.items.some((item) => item.id === grant.id)).toBe(false);
  }

  // These bounded API writes prepare data for paging, not workflow assertions.
  // Creation/grant actions above are separately executed through real UI commands.
  const roleName = `Qualification roles ${suffix}`;
  const groupName = `Qualification groups ${suffix}`;
  const seeded = await page.evaluate(
    async ({ prefix, roleName, groupName, suffix }) => {
      const ids = { roles: [], groups: [] };
      for (let i = 0; i < 26; i++) {
        for (const [kind, path, body] of [
          [
            "roles",
            "/api/v1/access/roles",
            {
              name: roleName,
              permissions: ["resource.read"],
              reason: "Owned filtered paging fixture",
            },
          ],
          [
            "groups",
            "/api/v1/access/groups",
            { name: groupName, reason: "Owned filtered paging fixture" },
          ],
          [
            "members",
            "/api/v1/role-assignments",
            {
              subject: `directory-${suffix}-${i}`,
              actorType: "service",
              role: "service_client",
              reason: "Owned filtered paging fixture",
            },
          ],
        ]) {
          const response = await fetch(prefix + path, {
            method: "POST",
            headers: {
              "Content-Type": "application/json",
              "X-Catalog-Request": "1",
              "Idempotency-Key": crypto.randomUUID(),
            },
            body: JSON.stringify(body),
          });
          if (response.status !== 201)
            throw new Error("Owned directory preparation refused");
          const value = await response.json();
          if (kind !== "members") ids[kind].push(value.id);
        }
      }
      return ids;
    },
    { prefix: basePath + "/_catalog", roleName, groupName, suffix },
  );

  for (const [route, label, expected] of [
    ["access-roles", roleName, seeded.roles],
    ["access-groups", groupName, seeded.groups],
    ["members", "", undefined],
  ]) {
    await page.goto(at("/" + route));
    let expectedIDs = expected;
    if (route === "members") {
      await page
        .getByRole("combobox", { name: "Identity type filter", exact: true })
        .selectOption("service");
      await page
        .getByLabel("Exact realm ID", { exact: true })
        .fill(administrator.realmId);
      const complete = await read(
        `/api/v1/memberships?limit=100&state=active&actorType=service&realmId=${administrator.realmId}`,
      );
      expect(complete.nextCursor).toBeUndefined();
      expectedIDs = complete.items.map((item) => item.id);
    } else {
      await page.getByLabel("Exact name", { exact: true }).fill(label);
    }
    await page
      .getByRole("combobox", { name: "State", exact: true })
      .selectOption("active");
    const endpoint =
      basePath +
      "/_catalog/api/v1/" +
      (route === "members"
        ? "memberships"
        : route.replace("access-", "access/"));
    const filtered = page.waitForResponse((response) => {
      const url = new URL(response.url());
      return (
        response.request().method() === "GET" &&
        url.pathname === endpoint &&
        url.searchParams.get("state") === "active" &&
        !url.searchParams.has("cursor") &&
        (label
          ? url.searchParams.get("name") === label
          : url.searchParams.get("actorType") === "service")
      );
    });
    await page
      .getByRole("button", { name: "Apply filters", exact: true })
      .click();
    expect((await filtered).status()).toBe(200);
    const links =
      route === "members"
        ? page.locator(".workspace-table tbody tr td:first-child a")
        : page.locator(`a[href^="${basePath}/${route}/"]:not([href$="/new"])`);
    const observed = [];
    for (let i = 0; i < 4; i++) {
      const count = Math.min(25, expectedIDs.length - observed.length);
      await expect(links).toHaveCount(count);
      observed.push(
        ...(await links.evaluateAll((items) =>
          items.map((item) => item.getAttribute("href").split("/").at(-1)),
        )),
      );
      const next = page.getByRole("button", { name: "Next page", exact: true });
      if (observed.length === expectedIDs.length) {
        await expect(next).toHaveCount(0);
        break;
      }
      const previousFirst = await links.first().getAttribute("href");
      const continuation = page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname === endpoint &&
          new URL(response.url()).searchParams.has("cursor"),
      );
      await next.click();
      expect((await continuation).status()).toBe(200);
      await expect(links.first()).not.toHaveAttribute("href", previousFirst);
    }
    expect(new Set(observed).size).toBe(observed.length);
    expect(observed.sort()).toEqual(expectedIDs.sort());
    await page.getByRole("button", { name: "First page", exact: true }).click();
    await expect(links).toHaveCount(25);
    if (label)
      await expect(page.getByLabel("Exact name", { exact: true })).toHaveValue(
        label,
      );
    await expect(
      page.getByRole("combobox", { name: "State", exact: true }),
    ).toHaveValue("active");
  }
}
