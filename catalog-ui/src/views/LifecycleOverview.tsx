// Lifecycle is an inspection of selected exact contexts, not aggregate Resource state.
import { useState } from "react";
import { result } from "../api/client";
import { object, page } from "../api/values";
import { permits, permitsNamespace } from "../api/permissions";
import type { components } from "../api/schema";
import { ChoiceField, EvidenceDetails } from "./authoring";
import {
  Field,
  Pager,
  ReadState,
  useRead,
  type WorkspaceProps,
} from "./workspace";
import { ValidationFindings, ReviewDecisions } from "./ValidationFindings";
import * as checks from "./responses";

type Props = WorkspaceProps & {
  resourceId: string;
  namespaceId: string;
  listing?: components["schemas"]["CatalogListing"];
};
export function LifecycleOverview(props: Props) {
  return (
    <LifecycleContext
      key={JSON.stringify([
        props.access,
        props.resourceId,
        props.namespaceId,
        props.listing,
      ])}
      {...props}
    />
  );
}
function LifecycleContext(props: Props) {
  const [open, setOpen] = useState(false);
  const [versionId, setVersionId] = useState("");
  const [listingId, setListingId] = useState(props.listing?.id ?? "");
  const [revisionId, setRevisionId] = useState("");
  const [selected, setSelected] = useState<{
    versionId: string;
    listingId: string;
    revisionId: string;
    generation: number;
  }>();
  const history = permitsNamespace(
    props.access,
    "resource.read_history",
    "catalog.read",
    props.namespaceId,
  );
  const content = permitsNamespace(
    props.access,
    "resource.read_content",
    "catalog.read",
    props.namespaceId,
  );
  return (
    <section className="workspace-panel" aria-label="Lifecycle overview">
      <h2>Lifecycle overview</h2>
      <p>
        A Resource has no single draft or published state. Choose one Version
        and one Listing revision. Draft, validation, review, release and
        discovery are separate stages.
      </p>
      {!selected && (
        <ol>
          <li>
            Draft:{" "}
            {props.listing?.candidateRevisionId ? (
              <>
                Current Listing candidate{" "}
                <code>{props.listing.candidateRevisionId}</code>.
              </>
            ) : (
              "Version and Listing revision not inspected."
            )}
          </li>
          <li>Validation: not inspected.</li>
          <li>Review: not inspected.</li>
          <li>
            Release:{" "}
            {props.listing?.currentReleaseId ? (
              <>
                Current Listing release pointer{" "}
                <code>{props.listing.currentReleaseId}</code>; selected context
                not inspected.
              </>
            ) : (
              "Not inspected."
            )}
          </li>
          <li>Discovery: no host or audience inspected.</li>
        </ol>
      )}
      {!history ? (
        <p>
          Lifecycle history requires resource.read_history in this namespace and
          catalog.read scope when enforced.
        </p>
      ) : (
        <>
          <button
            type="button"
            onClick={() => setOpen((v) => !v)}
            aria-expanded={open}
          >
            {open ? "Close lifecycle choices" : "Choose lifecycle context"}
          </button>
          {open && (
            <>
              <ChoiceField
                label="Lifecycle Version"
                value={versionId}
                onChange={setVersionId}
                onFailure={props.onFailure}
                queryKey={props.resourceId + "lifecycle-versions"}
                load={async (cursor, signal) => {
                  const rows = result(
                    await props.client.api.GET(
                      "/api/v1/resources/{resourceId}/versions",
                      {
                        params: {
                          path: { resourceId: props.resourceId },
                          query: { limit: 25, ...(cursor ? { cursor } : {}) },
                        },
                        signal,
                      },
                    ),
                    page(checks.ResourceVersion),
                  );
                  if (
                    rows.items.some(
                      (item) => item.resourceId !== props.resourceId,
                    )
                  )
                    throw new Error(
                      "Version choices do not match this Resource.",
                    );
                  return {
                    ...rows,
                    items: rows.items.map((item) => ({
                      id: item.id,
                      label: `${item.version} / ${item.id}`,
                    })),
                  };
                }}
              />
              {!props.listing && (
                <ChoiceField
                  label="Lifecycle Listing"
                  value={listingId}
                  onChange={(value) => {
                    setListingId(value);
                    setRevisionId("");
                  }}
                  onFailure={props.onFailure}
                  queryKey={props.resourceId + "lifecycle-listings"}
                  load={async (cursor, signal) => {
                    const rows = result(
                      await props.client.api.GET(
                        "/api/v1/resources/{resourceId}/listings",
                        {
                          params: {
                            path: { resourceId: props.resourceId },
                            query: { limit: 25, ...(cursor ? { cursor } : {}) },
                          },
                          signal,
                        },
                      ),
                      page(checks.CatalogListing),
                    );
                    if (
                      rows.items.some(
                        (item) =>
                          item.resourceId !== props.resourceId ||
                          item.namespaceId !== props.namespaceId,
                      )
                    )
                      throw new Error(
                        "Listing choices do not match this Resource.",
                      );
                    return {
                      ...rows,
                      items: rows.items.map((item) => ({
                        id: item.id,
                        label: item.id,
                      })),
                    };
                  }}
                />
              )}
              {listingId && (
                <ChoiceField
                  label="Lifecycle Listing revision"
                  value={revisionId}
                  onChange={setRevisionId}
                  onFailure={props.onFailure}
                  queryKey={listingId + "lifecycle-revisions"}
                  load={async (cursor, signal) => {
                    const rows = result(
                      await props.client.api.GET(
                        "/api/v1/catalog-listings/{listingId}/revisions",
                        {
                          params: {
                            path: { listingId },
                            query: { limit: 25, ...(cursor ? { cursor } : {}) },
                          },
                          signal,
                        },
                      ),
                      page(checks.RevisionSummary),
                    );
                    return {
                      ...rows,
                      items: rows.items.map((item) => ({
                        id: item.id,
                        label: `Revision ${item.revisionNumber} / ${item.id}`,
                      })),
                    };
                  }}
                />
              )}
              {!content && (
                <p>
                  Inspection of documents and validation additionally requires
                  resource.read_content. History rows alone do not grant that
                  access.
                </p>
              )}
              <button
                type="button"
                disabled={!content || !versionId || !listingId || !revisionId}
                onClick={() =>
                  setSelected((previous) => ({
                    versionId,
                    listingId,
                    revisionId,
                    generation: (previous?.generation ?? 0) + 1,
                  }))
                }
              >
                Inspect selected lifecycle
              </button>
            </>
          )}
          {selected && content && (
            <SelectedLifecycle
              key={JSON.stringify(selected)}
              {...props}
              {...selected}
            />
          )}
        </>
      )}
    </section>
  );
}
function SelectedLifecycle(
  props: Props & { versionId: string; listingId: string; revisionId: string },
) {
  const read = useRead(
    props.versionId + props.revisionId,
    async (signal) => {
      const version = result(
        await props.client.api.GET("/api/v1/resource-versions/{versionId}", {
          params: { path: { versionId: props.versionId } },
          signal,
        }),
        checks.ResourceVersion,
      );
      const listing = result(
        await props.client.api.GET("/api/v1/catalog-listings/{listingId}", {
          params: { path: { listingId: props.listingId } },
          signal,
        }),
        checks.CatalogListing,
      );
      const revision = result(
        await props.client.api.GET("/api/v1/listing-revisions/{revisionId}", {
          params: { path: { revisionId: props.revisionId } },
          signal,
        }),
        checks.ListingRevision,
      );
      if (
        version.id !== props.versionId ||
        version.resourceId !== props.resourceId ||
        listing.id !== props.listingId ||
        listing.resourceId !== props.resourceId ||
        listing.namespaceId !== props.namespaceId ||
        revision.id !== props.revisionId ||
        revision.listingId !== listing.id ||
        listing.tenantId !== props.access.tenantId ||
        revision.tenantId !== props.access.tenantId
      )
        throw new Error(
          "The selected lifecycle context does not match this Resource.",
        );
      const bundle = result(
        await props.client.api.GET("/api/v1/version-revisions/{revisionId}", {
          params: { path: { revisionId: revision.versionRevisionId } },
          signal,
        }),
        checks.RevisionEvidenceEnvelope,
      );
      if (
        bundle.revision.id !== revision.versionRevisionId ||
        bundle.revision.resourceVersionId !== version.id ||
        bundle.namespaceId !== props.namespaceId
      )
        throw new Error(
          "The Listing revision refers to a different Version. Select its exact Version before inspecting.",
        );
      return { version, listing, revision, bundle };
    },
    props.onFailure,
  );
  return (
    <>
      <ReadState {...read} />
      {read.data && (
        <section aria-label="Selected lifecycle context">
          <h3>Selected Version {read.data.version.version}</h3>
          <p>
            Version <code>{read.data.version.id}</code>; Listing{" "}
            <code>{read.data.listing.id}</code>; Listing revision{" "}
            <code>{read.data.revision.id}</code>; Version revision{" "}
            <code>{read.data.bundle.revision.id}</code>.
          </p>
          <p>
            Version is{" "}
            {read.data.version.sealedRevisionId
              ? "sealed; use a new version label for replacement"
              : "a draft"}
            . Its current candidate is{" "}
            <code>{read.data.version.candidateRevisionId ?? "none"}</code>.
          </p>
          <p>
            {read.data.listing.candidateRevisionId === read.data.revision.id
              ? "This is the current Listing candidate."
              : "This is a historical Listing revision. Newer candidates do not invalidate an older immutable approval."}
          </p>
          {!read.data.version.sealedRevisionId &&
            permitsNamespace(
              props.access,
              "resource.edit",
              "catalog.write",
              props.namespaceId,
            ) && (
              <a
                href={`${props.runtime.basePath}/versions/${read.data.version.id}`}
              >
                Correct Version draft
              </a>
            )}
          {permitsNamespace(
            props.access,
            "resource.edit",
            "catalog.write",
            props.namespaceId,
          ) && (
            <p>
              <a
                href={`${props.runtime.basePath}/resources/${props.resourceId}#listings`}
              >
                Prepare listing metadata
              </a>
              . Reuse this Resource's existing Listing.
            </p>
          )}
          <ValidationFindings
            evidence={read.data.bundle.evidence}
            revisionId={read.data.bundle.revision.id}
          />
          <EvidenceDetails
            title="Complete validation JSON"
            value={read.data.bundle.evidence}
          />
          <LifecycleHistory
            {...props}
            versionRevisionId={read.data.bundle.revision.id}
          />
          <DiscoveryObservation {...props} />
        </section>
      )}
    </>
  );
}
function LifecycleHistory(
  props: Props & {
    listingId: string;
    revisionId: string;
    versionRevisionId: string;
  },
) {
  const [reviewCursor, setReviewCursor] = useState("");
  const [releaseCursor, setReleaseCursor] = useState("");
  const reviews = useRead(
    props.listingId + reviewCursor,
    async (signal) => {
      const rows = result(
        await props.client.api.GET("/api/v1/review-submissions", {
          params: {
            query: {
              listingId: props.listingId,
              limit: 25,
              ...(reviewCursor ? { cursor: reviewCursor } : {}),
            },
          },
          signal,
        }),
        page(checks.ReviewSubmission),
      );
      if (
        rows.items.some(
          (item) =>
            item.listingId !== props.listingId ||
            item.tenantId !== props.access.tenantId,
        )
      )
        throw new Error("Review history does not match this Listing.");
      return rows;
    },
    props.onFailure,
  );
  const releases = useRead(
    props.listingId + releaseCursor,
    async (signal) => {
      const rows = result(
        await props.client.api.GET(
          "/api/v1/catalog-listings/{listingId}/releases",
          {
            params: {
              path: { listingId: props.listingId },
              query: {
                limit: 25,
                ...(releaseCursor ? { cursor: releaseCursor } : {}),
              },
            },
            signal,
          },
        ),
        page(checks.ListingRelease),
      );
      if (
        rows.items.some(
          (item) =>
            item.listingId !== props.listingId ||
            item.tenantId !== props.access.tenantId,
        )
      )
        throw new Error("Release history does not match this Listing.");
      return rows;
    },
    props.onFailure,
  );
  const matching = (item: {
    listingRevisionId: string;
    versionRevisionId: string;
  }) =>
    item.listingRevisionId === props.revisionId &&
    item.versionRevisionId === props.versionRevisionId;
  return (
    <>
      <h3>Review for this context</h3>
      <ReadState {...reviews} />
      <p>
        Only matching submissions on the selected page appear below. No matching
        row is not proof of absence from older pages. Approval still requires
        the release command's current eligibility checks.
      </p>
      {reviews.data?.items.filter(matching).map((item) => (
        <section key={item.id}>
          <a href={`${props.runtime.basePath}/reviews/${item.id}`}>
            {item.state} / {item.id}
          </a>
          <ReviewDecisions submission={item} />
        </section>
      ))}
      <Pager
        cursor={reviewCursor}
        nextCursor={reviews.data?.nextCursor}
        busy={reviews.busy}
        onPage={setReviewCursor}
      />
      <h3>Release for this context</h3>
      <ReadState {...releases} />
      <p>
        Release is separate from discovery. No matching release on this page
        does not establish that this submission was never released.
      </p>
      {releases.data?.items.filter(matching).map((item) => (
        <p key={item.id}>
          <a href={`${props.runtime.basePath}/releases/${item.id}`}>
            {item.state} / {item.id}
          </a>
          ; submission <code>{item.submissionId}</code>
          {item.replacementReleaseId && (
            <>
              ; replacement <code>{item.replacementReleaseId}</code>
            </>
          )}
        </p>
      ))}
      <Pager
        cursor={releaseCursor}
        nextCursor={releases.data?.nextCursor}
        busy={releases.busy}
        onPage={setReleaseCursor}
      />
    </>
  );
}
function DiscoveryObservation(props: WorkspaceProps) {
  const [host, setHost] = useState("");
  const [audience, setAudience] = useState<"anonymous" | "authenticated">(
    "anonymous",
  );
  const [selected, setSelected] = useState<{
    host: string;
    audience: "anonymous" | "authenticated";
    generation: number;
  }>();
  const allowed = permits(props.access, "publication.read", "catalog.read");
  return (
    <section aria-label="Discovery observation">
      <h3>Discovery</h3>
      {!selected && (
        <p>
          Not inspected for a host and audience. A release alone does not
          establish active discovery.
        </p>
      )}
      {allowed ? (
        <>
          <Field label="Lifecycle discovery host">
            <input
              maxLength={253}
              value={host}
              onChange={(event) => setHost(event.target.value)}
            />
          </Field>
          <Field label="Lifecycle discovery audience">
            <select
              value={audience}
              onChange={(event) =>
                setAudience(event.target.value as typeof audience)
              }
            >
              <option value="anonymous">Anonymous</option>
              <option value="authenticated">Authenticated</option>
            </select>
          </Field>
          <button
            type="button"
            disabled={!host.trim()}
            onClick={() =>
              setSelected((previous) => ({
                host: host.trim().toLowerCase(),
                audience,
                generation: (previous?.generation ?? 0) + 1,
              }))
            }
          >
            Inspect chosen discovery host
          </button>
          {selected && (
            <SnapshotObservation
              key={JSON.stringify(selected)}
              {...props}
              {...selected}
            />
          )}
        </>
      ) : (
        <p>
          Host inspection requires tenant-wide publication.read and catalog.read
          scope when enforced. This namespace's release access does not grant
          it.
        </p>
      )}
    </section>
  );
}
function SnapshotObservation(
  props: WorkspaceProps & {
    host: string;
    audience: "anonymous" | "authenticated";
  },
) {
  const [cursor, setCursor] = useState("");
  const read = useRead(
    props.host + cursor,
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/publication/snapshots", {
          params: {
            query: {
              host: props.host,
              limit: 25,
              ...(cursor ? { cursor } : {}),
            },
          },
          signal,
        }),
        page(
          (value) =>
            checks.PublicationSnapshotSummary(value) &&
            object(value) &&
            value.host === props.host,
        ),
      ),
    props.onFailure,
  );
  return (
    <>
      <ReadState {...read} />
      <p>
        {read.data ? "Observed host" : "Selected host"}{" "}
        <code>{props.host}</code>, audience {props.audience}. Snapshot metadata
        does not by itself prove inclusion of this exact release. Open a
        snapshot to inspect its documents and exclusions. This page says nothing
        about other hosts.
      </p>
      {read.data?.items
        .filter((item) => item.audience === props.audience)
        .map((item) => (
          <p key={item.id}>
            <a
              href={`${props.runtime.basePath}/publication/${item.id}?host=${encodeURIComponent(props.host)}`}
            >
              Snapshot {item.id}
            </a>
            : generation {item.generation}, {item.state}.
          </p>
        ))}
      <Pager
        cursor={cursor}
        nextCursor={read.data?.nextCursor}
        busy={read.busy}
        onPage={setCursor}
      />
    </>
  );
}
