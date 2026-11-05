// Run in the actual page so cookies and the browser network ledger apply.
// Project booleans only: failed assertions must never print a private body.
export async function namespaceReadControl({ route, id, slug, tenant }) {
  const response = await fetch(route, {
    method: "GET",
    credentials: "same-origin",
    cache: "no-store",
    headers: { "X-Catalog-Request": "1" },
  });
  const body = await response.json();
  const problemKeys = [
    "type",
    "title",
    "status",
    "detail",
    "instance",
    "code",
    "requestId",
  ];
  return {
    status: response.status,
    retained:
      response.status === 200 &&
      body?.id === id &&
      body?.tenantId === tenant &&
      body?.slug === slug &&
      body?.displayName === slug,
    forbidden:
      response.status === 403 &&
      response.headers.get("content-type") === "application/problem+json" &&
      body?.type === "https://catalog.example.com/problems/forbidden" &&
      body?.title === "Forbidden" &&
      body?.status === 403 &&
      body?.code === "FORBIDDEN" &&
      body?.detail === "OAuth scope catalog.read is required" &&
      body?.instance === route.slice(route.indexOf("/_catalog") + 9) &&
      typeof body?.requestId === "string" &&
      body.requestId.length > 0 &&
      body.requestId === response.headers.get("x-request-id") &&
      Object.keys(body).length === problemKeys.length &&
      Object.keys(body).every((key) => problemKeys.includes(key)),
  };
}

// A synthetic human profile for the external test provider. The ordinary
// backend provider gate uses service accounts and cannot qualify this flow.
export function humanRealm(base, { origin, subject, password, secret }) {
  const parsed = new URL(origin);
  if (
    parsed.origin !== origin ||
    parsed.protocol !== "https:" ||
    parsed.hostname !== "127.0.0.1"
  )
    throw new Error(
      "Human browser fixture requires an owned HTTPS loopback origin",
    );
  return {
    ...base,
    users: [
      {
        id: subject,
        username: "browser-human",
        enabled: true,
        emailVerified: true,
        firstName: "Browser",
        lastName: "Human",
        email: "browser@example.invalid",
        // Imported users do not automatically receive the realm's defaults.
        // The CLI explicitly requests offline_access; this grants no Catalog role.
        realmRoles: ["offline_access"],
        credentials: [{ type: "password", value: password, temporary: false }],
      },
      {
        id: "00000000-0000-4000-8000-000000000012",
        username: "unregistered-human",
        enabled: true,
        emailVerified: true,
        firstName: "Unknown",
        lastName: "Human",
        email: "unknown@example.invalid",
        credentials: [{ type: "password", value: password, temporary: false }],
      },
    ],
    clients: [
      {
        clientId: "catalog-ui",
        enabled: true,
        secret,
        protocol: "openid-connect",
        publicClient: false,
        standardFlowEnabled: true,
        directAccessGrantsEnabled: false,
        implicitFlowEnabled: false,
        serviceAccountsEnabled: false,
        redirectUris: [
          origin + "/auth/callback",
          origin + "/browser/auth/callback",
        ],
        attributes: { "pkce.code.challenge.method": "S256" },
        // Since Keycloak 25, basic supplies the access-token subject mapper.
        // ID tokens and service-account access tokens do not need that mapper.
        defaultClientScopes: ["basic", "profile", "email", "roles"],
        optionalClientScopes: [],
        protocolMappers: [
          {
            name: "catalog-audience",
            protocol: "openid-connect",
            protocolMapper: "oidc-audience-mapper",
            config: {
              "included.custom.audience": "open-agentic-catalog",
              "access.token.claim": "true",
              "id.token.claim": "false",
            },
          },
        ],
      },
      {
        clientId: "catalog-cli",
        enabled: true,
        protocol: "openid-connect",
        publicClient: true,
        standardFlowEnabled: true,
        directAccessGrantsEnabled: false,
        implicitFlowEnabled: false,
        serviceAccountsEnabled: false,
        // Native clients select a free literal-loopback port (RFC 8252).
        redirectUris: ["http://127.0.0.1/oauth/callback"],
        attributes: { "pkce.code.challenge.method": "S256" },
        defaultClientScopes: ["basic", "profile"],
        optionalClientScopes: ["offline_access"],
        protocolMappers: [
          {
            name: "catalog-audience",
            protocol: "openid-connect",
            protocolMapper: "oidc-audience-mapper",
            config: {
              "included.custom.audience": "open-agentic-catalog",
              "access.token.claim": "true",
              "id.token.claim": "false",
            },
          },
        ],
      },
    ],
  };
}

// A second realm in the same owned provider isolates explicit rating scopes
// from the existing UI/CLI default-scope and missing-scope controls.
export function ratingsRealm(base, secret) {
  const ui = base.clients.find((client) => client.clientId === "catalog-ui");
  if (!ui || !base.users[0]) throw new Error("Missing human provider fixture");
  const scopes = [
    "catalog.read",
    "ratings.write",
    "personal.write",
    "tenant.admin",
  ];
  return {
    ...base,
    realm: "catalog-ratings",
    id: "catalog-ratings",
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
      // Keycloak user IDs are globally keyed, even across separate realms.
      {
        ...base.users[0],
        id: "00000000-0000-4000-8000-000000000015",
        realmRoles: [],
      },
      {
        id: "00000000-0000-4000-8000-000000000014",
        username: "service-account-catalog-ratings-service",
        enabled: true,
        serviceAccountClientId: "catalog-ratings-service",
      },
    ],
    clients: [
      {
        ...ui,
        // Keep distinct Keycloak storage IDs but deliberately collide the
        // signed subject with the original realm. This qualifies Catalog's
        // realm-bound principal resolution, never subject-only authority.
        protocolMappers: [
          ...structuredClone(ui.protocolMappers),
          {
            name: "same-subject-other-realm",
            protocol: "openid-connect",
            protocolMapper: "oidc-hardcoded-claim-mapper",
            config: {
              "claim.name": "sub",
              "claim.value": base.users[0].id,
              "jsonType.label": "String",
              "access.token.claim": "true",
              "id.token.claim": "true",
              "userinfo.token.claim": "true",
            },
          },
        ],
        defaultClientScopes: ["basic"],
        optionalClientScopes: [
          "catalog.read",
          "ratings.write",
          "personal.write",
          "tenant.admin",
        ],
      },
      {
        clientId: "catalog-ratings-service",
        secret,
        enabled: true,
        protocol: "openid-connect",
        publicClient: false,
        standardFlowEnabled: false,
        directAccessGrantsEnabled: false,
        implicitFlowEnabled: false,
        serviceAccountsEnabled: true,
        defaultClientScopes: ["basic", ...scopes],
        optionalClientScopes: [],
        protocolMappers: structuredClone(ui.protocolMappers),
      },
    ],
  };
}
