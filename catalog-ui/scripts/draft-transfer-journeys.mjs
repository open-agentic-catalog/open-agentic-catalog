// UI/CLI interchange between the existing owned PostgreSQL Catalogs. No resource invocation.
import { chromium, expect } from "@playwright/test";
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { randomBytes, randomUUID } from "node:crypto";
import { readFile, writeFile, mkdir, chmod } from "node:fs/promises";
import { join } from "node:path";
import {
  startProcess,
  stopProcess,
  localPorts,
  ready,
  runtimeEnvironment,
} from "./browser-fixture.mjs";
import { networkObserver } from "./network-observer.mjs";

const [directory] = process.argv.slice(2);
let input;
const kinds = ["agent", "mcp_server", "skill", "plugin"];
const execute = promisify(execFile);
const hosts = [];
const ledgers = [];
const errors = [];
let browser;
let stage = "startup";
const stop = () => {
  for (const host of hosts) host.child.kill("SIGTERM");
  void browser?.close();
};
process.on("SIGINT", stop);
process.on("SIGTERM", stop);
let forbiddenRequests = 0;
const privateJSON = async (name, value) => {
  const path = join(directory, name);
  await writeFile(path, JSON.stringify(value), { mode: 0o600 });
  return path;
};
async function api(side, method, path, body, actor = "local-admin", etag) {
  const response = await fetch(input[side] + path, {
    method,
    redirect: "error",
    signal: AbortSignal.timeout(10000),
    headers: {
      Authorization: `Bearer ${side === "a" ? "peer-" : ""}${actor}`,
      "Content-Type": "application/json",
      ...(method !== "GET" ? { "Idempotency-Key": randomUUID() } : {}),
      ...(etag ? { "If-Match": etag } : {}),
    },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  assert.ok(
    response.ok,
    `Owned transfer API ${method} failed: ${response.status}`,
  );
  return { data: await response.json(), etag: response.headers.get("ETag") };
}
async function cli(side, args, actor = "local-contributor") {
  const token = join(directory, `${side}-${actor}.token`);
  await writeFile(token, `${side === "a" ? "peer-" : ""}${actor}`, {
    mode: 0o600,
  });
  let stdout;
  try {
    ({ stdout } = await execute(
      join(directory, "catalogctl"),
      [...args, "--base-url", input[side], "--token-file", token],
      { env: runtimeEnvironment(), timeout: 30000, maxBuffer: 1 << 20 },
    ));
  } catch {
    throw new Error("Owned transfer CLI command failed");
  }
  return JSON.parse(stdout);
}
async function host(side) {
  const [port] = await localPorts();
  const origin = `http://127.0.0.1:${port}`;
  const keysFile = await privateJSON(`${side}-keys.json`, [
    {
      hash: randomBytes(64).toString("hex"),
      block: randomBytes(32).toString("hex"),
    },
  ]);
  const developmentUsersFile = await privateJSON(
    `${side}-users.json`,
    ["local-reader", "local-contributor"].map((alias) => ({
      alias,
      label: alias,
      token: `${side === "a" ? "peer-" : ""}${alias}`,
    })),
  );
  const config = await privateJSON(`${side}-ui.json`, {
    listen: `127.0.0.1:${port}`,
    environment: "development",
    origin,
    upstream: input[side],
    assets: join(directory, "assets"),
    cookieName: `draft-transfer-${side}-${port}`,
    keysFile,
    sessionSeconds: 3600,
    requestBytes: 2097152,
    responseBytes: 16777216,
    timeoutSeconds: 30,
    authMode: "development",
    developmentUsersFile,
  });
  const process = startProcess(join(directory, "catalog-ui"), [
    "--config",
    config,
  ]);
  hosts.push(process);
  await ready(origin, process);
  return origin;
}
async function open(origin, alias, path) {
  const context = await browser.newContext();
  const page = await context.newPage();
  const ledger = networkObserver(origin);
  ledgers.push(ledger);
  page.on("pageerror", () => errors.push("browser exception"));
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.origin !== origin && !["blob:", "data:"].includes(url.protocol))
      forbiddenRequests++;
  });
  page.on("response", (response) =>
    ledger.observe(
      response.request().method(),
      response.url(),
      response.status(),
    ),
  );
  page.on("requestfailed", (request) =>
    ledger.failure(
      request.method(),
      request.url(),
      request.failure()?.errorText,
    ),
  );
  await page.goto(origin + path);
  await page.locator(`[data-login-alias="${alias}"]`).click();
  await expect(page.getByTitle("Account", { exact: true })).toBeVisible();
  return { context, page };
}
async function download(page, region, path) {
  const pending = page.waitForEvent("download");
  await region
    .getByRole("link", { name: "Save catalog-draft.json", exact: true })
    .click();
  await (await pending).saveAs(path);
  await chmod(path, 0o600);
  return JSON.parse(await readFile(path, "utf8"));
}
async function exactFile(side, revisionId, listingRevisionId, output) {
  await cli(
    side,
    [
      "export",
      "--format",
      "draft",
      "--version-revision-id",
      revisionId,
      ...(listingRevisionId
        ? ["--listing-revision-id", listingRevisionId]
        : []),
      "--output-dir",
      output,
    ],
    "local-reader",
  );
  return JSON.parse(await readFile(join(output, "draft.json"), "utf8"));
}
try {
  input = JSON.parse(
    await readFile(join(directory, "draft-transfer.json"), "utf8"),
  );
  assert.deepEqual(
    input.items.map((item) => item.kind),
    kinds,
  );
  for (const origin of [input.a, input.b])
    assert.match(origin, /^http:\/\/127\.0\.0\.1:\d+$/);
  assert.notEqual(input.a, input.b);
  const origins = { a: await host("a"), b: await host("b") };
  browser = await chromium.launch({ headless: true });
  stage = "destination";
  const namespace = (
    await api("a", "POST", "/api/v1/namespaces", {
      slug: "draft-transfers",
      displayName: "Draft transfers",
    })
  ).data;
  const publisher = (
    await api("a", "GET", `/api/v1/publishers/${input.publisherA}`)
  ).data;
  const approvedDomains = structuredClone(publisher.approvedPublicationDomains);
  assert.notEqual(publisher.id, input.publisherB);
  let publishTarget;
  for (const item of input.items) {
    stage = "source-download";
    const folder = join(directory, `transfer-${item.kind}`);
    await mkdir(folder, { mode: 0o700 });
    const source = await open(
      origins.b,
      "local-reader",
      `/catalog/${item.releaseId}`,
    );
    const section = source.page.getByRole("region", {
      name: "Download saved draft JSON",
      exact: true,
    });
    await section
      .getByLabel("Include Listing metadata", { exact: true })
      .check();
    await section
      .getByRole("button", { name: "Prepare draft download", exact: true })
      .click();
    const file = await download(
      source.page,
      section,
      join(folder, "from-ui.json"),
    );
    assert.equal(file.kind, item.kind);
    assert.equal(file.canonicalIdentifier, item.identifier);
    assert.ok(file.listing);
    for (const name of [
      "tenantId",
      "namespaceId",
      "resourceId",
      "publisherId",
      "release",
      "approval",
    ])
      assert.equal(
        Object.hasOwn(file, name) || Object.hasOwn(file.listing, name),
        false,
      );
    await source.context.close();
    const contentOnly = item.kind === "skill";
    const state = join(folder, "receipt", "state.json");
    stage = "cli-import";
    const applied = await cli("a", [
      "import",
      "apply",
      "--file",
      join(folder, "from-ui.json"),
      "--namespace-id",
      namespace.id,
      ...(contentOnly ? ["--content-only"] : ["--publisher-id", publisher.id]),
      "--state-file",
      state,
    ]);
    assert.equal(applied.disposition, "complete");
    const steps = applied.items[0].steps;
    assert.equal(steps.length, contentOnly ? 2 : 4);
    const resourceId = steps[0].ack.id;
    const revisionId = steps[1].ack.revisionId;
    stage = "retained-readback";
    const resource = (await api("a", "GET", `/api/v1/resources/${resourceId}`))
      .data;
    assert.equal(resource.namespaceId, namespace.id);
    assert.notEqual(resource.id, item.resourceId);
    assert.notEqual(resource.namespaceId, item.namespaceId);
    assert.equal(resource.canonicalIdentifier, item.identifier);
    if (contentOnly)
      assert.deepEqual(
        (await api("a", "GET", `/api/v1/resources/${resourceId}/listings`)).data
          .items,
        [],
      );
    let listing;
    let listingRevision;
    if (!contentOnly) {
      listing = await api(
        "a",
        "GET",
        `/api/v1/catalog-listings/${steps[2].ack.id}`,
      );
      listingRevision = (
        await api("a", "GET", `/api/v1/listing-revisions/${steps[3].ack.id}`)
      ).data;
      assert.equal(listing.data.resourceId, resourceId);
      assert.equal(listing.data.namespaceId, namespace.id);
      assert.equal(listingRevision.publisherId, publisher.id);
      assert.equal(listing.data.currentReleaseId, undefined);
      assert.deepEqual(
        (
          await api(
            "a",
            "GET",
            `/api/v1/review-submissions?listingId=${listing.data.id}`,
          )
        ).data.items,
        [],
      );
      assert.deepEqual(
        (
          await api(
            "a",
            "GET",
            `/api/v1/catalog-listings/${listing.data.id}/releases`,
          )
        ).data.items,
        [],
      );
      if (item.kind === "agent") publishTarget = { listing, listingRevision };
    }
    stage = "cli-export";
    const exported = await exactFile(
      "a",
      revisionId,
      listingRevision?.id,
      join(folder, "cli-export"),
    );
    assert.deepEqual(exported.representations, file.representations);
    if (!contentOnly) assert.deepEqual(exported.listing, file.listing);
    else assert.equal(exported.listing, undefined);
    stage = "destination-import";
    const destination = await open(
      origins.b,
      "local-contributor",
      `/versions/new?resource=${item.resourceId}`,
    );
    const page = destination.page;
    const previousListing = (
      await api("b", "GET", `/api/v1/catalog-listings/${item.listingId}`)
    ).data;
    await page
      .getByLabel("Choose draft JSON file", { exact: true })
      .setInputFiles(join(folder, "cli-export", "draft.json"));
    if (contentOnly)
      await page
        .getByRole("combobox", { name: "Listing metadata to use", exact: true })
        .selectOption("none");
    await page
      .getByRole("button", { name: "Use this draft", exact: true })
      .click();
    await page
      .getByRole("textbox", { name: "Version label", exact: true })
      .fill("draft-transfer");
    if (!contentOnly) {
      await page
        .getByRole("button", { name: "Next: Listing details", exact: true })
        .click();
      const choice = page.getByRole("combobox", {
        name: "Publisher",
        exact: true,
      });
      await expect(choice).toHaveValue("");
      await choice.selectOption(input.publisherB);
    }
    const versionReply = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        new URL(response.url()).pathname.endsWith(
          `/resources/${item.resourceId}/versions`,
        ),
    );
    const listingReply = !contentOnly
      ? page.waitForResponse(
          (response) =>
            response.request().method() === "POST" &&
            /\/catalog-listings\/[^/]+\/revisions$/.test(
              new URL(response.url()).pathname,
            ),
        )
      : undefined;
    await page
      .getByRole("button", { name: "Create version", exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    const saved = await (await versionReply).json();
    const metadata = listingReply
      ? await (await listingReply).json()
      : undefined;
    await expect(
      page.getByRole("button", { name: "Create another version", exact: true }),
    ).toBeVisible();
    stage = "retained-readback";
    const roundTrip = await exactFile(
      "b",
      saved.revision.id,
      metadata?.revision.id,
      join(folder, "final-export"),
    );
    assert.equal(roundTrip.version, "draft-transfer");
    assert.deepEqual(roundTrip.representations, exported.representations);
    assert.deepEqual(roundTrip.listing, exported.listing);
    if (metadata) {
      assert.equal(metadata.listing.id, item.listingId);
      assert.equal(metadata.revision.publisherId, input.publisherB);
      assert.equal(metadata.listing.currentReleaseId, item.releaseId);
    } else
      assert.deepEqual(
        (await api("b", "GET", `/api/v1/catalog-listings/${item.listingId}`))
          .data,
        previousListing,
      );
    await destination.context.close();
  }
  stage = "publication";
  // The destination Publisher was approved independently during seed. Import cannot change that policy.
  assert.deepEqual(
    (await api("a", "GET", `/api/v1/publishers/${publisher.id}`)).data
      .approvedPublicationDomains,
    approvedDomains,
  );
  const listingPath = `/api/v1/catalog-listings/${publishTarget.listing.data.id}`;
  const submission = await api(
    "a",
    "POST",
    `${listingPath}/submissions`,
    {},
    "local-contributor",
  );
  await api(
    "a",
    "POST",
    `/api/v1/review-submissions/${submission.data.id}:approve`,
    { reason: "Independent transfer review" },
    "local-reviewer",
    submission.etag,
  );
  const published = await api(
    "a",
    "POST",
    `${listingPath}:publish`,
    { submissionId: submission.data.id },
    "local-publisher",
    publishTarget.listing.etag,
  );
  const record = (
    await api(
      "a",
      "GET",
      `/api/v1/catalog/resources/${published.data.release.id}`,
      undefined,
      "local-reader",
    )
  ).data;
  assert.equal(record.resource.namespaceId, namespace.id);
  assert.equal(record.listingRevision.publisherId, publisher.id);
  assert.equal(
    record.versionRevision.id,
    publishTarget.listingRevision.versionRevisionId,
  );
  for (const ledger of ledgers) assert.deepEqual(ledger.results(), []);
  assert.deepEqual(errors, []);
  assert.equal(forbiddenRequests, 0);
} catch (error) {
  const line = Number(
    /draft-transfer-journeys\.mjs:(\d+):\d+/.exec(error.stack ?? "")?.[1],
  );
  process.stdout.write(
    JSON.stringify({
      stage,
      ...(Number.isInteger(line) && line > 0 && line < 10000 ? { line } : {}),
    }),
  );
  process.exitCode = 1;
} finally {
  try {
    await browser?.close();
  } finally {
    const stopped = await Promise.allSettled(hosts.map(stopProcess));
    process.off("SIGINT", stop);
    process.off("SIGTERM", stop);
    assert.ok(
      stopped.every((result) => result.status === "fulfilled"),
      "Owned transfer UI cleanup failed",
    );
  }
}
if (!process.exitCode)
  process.stdout.write(
    JSON.stringify({
      kinds,
      directions: ["ui-to-cli", "cli-to-ui"],
      newResources: 4,
      newVersions: 4,
      contentOnly: 2,
      independentlyPublished: 1,
      resourceInvocations: 0,
      cleanup: "passed",
    }),
  );
