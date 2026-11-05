import {
  administrationParameter,
  moderationSearch,
} from "./administration-filter-state";
import { commandSummary } from "./workspace";
import { controlIcons } from "../shell/Icon";
import { StatusLabel, stateLabel } from "./operational-labels";
import { DateValue } from "./date-value";
import { documentationGuideLink } from "../api/session";
import { CommandReceipt } from "./authoring";
import { CopyReference } from "./CopyReference";
import * as checks from "./responses";
// Private feedback workflows reuse session-bound transport and exact-key command confirmation.
import { useEffect, useState } from "react";
import { CaseLookup, useCaseNavigation } from "./case-navigation";
import type { components } from "../api/schema";
import { result } from "../api/client";
import { acknowledged } from "./audit-handoff";
import {
  permits,
  availablePermission,
  permitsNamespace,
} from "../api/permissions";
import {
  Field,
  RequiredFields,
  Pager,
  ReadState,
  useRead,
  CommandDialog,
  Heading,
  useUnsaved,
  confirmDiscard,
} from "./workspace";
import type { WorkspaceProps, Command } from "./workspace";

import { useNamespaceAccessState } from "./namespace-access";

type Item = components["schemas"]["Feedback"];
type Category = components["schemas"]["FeedbackCategory"];
type State = components["schemas"]["FeedbackState"];
type Resource = components["schemas"]["Resource"];
type Criteria = { cursor: string; category: Category | ""; state: State | "" };
const emptyCriteria: Criteria = { cursor: "", category: "", state: "" };
function criteriaFromURL(search: string): Criteria {
  return {
    cursor: "",
    category: administrationParameter(search, "category") as Category | "",
    state: administrationParameter(search, "state") as State | "",
  };
}
type Context = {
  resourceId: string;
  releaseId?: string;
  versionRevisionId?: string;
};
const categories: Record<Category, string> = {
  problem: "Problem",
  documentation: "Documentation correction",
  suggestion: "Suggestion",
  positive: "Positive feedback",
  other: "Other",
};

/** Defer private reads until the reader deliberately opens the feedback panel. */
export function FeedbackEntry(
  props: WorkspaceProps & Context & { namespaceId: string },
) {
  return (
    <FeedbackEntryPanel
      key={JSON.stringify([
        props.access.tenantId,
        props.access.realmId,
        props.access.membershipId,
        props.access.subject,
        props.access.actorType,
      ])}
      {...props}
    />
  );
}
function FeedbackEntryPanel(
  props: WorkspaceProps & Context & { namespaceId: string },
) {
  const [open, setOpen] = useState(false);
  const allowed =
    permits(props.access, "feedback.own", "catalog.read") &&
    props.access.actorType === "human";
  const [admitted, setAdmitted] = useState(allowed);
  useEffect(() => {
    if (allowed) setAdmitted(true);
  }, [allowed]);
  if (!allowed && !admitted) return null;
  return (
    <section aria-label="Private item feedback">
      <p className="muted">
        Item feedback is a private report to authorized moderators, separate
        from a human rating.{" "}
        <a href={`${props.runtime.basePath}/my-feedback`}>Open My feedback</a>{" "}
        to revisit your own submissions, including after resource access is
        lost.
      </p>
      <details onToggle={(event) => setOpen(event.currentTarget.open)}>
        <summary
          onClick={(event) => {
            if (open && !confirmDiscard()) event.preventDefault();
          }}
        >
          {controlIcons["message"]}
          Item feedback
        </summary>
        {open && <ReadableFeedback {...props} />}
      </details>
    </section>
  );
}

/** Resolve the selected namespace only after the private panel is opened. */
function ReadableFeedback(
  props: WorkspaceProps & Context & { namespaceId: string },
) {
  const resolved = useNamespaceAccessState(props, props.namespaceId);
  return (
    <>
      <ReadState {...resolved} />
      <FeedbackPanel
        {...props}
        readAllowed={
          props.access.actorType === "human" &&
          permits(props.access, "feedback.own", "catalog.read")
        }
        allowSubmit={
          !resolved.busy &&
          !resolved.error &&
          permitsNamespace(
            resolved.access,
            "resource.read",
            "catalog.read",
            props.namespaceId,
          )
        }
      />
    </>
  );
}

/** Keep existing private submissions reachable without resource read authority. */
export function OwnFeedback(props: WorkspaceProps) {
  return <Feedback {...props} own />;
}

/** Private feedback inventory and moderation have separate permission boundaries. */
export default function Feedback(props: WorkspaceProps & { own?: boolean }) {
  return (
    <FeedbackCases
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
function FeedbackCases(props: WorkspaceProps & { own?: boolean }) {
  const navigation = useCaseNavigation(
    props,
    props.own ? "my-feedback" : "feedback",
    (search) => {
      setCriteria(criteriaFromURL(search));
      setResourceId("");
      setCursor("");
      setFilterEpoch((value) => value + 1);
    },
  );
  const [filterEpoch, setFilterEpoch] = useState(0);
  const [criteria, setCriteria] = useState<Criteria>(() =>
    criteriaFromURL(navigation.listSearch),
  );
  const [knownResources, setKnownResources] = useState<Resource[]>([]);
  const [resourceId, setResourceId] = useState("");
  const [cursor, setCursor] = useState("");
  const policyKey = JSON.stringify(props.access);
  const [listPolicy, setListPolicy] = useState(policyKey);
  if (listPolicy !== policyKey) {
    setListPolicy(policyKey);
    setCriteria(emptyCriteria);
    setCursor("");
  }
  const changeResource = (value: string) => {
    if (value === resourceId) return true;
    if (!confirmDiscard()) return false;
    setResourceId(value);
    setCriteria((previous) => ({ ...previous, cursor: "" }));
    return true;
  };
  const caseReadAllowed = props.own
    ? props.access.actorType === "human" &&
      permits(props.access, "feedback.own", "catalog.read")
    : permits(props.access, "feedback.moderate", "catalog.read");
  const allowed = navigation.id
    ? caseReadAllowed
    : props.own
      ? props.access.actorType === "human" &&
        permits(props.access, "feedback.own", "catalog.read")
      : permits(props.access, "feedback.moderate", "tenant.admin");
  const [admittedCase, setAdmittedCase] = useState<string | undefined>(
    allowed ? navigation.id : undefined,
  );
  const admitted = admittedCase === navigation.id;
  useEffect(() => {
    if (allowed) setAdmittedCase(navigation.id);
  }, [allowed, navigation.id]);
  return (
    <>
      <Heading
        title={
          navigation.id
            ? "Feedback case"
            : props.own
              ? "My feedback"
              : "Feedback inbox"
        }
        icon="message"
        description="Only the submitter and authorized tenant moderators can read feedback. Do not include credentials, personal information or sensitive logs."
        help={(() => {
          const help = documentationGuideLink(
            props.runtime.documentationUrl,
            "feedback",
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
              ? "Return to feedback list"
              : "Open feedback list"}
          </a>
        </p>
      )}
      {!allowed && admitted && (
        <p role="alert" className="notice">
          {navigation.id && !props.own
            ? "Feedback case reads require feedback.moderate and catalog.read scope when enforced."
            : "Current access no longer permits feedback reads or changes (feedback.own or feedback.moderate with the required scope)."}{" "}
          Entered drafts remain protected.{" "}
          <a href={`${props.runtime.basePath}/access`}>Review My access</a>
        </p>
      )}
      {allowed || admitted ? (
        <>
          <FeedbackPanel
            key={`${navigation.id}:${filterEpoch}`}
            onFilter={(filters) =>
              navigation.updateListSearch(moderationSearch(filters))
            }
            resourceFilters={
              !navigation.id ? (
                <>
                  {availablePermission(
                    props.access,
                    "resource.read",
                    "catalog.read",
                  ) && (
                    <ResourceFilter
                      {...props}
                      cursor={cursor}
                      setCursor={setCursor}
                      value={resourceId}
                      onChange={changeResource}
                      onLoad={setKnownResources}
                    />
                  )}
                  <ExactResourceFilter
                    value={resourceId}
                    onChange={changeResource}
                  />
                </>
              ) : undefined
            }
            readAllowed={allowed}
            initialCriteria={criteria}
            onCriteria={navigation.id ? undefined : setCriteria}
            knownResources={knownResources}
            caseId={navigation.id}
            caseRoot={caseReadAllowed ? navigation.root : undefined}
            openCase={navigation.open}
            {...props}
            resourceId={resourceId}
            inbox={!props.own}
            inventory
            allowSubmit={false}
          />
          {!navigation.id && caseReadAllowed && (
            <details className="workspace-advanced">
              <summary>Open a case by ID</summary>
              <CaseLookup kind="feedback" open={navigation.move} />
            </details>
          )}
          {!navigation.id && !caseReadAllowed && (
            <p>
              Opening an exact case also requires catalog.read scope when
              enforced.
            </p>
          )}
        </>
      ) : (
        <p role="alert" className="notice error">
          {props.own
            ? "My feedback requires a human identity with feedback.own and catalog.read scope when enforced."
            : navigation.id
              ? "Feedback case reads require feedback.moderate and catalog.read scope when enforced."
              : "The inbox requires feedback.moderate and tenant.admin scope when enforced."}
          <a href={`${props.runtime.basePath}/access`}>Review My access</a>
        </p>
      )}
    </>
  );
}

/** A retained exact ID is a filter, never authority to read or create resource content. */
function ExactResourceFilter({
  value,
  onChange,
}: {
  value: string;
  onChange: (value: string) => boolean;
}) {
  const [draft, setDraft] = useState(value);
  useEffect(() => setDraft(value), [value]);
  return (
    <details className="workspace-advanced">
      <summary>Enter exact resource ID</summary>
      <form
        className="workspace-toolbar"
        onSubmit={(event) => {
          event.preventDefault();
          onChange(draft.trim().toLowerCase());
        }}
      >
        <Field
          label="Resource ID filter (optional)"
          hint="Leave blank to show all feedback you can access. Resource names need separate access."
        >
          <input
            value={draft}
            maxLength={36}
            onChange={(event) => setDraft(event.target.value)}
            pattern="[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
          />
        </Field>
        <div className="workspace-actions">
          <button type="submit">
            {controlIcons["filters"]} Apply resource filter
          </button>
          <button
            type="button"
            onClick={() => {
              if (onChange("")) setDraft("");
            }}
          >
            {controlIcons["close"]}
            All resources
          </button>
        </div>
      </form>
    </details>
  );
}

/** Page resource choices without loading the tenant's entire resource collection. */
function ResourceFilter(
  props: WorkspaceProps & {
    cursor: string;
    setCursor: (s: string) => void;
    value: string;
    onChange: (s: string) => boolean;
    onLoad: (items: Resource[]) => void;
  },
) {
  const resources = useRead(
    props.cursor,
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/resources", {
          params: {
            query: {
              limit: 20,
              ...(props.cursor ? { cursor: props.cursor } : {}),
            },
          },
          signal,
        }),
        checks.ResourcePage,
      ),
    props.onFailure,
  );
  const { onLoad } = props;
  useEffect(() => {
    onLoad(resources.data?.items ?? []);
  }, [resources.data, onLoad]);
  return (
    <div>
      <Field label="Resource filter">
        <select
          value={props.value}
          onChange={(e) => props.onChange(e.target.value)}
        >
          <option value="">All resources</option>
          {props.value &&
            !resources.data?.items.some((r) => r.id === props.value) && (
              <option value={props.value}>
                Selected resource (another page)
              </option>
            )}
          {resources.data?.items.map((r) => (
            <option key={r.id} value={r.id}>
              {r.canonicalIdentifier}
            </option>
          ))}
        </select>
      </Field>
      <ReadState
        {...resources}
        refresh={() =>
          resources.error && props.cursor
            ? props.setCursor("")
            : resources.refresh()
        }
      />

      <Pager
        label="Feedback Resource choices pagination"
        cursor={props.cursor}
        nextCursor={resources.data?.nextCursor}
        busy={resources.busy}
        onPage={props.setCursor}
      />
    </div>
  );
}

/** Keep unchanged row drafts during a same-scope reread; explicit replacement asks first. */
function FeedbackPanel(
  props: WorkspaceProps &
    Partial<Context> & {
      inbox?: boolean;
      inventory?: boolean;
      allowSubmit?: boolean;
      own?: boolean;
      caseId?: string;
      caseRoot?: string | undefined;
      openCase?: (
        event: React.MouseEvent<HTMLAnchorElement>,
        id: string,
      ) => void;
      knownResources?: Resource[];
      resourceFilters?: React.ReactNode;
      readAllowed?: boolean;
      initialCriteria?: Criteria;
      onCriteria?: ((criteria: Criteria) => void) | undefined;
      onFilter?: (filters: { state: string; category: string }) => void;
    },
) {
  const [cursor, setCursor] = useState(props.initialCriteria?.cursor ?? "");
  const [category, setCategory] = useState<Category | "">(
    props.initialCriteria?.category ?? "",
  );
  const [state, setState] = useState<State | "">(
    props.initialCriteria?.state ?? "",
  );
  const { onCriteria } = props;
  useEffect(() => {
    onCriteria?.({ cursor, category, state });
  }, [cursor, category, state, onCriteria]);
  const [generation, setGeneration] = useState(0);
  const [submissionGeneration, setSubmissionGeneration] = useState(0);
  const [newSubmission, setNewSubmission] = useState(false);
  const [commandOwn, setCommandOwn] = useState(false);
  const [command, setCommand] = useState<Command>();
  const [message, setMessage] = useState("");
  const [acknowledgements, setAcknowledgements] = useState(0);
  const [selectedResource, setSelectedResource] = useState(props.resourceId);
  if (selectedResource !== props.resourceId) {
    // Resource replacement was already confirmed by the owning filter.
    setSelectedResource(props.resourceId);
    setCursor("");
    setGeneration((value) => value + 1);
    setSubmissionGeneration((value) => value + 1);
  }
  const currentPolicy = JSON.stringify(props.access);
  const [pagePolicy, setPagePolicy] = useState(currentPolicy);
  const page = useRead(
    JSON.stringify(
      props.caseId
        ? [props.caseId]
        : [props.resourceId, category, state, cursor, pagePolicy],
    ),
    async (signal) =>
      props.readAllowed === false
        ? { items: [] }
        : props.caseId
          ? {
              items: [
                result(
                  await props.client.api.GET("/api/v1/feedback/{feedbackId}", {
                    params: { path: { feedbackId: props.caseId } },
                    signal,
                  }),
                  (value) =>
                    checks.Feedback(value) &&
                    (value as Item).id.toLowerCase() === props.caseId &&
                    (!props.own ||
                      (value as Item).membershipId ===
                        props.access.membershipId),
                ),
              ],
            }
          : result(
              await props.client.api.GET(
                props.inbox ? "/api/v1/feedback/inbox" : "/api/v1/feedback",
                {
                  params: {
                    query: {
                      ...(props.resourceId
                        ? { resourceId: props.resourceId }
                        : {}),
                      ...(category ? { category: category } : {}),
                      ...(state ? { state: state } : {}),
                      limit: 20,
                      ...(cursor ? { cursor: cursor } : {}),
                    },
                  },
                  signal,
                },
              ),
              checks.FeedbackPage,
            ),
    props.onFailure,
    !props.caseId,
  );
  const discardPage = () => {
    if (!confirmDiscard()) return false;
    setGeneration((g) => g + 1);
    setSubmissionGeneration((g) => g + 1);
    return true;
  };
  const refresh = () => {
    if (props.readAllowed === false || !discardPage()) return;
    if (currentPolicy !== pagePolicy) {
      setPagePolicy(currentPolicy);
      setCursor("");
      if (props.caseId || !cursor) page.refresh();
    } else if (page.error && cursor) setCursor("");
    else page.refresh();
  };
  const write =
    props.access.actorType === "human" &&
    permits(props.access, "feedback.own", "feedback.write");
  const [admittedWrite, setAdmittedWrite] = useState(
    write && props.allowSubmit !== false,
  );
  useEffect(() => {
    if (write && props.allowSubmit !== false) setAdmittedWrite(true);
  }, [write, props.allowSubmit]);
  const resourceId = props.resourceId;
  return (
    <section
      className="workspace-panel"
      aria-label={
        props.inbox
          ? "Feedback inbox records"
          : props.inventory
            ? "Your submissions"
            : "Your item feedback"
      }
    >
      {(props.inbox || props.caseId) && (
        <h2>{props.caseId ? "Current feedback" : "Feedback cases"}</h2>
      )}
      {!props.inbox && !props.caseId && (
        <>
          <h2>{props.inventory ? "Your submissions" : "Your item feedback"}</h2>
          {!props.inventory && (
            <p>
              Only you and authorized tenant moderators can read your feedback.
              Do not include credentials, personal information or sensitive
              logs. Feedback does not change publication approval.
            </p>
          )}
          {resourceId &&
          ((write && props.allowSubmit !== false) || admittedWrite) ? (
            <>
              <p>
                {props.releaseId
                  ? "New feedback refers to this exact release."
                  : props.versionRevisionId
                    ? "New feedback refers to this exact version revision."
                    : "New feedback is about the resource generally, not a specific version."}
              </p>
              <FeedbackForm
                key={submissionGeneration}
                title="Submit feedback"
                editable={
                  write &&
                  props.allowSubmit !== false &&
                  props.readAllowed !== false
                }
                prepare={(category, comment) => {
                  setNewSubmission(true);
                  setCommand({
                    title: "Submit feedback",
                    intent: "primary",
                    icon: "message",
                    description:
                      "Send this private feedback to authorized tenant moderators?",
                    summary: commandSummary([
                      ["Resource ID", resourceId],
                      ["Release ID", props.releaseId ?? "None"],
                      [
                        "Version revision",
                        props.versionRevisionId ?? "General resource feedback",
                      ],
                      ["Category", category],
                    ]),
                    run: async (key) => {
                      const reply = await props.client.api.POST(
                        "/api/v1/feedback",
                        {
                          params: { header: { "Idempotency-Key": key } },
                          body: {
                            resourceId,
                            ...(props.releaseId === undefined
                              ? {}
                              : { releaseId: props.releaseId }),
                            ...(props.versionRevisionId === undefined
                              ? {}
                              : { versionRevisionId: props.versionRevisionId }),
                            category,
                            comment,
                          },
                        },
                      );
                      const saved = result(reply, checks.FeedbackReceipt);
                      return acknowledged(
                        "Feedback submitted.",
                        reply.response,
                        {
                          action: "feedback.created",
                          targetType: "feedback",
                          targetId: saved.id,
                          revision: saved.revision,
                        },
                      );
                    },
                  });
                }}
              />
            </>
          ) : (
            <>
              <p>
                {props.inventory
                  ? "To send feedback, open a resource you can access and choose Item feedback."
                  : "To submit feedback, you need access to this resource and permission to send feedback."}
              </p>
              {props.inventory && (
                <p>
                  Your existing submissions remain available here after resource
                  access changes.
                </p>
              )}
              <details>
                <summary>Feedback access</summary>
                <p>
                  Reading your submissions requires a human identity with
                  feedback.own permission and catalog.read scope when enforced.
                  Sending or editing feedback also requires feedback.write scope
                  when enforced. New submissions require resource.read
                  permission for the resource; existing submissions do not.
                </p>
              </details>
            </>
          )}
        </>
      )}
      <CommandReceipt key={acknowledgements} message={message} />
      {!props.caseId && (
        <section className="feedback-filters" aria-label="Feedback filters">
          {!props.inventory && resourceId && <h3>Submitted feedback</h3>}
          <fieldset
            className="workspace-fields"
            disabled={props.readAllowed === false}
          >
            {props.resourceFilters}
          </fieldset>
          <div className="workspace-toolbar feedback-toolbar">
            {!props.caseId && (
              <>
                <Field label="Feedback category">
                  <select
                    disabled={props.readAllowed === false}
                    value={category}
                    onChange={(e) => {
                      if (!discardPage()) return;
                      setCategory(e.target.value as Category | "");
                      props.onFilter?.({ state, category: e.target.value });
                      setCursor("");
                    }}
                  >
                    <option value="">All categories</option>
                    {category && !(category in categories) && (
                      <option value={category}>Unsupported category</option>
                    )}
                    {Object.entries(categories).map(([key, label]) => (
                      <option key={key} value={key}>
                        {label}
                      </option>
                    ))}
                  </select>
                </Field>
                <Field label="Feedback state">
                  <select
                    disabled={props.readAllowed === false}
                    value={state}
                    onChange={(e) => {
                      if (!discardPage()) return;
                      setState(e.target.value as State | "");
                      props.onFilter?.({ state: e.target.value, category });
                      setCursor("");
                    }}
                  >
                    <option value="">All states</option>
                    {![
                      "",
                      "open",
                      "addressed",
                      "withdrawn",
                      "removed",
                    ].includes(state) && (
                      <option value={state}>Unsupported state</option>
                    )}
                    {["open", "addressed", "withdrawn", "removed"].map(
                      (value) => (
                        <option key={value} value={value}>
                          {stateLabel(value)}
                        </option>
                      ),
                    )}
                  </select>
                </Field>
              </>
            )}
            <button
              disabled={page.busy || !!command || props.readAllowed === false}
              onClick={refresh}
            >
              {controlIcons["refresh"]}
              {props.caseId ? "Read current feedback" : "Refresh feedback"}
            </button>
          </div>
          <p className="muted">
            Applied filters:{" "}
            {resourceId ? `resource ${resourceId}` : "all resources"};{" "}
            {category ? categories[category] : "all categories"};{" "}
            {state ? stateLabel(state) : "all states"}.
          </p>
        </section>
      )}
      {props.caseId && (
        <button
          disabled={page.busy || !!command || props.readAllowed === false}
          onClick={refresh}
        >
          {controlIcons["refresh"]}
          Read current feedback
        </button>
      )}
      <ReadState {...page} refresh={refresh} />
      {page.data && (
        <>
          {(page.busy || page.error) && (
            <p role="status">
              Last loaded feedback is shown until the current page is read
              successfully.
            </p>
          )}
          {page.data.items.length === 0 && (
            <p>
              {props.inventory &&
              !props.inbox &&
              !resourceId &&
              !category &&
              !state
                ? "You have not sent feedback yet. Open a resource you can access and choose Item feedback."
                : "No feedback matches these filters."}
            </p>
          )}
          {page.data.items.map((item) =>
            props.inventory && !props.caseId ? (
              <div className="case-summary" key={item.id}>
                <details className="workspace-advanced">
                  <summary>
                    {props.readAllowed === false
                      ? "Retained feedback draft"
                      : categories[item.category]}{" "}
                    ·{" "}
                    <StatusLabel
                      state={item.state}
                      tone={item.state === "open" ? "info" : "neutral"}
                    />{" "}
                    · <DateValue value={item.createdAt} />
                    {props.readAllowed !== false &&
                    item.state !== "withdrawn" &&
                    item.state !== "removed"
                      ? ` — ${Array.from(item.comment ?? "")
                          .slice(0, 180)
                          .join(
                            "",
                          )}${Array.from(item.comment ?? "").length > 180 ? "…" : ""}`
                      : ""}
                  </summary>
                  <FeedbackItem
                    key={`${item.id}/${item.revision}/${generation}`}
                    {...props}
                    item={item}
                    write={write}
                    knownResource={props.knownResources?.find(
                      (resource) => resource.id === item.resourceId,
                    )}
                    prepare={(next, own) => {
                      setCommandOwn(!!own);
                      setNewSubmission(false);
                      setCommand(next);
                    }}
                  />
                </details>
                {props.caseRoot && props.readAllowed !== false && (
                  <a
                    href={`${props.caseRoot}/${encodeURIComponent(item.id)}`}
                    onClick={(event) => props.openCase?.(event, item.id)}
                  >
                    {controlIcons["arrow"]}
                    Open feedback case
                  </a>
                )}
              </div>
            ) : (
              <FeedbackItem
                key={`${item.id}/${item.revision}/${generation}`}
                {...props}
                item={item}
                write={write}
                knownResource={props.knownResources?.find(
                  (resource) => resource.id === item.resourceId,
                )}
                prepare={(next, own) => {
                  setCommandOwn(!!own);
                  setNewSubmission(false);
                  setCommand(next);
                }}
              />
            ),
          )}
        </>
      )}
      {!props.caseId && (
        <Pager
          label="Feedback cases pagination"
          cursor={cursor}
          nextCursor={page.data?.nextCursor}
          busy={page.busy || props.readAllowed === false}
          onPage={(next) => {
            if (props.readAllowed === false || !discardPage()) return false;
            if (currentPolicy !== pagePolicy) {
              setPagePolicy(currentPolicy);
              setCursor("");
            } else setCursor(next);
          }}
        />
      )}
      {command && (
        <CommandDialog
          allowed={
            props.readAllowed !== false &&
            !page.busy &&
            !page.error &&
            (newSubmission
              ? write && props.allowSubmit !== false
              : commandOwn
                ? write
                : permits(props.access, "feedback.moderate", "tenant.admin"))
          }
          command={command}
          onFailure={props.onFailure}
          onClose={() => setCommand(undefined)}
          onDone={(text) => {
            setCommand(undefined);
            setMessage(text);
            setAcknowledgements((v) => v + 1);
            if (newSubmission) setSubmissionGeneration((g) => g + 1);
            page.refresh();
          }}
        />
      )}
    </section>
  );
}

/** Present retained context and one current response without interpreting text as markup. */
function FeedbackItem(
  props: WorkspaceProps & {
    item: Item;
    write: boolean;
    readAllowed?: boolean;
    knownResource?: Resource | undefined;
    prepare: (c: Command, own?: boolean) => void;
  },
) {
  const { item } = props;
  const [response, setResponse] = useState(item.response || "");
  const [editGeneration, setEditGeneration] = useState(0);
  useUnsaved(response !== (item.response || ""));
  const admin = permits(props.access, "feedback.moderate", "tenant.admin");
  const [admittedAdmin, setAdmittedAdmin] = useState(admin);
  const ownEditor =
    props.write && item.membershipId === props.access.membershipId;
  const [admittedOwnEditor, setAdmittedOwnEditor] = useState(ownEditor);
  useEffect(() => {
    if (admin) setAdmittedAdmin(true);
  }, [admin]);
  useEffect(() => {
    if (ownEditor) setAdmittedOwnEditor(true);
  }, [ownEditor]);
  const terminal = item.state === "withdrawn" || item.state === "removed";
  const act = (action: "address" | "reopen" | "remove" | "withdraw") => {
    const body = action === "address" ? { response } : {};
    props.prepare(
      {
        title: `${action.charAt(0).toUpperCase()}${action.slice(1)} feedback`,
        intent:
          action === "remove" || action === "withdraw" ? "danger" : "primary",
        icon:
          action === "remove" || action === "withdraw"
            ? "remove"
            : action === "address"
              ? "message"
              : "refresh",
        description:
          action === "remove" || action === "withdraw"
            ? "Clear the comment and response permanently from current feedback. This feedback cannot be reopened. Its identifying context and audit records remain. Backups and earlier authorized copies are separate."
            : action === "address"
              ? "Mark this feedback addressed and replace its current moderator response. The submitter's comment stays unchanged. Addressed does not confirm a fix."
              : "Reopen this feedback and clear its current moderator response. The submitter's comment stays unchanged.",
        summary: commandSummary([
          ["Action", action],
          ["Feedback ID", item.id],
          ["Resource ID", item.resourceId],
          [
            "Version revision",
            item.versionRevisionId ?? "General resource feedback",
          ],
          ["Revision", `"rev-${item.revision}"`],
          ...(action === "address"
            ? ([["Moderator response", response || "Empty response"]] as const)
            : ([] as const)),
        ]),
        run: async (key) => {
          const reply = await props.client.api.POST(
            `/api/v1/feedback/{feedbackId}:${action}`,
            {
              params: {
                path: { feedbackId: item.id },
                header: {
                  "Idempotency-Key": key,
                  "If-Match": `"rev-${item.revision}"`,
                },
              },
              body,
            },
          );
          const saved = result(reply, checks.FeedbackReceipt);
          return acknowledged("Feedback updated.", reply.response, {
            action: `feedback.${action}`,
            targetType: "feedback",
            targetId: item.id,
            revision: saved.revision,
          });
        },
      },
      action === "withdraw",
    );
  };
  const identifiers = props.readAllowed !== false && (
    <details className="workspace-advanced">
      <summary>Identifiers and revision</summary>
      <CopyReference label="feedback ID" value={item.id} />
      <CopyReference label="feedback revision" value={String(item.revision)} />
      <CopyReference label="Resource ID" value={item.resourceId} />
      {item.releaseId && (
        <CopyReference label="release ID" value={item.releaseId} />
      )}
      {item.versionRevisionId && (
        <CopyReference
          label="version revision ID"
          value={item.versionRevisionId}
        />
      )}
      {(admin ||
        permits(props.access, "feedback.moderate", "catalog.read")) && (
        <CopyReference
          label="submitter membership ID"
          value={item.membershipId}
        />
      )}
      <FeedbackResourceLinks {...props} />
      <p>
        {item.versionRevisionId ? (
          <>
            Exact version revision: <code>{item.versionRevisionId}</code>
          </>
        ) : (
          "General resource feedback"
        )}
      </p>
    </details>
  );
  return (
    <article className="workspace-panel">
      {props.readAllowed !== false && (
        <>
          <h3>
            {categories[item.category]}{" "}
            <StatusLabel
              state={item.state}
              tone={item.state === "open" ? "info" : "neutral"}
            />
          </h3>
          <p>
            Submitted{" "}
            <time dateTime={item.createdAt}>
              {new Date(item.createdAt).toLocaleString()}
            </time>
          </p>
          <p>
            Updated{" "}
            <time dateTime={item.updatedAt}>
              {new Date(item.updatedAt).toLocaleString()}
            </time>
          </p>
        </>
      )}
      {terminal ? (
        <>
          <p>Comment and response have been cleared.</p>
          {identifiers}
        </>
      ) : (
        <>
          {props.readAllowed !== false && (
            <>
              <p style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>
                {item.comment}
              </p>
              {item.response && (
                <>
                  <h4>Moderator response</h4>
                  <p
                    style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}
                  >
                    {item.response}
                  </p>
                </>
              )}
            </>
          )}
          {identifiers}
          {(ownEditor || admittedOwnEditor) && (
            <details>
              <summary
                onClick={(event) => {
                  if (!event.currentTarget.closest("details")?.open) return;
                  if (!confirmDiscard()) event.preventDefault();
                  else setEditGeneration((g) => g + 1);
                }}
              >
                {controlIcons["edit"]}
                Edit or withdraw your feedback
              </summary>
              <FeedbackForm
                key={editGeneration}
                title="Save feedback changes"
                editable={ownEditor && props.readAllowed !== false}
                item={item}
                prepare={(category, comment) =>
                  props.prepare(
                    {
                      title: "Save feedback changes",
                      intent: "primary",
                      icon: "save",
                      description:
                        "Editing reopens this feedback and clears the previous response.",
                      summary: commandSummary([
                        ["Feedback ID", item.id],
                        ["Resource ID", item.resourceId],
                        [
                          "Version revision",
                          item.versionRevisionId ?? "General resource feedback",
                        ],
                        ["Revision", `"rev-${item.revision}"`],
                        ["Category", category],
                      ]),
                      run: async (key) => {
                        const reply = await props.client.api.PATCH(
                          "/api/v1/feedback/{feedbackId}",
                          {
                            params: {
                              path: { feedbackId: item.id },
                              header: {
                                "Idempotency-Key": key,
                                "If-Match": `"rev-${item.revision}"`,
                              },
                            },
                            body: { category, comment },
                          },
                        );
                        const saved = result(reply, checks.FeedbackReceipt);
                        return acknowledged(
                          "Feedback edited and reopened.",
                          reply.response,
                          {
                            action: "feedback.edit",
                            targetType: "feedback",
                            targetId: item.id,
                            revision: saved.revision,
                          },
                        );
                      },
                    },
                    true,
                  )
                }
              />
              <button
                className="danger"
                disabled={!ownEditor || props.readAllowed === false}
                onClick={() => {
                  if (ownEditor && props.readAllowed !== false) act("withdraw");
                }}
              >
                {controlIcons["remove"]}
                Withdraw feedback
              </button>
            </details>
          )}
          {(admin || (admittedAdmin && response !== (item.response || ""))) && (
            <details>
              <summary
                onClick={(event) => {
                  if (!event.currentTarget.closest("details")?.open) return;
                  if (!confirmDiscard()) event.preventDefault();
                  else setResponse(item.response || "");
                }}
              >
                {controlIcons["message"]}
                Respond or manage feedback
              </summary>
              <fieldset
                className="workspace-fields"
                disabled={!admin || props.readAllowed === false}
              >
                <Field label="Moderator response (optional)">
                  <textarea
                    value={response}
                    onChange={(e) => setResponse(e.target.value)}
                    maxLength={8000}
                  />
                </Field>
                <p>
                  {Array.from(response).length} / 4,000 characters. One current
                  plain-text response. Addressing can have no response;
                  reopening clears it.
                </p>
                <div className="form-actions">
                  <button
                    disabled={Array.from(response).length > 4000}
                    onClick={() => {
                      if (admin && props.readAllowed !== false) act("address");
                    }}
                  >
                    {controlIcons["message"]}
                    Address feedback
                  </button>
                  {item.state === "addressed" && (
                    <button onClick={() => act("reopen")}>
                      {controlIcons["refresh"]}
                      Reopen feedback
                    </button>
                  )}
                  <button className="danger" onClick={() => act("remove")}>
                    {controlIcons["remove"]} Remove feedback
                  </button>
                </div>
              </fieldset>
            </details>
          )}
        </>
      )}
    </article>
  );
}

/** Resolve target metadata only on request; retained feedback itself never needs that read. */
function FeedbackResourceLinks(
  props: WorkspaceProps & { item: Item; knownResource?: Resource | undefined },
) {
  const [open, setOpen] = useState(false);
  const read = useRead(
    `${props.item.resourceId}/${open}`,
    async (signal) =>
      !open
        ? undefined
        : result(
            await props.client.api.GET("/api/v1/resources/{resourceId}", {
              params: { path: { resourceId: props.item.resourceId } },
              signal,
            }),
            checks.Resource,
          ),
    props.onFailure,
  );
  const resource = read.data ?? props.knownResource;
  const resolved = useNamespaceAccessState(
    props,
    open ? (resource?.namespaceId ?? "") : "",
  );
  const metadata = resource
    ? permitsNamespace(
        resolved.access,
        "resource.read",
        "catalog.read",
        resource.namespaceId,
      )
    : permits(props.access, "resource.read", "catalog.read");
  const history = resource
    ? permitsNamespace(
        resolved.access,
        "resource.read_history",
        "catalog.read",
        resource.namespaceId,
      )
    : permits(props.access, "resource.read_history", "catalog.read");
  return (
    <>
      {resource && (
        <p>
          Resource identifier: <code>{resource.canonicalIdentifier}</code>
        </p>
      )}
      {metadata && (
        <p>
          <a
            href={`${props.runtime.basePath}/resources/${props.item.resourceId}`}
          >
            Resource and versions
          </a>
        </p>
      )}
      {history && props.item.releaseId && (
        <p>
          <a
            href={`${props.runtime.basePath}/releases/${props.item.releaseId}`}
          >
            Original release
          </a>
        </p>
      )}
      {!metadata &&
        availablePermission(props.access, "resource.read", "catalog.read") && (
          <button onClick={() => setOpen(true)} disabled={open}>
            {controlIcons["book"]}
            Read resource context
          </button>
        )}
      {open && <ReadState {...read} />}
      {resource && <ReadState {...resolved} />}
    </>
  );
}

/** Validate plain-text input locally; the API remains the authoritative validator. */
function FeedbackForm({
  title,
  item,
  prepare,
  editable = true,
}: {
  editable?: boolean;
  title: string;
  item?: Item;
  prepare: (category: Category, comment: string) => void;
}) {
  const [category, setCategory] = useState<Category>(
    item?.category || "problem",
  );
  const [comment, setComment] = useState(item?.comment || "");
  useUnsaved(
    category !== (item?.category || "problem") ||
      comment !== (item?.comment || ""),
  );
  const characters = Array.from(comment).length;
  const valid = !!comment.trim() && characters <= 4000;
  if (
    !editable &&
    category === (item?.category || "problem") &&
    comment === (item?.comment || "")
  )
    return null;
  return (
    <form
      className="workspace-form"
      onSubmit={(e) => {
        e.preventDefault();
        if (valid && editable) prepare(category, comment);
      }}
    >
      <RequiredFields>
        <fieldset className="workspace-fields" disabled={!editable}>
          <Field label="Category">
            <select
              value={category}
              onChange={(e) => setCategory(e.target.value as Category)}
            >
              {Object.entries(categories).map(([value, label]) => (
                <option key={value} value={value}>
                  {label}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Feedback comment">
            <textarea
              required
              value={comment}
              maxLength={8000}
              onChange={(e) => setComment(e.target.value)}
            />
          </Field>
          <p>{characters} / 4,000 characters. Plain text only.</p>
        </fieldset>
        <div className="form-actions">
          <button
            className="primary"
            type="submit"
            disabled={!valid || !editable}
          >
            {controlIcons["message"]}
            {title}
          </button>
        </div>
      </RequiredFields>
    </form>
  );
}
