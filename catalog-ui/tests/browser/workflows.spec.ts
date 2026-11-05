import { openDisclosure } from "./editor-disclosure";
import { object, text } from "./json";
import { required } from "../required";
import { test, expect, login, confirmCommand, signOut } from "./fixtures";
import { authorizationCommandOutcome } from "../../scripts/authorization-evidence.mjs";

test("catalog records link to the exact governed workflows under a subpath", async ({
  page,
  catalog,
  network,
}) => {
  const item = catalog.receipt.items.find(
    (item) => item.key === "order-agent",
  )!;
  let versionReads = 0;
  page.on("request", (request) => {
    if (
      request.method() === "GET" &&
      new URL(request.url()).pathname.endsWith(
        `/api/v1/resources/${item.resourceId}/versions`,
      )
    )
      versionReads++;
  });
  await login(page, catalog, "local-reader", `/catalog/${item.releaseId}`);
  const recordURL = page.url();
  for (const [label, path] of [
    ["Resource and versions", `/resources/${item.resourceId}`],
    ["Listing and revisions", `/listings/${item.listingId}`],
    ["Approved review", `/reviews/${item.submissionId}`],
    ["Release details", `/releases/${item.releaseId}`],
  ] as const) {
    await page
      .getByRole("navigation", { name: "Record workflows" })
      .getByRole("link", { name: label, exact: true })
      .click();
    await expect(page).toHaveURL(catalog.origin + catalog.basePath + path);
    await expect(page.getByRole("heading").first()).toBeVisible();
    await expect(page.getByRole("alert")).toHaveCount(0);
    if (label === "Listing and revisions") {
      await expect(
        page.getByRole("button", { name: "New listing revision", exact: true }),
      ).toBeDisabled();
      await expect(
        page.getByText(
          "The resource.edit permission is required in this namespace.",
          { exact: true },
        ),
      ).toBeVisible();
      const current = await catalog.admin(
        "GET",
        `/api/v1/catalog-listings/${item.listingId}`,
      );
      const path = `${catalog.basePath}/_catalog/api/v1/catalog-listings/${item.listingId}/revisions`;
      network.expectFailure(
        "POST",
        `/_catalog/api/v1/catalog-listings/${item.listingId}/revisions`,
        403,
      );
      const denied = await page.evaluate(
        async ({ path, etag, item, publisherId }) => {
          const response = await fetch(path, {
            method: "POST",
            headers: {
              "Content-Type": "application/json",
              "X-Catalog-Request": "1",
              "Idempotency-Key": "reader-newcomer-denial",
              "If-Match": etag!,
            },
            body: JSON.stringify({
              versionRevisionId: item.versionRevisionId,
              displayName: "Reader denial control",
              description: "A valid revision requires author permission.",
              publisherId,
              visibility: "tenant",
            }),
          });
          return {
            status: response.status,
            body: (await response.json()) as { code: string },
          };
        },
        {
          path,
          etag: current.etag,
          item,
          publisherId: catalog.receipt.publisherId,
        },
      );
      expect(denied.status).toBe(403);
      expect(denied.body.code).toBe("FORBIDDEN");
      expect(
        (
          await catalog.admin(
            "GET",
            `/api/v1/catalog-listings/${item.listingId}`,
          )
        ).etag,
      ).toBe(current.etag);
    }
    if (label === "Resource and versions") {
      await page.setViewportSize({ width: 390, height: 844 });
      await expect
        .poll(() => page.evaluate(() => document.documentElement.scrollWidth))
        .toBeLessThanOrEqual(390);
      const versions = page.getByRole("region", {
        name: "Versions",
        exact: true,
      });
      await expect(
        versions.getByRole("link", { name: "Version 1.0.0", exact: true }),
      ).toBeVisible();
      // Re-selecting the active view is an explicit reload, not a silent no-op.
      const before = versionReads;
      await page.getByRole("button", { name: "Versions", exact: true }).click();
      await expect.poll(() => versionReads).toBe(before + 1);
      await expect(versions).toBeFocused();
      await page.getByRole("button", { name: "Listings", exact: true }).click();
      await expect(
        page.getByRole("region", { name: "Listings", exact: true }),
      ).toBeFocused();
      await page.getByRole("button", { name: "Versions", exact: true }).click();
      await expect(versions).toBeFocused();
      await versions
        .getByRole("link", { name: "Version 1.0.0", exact: true })
        .click();
      await expect(
        page.getByRole("heading", { name: "Version 1.0.0", exact: true }),
      ).toBeVisible();
      await expect(
        page.getByRole("button", { name: "New revision", exact: true }),
      ).toHaveCount(0);
      await page
        .getByRole("link", { name: "Inspect current revision and validation" })
        .click();
      await expect(page).toHaveURL(
        `${catalog.origin}${catalog.basePath}/version-revisions/${item.versionRevisionId}`,
      );
      await expect(page.getByRole("alert")).toHaveCount(0);
      await page.setViewportSize({ width: 1440, height: 900 });
    }
    await page.goto(recordURL);
  }
});

test("administrator grants and revokes a role and completes membership lifecycle", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-admin", "/members");
  await expect(
    page.getByRole("columnheader").filter({
      has: page.getByRole("button", {
        name: "Sort by realm, ascending",
        exact: true,
      }),
    }),
  ).toBeVisible();
  await expect(
    page.getByText(/Realm identifies the identity-provider boundary/),
  ).toBeVisible();
  await openDisclosure(page, "Add Catalog member");
  const grant = page.locator("section.workspace-panel").filter({
    has: page.getByRole("heading", { name: "Add Catalog member", exact: true }),
  });
  // Service selection supplements the full human membership lifecycle below.
  await grant
    .getByRole("textbox", { name: "Subject", exact: true })
    .fill("browser-service");
  await grant
    .getByRole("combobox", { name: "Identity type", exact: true })
    .selectOption("service");
  await grant
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Browser service identity regression");
  await grant
    .getByRole("combobox", { name: "Assignment scope", exact: true })
    .selectOption("tenant");
  await grant
    .getByRole("button", { name: "Add Catalog member", exact: true })
    .click();
  await confirmCommand(page);
  await page.getByRole("link", { name: "Open updated membership" }).click();
  await expect(
    page.getByRole("heading", { name: "browser-service", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("service: Active", { exact: true }),
  ).toBeVisible();
  await page.goto(`${catalog.origin}${catalog.basePath}/members`);
  await openDisclosure(page, "Add Catalog member");
  await expect(
    grant.getByRole("textbox", { name: "Subject", exact: true }),
  ).toHaveAccessibleDescription(/exact login identifier/);
  const subjectHelp = grant.getByRole("button", {
    name: "About subject",
    exact: true,
  });
  await subjectHelp.focus();
  await page.keyboard.press("Enter");
  await expect(subjectHelp).toHaveAttribute("aria-expanded", "true");
  await expect(
    grant.getByRole("textbox", { name: "Subject", exact: true }),
  ).toHaveAccessibleDescription(/do not paste a token/);
  await page.keyboard.press("Space");
  await expect(subjectHelp).toHaveAttribute("aria-expanded", "false");
  await expect(
    grant.getByRole("textbox", { name: "Subject", exact: true }),
  ).toHaveAccessibleDescription(/exact login identifier/);
  await expect(
    grant.getByRole("textbox", { name: "Subject", exact: true }),
  ).not.toHaveAccessibleDescription(/do not paste a token/);
  await expect(
    grant.getByRole("combobox", { name: "Catalog role", exact: true }),
  ).toHaveAccessibleDescription(
    /Identity type independently selects Human or Service/,
  );
  await grant
    .getByRole("combobox", { name: "Catalog role", exact: true })
    .selectOption("source_admin");
  await grant
    .getByRole("combobox", { name: "Assignment scope", exact: true })
    .selectOption("namespace");
  await expect(
    grant.getByRole("combobox", { name: "Catalog role", exact: true }),
  ).toHaveValue("source_admin");
  await expect(
    grant.getByRole("button", { name: "Add Catalog member", exact: true }),
  ).toBeDisabled();
  await grant
    .getByRole("combobox", { name: "Assignment scope", exact: true })
    .selectOption("tenant");
  await grant
    .getByRole("textbox", { name: "Subject", exact: true })
    .fill("browser-member");
  await grant
    .getByRole("combobox", { name: "Catalog role", exact: true })
    .selectOption("reader");
  await grant
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Browser administration regression");
  await grant
    .getByRole("button", { name: "Add Catalog member", exact: true })
    .click();
  await confirmCommand(page);
  await page.getByRole("link", { name: "Open updated membership" }).click();
  await expect(
    page.getByRole("heading", { name: "browser-member", exact: true }),
  ).toBeVisible();
  const directAssignments = page.locator("section.workspace-panel").filter({
    has: page.getByRole("heading", {
      name: "Direct role assignments",
      exact: true,
    }),
  });
  await expect(directAssignments).toContainText("reader");
  await expect(directAssignments).toContainText("Tenant-wide");
  await expect(directAssignments).toContainText("Active");
  const assignmentOpener = directAssignments.getByRole("button", {
    name: /^Revoke assignment /,
  });
  const revokeTitle = (await assignmentOpener.innerText()).trim();
  for (const [action, state] of [
    ["suspend", "suspended"],
    ["reactivate", "active"],
  ] as const) {
    await openDisclosure(page, "Change membership state");
    const lifecycle = page.locator("section.workspace-panel").filter({
      has: page.getByRole("heading", {
        name: "Change membership state",
        exact: true,
      }),
    });
    await lifecycle
      .getByRole("combobox", { name: "Membership action" })
      .selectOption(action);
    await lifecycle
      .getByRole("textbox", { name: "Reason", exact: true })
      .fill("Browser membership lifecycle");
    await lifecycle
      .getByRole("button", { name: "Change membership state", exact: true })
      .click();
    await confirmCommand(page);
    await expect(lifecycle.getByRole("status")).toContainText(
      `Membership ${state}.`,
    );
    await page.reload();
    await expect(
      page.getByText(`human: ${state[0]!.toUpperCase()}${state.slice(1)}`, {
        exact: true,
      }),
    ).toBeVisible();
  }
  await page.getByRole("button", { name: revokeTitle, exact: true }).click();
  await expect(
    page.getByRole("region", {
      name: "Selected role revocation editor",
      exact: true,
    }),
  ).toBeFocused();
  const revoke = page
    .locator("section.workspace-panel")
    .filter({
      has: page.getByRole("heading", { name: revokeTitle, exact: true }),
    })
    .last();
  await revoke
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Remove test grant");
  await revoke.getByRole("button", { name: revokeTitle, exact: true }).click();
  const revokePath = `/api/v1/access/assignments/${revokeTitle.slice("Revoke assignment ".length)}:revoke`;
  const written = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname ===
        `${catalog.basePath}/_catalog${revokePath}`,
  );
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  const response = await written;
  if (response.status() >= 400)
    console.log(
      "Owned authorization refusal: " +
        JSON.stringify(
          authorizationCommandOutcome({
            method: "POST",
            path: revokePath,
            status: response.status(),
            problem: await response.json().catch(() => undefined),
          }),
        ),
    );
  expect(response.status()).toBe(200);
  await expect(page.getByRole("dialog")).not.toBeVisible();
  await expect(revoke.getByRole("status")).toContainText("Role revoked.");
  await page.reload();
  await expect(directAssignments).toContainText("reader");
  await expect(directAssignments).toContainText("Tenant-wide");
  await expect(directAssignments).toContainText("Revoked");
  await openDisclosure(page, "Change membership state");
  const lifecycle = page.locator("section.workspace-panel").filter({
    has: page.getByRole("heading", {
      name: "Change membership state",
      exact: true,
    }),
  });
  await lifecycle
    .getByRole("combobox", { name: "Membership action" })
    .selectOption("offboard");
  await lifecycle
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Complete test membership lifecycle");
  await lifecycle
    .getByRole("button", { name: "Change membership state", exact: true })
    .click();
  await confirmCommand(page);
  await expect(lifecycle.getByRole("status")).toContainText(
    "Membership offboarded.",
  );
  await expect(
    page.getByRole("button", { name: "Grant a built-in role", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("combobox", { name: "Membership action", exact: true }),
  ).toBeDisabled();
  await page.reload();
  await expect(
    page.getByText("human: Offboarded", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Grant a built-in role", exact: true }),
  ).toHaveCount(0);
});

test("independent approval publishes a listing and release retirement removes the dead catalog link", async ({
  page,
  catalog,
}) => {
  const item = catalog.receipt.items.find(
    (item) => item.key === "pending-mcp",
  )!;
  await login(
    page,
    catalog,
    "local-template-reviewer",
    `/reviews/${item.submissionId}`,
  );
  await page
    .getByRole("button", { name: "Inspect submitted evidence" })
    .click();
  await page
    .getByRole("combobox", { name: "Decision", exact: true })
    .selectOption("approve");
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Independent browser review");
  await page.getByRole("button", { name: "Record review decision" }).click();
  await confirmCommand(page);
  await expect(page.getByRole("status")).toContainText("Review approved.");
  await signOut(page);
  await login(
    page,
    catalog,
    "local-template-publisher",
    `/reviews/${item.submissionId}`,
  );
  await page
    .getByRole("button", { name: "Publish approved listing", exact: true })
    .click();
  await confirmCommand(page);
  await page.getByRole("link", { name: "Open published release" }).click();
  const releaseURL = page.url();
  await page.getByRole("link", { name: "Catalog record", exact: true }).click();
  await expect(
    page.getByRole("heading", {
      name: "MCP Server awaiting review",
      exact: true,
    }),
  ).toBeVisible();
  await page
    .getByRole("link", { name: "Release details", exact: true })
    .click();
  await expect(page).toHaveURL(releaseURL);
  for (const [action, state] of [
    ["deprecate", "deprecated"],
    ["retire", "retired"],
    ["revoke", "revoked"],
  ] as const) {
    await page
      .getByRole("combobox", { name: "Disposition", exact: true })
      .selectOption(action);
    await page
      .getByRole("textbox", { name: "Reason", exact: true })
      .fill("Browser release lifecycle");
    if (action === "deprecate") {
      await page
        .getByRole("textbox", { name: "Replacement release ID", exact: true })
        .fill(
          catalog.receipt.items.find((entry) => entry.key === "order-mcp")!
            .releaseId!,
        );
      await page
        .getByLabel("Sunset time (optional, local time)", { exact: true })
        .fill("2099-01-02T03:04");
    }
    const changed = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().endsWith(`:${action}`),
    );
    const dispositionAction = page.getByRole("button", {
      name: "Change release disposition",
      exact: true,
    });
    await expect(dispositionAction).toHaveClass(
      action === "deprecate" ? "warning" : "danger",
    );
    await expect(
      dispositionAction.locator('svg[aria-hidden="true"]'),
    ).toHaveCount(1);
    await dispositionAction.click();
    await expect(
      page
        .getByRole("dialog")
        .getByRole("button", { name: "Confirm", exact: true }),
    ).toHaveClass(action === "deprecate" ? "warning" : "danger");
    await expect(page.getByRole("dialog")).toContainText(
      action === "deprecate"
        ? "do not change the listing's current release or schedule its withdrawal"
        : "each entire active discovery snapshot that contains it",
    );
    const summary = page
      .getByRole("dialog")
      .getByLabel("Command summary", { exact: true });
    await expect(summary).toContainText(
      {
        deprecate: "Deprecate release",
        retire: "Retire release",
        supersede: "Supersede release",
        revoke: "Revoke release",
      }[action],
    );
    await expect(summary).toContainText(
      new URL(releaseURL).pathname.split("/").at(-1)!,
    );
    await expect(summary).toContainText("Browser release lifecycle");
    await confirmCommand(page);
    await expect(page.getByRole("status")).toContainText(`Release ${state}.`);
    const change = await changed;
    expect(change.ok()).toBe(true);
    const saved = object(await change.json());
    if (action === "deprecate") {
      const utc = await page.evaluate(() =>
        new Date("2099-01-02T03:04").toISOString(),
      );
      expect(object(change.request().postDataJSON()).sunsetAt).toBe(utc);
      expect(new Date(text(saved.sunsetAt)).toISOString()).toBe(utc);
      const replacement = catalog.receipt.items.find(
        (entry) => entry.key === "order-mcp",
      )!.releaseId;
      expect(object(change.request().postDataJSON()).replacementId).toBe(
        replacement,
      );
      expect(saved.replacementReleaseId).toBe(replacement);
    }
    await expect(
      page.getByText(/displayed release is last-loaded/),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Reload release state", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: `Release ${state}`, exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Catalog record", exact: true }),
    ).toHaveCount(state === "deprecated" ? 1 : 0);
    if (state === "retired" || state === "revoked") {
      const hint = page.getByText(
        /Retired and superseded releases can only be revoked\./,
      );
      const button = page.getByRole("button", {
        name: "Change release disposition",
        exact: true,
      });
      await expect(hint).toBeVisible();
      await expect(hint).toContainText("Revoked releases cannot be changed.");
      await expect(button).toBeDisabled();
      const disposition = page.getByRole("combobox", {
        name: "Disposition",
        exact: true,
      });
      if (state === "retired") {
        await disposition.selectOption("revoke");
        await expect(button).toBeEnabled();
        await expect(hint).toBeHidden();
      } else {
        await expect(button).toBeDisabled();
        await expect(disposition).toBeDisabled();
      }
    }
    const facts = page.locator(".workspace-kv");
    for (const [label, field] of [
      ["Activated", "activatedAt"],
      ["Disposition last updated", "updatedAt"],
    ] as const)
      await expect(
        facts.locator("dt", { hasText: label }).locator("+ dd time"),
      ).toHaveAttribute("datetime", text(saved[field]));
    if (saved.sunsetAt !== undefined)
      await expect(
        facts
          .locator("dt", { hasText: "Declared sunset" })
          .locator("+ dd time"),
      ).toHaveAttribute("datetime", text(saved.sunsetAt));
    await expect(
      facts.locator("dt", { hasText: "Disposition reason" }).locator("+ dd"),
    ).toHaveText("Browser release lifecycle");
  }
});

test("publisher builds activates rolls back and unpublishes host snapshots", async ({
  page,
  catalog,
}) => {
  await login(page, catalog, "local-publisher", "/publication");
  const choices = page.getByRole("combobox", {
    name: "Configured publication hosts",
    exact: true,
  });
  await expect(choices).toHaveValue("");
  await expect(
    choices.getByRole("option", { name: "127.0.0.1 — anonymous", exact: true }),
  ).toBeEnabled();
  await expect(
    page.getByRole("button", { name: "Build candidate snapshot", exact: true }),
  ).toHaveCount(0);
  await choices.selectOption("127.0.0.1");
  await expect(
    page.getByLabel("Configured discovery host", { exact: true }),
  ).toHaveValue("127.0.0.1");
  await expect(
    page.getByRole("button", { name: "Build candidate snapshot", exact: true }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "Open host", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Build candidate snapshot", exact: true }),
  ).toBeEnabled();
  const otherCandidate = await catalog.admin<{
    id: string;
    generation: number;
  }>("POST", "/api/v1/publication/snapshots", { host: "127.0.0.1" });
  await expect(
    page.getByRole("link", {
      name: `Generation ${otherCandidate.data.generation}`,
      exact: true,
    }),
  ).toHaveCount(0);
  await page
    .getByRole("button", { name: "Refresh snapshots", exact: true })
    .click();
  await expect(
    page.getByRole("link", {
      name: `Generation ${otherCandidate.data.generation}`,
      exact: true,
    }),
  ).toHaveAttribute(
    "href",
    new RegExp(`/publication/${otherCandidate.data.id}\\?host=`),
  );
  const hostURL = catalog.origin + "/publication?host=127.0.0.1";
  const snapshots: string[] = [];
  for (let index = 0; index < 2; index++) {
    await page
      .getByRole("button", { name: "Build candidate snapshot", exact: true })
      .click();
    await expect(
      page.getByRole("dialog").getByLabel("Command summary", { exact: true }),
    ).toContainText("127.0.0.1");
    await confirmCommand(page);
    await page
      .getByRole("link", { name: "Inspect candidate", exact: true })
      .click();
    snapshots.push(page.url());
    await expect(page.getByText(/This candidate is not served/)).toBeVisible();
    await page
      .getByRole("textbox", { name: "Reason", exact: true })
      .fill("Browser snapshot activation");
    await page
      .getByRole("button", { name: "Activate candidate", exact: true })
      .click();
    await expect(
      page.getByRole("dialog").getByLabel("Command summary", { exact: true }),
    ).toContainText("Activate snapshot");
    await expect(
      page.getByRole("dialog").getByLabel("Command summary", { exact: true }),
    ).toContainText("Browser snapshot activation");
    await confirmCommand(page);
    await expect(page.getByRole("status")).toContainText(
      "Discovery snapshot activated.",
    );
    await page
      .getByRole("button", { name: "Reload snapshot state", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: /Generation .*: active/ }),
    ).toBeVisible();
    await expect(
      page.getByText(/Later listing releases are not added automatically/),
    ).toBeVisible();
    await page.goto(hostURL);
  }
  expect(snapshots[0]).not.toBe(snapshots[1]);
  await page.goto(required(snapshots[0]));
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Browser rollback");
  await page
    .getByRole("button", { name: "Restore retained snapshot", exact: true })
    .click();
  const rolledBack = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname.endsWith(":rollback"),
  );
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Confirm", exact: true })
    .click();
  const rollbackReply = await rolledBack;
  // Report only fixed categories from this owned fixture, never raw evidence.
  let rejection = "unclassified";
  if (!rollbackReply.ok()) {
    const detail = String(object(await rollbackReply.json()).detail || "");
    rejection =
      [
        "activation evidence",
        "digest metadata",
        "no longer eligible",
        "eligible release set",
        "profiles",
        "host or audience",
        "not a conforming",
        "representations are inconsistent",
      ].find((category) => detail.includes(category)) || rejection;
  }
  expect(rollbackReply.status(), `Rollback rejection: ${rejection}`).toBe(200);
  await expect(page.getByRole("dialog")).not.toBeVisible();
  await expect(page.getByRole("status")).toContainText(
    "Discovery snapshot activated.",
  );
  await page.reload();
  await expect(
    page.getByRole("heading", { name: /Generation .*: active/ }),
  ).toBeVisible();
  await page.goto(hostURL);
  await page.getByText("Withdraw public discovery", { exact: true }).click();
  await page
    .getByRole("textbox", { name: "Reason", exact: true })
    .fill("Browser withdrawal");
  const unpublish = page.getByRole("button", {
    name: "Unpublish host",
    exact: true,
  });
  await expect(unpublish).toHaveClass("danger");
  await unpublish.click();
  await expect(
    page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true }),
  ).toHaveClass("danger");
  await confirmCommand(page);
  await expect(page.getByRole("status")).toContainText("Host unpublished.");
  await page
    .getByRole("button", { name: "Refresh snapshots", exact: true })
    .click();
  await expect(
    page.getByRole("link", { name: "Generation 1", exact: true }),
  ).toBeVisible();
  await page.goto(required(snapshots[0]));
  await expect(
    page.getByRole("heading", { name: /Generation .*: retired/ }),
  ).toBeVisible();
});

test("subpath: malformed host choices recover without hiding an explicitly opened publication host", async ({
  page,
  catalog,
}) => {
  await page.route(
    "**/_catalog/api/v1/publication/hosts",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        json: { items: [{ host: "127.0.0.1", mode: "unexpected" }] },
      });
    },
    { times: 1 },
  );
  let snapshotReads = 0;
  let publicationWrites = 0;
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (path.endsWith("/publication/snapshots") && request.method() === "GET")
      snapshotReads++;
    if (path.includes("/publication") && request.method() === "POST")
      publicationWrites++;
  });
  await login(page, catalog, "local-publisher", "/publication");
  await page.setViewportSize({ width: 390, height: 844 });
  const inventory = page.getByRole("region", {
    name: "Publication host choices",
    exact: true,
  });
  await expect(inventory.getByRole("alert")).toBeVisible();
  await expect(
    page.getByRole("combobox", { name: "Configured publication hosts" }),
  ).toHaveCount(0);
  expect(snapshotReads).toBe(0);
  expect(publicationWrites).toBe(0);
  await page
    .getByLabel("Configured discovery host", { exact: true })
    .fill("127.0.0.1");
  await page.getByRole("button", { name: "Open host", exact: true }).click();
  await expect(
    page.getByRole("link", { name: "Generation 1", exact: true }),
  ).toBeVisible();
  expect(snapshotReads).toBe(1);
  await expect(inventory).toBeHidden();
  await openDisclosure(page, "Change host");
  await inventory.getByRole("button", { name: "Reload", exact: true }).click();
  const choices = page.getByRole("combobox", {
    name: "Configured publication hosts",
    exact: true,
  });
  await expect(choices).toHaveValue("127.0.0.1");
  await expect(
    page.getByRole("link", { name: "Generation 1", exact: true }),
  ).toBeVisible();
  expect(snapshotReads).toBe(1);
  expect(publicationWrites).toBe(0);
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
});

test("source settings keep disclosure visible and checkbox sizing bounded at desktop and narrow widths", async ({
  page,
  catalog,
}) => {
  const inspectionId = "00000000-0000-7000-8000-000000000124";
  const sourceCommands: string[] = [];
  const sourceReads: string[] = [];
  const peerRequests: string[] = [];
  page.on("request", (request) => {
    if (request.url().startsWith("https://peer.invalid"))
      peerRequests.push(request.url());
    if (request.url().includes("/_catalog/api/v1/federation/"))
      (request.method() === "GET" ? sourceReads : sourceCommands).push(
        request.url(),
      );
  });
  await page.route(
    `**/_catalog/api/v1/federation/source-inspections/${inspectionId}`,
    (route) =>
      route.fulfill({
        json: {
          id: inspectionId,
          resolvedUrl: "https://peer.invalid/ard/",
          profileId: "ard",
          entryCount: 0,
          warnings: [],
          expiresAt: new Date(Date.now() + 3600000).toISOString(),
        },
      }),
  );
  await login(page, catalog, "local-admin", `/inspections/${inspectionId}`);
  const policy = page.getByRole("textbox", {
    name: "ANS verification policy",
    exact: true,
  });
  await expect(policy).toHaveAccessibleDescription(
    /policy name supplied by your operator/,
  );
  for (const value of ["operator-ans", "a".repeat(63), ""]) {
    await policy.fill(value);
    expect(
      await policy.evaluate(
        (element: HTMLInputElement) => element.validity.valid,
      ),
    ).toBe(true);
  }
  for (const value of ["Bad", "bad_", "bad-"]) {
    await policy.fill(value);
    expect(
      await policy.evaluate(
        (element: HTMLInputElement) => element.validity.valid,
      ),
    ).toBe(false);
  }
  await policy.clear();
  const readsBeforeHelp = sourceReads.length;
  const scopeHelp = page.locator("details").filter({
    has: page.getByText("How scope filters match", { exact: true }),
  });
  const scopeSummary = scopeHelp.locator("summary");
  const inclusion = page.getByText(
    /Add at least one filter\. Entries must match every filter group\./,
  );
  await expect(scopeHelp).not.toHaveAttribute("open");
  await expect(inclusion).toBeVisible();
  await expect(inclusion).toContainText(
    "Excluded identifiers are always omitted.",
  );
  await scopeSummary.focus();
  await page.keyboard.press("Enter");
  await expect(scopeHelp).toHaveAttribute("open", "");
  await expect(scopeHelp).toContainText("All listed tags must match.");
  await expect(scopeHelp).toContainText(
    "Publishers are domains from AIR identifiers",
  );
  await page.keyboard.press("Enter");
  await expect(scopeHelp).not.toHaveAttribute("open");
  await page.setViewportSize({ width: 390, height: 900 });
  await scopeSummary.click();
  await expect(scopeHelp).toHaveAttribute("open", "");
  await expect(scopeHelp.getByText(/All listed tags must match/)).toBeVisible();
  await scopeSummary.click();
  await expect(scopeHelp).not.toHaveAttribute("open");
  await expect(inclusion).toBeVisible();
  expect(sourceReads).toHaveLength(readsBeforeHelp);
  const artwork = page.getByRole("checkbox", {
    name: "Display source artwork",
    exact: true,
  });
  const mode = page.getByRole("combobox", {
    name: "Referral traversal",
    exact: true,
  });
  const live = page.getByRole("checkbox", {
    name: "Enable live queries",
    exact: true,
  });
  const borders = new Set<string>();
  for (const theme of ["Light", "Dark"]) {
    for (const density of ["Comfortable", "Compact"]) {
      await page.getByLabel("Appearance", { exact: true }).click();
      await page.getByRole("radio", { name: theme, exact: true }).check();
      await page.getByRole("radio", { name: density, exact: true }).check();
      await page.keyboard.press("Escape");
      await expect(page.locator("html")).toHaveAttribute(
        "data-theme",
        theme.toLowerCase(),
      );
      await expect(page.locator("html")).toHaveAttribute(
        "data-density",
        density.toLowerCase(),
      );
      for (const width of [1440, 390]) {
        await page.setViewportSize({ width, height: 900 });
        const group = page.getByRole("group", {
          name: "Live query disclosure",
          exact: true,
        });
        const border = await group.evaluate((element) => ({
          padding: parseFloat(getComputedStyle(element).paddingTop),
          space: parseFloat(
            getComputedStyle(document.documentElement).getPropertyValue(
              "--space",
            ),
          ),
          style: getComputedStyle(element).borderTopStyle,
          color: getComputedStyle(element).borderTopColor,
          panelColor: getComputedStyle(element.closest(".workspace-panel")!)
            .borderTopColor,
        }));
        expect(border.padding).toBe(border.space);
        expect(border.style).toBe("solid");
        expect(border.color).toBe(border.panelColor);
        borders.add(border.color);
        await expect(artwork).toBeVisible();
        const label = artwork.locator("..");
        const textBounds = await label.locator("span").boundingBox();
        const checkboxBounds = await artwork.boundingBox();
        expect(
          Math.abs(
            textBounds!.y +
              textBounds!.height / 2 -
              checkboxBounds!.y -
              checkboxBounds!.height / 2,
          ),
        ).toBeLessThanOrEqual(2);
        expect(textBounds!.x).toBeGreaterThan(
          checkboxBounds!.x + checkboxBounds!.width,
        );
        const size = await artwork.boundingBox();
        expect(required(size)!.width).toBeGreaterThanOrEqual(12);
        expect(required(size)!.width).toBeLessThanOrEqual(28);
        expect(required(size)!.height).toBeLessThanOrEqual(28);
        await expect(artwork).toHaveAccessibleDescription(
          /credential-free requests/,
        );
        await expect(
          page.getByText(/Live requests send query text and filters/),
        ).toBeVisible();
        await expect(
          page.getByRole("checkbox", {
            name: /I acknowledge anonymous results/,
          }),
        ).toBeVisible();
        await expect(mode).toHaveAccessibleDescription(
          /Live queries use the setting above\. This choice controls whether a query may follow approved referrals\./,
        );
        const about = page.getByRole("button", {
          name: "About referral traversal",
          exact: true,
        });
        await about.focus();
        await page.keyboard.press("Enter");
        await expect(about).toHaveAttribute("aria-expanded", "true");
        await expect(mode).toHaveAccessibleDescription(
          /independent source and topology approval/,
        );
        await about.click();
        await expect(about).toHaveAttribute("aria-expanded", "false");
        await expect(mode).not.toHaveAccessibleDescription(
          /independent source and topology approval/,
        );
        await expect(live).not.toBeChecked();
        await expect(mode).toHaveValue("none");
        await artwork.focus();
        await page.keyboard.press("Space");
        await expect(artwork).toBeChecked();
        await page.keyboard.press("Space");
        await expect(artwork).not.toBeChecked();
      }
    }
  }
  expect(borders.size).toBe(2);
  expect(sourceCommands).toEqual([]);
  expect(peerRequests).toEqual([]);
});
