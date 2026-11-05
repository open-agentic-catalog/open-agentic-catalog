import { Facts } from "./Facts";
// Retained findings are observations about exact evidence, never edits or approval.
import type { components } from "../api/schema";
import { stateLabel } from "./operational-labels";

type Evidence = components["schemas"]["EvidenceSet"];
export function ValidationFindings({
  evidence,
  revisionId,
  submitted = false,
  fieldLinks,
}: {
  evidence: Evidence;
  revisionId: string;
  submitted?: boolean;
  fieldLinks?:
    Record<string, { label: string; activate: () => void }> | undefined;
}) {
  const expired =
    !!evidence.expiresAt && Date.parse(evidence.expiresAt) <= Date.now();
  return (
    <section
      aria-label={
        submitted
          ? "Submitted validation findings"
          : "Latest validation findings"
      }
    >
      <h3>
        {submitted ? "Submission-bound validation" : "Latest validation"}:{" "}
        {stateLabel(evidence.state)}
      </h3>
      <Facts
        rows={[
          ["Version revision", <code>{revisionId}</code>],
          ["Evidence ID", <code>{evidence.id}</code>],
          ["Evidence digest", <code>{evidence.digest}</code>],
          ["Validation profile", evidence.validationProfile],
          ["Policy", evidence.policyVersion],
          [
            "Expiry",
            evidence.expiresAt
              ? `${evidence.expiresAt}${expired ? " (expired)" : ""}`
              : "No time-based expiry",
          ],
        ]}
      />
      {(expired || evidence.state === "blocked") && (
        <p className="notice warning">
          This evidence is {expired ? "expired" : "blocked"}. The Catalog
          rechecks eligibility at submission, review and publication.
        </p>
      )}
      <p>
        Trust: {stateLabel(evidence.trust.state)}. Legal rights:{" "}
        {stateLabel(evidence.legalRights.state)}. Validation does not establish
        trust, rights, approval or permission to execute.
      </p>
      {!evidence.findings?.length ? (
        <p>No validation findings recorded.</p>
      ) : (
        <ul className="validation-findings">
          {evidence.findings.map((finding, index) => (
            <li key={index} className={`finding-${finding.severity}`}>
              <strong>
                {finding.severity === "error"
                  ? "Blocking error"
                  : finding.severity === "warning"
                    ? "Warning"
                    : "Information"}
              </strong>{" "}
              <code>{finding.code}</code>
              {finding.path && (
                <>
                  {" "}
                  at <code>{finding.path}</code>
                </>
              )}
              <p>{finding.message}</p>
              {finding.path && fieldLinks?.[finding.path] && (
                <button
                  type="button"
                  onClick={fieldLinks[finding.path]!.activate}
                >
                  {fieldLinks[finding.path]!.label}
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
      <p className="muted">
        Findings refer to this retained revision. Use its authoring workflow to
        correct a draft; these paths are not editable fields in this view.
      </p>
    </section>
  );
}
export function ReviewDecisions({
  submission,
}: {
  submission: components["schemas"]["ReviewSubmission"];
}) {
  return (
    <section aria-label="Reviewer decisions">
      <h3>Reviewer decisions</h3>
      <p>
        Submission <code>{submission.id}</code>; listing revision{" "}
        <code>{submission.listingRevisionId}</code>.
      </p>
      {!submission.decisions?.length ? (
        <p>No reviewer decision recorded.</p>
      ) : (
        <ol>
          {submission.decisions.map((decision, index) => (
            <li key={index}>
              <strong
                className="state-badge"
                data-tone={
                  decision.action === "approve"
                    ? "success"
                    : decision.action === "request_changes"
                      ? "warning"
                      : "danger"
                }
              >
                {decision.action === "approve"
                  ? "Approved"
                  : decision.action === "request_changes"
                    ? "Changes requested"
                    : "Rejected"}
              </strong>{" "}
              by {decision.actor} at{" "}
              <time dateTime={decision.at}>{decision.at}</time>.
              <p>
                {decision.reason?.trim()
                  ? decision.reason
                  : "No reason was recorded."}
              </p>
            </li>
          ))}
        </ol>
      )}
      <p className="muted">
        These immutable decisions concern this submission. Later candidates do
        not change this history or establish current release eligibility.
      </p>
    </section>
  );
}
