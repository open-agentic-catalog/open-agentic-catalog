import { createHash } from "node:crypto";
import { isIP } from "node:net";
import { ClientError, record, safeURL } from "./config.js";
import { literal } from "./browse.js";

export const packageLimits = {
  archive: 16 * 1024 ** 2,
  expanded: 64 * 1024 ** 2,
  files: 1024,
  depth: 16,
  ratio: 100,
};
export interface PackageFile {
  path: string;
  size: number;
  digest: string;
  executable: boolean;
}
export type PackageSource =
  | { type: "git"; repository: string; commit: string; directory: string }
  | { type: "oci"; reference: string }
  | { type: "https"; url: string };
export interface SkillPlan {
  kind?: "plugin";
  archiveRoot?: string;
  name: string;
  mediaType: string;
  archiveDigest: string;
  archiveSize: number;
  files: PackageFile[];
  sources: PackageSource[];
  binding: string;
  identity: string;
}
export const packageHash = (value: Uint8Array | string): string =>
  "sha256:" + createHash("sha256").update(value).digest("hex");
export function packageFail(code = "invalid_skill_package"): never {
  throw new ClientError(code);
}
export function skillName(value: unknown): string {
  const name = literal(value, 64);
  if (!/^[a-z0-9]+(?:-[a-z0-9]+)*$/u.test(name)) return packageFail();
  return name;
}
export function packagePath(value: unknown): string {
  const path = literal(value, 1024),
    parts = path.split("/");
  if (
    Buffer.byteLength(path) > 1024 ||
    parts.length > packageLimits.depth + 1 ||
    parts.some(
      (p) =>
        Buffer.byteLength(p) > 255 ||
        !p ||
        p === "." ||
        p === ".." ||
        /[\\:*?"<>|]/u.test(p) ||
        /[. ]$/u.test(p) ||
        /^(con|prn|aux|nul|com[0-9]|lpt[0-9])(?:\.|$)/iu.test(p) ||
        p.toLowerCase() === ".git",
    )
  )
    return packageFail("unsafe_package_path");
  return path;
}
export function packageNumber(value: unknown, max: number, min = 0): number {
  if (
    !Number.isSafeInteger(value) ||
    (value as number) < min ||
    (value as number) > max
  )
    return packageFail();
  return value as number;
}
export function packageDigest(value: unknown): string {
  if (typeof value !== "string" || !/^sha256:[a-f0-9]{64}$/u.test(value))
    return packageFail();
  return value;
}
export function inventory(
  value: unknown,
  name: string,
  required = "SKILL.md",
): PackageFile[] {
  if (
    !Array.isArray(value) ||
    !value.length ||
    value.length > packageLimits.files
  )
    return packageFail();
  let total = 0;
  const paths = new Map<string, string>(),
    directories = new Map<string, string>();
  const files = value
    .map((entry) => {
      const v = record(entry),
        path = packagePath(v.path),
        parts = path.split("/");
      if (
        parts[0] !== name ||
        parts.length < 2 ||
        paths.has(path.normalize("NFC").toLowerCase()) ||
        directories.has(path.normalize("NFC").toLowerCase()) ||
        typeof v.executable !== "boolean"
      )
        return packageFail("unsafe_package_path");
      for (let index = 1; index < parts.length; index++) {
        const parent = parts.slice(0, index).join("/"),
          folded = parent.normalize("NFC").toLowerCase();
        if (
          paths.has(folded) ||
          (directories.has(folded) && directories.get(folded) !== parent)
        )
          return packageFail("unsafe_package_path");
        directories.set(folded, parent);
      }
      paths.set(path.normalize("NFC").toLowerCase(), path);
      const size = packageNumber(v.size, packageLimits.archive);
      total += size;
      if (total > packageLimits.expanded)
        return packageFail("package_size_limit");
      return {
        path,
        size,
        digest: packageDigest(v.digest),
        executable: v.executable,
      };
    })
    .sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
  if (!files.some((f) => f.path === `${name}/${required}`))
    return packageFail();
  return files;
}
export function skillPlan(value: unknown, binding: string): SkillPlan {
  const r = record(value),
    inspected = record(r.package),
    handoff = record(r.handoff);
  if (
    r.validationState !== "valid" ||
    inspected.structureState !== "passed" ||
    !["application/agent-skills+zip", "application/agent-skills+gzip"].includes(
      String(r.mediaType),
    )
  )
    return packageFail("complete_skill_package_required");
  const name = skillName(inspected.rootDirectory),
    files = inventory(inspected.files, name);
  if (
    inspected.fileCount !== files.length ||
    inspected.expandedSize !== files.reduce((n, f) => n + f.size, 0)
  )
    return packageFail();
  const sources: PackageSource[] = [];
  if (handoff.git !== undefined) {
    const g = record(handoff.git),
      repository = safeURL(g.repository, false, false),
      directory = packagePath(g.directory);
    if (
      new URL(repository).pathname.includes("%") ||
      directory.split("/").at(-1) !== name ||
      typeof g.commit !== "string" ||
      !/^(?:[a-f0-9]{40}|[a-f0-9]{64})$/u.test(g.commit)
    )
      return packageFail("immutable_package_source_required");
    sources.push({ type: "git", repository, commit: g.commit, directory });
  }
  if (handoff.oci !== undefined) {
    const reference = literal(record(handoff.oci).reference, 2048);
    const match =
      /^((?:[a-z0-9.-]+|\[[a-f0-9:]+\])(?::[0-9]+)?)\/([a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:\/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*)@sha256:[a-f0-9]{64}$/u.exec(
        reference,
      );
    if (!match) return packageFail("immutable_package_source_required");
    let registry: URL;
    try {
      registry = new URL("https://" + match[1]);
    } catch {
      return packageFail("immutable_package_source_required");
    }
    const host = registry.hostname.replace(/^\[|\]$/gu, "");
    if (
      registry.port === "0" ||
      (!isIP(host) &&
        (host.length > 253 ||
          host
            .split(".")
            .some(
              (label) => !/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/u.test(label),
            ) ||
          (!/:[0-9]+$/u.test(match[1]!) &&
            host !== "localhost" &&
            !host.includes("."))))
    )
      return packageFail("immutable_package_source_required");
    sources.push({ type: "oci", reference });
  }
  if (!sources.length) return packageFail("immutable_package_source_required");
  const plan = {
    name,
    mediaType: String(r.mediaType),
    archiveDigest: packageDigest(r.contentDigest),
    archiveSize: packageNumber(r.size, packageLimits.archive, 1),
    files,
    sources,
    binding,
  };
  return { ...plan, identity: packageHash(JSON.stringify(plan)) };
}
export function verifyPackage(
  plan: Pick<SkillPlan, "name" | "files">,
  content: Map<string, { bytes: Uint8Array; executable: boolean }>,
): void {
  if (content.size !== plan.files.length)
    return packageFail("package_integrity_failed");
  for (const file of plan.files) {
    const actual = content.get(file.path);
    if (
      !actual ||
      actual.bytes.length !== file.size ||
      actual.executable !== file.executable ||
      packageHash(actual.bytes) !== file.digest
    )
      return packageFail("package_integrity_failed");
  }
}
export function packageChanges(
  before: PackageFile[],
  after: PackageFile[],
): string[] {
  const old = new Map(before.map((f) => [f.path, f])),
    next = new Map(after.map((f) => [f.path, f]));
  return [...new Set([...old.keys(), ...next.keys()])]
    .sort()
    .flatMap((path) => {
      const a = old.get(path),
        b = next.get(path);
      return !a
        ? [`Add ${path}`]
        : !b
          ? [`Remove ${path}`]
          : a.digest !== b.digest || a.executable !== b.executable
            ? [`Change ${path}`]
            : [];
    });
}
