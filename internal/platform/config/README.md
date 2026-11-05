# Runtime configuration boundary

This package combines typed defaults, an optional strict JSON file, explicit environment overrides and external policies, then validates inputs for the selected process role. It does not read `.env` or infer setting names from arbitrary environment variables.

| Files | Responsibility |
| --- | --- |
| [config.go](config.go), [defaults.go](defaults.go) | Configuration types, duration encoding, role vocabulary, defaults and loading order. |
| [environment.go](environment.go) | Explicit override allowlist and typed scalar or collection decoding. |
| [validation.go](validation.go), [tracing.go](tracing.go) | Cross-field, role, listener, authentication and optional tracing validation. |
| [trust_verification.go](trust_verification.go) | Default-off ANS policies, strict override, role budgets, bounded startup public-key loading and safe policy reporting. |
| [artwork.go](artwork.go) | Exact media origin/profile bindings, image-work limits, strict `CATALOG_LISTING_MEDIA` override and redacted policy reporting. |
| [semantic.go](semantic.go) | Typed provider settings, strict validation, vector/ranking-profile identity and bounded `CATALOG_SEMANTIC_SEARCH` JSON override. Runtime validation also checks role-specific request/lease budgets. |
| [federation_policy.go](federation_policy.go), [webhook_policy.go](webhook_policy.go) | Bounded external policy loading, normalized routing rules and trust digests. |
| [secrets.go](secrets.go) | Deployment-injected runtime keys and bounded secret-file checks. |
| [report.go](report.go) | Safe diagnostic projection without secret values. |

The shared `jobs` group defaults to five attempts and a one-hour retry window cap, with explicit `CATALOG_JOBS_MAX_ATTEMPTS` and `CATALOG_JOBS_RETRY_CEILING` overrides. API, worker and combined roles validate its bounds; migration validation ignores its semantic bounds. The non-secret values are included in configuration reports.

Add settings across the parser, applicable sample profiles, explicit override list, safe report, deployment wiring and tests. The [configuration reference](../../../documentation/src/content/docs/reference/configuration.mdx) owns operator-facing semantics; [sample profiles](../../../config/README.md) provide runnable inputs. Run `go test ./internal/platform/config -shuffle=on -count=1` for focused checks.

The configuration tests are isolated units. Trust-bundle tests generate certificate bytes directly and use temporary files; they do not start a TLS server or test a handshake. Actual private-trust exchanges remain in the owning transport packages' functional suites.

Worker dependency restarts default to a 1s initial window and 30s ceiling. Worker/all roles validate `1s <= initial <= ceiling <= 5m`; API/migration roles ignore these semantic bounds. Explicit overrides and safe reports use the existing worker group. See [configuration and restart semantics](../../../documentation/src/content/docs/reference/configuration.mdx#worker-dependency-restarts).
