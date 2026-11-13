# GitHub Agent Finder publication qualification

This optional test reads one pinned generated publication from [GitHub Agent Finder Catalog](https://github.com/github/agentfinder-catalog/tree/3286bfd843a3831b52a8a72b0ff6f3eb0f730d7f). It serves the original bytes through an owned HTTPS fixture and exercises the real Catalog API, worker and PostgreSQL. It does not contact hosted Agent Finder, run its generator or follow listed resource URLs.

## Run locally

Use the repository Go/Python and Docker prerequisites. Run each target separately from the repository root:

```sh
make agentfinder-tools-install
make test-interoperability-agentfinder
```

Preparation verifies the bundled artifact and MIT notice. It acquires the pinned standard Python image already selected by the interoperability tools when missing. There is no separate Python dependency environment or installation stamp. Normal execution verifies cached inputs and fails on missing images; it does not acquire replacements. Application and fixture builds use the maintained Containerfiles and can need their pinned build dependencies.

The test creates an invocation-owned Compose project. Migration and identity provisioning precede the separate API/worker processes. The standard functional fixture supplies private TLS and exact publication bytes. An internal Docker bridge confines the API, worker and HTTP client; no service port is published. The fixture shares the API network namespace, and requests use the owned private hostname through the normal hardened fetcher. External DNS forwarding is disabled for API/worker. Positive fixture connectivity and denied external TCP probes run from both namespaces.

The total deadline is 30 minutes, including builds. HTTP and worker waits have smaller explicit budgets. Reports use a separate run directory under ignored output, and distinguish completed checks, failures and cleanup.

## Selected publication

| Input | Value |
| --- | --- |
| Repository | `github/agentfinder-catalog` |
| Revision | `3286bfd843a3831b52a8a72b0ff6f3eb0f730d7f` |
| Artifact | Checked-in generated `ai-catalog.json`, unchanged |
| Size | 1,248,214 bytes |
| SHA-256 | `7ca6ac36768ebffb05639084681d85334b3c8b72a8637264fb01fc43710853bb` |
| Entries | 1,718 distinct identifier/version identities |

[Provenance](provenance.json) records source hashes and the independently reviewed inventory. The [upstream generator](https://github.com/github/agentfinder-catalog/blob/3286bfd843a3831b52a8a72b0ff6f3eb0f730d7f/scripts/generate_ai_catalog.py) changes contributor identifiers/types and fetches hosted MCP inventory. This test uses its committed output instead of running those acquisition paths.

| Declared media type | Entries | Version |
| --- | --- | --- |
| `application/mcp-server+json` | 647 | Present and nonempty |
| `application/ai-skill` | 925 | Omitted |
| `application/vnd.anthropic.claude-plugin+json` | 69 | Omitted |
| `application/vnd.cursor.cursor-plugin+json` | 22 | Omitted |
| `application/vnd.github.copilot-plugin` | 55 | Omitted |

Missing versions remain absent. The oracle comes from the pinned publisher bytes before ingestion, independently of Catalog responses.

The bundled publication is covered by the retained [MIT notice](LICENSE). This does not license the contents of referenced repositories, packages, artwork or runtimes. Those assets are neither downloaded nor redistributed here.

## What the test checks

- Normal source inspection and administrator connection using a publisher scope that includes unsupported kinds.
- The explicit fixture `application/ai-catalog+json` response selects `ai-catalog-1.0-b062278`. The same bytes under generic `application/json` are refused by native inspection. Filename and `specVersion` alone do not select the predecessor profile. Neither fixture response claims to reproduce GitHub's hosting headers.
- A real synchronization Job produces the exact nonempty administrative population. Every raw entry retains its original identifier, version presence, URL and other metadata, with matching content digests.
- Read-only owner SQL independently binds the tenant, inspection, source revision, Job, snapshot, audit and all 1,718 immutable occurrences to the retrieved publication digest. SQL creates or repairs no evidence.
- All five types retain native `compatibility.support: unknown`, without a kind or normalized Search projection. Authorized reader Search excludes them. No locally governed Resource, release or publication is created.
- A repeated synchronization retains identities and raw evidence without false additions, updates or withdrawals. Prior snapshots and occurrences remain unchanged.
- An observed HTTP 503 causes a new failed snapshot while preserving the last complete membership. The same Job recovers after the fixture returns, and its later attempt binds to a complete snapshot.
- Another tenant cannot read source, snapshot, raw-entry or occurrence evidence. Separate source-policy and untrusted-certificate controls retain their actual refusal codes.
- Phase failures and missing cleanup cannot produce a passing report.

This is a passing retention characterization with unsupported formats, not successful searchable transfer. There is no reverse importer, native Copilot execution, package inspection, resource invocation or hosted compatibility claim.

## Unit checks and cleanup

`make test-tooling-unit` includes isolated inventory and evidence-validator tests. They never acquire the artifact or start services. The functional fixture has recorder-based Go units, and the real HTTPS/worker/database path runs only through the explicit target above.

Normal completion, failure, SIGINT and SIGTERM remove only invocation-owned containers, network, volume, images and private fixture files. Cached Python/PostgreSQL images and existing development data remain. After an uncatchable host failure, use the exact project, stop command and image names recorded in that run's report. Do not use global Docker cleanup.
