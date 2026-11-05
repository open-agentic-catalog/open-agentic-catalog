// Pure input construction for the owned MCP OAuth fixture. No provider contact.
export function mcpRealm(base, origin) {
  const url = new URL(origin);
  if (
    url.origin !== origin ||
    url.protocol !== "https:" ||
    url.hostname !== "catalog-mcp.test"
  )
    throw new Error("MCP fixture requires its owned HTTPS origin");
  const scopes = ["catalog.read", "catalog.write", "tenant.admin"];
  const client = (clientId, audience, lifespan) => ({
    clientId,
    enabled: true,
    protocol: "openid-connect",
    publicClient: true,
    standardFlowEnabled: true,
    directAccessGrantsEnabled: false,
    implicitFlowEnabled: false,
    serviceAccountsEnabled: false,
    redirectUris: [origin + "/mcp-oauth/callback"],
    attributes: {
      "pkce.code.challenge.method": "S256",
      ...(lifespan ? { "access.token.lifespan": lifespan } : {}),
    },
    defaultClientScopes: ["basic"],
    optionalClientScopes: scopes,
    protocolMappers: [
      {
        name: "catalog-audience",
        protocol: "openid-connect",
        protocolMapper: "oidc-audience-mapper",
        config: {
          "included.custom.audience": audience,
          "access.token.claim": "true",
          "id.token.claim": "false",
        },
      },
    ],
  });
  return {
    ...base,
    // Explicit realm scopes replace Keycloak defaults. Retain the basic
    // access-token subject mapper required for human membership resolution.
    clientScopes: [
      {
        name: "basic",
        protocol: "openid-connect",
        attributes: { "include.in.token.scope": "false" },
        protocolMappers: [
          {
            name: "subject",
            protocol: "openid-connect",
            protocolMapper: "oidc-sub-mapper",
            config: { "access.token.claim": "true" },
          },
        ],
      },
      ...scopes.map((name) => ({
        name,
        protocol: "openid-connect",
        attributes: {
          "include.in.token.scope": "true",
          "display.on.consent.screen": "false",
        },
      })),
    ],
    users: [
      ...base.users,
      {
        ...base.users[0],
        id: "00000000-0000-4000-8000-000000000013",
        username: "mcp-reader",
        email: "mcp-reader@example.invalid",
      },
    ],
    clients: [
      ...base.clients,
      client("catalog-mcp", "open-agentic-catalog"),
      client("catalog-mcp-wrong-audience", "different-catalog"),
      client("catalog-mcp-expiring", "open-agentic-catalog", "5"),
    ],
  };
}

export function mcpEnvelope(version, method, name, args = {}) {
  const params = name ? { name, arguments: args } : {};
  const headers = {
    "Content-Type": "application/json",
    Accept: "application/json, text/event-stream",
    "Mcp-Protocol-Version": version,
  };
  if (version === "2026-07-28") {
    params._meta = {
      "io.modelcontextprotocol/protocolVersion": version,
      "io.modelcontextprotocol/clientInfo": {
        name: "owned-oauth-check",
        version: "1",
      },
      "io.modelcontextprotocol/clientCapabilities": {},
    };
    headers["Mcp-Method"] = method;
    if (name) headers["Mcp-Name"] = name;
  }
  return { headers, body: { jsonrpc: "2.0", id: 1, method, params } };
}
