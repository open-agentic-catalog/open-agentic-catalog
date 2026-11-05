# Identity and Catalog authorization

Identity maps authenticated principals to compact active tenant memberships and evaluates current finite permission assignments at tenant/exact-namespace scope. Custom roles and direct-member groups are retained Catalog policy data. Authentication happens in the platform verifier; upstream groups and OAuth scopes do not directly confer a Catalog role.

| File | Responsibility |
| --- | --- |
| [identity.go](identity.go) | Actors, tenant validation, context and stable identity/permission bindings; actor role arrays are not runtime authority. |
| [access.go](access.go) | Membership and fixed-template assignment records, persistence contracts and principal resolution. |
| [service.go](service.go) | Protected membership changes and attributed evidence. |
| [membership_cursor.go](membership_cursor.go) | Signed continuation tokens bound to effective authorization and the selected page limit. |
| [memory_store.go](memory_store.go) | In-memory memberships and guarded administrator transitions. |
| [directory.go](directory.go) | Exact directory filters and continuation bindings; no page-local filtering. |
| [member_access.go](member_access.go) | Protected bounded member contributions, current-realm group coverage and retained lifecycle eligibility. |
| [development_seed.go](development_seed.go) | Pure principal validation and deterministic alias unions, with explicit bootstrap companions and exact namespace grants. |
| [self_permissions.go](self_permissions.go) | Bounded current tenant/namespace permission hints from retained policy. |
| [permissions.go](permissions.go) and [enforcement.go](enforcement.go) | Finite descriptors/templates, current action/scope checks and admission lifetime. |
| [administration.go](administration.go) | Custom roles, direct-member groups, scoped assignments, bounded impact/self pages and lifecycle. |
| [collection.go](collection.go) | Coherent authorized collection population and continuation revision. |
| [overview.go](overview.go) | Protected membership, active definition and retained assignment counts; Group counts stay in the caller's realm. |

Preserve tenant isolation and the remaining active administrator when changing memberships. Signed continuation cursors bind current policy revision so they cannot retain access after an authorization change. Authority writers advance revisions atomically with required evidence; commands and private replay recheck current policy under admission. Browser-visible permissions supplement server authorization. Membership cursor encoding preserves valid subjects up to 512 bytes within the 2,048-byte token limit; payloads use base64 transport without unnecessary HTML escaping.

Run `go test ./internal/identity -shuffle=on -count=1`.

Effective self-assignment pages can include the current caption of an eligible group path. They disclose no other members or unrelated groups and require no administration grant. Captions are presentation separate from retained assignment attribution; direct paths omit them.
