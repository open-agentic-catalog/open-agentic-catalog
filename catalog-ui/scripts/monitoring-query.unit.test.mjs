import assert from "node:assert/strict";
import { test } from "node:test";
import { hasPositiveHeapQuery } from "./monitoring-query.mjs";

const window = [1000, 2000];
const frame = (refId, values = [42]) => ({
  schema: {
    refId,
    fields: [
      { name: "Time", type: "time" },
      { name: "Value", type: "number" },
    ],
  },
  data: { values: [values.map((_, index) => 1000 + index), values] },
});
function fixture(role = "worker") {
  return {
    request: {
      from: "1000",
      to: "2000",
      queries: [
        {
          refId: "A",
          datasource: { uid: "catalog-prometheus" },
          expr: `open_agentic_catalog_go_heap_bytes{catalog_deployment=~"local",catalog_role=~"${role === "All" ? "api|worker" : role}"}`,
        },
        {
          refId: "B",
          datasource: { uid: "catalog-prometheus" },
          expr: "unrelated_metric",
        },
      ],
    },
    body: {
      results: {
        A: { status: 200, frames: [frame("A")] },
        B: { status: 200, frames: [frame("B", [123])] },
      },
    },
  };
}

test("positive heap evidence binds the matching refId, role, data source and actual observation window", () => {
  for (const role of ["api", "worker", "All"]) {
    const value = fixture(role);
    const original = globalThis.structuredClone(value);
    assert.equal(hasPositiveHeapQuery(value, role, window), true);
    assert.deepEqual(value, original, "admission must not change its input");
    value.request.from = 1000;
    value.request.to = 2000;
    const numeric = globalThis.structuredClone(value);
    assert.equal(hasPositiveHeapQuery(value, role, window), true);
    assert.deepEqual(
      value,
      numeric,
      "numeric request admission must not change input",
    );
    assert.equal(hasPositiveHeapQuery(original, role, [1000, 2001]), false);
  }
});

test("an unrelated positive query cannot replace missing, empty, failed or incorrectly identified heap results", () => {
  for (const mutate of [
    (value) => {
      value.body.results.A.frames = [];
    },
    (value) => {
      value.body.results.A.error = "owned heap failure";
    },
    (value) => {
      value.body.results.A.status = 500;
    },
    (value) => {
      delete value.body.results.A;
    },
    (value) => {
      value.body.results.A.frames[0].schema.refId = "B";
    },
    (value) => {
      delete value.body.results.A.frames[0].schema.refId;
    },
    (value) => {
      value.request.queries[0].datasource.uid = "unrelated";
    },
    (value) => {
      value.request.queries[0].expr = "unrelated_metric";
    },
    (value) => {
      value.request.queries[0].expr = value.request.queries[0].expr.replace(
        "worker",
        "api",
      );
    },
    (value) => {
      value.request.queries[0].expr = value.request.queries[0].expr.replace(
        "local",
        "other",
      );
    },
  ]) {
    const value = fixture();
    mutate(value);
    assert.equal(hasPositiveHeapQuery(value, "worker", window), false);
  }
});

test("missing, malformed and ambiguous refIds cannot admit another result's samples", () => {
  for (const refId of [
    undefined,
    null,
    "",
    " ",
    "A\n",
    "A\u0001B",
    "A\u007fB",
    1,
    false,
    {},
    [],
  ]) {
    const value = fixture();
    value.request.queries[0].refId = refId;
    assert.equal(hasPositiveHeapQuery(value, "worker", window), false);
  }
  const duplicate = fixture();
  duplicate.request.queries[1].refId = "A";
  assert.equal(hasPositiveHeapQuery(duplicate, "worker", window), false);
});

test("native request times must equal the recorded workload window", () => {
  for (const key of ["from", "to"]) {
    for (const supplied of [
      undefined,
      null,
      "",
      "not-a-time",
      "1e3",
      true,
      NaN,
      Infinity,
      0,
      -1,
      1000.5,
      Number.MAX_SAFE_INTEGER + 1,
      "999999999",
    ]) {
      const value = fixture();
      value.request[key] = supplied;
      assert.equal(hasPositiveHeapQuery(value, "worker", window), false);
    }
  }
  for (const invalid of [
    undefined,
    [],
    [2000, 1000],
    [1000, 1000],
    [true, 2000],
    [1000, Infinity],
  ]) {
    assert.equal(hasPositiveHeapQuery(fixture(), "worker", invalid), false);
  }
});

test("malformed or nonpositive numeric frames refuse rather than throw", () => {
  for (const invalid of [
    null,
    {},
    { schema: {} },
    frame("B"),
    frame("A", []),
    frame("A", [0, -2, null]),
    frame("A", [NaN, Infinity]),
    {
      schema: { refId: "A", fields: [{ type: "number" }] },
      data: { values: [[]] },
    },
    {
      schema: { refId: "A", fields: [{ type: "number" }] },
      data: { values: ["42"] },
    },
    {
      schema: { refId: "A", fields: [{ type: "number" }] },
      data: { values: [[42], [1]] },
    },
    {
      schema: { refId: "A", fields: [{ type: "time" }, { type: "number" }] },
      data: { values: [[1000, 1001], [42]] },
    },
  ]) {
    const value = fixture();
    value.body.results.A.frames = [invalid];
    assert.equal(hasPositiveHeapQuery(value, "worker", window), false);
  }
  for (const invalid of [
    null,
    {},
    { request: {} },
    { request: { queries: [] } },
    { ...fixture(), body: { results: [] } },
  ]) {
    assert.equal(hasPositiveHeapQuery(invalid, "worker", window), false);
  }
  assert.equal(hasPositiveHeapQuery(fixture(), "unselected", window), false);
});
