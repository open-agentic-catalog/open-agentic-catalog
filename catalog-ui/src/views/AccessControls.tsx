import { controlIcons } from "../shell/Icon";
import { useEffect, useRef, useState } from "react";
import type { components } from "../api/schema";
import { permissionLabels, templateHelp } from "./access-labels";
import { result } from "../api/client";
import { permits, permitsNamespace } from "../api/permissions";
import { identifier } from "../api/values";
import * as checks from "./access-responses";
import * as records from "./responses";
import { Field, Pager, ReadState, useRead, confirmDiscard } from "./workspace";
import type { WorkspaceProps } from "./workspace";

type Role = components["schemas"]["RoleDefinition"];

export function BuiltinRoleHelp({
  role,
  namespaceEligible = role.namespaceCapable,
}: {
  role: Role;
  namespaceEligible?: boolean | undefined;
}) {
  const help = templateHelp(role);
  if (!help) return null;
  return (
    <section className="notice" aria-label="Built-in template help">
      <h3>About this template</h3>
      <p>{help.summary}</p>
      <p>{help.limits}</p>
      <p>
        {namespaceEligible
          ? "Allowed assignment scopes: tenant-wide or exact namespace."
          : "Allowed assignment scope: tenant-wide only."}{" "}
        The selected assignment scope and current backend checks determine
        access.
      </p>
    </section>
  );
}
export type DirectoryQuery = {
  name?: string;
  subject?: string;
  state?: "active" | "suspended" | "archived" | "offboarded";
  actorType?: "human" | "service";
  realmId?: string;
};

/** Filters change server selection; they never discard a downloaded page. */
export function DirectoryFilters({
  members = false,
  groups = false,
  applied,
  onApply,
}: {
  members?: boolean;
  groups?: boolean;
  applied?: DirectoryQuery;
  onApply: (query: DirectoryQuery) => void;
}) {
  const [name, setName] = useState(applied?.subject ?? applied?.name ?? "");
  const [state, setState] = useState<string>(applied?.state ?? "");
  const [actorType, setActorType] = useState<string>(applied?.actorType ?? "");
  const [realmId, setRealmId] = useState(applied?.realmId ?? "");
  useEffect(() => {
    if (!applied) return;
    setName(applied.subject ?? applied.name ?? "");
    setState(applied.state ?? "");
    setActorType(applied.actorType ?? "");
    setRealmId(applied.realmId ?? "");
  }, [applied]);
  return (
    <form
      className="workspace-toolbar"
      onSubmit={(event) => {
        event.preventDefault();
        if (!confirmDiscard()) return;
        onApply({
          ...(name ? (members ? { subject: name } : { name }) : {}),
          ...(state
            ? { state: state as NonNullable<DirectoryQuery["state"]> }
            : {}),
          ...(members && actorType
            ? {
                actorType: actorType as NonNullable<
                  DirectoryQuery["actorType"]
                >,
              }
            : {}),
          ...(members && realmId ? { realmId } : {}),
        });
      }}
    >
      <Field
        label={members ? "Exact subject" : "Exact name"}
        hint="Match the complete value exactly, including case. All filters must match."
      >
        <input value={name} onChange={(e) => setName(e.target.value)} />
      </Field>
      <Field label="State">
        <select value={state} onChange={(e) => setState(e.target.value)}>
          <option value="">All states</option>
          {![
            "",
            "active",
            ...(members
              ? ["suspended", "offboarded"]
              : groups
                ? ["suspended", "archived"]
                : ["archived"]),
          ].includes(state) && <option value={state}>Unsupported state</option>}
          <option value="active">Active</option>
          {members ? (
            <>
              <option value="suspended">Suspended</option>
              <option value="offboarded">Offboarded</option>
            </>
          ) : (
            <>
              {groups && <option value="suspended">Suspended</option>}
              <option value="archived">Archived</option>
            </>
          )}
        </select>
      </Field>
      {members && (
        <>
          <Field label="Identity type filter">
            <select
              value={actorType}
              onChange={(e) => setActorType(e.target.value)}
            >
              <option value="">All types</option>
              {!["", "human", "service"].includes(actorType) && (
                <option value={actorType}>Unsupported identity type</option>
              )}
              <option value="human">Human</option>
              <option value="service">Service</option>
            </select>
          </Field>
          <Field label="Exact realm ID">
            <input
              value={realmId}
              onChange={(e) => setRealmId(e.target.value)}
            />
          </Field>
        </>
      )}
      <div className="workspace-actions">
        <button type="submit">{controlIcons["filters"]} Apply filters</button>
        <button
          type="button"
          onClick={() => {
            if (!confirmDiscard()) return;
            setName("");
            setState("");
            setActorType("");
            setRealmId("");
            onApply({});
          }}
        >
          {controlIcons["close"]}
          Clear filters
        </button>
      </div>
    </form>
  );
}

/** One selected reference, gated by its directory's own permission and scope. */
export function AccessReference({
  workspace,
  kind,
  id,
  refreshToken,
  realmId,
  compact = false,
}: {
  workspace: WorkspaceProps;
  refreshToken?: object;
  realmId?: string;
  compact?: boolean;
  kind: "role" | "member" | "group" | "namespace";
  id: string;
}) {
  const allowed =
    identifier(id) &&
    (kind === "namespace"
      ? permitsNamespace(workspace.access, "namespace.read", "catalog.read", id)
      : permits(workspace.access, "access.manage", "tenant.admin") &&
        (kind !== "group" || !realmId || realmId === workspace.access.realmId));
  const binding = JSON.stringify([
    kind,
    id,
    workspace.access.tenantId,
    workspace.access.membershipId,
    workspace.access.realmId,
    workspace.access.authorizationRevision,
    allowed,
  ]);
  function bound<T extends { id: string; tenantId: string }>(record: T): T {
    if (
      record.id !== id ||
      record.tenantId !== workspace.access.tenantId ||
      (kind === "group" &&
        "realmId" in record &&
        record.realmId !== workspace.access.realmId)
    )
      throw new Error("Reference details do not match the selected record.");
    return record;
  }
  const label = useRead(
    binding,
    async (signal) => {
      if (!allowed) return null;
      const caption = await (async () => {
        switch (kind) {
          case "role":
            return bound(
              result(
                await workspace.client.api.GET(
                  "/api/v1/access/roles/{roleId}",
                  {
                    params: { path: { roleId: id } },
                    signal,
                  },
                ),
                checks.role,
              ),
            ).name;
          case "member": {
            const m = bound(
              result(
                await workspace.client.api.GET(
                  "/api/v1/memberships/{membershipId}",
                  { params: { path: { membershipId: id } }, signal },
                ),
                records.Membership,
              ),
            );
            return `${m.subject} · ${m.actorType} · ${m.state}`;
          }
          case "group":
            return bound(
              result(
                await workspace.client.api.GET(
                  "/api/v1/access/groups/{groupId}",
                  { params: { path: { groupId: id } }, signal },
                ),
                checks.group,
              ),
            ).name;
          case "namespace":
            return bound(
              result(
                await workspace.client.api.GET(
                  "/api/v1/namespaces/{namespaceId}",
                  { params: { path: { namespaceId: id } }, signal },
                ),
                records.Namespace,
              ),
            ).displayName;
        }
      })();
      return { caption, binding };
    },
    workspace.onFailure,
  );
  const previous = useRef(refreshToken);
  const refresh = useRef(label.refresh);
  refresh.current = label.refresh;
  useEffect(() => {
    if (previous.current !== refreshToken) {
      previous.current = refreshToken;
      refresh.current();
    }
  }, [refreshToken]);
  return (
    <span>
      {label.data?.binding === binding
        ? `${label.data.caption}${compact ? "" : " · "}`
        : `${kind} name unavailable · `}
      {(!compact || label.data?.binding !== binding) && <code>{id}</code>}
    </span>
  );
}

export function GrantPreview({
  role,
  scope,
  namespaceId,
  namespaceEligible = role?.namespaceCapable,
}: {
  role?: Role | null | undefined;
  scope: string;
  namespaceId: string;
  namespaceEligible?: boolean | undefined;
}) {
  return (
    <section className="notice" aria-label="Proposed grant permissions">
      <h3>Proposed grant</h3>
      <p>
        {scope === "tenant"
          ? "Tenant-wide: namespace-capable actions cover every namespace, including future namespaces."
          : scope === "namespace"
            ? `Exact namespace: ${namespaceId || "choose an existing namespace"}.`
            : "Choose Tenant-wide or Specific namespace explicitly."}
      </p>
      {role ? (
        <>
          <p>
            {role.name} · <code>{role.key ?? role.id}</code>
          </p>
          <BuiltinRoleHelp role={role} namespaceEligible={namespaceEligible} />
          <ul>
            {role.permissions.map((p) => (
              <li key={p}>
                {permissionLabels[p]}: <code>{p}</code>
              </li>
            ))}
          </ul>
          {role.permissions.length === 0 && (
            <p>
              This template supplies no product permissions. Identity type
              controls Human or Service classification; baseline human self and
              ownership rights remain separate.
            </p>
          )}
          <p>
            {namespaceEligible
              ? "Namespace or tenant eligible."
              : "Tenant-wide only."}
          </p>
        </>
      ) : (
        <p>
          Role details are unverified or unavailable. This preview does not
          establish permissions; the API validates the exact reference on
          confirmation.
        </p>
      )}
      <p>
        Direct and group grants add together. Removing one path can leave
        overlapping access. Token scopes, human-only actions, target ownership
        and lifecycle requirements still apply. No companion is added
        automatically.
      </p>
    </section>
  );
}

export function MemberContributions(
  props: WorkspaceProps & { memberId: string },
) {
  return (
    <MemberContributionPage
      key={JSON.stringify([
        props.memberId,
        props.access.tenantId,
        props.access.membershipId,
        props.access.realmId,
        props.access.scopeEnforcement,
        props.access.scopes,
      ])}
      {...props}
    />
  );
}

function MemberContributionPage(props: WorkspaceProps & { memberId: string }) {
  const [cursor, setCursor] = useState("");
  const page = useRead(
    JSON.stringify([
      props.memberId,
      props.access.authorizationRevision,
      cursor,
    ]),
    async (signal) => {
      const value = result(
        await props.client.api.GET(
          "/api/v1/memberships/{membershipId}/access",
          {
            params: {
              path: { membershipId: props.memberId },
              query: { limit: 25, ...(cursor ? { cursor } : {}) },
            },
            signal,
          },
        ),
        checks.memberAccess,
      );
      if (
        value.membership.id !== props.memberId ||
        value.membership.tenantId !== props.access.tenantId ||
        (value.groupCoverage === "current_realm") !==
          (value.membership.realmId === props.access.realmId)
      )
        throw new Error(
          "Member contributions do not match the selected context.",
        );
      return value;
    },
    props.onFailure,
  );
  return (
    <section
      className="workspace-panel"
      aria-label="Member access contributions"
    >
      <h2>Member access contributions</h2>
      <p>
        Inspect these paths before lifecycle changes. Each page is partial; it
        is not a complete permission union or an OAuth decision for this
        recipient. Suspension preserves eligible paths until explicit
        reactivation. Offboarding is terminal and revokes direct grants and
        group relations.
      </p>
      <button
        type="button"
        disabled={page.busy}
        onClick={() => (cursor ? setCursor("") : page.refresh())}
      >
        {controlIcons["refresh"]}
        Refresh member contributions
      </button>
      <ReadState
        {...page}
        refresh={() => (cursor ? setCursor("") : page.refresh())}
      />
      {page.data && (
        <>
          <p>
            Current policy revision: {page.data.authorizationRevision}. Group
            coverage:{" "}
            {page.data.groupCoverage === "current_realm"
              ? "this identity realm"
              : "unavailable in another identity realm; direct grants only"}
            .
          </p>
          {page.data.items.map((item) => (
            <div className="policy-summary" key={item.assignment.id}>
              <p>
                <strong>{item.role.name}</strong> · {item.assignment.state} ·{" "}
                {item.effective
                  ? "Eligible assignment path"
                  : "Currently ineffective assignment path"}
              </p>
              <p>
                Assignment <code>{item.assignment.id}</code> ·{" "}
                {item.assignment.namespaceId ? (
                  <AccessReference
                    workspace={props}
                    kind="namespace"
                    id={item.assignment.namespaceId}
                    refreshToken={item}
                  />
                ) : (
                  "Tenant-wide"
                )}
              </p>
              <p>
                Role permissions: {item.role.permissions.join(", ") || "None"}.
              </p>
              {item.group && item.relation ? (
                <p>
                  Group {item.group.name} (<code>{item.group.id}</code>) ·{" "}
                  {item.group.state}. Relation <code>{item.relation.id}</code> ·{" "}
                  {item.relation.state}. An active relation alone does not
                  establish effective access.
                </p>
              ) : (
                <p>Direct member grant.</p>
              )}
              <a
                href={`${props.runtime.basePath}/access-assignments?${item.assignment.groupId ? `groupId=${item.assignment.groupId}` : `membershipId=${props.memberId}`}`}
              >
                {controlIcons["access"]}
                Inspect assignment path
              </a>
            </div>
          ))}
          {page.data.items.length === 0 && (
            <p>No retained contributions on this page.</p>
          )}
        </>
      )}
      <Pager
        label="Member access contributions pagination"
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        busy={page.busy}
        onPage={setCursor}
      />
    </section>
  );
}
