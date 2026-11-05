// Reuse the Listing editor, including advanced fields, inside the authoring workspace.
import type { ListingSnapshot } from "./draft-editor";
import { useEffect, useRef, useState } from "react";
import type { components } from "../api/schema";
import { result } from "../api/client";
import { page, object } from "../api/values";
import { ListingForm, type ListingFormHandle } from "./Listings";
import { ReadState, useRead, type WorkspaceProps } from "./workspace";
import { permitsNamespace } from "../api/permissions";
import { ActionForm, EvidenceDetails } from "./authoring";
import { savePreparedListing, type ListingCheckpoint } from "./authoring-save";
import * as checks from "./responses";

type Listing = components["schemas"]["CatalogListing"];
export type ListingPlan = {
  listing?: Listing | undefined;
  etag?: string | null | undefined;
  body: components["schemas"]["CreateListingRevision"];
};
export type ListingPreparationHandle = {
  read: () => ListingPlan;
  snapshot: () => ListingSnapshot;
};
export default function ListingPreparation(
  props: WorkspaceProps & {
    resource?: components["schemas"]["Resource"] | undefined;
    namespaceId: string;
    snapshot?: ListingSnapshot | undefined;
    known?: import("./authoring-save").VersionBundle | undefined;
    handle: React.RefObject<ListingPreparationHandle | undefined>;
  },
) {
  const fields = useRef<ListingFormHandle | undefined>(undefined);
  const context = useRead(
    props.resource?.id ?? "new",
    async (signal) => {
      const resource = props.resource;
      if (!resource)
        return { listing: undefined, etag: undefined, initial: null };
      const found = result(
        await props.client.api.GET("/api/v1/resources/{resourceId}/listings", {
          params: { path: { resourceId: resource.id }, query: { limit: 2 } },
          signal,
        }),
        page(checks.CatalogListing),
      );
      if (
        found.nextCursor ||
        found.items.length > 1 ||
        found.items.some(
          (item) =>
            item.resourceId !== resource.id ||
            item.namespaceId !== resource.namespaceId ||
            item.tenantId !== resource.tenantId,
        )
      )
        throw new Error(
          "The Listing lookup is incomplete or does not match this Resource. No new Listing will be created.",
        );
      const id = found.items[0]?.id;
      if (!id) return { listing: undefined, etag: undefined, initial: null };
      const reply = await props.client.api.GET(
        "/api/v1/catalog-listings/{listingId}",
        { params: { path: { listingId: id } }, signal },
      );
      const listing = result(
        reply,
        (value) =>
          checks.CatalogListing(value) &&
          object(value) &&
          value.id === id &&
          value.resourceId === resource.id &&
          value.namespaceId === resource.namespaceId &&
          value.tenantId === resource.tenantId,
      );
      const etag = reply.response.headers.get("ETag");
      if (!etag || !/^"[^"\r\n]+"$/.test(etag))
        throw new Error(
          "The Listing response has no usable revision. Reload its current state before editing.",
        );
      const initial = listing.candidateRevisionId
        ? result(
            await props.client.api.GET(
              "/api/v1/listing-revisions/{revisionId}",
              {
                params: { path: { revisionId: listing.candidateRevisionId } },
                signal,
              },
            ),
            (value) =>
              checks.ListingRevision(value) &&
              object(value) &&
              value.id === listing.candidateRevisionId &&
              value.listingId === id &&
              value.tenantId === resource.tenantId &&
              (value.extensions === undefined || object(value.extensions)),
          )
        : null;
      return { listing, etag, initial };
    },
    props.onFailure,
  );
  useEffect(() => {
    const handle = props.handle;
    handle.current =
      context.data && !context.busy && !context.error
        ? {
            snapshot: () => {
              if (!fields.current)
                throw new Error("Wait for Listing details to load.");
              return fields.current.snapshot();
            },
            read: () => {
              if (!fields.current)
                throw new Error("Wait for Listing details to load.");
              return {
                listing: context.data!.listing,
                etag: context.data!.etag,
                body: fields.current.read(props.known?.revision.id ?? "", true),
              };
            },
          }
        : undefined;
    return () => {
      handle.current = undefined;
    };
  });
  return (
    <section aria-label="Listing details" className="workspace-panel">
      <h3>Listing details</h3>
      <p>
        Prepare the name, description and Publisher here. The saved Listing will
        reference the exact content revision from this workspace.
      </p>
      <ReadState {...context} />
      {context.data && (
        <>
          {context.data.listing && (
            <p>
              Update existing Listing <code>{context.data.listing.id}</code>.
              Keep its metadata and link the newly saved content. Published
              metadata stays unchanged.
            </p>
          )}
          {!context.data.initial && (
            <p>
              Leave the name or description empty to use validated
              primary-document values after saving content. If those values are
              unavailable, enter them before continuing.
            </p>
          )}
          <ListingForm
            {...props}
            embedded
            snapshot={props.snapshot}
            formHandle={fields}
            listing={context.data.listing}
            initial={context.data.initial}
            etag={context.data.etag}
            known={props.known}
          />
        </>
      )}
    </section>
  );
}

// Existing content enters the same Listing step with its exact revision already selected.
export function SavedListingAuthoring(
  props: WorkspaceProps & {
    resource: components["schemas"]["Resource"];
    version: components["schemas"]["ResourceVersion"];
    known?: import("./authoring-save").VersionBundle;
  },
) {
  const handle = useRef<ListingPreparationHandle | undefined>(undefined);
  const [saved, setSaved] = useState<ListingCheckpoint>();
  const revisionId =
    props.known?.revision.id ??
    props.version.sealedRevisionId ??
    props.version.candidateRevisionId;
  const current = useRead(
    revisionId ?? "",
    async (signal) => {
      if (props.known) return props.known;
      if (!revisionId)
        throw new Error("This Version has no retained revision.");
      const bundle = result(
        await props.client.api.GET("/api/v1/version-revisions/{revisionId}", {
          params: { path: { revisionId } },
          signal,
        }),
        (value) =>
          checks.RevisionEvidenceEnvelope(value) &&
          object(value) &&
          object(value.revision) &&
          value.namespaceId === props.resource.namespaceId &&
          value.revision.id === revisionId &&
          value.revision.resourceVersionId === props.version.id &&
          value.revision.tenantId === props.resource.tenantId,
      );
      return {
        version: props.version,
        revision: bundle.revision,
        evidence: bundle.evidence,
      };
    },
    props.onFailure,
  );
  const allowed = permitsNamespace(
    props.access,
    "resource.edit",
    "catalog.write",
    props.resource.namespaceId,
  );
  return (
    <section className="workspace-panel">
      <h2>Prepare Listing for saved content</h2>
      <ReadState {...current} />
      {current.data && (
        <ActionForm
          requiredMarkers
          title="Save Listing draft"
          description="Save Listing metadata linked to this exact content. Review and publication remain separate."
          editable={allowed}
          allowed={allowed}
          dirty
          onFailure={props.onFailure}
          prepare={() => {
            if (!handle.current)
              throw new Error("Wait for the complete Listing lookup.");
            const plan = handle.current.read();
            const checkpoint: ListingCheckpoint = {
              listing: saved?.listing ?? plan.listing,
              etag: saved?.etag ?? plan.etag,
            };
            const keys = {
              listing: crypto.randomUUID(),
              revision: crypto.randomUUID(),
            };
            const bundle = current.data!;
            return {
              summary: [
                { label: "Resource", value: props.resource.id },
                { label: "Exact content revision", value: bundle.revision.id },
                {
                  label: "Listing",
                  value: checkpoint.listing?.id ?? "Create for this Resource",
                },
                { label: "Publisher", value: plan.body.publisherId },
                { label: "Visibility", value: plan.body.visibility },
              ],
              run: async () => {
                setSaved({
                  ...(await savePreparedListing(
                    props.client,
                    props.resource,
                    bundle,
                    plan,
                    checkpoint,
                    keys,
                    setSaved,
                  )),
                });
                return "Listing draft saved. Open it for the next permitted review or publication action.";
              },
            };
          }}
        >
          <p>
            Exact content revision: <code>{current.data.revision.id}</code>.
          </p>
          <ListingPreparation
            {...props}
            namespaceId={props.resource.namespaceId}
            known={current.data}
            handle={handle}
          />
        </ActionForm>
      )}
      {saved?.listing && (
        <>
          <a href={`${props.runtime.basePath}/listings/${saved.listing.id}`}>
            Open saved Listing
          </a>
          <EvidenceDetails title="Saved Listing references" value={saved} />
        </>
      )}
    </section>
  );
}
