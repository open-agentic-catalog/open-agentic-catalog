import { required } from "../required";
import { test, expect, login } from "./fixtures";
import { doubleText } from "./text-layout";
import type { Locator } from "@playwright/test";

async function expectActionGap(first: Locator, second: Locator) {
  const a = required((await first.boundingBox()) ?? undefined);
  const b = required((await second.boundingBox()) ?? undefined);
  const horizontal = b.x - (a.x + a.width);
  const vertical = b.y - (a.y + a.height);
  expect(Math.max(horizontal, vertical)).toBeGreaterThanOrEqual(8);
}

test("exact review and snapshot keep Back and Reload separate", async ({
  page,
  catalog,
}) => {
  const item = required(
    catalog.receipt.items.find((entry) => entry.key === "pending-mcp"),
  );
  const snapshot = await catalog.admin<{ id: string; generation: number }>(
    "POST",
    "/api/v1/publication/snapshots",
    { host: "127.0.0.1" },
  );
  const writes: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() !== "GET" &&
      new URL(request.url()).pathname.includes("/_catalog/api/")
    )
      writes.push(request.url());
  });
  await login(
    page,
    catalog,
    "local-admin",
    `/reviews/${required(item.submissionId)}`,
  );
  await expect(
    page.getByRole("button", {
      name: "Inspect submitted evidence",
      exact: true,
    }),
  ).toBeVisible();
  for (const width of [1280, 320]) {
    await page.setViewportSize({ width, height: 900 });
    await expectActionGap(
      page.getByRole("link", { name: "Back to reviews", exact: true }),
      page.getByRole("button", {
        name: "Reload review and next steps",
        exact: true,
      }),
    );
  }
  await page.goto(
    `${catalog.origin}${catalog.basePath}/publication/${snapshot.data.id}?host=127.0.0.1`,
  );
  await expect(
    page.getByRole("heading", {
      name: `Generation ${snapshot.data.generation}: candidate`,
      exact: true,
    }),
  ).toBeVisible();
  await expect(page.getByText("Change host", { exact: true })).toBeVisible();
  expect(
    await page
      .getByText("Change host", { exact: true })
      .evaluate((node) => node.closest("details")!.open),
  ).toBe(false);
  for (const width of [1280, 320]) {
    await page.setViewportSize({ width, height: 900 });
    await expectActionGap(
      page.getByRole("link", { name: "Back to host", exact: true }),
      page.getByRole("button", { name: "Reload snapshot state", exact: true }),
    );
  }
  expect(writes).toEqual([]);
});

test("release sunset fits enlarged narrow text and confirms the exact UTC instant without sending on cancel", async ({
  page,
  catalog,
}) => {
  const item = required(
    catalog.receipt.items.find((entry) => entry.key === "order-agent"),
  );
  const writes: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() !== "GET" &&
      new URL(request.url()).pathname.includes("/_catalog/api/")
    )
      writes.push(request.url());
  });
  await login(
    page,
    catalog,
    "local-admin",
    `/releases/${required(item.releaseId)}`,
  );
  const sunset = page.getByLabel("Sunset time (optional, local time)", {
    exact: true,
  });
  await expect(sunset).toBeEnabled();
  await sunset.fill("2026-10-01T12:30");
  const expectedUTC = await sunset.evaluate((node) =>
    new Date((node as HTMLInputElement).value).toISOString(),
  );
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Inspect a planned sunset without changing the release");
  await page.setViewportSize({ width: 320, height: 844 });
  await page.evaluate(() => {
    document.documentElement.dataset.theme = "dark";
    document.documentElement.dataset.density = "compact";
  });
  await doubleText(page);
  await expect
    .poll(() => page.evaluate(() => document.documentElement.scrollWidth))
    .toBeLessThanOrEqual(320);
  const box = required((await sunset.boundingBox()) ?? undefined);
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(320);
  await sunset.focus();
  const prepare = page.getByRole("button", {
    name: "Change release disposition",
    exact: true,
  });
  // Native datetime controls can expose separate date/time segment tab stops.
  // Exercise those actual keyboard stops before reaching the submit action.
  for (let stops = 0; stops < 12; stops++) {
    await page.keyboard.press("Tab");
    if (await prepare.evaluate((node) => node === document.activeElement))
      break;
  }
  await expect(prepare).toBeFocused();
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByLabel("Command summary")).toContainText(expectedUTC);
  await expect(sunset).toBeDisabled();
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect(sunset).toHaveValue("2026-10-01T12:30");
  await expect(sunset).toBeEnabled();
  expect(writes).toEqual([]);
});
