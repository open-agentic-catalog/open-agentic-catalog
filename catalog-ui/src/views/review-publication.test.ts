import { expect, it } from "vitest";
import { publicationReceiptMatches } from "./review-publication";

const expected = {
  tenantId: "tenant-a",
  listingId: "listing-a",
  namespaceId: "namespace-a",
  submissionId: "submission-a",
  listingRevisionId: "listing-revision-a",
  versionRevisionId: "native-revision-a",
  disposition: "retired" as const,
};
const release = {
  id: "new-release",
  tenantId: "tenant-a",
  listingId: "listing-a",
  submissionId: "submission-a",
  listingRevisionId: "listing-revision-a",
  versionRevisionId: "native-revision-a",
  state: "published",
  revision: 1,
  activatedAt: "2026-10-20T10:00:00Z",
  updatedAt: "2026-10-20T10:00:00Z",
};
const receipt = {
  listing: {
    id: "listing-a",
    tenantId: "tenant-a",
    namespaceId: "namespace-a",
    resourceId: "resource-a",
    currentReleaseId: "new-release",
  },
  release,
  priorRelease: { ...release, id: "old-release", state: "retired" },
};

it("accepts an exact replacement and an explicit null prior release without inferring history", () => {
  expect(publicationReceiptMatches(receipt, expected)).toBe(true);
  expect(
    publicationReceiptMatches({ ...receipt, priorRelease: null }, expected),
  ).toBe(true);
});

it("requires the returned prior release to match inspected context captured with the Listing revision", () => {
  const inspected = { ...expected, priorReleaseId: "old-release" };
  expect(publicationReceiptMatches(receipt, inspected)).toBe(true);
  expect(
    publicationReceiptMatches({ ...receipt, priorRelease: null }, inspected),
  ).toBe(false);
  expect(
    publicationReceiptMatches(
      {
        ...receipt,
        priorRelease: { ...receipt.priorRelease, id: "other-old-release" },
      },
      inspected,
    ),
  ).toBe(false);
  const absent = { ...expected, priorReleaseId: null };
  expect(
    publicationReceiptMatches({ ...receipt, priorRelease: null }, absent),
  ).toBe(true);
  expect(publicationReceiptMatches(receipt, absent)).toBe(false);
});

it("requires the returned replacement reference for supersession", () => {
  const input = {
    ...receipt,
    priorRelease: {
      ...receipt.priorRelease,
      state: "superseded",
      replacementReleaseId: "new-release",
    },
  };
  expect(
    publicationReceiptMatches(input, {
      ...expected,
      disposition: "superseded",
    }),
  ).toBe(true);
  expect(
    publicationReceiptMatches(
      {
        ...input,
        priorRelease: { ...input.priorRelease, replacementReleaseId: "other" },
      },
      { ...expected, disposition: "superseded" },
    ),
  ).toBe(false);
});

it.each([
  ["listing.id", "other"],
  ["listing.tenantId", "other"],
  ["listing.namespaceId", "other"],
  ["listing.currentReleaseId", "other"],
  ["listing.resourceId", null],
  ["release.id", "../unsafe"],
  ["release.tenantId", "other"],
  ["release.listingId", "other"],
  ["release.submissionId", "other"],
  ["release.listingRevisionId", "other"],
  ["release.versionRevisionId", "other"],
  ["release.state", "deprecated"],
  ["priorRelease.id", "new-release"],
  ["priorRelease.listingId", "other"],
  ["priorRelease.tenantId", "other"],
  ["priorRelease.state", "revoked"],
  ["priorRelease.tenantId", undefined],
  ["priorRelease.updatedAt", "bad"],
  ["listing.tenantId", undefined],
  ["release.tenantId", undefined],
  ["priorRelease", undefined],
  ["priorRelease", []],
  ["priorRelease", {}],
  ["release", null],
  ["listing", null],
] as [string, unknown][])(
  "rejects a malformed or unrelated receipt at %s",
  (path, value) => {
    const changed: Record<string, unknown> = structuredClone(receipt);
    const [parent, field] = path.split(".");
    if (field) (changed[parent!] as Record<string, unknown>)[field] = value;
    else changed[parent!] = value;
    expect(publicationReceiptMatches(receipt, expected)).toBe(true);
    expect(publicationReceiptMatches(changed, expected)).toBe(false);
  },
);
