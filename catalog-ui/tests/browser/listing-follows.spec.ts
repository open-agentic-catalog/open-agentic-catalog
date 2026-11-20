import { test, expect, login, confirmPersonalCommand } from "./fixtures";
import type { components } from "../../src/api/schema";

for (const mount of ["root", "subpath"]) {
  test(`${mount}: follow a local Listing, check changes on demand and acknowledge the displayed revision`, async ({
    page,
    catalog,
  }) => {
    const target = catalog.receipt.items.find((i) => i.releaseId);
    if (!target?.releaseId)
      throw new Error("A published local release is required");
    const commandReply = (method: string, path: string) =>
      page.waitForResponse(
        (r) =>
          r.request().method() === method &&
          new URL(r.url()).pathname.endsWith(path),
      );
    const path = "/api/v1/personal/items";
    await login(page, catalog, "local-reader", "/catalog/" + target.releaseId);
    await page.getByText("Follow Listing", { exact: true }).click();
    const form = page.locator("details").filter({
      has: page.locator("summary").filter({ hasText: /^Follow Listing$/ }),
    });
    await form
      .getByRole("textbox", { name: "Saved label" })
      .fill("My release watch");
    await form.getByRole("button", { name: "Follow this Listing" }).click();
    const create = await confirmPersonalCommand(
      page,
      commandReply("POST", path),
      201,
    );
    const receipt = (await create.json()) as { id: string; revision: number };
    expect(receipt.revision).toBe(1);
    await form.getByRole("link", { name: "Open saved item" }).click();
    await page.getByRole("button", { name: "Check current release" }).click();
    await expect(
      page.getByText("No change since acknowledgement", { exact: true }),
    ).toBeVisible();
    const releasePath = "/api/v1/listing-releases/" + target.releaseId;
    const release = await catalog.admin("GET", releasePath);
    await catalog.admin(
      "POST",
      releasePath + ":deprecate",
      { reason: "Maintenance for the next release" },
      release.etag,
    );
    await page.goto(catalog.origin + catalog.basePath + "/saved-items");
    await page.getByLabel("Saved item type").selectOption("follow");
    await page
      .getByRole("button", { name: "Apply saved item filters" })
      .click();
    await page
      .getByRole("button", { name: "Check followed Listings on this page" })
      .click();
    const updates = page.getByRole("region", { name: "Followed Listings" });
    await expect(updates).toContainText("Changed since acknowledgement");
    const itemPath = path + "/" + receipt.id;
    const item = await catalog.admin<components["schemas"]["PersonalItem"]>(
      "GET",
      itemPath,
      undefined,
      undefined,
      "local-reader",
    );
    expect(item.data.revision).toBe(1);
    expect(item.data.listingId).toBe(target.listingId);
    expect(Object.keys(item.data)).not.toContain("acknowledged");
    await updates.getByRole("link", { name: "My release watch" }).click();
    await page.getByRole("button", { name: "Check current release" }).click();
    await page
      .getByRole("button", { name: "Acknowledge current release" })
      .click();
    const ack = await confirmPersonalCommand(
      page,
      commandReply("PATCH", itemPath),
      200,
    );
    expect(ack.request().postDataJSON()).toMatchObject({
      listingId: target.listingId,
      observed: { releaseId: target.releaseId, revision: 2 },
    });
    await page.getByRole("button", { name: "Check current release" }).click();
    await expect(
      page.getByText("No change since acknowledgement", { exact: true }),
    ).toBeVisible();
    const deprecated = await catalog.admin("GET", releasePath);
    await catalog.admin(
      "POST",
      releasePath + ":retire",
      { reason: "Retire the followed fixture release" },
      deprecated.etag,
    );
    await page.getByRole("button", { name: "Check current release" }).click();
    await expect(
      page.getByText("This followed Listing is unavailable."),
    ).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Open current release" }),
    ).toHaveCount(0);
    await expect(page.getByText("No change since acknowledgement")).toHaveCount(
      0,
    );
    await expect(
      page.getByRole("button", { name: "Acknowledge current release" }),
    ).toHaveCount(0);
    await page.getByRole("button", { name: "Unfollow Listing" }).click();
    await confirmPersonalCommand(page, commandReply("DELETE", itemPath), 200);
    await expect(
      page.getByText("Saved item removed.", { exact: true }).first(),
    ).toBeVisible();
  });
}
