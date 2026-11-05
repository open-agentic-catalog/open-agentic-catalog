---
name: configuration-and-credentials
description: Review or change Open Agentic Catalog runtime settings, capability activation, secret delivery and credential rotation. Use for precedence, validation, loading and replacement semantics; image mechanics and local startup use build-and-deployment, and chart implementation uses helm-and-minikube.
---

# Configuration and credentials

Work from the repository root. Resolve instruction paths from that root. Read `AGENTS.md`, the configuration reference (`documentation/src/content/docs/reference/configuration.mdx`) and only the affected backend loader (`internal/platform/config/README.md`), sample profiles (`config/README.md`) or independent UI host (`catalog-ui/internal/web/README.md`). Preserve review-only and unit-only scope. Changing code or documenting rotation does not authorize replacing credentials in a retained deployment.

## Trace delivery and activation

Identify the setting's consuming role, non-secret configuration owner, secret/trust input where applicable, and loading point. Trace defaults, selected JSON, allowlisted environment overrides, Make exports, Compose inputs and Helm templates only where they participate. Binaries do not load env files; Make's selected workflow file is executable Make syntax. `CATALOG_ENV_FILE=/dev/null` omits that file, not inherited overrides. Chart-pinned values and the independent UI's strict JSON loader have distinct precedence; backend overrides do not configure the UI.

Distinguish delivery, process activation and upstream acceptance/revocation. Use the per-consumer rotation table (`documentation/src/content/docs/operations/maintenance.mdx#rotate-credentials-and-trust`) rather than assume mounted file changes reload a client. Most inputs are startup-loaded; source credential bindings resolve complete immutable generations per request. Existing Secret/ConfigMap content changes alone do not establish a Helm rollout. Preserve direct/file mutual exclusion, bounded secret parsing, safe diagnostics and separate API, worker, owner and UI authority.

The three live tenant settings (`documentation/src/content/docs/reference/configuration.mdx#live-tenant-settings`) are retained application policy under ADR-0025 (`documentation/src/content/docs/architecture/decisions/0025-tenant-operational-settings.md`). Trace their typed API/store and admission consumers instead of adding loader, environment or chart fields. Preserve inheritance, current permissions, exact ETags, atomic audit, policy-read failure and unchanged admitted work. Deployment changes do not overwrite saved overrides; diagnostics show startup inputs, not active tenant policy.

## Change the smallest owning contract

For a setting change, update typed decoding/defaults, cross-field and role validation, the explicit override allowlist, safe reports, affected profiles and configuration documentation together. Update `.env.example` and actual Compose/Helm delivery when relevant. Include omission, invalid/null/duplicate/unknown input, role applicability and meaningful enabled/disabled controls. Keep unavailable capabilities unavailable; a feature flag or provider declaration cannot implement them. Lower limits prospectively without rewriting retained evidence or replay responses.

For activation or rotation, follow the specific runbook: UI session keys use staged reader/writer overlap; cursor keys have no overlap ring; PostgreSQL password acceptance must be tested with new connections; source binding generations are replaced atomically; webhook signing rotation and deployment ciphertext-key replacement are different operations. Webhook maintenance (`documentation/src/content/docs/operations/webhooks.mdx#deployment-encryption-key-replacement`) owns its stopped-runtime and backup-key requirements. Select only the relevant procedure, preserve recovery material, and never create a blanket hot-reload or restart controller.

External secret managers deliver existing scoped inputs. The owned Minikube scenario qualifies selected Vault/VSO delivery, rotation and recovery. Follow the maintenance guide for tested scope and remaining limits. Vault remains optional and is not a Catalog dependency. Keep real secrets out of profiles, values, fixtures, reports and command output. Use security review (`.agents/skills/security-review/SKILL.md`) for substantive secret/trust-boundary changes and recovery and repair (`.agents/skills/recovery-and-repair/SKILL.md`) when retained ciphertext or restored state is involved.

## Verify the consuming boundary

Use `make config-check` with explicit `CATALOG_CONFIG_FILE` and `CATALOG_CONFIG_ROLE` for static backend validation. It loads the files required by that role's static contract but does not contact dependencies or qualify database privileges, credential acceptance or UI configuration. It executes the CLI and is not a pure-unit gate; under an explicit unit-only limit, use isolated loader/host tests and report command/deployment qualification as unrun. Check its documented validation layers before choosing a deeper diagnostic. Reuse unit testing (`.agents/skills/unit-testing/SKILL.md`) for parser/loading/privacy decisions and the existing functional fixtures (`.agents/skills/functional-testing/SKILL.md`) for real replacement, retired-input rejection and retained-state checks.

Use build and deployment (`.agents/skills/build-and-deployment/SKILL.md`) for launcher delivery and Helm and Minikube (`.agents/skills/helm-and-minikube/SKILL.md`) for rendered settings, mount contracts and affected rollouts. Select applicable checks through post-code verification (`.agents/skills/post-code-verification/SKILL.md`). Report which consumer loaded which generation using safe identity fields, actual acceptance tests, restart/overlap requirements and unrun boundaries. Update the configuration reference and owning maintenance procedure rather than duplicate another input inventory here.
