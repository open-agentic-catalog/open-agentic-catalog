// Contract-shaped subjects shared by authoring continuation units.
import type { VersionBundle } from "../../src/views/authoring-save";
import { newAgentCard } from "../../src/views/agent-card";
import { version, stamp } from "./screens";
import { evidence, versionRevision, representation } from "./responses";
export const digest = "sha256:" + "a".repeat(64);
export const native = {
  ...newAgentCard("1.0.0"),
  name: "Order Agent",
  description: "Explain an order.",
  supportedInterfaces: [
    {
      url: "https://agents.example.test/orders",
      protocolBinding: "JSONRPC",
      protocolVersion: "1.0",
    },
  ],
  skills: [
    {
      id: "orders",
      name: "Explain an order",
      description: "Explain an order.",
      tags: ["orders"],
    },
  ],
  extra: { keep: ["second", "first"] },
};
export const bundle: VersionBundle = {
  version: { ...version, candidateRevisionId: versionRevision.id },
  revision: {
    ...versionRevision,
    contentSetDigest: digest,
    createdBy: "author",
    createdAt: stamp,
    representations: [
      {
        ...representation,
        mediaType: "application/a2a-agent-card+json",
        profileId: "",
        role: "primary",
        validationState: "valid",
        data: native,
        normalizedFacts: {
          name: "Order Agent",
          description: "Explain an order.",
        },
      },
    ],
  },
  evidence: {
    ...evidence,
    subjectDigest: digest,
    state: "complete",
    createdAt: stamp,
    trust: { state: "not_assessed" },
    legalRights: { state: "not_assessed" },
  },
};
