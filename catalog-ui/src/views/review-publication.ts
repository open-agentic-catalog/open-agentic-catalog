import * as checks from "./responses";

/** Match the receipt to the captured command, never to a later mutable pointer. */
export function publicationReceiptMatches(
  value: unknown,
  expected: {
    tenantId: string;
    listingId: string;
    namespaceId: string;
    submissionId: string;
    listingRevisionId: string;
    versionRevisionId: string;
    disposition: "retired" | "superseded";
    // Omitted when metadata was not read; null means an inspected absent pointer.
    priorReleaseId?: string | null;
  },
) {
  if (!checks.ListingPublished(value)) return false;
  return (
    value.listing.id === expected.listingId &&
    value.listing.tenantId === expected.tenantId &&
    value.listing.namespaceId === expected.namespaceId &&
    value.listing.currentReleaseId === value.release.id &&
    value.release.listingId === expected.listingId &&
    value.release.tenantId === expected.tenantId &&
    value.release.submissionId === expected.submissionId &&
    value.release.listingRevisionId === expected.listingRevisionId &&
    value.release.versionRevisionId === expected.versionRevisionId &&
    value.release.state === "published" &&
    (expected.priorReleaseId === undefined ||
      (value.priorRelease === null ? null : value.priorRelease.id) ===
        expected.priorReleaseId) &&
    (value.priorRelease === null ||
      (value.priorRelease.id !== value.release.id &&
        value.priorRelease.listingId === expected.listingId &&
        value.priorRelease.tenantId === expected.tenantId &&
        value.priorRelease.state === expected.disposition &&
        (expected.disposition !== "superseded" ||
          value.priorRelease.replacementReleaseId === value.release.id)))
  );
}
