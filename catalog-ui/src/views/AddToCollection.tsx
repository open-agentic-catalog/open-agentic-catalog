import { useRef, useState } from "react";
import { result } from "../api/client";
import { availablePermission, permitsNamespace } from "../api/permissions";
import { ChoiceField } from "./authoring";
import { CommandDialog, ReadState, useRead } from "./workspace";
import type { Command, WorkspaceProps } from "./workspace";
import { useNamespaceAccessState } from "./namespace-access";
import * as checks from "./collection-responses";
import { PersonalReceipt } from "./responses";

export function AddToCollection(
  props: Pick<WorkspaceProps, "client" | "access" | "onFailure"> & {
    listingId: string;
    basePath: string;
    compact?: boolean;
  },
) {
  const SectionHeading = props.compact ? "h3" : "h2";
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState("");
  const [command, setCommand] = useState<Command>();
  const [message, setMessage] = useState("");
  const opener = useRef<HTMLButtonElement>(null);
  const read = useRead(
    JSON.stringify([open, selected, props.listingId, props.access]),
    async (signal) => {
      if (!open || !selected) return undefined;
      const reply = await props.client.api.GET(
        "/api/v1/collections/{collectionId}",
        { params: { path: { collectionId: selected } }, signal },
      );
      return {
        item: result(reply, checks.collection),
        etag: reply.response.headers.get("ETag"),
      };
    },
    props.onFailure,
  );
  const scope = useNamespaceAccessState(
    props,
    read.data?.item.namespaceId ?? "",
  );
  const allowed =
    !!read.data?.etag &&
    !read.busy &&
    !read.error &&
    !scope.busy &&
    !scope.error &&
    permitsNamespace(
      scope.access,
      "collection.manage",
      "catalog.write",
      read.data.item.namespaceId,
    );
  const offer = availablePermission(
    props.access,
    "collection.manage",
    "catalog.write",
  );
  if (!offer && !open && !command && !message) return null;
  return (
    <section className="workspace-panel">
      <SectionHeading>Shared collections</SectionHeading>
      <p>
        Add a reference to this Listing. The collection follows later eligible
        releases and does not grant access to the entry.
      </p>
      <button
        type="button"
        ref={opener}
        disabled={!offer && !open}
        onClick={() => {
          setOpen(!open);
          setMessage("");
        }}
      >
        {open ? "Close collection picker" : "Add to collection"}
      </button>
      {message && <p role="status">{message}</p>}
      {open && (
        <>
          <ChoiceField
            label="Destination collection"
            manualLabel="Collection ID"
            value={selected}
            onChange={setSelected}
            onFailure={props.onFailure}
            nextSteps={
              <a
                href={`${props.basePath}/collections/new`}
                target="_blank"
                rel="noopener noreferrer"
              >
                Create a collection
              </a>
            }
            load={async (cursor, signal) => {
              const page = result(
                await props.client.api.GET("/api/v1/collections", {
                  params: {
                    query: { limit: 20, ...(cursor ? { cursor } : {}) },
                  },
                  signal,
                }),
                checks.collectionPage,
              );
              return {
                ...page,
                items: page.items.map((item) => ({
                  id: item.id,
                  label: `${item.name} / ${item.namespaceId} / ${item.id}`,
                })),
              };
            }}
          />
          {selected && (
            <>
              <ReadState {...read} />
              <ReadState {...scope} />
              <p className="muted">
                You need collection management in the selected owner's
                namespace.
              </p>
              <button
                type="button"
                disabled={!allowed}
                onClick={() => {
                  const snapshot = read.data;
                  if (!allowed || !snapshot?.etag) return;
                  const collectionId = snapshot.item.id,
                    etag = snapshot.etag,
                    listingId = props.listingId;
                  setCommand({
                    title: "Add entry to collection",
                    description:
                      "Store one local Listing reference. Its source ownership and publication stay unchanged.",
                    summary: [
                      { label: "Collection", value: snapshot.item.name },
                      { label: "Collection ID", value: collectionId },
                      { label: "Listing ID", value: listingId },
                    ],
                    run: async (key) => {
                      result(
                        await props.client.api.POST(
                          "/api/v1/collections/{collectionId}/members",
                          {
                            params: {
                              path: { collectionId },
                              header: {
                                "If-Match": etag,
                                "Idempotency-Key": key,
                              },
                            },
                            body: { listingId },
                          },
                        ),
                        PersonalReceipt,
                      );
                      return "Entry added to collection.";
                    },
                  });
                }}
              >
                Review addition
              </button>
            </>
          )}
        </>
      )}
      {command && (
        <CommandDialog
          command={command}
          allowed={allowed}
          onClose={() => setCommand(undefined)}
          onDone={(text) => {
            setCommand(undefined);
            setOpen(false);
            setSelected("");
            setMessage(text);
            opener.current?.focus();
          }}
          onFailure={props.onFailure}
        />
      )}
    </section>
  );
}
