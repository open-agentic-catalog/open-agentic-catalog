// Explicit commands against the owned DNS/API/worker/UI fixture. No public DNS
// changes, automatic mutation retries or clock changes enter this journey.
// Selected replies are corrupted only after the real command commits.
import { expect } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { randomUUID } from "node:crypto";
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";

export async function publisherVerificationJourney({
  origin,
  request,
  fixtureRequest,
  replyFault,
  restart,
  phase,
  output,
}) {
  const reason = "Qualify owned Publisher DNS";
  const body = { reason };
  const queries = async () => (await fixtureRequest("/publisher-dns")).queries;
  const ok = (reply, status = 200) => {
    if (reply.status !== status) {
      const known = new Set([
        "PRECONDITION_FAILED",
        "NOT_FOUND",
        "CONFLICT",
        "DEPENDENCY_UNAVAILABLE",
        "UNAUTHENTICATED",
        "FORBIDDEN",
        "REQUEST_DEADLINE_EXCEEDED",
        "WORK_LIMIT_EXCEEDED",
        "INTERNAL",
      ]);
      console.error(
        `Owned Publisher DNS reply: operation=${reply.operation} status=${reply.status} expected=${status} code=${known.has(reply.data?.code) ? reply.data.code : "other"}`,
      );
    }
    expect(reply.status).toBe(status);
    return reply;
  };
  const command = async (path, etag, input = body, key = randomUUID()) =>
    ok(await request("POST", path, { body: input, key, etag }));
  const created = ok(
    await request("POST", "/api/v1/publishers", {
      body: {
        displayName: "Owned DNS verification",
        domains: ["publisher.test", "second.test"],
      },
      key: randomUUID(),
    }),
    201,
  );
  const publisher = `/api/v1/publishers/${created.data.id}`;
  const verification = `${publisher}/domains/publisher.test/verification`;
  const status = async () => ok(await request("GET", verification));
  const metadata = async () => ok(await request("GET", publisher));
  const approve = async () =>
    command(
      `${publisher}/publication-domains/publisher.test:approve`,
      (await metadata()).headers.get("etag"),
    );
  let page;
  let errors = [];
  const open = async (mode) => {
    page = await restart(mode);
    errors = [];
    page.on("pageerror", () => errors.push("page"));
    page.on("console", (message) => {
      if (message.type() === "error") errors.push("console");
    });
    await page.goto(origin + "/catalog");
    await page.locator('[data-login-alias="local-admin"]').click();
    await expect(page.getByLabel("Account", { exact: true })).toBeVisible();
    await page.goto(origin + `/publishers/${created.data.id}`);
    const panel = page.getByRole("region", {
      name: "Publisher DNS verification",
      exact: true,
    });
    await panel
      .getByRole("button", { name: "View DNS verification", exact: true })
      .click();
    return panel;
  };
  const sendUI = async (panel, name, suffix, corruptReply) => {
    const fault = corruptReply
      ? replyFault.arm(
          new URL(origin).pathname + "/_catalog" + verification + suffix,
          corruptReply,
        )
      : undefined;
    let step = "prepare";
    let commandResponse;
    try {
      await panel
        .getByRole("textbox", { name: "DNS verification reason", exact: true })
        .fill(reason);
      await panel.getByRole("button", { name, exact: true }).click();
      const completed = page.waitForResponse(
        (response) =>
          response.request().method() === "POST" &&
          new URL(response.url()).pathname.endsWith(verification + suffix),
      );
      await page
        .getByRole("dialog")
        .getByRole("button", { name: "Confirm", exact: true })
        .click();
      step = "response";
      commandResponse = await completed;
      expect(commandResponse.status()).toBe(200);
      if (corruptReply) {
        step = "retry-available";
        const retry = page
          .getByRole("dialog")
          .getByRole("button", { name: "Retry same command", exact: true });
        await expect(retry).toBeEnabled();
        await expect(
          panel.getByRole("textbox", {
            name: "DNS verification reason",
            exact: true,
          }),
        ).toHaveValue(reason);
        expect(fault.attempts).toHaveLength(1);
        expect(fault.replies[0].status).toBe(200);
        const count = await queries();
        step = "retry-submit";
        await retry.click();
        await expect(page.getByRole("dialog")).toHaveCount(0);
        step = "replay-evidence";
        expect(fault.attempts).toHaveLength(2);
        expect(fault.attempts[1]).toEqual(fault.attempts[0]);
        expect(fault.replies).toHaveLength(2);
        expect(fault.replies[1]).toEqual({
          ...fault.replies[0],
          replayed: "true",
        });
        expect(await queries()).toBe(count);
        await expect(panel).toContainText("Original command result:");
      }
      await expect(page.getByRole("dialog")).toHaveCount(0);
    } catch (error) {
      console.error(
        `Publisher DNS command failed: command=${suffix} step=${step} attempts=${fault?.attempts.length ?? 0} pageClosed=${page.isClosed()}`,
      );
      if (commandResponse) {
        try {
          const value = await commandResponse.json();
          const status = value.status ?? value;
          const known = (value, choices) =>
            choices.includes(value) ? value : "other";
          console.error(
            "Publisher DNS reply shape: " +
              JSON.stringify({
                status: commandResponse.status(),
                mode: known(status.mode, [
                  "disabled",
                  "optional",
                  "required_for_approval",
                ]),
                challengeState: known(status.challengeState, [
                  "not_issued",
                  "pending",
                  "expired",
                  "consumed",
                ]),
                proofState: known(status.state, [
                  "not_verified",
                  "verified",
                  "expired",
                ]),
                hasInstructions: typeof value.value === "string",
                challengeMatches: value.challengeId === status.challengeId,
                expiryMatches: value.expiresAt === status.challengeExpiresAt,
                observedBeforeIssue:
                  Date.now() < Date.parse(status.challengeIssuedAt),
                observedBeforeExpiry:
                  Date.now() < Date.parse(status.challengeExpiresAt),
              }),
          );
        } catch {
          console.error("Publisher DNS reply shape unavailable");
        }
      }
      throw error;
    } finally {
      if (fault) replyFault.clear();
    }
  };

  phase("disabled");
  let panel = await open("disabled");
  const before = await queries();
  await expect(panel).toContainText("DNS verification is turned off");
  await expect(
    panel.getByRole("button", { name: "Create DNS challenge", exact: true }),
  ).toHaveCount(0);
  const absent = await status();
  ok(
    await request("POST", verification + ":issue", {
      body,
      key: randomUUID(),
      etag: absent.headers.get("etag"),
    }),
    404,
  );
  await approve(); // Disabled mode preserves the independent administrative decision.
  expect(await queries()).toBe(before);
  expect(errors).toEqual([]);

  phase("optional");
  panel = await open("optional");
  await expect(panel).toContainText("DNS verification is optional");
  await sendUI(panel, "Create DNS challenge", ":issue", (reply) => ({
    status: reply.status,
  }));
  phase("optional-instructions");
  const issued = ok(await request("GET", verification + "/challenge"));
  expect(issued.data.name).toBe(
    "_open-agentic-catalog-challenge.publisher.test.",
  );
  expect(issued.data.value).toMatch(
    /^open-agentic-catalog-verification=[A-Za-z0-9_-]{43}$/,
  );
  const checkKey = randomUUID();
  phase("optional-negative-check");
  const negative = await command(
    verification + ":check",
    issued.headers.get("etag"),
    body,
    checkKey,
  );
  expect(negative.data).toMatchObject({
    state: "not_verified",
    lastCheckOutcome: "not_found",
    challengeState: "pending",
  });
  expect(await queries()).toBeGreaterThan(before);
  await fixtureRequest("/publisher-dns", {
    name: issued.data.name,
    value: issued.data.value,
  });
  const afterNegative = await queries();
  phase("optional-negative-replay");
  const replay = await command(
    verification + ":check",
    issued.headers.get("etag"),
    body,
    checkKey,
  );
  expect(replay.headers.get("idempotency-replayed")).toBe("true");
  expect(replay.text).toBe(negative.text);
  expect(await queries()).toBe(afterNegative);
  phase("optional-positive-check");
  await panel
    .getByRole("button", { name: "Refresh DNS status", exact: true })
    .click();
  await expect(panel).toContainText("No TXT record was found.");
  await sendUI(panel, "Check DNS", ":check", (reply) => ({
    ...reply,
    lastCheckChallengeId: randomUUID(),
  }));
  const proof = await status();
  expect(proof.data).toMatchObject({
    state: "verified",
    challengeState: "consumed",
    lastCheckOutcome: "matched",
  });
  await expect(panel).toContainText("The expected TXT value matched.");
  // Use the existing capture size so the full administration menu is painted.
  await page.setViewportSize({ width: 1440, height: 1500 });
  await page.evaluate(() => globalThis.scrollTo(0, 0));
  await page.locator("#workspace-navigation").evaluateAll((menus) => {
    for (const menu of menus) menu.scrollTop = 0;
  });
  const accessibility = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21aa"])
    .analyze();
  if (accessibility.violations.length || accessibility.incomplete.length) {
    await mkdir(output, { recursive: true });
    const summarize = (items) =>
      items.map((item) => ({
        rule: item.id,
        nodes: item.nodes.map((node) => ({
          target: node.target,
          reason: node.failureSummary,
        })),
      }));
    await writeFile(
      join(output, "publisher-accessibility.json"),
      JSON.stringify(
        {
          violations: summarize(accessibility.violations),
          incomplete: summarize(accessibility.incomplete),
        },
        null,
        2,
      ),
    );
  }
  expect(accessibility.violations).toEqual([]);
  expect(accessibility.incomplete).toEqual([]);
  await panel.scrollIntoViewIfNeeded();
  await mkdir(output, { recursive: true });
  await panel.screenshot({
    path: join(output, "publisher-dns-verification.png"),
  });
  const afterMatch = await queries();
  phase("optional-read-controls");
  // Read, health and public discovery do not refresh verification implicitly.
  await status();
  await metadata();
  ok(await request("GET", "/livez"));
  ok(await request("GET", "/readyz"));
  ok(await request("GET", "/api/v1/capabilities"));
  ok(await request("GET", "/.well-known/ard.json"));
  ok(
    await request("POST", verification + ":check", {
      token: "local-reader",
      body,
      key: randomUUID(),
      etag: proof.headers.get("etag"),
    }),
    403,
  );
  expect(await queries()).toBe(afterMatch);
  expect(errors).toEqual([]);

  phase("restart-persistence");
  panel = await open("required_for_approval");
  const persisted = await status();
  expect(persisted.data.state).toBe("verified");
  expect(persisted.data.verifiedAt).toBe(proof.data.verifiedAt);
  expect(persisted.data.expiresAt).toBe(proof.data.expiresAt);
  expect((await metadata()).data.approvedPublicationDomains).toContain(
    "publisher.test",
  );
  const modeReplay = await command(
    verification + ":check",
    issued.headers.get("etag"),
    body,
    checkKey,
  );
  expect(modeReplay.headers.get("idempotency-replayed")).toBe("true");
  expect(modeReplay.text).toBe(negative.text);
  expect(await queries()).toBe(afterMatch);
  await sendUI(panel, "Clear DNS verification", ":clear", () => persisted.data);
  expect((await status()).data).toMatchObject({
    state: "not_verified",
    challengeState: "not_issued",
    lastCheckOutcome: "matched",
  });
  expect((await metadata()).data.approvedPublicationDomains).toContain(
    "publisher.test",
  );
  await command(
    `${publisher}/publication-domains/publisher.test:revoke`,
    (await metadata()).headers.get("etag"),
  );
  ok(
    await request(
      "POST",
      `${publisher}/publication-domains/publisher.test:approve`,
      { body, key: randomUUID(), etag: (await metadata()).headers.get("etag") },
    ),
    412,
  );
  expect(await queries()).toBe(afterMatch);
  await page.reload();
  panel = page.getByRole("region", {
    name: "Publisher DNS verification",
    exact: true,
  });
  await panel
    .getByRole("button", { name: "View DNS verification", exact: true })
    .click();
  await sendUI(panel, "Create DNS challenge", ":issue");
  const renewed = ok(await request("GET", verification + "/challenge"));
  expect(renewed.data.value).not.toBe(issued.data.value);
  await fixtureRequest("/publisher-dns", {
    name: renewed.data.name,
    value: renewed.data.value,
  });
  await sendUI(panel, "Check DNS", ":check");
  await approve();
  expect((await metadata()).data.approvedPublicationDomains).toContain(
    "publisher.test",
  );
  const finalProof = await status();
  const lookedUp = await queries();
  await command(
    `${publisher}/publication-domains/publisher.test:revoke`,
    (await metadata()).headers.get("etag"),
  );
  const removed = ok(
    await request("PATCH", publisher, {
      body: { domains: ["second.test"], reason },
      key: randomUUID(),
      etag: (await metadata()).headers.get("etag"),
    }),
  );
  ok(await request("GET", verification), 404);
  ok(
    await request("PATCH", publisher, {
      body: { domains: ["publisher.test", "second.test"], reason },
      key: randomUUID(),
      etag: removed.headers.get("etag"),
    }),
  );
  expect((await status()).data).toMatchObject({
    state: "not_verified",
    challengeState: "not_issued",
  });
  ok(
    await request("POST", verification + ":clear", {
      body,
      key: randomUUID(),
      etag: finalProof.headers.get("etag"),
    }),
    412,
  );
  expect(await queries()).toBe(lookedUp);
  expect(errors).toEqual([]);
  await fixtureRequest("/publisher-dns", {
    name: renewed.data.name,
    value: "",
  });
  phase("disabled-replay");
  await open("disabled");
  const disabledReplay = await command(
    verification + ":check",
    issued.headers.get("etag"),
    body,
    checkKey,
  );
  expect(disabledReplay.headers.get("idempotency-replayed")).toBe("true");
  expect(disabledReplay.text).toBe(negative.text);
  expect(await queries()).toBe(lookedUp);
  expect(errors).toEqual([]);
  await page.context().close();
  console.log(
    "Packaged Publisher DNS passed: disabled/optional/required, propagation, replay, restart and removal",
  );
}
