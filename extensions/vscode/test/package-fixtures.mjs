import { createHash } from "node:crypto";
import { crc32, gzipSync, deflateRawSync } from "node:zlib";
export const hash = (bytes) =>
  "sha256:" + createHash("sha256").update(bytes).digest("hex");
export function files(text = "Owned instructions.") {
  return [
    {
      path: "owned-skill/SKILL.md",
      bytes: Buffer.from(
        "---\nname: owned-skill\ndescription: Owned instructions.\n---\n" +
          text,
      ),
      executable: false,
    },
    {
      path: "owned-skill/references/help.txt",
      bytes: Buffer.from("Supporting file."),
      executable: false,
    },
    {
      path: "owned-skill/scripts/never-run.sh",
      bytes: Buffer.from("#!/bin/sh\nexit 91\n"),
      executable: true,
    },
  ];
}
export function zip(entries, descriptor = false) {
  const locals = [],
    centrals = [];
  let offset = 0;
  for (const file of entries) {
    const name = Buffer.from(file.path),
      bytes = file.bytes,
      packed = deflateRawSync(bytes),
      crc = crc32(bytes),
      flags = 2048 | (descriptor ? 8 : 0);
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50);
    local.writeUInt16LE(20, 4);
    local.writeUInt16LE(flags, 6);
    local.writeUInt16LE(8, 8);
    if (!descriptor) {
      local.writeUInt32LE(crc, 14);
      local.writeUInt32LE(packed.length, 18);
      local.writeUInt32LE(bytes.length, 22);
    }
    local.writeUInt16LE(name.length, 26);
    const central = Buffer.alloc(46);
    central.writeUInt32LE(0x02014b50);
    central.writeUInt16LE(0x314, 4);
    central.writeUInt16LE(20, 6);
    central.writeUInt16LE(flags, 8);
    central.writeUInt16LE(8, 10);
    central.writeUInt32LE(crc, 16);
    central.writeUInt32LE(packed.length, 20);
    central.writeUInt32LE(bytes.length, 24);
    central.writeUInt16LE(name.length, 28);
    central.writeUInt32LE(
      ((file.mode ?? 0o100000 | (file.executable ? 0o755 : 0o644)) * 65536) >>>
        0,
      38,
    );
    central.writeUInt32LE(offset, 42);
    const d = Buffer.alloc(descriptor ? 16 : 0);
    if (descriptor) {
      d.writeUInt32LE(0x08074b50);
      d.writeUInt32LE(crc, 4);
      d.writeUInt32LE(packed.length, 8);
      d.writeUInt32LE(bytes.length, 12);
    }
    locals.push(local, name, packed, d);
    centrals.push(central, name);
    offset += local.length + name.length + packed.length + d.length;
  }
  const central = Buffer.concat(centrals),
    end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50);
  end.writeUInt16LE(entries.length, 8);
  end.writeUInt16LE(entries.length, 10);
  end.writeUInt32LE(central.length, 12);
  end.writeUInt32LE(offset, 16);
  return Buffer.concat([...locals, central, end]);
}
export function tar(entries) {
  const blocks = [];
  for (const file of entries) {
    const h = Buffer.alloc(512);
    h.write(file.path, 0, 100);
    h.write(
      (file.executable ? 0o755 : 0o644).toString(8).padStart(7, "0") + "\0",
      100,
    );
    h.write("0000000\0", 108);
    h.write("0000000\0", 116);
    h.write(file.bytes.length.toString(8).padStart(11, "0") + "\0", 124);
    h.write("00000000000\0", 136);
    h.fill(32, 148, 156);
    h[156] = file.type ?? 48;
    h.write("ustar\0", 257);
    h.write("00", 263);
    const sum = h.reduce((a, b) => a + b, 0);
    h.write(sum.toString(8).padStart(6, "0") + "\0 ", 148);
    blocks.push(
      h,
      file.bytes,
      Buffer.alloc((512 - (file.bytes.length % 512)) % 512),
    );
  }
  return Buffer.concat([...blocks, Buffer.alloc(1024)]);
}
export function representation(
  entries = files(),
  mediaType = "application/agent-skills+zip",
) {
  const archive = mediaType.endsWith("gzip")
    ? gzipSync(tar(entries))
    : zip(entries);
  return {
    id: "owned",
    validationState: "valid",
    mediaType,
    contentDigest: hash(archive),
    size: archive.length,
    package: {
      structureState: "passed",
      rootDirectory: "owned-skill",
      expandedSize: entries.reduce((n, f) => n + f.bytes.length, 0),
      fileCount: entries.length,
      files: entries.map((f) => ({
        path: f.path,
        size: f.bytes.length,
        digest: hash(f.bytes),
        executable: f.executable,
      })),
    },
    handoff: {
      git: {
        repository: "https://git.example/owned.git",
        commit: "a".repeat(40),
        directory: "skills/owned-skill",
      },
      oci: {
        reference: "registry.example/skills/owned@sha256:" + "b".repeat(64),
      },
    },
  };
}
export const content = (entries = files()) =>
  new Map(
    entries.map((f) => [f.path, { bytes: f.bytes, executable: f.executable }]),
  );
