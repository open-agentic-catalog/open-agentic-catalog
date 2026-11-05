import { commandSummary } from "./workspace";
import { WorkState } from "./operational-labels";
import { Facts } from "./Facts";
import * as checks from "./responses";
// Semantic indexing administration uses the same confirmed, idempotent Job flow as publication.
import { useState } from "react";
import { result } from "../api/client";
import { acknowledged } from "./audit-handoff";
import type { Access, Client, Runtime } from "../api/client";
import { permits } from "../api/permissions";
import { ActionForm } from "./authoring";
import { ChoiceField } from "./authoring";
import { ActiveSnapshot } from "./ActiveSnapshot";
import type { components } from "../api/schema";
import { Field, Reason } from "./workspace";
type Snapshot = components["schemas"]["PublicationSnapshotSummary"];

/** Rebuild one tenant-local search population without exposing model or database settings. */
export default function SemanticAdmin({
  client,
  access,
  runtime,
  onFailure,
  ready = true,
}: {
  client: Client;
  access: Access;
  runtime: Runtime;
  onFailure: (e: unknown) => void;
  ready?: boolean;
}) {
  const [scope, setScope] = useState("control");
  const [snapshot, setSnapshot] = useState("");
  const [reason, setReason] = useState("");
  const [choosing, setChoosing] = useState(false);
  const [observed, setObserved] = useState<Snapshot>();
  const [job, setJob] = useState<{
    id: string;
    population: string;
    state: string;
    snapshotId?: string;
    host?: string;
    audience?: string;
  }>();
  return (
    <>
      <ActionForm
        title="Rebuild search index"
        icon="refresh"
        intent="warning"
        description="Rebuild semantic search data with the configured model. Catalog records and published documents are unchanged. Keyword search remains available during the rebuild. A matching queued, running or waiting job will be reused."
        editable={permits(access, "operations.reindex", "tenant.admin")}
        allowed={ready && permits(access, "operations.reindex", "tenant.admin")}
        unavailableReason={
          !permits(access, "operations.reindex", "tenant.admin")
            ? "Rebuilding requires operations.reindex permission and tenant.admin scope when enforced. Your input is kept."
            : "Refresh service capabilities successfully before sending a new rebuild request. Semantic indexing must be enabled."
        }
        dirty={scope !== "control" || !!reason || !!snapshot}
        onFailure={onFailure}
        prepare={() => {
          const body = {
            reason: reason.trim(),
            ...(scope === "public" ? { snapshotId: snapshot.trim() } : {}),
          };
          const population =
            scope === "public" ? "Public discovery snapshot" : "Catalog search";
          const context =
            scope === "public" && observed && observed.id === body.snapshotId
              ? { host: observed.host, audience: observed.audience }
              : undefined;
          return {
            summary: commandSummary([
              ["Population", population] as const,
              [
                "Observed host / audience",
                context
                  ? `${context.host} / ${context.audience}`
                  : "Not inspected for this request",
              ] as const,
              [
                "Snapshot ID",
                body.snapshotId ?? "Not applicable to Catalog search",
              ] as const,
              ["Reason", body.reason] as const,
            ]),
            run: async (key) => {
              const reply = await client.api.POST("/api/v1/catalog:reindex", {
                params: { header: { "Idempotency-Key": key } },
                body,
              });
              const created = result(reply, (value) =>
                checks.JobFor(value, access.tenantId, "semantic.reindex"),
              );
              setJob({
                id: created.id,
                state: created.state,
                population,
                ...(body.snapshotId ? { snapshotId: body.snapshotId } : {}),
                ...context,
              });
              return acknowledged(
                "Rebuild request accepted. View the job to check progress.",
                reply.response,
                {
                  action: "semantic.reindex.requested",
                  targetType: "job",
                  targetId: created.id,
                },
              );
            },
          };
        }}
      >
        <Field
          label="Search index"
          hint="Choose Catalog search, or one active public discovery snapshot. Each snapshot keeps its own audience."
        >
          <select
            value={scope}
            onChange={(event) => setScope(event.target.value)}
          >
            <option value="control">Catalog search</option>
            <option value="public">Public discovery snapshot</option>
          </select>
        </Field>
        {scope === "public" && (
          <>
            {permits(access, "publication.read", "catalog.read") ? (
              <>
                <button type="button" onClick={() => setChoosing(!choosing)}>
                  {choosing
                    ? "Hide snapshot choices"
                    : "Choose active snapshot"}
                </button>
                {choosing && (
                  <ReindexSnapshotChoice
                    client={client}
                    access={access}
                    onFailure={onFailure}
                    runtime={runtime}
                    onChoose={(item) => {
                      setSnapshot(item.id);
                      setObserved(item);
                    }}
                  />
                )}
              </>
            ) : (
              <p>
                Snapshot choices require publication.read and catalog.read when
                scopes are enforced. You can still enter an exact active
                Snapshot ID without those grants.
              </p>
            )}
            <Field
              label="Active snapshot ID"
              hint="Choose an observed active snapshot or enter its exact ID, including when choices are unavailable. The server rechecks current active eligibility for this tenant."
            >
              <input
                required
                pattern="[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
                value={snapshot}
                onChange={(event) => {
                  setSnapshot(event.target.value);
                  setObserved(undefined);
                }}
              />
            </Field>
            {observed?.id === snapshot && (
              <p>
                Selected snapshot: {observed.host} / {observed.audience}.
                Snapshot <code>{observed.id}</code>. This observation does not
                guarantee it remains active.
              </p>
            )}
          </>
        )}
        <Reason value={reason} onChange={setReason} />
      </ActionForm>
      {job && (
        <section
          className="workspace-panel"
          aria-label="Accepted indexing request"
        >
          <h3>Rebuild request accepted</h3>
          <p>View the job to check progress. Acceptance is not completion.</p>
          <Facts
            rows={[
              ["Job ID", <code>{job.id}</code>],
              ["State when accepted", <WorkState state={job.state} />],
              ["Search index", job.population],
              ...(job.snapshotId
                ? ([
                    ["Snapshot ID", <code>{job.snapshotId}</code>],
                    [
                      "Selected host",
                      job.host ?? "Not inspected for this request",
                    ],
                    [
                      "Selected audience",
                      job.audience ?? "Not inspected for this request",
                    ],
                  ] as const)
                : []),
            ]}
          />
          {permits(access, "job.read", "catalog.read") ? (
            <a className="button" href={`${runtime.basePath}/jobs/${job.id}`}>
              View indexing Job
            </a>
          ) : (
            <p>
              Inspecting progress requires job.read and catalog.read scope when
              enforced.
            </p>
          )}
          {job.snapshotId && (
            <p>
              Catalog indexing counts do not show progress for this public
              snapshot rebuild.
            </p>
          )}
          <details>
            <summary>About this request</summary>
            <p>
              The displayed state was returned with the request and may have
              changed. Selected snapshot details stay only on this page;
              reloading does not stop the job.
            </p>
          </details>
        </section>
      )}
    </>
  );
}

/** One bounded host inventory, then one explicitly selected active summary; never scan history. */
function ReindexSnapshotChoice(props: {
  client: Client;
  access: Access;
  runtime: Runtime;
  onFailure: (e: unknown) => void;
  onChoose: (item: Snapshot) => void;
}) {
  const [host, setHost] = useState("");
  return (
    <section className="workspace-panel" aria-label="Reindex snapshot choices">
      <ChoiceField
        label="Publication host"
        required={false}
        value={host}
        onChange={setHost}
        onFailure={props.onFailure}
        load={async (_cursor, signal) => {
          const inventory = result(
            await props.client.api.GET("/api/v1/publication/hosts", { signal }),
            checks.PublicationHostInventory,
          );
          return {
            items: inventory.items
              .filter((item) => item.mode !== "disabled")
              .map((item) => ({
                id: item.host,
                label: `${item.host} / ${item.mode}`,
              })),
          };
        }}
      />
      {host && (
        <ActiveSnapshot
          key={host}
          {...props}
          id=""
          host={host}
          onChoose={(_id, item) => props.onChoose(item)}
          chooseLabel="Use observed snapshot for reindexing"
        />
      )}
      <p>
        Choices are bounded observations. No snapshot history is scanned;
        reading a choice does not start a rebuild. The exact-ID field remains
        available if choices fail or no active snapshot is observed.
      </p>
    </section>
  );
}
