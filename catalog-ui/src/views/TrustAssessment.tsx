import { controlIcons } from "../shell/Icon";
import { StatusLabel } from "./operational-labels";
import { Facts } from "./Facts";
import { DateValue } from "./date-value";
import { ScrollableTable } from "./ScrollableTable";
// Private evidence is read only by explicit action and never cached across identities.
import { useEffect, useState } from "react";
import { APIError, result } from "../api/client";
import { documentationGuideLink } from "../api/session";
import { CopyReference } from "./CopyReference";
import { CodeBlock } from "./CodeBlock";
import type { components } from "../api/schema";
import { Field, Heading, ReadState, useRead } from "./workspace";
import type { WorkspaceProps } from "./workspace";
import {
  canReadTrust,
  canReadTrustJob,
  trustID,
  trustResponse,
  trustChecks,
  evidenceNames,
  evidenceBytes,
} from "./trust-result";

type Response = components["schemas"]["TrustAssessmentResponse"];
const applicability = {
  current: "Current registration under the recorded policy at this read.",
  historical:
    "Historical evidence. Current source and policy eligibility could not be confirmed.",
  expired:
    "Expired validity, or the read time is outside the signed validity interval.",
  negative_or_insufficient:
    "Negative or insufficient evidence. The assessment did not establish a current registration.",
};
const labels = {
  receipt: "Receipt",
  producer: "Registration authority signature",
  log: "Log signature",
  checkpoint: "Checkpoint",
  inclusion: "Log inclusion",
  target: "Entry target",
  status: "Registration status",
};
const evidenceLabels = {
  entry: "Original entry",
  receipt: "Receipt",
  checkpoint: "Checkpoint",
  checkpointHistory: "Checkpoint history",
  statusToken: "Status token",
};
const detailLabels = {
  schemaVersion: "Receipt schema version",
  eventType: "Registration event",
  subject: "Registered subject",
  logId: "Log ID",
  registrationKeyFingerprint: "Registration key fingerprint",
  logKeyFingerprint: "Log key fingerprint",
  envelopeLogKeyFingerprint: "Envelope log key fingerprint",
};

export default function TrustAssessment(props: WorkspaceProps) {
  const guide = documentationGuideLink(
    props.runtime.documentationUrl,
    "ans-assessments",
  );
  // Permission/identity changes unmount the private reader and its object URLs.
  return (
    <>
      <Heading
        title="ANS assessments"
        icon="globe"
        description="Enter the source and job IDs to read a saved private assessment."
      />
      {!canReadTrust(props.access) ? (
        <p role="alert">
          Private assessments require job.read and source.read_evidence, plus
          sources.read scope when enforced.
        </p>
      ) : (
        <AssessmentLookup
          key={JSON.stringify(props.access) + props.id}
          {...props}
        />
      )}
      <details className="workspace-advanced">
        <summary>Find an assessment and read historical evidence</summary>
        <p>
          Find both IDs in the accepted assessment request. You can also copy
          the Source ID from Federation sources. There is no global assessment
          inventory.
        </p>
        <p>
          New requests depend on deployment configuration. Saved results may
          remain readable after source removal or policy changes, until the
          job's retention period ends.
        </p>
        {guide && (
          <a href={guide.href} target="_blank" rel="noopener noreferrer">
            {guide.label}
            <span className="sr-only"> (opens in a new tab)</span>
          </a>
        )}
      </details>
    </>
  );
}

function AssessmentLookup(props: WorkspaceProps) {
  const [sourceId, setSourceId] = useState(
    () => new URLSearchParams(location.search).get("sourceId") ?? "",
  );
  const [jobId, setJobId] = useState(props.id);
  const [selected, setSelected] = useState<{
    sourceId: string;
    jobId: string;
  }>();
  return (
    <>
      <p>
        Reading or refreshing an assessment is recorded in the audit history.
      </p>
      <form
        className="workspace-form"
        onSubmit={(event) => {
          event.preventDefault();
          if (!selected && trustID(sourceId) && trustID(jobId))
            setSelected({
              sourceId: sourceId.toLowerCase(),
              jobId: jobId.toLowerCase(),
            });
        }}
      >
        <Field
          label="Source ID"
          hint={
            sourceId && !trustID(sourceId)
              ? "Enter a valid Source ID in UUID format."
              : "Use the 36-character Source ID from the original request or source configuration."
          }
        >
          <input
            value={sourceId}
            pattern="[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
            aria-invalid={(!!sourceId && !trustID(sourceId)) || undefined}
            onChange={(e) => {
              setSourceId(e.target.value);
              setSelected(undefined);
            }}
            required
            maxLength={36}
          />
        </Field>
        <Field
          label="Assessment Job ID"
          hint={
            jobId && !trustID(jobId)
              ? "Enter a valid assessment Job ID in UUID format."
              : "Use the 36-character Job ID returned for this assessment. Synchronization jobs have no assessment result."
          }
        >
          <input
            value={jobId}
            pattern="[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
            aria-invalid={(!!jobId && !trustID(jobId)) || undefined}
            onChange={(e) => {
              setJobId(e.target.value);
              setSelected(undefined);
            }}
            required
            maxLength={36}
          />
        </Field>
        <div className="form-actions">
          <button
            type="submit"
            disabled={!!selected || !trustID(sourceId) || !trustID(jobId)}
          >
            {controlIcons["book"]} Read private assessment
          </button>
        </div>
      </form>
      {trustID(sourceId) && (
        <CopyReference label="Assessment Source ID" value={sourceId} />
      )}
      {trustID(jobId) && (
        <CopyReference label="Assessment Job ID" value={jobId} />
      )}
      {props.id && !trustID(sourceId) && (
        <p className="notice">
          This Job link has no valid Source context. Supply the original
          request's exact Source ID before reading evidence.
        </p>
      )}
      {selected && (
        <AssessmentRead
          key={`${selected.sourceId}/${selected.jobId}`}
          {...props}
          sourceId={selected.sourceId}
          jobId={selected.jobId}
        />
      )}
    </>
  );
}

function AssessmentRead(
  props: WorkspaceProps & { sourceId: string; jobId: string },
) {
  const read = useRead(
    props.sourceId + props.jobId,
    async (signal) => {
      const reply = await props.client.api.GET(
        "/api/v1/federation/sources/{sourceId}/trust-assessments/{jobId}",
        {
          params: { path: { sourceId: props.sourceId, jobId: props.jobId } },
          signal,
        },
      );
      const data = result(reply, trustResponse);
      if (data.result.sourceId !== props.sourceId)
        throw new Error("Assessment source does not match the request.");
      const bytes = evidenceBytes(data.result.evidence.entry);
      const hash = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
      const digest =
        "sha256:" +
        Array.from(hash, (byte) => byte.toString(16).padStart(2, "0")).join("");
      result(reply); // Recheck session lifetime after asynchronous checksum verification.
      if (digest !== data.result.exactEntryDigest)
        throw new Error("Assessment entry checksum does not match.");
      return data;
    },
    props.onFailure,
  );
  return (
    <section className="workspace-panel">
      <h2>Private assessment result</h2>
      <p>
        Source: <code>{props.sourceId}</code>. Job: <code>{props.jobId}</code>.
      </p>
      <button type="button" disabled={read.busy} onClick={read.refresh}>
        {controlIcons["refresh"]} Refresh private assessment
      </button>
      <p>
        Refresh checks whether this saved result still applies. To get new
        evidence, request another assessment from the source entry.
      </p>
      <ReadState
        {...read}
        error={
          read.problem?.status === 409
            ? "No completed assessment is available for this Job."
            : read.error
        }
      />
      {read.problem instanceof APIError && read.problem.status === 409 && (
        <p>
          The Job has not completed successfully. Pending, failed or cancelled
          Jobs have no completed assessment here. Check Job status if your
          access permits it.
        </p>
      )}
      {read.problem?.status === 404 && (
        <p>
          No retained assessment is available for these IDs under your current
          access.
        </p>
      )}
      {read.problem?.status === 503 && (
        <p>
          The private read could not complete, including its required disclosure
          audit. No evidence is shown. Retry explicitly when the service
          recovers.
        </p>
      )}
      {canReadTrustJob(props.access) && (
        <p>
          <a
            href={`${props.runtime.basePath}/jobs/${props.jobId}?sourceId=${encodeURIComponent(props.sourceId)}`}
          >
            View Job status
          </a>
        </p>
      )}
      {!canReadTrustJob(props.access) && (
        <p>
          Job status requires job.read and source.read_evidence, plus
          catalog.read scope when enforced. Private assessment reads instead
          require sources.read scope when enforced.
        </p>
      )}
      {read.data && (
        <AssessmentRecord
          key={read.data.result.assessment.checkedAt}
          data={read.data}
          jobId={props.jobId}
        />
      )}
    </section>
  );
}

function AssessmentRecord({ data, jobId }: { data: Response; jobId: string }) {
  const r = data.result;
  const a = r.assessment;
  return (
    <>
      <p role="status">
        {applicability[data.applicability]} This is the server's interpretation
        at the last explicit read; refresh to check again.
      </p>
      <p>
        A completed job may contain failed checks. Software safety, artifact
        integrity and rights remain Not assessed.
      </p>
      <p>
        Saving an evidence download uses these already verified original bytes.
        It sends no new evidence request and records no additional disclosure
        audit event.
      </p>
      {!data.current && (
        <p>
          For a current answer, check the recorded validity and separate
          outcomes. An expired, historical or negative result is not current
          approval. If fresh evidence is needed, return to the original entry
          selection and deliberately request a new assessment under current
          policy. Refresh only rereads this saved result.
        </p>
      )}
      <Facts
        rows={[
          [
            "Applicability",
            <StatusLabel
              state={data.applicability}
              tone={data.applicability === "current" ? "info" : "neutral"}
            />,
          ],
          ["Registration current when read", data.current ? "Yes" : "No"],
          [
            "Registration current when assessed",
            a.currentRegistration ? "Yes" : "No",
          ],
          ["Checked at", <DateValue value={a.checkedAt} />],
          [
            "Valid until",
            a.validUntil ? (
              <DateValue value={a.validUntil} />
            ) : (
              "No signed validity established"
            ),
          ],
        ]}
      />
      <details className="workspace-advanced">
        <summary>Assessment technical details</summary>
        <Facts
          rows={[
            ["Source revision", r.sourceRevision],
            ["Policy", r.policyName],
            ["Policy digest", <code>{r.policyDigest}</code>],
            ["Authority", r.authority],
            ["Profile", a.profile],
            ["Entry digest", <code>{r.exactEntryDigest}</code>],
            [
              "Native type and delivery",
              <>
                {r.mediaType}, {r.delivery}
              </>,
            ],
            ...(
              [
                "schemaVersion",
                "eventType",
                "subject",
                "logId",
                "registrationKeyFingerprint",
                "logKeyFingerprint",
                "envelopeLogKeyFingerprint",
              ] as const
            ).flatMap((field) =>
              a[field] !== undefined
                ? [[detailLabels[field], a[field]] as const]
                : [],
            ),
          ]}
        />
      </details>
      <details>
        <summary>What these checks mean</summary>
        <p>
          Signature checks use the operator's accepted public keys. The
          checkpoint and inclusion checks assess the signed log tree and whether
          it contains the registration. The entry target check compares the
          registration with the selected entry. Registration status describes
          signed evidence at assessment time, not a new check of the resource.
        </p>
        <p>
          Read each outcome separately and check its validity period and current
          applicability. These checks do not assess software safety, artifact
          integrity or legal rights.
        </p>
      </details>
      <ScrollableTable label="Registration assessment checks">
        <table className="workspace-table">
          <thead>
            <tr>
              {["Check", "Outcome", "Code"].map((label) => (
                <th key={label}>{label}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {trustChecks.map((name) => (
              <tr key={name}>
                <th scope="row">{labels[name]}</th>
                <td>
                  <StatusLabel
                    state={a[name].state}
                    tone={
                      a[name].state === "valid"
                        ? "success"
                        : a[name].state === "invalid"
                          ? "danger"
                          : "neutral"
                    }
                  />
                </td>
                <td>
                  <code>{a[name].code}</code>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </ScrollableTable>
      <h3>Exact retained evidence</h3>
      <AssessedEntry encoded={r.evidence.entry} />
      <p>
        Downloads contain the original bytes from this audited read. They are
        not reserialized JSON. Saving these bytes does not execute content or
        contact an evidence URL.
      </p>
      <div className="workspace-toolbar">
        {evidenceNames.map((name) =>
          r.evidence[name] ? (
            <EvidenceDownload
              key={name}
              name={name}
              encoded={r.evidence[name]}
              jobId={jobId}
            />
          ) : (
            <span key={name}>{evidenceLabels[name]}: not retained</span>
          ),
        )}
      </div>
    </>
  );
}

/** This viewer uses only entry bytes already checksum-verified by the audited read. */
function AssessedEntry({ encoded }: { encoded: string }) {
  const [open, setOpen] = useState(false);
  return (
    <details onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary>Inspect assessed original entry</summary>
      <p>
        This is the exact assessed entry, not a Catalog resource identity.
        Opening this viewer makes no evidence request.
      </p>
      {open && <EntryText encoded={encoded} />}
    </details>
  );
}

function EntryText({ encoded }: { encoded: string }) {
  const bytes = evidenceBytes(encoded);
  if (bytes.byteLength > 128 * 1024)
    return (
      <p>
        Entry exceeds the 128 KiB text preview limit. Save the original entry to
        inspect its exact bytes.
      </p>
    );
  let text: string;
  try {
    text = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(
      bytes,
    );
  } catch {
    return (
      <p>
        Entry is not valid UTF-8 text. Save the original entry to inspect its
        exact bytes.
      </p>
    );
  }
  return <CodeBlock text={text} original rawText />;
}

function EvidenceDownload({
  name,
  encoded,
  jobId,
}: {
  name: keyof typeof evidenceLabels;
  encoded: string;
  jobId: string;
}) {
  const [href, setHref] = useState("");
  useEffect(() => {
    const url = URL.createObjectURL(
      new Blob([evidenceBytes(encoded)], { type: "application/octet-stream" }),
    );
    setHref(url);
    return () => URL.revokeObjectURL(url);
  }, [encoded]);
  return (
    <a href={href} download={`ans-${jobId}-${name}.bin`}>
      {controlIcons["download"]} Save {evidenceLabels[name].toLowerCase()}
    </a>
  );
}
