import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, readdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  archiveReceipt,
  browserSelections,
  localPorts,
  runtimeEnvironment,
  startCatalog,
  startupFailureStage,
} from "./browser-fixture.mjs";

test("demo startup stages accept only complete fixed messages", () => {
  for (const stage of [
    "sample.load",
    "prepared.write",
    "prepared.read",
    "tls.prepare",
    "tls.not-yet-valid",
    "tls.expired",
    "tls.hostname",
    "listener.address",
    "listener.address-in-use",
    "listener.serve",
  ]) {
    assert.equal(
      startupFailureStage(`Demo sample tool failed (stage: ${stage}).\n`),
      stage,
    );
    assert.equal(
      startupFailureStage(
        `private text\nDemo sample tool failed (stage: ${stage}).\r\n`,
      ),
      stage,
    );
  }
  for (const text of [
    "",
    "private-secret",
    "Demo sample tool failed (stage: unclassified).\n",
    "Demo sample tool failed (stage: private-secret).\n",
    "Demo sample tool failed (stage: tls.prepare). private-secret\n",
    "prefix Demo sample tool failed (stage: prepared.read).\n",
    "Demo sample tool failed (stage: listener.serve)",
  ])
    assert.equal(startupFailureStage(text), undefined);
});

test("runtime fixtures cannot inherit a configured database, identity or target", () => {
  const env = {
    PATH: "/bin",
    CATALOG_DATABASE_DSN: "unrelated",
    CATALOG_CONFIG_FILE: "other.json",
    CATALOG_HTTP_ADDRESS: "other:8080",
    CATALOG_TRACING: "true",
    CATALOG_UI_VISUAL: "1",
  };
  assert.deepEqual(runtimeEnvironment(env), { PATH: "/bin" });
  assert.equal(env.CATALOG_DATABASE_DSN, "unrelated");
});
test("fixture port allocation rejects invalid candidates before opening a socket", async () => {
  await assert.rejects(
    localPorts(() => 6000),
    /dynamic\/private/,
  );
});
test("fixture startup rejects invalid options before allocation", async () => {
  const directory = await mkdtemp(join(tmpdir(), "ui-harness-test-"));
  try {
    await assert.rejects(
      startCatalog(directory, directory, "/unsupported"),
      /Unsupported/,
    );
    await assert.rejects(
      startCatalog(directory, directory, "", { publisherVerification: "true" }),
      /Invalid publisher verification fixture option/,
    );
    await assert.rejects(
      startCatalog(directory, directory, "", { serviceActivity: "true" }),
      /Invalid service activity fixture option/,
    );
    await assert.rejects(
      startCatalog(directory, directory, "", { webhooks: "true" }),
      /Invalid webhook fixture option/,
    );
    await assert.rejects(
      startCatalog(directory, directory, "", { maintainedIdentities: "true" }),
      /Invalid maintained identity fixture option/,
    );
    await assert.rejects(
      startCatalog(directory, directory, "", { demoArchives: "true" }),
      /Invalid demo archive/,
    );
    for (const options of [
      { authoredPlugin: "true" },
      { authoredPlugin: true },
    ])
      await assert.rejects(
        startCatalog(directory, directory, "", options),
        /Invalid authored Plugin/,
      );
    await assert.rejects(
      startCatalog(directory, directory, "", { artwork: "true" }),
      /Invalid artwork fixture option/,
    );
    await assert.rejects(
      startCatalog(directory, directory, "", { publicationAudiences: "true" }),
      /Invalid publication audience fixture option/,
    );
    for (const options of [
      { curated: "true" },
      { curated: true, artwork: true },
      { curated: true, demoArchives: true },
    ])
      await assert.rejects(
        startCatalog(directory, directory, "", options),
        /Invalid curated fixture option/,
      );
    await assert.rejects(
      startCatalog(directory, directory, "", { performanceScale: "0" }),
      /integer/,
    );
    assert.deepEqual(await readdir(directory), []);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("archive receipt rejects wrong prepared tenant, size, checksum and missing selected path", () => {
  const bytes = Buffer.from("abc");
  const documents = [
    "mcp.json",
    "plugin.json",
    "skills/prepare-release/SKILL.md",
  ].map((path) => ({ path, content: "abc" }));
  const entry = {
    path: "/release-workflow.zip",
    digest:
      "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
    size: 3,
  };
  const allowlist = {
    format: 1,
    tenantId: "00000000-0000-7000-8000-000000000001",
    entries: [entry],
  };
  assert.equal(
    archiveReceipt("https://127.0.0.1:55123", allowlist, bytes, documents)
      .digest,
    entry.digest,
  );
  for (const invalid of [
    { ...allowlist, tenantId: "other" },
    { ...allowlist, format: 2 },
    { ...allowlist, entries: [] },
    { ...allowlist, entries: [{ ...entry, size: 4 }] },
    {
      ...allowlist,
      entries: [{ ...entry, digest: "sha256:" + "0".repeat(64) }],
    },
  ])
    assert.throws(
      () =>
        archiveReceipt("https://127.0.0.1:55123", invalid, bytes, documents),
      /identity mismatch/,
    );
});

test("ordinary browser selection covers both native shards without changing explicit or exclusive requests", () => {
  assert.deepEqual(browserSelections([], false), [
    ["--shard=1/2"],
    ["--shard=2/2"],
  ]);
  for (const args of [["--grep", "density"], ["--shard=2/2"], ["--list"]]) {
    const before = [...args];
    assert.deepEqual(browserSelections(args, false), [before]);
    assert.deepEqual(args, before);
  }
  assert.deepEqual(browserSelections([], true), [[]]);
  assert.deepEqual(browserSelections(["--grep", "Overview"], true), [
    ["--grep", "Overview"],
  ]);
});
