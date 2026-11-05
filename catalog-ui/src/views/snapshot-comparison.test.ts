import { expect, it } from "vitest";
import { snapshotChanges } from "./snapshot-comparison";

const entry = {
  identifier: "urn:example:agent",
  type: "application/json",
  description: "Original",
  metadata: { digest: "sha256:old" },
};
it("compares exact duplicate multisets without treating object order as a change", () => {
  const other = { ...entry, version: "" };
  expect(
    snapshotChanges(
      [entry, entry, other],
      [
        other,
        {
          metadata: { digest: "sha256:old" },
          description: "Original",
          type: "application/json",
          identifier: "urn:example:agent",
        },
        entry,
      ],
    ),
  ).toEqual([]);
  expect(snapshotChanges([], [])).toEqual([]);
});
it("keeps metadata changes and separate duplicate occurrences without changing the inputs", () => {
  const changed = {
    ...entry,
    description: "Updated",
    metadata: {
      digest: "sha256:new",
      reference: "https://example.invalid/inert",
    },
  };
  const before = Object.freeze([entry, entry]);
  const after = Object.freeze([changed, entry, changed]);
  const changes = snapshotChanges([...before], [...after]);
  expect(changes.map((item) => item.change)).toEqual(["Changed", "Added"]);
  expect(changes[0]).toMatchObject({ before: entry, after: changed });
  expect(changes[1]).toMatchObject({ before: undefined, after: changed });
  expect(before).toEqual([entry, entry]);
  expect(after).toEqual([changed, entry, changed]);
});
it("keeps absent and empty versions and different media types distinct", () => {
  const after = [
    { ...entry, version: "" },
    { ...entry, type: "text/plain" },
  ];
  const changes = snapshotChanges([entry], after);
  expect(changes.map((item) => item.change)).toEqual([
    "Removed",
    "Added",
    "Added",
  ]);
  expect(changes.map((item) => item.identity)).toEqual([
    '["urn:example:agent",false,null,"application/json"]',
    '["urn:example:agent",true,"","application/json"]',
    '["urn:example:agent",false,null,"text/plain"]',
  ]);
});
it("compares ordered content and independently keyed exclusions", () => {
  const before = { ...entry, tags: ["a", "b"] };
  const after = { ...entry, tags: ["b", "a"] };
  expect(snapshotChanges([before], [after])).toMatchObject([
    { change: "Changed", before, after },
  ]);
  const old = { resourceId: "first", identifier: "same", reasons: ["blocked"] };
  const next = { ...old, resourceId: "second" };
  expect(
    snapshotChanges([old], [next], (item) =>
      JSON.stringify([item.resourceId, item.identifier]),
    ).map((item) => item.change),
  ).toEqual(["Removed", "Added"]);
});
it("aligns unmatched duplicate occurrences in their retained order", () => {
  const before = ["old-a", "old-b", "old-c"].map((description) => ({
    ...entry,
    description,
  }));
  const after = ["new-a", "new-b", "new-a"].map((description) => ({
    ...entry,
    description,
  }));
  expect(snapshotChanges(before, after).map((change) => change.after)).toEqual(
    after,
  );
});
