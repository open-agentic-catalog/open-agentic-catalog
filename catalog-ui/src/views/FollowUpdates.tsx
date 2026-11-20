import { useEffect, useRef, useState } from "react";
import { result } from "../api/client";
import type { components } from "../api/schema";
import { object } from "../api/values";
import { PersonalResolution } from "./responses";
import type { WorkspaceProps } from "./workspace";

type Resolution = components["schemas"]["PersonalResolution"];
type Follow = components["schemas"]["PersonalItem"];

export const changeLabels = {
  changed: "Changed since acknowledgement",
  unchanged: "No change since acknowledgement",
  unacknowledged: "Not yet acknowledged",
};

export function followResolution(value: unknown) {
  return (
    PersonalResolution(value) &&
    object(value) &&
    (value.status === "unavailable" || value.current !== undefined)
  );
}

// This component is keyed to one loaded owner page and authority. Leaving either
// cancels the selection and discards all resolved target facts.
export function FollowUpdates({
  items,
  client,
  onFailure,
  basePath,
}: Pick<WorkspaceProps, "client" | "onFailure"> & {
  items: Follow[];
  basePath: string;
}) {
  const request = useRef<AbortController | undefined>(undefined);
  useEffect(() => () => request.current?.abort(), []);
  const [rows, setRows] = useState<{ item: Follow; result: Resolution }[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(false);
  if (!items.length) return null;
  async function refresh() {
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setRows([]);
    setBusy(true);
    setError(false);
    try {
      const checked = [];
      for (const item of items) {
        const reply = await client.api.POST(
          "/api/v1/personal/items/{itemId}/resolve",
          {
            params: { path: { itemId: item.id } },
            body: {},
            signal: controller.signal,
          },
        );
        checked.push({ item, result: result(reply, followResolution) });
      }
      if (!controller.signal.aborted) setRows(checked);
    } catch (e) {
      if (!controller.signal.aborted) {
        setError(true);
        onFailure(e);
      }
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  }
  return (
    <section aria-label="Followed Listings">
      <button type="button" disabled={busy} onClick={() => void refresh()}>
        Check followed Listings on this page
      </button>
      {error && (
        <p role="alert">Could not check this page. Refresh to try again.</p>
      )}
      {rows.length > 0 && (
        <ul>
          {rows.map(({ item, result: r }) => (
            <li key={item.id}>
              <a href={basePath + "/saved-items/" + item.id}>{item.label}</a>
              {" · "}
              {r.change ? changeLabels[r.change] : "Unavailable"}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
