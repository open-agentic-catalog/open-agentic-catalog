import { object } from "./json";
import { test, expect, login, signOut } from "./fixtures";

test("Catalog identity admission rejects genuine self-review and permits an independent reviewer", async ({
  page,
  catalog,
  network,
}) => {
  const item = catalog.receipt.items[0]!;
  const current = await catalog.admin(
    "GET",
    `/api/v1/catalog-listings/${item.listingId}`,
  );
  const revision = await catalog.admin<{ revision: { id: string } }>(
    "POST",
    `/api/v1/catalog-listings/${item.listingId}/revisions`,
    {
      versionRevisionId: item.versionRevisionId,
      displayName: "Independent identity control",
      description: "Review exact retained content with a new listing revision.",
      publisherId: catalog.receipt.publisherId,
      visibility: "tenant",
    },
    current.etag,
  );
  const submitted = await catalog.admin<{ id: string; revision: number }>(
    "POST",
    `/api/v1/catalog-listings/${item.listingId}/submissions`,
    {},
  );
  const route = `/reviews/${submitted.data.id}`;
  await login(page, catalog, "local-admin", route);
  await page
    .getByRole("button", { name: "Inspect submitted evidence", exact: true })
    .click();
  const decide = page.getByRole("button", {
    name: "Record review decision",
    exact: true,
  });
  await expect(decide).toBeEnabled();
  await expect(
    page.getByText(/The Catalog verifies independence/),
  ).toBeVisible();
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Verify genuine self-review refusal");
  network.expectFailure(
    "POST",
    `/_catalog/api/v1/review-submissions/${submitted.data.id}:approve`,
    409,
  );
  await decide.click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  await expect(page.getByText(/SEPARATION_OF_DUTIES/).first()).toBeVisible();
  const retained = await catalog.admin<{
    state: string;
    revision: number;
    decisions: unknown[];
  }>("GET", `/api/v1/review-submissions/${submitted.data.id}`);
  expect(retained.data.state).toBe("submitted");
  expect(retained.data.revision).toBe(submitted.data.revision);
  expect(retained.data.decisions ?? []).toEqual([]);
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Back to form", exact: true })
    .click();
  const inspect = page.getByRole("button", {
    name: "Inspect submitted evidence",
    exact: true,
  });
  if ((await inspect.getAttribute("aria-expanded")) !== "true")
    await inspect.click();
  await page.getByRole("textbox", { name: "Reason", exact: true }).fill("");
  await signOut(page);
  await login(page, catalog, "local-reviewer", route);
  await page
    .getByRole("button", { name: "Inspect submitted evidence", exact: true })
    .click();
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Independently inspect the exact submission");
  await decide.click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  await expect(page.getByText(/Review approved\./)).toBeVisible();
  const approved = await catalog.admin<{
    state: string;
    listingRevisionId: string;
    versionRevisionId: string;
  }>("GET", `/api/v1/review-submissions/${submitted.data.id}`);
  expect(approved.data.state).toBe("approved");
  expect(approved.data.listingRevisionId).toBe(revision.data.revision.id);
  expect(approved.data.versionRevisionId).toBe(item.versionRevisionId);
});

for (const fault of ["evidence", "listing"]) {
  test(`mismatched submitted ${fault} prevents a decision and recovers with the actual evidence`, async ({
    page,
    catalog,
  }) => {
    const item = catalog.receipt.items.find(
      (entry) => entry.key === "pending-mcp",
    )!;
    await login(
      page,
      catalog,
      "local-reviewer",
      `/reviews/${item.submissionId}`,
    );
    const writes: string[] = [];
    page.on("request", (request) => {
      if (request.method() === "POST") writes.push(request.url());
    });
    await page.route(
      fault === "evidence"
        ? "**/review-submissions/*/evidence"
        : "**/listing-revisions/*",
      async (route) => {
        const response = await route.fetch();
        const body = object(await response.json());
        body.id = "00000000-0000-7000-8000-000000000999";
        await route.fulfill({ response, json: body });
      },
      { times: 1 },
    );
    await page
      .getByRole("button", { name: "Inspect submitted evidence", exact: true })
      .click();
    await expect(page.getByRole("alert")).toContainText(
      "This page could not be loaded",
    );
    await expect(
      page.getByRole("button", { name: "Record review decision", exact: true }),
    ).toBeDisabled();
    expect(writes).toEqual([]);
    await page.getByRole("button", { name: "Reload", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "Record review decision", exact: true }),
    ).toBeEnabled();
    await expect(page.getByRole("alert")).toHaveCount(0);
    expect(writes).toEqual([]);
    const review = await catalog.admin<{ state: string }>(
      "GET",
      `/api/v1/review-submissions/${item.submissionId}`,
    );
    expect(review.data.state).toBe("submitted");
  });
}
