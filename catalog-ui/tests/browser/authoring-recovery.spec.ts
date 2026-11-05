// Controlled faults cross the real API; acknowledged writes and original retries remain observable.
import { test, expect, login, confirmCommand } from "./fixtures";
import { fillAgentFields } from "./authoring-fields";
import { object, text } from "./json";
import { required } from "../required";

for (const fault of [
  "refused Version",
  "uncertain Listing revision",
] as const) {
  test(`subpath: resume authoring after ${fault} without repeating acknowledged writes`, async ({
    page,
    catalog,
    network,
  }) => {
    const writes: {
      path: string;
      body: unknown;
      key: string | undefined;
      etag: string | undefined;
    }[] = [];
    page.on("request", (request) => {
      if (
        request.method() === "POST" &&
        request.url().includes("/_catalog/api/v1/")
      )
        writes.push({
          path: new URL(request.url()).pathname,
          body: request.postDataJSON(),
          key: request.headers()["idempotency-key"],
          etag: request.headers()["if-match"],
        });
    });
    await login(page, catalog, "local-contributor", "/resources/new");
    await page
      .getByRole("combobox", { name: "Namespace", exact: true })
      .selectOption(catalog.receipt.namespaceId);
    const identifier = `urn:air:demo.catalog.example.com:agents:recovery-${fault === "refused Version" ? "version" : "listing"}`;
    await page
      .getByLabel("Canonical resource identifier", { exact: true })
      .fill(identifier);
    await page
      .getByRole("button", { name: "Next: Content", exact: true })
      .click();
    await page.getByLabel("Version label", { exact: true }).fill("1.0.0");
    await fillAgentFields(page);
    await page.getByLabel("Include Listing details", { exact: true }).check();
    await page
      .getByRole("button", { name: "Next: Listing details", exact: true })
      .click();
    await page
      .getByRole("combobox", { name: "Publisher", exact: true })
      .selectOption(catalog.receipt.publisherId);
    await page
      .getByRole("textbox", { name: "Display name", exact: true })
      .fill("Retained recovery draft");
    await page
      .getByRole("textbox", { name: "Description", exact: true })
      .fill("Keep this Listing text after an interrupted save.");
    if (fault === "refused Version") {
      await page.route(
        "**/_catalog/api/v1/resources/*/versions",
        async (route) => {
          network.expectFailure(
            "POST",
            new URL(route.request().url()).pathname.slice(
              catalog.basePath.length,
            ),
            400,
          );
          await route.continue({
            postData: JSON.stringify({
              ...object(route.request().postDataJSON()),
              unsupportedField: true,
            }),
          });
        },
        { times: 1 },
      );
      const refused = page.waitForResponse(
        (response) =>
          response.request().method() === "POST" &&
          /\/resources\/[^/]+\/versions$/.test(
            new URL(response.url()).pathname,
          ),
      );
      await page
        .getByRole("button", { name: "Save content draft", exact: true })
        .click();
      const expected = `${catalog.basePath}/_catalog/api/v1/resources`;
      await page
        .getByRole("dialog")
        .getByRole("button", { name: "Confirm", exact: true })
        .click();
      expect((await refused).status()).toBe(400);
      await expect(
        page
          .getByRole("dialog")
          .getByRole("button", { name: "Back to form", exact: true }),
      ).toBeVisible();
      await page
        .getByRole("button", { name: "Back to form", exact: true })
        .click();
      await expect(
        page.getByLabel("Choose draft JSON file", { exact: true }),
      ).toBeDisabled();
      await page
        .getByRole("button", { name: "Back: Content", exact: true })
        .click();
      await expect(page.getByLabel("Agent name", { exact: true })).toHaveValue(
        "Order status Agent",
      );
      await page
        .getByRole("button", { name: "Back: Basics", exact: true })
        .click();
      await expect(
        page.getByLabel("Canonical resource identifier", { exact: true }),
      ).toHaveValue(identifier);
      await expect(
        page.getByLabel("Canonical resource identifier", { exact: true }),
      ).toBeDisabled();
      await page
        .getByRole("button", { name: "Next: Content", exact: true })
        .click();
      await expect(
        page.getByLabel("Version label", { exact: true }),
      ).toHaveValue("1.0.0");
      await page
        .getByRole("button", { name: "Next: Listing details", exact: true })
        .click();
      expect(writes.filter((item) => item.path === expected)).toHaveLength(1);
      await expect(
        page.getByRole("textbox", { name: "Description", exact: true }),
      ).toHaveValue("Keep this Listing text after an interrupted save.");
      await page
        .getByRole("button", { name: "Save content draft", exact: true })
        .click();
      await confirmCommand(page);
    } else {
      await page.route(
        "**/_catalog/api/v1/catalog-listings/*/revisions",
        async (route) => {
          const response = await route.fetch();
          expect(response.status()).toBe(201);
          await route.fulfill({ response, body: "{}" });
        },
        { times: 1 },
      );
      await page
        .getByRole("button", { name: "Save content draft", exact: true })
        .click();
      await page
        .getByRole("dialog")
        .getByRole("button", { name: "Confirm", exact: true })
        .click();
      await expect(
        page.getByRole("button", { name: "Retry same command", exact: true }),
      ).toBeVisible();
      await expect(
        page.getByRole("button", { name: "Back to form", exact: true }),
      ).toBeDisabled();
      await expect(
        page.getByLabel("Choose draft JSON file", { exact: true }),
      ).toBeDisabled();
      expect(writes).toHaveLength(4);
      await page
        .getByRole("button", { name: "Retry same command", exact: true })
        .click();
    }
    await expect(
      page.getByRole("heading", { name: "Listing draft saved", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByLabel("Choose draft JSON file", { exact: true }),
    ).toBeDisabled();
    expect(writes).toHaveLength(5);
    expect(
      writes.filter((item) => item.path.endsWith("/resources")),
    ).toHaveLength(1);
    expect(
      writes.filter((item) => item.path.endsWith("/catalog-listings")),
    ).toHaveLength(1);
    if (fault === "uncertain Listing revision")
      expect(writes[4]).toEqual(writes[3]);
    const saved = required(writes.at(-1));
    const listingId = required(saved.path.split("/").at(-2));
    const retained = await catalog.admin(
      "GET",
      `/api/v1/catalog-listings/${listingId}`,
    );
    const candidate = await catalog.admin(
      "GET",
      `/api/v1/listing-revisions/${text(retained.data.candidateRevisionId)}`,
    );
    expect(candidate.data).toMatchObject({
      displayName: "Retained recovery draft",
      description: "Keep this Listing text after an interrupted save.",
      versionRevisionId: text(object(saved.body).versionRevisionId),
    });
  });
}
