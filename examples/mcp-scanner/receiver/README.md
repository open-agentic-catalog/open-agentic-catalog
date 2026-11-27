# Scan MCP definitions after a release notification

This example connects Catalog release webhooks to the third-party Cisco MCP Scanner. Its HTTPS receiver collects definitions from an explicitly configured local MCP server. The receiver uses the Catalog Go SDK and the MCP Go SDK. PostgreSQL stores its inbox, reports and observations in a separate database.

The receiver never invokes tools or changes Catalog records. It does not approve, revoke or block releases. It does not enable Catalog software assessments. A runtime endpoint can change after publication; these reports describe captured definitions, not immutable runtime code.

The [manual](../../../documentation/src/content/docs/guides/mcp-scanner.mdx) owns operator setup and the complete Catalog/worker/MCP workflow. This README covers receiver development, processing behavior and limits. Native YARA controls and the full Compose campaign use the installed locked scanner. Focused receiver tests use an explicit scanner executable double; they do not qualify the real scanner.

## Prerequisites

- Use a non-root Linux AMD64 host and a source checkout.
- Provide Go 1.27.4 and the [installed offline scanner](../README.md#install-the-scanner).
- Provide a separate PostgreSQL 18 database and account for this receiver.
- Obtain a Catalog token with access to the configured resource namespaces.
- Configure a local HTTPS MCP endpoint with a separate bearer credential.
- Provide trusted certificates for the Catalog, MCP endpoint and receiver.

The MCP endpoint must use Streamable HTTP with protocol `2025-11-25`. Both JSON and finite SSE responses are supported. The client disables standalone SSE, sampling, elicitation, roots, automatic multi-round requests and reconnect retries.

The example pins the same MCP Go SDK and pgx versions already used by the repository. It has its own module and lock. It does not import the Catalog application module. The copied webhook verifier stays byte-identical to the [verification example](../../webhookreceiver/verify.go).

## Build and test

Install the [shared development tools](../../../documentation/src/content/docs/contributing/build-and-release.md#shared-development-tools) for the Python process fixture. Provide Docker with Compose for the owned PostgreSQL test. Receiver checks do not require an installed scanner.

Run these targets separately from the repository root:

```sh
make dev-tools-install
make sdk-tools-install SDK_LANGUAGE=go
go -C examples/mcp-scanner/receiver mod download
make mcp-scanner-receiver-build
make mcp-scanner-receiver-check
make test-mcp-scanner-receiver
```

Build writes `bin/examples/mcp-scan-receiver`. The check runs isolated units, real local MCP/TLS tests, analyzers and dependency license checks. It also qualifies the Go SDK.

The PostgreSQL target owns its database container, receiver database/account, HTTPS listeners and receiver process. It uses controlled Catalog responses and a Python executable double. It verifies the adapter without installing or executing MCP Scanner. The separate offline check qualifies actual YARA after approved installation.

Concurrent admission checks observe two independent PostgreSQL sessions waiting at the actual admission lock before release. They verify the last capacity slot, equal-ID acknowledgement at capacity, pending-receipt preservation and bounded completed-receipt pruning. These inbox assertions do not depend on the scanner double's behavior.

Run the independent module's existing event fuzzer separately:

```sh
make test-mcp-scanner-receiver-fuzz FUZZ_TIME=10s
```

The selected budget accepts 1 to 60 seconds. The target requires the installed Go toolchain and cached module dependencies; it makes no scanner, database or network request. It first requires native seed-owner completion, then runs the bounded campaign with two workers. Reports bind source and Go identities under `output/mcp-scanner-receiver-fuzz/`. A failing campaign preserves its exit status even if bounded counterexample retention fails. Only new synthetic files for `FuzzEventAdmission` are eligible; retained input files remain untouched. Review and minimize an actual counterexample before adding a permanent regression. This selection is separate from root-module `test-fuzz`.

## Configure and run the receiver

Follow [the manual's example receiver setup](../../../documentation/src/content/docs/guides/mcp-scanner.mdx#configure-the-example-receiver) for private inputs, endpoint mapping, minimum permissions, the separate database, webhook registration, signer installation and startup. It includes normal delivery, results, recovery and retirement. The manual owns that operator procedure; this README owns build/test commands and the implementation limits below.

The receiver uses an explicitly configured local HTTPS MCP endpoint and separate credentials. It never treats a notification or Catalog record as authority to choose an arbitrary destination. Reports stay outside Catalog assessments and release decisions.

## Processing, reports and recovery

Acceptance verifies signatures and the event schema before storing the `(source, id)` receipt. It commits receipt deduplication before returning `204`. Full or unavailable storage returns `503`. The asynchronous processor uses one pending item at a time.

The processor reads the exact eligible release before collecting definitions. It permits MCP initialization, `tools/list`, cancellation and session cleanup only. It checks Catalog access and the same release again after analysis. Changed eligibility produces a historical report, without erasing completed findings.

Each report binds the Resource, release, ListingRevision, VersionRevision, content-set digest and representation digest. Captured input, scanner image, rules and configuration have separate hashes. The event's optional digest remains transition context. It is never used as the captured-input checksum.

The report directory contains `index.json`, one metadata file per Resource and one native scan file per Resource. Metadata records the native file's SHA-256 hash. Database completion and file rendering are separate operations. Compare that hash before using a scan file, or render again after an interrupted write. The database remains the receiver's authority.

`eligible_at_check` means the release was eligible at the recorded `eligibleAt` time. It does not promise continuing permission or unchanged runtime code. Other states include pending, partial, failed, skipped, historical and not listed. An empty capture is skipped; it cannot count as completed analysis. Old observations retain their original capture context.

The processor reuses completed analysis only for the same release, capture digest and scanner identity. It still collects fresh definitions and rechecks Catalog access. Reused reports retain their original analysis timestamp. Failed or partial analysis never becomes a successful cached result.

Startup and periodic reconciliation read both loss counters and traverse the configured namespaces. Traversal continues even when counters are unchanged or decrease after a restore. A complete traversal schedules the current release for each configured Resource. If the same release is already pending, it keeps its retry schedule. A change of release replaces that pending work. If the Resource disappears, the receiver clears its pending reconciliation work and keeps the last scan for reference. A failed or incomplete traversal preserves the previous selection.

Signed notifications have their own receipts. If a delayed publication notification receives a Catalog 404 after reconciliation confirmed the Resource was absent, the receipt records the failed lookup. The observation stays `not_listed`. An endpoint test completes its receipt without changing any release observation.

Restart with the same database and binding after interruption. Accepted pending work survives. A session advisory lock refuses a second processor. Loss of that lock connection stops processing; restart the single receiver after repairing the database connection.

Stop `serve` before a one-shot command. Reuse its connection, mapping and `--reports-dir` flags. Include token and trust flags for Catalog reads:

- Run `reconcile` to refresh Catalog membership and schedule scans. This command does not contact MCP.
- Run `process` to process at most one due receipt. Include the scanner flags.
- Run `render` to regenerate private files from the database. This command needs no Catalog token or scanner flags.

These commands accept the same flags as `serve`; listener and signer flags are required only for `serve`. A failed render does not undo stored results or receipt completion.

## Limits

| Work or setting | Limit or default |
| --- | --- |
| Resources and namespaces | 32 Resources, eight namespaces |
| Configured MCP endpoint URL | 1,024 UTF-8 bytes, including the path |
| Catalog reconciliation | Eight pages total, 32 entries per page, 30-second budget |
| MCP collection | Eight pages, 128 tools, 1 MiB, 30 seconds |
| Scan analysis | 30-second process budget |
| Full processing step | Three minutes, plus bounded scanner-child cleanup |
| Native report | 4 MiB |
| Retained notification receipts, `--capacity` | 1,000 by default; accepted range 1-10,000 |
| Retained scans | Same independent capacity limit |
| Reconciliation work | One slot per configured Resource, outside notification capacity |
| Retention, `--retention` | 24 hours; accepted range 1 minute-30 days |
| Poll interval, `--poll-interval` | 5 seconds; accepted range 1 second-1 minute |
| Reconciliation interval, `--reconcile-interval` | 1 minute; accepted range 5 seconds-24 hours |
| Transient retries | Three attempts with bounded exponential delay and jitter |
| HTTP admission | 16 concurrent requests, 64 connections, 256 KiB events, 16 KiB headers |
| Database pool | Four connections, including the processor lock connection |

The capacity must accommodate every configured Resource's latest scan. Full report storage records a failure; it does not evict a protected latest report. Increase capacity or let unused reports expire before scheduling another scan. Acceptance prunes at most 128 expired completed notification receipts. Report completion prunes at most 128 expired scans that no observation references. Pending receipts do not expire.

Press Ctrl-C or send SIGTERM to stop. Scanner-child cancellation allows up to one minute for its cleanup. HTTP drain allows a further ten seconds. An interrupted scan remains pending for restart. No command publishes images, uploads reports or changes retained Compose services.
