import { controlIcons } from "../shell/Icon";
import { Facts } from "./Facts";
import { useState } from "react";
import { result } from "../api/client";
import type { components } from "../api/schema";
import { ReadState, useRead, type WorkspaceProps } from "./workspace";
import * as checks from "./responses";

type Summary = components["schemas"]["PublicationSnapshotSummary"];
export function SnapshotState({ state }: { state: Summary["state"] }) {
  return (
    <span
      className="state-badge"
      data-tone={state === "retired" ? "neutral" : "info"}
    >
      {state}
    </span>
  );
}

export function SnapshotFacts({ item }: { item: Summary }) {
  return (
    <Facts
      rows={[
        ["Snapshot ID", <code>{item.id}</code>],
        [
          "Host / audience",
          <>
            {item.host} / {item.audience}
          </>,
        ],
        [
          "Generation / observed state",
          <>
            {item.generation} / <SnapshotState state={item.state} />
          </>,
        ],
        ["Created", <time dateTime={item.createdAt}>{item.createdAt}</time>],
        [
          "Activated",
          item.activatedAt ? (
            <time dateTime={item.activatedAt}>{item.activatedAt}</time>
          ) : (
            "Not supplied"
          ),
        ],
      ]}
    />
  );
}

/** Observe one bounded summary independently of the inventory, never full history. */
export function ActiveSnapshot(
  props: WorkspaceProps & {
    host: string;
    onChoose?: (id: string, item: Summary) => void;
    chooseLabel?: string;
    baselineId?: string;
  },
) {
  const [requested, setRequested] = useState(0);
  const read = useRead(
    props.host,
    async (signal) => {
      if (!requested) return undefined;
      const page = result(
        await props.client.api.GET("/api/v1/publication/snapshots", {
          params: { query: { host: props.host, state: "active", limit: 1 } },
          signal,
        }),
        checks.PublicationSnapshotPage,
      );
      if (
        page.items.length > 1 ||
        page.nextCursor ||
        page.items.some(
          (item) =>
            item.host !== props.host ||
            item.tenantId !== props.access.tenantId ||
            item.state !== "active",
        )
      )
        throw new Error(
          "Active snapshot does not match the requested host and tenant.",
        );
      return page.items[0] ?? null;
    },
    props.onFailure,
    true,
  );
  const refresh = () => {
    setRequested((value) => value + 1);
    read.refresh();
  };
  return (
    <section className="workspace-panel" aria-label="Active snapshot lookup">
      <h2>Observe active snapshot</h2>
      <p>
        Read one active snapshot independently of the history page. This is an
        observation, not a guarantee of later state.
      </p>
      <button
        type="button"
        disabled={requested > 0 && read.busy}
        onClick={refresh}
      >
        {controlIcons[requested ? "refresh" : "search"]}
        {requested ? "Refresh active snapshot" : "Read active snapshot"}
      </button>
      {requested > 0 && (
        <>
          <ReadState {...read} refresh={refresh} />
          {read.data === null && !read.busy && !read.error && (
            <p>No active snapshot at this observation.</p>
          )}
          {read.data && (
            <>
              <p className="eyebrow">Last observed active snapshot</p>
              {(read.busy || read.error) && (
                <p className="notice">
                  The previous observation is retained; the current read has not
                  succeeded.
                </p>
              )}
              <SnapshotFacts item={read.data} />
              <a
                href={`${props.runtime.basePath}/publication/${read.data.id}?host=${encodeURIComponent(props.host)}`}
              >
                Open observed snapshot
              </a>
              {props.onChoose && (
                <button
                  type="button"
                  onClick={() => props.onChoose!(read.data!.id, read.data!)}
                >
                  {props.chooseLabel ?? "Use observed snapshot as baseline"}
                </button>
              )}
              {props.baselineId && props.baselineId !== read.data.id && (
                <p className="notice">
                  The selected baseline differs from the last observed active
                  snapshot. An open comparison keeps its captured references.
                </p>
              )}
            </>
          )}
        </>
      )}
    </section>
  );
}
