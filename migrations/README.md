# Embedded PostgreSQL migrations

Numbered `.sql` files are the forward-only schema history embedded by [embed.go](embed.go). The migration runner in [internal/platform/database/migrate.go](../internal/platform/database/migrate.go) plans and applies them; database lifecycle and exact runtime grants are documented in the [database-role runbook](../documentation/src/content/docs/operations/database-roles.mdx).

Fresh installations use these files to create the current schema. The Helm pre-install Job and `make compose-up` apply them and configure runtime database permissions automatically. After supplying the documented deployment inputs, no separate migration command or runtime drain is needed for a fresh installation.

The stop/drain and retained-data rewrite or repair instructions below apply when updating an existing database. Use `make helm-deploy` or `make compose-up` to coordinate that update. Raw `helm upgrade` does not stop existing API and worker Pods before its migration hook runs.

Migration `000053_adoption_version_selection.sql` adds a partial B-tree index for retained adoption Source selection. It changes no rows, runtime grants, RLS rules or immutable evidence. Apply it with the normal drained schema-owner workflow; allow index-build storage and write-lock time. Failure rolls back this migration; correct the cause and retry forward before restarting runtimes. No data backfill or automatic rollback is required.

Migration `000031_agent_plugins.sql` adds the Plugin resource kind and immutable retained inspection documents/inventory. Runtime privilege manifest v15 introduced API insertion and API/worker reads of `plugin_inspection`, without update authority. Drain API and worker runtimes, apply migrations with the schema owner, reconcile both manifests, then start the matching binaries. External archives are not copied into PostgreSQL or reconstructed during restore.

Migration `000046_publisher_search_order.sql` invalidates search cursors when a Publisher changes, because its displayed name can determine result order. It reuses the existing tenant generation function and adds no table, function or runtime grant; manifest v26 is unchanged. Apply it through the normal drained schema-owner workflow. Failure rolls back this migration; keep runtimes stopped, fix the cause and rerun. There is no data rewrite or automatic SQL rollback.

Migration `000045_directory_lookup.sql` adds a tenant/display-name/slug/ID B-tree index for exact Namespace lookup and duplicate-name paging. It changes no tables, retained evidence, RLS policies or runtime grants; manifest v26 remains current. The normal drained schema-owner workflow builds the index transactionally, with the associated write lock, storage and ongoing index-maintenance cost. Do not use concurrent index creation inside this migration runner. Failure rolls back this migration; keep runtimes stopped, fix the reported cause and rerun. There is no automatic SQL rollback. Declared Publisher domains remain filtered in SQL through the existing tenant selection: the runtime's forced-RLS plans do not use an array-containment GIN index.

The separate [semantic schema](semantic/README.md) is opt-in through `catalogctl migrate plan --semantic` and `catalogctl migrate apply --semantic`. Use the selected image's `migrate plan` for current core numbering; core migrations never require pgvector. The exact manifest includes conditional optional-table privileges; installing this schema does not enable semantic runtime search.

Add the next numbered migration for schema changes. Never edit a migration that may have been applied: checksums protect its identity. Runtime privilege manifests are maintained separately in [privileges.go](../internal/platform/database/privileges.go) and must change deliberately with affected schema authority. The local [PostgreSQL bootstrap](../deployments/postgres/README.md) is not this migration system.

Only the dedicated Catalog schema owner applies migrations and reconciles grants. API and worker roles never migrate automatically. Review the plan and documented full-drain boundaries before upgrading; forward migrations do not imply that an older binary or Helm rollback can use the new schema. Unsupported development state fails explicitly without conversion or deletion.

Migration `000028_authoring_history.sql` adds tenant/parent/UUID indexes for bounded version, revision, listing, release and review history. It changes no row format, retained evidence or runtime privilege grant. No backfill or repair is required for this migration. Leave these additive indexes and the migration ledger in place when rolling back a compatible application change.

Migrations `000029_local_search_projections.sql` and `000030_indexed_search_projections.sql` add six local search fields on Resources/ListingRevisions and five indexed ranking vectors on observations. PostgreSQL computes these stored generated columns under table locks without changing authoritative metadata or sealed evidence. These migrations introduced manifest v14. Drain runtimes before applying migrations and reconcile both current manifests before startup. Only the API reads the generated fields; both roles execute the text-array helper for authoring or ingestion, and neither can write generated values directly.

No separate generated-column backfill is needed. The indexed-observation repair required when crossing migration 000027 remains necessary. Repair forward after a failed grant reconciliation; do not restart an older exact-manifest binary against the new schema.

Use `make test-integration` for fresh application, retained checksums, role reconciliation, RLS and direct-SQL lifecycle guards against disposable PostgreSQL. Migration changes also need affected repository tests and an explicit repair or rollback boundary. See [CONTRIBUTING.md](../CONTRIBUTING.md) for the full verification workflow.

Normal releases use the deployment-side [coordinated maintenance workflow](../documentation/src/content/docs/operations/maintenance.mdx#coordinated-release-deployment). Compose and Helm automate the existing owner command after runtime drain; API/worker startup remains migration-free. `plan` validates ledger/checksum identity, whereas `apply` additionally admits both roles before SQL and reconciles exact grants. Earlier committed migrations survive a later failure: repair forward, keeping runtimes stopped. Explicit stopped-runtime data repair remains a separate owner procedure, not an arbitrary command hook in the deployment tool.

Migration `000034_authorization_core.sql` adds immutable built-in permission data, same-tenant assignment references and tenant revision/fencing state under manifest v18. Drain runtimes before apply/reconciliation. It requires complete schema-owner reads and refuses policy-filtered reads or contradictory active namespace gates; investigate rather than resetting or relaxing RLS. Migration 000035 adds administration and 000036 completes replay/bundle cutover. The [authorization test guide](../test/authorization/README.md) covers integrated collection/search and browser enforcement.

Migration `000035_authorization_administration.sql` adds custom-role metadata and exact permission-set guards, same-realm groups/direct members, direct/group scoped assignments and immutable history under manifest v19. Drain runtimes and reconcile both exact manifests before starting matching binaries. API permission deletion is narrowly guarded for active custom roles; built-ins remain immutable and workers gain no administration authority. The [authorization test guide](../test/authorization/README.md) covers integrated enforcement and recovery. Repair forward after failure; no retained-data reset or obsolete-runtime overlap is supported.

Migration `000036_command_replay_authorization.sql` adds bounded original action/scope replay evidence under manifest v20. It refuses every unexpired command reservation and every unfinished unreleased reservation, including expired pending work. Drain runtimes, let completed replay windows expire and use the existing owner repair only for proven interruption before retrying. Previously committed migrations stay applied. It preserves tenant-wide development bundles as explicit assignments without granting tenant authority to scoped assignments; no role-name hierarchy is evaluated at runtime.

Migration `000037_listing_artwork.sql` adds source generation guards, compact observation artwork metadata, and tenant-scoped override heads with immutable revisions. Exact manifest v21 grants API override writes and worker observation metadata writes. Images remain external; the migration does not fetch them or enable managed storage. Follow the normal owner-only maintenance workflow.

Migration `000039_indexed_discovery_filters.sql` adds nullable indexed license/category fields and a separate readiness marker under manifest v23. Strict invoker-only helpers project literal declarations from retained normalized bytes. Both roles can execute the trigger helpers; the API can read the filter values, and neither role can assign them. Workers can read the readiness marker. No source document is rewritten during migration. Drain runtimes, apply and reconcile, then run the existing tenant-scoped search-projection repair until complete for each tenant before restarting. Fresh databases need no repair. The repair runbook owns declaration limits and failure behavior.

Migration `000040_unicode_version_limits.sql` changes the source artwork version constraint from 128 bytes to 128 Unicode code points. It preserves version presence, immutable records, document byte budgets and exact equality. It adds no privileges and retains manifest v23. Use the existing owner-only migration workflow; no data rewrite or search repair is required for this migration.

Migrations `000041_tenant_settings.sql` and `000042_tenant_job_defaults.sql` add bounded tenant overrides, revision guards and the two tenant settings permissions. Exact manifest v25 grants API reads/writes and worker reads. Existing custom roles receive no settings grants. Apply through the normal drained, owner-only migration workflow; startup never applies these migrations.

Migration `000043_personal_library.sql` adds human-owned saved references and search criteria, forced tenant/owner RLS, immutable owner/revision guards, a serialized 200-item quota and exact work-attribution indexing. Manifest v26 grants exact API library columns and owner-bound deletion; workers receive no library access. Required private audit commits with mutations without a public outbox event. Restore check set 6 verifies private-library integrity without emitting labels or query text. Use the same drained, owner-only apply workflow; see the [personal library guide](../documentation/src/content/docs/guides/catalog-ui.mdx#save-resources-and-searches).

Migration `000044_principal_and_group_guards.sql` adds an exact-target audit index for Resource creation/adoption and review submission attribution, and replaces the group guard to require explicit revocation of active assignments and members before archival. It changes no retained attribution, archived history or runtime grants; manifest v26 remains current. Use the normal drained owner-only migration workflow. Missing or ambiguous origin evidence fails closed; do not infer ownership, reset data or reopen archived groups. See the [persistence chapter](../documentation/src/content/docs/architecture/persistence.md#000044---principal-lookups-and-group-archival-guards).

Migration `000047_publisher_domain_verification.sql` removes obsolete approval storage after preserving its complete union, then adds separate DNS evidence. Undeclared legacy values stop the migration. Existing approvals gain no verified status. Manifest v27 grants exact API columns; workers receive no verification-table access. Restore check set 7 includes the new records. Use the drained, owner-only migration procedure and repair forward after failure. Verification remains disabled by default.

Migration `000048_agent_protocol_projection.sql` materializes exact A2A interface bindings for both inline representations and retained document blobs. It preserves content bytes, digests and sealed evidence. Drain API/worker processes and budget for table rewrites before the schema owner applies it and reconciles manifest v28. API alone gains column reads and execution of the pure derivation function; workers gain neither. No semantic reindex is needed. After failure, keep runtimes stopped, fix the reported cause and retry forward through the normal migration command.

Migration `000049_mcp_tool_declarations.sql` adds stored private MCP tool arrays,
lexical GIN indexes and one valid tool declaration per immutable revision.
Drain API/worker and budget for table rewrites before the schema owner applies
it and reconciles manifest v29. API projection/function access is read-only;
workers gain neither. Exact declaration bytes and digests stay unchanged.
Generated columns recompute on insertion and logical restore; index repair uses
normal PostgreSQL `REINDEX`, not observation repair or a semantic reindex.
If migration/reconciliation fails, keep runtimes stopped and retry forward after
correcting the reported cause. Omit `includeMcpTools` to use ordinary discovery;
do not downgrade the schema or rewrite retained declarations.

Migration `000050_feedback_recent_page.sql` adds the tenant/member/state/update/ID
index used by recent own-feedback selection. It changes no records, functions
or runtime grants; manifest v29 is unchanged. Use the normal drained, owner-only
migration workflow. Index creation locks feedback writes during application;
budget maintenance time for the retained table size. Repair forward after
failure; no row conversion, data reset or response-history backfill is needed.

Migration `000051_source_completed_attempt_page.sql` adds the shared current-revision
completed-attempt index for Source directory filters and Overview. It changes no
records, functions or runtime grants; manifest v29 remains unchanged. Apply
through the normal owner-only migration with snapshot writers drained. Index
creation locks snapshot writes; allow maintenance time for retained history.
Repair forward after failure without resetting Source membership or history.

Migration `000052_shared_collections.sql` adds namespace-owned shared collections and same-tenant Listing references, with forced RLS and ownership/revision/member guards. Manifest v30 grants API-only access. It extends the finite scoped permission vocabulary and selected retained/new built-ins; custom roles are unchanged. Drain runtimes, apply as schema owner, reconcile grants and restart matching binaries. Restore check set 8 includes collection integrity. Failure requires forward repair with runtimes stopped; no retained-data reset or automatic rollback is provided.

Migration `000054_native_capability_projection.sql` adds generated private card declarations on representations and document blobs. Drain API and worker processes and budget for both stored-column rewrites before the schema owner applies it and reconciles manifest v31. The API alone reads the columns and executes the bounded pure extractor; workers gain neither privilege. Authoritative card bytes, digests, revisions and public snapshots remain unchanged. An unavailable projection never provides partial matches. No semantic reindex or external-observation repair is needed. Logical restore recomputes the generated values from retained content; use the current migration binary and exact privilege reconciliation before read-only restore verification and representative Search/Explore checks. If migration fails, keep runtimes stopped, resolve its cause and retry forward through the maintained migration command.

Migration `000055_personal_listing_follows.sql` extends the private library input constraint and adds owner/Listing uniqueness. Existing personal items and runtime grants are unchanged.
