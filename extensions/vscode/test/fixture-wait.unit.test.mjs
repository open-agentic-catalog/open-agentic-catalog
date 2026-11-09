import test from "node:test";
import assert from "node:assert/strict";
import { fixtureWait } from "../../../test/vscode/wait.mjs";

test("owned fixture waits distinguish readiness, early operation outcomes and a missing boundary", async () => {
  const never = new Promise(() => {});
  assert.equal(
    await fixtureWait(Promise.resolve("ready"), "ready", never),
    "ready",
  );
  await assert.rejects(
    fixtureWait(never, "early refusal", Promise.reject(Error("private cause"))),
    { message: "Owned fixture early refusal failed before readiness" },
  );
  await assert.rejects(
    fixtureWait(never, "early completion", Promise.resolve()),
    {
      message: "Owned fixture early completion finished before readiness",
    },
  );
  await assert.rejects(fixtureWait(never, "missing close", undefined, 20), {
    message: "Owned fixture missing close timed out",
  });
});
