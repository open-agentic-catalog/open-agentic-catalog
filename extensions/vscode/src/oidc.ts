import { randomBytes, createHash } from "node:crypto";
import { createLocalJWKSet, jwtVerify, type JSONWebKeySet } from "jose";
import {
  ClientError,
  record,
  safeURL,
  text,
  type Connection,
} from "./config.js";

export interface Credentials {
  binding: string;
  accessToken: string;
  refreshToken?: string;
  expiresAt?: number;
  subject?: string;
  nonce?: string;
  authTime?: number;
}
export interface Login {
  state: string;
  nonce: string;
  verifier: string;
  redirectURI: string;
  callbackURI: string;
  expiresAt: number;
  provider: Provider;
}
interface Provider {
  authorization: string;
  token: string;
  jwks: string;
}
const algorithms = ["RS256", "ES256", "EdDSA"];
export function bearer(value: unknown): string {
  const result = text(value, 16384);
  if (!/^[A-Za-z0-9\-._~+/]+=*$/u.test(result))
    throw new ClientError("invalid_token");
  return result;
}
export function opaqueToken(value: unknown): string {
  const result = text(value, 16384);
  if ([...result].some((c) => c.charCodeAt(0) > 126))
    throw new ClientError("invalid_token");
  return result;
}
export async function issuerJSON(
  fetcher: typeof fetch,
  url: string,
  signal: AbortSignal,
  body?: URLSearchParams,
): Promise<unknown> {
  const response = await fetcher(url, {
    method: body ? "POST" : "GET",
    redirect: "error",
    credentials: "omit",
    signal: AbortSignal.any([signal, AbortSignal.timeout(30000)]),
    headers: {
      Accept: "application/json",
      "Accept-Encoding": "identity",
      ...(body ? { "Content-Type": "application/x-www-form-urlencoded" } : {}),
    },
    ...(body ? { body: body.toString() } : {}),
  });
  try {
    if (
      response.status !== 200 ||
      !/^application\/(?:[a-z.+-]*\+)?json(?:;|$)/iu.test(
        response.headers.get("content-type") ?? "",
      ) ||
      (response.headers.get("content-encoding") ?? "identity") !== "identity"
    )
      throw new ClientError("issuer_refused");
    const limit = body ? 65536 : 262144;
    let length = 0;
    const chunks: Uint8Array[] = [];
    const reader = response.body?.getReader();
    if (!reader) throw new ClientError("invalid_issuer_response");
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        length += value.length;
        if (length > limit) throw new ClientError("issuer_response_too_large");
        chunks.push(value);
      }
    } finally {
      await reader.cancel();
    }
    try {
      return JSON.parse(
        new TextDecoder("utf-8", { fatal: true }).decode(Buffer.concat(chunks)),
      );
    } catch {
      throw new ClientError("invalid_issuer_response");
    }
  } finally {
    if (!response.body?.locked) await response.body?.cancel();
  }
}
async function provider(
  connection: Connection,
  fetcher: typeof fetch,
  signal: AbortSignal,
): Promise<Provider> {
  const oidc = connection.oidc;
  if (!oidc) throw new ClientError("invalid_configuration");
  const config = record(
    await issuerJSON(
      fetcher,
      oidc.issuer.replace(/\/$/u, "") + "/.well-known/openid-configuration",
      signal,
    ),
  );
  if (
    config.issuer !== oidc.issuer ||
    !Array.isArray(config.code_challenge_methods_supported) ||
    !config.code_challenge_methods_supported.includes("S256") ||
    !Array.isArray(config.response_types_supported) ||
    !config.response_types_supported.includes("code")
  )
    throw new ClientError("invalid_issuer_metadata");
  function endpoint(value: unknown): string {
    const url = safeURL(value, connection.allowLoopbackHTTP, false);
    if (new URL(url).origin !== new URL(oidc!.issuer).origin)
      throw new ClientError("issuer_origin_mismatch");
    return url;
  }
  return {
    authorization: endpoint(config.authorization_endpoint),
    token: endpoint(config.token_endpoint),
    jwks: endpoint(config.jwks_uri),
  };
}
export async function beginLogin(
  connection: Connection,
  redirectURI: string,
  fetcher: typeof fetch,
  signal: AbortSignal,
  callbackURI = redirectURI,
): Promise<{ login: Login; url: string }> {
  const discovery = await provider(connection, fetcher, signal);
  const login = {
    state: randomBytes(32).toString("base64url"),
    nonce: randomBytes(32).toString("base64url"),
    verifier: randomBytes(48).toString("base64url"),
    redirectURI,
    callbackURI,
    expiresAt: Date.now() + 300000,
    provider: discovery,
  };
  const url = new URL(discovery.authorization);
  const config = connection.oidc!;
  const scopes = [...config.scopes];
  if (config.refresh && !scopes.includes("offline_access"))
    scopes.push("offline_access");
  for (const [name, value] of Object.entries({
    client_id: config.clientId,
    redirect_uri: redirectURI,
    response_type: "code",
    scope: scopes.join(" "),
    audience: config.audience,
    state: login.state,
    nonce: login.nonce,
    code_challenge: createHash("sha256")
      .update(login.verifier)
      .digest("base64url"),
    code_challenge_method: "S256",
  }))
    url.searchParams.set(name, value);
  if (config.refresh) url.searchParams.set("prompt", "consent");
  return { login, url: url.href };
}
async function tokens(
  connection: Connection,
  result: unknown,
  fetcher: typeof fetch,
  signal: AbortSignal,
  discovery: Provider,
  bound: string,
  expected: { nonce: string; subject?: string; authTime?: number },
): Promise<Credentials> {
  const config = connection.oidc!;
  const value = record(result);
  if (
    typeof value.token_type !== "string" ||
    value.token_type.toLowerCase() !== "bearer" ||
    !Number.isSafeInteger(value.expires_in) ||
    (value.expires_in as number) <= 0 ||
    (value.expires_in as number) > 86400
  )
    throw new ClientError("invalid_token_response");
  const accessToken = bearer(value.access_token);
  if (value.id_token === accessToken)
    throw new ClientError("id_token_substitution");
  const keys = record(await issuerJSON(fetcher, discovery.jwks, signal));
  if (!Array.isArray(keys.keys) || keys.keys.length > 20)
    throw new ClientError("invalid_issuer_keys");
  const keySet = createLocalJWKSet(keys as unknown as JSONWebKeySet);
  try {
    const access = await jwtVerify(accessToken, keySet, {
      issuer: config.issuer,
      audience: config.audience,
      algorithms,
      requiredClaims: ["sub", "exp", "iat"],
    });
    const actualSubject = text(access.payload.sub);
    if (expected.subject !== undefined && expected.subject !== actualSubject)
      throw new ClientError("identity_changed");
    let authTime = expected.authTime;
    if (expected.subject === undefined || value.id_token !== undefined) {
      const id = await jwtVerify(bearer(value.id_token), keySet, {
        issuer: config.issuer,
        audience: config.clientId,
        algorithms,
        requiredClaims: ["sub", "exp", "iat"],
      });
      if (
        id.payload.sub !== actualSubject ||
        ((expected.subject === undefined || id.payload.nonce !== undefined) &&
          id.payload.nonce !== expected.nonce) ||
        (Array.isArray(id.payload.aud) &&
          id.payload.aud.some((audience) => audience !== config.clientId)) ||
        (Array.isArray(id.payload.aud) &&
          id.payload.aud.length > 1 &&
          id.payload.azp !== config.clientId) ||
        (id.payload.azp !== undefined && id.payload.azp !== config.clientId)
      )
        throw new ClientError("invalid_identity_token");
      if (id.payload.auth_time !== undefined) {
        if (
          typeof id.payload.auth_time !== "number" ||
          !Number.isFinite(id.payload.auth_time) ||
          (expected.authTime !== undefined &&
            id.payload.auth_time !== expected.authTime)
        )
          throw new ClientError("invalid_identity_token");
        authTime = id.payload.auth_time;
      }
    }
    const expiresAt = Math.min(
      Date.now() + (value.expires_in as number) * 1000,
      // JWT NumericDate permits fractions; store whole milliseconds without extending expiry.
      Math.floor(access.payload.exp! * 1000),
    );
    if (expiresAt <= Date.now()) throw new ClientError("expired_token");
    const refreshToken =
      config.refresh && value.refresh_token !== undefined
        ? opaqueToken(value.refresh_token)
        : undefined;
    return {
      binding: bound,
      accessToken,
      subject: actualSubject,
      expiresAt,
      nonce: expected.nonce,
      ...(authTime === undefined ? {} : { authTime }),
      ...(refreshToken ? { refreshToken } : {}),
    };
  } catch {
    throw new ClientError("token_verification_failed");
  }
}
export async function finishLogin(
  connection: Connection,
  login: Login,
  callback: string,
  fetcher: typeof fetch,
  signal: AbortSignal,
  bound: string,
): Promise<Credentials> {
  if (Date.now() >= login.expiresAt) throw new ClientError("login_expired");
  const url = new URL(text(callback, 8192));
  const expected = new URL(login.callbackURI);
  if (
    url.protocol !== expected.protocol ||
    url.host !== expected.host ||
    url.pathname !== expected.pathname ||
    url.username ||
    url.password ||
    url.hash ||
    [...url.searchParams.keys()].some(
      (key, i, all) => all.indexOf(key) !== i,
    ) ||
    url.searchParams.get("state") !== login.state ||
    url.searchParams.has("error") ||
    (url.searchParams.has("iss") &&
      url.searchParams.get("iss") !== connection.oidc!.issuer)
  )
    throw new ClientError("invalid_login_callback");
  const code = text(url.searchParams.get("code"), 4096);
  return tokens(
    connection,
    await issuerJSON(
      fetcher,
      login.provider.token,
      signal,
      new URLSearchParams({
        grant_type: "authorization_code",
        client_id: connection.oidc!.clientId,
        code,
        code_verifier: login.verifier,
        redirect_uri: login.redirectURI,
      }),
    ),
    fetcher,
    signal,
    login.provider,
    bound,
    { nonce: login.nonce },
  );
}
export async function refreshLogin(
  connection: Connection,
  old: Credentials,
  fetcher: typeof fetch,
  signal: AbortSignal,
): Promise<Credentials> {
  if (
    !old.refreshToken ||
    !old.subject ||
    !old.nonce ||
    !connection.oidc?.refresh
  )
    throw new ClientError("sign_in_required");
  const discovery = await provider(connection, fetcher, signal);
  const next = await tokens(
    connection,
    await issuerJSON(
      fetcher,
      discovery.token,
      signal,
      new URLSearchParams({
        grant_type: "refresh_token",
        client_id: connection.oidc.clientId,
        refresh_token: old.refreshToken,
      }),
    ),
    fetcher,
    signal,
    discovery,
    old.binding,
    {
      nonce: old.nonce,
      subject: old.subject,
      ...(old.authTime === undefined ? {} : { authTime: old.authTime }),
    },
  );
  return { ...next, refreshToken: next.refreshToken ?? old.refreshToken };
}
