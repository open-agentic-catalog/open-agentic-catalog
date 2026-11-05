# Local governed A2A handoff

Run these targets separately from the repository root:

```sh
make a2a-tools-install
make test-interoperability-a2a
```

The installer downloads the locked, execution-only Python dependencies and unchanged official Hello World executor. The test uses the installed image without downloads and fails for missing or stale tools. Both targets ignore deployment environment files and reject mixed Make goals.

Neither is a default, unit-test or Catalog runtime dependency. Prerequisites are a local Linux Docker engine, a non-root developer, Python 3.10+, Make, OpenSSL, a Go launcher on `PATH`, the exact repository Go toolchain installed or cached, and cached Go modules; `gh` is required for acquisition. Before building, qualification resolves and version-checks the compiler matching `go.mod` from the launcher or its module cache, with automatic switching, inherited Go flags and downloads disabled.

An older launcher requires the exact compiler already cached; missing tools fail without acquisition. The test builds current static `catalogd` and `catalogctl` commands offline into a private directory and runs an owned in-memory Catalog. It needs no prebuilt binary, database, model, API key or retained Compose environment.

This is functional testing: real binaries, client/server processes and HTTPS connections execute.

## What the control proves

The image contains official `a2a-sdk` 1.2.2 and the unchanged deterministic sample executor. The small deployment launcher uses upstream Starlette routes, the SDK's request handler and in-memory task store. Its card truthfully advertises one anonymous `JSONRPC` 1.0 HTTPS interface with `text/plain` modes.

Streaming, polling, extended cards, OAuth, gRPC, push notifications and cancellation are not qualified. The sample cancel implementation raises `NotImplementedError`.

The native client fetches the public card once, checks the exact interface before constructing the SDK client, and supplies that protobuf Card object to `create_client`. It explicitly disables streaming/polling, sets the binding and sends `A2A-Version: 1.0`. It refuses alternate versions, bindings, endpoints and ambiguous cards before invoking the SDK.

This consumer selection is narrower than Catalog admission; it is not a new Catalog validation policy.

Five reported outcomes require:

| Outcome | Required evidence |
| --- | --- |
| `card` | Actual HTTPS card, strict protobuf parsing, truthful selected interface and stable semantic JSON SHA-256. |
| `send` | One native non-streaming task, nonempty task/artifact IDs, requested context identity, completed state and exact request-derived text, including punctuation and Unicode. |
| `readback` | The send response contains the original user and working-status messages. One native `GetTask` requests only one history message and must return the newest one, with every other task field unchanged. This proves actual truncation rather than requesting the full existing history. |
| `selection-refusals` | Four invalid selections rejected before connection. |
| `traffic` | Server observation of exactly one card GET, one `SendMessage` and one `GetTask`; correct JSON-RPC/version, request/readback identities and history bound, with no Authorization, cookies or query strings. |

## Governed handoff and failure checks

The same target creates an Agent Resource, VersionRevision, validation evidence, listing and review submission through the normal Catalog API. Separate contributor, reviewer and publisher identities approve, release, construct and activate one public snapshot. Real `catalogctl remote search` selects the release; `remote record` supplies the exact retained primary `application/a2a-agent-card+json` document, bound to profile `a2a-agent-card-1.0.1-3303592`, canonical digest/size, evidence and resource/version/revision/release identities.

The public ARD projection must agree. A later valid but unpublished version and listing draft use a different endpoint; the selected immutable evidence and active snapshot remain unchanged.

No A2A request may occur during these metadata operations. Missing and invalid Catalog credentials receive API 401; the CLI refuses a missing token locally and receives the structured HTTP failure for an invalid token. A valid reader in another tenant receives 404.

All three cases produce no handoff file, client launch or runtime traffic. The anonymous A2A endpoint receives no Catalog bearer or cookies.

The selected retained card is mounted into the official client. No card GET is allowed on this path. Exactly one `SendMessage` and one `GetTask` must succeed with a fresh context, message and task identity, exact request-derived content and bounded history.

Unsupported versions, bindings, endpoints and ambiguous interfaces are rejected before SDK connection on both successful paths. These controls are test-consumer policy, not new Catalog validation rules.

Two actual negative clients use the same retained card: the first omits the private CA and must report native certificate verification failure; the second runs after the A2A listener stops and must report connection refusal at the owned IP/port. Neither arbitrary process failure nor timeout satisfies these checks. The Catalog remains live/ready and its selected record, active snapshot and public document/ETag must be unchanged after each failure.

Both services must shut down normally before owned cleanup.

The complete report preserves five direct outcomes, four governed metadata groups, the successful governed exchange, three Catalog denials and two native transport failures. Scenario outcomes are not application statement coverage or general A2A conformance. Invocation belongs only to this explicitly selected test consumer; Catalog remains a metadata service.

## Isolation, reports and cleanup

The Catalog owns a Docker `none` network; the A2A server and clients share only that loopback namespace. Stopping the A2A listener leaves Catalog and the namespace alive. No ports are published and external egress is unavailable.

Containers run as the developer's non-root UID, with read-only roots, dropped capabilities, no new privileges and bounded memory, CPUs, PIDs, logs and tmpfs. Proxy environment variables are empty. Only copied fixture files and the required private files are mounted read-only; the client receives only the CA certificate and, after successful selection, the retained card; it receives no server/CA key or Catalog credentials.

A fresh CA and IP-valid server certificate use normal strict certificate verification.

The supervisor bounds readiness and subprocess time/output, with a 600-second scenario alarm plus bounded diagnostics and cleanup. Container creation defers signal delivery until its bounded operation returns, so 600 seconds is not a hard end-to-end wall-clock limit. Reports under ignored `output/interoperability/a2a/<run-id>/` retain tool/source/image and Catalog binary identities, native results/exits, synthetic wire observations, case completeness and cleanup.

Failed and partial reports cannot pass. Signal handling removes only invocation-owned containers and private files; an unconfirmed container creation remains a failed cleanup qualification. The installed tool image and reports remain.

After an uncatchable host failure, use only the exact names listed in that run's report `containers` array; never prune retained environments.

## Pins, dependencies and maintenance

[provenance.json](provenance.json) binds SDK source/release `2f9e44af243df5c7c7a3da361c8d095324bcd5e3`, the exact wheel SHA-256, the complete installed SDK source digest, and sample executor commit `6603ba3f2c31a7ef33e70b9d8b5b5f8be42ac9a3` plus file hash. The sample declares SDK 1.1.0; this target qualifies its unchanged executor with 1.2.2. The upstream launcher is not used: its hardcoded HTTP interface and streaming/extended-card advertisements do not describe this deployment.

Catalog's pinned `a2a-agent-card-1.0.1-3303592` profile is unchanged.

[requirements.in](requirements.in) owns selected dependencies; [requirements.txt](requirements.txt) locks the complete Python 3.13 closure and archive hashes. Installation accepts wheels only, verifies installed SDK/executor source digests, checks dependency consistency and retains the complete package/license inventory in `output/interoperability/a2a/dependencies.json`. The Python base image is digest-pinned.

SDK and sample Apache-2.0 notices are retained here and in the image. Other installed packages declare Apache, MIT, BSD, PSF and MPL-2.0 licenses; certifi's unchanged MPL-2.0 certificate bundle stays inside this execution-only image. This adds no application dependency or distributed product artifact.

The image's pip is separately pinned to avoid retaining the old base-image installer.

For an explicit dependency update, use the repository's reviewed uv 0.13.4. Preserve the fixed cutoff and approved package exceptions.

```sh
uv pip compile test/interoperability/a2a/requirements.in \
  --exclude-newer 2026-10-10T13:32:13Z \
  --generate-hashes --python-version 3.13 \
  --output-file test/interoperability/a2a/requirements.txt \
  --exclude-newer-package pip=2026-10-20T16:36:29.008409Z \
  --exclude-newer-package certifi=2026-10-20T02:24:16.604828Z \
  --exclude-newer-package pyasn1=2026-10-27T06:32:50Z \
  --upgrade-package pyasn1 --refresh-package pyasn1
```

Review exact upstream source changes, wheel/source agreement, notices and the full installed closure's advisories before qualification. The installer does not claim a live vulnerability scan or operating-system advisory clearance. Retain dated evidence under ignored `output/`.

Never replace the deterministic executor or patch peer protocol behavior to obtain a pass.

Pure selection/result/traffic, immutable evidence, CLI release selection, failure classification and completeness tests use only the standard library and run through `make test-tooling-unit`. Ordinary functional checks cover Make dispatch, assertion-enabled execution, supervisor failure/deadline/interruption cleanup and the shared bounded subprocess helpers without importing the SDK or launching A2A. Shared Catalog authority controls retain their MCP regressions. Actual tool qualification requires the explicit target above, matching final runs and an active-run interruption check.
