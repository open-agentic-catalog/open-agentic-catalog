import { controlIcons } from "../shell/Icon";
// Personal pages keep drafts private and let the API own durable state.
import { useState, useRef, useEffect, useId } from "react";
import { DateValue } from "./date-value";
import { ScrollableTable } from "./ScrollableTable";
import { compareTableText, usePageSort } from "./table-sort";
import { catalogOrders } from "./catalog-order";
import { kindLabel } from "../resource-kinds";
import type { components } from "../api/schema";
import { result } from "../api/client";
import { object } from "../api/values";
import {
  permits,
  permitsNamespace,
  availablePermission,
} from "../api/permissions";
import { page as checkedPage } from "../api/values";
import * as checks from "./responses";
import { ActionForm, CommandReceipt } from "./authoring";
import {
  Heading,
  Field,
  Pager,
  ReadState,
  useRead,
  confirmDiscard,
} from "./workspace";
import type { WorkspaceProps } from "./workspace";
import { atField, characterText } from "./input-validation";
import { JSONBlock } from "./CodeBlock";
import { parseObject } from "./representations";
import { checkedCriteria } from "./personal-criteria";
import { PersonalCriteriaEditor } from "./PersonalCriteriaEditor";
import { personalFacts, PersonalSummary } from "./PersonalSummary";
import { CurrentRead, useCurrentRead } from "./current-read";
import { ExternalHandoff, IndexedProvenance } from "./Handoff";
type Input = components["schemas"]["PersonalInput"];
type SavedItem = components["schemas"]["PersonalItem"];
function SavedRowSummary({ input }: { input: SavedItem }) {
  if (input.reference)
    return (
      <span>
        {input.reference.origin === "local"
          ? "Local release reference"
          : input.reference.identifier}
      </span>
    );
  const criteria = input.search!;
  return (
    <span>
      {criteria.text ? JSON.stringify(criteria.text) : "Any search text"}
      {criteria.kinds?.length
        ? ` · ${criteria.kinds.map(kindLabel).join(", ")}`
        : ""}
      {criteria.order
        ? ` · ${catalogOrders.find((order) => order.value === criteria.order)!.label}`
        : ""}{" "}
      · Open for all saved filters
    </span>
  );
}
const queueNames = {
  drafts: "Draft resources and candidates",
  submitted: "Awaiting review",
  changes_required: "Changes required",
  approved: "Approved and never released",
};

const workNames = {
  resource: "Resource without versions",
  version: "Version candidate",
  candidate: "Listing candidate",
  submission: "Review submission",
};
const workStates = {
  empty_resource: "No versions yet",
  unsealed_version: "Unsealed version",
  unsubmitted_candidate: "Not submitted",
  submitted: "Awaiting review",
  changes_required: "Changes required",
  approved: "Approved",
};

export function MyWork(props: WorkspaceProps) {
  const sortHint = useId();
  const authoringAvailable =
    availablePermission(props.access, "resource.read", "catalog.read") ||
    availablePermission(props.access, "resource.create", "catalog.write");
  const [queue, setQueue] = useState<keyof typeof queueNames>("drafts");
  const [cursor, setCursor] = useState("");
  const read = useRead(
    JSON.stringify([queue, cursor]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/me/work", {
          params: {
            query: { queue, limit: 25, ...(cursor ? { cursor } : {}) },
          },
          signal,
        }),
        checkedPage(checks.PersonalWorkItem),
      ),
    props.onFailure,
    true,
  );
  const refresh = () => (read.error && cursor ? setCursor("") : read.refresh());
  const sorted = usePageSort(read.data?.items, {
    work: (a, b) =>
      compareTableText(
        a.displayName ?? workNames[a.type],
        b.displayName ?? workNames[b.type],
      ),
    resource: (a, b) =>
      compareTableText(
        a.canonicalIdentifier ?? "Identifier unavailable",
        b.canonicalIdentifier ?? "Identifier unavailable",
      ),
    state: (a, b) => compareTableText(workStates[a.state], workStates[b.state]),
  });
  return (
    <>
      <Heading
        title="My work"
        icon="book"
        description="View drafts, submitted reviews, requested changes and approved work with no release."
      />
      <div className="workspace-toolbar">
        <Field label="Work queue">
          <select
            value={queue}
            onChange={(e) => {
              setQueue(e.target.value as keyof typeof queueNames);
              setCursor("");
            }}
          >
            {Object.entries(queueNames).map(([key, label]) => (
              <option key={key} value={key}>
                {label}
              </option>
            ))}
          </select>
        </Field>
        <div className="workspace-actions">
          <button disabled={read.busy} onClick={refresh}>
            {controlIcons["refresh"]}
            Refresh
          </button>
        </div>
      </div>
      {authoringAvailable && (
        <p>
          <a href={`${props.runtime.basePath}/resources`}>Open Authoring</a>
        </p>
      )}
      <details className="workspace-advanced">
        <summary>How these queues work</summary>
        <p>
          These are your currently readable items, not assigned review work.
          Ownership uses your membership and identity realm. Older transitions
          without that attribution are omitted. Submitted revisions cannot be
          edited; approved work still needs current publication checks.
        </p>
      </details>
      {availablePermission(props.access, "review.decide", "catalog.review") && (
        <p>
          <a href={`${props.runtime.basePath}/reviews`}>
            Open the review queue
          </a>
          . Reviewers remain independent; these are not assigned reviews.
        </p>
      )}
      <ReadState {...read} refresh={refresh} />
      {read.data && <p className="eyebrow">Last loaded work page</p>}
      {read.data && (
        <>
          <p id={sortHint} className="muted">
            Select a column heading to sort the results on this page.
          </p>
          <ScrollableTable label="My work results">
            <table className="workspace-table" aria-describedby={sortHint}>
              <thead>
                <tr>
                  {sorted.heading("Work", "work")}
                  {sorted.heading("Resource", "resource")}
                  {sorted.heading("State", "state")}
                  <th scope="col">Continue</th>
                </tr>
              </thead>
              <tbody>
                {sorted.rows.map((item) => {
                  const meta = permitsNamespace(
                    props.access,
                    "resource.read",
                    "catalog.read",
                    item.namespaceId,
                  );
                  const content = permitsNamespace(
                    props.access,
                    "resource.read_content",
                    "catalog.read",
                    item.namespaceId,
                  );
                  const history = permitsNamespace(
                    props.access,
                    "resource.read_history",
                    "catalog.read",
                    item.namespaceId,
                  );
                  return (
                    <tr key={item.id}>
                      <td>{item.displayName ?? workNames[item.type]}</td>
                      <td>
                        {item.canonicalIdentifier ?? "Identifier unavailable"}
                      </td>
                      <td>
                        <span
                          className="workspace-status"
                          data-state={item.state}
                          data-tone={
                            item.state === "changes_required"
                              ? "warning"
                              : item.state === "submitted"
                                ? "info"
                                : "neutral"
                          }
                        >
                          {workStates[item.state]}
                        </span>
                        {item.state === "approved" && (
                          <p>
                            A publisher must recheck current policy and
                            evidence. A newer candidate does not withdraw this
                            approval.
                          </p>
                        )}
                        {item.state === "changes_required" &&
                          item.currentCandidateRevisionId !==
                            item.listingRevisionId && (
                            <p>
                              The current correction target is a newer
                              candidate.
                            </p>
                          )}
                      </td>
                      <td>
                        <div className="workspace-toolbar">
                          {meta && (
                            <a
                              href={`${props.runtime.basePath}/resources/${item.resourceId}`}
                            >
                              Open resource
                            </a>
                          )}
                          {meta && item.listingId && (
                            <a
                              href={`${props.runtime.basePath}/listings/${item.listingId}`}
                            >
                              Open current listing
                            </a>
                          )}
                          {history && item.submissionId && (
                            <a
                              href={`${props.runtime.basePath}/reviews/${item.submissionId}`}
                            >
                              Open submitted review
                            </a>
                          )}
                          {content && item.versionRevisionId && (
                            <a
                              href={`${props.runtime.basePath}/version-revisions/${item.versionRevisionId}`}
                            >
                              Inspect version content
                            </a>
                          )}
                          {content && item.currentCandidateRevisionId && (
                            <a
                              href={`${props.runtime.basePath}/listing-revisions/${item.currentCandidateRevisionId}`}
                            >
                              Inspect current candidate
                            </a>
                          )}
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
            {!read.data.items.length && (
              <p className="workspace-empty">
                {queue === "drafts"
                  ? authoringAvailable
                    ? "No drafts to continue. Start in Authoring."
                    : "No drafts to continue."
                  : queue === "submitted"
                    ? "No submitted work is awaiting review."
                    : queue === "changes_required"
                      ? "No reviewed work needs changes."
                      : "No approved work without a release."}
              </p>
            )}
          </ScrollableTable>
        </>
      )}
      <Pager
        cursor={cursor}
        nextCursor={read.data?.nextCursor}
        busy={read.busy}
        onPage={setCursor}
      />
    </>
  );
}

type LibraryProps = WorkspaceProps & {
  onOpenSearch?: (criteria: components["schemas"]["PersonalCriteria"]) => void;
};
export function PersonalLibrary(props: LibraryProps) {
  return (
    <>
      <Heading
        title="Saved items and searches"
        icon="bookmark"
        description="Your private server-stored references and named Catalog criteria. Saving does not grant access, approval, trust or execution."
      />
      {props.id ? <LibraryDetail {...props} /> : <LibraryList {...props} />}
    </>
  );
}
function LibraryList(props: WorkspaceProps) {
  const sortHint = useId();
  const [kind, setKind] = useState<"" | "resource" | "search">("");
  const [label, setLabel] = useState("");
  const [draftKind, setDraftKind] = useState<typeof kind>("");
  const [draftLabel, setDraftLabel] = useState("");
  const [filterError, setFilterError] = useState("");
  const [cursor, setCursor] = useState("");
  const read = useCurrentRead(
    JSON.stringify([kind, label, cursor]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/personal/items", {
          params: {
            query: {
              ...(kind ? { type: kind } : {}),
              ...(label ? { label } : {}),
              ...(cursor ? { cursor } : {}),
              limit: 25,
            },
          },
          signal,
        }),
        checkedPage(checks.PersonalItem),
      ),
    props.onFailure,
  );
  const sorted = usePageSort(read.data?.items, {
    label: (a, b) => compareTableText(a.label, b.label),
    type: (a, b) =>
      compareTableText(
        a.type === "search" ? "Named search" : "Resource reference",
        b.type === "search" ? "Named search" : "Resource reference",
      ),
    updated: (a, b) => Date.parse(a.updatedAt) - Date.parse(b.updatedAt),
  });
  const refresh = () => (read.error && cursor ? setCursor("") : read.refresh());
  return (
    <>
      <p>
        You can keep up to 200 items. Labels may contain 1 to 120 characters.
        Manage an item even after access to its target is lost.
      </p>
      <form
        className="workspace-toolbar"
        aria-label="Saved item filters"
        onSubmit={(event) => {
          event.preventDefault();
          if (
            draftLabel &&
            !checks.PersonalInput({
              type: "search",
              label: draftLabel,
              search: {},
            })
          ) {
            setFilterError(
              "An exact label needs 1 to 120 characters without controls and cannot be whitespace only.",
            );
            document.getElementById("library-label")!.focus();
            return;
          }
          setFilterError("");
          setKind(draftKind);
          setLabel(draftLabel);
          setCursor("");
        }}
      >
        <Field label="Saved item type">
          <select
            value={draftKind}
            onChange={(e) => {
              setDraftKind(e.target.value as typeof kind);
            }}
          >
            <option value="">All types</option>
            <option value="resource">Resources</option>
            <option value="search">Searches</option>
          </select>
        </Field>
        <Field
          label="Exact saved label"
          hint="Match the whole label, including capitals, spaces and spelling. Leave blank to include all labels."
        >
          <input
            id="library-label"
            value={draftLabel}
            onChange={(event) => setDraftLabel(event.target.value)}
          />
        </Field>
        <div className="workspace-actions">
          <button type="submit" disabled={read.busy}>
            {controlIcons["filters"]}
            Apply saved item filters
          </button>
          <button
            type="button"
            disabled={read.busy}
            onClick={() => {
              setKind("");
              setLabel("");
              setDraftKind("");
              setDraftLabel("");
              setCursor("");
              setFilterError("");
            }}
          >
            {controlIcons["close"]}
            Clear saved item filters
          </button>
          {permits(props.access, "library.own", "personal.write") && (
            <a href={`${props.runtime.basePath}/saved-items/new`}>
              {controlIcons["search"]}
              Save a named search
            </a>
          )}
        </div>
      </form>
      {filterError && (
        <p className="notice error" role="alert">
          {filterError}
        </p>
      )}
      <p className="muted">
        Applied: {kind || "all saved types"};{" "}
        {label ? (
          <>
            exact label{" "}
            <span className="personal-value">{JSON.stringify(label)}</span>
          </>
        ) : (
          "any label"
        )}
        . Labels need not be unique. Rows describe saved metadata and do not
        check current target access.
      </p>
      {(draftKind !== kind || draftLabel !== label) && (
        <p role="status">
          Filter edits are pending. Apply saved item filters to use them.
        </p>
      )}
      <CurrentRead
        read={read}
        label="saved library page"
        reload="Refresh saved items"
        refresh={refresh}
      />
      {read.data && (
        <>
          <p id={sortHint} className="muted">
            Select a column heading to sort the results on this page.
          </p>
          <ScrollableTable label="Saved library results">
            <table className="workspace-table" aria-describedby={sortHint}>
              <thead>
                <tr>
                  {sorted.heading("Label", "label")}
                  {sorted.heading("Type", "type")}
                  <th scope="col">Saved summary</th>
                  {sorted.heading("Last updated", "updated")}
                </tr>
              </thead>
              <tbody>
                {sorted.rows.map((item) => (
                  <tr key={item.id}>
                    <td>
                      <a
                        href={`${props.runtime.basePath}/saved-items/${item.id}`}
                      >
                        {item.label}
                      </a>
                    </td>
                    <td>
                      {
                        controlIcons[
                          item.type === "search" ? "search" : "bookmark"
                        ]
                      }{" "}
                      {item.type === "search"
                        ? "Named search"
                        : "Resource reference"}
                    </td>
                    <td>
                      <SavedRowSummary input={item} />
                    </td>
                    <td>
                      <DateValue value={item.updatedAt} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!read.data.items.length && (
              <p className="workspace-empty">
                {kind || label
                  ? "No saved items match the applied filters."
                  : "Your library is empty. Save a resource from Catalog or create a named search."}
              </p>
            )}
          </ScrollableTable>
        </>
      )}
      <Pager
        cursor={cursor}
        nextCursor={read.data?.nextCursor}
        busy={read.busy}
        onPage={setCursor}
      />
    </>
  );
}
function LibraryDetail(props: LibraryProps) {
  if (props.id === "new")
    return (
      <SavePersonal
        basePath={props.runtime.basePath}
        {...props}
        initial={{ type: "search", label: "", search: { text: "" } }}
      />
    );
  return <ExistingItem key={props.id} {...props} />;
}
function referenceKey(v: components["schemas"]["PersonalReference"]) {
  return JSON.stringify([
    v.origin,
    v.releaseId,
    v.versionRevisionId,
    v.kind,
    v.sourceId,
    v.presentationGeneration,
    v.identifier,
    v.versionPresent,
    v.version ?? "",
  ]);
}
function ExistingItem(props: LibraryProps) {
  const readable =
    props.access.actorType === "human" &&
    permits(props.access, "library.own", "catalog.read");
  const policyKey = JSON.stringify(props.access);
  const authority = useRef({ policyKey, epoch: 0, readable });
  authority.current = {
    policyKey,
    epoch:
      authority.current.epoch +
      Number(authority.current.policyKey !== policyKey),
    readable,
  };
  const readRequest = useRef<AbortController | undefined>(undefined);
  const requestedEpoch = useRef(0);
  const read = useCurrentRead(
    props.id,
    async (signal) => {
      if (!authority.current.readable) return null;
      const epoch = authority.current.epoch;
      requestedEpoch.current = epoch;
      const controller = new AbortController();
      readRequest.current = controller;
      const reply = await props.client.api
        .GET("/api/v1/personal/items/{itemId}", {
          params: { path: { itemId: props.id } },
          signal: AbortSignal.any([signal, controller.signal]),
        })
        .catch((error: unknown) => {
          if (
            controller.signal.aborted ||
            authority.current.epoch !== epoch ||
            !authority.current.readable
          )
            return null;
          throw error;
        });
      if (
        !reply ||
        authority.current.epoch !== epoch ||
        !authority.current.readable
      )
        return null;
      return {
        policyEpoch: epoch,
        item: result(
          reply,
          (value) =>
            checks.PersonalItem(value) &&
            object(value) &&
            value.id === props.id,
        ),
        etag: reply.response.headers.get("ETag"),
      };
    },
    (error) => {
      if (
        requestedEpoch.current === authority.current.epoch &&
        authority.current.readable
      )
        props.onFailure(error);
    },
  );
  const [base, setBase] = useState<NonNullable<typeof read.data>>();
  if (
    read.data &&
    readable &&
    read.data.policyEpoch === authority.current.epoch &&
    base !== read.data
  )
    setBase(read.data);
  const visible = readable && base?.policyEpoch === authority.current.epoch;
  const ready = visible && !read.busy && !read.error && !read.stale;
  const resolveRequest = useRef<AbortController | undefined>(undefined);
  useEffect(() => () => resolveRequest.current?.abort(), []);
  const [removed, setRemoved] = useState(false);
  const [resolution, setResolution] =
    useState<components["schemas"]["PersonalResolution"]>();
  const [resolveError, setResolveError] = useState("");
  const [resolving, setResolving] = useState(false);
  const readPolicy = useRef(policyKey);
  useEffect(() => {
    if (readPolicy.current !== policyKey) {
      readPolicy.current = policyKey;
      readRequest.current?.abort();
    }
    resolveRequest.current?.abort();
    setResolution(undefined);
    setResolveError("");
    setResolving(false);
  }, [policyKey]);
  return (
    <>
      <p>
        <a href={`${props.runtime.basePath}/saved-items`}>
          Back to saved items
        </a>
      </p>
      {!readable && (
        <p role="alert">
          Reading saved items requires library.own and catalog.read when scopes
          are enforced. Your entered draft and prepared request remain in this
          tab; new reads and saves are unavailable.
        </p>
      )}
      {readable && !visible && base && (
        <p className="notice">
          Access changed. Read the current saved item explicitly before
          preparing another update or checking its target.
        </p>
      )}
      {!readable && read.message && (
        <CommandReceipt key={read.acknowledgements} message={read.message} />
      )}
      {!removed && readable && (
        <CurrentRead
          read={{ ...read, data: visible ? read.data : undefined }}
          label="saved item"
          reload="Read current saved item"
          refresh={() => {
            if (authority.current.readable && confirmDiscard()) {
              resolveRequest.current?.abort();
              setResolving(false);
              setResolution(undefined);
              setResolveError("");
              read.refresh();
            }
          }}
        />
      )}
      {removed ? (
        <p role="status">Saved item removed.</p>
      ) : (
        base && (
          <>
            {visible && (
              <>
                <h2>{base.item.label}</h2>
                <details>
                  <summary>Last loaded saved criteria or reference</summary>
                  <PersonalSummary input={base.item} />
                </details>
                <p className="muted">
                  Last updated <DateValue value={base.item.updatedAt} />. These
                  saved facts do not establish current target availability.
                </p>
              </>
            )}
            <SavePersonal
              basePath={props.runtime.basePath}
              key={`${base.item.id}:${base.item.revision}`}
              {...props}
              initial={base.item}
              existing={base}
              ready={ready}
              saved={() => {
                resolveRequest.current?.abort();
                setResolving(false);
                read.acknowledge("Private item saved.");
                setResolution(undefined);
                if (authority.current.readable) read.refresh();
              }}
            />
            <ActionForm
              title="Remove saved item"
              intent="danger"
              icon="remove"
              description="Remove this private reference or search. Its target is not changed."
              editable={
                readable &&
                permits(props.access, "library.own", "personal.write")
              }
              allowed={
                ready &&
                permits(props.access, "library.own", "personal.write") &&
                !!base.etag
              }
              onFailure={props.onFailure}
              prepare={() => {
                const expected = base!;
                if (!expected.etag)
                  throw new Error("Reload the saved item before removing it.");
                return {
                  summary: [
                    { label: "Saved item ID", value: expected.item.id },
                    { label: "Revision", value: expected.etag },
                    ...personalFacts(expected.item),
                  ],
                  run: async (key: string) => {
                    const receipt = result(
                      await props.client.api.DELETE(
                        "/api/v1/personal/items/{itemId}",
                        {
                          params: {
                            path: { itemId: props.id },
                            header: {
                              "If-Match": expected.etag!,
                              "Idempotency-Key": key,
                            },
                          },
                        },
                      ),
                      checks.PersonalReceipt,
                    );
                    if (
                      receipt.id !== expected.item.id ||
                      receipt.revision !== expected.item.revision + 1
                    )
                      throw new Error(
                        "The removal acknowledgement could not be verified.",
                      );
                    resolveRequest.current?.abort();
                    setResolving(false);
                    setRemoved(true);
                    return "Saved item removed.";
                  },
                };
              }}
            />
            {visible && base.item.reference && (
              <section>
                <h3>Exact saved reference</h3>
                <PersonalSummary input={base.item} referenceOnly />
                <JSONBlock value={base.item.reference} />
                <p>
                  Check this exact release or source generation against your
                  current access. This does not make live requests or substitute
                  another source.
                </p>
                <button
                  disabled={resolving || !ready}
                  onClick={() => {
                    if (!ready || !authority.current.readable) return;
                    resolveRequest.current?.abort();
                    const controller = new AbortController();
                    resolveRequest.current = controller;
                    const expectedEpoch = authority.current.epoch;
                    const expectedReference = base!.item.reference!;
                    setResolution(undefined);
                    setResolveError("");
                    setResolving(true);
                    void props.client.api
                      .POST("/api/v1/personal/items/{itemId}/resolve", {
                        params: { path: { itemId: props.id } },
                        body: {},
                        signal: controller.signal,
                      })
                      .then((reply) => {
                        if (
                          controller.signal.aborted ||
                          authority.current.epoch !== expectedEpoch ||
                          !authority.current.readable
                        )
                          return;
                        setResolution(
                          result(
                            reply,
                            (value) =>
                              checks.PersonalResolution(value) &&
                              object(value) &&
                              (value.status === "unavailable" ||
                                referenceKey(
                                  value.reference as components["schemas"]["PersonalReference"],
                                ) === referenceKey(expectedReference)),
                          ),
                        );
                      })
                      .catch((error) => {
                        if (
                          controller.signal.aborted ||
                          authority.current.epoch !== expectedEpoch ||
                          !authority.current.readable
                        )
                          return;
                        props.onFailure(error);
                        setResolveError(
                          "The saved reference could not be checked. Retry explicitly.",
                        );
                      })
                      .finally(() => {
                        if (!controller.signal.aborted) setResolving(false);
                      });
                  }}
                >
                  {controlIcons["check"]}
                  Check saved reference
                </button>
                {resolveError && <p role="alert">{resolveError}</p>}
                {resolution?.status === "unavailable" && (
                  <p role="status">
                    This exact saved target is unavailable with current access
                    or source generation. Your reference is unchanged.
                  </p>
                )}
                {resolution?.status === "available" &&
                  resolution.reference?.origin === "local" && (
                    <p>
                      <a
                        href={`${props.runtime.basePath}/catalog/${resolution.reference.releaseId}`}
                      >
                        {controlIcons["arrow"]}
                        Open exact saved release
                      </a>
                    </p>
                  )}
                {resolution?.status === "available" && resolution.indexed && (
                  <section>
                    <h4>Current exact source metadata</h4>
                    <p>{resolution.indexed.displayName}</p>
                    <p>
                      {resolution.indexed.description ||
                        "No description supplied."}
                    </p>
                    <IndexedProvenance
                      item={resolution.indexed}
                      access={props.access}
                      basePath={props.runtime.basePath}
                    />
                    <ExternalHandoff
                      item={resolution.indexed}
                      documentationUrl={props.runtime.documentationUrl}
                    />
                    <JSONBlock value={resolution.indexed} />
                  </section>
                )}
              </section>
            )}
            {visible && base.item.search && (
              <section>
                <h3>Saved search</h3>
                <p>
                  Opening stages these private criteria. Choose Search
                  explicitly after reviewing current query disclosure and
                  provider consent.
                </p>
                <JSONBlock value={base.item.search} />
                <button
                  disabled={!ready}
                  onClick={() => {
                    if (ready && authority.current.readable && confirmDiscard())
                      props.onOpenSearch?.(base!.item.search!);
                  }}
                >
                  {controlIcons["search"]}
                  Open search editor
                </button>
              </section>
            )}
          </>
        )
      )}
    </>
  );
}
export function SavePersonal(
  props: Pick<WorkspaceProps, "access" | "client" | "onFailure"> & {
    basePath: string;
  } & {
    initial: Input;
    existing?: { item: SavedItem; etag: string | null };
    saved?: () => void;
    ready?: boolean;
    quick?: boolean;
  },
) {
  const prefix = useId();
  const [label, setLabel] = useState(props.initial.label);
  const [payload, setPayload] = useState(
    JSON.stringify(props.initial.search ?? props.initial.reference, null, 2),
  );
  const [created, setCreated] = useState("");
  const readable =
    props.access.actorType === "human" &&
    permits(props.access, "library.own", "catalog.read");
  const editable =
    props.access.actorType === "human" &&
    permits(props.access, "library.own", "personal.write") &&
    (!props.existing || readable);
  return (
    <>
      <ActionForm
        icon="save"
        title={
          props.existing
            ? "Update saved item"
            : props.initial.type === "search"
              ? "Save named search"
              : "Save resource"
        }
        description="Only this label and exact reference or validated criteria are retained. No record documents, credentials or transient cursors are saved."
        editable={editable}
        allowed={
          editable &&
          props.ready !== false &&
          (!props.existing || !!props.existing.etag)
        }
        unavailableReason={
          props.existing
            ? "Updating requires library.own, personal.write when scopes are enforced, and a successfully loaded current item validator. Read current saved item before preparing a new update. Reading saved items also requires catalog.read when scopes are enforced."
            : "Saving requires a human account with library.own and personal.write when scopes are enforced."
        }
        onFailure={props.onFailure}
        dirty={
          label !== props.initial.label ||
          payload !==
            JSON.stringify(
              props.initial.search ?? props.initial.reference,
              null,
              2,
            )
        }
        {...(props.saved ? { onAcknowledged: props.saved } : {})}
        prepare={() => {
          const input = {
            type: props.initial.type,
            label: atField(`${prefix}-label`, () => {
              characterText(label, "Saved label", 120, 1);
              if (!checks.PersonalInput({ type: "search", label, search: {} }))
                throw new Error(
                  "Saved label needs 1 to 120 characters without controls and cannot be whitespace only.",
                );
              return label;
            }),
            ...(props.initial.type === "search"
              ? {
                  search: atField(prefix, () =>
                    checkedCriteria(parseObject(payload), prefix),
                  ),
                }
              : {
                  reference: atField(`${prefix}-reference`, () =>
                    parseObject(payload),
                  ) as components["schemas"]["PersonalReference"],
                }),
          };
          if (!checks.PersonalInput(input))
            throw new Error(
              "Enter valid supported criteria or an exact resource reference.",
            );
          if (
            new TextEncoder().encode(JSON.stringify(input)).length >
            16 * 1024
          )
            throw new Error(
              "The saved label and criteria/reference must fit the 16 KiB request budget.",
            );
          const old = props.existing;
          if (old && !old.etag)
            throw new Error("Reload the saved item before updating it.");
          return {
            summary: [
              ...(old
                ? [
                    { label: "Saved item ID", value: old.item.id },
                    { label: "Revision", value: old.etag! },
                  ]
                : []),
              ...personalFacts(input),
            ],
            run: async (key: string) => {
              const reply = old
                ? await props.client.api.PATCH(
                    "/api/v1/personal/items/{itemId}",
                    {
                      params: {
                        path: { itemId: old.item.id },
                        header: {
                          "Idempotency-Key": key,
                          "If-Match": old.etag!,
                        },
                      },
                      body: input,
                    },
                  )
                : await props.client.api.POST("/api/v1/personal/items", {
                    params: { header: { "Idempotency-Key": key } },
                    body: input,
                  });
              const receipt = result(reply, checks.PersonalReceipt);
              if (
                (!old && receipt.revision !== 1) ||
                (old &&
                  (receipt.id !== old.item.id ||
                    receipt.revision !== old.item.revision + 1))
              )
                throw new Error(
                  "The save acknowledgement could not be verified.",
                );
              setCreated(receipt.id);
              return "Private item saved.";
            },
          };
        }}
      >
        <Field label="Saved label">
          <input
            id={`${prefix}-label`}
            required
            value={label}
            onChange={(e) => setLabel(e.target.value)}
          />
        </Field>
        {props.initial.type === "search" ? (
          <PersonalCriteriaEditor
            id={prefix}
            value={payload}
            onChange={setPayload}
          />
        ) : props.quick ? (
          <>
            <h3>Captured resource reference</h3>
            <PersonalSummary
              input={{
                type: "resource",
                label,
                reference: JSON.parse(
                  payload,
                ) as components["schemas"]["PersonalReference"],
              }}
              referenceOnly
            />
            <p className="muted">
              Quick save retains this inspected reference. You can deliberately
              edit a reference later in its saved-item detail.
            </p>
          </>
        ) : (
          <details>
            <summary>Edit exact resource reference</summary>
            <Field
              label="Exact resource reference JSON"
              hint="Advanced replacement requires current admission of that exact target. Renaming an unchanged reference does not resolve it."
            >
              <textarea
                id={`${prefix}-reference`}
                rows={7}
                value={payload}
                onChange={(e) => setPayload(e.target.value)}
              />
            </Field>
          </details>
        )}
      </ActionForm>
      {created && readable && !props.existing && (
        <p>
          <a href={`${props.basePath}/saved-items/${created}`}>
            {controlIcons["arrow"]}
            Open saved item
          </a>
        </p>
      )}
    </>
  );
}

/** Offer one explicit private save at the exact inspected result boundary. */
export function SaveResource(
  props: Pick<WorkspaceProps, "access" | "client" | "onFailure"> & {
    basePath: string;
    label: string;
    item?: components["schemas"]["CatalogSearchResult"];
    reference?: components["schemas"]["PersonalReference"];
  },
) {
  const [open, setOpen] = useState(false);
  const item = props.item;
  let reference = props.reference;
  if (!reference && item?.local?.release?.id && item.local.versionRevision?.id)
    reference = {
      origin: "local",
      releaseId: item.local.release.id,
      versionRevisionId: item.local.versionRevision.id,
    };
  if (!reference && item) {
    const source = item.indexed?.presentationSourceId ?? item.live?.sourceId;
    const generation =
      item.indexed?.presentationGeneration ?? item.live?.presentationGeneration;
    if (source && generation)
      reference = {
        origin: "foreign",
        kind: item.kind,
        sourceId: source,
        presentationGeneration: generation,
        identifier: item.identifier,
        versionPresent: item.versionPresent,
        ...(item.versionPresent ? { version: item.version ?? "" } : {}),
      };
  }
  if (
    props.access.actorType !== "human" ||
    !permits(props.access, "library.own", "personal.write") ||
    !reference
  )
    return null;
  return (
    <details onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary
        onClick={(e) => {
          if (open && !confirmDiscard()) e.preventDefault();
        }}
      >
        {controlIcons["bookmark"]}
        Save resource
      </summary>
      {open && (
        <SavePersonal
          {...props}
          quick
          initial={{
            type: "resource",
            label: [...props.label].slice(0, 120).join(""),
            reference,
          }}
        />
      )}
    </details>
  );
}

/** Capture the submitted criteria only when opened; later searches cannot replace a private save draft. */
export function SaveSearch(
  props: Pick<WorkspaceProps, "access" | "client" | "onFailure"> & {
    basePath: string;
    criteria?: components["schemas"]["CatalogSearch"] | undefined;
  },
) {
  const [open, setOpen] = useState(false);
  const [captured, setCaptured] =
    useState<components["schemas"]["PersonalCriteria"]>();
  if (!props.criteria && !open) return null;
  return (
    <details open={open}>
      <summary
        onClick={(e) => {
          e.preventDefault();
          if (open) {
            if (!confirmDiscard()) return;
            setOpen(false);
            setCaptured(undefined);
          } else {
            const {
              limit: _limit,
              cursor: _cursor,
              ...criteria
            } = props.criteria!;
            setCaptured({ ...criteria, text: criteria.text ?? "" });
            setOpen(true);
          }
        }}
      >
        {controlIcons["search"]}
        Save this named search
      </summary>
      {open && captured && (
        <SavePersonal
          {...props}
          initial={{ type: "search", label: "", search: captured }}
        />
      )}
    </details>
  );
}
