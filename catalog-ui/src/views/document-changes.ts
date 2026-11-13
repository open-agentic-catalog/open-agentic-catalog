import { diffChars, diffLines } from "diff";

// Detailed navigation is deliberately smaller than the 2 MiB exact-text view.
// Bound input, Myers edit distance, refinement work and rendered regions separately.
const maxText = 64 * 1024;
const maxLines = 2000;
const maxRegions = 100;
const context = 60;

export type ChangeExcerpt = {
  prefix: string;
  value: string;
  suffix: string;
  line: number;
  column: number;
};
export type DocumentChange = {
  before: ChangeExcerpt;
  after: ChangeExcerpt;
};
export type DocumentChanges =
  { available: true; changes: DocumentChange[] } | { available: false };

function excerpt(text: string, start: number, value: string): ChangeExcerpt {
  const preceding = text.slice(0, start).split("\n");
  const prefix = Array.from(text.slice(0, start)).slice(-context).join("");
  const following = text.slice(start + value.length);
  const suffix = Array.from(following).slice(0, context).join("");
  return {
    prefix: (prefix.length < start ? "..." : "") + prefix,
    value,
    suffix: suffix + (suffix.length < following.length ? "..." : ""),
    line: preceding.length,
    column: Array.from(preceding[preceding.length - 1]!).length + 1,
  };
}

/** Compare original text, never parsed or formatted JSON. No incomplete result escapes. */
export function documentChanges(
  before: string,
  after: string,
): DocumentChanges {
  if (before === after) return { available: true, changes: [] };
  if (
    [before, after].some(
      (text) => text.length > maxText || text.split("\n").length > maxLines,
    )
  )
    return { available: false };
  const lines = diffLines(before, after, { maxEditLength: 128, timeout: 50 });
  if (!lines) return { available: false };
  const changes: DocumentChange[] = [];
  let beforeOffset = 0;
  let afterOffset = 0;
  let refined = 0;
  for (let index = 0; index < lines.length;) {
    const line = lines[index]!;
    if (!line.added && !line.removed) {
      beforeOffset += line.value.length;
      afterOffset += line.value.length;
      index++;
      continue;
    }
    let removed = "";
    let added = "";
    while (
      index < lines.length &&
      (lines[index]!.added || lines[index]!.removed)
    ) {
      const part = lines[index++]!;
      if (part.removed) removed += part.value;
      else added += part.value;
    }
    refined += removed.length + added.length;
    if (removed.length + added.length > 8192 || refined > 32768)
      return { available: false };
    const characters = diffChars(removed, added, {
      maxEditLength: 256,
      timeout: 25,
    });
    if (!characters) return { available: false };
    for (let partIndex = 0; partIndex < characters.length;) {
      const part = characters[partIndex]!;
      if (!part.added && !part.removed) {
        beforeOffset += part.value.length;
        afterOffset += part.value.length;
        partIndex++;
        continue;
      }
      let oldValue = "";
      let newValue = "";
      while (
        partIndex < characters.length &&
        (characters[partIndex]!.added || characters[partIndex]!.removed)
      ) {
        const changed = characters[partIndex++]!;
        if (changed.removed) oldValue += changed.value;
        else newValue += changed.value;
      }
      changes.push({
        before: excerpt(before, beforeOffset, oldValue),
        after: excerpt(after, afterOffset, newValue),
      });
      if (changes.length > maxRegions) return { available: false };
      beforeOffset += oldValue.length;
      afterOffset += newValue.length;
    }
  }
  return { available: true, changes };
}

/** Visible whitespace belongs only to excerpts; the original view stays unchanged. */
export function visibleChangeText(text: string): string {
  return text.replace(/[ \t\r\n\uFEFF]/g, (value) => {
    switch (value) {
      case " ":
        return "\u00b7";
      case "\t":
        return "\\t";
      case "\r":
        return "\\r";
      case "\n":
        return "\\n\n";
      default:
        return "[BOM]";
    }
  });
}
