import { object } from "./json";
import { test, expect, confirmCommand, login, signOut } from "./fixtures";
import { required } from "../required";

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

test("subpath: a stale review tab cannot reject an approved submission", async ({
  page,
  context,
  catalog,
  network,
  sourceCoverage,
}) => {
  const id = required(
    catalog.receipt.items.find((entry) => entry.key === "pending-mcp")
      ?.submissionId,
  );
  const apiPath = `/api/v1/review-submissions/${id}`;
  const original = await catalog.admin<{ revision: number }>("GET", apiPath);
  expect(original.etag).toBeTruthy();
  const approval = "The submitted tool declarations match the evidence.";
  const rejection = "Please clarify the description before release.";
  const commandPath = catalog.basePath + "/_catalog" + apiPath;
  const decisions: [string, string | undefined][] = [];
  context.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (request.method() === "POST" && path.startsWith(commandPath + ":"))
      decisions.push([path, request.headers()["if-match"]]);
  });
  await login(page, catalog, "local-reviewer", `/reviews/${id}`);
  const stale = await context.newPage();
  await sourceCoverage.ready(stale);
  await stale.goto(page.url());

  const decisionButton = { name: "Record review decision", exact: true };
  for (const tab of [stale, page]) {
    await tab
      .getByRole("button", { name: "Inspect submitted evidence", exact: true })
      .click();
    await expect(tab.getByRole("button", decisionButton)).toBeEnabled();
  }
  const reason = stale.getByRole("textbox", { name: "Reason", exact: true });
  await stale
    .getByRole("combobox", { name: "Decision", exact: true })
    .selectOption("reject");
  await reason.fill(rejection);
  await stale.getByRole("button", decisionButton).click();
  const dialog = stale.getByRole("dialog");
  await expect(dialog).toContainText("Reject review");

  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill(approval);
  await page.getByRole("button", decisionButton).click();
  await confirmCommand(page);
  await expect(page.getByRole("status")).toContainText("Review approved.");

  network.expectFailure("POST", "/_catalog" + apiPath + ":reject", 412);
  const [refusal] = await Promise.all([
    stale.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        new URL(response.url()).pathname === commandPath + ":reject",
    ),
    dialog.getByRole("button", { name: "Confirm", exact: true }).click(),
  ]);
  expect(refusal.status()).toBe(412);
  expect(object(await refusal.json()).code).toBe("PRECONDITION_FAILED");
  await expect(dialog.getByRole("alert")).toContainText(
    "The record changed. Your input is retained.",
  );
  await expect(
    dialog.getByRole("button", { name: "Retry same command" }),
  ).toHaveCount(0);
  await dialog
    .getByRole("button", { name: "Back to form", exact: true })
    .click();
  await expect(reason).toHaveValue(rejection);

  await Promise.all([
    stale.waitForEvent("dialog").then(async (prompt) => {
      await prompt.accept();
      expect(prompt.type()).toBe("beforeunload");
    }),
    stale.evaluate(() => window.location.reload()),
  ]);
  await expect(
    stale.getByRole("heading", { name: "Review approved", exact: true }),
  ).toBeVisible();
  const history = stale
    .getByRole("region", { name: "Reviewer decisions" })
    .getByRole("listitem");
  await expect(history).toHaveCount(1);
  await expect(history).toContainText(approval);
  await stale
    .getByRole("button", { name: "Inspect submitted evidence", exact: true })
    .click();
  await expect(
    stale.getByText(
      "Only a pending submission accepts a new review decision.",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(stale.getByRole("button", decisionButton)).toHaveCount(0);

  const saved = await catalog.admin("GET", apiPath);
  expect(saved.data).toMatchObject({
    id,
    state: "approved",
    revision: original.data.revision + 1,
    decisions: [
      { action: "approve", actor: "local-reviewer", reason: approval },
    ],
  });
  expect(decisions).toEqual([
    [commandPath + ":approve", original.etag],
    [commandPath + ":reject", original.etag],
  ]);
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
