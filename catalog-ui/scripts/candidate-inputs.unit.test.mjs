import { test } from "node:test";
import assert from "node:assert/strict";
import { candidateArguments } from "./candidate-inputs.mjs";

test("Retained candidates are explicit, paired and never instrumented fixtures", () => {
  const ordinary = {
    coverage: false,
    mcp: false,
    publisherDNSOnly: false,
    localPasswords: false,
  };
  assert.equal(candidateArguments({}, ordinary), null);
  assert.throws(() =>
    candidateArguments({ CATALOG_CANDIDATE_BACKEND: "backend.json" }, ordinary),
  );
  const environment = {
    CATALOG_CANDIDATE_BACKEND: "backend.json",
    CATALOG_CANDIDATE_UI: "ui.json",
  };
  assert.deepEqual(candidateArguments(environment, ordinary), [
    "-B",
    "test/ci/candidate.py",
    "admit",
    "--backend",
    "backend.json",
    "--ui",
    "ui.json",
  ]);
  for (const key of Object.keys(ordinary))
    assert.throws(() =>
      candidateArguments(environment, { ...ordinary, [key]: true }),
    );
});
