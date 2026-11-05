// Prepared commands keep retry identity separate from mutable form state.
import { APIError } from "./client";

/** Allocate one key and prevent overlapping sends for an explicit command.
 * The caller's send closure must retain its target, body and ETag unchanged;
 * this helper preserves the key but does not copy or freeze those inputs. */
export function prepareCommand<T>(send: (key: string) => Promise<T>) {
  const key = crypto.randomUUID();
  let running = false;
  return async () => {
    if (running) throw new Error("A command is already pending.");
    running = true;
    try {
      return await send(key);
    } finally {
      running = false;
    }
  };
}

/** A lost or retryable response does not prove the write failed. Keep the
 * prepared command so a deliberate retry cannot create a replacement write. */
export function uncertain(error: unknown) {
  if (
    error instanceof APIError &&
    error.status === 503 &&
    error.code === "WEBHOOK_TEST_BUSY"
  )
    return false;
  return (
    !(error instanceof APIError) ||
    error.status < 400 ||
    error.status >= 500 ||
    error.status === 408 ||
    error.status === 429 ||
    /IDEMPOTENCY|IN_PROGRESS/i.test(error.code)
  );
}

/** Split and deduplicate form input within the publisher domain-count limit. */
export function domainsFromInput(value: string): string[] {
  const domains = [...new Set(value.split(/[\s,]+/).filter(Boolean))];
  if (!domains.length || domains.length > 64)
    throw new Error("Enter between 1 and 64 domain names.");
  return domains;
}
