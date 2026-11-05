import { useId, useState } from "react";
import type { components } from "../api/schema";
import { result } from "../api/client";
import { Panel, Observation, CategoryBars } from "./overview-presentation";
import { reviewOverview } from "./review-overview-values";
import { ReadState, useRead, type WorkspaceProps } from "./workspace";
import { ScrollableTable } from "./ScrollableTable";
import { durationLabel } from "./operational-labels";

type Summary = components["schemas"]["ReviewOverview"];
const count = (value: components["schemas"]["OverviewBoundedCount"]) =>
  `${value.value.toLocaleString()}${value.limited ? "+" : ""}`;
const date = (value: string) =>
  new Date(value).toLocaleDateString(undefined, {
    timeZone: "UTC",
    month: "short",
    day: "numeric",
  });

export default function ReviewOverview(props: WorkspaceProps) {
  const read = useRead(
    "review-overview",
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/overview/reviews", { signal }),
        (reviewOverviewValue) =>
          reviewOverview(reviewOverviewValue, props.access.tenantId),
      ),
    props.onFailure,
  );
  const data = read.data;
  return (
    <Panel title="Reviews" wide>
      <ReadState {...read} />
      {data && (
        <>
          <div className="overview-review-layout">
            <section aria-label="Review queue">
              <h3>Review queue</h3>
              <dl className="overview-facts">
                <div>
                  <dt>
                    <a
                      href={`${props.runtime.basePath}/reviews?state=submitted`}
                    >
                      Awaiting review
                    </a>
                  </dt>
                  <dd>{count(data.pending)}</dd>
                </div>
                <div>
                  <dt>
                    <a
                      href={`${props.runtime.basePath}/reviews?state=approved&unreleased=true`}
                    >
                      Approved, unpublished
                    </a>
                  </dt>
                  <dd>{count(data.approvedUnreleased)}</dd>
                </div>
              </dl>
              {data.oldestPendingAt && (
                <p className="overview-secondary">
                  Oldest review:{" "}
                  <time dateTime={data.oldestPendingAt}>
                    {new Date(data.oldestPendingAt).toLocaleDateString()}
                  </time>
                </p>
              )}
              <PendingAge data={data} />
              <h3>Median review time</h3>
              {data.medianSeconds !== undefined ? (
                <p className="overview-duration">
                  <strong title={`${data.medianSeconds} seconds`}>
                    {data.medianSeconds < 1
                      ? "Less than 1 second"
                      : durationLabel(Math.round(data.medianSeconds))}
                  </strong>
                  <span className="overview-secondary">
                    {count(data.decisions)}{" "}
                    {data.decisions.value === 1 ? "decision" : "decisions"}, 30
                    days
                  </span>
                </p>
              ) : (
                <p>
                  {data.medianUnavailable === "limited"
                    ? "Review time unavailable: more than 10,000 decisions."
                    : data.medianUnavailable
                      ? "Review time unavailable: inconsistent timestamps."
                      : "No decisions in this period."}
                </p>
              )}
            </section>
            <ReviewActivity data={data} />
          </div>
          <Observation data={data} />
        </>
      )}
    </Panel>
  );
}
function PendingAge({ data }: { data: Summary }) {
  if (data.pending.value === 0) return null;
  return data.pendingAge ? (
    <CategoryBars
      title="Waiting time"
      rows={[
        { label: "Under 24 hours", count: data.pendingAge.under24Hours },
        { label: "1 to under 7 days", count: data.pendingAge.under7Days },
        { label: "7 days or more", count: data.pendingAge.atLeast7Days },
      ]}
    />
  ) : (
    <p>
      {data.pendingAgeUnavailable === "limited"
        ? "Age breakdown unavailable: more than 10,000 reviews."
        : "Age breakdown unavailable: inconsistent timestamps."}
    </p>
  );
}
function ReviewActivity({ data }: { data: Summary }) {
  const id = useId();
  const [daily, setDaily] = useState(false);
  const complete = !data.submissions.limited && !data.decisions.limited;
  const maximum = Math.max(
    1,
    ...data.days.flatMap((day) => [
      day.submitted ?? 0,
      (day.approved ?? 0) + (day.changesRequired ?? 0) + (day.rejected ?? 0),
    ]),
  );
  const any = data.submissions.value > 0 || data.decisions.value > 0;
  return (
    <section aria-label="Review activity" className="overview-activity">
      <div className="overview-activity-heading">
        <h3>Review activity</h3>
        <div
          className="segmented"
          role="group"
          aria-label="Review activity view"
        >
          <button
            type="button"
            aria-pressed={!daily}
            onClick={() => setDaily(false)}
          >
            Chart
          </button>
          <button
            type="button"
            aria-pressed={daily}
            onClick={() => setDaily(true)}
          >
            Daily breakdown
          </button>
        </div>
      </div>
      <p className="overview-secondary">
        {date(data.startAt)} - {date(data.endAt)} (UTC)
        {data.days.some((day) => day.partial) ? ", today so far" : ""}
      </p>
      {daily ? (
        <ScrollableTable label="Daily review activity">
          <table>
            <caption>Daily submissions and decision outcomes in UTC</caption>
            <thead>
              <tr>
                <th scope="col">UTC date</th>
                <th scope="col">Submitted</th>
                <th scope="col">Approved</th>
                <th scope="col">Changes required</th>
                <th scope="col">Rejected</th>
              </tr>
            </thead>
            <tbody>
              {data.days.map((day) => (
                <tr key={day.date}>
                  <th scope="row">
                    {day.date}
                    {day.partial ? " (partial)" : ""}
                  </th>
                  {[
                    day.submitted,
                    day.approved,
                    day.changesRequired,
                    day.rejected,
                  ].map((value, index) => (
                    <td key={index}>
                      {value === undefined
                        ? "Unavailable"
                        : value.toLocaleString()}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </ScrollableTable>
      ) : complete && any ? (
        <>
          <div className="overview-chart-legend">
            <span>
              <i className="review-legend-submitted" aria-hidden="true" />
              Submitted
            </span>
            <span>
              <i className="review-legend-decided" aria-hidden="true" />
              Decisions
            </span>
          </div>
          <div className="review-plot">
            <div className="review-value-axis" aria-hidden="true">
              <span>{maximum.toLocaleString()}</span>
              <span>0</span>
            </div>
            <svg
              className="review-activity-chart"
              viewBox="0 0 750 140"
              preserveAspectRatio="none"
              role="img"
              aria-labelledby={id}
            >
              <title id={id}>
                Daily submitted reviews and decisions, from zero to {maximum}
              </title>
              <line x1="0" y1="130" x2="750" y2="130" stroke="currentColor" />
              {data.days.map((day, index) => {
                const decisions =
                  day.approved! + day.changesRequired! + day.rejected!;
                return (
                  <g key={day.date}>
                    <rect
                      className="review-submitted-bar"
                      x={3.5 + index * 25}
                      y={130 - (120 * day.submitted!) / maximum}
                      width="8"
                      height={(120 * day.submitted!) / maximum}
                    />
                    <rect
                      className="review-decided-bar"
                      x={13.5 + index * 25}
                      y={130 - (120 * decisions) / maximum}
                      width="8"
                      height={(120 * decisions) / maximum}
                    />
                  </g>
                );
              })}
            </svg>
            <div className="review-date-axis" aria-hidden="true">
              {[0, 7, 14, 21, 29].map((index) => (
                <time
                  key={index}
                  dateTime={data.days[index]!.date}
                  style={{ left: `${((index + 0.5) / 30) * 100}%` }}
                >
                  {date(data.days[index]!.date)}
                </time>
              ))}
            </div>
          </div>
        </>
      ) : (
        <p>
          {!complete
            ? "Chart unavailable: more than 10,000 submissions or decisions. Use Daily breakdown for available values."
            : "No review activity in this period."}
        </p>
      )}
    </section>
  );
}
