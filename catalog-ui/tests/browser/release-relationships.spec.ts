import { test, expect, login, signOut } from "./fixtures";
import type { components } from "../../src/api/schema";

for (const location of ["root", "subpath"]) {
  test(`${location}: follow a declared dependency and find the exact referring release`, async ({
    page,
    catalog,
  }) => {
    const [source, target] = catalog.receipt.items.filter(
      (item) => item.releaseId,
    );
    if (!source || !target?.releaseId)
      throw new Error("Two published fixtures required");
    const record = await catalog.admin<components["schemas"]["CatalogRecord"]>(
      "GET",
      "/api/v1/catalog/resources/" + target.releaseId,
    );
    const listing = await catalog.admin(
      "GET",
      "/api/v1/catalog-listings/" + source.listingId,
    );
    const revision = await catalog.admin(
      "POST",
      "/api/v1/catalog-listings/" + source.listingId + "/revisions",
      {
        versionRevisionId: source.versionRevisionId,
        publisherId: catalog.receipt.publisherId,
        displayName: "Reader relationship fixture",
        description: "Points to the published target",
        visibility: "tenant",
        relationships: [
          {
            type: "depends_on",
            targetIdentifier: target.identifier,
            targetVersion: record.data.version.version,
          },
          { type: "depends_on", targetIdentifier: target.identifier },
        ],
      },
      listing.etag,
    );
    const submission = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/catalog-listings/" + source.listingId + "/submissions",
      {},
    );
    await catalog.admin(
      "POST",
      "/api/v1/review-submissions/" + submission.data.id + ":approve",
      { reason: "Reviewed dependency declarations" },
      submission.etag,
      "local-reviewer",
    );
    const published = await catalog.admin<{ release: { id: string } }>(
      "POST",
      "/api/v1/catalog-listings/" + source.listingId + ":publish",
      { submissionId: submission.data.id },
      revision.etag,
    );
    const sourceID = published.data.release.id;

    // Exercise real HTTP cursor binding and the limit+1 boundary on two edges from one release.
    const path = `/api/v1/listing-releases/${sourceID}/relationships?view=outgoing&limit=1`;
    const first = await catalog.admin<
      components["schemas"]["ReleaseRelationshipPage"]
    >("GET", path);
    expect(first.data.items).toHaveLength(1);
    expect(first.data.nextCursor).toBeTruthy();
    const last = await catalog.admin<
      components["schemas"]["ReleaseRelationshipPage"]
    >("GET", path + "&cursor=" + encodeURIComponent(first.data.nextCursor!));
    expect(last.data.items).toHaveLength(1);
    expect(last.data.nextCursor).toBeUndefined();
    expect(last.data.items[0]?.declaration.targetVersion).toBe(
      record.data.version.version,
    );
    expect(first.data.items[0]?.declaration.targetVersion).toBeUndefined();
    expect(last.data.items[0]?.related?.listingRevisionId).toBe(
      record.data.listingRevision.id,
    );

    await login(page, catalog, "local-reader", "/catalog/" + sourceID);
    const outgoing = page.getByRole("region", {
      name: "Declared relationships",
      exact: true,
    });
    await expect(
      outgoing.getByText("(unversioned)", { exact: false }),
    ).toBeVisible();
    await outgoing
      .getByRole("button", { name: "Check current destinations" })
      .click();
    const links = outgoing.getByRole("link", {
      name:
        record.data.listingRevision.displayName +
        " (" +
        record.data.version.version +
        ")",
      exact: true,
    });
    await expect(links).toHaveCount(2);
    await links.first().click();
    await expect(page).toHaveURL(
      catalog.origin + catalog.basePath + "/catalog/" + target.releaseId,
    );
    const incoming = page.getByRole("region", {
      name: "Listings that refer to this resource",
    });
    await incoming
      .getByRole("button", { name: "Find referring Listings" })
      .click();
    const sources = incoming.getByRole("link", {
      name: /Reader relationship fixture/,
    });
    await expect(sources).toHaveCount(2);
    await expect(sources.first()).toHaveAttribute(
      "href",
      catalog.basePath + "/catalog/" + sourceID,
    );

    await signOut(page);
    await login(
      page,
      catalog,
      "local-publisher",
      "/releases/" + target.releaseId,
    );
    await page
      .getByRole("combobox", { name: "Disposition", exact: true })
      .selectOption("retire");
    const impact = page.getByRole("region", {
      name: "Declared dependency impact",
    });
    await impact
      .getByRole("button", { name: "Check declared dependencies" })
      .click();
    await expect(
      impact.getByRole("link", { name: /Reader relationship fixture/ }),
    ).toHaveCount(2);
    await expect(
      impact.getByText("(exact version)", { exact: false }),
    ).toBeVisible();
    await expect(
      impact.getByText("(unversioned)", { exact: false }),
    ).toBeVisible();
    await page.setViewportSize({ width: 360, height: 800 });
    await impact.scrollIntoViewIfNeeded();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth + 1,
      ),
    ).toBe(true);
  });
}
