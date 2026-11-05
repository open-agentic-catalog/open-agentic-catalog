import { test, expect, login, signOut } from "./fixtures";
import { object, text } from "./json";
import { required } from "../required";
import type { Catalog } from "./fixtures";
import AxeBuilder from "@axe-core/playwright";

for (const field of ["id", "tenantId"] as const) {
  test(`exact submission read refuses a mismatched ${field} before loading Listing context`, async ({
    page,
    catalog,
  }) => {
    const item = required(
      catalog.receipt.items.find((row) => row.key === "pending-mcp"),
    );
    const id = required(item.submissionId);
    const path = `/_catalog/api/v1/review-submissions/${id}`;
    const reads: string[] = [];
    const writes: string[] = [];
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (request.method() === "GET") reads.push(path);
      if (request.method() === "POST" && path.startsWith("/_catalog/"))
        writes.push(path);
    });
    await page.route(
      `**${path}`,
      async (route) => {
        const response = await route.fetch();
        expect(response.status()).toBe(200);
        await route.fulfill({
          response,
          json: {
            ...object(await response.json()),
            [field]: "00000000-0000-7000-8000-000000000099",
          },
        });
      },
      { times: 1 },
    );
    await login(page, catalog, "local-template-reviewer", `/reviews/${id}`);
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(
      page.getByRole("button", {
        name: "Inspect submitted evidence",
        exact: true,
      }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("button", {
        name: "Publish approved listing",
        exact: true,
      }),
    ).toHaveCount(0);
    expect(reads.filter((path) => path.includes("/catalog-listings/"))).toEqual(
      [],
    );
    expect(reads.filter((read) => read === path)).toHaveLength(1);
    await page
      .getByRole("button", {
        name: "Reload review and next steps",
        exact: true,
      })
      .click();
    await expect(
      page.getByRole("button", {
        name: "Inspect submitted evidence",
        exact: true,
      }),
    ).toBeVisible();
    expect(reads.filter((read) => read === path)).toHaveLength(2);
    expect(reads.filter((path) => path.includes("/catalog-listings/"))).toEqual(
      [`/_catalog/api/v1/catalog-listings/${item.listingId}`],
    );
    expect(writes).toEqual([]);
    const retained = await catalog.admin(
      "GET",
      `/api/v1/review-submissions/${id}`,
    );
    expect(retained.data).toMatchObject({ id, state: "submitted" });
  });
}

async function grantHistoryOnly(catalog: Catalog, publish = false) {
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
      name: "Exact review history delegate",
      permissions: [
        "resource.read_history",
        "resource.read_content",
        ...(publish ? ["release.publish"] : ["review.decide"]),
      ],
      reason: "Owned history-only browser prerequisite",
    },
  );
  await catalog.admin("POST", "/api/v1/access/assignments", {
    membershipId: member.id,
    roleDefinitionId: role.data.id,
    reason: "Owned exact permission prerequisite",
  });
}

test("history-only publisher uses the concurrency hint without Listing metadata reads", async ({
  page,
  catalog,
}) => {
  const item = required(
    catalog.receipt.items.find((row) => row.key === "pending-mcp"),
  );
  const id = required(item.submissionId);
  const submission = await catalog.admin(
    "GET",
    `/api/v1/review-submissions/${id}`,
  );
  await catalog.admin(
    "POST",
    `/api/v1/review-submissions/${id}:approve`,
    { reason: "Independent minimal publication prerequisite" },
    submission.etag,
    "local-reviewer",
  );
  await grantHistoryOnly(catalog, true);
  const reads: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "GET") reads.push(new URL(request.url()).pathname);
  });
  await login(page, catalog, "local-no-role", `/reviews/${id}`);
  await expect(
    page.getByText("Uninspected: current Listing metadata was not read", {
      exact: true,
    }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Publish approved listing", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  const outcome = page.getByRole("region", {
    name: "Publication command outcome",
    exact: true,
  });
  await expect(outcome).toContainText(
    "No prior release was changed by this command.",
  );
  await outcome
    .getByRole("button", { name: "Read current release history", exact: true })
    .click();
  await expect(page.locator(".release-history-entry")).toHaveCount(1);
  expect(
    reads.filter((path) =>
      /\/catalog-listings\/[0-9a-f-]+$|\/(listing-revisions|version-revisions)\//.test(
        path,
      ),
    ),
  ).toEqual([]);
  expect(
    reads.filter((path) =>
      path.endsWith(`/catalog-listings/${item.listingId}/releases`),
    ),
  ).toHaveLength(1);
});

for (const location of ["root", "subpath"]) {
  test(`${location}: minimal publication recovers a historical receipt after the committed release is retired`, async ({
    page,
    catalog,
  }) => {
    const item = required(
      catalog.receipt.items.find((row) => row.key === "order-agent"),
    );
    const oldReleaseId = required(item.releaseId);
    const listing = await catalog.admin(
      "GET",
      `/api/v1/catalog-listings/${item.listingId}`,
    );
    const revision = await catalog.admin(
      "POST",
      `/api/v1/catalog-listings/${item.listingId}/revisions`,
      {
        versionRevisionId: item.versionRevisionId,
        publisherId: catalog.receipt.publisherId,
        displayName: "Historical publication receipt control",
        description: "Owned supersession and retirement prerequisite",
        visibility: "public",
        representativeQueries: ["Find order agents", "Find history examples"],
        license: "Apache-2.0",
      },
      listing.etag,
    );
    const submitted = await catalog.admin(
      "POST",
      `/api/v1/catalog-listings/${item.listingId}/submissions`,
      {},
    );
    const submissionId = text(object(submitted.data).id);
    await catalog.admin(
      "POST",
      `/api/v1/review-submissions/${submissionId}:approve`,
      { reason: "Independent historical-receipt fixture review" },
      submitted.etag,
      "local-reviewer",
    );
    await grantHistoryOnly(catalog, true);
    const reads: string[] = [];
    const writes: {
      method: string;
      path: string;
      body: string | null;
      key: string;
      etag: string;
    }[] = [];
    let committedReleaseId: string | undefined;
    page.on("request", (request) => {
      if (request.method() === "GET")
        reads.push(new URL(request.url()).pathname);
    });
    const publishPath = `/_catalog/api/v1/catalog-listings/${item.listingId}:publish`;
    await page.route(`**${publishPath}`, async (route) => {
      const request = route.request();
      writes.push({
        method: request.method(),
        path: new URL(request.url()).pathname,
        body: request.postData(),
        key: required(request.headers()["idempotency-key"]),
        etag: required(request.headers()["if-match"]),
      });
      const response = await route.fetch();
      expect(response.status()).toBe(201);
      const value = object(await response.json());
      if (writes.length === 1) {
        committedReleaseId = text(object(value.release).id);
        // The publication really commits. Only its browser receipt is damaged.
        delete value.priorRelease;
        await route.fulfill({ response, json: value });
      } else {
        expect(response.headers()["idempotency-replayed"]).toBe("true");
        await route.fulfill({ response });
      }
    });
    await login(page, catalog, "local-no-role", `/reviews/${submissionId}`);
    await expect(
      page.getByText("Uninspected: current Listing metadata was not read", {
        exact: true,
      }),
    ).toBeVisible();
    await page
      .getByRole("combobox", {
        name: "Previous release disposition",
        exact: true,
      })
      .selectOption("superseded");
    await page
      .getByRole("textbox", { name: "Reason", exact: true })
      .fill("Replace the previous release before retirement");
    await page
      .getByRole("button", { name: "Publish approved listing", exact: true })
      .click();
    const summary = page.getByLabel("Command summary");
    await expect(summary).toContainText("Uninspected");
    await expect(summary).not.toContainText(oldReleaseId);
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    await expect(page.getByText(/The outcome is unknown/)).toBeVisible();
    await expect(
      page.getByRole("region", { name: "Publication command outcome" }),
    ).toHaveCount(0);
    const releaseId = required(committedReleaseId);
    const published = await catalog.admin(
      "GET",
      `/api/v1/listing-releases/${releaseId}`,
    );
    expect(published.data).toMatchObject({ state: "published", submissionId });
    await catalog.admin(
      "POST",
      `/api/v1/listing-releases/${releaseId}:retire`,
      { reason: "Independent retirement before browser replay" },
      published.etag,
    );
    const terminalListing = await catalog.admin(
      "GET",
      `/api/v1/catalog-listings/${item.listingId}`,
    );
    expect(terminalListing.data.currentReleaseId).toBeUndefined();
    await page
      .getByRole("button", { name: "Retry same command", exact: true })
      .click();
    const outcome = page.getByRole("region", {
      name: "Publication command outcome",
      exact: true,
    });
    await expect(outcome).toContainText(releaseId);
    await expect(outcome).toContainText(oldReleaseId);
    await expect(outcome).toContainText("Returned state: published");
    await expect(outcome).toContainText("Returned state: superseded");
    await expect(outcome).toContainText("historical replay");
    expect(writes).toHaveLength(2);
    expect(writes[1]).toEqual(writes[0]);
    expect(writes[0]).toMatchObject({
      method: "POST",
      path: `${catalog.basePath}${publishPath}`,
      etag: revision.etag,
    });
    expect(writes[0]?.key).not.toBe("");
    expect(JSON.parse(text(writes[0]?.body))).toEqual({
      submissionId,
      priorDisposition: "superseded",
      priorReason: "Replace the previous release before retirement",
    });
    expect(
      reads.filter((path) =>
        path.endsWith(`/catalog-listings/${item.listingId}/releases`),
      ),
    ).toEqual([]);
    await outcome
      .getByRole("button", {
        name: "Read current release history",
        exact: true,
      })
      .click();
    const entries = page.locator(".release-history-entry");
    await expect(entries).toHaveCount(2);
    await expect(
      entries.getByRole("link", { name: /^retired -/ }),
    ).toHaveAttribute("href", `${catalog.basePath}/releases/${releaseId}`);
    await expect(
      entries.getByRole("link", { name: /^superseded -/ }),
    ).toHaveAttribute("href", `${catalog.basePath}/releases/${oldReleaseId}`);
    await expect(
      page.getByText("Current listing release", { exact: true }),
    ).toHaveCount(0);
    await expect(outcome).toContainText("Returned state: published");
    await expect(
      page.getByRole("button", {
        name: "Publish approved listing",
        exact: true,
      }),
    ).toBeDisabled();
    const finalListing = await catalog.admin(
      "GET",
      `/api/v1/catalog-listings/${item.listingId}`,
    );
    expect(finalListing.data).toEqual(terminalListing.data);
    expect(finalListing.etag).toEqual(terminalListing.etag);
    const oldRelease = await catalog.admin(
      "GET",
      `/api/v1/listing-releases/${oldReleaseId}`,
    );
    expect(oldRelease.data).toMatchObject({
      state: "superseded",
      replacementReleaseId: releaseId,
    });
    const terminalRelease = await catalog.admin(
      "GET",
      `/api/v1/listing-releases/${releaseId}`,
    );
    expect(terminalRelease.data).toMatchObject({ state: "retired" });
    expect(
      reads.filter((path) =>
        /\/catalog-listings\/[0-9a-f-]+$|\/(listing-revisions|version-revisions)\//.test(
          path,
        ),
      ),
    ).toEqual([]);
    expect(
      reads.filter((path) =>
        path.endsWith(`/catalog-listings/${item.listingId}/releases`),
      ),
    ).toHaveLength(1);
  });

  test(`${location}: exact review filters and opener preserve history-only access and safe unknown identities`, async ({
    page,
    catalog,
    network,
  }) => {
    const item = required(
      catalog.receipt.items.find((row) => row.key === "pending-mcp"),
    );
    const submissionId = required(item.submissionId);
    const reads: string[] = [];
    page.on("request", (request) => {
      if (request.method() === "GET")
        reads.push(new URL(request.url()).pathname);
    });
    await grantHistoryOnly(catalog);
    network.expectFailure("GET", "/_catalog/api/v1/review-submissions", 400);
    await login(page, catalog, "local-no-role", "/reviews?state=__proto__");
    await expect(page.getByRole("alert")).toBeVisible();
    await page
      .getByRole("combobox", { name: "Review state", exact: true })
      .selectOption("submitted");
    await page
      .getByRole("button", { name: "Apply review filters", exact: true })
      .click();
    const rows = page.locator(".workspace-table tbody tr");
    await expect(
      rows.getByRole("link", { name: submissionId, exact: true }),
    ).toBeVisible();
    const filter = page.getByRole("textbox", {
      name: "Exact Listing ID",
      exact: true,
    });
    await filter.fill(item.listingId);
    await page
      .getByRole("button", { name: "Apply review filters", exact: true })
      .click();
    await expect(rows).toHaveCount(1);
    await expect(page).toHaveURL(
      `${catalog.origin}${catalog.basePath}/reviews?state=submitted&listing=${item.listingId}`,
    );
    await expect(
      page.getByRole("link", { name: "Back to listing", exact: true }),
    ).toHaveCount(0);
    expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
      violations: [],
      incomplete: [],
    });
    await filter.fill("00000000-0000-7000-8000-000000000099");
    network.expectFailure("GET", `/_catalog/api/v1/review-submissions`, 404);
    await page
      .getByRole("button", { name: "Apply review filters", exact: true })
      .click();
    await expect(page.getByRole("alert")).toBeVisible();
    await page.goBack();
    await expect(filter).toHaveValue(item.listingId);
    await expect(rows).toHaveCount(1);
    const emptyListing = required(
      catalog.receipt.items.find((row) => row.key === "draft-agent"),
    );
    await filter.fill(emptyListing.listingId);
    await page
      .getByRole("button", { name: "Apply review filters", exact: true })
      .click();
    await expect(
      page.getByText("No matching review submissions.", { exact: true }),
    ).toBeVisible();
    await page.goBack();
    await expect(filter).toHaveValue(item.listingId);
    await expect(rows).toHaveCount(1);
    expect(
      reads.filter((path) =>
        /\/(catalog-listings|listing-revisions|version-revisions)\//.test(path),
      ),
    ).toEqual([]);
    const opener = page.getByRole("textbox", {
      name: "Exact Submission ID",
      exact: true,
    });
    await opener.fill(submissionId.toUpperCase());
    await opener.press("Enter");
    await expect(page).toHaveURL(
      new RegExp(`/reviews/${submissionId.toUpperCase()}\\??$`),
    );
    await expect(
      page.getByRole("button", {
        name: "Inspect submitted evidence",
        exact: true,
      }),
    ).toBeVisible();
    expect(
      reads.filter((path) =>
        /\/(catalog-listings|listing-revisions|version-revisions)\//.test(path),
      ),
    ).toEqual([]);
    const unknown = "00000000-0000-7000-8000-000000000098";
    for (const id of [unknown, submissionId]) {
      if (id === submissionId) {
        await signOut(page);
        network.expectFailure(
          "GET",
          `/_catalog/api/v1/review-submissions`,
          404,
        );
        await login(
          page,
          catalog,
          "other-tenant-admin",
          `/reviews?listing=${item.listingId}`,
        );
        await expect(page.getByRole("alert")).toBeVisible();
      } else await page.goto(`${catalog.origin}${catalog.basePath}/reviews`);
      network.expectFailure(
        "GET",
        `/_catalog/api/v1/review-submissions/${id}`,
        404,
      );
      await page
        .getByRole("textbox", { name: "Exact Submission ID", exact: true })
        .fill(id);
      await page
        .getByRole("button", { name: "Open submission", exact: true })
        .click();
      await expect(page.getByRole("alert")).toBeVisible();
      await expect(
        page.getByRole("button", {
          name: "Inspect submitted evidence",
          exact: true,
        }),
      ).toHaveCount(0);
    }
  });

  test(`${location}: no current release after retirement retains history and publishes without replacement controls`, async ({
    page,
    catalog,
  }) => {
    const item = required(
      catalog.receipt.items.find((row) => row.key === "order-agent"),
    );
    const releaseId = required(item.releaseId);
    const release = await catalog.admin(
      "GET",
      `/api/v1/listing-releases/${releaseId}`,
    );
    await catalog.admin(
      "POST",
      `/api/v1/listing-releases/${releaseId}:retire`,
      { reason: "Owned terminal-history prerequisite" },
      release.etag,
    );
    const listing = await catalog.admin(
      "GET",
      `/api/v1/catalog-listings/${item.listingId}`,
    );
    expect(object(listing.data).currentReleaseId).toBeUndefined();
    const revision = await catalog.admin(
      "POST",
      `/api/v1/catalog-listings/${item.listingId}/revisions`,
      {
        versionRevisionId: item.versionRevisionId,
        publisherId: catalog.receipt.publisherId,
        displayName: "Publication after retirement",
        description: "Owned no-current-pointer publication prerequisite",
        visibility: "public",
        representativeQueries: ["Find order agents", "Find history examples"],
        license: "Apache-2.0",
      },
      listing.etag,
    );
    const submission = await catalog.admin(
      "POST",
      `/api/v1/catalog-listings/${item.listingId}/submissions`,
      {},
    );
    const id = text(object(submission.data).id);
    await catalog.admin(
      "POST",
      `/api/v1/review-submissions/${id}:approve`,
      { reason: "Independent terminal-history fixture review" },
      submission.etag,
      "local-reviewer",
    );
    await login(page, catalog, "local-publisher", `/reviews/${id}`);
    await expect(
      page.getByText("No current release at the loaded Listing revision", {
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      page.getByText(/does not mean this Listing was never published/),
    ).toBeVisible();
    await expect(
      page.getByRole("combobox", {
        name: "Previous release disposition",
        exact: true,
      }),
    ).toHaveCount(0);
    await page
      .getByRole("button", { name: "Publish approved listing", exact: true })
      .click();
    await expect(page.getByLabel("Command summary")).toContainText(
      text(revision.etag),
    );
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    const outcome = page.getByRole("region", {
      name: "Publication command outcome",
      exact: true,
    });
    await expect(outcome).toContainText(
      "No prior release was changed by this command.",
    );
    await outcome
      .getByRole("button", {
        name: "Read current release history",
        exact: true,
      })
      .click();
    await expect(page.locator(".release-history-entry")).toHaveCount(2);
    await expect(
      page
        .locator(".release-history-entry")
        .getByRole("link", { name: /^retired -/ }),
    ).toHaveAttribute("href", `${catalog.basePath}/releases/${releaseId}`);
    const retained = await catalog.admin(
      "GET",
      `/api/v1/listing-releases/${releaseId}`,
    );
    expect(retained.data).toMatchObject({ state: "retired" });
  });
}
