// Browser actions against the invocation-owned PostgreSQL stack and the
// existing functional HTTPS origin. No successful API response is mocked.
import { expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { readFile } from "node:fs/promises";

/** Open only the named native disclosure and its containing disclosures through real summary clicks. */
async function openDisclosure(root, title) {
  const summary = root.locator("summary").filter({
    hasText: new RegExp(
      `^\\s*${title.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*$`,
    ),
  });
  await expect(summary).toHaveCount(1);
  const ancestors = summary.locator("xpath=ancestor::details");
  for (let index = 0; index < (await ancestors.count()); index++) {
    const details = ancestors.nth(index);
    if ((await details.getAttribute("open")) === null)
      await details.locator(":scope > summary").click();
  }
  await expect(summary.locator("..")).toHaveJSProperty("open", true);
}

async function chooseNamedOption(select, name) {
  const option = select.getByRole("option", { name });
  await expect(option).toHaveCount(1);
  const id = await option.getAttribute("value");
  expect(id).toMatch(/^[0-9a-f-]{36}$/i);
  await select.selectOption(id);
}

/** Exercise persisted source, governance and webhook workflows against the invocation-owned stack. */
export async function packagedJourneys({
  page,
  origin,
  fixtureRequest,
  remoteSearch,
  phase,
  worker,
  api,
  ui,
  network,
  rotationEvidence,
  settingsFault,
  inspectAssignmentOutcome,
  captureDocumentation,
}) {
  const button = (name) => page.getByRole("button", { name, exact: true });
  const selects = new Set([
    "Search mode",
    "Synchronization",
    "Referral traversal",
    "Show entries",
    "Source action",
    "Change type",
    "Destination namespace",
    "Publisher",
    "Local publisher",
    "Namespace",
    "Webhook action",
    "Rotation mode",
    "Delivery state",
    "Job state",
    "Job type",
  ]);
  const field = (name) =>
    selects.has(name)
      ? page.getByRole("combobox", { name, exact: true })
      : [
            "Agents",
            "Enable live queries",
            "I acknowledge queries are sent to this upstream",
            "Resource created",
            "Override local display name",
            "Override local categories",
            "Override local license",
          ].includes(name)
        ? page.getByRole("checkbox", { name, exact: true })
        : name === "Signing secret"
          ? page.getByLabel(name, { exact: true })
          : page.getByRole("textbox", { name, exact: true });
  const link = (name) => page.getByRole("link", { name, exact: true });
  const visit = (path) => page.goto(origin + path);
  const command = async (name) => {
    await button(name).click();
    const outcome =
      name === "Revoke access assignment"
        ? page.waitForResponse(
            (response) =>
              response.request().method() === "POST" &&
              /\/api\/v1\/access\/assignments\/[0-9a-f-]{36}:revoke$/.test(
                new URL(response.url()).pathname,
              ),
          )
        : undefined;
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    if (outcome) {
      const response = await outcome;
      try {
        await inspectAssignmentOutcome(response);
      } catch {
        console.log("Owned assignment refusal diagnostics unavailable");
      }
    }
    await expect(page.getByRole("dialog")).toHaveCount(0);
  };
  const login = async (alias) => {
    await page.getByTitle("Account", { exact: true }).click();
    await button("Sign out").click();
    await page.locator(`[data-login-alias="${alias}"]`).click();
    await expect(page.getByTitle("Account", { exact: true })).toBeVisible();
  };
  const waitJob = async (state) => {
    const reportedState = page
      .locator(".workspace-kv > dt")
      .filter({ hasText: /^State$/ })
      .locator("xpath=following-sibling::dd[1]")
      .locator(".workspace-status");
    await expect
      .poll(
        async () => {
          const reply = page.waitForResponse(
            (r) =>
              r.request().method() === "GET" &&
              /\/api\/v1\/jobs\/[^/]+$/.test(new URL(r.url()).pathname),
          );
          await button("Reload job").click();
          const job = await (await reply).json();
          await expect(reportedState).toHaveAttribute("data-state", job.state);
          await expect(reportedState).toHaveText(
            {
              queued: "Queued",
              running: "Running",
              waiting: "Waiting",
              succeeded: "Succeeded",
              failed: "Failed",
              cancelled: "Cancelled",
            }[job.state] ?? job.state,
          );
          await expect(
            page.getByRole("status").filter({ hasText: "Loading" }),
          ).toHaveCount(0);
          return job.state;
        },
        { timeout: 30000 },
      )
      .toBe(state);
  };
  phase("compose-api-recovery");
  await login("local-admin");
  await visit("/overview");
  const publicationMetadata = await page.evaluate(async (endpoint) => {
    const response = await fetch(endpoint, {
      headers: { "X-Catalog-Request": "1" },
      credentials: "same-origin",
      cache: "no-store",
    });
    if (!response.ok || response.headers.get("Cache-Control") !== "no-store")
      throw new Error("Private publication summary unavailable");
    return response.json();
  }, origin + "/_catalog/api/v1/overview/publication");
  expect(publicationMetadata.selectionLimit).toBe(64);
  const publicationPanel = page.getByRole("region", {
    name: "Discovery publication",
    exact: true,
  });
  await expect(publicationPanel.getByRole("row")).toHaveCount(
    publicationMetadata.hosts.length + 1,
  );
  const administrationRead = page.waitForResponse((response) =>
    new URL(response.url()).pathname.endsWith(
      "/api/v1/overview/administration",
    ),
  );
  const feedbackSummaryRead = page.waitForResponse((response) =>
    new URL(response.url()).pathname.endsWith("/api/v1/overview/feedback"),
  );
  const ratingSummaryRead = page.waitForResponse((response) =>
    new URL(response.url()).pathname.endsWith("/api/v1/overview/ratings"),
  );
  await visit("/overview?view=administration");
  const administrationResponse = await administrationRead;
  const feedbackResponse = await feedbackSummaryRead;
  const ratingResponse = await ratingSummaryRead;
  for (const response of [
    administrationResponse,
    feedbackResponse,
    ratingResponse,
  ]) {
    expect(response.status()).toBe(200);
    expect(response.headers()["cache-control"]).toBe("no-store");
  }
  const administrationSummary = await administrationResponse.json();
  const accessPanel = page.getByRole("region", {
    name: "Catalog access",
    exact: true,
  });
  for (const [kind, title] of [
    ["human", "People"],
    ["service", "Service identities"],
  ]) {
    await expect(
      accessPanel
        .getByRole("row", { name: new RegExp("^" + title) })
        .getByRole("cell")
        .first(),
    ).toHaveText(String(administrationSummary[kind].total.value));
  }
  await expect(accessPanel.locator(".overview-count")).toHaveText(
    [
      administrationSummary.activeGroups.value,
      administrationSummary.activeRoles.total.value,
      administrationSummary.activeAssignments.value,
    ].map(String),
  );
  const feedbackSummary = await feedbackResponse.json();
  const ratingSummary = await ratingResponse.json();
  const feedbackPanel = page.getByRole("region", {
    name: "Open feedback",
    exact: true,
  });
  if (feedbackSummary.open.value > 0)
    await expect(feedbackPanel.locator(".overview-number")).toHaveText(
      String(feedbackSummary.open.value),
    );
  else await expect(feedbackPanel).toContainText("No open feedback.");
  const ratingPanel = page.getByRole("region", {
    name: "Ratings",
    exact: true,
  });
  if (ratingSummary.total.value > 0)
    await expect(ratingPanel.locator(".overview-number")).toHaveText(
      String(ratingSummary.total.value),
    );
  else await expect(ratingPanel).toContainText("No ratings to show.");
  const sourceSummaryRead = page.waitForResponse((response) =>
    new URL(response.url()).pathname.endsWith("/api/v1/overview/sources"),
  );
  const jobSummaryRead = page.waitForResponse((response) =>
    new URL(response.url()).pathname.endsWith("/api/v1/overview/jobs"),
  );
  const endpointSummaryRead = page.waitForResponse((response) =>
    new URL(response.url()).pathname.endsWith(
      "/api/v1/overview/webhook-endpoints",
    ),
  );
  const deliverySummaryRead = page.waitForResponse((response) =>
    new URL(response.url()).pathname.endsWith(
      "/api/v1/overview/webhook-deliveries",
    ),
  );
  await visit("/overview?view=operations");
  const endpointSummaryResponse = await endpointSummaryRead;
  const deliverySummaryResponse = await deliverySummaryRead;
  for (const [response, title] of [
    [endpointSummaryResponse, "Webhook endpoints"],
    [deliverySummaryResponse, "Delivery history"],
  ]) {
    expect(response.status()).toBe(200);
    expect(response.headers()["cache-control"]).toBe("no-store");
    const data = await response.json();
    const panel = page.getByRole("region", { name: title, exact: true });
    if (data.total.value > 0)
      await expect(panel.locator(".overview-number")).toHaveText(
        data.total.value.toLocaleString("en-US") +
          (data.total.limited ? "+" : ""),
      );
    else
      await expect(panel).toContainText(
        title === "Webhook endpoints"
          ? "No endpoints configured."
          : "No deliveries to show.",
      );
  }
  await expect(
    page.getByRole("region", {
      name: "Notification totals",
      exact: true,
    }),
  ).toBeVisible();
  const sourceSummaryResponse = await sourceSummaryRead;
  const jobSummaryResponse = await jobSummaryRead;
  expect(sourceSummaryResponse.status()).toBe(200);
  expect(jobSummaryResponse.status()).toBe(200);
  expect(sourceSummaryResponse.headers()["cache-control"]).toBe("no-store");
  expect(jobSummaryResponse.headers()["cache-control"]).toBe("no-store");
  const sourceSummary = await sourceSummaryResponse.json();
  const jobSummary = await jobSummaryResponse.json();
  const sourcePanel = page.getByRole("region", {
    name: "Federation sources",
    exact: true,
  });
  if (sourceSummary.total.value > 0)
    await expect(sourcePanel.locator(".overview-number")).toHaveText(
      sourceSummary.total.value.toLocaleString("en-US") +
        (sourceSummary.total.limited ? "+" : ""),
    );
  else await expect(sourcePanel).toContainText("No sources configured.");
  const queueSummary = page.getByRole("region", {
    name: "Job queues",
    exact: true,
  });
  await expect(queueSummary.locator("dd")).toHaveText(
    [jobSummary.total, jobSummary.due].map(
      (count) =>
        count.value.toLocaleString("en-US") + (count.limited ? "+" : ""),
    ),
  );
  const summaryRows = queueSummary.getByRole("table").locator("tbody tr");
  if (jobSummary.total.value === 0) {
    expect(
      jobSummary.kinds.every((kind) =>
        kind.states.every((state) => state.count === 0),
      ),
    ).toBe(true);
    await expect(queueSummary.getByRole("table")).toHaveCount(0);
    await expect(queueSummary).toContainText("No jobs to show.");
  } else {
    await expect(summaryRows).toHaveCount(jobSummary.kinds.length);
    for (const [index, kind] of jobSummary.kinds.entries())
      await expect(summaryRows.nth(index).getByRole("cell")).toHaveText(
        kind.states.map((state) => state.count.toLocaleString("en-US")),
      );
  }
  await expect(
    page.getByRole("region", { name: "Search indexing", exact: true }),
  ).toBeVisible();
  const processRead = async (path) => {
    const value = await page.evaluate(async (endpoint) => {
      const response = await fetch(endpoint, {
        headers: { "X-Catalog-Request": "1" },
        credentials: "same-origin",
      });
      if (!response.ok || response.headers.get("Cache-Control") !== "no-store")
        throw new Error("Process information unavailable");
      return response.json();
    }, origin + path);
    expect(value.instanceId).toMatch(
      /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/,
    );
    expect(value.uptimeSeconds).toBeGreaterThanOrEqual(0);
    return value;
  };
  const apiBefore = await processRead("/_catalog/api/v1/service-information");
  const uiBefore = await processRead("/_status");
  expect(apiBefore.role).toBe("api");
  expect(uiBefore.role).toBe("ui");
  expect(uiBefore.instanceId).not.toBe(apiBefore.instanceId);
  await visit("/namespaces/new");
  await field("Display name").fill("Packaged API recovery");
  await field("Slug").fill("packaged-api-recovery");
  await command("Create namespace");
  await link("Back to namespaces").click();
  const savedNamespace = link("Packaged API recovery");
  await expect(savedNamespace).toBeVisible();
  const savedHref = await savedNamespace.getAttribute("href");
  let mutations = 0;
  const observe = (request) => {
    if (
      !["GET", "HEAD"].includes(request.method()) &&
      new URL(request.url()).pathname.includes("/_catalog/api/")
    )
      mutations++;
  };
  page.on("request", observe);
  try {
    await api(false);
    const uiDuringOutage = await processRead("/_status");
    expect(uiDuringOutage.instanceId).toBe(uiBefore.instanceId);
    expect(uiDuringOutage.startedAt).toBe(uiBefore.startedAt);
    network.expectFailure("GET", "/_catalog/api/v1/namespaces", 502);
    const unavailable = page.waitForResponse((r) =>
      new URL(r.url()).pathname.endsWith("/_catalog/api/v1/namespaces"),
    );
    await button("Refresh").click();
    const reply = await unavailable;
    expect(reply.status()).toBe(502);
    expect((await reply.json()).code).toBe("catalog_unavailable");
    await expect(page.getByRole("alert")).toContainText(
      "The Catalog is unavailable. Try again shortly.",
    );
    await expect(savedNamespace).toHaveCount(0);
    expect(mutations).toBe(0);
  } finally {
    await api(true);
  }
  await page
    .getByRole("alert")
    .getByRole("button", { name: "Reload", exact: true })
    .click();
  await expect(savedNamespace).toBeVisible();
  await expect(savedNamespace).toHaveAttribute("href", savedHref);
  await savedNamespace.click();
  await expect(field("Display name")).toHaveValue("Packaged API recovery");
  expect(mutations).toBe(0);
  page.off("request", observe);
  const apiAfter = await processRead("/_catalog/api/v1/service-information");
  const uiAfterAPI = await processRead("/_status");
  expect(apiAfter.instanceId).not.toBe(apiBefore.instanceId);
  // Wall-clock ordering does not establish restart; execution identity does.
  expect(apiAfter.startedAt).not.toBe(apiBefore.startedAt);
  expect(uiAfterAPI.instanceId).toBe(uiBefore.instanceId);
  expect(uiAfterAPI.startedAt).toBe(uiBefore.startedAt);
  expect(uiAfterAPI.uptimeSeconds).toBeGreaterThanOrEqual(
    uiBefore.uptimeSeconds,
  );
  phase("compose-ui-restart");
  await ui();
  const uiAfter = await processRead("/_status");
  const apiAfterUI = await processRead("/_catalog/api/v1/service-information");
  expect(uiAfter.instanceId).not.toBe(uiBefore.instanceId);
  expect(uiAfter.startedAt).not.toBe(uiBefore.startedAt);
  expect(apiAfterUI.instanceId).toBe(apiAfter.instanceId);
  expect(apiAfterUI.startedAt).toBe(apiAfter.startedAt);
  await visit("/service");
  await expect(
    page.getByText(apiAfterUI.instanceId, { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText(uiAfter.instanceId, { exact: true }),
  ).toBeVisible();
  phase("compose-tenant-settings");
  await visit("/settings");
  await expect(
    page.getByRole("heading", { name: "Tenant settings", exact: true }),
  ).toBeVisible();
  const settingsPath = origin + "/_catalog/api/v1/tenant-settings";
  const settingsRead = async () =>
    page.evaluate(async (path) => {
      const reply = await fetch(path, {
        headers: { "X-Catalog-Request": "1" },
        credentials: "same-origin",
      });
      if (!reply.ok) throw new Error("Owned settings read failed");
      return { value: await reply.json(), tag: reply.headers.get("ETag") };
    }, settingsPath);
  const initialSettings = await settingsRead();
  expect(initialSettings.value.revision).toBe(0);
  const syncControl = page.getByRole("combobox", {
    name: "Automatic source sync",
    exact: true,
  });
  const intervalControl = page.getByRole("textbox", {
    name: "Automatic sync interval",
    exact: true,
  });
  const attemptsControl = page.getByRole("spinbutton", {
    name: "New Job maximum attempts",
    exact: true,
  });
  await syncControl.selectOption("paused");
  await page
    .getByRole("combobox", {
      name: "Automatic sync interval source",
      exact: true,
    })
    .selectOption("override");
  await page
    .getByRole("combobox", {
      name: "New Job maximum attempts source",
      exact: true,
    })
    .selectOption("override");
  await intervalControl.fill("1m30.123456789s");
  await attemptsControl.fill("2");
  await field("Reason").fill("Owned packaged settings recovery");
  const submittedSettings = [];
  const observeSettings = (request) => {
    if (request.url() !== settingsPath || request.method() !== "POST") return;
    submittedSettings.push({
      body: request.postData(),
      key: request.headers()["idempotency-key"],
      tag: request.headers()["if-match"],
    });
  };
  page.on("request", observeSettings);
  settingsFault.arm();
  network.expectFailure("POST", "/_catalog/api/v1/tenant-settings", 0);
  try {
    await button("Save settings").click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    const retry = page.getByRole("button", {
      name: "Retry same command",
      exact: true,
    });
    await expect(retry).toBeVisible();
    expect(settingsFault.dropped()).toBe(1);
    const [replayed] = await Promise.all([
      page.waitForResponse(
        (reply) =>
          reply.url() === settingsPath && reply.request().method() === "POST",
      ),
      retry.click(),
    ]);
    expect(replayed.status()).toBe(200);
    expect(replayed.headers()["idempotency-replayed"]).toBe("true");
    const replayedSettings = await replayed.json();
    expect(replayedSettings.revision).toBe(1);
    expect((await settingsRead()).value).toEqual(replayedSettings);
    expect(submittedSettings).toHaveLength(2);
    expect(submittedSettings[0].key).toBeTruthy();
    expect(submittedSettings[0].tag).toBe(initialSettings.tag);
    expect(submittedSettings[1]).toEqual(submittedSettings[0]);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    const acknowledgement = page.getByRole("region", {
      name: "Acknowledged administration change",
    });
    await expect(acknowledgement).toContainText(
      "Replayed command acknowledgement",
    );
    await expect(acknowledgement).toContainText(
      replayed.headers()["x-request-id"],
    );
    const investigated = page.waitForResponse(
      (reply) =>
        new URL(reply.url()).pathname ===
        new URL(origin + "/_catalog/api/v1/audit-events").pathname,
    );
    await acknowledgement
      .getByRole("link", { name: "Investigate change", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: "Audit investigation", exact: true }),
    ).toBeVisible();
    await button("Search audit events").click();
    const auditReply = await investigated;
    expect(auditReply.status()).toBe(200);
    const auditQuery = new URL(auditReply.url()).searchParams;
    expect(auditQuery.get("action")).toBe("tenant.settings.changed");
    expect(auditQuery.get("targetType")).toBe("tenant_settings");
    expect(auditQuery.get("targetId")).toBe(replayedSettings.tenantId);
    expect(auditQuery.has("requestId")).toBe(false);
    const auditPage = await auditReply.json();
    expect(auditPage.items).toHaveLength(1);
    expect(auditPage.items[0].requestId).not.toBe(
      replayed.headers()["x-request-id"],
    );
    expect(auditPage.items[0].details.facts.afterRevision).toBe(1);
    await visit("/settings");
  } finally {
    page.off("request", observeSettings);
  }
  // The private handoff returns through a new document. Finish its Settings and
  // capability reads before deliberately stopping this invocation's API.
  await expect(syncControl).toHaveValue("paused");
  await expect(intervalControl).toHaveValue("1m30.123456789s");
  await expect(attemptsControl).toHaveValue("2");
  await openDisclosure(page, "Related controls and deployment settings");
  await expect(button("Refresh capability evidence")).toBeEnabled();
  expect(network.results()).toEqual([]);
  const beforeRestart = await settingsRead();
  expect(beforeRestart.value.overrides).toEqual({
    automaticSourceSyncEnabled: false,
    automaticSourceSyncInterval: "1m30.123456789s",
    newJobMaxAttempts: 2,
  });
  await api(false);
  await api(true);
  await page.reload();
  await expect(syncControl).toHaveValue("paused");
  await expect(intervalControl).toHaveValue("1m30.123456789s");
  await expect(attemptsControl).toHaveValue("2");
  await openDisclosure(page, "Related controls and deployment settings");
  await expect(button("Refresh capability evidence")).toBeEnabled();
  expect(network.results()).toEqual([]);
  expect(await settingsRead()).toEqual(beforeRestart);
  expect((await processRead("/_status")).instanceId).toBe(uiAfter.instanceId);
  expect(
    (await processRead("/_catalog/api/v1/service-information")).instanceId,
  ).not.toBe(apiAfterUI.instanceId);
  await page
    .getByRole("combobox", {
      name: "Automatic sync interval source",
      exact: true,
    })
    .selectOption("inherit");
  await field("Reason").fill("Inherit only the packaged interval");
  await command("Save settings");
  const singleReset = await settingsRead();
  expect(singleReset.value.revision).toBe(2);
  expect(singleReset.value.overrides).toEqual({
    automaticSourceSyncEnabled: false,
    newJobMaxAttempts: 2,
  });
  await field("Reason").fill("Reset all packaged overrides");
  await command("Use deployment defaults for all settings");
  const resetSettings = await settingsRead();
  expect(resetSettings.value.revision).toBe(3);
  expect(resetSettings.value.overrides).toEqual({});
  expect(resetSettings.value.effective).toEqual(
    initialSettings.value.deploymentDefaults,
  );
  const settingsAudit = await page.evaluate(async (path) => {
    const reply = await fetch(path, {
      headers: { "X-Catalog-Request": "1" },
      credentials: "same-origin",
    });
    if (!reply.ok) throw new Error("Owned settings audit read failed");
    return reply.json();
  }, origin + "/_catalog/api/v1/audit-events?action=tenant.settings.changed&limit=100");
  expect(settingsAudit.items).toHaveLength(3);
  phase("compose-settings-history-restore");
  await button("Read settings history").click();
  const settingsHistory = page.getByRole("region", {
    name: "Settings history",
    exact: true,
  });
  await expect(settingsHistory.locator("article")).toHaveCount(3);
  const priorPolicy = settingsHistory.locator("article").filter({
    has: page.getByRole("heading", { name: "Revision 2 → 3", exact: true }),
  });
  page.once("dialog", (dialog) => dialog.accept());
  await priorPolicy
    .getByRole("button", {
      name: "Use revision 2 values in draft",
      exact: true,
    })
    .click();
  await expect(field("Reason")).toHaveValue("");
  await expect(button("Save settings")).toBeDisabled();
  expect((await settingsRead()).value).toEqual(resetSettings.value);
  await expect(syncControl).toHaveValue("paused");
  await expect(attemptsControl).toHaveValue("2");
  await field("Reason").fill("New packaged restore from recorded overrides");
  await command("Save settings");
  expect((await settingsRead()).value).toMatchObject({
    revision: 4,
    overrides: { automaticSourceSyncEnabled: false, newJobMaxAttempts: 2 },
  });
  await field("Reason").fill("Return owned policy to inheritance");
  await command("Use deployment defaults for all settings");
  expect((await settingsRead()).value).toMatchObject({
    revision: 5,
    overrides: {},
  });
  await login("local-reader");
  await expect(link("Settings")).toHaveCount(0);
  network.expectFailure("GET", "/_catalog/api/v1/tenant-settings", 403);
  expect(
    await page.evaluate(
      async (path) =>
        (
          await fetch(path, {
            headers: { "X-Catalog-Request": "1" },
            credentials: "same-origin",
          })
        ).status,
      settingsPath,
    ),
  ).toBe(403);
  phase("compose-mcp-client-configuration");
  const native = JSON.parse(
    await readFile(
      new URL("../../samples/cards/order-mcp.json", import.meta.url),
      "utf8",
    ),
  );
  const endpointPattern = new URL(native.remotes[0].url).origin + "/**";
  let endpointRequests = 0;
  const blockEndpoint = async (route) => {
    endpointRequests++;
    await route.abort();
  };
  await page.route(endpointPattern, blockEndpoint);
  await visit("/explore");
  await page
    .getByRole("combobox", { name: "Kind", exact: true })
    .selectOption("mcp_server");
  await expect(page.locator("tbody tr")).toHaveCount(1);
  await page.locator("tbody a").click();
  const configuration = page.getByRole("region", {
    name: "MCP client configuration",
    exact: true,
  });
  await expect(configuration).toBeVisible();
  const releaseID = new URL(page.url()).pathname.split("/").at(-1);
  expect(releaseID).toMatch(/^[a-f\d-]{36}$/i);

  for (const format of ["mcp-vscode", "mcp-claude-code", "mcp-cursor"]) {
    await configuration
      .getByRole("combobox", { name: "MCP client", exact: true })
      .selectOption(format);
    await expect(configuration.getByRole("link")).toHaveCount(0);
    await configuration
      .getByRole("button", {
        name: "Prepare client configuration",
        exact: true,
      })
      .click();
    const save = configuration.getByRole("link", {
      name: "Download client configuration",
      exact: true,
    });
    await expect(save).toBeVisible();
    const pending = page.waitForEvent("download");
    await save.click();
    const download = await pending;
    expect(download.suggestedFilename()).toBe("mcp.json");
    const actual = JSON.parse(await readFile(await download.path(), "utf8"));
    const wrapper = format === "mcp-vscode" ? "servers" : "mcpServers";
    const entry =
      format === "mcp-cursor"
        ? { url: native.remotes[0].url }
        : { type: "http", url: native.remotes[0].url };
    expect(actual).toEqual({ [wrapper]: { [`catalog-${releaseID}`]: entry } });
  }
  expect(endpointRequests).toBe(0);
  await page.unroute(endpointPattern, blockEndpoint);
  await login("local-reader");
  await expect(
    page.getByRole("link", {
      name: "Download client configuration",
      exact: true,
    }),
  ).toHaveCount(0);
  phase("compose-reader-handoff");
  await visit("/explore");
  await page
    .getByRole("combobox", { name: "Kind", exact: true })
    .selectOption("skill");
  await expect(page.locator("tbody tr")).toHaveCount(1);
  await page.locator("tbody a").click();
  await expect(
    page.getByRole("region", { name: "Resource handoff" }),
  ).toBeVisible();
  const retained = page.waitForResponse(
    (response) =>
      response.url().includes("/artifacts/") &&
      response.request().method() === "GET",
  );
  await button("Prepare retained document download").click();
  const response = await retained;
  expect(response.status()).toBe(200);
  const exact = await response.body();
  const saved = page.waitForEvent("download");
  await link("Save retained document").click();
  const download = await saved;
  expect((await readFile(await download.path())).equals(exact)).toBe(true);
  phase("compose-source-inspection");
  await login("local-admin");
  await visit("/sources/new");
  await field("Source URL").fill(
    "https://federation-fixture:8443/.well-known/ard.json",
  );
  await field("Network policy reference").fill("functional-fixture");
  await command("Inspect source");
  await link("Review inspection and connect").click();
  await expect(
    page.getByRole("heading", { name: "Inspection summary" }),
  ).toBeVisible();
  await expect(
    page
      .locator("dt")
      .filter({ hasText: /^Entries found$/ })
      .locator("+ dd"),
  ).toHaveText("1"); // The inspected manifest advertises one Registry; sync enumerates its two entries.
  await field("Display name").fill("Browser upstream");
  await field("ANS verification policy").fill("operator-ans");
  await field("Search mode").selectOption("hybrid");
  await field("Reason").fill("Owned browser federation test");
  await field("Agents").check();
  for (const name of ["Agents", "MCP Servers", "Skills", "Plugins"])
    await page.getByRole("checkbox", { name, exact: true }).uncheck();
  await button("Connect inspected source").click();
  await expect(page.getByRole("alert")).toContainText(
    "Select at least one source content scope filter.",
  );
  await expect(page.getByRole("dialog")).toHaveCount(0);
  for (const name of [
    "Scope identifier",
    "Scope version",
    "Scope publisher",
    "Scope tag",
    "Excluded identifier",
  ]) {
    await button(`Add ${name.toLowerCase()}`).click();
    await field(`${name} 1`).fill("temporary, temporary");
    await expect(field(`${name} 1`)).toHaveValue("temporary, temporary");
    await button(`Remove ${name.toLowerCase()} 1`).click();
    await expect(field(`${name} 1`)).toHaveCount(0);
  }
  await field("Synchronization").selectOption("automatic");
  await field("Synchronization").selectOption("manual_review");
  await field("Referral traversal").selectOption("referrals");
  await field("Referral traversal").selectOption("none");
  await field("Agents").check();
  await field("Enable live queries").check();
  await field("I acknowledge queries are sent to this upstream").check();
  const topologyAPIPath = new URL(origin + "/_catalog/api/v1").pathname;
  const topologyRequests = [];
  const observeTopologyRequest = (request) => {
    const pathname = new URL(request.url()).pathname;
    if (!pathname.startsWith(topologyAPIPath + "/")) return;
    const path = pathname.slice(topologyAPIPath.length);
    if (
      path === "/publication/hosts" ||
      /^\/catalog\/(search|explore|resources)$/.test(path) ||
      path === "/federation/sources:live-search" ||
      (request.method() !== "GET" && path.startsWith("/federation/"))
    )
      topologyRequests.push({ method: request.method(), path });
  };
  page.on("request", observeTopologyRequest);
  await openDisclosure(page, "Advanced source participation");
  await button("Edit topology as JSON").click();
  await field("Topology JSON").fill('{"unexpected":true}');
  await button("Connect inspected source").click();
  await expect(page.getByRole("alert")).toContainText(
    "Topology accepts only referrals, nesting and discoveryAssignments.",
  );
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await field("Topology JSON").fill(
    JSON.stringify({
      referrals: { enabled: false },
      nesting: { enabled: false },
      discoveryAssignments: [],
    }),
  );
  await button("Use guided topology").click();
  await button("Add discovery assignment").click();
  expect(topologyRequests).toEqual([]);
  const hostInventoryResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      new URL(response.url()).pathname ===
        topologyAPIPath + "/publication/hosts",
  );
  await button("Load configured host choices").click();
  const hostInventory = await hostInventoryResponse;
  expect(hostInventory.status()).toBe(200);
  expect((await hostInventory.json()).items).toEqual([
    expect.objectContaining({ host: "127.0.0.1", mode: "anonymous" }),
    expect.objectContaining({
      host: "private.catalog.test",
      mode: "authenticated",
    }),
  ]);
  const configuredSourceHost = page.getByRole("combobox", {
    name: "Configured discovery host 1",
    exact: true,
  });
  await expect(
    configuredSourceHost.locator("option:not([value='']):not([disabled])"),
  ).toHaveText([
    "127.0.0.1 (anonymous)",
    "private.catalog.test (authenticated)",
  ]);
  await configuredSourceHost.selectOption("127.0.0.1");
  await expect(field("Discovery host 1")).toHaveValue("127.0.0.1");
  // Manual entry remains independent of publication inventory and preserves
  // the exact authenticated host selected for this owned source.
  await field("Discovery host 1").fill("private.catalog.test");
  await page
    .getByRole("combobox", { name: "Discovery audience 1", exact: true })
    .selectOption("authenticated");
  await button("Add discovery assignment").click();
  await field("Discovery host 2").fill("127.0.0.1");
  page.once("dialog", (dialog) => dialog.dismiss());
  await button("Remove discovery assignment 2").click();
  await expect(field("Discovery host 2")).toHaveValue("127.0.0.1");
  page.once("dialog", (dialog) => dialog.accept());
  await button("Remove discovery assignment 2").click();
  await expect(field("Discovery host 2")).toHaveCount(0);
  await expect(field("Discovery host 1")).toHaveValue("private.catalog.test");
  await expect(
    page.getByRole("combobox", { name: "Discovery audience 1", exact: true }),
  ).toHaveValue("authenticated");
  await button("Hide configured host choices").click();
  await expect(configuredSourceHost).toHaveCount(0);
  expect(topologyRequests).toEqual([
    { method: "GET", path: "/publication/hosts" },
  ]);
  const topology = {
    referrals: { enabled: false },
    nesting: { enabled: false },
    discoveryAssignments: [
      { host: "private.catalog.test", audience: "authenticated" },
    ],
  };
  await button("Edit topology as JSON").click();
  expect(JSON.parse(await field("Topology JSON").inputValue())).toEqual(
    topology,
  );
  await field("Reason").fill("Owned browser federation test");
  await button("Add excluded identifier").click();
  await field("Excluded identifier 1").fill("legacy:browser-hidden");
  const connectedResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/api/v1/federation/sources"),
  );
  await command("Connect inspected source");
  const connected = await connectedResponse;
  expect(connected.status()).toBe(201);
  expect(connected.request().postDataJSON().trustVerificationPolicy).toBe(
    "operator-ans",
  );
  const connectedSource = await connected.json();
  expect(connectedSource.trustVerificationPolicy).toBe("operator-ans");
  expect(connected.request().postDataJSON()).toMatchObject(topology);
  expect(connectedSource).toMatchObject(topology);
  expect(connected.request().postDataJSON().scope).toEqual({
    kinds: ["agent"],
    excludedIdentifiers: ["legacy:browser-hidden"],
  });
  expect(connectedSource.scope).toEqual({
    kinds: ["agent"],
    excludedIdentifiers: ["legacy:browser-hidden"],
  });
  await link("Open source").click();
  const sourceURL = page.url();
  const sourceID = new URL(sourceURL).pathname.split("/").pop();
  await expect(button("Sync history")).toBeVisible();
  const sourceReadback = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      new URL(response.url()).pathname ===
        `${topologyAPIPath}/federation/sources/${sourceID}`,
  );
  await page.reload();
  const retainedSource = await sourceReadback;
  expect(retainedSource.status()).toBe(200);
  expect(await retainedSource.json()).toMatchObject({
    id: sourceID,
    ...topology,
    scope: {
      kinds: ["agent"],
      excludedIdentifiers: ["legacy:browser-hidden"],
    },
  });
  await expect(button("Sync history")).toBeVisible();
  expect(topologyRequests).toEqual([
    { method: "GET", path: "/publication/hosts" },
    { method: "POST", path: "/federation/sources" },
  ]);
  page.off("request", observeTopologyRequest);
  // Verification is deliberately off in this existing fixture. Exercise real
  // UI-to-API refusal, not a mocked positive verdict or a second ANS campaign.
  const unavailableAssessment = async (selection, reason) => {
    await page
      .locator("section.workspace-panel")
      .filter({
        has: page.getByRole("heading", {
          name: "Request ANS assessment",
          exact: true,
        }),
      })
      .last()
      .getByRole("textbox", { name: "Reason", exact: true })
      .fill(reason);
    const path = `/api/v1/federation/sources/${sourceID}/trust-assessments`;
    network.expectFailure("POST", "/_catalog" + path, 503);
    const reply = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" && response.url().endsWith(path),
    );
    await button("Request ANS assessment").click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    const refused = await reply;
    expect(refused.status()).toBe(503);
    expect((await refused.json()).code).toBe("DEPENDENCY_UNAVAILABLE");
    expect(refused.request().postDataJSON()).toEqual({ ...selection, reason });
    expect(refused.request().headers()["idempotency-key"]).toBeTruthy();
    expect(refused.request().headers()["if-match"]).toBeUndefined();
    await expect(page.getByRole("dialog").getByRole("alert")).toContainText(
      "DEPENDENCY_UNAVAILABLE",
    );
    await expect(
      page.getByText(/Verification or the selected policy is unavailable/),
    ).toBeVisible();
    await expect(page.getByText(/Assessment Job:/)).toHaveCount(0);
    // Read the real queue, not the confirmation UI, to prove no verifier Job exists.
    const trustJobs = await page.evaluate(async (url) => {
      const response = await fetch(url, {
        headers: { "X-Catalog-Request": "1" },
      });
      if (!response.ok) throw new Error("Job control read failed");
      return response.json();
    }, origin + "/_catalog/api/v1/jobs?limit=25");
    expect(trustJobs.nextCursor).toBeFalsy();
    expect(
      trustJobs.items.some((job) => job.kind === "source.trust_verify"),
    ).toBe(false);
    // The controlled 503 is known to occur before enqueue. Explicitly leave
    // the retained command; never submit a replacement key or hide a retry.
    page.once("dialog", (dialog) => dialog.accept());
  };
  await visit("/sources");
  await expect(link("Browser upstream")).toBeVisible();
  await field("Source name (exact match)").fill("Browser upstream");
  await page
    .getByRole("combobox", { name: "Source status", exact: true })
    .selectOption("active");
  const filteredDirectory = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return (
      response.request().method() === "GET" &&
      url.pathname.endsWith("/api/v1/federation/sources") &&
      url.searchParams.get("displayName") === "Browser upstream" &&
      url.searchParams.get("status") === "active"
    );
  });
  await button("Apply source filters").click();
  const directoryReply = await filteredDirectory;
  expect(directoryReply.status()).toBe(200);
  expect(
    (await directoryReply.json()).items.map((source) => source.id),
  ).toEqual([sourceID]);
  await link("Browser upstream").click();
  const testedReply = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith(`/api/v1/federation/sources/${sourceID}:test`),
  );
  await command("Test source");
  const tested = await (await testedReply).json();
  expect(tested).toMatchObject({
    sourceId: sourceID,
    sourceRevision: connectedSource.revision + 1,
    status: "succeeded",
    httpStatus: 200,
  });
  await expect(page.getByRole("status")).toContainText("Source test returned.");
  const testOutcome = page.getByRole("region", {
    name: "Recorded source test outcome",
    exact: true,
  });
  await expect(testOutcome).toContainText("Source test: Succeeded");
  await expect(testOutcome).toContainText("HTTP 200");
  await expect(
    testOutcome
      .locator("dt")
      .filter({ hasText: /^Observed entry count$/ })
      .locator("+ dd"),
  ).toHaveText(String(tested.entryCount));
  const sync = async () => {
    await page.goto(sourceURL);
    await field("Source action").selectOption("sync");
    await command("Synchronize source");
    await expect(page.getByRole("status")).toContainText(
      "acceptance is not completion",
    );
    await link("Inspect synchronization Job").click();
    await expect(
      page.getByRole("heading", {
        name: "Source synchronization",
        exact: true,
      }),
    ).toBeVisible();
    return page.url();
  };
  phase("compose-source-sync");
  await sync();
  await waitJob("succeeded");
  await expect(button("Retry job")).toHaveCount(0);
  await expect(page.locator("pre")).toContainText('"state": "complete"');
  await expect(
    page.getByText(/produced a complete source snapshot/),
  ).toBeVisible();
  await expect(link("Open resulting source")).toHaveAttribute(
    "href",
    new URL(sourceURL).pathname,
  );
  const jobSnapshotHref = await link("Inspect source snapshot").getAttribute(
    "href",
  );
  await link("Inspect source snapshot").click();
  await expect(
    page.getByRole("heading", { name: "Source snapshot", exact: true }),
  ).toBeVisible();
  await page.goto(sourceURL);
  await button("Sync history").click();
  const snapshotHistory = page.locator("section").filter({
    has: page.getByRole("heading", {
      name: "Synchronization snapshots",
      exact: true,
    }),
  });
  const snapshotLink = snapshotHistory.locator('a[href*="/source-snapshots/"]');
  await expect(snapshotLink).toHaveCount(1);
  const snapshotRow = snapshotLink.locator("xpath=ancestor::article");
  for (const [label, value] of [
    ["Snapshot state", "Complete"],
    ["Entries", "2"],
    ["Withdrawal authority", "No"],
  ])
    await expect(
      snapshotRow
        .locator("dt")
        .filter({ hasText: new RegExp(`^${label}$`) })
        .locator("+ dd"),
    ).toHaveText(value);
  await expect(link("Inspect last complete snapshot")).toHaveAttribute(
    "href",
    await snapshotLink.getAttribute("href"),
  );
  const retainedSnapshotHref = await snapshotLink.getAttribute("href");
  expect(retainedSnapshotHref).toBe(jobSnapshotHref);
  await visit("/sources");
  await expect(link("Inspect last complete snapshot")).toHaveAttribute(
    "href",
    retainedSnapshotHref,
  );
  await expect(
    page
      .locator("dt")
      .filter({ hasText: /^Last sync attempt finished$/ })
      .locator("+ dd"),
  ).not.toHaveText("No finished synchronization attempt recorded");
  await expect(link("Synchronization history")).toHaveAttribute(
    "href",
    new URL(sourceURL).pathname,
  );
  await link("Inspect last complete snapshot").click();
  await expect(
    page.getByRole("heading", { name: "Snapshot: Complete", exact: true }),
  ).toBeVisible();
  await expect(
    page
      .locator("dt")
      .filter({ hasText: /^Entry count$/ })
      .locator("+ dd"),
  ).toHaveText("2");
  phase("compose-ans-indexed-selection");
  const sourceRead = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      response.url().endsWith(`/api/v1/federation/sources/${sourceID}`),
  );
  await page.goto(sourceURL);
  const currentSource = await (await sourceRead).json();
  expect(currentSource.trustVerificationPolicy).toBe("operator-ans");
  await button("ANS registration").click();
  await expect(page.getByText(/Policy: operator-ans/)).toBeVisible();
  const observedPage = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      new URL(response.url()).pathname.endsWith("/api/v1/observations"),
  );
  await button("Choose indexed entry").click();
  const observations = (await (await observedPage).json()).items;
  const indexed = observations.find(
    (item) => item.identifier === "urn:air:functional.example:default:one",
  );
  expect(indexed).toBeTruthy();
  const rawReply = page.waitForResponse((response) =>
    response.url().endsWith(`/api/v1/observations/${indexed.id}/raw`),
  );
  await page
    .locator("article")
    .filter({
      has: page.getByRole("heading", { name: indexed.identifier, exact: true }),
    })
    .getByRole("button", { name: "Select indexed entry", exact: true })
    .click();
  const rawEntry = await (await rawReply).json();
  await expect(
    page.getByRole("heading", { name: "Request ANS assessment", exact: true }),
  ).toBeVisible();
  await unavailableAssessment(
    {
      sourceRevision: currentSource.revision,
      observationId: indexed.id,
      identifier: indexed.identifier,
      versionPresent: indexed.versionPresent,
      ...(indexed.versionPresent ? { version: indexed.version } : {}),
      mediaType: rawEntry.type,
      delivery: Object.hasOwn(rawEntry, "url") ? "url" : "data",
      exactEntryDigest: indexed.exactEntryDigest,
    },
    "Check the indexed registration",
  );
  await page.goto(new URL(retainedSnapshotHref, origin).href);
  await page.getByText("Snapshot and retrievals", { exact: true }).click();
  await expect(page.locator("pre")).toContainText(
    '"enumerationComplete": true',
  );

  phase("compose-observations");
  await visit("/explore");
  await button("View counts").click();
  await expect(
    page.getByRole("status", { name: "Matched releases" }),
  ).toHaveText("3 matching local releases");
  // Connect-style discovery is a reader workflow, not an administrator privilege.
  await login("local-reader");
  await visit("/catalog");
  await page
    .getByRole("searchbox", { name: "Search Catalog", exact: true })
    .fill("Functional");
  const searchReply = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname.endsWith("/api/v1/catalog/search") &&
      response.request().postDataJSON()?.text === "Functional",
  );
  await button("Search").click();
  const browserResults = await (await searchReply).json();
  const cliResults = await remoteSearch({ text: "Functional" });
  const identities = (items) =>
    items.map(({ type, identifier, versionPresent, version }) => ({
      type,
      identifier,
      versionPresent,
      version,
    }));
  expect(identities(cliResults.items)).toEqual(
    identities(browserResults.items),
  );
  expect(cliResults.partial).toBe(false);
  expect(cliResults.liveCount).toBe(1);
  const liveEntry = cliResults.items.find((item) => item.type === "live");
  expect(liveEntry.versionPresent).toBe(false);
  expect(liveEntry.live.entry.type).toBe("application/a2a-agent-card+json");
  expect(liveEntry.live.entry.url).toBe(
    "https://federation-fixture:8443/live-card.json",
  );
  expect(liveEntry.live.entry.mediaType).toBeUndefined();
  for (const item of cliResults.items.filter(
    (item) => item.type === "indexed",
  )) {
    expect(item.local).toBeUndefined();
    expect(item.indexed.sourceIds).toEqual([liveEntry.live.sourceId]);
    expect(item.indexed.sourceCount).toBe(1);
    expect(item.indexed.normalizedEntryDigest).toMatch(/^sha256:[a-f0-9]{64}$/);
  }
  const firstPage = await remoteSearch({ text: "Functional", limit: 1 });
  expect(firstPage.items).toHaveLength(1);
  expect(firstPage.items[0].type).toBe("indexed");
  expect(firstPage.liveCount).toBe(0);
  expect(firstPage.nextCursor).toBeTruthy();
  const nextPage = await remoteSearch({
    text: "Functional",
    limit: 1,
    cursor: firstPage.nextCursor,
  });
  expect(nextPage.liveCount).toBe(0);
  expect(nextPage.items).toHaveLength(1);
  expect(nextPage.items[0].type).toBe("indexed");
  expect(nextPage.items[0].identifier).not.toBe(firstPage.items[0].identifier);
  // The two indexed matches precede the unchanged live tail. Do not stop at
  // the durable boundary and silently omit the remaining live result.
  expect(nextPage.nextCursor).toBeTruthy();
  const livePage = await remoteSearch({
    text: "Functional",
    limit: 1,
    cursor: nextPage.nextCursor,
  });
  expect(livePage.items).toHaveLength(1);
  expect(livePage.items[0].type).toBe("live");
  expect(livePage.liveCount).toBe(1);
  expect(livePage.nextCursor).toBeUndefined();
  expect(
    identities([...firstPage.items, ...nextPage.items, ...livePage.items]),
  ).toEqual(identities(cliResults.items));
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  await expect(
    page
      .locator("article.resource-card")
      .filter({ hasText: "Indexed external" }),
  ).toHaveCount(2);
  await expect(
    page.locator("article.resource-card").filter({ hasText: "Live external" }),
  ).toHaveCount(1);
  // Missing optional declarations are valid ARD. Indexed omissions are
  // ordinary nonmatches; live omissions expose incomplete filter evidence.
  const declarationQuery = {
    text: "Functional",
    license: "Apache-2.0",
    categories: ["tools"],
  };
  const indexedDeclarations = await remoteSearch({
    ...declarationQuery,
    origins: ["indexed"],
  });
  expect(indexedDeclarations.items).toEqual([]);
  expect(indexedDeclarations.partial).toBe(false);
  const liveDeclarations = await remoteSearch({
    ...declarationQuery,
    origins: ["indexed", "live"],
  });
  expect(liveDeclarations.items).toEqual([]);
  expect(liveDeclarations.partial).toBe(true);
  expect(liveDeclarations.diagnostics.map((item) => item.status)).toContain(
    "insufficient_evidence",
  );
  await button("Filters").click();
  await field("License declaration").fill(declarationQuery.license);
  await button("Add category").click();
  await field("Category 1").fill("tools");
  await expect(
    page.getByRole("checkbox", { name: "Live external", exact: true }),
  ).toBeEnabled();
  const declarations = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/catalog/search") &&
      response.request().postDataJSON()?.license === declarationQuery.license,
  );
  await button("Apply filters").click();
  const declarationPage = await (await declarations).json();
  expect(declarationPage.items).toEqual([]);
  expect(declarationPage.partial).toBe(true);
  await page.getByText("Search diagnostics", { exact: true }).click();
  await expect(
    page.getByText("insufficient_evidence", { exact: true }),
  ).toBeVisible();
  await button(`Remove license: ${declarationQuery.license}`).click();
  const restoredSearch = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/api/v1/catalog/search") &&
      !response.request().postDataJSON()?.license &&
      !response.request().postDataJSON()?.categories?.length,
  );
  await button("Remove category tools").click();
  const restoredReply = await restoredSearch;
  expect(restoredReply.status()).toBe(200);
  const displayedSearch = restoredReply.request().postDataJSON();
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  // Preview metadata without fetching the returned document or treating it as a local release.
  let foreignRequests = 0;
  const observeDestination = (request) => {
    if (new URL(request.url()).origin !== new URL(origin).origin)
      foreignRequests++;
  };
  page.on("request", observeDestination);
  await button("Functional Live").click();
  const previewLinks = page.getByRole("dialog").getByRole("link");
  await expect(previewLinks).toHaveCount(1);
  await expect(previewLinks).toHaveText(
    "Agent native example (opens in a new tab)",
  );
  await expect(previewLinks).toHaveAttribute(
    "href",
    "/docs/guides/resource-authoring/#agent-retain-an-a2a-agent-card",
  );
  await expect(previewLinks).toHaveAttribute("target", "_blank");
  await expect(previewLinks).toHaveAttribute("rel", "noopener noreferrer");
  await expect(
    page.getByRole("dialog").getByRole("heading", { level: 3 }),
  ).toHaveText([
    "Overview",
    "Use this resource",
    "Technical details and evidence",
  ]);
  await expect(
    page
      .getByRole("dialog")
      .getByRole("heading", { name: /Agent interfaces|Plugin contents/ }),
  ).toHaveCount(0);
  await page.getByText("Live entry metadata", { exact: true }).click();
  await expect(page.getByRole("dialog").locator("pre")).toContainText(
    '"type": "application/a2a-agent-card+json"',
  );
  await expect(page.getByRole("dialog").locator("pre")).toContainText(
    '"url": "https://federation-fixture:8443/live-card.json"',
  );
  phase("compose-live-rating-refresh");
  const ratingWrites = [];
  const observeRating = (request) => {
    if (
      request.method() === "POST" &&
      new URL(request.url()).pathname.endsWith("/api/v1/ratings")
    )
      ratingWrites.push(request.postDataJSON());
  };
  page.on("request", observeRating);
  await button("Ratings for this version").click();
  const score = page.getByRole("radio", { name: "4 stars", exact: true });
  const consent = page.getByRole("checkbox", { name: /Allow my score/ });
  await score.check();
  await consent.check();
  const renewedSearch = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/api/v1/catalog/search"),
  );
  await button("Refresh live ratings").click();
  const renewedReply = await renewedSearch;
  expect(renewedReply.status()).toBe(200);
  expect(renewedReply.request().postDataJSON()).toEqual(displayedSearch);
  const renewedLive = (await renewedReply.json()).items.find(
    (item) => item.type === "live" && item.identifier === liveEntry.identifier,
  );
  expect(renewedLive).toBeTruthy();
  expect(renewedLive.ratings.receipt).toBeTruthy();
  await expect(
    page.getByText("Live ratings refreshed. Your rating draft is preserved.", {
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    page.getByRole("status").filter({ hasText: "Loading" }),
  ).toHaveCount(0);
  await expect(score).toBeChecked();
  await expect(consent).toBeChecked();
  expect(ratingWrites).toEqual([]);
  const ratingTarget = {
    origin: "foreign",
    sourceId: liveEntry.live.sourceId,
    presentationGeneration: liveEntry.live.presentationGeneration,
    identifier: liveEntry.identifier,
    kind: liveEntry.kind,
    versionPresent: false,
  };
  const ratingRead = (state) =>
    page.waitForResponse(
      async (response) =>
        response.request().method() === "POST" &&
        response.url().endsWith("/api/v1/ratings/read") &&
        response.status() === 200 &&
        (await response.json()).own?.state === state,
    );
  const submitted = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/api/v1/ratings"),
  );
  const savedRead = ratingRead("active");
  await button("Submit rating").click();
  await button("Confirm").click();
  const submittedReply = await submitted;
  expect(submittedReply.status()).toBe(201);
  expect(submittedReply.request().headers()["idempotency-key"]).toBeTruthy();
  const receipt = await submittedReply.json();
  expect(receipt.revision).toBe(1);
  const savedReply = await savedRead;
  expect(savedReply.status()).toBe(200);
  expect(await savedReply.json()).toMatchObject({
    target: ratingTarget,
    count: 1,
    average: 4,
    own: {
      id: receipt.id,
      revision: 1,
      target: ratingTarget,
      score: 4,
      consent: true,
      state: "active",
    },
  });
  expect(ratingWrites).toEqual([
    {
      target: ratingTarget,
      receipt: renewedLive.ratings.receipt,
      score: 4,
      consent: true,
    },
  ]);
  const withdrawn = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith(`/api/v1/ratings/${receipt.id}:withdraw`),
  );
  const withdrawnRead = ratingRead("withdrawn");
  await button("Withdraw rating").click();
  await button("Confirm").click();
  const withdrawCommand = await withdrawn;
  expect(withdrawCommand.status()).toBe(200);
  expect(withdrawCommand.request().headers()["if-match"]).toBe('"rev-1"');
  const withdrawnReply = await withdrawnRead;
  expect(withdrawnReply.status()).toBe(200);
  expect(await withdrawnReply.json()).toMatchObject({
    target: ratingTarget,
    count: 0,
    own: { id: receipt.id, revision: 2, state: "withdrawn" },
  });
  page.off("request", observeRating);
  await button("Close preview").click();
  page.off("request", observeDestination);
  expect(foreignRequests).toBe(0);
  await page
    .getByRole("searchbox", { name: "Search Catalog", exact: true })
    .fill("Unsubmitted indexed return draft");
  await page
    .locator("article.resource-card")
    .first()
    .getByRole("button")
    .first()
    .click();
  await expect(
    page.getByText(
      "External metadata is not a locally reviewed or approved resource.",
      { exact: true },
    ),
  ).toBeVisible();
  await link("Open current indexed record").click();
  const indexedURL = page.url();
  await expect(
    page.getByRole("heading", { name: /Functional (One|Two)/ }),
  ).toBeVisible();
  const returnedSearch = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname.endsWith("/api/v1/catalog/search"),
  );
  await page.goBack();
  const returnedReply = await returnedSearch;
  expect(returnedReply.status()).toBe(200);
  expect(returnedReply.request().postDataJSON()).toEqual(displayedSearch);
  await expect(
    page.getByRole("searchbox", { name: "Search Catalog", exact: true }),
  ).toHaveValue("Unsubmitted indexed return draft");
  await expect(page.locator("article.resource-card")).toHaveCount(3);
  const forwardedRead = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      new URL(response.url()).pathname.endsWith(
        "/api/v1/catalog/indexed-record",
      ),
  );
  await page.goForward();
  expect((await forwardedRead).status()).toBe(200);
  await expect(page).toHaveURL(indexedURL);
  await expect(
    page.getByRole("heading", { name: /Functional (One|Two)/ }),
  ).toBeVisible();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: /Functional (One|Two)/ }),
  ).toBeVisible();
  await login("local-admin");
  phase("compose-source-artwork");
  await page.goto(indexedURL);
  const artworkRequestPath = `/_catalog/api/v1/federation/sources/${sourceID}/artwork`;
  const artworkPath = new URL(origin + artworkRequestPath).pathname;
  network.expectFailure("GET", artworkRequestPath, 404);
  await button("Manage local artwork").click();
  await page
    .getByRole("combobox", { name: "Icon behavior", exact: true })
    .selectOption("suppress");
  await page
    .getByRole("combobox", { name: "Screenshot behavior", exact: true })
    .selectOption("suppress");
  await field("Artwork override reason").fill(
    "Hide artwork in this consuming Catalog",
  );
  const overridden = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname === artworkPath,
  );
  await command("Save local artwork override");
  const overrideReply = await overridden;
  expect(overrideReply.status()).toBe(200);
  expect(overrideReply.request().headers()["if-match"]).toBeUndefined();
  const firstOverride = await overrideReply.json();
  const indexedSelection = new URL(indexedURL).searchParams;
  expect(firstOverride.target).toMatchObject({
    sourceId: sourceID,
    identifier: indexedSelection.get("identifier"),
    versionPresent: indexedSelection.get("versionPresent") === "true",
    kind: "agent",
  });
  if (firstOverride.target.versionPresent)
    expect(firstOverride.target.version).toBe(indexedSelection.get("version"));
  expect(firstOverride.revision).toBe(1);
  expect(firstOverride.icon).toEqual({ mode: "suppress" });
  expect(firstOverride.screenshots).toEqual({ mode: "suppress" });
  await expect(
    page.getByRole("combobox", { name: "Icon behavior", exact: true }),
  ).toHaveValue("suppress");
  await expect(
    page.getByText(/Refresh the override head and history here/),
  ).toBeVisible();
  const existingHead = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      new URL(response.url()).pathname === artworkPath,
  );
  await button("Refresh artwork override").click();
  const headReply = await existingHead;
  expect((await headReply.json()).id).toBe(firstOverride.id);
  const overrideTag = headReply.headers().etag;
  expect(overrideTag).toBeTruthy();
  await expect(
    page.getByRole("combobox", { name: "Screenshot behavior", exact: true }),
  ).toHaveValue("suppress");
  await button("Reset both choices to inherit").click();
  await field("Artwork override reason").fill(
    "Restore source presentation without changing the peer entry",
  );
  const restored = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname === artworkPath,
  );
  await command("Save local artwork override");
  const resetReply = await restored;
  expect(resetReply.status()).toBe(200);
  expect(resetReply.request().headers()["if-match"]).toBe(overrideTag);
  const resetOverride = await resetReply.json();
  expect(resetOverride.revision).toBe(2);
  expect(resetOverride.target).toEqual(firstOverride.target);
  expect(resetOverride.icon).toEqual({ mode: "inherit" });
  expect(resetOverride.screenshots).toEqual({ mode: "inherit" });
  const historyResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      new URL(response.url()).pathname === artworkPath + "/history",
  );
  await page.getByText("Artwork override history", { exact: true }).click();
  const history = await (await historyResponse).json();
  expect(history.items.map((item) => item.id).sort()).toEqual(
    [firstOverride.id, resetOverride.id].sort(),
  );
  expect(history.nextCursor).toBeUndefined();
  await expect(
    page.getByText("Artwork revision 1", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Artwork revision 2", { exact: true }),
  ).toBeVisible();

  phase("compose-source-artwork-reader");
  // Add only evidence reads to the exact Discovery Reader template. Its
  // existing discovery authority admits the containing indexed-record page.
  await visit("/access-roles/new");
  await field("Role name").fill("Artwork evidence reader");
  await page
    .getByRole("checkbox", { name: "source.read", exact: true })
    .check();
  await field("Metadata change reason").fill(
    "Qualify read-only retained artwork",
  );
  await command("Create role");
  await link("Open saved role").click();
  const artworkReaderRoleID = new URL(page.url()).pathname.split("/").at(-1);
  expect(artworkReaderRoleID).toMatch(/^[0-9a-f-]{36}$/);
  const readerMembershipID = await page.evaluate(async (url) => {
    const response = await fetch(url, {
      headers: { "X-Catalog-Request": "1" },
    });
    if (!response.ok) throw new Error("Owned membership lookup failed");
    const members = await response.json();
    return members.items.find(
      (member) => member.subject === "local-template-discovery-reader",
    )?.id;
  }, origin + "/_catalog/api/v1/memberships?limit=100");
  expect(readerMembershipID).toMatch(/^[0-9a-f-]{36}$/);
  await visit("/access-assignments");
  await openDisclosure(page, "Grant access");
  const readerGrant = page.locator("section.workspace-panel").filter({
    has: page.getByRole("heading", {
      name: "Grant access assignment",
      exact: true,
    }),
  });
  await readerGrant
    .getByLabel("Role definition ID", { exact: true })
    .fill(artworkReaderRoleID);
  await readerGrant
    .getByLabel("Member or group ID", { exact: true })
    .fill(readerMembershipID);
  await readerGrant
    .getByRole("combobox", { name: "Assignment scope", exact: true })
    .selectOption("tenant");
  await readerGrant
    .getByLabel("Reason", { exact: true })
    .fill("Read evidence without editing artwork");
  const grantedReadResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname.endsWith("/api/v1/access/assignments"),
  );
  await command("Grant access assignment");
  const grantedRead = await grantedReadResponse;
  expect(grantedRead.status()).toBe(201);
  const artworkReadAssignment = await grantedRead.json();
  expect(artworkReadAssignment.id).toMatch(/^[0-9a-f-]{36}$/);
  expect(artworkReadAssignment.roleDefinitionId).toBe(artworkReaderRoleID);
  expect(artworkReadAssignment.membershipId).toBe(readerMembershipID);
  expect(artworkReadAssignment.namespaceId).toBeUndefined();
  expect(artworkReadAssignment.state).toBe("active");
  await login("local-template-discovery-reader");
  const artworkReaderPermissions = await page.evaluate(async (url) => {
    const response = await fetch(url, {
      headers: { "X-Catalog-Request": "1" },
    });
    if (!response.ok) throw new Error("Owned permission lookup failed");
    return (await response.json()).permissions;
  }, origin + "/_catalog/api/v1/me");
  expect(artworkReaderPermissions).toContain("source.read");
  expect(artworkReaderPermissions).toContain("federation.discover");
  expect(artworkReaderPermissions).not.toContain("source.manage");
  const evidenceRequests = [];
  const observeEvidence = (request) => {
    if (new URL(request.url()).pathname.startsWith(artworkPath))
      evidenceRequests.push({
        method: request.method(),
        path: new URL(request.url()).pathname,
      });
  };
  page.on("request", observeEvidence);
  await page.goto(indexedURL);
  await expect(button("Inspect local artwork")).toBeVisible();
  expect(evidenceRequests).toEqual([]);
  const readerHeadResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      new URL(response.url()).pathname === artworkPath,
  );
  await button("Inspect local artwork").click();
  const readerHead = await readerHeadResponse;
  expect(readerHead.status()).toBe(200);
  expect(await readerHead.json()).toEqual(resetOverride);
  await expect(button("Manage local artwork")).toHaveCount(0);
  await expect(button("Save local artwork override")).toHaveCount(0);
  await expect(
    page.getByRole("combobox", { name: "Icon behavior", exact: true }),
  ).toHaveCount(0);
  const currentArtwork = page.getByText("Current artwork override", {
    exact: true,
  });
  await currentArtwork.click();
  await expect(currentArtwork.locator("..").locator("pre")).toContainText(
    resetOverride.reason,
  );
  expect(evidenceRequests).toEqual([{ method: "GET", path: artworkPath }]);
  const readerHistoryResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      new URL(response.url()).pathname === artworkPath + "/history",
  );
  await page.getByText("Artwork override history", { exact: true }).click();
  const readerHistory = await readerHistoryResponse;
  expect(readerHistory.status()).toBe(200);
  expect(await readerHistory.json()).toEqual(history);
  await expect(
    page.getByText("Artwork revision 1", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Artwork revision 2", { exact: true }),
  ).toBeVisible();
  expect(evidenceRequests).toEqual([
    { method: "GET", path: artworkPath },
    { method: "GET", path: artworkPath + "/history" },
  ]);
  page.off("request", observeEvidence);
  await login("local-admin");
  await expect(button("Manage local artwork")).toBeVisible();
  await expect(
    page.getByText("Current artwork override", { exact: true }),
  ).toHaveCount(0);

  // Revoke only evidence access; the existing discovery grant must still
  // admit the record without requests for its now-private head or history.
  await visit("/access-assignments?roleDefinitionId=" + artworkReaderRoleID);
  const evidenceAssignment = page.locator(
    `[data-assignment-id="${artworkReadAssignment.id}"]`,
  );
  await evidenceAssignment
    .getByRole("button", {
      name: `Manage assignment ${artworkReadAssignment.id}`,
      exact: true,
    })
    .click();
  await page
    .locator(`[data-editor-assignment-id="${artworkReadAssignment.id}"]`)
    .getByLabel("Reason", { exact: true })
    .fill("Remove only private artwork evidence access");
  const revokedReadResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname.endsWith(
        `/api/v1/access/assignments/${artworkReadAssignment.id}:revoke`,
      ),
  );
  await command("Revoke access assignment");
  const revokedRead = await revokedReadResponse;
  expect(revokedRead.status()).toBe(200);
  expect(revokedRead.request().headers()["if-match"]).toBe(
    grantedRead.headers()["etag"],
  );
  expect((await revokedRead.json()).state).toBe("revoked");
  await login("local-template-discovery-reader");
  evidenceRequests.length = 0;
  page.on("request", observeEvidence);
  await page.goto(indexedURL);
  await expect(
    page.getByRole("heading", { name: /Functional (One|Two)/ }),
  ).toBeVisible();
  const revokedPermissions = await page.evaluate(async (url) => {
    const response = await fetch(url, {
      headers: { "X-Catalog-Request": "1" },
    });
    if (!response.ok) throw new Error("Owned permission lookup failed");
    return (await response.json()).permissions;
  }, origin + "/_catalog/api/v1/me");
  expect(revokedPermissions).toContain("federation.discover");
  expect(revokedPermissions).not.toContain("source.read");
  await expect(button("Inspect local artwork")).toHaveCount(0);
  await expect(button("Manage local artwork")).toHaveCount(0);
  await expect(
    page.getByText("Current artwork override", { exact: true }),
  ).toHaveCount(0);
  expect(evidenceRequests).toEqual([]);
  page.off("request", observeEvidence);
  await login("local-admin");
  await expect(button("Manage local artwork")).toBeVisible();
  const unchangedArtwork = await page.evaluate(async (url) => {
    const response = await fetch(url, {
      headers: { "X-Catalog-Request": "1" },
    });
    if (!response.ok) throw new Error("Owned artwork read failed");
    return { etag: response.headers.get("ETag"), value: await response.json() };
  }, readerHead.url());
  expect(unchangedArtwork.value).toEqual(resetOverride);
  expect(unchangedArtwork.etag).toBe(resetReply.headers()["etag"]);
  await page.goto(sourceURL);
  await link("Source observations").click();
  await expect(page.locator('h2 a[href*="/observations/"]')).toHaveCount(2);
  await field("Show entries").selectOption("all");
  await button("Apply observation filters").click();
  await expect(page.locator('h2 a[href*="/observations/"]')).toHaveCount(2);
  await expect(
    page.getByText(/Showing observations from Source/),
  ).toContainText(sourceID);
  await button("All sources").click();
  await expect(field("Show entries")).toHaveValue("all");
  expect(new URL(page.url()).searchParams.has("source")).toBe(false);
  await expect(page.locator('h2 a[href*="/observations/"]')).toHaveCount(2);
  await field("Show entries").selectOption("current");
  await button("Apply observation filters").click();
  await link("Compare current contributions and conflicts").first().click();
  await expect(
    page.getByText("Sources supplied matching entry contents", { exact: true }),
  ).toBeVisible();
  await expect(link(sourceID)).toBeVisible();
  await expect(page.getByText(/Selected version:/)).toBeVisible();
  const adoptionEvidenceReads = [];
  const observeAdoptionEvidence = (request) => {
    const path = new URL(request.url()).pathname;
    if (
      path.startsWith(topologyAPIPath + "/") &&
      /\/observations\/[0-9a-f-]{36}\/raw$/.test(path)
    )
      adoptionEvidenceReads.push(path);
  };
  page.on("request", observeAdoptionEvidence);
  const occurrenceReply = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      /\/api\/v1\/observations\/[0-9a-f-]{36}\/occurrences$/.test(
        new URL(response.url()).pathname,
      ),
  );
  await page.locator('a[href*="/observations/"]').first().click();
  const observationURL = page.url();
  const occurrenceResponse = await occurrenceReply;
  expect(occurrenceResponse.status()).toBe(200);
  const occurrenceInventory = await occurrenceResponse.json();
  expect(occurrenceInventory.items.length).toBeGreaterThan(0);
  const adoptionOccurrence = occurrenceInventory.items[0];
  const observationID = new URL(observationURL).pathname.split("/").at(-1);
  expect(adoptionOccurrence.observationId).toBe(observationID);
  expect(adoptionOccurrence.evidenceComplete).toBe(true);
  const selectedRawPath = `${topologyAPIPath}/observations/${observationID}/raw`;
  const adoptionRawPanel = page.locator("section.workspace-panel").filter({
    has: page.getByRole("heading", { name: "Exact raw entry", exact: true }),
  });
  expect(adoptionEvidenceReads).toEqual([]);
  await expect(adoptionRawPanel).toHaveCount(0);
  await expect(button("Read exact raw entry")).toHaveAttribute(
    "aria-expanded",
    "false",
  );
  const occurrences = page.locator("section.workspace-panel").filter({
    has: page.getByRole("heading", {
      name: "Saved occurrences",
      exact: true,
    }),
  });
  await occurrences
    .getByText("Exact occurrence references", { exact: true })
    .first()
    .click();
  if (captureDocumentation)
    await captureDocumentation("source-retained-occurrence", occurrences);
  const rawEntryReply = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      new URL(response.url()).pathname === selectedRawPath,
  );
  await button("Read exact raw entry").click();
  expect((await rawEntryReply).status()).toBe(200);
  await expect(adoptionRawPanel.locator("pre")).toContainText("Functional");
  expect(adoptionEvidenceReads).toEqual([selectedRawPath]);
  const adoptionPath = new URL(
    `${origin}/_catalog/api/v1/observations/${observationID}:adopt`,
  ).pathname;
  const adoptionWrites = [];
  const adoptionMetadataRequests = [];
  const adoptionDocumentation = "https://adoption-metadata.invalid/guide";
  const observeAdoption = (request) => {
    const url = new URL(request.url());
    if (
      url.pathname.includes("/_catalog/api/v1/") &&
      !["GET", "HEAD", "OPTIONS"].includes(request.method())
    )
      adoptionWrites.push({ method: request.method(), path: url.pathname });
    if (url.hostname === new URL(adoptionDocumentation).hostname)
      adoptionMetadataRequests.push(url.href);
  };
  page.on("request", observeAdoption);
  await button("Use this occurrence for adoption").first().click();
  await chooseNamedOption(
    field("Destination namespace"),
    /^Catalog demo \/ .+ \/ [0-9a-f-]{36}$/i,
  );
  await chooseNamedOption(
    field("Local publisher"),
    /^Demo Publisher \/ [0-9a-f-]{36}$/i,
  );
  const adoptionNamespaceID = await field("Destination namespace").inputValue();
  const adoptionPublisherID = await field("Local publisher").inputValue();
  await button("Edit local metadata overrides as JSON").click();
  await field("Local metadata overrides JSON").fill(
    '{"publisherId":"not-an-adoption-override"}',
  );
  await button("Adopt as local draft").click();
  await expect(page.getByRole("alert")).toContainText(
    "Use only the documented local metadata fields.",
  );
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await field("Local metadata overrides JSON").fill(
    '{"version":"1.0.0","visibility":"private"}',
  );
  await button("Use guided local metadata overrides").click();
  await field("Override local display name").check();
  await field("Local display name").fill("Guided adopted resource");
  if (captureDocumentation) {
    const adoption = page.locator("section.workspace-panel").filter({
      has: page.getByRole("heading", {
        name: "Adopt as local draft",
        exact: true,
      }),
    });
    await captureDocumentation("source-adoption-preparation", adoption);
  }
  const adoptionMetadata = page.getByRole("group", {
    name: "Local metadata overrides",
    exact: true,
  });
  const documentationOverride = adoptionMetadata.getByRole("checkbox", {
    name: "Override local documentation url",
    exact: true,
  });
  const localVisibility = adoptionMetadata.getByRole("combobox", {
    name: "Local visibility",
    exact: true,
  });
  await documentationOverride.check();
  await field("Local documentation URL").fill(
    "http://adoption-metadata.invalid/guide",
  );
  await button("Adopt as local draft").click();
  await expect(page.getByRole("alert")).toContainText(
    "Use a credential-free HTTPS link.",
  );
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(adoptionWrites).toEqual([]);
  await field("Local documentation URL").fill(adoptionDocumentation);
  await localVisibility.selectOption("tenant");
  await localVisibility.selectOption("");
  await field("Override local license").check();
  await expect(field("Local license")).toHaveValue("");
  await field("Override local categories").check();
  await button("Add local category").click();
  await field("Local category 1").fill("temporary, literal category");
  await button("Remove local category 1").click();
  await expect(field("Local category 1")).toHaveCount(0);
  await expect(button("Add local category")).toBeFocused();
  const prepareAndCancelAdoption = async (expectedMetadata) => {
    await button("Adopt as local draft").click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible();
    const summary = dialog.getByLabel("Command summary", { exact: true });
    const value = (name) =>
      summary
        .getByText(name, { exact: true })
        .locator("xpath=following-sibling::dd[1]");
    for (const [name, expected] of [
      ["Observation ID", observationID],
      ["Source ID", adoptionOccurrence.sourceId],
      ["Source revision", String(adoptionOccurrence.sourceRevision)],
      ["Snapshot ID", adoptionOccurrence.snapshotId],
      ["Entry location", adoptionOccurrence.entryLocation],
      ["Retrieval digest", adoptionOccurrence.retrievalDigest],
      ["Namespace ID", adoptionNamespaceID],
      ["Publisher ID", adoptionPublisherID],
    ])
      await expect(value(name)).toHaveText(expected);
    expect(
      JSON.parse(await value("Local metadata overrides").textContent()),
    ).toEqual(expectedMetadata);
    expect(adoptionWrites).toEqual([]);
    expect(adoptionEvidenceReads).toEqual([selectedRawPath]);
    expect(adoptionMetadataRequests).toEqual([]);
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(dialog).not.toBeVisible();
    expect(adoptionWrites).toEqual([]);
  };
  await prepareAndCancelAdoption({
    version: "1.0.0",
    displayName: "Guided adopted resource",
    documentationUrl: adoptionDocumentation,
    license: "",
    categories: [],
  });
  await documentationOverride.uncheck();
  await field("Override local license").uncheck();
  await field("Override local categories").uncheck();
  await expect(field("Local documentation URL")).toHaveCount(0);
  await expect(field("Local license")).toHaveCount(0);
  await expect(button("Add local category")).toHaveCount(0);
  await prepareAndCancelAdoption({
    version: "1.0.0",
    displayName: "Guided adopted resource",
  });
  await expect(field("Local display name")).toHaveValue(
    "Guided adopted resource",
  );
  await localVisibility.selectOption("private");
  await documentationOverride.check();
  await field("Local documentation URL").fill(adoptionDocumentation);
  const adoptionLicense = (
    "Apache-2.0; " + "Local qualification declaration. ".repeat(12)
  ).trim();
  await field("Override local license").check();
  await field("Local license").fill(adoptionLicense);
  await field("Override local categories").check();
  await button("Add local category").click();
  await field("Local category 1").fill("operations, tools");
  const adoptionReply = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith(":adopt"),
  );
  await command("Adopt as local draft");
  const adoptionResponse = await adoptionReply;
  expect(adoptionResponse.status()).toBe(201);
  expect(new URL(adoptionResponse.url()).pathname).toBe(adoptionPath);
  expect(adoptionResponse.request().headers()["idempotency-key"]).toMatch(
    /^[0-9a-f-]{36}$/i,
  );
  expect(adoptionResponse.request().postDataJSON()).toEqual({
    snapshotId: adoptionOccurrence.snapshotId,
    retrievalDigest: adoptionOccurrence.retrievalDigest,
    entryLocation: adoptionOccurrence.entryLocation,
    namespaceId: adoptionNamespaceID,
    publisherId: adoptionPublisherID,
    version: "1.0.0",
    displayName: "Guided adopted resource",
    documentationUrl: adoptionDocumentation,
    visibility: "private",
    license: adoptionLicense,
    categories: ["operations, tools"],
  });
  expect(adoptionWrites).toEqual([{ method: "POST", path: adoptionPath }]);
  expect(adoptionEvidenceReads).toEqual([selectedRawPath]);
  expect(adoptionMetadataRequests).toEqual([]);
  const adopted = await adoptionResponse.json();
  expect(adopted.listingRevision.displayName).toBe("Guided adopted resource");
  expect(adopted.listingRevision.categories).toEqual(["operations, tools"]);
  expect(adopted.listingRevision.visibility).toBe("private");
  expect(adopted.listingRevision.license).toBe(adoptionLicense);
  expect(adopted.listingRevision.documentationUrl).toBe(adoptionDocumentation);
  await expect(page.getByRole("status")).toContainText("Local draft created.");
  await link("Open adopted resource").click();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: /urn:air:functional.example/ }),
  ).toBeVisible();
  const persistedRevision = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      response
        .url()
        .endsWith(`/api/v1/listing-revisions/${adopted.listingRevision.id}`),
  );
  await visit(`/listing-revisions/${adopted.listingRevision.id}`);
  const persisted = await persistedRevision;
  expect(persisted.status()).toBe(200);
  const readback = await persisted.json();
  expect(readback.displayName).toBe("Guided adopted resource");
  expect(readback.categories).toEqual(["operations, tools"]);
  expect(readback.visibility).toBe("private");
  expect(readback.license).toBe(adoptionLicense);
  expect(readback.documentationUrl).toBe(adoptionDocumentation);
  expect(adoptionWrites).toEqual([{ method: "POST", path: adoptionPath }]);
  expect(adoptionEvidenceReads).toEqual([selectedRawPath]);
  expect(adoptionMetadataRequests).toEqual([]);
  page.off("request", observeAdoption);
  page.off("request", observeAdoptionEvidence);
  await expect(link("Back to listing")).toBeVisible();
  await page.goto(observationURL);
  await expect(button("Read exact raw entry")).toBeVisible();

  // Qualify readable diagnostics against the real owned peer and API before its outage control.
  await visit("/federation-search");
  await field("Query").fill("Functional");
  await button("Choose configured sources").click();
  await page.getByRole("checkbox", { name: new RegExp(sourceID) }).check();
  await expect(field("Source IDs (optional)")).toHaveValue(sourceID);
  await field("Reason").fill("Explicit owned peer diagnostic");
  const liveReply = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/api/v1/federation/sources:live-search"),
  );
  await command("Query live sources");
  const diagnostic = await (await liveReply).json();
  const liveRows = page.getByRole("region", {
    name: "Live diagnostic results",
  });
  await expect(
    liveRows.getByRole("heading", {
      name: "urn:air:aaa.functional.example:default:live",
      exact: true,
    }),
  ).toBeVisible();
  await expect(liveRows).toContainText("No version supplied");
  await expect(liveRows).toContainText("application/a2a-agent-card+json");
  await expect(liveRows).toContainText("This bounded source query succeeded.");
  await expect(
    liveRows.getByRole("link", { name: /record|adopt/i }),
  ).toHaveCount(0);

  phase("compose-ans-live-selection");
  const liveRegistration = diagnostic.results.find(
    (item) => item.identifier === "urn:air:aaa.functional.example:default:live",
  );
  expect(liveRegistration).toBeTruthy();
  await liveRows
    .locator("article")
    .filter({
      has: page.getByRole("heading", {
        name: liveRegistration.identifier,
        exact: true,
      }),
    })
    .getByRole("button", { name: "Select for ANS assessment", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Request ANS assessment", exact: true }),
  ).toBeVisible();
  await unavailableAssessment(
    {
      sourceRevision: liveRegistration.sourceRevision,
      identifier: liveRegistration.identifier,
      versionPresent: liveRegistration.versionPresent,
      ...(liveRegistration.versionPresent
        ? { version: liveRegistration.version }
        : {}),
      mediaType: liveRegistration.entry.type,
      delivery: "url",
      exactEntryDigest: liveRegistration.exactEntryDigest,
      text: "Functional",
    },
    "Check the live registration",
  );

  phase("compose-source-failure-retry");
  await fixtureRequest("/control", { mode: "denied" });
  const failedURL = await sync();
  await waitJob("failed");
  await expect(page.getByText("authentication", { exact: true })).toBeVisible();
  await page.goto(indexedURL);
  await expect(
    page.getByRole("heading", { name: /Functional (One|Two)/ }),
  ).toBeVisible();
  await page.goto(sourceURL);
  await link("Source observations").click();
  await expect(page.locator('h2 a[href*="/observations/"]')).toHaveCount(2);
  await fixtureRequest("/control", { mode: "initial" });
  await page.goto(failedURL);
  await field("Reason").fill("Upstream access restored");
  await command("Retry job");
  await link("Open retry job").click();
  expect(page.url()).not.toBe(failedURL);
  await expect(
    page.locator(`a[href="${new URL(failedURL).pathname}"]`),
  ).toBeVisible();
  await waitJob("succeeded");

  phase("compose-source-partial");
  await fixtureRequest("/control", { mode: "partial" });
  const partialCLI = await remoteSearch({ text: "Functional" });
  expect(partialCLI.partial).toBe(true);
  expect(partialCLI.liveCount).toBe(0);
  expect(
    partialCLI.items.filter((item) => item.type === "indexed"),
  ).toHaveLength(2);
  expect(partialCLI.diagnostics.length).toBeGreaterThan(0);
  await visit("/catalog");
  await page
    .getByRole("searchbox", { name: "Search Catalog", exact: true })
    .fill("Functional");
  await button("Search").click();
  await expect(
    page.getByText(/Some source queries or results are incomplete/),
  ).toBeVisible();
  await expect(page.locator("article.resource-card")).toHaveCount(2);
  const repeatedSearch = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/catalog/search") &&
      response.request().method() === "POST",
  );
  await button("Retry search").click();
  const retried = await repeatedSearch;
  expect(retried.status()).toBe(200);
  expect(retried.request().postDataJSON()).toMatchObject({
    text: "Functional",
  });
  expect(retried.request().postDataJSON()).not.toHaveProperty("cursor");
  await expect(
    page.getByText(/Some source queries or results are incomplete/),
  ).toBeVisible();
  await expect(
    page
      .locator("article.resource-card")
      .filter({ hasText: "Indexed external" }),
  ).toHaveCount(2);
  await expect(
    page.locator("article.resource-card").filter({ hasText: "Live external" }),
  ).toHaveCount(0);
  await visit("/federation-search");
  await field("Query").fill("functional");
  await button("Search indexed observations").click();
  const indexedRows = page.getByRole("region", {
    name: "Indexed diagnostic results",
  });
  await expect(indexedRows.locator("article")).toHaveCount(2);
  await expect(
    indexedRows.getByRole("link", { name: sourceID, exact: true }),
  ).toHaveCount(2);
  await expect(
    indexedRows
      .locator("dt")
      .filter({ hasText: /^Freshness$/ })
      .locator("+ dd"),
  ).toHaveText(["current", "current"]);
  await expect(
    indexedRows
      .locator("dt")
      .filter({ hasText: /^Native support$/ })
      .locator("+ dd"),
  ).toHaveText(["Supported", "Supported"]);
  await page.getByText("Indexed results", { exact: true }).click();
  expect(
    JSON.parse(await page.locator("pre").filter({ visible: true }).innerText())
      .items,
  ).toHaveLength(2);
  await page.getByRole("checkbox", { name: "Agents", exact: true }).check();
  await button("Add observation tag").click();
  await field("Observation tag 1").fill("no-matching-owned-tag");
  await button("Search indexed observations").click();
  await page.getByText("Indexed results", { exact: true }).click();
  await expect
    .poll(
      async () =>
        JSON.parse(
          await page.locator("pre").filter({ visible: true }).innerText(),
        ).items ?? [],
    )
    .toEqual([]);
  await expect(indexedRows).toContainText("No matching indexed observations.");
  await button("Remove observation tag 1").click();
  await button("Search indexed observations").click();
  await field("Source IDs (optional)").fill(sourceID);
  await field("Advanced live options (JSON)").fill('{"unexpected":true}');
  await field("Reason").fill("Explicit test query disclosure");
  await button("Query live sources").click();
  await expect(page.getByRole("alert")).toContainText(
    "Live options accept filter, @context and pageSize only.",
  );
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await field("Advanced live options (JSON)").fill('{"pageSize":2}');
  await command("Query live sources");
  await expect(
    page.getByText(
      "Some source queries or results are incomplete. Available results are shown.",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(
    page.getByRole("region", { name: "Live diagnostic results" }),
  ).toContainText("No live results returned.");
  await expect(
    page.getByRole("region", { name: "Live diagnostic results" }),
  ).toContainText("The source query failed");
  const partialJobURL = await sync();
  await waitJob("waiting");
  await expect(button("Retry job")).toHaveCount(0);
  await page.goto(indexedURL);
  await expect(
    page.getByRole("heading", { name: /Functional (One|Two)/ }),
  ).toBeVisible();
  await page.goto(sourceURL);
  await link("Source observations").click();
  await expect(page.locator('h2 a[href*="/observations/"]')).toHaveCount(2);
  await fixtureRequest("/control", { mode: "initial" });
  await page.goto(partialJobURL);
  await waitJob("succeeded");

  phase("compose-job-cancellation");
  // Stop only the invocation-owned worker to leave this new Job queued.
  // Cancellation and retry intentionally use different Jobs.
  await worker(false);
  await sync();
  await field("Reason").fill("Cancel queued browser work");
  const queuedURL = page.url();
  await visit("/jobs");
  await expect(
    page.locator(`a[href="${new URL(queuedURL).pathname}"]`),
  ).toBeVisible();
  await field("Job type").selectOption("source.sync");
  await field("Job state").selectOption("queued");
  const filtered = page.waitForResponse((r) => {
    const url = new URL(r.url());
    return (
      url.pathname.endsWith("/api/v1/jobs") &&
      url.searchParams.get("kind") === "source.sync" &&
      url.searchParams.get("state") === "queued"
    );
  });
  await button("Apply Job filters").click();
  const filteredResponse = await filtered;
  expect(filteredResponse.status()).toBe(200);
  const filteredJobs = await filteredResponse.json();
  expect(
    filteredJobs.items.some(
      (job) => job.id === new URL(queuedURL).pathname.split("/").at(-1),
    ),
  ).toBe(true);
  expect(
    filteredJobs.items.every(
      (job) => job.kind === "source.sync" && job.state === "queued",
    ),
  ).toBe(true);
  await field("Exact Job ID").fill(
    new URL(queuedURL).pathname.split("/").at(-1),
  );
  await button("Open Job").click();
  await expect(page).toHaveURL(
    (url) =>
      url.origin === new URL(queuedURL).origin &&
      url.pathname === new URL(queuedURL).pathname &&
      !url.search,
  );
  await field("Reason").fill("Cancel queued browser work");
  await button("Cancel job").click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Cancel", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(
    page.locator('.workspace-status[data-state="queued"]'),
  ).toBeVisible();
  await command("Cancel job");
  await expect(
    page.getByText(
      /This view has not been reread since the command acknowledgement/,
    ),
  ).toBeVisible();
  await button("Reload job").click();
  await waitJob("cancelled");
  await expect(button("Retry job")).toHaveCount(0);
  await worker(true);

  phase("compose-alternate-source-artwork");
  await alternateSourceArtworkJourney({
    page,
    origin,
    sourceId: sourceID,
    indexedURL,
    currentHead: resetOverride,
    currentETag: resetReply.headers().etag,
    waitJob,
  });

  phase("compose-source-change");
  for (const action of ["pause", "resume"]) {
    await page.goto(sourceURL);
    await field("Source action").selectOption(action);
    await field("Reason").fill("Browser source lifecycle");
    await command(action === "pause" ? "Pause source" : "Resume source");
    await expect(
      page.getByText(
        /This view has not been reread since the command acknowledgement/,
      ),
    ).toBeVisible();
    await button("Reload source").click();
    await expect(
      page
        .locator("dt")
        .filter({ hasText: /^Configured state$/ })
        .locator("+ dd"),
    ).toHaveText(action === "pause" ? "Paused" : "Active");
    await expect(
      page
        .locator("dt")
        .filter({ hasText: /^Search mode$/ })
        .locator("+ dd"),
    ).toHaveText("Indexed copy and live requests");
  }
  // Reinspect the same owned endpoint with its real configured network policy.
  // This exercises binding replacement without inventing a credential provider.
  await button("Preview configuration change or removal").click();
  await openDisclosure(page, "Replace network or credential references");
  await link("Reinspect existing interface").click();
  await field("Source URL").fill(
    "https://federation-fixture:8443/.well-known/ard.json",
  );
  await field("Network policy reference").fill("functional-fixture");
  await command("Inspect source");
  await link("Review inspection and connect").click();
  const inspectionID = new URL(page.url()).pathname.split("/").pop();
  const updateSettings = async () => {
    if (await field("Excluded identifier 1").count()) {
      await expect(field("Excluded identifier 1")).toHaveValue(
        "legacy:browser-hidden",
      );
      await button("Clear excluded identifiers").click();
      await expect(button("Add excluded identifier")).toBeFocused();
    }
    await field("Display name").fill("Updated browser upstream");
    await field("Synchronization").selectOption("automatic");
    await field("ANS verification policy").fill("");
    await openDisclosure(page, "Replace network or credential references");
    await field("Replacement inspection ID").fill(inspectionID);
    await field("New network profile reference").fill("functional-fixture");
    await field("New credential binding reference").fill("");
  };
  for (const intent of ["update", "remove"]) {
    await page.goto(sourceURL);
    await button("Preview configuration change or removal").click();
    await field("Change type").selectOption(intent);
    if (intent === "update") {
      await expect(field("ANS verification policy")).toHaveValue(
        "operator-ans",
      );
      await updateSettings();
    }
    await command("Preview source change");
    if (intent === "update") {
      page.once("dialog", (dialog) => dialog.accept());
      await button("Revise configuration and discard preview").click();
      await expect(button("Apply source change")).toHaveCount(0);
      await updateSettings();
      await command("Preview source change");
    }
    await page.getByText("Exact change impact", { exact: true }).click();
    await expect(page.locator("pre").filter({ visible: true })).toContainText(
      '"digest"',
    );
    const preview = JSON.parse(
      await page.locator("pre").filter({ visible: true }).innerText(),
    );
    for (const [label, value] of [
      ["Current saved entries", preview.currentMatched],
      ["Entries included after this change", preview.proposedMatched],
      ["Entries excluded after this change", preview.excludedObservations],
      ["Queries sent upstream", preview.sendsQueriesUpstream ? "Yes" : "No"],
    ])
      await expect(
        page
          .locator("dt")
          .filter({ hasText: new RegExp(`^${label}$`) })
          .locator("+ dd"),
      ).toHaveText(String(value));
    const applied = page.waitForResponse(
      (response) =>
        response.request().method() ===
          (intent === "update" ? "PATCH" : "POST") &&
        response
          .url()
          .endsWith(
            `/sources/${sourceID}${intent === "update" ? "" : ":remove"}`,
          ),
    );
    await field("Reason").fill("Apply inspected browser change");
    await command(
      intent === "update" ? "Apply source change" : "Remove source",
    );
    const change = await applied;
    expect(change.ok()).toBe(true);
    expect(change.request().postDataJSON().changePreviewDigest).toBe(
      preview.digest,
    );
    if (intent === "update")
      expect(change.request().postDataJSON()).toMatchObject({
        displayName: "Updated browser upstream",
        updateMode: "automatic",
        inspectionId: inspectionID,
        networkProfile: "functional-fixture",
        credentialBinding: "",
        trustVerificationPolicy: "",
      });
    if (intent === "remove") {
      await expect(
        page.getByRole("status").filter({ hasText: "Source removed." }),
      ).toHaveCount(1);
      await expect(link("Reload source")).toHaveCount(0);
      await link("Back to sources").last().click();
      await expect(
        page.getByText("No sources on this page.", { exact: true }),
      ).toBeVisible();
      continue;
    }
    await page.reload();
    await expect(
      page
        .locator("dt")
        .filter({ hasText: /^ANS verification policy$/ })
        .locator("+ dd"),
    ).toHaveText("No policy selected");
    await expect(
      page.getByRole("heading", {
        name: "Updated browser upstream",
        exact: true,
      }),
    ).toBeVisible();
  }

  phase("compose-ans-private-lookup");
  // Historical lookup must work without loading the now-removed Source.
  // This fixture has no completed verifier Job, so the real dedicated read is a 404.
  const absentAssessmentJob = "00000000-0000-7000-8000-000000000099";
  const privatePath = `/api/v1/federation/sources/${sourceID}/trust-assessments/${absentAssessmentJob}`;
  await visit(`/trust-assessments/${absentAssessmentJob}?sourceId=${sourceID}`);
  await expect(field("Source ID")).toHaveValue(sourceID);
  await expect(field("Assessment Job ID")).toHaveValue(absentAssessmentJob);
  network.expectFailure("GET", "/_catalog" + privatePath, 404);
  const privateRead = page.waitForResponse((response) =>
    response.url().endsWith(privatePath),
  );
  await button("Read private assessment").click();
  expect((await privateRead).status()).toBe(404);
  await expect(
    page.getByText(/No retained assessment is available/),
  ).toBeVisible();
  await expect(link("Save original entry")).toHaveCount(0);

  phase("compose-webhook-delivery");
  await visit("/webhooks/new");
  await field("Webhook name").fill("Owned browser receiver");
  await field("Receiver URL").fill(
    "https://federation-fixture:8443/webhooks/events",
  );
  await field("Resource created").check();
  const created = page.waitForResponse(
    (r) =>
      r.request().method() === "POST" && r.url().endsWith("/api/v1/webhooks"),
  );
  await command("Create webhook");
  let registration = await (await created).json();
  const endpoint = registration.endpoint.id;
  const receiver = async (status) =>
    fixtureRequest("/webhooks/control", {
      endpointId: endpoint,
      secret: registration.secret,
      version: registration.endpoint.secretVersion,
      status,
    });
  await receiver(204);
  await button("Clear secret from this page").click();
  await link("Open webhook").click();
  const webhookURL = page.url();
  const send = async (state) => {
    const reply = page.waitForResponse(
      (r) => r.request().method() === "POST" && r.url().endsWith(":test"),
    );
    await command("Manage webhook");
    const delivery = await (await reply).json();
    expect(delivery.state).toBe(state);
    return delivery;
  };
  const first = await send("succeeded");
  expect((await fixtureRequest("/webhooks/state")).ids).toContain(
    first.eventId,
  );
  await page.reload();
  await field("Webhook action").selectOption("rotate");
  await expect(field("Rotation mode")).toHaveValue("routine");
  // Resolve the exact current principal before the fault-prone write so
  // database diagnostics remain independent of subsequent API availability.
  const selfRead = await page.evaluate(async (url) => {
    const reply = await fetch(url, { headers: { "X-Catalog-Request": "1" } });
    if (!reply.ok) throw new Error("Owned principal unavailable");
    const value = await reply.json();
    return { realm: value.realmId, membership: value.membershipId };
  }, origin + "/_catalog/api/v1/me");
  const rotated = page.waitForResponse(
    (r) =>
      r.request().method() === "POST" && r.url().endsWith(":rotate-secret"),
  );
  await button("Manage webhook").click();
  const rotationStarted = Date.now();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  const rotationResponse = await rotated;
  await rotationEvidence(
    rotationResponse,
    endpoint,
    Date.now() - rotationStarted,
    selfRead,
  );
  await expect(page.getByRole("dialog")).toHaveCount(0);
  const previousVersion = registration.endpoint.secretVersion;
  registration = await (await rotated).json();
  expect(registration.endpoint.secretVersion).toBe(previousVersion + 1);
  expect(typeof registration.previousSecretExpiresAt).toBe("string");
  await receiver(503);
  await button("Clear secret from this page").click();
  await page.reload();
  await send("dead_letter");
  const testReceipt = page.getByRole("region", {
    name: "Test delivery result",
  });
  await expect(
    testReceipt
      .locator("dt")
      .filter({ hasText: /^Receiver HTTP outcome$/ })
      .locator("+ dd"),
  ).toHaveText("503");
  await expect(page.getByRole("status")).toContainText("not delivered");
  await receiver(204);
  await page.reload();
  await send("succeeded");

  const syntheticCounters = await page.evaluate(async (url) => {
    const response = await fetch(url, {
      headers: { "X-Catalog-Request": "1" },
    });
    if (!response.ok)
      throw new Error("Synthetic notification counters unavailable");
    return response.json();
  }, origin + "/_catalog/api/v1/webhooks/reconciliation");
  expect(syntheticCounters.deliveryLossCount).toBe(0);

  // One terminal production failure qualifies the summary and its exact evidence link.
  await receiver(400);
  await visit("/resources/new");
  await chooseNamedOption(
    field("Namespace"),
    /^Catalog demo \/ .+ \/ [0-9a-f-]{36}$/i,
  );
  await field("Canonical resource identifier").fill(
    "urn:air:catalog.example.com:demo:overview-failure",
  );
  await command("Create resource");
  await link("Open created resource").click();
  await page.goto(webhookURL);
  await expect
    .poll(
      async () => {
        await button("Refresh deliveries from first page").click();
        return page
          .getByText("Resource created: Automatic retries stopped", {
            exact: true,
          })
          .count();
      },
      { timeout: 30000 },
    )
    .toBe(1);
  const failedOverviewRead = page.waitForResponse((response) =>
    new URL(response.url()).pathname.endsWith(
      "/api/v1/overview/webhook-deliveries",
    ),
  );
  await visit("/overview?view=operations");
  const failedOverview = await (await failedOverviewRead).json();
  expect(failedOverview.total.value).toBe(1);
  expect(failedOverview.failedEndpoints).toHaveLength(1);
  const failedEndpoint = failedOverview.failedEndpoints[0];
  expect(failedEndpoint.endpointId).toBe(endpoint);
  expect(failedEndpoint.state).toBe("dead_letter");
  await link("View delivery").click();
  await expect(field("Exact delivery ID")).toHaveValue(
    failedEndpoint.deliveryId,
  );
  await expect(field("Delivery state")).toHaveValue("dead_letter");
  await expect(page.locator("article")).toHaveCount(1);
  await receiver(204);

  // The synchronous test above is not the outbox worker. Create a subscribed
  // resource through the UI and match the worker's retained event ID instead.
  await visit("/resources/new");
  await chooseNamedOption(
    field("Namespace"),
    /^Catalog demo \/ .+ \/ [0-9a-f-]{36}$/i,
  );
  await field("Canonical resource identifier").fill(
    "urn:air:catalog.example.com:demo:browser-notification",
  );
  await command("Create resource");
  await link("Open created resource").click();
  await page.goto(webhookURL);
  const title = "Resource created: Succeeded";
  await expect
    .poll(
      async () => {
        await button("Refresh deliveries from first page").click();
        return page.getByText(title, { exact: true }).count();
      },
      { timeout: 30000 },
    )
    .toBe(1);
  const deliveryRow = page
    .locator("article")
    .filter({ has: page.getByRole("heading", { name: title, exact: true }) });
  for (const [label, value] of [
    ["HTTP outcome", "204"],
    ["Attempt", "1"],
    ["Event type code", "com.open.agentic.catalog.resource.created.v1"],
  ])
    await expect(
      deliveryRow
        .locator("dt")
        .filter({ hasText: new RegExp(`^${label}$`) })
        .locator("+ dd"),
    ).toHaveText(value);
  await expect(
    deliveryRow.getByRole("button", { name: "Copy delivery ID", exact: true }),
  ).toBeVisible();
  await expect(
    deliveryRow.getByRole("button", { name: "Copy event ID", exact: true }),
  ).toBeVisible();
  await deliveryRow
    .getByText("Complete delivery evidence", { exact: true })
    .click();
  const delivered = JSON.parse(
    await page.locator("pre").filter({ visible: true }).innerText(),
  );
  expect(delivered.endpointId).toBe(endpoint);
  await field("Exact delivery ID").fill(delivered.id);
  await field("Exact event ID").fill(delivered.eventId);
  await field("Delivery state").selectOption("succeeded");
  await button("Apply delivery filters").click();
  await expect(page.locator("article")).toHaveCount(1);
  await expect(page.locator("article")).toContainText(delivered.eventId);
  await button("Clear delivery filters").click();
  const receiverState = await fixtureRequest("/webhooks/state");
  expect(receiverState.ids).toContain(delivered.eventId);
  expect(receiverState.invalid).toBe(0);
  const recoveredOverviewRead = page.waitForResponse((response) =>
    new URL(response.url()).pathname.endsWith(
      "/api/v1/overview/webhook-deliveries",
    ),
  );
  await visit("/overview?view=operations");
  const recoveredOverview = await (await recoveredOverviewRead).json();
  expect(recoveredOverview.total.value).toBe(2);
  expect(
    recoveredOverview.states.find((row) => row.state === "dead_letter").count,
  ).toBe(1);
  expect(
    recoveredOverview.states.find((row) => row.state === "succeeded").count,
  ).toBe(1);
  expect(recoveredOverview.failedEndpoints).toEqual([]);
  await visit("/webhooks");
  await button("Refresh counters").click();
  await page.getByText("Reconciliation counters", { exact: true }).click();
  const counters = JSON.parse(
    await page.locator("pre").filter({ visible: true }).last().innerText(),
  );
  expect(counters.pendingEventLimit).toBe(1000);
  expect(counters.pendingEvents).toBeGreaterThanOrEqual(0);
  expect(counters.pendingEvents).toBeLessThanOrEqual(
    counters.pendingEventLimit,
  );
  expect(counters.capacityLossCount).toBe(0);
  for (const [label, value] of [
    ["Events awaiting delivery", counters.pendingEvents],
    ["Notifications suppressed by capacity limits", counters.capacityLossCount],
    ["Final delivery failures", counters.deliveryLossCount],
  ])
    await expect(
      page
        .locator("dt")
        .filter({ hasText: new RegExp(`^${label}$`) })
        .locator("+ dd"),
    ).toHaveText(String(value));
  // The one terminal production failure remains counted after a newer success.
  expect(counters.deliveryLossCount).toBe(1);
  await expect(field("Signing secret")).toHaveCount(0);
  expect(await page.evaluate(() => Object.keys(sessionStorage))).toEqual([]);
  phase("compose-independent-publication");
  await publicationJourney({
    page,
    visit,
    login,
    button,
    field,
    link,
    command,
  });
  console.log(
    "Packaged federation, Job, webhook and publication journeys passed",
  );
}

// A real overlapping Registry contribution gets its own local artwork history.
// Setup uses supported API commands; only the alternate picker/save is the new
// browser workflow. The original source and every legacy count stay unchanged.
async function alternateSourceArtworkJourney({
  page,
  origin,
  sourceId,
  indexedURL,
  currentHead,
  currentETag,
  waitJob,
}) {
  const button = (name) => page.getByRole("button", { name, exact: true });
  const request = (method, path, body, etag) =>
    page.evaluate(
      async ({ url, method, body, etag, key }) => {
        const response = await fetch(url, {
          method,
          credentials: "same-origin",
          cache: "no-store",
          signal: AbortSignal.timeout(10000),
          headers: {
            "X-Catalog-Request": "1",
            ...(body === undefined
              ? {}
              : { "Content-Type": "application/json" }),
            ...(key ? { "Idempotency-Key": key } : {}),
            ...(etag ? { "If-Match": etag } : {}),
          },
          ...(body === undefined ? {} : { body: JSON.stringify(body) }),
        });
        const content = await response.text();
        return {
          status: response.status,
          etag: response.headers.get("ETag"),
          data: content ? JSON.parse(content) : undefined,
        };
      },
      {
        url: origin + "/_catalog" + path,
        method,
        body,
        etag,
        key: method === "GET" ? undefined : randomUUID(),
      },
    );
  const { sourceId: _currentSource, ...entry } = currentHead.target;
  expect(_currentSource).toBe(sourceId);
  const currentPath = `/api/v1/federation/sources/${sourceId}/artwork`;
  const query = new URLSearchParams(
    Object.entries(entry).map(([key, value]) => [key, String(value)]),
  );
  const currentBefore = await request("GET", currentPath + "?" + query);
  expect(currentBefore).toEqual({
    status: 200,
    etag: currentETag,
    data: currentHead,
  });
  const currentHistory = await request(
    "GET",
    currentPath + "/history?" + query + "&limit=25",
  );
  expect(currentHistory.status).toBe(200);
  expect(currentHistory.data.nextCursor).toBeUndefined();
  expect(currentHistory.data.items.map((item) => item.revision)).toEqual([
    1, 2,
  ]);
  const selection = new URLSearchParams(new URL(indexedURL).search);
  const indexedPath = "/api/v1/catalog/indexed-record?" + selection;
  const original = await request("GET", indexedPath);
  expect(original.status).toBe(200);
  expect(original.data.indexed.presentationSourceId).toBe(sourceId);
  expect(original.data.indexed.sourceIds).toEqual([sourceId]);
  const manifestURL =
    "https://federation-fixture:8443/artwork-alternate/.well-known/ard.json";
  const inspection = await request(
    "POST",
    "/api/v1/federation/source-inspections",
    {
      url: manifestURL,
      networkProfile: "functional-fixture",
    },
  );
  expect(inspection.status).toBe(201);
  expect(inspection.data).toMatchObject({
    requestedUrl: manifestURL,
    resolvedUrl: manifestURL,
    registryListUrl:
      "https://federation-fixture:8443/artwork-alternate/ard/v1/agents",
  });
  const connected = await request("POST", "/api/v1/federation/sources", {
    inspectionId: inspection.data.id,
    displayName: "Owned alternate artwork contribution",
    scope: { kinds: ["agent"], identifiers: [entry.identifier] },
    retrievalMode: "indexed",
    updateMode: "manual_review",
    networkProfile: "functional-fixture",
    reason: "Qualify independent artwork for one overlapping contribution",
  });
  expect(connected.status).toBe(201);
  const alternateId = connected.data.id;
  expect(alternateId).toMatch(/^[0-9a-f-]{36}$/);
  expect(alternateId).not.toBe(sourceId);
  expect(connected.data.endpoint).toBe(manifestURL);
  expect(connected.data.registryListUrl).toBe(inspection.data.registryListUrl);
  await page.goto(origin + `/sources/${alternateId}`);
  await page
    .getByRole("combobox", { name: "Source action", exact: true })
    .selectOption("sync");
  await button("Synchronize source").click();
  const syncPath = `/api/v1/federation/sources/${alternateId}:sync`;
  const [syncReply] = await Promise.all([
    page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url() === origin + "/_catalog" + syncPath,
    ),
    page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click(),
  ]);
  expect(syncReply.status()).toBe(202);
  const accepted = await syncReply.json();
  expect(accepted.job.kind).toBe("source.sync");
  expect(accepted.existing).toBe(false);
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page
    .getByRole("link", { name: "Inspect synchronization Job", exact: true })
    .click();
  await waitJob("succeeded");
  const completed = await request("GET", `/api/v1/jobs/${accepted.job.id}`);
  expect(completed.status).toBe(200);
  expect(completed.data.state).toBe("succeeded");
  const alternate = await request(
    "GET",
    `/api/v1/federation/sources/${alternateId}`,
  );
  expect(alternate.status).toBe(200);
  expect(alternate.data.presentationGeneration).toBe(1);
  const snapshot = await request(
    "GET",
    `/api/v1/federation/snapshots/${alternate.data.lastCompleteSnapshotId}`,
  );
  expect(snapshot.status).toBe(200);
  expect(snapshot.data).toMatchObject({
    sourceId: alternateId,
    state: "complete",
    enumerationComplete: true,
  });
  const target = {
    ...entry,
    sourceId: alternateId,
    presentationGeneration: alternate.data.presentationGeneration,
  };
  const alternateQuery = new URLSearchParams(
    Object.entries(target)
      .filter(([key]) => key !== "sourceId")
      .map(([key, value]) => [key, String(value)]),
  );
  const alternatePath = `/api/v1/federation/sources/${alternateId}/artwork`;
  const initialHead = await request("POST", alternatePath, {
    target,
    icon: { mode: "inherit" },
    screenshots: { mode: "inherit" },
    reason: "Retain an initial head for the alternate contribution",
  });
  expect(initialHead.status).toBe(200);
  expect(initialHead.data).toMatchObject({ target, revision: 1 });
  expect(initialHead.etag).toBe('"rev-1"');
  const withBoth = await request("GET", indexedPath);
  expect(withBoth.status).toBe(200);
  expect(withBoth.data.indexed.sourceIds).toEqual(
    [sourceId, alternateId].sort(),
  );
  expect(withBoth.data.indexed.presentationSourceId).toBe(sourceId);
  expect(withBoth.data.artwork).toEqual(original.data.artwork);
  const writes = [];
  const reads = [];
  const outside = [];
  const observe = (request) => {
    const url = new URL(request.url());
    if (
      ["http:", "https:"].includes(url.protocol) &&
      url.origin !== new URL(origin).origin
    )
      outside.push(url.origin);
    if (
      url.pathname.includes("/federation/sources/") &&
      /\/artwork(?:\/history)?$/.test(url.pathname)
    ) {
      if (request.method() === "GET") reads.push(url.pathname);
      else
        writes.push({
          method: request.method(),
          path: url.pathname,
          body: request.postDataJSON(),
          etag: request.headers()["if-match"],
          key: request.headers()["idempotency-key"],
        });
    }
  };
  page.on("request", observe);
  try {
    await page.goto(indexedURL);
    const [currentReply] = await Promise.all([
      page.waitForResponse(
        (response) =>
          response.request().method() === "GET" &&
          new URL(response.url()).pathname ===
            new URL(origin + "/_catalog" + currentPath).pathname,
      ),
      button("Manage local artwork").click(),
    ]);
    expect(currentReply.status()).toBe(200);
    expect(await currentReply.json()).toEqual(currentHead);
    expect(currentReply.headers().etag).toBe(currentETag);
    const contributions = page.getByRole("combobox", {
      name: "Artwork contribution source",
      exact: true,
    });
    await expect(contributions).toHaveValue(sourceId);
    await expect(
      contributions.getByRole("option", {
        name: `${sourceId} (current presentation)`,
        exact: true,
      }),
    ).toHaveAttribute("value", sourceId);
    await expect(
      contributions.getByRole("option", {
        name: `${alternateId} (alternate contribution)`,
        exact: true,
      }),
    ).toHaveAttribute("value", alternateId);
    expect(reads).toEqual([
      new URL(origin + "/_catalog" + currentPath).pathname,
    ]);
    const [sourceReply, headReply] = await Promise.all([
      page.waitForResponse(
        (response) =>
          response.request().method() === "GET" &&
          response.url() ===
            origin + `/_catalog/api/v1/federation/sources/${alternateId}`,
      ),
      page.waitForResponse(
        (response) =>
          response.request().method() === "GET" &&
          new URL(response.url()).pathname ===
            new URL(origin + "/_catalog" + alternatePath).pathname,
      ),
      contributions.selectOption(alternateId),
    ]);
    expect(sourceReply.status()).toBe(200);
    expect((await sourceReply.json()).presentationGeneration).toBe(
      target.presentationGeneration,
    );
    expect(headReply.status()).toBe(200);
    expect(headReply.headers().etag).toBe(initialHead.etag);
    expect(await headReply.json()).toEqual(initialHead.data);
    expect(Object.fromEntries(new URL(headReply.url()).searchParams)).toEqual(
      Object.fromEntries(alternateQuery),
    );
    await expect(
      page.getByText(
        "Editing an alternate contribution. This does not change which source supplies the current presentation.",
        { exact: true },
      ),
    ).toBeVisible();
    await page
      .getByRole("combobox", { name: "Icon behavior", exact: true })
      .selectOption("suppress");
    await page
      .getByRole("textbox", { name: "Artwork override reason", exact: true })
      .fill("Restrict the alternate contribution only");
    await button("Save local artwork override").click();
    const facts = page
      .getByRole("dialog")
      .getByLabel("Command summary", { exact: true });
    for (const [label, value] of [
      ["Source ID", alternateId],
      ["Presentation generation", String(target.presentationGeneration)],
      ["Override revision", initialHead.etag],
    ])
      await expect(
        facts
          .locator("dt")
          .filter({ hasText: new RegExp(`^${label}$`) })
          .locator("+ dd"),
      ).toHaveText(value);
    expect(writes).toEqual([]);
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Cancel", exact: true })
      .click();
    await expect(
      page.getByRole("textbox", {
        name: "Artwork override reason",
        exact: true,
      }),
    ).toHaveValue("Restrict the alternate contribution only");
    expect(await request("GET", alternatePath + "?" + alternateQuery)).toEqual(
      initialHead,
    );
    expect(writes).toEqual([]);
    await button("Save local artwork override").click();
    const [savedReply] = await Promise.all([
      page.waitForResponse(
        (response) =>
          response.request().method() === "POST" &&
          response.url() === origin + "/_catalog" + alternatePath,
      ),
      page
        .getByRole("dialog")
        .getByRole("button", { name: "Confirm", exact: true })
        .click(),
    ]);
    expect(savedReply.status()).toBe(200);
    const saved = await savedReply.json();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(saved).toMatchObject({
      target,
      revision: 2,
      icon: { mode: "suppress" },
      screenshots: { mode: "inherit" },
      reason: "Restrict the alternate contribution only",
    });
    expect(writes).toEqual([
      {
        method: "POST",
        path: new URL(origin + "/_catalog" + alternatePath).pathname,
        body: {
          target,
          icon: { mode: "suppress" },
          screenshots: { mode: "inherit" },
          reason: "Restrict the alternate contribution only",
        },
        etag: initialHead.etag,
        key: expect.any(String),
      },
    ]);
    expect(writes[0].key).toBeTruthy();
    expect(await request("GET", alternatePath + "?" + alternateQuery)).toEqual({
      status: 200,
      etag: savedReply.headers().etag,
      data: saved,
    });
    const history = await request(
      "GET",
      alternatePath + "/history?" + alternateQuery + "&limit=25",
    );
    expect(history.status).toBe(200);
    expect(history.data.items).toEqual([initialHead.data, saved]);
    expect(history.data.nextCursor).toBeUndefined();
    expect(await request("GET", currentPath + "?" + query)).toEqual(
      currentBefore,
    );
    expect(
      await request("GET", currentPath + "/history?" + query + "&limit=25"),
    ).toEqual(currentHistory);
    expect(await request("GET", indexedPath)).toEqual(withBoth);
    expect(outside).toEqual([]);
  } finally {
    page.off("request", observe);
  }
  // Keep the existing source-removal journey's exact empty-list assertion.
  // Removal retains the new source's evidence; it deletes no unrelated state.
  const currentAlternate = await request(
    "GET",
    `/api/v1/federation/sources/${alternateId}`,
  );
  expect(currentAlternate.status).toBe(200);
  const removal = await request(
    "POST",
    `/api/v1/federation/sources/${alternateId}/change-previews`,
    { intent: "remove" },
  );
  expect(removal.status).toBe(200);
  expect(removal.data.currentMatched).toBe(1);
  expect(
    (
      await request(
        "POST",
        `/api/v1/federation/sources/${alternateId}:remove`,
        {
          changePreviewDigest: removal.data.digest,
          reason: "Finish owned alternate artwork qualification",
        },
        currentAlternate.etag,
      )
    ).status,
  ).toBe(204);
  const sources = await request("GET", "/api/v1/federation/sources?limit=25");
  expect(sources.status).toBe(200);
  expect(sources.data.items.map((source) => source.id)).toEqual([sourceId]);
  expect(sources.data.nextCursor).toBeUndefined();
  expect(await request("GET", currentPath + "?" + query)).toEqual(
    currentBefore,
  );
}

// One full persisted author/reviewer/publisher/reader journey. Routing and
// cookies are tested separately at both paths, not with duplicate mutations.
async function publicationJourney({
  page,
  visit,
  login,
  button,
  field,
  link,
  command,
}) {
  const example = JSON.parse(
    await readFile(
      new URL("../../api/examples/agent-lifecycle.json", import.meta.url),
      "utf8",
    ),
  );
  const agent = example.requests.find((r) => r.name === "Create Agent version")
    .body.representations[0].data;
  await login("local-contributor");
  await visit("/resources/new");
  await chooseNamedOption(
    field("Namespace"),
    /^Catalog demo \/ .+ \/ [0-9a-f-]{36}$/i,
  );
  const namespaceId = await field("Namespace").inputValue();
  await field("Canonical resource identifier").fill(
    "urn:air:demo.catalog.example.com:demo:browser-published",
  );
  await button("Next: Content").click();
  await page.getByText("Advanced Agent Card JSON", { exact: true }).click();
  const nativeHelp = page.getByRole("region", {
    name: "Native input guidance",
    exact: true,
  });
  await expect(nativeHelp).toContainText("A2A Agent Card");
  await expect(nativeHelp.getByRole("link")).toHaveAttribute(
    "href",
    "/docs/guides/resource-authoring/#create-an-agent-from-fields",
  );
  await field("Version label").fill("1.0.0");
  await page
    .getByText("Import an existing Agent document", { exact: true })
    .click();
  await page.getByLabel("Document file", { exact: true }).setInputFiles({
    name: "agent-card.json",
    mimeType: "application/json",
    buffer: Buffer.from(JSON.stringify(agent)),
  });
  await button("Use loaded document").click();
  await expect(field("Standards document (JSON)")).toHaveValue(
    JSON.stringify(agent),
  );
  await command("Save content draft");
  await expect(page.getByRole("status")).toContainText(
    "Draft saved. Version 1.0.0.",
  );
  await link("Open saved version").click();
  await link("Inspect current revision and validation").click();
  const revisionId = new URL(page.url()).pathname.split("/").pop();
  await expect(
    page.getByText(/Validation records checks on this revision/),
  ).toBeVisible();
  const validated = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response
        .url()
        .endsWith(`/api/v1/version-revisions/${revisionId}:validate`),
  );
  await command("Revalidate revision");
  const validation = await validated;
  expect(validation.status()).toBe(201);
  expect(validation.request().postDataJSON()).toEqual({});
  const validationBody = await validation.json();
  expect(validationBody).toMatchObject({
    namespaceId,
    revision: { id: revisionId },
    evidence: { state: "complete" },
  });
  const reloadedRevision = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      response.url().endsWith(`/api/v1/version-revisions/${revisionId}`),
  );
  await page.reload();
  const retainedRevision = await reloadedRevision;
  expect(retainedRevision.status()).toBe(200);
  expect(await retainedRevision.json()).toMatchObject({
    namespaceId,
    revision: {
      id: revisionId,
      contentSetDigest: validationBody.revision.contentSetDigest,
      evidenceSetId: validationBody.evidence.id,
    },
    evidence: { id: validationBody.evidence.id, state: "complete" },
  });
  await link("Back to version").click();
  await link("Back to resource").click();
  await button("Listings").click();
  await command("Create listing");
  await link("Open existing listing").click();
  await expect(page.getByText(/No candidate revision yet/)).toBeVisible();
  await expect(button("Submit for review")).toBeDisabled();
  await button("New listing revision").click();
  await page
    .getByRole("combobox", { name: "Resource version", exact: true })
    .selectOption({ label: "1.0.0" });
  await page
    .getByRole("combobox", { name: "Version revision", exact: true })
    .selectOption(revisionId);
  await button("Load name and description suggestions").click();
  const prefill = page.getByRole("combobox", {
    name: "Prefill representation",
    exact: true,
  });
  await expect(prefill.locator("option")).toHaveCount(2);
  await prefill.selectOption({ index: 1 });
  await button("Use suggested name and description").click();
  await expect(field("Display name")).toHaveValue(agent.name);
  await expect(field("Description")).toHaveValue(agent.description);
  await field("Display name").fill("Packaged browser agent");
  await field("Description").fill(
    "Independently reviewed synthetic browser metadata",
  );
  await chooseNamedOption(
    field("Publisher"),
    /^Demo Publisher \/ [0-9a-f-]{36}$/i,
  );
  await button("Add tag").click();
  await field("Tag 1").fill("packaged, discovery");
  await button("Add category").click();
  await field("Category 1").fill("packaged, tools");
  await page
    .getByText("Links, license and relationships", { exact: true })
    .click();
  await field("License").fill("Apache-2.0");
  await page
    .getByRole("combobox", { name: "Visibility", exact: true })
    .selectOption("public");
  await button("Create listing revision").click();
  await expect(page.getByRole("alert")).toBeFocused();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page
    .getByRole("link", { name: /Public listings need two to five/ })
    .click();
  await expect(field("Representative queries (one per line)")).toBeFocused();
  await expect(field("Display name")).toHaveValue("Packaged browser agent");
  await field("Representative queries (one per line)").fill(
    "Find packaged browser agent\nBrowse synthetic browser metadata",
  );
  await command("Create listing revision");
  await link("Reload listing to review saved revision").click();
  await expect(page.getByText(/Candidate revision saved/)).toBeVisible();
  await command("Submit for review");
  await link("Open review submission").click();
  const reviewURL = page.url();
  await button("Inspect submitted evidence").click();
  await expect(button("Record review decision")).toHaveCount(0);
  await expect(
    page.getByText(
      "The review.decide permission is required in this namespace.",
      { exact: true },
    ),
  ).toBeVisible();
  await login("local-reviewer");
  await page.goto(reviewURL);
  await button("Inspect submitted evidence").click();
  await page
    .getByRole("combobox", { name: "Decision", exact: true })
    .selectOption("approve");
  await field("Reason").fill("Independent packaged browser review");
  await command("Record review decision");
  await expect(page.getByRole("status")).toContainText("Review approved.");
  await button("Reload review and next steps").click();
  await expect(page.getByText(/This submission is approved/)).toBeVisible();
  await expect(button("Publish approved listing")).toHaveCount(0);
  await expect(
    page.getByText(
      "The release.publish permission is required in this namespace.",
      { exact: true },
    ),
  ).toBeVisible();
  await login("local-publisher");
  await page.goto(reviewURL);
  await command("Publish approved listing");
  await link("Open published release").click();
  const releaseURL = page.url();
  // Use the genuinely published Listing to qualify browser filters through
  // the packaged UI/API and real PostgreSQL, without response mocking.
  await login("local-reader");
  await visit("/catalog");
  await button("Filters").click();
  await field("License declaration").fill("Apache-2.0");
  await field("Add tag").fill("packaged, discovery");
  await button("Add category").click();
  await field("Category 1").fill("packaged, tools");
  await expect(
    page.getByRole("checkbox", { name: "Indexed external", exact: true }),
  ).toBeEnabled();
  await page
    .getByRole("checkbox", { name: "Indexed external", exact: true })
    .uncheck();
  await page
    .getByRole("checkbox", { name: "Live external", exact: true })
    .uncheck();
  const filterResponse = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/catalog/search") &&
      response.request().postDataJSON()?.license === "Apache-2.0",
  );
  await button("Apply filters").click();
  const filtered = await filterResponse;
  expect(filtered.status()).toBe(200);
  expect(filtered.request().postDataJSON()).toMatchObject({
    license: "Apache-2.0",
    categories: ["packaged, tools"],
    tags: ["packaged, discovery"],
    origins: ["local"],
  });
  const filteredBody = await filtered.json();
  expect(filteredBody.durableTotal).toBe(1);
  expect(filteredBody.liveCount).toBe(0);
  expect(filteredBody.items.map((item) => item.type)).toEqual(["local"]);
  await expect(page.locator("article.resource-card")).toHaveCount(1);
  await expect(page.locator("article.resource-card")).toContainText(
    "Packaged browser agent",
  );
  await button("Filters").click();
  await field("License declaration").fill("apache-2.0");
  await button("Apply filters").click();
  await expect(
    page.getByRole("heading", { name: "No resources found", exact: true }),
  ).toBeVisible();
  await button("Remove license: apache-2.0").click();
  await expect(page.locator("article.resource-card")).toHaveCount(1);
  await visit("/explore");
  await button("View counts").click();
  await button("Filter by category packaged, tools: 1").click();
  await expect(
    page.getByRole("status", { name: "Matched releases" }),
  ).toHaveText("1 matching local release");
  await page.getByText("Tags and categories", { exact: true }).click();
  await expect(field("Category 1")).toHaveValue("packaged, tools");
  await page
    .getByText("Publisher, license and visibility", { exact: true })
    .click();
  await field("License declaration").fill("Apache-2.0");
  await button("Apply filters").click();
  await expect(
    page.getByRole("status", { name: "Matched releases" }),
  ).toHaveText("1 matching local release");
  await button("Remove category packaged, tools").click();
  await expect(
    page.getByRole("status", { name: "Matched releases" }),
  ).toHaveText("4 matching local releases");
  await login("local-publisher");
  await page.goto(releaseURL);
  await expect(link("Public discovery snapshots")).toBeVisible();
  await link("Public discovery snapshots").click();
  const hosts = page.getByRole("combobox", {
    name: "Configured publication hosts",
    exact: true,
  });
  await expect(hosts).toHaveValue("");
  const configured = hosts.locator("option:not([value='']):not([disabled])");
  // The functional Compose overlay supplies both audiences, overriding api.json.
  await expect(configured).toHaveText([
    "127.0.0.1 — anonymous",
    "private.catalog.test — authenticated",
  ]);
  const configuredHost = "127.0.0.1";
  await expect(button("Build candidate snapshot")).toHaveCount(0);
  await hosts.selectOption(configuredHost);
  await expect(field("Configured discovery host")).toHaveValue(configuredHost);
  await expect(button("Build candidate snapshot")).toHaveCount(0);
  await button("Open host").click();
  await expect(button("Build candidate snapshot")).toBeEnabled();
  await page.goto(releaseURL);
  await expect(
    page.getByText(/this screen does not establish that a snapshot is active/),
  ).toBeVisible();
  await login("local-reader");
  await page.goto(releaseURL);
  await expect(button("Change release disposition")).toHaveCount(0);
  await expect(
    page.getByText(
      "The release.transition permission is required in this namespace.",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(link("Public discovery snapshots")).toHaveCount(0);
  await link("Catalog record").click();
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Packaged browser agent", exact: true }),
  ).toBeVisible();
  await link("Release history").click();
  const historyHeading = page.getByRole("heading", {
    name: "Release history",
    exact: true,
  });
  await expect(historyHeading).toBeFocused();
  await page.reload();
  await expect(historyHeading).toBeFocused();
  await expect(
    page.getByText("Current listing release", { exact: true }),
  ).toBeVisible();
  await page
    .locator(`.release-history-entry a[href="${new URL(releaseURL).pathname}"]`)
    .click();
  await expect(page).toHaveURL(releaseURL);
  for (const label of [
    "Exact version revision",
    "Released listing revision",
    "Activated",
    "Disposition last updated",
  ]) {
    await expect(page.getByText(label, { exact: true })).toBeVisible();
  }
  await expect(button("Change release disposition")).toHaveCount(0);
  await expect(
    page.getByText(
      "The release.transition permission is required in this namespace.",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(link("Public discovery snapshots")).toHaveCount(0);
}

// One persisted private-feedback handoff complements the memory-browser and
// packaged API transition matrices. Only role prerequisites use direct commands.
export async function privateFeedbackJourney({ page, origin, network, api }) {
  const button = (name) => page.getByRole("button", { name, exact: true });
  const field = (name) => page.getByRole("textbox", { name, exact: true });
  const visit = (path) => page.goto(origin + path);
  const login = async (alias) => {
    await page.getByTitle("Account", { exact: true }).click();
    await button("Sign out").click();
    await page.locator(`[data-login-alias="${alias}"]`).click();
    await expect(page.getByTitle("Account", { exact: true })).toBeVisible();
  };
  const comment = "Owned packaged feedback <script>inert report</script>";
  const responseText = "Owned moderator response retained after API restart";
  const responseParagraph = page
    .getByText(responseText, { exact: true })
    .and(page.locator("p"));
  const request = (method, path, body, etag) =>
    page.evaluate(
      async ({ url, method, body, etag, key }) => {
        const response = await fetch(url, {
          method,
          credentials: "same-origin",
          cache: "no-store",
          signal: AbortSignal.timeout(10000),
          headers: {
            "X-Catalog-Request": "1",
            ...(body === undefined
              ? {}
              : { "Content-Type": "application/json" }),
            ...(key ? { "Idempotency-Key": key } : {}),
            ...(etag ? { "If-Match": etag } : {}),
          },
          ...(body === undefined ? {} : { body: JSON.stringify(body) }),
        });
        return {
          status: response.status,
          etag: response.headers.get("ETag"),
          cache: response.headers.get("Cache-Control"),
          data: await response.json(),
        };
      },
      {
        url: origin + "/_catalog" + path,
        method,
        body,
        etag,
        key: method === "GET" ? undefined : randomUUID(),
      },
    );
  // The existing owner seed publishes this release through governed commands.
  // Select one bounded exact target without depending on another browser journey.
  const targets = await request(
    "GET",
    "/api/v1/catalog/resources?kind=agent&limit=1",
  );
  expect(targets.status).toBe(200);
  expect(targets.data.items).toHaveLength(1);
  const releaseId = targets.data.items[0].release.id;
  expect(releaseId).toMatch(/^[0-9a-f-]{36}$/);
  const target = await request("GET", `/api/v1/catalog/resources/${releaseId}`);
  expect(target.status).toBe(200);
  expect(target.data.release.id).toBe(releaseId);
  expect(target.data.release.state).toBe("published");
  const resourceId = target.data.resource.id;
  const versionRevisionId = target.data.versionRevision.id;
  const releaseURL = origin + `/releases/${releaseId}`;
  await login("local-template-library-member");
  const initialPermissions = await request("GET", "/api/v1/me");
  expect(initialPermissions.status).toBe(200);
  expect(initialPermissions.data.actorType).toBe("human");
  expect(initialPermissions.data.availablePermissions).not.toContain(
    "feedback.moderate",
  );
  expect(initialPermissions.data.availablePermissions).not.toContain(
    "resource.read",
  );
  await login("local-admin");
  const members = await request("GET", "/api/v1/memberships?limit=100");
  expect(members.status).toBe(200);
  expect(members.data.nextCursor).toBeFalsy();
  const moderator = members.data.items.find(
    (member) => member.subject === "local-template-library-member",
  );
  expect(moderator?.id).toMatch(/^[0-9a-f-]{36}$/);
  const role = await request("POST", "/api/v1/access/roles", {
    name: "Owned packaged feedback moderation",
    permissions: ["feedback.moderate"],
    reason: "Qualify private moderation without Resource reads",
  });
  expect(role.status).toBe(201);
  expect(role.data.permissions).toEqual(["feedback.moderate"]);
  const assignment = await request("POST", "/api/v1/access/assignments", {
    membershipId: moderator.id,
    roleDefinitionId: role.data.id,
    reason: "Grant only the owned feedback moderation permission",
  });
  expect(assignment.status).toBe(201);
  await login("local-reader");
  await visit(`/catalog/${releaseId}`);
  await page.getByText("Item feedback", { exact: true }).click();
  const writes = [];
  const feedbackPath = new URL(origin + "/_catalog/api/v1/feedback").pathname;
  const observe = (request) => {
    const url = new URL(request.url());
    if (
      !["GET", "HEAD", "OPTIONS"].includes(request.method()) &&
      url.pathname.startsWith(feedbackPath)
    )
      writes.push({
        method: request.method(),
        path: url.pathname,
        key: request.headers()["idempotency-key"],
        etag: request.headers()["if-match"],
        body: request.postDataJSON(),
      });
  };
  page.on("request", observe);
  const commit = async (label, method, path, status) => {
    await button(label).click();
    const [reply] = await Promise.all([
      page.waitForResponse(
        (reply) =>
          reply.request().method() === method &&
          reply.url() === origin + "/_catalog" + path,
      ),
      page
        .getByRole("dialog")
        .getByRole("button", { name: "Confirm", exact: true })
        .click(),
    ]);
    expect(reply.status()).toBe(status);
    expect(reply.request().headers()["idempotency-key"]).toBeTruthy();
    expect(reply.headers()["cache-control"]).toBe("no-store");
    const receipt = await reply.json();
    expect(receipt).toEqual({
      id: expect.stringMatching(/^[0-9a-f-]{36}$/),
      revision: expect.any(Number),
    });
    expect(reply.headers().etag).toBe(`"rev-${receipt.revision}"`);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    return receipt;
  };
  try {
    await page
      .getByRole("combobox", { name: "Category", exact: true })
      .selectOption("documentation");
    await field("Feedback comment").fill(comment);
    await button("Submit feedback").click();
    expect(writes).toEqual([]);
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Cancel", exact: true })
      .click();
    await expect(field("Feedback comment")).toHaveValue(comment);
    expect(writes).toEqual([]);
    const created = await commit(
      "Submit feedback",
      "POST",
      "/api/v1/feedback",
      201,
    );
    expect(created.revision).toBe(1);
    const path = `/api/v1/feedback/${created.id}`;
    const own = await request("GET", path);
    expect(own.status).toBe(200);
    expect(own.cache).toBe("no-store");
    expect(own.etag).toBe('"rev-1"');
    expect(own.data).toMatchObject({
      id: created.id,
      resourceId,
      releaseId,
      versionRevisionId,
      state: "open",
      revision: 1,
      category: "documentation",
      comment,
    });
    expect(writes).toHaveLength(1);
    expect(writes[0]).toMatchObject({
      method: "POST",
      body: {
        resourceId,
        releaseId,
        versionRevisionId,
        category: "documentation",
        comment,
      },
    });

    await login("local-template-library-member");
    const permissions = await request("GET", "/api/v1/me");
    expect(permissions.status).toBe(200);
    expect(permissions.data.availablePermissions).toContain(
      "feedback.moderate",
    );
    expect(permissions.data.availablePermissions).not.toContain(
      "resource.read",
    );
    network.expectFailure(
      "GET",
      `/_catalog/api/v1/resources/${resourceId}`,
      403,
    );
    const forbiddenResource = await request(
      "GET",
      `/api/v1/resources/${resourceId}`,
    );
    expect(forbiddenResource.status).toBe(403);
    expect(forbiddenResource.data.code).toBe("FORBIDDEN");
    expect(forbiddenResource.data).not.toHaveProperty("canonicalIdentifier");
    await visit(`/feedback/${created.id}`);
    await expect(page.getByText(comment, { exact: true })).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Resource and versions", exact: true }),
    ).toHaveCount(0);
    await openDisclosure(page, "Respond or manage feedback");
    await field("Moderator response (optional)").fill(responseText);
    const addressed = await commit(
      "Address feedback",
      "POST",
      path + ":address",
      200,
    );
    expect(addressed).toEqual({ id: created.id, revision: 2 });
    expect(writes).toHaveLength(2);
    expect(writes[1]).toMatchObject({
      method: "POST",
      etag: own.etag,
      body: { response: responseText },
    });
    const persisted = await request("GET", path);
    expect(persisted.status).toBe(200);
    expect(persisted.data).toMatchObject({
      ...own.data,
      state: "addressed",
      revision: 2,
      response: responseText,
      updatedAt: expect.any(String),
    });
    expect(persisted.etag).toBe('"rev-2"');
    await api(false);
    await api(true);
    await page.reload();
    await expect(page.getByText(comment, { exact: true })).toBeVisible();
    await expect(responseParagraph).toBeVisible();
    expect(await request("GET", path)).toEqual(persisted);

    await login("local-contributor");
    const other = await request("GET", "/api/v1/me");
    expect(other.status).toBe(200);
    expect(other.data.availablePermissions).toContain("feedback.own");
    expect(other.data.availablePermissions).not.toContain("feedback.moderate");
    network.expectFailure("GET", "/_catalog" + path, 404);
    const [denied] = await Promise.all([
      page.waitForResponse(
        (reply) =>
          reply.url() === origin + "/_catalog" + path &&
          reply.request().method() === "GET",
      ),
      visit(`/my-feedback/${created.id}`),
    ]);
    expect(denied.status()).toBe(404);
    expect((await denied.json()).code).toBe("NOT_FOUND");
    await expect(page.getByRole("alert")).toContainText("NOT_FOUND");
    await expect(page.getByText(comment, { exact: true })).toHaveCount(0);
    await expect(page.getByText(responseText, { exact: true })).toHaveCount(0);
    await expect(button("Withdraw feedback")).toHaveCount(0);
    expect(writes).toHaveLength(2);
    await login("local-reader");
    await visit(`/my-feedback/${created.id}`);
    await expect(responseParagraph).toBeVisible();
    expect(await request("GET", path)).toEqual(persisted);
  } finally {
    page.off("request", observe);
  }
  // Restore the exact template before later authorization journeys use it.
  await login("local-admin");
  const revoked = await request(
    "POST",
    `/api/v1/access/assignments/${assignment.data.id}:revoke`,
    {
      reason: "Finish owned private-feedback qualification",
    },
    `"rev-${assignment.data.revision}"`,
  );
  expect(revoked.status).toBe(200);
  expect(revoked.data).toMatchObject({
    id: assignment.data.id,
    membershipId: moderator.id,
    roleDefinitionId: role.data.id,
    state: "revoked",
    revision: assignment.data.revision + 1,
  });
  const retainedAssignment = await request(
    "GET",
    `/api/v1/access/assignments?roleDefinitionId=${role.data.id}&membershipId=${moderator.id}&limit=25`,
  );
  expect(retainedAssignment.status).toBe(200);
  expect(retainedAssignment.data.items).toEqual([revoked.data]);
  expect(retainedAssignment.data.nextCursor).toBeFalsy();
  await login("local-template-library-member");
  const restored = await request("GET", "/api/v1/me");
  expect(restored.status).toBe(200);
  expect(restored.data.availablePermissions).toContain("library.own");
  expect(restored.data.availablePermissions).not.toContain("feedback.moderate");
  await login("local-reader");
  await page.goto(releaseURL);
}
