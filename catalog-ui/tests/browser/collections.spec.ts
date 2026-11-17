import AxeBuilder from "@axe-core/playwright";
import { test, expect, login, confirmCommand, signOut } from "./fixtures";
import { agentDocument } from "./published-fixture";
import { doubleText } from "./text-layout";
import type { components } from "../../src/api/schema";
import { required } from "../required";

test.use({ curated: true });
for (const location of ["root", "subpath"]) {
  test(`${location}: shared collections curate all kinds and follow current releases without granting management`, async ({
    page,
    catalog,
  }, info) => {
    const owner = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/namespaces",
      { slug: "shared-team", displayName: "Shared team" },
    );
    const records = await catalog.admin<{
      items: components["schemas"]["CatalogSummary"][];
    }>("GET", "/api/v1/catalog/resources?limit=100");
    const selected = ["agent", "mcp_server", "skill", "plugin"].map((kind) => {
      const record = records.data.items.find(
        (item) => item.resource.kind === kind,
      );
      expect(
        record,
        `curated fixture must include published ${kind}`,
      ).toBeTruthy();
      return record!;
    });
    await login(page, catalog, "local-contributor", "/collections");
    await page
      .getByRole("link", { name: "Create collection", exact: true })
      .click();
    await page
      .getByRole("combobox", { name: "Owner namespace", exact: true })
      .selectOption(owner.data.id);
    const name = "Finance " + "x".repeat(150);
    await page.getByRole("textbox", { name: "Name", exact: true }).fill(name);
    await page
      .getByRole("textbox", { name: "Description", exact: true })
      .fill(
        "Shared <metadata>, with each entry retaining its own permissions.",
      );
    await page
      .getByRole("button", { name: "Review collection", exact: true })
      .click();
    await confirmCommand(page);
    await page
      .getByRole("link", { name: "Open collection", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name, exact: true }),
    ).toBeVisible();
    const reviewButton = await page
      .getByRole("button", { name: "Review changes", exact: true })
      .boundingBox();
    const reloadButton = await page
      .getByRole("button", { name: "Reload collection", exact: true })
      .boundingBox();
    expect(
      reloadButton!.y - (reviewButton!.y + reviewButton!.height),
    ).toBeGreaterThanOrEqual(16);
    const collectionId = new URL(page.url()).pathname.split("/").at(-1)!;
    // Add each kind through the actual inspected local record and picker.
    for (const record of selected) {
      await page.goto(
        `${catalog.origin}${catalog.basePath}/catalog/${record.release.id}`,
      );
      await expect(
        page.getByRole("heading", { name: "Shared collections", level: 2 }),
      ).toBeVisible();
      await page
        .getByRole("button", { name: "Add to collection", exact: true })
        .click();
      await page
        .getByRole("combobox", { name: "Destination collection", exact: true })
        .selectOption(collectionId);
      await page
        .getByRole("button", { name: "Review addition", exact: true })
        .click();
      await confirmCommand(page);
      await expect(
        page
          .getByRole("status")
          .filter({ hasText: "Entry added to collection." }),
      ).toBeVisible();
    }
    await page.goto(
      `${catalog.origin}${catalog.basePath}/collections/${collectionId}`,
    );
    await expect(page.locator(".resource-card")).toHaveCount(4);
    const toolbar = await page
      .getByRole("button", { name: "Refresh current entries", exact: true })
      .boundingBox();
    const card = await page.locator(".resource-card").first().boundingBox();
    expect(card!.y - (toolbar!.y + toolbar!.height)).toBeGreaterThanOrEqual(16);
    for (const record of selected)
      await expect(
        page.getByRole("link", {
          name: record.listingRevision.displayName,
          exact: true,
        }),
      ).toBeVisible();
    await expect(page.locator("metadata")).toHaveCount(0);
    const wide = await new AxeBuilder({ page }).analyze();
    expect(wide.violations).toEqual([]);
    expect(wide.incomplete).toEqual([]);
    await page.screenshot({
      path: info.outputPath("collection-wide.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 390, height: 844 });
    await expect
      .poll(() => page.evaluate(() => document.documentElement.scrollWidth))
      .toBeLessThanOrEqual(390);
    await doubleText(page);
    await expect
      .poll(() => page.evaluate(() => document.documentElement.scrollWidth))
      .toBeLessThanOrEqual(390);
    await page.screenshot({
      path: info.outputPath("collection-narrow-large-text.png"),
      fullPage: true,
    });
    await page.reload();
    await page.setViewportSize({ width: 1280, height: 900 });
    // Publish a later Agent release through the ordinary governance API.
    const agent = selected[0];
    if (!agent) throw new Error("Published Agent fixture is missing");
    const oldCollection = await catalog.admin<{ revision: number }>(
      "GET",
      `/api/v1/collections/${collectionId}`,
    );
    const version = await catalog.admin<{ revision: { id: string } }>(
      "POST",
      `/api/v1/resources/${agent.resource.id}/versions`,
      {
        version: "1.1.0",
        representations: [
          {
            role: "primary",
            mediaType: "application/a2a-agent-card+json",
            data: { ...agentDocument, version: "1.1.0" },
          },
        ],
      },
    );
    const listing = await catalog.admin(
      "GET",
      `/api/v1/catalog-listings/${agent.listing.id}`,
    );
    const display = await catalog.admin(
      "POST",
      `/api/v1/catalog-listings/${agent.listing.id}/revisions`,
      {
        versionRevisionId: version.data.revision.id,
        publisherId: catalog.receipt.publisherId,
        displayName: "Current finance Agent",
        description: "A later current release, not copied collection metadata.",
        visibility: "tenant",
      },
      listing.etag,
    );
    const submission = await catalog.admin<{ id: string }>(
      "POST",
      `/api/v1/catalog-listings/${agent.listing.id}/submissions`,
      {},
    );
    await catalog.admin(
      "POST",
      `/api/v1/review-submissions/${submission.data.id}:approve`,
      { reason: "Review collection release-following fixture" },
      submission.etag,
      "local-reviewer",
    );
    const publication = await catalog.admin<{ release: { id: string } }>(
      "POST",
      `/api/v1/catalog-listings/${agent.listing.id}:publish`,
      { submissionId: submission.data.id },
      display.etag,
    );
    const currentCollection = await catalog.admin<{ revision: number }>(
      "GET",
      `/api/v1/collections/${collectionId}`,
    );
    expect(currentCollection.data.revision).toBe(oldCollection.data.revision);
    await page
      .getByRole("button", { name: "Refresh current entries", exact: true })
      .click();
    await expect(
      page.getByRole("link", { name: "Current finance Agent", exact: true }),
    ).toHaveAttribute(
      "href",
      `${catalog.basePath}/catalog/${publication.data.release.id}`,
    );
    // Ordinary readers can browse; the collection does not grant management.
    await signOut(page);
    await login(page, catalog, "local-reader", `/collections/${collectionId}`);
    await expect(page.locator(".resource-card")).toHaveCount(4);
    await expect(
      page.getByRole("button", { name: "Delete collection", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "Remove from collection", exact: true }),
    ).toHaveCount(0);
    await signOut(page);
    await login(
      page,
      catalog,
      "local-contributor",
      `/collections/${collectionId}`,
    );
    await page
      .getByRole("textbox", { name: "Name", exact: true })
      .fill("Finance tools");
    await page
      .getByRole("button", { name: "Review changes", exact: true })
      .click();
    await confirmCommand(page);
    await expect(
      page.getByRole("heading", { name: "Finance tools", exact: true }),
    ).toBeVisible();
    await page
      .locator(".resource-card")
      .filter({
        has: page.getByRole("link", {
          name: "Current finance Agent",
          exact: true,
        }),
      })
      .getByRole("button", { name: "Remove from collection", exact: true })
      .click();
    await confirmCommand(page);
    await expect(page.locator(".resource-card")).toHaveCount(3);
    await page
      .getByRole("button", { name: "Delete collection", exact: true })
      .click();
    await confirmCommand(page);
    await page
      .getByRole("link", { name: "Back to collections", exact: true })
      .click();
    await expect(page).toHaveURL(
      `${catalog.origin}${catalog.basePath}/collections`,
    );
    await expect(
      page.getByRole("link", { name: "Finance tools", exact: true }),
    ).toHaveCount(0);
  });
}

for (const location of ["root", "subpath"]) {
  test(`${location}: custom collection roles preserve target isolation, inert text and current revocation`, async ({
    page,
    catalog,
    network,
  }) => {
    const owner = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/namespaces",
      { slug: "delegated-collections", displayName: "Delegated collections" },
    );
    const records = await catalog.admin<{
      items: components["schemas"]["CatalogSummary"][];
    }>("GET", "/api/v1/catalog/resources?limit=100");
    const target = required(
      records.data.items.find((item) => item.resource.kind === "agent"),
    );
    const another = required(
      records.data.items.find((item) => item.listing.id !== target.listing.id),
    );
    expect(target.listing.namespaceId).not.toBe(owner.data.id);
    const name = "Delegated team tools";
    const description =
      '<img src="/collection-render-probe" onerror="globalThis.__oacCollectionProbe=1">';
    const created = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/collections",
      { namespaceId: owner.data.id, name, description },
    );
    const id = created.data.id;
    const added = await catalog.admin(
      "POST",
      `/api/v1/collections/${id}/members`,
      { listingId: target.listing.id },
      created.etag,
    );
    const members = await catalog.admin<{
      items: { id: string; subject: string }[];
    }>("GET", "/api/v1/memberships?limit=100");
    const member = required(
      members.data.items.find((item) => item.subject === "local-no-role"),
    );
    const grant = async (
      roleName: string,
      permissions: components["schemas"]["PermissionKey"][],
      namespaceId: string,
    ) => {
      const role = await catalog.admin<{ id: string }>(
        "POST",
        "/api/v1/access/roles",
        { name: roleName, permissions, reason: "Owned collection delegation" },
      );
      const assignment = await catalog.admin<{ id: string; revision: number }>(
        "POST",
        "/api/v1/access/assignments",
        {
          roleDefinitionId: role.data.id,
          membershipId: member.id,
          namespaceId,
          reason: "Qualify exact collection and target scopes",
        },
      );
      return { role, assignment };
    };
    const request = (
      method: string,
      path: string,
      body?: unknown,
      etag?: string | null,
    ) =>
      page.evaluate(
        async ({ url, method, body, etag }) => {
          const reply = await fetch(url, {
            method,
            credentials: "same-origin",
            headers: {
              "X-Catalog-Request": "1",
              ...(method === "GET"
                ? {}
                : { "Idempotency-Key": crypto.randomUUID() }),
              ...(etag ? { "If-Match": etag } : {}),
              ...(body === undefined
                ? {}
                : { "Content-Type": "application/json" }),
            },
            ...(body === undefined ? {} : { body: JSON.stringify(body) }),
          });
          const problem: unknown = await reply.json();
          const code =
            problem !== null && typeof problem === "object" && "code" in problem
              ? problem.code
              : undefined;
          return { status: reply.status, code };
        },
        { url: `${catalog.basePath}/_catalog${path}`, method, body, etag },
      );
    const collectionPath = `/api/v1/collections/${id}`;
    const uiPath = `${catalog.origin}${catalog.basePath}/collections/${id}`;
    await login(page, catalog, "local-no-role", "/collections");
    const navigation = page.getByRole("navigation", {
      name: "Workspace",
      exact: true,
    });
    await expect(
      navigation.getByRole("link", { name: "Shared collections", exact: true }),
    ).toHaveCount(0);
    network.expectFailure("GET", `/_catalog${collectionPath}`, 404);
    expect(await request("GET", collectionPath)).toEqual({
      status: 404,
      code: "NOT_FOUND",
    });

    const ownerGrant = await grant(
      "Collection metadata reader",
      ["collection.read"],
      owner.data.id,
    );
    await page.goto(uiPath);
    await expect(
      page.getByRole("heading", { name, exact: true }),
    ).toBeVisible();
    await expect(page.getByText(description, { exact: true })).toBeVisible();
    await expect(
      page.locator('img[src="/collection-render-probe"]'),
    ).toHaveCount(0);
    expect(
      await page.evaluate(() =>
        Reflect.has(globalThis, "__oacCollectionProbe"),
      ),
    ).toBe(false);
    await expect(page.locator(".resource-card")).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "Delete collection", exact: true }),
    ).toHaveCount(0);
    network.expectFailure("DELETE", `/_catalog${collectionPath}`, 403);
    expect(
      await request("DELETE", collectionPath, undefined, added.etag),
    ).toEqual({ status: 403, code: "FORBIDDEN" });

    await catalog.admin(
      "PATCH",
      `/api/v1/access/roles/${ownerGrant.role.data.id}`,
      {
        name: "Collection curator",
        permissions: ["collection.read", "collection.manage"],
        reason: "Explicitly enable management in the owner namespace",
      },
      ownerGrant.role.etag,
    );
    await page.reload();
    await expect(
      page.getByRole("button", { name: "Review changes", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Delete collection", exact: true }),
    ).toBeVisible();
    await expect(page.locator(".resource-card")).toHaveCount(0);
    network.expectFailure("POST", `/_catalog${collectionPath}/members`, 404);
    expect(
      await request(
        "POST",
        collectionPath + "/members",
        { listingId: another.listing.id },
        added.etag,
      ),
    ).toEqual({ status: 404, code: "NOT_FOUND" });

    const targetGrant = await grant(
      "Referenced entry reader",
      ["resource.read"],
      target.listing.namespaceId,
    );
    await page.reload();
    await expect(page.locator(".resource-card")).toHaveCount(1);
    await expect(
      page.getByRole("link", {
        name: target.listingRevision.displayName,
        exact: true,
      }),
    ).toBeVisible();
    await catalog.admin(
      "POST",
      `/api/v1/access/assignments/${targetGrant.assignment.data.id}:revoke`,
      {
        reason:
          "Withdraw target access without changing collection permissions",
      },
      targetGrant.assignment.etag,
    );
    await page.reload();
    await expect(page.locator(".resource-card")).toHaveCount(0);
    await expect(
      page.getByRole("heading", { name, exact: true }),
    ).toBeVisible();
    await catalog.admin(
      "POST",
      `/api/v1/access/assignments/${ownerGrant.assignment.data.id}:revoke`,
      {
        reason: "Withdraw collection access",
      },
      ownerGrant.assignment.etag,
    );
    network.expectFailure("GET", `/_catalog${collectionPath}`, 404);
    network.expectFailure("GET", `/_catalog${collectionPath}/members`, 404);
    const deniedReads = Promise.all(
      [collectionPath, collectionPath + "/members"].map((path) =>
        page.waitForResponse(
          (reply) =>
            new URL(reply.url()).pathname ===
              `${catalog.basePath}/_catalog${path}` && reply.status() === 404,
        ),
      ),
    );
    await page.reload();
    await deniedReads;
    await expect(page.getByRole("alert").first()).toBeVisible();
    await expect(page.getByRole("heading", { name, exact: true })).toHaveCount(
      0,
    );
    await expect(
      navigation.getByRole("link", { name: "Shared collections", exact: true }),
    ).toHaveCount(0);
    await expect(page.getByTitle("Account", { exact: true })).toBeVisible();
    const unchanged = await catalog.admin<{
      name: string;
      revision: number;
      description: string;
    }>("GET", collectionPath);
    expect(unchanged.data).toMatchObject({ name, description, revision: 2 });
    const retained = await catalog.admin<{
      items: components["schemas"]["CatalogSummary"][];
    }>("GET", collectionPath + "/members");
    expect(retained.data.items.map((item) => item.listing.id)).toEqual([
      target.listing.id,
    ]);
  });
}
