import { useEffect, useRef, useState } from "react";
import { APIError, result } from "../api/client";
import { controlIcons } from "../shell/Icon";
import type { WorkspaceProps } from "./workspace";
import { Panel } from "./overview-presentation";
import { ScrollableTable } from "./ScrollableTable";
import {
  activity,
  catalogFamilies,
  completed,
  countText,
  duration,
  interval,
  mean,
  methods,
  statuses,
  mcpOperations,
  mcpOutcomes,
  type Activity,
} from "./activity-values";
import "./activity.css";

type Sample = {
  data: Activity;
  received: number;
  interval: ReturnType<typeof interval>;
};
const decimal = (n: number | undefined, unit = "") =>
  n === undefined || !Number.isFinite(n)
    ? "Not available"
    : n.toLocaleString(undefined, { maximumFractionDigits: 3 }) + unit;

/** The keyed Overview boundary owns identity/policy/source changes. No sample persists. */
type ActivityProps = WorkspaceProps & {
  startPaused?: boolean | undefined;
  onPause?: (paused: boolean) => void;
};
export default function ActivityOverview(props: ActivityProps) {
  const boundary = useRef({
    access: props.access,
    client: props.client,
    signal: props.client.signal,
    source: props.runtime.basePath,
    epoch: 0,
  });
  const old = boundary.current;
  if (
    old.access !== props.access ||
    old.client !== props.client ||
    old.signal !== props.client.signal ||
    old.source !== props.runtime.basePath
  )
    boundary.current = {
      access: props.access,
      client: props.client,
      signal: props.client.signal,
      source: props.runtime.basePath,
      epoch: old.epoch + 1,
    };
  return (
    <ActivitySession
      key={boundary.current.epoch}
      {...props}
      startPaused={props.startPaused || boundary.current.epoch > 0}
    />
  );
}

function ActivitySession(props: ActivityProps) {
  const initiallyPaused = useRef(props.startPaused).current;
  const [sample, setSample] = useState<Sample>();
  const [busy, setBusy] = useState(!initiallyPaused);
  const [live, setLive] = useState(false);
  const [error, setError] = useState(
    initiallyPaused
      ? "Request statistics are paused. Select Reload to continue."
      : "",
  );
  const [age, setAge] = useState(0);
  const [hidden, setHidden] = useState(false);
  const [group, setGroup] = useState("family");
  const failure = useRef(props.onFailure);
  failure.current = props.onFailure;
  const pause = useRef(props.onPause);
  pause.current = props.onPause;
  const actions = useRef<{
    refresh: () => void;
    live: (enabled: boolean) => void;
  }>(undefined);
  useEffect(() => {
    let current = true;
    let generation = 0;
    let pending = false;
    let automatic = false;
    let previous: Sample | undefined;
    let receivedAt: number | undefined;
    let controller: AbortController | undefined;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const identity = props.client.signal;
    const visible = () => document.visibilityState !== "hidden";
    const cancelTimer = () => {
      clearTimeout(timer);
      timer = undefined;
    };
    const stop = () => {
      cancelTimer();
      automatic = false;
      setLive(false);
      generation++;
      controller?.abort();
      pending = false;
      setBusy(false);
      previous = undefined;
    };
    const schedule = () => {
      cancelTimer();
      if (current && automatic && visible())
        timer = setTimeout(() => void load(), 10000);
    };
    async function load() {
      if (!current || pending || !visible() || identity?.aborted) return;
      cancelTimer();
      pending = true;
      setBusy(true);
      setError("");
      setHidden(false);
      const epoch = ++generation;
      controller = new AbortController();
      try {
        const data = result(
          await props.client.api.GET("/api/v1/service-activity", {
            signal: controller.signal,
          }),
          activity,
        );
        if (!current || epoch !== generation || identity?.aborted) return;
        if (previous && performance.now() - previous.received >= 30000)
          previous = undefined;
        const next = {
          data,
          received: performance.now(),
          interval: interval(previous?.data, data),
        };
        receivedAt = next.received;
        pause.current?.(false);
        previous = next;
        setSample(next);
        setAge(0);
      } catch (e) {
        if (!current || epoch !== generation || identity?.aborted) return;
        previous = undefined;
        receivedAt = undefined;
        setSample(undefined);
        pause.current?.(true);
        automatic = false;
        setLive(false);
        setError(
          e instanceof APIError
            ? e.message
            : "Request statistics could not be loaded.",
        );
        failure.current(e);
      } finally {
        if (current && epoch === generation) {
          pending = false;
          setBusy(false);
          schedule();
        }
      }
    }
    const visibility = () => {
      if (!visible()) {
        stop();
        setSample((value) => value && { ...value, interval: undefined });
        setHidden(true);
      } else if (receivedAt !== undefined)
        setAge(Math.max(0, (performance.now() - receivedAt) / 1000));
    };
    const invalidated = () => {
      stop();
      receivedAt = undefined;
      setSample(undefined);
    };
    document.addEventListener("visibilitychange", visibility);
    identity?.addEventListener("abort", invalidated);
    const tick = setInterval(() => {
      if (visible() && receivedAt !== undefined) {
        const age = Math.max(0, (performance.now() - receivedAt) / 1000);
        setAge(age);
        if (age >= 30) previous = undefined;
      }
    }, 1000);
    actions.current = {
      refresh: () => void load(),
      live: (enabled) => {
        automatic = enabled;
        setLive(enabled);
        if (enabled && !pending) schedule();
        else cancelTimer();
      },
    };
    if (!initiallyPaused && visible()) void load();
    else if (!visible()) {
      setBusy(false);
      setHidden(true);
    }
    return () => {
      current = false;
      generation++;
      cancelTimer();
      clearInterval(tick);
      controller?.abort();
      document.removeEventListener("visibilitychange", visibility);
      identity?.removeEventListener("abort", invalidated);
    };
  }, [props.client, initiallyPaused]);
  const data = sample?.data;
  const stale = age >= 30;
  const measured = !stale && !hidden ? sample?.interval : undefined;
  const rows = data && catalogFamilies(data);
  const statusCount = (index: number) =>
    rows!.reduce(
      (n, row) => n + BigInt(row.statusClasses[index]!.completed),
      0n,
    );
  const labels: Record<string, string> = {
    control_search: "Catalog search",
    control_explore: "Local browse",
    observation_search: "Indexed source search",
    administrative_live_search: "Source live search",
    ard_search: "ARD Search",
    ard_explore: "ARD Explore",
    ard_list: "ARD List",
    discovery_documents: "Discovery documents",
    http_mcp: "MCP HTTP",
    control_read: "Other API reads",
    control_command: "API commands",
  };
  return (
    <div className="activity-overview">
      <div className="activity-toolbar">
        <button disabled={busy} onClick={() => actions.current?.refresh()}>
          {controlIcons.refresh} {error ? "Reload" : "Refresh"}
        </button>
        <label>
          <input
            type="checkbox"
            checked={live}
            disabled={!data || !!error || hidden}
            onChange={(e) => actions.current?.live(e.target.checked)}
          />
          Live refresh every 10 seconds
        </label>
        {busy && <span role="status">Loading request statistics...</span>}
      </div>
      {error && <p role="alert">{error} Live refresh is paused.</p>}
      {hidden && (
        <p role="status">
          Live refresh paused while this tab was hidden. Refresh to start a new
          interval.
        </p>
      )}
      {data && (
        <>
          <p className="activity-context">
            Requests handled by this API process across all tenants. Unique
            clients are not measured.{" "}
            <a href={`${props.runtime.basePath}/service`}>
              Service information
            </a>
          </p>
          <p
            className={stale ? "activity-stale" : "muted"}
            role={stale ? "status" : undefined}
          >
            {stale ? "Out of date. " : ""}Updated {Math.floor(age)} seconds ago.
          </p>
          <div className="activity-stats">
            {[
              ["Catalog requests", countText(completed(data))],
              ["In progress", countText(data.http.activeCatalog)],
              ["Client errors (4xx)", countText(statusCount(3))],
              ["Server errors (5xx)", countText(statusCount(4))],
              [
                "Mean handler time since start",
                decimal(mean(duration(data), completed(data)), " ms"),
              ],
              ["Requests per second", decimal(measured?.rate)],
              [
                "Mean handler time in interval",
                decimal(measured?.latency, " ms"),
              ],
            ].map(([title, value]) => (
              <Panel title={title!} value={value} compact key={title}>
                {null}
              </Panel>
            ))}
          </div>
          <p className="muted">
            {measured
              ? `Measured over ${decimal(measured.elapsed, " seconds")}.`
              : "Refresh again after 1-30 seconds to measure a rate. A restart, process switch or stale sample starts a new interval."}
          </p>
          <Panel title="Catalog requests by type" wide>
            <label>
              Break down requests by{" "}
              <select value={group} onChange={(e) => setGroup(e.target.value)}>
                <option value="family">Request type</option>
                <option value="method">HTTP method</option>
                <option value="status">Status class</option>
              </select>
            </label>
            <ScrollableTable label="Catalog request breakdown">
              <table>
                <caption>
                  Completed Catalog requests since this process started
                </caption>
                <thead>
                  <tr>
                    <th scope="col">
                      {group === "family"
                        ? "Request type"
                        : group === "method"
                          ? "HTTP method"
                          : "Status class"}
                    </th>
                    <th scope="col">Requests</th>
                    {group === "family" && (
                      <th scope="col">Mean handler time</th>
                    )}
                  </tr>
                </thead>
                <tbody>
                  {group === "family"
                    ? rows!.map((row) => (
                        <tr key={row.family}>
                          <th scope="row">{labels[row.family]}</th>
                          <td>{countText(row.completed)}</td>
                          <td>
                            {decimal(
                              mean(row.durationSeconds, BigInt(row.completed)),
                              " ms",
                            )}
                          </td>
                        </tr>
                      ))
                    : (group === "method" ? methods : statuses).map(
                        (name, i) => (
                          <tr key={name}>
                            <th scope="row">{name}</th>
                            <td>
                              {countText(
                                group === "status"
                                  ? statusCount(i)
                                  : rows!.reduce(
                                      (n, row) =>
                                        n + BigInt(row.methods[i]!.completed),
                                      0n,
                                    ),
                              )}
                            </td>
                          </tr>
                        ),
                      )}
                </tbody>
              </table>
            </ScrollableTable>
          </Panel>
          <div className="activity-details">
            <Panel title="Other HTTP traffic">
              <p>
                All measured HTTP completions:{" "}
                <strong>{countText(data.http.completed)}</strong>, including
                Catalog requests.
              </p>
              <dl className="overview-facts">
                {[
                  ["platform", "Health, metrics and platform"],
                  ["observer", "Activity reads"],
                  ["unknown_auxiliary", "Unmatched and auxiliary requests"],
                ].map(([key, label]) => (
                  <div key={key}>
                    <dt>{label}</dt>
                    <dd>
                      {countText(
                        data.http.families.find((row) => row.family === key)!
                          .completed,
                      )}
                    </dd>
                  </div>
                ))}
              </dl>
            </Panel>
            <Panel title="Control API search outcomes">
              <dl className="overview-facts">
                {data.controlSearch.outcomes.map((row) => (
                  <div key={row.outcome}>
                    <dt>
                      {
                        {
                          succeeded: "Succeeded",
                          partial: "Partial results",
                          rejected: "Rejected",
                          failed: "Failed",
                        }[row.outcome]
                      }
                    </dt>
                    <dd>{countText(row.completed)}</dd>
                  </div>
                ))}
              </dl>
              <p className="muted">
                These searches also appear in the HTTP counts.
              </p>
            </Panel>
          </div>
          <Panel title="MCP outcomes" wide>
            {!data.mcp.enabled ? (
              <p>MCP is turned off.</p>
            ) : (
              <>
                <p className="muted">
                  Protocol and tool outcomes overlap HTTP requests. They are not
                  an additional request total.
                </p>
                <ScrollableTable label="MCP operation outcomes">
                  <table>
                    <thead>
                      <tr>
                        <th scope="col">Operation</th>
                        {[
                          "Completed",
                          "Rejected",
                          "Failed",
                          "Tool error",
                          "Busy",
                          "Cancelled",
                        ].map((v) => (
                          <th scope="col" key={v}>
                            {v}
                          </th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {mcpOperations.map((op) => (
                        <tr key={op}>
                          <th scope="row">
                            {
                              {
                                protocol: "Protocol",
                                discover: "Discovery",
                                list: "List tools",
                                call: "Call tool",
                              }[op]
                            }
                          </th>
                          {mcpOutcomes.map((outcome) => (
                            <td key={outcome}>
                              {countText(
                                data.mcp.outcomes.find(
                                  (row) =>
                                    row.operation === op &&
                                    row.outcome === outcome,
                                )!.completed,
                              )}
                            </td>
                          ))}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </ScrollableTable>
              </>
            )}
          </Panel>
          <details className="workspace-panel">
            <summary>Responding process</summary>
            <dl className="overview-facts">
              <div>
                <dt>Execution ID</dt>
                <dd className="activity-id">{data.instanceId}</dd>
              </div>
              <div>
                <dt>Role</dt>
                <dd>{data.role}</dd>
              </div>
              <div>
                <dt>Started</dt>
                <dd>
                  <time dateTime={data.startedAt}>
                    {new Date(data.startedAt).toLocaleString()}
                  </time>
                </dd>
              </div>
              <div>
                <dt>Sampled</dt>
                <dd>
                  <time dateTime={data.observedAt}>
                    {new Date(data.observedAt).toLocaleString()}
                  </time>
                </dd>
              </div>
              <div>
                <dt>Elapsed since start</dt>
                <dd>{decimal(data.elapsedSeconds, " seconds")}</dd>
              </div>
            </dl>
            <p>
              This is one responding process, not a count of servers or active
              users.
            </p>
          </details>
        </>
      )}
    </div>
  );
}
