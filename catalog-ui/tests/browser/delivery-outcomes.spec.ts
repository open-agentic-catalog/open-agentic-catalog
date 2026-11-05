// Controlled histories qualify presentation states; packaged journeys own worker/receiver behavior.
import { test, expect, login } from "./fixtures";
import { doubleText } from "./text-layout";

test.use({ webhooks: true });
test("documentation shows four non-secret delivery outcomes before evidence is opened", async ({
  page,
  catalog,
}) => {
  const id = "00000000-0000-7000-8000-000000000099";
  const stamp = "2026-09-01T12:00:00Z";
  await page.route(`**/_catalog/api/v1/webhooks/${id}`, (route) =>
    route.fulfill({
      json: {
        id,
        name: "Publication report",
        url: "https://receiver.example.test/events",
        eventTypes: ["*"],
        status: "active",
        revision: 1,
        secretVersion: 1,
        createdAt: stamp,
        updatedAt: stamp,
      },
    }),
  );
  const states = ["pending", "waiting", "succeeded", "dead_letter"];
  await page.route(`**/_catalog/api/v1/webhooks/${id}/deliveries?*`, (route) =>
    route.fulfill({
      json: {
        items: states.map((state, index) => ({
          id: `delivery-${index}`,
          eventId: `event-${index}`,
          eventType: "com.open.agentic.catalog.resource.created.v1",
          state,
          attempt: index,
          updatedAt: stamp,
          ...(state === "waiting"
            ? {
                nextAttemptAt: "2026-09-01T12:01:00Z",
                responseStatus: 503,
                safeError: "Receiver returned HTTP 503.",
              }
            : {}),
          ...(state === "succeeded" ? { responseStatus: 204 } : {}),
          ...(state === "dead_letter"
            ? {
                responseStatus: 500,
                safeError: "The automatic retry limit was reached.",
              }
            : {}),
        })),
      },
    }),
  );
  await login(page, catalog, "local-admin", `/webhooks/${id}`);
  const history = page.locator("section.workspace-panel").filter({
    has: page.getByRole("heading", { name: "Delivery history", exact: true }),
  });
  await expect(history.locator("article")).toHaveCount(4);
  for (const state of [
    "Pending",
    "Waiting",
    "Succeeded",
    "Automatic retries stopped",
  ])
    await expect(
      history.getByRole("heading", {
        name: `Resource created: ${state}`,
        exact: true,
      }),
    ).toBeVisible();
  await expect(
    history.getByRole("group", { name: "JSON document" }),
  ).toHaveCount(0);
  await page.setViewportSize({ width: 1440, height: 3000 });
  await history.screenshot({
    path: test.info().outputPath("webhook-delivery-history.png"),
  });
});

test("delivery outcomes retain literal bounded evidence, keyboard copying and narrow layout", async ({
  page,
  catalog,
  context,
}) => {
  const id = "00000000-0000-7000-8000-000000000099";
  const stamp = "2026-09-01T12:00:00Z";
  const rows = ["pending", "waiting", "succeeded", "dead_letter"].map(
    (state, index) => ({
      id: `delivery-${index}`,
      eventId: `event-${index}`,
      eventType: "com.open.agentic.catalog.resource.created.v1",
      state,
      attempt: index,
      updatedAt: stamp,
      ...(state === "waiting"
        ? {
            nextAttemptAt: stamp,
            responseStatus: 503,
            safeError: "<script>literal</script> " + "receiver ".repeat(200),
          }
        : {}),
      ...(state === "succeeded" ? { responseStatus: 204 } : {}),
    }),
  );
  await page.route(`**/_catalog/api/v1/webhooks/${id}`, (route) =>
    route.fulfill({
      json: {
        id,
        name: "Owned delivery history",
        url: "https://example.test/events",
        eventTypes: ["*"],
        status: "active",
        revision: 1,
        secretVersion: 1,
        createdAt: stamp,
        updatedAt: stamp,
      },
    }),
  );
  let reads = 0;
  await page.route(
    `**/_catalog/api/v1/webhooks/${id}/deliveries?*`,
    (route) => {
      reads++;
      return route.fulfill({ json: { items: rows } });
    },
  );
  await login(page, catalog, "local-admin", `/webhooks/${id}`);
  const histories = page.locator("article");
  await expect(histories).toHaveCount(4);
  const labels = [
    "Pending",
    "Waiting",
    "Succeeded",
    "Automatic retries stopped",
  ];
  for (const [index, label] of labels.entries()) {
    await expect(histories.nth(index).getByRole("heading")).toHaveText(
      `Resource created: ${label}`,
    );
    await expect(
      histories
        .nth(index)
        .getByRole("button", { name: "Copy event ID", exact: true }),
    ).toBeVisible();
  }
  await expect(histories.nth(1)).toContainText("<script>literal</script>");
  await expect(histories.nth(1).locator("script")).toHaveCount(0);
  await expect(
    histories.nth(0).getByText("Not reported", { exact: true }),
  ).toHaveCount(2);
  await context.grantPermissions(["clipboard-read", "clipboard-write"], {
    origin: catalog.origin,
  });
  await histories
    .nth(1)
    .getByRole("button", { name: "Copy event ID", exact: true })
    .focus();
  await page.keyboard.press("Enter");
  await expect(
    histories.nth(1).getByText("Copied.", { exact: true }),
  ).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
    "event-1",
  );
  expect(reads).toBe(1);
  await page.setViewportSize({ width: 390, height: 844 });
  await doubleText(page);
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await histories
    .nth(1)
    .getByText("Complete delivery evidence", { exact: true })
    .focus();
  await page.keyboard.press("Enter");
  await expect(
    histories.nth(1).getByRole("group", { name: "JSON document" }),
  ).toContainText('"responseStatus": 503');
  await expect(
    histories.nth(1).getByRole("group", { name: "JSON document" }),
  ).toContainText('"state": "waiting"');
  expect(reads).toBe(1);
});
