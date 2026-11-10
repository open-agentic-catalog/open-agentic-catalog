# Independent portable Plugin loading

```sh
make plugin-tools-install
make test-interoperability-plugins
```

Run each target alone. This explicit functional test installs unchanged OpenHands SDK 1.58.2 at `d2ee0cdea60825608c2f8906eb54e1d4d264fb42`, then calls its native `Plugin.load(directory)` for owned Agent Plugins 1.0.0 packages. The test driver discovers and downloads Catalog content; the SDK loads the verified directory.

No model, agent session, MCP connection, dynamic Skill rendering, component execution or installation into a developer home is requested.

## Prerequisites and isolation

Installation requires Python 3.10+, `gh`, Go, a local Linux amd64 Docker engine and network access to acquire pinned source, toolchain/modules and hashed wheels. The selected Python 3.13 image is digest-pinned. The check requires a non-root developer and the installed image/Go cache; it compiles its own current Catalog commands without network acquisition.

There are no public endpoints, exposed host ports, Compose changes, PostgreSQL or Kubernetes prerequisites. The Linux amd64 Python dependency lock does not qualify ARM64 execution.

Each native loader runs in a separate network-disabled, non-root, read-only container with only the owned read-only package/probe mounts. It has no Catalog tokens, TLS key, host home, Docker socket or provider credentials. Capabilities are dropped; no-new-privileges, 2 CPUs, 2 GiB RAM, 256 PIDs and a 128 MiB noexec/nosuid temporary filesystem bound execution; Docker inspection verifies the effective settings.

The Catalog and existing prepared HTTPS archive handler share their own separate loopback namespace. Their private CA, exact path/size/digest allowlist and synthetic identities never enter the native reader. Existing ordinary production fetch policy is unchanged.

The genuine SDK imports its wider dependencies and creates local caches/logs. Private HOME/OH_PERSISTENCE_DIR contain those effects; loading is not claimed to be free of all filesystem side effects. Supported `LITELLM_LOCAL_MODEL_COST_MAP=true` disables the dependency's import-time pricing download.

The loader namespace cannot reach even the otherwise-live owned archive peer. A successful loopback observer control precedes loading and zero subsequent MCP requests are required; script sentinels must remain absent. These assertions qualify this selected inert package, not arbitrary Plugin safety.

## Assertions

Six direct controls exercise valid portable loading, malformed root JSON, missing/unsupported schema, invalid name and one invalid MCP sibling beside a valid Skill/server. A declared portable manifest takes precedence over the conflicting legacy manifest. OpenHands 1.58.2 treats a root object without `$schema` as a different format and loads the exact legacy decoy, its Skill and no portable MCP definitions; this is a consumer characterization, not Catalog admission. Malformed JSON, an unsupported declared schema and invalid names still require the intended native diagnostic. The governed Catalog handoff continues to require the portable profile and exact retained bytes.

The SDK deliberately skips the bad MCP sibling and preserves valid siblings; this does not weaken Catalog whole-package admission.

The governed scenario prepares two bounded immutable ZIPs from invocation-owned sample YAML/assets through existing `test/demo` machinery. Normal contributor/reviewer/publisher APIs inspect and publish the first package. Real CLI Search and exact record bind all retained identities to one active ARD snapshot's canonical identifier, URL, media type, digest and size.

This is the existing generic Plugin archive pointer, not a standardized `application/agent-plugins+json` mapping or native OpenHands ARD support.

The selected owned URL is downloaded anonymously with private TLS verification, without ambient proxies or redirects. Size/SHA-256 are checked before extraction. The consumer bounds actual expanded bytes, paths, entry counts and file types; exact retained files, directories, executable flags and component documents must match.

All five deferred assessment fields must remain explicitly `not_assessed`. Unchanged OpenHands then loads the directory. Expected manifest fields, Skill body/resource paths and normalized uninvoked MCP URL/transport are checked independently.

Foreign client extensions stay inert. The prepared sample packager uses non-executable files; the test preserves those flags instead of manufacturing executable entries. Pure extractor tests separately exercise executable-mode preservation.

A later unpublished draft points at the second archive. Published revisions, validation evidence, selected bytes and public snapshot/ETag must remain unchanged. Same-length corruption and truncation fail before extraction/loading.

Missing/invalid/wrong-tenant required reads stop the consumer before downloading or loading; they do not revoke independently public archives. Anonymous archive GET observations are kept separate from forbidden MCP/component traffic.

## Bounds, reports and maintenance

Installation has a 900-second deadline, each native process 90 seconds and 64 KiB combined output, and the complete scenario 600 seconds plus bounded cleanup. Prepared ZIPs are limited to 64 KiB; extraction additionally enforces count, depth, per-file, total and expansion-ratio ceilings. Only named invocation containers/private paths are removed, including on interruption.

Images and ignored evidence/tool stamps remain reusable; `make clean` preserves this retained evidence. A stale or missing installation stamp requires the explicit install target.

`output/interoperability/plugins/installation.json` binds source and lock inputs, exact image, Go tool, image size and installed distribution inventory. `<run-id>/report.json` retains exact native results, source/tool/binary identities, selected immutable IDs, refusals and independent cleanup. Missing phases, stale/mismatched results, cancellation, source drift and cleanup failures cannot pass.

Reports use synthetic data and omit authentication values. This is functional evidence, not a new statement-coverage denominator.

Pure `test_unit_plugins.py` covers archive/materializer, native-output, source-integrity and report rejection without subprocesses or SDK imports. `test_functional_plugins.py` covers actual Make and child-process interruption/early-report cleanup without Docker or installing the SDK. Existing ordinary Python/unit/local-functional discovery runs them; only the explicit target runs the native client.

`container-check` includes the Containerfile.

## Dependency review

`provenance.json` binds the entire SDK package source and root MIT license. Installation verifies extracted checksums and every installed SDK code/data file against that pin. `requirements.in` mirrors declared dependencies/build requirements; no runtime modules are stubbed or removed to make import cheap.

`constraints.txt` retains the selected upstream security floors and LiteLLM 1.103.9, within the SDK's supported `>=1.93.0,<1.106` range. This replaces the upstream workspace's older 1.93.0 selection. Pass this constraint file when regenerating the hash lock with the repository-pinned uv command recorded in `requirements.txt`. Review the closure, reinstall and rerun native controls. No Python package becomes a Catalog runtime dependency.

Most installed dependencies declare MIT, Apache, BSD, PSF, ISC or MPL terms; source distributions retain their notices. The SDK also requires `lmnr`, which brings `lmnr-claude-code-proxy` 0.1.24 with `LicenseRef-Proprietary`. Its bundled notice refers to [Laminar terms](https://laminar.sh/policies/terms), which describe internal-use rights and redistribution restrictions.

This local qualification neither invokes that proxy nor contacts Laminar. Do not publish or ship this test image as an all-open-source dependency bundle. This is an explicit local-tool limitation, not an Apache redistribution approval or a change to the application license gate.

`agent-client-protocol` and `socksio` have incomplete metadata but carry Apache-2.0 and MIT license files respectively; SDK license is pinned directly.

Record actual native outcomes, advisory scan scope and remaining limits under ignored `output/`.
