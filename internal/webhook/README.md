# Optional signed webhooks

Webhooks deliver public Catalog notifications from the transactional outbox. Delivery is at least once and unordered; receiver acceptance cannot share a transaction with local delivery evidence.

| Files | Responsibility |
| --- | --- |
| [service.go](service.go), [types.go](types.go) | Endpoint administration, admission limits, test sends and delivery records. |
| [dispatcher.go](dispatcher.go), [retry.go](retry.go) | Lease-fenced dispatch, bounded recipient work, retry scheduling and jitter. |
| [drainer.go](drainer.go) | Acknowledgement without delivery when webhooks are disabled. |
| [store.go](store.go), [audit.go](audit.go) | Persistence ports, in-memory state and transition/read evidence. |
| [signing.go](signing.go), [crypto.go](crypto.go), [reencrypt.go](reencrypt.go) | Exact-byte HMAC signing, authenticated secret storage, rotation and operator rewrapping. |
| [notification.go](notification.go), [observability.go](observability.go) | Safe notification state and fixed transport-outcome observations. |

The sender port must use the hardened outbound boundary. Refresh endpoint state before sending and fence delivery persistence against the current outbox claim. Never expose ciphertext, signing material or raw receiver responses in diagnostics. A disabled drainer permanently acknowledges historical records; it must not overlap an enabled dispatcher.

After failed attempt `a`, cap the five-second, tripling exponential window at `retryCeiling` before applying stable delivery-ID/attempt jitter over its upper half. Then take the greater of that delay and the receiver's valid `Retry-After`, separately capped at the same ceiling, and schedule from response completion. Low ceilings preserve dispersion; a dominating receiver hint can still align deliveries. The default ceiling is one hour, and valid configured ceilings range from five seconds through one hour, including fractional durations.

`maxAttempts` includes the initial send (default five, range one through twenty). It belongs to the running dispatcher, not the retained delivery. Restarting with different limits preserves existing waiting timestamps and terminal deliveries; the next failed attempt uses the current settings. Synchronous API test sends always make one attempt. Keep delay calculation in these helpers and durable scheduling in the dispatcher/store; do not add a nested retry loop or library.

Run `go test ./internal/webhook -shuffle=on -count=1`. Operational configuration and receiver guidance are in the [webhook runbook](../../documentation/src/content/docs/operations/webhooks.mdx).

Dispatcher and disabled-drainer panic boundaries set the returned failure before deferred observations run. Attempt spans and batch counters therefore cannot report a successful dispatch merely because a panic bypassed the return assignment. The supervisor treats the fatal marker as terminal; uncertain delivery remains governed by existing lease recovery and at-least-once semantics.

Endpoint pages filter exact saved names and active/disabled status before limits. Delivery pages intersect exact delivery/event IDs and current state within one endpoint, retaining atomic page-read audit. The HTTP cursor owner binds these selections to current authority. Test-slot admission uses `WEBHOOK_TEST_BUSY` only before sending; receiver failure remains a successful operation returning delivery evidence, while post-send persistence failure can leave acceptance uncertain.
