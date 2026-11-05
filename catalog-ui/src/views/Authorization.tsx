import { useCaseNavigation } from "./case-navigation";
import {
  administrationDirectory,
  directorySearch,
} from "./administration-filter-state";
import { commandSummary } from "./workspace";
import { AccessState } from "./operational-labels";
import { controlIcons } from "../shell/Icon";
import {
  AccessReference,
  DirectoryFilters,
  type DirectoryQuery,
  GrantPreview,
  BuiltinRoleHelp,
} from "./AccessControls";
import { identifier } from "../api/values";
import { characterText } from "./input-validation";
// Explicit bounded administration of service-owned local policy. No OAuth mapping or tenant graph cache.
import { useEffect, useRef, useState } from "react";
import type { components } from "../api/schema";
import { result } from "../api/client";
import { acknowledged as commandAcknowledged } from "./audit-handoff";
import { permits, availablePermission } from "../api/permissions";
import { ActionForm, ChoiceField, EvidenceDetails } from "./authoring";
import * as records from "./responses";
import {
  Field,
  Heading,
  Pager,
  ReadState,
  Reason,
  useRead,
  confirmDiscard,
} from "./workspace";
import type { WorkspaceProps } from "./workspace";
import * as check from "./access-responses";

type Role = components["schemas"]["RoleDefinition"];
type Group = components["schemas"]["AccessGroup"];
type Assignment = components["schemas"]["AccessAssignment"];

import { permissionLabels } from "./access-labels";

export default function Authorization(props: WorkspaceProps) {
  const authorized = permits(props.access, "access.manage", "tenant.admin");
  const [admitted, setAdmitted] = useState(authorized);
  if (authorized && !admitted) setAdmitted(true);
  const section = location.pathname
    .slice(props.runtime.basePath.length + 1)
    .split("/")[0];
  return (
    <>
      {(!props.id || props.id === "new" || (!authorized && !admitted)) && (
        <Heading
          title={
            section === "access-roles"
              ? "Roles"
              : section === "access-groups"
                ? "Groups"
                : "Access assignments"
          }
          icon={section === "access-groups" ? "group" : "access"}
          description={
            section === "access-roles"
              ? "Choose permissions for custom roles and inspect built-in roles."
              : section === "access-groups"
                ? "Group members so they can share role assignments."
                : "Assign roles to members or groups for a tenant or namespace."
          }
        />
      )}
      <p className="muted">
        Changes take effect without restarting services or signing in again;
        current backend checks remain authoritative.
      </p>
      {!authorized && (
        <p role="alert" className="notice error">
          You do not have access to access administration. Direct tenant
          administrator access and tenant.admin when scopes are enforced are
          required.
          <a href={`${props.runtime.basePath}/access`}>Review My access</a>
        </p>
      )}
      {(authorized || admitted) &&
        (section === "access-roles" ? (
          <Roles {...props} />
        ) : section === "access-groups" ? (
          <Groups {...props} />
        ) : (
          <Assignments {...props} />
        ))}
    </>
  );
}
function Roles(props: WorkspaceProps) {
  const [cursor, setCursor] = useState("");
  const navigation = useCaseNavigation(
    props,
    "access-roles",
    (search) => {
      setDirectory(administrationDirectory(search));
      setCursor("");
    },
    { preserveDirectoryFilters: true },
  );
  const [directory, setDirectory] = useState<DirectoryQuery>(() =>
    administrationDirectory(navigation.listSearch),
  );
  const [copying, setCopying] = useState<Role>();
  const page = useRead(
    JSON.stringify([directory, cursor]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/access/roles", {
          params: {
            query: {
              limit: 25,
              ...(directory.name ? { name: directory.name } : {}),
              ...(directory.state
                ? { state: directory.state as Role["state"] }
                : {}),
              ...(cursor ? { cursor } : {}),
            },
          },
          signal,
        }),
        check.policyPage(check.role),
      ),
    props.onFailure,
  );
  const detail = useRead(
    props.id,
    async (signal) =>
      props.id && props.id !== "new"
        ? await props.client.api
            .GET("/api/v1/access/roles/{roleId}", {
              params: { path: { roleId: props.id } },
              signal,
            })
            .then((r) => ({
              item: result(r, check.role),
              etag: r.response.headers.get("ETag"),
            }))
        : null,
    props.onFailure,
  );
  if (props.id)
    return (
      <>
        <ReadState {...detail} />
        {props.id === "new" ? (
          <RoleEditor {...props} />
        ) : (
          detail.data && (
            <>
              <Heading
                title={detail.data.item.name}
                icon="access"
                description={
                  detail.data.item.description ||
                  "Inspect this role and its assignments."
                }
              />
              <nav className="workspace-section-nav" aria-label="Role sections">
                <a href={`${props.runtime.basePath}/access-roles`}>
                  Back to roles
                </a>
                <a href="#role-permissions">Permissions</a>
                <a href="#assignment-impact">Assignments</a>
              </nav>
              {copying ? (
                <>
                  <button
                    type="button"
                    onClick={() => {
                      if (confirmDiscard()) setCopying(undefined);
                    }}
                  >
                    {controlIcons["close"]}
                    Cancel role copy
                  </button>
                  <RoleEditor
                    key={`copy-${copying.id}-${copying.revision}`}
                    {...props}
                    source={copying}
                  />
                </>
              ) : (
                <>
                  <button
                    type="button"
                    onClick={() => {
                      if (confirmDiscard()) setCopying(detail.data!.item);
                    }}
                  >
                    {controlIcons["copy"]}
                    Copy to custom role
                  </button>
                  <RoleEditor
                    key={detail.data.item.revision}
                    {...props}
                    item={detail.data.item}
                    etag={detail.data.etag}
                  />
                </>
              )}
              <Assignments
                {...props}
                id=""
                roleId={props.id}
                readOnly={detail.data.item.state === "archived"}
              />
            </>
          )
        )}
      </>
    );
  return (
    <>
      <DirectoryFilters
        applied={directory}
        onApply={(query) => {
          setDirectory(query);
          navigation.updateListSearch(directorySearch(query));
          setCursor("");
        }}
      />
      <div className="workspace-toolbar">
        <a
          className="button primary"
          href={`${props.runtime.basePath}/access-roles/new`}
        >
          {controlIcons["add"]}
          Create role
        </a>
        <button
          onClick={() =>
            confirmDiscard() &&
            (page.error && cursor ? setCursor("") : page.refresh())
          }
        >
          {controlIcons["refresh"]}
          Refresh roles
        </button>
      </div>
      <ReadState
        {...page}
        refresh={() =>
          confirmDiscard() &&
          (page.error && cursor ? setCursor("") : page.refresh())
        }
      />
      {page.data && (
        <section className="workspace-panel">
          {page.data.items.length === 0 && (
            <p className="muted">No roles on this page.</p>
          )}
          {page.data.items.map((r) => (
            <div key={r.id} className="policy-record">
              <a href={`${props.runtime.basePath}/access-roles/${r.id}`}>
                {r.name}
              </a>
              <div className="policy-record-meta">
                <span>{r.builtin ? "Built-in" : "Custom"}</span>
                <AccessState state={r.state} />
                <span>
                  {r.namespaceCapable ? "Namespace or tenant" : "Tenant only"}
                </span>
              </div>
            </div>
          ))}
        </section>
      )}
      <Pager
        label="Role inventory pagination"
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        busy={page.busy}
        onPage={(next) => {
          if (!confirmDiscard()) return false;
          setCursor(next);
        }}
      />
    </>
  );
}
function RoleEditor(
  props: WorkspaceProps & { item?: Role; source?: Role; etag?: string | null },
) {
  const [name, setName] = useState(props.item?.name ?? "");
  const [description, setDescription] = useState(
    props.item?.description ?? props.source?.description ?? "",
  );
  const [permissions, setPermissions] = useState<Role["permissions"]>(
    props.item?.permissions ??
      props.source?.permissions.filter((p) => p !== "access.manage") ??
      [],
  );
  const [reason, setReason] = useState("");
  const [acknowledged, setAcknowledged] = useState(false);
  const protectedCopy =
    props.source?.permissions.includes("access.manage") ?? false;
  const [lifecycleReason, setLifecycleReason] = useState("");
  const [created, setCreated] = useState("");
  const [permissionSearch, setPermissionSearch] = useState("");
  const [selectedOnly, setSelectedOnly] = useState(
    props.item?.builtin === true || props.item?.state === "archived",
  );
  const vocabulary = useRead(
    "permissions",
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/access/permissions", { signal }),
        check.descriptors,
      ),
    props.onFailure,
  );
  const immutable =
    props.item?.builtin === true || props.item?.state === "archived";
  const writable =
    !props.item?.builtin &&
    (!props.item || (props.item.state === "active" && !!props.etag));
  const visiblePermissions = vocabulary.data?.filter((permission) => {
    const query = permissionSearch.trim().toLowerCase();
    const label = permissionLabels[permission.key];
    return (
      (!selectedOnly || permissions.includes(permission.key)) &&
      `${permission.key} ${label}`.toLowerCase().includes(query)
    );
  });
  const missing = [
    ...new Set(
      vocabulary.data
        ?.filter((p) => permissions.includes(p.key))
        .flatMap((p) =>
          p.requires.filter((key) => !permissions.includes(key)),
        ) ?? [],
    ),
  ];
  const permissionPicker = (
    <fieldset className="permission-choices">
      <legend>Permissions</legend>
      <p role="status">
        {permissions.length}{" "}
        {permissions.length === 1 ? "permission" : "permissions"} selected.{" "}
        {permissions.length &&
        vocabulary.data
          ?.filter((p) => permissions.includes(p.key))
          .every((p) => p.namespaceScoped)
          ? "Namespace or tenant eligible."
          : "Tenant-wide only until every selected permission supports namespace scope."}
      </p>
      {missing.length > 0 && (
        <p className="notice">
          Required companions missing: {missing.join(", ")}. Selection is
          explicit; none are added automatically.
        </p>
      )}
      <div className="workspace-toolbar">
        <Field
          label="Find permissions"
          hint="Search action labels or exact permission keys. Filtering does not change the selection."
        >
          <input
            type="search"
            value={permissionSearch}
            onChange={(event) => setPermissionSearch(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") event.preventDefault();
            }}
          />
        </Field>
        <label className="event-choice">
          <input
            type="checkbox"
            checked={selectedOnly}
            onChange={(event) => setSelectedOnly(event.target.checked)}
          />
          Show selected permissions only
        </label>
      </div>
      <p role="status">
        {visiblePermissions?.length ?? 0} matching{" "}
        {visiblePermissions?.length === 1 ? "permission" : "permissions"}.
      </p>
      {visiblePermissions?.length === 0 && (
        <p>
          No permissions match these filters. Clear the search or show all
          permissions.
        </p>
      )}
      {[
        ...new Set(visiblePermissions?.map((p) => p.key.split(".")[0]) ?? []),
      ].map((family) => (
        <fieldset key={family} className="permission-family">
          <legend>{family}</legend>
          <div className="permission-grid">
            {visiblePermissions
              ?.filter((p) => p.key.split(".")[0] === family)
              .map((p) => (
                <label key={p.key} className="event-choice permission-choice">
                  <input
                    type="checkbox"
                    aria-label={p.key}
                    checked={permissions.includes(p.key)}
                    disabled={!writable || !p.delegable}
                    onChange={(e) =>
                      setPermissions((current) =>
                        e.target.checked
                          ? [...current, p.key]
                          : current.filter((v) => v !== p.key),
                      )
                    }
                  />
                  <span>
                    <strong>{permissionLabels[p.key]}</strong>
                    <code>{p.key}</code>
                    <small>
                      {p.namespaceScoped
                        ? "Namespace or tenant"
                        : "Tenant only"}
                      {!p.delegable && " · Not delegable"}
                      {p.requires.length > 0 &&
                        ` · Requires ${p.requires.join(", ")}`}
                    </small>
                  </span>
                </label>
              ))}
          </div>
        </fieldset>
      ))}
    </fieldset>
  );
  if (immutable && props.item)
    return (
      <section className="workspace-panel" id="role-permissions">
        <h2>Role permissions</h2>
        <p>
          {props.item.builtin
            ? "Built-in templates cannot be edited. Copy this role to create a custom permission set."
            : "Archived roles are read-only. Their history remains available."}
        </p>
        <p>
          Role ID: <code>{props.item.id}</code> · {props.item.state} ·{" "}
          {props.item.namespaceCapable ? "Namespace or tenant" : "Tenant only"}
        </p>
        <details className="workspace-advanced">
          <summary>About this role</summary>
          <BuiltinRoleHelp role={props.item} />
        </details>
        <ReadState {...vocabulary} />
        {permissionPicker}
      </section>
    );
  return (
    <>
      {props.source && (
        <section className="notice">
          <p>
            Copy source: {props.source.name} · <code>{props.source.id}</code> ·
            revision {props.source.revision}. This creates a separate custom
            role. Assignments and the original definition remain unchanged.
          </p>
          {protectedCopy && (
            <label>
              <input
                type="checkbox"
                checked={acknowledged}
                onChange={(e) => setAcknowledged(e.target.checked)}
              />
              I acknowledge that protected access.manage is excluded from this
              delegable-only copy.
            </label>
          )}
        </section>
      )}
      {props.item && (
        <p>
          Role definition ID: <code>{props.item.id}</code> /{" "}
          <AccessState state={props.item.state} /> /{" "}
          {props.item.namespaceCapable ? "Namespace or tenant" : "Tenant only"}
        </p>
      )}
      <ReadState {...vocabulary} />
      {props.item && <BuiltinRoleHelp role={props.item} />}
      {props.item?.builtin && (
        <p className="notice">
          Built-in templates are immutable. Create a custom role for a different
          permission set. access.manage cannot be delegated to a custom role or
          group.
        </p>
      )}
      {props.item && immutable && (
        <p>
          <a href={`${props.runtime.basePath}/access-roles/new`}>
            {controlIcons["add"]}
            Create a custom role
          </a>
        </p>
      )}
      <details className="workspace-advanced" open={!props.item}>
        <summary>
          {controlIcons["edit"]}{" "}
          {props.item ? "Edit role" : "Create role details"}
        </summary>
        <ActionForm
          title={props.item ? "Update role" : "Create role"}
          icon={props.item ? "save" : "add"}
          description="Changing a role changes every effective assignment using it. Required companion permissions are explicit; all permissions must support namespace scope for a namespace assignment. Reload after a conflict. Active incompatible bindings must be revoked first."
          editable={permits(props.access, "access.manage", "tenant.admin")}
          allowed={
            permits(props.access, "access.manage", "tenant.admin") &&
            writable &&
            (!protectedCopy || acknowledged) &&
            !!vocabulary.data &&
            permissions.length > 0 &&
            missing.length === 0
          }
          unavailableReason={
            protectedCopy && !acknowledged
              ? "Acknowledge the excluded protected permission before preparing this copy."
              : props.item?.builtin
                ? "Built-in roles cannot change. Create a custom role for different permissions."
                : props.item?.state === "archived"
                  ? "Archived roles cannot change. Create a custom role instead."
                  : !writable
                    ? "Reload the role to obtain its current revision."
                    : vocabulary.busy
                      ? "Loading available permissions."
                      : !vocabulary.data
                        ? "Load the available permissions before preparing a role."
                        : !permissions.length
                          ? "Select at least one permission."
                          : missing.length
                            ? `Select the required companion permissions explicitly: ${missing.join(", ")}.`
                            : undefined
          }
          dirty={
            !immutable &&
            (!!reason ||
              name !== (props.item?.name ?? "") ||
              description !== (props.item?.description ?? "") ||
              JSON.stringify(permissions) !==
                JSON.stringify(props.item?.permissions ?? []))
          }
          onFailure={props.onFailure}
          prepare={() => {
            const body = {
              name: characterText(name.trim(), "Name", 120, 1),
              description: characterText(description, "Description", 2000),
              permissions: [...permissions],
              reason: reason.trim(),
            };
            const id = props.item?.id;
            const etag = props.etag!;
            return {
              summary: commandSummary([
                ["Action", id ? "Update role" : "Create role"],
                ["Role", body.name],
                ["Role ID", id ?? "Assigned on creation"],
                ["Revision", id ? etag : "New role"],
                ["Description", body.description],
                ["Permissions", body.permissions.join(", ")],
                ...(props.source
                  ? ([
                      ["Copy source ID", props.source.id],
                      ["Copy source revision", String(props.source.revision)],
                      [
                        "Excluded protected permissions",
                        protectedCopy ? "access.manage (acknowledged)" : "None",
                      ],
                    ] as const)
                  : ([] as const)),
                ...(props.item
                  ? ([
                      [
                        "Added permissions",
                        body.permissions
                          .filter((p) => !props.item!.permissions.includes(p))
                          .join(", ") || "None",
                      ],
                      [
                        "Removed permissions",
                        props.item.permissions
                          .filter((p) => !body.permissions.includes(p))
                          .join(", ") || "None",
                      ],
                      [
                        "Scope eligibility",
                        `${props.item.namespaceCapable ? "Namespace or tenant" : "Tenant only"} → ${vocabulary.data?.filter((p) => body.permissions.includes(p.key)).every((p) => p.namespaceScoped) ? "Namespace or tenant" : "Tenant only"}`,
                      ],
                    ] as const)
                  : ([] as const)),
                ["Reason", body.reason],
              ]),
              run: async (key) => {
                const reply = id
                  ? await props.client.api.PATCH(
                      "/api/v1/access/roles/{roleId}",
                      {
                        params: {
                          path: { roleId: id },
                          header: { "Idempotency-Key": key, "If-Match": etag },
                        },
                        body,
                      },
                    )
                  : await props.client.api.POST("/api/v1/access/roles", {
                      params: { header: { "Idempotency-Key": key } },
                      body,
                    });
                const item = result(reply, check.role);
                setCreated(item.id);
                return commandAcknowledged(
                  "Role saved. Refresh My access in affected sessions to see current hints.",
                  reply.response,
                  {
                    action: id
                      ? "authorization.role.updated"
                      : "authorization.role.created",
                    targetType: "role_definition",
                    targetId: id ?? item.id,
                    revision: item.revision,
                  },
                );
              },
            };
          }}
        >
          <Field label="Role name">
            <input
              required
              readOnly={immutable}
              maxLength={240}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </Field>
          <Field label="Role description">
            <textarea
              readOnly={immutable}
              maxLength={4000}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </Field>
          {permissionPicker}
          {props.item && (
            <section className="notice" aria-label="Role change impact">
              <h3>Proposed permission changes</h3>
              <p>
                Added:{" "}
                {permissions
                  .filter((p) => !props.item!.permissions.includes(p))
                  .join(", ") || "None"}
                .
              </p>
              <p>
                Removed:{" "}
                {props.item.permissions
                  .filter((p) => !permissions.includes(p))
                  .join(", ") || "None"}
                .
              </p>
              <p>
                Scope:{" "}
                {props.item.namespaceCapable
                  ? "Namespace or tenant"
                  : "Tenant only"}{" "}
                →{" "}
                {permissions.length &&
                vocabulary.data
                  ?.filter((p) => permissions.includes(p.key))
                  .every((p) => p.namespaceScoped)
                  ? "Namespace or tenant"
                  : "Tenant only"}
                .
              </p>
              <p>
                This tenant-global definition affects assignments in every
                realm, including suspended groups. Inspect the bounded
                assignment inventory below before confirming. Paths are not
                distinct people or guaranteed access losses. The preview
                reserves no authority.
              </p>
              <a
                href={`${props.runtime.basePath}/access-assignments?roleDefinitionId=${props.item.id}`}
              >
                Inspect and explicitly clean up role assignments
              </a>
            </section>
          )}
          <Reason
            label="Metadata change reason"
            value={reason}
            onChange={setReason}
            readOnly={immutable}
          />
        </ActionForm>
      </details>
      {created && (
        <a href={`${props.runtime.basePath}/access-roles/${created}`}>
          {controlIcons["arrow"]}
          Open saved role
        </a>
      )}
      {props.item && !props.item.builtin && (
        <details className="workspace-advanced">
          <summary>{controlIcons["remove"]} Archive role</summary>
          <ActionForm
            title="Archive role"
            intent="danger"
            icon="remove"
            resultLink={{
              href: `${props.runtime.basePath}/access-roles/${props.item.id}`,
              label: "Reload role",
            }}
            description="Revoke active assignments first. Archiving retains policy history and is permanent."
            unavailableReason="Archiving requires an active custom role and its current revision. Built-in and archived definitions are read-only."
            editable={permits(props.access, "access.manage", "tenant.admin")}
            allowed={
              writable && permits(props.access, "access.manage", "tenant.admin")
            }
            onFailure={props.onFailure}
            dirty={!!lifecycleReason}
            prepare={() => {
              const body = { reason: lifecycleReason.trim() };
              const id = props.item!.id;
              const etag = props.etag!;
              return {
                summary: commandSummary([
                  ["Action", "Archive role"],
                  ["Role", props.item!.name],
                  ["Role ID", id],
                  ["Revision", etag],
                  ["Reason", body.reason],
                ]),
                run: async (key) => {
                  const reply = await props.client.api.POST(
                    "/api/v1/access/roles/{roleId}:archive",
                    {
                      params: {
                        path: { roleId: id },
                        header: { "Idempotency-Key": key, "If-Match": etag },
                      },
                      body,
                    },
                  );
                  const saved = result(reply, check.role);
                  setLifecycleReason("");
                  return commandAcknowledged(
                    "Role archived. Reload this page.",
                    reply.response,
                    {
                      action: "authorization.role.archived",
                      targetType: "role_definition",
                      targetId: id,
                      revision: saved.revision,
                    },
                  );
                },
              };
            }}
          >
            <Reason
              label="Lifecycle change reason"
              value={lifecycleReason}
              onChange={setLifecycleReason}
              readOnly={immutable}
            />
          </ActionForm>
        </details>
      )}
    </>
  );
}
function Groups(props: WorkspaceProps) {
  const [committedGroup, setCommittedGroup] = useState<Group>();
  const [cursor, setCursor] = useState("");
  const navigation = useCaseNavigation(
    props,
    "access-groups",
    (search) => {
      setDirectory(administrationDirectory(search));
      setCursor("");
    },
    { preserveDirectoryFilters: true },
  );
  const [directory, setDirectory] = useState<DirectoryQuery>(() =>
    administrationDirectory(navigation.listSearch),
  );
  const page = useRead(
    JSON.stringify([directory, cursor]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/access/groups", {
          params: {
            query: {
              limit: 25,
              ...(directory.name ? { name: directory.name } : {}),
              ...(directory.state
                ? { state: directory.state as Group["state"] }
                : {}),
              ...(cursor ? { cursor } : {}),
            },
          },
          signal,
        }),
        check.policyPage(check.group),
      ),
    props.onFailure,
  );
  const detail = useRead(
    props.id,
    async (signal) =>
      props.id && props.id !== "new"
        ? await props.client.api
            .GET("/api/v1/access/groups/{groupId}", {
              params: { path: { groupId: props.id } },
              signal,
            })
            .then((r) => ({
              item: result(r, check.group),
              etag: r.response.headers.get("ETag"),
            }))
        : null,
    props.onFailure,
  );
  const currentGroup =
    committedGroup &&
    detail.data &&
    committedGroup.id === detail.data.item.id &&
    committedGroup.revision >= detail.data.item.revision
      ? committedGroup
      : detail.data?.item;
  if (props.id)
    return (
      <>
        <ReadState {...detail} />
        {props.id === "new" ? (
          <GroupEditor {...props} onLifecycle={setCommittedGroup} />
        ) : (
          detail.data && (
            <>
              <Heading
                title={currentGroup!.name}
                icon="group"
                description={
                  currentGroup!.description ||
                  "Inspect this group and its access paths."
                }
              />
              <nav
                className="workspace-section-nav"
                aria-label="Group sections"
              >
                <a href={`${props.runtime.basePath}/access-groups`}>
                  Back to groups
                </a>
                <a href="#group-overview">Overview</a>
                <a href="#group-member-impact">Members</a>
                <a href="#assignment-impact">Assignments</a>
              </nav>
              <GroupEditor
                key={detail.data.item.revision}
                {...props}
                item={detail.data.item}
                etag={detail.data.etag}
                onLifecycle={setCommittedGroup}
              />
              <GroupMembers
                key={`members-${currentGroup!.id}`}
                {...props}
                group={currentGroup!}
              />
              <Assignments
                key={`assignments-${currentGroup!.id}`}
                {...props}
                id=""
                groupId={props.id}
                ownerRevision={currentGroup!.revision}
                readOnly={currentGroup!.state === "archived"}
                grantUnavailable={currentGroup!.state !== "active"}
              />
            </>
          )
        )}
      </>
    );
  return (
    <>
      <DirectoryFilters
        applied={directory}
        groups
        onApply={(query) => {
          setDirectory(query);
          navigation.updateListSearch(directorySearch(query));
          setCursor("");
        }}
      />
      <div className="workspace-toolbar">
        <a
          className="button primary"
          href={`${props.runtime.basePath}/access-groups/new`}
        >
          {controlIcons["add"]}
          Create group
        </a>
        <button
          onClick={() =>
            confirmDiscard() &&
            (page.error && cursor ? setCursor("") : page.refresh())
          }
        >
          {controlIcons["refresh"]}
          Refresh groups
        </button>
      </div>
      <ReadState
        {...page}
        refresh={() =>
          confirmDiscard() &&
          (page.error && cursor ? setCursor("") : page.refresh())
        }
      />
      {page.data && (
        <section className="workspace-panel">
          {page.data.items.length === 0 && (
            <p className="muted">No groups on this page.</p>
          )}
          {page.data.items.map((g) => (
            <div key={g.id} className="policy-record">
              <a href={`${props.runtime.basePath}/access-groups/${g.id}`}>
                {g.name}
              </a>
              <div className="policy-record-meta">
                <AccessState state={g.state} />
              </div>
            </div>
          ))}
        </section>
      )}
      <Pager
        label="Group inventory pagination"
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        busy={page.busy}
        onPage={(next) => {
          if (!confirmDiscard()) return false;
          setCursor(next);
        }}
      />
    </>
  );
}
function GroupEditor(
  props: WorkspaceProps & {
    item?: Group;
    etag?: string | null;
    onLifecycle: (item: Group) => void;
  },
) {
  const [committed, setCommitted] = useState<Group>();
  const [name, setName] = useState(props.item?.name ?? "");
  const [description, setDescription] = useState(props.item?.description ?? "");
  const [reason, setReason] = useState("");
  const [lifecycleReason, setLifecycleReason] = useState("");
  const [created, setCreated] = useState("");
  const [action, setAction] = useState<"suspend" | "reactivate" | "archive">(
    props.item?.state === "suspended" ? "reactivate" : "suspend",
  );
  const immutable =
    props.item?.state === "archived" || committed?.state === "archived";
  const writable =
    !committed &&
    (!props.item || (props.item.state !== "archived" && !!props.etag));
  if (props.item?.state === "archived" && !committed)
    return (
      <section className="workspace-panel" id="group-overview">
        <h2>Group overview</h2>
        <p>
          Archived groups are read-only. Retained member relations and
          assignments do not grant access through this group.
        </p>
        <p>
          Group ID: <code>{props.item.id}</code> · Realm:{" "}
          <code>{props.item.realmId}</code>
        </p>
        <EvidenceDetails
          title="Group details"
          value={committed ?? props.item}
        />
      </section>
    );
  return (
    <>
      {props.item && (
        <section className="workspace-panel" id="group-overview">
          <h2>Group overview</h2>
          <p>
            Group ID: <code>{props.item.id}</code> /{" "}
            <AccessState state={committed?.state ?? props.item.state} /> /
            Realm: <code>{props.item.realmId}</code>
          </p>
        </section>
      )}
      {props.item && (
        <p className="notice">
          Before a lifecycle command, inspect the current bounded{" "}
          <a href="#group-member-impact">group member inventory</a> and{" "}
          <a href="#assignment-impact">assignment paths</a> below. Pages and
          paths are not distinct-person counts or guaranteed access losses.
          Removing one member affects that relation; revoking one group
          assignment affects its role path. Suspending the group preserves paths
          for explicit reactivation. Independent direct and other-group grants
          can remain. Archival is terminal and requires explicit member and
          assignment cleanup. A preview reserves no authority.
        </p>
      )}
      <details className="workspace-advanced" open={!props.item}>
        <summary>
          {controlIcons["edit"]}{" "}
          {props.item ? "Edit group" : "Create group details"}
        </summary>
        <ActionForm
          title={props.item ? "Update group" : "Create group"}
          icon={props.item ? "save" : "add"}
          description="Groups contain direct members in one identity realm; nesting and upstream group mappings are not supported."
          unavailableReason={
            immutable
              ? "Archived groups cannot change. Their history remains available."
              : "Reload the group to obtain its current revision."
          }
          editable={
            !immutable && permits(props.access, "access.manage", "tenant.admin")
          }
          allowed={
            writable && permits(props.access, "access.manage", "tenant.admin")
          }
          dirty={
            !!reason ||
            name !== (props.item?.name ?? "") ||
            description !== (props.item?.description ?? "")
          }
          onFailure={props.onFailure}
          prepare={() => {
            const body = {
              name: characterText(name.trim(), "Name", 120, 1),
              description: characterText(description, "Description", 2000),
              reason: reason.trim(),
            };
            const id = props.item?.id;
            const etag = props.etag!;
            return {
              summary: commandSummary([
                ["Action", id ? "Update group" : "Create group"],
                ["Group", body.name],
                ["Group ID", id ?? "Assigned on creation"],
                ["Revision", id ? etag : "New group"],
                ["Description", body.description],
                ["Reason", body.reason],
              ]),
              run: async (key) => {
                const reply = id
                  ? await props.client.api.PATCH(
                      "/api/v1/access/groups/{groupId}",
                      {
                        params: {
                          path: { groupId: id },
                          header: { "Idempotency-Key": key, "If-Match": etag },
                        },
                        body,
                      },
                    )
                  : await props.client.api.POST("/api/v1/access/groups", {
                      params: { header: { "Idempotency-Key": key } },
                      body,
                    });
                const item = result(reply, check.group);
                setCreated(item.id);
                return commandAcknowledged("Group saved.", reply.response, {
                  action: id
                    ? "authorization.group.updated"
                    : "authorization.group.created",
                  targetType: "access_group",
                  targetId: id ?? item.id,
                  revision: item.revision,
                });
              },
            };
          }}
        >
          <Field label="Group name">
            <input
              required
              readOnly={immutable || !!committed}
              maxLength={240}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </Field>
          <Field label="Group description">
            <textarea
              readOnly={immutable || !!committed}
              maxLength={4000}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </Field>
          <Reason
            label="Metadata change reason"
            value={reason}
            onChange={setReason}
            readOnly={immutable || !!committed}
          />
        </ActionForm>
      </details>
      {created && (
        <a href={`${props.runtime.basePath}/access-groups/${created}`}>
          {controlIcons["arrow"]}
          Open saved group
        </a>
      )}
      {props.item && (
        <details className="workspace-advanced">
          <summary>Change group state</summary>
          <ActionForm
            title="Change group state"
            intent={
              action === "archive"
                ? "danger"
                : action === "suspend"
                  ? "warning"
                  : "primary"
            }
            icon={
              action === "archive"
                ? "remove"
                : action === "suspend"
                  ? "pause"
                  : "play"
            }
            resultLink={{
              href: `${props.runtime.basePath}/access-groups/${props.item.id}`,
              label: "Reload group",
            }}
            description="Suspension removes only this group's access paths; direct and other group grants may remain. Reactivation restores eligible paths. Archive requires all active members and assignments to be revoked first and is permanent."
            editable={
              !immutable &&
              permits(props.access, "access.manage", "tenant.admin")
            }
            allowed={
              writable &&
              (action === "archive" ||
                (action === "suspend" && props.item.state === "active") ||
                (action === "reactivate" && props.item.state === "suspended"))
            }
            unavailableReason={
              immutable
                ? "Archived groups cannot change state."
                : !props.etag
                  ? "Reload the group to obtain its current revision."
                  : "Choose a change supported by this group's current state."
            }
            onFailure={props.onFailure}
            dirty={!!lifecycleReason}
            prepare={() => {
              const body = { reason: lifecycleReason.trim() };
              const id = props.item!.id;
              const etag = props.etag!;
              const path =
                action === "suspend"
                  ? "/api/v1/access/groups/{groupId}:suspend"
                  : action === "reactivate"
                    ? "/api/v1/access/groups/{groupId}:reactivate"
                    : "/api/v1/access/groups/{groupId}:archive";
              return {
                summary: commandSummary([
                  [
                    "Action",
                    action === "suspend"
                      ? "Suspend group"
                      : action === "reactivate"
                        ? "Reactivate group"
                        : "Archive group",
                  ],
                  ["Group", props.item!.name],
                  ["Group ID", id],
                  ["Revision", etag],
                  ["Reason", body.reason],
                ]),
                run: async (key) => {
                  const reply = await props.client.api.POST(path, {
                    params: {
                      path: { groupId: id },
                      header: { "Idempotency-Key": key, "If-Match": etag },
                    },
                    body,
                  });
                  const changed = result(reply, check.group);
                  if (
                    changed.id !== id ||
                    changed.tenantId !== props.item!.tenantId ||
                    changed.realmId !== props.item!.realmId
                  )
                    throw new Error(
                      "Group lifecycle result does not match this group.",
                    );
                  setCommitted(changed);
                  props.onLifecycle(changed);
                  setLifecycleReason("");
                  return commandAcknowledged(
                    "Group state changed. Reload this page.",
                    reply.response,
                    {
                      action:
                        action === "suspend"
                          ? "authorization.group.suspended"
                          : action === "reactivate"
                            ? "authorization.group.reactivated"
                            : "authorization.group.archived",
                      targetType: "access_group",
                      targetId: id,
                      revision: changed.revision,
                    },
                  );
                },
              };
            }}
          >
            <Field label="Group action">
              <select
                disabled={immutable || !!committed}
                value={action}
                onChange={(e) => setAction(e.target.value as typeof action)}
              >
                <option value="suspend">Suspend</option>
                <option value="reactivate">Reactivate</option>
                <option value="archive">Archive</option>
              </select>
            </Field>
            <Reason
              label="Lifecycle change reason"
              value={lifecycleReason}
              onChange={setLifecycleReason}
              readOnly={immutable || !!committed}
            />
          </ActionForm>
        </details>
      )}
    </>
  );
}
function GroupMembers(props: WorkspaceProps & { group: Group }) {
  const [cursor, setCursor] = useState("");
  const [groupRevision, setGroupRevision] = useState(props.group.revision);
  if (groupRevision !== props.group.revision) {
    setGroupRevision(props.group.revision);
    setCursor("");
  }
  const [member, setMember] = useState("");
  const [reason, setReason] = useState("");
  const [selected, setSelected] =
    useState<components["schemas"]["AccessGroupMember"]>();
  const editor = useRef<HTMLElement>(null);
  const opener = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (selected) editor.current?.focus();
  }, [selected]);
  const page = useRead(
    JSON.stringify([props.group.id, groupRevision, cursor]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/access/groups/{groupId}/members", {
          params: {
            path: { groupId: props.group.id },
            query: { limit: 25, ...(cursor ? { cursor } : {}) },
          },
          signal,
        }),
        check.policyPage(check.groupMember),
      ),
    props.onFailure,
  );
  return (
    <>
      <ReadState
        {...page}
        refresh={() => {
          if (!confirmDiscard()) return;
          setSelected(undefined);
          if (page.error && cursor) setCursor("");
          else page.refresh();
        }}
      />
      {page.data && (
        <section className="workspace-panel" id="group-member-impact">
          <h2>Direct group members</h2>
          {props.group.state === "archived" && (
            <p className="notice">
              These retained relations are historical and ineffective. An active
              relation state grants no access through an archived group.
            </p>
          )}
          {page.data.items.map((m) => (
            <div className="policy-summary" key={m.id}>
              <p>
                <a href={`${props.runtime.basePath}/members/${m.membershipId}`}>
                  <AccessReference
                    workspace={props}
                    kind="member"
                    compact
                    id={m.membershipId}
                    refreshToken={m}
                  />
                </a>{" "}
                <AccessState state={m.state} />
              </p>
              <p>
                Relation <code>{m.id}</code>. Relation state alone does not
                establish access; suspended or offboarded members and suspended
                or archived groups are ineffective.
              </p>
              <EvidenceDetails title="Group membership attribution" value={m} />
              {m.state === "active" && props.group.state !== "archived" && (
                <button
                  type="button"
                  onClick={(event) => {
                    if (!confirmDiscard()) return;
                    opener.current = event.currentTarget;
                    setSelected(m);
                    if (selected?.id === m.id) editor.current?.focus();
                  }}
                >
                  {controlIcons["edit"]}
                  Manage membership {m.membershipId}
                </button>
              )}
            </div>
          ))}
        </section>
      )}
      <Pager
        label="Group members pagination"
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        busy={page.busy}
        onPage={(next) => {
          if (!confirmDiscard()) return false;
          setSelected(undefined);
          setCursor(next);
        }}
      />
      {selected && (
        <section
          aria-label="Selected group membership editor"
          tabIndex={-1}
          ref={editor}
        >
          <button
            type="button"
            onClick={() => {
              if (!confirmDiscard()) return;
              setSelected(undefined);
              opener.current?.focus();
            }}
          >
            {controlIcons["close"]}
            Close membership editor
          </button>
          <GroupMemberRevoke key={selected.id} {...props} member={selected} />
        </section>
      )}
      <details className="workspace-advanced">
        <summary>{controlIcons["add"]} Add group member</summary>
        <ActionForm
          title="Add group member"
          icon="add"
          another={{
            label: "Add another group member",
            reset: () => {
              setMember("");
              setReason("");
            },
          }}
          description="Use an existing active membership ID in this group's realm. A group member receives only permissions from the group's active assignments."
          editable={
            props.group.state !== "archived" &&
            permits(props.access, "access.manage", "tenant.admin")
          }
          allowed={
            props.group.state === "active" &&
            permits(props.access, "access.manage", "tenant.admin")
          }
          unavailableReason="Adding members requires an active group. Reactivate a suspended group first; archived groups cannot accept members."
          dirty={!!member || !!reason}
          onFailure={props.onFailure}
          prepare={() => {
            const body = { membershipId: member.trim(), reason: reason.trim() };
            const groupId = props.group.id;
            return {
              summary: commandSummary([
                ["Action", "Add group member"],
                ["Group", props.group.name],
                ["Group ID", groupId],
                ["Membership ID", body.membershipId],
                ["Reason", body.reason],
              ]),
              run: async (key) => {
                const reply = await props.client.api.POST(
                  "/api/v1/access/groups/{groupId}/members",
                  {
                    params: {
                      path: { groupId },
                      header: { "Idempotency-Key": key },
                    },
                    body,
                  },
                );
                const saved = result(reply, check.groupMember);
                page.refresh();
                return commandAcknowledged(
                  "Group member added.",
                  reply.response,
                  {
                    action: "authorization.group_member.added",
                    targetType: "group_member",
                    targetId: saved.id,
                    revision: saved.revision,
                  },
                );
              },
            };
          }}
        >
          <fieldset
            className="workspace-fields"
            disabled={props.group.state !== "active"}
          >
            <ChoiceField
              label="Member"
              pagerLabel="Add-member choices pagination"
              searchLabel="Exact member subject"
              manualLabel="Membership ID"
              value={member}
              onChange={setMember}
              onFailure={props.onFailure}
              queryKey={props.group.realmId}
              load={async (cursor, signal, search) => {
                const page = result(
                  await props.client.api.GET("/api/v1/memberships", {
                    params: {
                      query: {
                        limit: 25,
                        state: "active",
                        realmId: props.group.realmId,
                        ...(search ? { subject: search } : {}),
                        ...(cursor ? { cursor } : {}),
                      },
                    },
                    signal,
                  }),
                  records.MembershipPage,
                );
                return {
                  ...page,
                  items: page.items
                    .filter(
                      (m) =>
                        m.state === "active" &&
                        m.tenantId === props.access.tenantId &&
                        m.realmId === props.group.realmId,
                    )
                    .map((m) => ({
                      id: m.id,
                      label: `${m.subject} · ${m.actorType} · ${m.id}`,
                    })),
                };
              }}
            />
            <p>
              Only active members in this group's realm are listed. Tenant:{" "}
              <code>{props.access.tenantId}</code>; group realm:{" "}
              <code>{props.group.realmId}</code>.
            </p>
            <Reason value={reason} onChange={setReason} />
          </fieldset>
        </ActionForm>
      </details>
    </>
  );
}
function GroupMemberRevoke(
  props: WorkspaceProps & {
    group: Group;
    member: components["schemas"]["AccessGroupMember"];
  },
) {
  const [reason, setReason] = useState("");
  const m = props.member;
  const writable = m.state === "active" && props.group.state !== "archived";
  return (
    <ActionForm
      title="Revoke group member"
      intent="danger"
      icon="remove"
      description={`Remove only member ${m.membershipId} from group ${props.group.name}. Other access paths remain effective.`}
      editable={
        writable && permits(props.access, "access.manage", "tenant.admin")
      }
      allowed={
        writable && permits(props.access, "access.manage", "tenant.admin")
      }
      unavailableReason={
        props.group.state === "archived"
          ? "This archived group is terminal. Retained membership relations are historical and grant no access."
          : "This membership has already been revoked."
      }
      dirty={!!reason}
      onFailure={props.onFailure}
      prepare={() => {
        const body = { reason: reason.trim() };
        const groupId = props.group.id;
        const memberId = m.id;
        const etag = `"rev-${m.revision}"`;
        return {
          summary: commandSummary([
            ["Action", "Revoke group member"],
            ["Group", props.group.name],
            ["Group ID", groupId],
            ["Membership ID", m.membershipId],
            ["Group membership ID", memberId],
            ["Revision", etag],
            ["Reason", body.reason],
          ]),
          run: async (key) => {
            const reply = await props.client.api.POST(
              "/api/v1/access/groups/{groupId}/members/{memberId}:revoke",
              {
                params: {
                  path: { groupId, memberId },
                  header: { "Idempotency-Key": key, "If-Match": etag },
                },
                body,
              },
            );
            const saved = result(reply, check.groupMember);
            return commandAcknowledged(
              "Group membership revoked. Refresh the page.",
              reply.response,
              {
                action: "authorization.group_member.revoked",
                targetType: "group_member",
                targetId: memberId,
                revision: saved.revision,
              },
            );
          },
        };
      }}
    >
      <Reason readOnly={!writable} value={reason} onChange={setReason} />
    </ActionForm>
  );
}

function Assignments(
  props: WorkspaceProps & {
    roleId?: string;
    groupId?: string;
    readOnly?: boolean;
    grantUnavailable?: boolean;
    ownerRevision?: number;
  },
) {
  const filters = new URLSearchParams(location.search);
  const [appliedFilters, setAppliedFilters] = useState({
    roleDefinitionId: filters.get("roleDefinitionId") ?? "",
    groupId: filters.get("groupId") ?? "",
    membershipId: filters.get("membershipId") ?? "",
  });
  const [draftFilters, setDraftFilters] = useState(appliedFilters);
  const filterRoleId = props.roleId ?? appliedFilters.roleDefinitionId;
  const filterGroupId = props.groupId ?? appliedFilters.groupId;
  const membershipId = appliedFilters.membershipId || undefined;
  const grantBase = useRef({
    roleId: filterRoleId,
    targetId: filterGroupId || membershipId || "",
    target: filterGroupId ? "groupId" : "membershipId",
  });
  const [cursor, setCursor] = useState("");
  const [ownerRevision, setOwnerRevision] = useState(props.ownerRevision);
  if (ownerRevision !== props.ownerRevision) {
    setOwnerRevision(props.ownerRevision);
    setCursor("");
  }
  const [roleId, setRole] = useState(filterRoleId);
  const [target, setTarget] = useState<"membershipId" | "groupId">(
    filterGroupId ? "groupId" : "membershipId",
  );
  const [targetId, setTargetId] = useState(filterGroupId || membershipId || "");
  const [namespaceId, setNamespace] = useState("");
  const [scope, setScope] = useState("");
  const [roleChoices, setRoleChoices] = useState<Role[]>([]);
  const selectedRole = useRead(
    JSON.stringify([roleId, props.access.authorizationRevision]),
    async (signal) => {
      if (!identifier(roleId)) return null;
      const value = result(
        await props.client.api.GET("/api/v1/access/roles/{roleId}", {
          params: { path: { roleId } },
          signal,
        }),
        check.role,
      );
      if (value.id !== roleId || value.tenantId !== props.access.tenantId)
        throw new Error("Role details do not match the selected reference.");
      return value;
    },
    props.onFailure,
  );
  const chosenRole =
    (selectedRole.data?.id === roleId ? selectedRole.data : null) ??
    roleChoices.find((r) => r.id === roleId);
  const [selected, setSelected] = useState<Assignment>();
  const editor = useRef<HTMLElement>(null);
  const opener = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (selected) editor.current?.focus();
  }, [selected]);
  const [reason, setReason] = useState("");
  const page = useRead(
    JSON.stringify([
      filterRoleId,
      filterGroupId,
      membershipId,
      cursor,
      ownerRevision,
    ]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/access/assignments", {
          params: {
            query: {
              limit: 25,
              ...(cursor ? { cursor } : {}),
              ...(filterRoleId ? { roleDefinitionId: filterRoleId } : {}),
              ...(filterGroupId ? { groupId: filterGroupId } : {}),
              ...(membershipId ? { membershipId } : {}),
            },
          },
          signal,
        }),
        check.policyPage(check.assignment),
      ),
    props.onFailure,
  );
  return (
    <>
      {!props.roleId && !props.groupId && (
        <details
          className="workspace-advanced"
          open={!!(filterRoleId || filterGroupId || membershipId)}
        >
          <summary>Filter assignments</summary>
          <form
            className="workspace-toolbar"
            onSubmit={(event) => {
              event.preventDefault();
              if (!confirmDiscard()) return;
              setAppliedFilters({
                roleDefinitionId: draftFilters.roleDefinitionId.trim(),
                membershipId: draftFilters.membershipId.trim(),
                groupId: draftFilters.groupId.trim(),
              });
              setCursor("");
              setSelected(undefined);
            }}
          >
            <Field
              label="Role ID filter"
              hint="Optional exact role definition ID."
            >
              <input
                value={draftFilters.roleDefinitionId}
                onChange={(event) =>
                  setDraftFilters({
                    ...draftFilters,
                    roleDefinitionId: event.target.value,
                  })
                }
              />
            </Field>
            <Field
              label="Member ID filter"
              hint="Direct member grants only; group paths are separate."
            >
              <input
                value={draftFilters.membershipId}
                onChange={(event) =>
                  setDraftFilters({
                    ...draftFilters,
                    membershipId: event.target.value,
                  })
                }
              />
            </Field>
            <Field
              label="Group ID filter"
              hint="Optional exact group ID. All selected filters must match."
            >
              <input
                value={draftFilters.groupId}
                onChange={(event) =>
                  setDraftFilters({
                    ...draftFilters,
                    groupId: event.target.value,
                  })
                }
              />
            </Field>
            <div className="workspace-actions">
              <button type="submit">
                {controlIcons["filters"]} Apply filters
              </button>
              <button
                type="button"
                onClick={() => {
                  if (!confirmDiscard()) return;
                  const empty = {
                    roleDefinitionId: "",
                    membershipId: "",
                    groupId: "",
                  };
                  setDraftFilters(empty);
                  setAppliedFilters(empty);
                  setCursor("");
                  setSelected(undefined);
                }}
              >
                {controlIcons["close"]}
                Clear filters
              </button>
            </div>
          </form>
          {JSON.stringify(draftFilters) !== JSON.stringify(appliedFilters) && (
            <p role="status">
              Filter edits are pending. Apply filters to update the inventory.
            </p>
          )}
        </details>
      )}
      <details className="workspace-advanced">
        <summary>How assignment scopes work</summary>
        <p>
          Tenant assignments apply everywhere. Namespace assignments apply only
          there and require a namespace-capable role. Revoking one assignment
          does not remove overlapping grants. Protected tenant administrator
          assignments cannot be granted to groups; the last administrator is
          protected.
        </p>
      </details>
      {(filterRoleId || filterGroupId || membershipId) && (
        <p className="notice">
          Showing assignments for:{" "}
          {filterRoleId && (
            <AccessReference workspace={props} kind="role" id={filterRoleId} />
          )}{" "}
          {filterGroupId && (
            <AccessReference
              workspace={props}
              kind="group"
              id={filterGroupId}
            />
          )}{" "}
          {membershipId && (
            <>
              <AccessReference
                workspace={props}
                kind="member"
                id={membershipId}
              />{" "}
              (direct grants only; group contributions are separate)
            </>
          )}{" "}
          <a href={`${props.runtime.basePath}/access-assignments`}>
            All assignments
          </a>
        </p>
      )}
      <button
        onClick={() => {
          if (!confirmDiscard()) return;
          setSelected(undefined);
          if (page.error && cursor) setCursor("");
          else page.refresh();
        }}
      >
        {controlIcons["refresh"]}
        Refresh assignments
      </button>
      <ReadState
        {...page}
        refresh={() => {
          if (!confirmDiscard()) return;
          setSelected(undefined);
          if (page.error && cursor) setCursor("");
          else page.refresh();
        }}
      />
      {page.data && (
        <section className="workspace-panel" id="assignment-impact">
          <h2>Assignments</h2>
          <p>
            Current policy revision: {page.data.authorizationRevision}. This
            page is a bounded assignment inventory, not a distinct-user count or
            complete access-loss forecast.
          </p>
          {page.data.items.length === 0 && (
            <p className="muted">No assignments on this page.</p>
          )}
          {page.data.items.map((a) => (
            <div
              key={a.id}
              className="policy-summary"
              data-assignment-id={a.id}
            >
              <p className="assignment-links">
                <a
                  href={`${props.runtime.basePath}/access-roles/${a.roleDefinitionId}`}
                >
                  <AccessReference
                    workspace={props}
                    compact
                    kind="role"
                    id={a.roleDefinitionId}
                    refreshToken={a}
                  />
                </a>{" "}
                →{" "}
                <a
                  href={`${props.runtime.basePath}/${a.membershipId ? "members" : "access-groups"}/${a.membershipId ?? a.groupId}`}
                >
                  <AccessReference
                    workspace={props}
                    compact
                    kind={a.membershipId ? "member" : "group"}
                    id={(a.membershipId ?? a.groupId)!}
                    refreshToken={a}
                    realmId={a.realmId}
                  />
                </a>{" "}
                /{" "}
                {a.namespaceId ? (
                  <a
                    href={`${props.runtime.basePath}/namespaces/${a.namespaceId}`}
                  >
                    <AccessReference
                      workspace={props}
                      compact
                      kind="namespace"
                      id={a.namespaceId}
                      refreshToken={a}
                    />
                  </a>
                ) : (
                  "Tenant-wide"
                )}{" "}
                <AccessState state={a.state} />
              </p>
              <div className="policy-record-meta">
                Assignment <code>{a.id}</code>
              </div>
              {a.realmId !== props.access.realmId && (
                <p className="notice">
                  Read-only in realm <code>{a.realmId}</code>. Revocation
                  requires an administrator session in that realm.
                </p>
              )}
              <EvidenceDetails title="Assignment attribution" value={a} />
              {a.state === "active" && !props.readOnly && (
                <button
                  aria-label={`Manage assignment ${a.id}`}
                  type="button"
                  onClick={(event) => {
                    if (!confirmDiscard()) return;
                    opener.current = event.currentTarget;
                    setSelected(a);
                    if (selected?.id === a.id) editor.current?.focus();
                  }}
                >
                  {controlIcons["edit"]}
                  Manage assignment
                </button>
              )}
            </div>
          ))}
        </section>
      )}
      <Pager
        label="Assignment inventory pagination"
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        busy={page.busy}
        onPage={(next) => {
          if (!confirmDiscard()) return false;
          setSelected(undefined);
          setCursor(next);
        }}
      />
      {selected && (
        <section
          aria-label="Selected access assignment editor"
          tabIndex={-1}
          ref={editor}
        >
          <button
            type="button"
            onClick={() => {
              if (!confirmDiscard()) return;
              setSelected(undefined);
              opener.current?.focus();
            }}
          >
            {controlIcons["close"]}
            Close assignment editor
          </button>
          <AssignmentRow key={selected.id} {...props} assignment={selected} />
        </section>
      )}
      <details className="workspace-advanced">
        <summary>{controlIcons["add"]} Grant access</summary>
        <ActionForm
          title="Grant access assignment"
          icon="add"
          another={{
            label: "Grant another access assignment",
            reset: () => {
              setTargetId("");
              setReason("");
            },
          }}
          description="Assign an active role to an existing active member or same-realm group. Use Members for a Catalog membership of an existing issuer identity; this operation does not create an issuer account or issue a token."
          editable={
            !props.readOnly &&
            permits(props.access, "access.manage", "tenant.admin")
          }
          allowed={
            permits(props.access, "access.manage", "tenant.admin") &&
            !props.readOnly &&
            !props.grantUnavailable &&
            !!scope &&
            (!chosenRole || chosenRole.state === "active") &&
            !(
              scope === "namespace" &&
              chosenRole &&
              !chosenRole.namespaceCapable
            ) &&
            !(
              target === "groupId" &&
              chosenRole?.permissions.includes("access.manage")
            )
          }
          unavailableReason={
            props.readOnly
              ? "This archived record cannot receive new assignments."
              : props.grantUnavailable
                ? "Reactivate this group before granting access."
                : !scope
                  ? "Choose an assignment scope."
                  : "This role is tenant-only or cannot be assigned to a group. Choose a compatible role, target and scope explicitly."
          }
          dirty={
            scope !== "" ||
            roleId !== grantBase.current.roleId ||
            targetId !== grantBase.current.targetId ||
            target !== grantBase.current.target ||
            !!namespaceId ||
            !!reason
          }
          onFailure={props.onFailure}
          prepare={() => {
            const body = {
              roleDefinitionId: roleId.trim(),
              ...(target === "membershipId"
                ? { membershipId: targetId.trim() }
                : { groupId: targetId.trim() }),
              ...(scope === "namespace"
                ? { namespaceId: namespaceId.trim() }
                : {}),
              reason: reason.trim(),
            };
            return {
              summary: commandSummary([
                ["Action", "Grant access"],
                ["Role", chosenRole?.name ?? "Exact role ID supplied"],
                ["Role ID", body.roleDefinitionId],
                [
                  "Role permissions",
                  chosenRole
                    ? chosenRole.permissions.join(", ") ||
                      "No product permissions"
                    : "Unverified; API validates the exact role ID",
                ],
                [
                  "Membership ID",
                  "membershipId" in body
                    ? body.membershipId
                    : "None — group assignment",
                ],
                [
                  "Group ID",
                  "groupId" in body ? body.groupId : "None — direct assignment",
                ],
                ["Scope", body.namespaceId ? "Exact namespace" : "Tenant-wide"],
                ["Namespace ID", body.namespaceId ?? "Not namespace-scoped"],
                ["Reason", body.reason],
              ]),
              run: async (key) => {
                const reply = await props.client.api.POST(
                  "/api/v1/access/assignments",
                  {
                    params: { header: { "Idempotency-Key": key } },
                    body,
                  },
                );
                const saved = result(reply, check.assignment);
                page.refresh();
                return commandAcknowledged(
                  "Access assigned. Refresh My access in the affected session.",
                  reply.response,
                  {
                    action: "authorization.assignment.granted",
                    targetType: "role_assignment",
                    targetId: saved.id,
                    revision: saved.revision,
                  },
                );
              },
            };
          }}
        >
          <ChoiceField
            label="Role"
            searchLabel="Exact role name"
            manualLabel="Role definition ID"
            value={roleId}
            onChange={(id) => {
              setRole(id);
            }}
            onFailure={props.onFailure}
            load={async (cursor, signal, search) => {
              const page = result(
                await props.client.api.GET("/api/v1/access/roles", {
                  params: {
                    query: {
                      limit: 25,
                      state: "active",
                      ...(search ? { name: search } : {}),
                      ...(cursor ? { cursor } : {}),
                    },
                  },
                  signal,
                }),
                check.policyPage(check.role),
              );
              const items = page.items.filter(
                (r) =>
                  r.state === "active" && r.tenantId === props.access.tenantId,
              );
              if (!signal.aborted)
                setRoleChoices((known) => [
                  ...known.filter(
                    (r) => r.id === roleId && !items.some((i) => i.id === r.id),
                  ),
                  ...items,
                ]);
              return {
                ...page,
                items: items.map((r) => ({
                  id: r.id,
                  label: `${r.name} · ${r.namespaceCapable ? "Namespace or tenant" : "Tenant only"} · ${r.id}`,
                })),
              };
            }}
          />
          <p>
            {chosenRole
              ? `${chosenRole.name}: ${chosenRole.namespaceCapable ? "Namespace or tenant eligible" : "Tenant only"}`
              : "Manual or preset role reference: scope eligibility has not been verified here."}
          </p>
          <Field label="Assignment target">
            <select
              value={target}
              onChange={(e) => {
                setTarget(e.target.value as typeof target);
                setTargetId("");
              }}
            >
              <option value="membershipId">Member</option>
              <option value="groupId">Group</option>
            </select>
          </Field>
          <ChoiceField
            label={target === "membershipId" ? "Member" : "Group"}
            searchLabel={
              target === "membershipId"
                ? "Exact member subject"
                : "Exact group name"
            }
            manualLabel="Member or group ID"
            value={targetId}
            onChange={setTargetId}
            onFailure={props.onFailure}
            queryKey={target}
            load={async (cursor, signal, search) => {
              if (target === "membershipId") {
                const page = result(
                  await props.client.api.GET("/api/v1/memberships", {
                    params: {
                      query: {
                        limit: 25,
                        state: "active",
                        realmId: props.access.realmId,
                        ...(search ? { subject: search } : {}),
                        ...(cursor ? { cursor } : {}),
                      },
                    },
                    signal,
                  }),
                  records.MembershipPage,
                );
                return {
                  ...page,
                  items: page.items
                    .filter(
                      (m) =>
                        m.state === "active" &&
                        m.tenantId === props.access.tenantId &&
                        m.realmId === props.access.realmId,
                    )
                    .map((m) => ({
                      id: m.id,
                      label: `${m.subject} · ${m.actorType} · ${m.realmId} · ${m.id}`,
                    })),
                };
              }
              const page = result(
                await props.client.api.GET("/api/v1/access/groups", {
                  params: {
                    query: {
                      limit: 25,
                      state: "active",
                      ...(search ? { name: search } : {}),
                      ...(cursor ? { cursor } : {}),
                    },
                  },
                  signal,
                }),
                check.policyPage(check.group),
              );
              return {
                ...page,
                items: page.items
                  .filter(
                    (g) =>
                      g.state === "active" &&
                      g.tenantId === props.access.tenantId &&
                      g.realmId === props.access.realmId,
                  )
                  .map((g) => ({
                    id: g.id,
                    label: `${g.name} · ${g.realmId} · ${g.id}`,
                  })),
              };
            }}
          />
          <Field
            label="Assignment scope"
            hint="Tenant-wide includes every namespace. Specific namespace covers only that exact namespace; a companion permission does not widen its scope."
            help="Direct and group assignments add their permissions together. Revoking one path does not remove permissions from another active path. Namespace grants do not imply tenant-wide reads."
          >
            <select
              required
              value={scope}
              onChange={(e) => setScope(e.target.value)}
            >
              <option value="">Choose assignment scope</option>
              <option value="tenant">Tenant-wide</option>
              <option value="namespace">Specific namespace</option>
            </select>
          </Field>
          {scope === "namespace" &&
            (availablePermission(
              props.access,
              "namespace.read",
              "catalog.read",
            ) ? (
              <ChoiceField
                label="Namespace"
                manualLabel="Namespace ID"
                value={namespaceId}
                onChange={setNamespace}
                onFailure={props.onFailure}
                load={async (cursor, signal) => {
                  const page = result(
                    await props.client.api.GET("/api/v1/namespaces", {
                      params: {
                        query: { limit: 25, ...(cursor ? { cursor } : {}) },
                      },
                      signal,
                    }),
                    records.NamespacePage,
                  );
                  return {
                    ...page,
                    items: page.items
                      .filter((n) => n.tenantId === props.access.tenantId)
                      .map((n) => ({
                        id: n.id,
                        label: `${n.displayName} · ${n.id}`,
                      })),
                  };
                }}
              />
            ) : (
              <Field
                label="Namespace ID"
                hint="Namespace names require namespace.read. When scopes are enforced, catalog.read is also required. Use an exact reference; the Catalog checks it on confirmation."
              >
                <input
                  required
                  value={namespaceId}
                  onChange={(e) => setNamespace(e.target.value)}
                />
              </Field>
            ))}
          <p>
            Current tenant: <code>{props.access.tenantId}</code>; your issuer
            realm: <code>{props.access.realmId}</code>. Group membership is
            restricted to its selected group's realm.
          </p>
          <button type="button" onClick={selectedRole.refresh}>
            {controlIcons["refresh"]}
            Refresh proposed role details
          </button>
          <GrantPreview
            role={chosenRole}
            scope={scope}
            namespaceId={namespaceId}
          />
          <Reason value={reason} onChange={setReason} />
        </ActionForm>
      </details>
    </>
  );
}
function AssignmentRow(
  props: WorkspaceProps & { assignment: Assignment; readOnly?: boolean },
) {
  const [reason, setReason] = useState("");
  const a = props.assignment;
  const sameRealm = a.realmId === props.access.realmId;
  const writable = a.state === "active" && sameRealm && !props.readOnly;
  return (
    <div className="policy-assignment" data-editor-assignment-id={a.id}>
      <p>
        <a
          href={`${props.runtime.basePath}/access-roles/${a.roleDefinitionId}`}
        >
          {a.roleDefinitionId}
        </a>{" "}
        → {a.membershipId ?? a.groupId} / {a.namespaceId ?? "Tenant-wide"} /{" "}
        <AccessState state={a.state} />
      </p>
      <ActionForm
        title="Revoke access assignment"
        intent="danger"
        icon="remove"
        description="This immediately removes only this path. A member may retain access through other assignments. Reload after a revision conflict."
        editable={
          writable && permits(props.access, "access.manage", "tenant.admin")
        }
        allowed={
          writable && permits(props.access, "access.manage", "tenant.admin")
        }
        unavailableReason={
          props.readOnly
            ? "This archived record is read-only. Retained assignment drafts cannot be sent."
            : sameRealm
              ? "This assignment has already been revoked."
              : `Read-only assignment in realm ${a.realmId}. Revocation requires an administrator session in that realm. Your realm is ${props.access.realmId}.`
        }
        dirty={!!reason}
        onFailure={props.onFailure}
        prepare={() => {
          const body = { reason: reason.trim() };
          const assignmentId = a.id;
          const etag = `"rev-${a.revision}"`;
          return {
            summary: commandSummary([
              ["Action", "Revoke access assignment"],
              ["Assignment ID", assignmentId],
              ["Role ID", a.roleDefinitionId],
              ["Membership ID", a.membershipId ?? "None — group assignment"],
              ["Group ID", a.groupId ?? "None — direct assignment"],
              ["Scope", a.namespaceId ? "Exact namespace" : "Tenant-wide"],
              ["Namespace ID", a.namespaceId ?? "Not namespace-scoped"],
              ["Revision", etag],
              ["Reason", body.reason],
            ]),
            run: async (key) => {
              const reply = await props.client.api.POST(
                "/api/v1/access/assignments/{assignmentId}:revoke",
                {
                  params: {
                    path: { assignmentId },
                    header: { "Idempotency-Key": key, "If-Match": etag },
                  },
                  body,
                },
              );
              const saved = result(reply, check.assignment);
              return commandAcknowledged(
                "Assignment revoked. Refresh this page and affected My access hints.",
                reply.response,
                {
                  action: "authorization.assignment.revoked",
                  targetType: "role_assignment",
                  targetId: assignmentId,
                  revision: saved.revision,
                },
              );
            },
          };
        }}
      >
        <Reason readOnly={!writable} value={reason} onChange={setReason} />
      </ActionForm>
    </div>
  );
}
