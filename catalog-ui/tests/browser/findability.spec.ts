import { test, expect, login, confirmCommand } from "./fixtures";
import { doubleText } from "./text-layout";
import { object, text } from "./json";

for (const base of ["root", "subpath"]) {
  // Prepare a later-page population through the owned fixture API, then exercise
  // the actual browser and fixed-upstream UI host for every find/filter assertion.
  test(`${base}: authoring finds an exact identity beyond the first page and combines kind`, async ({
    page,
    catalog,
  }, testInfo) => {
    const prefix = "urn:air:demo.catalog.example.com:demo:findability-";
    for (let i = 0; i < 26; i++) {
      await catalog.admin("POST", "/api/v1/resources", {
        namespaceId: catalog.receipt.namespaceId,
        canonicalIdentifier: `${prefix}${i}`,
        kind: "agent",
      });
    }
    // Creation order cannot establish update-time order on this host. Select
    // an actual second-page fixture target; the application still makes only
    // its first-page request and one server-filtered lookup.
    type Resource = { canonicalIdentifier: string };
    const first = await catalog.admin<{
      items: Resource[];
      nextCursor: string;
    }>("GET", "/api/v1/resources?limit=25");
    expect(first.data.nextCursor).toBeTruthy();
    const second = await catalog.admin<{ items: Resource[] }>(
      "GET",
      `/api/v1/resources?limit=25&cursor=${encodeURIComponent(first.data.nextCursor)}`,
    );
    const target = {
      identifier: second.data.items.find((item) =>
        item.canonicalIdentifier.startsWith(prefix),
      )!.canonicalIdentifier,
    };
    await login(page, catalog, "local-contributor", "/resources");
    const identifier = page.getByRole("textbox", {
      name: "Exact canonical identifier",
      exact: true,
    });
    await expect(
      page.getByRole("button", { name: "Next page", exact: true }),
    ).toBeEnabled();
    await expect(
      page.getByRole("link", { name: target.identifier, exact: true }),
    ).toHaveCount(0);
    await identifier.fill(
      `  ${target.identifier.replace("urn:air:", "URN:AIR:")}  `,
    );
    await page
      .getByRole("button", { name: "Find resource", exact: true })
      .click();
    await expect(
      page.getByRole("link", { name: target.identifier, exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Next page", exact: true }),
    ).toHaveCount(0);
    await page
      .getByRole("combobox", { name: "Resource kind", exact: true })
      .selectOption("skill");
    await expect(
      page.getByText("No resources match these filters.", { exact: true }),
    ).toBeVisible();
    await page
      .getByRole("combobox", { name: "Resource kind", exact: true })
      .selectOption("agent");
    await expect(
      page.getByRole("link", { name: target.identifier, exact: true }),
    ).toBeVisible();
    await page.screenshot({
      path: testInfo.outputPath("authoring-exact-identifier.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 320, height: 900 });
    await doubleText(page);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    await page
      .getByRole("button", { name: "Clear identifier", exact: true })
      .click();
    await expect(identifier).toHaveValue("");
    await expect(
      page.getByRole("combobox", { name: "Resource kind", exact: true }),
    ).toHaveValue("agent");
    await expect(
      page.getByRole("button", { name: "Next page", exact: true }),
    ).toBeEnabled();
    await identifier.fill("findability:absent");
    await page
      .getByRole("button", { name: "Find resource", exact: true })
      .click();
    await expect(
      page.getByText("No resources match these filters.", { exact: true }),
    ).toBeVisible();
    const opaqueIdentifier = "\ufeffOpaque:e\u0301:🙂\ufeff";
    await page.goto(`${catalog.origin}${catalog.basePath}/resources/new`);
    await page
      .getByRole("combobox", { name: "Namespace", exact: true })
      .selectOption(catalog.receipt.namespaceId);
    await page
      .getByRole("textbox", {
        name: "Canonical resource identifier",
        exact: true,
      })
      .fill(`\u0085 \t${opaqueIdentifier}\u0085 `);
    await page
      .getByRole("button", { name: "Create resource", exact: true })
      .click();
    expect(
      await page
        .getByRole("dialog")
        .getByLabel("Command summary")
        .textContent(),
    ).toContain(opaqueIdentifier);
    const created = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        new URL(response.url()).pathname.endsWith("/api/v1/resources"),
    );
    await confirmCommand(page);
    const response = await created;
    expect(response.status()).toBe(201);
    expect(response.request().postDataJSON()).toEqual({
      namespaceId: catalog.receipt.namespaceId,
      canonicalIdentifier: opaqueIdentifier,
      kind: "agent",
    });
    const opaque = object(await response.json());
    expect(opaque.canonicalIdentifier).toBe(opaqueIdentifier);
    const opaqueId = text(opaque.id);
    expect(opaqueId).toMatch(/^[0-9a-f-]{36}$/);
    await page.goto(`${catalog.origin}${catalog.basePath}/resources`);
    await page
      .getByRole("combobox", { name: "Resource kind", exact: true })
      .selectOption("agent");
    await identifier.fill(` \t${opaqueIdentifier} `);
    await page
      .getByRole("button", { name: "Find resource", exact: true })
      .click();
    const opaqueLink = page.locator(
      `a[href="${catalog.basePath}/resources/${opaqueId}"]`,
    );
    await expect(opaqueLink).toBeVisible();
    expect(await opaqueLink.textContent()).toBe(opaqueIdentifier);
  });

  test(`${base}: review queue shows exact submitted context after a later rename without hydrating rows`, async ({
    page,
    catalog,
  }, testInfo) => {
    const target = catalog.receipt.items.find(
      (item) => item.key === "pending-mcp",
    )!;
    const listing = await catalog.admin<{ candidateRevisionId: string }>(
      "GET",
      `/api/v1/catalog-listings/${target.listingId}`,
    );
    const original = await catalog.admin<{
      displayName: string;
      description: string;
      publisherId: string;
      visibility: string;
    }>("GET", `/api/v1/listing-revisions/${listing.data.candidateRevisionId}`);
    await catalog.admin(
      "POST",
      `/api/v1/catalog-listings/${target.listingId}/revisions`,
      {
        versionRevisionId: target.versionRevisionId,
        displayName: "Renamed after submission",
        description: original.data.description,
        publisherId: original.data.publisherId,
        visibility: original.data.visibility,
      },
      listing.etag,
    );
    const reads: string[] = [];
    page.on("request", (r) => {
      if (
        r.method() === "GET" &&
        /\/(catalog-listings|listing-revisions|resources)\//.test(
          new URL(r.url()).pathname,
        )
      )
        reads.push(r.url());
    });
    await login(page, catalog, "local-reviewer", "/reviews");
    const state = page.getByRole("combobox", {
      name: "Review state",
      exact: true,
    });
    await expect(state).toHaveValue("submitted");
    await expect(
      state.getByRole("option", { name: "Submitted", exact: true }),
    ).toHaveAttribute("value", "submitted");
    await expect(
      state.getByRole("option", { name: "Changes required", exact: true }),
    ).toHaveAttribute("value", "changes_required");
    await expect(page.getByText(/Applied Listing:/)).toContainText(
      "State: Submitted.",
    );
    const row = page.getByRole("row").filter({
      has: page.getByRole("link", { name: target.submissionId!, exact: true }),
    });
    await expect(row).toContainText(original.data.displayName);
    await expect(row).toContainText(target.identifier);
    await expect(row).not.toContainText("Renamed after submission");
    expect(reads).toEqual([]);
    await page.screenshot({
      path: testInfo.outputPath("review-queue-context.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 320, height: 900 });
    await doubleText(page);
    await expect(row).toContainText(original.data.displayName);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
  });
}
