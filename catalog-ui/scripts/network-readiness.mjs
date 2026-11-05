// Browser startup waits for bounded host-network stability without retaining address diagnostics.
import { networkInterfaces } from "node:os";
import { setTimeout as delay } from "node:timers/promises";

// Container readiness can precede host-side IPv6 address assignment. Wait for
// a quiet address set before starting Chromium, not by retrying browser writes
// or disabling its network-change detection. Addresses remain in memory only.
export async function waitForStableNetwork({
  read = networkInterfaces,
  now = () => performance.now(),
  sleep = delay,
  interrupted = () => false,
} = {}) {
  const fingerprint = () =>
    JSON.stringify(
      Object.entries(read())
        .flatMap(([name, addresses]) =>
          (addresses || []).map(({ family, address, scopeid }) => [
            name,
            family,
            address,
            scopeid ?? 0,
          ]),
        )
        .sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b))),
    );
  const start = now();
  let stableSince = start;
  let previous = fingerprint();
  while (now() - start < 15000) {
    if (interrupted()) throw new Error("Network readiness interrupted");
    await sleep(100);
    if (interrupted()) throw new Error("Network readiness interrupted");
    const at = now();
    if (at - start >= 15000) break;
    const current = fingerprint();
    if (current !== previous) {
      previous = current;
      stableSince = at;
    }
    // Chromium's Linux change calculator uses a two-second address window.
    if (at - stableSince >= 2000) return;
  }
  throw new Error(
    "Host network addresses did not settle before browser startup",
  );
}
