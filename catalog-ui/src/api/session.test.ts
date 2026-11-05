import { testAccess, testPermissions } from "../../tests/unit/access-fixtures";
// Host-shaped controls exercise runtime values independently of component mocks.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { sessionAccess } from "../../tests/unit/fixtures";
import {
  loginFailureFromSearch,
  decodeRuntime,
  decodeSession,
  decodeLoginOptions,
  decodeLoginLocation,
  documentationLink,
  documentationGuideLink,
} from "./session";

const runtime = {
  basePath: "/nested",
  authMode: "development" as const,
  documentationUrl: "",
};
const now = Date.UTC(2026, 8, 28, 12);
const session = () => ({
  access: { ...sessionAccess },
  expiresAt: now / 1000 + 600,
});
beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(now);
});
afterEach(() => {
  vi.useRealTimers();
});

describe("public runtime configuration", () => {
  it.each([
    [
      "configuration",
      "/docs/reference/configuration/",
      "Configuration documentation",
    ],
    ["publishing", "/docs/guides/publishing/", "Operator documentation"],
    [
      "database-roles",
      "/docs/operations/database-roles/",
      "Database roles guide",
    ],
    [
      "sign-in",
      "/docs/operations/sign-in/",
      "Authentication and sign-in guide",
    ],
    [
      "security",
      "/docs/operations/security/",
      "Security and credential configuration",
    ],
    ["feedback", "/docs/guides/feedback/", "Feedback guide"],
    ["ratings", "/docs/guides/ratings/", "Ratings guide"],
    ["audit", "/docs/operations/audit/", "Audit investigation guide"],
    ["webhooks", "/docs/operations/webhooks/", "Webhook operations guide"],
    [
      "observability",
      "/docs/operations/observability/",
      "Service observations guide",
    ],
    [
      "hybrid-search",
      "/docs/operations/hybrid-search/",
      "Search indexing guide",
    ],
    [
      "ans-assessments",
      "/docs/operations/federation/#read-a-private-ans-assessment",
      "Private ANS assessment guide",
    ],
    [
      "webhook-receiver",
      "/docs/reference/receiver-example/",
      "Webhook receiver example",
    ],
    [
      "resource-authoring",
      "/docs/guides/resource-authoring/",
      "Resource authoring guide",
    ],
    [
      "draft-transfer",
      "/docs/guides/draft-transfer/",
      "Move drafts between Catalogs",
    ],
    [
      "agent-authoring",
      "/docs/guides/resource-authoring/#create-an-agent-from-fields",
      "Agent authoring guide",
    ],
    [
      "mcp-authoring",
      "/docs/guides/resource-authoring/#create-mcp-metadata-from-fields",
      "MCP authoring guide",
    ],
    [
      "agent-example",
      "/docs/guides/resource-authoring/#agent-retain-an-a2a-agent-card",
      "Agent native example",
    ],
    [
      "mcp-example",
      "/docs/guides/resource-authoring/#mcp-server-retain-an-experimental-server-card",
      "MCP native examples",
    ],
    [
      "skill-authoring",
      "/docs/guides/resource-authoring/#write-a-skill-from-fields",
      "Skill authoring guide",
    ],
    [
      "skill-example",
      "/docs/guides/resource-authoring/#skill-retain-a-standalone-markdown-document",
      "Skill native examples",
    ],
    [
      "plugin-example",
      "/docs/guides/resource-authoring/#prepare-plugin-files-from-fields",
      "Plugin file preparation",
    ],
  ] as const)(
    "links only the bundled %s guide and preserves an external override",
    (guide, href, label) => {
      expect(documentationGuideLink("/docs/", guide)).toEqual({ href, label });
      const external = "https://manual.example.test/custom%20guide/#";
      expect(documentationGuideLink(external, guide)).toEqual({
        href: external,
        label: "Documentation",
      });
      for (const value of [
        "",
        "/nested/docs/",
        "/docs/guide/",
        "https://manual.example.test/?token=private",
        "http://manual.example.test/",
        "https://user:private@manual.example.test/",
      ])
        expect(documentationGuideLink(value, guide)).toBeUndefined();
    },
  );
  it("accepts only the exact bundled destination alongside the HTTPS override", () => {
    expect(
      decodeRuntime({ ...runtime, documentationUrl: "/docs/" })
        .documentationUrl,
    ).toBe("/docs/");
    expect(documentationLink("")).toBeUndefined();
    expect(documentationLink("/docs/")).toBe("/docs/");
  });
  it.each(["", "/catalog-ui", "/nested/ABC_123-x"])(
    "accepts canonical base %s and HTTPS docs",
    (basePath) => {
      const value = {
        ...runtime,
        basePath,
        authMode: "oidc",
        documentationUrl: "https://docs.example.test/guide%20one",
        ignored: "extension",
      };
      expect(decodeRuntime(value)).toEqual({
        basePath,
        authMode: "oidc",
        documentationUrl: value.documentationUrl,
      });
      expect(decodeRuntime(runtime)).toEqual(runtime);
    },
  );
  it("preserves an empty documentation fragment accepted by the host", () => {
    const value = { ...runtime, documentationUrl: "https://docs.test/#" };
    expect(decodeRuntime(value)).toEqual(value);
  });
  it.each([
    null,
    [],
    "private runtime",
    {},
    ...[
      null,
      1,
      "/",
      "//evil.test",
      "/a/",
      "/a/../b",
      "/a%2fb",
      "/a?x",
      "/a\\b",
      "/docs",
      "/docs/manual",
    ].map((basePath) => ({ ...runtime, basePath })),
    { ...runtime, authMode: "unknown" },
    { ...runtime, documentationUrl: null },
    ...[
      "http://docs.test",
      "/docs",
      "/docs/guide/",
      "/docs/?token=private",
      "/docs/#section",
      "/%64ocs/",
      "/docs/../catalog",
      "//docs.test",
      "javascript:alert(1)",
      "https://user:private@docs.test",
      "https://@docs.test",
      "https://[",
      "https://docs.test?",
      "https://docs.test#private",
      "https://docs.test/\nprivate",
      "https://docs.test\\private",
    ].map((documentationUrl) => ({ ...runtime, documentationUrl })),
  ])(
    "rejects invalid bootstrap without returning its contents: %#",
    (value) => {
      expect(() => decodeRuntime(value)).toThrow("Invalid UI reply.");
    },
  );
});

describe("session self-context", () => {
  it("preserves complete identity/grants without inferring additional permissions", () => {
    const value = session();
    value.access = {
      ...sessionAccess,
      actorType: "service",
      subject: "😀".repeat(512),
      ...testAccess(["reader", "contributor"]),
      scopes: ["catalog.read"],
      scopeEnforcement: true,
      namespaceGrants: [
        {
          namespaceId: "ABCDEF12-3456-7890-ABCD-EF1234567890",
          permissions: testPermissions(["publisher"]),
        },
      ],
      nextCursor: "opaque-next",
    };
    expect(decodeSession(value)).toEqual(value);
    expect(decodeSession(session())).toEqual(session());
    expect(
      decodeSession({
        ...value,
        access: {
          ...value.access,
          ...testAccess([]),
          scopes: [],
          nextCursor: "",
          namespaceGrants: Array.from({ length: 100 }, () => ({
            namespaceId: sessionAccess.tenantId,
            permissions: testPermissions([]),
          })),
        },
      }),
    ).toMatchObject({
      access: {
        ...testAccess([]),
        scopes: [],
        namespaceGrants: expect.any(Array) as unknown,
      },
    });
  });
  it("treats explicit sign-out and valid expired sessions as signed out", () => {
    const out = { access: null, expiresAt: 0 };
    expect(decodeSession(out)).toEqual(out);
    for (const expiresAt of [now / 1000, now / 1000 - 1])
      expect(decodeSession({ ...session(), expiresAt })).toEqual(out);
  });
  it.each([
    null,
    [],
    false,
    {},
    { access: null, expiresAt: 10 },
    { access: null },
    ...[
      undefined,
      null,
      0,
      -1,
      "123",
      Number.NaN,
      Infinity,
      1.5,
      Number.MAX_SAFE_INTEGER,
      (now + 2 ** 31) / 1000 + 1,
    ].map((expiresAt) => ({ ...session(), expiresAt })),
    ...[null, [], "reader", {}].map((access) => ({ ...session(), access })),
    ...["tenantId", "membershipId", "realmId"].flatMap((key) =>
      [null, "private"].map((value) => ({
        ...session(),
        access: { ...sessionAccess, [key]: value },
      })),
    ),
    ...["", null, "x".repeat(513)].map((subject) => ({
      ...session(),
      access: { ...sessionAccess, subject },
    })),
    ...[null, "robot"].map((actorType) => ({
      ...session(),
      access: { ...sessionAccess, actorType },
    })),
    ...[
      "reader",
      null,
      ["other"],
      [1],
      ["reader", "reader"],
      Array(9).fill("reader"),
    ].map((roles) => ({
      ...session(),
      access: { ...sessionAccess, permissions: roles },
    })),
    ...[
      null,
      "catalog.read",
      ["unknown"],
      ["catalog.read", "catalog.read"],
      Array(14).fill("catalog.read"),
    ].map((scopes) => ({ ...session(), access: { ...sessionAccess, scopes } })),
    ...[null, "false", 0].map((scopeEnforcement) => ({
      ...session(),
      access: { ...sessionAccess, scopeEnforcement },
    })),
    ...[null, 7, "c".repeat(2049)].map((nextCursor) => ({
      ...session(),
      access: { ...sessionAccess, nextCursor },
    })),
    ...[
      null,
      {},
      [null],
      [{}],
      [{ namespaceId: "bad", permissions: testPermissions([]) }],
      [{ namespaceId: sessionAccess.tenantId, permissions: "resource.read" }],
      Array(101).fill({
        namespaceId: sessionAccess.tenantId,
        permissions: testPermissions([]),
      }),
    ].map((namespaceGrants) => ({
      ...session(),
      access: { ...sessionAccess, namespaceGrants },
    })),
  ])("rejects malformed identity/expiry before use: %#", (value) => {
    expect(() => decodeSession(value)).toThrow("Invalid UI reply.");
  });
  it("accepts the largest representable timer delay but rejects the next expiry second", () => {
    const latest = Math.floor((now + 2 ** 31 - 1) / 1000);
    expect(decodeSession({ ...session(), expiresAt: latest })).toMatchObject({
      expiresAt: latest,
    });
    expect(() =>
      decodeSession({ ...session(), expiresAt: latest + 1 }),
    ).toThrow("Invalid UI reply.");
  });
});

describe("login inputs from the host", () => {
  const options = {
    mode: "development",
    users: [{ alias: "reader", label: "Reader" }],
  };
  it("accepts mounted aliases, UTF-8 label bounds and empty provider options", () => {
    expect(decodeLoginOptions(options, runtime)).toEqual(options);
    expect(
      decodeLoginOptions(
        {
          mode: "development",
          users: [{ alias: "a".repeat(48), label: "é".repeat(50) }],
        },
        runtime,
      ).users,
    ).toHaveLength(1);
    expect(
      decodeLoginOptions(
        { mode: "oidc", users: [] },
        { ...runtime, authMode: "oidc" },
      ),
    ).toEqual({ mode: "oidc", users: [] });
  });
  it("accepts the host's maximum development identity count", () => {
    const users = Array.from({ length: 32 }, (_, i) => ({
      alias: `user-${i}`,
      label: `User ${i}`,
    }));
    expect(decodeLoginOptions({ mode: "development", users }, runtime)).toEqual(
      {
        mode: "development",
        users,
      },
    );
  });
  it("preserves explicit presentation flags without making them role claims", () => {
    const users = [
      {
        alias: "exact",
        label: "Configured",
        profileGroup: "template" as const,
        elevated: false,
        separateTenant: true,
      },
      {
        alias: "workflow",
        label: "Workflow",
        profileGroup: "bundle" as const,
        elevated: true,
        separateTenant: false,
      },
    ];
    expect(decodeLoginOptions({ mode: "development", users }, runtime)).toEqual(
      { mode: "development", users },
    );
  });
  it.each([
    null,
    [],
    {},
    { ...options, mode: "oidc" },
    { ...options, users: null },
    { ...options, users: {} },
    { ...options, users: [] },
    {
      ...options,
      users: Array.from({ length: 33 }, (_, i) => ({
        alias: `user-${i}`,
        label: `User ${i}`,
      })),
    },
    ...[
      null,
      [],
      {},
      { alias: 1, label: "Reader" },
      { alias: "A", label: "Reader" },
      { alias: "a".repeat(49), label: "Reader" },
      { alias: "a", label: null },
      { alias: "a", label: "  " },
      { alias: "a", label: "é".repeat(51) },
      { alias: "a", label: "Reader", profileGroup: "administrator" },
      { alias: "a", label: "Reader", profileGroup: null },
      { alias: "a", label: "Reader", elevated: "true" },
      { alias: "a", label: "Reader", elevated: null },
      { alias: "a", label: "Reader", separateTenant: 1 },
      { alias: "a", label: "Reader", separateTenant: null },
    ].map((user) => ({ ...options, users: [user] })),
    { ...options, users: [...options.users, ...options.users] },
  ])("rejects malformed login options: %#", (value) => {
    expect(() => decodeLoginOptions(value, runtime)).toThrow(
      "Invalid UI reply.",
    );
  });
  it("accepts bounded readable descriptions and drops server-only properties", () => {
    expect(
      decodeLoginOptions(
        {
          ...options,
          users: [
            {
              alias: "a",
              label: "Reader",
              description: "Read local resources",
              token: "private",
            },
          ],
        },
        runtime,
      ).users,
    ).toEqual([
      { alias: "a", label: "Reader", description: "Read local resources" },
    ]);
  });
  it.each([
    null,
    1,
    [],
    "x".repeat(301),
    "é".repeat(151),
    "line\ncontrol",
    "\u0007",
  ])("rejects invalid login descriptions: %s", (description) => {
    expect(() =>
      decodeLoginOptions(
        { ...options, users: [{ alias: "a", label: "Reader", description }] },
        runtime,
      ),
    ).toThrow("Invalid UI reply.");
  });
  it("refuses development choices in provider mode", () => {
    expect(() =>
      decodeLoginOptions(
        { ...options, mode: "oidc" },
        { ...runtime, authMode: "oidc" },
      ),
    ).toThrow("Invalid UI reply.");
  });
  it("preserves exact development destinations and cross-origin provider query values", () => {
    expect(
      decodeLoginLocation({ location: "/nested/catalog" }, runtime),
    ).toEqual({ location: "/nested/catalog" });
    expect(
      decodeLoginLocation(
        { location: "/catalog" },
        { ...runtime, basePath: "" },
      ),
    ).toEqual({ location: "/catalog" });
    for (const location of [
      "https://issuer.example.test/authorize?state=a%2Bb&scope=openid+profile",
      "http://127.0.0.1:9090/authorize?code_challenge=abc",
    ]) {
      expect(
        decodeLoginLocation({ location }, { ...runtime, authMode: "oidc" }),
      ).toEqual({ location });
    }
  });
  it.each([
    null,
    [],
    {},
    { location: 1 },
    { location: "https://issuer.test" },
    { location: "/other/catalog" },
  ])("rejects invalid development locations: %#", (value) => {
    expect(() => decodeLoginLocation(value, runtime)).toThrow(
      "Invalid UI reply.",
    );
  });
  it.each([
    "/nested/catalog",
    "//issuer.test",
    "javascript:alert(1)",
    "data:text/html,private",
    "https://user:private@issuer.test",
    "https://issuer.test/auth#token",
    "https://issuer.test/auth#",
    "https://issuer.test/\nprivate",
    "https://issuer.test\\private",
    "https://[",
    "https:///issuer.test",
  ])("rejects unsafe provider location: %#", (location) => {
    expect(() =>
      decodeLoginLocation({ location }, { ...runtime, authMode: "oidc" }),
    ).toThrow("Invalid UI reply.");
  });
});

describe("fixed sign-in failure notices", () => {
  it.each([
    [
      "sign_in_unavailable",
      "Sign-in is temporarily unavailable. Try again shortly.",
    ],
    [
      "invalid_login_callback",
      "This sign-in attempt expired or is invalid. Sign in again.",
    ],
    ["login_not_completed", "Sign-in was not completed. Try again."],
    [
      "login_exchange_failed",
      "Sign-in could not be completed. Try again or contact your administrator.",
    ],
    [
      "invalid_login_identity",
      "The sign-in response could not be verified. Contact your administrator.",
    ],
    [
      "invalid_access_token_expiry",
      "The sign-in response could not be verified. Contact your administrator.",
    ],
    [
      "session_token_too_large",
      "Your account could not start a session. Contact your administrator.",
    ],
    [
      "catalog_login_denied",
      "The Catalog did not accept this account. Ask an administrator to check your access.",
    ],
    [
      "identity_provider_unavailable",
      "The sign-in service is unavailable. Try again shortly.",
    ],
    ["catalog_unavailable", "The Catalog is unavailable. Try again shortly."],
    [
      "invalid_login_state",
      "This sign-in attempt expired or is invalid. Sign in again.",
    ],
  ])("describes %s without guessing at membership", (code, message) => {
    expect(loginFailureFromSearch("?sign_in_error=" + code)).toBe(message);
  });
  it.each([
    "?sign_in_error=constructor",
    "?sign_in_error=privateSentinel",
    "?sign_in_error=%3Cscript%3Ehostile()%3C/script%3E",
    "?sign_in_error=catalog_login_denied&sign_in_error=invalid_login_state",
    "",
  ])("ignores arbitrary or repeated notices: %s", (query) => {
    expect(loginFailureFromSearch(query)).toBe("");
  });
});
