import test from "node:test";
import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import {
  fixturePortAllowed,
  listenFixture,
} from "../../../test/vscode/listen.mjs";

class Listener extends EventEmitter {
  binds = 0;
  closes = 0;
  constructor(ports, failure, closeError) {
    super();
    this.ports = ports;
    this.failure = failure;
    this.closeError = closeError;
  }
  listen(port, host, ready) {
    assert.equal(port, 0);
    assert.equal(host, "127.0.0.1");
    this.binds++;
    this.port = this.ports.shift() ?? 6000;
    if (this.failure === "sync") throw new Error("owned bind failure");
    queueMicrotask(() =>
      this.failure
        ? this.emit("error", new Error("owned bind failure"))
        : ready(),
    );
  }
  address() {
    return { port: this.port };
  }
  close(done) {
    this.closes++;
    queueMicrotask(() => done(this.closeError));
  }
}
test("fixture admission keeps a usable loopback listener bound and closes only rejected candidates", async () => {
  const server = new Listener([6000, 10080, 43123]);
  await listenFixture(server);
  assert.equal(server.binds, 3);
  assert.equal(server.closes, 2);
  assert.equal(server.address().port, 43123);
  assert.equal(server.listenerCount("error"), 0);
  for (const port of [0, 6000, 10080, 16383, 65536, "43123", NaN])
    assert.equal(fixturePortAllowed(port), false);
  for (const port of [16384, 43123, 65535])
    assert.equal(fixturePortAllowed(port), true);
});
test("fixture setup is bounded and stops on bind or cleanup failures", async () => {
  const server = new Listener([]);
  await assert.rejects(listenFixture(server), /startup bound/u);
  assert.equal(server.binds, 32);
  assert.equal(server.closes, 32);
  for (const failure of ["sync", "async"]) {
    const failed = new Listener([43123], failure);
    await assert.rejects(listenFixture(failed), /owned bind failure/u);
    assert.equal(failed.binds, 1);
    assert.equal(failed.listenerCount("error"), 0);
  }
  const closeError = new Error("owned cleanup failure");
  const failed = new Listener([6000, 43123], undefined, closeError);
  await assert.rejects(listenFixture(failed), (error) => error === closeError);
  assert.equal(failed.binds, 1, "Cleanup failure must prevent another bind");
  assert.equal(failed.closes, 1);
  assert.equal(failed.listenerCount("error"), 0);
});
