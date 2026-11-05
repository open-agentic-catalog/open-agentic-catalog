// Bind a consumed record to its requested subject before exposing reader actions.
import type { RecordDetail, SearchResult } from "../api/client";
import { CatalogRecord } from "./responses";
import { object } from "../api/values";

export function recordForRequest(
  value: unknown,
  releaseId: string,
  tenantId: string | undefined,
  selected?: SearchResult,
): boolean {
  if (!CatalogRecord(value)) return false;
  const record = value as RecordDetail;
  const {
    resource,
    version,
    versionRevision,
    listing,
    listingRevision,
    release,
    evidence,
  } = record;
  if (
    !tenantId ||
    [
      resource,
      version,
      versionRevision,
      listing,
      listingRevision,
      release,
      evidence,
    ].some((part) => part.tenantId !== tenantId)
  )
    return false;
  if (
    release.id.toLowerCase() !== releaseId.toLowerCase() ||
    release.listingId !== listing.id ||
    release.versionRevisionId !== versionRevision.id ||
    release.listingRevisionId !== listingRevision.id ||
    listingRevision.listingId !== listing.id ||
    listingRevision.versionRevisionId !== versionRevision.id ||
    versionRevision.resourceVersionId !== version.id ||
    versionRevision.evidenceSetId !== evidence.id ||
    version.resourceId !== resource.id ||
    listing.resourceId !== resource.id ||
    listing.namespaceId !== resource.namespaceId
  )
    return false;
  if (!selected) return true;
  const local = selected.local;
  return (
    !!local &&
    [
      local.resource,
      local.listing,
      local.version,
      local.versionRevision,
      local.listingRevision,
      local.release,
    ].every(object) &&
    selected.identifier === resource.canonicalIdentifier &&
    selected.kind === resource.kind &&
    selected.versionPresent &&
    selected.version === version.version &&
    local.resource.id === resource.id &&
    local.resource.tenantId === tenantId &&
    local.resource.namespaceId === resource.namespaceId &&
    local.resource.kind === resource.kind &&
    local.resource.canonicalIdentifier === resource.canonicalIdentifier &&
    local.listing.id === listing.id &&
    local.listing.namespaceId === listing.namespaceId &&
    local.version.id === version.id &&
    local.version.version === version.version &&
    local.versionRevision.id === versionRevision.id &&
    local.listingRevision.id === listingRevision.id &&
    local.release.id === releaseId &&
    local.release.versionRevisionId === versionRevision.id &&
    local.release.listingRevisionId === listingRevision.id
  );
}
