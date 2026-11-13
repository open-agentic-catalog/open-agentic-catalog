# Standards profile bundle

The service builds against immutable upstream inputs. `checksums.txt` binds every vendored artifact and `catalogctl profiles verify` checks the embedded bytes without contacting upstream. See [bundle maintenance](../documentation/src/content/docs/reference/standards.mdx#verify-and-update-standards-profiles) for validation commands and the upgrade workflow.

[Upstream proposals](../documentation/src/content/docs/reference/standards.mdx#unsupported-discovery-features) do not change the active bundle. The selected contracts and their runtime use are documented below and in the [schema validation guide](../documentation/src/content/docs/contributing/testing.md#schema-validation-and-ard-conformance).

The individual [IETF ARD draft-00 documents](../documentation/src/content/docs/reference/standards.mdx#ietf-ard-drafts) are tracked, not selected as a profile. Their media types, continuation and trust requirements must not be inferred from this bundle or the pinned conformance CLI.

## Active profiles

| Profile | Upstream repository | Pinned commit | Purpose |
| --- | --- | --- | --- |
| `ard-0.91-aa3e598` | [ards-project/ard-spec](https://github.com/ards-project/ard-spec) | `aa3e598bb7752a9175897823234311216acfa864` | ARD manifest, Registry API, JSON-LD context, schemas, and official conformance runner. |
| `ai-catalog-1.0-b062278` | [Agent-Card/ai-catalog](https://github.com/Agent-Card/ai-catalog) | `b062278fd7f5a83202d33208f1d07c5b5eb7ccb3` | AI Catalog document and Trust Manifest semantics. |
| `a2a-agent-card-1.0.1-3303592` | [a2aproject/A2A](https://github.com/a2aproject/A2A) | `3303592588e388e62e0f69f701af531d2f4e3991` | A2A Agent Card representation. |
| `mcp-registry-server-1.8.1-f52dc85` | [modelcontextprotocol/registry](https://github.com/modelcontextprotocol/registry) | `f52dc8525a441a3abf5fedc9912152d95af5aab1` | MCP Registry `server.json` distribution metadata. |
| `mcp-server-card-v1-experimental-526201b` | [modelcontextprotocol/ext-server-card](https://github.com/modelcontextprotocol/ext-server-card) | `526201bbc80231daa40ffcdecfc9da4e54e5dc93` | Experimental MCP Server Card discovery representation. |
| `agent-skills-69ef37e` | [agentskills/agentskills](https://github.com/agentskills/agentskills) | `69ef37e9424c0a7ea9dd2293b559e43ec8176379` | Agent Skills package and `SKILL.md` contract. |
| `agent-plugins-1.0.0-1fc1b627` | [agentplugins/agent-plugins-spec](https://github.com/agentplugins/agent-plugins-spec) | `1fc1b6270e3cc492ec2d24ad7a34277c6d53b9c1` | Published Agent Plugins 1.0.0 directory contract, manifest and MCP configuration schemas. |

The upstream licenses are stored beside their artifacts. To upgrade a profile:

1. Retrieve the selected source through the `gh` CLI.
2. Retain its immutable commit in a new profile directory.
3. Update compatibility tests and regenerate `checksums.txt`.
4. Record the impact before you switch configuration.

Do not overwrite an existing profile directory.

Each profile ID identifies a fixed compatibility contract. It does not follow an upstream `latest` version. Upstream changes outside the selected inputs, such as maintainer lists or reader guides, do not by themselves require a bundle or runtime profile upgrade.

## Vendored source paths

Paths below are relative to each profile directory and its upstream repository at the pinned commit. Unlisted path mappings are unchanged: ARD `conformance/` and `spec/schemas/`, MCP Server Card files, Agent Plugins `spec/`, `schemas/` and `LICENSES/`, and root license files retain their upstream paths.

| Profile | Local path | Upstream path |
| --- | --- | --- |
| ARD | `ard.md` | `spec/ard.md` |
| ARD | `ard-v0.9.md` | `spec/ard-v0.9.md` |
| ARD | `ard.cddl` | `spec/schemas/ard.cddl`; the same bytes are also retained at that path locally. |
| ARD | `urn-naming-guide.md` | `spec/urn-naming-guide.md` |
| AI Catalog | `ai-catalog.md` | `specification/ai-catalog.md` |
| AI Catalog | `example.json` | `specification/examples/ai-catalog.json` |
| AI Catalog | `trust-manifest-threat-model.md` | `specification/trust-manifest-threat-model.md` |
| A2A | `a2a.proto` | `specification/a2a.proto` |
| MCP Registry | `generic-server-json.md` | `docs/reference/server-json/generic-server-json.md` |
| MCP Registry | `server.schema.json` | `docs/reference/server-json/draft/server.schema.json` |
| Agent Skills | `specification.mdx` | `docs/specification.mdx` |

All non-license artifacts are byte-for-byte copies of those upstream files. Three retained licenses differ only in final newline bytes: ARD has an extra trailing blank line, and AI Catalog and Agent Skills have a final newline absent upstream. The checksum manifest retains those existing bytes. Do not modify an immutable profile to normalize whitespace. AI Catalog's `LICENSE.md` distinguishes Apache-2.0 software from CC-BY-4.0 specification, documentation and examples; the vendored specification material does not become project-owned Apache-2.0 content.

## Protocols outside the vendored profile bundle

| Standard | Reviewed baseline | Catalog support |
| --- | --- | --- |
| MCP core protocol | [Specification dated 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28). | The backend validates metadata without acting as a resource MCP host or client. Optional `catalogctl mcp serve` and API `/mcp` use the official Go SDK v1.8.0 for read-only stdio and stateless HTTP Control access. Local SDK/CLI exchanges qualify their wiring. The separate `make test-interoperability-mcp-catalog` target uses the official Python SDK 2.3.0 to qualify both transports and protocols `2025-11-25` and `2026-07-28`. This is not the ARD Registry wrapper and does not change the separate Registry or Server Card profiles. |

Agent Plugins 1.0.0 defines a directory package, not a Catalog discovery media type or archive distribution. The AI Catalog draft's `application/agent-plugins+zip` and `application/agent-plugins+gzip` labels do not prove conformance to the published Plugin format, and the pinned ARD profile does not define `application/agent-plugins+json`. See [Standards and interoperability](../documentation/src/content/docs/reference/standards.mdx) for the complete boundary.

## Runtime validation

Agent Skills frontmatter bounds apply to decoded YAML values before display normalization, including decoded whitespace and block-scalar newlines. Explicitly empty compatibility is invalid; omission is allowed. These rules use profile `agent-skills-69ef37e`; its upstream files remain unchanged. See [Skill validation and package requests](../documentation/src/content/docs/reference/api-conventions.md#agent-skill-package-requests). Revalidation of retained Markdown runs the current parser; package-only revalidation verifies previously retained inventory and findings, not transient archive bytes. Corrected package inspection requires a newly submitted revision, not an automatic reinspection of old packages.

### ARD

ARD entries and manifests are duplicate-key checked and validated at ingestion against the exact embedded Draft 2020-12 schema, followed by the prose-required domain-anchored identifier, display name, media type, optional `@id` equivalence, and Trust Manifest publisher-alignment checks. JSON-LD 1.1 uses the pinned base context as `expandContext`; source-owned remote contexts travel through the service safe-fetch policy, while public query requests cannot trigger a new remote-context fetch. The schema and accepted naming ADR allow a terminal-only name, and governance and publication accept that form while service-generated identifiers retain an explicit namespace.

Prose and machine schema disagree on `trustManifest` casing; the profile accepts either spelling alone, rejects both together as ambiguous, preserves source spelling in raw evidence, and normalizes the field to lowercase. Complete-entry and sparse live Search validation apply the same pinned trust-field types and declared publisher alignment. Framework-specific extensions remain open. Validation does not fetch evidence or verify signatures. ARD Search preserves valid manifests from eligible live peers under lowercase `trustManifest`, including unknown framework members and exact numbers. Local publication results do not inherit these peer declarations; raw Control evidence keeps its original spelling.

Complete entries and sparse live Search share validation of present media-type syntax and exact `@id`/`identifier` agreement. Valid unfamiliar and parameterized media types remain accepted without a registration lookup. Sparse results can omit these fields; complete-entry requirements remain schema-owned. The shared EntryFields/query-context validator stays structural. Invalid live entries produce sanitized partial evidence while healthy results survive; source bytes and accepted profiles are unchanged.

Generic manifests use the current ARD model. ARD section 5.1 permits `/.well-known/ai-catalog.json` and `rel="ai-catalog"` as equivalent entry sources; these aliases alone do not select predecessor AI Catalog. Explicit media types, Link `type`, nested catalog declarations and retained source profiles distinguish payloads. `specVersion` and `host` remain transport-defined ARD extras rather than implicit AI Catalog selectors. Contradictory explicit signals fail closed. An approved source endpoint, profile or Registry interface cannot change during synchronization without reinspection.

### AI Catalog

AI Catalog validation implements the pinned 1.0 structural rules for Levels 1 and 2. It checks identifier-version uniqueness, forward-compatible 1.x consumption, extension namespaces, Trust Manifest structure and subject binding. The Catalog does not verify cryptographic signatures or trust anchors. It exposes no verified-trust capability.

Extension maps accept absolute-URI keys or reverse-DNS names with at least two valid labels, such as `com.example`. URI compatibility includes `urn:example:metadata`; it is not limited to HTTP(S). Keys cannot contain literal whitespace or control characters. Validation preserves exact spelling and unknown values without fetching namespace identifiers. These rules apply to document, entry, host Trust Manifest and entry Trust Manifest extension maps. The two-label minimum and URI acceptance are Catalog compatibility choices, not a lexical grammar specified by the pinned prose.

Entry trust binding uses the publisher domain in an AIR identifier. The declared trust identity's domain must match that domain or a subdomain. Existing identity forms include `did:web`, SPIFFE and HTTPS. Alternate identifiers can appear without entry trust metadata; entries with trust metadata require the supported AIR binding. An artifact URL, optional publisher object or trust declaration cannot supply a missing publisher domain. These are declaration checks; they do not establish domain control.

ARD retains its separate AIR identifier requirements and open framework-specific Trust Manifest fields. AI Catalog namespace restrictions do not apply to those ARD fields.

### A2A Agent Cards

A2A validation uses strict ProtoJSON against `internal/a2apb/a2a.pb.go`, generated from the pinned canonical `a2a.proto` with `protoc` v6.32.1 from the Protobuf 32.1 release and `protoc-gen-go` 1.36.10. The generated binding uses Google API annotation sources from `googleapis/googleapis` commit `1f38da6aa7661cf22e17247ad33d2d566a2c356e`; the generated Go file imports the annotation package version pinned in `go.mod`. Service-owned semantic checks enforce required Agent Card fields, exact major/minor protocol-version spelling and safe metadata-only endpoints.

Signature structure requires whitespace-free unpadded base64url, duplicate/Unicode-safe protected JSON, exact protected `alg` and string `kid` members, and disjoint protected/unprotected header names. The signing algorithm cannot be empty or `none`; optional `typ` and unrelated extensions remain optional/opaque. These checks follow the pinned prose's section 8.4.2 and [RFC 7515](https://www.rfc-editor.org/rfc/rfc7515) without verifying signatures or fetching keys.

Existing evidence and snapshots are not automatically rescanned; use explicit revalidation and corrected new revisions, with explicit release retirement/revocation and republication for affected active cards as described in the [A2A Agent Cards](../documentation/src/content/docs/reference/standards.mdx#a2a-agent-cards). Upstream bytes and the evidence storage format are unchanged.

### MCP metadata

MCP Server Cards and MCP Registry `server.json` documents are validated against their exact embedded schemas and then checked for service safety rules. They remain distinct profiles and are never relabeled into one another.

### Agent Skills

Agent Skills Markdown uses a bounded YAML decoder with an exact frontmatter field and type allowlist. Inline validation covers intrinsic `SKILL.md` requirements; the package-level parent-directory-name rule is not knowable from a standalone document. AI Catalog-declared Skill JSON receives bounded service-owned metadata checks but is not claimed as Agent Skills package conformance or an installable package.

Skill ZIP and single-member gzipped-tar representations receive bounded non-executing package validation, matching root-directory and `SKILL.md` checks, complete file inventory and SHA-256 digests, and immutable Git or OCI client handoff. The archive bytes are transient and optional software-assessment states remain `not_assessed`. No assessment provider is implemented; enabling the flag currently fails startup.

### Agent Plugins

Agent Plugins use the exact published 1.0.0 manifest and MCP schemas, followed by bounded, non-executing component checks. The Go validator handles the manifest name pattern through a profile-local equivalent of its negative lookahead without changing the vendored schema. Schema/software licenses are Apache-2.0; the specification and examples retain CC-BY-4.0.

Specification, schemas, license declaration and both license texts retain their upstream paths. Archive delivery and immutable inspection follow [ADR-0019](../documentation/src/content/docs/architecture/decisions/0019-native-agent-plugin-packages.md); client extensions remain inert. Catalog does not install or execute Plugins.

## Registry contract interpretation

ARD Appendix D.3 makes the pinned Registry OpenAPI authoritative for HTTP operations. Its RegistryReferral object allows additional properties despite the closed CDDL shape. Catalog accepts bounded referral extensions; only exact-name core fields matching an approved endpoint authorize traversal. The enclosing Search response remains closed, including differently cased aliases of known fields. Live Search validates present EntryFields without imposing full-entry requirements, accepts exact integer scores such as 80, 80.0 and 8e1, and preserves raw peer evidence. Public merging rejects disclosed contradictions against canonical structured filters; section 5.3.2 permits omission of indexed projection terms, so absence alone is not a contradiction. Sparse output preserves omitted name/type fields instead of inventing empty values. Search and Explore validate query-context URI/object/array shape independently of terms and reject explicit null. Explicit Search selection after a failed GET preserves root discovery, profile binding and reinspection. These rules do not change immutable profiles. The official Registry checker does not validate every property; independent schema/filter assertions remain required.

## ARD conformance evidence

The retained schemas, OpenAPI, CDDL, context, prose, naming guide, conformance CLI and examples provide the pinned ARD inputs. Upstream ADRs, demo and mock code, and repository automation are not runtime specifications; the official examples are conformance fixtures, not schema authorities.

The maintained default gate validates one service-owned manifest and the four checksummed official examples, passes every official example through the actual ARD federation-ingestion profile, proves mandatory-path well-known and relation-aware generic-JSON resolution, rejects profile confusion and synchronization drift, then runs all five fixtures through the unchanged official CLI with exact warning expectations. The reference corpus records exact source commits and distinguishes source-derived, documented and synthetic cases. Catalog handler tests cover requirements absent from the official runner and mock. Independent JSON Schema checks remain mandatory even if the upstream CLI can omit that dependency. The disposable running gate covers official manifest, publisher, and Registry modes. Passing the official runner alone does not establish complete ARD conformance. Use the combined suite described in the [testing guide](../documentation/src/content/docs/contributing/testing.md#schema-validation-and-ard-conformance).

Separate actual-tool qualification serves one schema-valid MCP entry through an owned loopback fixture. It checks the unchanged CLI's exact Search query, implemented-route success, expected native result count and served identifier/type. Missing score, source and identifier each trigger the intended native failure. Empty Search remains a valid protocol response; it cannot qualify expected nonempty result coverage.

These loopback controls do not extend the native checker's field, authentication or continuation coverage. See the [packaging guide](../test/packaging/README.md) for execution and evidence limits.

The default-off packaged phase separately publishes one independently reviewed weather MCP under a dedicated publisher domain. It binds the fixed query's exact identifier/type and nonzero native count to one stable immutable snapshot. All three native modes must succeed without warnings, including complete manifest/entry schema evidence where supported. The phase retires its release and restores the original snapshot. The report retains fixture and native evidence, including after a later failure. These checks do not invoke the MCP or establish complete ARD conformance.

## Separate ANS registration verification

The service-owned `ans-registration-v1` verification profile is separate from the vendored ARD schema profile. The file-only `catalogctl trust verify-bundle` checks authentic ANS V1/V2 evidence using operator-supplied public keys. It does not modify ARD schema inputs or admission, fetch evidence during discovery, or publish verified trust. See the [profile contract](../internal/standards/anstrust/README.md) and [ADR-0024](../documentation/src/content/docs/architecture/decisions/0024-optional-ans-registration-verification.md).

Runtime acquisition and private assessment Jobs are implemented. Verification defaults off and requires an operator policy plus an approved source's explicit policy selection. Administrators request assessments separately from discovery. Broad trust and rights remain `not_assessed`; an ANS registration assessment does not verify software or publisher ownership.
