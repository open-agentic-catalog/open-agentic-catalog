// Decode the UI host's public settings and authentication replies before use.
import { permissionKeys } from "./permission-keys";
import type { components } from "./schema";

type Access = components["schemas"]["CurrentAccess"];
export type Runtime = {
  basePath: string;
  authMode: "development" | "oidc";
  documentationUrl: string;
};
export type Session = { access: Access; expiresAt: number };
export type SessionReply = Session | { access: null; expiresAt: 0 };
export type LoginOptions = {
  mode: Runtime["authMode"];
  users: {
    alias: string;
    label: string;
    description?: string;
    profileGroup?: "bundle" | "template";
    elevated?: boolean;
    separateTenant?: boolean;
  }[];
};
export type LoginLocation = { location: string };

const scopes = [
  "audit.export",
  "audit.read",
  "catalog.publish",
  "catalog.read",
  "catalog.review",
  "catalog.write",
  "ratings.write",
  "feedback.write",
  "personal.write",
  "publishers.manage",
  "publishers.read",
  "sources.manage",
  "sources.read",
  "tenant.admin",
  "webhooks.manage",
] as const satisfies readonly Access["scopes"][number][];

/** Narrow JSON objects without treating arrays or null as control envelopes. */
function object(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

/** Use JSON Schema character counts rather than UTF-16 code-unit counts. */
function text(value: unknown, min: number, max: number): value is string {
  return (
    typeof value === "string" &&
    [...value].length >= min &&
    [...value].length <= max
  );
}

/** Identity fields use the API's UUID format, without imposing a UUID version. */
function uuid(value: unknown): value is string {
  return (
    typeof value === "string" &&
    /^[\da-f]{8}-[\da-f]{4}-[\da-f]{4}-[\da-f]{4}-[\da-f]{12}$/i.test(value)
  );
}

/** Access arrays contain only recognized, unique role or scope hints. */
function choices(value: unknown, allowed: readonly string[]): boolean {
  return (
    Array.isArray(value) &&
    value.length <= allowed.length &&
    value.every(
      (item: unknown) => typeof item === "string" && allowed.includes(item),
    ) &&
    new Set(value).size === value.length
  );
}

/** Check every CurrentAccess field used by permission hints and namespace paging. */
export function checkAccess(value: unknown): value is Access {
  return (
    object(value) &&
    uuid(value.tenantId) &&
    uuid(value.membershipId) &&
    uuid(value.realmId) &&
    text(value.subject, 1, 512) &&
    (value.actorType === "human" || value.actorType === "service") &&
    choices(value.permissions, permissionKeys) &&
    choices(value.availablePermissions, permissionKeys) &&
    Number.isSafeInteger(value.authorizationRevision) &&
    typeof value.authorizationRevision === "number" &&
    value.authorizationRevision > 0 &&
    typeof value.scopeEnforcement === "boolean" &&
    choices(value.scopes, scopes) &&
    (value.nextCursor === undefined || text(value.nextCursor, 0, 2048)) &&
    Array.isArray(value.namespaceGrants) &&
    value.namespaceGrants.length <= 100 &&
    value.namespaceGrants.every(
      (grant: unknown) =>
        object(grant) &&
        uuid(grant.namespaceId) &&
        choices(grant.permissions, permissionKeys),
    )
  );
}

/** Reject malformed host replies without retaining their contents in an error. */
function invalid(): never {
  throw new Error("Invalid UI reply.");
}

/** Accept absolute HTTP(S) URLs without credentials, controls or backslashes. */
function httpURL(value: string): URL | undefined {
  if (
    !/^https?:\/\/[^/?#\\\s@]+(?:[/?#]|$)/i.test(value) ||
    /[\p{Cc}\\]/u.test(value)
  )
    return undefined;
  try {
    return new URL(value);
  } catch {
    return undefined;
  }
}

/** This host-owned destination is separate from resource metadata links. */
export function documentationLink(value: string): string | undefined {
  if (value === "/docs/") return value;
  const url = httpURL(value);
  return url?.protocol === "https:" && !value.includes("?") && url.hash === ""
    ? value
    : undefined;
}

/** Only the bundled manual has known guide paths; external overrides stay exact. */
export function documentationGuideLink(
  value: string,
  guide:
    | "configuration"
    | "publishing"
    | "resource-authoring"
    | "draft-transfer"
    | "agent-authoring"
    | "mcp-authoring"
    | "skill-authoring"
    | "agent-example"
    | "mcp-example"
    | "skill-example"
    | "plugin-example"
    | "database-roles"
    | "sign-in"
    | "security"
    | "feedback"
    | "ratings"
    | "audit"
    | "webhooks"
    | "observability"
    | "hybrid-search"
    | "ans-assessments"
    | "webhook-receiver",
): { href: string; label: string } | undefined {
  const href = documentationLink(value);
  if (!href) return undefined;
  if (href !== "/docs/") return { href, label: "Documentation" };
  if (
    guide === "observability" ||
    guide === "hybrid-search" ||
    guide === "ans-assessments"
  )
    return {
      href:
        guide === "ans-assessments"
          ? "/docs/operations/federation/#read-a-private-ans-assessment"
          : `/docs/operations/${guide}/`,
      label: {
        observability: "Service observations guide",
        "hybrid-search": "Search indexing guide",
        "ans-assessments": "Private ANS assessment guide",
      }[guide],
    };
  const nativeGuides = {
    "agent-authoring": ["create-an-agent-from-fields", "Agent authoring guide"],
    "mcp-authoring": ["create-mcp-metadata-from-fields", "MCP authoring guide"],
    "skill-authoring": ["write-a-skill-from-fields", "Skill authoring guide"],
    "agent-example": ["agent-retain-an-a2a-agent-card", "Agent native example"],
    "mcp-example": [
      "mcp-server-retain-an-experimental-server-card",
      "MCP native examples",
    ],
    "skill-example": [
      "skill-retain-a-standalone-markdown-document",
      "Skill native examples",
    ],
    "plugin-example": [
      "prepare-plugin-files-from-fields",
      "Plugin file preparation",
    ],
  } as const;
  if (Object.hasOwn(nativeGuides, guide)) {
    const [anchor, label] = nativeGuides[guide as keyof typeof nativeGuides];
    return { href: `/docs/guides/resource-authoring/#${anchor}`, label };
  }
  if (guide === "webhooks")
    return {
      href: "/docs/operations/webhooks/",
      label: "Webhook operations guide",
    };
  if (guide === "webhook-receiver")
    return {
      href: "/docs/reference/receiver-example/",
      label: "Webhook receiver example",
    };
  if (guide === "database-roles" || guide === "sign-in" || guide === "security")
    return {
      href: `/docs/operations/${guide}/`,
      label: {
        "database-roles": "Database roles guide",
        "sign-in": "Authentication and sign-in guide",
        security: "Security and credential configuration",
      }[guide],
    };
  if (guide === "audit" || guide === "feedback" || guide === "ratings")
    return {
      href: `/docs/${guide === "audit" ? "operations" : "guides"}/${guide}/`,
      label: {
        audit: "Audit investigation guide",
        feedback: "Feedback guide",
        ratings: "Ratings guide",
      }[guide],
    };
  if (guide === "resource-authoring")
    return {
      href: "/docs/guides/resource-authoring/",
      label: "Resource authoring guide",
    };
  if (guide === "draft-transfer")
    return {
      href: "/docs/guides/draft-transfer/",
      label: "Move drafts between Catalogs",
    };
  return guide === "configuration"
    ? {
        href: "/docs/reference/configuration/",
        label: "Configuration documentation",
      }
    : { href: "/docs/guides/publishing/", label: "Operator documentation" };
}

/** Runtime routing matches the host's canonical root/subpath and documentation policy. */
export function decodeRuntime(value: unknown): Runtime {
  if (
    !object(value) ||
    typeof value.basePath !== "string" ||
    !/^(?:\/[a-zA-Z0-9_-]+)*$/.test(value.basePath) ||
    value.basePath === "/docs" ||
    value.basePath.startsWith("/docs/") ||
    (value.authMode !== "development" && value.authMode !== "oidc") ||
    typeof value.documentationUrl !== "string"
  )
    invalid();
  if (
    value.documentationUrl !== "" &&
    !documentationLink(value.documentationUrl)
  )
    invalid();
  return {
    basePath: value.basePath,
    authMode: value.authMode,
    documentationUrl: value.documentationUrl,
  };
}

/** Expired valid sessions are signed out; malformed or unusable expiry values fail safely. */
export function decodeSession(value: unknown): SessionReply {
  if (!object(value)) invalid();
  if (value.access === null && value.expiresAt === 0)
    return { access: null, expiresAt: 0 };
  if (
    !checkAccess(value.access) ||
    typeof value.expiresAt !== "number" ||
    value.expiresAt <= 0 ||
    !Number.isSafeInteger(value.expiresAt) ||
    !Number.isSafeInteger(value.expiresAt * 1000) ||
    value.expiresAt * 1000 - Date.now() > 2 ** 31 - 1
  )
    invalid();
  if (value.expiresAt * 1000 <= Date.now())
    return { access: null, expiresAt: 0 };
  return { access: value.access, expiresAt: value.expiresAt };
}

/** Login options must agree with bootstrap and the host's mounted alias/label constraints. */
export function decodeLoginOptions(
  value: unknown,
  runtime: Runtime,
): LoginOptions {
  if (
    !object(value) ||
    value.mode !== runtime.authMode ||
    !Array.isArray(value.users) ||
    (value.mode === "development" &&
      (value.users.length < 1 || value.users.length > 32)) ||
    (value.mode === "oidc" && value.users.length !== 0)
  )
    invalid();
  const aliases = new Set<string>();
  const users = value.users.map((user: unknown) => {
    if (
      !object(user) ||
      typeof user.alias !== "string" ||
      !/^[a-z][a-z0-9-]{0,47}$/.test(user.alias) ||
      aliases.has(user.alias) ||
      typeof user.label !== "string" ||
      !user.label.trim() ||
      new TextEncoder().encode(user.label).length > 100 ||
      /[\p{Cc}]/u.test(user.label) ||
      (user.profileGroup !== undefined &&
        user.profileGroup !== "bundle" &&
        user.profileGroup !== "template") ||
      (user.elevated !== undefined && typeof user.elevated !== "boolean") ||
      (user.separateTenant !== undefined &&
        typeof user.separateTenant !== "boolean") ||
      (user.description !== undefined &&
        (typeof user.description !== "string" ||
          new TextEncoder().encode(user.description).length > 300 ||
          /[\p{Cc}]/u.test(user.description)))
    )
      invalid();
    aliases.add(user.alias);
    return {
      alias: user.alias,
      label: user.label,
      ...(user.profileGroup !== undefined
        ? { profileGroup: user.profileGroup as "bundle" | "template" }
        : {}),
      ...(user.elevated !== undefined
        ? { elevated: user.elevated as boolean }
        : {}),
      ...(user.separateTenant !== undefined
        ? { separateTenant: user.separateTenant as boolean }
        : {}),
      ...(typeof user.description === "string"
        ? { description: user.description }
        : {}),
    };
  });
  return { mode: runtime.authMode, users };
}

/** Preserve the exact approved authorization query; never allow executable or credentialed navigation. */
export function decodeLoginLocation(
  value: unknown,
  runtime: Runtime,
): LoginLocation {
  if (!object(value) || typeof value.location !== "string") invalid();
  if (runtime.authMode === "development") {
    if (value.location !== runtime.basePath + "/catalog") invalid();
  } else if (!httpURL(value.location) || value.location.includes("#"))
    invalid();
  return { location: value.location };
}

/** Translate only fixed host login codes. Provider text never becomes UI copy. */
export function loginFailureMessage(code: string): string {
  switch (code) {
    case "sign_in_unavailable":
      return "Sign-in is temporarily unavailable. Try again shortly.";
    case "invalid_login_callback":
    case "invalid_login_state":
      return "This sign-in attempt expired or is invalid. Sign in again.";
    case "login_not_completed":
      return "Sign-in was not completed. Try again.";
    case "login_exchange_failed":
      return "Sign-in could not be completed. Try again or contact your administrator.";
    case "identity_provider_unavailable":
      return "The sign-in service is unavailable. Try again shortly.";
    case "catalog_login_denied":
      return "The Catalog did not accept this account. Ask an administrator to check your access.";
    case "catalog_unavailable":
      return "The Catalog is unavailable. Try again shortly.";
    case "invalid_login_identity":
    case "invalid_access_token_expiry":
      return "The sign-in response could not be verified. Contact your administrator.";
    case "session_token_too_large":
      return "Your account could not start a session. Contact your administrator.";
    default:
      return "";
  }
}

/** Query values supply a fixed notice, never identity or a redirect destination. */
export function loginFailureFromSearch(search = ""): string {
  const codes = new URLSearchParams(search).getAll("sign_in_error");
  const code = codes[0];
  if (code === undefined || codes.length !== 1) return "";
  return loginFailureMessage(code);
}
