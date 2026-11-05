import { object, text } from "./json";
import { expectAlignedOperationControls } from "./operations-layout";
import { test, expect, login, confirmCommand } from "./fixtures";
import { expectNaturalText } from "./text-layout";
import { required } from "../required";
import AxeBuilder from "@axe-core/playwright";
import type { Page } from "@playwright/test";

// Revealed one-time secrets must never enter automatic failure artifacts.
test.use({ webhooks: true, screenshot: "off" });

/** Suppress plaintext error-context artifacts; a failed worker must retain this through teardown. */
function protectSigningArtifacts() {
  const previous = process.env.PLAYWRIGHT_NO_COPY_PROMPT;
  process.env.PLAYWRIGHT_NO_COPY_PROMPT = "1";
  return () => {
    if (previous === undefined) delete process.env.PLAYWRIGHT_NO_COPY_PROMPT;
    else process.env.PLAYWRIGHT_NO_COPY_PROMPT = previous;
  };
}

/** Return only display mode; locator attribute failures can otherwise include the signing value. */
async function signingDisplayMode(page: Page) {
  return page.evaluate(() => {
    const inputs = document.querySelectorAll<HTMLInputElement>(
      'input[autocomplete="off"][readonly]',
    );
    return inputs.length === 1 ? inputs.item(0).type : null;
  });
}

test("enabled webhooks support one-time secrets editing rotation policy-denied tests and disabling", async ({
  page,
  catalog,
  context,
  network,
}) => {
  // The pinned Playwright runner otherwise includes plaintext in error-context.md.
  // Restore only after success: a failed worker must retain this through teardown.
  const restoreArtifacts = protectSigningArtifacts();
  const receiver = `https://127.0.0.1/${"receiver".repeat(200)}`;
  await login(page, catalog, "local-admin", "/webhooks");
  await expect(
    page.getByRole("heading", { name: "No webhooks yet", exact: true }),
  ).toBeVisible();
  const empty = page.locator(".workspace-empty");
  await expectNaturalText(empty.locator("p"), empty, 1);
  await page.setViewportSize({ width: 390, height: 844 });
  await expectNaturalText(empty.locator("p"), empty, "wrapped");
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByRole("link", { name: "Create webhook", exact: true }).click();
  const setup = page
    .getByRole("heading", { name: "Set up webhook delivery", exact: true })
    .locator("..");
  await expect(setup.getByRole("link")).toHaveAttribute(
    "href",
    /^https:\/\/fixture\.invalid\//,
  );
  await expect(setup.getByRole("link")).toHaveAttribute("target", "_blank");
  await page
    .getByText("Deployment and receiver checklist", { exact: true })
    .click();
  await expect(setup).toContainText(
    "Normal notification delivery needs a worker",
  );
  await page.getByText("More public event types (32)", { exact: true }).click();
  await page
    .getByRole("checkbox", { name: "Member suspended", exact: true })
    .check();
  await expect(
    page.getByRole("checkbox", { name: "Member suspended", exact: true }),
  ).toBeChecked();
  await page.setViewportSize({ width: 320, height: 844 });
  await page.emulateMedia({ colorScheme: "dark" });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
    violations: [],
    incomplete: [],
  });
  await page
    .getByRole("checkbox", { name: "Member suspended", exact: true })
    .uncheck();
  await page.getByText("More public event types (32)", { exact: true }).click();
  await page
    .getByText("Deployment and receiver checklist", { exact: true })
    .click();
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.emulateMedia({ colorScheme: "light" });
  await page
    .getByRole("textbox", { name: "Webhook name", exact: true })
    .fill("Browser receiver");
  // Literal loopback is rejected by the default egress policy before dialing.
  // No external receiver, credentials, DNS lookup or listener is required.
  await page
    .getByRole("textbox", { name: "Receiver URL", exact: true })
    .fill(receiver);
  await page
    .getByRole("button", { name: "Create webhook", exact: true })
    .click();
  await expect(page.getByRole("alert")).toContainText(
    "Choose between 1 and 64 event selectors.",
  );
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page
    .getByRole("checkbox", { name: "Resource created", exact: true })
    .check();
  await page.getByText("Advanced subscriptions", { exact: true }).click();
  await expect(
    page.getByRole("textbox", { name: "Event type codes", exact: true }),
  ).toHaveValue("com.open.agentic.catalog.resource.created.v1");
  await page
    .getByRole("checkbox", { name: "Resource created", exact: true })
    .uncheck();
  await expect(
    page.getByRole("textbox", { name: "Event type codes", exact: true }),
  ).toHaveValue("");
  let createRequests = 0;
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname.endsWith("/_catalog/api/v1/webhooks")
    )
      createRequests++;
  });
  for (const [selectors, error] of [
    ["RESOURCE.CREATED", "Unknown event selector."],
    ["tenant.settings.updated", "Unknown event selector."],
    [
      Array(65).fill("resource.created").join(","),
      "Choose between 1 and 64 event selectors.",
    ],
  ] as const) {
    await page
      .getByRole("textbox", { name: "Event type codes", exact: true })
      .fill(selectors);
    await page
      .getByRole("button", { name: "Create webhook", exact: true })
      .click();
    await expect(page.getByRole("alert")).toContainText(error);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(
      page.getByRole("textbox", { name: "Event type codes", exact: true }),
    ).toHaveValue(selectors);
    expect(createRequests).toBe(0);
  }
  const unchanged = await catalog.admin<{ items: unknown[] }>(
    "GET",
    "/api/v1/webhooks?limit=25",
  );
  expect(unchanged.data.items).toEqual([]);
  await page
    .getByRole("textbox", { name: "Event type codes", exact: true })
    .fill("resource.created,com.open.agentic.catalog.resource.created.v1");
  await expect(
    page.getByRole("checkbox", { name: "Resource created", exact: true }),
  ).toBeChecked();
  await page
    .getByRole("textbox", { name: "Receiver URL", exact: true })
    .fill("http://127.0.0.1/events");
  await page
    .getByRole("button", { name: "Create webhook", exact: true })
    .click();
  await expect(page.getByRole("alert")).toContainText("HTTPS");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page
    .getByRole("textbox", { name: "Receiver URL", exact: true })
    .fill(receiver);
  await page.getByText("Advanced subscriptions", { exact: true }).click();
  await page
    .getByRole("button", { name: "Create webhook", exact: true })
    .click();
  const createdRequest = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/api/v1/webhooks"),
  );
  await confirmCommand(page);
  expect(
    object((await createdRequest).request().postDataJSON()).eventTypes,
  ).toEqual(["com.open.agentic.catalog.resource.created.v1"]);
  expect(createRequests).toBe(1);
  await expect(
    page.getByRole("heading", { name: "Save the signing secret", exact: true }),
  ).toBeVisible();
  await expect.poll(() => signingDisplayMode(page)).toBe("password");
  expect(
    await page
      .getByLabel("Signing secret", { exact: true })
      .evaluate((input) => (input as HTMLInputElement).value.length > 0),
  ).toBe(true);
  await page
    .getByRole("button", { name: "Reveal secret", exact: true })
    .click();
  await expect.poll(() => signingDisplayMode(page)).toBe("text");
  await page.getByRole("button", { name: "Hide secret", exact: true }).click();
  await context.grantPermissions(["clipboard-read", "clipboard-write"], {
    origin: catalog.origin,
  });
  await page.getByRole("button", { name: "Copy secret", exact: true }).click();
  await expect(
    page.getByText("Copied. Clear your clipboard when finished.", {
      exact: true,
    }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      async () =>
        (await navigator.clipboard.readText()) ===
        (
          document.querySelector(
            'input[autocomplete="off"][readonly]',
          ) as HTMLInputElement
        ).value,
    ),
  ).toBe(true);
  await context.clearPermissions();
  const permission = await context.newCDPSession(page);
  const { targetInfo } = await permission.send("Target.getTargetInfo");
  await permission.send("Browser.setPermission", {
    permission: { name: "clipboard-write" },
    setting: "denied",
    origin: catalog.origin,
    browserContextId: required(targetInfo.browserContextId),
  });
  expect(
    await page.evaluate(
      async () =>
        (
          await navigator.permissions.query({
            name: "clipboard-write" as PermissionName,
          })
        ).state,
    ),
  ).toBe("denied");
  await page.getByRole("button", { name: "Copy secret", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText(
    "Clipboard access failed",
  );
  await expect(
    page.getByText("Copied. Clear your clipboard when finished.", {
      exact: true,
    }),
  ).toHaveCount(0);
  await permission.detach();
  expect(await page.evaluate(() => Object.keys(sessionStorage))).toEqual([]);
  expect(await page.evaluate(() => Object.keys(localStorage).sort())).toEqual([
    "catalog-ui:density",
    "catalog-ui:theme",
  ]);
  await page
    .getByRole("button", { name: "Clear secret from this page" })
    .click();
  await expect(page.getByLabel("Signing secret", { exact: true })).toHaveCount(
    0,
  );
  await expect(
    page.getByRole("checkbox", { name: "Resource created", exact: true }),
  ).toBeChecked();
  await page.getByRole("link", { name: "Open webhook", exact: true }).click();
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(
    page.getByRole("region", { name: "Last loaded endpoint" }),
  ).toBeVisible();
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page.setViewportSize({ width: 1440, height: 900 });
  await expect(page.getByLabel("Signing secret", { exact: true })).toHaveCount(
    0,
  );
  const webhookID = required(new URL(page.url()).pathname.split("/").pop());
  const beforeUpdate = await catalog.admin<{ revision: number }>(
    "GET",
    `/api/v1/webhooks/${webhookID}`,
  );
  const endpointUpdates: string[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "PATCH" &&
      new URL(request.url()).pathname.endsWith(`/api/v1/webhooks/${webhookID}`)
    )
      endpointUpdates.push(request.method());
  });
  await page
    .getByRole("button", { name: "Update webhook", exact: true })
    .click();
  await expect(page.getByRole("alert")).toContainText(
    "Change the name, receiver URL or subscriptions before updating.",
  );
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(endpointUpdates).toEqual([]);
  const afterNoop = await catalog.admin<{ revision: number }>(
    "GET",
    `/api/v1/webhooks/${webhookID}`,
  );
  expect(afterNoop.data).toEqual(beforeUpdate.data);
  await page
    .getByRole("textbox", { name: "Webhook name", exact: true })
    .fill("Updated browser receiver");
  await page
    .getByRole("button", { name: "Update webhook", exact: true })
    .click();
  await confirmCommand(page);
  await expect(page.getByRole("status")).toContainText("Webhook updated.");
  expect(endpointUpdates).toEqual(["PATCH"]);
  await expect(
    page.getByRole("button", { name: "Manage webhook", exact: true }),
  ).toBeDisabled();
  await page
    .getByRole("button", { name: "Reload endpoint", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "Manage webhook", exact: true }),
  ).toBeEnabled();
  await expect(
    page.getByRole("textbox", { name: "Webhook name", exact: true }),
  ).toHaveValue("Updated browser receiver");
  await page
    .getByRole("combobox", { name: "Webhook action", exact: true })
    .selectOption("rotate");
  await expect(
    page.getByText(/Catalog signs with both secrets for 15 minutes/),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Manage webhook", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toContainText(
    "Save and install the new secret in your receiver",
  );
  const rotated = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith(":rotate-secret"),
  );
  await confirmCommand(page);
  const routine = await rotated;
  expect(routine.status()).toBe(200);
  expect(object(routine.request().postDataJSON())).toEqual({
    revokePrevious: false,
  });
  // Inspect only the public deadline, never include the one-time secret in assertions.
  const deadline = text(object(await routine.json()).previousSecretExpiresAt);
  expect(Number.isFinite(Date.parse(deadline))).toBe(true);
  const previous = page.getByText(
    /Catalog includes a signature made with the previous secret until/,
  );
  const localDeadline = await page.evaluate(
    (value) => new Date(value).toLocaleString(),
    deadline,
  );
  await expect(previous).toContainText(localDeadline);
  await expect(previous).toContainText(
    "Install the new secret in your receiver before then",
  );
  // A controlled clipboard keeps comparison local to the page and reports only booleans.
  // The earlier create flow separately qualifies the real browser clipboard.
  await page.evaluate(() => {
    let value = "";
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: {
        writeText: (text: string) => {
          value = text;
          return Promise.resolve();
        },
        readText: () => Promise.resolve(value),
      },
    });
  });
  await page
    .getByRole("button", { name: "Reveal secret", exact: true })
    .click();
  await page.getByRole("button", { name: "Copy secret", exact: true }).click();
  await expect(
    page.getByText("Copied. Clear your clipboard when finished.", {
      exact: true,
    }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Reload endpoint", exact: true })
    .click();
  await expect(page.getByLabel("Signing secret", { exact: true })).toHaveCount(
    1,
  );
  await expect(
    page.getByRole("button", {
      name: "Prepare another webhook action",
      exact: true,
    }),
  ).toBeEnabled();
  await page
    .getByRole("button", {
      name: "Prepare another webhook action",
      exact: true,
    })
    .click();
  await page
    .getByRole("combobox", { name: "Webhook action", exact: true })
    .selectOption("rotate");
  const acknowledgement = page.getByRole("checkbox", {
    name: /I understand the signing secret still on this page will be replaced/,
  });
  const rotationGuide = page.getByRole("region", {
    name: "Signing-key rotation guidance",
  });
  await expect(rotationGuide.locator("time")).toHaveAttribute(
    "datetime",
    deadline,
  );
  const beforeConflict = await catalog.admin<{
    revision: number;
    secretVersion: number;
  }>("GET", `/api/v1/webhooks/${webhookID}`);
  await acknowledgement.check();
  network.expectFailure(
    "POST",
    `/_catalog/api/v1/webhooks/${webhookID}:rotate-secret`,
    409,
  );
  const overlapRefusal = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith(":rotate-secret"),
  );
  await page
    .getByRole("button", { name: "Manage webhook", exact: true })
    .click();
  await page.getByRole("button", { name: "Confirm", exact: true }).click();
  expect((await overlapRefusal).status()).toBe(409);
  await expect(page.getByRole("dialog")).toContainText(
    "conflicts with the current state",
  );
  await page.getByRole("button", { name: "Back to form", exact: true }).click();
  await expect(
    page.getByRole("combobox", { name: "Rotation mode", exact: true }),
  ).toHaveValue("routine");
  await expect(rotationGuide.locator("time")).toHaveAttribute(
    "datetime",
    deadline,
  );
  const afterConflict = await catalog.admin<{
    revision: number;
    secretVersion: number;
  }>("GET", `/api/v1/webhooks/${webhookID}`);
  expect(afterConflict.data).toEqual(beforeConflict.data);
  await acknowledgement.uncheck();
  await page
    .getByRole("combobox", { name: "Rotation mode", exact: true })
    .selectOption("emergency");
  await expect(
    page.getByText(/Catalog stops signing with previous secrets immediately/),
  ).toBeVisible();
  await expect(acknowledgement).not.toBeChecked();
  await page
    .getByRole("button", { name: "Manage webhook", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(
    await acknowledgement.evaluate(
      (input) => (input as HTMLInputElement).validity.valueMissing,
    ),
  ).toBe(true);
  await acknowledgement.check();
  await expect(
    page.getByRole("button", { name: "Manage webhook", exact: true }),
  ).toHaveClass("warning");
  await page
    .getByRole("button", { name: "Manage webhook", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toContainText(
    "Replace the keys accepted by your receiver immediately",
  );
  await expect(
    page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true }),
  ).toHaveClass("warning");
  await expect(page.getByRole("dialog")).toContainText(
    "In-flight requests cannot be recalled",
  );
  await confirmCommand(page);
  await expect(
    page.getByRole("heading", { name: "Save the signing secret", exact: true }),
  ).toBeVisible();
  await expect.poll(() => signingDisplayMode(page)).toBe("password");
  await expect(acknowledgement).not.toBeChecked();
  await expect(
    page.getByText("Copied. Clear your clipboard when finished.", {
      exact: true,
    }),
  ).toHaveCount(0);
  expect(
    await page.evaluate(
      async () =>
        (await navigator.clipboard.readText()) !==
        (
          document.querySelector(
            'input[autocomplete="off"][readonly]',
          ) as HTMLInputElement
        ).value,
    ),
  ).toBe(true);
  await expect(
    page.getByText(
      /Catalog includes a signature made with the previous secret until/,
    ),
  ).toHaveCount(0);
  await page
    .getByRole("button", { name: "Clear secret from this page" })
    .click();
  await expect(rotationGuide).toContainText("Observed emergency rotation");
  await expect(rotationGuide).toContainText("signing version 3");
  await page.reload();
  await expect(
    page.getByRole("region", { name: "Signing-key rotation guidance" }),
  ).toContainText("No rotation deadline is known on this page");
  await expect(page.getByLabel("Signing secret", { exact: true })).toHaveCount(
    0,
  );
  const delivery = page.waitForResponse(
    (response) =>
      response.url().endsWith(":test") &&
      response.request().method() === "POST",
  );
  await page
    .getByRole("button", { name: "Manage webhook", exact: true })
    .click();
  await confirmCommand(page);
  const result = await delivery;
  expect(result.status()).toBe(200);
  expect(object(await result.json()).state).toBe("dead_letter");
  await expect(page.getByRole("status")).toContainText(
    "Test notification was not delivered.",
  );
  await expect(
    page.getByRole("region", { name: "Test delivery result" }),
  ).toContainText("Automatic retries stopped");
  await page
    .getByText("Complete test delivery evidence", { exact: true })
    .click();
  await expect(
    page.locator("pre").filter({ hasText: '"state": "dead_letter"' }).first(),
  ).toBeVisible();
  await page.reload();
  await page
    .getByRole("combobox", { name: "Webhook action", exact: true })
    .selectOption("disable");
  await page
    .getByRole("button", { name: "Manage webhook", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toContainText(
    "Permanently disabled; no re-enable or replay",
  );
  await expect(
    page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true }),
  ).toHaveClass("danger");
  await expect(
    page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .locator('svg[aria-hidden="true"]'),
  ).toHaveCount(1);
  await expect(page.getByRole("dialog")).toContainText(
    "no historical backfill",
  );
  await confirmCommand(page);
  await expect(page.getByRole("status")).toContainText("Webhook disabled.");
  await page.reload();
  await expect(
    page.getByRole("button", { name: "Update webhook", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Manage webhook", exact: true }),
  ).toHaveCount(0);
  await page
    .getByRole("button", { name: "Create replacement", exact: true })
    .click();
  const beforeReplacement = await catalog.admin<{
    items: {
      id: string;
      status: string;
      revision: number;
      secretVersion: number;
    }[];
  }>("GET", "/api/v1/webhooks?limit=25");
  const original = required(
    beforeReplacement.data.items.find((item) => item.status === "disabled"),
  );
  expect(original).toMatchObject({ revision: 5, secretVersion: 3 });
  await expect(
    page.getByRole("textbox", { name: "Webhook name", exact: true }),
  ).toHaveValue("Updated browser receiver");
  await page
    .getByRole("textbox", { name: "Webhook name", exact: true })
    .fill("Replacement browser receiver");
  await page
    .getByRole("button", { name: "Create replacement webhook", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toContainText(original.id);
  await expect(page.getByRole("dialog")).toContainText("No backfill");
  await confirmCommand(page);
  await expect(
    page.getByRole("heading", { name: "Save the signing secret", exact: true }),
  ).toBeVisible();
  await expect.poll(() => signingDisplayMode(page)).toBe("password");
  const inventory = await catalog.admin<{
    items: {
      id: string;
      status: string;
      secretVersion: number;
      revision: number;
    }[];
  }>("GET", "/api/v1/webhooks?limit=25");
  expect(
    inventory.data.items.find((item) => item.id === original.id),
  ).toMatchObject({
    status: "disabled",
    secretVersion: 3,
    revision: original.revision,
  });
  expect(
    inventory.data.items.filter((item) => item.status === "active"),
  ).toHaveLength(1);
  await page
    .getByRole("button", { name: "Clear secret from this page" })
    .click();
  restoreArtifacts();
});

test.describe("exact webhook delegates", () => {
  test.use({ maintainedIdentities: true });
  test("read-only and rotation-only delegates inspect exact metadata and receive honest controls", async ({
    page,
    catalog,
  }) => {
    const restoreArtifacts = protectSigningArtifacts();
    const created = await catalog.admin<{ endpoint: { id: string } }>(
      "POST",
      "/api/v1/webhooks",
      {
        name: "Delegate receiver",
        url: "https://127.0.0.1/events",
        eventTypes: ["com.open.agentic.catalog.webhook.test.v1"],
      },
    );
    const id = created.data.endpoint.id;
    const memberships = await catalog.admin<{ items: { id: string }[] }>(
      "GET",
      "/api/v1/memberships?subject=local-template-service-client&limit=1",
    );
    const membership = required(memberships.data.items[0]);
    const role = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/access/roles",
      {
        name: "Owned webhook delegate",
        permissions: ["webhook.read"],
        reason: "Qualify read-only webhook controls",
      },
    );
    await catalog.admin("POST", "/api/v1/access/assignments", {
      roleDefinitionId: role.data.id,
      membershipId: membership.id,
      reason: "Qualify exact webhook authority",
    });
    const reads: string[] = [];
    page.on("request", (request) => {
      if (request.url().includes("/_catalog/api/v1/"))
        reads.push(new URL(request.url()).pathname);
    });
    await login(
      page,
      catalog,
      "local-template-service-client",
      `/webhooks/${id}`,
    );
    await expect(
      page.getByRole("textbox", { name: "Webhook name", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("region", { name: "Last loaded endpoint" }),
    ).toContainText("https://127.0.0.1/");
    await expect(
      page.getByRole("textbox", { name: "Receiver URL", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("checkbox", { name: "All events", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "Manage webhook", exact: true }),
    ).toHaveCount(0);
    expect(
      await page
        .getByText("Signing version", { exact: true })
        .evaluate((element) => element.nextElementSibling?.textContent),
    ).toBe("1");
    await expect(
      page.getByRole("button", { name: "Copy endpoint ID", exact: true }),
    ).toBeVisible();
    expect(
      reads.some((path) =>
        /\/resources|\/deliveries|\/reconciliation/.test(path),
      ),
    ).toBe(false);
    await page.setViewportSize({ width: 320, height: 844 });
    await page.emulateMedia({ colorScheme: "dark" });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
      violations: [],
      incomplete: [],
    });
    await catalog.admin(
      "PATCH",
      `/api/v1/access/roles/${role.data.id}`,
      {
        name: "Owned webhook delegate",
        permissions: ["webhook.read", "webhook.rotate"],
        reason: "Qualify rotation-only controls",
      },
      role.etag,
    );
    await page.reload();
    await expect(
      page.getByRole("combobox", { name: "Webhook action", exact: true }),
    ).toHaveValue("rotate");
    await expect(
      page.getByRole("option", { name: "Send test", exact: true }),
    ).toHaveAttribute("disabled", "");
    await expect(
      page.getByRole("button", { name: "Update webhook", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "Manage webhook", exact: true }),
    ).toBeEnabled();
    await page
      .getByRole("button", { name: "Manage webhook", exact: true })
      .click();
    await confirmCommand(page);
    await expect(
      page.getByRole("heading", {
        name: "Save the signing secret",
        exact: true,
      }),
    ).toBeVisible();
    const endpoint = await catalog.admin<{
      secretVersion: number;
      status: string;
    }>("GET", `/api/v1/webhooks/${id}`);
    expect(endpoint.data).toMatchObject({ secretVersion: 2, status: "active" });
    const deliveries = await catalog.admin<{ items: unknown[] }>(
      "GET",
      `/api/v1/webhooks/${id}/deliveries?limit=25`,
    );
    expect(deliveries.data.items).toEqual([]);
    await page
      .getByRole("button", { name: "Clear secret from this page" })
      .click();
    const guidance = page.getByRole("region", {
      name: "Signing-key rotation guidance",
    });
    await expect(guidance).toContainText("Observed routine rotation");
    const deadline = text(
      await guidance.locator("time").getAttribute("datetime"),
    );
    expect(Number.isFinite(Date.parse(deadline))).toBe(true);
    await expect(
      page.getByLabel("Signing secret", { exact: true }),
    ).toHaveCount(0);
    // Only non-secret metadata and the observed rotation deadline enter the manual.
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.emulateMedia({ colorScheme: "light" });
    await guidance.scrollIntoViewIfNeeded();
    await guidance.screenshot({
      path: test.info().outputPath("webhook-rotation.png"),
    });
    const renamed = await catalog.admin<{
      revision: number;
      secretVersion: number;
    }>(
      "PATCH",
      `/api/v1/webhooks/${id}`,
      { name: "Delegate receiver metadata changed" },
      endpoint.etag,
    );
    expect(renamed.data.secretVersion).toBe(2);
    await page
      .getByRole("button", { name: "Reload endpoint", exact: true })
      .click();
    await expect(
      page.getByRole("heading", {
        name: "Delegate receiver metadata changed",
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      page.getByRole("region", { name: "Last loaded endpoint" }),
    ).toContainText("Delegate receiver metadata changed");
    await expect(
      page.getByRole("textbox", { name: "Webhook name", exact: true }),
    ).toHaveCount(0);
    await expect(guidance.locator("time")).toHaveAttribute(
      "datetime",
      deadline,
    );
    const rotatedElsewhere = await catalog.admin<{
      endpoint: { secretVersion: number };
    }>(
      "POST",
      `/api/v1/webhooks/${id}:rotate-secret`,
      { revokePrevious: true },
      renamed.etag,
    );
    expect(rotatedElsewhere.data.endpoint.secretVersion).toBe(3);
    await page
      .getByRole("button", { name: "Reload endpoint", exact: true })
      .click();
    await expect(guidance).toContainText(
      "No rotation deadline is known on this page",
    );
    await expect(guidance.locator("time")).toHaveCount(0);
    expect(
      await page
        .getByText("Signing version", { exact: true })
        .evaluate((element) => element.nextElementSibling?.textContent),
    ).toBe("3");
    await expect(
      page.getByLabel("Signing secret", { exact: true }),
    ).toHaveCount(0);
    restoreArtifacts();
  });
});

test("subpath webhook filters find off-page endpoints and retained deliveries", async ({
  page,
  catalog,
}) => {
  const restoreArtifacts = protectSigningArtifacts();
  for (let index = 0; index < 26; index++) {
    await catalog.admin("POST", "/api/v1/webhooks", {
      name: `Alpha ${String(index).padStart(2, "0")}`,
      url: `https://127.0.0.1/alpha-${index}`,
      eventTypes: ["*"],
    });
  }
  const created = await catalog.admin<{ endpoint: { id: string } }>(
    "POST",
    "/api/v1/webhooks",
    {
      name: "Zulu filtered",
      url: `https://127.0.0.1/${"filtered".repeat(100)}`,
      eventTypes: ["*"],
    },
  );
  const id = created.data.endpoint.id;
  await catalog.admin("POST", "/api/v1/webhooks", {
    name: "Zulu filtered",
    url: "https://127.0.0.1/duplicate",
    eventTypes: ["*"],
  });
  let oldest: { id: string; eventId: string } | undefined;
  for (let index = 0; index < 27; index++) {
    const sent = await catalog.admin<{ id: string; eventId: string }>(
      "POST",
      `/api/v1/webhooks/${id}:test`,
      {},
      '"rev-1"',
    );
    oldest ??= sent.data;
  }
  const target = required(oldest);
  await login(page, catalog, "local-admin", "/webhooks");
  await expect(
    page.getByRole("link", { name: "Zulu filtered", exact: true }),
  ).toHaveCount(0);
  await page.setViewportSize({ width: 1440, height: 1000 });
  await expectAlignedOperationControls([
    page.getByLabel("Webhook name (exact match)", { exact: true }),
    page.getByRole("combobox", { name: "Status", exact: true }),
  ]);
  await expectAlignedOperationControls([
    page.getByLabel("Exact endpoint ID", { exact: true }),
    page.getByRole("button", { name: "Open endpoint", exact: true }),
  ]);
  await page
    .getByLabel("Webhook name (exact match)", { exact: true })
    .fill("Zulu filtered");
  await page
    .getByRole("combobox", { name: "Status", exact: true })
    .selectOption("active");
  await page
    .getByRole("button", { name: "Apply endpoint filters", exact: true })
    .click();
  await expect(
    page.getByRole("link", { name: "Zulu filtered", exact: true }),
  ).toHaveCount(2);
  await page
    .getByLabel("Exact endpoint ID", { exact: true })
    .fill(id.toUpperCase());
  await page
    .getByRole("button", { name: "Open endpoint", exact: true })
    .click();
  await expect(page).toHaveURL(new RegExp(`/webhooks/${id}$`));
  await expect(page.getByLabel("Webhook name", { exact: true })).toHaveValue(
    "Zulu filtered",
  );
  const history = page
    .getByRole("heading", { name: "Delivery history", exact: true })
    .locator("..");
  await expect(
    history.getByRole("button", { name: "Copy delivery ID", exact: true }),
  ).toHaveCount(25);
  await page.getByLabel("Exact delivery ID", { exact: true }).fill(target.id);
  await page.getByLabel("Exact event ID", { exact: true }).fill(target.eventId);
  await page
    .getByRole("combobox", { name: "Delivery state", exact: true })
    .selectOption("dead_letter");
  await page
    .getByRole("button", { name: "Apply delivery filters", exact: true })
    .click();
  await expect(
    history.getByRole("button", { name: "Copy delivery ID", exact: true }),
  ).toHaveCount(1);
  const evidence = await catalog.admin<{ items: { id: string }[] }>(
    "GET",
    `/api/v1/webhooks/${id}/deliveries?deliveryId=${target.id}&eventId=${target.eventId}&state=dead_letter&limit=1`,
  );
  expect(evidence.data.items.map((item) => item.id)).toEqual([target.id]);
  await page.setViewportSize({ width: 320, height: 844 });
  await page.emulateMedia({ colorScheme: "dark" });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  expect(await new AxeBuilder({ page }).analyze()).toMatchObject({
    violations: [],
    incomplete: [],
  });
  await page
    .getByRole("button", { name: "Clear delivery filters", exact: true })
    .click();
  await expect(
    history.getByRole("button", { name: "Copy delivery ID", exact: true }),
  ).toHaveCount(25);
  restoreArtifacts();
});
