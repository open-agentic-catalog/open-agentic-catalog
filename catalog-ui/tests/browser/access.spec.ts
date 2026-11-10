import { openDisclosure } from "./editor-disclosure";
import { authorizationJourney } from "../../scripts/authorization-journey.mjs";
import { authorizationQualificationJourney } from "../../scripts/authorization-qualification-journey.mjs";
import { collectBrowserCoverage } from "../../scripts/e2e-coverage.mjs";
import { networkObserver } from "../../scripts/network-observer.mjs";
import { authorizationCommandOutcome } from "../../scripts/authorization-evidence.mjs";
import { required } from "../required";
import { test, expect, login, signOut, openAccount } from "./fixtures";
import { object, array, text } from "./json";

test("group archival refuses active members, then succeeds after explicit cleanup", async ({
  page,
  catalog,
  network,
}) => {
  const members = await catalog.admin<{
    items: { id: string; subject: string }[];
  }>("GET", "/api/v1/memberships?limit=100");
  const member = required(
    members.data.items.find((item) => item.subject === "local-reader"),
  );
  const group = await catalog.admin<{ id: string; revision: number }>(
    "POST",
    "/api/v1/access/groups",
    {
      name: "Archive cleanup control",
      reason: "Create owned archival control",
    },
  );
  const relation = await catalog.admin<{ id: string; revision: number }>(
    "POST",
    `/api/v1/access/groups/${group.data.id}/members`,
    { membershipId: member.id, reason: "Add explicit member control" },
  );
  await login(page, catalog, "local-admin", `/access-groups/${group.data.id}`);
  const browserCommands: string[] = [];
  const observeGroupCommands = (
    request: import("@playwright/test").Request,
  ) => {
    const path = new URL(request.url()).pathname;
    if (
      request.method() === "POST" &&
      path.startsWith(`/_catalog/api/v1/access/groups/${group.data.id}`)
    )
      browserCommands.push(path);
  };
  page.on("request", observeGroupCommands);
  await openDisclosure(page, "Change group state");
  const form = page.locator("section.workspace-panel").filter({
    has: page.getByRole("heading", {
      name: "Change group state",
      exact: true,
    }),
  });
  await form
    .getByRole("combobox", { name: "Group action", exact: true })
    .selectOption("archive");
  await form
    .getByRole("textbox", { name: "Lifecycle change reason", exact: true })
    .fill("Archive requires explicit member cleanup");
  network.expectFailure(
    "POST",
    `/_catalog/api/v1/access/groups/${group.data.id}:archive`,
    409,
  );
  await form
    .getByRole("button", { name: "Change group state", exact: true })
    .click();
  const [refusedArchiveResponse] = await Promise.all([
    page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        new URL(response.url()).pathname ===
          `/_catalog/api/v1/access/groups/${group.data.id}:archive`,
    ),
    page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click(),
  ]);
  expect(refusedArchiveResponse.status()).toBe(409);
  expect(object(await refusedArchiveResponse.json()).detail).toBe(
    "revoke active members before group archival",
  );
  await expect(form.getByRole("alert")).toContainText(/\(CONFLICT\)/);
  const retained = await catalog.admin<{ state: string; revision: number }>(
    "GET",
    `/api/v1/access/groups/${group.data.id}`,
  );
  expect(retained.data.state).toBe("active");
  expect(retained.data.revision).toBe(group.data.revision);
  await page.getByRole("button", { name: "Back to form", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await openDisclosure(page, "Change group state");
  await expect(form).toHaveCount(1);
  await form
    .getByRole("textbox", { name: "Lifecycle change reason", exact: true })
    .fill("");
  await page
    .getByRole("button", {
      name: `Manage membership ${member.id}`,
      exact: true,
    })
    .click();
  const membershipEditor = page.getByRole("region", {
    name: "Selected group membership editor",
    exact: true,
  });
  await expect(membershipEditor).toBeVisible();
  const retainedReason = membershipEditor.getByRole("textbox", {
    name: "Reason",
    exact: true,
  });
  await retainedReason.fill(
    "Keep this separate revocation draft through archive",
  );
  // A second authorized client completes the prerequisite cleanup. The browser
  // keeps its independent, now-stale revocation draft while archiving the group.
  await catalog.admin(
    "POST",
    `/api/v1/access/groups/${group.data.id}/members/${relation.data.id}:revoke`,
    { reason: "Explicit cleanup before terminal archive" },
    `"rev-${relation.data.revision}"`,
  );
  await openDisclosure(page, "Change group state");
  await form
    .getByRole("combobox", { name: "Group action", exact: true })
    .selectOption("archive");
  await form
    .getByRole("textbox", { name: "Lifecycle change reason", exact: true })
    .fill("Archive after explicit cleanup");
  await form
    .getByRole("button", { name: "Change group state", exact: true })
    .click();
  await expect(
    page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true }),
  ).toHaveClass("danger");
  const archivePath = `/api/v1/access/groups/${group.data.id}:archive`;
  const [archiveResponse] = await Promise.all([
    page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        new URL(response.url()).pathname === `/_catalog${archivePath}`,
    ),
    page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click(),
  ]);
  if (!archiveResponse.ok())
    console.error(
      "Owned archive outcome:",
      JSON.stringify(
        authorizationCommandOutcome({
          method: "POST",
          path: archivePath,
          status: archiveResponse.status(),
          problem: await archiveResponse.json().catch(() => ({})),
        }),
      ),
    );
  await expect(form.getByRole("status")).toContainText("Group state changed");
  const archived = await catalog.admin<{ state: string; revision: number }>(
    "GET",
    `/api/v1/access/groups/${group.data.id}`,
  );
  expect(archived.data.state).toBe("archived");
  expect(archived.data.revision).toBe(group.data.revision + 1);
  await expect(retainedReason).toHaveValue(
    "Keep this separate revocation draft through archive",
  );
  await expect(retainedReason).toBeDisabled();
  await expect(
    membershipEditor.getByRole("button", {
      name: "Revoke group member",
      exact: true,
    }),
  ).toBeDisabled();
  expect(browserCommands).toEqual([
    `/_catalog${archivePath}`,
    `/_catalog${archivePath}`,
  ]);
  await openDisclosure(page, "Add group member");
  await openDisclosure(page, "Grant access");
  await expect(
    page.getByRole("button", { name: "Add group member", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Grant access assignment", exact: true }),
  ).toHaveCount(0);
  await openDisclosure(page, "Edit group");
  await expect(
    page.getByRole("button", { name: "Update group", exact: true }),
  ).toHaveCount(0);
  await expect(
    form.getByRole("button", { name: "Change group state", exact: true }),
  ).toHaveCount(0);
  page.once("dialog", (dialog) => dialog.accept());
  await membershipEditor
    .getByRole("button", { name: "Close membership editor", exact: true })
    .click();
  await page.reload();
  await openDisclosure(page, "Add group member");
  await openDisclosure(page, "Grant access");
  await expect(membershipEditor).toHaveCount(0);
  for (const name of [
    "Add group member",
    "Grant access assignment",
    "Update group",
    "Change group state",
  ])
    await expect(page.getByRole("button", { name, exact: true })).toHaveCount(
      0,
    );
  await expect(page.getByLabel("Group action", { exact: true })).toHaveCount(0);
  expect(browserCommands).toEqual([
    `/_catalog${archivePath}`,
    `/_catalog${archivePath}`,
  ]);
  page.off("request", observeGroupCommands);
});

test("metadata-only Catalog readers gain content and history links only with their independent current grants", async ({
  page,
  catalog,
  network,
}) => {
  const skill = required(
    catalog.receipt.items.find((item) => item.key === "order-skill"),
  );
  const loaded = await catalog.admin(
    "GET",
    `/api/v1/catalog/resources/${skill.releaseId}`,
  );
  const representation = object(
    array(object(object(loaded.data).versionRevision).representations)[0],
  );
  const artifactId = text(representation.artifactId);
  const artifactPath = `/api/v1/artifacts/${artifactId}/content`;
  const members = await catalog.admin<{
    items: { id: string; subject: string }[];
  }>("GET", "/api/v1/memberships?limit=100");
  const member = required(
    members.data.items.find((item) => item.subject === "local-no-role"),
  );
  const grant = async (
    name: string,
    permission: string,
    namespaceId?: string,
  ) => {
    const role = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/access/roles",
      {
        name,
        permissions: [permission],
        reason: "Independent Catalog workflow permission",
      },
    );
    return catalog.admin<{ id: string; revision: number }>(
      "POST",
      "/api/v1/access/assignments",
      {
        roleDefinitionId: role.data.id,
        membershipId: member.id,
        ...(namespaceId ? { namespaceId } : {}),
        reason: "Exact current workflow permission",
      },
    );
  };
  await grant("Metadata only", "resource.read");
  await login(page, catalog, "local-no-role");
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  await page
    .locator("article.resource-card[data-kind=skill]")
    .getByRole("heading")
    .getByRole("button")
    .click();
  await expect(
    page.getByRole("link", {
      name: "Inspect retained version revision",
      exact: true,
    }),
  ).toHaveCount(0);
  await page
    .getByRole("link", { name: "Open full record", exact: true })
    .click();
  await expect(
    page.getByRole("link", { name: "Resource metadata", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Listing metadata", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Approved review", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("link", { name: "Release history", exact: true }),
  ).toHaveCount(0);
  const prepare = page.getByRole("button", {
    name: "Prepare retained document download",
    exact: true,
  });
  await expect(
    page.getByText(/current permission hint does not include content/),
  ).toBeVisible();
  network.expectFailure("GET", `/_catalog${artifactPath}`, 404);
  const denied = page.waitForResponse((reply) =>
    reply.url().endsWith(artifactPath),
  );
  await prepare.click();
  expect((await denied).status()).toBe(404);
  await expect(
    page.getByRole("link", { name: "Save retained document", exact: true }),
  ).toHaveCount(0);
  const content = await grant("Retained content only", "resource.read_content");
  await page.reload();
  await expect(
    page.getByRole("link", {
      name: "Inspect retained version revision",
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Release details", exact: true }),
  ).toHaveCount(0);
  const allowed = page.waitForResponse((reply) =>
    reply.url().endsWith(artifactPath),
  );
  await prepare.click();
  const allowedResponse = await allowed;
  expect(allowedResponse.status()).toBe(200);
  const retainedContent = (await allowedResponse.body()).toString("utf8");
  await expect(page.getByText(/Exact bytes verified/)).toBeVisible();
  await grant("History only", "resource.read_history");
  await page.reload();
  await expect(
    page.getByRole("link", { name: "Release history", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Approved review", exact: true }),
  ).toBeVisible();
  await catalog.admin(
    "POST",
    `/api/v1/access/assignments/${content.data.id}:revoke`,
    { reason: "Withdraw the independent content grant" },
    `"rev-${content.data.revision}"`,
  );
  await page.reload();
  await expect(
    page.getByRole("link", {
      name: "Inspect retained version revision",
      exact: true,
    }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("link", { name: "Release details", exact: true }),
  ).toBeVisible();
  // The same retained bytes have an independently authorized reference in another namespace.
  // This record's negative content hint must not suppress server admission through that reference.
  const namespace = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/namespaces",
    { slug: "shared-document", displayName: "Shared document reference" },
  );
  const resource = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/resources",
    {
      namespaceId: namespace.data.id,
      kind: "skill",
      canonicalIdentifier: "urn:air:demo.catalog.example.com:shared:document",
    },
  );
  const shared = await catalog.admin<{
    revision: { representations: { artifactId: string }[] };
  }>("POST", `/api/v1/resources/${resource.data.id}/versions`, {
    version: "1.0.0",
    representations: [
      {
        role: representation.role,
        mediaType: representation.mediaType,
        content: retainedContent,
      },
    ],
  });
  expect(required(shared.data.revision.representations[0]).artifactId).toBe(
    artifactId,
  );
  await grant(
    "Other namespace content",
    "resource.read_content",
    namespace.data.id,
  );
  await page.reload();
  await expect(
    page.getByText(/current permission hint does not include content/),
  ).toBeVisible();
  await expect(prepare).toBeEnabled();
  const alternate = page.waitForResponse((reply) =>
    reply.url().endsWith(artifactPath),
  );
  await prepare.click();
  expect((await alternate).status()).toBe(200);
  await expect(page.getByText(/Exact bytes verified/)).toBeVisible();
  await expect(
    page.getByRole("link", {
      name: "Inspect retained version revision",
      exact: true,
    }),
  ).toHaveCount(0);
});

test("specialist and task-only identities reach their own workspace without administrator or reader grants", async ({
  page,
  catalog,
}) => {
  for (const [alias, path, heading] of [
    ["local-source", "/sources", "Federation sources"],
    ["local-security", "/audit", "Audit investigation"],
    ["local-task", "/access", "My access"],
    ["local-no-role", "/access", "My access"],
  ] as const) {
    await login(page, catalog, alias, path);
    await expect(
      page.getByRole("heading", { name: heading, exact: true }),
    ).toBeVisible();
    const navigation = page.getByRole("navigation", {
      name: "Workspace",
      exact: true,
    });
    await expect(
      navigation.getByRole("link", { name: "Members", exact: true }),
    ).toHaveCount(0);
    await expect(
      navigation.getByRole("link", { name: "Catalog", exact: true }),
    ).toHaveCount(alias === "local-task" || alias === "local-source" ? 1 : 0);
    await page.goto(catalog.origin + "/members");
    await expect(page.getByRole("alert")).toContainText("access.manage");
    await page.goto(catalog.origin + "/access");
    await expect(
      page.getByRole("definition").filter({ hasText: alias }),
    ).toBeVisible();
    await expect(
      page.getByText("Additional scope checks are disabled", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("No namespace grants.", { exact: true }),
    ).toBeVisible();
    await signOut(page);
  }
});

test("namespace grants allow only the selected editor and are shown in My access", async ({
  page,
  catalog,
}) => {
  await catalog.admin("POST", "/api/v1/role-assignments", {
    subject: "local-scoped",
    actorType: "human",
    role: "contributor",
    namespaceId: catalog.receipt.namespaceId,
    reason: "Scoped browser fixture",
  });
  const other = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/namespaces",
    { slug: "other-space", displayName: "Other space" },
  );
  await login(page, catalog, "local-scoped", "/access");
  await expect(page.locator(".grant-list")).toContainText(
    catalog.receipt.namespaceId,
  );
  await expect(page.locator(".grant-list")).toContainText("resource.create");
  await page.goto(catalog.origin + "/resources/new");
  const namespace = page.getByRole("combobox", {
    name: "Namespace",
    exact: true,
  });
  await namespace.selectOption(catalog.receipt.namespaceId);
  await expect(
    page.getByRole("button", { name: "Create resource", exact: true }),
  ).toBeEnabled();
  await expect(
    namespace.locator(`option[value="${other.data.id}"]`),
  ).toHaveCount(0);
});

test("membership suspension denies the next browser request and clears private state", async ({
  page,
  catalog,
  network,
}) => {
  await login(page, catalog, "local-reader");
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  const members = await catalog.admin<{
    items: { id: string; subject: string }[];
  }>("GET", "/api/v1/memberships?limit=100");
  const member = members.data.items.find(
    (item) => item.subject === "local-reader",
  )!;
  const detail = await catalog.admin("GET", `/api/v1/memberships/${member.id}`);
  await catalog.admin(
    "POST",
    `/api/v1/memberships/${member.id}:suspend`,
    { reason: "Live-session revocation fixture" },
    detail.etag,
  );
  network.expectFailure("POST", "/_catalog/api/v1/catalog/search", 401);
  await page.getByRole("button", { name: "Search", exact: true }).click();
  await expect(page.locator("article.resource-card")).toHaveCount(0);
  await expect(page.locator('[data-login-alias="local-reader"]')).toBeVisible();
  await expect(page.getByTitle("Account", { exact: true })).toHaveCount(0);
});

test("revoking the only Reader grant clears resources and reloading refreshes access without signing out", async ({
  page,
  catalog,
}) => {
  const assignment = await catalog.admin<{ id: string; revision: number }>(
    "POST",
    "/api/v1/role-assignments",
    {
      subject: "local-no-role",
      actorType: "human",
      role: "reader",
      reason: "Temporary browser Reader grant",
    },
  );
  await login(page, catalog, "local-no-role");
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  await catalog.admin(
    "POST",
    `/api/v1/role-assignments/${assignment.data.id}:revoke`,
    { reason: "Revoke access during the active browser session" },
    `"rev-${assignment.data.revision}"`,
  );
  const search = page.waitForResponse(
    (reply) =>
      new URL(reply.url()).pathname === "/_catalog/api/v1/catalog/search",
  );
  await page.getByRole("button", { name: "Search", exact: true }).click();
  const response = await search;
  expect(response.status()).toBe(200);
  await expect(page.locator("article.resource-card")).toHaveCount(0);
  // Collection reads now succeed with an empty authorized population. Refresh
  // the existing session explicitly to reconcile its legacy navigation hints.
  await page.reload();
  await expect(page.getByTitle("Account", { exact: true })).toBeVisible();
  await expect(
    page
      .getByRole("navigation", { name: "Workspace", exact: true })
      .getByRole("link", { name: "Catalog", exact: true }),
  ).toHaveCount(0);
  await openAccount(page);
  await page.getByRole("link", { name: "My access", exact: true }).click();
  await expect(
    page.getByRole("definition").filter({ hasText: "local-no-role" }),
  ).toBeVisible();
  await expect(
    page.getByText("No namespace grants.", { exact: true }),
  ).toBeVisible();
});

test("sign-out cancels an old identity's pending search before another tenant signs in", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-reader");
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  let captured!: () => void;
  const pending = new Promise<void>((resolve) => {
    captured = resolve;
  });
  let release!: () => void;
  const released = new Promise<void>((resolve) => {
    release = resolve;
  });
  let first = true;
  await page.route("**/_catalog/api/v1/catalog/search", async (route) => {
    if (!first) return route.continue();
    first = false;
    const response = await route.fetch();
    expect(response.status()).toBe(200);
    captured();
    await released;
    await route.fulfill({ response });
  });
  try {
    const cancelled = page.waitForEvent(
      "requestfailed",
      (request) =>
        new URL(request.url()).pathname === "/_catalog/api/v1/catalog/search",
    );
    await page.getByRole("button", { name: "Search", exact: true }).click();
    await pending;
    await signOut(page);
    expect((await cancelled).failure()?.errorText).toBe("net::ERR_ABORTED");
    await page.locator('[data-login-alias="other-tenant-admin"]').click();
    await expect(
      page.getByRole("heading", { name: "No resources found" }),
    ).toBeVisible();
    release();
    await page.unrouteAll({ behavior: "wait" });
    await expect(page.locator("article.resource-card")).toHaveCount(0);
    await expect(page.getByTitle("Account", { exact: true })).toBeVisible();
  } finally {
    release();
  }
});

test("My access pages real grants and recovers from a failed next-page read", async ({
  page,
  catalog,
  network,
}) => {
  // /me defaults to twenty grants. One extra namespace exercises the real
  // signed cursor without inflating the shared five-item demo dataset.
  const ids: string[] = [];
  for (let index = 0; index < 21; index++) {
    const namespace = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/namespaces",
      { slug: `access-${index}`, displayName: `Access ${index}` },
    );
    ids.push(namespace.data.id);
    await catalog.admin("POST", "/api/v1/role-assignments", {
      subject: "local-scoped",
      actorType: "human",
      role: "contributor",
      namespaceId: namespace.data.id,
      reason: "Paged browser self-context",
    });
  }
  ids.sort();
  await login(page, catalog, "local-scoped", "/access");
  await expect(page.locator(".grant-list li")).toHaveCount(20);
  await expect(page.locator(".grant-list")).toContainText(required(ids[0]));
  network.expectFailure("GET", "/_catalog/api/v1/me", 503);
  let releasePage!: () => void;
  const heldPage = new Promise<void>((resolve) => {
    releasePage = resolve;
  });
  await page.route("**/_catalog/api/v1/me?cursor=*", async (route) => {
    await heldPage;
    await route.fulfill({
      status: 503,
      contentType: "application/problem+json",
      body: JSON.stringify({
        title: "Temporarily unavailable",
        status: 503,
        code: "unavailable",
      }),
    });
    await page.unroute("**/_catalog/api/v1/me?cursor=*");
  });
  const grantsPager = page
    .getByRole("heading", { name: "Namespace grants" })
    .locator("..")
    .getByRole("group", { name: "Namespace grants pagination" });
  await grantsPager
    .getByRole("button", { name: "Next page", exact: true })
    .focus();
  await page.keyboard.press("Enter");
  try {
    await expect(
      page.getByText("Loading grants...", { exact: true }),
    ).toBeVisible();
    await expect(grantsPager).toBeFocused();
    await expect(
      grantsPager.getByRole("button", { name: "Next page", exact: true }),
    ).toBeDisabled();
  } finally {
    releasePage();
  }
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(grantsPager).toBeFocused();
  await expect(page.locator(".grant-list")).toHaveCount(0);
  await page.getByRole("button", { name: "First page", exact: true }).click();
  await expect(page.locator(".grant-list li")).toHaveCount(20);
  await page.getByRole("button", { name: "Next page", exact: true }).click();
  await expect(page.locator(".grant-list li")).toHaveCount(1);
  await expect(grantsPager).toBeFocused();
  await expect(page.locator(".grant-list")).toContainText(required(ids[20]));
  await expect(
    page.getByRole("button", { name: "Next page", exact: true }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "First page", exact: true }).click();
  await expect(page.locator(".grant-list li")).toHaveCount(20);
  const selfReads: URL[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname.endsWith("/api/v1/me")) selfReads.push(url);
  });
  for (const namespaceId of [ids[0], ids[20]]) {
    const resource = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/resources",
      {
        namespaceId,
        kind: "agent",
        canonicalIdentifier: `urn:air:catalog.example.com:access:item-${namespaceId}`,
      },
    );
    await page.goto(catalog.origin + `/resources/${resource.data.id}`);
    await page.getByRole("button", { name: "Listings", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "Create listing", exact: true }),
    ).toBeEnabled();
  }
  expect(selfReads.filter((url) => url.searchParams.has("cursor"))).toEqual([]);
  expect(
    selfReads
      .filter((url) => url.searchParams.has("namespaceId"))
      .map((url) => url.searchParams.get("namespaceId")),
  ).toEqual([ids[20]]);
});

test("client expiry clears a view and explicit sign-in reopens saved server work", async ({
  page,
  catalog,
}) => {
  await page.clock.install();
  const item = catalog.receipt.items.find(
    (item) => item.key === "order-agent",
  )!;
  await login(page, catalog, "local-reader", `/resources/${item.resourceId}`);
  await expect(
    page.getByRole("link", { name: "Add version", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByText(
      "The resource.edit permission is required in this namespace.",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: item.identifier, exact: true }),
  ).toBeVisible();
  // Advance only page JavaScript. Host/token expiry has its independent Go and
  // provider tests; this checks the shell timer without changing the host clock.
  const appearance = page.getByLabel("Appearance", { exact: true });
  await appearance.focus();
  await page.clock.runFor(3480001);
  await expect(
    page.getByText(/Your session expires within two minutes/),
  ).toBeVisible();
  await expect(appearance).toBeFocused();
  await page.clock.runFor(120000);
  await expect(
    page.getByText(/Your session expires within two minutes/),
  ).toHaveCount(0);
  await expect(appearance).toBeFocused();
  await expect(page.locator('[data-login-alias="local-reader"]')).toBeVisible();
  await expect(
    page.getByRole("heading", { name: item.identifier, exact: true }),
  ).toHaveCount(0);
  await page.clock.setSystemTime(new Date());
  await page.clock.resume();
  await page.locator('[data-login-alias="local-reader"]').click();
  await expect(
    page.getByRole("heading", { name: item.identifier, exact: true }),
  ).toBeVisible();
  expect(await page.evaluate(() => Object.keys(sessionStorage))).toEqual([]);
});

test("revoking a prepared command's sole namespace grant denies the write and refreshes controls", async ({
  page,
  catalog,
  network,
}) => {
  // local-scoped has no tenant-wide permissions; this exact grant is its only access path.
  const assignment = await catalog.admin<{ id: string; revision: number }>(
    "POST",
    "/api/v1/role-assignments",
    {
      subject: "local-scoped",
      actorType: "human",
      role: "contributor",
      namespaceId: catalog.receipt.namespaceId,
      reason: "Prepared-command browser fixture",
    },
  );
  const identifier = "urn:air:catalog.example.com:demo:revoked-command";
  const before = await catalog.admin<{ items: { id: string }[] }>(
    "GET",
    "/api/v1/resources?limit=100",
  );
  await login(page, catalog, "local-scoped", "/resources/new");
  await page
    .getByRole("combobox", { name: "Namespace", exact: true })
    .selectOption(catalog.receipt.namespaceId);
  await page
    .getByRole("textbox", { name: "Canonical resource identifier" })
    .fill(identifier);
  await page
    .getByRole("button", { name: "Create resource", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await catalog.admin(
    "POST",
    `/api/v1/role-assignments/${assignment.data.id}:revoke`,
    { reason: "Revoke before confirmation" },
    `"rev-${assignment.data.revision}"`,
  );
  network.expectFailure("POST", "/_catalog/api/v1/resources", 403);
  let writes = 0;
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname === "/_catalog/api/v1/resources"
    )
      writes++;
  });
  const denied = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname === "/_catalog/api/v1/resources",
  );
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  const deniedResponse = await denied;
  expect(deniedResponse.status()).toBe(403);
  expect(object(await deniedResponse.json()).code).toBe("FORBIDDEN");
  // Current-policy reconciliation retains the entered form. This definite
  // refusal must close deliberately and cannot offer uncertain-result replay.
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("alert")).toContainText(
    "Your current access does not permit this action. (FORBIDDEN)",
  );
  await expect(
    page.getByRole("button", { name: "Retry same command", exact: true }),
  ).toHaveCount(0);
  await dialog
    .getByRole("button", { name: "Back to form", exact: true })
    .click();
  await expect(dialog).toHaveCount(0);
  await expect(
    page.getByRole("textbox", { name: "Canonical resource identifier" }),
  ).toHaveValue(identifier);
  await expect(
    page.getByRole("textbox", { name: "Namespace ID", exact: true }),
  ).toHaveValue(catalog.receipt.namespaceId);
  await expect(
    page.getByRole("textbox", { name: "Namespace ID", exact: true }),
  ).toBeDisabled();
  await expect(
    page.getByRole("combobox", { name: "Namespace", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Create resource", exact: true }),
  ).toBeDisabled();
  await expect(page.getByTitle("Account", { exact: true })).toBeVisible();
  const after = await catalog.admin<{ items: { id: string }[] }>(
    "GET",
    "/api/v1/resources?limit=100",
  );
  expect(after.data.items.map((item) => item.id).sort()).toEqual(
    before.data.items.map((item) => item.id).sort(),
  );
  expect(writes).toBe(1);
});

test("sign-in in another tab discards a prepared command across a tenant change", async ({
  page,
  context,
  sourceCoverage,
  catalog,
}) => {
  const before = await catalog.admin<{ items: { id: string }[] }>(
    "GET",
    "/api/v1/namespaces?limit=100",
  );
  // Keep a genuine signed-out chooser open: replacement is a login, not logout.
  const chooser = await context.newPage();
  await sourceCoverage.ready(chooser);
  await chooser.goto(catalog.origin + "/access");
  await expect(
    chooser.locator('[data-login-alias="other-tenant-admin"]'),
  ).toBeVisible();
  let writes = 0;
  context.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname === "/_catalog/api/v1/namespaces"
    )
      writes++;
  });
  await login(page, catalog, "local-admin", "/namespaces/new");
  await page
    .getByLabel("Display name", { exact: true })
    .fill("Private prepared namespace");
  await page.getByLabel("Slug", { exact: true }).fill("private-prepared");
  await page
    .getByRole("button", { name: "Create namespace", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toBeVisible();
  expect(writes).toBe(0);

  await chooser.locator('[data-login-alias="other-tenant-admin"]').click();
  await expect(
    chooser.getByRole("definition").filter({ hasText: "other-admin" }),
  ).toBeVisible();
  await expect(
    page.getByText(
      "Your session changed in another tab. Sign in to continue.",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page.getByLabel("Display name", { exact: true })).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Confirm", exact: true }),
  ).toHaveCount(0);
  expect(writes).toBe(0);

  // Re-entry into the same route must start a new form, never resume the old command.
  await page.locator('[data-login-alias="other-tenant-admin"]').click();
  await expect(page.getByLabel("Display name", { exact: true })).toHaveValue(
    "",
  );
  await expect(page.getByLabel("Slug", { exact: true })).toHaveValue("");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page
    .getByRole("link", { name: "Back to namespaces", exact: true })
    .click();
  await expect(
    page.getByText("No namespaces on this page.", { exact: true }),
  ).toBeVisible();
  const after = await catalog.admin<{ items: { id: string }[] }>(
    "GET",
    "/api/v1/namespaces?limit=100",
  );
  expect(after.data.items.map((item) => item.id).sort()).toEqual(
    before.data.items.map((item) => item.id).sort(),
  );
  expect(writes).toBe(0);
});

test("membership-only grants and exact filtered directories preserve actual paging", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-admin", "/members");
  await authorizationQualificationJourney({
    page,
    origin: catalog.origin,
    basePath: catalog.basePath,
  });
});

test("custom roles and group grants reconcile two independent browser sessions", async ({
  page,
  browser,
  catalog,
}, info) => {
  if (process.env.CATALOG_UI_E2E_COVERAGE)
    info.annotations.push({ type: "coverage-extra-context" });
  const context = await browser.newContext();
  const coverage = await collectBrowserCoverage(
    context,
    process.env.CATALOG_UI_E2E_COVERAGE,
  );
  const ledger = networkObserver(catalog.origin, catalog.basePath);
  const errors: string[] = [];
  context.on("response", (r) =>
    ledger.observe(r.request().method(), r.url(), r.status()),
  );
  context.on("requestfailed", (r) =>
    ledger.failure(r.method(), r.url(), r.failure()?.errorText),
  );
  const memberPage = await context.newPage();
  memberPage.on("pageerror", () => errors.push("pageerror"));
  await coverage.ready(memberPage);
  try {
    const qualification = await authorizationJourney({
      page,
      memberPage,
      origin: catalog.origin,
      basePath: catalog.basePath,
      memberAlias: "local-policy",
      expectFailure: ledger.expectFailure,
      captureDocumentation: async (name, target) => {
        await target.screenshot({ path: info.outputPath(name + ".png") });
      },
    });
    // Reuse the journey's owned group and existing assignments; selection sends no command.
    await page.goto(
      `${catalog.origin}${catalog.basePath}/access-groups/${qualification.groupId}`,
    );
    const membershipOpener = page.getByRole("button", {
      name: `Manage membership ${qualification.membershipId}`,
      exact: true,
    });
    await membershipOpener.focus();
    await membershipOpener.press("Enter");
    const membershipEditor = page.getByRole("region", {
      name: "Selected group membership editor",
      exact: true,
    });
    await expect(membershipEditor).toBeFocused();
    await expect(
      membershipEditor.getByRole("heading", {
        name: "Revoke group member",
        exact: true,
      }),
    ).toBeInViewport();
    await membershipEditor
      .getByRole("button", { name: "Close membership editor", exact: true })
      .click();
    await expect(membershipOpener).toBeFocused();
    await page.goto(`${catalog.origin}${catalog.basePath}/access-assignments`);
    const assignmentOpener = page
      .getByRole("button", { name: /^Manage assignment / })
      .first();
    await assignmentOpener.focus();
    await assignmentOpener.press("Enter");
    const assignmentEditor = page.getByRole("region", {
      name: "Selected access assignment editor",
      exact: true,
    });
    await expect(assignmentEditor).toBeFocused();
    await expect(
      assignmentEditor.getByRole("heading", {
        name: "Revoke access assignment",
        exact: true,
      }),
    ).toBeInViewport();
    const reason = assignmentEditor.getByRole("textbox", {
      name: "Reason",
      exact: true,
    });
    await reason.fill("Keep this unsent revocation draft");
    page.once("dialog", (dialog) => dialog.dismiss());
    await assignmentEditor
      .getByRole("button", { name: "Close assignment editor", exact: true })
      .click();
    await expect(reason).toHaveValue("Keep this unsent revocation draft");
    page.once("dialog", (dialog) => dialog.accept());
    await assignmentEditor
      .getByRole("button", { name: "Close assignment editor", exact: true })
      .click();
    await expect(assignmentOpener).toBeFocused();
    expect(ledger.results()).toEqual([]);
    expect(errors).toEqual([]);
  } finally {
    await coverage.finish();
    await context.close();
  }
});

test("namespace managers resolve a grant beyond the first self-context page without tenant-wide Reader", async ({
  page,
  catalog,
}) => {
  const members = await catalog.admin<{
    items: { id: string; subject: string }[];
  }>("GET", "/api/v1/memberships?limit=100");
  const membership = required(
    members.data.items.find((item) => item.subject === "local-no-role"),
  );
  const role = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/access/roles",
    {
      name: "Scoped namespace manager",
      permissions: ["namespace.read", "namespace.manage"],
      reason: "Later-page namespace regression",
    },
  );
  const namespaces: { id: string; displayName: string }[] = [];
  for (let index = 0; index < 21; index++) {
    const namespace = await catalog.admin<{ id: string; displayName: string }>(
      "POST",
      "/api/v1/namespaces",
      {
        slug: `scoped-manager-${index}`,
        displayName: `Managed namespace ${index}`,
      },
    );
    namespaces.push(namespace.data);
    await catalog.admin("POST", "/api/v1/access/assignments", {
      membershipId: membership.id,
      roleDefinitionId: role.data.id,
      namespaceId: namespace.data.id,
      reason: "Exact namespace grant",
    });
  }
  await login(page, catalog, "local-no-role", "/access");
  const access = await page.evaluate(async () => {
    const reply = await fetch("/_catalog/api/v1/me", {
      headers: { "X-Catalog-Request": "1" },
    });
    return (await reply.json()) as {
      permissions: string[];
      namespaceGrants: { namespaceId: string }[];
      nextCursor?: string;
    };
  });
  expect(access.permissions).toEqual([]);
  expect(access.namespaceGrants).toHaveLength(20);
  expect(access.nextCursor).toBeTruthy();
  const first = required(
    namespaces.find((item) =>
      access.namespaceGrants.some((grant) => grant.namespaceId === item.id),
    ),
  );
  const later = required(
    namespaces.find(
      (item) =>
        !access.namespaceGrants.some((grant) => grant.namespaceId === item.id),
    ),
  );
  for (const namespace of [first, later]) {
    await page.goto(catalog.origin + `/namespaces/${namespace.id}`);
    await expect(
      page.getByRole("textbox", { name: "Display name", exact: true }),
    ).toBeEnabled();
    await page
      .getByRole("textbox", { name: "Display name", exact: true })
      .fill(`${namespace.displayName} updated`);
    await page
      .getByRole("textbox", { name: "Reason", exact: true })
      .fill("Confirm scoped namespace management");
    await page
      .getByRole("button", { name: "Review changes", exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    await expect(page.getByRole("status")).toContainText("Namespace updated");
    const stored = await catalog.admin<{ displayName: string }>(
      "GET",
      `/api/v1/namespaces/${namespace.id}`,
    );
    expect(stored.data.displayName).toBe(`${namespace.displayName} updated`);
  }
});

test("a policy change invalidates a continuation and First page recovers without replaying mutations", async ({
  page,
  catalog,
  network,
}) => {
  const role = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/access/roles",
    {
      name: "Cursor population",
      permissions: ["namespace.read"],
      reason: "Continuation regression",
    },
  );
  const members = await catalog.admin<{
    items: { id: string; subject: string }[];
  }>("GET", "/api/v1/memberships?limit=100");
  const membership = required(
    members.data.items.find((member) => member.subject === "local-no-role"),
  );
  for (let index = 0; index < 26; index++) {
    await catalog.admin("POST", "/api/v1/access/assignments", {
      roleDefinitionId: role.data.id,
      membershipId: membership.id,
      reason: `Assignment ${index}`,
    });
    // Avoid duplicate active assignments to the same role/member by ending each
    // retained grant; the inventory still has more than one page of history.
    const grants = await catalog.admin<{
      items: { id: string; revision: number; state: string }[];
    }>(
      "GET",
      `/api/v1/access/assignments?roleDefinitionId=${role.data.id}&limit=100`,
    );
    const active = required(
      grants.data.items.find((item) => item.state === "active"),
    );
    await catalog.admin(
      "POST",
      `/api/v1/access/assignments/${active.id}:revoke`,
      { reason: "Retained cursor population" },
      `"rev-${active.revision}"`,
    );
  }
  await login(
    page,
    catalog,
    "local-admin",
    `/access-assignments?roleDefinitionId=${role.data.id}`,
  );
  await expect(
    page.getByRole("button", { name: "Next page", exact: true }),
  ).toBeVisible();
  await catalog.admin("POST", "/api/v1/access/roles", {
    name: "Policy revision change",
    permissions: ["namespace.read"],
    reason: "Invalidate the retained continuation",
  });
  network.expectFailure("GET", "/_catalog/api/v1/access/assignments", 400);
  await page.getByRole("button", { name: "Next page", exact: true }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(page.locator("[data-assignment-id]")).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "First page", exact: true }),
  ).toBeEnabled();
  const restart = page.waitForResponse(
    (reply) =>
      new URL(reply.url()).pathname === "/_catalog/api/v1/access/assignments" &&
      reply.status() === 200,
  );
  await page.getByRole("button", { name: "First page", exact: true }).focus();
  await page.keyboard.press("Enter");
  const response = await restart;
  expect(new URL(response.url()).searchParams.has("cursor")).toBe(false);
  expect(new URL(response.url()).searchParams.get("roleDefinitionId")).toBe(
    role.data.id,
  );
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(page.locator("[data-assignment-id]")).toHaveCount(25);
  await expect(
    page.getByRole("group", {
      name: "Assignment inventory pagination",
      exact: true,
    }),
  ).toBeFocused();
});
