---
title: "Incident response runbook"
---

## Triage

1. Record the service version, commit and profile bundle digest. Identify the affected tenant, request ID, first observed time and scope. Do not copy sensitive payloads.
2. Check `/readyz`, safe structured logs and HTTP error rate. Inspect Job, delivery, source and publication snapshot states. Check PostgreSQL, the identity provider and enabled webhook component degradation.

   For enabled hybrid search, inspect last-observed provider state, semantic Jobs and fixed fallback metrics. Provider outages can safely degrade retrieval while PostgreSQL-backed readiness remains healthy. Follow [hybrid-search recovery](hybrid-search.mdx#indexing-and-recovery). Managed-media and scanner adapters remain unavailable.
3. Determine whether the incident affects authoring, discovery, public publication, federation, artifacts, worker processing, or tenant isolation.
4. Preserve audit, outbox, job, delivery, source-run, and snapshot evidence before intervention.

## Containment

- Suspected tenant isolation, token validation, secret exposure, artifact integrity, signature, or public-host confusion requires urgent containment review. The deployment owner assigns incident severity, escalation and on-call routing. Remove affected ingress, stop workers if they could expand impact, revoke credentials, and preserve evidence.
- For a compromised federation source, pause that exact source first so future synchronization and live use stop without deleting retained evidence. Before removal, request the bounded consequence preview and submit its token, the current source ETag, and a concise reason to the removal command. The service rejects a missing, expired, cross-tenant, stale, wrong-source, or wrong-intent preview and preserves observations needed for analysis.
- For unsafe public content, revoke the affected listing release, build and activate a clean snapshot, or unpublish the exact configured discovery host. Verify both well-known documents and Registry results after the change.
- For a compromised webhook signing secret, use the endpoint rotation command with `{"revokePrevious":true}` and have the receiver reject both prior signers. Routine rotation deliberately overlaps keys for 15 minutes and is not emergency revocation. Disable the endpoint or isolate the receiver if delivery integrity is uncertain. An in-flight request cannot be recalled. A compromised deployment encryption key needs the separate stopped-runtime [key replacement procedure](webhooks.mdx#deployment-encryption-key-replacement).
- For a poisoned or unavailable worker dependency, stop workers while leaving safe reads available. Do not bypass validation or mark work successful manually.

## Recovery

Recover with typed API commands or a reviewed forward repair. Use the [idempotency repair runbook](idempotency-repair.mdx) only after the original execution has ended and its protocol-v2 plan proves interruption. After a database writer change, fence the old writer first. Advisory locks are not replicated.

A free lock on the new writer alone is insufficient. Unsupported command formats are refused, not converted. Do not edit lifecycle, audit, outbox, job, delivery, or snapshot tables by hand unless an approved repair runbook explicitly covers the case. Re-run smoke, tenant-denial and affected integration tests only in a disposable environment created for that test run.

:::caution[Smoke changes Catalog state]
Smoke creates governed records and activates a snapshot. It is not a read-only production probe.
:::

Verify the repaired or restored deployment through profile checks and authorized read-only state inspection before restoring traffic. Any mutating validation against shared or production data requires explicit operator authorization and its own reviewed scope.

## Evidence and follow-up

Document the timeline, customer effect, violated invariant, root cause, containment, recovery, and durable prevention. Add a regression test for the exact failure mode where practical. Rotate exposed material. Review adjacent tenants and surfaces. Assign an owner and due date to each incomplete follow-up.
