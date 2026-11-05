import {
  administrationParameter,
  moderationSearch,
} from "./administration-filter-state";
import { Facts } from "./Facts";
import { commandSummary } from "./workspace";
import { StatusLabel, stateLabel } from "./operational-labels";
import { DateValue } from "./date-value";
import { documentationGuideLink } from "../api/session";
import { LiveRefreshNotice } from "./LiveRefreshNotice";
// Opinions remain separate from relevance, approval and installation telemetry.
import { useEffect, useId, useRef, useState } from "react";
import {
  result,
  type Access,
  type Client,
  type SearchResult,
  type RecordDetail,
} from "../api/client";
import type { components } from "../api/schema";
import { acknowledged } from "./audit-handoff";
import { permits, permitsNamespace, ownRatingAccess } from "../api/permissions";
import {
  array,
  choice,
  flag,
  identifier,
  number,
  object,
  text,
  timestamp,
  cursor,
} from "../api/values";
import { useNamespaceAccessState } from "./namespace-access";
import { kindLabel } from "../resource-kinds";
import { CommandReceipt, EvidenceDetails } from "./authoring";
import { Icon, controlIcons } from "../shell/Icon";
import { CopyReference } from "./CopyReference";
import { CaseLookup, useCaseNavigation } from "./case-navigation";
import {
  CommandDialog,
  Field,
  Heading,
  Pager,
  ReadState,
  Reason,
  useRead,
  useUnsaved,
  confirmDiscard,
  type Command,
  type WorkspaceProps,
} from "./workspace";

type Target = components["schemas"]["RatingTarget"];
type Request = components["schemas"]["RatingRequest"];
type Vote = components["schemas"]["Rating"];
type View = components["schemas"]["RatingView"];
/** The returned target identifies the opinion; directory reads cannot add authority. */
function targetSummary(target: Target, ratingId?: string) {
  return commandSummary([
    ...(ratingId ? [["Rating ID", ratingId] as const] : []),
    [
      "Origin",
      target.origin === "local" ? "Local governed resource" : "External source",
    ] as const,
    ["Kind", kindLabel(target.kind)] as const,
    ["Identifier", target.identifier] as const,
    [
      "Version",
      target.versionPresent
        ? target.version || "Empty version"
        : "Not supplied",
    ] as const,
    ...(target.origin === "local"
      ? [
          ["Resource ID", target.resourceId!] as const,
          ["Version revision ID", target.versionRevisionId!] as const,
        ]
      : [
          ["Source ID", target.sourceId!] as const,
          [
            "Presentation generation",
            String(target.presentationGeneration),
          ] as const,
        ]),
  ]);
}
function RatingTargetDetails({
  target,
  ratingId,
  copy = false,
}: {
  target: Target;
  ratingId?: string | undefined;
  copy?: boolean;
}) {
  return (
    <>
      <Facts
        rows={targetSummary(target)
          .filter((item) =>
            copy
              ? ["Origin", "Kind"].includes(item.label)
              : item.label !== "Identifier" && item.label !== "Version",
          )
          .map((item) => [item.label, item.value])}
      />
      {copy && (
        <>
          {targetSummary(target, ratingId)
            .filter((item) =>
              [
                "Rating ID",
                "Resource ID",
                "Version revision ID",
                "Source ID",
                "Presentation generation",
              ].includes(item.label),
            )
            .map((item) => (
              <CopyReference
                key={item.label}
                label={item.label === "Rating ID" ? "rating ID" : item.label}
                value={item.value}
              />
            ))}
          <CopyReference label="AIR identifier" value={target.identifier} />
          {target.versionPresent && (
            <CopyReference
              label="advertised version"
              value={target.version ?? ""}
            />
          )}
        </>
      )}
      <EvidenceDetails
        title="Exact rating references"
        value={{ ...(ratingId ? { ratingId } : {}), target }}
      />
    </>
  );
}

function isTarget(value: unknown): value is Target {
  return (
    object(value) &&
    choice(value.origin, ["local", "foreign"]) &&
    text(value.identifier) &&
    value.identifier.startsWith("urn:air:") &&
    choice(value.kind, ["agent", "mcp_server", "skill", "plugin"]) &&
    flag(value.versionPresent) &&
    (value.version === undefined ||
      (text(value.version) && [...value.version].length <= 128)) &&
    (!value.versionPresent ? !value.version : true) &&
    (value.origin === "local"
      ? identifier(value.resourceId) &&
        identifier(value.versionRevisionId) &&
        value.versionPresent
      : identifier(value.sourceId) &&
        number(value.presentationGeneration, 1, Number.MAX_SAFE_INTEGER, true))
  );
}
function isVote(value: unknown): value is Vote {
  return (
    object(value) &&
    identifier(value.id) &&
    identifier(value.tenantId) &&
    isTarget(value.target) &&
    number(value.score, 0, 5, true) &&
    (value.state === "active"
      ? number(value.score, 1, 5, true)
      : value.score === 0 && value.consent === false) &&
    flag(value.consent) &&
    choice(value.state, ["active", "withdrawn", "removed"]) &&
    number(value.revision, 1, Infinity, true) &&
    timestamp(value.createdAt) &&
    timestamp(value.updatedAt)
  );
}
function isView(value: unknown): value is View {
  return (
    object(value) &&
    isTarget(value.target) &&
    number(value.count, 0, Number.MAX_SAFE_INTEGER, true) &&
    (value.count === 0
      ? value.average === undefined
      : number(value.average, 1, 5, false)) &&
    (value.own === undefined || isVote(value.own)) &&
    flag(value.changedEntry)
  );
}
function sameTarget(left: Target, right: Target) {
  return (
    left.origin === right.origin &&
    left.identifier === right.identifier &&
    left.kind === right.kind &&
    left.versionPresent === right.versionPresent &&
    (!left.versionPresent || (left.version ?? "") === (right.version ?? "")) &&
    (left.origin === "local"
      ? left.resourceId === right.resourceId &&
        left.versionRevisionId === right.versionRevisionId
      : left.sourceId === right.sourceId &&
        left.presentationGeneration === right.presentationGeneration)
  );
}
function receiptFor(value: unknown, own?: Vote) {
  return (
    isReceipt(value) &&
    object(value) &&
    value.revision === (own ? own.revision + 1 : 1) &&
    (!own || value.id === own.id)
  );
}
function isReceipt(value: unknown) {
  return (
    object(value) &&
    identifier(value.id) &&
    number(value.revision, 1, Infinity, true)
  );
}
function isPage(value: unknown) {
  return (
    object(value) && array(value.items, isVote) && cursor(value.nextCursor)
  );
}
export function requestForResult(item: SearchResult): Request | undefined {
  let target: Target;
  const base = {
    identifier: item.identifier,
    kind: item.kind,
    versionPresent: item.versionPresent,
    ...(item.version === undefined ? {} : { version: item.version }),
  };
  if (item.local) {
    target = {
      ...base,
      origin: "local",
      resourceId: item.local.resource.id,
      versionRevisionId: item.local.versionRevision.id,
    };
    return { target, releaseId: item.local.release.id };
  }
  const source = item.indexed?.presentationSourceId ?? item.live?.sourceId;
  const generation =
    item.indexed?.presentationGeneration ?? item.live?.presentationGeneration;
  if (!source || !generation) return undefined;
  target = {
    ...base,
    origin: "foreign",
    sourceId: source,
    presentationGeneration: generation,
  };
  return {
    target,
    ...(item.live && item.ratings?.receipt
      ? { receipt: item.ratings.receipt }
      : {}),
  };
}
export function requestForRecord(record: RecordDetail): Request {
  return {
    target: {
      origin: "local",
      resourceId: record.resource.id,
      versionRevisionId: record.versionRevision.id,
      identifier: record.resource.canonicalIdentifier,
      kind: record.resource.kind,
      versionPresent: true,
      version: record.version.version,
    },
    releaseId: record.release.id,
  };
}
export function RatingLabels({
  item,
  basePath,
}: {
  item: SearchResult;
  basePath: string;
}) {
  return (
    <>
      {item.ratings && (
        <p className="rating-summary">
          <Icon name="star" />
          {item.ratings.count
            ? item.type === "local"
              ? "Ratings: "
              : "Ratings here: "
            : ""}
          {item.ratings.count
            ? `${item.ratings.average?.toFixed(1) ?? "Unavailable"} / 5 (${item.ratings.count})`
            : item.type === "local"
              ? "No ratings yet"
              : "No ratings here yet"}
        </p>
      )}
      {item.peerRatings && (
        <p className="muted">
          Peer-reported rating: {item.peerRatings.average.toFixed(1)} / 5 (
          {item.peerRatings.count}), reported by {item.peerRatings.reporter},
          measured {new Date(item.peerRatings.measuredAt).toLocaleString()}.
        </p>
      )}
      {item.importedHere && (
        <p>
          <a href={`${basePath}/resources/${item.importedHere.resourceId}`}>
            Imported here
          </a>
          {item.importedHere.changedEntry &&
            " · Source entry has changed since import"}
        </p>
      )}
    </>
  );
}
/** A UI hint only; the backend remains the authority for rating admission. */
export function normalizedRatingIdentifier(value: string) {
  const parts = value.split(":");
  const domain = parts[2] ?? "";
  const labels = domain.split(".");
  const ipv4 =
    labels.length === 4 &&
    labels.every(
      (label) => /^(0|[1-9][0-9]{0,2})$/.test(label) && Number(label) <= 255,
    );
  return (
    value.length <= 512 &&
    parts[0] === "urn" &&
    parts[1] === "air" &&
    parts.length >= 4 &&
    domain.length <= 253 &&
    labels.length >= 2 &&
    !ipv4 &&
    labels.every((label) =>
      /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(label),
    ) &&
    parts
      .slice(3)
      .every(
        (part) =>
          part !== "." && part !== ".." && /^[A-Za-z0-9_.-]{1,64}$/.test(part),
      )
  );
}

export function RatingEntry(props: {
  client: Client;
  access?: Access | undefined;
  onFailure: (error: unknown) => void;
  request: Request | undefined;
  namespaceId?: string | undefined;
  live?: boolean | undefined;
  refreshProof?:
    ((signal: AbortSignal) => Promise<Request | undefined>) | undefined;
}) {
  const [open, setOpen] = useState(false);
  if (!props.request) return null;
  if (!normalizedRatingIdentifier(props.request.target.identifier))
    return (
      <p className="muted">
        Ratings require a normalized AIR identifier. This resource uses another
        identifier; no rating request is sent.
      </p>
    );
  return (
    <div className="rating-entry">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => {
          if (!open || confirmDiscard()) setOpen(!open);
        }}
      >
        Ratings for this version
      </button>
      {open && (
        <section
          className="workspace-panel"
          aria-label="Ratings for this version"
        >
          {props.access ? (
            <ReadableRatingPanel
              key={JSON.stringify(props.request.target)}
              {...props}
              access={props.access}
              request={props.request}
            />
          ) : (
            <RatingPanel
              key={JSON.stringify(props.request.target)}
              {...props}
              request={props.request}
            />
          )}
        </section>
      )}
    </div>
  );
}
function ReadableRatingPanel(
  props: React.ComponentProps<typeof RatingPanel> & { access: Access },
) {
  const resolved = useNamespaceAccessState(props, props.namespaceId ?? "");
  return (
    <>
      <ReadState {...resolved} />
      <RatingPanel
        {...props}
        actionAccess={
          resolved.busy || resolved.error ? undefined : resolved.access
        }
      />
    </>
  );
}
function RatingPanel(props: {
  client: Client;
  access?: Access | undefined;
  actionAccess?: Access | undefined;
  onFailure: (error: unknown) => void;
  request: Request;
  namespaceId?: string | undefined;
  live?: boolean | undefined;
  refreshProof?:
    ((signal: AbortSignal) => Promise<Request | undefined>) | undefined;
}) {
  const [proof, setProof] = useState<Request>();
  const [proofMessage, setProofMessage] = useState("");
  const [refreshingProof, setRefreshingProof] = useState(false);
  const proofRequest = useRef<AbortController | undefined>(undefined);
  useEffect(() => () => proofRequest.current?.abort(), []);
  async function refreshProof() {
    proofRequest.current?.abort();
    const controller = new AbortController();
    proofRequest.current = controller;
    setRefreshingProof(true);
    setProofMessage("Refreshing live ratings...");
    try {
      const value = await props.refreshProof?.(controller.signal);
      if (controller.signal.aborted) return;
      if (value) {
        if (!sameTarget(value.target, props.request.target))
          throw new Error("Live rating target changed");
        setProof(value);
        setProofMessage("Live ratings refreshed.");
      } else {
        setProofMessage(
          "This target is no longer on the captured result page. Start a new search and open the current result.",
        );
      }
    } catch (error) {
      if (controller.signal.aborted) return;
      props.onFailure(error);
      setProofMessage(
        "Could not refresh live ratings. Start a new search if the result window expired.",
      );
    } finally {
      if (!controller.signal.aborted) setRefreshingProof(false);
    }
  }
  const request = proof ?? props.request;
  const readAllowed =
    !props.access ||
    !props.access.scopeEnforcement ||
    props.access.scopes.includes("catalog.read");
  const withdrawAllowed =
    !!props.access && ownRatingAccess(props.access).withdraw;
  const read = useRead(
    JSON.stringify([request, readAllowed]),
    async (signal) =>
      readAllowed
        ? result(
            await props.client.api.POST("/api/v1/ratings/read", {
              body: request,
              signal,
            }),
            (value) =>
              isView(value) &&
              sameTarget(value.target, request.target) &&
              (!value.own ||
                (sameTarget(value.own.target, request.target) &&
                  value.own.tenantId === props.access?.tenantId)),
          )
        : undefined,
    props.onFailure,
    true,
  );
  const [score, setScore] = useState(0);
  const [consent, setConsent] = useState<boolean>();
  const [command, setCommand] = useState<Command>();
  const [commandAction, setCommandAction] = useState<"submit" | "withdraw">(
    "submit",
  );
  const [message, setMessage] = useState({ text: "", sequence: 0 });
  const group = useId();
  useUnsaved(score !== 0 || consent !== undefined);
  const own = read.data?.own;
  const allowed =
    !!props.actionAccess &&
    props.actionAccess.actorType === "human" &&
    (request.target.origin === "local"
      ? permitsNamespace(
          props.actionAccess,
          "ratings.own",
          "ratings.write",
          props.namespaceId ?? "",
        )
      : permits(props.actionAccess, "ratings.own", "ratings.write"));
  const liveReady = !props.live || !!request.receipt;
  function prepareSubmit() {
    setCommandAction("submit");
    const body = {
      ...request,
      score: score || own?.score || 0,
      ...(consent === undefined ? {} : { consent }),
    };
    const etag = own ? `"rev-${own.revision}"` : undefined;
    setCommand({
      intent: "primary",
      icon: "star",
      title:
        own?.state === "active" ? "Update your rating" : "Submit your rating",
      description:
        "This is a human opinion about the exact version and source shown. It does not indicate approval or trust.",
      summary: [
        ...targetSummary(request.target, own?.id),
        { label: "Score", value: `${body.score} / 5` },
        {
          label: "Sharing consent",
          value:
            (body.consent ?? (own?.state === "active" ? own.consent : false))
              ? "Allowed"
              : "Not allowed",
        },
      ],
      run: async (key) => {
        result(
          await props.client.api.POST("/api/v1/ratings", {
            body,
            params: {
              header: {
                "Idempotency-Key": key,
                ...(etag ? { "If-Match": etag } : {}),
              },
            },
          }),
          (value) => receiptFor(value, own),
        );
        return "Rating saved.";
      },
    });
  }
  return (
    <>
      {!readAllowed && (
        <p className="notice">
          Reading ratings requires catalog.read scope when enforced.
        </p>
      )}
      {!props.live && readAllowed && (
        <button
          type="button"
          disabled={read.busy || !!command}
          onClick={read.refresh}
        >
          {controlIcons["refresh"]}
          Refresh this rating
        </button>
      )}
      {!props.live && readAllowed && (
        <p className="muted">
          Refresh reads this exact rating target. Your score and sharing-consent
          draft stay here.
        </p>
      )}
      <ReadState {...read} />
      {read.data && (
        <>
          <p className="eyebrow">Last loaded rating for this exact target</p>
          <p className="rating-summary">
            <Icon name="star" />
            {read.data.count
              ? request.target.origin === "foreign"
                ? "Ratings here: "
                : "Ratings: "
              : ""}
            {read.data.count
              ? `${read.data.average?.toFixed(1)} / 5 (${read.data.count})`
              : request.target.origin === "foreign"
                ? "No ratings here yet"
                : "No ratings yet"}
          </p>
          <RatingTargetDetails target={read.data.target} ratingId={own?.id} />
          {!request.target.versionPresent && (
            <p className="notice">
              This source did not supply a version. Your opinion refers to its
              unversioned entry.
            </p>
          )}
          {read.data.changedEntry && (
            <p className="notice">
              The source entry has changed since your rating. Review it before
              updating your opinion.
            </p>
          )}
          {own?.state === "removed" ? (
            <p className="notice">
              A moderator removed your rating. A moderator must permit a new
              submission.
            </p>
          ) : allowed ? (
            <form
              onSubmit={(event) => {
                event.preventDefault();
                prepareSubmit();
              }}
            >
              <fieldset
                className="rating-controls"
                disabled={!!command || !liveReady || read.busy || !!read.error}
              >
                <legend>Your score</legend>
                <div className="rating-choices">
                  {[1, 2, 3, 4, 5].map((value) => (
                    <label key={value}>
                      <input
                        type="radio"
                        aria-label={`${value} ${value === 1 ? "star" : "stars"}`}
                        name={group}
                        value={value}
                        checked={(score || own?.score) === value}
                        onChange={() => setScore(value)}
                        required
                      />
                      <Icon name="star" />
                      {value}
                    </label>
                  ))}
                </div>
                <label className="rating-consent">
                  <input
                    type="checkbox"
                    checked={
                      consent ?? (own?.state === "active" ? own.consent : false)
                    }
                    onChange={(event) => setConsent(event.target.checked)}
                  />
                  Allow my score in shared aggregate summaries
                </label>
                <p className="muted">
                  Sharing starts off for a new or withdrawn rating. Editing an
                  active rating keeps your saved choice unless you change it.
                </p>
                <p className="muted">
                  {request.target.origin === "local"
                    ? "Published summaries include only consenting ratings of locally released versions. Deployment and administrator opt-in are also required."
                    : "Ratings of external entries are excluded from published summaries."}
                </p>
                <p className="muted">
                  Published snapshots and copies cannot be recalled; withdrawal
                  affects future snapshots.
                </p>
                <button
                  className="primary"
                  disabled={!(score || own?.score)}
                  type="submit"
                >
                  {controlIcons["star"]}
                  {own?.state === "active" ? "Update rating" : "Submit rating"}
                </button>
              </fieldset>
            </form>
          ) : (
            <>
              <p className="muted">
                To rate this version, you need a human identity and permission
                for this target.
              </p>
              <details>
                <summary>Rating access</summary>
                <p>
                  Scoring requires a human identity with ratings.own permission
                  for this exact target and ratings.write scope when enforced.
                </p>
              </details>
            </>
          )}
          {own?.state === "active" && (
            <button
              className="danger"
              type="button"
              disabled={
                !!command || !withdrawAllowed || read.busy || !!read.error
              }
              onClick={() => {
                setCommandAction("withdraw");
                setCommand({
                  title: "Withdraw your rating",
                  intent: "danger",
                  icon: "remove",
                  summary: targetSummary(own.target, own.id),
                  description:
                    "Remove your current score from local totals and stop consent for future shared snapshots. Existing published copies remain unchanged.",
                  run: async (key) => {
                    result(
                      await props.client.api.POST(
                        "/api/v1/ratings/{ratingId}:withdraw",
                        {
                          params: {
                            path: { ratingId: own.id },
                            header: {
                              "If-Match": `"rev-${own.revision}"`,
                              "Idempotency-Key": key,
                            },
                          },
                          body: {},
                        },
                      ),
                      (value) => receiptFor(value, own),
                    );
                    return "Rating withdrawn.";
                  },
                });
              }}
            >
              {controlIcons["remove"]}
              Withdraw rating
            </button>
          )}
        </>
      )}
      {props.live && props.refreshProof && (
        <>
          <LiveRefreshNotice />
          <button
            type="button"
            disabled={!!command || refreshingProof}
            aria-busy={refreshingProof}
            onClick={() => void refreshProof()}
          >
            {controlIcons["refresh"]}
            Refresh live ratings
          </button>
        </>
      )}
      {proofMessage && (
        <p role="status">
          {proofMessage}
          {(score !== 0 || consent !== undefined) &&
            " Your rating draft is preserved."}
        </p>
      )}
      {props.live && !liveReady && (
        <p className="notice">
          Search again to read or change ratings for this live result.
          {(score !== 0 || consent !== undefined) &&
            " Your rating draft remains here."}
        </p>
      )}
      <CommandReceipt key={message.sequence} message={message.text} />
      {command && (
        <CommandDialog
          allowed={
            !read.busy &&
            !read.error &&
            (commandAction === "withdraw"
              ? withdrawAllowed
              : allowed && liveReady)
          }
          command={command}
          onFailure={props.onFailure}
          onClose={() => setCommand(undefined)}
          onDone={(text) => {
            setMessage((previous) => ({
              text,
              sequence: previous.sequence + 1,
            }));
            setCommand(undefined);
            setScore(0);
            setConsent(undefined);
            read.refresh();
          }}
        />
      )}
    </>
  );
}
export function OwnRatings(props: WorkspaceProps) {
  return <RatingsList {...props} own />;
}
export default function RatingsList(props: WorkspaceProps & { own?: boolean }) {
  return (
    <RatingCases
      key={JSON.stringify([
        props.access.tenantId,
        props.access.realmId,
        props.access.membershipId,
        props.access.subject,
        props.access.actorType,
        props.own,
      ])}
      {...props}
    />
  );
}
function RatingCases(props: WorkspaceProps & { own?: boolean }) {
  const navigation = useCaseNavigation(
    props,
    props.own ? "my-ratings" : "ratings",
    (search) => {
      setState(administrationParameter(search, "state"));
      setCursor("");
    },
  );
  const [cursorValue, setCursor] = useState("");
  const [state, setState] = useState(() =>
    administrationParameter(navigation.listSearch, "state"),
  );
  const policyKey = JSON.stringify(props.access);
  const [listPolicy, setListPolicy] = useState(policyKey);
  if (listPolicy !== policyKey) {
    setListPolicy(policyKey);
    setCursor("");
    setState("");
  }
  const [command, setCommand] = useState<Command>();
  const [message, setMessage] = useState({ text: "", sequence: 0 });
  const [reason, setReason] = useState("");
  const [moderation, setModeration] = useState<{
    vote: Vote;
    action: "remove" | "permit";
  }>();
  const ownCase = useRef("");
  useUnsaved(!!reason);
  const caseReadAllowed = props.own
    ? ownRatingAccess(props.access).read
    : permits(props.access, "ratings.moderate", "catalog.read");
  const allowed = navigation.id
    ? caseReadAllowed
    : props.own
      ? ownRatingAccess(props.access).read
      : permits(props.access, "ratings.moderate", "tenant.admin");
  const moderate = permits(props.access, "ratings.moderate", "tenant.admin");
  const read = useRead(
    JSON.stringify(
      navigation.id
        ? [props.own, navigation.id, allowed]
        : [props.own, cursorValue, state, allowed, listPolicy],
    ),
    async (signal) => {
      if (!allowed) return { items: [] };
      if (navigation.id)
        return {
          items: [
            result(
              await props.client.api.GET("/api/v1/ratings/{ratingId}", {
                params: { path: { ratingId: navigation.id } },
                signal,
              }),
              (value) =>
                isVote(value) && value.id.toLowerCase() === navigation.id,
            ),
          ],
        };
      return result(
        await props.client.api.GET(
          props.own ? "/api/v1/ratings" : "/api/v1/ratings/inbox",
          {
            params: {
              query: {
                limit: 25,
                ...(cursorValue ? { cursor: cursorValue } : {}),
                ...(state ? { state: state as Vote["state"] } : {}),
              },
            },
            signal,
          },
        ),
        isPage,
      );
    },
    props.onFailure,
    true,
  );
  function action(vote: Vote, commandAction: "withdraw" | "remove" | "permit") {
    const path =
      commandAction === "withdraw"
        ? "/api/v1/ratings/{ratingId}:withdraw"
        : commandAction === "remove"
          ? "/api/v1/ratings/{ratingId}:remove"
          : "/api/v1/ratings/{ratingId}:permit";
    const body = commandAction === "withdraw" ? {} : { reason };
    setCommand({
      intent: commandAction === "permit" ? "primary" : "danger",
      icon: commandAction === "permit" ? "add" : "remove",
      title: `${commandAction === "permit" ? "Permit a new submission" : commandAction === "remove" ? "Remove rating" : "Withdraw rating"}`,
      description:
        commandAction === "withdraw"
          ? "Clear your current score and sharing consent. Previously published summaries and copies remain unchanged."
          : commandAction === "remove"
            ? "Clear this score and sharing consent. The person cannot submit again until a moderator permits a new submission. Previously published summaries and copies remain unchanged."
            : "Allow the person to submit a new rating. The old score stays withdrawn and sharing consent stays off.",
      summary: [
        ...targetSummary(vote.target, vote.id),
        { label: "Revision", value: `"rev-${vote.revision}"` },
        ...(commandAction === "withdraw"
          ? []
          : [{ label: "Moderation reason", value: reason }]),
      ],
      run: async (key) => {
        const reply = await props.client.api.POST(path, {
          body,
          params: {
            path: { ratingId: vote.id },
            header: {
              "Idempotency-Key": key,
              "If-Match": `"rev-${vote.revision}"`,
            },
          },
        });
        const saved = result(reply, (value) => receiptFor(value, vote));
        return acknowledged("Rating state updated.", reply.response, {
          action: `rating.${commandAction}`,
          targetType: "rating",
          targetId: vote.id,
          revision: saved.revision,
        });
      },
    });
  }
  return (
    <>
      <Heading
        title={
          navigation.id
            ? "Rating case"
            : props.own
              ? "My ratings"
              : "Rating moderation"
        }
        icon="star"
        description={
          props.own
            ? "Review and withdraw your ratings for specific resource versions. You can withdraw an active rating here after losing access to its resource or permission to submit ratings."
            : "Review ratings and remove or permit submissions."
        }
        help={(() => {
          const help = documentationGuideLink(
            props.runtime.documentationUrl,
            "ratings",
          );
          return help && <a href={help.href}>{help.label}</a>;
        })()}
      />
      {navigation.id && (
        <p>
          <a
            href={navigation.listRoot}
            onClick={(event) => navigation.open(event, "")}
          >
            {navigation.hasReturn
              ? "Return to rating list"
              : "Open rating list"}
          </a>
        </p>
      )}
      {moderation && (
        <section
          className="workspace-panel"
          aria-label="Selected rating moderation"
        >
          <p>
            This selected rating is separate from list filters. The Catalog
            checks its revision and your current access on confirmation.
          </p>
          <form
            className="workspace-form"
            onSubmit={(event) => {
              event.preventDefault();
              if (
                !reason.trim() ||
                [...reason].length > 2000 ||
                read.busy ||
                read.error ||
                command ||
                !moderate ||
                !allowed
              )
                return;
              action(moderation.vote, moderation.action);
            }}
          >
            <h2>
              {moderation.action === "permit"
                ? "Permit a new submission"
                : "Remove this rating"}
            </h2>
            <p>
              {moderation.vote.target.identifier} ·{" "}
              {moderation.vote.target.versionPresent
                ? `Version: ${moderation.vote.target.version || "Empty version"}`
                : "No version supplied"}{" "}
              · Revision {moderation.vote.revision}
            </p>
            {moderation.action === "permit" && (
              <p>
                This permits a future submission; it does not restore the
                removed score.
              </p>
            )}
            <fieldset
              className="workspace-fields"
              disabled={!!command || !moderate || !allowed}
            >
              <Reason
                label="Moderation reason"
                value={reason}
                onChange={setReason}
              />
            </fieldset>
            <div className="form-actions">
              <button
                className={
                  moderation.action === "permit" ? "primary" : "danger"
                }
                type="submit"
                disabled={
                  !!command ||
                  !moderate ||
                  !allowed ||
                  read.busy ||
                  !!read.error ||
                  !reason.trim() ||
                  [...reason].length > 2000
                }
              >
                {
                  controlIcons[
                    moderation.action === "permit" ? "add" : "remove"
                  ]
                }
                {moderation.action === "permit"
                  ? "Permit new submission"
                  : "Remove rating"}
              </button>
              <button
                type="button"
                disabled={!!command}
                onClick={() => {
                  if (!confirmDiscard()) return;
                  setReason("");
                  setModeration(undefined);
                }}
              >
                {controlIcons["close"]}
                Cancel moderation
              </button>
            </div>
          </form>
        </section>
      )}
      {!allowed ? (
        <p role="alert" className="notice">
          You do not have access to{" "}
          {props.own ? "your ratings" : "rating moderation"}.{" "}
          <a href={`${props.runtime.basePath}/access`}>Review My access</a>
        </p>
      ) : (
        <>
          {!navigation.id &&
            (caseReadAllowed ? (
              <details className="workspace-advanced">
                <summary>Open a case by ID</summary>
                <CaseLookup kind="rating" open={navigation.move} />
              </details>
            ) : (
              <p>
                Opening an exact case also requires catalog.read scope when
                enforced.
              </p>
            ))}
          <section className="workspace-panel" aria-label="Rating filters">
            <div className="workspace-toolbar">
              {!navigation.id && (
                <Field label="Rating state">
                  <select
                    value={state}
                    onChange={(event) => {
                      setState(event.target.value);
                      navigation.updateListSearch(
                        moderationSearch({ state: event.target.value }),
                      );
                      setCursor("");
                    }}
                  >
                    <option value="">All states</option>
                    {!["", "active", "withdrawn", "removed"].includes(
                      state,
                    ) && <option value={state}>Unsupported state</option>}
                    {["active", "withdrawn", "removed"].map((value) => (
                      <option key={value} value={value}>
                        {stateLabel(value)}
                      </option>
                    ))}
                  </select>
                </Field>
              )}
            </div>
            <p className="muted">
              Ratings are human opinions, separate from peer reports and search
              relevance. They do not establish approval or trust.
            </p>
            {props.own && (
              <p>
                Saved consent permits future eligible shared summaries; it does
                not prove publication. Foreign ratings are excluded from
                published summaries.
              </p>
            )}
          </section>
          <button
            disabled={read.busy || !!command}
            onClick={() => {
              read.refresh();
            }}
          >
            {controlIcons["refresh"]}
            {navigation.id ? "Read current rating" : "Refresh ratings"}
          </button>
          <ReadState {...read} />
          {read.data && (
            <p className="eyebrow">
              Last loaded {props.own ? "own ratings" : "ratings"} page
            </p>
          )}
          {read.data?.items.map((vote) => (
            <article
              className={navigation.id ? "workspace-panel" : "case-summary"}
              key={vote.id}
            >
              <h2>{vote.target.identifier}</h2>
              <p>
                {vote.target.versionPresent
                  ? `Version: ${vote.target.version || "Empty version"}`
                  : "No version supplied"}{" "}
                ·{" "}
                <StatusLabel
                  state={vote.state}
                  tone={vote.state === "active" ? "info" : "neutral"}
                />
                {vote.state === "active" ? ` · ${vote.score} / 5` : ""}
              </p>
              <p>
                Updated <DateValue value={vote.updatedAt} />
              </p>
              {navigation.id && (
                <CopyReference
                  label="rating revision"
                  value={String(vote.revision)}
                />
              )}
              {!navigation.id && caseReadAllowed && (
                <p>
                  <a
                    href={`${navigation.root}/${encodeURIComponent(vote.id)}`}
                    onClick={(event) => {
                      if (props.own) ownCase.current = vote.id;
                      navigation.open(event, vote.id);
                    }}
                  >
                    {controlIcons["arrow"]}
                    Open rating case
                  </a>
                </p>
              )}
              <details className="workspace-advanced" open={!!navigation.id}>
                <summary>Identifiers and sharing</summary>
                <RatingTargetDetails
                  target={vote.target}
                  ratingId={vote.id}
                  copy
                />
                {props.own && (
                  <Facts
                    rows={[
                      ["Saved sharing consent", vote.consent ? "Yes" : "No"],
                      [
                        "Last rating update",
                        <time dateTime={vote.updatedAt}>
                          {new Date(vote.updatedAt).toLocaleString()}
                        </time>,
                      ],
                    ]}
                  />
                )}
              </details>
              {props.own &&
                navigation.id &&
                ownCase.current !== vote.id &&
                props.access.permissions.includes("ratings.moderate") && (
                  <p>
                    This exact read also permits moderated cases. To confirm
                    ownership before withdrawal, open this case from your My
                    ratings list.
                  </p>
                )}
              {props.own
                ? vote.state === "active" &&
                  (!navigation.id ||
                    ownCase.current === vote.id ||
                    !props.access.permissions.includes("ratings.moderate")) && (
                    <button
                      className="danger"
                      disabled={
                        !!command ||
                        read.busy ||
                        !!read.error ||
                        !ownRatingAccess(props.access).withdraw
                      }
                      onClick={() => action(vote, "withdraw")}
                    >
                      {controlIcons["remove"]}
                      Withdraw rating
                    </button>
                  )
                : moderate &&
                  (moderation?.vote.id !== vote.id ||
                    moderation.vote.revision !== vote.revision) && (
                    <button
                      className={
                        vote.state === "removed" ? "primary" : "danger"
                      }
                      disabled={!!command || read.busy || !!read.error}
                      onClick={() => {
                        if (moderation?.vote.id !== vote.id) {
                          if (!confirmDiscard()) return;
                          setReason("");
                        }
                        setModeration({
                          vote,
                          action:
                            vote.state === "removed" ? "permit" : "remove",
                        });
                      }}
                    >
                      {
                        controlIcons[
                          vote.state === "removed" ? "add" : "remove"
                        ]
                      }
                      {vote.state === "removed"
                        ? "Permit new submission"
                        : "Remove rating"}
                    </button>
                  )}
            </article>
          ))}
          {read.data && !read.data.items.length && (
            <section className="workspace-panel" aria-label="Rating results">
              <p>
                {cursorValue
                  ? "No ratings match this page. Return to the first page or choose another state."
                  : state
                    ? "No ratings match this state. Choose another state or All states."
                    : props.own
                      ? "You have not rated any resources. Open a resource version to leave a rating if you have permission."
                      : "No ratings to moderate."}
              </p>
            </section>
          )}
          {!navigation.id && (
            <Pager
              label="Rating cases pagination"
              cursor={cursorValue}
              nextCursor={read.data?.nextCursor}
              busy={read.busy}
              onPage={(cursor) => {
                setCursor(cursor);
              }}
            />
          )}
        </>
      )}
      <CommandReceipt key={message.sequence} message={message.text} />
      {command && (
        <CommandDialog
          allowed={
            allowed &&
            !read.busy &&
            !read.error &&
            (props.own ? ownRatingAccess(props.access).withdraw : moderate)
          }
          command={command}
          onFailure={props.onFailure}
          onClose={() => setCommand(undefined)}
          onDone={(text) => {
            setMessage((previous) => ({
              text,
              sequence: previous.sequence + 1,
            }));
            setCommand(undefined);
            if (
              command.summary?.some(
                (fact) =>
                  fact.label === "Moderation reason" && fact.value === reason,
              )
            ) {
              setReason("");
              setModeration(undefined);
            }
            read.refresh();
          }}
        />
      )}
    </>
  );
}
function isSharing(
  value: unknown,
): value is components["schemas"]["RatingSharing"] {
  return (
    object(value) &&
    text(value.host) &&
    choice(value.audience, ["anonymous", "authenticated"]) &&
    flag(value.enabled) &&
    number(value.revision, 0, Infinity, true)
  );
}
export function RatingSharing(props: {
  client: Client;
  access: Access;
  host: string;
  audience: "anonymous" | "authenticated";
  onFailure: (error: unknown) => void;
}) {
  const [open, setOpen] = useState(false);
  const currentAllowed = permits(
    props.access,
    "ratings.sharing.manage",
    "tenant.admin",
  );
  const [admitted, setAdmitted] = useState(currentAllowed);
  useEffect(() => {
    if (currentAllowed) setAdmitted(true);
  }, [currentAllowed]);
  if (!currentAllowed && !admitted) return null;
  return (
    <section className="workspace-panel">
      <p>
        Rating sharing for <strong>{props.host}</strong> · {props.audience}{" "}
        audience
      </p>
      <button
        aria-expanded={open}
        onClick={() => {
          if (!open || confirmDiscard()) setOpen(!open);
        }}
      >
        {controlIcons["star"]}
        Rating summary sharing
      </button>
      {open && (
        <SharingControls
          key={JSON.stringify([props.host, props.audience])}
          {...props}
        />
      )}
    </section>
  );
}
function SharingControls(props: {
  access: Access;
  client: Client;
  host: string;
  audience: "anonymous" | "authenticated";
  onFailure: (error: unknown) => void;
}) {
  const [reason, setReason] = useState("");
  const [command, setCommand] = useState<Command>();
  const [message, setMessage] = useState({ text: "", sequence: 0 });
  useUnsaved(!!reason.trim() || !!command);
  const read = useRead(
    JSON.stringify([props.host, props.audience]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/ratings/sharing", {
          params: { query: { host: props.host, audience: props.audience } },
          signal,
        }),
        (value): value is components["schemas"]["RatingSharing"] =>
          isSharing(value) &&
          value.host === props.host &&
          value.audience === props.audience,
      ),
    props.onFailure,
  );
  return (
    <>
      <ReadState {...read} />
      {read.data && (
        <>
          <p>
            Audience opt-in:{" "}
            <span className="workspace-status" data-tone="neutral">
              {read.data.enabled ? "Enabled" : "Disabled"}
            </span>
            . The operator must also enable sharing. Only consented scores that
            meet the minimum count qualify.
          </p>
          <p>
            Generate and activate a new ordinary publication to update
            summaries. This choice does not publish automatically.
          </p>
          <Reason label="Sharing reason" value={reason} onChange={setReason} />
          <button
            className="warning"
            disabled={
              !permits(
                props.access,
                "ratings.sharing.manage",
                "tenant.admin",
              ) ||
              read.busy ||
              !!read.error ||
              !reason.trim() ||
              [...reason].length > 2000 ||
              !!command
            }
            onClick={() => {
              const current = read.data;
              if (!current) return;
              const body = {
                host: props.host,
                audience: props.audience,
                enabled: !current.enabled,
                reason,
              };
              setCommand({
                intent: "warning",
                icon: "save",
                title: current.enabled
                  ? "Disable rating sharing"
                  : "Enable rating sharing",
                description:
                  "Only future snapshots use this choice. Disabling does not recall published copies.",
                summary: commandSummary([
                  ["Host", body.host],
                  ["Audience", body.audience],
                  ["Reason", body.reason],
                  [
                    "Revision",
                    current.revision
                      ? `"rev-${current.revision}"`
                      : "No saved choice",
                  ],
                  ["Sharing", body.enabled ? "Enabled" : "Disabled"],
                ]),
                run: async (key) => {
                  result(
                    await props.client.api.POST("/api/v1/ratings/sharing", {
                      body,
                      params: {
                        header: {
                          "Idempotency-Key": key,
                          ...(current.revision
                            ? { "If-Match": `"rev-${current.revision}"` }
                            : {}),
                        },
                      },
                    }),
                    isSharing,
                  );
                  return "Rating sharing choice saved.";
                },
              });
            }}
          >
            {controlIcons["save"]}
            {read.data.enabled ? "Disable sharing" : "Enable sharing"}
          </button>
        </>
      )}
      <CommandReceipt key={message.sequence} message={message.text} />
      {command && (
        <CommandDialog
          allowed={
            permits(props.access, "ratings.sharing.manage", "tenant.admin") &&
            !read.busy &&
            !read.error
          }
          command={command}
          onFailure={props.onFailure}
          onClose={() => setCommand(undefined)}
          onDone={(text) => {
            setMessage((previous) => ({
              text,
              sequence: previous.sequence + 1,
            }));
            setCommand(undefined);
            setReason("");
            read.refresh();
          }}
        />
      )}
    </>
  );
}
