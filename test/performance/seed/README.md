# Governed scale setup

This test-only command is launched by `make test-scale`, not an operator import tool. It accepts the supervisor's owned PostgreSQL loopback port, closed item count, setup deadline and exclusive private result path. It does not accept an arbitrary URL or DSN.

Never point it at retained data.

`make test-semantic` also uses the helper with the explicit final `local` argument. That mode adds governed releases to the three-record demo baseline until the requested population is reached, without creating indexed-source fixtures. The semantic supervisor owns the database and validates automatic indexing before its 1,000-item query pilot.

Both modes use version 2 of the shared four-kind corpus. Earlier three-kind measurements remain version 1.

`make test-public-delivery` selects a separate `public` mode, fixed at 1,000 releases including the three smoke releases. It adds 997 small native MCP Server Cards through the same authoring, independent review and publication workflow. These public listings include the required representative queries and publisher-domain approval. Server Cards use their own schema; the existing scale fixture's Registry `server.json` records remain unchanged. It creates no indexed Sources.

Setup provisions synthetic identities through the schema owner, then uses exact API and worker database roles for normal validation, independent review, release creation and audited federation synchronization. Webhook notifications follow the disabled deployment's acknowledge-at-creation behavior. The packaged worker is stopped during setup so one real, leased Job can use static in-process federation retrieval without racing a network worker.

The scale fixture adds a reader with 105 Namespace grants. Its populated Namespace sorts after the first 100 `/me` grants. Dashboard checks verify its exact visible population. Ordinary load and soak fixtures do not add this identity.

A second source contributes seven existing indexed identities. Search and Overview must count each identity once and retain both source references. Plugin archives come from the embedded maintained samples. The helper fetches only their exact known URLs through an in-process fixture transport. Normal package inspection retains their component evidence.

The supervisor restarts the packaged worker, rediscovers its temporary port and verifies readiness and drained work before measurements.

Preparation runs in pairs within the existing two-connection pool limit. Each draft keeps its ordered authoring, validation and independent review commands. The helper joins both preparations before publishing either record. Publication runs serially, before the next pair starts. This avoids concurrent changes to the tenant generation during Serializable publication.

A failed preparation cancels and joins its sibling; publication failures stop without retries. Only fully published records enter the completed count. Indexed setup and final inventory queries remain sequential. At most 9,997 external entries are retained for one bounded source snapshot. Local records are not accumulated in memory. Setup is not an HTTP ingestion benchmark.

The helper uses the schema-owner connection for ANALYZE and fixed tenant-scoped count/byte queries after seeding. Reports retain numeric inventory, helper allocation totals, a bounded failure stage and safe error codes; they contain no documents or database credentials. Cleanup belongs to the existing performance supervisor and `perf-clean` recovery path.

Scale and public-delivery setup run the static helper in a temporary container, using the invocation's exact backend image. It shares the owned PostgreSQL network namespace and connects to loopback port 5432. This avoids the host-published port without changing database work, roles or transaction boundaries. The helper has two CPUs, 512 MiB of container memory and a 256 MiB Go memory target. Its root filesystem is read-only, capabilities are dropped and evidence uses the private invocation directory. Both ownership labels participate in normal and abandoned-run cleanup. The container exits before HTTP measurements; application and PostgreSQL limits remain unchanged. Semantic setup retains its existing host helper.

The `seedResult.sql` map records calls, failures and elapsed microseconds for fixed statement classes. It does not retain SQL, parameters or error text. These client-side times include network and server work; they are not PostgreSQL execution plans. Preparation overlaps queries, so the sum of SQL times can exceed setup elapsed time. The map includes provisioning and inventory queries. A failed setup keeps its actual `failureStage` and `seedResult.stage`; an earlier source-sync command is not its cause.

`completedLocalRecords` counts fully published generated records. It excludes the three smoke releases. An unreadable partial report sets `seedReportStatus` to `unreadable` without replacing the original failure.
