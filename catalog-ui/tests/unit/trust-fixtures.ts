// Synthetic retained bytes for isolated screen and controlled browser checks.
import { createHash } from "node:crypto";
import { Buffer } from "node:buffer";
import type { components } from "../../src/api/schema";

export const trustSource = "00000000-0000-7000-8000-000000000001";
export const trustJob = "00000000-0000-7000-8000-000000000002";
export const trustEntry =
  ' { "identifier":"urn:air:peer.invalid:agents:helper", "type":"application/a2a-agent-card+json", "data":{"name":"Helper"} }\n';
export const evidenceSamples = {
  entry: Buffer.from(trustEntry),
  // Synthetic COSE-shaped bytes, including invalid UTF-8; this is not a signed receipt.
  receipt: Buffer.from([0xd2, 0x84, 0x40, 0xa0, 0x43, 0x00, 0xff, 0x80, 0x40]),
  checkpoint: Buffer.from("checkpoint\r\n"),
  checkpointHistory: Buffer.from("history\u0000bytes"),
  statusToken: Buffer.from("status\n"),
};

export function trustSample(): components["schemas"]["TrustAssessmentResponse"] {
  const entryDigest = createHash("sha256").update(trustEntry).digest("hex");
  const valid = { state: "valid" as const, code: "verified" };
  return {
    current: true,
    applicability: "current",
    result: {
      sourceId: trustSource,
      sourceRevision: 7,
      policyName: "operator-ans",
      policyDigest: `sha256:${"b".repeat(64)}`,
      authority: "peer.invalid",
      exactEntryDigest: `sha256:${entryDigest}`,
      mediaType: "application/a2a-agent-card+json",
      delivery: "data",
      softwareTrust: "not_assessed",
      artifactIntegrity: "not_assessed",
      rights: "not_assessed",
      assessment: {
        profile: "ans-registration-v1",
        policyDigest: "b".repeat(64),
        entryDigest,
        checkedAt: "2026-10-11T01:00:00Z",
        validUntil: "2026-10-12T01:00:00Z",
        receipt: { ...valid },
        producer: { ...valid },
        log: { ...valid },
        checkpoint: { ...valid },
        inclusion: { ...valid },
        target: { ...valid },
        status: { ...valid },
        currentRegistration: true,
        schemaVersion: "2",
        eventType: "AGENT_REGISTERED",
        subject: "https://peer.invalid/card",
        logId: "owned-log",
        registrationKeyFingerprint: "c".repeat(64),
        logKeyFingerprint: "d".repeat(64),
        envelopeLogKeyFingerprint: "e".repeat(64),
      },
      evidence: {
        profile: "ans-registration-v1",
        ...Object.fromEntries(
          Object.entries(evidenceSamples).map(([name, bytes]) => [
            name,
            bytes.toString("base64"),
          ]),
        ),
      } as components["schemas"]["TrustEvidenceBundle"],
    },
  };
}
