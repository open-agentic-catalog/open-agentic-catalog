import { Facts } from "./Facts";
import { checkAccess } from "../api/session";
// Self-context pagination shows current caller grants without reading the tenant assignment inventory.
import { useEffect, useRef, useState } from "react";
import { APIError, result } from "../api/client";
import type { Access as CurrentAccess, Client, Runtime } from "../api/client";
import { useRead, ReadState, Pager, Heading } from "./workspace";
import * as checks from "./access-responses";
import { navigationGroups } from "../shell/navigation";
import { EvidenceDetails } from "./authoring";
import { Icon, controlIcons } from "../shell/Icon";
import { AccessReference, BuiltinRoleHelp } from "./AccessControls";
import { readOnlyTask, permissionLabels } from "./access-labels";

/** Self-context pages disclose only this caller's resolved grants. They do not
 * replace the API's authorization or infer roles from a selected namespace. */
export default function Access({
  access,
  runtime,
  client,
  onFailure,
}: {
  access: CurrentAccess;
  runtime: Runtime;
  client: Client;
  onFailure: (error: unknown) => void;
}) {
  return (
    <SelfAccess
      key={JSON.stringify([
        access.tenantId,
        access.realmId,
        access.membershipId,
        access.actorType,
        access.authorizationRevision,
        access.scopeEnforcement,
        access.scopes,
        access.permissions,
      ])}
      access={access}
      runtime={runtime}
      client={client}
      onFailure={onFailure}
    />
  );
}

function SelfAccess({
  access,
  runtime,
  client,
  onFailure,
}: {
  access: CurrentAccess;
  runtime: Runtime;
  client: Client;
  onFailure: (error: unknown) => void;
}) {
  const [assignmentCursor, setAssignmentCursor] = useState("");
  const assignments = useRead(
    `${access.tenantId}/${access.membershipId}/${access.subject}/${access.authorizationRevision}/${assignmentCursor}`,
    async (signal) => {
      const value = result(
        await client.api.GET("/api/v1/access/self", {
          params: {
            query: {
              limit: 25,
              ...(assignmentCursor ? { cursor: assignmentCursor } : {}),
            },
          },
          signal,
        }),
        checks.policyPage(checks.effective),
      );
      if (
        value.items.some(
          (item) =>
            item.assignment.tenantId !== access.tenantId ||
            item.assignment.realmId !== access.realmId ||
            (item.assignment.membershipId &&
              item.assignment.membershipId !== access.membershipId),
        )
      )
        throw new Error(
          "Assignment sources do not match the current identity.",
        );
      return value;
    },
    onFailure,
  );
  const [page, setPage] = useState(access);
  const [cursor, setCursor] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const failure = useRef(onFailure);
  failure.current = onFailure;
  useEffect(() => {
    setCursor("");
    setAssignmentCursor("");
    setPage(access);
  }, [access]);
  useEffect(() => {
    setError("");
    if (!cursor) {
      setPage(access);
      setBusy(false);
      return;
    }
    const controller = new AbortController();
    let current = true;
    setBusy(true);
    client.api
      .GET("/api/v1/me", {
        params: { query: { cursor } },
        signal: controller.signal,
      })
      .then((reply) => {
        if (current) {
          const value = result(reply, checkAccess);
          if (
            value.tenantId !== access.tenantId ||
            value.realmId !== access.realmId ||
            value.membershipId !== access.membershipId
          )
            throw new Error("Access page does not match the current identity.");
          setPage(value);
        }
      })
      .catch((e) => {
        if (current) {
          failure.current(e);
          setError(
            e instanceof APIError ? e.message : "Access could not be loaded.",
          );
        }
      })
      .finally(() => {
        if (current) setBusy(false);
      });
    return () => {
      current = false;
      controller.abort();
    };
  }, [access, client, cursor]);
  return (
    <>
      <Heading
        title="My access"
        icon="access"
        description="View your current permissions and assignments. Signing in does not grant additional permissions."
      />
      <section className="access-panel" aria-labelledby="identity-heading">
        <h2 id="identity-heading">
          <Icon name="user" size={18} />
          Your identity
        </h2>
        <Facts
          className="metadata-grid"
          rows={[
            ["Subject", access.subject],
            [
              "Actor type",
              access.actorType === "human" ? "Human" : "Service (nonhuman)",
            ],
            ["Identity realm", <code>{access.realmId}</code>],
            ["Tenant", <code>{access.tenantId}</code>],
            [
              "Tenant-wide permissions",
              access.permissions.length ? (
                <details>
                  <summary>
                    {access.permissions.length} tenant-wide permissions
                  </summary>
                  <div className="role-tags">
                    {access.permissions.map((permission) => (
                      <span className="badge" key={permission}>
                        {permission}
                      </span>
                    ))}
                  </div>
                </details>
              ) : (
                "No tenant-wide permissions"
              ),
            ],
            [
              "Token scopes (OAuth)",
              access.scopeEnforcement
                ? access.scopes.join(", ") || "None"
                : "Additional scope checks are disabled",
            ],
          ]}
        />
      </section>
      <section className="access-panel" aria-labelledby="tasks-heading">
        <h2 id="tasks-heading">Your available tasks</h2>
        <p className="muted">
          Choose a task below. Access is checked again for each action.
        </p>
        <nav aria-label="Available tasks" className="task-links">
          {navigationGroups(access)
            .flatMap((group) => group.entries)
            .filter((entry) => entry.allowed && entry.path !== "access")
            .map((entry) => (
              <a key={entry.path} href={`${runtime.basePath}/${entry.path}`}>
                <Icon name={entry.icon} size={18} />
                {entry.label}
                {readOnlyTask(access, entry.path) && (
                  <>
                    {" "}
                    <span className="badge">Read-only</span>
                  </>
                )}
              </a>
            ))}
        </nav>
        <details>
          <summary>Permission and account limits</summary>{" "}
          <p>
            {access.actorType === "human"
              ? "Active humans can use eligible My work and own-rating reads even with no role permissions, subject to OAuth ceilings. Withdrawing an existing own rating does not require current target visibility or ratings.own; it still requires human identity, ownership and the ratings.write scope when enforced. A new score requires separate target and rating permission."
              : "Service identities retain authenticated self and service-information reads. Human-only My work, ratings, feedback and library actions are unavailable."}
          </p>
        </details>
      </section>
      <section className="access-panel">
        <h2>Assignment sources</h2>
        <button
          type="button"
          disabled={assignments.busy}
          onClick={() =>
            assignmentCursor ? setAssignmentCursor("") : assignments.refresh()
          }
        >
          {controlIcons["refresh"]}
          Refresh assignment sources
        </button>
        <ReadState
          {...assignments}
          refresh={() =>
            assignments.error && assignmentCursor
              ? setAssignmentCursor("")
              : assignments.refresh()
          }
        />
        {assignments.data && (
          <>
            {assignments.data.items.map((item) => (
              <details className="policy-summary" key={item.assignment.id}>
                <summary>
                  <strong>{item.role.name}</strong> /{" "}
                  {item.assignment.namespaceId ? (
                    <AccessReference
                      workspace={{ access, runtime, client, onFailure, id: "" }}
                      kind="namespace"
                      id={item.assignment.namespaceId}
                      refreshToken={item}
                    />
                  ) : (
                    "Tenant-wide"
                  )}{" "}
                  /{" "}
                  {item.assignment.groupId ? (
                    <>
                      Group {item.groupName ?? "name unavailable"} (
                      <code>{item.assignment.groupId}</code>)
                    </>
                  ) : (
                    "Direct member grant"
                  )}
                </summary>
                <details>
                  <summary>Role template and permission keys</summary>
                  <BuiltinRoleHelp role={item.role} />
                  <p>Role permissions: {item.role.permissions.join(", ")}.</p>
                </details>
                {item.role.permissions.length > 0 && (
                  <details>
                    <summary>Permission meanings</summary>
                    <ul>
                      {item.role.permissions.map((permission) => (
                        <li key={permission}>
                          {permissionLabels[permission]}:{" "}
                          <code>{permission}</code>
                        </li>
                      ))}
                    </ul>
                  </details>
                )}
                <EvidenceDetails
                  title="Assignment attribution"
                  value={item.assignment}
                />
                <details>
                  <summary>Exact assignment references</summary>
                  <Facts
                    className="metadata-grid"
                    rows={[
                      ["Assignment", <code>{item.assignment.id}</code>],
                      ["Role", <code>{item.role.id}</code>],
                      ["State", item.assignment.state],
                    ]}
                  />
                </details>
              </details>
            ))}
          </>
        )}
        <Pager
          label="Own access assignments pagination"
          cursor={assignmentCursor}
          nextCursor={assignments.data?.nextCursor}
          busy={assignments.busy}
          onPage={setAssignmentCursor}
        />
      </section>
      <section className="access-panel" aria-labelledby="grants-heading">
        <h2 id="grants-heading">
          <Icon name="access" size={18} />
          Namespace grants
        </h2>
        {busy ? (
          <p role="status">Loading grants...</p>
        ) : error ? (
          <p className="notice error" role="alert">
            {error}
          </p>
        ) : page.namespaceGrants.length ? (
          <ul className="grant-list">
            {page.namespaceGrants.map((grant) => (
              <li key={grant.namespaceId}>
                <details>
                  <summary>
                    <AccessReference
                      workspace={{
                        access: page,
                        runtime,
                        client,
                        onFailure,
                        id: "",
                      }}
                      kind="namespace"
                      id={grant.namespaceId}
                    />{" "}
                    · {grant.permissions.length} permissions
                  </summary>
                  <p>{grant.permissions.join(", ")}</p>
                </details>
              </li>
            ))}
          </ul>
        ) : (
          <p className="muted">No namespace grants.</p>
        )}
        <Pager
          label="Namespace grants pagination"
          cursor={cursor}
          nextCursor={error ? undefined : page.nextCursor}
          busy={busy}
          onPage={setCursor}
        />
      </section>
      <button onClick={() => location.reload()}>
        {controlIcons["refresh"]} Refresh My access
      </button>
      <details>
        <summary>Navigation hints and policy revision</summary>
        <p>
          Policy revision: {access.authorizationRevision}. Permissions in at
          least one scope: {access.availablePermissions.join(", ") || "None"}.
        </p>
      </details>
      <p className="notice">
        Namespace grants apply only to their assigned namespace. A visible link
        does not grant access elsewhere. Token scopes may further restrict
        actions. Revoking one grant can leave other direct or group grants
        effective.
      </p>
    </>
  );
}
