// Native subprocess/reporter controls use synthetic files only, never a browser or service.
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { createHash } from "node:crypto";
import {
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rm,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const execute = promisify(execFile);
const producer = new URL("./tooling-coverage.mjs", import.meta.url).href;
const packaging = fileURLToPath(
  new URL("../../test/packaging", import.meta.url),
);
const digest = (bytes) => createHash("sha256").update(bytes).digest("hex");

async function fixture(t) {
  const root = await mkdtemp(
    join(tmpdir(), "catalog-node-coverage-functional-"),
  );
  t.after(() => rm(root, { recursive: true, force: true }));
  for (const owner of ["catalog-ui", "documentation"])
    await mkdir(join(root, owner, "scripts"), { recursive: true });
  await writeFile(
    join(root, "catalog-ui/scripts/unused.mjs"),
    'throw new Error("unobserved root must not execute");\n',
  );
  return root;
}

async function produce(root, profile) {
  const env = { ...process.env };
  delete env.NODE_TEST_CONTEXT;
  const source = `import {runCoverage} from ${JSON.stringify(producer)}; console.log(await runCoverage(${JSON.stringify(profile)},${JSON.stringify(root)}));`;
  const entry = join(root, "run-coverage.mjs");
  await writeFile(entry, source);
  return execute(process.execPath, [entry], {
    env,
    timeout: 15000,
    maxBuffer: 1 << 20,
  });
}

async function receipt(root) {
  const directory = join(root, "output/coverage/tooling-node");
  const runs = await readdir(directory);
  assert.equal(runs.length, 1);
  const path = join(directory, runs[0]);
  assert.deepEqual((await readdir(path)).sort(), ["lcov.info", "report.json"]);
  const report = JSON.parse(await readFile(join(path, "report.json"), "utf8"));
  const lcov = await readFile(join(root, report.reports[0].path), "utf8");
  assert.equal(report.reports[0].sha256, digest(lcov));
  for (const [source, hash] of Object.entries(report.sourceFiles))
    assert.equal(hash, digest(await readFile(join(root, source))));
  return { report, lcov };
}

for (const [profile, owner, expected] of [
  ["ui-unit", "catalog-ui", "node-ui-unit"],
  ["docs-unit", "documentation", "node-docs-unit"],
]) {
  test(`${profile} native report preserves selection, zero-hit inventory and source identity`, async (t) => {
    const root = await fixture(t);
    await writeFile(
      join(root, owner, "scripts/helper.mjs"),
      "export function answer() { return 42; }\n",
    );
    await writeFile(
      join(root, owner, "scripts/one.unit.test.mjs"),
      'import {test} from "node:test"; import assert from "node:assert/strict"; import {answer} from "./helper.mjs"; test("existing assertion",()=>assert.equal(answer(),42));\n',
    );
    await writeFile(
      join(root, owner, "scripts/never.functional.test.mjs"),
      'throw new Error("functional selection must remain separate");\n',
    );
    await produce(root, profile);
    const { report, lcov } = await receipt(root);
    assert.equal(report.status, "passed");
    assert.equal(report.schemaVersion, 1);
    assert.equal(report.kind, "javascript");
    assert.equal(report.profile, expected);
    assert.deepEqual(report.tool, { name: "node", version: process.version });
    assert.deepEqual(report.selection, {
      files: [`${owner}/scripts/one.unit.test.mjs`],
      argv: [],
      scope: "unit",
    });
    assert.deepEqual(report.observedSourceFiles, [
      `${owner}/scripts/helper.mjs`,
    ]);
    assert.deepEqual(report.unobservedSourceFiles, [
      "catalog-ui/scripts/unused.mjs",
    ]);
    assert(report.sourceFiles[`${owner}/scripts/one.unit.test.mjs`]);
    assert.match(lcov, /FNDA:[1-9][0-9]*,answer/);
    assert(!lcov.includes(".test.mjs"));
    assert(!lcov.includes("unused.mjs"));
  });
}

test("synthetic packaged argv, imported helper and inherited child coverage survive native wrapping", async (t) => {
  const root = await fixture(t);
  const scripts = join(root, "catalog-ui/scripts");
  await writeFile(
    join(scripts, "manual-layout.mjs"),
    "export async function manualLayout() { return 42; }\n",
  );
  await writeFile(
    join(scripts, "child.mjs"),
    'function childOnly() { return 7; } if(childOnly() !== 7) throw new Error("child assertion");\n',
  );
  await writeFile(
    join(scripts, "packaged-test.mjs"),
    'import assert from "node:assert/strict"; import {execFileSync} from "node:child_process"; import {writeFileSync} from "node:fs"; import {manualLayout} from "./manual-layout.mjs"; try { assert.deepEqual(process.argv.slice(2),["compose"]); assert.equal(await manualLayout(),42); execFileSync(process.execPath,[new URL("./child.mjs",import.meta.url).pathname],{env:process.env}); } finally { writeFileSync("cleanup.json","completed"); }\n',
  );
  await produce(root, "compose");
  const { report, lcov } = await receipt(root);
  assert.equal(report.profile, "node-compose");
  assert.deepEqual(report.selection, {
    files: ["catalog-ui/scripts/packaged-test.mjs"],
    argv: ["compose"],
    scope: "packaged-browser",
  });
  assert(
    report.observedSourceFiles.includes("catalog-ui/scripts/manual-layout.mjs"),
  );
  assert(report.observedSourceFiles.includes("catalog-ui/scripts/child.mjs"));
  assert.match(lcov, /FNDA:[1-9][0-9]*,manualLayout/);
  assert.match(lcov, /FNDA:[1-9][0-9]*,childOnly/);
  assert.equal(
    await readFile(join(root, "catalog-ui/cleanup.json"), "utf8"),
    "completed",
  );
});

test("a packaged cleanup exit failure cannot publish a successful coverage receipt", async (t) => {
  const root = await fixture(t);
  await writeFile(
    join(root, "catalog-ui/scripts/packaged-test.mjs"),
    'import {writeFileSync} from "node:fs"; try { if(process.argv[2] !== "compose") throw new Error("wrong selection"); } finally { writeFileSync("cleanup.json","failed"); process.exitCode=1; }\n',
  );
  await assert.rejects(produce(root, "compose"));
  assert.equal(
    await readFile(join(root, "catalog-ui/cleanup.json"), "utf8"),
    "failed",
  );
  assert.deepEqual(
    await readdir(join(root, "output/coverage/tooling-node")),
    [],
  );
});

for (const mode of ["assertion failure", "changed source"]) {
  test(`${mode} publishes neither a passing receipt nor LCOV and retains test cleanup`, async (t) => {
    const root = await fixture(t);
    const scripts = join(root, "catalog-ui/scripts");
    await writeFile(join(scripts, "helper.mjs"), "export const value = 1;\n");
    const action =
      mode === "assertion failure"
        ? "assert.equal(value,2);"
        : 'writeFileSync(new URL("./helper.mjs",import.meta.url),"export const value = 2;\\n");';
    await writeFile(
      join(scripts, "one.unit.test.mjs"),
      `import {test} from "node:test"; import assert from "node:assert/strict"; import {writeFileSync} from "node:fs"; import {value} from "./helper.mjs"; test("owned control",t=>{t.after(()=>writeFileSync("cleanup.json","completed"));${action}});\n`,
    );
    await assert.rejects(produce(root, "ui-unit"));
    assert.equal(
      await readFile(join(root, "catalog-ui/cleanup.json"), "utf8"),
      "completed",
    );
    assert.deepEqual(
      await readdir(join(root, "output/coverage/tooling-node")),
      [],
    );
  });
}

for (const profile of ["ui-unit", "docs-unit"]) {
  for (const mode of ["skipped", "todo", "empty", "missing sibling cases"]) {
    test(`${profile} ${mode} required execution refuses a receipt despite a healthy sibling`, async (t) => {
      const root = await fixture(t);
      const owner = profile === "ui-unit" ? "catalog-ui" : "documentation";
      const scripts = join(root, owner, "scripts");
      await writeFile(join(scripts, "helper.mjs"), "export const value = 1;\n");
      await writeFile(
        join(scripts, "good.unit.test.mjs"),
        'import {test} from "node:test"; import {value} from "./helper.mjs"; test("good",()=>{if(value!==1)throw Error("bad");});\n',
      );
      const body = {
        skipped: 'test.skip("required",()=>{});',
        todo: 'test.todo("required");',
        empty: "",
        "missing sibling cases": 'test("parent",()=>{});',
      }[mode];
      await writeFile(
        join(scripts, "required.unit.test.mjs"),
        'import {test} from "node:test";\n' + body,
      );
      if (mode === "missing sibling cases")
        await writeFile(
          join(scripts, "empty.unit.test.mjs"),
          "// No registered test cases.\n",
        );
      await assert.rejects(produce(root, profile), /Required native tests/);
      assert.deepEqual(
        await readdir(join(root, "output/coverage/tooling-node")),
        [],
      );
    });
  }
}

for (const [label, names] of [
  [
    "non-LF controls",
    [
      "scope\u0085SF:documentation/scripts/docs.mjs",
      "vertical\vtab",
      "form\ffeed",
    ],
  ],
  [
    "Unicode line separators",
    ["line\u2028separator", "paragraph\u2029separator"],
  ],
]) {
  test(`actual native ${label} pass receipt and complete Sonar import without phantom sources`, async (t) => {
    const root = await fixture(t);
    const literal = (value) =>
      JSON.stringify(value)
        .replaceAll("\u2028", "\\u2028")
        .replaceAll("\u2029", "\\u2029");
    const helper = [
      "export const api = {",
      ...names.map(
        (name, index) => `  ${literal(name)}() { return ${42 + index}; },`,
      ),
      "};",
      "",
    ].join("\n");
    await writeFile(join(root, "catalog-ui/scripts/helper.mjs"), helper);
    await writeFile(
      join(root, "catalog-ui/scripts/one.unit.test.mjs"),
      `import {test} from "node:test"; import assert from "node:assert/strict"; import {api} from "./helper.mjs"; test("native opaque names",()=>{${names.map((name, index) => `assert.equal(api[${literal(name)}](),${42 + index});`).join("")}});\n`,
    );
    await writeFile(
      join(root, "documentation/scripts/docs.mjs"),
      "export function answer() { return 7; }\n",
    );
    await writeFile(
      join(root, "documentation/scripts/one.unit.test.mjs"),
      'import {test} from "node:test"; import assert from "node:assert/strict"; import {answer} from "./docs.mjs"; test("docs control",()=>assert.equal(answer(),7));\n',
    );
    await produce(root, "ui-unit");
    const { report, lcov } = await receipt(root);
    for (const [index, name] of names.entries()) {
      assert(lcov.includes(`FN:${index + 2},${name}\n`));
      assert(lcov.includes(`FNDA:1,${name}\n`));
    }
    assert.deepEqual(report.observedSourceFiles, [
      "catalog-ui/scripts/helper.mjs",
    ]);
    const parent = join(root, "output/coverage/tooling-node");
    const [uiRun] = await readdir(parent);
    await produce(root, "docs-unit");
    const docsRuns = (await readdir(parent)).filter((name) => name !== uiRun);
    assert.equal(docsRuns.length, 1);
    const env = { ...process.env };
    for (const name of [
      "COVERAGE_PROCESS_START",
      "COVERAGE_PROCESS_CONFIG",
      "COVERAGE_FILE",
    ])
      delete env[name];
    const consumer = String.raw`
import hashlib, json, os, shutil, sys
from pathlib import Path
sys.path.insert(0, sys.argv[1])
import sonarqube
root = Path(sys.argv[2])
receipts = [root / name for name in sys.argv[3:]]
reports = [json.loads(path.read_text()) for path in receipts]
snapshot = root / "snapshot"
for owner in ("catalog-ui", "documentation"):
    shutil.copytree(root / owner / "scripts", snapshot / owner / "scripts")
before = {path.relative_to(snapshot).as_posix(): path.read_bytes()
          for path in snapshot.rglob("*") if path.is_file()}
sonarqube.ROOT = root
results = {}
for selection in ("native", "legacy"):
    artifacts = root / ("artifacts-" + selection)
    artifacts.mkdir()
    os.environ.pop("SONAR_GO_COVERAGE", None)
    os.environ.pop("SONAR_JS_COVERAGE", None)
    if selection == "native":
        chosen = receipts
        expected = {"coverage-tooling-0.info": reports[0], "coverage-tooling-1.info": reports[1]}
    else:
        os.environ["SONAR_JS_COVERAGE"] = reports[0]["reports"][0]["path"]
        chosen = receipts[1:]
        expected = {"coverage-js.info": reports[0], "coverage-tooling-0.info": reports[1]}
    os.environ["SONAR_TOOLING_REPORTS"] = " ".join(str(path.relative_to(root)) for path in chosen)
    properties, evidence = sonarqube.coverage_inputs(snapshot, artifacts)
    assert properties == "sonar.javascript.lcov.reportPaths=" + ",".join("/coverage/" + name for name in expected) + "\n"
    for name, report in expected.items():
        raw = (root / report["reports"][0]["path"]).read_bytes()
        assert (artifacts / name).read_bytes() == raw
        assert hashlib.sha256(raw).hexdigest() == report["reports"][0]["sha256"]
    assert len(evidence["tooling"]) == len(chosen)
    assert before == {path.relative_to(snapshot).as_posix(): path.read_bytes()
                      for path in snapshot.rglob("*") if path.is_file()}
    results[selection] = {"sourceUnchanged": True, "reportBytesUnchanged": True, "profiles": len(chosen)}
print(json.dumps(results))
`;
    const result = await execute(
      "python3",
      [
        "-B",
        "-c",
        consumer,
        packaging,
        root,
        `output/coverage/tooling-node/${uiRun}/report.json`,
        `output/coverage/tooling-node/${docsRuns[0]}/report.json`,
      ],
      { env, timeout: 15000, maxBuffer: 1 << 20 },
    );
    assert.deepEqual(JSON.parse(result.stdout), {
      native: {
        sourceUnchanged: true,
        reportBytesUnchanged: true,
        profiles: 2,
      },
      legacy: {
        sourceUnchanged: true,
        reportBytesUnchanged: true,
        profiles: 1,
      },
    });
  });
}
