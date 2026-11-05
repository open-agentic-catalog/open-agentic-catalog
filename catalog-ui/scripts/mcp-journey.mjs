// Functional OAuth and MCP checks against the invocation-owned packaged API.
// Never persist tokens, provider pages, authorization codes or response bodies.
import { randomBytes, randomUUID, createHash } from "node:crypto";
import { request } from "node:https";
import { setTimeout as delay } from "node:timers/promises";
import { mcpEnvelope } from "./mcp-fixture.mjs";

function requireCheck(condition, name) {
  if (!condition) throw new Error(`MCP qualification failed: ${name}`);
}

// All DNS is pinned to this invocation's loopback listeners. TLS remains on.
function exchange(url, ca, { method = "GET", headers = {}, body } = {}) {
  const target = new URL(url);
  requireCheck(
    target.protocol === "https:" &&
      ["catalog-mcp.test", "catalog-oidc"].includes(target.hostname),
    "owned HTTPS destination",
  );
  return new Promise((done, fail) => {
    const req = request(
      target,
      {
        ca,
        servername: target.hostname,
        method,
        headers,
        timeout: 15000,
        lookup: (_host, options, cb) =>
          options.all
            ? cb(null, [{ address: "127.0.0.1", family: 4 }])
            : cb(null, "127.0.0.1", 4),
      },
      (reply) => {
        let text = "",
          bytes = 0;
        reply.on("data", (chunk) => {
          bytes += chunk.length;
          if (bytes > 1 << 20)
            req.destroy(new Error("Owned MCP reply exceeded its bound"));
          else text += chunk;
        });
        reply.on("end", () => {
          let data;
          try {
            data = text ? JSON.parse(text) : undefined;
          } catch {
            fail(new Error("Owned MCP reply is not JSON"));
            return;
          }
          done({ status: reply.statusCode, headers: reply.headers, data });
        });
        reply.on("error", fail);
      },
    );
    req.on("error", () => fail(new Error("Owned TLS exchange failed")));
    req.on("timeout", () =>
      req.destroy(new Error("Owned TLS exchange timed out")),
    );
    if (body) req.write(body);
    req.end();
  });
}

export async function mcpJourney({
  browser,
  origin,
  browserOrigin,
  issuer,
  ca,
  password,
  apiBase,
  foreignNamespace,
  onPhase,
}) {
  const checks = [];
  const check = (condition, name) => {
    requireCheck(condition, name);
    checks.push(name);
  };
  const resource = origin + "/mcp";
  const metadataURL = origin + "/.well-known/oauth-protected-resource/mcp";
  const call = (token, version, method, name, args, extra = {}) => {
    const envelope = mcpEnvelope(version, method, name, args);
    return exchange(resource, ca, {
      method: "POST",
      headers: {
        ...envelope.headers,
        ...(token ? { Authorization: "Bearer " + token } : {}),
        ...extra,
      },
      body: JSON.stringify(envelope.body),
    });
  };
  const tool = async (token, name, args = {}, version = "2026-07-28") => {
    const response = await call(token, version, "tools/call", name, args);
    requireCheck(
      response.status === 200 &&
        response.data?.result &&
        !response.data.result.isError,
      "successful tool " + name,
    );
    const result = response.data.result;
    const envelope = result.structuredContent;
    requireCheck(
      envelope?.data &&
        envelope.meta?.requestId &&
        result.content?.length === 1 &&
        result.content[0].type === "text" &&
        JSON.stringify(JSON.parse(result.content[0].text)) ===
          JSON.stringify(envelope),
      "complete Control envelope " + name,
    );
    return envelope.data;
  };
  onPhase("mcp-metadata");
  let untrusted = false;
  try {
    await exchange(metadataURL, undefined);
  } catch {
    untrusted = true;
  }
  check(untrusted, "private CA is required");
  const metadata = await exchange(metadataURL, ca, {
    headers: {
      Host: "attacker.invalid",
      "X-Forwarded-Host": "attacker.invalid",
      "X-Forwarded-Proto": "http",
    },
  });
  check(metadata.status === 200, "protected-resource metadata is available");
  check(
    metadata.data?.resource === resource,
    "canonical metadata resource ignores forwarded authority",
  );
  check(
    metadata.data?.authorization_servers?.[0] === issuer,
    "canonical metadata issuer ignores forwarded authority",
  );
  check(
    metadata.headers["cache-control"] === "no-store",
    "protected-resource metadata is not cached",
  );
  const missing = await call(
    undefined,
    "2026-07-28",
    "tools/list",
    undefined,
    undefined,
    { Host: "attacker.invalid", "X-Forwarded-Host": "attacker.invalid" },
  );
  check(
    missing.status === 401 &&
      missing.headers["www-authenticate"]?.includes(
        `resource_metadata="${metadataURL}"`,
      ),
    "canonical bearer challenge",
  );
  const cookies = await call(
    undefined,
    "2026-07-28",
    "tools/list",
    undefined,
    undefined,
    { Cookie: "catalog-ui-packaged=untrusted" },
  );
  check(cookies.status === 401, "UI cookies do not authenticate MCP");
  const preflight = await exchange(resource, ca, {
    method: "OPTIONS",
    headers: {
      Origin: browserOrigin,
      "Access-Control-Request-Method": "POST",
      "Access-Control-Request-Headers":
        "authorization,content-type,mcp-protocol-version,mcp-method,mcp-name",
    },
  });
  check(
    preflight.status === 204 &&
      preflight.headers["access-control-allow-origin"] === browserOrigin,
    "configured exact Origin preflight",
  );
  const deniedOrigin = await exchange(resource, ca, {
    method: "OPTIONS",
    headers: {
      Origin: "https://attacker.invalid",
      "Access-Control-Request-Method": "POST",
    },
  });
  check(
    deniedOrigin.status === 403 &&
      !deniedOrigin.headers["access-control-allow-origin"],
    "foreign Origin refused",
  );

  // A distinct registered public client, not the confidential UI client.
  const login = async ({
    username = "browser-human",
    client = "catalog-mcp",
    scopes = "openid catalog.read",
    realmIssuer = issuer,
    wrongVerifier = false,
  } = {}) => {
    const discovery = await exchange(
      realmIssuer + "/.well-known/openid-configuration",
      ca,
    );
    requireCheck(
      discovery.status === 200 && discovery.data.issuer === realmIssuer,
      "owned issuer discovery",
    );
    const verifier = randomBytes(32).toString("base64url");
    const state = randomBytes(24).toString("hex");
    const nonce = randomBytes(24).toString("hex");
    const redirect = origin + "/mcp-oauth/callback";
    const authorization = new URL(discovery.data.authorization_endpoint);
    for (const [key, value] of Object.entries({
      client_id: client,
      response_type: "code",
      redirect_uri: redirect,
      scope: scopes,
      resource,
      state,
      nonce,
      code_challenge: createHash("sha256").update(verifier).digest("base64url"),
      code_challenge_method: "S256",
    }))
      authorization.searchParams.set(key, value);
    const context = await browser.newContext();
    let code;
    try {
      const page = await context.newPage();
      await page.goto(authorization.href, { timeout: 30000 });
      await page.locator("#username").fill(username);
      await page.locator("#password").fill(password);
      await page.getByRole("button", { name: "Sign In", exact: true }).click();
      try {
        await page.waitForURL(
          (url) =>
            url.origin === origin && url.pathname === "/mcp-oauth/callback",
          { timeout: 30000 },
        );
      } catch {
        const location = new URL(page.url());
        console.error("Owned OAuth callback diagnostics", {
          issuerOrigin: location.origin === new URL(realmIssuer).origin,
          callbackOrigin: location.origin === origin,
          callbackPath: location.pathname === "/mcp-oauth/callback",
          requiredAction: location.pathname.endsWith(
            "/login-actions/required-action",
          ),
          loginError: (await page.locator("#input-error").count()) > 0,
          providerError: (await page.locator("#kc-error-message").count()) > 0,
          callbackError: location.searchParams.has("error"),
        });
        throw new Error("MCP qualification failed: OAuth callback not reached");
      }
      const callback = new URL(page.url());
      requireCheck(
        callback.searchParams.get("state") === state &&
          callback.searchParams.has("code") &&
          !callback.searchParams.has("error"),
        "OAuth state and callback",
      );
      code = callback.searchParams.get("code");
    } finally {
      await context.close();
    }
    const body = new URLSearchParams({
      grant_type: "authorization_code",
      client_id: client,
      code,
      redirect_uri: redirect,
      code_verifier: wrongVerifier
        ? randomBytes(32).toString("base64url")
        : verifier,
      resource,
    });
    const token = await exchange(discovery.data.token_endpoint, ca, {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: body.toString(),
    });
    if (wrongVerifier) {
      check(
        token.status === 400 && token.data?.error === "invalid_grant",
        "wrong PKCE verifier refused by issuer",
      );
      return undefined;
    }
    requireCheck(
      token.status === 200 &&
        typeof token.data.access_token === "string" &&
        typeof token.data.id_token === "string",
      "Authorization Code token exchange",
    );
    const claims = JSON.parse(
      Buffer.from(token.data.id_token.split(".")[1], "base64url"),
    );
    requireCheck(
      claims.nonce === nonce &&
        claims.iss === realmIssuer &&
        claims.aud === client,
      "OIDC nonce issuer and client binding",
    );
    return token.data;
  };
  onPhase("mcp-oauth-login");
  await login({ wrongVerifier: true });
  const tokens = await login({
    scopes: "openid catalog.read catalog.write tenant.admin",
  });
  check(
    Boolean(tokens.access_token),
    "separate public client Code S256 flow includes resource on both requests",
  );
  const accessClaims = JSON.parse(
    Buffer.from(tokens.access_token.split(".")[1], "base64url"),
  );
  check(
    accessClaims.sub === "00000000-0000-4000-8000-000000000011",
    "issuer access token binds the registered human subject",
  );
  let sampleRelease;
  for (const version of ["2026-07-28", "2025-11-25"]) {
    onPhase("mcp-reads-" + version);
    const list = await call(tokens.access_token, version, "tools/list");
    check(
      list.status === 200 &&
        list.data?.result?.tools?.length === 6 &&
        !list.headers["mcp-session-id"],
      version + " public host lists six tools without a session",
    );
    const search = await tool(
      tokens.access_token,
      "catalog_control_search",
      { limit: 10 },
      version,
    );
    check(
      search.items.length === 3 &&
        search.items.every((item) => item.type === "local"),
      version + " real PostgreSQL Search returns seeded releases",
    );
    sampleRelease ??= search.items[0].local.release.id;
    const release = await tool(
      tokens.access_token,
      "catalog_control_release",
      { releaseId: search.items[0].local.release.id },
      version,
    );
    check(
      release.release.id === search.items[0].local.release.id,
      version + " exact release read",
    );
    const facets = await tool(
      tokens.access_token,
      "catalog_control_explore",
      { facets: [{ field: "kind", limit: 10 }] },
      version,
    );
    check(
      facets.matched === 3 && facets.facets.kind.buckets.length === 3,
      version + " local facets",
    );
    const access = await tool(
      tokens.access_token,
      "catalog_control_access",
      {},
      version,
    );
    check(
      access.tenantId === "00000000-0000-7000-8000-000000000001" &&
        access.scopeEnforcement,
      version + " current access and enforced scope ceiling",
    );
  }
  const approved = await call(
    tokens.access_token,
    "2026-07-28",
    "tools/list",
    undefined,
    undefined,
    { Origin: browserOrigin },
  );
  check(
    approved.status === 200 &&
      approved.headers["access-control-allow-origin"] === browserOrigin &&
      approved.headers["access-control-expose-headers"]?.includes(
        "WWW-Authenticate",
      ),
    "approved browser Origin and exposed challenge",
  );
  // Header inspection alone cannot prove that a browser permits the read.
  // Keep credentials in memory; this context saves no trace or auth state.
  const corsContext = await browser.newContext();
  try {
    const page = await corsContext.newPage();
    await page.goto(browserOrigin + "/mcp-oauth/callback?owned=1", {
      timeout: 15000,
    });
    const envelope = mcpEnvelope("2026-07-28", "tools/list");
    const result = await page.evaluate(
      async ({ resource, envelope, token }) => {
        const send = (authenticated) =>
          fetch(resource, {
            method: "POST",
            mode: "cors",
            credentials: "omit",
            redirect: "error",
            signal: AbortSignal.timeout(15000),
            headers: {
              ...envelope.headers,
              ...(authenticated ? { Authorization: "Bearer " + token } : {}),
            },
            body: JSON.stringify(envelope.body),
          });
        const admitted = await send(true);
        const data = await admitted.json();
        const refused = await send(false);
        return {
          status: admitted.status,
          count: data.result?.tools?.length,
          refusedStatus: refused.status,
          challenge: refused.headers.get("www-authenticate"),
        };
      },
      { resource, envelope, token: tokens.access_token },
    );
    check(
      result.status === 200 && result.count === 6,
      "browser completes cross-origin authenticated MCP read",
    );
    check(
      result.refusedStatus === 401 &&
        result.challenge?.includes(`resource_metadata="${metadataURL}"`),
      "browser can read the cross-origin authentication challenge",
    );
  } finally {
    await corsContext.close();
  }
  const mismatch = await call(
    tokens.access_token,
    "2026-07-28",
    "tools/list",
    undefined,
    undefined,
    { "Mcp-Method": "tools/call" },
  );
  check(
    mismatch.status === 400,
    "mirrored method mismatch refused through proxy",
  );
  const duplicate = await call(
    tokens.access_token,
    "2026-07-28",
    "tools/list",
    undefined,
    undefined,
    { "Mcp-Protocol-Version": ["2026-07-28", "2025-11-25"] },
  );
  check(
    duplicate.status === 400,
    "duplicate protocol headers refused through proxy",
  );
  check(
    (
      await exchange(resource, ca, {
        headers: { Authorization: "Bearer " + tokens.access_token },
      })
    ).status === 405,
    "GET stream unavailable",
  );
  check(
    (
      await call(
        tokens.access_token,
        "2026-07-28",
        "tools/list",
        undefined,
        undefined,
        { "Mcp-Session-Id": "untrusted" },
      )
    ).status === 400,
    "session identifier refused",
  );

  onPhase("mcp-token-refusals");
  const wrongAudience = await login({ client: "catalog-mcp-wrong-audience" });
  const wrongIssuer = await login({
    realmIssuer: issuer.replace(/\/catalog$/, "/other-catalog"),
  });
  const unregistered = await login({ username: "unregistered-human" });
  for (const [name, token] of [
    ["wrong audience", wrongAudience.access_token],
    ["wrong issuer", wrongIssuer.access_token],
    ["ID token substitution", tokens.id_token],
    ["unregistered identity", unregistered.access_token],
  ]) {
    const response = await call(token, "2026-07-28", "tools/list");
    check(
      response.status === 401 &&
        response.headers["www-authenticate"]?.includes("invalid_token"),
      name + " refused",
    );
  }
  const noScope = await login({ scopes: "openid" });
  check(
    (await call(noScope.access_token, "2026-07-28", "tools/list")).status ===
      200,
    "authenticated tool inventory needs no resource scope",
  );
  const scopeDenied = await call(
    noScope.access_token,
    "2026-07-28",
    "tools/call",
    "catalog_control_search",
    { limit: 10 },
  );
  check(
    scopeDenied.status === 403 &&
      scopeDenied.headers["www-authenticate"]?.includes('scope="catalog.read"'),
    "missing read scope refused",
  );
  const expiring = await login({ client: "catalog-mcp-expiring" });
  check(
    (await call(expiring.access_token, "2026-07-28", "tools/list")).status ===
      200,
    "short-lived access token starts valid",
  );
  const expiry = JSON.parse(
    Buffer.from(expiring.access_token.split(".")[1], "base64url"),
  ).exp;
  const wait = expiry * 1000 - Date.now() + 1500;
  requireCheck(wait > 0 && wait < 20000, "bounded token expiry");
  await delay(wait);
  check(
    (await call(expiring.access_token, "2026-07-28", "tools/list")).status ===
      401,
    "expired access token refused",
  );

  onPhase("mcp-retained-permissions");
  const api = async (path, body, revision) => {
    const response = await fetch(apiBase + "/api/v1" + path, {
      method: body ? "POST" : "GET",
      signal: AbortSignal.timeout(15000),
      redirect: "error",
      headers: {
        Authorization: "Bearer " + tokens.access_token,
        ...(body
          ? {
              "Content-Type": "application/json",
              "Idempotency-Key": randomUUID(),
            }
          : {}),
        ...(revision ? { "If-Match": `"rev-${revision}"` } : {}),
      },
      ...(body ? { body: JSON.stringify(body) } : {}),
    });
    requireCheck(
      response.ok,
      "owned permission fixture command " + path.split("?")[0],
    );
    return response.status === 204 ? undefined : response.json();
  };
  const memberships = await api("/memberships?limit=100");
  const member = memberships.items.find(
    (item) => item.subject === "00000000-0000-4000-8000-000000000013",
  );
  requireCheck(
    member?.id && !memberships.nextCursor,
    "owned reader membership",
  );
  const assignments = await api(
    `/access/assignments?membershipId=${member.id}&limit=100`,
  );
  requireCheck(!assignments.nextCursor, "bounded bootstrap assignments");
  for (const row of assignments.items.filter((item) => item.state === "active"))
    await api(
      `/access/assignments/${row.id}:revoke`,
      { reason: "Owned MCP namespace control" },
      row.revision,
    );
  const roles = await api("/access/roles?limit=100");
  const role = roles.items.find((item) => item.key === "reader");
  const namespaces = await api("/namespaces?limit=100");
  const namespace = namespaces.items.find(
    (item) => item.slug === "catalog-demo-v1",
  );
  requireCheck(
    role?.id && namespace?.id && !roles.nextCursor && !namespaces.nextCursor,
    "owned reader namespace and role",
  );
  const localDenied = await api("/namespaces", {
    slug: "mcp-private",
    displayName: "Owned private namespace",
  });
  const unrelatedGrant = await api("/access/assignments", {
    membershipId: member.id,
    roleDefinitionId: role.id,
    namespaceId: localDenied.id,
    reason: "Owned MCP wrong namespace control",
  });
  const reader = await login({ username: "mcp-reader" });
  const refused = await call(
    reader.access_token,
    "2026-07-28",
    "tools/call",
    "catalog_control_release",
    { releaseId: sampleRelease },
  );
  check(
    refused.status === 200 &&
      refused.data.result?.isError &&
      refused.data.result.content[0]?.text.startsWith("api_denied:"),
    "grant in another namespace cannot read a known published release",
  );
  await api(
    `/access/assignments/${unrelatedGrant.id}:revoke`,
    { reason: "Owned MCP namespace transition" },
    unrelatedGrant.revision,
  );
  const assignment = await api("/access/assignments", {
    membershipId: member.id,
    roleDefinitionId: role.id,
    namespaceId: namespace.id,
    reason: "Owned MCP exact namespace grant",
  });
  const allowed = await tool(reader.access_token, "catalog_control_search", {
    namespaceId: namespace.id,
    limit: 10,
  });
  check(allowed.items.length === 3, "namespace grant admits own releases");
  const foreign = await tool(reader.access_token, "catalog_control_search", {
    namespaceId: foreignNamespace,
    limit: 10,
  });
  check(
    foreign.items.length === 0 && foreign.durableTotal === 0,
    "cross-tenant namespace returns no private resources",
  );
  const exact = await tool(reader.access_token, "catalog_control_release", {
    releaseId: sampleRelease,
  });
  check(
    exact.release.id === sampleRelease,
    "same access token sees the newly granted exact namespace",
  );
  await api(
    `/access/assignments/${assignment.id}:revoke`,
    { reason: "Owned MCP revocation control" },
    assignment.revision,
  );
  const revoked = await tool(reader.access_token, "catalog_control_search", {
    namespaceId: namespace.id,
    limit: 10,
  });
  check(
    revoked.items.length === 0,
    "same access token observes current grant revocation",
  );
  await api(
    `/memberships/${member.id}:suspend`,
    { reason: "Owned MCP membership control" },
    member.revision,
  );
  check(
    (await call(reader.access_token, "2026-07-28", "tools/list")).status ===
      401,
    "same access token observes membership suspension",
  );
  return checks;
}

export async function mcpDisabled(origin, ca) {
  for (const [path, method] of [
    ["/mcp", "POST"],
    ["/.well-known/oauth-protected-resource/mcp", "GET"],
  ]) {
    const response = await exchange(origin + path, ca, {
      method,
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
      },
      ...(method === "POST" ? { body: "{}" } : {}),
    });
    requireCheck(response.status === 404, "disabled " + path);
  }
  return ["disabled transport and metadata return 404"];
}
