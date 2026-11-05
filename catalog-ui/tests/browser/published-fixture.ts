import { object, array } from "./json";
// Pagination prerequisites use the reviewed Agent example and real governed publication steps.
import { readFile } from "node:fs/promises";
import type { Catalog } from "./fixtures";

const example = object(
  JSON.parse(
    await readFile(
      new URL("../../../api/examples/agent-lifecycle.json", import.meta.url),
      "utf8",
    ),
  ),
);
const versionExample = object(
  array(example.requests)
    .map(object)
    .find((r) => r.name === "Create Agent version"),
);
export const agentDocument = object(
  object(array(object(versionExample.body).representations)[0]).data,
);

// Real API prerequisites for pagination and publication membership. UI actions are asserted in the
// owning journey; this does not replace browser authoring/review tests.
export async function publishFixture(
  catalog: Catalog,
  index: number,
  documentPadding = "",
  options: {
    visibility?: "public" | "tenant" | "private";
    representativeQueries?: string[];
    displayName?: string;
    description?: string;
    nativeDocument?: Record<string, unknown>;
    additionalRepresentations?: Record<string, unknown>[];
  } = {},
) {
  const { data: resource } = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/resources",
    {
      namespaceId: catalog.receipt.namespaceId,
      kind: "agent",
      canonicalIdentifier: `urn:air:demo.catalog.example.com:demo:paged-${index}`,
    },
  );
  const { data: version } = await catalog.admin<{ revision: { id: string } }>(
    "POST",
    `/api/v1/resources/${resource.id}/versions`,
    {
      version: "1.0.0",
      representations: [
        {
          role: "primary",
          mediaType: "application/a2a-agent-card+json",
          data: documentPadding
            ? {
                ...agentDocument,
                skills: array(agentDocument.skills)
                  .map(object)
                  .map((skill: Record<string, unknown>, i: number) =>
                    i === 0
                      ? { ...skill, description: documentPadding }
                      : skill,
                  ),
              }
            : (options.nativeDocument ?? agentDocument),
        },
        ...(options.additionalRepresentations ?? []),
      ],
    },
  );
  const listing = await catalog.admin<{ id: string }>(
    "POST",
    "/api/v1/catalog-listings",
    { namespaceId: catalog.receipt.namespaceId, resourceId: resource.id },
  );
  const revision = await catalog.admin(
    "POST",
    `/api/v1/catalog-listings/${listing.data.id}/revisions`,
    {
      versionRevisionId: version.revision.id,
      publisherId: catalog.receipt.publisherId,
      displayName:
        options.displayName ?? `Paged agent ${String(index).padStart(2, "0")}`,
      description: options.description ?? "Synthetic pagination metadata",
      tags: ["paged"],
      categories: ["testing", "tools"],
      representativeQueries: options.representativeQueries ?? [
        "Find paged agents",
        "Browse synthetic agents",
      ],
      license: "Apache-2.0",
      visibility: options.visibility ?? "public",
    },
    listing.etag,
  );
  const submission = await catalog.admin<{ id: string }>(
    "POST",
    `/api/v1/catalog-listings/${listing.data.id}/submissions`,
    {},
  );
  await catalog.admin(
    "POST",
    `/api/v1/review-submissions/${submission.data.id}:approve`,
    { reason: "Independent pagination fixture review" },
    submission.etag,
    "local-reviewer",
  );
  const { data } = await catalog.admin<{ release: { id: string } }>(
    "POST",
    `/api/v1/catalog-listings/${listing.data.id}:publish`,
    { submissionId: submission.data.id },
    revision.etag,
  );
  return data.release.id;
}
