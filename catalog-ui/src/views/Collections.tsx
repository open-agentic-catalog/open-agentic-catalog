import { useId, useState } from "react";
import type { components } from "../api/schema";
import { result, type Kind } from "../api/client";
import { availablePermission, permitsNamespace } from "../api/permissions";
import { kindLabel } from "../resource-kinds";
import {
  CommandDialog,
  Field,
  Heading,
  Pager,
  ReadState,
  useRead,
  useUnsaved,
} from "./workspace";
import type { Command, WorkspaceProps } from "./workspace";
import { NamespaceChoice } from "./NamespaceChoice";
import { useNamespaceAccessState } from "./namespace-access";
import { CatalogPresentation, SelectedNamespace } from "./CatalogPresentation";
import * as checks from "./collection-responses";
import { PersonalReceipt } from "./responses";
import { characterText } from "./input-validation";
import { KindSelection } from "./KindSelection";

type Collection = components["schemas"]["SharedCollection"];
export default function Collections(props: WorkspaceProps) {
  return (
    <>
      <Heading
        title="Shared collections"
        icon="book"
        description="Curate existing local entries across namespaces. Each entry follows its current release and keeps its own access rules."
      />
      {props.id === "new" ? (
        <CollectionEditor {...props} />
      ) : props.id ? (
        <CollectionDetail key={props.id} {...props} />
      ) : (
        <CollectionList {...props} />
      )}
    </>
  );
}
function CollectionList(props: WorkspaceProps) {
  const [cursor, setCursor] = useState("");
  const list = useRead(
    JSON.stringify([props.access, cursor]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/collections", {
          params: { query: { limit: 20, ...(cursor ? { cursor } : {}) } },
          signal,
        }),
        checks.collectionPage,
      ),
    props.onFailure,
  );
  return (
    <section className="workspace-panel">
      <div className="workspace-toolbar">
        <button type="button" onClick={list.refresh}>
          Refresh collections
        </button>
        {availablePermission(
          props.access,
          "collection.manage",
          "catalog.write",
        ) && (
          <a
            className="button"
            href={`${props.runtime.basePath}/collections/new`}
          >
            Create collection
          </a>
        )}
      </div>
      <ReadState {...list} />
      {list.data && (
        <>
          <div className="card-grid">
            {list.data.items.map((item) => (
              <article className="resource-card" key={item.id}>
                <h2>
                  <a href={`${props.runtime.basePath}/collections/${item.id}`}>
                    {item.name}
                  </a>
                </h2>
                <p>{item.description || "No description"}</p>
                <p>
                  Owned by{" "}
                  <SelectedNamespace
                    id={item.namespaceId}
                    client={props.client}
                    access={props.access}
                    onFailure={props.onFailure}
                  />
                </p>
                <small>Collection ID: {item.id}</small>
              </article>
            ))}
          </div>
          {list.data.items.length === 0 && (
            <p className="workspace-empty">
              No collections are available with your current access.
            </p>
          )}
          <Pager
            cursor={cursor}
            nextCursor={list.data.nextCursor}
            onPage={setCursor}
            busy={list.busy}
          />
        </>
      )}
    </section>
  );
}
function CollectionDetail(props: WorkspaceProps) {
  const [cursor, setCursor] = useState("");
  const [filters, setFilters] = useState<{ text: string; kinds: Kind[] }>({
    text: "",
    kinds: [],
  });
  const [draft, setDraft] = useState(filters);
  const [filterError, setFilterError] = useState("");
  const filterErrorId = useId();
  const pendingFilters = JSON.stringify(draft) !== JSON.stringify(filters);
  const [command, setCommand] = useState<Command>();
  const [deleting, setDeleting] = useState(false);
  const [deleted, setDeleted] = useState(false);
  const detail = useRead(
    props.id,
    async (signal) => {
      const response = await props.client.api.GET(
        "/api/v1/collections/{collectionId}",
        { params: { path: { collectionId: props.id } }, signal },
      );
      return {
        item: result(response, checks.collection),
        etag: response.response.headers.get("ETag"),
      };
    },
    props.onFailure,
  );
  const members = useRead(
    JSON.stringify([
      props.id,
      props.access,
      cursor,
      filters,
      detail.data?.item.revision,
    ]),
    async (signal) =>
      result(
        await props.client.api.GET(
          "/api/v1/collections/{collectionId}/members",
          {
            params: {
              path: { collectionId: props.id },
              query: {
                limit: 20,
                ...(cursor ? { cursor } : {}),
                ...(filters.text ? { text: filters.text } : {}),
                ...(filters.kinds.length ? { kinds: filters.kinds } : {}),
              },
            },
            signal,
          },
        ),
        checks.memberPage,
      ),
    props.onFailure,
  );
  const scope = useNamespaceAccessState(
    props,
    detail.data?.item.namespaceId ?? "",
  );
  const manage =
    !!detail.data?.etag &&
    !detail.busy &&
    !detail.error &&
    !scope.busy &&
    !scope.error &&
    permitsNamespace(
      scope.access,
      "collection.manage",
      "catalog.write",
      detail.data.item.namespaceId,
    );
  function refresh() {
    setCursor("");
    detail.refresh();
    members.refresh();
  }
  function refreshMembers() {
    if (cursor) setCursor("");
    else members.refresh();
  }
  function remove(listingId?: string) {
    const selected = detail.data;
    if (!selected?.etag || !manage) return;
    const id = selected.item.id,
      etag = selected.etag;
    setDeleting(!listingId);
    setCommand({
      title: listingId ? "Remove entry from collection" : "Delete collection",
      description: listingId
        ? "Remove this Listing reference. The source entry and its history stay unchanged."
        : "Delete this collection and all its references, including entries you cannot currently read. Source entries stay unchanged.",
      summary: [
        { label: "Collection", value: selected.item.name },
        { label: "Collection ID", value: id },
        ...(listingId ? [{ label: "Listing ID", value: listingId }] : []),
      ],
      run: async (key) => {
        const reply = listingId
          ? await props.client.api.DELETE(
              "/api/v1/collections/{collectionId}/members/{listingId}",
              {
                params: {
                  path: { collectionId: id, listingId },
                  header: { "If-Match": etag, "Idempotency-Key": key },
                },
              },
            )
          : await props.client.api.DELETE(
              "/api/v1/collections/{collectionId}",
              {
                params: {
                  path: { collectionId: id },
                  header: { "If-Match": etag, "Idempotency-Key": key },
                },
              },
            );
        result(reply, PersonalReceipt);
        return listingId ? "Entry removed." : "Collection deleted.";
      },
    });
  }
  if (deleted)
    return (
      <section className="workspace-panel">
        <p role="status">
          Collection deleted. Source entries remain unchanged.
        </p>
        <a href={`${props.runtime.basePath}/collections`}>
          Back to collections
        </a>
      </section>
    );
  return (
    <>
      <a className="back-link" href={`${props.runtime.basePath}/collections`}>
        Back to collections
      </a>
      <ReadState {...detail} />
      {detail.data && (
        <>
          <CollectionEditor
            key={`${detail.data.item.id}:${detail.data.etag}`}
            {...props}
            item={detail.data.item}
            etag={detail.data.etag}
            ready={!detail.busy && !detail.error}
            refresh={refresh}
          />
          <section className="workspace-panel">
            <h2>Current entries</h2>
            <p>
              Entries appear here when you can read them and a current release
              is available.
            </p>
            <form
              className="workspace-form"
              aria-label="Filter collection entries"
              onSubmit={(event) => {
                event.preventDefault();
                try {
                  characterText(draft.text, "Collection text", 256);
                  setFilterError("");
                  if (!pendingFilters && !cursor) members.refresh();
                  setFilters(draft);
                  setCursor("");
                } catch (error) {
                  setFilterError(
                    error instanceof Error
                      ? error.message
                      : "Check the collection filters.",
                  );
                }
              }}
            >
              <Field
                label="Search collection entries"
                help="Match every whole word in released names and descriptions. No ranking or wildcard search."
              >
                <input
                  type="search"
                  value={draft.text}
                  aria-invalid={!!filterError}
                  aria-describedby={filterError ? filterErrorId : undefined}
                  onChange={(event) =>
                    setDraft({ ...draft, text: event.target.value })
                  }
                />
              </Field>
              <KindSelection
                value={draft.kinds}
                onChange={(kinds) => setDraft({ ...draft, kinds })}
              />
              {filterError && (
                <p role="alert" id={filterErrorId}>
                  {filterError}
                </p>
              )}
              {pendingFilters && (
                <p className="muted">
                  Filter edits are pending. Apply filters to update the entries.
                </p>
              )}
              <div className="workspace-toolbar">
                <button type="submit" disabled={members.busy}>
                  Apply filters
                </button>
                <button
                  type="button"
                  disabled={members.busy}
                  onClick={() => {
                    const empty = { text: "", kinds: [] };
                    setDraft(empty);
                    setFilters(empty);
                    setFilterError("");
                    if (!filters.text && !filters.kinds.length && !cursor)
                      members.refresh();
                    setCursor("");
                  }}
                >
                  Clear filters
                </button>
              </div>
            </form>
            <div className="workspace-toolbar">
              <button type="button" onClick={refreshMembers}>
                Refresh current entries
              </button>
              {manage && (
                <button type="button" onClick={() => remove()}>
                  Delete collection
                </button>
              )}
            </div>
            {(filters.text || filters.kinds.length > 0) && (
              <p role="status">
                Applied filters: {filters.text || "any text"};{" "}
                {filters.kinds.length
                  ? filters.kinds.map(kindLabel).join(", ")
                  : "all kinds"}
                .
              </p>
            )}
            <ReadState {...members} />
            {members.data && (
              <>
                <div className="card-grid">
                  {members.data.items.map((item) => (
                    <article
                      className="resource-card"
                      data-kind={item.resource.kind}
                      key={item.listing.id}
                    >
                      <h3>
                        <a
                          href={`${props.runtime.basePath}/catalog/${item.release.id}`}
                        >
                          {item.listingRevision.displayName}
                        </a>
                      </h3>
                      <p>{item.listingRevision.description}</p>
                      <p>
                        {kindLabel(item.resource.kind)} / {item.version.version}{" "}
                        / {item.release.state}
                      </p>
                      <CatalogPresentation
                        display={item.display}
                        namespaceId={item.listing.namespaceId}
                      />
                      <p>
                        <small>Release: {item.release.id}</small>
                      </p>
                      {manage && (
                        <button
                          type="button"
                          onClick={() => remove(item.listing.id)}
                        >
                          Remove from collection
                        </button>
                      )}
                    </article>
                  ))}
                </div>
                {members.data.items.length === 0 && (
                  <p className="workspace-empty">
                    {filters.text || filters.kinds.length
                      ? "No currently readable entries match these filters."
                      : "No currently readable entries."}{" "}
                    References can reappear when access or a current release
                    becomes available.
                  </p>
                )}
                <Pager
                  cursor={cursor}
                  nextCursor={members.data.nextCursor}
                  onPage={setCursor}
                  busy={members.busy}
                />
              </>
            )}
            {members.error && cursor && (
              <button type="button" onClick={() => setCursor("")}>
                Start from first page
              </button>
            )}
          </section>
        </>
      )}
      {command && (
        <CommandDialog
          command={command}
          allowed={manage && !detail.busy && !detail.error}
          onClose={() => setCommand(undefined)}
          onDone={() => {
            setCommand(undefined);
            if (deleting) setDeleted(true);
            else refresh();
          }}
          onFailure={props.onFailure}
        />
      )}
    </>
  );
}
function CollectionEditor({
  item,
  etag,
  ready = true,
  refresh,
  ...props
}: WorkspaceProps & {
  item?: Collection;
  etag?: string | null;
  ready?: boolean;
  refresh?: () => void;
}) {
  const [namespace, setNamespace] = useState(item?.namespaceId ?? "");
  const [name, setName] = useState(item?.name ?? "");
  const [description, setDescription] = useState(item?.description ?? "");
  const [error, setError] = useState("");
  const [command, setCommand] = useState<Command>();
  const [saved, setSaved] = useState(false);
  const [createdId, setCreatedId] = useState("");
  const scope = useNamespaceAccessState(props, namespace);
  const allowed =
    ready &&
    !scope.busy &&
    !scope.error &&
    !!namespace &&
    permitsNamespace(
      scope.access,
      "collection.manage",
      "catalog.write",
      namespace,
    ) &&
    (!item || !!etag);
  const changed =
    name !== (item?.name ?? "") ||
    description !== (item?.description ?? "") ||
    namespace !== (item?.namespaceId ?? "");
  useUnsaved(changed && !saved);
  return (
    <section className="workspace-panel">
      <h2>{item ? item.name : "Create collection"}</h2>
      {item && (
        <p>
          Owner namespace:{" "}
          <SelectedNamespace
            id={item.namespaceId}
            client={props.client}
            access={props.access}
            onFailure={props.onFailure}
          />{" "}
          / Revision {item.revision}
        </p>
      )}
      <ReadState {...scope} />
      {saved && createdId ? (
        <p role="status">
          Collection created.{" "}
          <a href={`${props.runtime.basePath}/collections/${createdId}`}>
            Open collection
          </a>
        </p>
      ) : !item || allowed || changed ? (
        <form
          className="workspace-form"
          onSubmit={(event) => {
            event.preventDefault();
            if (!allowed) return;
            try {
              const body = {
                name: characterText(name.trim(), "Name", 160, 1),
                description: characterText(
                  description.trim(),
                  "Description",
                  2000,
                ),
              };
              const owner = namespace,
                id = item?.id,
                validator = etag;
              setError("");
              setCommand({
                title: item ? "Update collection" : "Create collection",
                description:
                  "Collection text is shared with collection readers. It is not automatically redacted when access to a mentioned entry changes.",
                summary: [
                  { label: "Name", value: body.name },
                  { label: "Owner namespace ID", value: owner },
                  { label: "Description", value: body.description || "None" },
                ],
                run: async (key) => {
                  const reply = id
                    ? await props.client.api.PATCH(
                        "/api/v1/collections/{collectionId}",
                        {
                          params: {
                            path: { collectionId: id },
                            header: {
                              "If-Match": validator!,
                              "Idempotency-Key": key,
                            },
                          },
                          body,
                        },
                      )
                    : await props.client.api.POST("/api/v1/collections", {
                        params: { header: { "Idempotency-Key": key } },
                        body: { namespaceId: owner, ...body },
                      });
                  const receipt = result(reply, PersonalReceipt);
                  if (!id) setCreatedId(receipt.id);
                  return "Collection saved.";
                },
              });
            } catch (e) {
              setError(
                e instanceof Error ? e.message : "Check collection text.",
              );
            }
          }}
        >
          {!item && (
            <NamespaceChoice
              workspace={props}
              label="Owner namespace"
              value={namespace}
              onChange={setNamespace}
              preserveDraft
            />
          )}
          <Field label="Name">
            <input
              required
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </Field>
          <Field
            label="Description"
            hint="Shared plain text, up to 2,000 characters."
          >
            <textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </Field>
          {error && <p role="alert">{error}</p>}
          <button type="submit" disabled={!allowed}>
            Review {item ? "changes" : "collection"}
          </button>
          {!allowed && (
            <p className="muted">
              Management requires collection.manage in the owner namespace and
              catalog.write when scopes are enforced.
            </p>
          )}
        </form>
      ) : (
        <p>{item.description || "No description"}</p>
      )}
      {refresh && (
        <div className="form-actions">
          <button
            type="button"
            onClick={() => {
              if (
                (!changed && !command) ||
                saved ||
                confirm("Discard unsaved changes and reload this collection?")
              )
                refresh();
            }}
          >
            Reload collection
          </button>
        </div>
      )}
      {command && (
        <CommandDialog
          command={command}
          allowed={allowed}
          onClose={() => setCommand(undefined)}
          onDone={() => {
            setSaved(true);
            setCommand(undefined);
            refresh?.();
          }}
          onFailure={props.onFailure}
        />
      )}
    </section>
  );
}
