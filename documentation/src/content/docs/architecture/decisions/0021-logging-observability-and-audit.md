---
title: "ADR-0021: Logging, observability and audit boundaries"
---

- Status: Accepted
- Related decisions: ADR-0012, ADR-0013, ADR-0015, ADR-0018

## Question

How should the backend, operator CLI and optional UI produce consistent, configurable and safe diagnostics while keeping metrics, sampled traces, durable audit and notifications distinct?

## Context and implementation boundary

The API, workers, operator commands and optional UI need useful diagnostics without exposing database connection strings, credentials, private content or panic values. Raw dependency errors and standard-library fallback output can bypass otherwise safe HTTP responses. Diagnostic severity must describe the final response or persisted outcome, while optional tracing and collector outages must not hide required operational signals or change business behavior.

This decision defines the implemented backend and UI policy: standard Go `slog`, strict startup configuration, safe projections, owned panic containment, bounded warnings and explicit event ownership. Required audit, exact command replay and notification classification remain independent of logging settings. The [configuration reference](../../reference/configuration.mdx#logging-configuration) and [observability runbook](../../operations/observability.mdx#logs-and-trace-correlation) own operator procedures and current limits. This decision supplements the existing audit and event-delivery decisions without changing their schemas or retention.

## Decision

### Five signals with separate purposes

| Signal | Question it answers | Authority and completeness |
| --- | --- | --- |
| Operational logs | What failed or changed in this process, and what should an operator inspect? | Selected, level-filtered, potentially suppressed or lost diagnostics; private operational data, never complete history. |
| Metrics | How often does an outcome occur, how long does work take, and is a component degraded? | Fixed aggregate labels and documented availability semantics; independent of log level and trace sampling. |
| Traces | Where did a sampled operation spend time, and which operations caused later work? | Optional bounded export, local sampling and explicit cross-process links; missing spans do not prove missing work. |
| Audit | Which reviewed action committed or which sensitive evidence was prepared, under whose authority? | Versioned tenant evidence bound to its owning transaction and authorization; required append failure prevents the protected result. |
| Outbox notifications | Which independently classified committed change needs post-commit delivery? | Durable PostgreSQL notification reference, optional at-least-once delivery; audit-only actions create no notification. |

Do not emit every audit row as a log, put every log into a span event, or treat every exception as a public notification. Changing verbosity, format, sampling or log collection must not alter evidence, retries, command success, notification classification or readiness.

### Formats and destinations

Use Go's standard `log/slog`. Every service role, including the UI host, defaults to structured text through `TextHandler`. Use quoted key/value text.

Do not use free-form multiline output. Offer newline-delimited JSON through `JSONHandler` when explicitly configured for a log collector. Text defaults favor direct human reading; this is not a claim that JSON has a measured performance problem.

Both formats carry the same safe fields and severity. Select one format at startup. Do not use terminal detection, color, simultaneous JSON/text output or a new logger dependency.

JSON encoding and quoting prevent structural ambiguity; neither makes a secret safe to record. [Go's logging API](https://pkg.go.dev/log/slog) supplies both handlers and minimum-level filtering.

| Producer | Destination and representation |
| --- | --- |
| `catalogd api`, `worker`, `all` and UI service diagnostics | All normal levels to stdout in the selected format. Keep severity in the record rather than splitting levels between streams. |
| Terminal service startup/shutdown failure | One safe ERROR on stderr, using the effective format after successful configuration; fixed structured-text bootstrap output before configuration is trusted. A failure already reported by its owning boundary must not be repeated with its cause. Exit remains nonzero. |
| `catalogctl`, help/version/probe result paths | Preserve existing stdout data contracts. Terminal command errors remain concise safe text on stderr, with fixed stage/code and reviewed usage guidance. Any runtime diagnostic emitted by an operator command uses stderr, never the result stream. Remote reads need no runtime config merely to print an error. |
| Browser | Existing bounded user feedback. No new client log collector, remote error endpoint or browser tracing is selected. |

No application `logFile`, file rotation, syslog client, network log exporter or sink selector is added. Container runtime, system service manager or operator-owned process supervision captures stdout/stderr, applies access control, rotation, retention, encryption and forwarding. File retention outside containers is a supervisor responsibility; shell redirection alone is not rotation or a permission policy. This fits the existing non-root scratch images and avoids new writable mounts or log-file privilege handling. Kubernetes manages container log rotation through the kubelet; application-managed files have a different lifecycle. See [Kubernetes logging](https://kubernetes.io/docs/concepts/cluster-administration/logging/).

Standard handlers write synchronously. A slow stdout consumer can delay the caller. `slog.Logger` does not return handler write failures to the application.

Logs therefore have no delivery or durability guarantee. Do not add an unbounded queue, retry log writes, recursively log sink errors or roll back a completed transaction because a diagnostic write failed. Preserve normal OS broken-pipe behavior.

Operators choose their collector's blocking/loss tradeoff and monitor storage/collector health; Docker's [delivery-mode documentation](https://docs.docker.com/engine/logging/configure/#configure-the-delivery-mode-of-log-messages-from-container-to-log-driver) explicitly describes blocking and bounded-buffer loss. This design does not promise that logging can never affect latency. A measured need for application buffering requires a separate bounded design.

### Startup configuration

| Setting | Values and default | Delivery |
| --- | --- | --- |
| Backend JSON `logLevel` | `debug`, `info`, `warn`, `error`; default `info` | Explicit `CATALOG_LOG_LEVEL` override. |
| Backend JSON `logFormat` | `text`, `json`; default `text` | Explicit `CATALOG_LOG_FORMAT` override. |
| UI JSON `logLevel`, `logFormat` | Same values and defaults | Independent strict UI configuration, rendered by its launchers and Helm; no backend config loading or generic environment reflection. |

These startup-only fields are implemented. Defaults apply only when omitted. Direct execution, Compose, Helm and UI launchers use `info`/`text` for omitted settings; collector-oriented examples select `json` explicitly without environment-dependent defaults.

Reject explicit empty, null, non-string, mixed-case or unknown values. Reject a present-but-empty backend override. Report the setting name and allowed vocabulary without echoing its value.

Backend precedence is defaults, selected JSON baseline, then explicit environment overrides. No CLI flags, per-package rules, arbitrary levels, `off`, hot reload or runtime settings endpoint are needed. Logging level is independent of profile, tracing enablement and sampling.

Common log-setting validation also applies to migration/operator configurations without adding unrelated runtime dependencies. Initialize UI defaults before strict decoding so omission and explicit empty values stay distinct.

Resolve and validate configuration once before constructing the effective service logger. Use a safe bootstrap reporter for parsing failures. Pass the resolved configuration and logger to runtime composition.

Keep logger injection explicit for tests and embedding. A caller-supplied logger controls its threshold and must still obey privacy rules. Do not mutate a process-global default to configure unrelated packages. A small shared platform construction/projection helper can serve actual backend/UI callers; domain services continue to use their existing observer and tracing ports.

The CLI diagnostics and bootstrap commands select stderr for diagnostics and preserve stdout for results. Remote/help/version/probe paths need no runtime configuration.

Include the selected level and format in the existing safe configuration report and startup INFO event when that level is enabled. Configuration inspection reports its own resolved inputs, not another running process's logger; `catalogctl diagnostics` constructs a separate runtime. Caller-injected handlers retain their own behavior, which cannot be inferred from the selected settings. An ERROR threshold need not emit an INFO startup record merely to report itself. The UI uses its independent configuration and startup event without a new diagnostic listener.

Settings ship with strict parser/default/override tests, safe diagnostics, sample profiles, Make exports, environment examples, Compose generation and Helm values/schema/rendering. Keep API/worker settings aligned through the existing runtime config model and UI settings in `ui.config`. A shared diagnostic helper must be included in the UI's deliberately narrow image build without pulling in backend runtime/configuration/database dependencies. Deliver UI build identity through the existing version/commit build inputs, with explicit development defaults rather than inferring binary identity from an image label. The configuration reference documents effective settings, restart requirements and examples. Existing verification targets suffice.

### Record vocabulary, severity and event ownership

Use standard `time`, `level`, `msg` plus a fixed `event`, `service`, `role`, `component`, validated `environment` and bounded build `version` across backend and UI-host diagnostics. Use UTC timestamps and numeric duration fields with explicit units. Project reviewed deployment/build values; do not serialize configuration or arbitrary identifiers.

Document the process-owned fields and collector-supplied deployment/instance/pod metadata without requiring Kubernetes or enabling tracing just to identify a process. Messages and event names are code-owned, with dotted event names such as `process.logging_configured`, `dependency.unavailable`, `http.request_failed`, `job.attempt_completed` and `ui.session_established`. Maintain the implemented vocabulary alongside its owning call sites and tests, not a generic registry or event bus.

No source-file paths or automatic caller stacks are added.

| Level | Intended use | Examples and exclusions |
| --- | --- | --- |
| DEBUG | Safe detail useful during diagnosis | Routine validation/authentication/authorization rejection, selected successful/waiting attempt detail. No private values even here. |
| INFO | Lifecycle or verified state transition | Process start, drain, stop and observed recovery. Entering a callback or scheduling a retry does not prove recovery. |
| WARN | Degradation or terminal business work needing inspection | Retriable required dependency outage, partial optional service, terminal non-internal Job failure, aggregated security rejection signal. No warning per ordinary bad request. |
| ERROR | Unexpected service failure or loss of a required guarantee | Panic, persistence/audit append failure, encoding/command-completion failure, invariant violation or terminal process failure. Expected cancellation is not an internal error. |

Logging has no fatal/panic level and never controls retries or exits. Use the effective HTTP problem/status and known failure class, including deadline translation, rather than raw error wording or a rule that all 5xx are identical. A safely returned 503 for dependency unavailability normally warrants WARN; unexpected internal failure warrants ERROR; normal client rejection is DEBUG. Public problem detail remains separate from the operational projection.

One boundary owns each diagnostic. The common request-failure path must not duplicate middleware, encoding or completion records. A persisted retry remains visible in retained state and existing retry metrics, without a default warning for each receiver response.

The Job observer owns selected attempt diagnostics even when tracing is off. The trace provider must not add a duplicate. The observer carries only the kind, outcome, safe failure classification, permitted correlation fields and whether the result committed.

A failed or panicking attempt is not proof that a terminal Job state committed. Unexpected internal failure still warrants ERROR when a retry was safely scheduled; ordinary dependency waiting does not warrant a warning for every attempt. This is not an exactly-once log-delivery promise.

The provider emits no Job or webhook-dispatch completion log. Webhook processing failures propagate through the existing dispatcher/supervisor boundary, with no duplicate for a propagated failure. A webhook processor can persist its result successfully even when the receiver rejects delivery.

Sender metrics and retained delivery evidence describe that rejection. No default record per send is required. Keep diagnostic writes outside metric-registry locks and preserve independent counts, active gauges and retry observations.

Preserve aggregate counts for all outcomes. A concurrency-safe helper bounds repeated readiness, dependency and authentication/authorization failure warnings. It uses fixed event/component keys.

Emit the first warning immediately. On subsequent occurrences, emit at most one summary per minute with the suppressed count. This is a fixed safety policy, not another deployment knob or a map keyed by tenant, URL, token, IP or request ID.

Individual security rejections may be DEBUG. A bounded WARN summary makes a sustained problem visible at the default INFO threshold. Enabling DEBUG can increase volume but cannot bypass privacy.

Do not suppress the first unexpected error, process panic or required-audit failure. Suppressed counts appear at the next admitted warning; shutdown does not flush an idle summary. No timer goroutine or durable summary queue is required.

The existing HTTP family groups status into classes such as `4xx`; it cannot distinguish authentication/authorization rejection. The API provides one counter for classified security rejection, with only `unauthenticated` and `forbidden` reasons, and a documented operator query. Count each final classified refusal once across Control and protected discovery paths, including direct middleware problems, without duplicating generic route/status metrics.

This counts the service's `unauthenticated` and `forbidden` codes, not every 401/403 status, an inferred attack or authenticated identity. Outbound `source_policy_denied` and upstream-provider refusals are separate classifications. The UI uses its bounded host diagnostics without adding a metrics listener.

No identity/IP risk scoring, per-user detection state or new security-event database is selected.

### Safe fields and error projection

| Field class | Policy |
| --- | --- |
| Outcome/code/type, operation and component | Fixed bounded vocabularies; classify unknown errors with a safe fallback. Preserve `errors.Is`/`errors.As` internally. Never serialize an arbitrary error, recovered value, `Stringer` or request object. |
| Request/trace correlation | Validated bounded request ID and active trace/span identifiers only; these identify an interaction, never establish identity or authority. |
| HTTP and retry detail | Allowlisted method, registered route or `unmatched`, numeric status, bounded attempt/count and explicit units such as `retry_delay_ms`. No raw URL, path, query or upstream body. |
| Repair correlation | Existing narrowly reviewed tenant/reservation identifiers and idempotency-key digest may remain in private repair diagnostics. Do not generalize them to every log, metric, span or audit record. |
| Sensitive content | No DSN, bearer token, cookie, password, OAuth code/state/verifier, key, claim set, credential binding, SQL, private document, feedback text, full query, audit reason, external response or raw panic text. Hashing sensitive input does not automatically make it acceptable. |

Apply privacy before serialization through small explicit projections at the boundaries that know the operation. Keep output schemas closed in tests: bound the existing request IDs and route templates, add no unbounded strings/arrays, and test invalid/control-character input in both formats. `ReplaceAttr` can normalize units or remove a forbidden key, but cannot infer every secret hidden in an arbitrary string. Never use a denylist or password regex as the primary safety boundary. The [OWASP logging guidance](https://cheatsheetseries.owasp.org/cheatsheets/Logging_Cheat_Sheet.html) supports explicit exclusions, injection resistance, access controls and verification of logging failures; Catalog deliberately excludes some diagnostic details that the general guide permits.

### Panics, standard-library diagnostics and terminal errors

Configure explicit safe `http.Server.ErrorLog` adapters for product API, worker operations and UI listeners. Consume raw standard-library messages only inside that adapter. Emit a fixed bounded event without their text.

`slog.NewLogLogger` alone does not sanitize them. Handle recovered request panics at the application boundary with safe request correlation, retain the API's abort/rollback behavior, and avoid a second standard-library panic report. Preserve `http.ErrAbortHandler`, already committed responses, connection abort and cancellation semantics.

Do not return a successful response or replay record after a panic.

Go's HTTP/1 client can also log unsolicited upstream bytes through the process-wide standard logger. Each command entry point installs a safety adapter before dispatch: discard that unstructured text and emit only a fixed, bounded `dependency.diagnostic` WARN. Bootstrap uses structured text on stderr; startup binds the configured process logger, including level/format and service-versus-operator destination. This explicit process-owned redaction boundary is the exception to global standard-logger ownership; library logger construction does not install it, and `slog.Default` remains untouched. Do not identify a dependency, mark health degraded or infer severity from discarded text. Direct dependency writes to stderr, initialization and runtime crash output remain outside this adapter.

Inventory command entry points and every Catalog-owned goroutine, including supervisor callbacks, concurrent Job loops and their heartbeat goroutine, and the UI listener. Recovery must occur in the goroutine that panics. Convert an owned process/worker panic to a safe non-retryable failure.

Cancel siblings, drain within the existing bound and exit nonzero. Do not resume potentially corrupt work or convert it into a normal completed/dead-lettered Job. Ensure heartbeat cancellation/join and transaction rollback still occur.

Deferred Job/webhook observations and span completion must not report a panic as success or invent a committed outcome. The existing narrow JSON-LD library containment has its own contract and should not be replaced indiscriminately.

A bounded event may identify component, operation and panic type, but never the panic value, stack arguments or raw runtime dump. Test caught faults at real output boundaries with synthetic sentinels. `GOTRACEBACK=none` removes stacks, not panic values, and a recovery in `main` does not cover child goroutines.

Unrecoverable Go runtime faults, out-of-memory termination, package initialization, signals and dependency-owned goroutines can bypass application handling; do not claim this policy sanitizes every possible runtime crash. No blanket stderr discard or automatic crash-dump upload is selected. Crash output remains restricted operator evidence.

See [Go runtime diagnostics](https://pkg.go.dev/runtime#hdr-Environment_Variables).

Terminal error projection must cover implicit flag/parser output as well as `main`'s returned error. Preserve actionable reviewed usage/configuration messages and safe stage/code/type for database, filesystem, TLS and other dependency failures; do not format their complete chains. Review all three commands and preserve success/help/stdout contracts and exit behavior. Privacy fixes precede optional convenience controls.

### Traces and correlation

Keep the existing optional process-owned OpenTelemetry trace provider, bounded OTLP/HTTP exporter, domain tracing ports and private attempt causality. No OpenTelemetry logs SDK, span per function, automatic exception recorder or log-to-span bridge is added. A collector can map structured stdout into its log model separately; fields are intentionally compatible concepts, not a claim that raw `slog` JSON is OTLP. The [OpenTelemetry log data model](https://opentelemetry.io/docs/specs/otel/logs/data-model/) distinguishes severity, resource identity and trace/span context.

Use the same restricted admission rules for trace parenting and fallback log correlation. Accept one version-00 header. Ignore duplicate, malformed or zero context. Preserve the current normalization of wire flags to the sampled bit before applying the retained causal-context validator; its normalized storage format is not a stricter wire-admission rule. Prefer active local context.

Set `trace_sampled` only when a local span context exists. An admitted remote ID provides correlation; it does not prove local sampling or storage. Even a sampled span can fail export.

Keep new roots and links for asynchronous attempts and never propagate context to resource-controlled federation/artifact/webhook destinations.

Set span error status for an exhausted `dead_letter` Job while preserving its distinct outcome. Review unsupported-handler classification against the committed result; do not mark handled waiting, cancellation or successful processor persistence as an unexpected error. Emit error codes and outcomes without raw exception events. Trace-export failure warnings stay bounded and raw SDK diagnostics stay suppressed; collector availability never becomes readiness.

The UI host admits one ASCII `X-Request-ID` of 1-128 letters, digits, dots, underscores or hyphens, otherwise generates a local ID. It records only fixed route families, status/code and a separately validated backend response ID. Only an admitted upstream ID replaces the local response header. The host forwards neither browser request IDs nor trace context to the API or issuer, and creates no tracing provider. Correlation never supplies authority or a destination.

UI lifecycle events reuse the configured process logger: one logging-configuration event, one drain event and a clean-stop event only after successful listener shutdown. API/issuer transport, 429/5xx, refused redirects, non-200 issuer discovery/key reads and invalid required API/discovery responses produce bounded WARN observations. Expected authentication/state refusals and successful session establishment are DEBUG; unexpected host failures are ERROR.

Cancellation does not mark a dependency unavailable. A recovery event means the observed dependency operation responded successfully; it is not a readiness probe or proof that all API/issuer operations work. Existing browser error statuses are preserved, so an issuer outage discovered during token verification can have a 401 response and a WARN operational event.

No token, cookie, OAuth state/code/verifier, claim set, raw URL or response body enters these events.

### Operational measurements and provider evidence

Metering here means aggregate operational usage, not tenant accounting or billing. Reuse existing HTTP response/byte/latency, workload, persisted retry and export metrics. Document each measurement's observation point, unit, process-local or database-global scope, reset/absence behavior and aggregation.

Keep labels bounded and independent of log level and sampling. Process counters can lose observations or reset. Do not sum repeated database-global snapshots across workers.

Do not infer unique users, exact histories, receiver delivery or backend retention from these signals. No new quota enforcement, token-cost estimation or accounting ledger is selected.

Embedding operation counts currently include local validation and pre-dispatch cancellation, and measured duration includes admission waiting. HELP text, dashboard labels and runbooks describe those observation points. The measurement does not count provider requests or provider-only execution time. The current adapter ignores provider token-usage fields. Usage unavailable at this boundary remains unavailable, not zero or an estimate derived from bytes. No additional request counter or token parser is needed for this scope.

Local rejection, cancellation or a deadline before dispatch must preserve the last provider observation. A timeout while waiting for an owned concurrency slot describes local admission, not an observed provider outage. Use its fixed operational outcome.

Preserve returned error identity, lexical fallback, retry policy and concurrency limits. Keep contacted-provider failures and subsequent successful recovery distinct, with no raw queries, responses or vectors in diagnostics. The metrics registry uses the same closed vocabulary for admission and rendering.

The maintained dashboard includes classified security-refusal rates and existing trace-export failure counters. Queries preserve deployment/role/instance scope and scrape availability. Zero failures with no submissions does not establish healthy collection; exact SDK queue loss remains unmeasured. Test actual shipped PromQL against counter resets, zero activity, absent/stale targets and isolation. Alert thresholds, collection, retention and production response procedures remain deployment choices.

### Service activity measurement contract

The Service activity view uses the following dictionary. Request classification, counters, the memory snapshot reader, the default-off API and the browser view are implemented. The selected audience requires an explicit deployment gate and current administration permission. The existing Service information permission rule does not authorize this disclosure.

#### Audience and privacy

The deployment setting defaults off. Enabling it permits current active members with tenant-wide `access.manage` to read aggregates from the responding process. When OAuth scope enforcement is enabled, GET and implicit HEAD also require `tenant.admin`. Ordinary readers and grantless service clients cannot enter. `http.serviceActivityEnabled` implements the startup gate. The API enforces admission. The browser uses current permission and capability hints, with manual refresh by default.

Use one deployment gate and existing permission checks. Do not add another role, audience mode or tenant-editable disclosure setting. The deployment owner explicitly permits cross-tenant disclosure by enabling the gate; tenant administration alone does not enable it.

The sample combines traffic from all tenants served by the responding execution. Even without identifiers, repeated samples reveal traffic volume, timings, refusals and periods of activity by other tenants. Tenant Administrator is a tenant role, not installation-wide authority. Existing tenant summaries retain their existing permissions. Authentication and current membership resolution can require dependency I/O; the sample itself must not fetch dependency state.

#### HTTP populations

Each completed measured request contributes once to a fixed HTTP family. Classification uses the listener boundary, the trusted registered route pattern and the accepted request method. Do not classify a raw request path as a registered route. A known route's refusal, failure or replay remains in that route's family. A nonexistent resource on a matched route is different from an unmatched URL.

| Family | Registered API patterns or rule |
| --- | --- |
| `control_search` | `POST /api/v1/catalog/search`. |
| `control_explore` | `POST /api/v1/catalog/explore`. |
| `observation_search` | `POST /api/v1/observations:search`. |
| `administrative_live_search` | `POST /api/v1/federation/sources:live-search`. |
| `ard_search` | `POST /ard/v1/search`. |
| `ard_explore` | `POST /ard/v1/explore`. |
| `ard_list` | `GET /ard/v1/agents`, including implicit HEAD. |
| `discovery_documents` | GET and implicit HEAD for `/.well-known/ard.json`, `/.well-known/ai-catalog.json` and `/public/v1/artifacts/{artifactId}`. |
| `http_mcp` | POST and OPTIONS for `/mcp`; GET, HEAD and OPTIONS for `/.well-known/oauth-protected-resource/mcp`. Requires the corresponding registered MCP route. |
| `control_read` | Remaining registered Control GETs, implicit HEADs and the four read-only POST exceptions below. |
| `control_command` | Remaining registered Control POST, PATCH and DELETE requests. This describes HTTP commands, not committed mutations. |
| `platform` | GET and implicit HEAD for `/livez`, `/readyz`, `/metrics`, `/version`, `/openapi/control-v1.yaml`, `/api/v1/service-information` and `/api/v1/capabilities`. |
| `observer` | GET and implicit HEAD for `/api/v1/service-activity`. |
| `unknown_auxiliary` | Unmatched routes, unsupported methods, other auxiliary requests and every request on the worker operations listener. |

Apply the explicit rows before the remaining-Control rules. The first eleven families form Catalog work. The last three families remain separate from Catalog totals, active work and Catalog duration. Global Control preflights have no matched method pattern and remain auxiliary. MCP preflights have registered method-independent handlers and use their explicit accepted-method rule. Unsupported MCP methods remain auxiliary even when the MCP handler records a protocol refusal.

The four read-only POST exceptions are `/api/v1/access/capabilities`, `/api/v1/artwork/read`, `/api/v1/ratings/read` and `/api/v1/personal/items/{itemId}/resolve`. These operations return decisions or content without committing the requested content change. They keep their existing authentication, authorization and sensitive-read audit rules.

Source inspection, change-preview creation, validation, audit export and synchronization remain commands. A source-change preview saves a preview and audit evidence. Returning evidence or a preview does not make its creation read-only.

Use the fixed method vocabulary `GET`, `HEAD`, `POST`, `PATCH`, `DELETE`, `OPTIONS`, `OTHER`. Use status classes `1xx`, `2xx`, `3xx`, `4xx`, `5xx`. HEAD retains its actual method while using its registered GET family's classification. Preserve existing status normalization and metrics. A request rejected before the Go handler is outside measurement.

The raw HTTP completion total covers the shared registry. In development role `all`, this includes the worker operations listener. Listener provenance must keep even identically named health or metrics routes auxiliary on that listener.

The API middleware supplies the Catalog classification before active admission. Existing route/status counters merge identical API and worker patterns. Record fixed family counts at the same completion boundary; do not reconstruct listener provenance from those merged counters. Preserve existing Prometheus names and meanings.

Worker operations retain their separate middleware; this does not add a worker panic guarantee.

#### Exact sample fields

The implemented endpoint uses the following closed shape. OpenAPI, encoding and memory snapshots implement this API contract. The browser validates this projection before displaying statistics. No selector can choose another process, tenant, source or historical interval.

| Field | Meaning and type |
| --- | --- |
| `schema` | Fixed string `service-activity-v1`. |
| `population` | Fixed string `catalog-http-v1`, identifying the HTTP dictionary above. |
| `scope` | Fixed string `responding_api_process`. |
| `source` | Fixed string `process_memory`. |
| `instanceId` | The responding execution's existing UUID, not a fleet or node identifier. |
| `role` | `api` or combined development role `all`. |
| `startedAt`, `observedAt` | UTC timestamps from the existing process identity. Wall-clock corrections can reverse their order. |
| `elapsedSeconds` | Finite nonnegative JSON number from the process's monotonic clock, retaining subsecond precision. |
| `http.completed` | Decimal-string uint64 count of all completed measured HTTP requests in the shared registry. |
| `http.activeCatalog` | Decimal-string uint64 gauge of unfinished requests in the first eleven families. |
| `http.families` | Exactly fourteen rows, in the table's order, including zero rows. |
| `http.families[].family` | One fixed family name. No route, URL or identifier. |
| `http.families[].completed` | Decimal-string uint64 completion count for this family. |
| `http.families[].durationSeconds` | Finite nonnegative JSON number: summed handler duration for those same completions. |
| `http.families[].methods` | Exactly seven rows of `{method, completed}`, in the fixed method order above. |
| `http.families[].statusClasses` | Exactly five rows of `{statusClass, completed}`, in ascending class order. |
| `controlSearch.outcomes` | Exactly four `{outcome, completed}` rows: `succeeded`, `partial`, `rejected`, `failed`. |
| `mcp.enabled` | Boolean from this process's startup configuration. Disabled does not mean healthy or available. |
| `mcp.outcomes` | Exactly 24 `{operation, outcome, completed}` rows, using the existing MCP vocabularies below. |

Every `completed` value uses canonical decimal strings from `0` through `18446744073709551615`. Preserve exact values and validated subtraction beyond JavaScript's safe-integer range. The API must reject an invalid required sample rather than silently truncate or report zero. Keep the serialized response within 64 KiB. Fourteen family rows contain separate method and status counts, not a method/status cross-product.

The sum of family completions equals `http.completed` in one coherent sample. Each family's method and status sums equal its completion count. The current observer request is unfinished when sampled and completes afterward. Catalog completions and duration are sums of the first eleven families; no duplicate stored Catalog total is needed. A weighted mean divides their summed duration by their matching completion count. Empty denominators are unavailable, not a measured zero latency or error rate.

Copy selected registry values under its short lock. Do not invoke `Snapshot`, `Render`, database/provider callbacks, readiness checks or worker/collector queries. Process identity and precise elapsed time use the existing process object. Use monotonic elapsed-time differences for browser rates within one execution. Reset deltas on execution/schema/population changes or decreasing counters. Browser receipt age uses its own monotonic clock, not UTC subtraction.

#### Overlapping measurements and limits

`controlSearch.outcomes` describes only the existing Control Catalog Search operation. An HTTP 200 can have a partial Search result. Observation Search, administrative live search, ARD Search and MCP are separate named surfaces; do not present their sum as a universal query count.

MCP operation order is `protocol`, `discover`, `list`, `call`. Outcome order is `completed`, `rejected`, `failed`, `tool_error`, `busy`, `cancelled`. These existing counters include protected-resource metadata and OPTIONS requests entering the MCP handler. An HTTP 200 can carry a tool error. MCP outcomes overlap HTTP counts and do not form another disjoint request population. Concurrent HTTP and MCP completion points do not promise cross-layer atomic equality.

Stdio clients perform ordinary Control reads against the backend. The Catalog cannot count their protocol sessions or downstream tool executions. SQL statements, embedding operations, workflow attempts and Jobs are different units. A replay is another HTTP response, not another logical mutation. Ordinary UI permission checks and tenant-summary reads remain Control traffic; only the explicitly excluded families leave Catalog totals.

One execution owns this sample until it stops. Process restart resets its counters. Do not combine replicas or infer connections, unique clients, active users, fleet size, tenant usage or lifetime totals. Do not add identity labels, an audit/request ledger, a node registry, a history store or a required Prometheus collector. Existing membership counts remain memberships; cataloged MCP Servers remain content.

### Audit, security events and retention

Preserve [ADR-0013](0013-audit-evidence-and-verification.md) and the [implemented audit operation matrix](../../operations/audit.md#core-operation-classification). Successful classified transitions, focused operator changes and named sensitive reads retain validated versioned evidence; state, evidence, applicable notification and command response remain atomic where implemented. Audit append failure withholds protected success or bytes and gets an ERROR diagnostic without the private evidence. External source calls still have the documented post-call evidence limitation. Logging changes add no table, migration, hash chain or audit exporter.

Authentication/authorization denials, malformed requests, ordinary reads, health checks and worker heartbeats do not become tenant audit rows. They have bounded operational counts and selected safe diagnostics. Identity-provider sign-in history stays with that provider; an unverified claim cannot supply a trusted tenant/actor for audit. Do not start logging subjects, emails, addresses or session/token digests merely to correlate a denial. Existing audit request/command/Job linkage is sufficient; do not embed trace trees or copy every retained audit reason into logs.

Audit reasons are deliberately bounded user-authored text, not an input that a heuristic can guarantee is secret-free. Existing client warnings, authorized access and export controls remain necessary. Routine housekeeping must continue to preserve audit history; there is no new audit TTL. Retention/legal-hold policy, backups, protected export storage and operator log retention have different authorities. Export digests prove structural consistency, and restore comparison needs its separately trusted recovery-point binding; this is not cryptographic non-repudiation or proof against a privileged database administrator.

## Alternatives and consequences

| Alternative | Disposition |
| --- | --- |
| JSON by default or JSON only | Useful for log collectors, but less convenient for direct human reading. Keep JSON explicitly selectable and show collector-oriented configuration examples. |
| Structured text by default, explicit JSON | Selected: readable defaults with the same fields, severity and privacy guarantees in either format. Operators choose JSON for collectors. |
| Terminal auto-detection or implicit production-format switching | Makes omitted settings depend on launch context; reject. One explicit startup setting selects the format. |
| File/network sinks, rotation library, new logging framework | Duplicate deployment responsibilities and introduce storage/queue/credential failures; reject for this scope. |
| Dynamic levels, per-package thresholds, configurable suppression policies | No demonstrated operational need; startup settings and one fixed repeat-warning policy suffice. |
| Log every request/retry or audit every denial | Duplicates aggregates, creates volume/privacy risk and changes audit authority; reject. |
| Global panic recovery or `GOTRACEBACK=none` | Does not cover child goroutines or remove raw panic values; use explicit owned boundaries with honest runtime limits. |
| Rewrite errors or regex-mask dependency strings | Loses internal classification or misses secrets; project safe information at output. |

Operators gain selectable verbosity/format and stable event queries. Developers must classify severity, output fields and ownership when adding a boundary, while keeping the CLI's user interface distinct from service logs. Text and JSON require equivalent tests. Fixed summaries can hide individual repetitions but retain aggregate evidence. There is no end-to-end log delivery guarantee; audit remains the durable evidence mechanism. This decision does not add a collector, dashboard product or platform dependency.

## Implementation and acceptance

Backend and UI configuration, safe terminal/HTTP diagnostics, owned panic handling, event classification and deployment delivery implement the logging and diagnostic decisions. The default-off Service activity endpoint and its browser view are implemented. API/worker Helm settings are shared; UI settings are independent. Configuration checksums roll only the affected workloads. The [chart logging guide](../../../../../../deployments/helm/open-agentic-catalog/README.md#logging) defines values and rollout behavior. Application logs need no monitoring chart or trace collector.

Acceptance requires meaningful positive controls alongside no-leak assertions: malformed DSNs and nested errors must still fail for the intended reason; panic probes must still abort/drain; ordinary valid commands, requests and callbacks must succeed. Test both formats and all levels, explicit invalid settings, pre-config fallback, injected loggers, failure/suppression recovery, concurrent writes, failed writers without recursive logging, trace-off/sampled/unsampled combinations, CLI stdout and fixed UI headers. Retain complete command/replay/audit/outbox assertions instead of merely checking that output contains no sentinel.

Use isolated unit tests for decisions and projections, including embedding admission/state and safe effective settings; owned HTTP/issuer/exporter and subprocess tests for actual output, panic and cancellation boundaries; existing PostgreSQL tests for preserved required-audit failure, atomicity and replay; and the current packaged UI/API, trace and monitoring harnesses for deployed delivery and actual dashboard expressions. Source/security/dead-code gates, configuration/Helm checks and documentation-site checks remain required for their changed boundaries. Reuse current Make targets and fixtures. Keep procedures, configuration examples and test assertions aligned; retain source-matched measurements and unresolved failures in repository verification records.
