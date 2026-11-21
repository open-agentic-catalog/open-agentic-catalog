import { expect, login, signOut, test } from "./fixtures";
import { publishFixture } from "./published-fixture";
import type { components } from "../../src/api/schema";
import { required } from "../required";

for (const location of ["root", "subpath"]) {
  test(`${location}: inspect missing released metadata and preserve another author's candidate during correction`, async ({
    page,
    catalog,
    network,
  }) => {
    const releasedName = "Release without documentation";
    const releaseID = await publishFixture(catalog, 112, "", {
      displayName: releasedName,
    });
    const { data: record } = await catalog.admin<
      components["schemas"]["CatalogRecord"]
    >("GET", "/api/v1/catalog/resources/" + releaseID);
    const listingPath = "/api/v1/catalog-listings/" + record.listing.id;
    const listing = await catalog.admin("GET", listingPath);
    const candidateName = "Documentation added for review";
    const candidateBody = {
      versionRevisionId: record.versionRevision.id,
      displayName: candidateName,
      description: "Candidate contains the correction",
      visibility: "tenant",
      publisherId: catalog.receipt.publisherId,
      documentationUrl: "https://docs.example.test/guide",
    };
    const candidate = await catalog.admin(
      "POST",
      listingPath + "/revisions",
      candidateBody,
      listing.etag,
    );
    const members = await catalog.admin<{
      items: { id: string; subject: string }[];
    }>("GET", "/api/v1/memberships?limit=100");
    const member = required(
      members.data.items.find((entry) => entry.subject === "local-no-role"),
    );
    const role = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/access/roles",
      {
        name: "Released metadata reader",
        permissions: ["resource.read"],
        reason: "Read released metadata only",
      },
    );
    await catalog.admin("POST", "/api/v1/access/assignments", {
      roleDefinitionId: role.data.id,
      membershipId: member.id,
      namespaceId: catalog.receipt.namespaceId,
      reason: "Read this fixture namespace",
    });
    await login(page, catalog, "local-no-role", "/resources");
    await page
      .getByRole("link", { name: "Missing Listing metadata", exact: true })
      .click();
    const row = page.getByRole("row").filter({
      has: page.getByRole("link", { name: releasedName, exact: true }),
    });
    await expect(
      row.getByRole("cell", {
        name: "Documentation URL, Support URL",
        exact: true,
      }),
    ).toBeVisible();
    await row.getByText("Release references", { exact: true }).click();
    await expect(
      row.getByText(record.listingRevision.id, { exact: true }),
    ).toBeVisible();
    await expect(
      row.getByRole("link", { name: record.listingRevision.id }),
    ).toHaveCount(0);
    await expect(
      row.getByRole("link", { name: "Edit current candidate" }),
    ).toHaveCount(0);
    await page.getByRole("checkbox", { name: "Support URL" }).uncheck();
    await page.getByRole("checkbox", { name: "License declaration" }).uncheck();
    await expect(
      row.getByRole("cell", {
        name: "Documentation URL, Support URL",
        exact: true,
      }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Apply filters" }).click();
    await expect(
      row.getByRole("cell", { name: "Documentation URL", exact: true }),
    ).toBeVisible();
    await page
      .getByLabel("Namespace ID", { exact: true })
      .fill("00000000-0000-4000-8000-000000000112");
    await page.getByRole("button", { name: "Apply filters" }).click();
    await expect(
      page.getByText("No current releases are missing the selected fields."),
    ).toBeVisible();
    await page
      .getByLabel("Namespace ID", { exact: true })
      .fill(catalog.receipt.namespaceId);
    await page.getByRole("button", { name: "Apply filters" }).click();
    await row.getByRole("link", { name: releasedName }).click();
    await expect(page).toHaveURL(
      catalog.origin + catalog.basePath + "/catalog/" + releaseID,
    );
    await expect(
      page.getByText(releasedName, { exact: true }).first(),
    ).toBeVisible();
    await signOut(page);
    await login(page, catalog, "local-contributor", "/listing-metadata-gaps");
    await row.getByText("Release references", { exact: true }).click();
    await expect(
      row.getByRole("link", { name: record.listingRevision.id }),
    ).toHaveAttribute(
      "href",
      catalog.basePath + "/listing-revisions/" + record.listingRevision.id,
    );
    await page.setViewportSize({ width: 390, height: 844 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth + 1,
      ),
    ).toBe(true);
    await row.getByRole("link", { name: "Edit current candidate" }).click();
    await page.getByRole("button", { name: "New listing revision" }).click();
    await expect(page.getByLabel("Display name", { exact: true })).toHaveValue(
      candidateName,
    );
    await page
      .getByText("Links, license and relationships", { exact: true })
      .click();
    await expect(
      page.getByLabel("Documentation URL", { exact: true }),
    ).toHaveValue(candidateBody.documentationUrl);
    await page
      .getByLabel("Support URL", { exact: true })
      .fill("https://support.example.test/help");
    await page
      .getByRole("button", { name: "Create listing revision", exact: true })
      .click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible();

    const newer = await catalog.admin<{ revision: { id: string } }>(
      "POST",
      listingPath + "/revisions",
      { ...candidateBody, displayName: "Another author's candidate" },
      candidate.etag,
    );
    network.expectFailure(
      "POST",
      "/_catalog" + listingPath + "/revisions",
      412,
    );
    await dialog.getByRole("button", { name: "Confirm", exact: true }).click();
    await expect(dialog.getByRole("alert")).toContainText(
      "The record changed. Your input is retained.",
    );
    await expect(
      dialog.getByRole("button", { name: "Retry same command" }),
    ).toHaveCount(0);
    const retained = await catalog.admin<
      components["schemas"]["CatalogListing"]
    >("GET", listingPath);
    expect(retained.etag).toBe(newer.etag);
    expect(retained.data.candidateRevisionId).toBe(newer.data.revision.id);
    expect(retained.data.currentReleaseId).toBe(releaseID);
  });
}
