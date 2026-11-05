# Infrastructure adapters and composition

This directory implements the infrastructure side of the [domain contracts](../README.md). Domain behavior remains in its owning package; provider and transport details stay here.

| Package | Responsibility |
| --- | --- |
| [runtime/](runtime) | Composition root, role-aware dependency wiring and repository architecture/configuration/documentation contract tests. |
| [database/](database/README.md) | PostgreSQL repositories, migrations, exact privileges, RLS, command transactions and operator repair/restore checks. |
| [httpserver/](httpserver) | Product routing, middleware, authentication handoff, strict request decoding and public/control response encoding. |
| [operations/](operations) | Private worker health, status, version and metrics listener; no product API. |
| [authn/](authn) | Development identities and production OIDC JWT verification, including issuer-specific trust. |
| [buildinfo/](buildinfo/) | Embedded bounded version, full commit and source-state projection; no runtime Git access. |
| [catalogmcp/](catalogmcp/) | Read-only Control MCP tools, stdio and finite stateless HTTP, bounded metadata and protocol lifecycle; no resource execution. |
| [cliauth/](cliauth/) | Explicit human CLI OIDC/PKCE login, private origin-bound credentials and serialized refresh. |
| [controlclient/](controlclient/) | Origin-bound CLI HTTP transport, explicit credentials, safe envelopes and selected Search inputs. |
| [config/](config) | Strict typed JSON, explicit environment overrides, scoped policies and safe reports. |
| [fetcher/](fetcher) | Shared outbound URL, DNS/IP, redirect, size, timeout and credential-routing controls. |
| [demoarchive/](demoarchive/) | Exact prepared development-only TLS archive origin, paths, digests, sizes and trust admission; no ordinary fetch-policy exception. |
| [demodata/](demodata/) | Sample YAML validation, native assets, deterministic archive preparation and same-source embedded fixtures. |
| [embeddings/](embeddings/README.md) | Fixed-endpoint inference, query/batch bounds, vector validation and provider-state reporting; no ranking policy. |
| [webhooksender/](webhooksender) | HTTPS webhook transport through the hardened fetcher. |
| [idempotency/](idempotency) | Replay and reservation contracts plus process-local memory implementation; PostgreSQL atomic execution lives in `database/`. |
| [observability/](observability) | Bounded low-cardinality metrics and runtime/component projections. |
| [processinfo/](processinfo/) | One in-memory execution identity and monotonic startup/uptime state; no history or node registry. |
| [diagnostics/](diagnostics/README.md) | Reviewed messages, safe structured logging and bounded diagnostic output. |
| [tracing/](tracing) | Optional process-owned OpenTelemetry SDK, sampling, private causality and hardened OTLP/HTTP export. |
| [trustevidence/](trustevidence/README.md) | Credential-free ANS evidence acquisition through the hardened fetcher, with startup authority policies and fixed work bounds. |
| [clock/](clock) and [id/](id) | Injectable time and UUIDv7 generation. |

External content must use the hardened fetcher. OIDC discovery/JWKS use the dedicated verifier against the configured issuer, not tenant-selected source URLs. Product metrics, worker operations and optional downstream failure must preserve the role-specific health contract.

See [AGENTS.md](../../AGENTS.md) for package boundaries, the [API contract](../../api/README.md) for route changes and the [configuration reference](../../documentation/src/content/docs/reference/configuration.mdx) for deployment inputs. `make architecture-check` and focused package tests catch dependency and behavior drift.
