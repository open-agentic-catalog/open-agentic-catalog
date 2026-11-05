// Real governed releases qualify profile membership; this fixture adds only owned configured host bindings.
import type { components } from "../../src/api/schema";
import { test, expect, login } from "./fixtures";
import { publishFixture } from "./published-fixture";

test.use({ publicationAudiences: true });
type Snapshot = components["schemas"]["PublicationSnapshot"];

test("subpath publication distinguishes tenant/public/private membership and retained audience", async ({
  page,
  catalog,
}, info) => {
  const initial = catalog.receipt.items.filter((item) => item.releaseId).length;
  await publishFixture(catalog, 901, "", {
    visibility: "tenant",
    representativeQueries: [],
    displayName: "Tenant research agent",
  });
  await publishFixture(catalog, 902, "", {
    visibility: "private",
    displayName: "Private research draft",
  });
  const tenantIdentifier = "urn:air:demo.catalog.example.com:demo:paged-901";
  const privateIdentifier = "urn:air:demo.catalog.example.com:demo:paged-902";
  await login(page, catalog, "local-publisher", "/publication");
  for (const [host, audience] of [
    ["127.0.0.1", "anonymous"],
    ["private.catalog.test", "authenticated"],
  ] as const) {
    const { data: snapshot } = await catalog.admin<Snapshot>(
      "POST",
      "/api/v1/publication/snapshots",
      { host },
    );
    expect(snapshot.audience).toBe(audience);
    expect(snapshot.ard.entries).toHaveLength(initial + 1);
    expect(snapshot.aiCatalog.entries).toHaveLength(
      initial + (audience === "authenticated" ? 1 : 0),
    );
    expect(
      snapshot.ard.entries.some(
        (entry) => entry.identifier === tenantIdentifier,
      ),
    ).toBe(false);
    expect(
      snapshot.aiCatalog.entries.some(
        (entry) => entry.identifier === tenantIdentifier,
      ),
    ).toBe(audience === "authenticated");
    expect(
      snapshot.ard.entries.some(
        (entry) => entry.identifier === privateIdentifier,
      ),
    ).toBe(false);
    expect(
      snapshot.aiCatalog.entries.some(
        (entry) => entry.identifier === privateIdentifier,
      ),
    ).toBe(false);
    // A staged contradictory audience cannot become evidence about the retained document.
    await page.goto(
      `${catalog.origin}${catalog.basePath}/publication/${snapshot.id}?host=${host}&audience=${audience === "anonymous" ? "authenticated" : "anonymous"}`,
    );
    const generation = page.locator("section.workspace-panel").filter({
      has: page.getByRole("heading", {
        name: new RegExp(`^Generation ${snapshot.generation}: candidate$`),
      }),
    });
    await expect(generation).toContainText(`${host} / ${audience}`);
    const selected = page.getByRole("region", {
      name: "Selected publication host",
      exact: true,
    });
    await expect(selected).toContainText(host);
    await expect(selected.getByText(/audience/i)).toHaveCount(0);
    await expect(selected.getByText(/Anonymous|Authenticated/)).toHaveCount(0);
    const contents = page.getByRole("region", {
      name: "Included discovery entries",
    });
    const count = (label: string) =>
      contents
        .locator("dt")
        .filter({ hasText: new RegExp(`^${label}$`) })
        .locator("+ dd");
    await expect(count("ARD Registry self-entries")).toHaveText("1");
    await expect(count("ARD resource entries")).toHaveText(String(initial));
    await expect(count("AI Catalog resource entries")).toHaveText(
      String(initial + (audience === "authenticated" ? 1 : 0)),
    );
    await contents
      .getByText(
        `Inspect AI Catalog entries (${snapshot.aiCatalog.entries.length})`,
        { exact: true },
      )
      .click();
    const entries = contents.getByRole("table", {
      name: "AI Catalog included entries",
    });
    if (audience === "authenticated") {
      const tenantRow = entries
        .getByRole("row")
        .filter({ hasText: tenantIdentifier });
      await expect(tenantRow).toContainText("1.0.0");
      await expect(tenantRow).not.toContainText("private");
      await contents
        .getByText(`Inspect ARD entries (${snapshot.ard.entries.length})`, {
          exact: true,
        })
        .click();
      for (const name of ["ARD", "AI Catalog"]) {
        await expect(
          contents
            .getByRole("table", {
              name: `${name} included entries`,
              exact: true,
            })
            .getByRole("button", {
              name: "Sort by identifier, ascending",
              exact: true,
            }),
        ).toBeVisible();
      }
      await page.evaluate(() => window.scrollTo(0, 0));
      await page.screenshot({
        fullPage: true,
        path: info.outputPath("publication-contents.png"),
      });
    }
  }
});

test("empty tenant publication includes only the ARD Registry self-entry", async ({
  page,
  catalog,
}) => {
  const host = "empty.catalog.test";
  const { data: snapshot } = await catalog.admin<Snapshot>(
    "POST",
    "/api/v1/publication/snapshots",
    { host },
    undefined,
    "other-tenant-admin",
  );
  expect(snapshot.ard.entries).toHaveLength(1);
  expect(snapshot.ard.entries[0]?.metadata?.catalogReleaseId).toBe(
    "registry:self",
  );
  expect(snapshot.aiCatalog.entries).toEqual([]);
  await login(
    page,
    catalog,
    "other-tenant-admin",
    `/publication/${snapshot.id}?host=${host}`,
  );
  const contents = page.getByRole("region", {
    name: "Included discovery entries",
  });
  await expect(
    contents
      .locator("dt")
      .filter({ hasText: /^ARD Registry self-entries$/ })
      .locator("+ dd"),
  ).toHaveText("1");
  await expect(
    contents
      .locator("dt")
      .filter({ hasText: /^ARD resource entries$/ })
      .locator("+ dd"),
  ).toHaveText("0");
  await contents
    .getByText("Inspect AI Catalog entries (0)", { exact: true })
    .click();
  await expect(
    contents.getByText("No AI Catalog entries in this document.", {
      exact: true,
    }),
  ).toBeVisible();
});

test("controlled publication Job projection links to its actual retained candidate without prefetch", async ({
  page,
  catalog,
}) => {
  const { data: snapshot } = await catalog.admin<Snapshot>(
    "POST",
    "/api/v1/publication/snapshots",
    { host: "127.0.0.1" },
  );
  // Publication generation has no public enqueue endpoint. The composed Go functional test owns actual worker execution.
  // This controlled read qualifies browser projection/navigation into the real API-owned snapshot.
  const id = "00000000-0000-7000-8000-000000000099";
  let jobs = 0;
  let snapshots = 0;
  await page.route(`**/_catalog/api/v1/jobs/${id}`, async (route) => {
    jobs++;
    await route.fulfill({
      json: {
        id,
        tenantId: "00000000-0000-7000-8000-000000000001",
        kind: "publication.generate",
        state: "succeeded",
        priority: 0,
        progress: 100,
        notBefore: "2026-09-01T12:00:00Z",
        attempt: 1,
        attemptLimit: 3,
        cancellationRequested: false,
        createdAt: "2026-09-01T12:00:00Z",
        updatedAt: "2026-09-01T12:00:00Z",
        result: {
          type: "publication_snapshot",
          snapshotId: snapshot.id,
          host: snapshot.host,
          generation: snapshot.generation,
          state: "candidate",
        },
      },
    });
  });
  page.on("request", (request) => {
    if (
      new URL(request.url()).pathname.endsWith(
        `/publication/snapshots/${snapshot.id}`,
      )
    )
      snapshots++;
  });
  await login(page, catalog, "local-publisher", `/jobs/${id}`);
  const link = page.getByRole("link", {
    name: "Inspect resulting publication snapshot",
    exact: true,
  });
  await expect(link).toBeVisible();
  expect(jobs).toBe(1);
  expect(snapshots).toBe(0);
  await expect(
    page.getByText(/not proof of current discovery activation/),
  ).toBeVisible();
  await link.click();
  await expect(
    page.getByRole("heading", {
      name: `Generation ${snapshot.generation}: candidate`,
      exact: true,
    }),
  ).toBeVisible();
  expect(snapshots).toBe(1);
});
