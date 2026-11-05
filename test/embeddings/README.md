# Embedding endpoint tests

`make check` includes YAML lint for both embedding Compose files, Hadolint for the fixture Containerfile and static Compose validation of the complete runtime-test overlay. These checks start no containers and need no model cache. The Containerfile's narrow DL3006 exception covers a build argument resolved by the harness from a verified digest-pinned profile, not an unversioned upstream image.

`make embeddings-check` runs service-free Go client and metadata/ranking tests. `make test-embeddings` runs the production Go HTTP client against an owned, offline TEI container with the pinned MiniLM profile. It checks batch ordering, normalized vectors, repeated-query stability and actual provider rejection of overlength input.

It neither starts a Catalog database nor tests integrated hybrid Search.

The qualifier checks that its 600-word negative query, including `queryPrefix`, fits the configured byte ceiling before making any inference call. An insufficient local budget fails qualification; local input rejection cannot stand in for a provider response. Functional loopback controls require exact short and long requests, reject an accepting endpoint, and test the exact prefixed byte boundary.

The Python supervisor validates all three success flags, dimensions and elapsed-time fields, then reports success only after model-integrity checks and owned cleanup. `make embeddings-check` includes these functional controls; isolated Go and native Python selections retain their separate test layers.

The pinned MiniLM profile has a 256-word-piece context; the separate BGE profile has 512. The fixture disables truncation and checks provider metadata. Its byte ceiling is not a token estimate, and this qualification does not establish how an arbitrary provider handles overlength input.

The real fixture permits 8 inputs per batch. The Catalog indexer's maximum 32-distinct-input rejection and resume tests use an isolated provider port, covering the 63-call worst case and successful-subset retention without a second model campaign.

Prepare cached inputs explicitly with `make embeddings-install`; matching pinned files are reused. `EMBEDDING_PROFILE=bge-small-v1` selects the other existing evaluation fixture without changing any application configuration. There is one running model per invocation, no automatic model selection and no hosted API calls.

The semantic qualifier checks that the pinned model image exists before it creates Compose resources. Run `make embeddings-install` if that image is missing.

Go is required for service-free tests; Docker and cached model inputs are additional prerequisites for real-provider targets. Raw results can be redirected under ignored `output/`.

`runner.py` reuses `test/search-relevance/runner.py` for exact image/model acquisition, offline launch and invocation-owned cleanup. `qualify/` compiles a small test command that uses `internal/platform/embeddings`; it does not duplicate provider HTTP code. Temporary binaries, model containers and the private network are cleaned after success or failure.

Verified model caches remain available for later tests. Existing development containers and databases are untouched.

The original relevance experiments remain separate. Their quality measurements are not protocol or runtime search qualification.

The semantic browser and coverage scripts use the UI toolchain for ESLint, Playwright rules and Prettier through `make ui-check` (also included in `make test-ui`). The lint regression suite checks that this external script directory is included and rejects missing browser awaits and undefined names.

## Runtime qualification

`make test-semantic` builds the Catalog image and runs an invocation-owned Compose project with PostgreSQL, the real pinned MiniLM service, API and worker. It explicitly selects `seed-demo --sample-set minimal` for its five native Resources/three published releases, independently of the expanded developer demo and archive fixture. It seeds only that disposable Catalog, checks automatic indexing, semantic-only matching, exact Search/Explore totals and facet counts, complete small-corpus pagination, public ARD headers/continuation, administrator reindex/replay and CLI metadata.

Outage checks require retained keyword identities; recovery restores the semantic-only results. Public queries use the seed tag to exclude the Registry self-entry explicitly. Browser rebuilds preserve the snapshot document and ETag.

A temporary UI host and real Chromium verify reader/admin and fallback workflows; install UI tools and Chromium first. The existing Go performance driver runs four readers for ten seconds during reindex requests and records latency, failures and scoped container resources. The same invocation then reuses the governed seeder for 1,000 published releases, waits for real-model indexing, checks the exact 997-match total and two nonempty disjoint 20-item scale-tagged pages, and runs a second four-reader phase.

For both identities, the browser also checks pending text, explicit refresh and guided saving against the owned Catalog. Refresh and saving use applied text while preserving the unsubmitted draft. Opening the saved editor and checking consent send no Search or Explore request. Search also refuses submission while the saved query's live-disclosure consent is absent, even after provider consent. After both choices, an explicit Search returns real hybrid results, or keyword fallback during the deliberate model outage. Request counters cover these actions; intercepted responses do not replace the provider or SQL library.

This samples pages rather than enumerating all 997 identities. A read-only exact-role SQL probe compares custom and generic plans with JIT enabled and disabled; it retains numeric timings and sanitized plan nodes, not SQL or documents. Cleanup removes the owned project volumes, temporary processes and unique Catalog image, not the verified model cache.

No caller API URL or database DSN is accepted. The fixture cutoff is a test setting, not a production relevance recommendation.

Polling accepts success only before its deadline; each underlying HTTP/process call retains its independent execution bound. Exact container identities/start times, restart counters and OOM state are checked across phases. Only a deliberately restarted model or worker receives a refreshed baseline.

Reports include source, effective semantic configuration, model/image and Go/Node/Chromium identities, and the model cache is verified before and after the run. Offline model loading does not imply an egress-isolated Compose bridge. The separate `make test-integration` selection includes a real PostgreSQL profile-replacement test using a deterministic provider port: equal dimensions and unchanged input digests cannot cause a new profile to reuse old vectors or become available before completion.

Version 2 of the shared corpus includes Plugins. The pilot checks all four kind totals independently: 250 Agents and 249 of each other kind.

It requires no second model or weight upgrade.

For browser source coverage, run `make ui-coverage-e2e UI_E2E_PROFILE=semantic` after `ui-install`, `ui-browser-install` and `embeddings-install`. This selects the same complete supervisor with private instrumented assets; it does not replace ordinary `test-semantic`. The two browser contexts exercise reader/admin hybrid search, provider-outage fallback and service degradation, control/public snapshot reindex, native invalid snapshot rejection and exact accepted Job links.

The supervisor waits for those Jobs and compares the publication snapshot before/after. It captures the public continuation after rebuilding, preserving the distinct provider-outage and stale control-cursor checks. Results and context counters go to a unique ignored `output/coverage/e2e/semantic/run-*/`; any qualification, source drift or cleanup failure invalidates coverage.

Use the UI report-only target to combine compatible explicitly selected local, Compose, OIDC and semantic runs. No model/cache download happens during measurement.

`make test-minikube-semantic` uses the existing isolated Helm harness, three API/worker/UI replicas and two identical model replicas. [Containerfile](Containerfile) builds a fixture-only model image from the verified cache; no runtime download or node bind mount is needed. Calico provider-egress checks, cross-replica pagination and disablement supplement Compose.

Run browser gates separately from these Docker/Minikube network changes.

The semantic overlay supplies 1.25s/2.5s worker dependency restart windows to qualify the optional maintainer composition, plus a seven-attempt creation limit and 5.5-second automatic retry cap to both API and worker. Before manual reindexing, the supervisor checks the actual automatically admitted semantic Jobs for that limit, then verifies it on the manual reindex/replay path. This qualifies configuration/admission, not embedding-provider `Retry-After` handling.

[local.py](local.py) validates local Compose/cache inputs. [semantic.py](semantic.py) owns Compose qualification, using [semantic.compose.yaml](semantic.compose.yaml) for disposable extension provisioning, optional migrations and nondefault Job policy. The product's optional [Compose model overlay](../../compose.embeddings.yaml) does not install extensions or enable runtime retrieval. See [hybrid search operations](../../documentation/src/content/docs/operations/hybrid-search.mdx).

## Semantic backup and restore

Run `make embeddings-install` to prepare the pinned MiniLM inputs. Then run `make test-semantic-restore` for the small semantic restore fixture.

The fixture publishes three sample releases and waits for completed indexing. It records a semantic-only query, exact release identities, reader results and denied results. It stops the API and worker before the dump. It stops the source database before it restores a separate owned destination.

The destination uses the same PostgreSQL, pgvector and model configuration. Owner commands reconcile exact semantic privileges and verify every tenant through the normal continuation chain. Semantic table counts and digests must match before any restored runtime starts. Configuration and credentials remain outside the dump.

The restored API answers semantic queries before its worker starts. Administrator and Reader results must match the baseline. Grantless and other-tenant identities receive no results. Exact Resource and Catalog records reject other-tenant reads.

The fixture then starts the restored worker and requests a reindex through the administrator API. The Job must succeed with three members and become the active generation. Search results, Resource records, publication documents and ETags must remain unchanged. The original database remains stopped throughout.

`make test-semantic-restore-prepare` stops after database verification. Both modes remove their disposable containers, networks, volumes and private dump on success or failure. Cleanup attempts image removal and resource checks even if stack removal fails. Both modes preserve the shared model cache and retained development stack. Redirect the JSON report under ignored `output/` when retaining local evidence. No hosted model is called.
