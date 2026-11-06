# PgBouncer qualification

Run `make test-pgbouncer` from the repository root with the maintained Go/Docker/Python environment and OpenSSL installed. The exact PgBouncer 1.26.0 and PostgreSQL 18 image digests are in `runner.py`; acquisition is explicit to this selected real-boundary gate. It creates a randomly named Compose project, private temporary credentials/certificates, loopback listeners and fresh primary/standby volumes.

Select abrupt writer loss with `make test-pgbouncer PGBOUNCER_WRITER_LOSS=abrupt`. The default is `planned`; it preserves the governance smoke, dependency restarts, planned promotion and six Search trials.

The abrupt selection holds a new resource-created outbox event through a direct-owner row lock. It drains earlier eligible Jobs before holding Namespace and source-snapshot writes. It then requires an incomplete command reservation, a running first-attempt Job with a live lease, and both blocked runtime roles. Empty source results need no conditional HTTP state; the snapshot write supplies the Job barrier.

Before SIGKILL, the helper waits for WAL replay and checks the exact reservation, Job owner/attempt and pending event content on the standby. It records the two API and two worker execution identities in that order. The old writer must stop before promotion and the writer-alias change. Its exit code must be 137. Both exact runtime roles must then execute fresh SQL through PgBouncer.

The abrupt fixture also tests recovery through the original processes. It rejects repair while the command is active and waits for the original request to end after fencing. It then plans and applies the exact interrupted-command repair through the direct owner CLI. Reapplying the stale plan must fail. The original command must succeed once, replay with identical bytes/ETag, and fail after the author's grant is revoked.

The first worker claims the selected Job before the second worker starts. After all four listeners report healthy liveness and degraded readiness, the fixture promotes the standby. An owner session blocks snapshot writes through a Unix socket, which survives the Docker network change. The fixture temporarily pauses the first worker so the second can claim the expired lease. It then resumes the first worker without restarting it.

A stale completion with the captured old owner must fail while the newer attempt remains running. The genuine retry must complete the same Job, with one completed sync transition. The exact pre-fault event must retain its content and progress to acknowledgement. No receiver is registered; this verifies outbox processing without a remote delivery claim. Audit-only repair must create no outbox record.

All four original process identities, useful authenticated reads and the public snapshot bytes/ETag must survive recovery. The report records WAL positions, record IDs, an event digest, old/new Job owners and each required recovery proof. Missing proofs fail the gate. Its recovery duration includes the controlled worker pause and is not an availability objective. The selected compiled-process case has a five-minute deadline; application timeouts and lease settings remain unchanged.

It never selects retained Compose services or ambient database DSNs. Runtime credentials go only to the pooler userlist and test processes; the schema owner, replication password and CA private key are excluded from pooler mounts. No runtime admin user or auth-query helper is added.

The runner reuses the functional process-group supervisor, compiled process/smoke helpers and physical standby/fenced-promotion control. Startup requires authenticated SQL, not a liveness probe against PostgreSQL's temporary init server. A retained standby may restart only with this invocation's exact ownership marker; it is never erased/resynchronized.

After promotion, the fixture executes SQL through fresh API and worker connections. Each session must use the exact role on the writable server. API readiness alone does not establish worker readiness. The bounded probe waits only for PgBouncer's known cached-login error. Other connection failures fail immediately.

A controlled worker `NOLOGIN` test keeps the API ready and requires a compiled worker to refuse startup. The fixture restores `LOGIN`, verifies both role connections, then starts the replacement worker once. This test changes only its owned database roles. It does not add application startup retries.

Infrastructure changes one stable writer alias after fencing and promotion. Do not model production failover by giving the pooler an unordered primary/standby host list.

Eleven required Go cases cover compiled processes and their certificate-renewal child, runtime admission, session locks/tenant reset, capacity/reset/cancellation, client authentication/trust, server certificate validity, upstream trust, exact-privilege refusal, role timeout defaults and dependency recovery. The default compiled case exercises two API/two worker processes, current role/group/namespace authority and revoked replay, three-kind governed smoke and Plugin draft isolation. The maintained direct integration suite owns positive Plugin archive/publication and the complete fault matrix; this fixture does not duplicate it.

Matched bounded API namespace and worker Job reads record direct/pooled overhead without a timing floor. Warm backend counts, independent worker capacity, server queue timeout, rollback/lock release and reset of a reused physical PID are asserted. Both TLS hops have successful and CA/hostname-refusal controls.

The reset control waits for pgx to finish cleanup of a timed-out queued client before releasing the holder. This prevents that client from taking the freed backend during the reuse check. The holder retains its original PID and private setting. The next connection must reuse that PID, clear the setting and acquire the released lock.

Upstream TLS failure is diagnosed by actual certificate errors plus the bounded `query_wait_timeout`, not assumed raw client SSL text.

Each upstream trust case logs a fixed candidate label, connection stage and error type. It records each assertion separately: a PostgreSQL error, the expected queue timeout and an active test context. It does not print connection strings or raw errors.

The TLS fixture creates independent predecessor and successor CAs with SAN-correct server leaves. It verifies both chains, distinct keys and cross-CA rejection. Separate same-CA replacements extend the original leaves' validity.

Separate owned endpoints serve expired and not-yet-valid leaves without changing the active writer. Fresh pgx connections reject the exact pooler and PostgreSQL leaves at their native X.509 date checks. The pooler also rejects each invalid PostgreSQL identity. Its fresh TLS diagnostics and bounded queue refusal must agree. Successful connections verify the API and worker logins, session mode, backend TLS and the direct owner before and after each fault.

The leaf validity windows have one-day margins. The fixture changes no host clock and waits for no certificate to expire.

These checks cover server identity on both hops. Runtime clients continue to authenticate with SCRAM passwords; no database client-certificate authentication is added. CA private keys remain outside container mounts. Activation delivers the selected server key pair through its existing scoped mount. Certificate evidence contains only public fingerprints, serials, dates and fixed outcomes. Bounded failure diagnostics include pooler connection-close reasons and observed clock adjustments.

## Renew server certificates and retire trust

The compiled-process case runs five fixed phases after its writer-loss selection. Both `planned` and `abrupt` execute this same renewal sequence. Writer loss and certificate replacement retain separate evidence.

| Phase | Activation | Required evidence |
| --- | --- | --- |
| Baseline | Start one API and one worker against the original identities. | Exact served leaves, runtime roles, backend TLS, direct owner access and one completed Job. |
| Same CA | Deliver renewed key pairs, then drain clients, restart PostgreSQL and replace the single pooler. | New keys and later expiry under the original CA, new served leaves and fresh backend sessions. |
| CA overlap | Keep the renewed leaves. Replace pooler and application consumers with the reviewed old/new trust bundles. | Both authorities remain accepted, and the PostgreSQL process stays unchanged. |
| Successor | Drain clients. Activate successor leaves through PostgreSQL restart and pooler replacement. | New served fingerprints on both hops, fresh backend sessions and acceptance of valid old-chain controls. |
| Retirement | Remove predecessor trust through another pooler and application replacement. | Current leaves still work. Fresh connections reject the valid old pooler and old database chains. |

Every phase completes an authorized Namespace read and a distinct source-sync Job through the actual API and worker. Direct owner SQL confirms the Job's retained terminal state. Served fingerprints come from real PostgreSQL TLS exchanges. Runtime backend PIDs, start times and the writer's start time distinguish new sessions from cached connections. Pooler container IDs and API/worker execution IDs must also change.

The file-only controls deliver complete key pairs without replacing their consumers. Fresh handshakes must still return the previous served leaves. These controls prevent copied files from counting as activation. A separate valid old-chain database proves pooler upstream acceptance during overlap and native verification failure after retirement.

The operator helper stops only this invocation's pooler and promoted writer. It never restarts the fenced predecessor or gives Catalog processes administrative pooler access. Clients drain before each replacement. The pooler is recreated once per phase without an overlapping instance. Runtime roles, passwords, connection caps and queue/query budgets stay unchanged.

The test restores only its original, unrevoked fixture inputs before the remaining protocol cases. Cleanup independently removes its private containers, volumes and key files. `tlsRenewalEvidence` contains five validated phase records and the required acceptance/refusal proofs. A complete checkpoint survives a later test failure; the overall report still fails. Unit checks reject missing phases, wrong leaves, reused backends, missing work and invalid activation order.

Kubernetes Secret projection and Pod activation use the separate Minikube selection. This local renewal test does not qualify that boundary. The [operator procedure](../../documentation/src/content/docs/operations/database-roles.mdx#replace-database-server-certificates) explains trust order and maintenance responsibilities.

## Reports and regression

`output/pgbouncer/run-*/report.json` records commit/worktree digest, image/version/config identity, required passed cases, failure disposition and exact owned cleanup. Passing `tests.jsonl` includes bounded durations/backend counts, separate from statement coverage. A missing/skipped/failed Go case, source drift or cleanup failure fails the gate.

On failure private bounded/redacted Go stdout and service diagnostics may be retained; incomplete runs never count as passed. Commands have deadlines/output limits and own process groups. Cleanup ignores a second interruption and removes only the selected project's containers/network/volumes and private inputs.

No daemon prune, retained-data reset or host tuning is performed.

Pure configuration/report/ownership-parser checks live in `test_unit_runner.py`, selected by `make test-tooling-unit`. They start no processes or sockets. Real Go tests retain the integration tag and run only through this explicit gate; normal integration reports a meaningful unselected skip.

Run the unchanged `make test-process-postgres` and `make test-integration` as the direct regression baseline. For actual chart/TLS/network enablement, replacement and disablement run `make test-minikube-pgbouncer` using the [owned harness](../minikube/README.md).

The default compiled-process case also reuses the maintained `test/performance` client for three alternating direct/pooled public-local Search pairs against the same three governed smoke records. Each trial uses two closed-loop readers, one second of warm-up and three seconds of measurement, with a 100,000-request ceiling. A separate direct API process uses the same runtime settings and database; both database routes verify private TLS.

Complete warm-up/measurement, zero errors and the same exact response digest across all six trials are required. Timing/count/bucket reports remain in `tests.jsonl`, labelled by route/trial. No timing floor, new performance framework or separate Make target is added.

The ordinary performance fixture's load/scale reports and these selected pooler measurements remain separate.

This small warm fixture measures achieved throughput and transport/query overhead, not offered-rate capacity, a large dataset or production sizing. Compilation and fixture setup precede timing; trials run serially, while the same API/worker fixture remains alive. Docker CPU/memory limits are unchanged from this compatibility fixture, not normalized to the separate scale harness.

Avoid concurrent benchmarks or cluster/network campaigns and keep source unchanged during execution.

The [operator pooling runbook](../../documentation/src/content/docs/operations/database-roles.mdx#connection-pooling-boundary) owns session-only limits, explicit DSN routing, startup parameter restrictions, protected SCRAM inputs, separate budgets, both trust hops and direct owner maintenance. Keep artifact review, findings, runs and remaining limits in local reports. Session pooling preserves warm pgx clients and does not promise connection reduction or higher throughput.
