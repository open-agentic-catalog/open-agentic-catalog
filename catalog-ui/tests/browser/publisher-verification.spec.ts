import { test, expect, login, confirmCommand } from "./fixtures";
import { writeFile } from "node:fs/promises";
import { object } from "./json";

test.describe("optional Publisher DNS verification", () => {
  test.use({ publisherVerification: true });
  test("issues, copies, replaces and clears owned challenges without DNS or approval changes", async ({
    page,
    catalog,
  }) => {
    const created = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/publishers",
      { displayName: "DNS fixture", domains: ["example.test", "second.test"] },
    );
    await login(page, catalog, "local-admin", `/publishers/${created.data.id}`);
    const evidence = page.getByRole("region", {
      name: "Publisher DNS verification",
    });
    await evidence
      .getByRole("button", { name: "View DNS verification" })
      .click();
    await expect(evidence).toContainText("DNS verification is optional");
    await expect(
      evidence.getByRole("button", { name: "Check DNS", exact: true }),
    ).toBeDisabled();
    await evidence
      .getByRole("textbox", { name: "DNS verification reason" })
      .fill("Set up owned TXT fixture");
    await evidence
      .getByRole("button", { name: "Create DNS challenge", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toContainText("example.test");
    await confirmCommand(page);
    await expect(
      evidence.getByRole("button", {
        name: "Show TXT instructions",
        exact: true,
      }),
    ).toBeVisible();
    await page.setViewportSize({ width: 1440, height: 1800 });
    await evidence.evaluate((element) =>
      element.scrollIntoView({ block: "center" }),
    );
    await evidence.screenshot({
      path: test.info().outputPath("publisher-dns-verification.png"),
    });
    await page.setViewportSize({ width: 1440, height: 900 });
    await evidence
      .getByRole("button", { name: "Show TXT instructions", exact: true })
      .click();
    const instructions = evidence.getByRole("region", {
      name: "Current TXT instructions",
    });
    await expect(instructions).toContainText(
      "_open-agentic-catalog-challenge.example.test.",
    );
    const value = await instructions.locator("code").last().innerText();
    expect(value).toMatch(
      /^open-agentic-catalog-verification=[A-Za-z0-9_-]{43}$/,
    );
    await page
      .context()
      .grantPermissions(["clipboard-read", "clipboard-write"]);
    await instructions
      .getByRole("button", { name: "Copy TXT record value", exact: true })
      .click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
      value,
    );
    await page.setViewportSize({ width: 320, height: 800 });
    const width = await page.evaluate(() => ({
      scroll: document.documentElement.scrollWidth,
      client: document.documentElement.clientWidth,
    }));
    expect(width.scroll).toBeLessThanOrEqual(width.client + 1);
    await evidence
      .getByRole("textbox", { name: "DNS verification reason" })
      .fill("Replace owned challenge");
    await evidence
      .getByRole("button", { name: "Replace DNS challenge", exact: true })
      .click();
    await confirmCommand(page);
    await expect(
      evidence.getByRole("region", { name: "Current TXT instructions" }),
    ).toHaveCount(0);
    await evidence
      .getByRole("button", { name: "Show TXT instructions", exact: true })
      .click();
    await expect(instructions.locator("code").last()).not.toHaveText(value);
    await evidence
      .getByRole("textbox", { name: "DNS verification reason" })
      .fill("Clear owned evidence");
    await evidence
      .getByRole("button", { name: "Clear DNS verification", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toContainText(
      "Publication approval stays unchanged",
    );
    await confirmCommand(page);
    await expect(
      evidence.getByRole("button", {
        name: "Create DNS challenge",
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      evidence.getByRole("button", {
        name: "Show TXT instructions",
        exact: true,
      }),
    ).toHaveCount(0);
    await expect(
      evidence.getByRole("button", { name: "Refresh DNS status", exact: true }),
    ).toBeFocused();
    const publisher = await catalog.admin<{
      approvedPublicationDomains: string[];
    }>("GET", `/api/v1/publishers/${created.data.id}`);
    expect(publisher.data.approvedPublicationDomains).toEqual([]);
    // No Check DNS action is submitted. Native resolver and HTTP tests own lookup outcomes.
  });
  test("recovers an issued challenge after another client clears it without issuing again", async ({
    page,
    catalog,
    network,
  }) => {
    const created = await catalog.admin<{ id: string }>(
      "POST",
      "/api/v1/publishers",
      { displayName: "Replay DNS fixture", domains: ["example.test"] },
    );
    await login(page, catalog, "local-admin", `/publishers/${created.data.id}`);
    const evidence = page.getByRole("region", {
      name: "Publisher DNS verification",
    });
    await evidence
      .getByRole("button", { name: "View DNS verification" })
      .click();
    const base = `/api/v1/publishers/${created.data.id}/domains/example.test/verification`;
    const path = `${catalog.basePath}/_catalog${base}:issue`;
    const commands: {
      key: string | undefined;
      etag: string | undefined;
      body: string | null;
    }[] = [];
    let original = "";
    let originalReceipt: unknown;
    network.expectFailure("POST", path, 502);
    await page.route(`**${path}`, async (route) => {
      const request = route.request();
      commands.push({
        key: request.headers()["idempotency-key"],
        etag: request.headers()["if-match"],
        body: request.postData(),
      });
      const reply = await route.fetch({ maxRetries: 0 });
      expect(reply.status()).toBe(200);
      if (commands.length === 1) {
        original = await reply.text();
        originalReceipt = JSON.parse(original) as unknown;
        const current = await catalog.admin("GET", base);
        await catalog.admin(
          "POST",
          `${base}:clear`,
          { reason: "Clear evidence through another owned client" },
          current.etag,
        );
        await route.fulfill({
          status: 502,
          contentType: "application/json",
          body: JSON.stringify({ status: 502, code: "DEPENDENCY_UNAVAILABLE" }),
        });
      } else {
        expect(reply.headers()["idempotency-replayed"]).toBe("true");
        expect(await reply.text()).toBe(original);
        await route.fulfill({ response: reply });
      }
    });
    await evidence
      .getByRole("textbox", { name: "DNS verification reason" })
      .fill("Recover the original issue command");
    await evidence
      .getByRole("button", { name: "Create DNS challenge", exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    const retry = page
      .getByRole("dialog")
      .getByRole("button", { name: "Retry same command", exact: true });
    await expect(retry).toBeEnabled();
    expect(commands).toHaveLength(1);
    const receipt = object(originalReceipt);
    const status = object(receipt.status);
    // Keep the pending-state evidence without recording the private TXT value.
    const issued = {
      domain: status.domain,
      mode: status.mode,
      challengeState: status.challengeState,
      challengeIssuedAt: status.challengeIssuedAt,
      challengeExpiresAt: status.challengeExpiresAt,
      matchingChallenge: receipt.challengeId === status.challengeId,
      matchingExpiry: receipt.expiresAt === status.challengeExpiresAt,
      validTXT:
        typeof receipt.value === "string" &&
        /^open-agentic-catalog-verification=[A-Za-z0-9_-]{43}$/.test(
          receipt.value,
        ),
    };
    await writeFile(
      test.info().outputPath("issued-challenge-status.json"),
      JSON.stringify(issued, null, 2) + "\n",
    );
    expect(
      issued,
      "The original issue response must contain a pending challenge",
    ).toMatchObject({
      domain: "example.test",
      mode: "optional",
      challengeState: "pending",
      matchingChallenge: true,
      matchingExpiry: true,
      validTXT: true,
    });
    await retry.click();
    await expect(evidence).toContainText(
      "Original command result: The Catalog issued a DNS challenge.",
    );
    await expect(evidence).toContainText(
      "The Catalog did not query DNS or change the evidence again.",
    );
    await expect(
      evidence.getByRole("button", {
        name: "Create DNS challenge",
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      evidence.getByRole("button", {
        name: "Show TXT instructions",
        exact: true,
      }),
    ).toHaveCount(0);
    expect(commands).toHaveLength(2);
    expect(commands[1]).toEqual(commands[0]);
    const current = await catalog.admin<{
      challengeState: string;
      state: string;
    }>("GET", base);
    expect(current.data).toMatchObject({
      challengeState: "not_issued",
      state: "not_verified",
    });
  });
});

test("disabled Publisher DNS verification exposes status without command controls", async ({
  page,
  catalog,
}) => {
  await login(
    page,
    catalog,
    "local-admin",
    `/publishers/${catalog.receipt.publisherId}`,
  );
  // The maintained sample declares an IP literal, which is not a verification domain.
  // Use an exact DNS domain so this test reaches disabled-capability presentation.
  const created = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/publishers",
    { displayName: "Disabled DNS fixture", domains: ["example.test"] },
  );
  await page.goto(
    `${catalog.origin}${catalog.basePath}/publishers/${created.data.id}`,
  );
  const evidence = page.getByRole("region", {
    name: "Publisher DNS verification",
  });
  await evidence.getByRole("button", { name: "View DNS verification" }).click();
  await expect(evidence).toContainText("DNS verification is turned off");
  await expect(
    evidence.getByRole("button", { name: "Create DNS challenge", exact: true }),
  ).toHaveCount(0);
});
