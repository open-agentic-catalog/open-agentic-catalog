import { setTimeout as delay } from "node:timers/promises";

const budget = 120000;
function errorCategory(error) {
  const code = error?.cause?.code ?? error?.code;
  if (
    error?.name === "TimeoutError" ||
    error?.cause?.name === "TimeoutError" ||
    code === "ETIMEDOUT"
  )
    return "timeout";
  if (error?.name === "AbortError") return "aborted";
  if (code === "ECONNREFUSED") return "connection_refused";
  if (code === "ECONNRESET") return "connection_reset";
  if (code === "ENOTFOUND" || code === "EAI_AGAIN") return "name_resolution";
  if (
    [
      "CERT_HAS_EXPIRED",
      "DEPTH_ZERO_SELF_SIGNED_CERT",
      "ERR_TLS_CERT_ALTNAME_INVALID",
      "UNABLE_TO_VERIFY_LEAF_SIGNATURE",
    ].includes(code)
  )
    return "tls";
  return "unclassified";
}

// A fixed monotonic budget covers probes and polling; late success or
// interruption cannot admit a listener. No response/error text is retained.
export async function waitForPackagedReadiness({
  probe,
  now = () => performance.now(),
  sleep = delay,
  interrupted = () => false,
}) {
  const started = now();
  let lastStatus;
  let lastError;
  while (now() - started < budget && !interrupted()) {
    const remainingBeforeProbe = budget - (now() - started);
    if (remainingBeforeProbe <= 0) break;
    try {
      const status = await probe(Math.min(1000, remainingBeforeProbe));
      lastStatus =
        Number.isInteger(status) && status >= 100 && status <= 599
          ? status
          : undefined;
      lastError = undefined;
      if (interrupted() || now() - started >= budget) break;
      if (status === 200) return;
    } catch (error) {
      lastStatus = undefined;
      lastError = errorCategory(error);
    }
    const remaining = budget - (now() - started);
    if (remaining <= 0 || interrupted()) break;
    await sleep(Math.min(200, remaining));
  }
  throw Object.assign(
    new Error("Owned packaged listener did not become ready"),
    {
      diagnostic: {
        interrupted: interrupted(),
        elapsedMs: Math.min(
          2147483647,
          Math.max(0, Math.floor(now() - started)),
        ),
        lastStatus,
        lastError,
      },
    },
  );
}
