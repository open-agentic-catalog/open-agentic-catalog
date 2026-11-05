import { useState } from "react";
import type { components } from "../api/schema";
import { result } from "../api/client";
import { permits } from "../api/permissions";
import { Panel, Observation, CategoryBars } from "./overview-presentation";
import { WebhookReconciliation } from "./responses";
import { ReadState, useRead, type WorkspaceProps } from "./workspace";
import {
  webhookEndpointOverview,
  webhookDeliveryOverview,
} from "./webhook-overview-values";
import { AccessState, WorkState } from "./operational-labels";
import {
  webhookDirectoryURL,
  webhookDeliveryURL,
} from "./webhook-filter-state";
import { ScrollableTable } from "./ScrollableTable";

const count = (value: { value: number; limited: boolean }) =>
  value.value.toLocaleString() + (value.limited ? "+" : "");
export type CapabilityRead = ReturnType<
  typeof useRead<components["schemas"]["Capabilities"]>
>;

export default function WebhookOverview(
  props: WorkspaceProps & { capabilities: CapabilityRead },
) {
  const caps = props.capabilities;
  return (
    <Panel
      title="Webhooks"
      wide={caps.data?.optionalCapabilities.webhooks === "enabled"}
    >
      <ReadState {...caps} />
      {caps.data && (
        <p className="overview-feature-state">
          <span
            className="state-badge"
            data-tone={
              caps.data.optionalCapabilities.webhooks === "enabled"
                ? "info"
                : "neutral"
            }
          >
            {caps.data.optionalCapabilities.webhooks === "enabled"
              ? "Enabled"
              : caps.data.optionalCapabilities.webhooks === "disabled"
                ? "Off"
                : "Unavailable"}
          </span>
          <a href={`${props.runtime.basePath}/webhooks`}>Manage webhooks</a>
        </p>
      )}
      {caps.data?.optionalCapabilities.webhooks === "enabled" && (
        <div className="overview-webhook-layout">
          <Endpoints {...props} />
          {permits(
            props.access,
            "webhook.read_evidence",
            "webhooks.manage",
          ) && (
            <>
              <Deliveries {...props} />
              <Notifications {...props} />
            </>
          )}
        </div>
      )}
    </Panel>
  );
}
function Endpoints(props: WorkspaceProps) {
  const read = useRead(
    "webhook-endpoint-overview",
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/overview/webhook-endpoints", {
          signal,
        }),
        (v) => webhookEndpointOverview(v, props.access.tenantId),
      ),
    props.onFailure,
  );
  return (
    <section aria-label="Webhook endpoints">
      <h3>Endpoints</h3>
      <ReadState {...read} />
      {read.data && (
        <>
          {read.data.total.value > 0 && (
            <p>
              <strong className="overview-number">
                {count(read.data.total)}
              </strong>{" "}
              {read.data.total.value === 1 ? "endpoint" : "endpoints"}
            </p>
          )}
          {read.data.total.limited ? (
            <p>More than 10,000 endpoints. Status breakdown unavailable.</p>
          ) : read.data.total.value === 0 ? (
            <p>No endpoints configured.</p>
          ) : (
            <CategoryBars
              title="Endpoint status"
              rows={read.data.states!.map((row) => ({
                label: (
                  <a
                    href={webhookDirectoryURL(
                      props.runtime.basePath,
                      row.state,
                    )}
                  >
                    <AccessState state={row.state} />
                  </a>
                ),
                tone: row.state === "active" ? "info" : "neutral",
                count: row.count,
              }))}
            />
          )}
          <Observation data={read.data} />
        </>
      )}
    </section>
  );
}
function Deliveries(props: WorkspaceProps) {
  const read = useRead(
    "webhook-delivery-overview",
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/overview/webhook-deliveries", {
          signal,
        }),
        (v) => webhookDeliveryOverview(v, props.access.tenantId),
      ),
    props.onFailure,
  );
  return (
    <section aria-label="Delivery history">
      <h3>Delivery history</h3>
      <ReadState {...read} />
      {read.data && (
        <>
          {read.data.total.value > 0 && (
            <p>
              <strong className="overview-number">
                {count(read.data.total)}
              </strong>{" "}
              {read.data.total.value === 1 ? "delivery" : "deliveries"}
            </p>
          )}
          {read.data.total.limited ? (
            <p>More than 10,000 deliveries. State breakdown unavailable.</p>
          ) : read.data.total.value === 0 ? (
            <p>No deliveries to show.</p>
          ) : (
            <CategoryBars
              title="Delivery states"
              rows={read.data.states!.map((row) => ({
                label: <WorkState state={row.state} />,
                tone: {
                  pending: "neutral",
                  waiting: "warning",
                  succeeded: "success",
                  dead_letter: "danger",
                }[row.state],
                count: row.count,
              }))}
            />
          )}
          {read.data.failedEndpoints.length > 0 && (
            <ScrollableTable label="Delivery failures">
              <table>
                <caption>Delivery failures</caption>
                <thead>
                  <tr>
                    <th scope="col">Endpoint</th>
                    <th scope="col">Delivery</th>
                    <th scope="col">Attempted</th>
                  </tr>
                </thead>
                <tbody>
                  {read.data.failedEndpoints.map((row) => (
                    <tr key={row.endpointId}>
                      <th scope="row">
                        <a
                          href={`${props.runtime.basePath}/webhooks/${row.endpointId}`}
                        >
                          {row.name}
                        </a>
                      </th>
                      <td>
                        <a
                          href={webhookDeliveryURL(
                            props.runtime.basePath,
                            row.endpointId,
                            {
                              deliveryId: row.deliveryId,
                              eventId: "",
                              state: row.state,
                            },
                          )}
                        >
                          View delivery
                        </a>
                      </td>
                      <td>
                        <time dateTime={row.attemptedAt}>
                          {new Date(row.attemptedAt).toLocaleString()}
                        </time>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </ScrollableTable>
          )}
          <Observation data={read.data} />
        </>
      )}
    </section>
  );
}
function Notifications(props: WorkspaceProps) {
  const [receivedAt, setReceivedAt] = useState<string>();
  const read = useRead(
    "webhook-notifications",
    async (signal) => {
      const value = result(
        await props.client.api.GET("/api/v1/webhooks/reconciliation", {
          signal,
        }),
        WebhookReconciliation,
      );
      if (!signal.aborted) setReceivedAt(new Date().toISOString());
      return value;
    },
    props.onFailure,
  );
  return (
    <section aria-label="Notification totals">
      <h3>Notification totals</h3>
      <ReadState {...read} />
      {read.data && (
        <>
          <dl className="overview-notification-facts">
            <div>
              <dt>Events awaiting delivery</dt>
              <dd>
                {read.data.pendingEvents === read.data.pendingEventLimit
                  ? "At least "
                  : ""}
                {read.data.pendingEvents.toLocaleString()}
              </dd>
            </div>
            <div>
              <dt>Events dropped by capacity limits</dt>
              <dd>{read.data.capacityLossCount.toLocaleString()}</dd>
            </div>
            <div>
              <dt>Final delivery failures</dt>
              <dd>{read.data.deliveryLossCount.toLocaleString()}</dd>
            </div>
          </dl>
          {read.data.lastCapacityLossAt && (
            <p className="overview-secondary">
              Last event dropped:{" "}
              <time dateTime={read.data.lastCapacityLossAt}>
                {new Date(read.data.lastCapacityLossAt).toLocaleString()}
              </time>
            </p>
          )}
          {read.data.lastDeliveryLossAt && (
            <p className="overview-secondary">
              Last delivery failure:{" "}
              <time dateTime={read.data.lastDeliveryLossAt}>
                {new Date(read.data.lastDeliveryLossAt).toLocaleString()}
              </time>
            </p>
          )}
          {receivedAt && (
            <p className="overview-observation muted">
              Loaded{" "}
              <time
                dateTime={receivedAt}
                title={new Date(receivedAt).toLocaleString()}
              >
                {new Date(receivedAt).toLocaleTimeString(undefined, {
                  hour: "2-digit",
                  minute: "2-digit",
                })}
              </time>
            </p>
          )}
        </>
      )}
    </section>
  );
}
