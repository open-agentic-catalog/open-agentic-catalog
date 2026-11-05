import { test, expect, login, confirmCommand, signOut } from "./fixtures";
import { object, array, text } from "./json";
import { required } from "../required";

test("replacement publication retains extension metadata and navigable superseded release history", async ({
  page,
  catalog,
  network,
}, testInfo) => {
  const item = catalog.receipt.items.find((i) => i.key === "order-agent")!;
  const listingURL = `${catalog.origin}/listings/${item.listingId}`;
  await login(
    page,
    catalog,
    "local-contributor",
    `/listings/${item.listingId}`,
  );
  await page
    .getByRole("button", { name: "New listing revision", exact: true })
    .click();
  await page
    .getByRole("textbox", { name: "Display name", exact: true })
    .fill("Replacement order agent");
  await page
    .getByText("Links, license and relationships", { exact: true })
    .click();
  await page
    .getByRole("textbox", { name: "Documentation URL", exact: true })
    .fill("https://docs.example.com/catalog-guide");
  await page
    .getByRole("textbox", { name: "Extensions (JSON object)", exact: true })
    .fill('{"browser":{"revision":2,"text":"<svg data-ui-injection></svg>"}}');
  await page
    .getByRole("button", { name: "Create listing revision", exact: true })
    .click();
  await confirmCommand(page);
  await page.reload();
  await page
    .getByRole("button", { name: "Submit for review", exact: true })
    .click();
  await confirmCommand(page);
  await page
    .getByRole("link", { name: "Open review submission", exact: true })
    .click();
  const reviewPath = new URL(page.url()).pathname;
  await signOut(page);
  await login(page, catalog, "local-reviewer", reviewPath);
  await page
    .getByRole("button", { name: "Inspect submitted evidence", exact: true })
    .click();
  await expect(page.locator("[data-ui-injection]")).toHaveCount(0);
  await page
    .getByRole("combobox", { name: "Decision", exact: true })
    .selectOption("approve");
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Independent replacement review");
  await page
    .getByRole("button", { name: "Record review decision", exact: true })
    .click();
  await confirmCommand(page);
  await signOut(page);
  await login(page, catalog, "local-publisher", reviewPath);
  await expect(
    page.getByRole("region", { name: "Observed publication context" }),
  ).toContainText(required(item.releaseId));
  await expect(
    page.getByText(/Both choices preserve the active discovery snapshot/),
  ).toBeVisible();
  await expect(page.getByText(/Either can withdraw entire/)).toHaveCount(0);
  await page
    .getByRole("combobox", {
      name: "Previous release disposition",
      exact: true,
    })
    .selectOption("superseded");
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Replace the original reviewed metadata");
  await page
    .getByRole("button", { name: "Publish approved listing", exact: true })
    .click();
  await expect(page.getByLabel("Command summary")).toContainText(
    required(item.releaseId),
  );
  const publicationRequests: {
    body: string | null;
    key: string;
    etag: string;
  }[] = [];
  const publishPath = `/_catalog/api/v1/catalog-listings/${item.listingId}:publish`;
  await page.route(`**${publishPath}`, async (route) => {
    const request = route.request();
    publicationRequests.push({
      body: request.postData(),
      key: required(request.headers()["idempotency-key"]),
      etag: required(request.headers()["if-match"]),
    });
    const response = await route.fetch();
    expect(response.status()).toBe(201);
    const body = object(await response.json());
    if (publicationRequests.length === 1) {
      // The real command committed. Only its browser reply loses required context.
      delete body.priorRelease;
      await route.fulfill({ response, json: body });
    } else {
      expect(response.headers()["idempotency-replayed"]).toBe("true");
      await route.fulfill({ response });
    }
  });
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  await expect(page.getByText(/The outcome is unknown/)).toBeVisible();
  await expect(
    page.getByRole("region", { name: "Publication command outcome" }),
  ).toHaveCount(0);
  const committed = await catalog.admin(
    "GET",
    `/api/v1/catalog-listings/${item.listingId}`,
  );
  await page
    .getByRole("button", { name: "Retry same command", exact: true })
    .click();
  const outcome = page.getByRole("region", {
    name: "Publication command outcome",
  });
  await expect(outcome).toContainText(required(item.releaseId));
  await expect(outcome).toContainText("superseded");
  expect(publicationRequests).toHaveLength(2);
  expect(publicationRequests[1]).toEqual(publicationRequests[0]);
  expect(publicationRequests[0]?.key).not.toBe("");
  const afterReplay = await catalog.admin(
    "GET",
    `/api/v1/catalog-listings/${item.listingId}`,
  );
  expect(afterReplay.data).toEqual(committed.data);
  expect(afterReplay.etag).toEqual(committed.etag);
  await outcome
    .getByRole("button", { name: "Read current release history", exact: true })
    .click();
  await expect(page.locator(".release-history-entry")).toHaveCount(2);
  await expect(
    page.getByText("Current listing release", { exact: true }),
  ).toHaveCount(0);
  await page
    .getByRole("link", { name: "Open published release", exact: true })
    .click();
  const replacementPath = new URL(page.url()).pathname;
  await page.getByRole("link", { name: "Catalog record", exact: true }).click();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Replacement order agent", exact: true }),
  ).toBeVisible();
  await expect(page.locator("[data-ui-injection]")).toHaveCount(0);
  const documentation = page.getByRole("main").getByRole("link", {
    name: "Documentation",
    exact: true,
  });
  await expect(documentation).toHaveAttribute(
    "href",
    "https://docs.example.com/catalog-guide",
  );
  await expect(documentation).toHaveAttribute("target", "_blank");
  await expect(documentation).toHaveAttribute("rel", "noopener noreferrer");
  await signOut(page);
  await login(page, catalog, "local-reader", new URL(page.url()).pathname);
  const historyReads: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "GET")
      historyReads.push(new URL(request.url()).pathname);
  });
  await page
    .getByRole("link", { name: "Release history", exact: true })
    .click();
  await expect(page).toHaveURL(listingURL + "#release-history");
  await expect(
    page.getByRole("heading", { name: "Release history", exact: true }),
  ).toBeFocused();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Release history", exact: true }),
  ).toBeFocused();
  await page.evaluate(() => {
    if (document.activeElement instanceof HTMLElement)
      document.activeElement.blur();
    window.scrollTo(0, 0);
  });
  await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(0);
  await page.screenshot({
    path: testInfo.outputPath("release-history.png"),
    fullPage: true,
  });
  expect(
    historyReads.filter((path) =>
      /\/(version-revisions|listing-revisions)\//.test(path),
    ),
  ).toEqual([]);
  await expect(
    page.locator(`a[href="${replacementPath}"]`).last(),
  ).toBeVisible();
  await page.locator(`a[href="/releases/${item.releaseId}"]`).click();
  await expect(
    page.getByRole("heading", { name: "Release superseded", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Catalog record", exact: true }),
  ).toHaveCount(0);
  await expect(
    page
      .getByText("Replace the original reviewed metadata", { exact: true })
      .first(),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Release history", exact: true }),
  ).toHaveAttribute("href", `/listings/${item.listingId}#release-history`);
  await page.evaluate(() => {
    if (document.activeElement instanceof HTMLElement)
      document.activeElement.blur();
    window.scrollTo(0, 0);
  });
  await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(0);
  await page.screenshot({
    path: testInfo.outputPath("release-details.png"),
    fullPage: true,
  });
  const replacementID = replacementPath.split("/").at(-1)!;
  await page.getByRole("link", { name: replacementID, exact: true }).click();
  await expect(page).toHaveURL(catalog.origin + replacementPath);
  await expect(
    page.getByRole("link", { name: "Catalog record", exact: true }),
  ).toBeVisible();
  await expect(
    page.locator(`a[href="/version-revisions/${item.versionRevisionId}"]`),
  ).toBeVisible();
  network.expectFailure(
    "GET",
    `/_catalog/api/v1/catalog/resources/${item.releaseId}`,
    404,
  );
  await page.goto(`${catalog.origin}/catalog/${item.releaseId}`);
  await expect(page.getByRole("alert")).toBeVisible();
  const search = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/api/v1/catalog/search"),
  );
  await page.goto(`${catalog.origin}/catalog`);
  await expect(
    page.getByRole("button", { name: "Replacement order agent", exact: true }),
  ).toBeVisible();
  const hits = array(object(await (await search).json()).items).map(object);
  const discovered = hits.map((hit) =>
    text(object(object(hit.local).release).id),
  );
  expect(discovered).toContain(replacementID);
  expect(discovered).not.toContain(item.releaseId);
  const listing = await catalog.admin<{ candidateRevisionId: string }>(
    "GET",
    `/api/v1/catalog-listings/${item.listingId}`,
  );
  const revision = await catalog.admin<{ extensions: unknown }>(
    "GET",
    `/api/v1/listing-revisions/${listing.data.candidateRevisionId}`,
  );
  expect(revision.data.extensions).toEqual({
    browser: { revision: 2, text: "<svg data-ui-injection></svg>" },
  });
});

test("reader pages real release history under a subpath without hydrating each revision", async ({
  page,
  catalog,
}) => {
  const item = catalog.receipt.items.find((i) => i.key === "order-agent")!;
  const expected = [item.releaseId!];
  for (let index = 0; index < 25; index++) {
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
        displayName: `History order agent ${index}`,
        description: "Owned release history pagination prerequisite",
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
    const submissionId = text(object(submission.data).id);
    await catalog.admin(
      "POST",
      `/api/v1/review-submissions/${submissionId}:approve`,
      { reason: "Independent history fixture review" },
      submission.etag,
      "local-reviewer",
    );
    const published = await catalog.admin(
      "POST",
      `/api/v1/catalog-listings/${item.listingId}:publish`,
      {
        submissionId,
        priorDisposition: "superseded",
        priorReason: "Replacement history fixture",
      },
      revision.etag,
    );
    expected.push(text(object(object(published.data).release).id));
  }
  const requests: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "GET")
      requests.push(new URL(request.url()).pathname);
  });
  const nextHistoryReply = () =>
    page.waitForResponse((response) =>
      new URL(response.url()).pathname.endsWith(
        `/catalog-listings/${item.listingId}/releases`,
      ),
    );
  const firstReply = nextHistoryReply();
  const ordered = [...expected].sort();
  const currentID = required(expected.at(-1));
  await login(
    page,
    catalog,
    "local-reader",
    `/listings/${item.listingId}#release-history`,
  );
  const entries = page.locator(".release-history-entry");
  const checkCurrentRelease = async (ids: string[]) => {
    const present = ids.includes(currentID);
    await expect(
      entries.getByText("Current listing release", { exact: true }),
    ).toHaveCount(present ? 1 : 0);
    if (present)
      await expect(
        entries
          .filter({
            has: page.locator(
              `a[href="${catalog.basePath}/releases/${currentID}"]`,
            ),
          })
          .getByText("Current listing release", { exact: true }),
      ).toBeVisible();
  };
  await expect(entries).toHaveCount(25);
  const first = await entries
    .locator("a")
    .evaluateAll((links) =>
      links.map((link) => link.getAttribute("href")!.split("/").at(-1)!),
    );
  expect(first).toEqual(ordered.slice(0, 25));
  await checkCurrentRelease(first);
  const firstData = object(await (await firstReply).json());
  expect(array(firstData.items).map((row) => text(object(row).id))).toEqual(
    first,
  );
  expect(text(firstData.nextCursor)).not.toBe("");
  const secondReply = nextHistoryReply();
  await page.getByRole("button", { name: "Next page", exact: true }).click();
  await expect(entries).toHaveCount(1);
  const last = await entries
    .locator("a")
    .evaluateAll((links) =>
      links.map((link) => link.getAttribute("href")!.split("/").at(-1)!),
    );
  expect(last).toEqual(ordered.slice(25));
  expect(new Set([...first, ...last]).size).toBe(26);
  await checkCurrentRelease(last);
  const lastData = object(await (await secondReply).json());
  expect(array(lastData.items).map((row) => text(object(row).id))).toEqual(
    last,
  );
  expect(lastData.nextCursor).toBeUndefined();
  await page.getByRole("button", { name: "First page", exact: true }).click();
  await expect(entries).toHaveCount(25);
  await checkCurrentRelease(first);
  expect(
    requests.filter((path) =>
      /\/(version-revisions|listing-revisions)\//.test(path),
    ),
  ).toEqual([]);
  await expect(
    page.getByRole("button", { name: "New listing revision", exact: true }),
  ).toBeDisabled();
  // The same real corpus supplies more than one queue page, without synthetic cursors.
  await page.goto(`${catalog.origin}${catalog.basePath}/reviews`);
  const listingFilter = page.getByRole("textbox", {
    name: "Exact Listing ID",
    exact: true,
  });
  await listingFilter.fill(item.listingId);
  await page
    .getByRole("combobox", { name: "Review state", exact: true })
    .selectOption("approved");
  await page
    .getByRole("button", { name: "Apply review filters", exact: true })
    .click();
  const rows = page.locator(".workspace-table tbody tr");
  await expect(rows).toHaveCount(25);
  await expect(page).toHaveURL(
    `${catalog.origin}${catalog.basePath}/reviews?state=approved&listing=${item.listingId}`,
  );
  await listingFilter.fill("00000000-0000-7000-8000-000000000099");
  const pager = page.getByRole("group", {
    name: "Review submissions pagination",
    exact: true,
  });
  await pager.getByRole("button", { name: "Next page", exact: true }).click();
  await expect(rows).toHaveCount(1);
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(rows).toHaveCount(1);
  await pager.getByRole("button", { name: "First page", exact: true }).click();
  await expect(rows).toHaveCount(25);
  await expect(listingFilter).toHaveValue(
    "00000000-0000-7000-8000-000000000099",
  );
  await page
    .getByRole("button", { name: "Clear review filters", exact: true })
    .click();
  await expect(page).toHaveURL(
    `${catalog.origin}${catalog.basePath}/reviews?state=`,
  );
  await page.goBack();
  await expect(listingFilter).toHaveValue(item.listingId);
  await expect(rows).toHaveCount(25);
});
