import test from "node:test";
import assert from "node:assert/strict";
import { pathToFileURL } from "node:url";
import {
  generateKeyPair,
  exportJWK,
  SignJWT,
} from "../node_modules/jose/dist/webapi/index.js";
const { connections, beginLogin, finishLogin, refreshLogin, issuerJSON } =
  await import(pathToFileURL(process.env.CATALOG_VSCODE_LIB));
const issuer = "https://issuer.example/realm";
const connection = connections([
  {
    id: "oidc",
    label: "OIDC",
    origin: "https://catalog.example",
    mode: "control",
    auth: "oidc",
    oidc: {
      issuer,
      clientId: "extension",
      audience: "catalog",
      scopes: ["openid"],
    },
  },
])[0];
const signal = new AbortController().signal;
const json = (value) =>
  new Response(JSON.stringify(value), {
    headers: { "content-type": "application/json" },
  });
const metadata = {
  issuer,
  authorization_endpoint: issuer + "/authorize",
  token_endpoint: issuer + "/token",
  jwks_uri: issuer + "/keys",
  code_challenge_methods_supported: ["S256"],
  response_types_supported: ["code"],
};

test("issuer metadata cannot redirect credentials or omit PKCE support", async () => {
  for (const change of [
    { issuer: "https://other.example" },
    { token_endpoint: "https://other.example/token" },
    { jwks_uri: "https://other.example/keys" },
    { code_challenge_methods_supported: ["plain"] },
    { response_types_supported: ["token"] },
  ])
    await assert.rejects(
      beginLogin(
        connection,
        "vscode://extension/oidc",
        async () => json({ ...metadata, ...change }),
        signal,
      ),
    );
});
test("PKCE state and nonce are fresh; a remote external URI retains its native callback binding", async () => {
  const fetcher = async () => json(metadata);
  const first = await beginLogin(
    connection,
    "https://remote.example/redirect",
    fetcher,
    signal,
    "vscode://extension/oidc",
  );
  const second = await beginLogin(
    connection,
    "vscode://extension/oidc",
    fetcher,
    signal,
  );
  assert.notEqual(first.login.state, second.login.state);
  assert.notEqual(first.login.nonce, second.login.nonce);
  assert.equal(first.login.callbackURI, "vscode://extension/oidc");
  assert.equal(first.login.redirectURI, "https://remote.example/redirect");
  const url = new URL(first.url);
  assert.equal(url.searchParams.get("code_challenge_method"), "S256");
  assert.equal(url.searchParams.get("audience"), "catalog");
  assert.equal(url.searchParams.get("scope"), "openid");
  assert.notEqual(url.searchParams.get("code_challenge"), first.login.verifier);
  assert.equal(url.searchParams.has("prompt"), false);
  const offline = await beginLogin(
    { ...connection, oidc: { ...connection.oidc, refresh: true } },
    "vscode://extension/oidc",
    fetcher,
    signal,
  );
  assert.equal(
    new URL(offline.url).searchParams.get("scope"),
    "openid offline_access",
  );
  assert.equal(new URL(offline.url).searchParams.get("prompt"), "consent");
});
test("invalid callback is refused before token contact", async () => {
  const { login } = await beginLogin(
    connection,
    "vscode://extension/oidc",
    async () => json(metadata),
    signal,
  );
  let calls = 0;
  const fetcher = async () => {
    calls++;
    return json({});
  };
  for (const callback of [
    "vscode://other/oidc?code=secret&state=" + login.state,
    "vscode://unexpected@extension/oidc?code=secret&state=" + login.state,
    "vscode://extension/wrong?code=secret&state=" + login.state,
    "vscode://extension/oidc?code=secret&state=wrong",
    "vscode://extension/oidc?code=secret&state=" +
      login.state +
      "&state=" +
      login.state,
    "vscode://extension/oidc?error=access_denied&state=" + login.state,
    "vscode://extension/oidc?code=secret&state=" +
      login.state +
      "&iss=https://other.example",
    "vscode://extension/oidc?code=secret&state=" + login.state + "#fragment",
  ])
    await assert.rejects(
      finishLogin(connection, login, callback, fetcher, signal, "binding"),
      /invalid_login_callback/u,
    );
  await assert.rejects(
    finishLogin(
      connection,
      { ...login, expiresAt: 0 },
      "vscode://extension/oidc",
      fetcher,
      signal,
      "binding",
    ),
    /login_expired/u,
  );
  assert.equal(calls, 0);
});
test("issuer trailing slash remains an exact issuer identity", async () => {
  const configured = {
    ...connection,
    oidc: { ...connection.oidc, issuer: issuer + "/" },
  };
  const seen = [];
  const { url } = await beginLogin(
    configured,
    "vscode://extension/oidc",
    async (input) => {
      seen.push(input);
      return json({ ...metadata, issuer: issuer + "/" });
    },
    signal,
  );
  assert.deepEqual(seen, [issuer + "/.well-known/openid-configuration"]);
  assert.equal(new URL(url).origin, new URL(issuer).origin);
});
test("issuer responses are bounded, have JSON media, and do not reflect upstream errors", async () => {
  for (const response of [
    new Response("secret", { status: 400 }),
    new Response("secret", { headers: { "content-type": "text/plain" } }),
    new Response("secret", {
      headers: {
        "content-type": "application/json",
        "content-encoding": "gzip",
      },
    }),
    new Response("not json", {
      headers: { "content-type": "application/json" },
    }),
    new Response(new Uint8Array([255]), {
      headers: { "content-type": "application/json" },
    }),
    json({ large: "x".repeat(262145) }),
  ])
    await assert.rejects(issuerJSON(async () => response, issuer, signal));
  assert.deepEqual(
    await issuerJSON(async () => json({ accepted: true }), issuer, signal),
    { accepted: true },
  );
});
test("signed token checks reject wrong signatures, expiry, missing claims and ID-token substitution", async () => {
  const { privateKey, publicKey } = await generateKeyPair("ES256");
  const jwk = { ...(await exportJWK(publicKey)), kid: "owned", alg: "ES256" };
  const other = await generateKeyPair("ES256");
  for (const failure of [
    "none",
    "fractional-exp",
    "signature",
    "expired",
    "issuer",
    "audience",
    "missing-sub",
    "substitution",
    "id-audience",
    "id-party",
    "auth-time",
  ]) {
    const { login } = await beginLogin(
      connection,
      "vscode://extension/oidc",
      async () => json(metadata),
      signal,
    );
    async function sign(aud, id = false) {
      let token = new SignJWT(
        id
          ? {
              nonce: login.nonce,
              ...(failure === "id-audience" ? { azp: "extension" } : {}),
              ...(failure === "id-party" ? { azp: "untrusted" } : {}),
              ...(failure === "auth-time" ? { auth_time: "invalid" } : {}),
            }
          : {},
      )
        .setProtectedHeader({ alg: "ES256", kid: "owned" })
        .setIssuer(failure === "issuer" ? "https://wrong.example" : issuer)
        .setAudience(
          id && failure === "id-audience"
            ? [aud, "untrusted"]
            : !id && failure === "audience"
              ? "wrong"
              : aud,
        )
        .setIssuedAt()
        .setExpirationTime(
          failure === "expired"
            ? "-1h"
            : !id && failure === "fractional-exp"
              ? Math.floor(Date.now() / 1000) + 120.0001
              : "1h",
        );
      if (failure !== "missing-sub") token = token.setSubject("reader");
      return token.sign(
        failure === "signature" ? other.privateKey : privateKey,
      );
    }
    const id = await sign("extension", true);
    const value = {
      access_token: failure === "substitution" ? id : await sign("catalog"),
      id_token: id,
      token_type: "Bearer",
      expires_in: 3600,
    };
    const fetcher = async (url) =>
      String(url).endsWith("/keys") ? json({ keys: [jwk] }) : json(value);
    const callback = `vscode://extension/oidc?code=owned&state=${login.state}`;
    if (["none", "fractional-exp"].includes(failure)) {
      const credentials = await finishLogin(
        connection,
        login,
        callback,
        fetcher,
        signal,
        "binding",
      );
      assert.equal(credentials.subject, "reader");
      assert.ok(
        Number.isSafeInteger(credentials.expiresAt),
        "stored expiry must use whole milliseconds without extending token life",
      );
    } else
      await assert.rejects(
        finishLogin(connection, login, callback, fetcher, signal, "binding"),
      );
  }
});

test("refresh ID tokens preserve the original nonce, authentication time and audience", async () => {
  const configured = {
    ...connection,
    oidc: { ...connection.oidc, refresh: true },
  };
  const { privateKey, publicKey } = await generateKeyPair("ES256");
  const jwk = { ...(await exportJWK(publicKey)), kid: "owned", alg: "ES256" };
  const { login } = await beginLogin(
    configured,
    "vscode://extension/oidc",
    async () => json(metadata),
    signal,
  );
  const sign = (audience, claims = {}, tokenSubject = "reader") =>
    new SignJWT(claims)
      .setProtectedHeader({ alg: "ES256", kid: "owned" })
      .setIssuer(issuer)
      .setSubject(tokenSubject)
      .setAudience(audience)
      .setIssuedAt()
      .setExpirationTime("1h")
      .sign(privateKey);
  let value = {
    access_token: await sign("catalog"),
    id_token: await sign("extension", { nonce: login.nonce, auth_time: 1000 }),
    token_type: "Bearer",
    expires_in: 3600,
    refresh_token: "initial",
  };
  const fetcher = async (url) =>
    String(url).endsWith("/keys")
      ? json({ keys: [jwk] })
      : String(url).endsWith("openid-configuration")
        ? json(metadata)
        : json(value);
  const original = await finishLogin(
    configured,
    login,
    `vscode://extension/oidc?code=owned&state=${login.state}`,
    fetcher,
    signal,
    "binding",
  );
  for (const claims of [
    undefined,
    {},
    { nonce: login.nonce, auth_time: 1000 },
  ]) {
    value = {
      access_token: await sign("catalog"),
      token_type: "Bearer",
      expires_in: 3600,
      ...(claims === undefined
        ? {}
        : { id_token: await sign("extension", claims) }),
    };
    const renewed = await refreshLogin(configured, original, fetcher, signal);
    assert.equal(renewed.subject, "reader");
    assert.equal(renewed.refreshToken, "initial");
    assert.equal(renewed.nonce, login.nonce);
    assert.equal(renewed.authTime, 1000);
  }
  for (const claims of [
    { nonce: "another-login" },
    { auth_time: 1001 },
    { auth_time: "1000" },
  ]) {
    value = { ...value, id_token: await sign("extension", claims) };
    await assert.rejects(
      refreshLogin(configured, original, fetcher, signal),
      /token_verification_failed/u,
    );
  }
  for (const id of [
    await sign(["extension", "untrusted"], { azp: "extension" }),
    await sign("extension", {}, "another-reader"),
  ]) {
    value = { ...value, id_token: id };
    await assert.rejects(
      refreshLogin(configured, original, fetcher, signal),
      /token_verification_failed/u,
    );
  }
  // An optional auth_time can first appear on refresh; bind it once it is known.
  const nextLogin = (
    await beginLogin(configured, "vscode://extension/oidc", fetcher, signal)
  ).login;
  value = {
    access_token: await sign("catalog"),
    id_token: await sign("extension", { nonce: nextLogin.nonce }),
    token_type: "Bearer",
    expires_in: 3600,
    refresh_token: "initial",
  };
  const withoutAuthTime = await finishLogin(
    configured,
    nextLogin,
    `vscode://extension/oidc?code=another&state=${nextLogin.state}`,
    fetcher,
    signal,
    "binding",
  );
  assert.equal(withoutAuthTime.authTime, undefined);
  value = { ...value, id_token: await sign("extension", { auth_time: 1000 }) };
  const firstAuthTime = await refreshLogin(
    configured,
    withoutAuthTime,
    fetcher,
    signal,
  );
  assert.equal(firstAuthTime.authTime, 1000);
  value = { ...value, id_token: await sign("extension", { auth_time: 1001 }) };
  await assert.rejects(
    refreshLogin(configured, firstAuthTime, fetcher, signal),
    /token_verification_failed/u,
  );
});
