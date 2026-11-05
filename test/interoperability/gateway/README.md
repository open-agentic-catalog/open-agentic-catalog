# Local MCP Gateway Registry qualification

This explicit functional test deploys the pinned [MCP Gateway Registry](https://github.com/agentic-community/mcp-gateway-registry) beside the current Helm Catalog API and worker. Each has its own database and namespace in one disposable Calico Minikube cluster.

The selection covers native Gateway controls, PostgreSQL-backed Catalog enrollment and worker synchronization, native membership changes, outage recovery and reverse-import characterization. A passing characterization does not mean the peer passes ARD conformance or supports bidirectional federation.

## Install and run

Run these targets separately from the repository root:

```sh
make dev-tools-install
make gateway-tools-install
make test-interoperability-gateway
```

Use a non-root Linux AMD64 host, a local Unix-socket Docker engine, `gh`, and the [Minikube prerequisites](../../../documentation/src/content/docs/operations/deployment.mdx#maintained-local-minikube-verification). The selected Python must have the shared development dependencies. Prepare the canonical Catalog container build cache first. The fixture needs eight CPUs, 12 GiB memory and at least 12 GiB free disk. Its overall deadline is 40 minutes; `MINIKUBE_TIMEOUT` accepts 5 minutes to 1 hour.

Select the existing Kubernetes preload archive when available:

```sh
make test-interoperability-gateway \
  MINIKUBE_PRELOAD_ARCHIVE=/absolute/path/to/preloaded-images.tar.lz4
```

Installation acquires immutable GitHub source through `gh` and digest-pinned images. It creates `.tools/gateway/source`, `installation.json` and `closure.json`. These ignored, reusable inputs contain no runtime credentials. The check target verifies them without installing or downloading missing tools. Neither target loads deployment `.env` files. The supervisor excludes inherited provider credentials, proxies and cluster selectors.

`make clean` removes `.tools/gateway/`, including the acquired source and installation evidence. Restore it with `make gateway-tools-install` before qualification. Cleanup preserves images, retained runtime inputs and reports; see [cleanup scope](../../../documentation/src/content/docs/contributing/build-and-release.md#clean-local-builds-and-dependencies).

## Exact peer inputs and licenses

[lock.json](lock.json) owns the source revision, complete source-tree digest and Linux AMD64 Registry/MongoDB image digests. The Registry image's revision label, all Registry/auth Python files, native entrypoint, bootstrap script, scope JSON files, nginx templates and selected configuration must match that checkout.

The upstream auth image is unavailable at the selected source revision. [Containerfile.auth](Containerfile.auth) copies unchanged auth files and its native entrypoint over the exact Registry image. This offline layout step uses the same installed dependencies. It does not patch source or resolve packages.

The closure inventory records every installed Python distribution, dependency metadata, license classifiers and license-file hashes, plus OS packages and copyright-file hashes. The source also retains the upstream frontend lock. This is HTTP API qualification; the peer's browser application is not executed.

Gateway source uses Apache-2.0. Its full image also includes other licenses, including GPL-3.0 for `pcodedmp` and Docutils' mixed terms. MongoDB uses [SSPL](https://www.mongodb.com/legal/licensing/server-side-public-license), which is not an OSI-approved open-source license. The [MongoDB FAQ](https://www.mongodb.com/legal/licensing/server-side-public-license/faq) explains its internal-use and hosted-service boundaries. These inputs are separate local test tools. They are not shipped Catalog dependencies or images for redistribution. Keep their licenses and inventory; do not extend the Catalog's imported-code license allowlist.

## Native controls

The fixture runs native registry-only nginx, its auth server and MongoDB 8.2. The unchanged bootstrap script initializes `rs0`, indexes and original privileged scopes. MongoDB uses the peer's private, no-auth local mode; this is not a production deployment recipe. All state uses ordinary disk-backed Kubernetes volumes.

The canonical origin is `https://gateway.gateway-qualification.svc.cluster.local` on port 443. Native nginx serves private TLS on port 8443 behind the ClusterIP Service. The private-key Secret mounts at `/etc/ssl/private` because the image's original directory is root-only. Tests require the owned CA and independently reject that issuer without it.

Synthetic admin and group-scoped static keys pass through native nginx and `/validate`. Native group-import APIs configure restricted scopes. No identity headers are forged. Four inert command recipes are registered through native APIs: two enabled/public, one disabled and one private/group-restricted. Their executable path does not exist; no MCP command is executed. Demo registration, security scans, telemetry, update checks and model downloads are disabled. No hosted federation source or OAuth provider is configured.

The native matrix checks:

- The manifest at the predecessor discovery path, the current well-known path's native 404, exact public membership, Registry advertisement and canonical publisher/descriptor URLs. A discovery path does not select the payload's profile.
- Admin List/Search membership, restricted reader/private membership, short-kind/media-type filters, tags and empty results in both interfaces.
- Complete one-item pages, unique identifiers, non-repeated continuation tokens and terminal pages.
- Missing/invalid key refusals, restricted mutation denial, disabled/private anonymous descriptor denial and malformed filter/cursor handling.
- Real nonempty lexical Search with no installed model or inference service.
- Independent pinned ARD entry, manifest, List and Search schemas, including native null serialization.
- Descriptor checks against both the experimental MCP Server Card and MCP Registry schemas; their formats are distinct.
- Unchanged official manifest, publisher and authenticated Registry checkers, with JSON Schema available. Explore's native 405 remains a separate result.

The selected peer has four compatibility limits: the current discovery path returns 404; Registry responses serialize invalid optional nulls; MCP Registry documents are advertised as MCP Server Cards; and Explore returns 405. The test requires these exact measured outcomes. It rejects extra schema errors, different failure causes or incomplete checker output. Native authentication refusals return valid JSON error envelopes. Working native List/Search behavior does not establish Catalog admission.

The official manifest and publisher modes pass. Publisher mode warns that it used the predecessor path. Registry mode fails on Explore's 405. Its probes do not validate complete Registry response schemas, so independent schema checks remain required. The Registry self-entry also lacks representative queries; the checker warns that this entry is not discoverable through semantic indexing.

The request helper refuses redirects, limits responses to 1 MiB and allows at most 128 recorded calls. It never fetches a remote schema. A response containing a synthetic credential fails before its body is retained. Official checker execution has a 45-second and 128 KiB bound per mode. The native phase has a five-minute deadline.

## Catalog enrollment and lifecycle

The same target runs eight phases after the native controls:

| Phase | Required result |
| --- | --- |
| Bootstrap | Disable the native Registry advertisement through its existing setting. Inspect and enroll the unmodified two-entry manifest as an indexed source. Reject reader, wrong-tenant and untrusted-CA requests. |
| Add | Register and enable a third native server. Complete a worker sync with one added member. |
| Disable | Disable one native server. Complete a worker sync with one withdrawn member. |
| Restore | Enable that server again. Complete a worker sync with restored membership. |
| Outage | Stop only the owned Gateway. Bind a new failed snapshot to its exact Job attempt. Preserve the last complete snapshot and current observations. |
| Recovery | Restart that Gateway. Complete the same Job at a later attempt, without membership or digest changes. |
| Advertised interface | Enable the native Registry advertisement. Inspect the changed interface. Preview and remove the old source with its current ETag, then enroll the new indexed source. Preserve the prior snapshot. Retain the Registry self-entry as an unknown kind. Distinguish Search authentication refusal from malformed JSON rejection. |
| Reverse | Configure the native importer against a healthy owned Catalog manifest. Require its private-address refusal before any manifest HTTP request, zero imports and unchanged native inventory. |

Every complete synchronization compares exact manifest and entry bytes, digests, current membership, source/tenant attribution and occurrence binding. Snapshot attribution comes from occurrence responses, not Observation fields. Owner read-only SQL independently checks retained bytes, source revision, Job, audit attempt and last-complete pointer. External ingestion must not create a governed Resource.

The fixture uses normal Catalog credential bindings restricted to one tenant and the exact Gateway origin. Its own CA is required. API and worker resolved network-policy digests must agree. Changing the Registry advertisement updates the Pod template and drains the previous Pod before inspection. The peer source remains unchanged.

Live Search is not selected: the native Search envelope fails independent validation. Working authenticated native Search does not justify relaxed Catalog admission. A source's interface remains fixed. Explicit inspection and reviewed replacement prevent an advertisement change from silently changing it.

The reverse target is a narrow HTTPS control that forwards the actual Catalog manifest without translation. A successful byte comparison and request counter qualify its availability. The stock importer refuses its private DNS/IP destination. Native sync nevertheless reports successful zero imports and advances its generation; the report preserves that misleading peer outcome. This is a peer limitation, not successful reverse ingestion. The harness never disables the public-address guard or exposes a public endpoint.

## Catalog, isolation and results

The same fixture builds current Catalog source with one managed version/commit/source-state stamp. It verifies actual migration/API/worker image identities, both roles' version responses, served OpenAPI bytes and a normal governed publication smoke workflow. The Catalog uses PostgreSQL 18, owner-only migration and separate exact API/worker roles.

The peer namespace can reach only cluster DNS and selected local component ports. Catalog runtime Pods can reach the peer's private HTTPS endpoint. Real workload-network probes require healthy allowed controls around denials to an unrelated live destination, Catalog PostgreSQL and worker operations. The fixture never reuses retained Compose, another Minikube profile or the Directory research lab.

Reports reside under ignored `output/interoperability/minikube-gateway-<id>/`. `result.json` retains progress, owned names, source/chart/harness fingerprints, installed inputs, managed build fields and running image identities. Its `report.gateway` object includes native results, lifecycle results and PostgreSQL assertions. `qualification.json` retains the native phase's original HTTP bodies as base64, hashes, statuses, schema errors and official outputs. `catalog-<phase>.json` preserves each host phase; `lifecycle.json` collects those results and the reverse guard diagnostic. Credential values are excluded or redacted.

Each lifecycle phase has a three-minute process deadline. Catalog polling has a 120-second completion bound and a 45-second failed-attempt bound. Native requests have a 30-second timeout. After the advertisement restart, a complete native Search control qualifies readiness before the Catalog's shorter probe budget. This preserves the peer's lazy import behavior without installing a model or changing Catalog timeouts. Responses remain limited to 2 MiB for Catalog reads and 1 MiB for native reads; phase captures have explicit count limits. Synthetic retry budgets are fixture settings, not production defaults.

Check `passed` and `cleanupPassed` separately. Native schema/checker failures remain visible even when their characterization succeeds. Missing evidence, wrong identities, unexpected membership, source changes or failed cleanup fail the target. Classify findings and verification limits in the local run report.

Run environment-owning scenarios sequentially. SIGTERM or Ctrl-C uses bounded cleanup for the owned profile, network, image aliases, port-forwards and private directory. Cached pinned inputs remain reusable. SIGKILL or host failure can prevent cleanup; use the recorded exact names for recovery. Never prune unrelated containers or change host limits to make the scenario pass.

## Helper tests

```sh
make test-tooling-unit
go test -tags=functional ./test/interoperability -shuffle=on -count=1
make test-minikube-contract
```

Units use files, injected HTTP responses and fabricated report/image inventories. They do not start services or import peer application code. Functional helpers cover real local Make/process/network boundaries. The explicitly selected Gateway target owns real peer, database and cluster execution. These outcomes are not Go statement coverage.

## Enabled-verifier regression

Add `INTEROPERABILITY_TRUST_VERIFICATION=on` to the existing target:

```sh
make test-interoperability-gateway INTEROPERABILITY_TRUST_VERIFICATION=on
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
