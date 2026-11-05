---
title: "ADR-0025: Tenant operational settings"
---

- Status: Accepted

## Context

Administrators need to pause automatic source synchronization and adjust selected work budgets without replacing API or worker processes. These choices belong to one tenant. Database access, credentials, network policy and process settings remain deployment inputs.

## Decision

Store three optional overrides in one typed tenant record: automatic source synchronization, its interval and the maximum attempts for new Jobs. PostgreSQL owns retained policy. The single-process development mode uses an equivalent memory store. No reload service, background cache or configuration file editor is added.

Omitted fields inherit the consuming process's validated deployment defaults. Explicit false pauses new automatic source admissions. An empty overrides object resets all fields to inheritance; it preserves the saved row and advances its revision. The API reports its own defaults, effective values and saved overrides. Operators must keep shared API and worker defaults consistent.

Expose private `GET` and `POST /api/v1/tenant-settings`. Require tenant-wide `settings.read` or `settings.manage`, with `tenant.admin` when OAuth ceilings are enforced. Management also requires read permission. Only the Tenant Administrator template receives these permissions automatically; custom roles may delegate them explicitly. Namespace grants do not confer tenant settings authority.

Require an exact ETag, idempotency key and reason for every save, including the first save and reset. The opaque ETag binds the tenant, saved revision, overrides and responding API defaults. A change to the API's deployment defaults invalidates an earlier ETag. Saves replace the complete overrides object and commit required audit evidence atomically. Audit facts record before/after values and presence flags; no public notification is emitted.

Serialize saves with a tenant-scoped exclusive transaction advisory lock. New admissions take its shared counterpart before existing maintenance or source locks. Read policy in a separate read-committed statement after the lock. This protects absent rows and observes a save that finished while admission waited. Workers can read settings but cannot edit them. Database failures stop admission; they never imply inheritance.

Apply attempt overrides only to new work that requests the default. Preserve explicit internal limits, deduplicated Jobs and retry-child budgets. Finalize request audit evidence against the actual stored Job. Preserve existing retry scheduling, cancellation and lease rules. Automatic source selection filters paused tenants before its SQL limit and rechecks eligibility during admission. Existing source anchors, jitter, capacity and last-complete snapshots remain authoritative.

## Consequences

A committed pause blocks subsequent automatic admissions. Already admitted Jobs continue, and manual synchronization and live discovery remain available. Settings are not an emergency outbound network switch.

The browser uses the same API. It distinguishes inheritance from saved values, retains drafts after conflicts and retries uncertain commands with the exact captured input. Related administrative controls keep their own permissions.

The feature does not make deployment configuration writable. Authentication, secret delivery, provider/model selection, outbound policy, database pools, logging, leases, concurrency, retention and capability activation still require their documented operator workflows.

## Verification

Use isolated domain and repository units, actual HTTP command tests and exact-role PostgreSQL integration tests. Assert authorization, stale validators, audit rollback, lock ordering, source pause and unchanged retained Job budgets. Browser tests cover editing, reset and conflict recovery. Process fixtures exercise two APIs and worker consumers against shared PostgreSQL. Packaged browser and backup/restore fixtures separately check exact command recovery and retained policy.
