// Investigation criteria stay in this access-bound tab, never in URLs or storage.
import { createContext, useContext, useState } from "react";
import type { ReactNode } from "react";
import type { Access, Runtime } from "../api/client";
import { permits } from "../api/permissions";
import { CopyReference } from "./CopyReference";
import { confirmDiscard } from "./workspace";

export type AuditCriteria = Partial<{
  actor: string;
  action: string;
  requestId: string;
  targetType: string;
  targetId: string;
  occurredFrom: string;
  occurredBefore: string;
}>;
export type AdministrationReceipt = {
  action: string;
  targetType?: string | undefined;
  targetId?: string | undefined;
  revision?: number | undefined;
  requestId?: string | undefined;
  replayed: boolean;
  acknowledgedAt: string;
};
export type CommandOutcome = {
  message: string;
  receipt: AdministrationReceipt;
};

/** Only code-owned binding and already validated response facts enter this projection. */
export function acknowledged(
  message: string,
  response: Response,
  binding: Pick<
    AdministrationReceipt,
    "action" | "targetType" | "targetId" | "revision"
  >,
): CommandOutcome {
  const requestId = response.headers.get("X-Request-ID") ?? "";
  return {
    message,
    receipt: {
      ...binding,
      requestId: /^[A-Za-z0-9._-]{1,128}$/.test(requestId)
        ? requestId
        : undefined,
      replayed: response.headers.get("Idempotency-Replayed") === "true",
      acknowledgedAt: new Date().toISOString(),
    },
  };
}

type Handoff = { criteria: AuditCriteria; note: string };
const HandoffContext = createContext<
  | {
      pending?: Handoff | undefined;
      stage: (value: Handoff) => void;
      clear: () => void;
    }
  | undefined
>(undefined);

export function InvestigationProvider({
  children,
  policyKey = "",
}: {
  children: ReactNode;
  policyKey?: string;
}) {
  const [stored, setStored] = useState<{ key: string; value: Handoff }>();
  const pending = stored?.key === policyKey ? stored.value : undefined;
  if (stored && stored.key !== policyKey) setStored(undefined);
  return (
    <HandoffContext
      value={{
        pending,
        stage: (value) => setStored({ key: policyKey, value }),
        clear: () => setStored(undefined),
      }}
    >
      {children}
    </HandoffContext>
  );
}
export function useInvestigation() {
  return useContext(HandoffContext);
}

/** An acknowledgement is historical command evidence, separate from a current GET. */
export function AdministrationAcknowledgement({
  receipt,
  access,
  runtime,
}: {
  receipt: AdministrationReceipt;
  access: Access;
  runtime: Runtime;
}) {
  const handoff = useInvestigation();
  return (
    <section
      className="workspace-panel"
      aria-label="Acknowledged administration change"
    >
      <h2>Acknowledged change</h2>
      <p>
        This receipt describes the confirmed command. Read the target separately
        for its current state.
      </p>
      <CopyReference label="acknowledged operation" value={receipt.action} />
      {receipt.targetType && (
        <CopyReference
          label="acknowledged target type"
          value={receipt.targetType}
        />
      )}
      {receipt.targetId && (
        <CopyReference
          label="acknowledged target ID"
          value={receipt.targetId}
        />
      )}
      {receipt.revision !== undefined && (
        <CopyReference
          label="acknowledged revision"
          value={String(receipt.revision)}
        />
      )}
      {receipt.requestId && (
        <CopyReference
          label="HTTP attempt request ID"
          value={receipt.requestId}
        />
      )}
      <p>
        {receipt.replayed
          ? "Replayed command acknowledgement. This HTTP attempt may be later than the original change."
          : "Command acknowledged."}
      </p>
      {handoff &&
        receipt.targetType &&
        receipt.targetId &&
        permits(access, "audit.read", "audit.read") && (
          <a
            className="button"
            href={`${runtime.basePath}/audit`}
            onClick={(event) => {
              if (
                event.button ||
                event.metaKey ||
                event.ctrlKey ||
                event.shiftKey ||
                event.altKey
              )
                return;
              event.preventDefault();
              if (!confirmDiscard()) return;
              const at = Date.parse(receipt.acknowledgedAt);
              handoff.stage({
                criteria: {
                  action: receipt.action,
                  actor: "",
                  requestId: "",
                  targetType: receipt.targetType!,
                  targetId: receipt.targetId!,
                  occurredFrom: new Date(at - 5 * 60000).toISOString(),
                  occurredBefore: new Date(at).toISOString(),
                },
                note: "Criteria are staged, not searched. This five-minute interval ends at acknowledgement time. A replay can describe an older change: adjust the bounds as needed. The HTTP attempt ID is not used as the original mutation's audit request ID.",
              });
              history.pushState(null, "", `${runtime.basePath}/audit`);
              window.dispatchEvent(new PopStateEvent("popstate"));
            }}
          >
            Investigate change
          </a>
        )}
    </section>
  );
}
