import { randomBytes } from "node:crypto";
import type { Browser, Item } from "./browse.js";
import { bookmarkLabel, consumerFamily, type DocumentRef } from "./browse.js";
import { mcpAlternatives } from "./mcp.js";
import { a2aPlan, a2aSettings } from "./a2a.js";
import { encodeProjection } from "./json.js";
import { record, ClientError } from "./config.js";
export function message(input: unknown): Record<string, unknown> {
  const v = record(input);
  if (v.type === "ready" && Object.keys(v).length === 1) return v;
  const keys: Record<string, string[]> = {
    search: ["query"],
    next: [],
    explore: ["query"],
    draft: ["query", "scope"],
    back: [],
    cancel: [],
    help: [],
    select: ["key"],
    tag: ["tag"],
    bookmark: [],
    reopen: ["index"],
    removeBookmark: ["index"],
    inspect: ["index"],
    mcp: ["index"],
    a2a: ["index"],
    skill: ["index"],
    plugin: ["index"],
    activatePlugin: ["index"],
    compare: ["index"],
    export: ["index"],
    open: ["index"],
    copy: [],
    manifest: [],
    image: ["index"],
    recover: ["action"],
  };
  if (
    typeof v.type !== "string" ||
    !Object.hasOwn(keys, v.type) ||
    !Number.isSafeInteger(v.revision)
  )
    throw new ClientError("invalid_message");
  const allowed = ["type", "revision", ...keys[v.type]!];
  if (
    Object.keys(v).length !== allowed.length ||
    Object.keys(v).some((k) => !allowed.includes(k))
  )
    throw new ClientError("invalid_message");
  if (
    keys[v.type]!.includes("index") &&
    (!Number.isSafeInteger(v.index) || (v.index as number) < 0)
  )
    throw new ClientError("invalid_message");
  for (const key of ["key", "tag"]) {
    if (!keys[v.type]!.includes(key)) continue;
    // Published keys include a 512-character identifier and JSON-escaped
    // 128-character version (up to six characters per escaped control).
    const max = key === "key" ? 32 + 512 + 6 * 128 : 80;
    const value = v[key];
    if (
      typeof value !== "string" ||
      value.length > 2 * max ||
      [...value].length > max ||
      value.includes("\0") ||
      /[\uD800-\uDFFF]/u.test(value)
    )
      throw new ClientError("invalid_message");
  }
  if (["search", "explore", "draft"].includes(v.type)) record(v.query);
  if (
    v.type === "draft" &&
    (!Number.isSafeInteger(v.scope) || (v.scope as number) < 0)
  )
    throw new ClientError("invalid_message");
  if (
    v.type === "recover" &&
    ![
      "addConnection",
      "connect",
      "signIn",
      "settings",
      "trust",
      "restart",
      "help",
      "account",
      "transportSettings",
      "combinedFilters",
      "rate",
      "feedback",
      "myRatings",
      "myFeedback",
      "recoverOpinion",
      "testConnection",
    ].includes(v.action as string)
  )
    throw new ClientError("invalid_message");
  return v;
}
export function guidance(
  connection: string,
  loaded: boolean,
  count: number,
  partial: boolean,
  error?: string,
) {
  if (
    error &&
    /settings|configuration|trust_file|^invalid_(?:connection|origin)|^host_storage|^issuer_|^oidc_/u.test(
      error,
    )
  )
    return {
      phase: "failed",
      message:
        "Check the connection, issuer and trust settings on this execution host. Use user or remote settings, not workspace overrides.",
      actions: ["settings", "help"],
    };
  if (connection === "none" || error === "select_connection")
    return {
      phase: "no_connection",
      message: "Add or select a Catalog connection to begin.",
      actions: ["addConnection", "connect", "help"],
    };
  if (connection === "signing_in")
    return {
      phase: "signing_in",
      message:
        "Complete browser sign-in or cancel its progress notification. A canceled or expired login needs a new Sign In.",
      actions: ["connect", "help"],
    };
  if (error === "credential_in_use" || error === "credential_update_pending")
    return {
      phase: "failed",
      message:
        "Another Catalog window is updating this connection's credentials. Retry after it finishes. If its host stopped, close other Catalog windows and use Recover Catalog Login.",
      actions: ["help"],
    };
  const authenticationError = [
    "sign_in_required",
    "unauthenticated",
    "unauthorized",
    "invalid_token",
    "token_expired",
    "authentication_required",
  ].includes(error ?? "");
  if (connection === "anonymous" && authenticationError)
    return {
      phase: "failed",
      message:
        "This publication refused anonymous access. Check its documented authentication settings or select another connection.",
      actions: ["settings", "connect"],
    };
  if (connection === "signed_out" || authenticationError)
    return {
      phase: "signed_out",
      message: "Sign in to the selected Catalog, then refresh.",
      actions: ["signIn", "testConnection", "connect", "help"],
    };
  if (
    error === "request_canceled" ||
    error === "aborted" ||
    error === "cancelled"
  )
    return {
      phase: "failed",
      message:
        "The read was canceled. Start a new search or explicitly retry the next page when available.",
      actions: ["restart", "help"],
    };
  if (
    error === "timeout" ||
    error === "request_timeout" ||
    error === "deadline_exceeded"
  )
    return {
      phase: "failed",
      message: "The read timed out. Check the connection and retry explicitly.",
      actions: ["settings", "restart", "help"],
    };
  if (error) {
    if (
      [
        "invalid_query",
        "unsupported_filter",
        "namespace_is_local",
        "published_search_needs_text",
        "facets_are_local",
      ].includes(error)
    )
      return {
        phase: "failed",
        message:
          "Check the search text and filters. Use filters supported by this connection, then search again.",
        actions: ["help"],
      };
    if (/cursor|continuation/.test(error))
      return {
        phase: "failed",
        message:
          "This continuation is no longer valid. Start a new search with the current filters.",
        actions: ["restart"],
      };
    if (/workspace.*trust|untrusted_workspace/.test(error))
      return {
        phase: "failed",
        message: "Review workspace trust before using a resource.",
        actions: ["trust", "help"],
      };
    if (/forbidden|permission|access_denied|access_refused/.test(error))
      return {
        phase: "failed",
        message:
          connection === "anonymous"
            ? "This publication refused the requested read. Check its access policy or select another connection."
            : "This account cannot perform the requested read. Check its Catalog permissions or select another connection.",
        actions: ["connect", "help"],
      };
    return {
      phase: "failed",
      message:
        "The read failed. Check the Catalog address, certificate trust and connection settings, then refresh.",
      actions: ["settings", "restart", "help"],
    };
  }
  if (!loaded)
    return {
      phase: "not_loaded",
      message: "Refresh to load the first page. No search has completed yet.",
      actions: ["restart", "testConnection"],
    };
  if (partial)
    return {
      phase: "partial",
      message:
        "These results are partial. Review the result summary before continuing.",
      actions: ["restart"],
    };
  if (!count)
    return {
      phase: "empty",
      message:
        "No resources match this page. Change the filters or start a new search.",
      actions: ["restart"],
    };
  return { phase: "loaded", message: "", actions: [] };
}
function card(item: Item) {
  return {
    key: item.key,
    kind: item.kind,
    name: item.name,
    description: item.description,
    tags: item.tags,
    origin: item.origin,
    versionPresent: item.versionPresent,
    ...(item.versionPresent ? { version: item.version } : {}),
    ...(item.publisher ? { publisher: item.publisher } : {}),
    ...(item.namespace ? { namespace: item.namespace } : {}),
    ...(item.namespaceId ? { namespaceId: item.namespaceId } : {}),
    ...(item.license ? { license: item.license } : {}),
    ...(item.visibility ? { visibility: item.visibility } : {}),
    ...(item.ratings ? { ratings: item.ratings } : {}),
    ...(item.sourceCount !== undefined
      ? {
          sourceCount: item.sourceCount,
          conflict: item.conflict,
          sameSourceConflict: item.sameSourceConflict,
        }
      : {}),
  };
}
function artworkSlots(item: Item): { index: number; label: string }[] {
  const value = item.artwork?.descriptor;
  if (
    !item.artworkTarget ||
    !value ||
    typeof value !== "object" ||
    Array.isArray(value)
  )
    return [];
  const d = record(value);
  return [
    ...(d.icon ? [{ index: 0, label: "Load icon from Catalog" }] : []),
    ...(Array.isArray(d.screenshots)
      ? d.screenshots.slice(0, 8).map((_, index) => ({
          index: index + 1,
          label: `Load screenshot ${index + 1} from Catalog`,
        }))
      : []),
  ];
}
export function projection(
  browser: Browser,
  mode: string,
  revision: number,
  connection = "unverified",
) {
  const s = browser.state;
  return {
    revision,
    scope: s.scope,
    mode,
    connection,
    canBrowse: !["none", "signed_out", "signing_in"].includes(connection),
    guidance: guidance(
      connection,
      !!s.page,
      s.page?.items.length ?? 0,
      s.page?.partial ?? false,
      s.error,
    ),
    busy: s.busy,
    error: s.error,
    query: s.query,
    summary: s.page
      ? `${s.page.items.length} shown on this page; ${s.page.summary}`
      : undefined,
    items: s.page?.items.map(card) ?? [],
    next: s.canNext,
    facets: s.facets,
    facetQuery: s.facetQuery,
    image: s.image,
    bookmarks: s.bookmarks.map((reference, index) => ({
      index,
      label: bookmarkLabel(reference),
    })),
    detail: s.detail
      ? {
          item: card(s.detail.item),
          identifier: s.detail.item.identifier,
          bookmark: ["local", "indexed"].includes(s.detail.item.origin),
          references: s.detail.references.map((r) => ({
            label: r.label,
            value: r.value,
            open: !!r.url,
          })),
          facts: s.detail.facts,
          documents: s.detail.documents.map((d) => ({
            label: d.label,
            ...documentAction(d, s.detail!.item.kind),
            exact: d.path !== undefined || d.content !== undefined,
            readable:
              d.path !== undefined ||
              d.value !== undefined ||
              d.content !== undefined,
          })),
          packages: ["skill", "plugin"].includes(s.detail.item.kind)
            ? (s.detail.packages ?? []).map((_, index) => ({
                index,
                label:
                  s.detail!.item.kind === "plugin"
                    ? "Review and install Plugin disabled"
                    : "Review and install complete Skill",
                type: s.detail!.item.kind === "plugin" ? "plugin" : "skill",
              }))
            : [],
          artwork: artworkSlots(s.detail.item),
        }
      : undefined,
  };
}
export function documentAction(
  document: DocumentRef,
  kind: string,
): { action?: "a2a" | "mcp"; reason?: string; notice?: string } {
  const action = consumerFamily(document, kind);
  if (!action)
    return ["agent", "mcp_server"].includes(kind)
      ? {
          reason:
            "Inspect-only: this document is not a supported native Agent Card, MCP Server Card or Registry server.json.",
        }
      : {};
  if (document.size !== undefined && document.size > 1024 ** 2)
    return {
      reason:
        "Inspect-only: native consumption accepts documents up to 1 MiB. Inspection and export have separate limits.",
    };
  if (document.value !== undefined) {
    try {
      const bytes = Buffer.from(encodeProjection(document.value));
      if (bytes.length > 1024 ** 2)
        return {
          reason:
            "Inspect-only: native consumption accepts documents up to 1 MiB.",
        };
      if (action === "mcp") {
        const alternatives = mcpAlternatives(bytes, true, document.mediaType);
        if (!alternatives.some((option) => option.recipe))
          return {
            reason: `${
              alternatives
                .map((option) => option.reason)
                .filter(Boolean)
                .join("; ") || "no supported MCP declaration"
            }.`,
          };
        return {
          action,
          notice:
            [
              ...new Set(
                alternatives.flatMap((option) =>
                  option.recipe?.command
                    ? [
                        `Requires ${option.recipe.command} on this execution host.`,
                      ]
                    : [],
                ),
              ),
            ].join(" ") ||
            "Native VS Code owns target TLS, OAuth and startup approvals.",
        };
      }
      const plan = a2aPlan(bytes, a2aSettings({ allowLoopbackHTTP: true }));
      if (!plan.interfaces.some((i) => i.url && !i.reason))
        return {
          reason:
            "Inspect-only: " +
            [
              ...new Set(plan.interfaces.map((i) => i.reason).filter(Boolean)),
            ].join(" "),
        };
      if (!plan.auth.some((a) => !a.reason))
        return {
          reason:
            "Inspect-only: " +
            [...new Set(plan.auth.map((a) => a.reason).filter(Boolean))].join(
              " ",
            ),
        };
    } catch (error) {
      return {
        reason: `Inspect-only: ${error instanceof ClientError ? error.code : "native_document_unavailable"}.`,
      };
    }
  }
  return {
    action,
    notice:
      "Exact native evidence, destination, target credentials and consent are checked before use. No runtime is installed automatically.",
  };
}
export function html(script: string, style: string, source: string): string {
  const nonce = randomBytes(24).toString("base64");
  return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'nonce-${nonce}'; style-src ${source}; img-src data:; base-uri 'none'; form-action 'none';"><link rel="stylesheet" href="${style}"><title>Open Agentic Catalog</title></head><body><main id="catalog"><h1>Catalog</h1><p>Select a connection, then refresh to browse.</p></main><script nonce="${nonce}" src="${script}"></script></body></html>`;
}
