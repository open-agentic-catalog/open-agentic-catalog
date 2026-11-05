import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import {
  humanRealm,
  namespaceReadControl,
  ratingsRealm,
} from "./oidc-fixture.mjs";

const namespace = {
  route: "/browser/_catalog/api/v1/namespaces/retained-id",
  id: "retained-id",
  slug: "private-namespace",
  tenant: "fixture-tenant",
};
const forbidden = {
  type: "https://catalog.example.com/problems/forbidden",
  title: "Forbidden",
  status: 403,
  detail: "OAuth scope catalog.read is required",
  instance: "/api/v1/namespaces/retained-id",
  code: "FORBIDDEN",
  requestId: "fixture-request",
};
const problemHeaders = {
  "content-type": "application/problem+json",
  "x-request-id": "fixture-request",
};

test("browser scope probe uses the cookie forwarding boundary and verifies the exact retained namespace", async (t) => {
  let body = {
    id: namespace.id,
    tenantId: namespace.tenant,
    slug: namespace.slug,
    displayName: namespace.slug,
  };
  t.mock.method(globalThis, "fetch", async (route, options) => {
    assert.equal(route, namespace.route);
    assert.deepEqual(options, {
      method: "GET",
      credentials: "same-origin",
      cache: "no-store",
      headers: { "X-Catalog-Request": "1" },
    });
    return new Response(JSON.stringify(body), { status: 200 });
  });
  assert.deepEqual(await namespaceReadControl(namespace), {
    status: 200,
    retained: true,
    forbidden: false,
  });
  for (const key of ["id", "tenantId", "slug", "displayName"]) {
    const original = body;
    body = { ...body, [key]: "different-record" };
    assert.equal((await namespaceReadControl(namespace)).retained, false);
    body = original;
  }
});

test("scope rejection requires the exact correlated safe problem and exposes no body", async (t) => {
  let body = forbidden;
  let status = 403;
  let headers = problemHeaders;
  t.mock.method(
    globalThis,
    "fetch",
    async () => new Response(JSON.stringify(body), { status, headers }),
  );
  assert.deepEqual(await namespaceReadControl(namespace), {
    status: 403,
    retained: false,
    forbidden: true,
  });
  for (const changed of [
    { ...forbidden, code: "UNAUTHENTICATED" },
    { ...forbidden, status: 401 },
    { ...forbidden, type: "about:blank" },
    { ...forbidden, title: namespace.slug },
    { ...forbidden, detail: namespace.slug },
    { ...forbidden, instance: "/api/v1/namespaces/other-id" },
    { ...forbidden, requestId: "different-request" },
    { ...forbidden, displayName: namespace.slug },
    { ...forbidden, nested: { namespace: namespace.slug } },
    { ...forbidden, errors: [{ message: namespace.slug }] },
    null,
    [],
  ]) {
    body = changed;
    const result = await namespaceReadControl(namespace);
    assert.deepEqual(result, {
      status: 403,
      retained: false,
      forbidden: false,
    });
    assert.equal(JSON.stringify(result).includes(namespace.slug), false);
  }
  body = forbidden;
  status = 401;
  assert.equal((await namespaceReadControl(namespace)).forbidden, false);
  status = 403;
  for (const changed of [
    { ...problemHeaders, "content-type": "application/json" },
    { ...problemHeaders, "x-request-id": "different-request" },
  ]) {
    headers = changed;
    assert.equal((await namespaceReadControl(namespace)).forbidden, false);
  }
});

test("human provider profile retains subject mapping, separate audiences and required PKCE", async () => {
  const base = JSON.parse(
    await readFile(
      new URL("../../test/functional/oidc-realm.json", import.meta.url),
      "utf8",
    ),
  );
  const before = structuredClone(base);
  const origin = "https://127.0.0.1:54321";
  const realm = humanRealm(base, {
    origin,
    subject: "fixture-subject",
    password: "fixture-password",
    secret: "fixture-secret",
  });
  assert.deepEqual(base, before);
  assert.equal(realm.realm, "catalog");
  assert.equal(realm.sslRequired, "all");
  assert.equal(realm.registrationAllowed, false);
  assert.equal(realm.users.length, 2);
  assert.equal(realm.users[0].id, "fixture-subject");
  assert.deepEqual(realm.users[0].realmRoles, ["offline_access"]);
  assert.equal(realm.users[1].username, "unregistered-human");
  const [client] = realm.clients;
  assert.equal(client.standardFlowEnabled, true);
  for (const flag of [
    "publicClient",
    "directAccessGrantsEnabled",
    "implicitFlowEnabled",
    "serviceAccountsEnabled",
  ])
    assert.equal(client[flag], false);
  assert.deepEqual(client.redirectUris, [
    origin + "/auth/callback",
    origin + "/browser/auth/callback",
  ]);
  assert.equal(client.attributes["pkce.code.challenge.method"], "S256");
  assert.ok(client.defaultClientScopes.includes("basic"));
  assert.deepEqual(client.optionalClientScopes, []);
  assert.equal(
    client.protocolMappers[0].config["included.custom.audience"],
    "open-agentic-catalog",
  );
  assert.equal(client.protocolMappers[0].config["id.token.claim"], "false");
  assert.equal(client.protocolMappers[0].config["access.token.claim"], "true");
  const cli = realm.clients.find((item) => item.clientId === "catalog-cli");
  assert.equal(cli.publicClient, true);
  assert.equal(cli.standardFlowEnabled, true);
  assert.equal(cli.directAccessGrantsEnabled, false);
  assert.equal(cli.implicitFlowEnabled, false);
  assert.equal(cli.serviceAccountsEnabled, false);
  assert.equal(Object.hasOwn(cli, "secret"), false);
  assert.deepEqual(cli.redirectUris, ["http://127.0.0.1/oauth/callback"]);
  assert.equal(cli.attributes["pkce.code.challenge.method"], "S256");
  assert.deepEqual(cli.optionalClientScopes, ["offline_access"]);
  assert.equal(cli.protocolMappers[0].config["id.token.claim"], "false");
});

test("provider profile refuses wildcard, remote, insecure or non-origin redirects", () => {
  for (const origin of [
    "http://127.0.0.1:54321",
    "https://external.example.com",
    "https://127.0.0.1:54321/path",
    "https://127.0.0.1:54321?redirect=elsewhere",
    "https://user@127.0.0.1:54321",
    "*",
  ])
    assert.throws(() => humanRealm({}, { origin }));
});

test("rating realm keeps the existing human flows separate from explicit scopes and a service identity", () => {
  const original = humanRealm(
    { sslRequired: "all", registrationAllowed: false },
    {
      origin: "https://127.0.0.1:54321",
      subject: "fixture-subject",
      password: "fixture-password",
      secret: "fixture-secret",
    },
  );
  const before = structuredClone(original);
  const realm = ratingsRealm(original, "separate-service-secret");
  assert.deepEqual(original, before);
  assert.equal(realm.realm, "catalog-ratings");
  assert.equal(realm.sslRequired, "all");
  assert.deepEqual(
    realm.clientScopes.map((scope) => scope.name),
    [
      "basic",
      "catalog.read",
      "ratings.write",
      "personal.write",
      "tenant.admin",
    ],
  );
  assert.equal(
    realm.clientScopes[0].protocolMappers[0].config["access.token.claim"],
    "true",
  );
  const [ui, service] = realm.clients;
  assert.deepEqual(ui.redirectUris, original.clients[0].redirectUris);
  assert.deepEqual(ui.defaultClientScopes, ["basic"]);
  assert.deepEqual(ui.optionalClientScopes, [
    "catalog.read",
    "ratings.write",
    "personal.write",
    "tenant.admin",
  ]);
  assert.deepEqual(ui.protocolMappers[1], {
    name: "same-subject-other-realm",
    protocol: "openid-connect",
    protocolMapper: "oidc-hardcoded-claim-mapper",
    config: {
      "claim.name": "sub",
      "claim.value": "fixture-subject",
      "jsonType.label": "String",
      "access.token.claim": "true",
      "id.token.claim": "true",
      "userinfo.token.claim": "true",
    },
  });
  assert.equal(
    service.protocolMappers.length,
    original.clients[0].protocolMappers.length,
  );
  assert.equal(ui.attributes["pkce.code.challenge.method"], "S256");
  assert.equal(ui.directAccessGrantsEnabled, false);
  assert.equal(ui.serviceAccountsEnabled, false);
  assert.equal(service.serviceAccountsEnabled, true);
  assert.equal(service.standardFlowEnabled, false);
  assert.equal(service.directAccessGrantsEnabled, false);
  assert.equal(service.implicitFlowEnabled, false);
  assert.equal(service.secret, "separate-service-secret");
  assert.deepEqual(service.defaultClientScopes, [
    "basic",
    "catalog.read",
    "ratings.write",
    "personal.write",
    "tenant.admin",
  ]);
  assert.equal(realm.users[0].id, "00000000-0000-4000-8000-000000000015");
  for (const user of realm.users)
    assert.ok(original.users.every((existing) => existing.id !== user.id));
  assert.equal(
    new Set(realm.users.map((user) => user.id)).size,
    realm.users.length,
  );
  assert.deepEqual(realm.users[0].realmRoles, []);
  assert.equal(realm.users[1].serviceAccountClientId, service.clientId);
  assert.match(realm.users[1].id, /^[0-9a-f-]{36}$/);
  assert.equal(service.protocolMappers[0].config["id.token.claim"], "false");
});
