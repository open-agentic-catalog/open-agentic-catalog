# Replay Desvela and Nevermined publications

Use `make test-interoperability-publication-replay` to check how Catalog imports and searches these two ARD publications. Each runs in its own disposable Catalog with an API, worker and PostgreSQL. The target reads local files, starts an internal Docker network with a private TLS publisher, and needs no provider account. It runs on demand.

## Supply the captures

The repository carries checksums and synthetic tests. Supply each original HTTP response body as a separate file under ignored `output/`. Keep headers separately and preserve the body bytes exactly. Reformatting the JSON changes its checksum. Public availability does not establish redistribution rights for the entries, prices or descriptions.

| Environment variable | File observed | Bytes | SHA-256 |
| --- | --- | ---: | --- |
| `DESVELA_CAPTURE` | Desvela, 21 November 2026. The 26 November HTTP capture has identical bytes. | 7,025 | `a12cc3f1e4941c60789eaeac7377e31bdc6c33422a7ff4d4d2a6a28389dc88a2` |
| `NEVERMINED_CAPTURE` | Nevermined, 21 November 2026. Used for retention and recovery. | 552,014 | `9c11d1130515fe577615d0e9b794ead276bda7fcca9df14d26a2a6c796493518` |
| `NEVERMINED_TRANSPORT_CAPTURE` | Nevermined, 26 November 2026. Used for independent schema validation and inspection with its recorded media type. | 551,036 | `47fde7c56fcdc26ed60e3ca74bb476db65953da2d4abf60460b5824a4648eaee` |

Both publishers served `/.well-known/ard.json`. The HTTP captures on 26 November returned 200 at the requested HTTPS URLs, with no redirect or Link header. Desvela returned `application/json` at 00:22:15 UTC. Nevermined returned `application/json; charset=utf-8` at 00:22:16 UTC. The original captures have no retained HTTP headers. The older Nevermined replay uses the later observed generic JSON transport as an explicit assumption. The byte-identical Desvela recapture corroborates its transport. [Desvela publication](https://desvela.dev/.well-known/ard.json) and [Nevermined publication](https://api.live.nevermined.app/.well-known/ard.json)

If conducting a new assessment, download only the public manifest for which you have permission to retain a copy. Make a single credential-free GET, cap it at 2 MiB and 15 seconds, and leave redirects disabled. Record the UTC time, URL, status, Content-Type, Location, Link and SHA-256 alongside the private body. A different digest requires separate review of format selection, entry inventory and projections before updating a pin. The test rejects a missing or changed file.

## Run locally

Start at the repository root with the [build prerequisites](../../../documentation/src/content/docs/contributing/build-and-release.md#prerequisites-and-identities) and Docker Compose connected to a local Linux engine. Install the shared Python tools once with `make dev-tools-install`. Verify the vendored profiles and validator, then build the two local images:

```sh
make profiles
make conformance-prerequisite
make container-build
docker build -f test/functional/Containerfile -t open-agentic-catalog:publication-replay-fixture .
```

If the pinned PostgreSQL or Python image is missing locally, fetch it during preparation. These commands use the same image references as the test:

```sh
docker compose --env-file /dev/null pull postgres
docker pull "$(awk '$1 == "FROM" {print $2; exit}' test/interoperability/Containerfile)"
```

The replay itself disables pulls and builds. It uses `open-agentic-catalog:dev` and `open-agentic-catalog:publication-replay-fixture` by default. To reuse other local builds, set `PUBLICATION_REPLAY_IMAGE` and `PUBLICATION_REPLAY_FIXTURE_IMAGE`. Point the three capture variables at your private files and run the target:

```sh
export DESVELA_CAPTURE="$PWD/output/captures/desvela.json"
export NEVERMINED_CAPTURE="$PWD/output/captures/nevermined.json"
export NEVERMINED_TRANSPORT_CAPTURE="$PWD/output/captures/nevermined-2026-11-26.json"
make test-interoperability-publication-replay
```

Open the run directory printed by the target. Both `desvela/report.json` and `nevermined/report.json` must show `"status": "passed"` and `"cleanup": "complete"`. The runner checks retained data after restarting the Catalog and removes each disposable project, its volumes and temporary keys.

A missing or changed capture marks its publisher as `blocked` before that Catalog starts. The other publisher still runs, and the target exits unsuccessfully. Restore the file matching the recorded checksum. To repeat a single publisher, run `.tools/dev/bin/python -B test/interoperability/publication_replay_runner.py --provider desvela` or select `nevermined`.

## What the replay establishes

| Capture | Pinned ARD CLI | Catalog importer and indexed Search |
| --- | --- | --- |
| Desvela | Manifest and five entries pass with zero warnings. | Four MCP entries are searchable, including Preflight. The Registry advertisement is retained as unsupported. |
| Nevermined | Manifest and 158 entries pass with 157 warnings. The CLI flags media types outside its list on 139 JSON entries and 17 OpenAPI entries. The Registry entry omits the recommended representative queries. | Flylabs is searchable as an A2A Agent. All 158 original entries remain available to an authorized evidence reader. These include pricing declarations on AgentOracle, Quicknode MPP and Tempo RPC. |

The Quicknode MPP and Tempo RPC entries pass schema validation but fail JSON-LD expansion. Each has a numeric `id` in a nested request example. The ARD context maps that field to `@id`, which requires a string there. Catalog retains the original entry, including its pricing declarations.

Both inspections select `ard-0.91-aa3e598` and classify the publication as Hybrid. All entries omit a version. Each healthy inspection reads the manifest once. Catalog derives `https://registry.desvela.dev/search` and `https://api.live.nevermined.app/api/v1/ard/search/search` from the advertised Registry URLs. Neither inspection establishes List support. The replay does not call those services, resolve offers, fetch artifacts or make payments.

The replay fails if the fixture receives an `Authorization`, `Cookie`, `Proxy-Authorization` or `X-Api-Key` header, even on a fallback request. Unexpected-request diagnostics record only the test phase and HTTP method.

The campaign checks reader Search and audited original evidence separately. It pages the complete administrative inventory and downloads each entry before and after restarting the API and worker. Each download must have its own audit record with the same request ID, reader, observation, digest and byte count. It also checks that a summary-only reader and another tenant cannot read the raw evidence, that the snapshot and its occurrences survive in PostgreSQL, and that importing creates no local release.

After a successful repeat, the publisher separately returns HTTP 503, times out, serves malformed JSON, and exceeds the configured 2 MiB limit. Each failure must preserve the previous complete snapshot and searchable entries. A successful retry or new synchronization must recover the unchanged publication. On a 503 or malformed document, Catalog can also try the same origin's predecessor well-known path. The fixture records and refuses that fallback. No second publication is served.
