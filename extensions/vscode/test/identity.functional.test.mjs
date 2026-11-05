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
