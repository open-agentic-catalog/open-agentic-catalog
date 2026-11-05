import { required } from "../required";
import { test, expect, login, confirmCommand } from "./fixtures";
import { doubleText } from "./text-layout";

for (const base of ["root", "subpath"]) {
  test(`${base}: authoring confirmations capture deletion, revalidation and complete Listing changes`, async ({
    page,
    catalog,
    network,
  }, info) => {
    const item = required(
      catalog.receipt.items.find((entry) => entry.key === "draft-agent"),
    );
    const bundle = await catalog.admin<{
      namespaceId: string;
      revision: { resourceVersionId: string };
    }>("GET", `/api/v1/version-revisions/${item.versionRevisionId}`);
    const unused = await catalog.admin<{
      id: string;
      canonicalIdentifier: string;
    }>("POST", "/api/v1/resources", {
      namespaceId: catalog.receipt.namespaceId,
      canonicalIdentifier: "unused-confirmation-agent",
      kind: "agent",
    });
    const current = await catalog.admin(
      "GET",
      `/api/v1/catalog-listings/${item.listingId}`,
    );
    const old = await catalog.admin<{ revision: { id: string } }>(
      "POST",
      `/api/v1/catalog-listings/${item.listingId}/revisions`,
      {
        versionRevisionId: item.versionRevisionId,
        publisherId: catalog.receipt.publisherId,
        displayName: "Saved confirmation title",
        description: "Saved confirmation description",
        visibility: "private",
        tags: ["saved,tag"],
        categories: ["Saved category"],
        representativeQueries: ["Saved query"],
        documentationUrl: "https://docs.example.test/old",
        relationships: [
          {
            type: "depends_on",
            targetIdentifier: required(
              catalog.receipt.items.find((entry) => entry.key === "order-mcp"),
            ).identifier,
          },
        ],
        extensions: { opaque: { kept: "a,b 🙂", removed: true } },
      },
      current.etag,
    );
    const mutations: {
      path: string;
      body: unknown;
      etag: string | undefined;
      key: string | undefined;
    }[] = [];
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (request.method() === "POST" && path.includes("/_catalog/"))
        mutations.push({
          path,
          body: request.postDataJSON() as unknown,
          etag: request.headers()["if-match"],
          key: request.headers()["idempotency-key"],
        });
    });
    await login(page, catalog, "local-admin", `/resources/${unused.data.id}`);
    await page.getByText("Discard an unused resource", { exact: true }).click();
    await page
      .getByRole("textbox", { name: "Reason", exact: true })
      .fill("  Discard unused confirmation draft  ");
    await expect(
      page.getByRole("button", { name: "Delete unused resource", exact: true }),
    ).toHaveClass("danger");
    await expect(
      page
        .getByRole("button", { name: "Delete unused resource", exact: true })
        .locator('svg[aria-hidden="true"]'),
    ).toHaveCount(1);
    await page
      .getByRole("button", { name: "Delete unused resource", exact: true })
      .click();
    const dialog = page.getByRole("dialog");
    const facts = dialog.getByLabel("Command summary", { exact: true });
    await expect(
      dialog.getByRole("button", { name: "Confirm", exact: true }),
    ).toHaveClass("danger");
    for (const value of [
      unused.data.id,
      unused.data.canonicalIdentifier,
      catalog.receipt.namespaceId,
      "Discard unused confirmation draft",
      unused.etag!,
    ])
      await expect(facts).toContainText(value);
    await expect(dialog).toContainText("Only an unused draft can be deleted");
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(mutations).toHaveLength(0);
    expect(
      (
        await catalog.admin<{ id: string }>(
          "GET",
          `/api/v1/resources/${unused.data.id}`,
        )
      ).data.id,
    ).toBe(unused.data.id);
    await page
      .getByRole("button", { name: "Delete unused resource", exact: true })
      .click();
    await confirmCommand(page);
    await expect(page.getByRole("status")).toContainText("Resource deleted.");
    expect(mutations).toHaveLength(1);
    expect(mutations[0]).toMatchObject({
      body: { reason: "Discard unused confirmation draft" },
      etag: unused.etag,
    });
    await expect(
      catalog.admin("GET", `/api/v1/resources/${unused.data.id}`),
    ).rejects.toThrow(/404/);

    await page.goto(
      `${catalog.origin}${catalog.basePath}/version-revisions/${item.versionRevisionId}`,
    );
    await expect(
      page.getByRole("button", { name: "Revalidate revision", exact: true }),
    ).toHaveClass("warning");
    await page
      .getByRole("button", { name: "Revalidate revision", exact: true })
      .click();
    for (const value of [
      item.versionRevisionId,
      bundle.data.revision.resourceVersionId,
      bundle.data.namespaceId,
    ])
      await expect(facts).toContainText(value);
    await expect(facts).toContainText("No If-Match");
    await expect(
      dialog.getByRole("button", { name: "Confirm", exact: true }),
    ).toHaveClass("warning");
    await expect(dialog).toContainText(
      "withdraw entire active discovery snapshots",
    );
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(mutations).toHaveLength(1);
    await page
      .getByRole("button", { name: "Revalidate revision", exact: true })
      .click();
    await confirmCommand(page);
    await expect(page.getByRole("status")).toContainText(
      "Validation: complete.",
    );
    expect(mutations).toHaveLength(2);
    expect(mutations[1]).toMatchObject({ body: {} });
    expect(required(mutations[1]).etag).toBeUndefined();

    await page.goto(
      `${catalog.origin}${catalog.basePath}/listings/${item.listingId}`,
    );
    await page
      .getByRole("button", { name: "New listing revision", exact: true })
      .click();
    await page
      .getByRole("textbox", { name: "Display name", exact: true })
      .fill("新しい, title 🙂");
    await page
      .getByRole("textbox", { name: "Description", exact: true })
      .fill("New line one\nNew line two");
    await page
      .getByRole("textbox", { name: "Tag 1", exact: true })
      .fill("one,two 🙂");
    await page
      .getByRole("textbox", { name: "Category 1", exact: true })
      .fill("new category");
    await page
      .getByRole("textbox", {
        name: "Representative queries (one per line)",
        exact: true,
      })
      .fill("Find a,b 🙂");
    await page
      .getByText("Links, license and relationships", { exact: true })
      .click();
    await page
      .getByRole("textbox", { name: "Documentation URL", exact: true })
      .fill("");
    await page
      .getByRole("textbox", { name: "Extensions (JSON object)", exact: true })
      .fill('{"opaque":{"kept":"a,b 🙂","array":["b","a"]}}');
    // Wait for the existing selector, then observe opening the inert review.
    await expect(
      page.getByRole("combobox", { name: "Publisher", exact: true }),
    ).toHaveValue(catalog.receipt.publisherId);
    let reads = 0;
    page.on("request", (r) => {
      if (r.method() === "GET" && r.url().includes("/_catalog/")) reads++;
    });
    await page
      .getByRole("button", { name: "Create listing revision", exact: true })
      .click();
    await expect(facts).toContainText("Edit saved candidate");
    await expect(facts).toContainText(old.data.revision.id);
    const review = dialog.getByRole("region", {
      name: "Prepared listing changes",
      exact: true,
    });
    await expect(
      review.getByRole("region", { name: "Changed tags", exact: true }),
    ).toContainText("one,two 🙂");
    await expect(
      review.getByRole("region", {
        name: "Removed documentationUrl",
        exact: true,
      }),
    ).toContainText("Absent");
    await review
      .getByText("Review relationships, extensions and artwork", {
        exact: true,
      })
      .click();
    await expect(
      review.getByRole("region", { name: "Changed extensions", exact: true }),
    ).toContainText("removed");
    await expect(review.getByRole("img")).toHaveCount(0);
    await expect(review.getByRole("link")).toHaveCount(0);
    expect(reads).toBe(0);
    expect(mutations).toHaveLength(2);
    await page.setViewportSize({ width: 390, height: 844 });
    await doubleText(page);
    expect(
      await page.evaluate(
        () =>
          document.documentElement.scrollWidth <=
          document.documentElement.clientWidth,
      ),
    ).toBe(true);
    await dialog.screenshot({
      path: info.outputPath("listing-prepared-changes-narrow.png"),
    });
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(mutations).toHaveLength(2);
    await page
      .getByRole("button", { name: "Create listing revision", exact: true })
      .click();
    const historyPath = `/api/v1/catalog-listings/${item.listingId}/revisions?limit=100`;
    const historyBefore = await catalog.admin<{
      items: { id: string }[];
      nextCursor?: string;
    }>("GET", historyPath);
    expect(historyBefore.data.nextCursor).toBeUndefined();
    const commandPath = `/_catalog/api/v1/catalog-listings/${item.listingId}/revisions`;
    network.expectFailure("POST", commandPath, 503);
    // Commit through the real API, then lose only its usable browser reply.
    await page.route(
      catalog.origin + catalog.basePath + commandPath,
      async (route) => {
        const response = await route.fetch({ maxRedirects: 0, maxRetries: 0 });
        expect(response.status()).toBe(201);
        await route.fulfill({
          status: 503,
          contentType: "application/problem+json",
          body: JSON.stringify({ code: "DEPENDENCY_UNAVAILABLE" }),
        });
      },
      { times: 1 },
    );
    await dialog.getByRole("button", { name: "Confirm", exact: true }).click();
    await expect(dialog.getByRole("alert")).toContainText(
      "The outcome is unknown",
    );
    await expect(
      dialog.getByRole("button", { name: "Back to form", exact: true }),
    ).toBeDisabled();
    await expect(facts).toContainText(old.data.revision.id);
    await expect(
      review.getByRole("region", { name: "Changed tags", exact: true }),
    ).toContainText("one,two 🙂");
    await expect(page.getByRole("status")).toHaveCount(0);
    const latest = await catalog.admin<{ candidateRevisionId: string }>(
      "GET",
      `/api/v1/catalog-listings/${item.listingId}`,
    );
    const retained = await catalog.admin<Record<string, unknown>>(
      "GET",
      `/api/v1/listing-revisions/${latest.data.candidateRevisionId}`,
    );
    expect(retained.data).toMatchObject({
      displayName: "新しい, title 🙂",
      description: "New line one\nNew line two",
      tags: ["one,two 🙂"],
      categories: ["new category"],
      representativeQueries: ["Find a,b 🙂"],
      extensions: { opaque: { kept: "a,b 🙂", array: ["b", "a"] } },
    });
    expect(retained.data.documentationUrl).toBeUndefined();
    expect(mutations).toHaveLength(3);
    const savedRequest = required(mutations[2]);
    expect(savedRequest.path).toBe(catalog.basePath + commandPath);
    expect(savedRequest.key).toBeTruthy();
    expect(savedRequest.etag).toBe(old.etag);
    expect(savedRequest.body).toEqual({
      versionRevisionId: item.versionRevisionId,
      publisherId: catalog.receipt.publisherId,
      displayName: "新しい, title 🙂",
      description: "New line one\nNew line two",
      tags: ["one,two 🙂"],
      categories: ["new category"],
      representativeQueries: ["Find a,b 🙂"],
      visibility: "private",
      relationships: [
        {
          type: "depends_on",
          targetIdentifier: required(
            catalog.receipt.items.find((entry) => entry.key === "order-mcp"),
          ).identifier,
        },
      ],
      extensions: { opaque: { kept: "a,b 🙂", array: ["b", "a"] } },
    });
    expect(savedRequest.body).not.toHaveProperty("documentationUrl");
    const replayResponse = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        new URL(response.url()).pathname === catalog.basePath + commandPath,
    );
    await dialog
      .getByRole("button", { name: "Retry same command", exact: true })
      .click();
    const replay = await replayResponse;
    expect(replay.status()).toBe(201);
    expect(replay.headers()["idempotency-replayed"]).toBe("true");
    await expect(dialog).toHaveCount(0);
    await expect(
      page.getByRole("link", {
        name: "Reload listing to review saved revision",
        exact: true,
      }),
    ).toBeVisible();
    expect(mutations).toHaveLength(4);
    expect(mutations[3]).toEqual(savedRequest);
    const recovered = await catalog.admin<{ candidateRevisionId: string }>(
      "GET",
      `/api/v1/catalog-listings/${item.listingId}`,
    );
    expect(recovered.data).toEqual(latest.data);
    expect(recovered.etag).toBe(latest.etag);
    const historyAfter = await catalog.admin<{
      items: { id: string }[];
      nextCursor?: string;
    }>("GET", historyPath);
    expect(historyAfter.data.nextCursor).toBeUndefined();
    expect(historyAfter.data.items.map(({ id }) => id).sort()).toEqual(
      [
        ...historyBefore.data.items.map(({ id }) => id),
        latest.data.candidateRevisionId,
      ].sort(),
    );
  });
}
