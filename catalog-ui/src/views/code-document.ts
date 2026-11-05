// A bounded JSON token walk preserves original literals while deriving display-only formatting.
type CodeToken = { text: string; kind: string };
export type JSONDocument = { original: CodeToken[]; formatted: CodeToken[] };

// Bound both DOM token count and whitespace expansion. Never round numbers,
// collapse duplicate keys or interpret markup while presenting retained text.
export function jsonDocument(source: string): JSONDocument | undefined {
  if (source.length > 128 * 1024) return;
  try {
    JSON.parse(source);
  } catch {
    return;
  }
  const parts =
    source.match(
      /"(?:[^"\\]|\\.)*"|\s+|true|false|null|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|[{}[\],:]/g,
    ) || [];
  if (parts.length > 6000) return;
  const significant = parts.filter((part) => !/^\s+$/.test(part));
  const tokens = significant.map((text, index) => ({
    text,
    kind: text.startsWith('"')
      ? significant[index + 1] === ":"
        ? "key"
        : "string"
      : /^[\d-]/.test(text)
        ? "number"
        : /^(true|false|null)$/.test(text)
          ? "literal"
          : "punctuation",
  }));
  let index = 0;
  // tokens is the one-to-one projection of the non-whitespace parts above.
  const original = parts.map((text) =>
    /^\s+$/.test(text) ? { text, kind: "space" } : tokens[index++]!,
  );
  const formatted: CodeToken[] = [];
  let depth = 0;
  let length = 0;
  const space = (text: string) => formatted.push({ text, kind: "space" });
  for (const [i, token] of tokens.entries()) {
    const closing = token.text === "}" || token.text === "]";
    if (closing) {
      depth--;
      if (tokens[i - 1]?.text !== "{" && tokens[i - 1]?.text !== "[")
        space("\n" + "  ".repeat(depth));
    }
    formatted.push(token);
    if (token.text === "{" || token.text === "[") {
      if (++depth > 40) return;
      if (tokens[i + 1]?.text !== "}" && tokens[i + 1]?.text !== "]")
        space("\n" + "  ".repeat(depth));
    } else if (token.text === ",") space("\n" + "  ".repeat(depth));
    else if (token.text === ":") space(" ");
    length += token.text.length + depth * 2 + 1;
    if (length > 512 * 1024) return;
  }
  return { original, formatted };
}
