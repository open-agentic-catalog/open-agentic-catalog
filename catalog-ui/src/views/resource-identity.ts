// Resource identity follows Catalog creation rules; AIR components never come
// from the administrative Namespace or establish publisher-domain control.
import { characterText } from "./input-validation";

function airSegment(value: string): boolean {
  return (
    /^[A-Za-z0-9_.-]{1,64}$/u.test(value) && value !== "." && value !== ".."
  );
}

const publisherMessage =
  "Use a dotted publisher domain without a scheme, port or IP address.";
const segmentMessage =
  "AIR namespace segments and resource name need 1 to 64 ASCII letters, digits, dots, underscores or hyphens; dot and double-dot segments are not allowed.";

function publisherDomain(value: string): boolean {
  if (value.length > 253 || !value.includes(".")) return false;
  if (
    value
      .split(".")
      .some(
        (label) =>
          !/^[a-z0-9-]{1,63}$/u.test(label) ||
          label.startsWith("-") ||
          label.endsWith("-"),
      )
  )
    return false;
  // Go's net.ParseIP rejects decimal IPv4 addresses, but not DNS names with
  // leading-zero labels or values outside the IPv4 octet range.
  return !(
    /^(?:0|[1-9][0-9]{0,2})(?:\.(?:0|[1-9][0-9]{0,2})){3}$/u.test(value) &&
    value.split(".").every((part) => Number(part) <= 255)
  );
}

/** Normalize creation input without changing opaque spelling or AIR segment case. */
export function canonicalResourceIdentifier(input: string): string {
  const value = input.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, "");
  characterText(value, "Canonical identifier", 512, 1);
  if (/\p{Surrogate}/u.test(value))
    throw new Error("Canonical identifier must contain valid Unicode text.");
  if (!/^urn:air:/iu.test(value)) return value;
  if (new TextEncoder().encode(value).length > 512)
    throw new Error("An AIR identifier must fit 512 bytes.");
  const parts = value.split(":");
  const publisher = parts[2]!.toLowerCase().replace(/\.$/u, "");
  if (!publisherDomain(publisher)) throw new Error(publisherMessage);
  const segments = parts.slice(3);
  if (!segments.length || segments.some((part) => !airSegment(part)))
    throw new Error(segmentMessage);
  return `urn:air:${publisher}:${segments.join(":")}`;
}

/** An empty AIR namespace produces a terminal-only identifier without an empty segment. */
export function buildAIRIdentifier({
  publisher,
  namespace,
  name,
}: {
  publisher: string;
  namespace: string;
  name: string;
}): string {
  if (!publisherDomain(publisher.toLowerCase().replace(/\.$/u, "")))
    throw new Error(publisherMessage);
  if (
    !airSegment(name) ||
    (namespace !== "" && namespace.split(":").some((part) => !airSegment(part)))
  )
    throw new Error(segmentMessage);
  return canonicalResourceIdentifier(
    `urn:air:${publisher}:${namespace ? `${namespace}:` : ""}${name}`,
  );
}

/** A suggestion is applied deliberately; it never supplies domain or Namespace data. */
export function suggestResourceName(itemName: string): string {
  return itemName
    .normalize("NFKD")
    .replace(/\p{Mark}/gu, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/gu, "-")
    .replace(/^-+|-+$/gu, "")
    .slice(0, 64)
    .replace(/-+$/u, "");
}
