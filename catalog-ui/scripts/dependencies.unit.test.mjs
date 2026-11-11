// Advisory fixtures test launcher policy without tools, network or installed npm packages.
import assert from "node:assert/strict";
import test from "node:test";
import {
  mkdtemp,
  readFile,
  writeFile,
  rm,
  stat,
  readdir,
} from "node:fs/promises";
import { readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  checkDependencies,
  validateAudit,
  validateTrivy,
  trivyVersion,
} from "./dependencies.mjs";

const lock = {
  packages: Object.fromEntries(
    ["react", "vite", "@playwright/test"].map((name) => [
      `node_modules/${name}`,
      { version: "1.0.0" },
    ]),
  ),
};
const trivy = () => ({
  SchemaVersion: 2,
  Results: [
    {
      Type: "npm",
      Target: "package-lock.json",
      Packages: Object.keys(lock.packages).map((path) => ({
        Name: path.slice(13),
        Version: "1.0.0",
      })),
      Vulnerabilities: [
        { VulnerabilityID: "TEST-LOW", PkgName: "react", Severity: "LOW" },
      ],
    },
  ],
});
const audit = (high = 0, critical = 0) => ({
  auditReportVersion: 2,
  vulnerabilities: {},
  metadata: {
    vulnerabilities: {
      info: 0,
      low: 1,
      moderate: 0,
      high,
      critical,
      total: 1 + high + critical,
    },
    dependencies: { prod: 1, dev: 2, total: 3 },
  },
});

/** Each case owns only two manifests and a disposable report directory. */
async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), "catalog-deps-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  await writeFile(join(root, "package.json"), "{}\n");
  await writeFile(join(root, "package-lock.json"), JSON.stringify(lock));
  return {
    root,
    output: join(root, "reports"),
    env: {
      PATH: "/test",
      HOME: root,
      NODE_ENV: "production",
      npm_config_omit: "dev",
      TRIVY_SEVERITY: "CRITICAL",
      TRIVY_SKIP_DB_UPDATE: "true",
      TRIVY_SERVER: "https://invalid.example",
      TRIVY_DISABLE_TELEMETRY: "false",
      TRIVY_SKIP_VERSION_CHECK: "false",
      TRIVY_CACHE_DIR: join(root, "cache"),
      UI_DEPS_TRIVY: "/tools/trivy",
    },
  };
}

/** Emulate only scanner wire replies, capturing exact arguments and options. */
function scanner({
  report = trivy(),
  npmReport = audit(),
  gateStatus = 0,
  scanStatus = 0,
  npmStatus = 0,
  change,
  version = {
    Version: trivyVersion,
    VulnerabilityDB: { UpdatedAt: "2026-09-13T01:11:30Z" },
  },
} = {}) {
  const calls = [];
  const run = (exe, args, options) => {
    calls.push({ exe, args, options });
    const result = { status: 0, stdout: "", stderr: "secret-registry-token" };
    if (args[0] === "fs") {
      writeFileSync(
        args[args.indexOf("--output") + 1],
        typeof report === "string" ? report : JSON.stringify(report),
      );
      result.status = scanStatus;
    } else if (exe === "npm" && args[0] === "--version")
      result.stdout = "10.9.4\n";
    else if (args[0] === "--version") result.stdout = JSON.stringify(version);
    else if (args[0] === "convert") {
      result.status = gateStatus;
      result.stdout = "native summary\n";
    } else if (args[0] === "audit") {
      result.status = npmStatus;
      result.stdout =
        typeof npmReport === "string" ? npmReport : JSON.stringify(npmReport);
    } else assert.fail("Unexpected scanner operation");
    if (change) change(args, options);
    return result;
  };
  return { calls, run };
}

test("Trivy retains full evidence and uses native threshold conversion with explicit scope", async (t) => {
  const options = await fixture(t);
  const mock = scanner();
  const result = await checkDependencies({
    ...options,
    mode: "trivy",
    run: mock.run,
  });
  assert.equal(result.status, 0);
  assert.equal(result.outcome, "passed");
  assert.equal(mock.calls.length, 4);
  const [preflight, scan, version, gate] = mock.calls;
  assert.equal(preflight.args[0], "--version");
  assert.equal(scan.exe, "/tools/trivy");
  for (const flag of [
    "--include-dev-deps",
    "--list-all-pkgs",
    "--disable-telemetry",
    "--skip-version-check",
  ])
    assert.ok(scan.args.includes(flag));
  assert.equal(scan.args[scan.args.indexOf("--scanners") + 1], "vuln");
  assert.equal(
    scan.args[scan.args.indexOf("--severity") + 1],
    "UNKNOWN,LOW,MEDIUM,HIGH,CRITICAL",
  );
  assert.equal(scan.args.at(-1), join(options.root, "package-lock.json"));
  for (const call of mock.calls) {
    assert.equal(call.options.shell, false);
    assert.equal(call.options.timeout, 180000);
    assert.ok(call.options.maxBuffer > 0);
    assert.equal(call.options.env.TRIVY_CACHE_DIR, options.env.TRIVY_CACHE_DIR);
    for (const key of [
      "TRIVY_SEVERITY",
      "TRIVY_SKIP_DB_UPDATE",
      "TRIVY_SERVER",
      "TRIVY_DISABLE_TELEMETRY",
      "TRIVY_SKIP_VERSION_CHECK",
    ])
      assert.equal(call.options.env[key], undefined);
    assert.equal(
      readFileSync(call.args[call.args.indexOf("--config") + 1], "utf8"),
      "{}\n",
    );
  }
  assert.equal(
    readFileSync(scan.args[scan.args.indexOf("--ignorefile") + 1], "utf8"),
    "",
  );
  assert.equal(version.args[0], "--version");
  for (const call of [preflight, version, gate]) {
    assert.ok(!call.args.includes("--disable-telemetry"));
    assert.ok(!call.args.includes("--skip-version-check"));
  }
  assert.equal(gate.args[gate.args.indexOf("--severity") + 1], "HIGH,CRITICAL");
  assert.equal(gate.args[gate.args.indexOf("--exit-code") + 1], "10");
  assert.equal(gate.args.at(-1), join(result.directory, "trivy.json"));
  assert.equal(
    JSON.parse(await readFile(gate.args.at(-1))).Results[0].Vulnerabilities[0]
      .Severity,
    "LOW",
  );
  assert.equal((await stat(result.directory)).mode & 0o777, 0o700);
  const metadata = JSON.parse(
    await readFile(join(result.directory, "run.json")),
  );
  assert.match(metadata.lockSha256, /^[a-f0-9]{64}$/);
  assert.equal(metadata.toolVersion, trivyVersion);
  assert.equal(
    metadata.vulnerabilityDatabase.UpdatedAt,
    "2026-09-13T01:11:30Z",
  );
});

for (const [label, overrides, expected, outcome] of [
  ["high findings", { gateStatus: 10 }, 1, "findings"],
  ["conversion error", { gateStatus: 1 }, 2, "error"],
  ["scan failure", { scanStatus: 1 }, 2, "error"],
  ["malformed report", { report: "{" }, 2, "error"],
  ["empty report", { report: {} }, 2, "error"],
  ["missing DB evidence", { version: { Version: trivyVersion } }, 2, "error"],
])
  test(`Trivy ${label} is classified accurately`, async (t) => {
    const result = await checkDependencies({
      ...(await fixture(t)),
      mode: "trivy",
      run: scanner(overrides).run,
    });
    assert.equal(result.status, expected);
    assert.equal(result.outcome, outcome);
  });

test("inventory validation rejects wrong target, package versions and omitted dev packages", () => {
  for (const mutate of [
    (r) => {
      r.Results[0].Target = "other/package-lock.json";
    },
    (r) => {
      r.Results[0].Type = "gobinary";
    },
    (r) => {
      r.Results[0].Packages.pop();
    },
    (r) => {
      r.Results[0].Packages[1].Version = "2.0.0";
    },
    (r) => {
      r.Results[0].Vulnerabilities = {};
    },
    (r) => {
      r.Results[0].Vulnerabilities[0].Severity = "invalid";
    },
    (r) => {
      r.Results[0].Vulnerabilities[0].PkgName = "";
    },
  ]) {
    const report = trivy();
    mutate(report);
    assert.throws(() => validateTrivy(report, lock));
  }
});

test("npm includes every dependency class and retains native lower-severity findings", async (t) => {
  const options = await fixture(t);
  const mock = scanner();
  const result = await checkDependencies({
    ...options,
    mode: "audit",
    run: mock.run,
  });
  assert.equal(result.status, 0);
  assert.deepEqual(mock.calls[1].args, [
    "audit",
    "--package-lock-only",
    "--include=dev",
    "--include=optional",
    "--include=peer",
    "--ignore-scripts",
    "--workspaces=false",
    "--audit-level=high",
    "--json",
  ]);
  assert.equal(mock.calls[1].options.cwd, options.root);
  assert.equal(mock.calls[1].options.env.npm_config_omit, "dev");
  assert.equal(
    JSON.parse(await readFile(join(result.directory, "npm-audit.json")))
      .metadata.vulnerabilities.low,
    1,
  );
  assert.equal(
    await readFile(join(options.root, "package.json"), "utf8"),
    "{}\n",
  );
  assert.equal(
    await readFile(join(options.root, "package-lock.json"), "utf8"),
    JSON.stringify(lock),
  );
});

for (const [label, overrides, status] of [
  ["high", { npmReport: audit(1), npmStatus: 1 }, 1],
  ["critical", { npmReport: audit(0, 1), npmStatus: 1 }, 1],
  [
    "registry error",
    {
      npmReport: { error: { message: "secret-registry-token" } },
      npmStatus: 1,
    },
    2,
  ],
  ["malformed JSON", { npmReport: "not-json", npmStatus: 0 }, 2],
  ["unexpected success", { npmReport: audit(1), npmStatus: 0 }, 2],
  ["unexpected failure", { npmStatus: 1 }, 2],
])
  test(`npm ${label} keeps errors distinct from findings`, async (t) => {
    const result = await checkDependencies({
      ...(await fixture(t)),
      mode: "audit",
      run: scanner(overrides).run,
    });
    assert.equal(result.status, status);
    for (const name of await readdir(result.directory))
      assert.ok(
        !(await readFile(join(result.directory, name), "utf8")).includes(
          "secret-registry-token",
        ),
      );
  });

test("npm rejects absent dev inventory and invalid vulnerability counts", () => {
  for (const mutate of [
    (r) => {
      r.metadata.dependencies.dev = 0;
    },
    (r) => {
      r.metadata.vulnerabilities.high = "0";
    },
    (r) => {
      r.metadata.vulnerabilities.critical = -1;
    },
    (r) => {
      r.vulnerabilities = [];
    },
  ]) {
    const report = audit();
    mutate(report);
    assert.throws(() => validateAudit(report, 0));
  }
});

test("missing tools, signals and output limits fail without leaking diagnostics", async (t) => {
  for (const failure of [
    { error: new Error("secret-registry-token"), status: null },
    { signal: "SIGTERM", status: null },
    { status: null },
  ]) {
    const result = await checkDependencies({
      ...(await fixture(t)),
      mode: "audit",
      run: () => failure,
    });
    assert.equal(result.status, 2);
    assert.ok(
      !(await readFile(join(result.directory, "run.json"), "utf8")).includes(
        "secret-registry-token",
      ),
    );
  }
});

test("runs are independent and concurrent manifest edits fail without being reverted", async (t) => {
  const options = await fixture(t);
  const first = await checkDependencies({
    ...options,
    mode: "audit",
    run: scanner().run,
  });
  const second = await checkDependencies({
    ...options,
    mode: "audit",
    run: scanner({
      change: () =>
        writeFileSync(join(options.root, "package.json"), "changed"),
    }).run,
  });
  assert.notEqual(first.directory, second.directory);
  assert.equal(second.status, 2);
  assert.equal(second.outcome, "inputs-changed");
  assert.equal(
    await readFile(join(options.root, "package.json"), "utf8"),
    "changed",
  );
});

for (const Version of ["0.66.0", "0.74.1", undefined])
  test(`Trivy rejects unqualified version ${Version} before scanning`, async (t) => {
    const mock = scanner({ version: { Version } });
    const result = await checkDependencies({
      ...(await fixture(t)),
      mode: "trivy",
      run: mock.run,
    });
    assert.equal(result.status, 2);
    assert.deepEqual(
      mock.calls.map(({ args }) => args[0]),
      ["--version"],
    );
  });
