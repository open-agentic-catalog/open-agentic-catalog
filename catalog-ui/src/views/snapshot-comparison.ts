import { semanticValue } from "./RevisionComparison";

type Item = Record<string, unknown>;
export type SnapshotChange = {
  identity: string;
  change: "Added" | "Removed" | "Changed";
  before: Item | undefined;
  after: Item | undefined;
};

/** Compare multisets: preserve duplicates and absent/empty versions, ignoring object-key order. */
export function snapshotChanges(
  before: Item[],
  after: Item[],
  key: (item: Item) => string = (item) =>
    JSON.stringify([
      item.identifier,
      Object.hasOwn(item, "version"),
      item.version,
      item.type,
    ]),
): SnapshotChange[] {
  const groups = new Map<string, { before: Item[]; after: Item[] }>();
  for (const [side, items] of [
    ["before", before],
    ["after", after],
  ] as const) {
    for (const item of items) {
      const identity = key(item);
      const group = groups.get(identity) ?? { before: [], after: [] };
      group[side].push(item);
      groups.set(identity, group);
    }
  }
  const changes: SnapshotChange[] = [];
  for (const [identity, group] of groups) {
    const exact = new Map<string, number[]>();
    for (const [index, item] of group.after.entries()) {
      const text = JSON.stringify(semanticValue(item));
      const copies = exact.get(text) ?? [];
      copies.push(index);
      exact.set(text, copies);
    }
    const removed: Item[] = [];
    const matched = new Set<number>();
    for (const item of group.before) {
      const copies = exact.get(JSON.stringify(semanticValue(item)));
      const index = copies?.pop();
      if (index !== undefined) matched.add(index);
      else removed.push(item);
    }
    const remaining = group.after.filter((_item, index) => !matched.has(index));
    for (let i = 0; i < Math.max(removed.length, remaining.length); i++) {
      const a = removed[i],
        b = remaining[i];
      changes.push({
        identity,
        change:
          a === undefined ? "Added" : b === undefined ? "Removed" : "Changed",
        before: a,
        after: b,
      });
    }
  }
  return changes;
}
