// Owned responses qualify private presentation and exact downloads, not native cryptographic verification.
import { readFile } from "node:fs/promises";
import { test, expect, login, signOut } from "./fixtures";
import {
  trustSample,
  trustSource,
  trustJob,
  evidenceSamples,
  trustEntry,
} from "../unit/trust-fixtures";

for (const base of ["root", "subpath"]) {
  test(`${base}: private ANS results require an explicit audited read and preserve exact evidence`, async ({
    page,
    catalog,
    network,
  }, info) => {
    let reads = 0;
    let forbiddenSourceReads = 0;
    let response = trustSample();
    let status = 200;
    await page.route(
      `**/_catalog/api/v1/federation/sources/${trustSource}`,
      async (route) => {
        forbiddenSourceReads++;
        await route.fulfill({ status: 500 });
      },
    );
    const path = `/api/v1/federation/sources/${trustSource}/trust-assessments/${trustJob}`;
    await page.route(`**/_catalog${path}`, async (route) => {
      reads++;
      expect(route.request().method()).toBe("GET");
      await route.fulfill({
        status,
        json:
          status === 200
            ? response
            : {
                type: "about:blank",
                status,
                title: "Unavailable",
                code: "DEPENDENCY_UNAVAILABLE",
              },
      });
    });
    const route = `/trust-assessments/${trustJob}?sourceId=${trustSource}`;
    await login(page, catalog, "local-reader", route);
    await expect(page.getByRole("alert")).toContainText("source.read_evidence");
    expect(reads).toBe(0);
    await signOut(page);
    await login(page, catalog, "local-source", route);
    await expect(
      page.getByRole("heading", { name: "ANS assessments", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("textbox", { name: "Source ID", exact: true }),
    ).toHaveValue(trustSource);
    expect(reads).toBe(0);
    for (const [label, valid, error] of [
      ["Source ID", trustSource, "Enter a valid Source ID in UUID format."],
      [
        "Assessment Job ID",
        trustJob,
        "Enter a valid assessment Job ID in UUID format.",
      ],
    ] as const) {
      const input = page.getByRole("textbox", { name: label, exact: true });
      await input.fill("invalid-id");
      await expect(input).toHaveAttribute("aria-invalid", "true");
      expect(
        await input.evaluate(
          (node: HTMLInputElement) => node.validity.patternMismatch,
        ),
      ).toBe(true);
      await expect(page.getByText(error, { exact: true })).toBeVisible();
      await expect(
        page.getByRole("button", {
          name: "Read private assessment",
          exact: true,
        }),
      ).toBeDisabled();
      expect(reads).toBe(0);
      await input.fill(valid);
    }
    await expect(
      page.getByRole("button", {
        name: "Copy Assessment Source ID",
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Copy Assessment Job ID", exact: true }),
    ).toBeVisible();
    const documentation = await page
      .getByRole("link", { name: "Documentation", exact: true })
      .getAttribute("href");
    expect(documentation).toMatch(/^https:\/\/fixture\.invalid\/case-/);
    const setup = page.getByText(
      "Find an assessment and read historical evidence",
      { exact: true },
    );
    await setup.click();
    await expect(
      page.getByRole("main").getByRole("link", { name: /^Documentation/ }),
    ).toHaveAttribute("href", documentation!);
    await setup.click();
    await page
      .getByRole("button", { name: "Read private assessment", exact: true })
      .focus();
    await page.keyboard.press("Enter");
    await expect(page.getByText(/Current registration under/)).toBeVisible();
    expect(reads).toBe(1);
    expect(forbiddenSourceReads).toBe(0);
    await expect(page.getByRole("row")).toHaveCount(8);
    await expect(
      page.locator('td [data-state="valid"][data-tone="success"]'),
    ).toHaveCount(7);
    await expect(
      page
        .getByRole("button", { name: "Read private assessment", exact: true })
        .locator('svg[aria-hidden="true"]'),
    ).toHaveCount(1);
    const checks = page.getByRole("region", {
      name: "Registration assessment checks",
      exact: true,
    });
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(checks).toHaveAttribute("tabindex", "0");
    await checks.focus();
    await expect(checks).toBeFocused();
    await page.setViewportSize({ width: 1440, height: 1100 });
    await expect(
      page.getByText(/Software safety, artifact integrity/),
    ).toContainText("Not assessed");
    await expect(
      page.getByText(/Refresh checks whether this saved result still applies/),
    ).toBeVisible();
    const checkHelp = page.locator("details").filter({
      has: page.getByText("What these checks mean", { exact: true }),
    });
    await expect(checkHelp).toHaveJSProperty("open", false);
    await checkHelp.locator("summary").focus();
    await page.keyboard.press("Enter");
    await expect(checkHelp).toHaveJSProperty("open", true);
    await expect(checkHelp).toContainText("signed evidence at assessment time");
    await checkHelp.locator("summary").click();
    await expect(checkHelp).toHaveJSProperty("open", false);
    expect(reads).toBe(1);
    expect(forbiddenSourceReads).toBe(0);
    const entry = page
      .getByText("Inspect assessed original entry", { exact: true })
      .locator("..");
    await expect(entry).toHaveJSProperty("open", false);
    await expect(
      entry.getByRole("group", { name: "JSON document" }),
    ).toHaveCount(0);
    await entry.locator("summary").focus();
    await page.keyboard.press("Enter");
    await expect(
      entry.getByRole("group", { name: "JSON document" }),
    ).toHaveText(trustEntry);
    await expect(
      entry.getByRole("button", { name: "Copy document text" }),
    ).toBeVisible();
    expect(reads).toBe(1);
    expect(forbiddenSourceReads).toBe(0);
    await entry.locator("summary").click();
    for (const [name, original] of Object.entries(evidenceSamples)) {
      const labels = {
        entry: "original entry",
        receipt: "receipt",
        checkpoint: "checkpoint",
        checkpointHistory: "checkpoint history",
        statusToken: "status token",
      };
      const download = page.waitForEvent("download");
      await page
        .getByRole("link", {
          name: `Save ${labels[name as keyof typeof labels]}`,
          exact: true,
        })
        .click();
      const file = await download;
      expect(file.suggestedFilename()).toBe(`ans-${trustJob}-${name}.bin`);
      expect(await readFile((await file.path())!)).toEqual(
        Buffer.from(original),
      );
    }
    expect(reads).toBe(1); // Downloads use this audited read, without additional requests.
    response.current = false;
    response.applicability = "historical";
    response.result.assessment.currentRegistration = false;
    response.result.assessment.producer = {
      state: "invalid",
      code: "signature_failed",
    };
    response.result.assessment.log = {
      state: "unsupported",
      code: "algorithm_unsupported",
    };
    await page
      .getByRole("button", { name: "Refresh private assessment", exact: true })
      .click();
    await expect(page.getByText(/Historical evidence\./)).toBeVisible();
    await expect(
      page.locator('[data-state="historical"][data-tone="neutral"]'),
    ).toHaveText("Historical");
    await expect(
      page.locator('td [data-state="invalid"][data-tone="danger"]'),
    ).toHaveText("Invalid");
    await expect(
      page.locator('td [data-state="unsupported"][data-tone="neutral"]'),
    ).toHaveText("Unsupported");
    expect(reads).toBe(2);
    await page.setViewportSize({ width: 1440, height: 1100 });
    // Downloads and keyboard actions can scroll the page. Capture its normal top view.
    await page.evaluate(() => {
      if (document.activeElement instanceof HTMLElement)
        document.activeElement.blur();
      window.scrollTo(0, 0);
    });
    await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(0);
    await page.screenshot({
      path: info.outputPath("ans-assessment.png"),
      fullPage: true,
    });
    await page.setViewportSize({ width: 390, height: 844 });
    await checkHelp.locator("summary").click();
    await expect(checkHelp).toHaveJSProperty("open", true);
    await expect(checkHelp).toContainText("do not assess software safety");
    expect(reads).toBe(2);
    await checkHelp.locator("summary").click();
    await expect
      .poll(() => page.evaluate(() => document.documentElement.scrollWidth))
      .toBeLessThanOrEqual(390);

    // Valid response fields do not establish that the retained entry bytes match their digest.
    response = trustSample();
    response.result.evidence.entry = Buffer.from(
      "changed original entry",
    ).toString("base64");
    await page
      .getByRole("button", { name: "Refresh private assessment", exact: true })
      .click();
    await expect(page.getByRole("alert")).toContainText(
      "This page could not be loaded.",
    );
    await expect(page.getByText("operator-ans", { exact: true })).toHaveCount(
      0,
    );
    await expect(page.getByRole("link", { name: /^Save / })).toHaveCount(0);
    expect(reads).toBe(3);
    response = trustSample();
    await page
      .getByRole("button", { name: "Refresh private assessment", exact: true })
      .click();
    await expect(page.getByText(/Current registration under/)).toBeVisible();
    await expect(page.getByRole("link", { name: /^Save / })).toHaveCount(5);
    expect(reads).toBe(4);
    status = 503;
    network.expectFailure("GET", "/_catalog" + path, 503);
    await page
      .getByRole("button", { name: "Refresh private assessment", exact: true })
      .click();
    await expect(page.getByText(/required disclosure audit/)).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Save original entry", exact: true }),
    ).toHaveCount(0);
    expect(reads).toBe(5);
    status = 200;
    response = trustSample();
    response.current = false;
    response.applicability = "negative_or_insufficient";
    const a = response.result.assessment;
    const skipped = {
      state: "not_assessed" as const,
      code: "prerequisite_failed",
    };
    response.result.assessment = {
      profile: a.profile,
      policyDigest: a.policyDigest,
      entryDigest: a.entryDigest,
      checkedAt: a.checkedAt,
      currentRegistration: false,
      receipt: { state: "invalid", code: "receipt_signature_or_encoding" },
      producer: skipped,
      log: skipped,
      checkpoint: skipped,
      inclusion: skipped,
      target: skipped,
      status: skipped,
    };
    await page
      .getByRole("button", { name: "Refresh private assessment", exact: true })
      .click();
    await expect(
      page.getByText(/Negative or insufficient evidence\./),
    ).toBeVisible();
    await expect(
      page.getByRole("row", {
        name: "Receipt Invalid receipt_signature_or_encoding",
        exact: true,
      }),
    ).toBeVisible();
    expect(reads).toBe(6);
    await signOut(page);
    await expect(page.getByText("operator-ans", { exact: true })).toHaveCount(
      0,
    );
    await login(page, catalog, "local-reader", route);
    await expect(page.getByRole("alert")).toContainText("source.read_evidence");
    expect(reads).toBe(6);
    expect(forbiddenSourceReads).toBe(0);
  });
}
