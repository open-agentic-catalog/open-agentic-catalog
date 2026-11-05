// Real pinned-tool qualification on disposable files, never on a running API.
import assert from "node:assert/strict";
import { test } from "node:test";
import { mkdtemp, writeFile, rm, readFile, mkdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { compare, lint, lintSnapshot, CONTROL, ROOT, DIRECTORY, prerequisites } from "./runner.mjs";
import { snapshot, digest } from "./input.mjs";
import { execute } from "./process.mjs";

function fixture() {
  return {
    openapi: "3.1.0", info: { title: "Qualification fixture", version: "1" },
    tags: [{ name: "items", description: "Fixture operations" }],
    paths: {
      "/items": {
        parameters: [{ name: "mode", in: "query", schema: { type: "string", enum: ["a", "b"] } }],
        get: { operationId: "listItems", summary: "List items", tags: ["items"], security: [],
          responses: { "200": { description: "Items", content: { "application/json": {
            schema: { $ref: "#/components/schemas/Item" }, example: { name: "a" },
          } } } },
        },
      },
    },
    components: { schemas: { Item: { type: "object", required: ["name"], properties: { name: { type: "string", enum: ["a", "b"] } } } } },
  };
}
const operation = (doc) => doc.paths["/items"].get;
const schema = (doc) => doc.components.schemas.Item;
const media = (doc) => operation(doc).responses["200"].content["application/json"];

async function owned(fn) {
  const dir = await mkdtemp(path.join(tmpdir(), "catalog-api-tool-test-"));
  const file = async (name, document) => {
    const filename = path.join(dir, name + ".json");
    await writeFile(filename, JSON.stringify(document));
    return filename;
  };
  try { return await fn(dir, file); }
  finally { await rm(dir, { recursive: true, force: true }); }
}

test("installed tool identity and authoritative contract pass without changes", async () => {
  await prerequisites();
  const before = await readFile(CONTROL);
  assert.equal((await lint(CONTROL, false)).totals.errors, 0);
  assert.deepEqual(await readFile(CONTROL), before);
});

test("Redocly catches structural, documentation, reference and example defects", async (t) => {
  const cases = [
    ["missing info", (doc) => { delete doc.info; }, "struct"],
    ["missing summary", (doc) => { delete operation(doc).summary; }, "operation-summary"],
    ["missing operation ID", (doc) => { delete operation(doc).operationId; }, "operation-operationId"],
    ["duplicate operation ID", (doc) => { doc.paths["/other"] = structuredClone(doc.paths["/items"]); }, "operation-operationId-unique"],
    ["unresolved local reference", (doc) => { media(doc).schema.$ref = "#/components/schemas/Missing"; }, "no-unresolved-refs"],
    ["undeclared path parameter", (doc) => { doc.paths["/{id}"] = doc.paths["/items"]; delete doc.paths["/items"]; }, "path-parameters-defined"],
    ["duplicate parameters", (doc) => { doc.paths["/items"].parameters.push(structuredClone(doc.paths["/items"].parameters[0])); }, "operation-parameters-unique"],
    ["invalid example", (doc) => { media(doc).example.name = 5; }, "no-invalid-media-type-examples"],
    ["invalid schema keyword", (doc) => { schema(doc).properties.name.type = "not-a-type"; }, "struct"],
    ["invalid parameter example", (doc) => { doc.paths["/items"].parameters[0].example = "outside-enum"; }, "no-invalid-parameter-examples"],
    ["invalid schema example", (doc) => { schema(doc).example = { name: 5 }; }, "no-invalid-schema-examples"],
    ["undefined security scheme", (doc) => { operation(doc).security = [{ missingScheme: [] }]; }, "security-defined"],
    ["closed object example", (doc) => { schema(doc).additionalProperties = false; media(doc).example.extra = true; }, "no-invalid-media-type-examples"],
    ["unevaluated object example", (doc) => { schema(doc).unevaluatedProperties = false; media(doc).example.extra = true; }, "no-invalid-media-type-examples"],
  ];
  await owned(async (_dir, file) => {
    for (const [name, mutate, rule] of cases) await t.test(name, async () => {
      const doc = fixture(); mutate(doc);
      const filename = await file(name, doc);
      const before = await readFile(filename);
      await assert.rejects(lint(filename, false), (error) => {
        assert.equal(error.code, 1);
        assert.ok(error.findings?.some((finding) => finding.ruleId === rule), JSON.stringify(error.findings));
        return true;
      });
      assert.deepEqual(await readFile(filename), before);
    });
  });
});

test("Redocly accepts maintained 3.1 shapes and does not resolve example data", async (t) => {
  const cases = [
    ["open object", (doc) => { media(doc).example.extra = true; }],
    ["nullable type", (doc) => { schema(doc).properties.name = { type: ["string", "null"] }; media(doc).example.name = null; }],
    ["numeric exclusive minimum", (doc) => { schema(doc).properties.name = { type: "number", exclusiveMinimum: 1 }; media(doc).example.name = 2; }],
    ["recursive reference", (doc) => { schema(doc).properties.child = { $ref: "#/components/schemas/Item" }; }],
    ["reference siblings", (doc) => { media(doc).schema.description = "Local reference with description"; }],
    ["literal example reference", (doc) => { media(doc).example.$ref = "https://example.invalid/not-a-reference"; }],
    ["named example reference data", (doc) => { media(doc).examples = { sample: { value: { name: "a", $ref: "https://example.invalid/data" } } }; delete media(doc).example; }],
  ];
  await owned(async (_dir, file) => {
    for (const [name, mutate] of cases) await t.test(name, async () => {
      const doc = fixture(); mutate(doc);
      assert.equal((await lint(await file(name, doc), false)).totals.errors, 0);
    });
  });
});

test("known tool limitations stay explicit, not hidden by disabling structural rules", async () => {
  await owned(async (_dir, file) => {
    const boolean = fixture();
    boolean.components.schemas.Item = false;
    delete media(boolean).example;
    await assert.rejects(lint(await file("referenced-boolean", boolean), false), (error) => {
      assert.ok(error.findings?.some((finding) => finding.ruleId === "struct"));
      return true;
    });
    const integer = fixture();
    schema(integer).properties.name = { type: "integer", maximum: "MAXIMUM" };
    media(integer).example.name = "EXAMPLE";
    const filename = await file("large-integer", integer);
    await writeFile(filename, JSON.stringify(integer).replace('"MAXIMUM"', "9007199254740992").replace('"EXAMPLE"', "9007199254740993"));
    // Characterization: this invalid example currently passes due to rounding.
    assert.equal((await lint(filename, false)).totals.errors, 0);
  });
});

test("oasdiff reports changes and fails only the explicit breaking gate", async (t) => {
  const cases = [
    ["identical", () => {}, false],
    ["description only", (doc) => { operation(doc).description = "More detail"; }, false],
    ["removed operation", (doc) => { delete doc.paths["/items"]; }, true],
    ["narrow inherited request enum", (doc) => { doc.paths["/items"].parameters[0].schema.enum = ["a"]; }, true],
    ["expand inherited request enum", (doc) => { doc.paths["/items"].parameters[0].schema.enum.push("c"); }, false],
    ["new inherited required parameter", (doc) => { doc.paths["/items"].parameters.push({ name: "required", in: "query", required: true, schema: { type: "string" } }); }, true],
    ["expand response enum", (doc) => { schema(doc).properties.name.enum.push("c"); }, true],
    ["narrow response enum", (doc) => { schema(doc).properties.name.enum = ["a"]; }, false],
    ["equivalent inline schema", (doc) => { media(doc).schema = schema(doc); delete doc.components; }, false],
  ];
  await owned(async (_dir, file) => {
    const baseline = await file("base", fixture());
    for (const [name, mutate, breaks] of cases) await t.test(name, async () => {
      const candidate = fixture(); mutate(candidate);
      const filename = await file(name, candidate);
      if (breaks) {
        const changes = await compare(baseline, filename, false, false);
        assert.ok(changes.length > 0);
        await assert.rejects(compare(baseline, filename, true, false), (error) => {
          assert.equal(error.code, 1);
          assert.ok(error.changes?.length > 0);
          return true;
        });
      } else assert.deepEqual(await compare(baseline, filename, true, false), []);
    });
    const base = fixture();
    operation(base).parameters = [{ name: "mode", in: "query", schema: { type: "string", enum: ["override"] } }];
    const next = structuredClone(base);
    next.paths["/items"].parameters[0].schema.enum = ["a"];
    assert.deepEqual(await compare(await file("override-base", base), await file("override-next", next), true, false), []);
  });
});

test("invalid configuration, missing input and unavailable installation fail", async () => {
  await owned(async (dir, file) => {
    const input = await snapshot(await file("valid", fixture()));
    const config = path.join(dir, "redocly.yaml");
    await writeFile(config, 'extends: [spec]\nresolve:\n  doNotResolveExamples: true\nrules:\n  not-a-real-rule: error\n');
    await assert.rejects(lintSnapshot(input, dir, config, false));
    await assert.rejects(lint(path.join(dir, "missing.json"), false), /cannot read/);
    // The launcher can explain missing dependencies without importing YAML first.
    for (const name of ["runner.mjs", "process.mjs"])
      await writeFile(path.join(dir, name), await readFile(path.join(DIRECTORY, name)));
    const result = await execute(process.execPath, [path.join(dir, "runner.mjs"), "lint"]);
    assert.equal(result.code, 2);
    assert.match(result.stderr, /make validation-tools-install/);
    // A lock change, altered executable or wrong direct-package version must
    // require explicit reinstallation, even when an installation stamp exists.
    const locks = {};
    const packages = { dependencies: { "@redocly/cli": "2.52.1", yaml: "2.9.1" } };
    await mkdir(path.join(dir, "tools"));
    await mkdir(path.join(dir, ".tools"));
    for (const [name, content] of Object.entries({
      "package.json": JSON.stringify(packages), "package-lock.json": "{}",
      "tools/go.mod": "fixture", "tools/go.sum": "fixture",
    })) { await writeFile(path.join(dir, name), content); locks[name] = digest(content); }
    const binary = path.join(dir, ".tools", process.platform === "win32" ? "oasdiff.exe" : "oasdiff");
    await writeFile(binary, "fixture-binary");
    for (const [name, version] of Object.entries(packages.dependencies)) {
      const location = path.join(dir, "node_modules", name);
      await mkdir(location, { recursive: true });
      await writeFile(path.join(location, "package.json"), JSON.stringify({ version }));
    }
    const stamp = { locks, binary: digest("fixture-binary") };
    const check = () => execute(process.execPath, ["--input-type=module", "-e",
      'import { prerequisites } from "./runner.mjs"; await prerequisites();'], { cwd: dir });
    await writeFile(path.join(dir, ".tools/installed.json"), JSON.stringify(stamp));
    assert.equal((await check()).code, 0);
    for (const [name, bad] of [["tools/go.sum", "changed"], [path.relative(dir, binary), "changed"],
      ["node_modules/yaml/package.json", '{"version":"0.0.0"}']]) {
      const filename = path.join(dir, name);
      const original = await readFile(filename);
      await writeFile(filename, bad);
      assert.match((await check()).stderr, /make validation-tools-install/);
      await writeFile(filename, original);
    }
  });
});

test("Make API-only targets ignore environment files and reject mixed workflows", async () => {
  await owned(async (dir) => {
    const envFile = path.join(dir, "unsafe.env");
    await writeFile(envFile, "$(error environment file must not be evaluated)\n");
    const args = ["-n", `CATALOG_ENV_FILE=${envFile}`];
    const safe = await execute("make", [...args, "api-check", "api-contract-check"], { cwd: ROOT });
    assert.equal(safe.code, 0, safe.stderr);
    for (const goals of [["api-check", "run"], ["validation-tools-install", "api-check"]]) {
      const result = await execute("make", [...args, ...goals], { cwd: ROOT });
      assert.notEqual(result.code, 0);
      assert.doesNotMatch(result.stderr, /environment file must not be evaluated/);
    }
  });
});
