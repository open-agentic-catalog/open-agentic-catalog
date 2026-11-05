// Guided fields use a small, unambiguous YAML subset. The backend owns validation.
export type SkillDocument = {
  name: string;
  description: string;
  body: string;
  license?: string;
  compatibility?: string;
  "allowed-tools"?: string;
  metadata?: [string, string][];
};
const fields = [
  "name",
  "description",
  "license",
  "compatibility",
  "allowed-tools",
] as const;
const utf8 = new TextEncoder();
const quote = (value: string) =>
  JSON.stringify(value).replace(
    /[\u007f-\u009f\u2028\u2029]/gu,
    (character) =>
      `\\u${character.charCodeAt(0).toString(16).padStart(4, "0")}`,
  );
const blank = (value: string) => !value.replace(/\p{White_Space}/gu, "");
const safeText = (value: string) =>
  !value.includes("\0") && !/[\uD800-\uDFFF]/u.test(value);

/** Limits count decoded characters, not YAML spelling or UTF-16 code units. */
export function skillFieldError(field: string, value: string): string {
  if (!safeText(value))
    return "Use Unicode text without NUL characters or incomplete surrogate pairs.";
  if (field === "name" && !/^[a-z0-9]+(?:-[a-z0-9]+)*$/u.test(value))
    return "Use lowercase letters, digits and single hyphens between words.";
  if (field === "name" && value.length > 64)
    return "Use at most 64 characters.";
  if (field === "description" && (blank(value) || [...value].length > 1024))
    return "Enter a description of 1 to 1024 characters.";
  if (
    field === "compatibility" &&
    value &&
    (blank(value) || [...value].length > 500)
  )
    return "Use 1 to 500 characters, or clear this optional field.";
  return "";
}

/** Quote every scalar so YAML never treats numeric, boolean or punctuation text as another type. */
export function writeSkillDocument(document: SkillDocument): string {
  const lines: string[] = [];
  for (const field of fields) {
    const value = document[field];
    if (value !== undefined) lines.push(`${field}: ${quote(value)}`);
  }
  if (document.metadata) {
    const keys = new Set<string>();
    if (!document.metadata.length) lines.push("metadata: {}");
    else {
      lines.push("metadata:");
      for (const [key, value] of document.metadata) {
        if (keys.has(key))
          throw new Error("Use a different key for each metadata entry.");
        keys.add(key);
        lines.push(`  ${quote(key)}: ${quote(value)}`);
      }
    }
  }
  const content = `---\n${lines.join("\n")}\n---\n${document.body}`;
  if (!safeText(content) || utf8.encode(content).length > 500_000)
    throw new Error(
      "Keep SKILL.md within 500,000 UTF-8 bytes, without NUL characters or incomplete surrogate pairs.",
    );
  return content;
}

// This is recognition for guided editing, not a general YAML parser. Anything
// outside this subset stays untouched in Advanced, including block scalars,
// aliases, tags, flow maps, unknown fields and ambiguous scalar spellings.
function scalar(source: string): string {
  if (source.startsWith('"')) {
    // Escaped backslashes spell literal text, not YAML Unicode escapes.
    if (/\\u[dD][89a-fA-F][0-9a-fA-F]{2}/u.test(source.replace(/\\\\/gu, "")))
      throw new Error("unsupported YAML surrogate escape");
    const parsed: unknown = JSON.parse(source);
    if (typeof parsed === "string" && safeText(parsed)) return parsed;
  } else if (/^'(?:[^']|'')*'$/u.test(source)) {
    return source.slice(1, -1).replaceAll("''", "'");
  } else if (
    /^[\p{L}\p{N}][\p{L}\p{N} ._()/,-]*$/u.test(source) &&
    !/^(?:true|false|null|yes|no|on|off|~|[-+]?\d.*)$/iu.test(source)
  ) {
    return source;
  }
  throw new Error("unsupported YAML");
}

export function readSkillDocument(content: string): {
  document?: SkillDocument;
  reason?: string;
} {
  if (!content) return { document: { name: "", description: "", body: "" } };
  const reason =
    "This YAML needs the Advanced SKILL.md editor. Your input is unchanged. Guided fields support plain text and single-line quoted values; use Advanced for block scalars, aliases, tags or other YAML forms.";
  try {
    if (!safeText(content) || utf8.encode(content).length > 500_000)
      return { reason };
    const header = /^---\r?\n([\s\S]*?)^---(?:\r?\n|$)/mu.exec(content);
    if (
      !header ||
      header.index !== 0 ||
      /[\u007f-\u009f\u2028\u2029]/u.test(header[1]!)
    )
      return { reason };
    const document: SkillDocument = {
      name: "",
      description: "",
      body: content.slice(header[0].length),
    };
    const seen = new Set<string>();
    const metadataKeys = new Set<string>();
    let metadata = false;
    let metadataBlock = false;
    for (const line of header[1]!.split(/\r?\n/u)) {
      if (!line.trim() || /^ *#/u.test(line)) continue;
      if (metadata && line.startsWith("  ")) {
        const pair =
          /^ {2}("(?:[^"\\]|\\.)*"|'(?:[^']|'')*'|[^\s:][^:]*): +(.*)$/u.exec(
            line,
          );
        if (!pair) return { reason };
        const key = scalar(pair[1]!.trim());
        if (metadataKeys.has(key)) return { reason };
        metadataKeys.add(key);
        document.metadata!.push([key, scalar(pair[2]!.trim())]);
        continue;
      }
      const pair = /^([a-z-]+):(?: +(.*))?$/u.exec(line);
      if (!pair || seen.has(pair[1]!)) return { reason };
      const key = pair[1]!;
      seen.add(key);
      metadata = key === "metadata" && !pair[2]?.trim();
      if (key === "metadata") {
        if (pair[2] && pair[2].trim() !== "{}") return { reason };
        metadataBlock = metadata;
        document.metadata = [];
      } else {
        if (!fields.includes(key as (typeof fields)[number])) return { reason };
        document[key as (typeof fields)[number]] = scalar(
          (pair[2] ?? "").trim(),
        );
      }
    }
    if (metadataBlock && !document.metadata!.length) return { reason };
    if (document.compatibility === "") return { reason };
    // A non-empty import must actually declare both required fields.
    return seen.has("name") && seen.has("description")
      ? { document }
      : { reason };
  } catch {
    return { reason };
  }
}
