// Real custom grants qualify admission; controlled Source/Job replies qualify browser behavior only.
import { required } from "../required";
import { test, expect, login, signOut, confirmCommand } from "./fixtures";
import { doubleText } from "./text-layout";

for (const path of ["root", "subpath"]) {
  test(`${path}: Operations delegates retain honest actions and explicit current reads`, async ({
    page,
    catalog,
    network,
  }) => {
    const sourceId = "00000000-0000-7000-8000-000000000991";
    const memberships = await catalog.admin<{
      items: { id: string; subject: string }[];
    }>("GET", "/api/v1/memberships?limit=100");
    const membership = required(
      memberships.data.items.find((item) => item.subject === "local-no-role"),
    );
    const grant = async (name: string, permissions: string[]) => {
      const role = await catalog.admin<{ id: string }>(
        "POST",
        "/api/v1/access/roles",
        {
          name,
          permissions,
          reason: "Qualify independent Operations controls",
        },
      );
      await catalog.admin("POST", "/api/v1/access/assignments", {
        roleDefinitionId: role.data.id,
        membershipId: membership.id,
        reason: "Qualify owned Operations delegate",
      });
    };
    await grant("Read configured sources only", ["source.read"]);
    let writes = 0;
    page.on("request", (request) => {
      if (
        new URL(request.url()).pathname.includes("/_catalog/api/v1/") &&
        request.method() !== "GET"
      )
        writes++;
    });
    await login(page, catalog, "local-no-role", "/sources");
    await expect(page.getByText("No sources on this page.")).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Inspect a new source" }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("link", { name: "Search diagnostics" }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("link", { name: "Observations", exact: true }),
    ).toBeVisible();
    await page.goto(`${catalog.origin}${catalog.basePath}/sources/new`);
    // Establish the checked identity before asserting absent controls or changing its grants.
    await expect(page.getByLabel("Account", { exact: true })).toBeVisible();
    await expect(page.getByLabel("Source URL", { exact: true })).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "Inspect source", exact: true }),
    ).toHaveCount(0);
    expect(writes).toBe(0);

    await grant("Synchronize sources independently", [
      "source.read",
      "source.sync",
    ]);
    await signOut(page);
    const source = {
      id: sourceId,
      tenantId: "00000000-0000-7000-8000-000000000001",
      displayName: "Owned rendering Source",
      endpoint: "https://peer.invalid/ard.json",
      status: "active",
      profileId: "ard",
      retrievalMode: "indexed",
      updateMode: "manual_review",
      scope: {},
      live: {},
      nesting: {},
      referrals: {},
      revision: 1,
    };
    let state = source;
    let reads = 0;
    let failRead = false;
    let pauseCommands = 0;
    await page.route(
      `**/_catalog/api/v1/federation/sources/${sourceId}`,
      async (route) => {
        expect(route.request().method()).toBe("GET");
        reads++;
        await route.fulfill(
          failRead
            ? {
                status: 503,
                contentType: "application/problem+json",
                json: {
                  title: "Owned read unavailable",
                  status: 503,
                  code: "DEPENDENCY_UNAVAILABLE",
                },
              }
            : {
                status: 200,
                contentType: "application/json",
                headers: { ETag: `"rev-${state.revision}"` },
                json: state,
              },
        );
      },
    );
    await page.route(
      `**/_catalog/api/v1/federation/sources/${sourceId}:pause`,
      async (route) => {
        pauseCommands++;
        expect(route.request().postDataJSON()).toEqual({
          reason: "Pause owned metadata",
        });
        expect(route.request().headers()["if-match"]).toBe('"rev-1"');
        expect(route.request().headers()["idempotency-key"]).toBeTruthy();
        state = { ...source, status: "paused", revision: 2 };
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          json: state,
        });
      },
    );
    await login(page, catalog, "local-no-role", `/sources/${sourceId}`);
    const action = page.getByRole("combobox", {
      name: "Source action",
      exact: true,
    });
    await expect(action).toHaveValue("sync");
    await expect(
      page.getByRole("option", {
        name: "Test current policy and endpoint",
        exact: true,
      }),
    ).toHaveJSProperty("disabled", true);
    await action.focus();
    await page.keyboard.press("Home");
    await expect(action).toHaveValue("sync");
    await grant("Manage source lifecycle", [
      "source.read",
      "source.inspect",
      "source.manage",
    ]);
    await signOut(page);
    await login(page, catalog, "local-no-role", `/sources/${sourceId}`);
    await expect(action).toHaveValue("test");
    await action.selectOption("pause");
    await expect(
      page.getByRole("button", { name: "ANS registration", exact: true }),
    ).toHaveCount(0);
    await page
      .getByLabel("Reason", { exact: true })
      .fill("Pause owned metadata");
    await page
      .getByRole("button", { name: "Pause source", exact: true })
      .click();
    await confirmCommand(page);
    const receipt = page.getByRole("status").filter({
      hasText: "Source paused. Reload to inspect its current state.",
    });
    await expect(receipt).toBeVisible();
    await expect(receipt).toBeFocused();
    await expect(
      page.getByText("Configured state", { exact: true }).locator("+ dd"),
    ).toHaveText("Active");
    await expect(
      page.getByText(/not been reread since the command acknowledgement/),
    ).toBeVisible();
    failRead = true;
    network.expectFailure(
      "GET",
      `/_catalog/api/v1/federation/sources/${sourceId}`,
      503,
    );
    await page
      .getByRole("button", { name: "Reload source", exact: true })
      .click();
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(receipt).toBeVisible();
    await expect(
      page.getByText("Configured state", { exact: true }).locator("+ dd"),
    ).toHaveText("Active");
    failRead = false;
    await page.getByRole("button", { name: "Reload", exact: true }).click();
    await expect(
      page.getByText("Configured state", { exact: true }).locator("+ dd"),
    ).toHaveText("Paused");
    await expect(action).toHaveValue("resume");
    await expect(
      page.getByText(/not been reread since the command acknowledgement/),
    ).toHaveCount(0);
    await expect(receipt).toBeVisible();
    expect(reads).toBe(4);
    expect(pauseCommands).toBe(1);
    expect(writes).toBe(1);
    const reason = page.getByRole("textbox", { name: "Reason", exact: true });
    await reason.fill("Keep this unsent resume reason");
    const discard = page.waitForEvent("dialog").then(async (dialog) => {
      expect(dialog.type()).toBe("confirm");
      expect(dialog.message()).toContain("Discard unsaved input");
      await dialog.dismiss();
    });
    await Promise.all([
      discard,
      page.getByRole("button", { name: "Reload source", exact: true }).click(),
    ]);
    await expect(reason).toHaveValue("Keep this unsent resume reason");
    expect(reads).toBe(4);
    const replace = page.waitForEvent("dialog").then(async (dialog) => {
      expect(dialog.type()).toBe("confirm");
      expect(dialog.message()).toContain("Discard unsaved input");
      await dialog.accept();
    });
    await Promise.all([
      replace,
      page.getByRole("button", { name: "Reload source", exact: true }).click(),
    ]);
    await expect(reason).toHaveValue("");
    await expect(action).toHaveValue("resume");
    await expect(receipt).toBeVisible();
    await expect(receipt).not.toBeFocused();
    expect(reads).toBe(5);
    expect(pauseCommands).toBe(1);
    expect(writes).toBe(1);
    await page.setViewportSize({ width: 320, height: 900 });
    await doubleText(page);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth + 1,
      ),
    ).toBe(true);
    await grant("Read source Job metadata", ["job.read"]);
    await signOut(page);
    const me = await catalog.admin<{ tenantId: string }>("GET", "/api/v1/me");
    await page.route("**/_catalog/api/v1/jobs?**", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        json: {
          items: [
            {
              id: sourceId,
              tenantId: me.data.tenantId,
              kind: "source.sync",
              state: "queued",
              priority: 0,
              progress: 0,
              attempt: 0,
              attemptLimit: 3,
              cancellationRequested: false,
              notBefore: "2026-10-20T12:00:00Z",
              createdAt: "2026-10-20T12:00:00Z",
              updatedAt: "2026-10-20T12:00:00Z",
            },
          ],
        },
      });
    });
    await login(page, catalog, "local-no-role", "/jobs");
    await expect(
      page.getByRole("link", { name: "Source synchronization", exact: true }),
    ).toBeVisible();
    await doubleText(page);
    const table = page.getByRole("region", {
      name: "Job inventory",
      exact: true,
    });
    await expect(table).toHaveAttribute("tabindex", "0");
    await table.focus();
    await page.keyboard.press("ArrowRight");
    await expect
      .poll(() => table.evaluate((node) => node.scrollLeft))
      .toBeGreaterThan(0);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth + 1,
      ),
    ).toBe(true);
  });
}
