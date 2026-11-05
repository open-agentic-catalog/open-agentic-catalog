import { openDisclosure, revealControl } from "./editor-disclosure";
import { object, array, text } from "./json";
import { readFile } from "node:fs/promises";
import { test, expect, login, signOut } from "./fixtures";

test.use({ timezoneId: "UTC" });

test("audit pages use real cursors and recover from a failed page and empty filters", async ({
  page,
  catalog,
  network,
}) => {
  // One seeded namespace plus 25 new ones crosses the actual page boundary.
  for (let index = 0; index < 25; index++) {
    await catalog.admin("POST", "/api/v1/namespaces", {
      slug: `audit-page-${index}`,
      displayName: `Audit page ${index}`,
    });
  }
  const expected = await catalog.admin<{ items: { id: string }[] }>(
    "GET",
    "/api/v1/audit-events?action=namespace.created&limit=100",
  );
  expect(expected.data.items).toHaveLength(26);
  await login(page, catalog, "local-admin", "/audit");
  // Unbounded custom search avoids a wall-clock assumption. Export remains
  // disabled until a bounded range is explicitly supplied.
  await page
    .getByRole("combobox", { name: "Time range", exact: true })
    .selectOption("custom");
  await page
    .getByRole("combobox", { name: "Activity", exact: true })
    .selectOption("custom");
  await page
    .getByLabel("Exact action", { exact: true })
    .fill("namespace.created");
  await page.getByRole("button", { name: "Search audit events" }).click();
  const entries = page.locator(".audit-entry");
  await expect(entries).toHaveCount(25);
  const eventIds = () =>
    entries.locator("pre").evaluateAll((blocks) =>
      blocks.map((block) => {
        const value: unknown = JSON.parse(block.textContent!);
        if (
          !value ||
          typeof value !== "object" ||
          !("id" in value) ||
          typeof value.id !== "string"
        )
          throw new Error("Expected audit event ID");
        return value.id;
      }),
    );
  const first = await eventIds();
  await openDisclosure(page, "Export matching events");
  await expect(
    page.getByRole("button", { name: "Prepare audit export" }),
  ).toBeDisabled();
  network.expectFailure("GET", "/_catalog/api/v1/audit-events", 503);
  await page.route("**/_catalog/api/v1/audit-events?**", async (route) => {
    expect(
      new URL(route.request().url()).searchParams.get("cursor"),
    ).toBeTruthy();
    await route.fulfill({
      status: 503,
      contentType: "application/problem+json",
      body: JSON.stringify({
        title: "Audit temporarily unavailable",
        status: 503,
        code: "unavailable",
      }),
    });
    await page.unroute("**/_catalog/api/v1/audit-events?**");
  });
  await page.getByRole("button", { name: "Next page", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText(
    "The request could not be completed. Please try again.",
  );
  await expect(entries).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "First page", exact: true }),
  ).toBeEnabled();
  const restart = page.waitForResponse(
    (reply) =>
      new URL(reply.url()).pathname === "/_catalog/api/v1/audit-events" &&
      reply.status() === 200,
  );
  await page.getByRole("button", { name: "Reload", exact: true }).click();
  const restarted = new URL((await restart).url());
  expect(restarted.searchParams.has("cursor")).toBe(false);
  expect(restarted.searchParams.get("action")).toBe("namespace.created");
  await expect(entries).toHaveCount(25);
  expect(await eventIds()).toEqual(first);
  await page.getByRole("button", { name: "Next page", exact: true }).click();
  await expect(entries).toHaveCount(1);
  const second = await eventIds();
  expect(first).not.toContain(second[0]);
  expect([...first, ...second].sort()).toEqual(
    expected.data.items.map((event) => event.id).sort(),
  );
  await expect(
    page.getByRole("button", { name: "Next page", exact: true }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "First page", exact: true }).click();
  await expect(entries).toHaveCount(25);
  expect(await eventIds()).toEqual(first);
  await page.getByRole("button", { name: "Next page", exact: true }).click();
  await expect(entries).toHaveCount(1);
  await page
    .getByRole("combobox", { name: "Activity", exact: true })
    .selectOption("resource.deleted");
  await page.getByRole("button", { name: "Search audit events" }).click();
  await expect(
    page.getByText(/No matching events\. Try a longer time range/),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "First page", exact: true }),
  ).toHaveCount(0);
  await page
    .getByRole("combobox", { name: "Activity", exact: true })
    .selectOption("resource.created");
  await page.getByRole("button", { name: "Search audit events" }).click();
  await expect(entries).toHaveCount(5);
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("audit filters are explicit and export preserves the exact server bytes", async ({
  page,
  catalog,
  network,
}) => {
  const seeded = await catalog.admin<{ items: { at: string }[] }>(
    "GET",
    "/api/v1/audit-events?action=resource.created",
  );
  expect(seeded.data.items).toHaveLength(5);
  // A preset is a browser-clock interval, not a server-clock synchronization
  // contract. Anchor this fixture to its actual events so host clock corrections
  // cannot move the exclusive upper bound behind the just-created seed.
  const end =
    Math.max(...seeded.data.items.map((event) => Date.parse(event.at))) + 1000;
  await page.clock.setFixedTime(new Date(end));
  const requests: string[] = [];
  page.on("request", (request) => {
    if (/\/_catalog\/api\/v1\/audit-(events|exports)/.test(request.url()))
      requests.push(request.url());
  });
  await login(page, catalog, "local-admin", "/audit");
  await expect(
    page.getByRole("button", { name: "Search audit events" }),
  ).toBeVisible();
  expect(requests).toEqual([]);
  await page
    .getByRole("combobox", { name: "Time range", exact: true })
    .selectOption("custom");
  await page
    .getByLabel("Occurred from (inclusive, local time)", { exact: true })
    .fill("2026-01-02T00:00");
  await page
    .getByLabel("Occurred before (exclusive, local time)", { exact: true })
    .fill("2026-01-01T00:00");
  await page.getByRole("button", { name: "Search audit events" }).click();
  await expect(page.getByRole("alert")).toContainText(
    "The end time must follow the start time.",
  );
  expect(requests).toEqual([]);
  await page
    .getByRole("combobox", { name: "Time range", exact: true })
    .selectOption("day");

  await expect(
    page.getByRole("combobox", { name: "Time range", exact: true }),
  ).toHaveValue("day");
  await page
    .getByText("Advanced filters: person, request or object", { exact: true })
    .click();
  await page
    .getByRole("textbox", { name: "Target type", exact: true })
    .fill("resource");
  await page.getByRole("button", { name: "Search audit events" }).click();
  await expect(page.getByRole("alert")).toHaveText(
    "Supply both target type and target ID.",
  );
  expect(requests).toEqual([]);
  await page
    .getByRole("textbox", { name: "Target type", exact: true })
    .fill("");
  await page
    .getByRole("combobox", { name: "Activity", exact: true })
    .selectOption("resource.created");
  // Reproduce the old empty-preset condition without moving the host clock:
  // if the browser's end precedes the events, an empty result is correct.
  await page.clock.setFixedTime(
    new Date(
      Math.min(...seeded.data.items.map((event) => Date.parse(event.at))) -
        1000,
    ),
  );
  const earlier = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === "/_catalog/api/v1/audit-events",
  );
  await page.getByRole("button", { name: "Search audit events" }).click();
  const earlierReply = await earlier;
  expect(earlierReply.status()).toBe(200);
  expect(object(await earlierReply.json()).items).toEqual([]);
  await expect(
    page.getByText(/No matching events\. Try a longer time range/),
  ).toBeVisible();
  await page.clock.setFixedTime(new Date(end));
  const searched = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === "/_catalog/api/v1/audit-events",
  );
  await page.getByRole("button", { name: "Search audit events" }).click();
  const reply = await searched;
  expect(reply.status()).toBe(200);
  const seededEvents = array(object(await reply.json()).items).map((item) => ({
    at: text(object(item).at),
  }));
  expect(seededEvents).toHaveLength(5);
  const query = new URL(reply.url()).searchParams;
  expect(query.get("occurredBefore")).toBe(new Date(end).toISOString());
  expect(query.get("occurredFrom")).toBe(
    new Date(end - 24 * 3600000).toISOString(),
  );
  const events = page.locator("section").filter({
    has: page.getByRole("heading", { name: "Audit events", exact: true }),
  });
  await expect(events.locator(".audit-entry > details > summary")).toHaveCount(
    5,
  );
  await openDisclosure(page, "Export matching events");
  await expect(
    page.getByRole("button", { name: "Prepare audit export" }),
  ).toBeEnabled();

  // Use the same bounded fixture interval for custom filtering and export.
  const start =
    Math.min(...seededEvents.map((event) => Date.parse(event.at))) - 1000;
  // datetime-local canonicalizes fractional trailing zeros (.260 becomes .26).
  // Playwright checks the assigned value verbatim, so use that canonical form.
  const localTime = (time: number) =>
    new Date(time)
      .toISOString()
      .slice(0, 23)
      .replace(/\.?0+$/, "")
      .replace(/:00$/, "");
  await page
    .getByRole("combobox", { name: "Time range", exact: true })
    .selectOption("custom");
  await page
    .getByLabel("Occurred from (inclusive, local time)", { exact: true })
    .fill(localTime(start));
  await page
    .getByLabel("Occurred before (exclusive, local time)", { exact: true })
    .fill(localTime(end));
  await page.getByRole("button", { name: "Search audit events" }).click();
  await expect(events.locator(".audit-entry > details > summary")).toHaveCount(
    5,
  );
  const beforeRepeat = requests.length;
  await page.getByRole("button", { name: "Search audit events" }).click();
  await expect.poll(() => requests.length).toBe(beforeRepeat + 1);
  await expect(events.locator(".audit-entry > details > summary")).toHaveCount(
    5,
  );
  await revealControl(page.getByLabel("Reason", { exact: true }));
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Verify synthetic fixture audit export.");
  const beforeDiscard = requests.length;
  page.once("dialog", (dialog) => dialog.dismiss());
  await page.getByRole("button", { name: "Search audit events" }).click();
  await expect(
    page.getByRole("textbox", { name: "Reason", exact: true }),
  ).toHaveValue("Verify synthetic fixture audit export.");
  expect(requests).toHaveLength(beforeDiscard);
  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: "Search audit events" }).click();
  await expect.poll(() => requests.length).toBe(beforeDiscard + 1);
  await openDisclosure(page, "Export matching events");
  await expect(
    page.getByRole("textbox", { name: "Reason", exact: true }),
  ).toHaveValue("");
  // Searches may include future times; exports deliberately cannot. Exercise
  // the real rejection and correction, without retrying a rejected mutation.
  const exportCutoffDetail =
    "audit-export occurredBefore must not be after the server cutoff";
  await page
    .getByLabel("Occurred before (exclusive, local time)", { exact: true })
    .fill(localTime(end + 3600000));
  const futureSearch = page.waitForResponse(
    (reply) =>
      new URL(reply.url()).pathname === "/_catalog/api/v1/audit-events",
  );
  await page.getByRole("button", { name: "Search audit events" }).click();
  expect((await futureSearch).status()).toBe(200);
  await revealControl(page.getByLabel("Reason", { exact: true }));
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Check future export rejection.");
  network.expectFailure("POST", "/_catalog/api/v1/audit-exports", 400);
  const rejected = page.waitForResponse(
    (reply) =>
      new URL(reply.url()).pathname === "/_catalog/api/v1/audit-exports",
  );
  await page.getByRole("button", { name: "Prepare audit export" }).click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  const rejection = await rejected;
  expect(rejection.status()).toBe(400);
  expect(await rejection.json()).toMatchObject({
    code: "INVALID_ARGUMENT",
    detail: exportCutoffDetail,
  });
  await expect(page.getByRole("dialog").getByRole("alert")).toContainText(
    "The request is no longer valid. Start a new search or check your input.",
  );
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Back to form" })
    .click();
  await page
    .getByLabel("Occurred before (exclusive, local time)", { exact: true })
    .fill(localTime(end));
  page.once("dialog", (dialog) => dialog.accept());
  const corrected = page.waitForResponse(
    (reply) =>
      new URL(reply.url()).pathname === "/_catalog/api/v1/audit-events",
  );
  await page.getByRole("button", { name: "Search audit events" }).click();
  const correctedReply = await corrected;
  expect(correctedReply.status()).toBe(200);
  expect(object(await correctedReply.json()).items).toHaveLength(5);
  expect(new URL(correctedReply.url()).searchParams.get("occurredBefore")).toBe(
    new Date(end).toISOString(),
  );
  // HTTP Date comes from the owned API, not the browser's fixed Date. Wait
  // only on cheap reads until the export interval is in the server's past.
  let observedServerTime = 0;
  await expect
    .poll(
      async () => {
        const { date } = await catalog.admin(
          "GET",
          "/api/v1/namespaces?limit=1",
        );
        expect(date).not.toBeNull();
        observedServerTime = Date.parse(date!);
        return observedServerTime;
      },
      { timeout: 10000 },
    )
    .toBeGreaterThan(end);
  await revealControl(page.getByLabel("Reason", { exact: true }));
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Verify synthetic fixture audit export.");
  let serverBytes: Buffer | undefined;
  // UI-only bound presentation: backend tests own the real row/byte ceiling.
  // Recovery below exports actual server bytes instead of a mocked success.
  network.expectFailure("POST", "/_catalog/api/v1/audit-exports", 422);
  await page.route(
    "**/_catalog/api/v1/audit-exports",
    async (route) => {
      await route.fulfill({
        status: 422,
        contentType: "application/problem+json",
        body: JSON.stringify({
          type: "https://catalog.example.com/problems/audit-export-range-too-large",
          title: "Unprocessable content",
          status: 422,
          code: "AUDIT_EXPORT_RANGE_TOO_LARGE",
          detail: "Narrow the export range.",
        }),
      });
    },
    { times: 1 },
  );
  await page.getByRole("button", { name: "Prepare audit export" }).click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  await expect(page.getByRole("dialog").getByRole("alert")).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Download exact audit export" }),
  ).toHaveCount(0);
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Back to form" })
    .click();
  await expect(
    page.getByRole("textbox", { name: "Reason", exact: true }),
  ).toHaveValue("Verify synthetic fixture audit export.");
  // Forward exactly once to the real host/API and preserve its response. This
  // captures the attachment independently of Chromium's Blob body reporting.
  await page.route("**/_catalog/api/v1/audit-exports", async (route) => {
    const upstream = await route.fetch({ maxRedirects: 0, maxRetries: 0 });
    serverBytes = await upstream.body();
    await route.fulfill({ response: upstream });
  });
  const exported = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/audit-exports") &&
      response.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Prepare audit export" }).click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  const response = await exported;
  expect(object(response.request().postDataJSON())).toMatchObject({
    occurredFrom: new Date(start).toISOString(),
    occurredBefore: new Date(end).toISOString(),
  });
  const exportProblem = response.ok()
    ? undefined
    : object(await response.json());
  expect(
    response.status(),
    `Export end exceeded server cutoff: ${exportProblem?.detail === exportCutoffDetail}; read Date offset: ${observedServerTime - end} ms; export Date offset: ${Date.parse(response.headers().date ?? "") - end} ms`,
  ).toBe(200);
  await expect(page.getByRole("dialog")).not.toBeVisible();
  const downloaded = page.waitForEvent("download");
  await page.getByRole("link", { name: "Download exact audit export" }).click();
  const download = await downloaded;
  expect(download.suggestedFilename()).toBe("catalog-audit-export.json");
  const bytes = await readFile((await download.path())!);
  expect(bytes.length).toBeGreaterThan(0);
  expect(object(JSON.parse(bytes.toString())).events).toHaveLength(5);
  expect(bytes).toEqual(serverBytes);
  await signOut(page);
  await page.locator('[data-login-alias="local-reader"]').click();
  await expect(
    page.getByRole("link", { name: "Download exact audit export" }),
  ).toHaveCount(0);
  const before = requests.length;
  await page.goto(catalog.origin + catalog.basePath + "/audit");
  await expect(page.getByRole("alert")).toContainText(
    "audit.read or audit.export permission",
  );
  expect(requests).toHaveLength(before);
});
