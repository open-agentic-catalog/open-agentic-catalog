import test from "node:test";
import assert from "node:assert/strict";

const { uniqueJSON } = await import(process.env.CATALOG_VSCODE_LIB);

test("unique JSON compares decoded names only within the same object", () => {
  for (const source of [
    '{"name":"first","name":"second"}',
    '{"nested":[{"name":"first","na\\u006de":"second"}]}',
    '{"\\u006eame":"first","name":"second"}',
    '{"outer":{"items":[{"value":1,"value":2}]}}',
  ])
    assert.throws(() => uniqueJSON(source, "owned_duplicate"), {
      code: "owned_duplicate",
    });
  const control = String.raw`{"name":"outer","items":[{"name":"one"},{"na\u006de":"two"}],"text":"braces { [ and escaped \"name\": do not introduce keys"}`;
  assert.deepEqual(uniqueJSON(control), {
    name: "outer",
    items: [{ name: "one" }, { name: "two" }],
    text: 'braces { [ and escaped "name": do not introduce keys',
  });
  for (const [source, expected] of [
    ["{}", {}],
    ["[]", []],
    ["null", null],
    ["false", false],
    ["0", 0],
    ['"scalar"', "scalar"],
  ])
    assert.deepEqual(uniqueJSON(source), expected);
});

test("unique JSON admits depth 64 and rejects the immediately deeper valid JSON value", () => {
  for (const container of ["array", "object", "mixed"]) {
    let accepted = "leaf";
    for (let depth = 0; depth < 64; depth++)
      accepted =
        container === "array" || (container === "mixed" && depth % 2 === 0)
          ? [accepted]
          : { child: accepted };
    const acceptedSource = JSON.stringify(accepted);
    assert.deepEqual(uniqueJSON(acceptedSource), accepted);
    const deeper = [accepted];
    assert.deepEqual(JSON.parse(JSON.stringify(deeper)), deeper);
    assert.throws(() => uniqueJSON(JSON.stringify(deeper), "owned_depth"), {
      code: "owned_depth",
    });
  }
});
