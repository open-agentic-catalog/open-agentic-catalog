// Controlled replies qualify explicit browser recovery and draft retention, not live-peer ingestion.
import { test, expect, login } from "./fixtures";
import { object } from "./json";

for (const present of [false, true]) {
  test(`${present ? "root present-empty" : "subpath absent"}: live proof refresh explains disclosure, preserves drafts and never changes the captured target`, async ({
    page,
    catalog,
    network,
  }) => {
    const sourceId = "00000000-0000-7000-8000-000000000194";
    const live = {
      id: "live-reader",
      type: "live",
      kind: "agent",
      identifier: "urn:air:reader.example:agents:one",
      versionPresent: present,
      ...(present ? { version: "" } : {}),
      displayName: "Live reader fixture",
      tags: [],
      live: {
        sourceId,
        source: "https://reader.example",
        presentationGeneration: 1,
        entry: { description: "Synthetic declaration" },
      },
      artwork: { receipt: "artwork-initial" },
      ratings: { count: 0, receipt: "ratings-initial" },
    };
    let mode: "valid" | "changed" | "expired" | "unbound" = "valid";
    let hold: (() => void) | undefined;
    let waiting = false;
    const calls: unknown[] = [];
    await page.route("**/_catalog/api/v1/catalog/search", async (route) => {
      calls.push(route.request().postDataJSON());
      if (waiting)
        await new Promise<void>((resolve) => {
          hold = resolve;
        });
      if (mode === "expired") {
        await route.fulfill({
          status: 409,
          contentType: "application/problem+json",
          json: {
            type: "about:blank",
            title: "Result window expired",
            status: 409,
            code: "INVALID_CURSOR",
          },
        });
        return;
      }
      const response = await route.fetch();
      const data = object(await response.json());
      delete data.nextCursor;
      const wire = { ...live };
      if (present && calls.length % 2 === 1) delete wire.version;
      await route.fulfill({
        response,
        json: {
          ...data,
          items: [
            {
              ...wire,
              live: {
                ...live.live,
                presentationGeneration:
                  mode === "unbound" ? undefined : mode === "changed" ? 2 : 1,
              },
              artwork: { receipt: `artwork-${calls.length}` },
              ratings: { count: 0, receipt: `rating-${calls.length}` },
            },
          ],
          durableTotal: 0,
          liveCount: 1,
          partial: false,
        },
      });
    });
    await page.route("**/_catalog/api/v1/ratings/read", async (route) => {
      await route.fulfill({
        json: {
          target: object(route.request().postDataJSON()).target,
          count: 0,
          changedEntry: false,
        },
      });
    });
    const artworkPath = `/api/v1/federation/sources/${sourceId}/artwork`;
    network.expectFailure("GET", `/_catalog${artworkPath}`, 404);
    await page.route(`**/_catalog${artworkPath}?*`, async (route) => {
      await route.fulfill({
        status: 404,
        contentType: "application/problem+json",
        json: {
          type: "about:blank",
          title: "Not found",
          status: 404,
          code: "NOT_FOUND",
        },
      });
    });
    await login(page, catalog, "local-admin");
    const query = page.getByRole("searchbox", {
      name: "Search Catalog",
      exact: true,
    });
    await query.fill("applied live text");
    await query.press("Enter");
    await expect.poll(() => calls.length).toBe(2);
    await query.fill("pending unsent text");
    await page
      .getByRole("button", { name: live.displayName, exact: true })
      .click();
    const dialog = page.getByRole("dialog");
    await expect(
      dialog.getByText(/Refresh repeats the captured Catalog search/),
    ).toBeVisible();
    await dialog
      .getByRole("button", { name: "Manage local artwork", exact: true })
      .click();
    const reason = dialog.getByRole("textbox", {
      name: "Artwork override reason",
      exact: true,
    });
    await reason.fill("Retain artwork reason");
    await dialog
      .getByRole("button", { name: "Ratings for this version", exact: true })
      .click();
    await dialog.getByRole("radio", { name: "3 stars", exact: true }).check();
    const refresh = dialog.getByRole("button", {
      name: "Refresh live ratings",
      exact: true,
    });
    waiting = true;
    await refresh.click();
    await expect(refresh).toBeDisabled();
    await expect(refresh).toHaveAttribute("aria-busy", "true");
    await expect.poll(() => !!hold).toBe(true);
    waiting = false;
    hold!();
    await expect(dialog.getByText(/Live ratings refreshed/)).toBeVisible();
    await expect(
      dialog.getByRole("radio", { name: "3 stars", exact: true }),
    ).toBeChecked();
    await expect(reason).toHaveValue("Retain artwork reason");
    await dialog
      .getByRole("button", { name: "Refresh live artwork", exact: true })
      .click();
    await expect(
      dialog.getByText("Live artwork refreshed.", { exact: true }),
    ).toBeVisible();
    mode = "changed";
    await refresh.click();
    await expect(
      dialog.getByText(/This target is no longer on the captured result page/),
    ).toBeVisible();
    await dialog
      .getByRole("button", { name: "Refresh live artwork", exact: true })
      .click();
    await expect(
      dialog.getByText(
        /This source entry is no longer on the captured result page/,
      ),
    ).toBeVisible();
    mode = "expired";
    network.expectFailure("POST", "/_catalog/api/v1/catalog/search", 409);
    await refresh.click();
    await expect(
      dialog.getByText(/Could not refresh live ratings/),
    ).toBeVisible();
    await expect(
      dialog.getByText("Last loaded live result", { exact: true }),
    ).toBeVisible();
    await expect(
      dialog.getByRole("radio", { name: "3 stars", exact: true }),
    ).toBeChecked();
    await expect(reason).toHaveValue("Retain artwork reason");
    expect(calls).toHaveLength(7);
    for (const body of calls.slice(2))
      expect(body).toEqual({ text: "applied live text", limit: 24 });
    expect(JSON.stringify(calls)).not.toContain("pending unsent text");
    page.once("dialog", (dialog) => {
      void dialog.accept();
    });
    await dialog
      .getByRole("button", { name: "Close preview", exact: true })
      .click();
    mode = "unbound";
    await query.fill("applied live text");
    await query.press("Enter");
    await expect.poll(() => calls.length).toBe(8);
    await page
      .getByRole("button", { name: live.displayName, exact: true })
      .click();
    await expect(
      dialog.getByText(/Exact source and generation are unavailable/),
    ).toBeVisible();
    await expect(
      dialog.getByRole("button", { name: "Refresh live artwork", exact: true }),
    ).toBeDisabled();
    expect(calls).toHaveLength(8);
  });
}
