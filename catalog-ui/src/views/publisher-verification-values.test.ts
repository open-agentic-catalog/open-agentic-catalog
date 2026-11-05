import { expect, it } from "vitest";
import {
  publisherVerificationChallenge as challenge,
  publisherVerificationDomain as domain,
  publisherVerificationMode as mode,
  publisherVerificationStatus as status,
} from "./publisher-verification-values";
const stamp = "2026-10-25T12:00:00Z";
const base = {
  domain: "example.test",
  mode: "optional",
  state: "not_verified",
  challengeState: "not_issued",
};
it("consumes the configured mode without claiming publisher authority", () => {
  for (const value of ["disabled", "optional", "required_for_approval"])
    expect(mode({ governance: { publisherDomainVerification: value } })).toBe(
      true,
    );
  for (const value of [
    null,
    {},
    { governance: null },
    { governance: {} },
    { governance: { publisherDomainVerification: "future" } },
  ])
    expect(mode(value)).toBe(false);
});
it("requires the fields used to present proof and challenge status", () => {
  expect(status(base)).toBe(true);
  for (const value of [
    null,
    {},
    { ...base, domain: "" },
    { ...base, domain: "a".repeat(222) },
    { ...base, mode: "future" },
    { ...base, state: "future" },
    { ...base, challengeState: "future" },
    { ...base, challengeId: [] },
    { ...base, lastCheckChallengeId: [] },
    ...[
      "verifiedAt",
      "expiresAt",
      "challengeIssuedAt",
      "challengeExpiresAt",
      "lastCheckAt",
    ].map((field) => ({ ...base, [field]: "invalid" })),
    { ...base, state: "verified" },
    { ...base, challengeState: "pending" },
    { ...base, lastCheckOutcome: "matched" },
    {
      ...base,
      lastCheckOutcome: "future",
      lastCheckAt: stamp,
      lastCheckChallengeId: "challenge-1",
    },
  ])
    expect(status(value), JSON.stringify(value)).toBe(false);
  expect(
    status({ ...base, state: "verified", verifiedAt: stamp, expiresAt: stamp }),
  ).toBe(true);
  expect(
    status({
      ...base,
      state: "expired",
      verifiedAt: stamp,
      expiresAt: stamp,
      challengeState: "consumed",
      challengeId: "challenge-1",
      challengeIssuedAt: stamp,
      challengeExpiresAt: stamp,
      lastCheckAt: stamp,
      lastCheckOutcome: "matched",
      lastCheckChallengeId: "challenge-1",
    }),
  ).toBe(true);
});
it("validates private instructions against the exact challenge and domain", () => {
  const pending = {
    ...base,
    challengeState: "pending",
    challengeId: "challenge-1",
    challengeIssuedAt: stamp,
    challengeExpiresAt: stamp,
  };
  const good = {
    status: pending,
    challengeId: "challenge-1",
    name: "_open-agentic-catalog-challenge.example.test.",
    value: "open-agentic-catalog-verification=" + "a".repeat(43),
    expiresAt: stamp,
  };
  expect(challenge(good)).toBe(true);
  expect(challenge({ status: base })).toBe(true);
  for (const value of [
    null,
    { status: {} },
    { status: base, name: "extra" },
    { status: base, challengeId: "extra" },
    { status: base, expiresAt: stamp },
    { ...good, status: { ...pending, mode: "disabled" } },
    { ...good, status: { ...pending, challengeState: "expired" } },
    { ...good, challengeId: "other" },
    { ...good, name: "wrong" },
    { ...good, name: 5 },
    { ...good, value: 5 },
    { ...good, value: "wrong" },
    { ...good, expiresAt: "invalid" },
    { ...good, expiresAt: "2026-10-26T12:00:00Z" },
  ])
    expect(challenge(value), JSON.stringify(value)).toBe(false);
});

it("distinguishes declared DNS names from IP literals and overlong names", () => {
  for (const name of [
    "example.test",
    "xn--bcher-kva.test",
    "256.example",
    "01.example",
  ])
    expect(domain(name)).toBe(true);
  for (const name of [
    "127.0.0.1",
    "localhost",
    "*.example.test",
    "a".repeat(222),
  ])
    expect(domain(name)).toBe(false);
});
