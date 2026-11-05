# Authorization evolution qualification

ADR-0022 accepts local PostgreSQL RBAC. The implementation enforces domain actions and scopes, stable command identity and current replay disclosure. It also enforces claimed worker authority and authorized collection/search selection. Authentication uses compact principals. Browser administration is bounded. Separate qualification covers replicas, retained recovery policy and current costs.

The original SQL trials remain distinct from production enforcement. [ADR-0022](../../documentation/src/content/docs/architecture/decisions/0022-local-postgresql-authorization.md) defines the current authorization model. The sections below describe the fixtures, their limits and the test commands.

`inventory.json` is the reviewed target catalog: 50 permissions, 14 built-in templates including the inert service-client template, companion-read requirements, the implemented HTTP operations, conditional permissions, current authority, scope/disclosure/eligibility rules, the protected domain service entry points and internal/operator execution owners. It is not loaded by the runtime and is not a policy engine. Its `currentAuthority` column retains the frozen pre-cutover baseline; implemented current decisions are qualified by the owning code/tests, not that historical role description.

Its scope labels mean assignment applicability, not permission to accept a browser-supplied scope without resolving the protected target. The operation policy supplies actor/lifecycle/disclosure constraints in addition to its permissions. Job authority and external-search/feedback/public-host branches have explicit additional conditions.

The isolated `TestAuthorizationEvolutionOperationInventory` and `TestAuthorizationEvolutionDomainInventory` cases check complete OpenAPI/domain coverage, known policy/permission references, unchanged OAuth ceilings and usable namespace-capable templates. They are documentation/contract drift checks, not proof that production enforcement implements the matrix. Update reviewed fixtures when a protected caller changes; do not regenerate expectations merely to accept a changed allow decision.

`scenarios.json` contains twelve independent expected decisions covering direct/group access, same role in distinct namespaces, tenant/realm collisions, inactive identities, service non-privilege and unsupported actions. `TestPostgresAuthorizationEvolutionScenarioOracle` executes them against explicitly test-only role/assignment/group SQL relations, checks selection before counts with duplicate direct/group paths, and checks removal of a group path preserves direct access, tenant grants cover other namespaces, and RLS hides another tenant's otherwise valid grant. The trial omits production administration and is not qualification of its eventual full schema/privilege manifests.

`TestPostgresAuthorizationEvolutionAdmissionOrdering`, `TestPostgresAuthorizationEvolutionReadSnapshots` and `TestPostgresAuthorizationEvolutionBootstrapAndLockOrder` qualify PostgreSQL 18 mechanics in the existing disposable exact-login fixture. Trial tables use forced tenant RLS and explicit column grants to its API login; the worker gains no grant. No applied migration, production role manifest or retained data is modified.

Successful trial queries do not make that expanded test role a compliant production runtime manifest.

The assertions observe the selected blocked session through `pg_blocking_pids`, not a sleep or eventual success. They cover admitted-first/revoked-first ordering, fresh permission evaluation after waiting, rollback, shared admission, independent tenants, timeout/cancellation/recovery, missing coordination state, owner-only repeat-safe bootstrap, advisory/row lock order, and Read Committed versus Repeatable Read. `FOR KEY SHARE`, a combined lock/permission statement and a constant collection predicate are negative controls.

The SQL trial does not qualify production command/audit/outbox replay integration. The domain and replay tests below cover that boundary.

`TestPostgresAuthorizationBaseline` measures the actual resolver with 0/100/1,000 namespace grants through the current API role, exact direct SQL versus already-resolved Go checks, and the existing coherent Search snapshot over 20 governed releases. Statement counts and hydration bounds are assertions. Wall times are logged diagnostic samples, including their setup exclusions; there is no arbitrary performance threshold or claim about custom-role/group throughput.

The current dense/sparse and compact trials in final qualification below supplement this historical control.

## Run the existing gates

```sh
# Isolated inventory checks, no server or database.
go test ./internal/platform/httpserver -run '^TestAuthorizationEvolution' -count=1

# Creates and cleans its own PostgreSQL Compose fixture; includes all new trials.
make test-integration

# Source/analyzer checks and broad local functional/race qualification.
make check verify

# Complete isolated units, without application listeners, processes or SQL.
make test-unit

# Reader-site source, build and navigation checks.
make docs-site-check
```

The Go database cases retain the `integration` build tag. The owning `make test-integration` target selects the digest-pinned PostgreSQL 18 image in `compose.integration.yaml`, an invocation-owned project, loopback port and volume cleanup. Do not substitute a retained development DSN.

For a focused rerun against an already explicitly selected disposable fixture only, use `go test -race -tags=integration ./internal/platform/database -run '^TestPostgresAuthorization' -count=1 -v`; absent `CATALOG_TEST_DATABASE_DSN` skips execution and is not PostgreSQL evidence. No new Make target, tool installer, cluster or service launcher is required.

## Compact principals and write admission

`identity.PermissionStore` provides compact resolution, bounded current checks and write admission. `permissions_test.go` checks its finite Go descriptors/templates against the accepted fixture; the PostgreSQL core tests independently compare migration-seeded templates, exercise actual runtime-role/RLS restrictions and observe selected sessions waiting on the real authorization row. These are separate from the test-only trial relations.

`TestPostgresAuthorizationCoreDecisionsAndHistory`, `TestPostgresAuthorizationCoreAdmissionAtomicityAndOrdering`, `TestPostgresAuthorizationCoreForwardMigration` and `TestPostgresAuthorizationCoreStaleAdministratorAndMissingState` cover migration 000034/manifests, scoped decisions, transaction/evidence rollback, response-held admission, waited revocation, retained assignment preservation/refusal, stale administration, same-tenant references and missing-state restore refusal. They run through the existing PostgreSQL target and focused prefix above. `make test-process-postgres` additionally qualifies the matching real API/worker/CLI process boundary.

Authentication uses compact `ResolvePrincipal`; `/api/v1/me` projects bounded current finite permissions and namespace hints separately. Hydrated `Resolve` remains only as a historical/bootstrap comparison test control, never runtime authority. Domain/replay tests cover the current cutover and explicit split-bundle conversion. Collection tests qualify private selection.

An admitted callback performs synchronous bounded local work and must not spawn asynchronous work or call external services. Memory mode coordinates current authority, but supplies no generic transaction rollback across repositories. No additional configuration, environment or Make target is required.

## Role and group administration

`identity.AdministrationStore` has narrow synchronous methods for role/group/member/assignment records. Unit tests in `administration_test.go` exercise finite custom sets, Unicode bounds, composition, guarded lifecycle, immutable built-ins, evidence rollback, current authority, signed paging and detached projections. HTTP unit tests inject typed ports; listener-based response-schema, replay and malformed-input tests retain the `functional` tag.

The [reviewed inventory](inventory.json) lists the current HTTP operations and protected domain entry points. Inventory coverage alone does not prove enforcement.

`TestPostgresAuthorizationAdministrationCompositionAndLifecycle`, `TestPostgresAuthorizationAdministrationSchemaAndRollback`, `TestPostgresAuthorizationAdministrationRealmIsolationAndSchemaGuards` and `TestPostgresAuthorizationAdministrationRestoreGroupMembership` use actual API logins and the owned PostgreSQL fixture. They qualify exact custom sets/parents, real rollback, duplicate paths, revocation blocking, offboarding, restore findings, forced RLS and prospective SQL guards. They run through `make test-integration` and the focused prefix above.

`make test-process-postgres` additionally creates a role/group grant through the actual API, restarts the process, verifies one retained mutation/audit with byte-identical replay, then revokes administrator access and requires refusal before private replay bytes. It does not replace complete domain, collection, replica or browser qualification.

Run `make coverage-unit` alone for the complete isolated coverage report, `make check verify` for source and broad local functional/race/profile gates, `make client-go-check` and `make ui-check` for generated contracts, and `make docs-site-check` for the reader manual. No new launcher, dependency or configuration is required. Administration writes have immutable audit-only evidence; current policy/replay fences use the existing command transaction.

Retained development databases and unrelated containers must not be selected.

## Administration failure and replay coverage

`TestPostgresAuthorizationEveryMutationRollsBackLateAuditFailure` qualifies all twelve role/group/member/assignment mutation forms against the actual API-role store. The injected identifier generator deliberately collides at the final audit INSERT; the test requires that exact PostgreSQL constraint failure and compares retained policy, permission, revision, audit and outbox rows. Removing only that fault must change the intended table, append exactly one audit and leave outbox rows unchanged.

`TestPostgresAuthorizationRefusalsPreserveExactRetainedState` covers 27 missing-target, wrong-parent, stale-revision, built-in and terminal-state cases. These `integration` cases run automatically through `make test-integration` and `make coverage-full`; no schema guard or privilege is weakened.

`TestAuthorizationAdministrationReplayRequiresCurrentGrant` and `TestGroupPolicyChangesImmediatelyGovernHTTPHintsAndRetainedCommands` exercise actual local HTTP. Direct regrant, group suspension/reactivation and membership revocation/replacement must immediately change current hints and cached-response disclosure. Allowed replay preserves original bytes and ETag; denied replay exposes no private object or success headers.

Complete directory pages check duplicate execution and immutable terminal relation history. These cases retain the `functional` tag and do not enter unit selections.

```sh
# Focused real HTTP scenarios, without PostgreSQL or Compose.
go test -tags=functional ./internal/platform/httpserver -run 'Test(AuthorizationAdministration|GroupPolicyChanges)' -count=1 -shuffle=on -race

# Fresh unit/local/PostgreSQL/process profiles and compatible application union.
# Run alone; do not edit source or run another profile writer concurrently.
make coverage-full
```

More coverage is not proof of every failure path; prior browser, replica, restore and performance measurements retain their separately dated scope.

The restore fault fixture constructs valid owner-side transitions from retained timestamps and separately proves that backdated transitions remain rejected by PostgreSQL. Worker outage/recovery sampling and unchanged recovery requirements are documented in the [functional fixture guide](../functional/README.md).

These are test-fixture repairs, not new application authority or recovery behavior.

Separate qualification covers two-Catalog federation, HF/MCP/A2A/Skill/Plugin consumers, owned demo loading, fuzzing, maintained properties/frontend mutation scopes and repeated authorization races. Use the existing independent targets and their owning guides; ordinary units never execute these runtimes. Offline MCP/A2A builds resolve and version-check the exact installed/cached repository compiler instead of requiring the launcher itself to match.

Compiler resolution does not change Catalog permissions or refresh statement coverage.

## Domain authorization and replay

Isolated `identity/enforcement_test.go`, Catalog document-access tests and domain suites check exact actions, metadata/history/content separation, detached checks, revocation and forged worker actors without starting services. Real PostgreSQL `authorization_domain_integration_test.go` exercises revocation after preflight before write (no mutation/audit/outbox), scoped raw content versus metadata-only access, ended claims and the policy revision recorded in administration audit. The forward-migration fixture proves an expired unfinished reservation blocks cutover and resumes after owner repair.

Local HTTP qualification covers unrelated policy changes, deleted-target replay and revoked webhook-secret disclosure; real process tests retain restart/durable replay checks. Run the existing gates above and `make test-process-postgres`, `make test-functional`.

Unfiltered Jobs use eligible-family and retained-target predicates before paging. Namespace-only collections/search use current policy. Memory admission coordinates bounded synchronous work but is not generic cross-repository rollback.

No new configuration, target or tool dependency is introduced.

## Authorized collections and search

Isolated `internal/identity/collection_test.go`, Catalog/Jobs collection units and PostgreSQL query-binding units cover coherent admission, cancellation/panic cleanup, tenant binding, independent populations, permission failures and denied federation disclosure. `TestScopedDirectoryHTTPPagesAndRevocation` uses a real local listener. `TestPostgresScopedCollectionsAndPolicyContinuation` uses the exact API login, governed releases, direct/group grants and sparse SQL plans; hybrid assertions extend the existing semantic indexing integration fixture.

These real-boundary cases retain their functional/integration tags.

Run `make coverage-unit` alone, `make check verify`, `make test-integration`, `make test-process-postgres`, `make client-go-check`, `make test-ui`, `make test-functional`, `make test-api-generated` and `make test-semantic` with the installed documented tools/model. The semantic plan probe binds a real compact administrator and all twenty current hybrid parameters; it requires a full 21-row diagnostic page under each selected plan/JIT mode. Collection tests do not grant policy-table privileges to workers.

The record separates fresh results, corrected obsolete fixtures and the retained transient asset-loading failure.

## Browser, replica, cost and recovery qualification

`make test-ui`, `make test-ui-compose`, `make test-ui-oidc` and `make ui-visual` select the existing independent browser fixtures. The shared administration journey uses separate administrator/member sessions, custom namespace-only roles, direct group membership, role edits, group suspension/reactivation, assignment revocation, denied deep links and repeat provisioning/seeding. Packaged Compose additionally checks retained-session database outage/recovery.

Units cover finite permission validation, exact namespace resolution and metadata/history/content separation; capabilities remain hints rather than browser authority.

`make test-process-postgres` starts two actual API processes sharing the exact PostgreSQL API login. Role edits written through different processes must affect both immediately. After process replacement it requires exact durable replay, one audit and refusal of private replay after revocation.

`make test-minikube-replicas` separately exercises the maintained three-UI/API/worker deployment; no new cluster workflow or HA controller is added.

`TestPostgresAuthorizationCurrentPolicyCost` measures warmed five-sample current reads with 1,001 namespace assignments, 100 groups, dense/sparse direct membership, bounded self/directory pages, 20 governed Search records and audited group churn. `TestPostgresAuthorizationBaseline` retains the historical hydrated control and compares compact principals across 0/100/1,000 grants. Run `make test-integration GO='env GOFLAGS="-mod=readonly -v" go'` to retain diagnostic samples without changing the suite.

These are local race-instrumented costs, not timing gates or capacity claims.

`make test-backup-restore` and `make test-postgres-failover` reuse `test/functional/authorization_recovery.py`: retain a custom scoped group role, active membership, separately revoked edit grant and completed member command. After recovery, verify exact policy rows, scope/denial and byte-identical replay with one audit before advancing to worker/resumed workflow verification. Helper-unit fault controls remain service-free. Forward-migration refusal, retained evidence and repair/resume cases stay in the existing integration suite; no retained deployment reset is authorized. `make coverage-full` regenerates the complete compatible unit/local/PostgreSQL/process union. The qualification record owns actual outcomes and any failed or unrun scope.
