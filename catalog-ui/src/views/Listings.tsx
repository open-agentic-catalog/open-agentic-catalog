import { snapshotExtensions, type ListingSnapshot } from "./draft-editor";
import { draftArtwork, editorArtwork } from "./draft-artwork";
import { Facts } from "./Facts";
import { ReleaseState } from "./ReleaseState";
import { controlIcons } from "../shell/Icon";
import { LifecycleOverview } from "./LifecycleOverview";
import { RevisionComparison } from "./RevisionComparison";
import { ArtworkEditor } from "./ArtworkEditor";
import {
  artworkIRI,
  isArtwork,
  artworkInput,
  type ArtworkDescriptor,
  type NativeIcon,
} from "./artwork";
import { ChoicePrerequisite } from "./ChoicePrerequisite";
import { ValueList } from "./ValueList";
import {
  atField,
  InputError,
  characterText,
  metadataTerms,
  listingLink,
} from "./input-validation";
import * as checks from "./responses";
import { page as checkedPage, object } from "../api/values";
// Listing authoring keeps immutable revisions, submitted evidence and release history distinct.
import { useEffect, useRef, useState } from "react";
import type { components } from "../api/schema";
import { result } from "../api/client";
import {
  permitsNamespace,
  publisherAccess,
  contributionReason,
  permits,
} from "../api/permissions";
import { ActionForm, ChoiceField, EvidenceDetails } from "./authoring";
import type { WorkspaceProps } from "./workspace";
import {
  Field,
  Heading,
  Pager,
  ReadState,
  useRead,
  confirmDiscard,
} from "./workspace";
import { parseObject } from "./representations";
import {
  ListingMetadata,
  RelationshipEditor,
  relationshipDraft,
  relationshipInputs,
  listingVersionRevision,
} from "./listing-metadata";
import { useNamespaceAccess } from "./namespace-access";
import { ListingPrefill, listingSuggestion } from "./ListingPrefill";
import {
  ListingRevisionReview,
  listingRevisionChanges,
} from "./ListingRevisionReview";

/** Normalize example queries and enforce the public-listing query-count requirement. */
export function listingQueries(input: string, visibility: string) {
  const queries = [
    ...new Set(
      input
        .split("\n")
        .map((value) => value.trim().split(/\s+/u).join(" "))
        .filter(Boolean),
    ),
  ];
  if (visibility === "public" && (queries.length < 2 || queries.length > 5))
    throw new InputError(
      "Public listings need two to five representative queries. Use private visibility while drafting incomplete metadata.",
      "listing-queries",
    );
  return atField("listing-queries", () => {
    if (queries.length > 5)
      throw new Error(
        "Use up to five representative queries for any visibility.",
      );
    for (const query of queries)
      characterText(query, "Representative queries", 240, 3);
    return queries;
  });
}

/** Inspect a listing's candidate and retained history, with explicit revision and review actions. */
export default function Listings(props: WorkspaceProps) {
  const detail = useRead(
    props.id,
    async (signal) => {
      const reply = await props.client.api.GET(
        "/api/v1/catalog-listings/{listingId}",
        { params: { path: { listingId: props.id } }, signal },
      );
      return {
        listing: result(reply, checks.CatalogListing),
        etag: reply.response.headers.get("ETag"),
      };
    },
    props.onFailure,
  );
  const [tab, setTab] = useState(() =>
    location.hash === "#release-history" ? "releases" : "revisions",
  );
  const [edit, setEdit] = useState(false);
  const [submitted, setSubmitted] = useState("");
  const [saved, setSaved] = useState<{
    listing: components["schemas"]["CatalogListing"];
    etag: string;
  }>();
  const listing = saved?.listing ?? detail.data?.listing;
  const etag = saved?.etag ?? detail.data?.etag;
  const namespaceAccess = useNamespaceAccess(props, listing?.namespaceId || "");
  const contentAllowed =
    !!listing &&
    permitsNamespace(
      namespaceAccess,
      "resource.read_content",
      "catalog.read",
      listing.namespaceId,
    );
  const historyAllowed =
    !!listing &&
    permitsNamespace(
      namespaceAccess,
      "resource.read_history",
      "catalog.read",
      listing.namespaceId,
    );
  const editingAllowed =
    !!listing &&
    permitsNamespace(
      namespaceAccess,
      "resource.edit",
      "catalog.write",
      listing.namespaceId,
    );
  const revisionReason =
    contributionReason(namespaceAccess, listing?.namespaceId || "") ??
    (!etag
      ? "Reload the listing to obtain its current revision before saving."
      : undefined);
  return (
    <>
      <Heading
        title="Listing"
        icon="catalog"
        description="Prepare listing metadata for review and inspect its revisions and releases."
      />
      <ReadState {...detail} />
      {listing && (
        <>
          <a
            className="resource-return"
            href={`${props.runtime.basePath}/resources/${listing.resourceId}`}
          >
            {controlIcons.arrow} Back to resource
          </a>
          <section className="workspace-panel">
            <h2>Listing workspace</h2>
            <p>
              {listing.candidateRevisionId
                ? "Candidate revision saved. Inspect its metadata and validation, then submit it for independent review. Editing later does not change an existing submission."
                : "No candidate revision yet. Create a listing revision with an exact version revision, a publisher and descriptive metadata before submitting for review."}
            </p>
            <p>
              Approval allows a publisher to create a release. A release and an
              active public discovery snapshot are separate steps.
            </p>
            <EvidenceDetails title="Listing metadata" value={listing} />
            <div className="workspace-toolbar">
              <button
                disabled={!edit && !editingAllowed}
                onClick={() => {
                  if (!edit || confirmDiscard()) setEdit((v) => !v);
                }}
                aria-expanded={edit}
              >
                {controlIcons["edit"]} New listing revision
              </button>
              {listing.candidateRevisionId && contentAllowed && (
                <a
                  href={`${props.runtime.basePath}/listing-revisions/${listing.candidateRevisionId}`}
                >
                  Inspect candidate revision
                </a>
              )}
              {historyAllowed && listing.currentReleaseId && (
                <a
                  href={`${props.runtime.basePath}/releases/${listing.currentReleaseId}`}
                >
                  Current release
                </a>
              )}
              {historyAllowed && (
                <>
                  <button
                    aria-pressed={tab === "revisions"}
                    onClick={() => setTab("revisions")}
                  >
                    {controlIcons["history"]} Revision history
                  </button>
                  <button
                    aria-pressed={tab === "releases"}
                    onClick={() => setTab("releases")}
                  >
                    {controlIcons["history"]} Release history
                  </button>
                  <a
                    href={`${props.runtime.basePath}/reviews?listing=${listing.id}`}
                  >
                    Review submissions
                  </a>
                </>
              )}
            </div>
          </section>
          {!edit && revisionReason && <p className="muted">{revisionReason}</p>}
          {edit && (
            <ListingEditor
              {...props}
              listing={listing}
              access={namespaceAccess}
              etag={etag}
              onSaved={(listing, etag) => setSaved({ listing, etag })}
            />
          )}
          <LifecycleOverview
            {...props}
            access={namespaceAccess}
            resourceId={listing.resourceId}
            namespaceId={listing.namespaceId}
            listing={listing}
          />
          <RevisionComparison
            {...props}
            access={namespaceAccess}
            kind="listing"
            ownerId={listing.id}
            namespaceId={listing.namespaceId}
          />
          {historyAllowed && (
            <ListingHistory
              key={`${tab}:${tab === "revisions" ? (listing.candidateRevisionId ?? "empty") : ""}`}
              {...props}
              type={tab}
              contentAllowed={contentAllowed}
              currentReleaseId={listing.currentReleaseId}
            />
          )}
          <ActionForm
            requiredMarkers
            title="Submit for review"
            icon="access"
            editable={permitsNamespace(
              namespaceAccess,
              "review.submit",
              "catalog.write",
              listing.namespaceId,
            )}
            another={{
              label: "Prepare another review submission",
              reset: () => {},
            }}
            unavailableReason="Submitting requires a saved candidate and review.submit permission in this namespace. When scopes are enforced, catalog.write is also required."
            description="Submit the current listing candidate and its exact version validation evidence for independent review. Later validation does not replace the submitted evidence."
            allowed={
              !!listing.candidateRevisionId &&
              permitsNamespace(
                namespaceAccess,
                "review.submit",
                "catalog.write",
                listing.namespaceId,
              )
            }
            onFailure={props.onFailure}
            prepare={() => {
              const observed = listing.candidateRevisionId!;
              return {
                summary: [
                  { label: "Listing ID", value: listing.id },
                  { label: "Namespace ID", value: listing.namespaceId },
                  { label: "Resource ID", value: listing.resourceId },
                  { label: "Observed candidate", value: observed },
                  {
                    label: "Selection",
                    value:
                      "The server submits the current candidate at execution. The observed revision is not a precondition.",
                  },
                ],
                run: async (key) => {
                  const submission = result(
                    await props.client.api.POST(
                      "/api/v1/catalog-listings/{listingId}/submissions",
                      {
                        params: {
                          path: { listingId: listing.id },
                          header: { "Idempotency-Key": key },
                        },
                        body: {},
                      },
                    ),
                    (value) =>
                      checks.ReviewSubmission(value) &&
                      object(value) &&
                      value.listingId === listing.id,
                  );
                  setSubmitted(submission.id);
                  return `Submitted listing revision ${submission.listingRevisionId} for independent review. Submission ${submission.id}.${submission.listingRevisionId !== observed ? " The current candidate changed since confirmation was prepared." : ""}`;
                },
              };
            }}
          />
          {submitted && historyAllowed && (
            <a
              className="button primary"
              href={`${props.runtime.basePath}/reviews/${submitted}`}
            >
              Open review submission
            </a>
          )}
        </>
      )}
    </>
  );
}

/** Load the existing listing context before mounting a revision form. */
function ListingEditor(
  props: WorkspaceProps & {
    listing: components["schemas"]["CatalogListing"];
    etag?: string | null | undefined;
    onSaved: (
      listing: components["schemas"]["CatalogListing"],
      etag: string,
    ) => void;
  },
) {
  const [candidate] = useState(props.listing.candidateRevisionId);
  const current = useRead(
    candidate || "empty",
    async (signal) =>
      candidate
        ? result(
            await props.client.api.GET(
              "/api/v1/listing-revisions/{revisionId}",
              {
                params: {
                  path: { revisionId: candidate },
                },
                signal,
              },
            ),
            checks.ListingRevision,
          )
        : null,
    props.onFailure,
  );
  return (
    <>
      <ReadState {...current} />
      {current.data !== undefined && (
        <ListingForm {...props} initial={current.data} />
      )}
    </>
  );
}

/** Prepare listing revisions with explicit version evidence, relationships and captured ETag. */
export type ListingFormHandle = {
  snapshot: () => ListingSnapshot;
  read: (
    revisionId: string,
    allowDefaults?: boolean,
  ) => components["schemas"]["CreateListingRevision"];
};
function ListingFormShell({
  embedded,
  ...props
}: React.ComponentProps<typeof ActionForm> & {
  embedded?: boolean | undefined;
}) {
  return embedded ? (
    <div className="workspace-fields">{props.children}</div>
  ) : (
    <ActionForm {...props} requiredMarkers />
  );
}
export function ListingForm(
  props: WorkspaceProps & {
    listing?: components["schemas"]["CatalogListing"] | undefined;
    namespaceId?: string;
    embedded?: boolean;
    formHandle?: React.RefObject<ListingFormHandle | undefined>;
    known?: import("./authoring-save").VersionBundle | undefined;
    etag?: string | null | undefined;
    initial: components["schemas"]["ListingRevision"] | null;
    snapshot?: ListingSnapshot | undefined;
    onSaved?: (
      listing: components["schemas"]["CatalogListing"],
      etag: string,
    ) => void;
  },
) {
  const initial = props.initial;
  const snapshot = props.snapshot;
  const namespaceId = props.namespaceId ?? props.listing!.namespaceId;
  const namespaceAccess = useNamespaceAccess(props, namespaceId);
  const [name, setName] = useState(
    snapshot?.displayName ?? initial?.displayName ?? "",
  );
  const [description, setDescription] = useState(
    snapshot?.description ?? initial?.description ?? "",
  );
  const [publisher, setPublisher] = useState(
    snapshot ? "" : initial?.publisherId || "",
  );
  const [version, setVersion] = useState("");
  const [revision, setRevision] = useState("");
  const [keepCurrentVersion, setKeepCurrentVersion] = useState(
    !!initial?.versionRevisionId,
  );
  const [visibility, setVisibility] = useState<
    components["schemas"]["Visibility"]
  >(snapshot?.visibility ?? initial?.visibility ?? "private");
  const [tags, setTags] = useState(
    (snapshot ? snapshot.tags : initial?.tags) ?? [],
  );
  const [categories, setCategories] = useState(
    (snapshot ? snapshot.categories : initial?.categories) ?? [],
  );
  const [queries, setQueries] = useState(
    (snapshot
      ? snapshot.representativeQueries
      : initial?.representativeQueries
    )?.join("\n") ?? "",
  );
  const [links, setLinks] = useState({
    documentationUrl:
      (snapshot ? snapshot.documentationUrl : initial?.documentationUrl) ?? "",
    supportUrl: (snapshot ? snapshot.supportUrl : initial?.supportUrl) ?? "",
    termsUrl: (snapshot ? snapshot.termsUrl : initial?.termsUrl) ?? "",
    license: (snapshot ? snapshot.license : initial?.license) ?? "",
  });
  const [relationships, setRelationships] = useState(() =>
    ((snapshot ? snapshot.relationships : initial?.relationships) ?? []).map(
      (value) =>
        relationshipDraft(value as components["schemas"]["Relationship"]),
    ),
  );
  const [extensionsEdited, setExtensionsEdited] = useState(false);
  const [extensions, setExtensions] = useState(
    snapshot
      ? (snapshot.extensions ?? "{}")
      : JSON.stringify(
          Object.fromEntries(
            Object.entries(initial?.extensions || {}).filter(
              ([key]) => key !== artworkIRI,
            ),
          ),
          null,
          2,
        ),
  );
  const [artwork, setArtwork] = useState<ArtworkDescriptor>(() => {
    if (snapshot) return editorArtwork(snapshot.artwork);
    const value = initial?.extensions?.[artworkIRI];
    return isArtwork(value) ? value : { schemaVersion: 1 };
  });
  const [icons, setIcons] = useState<NativeIcon[]>([]);
  const [created, setCreated] = useState("");
  const [changed, setChanged] = useState(false);
  const exactTerms = (values: string[]) => [
    ...new Set(values.map((value) => value.trim()).filter(Boolean)),
  ];
  const primary =
    props.known?.evidence.state === "complete"
      ? props.known.revision.representations.filter(
          (item) => item.role === "primary" && item.validationState === "valid",
        )
      : [];
  const suggestion = listingSuggestion(
    primary.length === 1 ? primary[0]!.normalizedFacts : undefined,
  );
  useEffect(() => {
    if (!props.embedded || initial || snapshot) return;
    setName((value) => value || suggestion.name || "");
    setDescription((value) => value || suggestion.description || "");
  }, [
    props.embedded,
    initial,
    snapshot,
    suggestion.name,
    suggestion.description,
  ]);
  const extensionText = () =>
    snapshotExtensions(
      snapshot
        ? undefined
        : Object.fromEntries(
            Object.entries(initial?.extensions ?? {}).filter(
              ([key]) => key !== artworkIRI,
            ),
          ),
      extensions,
      extensionsEdited,
    );
  const readBody = (
    revisionId: string,
    allowDefaults = false,
  ): components["schemas"]["CreateListingRevision"] => {
    return structuredClone({
      versionRevisionId: revisionId,
      displayName: atField("listing-name", () =>
        characterText(name.trim(), "Display name", 160, allowDefaults ? 0 : 1),
      ),
      description: atField("listing-description", () =>
        characterText(
          description.trim(),
          "Description",
          4000,
          allowDefaults ? 0 : 1,
        ),
      ),
      publisherId: publisher,
      visibility,
      tags: atField("listing-tags", () =>
        metadataTerms(exactTerms(tags), "tags", 32),
      ),
      categories: atField("listing-categories", () =>
        metadataTerms(exactTerms(categories), "categories", 16),
      ),
      representativeQueries: listingQueries(
        queries,
        props.embedded ? "private" : visibility,
      ),
      ...Object.fromEntries(
        Object.entries(links)
          .filter(([, value]) => !!value)
          .map(([key, value]) => [
            key,
            atField(`listing-${key}`, () => {
              if (key !== "license") return listingLink(value);
              return value;
            }),
          ]),
      ),
      relationships: relationshipInputs(relationships),
      extensions: {
        ...atField("listing-extensions", () => parseObject(extensionText())),
        ...(artwork.icon || artwork.screenshots?.length
          ? { [artworkIRI]: artworkInput(artwork) }
          : {}),
      },
    });
  };
  useEffect(() => {
    if (!props.formHandle) return;
    const handle = props.formHandle;
    handle.current = {
      read: readBody,
      snapshot: () => ({
        displayName: name,
        description,
        visibility,
        tags: [...tags],
        categories: [...categories],
        representativeQueries: queries === "" ? [] : queries.split("\n"),
        ...links,
        relationships: relationships.map(
          ({ type, targetIdentifier, targetVersion }) => ({
            type,
            targetIdentifier,
            ...(targetVersion !== undefined ? { targetVersion } : {}),
          }),
        ),
        extensions: extensionText(),
        artwork: draftArtwork(artwork),
      }),
    };
    return () => {
      handle.current = undefined;
    };
  });
  return (
    <>
      <div onChange={() => setChanged(true)}>
        <ListingFormShell
          embedded={props.embedded}
          title="Create listing revision"
          icon="save"
          editable={permitsNamespace(
            namespaceAccess,
            "resource.edit",
            "catalog.write",
            namespaceId,
          )}
          unavailableReason={
            contributionReason(namespaceAccess, namespaceId) ??
            "Reload the listing to obtain its current revision before saving."
          }
          description="Save a new immutable listing revision. Existing published metadata remains unchanged until an approved revision is published."
          allowed={
            !!props.etag &&
            permitsNamespace(
              namespaceAccess,
              "resource.edit",
              "catalog.write",
              namespaceId,
            )
          }
          dirty={changed}
          retainDraft={changed}
          onFailure={props.onFailure}
          prepare={() => {
            const body = readBody(
              listingVersionRevision(
                keepCurrentVersion,
                initial?.versionRevisionId,
                revision,
              ),
            );
            const etag = props.etag!;
            const changes = listingRevisionChanges(initial, body);
            return {
              summary: [
                { label: "Listing ID", value: props.listing!.id },
                {
                  label: "Resource ID",
                  value: props.listing!.resourceId,
                },
                { label: "Namespace ID", value: namespaceId },
                { label: "Display name", value: body.displayName },
                { label: "Version revision", value: body.versionRevisionId },
                { label: "Publisher ID", value: body.publisherId },
                { label: "Visibility", value: body.visibility },
                { label: "Revision", value: etag },
                {
                  label: "Revision type",
                  value: initial
                    ? "Edit saved candidate"
                    : "First listing revision",
                },
                {
                  label: "Saved listing revision ID",
                  value: initial?.id ?? "None",
                },
              ],
              details: (
                <ListingRevisionReview changes={changes} proposed={body} />
              ),
              run: async (key) => {
                const reply = await props.client.api.POST(
                  "/api/v1/catalog-listings/{listingId}/revisions",
                  {
                    params: {
                      path: { listingId: props.listing!.id },
                      header: { "Idempotency-Key": key, "If-Match": etag },
                    },
                    body,
                  },
                );
                const updatedETag = reply.response.headers.get("ETag");
                const bundle = result(
                  reply,
                  (value) =>
                    checks.ListingRevisionCreated(value) &&
                    object(value) &&
                    object(value.listing) &&
                    object(value.revision) &&
                    value.listing.id === props.listing?.id &&
                    value.listing.namespaceId === namespaceId &&
                    value.listing.resourceId === props.listing?.resourceId &&
                    value.listing.tenantId === props.access.tenantId &&
                    value.revision.tenantId === props.access.tenantId &&
                    value.revision.listingId === props.listing?.id &&
                    value.listing.candidateRevisionId === value.revision.id &&
                    typeof updatedETag === "string" &&
                    /^"[^"\r\n]+"$/.test(updatedETag) &&
                    updatedETag !== etag,
                );
                props.onSaved!(bundle.listing, updatedETag!);
                setCreated(bundle.revision.id);
                return "Listing revision saved.";
              },
            };
          }}
        >
          {!props.embedded && initial?.versionRevisionId && (
            <div className="version-selection">
              <label>
                <input
                  type="checkbox"
                  checked={keepCurrentVersion}
                  onChange={(e) => setKeepCurrentVersion(e.target.checked)}
                />{" "}
                Keep the current version revision
              </label>
              <p className="muted">
                Update listing metadata without changing the version content.{" "}
                <a
                  href={`${props.runtime.basePath}/version-revisions/${initial.versionRevisionId}`}
                >
                  Inspect current version revision
                </a>
                .
              </p>
            </div>
          )}
          {!props.embedded && !keepCurrentVersion && (
            <>
              <ChoiceField
                label="Resource version"
                nextSteps={
                  <ChoicePrerequisite
                    props={{ ...props, access: namespaceAccess }}
                    kind="version"
                    resourceId={props.listing!.resourceId}
                    namespaceId={namespaceId}
                  />
                }
                value={version}
                onChange={(id) => {
                  setVersion(id);
                  setRevision("");
                }}
                onFailure={props.onFailure}
                load={async (cursor, signal) => {
                  const page = result(
                    await props.client.api.GET(
                      "/api/v1/resources/{resourceId}/versions",
                      {
                        params: {
                          path: { resourceId: props.listing!.resourceId },
                          query: {
                            limit: 25,
                            ...(cursor ? { cursor: cursor } : {}),
                          },
                        },
                        signal,
                      },
                    ),
                    checkedPage(checks.ResourceVersion),
                  );
                  return {
                    ...page,
                    items: page.items.map((i) => ({
                      id: i.id,
                      label: i.version,
                    })),
                  };
                }}
              />
              {version && (
                <ChoiceField
                  key={version}
                  label="Version revision"
                  nextSteps={
                    <ChoicePrerequisite
                      props={{ ...props, access: namespaceAccess }}
                      kind="revision"
                      resourceId={props.listing!.resourceId}
                      namespaceId={namespaceId}
                      versionId={version}
                    />
                  }
                  value={revision}
                  onChange={setRevision}
                  onFailure={props.onFailure}
                  load={async (cursor, signal) => {
                    const page = result(
                      await props.client.api.GET(
                        "/api/v1/resource-versions/{versionId}/revisions",
                        {
                          params: {
                            path: { versionId: version },
                            query: {
                              limit: 25,
                              ...(cursor ? { cursor: cursor } : {}),
                            },
                          },
                          signal,
                        },
                      ),
                      checkedPage(checks.RevisionSummary),
                    );
                    return {
                      ...page,
                      items: page.items.map((i) => ({
                        id: i.id,
                        label: `Revision ${i.revisionNumber} by ${i.createdBy}`,
                      })),
                    };
                  }}
                />
              )}
            </>
          )}
          {!props.embedded && (
            <ListingPrefill
              key={keepCurrentVersion ? initial?.versionRevisionId : revision}
              client={props.client}
              revisionID={
                (keepCurrentVersion ? initial?.versionRevisionId : revision) ||
                ""
              }
              name={name}
              description={description}
              onFailure={props.onFailure}
              onNativeIcons={setIcons}
              onApply={(suggestion) => {
                if (suggestion.name !== undefined) setName(suggestion.name);
                if (suggestion.description !== undefined)
                  setDescription(suggestion.description);
                setChanged(true);
              }}
            />
          )}
          {props.embedded &&
            (suggestion.name || suggestion.description) &&
            initial && (
              <button
                type="button"
                onClick={() => {
                  if (
                    (name || description) &&
                    !window.confirm(
                      "Replace the name and description with validated primary-document values?",
                    )
                  )
                    return;
                  if (suggestion.name) setName(suggestion.name);
                  if (suggestion.description)
                    setDescription(suggestion.description);
                }}
              >
                Use validated content name and description
              </button>
            )}
          <Field label="Display name">
            <input
              required={!props.embedded}
              id="listing-name"
              maxLength={320}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </Field>
          <Field label="Description">
            <textarea
              required={!props.embedded}
              id="listing-description"
              maxLength={8000}
              rows={4}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </Field>
          {publisherAccess(props.access).read ? (
            <ChoiceField
              label="Publisher"
              manualLabel="Publisher ID"
              nextSteps={
                <ChoicePrerequisite
                  props={props}
                  kind="publisher"
                  newTab={!!props.embedded}
                />
              }
              value={publisher}
              onChange={setPublisher}
              onFailure={props.onFailure}
              load={async (cursor, signal) => {
                const page = result(
                  await props.client.api.GET("/api/v1/publishers", {
                    params: {
                      query: {
                        limit: 25,
                        ...(cursor ? { cursor: cursor } : {}),
                      },
                    },
                    signal,
                  }),
                  checks.PublisherPage,
                );
                return {
                  ...page,
                  items: page.items.map((i) => ({
                    id: i.id,
                    label: `${i.displayName} / ${i.id}`,
                  })),
                };
              }}
            />
          ) : (
            <Field
              label="Publisher ID"
              hint="Enter the local publisher's UUID, supplied by someone with publisher access. Your current access does not allow browsing Publishers."
            >
              <input
                required
                value={publisher}
                onChange={(e) => setPublisher(e.target.value)}
              />
            </Field>
          )}
          <Field
            label="Visibility"
            hint="Controls who can discover the published listing. A draft remains private until it passes review and publication."
          >
            <select
              required
              value={visibility}
              onChange={(e) =>
                setVisibility(e.target.value as typeof visibility)
              }
            >
              {["private", "tenant", "public"].map((v) => (
                <option key={v}>{v}</option>
              ))}
            </select>
          </Field>
          <ValueList
            id="listing-tags"
            label="Tags"
            itemLabel="Tag"
            value={tags}
            onChange={(values) => {
              setTags(values);
              setChanged(true);
            }}
            maximum={32}
            length={80}
          />
          <ValueList
            id="listing-categories"
            label="Categories"
            itemLabel="Category"
            value={categories}
            onChange={(values) => {
              setCategories(values);
              setChanged(true);
            }}
            maximum={16}
            length={80}
          />
          <Field
            label="Representative queries (one per line)"
            hint="Example searches that describe when this item is useful. Enter one natural-language query per line. Discovery snapshots need two to five queries for both audiences."
          >
            <textarea
              rows={3}
              id="listing-queries"
              value={queries}
              onChange={(e) => setQueries(e.target.value)}
            />
          </Field>
          <p className="muted">
            Both discovery audiences need an AIR identifier, a supported primary
            document and two to five representative queries. Keep incomplete
            metadata in a draft.
          </p>
          {props.embedded && (
            <p className="muted">
              Private Listings need an active Publisher. Tenant visibility also
              requires tenant approval. Public visibility requires tenant and
              domain approval. Saving a draft does not publish it.
            </p>
          )}
          <ArtworkEditor
            value={artwork}
            candidates={icons}
            onChange={(value) => {
              setArtwork(value);
              setChanged(true);
            }}
          />
          <details>
            <summary>Links, license and relationships</summary>
            <p className="muted">
              These fields are optional. Each added relationship needs its
              target identifier; Supersedes also needs an exact target version.
            </p>
            {(Object.keys(links) as (keyof typeof links)[]).map((key) => (
              <Field
                key={key}
                label={
                  {
                    documentationUrl: "Documentation URL",
                    supportUrl: "Support URL",
                    termsUrl: "Terms URL",
                    license: "License",
                  }[key]
                }
              >
                <input
                  id={`listing-${key}`}
                  type={key === "license" ? "text" : "url"}
                  value={links[key]}
                  onChange={(e) =>
                    setLinks((v) => ({ ...v, [key]: e.target.value }))
                  }
                />
              </Field>
            ))}
            <RelationshipEditor
              value={relationships}
              onChange={(value) => {
                setRelationships(value);
                setChanged(true);
              }}
            />
            <Field
              label="Extensions (JSON object)"
              hint="Optional extra metadata as a JSON object. Leave {} if none is needed. Do not store secrets here."
            >
              <textarea
                rows={5}
                id="listing-extensions"
                value={extensions}
                onChange={(e) => {
                  setExtensionsEdited(true);
                  setExtensions(e.target.value);
                }}
              />
            </Field>
          </details>
        </ListingFormShell>
      </div>
      {created && (
        <a
          className="button primary"
          href={`${props.runtime.basePath}/listings/${props.listing!.id}`}
        >
          Reload listing to review saved revision
        </a>
      )}
    </>
  );
}

/** Page the selected revision or release history without loading the other collection. */
export function ListingHistory(
  props: WorkspaceProps & {
    type: string;
    contentAllowed: boolean;
    currentReleaseId?: string | undefined;
  },
) {
  const [cursor, setCursor] = useState("");
  const heading = useRef<HTMLHeadingElement>(null);
  const focused = useRef(false);
  const page = useRead(
    props.id + props.type + cursor,
    async (signal) => {
      const params = {
        path: { listingId: props.id },
        query: { limit: 25, ...(cursor ? { cursor: cursor } : {}) },
      };
      if (props.type === "revisions") {
        const data = result(
          await props.client.api.GET(
            "/api/v1/catalog-listings/{listingId}/revisions",
            { params, signal },
          ),
          checkedPage(checks.RevisionSummary),
        );
        return {
          nextCursor: data.nextCursor,
          items: data.items.map((i) => ({
            id: i.id,
            label: `Revision ${i.revisionNumber} by ${i.createdBy}`,
            release: undefined,
          })),
        };
      }
      const data = result(
        await props.client.api.GET(
          "/api/v1/catalog-listings/{listingId}/releases",
          { params, signal },
        ),
        checkedPage(checks.ListingRelease),
      );
      return {
        nextCursor: data.nextCursor,
        items: data.items.map((i) => ({
          id: i.id,
          label: new Date(i.activatedAt).toLocaleString(),
          release: i,
        })),
      };
    },
    props.onFailure,
  );
  useEffect(() => {
    if (
      !focused.current &&
      props.type === "releases" &&
      location.hash === "#release-history" &&
      !page.busy &&
      !page.error
    ) {
      focused.current = true;
      heading.current?.focus();
    }
  }, [props.type, page.busy, page.error]);
  return (
    <section className="workspace-panel">
      <h2
        ref={heading}
        id={props.type === "releases" ? "release-history" : undefined}
        tabIndex={-1}
      >
        {props.type === "revisions"
          ? "Listing revision history"
          : "Release history"}
      </h2>
      {props.type === "releases" && (
        <p className="muted">
          Retained releases in release-ID order, not version order. Historical
          releases may no longer be available in discovery. Open a release for
          its exact revisions and replacement information.
        </p>
      )}
      <ReadState {...page} />
      {page.data?.items.map((i) => {
        const label =
          props.type === "revisions" && !props.contentAllowed ? (
            i.label
          ) : (
            <a
              href={`${props.runtime.basePath}/${props.type === "revisions" ? "listing-revisions" : "releases"}/${i.id}`}
            >
              {i.release && (
                <>
                  <ReleaseState state={i.release.state} /> -{" "}
                </>
              )}
              {i.label}
            </a>
          );
        if (!i.release) return <p key={i.id}>{label}</p>;
        return (
          <div key={i.id} className="release-history-entry">
            {label}
            {props.currentReleaseId === i.id && (
              <span className="state-badge">Current listing release</span>
            )}
            <Facts
              rows={[
                ["Release ID", <code>{i.id}</code>],
                [
                  "Activated",
                  <time dateTime={i.release.activatedAt}>
                    {new Date(i.release.activatedAt).toLocaleString()}
                  </time>,
                ],
              ]}
            />
          </div>
        );
      })}
      {page.data && !page.data.items.length && <p>No history yet.</p>}
      <Pager
        label={
          props.type === "revisions"
            ? "Listing revisions pagination"
            : "Listing releases pagination"
        }
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        busy={page.busy}
        onPage={setCursor}
      />
    </section>
  );
}

/** Read one immutable listing revision and present its retained metadata. */
export function ListingRevision(props: WorkspaceProps) {
  const detail = useRead(
    props.id,
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/listing-revisions/{revisionId}", {
          params: { path: { revisionId: props.id } },
          signal,
        }),
        checks.ListingRevision,
      ),
    props.onFailure,
  );
  return (
    <>
      <Heading
        title="Listing revision"
        icon="catalog"
        description="Immutable listing metadata retained for review and publication."
      />
      <ReadState {...detail} />
      {detail.data && (
        <section className="workspace-panel">
          <ListingMetadata revision={detail.data} />
          <div className="workspace-toolbar">
            {permits(props.access, "resource.read", "catalog.read") ? (
              <a
                href={`${props.runtime.basePath}/listings/${detail.data.listingId}`}
              >
                Back to listing
              </a>
            ) : (
              <span>
                Listing: <code>{detail.data.listingId}</code>
              </span>
            )}
            <a
              href={`${props.runtime.basePath}/version-revisions/${detail.data.versionRevisionId}`}
            >
              Inspect version revision
            </a>
          </div>
          <EvidenceDetails title="All listing metadata" value={detail.data} />
        </section>
      )}
    </>
  );
}
