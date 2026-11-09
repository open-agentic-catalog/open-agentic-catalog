# Offline MCP Scanner qualification

This harness owns the optional [saved-capture example](../../examples/mcp-scanner/README.md) and its offline scanner runner. The separate [signed-webhook receiver](../../examples/mcp-scanner/receiver/README.md) uses that runner. Neither is a Catalog runtime dependency.

## Setup and checks

Run these targets separately:

```sh
make mcp-scanner-tools-install
make mcp-scanner-check
```

Setup, checks and scans require a non-root Linux AMD64 host, Python 3.11 or later, Make and the local Unix Docker socket. Downloads occur only during setup. Tests require the installed image, a current source-bound receipt and the [shared development tools](../../documentation/src/content/docs/contributing/build-and-release.md#shared-development-tools).

The example guide explains the `mcp-scanner-scan` inputs, limits, report fields and exit statuses. All three targets bypass workflow env files. Scan inputs pass literally through target-scoped environment variables. The runner gives Docker a separate environment without provider credentials, proxies or host Docker configuration.

`mcp-scanner-check` runs three distinct selections:

1. Pure capture, schema and report validation tests.
2. Owned subprocess doubles for process limits, failures, cleanup and file handling.
3. The pinned scanner's actual YARA controls inside network-disabled containers.

The third selection verifies real benign, description-only and parameter-only outcomes. It also tests pagination, empty work, invalid rules and timeout cleanup. A test-only injected YARA exception verifies the upstream static analyzer's failure and partial-result evidence. No upstream source or rule is patched.

Ordinary `make test-tooling-unit PYTHON_UNIT_DIRS=mcp-scanner` runs only isolated tests. It uses the shared development environment's existing JSON Schema validator. These units do not import MCP Scanner, start subprocesses, access Docker or require a database.

The tagged Go functional wrapper runs the subprocess tests. Native scanner tests run only when `CATALOG_TEST_MCP_SCANNER=1`. An unselected native suite is skipped, not qualified. The explicit check target selects it and fails when installation is missing or stale.

## Signed-webhook receiver checks

Run these separate targets after `make sdk-tools-install SDK_LANGUAGE=go` and shared development-tool setup:

```sh
make mcp-scanner-receiver-check
make test-mcp-scanner-receiver
```

The first target checks the independent Go module, copied signature verifier, SDK, licenses, isolated units and local MCP/TLS behavior. It checks real MCP SDK JSON and finite SSE responses. It rejects redirects, wrong credentials, invalid trust, malformed pages and excessive work. It permits metadata reads and session setup/cleanup only.

The second target starts one owned PostgreSQL container. It creates a dedicated receiver database/account and builds the receiver binary. Tests use controlled HTTPS Catalog responses, a real MCP SDK peer and an explicit Python scanner executable double. They verify durable acceptance, bounded storage, reconciliation, report bindings, failure handling and restart recovery. Cleanup removes only the owned fixture.

These tests do not execute MCP Scanner or qualify YARA results. They do not run the full Catalog API/worker delivery workflow. The receiver guide documents private inputs, permissions and manual commands. Build the binary separately with `make mcp-scanner-receiver-build`.

The runner's `identity` command checks the existing installation receipt and prints only scanner/image and rules/configuration identities. It never installs tools or uploads package inventories.

## Complete Compose campaign

Run `make test-mcp-scanner-compose` after native scanner setup and checks. The [manual guide](../../documentation/src/content/docs/guides/mcp-scanner.mdx) provides prerequisites, automatic execution and explicit prepare/start/test/stop commands.

This target builds the current backend and one inert MCP fixture. It owns one Compose project with an internal network. Application containers publish no ports. The receiver runs on the host and binds to that project's private bridge. This permits native network-disabled scanner execution without mounting a Docker socket into a container.

An owned host process binds three loopback TCP listeners for the API, receiver database and MCP peer. Targets come from inspected containers in the exact owned subnet. The relay validates project, service, network and address identity; discovered metadata never selects its targets. It preserves protocol bytes, permits 16 active connections per listener and cancels open streams during cleanup. This does not require Docker to publish ports on an internal bridge. Keep the internal network and loopback checks enabled; do not redirect the campaign to retained services.

The campaign uses real authoring, independent review, publication, worker delivery, current scoped reads and a separate receiver database/account. It requires native benign and finding reports, durable acceptance before restart, duplicate/stale delivery controls, unchanged sealed evidence and zero tool invocations. It checks incorrect credentials/trust, revoked access, a failed scanner process and a release published while the endpoint is disabled. The process-failure control exits without fabricating a YARA report; native analyzer-error controls remain in `mcp-scanner-check`.

Persistent reports and selected private scan evidence stay under ignored `output/review/mcp-scanner/`. Tests do not acquire a missing scanner or substitute an executable double for native qualification.

Cleanup stops the receiver before it copies final reports. It stops the owned relay before removing the project network. Thread and process creation register ownership before delivering a deferred stop signal. The single-threaded coordinator restores the child's original signal mask before execution. A failed process record stops and reaps the new child.

Cleanup removes only the owned project, processes, unique image tag and private inputs. It verifies that retained containers remain present. An unavailable container inventory fails cleanup and preserves private inputs. A successful cleanup retry clears resolved cleanup-failure fields. SIGTERM or Ctrl+C marks the run as interrupted and starts scoped cleanup.

The isolated campaign units run through the scanner's existing unit selection. Ordinary functional tests render Compose and execute the first-party HTTPS/MCP fixture and loopback relay. They check byte preservation, half-close, stream cancellation, process ownership and actual SIGTERM during thread/process registration. They require cached Go modules and Docker Compose, but do not install or execute MCP Scanner. The full campaign remains a separate explicit selection. Startup failures record `failureStage`; `failedCase` identifies only an assertion reached during qualification.

## Pins and lock maintenance

[pins.json](pins.json) fixes scanner 4.8.7, source revision, bundled-rule digest and the Python image digest. [requirements.in](requirements.in) names direct requirements. [constraints.txt](constraints.txt) retains previously approved exact security exceptions. [requirements.txt](requirements.txt) pins the complete wheel closure and hashes.

The maintainer approves the age exception for MCP Scanner 4.8.7. Apply `scannerAgeExceptionApproved` through the reviewed installer inputs before installation. The dependency findings still require supported fixes. After November 3, 2026, at 17:30:42 UTC, ordinary release-age admission permits this exact wheel.

Use the repository-pinned uv 0.13.4 to regenerate the lock. The command in its header records the fixed cutoff and package-specific exceptions. Start from the repository root. Do not inherit registry credentials or user uv configuration. Review every changed version, wheel hash, release timestamp and dependency requirement before reinstalling.

The base image and pip 26.2.2 reuse earlier approved exact security exceptions. The lock also reuses certifi 2026.10.19 and filelock 4.2.1. The constraint file preserves earlier approvals for Pillow and setuptools if a future resolution needs them. Constraints do not add absent packages.

The rules hash covers a sorted array of filenames and SHA-256 file hashes. Its JSON encoding uses sorted keys, ASCII escapes and compact separators. Setup checks the wheel's actual rules against this independent source-derived pin. Runtime binds each report to the installed rules and image.

The full package includes unused provider and proxy dependencies. The registry review identifies two LiteLLM 1.93.2 proxy advisories: [privilege escalation](https://github.com/advisories/GHSA-7hp6-4w63-5g45) and [credential-file disclosure](https://github.com/advisories/GHSA-g5ff-637f-6q2m). Their vulnerable HTTP proxy endpoints are not started by this example. The scanner runs with no network, provider credentials or developer mounts. This is scoped offline use, not a clean dependency-advisory result. The upstream exact LiteLLM requirement is preserved without an incompatible override.

Keep [upstream notices](../../examples/mcp-scanner/NOTICE.md) with the example and installed distributions. The complete image has additional licenses; do not ship it as an Apache-only dependency bundle.

## Local files and cleanup

The ignored `.tools/installed.json` receipt binds the image, package inventory, rules and implementation fingerprint. Setup and scans use private temporary directories. Each scan checks exact container ownership before removal. An engine failure leaves cleanup unconfirmed and fails the scan.

The host validates capture structure and byte limits. The isolated worker validates JSON Schemas before importing the scanner. Report validation rejects mismatched identities, missing completion evidence and unexpected analyzers.

`make clean` includes this harness's `.tools/` directory. It does not remove Docker images or saved reports. Do not run cleanup while a scan or setup is active. Use the existing separate output-cleanup policy for reports under `output/review/`.

No target publishes images, uploads reports or changes retained Compose services. The current runtime platform is Linux AMD64; ARM64, Windows and macOS execution are not qualified.
