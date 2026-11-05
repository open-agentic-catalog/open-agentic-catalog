# Generated API pilot

Run from the repository root on Linux amd64 with Python 3.12, Docker Compose and the existing packaged-functional prerequisites:

```sh
make schemathesis-tools-install
make test-api-generated
SCHEMATHESIS_SEED=2 make test-api-generated
```

Each target runs alone and ignores the deployment `.env`. Installation downloads only the hash-locked wheels in `tools.txt` into the ignored `.tools` virtual environment. Tests reject malformed/empty locks and check every installed dependency version, disable third-party pytest plugin discovery and never install Schemathesis dependencies.

The existing packaged harness builds current-source images, seeds three governed resources and owns PostgreSQL, API, worker, fixture, volumes and cleanup. It does not target an existing development deployment. Go/runtime images and ordinary unit gates do not depend on this tool.

## Scope and assertions

Schemathesis 4.30.1 reads the original `api/openapi/control-v1.yaml`. Local references are required before loading it; no flattened or relaxed schema is generated. Successful controls exercise `listNamespaces`, `createNamespace`, `getNamespace`, `updateNamespace`, `searchCatalog`, `browseCatalog`, `listResources` and `listResourceVersions`, including create/read/update, nonempty browse/search continuation and selected reader/other-tenant denials.

Resource List and parent-bound Version List add complete fixture identity, exact canonical-identifier and intersected kind filtering (including an empty Plugin selection), nonempty continuation and exact timestamp/UUID ordering controls. Both reads succeed for global Reader; a valid fixture-only member with only the inert Service client template receives an empty Resource List and 403 for the exact parent-bound Version List. Other-tenant Resource List is empty and foreign-parent Version List receives 404.

Resource and version cursors reject changed kind/parent binding. The independent Resource oracle also applies canonical-identifier criteria before limits, preserves opaque case and normalizes AIR scheme/publisher spelling. Real-tool calibration catches ignored criteria and erroneous nonempty results; Namespace controls check exact name/normalized-slug intersection. Owned IDs, current ETags and fresh command keys provide valid prerequisites. The private smoke projection supplies three exact Resource IDs/kinds/identifiers; three setup-only exact-ID GETs supply their authoritative metadata.

One setup-only version POST under the fixed Agent parent creates `0.0.0` after seeded `1.0.0`, making UUID order distinguishable from label order. These four requests have a separate exact `setup` inventory and consume the same 1,000-request limit. Setup operations remain omitted from generated-operation selection, and all other lifecycle methods are forbidden.

The fixture-only no-Reader token/configuration and private seed projection are removed with the owned fixture. The generated fields remain unchanged.

Seven operations each run 20 positive and 20 negative Hypothesis examples with an explicit seed, no persistent database, generation and shrinking only. `getNamespace` has a successful owned-ID control, not invented body/query generation. The Go contract driver reads the original OpenAPI and supplies the operation inventory before traffic. The Schemathesis loader must match that independent inventory. Each selection and its sorted omitted IDs must form an exact, disjoint partition of the inventory. Child-report validation and supervisor finalization enforce the same partition. New OpenAPI operations enter omitted automatically; the selected operations stay fixed. Omitted means excluded from generated cases, not necessarily absent from setup or readback traffic.

There is no crawl, unexpected-method phase, general state machine, external source selection or third-party traffic.

Negative query cases must remain invalid after Requests serializes them: `None` can disappear and other invalid Python types can become valid strings. The original resolved parameter schemas and the API's strict integer query syntax determine eligibility before sending. Repaired-by-serialization cases are filtered, never counted as invalid rejections.

Every selected generator must still complete 20 examples. All four GET-query generators check their serialized negativity; admitted Resource/version replies are compared against independent seeded inventories using the actual serialized query, never Catalog validation as an oracle.

Response schema, media type and exact operation status checks run through Schemathesis (201 for namespace creation, 200 for the other positive operations). A custom check requires `400`/`INVALID_ARGUMENT` for invalid inputs; authentication, lookup and precondition failures cannot substitute for validation. Positive generated inputs have narrow domain dispositions: bound cursors, whitespace-only display names, control-bearing audit reasons, unchanged updates and existing namespace slugs.

The documented strict integer literal rule rejects JSON Schema's mathematical integer spelling `1.0`; that representational limitation is explicit, not a general allowance for positive 4xx responses. Namespace conflicts require finding the exact generated slug. Rejected namespace mutations must preserve the readback state.

Successful controls must finish before generated traffic begins.

Nine real-tool calibrations exercise valid HTTP, deliberately accepted invalid input, a response-schema defect plus an incorrectly successful Problem status, authentication loss, transport budgets, 2020-12 keywords/UUID format query serialization and command-sequence oracles, plus schema-valid wrong filter/parent/order/continuation pages. The tool must detect the injected defects through the intended checks; parser failures do not count as catches.

## Bounds, evidence and replay

The pilot is sequential, with 1,000 requests, at most 100 namespace-creation attempts, 1 MiB request bodies, 64 KiB URLs/headers, 2 MiB responses, five-second socket timeouts and a 150-second HTTP budget. The child supervisor additionally limits each pytest process to 180 seconds and 8 MiB output. The packaged supervisor retains its 20-minute workflow and independent cleanup bounds.

Requests stay on the selected loopback origin and operation/method allowlist, with proxies and redirects disabled. Incomplete examples, missing/failed/skipped JUnit cases, authentication loss, child errors and cleanup failures fail the gate.

Inspect `output/functional/run-<id>/report.json`, `generated.xml` and `generated-calibration.xml`. Reports bind the source, schema, images, served version, dependency lock, seed, selected/omitted operations, controls, examples and actual HTTP count. Existing operation counters include observed validated responses; generated request counts are not Go statement coverage.

Failures retain native JUnit diagnostics with all four fixed development tokens removed, plus the last minimized synthetic body/query when available. No production/OIDC identity is accepted by this profile. Raw artifacts remain ignored.

Replay the recorded seed through the same Make command; the new invocation supplies its own IDs, tokens and ETags. The synthetic reproducer records the operation, generation mode, body/query and status, not reusable credentials. Add confirmed defects as minimal regressions in the owning Go suite.

Seeded examples are bounded evidence, not exhaustive validation or a security certificate.

## Resource and Version authoring

The same command runs a second selection after the original pilot. It generates 20 positive and 20 negative examples for each of `createResource` and `createResourceVersion`. The original eight operations, 280 examples, nine calibrations and twenty Namespace histories remain unchanged.

Authoring controls create a Namespace, Resources, an Agent Version and a Skill Version before generated traffic. Successful responses require 201 and the original response schema. Exact GETs verify the bound tenant, Resource identity and kind, Version parent and label, immutable revision pointers and evidence references. They also verify retained native content digests. The readbacks are controls, not generated operations.

The selection binds authorized parent IDs, distinct AIR identities, fresh version labels and command keys. Hypothesis varies each identity suffix or version label, including the 64-byte AIR name and 128-character Version limits. A structured inline Agent Card supplies known-valid native content. Native parsing and external document retrieval are outside this generated selection.

The control envelope remains generated. Binding preserves missing fields and invalid identity/label types or lengths. Each negative body must remain invalid against the original schema after binding and Requests serialization. Malformed JSON and non-UTF-8 bytes also require rejection. Repaired cases are discarded before HTTP and do not count toward the required twenty examples. Validator errors stop the selection; they cannot count as invalid requests.

Negative cases require `400` with `INVALID_ARGUMENT`. Authentication, lookup, precondition and arbitrary domain failures fail the selection. No schema-valid domain refusal is allowed here. Before and after each rejection, one read-only PostgreSQL transaction counts and hashes nine projections. These cover Resources, Versions, immutable revisions, representations, evidence sets, validation runs, document blobs and selected audit/outbox events. The queries cover the whole owned database without parent joins or tenant filters. This includes orphan rows and accidental writes under another tenant.

The audit projection selects `resource.created` and `resource_version.created`. The outbox projection selects `com.open.agentic.catalog.resource.created.v1` and `com.open.agentic.catalog.resource.version.created.v1`. Stable projections include event identity, tenant, target, request binding, payload, digests and creation evidence. Outbox acknowledgement, leases, dispatch scheduling and cleanup markers remain outside the digest. Unrelated Jobs, authentication, metrics and idempotency reservations remain outside this oracle. Successful Resource and Version controls first prove the expected table deltas. Each creation also verifies one exact audit record and public outbox event. The checks bind the action, tenant, target, request ID, chronology and notification payload digest.

Six additional real-tool cases qualify valid creation/rejection, accepted invalid input, response defects, mutation after rejection, serialized binding and transport limits. Response controls include malformed envelopes and matching documents with the wrong tenant. The selection has its own unchanged limits: 1,000 HTTP requests, 150 seconds for HTTP and state checks, and 180 seconds for the child process. All setup, readback and shrinking requests count. At most 100 Resource/Version creation attempts are allowed. The original byte, socket, origin, method and cleanup limits also apply.

Snapshot SQL uses only the invocation-owned PostgreSQL container and one read-only transaction with a three-second statement limit. Each projection selects at most 1,001 rows to detect the 1,000-row limit. SQL checks a 4 MiB encoded limit per projection and a 16 MiB combined limit before string aggregation. Exceeding a limit fails the snapshot. Primary-key ordering includes the composite validation-run and document-blob keys. Each subprocess has a five-second limit and a 64 KiB output limit, within the existing overall deadline.

A valid Skill Markdown `content` control checks exact blob digest, size and retained reference. Inline Agent `data` remains a separate control. Four SQL calibrations check an orphan blob write after a clean `400`, valid audit/outbox inserts and legal outbox cleanup marking. The injected writes are fixture controls, not observed application defects. They preserve database guards and change only the invocation-owned database.

Successful authoring evidence requires 100 snapshots, four exact event checks and all four SQL calibrations. It also requires 22 readbacks for each exact read operation and at least 154 HTTP requests. The 80 generated examples, five native authoring cases and six real-tool calibrations remain unchanged.

The report's `authoring` section records its separate operation selection, omitted operations, examples, readbacks, state checks and HTTP requests. Inspect `generated-authoring.xml` and `generated-authoring-calibration.xml` beside the original reports. Failures retain a bounded synthetic `generated-authoring-reproducer.json`. Binary bodies use an explicit base64 encoding in that file. Replay uses the same recorded seed and fresh fixture prerequisites. No review, release, source enrollment or general state machine is selected.

## Maintenance

Native helper units live in `test/functional/test_unit_generated_api.py`; Make/process checks live in `test_functional_generated_api.py`. Existing `test-tooling-unit` and local functional wrappers discover them without optional packages. Run the real `test-api-generated` gate after changing the pilot, tool pin, transport, report policy or selected API behavior.

Preserve failed attempts and classify product defects separately from generator/fixture mistakes.

`tools.txt` locks the 27 installed packages by exact version and qualified wheel hash. Updating it requires reviewing the complete closure, notices and platform/Python requirements, reinstalling explicitly, and rerunning calibrations and live seeds. This lock qualifies Python 3.12/Linux amd64; it is not a cross-platform wheel matrix.

`make clean` includes the virtual environment in its existing explicit local cleanup scope.

The wheel metadata declares these licenses; installed distributions retain their upstream notices. These are execution-only test dependencies, outside the Go license allowlist and shipped artifacts:

| License | Packages |
| --- | --- |
| MIT | anyio, charset-normalizer, graphql-core, harfile, hypothesis-graphql, iniconfig, jsonschema-rs, markdown-it-py, mdurl, pluggy, pyrate-limiter, pytest, PyYAML, rich, schemathesis, urllib3 |
| BSD-3-Clause | click, idna, MarkupSafe, Werkzeug |
| BSD-2-Clause | Pygments |
| Apache-2.0 | requests, sortedcontainers |
| Apache-2.0 OR BSD-2-Clause | packaging |
| MPL-2.0 | certifi, hypothesis |
| PSF-2.0 | typing-extensions |

See the [CLI reference](https://schemathesis.readthedocs.io/en/stable/reference/cli/) and [Python API](https://schemathesis.readthedocs.io/en/stable/reference/python/) for upstream semantics. The maintained gate deliberately uses the Python API to constrain operation selection and prerequisites; the upstream CLI's default phases and permissive negative-status allowlist are not this pilot's policy.

## Generated namespace histories

The same pilot also executes 20 generated histories. Each uses a fresh namespace and an initial successful revision, followed by a permutation of seven required actions and up to two extra actions: update, exact replay, changed payload under a used key, fresh-key stale revision, reader denial, other-tenant read and other-tenant write. The first successful request remains bound for later replay even after subsequent updates.

Its Case retains method, path, serialized body, content type, query, identity, original ETag and key.

Every action has an exact expected status/problem code, followed by an administrator readback. Rejections and replay must leave state exactly unchanged; successful updates increment the modeled revision once and preserve all unrelated fields. Successful response/readback timestamps are compared at PostgreSQL microsecond precision; replay responses remain exact.

Only response-side schema checks run, so Schemathesis cannot create unbudgeted authentication probes outside the owned Session.

`sequences.completed` and per-action `sequences.steps` supplement the 280 generated request counts (14 cases times 20 examples). All setup and shrinking requests count against the same 1,000-request/100-creation/time limits. A failed sequence retains bounded synthetic action order and label in `generated-sequence-reproducer.json`; rerun its recorded seed with unchanged generators. Nine calibration cases include deliberately wrong replay, stale/key-reuse acceptance and mutation after rejection. These are harness-oracle calibrations; the real PostgreSQL profile exercises the application. Missing sequences or step kinds fail report reconciliation.
