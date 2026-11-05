import { Facts } from "./Facts";
import { controlIcons } from "../shell/Icon";
import { ScrollableTable } from "./ScrollableTable";
import { CatalogDirectoryFilters } from "./catalog-directory-filters";
import { characterText } from "./input-validation";
import * as checks from "./responses";
// Namespace administration binds metadata revisions to the ETag returned by the selected read.
import { useId, useState } from "react";
import { compareTableText, usePageSort } from "./table-sort";
import { result } from "../api/client";
import { acknowledged } from "./audit-handoff";
import type { components } from "../api/schema";
import {
  availablePermission,
  permits,
  permitsNamespace,
} from "../api/permissions";
import {
  CommandDialog,
  Field,
  Heading,
  Pager,
  ReadState,
  Reason,
  useRead,
  useUnsaved,
} from "./workspace";
import type { Command, WorkspaceProps as Props } from "./workspace";
import { useNamespaceAccessState } from "./namespace-access";

type Namespace = components["schemas"]["Namespace"];

/** Select namespace list, creation or detail from the route and current access hints. */
export default function Namespaces(props: Props) {
  const read = availablePermission(
    props.access,
    "namespace.read",
    "catalog.read",
  );
  const create = permits(props.access, "namespace.create", "tenant.admin");
  const identityKey = JSON.stringify([
    props.id,
    props.access.tenantId,
    props.access.realmId,
    props.access.membershipId,
    props.access.subject,
    props.access.actorType,
  ]);
  return (
    <>
      <Heading
        title="Namespaces"
        icon="catalog"
        description="Organize locally governed resources and namespace-scoped access."
      />
      {props.id === "new" ? (
        <>
          {!create && (
            <p role="alert">
              Your current access does not permit namespace creation.
            </p>
          )}
          <NamespaceEditor key={identityKey} {...props} />
        </>
      ) : read ? (
        props.id ? (
          <NamespaceDetail key={identityKey} {...props} />
        ) : (
          <NamespaceList {...props} />
        )
      ) : (
        <p className="notice">
          Namespace browsing requires namespace.read and catalog.read when
          scopes are enforced.{" "}
          {create && (
            <a href={`${props.runtime.basePath}/namespaces/new`}>
              Create a namespace
            </a>
          )}
        </p>
      )}
    </>
  );
}

/** Page namespace summaries while leaving creation as an explicit navigation action. */
function NamespaceList(props: Props) {
  const sortHint = useId();
  const [cursor, setCursor] = useState("");
  const [filters, setFilters] = useState<{
    displayName?: string;
    slug?: string;
  }>({});
  const page = useRead(
    JSON.stringify([cursor, filters]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/namespaces", {
          params: {
            query: {
              ...filters,
              limit: 25,
              ...(cursor ? { cursor: cursor } : {}),
            },
          },
          signal,
        }),
        checks.NamespacePage,
      ),
    props.onFailure,
  );
  const refresh = () => (page.error && cursor ? setCursor("") : page.refresh());
  const sorted = usePageSort(page.data?.items, {
    name: (a, b) => compareTableText(a.displayName, b.displayName),
    slug: (a, b) => compareTableText(a.slug, b.slug),
  });
  return (
    <>
      <CatalogDirectoryFilters
        kind="namespace"
        busy={page.busy}
        applied={filters}
        onApply={(next) => {
          setCursor("");
          setFilters(next);
          if (!cursor && JSON.stringify(next) === JSON.stringify(filters))
            page.refresh();
        }}
      />
      <div className="workspace-toolbar">
        <button disabled={page.busy} onClick={refresh}>
          {controlIcons["refresh"]} Refresh
        </button>
        {permits(props.access, "namespace.create", "tenant.admin") && (
          <a
            className="button primary"
            href={`${props.runtime.basePath}/namespaces/new`}
          >
            {controlIcons["add"]} Create namespace
          </a>
        )}
      </div>
      <ReadState {...page} refresh={refresh} />
      {page.data && (
        <>
          <p id={sortHint} className="muted">
            Select a column heading to sort the results on this page.
          </p>
          <ScrollableTable label="Namespaces">
            <table className="workspace-table" aria-describedby={sortHint}>
              <thead>
                <tr>
                  {sorted.heading("Name", "name")}
                  {sorted.heading("Slug", "slug")}
                  <th scope="col">Description</th>
                </tr>
              </thead>
              <tbody>
                {sorted.rows.map((item) => (
                  <tr key={item.id}>
                    <td>
                      <a
                        href={`${props.runtime.basePath}/namespaces/${item.id}`}
                      >
                        {item.displayName}
                      </a>
                    </td>
                    <td>
                      <code>{item.slug}</code>
                    </td>
                    <td>{item.description || "No description"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!page.data.items.length && (
              <p className="workspace-empty">
                {filters.displayName || filters.slug
                  ? "No namespaces match the applied filters."
                  : "No namespaces on this page."}
              </p>
            )}
          </ScrollableTable>
        </>
      )}
      <Pager
        label="Namespace inventory pagination"
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        busy={page.busy}
        onPage={setCursor}
      />
    </>
  );
}

/** Load namespace metadata and its ETag before presenting the editor. */
function NamespaceDetail(props: Props) {
  const detail = useRead(
    props.id,
    async (signal) => {
      const reply = await props.client.api.GET(
        "/api/v1/namespaces/{namespaceId}",
        { params: { path: { namespaceId: props.id } }, signal },
      );
      return {
        item: result(reply, checks.Namespace),
        etag: reply.response.headers.get("ETag"),
      };
    },
    props.onFailure,
  );
  return (
    <>
      <a className="back-link" href={`${props.runtime.basePath}/namespaces`}>
        Back to namespaces
      </a>
      <ReadState {...detail} />
      {detail.data && (
        <NamespaceEditor
          key={`${detail.data.item.id}:${detail.data.etag}`}
          {...props}
          item={detail.data.item}
          etag={detail.data.etag}
          ready={!detail.busy && !detail.error}
          refresh={detail.refresh}
        />
      )}
    </>
  );
}

/** Prepare namespace creation or revision with captured metadata and concurrency evidence. */
function NamespaceEditor({
  item,
  etag,
  ready = true,
  refresh,
  ...props
}: Props & {
  item?: Namespace;
  etag?: string | null;
  ready?: boolean;
  refresh?: () => void;
}) {
  const [name, setName] = useState(item?.displayName ?? "");
  const [slug, setSlug] = useState(item?.slug ?? "");
  const [description, setDescription] = useState(item?.description ?? "");
  const [reason, setReason] = useState("");
  const [command, setCommand] = useState<Command>();
  const [message, setMessage] = useState("");
  const [inputError, setInputError] = useState("");
  const namespaceAccess = useNamespaceAccessState(props, item?.id ?? "");
  const writable = item
    ? permitsNamespace(
        namespaceAccess.access,
        "namespace.manage",
        "tenant.admin",
        item.id,
      )
    : permits(props.access, "namespace.create", "tenant.admin");
  const commandReady =
    ready &&
    !namespaceAccess.busy &&
    !namespaceAccess.error &&
    (!item || !!etag);
  const changed =
    name !== (item?.displayName ?? "") ||
    slug !== (item?.slug ?? "") ||
    description !== (item?.description ?? "");
  const dirty = changed || !!reason;
  const showEditor = writable || dirty || !!command;
  const [saved, setSaved] = useState(false);
  useUnsaved(dirty && !saved);
  return (
    <section className="workspace-panel">
      <h2>{item ? item.displayName : "Create namespace"}</h2>
      {item && <ReadState {...namespaceAccess} />}
      {item && (
        <Facts
          rows={[
            ["Namespace ID", <code>{item.id}</code>],
            ["Revision", item.revision],
            ...(!showEditor
              ? ([
                  ["Display name", item.displayName],
                  ["Slug", <code>{item.slug}</code>],
                  ["Description", item.description || "No description"],
                ] as const)
              : []),
          ]}
        />
      )}
      {message && (
        <p className="notice" role="status">
          {message}
        </p>
      )}
      {inputError && <p role="alert">{inputError}</p>}
      {showEditor && (
        <form
          className="workspace-form"
          onSubmit={(e) => {
            e.preventDefault();
            if (
              !writable ||
              !commandReady ||
              !name.trim() ||
              (item && !reason.trim())
            )
              return;
            setInputError("");
            try {
              const body = {
                displayName: characterText(name.trim(), "Display name", 160, 1),
                description: characterText(description, "Description", 2000),
              };
              setCommand({
                title: item ? "Save namespace changes" : "Create namespace",
                icon: item ? "save" : "add",
                summary: [
                  {
                    label: "Namespace ID",
                    value:
                      item?.id ?? "New object; ID is returned after creation",
                  },
                  { label: "Slug", value: item?.slug ?? slug },
                  ...(item
                    ? [
                        {
                          label: "Base ETag",
                          value: etag ?? "Unavailable; reload before saving",
                        },
                      ]
                    : []),
                  {
                    label: "Display name",
                    value: item
                      ? `${item.displayName} → ${body.displayName}`
                      : body.displayName,
                  },
                  {
                    label: "Description",
                    value: item
                      ? `${item.description ?? ""} → ${body.description}`
                      : body.description || "None",
                  },
                  ...(item ? [{ label: "Reason", value: reason.trim() }] : []),
                ],
                description: item
                  ? `Update ${item.slug}. Its stable slug does not change. Reason: ${reason.trim()}`
                  : `Create ${slug} in your current tenant.`,
                run: async (key) => {
                  if (item) {
                    if (!etag)
                      throw new Error(
                        "Reload the namespace to obtain its current revision.",
                      );
                    const reply = await props.client.api.PATCH(
                      "/api/v1/namespaces/{namespaceId}",
                      {
                        params: {
                          path: { namespaceId: item.id },
                          header: { "Idempotency-Key": key, "If-Match": etag },
                        },
                        body: { ...body, reason: reason.trim() },
                      },
                    );
                    const saved = result(reply, checks.Namespace);
                    return acknowledged(
                      "Namespace updated. Reload to view its current revision.",
                      reply.response,
                      {
                        action: "namespace.updated",
                        targetType: "namespace",
                        targetId: item.id,
                        revision: saved.revision,
                      },
                    );
                  }
                  const reply = await props.client.api.POST(
                    "/api/v1/namespaces",
                    {
                      params: { header: { "Idempotency-Key": key } },
                      body: { ...body, slug },
                    },
                  );
                  const created = result(reply, checks.Namespace);
                  return acknowledged(
                    `Created namespace ${created.slug}. ID: ${created.id}`,
                    reply.response,
                    {
                      action: "namespace.created",
                      targetType: "namespace",
                      targetId: created.id,
                      revision: created.revision,
                    },
                  );
                },
              });
            } catch (cause) {
              // Only local character validators run in this synchronous block.
              setInputError((cause as Error).message);
            }
          }}
        >
          <fieldset
            disabled={!writable || !!command || saved}
            className="workspace-fields"
          >
            <Field label="Display name">
              <input
                required
                maxLength={320}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </Field>
            <Field
              label="Slug"
              hint="A short URL-friendly name, for example customer-support. Use lowercase letters, numbers and hyphens."
            >
              <input
                required
                readOnly={!!item}
                pattern="[a-z0-9]+(-[a-z0-9]+)*"
                maxLength={64}
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
              />
            </Field>
            <Field label="Description">
              <textarea
                maxLength={4000}
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                rows={4}
              />
            </Field>
            {item && <Reason value={reason} onChange={setReason} />}
            {writable && (
              <button
                className="primary"
                disabled={
                  !commandReady || (!!item && (!changed || !reason.trim()))
                }
                type="submit"
              >
                {controlIcons[item ? "save" : "add"]}{" "}
                {item ? "Review changes" : "Create namespace"}
              </button>
            )}
          </fieldset>
        </form>
      )}
      {!writable && (
        <p className="muted">
          Read-only. Namespace changes require namespace.create or
          namespace.manage for the selected action, and tenant.admin when scopes
          are enforced.
        </p>
      )}
      <div className="workspace-toolbar">
        {refresh && (
          <button
            onClick={() => {
              if (
                !dirty ||
                saved ||
                confirm("Discard unsaved changes and reload this namespace?")
              )
                refresh();
            }}
          >
            Reload namespace
          </button>
        )}
        <a href={`${props.runtime.basePath}/namespaces`}>Back to namespaces</a>
      </div>
      {command && (
        <CommandDialog
          command={command}
          allowed={commandReady && writable && !saved}
          onClose={() => setCommand(undefined)}
          onFailure={props.onFailure}
          onDone={(text) => {
            setSaved(true);
            setCommand(undefined);
            setMessage(text);
          }}
        />
      )}
    </section>
  );
}
