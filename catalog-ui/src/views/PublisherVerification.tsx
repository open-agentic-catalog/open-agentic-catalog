import { useEffect, useRef, useState } from "react";
import { result } from "../api/client";
import type { components } from "../api/schema";
import { permits } from "../api/permissions";
import { controlIcons } from "../shell/Icon";
import { CopyReference } from "./CopyReference";
import { DateValue } from "./date-value";
import { Facts } from "./Facts";
import { acknowledged } from "./audit-handoff";
import { useExpired } from "./use-expired";
import {
  publisherVerificationChallenge,
  publisherVerificationDomain,
  publisherVerificationMode,
  publisherVerificationStatus,
} from "./publisher-verification-values";
import {
  CommandDialog,
  Field,
  ReadState,
  Reason,
  useRead,
  useUnsaved,
} from "./workspace";
import type { Command, WorkspaceProps } from "./workspace";

type Publisher = components["schemas"]["Publisher"];
type Action = "issue" | "check" | "clear";
type Proof = components["schemas"]["PublisherDomainVerification"];
type VerificationProps = WorkspaceProps & {
  publisher: Publisher;
  blocked: boolean;
  reason: string;
  setReason: (value: string) => void;
};
const paths = {
  issue: "/api/v1/publishers/{publisherId}/domains/{domain}/verification:issue",
  check: "/api/v1/publishers/{publisherId}/domains/{domain}/verification:check",
  clear: "/api/v1/publishers/{publisherId}/domains/{domain}/verification:clear",
} as const;
const outcomes = {
  matched: "The expected TXT value matched.",
  mismatch: "DNS answered, but the expected TXT value was not present.",
  not_found: "No TXT record was found.",
  unavailable:
    "The DNS lookup could not complete. Check DNS availability and propagation.",
  response_limit: "The DNS answer exceeded the Catalog's record or size limit.",
} as const;

/** Fetch private evidence only after the user opens this part of the Publisher. */
export function PublisherVerification(props: VerificationProps) {
  const [open, setOpen] = useState(false);
  return (
    <section
      className="workspace-panel"
      aria-label="Publisher DNS verification"
    >
      <h2>DNS verification</h2>
      <p>
        DNS evidence records control of an exact domain at one time. Publication
        approval is a separate decision.
      </p>
      <p className="muted">
        Approval can require current DNS evidence. Open this panel to check the
        configured mode.
      </p>
      {!open ? (
        <button type="button" onClick={() => setOpen(true)}>
          {controlIcons["globe"]} View DNS verification
        </button>
      ) : (
        <VerificationWorkspace {...props} />
      )}
    </section>
  );
}
function VerificationWorkspace(props: VerificationProps) {
  const caps = useRead(
    "publisher-verification-policy",
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/capabilities", { signal }),
        publisherVerificationMode,
      ),
    props.onFailure,
  );
  const [domain, setDomain] = useState(props.publisher.domains[0] ?? "");
  return (
    <>
      <ReadState {...caps} />
      {caps.data && (
        <>
          <p className="notice">
            {caps.data.governance.publisherDomainVerification === "disabled"
              ? "DNS verification is turned off. Existing evidence remains readable. Ask your operator to enable new checks."
              : caps.data.governance.publisherDomainVerification ===
                  "required_for_approval"
                ? "Fresh DNS evidence is required for a new publication approval. Existing approvals remain in effect when evidence expires or is cleared."
                : "DNS verification is optional. It does not approve a domain for publication."}
          </p>
          <Field label="DNS verification domain">
            <select
              value={domain}
              disabled={!!props.reason}
              onChange={(event) => setDomain(event.target.value)}
            >
              {props.publisher.domains.map((value) => (
                <option key={value} value={value}>
                  {value}
                </option>
              ))}
            </select>
          </Field>
          {props.reason && (
            <p className="muted">
              Clear the DNS verification reason before selecting another domain.
            </p>
          )}
          {domain &&
            (publisherVerificationDomain(domain) ? (
              <DomainEvidence key={domain} {...props} domain={domain} />
            ) : (
              <p className="notice">
                DNS verification requires an exact DNS name with at most 221
                characters. IP addresses and wildcard domains cannot be
                verified.
              </p>
            ))}
        </>
      )}
    </>
  );
}
function DomainEvidence(props: VerificationProps & { domain: string }) {
  const { domain } = props;
  const manage = permits(props.access, "publisher.manage", "publishers.manage");
  const access = useRef({ allowed: manage, generation: 0 });
  if (access.current.allowed !== manage)
    access.current = {
      allowed: manage,
      generation: access.current.generation + 1,
    };
  const [epoch, setEpoch] = useState(0);
  const { reason, setReason } = props;
  const [instructions, setInstructions] = useState<number>();
  const [command, setCommand] = useState<Command & { generation: number }>();
  const [receipt, setReceipt] = useState<{
    text: string;
    failed: boolean;
    generation: number;
  }>();
  const failedCheck = useRef(false);
  const refreshButton = useRef<HTMLButtonElement>(null);
  const restoreFocus = useRef(false);
  const status = useRead(
    JSON.stringify([domain, epoch]),
    async (signal) => {
      const reply = await props.client.api.GET(
        "/api/v1/publishers/{publisherId}/domains/{domain}/verification",
        {
          params: { path: { publisherId: props.publisher.id, domain } },
          signal,
        },
      );
      const value = result(reply, publisherVerificationStatus);
      if (value.domain !== domain)
        throw new Error(
          "Verification response does not match the selected domain.",
        );
      return { value, etag: reply.response.headers.get("ETag"), epoch };
    },
    props.onFailure,
  );
  const data = status.data?.epoch === epoch ? status.data : undefined;
  useEffect(() => {
    if (restoreFocus.current && !status.busy && (data || status.error)) {
      restoreFocus.current = false;
      if (document.activeElement === document.body)
        refreshButton.current?.focus();
    }
  }, [data, status.busy, status.error]);
  const current = data?.value;
  const enabled = !!current && current.mode !== "disabled";
  const proofExpired = useExpired(
    current?.state === "verified" ? current.expiresAt : undefined,
  );
  const challengeExpired = useExpired(
    current?.challengeState === "pending"
      ? current.challengeExpiresAt
      : undefined,
  );
  const ready =
    !!data?.etag &&
    !status.busy &&
    !status.error &&
    !props.blocked &&
    manage &&
    enabled;
  useUnsaved(!!reason);
  const refresh = () => {
    setInstructions(undefined);
    setEpoch((value) => value + 1);
  };
  function prepare(action: Action) {
    if (!ready || !data?.etag || !reason.trim()) return;
    failedCheck.current = false;
    const etag = data.etag;
    const body = { reason: reason.trim() };
    const generation = access.current.generation;
    const publisherId = props.publisher.id;
    const challengeId = current?.challengeId;
    const titles = {
      issue:
        current?.challengeState === "not_issued"
          ? "Create DNS challenge"
          : "Replace DNS challenge",
      check: "Check DNS",
      clear: "Clear DNS verification",
    };
    setCommand({
      generation,
      title: titles[action],
      icon:
        action === "clear" ? "remove" : action === "check" ? "check" : "add",
      intent: action === "clear" ? "danger" : "primary",
      description:
        action === "clear"
          ? "Clear the pending challenge and DNS proof. Publication approval stays unchanged."
          : action === "issue"
            ? "Create a fresh TXT value. Any pending value stops working. Earlier proof keeps its original expiry."
            : "Query DNS once for the current TXT value. Replaying this command returns its original result without another lookup.",
      summary: [
        { label: "Publisher ID", value: publisherId },
        { label: "Domain", value: domain },
        { label: "Verification ETag", value: etag },
        { label: "Reason", value: body.reason },
      ],
      ...(action === "check" && current?.challengeExpiresAt
        ? { expiresAt: current.challengeExpiresAt }
        : {}),
      run: async (key) => {
        const options = {
          params: {
            path: { publisherId, domain },
            header: { "Idempotency-Key": key, "If-Match": etag },
          },
          body,
        };
        const issued =
          action === "issue"
            ? await props.client.api.POST(paths.issue, options)
            : undefined;
        const checked =
          action === "check"
            ? await props.client.api.POST(paths.check, options)
            : undefined;
        const cleared =
          action === "clear"
            ? await props.client.api.POST(paths.clear, options)
            : undefined;
        const issuedValue = issued
          ? result(issued, publisherVerificationChallenge)
          : undefined;
        const value: Proof = issuedValue
          ? issuedValue.status
          : checked
            ? result(checked, publisherVerificationStatus)
            : result(cleared!, publisherVerificationStatus);
        if (value.domain !== domain)
          throw new Error(
            "Command response does not match the selected domain.",
          );
        let message: string;
        if (action === "check") {
          if (!value.lastCheckOutcome)
            throw new Error("DNS check response does not contain an outcome.");
          if (value.lastCheckChallengeId !== challengeId)
            throw new Error(
              "DNS check response does not match the submitted challenge.",
            );
          message = outcomes[value.lastCheckOutcome];
        } else {
          if (action === "issue" && !issuedValue?.value)
            throw new Error(
              "DNS challenge response does not contain TXT instructions.",
            );
          if (
            action === "clear" &&
            (value.state !== "not_verified" ||
              value.challengeState !== "not_issued" ||
              (
                [
                  "challengeId",
                  "challengeIssuedAt",
                  "challengeExpiresAt",
                  "verifiedAt",
                  "expiresAt",
                ] as const
              ).some((field) => value[field] !== undefined))
          )
            throw new Error(
              "DNS clear response does not show cleared evidence.",
            );
          message =
            action === "issue"
              ? "DNS challenge issued. Show the current TXT instructions below."
              : "DNS verification cleared. Publication approval is unchanged.";
        }
        const reply = issued ?? checked ?? cleared!;
        if (!access.current.allowed || access.current.generation !== generation)
          return "Command acknowledged. Reload with current access to inspect the result.";
        failedCheck.current =
          action === "check" && value.lastCheckOutcome !== "matched";
        const originalResult =
          action === "issue"
            ? "The Catalog issued a DNS challenge."
            : action === "clear"
              ? "The Catalog cleared DNS verification."
              : message;
        const receiptMessage =
          reply.response.headers.get("Idempotency-Replayed") === "true"
            ? `Original command result: ${originalResult} The Catalog did not query DNS or change the evidence again.`
            : message;
        return acknowledged(receiptMessage, reply.response, {
          action:
            action === "issue"
              ? "publisher.domain_challenge_issued"
              : action === "check"
                ? "publisher.domain_verification_checked"
                : "publisher.domain_verification_cleared",
          targetType: "publisher",
          targetId: publisherId,
        });
      },
    });
  }
  return (
    <>
      <ReadState {...status} />
      {receipt && (
        <p
          className={receipt.failed ? "notice error" : "notice"}
          role={receipt.failed ? "alert" : "status"}
        >
          {receipt.generation === access.current.generation && manage
            ? receipt.text
            : "Command acknowledged. Reload with current access to inspect the result."}
        </p>
      )}
      <button
        type="button"
        ref={refreshButton}
        disabled={status.busy || !!command}
        onClick={refresh}
      >
        {controlIcons["refresh"]} Refresh DNS status
      </button>
      {current && (
        <>
          <Facts
            rows={[
              ["Domain", domain],
              [
                "Publication approval",
                props.publisher.approvedPublicationDomains.includes(domain)
                  ? "Approved"
                  : "Not approved",
              ],
              [
                "DNS proof",
                current.state === "verified" && !proofExpired
                  ? "Current"
                  : current.state === "not_verified"
                    ? "Not verified"
                    : "Expired or not current",
              ],
              [
                "Proof recorded",
                current.verifiedAt ? (
                  <DateValue value={current.verifiedAt} />
                ) : (
                  "None"
                ),
              ],
              [
                "Proof expires",
                current.expiresAt ? (
                  <DateValue value={current.expiresAt} />
                ) : (
                  "None"
                ),
              ],
              [
                "Challenge",
                challengeExpired
                  ? "Expired"
                  : (
                      {
                        not_issued: "Not issued",
                        pending: "Pending",
                        expired: "Expired",
                        consumed: "Completed",
                      } as const
                    )[current.challengeState],
              ],
              [
                "Challenge expires",
                current.challengeExpiresAt ? (
                  <DateValue value={current.challengeExpiresAt} />
                ) : (
                  "None"
                ),
              ],
              [
                "Last DNS check",
                current.lastCheckAt ? (
                  <DateValue value={current.lastCheckAt} />
                ) : (
                  "None"
                ),
              ],
              [
                "Last check result",
                current.lastCheckOutcome
                  ? outcomes[current.lastCheckOutcome]
                  : "No completed check",
              ],
            ]}
          />
          {current.lastCheckChallengeId &&
            current.lastCheckChallengeId !== current.challengeId && (
              <p className="muted">
                The last DNS check used an earlier challenge.
              </p>
            )}
          {enabled && manage && data && !data.etag && (
            <p className="notice error" role="alert">
              The API did not return a verification ETag. Refresh DNS status
              before preparing a command.
            </p>
          )}
          {enabled && manage && (
            <>
              {props.blocked && (
                <p className="notice">
                  Finish or discard publisher metadata changes before changing
                  DNS verification.
                </p>
              )}
              <fieldset
                className="workspace-fields"
                disabled={props.blocked || !!command}
              >
                <Reason
                  label="DNS verification reason"
                  value={reason}
                  onChange={setReason}
                />
                <div className="workspace-toolbar">
                  <button
                    type="button"
                    disabled={!ready || !reason.trim()}
                    onClick={() => prepare("issue")}
                  >
                    {controlIcons["add"]}
                    {current.challengeState === "not_issued"
                      ? "Create DNS challenge"
                      : "Replace DNS challenge"}
                  </button>
                  <button
                    type="button"
                    disabled={
                      !ready ||
                      !reason.trim() ||
                      current.challengeState !== "pending" ||
                      challengeExpired
                    }
                    onClick={() => prepare("check")}
                  >
                    {controlIcons["check"]}Check DNS
                  </button>
                  <button
                    type="button"
                    className="danger"
                    disabled={
                      !ready ||
                      !reason.trim() ||
                      (!current.challengeId && !current.verifiedAt)
                    }
                    onClick={() => prepare("clear")}
                  >
                    {controlIcons["remove"]}Clear DNS verification
                  </button>
                </div>
              </fieldset>
              {current.challengeState === "pending" && !challengeExpired && (
                <button
                  type="button"
                  onClick={() => setInstructions(access.current.generation)}
                  disabled={status.busy || !!command}
                >
                  {controlIcons["copy"]}Show TXT instructions
                </button>
              )}
              {instructions === access.current.generation &&
                current.challengeState === "pending" &&
                !challengeExpired && (
                  <TXTInstructions
                    key={JSON.stringify([
                      domain,
                      access.current.generation,
                      epoch,
                    ])}
                    {...props}
                    challengeId={current.challengeId ?? ""}
                  />
                )}
            </>
          )}
          {enabled && !manage && (
            <p className="muted">
              TXT instructions and DNS commands require publisher.manage and
              publishers.manage when scopes are enforced.
            </p>
          )}
        </>
      )}
      {command && (
        <CommandDialog
          command={command}
          allowed={
            ready &&
            !!reason.trim() &&
            command.generation === access.current.generation
          }
          onClose={() => setCommand(undefined)}
          onFailure={(error) => {
            if (
              access.current.allowed &&
              command.generation === access.current.generation
            )
              props.onFailure(error);
          }}
          onDone={(text) => {
            restoreFocus.current = true;
            setReceipt({
              text,
              failed: failedCheck.current,
              generation: command.generation,
            });
            setCommand(undefined);
            setReason("");
            refresh();
          }}
        />
      )}
    </>
  );
}
function TXTInstructions(
  props: WorkspaceProps & {
    publisher: Publisher;
    domain: string;
    challengeId: string;
  },
) {
  const read = useRead(
    props.domain,
    async (signal) =>
      result(
        await props.client.api.GET(
          "/api/v1/publishers/{publisherId}/domains/{domain}/verification/challenge",
          {
            params: {
              path: { publisherId: props.publisher.id, domain: props.domain },
            },
            signal,
          },
        ),
        publisherVerificationChallenge,
      ),
    props.onFailure,
  );
  const expired = useExpired(read.data?.expiresAt);
  const value = read.data;
  return (
    <section className="workspace-panel" aria-label="Current TXT instructions">
      <ReadState {...read} />
      {value &&
        (!expired &&
        value.value &&
        value.status.domain === props.domain &&
        value.challengeId === props.challengeId ? (
          <>
            <p>
              Add this TXT record through your DNS provider. Keep the value
              unchanged. Some providers add the zone name automatically.
            </p>
            <CopyReference label="TXT record name" value={value.name ?? ""} />
            <CopyReference label="TXT record value" value={value.value} />
            <p>
              DNS changes can take time to appear. Choose Check DNS explicitly
              after updating the record. Remove the value after completion or
              expiry.
            </p>
          </>
        ) : (
          <p className="notice">
            The challenge changed or expired. Refresh DNS status before
            requesting new instructions.
          </p>
        ))}
    </section>
  );
}
