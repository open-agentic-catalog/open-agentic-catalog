// Separate acknowledged writes keep their captured keys after partial success.
import type { Client } from "../api/client";
import { APIError, result } from "../api/client";
import type { FetchResponse } from "openapi-fetch";
import type { components, operations } from "../api/schema";
import { object } from "../api/values";
import * as checks from "./responses";
import { listingSuggestion } from "./ListingPrefill";
import type { ListingPlan } from "./ListingPreparation";
export type VersionBundle = {
  version: components["schemas"]["ResourceVersion"];
  revision: NonNullable<
    FetchResponse<
      operations["getVersionRevision"],
      object,
      "application/json"
    >["data"]
  >["revision"];
  evidence: components["schemas"]["EvidenceSet"];
};
export type ListingCheckpoint = {
  listing?: components["schemas"]["CatalogListing"] | undefined;
  etag?: string | null | undefined;
  revision?: components["schemas"]["ListingRevision"] | undefined;
};
export const commandETag = (response: Response) => {
  const etag = response.headers.get("ETag");
  if (!etag || !/^"[^"\r\n]+"$/.test(etag))
    throw new Error(
      "The save outcome is uncertain: the reply has no usable ETag. Retry this captured command with the same key.",
    );
  return etag;
};
export async function savePreparedListing(
  client: Client,
  resource: components["schemas"]["Resource"],
  bundle: VersionBundle,
  plan: ListingPlan,
  checkpoint: ListingCheckpoint,
  keys: { listing: string; revision: string },
  onAcknowledged?: (checkpoint: ListingCheckpoint) => void,
) {
  const primary =
    bundle.evidence.state === "complete"
      ? bundle.revision.representations.filter(
          (item) => item.role === "primary" && item.validationState === "valid",
        )
      : [];
  const suggestion = listingSuggestion(
    primary.length === 1 ? primary[0]!.normalizedFacts : undefined,
  );
  const body = {
    ...structuredClone(plan.body),
    versionRevisionId: bundle.revision.id,
    displayName: plan.body.displayName || suggestion.name || "",
    description: plan.body.description || suggestion.description || "",
  };
  if (!body.displayName || !body.description) {
    const error = new APIError(409, "LISTING_DETAILS_REQUIRED");
    error.message =
      "Content is saved. Enter a Listing name and description, then continue with the saved content.";
    throw error;
  }
  if (!checkpoint.listing) {
    const reply = await client.api.POST("/api/v1/catalog-listings", {
      params: { header: { "Idempotency-Key": keys.listing } },
      body: { resourceId: resource.id, namespaceId: resource.namespaceId },
    });
    result(reply);
    const etag = commandETag(reply.response);
    const listing = result(
      reply,
      (value) =>
        checks.CatalogListing(value) &&
        object(value) &&
        value.tenantId === resource.tenantId &&
        value.resourceId === resource.id &&
        value.namespaceId === resource.namespaceId,
    );
    checkpoint.listing = listing;
    checkpoint.etag = etag;
    onAcknowledged?.({ ...checkpoint });
  }
  if (!checkpoint.etag)
    throw new Error(
      "The Listing has no captured ETag. Reload it before editing.",
    );
  if (!checkpoint.revision) {
    const id = checkpoint.listing.id;
    const old = checkpoint.etag;
    const reply = await client.api.POST(
      "/api/v1/catalog-listings/{listingId}/revisions",
      {
        params: {
          path: { listingId: id },
          header: { "Idempotency-Key": keys.revision, "If-Match": old },
        },
        body,
      },
    );
    result(reply);
    const etag = commandETag(reply.response);
    const saved = result(
      reply,
      (value) =>
        checks.ListingRevisionCreated(value) &&
        object(value) &&
        object(value.listing) &&
        object(value.revision) &&
        value.listing.id === id &&
        value.listing.tenantId === resource.tenantId &&
        value.listing.resourceId === resource.id &&
        value.listing.namespaceId === resource.namespaceId &&
        value.revision.listingId === id &&
        value.revision.tenantId === resource.tenantId &&
        value.revision.versionRevisionId === bundle.revision.id &&
        value.listing.candidateRevisionId === value.revision.id &&
        etag !== old,
    );
    checkpoint.listing = saved.listing;
    checkpoint.revision = saved.revision;
    checkpoint.etag = etag;
    onAcknowledged?.({ ...checkpoint });
  }
  return checkpoint;
}
