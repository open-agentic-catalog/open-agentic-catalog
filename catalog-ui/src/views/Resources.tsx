import { KindBadge } from "./KindBadge";
import { resourceKindPreset } from "./overview-values";
import { ScrollableTable } from "./ScrollableTable";
import { compareTableText, usePageSort } from "./table-sort";
import { LifecycleOverview } from "./LifecycleOverview";
import { characterText } from "./input-validation";
const AuthoringWorkspace = lazy(() =>
  import("./AuthoringWorkspace").then((module) => ({
    default: module.AuthoringWorkspace,
  })),
);
import * as checks from "./responses";
import { page as checkedPage } from "../api/values";
// Governed resource workspaces link stable identities to explicit version and listing authoring.
import { FeedbackEntry } from "./Feedback";
import { lazy, Suspense, useEffect, useId, useRef, useState } from "react";
import type { RefObject } from "react";
import { controlIcons } from "../shell/Icon";
import { result, acknowledge } from "../api/client";
import type { Kind } from "../api/client";
import { kindChoices, kindLabel } from "../resource-kinds";
import {
  permitsNamespace,
  availablePermission,
  resourceCreationAccess,
  contributionReason,
} from "../api/permissions";
import { ActionForm, EvidenceDetails } from "./authoring";
import type { WorkspaceProps } from "./workspace";
import { Field, Heading, Pager, ReadState, Reason, useRead } from "./workspace";
import { useNamespaceAccess } from "./namespace-access";

/** Route local resource authoring to its list, creation form or selected resource. */
export default function Resources(props: WorkspaceProps) {
  return (
    <>
      <Heading
        title="Authoring"
        icon="book"
        description="Create and maintain locally governed Agents, MCP Servers, Skills and Plugins."
      />
      {props.id === "new" ? (
        <Suspense fallback={<p role="status">Loading authoring...</p>}>
          <AuthoringWorkspace {...props} />
        </Suspense>
      ) : !availablePermission(props.access, "resource.read", "catalog.read") &&
        !availablePermission(
          props.access,
          "resource.create",
          "catalog.write",
        ) ? (
        <p role="alert">
          Resource browsing requires resource.read. Creating a resource requires
          resource.create in its destination namespace. When scopes are
          enforced, browsing also requires catalog.read and creation requires
          catalog.write.
        </p>
      ) : !availablePermission(
          props.access,
          "resource.read",
          "catalog.read",
        ) ? (
        props.id ? (
          <p role="alert">
            Opening an existing resource requires resource.read. When scopes are
            enforced, catalog.read is also required.
          </p>
        ) : (
          <a
            className="button primary"
            href={`${props.runtime.basePath}/resources/new`}
          >
            {controlIcons["add"]} Create resource
          </a>
        )
      ) : props.id ? (
        <ResourceDetail {...props} />
      ) : (
        <ResourceList {...props} />
      )}
    </>
  );
}

/** Match Go's Unicode whitespace rules without removing opaque identifier data such as BOM. */
function resourceIdentifierInput(value: string) {
  return value.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, "");
}

/** Page governed resources by kind and exact identity, resetting continuation on filter changes. */
function ResourceList(props: WorkspaceProps) {
  const sortHint = useId();
  const creation = resourceCreationAccess(props.access);
  const [cursor, setCursor] = useState("");
  const [kind, setKind] = useState<Kind | "">(() =>
    resourceKindPreset(location.search),
  );
  useEffect(() => {
    const moved = () => {
      setKind(resourceKindPreset(location.search));
      setCursor("");
    };
    window.addEventListener("popstate", moved);
    return () => window.removeEventListener("popstate", moved);
  }, []);
  const [identifierInput, setIdentifierInput] = useState("");
  const [identifier, setIdentifier] = useState("");
  const [filterError, setFilterError] = useState("");
  const page = useRead(
    JSON.stringify([kind, identifier, cursor]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/resources", {
          params: {
            query: {
              ...(kind ? { kind: kind } : {}),
              ...(identifier ? { canonicalIdentifier: identifier } : {}),
              limit: 25,
              ...(cursor ? { cursor: cursor } : {}),
            },
          },
          signal,
        }),
        checks.ResourcePage,
      ),
    props.onFailure,
  );
  const refresh = () => (page.error && cursor ? setCursor("") : page.refresh());
  const sorted = usePageSort(page.data?.items, {
    identifier: (a, b) =>
      compareTableText(a.canonicalIdentifier, b.canonicalIdentifier),
    kind: (a, b) => compareTableText(kindLabel(a.kind), kindLabel(b.kind)),
    updated: (a, b) => Date.parse(a.updatedAt) - Date.parse(b.updatedAt),
  });
  return (
    <>
      <div className="workspace-toolbar">
        {creation !== "denied" ? (
          <a
            className="button primary"
            href={`${props.runtime.basePath}/resources/new`}
          >
            {creation === "allowed"
              ? "Create resource"
              : "Choose namespace to create"}
          </a>
        ) : (
          <p className="muted">
            {contributionReason(props.access, undefined, "resource.create")}
          </p>
        )}
        <button disabled={page.busy} onClick={refresh}>
          Refresh
        </button>
      </div>
      <form
        className="workspace-toolbar"
        onSubmit={(event) => {
          event.preventDefault();
          const value = resourceIdentifierInput(identifierInput);
          try {
            if (value) characterText(value, "Canonical identifier", 512, 1);
          } catch {
            setFilterError(
              "Enter an identifier of 1 to 512 characters without NUL.",
            );
            return;
          }
          setFilterError("");
          setIdentifier(value);
          setCursor("");
        }}
      >
        <Field label="Resource kind">
          <select
            value={kind}
            onChange={(e) => {
              setKind(e.target.value as Kind | "");
              setCursor("");
              const query = e.target.value ? `?kind=${e.target.value}` : "";
              history.pushState(null, "", location.pathname + query);
            }}
          >
            <option value="">All kinds</option>
            {kindChoices.map(({ value, label }) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Exact canonical identifier">
          <input
            value={identifierInput}
            aria-invalid={filterError !== ""}
            onChange={(event) => setIdentifierInput(event.target.value)}
            placeholder="Paste a known resource identifier"
          />
        </Field>
        <div className="workspace-actions">
          <button type="submit" disabled={page.busy}>
            Find resource
          </button>
          <button
            type="button"
            onClick={() => {
              setIdentifierInput("");
              setIdentifier("");
              setFilterError("");
              setCursor("");
            }}
          >
            Clear identifier
          </button>
        </div>
      </form>

      <p className="muted">
        Find an exact identifier across all resources you can read. This is not
        a name or partial-text search.
      </p>
      {filterError && <p role="alert">{filterError}</p>}
      <ReadState {...page} refresh={refresh} />
      {page.data && (
        <>
          <p id={sortHint} className="muted">
            Select a column heading to sort the results on this page.
          </p>
          <ScrollableTable label="Resource inventory">
            <table className="workspace-table" aria-describedby={sortHint}>
              <thead>
                <tr>
                  {sorted.heading("Identifier", "identifier")}
                  {sorted.heading("Kind", "kind")}
                  {sorted.heading("Updated", "updated")}
                </tr>
              </thead>
              <tbody>
                {sorted.rows.map((item) => (
                  <tr key={item.id}>
                    <td>
                      <a
                        href={`${props.runtime.basePath}/resources/${item.id}`}
                      >
                        {item.canonicalIdentifier}
                      </a>
                    </td>
                    <td className="workspace-table-label">
                      <KindBadge kind={item.kind} />
                    </td>
                    <td>{new Date(item.updatedAt).toLocaleString()}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!page.data.items.length && (
              <p className="workspace-empty">
                No resources match these filters.
              </p>
            )}
          </ScrollableTable>
        </>
      )}
      <Pager
        label="Resource inventory pagination"
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        busy={page.busy}
        onPage={setCursor}
      />
    </>
  );
}

/** Inspect resource state and expose explicit version, listing and lifecycle workflows. */
function ResourceDetail(props: WorkspaceProps) {
  const detail = useRead(
    props.id,
    async (signal) => {
      const reply = await props.client.api.GET(
        "/api/v1/resources/{resourceId}",
        { params: { path: { resourceId: props.id } }, signal },
      );
      return {
        item: result(reply, checks.Resource),
        etag: reply.response.headers.get("ETag"),
      };
    },
    props.onFailure,
  );
  const [tab, setTab] = useState(() =>
    location.hash === "#listings" ? "listings" : "versions",
  );
  const [historyRevision, setHistoryRevision] = useState(0);
  const [listingHistoryBusy, setListingHistoryBusy] = useState(true);
  const history = useRef<HTMLElement>(null);
  const showHistory = (next: string) => {
    if (next === "listings") setListingHistoryBusy(true);
    setTab(next);
    setHistoryRevision((value) => value + 1);
  };
  useEffect(() => {
    if (!historyRevision) return;
    history.current?.focus({ preventScroll: true });
    history.current?.scrollIntoView({ block: "start" });
  }, [historyRevision]);
  const [reason, setReason] = useState("");
  const [listingID, setListingID] = useState("");
  const [createdListingID, setCreatedListingID] = useState("");
  const item = detail.data?.item;
  useEffect(() => {
    if (item && location.hash === "#listings") {
      const section =
        history.current ?? document.getElementById("resource-listing-actions");
      section?.focus();
    }
  }, [item]);
  const namespaceAccess = useNamespaceAccess(props, item?.namespaceId || "");
  return (
    <>
      <a
        className="resource-return"
        href={`${props.runtime.basePath}/resources`}
      >
        {controlIcons.arrow} Back to authoring
      </a>
      <ReadState {...detail} />
      {item && (
        <>
          <section className="workspace-panel">
            <h2>{item.canonicalIdentifier}</h2>
            <p className="resource-identity">
              <KindBadge kind={item.kind} />
              {permitsNamespace(
                namespaceAccess,
                "namespace.read",
                "catalog.read",
                item.namespaceId,
              ) ? (
                <a
                  href={`${props.runtime.basePath}/namespaces/${item.namespaceId}`}
                >
                  Namespace
                </a>
              ) : (
                <span>
                  Namespace: <code>{item.namespaceId}</code>
                </span>
              )}
            </p>
            <p>
              This Resource keeps the same identifier across its versions. Add
              content, prepare a Listing and submit it for independent review.
              Publishing a release is a separate step.
            </p>
            <EvidenceDetails title="Resource metadata" value={item} />
            <div className="workspace-toolbar">
              {!contributionReason(namespaceAccess, item.namespaceId) ? (
                <a
                  className="button primary"
                  href={`${props.runtime.basePath}/versions/new?resource=${item.id}`}
                >
                  {controlIcons["add"]} Add version
                </a>
              ) : (
                <p className="muted">
                  {contributionReason(namespaceAccess, item.namespaceId)}
                </p>
              )}
              <button
                type="button"
                onClick={() => showHistory("versions")}
                aria-pressed={tab === "versions"}
                aria-controls="resource-history"
                title="Show and refresh this resource's versions"
              >
                {controlIcons.list} Versions
              </button>
              <button
                type="button"
                onClick={() => showHistory("listings")}
                aria-pressed={tab === "listings"}
                aria-controls="resource-history"
                title="Show and refresh this resource's listings"
              >
                {controlIcons.catalog} Listings
              </button>
            </div>
          </section>
          <LifecycleOverview
            {...props}
            access={namespaceAccess}
            resourceId={item.id}
            namespaceId={item.namespaceId}
          />
          <FeedbackEntry
            {...props}
            access={namespaceAccess}
            namespaceId={item.namespaceId}
            resourceId={item.id}
          />
          {permitsNamespace(
            namespaceAccess,
            "resource.read_history",
            "catalog.read",
            item.namespaceId,
          ) ? (
            <ResourceHistory
              key={`${tab}-${historyRevision}`}
              {...props}
              metadataAllowed={permitsNamespace(
                namespaceAccess,
                "resource.read",
                "catalog.read",
                item.namespaceId,
              )}
              type={tab}
              historyRef={history}
              onListingKnown={setListingID}
              onListingBusy={setListingHistoryBusy}
            />
          ) : (
            <p className="notice">
              Resource history requires resource.read_history for this
              namespace.
            </p>
          )}
          {tab === "listings" && (
            <section
              id="resource-listing-actions"
              tabIndex={-1}
              aria-label="Listing preparation"
            >
              {listingID && !createdListingID ? (
                <p>
                  This Resource already has a Listing. Add version-specific
                  metadata revisions there.
                </p>
              ) : (
                <ActionForm
                  title="Create listing"
                  icon="add"
                  editable={permitsNamespace(
                    namespaceAccess,
                    "resource.edit",
                    "catalog.write",
                    item.namespaceId,
                  )}
                  unavailableReason={
                    permitsNamespace(
                      namespaceAccess,
                      "resource.read_history",
                      "catalog.read",
                      item.namespaceId,
                    ) && listingHistoryBusy
                      ? "Checking this Resource's existing Listing before creating one."
                      : contributionReason(namespaceAccess, item.namespaceId)
                  }
                  description="Create a listing in this resource's namespace. A listing revision is needed before review."
                  allowed={
                    (!permitsNamespace(
                      namespaceAccess,
                      "resource.read_history",
                      "catalog.read",
                      item.namespaceId,
                    ) ||
                      !listingHistoryBusy) &&
                    permitsNamespace(
                      namespaceAccess,
                      "resource.edit",
                      "catalog.write",
                      item.namespaceId,
                    )
                  }
                  onFailure={props.onFailure}
                  prepare={() => {
                    const body = {
                      resourceId: item.id,
                      namespaceId: item.namespaceId,
                    };
                    return {
                      summary: [
                        { label: "Resource", value: item.canonicalIdentifier },
                        { label: "Resource ID", value: body.resourceId },
                        { label: "Namespace ID", value: body.namespaceId },
                      ],
                      run: async (key) => {
                        const created = result(
                          await props.client.api.POST(
                            "/api/v1/catalog-listings",
                            {
                              params: { header: { "Idempotency-Key": key } },
                              body,
                            },
                          ),
                          checks.CatalogListing,
                        );
                        setCreatedListingID(created.id);
                        setListingID(created.id);
                        return "Listing created.";
                      },
                    };
                  }}
                />
              )}
              {listingID &&
                permitsNamespace(
                  namespaceAccess,
                  "resource.read",
                  "catalog.read",
                  item.namespaceId,
                ) && (
                  <a
                    className="button primary"
                    href={`${props.runtime.basePath}/listings/${listingID}`}
                  >
                    Open existing listing
                  </a>
                )}
            </section>
          )}
          <details>
            <summary>Discard an unused resource</summary>
            <ActionForm
              title="Delete unused resource"
              intent="danger"
              icon="remove"
              editable={permitsNamespace(
                namespaceAccess,
                "resource.delete_draft",
                "catalog.write",
                item.namespaceId,
              )}
              unavailableReason={
                contributionReason(
                  namespaceAccess,
                  item.namespaceId,
                  "resource.delete_draft",
                ) ??
                "Reload the Resource to obtain its current revision before deleting."
              }
              description="Only an unused draft can be deleted. The Catalog verifies exact creator identity or protected tenant-administrator authority when you confirm; the displayed creator name alone does not establish ownership. Adoption provenance, review history and release history prevent deletion."
              allowed={
                !!detail.data?.etag &&
                permitsNamespace(
                  namespaceAccess,
                  "resource.delete_draft",
                  "catalog.write",
                  item.namespaceId,
                )
              }
              dirty={!!reason}
              onFailure={props.onFailure}
              prepare={() => {
                const body = { reason: reason.trim() };
                const etag = detail.data!.etag!;
                const resourceId = item.id;
                return {
                  summary: [
                    { label: "Resource", value: item.canonicalIdentifier },
                    { label: "Resource ID", value: resourceId },
                    { label: "Namespace ID", value: item.namespaceId },
                    { label: "Revision", value: etag },
                    { label: "Reason", value: body.reason },
                  ],
                  run: async (key) => {
                    const reply = await props.client.api.POST(
                      "/api/v1/resources/{resourceId}:delete",
                      {
                        params: {
                          path: { resourceId },
                          header: { "Idempotency-Key": key, "If-Match": etag },
                        },
                        body,
                      },
                    );
                    acknowledge(reply);
                    return "Resource deleted. Return to authoring.";
                  },
                };
              }}
            >
              <Reason value={reason} onChange={setReason} />
            </ActionForm>
          </details>
        </>
      )}
    </>
  );
}

/** Page the selected resource's versions or listings without hydrating both collections. */
function ResourceHistory(
  props: WorkspaceProps & {
    type: string;
    onListingKnown: (id: string) => void;
    onListingBusy: (busy: boolean) => void;
    historyRef: RefObject<HTMLElement | null>;
    metadataAllowed: boolean;
  },
) {
  const [cursor, setCursor] = useState("");
  const page = useRead(
    props.id + props.type + cursor,
    async (signal) => {
      const params = {
        path: { resourceId: props.id },
        query: { limit: 25, ...(cursor ? { cursor: cursor } : {}) },
      };
      if (props.type === "versions") {
        const data = result(
          await props.client.api.GET(
            "/api/v1/resources/{resourceId}/versions",
            { params, signal },
          ),
          checkedPage(checks.ResourceVersion),
        );
        return {
          nextCursor: data.nextCursor,
          items: data.items.map((i) => ({
            id: i.id,
            label: i.version,
            state: i.sealedRevisionId ? "Sealed" : "Draft",
          })),
        };
      }
      const data = result(
        await props.client.api.GET("/api/v1/resources/{resourceId}/listings", {
          params,
          signal,
        }),
        checkedPage(checks.CatalogListing),
      );
      if (data.items.some((i) => i.resourceId !== props.id))
        throw new Error("Listing history does not match this resource.");
      return {
        nextCursor: data.nextCursor,
        items: data.items.map((i) => ({
          id: i.id,
          label: i.id,
          state: i.currentReleaseId ? "Has release" : "Draft",
        })),
      };
    },
    props.onFailure,
  );
  const { type, onListingKnown, onListingBusy } = props;
  useEffect(() => {
    if (type === "listings") onListingBusy(page.busy);
  }, [type, onListingBusy, page.busy]);
  useEffect(() => {
    if (type === "listings" && page.data?.items[0])
      onListingKnown(page.data.items[0].id);
  }, [type, onListingKnown, page.data]);
  return (
    <section
      className="workspace-panel resource-history"
      id="resource-history"
      ref={props.historyRef}
      tabIndex={-1}
      aria-labelledby="resource-history-title"
    >
      <h2 id="resource-history-title">
        {props.type === "versions" ? "Versions" : "Listings"}
      </h2>
      <p className="muted">
        {props.type === "versions"
          ? "Open a version to inspect its revisions and validation. Contributors can add a new version label."
          : "Open a listing to manage its metadata, review and publication."}
      </p>
      <ReadState {...page} />
      {!!page.data?.items.length && (
        <ul className="workspace-record-list">
          {page.data.items.map((i) => (
            <li key={i.id}>
              {props.metadataAllowed ? (
                <a href={`${props.runtime.basePath}/${props.type}/${i.id}`}>
                  <span>
                    {props.type === "versions"
                      ? `Version ${i.label}`
                      : `Listing ${i.label}`}
                  </span>
                  {controlIcons["chevron"]}
                </a>
              ) : (
                <span>
                  {props.type === "versions"
                    ? `Version ${i.label}`
                    : `Listing ${i.label}`}{" "}
                  / <code>{i.id}</code>
                </span>
              )}
              <span className="badge">{i.state}</span>
            </li>
          ))}
        </ul>
      )}
      {page.data && !page.data.items.length && <p>No {props.type} yet.</p>}
      <Pager
        label={
          props.type === "versions"
            ? "Resource versions pagination"
            : "Resource listings pagination"
        }
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        busy={page.busy}
        onPage={setCursor}
      />
    </section>
  );
}
