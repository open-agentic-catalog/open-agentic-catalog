// Real admin login and shell reconciliation; controlled hints/replies qualify browser custody,
// not API authorization. No command below reaches the backing Catalog.
import type { Page } from "@playwright/test";
import type { SessionReply } from "../../src/api/client";
import { test, expect, login } from "./fixtures";
import { openDisclosure } from "./editor-disclosure";

const stamp = "2026-10-20T00:00:00Z";
const snapshotId = "00000000-0000-7000-8000-000000009981";
const foreignTenant = "00000000-0000-7000-8000-000000009999";
type Command = {
  body: unknown;
  key: string | undefined;
  match: string | undefined;
};

/** Preserve the authenticated identity while simulating a newly bounded permission projection. */
async function currentHints(page: Page) {
  const state = { removed: new Set<string>(), tenantId: "", reads: 0 };
  await page.route("**/auth/session", async (route) => {
    const response = await route.fetch();
    const value = (await response.json()) as SessionReply;
    if (value.access) {
      state.tenantId = value.access.tenantId;
      value.access = {
        ...value.access,
        permissions: value.access.permissions.filter(
          (p) => !state.removed.has(p),
        ),
        availablePermissions: value.access.availablePermissions.filter(
          (p) => !state.removed.has(p),
        ),
      };
    }
    state.reads++;
    await route.fulfill({ response, json: value });
  });
  return state;
}

/** A user-started pending inventory read supplies the denial that refreshes shell hints. */
async function heldInventory(page: Page) {
  let release = () => {};
  const pending = new Promise<void>((resolve) => (release = resolve));
  const state = { reads: 0, release: () => release() };
  await page.route("**/_catalog/api/v1/publication/hosts", async (route) => {
    state.reads++;
    await pending;
    await route.fulfill({
      status: 403,
      contentType: "application/problem+json",
      json: { title: "Forbidden", status: 403, code: "FORBIDDEN" },
    });
  });
  return state;
}

for (const uncertain of [false, true]) {
  test(`reindex retains ${uncertain ? "uncertain recovery" : "unsent preparation"} across current hint loss`, async ({
    page,
    catalog,
    network,
  }) => {
    const hints = await currentHints(page);
    const inventory = await heldInventory(page);
    network.expectFailure("GET", "/_catalog/api/v1/publication/hosts", 403);
    await page.route("**/_catalog/api/v1/capabilities", async (route) => {
      const response = await route.fetch();
      const value = (await response.json()) as { search: object };
      await route.fulfill({
        response,
        json: { ...value, search: { ...value.search, semantic: "enabled" } },
      });
    });
    const commands: Command[] = [];
    let jobReads = 0;
    page.on("request", (request) => {
      if (request.url().includes("/_catalog/api/v1/jobs/")) jobReads++;
    });
    await page.route("**/_catalog/api/v1/catalog:reindex", async (route) => {
      const request = route.request();
      expect(request.method()).toBe("POST");
      commands.push({
        body: request.postDataJSON(),
        key: request.headers()["idempotency-key"],
        match: request.headers()["if-match"],
      });
      await route.fulfill({
        status: 202,
        json: {
          id: "00000000-0000-7000-8000-000000009982",
          tenantId: commands.length === 1 ? foreignTenant : hints.tenantId,
          kind: "semantic.reindex",
          state: "queued",
          progress: 0,
          priority: 0,
          attempt: 0,
          attemptLimit: 3,
          cancellationRequested: false,
          notBefore: stamp,
          createdAt: stamp,
          updatedAt: stamp,
        },
      });
    });
    await login(page, catalog, "local-admin", "/service");
    await page
      .getByRole("combobox", { name: "Search index", exact: true })
      .selectOption("public");
    await page
      .getByRole("button", { name: "Choose active snapshot", exact: true })
      .click();
    await expect.poll(() => inventory.reads).toBe(1);
    await page
      .getByLabel("Active snapshot ID", { exact: true })
      .fill(snapshotId);
    const reason = page.getByRole("textbox", { name: "Reason", exact: true });
    await reason.fill("Keep captured rebuild population");
    // Retain this exact node: the native modal makes background role lookup inert.
    const reasonNode = await reason.elementHandle();
    if (!reasonNode) throw new Error("Prepared rebuild reason is missing");
    await page
      .getByRole("button", { name: "Rebuild search index", exact: true })
      .click();
    const dialog = page.getByRole("dialog");
    if (uncertain) {
      await dialog
        .getByRole("button", { name: "Confirm", exact: true })
        .click();
      await expect(
        dialog.getByRole("button", { name: "Retry same command", exact: true }),
      ).toBeEnabled();
    }
    const previousReads = hints.reads;
    hints.removed.add("operations.reindex");
    inventory.release();
    await expect.poll(() => hints.reads).toBe(previousReads + 1);
    await expect(
      page.getByText(
        "Rebuilding requires operations.reindex permission and tenant.admin scope when enforced. Your input is kept.",
        { exact: true },
      ),
    ).toBeAttached();
    await expect
      .poll(() =>
        reasonNode.evaluate((node) => ({
          connected: node.isConnected,
          disabled: node.matches(":disabled"),
          value: node instanceof HTMLTextAreaElement ? node.value : null,
        })),
      )
      .toEqual({
        connected: true,
        disabled: true,
        value: "Keep captured rebuild population",
      });
    await expect(dialog.getByLabel("Command summary")).toContainText(
      snapshotId,
    );
    if (uncertain) {
      await dialog
        .getByRole("button", { name: "Retry same command", exact: true })
        .click();
      await expect(
        page.getByRole("region", { name: "Accepted indexing request" }),
      ).toBeVisible();
      expect(commands).toHaveLength(2);
      expect(commands[1]).toEqual(commands[0]);
      expect(commands[0]?.body).toEqual({
        reason: "Keep captured rebuild population",
        snapshotId,
      });
      expect(commands[0]?.key).toMatch(/^[\da-f-]{36}$/i);
      expect(commands[0]?.match).toBeUndefined();
    } else {
      await expect(
        dialog.getByRole("button", { name: "Confirm", exact: true }),
      ).toBeDisabled();
      expect(commands).toHaveLength(0);
      await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
      await expect(reason).toHaveValue("Keep captured rebuild population");
    }
    expect(inventory.reads).toBe(1);
    expect(jobReads).toBe(0);
  });
}

test("reinspection keeps its uncertain exact command when current inspect hints disappear", async ({
  page,
  catalog,
  network,
}) => {
  const hints = await currentHints(page);
  const inventory = await heldInventory(page);
  network.expectFailure("GET", "/_catalog/api/v1/publication/hosts", 403);
  const inspectionId = "00000000-0000-7000-8000-000000009983";
  const endpoint = "https://peer.invalid/catalog";
  const inspection = () => ({
    id: inspectionId,
    tenantId: hints.tenantId,
    requestedUrl: endpoint,
    resolvedUrl: endpoint,
    profileId: "ard",
    entryCount: 0,
    expiresAt: "2020-01-01T00:00:00Z",
  });
  await page.route(
    `**/_catalog/api/v1/federation/source-inspections/${inspectionId}`,
    async (route) => {
      await route.fulfill({ json: inspection() });
    },
  );
  const commands: Command[] = [];
  let sourceWrites = 0;
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname.endsWith("/federation/sources")
    )
      sourceWrites++;
  });
  await page.route(
    "**/_catalog/api/v1/federation/source-inspections",
    async (route) => {
      const request = route.request();
      expect(request.method()).toBe("POST");
      commands.push({
        body: request.postDataJSON(),
        key: request.headers()["idempotency-key"],
        match: request.headers()["if-match"],
      });
      await route.fulfill({
        json: {
          ...inspection(),
          id: "00000000-0000-7000-8000-000000009984",
          tenantId: commands.length === 1 ? foreignTenant : hints.tenantId,
          expiresAt: "2099-01-01T00:00:00Z",
        },
      });
    },
  );
  await login(page, catalog, "local-admin", `/inspections/${inspectionId}`);
  const displayName = page.getByRole("textbox", {
    name: "Display name",
    exact: true,
  });
  await displayName.fill("Keep connection draft");
  await openDisclosure(page, "Advanced source participation");
  await page
    .getByRole("button", { name: "Load configured host choices", exact: true })
    .click();
  await expect.poll(() => inventory.reads).toBe(1);
  await page
    .getByRole("button", { name: "Reinspect the same interface", exact: true })
    .click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "Confirm", exact: true }).click();
  await expect(
    dialog.getByRole("button", { name: "Retry same command", exact: true }),
  ).toBeEnabled();
  const previousReads = hints.reads;
  hints.removed.add("source.inspect");
  inventory.release();
  await expect.poll(() => hints.reads).toBe(previousReads + 1);
  await expect(
    page.getByText(/Reinspection requires source.inspect permission/),
  ).toBeAttached();
  await dialog
    .getByRole("button", { name: "Retry same command", exact: true })
    .click();
  await expect(
    page.getByRole("status").filter({ hasText: "Fresh inspection retained" }),
  ).toBeVisible();
  expect(commands).toHaveLength(2);
  expect(commands[1]).toEqual(commands[0]);
  expect(commands[0]?.body).toEqual({ url: endpoint });
  expect(commands[0]?.key).toMatch(/^[\da-f-]{36}$/i);
  expect(commands[0]?.match).toBeUndefined();
  await expect(displayName).toHaveValue("Keep connection draft");
  expect(sourceWrites).toBe(0);
  expect(inventory.reads).toBe(1);
});

test("raw evidence is cleared on current hint loss and ignores late replies until another explicit read", async ({
  page,
  catalog,
  network,
}) => {
  const hints = await currentHints(page);
  const observationId = "00000000-0000-7000-8000-000000009985";
  const occurrencePath = `/_catalog/api/v1/observations/${observationId}/occurrences`;
  network.expectFailure("GET", occurrencePath, 403, 4);
  let occurrenceReads = 0;
  await page.route(`**${occurrencePath}?**`, async (route) => {
    occurrenceReads++;
    await route.fulfill(
      occurrenceReads === 1
        ? {
            json: {
              items: [
                {
                  observationId,
                  sourceId: "00000000-0000-7000-8000-000000009986",
                  sourceRevision: 1,
                  snapshotId,
                  retrievalDigest: "retained-retrieval",
                  entryLocation: "/entries/0",
                  exactEntryDigest: "retained-entry",
                  evidenceComplete: true,
                  observedAt: stamp,
                },
              ],
            },
          }
        : {
            status: 403,
            contentType: "application/problem+json",
            json: { title: "Forbidden", status: 403, code: "FORBIDDEN" },
          },
    );
  });
  let rawReads = 0;
  let release = () => {};
  const late = new Promise<void>((resolve) => (release = resolve));
  let completeLateReply = () => {};
  const lateReply = new Promise<void>(
    (resolve) => (completeLateReply = resolve),
  );
  const body = '{"private":"retained entry"}';
  await page.route(
    `**/_catalog/api/v1/observations/${observationId}/raw`,
    async (route) => {
      rawReads++;
      const isLate = rawReads === 2;
      if (isLate) await late;
      await route.fulfill({ contentType: "application/json", body });
      if (isLate) completeLateReply();
    },
  );
  let writes = 0;
  page.on("request", (request) => {
    if (
      request.url().includes("/_catalog/api/v1/") &&
      request.method() !== "GET"
    )
      writes++;
  });
  await login(page, catalog, "local-admin", `/observations/${observationId}`);
  await page
    .getByRole("button", { name: "Read exact raw entry", exact: true })
    .click();
  const heading = page.getByRole("heading", {
    name: "Exact raw entry",
    exact: true,
  });
  await expect(heading).toBeVisible();
  expect(rawReads).toBe(1);
  const changeHints = async (revoked: boolean) => {
    if (revoked) hints.removed.add("source.read_evidence");
    else hints.removed.delete("source.read_evidence");
    const previousReads = hints.reads;
    await page
      .getByRole("button", { name: "Refresh occurrences", exact: true })
      .click();
    await expect.poll(() => hints.reads).toBe(previousReads + 1);
    const read = page.getByRole("button", {
      name: "Read exact raw entry",
      exact: true,
    });
    if (revoked) await expect(read).toBeDisabled();
    else await expect(read).toBeEnabled();
    await expect(heading).toHaveCount(0);
    await expect(page.getByText(body, { exact: true })).toHaveCount(0);
  };
  await changeHints(true);
  await changeHints(false);
  expect(rawReads).toBe(1);
  await page
    .getByRole("button", { name: "Read exact raw entry", exact: true })
    .click();
  await expect.poll(() => rawReads).toBe(2);
  await changeHints(true);
  release();
  await lateReply;
  await expect(heading).toHaveCount(0);
  await expect(page.getByText(body, { exact: true })).toHaveCount(0);
  await changeHints(false);
  expect(rawReads).toBe(2);
  await page
    .getByRole("button", { name: "Read exact raw entry", exact: true })
    .click();
  await expect(heading).toBeVisible();
  expect(rawReads).toBe(3);
  expect(occurrenceReads).toBe(5);
  expect(writes).toBe(0);
});
