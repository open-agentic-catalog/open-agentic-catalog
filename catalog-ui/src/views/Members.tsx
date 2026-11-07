import { useCaseNavigation } from "./case-navigation";
import {
  administrationDirectory,
  directorySearch,
} from "./administration-filter-state";
import { commandSummary } from "./workspace";
import { AccessState } from "./operational-labels";
import { controlIcons } from "../shell/Icon";
import { builtinHelp } from "./access-labels";
import { compareTableText, usePageSort } from "./table-sort";
import { ScrollableTable } from "./ScrollableTable";
import { NamespaceChoice } from "./NamespaceChoice";
import * as checks from "./responses";
import * as accessChecks from "./access-responses";
import {
  DirectoryFilters,
  type DirectoryQuery,
  MemberContributions,
  AccessReference,
  GrantPreview,
} from "./AccessControls";
// Tenant access administration separates membership lifecycle from role-assignment commands.
import { useEffect, useId, useRef, useState } from "react";
import type { components } from "../api/schema";
import { result } from "../api/client";
import { acknowledged } from "./audit-handoff";
import { permits } from "../api/permissions";
import { ActionForm, EvidenceDetails } from "./authoring";
import type { WorkspaceProps } from "./workspace";
import {
  Field,
  Heading,
  Pager,
  ReadState,
  Reason,
  confirmDiscard,
  useRead,
} from "./workspace";

export const catalogRoles = {
  reader: [
    "Reader",
    builtinHelp.reader.summary + " " + builtinHelp.reader.limits,
  ],
  contributor: [
    "Contributor",
    builtinHelp.contributor.summary + " " + builtinHelp.contributor.limits,
  ],
  reviewer: [
    "Reviewer",
    builtinHelp.reviewer.summary + " " + builtinHelp.reviewer.limits,
  ],
  publisher: [
    "Publisher",
    builtinHelp.publisher.summary + " " + builtinHelp.publisher.limits,
  ],
  source_admin: [
    "Source administrator",
    builtinHelp.source_admin.summary + " " + builtinHelp.source_admin.limits,
  ],
  security_reviewer: [
    "Security reviewer",
    builtinHelp.security_reviewer.summary +
      " " +
      builtinHelp.security_reviewer.limits,
  ],
  tenant_admin: [
    "Tenant administrator",
    builtinHelp.tenant_admin.summary + " " + builtinHelp.tenant_admin.limits,
  ],
  service_client: [
    "Membership only - no role permissions",
    builtinHelp.service_client.summary +
      " " +
      builtinHelp.service_client.limits,
  ],
} satisfies Record<components["schemas"]["CatalogRole"], [string, string]>;

/** Gate membership and role-assignment workflows behind tenant administration hints. */
export default function Members(props: WorkspaceProps) {
  const authorized = permits(props.access, "access.manage", "tenant.admin");
  const [admitted, setAdmitted] = useState(authorized);
  if (authorized && !admitted) setAdmitted(true);
  return (
    <>
      <Heading
        title="Members and access"
        icon="user"
        description="Manage Catalog membership and access for an existing issuer identity. This does not create an identity-provider account or issue tokens."
      />
      {!authorized && (
        <p role="alert" className="notice error">
          The access.manage permission is required. When scopes are enforced,
          tenant.admin is also required.{" "}
          <a href={`${props.runtime.basePath}/access`}>Review My access</a>
        </p>
      )}
      {(authorized || admitted) &&
        (props.id ? (
          <MemberDetail {...props} />
        ) : (
          <>
            <MemberList {...props} />
            <details className="workspace-advanced">
              <summary>{controlIcons["add"]} Add Catalog member</summary>
              <GrantRole {...props} />
            </details>
          </>
        ))}
    </>
  );
}

/** Page memberships and require explicit selection before loading member detail. */
function MemberList(props: WorkspaceProps) {
  const sortHint = useId();
  const [cursor, setCursor] = useState("");
  const navigation = useCaseNavigation(
    props,
    "members",
    (search) => {
      setDirectory(administrationDirectory(search, true));
      setCursor("");
    },
    { preserveDirectoryFilters: true },
  );
  const [directory, setDirectory] = useState<DirectoryQuery>(() =>
    administrationDirectory(navigation.listSearch, true),
  );
  const page = useRead(
    JSON.stringify([directory, cursor]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/memberships", {
          params: {
            query: {
              limit: 25,
              ...(directory.subject ? { subject: directory.subject } : {}),
              ...(directory.actorType
                ? { actorType: directory.actorType }
                : {}),
              ...(directory.realmId ? { realmId: directory.realmId } : {}),
              ...(directory.state
                ? {
                    state: directory.state as
                      "active" | "suspended" | "offboarded",
                  }
                : {}),
              ...(cursor ? { cursor: cursor } : {}),
            },
          },
          signal,
        }),
        checks.MembershipPage,
      ),
    props.onFailure,
  );
  const refresh = () => (page.error && cursor ? setCursor("") : page.refresh());
  const realmLabel = (realmId: string) =>
    realmId === props.access.realmId ? "Your realm" : realmId;
  const sorted = usePageSort(page.data?.items, {
    subject: (a, b) => compareTableText(a.subject, b.subject),
    actorType: (a, b) => compareTableText(a.actorType, b.actorType),
    state: (a, b) => compareTableText(a.state, b.state),
    realm: (a, b) =>
      compareTableText(realmLabel(a.realmId), realmLabel(b.realmId)),
  });
  return (
    <>
      <DirectoryFilters
        applied={directory}
        members
        onApply={(query) => {
          setDirectory(query);
          navigation.updateListSearch(directorySearch(query));
          setCursor("");
        }}
      />
      <button disabled={page.busy} onClick={refresh}>
        {controlIcons["refresh"]}
        Refresh
      </button>
      <ReadState {...page} refresh={refresh} />
      {page.data && (
        <>
          <p className="muted">
            Realm identifies the identity-provider boundary a member belongs to.
            Your realm is the one used by your current session.
          </p>
          <p id={sortHint} className="muted">
            Select a column heading to sort the results on this page.
          </p>
          <ScrollableTable label="Members">
            <table className="workspace-table" aria-describedby={sortHint}>
              <thead>
                <tr>
                  {sorted.heading("Login identifier (subject)", "subject")}
                  {sorted.heading("Identity type", "actorType")}
                  {sorted.heading("State", "state")}
                  {sorted.heading("Realm", "realm")}
                  <th scope="col">Access</th>
                </tr>
              </thead>
              <tbody>
                {sorted.rows.map((i) => (
                  <tr key={i.id}>
                    <td>
                      <a href={`${props.runtime.basePath}/members/${i.id}`}>
                        {i.subject}
                      </a>
                    </td>
                    <td>{i.actorType}</td>
                    <td>
                      <AccessState state={i.state} />
                    </td>
                    <td>{realmLabel(i.realmId)}</td>
                    <td>
                      <a
                        className="button"
                        href={`${props.runtime.basePath}/members/${i.id}`}
                        title={`View roles and manage access for ${i.subject}`}
                      >
                        {controlIcons["edit"]}
                        Manage access
                      </a>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!page.data.items.length && <p>No memberships on this page.</p>}
          </ScrollableTable>
        </>
      )}
      <Pager
        label="Member inventory pagination"
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        busy={page.busy}
        onPage={setCursor}
      />
    </>
  );
}

/** Load one membership and concurrency token for lifecycle administration. */
function MemberDetail(props: WorkspaceProps) {
  const detail = useRead(
    props.id,
    async (signal) => {
      const reply = await props.client.api.GET(
        "/api/v1/memberships/{membershipId}",
        { params: { path: { membershipId: props.id } }, signal },
      );
      return {
        member: result(reply, checks.Membership),
        etag: reply.response.headers.get("ETag"),
      };
    },
    props.onFailure,
  );
  return (
    <>
      <a href={`${props.runtime.basePath}/members`}>Back to members</a>
      <ReadState {...detail} />
      {detail.data && <MemberActions {...props} {...detail.data} />}
    </>
  );
}

/** Prepare membership lifecycle changes with an audit reason and captured ETag. */
function MemberActions(
  props: WorkspaceProps & {
    member: components["schemas"]["Membership"];
    etag: string | null;
  },
) {
  const [committed, setCommitted] =
    useState<components["schemas"]["Membership"]>();
  const member = committed ?? props.member;
  const [grantAdmitted] = useState(props.member.state !== "offboarded");
  const [action, setAction] = useState<"suspend" | "reactivate" | "offboard">(
    member.state === "suspended" ? "reactivate" : "suspend",
  );
  const [reason, setReason] = useState("");
  const valid =
    action === "reactivate"
      ? member.state === "suspended"
      : action === "suspend"
        ? member.state === "active"
        : member.state !== "offboarded";
  return (
    <>
      <section className="workspace-panel">
        <h2>{member.subject}</h2>
        <p>
          {member.actorType}: <AccessState state={member.state} />
        </p>
        <EvidenceDetails title="Membership details" value={member} />
      </section>
      <MemberContributions
        key={`contributions-${member.revision}`}
        {...props}
        memberId={member.id}
      />
      <Assignments
        key={`assignments-${member.id}`}
        {...props}
        memberState={member.state}
        memberRevision={member.revision}
      />
      {member.id === props.access.membershipId && (
        <p className="notice">
          This is your membership. Access changes can end this session. The
          Catalog protects the last active Tenant Administrator.
        </p>
      )}
      {props.member.state !== "offboarded" && (
        <details className="workspace-advanced">
          <summary>Change membership state</summary>
          <ActionForm
            title="Change membership state"
            intent={
              action === "offboard"
                ? "danger"
                : action === "suspend"
                  ? "warning"
                  : "primary"
            }
            icon={
              action === "offboard"
                ? "remove"
                : action === "suspend"
                  ? "pause"
                  : "play"
            }
            resultLink={{
              href: `${props.runtime.basePath}/members/${member.id}`,
              label: "Reload membership",
            }}
            description={
              action === "offboard"
                ? "Offboarding removes active grants and cannot be undone by reactivation. The Catalog preserves attributable history."
                : "Suspension blocks access without deleting role history. Reactivation restores an eligible suspended membership; Catalog role and identity-realm rules still apply."
            }
            editable={permits(props.access, "access.manage", "tenant.admin")}
            allowed={
              permits(props.access, "access.manage", "tenant.admin") &&
              !committed &&
              !!props.etag &&
              valid
            }
            unavailableReason={
              !props.etag || committed
                ? "Reload the membership to obtain its current revision."
                : "Choose an action supported by this membership's current state."
            }
            dirty={!!reason}
            onFailure={props.onFailure}
            prepare={() => {
              const path =
                action === "suspend"
                  ? "/api/v1/memberships/{membershipId}:suspend"
                  : action === "reactivate"
                    ? "/api/v1/memberships/{membershipId}:reactivate"
                    : "/api/v1/memberships/{membershipId}:offboard";
              const body = { reason: reason.trim() };
              const etag = props.etag!;
              return {
                summary: commandSummary([
                  [
                    "Action",
                    action === "suspend"
                      ? "Suspend member"
                      : action === "reactivate"
                        ? "Reactivate member"
                        : "Offboard member",
                  ],
                  ["Subject", member.subject],
                  ["Membership ID", member.id],
                  ["Realm ID", member.realmId],
                  ["Revision", etag],
                  ["Reason", body.reason],
                ]),
                run: async (key) => {
                  const reply = await props.client.api.POST(path, {
                    params: {
                      path: { membershipId: member.id },
                      header: { "Idempotency-Key": key, "If-Match": etag },
                    },
                    body,
                  });
                  const change = result(
                    reply,
                    checks.MembershipLifecycleResult,
                  );
                  if (
                    change.membership.id !== member.id ||
                    change.membership.tenantId !== member.tenantId ||
                    change.membership.realmId !== member.realmId
                  )
                    throw new Error(
                      "Membership lifecycle result does not match this member.",
                    );
                  setCommitted(change.membership);
                  return acknowledged(
                    `Membership ${change.membership.state}. Revoked direct assignments: ${change.revokedRoleAssignments}; namespace memberships: ${change.revokedNamespaceMemberships}; group relations: ${change.revokedGroupMemberships ?? 0}. Suspension preserves eligible paths; independent grants can remain after removing a single path.`,
                    reply.response,
                    {
                      action:
                        action === "suspend"
                          ? "identity.membership.suspended"
                          : action === "reactivate"
                            ? "identity.membership.reactivated"
                            : "identity.membership.offboarded",
                      targetType: "tenant_membership",
                      targetId: member.id,
                      revision: change.membership.revision,
                    },
                  );
                },
              };
            }}
          >
            <Field label="Membership action">
              <select
                disabled={!!committed}
                value={action}
                onChange={(e) => setAction(e.target.value as typeof action)}
              >
                <option value="suspend">Suspend</option>
                <option value="reactivate">Reactivate</option>
                <option value="offboard">Offboard</option>
              </select>
            </Field>
            <Reason
              readOnly={!!committed}
              value={reason}
              onChange={setReason}
            />
          </ActionForm>
        </details>
      )}
      {grantAdmitted && (
        <details className="workspace-advanced">
          <summary>{controlIcons["add"]} Grant a built-in role</summary>
          <GrantRole {...props} member={member} />
        </details>
      )}
      {member.state === "offboarded" && (
        <p>
          Offboarding is terminal. This retained membership cannot accept new
          grants or lifecycle changes.
        </p>
      )}
    </>
  );
}

/** Page role assignments independently from the membership inventory. */
function Assignments(
  props: WorkspaceProps & {
    memberState: components["schemas"]["Membership"]["state"];
    memberRevision: number;
  },
) {
  const [cursor, setCursor] = useState("");
  const [revision, setRevision] = useState(props.memberRevision);
  if (revision !== props.memberRevision) {
    setRevision(props.memberRevision);
    setCursor("");
  }
  const page = useRead(
    JSON.stringify([props.id, cursor, props.memberRevision]),
    async (signal) => ({
      ...result(
        await props.client.api.GET("/api/v1/access/assignments", {
          params: {
            query: {
              membershipId: props.id,
              limit: 25,
              ...(cursor ? { cursor: cursor } : {}),
            },
          },
          signal,
        }),
        accessChecks.policyPage(accessChecks.assignment),
      ),
      memberRevision: props.memberRevision,
    }),
    props.onFailure,
  );
  const [selected, setSelected] =
    useState<components["schemas"]["AccessAssignment"]>();
  const editor = useRef<HTMLElement>(null);
  const opener = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (selected) editor.current?.focus();
  }, [selected]);
  return (
    <section className="workspace-panel">
      <h2>Direct role assignments</h2>
      <p>
        These are direct member assignments, not complete effective access. The
        membership filter is direct-only. Inspect Member access contributions
        for current-realm group paths. Companion templates are separate retained
        assignments.
      </p>
      <ReadState {...page} />
      {page.data?.items.map((i) => (
        <div className="workspace-toolbar" key={i.id}>
          <span>
            <AccessReference
              workspace={props}
              kind="role"
              id={i.roleDefinitionId}
              refreshToken={i}
            />{" "}
            / {i.namespaceId ? `Namespace ${i.namespaceId}` : "Tenant-wide"} /{" "}
            <AccessState state={i.state} />
          </span>
          <EvidenceDetails title="Assignment attribution" value={i} />
          {i.state === "active" &&
            i.realmId === props.access.realmId &&
            props.memberState !== "offboarded" && (
              <button
                onClick={(event) => {
                  if (!confirmDiscard()) return;
                  opener.current = event.currentTarget;
                  setSelected(i);
                  if (selected?.id === i.id) editor.current?.focus();
                }}
              >
                {controlIcons["remove"]}
                Revoke assignment {i.id}
              </button>
            )}
        </div>
      ))}
      {page.data && !page.data.items.length && (
        <p>No assignments on this page.</p>
      )}
      <Pager
        label="Member direct assignments pagination"
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        busy={page.busy}
        onPage={(cursor) => {
          if (!confirmDiscard()) return false;
          setCursor(cursor);
          setSelected(undefined);
        }}
      />
      {selected && selected.realmId === props.access.realmId && (
        <section
          aria-label="Selected role revocation editor"
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
            Close role revocation editor
          </button>
          <RevokeRole
            key={selected.id}
            {...props}
            assignment={selected}
            ready={
              !page.busy &&
              !page.error &&
              page.data?.memberRevision === props.memberRevision &&
              page.data.items.some(
                (item) =>
                  item.id === selected.id &&
                  item.revision === selected.revision &&
                  item.state === "active",
              )
            }
          />
        </section>
      )}
    </section>
  );
}

/** Prepare revocation of the selected retained role assignment. */
function RevokeRole(
  props: WorkspaceProps & {
    assignment: components["schemas"]["AccessAssignment"];
    memberState: components["schemas"]["Membership"]["state"];
    ready: boolean;
  },
) {
  const [reason, setReason] = useState("");
  // Role assignments have no exact GET route. The contract's revision is the
  // authoritative validator for this bounded list result.
  const etag = `"rev-${props.assignment.revision}"`;
  return (
    <ActionForm
      title={`Revoke assignment ${props.assignment.id}`}
      intent="danger"
      icon="remove"
      description={`Remove this ${props.assignment.namespaceId ? "namespace" : "tenant-wide"} grant from membership ${props.assignment.membershipId}. The last active Tenant Administrator is protected.`}
      editable={
        props.memberState !== "offboarded" &&
        permits(props.access, "access.manage", "tenant.admin")
      }
      allowed={
        props.ready &&
        props.memberState !== "offboarded" &&
        permits(props.access, "access.manage", "tenant.admin")
      }
      dirty={!!reason}
      onFailure={props.onFailure}
      prepare={() => {
        const body = { reason: reason.trim() };
        return {
          summary: commandSummary([
            ["Action", "Revoke role"],
            ["Assignment ID", props.assignment.id],
            ["Membership ID", props.id],
            ["Target membership", props.id],
            ["Role", props.assignment.roleDefinitionId],
            ["Namespace ID", props.assignment.namespaceId ?? "Tenant-wide"],
            ["Revision", etag],
            ["Reason", body.reason],
          ]),
          run: async (key) => {
            const reply = await props.client.api.POST(
              "/api/v1/access/assignments/{assignmentId}:revoke",
              {
                params: {
                  path: { assignmentId: props.assignment.id },
                  header: { "Idempotency-Key": key, "If-Match": etag },
                },
                body,
              },
            );
            const saved = result(reply, accessChecks.assignment);
            return acknowledged("Role revoked.", reply.response, {
              action: "authorization.assignment.revoked",
              targetType: "role_assignment",
              targetId: props.assignment.id,
              revision: saved.revision,
            });
          },
        };
      }}
    >
      <Reason value={reason} onChange={setReason} />
    </ActionForm>
  );
}

/** Prepare a role grant with explicit subject, role and optional namespace scope. */
function GrantRole(
  props: WorkspaceProps & { member?: components["schemas"]["Membership"] },
) {
  const [subject, setSubject] = useState(props.member?.subject || "");
  const [actorType, setActorType] = useState<
    components["schemas"]["ActorType"]
  >(props.member?.actorType || "human");
  const [role, setRole] =
    useState<components["schemas"]["CatalogRole"]>("service_client");
  const [scope, setScope] = useState("");
  const scoped = scope === "namespace";
  const [namespaceId, setNamespace] = useState("");
  const [reason, setReason] = useState("");
  const [memberID, setMember] = useState("");
  const preview = useRead(
    JSON.stringify([
      role,
      props.access.tenantId,
      props.access.membershipId,
      props.access.authorizationRevision,
    ]),
    async (signal) => {
      const page = result(
        await props.client.api.GET("/api/v1/access/roles", {
          params: { query: { key: role, state: "active", limit: 1 } },
          signal,
        }),
        accessChecks.policyPage(accessChecks.role),
      );
      return (
        page.items.find(
          (r) =>
            r.builtin &&
            r.state === "active" &&
            r.key === role &&
            r.tenantId === props.access.tenantId,
        ) ?? null
      );
    },
    props.onFailure,
  );
  const compatible =
    !scoped ||
    ["reader", "contributor", "reviewer", "publisher"].includes(role);
  const verifiedRole =
    preview.data?.key === role &&
    preview.data.tenantId === props.access.tenantId
      ? preview.data
      : undefined;
  return (
    <>
      <p>
        <a
          href={`${props.runtime.basePath}/access-assignments${props.member ? `?membershipId=${encodeURIComponent(props.member.id)}` : ""}`}
        >
          Inspect and manage direct assignments (membership filter excludes
          groups)
        </a>
      </p>
      <ActionForm
        title={props.member ? "Grant a built-in role" : "Add Catalog member"}
        icon="add"
        description={
          props.member
            ? "Choose a built-in role and its scope for this existing member. Other assignments remain separate."
            : "Add an existing identity to this Catalog. This does not create a sign-in account or issue a token. Choose Human or Service, then select any role permissions to grant."
        }
        editable={
          permits(props.access, "access.manage", "tenant.admin") &&
          (!props.member || props.member.state !== "offboarded")
        }
        allowed={
          !!scope &&
          compatible &&
          !!verifiedRole &&
          permits(props.access, "access.manage", "tenant.admin") &&
          (!props.member ||
            (props.member.realmId === props.access.realmId &&
              props.member.state === "active"))
        }
        unavailableReason={
          !verifiedRole
            ? "Load the selected built-in role before preparing this grant."
            : "Choose an assignment scope explicitly. Membership-only and tenant-only roles cannot use a namespace; change the role or scope deliberately."
        }
        dirty={
          !!reason ||
          subject !== (props.member?.subject || "") ||
          actorType !== (props.member?.actorType || "human") ||
          role !== "service_client" ||
          !!scope ||
          !!namespaceId
        }
        onFailure={props.onFailure}
        prepare={() => {
          const body = {
            subject: subject.trim(),
            actorType,
            role,
            reason: reason.trim(),
            ...(scoped ? { namespaceId } : {}),
          };
          return {
            summary: commandSummary([
              ["Action", "Grant role"],
              ["Subject", body.subject],
              ["Identity type", body.actorType],
              ["Role", body.role],
              [
                "Role permissions",
                verifiedRole!.permissions.join(", ") ||
                  "No product permissions",
              ],
              ["Realm ID", props.access.realmId],
              ["Namespace ID", body.namespaceId ?? "Tenant-wide"],
              ["Reason", body.reason],
            ]),
            run: async (key) => {
              const reply = await props.client.api.POST(
                "/api/v1/role-assignments",
                {
                  params: { header: { "Idempotency-Key": key } },
                  body,
                },
              );
              const grant = result(reply, checks.RoleAssignment);
              setMember(grant.membershipId);
              const message =
                role === "service_client"
                  ? "Membership retained with the empty service_client template. Existing grants are unchanged. Add any specialist grant separately, then verify the recipient in My access."
                  : "Role granted.";
              return acknowledged(message, reply.response, {
                action: "identity.role_assignment.granted",
                targetType: "role_assignment",
                targetId: grant.id,
                revision: grant.revision,
              });
            },
          };
        }}
      >
        <Field
          label="Subject"
          hint="The exact login identifier from your identity provider's sub claim, not a display name. For a local test user, use its configured subject, such as local-reader."
          help="Your identity provider supplies this stable identifier when the person signs in. Ask your identity administrator for it if you do not know it; do not paste a token here. The grant applies in your current identity realm. Email works only if the provider actually uses it as the subject."
        >
          <input
            required
            readOnly={!!props.member}
            maxLength={512}
            value={subject}
            onChange={(e) => setSubject(e.target.value)}
          />
        </Field>
        <Field
          label="Identity type"
          hint="Human means a person signing in. Service means an automation identity. Match the identity's configured type."
        >
          <select
            disabled={!!props.member}
            value={actorType}
            onChange={(e) => setActorType(e.target.value as typeof actorType)}
          >
            <option value="human">Human</option>
            <option value="service">Service</option>
          </select>
        </Field>
        <Field
          label="Catalog role"
          hint={catalogRoles[role][1]}
          help="Most roles are task-specific; Tenant administrator has broad tenant access. Give only the roles needed for the task. API token scopes can further restrict access. A namespace role does not grant tenant-wide browsing."
        >
          <select
            value={role}
            onChange={(e) => setRole(e.target.value as typeof role)}
          >
            {[
              "reader",
              "contributor",
              "reviewer",
              "publisher",
              "source_admin",
              "security_reviewer",
              "tenant_admin",
              "service_client",
            ].map((v) => (
              <option key={v} value={v}>
                {catalogRoles[v as keyof typeof catalogRoles][0]}
              </option>
            ))}
          </select>
        </Field>
        <Field
          label="Assignment scope"
          hint="Tenant-wide covers the whole tenant. Specific namespace limits this grant to one namespace; administrative roles are tenant-wide only."
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
        {scoped && (
          <NamespaceChoice
            workspace={props}
            value={namespaceId}
            onChange={setNamespace}
          />
        )}
        <GrantPreview
          role={verifiedRole}
          scope={scope}
          namespaceId={namespaceId}
          namespaceEligible={[
            "reader",
            "contributor",
            "reviewer",
            "publisher",
          ].includes(role)}
        />
        <button type="button" disabled={preview.busy} onClick={preview.refresh}>
          {controlIcons["refresh"]}
          Refresh proposed role details
        </button>
        <Reason value={reason} onChange={setReason} />
      </ActionForm>
      {memberID && (
        <a
          className="button primary"
          href={`${props.runtime.basePath}/members/${memberID}`}
        >
          {controlIcons["arrow"]}
          Open updated membership
        </a>
      )}
    </>
  );
}
