import { openDisclosure } from "./editor-disclosure";
import { writeFile } from "node:fs/promises";
import AxeBuilder from "@axe-core/playwright";
import { test, expect, login, confirmCommand } from "./fixtures";
import type { components } from "../../src/api/schema";

test.use({ timezoneId: "America/New_York" });
type Event = components["schemas"]["AuditEvent"];
type Policy = components["schemas"]["TenantSettings"];
type Grant = { id: string; revision: number };

test("a delayed replay receipt keeps the retry attempt separate and requires a wider interval for the original change", async ({
  page,
  catalog,
  network,
}) => {
  await login(page, catalog, "local-admin", "/settings");
  await page
    .getByRole("combobox", { name: "Automatic source sync", exact: true })
    .selectOption("paused");
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Owned delayed replay investigation");
  const attempts: {
    key: string | undefined;
    body: string | null;
    tag: string | undefined;
    requestId: string | undefined;
  }[] = [];
  let committed: Policy | undefined;
  network.expectFailure("POST", "/_catalog/api/v1/tenant-settings", 503);
  await page.route("**/_catalog/api/v1/tenant-settings", async (route) => {
    if (route.request().method() !== "POST") {
      await route.continue();
      return;
    }
    const reply = await route.fetch({ maxRetries: 0 });
    expect(reply.status()).toBe(200);
    attempts.push({
      key: route.request().headers()["idempotency-key"],
      body: route.request().postData(),
      tag: route.request().headers()["if-match"],
      requestId: reply.headers()["x-request-id"],
    });
    committed = (await reply.json()) as Policy;
    if (attempts.length === 1)
      await route.fulfill({
        status: 503,
        contentType: "application/problem+json",
        body: JSON.stringify({ code: "UNAVAILABLE" }),
      });
    else {
      expect(reply.headers()["idempotency-replayed"]).toBe("true");
      await route.fulfill({ response: reply });
    }
  });
  await page
    .getByRole("button", { name: "Save settings", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "Retry same command", exact: true }),
  ).toBeVisible();
  expect(attempts).toHaveLength(1);
  const original = await catalog.admin<{ items: Event[] }>(
    "GET",
    "/api/v1/audit-events?action=tenant.settings.changed&limit=100",
  );
  expect(original.data.items).toHaveLength(1);
  const event = original.data.items[0]!;
  expect(event.requestId).toBe(attempts[0]!.requestId);
  // Model delayed acknowledgement only in the browser. Host/server clocks are unchanged.
  // Keep the replay acknowledgement later than the retained event on any date.
  // The repeated DST hour has a separate isolated regression.
  const delayedAcknowledgement = new Date(
    Date.parse(event.at) + 10 * 60000,
  ).toISOString();
  await page.clock.setFixedTime(new Date(delayedAcknowledgement));
  await page
    .getByRole("button", { name: "Retry same command", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(attempts).toHaveLength(2);
  expect(attempts[1]).toMatchObject({
    key: attempts[0]!.key,
    body: attempts[0]!.body,
    tag: attempts[0]!.tag,
  });
  expect(attempts[1]!.requestId).not.toBe(event.requestId);
  const receipt = page.getByRole("region", {
    name: "Acknowledged administration change",
    exact: true,
  });
  await expect(receipt).toContainText(attempts[1]!.requestId!);
  await receipt
    .getByRole("link", { name: "Investigate change", exact: true })
    .click();
  const initialSearch = page.waitForResponse((reply) =>
    new URL(reply.url()).pathname.endsWith("/api/v1/audit-events"),
  );
  await page
    .getByRole("button", { name: "Search audit events", exact: true })
    .click();
  expect(
    new URL((await initialSearch).url()).searchParams.get("occurredBefore"),
  ).toBe(delayedAcknowledgement);
  await expect(page.getByText(/No matching events/)).toBeVisible();
  await page
    .getByLabel("Occurred from (inclusive, local time)", { exact: true })
    .fill(
      new Date(Date.parse(event.at) - 60000)
        .toLocaleString("sv-SE", { timeZone: "America/New_York" })
        .replace(" ", "T")
        .replace(/:00$/, ""),
    );
  const found = page.waitForResponse((reply) =>
    new URL(reply.url()).pathname.endsWith("/api/v1/audit-events"),
  );
  await page
    .getByRole("button", { name: "Search audit events", exact: true })
    .click();
  expect(
    ((await (await found).json()) as { items: Event[] }).items.map(
      (item) => item.id,
    ),
  ).toEqual([event.id]);
  expect(
    (await catalog.admin<Policy>("GET", "/api/v1/tenant-settings")).data,
  ).toEqual(committed);
  expect(attempts).toHaveLength(2);
});

test("exact audit presets distinguish independently committed named, direct and group assignment changes", async ({
  page,
  catalog,
}) => {
  const members = await catalog.admin<{
    items: { id: string; subject: string }[];
  }>("GET", "/api/v1/memberships?limit=100");
  const member = members.data.items.find(
    (item) => item.subject === "local-policy",
  )!;
  const named = await catalog.admin<Grant>("POST", "/api/v1/role-assignments", {
    subject: "local-no-role",
    actorType: "human",
    role: "reader",
    reason: "Owned named-template control",
  });
  await catalog.admin(
    "POST",
    `/api/v1/role-assignments/${named.data.id}:revoke`,
    { reason: "Owned named-template revoke" },
    `"rev-${named.data.revision}"`,
  );
  const role = await catalog.admin<Grant>("POST", "/api/v1/access/roles", {
    name: "Audit preset controls",
    permissions: ["settings.read"],
    reason: "Owned role",
  });
  const group = await catalog.admin<Grant>("POST", "/api/v1/access/groups", {
    name: "Audit preset group",
    reason: "Owned group",
  });
  await catalog.admin(
    "POST",
    `/api/v1/access/groups/${group.data.id}/members`,
    { membershipId: member.id, reason: "Owned group membership" },
  );
  const direct = await catalog.admin<Grant>(
    "POST",
    "/api/v1/access/assignments",
    {
      roleDefinitionId: role.data.id,
      membershipId: member.id,
      reason: "Owned direct control",
    },
  );
  const grouped = await catalog.admin<Grant>(
    "POST",
    "/api/v1/access/assignments",
    {
      roleDefinitionId: role.data.id,
      groupId: group.data.id,
      reason: "Owned group control",
    },
  );
  for (const grant of [direct, grouped])
    await catalog.admin(
      "POST",
      `/api/v1/access/assignments/${grant.data.id}:revoke`,
      { reason: "Owned general revoke" },
      `"rev-${grant.data.revision}"`,
    );
  const expected = await catalog.admin<{ items: Event[] }>(
    "GET",
    "/api/v1/audit-events?limit=100",
  );
  await catalog.admin("POST", "/api/v1/audit-exports", {
    occurredFrom: "2020-01-01T00:00:00Z",
    occurredBefore: "2020-01-02T00:00:00Z",
    reason: "Owned audit access event control",
  });
  for (const action of [
    "identity.role_assignment.granted",
    "identity.role_assignment.revoked",
    "authorization.assignment.granted",
    "authorization.assignment.revoked",
  ]) {
    const ids = action.startsWith("identity.")
      ? [named.data.id]
      : [direct.data.id, grouped.data.id];
    expect(
      expected.data.items
        .filter(
          (event) => event.action === action && ids.includes(event.targetId),
        )
        .map((event) => event.targetId)
        .sort(),
    ).toEqual([...ids].sort());
  }
  await login(page, catalog, "local-admin", "/audit");
  await page
    .getByRole("combobox", { name: "Time range", exact: true })
    .selectOption("custom");
  for (const [action, label] of [
    ["identity.role_assignment.granted", "Named-template access granted"],
    ["identity.role_assignment.revoked", "Named-template access revoked"],
    ["authorization.assignment.granted", "Role or group access assigned"],
    ["authorization.assignment.revoked", "Role or group access revoked"],
    ["audit.events_read", "Audit events read"],
    ["audit.exported", "Audit exported"],
  ]) {
    await page
      .getByRole("combobox", { name: "Activity", exact: true })
      .selectOption(action!);
    const searched = page.waitForResponse((reply) =>
      new URL(reply.url()).pathname.endsWith("/api/v1/audit-events"),
    );
    await page.getByRole("button", { name: "Search audit events" }).click();
    const response = await searched;
    expect(response.status()).toBe(200);
    const data = (await response.json()) as { items: Event[] };
    expect(data.items.length).toBeGreaterThan(0);
    expect(data.items.every((event) => event.action === action)).toBe(true);
    if (action!.startsWith("identity.role_assignment."))
      expect(data.items.map((event) => event.targetId)).toContain(
        named.data.id,
      );
    if (action!.startsWith("authorization.assignment."))
      for (const grant of [direct, grouped])
        expect(data.items.map((event) => event.targetId)).toContain(
          grant.data.id,
        );
    await expect(page.locator(".audit-entry strong").first()).toHaveText(
      label!,
    );
  }
});

for (const deployment of ["root", "subpath"]) {
  test(`${deployment}: paged Settings history restores a new draft and receipt navigation stages exact private audit criteria`, async ({
    page,
    catalog,
    network,
  }, info) => {
    // Real shared runtime wiring retains Settings and access evidence together.
    let current = await catalog.admin<Policy>("GET", "/api/v1/tenant-settings");
    for (let index = 0; index < 22; index++)
      current = await catalog.admin<Policy>(
        "POST",
        "/api/v1/tenant-settings",
        {
          overrides: {
            automaticSourceSyncEnabled: false,
            newJobMaxAttempts: index % 2 ? 5 : 2,
          },
          reason: `Owned history transition ${index + 1}`,
        },
        current.etag,
      );
    const history = await catalog.admin<{ items: Event[] }>(
      "GET",
      `/api/v1/audit-events?${new URLSearchParams({ action: "tenant.settings.changed", targetType: "tenant_settings", targetId: current.data.tenantId, limit: "100" })}`,
    );
    expect(history.data.items).toHaveLength(22);
    const reads: string[] = [];
    const writes: { body: unknown; tag: string | undefined }[] = [];
    page.on("request", (request) => {
      if (request.url().includes("/api/v1/audit-events"))
        reads.push(request.url());
      if (
        request.method() === "POST" &&
        new URL(request.url()).pathname.endsWith("/api/v1/tenant-settings")
      )
        writes.push({
          body: request.postDataJSON(),
          tag: request.headers()["if-match"],
        });
    });
    await login(page, catalog, "local-admin", "/settings");
    expect(reads).toEqual([]);
    await page
      .getByRole("button", { name: "Read settings history", exact: true })
      .click();
    const region = page.getByRole("region", {
      name: "Settings history",
      exact: true,
    });
    await expect(region.locator("article")).toHaveCount(20);
    await region
      .getByRole("button", { name: "Next page", exact: true })
      .click();
    await expect(region.locator("article")).toHaveCount(2);
    const historical = region.locator("article").filter({
      has: page.getByRole("heading", { name: "Revision 0 → 1", exact: true }),
    });
    await expect(historical).toContainText("Owned history transition 1");
    await page
      .getByRole("textbox", { name: "Reason", exact: true })
      .fill("Retain pending draft until confirmed");
    page.once("dialog", (dialog) => dialog.dismiss());
    await historical
      .getByRole("button", {
        name: "Use revision 1 values in draft",
        exact: true,
      })
      .click();
    await expect(
      page.getByRole("textbox", { name: "Reason", exact: true }),
    ).toHaveValue("Retain pending draft until confirmed");
    expect(writes).toEqual([]);
    page.once("dialog", (dialog) => dialog.accept());
    await historical
      .getByRole("button", {
        name: "Use revision 1 values in draft",
        exact: true,
      })
      .click();
    await expect(
      page.getByText(/Historical overrides are staged/),
    ).toBeVisible();
    await expect(
      page.getByRole("textbox", { name: "Reason", exact: true }),
    ).toHaveValue("");
    await expect(
      page.getByRole("spinbutton", {
        name: "New Job maximum attempts",
        exact: true,
      }),
    ).toHaveValue("2");
    await expect(
      page.getByRole("button", { name: "Save settings", exact: true }),
    ).toBeDisabled();
    expect(writes).toEqual([]);
    // A concurrent writer invalidates this freshly captured base as usual.
    const competing = await catalog.admin<Policy>(
      "POST",
      "/api/v1/tenant-settings",
      {
        overrides: { newJobMaxAttempts: 3 },
        reason: "Owned concurrent history writer",
      },
      current.etag,
    );
    await page
      .getByRole("textbox", { name: "Reason", exact: true })
      .fill("Restore as a fresh change");
    network.expectFailure("POST", "/_catalog/api/v1/tenant-settings", 412);
    await page
      .getByRole("button", { name: "Save settings", exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    await expect(
      page
        .getByRole("dialog")
        .getByRole("button", { name: "Back to form", exact: true }),
    ).toBeVisible();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Back to form", exact: true })
      .click();
    expect(writes).toHaveLength(1);
    expect(
      (await catalog.admin<Policy>("GET", "/api/v1/tenant-settings")).data,
    ).toEqual(competing.data);
    page.once("dialog", (dialog) => dialog.accept());
    await historical
      .getByRole("button", {
        name: "Use revision 1 values in draft",
        exact: true,
      })
      .click();
    await expect(
      page.getByRole("textbox", { name: "Reason", exact: true }),
    ).toHaveValue("");
    await page
      .getByRole("textbox", { name: "Reason", exact: true })
      .fill("New deliberate historical restore");
    // Browser acknowledgement time is not an authoritative audit timestamp.
    // Keep the API clock unchanged and model a browser behind the retained events.
    const browserTime =
      Math.min(...history.data.items.map((event) => Date.parse(event.at))) -
      60000;
    await page.clock.setFixedTime(new Date(browserTime));
    await page
      .getByRole("button", { name: "Save settings", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toContainText(competing.etag!);
    await confirmCommand(page);
    const acknowledgement = page.getByRole("region", {
      name: "Acknowledged administration change",
      exact: true,
    });
    await expect(acknowledgement).toContainText("tenant.settings.changed");
    const saved = await catalog.admin<Policy>("GET", "/api/v1/tenant-settings");
    expect(saved.data.revision).toBe(24);
    expect(saved.data.overrides).toEqual({
      automaticSourceSyncEnabled: false,
      newJobMaxAttempts: 2,
    });
    const count = reads.length;
    await acknowledgement
      .getByRole("link", { name: "Investigate change", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: "Audit investigation", exact: true }),
    ).toBeVisible();
    expect(reads).toHaveLength(count);
    expect(new URL(page.url()).search).toBe("");
    await expect(
      page.getByRole("combobox", { name: "Time range", exact: true }),
    ).toHaveValue("custom");
    await expect(page.getByText(/Initially staged UTC interval/)).toBeVisible();
    const stagedBefore = await page
      .getByLabel("Occurred before (exclusive, local time)", { exact: true })
      .inputValue();
    const displayedBefore = await page.evaluate((at) => {
      const fields = Object.fromEntries(
        new Intl.DateTimeFormat("en-CA", {
          timeZone: "America/New_York",
          year: "numeric",
          month: "2-digit",
          day: "2-digit",
          hourCycle: "h23",
          hour: "2-digit",
          minute: "2-digit",
          second: "2-digit",
          fractionalSecondDigits: 3,
        })
          .formatToParts(new Date(at))
          .map((part) => [part.type, part.value]),
      );
      const input = document.createElement("input");
      input.type = "datetime-local";
      input.value = `${fields.year}-${fields.month}-${fields.day}T${fields.hour}:${fields.minute}:${fields.second}.${fields.fractionalSecond}`;
      return input.value;
    }, browserTime);
    expect(stagedBefore).toBe(displayedBefore);
    const stagedSearch = page.waitForResponse((reply) =>
      new URL(reply.url()).pathname.endsWith("/api/v1/audit-events"),
    );
    await page
      .getByRole("button", { name: "Search audit events", exact: true })
      .click();
    const stagedResponse = await stagedSearch;
    expect(stagedResponse.status()).toBe(200);
    expect(
      new URL(stagedResponse.url()).searchParams.get("occurredBefore"),
    ).toBe(new Date(browserTime).toISOString());
    await expect(page.getByText(/No matching events/)).toBeVisible();
    expect(reads).toHaveLength(count + 1);
    const retained = await catalog.admin<{ items: Event[] }>(
      "GET",
      `/api/v1/audit-events?${new URLSearchParams({ action: "tenant.settings.changed", targetType: "tenant_settings", targetId: saved.data.tenantId, limit: "100" })}`,
    );
    expect(retained.data.items).toHaveLength(24);
    const retainedTimes = retained.data.items.map((event) =>
      Date.parse(event.at),
    );
    // Use visible custom bounds from retained evidence, independent of browser time.
    // UTC midnight renders outside New York's ambiguous DST hour. These explicit
    // local bounds include all retained events without changing service clocks.
    const day = 24 * 3600000;
    const occurredFrom = Math.floor(Math.min(...retainedTimes) / day) * day;
    const occurredBefore =
      (Math.floor(Math.max(...retainedTimes) / day) + 1) * day;
    const localValue = (at: number) =>
      new Date(at)
        .toLocaleString("sv-SE", { timeZone: "America/New_York" })
        .replace(" ", "T")
        .replace(/:00$/, "");
    // Chromium's datetime-local value omits zero seconds. Exercise that boundary
    // independently of the retained event timestamps before setting real bounds.
    const fromInput = page.getByLabel("Occurred from (inclusive, local time)", {
      exact: true,
    });
    await fromInput.fill(localValue(Date.UTC(2026, 9, 25, 9, 41, 0)));
    await expect(fromInput).toHaveValue("2026-10-25T05:41");
    await page
      .getByLabel("Occurred from (inclusive, local time)", { exact: true })
      .fill(localValue(occurredFrom));
    await page
      .getByLabel("Occurred before (exclusive, local time)", { exact: true })
      .fill(localValue(occurredBefore));
    const searched = page.waitForResponse((reply) =>
      new URL(reply.url()).pathname.endsWith("/api/v1/audit-events"),
    );
    await page
      .getByRole("button", { name: "Search audit events", exact: true })
      .click();
    const response = await searched;
    expect(response.status()).toBe(200);
    const query = new URL(response.url()).searchParams;
    expect(query.get("targetType")).toBe("tenant_settings");
    expect(query.get("targetId")).toBe(saved.data.tenantId);
    expect(query.has("requestId")).toBe(false);
    expect(query.has("actor")).toBe(false);
    const bounds = [query.get("occurredFrom"), query.get("occurredBefore")];
    expect(bounds).toEqual([
      new Date(occurredFrom).toISOString(),
      new Date(occurredBefore).toISOString(),
    ]);
    const from = Date.parse(bounds[0]!);
    const before = Date.parse(bounds[1]!);
    const knownTimes = history.data.items.map((event) => Date.parse(event.at));
    const windowEvidence = info.outputPath("audit-handoff-window.json");
    await writeFile(
      windowEvidence,
      JSON.stringify({
        knownEventCount: knownTimes.length,
        returnedEventCount: ((await response.json()) as { items: Event[] })
          .items.length,
        knownEventsBeforeLower: knownTimes.filter((at) => at < from).length,
        knownEventsAtOrAfterUpper: knownTimes.filter((at) => at >= before)
          .length,
        knownEventsWithinBounds: knownTimes.filter(
          (at) => at >= from && at < before,
        ).length,
      }),
    );
    await info.attach("audit-handoff-window.json", {
      contentType: "application/json",
      path: windowEvidence,
    });
    const entries = page.locator(".audit-entry");
    await expect(entries).toHaveCount(24);
    await openDisclosure(page, "Details, identifiers and filter actions");
    await entries
      .first()
      .getByRole("button", { name: "Use target as filter", exact: true })
      .click();
    await entries
      .first()
      .getByRole("button", { name: "Use principal as filter", exact: true })
      .click();
    expect(reads).toHaveLength(count + 2);
    const repeated = page.waitForResponse((reply) =>
      new URL(reply.url()).pathname.endsWith("/api/v1/audit-events"),
    );
    await page
      .getByRole("button", { name: "Search audit events", exact: true })
      .click();
    const next = new URL((await repeated).url()).searchParams;
    expect([next.get("occurredFrom"), next.get("occurredBefore")]).toEqual(
      bounds,
    );
    if (deployment === "root") {
      await expect(entries).toHaveCount(24);
      await page.setViewportSize({ width: 1440, height: 1800 });
      await entries
        .first()
        .getByText("Details, identifiers and filter actions", { exact: true })
        .click();
      const criteria = page.getByRole("region", {
        name: "Applied audit criteria",
        exact: true,
      });
      await criteria.evaluate((node) =>
        window.scrollBy(0, node.getBoundingClientRect().top - 100),
      );
      const top = await criteria.boundingBox();
      const event = await entries.first().boundingBox();
      if (!top || !event)
        throw new Error("Audit criteria and results are not visible");
      await page.screenshot({
        path: info.outputPath("audit-investigation-desktop.png"),
        clip: {
          x: top.x,
          y: top.y,
          width: top.width,
          height: event.y + event.height - top.y + 8,
        },
      });
    }
    await page.setViewportSize({ width: 375, height: 812 });
    await expect(page.locator("body")).toHaveJSProperty("scrollWidth", 375);
    const axe = await new AxeBuilder({ page })
      .include("main")
      .withTags(["wcag2a", "wcag2aa", "wcag21aa"])
      .analyze();
    expect(axe.violations).toEqual([]);
    expect(axe.incomplete).toEqual([]);
    if (deployment === "root")
      await page.screenshot({
        path: info.outputPath("audit-investigation.png"),
        fullPage: true,
      });
    await page.reload();
    await expect(
      page.getByRole("combobox", { name: "Activity", exact: true }),
    ).toHaveValue("");
    await expect(page.locator(".audit-entry")).toHaveCount(0);
  });
}
