import { object, array, text } from "./json";
import { required } from "../required";
import { test, expect, login, confirmCommand, signOut } from "./fixtures";

test("revoking a used publication domain reports affected releases without rewriting their history", async ({
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
  const role = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/access/roles",
    {
      name: "Publication domain delegate",
      permissions: ["publisher.read", "publisher.approve_domain"],
      reason: "Qualify domain command receipts",
    },
  );
  await catalog.admin("POST", "/api/v1/access/assignments", {
    membershipId: member.id,
    roleDefinitionId: role.data.id,
    reason: "Permit only domain approval workflow",
  });
  const candidate = await catalog.admin<{ id: string; etag: string }>(
    "POST",
    "/api/v1/publication/snapshots",
    { host: "127.0.0.1" },
  );
  await catalog.admin(
    "POST",
    `/api/v1/publication/snapshots/${candidate.data.id}:activate`,
    { host: "127.0.0.1", reason: "Qualify withdrawal receipt" },
    candidate.data.etag,
  );
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
    `/publishers/${catalog.receipt.publisherId}`,
  );
  const domains = page
    .locator("section.workspace-panel")
    .filter({
      has: page.getByRole("heading", {
        name: "Publication domains",
        exact: true,
      }),
    })
    .last();
  await domains
    .getByRole("textbox", { name: "Publication approval reason", exact: true })
    .fill("Withdraw the owned fixture's publication approval");
  await expect(
    domains.locator('.workspace-status[data-tone="success"]'),
  ).toHaveText("Approved");
  const revoke = page.getByRole("button", {
    name: "Revoke approval",
    exact: true,
  });
  await expect(revoke).toHaveClass("danger");
  await revoke.click();
  await expect(
    page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true }),
  ).toHaveClass("danger");
  const commandResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().includes("publication-domains/") &&
      response.url().endsWith(":revoke"),
  );
  await confirmCommand(page);
  await expect(page.getByRole("status")).toContainText(
    "Approval revoked. 3 affected releases.",
  );
  const received = await commandResponse;
  expect(received.status()).toBe(200);
  const originalBody = await received.text();
  const returned = object(JSON.parse(originalBody));
  const receipt = page.getByRole("region", {
    name: "Publication revocation receipt",
  });
  await expect(receipt).toBeVisible();
  for (const id of array(returned.affectedReleaseIds))
    await expect(receipt).toContainText(text(id));
  for (const host of array(returned.withdrawnHosts))
    await expect(receipt).toContainText(text(host));
  expect(array(returned.withdrawnHosts)).toEqual(["127.0.0.1"]);
  await expect(receipt.getByRole("link")).toHaveCount(0);
  expect(reads).toEqual([
    `/_catalog/api/v1/publishers/${catalog.receipt.publisherId}`,
  ]);
  await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
  await receipt
    .getByRole("button", { name: "Copy release ID", exact: true })
    .first()
    .click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
    text(array(returned.affectedReleaseIds)[0]),
  );
  // Changing this delegate's role must fence a cached command receipt while
  // preserving publisher reads. Restoring authority must replay, not execute.
  const readOnlyRole = await catalog.admin(
    "PATCH",
    `/api/v1/access/roles/${role.data.id}`,
    {
      name: "Publication domain delegate",
      permissions: ["publisher.read"],
      reason: "Remove approval authority but preserve publisher browsing",
    },
    role.etag,
  );
  const commandHeaders = received.request().headers();
  const exactCommand = {
    path: new URL(received.url()).pathname,
    body: required(received.request().postData()),
    key: required(commandHeaders["idempotency-key"]),
    match: required(commandHeaders["if-match"]),
  };
  const replay = () =>
    page.evaluate(async ({ path, body, key, match }) => {
      const reply = await fetch(path, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-Catalog-Request": "1",
          "Idempotency-Key": key,
          "If-Match": match,
        },
        body,
        redirect: "error",
      });
      return {
        status: reply.status,
        etag: reply.headers.get("ETag"),
        replayed: reply.headers.get("Idempotency-Replayed"),
        body: await reply.text(),
      };
    }, exactCommand);
  network.expectFailure("POST", exactCommand.path, 403);
  const denied = await replay();
  expect(denied.status).toBe(403);
  expect(object(JSON.parse(denied.body)).code).toBe("FORBIDDEN");
  expect(denied.etag).toBeNull();
  expect(denied.replayed).toBeNull();
  expect(denied.body).not.toContain('"affectedReleaseIds"');
  expect(denied.body).not.toContain('"withdrawnHosts"');
  for (const id of array(returned.affectedReleaseIds))
    expect(denied.body).not.toContain(text(id));
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Publication domains", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("region", { name: "Publication revocation receipt" }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Approve domain", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Revoke approval", exact: true }),
  ).toHaveCount(0);
  await catalog.admin(
    "PATCH",
    `/api/v1/access/roles/${role.data.id}`,
    {
      name: "Publication domain delegate",
      permissions: ["publisher.read", "publisher.approve_domain"],
      reason: "Restore only the original publication delegate authority",
    },
    readOnlyRole.etag,
  );
  const restored = await replay();
  expect(restored.status).toBe(200);
  expect(restored.replayed).toBe("true");
  expect(restored.etag).toBe(received.headers()["etag"]);
  expect(restored.body).toBe(originalBody);
  await page.reload();
  await expect(
    page.getByRole("button", { name: "Approve domain", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("region", { name: "Publication revocation receipt" }),
  ).toHaveCount(0);
  const publisher = await catalog.admin<{
    approvedPublicationDomains: string[];
    revision: number;
  }>("GET", `/api/v1/publishers/${catalog.receipt.publisherId}`);
  expect(publisher.data.approvedPublicationDomains || []).toEqual([]);
  expect(publisher.data.revision).toBe(object(returned.publisher).revision);
  const audit = await catalog.admin<{
    items: unknown[];
    nextCursor?: string;
  }>(
    "GET",
    "/api/v1/audit-events?action=publisher.publication_domain_revoked&limit=100",
  );
  expect(audit.data.items).toHaveLength(1);
  expect(audit.data.nextCursor).toBeUndefined();
  // Domain approval governs public snapshot eligibility, not the authenticated
  // local Catalog's immutable releases or their recorded visibility metadata.
  await signOut(page);
  await page.locator('[data-login-alias="local-reader"]').click();
  await page.goto(catalog.origin + "/catalog");
  await expect(page.locator("article.resource-card")).toHaveCount(3);
});

test("publisher metadata and declared-domain approvals persist independently", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-admin", "/publishers/new");
  await page
    .getByRole("textbox", { name: "Display name", exact: true })
    .fill("Browser publisher");
  await page
    .getByRole("textbox", { name: "Declared domains", exact: true })
    .fill(
      Array.from(
        { length: 65 },
        (_, index) => `domain-${index}.example.com`,
      ).join(","),
    );
  await page
    .getByRole("button", { name: "Create publisher", exact: true })
    .click();
  await expect(page.getByRole("alert")).toContainText(
    "Enter between 1 and 64 domain names.",
  );
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page
    .getByRole("textbox", { name: "Declared domains", exact: true })
    .fill("browser.example.com");
  await page
    .getByRole("button", { name: "Create publisher", exact: true })
    .click();
  await confirmCommand(page);
  await expect(page.getByRole("status")).toContainText(
    "Created publisher Browser publisher.",
  );
  await page.goto(catalog.origin + "/publishers");
  await page
    .getByRole("link", { name: "Browser publisher", exact: true })
    .click();
  const publisherURL = page.url();
  const display = page.getByRole("textbox", {
    name: "Display name",
    exact: true,
  });
  await display.fill("Unsaved publisher metadata");
  page.once("dialog", (dialog) => dialog.dismiss());
  await page
    .getByRole("button", { name: "Reload publisher", exact: true })
    .click();
  await expect(display).toHaveValue("Unsaved publisher metadata");
  page.once("dialog", (dialog) => dialog.accept());
  await page
    .getByRole("button", { name: "Reload publisher", exact: true })
    .click();
  await expect(display).toHaveValue("Browser publisher");

  await expect(
    page.getByRole("textbox", { name: "Declared domains", exact: true }),
  ).toHaveValue("browser.example.com");
  await expect(
    page.getByRole("button", { name: "Approve domain", exact: true }),
  ).toBeDisabled();
  const domains = page
    .locator("section.workspace-panel")
    .filter({
      has: page.getByRole("heading", {
        name: "Publication domains",
        exact: true,
      }),
    })
    .last();
  await domains
    .getByRole("textbox", { name: "Publication approval reason", exact: true })
    .fill("Browser domain approval");
  await page
    .getByRole("button", { name: "Approve domain", exact: true })
    .click();
  await confirmCommand(page);
  await expect(page.getByRole("status")).toContainText(
    "Domain approved for publication.",
  );
  await page.reload();
  await expect(
    page.getByRole("button", { name: "Revoke approval", exact: true }),
  ).toBeVisible();
  await domains
    .getByRole("textbox", { name: "Publication approval reason", exact: true })
    .fill("Remove browser approval");
  await page
    .getByRole("button", { name: "Revoke approval", exact: true })
    .click();
  await confirmCommand(page);
  await expect(page.getByRole("status")).toContainText(
    "Approval revoked. 0 affected releases.",
  );
  await page.reload();
  await expect(
    page.getByRole("button", { name: "Approve domain", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("textbox", { name: "Display name", exact: true })
    .fill("Renamed browser publisher");
  await expect(
    page.getByRole("button", { name: "Approve domain", exact: true }),
  ).toBeDisabled();
  await page
    .getByRole("textbox", { name: "Metadata change reason", exact: true })
    .fill("Correct display metadata");
  await page
    .getByRole("button", { name: "Review changes", exact: true })
    .click();
  await confirmCommand(page);
  await expect(page.getByRole("status")).toContainText("Publisher updated.");
  await page.reload();
  await expect(
    page.getByRole("heading", {
      name: "Renamed browser publisher",
      exact: true,
    }),
  ).toBeVisible();
  await signOut(page);
  await page.locator('[data-login-alias="local-reader"]').click();
  await page.goto(publisherURL);
  await expect(
    page.getByRole("textbox", { name: "Display name", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("textbox", { name: "Declared domains", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("textbox", { name: "Metadata change reason", exact: true }),
  ).toHaveCount(0);
  const savedFacts = page.locator("dl.workspace-kv").filter({
    has: page.getByText("Publisher ID", { exact: true }),
  });
  await expect(
    savedFacts.getByText("Display name", { exact: true }).locator("+ dd"),
  ).toHaveText("Renamed browser publisher");
  await expect(
    savedFacts.getByText("Declared domains", { exact: true }).locator("+ dd"),
  ).toHaveText("browser.example.com");
  await expect(
    savedFacts.getByText("Revision", { exact: true }).locator("+ dd"),
  ).toHaveText(/^[1-9]\d*$/);
  await expect(
    page.getByText(
      "Read-only. Publisher changes require publisher.create or publisher.manage for the selected action, and publishers.manage when scopes are enforced.",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Approve domain", exact: true }),
  ).toHaveCount(0);
});

test("unused resource deletion persists and release history prevents deletion", async ({
  page,
  catalog,
  network,
}) => {
  const created = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/resources",
    {
      namespaceId: catalog.receipt.namespaceId,
      canonicalIdentifier: "urn:air:catalog.example.com:demo:unused-browser",
      kind: "agent",
    },
  );
  await login(page, catalog, "local-admin", `/resources/${created.data.id}`);
  await page.getByText("Discard an unused resource", { exact: true }).click();
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Remove unused browser fixture");
  await page
    .getByRole("button", { name: "Delete unused resource", exact: true })
    .click();
  await confirmCommand(page);
  await expect(page.getByRole("status")).toContainText("Resource deleted.");
  network.expectFailure(
    "GET",
    `/_catalog/api/v1/resources/${created.data.id}`,
    404,
  );
  await page.reload();
  await expect(page.getByRole("alert")).toBeVisible();
  const published = catalog.receipt.items.find(
    (item) => item.key === "order-agent",
  )!;
  await page.goto(catalog.origin + `/resources/${published.resourceId}`);
  await page.getByText("Discard an unused resource", { exact: true }).click();
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("A release must prevent deletion");
  network.expectFailure(
    "POST",
    `/_catalog/api/v1/resources/${published.resourceId}:delete`,
    409,
  );
  await page
    .getByRole("button", { name: "Delete unused resource", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  await expect(page.getByRole("dialog").getByRole("alert")).toBeVisible();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: published.identifier, exact: true }),
  ).toBeVisible();
});

test("publisher name entry preserves the full supplementary-character budget", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-admin", "/publishers/new");
  const name = page.getByRole("textbox", { name: "Display name", exact: true });
  await name.pressSequentially("🙂".repeat(160));
  await expect(name).toHaveValue("🙂".repeat(160));
  await page
    .getByRole("textbox", { name: "Declared domains", exact: true })
    .fill("unicode.example.com");
  await name.fill("🙂".repeat(159) + "aa");
  await page
    .getByRole("button", { name: "Create publisher", exact: true })
    .click();
  await expect(page.getByRole("alert")).toContainText("160 characters");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await name.fill("🙂".repeat(160));
  await page
    .getByRole("button", { name: "Create publisher", exact: true })
    .click();
  await confirmCommand(page);
  await expect(page.getByRole("status")).toContainText("Created publisher");
  const publishers = await catalog.admin<{ items: { displayName: string }[] }>(
    "GET",
    "/api/v1/publishers?limit=100",
  );
  expect(
    publishers.data.items.some((item) => item.displayName === "🙂".repeat(160)),
  ).toBe(true);
});
