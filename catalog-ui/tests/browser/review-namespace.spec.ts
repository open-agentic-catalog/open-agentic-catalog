// Real queue reads qualify namespace filtering independently of loaded-page sorting.
import { test, expect, login } from "./fixtures";
import type { Catalog } from "./fixtures";
import { agentDocument } from "./published-fixture";
import { object, text } from "./json";
import { required } from "../required";
import AxeBuilder from "@axe-core/playwright";
import { doubleText } from "./text-layout";

/** Build owned approved-but-unreleased prerequisites with independent review. */
async function submissions(catalog: Catalog, slug: string, count: number) {
  const namespace = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/namespaces",
    { slug, displayName: slug },
  );
  const resource = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/resources",
    {
      namespaceId: namespace.data.id,
      kind: "agent",
      canonicalIdentifier: `urn:air:demo.catalog.example.com:${slug}:review-fixture`,
    },
  );
  const version = await catalog.admin(
    "POST",
    `/api/v1/resources/${resource.data.id}/versions`,
    {
      version: "1.0.0",
      representations: [
        {
          role: "primary",
          mediaType: "application/a2a-agent-card+json",
          data: agentDocument,
        },
      ],
    },
  );
  const revisionId = text(object(object(version.data).revision).id);
  const listing = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/catalog-listings",
    { namespaceId: namespace.data.id, resourceId: resource.data.id },
  );
  let etag = listing.etag;
  const ids: string[] = [];
  for (let index = 0; index < count; index++) {
    const revision = await catalog.admin(
      "POST",
      `/api/v1/catalog-listings/${listing.data.id}/revisions`,
      {
        versionRevisionId: revisionId,
        publisherId: catalog.receipt.publisherId,
        displayName: `${slug} submission ${index}`,
        description: "Owned namespace review fixture",
        visibility: "tenant",
      },
      etag,
    );
    etag = revision.etag;
    const submission = await catalog.admin<{ id: string }>(
      "POST",
      `/api/v1/catalog-listings/${listing.data.id}/submissions`,
      {},
    );
    await catalog.admin(
      "POST",
      `/api/v1/review-submissions/${submission.data.id}:approve`,
      { reason: "Independent namespace paging fixture review" },
      submission.etag,
      "local-reviewer",
    );
    ids.push(submission.data.id);
  }
  return { namespaceId: namespace.data.id, listingId: listing.data.id, ids };
}

for (const location of ["root", "subpath"]) {
  test(`${location}: namespace review filters reach sparse later submissions and retain criteria through paging and recovery`, async ({
    page,
    catalog,
  }, testInfo) => {
    await submissions(catalog, "review-noise", 25);
    const target = await submissions(catalog, "review-target", 26);
    const first = await catalog.admin<{ items: { id: string }[] }>(
      "GET",
      "/api/v1/review-submissions?state=approved&unreleased=true&limit=25",
    );
    expect(first.data.items.every((row) => !target.ids.includes(row.id))).toBe(
      true,
    );
    const reads: string[] = [];
    const writes: string[] = [];
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (url.pathname.endsWith("/api/v1/review-submissions"))
        reads.push(request.url());
      if (
        url.pathname.includes("/_catalog/api/") &&
        request.method() === "POST"
      )
        writes.push(request.url());
    });
    await login(
      page,
      catalog,
      "local-reviewer",
      "/reviews?state=approved&unreleased=true",
    );
    const region = page.getByRole("region", {
      name: "Review submissions",
      exact: true,
    });
    await expect(region.locator("tbody tr")).toHaveCount(25);
    const before = reads.length;
    await page
      .getByRole("textbox", { name: "Exact namespace ID", exact: true })
      .fill(target.namespaceId);
    expect(reads).toHaveLength(before);
    await page
      .getByRole("button", { name: "Apply review filters", exact: true })
      .click();
    const links = region.locator("tbody a[href*='/reviews/']");
    await expect(links).toHaveCount(25);
    await expect(links.first()).toHaveAttribute(
      "href",
      `${catalog.basePath}/reviews/${target.ids[0]}`,
    );
    expect(new URL(reads.at(-1)!).searchParams.get("namespaceId")).toBe(
      target.namespaceId,
    );
    await page.getByRole("button", { name: "Next page", exact: true }).click();
    await expect(links).toHaveCount(1);
    await expect(links).toHaveAttribute(
      "href",
      `${catalog.basePath}/reviews/${target.ids[25]}`,
    );
    const continued = new URL(reads.at(-1)!);
    expect(continued.searchParams.get("namespaceId")).toBe(target.namespaceId);
    expect(continued.searchParams.get("cursor")).toBeTruthy();
    await page
      .getByRole("textbox", { name: "Exact namespace ID", exact: true })
      .fill(catalog.receipt.namespaceId);
    const refreshed = page.waitForResponse(
      (r) =>
        r.url().includes("/api/v1/review-submissions?") &&
        r.request().method() === "GET",
    );
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    expect((await refreshed).status()).toBe(200);
    expect(new URL(reads.at(-1)!).search).toBe(continued.search);
    await expect(links).toHaveCount(1);
    await page.getByRole("button", { name: "First page", exact: true }).click();
    await expect(links).toHaveCount(25);
    const query = new URL(reads.at(-1)!).searchParams;
    expect(query.get("namespaceId")).toBe(target.namespaceId);
    expect(query.has("cursor")).toBe(false);
    await page
      .getByRole("textbox", { name: "Exact namespace ID", exact: true })
      .fill(catalog.receipt.namespaceId);
    await page
      .getByRole("button", { name: "Apply review filters", exact: true })
      .click();
    await expect(page).toHaveURL(
      new RegExp(`namespace=${catalog.receipt.namespaceId}$`),
    );
    await page.goBack();
    await expect(
      page.getByRole("textbox", { name: "Exact namespace ID", exact: true }),
    ).toHaveValue(target.namespaceId);
    await expect(links).toHaveCount(25);
    const unknownNamespace = "00000000-0000-7000-8000-000000000099";
    await page
      .getByRole("textbox", { name: "Exact namespace ID", exact: true })
      .fill(unknownNamespace);
    const emptyPage = page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname.endsWith(
          "/api/v1/review-submissions",
        ) &&
        new URL(response.url()).searchParams.get("namespaceId") ===
          unknownNamespace,
    );
    await page
      .getByRole("button", { name: "Apply review filters", exact: true })
      .click();
    expect((await emptyPage).status()).toBe(200);
    await expect(
      page.getByText("No matching review submissions.", { exact: true }),
    ).toBeVisible();
    await expect(links).toHaveCount(0);
    await expect(page.getByRole("alert")).toHaveCount(0);
    await expect(page).toHaveURL(new RegExp(`namespace=${unknownNamespace}$`));
    await page.goBack();
    await expect(
      page.getByRole("textbox", { name: "Exact namespace ID", exact: true }),
    ).toHaveValue(target.namespaceId);
    await expect(links).toHaveCount(25);
    await page.reload();
    await expect(links).toHaveCount(25);
    await page.setViewportSize({ width: 320, height: 740 });
    await expect(
      page.getByRole("button", { name: "Apply review filters", exact: true }),
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
    await page
      .getByRole("form", { name: "Review queue filters", exact: true })
      .screenshot({
        path: testInfo.outputPath("namespace-filters-mobile.png"),
      });
    await page.emulateMedia({ colorScheme: "dark" });
    await doubleText(page);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page
      .getByRole("form", { name: "Review queue filters", exact: true })
      .screenshot({
        path: testInfo.outputPath("namespace-filters-dark-large-text.png"),
      });
    await page
      .getByRole("button", { name: "Clear review filters", exact: true })
      .click();
    await expect(page).toHaveURL(
      `${catalog.origin}${catalog.basePath}/reviews?state=`,
    );
    await expect(region.locator("tbody tr")).toHaveCount(25);
    expect(new URL(reads.at(-1)!).searchParams.has("namespaceId")).toBe(false);
    expect(writes).toEqual([]);
    // The existing exact-key form remains usable after a filtered page.
    await page
      .getByRole("textbox", { name: "Exact Submission ID", exact: true })
      .fill(required(target.ids[25]));
    await page
      .getByRole("button", { name: "Open submission", exact: true })
      .click();
    await expect(page).toHaveURL(new RegExp(`/reviews/${target.ids[25]}\\??$`));
  });
}

test("namespace review continuation refuses revoked history authority and clears stale private rows", async ({
  page,
  catalog,
  network,
}) => {
  const target = await submissions(catalog, "review-delegated", 26);
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
      name: "Review namespace history only",
      permissions: ["resource.read_history"],
      reason: "Owned review namespace permission fixture",
    },
  );
  const assignment = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/access/assignments",
    {
      membershipId: member.id,
      roleDefinitionId: role.data.id,
      namespaceId: target.namespaceId,
      reason: "Read one namespace without metadata or content permissions",
    },
  );
  await login(
    page,
    catalog,
    "local-no-role",
    `/reviews?state=approved&unreleased=true&namespace=${target.namespaceId}`,
  );
  const region = page.getByRole("region", {
    name: "Review submissions",
    exact: true,
  });
  await expect(region.locator("tbody tr")).toHaveCount(25);
  await expect(
    region.getByText("Name unavailable", { exact: true }),
  ).toHaveCount(25);
  await expect(
    region.getByText("Identifier unavailable", { exact: true }),
  ).toHaveCount(25);
  await catalog.admin(
    "POST",
    `/api/v1/access/assignments/${assignment.data.id}:revoke`,
    { reason: "Withdraw owned history access between pages" },
    assignment.etag,
  );
  network.expectFailure("GET", "/_catalog/api/v1/review-submissions", 400);
  await page.getByRole("button", { name: "Next page", exact: true }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(region).toHaveCount(0);
  await expect(
    page.getByRole("textbox", { name: "Exact namespace ID", exact: true }),
  ).toHaveValue(target.namespaceId);
  await page.getByRole("button", { name: "First page", exact: true }).click();
  await expect(
    page.getByText("No matching review submissions.", { exact: true }),
  ).toBeVisible();
  await expect(region.locator("tbody tr")).toHaveCount(0);
  await expect(page).toHaveURL(new RegExp(`namespace=${target.namespaceId}$`));
});
