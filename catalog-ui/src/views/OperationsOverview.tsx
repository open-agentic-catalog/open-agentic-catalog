import { useState } from "react";
import { result } from "../api/client";
import { canReadJobs, permits } from "../api/permissions";
import { CategoryBars, Panel, Observation } from "./overview-presentation";
import { ReadState, useRead, type WorkspaceProps } from "./workspace";
import { ScrollableTable } from "./ScrollableTable";
import { sourceOverview, jobOverview } from "./operations-overview-values";
import { jobKindLabel, stateLabel, SourceState } from "./operational-labels";
import { jobStates, jobQueueURL } from "./job-state";
import { Capabilities } from "./responses";
import WebhookOverview, { type CapabilityRead } from "./WebhookOverview";

const labels: Record<string, string> = {
  not_applicable: "Sync not required",
  not_yet_synced: "Not synced",
  complete: "Complete",
  incomplete: "Incomplete",
  failed: "Failed",
};
const count = (value: { value: number; limited: boolean }) =>
  value.value.toLocaleString() + (value.limited ? "+" : "");

/** Domain reads stay independent; availability and indexing share one capability read. */
export default function OperationsOverview(
  props: WorkspaceProps & {
    sharedCapabilities?: CapabilityRead | undefined;
    sharedReceivedAt?: string | undefined;
  },
) {
  const webhooks = permits(props.access, "webhook.read", "webhooks.manage");
  const indexing = permits(props.access, "operations.reindex", "tenant.admin");
  const [receivedAt, setReceivedAt] = useState<string>();
  const ownCapabilities = useRead(
    "operations-capabilities",
    async (signal) => {
      if (props.sharedCapabilities || (!webhooks && !indexing))
        return undefined;
      const value = result(
        await props.client.api.GET("/api/v1/capabilities", { signal }),
        Capabilities,
      );
      if (!signal.aborted) setReceivedAt(new Date().toISOString());
      return value;
    },
    props.onFailure,
  );
  const capabilities = props.sharedCapabilities ?? ownCapabilities;
  return (
    <>
      {permits(props.access, "source.read", "sources.read") && (
        <Sources {...props} />
      )}
      {canReadJobs(props.access) && <Jobs {...props} />}
      {webhooks && <WebhookOverview {...props} capabilities={capabilities} />}
      {indexing && (
        <Indexing
          {...props}
          capabilities={capabilities}
          receivedAt={props.sharedReceivedAt ?? receivedAt}
        />
      )}
    </>
  );
}

function Sources(props: WorkspaceProps) {
  const read = useRead(
    "source-overview",
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/overview/sources", { signal }),
        (value) => sourceOverview(value, props.access.tenantId),
      ),
    props.onFailure,
  );
  const data = read.data;
  return (
    <Panel
      title="Federation sources"
      wide={!!data && data.total.value > 0}
      value={data && data.total.value > 0 ? count(data.total) : undefined}
    >
      <ReadState {...read} />
      {data && (
        <>
          {data.total.limited ? (
            <p>
              More than 10,000 sources. Status and sync breakdowns are
              unavailable.
            </p>
          ) : data.total.value === 0 ? (
            <p>No sources configured.</p>
          ) : (
            <>
              <div className="overview-source-layout">
                <CategoryBars
                  title="Source status"
                  rows={data.states!.map((row) => ({
                    label: (
                      <a
                        href={`${props.runtime.basePath}/sources?status=${row.state}`}
                      >
                        <SourceState state={row.state} />
                      </a>
                    ),
                    count: row.count,
                  }))}
                />
                <CategoryBars
                  title="Sync status"
                  rows={data.sync!.map((row) => ({
                    label: labels[row.state],
                    count: row.count,
                  }))}
                />
              </div>
              {(data.historicalComplete ?? 0) > 0 && (
                <p>
                  {data.historicalComplete}{" "}
                  {data.historicalComplete === 1
                    ? "source still uses"
                    : "sources still use"}{" "}
                  an earlier sync.
                </p>
              )}
            </>
          )}
          {data.problems.length > 0 && (
            <ScrollableTable label="Sync problems">
              <table>
                <caption>Sync problems</caption>
                <thead>
                  <tr>
                    <th scope="col">Source</th>
                    <th scope="col">Last sync</th>
                    <th scope="col">Last complete sync</th>
                  </tr>
                </thead>
                <tbody>
                  {data.problems.map((row) => (
                    <tr key={row.sourceId}>
                      <th scope="row">
                        <a
                          href={`${props.runtime.basePath}/sources/${row.sourceId}`}
                        >
                          {row.displayName}
                        </a>
                        <span className="overview-row-detail">
                          Configuration {row.revision}
                        </span>
                      </th>
                      <td>
                        {labels[row.latest.state]}
                        <span className="overview-row-detail">
                          <time dateTime={row.latest.completedAt}>
                            {new Date(row.latest.completedAt).toLocaleString()}
                          </time>
                        </span>
                      </td>
                      <td>
                        {row.lastComplete ? (
                          <>
                            <span>
                              {row.lastComplete.revision !== row.revision
                                ? "Previous configuration: "
                                : "Configuration "}
                              {row.lastComplete.revision}
                            </span>
                            <span className="overview-row-detail">
                              <time dateTime={row.lastComplete.completedAt}>
                                {new Date(
                                  row.lastComplete.completedAt,
                                ).toLocaleString()}
                              </time>
                            </span>
                          </>
                        ) : (
                          "Not available"
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </ScrollableTable>
          )}
          <a
            className="overview-manage"
            href={`${props.runtime.basePath}/sources`}
          >
            Manage sources
          </a>
          <Observation data={data} />
        </>
      )}
    </Panel>
  );
}

function Jobs(props: WorkspaceProps) {
  const read = useRead(
    "job-overview",
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/overview/jobs", { signal }),
        (value) => jobOverview(value, props.access.tenantId),
      ),
    props.onFailure,
  );
  const data = read.data;
  return (
    <Panel title="Job queues" wide={!!data && data.total.value > 0}>
      <ReadState {...read} />
      {data && (
        <>
          <dl className="overview-facts">
            <div>
              <dt>Jobs</dt>
              <dd>{count(data.total)}</dd>
            </div>
            <div>
              <dt>Jobs due</dt>
              <dd>{count(data.due)}</dd>
            </div>
          </dl>
          {data.oldestDueAt && (
            <p>
              Oldest due:{" "}
              <time dateTime={data.oldestDueAt}>
                {new Date(data.oldestDueAt).toLocaleString()}
              </time>
              .
            </p>
          )}
          {data.total.limited ? (
            <p>
              More than 10,000 jobs. The kind and state breakdown is
              unavailable.
            </p>
          ) : data.total.value === 0 ? (
            <p>No jobs to show.</p>
          ) : (
            <>
              <ScrollableTable label="Jobs by kind and state">
                <table>
                  <caption>Jobs by kind and state</caption>
                  <thead>
                    <tr>
                      <th scope="col">Kind</th>
                      {Object.keys(jobStates).map((state) => (
                        <th scope="col" key={state}>
                          {stateLabel(state)}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {data.kinds!.map((kind) => (
                      <tr key={kind.kind}>
                        <th scope="row">{jobKindLabel(kind.kind)}</th>
                        {Object.keys(jobStates).map((state) => {
                          const row = kind.states.find(
                            (item) => item.state === state,
                          )!;
                          return (
                            <td key={row.state}>
                              <a
                                href={jobQueueURL(props.runtime.basePath, {
                                  kind: kind.kind,
                                  state: row.state,
                                  parentJobId: "",
                                })}
                              >
                                {row.count.toLocaleString()}
                              </a>
                            </td>
                          );
                        })}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </ScrollableTable>
            </>
          )}
          <p>
            <a href={`${props.runtime.basePath}/jobs`}>Manage Jobs</a>
          </p>
          <Observation data={data} />
        </>
      )}
    </Panel>
  );
}

function Indexing(
  props: WorkspaceProps & {
    capabilities: CapabilityRead;
    receivedAt: string | undefined;
  },
) {
  const read = props.capabilities;
  const data = read.data?.search;
  return (
    <Panel title="Search indexing">
      <ReadState {...read} />
      {data && (
        <>
          {data.semantic === "disabled" ? (
            <p>Semantic search is off. Keyword search is available.</p>
          ) : data.semantic === "unavailable" ? (
            <p>
              Semantic indexing is unavailable. Keyword search is available.
            </p>
          ) : !data.indexing ? (
            <p>Index status unavailable.</p>
          ) : (
            <>
              <p>
                Index status: <strong>{stateLabel(data.indexing.state)}</strong>
                .
              </p>
              <CategoryBars
                title="Indexed metadata"
                rows={[
                  { label: "Indexed", count: data.indexing.indexed },
                  {
                    label: "Failed indexing",
                    count: data.indexing.failed,
                  },
                ]}
              />
            </>
          )}
          {data.semantic !== "disabled" && (
            <p className="overview-secondary">
              Provider status (last reported):{" "}
              {data.providerState
                ? stateLabel(data.providerState)
                : "No observation"}
              .
            </p>
          )}
          {props.receivedAt && (
            <p className="muted">
              Loaded{" "}
              <time
                dateTime={props.receivedAt}
                title={new Date(props.receivedAt).toLocaleString()}
              >
                {new Date(props.receivedAt).toLocaleTimeString(undefined, {
                  hour: "2-digit",
                  minute: "2-digit",
                })}
              </time>
            </p>
          )}
          <p>
            <a href={`${props.runtime.basePath}/service#search-indexing`}>
              Manage search indexing
            </a>
          </p>
        </>
      )}
    </Panel>
  );
}
