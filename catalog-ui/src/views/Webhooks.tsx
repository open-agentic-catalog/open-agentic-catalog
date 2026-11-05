import {
  webhookParameter,
  webhookDeliveryFilters,
  webhookDirectoryURL,
  webhookDeliveryURL,
} from "./webhook-filter-state";
import { commandSummary } from "./workspace";
import { StatusLabel, WorkState, stateLabel } from "./operational-labels";

import { Facts } from "./Facts";
import { DateValue } from "./date-value";

import { characterText } from "./input-validation";
import { CopyReference } from "./CopyReference";
import * as checks from "./responses";
// Optional webhook workflows keep endpoint state, transient signing secrets and delivery evidence separate.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { components } from "../api/schema";
import { documentationGuideLink } from "../api/session";
import {
  commonWebhookChoices,
  moreWebhookChoices,
  normalizeWebhookEvents,
  toggleWebhookEvent,
  webhookChoices,
  webhookEventSelected,
} from "./webhook-events";
import { APIError, result, acknowledge } from "../api/client";
import { object } from "../api/values";
import { permits } from "../api/permissions";
import { Icon, controlIcons } from "../shell/Icon";
import { ActionForm, EvidenceDetails } from "./authoring";
import type { WorkspaceProps } from "./workspace";
import {
  Field,
  Heading,
  Pager,
  ReadState,
  useRead,
  useUnsaved,
} from "./workspace";

type SecretReply = components["schemas"]["WebhookSecretResult"];
type RotationMode = "routine" | "emergency";
type RotationReceipt = {
  endpointId: string;
  secretVersion: number;
  mode: RotationMode;
  previousSecretExpiresAt?: string;
};
/** Check deployment capability and management access before mounting webhook workflows. */
export default function Webhooks(props: WorkspaceProps) {
  const caps = useRead(
    "webhooks-capability",
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/capabilities", { signal }),
        checks.Capabilities,
      ),
    props.onFailure,
  );
  return (
    <>
      <Heading
        title="Webhooks"
        icon="webhook"
        description="Notify your own service about supported public Catalog events."
      />
      <ReadState {...caps} />
      {caps.data &&
        (caps.data.optionalCapabilities.webhooks !== "enabled" ? (
          <p className="notice">
            {caps.data.optionalCapabilities.webhooks === "disabled"
              ? "Webhooks are turned off. Ask your operator to enable them."
              : "Webhooks are unavailable in this deployment. Ask your operator to check the configuration."}
          </p>
        ) : !permits(props.access, "webhook.read", "webhooks.manage") ? (
          <p role="alert" className="notice error">
            The webhook.read permission is required. When scopes are enforced,
            webhooks.manage is also required.
          </p>
        ) : props.id === "new" ? (
          <WebhookCreate
            key={JSON.stringify([props.access, props.id])}
            {...props}
          />
        ) : props.id ? (
          <WebhookDetail
            key={JSON.stringify([props.access, props.id])}
            {...props}
          />
        ) : (
          <>
            <WebhookList {...props} />
            {permits(
              props.access,
              "webhook.read_evidence",
              "webhooks.manage",
            ) && <Reconciliation {...props} />}
          </>
        ))}
      {caps.data && <WebhookSetup {...props} />}
    </>
  );
}

/** Explain operator/receiver prerequisites without reading private deployment policy. */
function WebhookSetup(props: WorkspaceProps) {
  const links = [
    documentationGuideLink(props.runtime.documentationUrl, "webhooks"),
    documentationGuideLink(props.runtime.documentationUrl, "webhook-receiver"),
  ]
    .filter((link): link is { href: string; label: string } => !!link)
    .filter(
      (link, index, all) =>
        all.findIndex((other) => other.href === link.href) === index,
    );
  return (
    <details className="workspace-advanced" open={props.id === "new"}>
      <summary>Webhook setup and receiver guides</summary>
      <section className="workspace-panel">
        <h2>Set up webhook delivery</h2>
        <p>
          Enabling the capability, registering a tenant endpoint and installing
          its signing key in your receiver are separate steps.
        </p>
        <details className="workspace-advanced">
          <summary>Deployment and receiver checklist</summary>
          <ol>
            <li>
              Enable webhooks at startup in both API and worker. Production and
              Helm default off. Local Compose enables them unless
              LOCAL_WEBHOOKS=off; an explicit CATALOG_WEBHOOKS_ENABLED override
              takes precedence. Follow the operator procedure for ordered
              enable/disable transitions.
            </li>
            <li>
              Supply matching deployment encryption keys, policy and trust
              inputs to both roles, using each container's mounted paths. The
              deployment encryption key is separate from each endpoint's signing
              secret. Normal notification delivery needs a worker; an explicit
              test uses the API sender.
            </li>
            <li>
              Provide a credential-free HTTPS receiver. Private destinations
              need an exact tenant-assigned route in operator policy;
              certificate trust is configured separately. A host filesystem path
              is not automatically available inside a container.
            </li>
            <li>
              Register the endpoint with webhook.manage and webhook.read, plus
              webhooks.manage when OAuth scopes are enforced. Install the
              returned signing secret in your receiver. Registration, reads and
              rotation do not contact it; Send test makes a real request.
            </li>
            <li>
              Verify signatures over exact bytes, durably accept and deduplicate
              notifications, and fetch current Catalog state with separate read
              permissions. Tenant settings changes are audit-only; wildcard
              subscriptions do not include private audit or internal events.
            </li>
          </ol>
        </details>
        {links.length > 0 && (
          <div className="workspace-actions">
            {links.map((link) => (
              <a
                className="button"
                key={link.href}
                href={link.href}
                target="_blank"
                rel="noopener noreferrer"
              >
                {link.label} (opens in a new tab)
              </a>
            ))}
          </div>
        )}
      </section>
    </details>
  );
}

/** Keep a newly returned signer within this creation page and access context. */
function WebhookCreate(props: WorkspaceProps) {
  const [secret, setSecret] = useState<SecretReply>();
  return (
    <>
      <WebhookEditor {...props} secret={secret} onSecret={setSecret} />
      {secret && (
        <SigningSecret value={secret} onDismiss={() => setSecret(undefined)} />
      )}
    </>
  );
}

/** Page endpoint summaries and expose explicit creation and detail navigation. */
function WebhookList(props: WorkspaceProps) {
  const [cursor, setCursor] = useState("");
  const [name, setName] = useState("");
  const [status, setStatus] = useState(() =>
    webhookParameter(location.search, "status"),
  );
  const [filters, setFilters] = useState({ name, status });
  const [endpointId, setEndpointId] = useState("");
  useEffect(() => {
    const moved = () => {
      if (location.pathname !== `${props.runtime.basePath}/webhooks`) return;
      const next = webhookParameter(location.search, "status");
      setName("");
      setStatus(next);
      setFilters({ name: "", status: next });
      setCursor("");
    };
    window.addEventListener("popstate", moved);
    return () => window.removeEventListener("popstate", moved);
  }, [props.runtime.basePath]);
  const page = useRead(
    JSON.stringify([filters, cursor]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/webhooks", {
          params: {
            query: {
              limit: 25,
              ...(filters.name ? { name: filters.name } : {}),
              ...(filters.status
                ? { status: filters.status as "active" | "disabled" }
                : {}),
              ...(cursor ? { cursor } : {}),
            },
          },
          signal,
        }),
        checks.WebhookPage,
      ),
    props.onFailure,
  );
  return (
    <>
      <form
        className="workspace-toolbar"
        aria-label="Endpoint filters"
        onSubmit={(event) => {
          event.preventDefault();
          setCursor("");
          setFilters({ name, status });
          history.pushState(
            null,
            "",
            webhookDirectoryURL(props.runtime.basePath, status),
          );
          page.refresh();
        }}
      >
        <Field
          label="Webhook name (exact match)"
          hint="Enter the complete saved name, including capital letters. More than one webhook can have this name."
        >
          <input
            maxLength={320}
            value={name}
            onChange={(event) => setName(event.target.value)}
          />
        </Field>
        <Field label="Status">
          <select
            value={status}
            onChange={(event) => setStatus(event.target.value as typeof status)}
          >
            <option value="">All statuses</option>
            {!["", "active", "disabled"].includes(status) && (
              <option value={status}>Unsupported status</option>
            )}
            <option value="active">Active</option>
            <option value="disabled">Disabled</option>
          </select>
        </Field>
        <div className="workspace-actions">
          <button disabled={page.busy}>Apply endpoint filters</button>
          <button
            type="button"
            disabled={page.busy}
            onClick={() => {
              setName("");
              setStatus("");
              setFilters({ name: "", status: "" });
              history.pushState(
                null,
                "",
                webhookDirectoryURL(props.runtime.basePath, ""),
              );
              setCursor("");
              page.refresh();
            }}
          >
            Clear endpoint filters
          </button>
        </div>
      </form>
      <form
        className="workspace-toolbar workspace-lookup"
        aria-label="Open exact endpoint"
        onSubmit={(event) => {
          event.preventDefault();
          const value = endpointId.trim().toLowerCase();
          if (event.currentTarget.reportValidity())
            window.location.assign(
              `${props.runtime.basePath}/webhooks/${encodeURIComponent(value)}`,
            );
        }}
      >
        <Field
          label="Exact endpoint ID"
          hint="Uses the authorized detail read, independently of list filters."
        >
          <input
            required
            pattern="[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
            value={endpointId}
            onChange={(event) => setEndpointId(event.target.value)}
          />
        </Field>
        <div className="workspace-actions">
          <button>Open endpoint</button>
        </div>
      </form>
      <div className="workspace-toolbar">
        {permits(props.access, "webhook.manage", "webhooks.manage") && (
          <a
            className="button primary"
            href={`${props.runtime.basePath}/webhooks/new`}
          >
            Create webhook
          </a>
        )}
        <button
          disabled={page.busy}
          onClick={() =>
            page.error && cursor ? setCursor("") : page.refresh()
          }
        >
          {controlIcons["refresh"]} Refresh
        </button>
      </div>
      <ReadState
        {...page}
        refresh={() => (page.error && cursor ? setCursor("") : page.refresh())}
      />
      {page.data && (
        <section className="workspace-panel">
          {page.data.items.length > 0 && (
            <ul className="workspace-record-list">
              {page.data.items.map((i) => (
                <li key={i.id}>
                  <a href={`${props.runtime.basePath}/webhooks/${i.id}`}>
                    <span className="record-list-icon">
                      <Icon name="webhook" />
                    </span>
                    <span>{i.name}</span>
                    <Icon name="chevron" size={16} />
                  </a>
                  <StatusLabel
                    state={i.status}
                    tone={i.status === "active" ? "info" : "neutral"}
                  />
                  <div className="webhook-summary">
                    <p>Receiver: {i.url}</p>
                    <p>
                      Events: {i.eventTypes.map(webhookEventLabel).join(", ")}
                    </p>
                    <details>
                      <summary>Exact event type codes</summary>
                      <ul>
                        {i.eventTypes.map((event) => (
                          <li key={event}>
                            <code>{event}</code>
                          </li>
                        ))}
                      </ul>
                    </details>
                  </div>
                  <CopyReference label="endpoint ID" value={i.id} />
                </li>
              ))}
            </ul>
          )}
          {!page.data.items.length && (
            <div className="workspace-empty">
              <span className="empty-icon">
                <Icon name="webhook" size={28} />
              </span>
              <h2>
                {filters.name || filters.status
                  ? "No matching webhooks"
                  : "No webhooks yet"}
              </h2>
              <p>
                {filters.name || filters.status
                  ? "No endpoints match these filters. Adjust or clear them to browse again."
                  : "No webhook endpoints yet. Create one when you have an HTTPS receiver ready. Enabling webhooks alone does not send anything or connect to an external service."}
              </p>
            </div>
          )}
        </section>
      )}
      <Pager
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        busy={page.busy}
        onPage={setCursor}
      />
    </>
  );
}

/** Load the selected endpoint and its ETag before enabling management actions. */
function WebhookDetail(props: WorkspaceProps) {
  const [secret, setSecret] = useState<SecretReply>();
  const [rotation, setRotation] = useState<{
    receipt: RotationReceipt;
    readAtReceipt:
      | { item: components["schemas"]["Webhook"]; etag: string | null }
      | undefined;
  }>();
  const [replacement, setReplacement] =
    useState<components["schemas"]["Webhook"]>();
  const [superseded, setSuperseded] = useState<string | null>();
  const [preparedRevision, setPreparedRevision] = useState<string | null>();
  const onFailure = (error: unknown) => {
    if (error instanceof APIError && error.status === 412)
      setSuperseded(preparedRevision);
    props.onFailure(error);
  };
  const detail = useRead(
    props.id,
    async (signal) => {
      const reply = await props.client.api.GET("/api/v1/webhooks/{webhookId}", {
        params: { path: { webhookId: props.id } },
        signal,
      });
      return {
        item: result(
          reply,
          (value) => checks.Webhook(value) && checksWebhookID(value, props.id),
        ),
        etag: reply.response.headers.get("ETag"),
      };
    },
    props.onFailure,
    true,
  );
  const current =
    !!detail.data?.etag &&
    detail.data.etag !== superseded &&
    !detail.busy &&
    !detail.error;
  // The retained pre-command read cannot invalidate a new receipt. A later
  // successful read can, including a lower version observed after a restore.
  const obsoleteRotation =
    !!rotation &&
    !!detail.data &&
    (detail.data.item.secretVersion > rotation.receipt.secretVersion ||
      detail.data.item.status !== "active" ||
      (detail.data !== rotation.readAtReceipt &&
        !detail.busy &&
        !detail.error &&
        detail.data.item.secretVersion !== rotation.receipt.secretVersion));
  const knownRotation = obsoleteRotation ? undefined : rotation?.receipt;
  useLayoutEffect(() => {
    if (obsoleteRotation) setRotation(undefined);
  }, [obsoleteRotation]);
  return (
    <>
      <div className="workspace-actions">
        <a href={`${props.runtime.basePath}/webhooks`}>Back to webhooks</a>
        <button disabled={detail.busy} onClick={detail.refresh}>
          {controlIcons["refresh"]} Reload endpoint
        </button>
      </div>
      <ReadState {...detail} />
      {!current && detail.data && (
        <p className="notice">
          Reload the endpoint and compare its current revision before preparing
          another change. Your drafts and signing secrets remain in this page.
        </p>
      )}
      {detail.data && (
        <>
          <section
            className="workspace-panel"
            aria-label="Last loaded endpoint"
          >
            <h2>{detail.data.item.name}</h2>
            <Facts
              rows={[
                ["Receiver URL", detail.data.item.url],
                [
                  "Configured state",
                  <StatusLabel
                    state={detail.data.item.status}
                    tone={
                      detail.data.item.status === "active" ? "info" : "neutral"
                    }
                  />,
                ],
                ["Endpoint revision", detail.data.item.revision],
                [
                  "Events",
                  detail.data.item.eventTypes.map(webhookEventLabel).join(", "),
                ],
              ]}
            />
            <details>
              <summary>Endpoint technical details</summary>
              <p>
                Current validator: <code>{detail.data.etag}</code>
              </p>
              <ul>
                {detail.data.item.eventTypes.map((event) => (
                  <li key={event}>
                    <code>{event}</code>
                  </li>
                ))}
              </ul>
            </details>
          </section>
          <Facts
            rows={[
              [
                "Endpoint ID",
                <CopyReference
                  label="endpoint ID"
                  value={detail.data.item.id}
                />,
              ],
              ["Signing version", detail.data.item.secretVersion],
              ["Created", <DateValue value={detail.data.item.createdAt} />],
              [
                "Last updated",
                <DateValue value={detail.data.item.updatedAt} />,
              ],
            ]}
          />
          {detail.data.item.status === "disabled" && (
            <p className="notice">
              This endpoint is permanently disabled. Its configuration and
              delivery evidence remain; it cannot be re-enabled or replay old
              notifications. A replacement is a separate endpoint and signing
              key, and does not backfill earlier events.
            </p>
          )}
          {detail.data.item.status === "disabled" &&
            permits(props.access, "webhook.manage", "webhooks.manage") &&
            !replacement && (
              <button
                disabled={!current}
                onClick={() =>
                  setReplacement({
                    ...detail.data!.item,
                    eventTypes: [...detail.data!.item.eventTypes],
                  })
                }
              >
                {controlIcons["add"]} Create replacement
              </button>
            )}
          <WebhookEditor
            key={replacement ? "replacement" : "existing"}
            {...props}
            {...(replacement
              ? {
                  replacementOf: replacement,
                  onCancel: () => setReplacement(undefined),
                }
              : detail.data)}
            secret={secret}
            onSecret={(reply) => {
              setSecret(reply);
              setRotation(undefined);
            }}
            current={current}
            onFailure={onFailure}
            onPrepared={setPreparedRevision}
            onChanged={setSuperseded}
          />
          <WebhookActions
            {...props}
            {...detail.data}
            current={current}
            onFailure={onFailure}
            onPrepared={setPreparedRevision}
            onChanged={setSuperseded}
            secret={secret}
            onSecret={(reply, mode) => {
              setSecret(reply);
              setRotation({
                receipt: {
                  endpointId: reply.endpoint.id,
                  secretVersion: reply.endpoint.secretVersion,
                  mode,
                  ...(reply.previousSecretExpiresAt
                    ? { previousSecretExpiresAt: reply.previousSecretExpiresAt }
                    : {}),
                },
                readAtReceipt: detail.data,
              });
            }}
          />
          <RotationGuidance item={detail.data.item} receipt={knownRotation} />
          {secret && (
            <SigningSecret
              value={secret}
              onDismiss={() => setSecret(undefined)}
            />
          )}
          {permits(
            props.access,
            "webhook.read_evidence",
            "webhooks.manage",
          ) && <Deliveries {...props} />}
        </>
      )}
    </>
  );
}

/** Prepare endpoint creation or revision from explicit destination and event inputs. */
function WebhookEditor(
  props: WorkspaceProps & {
    item?: components["schemas"]["Webhook"];
    replacementOf?: components["schemas"]["Webhook"];
    onCancel?: () => void;
    secret: SecretReply | undefined;
    onSecret: (reply: SecretReply) => void;
    etag?: string | null;
    current?: boolean;
    onChanged?: (etag: string | null) => void;
    onPrepared?: (etag: string | null) => void;
  },
) {
  const initial = props.item ?? props.replacementOf;
  const [name, setName] = useState(initial?.name || "");
  const [url, setURL] = useState(initial?.url || "");
  const [events, setEvents] = useState(initial?.eventTypes.join("\n") || "");
  const [replaceSecret, setReplaceSecret] = useState(false);
  useLayoutEffect(() => setReplaceSecret(false), [props.secret]);
  const [created, setCreated] = useState("");
  const changed =
    name !== (initial?.name || "") ||
    url !== (initial?.url || "") ||
    events !== (initial?.eventTypes.join("\n") || "");
  const editable =
    permits(props.access, "webhook.manage", "webhooks.manage") &&
    (!props.item || props.item.status === "active");
  // Read-only controls show the last loaded record, while editable fields keep
  // their private draft through readback and failed requests.
  const fields =
    !editable && initial
      ? {
          name: initial.name,
          url: initial.url,
          events: initial.eventTypes.join("\n"),
        }
      : { name, url, events };
  const title = props.item
    ? "Update webhook"
    : props.replacementOf
      ? "Create replacement webhook"
      : "Create webhook";
  return (
    <>
      <ActionForm
        title={title}
        icon={props.item ? "save" : "add"}
        description={
          props.item
            ? "Compare saved and proposed configuration before updating. Queued notifications use the eligible endpoint's current URL and subscriptions when delivery starts. An in-flight request cannot be recalled."
            : props.replacementOf
              ? "Create a separate endpoint from the disabled endpoint's non-secret configuration. The original endpoint and key remain unchanged. Creation returns a new signing secret and does not backfill earlier events."
              : "Enter your receiver's HTTPS address and choose the notifications it needs. After creation, save the signing secret in your receiver. Notifications can be repeated or arrive out of order; receivers should fetch current Catalog state."
        }
        editable={editable}
        unavailableReason={
          !editable
            ? "Editing requires webhook.manage and, when enforced, webhooks.manage scope, plus an active endpoint. Saved configuration remains available for inspection."
            : "Reload the endpoint successfully and compare its current ETag before preparing a change."
        }
        allowed={
          permits(props.access, "webhook.manage", "webhooks.manage") &&
          (!props.item ||
            (!!props.current && !!props.etag && props.item.status === "active"))
        }
        dirty={changed}
        {...(props.item
          ? {
              another: {
                label: "Edit endpoint again",
                reset: () => {
                  setName(props.item!.name);
                  setURL(props.item!.url);
                  setEvents(props.item!.eventTypes.join("\n"));
                },
              },
            }
          : {})}
        onFailure={props.onFailure}
        prepare={() => {
          if (!props.item && props.secret && !replaceSecret)
            throw new Error(
              "Save or clear the existing signing secret, or acknowledge its replacement first.",
            );
          const destination = new URL(url);
          if (
            destination.protocol !== "https:" ||
            destination.username ||
            destination.password ||
            destination.hash
          )
            throw new Error(
              "Use a credential-free HTTPS endpoint without a fragment.",
            );
          const eventTypes = normalizeWebhookEvents(events);
          const body = {
            name: characterText(name.trim(), "Name", 160, 1),
            url,
            eventTypes,
          };
          const item = props.item;
          const etag = props.etag;
          const savedEvents = new Set<string>(item?.eventTypes);
          if (
            item &&
            body.name === item.name &&
            body.url === item.url &&
            body.eventTypes.length === item.eventTypes.length &&
            body.eventTypes.every((value) => savedEvents.has(value))
          )
            throw new Error(
              "Change the name, receiver URL or subscriptions before updating.",
            );
          props.onPrepared?.(etag ?? null);
          return {
            summary: commandSummary([
              ["Action", item ? "Update webhook" : "Create webhook"] as const,
              ["Webhook ID", item?.id ?? "New endpoint"] as const,
              ...(item
                ? [
                    ["Saved name", item.name] as const,
                    ["Saved receiver", item.url] as const,
                    ["Saved events", item.eventTypes.join(", ")] as const,
                  ]
                : []),
              ["Proposed name", body.name] as const,
              ["Proposed receiver", body.url] as const,
              ["Proposed events", body.eventTypes.join(", ")] as const,
              ["Revision", etag ?? "New endpoint"] as const,
              ...(props.replacementOf
                ? [
                    [
                      "Disabled original endpoint",
                      props.replacementOf.id,
                    ] as const,
                    ["Historical events", "No backfill"] as const,
                  ]
                : []),
              ...(!item && props.secret
                ? [
                    [
                      "Uncleared signing secret",
                      "Replacement acknowledged",
                    ] as const,
                  ]
                : []),
            ]),
            run: async (key) => {
              if (item) {
                result(
                  await props.client.api.PATCH("/api/v1/webhooks/{webhookId}", {
                    params: {
                      path: { webhookId: item.id },
                      header: { "Idempotency-Key": key, "If-Match": etag! },
                    },
                    body,
                  }),
                  checks.Webhook,
                );
                props.onChanged?.(etag!);
                return "Webhook updated. Reload endpoint before another change.";
              }
              const reply = result(
                await props.client.api.POST("/api/v1/webhooks", {
                  params: { header: { "Idempotency-Key": key } },
                  body,
                }),
                checks.WebhookSecretResult,
              );
              props.onSecret(reply);
              setCreated(reply.endpoint.id);
              return "Webhook created. Save the signing secret before leaving.";
            },
          };
        }}
      >
        {!props.item && (
          <SecretReplacement
            value={props.secret}
            accepted={replaceSecret}
            onChange={setReplaceSecret}
          />
        )}
        <Field
          label="Webhook name"
          hint="A friendly name to recognize this integration, such as Compliance checks."
        >
          <input
            required
            maxLength={320}
            value={fields.name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <Field
          label="Receiver URL"
          hint="Your service's HTTPS notification endpoint, for example https://hooks.example.com/catalog. Do not include a password or token in the URL."
          help="The deployment's outbound network policy must allow this destination. Private and loopback addresses are blocked unless the administrator has explicitly configured an allowed receiver. The Catalog signs notifications; it does not host the receiving service."
        >
          <input
            required
            type="url"
            maxLength={2048}
            value={fields.url}
            onChange={(e) => setURL(e.target.value)}
          />
        </Field>
        <fieldset className="relationship-row">
          <legend>Events to send</legend>
          <p>
            Choose from {webhookChoices.length - 1} public event types. Use
            common choices below, or enter specific event types in Advanced
            subscriptions. All events subscribes to every supported public
            event, including future public types. Specific choices do not narrow
            a wildcard subscription. Private audit and internal events are never
            included.
          </p>
          <WebhookEventChoices
            choices={commonWebhookChoices}
            events={fields.events}
            onChange={setEvents}
          />
          <details className="workspace-advanced">
            <summary>
              More public event types ({moreWebhookChoices.length})
            </summary>
            <WebhookEventChoices
              choices={moreWebhookChoices}
              events={fields.events}
              onChange={setEvents}
            />
          </details>
          <details className="workspace-advanced">
            <summary>Advanced subscriptions</summary>
            <Field
              label="Event type codes"
              hint="Enter 1-64 event type codes, separated by commas or new lines. The checkboxes edit the same list."
              help="Exact lowercase aliases such as resource.created use the listed versioned type. The 64-selector input limit is checked before duplicates are removed."
            >
              <textarea
                rows={6}
                placeholder="com.open.agentic.catalog.resource.created.v1"
                value={fields.events}
                onChange={(e) => setEvents(e.target.value)}
              />
            </Field>
          </details>
        </fieldset>
        {props.onCancel && (
          <button
            type="button"
            onClick={() => {
              if (
                !changed ||
                window.confirm(
                  "Discard this unsaved replacement configuration?",
                )
              )
                props.onCancel?.();
            }}
          >
            Cancel replacement
          </button>
        )}
      </ActionForm>
      {created && (
        <a href={`${props.runtime.basePath}/webhooks/${created}`}>
          Open webhook
        </a>
      )}
    </>
  );
}

/** Show exact selectors alongside readable labels, with no duplicate checkbox choices. */
function WebhookEventChoices({
  choices,
  events,
  onChange,
}: {
  choices: typeof commonWebhookChoices;
  events: string;
  onChange: (value: string) => void;
}) {
  return (
    <div className="event-choices">
      {choices.map(([value, label]) => (
        <label key={value} className="event-choice" title={value}>
          <input
            type="checkbox"
            aria-label={label}
            checked={webhookEventSelected(events, value)}
            onChange={(event) =>
              onChange(toggleWebhookEvent(events, value, event.target.checked))
            }
          />
          <span>
            {label}
            <code>{value}</code>
          </span>
        </label>
      ))}
    </div>
  );
}

/** Keep exact non-secret rotation evidence after plaintext is deliberately cleared. */
function RotationGuidance({
  item,
  receipt,
}: {
  item: components["schemas"]["Webhook"];
  receipt: RotationReceipt | undefined;
}) {
  if (!receipt && item.status !== "active") return null;
  return (
    <section
      className="workspace-panel"
      aria-label="Signing-key rotation guidance"
    >
      <h2>Signing-key rotation</h2>
      {receipt ? (
        <>
          <p>
            Observed {receipt.mode} rotation for endpoint {receipt.endpointId},
            signing version {receipt.secretVersion}. This command result is
            separate from the last loaded endpoint state.
          </p>
          {receipt.mode === "emergency" ? (
            <p>
              Catalog stops adding previous-key signatures to new requests.
              Replace the keys accepted by your receiver immediately. In-flight
              requests cannot be recalled.
            </p>
          ) : receipt.previousSecretExpiresAt ? (
            <p>
              Catalog includes a signature made with the previous secret until{" "}
              <DateValue value={receipt.previousSecretExpiresAt} />. Install the
              new secret in your receiver before then. This deadline does not
              prove which key your receiver accepts.
            </p>
          ) : (
            <p>
              No predecessor deadline was supplied with this result. No overlap
              deadline is known on this page.
            </p>
          )}
          <p>
            This non-secret receipt stays in page memory after you clear the
            plaintext. A successful reload showing a different signing version
            or a disabled endpoint invalidates it. Leaving this endpoint or
            changing access clears it.
          </p>
        </>
      ) : (
        <p>
          No rotation deadline is known on this page. Reloading cannot recover
          an earlier secret or its overlap deadline. The last updated time does
          not establish whether overlap is active.
        </p>
      )}
    </section>
  );
}

/** Require a deliberate decision before another command can replace uncleared one-time material. */
function SecretReplacement({
  value,
  accepted,
  onChange,
}: {
  value: SecretReply | undefined;
  accepted: boolean;
  onChange: (value: boolean) => void;
}) {
  if (!value) return null;
  return (
    <label className="event-choice">
      <input
        type="checkbox"
        required
        checked={accepted}
        onChange={(event) => onChange(event.target.checked)}
      />
      I understand the signing secret still on this page will be replaced. Save
      it first if needed.
    </label>
  );
}

/** Display a returned signing secret in transient component state for deliberate copying. */
function SigningSecret({
  value,
  onDismiss,
}: {
  value: SecretReply;
  onDismiss: () => void;
}) {
  const [reveal, setReveal] = useState(false);
  const [copyStatus, setCopyStatus] = useState("");
  const copyLifetime = useRef({ generation: 0 });
  useLayoutEffect(() => {
    setReveal(false);
    setCopyStatus("");
    const lifetime = copyLifetime.current;
    ++lifetime.generation;
    return () => {
      ++lifetime.generation;
    };
  }, [value]);
  useUnsaved(true);
  return (
    <section className="workspace-panel">
      <h2>{controlIcons["key"]} Save the signing secret</h2>
      <p>
        The UI keeps this value only in this page's memory. It cannot be
        retrieved from the endpoint detail.
      </p>
      <p>
        Signing secret for endpoint {value.endpoint.id}, signing version{" "}
        {value.endpoint.secretVersion}, endpoint revision{" "}
        {value.endpoint.revision}.
      </p>
      <Field label="Signing secret">
        <input
          readOnly
          autoComplete="off"
          type={reveal ? "text" : "password"}
          value={value.secret}
        />
      </Field>
      <div className="workspace-toolbar">
        <button onClick={() => setReveal((v) => !v)}>
          {controlIcons["eye"]} {reveal ? "Hide secret" : "Reveal secret"}
        </button>
        <button
          disabled={copyStatus === "pending"}
          onClick={() => {
            const lifetime = copyLifetime.current;
            const current = ++lifetime.generation;
            setCopyStatus("pending");
            void (async () => {
              try {
                await navigator.clipboard.writeText(value.secret);
                if (current === lifetime.generation) setCopyStatus("copied");
              } catch {
                if (current === lifetime.generation) setCopyStatus("failed");
              }
            })();
          }}
        >
          {controlIcons["copy"]} Copy secret
        </button>
        <button onClick={onDismiss}>
          {controlIcons["close"]} Clear secret from this page
        </button>
      </div>
      <p>
        An already-started clipboard write cannot be cancelled by this page.
        Clear your clipboard when finished, including after a replacement or
        page change.
      </p>
      {copyStatus === "pending" && <p role="status">Copying signing secret.</p>}
      {copyStatus === "copied" && (
        <p role="status">Copied. Clear your clipboard when finished.</p>
      )}
      {copyStatus === "failed" && (
        <p role="alert" className="notice error">
          Clipboard access failed. Reveal and copy it manually.
        </p>
      )}
    </section>
  );
}

/** Prepare endpoint lifecycle and key-maintenance commands against the loaded revision. */
function WebhookActions(
  props: WorkspaceProps & {
    item: components["schemas"]["Webhook"];
    etag: string | null;
    current: boolean;
    onChanged: (etag: string | null) => void;
    onPrepared: (etag: string | null) => void;
    secret: SecretReply | undefined;
    onSecret: (reply: SecretReply, mode: RotationMode) => void;
  },
) {
  const actions = [
    {
      value: "test",
      label: "Send test",
      permission: "webhook.test",
      help: "Makes one real outbound request; delivery evidence requires its separate permission.",
    },
    {
      value: "rotate",
      label: "Rotate secret",
      permission: "webhook.rotate",
      help: "Returns new one-time signing material; does not contact or configure the receiver.",
    },
    {
      value: "disable",
      label: "Disable endpoint",
      permission: "webhook.manage",
      help: "Permanently stops future delivery; evidence remains and no re-enable or replay exists.",
    },
  ] as const;
  const initialAction =
    actions.find((choice) =>
      permits(props.access, choice.permission, "webhooks.manage"),
    )?.value ?? "test";
  const [action, setAction] = useState<"test" | "rotate" | "disable">(
    initialAction,
  );
  const [emergency, setEmergency] = useState(false);
  const [replaceSecret, setReplaceSecret] = useState(false);
  useLayoutEffect(() => setReplaceSecret(false), [props.secret]);
  const [delivery, setDelivery] =
    useState<components["schemas"]["WebhookDelivery"]>();
  return (
    <>
      <ActionForm
        title="Manage webhook"
        icon={
          action === "disable"
            ? "remove"
            : action === "rotate"
              ? "key"
              : "webhook"
        }
        intent={
          action === "disable"
            ? "danger"
            : action === "rotate" && emergency
              ? "warning"
              : "primary"
        }
        description={
          action === "rotate"
            ? emergency
              ? "Catalog stops signing with previous secrets immediately. Replace the keys accepted by your receiver immediately. In-flight requests cannot be recalled."
              : "Catalog signs with both secrets for 15 minutes. Save and install the new secret in your receiver."
            : action === "disable"
              ? "Permanently disable future delivery for this endpoint. Existing configuration and evidence remain; there is no re-enable or replay operation. An in-flight request cannot be recalled. A replacement is a new endpoint and key with no historical backfill."
              : "Send one synthetic test notification to this HTTPS receiver. This makes one real attempt, ignores subscriptions and does not prove worker delivery or receiver processing."
        }
        editable={
          props.item.status === "active" &&
          actions.some((choice) =>
            permits(props.access, choice.permission, "webhooks.manage"),
          )
        }
        unavailableReason={
          props.item.status !== "active"
            ? "Disabled endpoints cannot be tested, rotated or re-enabled. Inspect retained evidence with its separate permission, or explicitly create a replacement with webhook.manage."
            : !props.current || !props.etag
              ? "Reload the endpoint successfully and compare its current ETag before preparing another action."
              : "This action requires its own webhook permission and webhooks.manage OAuth scope when enforcement is enabled."
        }
        allowed={
          props.current &&
          !!props.etag &&
          props.item.status === "active" &&
          permits(
            props.access,
            action === "test"
              ? "webhook.test"
              : action === "rotate"
                ? "webhook.rotate"
                : "webhook.manage",
            "webhooks.manage",
          )
        }
        another={{
          label: "Prepare another webhook action",
          reset: () => {
            setAction(initialAction);
            setEmergency(false);
          },
        }}
        onFailure={props.onFailure}
        prepare={() => {
          if (action === "rotate" && props.secret && !replaceSecret)
            throw new Error(
              "Save or clear the existing signing secret, or acknowledge its replacement first.",
            );
          props.onPrepared(props.etag);
          const params = {
            path: { webhookId: props.item.id },
            header: { "Idempotency-Key": "", "If-Match": props.etag! },
          };
          const chosen = action;
          const revokePrevious = emergency;
          return {
            summary: commandSummary([
              [
                "Action",
                chosen === "rotate"
                  ? "Rotate secret"
                  : chosen === "disable"
                    ? "Disable endpoint"
                    : "Send test notification",
              ] as const,
              ["Webhook ID", props.item.id] as const,
              ["Name", props.item.name] as const,
              ["Receiver", props.item.url] as const,
              ["Events", props.item.eventTypes.join(", ")] as const,
              ["Revision", props.etag!] as const,
              ...(chosen === "rotate"
                ? [
                    [
                      "Previous secrets",
                      revokePrevious
                        ? "Revoke immediately"
                        : "Overlap for 15 minutes",
                    ] as const,
                  ]
                : []),
              ...(chosen === "rotate" && props.secret
                ? [
                    [
                      "Uncleared signing secret",
                      "Replacement acknowledged",
                    ] as const,
                  ]
                : []),
              ...(chosen === "disable"
                ? [
                    [
                      "Lifecycle",
                      "Permanently disabled; no re-enable or replay",
                    ] as const,
                    ["In-flight delivery", "Cannot be recalled"] as const,
                    [
                      "Replacement",
                      "Separate endpoint and key; no historical backfill",
                    ] as const,
                  ]
                : []),
            ]),
            run: async (key) => {
              params.header["Idempotency-Key"] = key;
              if (chosen === "disable") {
                const reply = await props.client.api.DELETE(
                  "/api/v1/webhooks/{webhookId}",
                  { params },
                );
                acknowledge(reply);
                props.onChanged(params.header["If-Match"]);
                return "Webhook disabled.";
              }
              if (chosen === "rotate") {
                props.onSecret(
                  result(
                    await props.client.api.POST(
                      "/api/v1/webhooks/{webhookId}:rotate-secret",
                      { params, body: { revokePrevious } },
                    ),
                    (value) =>
                      checks.WebhookSecretResult(value) &&
                      object(value) &&
                      checksWebhookID(value.endpoint, props.item.id) &&
                      (!revokePrevious ||
                        value.previousSecretExpiresAt === undefined),
                  ),
                  revokePrevious ? "emergency" : "routine",
                );
                props.onChanged(params.header["If-Match"]);
                return "Secret rotated. Save the new signing secret.";
              }
              const tested = result(
                await props.client.api.POST(
                  "/api/v1/webhooks/{webhookId}:test",
                  {
                    params,
                    body: {},
                  },
                ),
                (value) =>
                  checks.WebhookDelivery(value) &&
                  object(value) &&
                  value.endpointId === props.item.id,
              );
              setDelivery(tested);
              return tested.state === "succeeded"
                ? "Test notification delivered."
                : "Test notification was not delivered. Inspect its delivery result.";
            },
          };
        }}
      >
        <Field label="Webhook action">
          <select
            value={action}
            onChange={(e) => setAction(e.target.value as typeof action)}
          >
            {actions.map((choice) => (
              <option
                key={choice.value}
                value={choice.value}
                disabled={
                  !permits(props.access, choice.permission, "webhooks.manage")
                }
              >
                {choice.label}
              </option>
            ))}
          </select>
        </Field>
        {action === "rotate" && (
          <>
            <Field label="Rotation mode">
              <select
                value={emergency ? "emergency" : "routine"}
                onChange={(e) => setEmergency(e.target.value === "emergency")}
              >
                <option value="routine">
                  Routine - overlap previous secret
                </option>
                <option value="emergency">
                  Emergency - revoke previous secrets
                </option>
              </select>
            </Field>
            <p>
              Another routine rotation is refused while a previous signing
              overlap is active. Wait for its deadline, or deliberately choose
              emergency rotation to revoke previous signatures. The Catalog does
              not change the keys your receiver accepts.
            </p>
            <details className="workspace-advanced">
              <summary>Lost a signing secret?</summary>
              <ol>
                <li>
                  If the original request outcome is uncertain, recover that
                  exact command with its original key and captured input. Do not
                  start another rotation to discover its outcome.
                </li>
                <li>
                  If a known-success key was discarded and original recovery is
                  unavailable, reload this endpoint before preparing a
                  deliberately new rotation with the current ETag.
                </li>
                <li>
                  If routine overlap blocks that new rotation, wait for expiry
                  or explicitly select emergency rotation. Emergency rotation
                  never happens automatically. Deployment encryption-key
                  replacement is a separate stopped-runtime operator procedure.
                </li>
              </ol>
            </details>
          </>
        )}
        {action === "rotate" && props.item.status === "active" && (
          <SecretReplacement
            value={props.secret}
            accepted={replaceSecret}
            onChange={setReplaceSecret}
          />
        )}
        <p>
          {actions.find((choice) => choice.value === action)!.help} Requires{" "}
          {actions.find((choice) => choice.value === action)!.permission} and
          webhook.read, plus webhooks.manage scope when enforced.
        </p>
        <details className="workspace-advanced">
          <summary>Other webhook actions and permissions</summary>
          <ul aria-label="Webhook action permissions">
            {actions
              .filter((choice) => choice.value !== action)
              .map((choice) => (
                <li key={choice.value}>
                  {choice.label}:{" "}
                  {permits(props.access, choice.permission, "webhooks.manage")
                    ? "Permission available"
                    : "Not permitted"}
                  . Requires {choice.permission} and webhook.read, plus
                  webhooks.manage scope when enforced. {choice.help}
                </li>
              ))}
          </ul>
        </details>
      </ActionForm>
      {action === "test" && (
        <details className="workspace-advanced">
          <summary>Test failures and safe recovery</summary>
          <p>
            A WEBHOOK_TEST_BUSY refusal means the API sent nothing. Return to
            the form and prepare a later test with a fresh key. Other server or
            transport failures can leave receiver acceptance uncertain, even
            without a saved delivery row. Recover the original command with its
            exact key. Network policy, certificate trust and transport failures
            are possible causes, not confirmed diagnoses.
          </p>
        </details>
      )}
      {delivery && (
        <section className="workspace-panel" aria-label="Test delivery result">
          <h2>Test delivery result</h2>
          <DeliveryOutcome item={delivery} />
          <p>
            This is the observed test result, independent of later endpoint or
            history readback. HTTP success does not establish receiver
            processing. Tests make one attempt and are not retried by the
            worker.
          </p>
          <EvidenceDetails
            title="Complete test delivery evidence"
            value={delivery}
          />
        </section>
      )}
    </>
  );
}

/** Read a bounded page of delivery evidence for the selected endpoint. */
function Deliveries(props: WorkspaceProps) {
  const [cursor, setCursor] = useState("");
  const [deliveryId, setDeliveryId] = useState(
    () => webhookDeliveryFilters(location.search).deliveryId,
  );
  const [eventId, setEventId] = useState(
    () => webhookDeliveryFilters(location.search).eventId,
  );
  const [state, setState] = useState(
    () => webhookDeliveryFilters(location.search).state,
  );
  const [filters, setFilters] = useState({ deliveryId, eventId, state });
  useEffect(() => {
    const moved = () => {
      if (
        location.pathname !== `${props.runtime.basePath}/webhooks/${props.id}`
      )
        return;
      const next = webhookDeliveryFilters(location.search);
      setDeliveryId(next.deliveryId);
      setEventId(next.eventId);
      setState(next.state);
      setFilters(next);
      setCursor("");
    };
    window.addEventListener("popstate", moved);
    return () => window.removeEventListener("popstate", moved);
  }, [props.runtime.basePath, props.id]);
  const page = useRead(
    JSON.stringify([props.id, filters, cursor]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/webhooks/{webhookId}/deliveries", {
          params: {
            path: { webhookId: props.id },
            query: {
              limit: 25,
              ...(filters.deliveryId ? { deliveryId: filters.deliveryId } : {}),
              ...(filters.eventId ? { eventId: filters.eventId } : {}),
              ...(filters.state
                ? {
                    state:
                      filters.state as components["schemas"]["WebhookDelivery"]["state"],
                  }
                : {}),
              ...(cursor ? { cursor } : {}),
            },
          },
          signal,
        }),
        checks.WebhookDeliveryPage,
      ),
    props.onFailure,
  );
  return (
    <section className="workspace-panel">
      <h2>Delivery history</h2>
      <p>
        Each row shows the latest saved outcome for one event. The attempt count
        may differ from requests received by your service. Waiting outcomes can
        move between pages; refresh from the first page to reconcile. Older
        evidence may have expired. No resend is available.
      </p>
      <form
        className="workspace-toolbar"
        aria-label="Delivery filters"
        onSubmit={(event) => {
          event.preventDefault();
          setCursor("");
          setFilters({ deliveryId, eventId, state });
          history.pushState(
            null,
            "",
            webhookDeliveryURL(props.runtime.basePath, props.id, {
              deliveryId,
              eventId,
              state,
            }),
          );
          page.refresh();
        }}
      >
        <Field label="Exact delivery ID">
          <input
            pattern="[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
            value={deliveryId}
            onChange={(event) => setDeliveryId(event.target.value)}
          />
        </Field>
        <Field label="Exact event ID">
          <input
            pattern="[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
            value={eventId}
            onChange={(event) => setEventId(event.target.value)}
          />
        </Field>
        <Field label="Delivery state">
          <select
            value={state}
            onChange={(event) => setState(event.target.value as typeof state)}
          >
            <option value="">All states</option>
            {!["", "pending", "waiting", "succeeded", "dead_letter"].includes(
              state,
            ) && <option value={state}>Unsupported state</option>}
            {["pending", "succeeded", "waiting", "dead_letter"].map((value) => (
              <option key={value} value={value}>
                {stateLabel(value)}
              </option>
            ))}
          </select>
        </Field>
        <div className="workspace-actions">
          <button disabled={page.busy}>Apply delivery filters</button>
          <button
            type="button"
            disabled={page.busy}
            onClick={() => {
              setDeliveryId("");
              setEventId("");
              setState("");
              setFilters({ deliveryId: "", eventId: "", state: "" });
              history.pushState(
                null,
                "",
                webhookDeliveryURL(props.runtime.basePath, props.id, {
                  deliveryId: "",
                  eventId: "",
                  state: "",
                }),
              );
              setCursor("");
              page.refresh();
            }}
          >
            Clear delivery filters
          </button>
        </div>
      </form>
      <button
        disabled={page.busy}
        onClick={() => {
          setCursor("");
          page.refresh();
        }}
      >
        {controlIcons["refresh"]} Refresh deliveries from first page
      </button>
      <ReadState
        {...page}
        refresh={() => (cursor ? setCursor("") : page.refresh())}
      />
      {page.data?.items.map((i) => (
        <article className="workspace-panel" key={i.id}>
          <h3>
            {webhookEventLabel(i.eventType)}: <WorkState state={i.state} />
          </h3>
          <CopyReference label="delivery ID" value={i.id} />
          <CopyReference label="event ID" value={i.eventId} />
          <Facts
            rows={[
              ["Attempt", i.attempt],
              ["Event type code", <code>{i.eventType}</code>],
              ["HTTP outcome", i.responseStatus ?? "Not reported"],
              ["Delivery error", i.safeError ?? "Not reported"],
              [
                "Next retry",
                i.nextAttemptAt ? (
                  <DateValue value={i.nextAttemptAt} />
                ) : (
                  "Not scheduled"
                ),
              ],
              ["Last updated", <DateValue value={i.updatedAt} />],
            ]}
          />
          <p>
            Notifications are unordered and may be delivered more than once.
            HTTP success does not establish receiver processing.
          </p>
          <EvidenceDetails title="Complete delivery evidence" value={i} />
        </article>
      ))}
      {page.data && !page.data.items.length && (
        <p>No deliveries on this page.</p>
      )}
      <Pager
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        busy={page.busy}
        onPage={setCursor}
      />
    </section>
  );
}

/** Render only validated, non-secret evidence returned by an admitted test. */
function DeliveryOutcome({
  item,
}: {
  item: components["schemas"]["WebhookDelivery"];
}) {
  return (
    <>
      <Facts
        rows={[
          ["State", <WorkState state={item.state} />],
          ["Attempt", item.attempt],
          [
            "Receiver HTTP outcome",
            item.responseStatus ?? "No HTTP response reported",
          ],
          ["Safe delivery error", item.safeError ?? "None reported"],
          ["Last updated", <DateValue value={item.updatedAt} />],
        ]}
      />
      <CopyReference label="delivery ID" value={item.id} />
      <CopyReference label="event ID" value={item.eventId} />
    </>
  );
}

/** Read reconciliation counters for comparison with the receiver's own checkpoint. */
function Reconciliation(props: WorkspaceProps) {
  const state = useRead(
    "reconciliation",
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/webhooks/reconciliation", {
          signal,
        }),
        checks.WebhookReconciliation,
      ),
    props.onFailure,
  );
  return (
    <section className="workspace-panel">
      <h2>Notification counters</h2>
      <p>
        Compare these counters with your receiver checkpoint. After a change,
        restore or disabled interval, refresh current authorized Catalog state.
        These counters do not promise replay of old events.
      </p>
      <button onClick={state.refresh}>
        {controlIcons["refresh"]} Refresh counters
      </button>
      <ReadState {...state} />
      {state.data && (
        <>
          <Facts
            rows={[
              [
                "Events awaiting delivery",
                <>
                  {state.data.pendingEvents === state.data.pendingEventLimit
                    ? "At least "
                    : ""}
                  {state.data.pendingEvents}
                </>,
              ],
              ["Maximum count shown", state.data.pendingEventLimit],
              [
                "Notifications suppressed by capacity limits",
                <StatusLabel
                  state={String(state.data.capacityLossCount)}
                  tone={state.data.capacityLossCount ? "warning" : "neutral"}
                />,
              ],
              [
                "Last capacity loss",
                state.data.lastCapacityLossAt ? (
                  <DateValue value={state.data.lastCapacityLossAt} />
                ) : (
                  "None recorded"
                ),
              ],
              [
                "Final delivery failures",
                <StatusLabel
                  state={String(state.data.deliveryLossCount)}
                  tone={state.data.deliveryLossCount ? "warning" : "neutral"}
                />,
              ],
              [
                "Last delivery loss",
                state.data.lastDeliveryLossAt ? (
                  <DateValue value={state.data.lastDeliveryLossAt} />
                ) : (
                  "None recorded"
                ),
              ],
            ]}
          />
          <p>
            Recorded losses do not by themselves establish a current incident.
          </p>
          <p>
            Pending events are Catalog notification work, not receiver
            acknowledgements. The reporting limit caps the count, not the queue.
            Notifications suppressed by capacity limits count suppressed
            notifications; delivery losses count terminal recipient failures.
            Refreshing does not reset these counters.
          </p>
          <EvidenceDetails title="Reconciliation counters" value={state.data} />
        </>
      )}
    </section>
  );
}

function checksWebhookID(value: unknown, id: string) {
  return object(value) && value.id === id;
}

/** Keep the exact event code available while using the existing public vocabulary labels. */
function webhookEventLabel(value: string): string {
  return webhookChoices.find(([code]) => code === value)?.[1] ?? value;
}
