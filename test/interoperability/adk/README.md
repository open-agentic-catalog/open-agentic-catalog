# Native ADK MCP qualification

This optional test uses unchanged [Google Agent Development Kit (ADK) for Python](https://github.com/google/adk-python/tree/d68ca857d5e3bab8e968d4e5f98ef028eb37dc87) to discover and call Catalog's read-only MCP tools. Predetermined calls use native `McpToolset` and `McpTool` objects. A native context and in-memory session provide bookkeeping. No model, model runner, Google account, provider API key, embeddings or model weights are required.

## Prerequisites

Run from the repository root with GNU Make, Git and Python 3.12-3.14. Use a non-root Linux account with access to a local Linux Docker engine through a Unix socket. The host and engine architectures must match because certificate setup executes the compiled fixture on the host. The qualified host is Linux AMD64; ARM64 requires its own native run.

Install the repository Go toolchain through [developer setup](../../../DEVELOPING.md). The fixture generates its private CA and certificates with Go. The host needs no separate certificate tool. Explicit tool acquisition also needs the `gh` CLI with read access to public source, plus access to package and image registries.

Tests build from cached Go modules and use an explicitly installed ADK image. They need no local Compose deployment, PostgreSQL, UI, frontend toolchain or hosted service. They do not use a developer's Catalog credentials.

## Install and select a test

With the repository Go compiler installed, prepare dependencies and run the complete qualification:

```sh
GOTELEMETRY=off go mod download
make adk-tools-install
make test-interoperability-adk-cleanup
```

The first two commands acquire dependencies. Repeat only the test command while those inputs remain current. Run each Make target alone. The targets skip workflow environment files, so a local `.env` does not select a retained environment.

| Target | Use |
| --- | --- |
| `make adk-tools-install` | Acquire the locked test tools and record their image, source and dependency identities. |
| `make test-interoperability-adk` | Run the thirteen normal native groups against one disposable Catalog. |
| `make test-interoperability-adk-cleanup` | Run the complete normal campaign, an intentional metadata failure and actual SIGTERM cleanup in sequence. |

The cleanup target includes the normal campaign; a separate normal run is optional. Normal tests never acquire tools. Missing or stale inputs fail instead of installing a replacement. Finish source edits before starting a campaign; source changes invalidate qualification.

The runnable example is [client.py](client.py). Its `connect`, `get_tools` and `run_async` calls show the native interfaces. The supervisor supplies a disposable Catalog, private trust and synthetic credentials. It publishes one benign MCP Resource through ordinary author, reviewer, publisher and snapshot APIs.

## Reproduce failure and interruption cleanup

`make test-interoperability-adk-cleanup` runs three campaigns. First, all thirteen normal groups must pass. Next, a changed expected display name must trigger the fixed `search-display` assertion. This control changes neither Catalog state nor native ADK/MCP code, tool arguments or schemas.

For the interruption control, the supervisor pauses at the existing reader-revocation checkpoint after twelve groups pass. The controller then sends SIGTERM only to its own supervisor PID. There is no manual timing step. The active group must remain incomplete, and cleanup must finish.

A passing controller summary contains these outcomes:

| Summary case | Supervisor `exitCode` | `verified` | `childSuccess` | `cleanup` | Native groups |
| --- | --- | --- | --- | --- | --- |
| `valid` | 0 | true | true | true | Thirteen passed. |
| `metadata-mismatch` | 2 | true | false | true | Three passed, Search failed and nine unrun. |
| `interrupt` | 2 | true | false | true | Twelve passed and revoked-access incomplete. |

The metadata control also requires native client exit 1, the intended assertion and native closure. Another Search failure cannot qualify. The controller checks actual native calls, identical source/binary/image identities and independent container/private-directory absence.

The aggregate command exits 0 only after all controls qualify. It prints `Passed native ADK success, metadata-failure and SIGTERM controls; owned cleanup passed.` Expected child exits of 2 are successful negative controls only within this verified summary. A nonzero ordinary test exit is a failed qualification.

## Native path and confinement

ADK connects to the actual Catalog `/mcp` handler through a transparent private HTTPS proxy. The proxy preserves request/response bytes and authorization. It records finite method, tool, status and negotiated-protocol observations. It refuses every resource route, including the published `/never-invoke` endpoint. It does not implement MCP or translate Catalog responses.

The image sets `GOOGLE_API_USE_CLIENT_CERTIFICATE=false` to disable ADK's automatic Google mTLS credential discovery. A supported `httpx_client_factory` supplies private-CA trust, finite timeouts and `trust_env=False`. Catalog uses an explicit synthetic bearer credential. The OpenTelemetry SDK, propagators and exports are disabled. These settings qualify server-authenticated HTTPS and bearer access, not Google mTLS or ADK-managed OAuth.

The Catalog owns a `network=none` namespace. The proxy and client share only that namespace, so external model and metadata endpoints are unreachable. No ports, host network, developer home or Docker socket are exposed. Containers are non-root, have readonly roots, no capabilities and bounded memory/PIDs/CPU.

Inputs are readonly. Only non-secret case progress and revocation checkpoints are writable on owned disk. No RAM scratch mount is used. The client receives neither the administrator credential nor the TLS private key.

## Qualified cases

The current native path negotiates MCP `2025-11-25`. The thirteen required groups are:

| Group | Actual assertion |
| --- | --- |
| Inventory | Discover exactly the six Catalog tools through `McpToolset.get_tools()`. |
| Schemas | Inspect the adapter's native declarations for Search fields, kind enums and required exact-release input. |
| Capabilities/access | Read supported kinds and current nonempty permission hints through native tools. |
| Known Search | Return the independently prepared release with explicit local-only filters and lexical results. |
| Contradictory filter | A different kind returns no match. |
| Exact release | Bind immutable identities, metadata and representation digests/sizes to an independent API read; native document bytes remain undisclosed. |
| Explore | Return the exact local kind bucket and population. |
| Closed arguments | Preserve refusals for unknown Search fields, an over-limit page and invalid release identifiers. |
| Missing token | Observe native failure and an independent HTTP 401. |
| Invalid token | Observe native failure and an independent HTTP 401. |
| Foreign tenant | An authenticated reader cannot inspect the selected release. |
| Untrusted CA | Fail specifically for certificate verification, before HTTP reaches the proxy. |
| Revoked access | Keep the original native tools alive while the supervisor revokes the reader's grants. Search becomes empty and the exact read returns `api_denied`. |

The upstream declaration inspection uses the selected release's `_get_declaration()` method. Reports retain both the original server definitions from native `raw_mcp_tool` objects and ADK's converted declarations for diagnosis. This observes schema adaptation without an LLM request. It is not a claim that every JSON Schema constraint survives conversion. Actual tool arguments still reach Catalog's unchanged validation and authorization.

## Reports and cleanup

Use the directory printed as `ADK report:` or `ADK cleanup report:` to locate that invocation's `report.json`. Paths stored in a cleanup summary are relative to the repository root.

| Artifact under `output/interoperability/adk/` | Contents |
| --- | --- |
| `installation.json` | Installed image, acquisition identity, upstream source pins and dependency-inventory hash. |
| `dependencies.json` | Installed package versions, license metadata and native adapter source hashes. |
| `install.log` | Bounded image-build output from explicit acquisition. |
| `run-*/report.json` | Native campaign source/binary/tool identities, immutable fixture references, traffic, case outcomes and cleanup. |
| `run-*/native.json` | Collected native exit, stdout and stderr. It can be absent after setup failure or interruption before collection. |
| `cleanup-*/report.json` | Controller result and each campaign's verified status, supervisor exit, child success, signal, outcomes, cleanup and report/log paths. |
| `cleanup-*/*.log` | Supervisor output for the valid, metadata-mismatch and interruption campaigns. |

For a complete result, require aggregate `success=true` and `cleanup=true`. A cleanup summary must include all three verified controls. Missing controls mean the run stopped before those campaigns completed. `cleanup=true` alone confirms resource cleanup, not compatibility.

Native `caseOutcomes` distinguish passed, failed, incomplete and unrun groups. Atomic, credential-free progress checkpoints preserve the last complete snapshot during interruption. The supervisor reads it after removing containers and before deleting private inputs. Original server definitions and ADK declarations remain in completed native results for schema diagnosis.

Native negative connections can emit upstream TaskGroup diagnostics. The harness retains them and checks for fixture-credential disclosure. Interpret them with case outcomes and process status; expected authentication and certificate refusals do not fail a completed valid campaign.

The native supervisor has a ten-minute campaign budget, followed by separately bounded cleanup. The cleanup controller allows twelve minutes per child. On interruption, it allows the child ninety seconds to clean up. A required forced stop leaves qualification unconfirmed.

Only the supervisor services the fixed reader-revocation checkpoint. It accepts no client-supplied commands or API paths. The original native toolset stays alive across revocation. ADK's `close()` return alone does not prove cleanup; the supervisor also checks process completion, drains normal fixtures and independently verifies absence.

Let Ctrl-C or SIGTERM cleanup finish before inspecting results. Completed, failed and interrupted runs remove their private fixture inputs and exact owned containers. No normal stop command or port-forward cleanup is needed afterward. Reusable tool images, dependency inventory and reports remain.

## Troubleshooting

| Observation | Action |
| --- | --- |
| Missing/stale installation, image or inventory | Run `make adk-tools-install` again after reviewing the changed pins. Normal checks do not pull images or install packages. |
| Missing compiler or modules | Install the Go version from `go.mod`, then run `GOTELEMETRY=off go mod download` explicitly. Tests disable module networking. |
| Engine, account or architecture refused | Use a non-root account and a local Linux Docker engine through a Unix socket. Match the native host/engine architectures. |
| Cleanup controller fails before printing a report | Check prerequisites and installation first. The ordinary native target can expose its fixed installation hint. |
| Source drift | Finish edits, keep sources unchanged during execution and rerun the selected target. Do not accept an older successful report as current evidence. |
| Child exits 2 or prints TaskGroup diagnostics | Compare the control with the expected-outcome table. Require the aggregate command to pass with every control verified and cleanup confirmed. |
| Missing native output | Inspect the campaign phase and checkpoint outcomes. Early setup failure or interruption can precede output collection. |
| `cleanup=false`, leftover private state or forced stop | Treat the run as failed. Inspect that invocation's child report and `containers` list. Confirm ownership before removing only those exact fixture containers. Never use global Docker cleanup. |

## Dependency provenance and updates

Installation downloads hash-locked wheels and builds a test-only image from the digest-pinned Python base. ADK 2.13.0, MCP 2.3.0 and the dependency closure use the fixed `2026-10-29T00:00:00Z` cutoff. Pinned pip 26.2.2 performs installation and dependency checks. The image then removes pip and its unused bundled libraries.

The installer verifies ADK's release revision, license, direct wheel hashes and installed adapter source bytes. Both upstream notices and the installed distribution/license inventory remain with the test tooling. These dependencies are outside application modules, SDK runtime dependencies and shipped Catalog images.

`requirements.in` owns direct selection. `requirements.txt` is its generated hash lock. Use the repository-pinned uv, an explicitly selected cutoff, Python 3.13, the Linux target and wheel-only resolution when regenerating it. Review the complete closure, provenance and notices together. Follow the [dependency policy](../../../documentation/src/content/docs/reference/dependencies.md#adk-interoperability-tools); do not edit lock graphs or force incompatible transitive versions.

Reinstall after changing the installer, Containerfile, locks, notices or provenance. Ordinary driver/source changes require another native qualification, but do not change the installed dependency selection.

The selected closure retains [CVE-2026-114603](https://github.com/advisories/GHSA-xcq7-p3pf-p88h), a Medium advisory in `opentelemetry-api` 1.44.0. ADK 2.13.0 caps this dependency at 1.44; fixed 1.45.1 is outside its supported range. The selected process is an outbound client with no ASGI server, and trace propagators are disabled. Retain the finding for a compatible upstream update. These controls do not fix the library or establish an all-severity clean inventory.

LLM decisions, natural-language answer quality, ADK Web/cloud deployment, OAuth login, Google mTLS, indexed/live federation, and installation or execution of discovered resources remain outside this selection. Existing [Catalog MCP qualification](../mcp/README.md#catalog-control-stdio-qualification) owns the broader protocol/replica matrix.

## Helper tests

Run isolated decisions and native process/Make controls separately:

```sh
python3 -B -m unittest discover -s test/interoperability -p 'test_unit_adk*.py'
python3 -B -m unittest discover -s test/interoperability -p test_functional_adk.py
go test ./test/interoperability/mcpfixture -run '^TestControl' -count=1
```

These selections do not import ADK, start containers or use a model. The Go tests observe unchanged forwarding and bounded private labels through injected transports. Python units reject incomplete/contradictory reports, source/version drift, wrong cleanup-control outcomes and uncertain cleanup. Functional helper tests exercise the actual CLI, Make admission and the controller's readiness/SIGTERM handshake through a disposable local Python process. They do not replace the separately selected native campaigns.
