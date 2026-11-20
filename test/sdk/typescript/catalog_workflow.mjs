import assert from "node:assert/strict";
import { pathToFileURL } from "node:url";
import { dirname, resolve } from "node:path";
import { readFile } from "node:fs/promises";
import { qualifyCatalogBrowser } from "./browser.functional.mjs";
const { createCatalogClient } = await import(
  pathToFileURL(process.env.CATALOG_SDK_CONSUMER)
);
const origin = process.argv[2];
const options = { origin, allowLoopbackHTTP: true };
const admin = createCatalogClient({ ...options, token: "admin" });
const adoption = JSON.parse(process.env.CATALOG_SDK_VERSION_ADOPTION);
const adopter = createCatalogClient({
  origin: adoption.origin,
  allowLoopbackHTTP: true,
  token: "admin",
});
const destination = await adopter.api.GET(
  "/api/v1/observations/{observationId}/adoption-target",
  { params: { path: { observationId: adoption.body.observationId } } },
);
assert.equal(destination.response.status, 200);
assert.equal(destination.data.resourceId, adoption.resourceId);
assert.equal(
  destination.data.priorVersionRevisionId,
  adoption.priorVersionRevisionId,
);
assert.equal(destination.data.version, "opaque-first");
assert.equal(destination.data.upstreamVersion, "opaque-next");
const appendVersion = (key) =>
  adopter.api.POST("/api/v1/resources/{resourceId}/versions:adopt", {
    params: {
      path: { resourceId: adoption.resourceId },
      header: { "Idempotency-Key": key },
    },
    body: adoption.body,
  });
const adopted = await appendVersion("ts-enabled-adoption");
assert.equal(adopted.response.status, 201);
assert.equal(adopted.data.resourceId, adoption.resourceId);
assert.equal(adopted.data.version.version, "opaque-next");
assert.equal(adopted.data.evidence.state, "complete");
const proof = adopted.data.versionRevision.adoption;
assert.equal(proof.sourceId, adoption.sourceId);
for (const field of [
  "observationId",
  "snapshotId",
  "retrievalDigest",
  "entryLocation",
]) {
  assert.equal(proof[field], adoption.body[field]);
}
assert.equal(
  adopted.data.versionRevision.representations[0].validationState,
  "valid",
);
const adoptedReplay = await appendVersion("ts-enabled-adoption");
assert.equal(adoptedReplay.response.status, 201);
assert.equal(
  adoptedReplay.response.headers.get("Idempotency-Replayed"),
  "true",
);
assert.deepEqual(adoptedReplay.data, adopted.data);
const duplicateVersion = await appendVersion("ts-duplicate-adoption");
assert.equal(duplicateVersion.response.status, 409);
assert.equal(duplicateVersion.error.code, "CONFLICT");
const selectedId = "00000000-0000-7000-8000-000000000001";
const disabledTarget = await admin.api.GET(
  "/api/v1/observations/{observationId}/adoption-target",
  { params: { path: { observationId: selectedId } } },
);
assert.equal(disabledTarget.response.status, 403);
assert.equal(disabledTarget.error.code, "FORBIDDEN");
const disabledAppend = await admin.api.POST(
  "/api/v1/resources/{resourceId}/versions:adopt",
  {
    params: {
      path: { resourceId: selectedId },
      header: { "Idempotency-Key": "ts-disabled-adoption" },
    },
    body: {
      observationId: selectedId,
      snapshotId: selectedId,
      priorVersionRevisionId: selectedId,
      retrievalDigest: "sha256:" + "a".repeat(64),
      entryLocation: "/entries/0",
    },
  },
);
assert.equal(disabledAppend.response.status, 403);
assert.equal(disabledAppend.error.code, "FORBIDDEN");
const namespaces = [];
for (let i = 0; i < 2; i++) {
  const reply = await admin.api.POST("/api/v1/namespaces", {
    params: { header: { "Idempotency-Key": `ts-ns-${i}` } },
    body: { slug: `ts${i}`, displayName: `TypeScript ${i}` },
  });
  assert.equal(reply.response.status, 201);
  assert.ok(reply.response.headers.get("ETag"));
  assert.ok(reply.response.headers.get("X-Request-ID"));
  namespaces.push(reply);
}
const namespace = namespaces[0];
const id = namespace.data.id;
const update = {
  params: {
    path: { namespaceId: id },
    header: {
      "Idempotency-Key": "ts-update",
      "If-Match": namespace.response.headers.get("ETag"),
    },
  },
  body: { displayName: "Updated", reason: "SDK qualification" },
};
const first = await admin.api.PATCH("/api/v1/namespaces/{namespaceId}", update);
const replay = await admin.api.PATCH(
  "/api/v1/namespaces/{namespaceId}",
  update,
);
assert.equal(first.response.status, 200);
assert.equal(replay.response.status, 200);
assert.deepEqual(
  admin.rawBytes(first.response),
  admin.rawBytes(replay.response),
);
assert.equal(replay.response.headers.get("Idempotency-Replayed"), "true");
assert.equal(
  first.response.headers.get("ETag"),
  replay.response.headers.get("ETag"),
);
const stale = await admin.api.PATCH("/api/v1/namespaces/{namespaceId}", {
  ...update,
  params: {
    ...update.params,
    header: { ...update.params.header, "Idempotency-Key": "ts-stale" },
  },
});
assert.equal(stale.response.status, 412);
assert.equal(stale.error.code, "PRECONDITION_FAILED");
const changed = await admin.api.PATCH("/api/v1/namespaces/{namespaceId}", {
  ...update,
  body: { ...update.body, displayName: "Different" },
});
assert.equal(changed.response.status, 409);
assert.equal(changed.error.code, "IDEMPOTENCY_KEY_REUSED");
const afterConflicts = await admin.api.GET("/api/v1/namespaces/{namespaceId}", {
  params: { path: { namespaceId: id } },
});
assert.equal(afterConflicts.response.status, 200);
assert.equal(afterConflicts.data.displayName, "Updated");
assert.equal(
  afterConflicts.response.headers.get("ETag"),
  first.response.headers.get("ETag"),
);
const page = await admin.api.GET("/api/v1/namespaces", {
  params: { query: { limit: 1 } },
});
assert.equal(page.data.items.length, 1);
assert.ok(page.data.nextCursor);
const next = await admin.api.GET("/api/v1/namespaces", {
  params: { query: { limit: 1, cursor: page.data.nextCursor } },
});
assert.notEqual(page.data.items[0].id, next.data.items[0].id);
assert.ok(!next.data.nextCursor);
const resources = [];
for (const kind of ["agent", "mcp_server", "skill", "plugin"]) {
  const r = await admin.api.POST("/api/v1/resources", {
    params: { header: { "Idempotency-Key": `ts-${kind}` } },
    body: {
      namespaceId: id,
      kind,
      canonicalIdentifier: `urn:air:catalog.example.com:typescript:${kind}`,
    },
  });
  assert.equal(r.response.status, 201);
  resources.push(r.data);
}
// Supply the original envelope text so JSON.stringify cannot round the native integer.
const body =
  '{"version":"1.0.0","representations":[{"role":"primary","mediaType":"application/a2a-agent-card+json","data":{"name":"TypeScript Agent","description":"Exact evidence","version":"1.0.0","supportedInterfaces":[{"url":"https://agents.example.com/ts","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{"extensions":[{"uri":"https://example.com/ts","params":{"large":9007199254740993,"nested":[null,false,""]}}]},"defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],"skills":[{"id":"ts","name":"TS","description":"Evidence","tags":["sdk"]}]}}]}';
const version = await admin.raw(
  `/api/v1/resources/${resources[0].id}/versions`,
  {
    method: "POST",
    body,
    headers: {
      "Content-Type": "application/json",
      "Idempotency-Key": "ts-version",
    },
  },
);
assert.equal(version.status, 201);
const revision = JSON.parse(version.text()).revision.id;
const raw = await admin.raw("/api/v1/version-revisions/" + revision);
assert.equal(raw.status, 200);
assert.equal(JSON.parse(raw.text()).namespaceId, id);
assert.ok(raw.text().includes("9007199254740993"));
assert.ok(raw.text().includes('[null,false,""]'));
const reader = createCatalogClient({ ...options, token: "reader" });
const deniedReplay = await reader.api.PATCH(
  "/api/v1/namespaces/{namespaceId}",
  update,
);
assert.equal(deniedReplay.response.status, 403);
assert.equal(deniedReplay.error.code, "FORBIDDEN");
assert.equal(deniedReplay.response.headers.get("Idempotency-Replayed"), null);
const afterDeniedReplay = await admin.api.GET(
  "/api/v1/namespaces/{namespaceId}",
  { params: { path: { namespaceId: id } } },
);
assert.equal(afterDeniedReplay.response.status, 200);
assert.deepEqual(
  admin.rawBytes(afterDeniedReplay.response),
  admin.rawBytes(first.response),
);
assert.equal(
  afterDeniedReplay.response.headers.get("ETag"),
  first.response.headers.get("ETag"),
);
const emptySearch = await reader.api.POST("/api/v1/catalog/search", {
  body: { origins: ["local"], limit: 1 },
});
assert.equal(emptySearch.response.status, 200);
assert.deepEqual(emptySearch.data.items, []);

// Exercise nonempty generated response types through the installed package.
// Drafts remain invisible until a separate reviewer approves each listing.
const publisher = await admin.api.POST("/api/v1/publishers", {
  params: { header: { "Idempotency-Key": "ts-publisher" } },
  body: { displayName: "SDK Publisher", domains: ["catalog.example.com"] },
});
assert.equal(publisher.response.status, 201);
const approvedDomain = await admin.api.POST(
  "/api/v1/publishers/{publisherId}/publication-domains/{domain}:approve",
  {
    params: {
      path: { publisherId: publisher.data.id, domain: "catalog.example.com" },
      header: {
        "Idempotency-Key": "ts-domain",
        "If-Match": publisher.response.headers.get("ETag"),
      },
    },
    body: { reason: "Local SDK qualification" },
  },
);
assert.equal(approvedDomain.response.status, 200);
const reviewer = createCatalogClient({ ...options, token: "reviewer" });
const releases = new Map();
for (let index = 0; index < 2; index++) {
  let resource = resources[0],
    versionRevision = revision;
  if (index === 1) {
    const second = await admin.api.POST("/api/v1/resources", {
      params: { header: { "Idempotency-Key": "ts-agent-second" } },
      body: {
        namespaceId: id,
        kind: "agent",
        canonicalIdentifier: "urn:air:catalog.example.com:typescript:second",
      },
    });
    assert.equal(second.response.status, 201);
    resource = second.data;
    const secondVersion = await admin.raw(
      `/api/v1/resources/${resource.id}/versions`,
      {
        method: "POST",
        body,
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": "ts-version-second",
        },
      },
    );
    assert.equal(secondVersion.status, 201);
    versionRevision = JSON.parse(secondVersion.text()).revision.id;
  }
  const listing = await admin.api.POST("/api/v1/catalog-listings", {
    params: { header: { "Idempotency-Key": `ts-listing-${index}` } },
    body: { namespaceId: id, resourceId: resource.id },
  });
  assert.equal(listing.response.status, 201);
  const displayName = `TypeScript discovery ${index}`;
  const extensions = {
    "sdk-detail": `listing-${index}`,
    additional_properties: { must: "stay nested" },
    model_extra: null,
    values: [null, false, ""],
  };
  const listingRevision = await admin.api.POST(
    "/api/v1/catalog-listings/{listingId}/revisions",
    {
      params: {
        path: { listingId: listing.data.id },
        header: {
          "Idempotency-Key": `ts-listing-revision-${index}`,
          "If-Match": listing.response.headers.get("ETag"),
        },
      },
      body: {
        versionRevisionId: versionRevision,
        displayName,
        description: "Installed SDK discovery fixture",
        publisherId: publisher.data.id,
        visibility: "tenant",
        tags: ["typescript"],
        extensions,
      },
    },
  );
  assert.equal(listingRevision.response.status, 201);
  const submission = await admin.api.POST(
    "/api/v1/catalog-listings/{listingId}/submissions",
    {
      params: {
        path: { listingId: listing.data.id },
        header: { "Idempotency-Key": `ts-submit-${index}` },
      },
      body: {},
    },
  );
  assert.equal(submission.response.status, 201);
  const decision = await reviewer.api.POST(
    "/api/v1/review-submissions/{submissionId}:approve",
    {
      params: {
        path: { submissionId: submission.data.id },
        header: {
          "Idempotency-Key": `ts-approve-${index}`,
          "If-Match": submission.response.headers.get("ETag"),
        },
      },
      body: { reason: "Independent SDK fixture review" },
    },
  );
  assert.equal(decision.response.status, 200);
  const currentListing = await admin.api.GET(
    "/api/v1/catalog-listings/{listingId}",
    { params: { path: { listingId: listing.data.id } } },
  );
  assert.equal(currentListing.response.status, 200);
  const published = await admin.api.POST(
    "/api/v1/catalog-listings/{listingId}:publish",
    {
      params: {
        path: { listingId: listing.data.id },
        header: {
          "Idempotency-Key": `ts-publish-${index}`,
          "If-Match": currentListing.response.headers.get("ETag"),
        },
      },
      body: { submissionId: submission.data.id },
    },
  );
  assert.equal(published.response.status, 201);
  if (index === 0) {
    const created = await admin.api.POST("/api/v1/collections", {
      params: { header: { "Idempotency-Key": "ts-collection" } },
      body: {
        namespaceId: id,
        name: "TypeScript SDK collection",
        description: "Original note",
      },
    });
    assert.equal(created.response.status, 201);
    const member = {
      params: {
        path: { collectionId: created.data.id },
        header: {
          "Idempotency-Key": "ts-member",
          "If-Match": created.response.headers.get("ETag"),
        },
      },
      body: { listingId: listing.data.id },
    };
    const added = await admin.api.POST(
      "/api/v1/collections/{collectionId}/members",
      member,
    );
    assert.equal(added.response.status, 201);
    assert.equal(added.data.revision, 2);
    const replayed = await admin.api.POST(
      "/api/v1/collections/{collectionId}/members",
      member,
    );
    assert.equal(replayed.response.headers.get("Idempotency-Replayed"), "true");
    assert.deepEqual(replayed.data, added.data);
    const page = await reader.api.GET(
      "/api/v1/collections/{collectionId}/members",
      {
        params: {
          path: { collectionId: created.data.id },
          query: { limit: 1 },
        },
      },
    );
    assert.equal(page.response.status, 200);
    assert.equal(page.data.items.length, 1);
    assert.equal(page.data.items[0].listing.id, listing.data.id);
    assert.equal(page.data.items[0].release.id, published.data.release.id);
    const selected = await reader.api.GET(
      "/api/v1/collections/{collectionId}/members",
      {
        params: {
          path: { collectionId: created.data.id },
          query: { text: "SDK", kinds: ["agent", "skill"] },
        },
      },
    );
    assert.equal(selected.response.status, 200);
    assert.deepEqual(
      selected.data.items.map((item) => item.listing.id),
      [listing.data.id],
    );
    const excluded = await reader.api.GET(
      "/api/v1/collections/{collectionId}/members",
      {
        params: {
          path: { collectionId: created.data.id },
          query: {
            text: "collection-filter-no-match",
            kinds: ["agent", "skill"],
          },
        },
      },
    );
    assert.equal(excluded.response.status, 200);
    assert.deepEqual(excluded.data.items, []);
    const wrongKind = await reader.api.GET(
      "/api/v1/collections/{collectionId}/members",
      {
        params: {
          path: { collectionId: created.data.id },
          query: { kinds: ["mcp_server", "plugin"] },
        },
      },
    );
    assert.equal(wrongKind.response.status, 200);
    assert.deepEqual(wrongKind.data.items, []);

    assert.equal(page.data.nextCursor, undefined);
    assert.equal(page.response.headers.get("Cache-Control"), "no-store");
    assert.equal(page.response.headers.get("ETag"), null);
    const path = { collectionId: created.data.id };
    const directory = await reader.api.GET("/api/v1/collections", {
      params: { query: { namespaceId: id, limit: 100 } },
    });
    assert.equal(directory.response.status, 200);
    assert.deepEqual(
      directory.data.items.map((item) => item.id),
      [created.data.id],
    );
    assert.equal(directory.data.nextCursor, undefined);
    const metadata = await reader.api.GET(
      "/api/v1/collections/{collectionId}",
      { params: { path } },
    );
    assert.equal(metadata.response.status, 200);
    assert.equal(metadata.data.name, "TypeScript SDK collection");
    assert.equal(metadata.data.description, "Original note");
    assert.equal(metadata.data.revision, 2);
    const updated = await admin.api.PATCH(
      "/api/v1/collections/{collectionId}",
      {
        params: {
          path,
          header: {
            "Idempotency-Key": "ts-update-collection",
            "If-Match": metadata.response.headers.get("ETag"),
          },
        },
        body: { name: "Updated TypeScript collection", description: "" },
      },
    );
    assert.equal(updated.response.status, 200);
    assert.equal(updated.data.revision, 3);
    const stale = await admin.api.PATCH("/api/v1/collections/{collectionId}", {
      params: {
        path,
        header: {
          "Idempotency-Key": "ts-stale-collection",
          "If-Match": metadata.response.headers.get("ETag"),
        },
      },
      body: { name: "Stale" },
    });
    assert.equal(stale.response.status, 412);
    assert.equal(stale.error.code, "PRECONDITION_FAILED");
    const denied = await reader.api.DELETE(
      "/api/v1/collections/{collectionId}",
      {
        params: {
          path,
          header: {
            "Idempotency-Key": "ts-reader-delete-collection",
            "If-Match": updated.response.headers.get("ETag"),
          },
        },
      },
    );
    assert.equal(denied.response.status, 403);
    assert.equal(denied.error.code, "FORBIDDEN");
    const readback = await reader.api.GET(
      "/api/v1/collections/{collectionId}",
      { params: { path } },
    );
    assert.equal(readback.response.status, 200);
    assert.equal(readback.data.name, "Updated TypeScript collection");
    assert.equal(readback.data.description, "");
    assert.equal(readback.data.revision, 3);
    const removed = await admin.api.DELETE(
      "/api/v1/collections/{collectionId}/members/{listingId}",
      {
        params: {
          path: { ...path, listingId: listing.data.id },
          header: {
            "Idempotency-Key": "ts-remove-member",
            "If-Match": updated.response.headers.get("ETag"),
          },
        },
      },
    );
    assert.equal(removed.response.status, 200);
    assert.equal(removed.data.revision, 4);
    const empty = await reader.api.GET(
      "/api/v1/collections/{collectionId}/members",
      { params: { path } },
    );
    assert.equal(empty.response.status, 200);
    assert.deepEqual(empty.data.items, []);
    assert.equal(empty.data.nextCursor, undefined);
    const deleted = await admin.api.DELETE(
      "/api/v1/collections/{collectionId}",
      {
        params: {
          path: { collectionId: created.data.id },
          header: {
            "Idempotency-Key": "ts-delete-collection",
            "If-Match": removed.response.headers.get("ETag"),
          },
        },
      },
    );
    assert.equal(deleted.response.status, 200);
    assert.equal(deleted.data.revision, 5);
    const absent = await reader.api.GET("/api/v1/collections/{collectionId}", {
      params: { path },
    });
    assert.equal(absent.response.status, 404);
    assert.equal(absent.error.code, "NOT_FOUND");
    const preserved = await reader.api.GET(
      "/api/v1/catalog/resources/{catalogRecordId}",
      {
        params: { path: { catalogRecordId: published.data.release.id } },
      },
    );
    assert.equal(preserved.response.status, 200);
    assert.equal(preserved.data.release.id, published.data.release.id);
  }
  releases.set(published.data.release.id, {
    resource,
    displayName,
    extensions,
  });
}
const other = createCatalogClient({ ...options, token: "other" });
for (const [nativeCapabilities, count] of [
  [{ agentDefaultInputModes: ["application/json"] }, releases.size],
  [
    {
      agentSkillIds: ["not-declared-sdk-sentinel", "ts"],
      agentSkillTags: ["sdk"],
      agentDefaultOutputModes: ["application/json"],
    },
    releases.size,
  ],
  [{ agentStreaming: false }, 0],
  [{ agentSkillIds: ["not-declared-sdk-sentinel"] }, 0],
]) {
  const selected = await reader.api.POST("/api/v1/catalog/search", {
    body: { nativeCapabilities, origins: ["local"] },
  });
  assert.equal(selected.response.status, 200);
  assert.equal(selected.data.durableTotal, count);
  assert.equal(selected.data.items.length, count);
  assert.deepEqual(
    new Set(selected.data.items.map((item) => item.local.release.id)),
    count ? new Set(releases.keys()) : new Set(),
  );
  const explored = await reader.api.POST("/api/v1/catalog/explore", {
    body: { query: { nativeCapabilities }, facets: [{ field: "kind" }] },
  });
  assert.equal(explored.response.status, 200);
  assert.equal(explored.data.matched, count);
}
const unmatchedProtocol = await reader.api.POST("/api/v1/catalog/search", {
  body: { agentProtocol: "HTTP+JSON", origins: ["local"] },
});
assert.equal(unmatchedProtocol.response.status, 200);
assert.equal(unmatchedProtocol.data.durableTotal, 0);
assert.deepEqual(unmatchedProtocol.data.items, []);

for (const selection of ["search", "browse"]) {
  const unseen = new Set(releases.keys());
  let cursor;
  for (let pageIndex = 0; pageIndex < 2; pageIndex++) {
    const reply =
      selection === "search"
        ? await reader.api.POST("/api/v1/catalog/search", {
            body: {
              agentProtocol: "JSONRPC",
              origins: ["local"],
              kinds: ["agent"],
              limit: 1,
              ...(cursor ? { cursor } : {}),
            },
          })
        : await reader.api.GET("/api/v1/catalog/resources", {
            params: {
              query: {
                agentProtocol: "JSONRPC",
                namespaceId: id,
                kind: "agent",
                limit: 1,
                ...(cursor ? { cursor } : {}),
              },
            },
          });
    assert.equal(reply.response.status, 200);
    assert.equal(reply.data.items.length, 1);
    if (selection === "search") {
      assert.equal(reply.data.partial, false);
      assert.equal(reply.data.durableTotal, 2);
      assert.equal(reply.data.items[0].type, "local");
    }
    const record =
      selection === "search" ? reply.data.items[0].local : reply.data.items[0];
    assert.ok(
      unseen.delete(record.release.id),
      "duplicate or unexpected release",
    );
    const expected = releases.get(record.release.id);
    assert.equal(
      record.resource.canonicalIdentifier,
      expected.resource.canonicalIdentifier,
    );
    cursor = reply.data.nextCursor;
    assert.equal(Boolean(cursor), pageIndex === 0);
    if (selection === "browse") continue;
    const detail = await reader.api.GET(
      "/api/v1/catalog/resources/{catalogRecordId}",
      { params: { path: { catalogRecordId: record.release.id } } },
    );
    assert.equal(detail.response.status, 200);
    assert.equal(detail.data.listingRevision.displayName, expected.displayName);
    assert.deepEqual(
      detail.data.listingRevision.extensions,
      expected.extensions,
    );
    assert.ok(
      new TextDecoder()
        .decode(reader.rawBytes(detail.response))
        .includes("9007199254740993"),
    );
    const hidden = await other.api.GET(
      "/api/v1/catalog/resources/{catalogRecordId}",
      { params: { path: { catalogRecordId: record.release.id } } },
    );
    assert.equal(hidden.response.status, 404);
    assert.equal(hidden.error.code, "NOT_FOUND");
  }
  assert.equal(unseen.size, 0);
}
const denial = await reader.api.POST("/api/v1/namespaces", {
  params: { header: { "Idempotency-Key": "ts-denied" } },
  body: { slug: "denied", displayName: "Denied" },
});
assert.equal(denial.response.status, 403);
assert.equal(denial.error.code, "FORBIDDEN");
const namespacesAfterDenial = await admin.api.GET("/api/v1/namespaces");
assert.equal(namespacesAfterDenial.response.status, 200);
assert.deepEqual(
  new Set(namespacesAfterDenial.data.items.map((item) => item.id)),
  new Set(namespaces.map((item) => item.data.id)),
);
assert.equal(
  (
    await other.api.GET("/api/v1/namespaces/{namespaceId}", {
      params: { path: { namespaceId: id } },
    })
  ).response.status,
  404,
);
const retried = await admin.api.POST("/api/v1/jobs/{jobId}:retry", {
  params: {
    path: { jobId: "00000000-0000-7000-8000-000000000090" },
    header: { "Idempotency-Key": "ts-job-retry" },
  },
  body: { reason: "SDK qualification" },
});
assert.equal(retried.response.status, 202);
assert.equal(
  retried.response.headers.get("Location"),
  "/api/v1/jobs/" + retried.data.id,
);
assert.equal(
  (
    await admin.api.GET("/api/v1/jobs/{jobId}", {
      params: { path: { jobId: retried.data.id } },
    })
  ).data.state,
  "queued",
);
assert.equal(
  (
    await other.api.GET("/api/v1/jobs/{jobId}", {
      params: { path: { jobId: retried.data.id } },
    })
  ).response.status,
  404,
);
assert.equal(
  (await createCatalogClient(options).api.GET("/api/v1/namespaces")).response
    .status,
  401,
);
await qualifyCatalogBrowser(
  resolve(dirname(process.env.CATALOG_SDK_CONSUMER), "../../../.."),
  origin,
  [...releases.keys()],
);
const afterBrowser = await admin.api.GET("/api/v1/namespaces");
assert.equal(afterBrowser.response.status, 200);
assert.deepEqual(
  afterBrowser.data.items,
  namespacesAfterDenial.data.items,
  "the refused browser write must not change namespace state",
);
console.log(
  "Installed JavaScript Catalog workflow passed: four kinds, governed releases, Search/Browse continuation, exact evidence, replay/conflict, current-permission replay refusal, denied-write readback, authorization, private Jobs and real Catalog browser calls",
);

// Source expectations come from the Go fixture before validation, not readback.
const corpus = JSON.parse(
  await readFile(process.env.CATALOG_SDK_CORPUS, "utf8"),
);
assert.equal(corpus.cases.length, 6);
const corpusReader = createCatalogClient({
  origin: corpus.origin,
  allowLoopbackHTTP: true,
  token: "reader",
});
const corpusOther = createCatalogClient({
  origin: corpus.origin,
  allowLoopbackHTTP: true,
  token: "other",
});
for (const expected of corpus.cases) {
  const reply = await corpusReader.api.GET(
    "/api/v1/catalog/resources/{catalogRecordId}",
    { params: { path: { catalogRecordId: expected.releaseId } } },
  );
  assert.equal(reply.response.status, 200);
  const record = reply.data;
  assert.equal(record.resource.kind, expected.kind);
  assert.equal(record.resource.canonicalIdentifier, expected.identifier);
  assert.equal(record.release.id, expected.releaseId);
  assert.equal(record.versionRevision.representations.length, 1);
  const rep = record.versionRevision.representations[0];
  for (const [key, value] of Object.entries(expected.representation))
    assert.deepEqual(rep[key], value, `${expected.name}: ${key}`);
  if (expected.data) assert.deepEqual(rep.data, expected.data);
  if (expected.description)
    assert.equal(rep.normalizedFacts.description, expected.description);
  if (expected.text) {
    const content = await corpusReader.raw(
      `/api/v1/artifacts/${rep.artifactId}/content`,
    );
    assert.equal(content.status, 200);
    assert.deepEqual(Buffer.from(content.bytes), Buffer.from(expected.text));
  }
  if (expected.fileCount)
    assert.equal(
      rep[expected.kind === "plugin" ? "plugin" : "package"].files.length,
      expected.fileCount,
    );
  if (expected.documents) {
    assert.equal(
      rep.plugin.documents.length,
      Object.keys(expected.documents).length,
    );
    assert.deepEqual(
      Object.fromEntries(rep.plugin.documents.map((d) => [d.path, d.content])),
      expected.documents,
    );
  }
  const denied = await corpusOther.api.GET(
    "/api/v1/catalog/resources/{catalogRecordId}",
    { params: { path: { catalogRecordId: expected.releaseId } } },
  );
  assert.equal(denied.response.status, 404);
}
console.log(
  "Installed JavaScript SDK: six reviewed upstream cases, exact native content and tenant denial passed",
);

// The installed binding reads actual current evidence without invoking validation.
const maintenanceResource = await admin.api.POST("/api/v1/resources", {
  params: { header: { "Idempotency-Key": "ts-maintenance-resource" } },
  body: {
    namespaceId: id,
    kind: "agent",
    canonicalIdentifier: "urn:air:catalog.example.com:typescript:maintenance",
  },
});
assert.equal(maintenanceResource.response.status, 201);
const maintenanceVersion = await admin.api.POST(
  "/api/v1/resources/{resourceId}/versions",
  {
    params: {
      path: { resourceId: maintenanceResource.data.id },
      header: { "Idempotency-Key": "ts-maintenance-version" },
    },
    body: {
      version: "79.0.0",
      representations: [
        {
          role: "primary",
          mediaType: "application/a2a-agent-card+json",
          data: { name: "Incomplete maintenance fixture" },
        },
      ],
    },
  },
);
assert.equal(maintenanceVersion.response.status, 201);
assert.equal(maintenanceVersion.data.evidence.state, "blocked");
const maintenance = await admin.api.GET("/api/v1/validation-maintenance", {
  params: { query: { state: "blocked", horizonHours: 24, limit: 100 } },
});
assert.equal(maintenance.response.status, 200);
assert.equal(maintenance.response.headers.get("Cache-Control"), "no-store");
const maintenanceRow = maintenance.data.items.find(
  (row) => row.revisionId === maintenanceVersion.data.revision.id,
);
assert.equal(maintenanceRow.evidenceId, maintenanceVersion.data.evidence.id);
assert.deepEqual(maintenanceRow.conditions, ["blocked"]);
assert.equal(maintenanceRow.expiresAt, undefined);
assert.equal(maintenanceRow.applicable, false);
const maintenanceDenied = await reader.api.GET(
  "/api/v1/validation-maintenance",
  { params: { query: { state: "blocked", horizonHours: 24 } } },
);
assert.equal(maintenanceDenied.response.status, 200);
assert.deepEqual(maintenanceDenied.data.items, []);
const maintenanceBad = await admin.api.GET("/api/v1/validation-maintenance", {
  params: { query: { horizonHours: 169 } },
});
assert.equal(maintenanceBad.response.status, 400);
assert.equal(maintenanceBad.error.code, "INVALID_ARGUMENT");
