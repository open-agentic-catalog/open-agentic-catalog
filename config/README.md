# Configuration profiles and examples

Logging samples explicitly select `logLevel: info` and `logFormat: text`. Omission retains those defaults; explicit empty/null/unknown values fail validation for every role, including migration. Backend `CATALOG_LOG_LEVEL` and `CATALOG_LOG_FORMAT` override the baseline. Restart after changes; JSON is an explicit collector choice. See the [configuration reference](../documentation/src/content/docs/reference/configuration.mdx) for independent UI delivery and output destinations.

These files provide non-secret runtime profiles and examples of scoped federation inputs. The [configuration reference](../documentation/src/content/docs/reference/configuration.mdx) owns field definitions, defaults, environment overrides and validation rules.

| Path | Contents and use |
| --- | --- |
| [local.json](local.json) | In-memory development with fixed identities; `make run` selects the combined `all` role. State is lost when the process exits. |
| [postgres.json](postgres.json) | Compose-oriented PostgreSQL development baseline for separate `api`, `worker`, and operator processes. Development identities must be explicitly provisioned. The embedded DSN selects only the development API role. |
| [production.example.json](production.example.json) | Production-shaped OIDC baseline for separate roles, with illustrative deployment values and optional capabilities disabled. It is not a ready-to-run deployment or a capacity recommendation. |
| [federation-policy.local.json](federation-policy.local.json) | Compose-only private route to `http://api:8080` within `172.30.255.0/24`, assigned to the example tenant; public egress is denied. |
| [federation-policy.production.example.json](federation-policy.production.example.json) | Example allowlist for one public HTTPS partner origin, with no private profiles. The example domain is not a hosted test service. |
| [federation-credentials.example/](federation-credentials.example/README.md) | Empty mount placeholder and a link to the credential-tree format and rotation contract. |
| [federation-trust.example/](federation-trust.example/README.md) | Private federation trust layout; actual certificates come from the deployment. |

Configuration is strict: duplicate keys, unknown fields and inconsistent settings fail validation. Fixed development tokens and the local PostgreSQL DSN are fixtures, not production credentials. Do not add real passwords, credential-bearing DSNs, access tokens, signing keys or private keys to these examples. The root [.env.example](../.env.example) documents non-secret Make inputs; binaries do not discover env files. Federation, OIDC and webhook trust have separate scopes.

## Validation and payload budgets

Every backend sample explicitly sets `validation.maxFindings` to 128 and `validation.maxDiagnosticBytes` to 65536. The explicit environment overrides are `CATALOG_VALIDATION_MAX_FINDINGS` and `CATALOG_VALIDATION_MAX_DIAGNOSTIC_BYTES`. Budgets govern new diagnostic arrays; lowering them does not rewrite retained evidence or replay. See [scope, ranges and exhaustion](../documentation/src/content/docs/reference/configuration.mdx#validation-diagnostic-limits).

Backend `http.maxBodyBytes` controls request admission. The independent UI's required `requestBytes` and `responseBytes` control forwarding, and the remote CLI has its own response ceiling. Changing a backend setting does not change either client. See [configuration examples, restart behavior and full-object retrieval](../documentation/src/content/docs/reference/configuration.mdx#payload-and-client-limits). Compact Job results and observation metadata use fixed contracts rather than additional configuration flags.

## Optional integrations

All three JSON profiles leave optional capabilities disabled. `make compose-up` adds development overrides: webhooks default on through `LOCAL_WEBHOOKS=on`, while `LOCAL_UI=on` and `LOCAL_MONITORING=on` independently add the UI and monitoring containers. These launcher settings do not change the JSON files or Helm defaults. Use `LOCAL_WEBHOOKS=off` to keep the local Compose webhook capability disabled unless an explicit `CATALOG_WEBHOOKS_ENABLED` override selects otherwise.

Tracing is also off in every profile. Enable it through the typed `tracing` object or explicit `CATALOG_TRACING` override; use a complete OTLP/HTTP trace URL and installation identity. Secrets belong in mounted header/certificate files, not JSON values. See [tracing configuration](../documentation/src/content/docs/reference/configuration.mdx#optional-tracing-configuration) and the [observability runbook](../documentation/src/content/docs/operations/observability.mdx#distributed-tracing). No provider, endpoint or referenced file is needed while disabled; `catalogctl` does not start an exporter.

Semantic search is disabled in the baseline examples. Enable the typed `semanticSearch` group only after configuring one compatible endpoint and explicitly applying its optional PostgreSQL schema. See [semantic configuration limits](../documentation/src/content/docs/reference/configuration.mdx#optional-hybrid-search) and [hybrid search operations](../documentation/src/content/docs/operations/hybrid-search.mdx). Owned model/runtime tests use separate fixture settings, not retained development data.

## Select a profile and role

The loader applies built-in defaults, the selected JSON file, and then allowlisted environment overrides. All three runtime profiles leave optional capabilities off and omit the federation policy path, so their standalone default denies outbound federation and HTTPS representation retrieval. Files next to a profile are not discovered automatically. Profile and policy paths refer to the process filesystem; relative development paths resolve from the process working directory, not the JSON file's directory.

`local.json` supports `api`, `worker`, and `all`, but separate memory-backed processes do not share state; use `all` for the local walkthrough. PostgreSQL profiles support `api`, `worker`, and `migration`, never `all`. Select the role with the command, not by editing `profile`.

For PostgreSQL, supply the API DSN to `catalogd api`, the distinct worker DSN to `catalogd worker`, and the schema-owner DSN to migration and initial identity-provisioning commands. Neither application role runs migrations. Compose wires those identities and orders migration and development-identity provisioning before startup. Outside Compose, replace the `postgres` service hostname, port, and credentials with the explicitly selected database inputs; see [database roles](../documentation/src/content/docs/operations/database-roles.mdx). The local discovery host is exactly `127.0.0.1`: requests to `localhost` or the Compose service name do not become discovery aliases merely because they reach the same listener.

For production, replace the example origins, issuer, audience, tenant mappings, publisher domain, CORS origin, and database endpoint. Inject each role's DSN through `CATALOG_DATABASE_DSN` or `CATALOG_DATABASE_DSN_FILE`, not both. Supply the same deployment-owned cursor signing key to API and worker through `CATALOG_CURSOR_SIGNING_KEY` or `CATALOG_CURSOR_SIGNING_KEY_FILE`: its value is base64 for exactly 32 bytes. No cursor key is required by migration commands.

Enabled webhooks require a separate encryption key; disabled webhooks need none. Provision Catalog identity mappings and initial authority before relying on OIDC authentication. Listener addresses do not configure TLS termination; see the [deployment guide](../documentation/src/content/docs/operations/deployment.mdx).

The development profiles keep six bundled personas and fourteen exact-template choices. All use explicit `developmentTokens.roleTemplates`, which does not expand companions. The administrator bundles select only `tenant_admin`; Contributor, Reviewer and Publisher omit redundant Reader grants. Permission unions remain unchanged. Legacy `roles` still supports its explicit bootstrap companions; do not place newer template keys in that vocabulary. Namespace-only and mixed declarations preserve exact scoped grants in memory and owner provisioning. Consistent aliases share one principal's union; conflicting actor types fail before provisioning. Optional `actorType` selects Human or Service independently of the empty template. A declaration without grants retains one empty template assignment and defaults to Human in both stores. Explicit `service_client` declarations keep their Service inference when `actorType` is omitted. Optional bounded `label`/`description` fields describe each choice. `profileGroup` (`bundle` or `template`) groups the chooser; `elevated` and `separateTenant` are optional public presentation flags. These describe seed intent, never current authority. Unclassified custom choices remain available. Generated UI users carry server-only credentials. Follow [identity configuration and restart/provisioning rules](../documentation/src/content/docs/reference/configuration.mdx#authentication-and-catalog-identity), the [fresh/retained assignment comparison](../documentation/src/content/docs/guides/access-management.mdx#development-provisioning-and-retained-grants) and the [role handoff guide](../documentation/src/content/docs/guides/catalog-ui.mdx#development-identities-and-role-handoffs).

## Select outbound policy and scoped mounts

Set `federation.policyFile` or `CATALOG_FEDERATION_POLICY_FILE` to load a reviewed policy. Production requires an absolute clean path. Compose mounts `CATALOG_COMPOSE_FEDERATION_POLICY_FILE` at `/config/federation-policy.json` and selects that in-container path for API and worker. Its local policy assumes the maintained Compose subnet; if the subnet changes, update the policy's exact private range too. The Helm chart supplies its own default-deny policy or a selected policy ConfigMap, not either example automatically.

The local private profile is named `compose-catalogs`; only a governed federation source assigned to that profile can use it. It does not enable public URL representations, create source records, change discovery-host bindings, or make the destination ready. Customize the production policy's illustrative origin before use. Retain the empty built-in allowlist if no outbound access is needed; do not enable unrestricted egress just to make an example work.

Credential and trust directories contain only README placeholders. Mounting them does not enable credential resolution or add private trust. Follow their linked layouts and the [configuration reference](../documentation/src/content/docs/reference/configuration.mdx#outbound-egress-policy) for explicit enablement, scoped trust, and rotation. Shared configuration and federation trust changes require an API/worker rollout; credential binding generations are resolved per request. OIDC and webhook trust remain separate from federation trust.

## Validate without starting dependencies

Run from the repository root:

```sh
make config-check CATALOG_ENV_FILE=/dev/null
make config-check CATALOG_ENV_FILE=/dev/null CATALOG_CONFIG_FILE=config/postgres.json CATALOG_CONFIG_ROLE=api
make config-check CATALOG_ENV_FILE=/dev/null CATALOG_CONFIG_FILE=config/postgres.json CATALOG_CONFIG_ROLE=worker
make config-check CATALOG_ENV_FILE=/dev/null CATALOG_CONFIG_FILE=config/production.example.json CATALOG_CONFIG_ROLE=migration
```

To check production API or worker inputs, select `CATALOG_CONFIG_FILE=config/production.example.json` and `CATALOG_CONFIG_ROLE=api` or `worker` after injecting the required cursor key. Without that key, validation is expected to fail. `CATALOG_ENV_FILE=/dev/null` skips the Make `.env` file only; explicit command-line and inherited runtime environment overrides still apply.

For API, worker and combined roles, `config-check` validates shape and required key inputs, loads any selected federation policy and referenced trust bundles, and loads a selected webhook policy only when webhooks are enabled. The migration role skips those outbound policies and runtime keys. API, worker and combined checks also read selected development demo-archive allowlist/trust files; migration skips those files. Migration still decodes supplied overrides and validates common settings and database inputs.

No role opens PostgreSQL, contacts OIDC, resolves source credentials, loads OIDC, semantic-provider or tracing trust/credential files, or starts listeners during this check. See [delivery precedence and validation layers](../documentation/src/content/docs/reference/configuration.mdx#configuration-delivery-and-precedence) for direct binaries, Make, Compose, Helm and the independent UI. A successful check is not database-role, connectivity, or deployment qualification. `catalogctl diagnostics` performs a deeper role-specific dependency check; use it only against an explicitly selected environment.

Runtime config changes also need parser tests, affected sample profiles and Compose/Helm wiring; see [contribution rules](../CONTRIBUTING.md).

PostgreSQL profiles expose `database.maxConnLifetime` (`30m`), `database.maxConnIdleTime` (`5m`) and `database.healthCheckPeriod` (`30s`). These positive startup durations apply to API, worker and operator pools; environment overrides take precedence over JSON and all three override parsed DSN timings. See [PostgreSQL pool settings](../documentation/src/content/docs/reference/configuration.mdx#postgresql-pool-settings) for bounds and restart semantics.

Optional external listing artwork uses `listingMedia` and the closed `CATALOG_LISTING_MEDIA` JSON override. All profiles start with an empty exact origin allowlist. Public origins must also be admitted by federation egress; private bindings name an existing tenant-assigned profile. See [listing artwork configuration](../documentation/src/content/docs/reference/configuration.mdx#listing-artwork). This does not enable managed uploads.

Optional `trustVerification` defaults off in every profile. `CATALOG_TRUST_VERIFICATION` supplies a strict partial JSON override. Enabled API and worker startup loads named authority policies and bounded public PEM files without network contact. Migration commands do not consume them. Administrators can request durable assessment Jobs for exact source entries and read private results. Ordinary discovery makes no evidence requests. See [ANS configuration](../documentation/src/content/docs/reference/configuration.mdx#ans-registration-verification), the [assessment procedure](../documentation/src/content/docs/operations/federation.mdx#request-an-ans-registration-assessment) and the [empty Compose key mount](trust-verification.example/README.md).

Optional API MCP is disabled in all baseline profiles. The four `mcp.*` settings control activation, exact browser origins, complete tool-result bytes and concurrent requests. They reuse the existing public URL, authentication audience and request deadline. See [HTTP MCP configuration](../documentation/src/content/docs/reference/configuration.mdx#optional-http-mcp). Do not set SDK `MCPGODEBUG` compatibility overrides.
