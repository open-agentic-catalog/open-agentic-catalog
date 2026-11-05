import { createContext, useContext, useState } from "react";
import type { ReactNode } from "react";
import type { Access } from "../api/client";
import { CopyReference } from "./CopyReference";
import { AdministrationAcknowledgement } from "./audit-handoff";
import type { AdministrationReceipt } from "./audit-handoff";
import type { Runtime } from "../api/client";

type Context = {
  tenantId: string;
  realmId: string;
  scope: string;
  acknowledge: (value: AdministrationReceipt) => void;
};
const OperationContext = createContext<Context | undefined>(undefined);

const scopes: Record<string, string> = {
  settings:
    "Tenant-wide settings. Deployment configuration remains operator-owned.",
  members:
    "Membership inventory and lifecycle are tenant-wide. Role grants require the operator's realm; the target member's realm is separate.",
  "access-groups":
    "Group inventory and operations are limited to the operator's realm in the current tenant.",
  "access-roles":
    "Tenant-wide role definitions. Each assignment supplies its own tenant or exact-namespace scope.",
  "access-assignments":
    "Tenant-wide assignment inventory. Each grant targets one tenant or exact namespace and a member or group in the operator's realm.",
  feedback:
    "Tenant-wide private feedback moderation. Each action requires current moderation authority.",
  ratings:
    "Tenant-wide rating moderation. Each action requires current moderation authority.",
  audit:
    "Tenant-wide audit evidence. Reads and exports have separate OAuth ceilings.",
};

/** Presentation uses current self evidence only; it neither loads names nor grants authority. */
export function OperationProvider({
  access,
  view,
  runtime,
  children,
}: {
  access: Access;
  view: string;
  runtime?: Runtime;
  children: ReactNode;
}) {
  const scopeKey = JSON.stringify([access, view]);
  const [stored, setStored] = useState<{
    scopeKey: string;
    receipt: AdministrationReceipt;
  }>();
  const receipt = stored?.scopeKey === scopeKey ? stored.receipt : undefined;
  if (stored && stored.scopeKey !== scopeKey) setStored(undefined);
  const acknowledge = (receipt: AdministrationReceipt) =>
    setStored({ scopeKey, receipt });
  const operationScope = Object.hasOwn(scopes, view) ? scopes[view] : undefined;
  return (
    <OperationContext
      key={JSON.stringify([
        access.tenantId,
        access.realmId,
        access.membershipId,
        access.subject,
        access.actorType,
      ])}
      value={
        operationScope ||
        ["namespaces", "publishers", "service", "jobs"].includes(view)
          ? {
              tenantId: access.tenantId,
              realmId: access.realmId,
              scope:
                operationScope ??
                "Use the exact target and scope captured below.",
              acknowledge,
            }
          : undefined
      }
    >
      {operationScope && (
        <section
          className="operation-context"
          aria-label="Current administration context"
        >
          <div className="operation-summary">
            <strong>Acting as {access.subject}</strong>
            <span title={access.tenantId}>
              Tenant{" "}
              {access.tenantId.length > 12
                ? `${access.tenantId.slice(0, 8)}…`
                : access.tenantId}
            </span>
            <span>
              {view === "access-groups" ? "Current realm" : "Tenant-wide"}
            </span>
          </div>
          <details>
            <summary>Context details</summary>
            <CopyReference label="Current tenant ID" value={access.tenantId} />
            <CopyReference label="Operator realm ID" value={access.realmId} />
            <p>{operationScope}</p>
          </details>
        </section>
      )}
      {children}
      {receipt && runtime && (
        <AdministrationAcknowledgement
          receipt={receipt}
          access={access}
          runtime={runtime}
        />
      )}
    </OperationContext>
  );
}

/** Capture with the prepared command; later renders cannot replace its context. */
export function useOperationContext() {
  return useContext(OperationContext);
}
