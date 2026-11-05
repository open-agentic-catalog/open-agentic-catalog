import { test } from "node:test";
import assert from "node:assert/strict";
import {
  mkdtemp,
  readdir,
  rm,
  mkdir,
  writeFile,
  readFile,
  chmod,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import { basename, join } from "node:path";
import { createServer } from "node:net";
import {
  localPorts,
  startProcess,
  stopProcess,
  startCatalog,
  ready,
} from "./browser-fixture.mjs";

test("fixture ports are distinct literal-loopback dynamic/private allocations", async () => {
  const ports = await localPorts();
  assert.equal(new Set(ports).size, 3);
  assert.ok(ports.every((port) => port >= 49152 && port <= 65535));
});
test("fixture port allocation rejects browser-blocked ranges and bounds collisions", async () => {
  const [port] = await localPorts();
  const occupied = createServer();
  await new Promise((resolve, reject) => {
    occupied.once("error", reject);
    occupied.listen(port, "127.0.0.1", resolve);
  });
  try {
    let attempts = 0;
    await assert.rejects(
      localPorts(() => {
        attempts++;
        return port;
      }),
      /Could not reserve/,
    );
    assert.equal(attempts, 100);
    assert.equal(occupied.listening, true);
  } finally {
    await new Promise((resolve) => occupied.close(resolve));
  }
});
test("process cleanup waits for exit and is safe to repeat", async () => {
  const child = startProcess(process.execPath, [
    "-e",
    "setInterval(() => {}, 1000)",
  ]);
  await stopProcess(child);
  await stopProcess(child);
  assert.notEqual(child.child.signalCode, null);
});
test("fixture allocation retries duplicate candidates and releases successful reservations", async () => {
  const candidates = await localPorts();
  const sequence = [candidates[0], candidates[0], candidates[1], candidates[2]];
  assert.deepEqual(await localPorts(() => sequence.shift()), candidates);
  assert.equal(sequence.length, 0);
  // Rebinding every exact port proves successful reservations were released.
  const again = [...candidates];
  assert.deepEqual(await localPorts(() => again.shift()), candidates);
});
test("fixture allocation releases partial reservations on failure", async () => {
  const candidates = await localPorts();
  for (const mode of ["invalid", "exhausted"]) {
    let attempts = 0;
    await assert.rejects(
      localPorts(() => {
        attempts++;
        return attempts === 1 || mode === "exhausted" ? candidates[0] : 6000;
      }),
      mode === "invalid" ? /dynamic\/private/ : /Could not reserve/,
    );
    const again = [...candidates];
    assert.deepEqual(await localPorts(() => again.shift()), candidates);
  }
});
test("failed spawn and partial fixture startup clean up without a running service", async () => {
  const directory = await mkdtemp(join(tmpdir(), "ui-harness-test-"));
  try {
    const process = startProcess(join(directory, "missing"), []);
    assert.equal(await process.done, -1);
    assert.equal(JSON.parse(process.diagnostics()).spawnError, "ENOENT");
    await assert.rejects(ready("http://127.0.0.1:1", process), /ENOENT/);
    await stopProcess(process);
    await assert.rejects(startCatalog(directory, directory, ""));
    assert.deepEqual(await readdir(directory), []);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("owned process diagnostics retain only fixed chronology stages and bounded request IDs", async () => {
  const child = startProcess(
    process.execPath,
    [
      "-e",
      `
    console.log(JSON.stringify({event:"http.request_failed",error_code:"CONFLICT",operation_stage:"authorization.assignment.time_regression",request_id:"owned-request",detail:"PRIVATE_SENTINEL"}));
    console.log(JSON.stringify({event:"http.request_failed",error_code:"CONFLICT",operation_stage:"PRIVATE_SENTINEL",request_id:"PRIVATE_SENTINEL"}));
    console.log(JSON.stringify({event:"http.request_failed",error_code:"CONFLICT",operation_stage:"authorization.group.time_regression",request_id:"unsafe\\nPRIVATE_SENTINEL"}));
    console.log("PRIVATE_SENTINEL".repeat(3000));
    console.log(JSON.stringify({event:"http.request_failed",error_code:"CONFLICT",operation_stage:"authorization.group_member.time_regression",request_id:"late-request"}));
  `,
    ],
    { collectStages: true },
  );
  try {
    assert.equal(await child.done, 0);
    const evidence = JSON.parse(child.diagnostics());
    assert.equal(evidence.stdoutTruncated, true);
    assert.deepEqual(evidence.refusals, [
      {
        event: "http.request_failed",
        code: "CONFLICT",
        operationStage: "authorization.assignment.time_regression",
        requestId: "owned-request",
      },
      {
        event: "http.request_failed",
        code: "CONFLICT",
        operationStage: "authorization.group.time_regression",
      },
      {
        event: "http.request_failed",
        code: "CONFLICT",
        operationStage: "authorization.group_member.time_regression",
        requestId: "late-request",
      },
    ]);
    assert.equal(JSON.stringify(evidence).includes("PRIVATE_SENTINEL"), false);
  } finally {
    await stopProcess(child);
  }
});

test("owned process diagnostics retain only the last eight reviewed refusals", async () => {
  const child = startProcess(
    process.execPath,
    [
      "-e",
      `
    for (let index = 0; index < 12; index++) console.log(JSON.stringify({
      event: "http.request_failed", error_code: "CONFLICT",
      operation_stage: "authorization.assignment.time_regression",
      request_id: "owned-" + index, detail: "PRIVATE_SENTINEL"
    }));
  `,
    ],
    { collectStages: true },
  );
  try {
    assert.equal(await child.done, 0);
    const diagnostic = JSON.parse(child.diagnostics());
    assert.deepEqual(
      diagnostic.refusals.map((item) => item.requestId),
      Array.from({ length: 8 }, (_, index) => "owned-" + (index + 4)),
    );
    assert.equal(
      JSON.stringify(diagnostic).includes("PRIVATE_SENTINEL"),
      false,
    );
  } finally {
    await stopProcess(child);
  }
});

test("startup diagnostics classify a real occupied port without disclosing child output", async () => {
  const occupied = createServer();
  await new Promise((resolve) => occupied.listen(0, "127.0.0.1", resolve));
  let child;
  try {
    const port = occupied.address().port;
    child = startProcess(process.execPath, [
      "-e",
      `const server = require('node:net').createServer();
       server.on('error', error => {
         process.stderr.write('private-secret ' + error.code);
         process.exitCode = 23;
       });
       server.listen(${port}, '127.0.0.1');`,
    ]);
    assert.equal(await child.done, 23);
    assert.deepEqual(JSON.parse(child.diagnostics()), {
      exitCode: 23,
      signal: null,
      spawnError: "none",
      stderr: "address-in-use",
      truncated: false,
    });
    await assert.rejects(ready(`http://127.0.0.1:${port}`, child), (error) => {
      assert.match(error.message, /exited before readiness.*address-in-use/);
      assert.equal(error.message.includes("private-secret"), false);
      return true;
    });
    assert.equal(occupied.listening, true);
  } finally {
    if (child) await stopProcess(child);
    await new Promise((resolve) => occupied.close(resolve));
  }
});

test("startup diagnostics drain noisy stderr with a fixed retention limit", async () => {
  const child = startProcess(process.execPath, [
    "-e",
    "process.stderr.write('private-secret'.repeat(100000)); process.exitCode = 7;",
  ]);
  try {
    assert.equal(await child.done, 7);
    const diagnostic = JSON.parse(child.diagnostics());
    assert.equal(diagnostic.truncated, true);
    assert.equal(diagnostic.stderr, "unclassified");
    assert.ok(child.diagnostics().length < 256);
    assert.equal(child.diagnostics().includes("private-secret"), false);
  } finally {
    await stopProcess(child);
  }
});

test(
  "selected archives own TLS, isolate default seeding, stop and clean on success and startup failure",
  { timeout: 90000 },
  async () => {
    const directory = await mkdtemp(join(tmpdir(), "ui-archive-lifecycle-"));
    const root = fileURLToPath(new URL("../../", import.meta.url));
    let catalog;
    try {
      for (const [name, pkg] of [
        ["catalogd", "./cmd/catalogd"],
        ["catalogctl", "./cmd/catalogctl"],
        ["catalog-ui", "./catalog-ui/cmd/catalog-ui"],
        ["demo-archives", "./test/demo"],
      ]) {
        const build = startProcess(
          "go",
          [
            "build",
            "-mod=readonly",
            ...(name === "catalogd"
              ? ["-ldflags", `-X main.version=${basename(directory)}`]
              : []),
            "-o",
            join(directory, name),
            pkg,
          ],
          { cwd: root, timeout: 30000 },
        );
        try {
          assert.equal(await build.done, 0, build.diagnostics());
        } finally {
          await stopProcess(build);
        }
      }
      await mkdir(join(directory, "assets/assets"), { recursive: true });
      await writeFile(
        join(directory, "assets/assets/fixture.js"),
        "export const ok = true;",
      );
      await writeFile(
        join(directory, "assets/index.html"),
        '<script src="./assets/fixture.js"></script>',
      );
      catalog = await startCatalog(directory, root, "");
      assert.equal(catalog.archive, undefined);
      assert.equal(catalog.receipt.items.length, 5);
      const healthy = catalog.diagnostics();
      for (const role of ["api", "ui"]) {
        assert.equal(healthy[role].exitCode, null);
        assert.equal(healthy[role].signal, null);
        assert.equal(healthy[role].spawnError, "none");
      }
      await catalog.close();
      for (const state of Object.values(catalog.diagnostics()))
        assert.ok(state.exitCode !== null || state.signal !== null);
      catalog = await startCatalog(directory, root, "", { demoArchives: true });
      assert.equal(catalog.receipt.items.length, 5);
      assert.equal(
        catalog.receipt.items.some((item) => item.key === "release-workflow"),
        false,
      );
      const caseName = (await readdir(directory)).find((name) =>
        name.startsWith("case-"),
      );
      const cert = await readFile(
        join(directory, caseName, "archives/cert.pem"),
      );
      assert.match(cert.toString(), /BEGIN CERTIFICATE/);
      const port = Number(new URL(catalog.archive.url).port);
      await catalog.stopArchives();
      await catalog.stopArchives();
      const reservation = createServer();
      const archiveDirectory = join(directory, caseName, "archives");
      const failedArchive = async (input, expectedStage) => {
        const child = startProcess(join(directory, "demo-archives"), input, {
          cwd: root,
          timeout: 15000,
        });
        try {
          assert.equal(await child.done, 1);
          const evidence = JSON.parse(child.diagnostics());
          assert.equal(evidence.operationStage, expectedStage);
          assert.equal(evidence.stderr, "classified");
          assert.equal(child.diagnostics().includes(archiveDirectory), false);
          assert.equal(child.diagnostics().includes("PRIVATE_SENTINEL"), false);
        } finally {
          await stopProcess(child);
        }
      };
      const listenerArgs = [
        "--directory",
        archiveDirectory,
        "--address",
        `127.0.0.1:${port}`,
      ];
      await failedArchive(
        ["--directory", join(directory, "PRIVATE_SENTINEL")],
        "prepared.read",
      );
      const keyPath = join(archiveDirectory, "key.pem");
      try {
        await chmod(keyPath, 0o644);
        await failedArchive(listenerArgs, "tls.prepare");
      } finally {
        await chmod(keyPath, 0o600);
      }
      try {
        await new Promise((resolve, reject) => {
          reservation.once("error", reject);
          reservation.listen(port, "127.0.0.1", resolve);
        });
        await failedArchive(listenerArgs, "listener.address-in-use");
        assert.equal(reservation.listening, true);
      } finally {
        await new Promise((resolve) => reservation.close(resolve));
      }
      await catalog.close();
      assert.equal(
        (await readdir(directory)).some((name) => name.startsWith("case-")),
        false,
      );
      // Preparation failure must leave no private files or listener and must not touch the default services.
      const fakeRoot = join(directory, "wrong-source");
      await mkdir(join(fakeRoot, "config"), { recursive: true });
      await writeFile(
        join(fakeRoot, "config/local.json"),
        await readFile(join(root, "config/local.json")),
      );
      const packageRoot = join(fakeRoot, "samples/packages/release-workflow");
      await mkdir(join(packageRoot, "skills/prepare-release"), {
        recursive: true,
      });
      for (const name of [
        "mcp.json",
        "plugin.json",
        "skills/prepare-release/SKILL.md",
      ])
        await writeFile(
          join(packageRoot, name),
          name === "plugin.json"
            ? "wrong-source"
            : await readFile(
                join(root, "samples/packages/release-workflow", name),
              ),
        );
      const previousOptimize = process.env.PYTHONOPTIMIZE;
      try {
        process.env.PYTHONOPTIMIZE = "1";
        await assert.rejects(
          startCatalog(directory, fakeRoot, "", { demoArchives: true }),
          /document oracle failed/,
        );
      } finally {
        if (previousOptimize === undefined) delete process.env.PYTHONOPTIMIZE;
        else process.env.PYTHONOPTIMIZE = previousOptimize;
      }
      assert.equal(
        (await readdir(directory)).some((name) => name.startsWith("case-")),
        false,
      );
      await rm(join(directory, "catalog-ui"));
      await assert.rejects(
        startCatalog(directory, root, "", { demoArchives: true }),
        /ENOENT/,
      );
      assert.equal(
        (await readdir(directory)).some((name) => name.startsWith("case-")),
        false,
      );
      await rm(join(directory, "demo-archives"));
      await assert.rejects(
        startCatalog(directory, root, "", { demoArchives: true }),
        /archive preparation failed/,
      );
      assert.equal(
        (await readdir(directory)).some((name) => name.startsWith("case-")),
        false,
      );
    } finally {
      if (catalog) await catalog.close();
      await rm(directory, { recursive: true, force: true });
    }
  },
);
