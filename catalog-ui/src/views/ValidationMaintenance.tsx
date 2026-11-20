// Maintenance reads are explicit; renewal stays in the existing exact-revision workflow.
import { useId, useState } from "react";
import { result } from "../api/client";
import { kindLabel } from "../resource-kinds";
import { DateValue } from "./date-value";
import { ScrollableTable } from "./ScrollableTable";
import {
  Field,
  Heading,
  Pager,
  ReadState,
  useRead,
  type WorkspaceProps,
} from "./workspace";
import { maintenancePage } from "./validation-maintenance-responses";

const states = {
  attention: "All evidence needing attention",
  expiring: "Expiring",
  expired: "Expired",
  blocked: "Blocked",
  stale: "Policy stale",
};
type Criteria = { state: keyof typeof states; horizonHours: number };

/** Find current evidence, then deliberately inspect and revalidate one exact revision. */
export default function ValidationMaintenance(props: WorkspaceProps) {
  const [criteria, setCriteria] = useState<Criteria>({
    state: "attention",
    horizonHours: 24,
  });
  const [draft, setDraft] = useState({ state: criteria.state, hours: "24" });
  const [cursor, setCursor] = useState("");
  const [error, setError] = useState("");
  const errorID = useId();
  const read = useRead(
    JSON.stringify([props.access, criteria, cursor]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/validation-maintenance", {
          params: {
            query: {
              state: criteria.state,
              horizonHours: criteria.horizonHours,
              limit: 20,
              ...(cursor ? { cursor } : {}),
            },
          },
          signal,
        }),
        maintenancePage,
      ),
    props.onFailure,
  );
  const refresh = () => {
    if (cursor) setCursor("");
    else read.refresh();
  };
  return (
    <>
      <Heading
        title="Validation maintenance"
        icon="refresh"
        description="Find current revisions with evidence that is expiring, expired, blocked or no longer applicable under current policy."
      />
      <p>
        Only namespaces where you can read Resource metadata and content and
        validate revisions contribute entries. This list makes no remote
        requests and does not run validation.
      </p>
      <form
        className="workspace-form"
        aria-label="Filter validation maintenance"
        onSubmit={(event) => {
          event.preventDefault();
          const hours = Number(draft.hours);
          if (
            !draft.hours.trim() ||
            !Number.isInteger(hours) ||
            hours < 1 ||
            hours > 168
          ) {
            setError("Choose an expiry horizon from 1 to 168 whole hours.");
            return;
          }
          setError("");
          if (
            criteria.state === draft.state &&
            criteria.horizonHours === hours &&
            !cursor
          )
            read.refresh();
          setCriteria({ state: draft.state, horizonHours: hours });
          setCursor("");
        }}
      >
        <Field label="Evidence condition">
          <select
            value={draft.state}
            onChange={(event) =>
              setDraft({
                ...draft,
                state: event.target.value as Criteria["state"],
              })
            }
          >
            {Object.entries(states).map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </select>
        </Field>
        <Field
          label="Expiry horizon (hours)"
          help="Expiring means after the observation time and within this horizon, including its end."
        >
          <input
            type="number"
            min="1"
            max="168"
            step="1"
            required
            value={draft.hours}
            aria-invalid={!!error}
            aria-describedby={error ? errorID : undefined}
            onChange={(event) =>
              setDraft({ ...draft, hours: event.target.value })
            }
          />
        </Field>
        {error && (
          <p role="alert" id={errorID}>
            {error}
          </p>
        )}
        {(draft.state !== criteria.state ||
          Number(draft.hours) !== criteria.horizonHours) && (
          <p className="muted">
            Filter edits are pending. Apply filters to update the list.
          </p>
        )}
        <div className="workspace-toolbar">
          <button type="submit" disabled={read.busy}>
            Apply filters
          </button>
          <button type="button" disabled={read.busy} onClick={refresh}>
            Refresh from first page
          </button>
        </div>
      </form>
      <p>
        Expiry alone leaves active discovery snapshots unchanged. Revalidation
        appends evidence for the same content. A blocked external check can
        withdraw entire affected snapshots, and a new run can make a pending
        review stale. Successful renewal does not approve, publish or restore
        anything automatically.
      </p>
      <ReadState {...read} refresh={refresh} />
      {read.data && (
        <>
          <p>
            Applied: {states[criteria.state]}; {read.data.horizonHours} hour
            horizon. Observed at <DateValue value={read.data.observedAt} />.
          </p>
          <p className="muted">
            Evidence and current references can change between pages. Refresh
            from the first page for a current traversal. Deterministic evidence
            without an expiry does not expire with time.
          </p>
          {read.data.items.length === 0 ? (
            <p className="workspace-empty">
              No current revisions match this selection.
            </p>
          ) : (
            <ScrollableTable label="Validation maintenance revisions">
              <table className="workspace-table">
                <thead>
                  <tr>
                    <th>Resource and version</th>
                    <th>Evidence conditions</th>
                    <th>Validation and expiry</th>
                    <th>Exact evidence</th>
                    <th>Next step</th>
                  </tr>
                </thead>
                <tbody>
                  {read.data.items.map((item) => (
                    <tr key={item.revisionId}>
                      <td>
                        {item.canonicalIdentifier}
                        <br />
                        {kindLabel(item.kind)} / {item.version} / Revision{" "}
                        {item.revisionNumber}
                      </td>
                      <td>
                        {item.conditions
                          .map((condition) => states[condition])
                          .join(", ")}
                        <br />
                        {item.applicable
                          ? "Applicable at observation"
                          : "Not applicable at observation"}
                      </td>
                      <td>
                        <DateValue value={item.validatedAt} />
                        <br />
                        {item.expiresAt ? (
                          <>
                            Expires <DateValue value={item.expiresAt} />
                          </>
                        ) : (
                          "No time expiry"
                        )}
                      </td>
                      <td>
                        {item.evidenceState} / {item.validationProfile} /{" "}
                        {item.policyVersion}
                        <br />
                        <code>{item.evidenceId}</code>
                      </td>
                      <td>
                        <a
                          href={`${props.runtime.basePath}/version-revisions/${item.revisionId}`}
                        >
                          Inspect evidence and renewal
                        </a>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </ScrollableTable>
          )}
        </>
      )}
      <Pager
        cursor={cursor}
        nextCursor={read.data?.nextCursor}
        busy={read.busy}
        onPage={setCursor}
      />
    </>
  );
}
