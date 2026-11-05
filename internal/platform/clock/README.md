# Injectable time

[clock.go](clock.go) defines the small clock contract used by time-dependent services. `Real` returns system time in UTC. `Fake` retains a caller-selected instant and advances under a mutex, including negative advances for rollback tests.

Inject a clock when testing leases, expiry or retention. Advancing a fake changes only its reported time; it does not trigger timers or worker loops. Run `go test ./internal/platform/clock -shuffle=on -count=1` from the repository root.
