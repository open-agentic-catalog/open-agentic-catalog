# Service implementation

The Catalog is a modular Go monolith. These packages implement domain behavior and its infrastructure adapters; they are not separate services or a public Go SDK. [DESIGN.md](../DESIGN.md) defines invariants, while [application context](../CONTEXT.md#implemented-features-and-defaults) distinguishes available behavior from future capabilities.

| Package | Ownership |
| --- | --- |
| [feedback/](feedback/README.md) | Private human item feedback, administrator responses and safe current-text retention. |
| [artwork/](artwork/README.md) | Strict artwork descriptors, bounded raster validation and transient delivery through a narrow fetch port; no managed storage. |
| [catalog/](catalog) | Canonical records, lifecycle, search contracts, repository ports, typed errors, identifiers and digests. |
| [identity/](identity) | Compact principals, finite permissions, role templates, custom roles, groups, scoped assignments and current authorization context. |
| [personal/](personal/README.md) | Private saved references/searches and owner-only stores; no authority over saved targets. |
| [ratings/](ratings/) | Human version-specific opinions, consent, aggregates, moderation and optional publisher summaries. |
| [tenantsettings/](tenantsettings/) | Typed tenant operational overrides, inheritance, exact validation and audited policy. |
| [safeerr/](safeerr/README.md) | Reviewed diagnostic messages, fixed operation stages and fatal owned-panic classification; no infrastructure dependencies. |
| [adapter/](adapter) | Native Agent, MCP Server, Skill and Plugin representation validation, including bounded package inspection. |
| [standards/](standards) | Pinned JSON Schema, JSON-LD, ARD and AI Catalog behavior. |
| [federation/](federation) | Inspection, onboarding, synchronization, observations and bounded live discovery. |
| [publication/](publication) | Candidate generation, activation, rollback, withdrawal and immutable public snapshots. |
| [jobs/](jobs) | Durable work contracts, leases, retries, cancellation and terminal states. |
| [housekeeping/](housekeeping) | Bounded worker cadence and class-specific cleanup contracts. |
| [audit/](audit) | Attributed records, closed action facts, bounded export encoding and verification. |
| [events/](events) | Public notification vocabulary, reference payloads and CloudEvents projection. |
| [webhook/](webhook) | Optional endpoint lifecycle, encryption, signing, delivery and reconciliation. |
| [jsondoc/](jsondoc) | Duplicate-key-safe JSON validation at untrusted document boundaries. |
| [a2apb/](a2apb) | Generated bindings for the pinned A2A protocol; regeneration inputs are documented in [profiles](../schemas/PROFILES.md). Do not hand-edit the generated binding. |
| [platform/](platform/README.md) | Infrastructure adapters and runtime composition. |
| [integration/](integration) | In-process end-to-end workflows across the composed service. |

Domain packages must not import SQL implementations, HTTP handlers, deployment providers or SDKs. Add a narrow domain port only for a real caller and implementation. Keep tenant isolation, immutable history, atomic evidence and safe outbound access intact; discovery never authorizes execution.

Tests are colocated with their owning code. Use focused package tests while iterating, `make architecture-check` for dependency boundaries and the [contribution workflow](../CONTRIBUTING.md) for complete verification. External harnesses are indexed in [test/](../test/README.md).
