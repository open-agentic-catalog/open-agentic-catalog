---
title: CLI reference
---

Use `catalogd` to run the backend, `catalogctl` for client and operator commands, and `catalog-ui` for the optional browser host. The CLI implements the commands below; it does not expose every HTTP API operation. Use [API conventions](api-conventions.md) and the generated HTTP reference for other supported requests. Contributor workflows have a separate [Make reference](../contributing/make-targets.md).

The binaries do not read `.env`. Make can load the selected developer `.env`; remote commands have separate client inputs. See [configuration delivery](configuration.mdx#configuration-delivery-and-precedence) for precedence and [UI configuration](ui-host-configuration.md#host-configuration) for host settings.

Provide credentials through a protected environment or secret mount. Do not put database credentials in command-line arguments. Select the required authority and target before an operator command.

## Command index

| Binary and purpose | Commands | Authority and detailed reference |
| --- | --- | --- |
| Backend processes | `catalogd api`, `worker`, `all` | Runtime configuration; [process commands](#process-commands). `all` requires memory-backed development. |
| Browser host | `catalog-ui --config PATH`, `--probe` | Independent UI JSON or fixed loopback probe; [UI host](#optional-ui-host). |
| Human API login | `catalogctl auth login`, `status`, `logout` | Explicit local credential file; [human CLI login](#human-cli-login). |
| Selected API clients | `catalogctl remote capabilities`, `search`, `record`, `job`, `reindex`, `trust-request`, `trust-result` | Explicit API origin and credentials; [remote commands](#remote-catalog-reads). Reindex and trust-request mutate. |
| MCP host connection | `catalogctl mcp serve` | Explicit API bearer input; [MCP host](#use-the-catalog-from-an-mcp-host). Stdio only; tools read metadata. |
| Draft import | `catalogctl import preview`, `apply`, `resume` | Offline preview, explicit destination and private recovery; [reusable JSON](#import-a-reusable-draft-json-file) or [native manifests](#offline-native-import-preview). |
| Draft and document downloads | `catalogctl export`, `export preview`, `export batch` | Read-only API selection with private local output; [native documents](#export-a-native-document) or [reusable JSON files](#export-a-reusable-draft-json-file). |
| Offline evidence | `catalogctl trust verify-bundle` | Local evidence and accepted public-key policy; [ANS verifier](#offline-ans-registration-verification). |
| Operator and recovery | `catalogctl audit`, `config`, `migrate`, `identity`, `repair`, `restore`, `profiles`, `diagnostics`, `probe` | Local files, explicit runtime role or schema-owner authority; [operator command index](#operator-commands). |
| Development fixtures | `catalogctl bootstrap`, `smoke`, `seed-demo` | Explicitly selected development state; [development commands](#development-bootstrap-and-smoke). |
| Embedded identity | `catalogd version`, `catalogctl version`, `catalog-ui version` | No configuration, service or network; [output conventions](#help-output-and-exit-status). |

`catalogd` and `catalogctl` accept `help`, `-h` and `--help` at top level, and `version` or `--version`. The UI accepts `-h`/`--help` and `version`/`--version`. Command-specific flag help follows the selected command; supported exceptions are stated below.

## Obtain and invoke the binaries

The backend source build writes `catalogd` and `catalogctl` under `bin/`; it does not install them on your shell's `PATH`:

```sh
make build
./bin/catalogd version
./bin/catalogctl version
./bin/catalogctl config validate --config config/local.json --role all
```

The tables and operator runbooks use bare `catalogd` and `catalogctl` names for installed binaries. In a source checkout, use the corresponding `./bin/` path. Build prerequisites are in the [local walkthrough](../guides/local-development.mdx); neither the Go build nor these commands needs Node/npm.

The runtime image contains `/usr/local/bin/catalogd` and `/usr/local/bin/catalogctl`, with `catalogd` as its default entrypoint. To run an operator command, explicitly select `/usr/local/bin/catalogctl` as the entrypoint. The scratch image has no shell or package manager: do not wrap commands in `sh -c` or rely on in-container shell expansion. Provide role-appropriate configuration, scoped secrets and trust through the deployment's inputs, using paths as seen inside the container. An operator command still requires the authority and prerequisites in its owning runbook.

Build the optional UI separately. Its build needs the [UI prerequisites](../contributing/build-and-release.md#build-and-run-the-ui-locally), including Node/npm and installed frontend dependencies:

```sh
make ui-install
make ui-build
./bin/catalog-ui version
./bin/catalog-ui --help
```

`make build` does not build the UI. `make ui-build` writes browser assets under `catalog-ui/dist/` and the host at `bin/catalog-ui`. No released binary download or Homebrew package is currently provided.

Follow [Docker deployment](../operations/docker.mdx) for entrypoint overrides and secret mounts, or [Helm deployment](../operations/deployment.mdx) for coordinated operator workflows. The separate UI image contains the UI host and its built assets; it has no database authority.

## Process commands

| Command | Effect |
| --- | --- |
| `catalogd api --config <path>` | Runs the product HTTP API with the exact API database identity. |
| `catalogd worker --config <path>` | Runs durable work and a private operations-only listener with the distinct worker identity. |
| `catalogd all --config <path>` | Combines roles for memory-backed development only. |
| `catalogd version` | Prints build identity without starting the service. |
| `catalogd --help` | Prints process usage. |

The backend role commands accept an optional `--config` JSON baseline plus backend environment overrides. Omitting the file does not supply required production credentials. Follow [runtime validation](configuration.mdx#validation-and-diagnostics) before startup.

### Optional UI host

| Invocation | Effect |
| --- | --- |
| `catalog-ui --config <path>` | Starts the independently configured host with its built assets, session keys and selected API/identity inputs. |
| `catalog-ui version`, `catalog-ui --version` | Prints embedded build identity as JSON without loading configuration. |
| `catalog-ui --help` | Prints host flags. |
| `catalog-ui --probe` | Checks only `http://127.0.0.1:8080/readyz`; success has no stdout. |

`--config /path/to/ui.json` is required for host startup. The binary reads no UI or backend environment overrides. Launchers can generate this JSON from their own variables. See [UI host configuration](ui-host-configuration.md) for required fields, files and restart behavior. See [the UI launch procedure](../contributing/build-and-release.md#build-and-run-the-ui-locally) for an owned local host.

The UI probe uses a fixed port, ignores `--config` and does not test API or issuer availability. For another host port, select its exact readiness URL with `catalogctl probe`. Health probes are described in [observability](../operations/observability.mdx#listener-boundaries).

## Help, output and exit status

Inspect usage before running an unfamiliar command:

```sh
./bin/catalogd --help
./bin/catalogd api --help
./bin/catalogctl --help
./bin/catalogctl remote --help
./bin/catalogctl import preview --help
./bin/catalogctl config validate --help
```

Flag help omits environment-derived defaults and supplied values. The tables below state relevant defaults. Shared operator help can list flags that one action rejects; read the action restrictions before use. `catalogctl profiles verify` takes no flags and does not accept `--help`; top-level help gives its invocation.

| Output | Convention |
| --- | --- |
| `catalogd version` or `--version` | Text: `open-agentic-catalog <version> (<commit>) [<sourceState>]`. |
| `catalogctl version` or `--version`; UI version | JSON fields `version`, `commit` and `sourceState`. |
| `catalogctl mcp serve` | Newline-delimited MCP protocol on stdout; tool results are not standalone command reports. |
| Successful `catalogctl` reports | One JSON value on stdout; remote commands use the `data`/`meta` envelope described below. |
| Successful probes | No result output. |
| Running services | Configured text/JSON service logs on stdout. |
| Command failures | Safe diagnostics on stderr and exit status 1. |

Successful help, version and completed command execution return exit status 0. Inspect the report's status as well. An accepted request, partial Search results, degraded diagnostics or a read of a failed Job does not prove operational success. `diagnostics` and `bootstrap` can emit configured startup diagnostics on stderr while returning successful JSON on stdout. Nonempty stderr alone is not a failure signal.

Use [the build guide](../contributing/build-and-release.md#application-version-and-release-checks) for version meaning and [diagnostic safety](../operations/observability.mdx#current-safety-and-interpretation-limits) for redaction limits. Do not treat a diagnostic report as anonymized data or a replacement for service metrics.

## Offline native import preview

`catalogctl import preview --file PATH` prepares one to 64 native drafts from a `format: 1` YAML manifest. It runs local validation without API credentials, runtime configuration, a database or network calls. It creates no drafts. Use the [manifest and input reference](../guides/resource-authoring.mdx#preview-a-native-draft-locally) for all four resource kinds, complete Skill packages and HTTPS Plugin input.

Preview applies the API's character limits: 512 Unicode code points for opaque identifiers and 128 for version labels. Relationship targets use the same limits. Request byte limits apply separately. See [identifier and version lengths](api-conventions.md#identifier-and-version-lengths).

| Flag | Default or bound |
| --- | --- |
| `--file PATH` | Required local YAML file. Asset paths are relative to its directory; symbolic links and escapes are rejected. |
| `--max-request-bytes BYTES` | 2,097,152 by default; 1-67,108,864 bytes. Includes JSON and base64 overhead; total prepared bodies remain bounded to 64 MiB. |
| `--timeout DURATION` | Two minutes; positive and at most ten minutes. Bounds the whole preview. Ctrl-C/SIGTERM cancels it. |

The bounded JSON report includes local findings and input checksums. Its ordered `resources` array describes the structure of four planned requests per resource. It omits full documents and package base64. URL content, server policy and authorization remain unchecked.

A local native validation error produces a report and exit status 1. Input or preparation errors can fail before a report is available. Apply and resume use the same prepared input, with explicit API authority and a private receipt.

## Apply and resume native drafts

Use `catalogctl import apply --file PATH --state-file PATH` to create one to 64 new drafts through four existing Control API commands per item. Use `import resume` with the unchanged inputs and receipt to resume an interrupted native import. Neither command reviews, publishes, installs or invokes a resource.

Both accept the preview flags plus `--base-url`, `--token-file`, `--ca-file` and `--max-response-bytes`. API inputs use the same `CATALOG_REMOTE_*` environment variables as remote commands. A token file wins over the token environment variable. No token argument is accepted. Response bytes default to 8 MiB, with a 64 MiB maximum; the whole invocation deadline remains two minutes, with a ten-minute maximum.

Apply requires a new receipt in a private directory. Resume binds the API origin, server-resolved principal and exact whole-manifest input. All local validation and namespace permission checks precede the first write. Items run in manifest order; known dependencies must target earlier items. It reuses original command keys, stops outside the conservative replay window and never automatically retries a mutation.

Completed reruns verify retained exact IDs without writes. If validation reports a blocked result, the import retains the Version and its validation evidence. It stops before creating a Listing.

Reports contain an ordered `items` array and identify the stopped item with `currentKey`, when known. Completed, current and unattempted items remain distinct. Reports and receipts each have a 2 MiB ceiling. A batch is not atomic, and the CLI does not roll back committed drafts.

An outcome report can accompany exit status 1. Preserve it and the receipt: failure does not prove that a request did not commit. Follow [apply and recovery](../guides/resource-authoring.mdx#apply-and-resume-a-native-draft) for permissions, directory modes, dispositions, limits and examples.

## Human CLI login

Use `catalogctl auth login` with a separately registered [public OAuth client](glossary.md#confidential-and-public-client). [OIDC and PKCE](glossary.md#oidc-and-pkce) connect that client to the identity provider. The issuer supplies tokens; Catalog supplies [membership](glossary.md#membership) and permissions. The CLI uses Authorization Code with S256 PKCE. It has no client secret and does not load server configuration.

Register a native client with the issuer. Permit `http://127.0.0.1:PORT/oauth/callback`, with a variable loopback port if supported. Use `--listen 127.0.0.1:49152` when the issuer requires a fixed port. IPv6 uses `--listen '[::1]:49152'` and the corresponding callback. For Keycloak, register `http://127.0.0.1/oauth/callback` without a port; Keycloak matches the same loopback path on a variable port. See [Keycloak redirect registration](https://www.keycloak.org/securing-apps/oidc-layers#_redirect_uris). Open the authorization URL on the same machine as the CLI. Remote shells need an explicit loopback tunnel; device authorization is unavailable.

Configure the issuer to provide an ID token for the CLI client and an access token for the Catalog API audience. The CLI checks the ID token's signature, issuer, client, expiry and nonce. The ID token must name only the CLI client in its audience. Catalog must accept the access token through `/api/v1/me` for the same active human subject. Issuer login alone grants no Catalog permissions. An ID token cannot replace an access token.

```sh
mkdir -m 700 "$HOME/.catalog-login"
catalogctl auth login \
  --issuer https://identity.example.com/realms/catalog \
  --client-id catalog-cli \
  --base-url https://catalog.example.com \
  --file "$HOME/.catalog-login/work.json" \
  --scope offline_access
```

Open the URL printed to stderr. The command writes only local status JSON to stdout. `openid` is automatic. Repeat `--scope` for additional API [scopes](glossary.md#scope); scopes do not configure the API audience. Request `offline_access` only when the issuer supports refresh. The issuer may refuse it or issue no refresh token. Keycloak also requires the user's `offline_access` realm role and the corresponding client scope. See [Keycloak offline access](https://www.keycloak.org/docs/latest/server_admin/#_offline-access). This issuer permission grants no Catalog permissions.

Clear inherited manual bearer inputs, then select the saved credentials explicitly:

```sh
unset CATALOG_REMOTE_TOKEN CATALOG_REMOTE_TOKEN_FILE
catalogctl remote search --base-url https://catalog.example.com \
  --login-file "$HOME/.catalog-login/work.json" --text "order processing"
catalogctl auth status --file "$HOME/.catalog-login/work.json"
catalogctl auth logout --file "$HOME/.catalog-login/work.json"
```

| Login flag | Required input, fallback or bound |
| --- | --- |
| `--file PATH` | Required for login, status and logout. Select a caller-owned private credential JSON file. Login requires a new file. |
| `--issuer URL` | Required for login; exact issuer URL. No environment fallback. |
| `--client-id ID` | Required for login; separately registered public native client, at most 256 UTF-8 bytes without whitespace or control characters. No environment fallback. |
| `--base-url URL` | Required login API origin, or `CATALOG_REMOTE_BASE_URL`. |
| `--ca-file PATH` | Optional API roots, or `CATALOG_REMOTE_CA_FILE`. |
| `--issuer-ca-file PATH` | Optional separate issuer roots. No environment fallback. |
| `--listen IP:PORT` | Login only; default `127.0.0.1:0`. `127.0.0.1` or `[::1]` with port 0-65535; zero selects a free port. |
| `--scope VALUE` | Repeat for additional distinct scopes; `openid` is automatic. Up to 16 values, each 1-256 printable ASCII bytes without whitespace, quotes or backslashes. |
| `--timeout DURATION` | Login, status or logout deadline; default five minutes, positive and at most ten minutes. |

Status and logout accept only `--file` and `--timeout`, apart from help. Their JSON output describes local credentials; status does not prove current access. See [credential formats and protected conversion](../operations/oidc-keycloak.md#prepare-private-http-credentials) before using curl, import, export or MCP. A private login JSON file is not a bare bearer-token file.

The private file binds the issuer, client, API origin, subject, tenant and membership. The CLI accepts an owned `0700` directory and owned `0600` regular files. It refuses symlinks and hard-linked credential files. Do not edit or commit this file. A persistent `.lock` file protects concurrent reads, refreshes and logout. Login requires a new file. Log out before you switch identity. Status and logout wait for an active login or refresh to release its lock. Logout deletes local credentials; it does not revoke issuer grants or browser sessions. Status makes no network request and cannot confirm current issuer or Catalog access.

Use `--issuer-ca-file PATH` for private issuer roots and `--ca-file PATH` for private API roots. These trust inputs are separate. Supply the issuer roots again with `--login-file` when refresh needs them. Issuer discovery, token and key endpoints must remain on the selected issuer origin. Redirects are refused. HTTPS is required, except for literal loopback IPs. No global TLS bypass is available.

Use the exact issuer URL, including a trailing slash when present. The CLI preserves it in callback checks, token verification and saved credentials. Discovery removes the terminal slash only when it constructs the metadata request path, as specified by [OIDC Discovery](https://openid.net/specs/openid-connect-discovery-1_0.html#ProviderConfigurationRequest). The API origin remains path-free and has no trailing slash. See [Auth0 CLI setup](../operations/oidc-auth0.md#register-the-human-cli-application) for its separate default-audience requirement.

Login defaults to a five-minute deadline, with a ten-minute maximum. Backchannel requests have a fifteen-second deadline and a 64 KiB response limit. Access and refresh token values have a 16 KiB limit. An access token needs a positive expiry of at most 24 hours. Remote commands refresh when access expires within 30 seconds. Refresh has a 30-second total deadline within the command's own deadline. A file lock serializes refresh. Replacement is atomic, including rotated refresh tokens. The CLI verifies any new ID token and requires the same human membership from Catalog before it saves renewed credentials. Refresh failure has no stale-token fallback. Sign in again after expiry, revocation, an identity change or an uncertain refresh result. A `401` response never causes a command replay. A failed mutation can still have an uncertain outcome; use its documented recovery procedure.

Refresh can omit an ID token. A supplied ID token must be a nonempty string and pass verification. Supplied nonce and authentication-time claims must preserve the original login values. A malformed response leaves the credential file unchanged and stops the selected operation.

`--login-file` is available on `remote`, import apply/resume and all export commands. Offline import preview needs no credentials. SDKs and the stdio MCP host retain their explicit bearer inputs. Do not combine a login file with `--token-file`, `CATALOG_REMOTE_TOKEN_FILE` or `CATALOG_REMOTE_TOKEN`, including inherited values. Without a login file, a selected token file still takes precedence over a token supplied through the environment. No cached credentials are selected automatically.

The flow follows [native-app OAuth](https://www.rfc-editor.org/rfc/rfc8252) and [OpenID Connect](https://openid.net/specs/openid-connect-core-1_0.html). See [sign-in setup](../operations/sign-in.md) for issuer registration and Catalog membership.

## Remote Catalog reads

The optional [assistant discovery Skill](../guides/assistant-discovery.md) uses only `remote capabilities`, `remote search` and `remote record`. It adds instructions, not a new transport or authorization layer.

`catalogctl remote` accesses an explicitly selected Catalog without a database DSN or server configuration. Reads use API bearer-token authorization and tenant boundaries. The separate `remote reindex` command requires tenant-scoped `operations.reindex` permission. The built-in Tenant Administrator template includes this permission. The assistant discovery Skill does not use this command. Build with `make build`; the same commands are available through the container's `catalogctl` entrypoint.

| Command | Result |
| --- | --- |
| `catalogctl remote capabilities` | Authenticated server capabilities, not the local binary version. |
| `catalogctl remote search --text <text>` | One page of local, indexed and eligible live results. |
| `catalogctl remote record --id <release-uuid>` | Published record including its resource, version, listing, release and evidence. Use `local.release.id` from Search, not its Resource UUID or prefixed Search ID. |
| `catalogctl remote job --id <job-uuid>` | One retained Job status. No polling, retry or cancellation. |
| `catalogctl remote source-compare --before-snapshot-id <uuid> --after-snapshot-id <uuid>` | One page of saved source membership changes. Requires current `source.read` permission. |
| `catalogctl remote reindex --reason <text> --idempotency-key <key>` | Enqueue a tenant-authorized search-index rebuild and return the Job. Add `--snapshot-id <uuid>` for one active public snapshot. Requires enabled semantic search; metadata is unchanged. |
| `catalogctl remote trust-request --source-id <uuid> --request-file <json> --idempotency-key <key>` | Request an assessment of one exact current source entry. Requires enabled verification and source administration/evidence permissions. |
| `catalogctl remote trust-result --source-id <uuid> --id <job-uuid>` | Read a completed private assessment, exact evidence and current applicability. Requires Job and source-evidence permission. |

Flags follow the command. For an existing HTTPS deployment:

```sh
export CATALOG_REMOTE_BASE_URL=https://catalog.example.com
export CATALOG_REMOTE_TOKEN_FILE=/run/secrets/catalog-access-token
./bin/catalogctl remote capabilities
./bin/catalogctl remote search --text orders --kind agent --tag support --limit 10
```

Replace the example hostname and secret-file path. Obtain a manual access token from the deployment's identity provider, or use the [human login procedure](#human-cli-login) and explicitly select its file. Manual bearer inputs have no automatic refresh. For a running local development instance, use `CATALOG_REMOTE_BASE_URL=http://127.0.0.1:8080` and its development token through `CATALOG_REMOTE_TOKEN`, with `CATALOG_REMOTE_TOKEN_FILE` unset. Reads do not seed data; use the [local walkthrough](../guides/local-development.mdx) for the separate mutating smoke workflow.

| Flag | Environment fallback | Default or bound |
| --- | --- | --- |
| `--base-url` | `CATALOG_REMOTE_BASE_URL` | Required origin without path, trailing slash, credentials, query or fragment. |
| `--token-file` | `CATALOG_REMOTE_TOKEN_FILE` | Optional regular file, at most 16 KiB; surrounding whitespace is trimmed. A selected file overrides `CATALOG_REMOTE_TOKEN` and fails if missing, empty or invalid. |
| None | `CATALOG_REMOTE_TOKEN` | Required when neither a token file nor a login file is selected; nonempty bearer value, at most 16 KiB. No token argument. |
| `--login-file` | None | Optional explicit human credentials; excludes every manual bearer source. Supports bounded refresh before the selected request. |
| `--issuer-ca-file` | None | Additional issuer roots for login-file refresh; requires `--login-file`. Separate from API trust. |
| `--ca-file` | `CATALOG_REMOTE_CA_FILE` | Optional PEM file, at most 1 MiB, adding roots only to this client's normal trust store. |
| `--max-response-bytes` | None | `record` only: 8,388,608 bytes by default; 1-67,108,864 bytes. Controls exact response retrieval, not server admission. |
| `--timeout` | None | 30 seconds; positive and at most 2 minutes. Covers connection, TLS, headers and body; Ctrl-C/SIGTERM cancels the request. |

Explicit flags override their environment fallbacks. These settings do not load `.env` and are not service runtime overrides or Make variables. HTTPS verifies the hostname with TLS 1.2 or later. HTTP is allowed only for literal loopback IPs, not `localhost`. Redirects are refused and proxy environment variables are not used. Responses default to 8 MiB and headers to 32 KiB. Search returns compact local metadata. For a larger exact record, use `remote record --id <release-uuid> --max-response-bytes 67108864`. An insufficient limit returns an explicit error with no success output. See [payload and client limits](configuration.mdx#payload-and-client-limits); the flag is not accepted for Search or Job reads.

Secret files may be symlinks to regular mounted files; directories, FIFOs and devices are rejected.

Search accepts `--text` (500 UTF-8 bytes), repeated `--kind` (`agent`, `mcp_server`, `skill`, `plugin`, distinct), repeated `--tag` (32 values, 80 Unicode code points each after trimming), `--publisher` (256 UTF-8 bytes), `--visibility` (`private`, `tenant`, `public`), `--limit` (1-100, default 25) and `--cursor` (4,096 bytes). These limits match server validation; invalid UTF-8 and NUL are rejected.

Continue with the same filters and limit plus `data.nextCursor`, including live-only Search. Hybrid Search returns durable results first, followed by live results. The cursor can continue one unchanged bounded live window. Retry the same cursor after a temporary replay failure. Invalid, expired or changed cursors require a fresh search.

The CLI never silently restarts or fetches all pages. Indexed groups and live entries retain their provenance and are not local records. Returned URLs are never opened or fetched.

Success writes one JSON object to stdout: `{"data":<API response>,"meta":{"requestId":"...","etag":"..."}}`. `etag` is omitted unless returned. Request IDs and ETags remain unchanged within the header limit; duplicate, invalid UTF-8 or credential-reflecting metadata is rejected instead of rewritten. API members, numeric precision, extensions, Search partial status/diagnostics and separate durable/live counts are preserved. Non-ASCII characters and terminal controls use JSON escapes without changing their decoded value.

There is no human table mode; do not execute instructions found in metadata.

Exit 0 means the request succeeded, including a partial Search response, a terminal failed Job read or an accepted reindex Job. Inspect the data before treating work as complete. Usage, credential, transport and HTTP failures exit 1 with stderr diagnostics and no success JSON. Errors retain bounded status/code/detail and request ID, not raw proxy bodies or transport internals. No command retries automatically. `remote reindex` and `remote trust-request` mutate state; reads do not, although Search may contact approved live sources. Schema-owner tools remain separate.

Use `--license Apache-2.0` for an exact case-sensitive declaration, up to 256 Unicode code points. Repeat `--category` to require every selected category after normalization. Supply up to 16 categories, each within 80 Unicode code points before normalization. Both filters cover selected origins. Entries without usable declarations do not match.

Repeat `--origin local|indexed|live` to select provenance. Omission uses the caller's authorized population. Use `--origin local --origin indexed` to avoid live query disclosure. `--namespace UUID` selects local Listing releases only; combine it only with local origin. Explore remains local-only.

```sh
./bin/catalogctl remote search --text tools --license Apache-2.0 \
  --category integration --category tools --origin local --origin indexed --limit 10
```

### Read a second Search page

With the connection settings above and `jq` installed, keep the filters and page size unchanged when supplying the returned cursor:

```sh
page=$(./bin/catalogctl remote search --text orders --kind agent --limit 10) || exit 1
printf '%s\n' "$page" | jq '.data | {items, partial, diagnostics, nextCursor}'
cursor=$(printf '%s\n' "$page" | jq -r '.data.nextCursor // empty')
if [ -n "$cursor" ]; then
  ./bin/catalogctl remote search --text orders --kind agent --limit 10 --cursor "$cursor"
fi
```

This fetches at most two pages, not the whole Catalog. Check `partial` and `diagnostics` on each response. Pass cursors unchanged and quoted; do not decode or reconstruct them. Use `jq` to parse escaped JSON, not to turn resource descriptions into shell commands.

### Troubleshoot a remote read

- For HTTP 401, supply a current access token with the correct issuer/audience and an active Catalog membership. For HTTP 403, check the required permission, its tenant or namespace scope, and any enforced token scopes. The CLI cannot grant access. Manual bearer credentials require external renewal; explicit login files support the bounded refresh described above.
- For TLS failures, check the origin hostname and configure `--ca-file` if the deployment uses a private CA. Point directly at the API origin: redirects and insecure certificate bypasses are not supported.
- For HTTP 429 or 503, retain the request ID and inspect the deployment's limits or dependency health before retrying explicitly. The CLI does not retry, even when `Retry-After` is present.
- For token-file failures, fix the selected file or explicitly clear its setting before using an environment token. A broken or empty file never falls back to another credential. Each invocation rereads the file, including a rotated symlink target; server cookies are not retained.

### Request reindexing and inspect its Job

Use this mutation only with tenant-scoped `operations.reindex` permission and enabled semantic search. If OAuth scope enforcement is active, the token also needs `tenant.admin`. Reads need their own current permissions.

`--reason` must be nonblank valid UTF-8, at most 2,000 bytes. `--idempotency-key` requires 8-128 ASCII characters in the range 33-126, without spaces. Optional `--snapshot-id` selects one active public snapshot by UUID; omission selects the tenant's control search index.

With the connection settings above and `jq` installed, submit one reviewed request and read its Job:

```sh
accepted=$(./bin/catalogctl remote reindex \
  --reason 'Rebuild the reviewed search scope' \
  --idempotency-key 'reindex-reviewed-change-01') &&
job_id=$(printf '%s\n' "$accepted" | jq -er '.data.id') &&
./bin/catalogctl remote job --id "$job_id"
```

Choose a unique key for a new logical request. Retain the same key and inputs when you reconcile an uncertain outcome; follow [command retry rules](../guides/integrating.mdx#retry-and-reconcile-uncertain-outcomes). The Job read is one status check, not a wait for completion. Reindexing changes derived search state, not resource metadata. Follow [hybrid-search operations](../operations/hybrid-search.mdx) for progress, failure and recovery.

### Remote ANS assessment commands

Create a UTF-8 JSON selection file, limited to 64 KiB. Copy binding fields from an administrator live result or exact indexed occurrence. Use `observationId` instead of `text` for an indexed entry. Omit `version` when `versionPresent` is false.

```json title="selection.json"
{
  "sourceRevision": 1,
  "identifier": "urn:air:example.com:agents:weather",
  "versionPresent": true,
  "version": "1.0.0",
  "mediaType": "application/a2a-agent-card+json",
  "delivery": "url",
  "exactEntryDigest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "text": "weather",
  "reason": "Check the selected registration"
}
```

The identifier and digest above are examples. Replace them with the exact selected values.

```sh
./bin/catalogctl remote trust-request --source-id "$SOURCE_ID" \
  --request-file selection.json --idempotency-key "$IDEMPOTENCY_KEY"
./bin/catalogctl remote job --id "$JOB_ID"
./bin/catalogctl remote trust-result --source-id "$SOURCE_ID" --id "$JOB_ID"
```

These commands use the existing remote URL, token, CA and timeout inputs. They do not load server configuration or access PostgreSQL. A 202 response means queued work, not successful verification. Result output uses the normal `data`/`meta` envelope. No command polls or retries automatically. See [assessment operations](../operations/federation.mdx#request-an-ans-registration-assessment).

### Compare saved source synchronizations

Select two complete snapshot UUIDs from the same source revision. The command reads saved occurrence membership without contacting the source or changing search results:

```sh
./bin/catalogctl remote source-compare \
  --before-snapshot-id "$BEFORE_SNAPSHOT_ID" \
  --after-snapshot-id "$AFTER_SNAPSHOT_ID" --view changes --limit 25
```

The default view is `changes`; use `--view all` to include unchanged entries. Each invocation reads one page. Continue explicitly with the returned `data.nextCursor` through `--cursor`, keeping the same pair, view, limit and identity. Cursors are opaque and limited to 2,048 bytes. A refused continuation is an error; the command does not retry or silently restart it.

The response retains one-sided and multiple occurrences, missing versus explicitly empty versions, and snapshot withdrawal-authority flags. Absence does not establish upstream deletion. The comparison does not fetch raw entries; those retain separate evidence permissions. See [saved synchronization comparison](../operations/federation.mdx#compare-two-saved-synchronizations) for eligibility, permissions and interpretation.

## Use the Catalog from an MCP host

Run `catalogctl mcp serve` for read-only Catalog tools over stdio. Configure your MCP host to start the process with an explicit API origin and bearer token. Stdout carries MCP messages; safe diagnostics use stderr.

Follow the [Catalog MCP server guide](../guides/mcp-server.mdx) for host setup, tool examples, permissions, pagination, connection settings, limits and troubleshooting. The command does not expose a remote MCP HTTP endpoint or invoke discovered resources.

## Export a native document

Use `catalogctl export` to save one representation from an exact governed release. Select the release ID from a local Search result. This command uses the same API origin, token and CA inputs as `remote`. It does not load service configuration or connect to PostgreSQL.

```sh
./bin/catalogctl export \
  --release-id "$RELEASE_ID" \
  --output-dir ./selected-release
```

If the release has several representations, add `--representation-id "$REPRESENTATION_ID"`. Use `remote record --id "$RELEASE_ID"` to inspect their IDs. The command does not select the newest version or choose a representation by name.

The output directory must not exist. Its parent must exist, and no ancestor can be a symbolic link. The command creates a private directory and private files. Existing paths are never overwritten. Choose a new directory for another export.

| Disposition | Files and meaning |
| --- | --- |
| `exact_document` | `native.bin` contains exact retained document bytes. `export.json` binds the selected release, revisions, representation, media type and profile to those bytes. |
| `inline_json` | `native.json` contains the raw JSON value received through the record API. Its file checksum and size are separate from the recorded document digest and size. |
| `reference_only` | `export.json` contains the available Skill or Plugin inventory, inspection documents, package references and archive digest. No archive is downloaded. |

Artifact downloads require current `resource.read_content` permission and the endpoint's OAuth scope. The command checks the artifact ID, `Content-Digest`, recorded digest, media type and all size values before it saves content. Inline JSON uses the record's existing disclosure rules. The API or PostgreSQL can change its byte layout, so it is not an exact retained-byte download or the original upload.

`export.json` is the companion metadata file and has `format: 1`. Its `recorded` section describes the retained document or external archive. Its optional `file` section describes the exported file. Each section includes a digest, size and explicit scope. A Git commit or OCI digest identifies immutable content; an HTTPS archive URL is bound to its recorded digest but can change upstream.

A standalone `SKILL.md` is a document, not a complete Skill tree. Retained Plugin documents are not the complete Plugin archive.

Export does not fetch declared URLs, clone Git repositories, pull OCI images, reconstruct archives or execute resources.

`export.json` is the completion marker and is written last. A directory without it is incomplete. Normal failures remove the command's partial files. A process or machine crash can leave an incomplete directory; remove it or select a new output path. Completed files remain available if the command cannot write its final stdout result.

The whole export defaults to a 30-second deadline. Set `--timeout` to a positive duration up to two minutes. Record and content limits each default to 8 MiB. Set `--max-response-bytes` or `--max-content-bytes` to a value from 1 to 67,108,864 bytes when needed. Header limits, verified TLS, redirect refusal and credential handling follow the existing remote transport. There are no automatic retries.

Exit 0 means the selected document or references were saved. Stdout contains a small JSON result with the release ID, representation ID and disposition. A `reference_only` result does not claim a complete package. Failures exit 1 with safe stderr diagnostics. Export does not change the Catalog and is not a tenant backup. See [native document handoff](../guides/integrating.mdx#export-native-documents) for API use.

### Export an MCP client configuration

Use a current local MCP Server release with a published or deprecated state. Export one literal HTTPS connection for VS Code, Claude Code or Cursor.

```sh
./bin/catalogctl export --format mcp-vscode \
  --release-id "$RELEASE_ID" \
  --output-dir ./mcp-connection
```

Use the same API credentials and trust inputs as native export. Select `--representation-id "$REPRESENTATION_ID"` when several representations exist. The selected document must use the pinned MCP Registry or experimental Server Card profile.

Add `--remote N` when the document declares several remotes. Positions start at 1 and follow the original `remotes[]` array. For example, `--remote 2` selects `remotes[1]`. Unsupported entries retain their positions; the command never substitutes a different connection. A missing or invalid selection reports positions and transport labels without printing endpoint URLs.

| Format | Exported file | Client project location | Configuration |
| --- | --- | --- | --- |
| `mcp-vscode` | `mcp.json` | `.vscode/mcp.json` | `servers` entries with `type` and `url`. |
| `mcp-claude-code` | `.mcp.json` | `.mcp.json` | `mcpServers` entries with `type` and `url`. |
| `mcp-cursor` | `mcp.json` | `.cursor/mcp.json` | `mcpServers` entries with `url`. |

Streamable HTTP maps to client `http`; SSE maps to `sse` where a `type` field is used. Cursor's documented remote format uses the URL without a `type` field. Each file contains one entry named `catalog-<release UUID>`.

The command writes into a new private output directory. It does not find or modify client settings. Review the file, then place or merge its entry in the selected client configuration. Rename the entry if needed to avoid a collision. Follow the [VS Code](https://code.visualstudio.com/docs/agents/reference/mcp-configuration), [Claude Code](https://code.claude.com/docs/en/mcp) or [Cursor](https://cursor.com/docs/mcp) instructions for the client setup.

Header values and defaults are omitted, including values marked as non-secret. The private `export.json` sidecar lists omitted header names and required flags. Configure the headers, target authentication and certificate trust in the client. The Catalog API token and CA file are not exported. Missing header metadata does not establish anonymous access. The exporter does not create OAuth clients or insert credential placeholders.

The sidecar identifies the client format, original remote position and declared transport. Its `recorded` section retains the source document digest. Its `file` section records the generated configuration checksum with scope `generated_client_configuration`. The configuration is derived output, not the original native document.

Artifact-backed documents retain the native export permission, audit and integrity checks. Inline JSON uses the existing record disclosure rules. Native content is parsed locally; external document URLs and MCP endpoints are never fetched.

The command rejects HTTP endpoints, URL credentials, fragments, unresolved templates and client substitutions. It preserves accepted URL query strings and percent encoding. A path or query can contain sensitive values, so review private output before sharing. Client formats support single-record export only. Package/stdio metadata remains available through native export and the VS Code extension.

Opening the configuration in a client can initiate connections. Export alone does not connect, install packages, execute tools or grant permission to use the server.

The [browser MCP handoff](../guides/understand-results.mdx#download-an-mcp-client-configuration) provides the same connection formats with a preview.
Browser downloads use `mcp.json`; rename the Claude Code download to `.mcp.json`.
CLI output retains each client's filename directly.

### Export a reusable draft JSON file

Use `--format draft` to save all representations and optional Listing metadata in one reusable JSON file.

Export a release with its exact Listing metadata:

```sh
./bin/catalogctl export --format draft \
  --release-id "$RELEASE_ID" --output-dir ./reusable-release
```

Export an exact VersionRevision without Listing metadata:

```sh
./bin/catalogctl export --format draft \
  --version-revision-id "$VERSION_REVISION_ID" --output-dir ./reusable-version
```

Add `--listing-revision-id "$LISTING_REVISION_ID"` to include that exact Listing revision. It must reference the selected VersionRevision and the same Resource, Namespace and tenant. Do not combine a release selector with either revision selector. Draft mode rejects representation selectors because it exports every representation.

The API operation is `GET /api/v1/version-revisions/{revisionId}/export`, with optional `listingRevisionId`. It requires current `resource.read` and `resource.read_content` permission in the Resource's Namespace. Enforced OAuth scopes also require `catalog.read`. Exact reads do not need `resource.read_history`. The operation records required private-read audits before it returns content. Audit failure returns no attachment.

The private output directory contains `draft.json` and the final `export.json` completion marker. The sidecar records source identities and the content-set digest. Its `file` checksum covers the reusable file's bytes. That checksum is separate from any retained native document or package digest. The reusable file itself contains no source UUIDs, Publisher identity, approval or release evidence.

The file uses the `open-agentic-catalog-draft-v1` format. It preserves retained native text, unknown native fields and numeric tokens. Inline content preserves stored JSON text; PostgreSQL can change its layout from the original upload. Readable drafts with validation findings can export, including unfinished native text. Export does not validate that text or make it ready to save.

Standalone Skills contain their retained document. Inspected Skill packages contain an immutable handoff and need separately supplied package content. Plugin archive references retain their URL and checksum. Export does not retrieve those archives or refetch native source URLs. Missing retained document content causes an error.

Stdout and the sidecar report `browserCompatible`. This field checks browser text-size limits only. It does not establish native validity, complete package inputs, destination permissions or readiness to save. The enclosing file ceiling is 64 MiB. Native text has a separate 16 MiB aggregate limit; each raw browser editor has a 2 MiB limit.

The response and content limits each default to 8 MiB. Increase both limits when a valid file needs more space:

```sh
./bin/catalogctl export --format draft \
  --version-revision-id "$VERSION_REVISION_ID" \
  --max-response-bytes 67108864 --max-content-bytes 67108864 \
  --output-dir ./large-reusable-version
```

Use the [import commands](#import-a-reusable-draft-json-file) to load this file into a destination Catalog. The [transfer tutorial](../guides/draft-transfer.mdx) covers both UI/CLI directions, all four kinds, separate package inputs and recovery.

## Import a reusable draft JSON file

Use the same `import preview`, `apply` and `resume` commands for `open-agentic-catalog-draft-v1` JSON. Native `format: 1` manifests retain their separate batch behavior.

Choose a destination Namespace for a new Resource. Supply an exact Resource ID to add a new Version instead. The CLI checks its kind, canonical identifier, tenant and Namespace. It does not find a Resource by name, merge a collision or rewrite relationship targets.

```sh
./bin/catalogctl import preview --file ./reusable-version/draft.json \
  --namespace-id "$NAMESPACE_ID" --content-only
./bin/catalogctl import apply --file ./reusable-version/draft.json \
  --namespace-id "$NAMESPACE_ID" --content-only \
  --state-file ./private-import/state.json \
  --base-url "$CATALOG_URL" --token-file "$TOKEN_FILE"
```

Set the variables to your destination inputs. Preview needs no credentials or running Catalog. It reads confined local files and runs pinned native validators. It reports local findings without fetching document URLs, declared endpoints or remote contexts. Exact targets, permissions, Listing lookup, ETags and server limits remain pending.

| Reusable-file flag | Meaning |
| --- | --- |
| `--namespace-id UUID` | Required for a new Resource. For an existing Resource, an optional confirmation that must match its Namespace. |
| `--resource-id UUID` | Create a new Version of this exact Resource. The file must use its kind and canonical identifier. |
| `--publisher-id UUID` | Required when the file includes Listing metadata. Choose the destination Publisher explicitly. |
| `--content-only` | Omit supplied Listing metadata. No Publisher is required. |
| `--listing-id UUID` | Use this exact destination Listing with `--resource-id`. Otherwise, require a complete zero-or-one Listing lookup. |
| `--package-file N=PATH` | Supply a separate Skill archive for representation N, starting at 1. |
| `--package-directory N=PATH` | Supply a Skill directory for representation N. The importer creates a bounded ZIP with executable modes. |

Package paths are relative to the draft file's directory. Symbolic links and escapes are rejected. Each package representation needs one file or directory and a valid immutable handoff. Missing inputs fail before writes. The file excludes package bytes and local paths. These destination and package flags do not apply to native manifests.

Listing metadata is included by default when present. Use `--content-only` to omit it. A missing Publisher argument fails before writes. Publisher existence and metadata eligibility remain subject to normal admission, which can fail after the Version commits. Publisher browsing permission is not required for an exact ID. Artwork declarations use the destination's normal validation and sealing.

```sh
./bin/catalogctl import apply --file ./reusable-version/draft.json \
  --resource-id "$RESOURCE_ID" --listing-id "$LISTING_ID" \
  --publisher-id "$PUBLISHER_ID" --state-file ./private-version/state.json \
  --base-url "$CATALOG_URL" --token-file "$TOKEN_FILE"
```

Choose a new Version label and keep any native version fields consistent before preview. The CLI refuses an existing label. An explicit Listing must belong to the selected Resource, tenant and Namespace. An automatic lookup must return a complete empty result or one Listing. Lookup failure, ambiguity and mismatched parents stop the import before writes.

| Destination plan | Writes |
| --- | --- |
| New Resource, content only | Resource and Version, two writes. |
| New Resource with Listing | Resource, Version, Listing and Listing revision, four writes. |
| New Version, content only | Version, one write. |
| New Version with a new Listing | Version, Listing and Listing revision, three writes. |
| New Version with an existing Listing | Version and Listing revision, two writes. |

Creation requires `resource.create`. All plans require `resource.edit`, `resource.read` and `resource.read_content` in the destination Namespace. The registered `resource.edit` permission also requires `resource.read_history`. Automatic Listing lookup uses that history permission. An exact Listing avoids the collection request. Enforced OAuth scopes require `catalog.read` and `catalog.write`.

Apply captures an existing Listing's strong ETag before writes. Resume preserves that ETag, exact targets, input checksums and original command keys. It does not repeat the Listing lookup or select a newer ETag. A changed ready Listing command is refused. An uncertain command replays its original request within the existing replay window.

```sh
./bin/catalogctl import resume --file ./reusable-version/draft.json \
  --namespace-id "$NAMESPACE_ID" --content-only \
  --state-file ./private-import/state.json \
  --base-url "$CATALOG_URL" --token-file "$TOKEN_FILE"
```

Use the original flags, file, package inputs and private receipt. A completed resume verifies exact retained records without writes. A blocked 201 response retains its IDs and validation evidence. It stops before Listing preparation. Preserve partial progress after failure; there is no automatic retry or rollback.

URL-only content is pending server retrieval, not locally validated content. Normal admission retrieves selected URLs through the hardened fetcher. Plugin archives retain their expected checksum; different bytes cannot replace the exported archive. Editable Plugin preparation needs separate packaging and an HTTPS archive reference before apply. The CLI does not construct or host a Plugin archive.

Reusable files have a 64 MiB encoded ceiling and a separate 16 MiB native-text budget. Request limits include JSON and package base64 overhead. Exact target reads use the separate response limit. Existing report, receipt and deadline limits apply. See [apply and recovery](../guides/resource-authoring.mdx#apply-and-resume-a-native-draft) for private-directory modes and outcome dispositions.

### Preview a local selection

Save the exact release IDs before you download their documents:

```sh
./bin/catalogctl export preview \
  --query search --kind agent --tag example \
  --namespace "$NAMESPACE_ID" \
  --limit 25 --max-pages 10 --max-records 64 \
  --output-dir ./export-selection
```

The preview writes a private `selection.json` and prints the same selection as JSON. It does not download native documents. Each item records the tenant, resource, version, release and revision IDs. Versions remain separate; the command does not choose the newest resource with a matching name.

Search always sends `origins: [local]`. It supports `--text`, repeatable `--kind`, `--tag` and `--category`, plus `--license`, `--publisher`, `--namespace` and `--visibility`. It cannot query indexed or live peers.

Use `--query browse` for the governed Browse API. Browse supports one `--kind`, `--namespace`, `--visibility` and the same page limits. It does not accept Search-only filters.

The page size stays unchanged during continuation. The command stops at the configured page or record limit. A bounded preview can therefore contain fewer records than the query matches. Its `complete` field is false, and `stopReason` identifies `page_limit`, `record_limit` or `server_partial`. This preview exits 1 but retains the selected items for inspection.

Malformed responses, repeated cursors and transport failures do not produce a saved selection. An omitted or empty continuation cursor ends the results. An explicit null or non-string cursor is invalid; it cannot mark the preview complete.

Inspect `selection.json` before you use it. You can remove unwanted items. For native export, add an exact `representationId` when an item has several representations. Obtain that ID from `remote record`. For draft export, omit every `representationId`. Do not change frozen identity fields. The selection contains no credentials, but its private resource identities can be sensitive.

### Export several releases

Use the saved selection without repeating the query:

```sh
./bin/catalogctl export batch \
  --selection-file ./export-selection/selection.json \
  --output-dir ./exported-releases
```

Alternatively, select release IDs directly:

```sh
./bin/catalogctl export batch \
  --release-id "$FIRST_RELEASE_ID" \
  --release-id "$SECOND_RELEASE_ID" \
  --output-dir ./exported-releases
```

Choose either repeated release IDs or a selection file. Each input accepts at most 64 selectors before deduplication. The command removes duplicate selections of the same release. It preserves different releases that share document bytes. Conflicting selections for one release are rejected.

Native mode accepts `--representation-id` with one explicit release. It also accepts per-item `representationId` fields in a saved selection. Add `--format draft` to export reusable files instead. Draft mode rejects both types of representation selector.

```sh
./bin/catalogctl export batch --format draft \
  --selection-file ./export-selection/selection.json \
  --output-dir ./reusable-releases
```

The batch saves `selection.json` before it reads any release. It creates a private subdirectory named by each release UUID. Each successful subdirectory uses the single-export files and completion marker described above. The final `batch.json` records every selected item:

| Outcome | Meaning |
| --- | --- |
| `complete` | The selected native document or reusable draft file and its sidecar were saved. |
| `reference_only` | References and inspection evidence were saved; the external archive was not downloaded. |
| `denied` | The API refused current authentication or permission. |
| `failed` | A read, identity check, integrity check or file operation failed. `code` gives a fixed failure category. |
| `not_attempted` | Cancellation, the deadline or the aggregate byte limit prevented the read. |

The API checks current access on every request. A preview does not preserve access after permission changes. Missing releases report `not_found`; a record that disagrees with frozen identities reports `selection_changed` before any artifact download. The command continues after independent item failures while its deadline and byte budget permit.

A batch exits 0 only when every selected item succeeds and the selection is complete. `reference_only` is a successful evidence export, not a complete package. An incomplete preview remains incomplete in the batch report, even if all selected items export successfully. Failures exit 1 after saving the available report and successful files.

Both `selection.json` and `batch.json` use `format: 1`. The batch report's `selectionComplete` field retains the preview's completion status. Its `complete` field also requires every selected export to succeed. Keep the report and per-release sidecars when you inspect partial results.

A missing `batch.json` means the invocation did not finish. Inspect per-release `export.json` markers to find completed exports. Completed files survive later item failures or a broken stdout consumer. Select a new output directory for another attempt; batch export has no resume or overwrite mode.

### Selection and batch limits

| Flag or input | Default | Maximum |
| --- | --- | --- |
| `--timeout` | 30 seconds for the whole invocation | 2 minutes |
| `--limit` | 25 items per preview page | 100 |
| `--max-pages` | 10 preview pages | 100 |
| `--max-records` | 64 unique releases | 64 |
| `--max-response-bytes` | 8 MiB per record or page | 64 MiB |
| `--max-content-bytes` | 8 MiB per native document in a batch | 64 MiB |
| `--max-total-bytes` | 64 MiB across response bodies | 256 MiB |
| Selection file and encoded selection/report | 1 MiB | 1 MiB |

The aggregate budget counts bytes read from successful and failed HTTP responses. One additional probe byte can detect overflow. Headers have a separate fixed limit. Preview and batch have independent budgets; `responseBytes` in `batch.json` reports that batch's body reads. Files and sidecars have their own existing limits.

Ctrl-C or SIGTERM cancels the export. After cancellation or deadline expiry, the batch uses a separate five-second deadline to save its final report. This allowance permits no further release reads, document downloads or per-release output. A crash or forced termination can prevent the report from being saved.

The selection is bound to one API origin. It does not snapshot the tenant or lock its records. Independent reads are not a point-in-time backup. Use the [database backup procedure](../operations/backup-restore.mdx) for retained-state recovery.

## Offline ANS registration verification

Use `catalogctl trust verify-bundle` to check explicitly selected local evidence. It requires no running Catalog, database, runtime configuration or network access.

### Prepare evidence and authority

Prepare a JSON bundle from previously captured evidence. Set `profile` to `ans-registration-v1`, which supports the pinned ANS V1/V2 agent events. Encode each byte field below as a JSON base64 string. Preserve exact signed bytes, including the checkpoint's final newline. Do not replace signed documents with reduced or reformatted JSON.

| Bundle field | Exact content | Decoded byte ceiling |
| --- | --- | ---: |
| `entry` | The selected Finder entry JSON, including `identifier`, `displayName`, `version`, `metadata.ansName`, `metadata.logId` and its Trust Manifest. | 256 KiB |
| `receipt` | The COSE registration receipt. | 256 KiB |
| `checkpoint` | The raw signed checkpoint text. | 32 KiB |
| `checkpointHistory` | Optional history object for the proof's exact tree when the latest checkpoint has advanced. | 256 KiB |
| `statusToken` | The COSE current-status token. | 64 KiB |

The complete encoded bundle has a 1 MiB ceiling, including JSON and base64 overhead. Field names are case-sensitive. Unknown or duplicate bundle fields, malformed JSON and over-limit input are rejected. Missing evidence cannot establish a current registration. Sparse discovery entries can remain valid for discovery while lacking the required assessment fields.

Obtain registration-authority and log public keys through an independently trusted operator channel. Confirm their complete SHA-256 SPKI fingerprints. Each file must contain one P-256 SPKI PEM `PUBLIC KEY`, with a 4 KiB ceiling. Private keys, evidence-supplied keys and remote key discovery cannot establish accepted authority. Keep these keys separate from TLS CA bundles.

Create a local policy file with the following shape. Replace the example authority names, domains and key paths with your accepted inputs:

```json title="offline-policy.json"
{
  "profile": "ans-registration-v1",
  "registrationAuthority": "company-ra",
  "logOrigin": "company-log",
  "evidenceOrigin": "https://ans.example.com",
  "publisherDomains": ["publisher.example.com"],
  "registrationKeys": [{"id": "ra-signer", "file": "ra.pem"}],
  "logKeyFiles": ["log.pem"],
  "maxStatusAgeSeconds": 3600,
  "futureSkewSeconds": 60
}
```

`registrationAuthority` and each registration key ID must match the signed producer fields. `logOrigin` is the signed checkpoint name, not a URL. Authority names and registration key IDs use 1-128 ASCII letters, digits, periods, underscores or hyphens. `evidenceOrigin` is one exact HTTPS origin without a path, query or credentials. The entry's single `ANS-Registration` reference must equal that origin plus `/v1/agents/<signed-ansId>/receipt`. Its media type must be `application/scitt-receipt+cose`.

Use 1-64 distinct lowercase exact `publisherDomains`; wildcards are unsupported. This policy accepts the authority's statements for those domains; it does not prove domain ownership. Supply 1-8 keys per role. Duplicate keys within a role, duplicate registration key IDs and colliding log-key hints are rejected.

Relative key paths resolve beside the policy file. The policy has a 64 KiB ceiling. Bundle, policy and key inputs must be regular local files; symbolic links and special files are rejected.

| Offline policy setting | Default | Allowed values |
| --- | --- | --- |
| `maxStatusAgeSeconds` | 3,600 | Integer 1-86,400; omission or zero selects the default. |
| `futureSkewSeconds` | 60 | Integer 0-60; explicit zero permits no future issuance. |

These numeric fields belong to the offline policy. Runtime configuration uses the separate duration fields `maxStatusAge` and `futureSkew`. Future skew never extends signed expiry. Status age and expiry are exclusive upper bounds; registration and certificate validity can shorten the result's validity.

### Run and interpret an assessment

```sh
./bin/catalogctl trust verify-bundle \
  --file internal/standards/anstrust/testdata/native/v1.json \
  --policy internal/standards/anstrust/testdata/native/policy.json \
  --at 2026-10-09T14:31:30Z
```

This source-checkout example uses archived synthetic evidence, public keys and its capture time. It does not establish a current real-world registration. Omit `--at` to use the current UTC time. Build the command first with `make build`. For your own evidence, replace the file and policy paths with your prepared inputs.

| Flag | Meaning |
| --- | --- |
| `--file` | Required bounded JSON bundle. Its fields carry exact evidence bytes as base64. |
| `--policy` | Required local policy with authority names, exact publisher domains and public-key file paths. Relative paths resolve beside this file. |
| `--at` | Optional RFC 3339 assessment time. The result reports this time explicitly. |

The command writes one JSON assessment to stdout, bounded to 16 KiB. Exit zero requires `currentRegistration: true`. Negative, unsupported or incomplete evidence produces JSON and exit 1. Invalid files or policy produce only safe stderr and exit 1.

Results separate receipt, producer and log signatures, checkpoint, inclusion, target matching and current status. Each check has a fixed `code` and a `state`: `valid`, `invalid`, `unavailable`, `unsupported` or `not_assessed`. A check that cannot run after a failed prerequisite uses `prerequisite_failed`. Inspect these fields even when a command exits successfully.

The result includes entry and policy digests, assessment time and accepted key fingerprints. `logKeyFingerprint` identifies the receipt key; `envelopeLogKeyFingerprint` identifies the outer log-envelope key. These can differ during accepted key overlap. `validUntil`, when present, is bounded by freshness and signed expiry. Only an eligible event with a matching target and valid `ACTIVE` status establishes a current registration. Historical signatures can remain valid after current status expires.

Checkpoint inclusion proves one authenticated tree, not global log consistency. Registration verification authenticates neither endpoint nor artifact bytes. It grants no approval and changes no Catalog trust or rights state. This command does not acquire evidence or retain assessments. Use [runtime configuration](configuration.mdx#ans-registration-verification), [authority operations](../operations/security.md#ans-verification-authorities) and [source assessment operations](../operations/federation.mdx#request-an-ans-registration-assessment) for the enabled API/worker workflow.

## Operator commands

| Command family | Scope and owning procedure |
| --- | --- |
| `catalogctl version`, `--help` | Read-only binary identity and command summary. |
| `catalogctl config validate --config <path> --role <role>` | Offline strict config validation for `api`, `worker`, `all` or `migration`; not dependency qualification. |
| `catalogctl profiles verify` | Read-only verification of the embedded pinned profile bundle, not validation of an arbitrary user document. |
| `catalogctl diagnostics` | Bounded safe diagnostics from a newly constructed runtime; use [observability](../operations/observability.mdx) for exact inputs and meaning. |
| `catalogctl probe` | Bounded loopback-only HTTP probe, including private worker health; not a product client. |
| `catalogctl migrate plan`, `migrate apply` | Read-only planning versus owner-only forward migration and exact role-grant mutation; [database roles](../operations/database-roles.mdx). Optional `--semantic` includes the separate derived schema and requires administrator-provisioned pgvector 0.8.7; it does not enable semantic search. |
| `catalogctl identity provision` | Owner-authorized production realm/principal/membership provisioning; [database roles](../operations/database-roles.mdx). |
| `catalogctl identity provision-development` | Explicit developer identity setup only. |
| `catalogctl identity realms list`, `suspend`, `reactivate` | Bounded realm inventory and operator-controlled lifecycle; read-only list versus attributed mutations. |
| `catalogctl restore verify` | Read-only isolated, paginated, per-tenant integrity checks; [backup and restore](../operations/backup-restore.mdx). |
| `catalogctl audit verify-export`, `bind-manifest`, `compare-restore` | Offline file verification, local manifest binding and read-only restored-range comparison; [audit](../operations/audit.md). Binding does not create independent provenance. |
| `catalogctl repair idempotency list`, `plan`, `apply` | Inspection and digest-bound lock-checked repair; [idempotency repair](../operations/idempotency-repair.mdx). Only apply mutates. |
| `catalogctl repair search-projections` | Owner-only bounded derived-state repair; [search projection repair](../operations/search-projection-repair.mdx). |
| `catalogctl repair webhook-secrets` | Owner-only bounded ciphertext re-encryption with all runtimes stopped; [webhooks](../operations/webhooks.mdx). |
| `catalogctl bootstrap` | Creates development state in its own runtime; not production identity provisioning. |
| `catalogctl smoke` | Mutating governed workflow for deliberately selected disposable environments; not a production health check. |
| `catalogctl seed-demo --base-url URL [--data-file YAML] [--state-file PATH] [--archive-origin HTTPS_ORIGIN] [--archive-trust-file PEM] [--sample-set PROFILE] [--extend] [--timeout DURATION]` | Load the shared YAML and native assets through governed APIs. Explicitly append new keys or adopt an exact original receipt, or verify a completed receipt without writes. Curated is the default (fourteen Resources/twelve published); minimal/expanded preserve original fixtures. Omitted `--data-file` uses embedded defaults. Public CA defaults to `output/demo/archives/cert.pem`, receipt to `output/demo/catalog-demo-v1.json`, deadline five minutes (maximum ten). Owned loopback development API only; no production credentials/runtime invocation. See [the populated demo](../guides/local-development.mdx#populated-development-catalog). |

One `samples/catalog.yaml` and exact native assets supply preview, preparation and API inputs. `make demo-check` validates `DEMO_DATA_FILE` and previews unverified receipt hints locally; `demo-archives-prepare` publishes immutable archives/allowlist without Catalog writes. `demo-seed` passes the selected file/profile/public trust to the API loader. `demo-up` explicitly combines those steps with existing Compose UI/archive startup and bounded API readiness. Plain `compose-up` never seeds.

Only `--extend` authorizes adoption/additions; prior bound entries cannot be edited/removed, and completed reruns need no archives. See [sample loading and recovery](../guides/sample-catalog.mdx).

The default `curated` profile is available to validation/preparation/API loading. `make demo-runtimes-prepare` verifies installed locked A2A/MCP tools and prepares stable private inputs; `make compose-up LOCAL_DEMO_RUNTIMES=on` starts the two fixed peers without invocation/seeding. `make demo-runtimes-test` exercises real clients against selected running loopback peers. `make test-demo-runtimes` separately owns a disposable lifecycle, denial/TLS controls, restart/privacy checks and cleanup. These are development fixture commands, not Catalog execution capabilities. Follow the [sample procedures](../guides/sample-catalog.mdx) for tool acquisition, configurable ports, separate credentials, source-only examples and qualification limits.

### Configuration, diagnostics and probes

| Command | Inputs and limits |
| --- | --- |
| `config validate` | Optional `--config`; `--role` defaults to `api`. Accepted roles: `api`, `worker`, `all`, `migration`. Validates configuration and required secret inputs without connecting to dependencies. |
| `diagnostics` | Optional `--config`; `--role` defaults to `api` and accepts only `api` or `worker`. Builds and closes a new runtime; PostgreSQL mode opens the selected role's database connection. Output is bounded to 256 KiB. |
| `probe` | Required `--url`: literal loopback HTTP IP, numeric port and `/startupz`, `/livez` or `/readyz`. `--expect-status` defaults to 200; only 200 and 503 are accepted. Deadline: 2.5 seconds; body limit: 4 KiB. No redirects or proxies. |

`diagnostics` does not inspect another process's memory or counters. A report with `status: degraded` can still return exit status 0. See [operator diagnostics](../operations/observability.mdx#operator-diagnostics) for the report's scope.

### Database operator inputs

These commands load `--config` plus backend environment overrides for the migration role. Select the direct schema-owner connection for the named target. Do not use API, worker or pooler credentials. Follow the linked runbooks for runtime drain and restore isolation requirements.

| Action | Required inputs and relevant defaults |
| --- | --- |
| `migrate plan`, `apply` | `--api-role` defaults to `catalog_api`; `--worker-role` to `catalog_worker`. `--semantic` defaults to false. Apply holds the migration lock through forward migrations and exact grant reconciliation. |
| `identity provision` | Required `--tenant`, `--tenant-name`, `--issuer`, `--subject`, `--operator`, `--reason`. `--tenancy-mode` defaults to `dedicated`; `shared` requires `--external-organization`. `--actor-type` defaults to `human`; `service` is also accepted. `--roles` defaults to `tenant_admin` and must include it. |
| `identity provision-development` | Uses the configured development identity roster, not production provisioning arguments. Requires PostgreSQL and a non-production development-auth profile. |
| `identity realms list` | Required `--tenant`; `--limit` defaults to 50, range 1-100. Optional `--after` is the last realm UUID from the preceding page. Rejects nonempty transition inputs. |
| `identity realms suspend`, `reactivate` | Required `--tenant`, `--realm`, positive current `--revision`, `--operator`, `--reason`. Rejects explicitly supplied `--limit` or `--after`. |
| `repair idempotency list` | Required `--tenant`; `--limit` defaults to 50, range 1-100. No cursor option. Rejects reservation, reason and digest inputs. |
| `repair idempotency plan` | Required `--tenant`, `--reservation`, `--reason`. Rejects a plan digest or explicit limit; reads and reports a proposed repair. |
| `repair idempotency apply` | Requires the same target and reason plus exact `--plan-digest` from the reviewed plan. Rejects explicit `--limit`; rechecks state under the repair lock. |
| `repair search-projections` | Required `--tenant`, `--reason`; optional `--operator-label`. `--limit` defaults to 500, range 1-1000. No cursor; follow the report and [repeat/stop procedure](../operations/search-projection-repair.mdx). |
| `repair webhook-secrets` | Required `--tenant`, `--reason`, `--old-key-file`, `--new-key-file`, `--runtimes-stopped`. `--limit` defaults to 100, range 1-100. Continue with `--after` from the prior batch; attest that all runtimes remain stopped. |
| `restore verify` | Required `--tenant`, `--recovery-point`; role defaults match migration. `--limit` defaults to 100, range 1-1000. Continue with `--cursor`, keeping all other inputs unchanged. Deadline: 60 seconds per invocation; output limit: 256 KiB. |

Realm listing uses `--after`; restore verification uses an opaque `--cursor`. They are not interchangeable. Prevent writes to the restored database throughout the complete traversal. An individual page does not qualify the whole database. Follow [restore verification](../operations/backup-restore.mdx#isolated-logical-integrity-verifier) for report interpretation and stop conditions.

`--roles` selects the provisioning command's global Catalog roles, not arbitrary retained role-template IDs. Ordinary role and group administration uses the authenticated [access-management API and UI](../guides/access-management.mdx). Do not infer permissions from a role name.

### Audit files versus restored database state

| Action | Boundary and required inputs |
| --- | --- |
| `audit verify-export` | Offline local export check. Requires `--file`; rejects any `--config` and a nonempty `--recovery-point`. |
| `audit bind-manifest` | Offline local export binding. Requires `--file` and `--recovery-point`; rejects any `--config`. Writes the bound manifest to stdout, not to the input file. |
| `audit compare-restore` | Reads a trusted manifest and the selected PostgreSQL restore. Requires `--file` and `--recovery-point`; accepts `--config` for the schema-owner connection. |

`restore verify` also connects to the isolated PostgreSQL database. Neither database check fetches upstream resource documents. Only the two offline audit actions require no database connection. Binding a manifest does not create independent backup provenance; follow [the audit procedure](../operations/audit.md#bind-and-check-a-recovery-point).

### Development bootstrap and smoke

`bootstrap` accepts optional `--config` and `--namespace` (default `default`). It requires a configured, active development Tenant Administrator and current access-management authority. It constructs an API-role runtime and creates a namespace, publisher and publication-domain approval there.

With the memory driver, that state belongs only to this invocation and disappears on exit. It does not populate a separately running Catalog. With PostgreSQL, use the exact API identity and existing development membership. Bootstrap is not production provisioning or a receipt-bound sample loader. Use [the API-based demo workflow](../guides/local-development.mdx#populated-development-catalog) for samples visible in a running catalog.

`smoke` creates governed records and activates a public snapshot. Run it only against an explicitly selected disposable environment. It uses API tokens, not a schema-owner DSN or `--config`.

| Flag | Default or bound |
| --- | --- |
| `--base-url` | `CATALOG_SMOKE_BASE_URL`, otherwise `http://127.0.0.1:8080`. |
| `--timeout` | Two minutes; positive and at most ten minutes. |
| `--run-id` | Generated when omitted; normalized to lowercase after trimming, at most 40 characters. Letters, digits and single hyphen-separated words only. |
| `--allow-remote` | False. Required for a non-loopback HTTPS target; does not grant permission to mutate it. |

Use the six [smoke token environment inputs](configuration.mdx#smoke-client-variables) rather than sensitive token arguments. Local defaults match `config/local.json`. Follow [the disposable walkthrough](../guides/local-development.mdx#run-the-disposable-walkthrough) for the expected report and cleanup. A failure can leave earlier successful writes; smoke does not roll back the whole walkthrough.

Read each linked runbook for authority, prerequisites, reports and stop conditions. Help and syntactic validity do not authorize repair or data deletion.

### Seed a development Catalog

`catalogctl seed-demo` uses fixed development identities against an explicitly selected loopback API. It has no production bearer input or runtime configuration. The command creates governed sample records; a completed receipt rerun verifies them without writes. Keep the receipt for safe recovery.

| Flag | Required input, default or bound |
| --- | --- |
| `--base-url URL` | Required owned loopback API origin; no environment fallback. |
| `--data-file PATH` | Optional local YAML and native assets. Omission uses embedded maintained samples. |
| `--sample-set NAME` | Default `curated`; the maintained manifest also provides `minimal` and `expanded`. A selected custom file supplies its own profiles. |
| `--state-file PATH` | Default `output/demo/catalog-demo-v1.json`. Retain this receipt across reruns. |
| `--archive-origin URL` | Default `https://127.0.0.1:18443`; the selected development HTTPS archive origin. |
| `--archive-trust-file PATH` | Default `output/demo/archives/cert.pem`; public CA for archive preflight. |
| `--extend` | Default false. Explicitly permit append-only additions or adoption of the exact original receipt. Existing bound entries cannot be edited or removed. |
| `--timeout DURATION` | Default five minutes; positive and at most ten minutes. |

A successful invocation prints the JSON demo report and exits 0. Failures exit 1 and can leave earlier committed records; they do not authorize deletion or a reset. Embedded minimal samples reject extension. See [sample setup and recovery](../guides/sample-catalog.mdx) for dataset versions, receipts, archive preparation and cleanup.

## Worker operations are not the product API

The worker exposes only role-specific startup/liveness/readiness, fixed status/version and metrics surfaces on its configured operations listener. It has no authoring, discovery or tenant-private API routes. Keep that listener off the product Service/Ingress and use private monitoring access. The exact paths and safe signal contracts are in [observability](../operations/observability.mdx), rather than the Control API.

## Make workflow links

See [Make targets and development tools](../contributing/make-targets.md) for the contributor command reference. The links below point to its relevant sections.

<a id="maintained-make-workflows"></a>

[Maintained Make workflows](../contributing/make-targets.md#maintained-make-workflows).

<a id="scoped-zap-checks"></a>

[Scoped ZAP checks](../contributing/make-targets.md#scoped-zap-checks).

<a id="version-and-shared-development-tools"></a>

[Version and shared development tools](../contributing/make-targets.md#version-and-shared-development-tools).

<a id="api-development-checks"></a>

[API development checks](../contributing/make-targets.md#api-development-checks).

<a id="optional-langgraph-example"></a>

[Optional LangGraph example](../contributing/make-targets.md#optional-langgraph-example).

<a id="structured-data-and-conformance-checks"></a>

[Structured data and conformance checks](../contributing/make-targets.md#structured-data-and-conformance-checks).

<a id="workflow-file-checks"></a>

[Workflow file checks](../contributing/make-targets.md#workflow-file-checks).

<a id="go-unreachable-function-checks"></a>

[Go unreachable-function checks](../contributing/make-targets.md#go-unreachable-function-checks).

<a id="go-property-testing"></a>

[Go property testing](../contributing/make-targets.md#go-property-testing).

<a id="go-mutation-measurement"></a>

[Go mutation measurement](../contributing/make-targets.md#go-mutation-measurement).

<a id="generated-api-pilot"></a>

[Generated API pilot](../contributing/make-targets.md#generated-api-pilot).

<a id="functional-api-coverage"></a>

[Functional API coverage](../contributing/make-targets.md#functional-api-coverage).

<a id="local-sonarqube"></a>

[Local SonarQube](../contributing/make-targets.md#local-sonarqube).

<a id="ui-checks"></a>

[UI checks](../contributing/make-targets.md#ui-checks).

<a id="optional-staged-checks"></a>

[Optional staged checks](../contributing/make-targets.md#optional-staged-checks).

<a id="frontend-assertion-strength-and-benchmark-comparison"></a>

[Frontend assertion strength and benchmark comparison](../contributing/make-targets.md#frontend-assertion-strength-and-benchmark-comparison).
