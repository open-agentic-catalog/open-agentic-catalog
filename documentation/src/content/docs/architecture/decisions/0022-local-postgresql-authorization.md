---
title: "ADR-0022: Scoped PostgreSQL authorization"
---

- Status: Accepted
- Related decisions: ADR-0006, ADR-0009, ADR-0013, ADR-0017, ADR-0018

## Question

How can administrators define useful roles and teams, and give users namespace-private access, without adding another authorization service or weakening Catalog transactions and tenant isolation?

## Decision and implementation boundary

Keep authorization in the Catalog and its existing PostgreSQL database. Use a finite application-owned permission vocabulary, configurable tenant-owned roles, direct-member groups and tenant or exact-namespace assignments. Authentication, Catalog membership, optional OAuth ceilings, domain eligibility and tenant isolation remain independent checks. PostgreSQL database login roles remain the existing API, worker and schema-owner identities; do not create a database login per Catalog user.

The model covers administration, domain enforcement, filtered collections, fenced replay, compact authentication and bounded browser administration. The [identity and security guide](../security.mdx#role-and-group-administration) describes runnable behavior. Authorization adds no setting or deployment dependency.

### Finite permissions and usable role templates

The finite permission catalog includes tenant settings from [ADR-0025](0025-tenant-operational-settings.md) and the personal library action `library.own`. Unknown identifiers deny; custom roles cannot invent actions, wildcards or executable policies. Permissions are selected explicitly rather than inferred from HTTP route text. The repository's frozen operation inventory accounts for every Control API operation and domain service entry point; tests reject unreviewed additions. Individual permissions remain distinct from owning a resource, satisfying validation or being eligible for a transition.

| Applicable assignment scope | Permission families |
| --- | --- |
| Tenant or exact namespace | Resource metadata/history/content reads; create/edit/draft deletion/validation; review submission/decision; release publication/transition; namespace read/manage; own ratings for visible targets. |
| Tenant only | Namespace creation; publisher read/create/manage/domain approval; federation discovery; source read/inspect/manage/sync/evidence; snapshot read/generate/activate/rollback/unpublish; authenticated discovery-host read; Job read/cancel/retry; audit read/export; webhook read/manage/test/rotate/evidence; own feedback/moderation; own saved references and named searches; rating moderation and sharing management; tenant settings read/manage; semantic reindex. |
| Protected tenant assignment only | Access administration, membership lifecycle and the direct built-in Tenant Administrator. Custom roles and groups cannot supply this authority. |

A usable role must explicitly include its required companion read permissions. Permission evaluation does not add them dynamically. For example, editing requires metadata/history/content reads; review and publication require access to the evidence they consume. A role set lacking required companion permissions is rejected with named missing permissions. Authorization across different operation scopes, such as source evidence plus destination-namespace creation for adoption, may be satisfied by separate assignments; it does not require one role containing both.

[Human resource ratings](0023-human-resource-ratings.md) use explicit permissions in this vocabulary. Scoring still requires current target visibility and a human identity. Own withdrawal remains available after target access or `ratings.own` is lost.

Reader, Contributor, Reviewer and Publisher templates contain only namespace-capable permissions so they remain assignable at both scopes. They include the reviewed reads required by their workflows. Permission checks use those explicit reads, not implied authority from a role name.

Separate tenant-only Discovery reader, Publisher creator and Publication administrator templates supply their respective workflows. Source administrator, Security reviewer, Webhook administrator, Feedback member and Library member remain explicit tenant templates.

Tenant Administrator contains the reviewed finite vocabulary and cannot be edited or assigned through a group. The `service_client` template is inert and supplies no permission; human/service classification remains membership data. Reserved keys distinguish built-ins from custom display names.

An ordinary grant confers exactly the selected template. It does not add companion templates. Only explicit development-seed or bootstrap bundles add separate companion assignments.

A role containing any tenant-only permission cannot be assigned to a namespace. Adding such a permission to a role already assigned at namespace scope refuses atomically; it never elevates, truncates or silently revokes assignments. Permission-set changes have paginated impact views. A preview does not authorize commit.

### Scope, groups and namespace membership

Effective access is the union of active direct and group assignments, current role permissions, active realm and active membership. Removing one assignment path does not remove another valid path. Roles are reusable definitions.

Sharing a role does not share assignees across namespaces. Groups have direct members only, belong to one identity realm and cannot include memberships from another realm. Ordinary grant/group administration preserves the existing administrator-realm boundary.

Last-administrator protection counts usable active direct administrator memberships, not group size or assignment rows.

Use scoped assignments as the sole namespace authorization relation. Existing `namespace_memberships` is maintained automatically with role grants/revocation and has no independent product administration workflow. Do not materialize that relation for group members.

The namespace-membership relation grants no permission by itself. Current scoped assignments and active identity gates determine access.

Suspension preserves eligible grants for reactivation. Terminal offboarding revokes direct assignments and group membership relations. Operator realm shutdown remains a supported separately authorized action.

No individual-resource ACL, namespace tree, nested group, role inheritance, deny policy, time condition, SCIM or automatic identity-provider group mapping is selected. Discovery grants do not authorize resource execution, installation or runtime provisioning.

### Protected reads and collections

Exact checks use indexed existence queries against current authority. Collection predicates use the same semantics before ordering, limits, totals, facets and hydration. Duplicate direct/group paths do not duplicate rows or counts. Namespace-limited readers receive only permitted local records and histories. Content addressed by artifact ID or digest requires an authorized retained reference; shared bytes do not disclose other references.

Read permission and protected data use the same statement or bounded transaction snapshot. Multi-statement counts, pages and hydration use a consistent snapshot. Search reuses its existing Repeatable Read transaction.

A read admitted before concurrent revocation may finish. A new snapshot after revocation commits cannot use the removed permission. Never hold a transaction across external fetches or slow response streaming.

Audited private reads use a bounded write-capable evidence transaction with shared admission, and release protected output only after evidence commits.

Filter options and namespace directory pages disclose only authorized choices. Necessary compact publisher/namespace labels on an authorized resource do not grant directory/evidence access. Lexical/hybrid candidates share the predicate; access edits do not regenerate embeddings. Hidden local records cannot suppress authorized external matches or leak through conflict/provenance grouping.

Federated records have no implicit local namespace. External indexed/live/referral discovery requires the explicit tenant federation-discovery permission. Check topology, source/audience policy and query disclosure before outbound contact.

Public discovery still reads active immutable snapshots: revoking private access does not unpublish intentionally public data. Authenticated discovery-host mode requires tenant `discovery.read`, active tenant membership and any configured OAuth ceiling.

Client and provisioning tests cover assignments and current admission.

Jobs require their read/cancel/retry permission plus the trusted kind/target authority. Reading source-sync Jobs requires `source.read`; cancel/retry requires `source.manage`, and enqueue requires `source.sync`. Publication Jobs require `publication.read` for reads and `publication.generate` for commands.

Semantic reindex Jobs require `operations.reindex`. Unknown/deleted targets refuse safely. Resource-read permission does not grant access to every operational Job.

Do not introduce a generic Job-management bypass. Metadata-only read permissions for new operational Job families require their own reviewed contract.

Own feedback uses active membership, tenant `feedback.own` and ownership; creating feedback also requires current target read access and existing human constraints. Own submission/list/edit/withdraw require human identity; exact own reads and moderation preserve the existing human/service policy. Losing target visibility does not reveal its current data or erase the submitter's own private submission. Moderator reads/commands use tenant `feedback.moderate`; command replay retains the existing text-free receipts. Audit/export, source raw evidence and webhook delivery evidence remain explicit privileged surfaces, not arbitrary object-filtered documents.

### Write admission and revocation

Use one tenant authorization-state row with a monotonic revision. A protected interactive write first takes [`FOR SHARE`](https://www.postgresql.org/docs/18/explicit-locking.html). It then evaluates current identity, permission and canonical target scope in a subsequent Read Committed statement. Preflight checks outside the transaction are advisory for final admission, but still required before unauthorized outbound disclosure. Hold shared admission through domain mutation, mandatory audit, independently classified outbox and retained response completion.

An authority-changing operation takes `FOR NO KEY UPDATE` from the outset, then checks current administrator authority and changes policy plus revision atomically. Never upgrade a previously acquired shared lock. All effective-authority writers participate: role permission changes, grants/revokes, group state/membership, membership lifecycle, realm lifecycle, provisioning and built-in vocabulary migration. Cosmetic name changes do not advance authorization revision. Failed audit/state/response work rolls back the authority change.

If a write obtains admission first, it may commit before revocation and the revocation waits within existing budgets. If revocation commits first, the waiting write must reread authority and refuse. Shared admissions coexist. Other tenants use other rows. `FOR KEY SHARE` is insufficient because it permits a non-key revision update. Do not substitute Serializable isolation for a reviewed protocol or introduce a distributed lock service.

The acquisition order is command-key session advisory lock where applicable, authorization row admission, existing tenant/domain advisory locks, then deterministically ordered object locks. Non-HTTP writes join equivalent repository/unit-of-work admission. Preparatory transactions must release their locks before later admission. Bootstrap of a new tenant initializes the coordination row in the same owner transaction; repeat initialization never resets revision. Existing-tenant operator changes follow normal admission order. Missing state is unavailable/fail-closed, never permission to create it through a runtime API connection.

Evaluate permissions in a subsequent statement because a combined lock-and-permission statement can retain a stale snapshot after waiting. Verify both race orders, rollback, cancellation, bootstrap and lock ordering against PostgreSQL. Measure shared-lock bookkeeping and revocation waits before making capacity claims. No long-lived allow cache is selected.

### Replay and continuations

A tenant authorization revision is a coarse invalidation mechanism. Bind decisions/cursors to tenant, realm, stable membership, verified OAuth ceiling and a versioned permission-semantics identifier. Unrelated tenant access changes can invalidate continuation; fresh requests still work. No fine-grained invalidation graph or build-SHA binding is selected.

A command identity remains unique independently of mutable authorization. Use stable Catalog membership plus tenant/realm binding rather than bare subject for new command identity. A revision change triggers fresh replay authorization, not another reservation or permanent rejection solely because a digest changed. Before disclosing any retained success or private error, verify current active identity, original operation permission, trusted target/scope and result-disclosure requirements. This includes local completed entries and waiters awakened after a long command.

Retain a bounded server-owned replay description where target/ownership/result requirements cannot be derived safely from existing retained state. Creation/deletion and one-time-secret responses need explicit cases. Still-authorized retries return exact saved responses without another mutation or renewed lifecycle/If-Match evaluation.

Denied callers retain the completed command but cannot read its private response. Unknown replay context fails safely; never trust a retry-body scope or retry with a new key automatically. Each supported command family has a reviewed replay contract.

Safe refusal of unknown context does not substitute for qualification of supported families.

### Internal execution and deployment

Workers use typed internal execution authority bound to admitted Job kind, tenant, target and lease. A caller's service classification, claimed tenant or worker-like name does not create internal authority. Preserve initiating attribution separately. Accepted durable work and committed notifications may complete after initiator revocation; new user retry/cancel/activation commands need current authority. Snapshot generation cannot activate itself. Housekeeping remains the existing bounded worker/database boundary.

Mutable roles/groups/assignments are durable data, not per-replica environment/Helm policy. Development seeding is explicit and repeat-safe. Production bootstrap remains operator-owned; runtime processes never migrate or recover remembered grants on database failure. Reuse existing operational limits and configuration delivery only for genuinely added bounds. No new sidecar, policy service, cache, activation queue, port or authorization-specific health dependency is needed.

### Migration and recovery

Keep applied migrations and retained audit immutable. Use the current-format boundary in [ADR-0017](0017-first-release-baseline.md), without a data reset or parallel authorization engine. Drain runtimes before a protocol-changing migration. Resolve incompatible pending commands through reviewed completion, repair or expiry before proceeding. Do not purge reservations, alias command keys or infer realm membership from a colliding subject.

A migration must preserve valid assignment identity, attribution and actor type. It must refuse ambiguous authority rather than revive access. Existing assignments remain explicit; ordinary grants do not gain companion templates through runtime inference. Migration and restore procedures require separately selected operator authority.

A backup restores authorization as well as content and can revive grants revoked after that backup. Review those revocations before reopening access. Replace the API processes to clear replay coordination state retained in memory from before the restore. Recovery remains PostgreSQL/operator-owned, with existing exact migration/role/RLS and restore verification.

## Alternatives

Retaining fixed roles is suitable when their bundles and tenant-wide read model meet deployment needs, but does not supply configurable teams or namespace-private browsing. A separate relationship-policy engine adds another authority and synchronization/recovery boundary without a selected relationship requirement. A generic embedded policy language adds expressive combinations and evaluation risks beyond the two supported scopes. This design uses relational joins and ordinary transactions already owned by the application.

## Verification and consequences

Preserve isolated unit tests for permission/scope/set validation and capability projection. Real PostgreSQL integration tests own joins, RLS, exact grants, snapshots, lock ordering, rollback, migrations and replay. Owned process/browser tests prove replicas, current access changes and namespace-only journeys; independent expected populations verify counts/facets and hidden-record cases. Reuse existing lint, API/client, security, conformance, coverage and performance gates instead of creating another environment or test framework.

This affects identity/domain checks, repository selection, command replay, workers, OpenAPI and UI hints together. Incomplete combinations are not supported deployments. The code, contracts and current-behavior guides implement this boundary. The [authorization test guide](../../../../../../test/authorization/README.md) describes replica, browser, migration, recovery and cost tests. Those results do not establish untested deployment capacity or authorize a retained-state operation.
