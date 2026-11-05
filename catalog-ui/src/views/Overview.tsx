import { useEffect, useState } from "react";
import type { components } from "../api/schema";
import { canReadLocal, result } from "../api/client";
import { availablePermission, canReadJobs, permits } from "../api/permissions";
import { kindChoices } from "../resource-kinds";
import { KindBadge } from "./KindBadge";
import { Heading, ReadState, useRead, type WorkspaceProps } from "./workspace";
import * as checks from "./overview-values";
import "./overview.css";
import { Panel, Observation } from "./overview-presentation";
import ReviewOverview from "./ReviewOverview";
import PublicationOverview from "./PublicationOverview";
import OperationsOverview from "./OperationsOverview";
import AdministrationOverview from "./AdministrationOverview";
import ActivityOverview from "./ActivityOverview";
import { Capabilities } from "./responses";
import type { CapabilityRead } from "./WebhookOverview";

type Count = components["schemas"]["OverviewBoundedCount"];
const countText = (count: Count) =>
  `${count.value.toLocaleString()}${count.limited ? "+" : ""}`;

/** Remount private summaries whenever the current permission projection changes. */
export default function Overview(props: WorkspaceProps) {
  const [activityPaused, setActivityPaused] = useState(false);
  const options = {
    ...props,
    activityPaused,
    onActivityPause: setActivityPaused,
  };
  return permits(props.access, "access.manage", "tenant.admin") ? (
    <OverviewActivityGate key={JSON.stringify(props.access)} {...options} />
  ) : (
    <OverviewContent key={JSON.stringify(props.access)} {...options} />
  );
}

type OverviewProps = WorkspaceProps & {
  activityPaused: boolean;
  onActivityPause: (paused: boolean) => void;
  activityCapabilities?: CapabilityRead | undefined;
  capabilitiesReceivedAt?: string | undefined;
};

function OverviewActivityGate(props: OverviewProps) {
  const [receivedAt, setReceivedAt] = useState<string>();
  const capabilities = useRead(
    "overview-capabilities",
    async (signal) => {
      const value = result(
        await props.client.api.GET("/api/v1/capabilities", { signal }),
        Capabilities,
      );
      if (
        value.optionalCapabilities.serviceActivity !== "enabled" &&
        value.optionalCapabilities.serviceActivity !== "disabled"
      )
        throw new Error("Invalid activity availability");
      if (!signal.aborted) setReceivedAt(new Date().toISOString());
      return value;
    },
    props.onFailure,
  );
  return (
    <OverviewContent
      {...props}
      activityCapabilities={capabilities}
      capabilitiesReceivedAt={receivedAt}
    />
  );
}

function OverviewContent(props: OverviewProps) {
  const [search, setSearch] = useState(location.search);
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    const moved = () => setSearch(location.search);
    window.addEventListener("popstate", moved);
    return () => window.removeEventListener("popstate", moved);
  }, []);
  const selections = new URLSearchParams(search).getAll("view");
  const local = availablePermission(
    props.access,
    "resource.read",
    "catalog.read",
  );
  const releases = availablePermission(
    props.access,
    "resource.read_history",
    "catalog.read",
  );
  const namespaces = availablePermission(
    props.access,
    "namespace.read",
    "catalog.read",
  );
  const publishers = permits(props.access, "publisher.read", "publishers.read");
  const indexed = permits(props.access, "federation.discover", "catalog.read");
  const publication = permits(props.access, "publication.read", "catalog.read");
  const contentAllowed =
    local || releases || namespaces || publishers || indexed || publication;
  const operationsAllowed =
    permits(props.access, "source.read", "sources.read") ||
    permits(props.access, "webhook.read", "webhooks.manage") ||
    canReadJobs(props.access) ||
    permits(props.access, "operations.reindex", "tenant.admin");
  const administrationAllowed = [
    "access.manage",
    "feedback.moderate",
    "ratings.moderate",
  ].some((permission) =>
    permits(
      props.access,
      permission as "access.manage" | "feedback.moderate" | "ratings.moderate",
      "tenant.admin",
    ),
  );
  const activityAllowed =
    permits(props.access, "access.manage", "tenant.admin") &&
    props.activityCapabilities?.data?.optionalCapabilities.serviceActivity ===
      "enabled";
  const allowed =
    contentAllowed ||
    operationsAllowed ||
    administrationAllowed ||
    activityAllowed;
  const view =
    selections.length === 0
      ? contentAllowed
        ? "content"
        : operationsAllowed
          ? "operations"
          : administrationAllowed
            ? "administration"
            : "activity"
      : selections[0];
  const validView =
    selections.length <= 1 &&
    ((view === "activity" && activityAllowed) ||
      (view === "content" && contentAllowed) ||
      (view === "operations" && operationsAllowed) ||
      (view === "administration" && administrationAllowed) ||
      (!allowed && selections.length === 0));
  return (
    <>
      <Heading title="Overview" icon="catalog" description="">
        {allowed && validView && view !== "activity" && (
          <button
            onClick={() => {
              setRevision((r) => r + 1);
              props.activityCapabilities?.refresh();
            }}
          >
            Refresh
          </button>
        )}
      </Heading>
      {!validView ? (
        <div>
          {view === "activity" &&
          selections.length === 1 &&
          props.activityCapabilities?.busy ? (
            <p role="status">Checking Activity availability...</p>
          ) : (
            <p role="alert">
              This Overview tab is unavailable.{" "}
              <a href={`${props.runtime.basePath}/overview`}>Open Overview</a>.
            </p>
          )}
          {view === "activity" &&
            selections.length === 1 &&
            props.activityCapabilities?.error && (
              <ReadState {...props.activityCapabilities} />
            )}
        </div>
      ) : !allowed ? (
        <p role="alert">
          Your access does not include Overview.{" "}
          <a href={`${props.runtime.basePath}/access`}>View My access</a>.
        </p>
      ) : (
        <>
          <nav aria-label="Overview views" className="overview-tabs">
            {contentAllowed && (
              <a
                href={`${props.runtime.basePath}/overview?view=content`}
                aria-current={view === "content" ? "page" : undefined}
              >
                Content
              </a>
            )}
            {operationsAllowed && (
              <a
                href={`${props.runtime.basePath}/overview?view=operations`}
                aria-current={view === "operations" ? "page" : undefined}
              >
                Operations
              </a>
            )}
            {administrationAllowed && (
              <a
                href={`${props.runtime.basePath}/overview?view=administration`}
                aria-current={view === "administration" ? "page" : undefined}
              >
                Administration
              </a>
            )}
            {activityAllowed && (
              <a
                href={`${props.runtime.basePath}/overview?view=activity`}
                aria-current={view === "activity" ? "page" : undefined}
              >
                Activity
              </a>
            )}
          </nav>
          <div
            className={view === "activity" ? "" : "overview-grid"}
            key={`${view}:${revision}`}
          >
            {view === "activity" ? (
              <ActivityOverview
                {...props}
                startPaused={props.activityPaused}
                onPause={props.onActivityPause}
              />
            ) : view === "administration" ? (
              <AdministrationOverview {...props} />
            ) : view === "operations" ? (
              <OperationsOverview
                {...props}
                sharedCapabilities={props.activityCapabilities}
                sharedReceivedAt={props.capabilitiesReceivedAt}
              />
            ) : (
              <>
                {local && <Resources {...props} />}
                {releases && <Releases {...props} />}
                {indexed && <Indexed {...props} />}
                {namespaces && <Directory {...props} type="namespaces" />}
                {publishers && <Directory {...props} type="publishers" />}
                {releases && <ReviewOverview {...props} />}
                {publication && <PublicationOverview {...props} />}
              </>
            )}
          </div>
        </>
      )}
    </>
  );
}

function Resources(props: WorkspaceProps) {
  const read = useRead(
    "resources",
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/overview/resources", { signal }),
        (value) => checks.resourceOverview(value, props.access.tenantId),
      ),
    props.onFailure,
  );
  const data = read.data;
  return (
    <Panel title="Local resources" wide value={data && countText(data.total)}>
      <ReadState {...read} />
      {data && (
        <>
          {data.total.limited ? (
            <p>
              More than 10,000 resources. The kind breakdown is unavailable.
            </p>
          ) : (
            <dl className="overview-kinds">
              {kindChoices.map((kind) => (
                <div className={`overview-kind ${kind.value}`} key={kind.value}>
                  <dt>
                    <a
                      href={`${props.runtime.basePath}/resources?kind=${kind.value}`}
                    >
                      <KindBadge kind={kind.value} />
                    </a>
                  </dt>
                  <dd>
                    {data
                      .kinds!.find((k) => k.kind === kind.value)!
                      .count.toLocaleString()}
                  </dd>
                </div>
              ))}
            </dl>
          )}
          <Observation data={data} />
        </>
      )}
    </Panel>
  );
}

function Releases(props: WorkspaceProps) {
  const read = useRead(
    "releases",
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/overview/releases", { signal }),
        (value) => checks.releaseOverview(value, props.access.tenantId),
      ),
    props.onFailure,
  );
  return (
    <Panel title="Current releases" compact>
      <ReadState {...read} />
      {read.data && (
        <>
          <dl className="overview-facts">
            <div>
              <dt>Published</dt>
              <dd>{countText(read.data.published)}</dd>
            </div>
            <div>
              <dt>Deprecated</dt>
              <dd>{countText(read.data.deprecated)}</dd>
            </div>
          </dl>
          {(read.data.published.limited || read.data.deprecated.limited) && (
            <p>Counts marked + exceed 10,000.</p>
          )}
          {canReadLocal(props.access) && (
            <p>
              <a href={`${props.runtime.basePath}/explore`}>
                Browse released content
              </a>
            </p>
          )}
          <Observation data={read.data} />
        </>
      )}
    </Panel>
  );
}

function Directory(
  props: WorkspaceProps & { type: "namespaces" | "publishers" },
) {
  const title = props.type === "namespaces" ? "Namespaces" : "Publishers";
  const read = useRead(
    props.type,
    async (signal) =>
      result(
        await props.client.api.GET(
          props.type === "namespaces"
            ? "/api/v1/overview/namespaces"
            : "/api/v1/overview/publishers",
          { signal },
        ),
        (value) => checks.directoryOverview(value, props.access.tenantId),
      ),
    props.onFailure,
  );
  return (
    <Panel
      title={title}
      compact
      value={read.data && countText(read.data.count)}
    >
      <ReadState {...read} />
      {read.data && (
        <>
          {read.data.count.limited && <p>More than 10,000 {props.type}.</p>}
          <p>
            <a href={`${props.runtime.basePath}/${props.type}`}>
              Manage {title.toLowerCase()}
            </a>
          </p>
          <Observation data={read.data} />
        </>
      )}
    </Panel>
  );
}

function Indexed(props: WorkspaceProps) {
  const read = useRead(
    "indexed",
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/overview/indexed", { signal }),
        (value) => checks.indexedOverview(value, props.access.tenantId),
      ),
    props.onFailure,
  );
  return (
    <Panel
      title="External records"
      compact
      value={
        read.data && !read.data.limited
          ? read.data.count!.toLocaleString()
          : undefined
      }
    >
      <ReadState {...read} />
      {read.data && (
        <>
          {read.data.limited ? (
            <p>
              Count unavailable. More than 10,000 source entries need to be
              checked.
            </p>
          ) : (
            <span className="overview-qualifier">Indexed</span>
          )}
          <p>
            <a href={`${props.runtime.basePath}/catalog?origin=indexed`}>
              Browse indexed records
            </a>
          </p>
          <Observation data={read.data} />
        </>
      )}
    </Panel>
  );
}
