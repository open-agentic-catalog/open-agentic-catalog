// Extend the existing owned Keycloak/PostgreSQL browser gate. Tokens remain in
// memory, and failures report fixed assertions rather than provider payloads.
import { randomUUID } from "node:crypto";
import { request as httpRequest } from "node:http";
import { request as tlsRequest } from "node:https";
import { expect } from "@playwright/test";
import { personalCommandOutcome } from "./personal-evidence.mjs";

function requireFixture(condition, name) {
  if (!condition)
    throw new Error(`Owned ratings qualification failed: ${name}`);
}

// Pin provider DNS to the invocation's loopback listener and retain TLS
// verification. The product API's separate private published port is HTTP.
function exchange(url, ca, { method = "GET", headers = {}, body } = {}) {
  const target = new URL(url);
  requireFixture(
    (target.protocol === "https:" && target.hostname === "catalog-oidc") ||
      (target.protocol === "http:" && target.hostname === "127.0.0.1"),
    "owned exchange destination",
  );
  return new Promise((done, fail) => {
    const request = target.protocol === "https:" ? tlsRequest : httpRequest;
    const req = request(
      target,
      {
        ...(target.protocol === "https:"
          ? {
              ca,
              servername: target.hostname,
              lookup: (_host, options, cb) =>
                options.all
                  ? cb(null, [{ address: "127.0.0.1", family: 4 }])
                  : cb(null, "127.0.0.1", 4),
            }
          : {}),
        method,
        headers,
        timeout: 15000,
      },
      (reply) => {
        let text = "";
        let bytes = 0;
        reply.on("data", (chunk) => {
          bytes += chunk.length;
          if (bytes > 1 << 20)
            req.destroy(new Error("Owned rating reply exceeded its bound"));
          else text += chunk;
        });
        reply.on("end", () => {
          try {
            done({
              status: reply.statusCode,
              data: text ? JSON.parse(text) : undefined,
              headers: reply.headers,
            });
          } catch {
            fail(new Error("Owned rating reply is not JSON"));
          }
        });
        reply.on("error", () => fail(new Error("Owned rating reply failed")));
      },
    );
    req.on("error", () => fail(new Error("Owned rating exchange failed")));
    req.on("timeout", () => req.destroy());
    if (body) req.write(body);
    req.end();
  });
}

export async function ratingsService({ issuer, ca, secret, apiOrigin }) {
  requireFixture(
    new URL(issuer).pathname === "/realms/catalog-ratings",
    "separate rating realm",
  );
  const response = await exchange(
    issuer + "/protocol/openid-connect/token",
    ca,
    {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({
        grant_type: "client_credentials",
        client_id: "catalog-ratings-service",
        client_secret: secret,
      }).toString(),
    },
  );
  requireFixture(
    response.status === 200 && typeof response.data?.access_token === "string",
    "service credential acquisition",
  );
  const token = response.data.access_token;
  let claims;
  try {
    claims = JSON.parse(Buffer.from(token.split(".")[1], "base64url"));
  } catch {
    throw new Error("Owned service token has no readable claim set");
  }
  requireFixture(
    claims.sub === "00000000-0000-4000-8000-000000000014" &&
      claims.iss === issuer &&
      typeof claims.scope === "string" &&
      ["catalog.read", "ratings.write", "personal.write", "tenant.admin"].every(
        (scope) => claims.scope.split(" ").includes(scope),
      ),
    "issuer-issued service subject and OAuth ceilings",
  );
  return {
    subject: claims.sub,
    async request(method, path, body, etag) {
      const origin = new URL(apiOrigin());
      requireFixture(
        origin.protocol === "http:" &&
          origin.hostname === "127.0.0.1" &&
          origin.pathname === "/" &&
          path.startsWith("/api/v1/") &&
          !path.includes("#"),
        "origin-bound service request",
      );
      return exchange(new URL(path, origin), ca, {
        method,
        headers: {
          Authorization: "Bearer " + token,
          ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
          ...(method !== "GET" ? { "Idempotency-Key": randomUUID() } : {}),
          ...(etag ? { "If-Match": etag } : {}),
        },
        ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
      });
    },
  };
}

// Every browser request goes through the real cookie/fixed-upstream boundary.
const read = (page, basePath, path, method = "GET", body, etag) =>
  page.evaluate(
    async ({ route, method, body, etag }) => {
      const response = await fetch(route, {
        method,
        credentials: "same-origin",
        cache: "no-store",
        signal: AbortSignal.timeout(15000),
        headers: {
          "X-Catalog-Request": "1",
          ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
          ...(method !== "GET"
            ? { "Idempotency-Key": crypto.randomUUID() }
            : {}),
          ...(etag ? { "If-Match": etag } : {}),
        },
        ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
      });
      return {
        status: response.status,
        data: await response.json(),
        etag: response.headers.get("etag"),
        requestId: response.headers.get("x-request-id"),
      };
    },
    { route: basePath + "/_catalog" + path, method, body, etag },
  );

export async function ratingsOIDCJourney({
  page,
  origin,
  basePath,
  subject,
  service,
  onPhase,
  restartAPI,
  expectFailure,
}) {
  const at = (path) => origin + basePath + path;
  const button = (name) => page.getByRole("button", { name, exact: true });
  const command = async (name, path, status, method = "POST") => {
    await button(name).click();
    const written = page.waitForResponse(
      (response) =>
        response.request().method() === method &&
        new URL(response.url()).pathname === basePath + "/_catalog" + path,
    );
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Confirm", exact: true })
      .click();
    const response = await written;
    if (
      response.status() !== status &&
      path.startsWith("/api/v1/personal/items")
    ) {
      let problem;
      try {
        problem = await response.json();
      } catch {
        // Preserve the original status assertion even for malformed failures.
      }
      console.error(
        `Owned personal command outcome: ${JSON.stringify(
          personalCommandOutcome({
            status: response.status(),
            method,
            problem,
            requestId: response.headers()["x-request-id"],
          }),
        )}`,
      );
    }
    expect(response.status()).toBe(status);
    const receipt = await response.json();
    expect(receipt.id).toMatch(/^[0-9a-f-]{36}$/);
    expect(Number.isSafeInteger(receipt.revision)).toBe(true);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    return receipt;
  };
  onPhase("oidc-ratings-real-self");
  const me = await read(page, basePath, "/api/v1/me");
  expect(me.status).toBe(200);
  expect(me.data.scopeEnforcement).toBe(true);
  expect(me.data.actorType).toBe("human");
  expect(me.data.subject).toBe(subject);
  expect(me.data.permissions).toContain("resource.read");
  expect(me.data.permissions).toContain("ratings.own");
  expect(me.data.scopes).toEqual([
    "catalog.read",
    "personal.write",
    "ratings.write",
    "tenant.admin",
  ]);
  const serviceMe = await service.request("GET", "/api/v1/me");
  expect(serviceMe.status).toBe(200);
  expect(serviceMe.data.actorType).toBe("service");
  expect(serviceMe.data.scopes).toContain("catalog.read");
  expect(serviceMe.data.scopes).toContain("personal.write");
  expect(serviceMe.data.scopes).toContain("ratings.write");
  expect(serviceMe.data.permissions).toContain("library.own");
  expect(serviceMe.data.permissions).toContain("ratings.own");

  onPhase("oidc-personal-library");
  expect(me.data.permissions).toContain("library.own");
  await page.goto(at("/saved-items/new"));
  await page
    .getByRole("textbox", { name: "Saved label", exact: true })
    .fill("OIDC saved search");
  await page
    .getByRole("textbox", { name: "Saved search text", exact: true })
    .fill("personal-oidc-query");
  await page.getByText("More saved filters", { exact: true }).click();
  await page
    .getByRole("checkbox", { name: "Specify origins", exact: true })
    .check();
  const saved = await command(
    "Save named search",
    "/api/v1/personal/items",
    201,
  );
  await page.goto(at("/saved-items/" + saved.id));
  await expect(
    page.getByRole("heading", { name: "OIDC saved search", exact: true }),
  ).toBeVisible();
  onPhase("oidc-personal-durable-restart");
  await restartAPI();
  await page.reload();
  await expect(
    page.getByRole("textbox", { name: "Saved label", exact: true }),
  ).toHaveValue("OIDC saved search");
  const refusedLibrary = await service.request("GET", "/api/v1/personal/items");
  expect(refusedLibrary.status).toBe(403);
  const refusedSave = await service.request("POST", "/api/v1/personal/items", {
    type: "search",
    label: "Service refusal",
    search: { text: "metadata" },
  });
  expect(refusedSave.status).toBe(403);
  onPhase("oidc-personal-literal-labels");
  const literalLabel = " OIDC library lookup ";
  const owned = [];
  for (const label of [
    literalLabel,
    literalLabel.toLowerCase(),
    literalLabel,
  ]) {
    const created = await read(
      page,
      basePath,
      "/api/v1/personal/items",
      "POST",
      {
        type: "search",
        label,
        search: { text: "label qualification", origins: ["local"] },
      },
    );
    expect(created.status).toBe(201);
    expect(created.data.revision).toBe(1);
    expect(created.etag).toBeTruthy();
    owned.push({ ...created.data, etag: created.etag, label });
  }
  const selected =
    "/api/v1/personal/items?type=search&label=" +
    encodeURIComponent(literalLabel) +
    "&limit=1";
  const first = await read(page, basePath, selected);
  expect(first.status).toBe(200);
  expect(first.data.items).toHaveLength(1);
  expect(first.data.items[0].label).toBe(literalLabel);
  expect(first.data.nextCursor).toBeTruthy();
  const continuation = "&cursor=" + encodeURIComponent(first.data.nextCursor);
  const second = await read(page, basePath, selected + continuation);
  expect(second.status).toBe(200);
  expect(second.data.items).toHaveLength(1);
  expect(second.data.items[0].label).toBe(literalLabel);
  expect(second.data.nextCursor).toBeFalsy();
  expect([first.data.items[0].id, second.data.items[0].id]).toEqual(
    owned
      .filter((item) => item.label === literalLabel)
      .map((item) => item.id)
      .sort(),
  );
  const lower = await read(
    page,
    basePath,
    "/api/v1/personal/items?label=" +
      encodeURIComponent(literalLabel.toLowerCase()),
  );
  expect(lower.status).toBe(200);
  expect(lower.data.items).toHaveLength(1);
  expect(lower.data.items[0].id).toBe(owned[1].id);
  expectFailure("GET", "/_catalog/api/v1/personal/items", 400, 3);
  for (const path of [
    selected.replace("type=search", "type=resource"),
    "/api/v1/personal/items?type=search&limit=1",
    selected.replace(
      encodeURIComponent(literalLabel),
      encodeURIComponent(literalLabel.toLowerCase()),
    ),
  ]) {
    const refused = await read(page, basePath, path + continuation);
    expect(refused.status).toBe(400);
    expect(refused.data.code).toBe("INVALID_ARGUMENT");
  }
  for (const item of owned) {
    const removed = await read(
      page,
      basePath,
      "/api/v1/personal/items/" + item.id,
      "DELETE",
      undefined,
      item.etag,
    );
    expect(removed.status).toBe(200);
    expect(removed.data).toEqual({ id: item.id, revision: item.revision + 1 });
  }
  await command(
    "Remove saved item",
    "/api/v1/personal/items/" + saved.id,
    200,
    "DELETE",
  );
  await expect(
    page.getByText("Saved item removed.", { exact: true }),
  ).toBeVisible();
  const search = await read(page, basePath, "/api/v1/catalog/search", "POST", {
    kinds: ["agent"],
    limit: 3,
  });
  expect(search.status).toBe(200);
  const item = search.data.items.find((item) => item.kind === "agent");
  expect(Boolean(item?.local?.release?.id)).toBe(true);
  const releaseId = item.local.release.id;
  const record = await read(
    page,
    basePath,
    "/api/v1/catalog/resources/" + releaseId,
  );
  expect(record.status).toBe(200);
  const target = {
    origin: "local",
    resourceId: record.data.resource.id,
    versionRevisionId: record.data.versionRevision.id,
    identifier: record.data.resource.canonicalIdentifier,
    kind: record.data.resource.kind,
    versionPresent: true,
    version: record.data.version.version,
  };
  onPhase("oidc-personal-exact-resource");
  await page.goto(at("/catalog/" + releaseId));
  await page.getByText("Save resource", { exact: true }).click();
  await page
    .getByRole("textbox", { name: "Saved label", exact: true })
    .fill("OIDC exact resource");
  const savedResource = await command(
    "Save resource",
    "/api/v1/personal/items",
    201,
  );
  await page
    .getByRole("link", { name: "Open saved item", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "OIDC exact resource", exact: true }),
  ).toBeVisible();
  await page.reload();
  const exactReference = {
    origin: "local",
    releaseId,
    versionRevisionId: target.versionRevisionId,
  };
  await page
    .getByText("Edit exact resource reference", { exact: true })
    .click();
  expect(
    JSON.parse(
      await page
        .getByRole("textbox", {
          name: "Exact resource reference JSON",
          exact: true,
        })
        .inputValue(),
    ),
  ).toEqual(exactReference);
  const checkedReference = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname ===
        basePath +
          "/_catalog/api/v1/personal/items/" +
          savedResource.id +
          "/resolve",
  );
  await button("Check saved reference").click();
  const resolution = await checkedReference;
  expect(resolution.status()).toBe(200);
  expect(await resolution.json()).toEqual({
    status: "available",
    reference: exactReference,
  });
  await expect(
    page.getByRole("link", { name: "Open exact saved release", exact: true }),
  ).toHaveAttribute("href", basePath + "/catalog/" + releaseId);
  await command(
    "Remove saved item",
    "/api/v1/personal/items/" + savedResource.id,
    200,
    "DELETE",
  );
  await expect(
    page.getByText("Saved item removed.", { exact: true }),
  ).toBeVisible();
  const input = { target, releaseId, score: 4 };
  onPhase("oidc-ratings-service-refusal");
  for (const [method, path, body] of [
    ["POST", "/api/v1/ratings", input],
    ["GET", "/api/v1/ratings", undefined],
  ]) {
    const refused = await service.request(method, path, body);
    expect(refused.status).toBe(403);
    expect(refused.data.code).toBe("FORBIDDEN");
    expect(refused.data.detail).toBe(
      "an authenticated human identity is required",
    );
  }
  await page.goto(at("/catalog/" + releaseId));
  await button("Ratings for this version").click();
  const panel = page.getByRole("region", {
    name: "Ratings for this version",
    exact: true,
  });
  await expect(
    panel.getByText("No ratings yet", { exact: true }),
  ).toBeVisible();
  onPhase("oidc-ratings-submit");
  await page.getByRole("radio", { name: "4 stars", exact: true }).check();
  const created = await command("Submit rating", "/api/v1/ratings", 201);
  const ratingPath = "/api/v1/ratings/" + created.id;
  await expect(
    panel.getByText("Ratings: 4.0 / 5 (1)", { exact: true }),
  ).toBeVisible();
  onPhase("oidc-ratings-reload-persistence");
  await page.reload();
  await button("Ratings for this version").click();
  await expect(
    page.getByRole("radio", { name: "4 stars", exact: true }),
  ).toBeChecked();
  const active = await read(page, basePath, ratingPath);
  expect(active.status).toBe(200);
  expect(active.data).toMatchObject({
    id: created.id,
    target,
    score: 4,
    state: "active",
    consent: false,
    revision: created.revision,
  });
  const serviceWithdraw = await service.request(
    "POST",
    ratingPath + ":withdraw",
    {},
    active.etag,
  );
  expect(serviceWithdraw.status).toBe(403);
  expect(serviceWithdraw.data.detail).toBe(
    "an authenticated human identity is required",
  );
  onPhase("oidc-ratings-withdraw");
  const withdrawn = await command(
    "Withdraw rating",
    ratingPath + ":withdraw",
    200,
  );
  const retained = await read(page, basePath, ratingPath);
  expect(retained.status).toBe(200);
  expect(retained.data).toMatchObject({
    id: created.id,
    target,
    score: 0,
    state: "withdrawn",
    consent: false,
    revision: withdrawn.revision,
  });
  await expect(
    panel.getByText("No ratings yet", { exact: true }),
  ).toBeVisible();
  await page.getByRole("radio", { name: "3 stars", exact: true }).check();
  await command("Submit rating", "/api/v1/ratings", 200);
  await expect(
    panel.getByText("Ratings: 3.0 / 5 (1)", { exact: true }),
  ).toBeVisible();

  onPhase("oidc-ratings-owner-grant-loss");
  const assignments = await service.request(
    "GET",
    `/api/v1/access/assignments?membershipId=${me.data.membershipId}&limit=100`,
  );
  expect(assignments.status).toBe(200);
  expect(assignments.data.nextCursor).toBeUndefined();
  const activeGrants = assignments.data.items.filter(
    (item) => item.state === "active",
  );
  expect(activeGrants.length).toBeGreaterThan(0);
  expect(activeGrants.length).toBeLessThanOrEqual(3);
  for (const grant of activeGrants) {
    const revoked = await service.request(
      "POST",
      `/api/v1/access/assignments/${grant.id}:revoke`,
      {
        reason:
          "Qualify scoped owner withdrawal after read and rating grant loss",
      },
      `"rev-${grant.revision}"`,
    );
    expect(revoked.status).toBe(200);
  }
  await page.goto(at("/my-ratings"));
  const withoutGrants = await read(page, basePath, "/api/v1/me");
  expect(withoutGrants.status).toBe(200);
  expect(withoutGrants.data.permissions).toEqual([]);
  expect(withoutGrants.data.availablePermissions).toEqual([]);
  expect(withoutGrants.data.namespaceGrants).toEqual([]);
  expect(withoutGrants.data.scopes).toEqual([
    "catalog.read",
    "personal.write",
    "ratings.write",
    "tenant.admin",
  ]);
  await expect(button("Withdraw rating")).toBeEnabled();
  await page.goto(at("/my-ratings/" + created.id));
  await expect(button("Read current rating")).toBeEnabled();
  await expect(button("Withdraw rating")).toBeEnabled();
  await command("Withdraw rating", ratingPath + ":withdraw", 200);
  const afterLoss = await read(page, basePath, ratingPath);
  expect(afterLoss.status).toBe(200);
  expect(afterLoss.data).toMatchObject({
    id: created.id,
    target,
    state: "withdrawn",
    score: 0,
    consent: false,
  });
  for (const grant of activeGrants) {
    const restored = await service.request(
      "POST",
      "/api/v1/access/assignments",
      {
        membershipId: me.data.membershipId,
        roleDefinitionId: grant.roleDefinitionId,
        ...(grant.namespaceId ? { namespaceId: grant.namespaceId } : {}),
        reason:
          "Restore exact reader and library grants for the missing-write-scope control",
      },
    );
    expect(restored.status).toBe(201);
  }
  await page.goto(at("/catalog/" + releaseId));
  await button("Ratings for this version").click();
  await page.getByRole("radio", { name: "5 stars", exact: true }).check();
  await command("Submit rating", "/api/v1/ratings", 200);
  return { target, releaseId, ratingId: created.id, subject: me.data.subject };
}

export async function ratingsMissingScope({
  page,
  origin,
  basePath,
  rating,
  expectFailure,
  onPhase,
}) {
  onPhase("oidc-ratings-missing-write-scope");
  const me = await read(page, basePath, "/api/v1/me");
  expect(me.status).toBe(200);
  expect(me.data.scopeEnforcement).toBe(true);
  expect(me.data.subject).toBe(rating.subject);
  expect(me.data.permissions).toContain("ratings.own");
  expect(me.data.permissions).toContain("library.own");
  expect(me.data.scopes).toEqual(["catalog.read"]);
  await page.goto(origin + basePath + "/saved-items/new");
  await expect(
    page.getByRole("button", { name: "Save named search", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByText(
      "Saving requires a human account with library.own and personal.write when scopes are enforced.",
      { exact: true },
    ),
  ).toBeVisible();
  expectFailure("POST", "/_catalog/api/v1/personal/items", 403);
  const personalDenied = await read(
    page,
    basePath,
    "/api/v1/personal/items",
    "POST",
    { type: "search", label: "Forbidden scope", search: { text: "private" } },
  );
  expect(personalDenied.status).toBe(403);
  expect(personalDenied.data.detail).toBe(
    "OAuth scope personal.write is required",
  );
  await page.goto(origin + basePath + "/catalog/" + rating.releaseId);
  await page
    .getByRole("button", { name: "Ratings for this version", exact: true })
    .click();
  const panel = page.getByRole("region", {
    name: "Ratings for this version",
    exact: true,
  });
  await expect(
    panel.getByText("Ratings: 5.0 / 5 (1)", { exact: true }),
  ).toBeVisible();
  await expect(panel.getByRole("radio")).toHaveCount(0);
  await expect(
    panel.getByRole("button", { name: "Withdraw rating", exact: true }),
  ).toBeDisabled();
  await page.goto(origin + basePath + "/my-ratings");
  await expect(
    page.getByRole("button", { name: "Withdraw rating", exact: true }),
  ).toBeDisabled();
  await page.goto(origin + basePath + "/my-ratings/" + rating.ratingId);
  await expect(
    page.getByRole("button", { name: "Read current rating", exact: true }),
  ).toBeEnabled();
  await expect(
    page.getByRole("button", { name: "Withdraw rating", exact: true }),
  ).toBeDisabled();
  const path = "/api/v1/ratings/" + rating.ratingId;
  const before = await read(page, basePath, path);
  expect(before.status).toBe(200);
  for (const [route, body] of [
    [
      "/api/v1/ratings",
      { target: rating.target, releaseId: rating.releaseId, score: 1 },
    ],
    [path + ":withdraw", {}],
  ]) {
    expectFailure("POST", "/_catalog" + route, 403);
    const refused = await read(
      page,
      basePath,
      route,
      "POST",
      body,
      before.etag,
    );
    expect(refused.status).toBe(403);
    expect(refused.requestId).toMatch(/^\S+$/);
    expect(refused.data).toEqual({
      type: "https://catalog.example.com/problems/forbidden",
      title: "Forbidden",
      code: "FORBIDDEN",
      status: 403,
      detail: "OAuth scope ratings.write is required",
      instance: route,
      requestId: refused.requestId,
    });
  }
  const after = await read(page, basePath, path);
  expect(after.status).toBe(200);
  expect(after.data).toEqual(before.data);
}
