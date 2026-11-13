# Configured ARD registry publication test

This optional test checks whether the independent [n1k0ver3E ARD registry](https://github.com/n1k0ver3E/ard-registry/tree/0ba2b4bd18870202e741eb1ce9e3e74a3cdd7b40) can consume Open Agentic Catalog's AI Catalog publication. It runs the unchanged native crawler against a disposable Catalog with real PostgreSQL, API and worker processes. No LLM, embedding provider, cloud account or resource invocation is involved.

## Run locally

Use the repository Go/Python and local Linux Docker prerequisites. Run each command separately from the repository root:

```sh
make ard-registry-tools-install
make test-interoperability-ard-registry
make test-interoperability-ard-registry-cleanup
```

Preparation downloads the exact public source revision, a checksum-verified pnpm 10.34.9 archive and the digest-pinned Node 22.23.4 base image. It installs the peer's original pnpm lock with `--frozen-lockfile --ignore-scripts`, records dependency licenses, checks native tool versions and compiles the unchanged TypeScript without networking. The prepared runtime contains compiled peer code and a fresh production-only dependency tree; it excludes compilers, npm and pnpm. The required native console asset is copied unchanged. It leaves application dependencies unchanged. The selected peer declares Apache-2.0 in its package manifest; its source tree has no standalone license file. Preparation preserves that declaration and records installed dependency licenses. The peer installation is a local test tool, outside Catalog release artifacts. Its original production dependency versions have known advisory matches; run it only inside the campaign's isolated environment. Updating those versions requires a separately reviewed upstream revision and another qualification run.

Ordinary execution requires the prepared image and checks its identity. It does not update the peer or install its packages. Application and fixture image builds use the usual maintained Containerfiles and may acquire pinned build dependencies when their cached layers are missing. The run has a 30-minute overall budget and smaller process, HTTP and state-observation deadlines.

## What it checks

The test publishes two independently reviewed public MCP Server Cards, each with a different identifier and one current version. The native crawler fetches the actual AI Catalog document through the existing private TLS frontend. Positive response bytes are forwarded unchanged. Native List, Search and the retained snapshot must preserve every published entry field, including extension metadata and the exact release reference.

The lifecycle checks then:

1. Publish a reviewed Listing update and verify the subject's new metadata and Search terms while the control remains unchanged.
2. Retire the subject release and activate a complete publication containing only the control. A successful crawl must remove exactly the subject.
3. Return deliberately malformed JSON, then HTTP 503, from separate controlled source failures. Each actual failed crawl must preserve the last successful members. An earlier failed status cannot satisfy a later failure phase.
4. Restart the native peer with its intact snapshot while the same source URL is already unavailable. Native reads must succeed without a new successful source fetch, including after an observed failed refresh.
5. Restore the source, publish changed control metadata and verify recovery.
6. Start a fresh second peer with an unrelated private CA. Verify empty membership, failed acquisition and the actual Node certificate-refusal cause.

Public result `source` identifies the peer registry. The native snapshot's `origin` identifies the Catalog manifest URL; the test checks both separately. The producer's publication identity, audience, digest and ETag are captured independently. This does not transfer Catalog audit history or the manifest's root host object.

## Compatibility limits

| Boundary | Selected peer behavior |
| --- | --- |
| Versions | The index keys only by identifier. Multiple versions of the same identifier overwrite one another; this campaign selects one current version per resource. |
| Reverse indexed import | Native terminal List pages contain `pageToken: null`. The selected ARD List contract and Catalog decoder reject that present null token. A passing one-way campaign does not qualify reverse replication. |
| Invalid members | Lenient ingestion can skip an invalid member while reporting a successful crawl. Warnings are discarded by the loader. Exact member and metadata comparisons are required. |
| Restart | The test qualifies ordinary restart with an intact snapshot and the same unnested source URL. Direct file writes do not establish crash durability or atomic replacement. |
| Health | The health endpoint captures its startup entry count. The test uses actual reads, current source status and observed requests for later phases. |
| Transport | Catalog acquisition uses verified private HTTPS. The peer's native REST listener uses private HTTP inside the owned namespace. |

The peer's older ARD claim and bundled checker do not establish complete conformance to Catalog's selected profiles. This is a selected publication-consumer qualification.

## Isolation and cleanup

The campaign creates its own Compose project, database volume, images, credentials, certificates and snapshot. No service publishes a host port. The API, native peer, TLS fixture and HTTP client share the owned API network namespace; the worker has its own namespace. The internal Docker bridge and disabled external DNS forwarding confine requests. Positive local TCP controls and denied external TCP/DNS checks run from the API, worker and peer namespaces. `ARD_SOURCES` is an exact nonempty owned source array and `ARD_UPSTREAMS=[]`; nested catalogs, public defaults and referral fanout are unused. Declared MCP URLs remain metadata.

Do not run upstream `verify:crawl` as a substitute: it explicitly contacts hosted Hugging Face. Its separate vendored conformance tooling is also outside this campaign.

Normal completion, failure, SIGINT and SIGTERM enter owned cleanup. The cleanup target independently checks both a deliberately wrong metadata expectation against actual native results and a real SIGTERM after successful initial transfer. Each must fail for its intended reason and leave no owned containers, networks, database volume or private fixture files. Reusing a nonempty report directory is refused before fixture creation; the existing report bytes must remain unchanged. Cached peer/base images and retained developer environments remain.

Reports and logs stay under ignored `output/interoperability/ard-registry/`. Each run records its source fingerprint, tool/image identities, completed phases and cleanup status. After an uncatchable host failure, use only the exact project and stop command from that run's report. Never use global Docker cleanup.

`make test-tooling-unit` includes isolated member/metadata/provenance and incomplete-report checks. These units never install or run the peer. The shared TLS frontend also has recorder-based Go tests; real transport, publication and restart assertions run through the selected target above.
