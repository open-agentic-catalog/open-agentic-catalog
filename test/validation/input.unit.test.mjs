// Safety tests do not invoke a validator, database or running Catalog.
import assert from "node:assert/strict";
import { test } from "node:test";
import { mkdtemp, writeFile, rm, symlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { parseInput, preflight, readBounded, snapshot, unchanged, INPUT_LIMIT } from "./input.mjs";
import { cleanEnvironment } from "./process.mjs";
import { main, validateRules } from "./runner.mjs";

const bytes = (value) => Buffer.from(typeof value === "string" ? value : JSON.stringify(value));
const spec = (extra = {}) => ({ openapi: "3.1.0", info: { title: "Fixture", version: "1" }, paths: {}, ...extra });

test("preflight preserves precise integers, schema semantics and literal data", () => {
  assert.equal(parseInput(bytes("number: 9007199254740993")).number, 9007199254740993n);
  const schemas = {
    Closed: false,
    Node: { type: ["object", "null"], properties: { next: { $ref: "#/components/schemas/Node" }, $ref: { type: "string" } } },
    Data: { examples: [{ $ref: "https://example.invalid/data" }], default: { $id: "data" } },
  };
  const value = spec({ components: { schemas, examples: { sample: { value: { $ref: "https://example.invalid/data" } } } } });
  value.example = { $ref: "https://example.invalid/data" };
  assert.deepEqual(preflight(bytes(value)), value);
});

test("ambiguous, non-JSON and oversized inputs fail before tool execution", () => {
  for (const raw of [
    "openapi: 3.1.0\nopenapi: 3.1.0", "a: 1\n---\na: 2", "a: &a {}\nb: *a",
    "a: !!str x", "a: !custom x", "1: value", "a: .nan", "a: .inf", "[", "",
    Buffer.alloc(INPUT_LIMIT + 1),
    "a: ".repeat(110) + "{}",
  ]) assert.throws(() => parseInput(Buffer.isBuffer(raw) ? raw : bytes(raw)));
  assert.throws(() => parseInput(Buffer.from([0xff])), /parsed safely/);
  for (const value of [null, [], spec({ openapi: "3.0.3" })])
    assert.throws(() => preflight(bytes(value)), /OpenAPI 3.1.0/);
});

test("every maintained reference position rejects external resolution", () => {
  const external = { $ref: "https://example.invalid/private" };
  for (const extra of [
    { paths: { "/": external } },
    { components: { parameters: { example: external } } },
    { components: { responses: { default: external } } },
    { components: { examples: { sample: { externalValue: "file:///private" } } } },
    { components: { links: { example: { operationRef: "https://example.invalid/api" } } } },
    ...["properties", "$defs", "patternProperties", "dependentSchemas"].map((key) => ({ components: { schemas: { A: { [key]: { field: external } } } } })),
    ...["items", "additionalProperties", "unevaluatedProperties", "not", "if", "then", "else", "contains"].map((key) => ({ components: { schemas: { A: { [key]: external } } } })),
    ...["allOf", "anyOf", "oneOf", "prefixItems"].map((key) => ({ components: { schemas: { A: { [key]: [external] } } } })),
    ...["$id", "$dynamicRef", "$recursiveRef", "$schema"].map((key) => ({ components: { schemas: { A: { [key]: "https://example.invalid/schema" } } } })),
    { components: { schemas: { A: { discriminator: { mapping: { a: "remote.yaml" } } } } } },
    { jsonSchemaDialect: "https://example.invalid/dialect" },
  ]) assert.throws(() => preflight(bytes(spec(extra))));
  for (const ref of ["file:///private", "relative.yaml", "#anchor", 42])
    assert.throws(() => preflight(bytes(spec({ components: { schemas: { A: { $ref: ref } } } }))));
});

test("file reads are bounded, regular and checked for changes", async () => {
  const dir = await mkdtemp(path.join(tmpdir(), "catalog-api-input-test-"));
  try {
    const file = path.join(dir, "api.json");
    await writeFile(file, bytes(spec()));
    const input = await snapshot(file);
    await unchanged(input);
    await symlink(file, path.join(dir, "link"));
    assert.equal((await snapshot(path.join(dir, "link"))).filename, file);
    await assert.rejects(readBounded(file, 2), /bounded regular file/);
    await assert.rejects(readBounded(dir), /bounded regular file/);
    await assert.rejects(readBounded(path.join(dir, "missing")), /cannot read/);
    await writeFile(file, bytes(spec({ info: { title: "Changed", version: "1" } })));
    await assert.rejects(unchanged(input), /changed/);

  } finally { await rm(dir, { recursive: true, force: true }); }
});

test("configuration cannot enable plugins, remote presets or example resolution", () => {
  const config = { extends: ["spec"], resolve: { doNotResolveExamples: true }, rules: { struct: "error" } };
  validateRules(config);
  for (const change of [
    { plugins: ["./plugin.js"] }, { extends: ["https://example.invalid/rules"] },
    { resolve: {} }, { rules: { struct: { severity: "error", extra: true } } },
    { rules: { struct: "invalid" } },
  ]) assert.throws(() => validateRules({ ...config, ...change }));
});

test("subprocess environment excludes runtime credentials and loader/proxy overrides", () => {
  const source = { PATH: "/bin", HOME: "/home/example", DATABASE_URL: "secret", NODE_OPTIONS: "--require=bad", HTTP_PROXY: "private", REDOCLY_TELEMETRY: "on" };
  const env = cleanEnvironment(source);
  assert.deepEqual(env, { PATH: "/bin", REDOCLY_TELEMETRY: "off", REDOCLY_SUPPRESS_UPDATE_NOTICE: "true", NO_COLOR: "1" });
  assert.equal(cleanEnvironment(source, true).HTTP_PROXY, "private");
  assert.equal(cleanEnvironment(source, true).NODE_OPTIONS, undefined);
});

test("CLI rejects unknown commands and implicit/remote baselines", async () => {
  await assert.rejects(main([]), /usage/);
  await assert.rejects(main(["unknown"]), /unknown/);
  const { compare } = await import("./runner.mjs");
  for (const base of [undefined, "relative.yaml", "https://example.invalid/api"])
    await assert.rejects(compare(base), /absolute local/);
});
