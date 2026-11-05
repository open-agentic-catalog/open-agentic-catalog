# Audit records and exports

This package defines the bounded audit format shared by domain services and persistence adapters. It has no Catalog domain dependency, so callers can construct validated records before committing the owning transition and evidence atomically.

| File | Responsibility |
| --- | --- |
| [types.go](types.go) | Record attribution, export envelopes and size limits. |
| [context.go](context.go) | Request, command and worker-attempt attribution carried in context. |
| [record.go](record.go) | Closed action/fact vocabulary, reason validation and deterministic record encoding. |
| [webhook.go](webhook.go) | Shared required facts for webhook endpoint changes. |
| [export.go](export.go) | Bounded exports, digest verification and recovery-point manifest binding. |

The deterministic encoding is service-owned and is not RFC 8785. Digest agreement verifies retained bytes and envelope consistency; a trusted recovery-point binding must come from the operator's separately retained manifest. Keep secrets, raw requests and private documents out of action facts.

Run `go test ./internal/audit -shuffle=on -count=1`. The [audit runbook](../../documentation/src/content/docs/operations/audit.md) contains the operator procedures.
