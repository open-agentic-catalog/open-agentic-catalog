# Independent interoperability qualification

The optional [Pi ARD qualification](pi-ard/README.md) uses an unchanged native Pi/pi-ard session with model networking disabled. Run `make pi-ard-tools-install`, then `make test-interoperability-pi-ard`, separately. The campaign checks actual owned Catalog HTTP, continuation, refusals and declined installation. The startup-only `test-interoperability-pi-ard-prepare` target remains available.

The optional [ARD Explorer check](ard-explorer/README.md) selects unchanged source and confines its native proxy, browser and owned Catalog. Run `make ard-explorer-tools-install`, then `make test-interoperability-ard-explorer`, separately. It tests public Search, filters, continuation, Explore, one List page and separate synthetic HTTP controls. Authentication, `queryContext`, List continuation and full Search-referral Connect behavior retain documented client limits. The startup-only target remains available for focused confinement checks.

The separate [ContextForge checks](contextforge/README.md) use an invocation-owned Compose project, authenticated native APIs and the installed independent MCP client. Run `make contextforge-tools-install`, then `make test-interoperability-contextforge`, separately. This target covers both Catalog MCP client protocols and CLI-selected virtual-server handoff. Echo and resource reads pass; fresh prompt forwarding retains a private-CA limitation. The [setup guide](../../documentation/src/content/docs/guides/contextforge.mdx) uses the same fixture through scoped stage commands and optional native browser checks.

The separate [A2A governed handoff](a2a/README.md) uses `make a2a-tools-install` and `make test-interoperability-a2a` for a real official SDK client/server exchange over private HTTPS. It builds an owned in-memory Catalog and verifies normal governance, exact CLI-selected retained-card invocation, later-draft immutability and Catalog/TLS/outage denials. It remains a separate selection from HF, Directory and MCP.

This test runs the unmodified HF Discover Search and navigation commands, plus native-library Search calls, against a freshly built, in-memory Catalog. Focused HTTP controls characterize documented client limitations. A separate peer target runs the same pinned implementation as owned HTTPS servers against the real Catalog API and worker. These targets complement the pinned wire corpus and two-Catalog federation suite; neither is full ARD certification.

The separately selected [AGNTCY MVP](agntcy/README.md) uses `make test-interoperability-agntcy AGNTCY_LAB_DIR=/path/to/lab` against the existing pinned local Directory lab. It covers one A2A artifact path through Catalog's APIs and compact ARD compatibility checks. It does not use the HF tool image or provision a cluster; its prerequisites, report and retained-seed rules are separate.

The [Gateway qualification](gateway/README.md) uses `make gateway-tools-install`, then `make test-interoperability-gateway`. This separate owned Minikube fixture deploys native nginx/auth, MongoDB and the current Helm API/worker with PostgreSQL. It characterizes authenticated discovery, paging and conformance, then tests source enrollment, exact retained membership, native changes and same-Job outage recovery. Reverse import retains the native private-address refusal. [ANS qualification](ans/README.md) uses `ans-tools-install` and `test-interoperability-ans` for native controls, Catalog live discovery and lifecycle with PostgreSQL. HF client extensions retain this Docker/memory harness; [fresh Directory qualification](agntcy/README.md#fresh-owned-deployment) has its own maintained target.

The separate [local MCP control](mcp/README.md) uses `make mcp-tools-install` followed by `make test-interoperability-mcp`. It qualifies real Inspector/Everything HTTPS controls and a governed Catalog release through exact CLI discovery into metadata-derived initialization, tool listing and echo on isolated loopback.

The separate [Catalog Control MCP qualification](mcp/README.md#catalog-control-stdio-qualification) uses `make mcp-catalog-tools-install`, then `make test-interoperability-mcp-catalog`. The locked Python SDK checks stdio and HTTP with both selected protocols. Seventeen legacy groups retain current authority and fault/lifecycle controls. Thirteen groups per matrix combination add four-kind local/indexed/live discovery and PostgreSQL replica/restart continuation. All results and owned cleanup must pass. Discovered resources are never executed.

The OpenARD native List evaluation does not select a maintained target. Its native List returns entries. However, direct Search inspection records no List capability, and indexed enrollment returns HTTP 412. This is not worker synchronization qualification.

## Run

Use a local Linux Docker engine (Unix socket), `gh` with public repository read access, Python 3.10+, Make and Go. Run each target separately from the repository root:

```sh
make interoperability-tools-install
make test-interoperability
```

Installation downloads the exact upstream source through `gh`, verifies its path-and-content SHA-256 digest and builds the test-only image. It checks the client version, Search help, Uvicorn TLS/server factories and Python dependencies, and installs the repository Go toolchain/module prerequisites. `output/interoperability/installation.json` binds the source, locks, image ID and Go executable; `dependencies.json` records installed Python packages.

Reinstall when client/build locks, the Containerfile or root Go module locks change. Missing or stale prerequisites fail checks instead of triggering downloads.

Each check also probes navigation help and required upstream entry points offline, including for previously installed images, before building the Catalog. The existing image supplies these capabilities; no additional package or research checkout is required.

Checks build current `catalogd`/`catalogctl` binaries from cached Go inputs and create an invocation-owned scratch fixture image and internal Docker network. The existing `catalogctl smoke` workflow authors, reviews and publishes three synthetic resources through the real API. A second snapshot enables authenticated-host checks.

State is entirely in memory: no PostgreSQL, existing Compose project, Minikube, real credential, model or HF account is used.

The client shares only the fixture container's network namespace. It queries anonymous `127.0.0.1` and authenticated `localhost` on port 8080 inside that namespace, not the host's port 8080. No ports, host networking, Docker socket or developer home are exposed.

Containers use non-root users, read-only roots, no capabilities and bounded memory/PIDs/CPU. They have no writable temporary mount or RAM disk. Docker-injected proxies are cleared; HF online access and telemetry are disabled. The client target binds this harness directory read-only. It also mounts one writable, non-secret progress file from a private, disk-backed directory. No other report files are exposed to the container.

The peer target additionally mounts its owned TLS and policy inputs.

Targets run alone and ignore deployment `.env` files. There is no option to select an existing Catalog, hosted Registry or arbitrary HF command. Default unit/coverage/verify gates do not run the client; pure assertion tests and service-free functional runner tests use their existing selections.

Ruff and Hadolint cover maintained Python and the Containerfile through `make check`.

## Helper verification

Run `make dev-tools-install` first. The Go wrapper uses `.tools/dev/bin/python` when available; otherwise, it uses `python3` from `PATH`.

Run isolated Python helpers and local functional tests separately:

```sh
make test-tooling-unit
go test -tags=functional ./test/interoperability -shuffle=on -count=1
```

Units use injected responses and errors to check metadata, filters, continuation, setup failures, source changes and cleanup. Functional helper tests use owned HTTP listeners and real subprocesses. They verify complete, failed, incomplete and contradictory child reports across the process boundary. Neither selection imports or executes HF. Run `make test-interoperability` separately for the unchanged native client against the actual local Catalog.

## Matrix and reports

The fixed inventory has 35 cases:

| Cases | Assertions |
| --- | --- |
| 13 Search cases | Each published Agent/MCP/Skill, combined Search, Registry-base and explicit-Search URLs, no matches, one-result limit and continuation token, MCP filtering, private reader, missing/invalid/wrong-tenant credentials and human output. Preserve identities, names, types, inline/URL metadata, source, scores and duplicate/referral checks against each host's own immutable manifest. |
| 9 navigation cases | Public origin, explicit current/predecessor documents, MCP/Skill filters, no matches and three private-manifest cases. Assert exact discovery diagnostics and metadata from the selected profile; the current-document case must successfully query Search, with scores beating static fallback. |
| 4 decoder controls | Real loopback HTTP through each unchanged Search/navigation decoder. Full results succeed; equally valid current-profile sparse results fail specifically for the older full-entry requirements. |
| 1 credential/source control | Two owned loopback origins demonstrate missing manifest authorization, synthetic-token forwarding to the other Registry and replacement of the raw result source with the queried Search URL. |
| 6 native filter cases | Three match/contradiction pairs: license plus full topic IRI; A2A skill, default input/output modes and interface binding/version; MCP transport and protocol version. Each negative request changes one condition and must return zero matches. Check exact membership and all published metadata. |
| 2 native continuation cases | Follow all three entries with page size one and unchanged order, scores and source; reject missing/repeated tokens and require no final token. Reuse a first-page token with a changed license filter and require HTTP 400 with the exact ARD `INVALID_ARGUMENT` response. |

The eight native cases call unchanged `discover.navigation.search_registry` with upstream `SearchRequest` and `SearchQuery`. Its serializer and decoder remain unchanged. The full topic IRI is `http://purl.org/dc/terms/subject`; this avoids query-context aliases that the pinned HF model cannot encode. Filters use the existing smoke listing metadata and checked-in Order Agent/MCP cards, without another seed or dependency.

The native child makes 17 Search calls, each with a ten-second transport timeout, within a 180-second child deadline. The three-page walk compares an independently fetched unpaged response and immutable manifest. It checks public snapshot continuation, not federated live-result windows. The CLI still has no general structured-filter or continuation-token input.

Navigation from an origin selects the predecessor manifest; its Skill media type differs from current ARD. Static results have score 50 and the manifest URL as source. Registry contributions retain their score while HF replaces source with the queried Search URL.

The navigation check requires successful Registry discovery separately from returned results, so static fallback cannot conceal a failed request. Control listeners exist only inside the isolated client namespace; request sizes, socket time, subprocess time/output and cleanup are bounded. Artifact URLs are metadata and are never fetched or invoked.

Case failures do not stop the remaining cases; setup failure or the aggregate deadline can produce an incomplete run, which fails. The supervisor rejects incomplete or contradictory reports and returns nonzero on failed qualification, setup or cleanup. `output/interoperability/run-*/result.json` identifies source/harness fingerprints, client image/revision, start time and case outcomes.

Each case has an assertion `status` and a fixed `disposition`: supported, profile-gap, client-limitation or filter-distinction. A reproduced limitation can pass its assertion without claiming the capability works. Search HTTP status is inferred from CLI completion or its explicit HTTP diagnostic; navigation reports per-endpoint `discovered` diagnostics and never infers HTTP 200 from exit 0.

Decoder controls additionally observe the actual successful fixture exchange. `smoke.json` retains synthetic seed identities; stderr is bounded and bearer values are redacted. Keep Search and navigation/HTTP measurements with their source identities under ignored `output/`.

Containers, network, fixture image and temporary binary/configuration files are removed on success, failure, timeout and interruption. Reports and the installed HF tool image remain reusable; there is no global Docker prune or volume reset. SIGKILL/host failure cannot run cleanup: remove only that invocation's named `open-agentic-catalog-hf-<id>` resources if necessary.

Normal SIGTERM/Ctrl-C uses scoped cleanup; cleanup errors report the exact owned names. `result.json` also identifies every owned container, network and fixture image. During native traversal, `progress/phase.json` records `native-pagination-page-1`, `-2` or `-3`. This diagnostic file supports interruption checks; it cannot authorize a passing result. A passing run still requires all 35 outcomes and successful cleanup.

## Local HTTPS peer qualification

Install the same pinned image, plus the existing host-side ARD requirements and OpenSSL. The HF image supplies Uvicorn but not jsonschema; the unchanged checker runs on the selected host Python. Use a non-root Linux user with access to the local Docker engine.

No additional image or research checkout is required.

```sh
make dev-tools-install
make test-interoperability-hf
```

Run the target alone. It ignores deployment `.env` files, honors `PYTHON`, rejects optimized Python that disables assertions, and never installs missing tools. Preflight checks the exact jsonschema version, pinned schema, OpenSSL and existing image capabilities.

One valid and nine invalid Search-response calibrations exercise the actual schema and HTTP-envelope validator before setup.

The target starts unchanged `discover.challenge:create_challenge_app` and `discover.server:create_app` under Uvicorn TLS. Each has an explicit private IP on the invocation-owned internal bridge and a certificate bound to those IPs. Keys are owner-readable under a protected temporary directory; the non-root peers receive read-only file mounts.

Public Catalog egress is disabled. Private profiles allow only the two exact origins, addresses and synthetic tenant, using the owned CA; an otherwise equivalent untrusted profile supplies a TLS-negative control. The API remains loopback-only, and phase clients share its namespace.

No host ports, real credentials, backend services, artifact downloads or hosted HF calls are used.

The separate fixed inventory has 26 cases:

| Cases | Assertions |
| --- | --- |
| 7 direct wire cases | Eight/two manifest entries, absent current path, challenge Search versus unavailable primary backend, independent pinned EntryFields and HTTP-envelope checks, ten filter controls, one-result bound, query-context pairs, referral behavior, optional List/Explore and operational-base versus Search-route spelling. |
| 6 official checker cases | Manifest, publisher and Registry for each peer. All challenge modes pass; primary manifest/publisher pass while Registry fails specifically on HTTP 500 Search. Manifest and publisher require actual schema evidence for all eight/two entries. Raw exit, output, warning lines and summary counts remain separate from assertion disposition. |
| 2 HF navigation cases | One/five Registry caps produce six/thirteen unique results, with successful discovery on the owned origin. These are client bounds, distinct from Catalog's hop policy. |
| 5 onboarding/admission cases | Root and explicit-manifest inspection, indexed/manual API approval and worker Jobs, ten/two current observations, raw evidence and three supported challenge projections; exact selected Search routes, missing-List mode rejection, failed primary Search inspection, wrong-tenant/untrusted-CA/anonymous-assignment rejection. |
| 4 live/result/route cases | Rich referrals contact exactly the root and its approved child. An available grandchild is separately approved but lies beyond the root's one-hop limit. Phase-specific bounded access-log counts reject both unapproved and beyond-hop calls. Raw `discover:challenge` results fail Catalog HTTPS-source admission, producing partial diagnostics; three local published results remain intact with the public ARD partial signal and no private diagnostic disclosure. |
| 2 outage/recovery cases | Stop only the challenge at its stable origin, require a newly failed snapshot and preserved complete ten-member view, then restart it and follow the same Job to a later successful attempt. Audit evidence binds each snapshot to its exact source, tenant, Job and attempt; an old failure cannot satisfy the outage. Current keys/digests remain identical with zero false additions, updates or withdrawals; immutable history may grow. |

`output/interoperability/peer-*/result.json` records its own exact inventory, Catalog/HF/source/tool identities, fixed dispositions, owned resource names and cleanup. The harness fingerprint includes the checker wrapper, requirements, pinned profile and shared helpers. Failed cases remain failures; later independent wire/checker cases continue, while a failed prerequisite phase marks dependent cases incomplete.

The scenario has a 20-minute overall deadline in addition to request/process/output bounds. Bounded Uvicorn access logs supply only synthetic method/path/status counts; no authorization headers or request bodies are captured.

All peer containers, phase clients, network, fixture image, temporary binaries/configuration and private keys are removed on success, failure, timeout or ordinary interruption. Child reports must match their phase and exit status; malformed evidence cannot bypass cleanup. Reports and the installed tool image remain.

For an uncatchable host failure, use only the exact names in that run's `phase.json` or `result.json`; resource intent is recorded before setup and each client launch. Never prune retained environments. Repeated runs compare source fingerprints and fixed outcomes, not ephemeral addresses, IDs or origin-dependent document hashes.

The official publisher summary counts ten/three warnings but displays eleven/four warning lines: the missing-current-path diagnostic is printed without joining its summary counter. Manifest summaries and lines both count nine/two. The report preserves both values.

Registry mode performs protocol checks rather than complete JSON Schema validation; the independent Search validator is required even when that mode exits zero.

HF limitations remain explicit: legacy-only discovery, rejected valid query context, nested referrals under `none`, malformed operational bases, non-HTTPS challenge source attribution and offline primary Search. Passing their characterization does not repair HF or establish native artifact validity, backend inventory transfer, adoption/publication, hosted behavior or production OIDC qualification. The Catalog's maintained federation suites supply valid HTTPS-source, sparse-result, exact-integer, filter and continuation positives; this target does not duplicate those matrices.

## Limits and maintenance

The client is `hf-discover` 1.3.7 at `49c927439fcaa8f210cfd42186c0641acef579fa`, with older full-entry ARD assumptions. Remote Search uses `--registry-url`, `--federation none`, `--kind all`/`mcp`, `--limit`, `--token` and `--json`. `--filter`/`--sdk` affect local HF searches only.

`--kind skill` selects `application/ai-skill`, not native Catalog Skill media types; compatible Skill metadata is tested through `--kind all` without relabeling.

The Search CLI cannot submit continuation tokens. Token presence is tested, not independent traversal. Both selected HF decoders reject valid current-profile sparse results.

Navigation omits the token on manifest reads and reports HTTP 401 even with a valid private reader token while exiting 0; direct authenticated Search still works. Navigation forwards its token to a discovered Registry on another origin. Keep credentials synthetic and destinations confined; do not relax Catalog authentication or use navigator output as unchanged upstream provenance.

List/Explore, conditional retrieval, Plugin interpretation, artifact fetching/installation/invocation, independent Registry servers and production OIDC/TLS remain outside the client-only matrix; the separate peer matrix above qualifies owned HTTPS HF servers. No HF implementation is patched. The loopback controls characterize client behavior and do not certify an independent server or all ARD operations.

```sh
go test -tags=functional ./test/interoperability -shuffle=on -count=1
make check
make verify
make test-interoperability
make test-interoperability-hf
make test-interoperability-hf
```

Set `RUFF=/path/to/ruff` when the required version is not on PATH. `test_unit_*.py` checks pure assertions, calibration faults and mocked supervisor/report behavior without HF, listeners or subprocesses. `test_functional_*.py` adds controlled short children, owned loopback HTTP and Make isolation; the functional Go wrapper runs both selections.

Existing checks cover archive safety, bounds, stamps, report integrity, confinement, redaction and cleanup. Actual HF execution is separate. Record product compatibility findings without weakening Catalog behavior or patching the client to force success.

The Containerfile pins Python 3.13.17 by digest. `tools.in` and generated `tools.txt` hash-lock uv 0.13.6, Hatchling 1.32.4 and build dependencies; refresh deliberately with the recorded `uv pip compile` command. Unmodified upstream `uv.lock` separately pins runtime dependencies.

Record the installed inventory too: upstream constraints can select a different compatible version of a shared build dependency. Source and dependency notices remain in the test image.

HF's [source license](https://github.com/huggingface/hf-discover/blob/49c927439fcaa8f210cfd42186c0641acef579fa/LICENSE) is MIT. The reviewed Python closure includes Apache/BSD/ISC/PSF terms and MPL-2.0 components (certifi, pathspec and applicable tqdm portions). These packages and the Python/Debian base remain local test tooling, outside distributed Catalog images and the Go runtime license allowlist.

## Independent Skill package consumption

Use `make skill-tools-install` then `make test-interoperability-skills`, separately, for locked offline skills-ref controls and a real CLI-selected immutable Git package handoff. The [Skill harness](skills/README.md) owns dependencies, direct calibration, inventory/authority refusals, source-bound reports and cleanup. No model, package execution, archive service or retained deployment is used.

## Independent Plugin loading

Run `make plugin-tools-install`, then `make test-interoperability-plugins`, separately, for pinned unchanged OpenHands portable loading and a real CLI-selected, verified HTTPS archive handoff. The [Plugin harness](plugins/README.md) owns direct controls, immutable inventory checks, isolation, dependency/license limits and cleanup. Only owned offline fixtures are used; no components, models or agents are executed.

## Fresh Directory deployment

`make agntcy-tools-install` acquires the pinned Directory source, CLI, chart and five upstream images. `make test-minikube-agntcy` deploys that chart beside the current Catalog API/worker and separate PostgreSQL in an owned Calico cluster. Each target runs alone. The [Directory guide](agntcy/README.md#fresh-owned-deployment) owns prerequisites, image/build checks, local schemas, TLS, isolation, reports and interruption cleanup. The same run qualifies the exact native corpus, paging/filter behavior, four-kind dispositions, all four MVP groups and PostgreSQL source/Job/snapshot bindings. It preserves native conformance failures and does not claim nonempty ARD federation. The retained-lab target stays separate.

## Local ANS discovery qualification

Run `make ans-tools-install`, then `make test-interoperability-ans`, separately. The [ANS guide](ans/README.md) owns pins, prerequisites, native controls, schemas, private TLS, isolation and cleanup. One fresh single-node Calico fixture deploys native RA/TL/Finder beside the current Helm Catalog API/worker and PostgreSQL. It qualifies live-only source approval, exact paging, ordinary and authenticated ARD Search, native revocation, stale-feed refusal and Finder outage recovery. Read-only SQL checks bind approval and audit evidence without indexed Jobs or snapshots. Native context, shared-identity and enumeration limits remain explicit.

## Enabled-verifier regression

Add `INTEROPERABILITY_TRUST_VERIFICATION=on` to the existing target:

```sh
make test-interoperability-hf INTEROPERABILITY_TRUST_VERIFICATION=on
```

The default is `off`. Other values fail before environment setup. This selection configures only the disposable test Catalog.
It loads the retained public ANS fixture keys through the existing application configuration. It does not deploy ANS or change production defaults.
The complete native and Catalog journey still runs. Ordinary discovery must create no trust Jobs or evidence requests.
The test then selects a source authority through the normal administrator API and synchronizes the new source revision.
It assesses an unchanged indexed entry through the actual worker and private-result API.
Missing ANS evidence must remain incomplete, with no receipt, checkpoint or status request.
Generic Job reads must omit the private result. Reader and other-tenant identities must not read it.
Reports retain the loaded verifier configuration and current Catalog image identity.
Existing peer capability limits and cleanup checks remain required.
