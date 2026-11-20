import { useState } from "react";
import { result, type Client } from "../api/client";
import type { components } from "../api/schema";
import { cursor, identifier, object, text } from "../api/values";
import { relationshipLabels } from "./listing-metadata";
import { Relationship } from "./responses";
import { Pager, ReadState, useRead } from "./workspace";

type Declaration = components["schemas"]["Relationship"];
type View = "outgoing" | "incoming" | "dependencies";
type Props = {
  client: Client;
  basePath: string;
  onFailure: (error: unknown) => void;
  releaseId: string;
  view: View;
  declarations?: Declaration[] | undefined;
};

const headings = {
  outgoing: "Declared relationships",
  incoming: "Listings that refer to this resource",
  dependencies: "Declared dependency impact",
};

function validPage(value: unknown, view: View, declarations: Declaration[]) {
  if (
    !object(value) ||
    !Array.isArray(value.items) ||
    !cursor(value.nextCursor) ||
    value.items.length > (view === "outgoing" ? 64 : 20)
  )
    return false;
  if (
    view === "outgoing" &&
    (value.items.length !== declarations.length ||
      value.nextCursor !== undefined)
  )
    return false;
  return value.items.every((item: unknown, index: number) => {
    if (
      !object(item) ||
      !object(item.declaration) ||
      !Relationship(item.declaration)
    )
      return false;
    const d = item.declaration;
    if (
      view === "outgoing" &&
      (d.type !== declarations[index]?.type ||
        d.targetVersion !== declarations[index]?.targetVersion ||
        d.targetIdentifier !== declarations[index]?.targetIdentifier)
    )
      return false;
    if (view === "dependencies" && d.type !== "depends_on") return false;
    const r = item.related;
    if (r === undefined) return view === "outgoing";
    return (
      object(r) &&
      identifier(r.releaseId) &&
      identifier(r.listingRevisionId) &&
      text(r.displayName) &&
      text(r.version) &&
      (view !== "outgoing" || !d.targetVersion || d.targetVersion === r.version)
    );
  });
}

export function ReleaseRelationships({
  client,
  basePath,
  onFailure,
  releaseId,
  view,
  declarations = [],
}: Props) {
  const [opened, setOpened] = useState(false);
  const [page, setPage] = useState("");
  const read = useRead(
    JSON.stringify([releaseId, view, opened, page]),
    async (signal) => {
      if (!opened) return undefined;
      return result(
        await client.api.GET(
          "/api/v1/listing-releases/{releaseId}/relationships",
          {
            params: {
              path: { releaseId },
              query: {
                view,
                limit: view === "outgoing" ? 100 : 20,
                ...(page && { cursor: page }),
              },
            },
            signal,
          },
        ),
        (value) => validPage(value, view, declarations),
      );
    },
    onFailure,
  );
  const items =
    read.data?.items ??
    (view === "outgoing"
      ? declarations.map((declaration) => ({ declaration, related: undefined }))
      : []);
  return (
    <section aria-label={headings[view]} className="workspace-panel">
      <h3>{headings[view]}</h3>
      {view === "dependencies" && (
        <p>
          Only currently readable Listings are included. An unversioned
          dependency may be affected. These declarations do not show runtime use
          or change any dependent release.
        </p>
      )}
      {view === "incoming" && (
        <p>
          Currently readable local Listings with a declaration about this
          resource, across all declared versions.
        </p>
      )}
      <button
        type="button"
        disabled={opened && read.busy}
        onClick={() => (opened ? read.refresh() : setOpened(true))}
      >
        {view === "outgoing"
          ? "Check current destinations"
          : view === "dependencies"
            ? "Check declared dependencies"
            : "Find referring Listings"}
      </button>
      {opened && <ReadState {...read} />}
      {items.length > 0 && (
        <ul>
          {items.map(({ declaration, related }, index) => (
            <li key={index}>
              {view !== "outgoing" && related && (
                <>
                  <a href={basePath + "/catalog/" + related.releaseId}>
                    {related.displayName} ({related.version})
                  </a>
                  {" · "}
                </>
              )}
              {relationshipLabels[declaration.type]}{" "}
              <code>{declaration.targetIdentifier}</code>
              {declaration.targetVersion ? (
                <>
                  {" "}
                  / {declaration.targetVersion}
                  {view === "dependencies" && " (exact version)"}
                </>
              ) : (
                " (unversioned)"
              )}
              {view === "outgoing" && related && (
                <>
                  {" · "}
                  <a href={basePath + "/catalog/" + related.releaseId}>
                    {related.displayName} ({related.version})
                  </a>
                </>
              )}
            </li>
          ))}
        </ul>
      )}
      {read.data?.items.length === 0 && (
        <p>No currently readable declarations match.</p>
      )}
      {view !== "outgoing" && (
        <Pager
          label={headings[view] + " pages"}
          cursor={page}
          nextCursor={read.data?.nextCursor}
          onPage={setPage}
          busy={read.busy}
        />
      )}
    </section>
  );
}
