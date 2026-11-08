// Package-tool configuration admission does not inspect or execute the selected tools.
import test from "node:test";
import assert from "node:assert/strict";
import { tmpdir } from "node:os";
import { join } from "node:path";

const { packageTools } = await import(process.env.CATALOG_VSCODE_LIB);
const paths = {
  gitPath: join(tmpdir(), "catalog unit tools", "git"),
  orasPath: join(tmpdir(), "catalog unit tools", "oras"),
  caFile: join(tmpdir(), "catalog unit trust", "ca.pem"),
};

test("package-tool admission preserves explicit absolute paths and inclusive timeout bounds", () => {
  for (const timeoutMs of [1000, 300000]) {
    const input = Object.freeze({ ...paths, timeoutMs });
    const result = packageTools(input);
    assert.deepEqual(result, { ...paths, timeoutMs });
    assert.notEqual(result, input);
    assert.deepEqual(input, { ...paths, timeoutMs });
  }
});

test("package-tool paths refuse controls and process options before any execution", () => {
  const valid = { ...paths, timeoutMs: 60000 };
  for (const field of ["gitPath", "orasPath", "caFile"])
    for (const value of [
      "",
      "tool",
      "./tool",
      ...["\0", "\t", "\n", "\r", "\x7f"].map(
        (control) => paths[field] + control,
      ),
    ])
      assert.throws(() => packageTools({ ...valid, [field]: value }), {
        code: "invalid_package_tools",
      });
  for (const extra of [
    { env: { CATALOG_TOKEN: "synthetic-private" } },
    { cwd: tmpdir() },
    { proxyURL: "https://proxy.example" },
  ])
    assert.throws(() => packageTools({ ...valid, ...extra }), {
      code: "invalid_package_tools",
    });
});

test("package-tool timeouts reject fractional, unbounded and coerced values", () => {
  for (const timeoutMs of [
    999,
    300001,
    1000.5,
    NaN,
    Infinity,
    -Infinity,
    "60000",
    false,
  ])
    assert.throws(() => packageTools({ ...paths, timeoutMs }), {
      code: "invalid_skill_package",
    });
});
