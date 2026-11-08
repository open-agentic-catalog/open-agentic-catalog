import test from "node:test";
import assert from "node:assert/strict";
import { crc32, deflateRawSync, gzipSync } from "node:zlib";
import { files, hash, representation, tar, zip } from "./package-fixtures.mjs";

const { skillPlan, unpackPackage } = await import(
  process.env.CATALOG_VSCODE_LIB
);
const binding = hash("archive unit account");
const planFor = (entries, type = "application/agent-skills+zip") =>
  skillPlan(representation(entries, type), binding);
const expectFiles = (plan, archive, entries) => {
  const actual = unpackPackage(plan, archive);
  assert.deepEqual(
    [...actual].sort(([a], [b]) => a.localeCompare(b)),
    entries
      .map((file) => [
        file.path,
        { bytes: file.bytes, executable: file.executable },
      ])
      .sort(([a], [b]) => a.localeCompare(b)),
  );
};

// One-entry ZIP fixtures expose optional fields that the ordinary package
// fixture deliberately omits. CRCs and byte lengths come from fixture bytes.
function singleZip(file, options = {}) {
  const bytes = file.bytes,
    name = options.name ?? Buffer.from(file.path),
    method = options.method ?? 8,
    packed = Buffer.concat([
      method === 0 ? bytes : deflateRawSync(bytes),
      options.compressedSuffix ?? Buffer.alloc(0),
    ]),
    flags = (options.flags ?? 0x800) | (options.descriptor ? 8 : 0),
    localExtra = options.localExtra ?? Buffer.alloc(0),
    centralExtra = options.centralExtra ?? Buffer.alloc(0),
    comment = Buffer.from(options.comment ?? ""),
    archiveComment = Buffer.from(options.archiveComment ?? ""),
    checksum = crc32(bytes),
    local = Buffer.alloc(30),
    central = Buffer.alloc(46),
    descriptor = Buffer.alloc(
      options.descriptor ? (options.signature === false ? 12 : 16) : 0,
    );
  local.writeUInt32LE(0x04034b50);
  local.writeUInt16LE(20, 4);
  local.writeUInt16LE(flags, 6);
  local.writeUInt16LE(method, 8);
  if (!options.descriptor) {
    local.writeUInt32LE(checksum, 14);
    local.writeUInt32LE(packed.length, 18);
    local.writeUInt32LE(bytes.length, 22);
  }
  local.writeUInt16LE(name.length, 26);
  local.writeUInt16LE(localExtra.length, 28);
  if (options.descriptor) {
    const start = options.signature === false ? 0 : 4;
    if (start) descriptor.writeUInt32LE(0x08074b50);
    descriptor.writeUInt32LE(checksum, start);
    descriptor.writeUInt32LE(packed.length, start + 4);
    descriptor.writeUInt32LE(bytes.length, start + 8);
  }
  central.writeUInt32LE(0x02014b50);
  central.writeUInt16LE(0x314, 4);
  central.writeUInt16LE(20, 6);
  central.writeUInt16LE(flags, 8);
  central.writeUInt16LE(method, 10);
  central.writeUInt32LE(checksum, 16);
  central.writeUInt32LE(packed.length, 20);
  central.writeUInt32LE(bytes.length, 24);
  central.writeUInt16LE(name.length, 28);
  central.writeUInt16LE(centralExtra.length, 30);
  central.writeUInt16LE(comment.length, 32);
  central.writeUInt32LE(
    ((file.executable ? 0o100755 : 0o100644) * 65536) >>> 0,
    38,
  );
  const body = Buffer.concat([local, name, localExtra, packed, descriptor]),
    directory = Buffer.concat([central, name, centralExtra, comment]),
    end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50);
  end.writeUInt16LE(1, 8);
  end.writeUInt16LE(1, 10);
  end.writeUInt32LE(directory.length, 12);
  end.writeUInt32LE(body.length, 16);
  end.writeUInt16LE(archiveComment.length, 20);
  return Buffer.concat([body, directory, end, archiveComment]);
}

function headerChecksum(header) {
  header.fill(32, 148, 156);
  const sum = header.reduce((total, byte) => total + byte, 0);
  header.write(sum.toString(8).padStart(6, "0") + "\0 ", 148);
}

test("gzip optional metadata and header checksum preserve exact package bytes", () => {
  const entries = files(),
    plan = planFor(entries, "application/agent-skills+gzip"),
    compressed = gzipSync(tar(entries)),
    extra = Buffer.from([3, 0, 0x61, 0x62, 0x63]),
    name = Buffer.from("owned.tar\0"),
    comment = Buffer.from("unit archive\0"),
    header = Buffer.concat([compressed.subarray(0, 10), extra, name, comment]);
  header[3] = 4 | 8 | 16 | 2;
  const checksum = Buffer.alloc(2);
  checksum.writeUInt16LE(crc32(header) & 0xffff);
  const valid = Buffer.concat([header, checksum, compressed.subarray(10)]);
  expectFiles(plan, valid, entries);
  const wrongChecksum = Buffer.from(valid);
  wrongChecksum[header.length] ^= 1;
  assert.throws(() => unpackPackage(plan, wrongChecksum), {
    code: "invalid_skill_package",
  });
  for (const reserved of [0x20, 0x40, 0x80]) {
    const invalidFlags = Buffer.from(valid);
    invalidFlags[3] |= reserved;
    // Keep FHCRC valid so only the reserved flag can cause this refusal.
    invalidFlags.writeUInt16LE(
      crc32(invalidFlags.subarray(0, header.length)) & 0xffff,
      header.length,
    );
    assert.throws(() => unpackPackage(plan, invalidFlags), {
      code: "invalid_skill_package",
    });
  }
  const overlongExtra = Buffer.from(valid);
  overlongExtra.writeUInt16LE(65535, 10);
  assert.throws(() => unpackPackage(plan, overlongExtra), {
    code: "invalid_skill_package",
  });
  const unterminatedName = Buffer.concat([
    compressed.subarray(0, 10),
    Buffer.from("filename-without-a-terminator"),
  ]);
  unterminatedName[3] = 8;
  assert.throws(() => unpackPackage(plan, unterminatedName), {
    code: "invalid_skill_package",
  });
});

test("ZIP stored entries, unsigned descriptors and bounded comments preserve exact bytes", () => {
  const entries = files().slice(0, 1),
    plan = planFor(entries);
  for (const options of [
    { method: 0 },
    { descriptor: true, signature: false },
    { descriptor: true, signature: true },
    { comment: "entry comment", archiveComment: "archive comment" },
  ])
    expectFiles(plan, singleZip(entries[0], options), entries);
});

test("ZIP optional fields must be bounded and cannot advertise ZIP64", () => {
  const entries = files().slice(0, 1),
    plan = planFor(entries),
    benign = Buffer.from([0xfe, 0xca, 3, 0, 0x61, 0x62, 0x63]);
  for (const field of ["localExtra", "centralExtra"]) {
    expectFiles(plan, singleZip(entries[0], { [field]: benign }), entries);
    for (const malformed of [
      Buffer.from([0xfe, 0xca, 0]),
      Buffer.from([0xfe, 0xca, 3, 0, 0x61]),
      Buffer.from([1, 0, 0, 0]),
    ])
      assert.throws(
        () =>
          unpackPackage(plan, singleZip(entries[0], { [field]: malformed })),
        { code: "invalid_skill_package" },
      );
  }
});

test("ZIP non-ASCII names require declared valid UTF-8", () => {
  const entries = files().slice(0, 1);
  entries.push({
    path: "owned-skill/caf\u00e9.txt",
    bytes: Buffer.from("A supporting document."),
    executable: false,
  });
  const plan = planFor(entries),
    valid = zip(entries);
  expectFiles(plan, valid, entries);
  // Clear the encoding declaration on the supporting entry in both headers.
  const missingFlag = Buffer.from(valid),
    firstCentral = missingFlag.readUInt32LE(missingFlag.length - 6),
    firstNameLength = missingFlag.readUInt16LE(firstCentral + 28),
    secondCentral = firstCentral + 46 + firstNameLength,
    secondLocal = missingFlag.readUInt32LE(secondCentral + 42);
  missingFlag.writeUInt16LE(0, secondLocal + 6);
  missingFlag.writeUInt16LE(0, secondCentral + 8);
  assert.throws(() => unpackPackage(plan, missingFlag), {
    code: "invalid_skill_package",
  });
  const one = files().slice(0, 1),
    badName = Buffer.from(one[0].path);
  badName[badName.length - 1] = 0xff;
  assert.throws(
    () => unpackPackage(planFor(one), singleZip(one[0], { name: badName })),
    { code: "invalid_package_archive" },
  );
});

test("ZIP data descriptors must match the retained CRC and both byte lengths", () => {
  const entries = files().slice(0, 1),
    plan = planFor(entries);
  for (const signature of [false, true]) {
    const valid = singleZip(entries[0], { descriptor: true, signature });
    expectFiles(plan, valid, entries);
    const central = valid.readUInt32LE(valid.length - 6),
      descriptor = central - (signature ? 16 : 12) + (signature ? 4 : 0);
    for (const field of [0, 4, 8]) {
      const corrupt = Buffer.from(valid);
      corrupt.writeUInt32LE(
        (corrupt.readUInt32LE(descriptor + field) + 1) >>> 0,
        descriptor + field,
      );
      assert.throws(() => unpackPackage(plan, corrupt), {
        code: "invalid_skill_package",
      });
    }
  }
});

test("ZIP rejects bytes hidden after the declared deflate stream", () => {
  const entries = files().slice(0, 1),
    plan = planFor(entries);
  expectFiles(plan, singleZip(entries[0]), entries);
  // Both headers include the suffix in the compressed length, while the
  // uncompressed byte length and CRC still describe the unchanged valid file.
  for (const compressedSuffix of [
    Buffer.from([0, 0, 0]),
    Buffer.from("unconsumed compressed bytes"),
  ])
    assert.throws(
      () => unpackPackage(plan, singleZip(entries[0], { compressedSuffix })),
      { code: "invalid_skill_package" },
    );
});

test("ZIP local headers and central records must describe the same entry", () => {
  const entries = files().slice(0, 1),
    plan = planFor(entries),
    valid = singleZip(entries[0]),
    central = valid.readUInt32LE(valid.length - 6);
  expectFiles(plan, valid, entries);
  for (const mutate of [
    (raw) => raw.writeUInt16LE(0, 6),
    (raw) => raw.writeUInt16LE(0, 8),
    (raw) => (raw[30] = "x".charCodeAt(0)),
    (raw) => raw.writeUInt32LE(1, central + 42),
    (raw) => raw.writeUInt16LE(1, central + 34),
  ]) {
    const corrupt = Buffer.from(valid);
    mutate(corrupt);
    assert.throws(() => unpackPackage(plan, corrupt), {
      code: "invalid_skill_package",
    });
  }
});

test("tar prefix fields and explicit directories do not change the file inventory", () => {
  const entries = files(),
    plan = planFor(entries, "application/agent-skills+gzip"),
    directory = {
      path: "owned-skill/",
      type: 53,
      bytes: Buffer.alloc(0),
      executable: false,
    },
    valid = tar([directory, ...entries]);
  // The second header splits the same path between ustar name and prefix.
  const header = valid.subarray(512, 1024);
  header.fill(0, 0, 100);
  header.write("SKILL.md", 0);
  header.write("owned-skill", 345);
  headerChecksum(header);
  expectFiles(plan, gzipSync(valid), entries);
  const duplicateDirectory = tar([directory, directory, ...entries]);
  assert.throws(() => unpackPackage(plan, gzipSync(duplicateDirectory)), {
    code: "unsafe_package_path",
  });
  const directoryWithData = tar([
    { ...directory, bytes: Buffer.from("not an empty directory") },
    ...entries,
  ]);
  assert.throws(() => unpackPackage(plan, gzipSync(directoryWithData)), {
    code: "invalid_skill_package",
  });
});

test("tar requires complete zero termination and octal size and mode fields", () => {
  const entries = files(),
    plan = planFor(entries, "application/agent-skills+gzip"),
    valid = tar(entries);
  expectFiles(plan, gzipSync(valid), entries);
  const shortEnd = valid.subarray(0, valid.length - 512),
    noEnd = valid.subarray(0, valid.length - 1024),
    dirtyEnd = Buffer.from(valid);
  dirtyEnd[dirtyEnd.length - 1] = 1;
  for (const corrupt of [shortEnd, noEnd, dirtyEnd])
    assert.throws(() => unpackPackage(plan, gzipSync(corrupt)), {
      code: "invalid_skill_package",
    });
  for (const [offset, length] of [
    [100, 8],
    [124, 12],
  ]) {
    const corrupt = Buffer.from(valid),
      header = corrupt.subarray(0, 512);
    header.fill(0, offset, offset + length);
    header.write("9", offset);
    headerChecksum(header);
    assert.throws(() => unpackPackage(plan, gzipSync(corrupt)), {
      code: "invalid_skill_package",
    });
  }
});
