import { createCatalogClient } from "../../../sdk/typescript/src/index.js";
import type { components } from "../../../sdk/typescript/src/index.js";
const client = createCatalogClient({ origin: "https://catalog.example.com" });
void client.api.GET("/api/v1/federation/snapshots/{snapshotId}/comparison", {
  params: {
    path: { snapshotId: "before" },
    query: {
      otherSnapshotId: "after",
      view: "all",
      limit: 25,
      cursor: "opaque",
    },
  },
});
void client.api.GET("/api/v1/federation/snapshots/{snapshotId}/comparison", {
  // @ts-expect-error An explicit after snapshot is required.
  params: { path: { snapshotId: "before" }, query: { view: "all" } },
});
void client.api.GET("/api/v1/federation/snapshots/{snapshotId}/comparison", {
  params: {
    path: { snapshotId: "before" },
    // @ts-expect-error Comparison view is finite.
    query: { otherSnapshotId: "after", view: "newest" },
  },
});
void client.api.GET("/api/v1/namespaces", {
  params: { query: { slug: "tools", displayName: "Tools", limit: 25 } },
});
void client.api.GET("/api/v1/publishers", {
  params: {
    query: { displayName: "Publisher", declaredDomain: "example.com" },
  },
});
void client.api.GET("/.well-known/ard.json");
void client.api.POST("/api/v1/catalog/search", {
  body: { text: "weather", limit: 10 },
});
void client.api.POST("/api/v1/resources", {
  params: { header: { "Idempotency-Key": "key" } },
  body: {
    namespaceId: "id",
    kind: "agent",
    canonicalIdentifier: "urn:air:catalog.example.com:demo:agent",
  },
});
// @ts-expect-error Search is POST, not GET.
void client.api.GET("/api/v1/catalog/search");
// @ts-expect-error Unknown route.
void client.api.GET("/unimplemented");
// @ts-expect-error Command headers are required by the source contract.
void client.api.POST("/api/v1/resources", {
  body: {
    namespaceId: "id",
    kind: "agent",
    canonicalIdentifier: "urn:air:x:y:z",
  },
});
// @ts-expect-error Unknown resource kind.
const kind: components["schemas"]["ResourceKind"] = "unknown";
void kind;
const native: components["schemas"]["RepresentationInput"] = {
  role: "primary",
  mediaType: "application/json",
  data: {
    "@context": ["https://example.com", { "@version": 1.1 }],
    custom: [null, false, ""],
  },
};
void native;
// Generated conditionals are type approximations. The server owns full validation;
// independent valid/invalid JSON Schema assertions remain in Go/Python harnesses.

// @ts-expect-error Transport overrides cannot bypass the selected origin.
void client.api.GET("/.well-known/ard.json", { fetch: fetch });
void client.api.GET("/.well-known/ard.json", {
  // @ts-expect-error The base URL is fixed at construction.
  baseUrl: "https://outside.example.com",
});
void client.api.GET("/.well-known/ard.json", {
  timeoutMs: 1000,
  responseBytes: 1024,
});

const trust: components["schemas"]["TrustAssessmentRequest"] = {
  sourceRevision: 1,
  identifier: "urn:air:example.com:ns:agent",
  versionPresent: false,
  mediaType: "application/json",
  delivery: "data",
  exactEntryDigest: "sha256:" + "a".repeat(64),
  reason: "Check",
  text: "entry",
};
const versionedTrust: components["schemas"]["TrustAssessmentRequest"] = {
  ...trust,
  versionPresent: true,
  version: "1.0.0",
};
const reference: components["schemas"]["RepresentationInput"] = {
  role: "primary",
  mediaType: "application/json",
  url: "https://example.com/card.json",
};
const plugin: components["schemas"]["RepresentationInput"] = {
  role: "primary",
  mediaType: "application/agent-plugins+zip",
  url: "https://example.com/plugin.zip",
  expectedDigest: "sha256:" + "a".repeat(64),
};
void versionedTrust;
void reference;
void plugin;
