# Replay Desvela and Nevermined publications

Use `make test-interoperability-publication-replay` to check how Catalog imports and searches these two ARD publications. Each runs in its own disposable Catalog with an API, worker and PostgreSQL. The target reads local files, starts an internal Docker network with a private TLS publisher, and needs no provider account. It runs on demand.

## Supply the captures

The repository carries checksums and synthetic tests. Supply the original files privately under ignored `output/`. Public availability does not establish redistribution rights for the entries, prices or descriptions.

| Environment variable | File observed | Bytes | SHA-256 |
| --- | --- | ---: | --- |
| `DESVELA_CAPTURE` | Desvela, 21 November 2026. The 26 November HTTP capture has identical bytes. | 7,025 | `a12cc3f1e4941c60789eaeac7377e31bdc6c33422a7ff4d4d2a6a28389dc88a2` |
| `NEVERMINED_CAPTURE` | Nevermined, 21 November 2026. Used for retention and recovery. | 552,014 | `9c11d1130515fe577615d0e9b794ead276bda7fcca9df14d26a2a6c796493518` |
| `NEVERMINED_TRANSPORT_CAPTURE` | Nevermined, 26 November 2026. Used for independent schema validation and inspection with its recorded media type. | 551,036 | `47fde7c56fcdc26ed60e3ca74bb476db65953da2d4abf60460b5824a4648eaee` |

Both publishers served `/.well-known/ard.json`. The HTTP captures on 26 November returned 200 at the requested HTTPS URLs, with no redirect or Link header. Desvela returned `application/json` at 00:22:15 UTC. Nevermined returned `application/json; charset=utf-8` at 00:22:16 UTC. The original captures have no retained HTTP headers. The older Nevermined replay uses the later observed generic JSON transport as an explicit assumption. The byte-identical Desvela recapture corroborates its transport. [Desvela publication](https://desvela.dev/.well-known/ard.json) and [Nevermined publication](https://api.live.nevermined.app/.well-known/ard.json)

If conducting a new assessment, download only the public manifest for which you have permission to retain a copy. Make a single credential-free GET, cap it at 2 MiB and 15 seconds, and leave redirects disabled. Record the UTC time, URL, status, Content-Type, Location, Link and SHA-256 alongside the private body. A different digest requires separate review of format selection, entry inventory and projections before updating a pin. The test rejects a missing or changed file.

## Run locally

Use the standard development Python with the pinned `jsonschema` and a local Linux Docker engine with Compose. Run `make profiles` and `make conformance-prerequisite` to check the standards tools. Prepare the Catalog and fixture images from the checkout. A prebuilt pair can be selected with `PUBLICATION_REPLAY_IMAGE` and `PUBLICATION_REPLAY_FIXTURE_IMAGE`.

```sh
make container-build
docker build -f test/functional/Containerfile -t open-agentic-catalog:publication-replay-fixture .
```

Cache the PostgreSQL image pinned in `compose.yaml` and the Python image pinned in `test/interoperability/Containerfile` before running. The target checks their presence and disables pulls and builds. Set the three capture paths, for example:

```sh
export DESVELA_CAPTURE="$PWD/output/captures/desvela.json"
export NEVERMINED_CAPTURE="$PWD/output/captures/nevermined.json"
export NEVERMINED_TRANSPORT_CAPTURE="$PWD/output/captures/nevermined-with-http-evidence.json"
make test-interoperability-publication-replay
```

Each publisher has a separate report under `output/interoperability/publication-replay/`. For a focused rerun, use `.tools/dev/bin/python -B test/interoperability/publication_replay_runner.py --provider desvela` or select `nevermined`. A missing or changed capture marks that publisher as `blocked` before its Catalog starts. Either a blocked or failed publisher makes the target exit unsuccessfully. The other publisher still runs. The runner removes its project, volumes and temporary keys on completion or interruption.

## What the replay establishes

| Capture | Pinned ARD CLI | Catalog importer and indexed Search |
| --- | --- | --- |
| Desvela | Manifest and five entries pass with zero warnings. | Four MCP entries are searchable, including Preflight. The Registry advertisement is retained as unsupported. |
| Nevermined | Manifest and 158 entries pass with 157 warnings. The CLI flags media types outside its list on 139 JSON entries and 17 OpenAPI entries. The Registry entry omits the recommended representative queries. | Flylabs is searchable as an A2A Agent. All 158 original entries remain available to an authorized evidence reader. This includes pricing declarations on AgentOracle, and on Quicknode MPP and Tempo RPC where JSON-LD expansion fails. |

Both inspections select `ard-0.91-aa3e598` and classify the publication as Hybrid. All entries omit a version. Each healthy inspection reads the manifest once. Catalog derives `https://registry.desvela.dev/search` and `https://api.live.nevermined.app/api/v1/ard/search/search` from the advertised Registry URLs. Neither inspection establishes List support. The replay does not call those services, resolve offers, fetch artifacts or make payments.

The campaign checks reader Search and audited original evidence separately. It pages the complete administrative inventory and downloads each entry before and after restarting the API and worker. Each download must have its own audit record with the same request ID, reader, observation, digest and byte count. It also checks that a summary-only reader and another tenant cannot read the raw evidence, that the snapshot and its occurrences survive in PostgreSQL, and that importing creates no local release.

After a successful repeat, the publisher separately returns HTTP 503, times out, serves malformed JSON, and exceeds the configured 2 MiB limit. Each failure must preserve the previous complete snapshot and searchable entries. A successful retry or new synchronization must recover the unchanged publication. On a 503 or malformed document, Catalog can also try the same origin's predecessor well-known path. The fixture records and refuses that fallback. No second publication is served.
