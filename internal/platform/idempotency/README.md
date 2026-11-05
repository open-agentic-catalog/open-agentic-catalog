# Command reservation and replay contracts

[store.go](store.go) defines caller/tenant/route-scoped reservations, exact retained HTTP responses, atomic execution and repair exclusion. Its `MemoryStore` supports process-local development. The [PostgreSQL adapter](../database/README.md) implements durable command exclusion and transactionally commits mutations, evidence and the replay response.

An atomic execution callback must pass its supplied context to every repository call so writes join the command transaction. Pending and replayed commands do not execute the callback. The HTTP layer must recheck current authentication, authorization and disclosure before serving retained responses; a matching key is not authority.

Abort hooks mark an active atomic transaction rollback-only after handler panic or encoding failure. Request digests bind the payload and concurrency precondition to the reserved command. Run `go test ./internal/platform/idempotency -shuffle=on -count=1` for memory-store and digest contracts; database integration tests separately verify durable concurrency and atomicity.
