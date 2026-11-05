import { test, expect, login, signOut } from "./fixtures";
import { object } from "./json";
import { trustEntry, trustSample } from "../unit/trust-fixtures";
import type { components } from "../../src/api/schema";
import { openDisclosure } from "./editor-disclosure";

test("subpath: ANS Jobs preserve confirmed cancellation, exact retry and private-read boundaries", async ({
  page,
  catalog,
}) => {
  const cancelID = "00000000-0000-7000-8000-000000000004";
  const failedID = "00000000-0000-7000-8000-000000000005";
  const retryID = "00000000-0000-7000-8000-000000000006";
  const stamp = "2026-10-11T00:00:00Z";
  const common = {
    tenantId: "00000000-0000-7000-8000-000000000001",
    kind: "source.trust_verify",
    priority: 0,
    progress: 0,
    notBefore: stamp,
    attempt: 0,
    attemptLimit: 3,
    cancellationRequested: false,
    createdAt: stamp,
    updatedAt: stamp,
  };
  const records = new Map<string, components["schemas"]["Job"]>([
    [cancelID, { ...common, id: cancelID, state: "running" }],
    [failedID, { ...common, id: failedID, state: "failed", attempt: 1 }],
    [
      retryID,
      { ...common, id: retryID, state: "queued", parentJobId: failedID },
    ],
  ]);
  const cancels: { key: string | undefined; body: unknown }[] = [];
  const retries: { key: string | undefined; body: unknown }[] = [];
  let reads = 0;
  let privateReads = 0;
  await page.route("**/_catalog/api/v1/federation/**", async (route) => {
    privateReads++;
    await route.abort();
  });
  for (const [id, record] of records) {
    await page.route(`**/_catalog/api/v1/jobs/${id}`, async (route) => {
      expect(route.request().method()).toBe("GET");
      reads++;
      await route.fulfill({ json: record });
    });
  }
  await page.route(
    `**/_catalog/api/v1/jobs/${cancelID}:cancel`,
    async (route) => {
      expect(route.request().method()).toBe("POST");
      expect(route.request().headers()["if-match"]).toBeUndefined();
      cancels.push({
        key: route.request().headers()["idempotency-key"],
        body: route.request().postDataJSON() as unknown,
      });
      await route.fulfill({
        json:
          cancels.length === 1
            ? {}
            : {
                ...common,
                id: cancelID,
                state: "running",
                cancellationRequested: true,
              },
      });
    },
  );
  await page.route(
    `**/_catalog/api/v1/jobs/${failedID}:retry`,
    async (route) => {
      expect(route.request().method()).toBe("POST");
      expect(route.request().headers()["if-match"]).toBeUndefined();
      retries.push({
        key: route.request().headers()["idempotency-key"],
        body: route.request().postDataJSON() as unknown,
      });
      await route.fulfill({ status: 202, json: records.get(retryID) });
    },
  );

  await login(page, catalog, "local-reader", `/jobs/${cancelID}`);
  await expect(page.getByRole("alert")).toContainText(
    "Your access does not include",
  );
  await expect(
    page
      .getByRole("navigation", { name: "Workspace", exact: true })
      .getByRole("link", { name: "Jobs", exact: true }),
  ).toHaveCount(0);
  expect(reads).toBe(0);
  await signOut(page);
  await login(page, catalog, "local-source", `/jobs/${cancelID}`);
  await expect(
    page.getByRole("heading", {
      name: "ANS registration assessment",
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    page
      .getByRole("navigation", { name: "Workspace", exact: true })
      .getByRole("link", { name: "Jobs", exact: true }),
  ).toBeVisible();
  const reason = page.getByRole("textbox", { name: "Reason", exact: true });
  await reason.fill("  Stop duplicate assessment  ");
  await page.getByRole("button", { name: "Cancel job", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("completed side effects are not undone");
  await expect(
    dialog.getByRole("button", { name: "Confirm", exact: true }),
  ).toHaveClass("warning");
  await expect(
    dialog
      .getByRole("button", { name: "Confirm", exact: true })
      .locator('svg[aria-hidden="true"]'),
  ).toHaveCount(1);
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(reason).toHaveValue("  Stop duplicate assessment  ");
  expect(cancels).toEqual([]);
  await page.getByRole("button", { name: "Cancel job", exact: true }).click();
  await dialog.getByRole("button", { name: "Confirm", exact: true }).click();
  await expect(dialog.getByRole("alert")).toContainText("outcome is unknown");
  expect(cancels).toHaveLength(1);
  await expect(
    dialog.getByRole("button", { name: "Retry same command", exact: true }),
  ).toHaveClass("warning");
  await dialog
    .getByRole("button", { name: "Retry same command", exact: true })
    .click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByRole("status")).toContainText(
    "Running; cancellation requested",
  );
  expect(cancels).toHaveLength(2);
  expect(cancels[1]).toEqual(cancels[0]);
  expect(cancels[0]?.body).toEqual({ reason: "Stop duplicate assessment" });
  expect(cancels[0]?.key).toMatch(/^[\da-f-]{36}$/);
  await expect(
    page.getByRole("button", { name: "Cancel job", exact: true }),
  ).toHaveCount(0);

  await page.goto(`${catalog.origin}${catalog.basePath}/jobs/${failedID}`);
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Evidence restored");
  await page.getByRole("button", { name: "Retry job", exact: true }).click();
  await expect(dialog).toContainText(
    "Create a new source.trust_verify Job linked to this one",
  );
  await expect(dialog).toContainText(
    "Finished Job records can expire under the deployment's retention policy",
  );
  expect(retries).toEqual([]);
  await dialog.getByRole("button", { name: "Confirm", exact: true }).click();
  const replacement = page.getByRole("link", {
    name: "Open retry job",
    exact: true,
  });
  await expect(replacement).toHaveAttribute(
    "href",
    `${catalog.basePath}/jobs/${retryID}`,
  );
  expect(retries).toHaveLength(1);
  expect(retries[0]?.body).toEqual({ reason: "Evidence restored" });
  expect(retries[0]?.key).toMatch(/^[\da-f-]{36}$/);
  expect(retries[0]?.key).not.toBe(cancels[0]?.key);
  await replacement.click();
  const original = page.getByRole("link", { name: failedID, exact: true });
  await expect(original).toHaveAttribute(
    "href",
    `${catalog.basePath}/jobs/${failedID}`,
  );
  await original.click();
  await expect(
    page.getByText("State", { exact: true }).locator("+ dd"),
  ).toHaveText("Failed");
  expect(reads).toBe(4);
  expect(privateReads).toBe(0);
});

// Owned transport controls qualify selection and confirmation, not cryptographic checks.
test("subpath: ANS selection reads evidence explicitly and recovers the exact assessment command", async ({
  page,
  catalog,
}) => {
  const sourceID = "00000000-0000-7000-8000-000000000001";
  const observationID = "00000000-0000-7000-8000-000000000002";
  const snapshotID = "00000000-0000-7000-8000-000000000003";
  const jobID = "00000000-0000-7000-8000-000000000004";
  const identifier = "urn:air:peer.invalid:agents:helper";
  const privateAssessment = trustSample();
  const digest = privateAssessment.result.exactEntryDigest;
  const mediaType = "application/a2a-agent-card+json";
  const stamp = "2026-10-11T00:00:00Z";
  let sourceReads = 0;
  let observationReads = 0;
  let evidenceReads = 0;
  let resultReads = 0;
  const commands: {
    body: unknown;
    key: string | undefined;
    ifMatch: string | undefined;
  }[] = [];
  await page.route(
    `**/_catalog/api/v1/federation/sources/${sourceID}`,
    async (route) => {
      sourceReads++;
      await route.fulfill({
        json: {
          id: sourceID,
          tenantId: "00000000-0000-7000-8000-000000000001",
          displayName: "ANS selection peer",
          endpoint: "https://peer.invalid/ard/",
          revision: 7,
          status: "active",
          profileId: "ard",
          retrievalMode: "hybrid",
          updateMode: "manual_review",
          scope: { kinds: ["agent"] },
          live: {},
          nesting: {},
          referrals: {},
          trustVerificationPolicy: "operator-ans",
          lastCompleteSnapshotId: snapshotID,
        },
      });
    },
  );
  await page.route("**/_catalog/api/v1/observations?**", async (route) => {
    observationReads++;
    const query = new URL(route.request().url()).searchParams;
    expect(query.get("sourceId")).toBe(sourceID);
    expect(query.get("freshness")).toBe("current");
    expect(query.get("limit")).toBe("25");
    await route.fulfill({
      json: {
        items: [
          {
            id: observationID,
            sourceId: sourceID,
            sourceKey: "helper",
            identifier,
            versionPresent: false,
            mediaType,
            completeness: "complete",
            freshness: "current",
            compatibility: {
              structural: "valid",
              support: "supported",
              profileId: "ard",
            },
            rawEntryAvailable: true,
            exactEntryDigest: digest,
          },
        ],
      },
    });
  });
  await page.route(
    `**/_catalog/api/v1/observations/${observationID}/raw`,
    async (route) => {
      evidenceReads++;
      await route.fulfill({
        contentType: "application/json",
        body: trustEntry,
      });
    },
  );
  await page.route(
    `**/_catalog/api/v1/federation/sources/${sourceID}/trust-assessments`,
    async (route) => {
      const body: unknown = route.request().postDataJSON();
      commands.push({
        body,
        key: route.request().headers()["idempotency-key"],
        ifMatch: route.request().headers()["if-match"],
      });
      await route.fulfill({
        status: 202,
        json:
          commands.length === 1
            ? {}
            : {
                id: jobID,
                kind: "source.trust_verify",
                state: "queued",
                priority: 0,
                progress: 0,
                notBefore: stamp,
                attempt: 0,
                attemptLimit: 3,
                cancellationRequested: false,
                createdAt: stamp,
                updatedAt: stamp,
              },
      });
    },
  );
  await page.route(
    `**/_catalog/api/v1/federation/sources/${sourceID}/trust-assessments/*`,
    async (route) => {
      resultReads++;
      expect(route.request().method()).toBe("GET");
      expect(new URL(route.request().url()).pathname).toBe(
        `${catalog.basePath}/_catalog/api/v1/federation/sources/${sourceID}/trust-assessments/${jobID}`,
      );
      await route.fulfill({ json: privateAssessment });
    },
  );

  await login(page, catalog, "local-reader", `/sources/${sourceID}`);
  await expect(page.getByRole("alert")).toContainText("source.read");
  expect(sourceReads).toBe(0);
  await signOut(page);
  await login(page, catalog, "local-source", `/sources/${sourceID}`);
  await expect(
    page.getByRole("heading", { name: "ANS selection peer", exact: true }),
  ).toBeVisible();
  expect(sourceReads).toBe(1);
  await page
    .getByRole("button", { name: "ANS registration", exact: true })
    .click();
  expect(observationReads).toBe(0);
  expect(evidenceReads).toBe(0);
  await page
    .getByRole("button", { name: "Choose indexed entry", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: identifier, exact: true }),
  ).toBeVisible();
  expect(evidenceReads).toBe(0);
  await page
    .getByRole("button", { name: "Select indexed entry", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Request ANS assessment", exact: true }),
  ).toBeVisible();
  expect(evidenceReads).toBe(1);
  expect(commands).toEqual([]);
  await page.setViewportSize({ width: 390, height: 844 });
  await expect
    .poll(() => page.evaluate(() => document.documentElement.scrollWidth))
    .toBeLessThanOrEqual(390);
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Verify the selected registration");
  await page
    .getByRole("button", { name: "Request ANS assessment", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("alert")).toContainText("outcome is unknown");
  expect(commands).toHaveLength(1);
  await dialog
    .getByRole("button", { name: "Retry same command", exact: true })
    .click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByText(/Assessment Job:/)).toContainText(jobID);
  expect(commands).toHaveLength(2);
  expect(commands[1]).toEqual(commands[0]);
  expect(commands[0]?.ifMatch).toBeUndefined();
  const body = object(commands[0]?.body);
  expect(body).toEqual({
    sourceRevision: 7,
    observationId: observationID,
    identifier,
    versionPresent: false,
    mediaType,
    delivery: "data",
    exactEntryDigest: digest,
    reason: "Verify the selected registration",
  });
  expect(commands[0]?.key).toMatch(/^[\da-f-]{36}$/);
  expect(resultReads).toBe(0);
  await page
    .getByRole("link", { name: "Open private assessment", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "ANS assessments", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("textbox", { name: "Source ID", exact: true }),
  ).toHaveValue(sourceID);
  await expect(
    page.getByRole("textbox", { name: "Assessment Job ID", exact: true }),
  ).toHaveValue(jobID);
  expect(resultReads).toBe(0);
  await page
    .getByRole("button", { name: "Read private assessment", exact: true })
    .click();
  await expect(page.getByText(/Current registration under/)).toBeVisible();
  const technical = page.locator("details").filter({
    has: page.getByText("Assessment technical details", { exact: true }),
  });
  await expect(technical).toHaveJSProperty("open", false);
  await openDisclosure(page, "Assessment technical details");
  await expect(page.getByText(digest, { exact: true })).toBeVisible();
  await expect(page.getByRole("row")).toHaveCount(8);
  expect(resultReads).toBe(1);
  expect(sourceReads).toBe(1);
  expect(observationReads).toBe(1);
  expect(evidenceReads).toBe(1);
  expect(commands).toHaveLength(2);
});

test("subpath: live ANS selection requires a current source and preserves the submitted query and empty version", async ({
  page,
  catalog,
}) => {
  const sourceID = "00000000-0000-7000-8000-000000000001";
  const jobID = "00000000-0000-7000-8000-000000000004";
  const identifier = "urn:air:peer.invalid:agents:helper";
  const digest = `sha256:${"b".repeat(64)}`;
  const mediaType = "application/a2a-agent-card+json";
  const stamp = "2026-10-11T00:00:00Z";
  let sourceReads = 0;
  let evidenceReads = 0;
  const liveQueries: unknown[] = [];
  const assessments: {
    body: unknown;
    key: string | undefined;
    ifMatch: string | undefined;
  }[] = [];
  await page.route(
    "**/_catalog/api/v1/federation/sources:live-search",
    async (route) => {
      liveQueries.push(route.request().postDataJSON());
      await route.fulfill({
        json: {
          results: [
            {
              identifier,
              versionPresent: true,
              version: "",
              score: 91,
              sourceId: sourceID,
              source: "https://peer.invalid/ard/",
              sourceRevision: liveQueries.length === 1 ? 7 : 8,
              exactEntryDigest: digest,
              entry: {
                identifier,
                version: "",
                type: mediaType,
                url: "https://peer.invalid/card.json",
              },
            },
          ],
          partial: false,
        },
      });
    },
  );
  await page.route(
    `**/_catalog/api/v1/federation/sources/${sourceID}`,
    async (route) => {
      sourceReads++;
      await route.fulfill({
        json: {
          id: sourceID,
          displayName: "ANS live selection peer",
          endpoint: "https://peer.invalid/ard/",
          revision: 8,
          status: "active",
          profileId: "ard",
          retrievalMode: "hybrid",
          updateMode: "manual_review",
          scope: {},
          live: { enabled: true },
          nesting: {},
          referrals: {},
          trustVerificationPolicy: "operator-ans",
        },
      });
    },
  );
  await page.route("**/_catalog/api/v1/observations/**", async (route) => {
    evidenceReads++;
    await route.fulfill({ status: 500 });
  });
  await page.route(
    `**/_catalog/api/v1/federation/sources/${sourceID}/trust-assessments`,
    async (route) => {
      assessments.push({
        body: route.request().postDataJSON(),
        key: route.request().headers()["idempotency-key"],
        ifMatch: route.request().headers()["if-match"],
      });
      await route.fulfill({
        status: 202,
        json: {
          id: jobID,
          kind: "source.trust_verify",
          state: "queued",
          priority: 0,
          progress: 0,
          notBefore: stamp,
          attempt: 0,
          attemptLimit: 3,
          cancellationRequested: false,
          createdAt: stamp,
          updatedAt: stamp,
        },
      });
    },
  );
  await login(page, catalog, "local-source", "/federation-search");
  await page
    .getByRole("textbox", { name: "Query", exact: true })
    .fill("older query");
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Inspect live entries");
  await page
    .getByRole("button", { name: "Query live sources", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  await expect(
    page.getByRole("button", {
      name: "Select for ANS assessment",
      exact: true,
    }),
  ).toBeEnabled();
  expect(sourceReads).toBe(0);
  await page
    .getByRole("button", { name: "Select for ANS assessment", exact: true })
    .click();
  await expect(
    page.getByText(
      "The source changed. Run a fresh live diagnostic query and select the entry again.",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Request ANS assessment", exact: true }),
  ).toHaveCount(0);
  expect(sourceReads).toBe(1);
  expect(assessments).toEqual([]);
  await page
    .getByRole("button", { name: "Start a new live query", exact: true })
    .click();
  await page
    .getByRole("textbox", { name: "Query", exact: true })
    .fill("fresh query");
  await page
    .getByRole("button", { name: "Query live sources", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  await expect(
    page.getByRole("button", {
      name: "Select for ANS assessment",
      exact: true,
    }),
  ).toBeEnabled();
  await page
    .getByRole("textbox", { name: "Query", exact: true })
    .fill("unsent edits");
  await page
    .getByRole("button", { name: "Select for ANS assessment", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Request ANS assessment", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText(/Confirming this request sends the original query/),
  ).toContainText("fresh query");
  expect(assessments).toEqual([]);
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .last()
    .fill("Assess the selected live registration");
  await page
    .getByRole("button", { name: "Request ANS assessment", exact: true })
    .click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText(
    "Confirming repeats the original query with this source",
  );
  await expect(dialog.getByLabel("Command summary")).not.toContainText(
    "unsent edits",
  );
  await expect(dialog.getByRole("definition")).toContainText([
    "ANS live selection peer",
    "operator-ans",
    identifier,
    "(empty)",
    `${mediaType}, url`,
    digest,
    "fresh query",
    "Assess the selected live registration",
  ]);
  expect(assessments).toEqual([]);
  await dialog.getByRole("button", { name: "Confirm", exact: true }).click();
  await expect(page.getByText(/Assessment Job:/)).toContainText(jobID);
  expect(assessments).toHaveLength(1);
  expect(assessments[0]?.body).toEqual({
    sourceRevision: 8,
    identifier,
    versionPresent: true,
    version: "",
    mediaType,
    delivery: "url",
    exactEntryDigest: digest,
    text: "fresh query",
    reason: "Assess the selected live registration",
  });
  expect(assessments[0]?.key).toMatch(/^[\da-f-]{36}$/);
  expect(assessments[0]?.ifMatch).toBeUndefined();
  expect(liveQueries).toEqual([
    { text: "older query", reason: "Inspect live entries" },
    { text: "fresh query", reason: "Inspect live entries" },
  ]);
  expect(sourceReads).toBe(2);
  expect(evidenceReads).toBe(0);
});
