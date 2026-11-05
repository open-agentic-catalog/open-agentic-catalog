# UUIDv7 generation

[id.go](id.go) combines a caller-supplied millisecond timestamp with entropy to produce lowercase UUIDv7 strings. `Generator.Reader` permits deterministic test input; a nil reader uses `crypto/rand.Reader`. Entropy failure and timestamps outside the unsigned 48-bit range return errors.

Identifiers carry timestamp ordering across milliseconds but do not provide a process-global monotonic sequence within one millisecond. Keep domain time injection separate from entropy injection. Run `go test ./internal/platform/id -shuffle=on -count=1` for format, version, variant, time-range and entropy checks.
