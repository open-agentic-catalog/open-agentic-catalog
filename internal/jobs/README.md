# Durable Jobs

[jobs.go](jobs.go) defines Job state, scheduling and lease contracts, the in-memory store and the worker loop. [audit.go](audit.go) validates transition evidence and binds worker-attempt attribution to the initiating actor and command.

`Service.Get` and `Service.List` return public `View` metadata through dedicated store reads. Full internal `Get`/`List` reads remain available to workers, cancellation/retry and automatic source admission. Memory reads own their payload/result bytes and lease timestamp; a limited full Job page exposes only copied selected items, including its slice capacity. Publication generation and source synchronization return a compact `SnapshotReference`; their owning stores retain complete snapshots. Failed or incomplete source snapshots keep their existing result and Job-state semantics. Public results retain snapshot identity/state and source or host/generation, never the retained documents.

Handlers run outside interactive domain transactions. A worker must retain its lease when renewing or completing work; unfinished work remains recoverable after cancellation or process loss. Retry and terminal transitions preserve prior evidence. Global claim selection belongs to the worker boundary, while every follow-up operation includes the tenant.

After the handler returns, the worker stops and joins its heartbeat before the final fenced transition. Cancellation of that owned heartbeat is not a dependency failure, even when the driver returns a query-cancellation error. A genuine heartbeat failure cancels the handler; panics remain fatal. Stopping the heartbeat never bypasses the final lease, cancellation or ownership checks.

Job updates retain the later of the sampled wall time and the previous update timestamp. A heartbeat never shortens the current lease; retry times cannot precede the retained failure transition. This prevents a backward clock step from rejecting an otherwise valid update. Tenant/owner/expiry checks still use the actual sampled time, and terminal state remains immutable. Operators still need synchronized clocks; the rule does not promise arbitrary clock-skew tolerance.

Automatic retry uses a five-second window multiplied by three per attempt, saturated safely at one hour and capped before sampling by the configured `jobs.retryCeiling` (`5s..1h`). Full jitter spans zero through that window. `HandlerError.RetryAfter` is an upstream minimum through 24 hours for retryable failures; normalize invalid durations to zero and shorten the jitter window before addition so the total remains within 24 hours. Both scheduling and failure persistence use attempt completion time. Terminal errors never gain retry permission from a hint. Entropy failure uses the remaining window midpoint.

`jobs.maxAttempts` supplies the creation default (five, range `1..20`, including the initial attempt). Service options and worker/maintainer constructors receive validated plain values from composition. Live deduplication and explicit retry children preserve the original limit; process replacement leaves retained due times unchanged. Expired-lease reclaims can exceed the normal retry limit, and a transient failure on such an attempt still reaches exhaustion safely. See the [configuration policy](../../documentation/src/content/docs/reference/configuration.mdx#durable-job-retry-policy).

The explicit [formal gate](../../test/formal/README.md) proves the current ceiling helper's bounded arithmetic and loop termination. Its proof supplements, rather than replaces, the exact retry schedule and worker regressions.

Run `go test ./internal/jobs -shuffle=on -count=1`. The PostgreSQL store is maintained in [platform/database](../platform/database/README.md).

A panic in a claim loop, handler, progress callback or heartbeat is a process-fatal failure rather than an ordinary Job retry. The worker cancels and joins heartbeat work, retains the interrupted lease for expiry-based recovery and cancels sibling claim loops. A sibling panic takes precedence even when another loop returned a dependency failure first. No terminal Job evidence is manufactured during unwinding. The PostgreSQL integration suite verifies rollback and reclaim after expiry.

The completion observer receives a bounded `AttemptResult` and context for active trace correlation, never a Job or raw error. `Committed` is true only after final-state persistence succeeds; panic, shutdown and persistence failure leave it false. The platform observer emits classified diagnostics independently of tracing after updating metrics, without holding the registry lock.

## Private source trust assessments

`source.trust_verify` uses payload version 1 and the existing lease, attempt, cancellation and retention rules. Its payload binds exact entry bytes, native variant, source revision and policy generations. Only the source assessment command selects a target. A Job-shaped context does not grant worker authority.

Completion requires one validated `source.trust.completed` audit record in the same transition. PostgreSQL locks the source row and checks lease ownership and cancellation before commit. Generic Job views omit trust results. The source-specific result endpoint requires `job.read` and `source.read_evidence`, including historical results after source removal. Each private read retains `source.trust.read` audit evidence before disclosure and fails closed if that write fails. List authorization runs before pagination.

Terminal negative assessments succeed with explicit checks. Transient acquisition failures use the existing retry policy and retain no completed assessment. See [the operator procedure](../../documentation/src/content/docs/operations/federation.mdx#request-an-ans-registration-assessment).
