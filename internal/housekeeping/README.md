# Bounded housekeeping

[housekeeping.go](housekeeping.go) defines cleanup classes and schedules fair, bounded sweeps over disposable operational state. The worker supplies a store, retention periods and observer; PostgreSQL owns exact row eligibility, ordering, transaction boundaries and locking.

A sweep reports fixed per-class progress and failures. Cleanup must preserve authoritative history and independently committed audit evidence. Add a cleanup class only with its corresponding bounded repository operation and retention contract.

Run `go test ./internal/housekeeping -shuffle=on -count=1`. Database integration coverage lives in [platform/database](../platform/database/README.md).

The runner's isolated tests use `testing/synctest`. They verify the immediate seven-class cycle, no early work and the next cycle at the exact interval. Scheduled failures preserve error identity and allow every class to run. Idle cancellation returns `context.Canceled`, joins the runner and prevents later work. Completion and cleanup have virtual-time bounds; these tests use no database or host sleeps.
