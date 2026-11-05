---
title: "ADR-0014: Bounded synchronous work and cancellation"
---

- Status: Accepted

## Question

How should the API bound synchronous product work without confusing socket protection, PostgreSQL statement limits, graceful shutdown, and idempotent transaction cleanup or adding a general quota system?

## Context

Socket timeouts protect transport, but do not give a handler one cooperative execution deadline. A product request can include authentication, decoding, idempotency coordination, database statements, outbound preparation and response encoding. Its total work needs a shared deadline in addition to each dependency's limits.

A deadline cannot make an unbounded query or construction algorithm memory-safe. Publication, federation and safety withdrawal must bound selection before hydration. Exact overflow behavior must prevent partial candidates even when every dependency responds quickly.

Go request contexts provide cooperative cancellation: blocking code must propagate and honor the context, and CPU work must check it at useful boundaries. The incoming request context is canceled when the client connection closes, an HTTP/2 request is canceled, or the handler returns, but it has no server-owned execution deadline by default. `http.Server.Shutdown` stops new connections and waits for active handlers only until its separate shutdown context expires; it does not turn the socket write timeout into a handler deadline.

The PostgreSQL command path has one necessary exception to caller cancellation. pgx does not automatically roll back a transaction when the context passed to `Begin` or `BeginTx` is canceled. The service uses a fixed five-second cancellation-independent context for rollback or atomic commit of an already-produced exact replay response. pgx's default context watcher also interrupts a query by closing its connection; that is safe for ordinary pool work but prevents a canceled atomic command from using the still-owned advisory-lock session to retain its rolled-back failure response. Cleanup is required for crash-safe idempotency, but it must not become detached business processing or a configurable second request phase.

The decision follows the official [`net/http` request and server lifecycle contracts](https://pkg.go.dev/net/http), the Go [`context` propagation contract](https://pkg.go.dev/context), the pgx [transaction contract](https://pkg.go.dev/github.com/jackc/pgx/v5#hdr-Transactions), PostgreSQL's distinction between [per-statement and transaction timeouts](https://www.postgresql.org/docs/18/runtime-config-client.html). Kubernetes documents [Pod termination](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#pod-termination). The Google SRE discussion of [latency and deadlines](https://sre.google/sre-book/addressing-cascading-failures/#latency-and-deadlines) supports deadline propagation and selection from measured behavior. [RFC 9110](https://www.rfc-editor.org/rfc/rfc9110.html#name-503-service-unavailable) distinguishes a temporary server inability represented by 503 from an incomplete client request represented by 408 and a gateway's upstream timeout represented by 504. The atomic response rule remains consistent with the AWS Builders Library guidance on [idempotent APIs](https://aws.amazon.com/builders-library/making-retries-safe-with-idempotent-APIs): the caller token and all mutations belong in one ACID boundary and retry returns the semantically equivalent result.

## Decision

### Bound data before bounding elapsed time

Publication, safety-withdrawal, source, referral, topology, and live-selection work uses exact or deterministic `limit + 1` PostgreSQL queries. Apply explicit entry, exclusion, artifact-reference, encoded-document-byte, and aggregate-work ceilings before retaining a publication candidate. Overflow fails safely and produces no partial candidate, audit record, or outbox row. A deadline is defense in depth and never substitutes for those deterministic bounds.

### Use one cooperative product-work deadline

Use one `http.requestTimeout` duration with a safe built-in default and a reviewed finite configuration range. It is an API-role setting with an explicit environment override and ordinary JSON, Compose, Helm, diagnostics, and configuration-reference wiring. It is not configurable by route, tenant, source, actor, or request header.

The deadline covers the complete authenticated Control API and host-bound public-discovery product path, including authentication, request decoding, idempotency reservation or replay coordination, database and outbound calls, domain work, and response encoding. An earlier parent deadline or client cancellation still wins. CORS preflight, unmatched routes, API `/livez`, `/readyz`, `/version`, `/metrics`, and `/openapi/control-v1.yaml`, plus the worker operations listener, remain outside this deadline because they have no comparable product-work boundary and already have narrow local or transport limits.

Enabled [HTTP MCP](../../guides/mcp-server.mdx#connect-over-http) `POST /mcp` requests use the same `http.requestTimeout` through their own handler. On deadline expiry, that handler returns HTTP 503 with safe JSON-RPC error `-32603`.

Every blocking boundary receives the request context. CPU loops and multi-stage workflows check cancellation at deterministic boundaries. The service describes the deadline as cooperative and does not claim that it can forcibly stop arbitrary code at an exact wall-clock instant.

If the deadline expires before a response is committed, the Control API returns one stable RFC 9457 service-timeout problem and public ARD routes return the corresponding pinned-contract error with HTTP 503. OpenAPI defines the implemented codes and schemas. A response already committed is not replaced, and the handler must not continue writing after it observes cancellation. Retry remains safe through ordinary read semantics or the existing required idempotency key; the service does not infer that an unkeyed mutation is safe to replay.

### Keep atomic cleanup narrow and independently bounded

Cancellation-independent cleanup permits only these actions:

- Roll back an unfinished database transaction.
- Commit an exact response with command mutations, audit evidence, and outbox rows already produced in that transaction.
- Persist a failure response after the command transaction rolls back.
- Release the command advisory lock or close its connection.

Cleanup performs no fetch, new domain decision, candidate construction, or other business work.

PostgreSQL connections use the driver's native cancel-request watcher with no grace delay and a fixed 500-millisecond network-deadline fallback. When PostgreSQL acknowledges cancellation, the session remains usable long enough to roll back and retain the exact failure response while still owning its advisory lock. If cancellation cannot be acknowledged, the fallback closes the connection and releases its session locks. The incomplete reservation remains for reviewed repair. The service does not guess whether work committed. This driver policy is fixed, applies equally to runtime and focused operator pools, and is not a product or tenant setting.

The fixed cleanup allowance is an internal correctness bound, not part of `http.requestTimeout`, not tenant configurable, and not advertised as useful product-processing time. Implementation shares one absolute cleanup deadline across completion, rollback, advisory unlock, and fallback close rather than silently stacking fresh five-second windows. Native PostgreSQL query cancellation preserves the standard `context.Canceled` or `context.DeadlineExceeded` cause in the returned error chain even though the server reports SQLSTATE `57014`. If safe completion cannot finish within the cleanup bound, the connection is discarded where necessary and the reservation remains pending for the existing reviewed repair workflow. If atomic completion wins, the retained response remains authoritative and replayable even if the original client disconnected.

### Align transport and termination bounds

Configuration validation must keep the socket write timeout long enough for the maximum product-work deadline, fixed cleanup allowance, and a small code-defined response-write margin. The process shutdown timeout must allow an in-flight request at the start of drain to finish the same bounded path. Kubernetes termination grace must exceed process shutdown plus endpoint-removal time. SRE may set a shorter ingress timeout for deployment policy, but that does not replace the service deadline because a disconnected request still needs cooperative cancellation and safe cleanup.

### Add shared-work controls only from evidence

The existing connection-pool, worker-concurrency, fetch, federation, parser, archive, and response bounds remain in force. Focused saturation tests must first determine whether bounded requests can still exhaust memory or starve another tenant. Add only the smallest process-local expensive-work semaphore or query-local tenant-fair selection rule demonstrated necessary by those tests. Weighted scheduling, persistent quota state, distributed rate limiting, entitlement policy, and billing controls are outside this decision.

## Consequences

- Deterministic query and construction bounds protect memory and define exact overflow behavior; the request deadline limits dependency stalls and total wasted synchronous work.
- One operator setting is sufficient for different deployment latency envelopes without creating per-route or tenant policy.
- A canceled mutating request can take up to the fixed cleanup allowance beyond its product-work deadline, so socket, shutdown, ingress, and Kubernetes limits must be configured with that explicit relationship.
- Command completion, rollback, failure retention, advisory unlock and forced connection discard share one absolute cleanup deadline.
- Responsive PostgreSQL cancellations preserve the command session and avoid needless pool churn; an unresponsive cancellation still closes the connection within a fixed sub-second fallback and fails closed to repair.
- A server deadline improves safety but does not replace load testing, bounded algorithms, database lock discipline, or graceful termination evidence.

## Rejected alternatives

### Rely on socket or ingress timeouts

Rejected because socket and ingress timeouts do not supply the handler and its database calls with one service-owned cooperative work deadline. They can stop accepting or carrying traffic while application work still needs cancellation and safe transaction cleanup.

### Use PostgreSQL `statement_timeout` as the request budget

Rejected because it applies to each statement rather than the complete authentication, multi-statement, outbound, and encoding workflow. A deployment may still use database-side limits as defense in depth, but they are not the Catalog request contract and must not be set globally by this feature.

### Use `http.TimeoutHandler` or detach work

Rejected because the generic wrapper can return independently of the domain and idempotency completion boundary, buffers a separate response, and cannot decide whether a mutation, evidence, outbox row, and exact replay response should commit or roll back together. No mutating handler continues as an independent background operation after its response times out.

### Split one advertised total into a work phase and finalization reserve

Rejected because it complicates configuration and suggests that new evidence or business work may begin after the useful deadline. The existing fixed cleanup is a narrow database correctness requirement with a separate, finite upper bound; it is not reserved product capacity and does not need another operator knob.

### Add per-route timeouts, tenant quotas or distributed admission

Rejected because bounded selection, the shared deadline and measured concurrency controls meet the current requirement. More granular controls need a measured workload and a concrete resource they protect.

## Verification

- Unit tests prove route inclusion and exclusion, parent-deadline precedence, cancellation propagation, stable pre-commit timeout responses, no response replacement after commitment, and no post-timeout business work.
- Command tests prove rollback, atomic response completion, client cancellation races, one shared cleanup deadline, forced connection discard on cleanup expiry, authoritative replay after completion, and recoverable pending state otherwise.
- Configuration tests cover strict JSON and environment parsing, the finite range, write and shutdown relationships, redacted diagnostics, and every maintained profile, Compose, and Helm input.
- Focused PostgreSQL tests prove cancellation while acquiring a connection, waiting for an advisory or row lock, executing a statement, and committing or rolling back without leaving an unusable transaction or cross-tenant work. A blocked atomic statement additionally proves native server cancellation, rollback, retained failure, exact replay, and continued connection use.
- Packaged functional tests prove timeout and retry behavior through the real HTTP server, graceful drain with an in-flight read and atomic command, readiness loss during drain, exact retained failure replay after API restart, and successful new-key retry.
- Publication and federation tests independently prove their exact-limit and `limit + 1` bounds; timeout tests are not accepted as evidence for memory-bounded selection.

## Current bounds

The implemented boundary uses no quota subsystem. Publication selects no more than 1,000 eligible records and 32 MiB of encoded governed projection in deterministic database order. It computes the next generation exactly. Separate ceilings apply to entries, exclusions, artifact references, each 16 MiB document, each 36 MiB retained snapshot, and 100,000 aggregate-work units. Checks run before persistence and again before activation or rollback.

External-representation withdrawal uses one exact bounded affected-release lookup. Federation conflict, live, referral, topology, and current-observation paths use exact or `limit + 1` selection and explicit overflow.

The API defaults `http.requestTimeout` to 10 seconds and accepts only 1 through 20 seconds. It emits stable 503 timeout contracts and one label-free metric. Authenticated capabilities and diagnostics expose the effective bounds. Configuration requires write and shutdown timeouts to cover the request deadline, five-second command cleanup, and one-second response margin. PostgreSQL cancellation uses the native cancel protocol with a fixed 500-millisecond connection-close fallback, preserving standard Go cancellation causes and the responsive atomic-command session needed for exact failure retention.

One process-wide publication-construction slot bounds the peak memory use observed during concurrent construction. Focused PostgreSQL and memory tests show progress across tenants for older fixed equal-priority Jobs and due automatic sources. These tests include later arrivals and capacity turnover. No query-local tenant scheduler was required.

Unit, in-process, PostgreSQL 18, and packaged Compose scenarios cover exact bounds, overflow, cancellation, pool recovery, contention, transaction atomicity, drain, replay, and retry. Record qualification results and operator-specific limits in local reports under ignored `output/`.
