# Local ANS registration and discovery qualification

This test runs pinned, unchanged [ANS](https://github.com/agentnameservice/ans) Registration Authority (RA), Transparency Log (TL) and Finder binaries. It deploys them beside the current Helm Catalog API/worker and PostgreSQL 18. The owned single-node Calico cluster has separate peer and Catalog namespaces. It does not use retained Compose, a public registry or a retained Directory lab.

## Install and run

Follow the [Minikube prerequisites](../../minikube/README.md). Install the shared development tools first. Run each command separately from the repository root:

```sh
make ans-tools-install
make test-interoperability-ans MINIKUBE_PRELOAD_ARCHIVE=/absolute/path/to/preloaded-images-k8s-v18-v1.35.9-containerd-overlay2-amd64.tar.lz4
```

Installation acquires the exact source revision in [lock.json](lock.json) through `gh`. It uses the digest-pinned Go compiler to download Go modules into ignored `.tools/ans/modules/`. This acquisition container uses host networking for local WSL DNS compatibility and is removed afterward. Compilation runs offline. No native service runs during installation. The resulting scratch image contains only the unchanged RA, TL and Finder binaries. Separate artifacts retain binary build information, the module/package inventory, Go license and dependency license files. The MIT [source license](LICENSE) is retained beside the lock.

`make clean` removes `.tools/ans/`, including the private module cache and installation evidence. Restore it with `make ans-tools-install` before qualification. Cleanup preserves local images and retained reports; see [cleanup scope](../../../documentation/src/content/docs/contributing/build-and-release.md#clean-local-builds-and-dependencies).

Execution verifies installed inputs and builds the current Catalog, shared HTTPS edge and network fixture from cached compiler/dependency inputs. Missing or changed prerequisites fail. It creates a private kubeconfig, home, network, image aliases and cluster. The scenario requires eight CPUs, 12 GiB memory and at least 12 GiB free disk. Its overall deadline is 40 minutes. It changes no host limits, mounts, retained data or unrelated containers. The shared supervisor removes owned resources after success, failure or interruption.

## Native controls

RA uses a self-signed development CA, synthetic static credentials and explicit no-op identity/DNS checks. RA starts first. Each native component has an owned 1 GiB disk claim and uses `Recreate` to avoid concurrent SQLite writers. The claims survive Pod replacement within the test and are removed with its cluster. A read-only fixture helper exports its generated public key to configure TL. Finder consumes only acknowledged lifecycle events. These settings qualify local development activation, not production domain ownership or identity proof.

The test registers two MCP resources, one A2A resource and one HTTP-API resource through V1 register, verify-acme and verify-dns. It requires ACTIVE state and TL-acknowledged feed events. Search must return exactly the three supported identities. HTTP-API remains excluded; Skills and Plugins are unavailable. Tests compare identifiers, versions, endpoints, tags, capabilities and receipt metadata. Receipt URLs remain metadata in `test-interoperability-ans`; that target does not fetch or verify receipts. The separately selected assessment target below verifies actual evidence.

Native controls cover nonempty and no-match Search, type/tag/capability filters, all pages, Explore counts, invalid queries and query-bound continuation refusal. Maximum and default page size are both one. An additional registration characterizes MCP/A2A endpoints that share one identity and version. It does not invent unique identifiers for them.

A private HTTPS edge forwards allowed Search/Explore requests without changing response bodies. Tests compare direct and HTTPS bytes and reject untrusted TLS. Network controls require healthy allowed connections and denied peer access to unrelated services, Catalog PostgreSQL and worker operations.

The pinned ARD 0.91 entry and Registry response schemas validate actual response bodies. The unchanged official Registry checker runs separately; its Registry mode checks protocol shapes, so it does not replace explicit JSON Schema validation. Finder has no manifest or List endpoint. Record ARD context and native-extension outcomes separately from protocol-shape checks.

## Reports and scope

Private reports are written under `output/interoperability/minikube-ans-*/`. `result.json` retains ownership, pins, current source/chart/harness fingerprints, resolved image identities, both Catalog build identities, checks and cleanup status. `qualification.json` retains native phase evidence and response hashes. RA credentials, private keys and certificate requests remain private temporary inputs and are removed. Reports are test evidence, not statement coverage or full ARD certification.

## Catalog live discovery and lifecycle

The same target runs native controls and Catalog integration from fresh setup. It inspects the exact HTTPS Search endpoint and connects a live-only source through normal authenticated APIs. The fixture selects a private network profile, its CA, four exact publisher domains and the `localhost` authenticated discovery host. It publishes that host's local snapshot through normal candidate and activation commands. The anonymous host remains local-only. Untrusted TLS, another tenant and anonymous private-source assignment are refused. Indexed enrollment fails because Finder has no enumeration endpoint.

The Catalog allows three peer pages in this fixture. Each native page contains at most one result. Tests record bounded Search request bodies at the HTTPS edge and compare the actual continuation tokens with native responses. Administrator Search and ordinary reader Search retain the exact native entry, including its trust extension. Authenticated ARD returns the supported metadata projection. Tests require exact local/live membership, versions, media types, delivery URLs, scalar metadata and provenance. They reject missing, duplicate or unexpected results. Live-only Catalog cursors traverse the unchanged result window without duplicate identities.

Authenticated ARD results must preserve the exact native declaration as lowercase `trustManifest`, including the identity and registration receipt reference. Local results must have neither trust spelling. Missing, changed or uppercase declarations fail qualification. The original pinned Search schema runs unchanged. A separate validation copy maps lowercase trust to the pinned `EntryFields` definition; reports keep both outcomes and the checked-manifest count. This extra check does not claim that the original schema validates the lowercase extension.

The [composed HTTP contracts](../../../internal/platform/httpserver/ard_live_validation_functional_test.go) also check disclosure, exact framework values and cursor binding. Trust declarations remain unverified, and receipt/key URLs are not fetched. The fixture's private network profile permits only the Finder origin; receipt references do not grant network access.

One registration with MCP and A2A endpoints produces two native entries with the same identifier/version. Private administrator live Search preserves both exact variants for assessment selection. Ordinary reader Search merges that identity. A kind filter selects either native format. Valid context requests retain the native refusal; the test does not remove the context or patch ANS to manufacture success.

Native revocation must reach the acknowledged feed, Finder Search/Explore and fresh Catalog results. An older Catalog cursor then requires a restart. Separate RA and Finder outages preserve the same private disk claims and Service origins. The fixture selects a five-second feed-staleness bound. Finder's `staleSince` extension fails the original closed response schema; removing only that field in a diagnostic copy passes validation. The Catalog refuses the unchanged response and reports partial federation while preserving the exact local inventory. Finder unavailability has a separate failure result. After recovery, exact live membership returns and the same temporarily unavailable Catalog cursor returns the expected remaining identity.

Owner SQL reads bind the source, inspection, approved revision, authenticated audience and audit evidence. They also require no source snapshots, observations or synchronization Jobs for this live-only source. API and worker retain their separate exact database roles. Neither live discovery nor a source outage adopts remote entries as governed Resources.

Each phase has a bounded three-minute execution deadline. ANS fixture clients allow 12 seconds per HTTP exchange, exceeding the Catalog and edge dependency deadlines. The complete supervisor deadline remains 40 minutes. `catalog-*.json` retains phase responses and native hashes; the final `result.json` includes all lifecycle and PostgreSQL evidence. Receipts and resource delivery URLs remain metadata only. Passing characterization of peer limitations is not full ARD certification.

## Real local assessment qualification

Run this target separately after the same installed prerequisites:

```sh
make test-trust-verification MINIKUBE_PRELOAD_ARCHIVE=/absolute/path/to/preloaded-images-k8s-v18-v1.35.9-containerd-overlay2-amd64.tar.lz4
```

It reuses the native deployment, HTTPS edge, current Catalog image, PostgreSQL and cleanup above. All eight metadata lifecycle phases run first. The native RA/TL/Finder binaries remain unchanged. The selected edge also forwards exact receipt, checkpoint, bounded history and status routes to TL. No public service or resource endpoint is contacted. Operators supply authority in production; this fixture exports only its owned RA/TL public keys.

The target runs 30 trust phases with fixed assertion inventories and owner SQL checks:

| Scope | Required results |
| --- | --- |
| Disabled and enabled without a selected source policy | Ordinary discovery and refused requests create no assessment Jobs and make no evidence requests. |
| Explicit V1/V2 assessments | Every independent signature, checkpoint, inclusion, target and status check passes. Key fingerprints match the exported public keys. |
| Private boundaries and replay | Reader and other-tenant access is denied. Replays retain one Job without another fetch. Generic Job reads omit evidence. |
| Unsupported or corrupted evidence | Unsupported frameworks make zero evidence requests. Invalid signatures, fake proof roots, wrong checkpoints, missing status and HTTP 410 cannot establish current registration. |
| Checkpoint advancement | A native registration advances the log after receipt retrieval. The Catalog verifies the older proof against exact signed history. Missing or corrupted history cannot establish current registration. |
| Cancellation, source changes and restart | Queued and in-flight cancellation stop further acquisition. A paused source blocks completion. Worker replacement preserves queued work and exact historical bytes. |
| Dependency recovery | TL outage retries the same Job while discovery remains available. A queued assessment completes with Finder stopped. |
| Loaded policy and key changes | Matching API/worker rollouts qualify overlap, key removal, restoration and shorter freshness. Old results remain readable as historical evidence. |
| Native signer changes | RA changes first, then TL, on retained native state. Old/new signatures use exact exported keys. Removing an old key rejects dependent evidence while new registrations still pass. |
| Expiry, native revocation and disablement | Expiry cannot erase valid historical signatures. A queued V2 assessment cannot become current after native revocation. Disabling refuses new work and preserves private history. |
| Offline command | The command exported from the actual Catalog image verifies captured native bundles and rejects wrong-key, transplanted and tampered controls. |

Each loaded-policy transition drains the owned API and worker before the owner migration hook. The fixture uses a 30-second worker lease, leaving the required margin around its 15-second assessment attempt. These are test inputs, not changed product defaults. Each phase has a three-minute bound. The complete supervisor retains its 40-minute bound.

The initial loaded-key cases change accepted Catalog keys without changing native signers. Six additional phases qualify actual signer changes:

1. Restart the owned RA with `signer.keyId: ans-ra-signer-next`. Native startup creates the new private key on the existing claim. Export only its public key, register it through TL's authenticated producer-key API, and load both RA keys into the Catalog. Register `ra-next.ans.test`. Require old and new registrations to pass. Remove the old RA key from Catalog policy; old producer signatures must fail while the new registration passes.
2. Restore both RA keys. Restart TL with `attestation.keyId: ans-tl-attestation-next` and retain its origin, database, tiles, old keys and subjects. Export its new public key and load both TL keys. Register `tl-next.ans.test` to advance the retained tree and avoid cached old receipts. Require old and new registrations to pass. Independently verify captured checkpoint and status signatures with only the new TL public key.
3. Remove the old TL key from Catalog policy. An old sealed envelope must fail log-signature verification even when its fresh receipt uses the new key. The new registration must pass. Restore accepted-key overlap and verify both registrations again. Preserve earlier private results and exact evidence throughout.

These steps test controlled native restarts and Catalog policy rollouts. They do not qualify hot rotation or automatic key discovery. The offline command derives its policy from the deployed fixture settings, including keys, publisher domains and freshness limits. Every new assessment completes in one attempt and agrees with the offline result.

The checkpoint cases prepare a native registration before they pause the Catalog's checkpoint request. The test gate expires after six seconds and stops on request cancellation. Only native DNS activation and checkpoint publication occur during the pause. The Catalog keeps its existing request limits and shared attempt deadline.

The positive case requires four requests: receipt, latest checkpoint, exact-size history with `limit=1`, then status. It retains exact native receipt, checkpoint and history bytes. Missing history returns `checkpoint_history_missing`; a corrupted c2sp signature returns `checkpoint_binding`. Both negative assessments complete in one attempt and stop before the status request. The actual image's offline command must return the same complete assessment at the recorded time.

`result.json` adds `report.ans.trust` and `trustConfigurations`. The latter binds each rollout to the hash of its redacted Helm values. History phases include `checkpoint_history` with tree sizes, roots, the exact history path, original history hash and Job ID. Evidence request rows include response status and native/delivered hashes where a native response exists.

Signer phases include `signer_native` with full old/new public-key fingerprints, native tree growth, subject IDs and retained history hashes. The `tl-signer` and `signers-restored` phases also retain `nativeSignatures`. The Go supervisor authenticates exact checkpoints and status tokens against the new TL key for both old and new registrations. Short key hints alone cannot satisfy these checks.

The `trust/` directory contains per-phase captures, exact public V1/V2 bundles and public PEM files. SQL checks bind exact Job counts, source/entry/policy identities and completion audits. They require no source snapshots or observations. Private keys, credentials, command-export containers and temporary state are removed by owned cleanup.

Named corruption controls belong to the test edge. They do not patch ANS or replace native positive evidence. Unit and worker tests separately check certificate expiry and malformed signed encodings. Passing phases do not prove publisher ownership, global log consistency, artifact integrity or universal ARD signature support. Broad trust and rights remain `not_assessed`.

## Authentic registration evidence capture

Run `make capture-ans-trust-fixtures` after the same installed tools, native image and Minikube prerequisites listed above. The existing supervisor creates an owned disposable cluster, starts the pinned unchanged RA/TL/Finder, captures V1/V2 public evidence and removes its resources. The target does not install a Catalog, enroll a Source or activate verification.

The result directory is `output/interoperability/minikube-ans-<suffix>/trust-fixtures/`. It contains exact entry/receipt/checkpoint/history/status bundles, RA/TL public PEM files, an example policy and provenance. Check `result.json` for both capture success and cleanup success. No private signing keys, bearer credentials or public-domain ownership claims belong in maintained fixtures.

The [offline profile](../../../internal/standards/anstrust/README.md) defines byte limits, expected results and the verification command. Review exact source/image identities and expected outcomes before copying refreshed public evidence to its `testdata/native/` directory. Runtime tests must reuse this capture path when selected; do not create another native deployment harness.
