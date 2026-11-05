// One maintained browser journey, reused against memory and packaged PostgreSQL.
// Both pages have independent cookies. Policy commands use UI confirmation;
// the final mixed-scope case prepares only its owned namespace/resource by API.
import { expect } from "@playwright/test";
import { authorizationCommandOutcome } from "./authorization-evidence.mjs";

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

export async function authorizationJourney({
  page,
  memberPage,
  origin,
  basePath = "",
  memberAlias,
  expectedCatalogCount = 3,
  expectFailure,
  qualifyRetainedPolicy,
  inspectCommandOutcome,
  captureDocumentation,
}) {
  const at = (path) => origin + basePath + path;
  // Use the browser's already qualified same-origin TLS/session boundary.
  // Playwright's separate request client does not share Chromium's pinned CA.
  const read = (target, path) =>
    target.evaluate(
      async (url) => {
        const response = await fetch(url, {
          headers: { "X-Catalog-Request": "1" },
        });
        return { status: response.status, data: await response.json() };
      },
      at("/_catalog" + path),
    );
  const request = async (path) => {
    const response = await read(page, path);
    expect(response.status).toBe(200);
    return response.data;
  };
  const panel = (title) =>
    page
      .locator("section.workspace-panel")
      .filter({ has: page.getByRole("heading", { name: title, exact: true }) });
  const confirm = async (title, root = panel(title), captureName) => {
    await root
      .getByLabel(/^(Reason|Metadata change reason|Lifecycle change reason)$/)
      .fill("Independent session qualification");
    await root.getByRole("button", { name: title, exact: true }).click();
    const summary = page
      .getByRole("dialog")
      .getByLabel("Command summary", { exact: true });
    await expect(summary).toContainText("Independent session qualification");
    if (captureName && captureDocumentation)
      await captureDocumentation(captureName, page.getByRole("dialog"));
    if (title === "Update role") {
      await expect(
        summary
          .locator("dt")
          .filter({ hasText: /^Removed permissions$/ })
          .locator("+ dd"),
      ).toHaveText("resource.create");
      await expect(
        summary
          .locator("dt")
          .filter({ hasText: /^Added permissions$/ })
          .locator("+ dd"),
      ).toHaveText("None");
      await expect(
        summary
          .locator("dt")
          .filter({ hasText: /^Scope eligibility$/ })
          .locator("+ dd"),
      ).toHaveText("Namespace or tenant → Namespace or tenant");
    }
    const capturedFacts = await summary.innerText();
    const written = page.waitForResponse(
      (r) =>
        r.request().method() !== "GET" &&
        new URL(r.url()).pathname.includes("/_catalog/api/v1/access/"),
    );
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    const response = await written;
    if (response.status() >= 400) {
      const path = new URL(response.url()).pathname.replace(
        /^.*\/_catalog(?=\/api\/v1\/)/,
        "",
      );
      console.log(
        "Owned authorization refusal: " +
          JSON.stringify(
            authorizationCommandOutcome({
              method: response.request().method(),
              path,
              status: response.status(),
              problem: await response.json().catch(() => undefined),
            }),
          ),
      );
    }
    const body = response.request().postDataJSON();
    expect(capturedFacts).toContain(body.reason);
    for (const field of [
      "roleDefinitionId",
      "membershipId",
      "groupId",
      "namespaceId",
    ])
      if (body[field]) expect(capturedFacts).toContain(body[field]);
    if (inspectCommandOutcome) {
      if (response.status() < 400)
        await inspectCommandOutcome({ title, response });
      else {
        try {
          await inspectCommandOutcome({ title, response });
        } catch {
          // Diagnostic failure must not replace the original command assertion.
          console.log("Owned authorization refusal diagnostics unavailable");
        }
      }
    }
    expect(response.status(), title).toBeGreaterThanOrEqual(200);
    expect(response.status(), title).toBeLessThan(300);
    await expect(page.getByRole("dialog")).not.toBeVisible();
    return response.json();
  };
  await page.goto(at("/access-roles"));
  await page.locator('[data-login-alias="local-admin"]').click();
  await expect(
    page.getByRole("heading", { name: "Roles", exact: true }),
  ).toBeVisible();
  const administrator = await request("/api/v1/me");
  const members = await request(
    `/api/v1/memberships?limit=25&subject=${encodeURIComponent(memberAlias)}&state=active&actorType=human&realmId=${encodeURIComponent(administrator.realmId)}`,
  );
  expect(members.items).toHaveLength(1);
  const member = members.items.find((m) => m.subject === memberAlias);
  expect(member?.id).toMatch(/^[0-9a-f-]{36}$/);
  expect(member.actorType).toBe("human");
  // PostgreSQL development provisioning starts this human with Reader. Revoke
  // only its owned Reader/companion grants in the UI before proving custom-only access.
  const removeBootstrap = async () => {
    console.log("Policy journey: remove owned bootstrap grants");
    const initial = await request(
      `/api/v1/access/assignments?membershipId=${member.id}&limit=25`,
    );
    // Root and subpath reuse retained policy history. Revoked rows are not
    // duplicates of live grants; inspect one complete bounded page instead of
    // assuming a fixed historical count after explicit repeat provisioning.
    expect(initial.items.length).toBeLessThanOrEqual(25);
    expect(initial.nextCursor).toBeUndefined();
    const active = initial.items.filter((a) => a.state === "active");
    expect(active.length).toBeLessThanOrEqual(3);
    const keys = new Set();
    for (const assignment of active) {
      const role = await request(
        `/api/v1/access/roles/${assignment.roleDefinitionId}`,
      );
      expect(role.builtin).toBe(true);
      expect([
        "reader",
        "discovery_reader",
        "feedback_member",
        "service_client",
      ]).toContain(role.key);
      if (role.key === "service_client") expect(role.permissions).toEqual([]);
      expect(keys.has(role.key)).toBe(false);
      keys.add(role.key);
      await page.goto(at(`/access-assignments?membershipId=${member.id}`));
      await page
        .locator(`[data-assignment-id="${assignment.id}"]`)
        .getByRole("button", {
          name: `Manage assignment ${assignment.id}`,
          exact: true,
        })
        .click();
      await confirm("Revoke access assignment");
    }
  };
  await removeBootstrap();
  await memberPage.goto(at("/access"));
  await memberPage.locator(`[data-login-alias="${memberAlias}"]`).click();
  await expect(
    memberPage.getByRole("heading", { name: "My access", exact: true }),
  ).toBeVisible();
  const beforeGrants = await read(memberPage, "/api/v1/me");
  expect(beforeGrants.status).toBe(200);
  expect(beforeGrants.data.actorType).toBe("human");
  expect(beforeGrants.data.permissions).toEqual([]);
  expect(beforeGrants.data.namespaceGrants).toEqual([]);
  const namespaces = await request("/api/v1/namespaces?limit=100");
  const namespace = namespaces.items.find(
    (item) => item.slug === "catalog-demo-v1",
  );
  expect(namespace?.id).toMatch(/^[0-9a-f-]{36}$/);
  const resources = await request("/api/v1/resources?limit=100");
  const resource = resources.items.find((r) => r.namespaceId === namespace.id);
  expect(resource?.id).toMatch(/^[0-9a-f-]{36}$/);
  console.log("Policy journey: create role, group and namespace assignment");
  const suffix = basePath ? "subpath" : "root";
  const roleName = `Browser scoped role ${suffix}`;
  const groupName = `Browser direct group ${suffix}`;
  await page.goto(at("/access-roles/new"));
  await page.getByLabel("Role name", { exact: true }).fill(roleName);
  for (const permission of [
    "namespace.read",
    "resource.read",
    "resource.create",
  ])
    await page
      .getByRole("checkbox", {
        name: permission,
        exact: true,
      })
      .check();
  await confirm("Create role");
  await page
    .getByRole("link", { name: "Open saved role", exact: true })
    .click();
  const roleId = new URL(page.url()).pathname.split("/").at(-1);
  expect(roleId).toMatch(/^[0-9a-f-]{36}$/);
  await expect(
    page.getByText("Role definition ID:", { exact: false }),
  ).toBeVisible();
  // Copying stages a new definition; its source and assignments remain unchanged.
  const originalRole = await request(`/api/v1/access/roles/${roleId}`);
  await page
    .getByRole("button", { name: "Copy to custom role", exact: true })
    .click();
  await page
    .getByLabel("Role name", { exact: true })
    .fill(`Browser copied role ${suffix}`);
  await confirm("Create role");
  await page
    .getByRole("link", { name: "Open saved role", exact: true })
    .click();
  const copiedRoleId = new URL(page.url()).pathname.split("/").at(-1);
  expect(copiedRoleId).not.toBe(roleId);
  const copiedRole = await request(`/api/v1/access/roles/${copiedRoleId}`);
  expect(copiedRole.permissions).toEqual(originalRole.permissions);
  expect(await request(`/api/v1/access/roles/${roleId}`)).toEqual(originalRole);
  const copiedAssignments = await request(
    `/api/v1/access/roles/${copiedRoleId}/assignments?limit=25`,
  );
  expect(copiedAssignments.items).toEqual([]);
  await page.goto(at("/access-roles"));
  await page
    .getByLabel("Exact name", { exact: true })
    .fill(`Browser copied role ${suffix}`);
  await page
    .getByRole("combobox", { name: "State", exact: true })
    .selectOption("active");
  const filteredRole = page.waitForResponse(
    (r) =>
      r.request().method() === "GET" &&
      new URL(r.url()).pathname.endsWith("/_catalog/api/v1/access/roles") &&
      new URL(r.url()).searchParams.get("name") ===
        `Browser copied role ${suffix}`,
  );
  await page
    .getByRole("button", { name: "Apply filters", exact: true })
    .click();
  expect((await filteredRole).status()).toBe(200);
  await expect(
    page.getByRole("link", {
      name: `Browser copied role ${suffix}`,
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: roleName, exact: true }),
  ).toHaveCount(0);
  await page
    .getByRole("button", { name: "Clear filters", exact: true })
    .click();
  await expect(page.getByLabel("Exact name", { exact: true })).toHaveValue("");
  // The owned qualification can retain more than a page of other roles.
  // Resolve this exact name through the bounded directory, not its first page.
  await page.getByLabel("Exact name", { exact: true }).fill(roleName);
  await page
    .getByRole("button", { name: "Apply filters", exact: true })
    .click();
  await expect(
    page.getByRole("link", { name: roleName, exact: true }),
  ).toBeVisible();
  await page.goto(at("/access-groups/new"));
  await page.getByLabel("Group name", { exact: true }).fill(groupName);
  await confirm("Create group");
  await page
    .getByRole("link", { name: "Open saved group", exact: true })
    .click();
  const groupId = new URL(page.url()).pathname.split("/").at(-1);
  await openDisclosure(page, "Add group member");
  const memberChoices = panel("Add group member");
  await memberChoices
    .getByLabel("Exact member subject", { exact: true })
    .fill(memberAlias);
  await memberChoices
    .getByRole("button", { name: "Apply choice filter", exact: true })
    .click();
  await memberChoices
    .getByRole("combobox", { name: "Member", exact: true })
    .selectOption(member.id);
  await expect(
    memberChoices.getByLabel("Membership ID", { exact: true }),
  ).toHaveValue(member.id);
  await confirm("Add group member");
  await page.goto(at("/access-assignments"));
  await openDisclosure(page, "Grant access");
  const grant = panel("Grant access assignment");
  await grant.getByLabel("Role definition ID", { exact: true }).fill(roleId);
  await grant
    .getByRole("combobox", { name: "Assignment target", exact: true })
    .selectOption("groupId");
  await grant.getByLabel("Member or group ID", { exact: true }).fill(groupId);
  await grant
    .getByRole("combobox", { name: "Assignment scope", exact: true })
    .selectOption("namespace");
  await grant.getByLabel("Namespace ID", { exact: true }).fill(namespace.id);
  await confirm(
    "Grant access assignment",
    grant,
    "access-assignment-confirmation",
  );
  const assignments = await request(
    `/api/v1/access/assignments?groupId=${groupId}&limit=25`,
  );
  expect(assignments.items).toHaveLength(1);
  expect(assignments.items[0]).toMatchObject({
    groupId,
    roleDefinitionId: roleId,
    namespaceId: namespace.id,
    state: "active",
  });
  const assignmentId = assignments.items[0].id;

  if (qualifyRetainedPolicy) {
    await qualifyRetainedPolicy({
      roleId,
      groupId,
      assignmentId,
      membershipId: member.id,
    });
    // Explicit owner provisioning may restore its configured built-in templates.
    // Remove those owned paths again before asserting custom-only authority.
    await removeBootstrap();
  }
  // A separate specialist grant adds library use without a tenant Reader.
  const library = await request(
    "/api/v1/access/roles?key=library_member&state=active&limit=1",
  );
  expect(library.items).toHaveLength(1);
  await page.goto(at(`/access-assignments?membershipId=${member.id}`));
  await openDisclosure(page, "Grant access");
  await panel("Grant access assignment")
    .getByLabel("Role definition ID", { exact: true })
    .fill(library.items[0].id);
  await panel("Grant access assignment")
    .getByRole("combobox", { name: "Assignment scope", exact: true })
    .selectOption("tenant");
  await confirm("Grant access assignment");
  const directLibrary = await request(
    `/api/v1/access/assignments?membershipId=${member.id}&roleDefinitionId=${library.items[0].id}&limit=25`,
  );
  const libraryAssignment = directLibrary.items.find(
    (item) => item.state === "active",
  );
  expect(libraryAssignment?.id).toMatch(/^[0-9a-f-]{36}$/);
  await page.goto(at("/members"));
  await page.getByLabel("Exact subject", { exact: true }).fill(memberAlias);
  await page
    .getByRole("combobox", { name: "State", exact: true })
    .selectOption("active");
  await page
    .getByRole("combobox", { name: "Identity type filter", exact: true })
    .selectOption("human");
  await page
    .getByLabel("Exact realm ID", { exact: true })
    .fill(administrator.realmId);
  const filteredMember = page.waitForResponse(
    (r) =>
      r.request().method() === "GET" &&
      new URL(r.url()).pathname.endsWith("/_catalog/api/v1/memberships") &&
      new URL(r.url()).searchParams.get("subject") === memberAlias,
  );
  await page
    .getByRole("button", { name: "Apply filters", exact: true })
    .click();
  expect((await filteredMember).status()).toBe(200);
  await expect(page.locator(".workspace-table tbody tr")).toHaveCount(1);
  await page.getByRole("link", { name: memberAlias, exact: true }).click();
  const contributions = page.getByRole("region", {
    name: "Member access contributions",
    exact: true,
  });
  await expect(contributions).toContainText(groupName);
  await expect(contributions).toContainText(assignmentId);
  await expect(contributions).toContainText(libraryAssignment.id);
  await expect(contributions).toContainText("partial");
  const projected = await request(
    `/api/v1/memberships/${member.id}/access?limit=25`,
  );
  expect(projected.groupCoverage).toBe("current_realm");
  expect(
    projected.items.some(
      (item) =>
        item.assignment.id === assignmentId &&
        item.group.id === groupId &&
        item.relation.membershipId === member.id &&
        item.effective,
    ),
  ).toBe(true);
  expect(
    projected.items.some(
      (item) =>
        item.assignment.id === libraryAssignment.id &&
        !item.group &&
        item.effective,
    ),
  ).toBe(true);
  if (captureDocumentation)
    await captureDocumentation("member-access-contributions", contributions);
  await memberPage.goto(at("/access"));
  await memberPage
    .getByRole("button", { name: "Refresh My access", exact: true })
    .click();
  await expect(
    memberPage.getByRole("heading", { name: "My access", exact: true }),
  ).toBeVisible();
  await expect(
    memberPage.getByText(`Group ${groupName}`, { exact: false }),
  ).toBeVisible();
  await expect(
    memberPage.locator(".policy-summary").filter({ hasText: groupId }),
  ).toContainText(namespace.displayName);
  const ownPolicy = memberPage
    .locator(".policy-summary")
    .filter({ hasText: groupId });
  await openDisclosure(ownPolicy, "Role template and permission keys");
  await expect(
    memberPage.getByText(
      "Role permissions: namespace.read, resource.create, resource.read.",
      { exact: true },
    ),
  ).toBeVisible();
  const ownAttribution = memberPage
    .locator(".policy-summary")
    .filter({ hasText: groupId })
    .filter({ hasText: groupName })
    .getByText("Assignment attribution", { exact: true });
  await openDisclosure(ownPolicy, "Assignment attribution");
  await expect(ownAttribution.locator("..").locator("pre")).toContainText(
    "Independent session qualification",
  );
  await expect(ownAttribution.locator("..").locator("pre")).toContainText(
    assignmentId,
  );
  // A metadata-only change refreshes the recipient's own caption without
  // directory authority, changing identity, or invalidating policy cursors.
  const selfBefore = await read(memberPage, "/api/v1/access/self?limit=25");
  expect(selfBefore.status).toBe(200);
  expect(
    selfBefore.data.items.find((item) => item.assignment.id === assignmentId)
      .groupName,
  ).toBe(groupName);
  const protectedReads = [];
  const observeGroups = (request) => {
    if (
      request.method() === "GET" &&
      /\/_catalog\/api\/v1\/access\/groups/.test(
        new URL(request.url()).pathname,
      )
    )
      protectedReads.push(request.url());
  };
  memberPage.on("request", observeGroups);
  await page.goto(at(`/access-groups/${groupId}`));
  const renamedGroupName = `${groupName} renamed`;
  await openDisclosure(page, "Edit group");
  await page.getByLabel("Group name", { exact: true }).fill(renamedGroupName);
  await confirm("Update group");
  const selfRefreshed = memberPage.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      new URL(response.url()).pathname ===
        basePath + "/_catalog/api/v1/access/self",
  );
  await memberPage
    .getByRole("button", { name: "Refresh assignment sources", exact: true })
    .click();
  const selfAfter = await (await selfRefreshed).json();
  expect(selfAfter.authorizationRevision).toBe(
    selfBefore.data.authorizationRevision,
  );
  expect(
    selfAfter.items.find((item) => item.assignment.id === assignmentId)
      .groupName,
  ).toBe(renamedGroupName);
  expect(
    selfAfter.items.find((item) => item.assignment.id === assignmentId)
      .assignment,
  ).toEqual(
    selfBefore.data.items.find((item) => item.assignment.id === assignmentId)
      .assignment,
  );
  const ownPath = memberPage
    .locator(".policy-summary")
    .filter({ hasText: groupId });
  await expect(ownPath).toHaveCount(1);
  await expect(ownPath).toContainText(renamedGroupName);
  await expect(ownPath.getByText(groupId, { exact: true })).toBeVisible();
  expect(protectedReads).toEqual([]);
  memberPage.off("request", observeGroups);
  const cookie = (await memberPage.context().cookies(at("/access"))).filter(
    (c) => !c.name.includes("login"),
  );
  expect(cookie.length).toBeGreaterThan(0);
  const refresh = async () => {
    await memberPage.goto(at("/access"));
    await memberPage
      .getByRole("button", { name: "Refresh My access", exact: true })
      .click();
    await expect(
      memberPage.getByRole("heading", { name: "My access", exact: true }),
    ).toBeVisible();
    // No sign-in/replacement is required for an active principal's policy change.
    expect(
      (await memberPage.context().cookies(at("/access"))).map((c) => ({
        name: c.name,
        value: c.value,
      })),
    ).toEqual(cookie.map((c) => ({ name: c.name, value: c.value })));
  };
  const expectOwnGroupPath = async (present) => {
    const self = await read(memberPage, "/api/v1/access/self?limit=100");
    expect(self.status).toBe(200);
    // This fixture has fewer than one page of grants. A truncated page cannot
    // establish that withdrawn evidence is absent.
    expect(self.data.nextCursor).toBeUndefined();
    const paths = self.data.items.filter(
      (item) => item.assignment.id === assignmentId,
    );
    expect(paths).toHaveLength(present ? 1 : 0);
    const visible = memberPage
      .locator(".policy-summary")
      .filter({ hasText: groupId });
    await expect(visible).toHaveCount(present ? 1 : 0);
    if (present) {
      expect(paths[0].assignment.groupId).toBe(groupId);
      expect(paths[0].groupName).toBe(renamedGroupName);
      await expect(visible).toContainText(renamedGroupName);
    } else {
      expect(
        self.data.items.some((item) => item.assignment.groupId === groupId),
      ).toBe(false);
      await expect(
        memberPage.getByText(renamedGroupName, { exact: false }),
      ).toHaveCount(0);
    }
  };
  await memberPage.goto(at("/catalog"));
  await expect(memberPage.locator("article.resource-card")).toHaveCount(
    expectedCatalogCount,
  );
  await memberPage.goto(at(`/resources/${resource.id}`));
  await expect(
    memberPage.getByRole("heading", {
      name: resource.canonicalIdentifier,
      exact: true,
    }),
  ).toBeVisible();
  await memberPage.goto(at("/resources/new"));
  await memberPage
    .getByRole("combobox", { name: "Namespace", exact: true })
    .selectOption(namespace.id);
  await expect(
    memberPage.getByRole("button", { name: "Create resource", exact: true }),
  ).toBeEnabled();

  console.log("Policy journey: edit, suspend, reactivate and revoke");
  // Exercise a real scoped authoring command, not just an enabled button.
  await memberPage
    .getByLabel("Canonical resource identifier", { exact: true })
    .fill(`urn:air:catalog.example.com:demo:custom-policy-${suffix}`);
  await memberPage
    .getByRole("button", { name: "Create resource", exact: true })
    .click();
  const createdResponse = memberPage.waitForResponse(
    (r) =>
      r.request().method() === "POST" &&
      new URL(r.url()).pathname === basePath + "/_catalog/api/v1/resources",
  );
  await memberPage
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  const createdReply = await createdResponse;
  expect(createdReply.status()).toBe(201);
  const authored = await createdReply.json();
  expect(authored.namespaceId).toBe(namespace.id);
  expect(authored.createdBy).toBe(member.subject);
  await expect(memberPage.getByRole("dialog")).not.toBeVisible();

  // Removing only creation preserves this group's discovery/read paths.
  await page.goto(at(`/access-roles/${roleId}`));
  await openDisclosure(page, "Edit role");
  await page
    .getByRole("checkbox", { name: "resource.create", exact: true })
    .uncheck();
  await confirm("Update role");
  await refresh();
  await expect(memberPage.locator(".grant-list")).not.toContainText(
    "resource.create",
  );
  await memberPage.goto(at("/resources/new"));
  await expect(
    memberPage.getByRole("button", { name: "Create resource", exact: true }),
  ).toHaveCount(0);
  await expect(
    memberPage.getByText(
      "The resource.create permission is required for this action.",
      { exact: true },
    ),
  ).toBeVisible();

  await page.goto(at(`/access-groups/${groupId}`));
  await openDisclosure(page, "Change group state");
  await panel("Change group state")
    .getByRole("combobox", { name: "Group action", exact: true })
    .selectOption("suspend");
  await confirm("Change group state");
  await refresh();
  await expectOwnGroupPath(false);
  await expect(
    memberPage.getByText("No namespace grants.", { exact: true }),
  ).toBeVisible();
  await expect(
    memberPage
      .getByRole("navigation", { name: "Workspace", exact: true })
      .getByRole("link", { name: "Catalog", exact: true }),
  ).toHaveCount(0);
  expectFailure("GET", `/_catalog/api/v1/resources/${resource.id}`, 403);
  const denied = await read(memberPage, `/api/v1/resources/${resource.id}`);
  expect(denied.status).toBe(403);
  expect(denied.data).toMatchObject({ status: 403, code: "FORBIDDEN" });
  await memberPage.goto(at(`/resources/${resource.id}`));
  await expect(memberPage.getByRole("alert")).toBeVisible();
  await expect(
    memberPage.getByRole("heading", {
      name: resource.canonicalIdentifier,
      exact: true,
    }),
  ).toHaveCount(0);

  await page.reload();
  await openDisclosure(page, "Change group state");
  await panel("Change group state")
    .getByRole("combobox", { name: "Group action", exact: true })
    .selectOption("reactivate");
  await confirm("Change group state");
  await refresh();
  await expectOwnGroupPath(true);
  await expect(memberPage.locator(".grant-list")).toContainText(namespace.id);
  await page.goto(at(`/access-assignments?groupId=${groupId}`));
  await page
    .locator(`[data-assignment-id="${assignmentId}"]`)
    .getByRole("button", {
      name: `Manage assignment ${assignmentId}`,
      exact: true,
    })
    .click();
  await confirm("Revoke access assignment");
  await refresh();
  await expectOwnGroupPath(false);
  await expect(
    memberPage.getByText("No namespace grants.", { exact: true }),
  ).toBeVisible();
  const final = await request(
    `/api/v1/access/assignments?groupId=${groupId}&limit=25`,
  );
  expect(final.items).toHaveLength(1);
  expect(final.items[0].state).toBe("revoked");
  await page
    .getByRole("button", { name: "Refresh assignments", exact: true })
    .click();
  const revokedRow = page.locator(`[data-assignment-id="${assignmentId}"]`);
  await revokedRow.getByText("Assignment attribution", { exact: true }).click();
  await expect(revokedRow.locator("pre")).toContainText(
    '"revokeReason": "Independent session qualification"',
  );
  await expect(
    revokedRow.getByRole("button", {
      name: `Manage assignment ${assignmentId}`,
      exact: true,
    }),
  ).toHaveCount(0);

  // Remove only this journey's library path; later retained-profile repeats stay isolated.
  await page.goto(at(`/access-assignments?membershipId=${member.id}`));
  await page
    .locator(`[data-assignment-id="${libraryAssignment.id}"]`)
    .getByRole("button", {
      name: `Manage assignment ${libraryAssignment.id}`,
      exact: true,
    })
    .click();
  await confirm("Revoke access assignment");
  await refresh();
  const afterLibrary = await read(memberPage, "/api/v1/me");
  expect(afterLibrary.status).toBe(200);
  expect(afterLibrary.data.permissions).toEqual([]);

  // Compose a real Reader in A, tenant library and disjoint metadata group in B.
  // These prerequisites are owned and unpublished; policy changes remain UI commands.
  const targetB = await page.evaluate(
    async ({ prefix, suffix }) => {
      const create = async (path, body) => {
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
          throw new Error("Owned mixed-scope prerequisite refused");
        return response.json();
      };
      const namespace = await create("/api/v1/namespaces", {
        slug: `qualified-b-${suffix}`,
        displayName: `Qualification B ${suffix}`,
      });
      const resource = await create("/api/v1/resources", {
        namespaceId: namespace.id,
        kind: "skill",
        canonicalIdentifier: `urn:air:catalog.example.com:demo:qualified-b-${suffix}`,
      });
      return { namespace, resource };
    },
    { prefix: basePath + "/_catalog", suffix },
  );
  const readerRole = await request(
    "/api/v1/access/roles?key=reader&state=active&limit=1",
  );
  expect(readerRole.items).toHaveLength(1);
  const mixedGrant = async (roleDefinitionId, target, namespaceId) => {
    await page.goto(at("/access-assignments"));
    await openDisclosure(page, "Grant access");
    const form = panel("Grant access assignment");
    await form
      .getByLabel("Role definition ID", { exact: true })
      .fill(roleDefinitionId);
    const groupTarget = target === "group";
    await form
      .getByRole("combobox", { name: "Assignment target", exact: true })
      .selectOption(groupTarget ? "groupId" : "membershipId");
    await form
      .getByLabel("Member or group ID", { exact: true })
      .fill(groupTarget ? groupId : member.id);
    await form
      .getByRole("combobox", { name: "Assignment scope", exact: true })
      .selectOption(namespaceId ? "namespace" : "tenant");
    if (namespaceId)
      await form.getByLabel("Namespace ID", { exact: true }).fill(namespaceId);
    return confirm("Grant access assignment");
  };
  const readerA = await mixedGrant(
    readerRole.items[0].id,
    "member",
    namespace.id,
  );
  const libraryGlobal = await mixedGrant(library.items[0].id, "member");
  const groupB = await mixedGrant(roleId, "group", targetB.namespace.id);
  await refresh();
  const readA = await read(memberPage, `/api/v1/resources/${resource.id}`);
  const readB = await read(
    memberPage,
    `/api/v1/resources/${targetB.resource.id}`,
  );
  expect(readA.status).toBe(200);
  expect(readB.status).toBe(200);
  expect(readB.data.namespaceId).toBe(targetB.namespace.id);
  const historyB = `/api/v1/resources/${targetB.resource.id}/versions`;
  expectFailure("GET", "/_catalog" + historyB, 403);
  const noHistoryB = await read(memberPage, historyB);
  expect(noHistoryB.status).toBe(403);
  expect(noHistoryB.data).toMatchObject({ status: 403, code: "FORBIDDEN" });
  const revokeMixed = async (grant, groupTarget = false) => {
    await page.goto(
      at(
        `/access-assignments?${groupTarget ? "groupId=" + groupId : "membershipId=" + member.id}`,
      ),
    );
    await page
      .locator(`[data-assignment-id="${grant.id}"]`)
      .getByRole("button", {
        name: `Manage assignment ${grant.id}`,
        exact: true,
      })
      .click();
    await confirm("Revoke access assignment");
  };
  const expectMixedPaths = async (ids) => {
    const paths = await read(memberPage, "/api/v1/access/self?limit=100");
    expect(paths.status).toBe(200);
    expect(paths.data.nextCursor).toBeUndefined();
    expect(paths.data.items.map((path) => path.assignment.id).sort()).toEqual(
      [...ids].sort(),
    );
  };
  await expectMixedPaths([readerA.id, libraryGlobal.id, groupB.id]);
  await revokeMixed(readerA);
  await refresh();
  const mixed = await read(memberPage, "/api/v1/me");
  expect(mixed.status).toBe(200);
  expect(mixed.data.permissions).toEqual(["library.own"]);
  expect(mixed.data.availablePermissions).toEqual([
    "library.own",
    "namespace.read",
    "resource.read",
  ]);
  expect(mixed.data.namespaceGrants).toEqual([
    {
      namespaceId: targetB.namespace.id,
      permissions: ["library.own", "namespace.read", "resource.read"],
    },
  ]);
  await expectMixedPaths([libraryGlobal.id, groupB.id]);
  await expect(memberPage.locator(".grant-list")).not.toContainText(
    namespace.id,
  );
  await expect(memberPage.locator(".grant-list")).toContainText(
    targetB.namespace.id,
  );
  await expect(
    memberPage
      .getByRole("navigation", { name: "Available tasks", exact: true })
      .getByRole("link", { name: "Saved items and searches", exact: true }),
  ).toBeVisible();
  expectFailure("GET", `/_catalog/api/v1/resources/${resource.id}`, 403);
  expect(
    (await read(memberPage, `/api/v1/resources/${resource.id}`)).status,
  ).toBe(403);
  expect(
    (await read(memberPage, `/api/v1/resources/${targetB.resource.id}`)).status,
  ).toBe(200);
  // Removing the group path must preserve the independent Library grant.
  await revokeMixed(groupB, true);
  await refresh();
  const libraryOnly = await read(memberPage, "/api/v1/me");
  expect(libraryOnly.status).toBe(200);
  expect(libraryOnly.data.permissions).toEqual(["library.own"]);
  expect(libraryOnly.data.availablePermissions).toEqual(["library.own"]);
  expect(libraryOnly.data.namespaceGrants).toEqual([]);
  await expectMixedPaths([libraryGlobal.id]);
  await expect(
    memberPage.getByText("No namespace grants.", { exact: true }),
  ).toBeVisible();
  await expect(
    memberPage
      .getByRole("navigation", { name: "Available tasks", exact: true })
      .getByRole("link", { name: "Saved items and searches", exact: true }),
  ).toBeVisible();
  await expect(
    memberPage
      .getByRole("navigation", { name: "Workspace", exact: true })
      .getByRole("link", { name: "Catalog", exact: true }),
  ).toHaveCount(0);
  expectFailure(
    "GET",
    `/_catalog/api/v1/resources/${targetB.resource.id}`,
    403,
  );
  const withdrawnB = await read(
    memberPage,
    `/api/v1/resources/${targetB.resource.id}`,
  );
  expect(withdrawnB.status).toBe(403);
  expect(withdrawnB.data).toMatchObject({ status: 403, code: "FORBIDDEN" });
  // Explicitly remove the final owned path before retained-profile repeats.
  await revokeMixed(libraryGlobal);
  await refresh();
  const cleared = await read(memberPage, "/api/v1/me");
  expect(cleared.status).toBe(200);
  expect(cleared.data.permissions).toEqual([]);
  expect(cleared.data.availablePermissions).toEqual([]);
  expect(cleared.data.namespaceGrants).toEqual([]);
  await expectMixedPaths([]);
  await expect(
    memberPage
      .getByRole("navigation", { name: "Available tasks", exact: true })
      .getByRole("link", { name: "Saved items and searches", exact: true }),
  ).toHaveCount(0);
  return { roleId, groupId, assignmentId, membershipId: member.id };
}
