import { Facts } from "./Facts";
import { ReleaseState } from "./ReleaseState";
import { ScrollableTable } from "./ScrollableTable";
import { RevisionComparison } from "./RevisionComparison";
import { ValidationFindings, ReviewDecisions } from "./ValidationFindings";
import { ReleaseReplacement } from "./ReleaseReplacement";
import * as checks from "./responses";
import { object, page as checkedPage } from "../api/values";
// Review decisions use retained submitted evidence; release commands manage subsequent lifecycle state.
import { useEffect, useId, useRef, useState } from "react";
import { compareTableText, usePageSort } from "./table-sort";
import {
  reviewLabels,
  reviewFilters,
  reviewQueueURL,
} from "./review-queue-state";
import type { ReviewFilters } from "./review-queue-state";
import { publicationReceiptMatches } from "./review-publication";
import { ListingHistory } from "./Listings";
import {
  useNamespaceAccess,
  useNamespaceAccessState,
} from "./namespace-access";
import type { components } from "../api/schema";
import { result } from "../api/client";
import {
  permits,
  permitsNamespace,
  releaseTransitionAllowed,
  contributionReason,
} from "../api/permissions";
import { ActionForm, CommandReceipt, EvidenceDetails } from "./authoring";
import type { WorkspaceProps } from "./workspace";
import {
  Field,
  Heading,
  Pager,
  ReadState,
  Reason,
  useRead,
  confirmDiscard,
} from "./workspace";
import { ListingMetadata, submittedListingMatches } from "./listing-metadata";
import { stateLabel } from "./operational-labels";

const reviewTones = {
  submitted: "info",
  approved: "success",
  changes_required: "warning",
  rejected: "danger",
};

const reviewGuidance = {
  submitted:
    "Inspect the exact submitted metadata and evidence. An independent authorized reviewer must decide this submission before a publisher can release it.",
  approved:
    "This submission is approved. An authorized publisher can create a release from this exact evidence; publication still checks current policy and validation.",
  changes_required:
    "Changes are required. Return to the listing, create a corrected candidate revision and submit it for a new independent review. This submission remains retained.",
  rejected:
    "This submission was rejected. Inspect its decision history. Any revised proposal needs a new listing revision and submission; this decision is retained.",
};

/** Route review browsing to the queue or an exact retained submission. */
export default function Reviews(props: WorkspaceProps) {
  return (
    <>
      <Heading
        title="Reviews"
        icon="access"
        description="Review exact submitted metadata and evidence before deciding. Submitters cannot review their own work."
      />
      {props.id ? <ReviewDetail {...props} /> : <ReviewQueue {...props} />}
    </>
  );
}

/** Page submissions by explicit state and listing filters. */
function ReviewQueue(props: WorkspaceProps) {
  const sortHint = useId();
  const [filters, setFilters] = useState(() => reviewFilters(location.search));
  const [draft, setDraft] = useState(filters);
  const [submissionId, setSubmissionId] = useState("");
  const [cursor, setCursor] = useState("");
  useEffect(() => {
    const moved = () => {
      if (location.pathname !== `${props.runtime.basePath}/reviews`) return;
      const restored = reviewFilters(location.search);
      setFilters(restored);
      setDraft(restored);
      setCursor("");
    };
    window.addEventListener("popstate", moved);
    return () => window.removeEventListener("popstate", moved);
  }, [props.runtime.basePath]);
  const page = useRead(
    JSON.stringify([filters, cursor]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/review-submissions", {
          params: {
            query: {
              ...(filters.listingId ? { listingId: filters.listingId } : {}),
              ...(filters.state
                ? { state: filters.state as keyof typeof reviewLabels }
                : {}),
              ...(filters.unreleased !== undefined
                ? { unreleased: filters.unreleased as unknown as boolean }
                : {}),
              limit: 25,
              ...(cursor ? { cursor: cursor } : {}),
            },
          },
          signal,
        }),
        checkedPage(checks.ReviewSubmission),
      ),
    props.onFailure,
  );
  const reset = () => {
    setCursor("");
    page.refresh();
  };
  const sorted = usePageSort(page.data?.items, {
    listing: (a, b) =>
      compareTableText(
        a.submittedDisplayName ?? "Name unavailable",
        b.submittedDisplayName ?? "Name unavailable",
      ),
    resource: (a, b) =>
      compareTableText(
        a.canonicalIdentifier || "Identifier unavailable",
        b.canonicalIdentifier || "Identifier unavailable",
      ),
    state: (a, b) =>
      compareTableText(reviewLabels[a.state], reviewLabels[b.state]),
    submitter: (a, b) => compareTableText(a.submittedBy, b.submittedBy),
    submitted: (a, b) => Date.parse(a.submittedAt) - Date.parse(b.submittedAt),
  });
  const apply = (selected: ReviewFilters) => {
    const next = { ...selected, listingId: selected.listingId.trim() };
    const url = reviewQueueURL(props.runtime.basePath, next);
    if (location.pathname + location.search !== url)
      history.pushState(null, "", url);
    setFilters(next);
    setDraft(next);
    reset();
  };
  return (
    <>
      <form
        className="workspace-toolbar"
        aria-label="Review queue filters"
        onSubmit={(event) => {
          event.preventDefault();
          apply(draft);
        }}
      >
        <Field
          label="Exact Listing ID"
          hint="Filter the authorized queue by one Listing UUID across all pages. Leave empty for all Listings."
        >
          <input
            maxLength={36}
            pattern="[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
            value={draft.listingId}
            onChange={(event) =>
              setDraft({ ...draft, listingId: event.target.value })
            }
          />
        </Field>
        <Field label="Review state">
          <select
            value={draft.state}
            onChange={(e) => {
              const next = { ...draft, state: e.target.value };
              if (next.state !== "approved") delete next.unreleased;
              setDraft(next);
            }}
          >
            {draft.state && !Object.hasOwn(reviewLabels, draft.state) && (
              <option value={draft.state} disabled>
                Unsupported review state
              </option>
            )}
            <option value="">All states</option>
            {["submitted", "approved", "changes_required", "rejected"].map(
              (v) => (
                <option key={v} value={v}>
                  {stateLabel(v)}
                </option>
              ),
            )}
          </select>
        </Field>
        <Field label="Approved submissions without a release">
          <input
            type="checkbox"
            checked={draft.unreleased === "true"}
            onChange={(event) => {
              if (event.target.checked)
                setDraft({ ...draft, state: "approved", unreleased: "true" });
              else {
                const next = { ...draft };
                delete next.unreleased;
                setDraft(next);
              }
            }}
          />
        </Field>
        <div className="workspace-actions">
          <button disabled={page.busy}>Apply review filters</button>
          <button
            type="button"
            disabled={page.busy}
            onClick={() => apply({ listingId: "", state: "" })}
          >
            Clear review filters
          </button>
          <button
            type="button"
            disabled={page.busy}
            onClick={page.error ? reset : page.refresh}
          >
            Refresh
          </button>
          {filters.listingId &&
            permits(props.access, "resource.read", "catalog.read") && (
              <a
                href={`${props.runtime.basePath}/listings/${encodeURIComponent(filters.listingId)}`}
              >
                Back to listing
              </a>
            )}
        </div>
      </form>
      <p className="muted">
        Applied Listing: <code>{filters.listingId || "All Listings"}</code>.
        State:{" "}
        {Object.hasOwn(reviewLabels, filters.state)
          ? stateLabel(filters.state)
          : filters.state
            ? "Unsupported review state"
            : "All states"}
        {filters.unreleased === "true"
          ? ". Without a release"
          : filters.unreleased !== undefined && filters.unreleased !== "false"
            ? ". Unsupported release filter"
            : ""}
        . Editing these fields does not change results until Apply.
      </p>
      <form
        className="workspace-toolbar workspace-lookup"
        aria-label="Open exact review submission"
        action={`${props.runtime.basePath}/reviews/${encodeURIComponent(submissionId.trim())}`}
        method="get"
      >
        <Field
          label="Exact Submission ID"
          hint="Open a known submission by its UUID. Access is checked when you open it."
        >
          <input
            required
            maxLength={36}
            pattern="[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
            value={submissionId}
            onChange={(event) => setSubmissionId(event.target.value)}
          />
        </Field>
        <div className="workspace-actions">
          <button disabled={!submissionId.trim()}>Open submission</button>
        </div>
      </form>
      <ReadState {...page} refresh={reset} />
      {page.data && (
        <>
          <p id={sortHint} className="muted">
            Select a column heading to sort the results on this page.
          </p>
          <ScrollableTable label="Review submissions">
            <table className="workspace-table" aria-describedby={sortHint}>
              <thead>
                <tr>
                  <th scope="col">Submission</th>
                  {sorted.heading("Submitted listing", "listing")}
                  {sorted.heading("Resource identifier", "resource")}
                  {sorted.heading("State", "state")}
                  {sorted.heading("Submitted by", "submitter")}
                  {sorted.heading("Submitted", "submitted")}
                </tr>
              </thead>
              <tbody>
                {sorted.rows.map((i) => (
                  <tr key={i.id}>
                    <td>
                      <a href={`${props.runtime.basePath}/reviews/${i.id}`}>
                        {i.id}
                      </a>
                    </td>
                    <td>
                      {i.submittedDisplayName ?? (
                        <span className="muted">Name unavailable</span>
                      )}
                    </td>
                    <td>
                      {i.canonicalIdentifier ? (
                        <code>{i.canonicalIdentifier}</code>
                      ) : (
                        <span className="muted">Identifier unavailable</span>
                      )}
                    </td>
                    <td>
                      <span
                        className="state-badge"
                        data-state={i.state}
                        data-tone={reviewTones[i.state]}
                      >
                        {reviewLabels[i.state]}
                      </span>
                    </td>
                    <td>{i.submittedBy}</td>
                    <td>{new Date(i.submittedAt).toLocaleString()}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!page.data.items.length && (
              <p className="workspace-empty">No matching review submissions.</p>
            )}
          </ScrollableTable>
        </>
      )}
      <Pager
        label="Review submissions pagination"
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        busy={page.busy}
        onPage={setCursor}
      />
    </>
  );
}

/** Resolve a submission and its listing context before review or publication actions. */
function ReviewDetail(props: WorkspaceProps) {
  const readEpoch = useRef(0);
  const detail = useRead(
    props.id,
    async (signal) => {
      const epoch = ++readEpoch.current;
      const submissionId = props.id.toLowerCase();
      const reply = await props.client.api.GET(
        "/api/v1/review-submissions/{submissionId}",
        { params: { path: { submissionId } }, signal },
      );
      const submission = result(reply, checks.ReviewSubmission);
      if (
        submission.id !== submissionId ||
        submission.tenantId !== props.access.tenantId
      )
        throw new Error("Review submission does not match the request.");
      if (
        submission.namespaceId &&
        !permitsNamespace(
          namespace.access,
          "resource.read",
          "catalog.read",
          submission.namespaceId,
        )
      ) {
        return {
          submission,
          readEpoch: epoch,
          etag: reply.response.headers.get("ETag"),
          listing: {
            id: submission.listingId,
            namespaceId: submission.namespaceId,
          },
          listingInspected: false,
          listingETag: submission.listingConcurrencyRevision
            ? `"rev-${submission.listingConcurrencyRevision}"`
            : null,
        };
      }
      const listingReply = await props.client.api.GET(
        "/api/v1/catalog-listings/{listingId}",
        { params: { path: { listingId: submission.listingId } }, signal },
      );
      const listing = result(listingReply, checks.CatalogListing);
      if (
        listing.id !== submission.listingId ||
        listing.tenantId !== props.access.tenantId ||
        (submission.namespaceId &&
          listing.namespaceId !== submission.namespaceId)
      )
        throw new Error("Listing context does not match the submission.");
      return {
        submission,
        readEpoch: epoch,
        etag: reply.response.headers.get("ETag"),
        listing,
        listingInspected: true,
        listingETag: listingReply.response.headers.get("ETag"),
      };
    },
    props.onFailure,
    true,
  );
  const [committed, setCommitted] = useState<{
    message: string;
    readEpoch: number;
  }>();
  const stale =
    !!committed &&
    (!detail.data || detail.data.readEpoch <= committed.readEpoch);
  const acknowledge = (message: string) =>
    setCommitted({ message, readEpoch: readEpoch.current });
  const [inspect, setInspect] = useState(false);
  // The read callback runs after commit. A deliberate Reload can use a resolved
  // later-page namespace hint without automatically loading Listing metadata.
  const namespace: ReturnType<typeof useNamespaceAccessState> =
    useNamespaceAccessState(props, detail.data?.listing.namespaceId || "");
  const metadataAllowed =
    !!detail.data &&
    permitsNamespace(
      namespace.access,
      "resource.read",
      "catalog.read",
      detail.data.listing.namespaceId,
    );
  const contentAllowed =
    !!detail.data &&
    !namespace.busy &&
    !namespace.error &&
    permitsNamespace(
      namespace.access,
      "resource.read_content",
      "catalog.read",
      detail.data.listing.namespaceId,
    );
  return (
    <>
      <div className="workspace-toolbar">
        <a href={`${props.runtime.basePath}/reviews`}>Back to reviews</a>
        <button
          type="button"
          disabled={detail.busy}
          onClick={() => {
            if (confirmDiscard()) detail.refresh();
          }}
        >
          Reload review and next steps
        </button>
      </div>
      <ReadState {...detail} />
      {committed && (
        <CommandReceipt message={committed.message}>
          <span>
            {" "}
            This command result may be a historical replay. Read current state
            before the next action.
          </span>
        </CommandReceipt>
      )}
      <ReadState {...namespace} />
      {detail.data && (
        <>
          <section className="workspace-panel">
            <p className="eyebrow">Last loaded review</p>
            {stale && (
              <p className="notice">
                The loaded review predates the committed command result. Reload
                review and next steps to inspect current state.
              </p>
            )}
            <h2>
              {detail.data.submission.state === "submitted"
                ? "Awaiting independent review"
                : `Review ${reviewLabels[detail.data.submission.state]}`}
            </h2>
            <p>
              <span
                className="state-badge"
                data-tone={reviewTones[detail.data.submission.state]}
              >
                {reviewLabels[detail.data.submission.state]}
              </span>
            </p>
            <ReviewDecisions submission={detail.data.submission} />
            <RevisionComparison
              {...props}
              access={namespace.access}
              kind="listing"
              ownerId={detail.data.submission.listingId}
              namespaceId={detail.data.listing.namespaceId}
              fixedAfter={detail.data.submission.listingRevisionId}
            />
            {!stale && <p>{reviewGuidance[detail.data.submission.state]}</p>}
            <p>
              Submitted by {detail.data.submission.submittedBy}. Policy:{" "}
              {detail.data.submission.policyVersion}.
            </p>
            <p>
              Evidence digest:{" "}
              <code>{detail.data.submission.evidenceDigest}</code>
            </p>
            <div className="workspace-toolbar">
              {metadataAllowed ? (
                <a
                  href={`${props.runtime.basePath}/listings/${detail.data.listing.id}`}
                >
                  Listing
                </a>
              ) : (
                <span>
                  Listing: <code>{detail.data.listing.id}</code>
                </span>
              )}
              {contentAllowed ? (
                <>
                  <a
                    href={`${props.runtime.basePath}/listing-revisions/${detail.data.submission.listingRevisionId}`}
                  >
                    Submitted listing revision
                  </a>
                  <a
                    href={`${props.runtime.basePath}/version-revisions/${detail.data.submission.versionRevisionId}`}
                  >
                    Submitted version revision
                  </a>
                  <button
                    onClick={() => {
                      if (!inspect || confirmDiscard()) setInspect((v) => !v);
                    }}
                    aria-expanded={inspect}
                  >
                    Inspect submitted evidence
                  </button>
                </>
              ) : (
                <>
                  <span>
                    Submitted listing revision:{" "}
                    <code>{detail.data.submission.listingRevisionId}</code>
                  </span>
                  <span>
                    Submitted version revision:{" "}
                    <code>{detail.data.submission.versionRevisionId}</code>
                  </span>
                </>
              )}
            </div>
            <EvidenceDetails
              title="Submission and decision history"
              value={detail.data.submission}
            />
            {!namespace.busy && !namespace.error && !contentAllowed && (
              <p className="muted">
                Submitted documents and evidence require content access to this
                namespace.
              </p>
            )}
          </section>
          {inspect && contentAllowed && (
            <SubmittedEvidence
              {...props}
              {...detail.data}
              access={namespace.access}
              onAcknowledged={acknowledge}
              stale={stale || detail.busy || !!detail.error}
              readEpoch={detail.data.readEpoch}
            />
          )}
          {detail.data.submission.state === "approved" &&
            !namespace.busy &&
            !namespace.error && (
              <PublishReview
                {...props}
                {...detail.data}
                access={namespace.access}
                listingInspected={
                  detail.data.listingInspected && metadataAllowed
                }
                onAcknowledged={acknowledge}
                stale={stale || detail.busy || !!detail.error}
                readEpoch={detail.data.readEpoch}
              />
            )}
        </>
      )}
    </>
  );
}

type Review = {
  submission: components["schemas"]["ReviewSubmission"];
  listing: Pick<
    components["schemas"]["CatalogListing"],
    "id" | "namespaceId" | "currentReleaseId"
  >;
  listingInspected: boolean;
  etag: string | null;
  listingETag: string | null;
  onAcknowledged: (message: string) => void;
  stale: boolean;
  readEpoch: number;
};

/** Load and match the exact submitted validation and listing evidence before review. */
function SubmittedEvidence(props: WorkspaceProps & Review) {
  const namespaceAccess = useNamespaceAccess(props, props.listing.namespaceId);
  const evidence = useRead(
    props.submission.id,
    async (signal) => {
      const data = result(
        await props.client.api.GET(
          "/api/v1/review-submissions/{submissionId}/evidence",
          { params: { path: { submissionId: props.submission.id } }, signal },
        ),
        checks.EvidenceSet,
      );
      if (
        data.id !== props.submission.evidenceSetId ||
        data.digest !== props.submission.evidenceDigest ||
        data.policyVersion !== props.submission.policyVersion
      )
        throw new Error("Submission evidence does not match.");
      const listingRevision = result(
        await props.client.api.GET("/api/v1/listing-revisions/{revisionId}", {
          params: { path: { revisionId: props.submission.listingRevisionId } },
          signal,
        }),
        checks.ListingRevision,
      );
      if (!submittedListingMatches(props.submission, listingRevision))
        throw new Error("Submitted listing revision does not match.");
      return { evidence: data, listingRevision };
    },
    props.onFailure,
  );
  const [action, setAction] = useState<
    "approve" | "request-changes" | "reject"
  >("approve");
  const [reason, setReason] = useState("");
  return (
    <>
      <ReadState {...evidence} />
      {evidence.data && (
        <section className="workspace-panel">
          <p className="eyebrow">Submitted listing metadata</p>
          <ListingMetadata revision={evidence.data.listingRevision} />
          <h2>Submitted evidence</h2>
          <ValidationFindings
            evidence={evidence.data.evidence}
            revisionId={props.submission.versionRevisionId}
            submitted
          />
          <EvidenceDetails
            title="Exact evidence details"
            value={evidence.data.evidence}
          />
          <EvidenceDetails
            title="Exact listing metadata"
            value={evidence.data.listingRevision}
          />
        </section>
      )}
      <p className="notice">
        Use a different sign-in identity from the submitter. The Catalog
        verifies independence when you submit a decision; the displayed subject
        name alone does not identify the submitter's membership and realm.
      </p>
      <ActionForm
        key={props.readEpoch}
        title="Record review decision"
        icon="access"
        intent={
          action === "approve"
            ? "primary"
            : action === "reject"
              ? "danger"
              : "warning"
        }
        retainDraft={!!reason || action !== "approve"}
        onAcknowledged={props.onAcknowledged}
        editable={
          props.submission.state === "submitted" &&
          permitsNamespace(
            namespaceAccess,
            "review.decide",
            "catalog.review",
            props.listing.namespaceId,
          )
        }
        unavailableReason={
          contributionReason(
            namespaceAccess,
            props.listing.namespaceId,
            "review.decide",
            "catalog.review",
          ) ??
          (props.stale
            ? "Reload review and next steps before preparing another decision."
            : props.submission.state !== "submitted"
              ? "Only a pending submission accepts a new review decision."
              : !props.etag
                ? "Reload the review to obtain its current revision."
                : "Load matching submitted evidence before preparing a decision.")
        }
        description="Apply the selected decision to this exact submission. Approval is checked against the current policy and validation state by the Catalog."
        allowed={
          !props.stale &&
          !!evidence.data &&
          !!props.etag &&
          props.submission.state === "submitted" &&
          permitsNamespace(
            namespaceAccess,
            "review.decide",
            "catalog.review",
            props.listing.namespaceId,
          )
        }
        dirty={!!reason || action !== "approve"}
        onFailure={props.onFailure}
        prepare={() => {
          const path =
            action === "approve"
              ? "/api/v1/review-submissions/{submissionId}:approve"
              : action === "reject"
                ? "/api/v1/review-submissions/{submissionId}:reject"
                : "/api/v1/review-submissions/{submissionId}:request-changes";
          const params = {
            path: { submissionId: props.submission.id },
            header: { "Idempotency-Key": "", "If-Match": props.etag! },
          };
          const body = { reason: reason.trim() };
          return {
            summary: [
              {
                label: "Action",
                value:
                  action === "approve"
                    ? "Approve review"
                    : action === "reject"
                      ? "Reject review"
                      : "Request changes",
              },
              {
                label: "Listing",
                value: evidence.data!.listingRevision.displayName,
              },
              { label: "Listing ID", value: props.listing.id },
              { label: "Submission ID", value: params.path.submissionId },
              { label: "Namespace ID", value: props.listing.namespaceId },
              { label: "Revision", value: params.header["If-Match"] },
              { label: "Reason", value: body.reason },
            ],
            run: async (key) => {
              const reply = result(
                await props.client.api.POST(path, {
                  params: {
                    ...params,
                    header: { ...params.header, "Idempotency-Key": key },
                  },
                  body,
                }),
                (value) =>
                  checks.ReviewDecision(value) &&
                  object(value) &&
                  object(value.submission) &&
                  value.submission.id === props.submission.id &&
                  value.submission.state ===
                    (action === "approve"
                      ? "approved"
                      : action === "reject"
                        ? "rejected"
                        : "changes_required"),
              );
              return `Review ${reviewLabels[reply.submission.state]}.`;
            },
          };
        }}
      >
        <Field label="Decision">
          <select
            required
            value={action}
            onChange={(e) => setAction(e.target.value as typeof action)}
          >
            <option value="approve">Approve</option>
            <option value="request-changes">Request changes</option>
            <option value="reject">Reject</option>
          </select>
        </Field>
        <Reason value={reason} onChange={setReason} />
      </ActionForm>
    </>
  );
}

/** Prepare publication using the approved submission and explicit publication inputs. */
function PublishReview(props: WorkspaceProps & Review) {
  const namespaceAccess = useNamespaceAccess(props, props.listing.namespaceId);
  const [disposition, setDisposition] = useState<"retired" | "superseded">(
    "retired",
  );
  const [reason, setReason] = useState("");
  const [published, setPublished] =
    useState<components["schemas"]["ListingPublished"]>();
  const [historyRead, setHistoryRead] = useState(0);
  const choosePrior =
    !props.listingInspected || !!props.listing.currentReleaseId;
  const priorContext = !props.listingInspected
    ? "Uninspected: current Listing metadata was not read"
    : props.listing.currentReleaseId ||
      "No current release at the loaded Listing revision";
  const historyAllowed = permitsNamespace(
    namespaceAccess,
    "resource.read_history",
    "catalog.read",
    props.listing.namespaceId,
  );
  return (
    <>
      <section
        aria-label="Observed publication context"
        className="workspace-panel"
      >
        <h2>Previous release context</h2>
        <p>{priorContext}</p>
        <p>
          Listing revision: <code>{props.listingETag || "Unavailable"}</code>.
          This is loaded context, not a guarantee of current state. Publication
          checks the captured revision. No current release does not mean this
          Listing was never published; terminal releases remain in history.
        </p>
      </section>
      <ActionForm
        key={props.readEpoch}
        title="Publish approved listing"
        icon="publish"
        onAcknowledged={props.onAcknowledged}
        editable={permitsNamespace(
          namespaceAccess,
          "release.publish",
          "catalog.publish",
          props.listing.namespaceId,
        )}
        unavailableReason={
          contributionReason(
            namespaceAccess,
            props.listing.namespaceId,
            "release.publish",
            "catalog.publish",
          ) ??
          "Reload review and next steps to obtain current listing state before publishing."
        }
        description="Create a release from this exact approved submission. If the listing already has a release, apply the selected disposition to it. Public discovery snapshots are managed separately."
        allowed={
          !props.stale &&
          !!props.listingETag &&
          permitsNamespace(
            namespaceAccess,
            "release.publish",
            "catalog.publish",
            props.listing.namespaceId,
          )
        }
        dirty={choosePrior && (!!reason || disposition !== "retired")}
        retainDraft={choosePrior && (!!reason || disposition !== "retired")}
        onFailure={props.onFailure}
        prepare={() => {
          const body = {
            submissionId: props.submission.id,
            priorDisposition: choosePrior ? disposition : ("retired" as const),
            ...(choosePrior && reason.trim()
              ? { priorReason: reason.trim() }
              : {}),
          };
          const listingId = props.listing.id;
          const etag = props.listingETag!;
          return {
            summary: [
              { label: "Action", value: "Publish approved listing" },
              { label: "Target", value: "Governed listing" },
              { label: "Listing ID", value: listingId },
              { label: "Submission ID", value: body.submissionId },
              { label: "Namespace ID", value: props.listing.namespaceId },
              { label: "Observed previous release", value: priorContext },
              {
                label: "Previous release disposition",
                value: body.priorDisposition,
              },
              { label: "Revision", value: etag },
              { label: "Reason", value: body.priorReason ?? "Not supplied" },
            ],
            run: async (key) => {
              const data = result(
                await props.client.api.POST(
                  "/api/v1/catalog-listings/{listingId}:publish",
                  {
                    params: {
                      path: { listingId },
                      header: { "Idempotency-Key": key, "If-Match": etag },
                    },
                    body,
                  },
                ),
                (value) =>
                  publicationReceiptMatches(value, {
                    tenantId: props.access.tenantId,
                    listingId,
                    namespaceId: props.listing.namespaceId,
                    submissionId: body.submissionId,
                    listingRevisionId: props.submission.listingRevisionId,
                    versionRevisionId: props.submission.versionRevisionId,
                    disposition: body.priorDisposition,
                    ...(props.listingInspected
                      ? {
                          priorReleaseId:
                            props.listing.currentReleaseId ?? null,
                        }
                      : {}),
                  }),
              );
              setPublished(data);
              return "Listing published.";
            },
          };
        }}
      >
        <p>
          Retiring removes the prior release from publication eligibility.
          Superseding records the new release as its replacement and requires a
          reason. Both choices preserve the active discovery snapshot. Build and
          activate a new snapshot to publish the replacement metadata, or
          withdraw discovery separately. Creating a release does not build or
          activate a snapshot.
        </p>
        {choosePrior && (
          <>
            <Field
              label="Previous release disposition"
              hint="Choose what happens to the existing release when this one is published. Check dependent clients before withdrawing an older release."
            >
              <select
                required
                value={disposition}
                onChange={(e) =>
                  setDisposition(e.target.value as typeof disposition)
                }
              >
                <option value="retired">Retire previous release</option>
                <option value="superseded">Supersede previous release</option>
              </select>
            </Field>
            {disposition === "superseded" ? (
              <Reason value={reason} onChange={setReason} />
            ) : (
              <Reason
                label="Reason (optional)"
                required={false}
                value={reason}
                onChange={setReason}
              />
            )}
          </>
        )}
      </ActionForm>
      {published && (
        <section
          className="workspace-panel"
          aria-label="Publication command outcome"
        >
          <h2>Returned release outcome</h2>
          <p>
            This command result may be a historical replay. It does not
            establish current release or discovery state.
          </p>
          <p>
            Published Release ID: <code>{published.release.id}</code>. Returned
            state: <ReleaseState state={published.release.state} />.{" "}
            {historyAllowed && (
              <a
                className="button primary"
                href={`${props.runtime.basePath}/releases/${published.release.id}`}
              >
                Open published release
              </a>
            )}
          </p>
          {published.priorRelease === null ? (
            <p>No prior release was changed by this command.</p>
          ) : (
            <p>
              Prior Release ID: <code>{published.priorRelease.id}</code>.
              Returned state:{" "}
              <ReleaseState state={published.priorRelease.state} />.{" "}
              {historyAllowed && (
                <a
                  href={`${props.runtime.basePath}/releases/${published.priorRelease.id}`}
                >
                  Open prior release
                </a>
              )}
            </p>
          )}
          {historyAllowed && (
            <>
              <button
                type="button"
                onClick={() => setHistoryRead((value) => value + 1)}
              >
                {historyRead
                  ? "Reload current release history"
                  : "Read current release history"}
              </button>
              {historyRead > 0 && (
                <ListingHistory
                  key={historyRead}
                  {...props}
                  id={props.listing.id}
                  type="releases"
                  contentAllowed={false}
                />
              )}
            </>
          )}
        </section>
      )}
    </>
  );
}

/** Read retained release history and its listing context before showing lifecycle actions. */
export function Release(props: WorkspaceProps) {
  const readEpoch = useRef(0);
  const detail = useRead(
    props.id,
    async (signal) => {
      const epoch = ++readEpoch.current;
      const reply = await props.client.api.GET(
        "/api/v1/listing-releases/{releaseId}",
        { params: { path: { releaseId: props.id } }, signal },
      );
      const release = result(reply, checks.ListingRelease);
      if (!permits(props.access, "resource.read", "catalog.read")) {
        const submission = result(
          await props.client.api.GET(
            "/api/v1/review-submissions/{submissionId}",
            {
              params: { path: { submissionId: release.submissionId } },
              signal,
            },
          ),
          checks.ReviewSubmission,
        );
        if (!submission.namespaceId)
          throw new Error("Release scope is unavailable.");
        if (
          !permitsNamespace(
            props.access,
            "resource.read",
            "catalog.read",
            submission.namespaceId,
          )
        ) {
          return {
            release,
            readEpoch: epoch,
            listing: { namespaceId: submission.namespaceId },
            etag: reply.response.headers.get("ETag"),
          };
        }
      }
      const listing = result(
        await props.client.api.GET("/api/v1/catalog-listings/{listingId}", {
          params: { path: { listingId: release.listingId } },
          signal,
        }),
        checks.CatalogListing,
      );
      return {
        release,
        listing,
        etag: reply.response.headers.get("ETag"),
        readEpoch: epoch,
      };
    },
    props.onFailure,
    true,
  );
  const [committed, setCommitted] = useState<{
    message: string;
    readEpoch: number;
  }>();
  const stale =
    !!committed &&
    (!detail.data || detail.data.readEpoch <= committed.readEpoch);
  return (
    <>
      <Heading
        title="Release"
        icon="catalog"
        description="Published listing identity and its current lifecycle disposition."
      />
      <ReadState {...detail} />
      <button
        type="button"
        disabled={detail.busy}
        onClick={() => {
          if (confirmDiscard()) detail.refresh();
        }}
      >
        Reload release state
      </button>
      {committed && (
        <CommandReceipt message={committed.message}>
          <span>
            {" "}
            This command result may be a historical replay. Read current state
            before the next action.
          </span>
        </CommandReceipt>
      )}
      {detail.data && (
        <ReleaseActions
          {...props}
          {...detail.data}
          stale={stale || detail.busy || !!detail.error}
          onAcknowledged={(message) =>
            setCommitted({ message, readEpoch: readEpoch.current })
          }
        />
      )}
    </>
  );
}

// Catalog reads expose only the current published/deprecated release. Retained
// history remains accessible through the release and listing screens.
export function hasCatalogRecord(
  release: { id: string; state: string },
  listing: { currentReleaseId?: string },
) {
  return (
    listing.currentReleaseId === release.id &&
    ["published", "deprecated"].includes(release.state)
  );
}

/** Prepare an eligible release transition with its reason and loaded concurrency token. */
function ReleaseActions(
  props: WorkspaceProps & {
    release: components["schemas"]["ListingRelease"];
    listing: Pick<components["schemas"]["CatalogListing"], "namespaceId"> &
      Partial<
        Pick<components["schemas"]["CatalogListing"], "currentReleaseId">
      >;
    etag: string | null;
    stale: boolean;
    readEpoch: number;
    onAcknowledged: (message: string) => void;
  },
) {
  const [action, setAction] = useState<
    "deprecate" | "retire" | "supersede" | "revoke"
  >("deprecate");
  const [reason, setReason] = useState("");
  const [replacement, setReplacement] = useState("");
  const namespace = useNamespaceAccessState(props, props.listing.namespaceId);
  const namespaceAccess = namespace.access;
  const metadataAllowed = permitsNamespace(
    namespaceAccess,
    "resource.read",
    "catalog.read",
    props.listing.namespaceId,
  );
  const historyAllowed = permitsNamespace(
    namespaceAccess,
    "resource.read_history",
    "catalog.read",
    props.listing.namespaceId,
  );
  const contentAllowed =
    !namespace.busy &&
    !namespace.error &&
    permitsNamespace(
      namespaceAccess,
      "resource.read_content",
      "catalog.read",
      props.listing.namespaceId,
    );
  const [sunset, setSunset] = useState("");
  return (
    <>
      <ReadState {...namespace} />
      {metadataAllowed && (
        <a
          href={`${props.runtime.basePath}/listings/${props.release.listingId}`}
        >
          Back to listing
        </a>
      )}
      {metadataAllowed && historyAllowed && (
        <p>
          <a
            href={`${props.runtime.basePath}/listings/${props.release.listingId}#release-history`}
          >
            Release history
          </a>
        </p>
      )}
      <section className="workspace-panel">
        <p className="eyebrow">Last loaded release</p>
        {props.stale && (
          <p className="notice">
            The displayed release is last-loaded state. Reload release state
            before preparing another disposition.
          </p>
        )}
        <h2>
          Release <ReleaseState state={props.release.state} />
        </h2>
        <p>
          A release retains the approved listing and version. Only a current
          published or deprecated release has a consumer Catalog record. A
          public listing also needs a separately built and activated discovery
          snapshot for public discovery; this screen does not establish that a
          snapshot is active.
        </p>
        <p className="muted">
          Discovery does not install or invoke a resource or establish approval
          for your use. Inspect the exact revisions and follow their documented
          prerequisites.
        </p>
        <Facts
          rows={[
            ["Release ID", <code>{props.release.id}</code>],
            [
              "Listing",
              metadataAllowed ? (
                <a
                  href={`${props.runtime.basePath}/listings/${props.release.listingId}`}
                >
                  <code>{props.release.listingId}</code>
                </a>
              ) : (
                <code>{props.release.listingId}</code>
              ),
            ],
            [
              "Exact version revision",
              contentAllowed ? (
                <a
                  href={`${props.runtime.basePath}/version-revisions/${props.release.versionRevisionId}`}
                >
                  <code>{props.release.versionRevisionId}</code>
                </a>
              ) : (
                <code>{props.release.versionRevisionId}</code>
              ),
            ],
            [
              "Released listing revision",
              contentAllowed ? (
                <a
                  href={`${props.runtime.basePath}/listing-revisions/${props.release.listingRevisionId}`}
                >
                  <code>{props.release.listingRevisionId}</code>
                </a>
              ) : (
                <code>{props.release.listingRevisionId}</code>
              ),
            ],
            [
              "Activated",
              <time dateTime={props.release.activatedAt}>
                {new Date(props.release.activatedAt).toLocaleString()}
              </time>,
            ],
            [
              "Disposition last updated",
              <time dateTime={props.release.updatedAt}>
                {new Date(props.release.updatedAt).toLocaleString()}
              </time>,
            ],
            ...(props.release.dispositionReason !== undefined
              ? ([
                  [
                    "Disposition reason",
                    props.release.dispositionReason,
                    "release-disposition-reason",
                  ],
                ] as const)
              : []),
            ...(props.release.sunsetAt !== undefined
              ? ([
                  [
                    "Declared sunset",
                    <>
                      <time dateTime={props.release.sunsetAt}>
                        {new Date(props.release.sunsetAt).toLocaleString()}
                      </time>
                      <p className="muted">
                        This declared date does not by itself change the release
                        state.
                      </p>
                    </>,
                  ],
                ] as const)
              : []),
            ...(props.release.replacementReleaseId !== undefined
              ? ([
                  [
                    "Replacement release",
                    permits(
                      namespaceAccess,
                      "resource.read_history",
                      "catalog.read",
                    ) ? (
                      <a
                        href={`${props.runtime.basePath}/releases/${props.release.replacementReleaseId}`}
                      >
                        <code>{props.release.replacementReleaseId}</code>
                      </a>
                    ) : (
                      <code>{props.release.replacementReleaseId}</code>
                    ),
                  ],
                ] as const)
              : []),
          ]}
        />
        {permits(props.access, "publication.read", "catalog.read") && (
          <p>
            <a href={`${props.runtime.basePath}/publication`}>
              Public discovery snapshots
            </a>
          </p>
        )}
        <div className="workspace-toolbar">
          {historyAllowed ? (
            <a
              href={`${props.runtime.basePath}/reviews/${props.release.submissionId}`}
            >
              Approved submission
            </a>
          ) : (
            <span>
              Approved submission: <code>{props.release.submissionId}</code>
            </span>
          )}
          {!props.stale &&
            metadataAllowed &&
            hasCatalogRecord(props.release, props.listing) && (
              <a href={`${props.runtime.basePath}/catalog/${props.release.id}`}>
                Catalog record
              </a>
            )}
        </div>
        <EvidenceDetails title="Release metadata" value={props.release} />
      </section>
      <ActionForm
        key={props.readEpoch}
        title="Change release disposition"
        intent={action === "deprecate" ? "warning" : "danger"}
        icon={action === "deprecate" ? "warning" : "remove"}
        retainDraft={
          !!reason || !!replacement || !!sunset || action !== "deprecate"
        }
        onAcknowledged={props.onAcknowledged}
        editable={
          props.release.state !== "revoked" &&
          permitsNamespace(
            namespaceAccess,
            "release.transition",
            "catalog.publish",
            props.listing.namespaceId,
          )
        }
        unavailableReason={
          contributionReason(
            namespaceAccess,
            props.listing.namespaceId,
            "release.transition",
            "catalog.publish",
          ) ??
          (props.stale
            ? "Reload release state before preparing another disposition."
            : !props.etag
              ? "Reload the release to obtain its current revision."
              : "The selected disposition is unavailable in the last loaded state. Retired and superseded releases can only be revoked. Revoked releases cannot be changed.")
        }
        description={
          action === "deprecate"
            ? "Deprecation keeps the current release discoverable. Replacement and sunset details are guidance; they do not change the listing's current release or schedule its withdrawal."
            : action === "supersede"
              ? "Supersession removes this release from current Catalog discovery and withdraws each entire active discovery snapshot that contains it. Naming a replacement does not make it the listing's current release."
              : "Retirement or revocation removes this release from current Catalog discovery and withdraws each entire active discovery snapshot that contains it. Other entries in those snapshots also become unavailable until a new snapshot is activated."
        }
        allowed={
          !props.stale &&
          !namespace.busy &&
          !namespace.error &&
          !!props.etag &&
          releaseTransitionAllowed(props.release.state, action) &&
          permitsNamespace(
            namespaceAccess,
            "release.transition",
            "catalog.publish",
            props.listing.namespaceId,
          )
        }
        dirty={!!reason || !!replacement || !!sunset || action !== "deprecate"}
        onFailure={props.onFailure}
        prepare={() => {
          const path =
            action === "deprecate"
              ? "/api/v1/listing-releases/{releaseId}:deprecate"
              : action === "retire"
                ? "/api/v1/listing-releases/{releaseId}:retire"
                : action === "supersede"
                  ? "/api/v1/listing-releases/{releaseId}:supersede"
                  : "/api/v1/listing-releases/{releaseId}:revoke";
          const body = {
            reason: reason.trim(),
            ...((action === "deprecate" || action === "supersede") &&
            replacement
              ? { replacementId: replacement }
              : {}),
            ...(action === "deprecate" && sunset
              ? { sunsetAt: new Date(sunset).toISOString() }
              : {}),
          };
          const releaseId = props.release.id;
          const etag = props.etag!;
          return {
            summary: [
              {
                label: "Action",
                value:
                  action === "deprecate"
                    ? "Deprecate release"
                    : action === "retire"
                      ? "Retire release"
                      : action === "supersede"
                        ? "Supersede release"
                        : "Revoke release",
              },
              { label: "Target", value: "Governed listing" },
              { label: "Release ID", value: releaseId },
              { label: "Namespace ID", value: props.listing.namespaceId },
              {
                label: "Replacement release ID",
                value: body.replacementId ?? "Not supplied",
              },
              { label: "Sunset", value: body.sunsetAt ?? "Not supplied" },
              { label: "Revision", value: etag },
              { label: "Reason", value: body.reason },
            ],
            run: async (key) => {
              const updated = result(
                await props.client.api.POST(path, {
                  params: {
                    path: { releaseId },
                    header: { "Idempotency-Key": key, "If-Match": etag },
                  },
                  body,
                }),
                (value) =>
                  checks.ListingRelease(value) &&
                  object(value) &&
                  value.id === releaseId &&
                  value.listingId === props.release.listingId &&
                  value.state ===
                    (action === "deprecate"
                      ? "deprecated"
                      : action === "retire"
                        ? "retired"
                        : action === "supersede"
                          ? "superseded"
                          : "revoked"),
              );
              return `Release ${updated.state}.`;
            },
          };
        }}
      >
        <Field label="Disposition">
          <select
            required
            value={action}
            onChange={(e) => setAction(e.target.value as typeof action)}
          >
            <option value="deprecate">Deprecate</option>
            <option value="retire">Retire</option>
            <option value="supersede">Supersede</option>
            <option value="revoke">Revoke</option>
          </select>
        </Field>
        <Reason value={reason} onChange={setReason} />
        {(action === "deprecate" || action === "supersede") && (
          <ReleaseReplacement
            props={{ ...props, access: namespaceAccess }}
            namespaceId={props.listing.namespaceId}
            listingId={props.release.listingId}
            releaseId={props.release.id}
            value={replacement}
            onChange={setReplacement}
            required={action === "supersede"}
          />
        )}
        {action === "deprecate" && (
          <Field
            label="Sunset time (optional, local time)"
            hint="Optional planned end date, in your browser's timezone. Leave blank if no sunset is planned."
          >
            <input
              type="datetime-local"
              value={sunset}
              onChange={(e) => setSunset(e.target.value)}
            />
          </Field>
        )}
      </ActionForm>
    </>
  );
}
