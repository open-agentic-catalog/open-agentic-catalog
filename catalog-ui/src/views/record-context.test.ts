import { expect, it } from "vitest";
import { recordForRequest } from "./record-context";
import { CatalogRecord } from "./responses";
import {
  evidence,
  versionRevision,
  listingRevision,
  release,
} from "../../tests/unit/responses";
import { resource, version, listing } from "../../tests/unit/screens";
import type { SearchResult } from "../api/client";

const record = {
  resource,
  version,
  listing,
  versionRevision,
  listingRevision,
  release,
  evidence,
};
const selected = {
  identifier: resource.canonicalIdentifier,
  kind: resource.kind,
  versionPresent: true,
  version: version.version,
  local: {
    resource,
    version,
    listing,
    versionRevision,
    listingRevision,
    release,
  },
} as unknown as SearchResult;

it("accepts the requested complete record and its compact selection", () => {
  expect(recordForRequest(record, release.id, "tenant-a")).toBe(true);
  expect(recordForRequest(record, release.id, "tenant-a", selected)).toBe(true);
  expect(recordForRequest(record, release.id, undefined)).toBe(false);
  expect(recordForRequest({}, release.id, "tenant-a")).toBe(false);
});

it.each([
  "resource",
  "version",
  "listing",
  "versionRevision",
  "listingRevision",
  "release",
  "evidence",
] as const)("requires the current tenant on %s", (part) => {
  for (const tenantId of [undefined, "other-tenant"]) {
    expect(
      recordForRequest(
        { ...record, [part]: { ...record[part], tenantId } },
        release.id,
        "tenant-a",
      ),
    ).toBe(false);
  }
});

it.each([
  ["release", "id"],
  ["release", "listingId"],
  ["release", "listingRevisionId"],
  ["release", "versionRevisionId"],
  ["listingRevision", "listingId"],
  ["listingRevision", "versionRevisionId"],
  ["versionRevision", "resourceVersionId"],
  ["versionRevision", "evidenceSetId"],
  ["version", "resourceId"],
  ["listing", "resourceId"],
  ["listing", "namespaceId"],
] as const)(
  "rejects an inconsistent %s.%s after a valid shape control",
  (part, field) => {
    const changed = {
      ...record,
      [part]: { ...record[part], [field]: "other-id" },
    };
    expect(CatalogRecord(changed)).toBe(true);
    expect(recordForRequest(changed, release.id, "tenant-a")).toBe(false);
  },
);

it.each([
  ["resource", "id"],
  ["resource", "tenantId"],
  ["resource", "namespaceId"],
  ["resource", "kind"],
  ["resource", "canonicalIdentifier"],
  ["listing", "id"],
  ["listing", "namespaceId"],
  ["version", "id"],
  ["version", "version"],
  ["versionRevision", "id"],
  ["listingRevision", "id"],
  ["release", "id"],
  ["release", "listingRevisionId"],
  ["release", "versionRevisionId"],
] as const)(
  "binds supplied compact %s.%s without inventing missing fields",
  (part, field) => {
    const changed = {
      ...selected,
      local: {
        ...selected.local!,
        [part]: { ...selected.local![part], [field]: "other-value" },
      },
    };
    expect(recordForRequest(record, release.id, "tenant-a", changed)).toBe(
      false,
    );
  },
);

it.each(["identifier", "kind", "version", "versionPresent"] as const)(
  "binds selected %s",
  (field) => {
    expect(
      recordForRequest(record, release.id, "tenant-a", {
        ...selected,
        [field]: field === "versionPresent" ? false : "other",
      }),
    ).toBe(false);
  },
);

it("accepts newer current validation evidence without matching historical approval or stale compact evidence", () => {
  const revalidated = {
    ...record,
    evidence: { ...evidence, id: "evidence-2", digest: "sha256:new" },
    versionRevision: { ...versionRevision, evidenceSetId: "evidence-2" },
  };
  expect(recordForRequest(revalidated, release.id, "tenant-a", selected)).toBe(
    true,
  );
});

it("rejects missing compact objects and accepts UUID case in a bookmarked request", () => {
  const { local: _local, ...external } = selected;
  expect(recordForRequest(record, release.id, "tenant-a", external)).toBe(
    false,
  );
  const id = "00000000-0000-7000-8000-000000000abc";
  expect(
    recordForRequest(
      { ...record, release: { ...release, id } },
      id.toUpperCase(),
      "tenant-a",
    ),
  ).toBe(true);
});
