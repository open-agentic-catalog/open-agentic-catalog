---
name: standards-conformance
description: Interpret and qualify Open Agentic Catalog standards adapters, emitted discovery formats and pinned upstream profiles. Use for format changes, profile reviews or conformance investigations; ordinary API changes and dependency upgrades use their owning workflows.
---

# Standards conformance

Work from the repository root. Resolve instruction paths from that root. Read `AGENTS.md`, the affected entry in pinned profiles (`schemas/PROFILES.md`) and the relevant standards reference (`documentation/src/content/docs/reference/standards.mdx`). Preserve review-only and unit-only scope. Profile review does not authorize a pin upgrade or execution of an external implementation.

When explicitly qualifying the local Directory lab, follow the AGNTCY harness (`test/interoperability/agntcy/README.md`). Its native A2A artifact path is distinct from ARD federation: passing empty manifest/publisher schemas cannot prove inventory transfer. Preserve the exact Registry List/Search failures and independent raw-entry identifier/metadata errors; an expected incompatibility is neither a conformance pass nor a Catalog defect. Use the fixed local target without translating peer responses, invoking Agent endpoints or acquiring a new peer version.

## Establish the requirement

Identify the exact profile, immutable revision and input/output boundary before changing a validator or assertion. Read only the relevant retained prose, machine schema and documented service interpretation. An ignored `specs/` checkout, upstream `main`, example or reference implementation does not supersede the selected profile. Use `gh` exclusively for GitHub retrieval and retain exact revisions when external research is needed.

Trace the format through its adapter or standards parser and affected ingestion, retained evidence, publication and discovery consumers. Distinguish schema validity, prose requirements and Catalog safety rules. For disagreements, cite both requirements and the existing profile interpretation; do not change vendor bytes or make tests accept the current implementation without justification.

- Generic hosted manifests use ARD. Select predecessor AI Catalog only from explicit transport/profile evidence. The `/.well-known/ai-catalog.json` path and `rel="ai-catalog"` alone do not select the legacy payload profile. Preserve unknown evidence where allowed and reject contradictory signals. Require reinspection when an approved source changes its resolved endpoint, profile or Registry interface.
- A2A bindings are generated; use the recorded source/toolchain and regeneration procedure rather than hand-editing them. MCP Registry `server.json` and MCP Server Card remain distinct formats.
- Skill and Plugin validation inspects bounded metadata/files without installation or execution. A directory specification is not an archive or discovery-media-type standard. Do not invent an ARD Plugin mapping or present structural checks as malware assessment or signature verification. Distinguish Catalog's whole-package admission checks from the Plugin specification's client rules for isolated component failures.

Reduce useful compatibility cases to local fixtures using the existing corpus conventions. Record whether each case is source-derived, documented or synthetic, its provenance and expected accept/reject result. Reference code and hosted claims have separate evidentiary limits; static fixtures do not establish a peer deployment's behavior.

## Coordinate an authorized change

A bug against the current profile normally needs a local implementation fix and focused regression assertions. A profile upgrade follows bundle maintenance (`schemas/README.md#how-to-update-a-profile`): add a versioned immutable directory, retain commit/path provenance and licenses, reconcile checksums, selectors and compatibility notes, and update affected validators, configuration, examples and tests together. Do not rewrite an existing bundle in place or infer a need to upgrade from unrelated upstream edits.

Use API contract changes (`.agents/skills/api-contract-changes/SKILL.md`) when wire behavior changes. Extend existing positive/negative cases for the actual interpretation, raw-evidence preservation and rejection/partial-failure behavior; avoid duplicating complete ingestion journeys. Test authoring and classification remain with unit testing (`.agents/skills/unit-testing/SKILL.md`) and functional testing (`.agents/skills/functional-testing/SKILL.md`).

## Qualify the applicable modes

Follow schema validation and ARD conformance (`documentation/src/content/docs/contributing/testing.md#schema-validation-and-ard-conformance`) and post-code verification (`.agents/skills/post-code-verification/SKILL.md`). `make profiles` verifies embedded bytes and identities, not arbitrary documents or running-service conformance. Profile changes need applicable compatibility tests and `make conformance-fixture`; the latter already runs independent Catalog checks and the pinned official examples/CLI through the maintained selection.

Before claiming upstream JSON Schema validation, import `Draft202012Validator` from `jsonschema` using the same interpreter selected by Make's `PYTHON`. Inspect the official output for both `ArdManifest` and per-entry `ArdEntry` validation for every checked manifest. The upstream runner can return success after skipping schemas; missing validators, missing schemas and validation failures cannot count as a complete pass. Keep the independent Go assertions and expected warnings intact.

When discovery output changes, use `make conformance-live` against an active publication in an owned disposable Catalog. It runs manifest, publisher and Registry modes but neither starts nor seeds the service. Follow the guide's URL, credential and matching Search-fixture requirements: an empty result skips per-result checks, and unsupported endpoints may be tolerated upstream. Retain the Catalog's stronger endpoint, authorization and continuation assertions.

Report pins, interpretation, checks actually executed, warnings and remaining limits. Unit-only execution leaves upstream process/live checks unrun. External implementation execution needs its separately selected scope; the interoperability harness (`test/interoperability/README.md`) separates `make test-interoperability` client qualification from the explicitly selected `make test-interoperability-hf` local HTTPS peer target. Preserve native checker status, manifest/entry schema evidence and independent Search assertions; protocol-only Registry success is not complete schema validation. Expected HF limits remain dispositions rather than repaired Catalog defects. Neither target authorizes hosted calls or another peer implementation.

For reader documentation, follow the writing guide (`documentation/src/content/docs/contributing/documentation.mdx#writing-style`). Explain supported behavior, version requirements and compatibility limits in plain language. Separate specification requirements, Catalog rules and observed peer behavior. Keep dated comparisons and test measurements in engineering records. Do not turn a standards reference page into a research journal or claim cryptographic trust from structure checks.

For explicitly selected independent clients, consumers or peers, use interoperability qualification (`.agents/skills/interoperability-qualification/SKILL.md`) to select the owning harness and native controls. This skill owns pinned normative interpretation, while the interoperability workflow owns measured compatibility, exact governed handoffs, confinement and capability dispositions. Metadata validation alone does not qualify a client handoff or authorize runtime invocation.
