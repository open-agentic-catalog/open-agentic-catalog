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

test("subpath: a stale acknowledgement preserves the follow until its owner confirms the new release revision", async ({
  page,
  catalog,
  network,
}) => {
  const target = catalog.receipt.items.find((item) => item.releaseId);
  if (!target?.releaseId)
    throw new Error("A published local release is required");
  const items = "/api/v1/personal/items";
  const follow = {
    type: "follow",
    listingId: target.listingId,
    label: "Watch release changes",
  };
  const reader = "local-reader";
  const created = await catalog.admin<components["schemas"]["PersonalReceipt"]>(
    "POST",
    items,
    follow,
    undefined,
    reader,
  );
  const itemPath = items + "/" + created.data.id;
  const readItem = () =>
    catalog.admin<components["schemas"]["PersonalItem"]>(
      "GET",
      itemPath,
      undefined,
      undefined,
      reader,
    );
  const original = await readItem();
  expect(original.data.revision).toBe(1);
  expect(original.etag).toBeTruthy();
  const releasePath = "/api/v1/listing-releases/" + target.releaseId;
  const release = await catalog.admin<{ revision: number }>("GET", releasePath);
  const observation = (revision: number) => ({
    releaseId: target.releaseId,
    revision,
  });
  const dialog = page.getByRole("dialog");
  const prepare = async (revision: number) => {
    await page.getByRole("button", { name: "Check current release" }).click();
    await expect(
      page.getByText("Not yet acknowledged", { exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Acknowledge current release" })
      .click();
    await expect(
      dialog.getByText("Displayed release", { exact: true }).locator("+ dd"),
    ).toHaveText(target.releaseId!);
    await expect(
      dialog.getByText("Release revision", { exact: true }).locator("+ dd"),
    ).toHaveText(String(revision));
  };
  const patch = () =>
    page.waitForResponse(
      (response) =>
        response.request().method() === "PATCH" &&
        new URL(response.url()).pathname ===
          catalog.basePath + "/_catalog" + itemPath,
    );
  await login(page, catalog, reader, "/saved-items/" + created.data.id);
  await prepare(release.data.revision);

  await catalog.admin(
    "POST",
    releasePath + ":deprecate",
    { reason: "Deprecate after the reader prepares acknowledgement" },
    release.etag,
  );
  network.expectFailure("PATCH", "/_catalog" + itemPath, 412);
  const [refused] = await Promise.all([
    patch(),
    dialog.getByRole("button", { name: "Confirm", exact: true }).click(),
  ]);
  expect(refused.status()).toBe(412);
  expect(refused.request().postDataJSON()).toEqual({
    ...follow,
    observed: observation(release.data.revision),
  });
  const refusedHeaders = refused.request().headers();
  expect(refusedHeaders["if-match"]).toBe(original.etag);
  expect(refusedHeaders["idempotency-key"]).toBeTruthy();
  await expect(dialog.getByRole("alert")).toContainText(
    "The record changed. Your input is retained.",
  );
  await expect(
    dialog.getByRole("button", { name: /confirm|retry/i }),
  ).toHaveCount(0);
  const afterRefusal = await readItem();
  expect(afterRefusal.data).toEqual(original.data);
  expect(afterRefusal.etag).toBe(original.etag);
  const current = observation(release.data.revision + 1);
  const resolved = await catalog.admin(
    "POST",
    itemPath + "/resolve",
    {},
    undefined,
    reader,
  );
  expect(resolved.data).toMatchObject({
    status: "available",
    change: "unacknowledged",
    current,
  });

  await dialog.getByRole("button", { name: "Back to form" }).click();
  await page.reload();
  await prepare(current.revision);
  const success = await confirmPersonalCommand(page, patch(), 200);
  expect(success.request().postDataJSON()).toEqual({
    ...follow,
    observed: current,
  });
  const freshHeaders = success.request().headers();
  expect(freshHeaders["if-match"]).toBe(original.etag);
  expect(freshHeaders["idempotency-key"]).toBeTruthy();
  expect(freshHeaders["idempotency-key"]).not.toBe(
    refusedHeaders["idempotency-key"],
  );
  expect((await readItem()).data.revision).toBe(original.data.revision + 1);
  await page.reload();
  await page.getByRole("button", { name: "Check current release" }).click();
  await expect(
    page.getByText("No change since acknowledgement", { exact: true }),
  ).toBeVisible();
});
