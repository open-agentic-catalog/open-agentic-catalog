import test from "node:test";
import { listenFixture } from "../../../test/vscode/listen.mjs";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { connect as tcpConnect } from "node:net";
import { createServer as httpsServer } from "node:https";
import { createSecureServer as http2Server } from "node:http2";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFileSync } from "node:child_process";
import { pathToFileURL } from "node:url";
import { gzipSync } from "node:zlib";
import {
  generateKeyPair,
  exportJWK,
  SignJWT,
  jwtVerify,
} from "../../../extensions/vscode/node_modules/jose/dist/webapi/index.js";
const {
  Browser,
  connections,
  Session,
  transport,
  executionHost,
  credentialAccess,
  issuerJSON,
} = await import(pathToFileURL(process.env.CATALOG_VSCODE_LIB));
const subject = "local-reader";
const me = {
  tenantId: "00000000-0000-7000-8000-000000000001",
  realmId: "00000000-0000-7000-8000-000000000002",
  membershipId: "00000000-0000-7000-8000-000000000003",
  subject,
  permissions: ["catalog.read"],
  availablePermissions: ["catalog.read"],
};
const json = (response, value, status = 200) => {
  response.writeHead(status, { "Content-Type": "application/json" });
  response.end(JSON.stringify(value));
};
async function listen(server, t) {
  await listenFixture(server);
  t.after(
    () =>
      new Promise((resolve) => {
        server.closeAllConnections();
        server.close(resolve);
      }),
  );
  return `http://127.0.0.1:${server.address().port}`;
}
function memory() {
  const data = new Map();
  return {
    data,
    get: async (k) => data.get(k),
    store: async (k, v) => {
      data.set(k, v);
    },
    delete: async (k) => {
      data.delete(k);
    },
  };
}

async function bounded(promise, label) {
  let timer;
  try {
    return await Promise.race([
      promise,
      new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error(label)), 3000);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

test("switching Catalog origins closes an old response and preserves the new verified session", async (t) => {
  const entered = Promise.withResolvers(),
    closed = Promise.withResolvers(),
    seen = [];
  let oldResponse;
  const server = (name, token) =>
    createServer((req, res) => {
      seen.push({
        name,
        path: req.url,
        authorization: req.headers.authorization,
      });
      res.setHeader("content-type", "application/json");
      if (req.headers.authorization !== `Bearer ${token}`) {
        res.writeHead(401);
        res.end("{}");
        return;
      }
      if (name === "old" && req.url === "/api/v1/catalog/resources") {
        oldResponse = res;
        res.once("close", closed.resolve);
        res.flushHeaders();
        res.write('{"private":"old unfinished bytes');
        entered.resolve();
        return;
      }
      res.end(
        JSON.stringify(
          req.url === "/api/v1/me"
            ? { ...me, subject: `${name}-reader` }
            : { resourceKinds: ["agent"] },
        ),
      );
    });
  const oldOrigin = await listen(server("old", "old-origin-token"), t);
  const newOrigin = await listen(server("new", "new-origin-token"), t);
  const definitions = connections([
    {
      id: "old",
      label: "Old",
      origin: oldOrigin,
      mode: "control",
      auth: "manual",
      allowLoopbackHTTP: true,
      timeoutMs: 5000,
    },
    {
      id: "new",
      label: "New",
      origin: newOrigin,
      mode: "control",
      auth: "manual",
      allowLoopbackHTTP: true,
      timeoutMs: 5000,
    },
  ]);
  const secrets = memory(),
    session = new Session(secrets, "origin-switch", transport);
  let rejected;
  t.after(async () => {
    await session.dispose();
    await Promise.allSettled(rejected ? [rejected] : []);
  });
  await session.select(definitions[0]);
  await session.manual("old-origin-token");
  assert.equal((await session.test()).identity.subject, "old-reader");
  const originalSecret = secrets.data.get(
    "openAgenticCatalog:origin-switch:old",
  );
  assert.equal(typeof originalSecret, "string");
  assert.ok(originalSecret.length > 0);
  const oldGuard = session.guard(definitions[0]);
  const oldSignal = session.cancellation();
  const pending = session.read("/api/v1/catalog/resources");
  rejected = assert.rejects(pending, { code: "connection_changed" });
  await bounded(
    Promise.race([entered.promise, pending]),
    "old response did not start",
  );
  assert.equal(oldSignal.aborted, false);
  await session.select(definitions[1]);
  await rejected;
  await bounded(closed.promise, "old Catalog response did not close");
  assert.equal(oldResponse.destroyed, true);
  assert.equal(oldSignal.aborted, true);
  assert.throws(oldGuard, { code: "connection_changed" });
  assert.equal(session.diagnostics(), undefined);
  await session.manual("new-origin-token");
  assert.equal((await session.test()).identity.subject, "new-reader");
  const newGuard = session.guard(definitions[1]),
    newSignal = session.cancellation();
  assert.equal((await session.json("/api/v1/me")).subject, "new-reader");
  newGuard();
  assert.equal(newSignal.aborted, false);
  assert.equal(session.snapshot().identity.subject, "new-reader");
  assert.equal(session.diagnostics(), undefined);
  assert.equal(
    secrets.data.get("openAgenticCatalog:origin-switch:old"),
    originalSecret,
  );
  assert.equal(secrets.data.size, 2);
  assert.deepEqual(
    seen.map(({ name, path }) => ({ name, path })),
    [
      { name: "old", path: "/api/v1/me" },
      { name: "old", path: "/api/v1/capabilities" },
      { name: "old", path: "/api/v1/catalog/resources" },
      { name: "new", path: "/api/v1/me" },
      { name: "new", path: "/api/v1/capabilities" },
      { name: "new", path: "/api/v1/me" },
    ],
  );
  assert.ok(
    seen.every(
      ({ name, authorization }) =>
        authorization === `Bearer ${name}-origin-token`,
    ),
  );
});

test("real issuer HTTP preserves exact chunked UTF-8 bounds and closes oversized metadata and token bodies", async (t) => {
  const value = { label: "Café 😀" },
    prefix = Buffer.from(JSON.stringify(value)),
    requests = [],
    closures = new Map();
  const server = createServer(async (req, res) => {
    let body = "";
    for await (const chunk of req) body += chunk;
    requests.push({
      path: req.url,
      method: req.method,
      body,
      accept: req.headers.accept,
      encoding: req.headers["accept-encoding"],
      contentType: req.headers["content-type"],
      authorization: req.headers.authorization,
      cookie: req.headers.cookie,
    });
    const token = req.url.startsWith("/token"),
      limit = token ? 65536 : 262144;
    res.writeHead(200, { "content-type": "application/json" });
    if (req.url.endsWith("-over")) {
      res.once("close", () => closures.get(req.url).resolve());
      res.write(
        Buffer.concat([prefix, Buffer.alloc(limit + 1 - prefix.length, " ")]),
      );
      return;
    }
    // No Content-Length: split UTF-8 bytes across chunked response writes.
    // Fetch may coalesce those writes into a different reader chunk layout.
    const boundary = prefix.indexOf(0xc3) + 1;
    res.write(prefix.subarray(0, boundary));
    res.write(prefix.subarray(boundary));
    res.end(Buffer.alloc(limit - prefix.length, " "));
  });
  const origin = await listen(server, t),
    connection = connections([
      {
        id: "wire-issuer",
        label: "Owned issuer",
        origin,
        mode: "control",
        auth: "manual",
        allowLoopbackHTTP: true,
      },
    ])[0],
    client = await transport(connection);
  t.after(() => client.close());
  for (const kind of ["metadata", "token"]) {
    const body =
      kind === "token"
        ? new URLSearchParams({
            grant_type: "authorization_code",
            code: "wire-code:once",
            client_id: "public-client",
          })
        : undefined;
    const read = (suffix) =>
      issuerJSON(
        client.fetch,
        `${origin}/${kind}${suffix}`,
        AbortSignal.timeout(3000),
        body,
      );
    assert.deepEqual(await read(""), value);
    const closed = Promise.withResolvers();
    closures.set(`/${kind}-over`, closed);
    await assert.rejects(read("-over"), { code: "issuer_response_too_large" });
    await bounded(closed.promise, "oversized issuer response did not close");
    assert.deepEqual(
      await read(""),
      value,
      "a refused body must leave subsequent explicit issuer reads usable",
    );
  }
  assert.equal(requests.length, 6);
  for (const request of requests) {
    const token = request.path.startsWith("/token");
    assert.equal(request.method, token ? "POST" : "GET");
    assert.equal(
      request.body,
      token
        ? "grant_type=authorization_code&code=wire-code%3Aonce&client_id=public-client"
        : "",
    );
    assert.equal(request.accept, "application/json");
    assert.equal(request.encoding, "identity");
    assert.equal(
      request.contentType,
      token ? "application/x-www-form-urlencoded" : undefined,
    );
    assert.equal(request.authorization, undefined);
    assert.equal(request.cookie, undefined);
  }
});

test("real issuer HTTP refuses encoded data and redirects before disclosing a token form to another peer", async (t) => {
  const value = { label: "Café 😀" },
    bytes = Buffer.from(JSON.stringify(value)),
    targetRequests = [],
    requests = [],
    closures = new Map();
  const target = createServer((req, res) => {
    targetRequests.push({ path: req.url, method: req.method });
    json(res, value);
  });
  const targetOrigin = await listen(target, t);
  const server = createServer(async (req, res) => {
    let body = "";
    for await (const chunk of req) body += chunk;
    requests.push({ path: req.url, method: req.method, body });
    if (req.url === "/valid") return json(res, value);
    if (req.url === "/invalid-utf8") {
      res.writeHead(200, { "content-type": "application/json" });
      return res.end(
        Buffer.concat([
          Buffer.from('{"label":"'),
          Buffer.from([0xc3, 0x28]),
          Buffer.from('"}'),
        ]),
      );
    }
    res.once("close", () => closures.get(req.url).resolve());
    if (req.url === "/encoded") {
      res.writeHead(200, {
        "content-type": "application/json",
        "content-encoding": "gzip",
      });
      res.write(gzipSync(bytes));
      return;
    }
    res.writeHead(307, {
      "content-type": "application/json",
      location: targetOrigin + "/redirected",
    });
    res.write(bytes);
  });
  const origin = await listen(server, t),
    connection = connections([
      {
        id: "wire-refusal",
        label: "Owned issuer refusal",
        origin,
        mode: "control",
        auth: "manual",
        allowLoopbackHTTP: true,
      },
    ])[0],
    client = await transport(connection);
  t.after(() => client.close());
  // Calibrate both actual peers before asserting that a redirect contacts none.
  assert.deepEqual(
    await issuerJSON(
      client.fetch,
      targetOrigin + "/calibrate",
      AbortSignal.timeout(3000),
    ),
    value,
  );
  assert.deepEqual(targetRequests, [{ path: "/calibrate", method: "GET" }]);
  targetRequests.length = 0;
  assert.deepEqual(
    await issuerJSON(
      client.fetch,
      origin + "/valid",
      AbortSignal.timeout(3000),
    ),
    value,
  );
  for (const [path, body, expected] of [
    ["/encoded", undefined, { code: "issuer_refused" }],
    ["/invalid-utf8", undefined, { code: "invalid_issuer_response" }],
    ["/redirect-get", undefined, { name: "TypeError" }],
    [
      "/redirect-post",
      new URLSearchParams({
        code: "private-wire-code",
        grant_type: "authorization_code",
      }),
      { name: "TypeError" },
    ],
  ]) {
    const closed = Promise.withResolvers();
    closures.set(path, closed);
    await assert.rejects(
      issuerJSON(client.fetch, origin + path, AbortSignal.timeout(3000), body),
      expected,
    );
    if (path !== "/invalid-utf8")
      await bounded(closed.promise, "refused issuer response did not close");
    assert.deepEqual(
      targetRequests,
      [],
      "a refused redirect cannot forward the code or even contact another peer",
    );
    assert.deepEqual(
      await issuerJSON(
        client.fetch,
        origin + "/valid",
        AbortSignal.timeout(3000),
      ),
      value,
    );
  }
  const exchange = requests.find(
    (request) => request.path === "/redirect-post",
  );
  assert.deepEqual(exchange, {
    path: "/redirect-post",
    method: "POST",
    body: "code=private-wire-code&grant_type=authorization_code",
  });
});

test("real local OIDC: PKCE, nonce, signature, audience, rotation and concurrent refresh", async (t) => {
  const { publicKey, privateKey } = await generateKeyPair("RS256");
  const jwk = {
    ...(await exportJWK(publicKey)),
    kid: "local-signing-key",
    alg: "RS256",
  };
  const codes = new Map();
  let exchanges = 0,
    refreshes = 0,
    apiReads = 0;
  let nonceMode = "correct",
    refreshIdentity = "same",
    accessAudience = "catalog",
    tokenSubject = subject,
    expiry = 3600,
    refuseRefresh = false,
    refreshRequested = true,
    includeRefresh = true;
  let originalNonce;
  let heldKeys, keysReached;
  const authTime = Math.floor(Date.now() / 1000) - 10;
  let issuer;
  const server = createServer(async (req, res) => {
    try {
      const url = new URL(req.url, issuer);
      if (url.pathname === "/.well-known/openid-configuration")
        return json(res, {
          issuer,
          authorization_endpoint: issuer + "/authorize",
          token_endpoint: issuer + "/token",
          jwks_uri: issuer + "/keys",
          response_types_supported: ["code"],
          code_challenge_methods_supported: ["S256"],
        });
      if (url.pathname === "/keys") {
        if (heldKeys) {
          keysReached();
          await heldKeys;
        }
        return json(res, { keys: [jwk] });
      }
      if (url.pathname === "/authorize") {
        assert.equal(url.searchParams.get("code_challenge_method"), "S256");
        assert.equal(
          url.searchParams.get("scope"),
          refreshRequested
            ? "openid catalog.read offline_access"
            : "openid catalog.read",
        );
        assert.equal(
          url.searchParams.get("prompt"),
          refreshRequested ? "consent" : null,
        );
        const code = `code-${codes.size}`;
        codes.set(code, Object.fromEntries(url.searchParams));
        const callback = new URL(url.searchParams.get("redirect_uri"));
        callback.searchParams.set("code", code);
        callback.searchParams.set("state", url.searchParams.get("state"));
        callback.searchParams.set("iss", issuer);
        res.writeHead(302, { location: callback.href });
        return res.end();
      }
      if (url.pathname === "/token") {
        let bytes = "";
        for await (const chunk of req) bytes += chunk;
        const form = new URLSearchParams(bytes);
        if (form.get("grant_type") === "authorization_code") {
          exchanges++;
          const code = codes.get(form.get("code"));
          if (!code) return json(res, { error: "invalid_grant" }, 400);
          codes.delete(form.get("code"));
          const { createHash } = await import("node:crypto");
          assert.equal(
            createHash("sha256")
              .update(form.get("code_verifier"))
              .digest("base64url"),
            code.code_challenge,
          );
          assert.equal(form.get("redirect_uri"), code.redirect_uri);
          originalNonce = code.nonce;
          const id = await new SignJWT({
            nonce: nonceMode === "correct" ? code.nonce : "wrong",
            auth_time: authTime,
          })
            .setProtectedHeader({ alg: "RS256", kid: jwk.kid })
            .setIssuer(issuer)
            .setAudience("extension-public")
            .setSubject(subject)
            .setIssuedAt()
            .setExpirationTime("1h")
            .sign(privateKey);
          const access = await new SignJWT({})
            .setProtectedHeader({ alg: "RS256", kid: jwk.kid })
            .setIssuer(issuer)
            .setAudience(accessAudience)
            .setSubject(tokenSubject)
            .setIssuedAt()
            .setExpirationTime(expiry + "s")
            .sign(privateKey);
          return json(res, {
            access_token: access,
            id_token: id,
            token_type: "Bearer",
            expires_in: expiry,
            ...(includeRefresh ? { refresh_token: "refresh-initial" } : {}),
          });
        }
        refreshes++;
        assert.equal(form.get("refresh_token"), "refresh-initial");
        if (refuseRefresh) return json(res, { error: "invalid_grant" }, 400);
        const access = await new SignJWT({})
          .setProtectedHeader({ alg: "RS256", kid: jwk.kid })
          .setIssuer(issuer)
          .setAudience("catalog")
          .setSubject(tokenSubject)
          .setIssuedAt()
          .setExpirationTime("1h")
          .sign(privateKey);
        const id = await new SignJWT({
          nonce: refreshIdentity === "nonce" ? "another-login" : originalNonce,
          auth_time: refreshIdentity === "time" ? authTime + 1 : authTime,
          ...(refreshIdentity === "audience"
            ? { azp: "extension-public" }
            : {}),
        })
          .setProtectedHeader({ alg: "RS256", kid: jwk.kid })
          .setIssuer(issuer)
          .setAudience(
            refreshIdentity === "audience"
              ? ["extension-public", "untrusted"]
              : "extension-public",
          )
          .setSubject(subject)
          .setIssuedAt()
          .setExpirationTime("1h")
          .sign(privateKey);
        return json(res, {
          access_token: access,
          id_token: id,
          token_type: "Bearer",
          expires_in: 3600,
          refresh_token: "refresh-rotated",
        });
      }
      if (url.pathname === "/api/v1/me") {
        apiReads++;
        assert.match(req.headers.authorization, /^Bearer ey/u);
        return json(res, me);
      }
      if (url.pathname === "/api/v1/capabilities") {
        apiReads++;
        return json(res, {
          resourceKinds: ["agent", "mcp_server", "skill", "plugin"],
        });
      }
      return json(res, {}, 404);
    } catch {
      json(res, { error: "fixture_assertion" }, 500);
    }
  });
  issuer = await listen(server, t);
  const connection = connections([
    {
      id: "oidc",
      label: "Local OIDC",
      origin: issuer,
      mode: "control",
      auth: "oidc",
      allowLoopbackHTTP: true,
      oidc: {
        issuer,
        clientId: "extension-public",
        audience: "catalog",
        scopes: ["openid", "catalog.read"],
        refresh: true,
      },
    },
  ])[0];
  const secrets = memory();
  const hostDirectory = await mkdtemp(join(tmpdir(), "catalog-oidc-host-"));
  t.after(() => rm(hostDirectory, { recursive: true, force: true }));
  const session = new Session(
    secrets,
    await executionHost(hostDirectory, "local"),
    transport,
    credentialAccess(hostDirectory),
  );
  t.after(() => session.dispose());
  await session.select(connection);
  async function callback() {
    const auth = await session.startLogin(
      "vscode://open-agentic-catalog.open-agentic-catalog/oidc",
    );
    const response = await fetch(auth, { redirect: "manual" });
    return response.headers.get("location");
  }
  const first = await callback();
  await session.callback(first);
  assert.equal((await session.test()).identity.subject, subject);
  assert.equal(exchanges, 1);
  await assert.rejects(session.callback(first), /no_pending_login/u);
  assert.equal(exchanges, 1);
  assert.match([...secrets.data.values()][0], /refresh-initial/u);
  // Reload the actual issuer credential, then deliver an unchanged notification.
  await session.select(connection);
  assert.equal((await session.test()).identity.subject, subject);
  const restoredGuard = session.guard(connection);
  await session.secretsChanged();
  restoredGuard();
  assert.equal(session.snapshot().identity.subject, subject);
  assert.equal(
    exchanges,
    1,
    "credential reload must not exchange another code",
  );
  await session.signOut();
  assert.equal(secrets.data.size, 0);
  const forged = await callback();
  await assert.rejects(
    session.callback(forged.replace(/state=[^&]+/u, "state=forged")),
    /invalid_login_callback/u,
  );
  assert.equal(exchanges, 1);
  for (const mode of ["nonce", "audience", "subject"]) {
    nonceMode = mode === "nonce" ? "wrong" : "correct";
    accessAudience = mode === "audience" ? "another" : "catalog";
    tokenSubject = mode === "subject" ? "other-reader" : subject;
    await assert.rejects(
      session.callback(await callback()),
      /token_verification_failed/u,
    );
    assert.equal(secrets.data.size, 0);
  }
  nonceMode = "correct";
  accessAudience = "catalog";
  tokenSubject = subject;
  expiry = 30;
  await session.callback(await callback());
  await session.select(connection);
  const verificationReached = new Promise((resolve) => {
    keysReached = resolve;
  });
  let releaseVerification;
  heldKeys = new Promise((resolve) => {
    releaseVerification = resolve;
  });
  const caller = new AbortController();
  const canceledReader = assert.rejects(
    session.test(caller.signal),
    /request_cancelled/u,
  );
  const browser = new Browser(
    session,
    { get: () => undefined, update: async () => {} },
    "owned-browser",
  );
  t.after(() => browser.dispose());
  const canceledBrowse = assert.rejects(browser.search(), {
    code: "request_canceled",
  });
  const timedReader = assert.rejects(session.test(AbortSignal.timeout(20)), {
    code: "request_timeout",
  });
  const remainingReaders = Promise.all([session.test(), session.test()]);
  const readsBeforeRotation = apiReads;
  try {
    await Promise.race([
      verificationReached,
      remainingReaders.then(() =>
        assert.fail("refresh verification was not held"),
      ),
    ]);
    caller.abort();
    await canceledReader;
    browser.cancel();
    await canceledBrowse;
    await timedReader;
    assert.equal(browser.state.busy, false);
    assert.equal(browser.state.error, "request_canceled");
    assert.equal(apiReads, readsBeforeRotation);
    assert.equal(session.cancellation().aborted, false);
    assert.equal(
      secrets.data.size,
      1,
      "reader cancellation must not delete login",
    );
  } finally {
    releaseVerification();
    heldKeys = undefined;
  }
  for (const result of await remainingReaders)
    assert.equal(result.identity.subject, subject);
  assert.equal(refreshes, 1);
  assert.match([...secrets.data.values()][0], /refresh-rotated/u);
  const stored = JSON.parse([...secrets.data.values()][0]);
  assert.equal(stored.nonce, originalNonce);
  assert.equal(stored.authTime, authTime);
  await session.select(connection);
  assert.equal((await session.test()).identity.subject, subject);
  const rotatedGuard = session.guard(connection);
  await session.secretsChanged();
  rotatedGuard();
  assert.equal(session.snapshot().identity.subject, subject);
  assert.equal(
    refreshes,
    1,
    "rotated credential reload must not refresh again",
  );
  await session.signOut();
  const readsBeforeRefusal = apiReads;
  for (const mode of ["nonce", "time", "audience"]) {
    refreshIdentity = mode;
    await session.callback(await callback());
    await assert.rejects(session.test(), /sign_in_required/u);
    assert.equal(secrets.data.size, 0);
    assert.equal(session.snapshot(), undefined);
  }
  assert.equal(
    apiReads,
    readsBeforeRefusal,
    "refused refresh must not reach the Catalog API",
  );
  refreshIdentity = "same";
  await session.callback(await callback());
  tokenSubject = "changed-during-refresh";
  await assert.rejects(session.test(), /sign_in_required/u);
  assert.equal(secrets.data.size, 0);
  tokenSubject = subject;
  await session.callback(await callback());
  refuseRefresh = true;
  await assert.rejects(session.test(), /sign_in_required/u);
  assert.equal(secrets.data.size, 0);
  refuseRefresh = false;
  const beforeShort = refreshes;
  for (const refresh of [false, true]) {
    refreshRequested = refresh;
    includeRefresh = !refresh;
    const short = { ...connection, oidc: { ...connection.oidc, refresh } };
    await session.select(short);
    await session.callback(await callback());
    assert.equal((await session.test()).identity.subject, subject);
    await session.select(short);
    assert.equal((await session.test()).identity.subject, subject);
    assert.equal(
      refreshes,
      beforeShort,
      "a valid signed 30-second token without usable refresh must not be shortened",
    );
    await session.signOut();
    assert.equal(secrets.data.size, 0);
  }
  // Let a real signed credential expire; no forged cache or test clock is used.
  expiry = 5;
  refreshRequested = false;
  includeRefresh = false;
  const nonRefreshing = {
    ...connection,
    oidc: { ...connection.oidc, refresh: false },
  };
  await session.select(nonRefreshing);
  await session.callback(await callback());
  assert.equal((await session.test()).identity.subject, subject);
  const credential = JSON.parse([...secrets.data.values()][0]);
  const { payload } = await jwtVerify(credential.accessToken, publicKey, {
    issuer,
    audience: "catalog",
  });
  assert.equal(credential.expiresAt, payload.exp * 1000);
  const deadline = performance.now() + 10000;
  while (Date.now() < payload.exp * 1000) {
    assert.ok(performance.now() < deadline, "signed token expiry deadline");
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  const readsAtExpiry = apiReads,
    refreshesAtExpiry = refreshes;
  await assert.rejects(session.test(), /sign_in_required/u);
  assert.equal(
    apiReads,
    readsAtExpiry,
    "expired credentials must not reach the API",
  );
  assert.equal(
    refreshes,
    refreshesAtExpiry,
    "expiry must not cause an implicit exchange",
  );
  assert.equal(secrets.data.size, 0);
  assert.equal(session.snapshot(), undefined);
  expiry = 3600;
  await session.callback(await callback());
  assert.equal((await session.test()).identity.subject, subject);
  await session.signOut();
});

test("real TLS: HTTP/1.1 ALPN, private CA, hostname validation and credential-free proxy CONNECT", async (t) => {
  const work = await mkdtemp(join(tmpdir(), "catalog-extension-tls-"));
  t.after(() => rm(work, { recursive: true, force: true }));
  execFileSync(
    "openssl",
    [
      "req",
      "-x509",
      "-newkey",
      "rsa:2048",
      "-nodes",
      "-days",
      "1",
      "-subj",
      "/CN=localhost",
      "-addext",
      "subjectAltName=DNS:localhost,IP:127.0.0.1",
      "-keyout",
      join(work, "key"),
      "-out",
      join(work, "cert"),
    ],
    { stdio: "ignore", timeout: 15000 },
  );
  const targetProtocols = [];
  const proxyProtocols = [];
  const dualProtocolServer = (options, handler, protocols) => {
    const sockets = new Set();
    const peer = http2Server(
      { ...options, allowHTTP1: true, ALPNProtocols: ["h2", "http/1.1"] },
      handler,
    );
    peer.on("connection", (socket) => {
      sockets.add(socket);
      socket.once("close", () => sockets.delete(socket));
    });
    peer.on("secureConnection", (socket) =>
      protocols.push(socket.alpnProtocol),
    );
    peer.closeAllConnections = () => {
      for (const socket of sockets) socket.destroy();
    };
    return peer;
  };
  let headerSize = 20 * 1024;
  let streamClosed;
  const closedStream = new Promise((resolve) => {
    streamClosed = resolve;
  });
  const server = dualProtocolServer(
    {
      key: await readFile(join(work, "key")),
      cert: await readFile(join(work, "cert")),
    },
    (req, res) => {
      if (req.url === "/stream") {
        res.writeHead(200, { "Content-Type": "text/plain" });
        res.write("started\n");
        res.once("close", streamClosed);
        return;
      }
      res.setHeader("X-Fixture-Padding", "x".repeat(headerSize));
      json(res, req.url === "/api/v1/me" ? me : { resourceKinds: ["agent"] });
    },
    targetProtocols,
  );
  await listen(server, t);
  const origin = `https://127.0.0.1:${server.address().port}`;
  const connection = connections([
    {
      id: "tls",
      label: "TLS",
      origin,
      mode: "control",
      auth: "manual",
      caFile: join(work, "cert"),
    },
  ])[0];
  const session = new Session(memory(), "tls", transport);
  t.after(() => session.dispose());
  await session.select(connection);
  await session.manual("synthetic-token");
  assert.equal((await session.test()).identity.subject, subject);
  const direct = await transport(connection);
  t.after(() => direct.close());
  const url = origin + "/api/v1/me";
  for (const input of [url, new URL(url), new Request(url)]) {
    const response = await direct.fetch(input);
    assert.equal((await response.json()).subject, subject);
  }
  const controller = new AbortController();
  const streaming = await direct.fetch(origin + "/stream", {
    signal: controller.signal,
  });
  controller.abort();
  await assert.rejects(streaming.text(), { name: "AbortError" });
  await closedStream;
  assert.equal((await (await direct.fetch(url)).json()).subject, subject);
  assert.deepEqual(new Set(targetProtocols), new Set(["http/1.1"]));
  const untrusted = new Session(memory(), "tls", transport);
  t.after(() => untrusted.dispose());
  await untrusted.select({ ...connection, caFile: undefined });
  await untrusted.manual("synthetic-token");
  await assert.rejects(untrusted.test(), /request_failed/u);
  for (const secure of [false, true]) {
    let connects = 0;
    const sockets = new Set();
    const reject = (_, res) => res.writeHead(403).end();
    const proxy = secure
      ? dualProtocolServer(
          {
            key: await readFile(join(work, "key")),
            cert: await readFile(join(work, "cert")),
          },
          reject,
          proxyProtocols,
        )
      : createServer(reject);
    proxy.on("connect", (req, socket, head) => {
      assert.equal(req.headers.authorization, undefined);
      assert.equal(req.headers["proxy-authorization"], undefined);
      assert.equal(req.url, `127.0.0.1:${server.address().port}`);
      connects++;
      const upstream = tcpConnect(server.address().port, "127.0.0.1", () => {
        socket.write("HTTP/1.1 200 Connection Established\r\n\r\n");
        if (head.length) upstream.write(head);
        upstream.pipe(socket);
        socket.pipe(upstream);
      });
      sockets.add(socket);
      sockets.add(upstream);
      upstream.on("error", () => socket.destroy());
      socket.on("error", () => upstream.destroy());
    });
    const close = proxy.closeAllConnections.bind(proxy);
    proxy.closeAllConnections = () => {
      for (const socket of sockets) socket.destroy();
      close();
    };
    const proxyOrigin = (await listen(proxy, t)).replace(
      /^http:/u,
      secure ? "https:" : "http:",
    );
    const proxied = new Session(memory(), "proxy", transport);
    t.after(() => proxied.dispose());
    await proxied.select({ ...connection, proxyURL: proxyOrigin });
    await proxied.manual("synthetic-token");
    assert.equal((await proxied.test()).identity.subject, subject);
    assert.ok(connects > 0);
    assert.deepEqual(new Set(targetProtocols), new Set(["http/1.1"]));
    if (secure) {
      assert.deepEqual(new Set(proxyProtocols), new Set(["http/1.1"]));
      const rejected = new Session(memory(), "untrusted-proxy", transport);
      t.after(() => rejected.dispose());
      await rejected.select({
        ...connection,
        caFile: undefined,
        proxyURL: proxyOrigin,
      });
      await rejected.manual("synthetic-token");
      const before = connects;
      await assert.rejects(rejected.test(), /request_failed/u);
      assert.equal(
        connects,
        before,
        "untrusted proxy must be refused before CONNECT",
      );
    }
  }
  headerSize = connection.headerBytes + 1;
  await assert.rejects(session.test(), /request_failed|headers_too_large/u);
  assert.equal(session.snapshot(), undefined);
  const wider = new Session(memory(), "wider-headers", transport);
  t.after(() => wider.dispose());
  await wider.select({ ...connection, headerBytes: 65536 });
  await wider.manual("synthetic-token");
  headerSize = 48 * 1024;
  assert.equal((await wider.test()).identity.subject, subject);
  headerSize = 65537;
  await assert.rejects(wider.test(), /request_failed|headers_too_large/u);
  assert.equal(wider.snapshot(), undefined);
  headerSize = 20 * 1024;
  assert.equal((await session.test()).identity.subject, subject);
  execFileSync(
    "openssl",
    [
      "req",
      "-x509",
      "-newkey",
      "rsa:2048",
      "-nodes",
      "-days",
      "1",
      "-subj",
      "/CN=wrong.invalid",
      "-addext",
      "subjectAltName=DNS:wrong.invalid",
      "-keyout",
      join(work, "wrong-key"),
      "-out",
      join(work, "wrong-cert"),
    ],
    { stdio: "ignore", timeout: 15000 },
  );
  const wrongServer = httpsServer(
    {
      key: await readFile(join(work, "wrong-key")),
      cert: await readFile(join(work, "wrong-cert")),
    },
    (_, res) => json(res, me),
  );
  await listen(wrongServer, t);
  const wrong = new Session(memory(), "wrong-host", transport);
  t.after(() => wrong.dispose());
  await wrong.select({
    ...connection,
    origin: `https://127.0.0.1:${wrongServer.address().port}`,
    caFile: join(work, "wrong-cert"),
  });
  await wrong.manual("synthetic-token");
  await assert.rejects(wrong.test(), /request_failed/u);
  await writeFile(join(work, "oversized"), Buffer.alloc(1024 ** 2 + 1));
  await assert.rejects(
    transport({ ...connection, caFile: join(work, "oversized") }),
    /invalid_trust_file/u,
  );
});
