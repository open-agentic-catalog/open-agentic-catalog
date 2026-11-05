import AxeBuilder from "@axe-core/playwright";
import { test, expect, login } from "./fixtures";
import type { components } from "../../src/api/schema";

test.use({ webhooks: true, screenshot: "off" });

for (const base of ["root", "subpath"]) {
  test(`${base}: webhook-only Operations summaries use actual production counts and exclude test sends`, async ({
    page,
    catalog,
    network,
  }) => {
    const created = await catalog.admin<
      components["schemas"]["WebhookSecretResult"]
    >("POST", "/api/v1/webhooks", {
      name: "Overview receiver",
      url: "https://127.0.0.1/events",
      // Fixture startup creates records, but this journey never deletes one.
      // Keep production notifications separate from the explicit synthetic send.
      eventTypes: ["resource.deleted"],
    });
    const endpoint = created.data.endpoint;
    // The owned fixture denies this loopback destination before network delivery.
    const sent = await catalog.admin<components["schemas"]["WebhookDelivery"]>(
      "POST",
      `/api/v1/webhooks/${endpoint.id}:test`,
      {},
      created.etag,
    );
    expect(sent.data.eventType).toBe(
      "com.open.agentic.catalog.webhook.test.v1",
    );
    const reads: string[] = [];
    const mutations: string[] = [];
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (path.includes("/_catalog/api/v1/overview/"))
        reads.push(path.split("/").at(-1)!);
      if (
        path.includes("/_catalog/api/") &&
        !["GET", "HEAD"].includes(request.method())
      )
        mutations.push(path);
    });
    const endpointRead = page.waitForResponse((r) =>
      new URL(r.url()).pathname.endsWith("/api/v1/overview/webhook-endpoints"),
    );
    const deliveryRead = page.waitForResponse((r) =>
      new URL(r.url()).pathname.endsWith("/api/v1/overview/webhook-deliveries"),
    );
    await login(page, catalog, "local-template-webhook-admin", "/overview");
    const ep = await endpointRead,
      delivery = await deliveryRead;
    expect(ep.status()).toBe(200);
    expect(delivery.status()).toBe(200);
    expect(ep.headers()["cache-control"]).toBe("no-store");
    expect(delivery.headers()["cache-control"]).toBe("no-store");
    const endpoints =
      (await ep.json()) as components["schemas"]["WebhookEndpointOverview"];
    const deliveries =
      (await delivery.json()) as components["schemas"]["WebhookDeliveryOverview"];
    expect(endpoints.total.value).toBe(1);
    expect(deliveries.total.value).toBe(0);
    expect(deliveries.failedEndpoints).toEqual([]);
    await expect(
      page.getByRole("link", { name: "Operations", exact: true }),
    ).toHaveAttribute("aria-current", "page");
    await expect(
      page
        .getByRole("region", { name: "Webhook endpoints", exact: true })
        .locator(".overview-number"),
    ).toHaveText("1");
    await expect(page.getByText("No deliveries to show.")).toBeVisible();
    await expect(
      page.getByRole("region", {
        name: "Notification totals",
        exact: true,
      }),
    ).toBeVisible();
    await expect.poll(() => reads.length).toBe(2);
    expect(reads.sort()).toEqual(["webhook-deliveries", "webhook-endpoints"]);
    expect(mutations).toEqual([]);
    expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
      violations: [],
      incomplete: [],
    });
    await page.setViewportSize({ width: 390, height: 900 });
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(390);
    await page.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect.poll(() => reads.length).toBe(4);
    const directory = page.waitForRequest((request) =>
      new URL(request.url()).pathname.endsWith("/api/v1/webhooks"),
    );
    await page
      .getByRole("link", { name: "Active", exact: true })
      .first()
      .click();
    expect(new URL((await directory).url()).searchParams.get("status")).toBe(
      "active",
    );
    await expect(
      page.getByRole("combobox", { name: "Status", exact: true }),
    ).toHaveValue("active");
    await page.reload();
    await expect(
      page.getByRole("combobox", { name: "Status", exact: true }),
    ).toHaveValue("active");
    await page
      .getByRole("button", { name: "Clear endpoint filters", exact: true })
      .click();
    await expect(page).toHaveURL(new RegExp(`${catalog.basePath}/webhooks$`));
    await page.goBack();
    await expect(
      page.getByRole("combobox", { name: "Status", exact: true }),
    ).toHaveValue("active");
    await expect(
      page.getByRole("link", { name: "Overview receiver", exact: true }),
    ).toBeVisible();
    const exact = `/webhooks/${endpoint.id}?deliveryId=${sent.data.id}&state=${sent.data.state}`;
    await page.goto(catalog.origin + catalog.basePath + exact);
    await expect(
      page.getByLabel("Exact delivery ID", { exact: true }),
    ).toHaveValue(sent.data.id);
    await expect(
      page.getByRole("combobox", { name: "Delivery state", exact: true }),
    ).toHaveValue(sent.data.state);
    await expect(
      page.getByRole("button", { name: "Copy delivery ID", exact: true }),
    ).toHaveCount(1);
    await page.reload();
    await expect(
      page.getByRole("button", { name: "Copy delivery ID", exact: true }),
    ).toHaveCount(1);
    await page
      .getByRole("button", { name: "Clear delivery filters", exact: true })
      .click();
    await expect(page).toHaveURL(new RegExp(`/webhooks/${endpoint.id}$`));
    await page.goBack();
    await expect(
      page.getByLabel("Exact delivery ID", { exact: true }),
    ).toHaveValue(sent.data.id);
    network.expectFailure(
      "GET",
      `/_catalog/api/v1/webhooks/${endpoint.id}/deliveries`,
      400,
    );
    await page.goto(
      catalog.origin +
        catalog.basePath +
        `/webhooks/${endpoint.id}?state=waiting&state=dead_letter`,
    );
    await expect(
      page.getByRole("option", { name: "Unsupported state", exact: true }),
    ).toHaveCount(1);
    await expect(page.getByRole("alert")).toBeVisible();
    expect(mutations).toEqual([]);
  });
}
