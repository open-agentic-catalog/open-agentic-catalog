# Local MCP tools and governed handoff

Run each target separately from the repository root:

```sh
make mcp-tools-install
make test-interoperability-mcp
```

Prerequisites: a Linux host with a matching local Linux Docker engine selected through a Unix socket, a non-root developer account, Python 3.10+, Make, `gh` with public repository read access, a Go launcher on `PATH`, the exact repository Go toolchain installed or cached, and its application modules already cached. AMD64 and ARM64 images are supported by the pinned Node base; recorded execution is Linux AMD64. The Everything wrapper forwards native MCP bytes. The Catalog-stdio fixture also reuses the existing inert sample archive package.

The target builds current `catalogd`/`catalogctl` offline and starts an owned memory Catalog. No existing Catalog, database, Compose environment, model, account or real credential is needed. Qualification resolves the compiler matching `go.mod` from the launcher or its module cache and verifies its version before building.

It clears inherited Go flags and uses `GOTOOLCHAIN=local`, `GOPROXY=off` and `GOSUMDB=off`; an older launcher is supported only when the exact compiler is already cached. Both wrapper and Catalog builds honor explicit `GOMODCACHE`/`GOCACHE` paths while clearing other inherited Go settings. Missing tools fail without downloads; installation remains an explicit separate action.

Installation acquires Inspector 2.11.0 and official Everything 2026.8.31 from the dedicated npm lock. Everything uses its compatible SDK 1.32.1. Inspector uses the core, client and server SDK modules at 2.3.1.

`provenance.json` binds exact versions, archive locations, SHA-512 integrity and the registry's `gitHead` when published. Inspector's registry record does not publish `gitHead`. Its separate `sourceRevision` records the reviewed immutable commit. The installer compares registry fields and GitHub source package versions. It checks Everything's source license through `gh` and uses `npm ci --ignore-scripts` in the digest-pinned Node 22.23.4 image.

No lifecycle script is needed by the selected published CLI/server artifacts; Inspector's recursive source-install script is deliberately disabled. Tool installation uses no unversioned `npx` or host npm installation and adds no application dependency. The Catalog stdio server separately uses its pinned Go MCP SDK.

`output/interoperability/mcp/installation.json` binds the tool inputs, image ID and installed manifest inventory. `dependencies.json` includes embedded manifests and lifecycle-script declarations. Reinstall after tool-input changes.

Test execution uses the installed image by immutable ID with `--pull=never`; missing/stale tools fail rather than download. Deployment `.env` files are ignored, and these targets reject additional Make goals. They are opt-in, outside ordinary unit tests and `make verify`.

## Scenario and report

The target qualifies 22 outcomes: thirteen real MCP exchanges, five governed assertion groups, three Catalog-denial groups and unchanged retained evidence after runtime failures. It authors, validates, independently reviews and publishes a Catalog release, searches/reads it through real `catalogctl`, and explicitly configures Inspector from its retained Card. Catalog metadata alone never authorizes a tool invocation.

Unchanged Everything runs in `streamableHttp` mode in an owned Docker namespace with `--network=none`. The stdlib Go wrapper, fresh memory Catalog and unchanged Inspector share only that namespace's loopback. No host port is published; there is no external egress, Docker socket or developer home mount.

Containers have non-root identities, read-only roots, no capabilities, bounded CPU/memory/PIDs/temp/logs and cleared proxy variables. Everything sees no private files. Catalog has a separate configuration/token mount and the wrapper has its own key/certificate mount.

Inspector sees only its read-only configuration and public CA; its credential store is explicitly memory-only and its other settings/scratch paths are private tmpfs.

The wrapper's fixed `/mcp` route verifies a synthetic `X-MCP-Test-Key`, rejects Catalog bearer authorization and consumes the key before forwarding to Everything, which has no native authentication. A generated private CA (signing key retained only in memory), five-minute certificate clock-skew allowance and exact `127.0.0.1` SAN protect HTTPS; Inspector uses `NODE_EXTRA_CA_CERTS`, without a TLS bypass. The wrapper preserves JSON-RPC bytes/IDs, protocol/session headers and streaming/cancellation.

Its observations contain bounded method/capability counts, never credential values. Only `echo` may be invoked; listing other upstream tools does not authorize their execution.

The five required cases are initialization, tool listing, exact echo, missing-key rejection and invalid-echo argument rejection. Every admitted client negotiates legacy `2025-11-25`, opens its SSE stream and initializes without sampling/elicitation; roots are configured as an empty list. Everything advertises `mcp-servers/everything` version `2.0.0`, distinct from its npm package version `2026.8.31`.

Native failure codes and structured errors must match, and the invalid call must contain a real `isError:true` result. Per-case observations prove denied requests are not forwarded and only the two direct echo calls occur before Catalog qualification. Native Everything logs independently corroborate forwarded POSTs and initialized sessions.

Inspector closes its connection without a session `DELETE`; destroying the owned server removes those sessions. Explicit client session termination is not qualified.

The governed fixture uses separate development author/reviewer/publisher identities and normal API idempotency keys/ETags, including administrator approval of its publisher domain and explicit public snapshot activation. One revision retains the experimental Server Card as `primary` and Registry metadata as `distribution`; only the Card is publicly projected. Both describe this synthetic wrapped deployment and preserve their distinct profiles, secret-header descriptors and opaque extension metadata.

The Card matches Everything's observed runtime identity. The header descriptor has no secret value or default.

Real CLI capabilities/search/readback select the ListingRelease ID from search output. Assertions bind resource, version, immutable revisions, release, evidence, both canonical document digests and the public snapshot. A new `2.0.1` version/listing draft changes its remote without publishing it; re-search/readback still yields the original sealed `2.0.0` release.

Inline document digests bind Catalog's canonical JSON, not submitted whitespace or CLI formatting. Governance, discovery and Catalog-denial phases leave MCP counters unchanged.

Only the final exact readback supplies the URL, transport and header name for Inspector; the runtime key is supplied separately. The narrow mapper requires the owned literal `/mcp` HTTPS endpoint, one valid primary Card/remote, declared `2025-11-25` and the expected secret input. It rejects unsupported selections, templates, paths, transport/protocol versions and extra/preset credential headers before writing configuration.

Unknown extensions stay inert. The derived client initializes, lists tools and echoes; final independent observations show seven initialized sessions, seven SSE GETs, twenty-nine POSTs and three echo calls including the deliberate invalid call. Discovery does not fetch a Card from the MCP server, install a package or execute opaque instructions.

Before writing the derived client config, missing and invalid Catalog credentials receive API 401, and a valid Reader from another tenant receives exact-release 404. The CLI rejects an absent token locally before HTTP; invalid/wrong-tenant credentials preserve real HTTP status/code/request-ID diagnostics. No denial produces a successful CLI envelope, new client config or MCP process/traffic.

Public discovery is deliberately anonymous, separate from these private Control reads.

Five further Inspector invocations reject a wrong wrapper key, a Catalog token substituted for that key, deliberate Catalog bearer forwarding, missing private-CA trust and an unavailable MCP listener. The normal handoff never sends Catalog authorization; only the deliberate bearer negative increments its observation counter. Synthetic Catalog-token injection is limited to the two credential-negative configurations, which are removed with other private state.

Denied requests add no upstream forwarding. The CA control omits `NODE_EXTRA_CA_CERTS` and requires native exit 4 with the certificate-chain verification cause, not any nonzero exit. For outage, only the owned wrapper stops; Everything keeps the namespace alive and Catalog remains readable.

Native connection refusal must name the owned listener, and a container launch failure cannot pass. Original record data/digests and active public snapshot/ETag remain unchanged after all failures.

Each run prints its private report directory under ignored `output/interoperability/mcp/run-*/`. `report.json` records all thirteen MCP case outcomes, five governed assertions, three Catalog denials, post-failure integrity, selected IDs/profiles/digests, tool/application/configuration-source identities, three binary hashes, phase observations and cleanup. Individual MCP JSON files retain bounded native stdout/stderr after checking every synthetic credential for leakage.

The final observations are the last completed wrapper probe; the outage case has no fabricated fresh observation. A zero exit requires all 22 outcomes, unchanged inputs, successful Catalog shutdown and owned cleanup. These are enumerated scenario outcomes, not line-coverage percentages or general MCP certification.

OAuth, sampling, elicitation, model-backed hosts, stdio installation and arbitrary tool invocation are outside this qualification.

The scenario deadline is ten minutes, including setup; Inspector processes have thirty seconds, connection/request settings ten seconds and captured output two MiB. Offline wrapper compilation has a separate two-minute bound and each application binary build a five-minute bound, all within the unchanged scenario deadline. Signals, including the scenario deadline, are deferred through a container-creation operation for at most thirty seconds so an ordinary interruption cannot race its registration; cleanup has separate bounded calls.

Explicit image acquisition has fifteen minutes. Owned containers and private certificate/configuration/binary files are removed on success, error or ordinary interruption. Reports and the explicitly installed tool image remain; `make clean` deliberately preserves `output/` installations and evidence, as for HF.

No retained environment is stopped or reset. `SIGKILL`, a creation timeout or an unavailable Docker daemon cannot guarantee automatic cleanup. Unconfirmed creation keeps `cleanup:false` even after best-effort removal because the daemon may still create the container; the failed report retains its exact owned names in `containers`.

Inspect only those names once the daemon responds. Unconfirmed cleanup cannot pass qualification.

## Maintenance and verification

Pure contract/selector/supervisor/API-helper tests live in `test_unit_mcp*.py`; Make/process checks live in `test_functional_mcp.py`. The Go fixture has recorder/injected-transport units and `functional`-tagged real HTTP/TLS/streaming/cancellation and fixed-origin redirect tests. Existing `test-tooling-unit`, `test-unit`, local functional/race selections, Containerfile lint, Python lint and JSON checks include these sources.

Ordinary tests never install or execute Inspector/Everything. Run the real target separately after harness/tool changes, including an interrupted active run; do not infer real compatibility from injected runner tests.

Use npm 11.19.1 to regenerate the v3 lock. It preserves `libc` filters that npm 10 drops when it rewrites optional packages. The Node 22.23.4 image continues to install the lock with its bundled npm 10.9.9.

Review the complete npm lock and packaged notices for each pin update. Compare the immutable source and npm identities. Rerun explicit installation and all real controls. Scan the complete npm lock locally with the pinned Trivy and current advisory data, including development dependencies. Do not submit dependency inventories to external audit services.

Keep raw advisory and installed-inventory reports under ignored output. Lockfile scans do not inventory every embedded/native artifact or base-image component. Retain Everything's exact upstream `Everything-LICENSE`: its npm archive omits that file, and upstream has an Apache-2.0/MIT licensing transition with CC-BY-4.0 documentation. Other installed manifests include permissive licenses, MPL-2.0 and permissively selectable dual licenses; a bundled pino test fixture has no own license field. Preserve notices and this inventory limitation. These tools are local execution-only dependencies, never redistributed in Catalog images and outside the imported-Go license allowlist.

## Catalog Control stdio qualification

Install the independent Catalog client, then run the qualification. Keep each target separate:

```sh
make mcp-catalog-tools-install
make test-interoperability-mcp-catalog
```

This selection uses the official [Python MCP SDK 2.3.0](https://github.com/modelcontextprotocol/python-sdk/releases/tag/v2.3.0). Its source revision, exact wheels and hashes are recorded under `catalog/`. The SDK owns negotiation, schemas, messages and cancellation. Driver code does not rewrite SDK source or protocol bytes. A temporary observer calls the original SDK process factory unchanged to record the stdio child's exit. Review this observer when updating the SDK.

The dedicated installer verifies wheel provenance, a minimum fourteen-day release age and the retained MIT license. It builds a non-root client image from `catalog/requirements.txt` with required hashes and binary wheels. Its inventory records all installed packages, licenses and the SDK source digest. Installation requires registry/GitHub access; qualification uses only the installed immutable image and cached Go modules. Missing or stale installations fail without downloads. These development tools never enter application images or modules.

The target retains the memory/legacy stdio regression, then runs a separate PostgreSQL matrix. It uses the native Linux prerequisites above and the already-installed PostgreSQL 18 image from the shipped Compose model. It never runs Inspector/Everything, starts Minikube, calls hosted APIs or changes retained services. The existing `make test-interoperability-mcp` handoff and its separate npm installer remain unchanged.

The fixture owns one internal Docker bridge with external egress disabled and no published ports. An exact private address and temporary CA admit one local source. The Catalog, peer and client share that owned network namespace. The stdio command calls its API through loopback. Containers remain non-root, read-only, capability-free and resource-bounded. The only bind mounts are invocation-owned inputs and binaries.

The existing expanded sample loader publishes twelve inert releases through normal API governance: three Agents, three MCP Servers, three Skills and three Plugins. A source inspection and real worker sync retain four ARD entries. Three entries have valid absent or nonempty versions and enter indexed Search. The fourth declares an invalid empty version; it remains retained evidence and cannot become a readable indexed record. No resource is invoked.

Seventeen required assertion groups cover these boundaries:

| Boundary | Assertions |
| --- | --- |
| Initialization and listing | Actual wire revision, Catalog identity, exactly six static read-only tools, closed inputs and structured output schemas. |
| Local discovery | Exact twelve-release set, all four kind filters and two-item continuation without loss or repetition. Explore counts, bounded bucket remainders and Plugin filters match direct API reads. Detail identities, representation digests and handoff references also match. Native content stays excluded. |
| Indexed discovery | Three exact version selections matching the direct API set, retained source provenance, missing empty-version rejection and complete mixed local/indexed continuation. Default Search stays local. |
| Input and authority | Closed arguments, changed-limit cursor rejection, invalid credentials, tenant isolation, current assignment revocation and rejection of a cursor issued before revocation. Self-access matches current Control permissions without identity fields. Revoked readers receive empty local facets and current access. |
| Fault controls | Malformed JSON and consumed fields, API byte limits, whole-result output refusal, escaped credential reflection and verified private TLS with an untrusted-CA negative. Valid controls precede or follow each refusal. |
| Lifecycle | Client cancellation reaches the actual HTTP request, excess concurrency returns `busy`, a cancelled session stays usable, idle and active-request EOF exit cleanly, and SIGTERM cancels active HTTP work before an unsuccessful exit. |
| Isolation | Metadata calls leave peer counters unchanged. All MCP children exit; API/peer shutdown and owned container, network and private-file cleanup must succeed. |

Revocation follows existing API behavior: local collection reads return the caller's now-empty authorized population. Exact local reads and explicit indexed reads refuse access. Capabilities remain configuration hints. The stdio server does not cache permissions.

Reports use `output/interoperability/mcp/stdio-*/report.json`. Success requires all groups, native client/container exit, unchanged source inputs and cleanup. Failed or interrupted runs keep `success: false`; unconfirmed creation or failed cleanup cannot pass. Reports contain fixed classifications and synthetic identities, not credentials or private response bodies. Raw private fixture files are removed.

Early network failures retain the exact owned network name for inspection. Docker removal errors cannot report successful cleanup. A fixture drain timeout returns an error and prevents a successful shutdown result.

Pure report/supervisor tests run through `make test-tooling-unit`. Both container-removal and network-removal exceptions must leave cleanup unsuccessful, while still removing private files and attempting the other owned cleanup. Go fixture units use recorders and temporary files; Make/process tests remain functional.

Use `make coverage-unit` for isolated application tests and `make test-functional-local` for local SDK, HTTP and compiled-command exchanges. The deadline unit uses `testing/synctest`; the private HTTPS test verifies actual request cancellation at the configured timeout and a successful subsequent call. The [testing guide](../../../documentation/src/content/docs/contributing/testing.md#catalog-control-tools-over-stdio) maps these layers to their existing gates.

The independent exchanges are functional assertions. Their uninstrumented processes do not add Go statement counters. The tools expose Control metadata; they do not wrap ARD entries or execute resources.

## PostgreSQL transport and protocol matrix

The same target requires every combination below. Each row uses one independent SDK client across two current API processes.

| Transport | Protocol | Required result |
| --- | --- | --- |
| stdio | `2025-11-25` | Legacy handshake, six tools and all thirteen case groups. |
| stdio | `2026-07-28` | Modern discovery, six tools and all thirteen case groups. |
| HTTPS | `2025-11-25` | Legacy handshake without a retained HTTP session and all thirteen groups. |
| HTTPS | `2026-07-28` | Modern per-request protocol and all thirteen groups. |

The thirteen groups cover inventory, local pages, filters, indexed records, exact releases, Explore/access, sparse/empty results, closed arguments, live results, partial peers, replica continuation, restart continuation and stale cursors. Four live resource kinds supplement twelve local releases and three indexed records. Positive and contradictory filters prevent ignored inputs from passing. Markdown Skill filtering must retain both supported media types.

A healthy peer proves complete live continuation. A separately unavailable peer proves partial results and bounded-window diagnostics; a failed peer cannot supply a stable replay fingerprint. Ordinary metadata reads, refused arguments and resource references must leave independent peer counters unchanged. No Catalog token reaches a peer.

The fixture creates one private internal bridge, a disposable PostgreSQL volume, two API processes, one worker and owned HTTPS controls. It publishes no host ports. API/worker roles are distinct; only preparation commands use the schema owner. Both APIs share the database, policy and cursor key. A verified private-CA TLS proxy alternates unchanged client requests between them. It adds no sticky routing, session registry or application behavior.

The same client continues a Catalog cursor on the other API. At a fixed checkpoint, the supervisor gracefully stops and restarts the first API. The client then reuses a still-valid cursor and makes a fresh call. All four combinations require their own observed restart. Pausing the indexed source separately proves stale-cursor refusal. No MCP session header may appear.

`output/interoperability/mcp/matrix-*/report.json` binds source, binaries, PostgreSQL/client image IDs, four client rows and restart timestamps. Each row requires thirteen successful groups and both replicas. Success also requires unchanged inputs, clean API/worker shutdown and removal of owned containers, database volume, network and private files.

A failed native client reports the tool-refusal code, transport, protocol and case. Unknown or malformed refusal text remains unclassified. This evidence excludes tool arguments, response bodies and credentials. A passing repeat does not explain an earlier refusal.

Failed or interrupted runs cannot pass; inspect their exact owned resource names if cleanup is unconfirmed. SIGKILL or an unavailable daemon can prevent automatic cleanup.

The memory stage has a ten-minute deadline; the PostgreSQL stage has fifteen minutes, including setup. The matrix client has 175 seconds and the supervisor 180 seconds. Restart acknowledgements have fifty seconds. Builds, readiness and Docker operations have separate finite limits; cleanup runs outside the scenario deadline. Do not edit source or run shared deployment campaigns during qualification.

Run `make python-lint` and `make test-tooling-unit` for the Python helpers. Make/process regressions remain functional under the local selection. Go recorder fixtures are units; real TLS/process tests retain the `functional` tag. Neither helper tests nor a clean exit without complete reports establish independent compatibility.
