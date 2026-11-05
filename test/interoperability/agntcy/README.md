# Local AGNTCY interoperability

The retained-lab target below uses an explicitly selected, already running Directory research lab and a freshly built memory Catalog. It qualifies one positive A2A artifact path and characterizes the pinned peer's ARD limits. It does not provision a cluster, install dependencies, run a hosted Registry or qualify automatic federation.

```sh
make test-interoperability-agntcy \
  AGNTCY_LAB_DIR=/path/to/agntcy-dir-lab \
  PYTHON=python3
```

Run alone. The target ignores deployment `.env` files, honors `GO` and `PYTHON`, and rejects optimized Python. Install the repository Go toolchain/modules and the root pyproject dev group through `make dev-tools-install` explicitly first.

Runtime checks use cached Go inputs, the selected Python with jsonschema 4.26.0, local Docker, kubectl and Helm. Missing or changed prerequisites fail instead of triggering downloads or cluster changes. Linux AMD64 is the qualified lab platform.

## Lab prerequisites

This target requires an existing lab with the layout below. The lab's own README owns startup, access and removal instructions. For a fresh deployment, use the [separate Minikube workflow](#fresh-owned-deployment). The retained-lab target requires:

- Unmodified `dir/` at the pinned revision; `artifacts/dirctl-linux-amd64` and `artifacts/dir-v1.7.1.tgz` with the hashes in [lock.json](lock.json).
- `private/state.json` selecting the lab's own `private/kubeconfig`, `private/minikube`, `agntcy-ard-*` context and `agntcy-research` namespace. Kubernetes access must use verified loopback TLS without external authentication plugins.
- Six Ready workloads: the five pinned upstream images and the existing schema-router fixture image. The deployed chart and exact reviewed network-policy specifications must match the lock. These configuration checks supplement the previously measured isolation evidence; they are not a fresh network-policy enforcement drill.
- Native HTTP on `127.0.0.1:18889`, native gRPC on `127.0.0.1:18888` and the fixed TLS gateway on the selected local Docker bridge at `172.25.0.1:18443`. The gateway preserves Directory response bytes. `private/tls.crt` must still be valid; expired or incorrect trust fails verification.

The native CLI gets an empty invocation-owned configuration/cache directory, explicit insecure loopback gRPC and no inherited credentials or proxies. Kubectl receives an explicit owned `--cache-dir`; XDG data and temporary files also stay under that run's private directory. It never uses import, signing, routing publication or hosted services.

The temporary Catalog receives synthetic development identities, disabled public egress and one tenant-bound private profile for the exact lab TLS origin/address/CA. The retained research Catalog is not contacted. Declared Agent endpoints remain metadata and are never invoked.

## Assertions and evidence

| Group | Required outcome |
| --- | --- |
| Artifact | Push, pull and export the exact fixture CID. Preserve the card through normal Catalog Resource/version creation, pinned A2A validation, explicit revalidation and retained-document/evidence readback. |
| Discovery | Prove the selected native record exists; compare direct and TLS manifest bytes. Inspect and approve the empty ARD manifest at the predecessor URL, complete a real worker Job, and verify its zero-member snapshot, source pointer and zero current observations. |
| Search | Require HTTP 400 and native code 3 for the ARD query-object refusal. Require HTTP 503 and native code 14 for the missing extractor. Check both diagnostic messages. Catalog explicit Search inspection must reject with `INVALID_ARGUMENT`. |
| Conformance | Run unchanged pinned manifest/publisher/Registry checkers with mandatory schema tooling. Empty manifest/publisher checks pass with warnings; Registry fails exactly on missing List `items` and Search HTTP 400. Independently validate the selected raw entry, retaining its legacy identifier and two object-valued metadata errors. |

`output/interoperability/agntcy-*/result.json` records the four groups, input/build/image identities, actual checker output, warning lines versus summary counts and cleanup. Native Registry failure remains failure even when the characterization passes. Registry mode does not perform complete entry-schema validation; the independent raw-entry check is required.

Missing groups, changed errors, skipped schemas, source drift or cleanup failure make the target fail. Setup failures leave dependent groups explicitly unrun. Reports and captures contain only the selected synthetic fixture, not lab secrets.

Requests, response bodies, process output and execution time are bounded, with a 15-minute aggregate deadline. Each run removes its Catalog process, binaries, synthetic configuration and temporary CLI directories on success, failure or ordinary interruption. The lab and deterministic seed remain.

Repeat runs require identical existing CID content before pushing the same record; no other record is deleted or overwritten. Reports persist. After an uncatchable host failure, inspect only that run's `phase.json` ownership record before removing its process/private directory; never prune the lab or unrelated Docker resources.

Run the target twice, sequentially, to check repeatability. Compare Catalog source/binary and harness fingerprints, lab identities, the fixture CID/card digests, all four outcomes and native checker evidence. Fresh Catalog Resource/revision/source/Job/snapshot IDs must differ; the existing Directory seed must be reused unchanged.

Both reports must show successful cleanup. This checks repeatability against the selected retained lab, not a clean-cluster installation.

For an interruption check, send SIGTERM to the invocation's Python runner after it prints `AGNTCY phase: conformance`; the preceding groups have exercised the active Catalog. Identify only that Make invocation's child, never processes by a broad name match. Its report must fail qualification, retain the interruption and mark unfinished groups unrun, while cleanup passes.

Check the owned Catalog PID and private directory from `phase.json`/`result.json`, and confirm its API listener is closed. Compare retained lab workload/container and record identities before and after; do not signal the lab's processes or delete its state.

Service-free helper checks run with the existing interoperability unit/functional selections; they never execute Directory, Docker or Kubernetes. They cover failed setup, malformed evidence, signal registration, owned process termination and cleanup failures. Follow the [testing guide](../../../documentation/src/content/docs/contributing/testing.md#local-agntcy-interoperability) for the maintained workflow.

## Fresh owned deployment

The separate `test-minikube-agntcy` target creates a disposable cluster. It does not require `AGNTCY_LAB_DIR` or the retained research lab.

```sh
make agntcy-tools-install
make test-minikube-agntcy MINIKUBE_PRELOAD_ARCHIVE=/absolute/path/to/preloaded-images-k8s-v18-v1.35.1-docker-overlay2-amd64.tar.lz4
```

Run each target alone. They ignore deployment `.env` files. The deployment target uses the [common Minikube prerequisites](../../minikube/README.md), Linux AMD64, the Apache `htpasswd` tool, eight CPUs, 12 GiB memory and at least 12 GiB free disk. The overall deadline is 40 minutes. Prepare the canonical image build cache through the normal [container workflow](../../../documentation/src/content/docs/contributing/build-and-release.md) first. Dependency acquisition belongs to that build stage; application compilation, license collection and router compilation run offline. A preload archive is optional; without one, Minikube acquires its pinned Kubernetes inputs.

The installer uses `gh` for GitHub downloads and Helm for the released OCI chart. Fresh deployment uses Directory v1.8.0 at commit `337ddd89721bd66595c701c9675d6bfbb5a65997`. The installer verifies [deployment.json](deployment.json), including the source hash, CLI version, chart checksum and five Linux AMD64 image digests. It stores the source license and CLI dependency inventory under `.tools/agntcy/v1.8.0/`. The retained lab keeps its separate v1.7.1 [lock](lock.json), tools and exact admission checks.

Execution verifies those installed inputs without reinstalling them. Docker must select a local Unix-socket engine; the same selected context is retained through acquisition and inspection.

`make clean` removes `.tools/agntcy/`, including the acquired source, CLI, chart and installation evidence. Restore these inputs with `make agntcy-tools-install` before fresh deployment qualification. Cleanup preserves images, retained reports and external lab checkouts; see [cleanup scope](../../../documentation/src/content/docs/contributing/build-and-release.md#clean-local-builds-and-dependencies).

The deployment creates one single-node Calico profile and private kubeconfig/home. Catalog runs in `open-agentic-catalog-e2e`; Directory runs in `agntcy-qualification`. Catalog uses its current application chart, API, worker and separate PostgreSQL 18 with exact runtime identities. Directory uses its unmodified released chart, PostgreSQL, Zot, OASF server and reconciler. Verified upstream images receive invocation-owned local aliases for Minikube import. The report retains each immutable upstream digest and configuration ID, plus the actual local alias. Workload IDs must match those verified inputs; workloads use `imagePullPolicy: Never`. The Bitnami image-name check permits these verified aliases; native validation and TLS checks stay enabled. The fixture adds a namespace resource limit and two small routers built from [reviewed source](router/main.go). It creates no cluster-wide peer permissions or ingress.

The schema router forwards only GET/POST under `/api/1.0.0/` to the local OASF server's `/api/` routes. The HTTPS gateway preserves native discovery/List/Search/Explore status, selected headers and response bytes. A direct HTTP control verifies that the optional Explore 404 comes from Directory, with identical bytes through HTTPS. Both routers have fixed upstreams, no redirects or proxies, a 2 MiB request limit, a 4 MiB response limit and five-second upstream timeouts. They do not translate entries or repair native API envelopes.

Fresh credentials, a matching BCrypt Zot password file and a private CA are generated per invocation. The password enters `htpasswd` through private stdin, not command arguments. Native OASF validation uses `policy.operations` for push, autosync and indexing. Both server and reconciler use the owned schema router. Public bootstrap, remote synchronization, scanners, optional model service and all peer chart hooks are disabled. Reconciler identity checks, pruning, publication and policy evaluation are also disabled. Services use ClusterIP with no external traffic policy. Peer Pods can reach only selected local component ports and cluster DNS. Catalog API/worker can reach the exact private HTTPS gateway. Tests use actual workload network namespaces and healthy controls to prove unrelated egress and peer access to Catalog PostgreSQL/worker operations are denied.

The complete fresh qualification requires:

- Built and running migration/API/worker images from one managed version/commit/source-state stamp; byte-exact served OpenAPI and normal governed publication smoke.
- Five pinned upstream workloads and two current router workloads, all Ready with matching image identities.
- Direct and HTTPS manifest/List byte equality, correct private trust and rejection for an unknown certificate authority.
- A real local versioned schema response and rejection of an unselected schema version.
- Allowed Catalog-to-gateway traffic, denied traffic with healthy controls, stable source/chart/harness fingerprints and successful cleanup.
- Six exact content-addressed records in native storage and structured search, with five projected entries across all one-item pages. The non-projectable record is excluded from both List and detail. Unknown records and the invalid/Skill fixtures are rejected.
- Unquoted and double-quoted A2A filters selecting four identifiers, the single-quoted example selecting none, an MCP filter selecting two and an unknown type selecting none. Native module selection includes the multi-module nested catalog; it is not equality against each returned top-level type.
- Current A2A push/pull/export and normal Catalog API validation/revalidation/readback. Older A2A, the projected current-A2A wrapper and native MCP data retain blocked evidence. The unwrapped current card remains a separate passing control. The selected Skill fails the peer schema; Plugin projection is unavailable. No listed workload is invoked.
- All four shared MVP groups, including exact native Search error causes, current ARD profile selection at the predecessor URL, real worker synchronization, explicit Search rejection, all three official checker modes and independent schema checks for all five raw entries.
- Native local-file A2A, MCP and Skill imports with explicit static enrichment, OASF 1.0.0, one-record limits and private client files. Check translation, original artifact bytes, real admission, repeated admission, malformed input and unchanged native inventory. Skill failures must identify both missing native fields and the rejected artifact field. The selected release rejects all three imports; no imported CID or subsequent export is claimed.
- Independent MCP profile checks distinguish the valid Registry input from the native IDE-settings export. Probe all four actual Publisher collection links before assessing nonempty federation. The selected peer has no manifest entries; its collections return native List envelopes that fail the ARD contract.
- Read-only PostgreSQL binding of the exact tenant, inspection, source/revision, Job payload/result, complete snapshot, retrieved-body digest, source pointer and zero current observations. Stored Job references use `id`; the public API projects that value as `snapshotId`.

`output/interoperability/minikube-agntcy-*/result.json` retains ownership before setup, the managed stamp, fingerprints, installed inputs, redacted Catalog/Directory values hashes, running images and completed checks. Its `batch` is `qualification`. `qualification.json` identifies the selected release and its expected outcomes. These expectations remain separate from the retained v1.7.1 checks. It retains native inventory, all four resource-kind dispositions, the four MVP groups, separate native-import results and a bounded index of original HTTP response captures. Checker output, generated import records, the exact manifest and raw response bytes remain beside the report. The projected-payload check requires blocked A2A/MCP evidence with exact codes and retained revision IDs. Search checks require the query-object refusal and missing-extractor diagnostic, not status codes alone. Native Registry failure and all 15 raw-entry schema findings remain visible even when the characterization passes. An empty manifest does not establish nonempty ARD federation.

### Native import limits

The pinned CLI embeds `dir-importer v1.5.5` and `oasf-sdk/pkg v1.3.1-0.20260910081500-f0c6172a010a`. Its translators add `modules[0].artifact`, which the pinned OASF 1.0.0 server schema rejects. Skill translation also lacks required `source_locator` and `validation` fields. First and repeated attempts must report the same schema failure and return no CID. The existing compatible manual A2A push/export path remains a separate passing control.

The CLI can exit zero after an import fails. Malformed JSON can produce an error while all summary counts remain zero. Assertions inspect counts, exact error causes, CID files and unchanged six-record inventory. A dry run proves translation only. It does not prove storage, deduplication or successful artifact export. Generated timestamps can change translated record digests between runs; original input and embedded-artifact digests must remain stable.

The MCP input reuses the local Everything Registry definition. The Skill input reuses the repository-review `SKILL.md` sample. Neither resource runs during this test. Native MCP export produces IDE connection settings, which fail both Catalog MCP profiles. There is no native Plugin mapping in the selected release.

Nonempty indexed federation requires a genuine valid Publisher manifest with entries. Registry/List/Search compatibility is required only for those interfaces. The current peer supplies neither nonempty compatible publication nor a valid Registry API. Do not fabricate a manifest, translate responses or disable native validation. Reports mark transfer, native changes, withdrawal and outage recovery as `unrun`. Revisit these checks when a reviewed, unmodified release supplies a compatible interface. An unexpected compatible response fails the current characterization and requires positive-transfer tests; it does not silently pass.

The host clients use owned loopback forwards; the gateway certificate includes the loopback address and service hostname. Both paths verify the private CA. The unchanged official checker uses the loopback HTTPS endpoint. Catalog API/worker use the exact namespace-local service origin. Client state and synthetic tokens remain in private temporary files, never command arguments or retained captures. Native structured search uses exact fixture-name filters without the optional model extractor.

Run the target twice sequentially. Compare pins, build/image identities, all three fingerprints, native CID/content membership, paging/filter outcomes, artifact digests and checker results. Fresh Catalog IDs, credentials, port numbers and Directory host identities can differ. Each manifest digest must agree with its own inspection/snapshot evidence. Require both assertion success and successful cleanup.

For a partial-setup cleanup check, send SIGTERM to the exact harness PID in its report while `phase` is `setup` or `cluster-ready`. For an active interruption check, wait for `qualification`, when the native client is active. The Python child interrupts its bounded native/checker process groups before the supervisor removes cluster resources. Confirm the process command is this invocation's `--scenario agntcy` before signaling it. Interrupted runs must exit unsuccessfully and retain `passed: false` with `cleanupPassed: true`. Verify that the reported profile container, network, unique images, private directory and owned port-forwards are gone. Never select processes by a broad name match.

Ordinary success and failure use the same bounded cleanup. Installed tools/images and reports remain for reuse; the old lab, Compose and other clusters remain untouched. After an uncatchable host failure, inspect the recorded exact ownership before cleanup. Do not prune Docker, tune the host or remove other profiles.

## Fixture provenance and pins

[card.json](card.json) is the synthetic current-A2A card constructed during the local investigation, with no executable workload. [record.json](record.json) retains its proven minimal OASF 1.0.0 wrapper, based on Directory's `tests/e2e/local/18_ai_catalog_sdk_test.go` at revision `7bf5795ce7f6b2c3069ba2da1a32768701412334`: Copyright [AGNTCY Contributors](https://github.com/agntcy), SPDX-License-Identifier: Apache-2.0. The test author label is retained metadata.

The [corpus](corpus.json) binds six stored records to fixed CIDs and projected media types. Its additional synthetic inputs under [corpus/](corpus/) preserve the reviewed lab fixtures, derived from the same pinned Directory SDK test data. The older A2A version, MCP, multi-module, plain, Skill and missing-field controls retain their upstream Apache-2.0 attribution. These are metadata and schema fixtures, not runnable resources.

The known CID binds its exact native content; the exported/retained card comparisons use semantic JSON equality plus SHA-256 evidence.

Directory source/CLI/chart are Apache-2.0; the OASF, Zot and PostgreSQL images retain their upstream licenses and image notices. All remain external local test prerequisites, outside distributed Catalog artifacts and the Go runtime license allowlist. The CLI hash agrees with the research download's provenance subject, but this does not claim publisher-signature verification. The [lock](lock.json) and [deployment inputs](deployment.json) distinguish chart archive checksums from OCI image digests. No floating pin is resolved during testing.

## Enabled-verifier regression

Add `INTEROPERABILITY_TRUST_VERIFICATION=on` to the existing target:

```sh
make test-minikube-agntcy INTEROPERABILITY_TRUST_VERIFICATION=on
```

The default is `off`. Other values fail before environment setup. This selection configures only the disposable test Catalog.
It loads the retained public ANS fixture keys through the existing application configuration. It does not deploy ANS or change production defaults.
The complete native and Catalog journey still runs. Ordinary discovery must create no trust Jobs or evidence requests.
Directory's native manifest is empty. The test records assessment as not applicable because there is no admitted entry.
Generic JSON at the predecessor URL selects the ARD profile. The URL alone does not select the older AI Catalog payload format.
Its HTTPS edge counts evidence paths, including rejected requests. A local control proves the observer is active.
Directory signatures remain outside the ANS verifier. Empty synchronization does not establish nonempty federation.
Reports retain the loaded verifier configuration and current Catalog image identity.
Existing peer capability limits and cleanup checks remain required.
