import { expect, it } from "vitest";
import * as checks from "./responses";

const local = {
  origin: "local",
  releaseId: "release",
  versionRevisionId: "revision",
};
const foreign = {
  origin: "foreign",
  kind: "agent",
  sourceId: "source",
  presentationGeneration: 1,
  identifier: "urn:air:example.com:default:agent",
  versionPresent: true,
  version: "",
};
it("validates complete saved reference identities without conflating absent and empty versions", () => {
  for (const valid of [
    local,
    foreign,
    { ...foreign, version: undefined },
    { ...foreign, versionPresent: false, version: undefined },
  ])
    expect(checks.PersonalReference(valid)).toBe(true);
  for (const invalid of [
    null,
    [],
    {},
    "text",
    { ...local, releaseId: "../other" },
    { ...local, versionRevisionId: null },
    { ...local, url: "https://arbitrary.example" },
    { ...foreign, kind: "unknown" },
    { ...foreign, sourceId: "../other" },
    { ...foreign, presentationGeneration: 0 },
    { ...foreign, identifier: 4 },
    { ...foreign, identifier: "x".repeat(513) },
    { ...foreign, versionPresent: undefined },
    { ...foreign, version: 4 },
    { ...foreign, version: "x".repeat(129) },
    { ...foreign, versionPresent: false, version: "1.0" },
    { ...foreign, releaseId: "other" },
  ])
    expect(checks.PersonalReference(invalid)).toBe(false);
});
it("accepts supported criteria and rejects persistence of continuation or arbitrary destinations", () => {
  const complete = {
    text: "query",
    origins: ["local"],
    namespaceId: "00000000-0000-4000-8000-000000000001",
    kinds: ["agent", "skill"],
    tags: ["support"],
    categories: ["tools"],
    license: "Apache-2.0",
    publisher: "publisher",
    visibility: "tenant",
  };
  expect(checks.PersonalCriteria(complete)).toBe(true);
  expect(checks.PersonalCriteria({})).toBe(true);
  for (const invalid of [
    null,
    [],
    { text: 1 },
    { text: "x".repeat(501) },
    { text: "q", cursor: "transient" },
    { origins: ["unknown"] },
    { namespaceId: "../other" },
    { kinds: ["unknown"] },
    { tags: [1] },
    { categories: [false] },
    { license: 1 },
    { publisher: [] },
    { visibility: "unknown" },
  ])
    expect(checks.PersonalCriteria(invalid)).toBe(false);
});
it("checks owner item fields and consumed work references", () => {
  const item = {
    type: "resource",
    label: "A label",
    reference: local,
    id: "item",
    revision: 1,
    createdAt: "2026-10-01T12:00:00Z",
    updatedAt: "2026-10-01T12:00:00Z",
  };
  expect(checks.PersonalItem(item)).toBe(true);
  for (const invalid of [
    null,
    { ...item, label: "" },
    { ...item, label: " " },
    { ...item, label: "x".repeat(121) },
    { ...item, label: "line\nbreak" },
    { ...item, label: String.fromCodePoint(128) },
    { ...item, type: "unknown" },
    { ...item, search: { text: "q" } },
    { ...item, id: "../item" },
    { ...item, revision: 0 },
    { ...item, createdAt: "bad" },
    { ...item, updatedAt: "bad" },
  ])
    expect(checks.PersonalItem(invalid)).toBe(false);
  const work = {
    id: "submission:1",
    type: "submission",
    resourceId: "resource",
    namespaceId: "ns",
    kind: "agent",
    state: "submitted",
    canonicalIdentifier: "urn:air:example.com:default:agent",
    displayName: "Name",
    versionId: "version",
    versionRevisionId: "revision",
    listingId: "listing",
    listingRevisionId: "listing-revision",
    submissionId: "submission",
    currentCandidateRevisionId: "candidate",
  };
  expect(checks.PersonalWorkItem(work)).toBe(true);
  for (const invalid of [
    null,
    { ...work, id: "../item" },
    { ...work, resourceId: [] },
    { ...work, namespaceId: null },
    { ...work, kind: "unknown" },
    { ...work, type: "unknown" },
    { ...work, state: "unknown" },
    { ...work, versionId: 4 },
    { ...work, canonicalIdentifier: 4 },
    { ...work, displayName: [] },
    { ...work, listingRevisionId: "../private" },
  ])
    expect(checks.PersonalWorkItem(invalid)).toBe(false);
  for (const invalid of [
    null,
    { id: "../item", revision: 1 },
    { id: "item", revision: 0 },
  ])
    expect(checks.PersonalReceipt(invalid)).toBe(false);
  expect(
    checks.PersonalResolution({ status: "available", reference: local }),
  ).toBe(true);
  expect(
    checks.PersonalResolution({
      status: "available",
      reference: local,
      indexed: {},
    }),
  ).toBe(false);
  expect(checks.PersonalResolution(null)).toBe(false);
  expect(checks.PersonalResolution({ status: "unknown" })).toBe(false);
});
