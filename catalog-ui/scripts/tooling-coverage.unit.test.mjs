import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import {
  collectSources,
  normalizeLcov,
  profileSelection,
} from "./tooling-coverage.mjs";

async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), "catalog-node-coverage-unit-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  for (const owner of ["catalog-ui", "documentation"])
    await mkdir(join(root, owner, "scripts"), { recursive: true });
  return root;
}

test("source identity includes unused modules and assertion files without importing them", async (t) => {
  const root = await fixture(t);
  const body = 'throw new Error("never import a coverage inventory");\n';
  await writeFile(join(root, "catalog-ui/scripts/unused.mjs"), body);
  await writeFile(join(root, "documentation/scripts/one.unit.test.mjs"), body);
  await writeFile(
    join(root, "catalog-ui/scripts/frontend.ts"),
    "not a native root",
  );
  const hash = createHash("sha256").update(body).digest("hex");
  assert.deepEqual(await collectSources(root), {
    "catalog-ui/scripts/unused.mjs": hash,
    "documentation/scripts/one.unit.test.mjs": hash,
  });
  await writeFile(
    join(root, "documentation/scripts/one.unit.test.mjs"),
    "changed",
  );
  assert.notDeepEqual(await collectSources(root), {
    "catalog-ui/scripts/unused.mjs": hash,
    "documentation/scripts/one.unit.test.mjs": hash,
  });
});

test("source binding rejects a symlink rather than hashing another tree", async (t) => {
  const root = await fixture(t);
  await writeFile(join(root, "outside.mjs"), "export const outside = true;");
  await symlink(
    join(root, "outside.mjs"),
    join(root, "catalog-ui/scripts/link.mjs"),
  );
  await assert.rejects(collectSources(root), /symlink/);
});

test("profiles preserve exact native-unit discovery, owner cwd and packaged argv", async (t) => {
  const root = await fixture(t);
  for (const path of [
    "catalog-ui/scripts/z.unit.test.mjs",
    "catalog-ui/scripts/a.unit.test.mjs",
    "catalog-ui/scripts/ignored.functional.test.mjs",
    "catalog-ui/scripts/ignored.test.mjs",
    "documentation/scripts/docs.unit.test.mjs",
  ])
    await writeFile(join(root, path), "");
  assert.deepEqual(await profileSelection("ui-unit", root), {
    cwd: join(root, "catalog-ui"),
    files: [
      join(root, "catalog-ui/scripts/a.unit.test.mjs"),
      join(root, "catalog-ui/scripts/z.unit.test.mjs"),
    ],
    argv: [],
    scope: "unit",
    receiptProfile: "node-ui-unit",
  });
  assert.deepEqual((await profileSelection("docs-unit", root)).files, [
    join(root, "documentation/scripts/docs.unit.test.mjs"),
  ]);
  assert.deepEqual(await profileSelection("compose", root), {
    cwd: join(root, "catalog-ui"),
    files: [join(root, "catalog-ui/scripts/packaged-test.mjs")],
    argv: ["compose"],
    scope: "packaged-browser",
    receiptProfile: "node-compose",
  });
  await assert.rejects(profileSelection("unknown", root), /Select/);
  await rm(join(root, "documentation/scripts/docs.unit.test.mjs"));
  await assert.rejects(profileSelection("docs-unit", root), /No native/);
});

const native =
  "TN:\nSF:scripts/helper.mjs\nFN:1,helper\nFNDA:1,helper\nFNF:1\nFNH:1\nBRDA:2,0,0,-\nBRF:1\nBRH:0\nDA:1,1\nDA:2,0\nLH:1\nLF:2\nend_of_record\n";

test("native LCOV keeps real zero/branch counts and reports no invented unused module", () => {
  const result = normalizeLcov(native, "/repo", "/repo/catalog-ui", [
    "catalog-ui/scripts/helper.mjs",
    "catalog-ui/scripts/unused.mjs",
  ]);
  assert.equal(
    result.lcov,
    native.replace("SF:scripts/helper.mjs", "SF:catalog-ui/scripts/helper.mjs"),
  );
  assert.deepEqual(result.observedSourceFiles, [
    "catalog-ui/scripts/helper.mjs",
  ]);
  assert(!result.lcov.includes("unused"));
  assert.equal(
    normalizeLcov(
      native.replace("scripts/helper.mjs", "../catalog-ui/scripts/helper.mjs"),
      "/repo",
      "/repo/documentation",
      ["catalog-ui/scripts/helper.mjs"],
    ).lcov,
    result.lcov,
  );
});

test("LCOV normalization rejects incomplete, foreign and duplicate records", () => {
  for (const raw of [
    "",
    native.replace("end_of_record\n", ""),
    native.replace("SF:scripts/helper.mjs", "SF:../src/frontend.ts"),
    native.replace("DA:2,0", "DA:2,-1"),
    native + native,
    "end_of_record\n",
  ])
    assert.throws(() =>
      normalizeLcov(raw, "/repo", "/repo/catalog-ui", [
        "catalog-ui/scripts/helper.mjs",
      ]),
    );
});

test("LCOV tags keep their own payload grammar and opaque function names", () => {
  const names = [
    "scope:method,part",
    "  λ😀\u0085name\ufeff  ",
    "name\vtab\fform\u2028line\u2029paragraph",
    "name\u0085SF:documentation/scripts/helper.mjs",
  ];
  const rows = [
    "DA:1,0",
    "DA:19,0002",
    "BRDA:2,0,9,-",
    "BRDA:20,03,0,004",
    ...names.flatMap((name) => [`FN:3,${name}`, `FNDA:0,${name}`]),
    ...["FNF", "FNH", "LF", "LH", "BRF", "BRH"].flatMap((tag) => [
      `${tag}:0`,
      `${tag}:012`,
    ]),
  ];
  for (const row of rows) {
    const raw = `TN:\nSF:scripts/helper.mjs\n${row}\nend_of_record\n`;
    assert.equal(
      normalizeLcov(raw, "/repo", "/repo/catalog-ui", [
        "catalog-ui/scripts/helper.mjs",
      ]).lcov,
      raw.replace("SF:scripts/helper.mjs", "SF:catalog-ui/scripts/helper.mjs"),
      row,
    );
  }
});

test("LCOV physical LF and CRLF frames preserve opaque payloads and exact final records", () => {
  const raw = native.replaceAll("helper\n", "name\u0085SF:other\u2028\u2029\n");
  const expected = raw.replace(
    "SF:scripts/helper.mjs",
    "SF:catalog-ui/scripts/helper.mjs",
  );
  for (const separator of ["\n", "\r\n"])
    for (const terminated of [false, true]) {
      const framed = raw.trimEnd().replaceAll("\n", separator);
      assert.equal(
        normalizeLcov(
          framed + (terminated ? separator : ""),
          "/repo",
          "/repo/catalog-ui",
          ["catalog-ui/scripts/helper.mjs"],
        ).lcov,
        expected,
      );
    }
  for (const invalid of [
    native.replace("FN:1,helper", "FN:1,name\rinside"),
    native.replace("FN:1,helper", "FN:1,name\ninside"),
    native.replace("FNDA:1,helper", "FNDA:1,name\rinside"),
    native.replace("FNDA:1,helper", "FNDA:1,name\ninside"),
    native.replaceAll("\n", "\r"),
    native.trimEnd() + "\r",
    native + "\u0085",
    native + "\n",
  ])
    assert.throws(() =>
      normalizeLcov(invalid, "/repo", "/repo/catalog-ui", [
        "catalog-ui/scripts/helper.mjs",
      ]),
    );
});

test("LCOV rows reject unknown tags and payloads from another tag's grammar", () => {
  const invalid = [
    "DA:0,1",
    "DA:1,-",
    "DA:1,2,checksum",
    "DA:١,2",
    "DA:1,٢",
    "BRDA:0,1,0,1",
    "BRDA:1,-1,0,1",
    "BRDA:1,0,-,1",
    "BRDA:1,0,0,-1",
    "BRDA:1,0,0,",
    "FN:0,name",
    "FN:1,",
    "FN:1,name\rinside",
    "FNDA:-1,name",
    "FNDA:1,",
    "FNDA:١,name",
    ...["FNF", "FNH", "LF", "LH", "BRF", "BRH"].flatMap((tag) => [
      `${tag}:-1`,
      `${tag}:1,name`,
      `${tag}:١`,
    ]),
    "Unknown:1",
    "toString:1",
    "constructor:1",
    "__proto__:1",
    ":1",
    "DA1,0",
    " DA:1,0",
  ];
  for (const row of invalid)
    assert.throws(
      () =>
        normalizeLcov(
          `TN:\nSF:scripts/helper.mjs\n${row}\nend_of_record\n`,
          "/repo",
          "/repo/catalog-ui",
          ["catalog-ui/scripts/helper.mjs"],
        ),
      /Invalid native LCOV row/,
      row,
    );
});
