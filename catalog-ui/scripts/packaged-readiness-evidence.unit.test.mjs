import assert from "node:assert/strict";
import test from "node:test";
import { collectReadinessEvidence } from "./packaged-readiness-evidence.mjs";

const project = "catalog-ui-owned-abc123";
const id = "a".repeat(64);
const container = {
  Project: project,
  Service: "ui-browser",
  ID: id,
  State: "running",
  Health: "",
};
const listener = {
  interrupted: false,
  elapsedMs: 120000,
  lastError: "connection_refused",
  lastStatus: undefined,
};
function options(overrides = {}) {
  return {
    project,
    service: "ui-browser",
    phase: "ui_restart",
    attemptedPort: 41000,
    listener,
    run: async (args) =>
      args[0] === "ps"
        ? JSON.stringify(container)
        : args[0] === "port"
          ? "127.0.0.1:42000\n"
          : "private probe output",
    ...overrides,
  };
}

test("readiness evidence binds exact project/service and reads current mapping separately from attempted port", async () => {
  const calls = [];
  const input = options();
  const result = await collectReadinessEvidence({
    ...input,
    now: () => 0,
    run: async (args, timeout) => {
      calls.push({ args, timeout });
      return input.run(args);
    },
  });
  assert.deepEqual(calls, [
    { args: ["ps", "--all", "--format", "json", "ui-browser"], timeout: 3000 },
    { args: ["port", "ui-browser", "8080"], timeout: 3000 },
    {
      args: [
        "exec",
        "-T",
        "ui-browser",
        "/usr/local/bin/catalog-ui",
        "--probe",
      ],
      timeout: 3000,
    },
  ]);
  assert.deepEqual(result, {
    service: "ui-browser",
    phase: "ui_restart",
    interrupted: false,
    elapsedMs: 0,
    attemptedPort: 41000,
    listener,
    container: { status: "available", id, state: "running", health: "none" },
    published: {
      status: "available",
      host: "loopback",
      port: 42000,
      containerPort: 8080,
    },
    internalProbe: { status: "ready" },
  });
});

test("API internal probes use only the existing fixed local readiness endpoint", async () => {
  const calls = [];
  await collectReadinessEvidence(
    options({
      service: "api",
      run: async (args) => {
        calls.push(args);
        return "";
      },
    }),
  );
  assert.deepEqual(calls.at(-1), [
    "exec",
    "-T",
    "api",
    "/usr/local/bin/catalogctl",
    "probe",
    "--url",
    "http://127.0.0.1:8080/readyz",
  ]);
  for (const input of [
    { service: "foreign" },
    { project: "private/foreign" },
    { project: 123 },
  ]) {
    let executed = false;
    await assert.rejects(
      collectReadinessEvidence(
        options({
          ...input,
          run: async () => {
            executed = true;
          },
        }),
      ),
      /Invalid owned readiness diagnostic target/,
    );
    assert.equal(executed, false);
  }
});

test("foreign, duplicate and invalid container rows cannot become owned evidence", async () => {
  for (const value of [
    { ...container, Project: "foreign-private-project" },
    { ...container, Service: "foreign-private-service" },
    { ...container, State: "private-state" },
    { ...container, Health: "private-health" },
    { ...container, ID: "private-id" },
    { ...container, ID: 123456789012 },
    [container, container],
    null,
    "private-message",
  ]) {
    const result = await collectReadinessEvidence(
      options({
        run: async (args) => (args[0] === "ps" ? JSON.stringify(value) : ""),
      }),
    );
    assert.deepEqual(result.container, { status: "invalid" });
    assert.doesNotMatch(JSON.stringify(result), /private/);
  }
  for (const text of ["", "[]"]) {
    const result = await collectReadinessEvidence(
      options({ run: async () => text }),
    );
    assert.deepEqual(result.container, { status: "missing" });
  }
});

test("unscoped ports and private diagnostic fields are omitted while independent stages still run", async () => {
  for (const mapping of [
    "0.0.0.0:42000",
    "[::]:42000",
    "127.0.0.1:0",
    "127.0.0.1:65536",
    "127.0.0.1:42000\n127.0.0.1:42001",
    "private-url",
  ]) {
    const result = await collectReadinessEvidence(
      options({
        phase: "private-phase",
        attemptedPort: -1,
        listener: {
          elapsedMs: "private",
          lastStatus: "private",
          lastError: "private-error",
          privateBody: "private-body",
        },
        run: async (args) => {
          if (args[0] === "ps")
            return JSON.stringify({
              ...container,
              private: "private-label",
              Command: "private-token",
            });
          if (args[0] === "port") return mapping;
          throw new Error("private-probe-url private-body");
        },
      }),
    );
    assert.equal(result.container.status, "available");
    assert.deepEqual(result.published, { status: "invalid" });
    assert.deepEqual(result.internalProbe, { status: "failed" });
    assert.equal(result.phase, "other");
    assert.doesNotMatch(JSON.stringify(result), /private/);
  }
});

test("missing engine evidence cannot suppress the current port or independent internal probe", async () => {
  const input = options();
  const result = await collectReadinessEvidence({
    ...input,
    run: async (args) => {
      if (args[0] === "ps") throw new Error("private engine output");
      return input.run(args);
    },
  });
  assert.deepEqual(result.container, { status: "unavailable" });
  assert.equal(result.published.port, 42000);
  assert.equal(result.internalProbe.status, "ready");
});

test("diagnostics obey a separate monotonic ten-second budget and honor interruption", async () => {
  let at = 0;
  const timeouts = [];
  const result = await collectReadinessEvidence(
    options({
      now: () => at,
      run: async (_args, timeout) => {
        timeouts.push(timeout);
        at += timeouts.length === 1 ? 9500 : 500;
        throw new Error("private");
      },
    }),
  );
  assert.deepEqual(timeouts, [3000, 500]);
  assert.equal(result.elapsedMs, 10000);
  assert.equal(result.published.status, "budget_exhausted");
  assert.equal(result.internalProbe.status, "budget_exhausted");
  let calls = 0;
  const interrupted = await collectReadinessEvidence(
    options({
      interrupted: () => true,
      run: async () => {
        calls++;
      },
    }),
  );
  assert.equal(calls, 0);
  assert.equal(interrupted.interrupted, true);
  for (const key of ["container", "published", "internalProbe"])
    assert.equal(interrupted[key].status, "interrupted");
  let stop = false;
  calls = 0;
  const during = await collectReadinessEvidence(
    options({
      interrupted: () => stop,
      run: async () => {
        calls++;
        stop = true;
        return JSON.stringify(container);
      },
    }),
  );
  assert.equal(calls, 1);
  for (const key of ["container", "published", "internalProbe"])
    assert.equal(during[key].status, "interrupted");
});
