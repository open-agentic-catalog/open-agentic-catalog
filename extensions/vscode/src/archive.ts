import { crc32, inflateRawSync } from "node:zlib";
import { ClientError } from "./config.js";
import {
  packageFail,
  packageLimits,
  packagePath,
  verifyPackage,
  type SkillPlan,
} from "./packages.js";

export type PackageContent = Map<
  string,
  { bytes: Uint8Array; executable: boolean }
>;
const utf8 = (value: Uint8Array) =>
  new TextDecoder("utf-8", { fatal: true }).decode(value);
/** Strict portable ZIP/ustar subset. Never extract through an external tool. */
export function unpackPackage(
  plan: SkillPlan,
  value: Uint8Array,
): PackageContent {
  try {
    const raw = Buffer.from(value);
    if (!raw.length || raw.length > packageLimits.archive)
      return packageFail("package_size_limit");
    const content: PackageContent = new Map(),
      seen = new Map<string, string>(),
      files = new Set<string>(),
      explicit = new Set<string>();
    let count = 0,
      total = 0;
    const entry = (
      name: string,
      directory: boolean,
      bytes: Uint8Array,
      mode: number,
    ) => {
      if (++count > packageLimits.files)
        return packageFail("package_size_limit");
      const rawPath = packagePath(
        directory && name.endsWith("/") ? name.slice(0, -1) : name,
      );
      const root = plan.archiveRoot;
      if (
        root !== undefined &&
        root &&
        rawPath !== root &&
        !rawPath.startsWith(root + "/")
      )
        return packageFail("unsafe_package_path");
      const relative = root ? rawPath.slice(root.length + 1) : rawPath;
      const path =
        root === undefined
          ? rawPath
          : directory && rawPath === root
            ? plan.name
            : packagePath(plan.name + "/" + relative);
      if (
        path.split("/")[0] !== plan.name ||
        (seen.has(path.toLowerCase()) &&
          (!directory || files.has(path.toLowerCase())))
      )
        return packageFail("unsafe_package_path");
      for (let i = 1; i <= path.split("/").length; i++) {
        const parent = path.split("/").slice(0, i).join("/"),
          folded = parent.toLowerCase();
        if (
          (seen.has(folded) && seen.get(folded) !== parent) ||
          (i < path.split("/").length && files.has(folded))
        )
          return packageFail("unsafe_package_path");
        seen.set(folded, parent);
      }
      if (explicit.has(path.toLowerCase()))
        return packageFail("unsafe_package_path");
      explicit.add(path.toLowerCase());
      if (!directory) files.add(path.toLowerCase());
      if (directory) {
        if (bytes.length) return packageFail();
        return;
      }
      if (!path.includes("/")) return packageFail();
      total += bytes.length;
      if (
        bytes.length > packageLimits.archive ||
        total > packageLimits.expanded ||
        total > raw.length * packageLimits.ratio
      )
        return packageFail("package_size_limit");
      content.set(path, { bytes, executable: (mode & 0o111) !== 0 });
    };
    if (
      [
        "application/agent-skills+gzip",
        "application/agent-plugins+gzip",
      ].includes(plan.mediaType)
    )
      readTar(gzip(raw), entry);
    else if (
      [
        "application/agent-skills+zip",
        "application/agent-plugins+zip",
      ].includes(plan.mediaType)
    )
      readZip(raw, entry);
    else return packageFail();
    // Also checks case collisions between files and their implicit directories.
    verifyPackage(plan, content);
    return content;
  } catch (error) {
    if (error instanceof ClientError) throw error;
    return packageFail("invalid_package_archive");
  }
}
function gzip(raw: Buffer): Buffer {
  if (
    raw.length < 18 ||
    raw.readUInt16LE(0) !== 0x8b1f ||
    raw[2] !== 8 ||
    raw[3]! & 0xe0
  )
    return packageFail();
  const flags = raw[3]!;
  let offset = 10;
  if (flags & 4) offset += raw.readUInt16LE(offset) + 2;
  for (const flag of [8, 16])
    if (flags & flag) {
      const end = raw.indexOf(0, offset);
      if (end < offset) return packageFail();
      offset = end + 1;
    }
  if (flags & 2) {
    if (raw.readUInt16LE(offset) !== (crc32(raw.subarray(0, offset)) & 0xffff))
      return packageFail();
    offset += 2;
  }
  if (offset >= raw.length - 8) return packageFail();
  const result = inflateRawSync(raw.subarray(offset), {
    maxOutputLength: Math.min(
      packageLimits.expanded + 2 * 1024 ** 2,
      raw.length * packageLimits.ratio,
    ),
    info: true,
  }) as unknown as { buffer: Buffer; engine: { bytesWritten: number } };
  const trailer = offset + result.engine.bytesWritten;
  if (
    trailer + 8 !== raw.length ||
    raw.readUInt32LE(trailer) !== crc32(result.buffer) ||
    raw.readUInt32LE(trailer + 4) !== result.buffer.length
  )
    return packageFail();
  return result.buffer;
}
type Entry = (
  name: string,
  directory: boolean,
  bytes: Uint8Array,
  mode: number,
) => void;
function readTar(raw: Buffer, entry: Entry): void {
  const field = (bytes: Buffer) =>
    utf8(
      bytes.subarray(0, bytes.indexOf(0) < 0 ? bytes.length : bytes.indexOf(0)),
    );
  const octal = (bytes: Buffer) => {
    const text = bytes.toString("ascii").replace(/\0.*$/u, "").trim();
    if (!/^[0-7]+$/u.test(text)) return packageFail();
    return parseInt(text, 8);
  };
  let offset = 0;
  while (offset + 512 <= raw.length) {
    const header = raw.subarray(offset, offset + 512);
    if (header.every((v) => v === 0)) {
      if (
        raw.length - offset < 1024 ||
        !raw.subarray(offset).every((v) => v === 0)
      )
        return packageFail();
      return;
    }
    const checksum = header.reduce(
      (sum, byte, index) => sum + (index >= 148 && index < 156 ? 32 : byte),
      0,
    );
    if (octal(header.subarray(148, 156)) !== checksum) return packageFail();
    const type = header[156],
      size = octal(header.subarray(124, 136)),
      mode = octal(header.subarray(100, 108));
    if (
      ![0, 48, 53].includes(type!) ||
      size > packageLimits.archive ||
      (type === 53 && size)
    )
      return packageFail();
    const prefix = field(header.subarray(345, 500)),
      name = (prefix ? prefix + "/" : "") + field(header.subarray(0, 100));
    offset += 512;
    if (offset + size > raw.length) return packageFail();
    entry(name, type === 53, raw.subarray(offset, offset + size), mode);
    offset += Math.ceil(size / 512) * 512;
  }
  return packageFail();
}
function readZip(raw: Buffer, entry: Entry): void {
  const extras = (bytes: Buffer) => {
    for (let at = 0; at < bytes.length;) {
      if (at + 4 > bytes.length || bytes.readUInt16LE(at) === 1)
        return packageFail();
      at += 4 + bytes.readUInt16LE(at + 2);
      if (at > bytes.length) return packageFail();
    }
  };
  let end = raw.length - 22;
  for (; end >= Math.max(0, raw.length - 65557); end--)
    if (
      raw.readUInt32LE(end) === 0x06054b50 &&
      end + 22 + raw.readUInt16LE(end + 20) === raw.length
    )
      break;
  if (end < 0 || raw.readUInt16LE(end + 4) || raw.readUInt16LE(end + 6))
    return packageFail();
  const count = raw.readUInt16LE(end + 10),
    central = raw.readUInt32LE(end + 16),
    length = raw.readUInt32LE(end + 12);
  if (
    !count ||
    count > packageLimits.files ||
    count !== raw.readUInt16LE(end + 8) ||
    central + length !== end
  )
    return packageFail();
  let cursor = central;
  const records: {
    offset: number;
    flags: number;
    method: number;
    crc: number;
    compressed: number;
    size: number;
    name: Buffer;
    mode: number;
  }[] = [];
  for (let index = 0; index < count; index++) {
    if (cursor + 46 > end || raw.readUInt32LE(cursor) !== 0x02014b50)
      return packageFail();
    const flags = raw.readUInt16LE(cursor + 8),
      method = raw.readUInt16LE(cursor + 10),
      compressed = raw.readUInt32LE(cursor + 20),
      size = raw.readUInt32LE(cursor + 24),
      names = raw.readUInt16LE(cursor + 28),
      extra = raw.readUInt16LE(cursor + 30),
      comment = raw.readUInt16LE(cursor + 32),
      mode = raw.readUInt32LE(cursor + 38) >>> 16;
    if (
      flags & ~0x080e ||
      flags & 1 ||
      ![0, 8].includes(method) ||
      size > packageLimits.archive ||
      compressed > packageLimits.archive ||
      raw.readUInt16LE(cursor + 34) ||
      cursor + 46 + names + extra + comment > end
    )
      return packageFail();
    const name = raw.subarray(cursor + 46, cursor + 46 + names);
    extras(raw.subarray(cursor + 46 + names, cursor + 46 + names + extra));
    if (!(flags & 0x0800) && name.some((b) => b >= 128)) return packageFail();
    if (mode & 0xf000 && ![0x8000, 0x4000].includes(mode & 0xf000))
      return packageFail();
    records.push({
      offset: raw.readUInt32LE(cursor + 42),
      flags,
      method,
      crc: raw.readUInt32LE(cursor + 16),
      compressed,
      size,
      name,
      mode,
    });
    cursor += 46 + names + extra + comment;
  }
  if (cursor !== end) return packageFail();
  cursor = 0;
  for (const r of records.sort((a, b) => a.offset - b.offset)) {
    if (
      r.offset !== cursor ||
      cursor + 30 > central ||
      raw.readUInt32LE(cursor) !== 0x04034b50 ||
      raw.readUInt16LE(cursor + 6) !== r.flags ||
      raw.readUInt16LE(cursor + 8) !== r.method
    )
      return packageFail();
    const names = raw.readUInt16LE(cursor + 26),
      extra = raw.readUInt16LE(cursor + 28);
    if (!raw.subarray(cursor + 30, cursor + 30 + names).equals(r.name))
      return packageFail();
    if (cursor + 30 + names + extra > central) return packageFail();
    extras(raw.subarray(cursor + 30 + names, cursor + 30 + names + extra));
    if (
      !(r.flags & 8) &&
      (raw.readUInt32LE(cursor + 14) !== r.crc ||
        raw.readUInt32LE(cursor + 18) !== r.compressed ||
        raw.readUInt32LE(cursor + 22) !== r.size)
    )
      return packageFail();
    cursor += 30 + names + extra;
    if (cursor + r.compressed > central) return packageFail();
    const compressed = raw.subarray(cursor, cursor + r.compressed);
    let bytes: Buffer;
    if (r.method === 0) bytes = compressed;
    else {
      const inflated = inflateRawSync(compressed, {
        maxOutputLength: Math.min(packageLimits.archive, Math.max(1, r.size)),
        info: true,
      }) as unknown as { buffer: Buffer; engine: { bytesWritten: number } };
      if (inflated.engine.bytesWritten !== compressed.length)
        return packageFail();
      bytes = inflated.buffer;
    }
    if (bytes.length !== r.size || crc32(bytes) !== r.crc) return packageFail();
    cursor += r.compressed;
    if (r.flags & 8) {
      if (raw.readUInt32LE(cursor) === 0x08074b50) cursor += 4;
      if (
        raw.readUInt32LE(cursor) !== r.crc ||
        raw.readUInt32LE(cursor + 4) !== r.compressed ||
        raw.readUInt32LE(cursor + 8) !== r.size
      )
        return packageFail();
      cursor += 12;
    }
    const name = utf8(r.name),
      directory = name.endsWith("/");
    if ((r.mode & 0xf000) === 0x4000 && !directory) return packageFail();
    entry(name, directory, bytes, r.mode);
  }
  if (cursor !== central) return packageFail();
}
