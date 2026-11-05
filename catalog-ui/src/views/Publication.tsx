import { Facts } from "./Facts";
import { controlIcons } from "../shell/Icon";
import { RatingSharing } from "./Ratings";
import { PublicationExclusion } from "./PublicationExclusions";
import * as checks from "./responses";
// Discovery publication works against one selected host and retained snapshot identities.
import { useEffect, useRef, useState } from "react";
import { result } from "../api/client";
import { object } from "../api/values";
import { documentationGuideLink } from "../api/session";
import { PublicationContents } from "./PublicationContents";
import { ActiveSnapshot, SnapshotFacts, SnapshotState } from "./ActiveSnapshot";
import { SnapshotComparison } from "./SnapshotComparison";
import { permits } from "../api/permissions";
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

/** Require explicit publication-host selection before reading or changing snapshots. */
export default function Publication(props: WorkspaceProps) {
  return (
    <>
      <Heading
        title="Discovery publication"
        icon="publish"
        description="Build and activate the discovery content served by a configured host."
      />
      <p>
        A snapshot fixes the released listings served for one host and audience.
        Discovery serves its active snapshot. Build a candidate, inspect its
        contents and exclusions, then activate it separately.
      </p>
      {!permits(props.access, "publication.read", "catalog.read") &&
      !permits(props.access, "ratings.sharing.manage", "tenant.admin") ? (
        <p role="alert">
          Snapshot reads require tenant-wide publication.read. Rating sharing
          requires tenant-wide ratings.sharing.manage. When scopes are enforced,
          these actions also require catalog.read and tenant.admin,
          respectively. Namespace release grants do not authorize host
          publication.
        </p>
      ) : (
        <HostSelection
          key={JSON.stringify([
            props.access.tenantId,
            props.access.realmId,
            props.access.membershipId,
            props.access.subject,
            props.access.actorType,
          ])}
          {...props}
        />
      )}
    </>
  );
}

/** Inventory failure never gates an existing exact-host read or prepared command. */
function HostSelection(props: WorkspaceProps) {
  const documentation = documentationGuideLink(
    props.runtime.documentationUrl,
    "publishing",
  );
  const initialHost =
    new URLSearchParams(location.search).get("host")?.trim() || "";
  const [host, setHost] = useState(initialHost);
  const initialAudience = new URLSearchParams(location.search).get("audience");
  const [audience, setAudience] = useState<"anonymous" | "authenticated">(
    initialAudience === "authenticated" ? "authenticated" : "anonymous",
  );
  const [selected, setSelected] = useState<{
    host: string;
    audience?: "anonymous" | "authenticated";
  }>(() => ({
    host: initialHost,
    ...(initialAudience === "anonymous" || initialAudience === "authenticated"
      ? { audience: initialAudience }
      : {}),
  }));
  const [editingHost, setEditingHost] = useState(!initialHost);
  const readAllowed = permits(props.access, "publication.read", "catalog.read");
  const hosts = useRead(
    JSON.stringify(["publication-hosts", readAllowed]),
    async (signal) =>
      readAllowed
        ? result(
            await props.client.api.GET("/api/v1/publication/hosts", { signal }),
            checks.PublicationHostInventory,
          )
        : { items: [] },
    props.onFailure,
  );
  const choice = hosts.data?.items.find((item) => item.host === host.trim());
  const selectedMode =
    selected.audience ??
    hosts.data?.items.find((item) => item.host === selected.host)?.mode;
  return (
    <>
      {selected.host && (
        <section
          className="workspace-panel"
          aria-label="Selected publication host"
        >
          <Facts
            rows={[
              ["Host", <code>{selected.host}</code>],
              ...(!props.id
                ? ([
                    [
                      selected.audience
                        ? "Selected audience"
                        : "Configured audience",
                      selectedMode === "anonymous"
                        ? "Anonymous"
                        : selectedMode === "authenticated"
                          ? "Authenticated"
                          : selectedMode === "disabled"
                            ? "Discovery disabled"
                            : "Not inspected",
                    ],
                  ] as const)
                : []),
            ]}
          />
        </section>
      )}
      <details
        className="workspace-advanced"
        open={editingHost}
        onToggle={(event) => setEditingHost(event.currentTarget.open)}
      >
        <summary>
          {selected.host ? "Change host" : "Choose a discovery host"}
        </summary>
        {readAllowed && (
          <section
            className="workspace-panel"
            aria-label="Publication host choices"
          >
            <button type="button" onClick={hosts.refresh} disabled={hosts.busy}>
              Refresh host choices
            </button>
            <ReadState {...hosts} />
            {hosts.data && (
              <>
                <Field label="Configured publication hosts">
                  <select
                    value={choice?.host ?? ""}
                    onChange={(e) => {
                      setHost(e.target.value);
                      const mode = hosts.data?.items.find(
                        (item) => item.host === e.target.value,
                      )?.mode;
                      if (mode === "anonymous" || mode === "authenticated")
                        setAudience(mode);
                    }}
                  >
                    <option value="">Choose a host</option>
                    {hosts.data.items.map((item) => (
                      <option
                        key={item.host}
                        value={item.host}
                        disabled={item.mode === "disabled"}
                      >
                        {item.host} — {item.mode}
                      </option>
                    ))}
                  </select>
                </Field>
                {!hosts.data.items.length && (
                  <p>No publication hosts configured for your tenant.</p>
                )}
              </>
            )}
            <p className="muted">
              Choices describe configured discovery mode, not health. Disabled
              hosts cannot be opened. Select a host, then choose Open host.
              Nothing is activated automatically.
            </p>
          </section>
        )}
        <form
          className="workspace-panel workspace-form"
          onSubmit={(e) => {
            e.preventDefault();
            if (choice?.mode === "disabled") return;
            const next = {
              host: host.trim(),
              audience: choice?.mode ?? audience,
            };
            if (
              JSON.stringify(next) !== JSON.stringify(selected) &&
              !confirmDiscard()
            )
              return;
            setSelected(next);
            setEditingHost(false);
          }}
        >
          <Field
            label="Configured discovery host"
            hint="Choose a configured host above or enter an exact hostname manually, including when choices cannot be loaded. The API rechecks host authority. Activating a snapshot changes what its audience can discover."
          >
            <input
              required
              maxLength={253}
              placeholder="catalog.example.com"
              value={host}
              onChange={(e) => setHost(e.target.value)}
            />
          </Field>
          <Field
            label="Discovery audience"
            hint="Use the audience configured for this exact host. Opening a host does not change its deployment settings."
          >
            <select
              value={
                choice && choice.mode !== "disabled" ? choice.mode : audience
              }
              onChange={(event) =>
                setAudience(event.target.value as "anonymous" | "authenticated")
              }
              disabled={!!choice}
            >
              <option value="anonymous">Anonymous</option>
              <option value="authenticated">Authenticated</option>
            </select>
          </Field>
          <div className="form-actions">
            <button
              className="primary"
              type="submit"
              disabled={choice?.mode === "disabled"}
            >
              Open host
            </button>
          </div>
          {choice?.mode === "disabled" && (
            <p role="status">
              Discovery is disabled for this host. Choose an enabled host.
            </p>
          )}
          {documentation && (
            <a
              href={documentation.href}
              target="_blank"
              rel="noopener noreferrer"
              aria-label={documentation.label}
              aria-describedby="operator-docs-new-tab"
            >
              {documentation.label}
              <span id="operator-docs-new-tab" className="sr-only">
                Opens in a new tab
              </span>
            </a>
          )}
        </form>
      </details>
      {selected.host && selected.audience && (
        <RatingSharing
          key={JSON.stringify(selected)}
          client={props.client}
          access={props.access}
          host={selected.host}
          audience={selected.audience}
          onFailure={props.onFailure}
        />
      )}
      {readAllowed &&
        selected.host &&
        (props.id ? (
          <Snapshot
            key={selected.host + props.id}
            {...props}
            host={selected.host}
          />
        ) : (
          <HostSnapshots key={selected.host} {...props} host={selected.host} />
        ))}
    </>
  );
}

/** Page snapshots for one host and prepare generation or unpublish commands. */
function HostSnapshots(props: WorkspaceProps & { host: string }) {
  const readEpoch = useRef(0);
  const [state, setState] = useState<"" | "candidate" | "active" | "retired">(
    "",
  );
  const [commandEpoch, setCommandEpoch] = useState(0);
  const resetCommandsAfter = useRef<number | undefined>(undefined);
  const [snapshotId, setSnapshotId] = useState("");
  const [cursor, setCursor] = useState("");
  const [created, setCreated] = useState("");
  const [reason, setReason] = useState("");
  const page = useRead(
    JSON.stringify([props.host, state, cursor]),
    async (signal) => {
      const epoch = ++readEpoch.current;
      return {
        ...result(
          await props.client.api.GET("/api/v1/publication/snapshots", {
            params: {
              query: {
                host: props.host,
                limit: 25,
                ...(state ? { state } : {}),
                ...(cursor ? { cursor: cursor } : {}),
              },
            },
            signal,
          }),
          (value) =>
            checks.PublicationSnapshotPage(value) &&
            value.items.every(
              (item) =>
                item.host === props.host &&
                item.tenantId === props.access.tenantId &&
                (!state || item.state === state),
            ),
        ),
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
  useEffect(() => {
    if (
      resetCommandsAfter.current !== undefined &&
      page.data &&
      !page.busy &&
      !page.error &&
      page.data.readEpoch > resetCommandsAfter.current
    ) {
      resetCommandsAfter.current = undefined;
      setCommandEpoch((value) => value + 1);
    }
  }, [page.data, page.busy, page.error]);
  const stale =
    !!committed && (!page.data || page.data.readEpoch <= committed.readEpoch);
  const acknowledge = (message: string) =>
    setCommitted({ message, readEpoch: readEpoch.current });
  const writable = permits(
    props.access,
    "publication.generate",
    "catalog.publish",
  );
  return (
    <>
      <ActiveSnapshot {...props} />
      <form
        className="workspace-toolbar workspace-lookup"
        aria-label="Open exact publication snapshot"
        action={`${props.runtime.basePath}/publication/${encodeURIComponent(snapshotId.trim().toLowerCase())}`}
        method="get"
      >
        <input type="hidden" name="host" value={props.host} />
        <Field
          label="Exact Snapshot ID"
          hint="Open a known retained snapshot for this configured host without scanning history."
        >
          <input
            required
            maxLength={36}
            pattern="[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
            value={snapshotId}
            onChange={(event) => setSnapshotId(event.target.value)}
          />
        </Field>
        <div className="workspace-actions">
          <button disabled={!snapshotId.trim()}>Open snapshot</button>
        </div>
      </form>
      <ReadState
        {...page}
        refresh={() => (page.error && cursor ? setCursor("") : page.refresh())}
      />
      <section className="workspace-panel">
        <p className="eyebrow">Last loaded host snapshots</p>
        <h2>{props.host}</h2>
        <Field
          label="Snapshot state"
          hint="Filter server-side before paging. Changing this inventory filter preserves command inputs."
        >
          <select
            value={state}
            onChange={(event) => {
              resetCommandsAfter.current = undefined;
              setState(event.target.value as typeof state);
              setCursor("");
            }}
          >
            <option value="">All states</option>
            <option value="candidate">Candidate</option>
            <option value="active">Active</option>
            <option value="retired">Retired</option>
          </select>
        </Field>
        {stale && (
          <p className="notice">
            The snapshot inventory predates the committed command result.
            Refresh snapshots to read current state.
          </p>
        )}
        <button
          disabled={page.busy}
          onClick={() => {
            if (confirmDiscard()) {
              resetCommandsAfter.current = readEpoch.current;
              page.refresh();
            }
          }}
        >
          {controlIcons["refresh"]} Refresh snapshots
        </button>
        {cursor && (
          <p className="muted">
            Newest generations appear on the first page. Refresh keeps this
            page.
          </p>
        )}
        {page.data && (
          <>
            {page.data.items.map((i) => (
              <div key={i.id} className="workspace-panel">
                <a
                  href={`${props.runtime.basePath}/publication/${i.id}?host=${encodeURIComponent(props.host)}`}
                >
                  Generation {i.generation}
                </a>{" "}
                / <SnapshotState state={i.state} /> / {i.audience}
                <SnapshotFacts item={i} />
              </div>
            ))}
            {!page.data.items.length && <p>No snapshots on this page.</p>}
          </>
        )}
        <Pager
          label="Publication snapshots pagination"
          cursor={cursor}
          nextCursor={page.data?.nextCursor}
          busy={page.busy}
          onPage={(value) => {
            resetCommandsAfter.current = undefined;
            setCursor(value);
          }}
        />
      </section>
      {committed && (
        <CommandReceipt message={committed.message}>
          <span>
            {" "}
            This command result may be a historical replay. Refresh snapshots
            before the next action.
          </span>
        </CommandReceipt>
      )}
      <ActionForm
        key={commandEpoch}
        title="Build candidate snapshot"
        icon="add"
        editable={writable}
        onAcknowledged={acknowledge}
        description={`Build a candidate from eligible releases for ${props.host}. Review exclusions before activation; this action does not replace the active snapshot.`}
        allowed={writable && !!page.data && !page.busy && !page.error && !stale}
        unavailableReason="Building requires a loaded host and tenant-wide publication.generate permission. When scopes are enforced, catalog.publish is also required. A namespace grant is not enough."
        onFailure={props.onFailure}
        prepare={() => {
          const body = { host: props.host };
          return {
            summary: [
              { label: "Action", value: "Build candidate snapshot" },
              { label: "Host", value: body.host },
              {
                label: "Scope",
                value: "Eligible releases for this host; no activation",
              },
            ],
            run: async (key) => {
              const item = result(
                await props.client.api.POST("/api/v1/publication/snapshots", {
                  params: { header: { "Idempotency-Key": key } },
                  body,
                }),
                (value) =>
                  checks.PublicationSnapshot(value) &&
                  object(value) &&
                  value.host === body.host &&
                  value.state === "candidate",
              );
              setCreated(item.id);
              return "Candidate snapshot built.";
            },
          };
        }}
      />
      {created && (
        <a
          className="button primary"
          href={`${props.runtime.basePath}/publication/${created}?host=${encodeURIComponent(props.host)}`}
        >
          Inspect candidate
        </a>
      )}
      <details>
        <summary>Withdraw public discovery</summary>
        <ActionForm
          key={commandEpoch}
          title="Unpublish host"
          intent="danger"
          icon="remove"
          retainDraft={!!reason}
          editable={permits(
            props.access,
            "publication.unpublish",
            "catalog.publish",
          )}
          onAcknowledged={acknowledge}
          description={`Withdraw the active snapshot for ${props.host}. Governed release history remains retained; public discovery will no longer serve this snapshot.`}
          allowed={
            permits(props.access, "publication.unpublish", "catalog.publish") &&
            !!page.data &&
            !page.busy &&
            !page.error &&
            !stale
          }
          unavailableReason="Unpublishing requires publication.unpublish and the catalog.publish scope when enforced. Refresh the host before preparing another command."
          dirty={!!reason}
          onFailure={props.onFailure}
          prepare={() => {
            const body = { host: props.host, reason: reason.trim() };
            return {
              summary: [
                { label: "Action", value: "Unpublish host" },
                { label: "Host", value: body.host },
                { label: "Reason", value: body.reason },
              ],
              run: async (key) => {
                result(
                  await props.client.api.POST("/api/v1/publication:unpublish", {
                    params: { header: { "Idempotency-Key": key } },
                    body,
                  }),
                  (value) =>
                    checks.PublicationSnapshot(value) &&
                    object(value) &&
                    value.host === body.host &&
                    value.state === "retired",
                );
                return "Host unpublished.";
              },
            };
          }}
        >
          <Reason value={reason} onChange={setReason} />
        </ActionForm>
      </details>
    </>
  );
}

/** Inspect one snapshot and prepare activation or rollback with retained revision evidence. */
function Snapshot(props: WorkspaceProps & { host: string }) {
  const readEpoch = useRef(0);
  const detail = useRead(
    props.host + props.id,
    async (signal) => {
      const epoch = ++readEpoch.current;
      const reply = await props.client.api.GET(
        "/api/v1/publication/snapshots/{snapshotId}",
        {
          params: {
            path: { snapshotId: props.id },
            query: { host: props.host },
          },
          signal,
        },
      );
      const item = result(reply, checks.PublicationSnapshot);
      if (
        item.id !== props.id ||
        item.host !== props.host ||
        item.tenantId !== props.access.tenantId
      )
        throw new Error(
          "Snapshot does not match the selected host and identity.",
        );
      return {
        item,
        readEpoch: epoch,
        etag: reply.response.headers.get("ETag"),
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
  const [reason, setReason] = useState("");
  const item = detail.data?.item;
  const action = item?.state === "retired" ? "rollback" : "activate";
  return (
    <>
      <div className="workspace-toolbar">
        <a
          href={`${props.runtime.basePath}/publication?host=${encodeURIComponent(props.host)}`}
        >
          Back to host
        </a>
        <button
          type="button"
          disabled={detail.busy}
          onClick={() => {
            if (confirmDiscard()) detail.refresh();
          }}
        >
          {controlIcons["refresh"]} Reload snapshot state
        </button>
      </div>
      <ReadState {...detail} />
      {committed && (
        <CommandReceipt message={committed.message}>
          <span>
            {" "}
            This command result may be a historical replay. Reload snapshot
            state before the next action.
          </span>
        </CommandReceipt>
      )}
      {item && (
        <>
          <section className="workspace-panel">
            <p className="eyebrow">Last loaded snapshot</p>
            {stale && (
              <p className="notice">
                The displayed snapshot predates the committed command result.
                Reload snapshot state to inspect current state.
              </p>
            )}
            <h2>
              Generation {item.generation}: <SnapshotState state={item.state} />
            </h2>
            <p>
              {item.host} / {item.audience}
            </p>
            {!stale && (
              <p>
                {item.state === "active"
                  ? "This snapshot is active for its host and audience. Later listing releases are not added automatically."
                  : item.state === "candidate"
                    ? "This candidate is not served by public discovery. Review exclusions, then activate it when ready."
                    : "This retained snapshot is not active. Restoration rechecks current release eligibility and domain approval."}
              </p>
            )}
            <PublicationContents snapshot={item} />
            <SnapshotComparison {...props} snapshotId={item.id} />
            <h3>Publication exclusions</h3>
            {item.exclusions.length ? (
              item.exclusions.map((i) => (
                <PublicationExclusion
                  key={i.resourceId}
                  item={i}
                  props={props}
                />
              ))
            ) : (
              <p>No exclusions reported.</p>
            )}
            <EvidenceDetails
              title="Snapshot, profiles and discovery documents"
              value={item}
            />
          </section>
          <ActionForm
            key={detail.data?.readEpoch}
            onAcknowledged={(message) =>
              setCommitted({ message, readEpoch: readEpoch.current })
            }
            editable={
              ["candidate", "retired"].includes(item.state) &&
              permits(
                props.access,
                action === "rollback"
                  ? "publication.rollback"
                  : "publication.activate",
                "catalog.publish",
              )
            }
            icon={action === "rollback" ? "history" : "publish"}
            title={
              action === "rollback"
                ? "Restore retained snapshot"
                : "Activate candidate"
            }
            description={`${action === "rollback" ? "Revalidate and restore" : "Revalidate and activate"} generation ${item.generation} for ${props.host}. Current release eligibility and domain approval are checked again; excluded or no-longer-eligible content cannot be restored.`}
            allowed={
              !stale &&
              !detail.busy &&
              !detail.error &&
              !!detail.data?.etag &&
              ["candidate", "retired"].includes(item.state) &&
              permits(
                props.access,
                action === "rollback"
                  ? "publication.rollback"
                  : "publication.activate",
                "catalog.publish",
              )
            }
            dirty={!!reason}
            retainDraft={!!reason}
            unavailableReason={
              props.access.scopeEnforcement &&
              !props.access.scopes.includes("catalog.publish")
                ? "The catalog.publish scope is required for activation or restoration."
                : !permits(
                      props.access,
                      action === "rollback"
                        ? "publication.rollback"
                        : "publication.activate",
                      "catalog.publish",
                    )
                  ? `Tenant-wide publication.${action} permission is required. A namespace grant is not enough.`
                  : !["candidate", "retired"].includes(item.state)
                    ? "An active snapshot cannot be activated or restored again. Reload snapshot state to inspect current state."
                    : "Reload snapshot state to obtain its current revision before activation or restoration."
            }
            onFailure={props.onFailure}
            prepare={() => {
              const body = { host: props.host, reason: reason.trim() };
              const snapshotId = item.id;
              const etag = detail.data!.etag!;
              const path =
                action === "rollback"
                  ? "/api/v1/publication/snapshots/{snapshotId}:rollback"
                  : "/api/v1/publication/snapshots/{snapshotId}:activate";
              return {
                summary: [
                  {
                    label: "Action",
                    value:
                      action === "rollback"
                        ? "Restore snapshot"
                        : "Activate snapshot",
                  },
                  { label: "Host", value: body.host },
                  { label: "Snapshot ID", value: snapshotId },
                  { label: "Generation", value: String(item.generation) },
                  { label: "Revision", value: etag },
                  { label: "Reason", value: body.reason },
                ],
                run: async (key) => {
                  result(
                    await props.client.api.POST(path, {
                      params: {
                        path: { snapshotId },
                        header: { "Idempotency-Key": key, "If-Match": etag },
                      },
                      body,
                    }),
                    (value) =>
                      checks.PublicationSnapshot(value) &&
                      object(value) &&
                      value.id === snapshotId &&
                      value.host === body.host &&
                      value.generation === item.generation &&
                      value.audience === item.audience &&
                      value.state === "active",
                  );
                  return "Discovery snapshot activated.";
                },
              };
            }}
          >
            <Reason value={reason} onChange={setReason} />
          </ActionForm>
        </>
      )}
    </>
  );
}
