import { required } from "../required";
import { test, expect, login, signOut, confirmCommand } from "./fixtures";

for (const path of ["root", "subpath"]) {
  test(`${path}: scoped content delegates keep honest links, mutation fields and committed state`, async ({
    page,
    catalog,
    network,
  }) => {
    const item = required(
      catalog.receipt.items.find((entry) => entry.key === "order-agent"),
    );
    const memberships = await catalog.admin<{
      items: { id: string; subject: string }[];
    }>("GET", "/api/v1/memberships?limit=100");
    const member = required(
      memberships.data.items.find((entry) => entry.subject === "local-no-role"),
    );
    const grant = async (
      name: string,
      permissions: string[],
      namespaceId?: string,
    ) => {
      const role = await catalog.admin<{ id: string }>(
        "POST",
        "/api/v1/access/roles",
        { name, permissions, reason: "Qualify exact content workflow access" },
      );
      await catalog.admin("POST", "/api/v1/access/assignments", {
        roleDefinitionId: role.data.id,
        membershipId: member.id,
        ...(namespaceId ? { namespaceId } : {}),
        reason: "Qualify the selected content boundary",
      });
    };
    await grant(
      "Scoped metadata and history",
      ["resource.read", "resource.read_history"],
      catalog.receipt.namespaceId,
    );
    await grant("Snapshot inspection", ["publication.read"]);
    const other = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/namespaces",
      { slug: "other-content-scope", displayName: "Other content scope" },
    );
    await grant("Other Namespace only", ["namespace.read"], other.data.id);
    const snapshot = await catalog.admin<{ id: string; generation: number }>(
      "POST",
      "/api/v1/publication/snapshots",
      { host: "127.0.0.1" },
    );
    let namespaceReads = 0;
    let mutations = 0;
    let snapshotReads = 0;
    let releaseReads = 0;
    const snapshotReadPath = `/_catalog/api/v1/publication/snapshots/${snapshot.data.id}`;
    const activationPath = `${snapshotReadPath}:activate`;
    const releaseReadPath = `/_catalog/api/v1/listing-releases/${required(item.releaseId)}`;
    const activationRequests: {
      body: string | null;
      key: string | undefined;
      etag: string | undefined;
    }[] = [];
    page.on("request", (request) => {
      const pathname = new URL(request.url()).pathname;
      if (!pathname.includes("/_catalog/api/v1/")) return;
      if (request.method() !== "GET") mutations++;
      if (request.method() === "GET" && /\/namespaces(?:\/|$)/.test(pathname))
        namespaceReads++;
      if (request.method() === "GET" && pathname.endsWith(snapshotReadPath))
        snapshotReads++;
      if (request.method() === "GET" && pathname.endsWith(releaseReadPath))
        releaseReads++;
      if (request.method() === "POST" && pathname.endsWith(activationPath))
        activationRequests.push({
          body: request.postData(),
          key: request.headers()["idempotency-key"],
          etag: request.headers()["if-match"],
        });
    });
    await login(
      page,
      catalog,
      "local-no-role",
      `/resources/${item.resourceId}`,
    );
    await expect(
      page.getByRole("heading", { name: item.identifier, exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Namespace", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByText(catalog.receipt.namespaceId, { exact: true }).first(),
    ).toBeVisible();
    expect(namespaceReads).toBe(0);
    await page.goto(
      `${catalog.origin}${catalog.basePath}/releases/${required(item.releaseId)}`,
    );
    await expect(
      page.getByRole("combobox", { name: "Disposition", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("textbox", { name: "Reason", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "Reload release state", exact: true }),
    ).toBeEnabled();
    const snapshotURL = `${catalog.origin}${catalog.basePath}/publication/${snapshot.data.id}?host=127.0.0.1`;
    await page.goto(snapshotURL);
    await expect(
      page.getByRole("heading", {
        name: `Generation ${snapshot.data.generation}: candidate`,
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      page.getByRole("textbox", { name: "Reason", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "Activate candidate", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "Reload snapshot state", exact: true }),
    ).toBeEnabled();
    expect(mutations).toBe(0);

    await signOut(page);
    await grant(
      "Scoped release transitions",
      ["resource.read", "resource.read_history", "release.transition"],
      catalog.receipt.namespaceId,
    );
    await grant("Explicit snapshot activation", [
      "publication.read",
      "publication.activate",
    ]);
    await grant(
      "Selected Namespace",
      ["namespace.read"],
      catalog.receipt.namespaceId,
    );
    await login(
      page,
      catalog,
      "local-no-role",
      `/resources/${item.resourceId}`,
    );
    await expect(
      page.getByRole("link", { name: "Namespace", exact: true }),
    ).toHaveAttribute(
      "href",
      `${catalog.basePath}/namespaces/${catalog.receipt.namespaceId}`,
    );
    expect(namespaceReads).toBe(0);
    await page.goto(snapshotURL);
    await expect(
      page.getByRole("textbox", { name: "Reason", exact: true }),
    ).toBeEnabled();
    const readsBefore = snapshotReads;
    await page
      .getByRole("textbox", { name: "Reason", exact: true })
      .fill("Scoped snapshot activation");
    await page
      .getByRole("button", { name: "Activate candidate", exact: true })
      .click();
    // The real activation commits; only its usable browser response is lost.
    network.expectFailure("POST", activationPath, 503);
    await page.route(
      catalog.origin + catalog.basePath + activationPath,
      async (route) => {
        const response = await route.fetch({ maxRedirects: 0, maxRetries: 0 });
        expect(response.status()).toBe(200);
        await route.fulfill({
          status: 503,
          contentType: "application/problem+json",
          body: JSON.stringify({ code: "DEPENDENCY_UNAVAILABLE" }),
        });
      },
      { times: 1 },
    );
    const activation = page.getByRole("dialog");
    await activation
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    await expect(activation.getByRole("alert")).toContainText(
      "The outcome is unknown",
    );
    const active = await catalog.admin<{ state: string }>(
      "GET",
      `/api/v1/publication/snapshots/${snapshot.data.id}?host=127.0.0.1`,
    );
    expect(active.data.state).toBe("active");
    expect(active.etag).toBeTruthy();
    expect(activationRequests).toHaveLength(1);
    expect(snapshotReads).toBe(readsBefore);
    await activation
      .getByRole("button", { name: "Retry same command", exact: true })
      .click();
    await expect(activation).toHaveCount(0);
    await expect(page.getByRole("status")).toContainText(
      "Discovery snapshot activated.",
    );
    expect(activationRequests).toHaveLength(2);
    expect(activationRequests[0]?.key).toBeTruthy();
    expect(activationRequests[0]?.etag).toBeTruthy();
    expect(activationRequests[1]).toEqual(activationRequests[0]);
    await expect(page.getByText(/displayed snapshot predates/)).toBeVisible();
    await expect(page.getByText(/This candidate is not served/)).toHaveCount(0);
    expect(snapshotReads).toBe(readsBefore);
    const replayed = await catalog.admin<{ state: string }>(
      "GET",
      `/api/v1/publication/snapshots/${snapshot.data.id}?host=127.0.0.1`,
    );
    expect(replayed.data.state).toBe("active");
    expect(replayed.etag).toBe(active.etag);
    network.expectFailure("GET", snapshotReadPath, 503);
    await page.route(
      (url) => url.pathname === catalog.basePath + snapshotReadPath,
      async (route) => {
        expect(route.request().method()).toBe("GET");
        await route.fulfill({
          status: 503,
          contentType: "application/problem+json",
          body: JSON.stringify({ code: "DEPENDENCY_UNAVAILABLE" }),
        });
      },
      { times: 1 },
    );
    await page
      .getByRole("button", { name: "Reload snapshot state", exact: true })
      .click();
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(page.getByRole("status")).toContainText(
      "Discovery snapshot activated.",
    );
    await expect(page.getByText(/displayed snapshot predates/)).toBeVisible();
    await expect(
      page.getByRole("textbox", { name: "Reason", exact: true }),
    ).toHaveValue("Scoped snapshot activation");
    await expect(
      page.getByRole("button", { name: "Activate candidate", exact: true }),
    ).toBeDisabled();
    expect(snapshotReads).toBe(readsBefore + 1);
    expect(activationRequests).toHaveLength(2);
    await page
      .getByRole("button", { name: "Reload snapshot state", exact: true })
      .click();
    await expect(
      page.getByRole("heading", {
        name: `Generation ${snapshot.data.generation}: active`,
        exact: true,
      }),
    ).toBeVisible();
    expect(snapshotReads).toBe(readsBefore + 2);
    await expect(page.getByRole("alert")).toHaveCount(0);
    await expect(page.getByText(/displayed snapshot predates/)).toHaveCount(0);
    await expect(page.getByRole("status")).toContainText(
      "Discovery snapshot activated.",
    );
    await expect(
      page.getByRole("textbox", { name: "Reason", exact: true }),
    ).toHaveValue("Scoped snapshot activation");
    await expect(
      page.getByRole("textbox", { name: "Reason", exact: true }),
    ).toBeDisabled();
    await page.goto(
      `${catalog.origin}${catalog.basePath}/releases/${required(item.releaseId)}`,
    );
    await expect(
      page.getByRole("combobox", { name: "Disposition", exact: true }),
    ).toBeEnabled();
    await page
      .getByRole("textbox", { name: "Reason", exact: true })
      .fill("Scoped delegate deprecation");
    await page
      .getByRole("button", { name: "Change release disposition", exact: true })
      .click();
    await confirmCommand(page);
    await expect(page.getByRole("status")).toContainText("Release deprecated.");
    await expect(
      page.getByRole("heading", { name: "Release published", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText(/displayed release is last-loaded/),
    ).toBeVisible();
    const current = await catalog.admin<{ state: string }>(
      "GET",
      `/api/v1/listing-releases/${required(item.releaseId)}`,
    );
    expect(current.data.state).toBe("deprecated");
    const releaseReadsBefore = releaseReads;
    network.expectFailure("GET", releaseReadPath, 503);
    await page.route(
      catalog.origin + catalog.basePath + releaseReadPath,
      async (route) => {
        expect(route.request().method()).toBe("GET");
        await route.fulfill({
          status: 503,
          contentType: "application/problem+json",
          body: JSON.stringify({ code: "DEPENDENCY_UNAVAILABLE" }),
        });
      },
      { times: 1 },
    );
    await page
      .getByRole("button", { name: "Reload release state", exact: true })
      .click();
    await expect(page.getByRole("alert")).toBeVisible();
    await expect(page.getByRole("status")).toContainText("Release deprecated.");
    await expect(
      page.getByRole("textbox", { name: "Reason", exact: true }),
    ).toHaveValue("Scoped delegate deprecation");
    await expect(
      page.getByRole("heading", { name: "Release published", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", {
        name: "Change release disposition",
        exact: true,
      }),
    ).toBeDisabled();
    expect(releaseReads).toBe(releaseReadsBefore + 1);
    expect(mutations).toBe(3);
    await page
      .getByRole("button", { name: "Reload release state", exact: true })
      .click();
    await expect(
      page.getByRole("heading", { name: "Release deprecated", exact: true }),
    ).toBeVisible();
    await expect(page.getByRole("alert")).toHaveCount(0);
    await expect(
      page.getByText(/displayed release is last-loaded/),
    ).toHaveCount(0);
    await expect(page.getByRole("status")).toContainText("Release deprecated.");
    await expect(
      page.getByRole("textbox", { name: "Reason", exact: true }),
    ).toHaveValue("Scoped delegate deprecation");
    expect(releaseReads).toBe(releaseReadsBefore + 2);

    await page.setViewportSize({ width: 390, height: 844 });
    await expect
      .poll(() => page.evaluate(() => document.documentElement.scrollWidth))
      .toBeLessThanOrEqual(390);
    expect(mutations).toBe(3);
  });
}
