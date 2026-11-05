import assert from "node:assert/strict";
import test from "node:test";
import { mcpEnvelope, mcpRealm } from "./mcp-fixture.mjs";

test("MCP fixture registers a separate public PKCE client with explicit audience and scopes", () => {
  const base = {
    users: [{ id: "human", username: "browser-human" }],
    clients: [{ clientId: "catalog-ui" }],
  };
  const before = structuredClone(base);
  const realm = mcpRealm(base, "https://catalog-mcp.test:54321");
  assert.deepEqual(base, before);
  assert.equal(realm.users[1].username, "mcp-reader");
  const client = realm.clients[1];
  assert.equal(client.clientId, "catalog-mcp");
  assert.equal(client.publicClient, true);
  assert.equal(client.secret, undefined);
  assert.equal(client.attributes["pkce.code.challenge.method"], "S256");
  assert.deepEqual(client.redirectUris, [
    "https://catalog-mcp.test:54321/mcp-oauth/callback",
  ]);
  assert.equal(client.directAccessGrantsEnabled, false);
  assert.equal(client.serviceAccountsEnabled, false);
  assert.equal(
    client.protocolMappers[0].config["included.custom.audience"],
    "open-agentic-catalog",
  );
  assert.equal(client.protocolMappers[0].config["id.token.claim"], "false");
  assert.equal(
    realm.clients[2].protocolMappers[0].config["included.custom.audience"],
    "different-catalog",
  );
  assert.equal(realm.clients[3].attributes["access.token.lifespan"], "5");
  assert.deepEqual(
    realm.clientScopes.map((scope) => scope.name),
    ["basic", "catalog.read", "catalog.write", "tenant.admin"],
  );
  assert.equal(
    realm.clientScopes[0].protocolMappers[0].protocolMapper,
    "oidc-sub-mapper",
  );
  assert.equal(
    realm.clientScopes[0].protocolMappers[0].config["access.token.claim"],
    "true",
  );
  for (const origin of [
    "https://public.example.com",
    "http://catalog-mcp.test",
    "https://catalog-mcp.test/path",
    "https://user@catalog-mcp.test",
  ])
    assert.throws(() => mcpRealm(base, origin));
});

test("selected protocol metadata and mirrored headers agree without legacy initialization metadata", () => {
  const modern = mcpEnvelope(
    "2026-07-28",
    "tools/call",
    "catalog_control_search",
    { limit: 5 },
  );
  assert.equal(modern.headers["Mcp-Name"], modern.body.params.name);
  assert.equal(modern.headers["Mcp-Method"], modern.body.method);
  assert.equal(
    modern.body.params._meta["io.modelcontextprotocol/protocolVersion"],
    modern.headers["Mcp-Protocol-Version"],
  );
  assert.deepEqual(modern.body.params.arguments, { limit: 5 });
  const legacy = mcpEnvelope("2025-11-25", "tools/list");
  assert.equal(legacy.headers["Mcp-Method"], undefined);
  assert.deepEqual(legacy.body.params, {});
});
