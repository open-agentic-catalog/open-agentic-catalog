# ContextForge integration tests

This opt-in test runs the official ContextForge image beside a disposable Catalog and Everything server. It checks Catalog MCP proxying and governed virtual-server handoff.

The Catalog remains an independent metadata service. ContextForge is a test tool, not a Catalog dependency or an ARD implementation used by the Catalog.

## Prerequisites

Use local Linux AMD64 Docker with Compose, GNU Make, Python 3.12-3.14, Git, curl, `gh`, Trivy and the repository's Go toolchain. Use a local Unix socket and a non-root account with Docker access.

Prepare the module cache, pinned PostgreSQL image and Trivy database through the [tutorial prerequisites](../../../documentation/src/content/docs/guides/contextforge.mdx#prerequisites).

Install the independent MCP client and Everything tools separately:

```sh
make mcp-tools-install
make mcp-catalog-tools-install
make contextforge-tools-install
```

The last command checks the latest stable official ContextForge release against [inputs.json](inputs.json). It verifies source and image identities, then installs the digest-pinned image.

Review and update the pin if a newer stable release exists. The ContextForge image has no release-age wait. Other tools retain their existing dependency policies.

The installer requires Trivy's existing local database. It records the image inventory and scan under ignored `output/interoperability/contextforge/`. It does not upload either report.

Keep these reports while you use the pin. Retire the exact image reference after its owned runs stop. Do not prune shared Docker storage.

Retain [the upstream license](UPSTREAM-LICENSE) with this fixture. The image remains a local execution input. Do not copy it into Catalog packages or generate Catalog release notices from it.

## Run the controls

Run this command alone from the repository root:

```sh
make test-interoperability-contextforge
```

Missing or stale ContextForge, SDK or Everything installations fail the check. The check never installs those tools.

The runner builds current Catalog and fixture images under private test tags. It starts only [this Compose file](compose.yaml), with explicit project and environment arguments.

Each invocation uses its own PostgreSQL volume, internal network, certificates and generated credentials. The root Compose environment and retained data remain separate.

Input preparation refuses nonempty directories and symlinks. Cleanup retries check current resources before removing private inputs or reporting success.

The Catalog uses real API and worker database roles, owner-only migrations, OIDC, shared tenancy and scope enforcement. API commands create namespace-scoped readers.

Readers also receive the exact discovery template. Separate Contributors, Reviewers and Publishers receive namespace-scoped publication grants. Their tokens carry separate OAuth scopes.

ContextForge uses one worker, SQLite, database caching, stateless sessions, native TLS and authenticated team-scoped virtual servers. Its Admin UI uses bundled assets.

The generated accounts use random passwords. The supported bootstrap setting permits unattended initial login. Default-password and other password checks remain enabled.

The Catalog, ContextForge, client and database use only the owned internal network. All proxy variables are cleared. Native SSRF controls allow only the frontend's exact internal address; loopback remains blocked.

The fixed frontend also joins an owned administration bridge and publishes one loopback port. It forwards versioned REST and native Admin UI paths to ContextForge with verified private TLS. It accepts no client-selected destination.

ContextForge runs as its packaged `10001:10001` user. Only its owned SQLite and temporary directories are writable. Private keys remain in role-specific mounts.

The fixture sets `RATE_LIMIT_MEDIUM_RPM=1000` for its bounded MCP checks. The default 100-request allowance interrupts this sequence with HTTP 429.

Rate limiting stays enabled. The fixture preserves authentication, permissions, TLS, Host/Origin checks and Catalog limits.

The native controls check:

- Direct Catalog discovery of exactly six unique read-only tools, capabilities and current access.
- Direct Everything echo, static resource bytes and the simple prompt.
- ContextForge's running version and authenticated Admin UI.
- Native Gateway, team, role, virtual-server and scoped-token creation.
- Six selected Catalog tools per tenant, plus one Everything tool, resource and prompt.
- Scoped runtime access, refused administration, missing credentials and untrusted certificate rejection.
- A real invalid-token response that preserves its HTTP status when diagnostic parsing cannot read a JWT.

## Catalog proxy checks

Both independent client protocols, `2025-11-25` and `2026-07-28`, read all six Catalog tools through ContextForge's stateless endpoint.

Tool discovery uses pages of two. Search uses separate one-item Catalog pages. The check resolves imported names and compares schemas and annotations.

Normal API commands publish two Agent releases for tenant A and one for tenant B. Independent reviewers approve the releases.

One inert ARD fixture supplies an indexed record. It does not expose a resource runtime.

The proxy checks cover:

- Capabilities, filtered and empty Search results, exact releases, indexed records, Explore counts and current access.
- Exact identifiers, immutable representation digests, complete pagination and matching text/structured responses.
- Separate teams, virtual servers and Catalog credentials for two tenants.
- Namespace grant revocation and restoration through the same open client, token, Gateway and virtual server.
- Refused exact reads, excluded Search results and excluded Explore counts after revocation.
- Missing/invalid gateway credentials, untrusted certificates and disallowed Host/Origin values.
- Expired Catalog tokens and invalid audiences, which return upstream HTTP 401.
- Insufficient OAuth scope, which returns upstream HTTP 403 before tool execution.
- Native `X-Upstream-Authorization` overrides, accepted credential replacements and restored tenant data.
- A subset server that refuses an excluded tool without an upstream call.
- A stopped Catalog API, bounded forwarded-call failure and successful reads after restart.

The ordinary ContextForge bearer authenticates the gateway user. It does not replace the configured Catalog credential.

Native `X-Upstream-Authorization` can replace that credential even when header passthrough is disabled. Catalog permissions apply to the effective token.

The fixture records fixed protocol fields, HTTP statuses and synthetic credential labels. It never records tokens, search text or response documents.

Counters distinguish native initialization from tool calls. Metadata checks require zero resource invocation and zero unexpected frontend requests.

Refusals remain distinct. A revoked namespace grant returns a Catalog tool error; a missing OAuth scope fails HTTP admission.

The SDK can present an HTTP failure as a generic MCP error. Response hooks retain the actual HTTP status without changing protocol traffic.

Isolated helper tests check cursor progress, duplicate tools, diagnostic privacy, credential output, cleanup retries, outage errors and logs for failed prompts. They use temporary files and fakes, without servers, subprocesses, databases or independent SDK tools.

Overall success stays false until both integration flows and their recovery checks finish. Failed diagnostics preserve the original failure.

The overall deadline is 30 minutes. Startup, subprocess output, requests, container resources and service logs have separate bounds.

## Run individual setup stages

Use `python3 -B test/interoperability/contextforge_runner.py prepare` to create a new fixture. Its stdout contains only the absolute owned directory.

Pass that directory to `start`, `register`, `proxy`, then `handoff`, each once. Successful stage commands keep the environment available for the next command.

`info` prints the generated Admin UI address, internal MCP URLs and private file paths. It prints no credentials.

`register` creates mode `0600` header files with complete Bearer values. The generated `private/login.json` supplies the separate synthetic browser account.

`browser` uses the UI's pinned Node and installed Playwright tools. It checks private-certificate refusal, sign-in, native registrations, virtual servers and sign-out.

It then reloads the protected Admin page to verify that the session is gone.

The browser allows only this owned server's certificate key and loopback origin. It records no trace or browser session. Its screenshot contains the synthetic server list.

Run `stop` with the owned directory after inspection. A failed or interrupted mutating stage also runs scoped cleanup. Invalid ordering refuses before mutation.

The `proxy-progress.json` marker records the first successful proxied tool response. Interruption tests use it to select real proxy work without retaining arguments.

## Governed virtual-server handoff

The second flow publishes connection metadata for the protected Everything virtual server. It uses the same native registration, private CA and separate upstream key.

Contributor, Reviewer and Publisher identities create and publish an MCP Server Card through normal Catalog APIs. The card declares an Authorization secret input without a token.

The descriptor names the virtual server and its exact URL. Its resource version is separate from ContextForge's shared application name and version.

An isolated `catalogctl` container searches for the release and reads that exact record. It receives private trust and reader tokens, without database inputs.

The check binds the Resource, version, immutable revisions, release, validation evidence, representation, profile and digest. A later unpublished draft does not change that selection.

Missing, invalid and other-tenant Catalog credentials refuse exact reads. These refusals create no runtime configuration or invocation.

The mapper accepts only the owned ContextForge origin and selected virtual-server ID. It supplies the separately generated ContextForge bearer token.

The independent MCP client resolves imported names and resource URIs through native discovery. It checks exact echo text and static `features.md` bytes against the direct Everything control.

The handoff also executes the public guide's Python example verbatim in the owned client. It requires `Echo: Hello` and rejects failed or credential-reflecting output.

The client uses `2026-07-28` with ContextForge. The gateway negotiates `2025-11-25` with Everything. The report records both hops and the shared native application identity.

The selected image has a reproduced prompt limitation. A prompt-first call in a fresh client fails private-CA verification on the gateway's upstream path.

Direct Everything prompt reads succeed. Echo and resource reads through ContextForge also succeed with the configured key and CA.

The report records that prompt failure as a peer limitation. It does not mark prompt forwarding as compatible or disable TLS verification.

The prompt's own log entry must identify the certificate failure. Unrelated TLS errors do not explain a prompt failure.

Runtime controls refuse missing credentials and wrong-team access before an upstream invocation. They stop Everything and ContextForge separately, then verify forwarded echo after each restart.

Outage checks require a tool error or connection failure. Successful tool results and HTTP authentication failures cannot qualify an outage.

ContextForge retains its SQLite state and virtual-server URL. The Catalog stays ready, and its sealed release and validation evidence remain unchanged.

Frontend counters observe accepted and credential-rejected Everything requests. They retain header presence and synthetic labels, without secret values.

## Reports and cleanup

Read the `report.json` path printed by the runner. Require both `success: true` and `cleanup: true`.

The report binds source, image and client identities. It records native results without credentials. Failed controls remain failures, even when teardown succeeds.

Success, failure and handled interruption remove only the invocation's containers, volume, networks and private files. Build logs and redacted reports remain under ignored output.

If Docker cannot remove the services, the report fails. Private inputs remain available for an exact teardown retry.

Do not interrupt teardown a second time. A forced kill or daemon failure can prevent cleanup. Use the exact reported directory for recovery:

```sh
python3 -B test/interoperability/contextforge_runner.py stop /absolute/path/to/output/interoperability/contextforge/catalog-contextforge-012345abcdef
```

Replace the example with the invocation's actual directory. The command refuses paths without a matching ownership marker. Do not use root Compose teardown or global prune.

## Current scope

This target covers native controls, Catalog MCP proxying and governed virtual-server handoff. The [setup tutorial](../../../documentation/src/content/docs/guides/contextforge.mdx) uses the same Compose fixture with separate stage commands.

The local image scan reports upstream advisories. Review current image findings before changing isolation or using real data.
