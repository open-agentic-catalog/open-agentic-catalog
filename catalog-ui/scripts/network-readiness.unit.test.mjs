import { test } from "node:test";
import assert from "node:assert/strict";
import { waitForStableNetwork } from "./network-readiness.mjs";

const ipv4 = { family: "IPv4", address: "127.0.0.1" };
const ipv6 = { family: "IPv6", address: "::1", scopeid: 0 };

function clock() {
  let elapsed = 0;
  return {
    now: () => elapsed,
    sleep: async (ms) => {
      elapsed += ms;
    },
  };
}

test("network readiness ignores enumeration order and optional empty interfaces", async () => {
  const time = clock();
  let reads = 0;
  await waitForStableNetwork({
    ...time,
    read: () =>
      ++reads % 2
        ? { loopback: [ipv4, ipv6], empty: undefined }
        : { loopback: [ipv6, ipv4] },
  });
  assert.equal(time.now(), 2000);
});

test("late address assignment and removal restart the quiet window", async () => {
  const time = clock();
  await waitForStableNetwork({
    ...time,
    read: () => ({
      loopback: [ipv4],
      ...(time.now() >= 1500 && time.now() < 3000 ? { bridge: [ipv6] } : {}),
    }),
  });
  assert.equal(time.now(), 5000);
});

test("continued network churn fails within a finite monotonic budget without disclosing addresses", async () => {
  const time = clock();
  await assert.rejects(
    waitForStableNetwork({
      ...time,
      read: () => ({ bridge: [{ ...ipv6, scopeid: time.now() }] }),
    }),
    { message: "Host network addresses did not settle before browser startup" },
  );
  assert.equal(time.now(), 15000);
});

test("interruption stops readiness before any further polling", async () => {
  const time = clock();
  await assert.rejects(
    waitForStableNetwork({
      ...time,
      read: () => ({ loopback: [ipv4] }),
      interrupted: () => true,
    }),
    /Network readiness interrupted/,
  );
  assert.equal(time.now(), 0);
});

test("interruption or delayed polling cannot report a quiet network as ready", async () => {
  for (const interruption of [true, false]) {
    let elapsed = 0;
    let reads = 0;
    await assert.rejects(
      waitForStableNetwork({
        now: () => elapsed,
        sleep: async () => {
          elapsed = 15000;
        },
        interrupted: () => interruption && elapsed > 0,
        read: () => {
          reads++;
          return { loopback: [ipv4] };
        },
      }),
      interruption ? /interrupted/ : /did not settle/,
    );
    assert.equal(reads, 1);
  }
});
