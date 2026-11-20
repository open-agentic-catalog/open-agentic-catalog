# Private human library

The [personal CLI](../../cmd/catalogctl/personal.go) exports observed owner pages and restores selected same-origin/tenant intent through these existing HTTP save operations. Its private file codec and local progress belong to the client; this package retains normal authority, quota, target admission and audit ownership. See [backup and restore](../../documentation/src/content/docs/reference/commands.md#back-up-and-restore-your-private-library).

This package owns saved labels, exact resource references, stable local Listing follows and validated Catalog search criteria. PostgreSQL owns durable records. The memory store implements the same owner and transition boundary for local development and tests.

The current human's tenant, membership and realm determine ownership. Callers cannot select another owner, including administrators. `library.own` is independent of resource permissions. The HTTP layer adds `catalog.read` or `personal.write` scope ceilings, idempotency and revision preconditions.

Changed references pass through `Authority.Admit`. Renaming or removing an unchanged reference does not require continued target access. Saving and reading criteria never executes Search. Source availability and exact current projection checks remain in their owning services. Library storage contains no resource documents, credentials or arbitrary destinations.

A local Listing follow stores a private stable Listing ID and an optional last-acknowledged release ID and disposition revision. A current check requires `resource.read` in the Listing namespace. Owner reads and backups omit the acknowledgement. A restored follow starts unacknowledged. A duplicate returns the existing preference, including under concurrent creates.

Each owner has at most 200 items. Labels contain 1-120 Unicode code points without controls. Command input and retained PostgreSQL JSON each have a 16 KiB limit. PostgreSQL serializes owner quota decisions under the retained human membership row lock; a trigger independently checks the quota. Forced tenant and owner RLS plus explicit owner predicates protect reads and writes. Workers have no library grant.

Listing optionally intersects type with an exact label before ascending-ID pagination. A nil `ListRequest.Label` omits that filter; a supplied empty or invalid label is refused. Both stores preserve case, surrounding spaces, Unicode spelling and literal wildcard characters. Duplicate labels remain separate items. PostgreSQL uses deterministic `C` collation for this equality. The HTTP continuation also binds type, label presence/value and current owner authority; changing a filter requires a fresh first page.

Each mutation commits its required audit record with the item change. Audit facts contain the item revision, never its label or criteria. Command receipts contain only ID and revision. Private preferences do not create public outbox records. Removal deletes the selected item; audit, command and backup retention remain separate.

My work belongs to `internal/catalog`: it projects current exact pointers and immutable submission state using exact membership/realm transition attribution. It is not a second work-status store or an audit-history browser.

Use `go test -race ./internal/personal` for isolated domain tests. HTTP contracts use the `functional` tests in `internal/platform/httpserver/personal_functional_test.go`. The PostgreSQL suites for [library ownership and quotas](../platform/database/personal_integration_test.go) and [follow admission and concurrent updates](../platform/database/follow_integration_test.go) use an owned disposable database. See the [UI guide](../../documentation/src/content/docs/guides/catalog-ui.mdx#my-work) for supported workflows and attribution limits.

Domain units verify defensive copies of every saved-criteria slice through input, save results, exact reads and filtered pages. Caller edits cannot change retained state. These units use memory stores and injected dependencies; they need no application executable, listener or database. Reading criteria performs no resource admission and creates no mutation audit record.
