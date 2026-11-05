import type { components } from "../api/schema";
import { result } from "../api/client";
import { permits } from "../api/permissions";
import { ReadState, useRead, type WorkspaceProps } from "./workspace";
import { Panel, CategoryBars, Observation } from "./overview-presentation";
import { stateLabel } from "./operational-labels";
import { ScrollableTable } from "./ScrollableTable";
import {
  administrationOverview,
  feedbackOverview,
  ratingOverview,
  feedbackCategories,
} from "./administration-overview-values";
import {
  directorySearch,
  moderationSearch,
} from "./administration-filter-state";

const count = (value: components["schemas"]["OverviewBoundedCount"]) =>
  value.value.toLocaleString() + (value.limited ? "+" : "");

export default function AdministrationOverview(props: WorkspaceProps) {
  return (
    <>
      {permits(props.access, "access.manage", "tenant.admin") && (
        <Access {...props} />
      )}
      {permits(props.access, "feedback.moderate", "tenant.admin") && (
        <Feedback {...props} />
      )}
      {permits(props.access, "ratings.moderate", "tenant.admin") && (
        <Ratings {...props} />
      )}
    </>
  );
}
function Access(props: WorkspaceProps) {
  const read = useRead(
    "administration-overview",
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/overview/administration", {
          signal,
        }),
        (value) => administrationOverview(value, props.access.tenantId),
      ),
    props.onFailure,
  );
  return (
    <Panel title="Catalog access" wide>
      <ReadState {...read} />
      {read.data && (
        <>
          <ScrollableTable label="Catalog members">
            <table>
              <caption>Catalog members</caption>
              <thead>
                <tr>
                  <th scope="col">Member type</th>
                  <th scope="col">Total</th>
                  <th scope="col">Active</th>
                  <th scope="col">Suspended</th>
                  <th scope="col">Offboarded</th>
                </tr>
              </thead>
              <tbody>
                {(["human", "service"] as const).map((type) => {
                  const members = read.data![type];
                  return (
                    <tr key={type}>
                      <th scope="row">
                        {type === "human" ? "People" : "Service identities"}
                      </th>
                      <td>{count(members.total)}</td>
                      {members.total.limited ? (
                        <td colSpan={3}>
                          Breakdown unavailable: more than 10,000 memberships.
                        </td>
                      ) : (
                        (["active", "suspended", "offboarded"] as const).map(
                          (state) => {
                            const row = members.states!.find(
                              (item) => item.state === state,
                            )!;
                            return (
                              <td key={state}>
                                <a
                                  aria-label={`${type === "human" ? "People" : "Service identities"}: ${stateLabel(state)}`}
                                  href={`${props.runtime.basePath}/members${directorySearch({ state, actorType: type })}`}
                                >
                                  {row.count.toLocaleString()}
                                </a>
                              </td>
                            );
                          },
                        )
                      )}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </ScrollableTable>
          <dl className="overview-access-facts">
            <div>
              <dt>Active Groups</dt>
              <dd>
                <a
                  href={`${props.runtime.basePath}/access-groups?state=active`}
                >
                  <span className="overview-count">
                    {count(read.data.activeGroups)}
                  </span>
                </a>
                <span className="overview-secondary">
                  Current sign-in realm
                </span>
              </dd>
            </div>
            <div>
              <dt>Active Roles</dt>
              <dd>
                <a href={`${props.runtime.basePath}/access-roles?state=active`}>
                  <span className="overview-count">
                    {count(read.data.activeRoles.total)}
                  </span>
                </a>
                {!read.data.activeRoles.total.limited && (
                  <span className="overview-secondary">
                    {read.data.activeRoles.builtin!.toLocaleString()} built-in;{" "}
                    {read.data.activeRoles.custom!.toLocaleString()} custom
                  </span>
                )}
              </dd>
            </div>
            <div>
              <dt>Active role assignments</dt>
              <dd>
                <span className="overview-count">
                  {count(read.data.activeAssignments)}
                </span>
                <span className="overview-secondary">
                  <a href={`${props.runtime.basePath}/access-assignments`}>
                    Manage assignments
                  </a>
                </span>
              </dd>
            </div>
          </dl>
          {read.data.activeRoles.total.limited && (
            <p>More than 10,000 roles. The role breakdown is unavailable.</p>
          )}
          {read.data.activeGroups.limited && (
            <p>More than 10,000 active groups.</p>
          )}
          {read.data.activeAssignments.limited && (
            <p>More than 10,000 active role assignments.</p>
          )}
          <Observation data={read.data} />
        </>
      )}
    </Panel>
  );
}

function Feedback(props: WorkspaceProps) {
  const read = useRead(
    "feedback-overview",
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/overview/feedback", { signal }),
        (value) => feedbackOverview(value, props.access.tenantId),
      ),
    props.onFailure,
  );
  return (
    <Panel title="Open feedback">
      <ReadState {...read} />
      {read.data && (
        <>
          {read.data.open.value > 0 && (
            <p>
              <strong className="overview-number">
                {count(read.data.open)}
              </strong>{" "}
              {read.data.open.value === 1
                ? "open submission"
                : "open submissions"}
            </p>
          )}
          {read.data.open.limited ? (
            <p>
              Open feedback exceeds the 10,000-row limit. The complete category
              distribution is unavailable.
            </p>
          ) : read.data.open.value === 0 ? (
            <p>No open feedback.</p>
          ) : (
            <CategoryBars
              title="Open feedback by category"
              rows={read.data.categories!.map((row) => ({
                label: (
                  <a
                    href={`${props.runtime.basePath}/feedback${moderationSearch({ state: "open", category: row.category })}`}
                  >
                    {feedbackCategories[row.category]}
                  </a>
                ),
                count: row.count,
              }))}
            />
          )}
          {read.data.oldestOpenAt && (
            <p>
              Oldest submission:{" "}
              <time dateTime={read.data.oldestOpenAt}>
                {new Date(read.data.oldestOpenAt).toLocaleString()}
              </time>
              .
            </p>
          )}
          <p>
            <a href={`${props.runtime.basePath}/feedback?state=open`}>
              View open feedback
            </a>
          </p>
          <Observation data={read.data} />
        </>
      )}
    </Panel>
  );
}
function Ratings(props: WorkspaceProps) {
  const read = useRead(
    "rating-overview",
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/overview/ratings", { signal }),
        (value) => ratingOverview(value, props.access.tenantId),
      ),
    props.onFailure,
  );
  return (
    <Panel title="Ratings">
      <ReadState {...read} />
      {read.data && (
        <>
          {read.data.total.value > 0 && (
            <p>
              <strong className="overview-number">
                {count(read.data.total)}
              </strong>{" "}
              {read.data.total.value === 1 ? "rating" : "ratings"}
            </p>
          )}
          {read.data.total.limited ? (
            <p>
              Ratings exceed the 10,000-row limit. The complete state
              distribution is unavailable.
            </p>
          ) : read.data.total.value === 0 ? (
            <p>No ratings to show.</p>
          ) : (
            <CategoryBars
              title="Rating states"
              rows={read.data.states!.map((row) => ({
                label: (
                  <a
                    href={`${props.runtime.basePath}/ratings${moderationSearch({ state: row.state })}`}
                  >
                    {stateLabel(row.state)}
                  </a>
                ),
                count: row.count,
              }))}
            />
          )}
          <Observation data={read.data} />
        </>
      )}
    </Panel>
  );
}
