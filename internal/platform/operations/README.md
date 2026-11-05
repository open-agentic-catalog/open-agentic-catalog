# Worker operations listener

[server.go](server.go) exposes worker startup, liveness, readiness, status, version and metrics handlers. It owns no network listener; [catalogd](../../../cmd/catalogd/README.md) starts and drains it separately from the product API. Keep its port off the product Service and Ingress.

Liveness reads process-local state. Readiness requires the running lifecycle and a bounded PostgreSQL check. Status and metrics can include optional delivery degradation without turning receiver failures into liveness failures. Query strings and escaped path aliases are rejected because this surface has no parameterized routes.

Run `go test ./internal/platform/operations -shuffle=on -count=1` for health-state, dependency, routing, response and metric contracts. The [observability runbook](../../../documentation/src/content/docs/operations/observability.mdx) is the reader authority for this separate listener; Control OpenAPI describes the product API.
