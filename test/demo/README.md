# Local sample and runtime qualification

Run `make demo-check` for pure local YAML/assets validation and inventory/addition preview, then `make demo-archives-prepare` for private TLS, immutable archive objects and the atomic exact allowlist. `make demo-archives` serves only the verified prepared bytes. [Sample authoring](../../samples/README.md) owns manifest fields, safe asset paths, limits and package layout.

Startup failures print `Demo sample tool failed (stage: <stage>).` to stderr. Stages distinguish prepared-file reads, TLS preparation and listener startup. Certificate stages distinguish `tls.not-yet-valid`, `tls.expired` and `tls.hostname`. The browser fixture retains recognized stages, exit status and bounded diagnostic flags. It omits raw child output, private paths and credentials. Unknown failures retain the `unclassified` fallback.

`DEMO_DATA_FILE` selects preview/preparation/API inputs; the curated profile is the developer default. The curated default selects three local bundles of real components; the explicit original expanded profile preserves the three synthetic Plugin packages. Interrupt only this owned foreground process to stop it.

The [configuration reference](../../documentation/src/content/docs/reference/configuration.mdx#development-demo-archives) owns listener/trust settings and the strict development-only fetch boundary. This tool never seeds a Catalog or executes package content; the receipt-bound seeder supports declared profiles and explicit append-only extension. For retained development use the [launch/load recipes](../../documentation/src/content/docs/guides/local-development.mdx#compose-launch-and-load) and explicit `LOCAL_DEMO_ARCHIVES=on`; stop/down preserve volumes, receipts and prepared TLS.

Run `go test ./test/demo ./test/demofixture ./internal/platform/demodata` for isolated preparation, metadata, deterministic archive, validator and in-memory handler assertions. Run `go test -tags=functional ./internal/platform/runtime -run '^TestDemoArchive(RealTLSAndFailures|RuntimeInspection)$' -count=1` for actual TLS, exact bytes and adversarial routing controls. These functional cases use owned loopback servers and require no external network, database or compiled Catalog binary.

## Explicit Compose qualification

Use a local Docker engine and a fresh invocation-owned project. Run from the repository root as a non-root user. Confirm that ports 18088, 55438 and 18448 and subnet `172.30.254.0/24` are unused. If you change the subnet, change its archive IP and origin together. Select the `expanded` manifest profile: these tests require the original three Plugin archive paths, which curated-only preparation does not supply.

The block below clears inherited runtime settings with `env -i`. It preserves only PATH and HOME for installed tools and the local engine's configuration. `CATALOG_ENV_FILE=/dev/null` also prevents Make from loading workflow files. Raw Compose's `--env-file /dev/null` skips the default `.env` file; it does not clear inherited shell settings by itself.

The temporary parent owns its archive directory and missing receipt path. It cannot read preparation hints from the retained developer receipt. The unique project name scopes the volume and cleanup. Review the command selected for the test before running:

```sh
env -i PATH="$PATH" HOME="$HOME" sh <<'SH'
set -eu
mkdir -p output/demo/qualification
demo_work="$(mktemp -d "$PWD/output/demo/qualification/archive-docs.XXXXXX")"
export COMPOSE_PROJECT_NAME="oac-$(basename "$demo_work" | tr '[:upper:].' '[:lower:]-')"
export CATALOG_ENV_FILE=/dev/null
export DEMO_DATA_FILE="$PWD/samples/catalog.yaml" DEMO_SAMPLE_SET=expanded
export DEMO_STATE_FILE="$demo_work/receipt.json" DEMO_ARCHIVE_DIR="$demo_work/archives"
export DEMO_ARCHIVE_UID="$(id -u)" DEMO_ARCHIVE_GID="$(id -g)"
export DEMO_ARCHIVE_IP=172.30.254.41 DEMO_ARCHIVE_ORIGIN=https://172.30.254.41:8443
export CATALOG_COMPOSE_SUBNET=172.30.254.0/24 CATALOG_COMPOSE_HTTP_PORT=18088
export CATALOG_COMPOSE_POSTGRES_PORT=55438 DEMO_ARCHIVE_PORT=18448
export IMAGE="open-agentic-catalog:$COMPOSE_PROJECT_NAME"
demo_compose() {
  docker compose --env-file /dev/null -f compose.yaml -f compose.demo.yaml -p "$COMPOSE_PROJECT_NAME" "$@"
}
demo_cleanup() {
  demo_status=$?
  trap - EXIT
  if ! demo_compose down --volumes; then exit 1; fi
  exit "$demo_status"
}
trap demo_cleanup EXIT
make demo-archives-prepare CATALOG_ENV_FILE=/dev/null
make container-build CATALOG_ENV_FILE=/dev/null IMAGE="$IMAGE"
demo_compose up -d --wait --wait-timeout 180 postgres api worker demo-archives
DEMO_ARCHIVE_TEST_OWNED=1 DEMO_ARCHIVE_TEST_API=http://127.0.0.1:18088 \
  DEMO_ARCHIVE_TEST_ORIGIN="$DEMO_ARCHIVE_ORIGIN" \
  go test -tags=functional ./test/demo -run '^TestOwnedComposeDemoArchives$' -count=1 -v
SH
```

Compose applies migrations and provisions identities before API/worker startup. `--wait` requires their readiness checks to succeed before the test. The test creates a temporary namespace and three Plugin resources through the API. It checks PostgreSQL inspection digests, rejects checksum drift and verifies no partial Version. It skips unless explicitly activated; ordinary functional/race selections never connect through this test.

The raw archives have exact ETags. Their `no-store` policy prevents stale development-cache bytes after a deliberate fixture rebuild. This qualification executes no package content and contacts no hosted resource endpoint. Image building can acquire its declared build inputs.

The exit trap removes only this invocation's project, network and PostgreSQL volume. Verify that cleanup succeeds. Its private ignored archive directory and built local image remain for deliberate inspection or reuse. Retained developer volumes and receipts remain separate.

## Expanded seed qualification

Run the complete block above on a new invocation-owned project. Replace its `TestOwnedComposeDemoArchives` command with this command; do not run both against one populated instance:

```sh
DEMO_ARCHIVE_TEST_OWNED=1 DEMO_ARCHIVE_TEST_API=http://127.0.0.1:18088 \
  DEMO_ARCHIVE_TEST_ORIGIN="$DEMO_ARCHIVE_ORIGIN" \
  go test -tags=functional ./cmd/catalogctl -run '^TestExpandedDemoOwnedCompose$' -count=1 -v
```

It creates the original five-record control, then extends it to fourteen Resources and twelve published releases. It verifies original evidence and a completed rerun against PostgreSQL. Reader assertions check exact Search/Browse/detail identities, continuation, kind/tag filters, namespace allocations and Plugin digests/components. Public projections contain thirteen ARD entries, including the self-Registry, and twelve AI Catalog entries. Default test selections skip this activation.

The same exit trap owns cleanup. Memory-backed functional cases separately cover fresh creation, lost responses, unavailable replay, drift/collision refusals and unrelated eligible releases.

## Persistent local real peers

`compose.demo-runtimes.yaml` is the fixed opt-in overlay selected by the existing local launcher with `LOCAL_DEMO_RUNTIMES=on`. [Samples](../../samples/README.md#start-and-test-the-two-local-peers) own the complete installation/preparation/start/client/stop instructions and qualification distinctions. `demo-runtimes-prepare` verifies existing locked tools, builds the shared MCP wrapper and reuses private local TLS plus a separate synthetic client key.

It never installs dependencies or seeds a Catalog.

`agent_runtime.py` is small persistent SDK deployment glue around the unchanged installed Hello World executor, with bounded requests/tasks and safe output. It does not run the finite `a2a_fixture.py` observer indefinitely. MCP reuses the existing wrapper's `serve-demo` mode, with bounded session admission and namespace-loopback upstream forwarding.

Existing network-none controls retain their original listener mode and assertions. `runtime_clients.py`/`agent_client.py` run real SDK/Inspector consumers against only explicitly selected host-loopback endpoints and clean their own ephemeral containers/files. Peer services remain until the selected Compose stop/down.

Pure Python `test_unit_runtimes.py` admission/path/port/selection units run through `make test-tooling-unit`; Go key/package units run through `make test-unit`; actual process/Make tests are functional. Keep direct client, startup/failure/stop/restart/custom-port/privacy evidence in local reports. Default unit/verify checks never execute these peer services or hosted endpoints.

`make test-demo-runtimes` is the separate owned functional lifecycle gate: it prepares a unique project/private directory and unused host-loopback ports, starts only the three fixed peer services, checks both actual clients plus adversarial/TLS controls, proves stop refusal and unchanged restart identity, checks private-value omission in logs, and records success/cleanup under `output/demo/qualification/`. It cleans its own containers/network/temporary inputs in a finally boundary and leaves retained Catalog projects untouched.

Install the existing A2A/MCP tools explicitly first; this target runs alone, bypasses workflow env files and does not acquire tools or call hosted endpoints.

## Configured seed and old-policy qualification

Use a separate fresh invocation-owned project. Confirm that ports 18089, 55439 and 18449 and subnet `172.30.253.0/24` are unused. The block first prepares the checked-in `expanded` manifest. It then seeds the original receipt through the preserved embedded expanded command. The seed's empty `DEMO_DATA_FILE=` is intentional; it selects that original command rather than the configured manifest loader.

Next, reprepare the same directory from the checked-in `curated` profile. Preparation retains the original archive paths/bytes and TLS identity. The checksums below also verify that repreparation preserves the original objects, TLS files and receipt. Recreate the archive/API/worker containers so their file bind mounts use the replaced allowlist; keep PostgreSQL and its completed migration/provisioning state.

```sh
env -i PATH="$PATH" HOME="$HOME" sh <<'SH'
set -eu
mkdir -p output/demo/qualification
demo_work="$(mktemp -d "$PWD/output/demo/qualification/configured-docs.XXXXXX")"
export COMPOSE_PROJECT_NAME="oac-$(basename "$demo_work" | tr '[:upper:].' '[:lower:]-')"
export CATALOG_ENV_FILE=/dev/null
export DEMO_DATA_FILE="$PWD/samples/catalog.yaml" DEMO_SAMPLE_SET=expanded
export DEMO_STATE_FILE="$demo_work/receipt.json" DEMO_ARCHIVE_DIR="$demo_work/archives"
export DEMO_ARCHIVE_UID="$(id -u)" DEMO_ARCHIVE_GID="$(id -g)"
export DEMO_ARCHIVE_IP=172.30.253.41 DEMO_ARCHIVE_ORIGIN=https://172.30.253.41:8443
export CATALOG_COMPOSE_SUBNET=172.30.253.0/24 CATALOG_COMPOSE_HTTP_PORT=18089
export CATALOG_COMPOSE_POSTGRES_PORT=55439 DEMO_ARCHIVE_PORT=18449
export DEMO_BASE_URL=http://127.0.0.1:18089
export DEMO_ARCHIVE_TRUST_FILE="$DEMO_ARCHIVE_DIR/cert.pem"
export IMAGE="open-agentic-catalog:$COMPOSE_PROJECT_NAME"
demo_compose() {
  docker compose --env-file /dev/null -f compose.yaml -f compose.demo.yaml -p "$COMPOSE_PROJECT_NAME" "$@"
}
demo_cleanup() {
  demo_status=$?
  trap - EXIT
  if ! demo_compose down --volumes; then exit 1; fi
  exit "$demo_status"
}
trap demo_cleanup EXIT
make demo-archives-prepare CATALOG_ENV_FILE=/dev/null
make container-build CATALOG_ENV_FILE=/dev/null IMAGE="$IMAGE"
demo_compose up -d --wait --wait-timeout 180 postgres api worker demo-archives
make demo-seed CATALOG_ENV_FILE=/dev/null DEMO_DATA_FILE= DEMO_SAMPLE_SET=expanded \
  DEMO_BASE_URL="$DEMO_BASE_URL" DEMO_ARCHIVE_ORIGIN="$DEMO_ARCHIVE_ORIGIN" \
  DEMO_STATE_FILE="$DEMO_STATE_FILE"
sha256sum "$DEMO_STATE_FILE" "$DEMO_ARCHIVE_DIR/cert.pem" "$DEMO_ARCHIVE_DIR/key.pem" \
  "$DEMO_ARCHIVE_DIR"/objects/*.zip > "$demo_work/original.sha256"
demo_compose stop api worker demo-archives
make demo-archives-prepare CATALOG_ENV_FILE=/dev/null \
  DEMO_DATA_FILE="$DEMO_DATA_FILE" DEMO_SAMPLE_SET=curated \
  DEMO_ARCHIVE_DIR="$DEMO_ARCHIVE_DIR" DEMO_ARCHIVE_IP="$DEMO_ARCHIVE_IP" \
  DEMO_ARCHIVE_ORIGIN="$DEMO_ARCHIVE_ORIGIN" DEMO_STATE_FILE="$DEMO_STATE_FILE"
sha256sum -c "$demo_work/original.sha256"
demo_compose up -d --no-deps --force-recreate --wait --wait-timeout 180 api worker demo-archives
DEMO_ARCHIVE_TEST_OWNED=1 DEMO_ARCHIVE_TEST_API="$DEMO_BASE_URL" \
  DEMO_ARCHIVE_TEST_ORIGIN="$DEMO_ARCHIVE_ORIGIN" \
  DEMO_ARCHIVE_TEST_STATE="$DEMO_STATE_FILE" DEMO_ARCHIVE_TEST_TRUST="$DEMO_ARCHIVE_TRUST_FILE" \
  go test -tags=functional ./cmd/catalogctl -run '^TestConfiguredDemoOwnedCompose$' -count=1 -v
demo_compose stop demo-archives
make demo-seed CATALOG_ENV_FILE=/dev/null DEMO_DATA_FILE="$DEMO_DATA_FILE" DEMO_SAMPLE_SET=curated \
  DEMO_BASE_URL="$DEMO_BASE_URL" DEMO_ARCHIVE_ORIGIN="$DEMO_ARCHIVE_ORIGIN" \
  DEMO_STATE_FILE="$DEMO_STATE_FILE" DEMO_ARCHIVE_TRUST_FILE="$DEMO_ARCHIVE_TRUST_FILE"
SH
```

All selected paths are absolute because Go package tests run from their package directory. The test accepts only an explicit original expanded receipt and literal loopback API. It verifies original immutable IDs/history, current-policy Plugin revalidation, 26 accumulated Resources and 24 published releases. It also checks paginated Reader discovery, three package-only Skill exclusions, exact public documents and an unchanged-input rerun without archive trust. It skips ordinary selections and contacts no hosted resource endpoint.

The final `demo-seed` runs with the archive stopped and must report `already_matched`, with no authoring commands. Required evidence-read and startup membership-provisioning audits remain distinct from authoring mutations. The exit trap removes only this invocation's containers, network and volume. Require successful cleanup; retained developer data remains separate.

`TestConfiguredDemo` memory-backed functional cases independently exercise arbitrary file additions/new namespace/publisher/domain, frozen assets, comment/order identity, exact interruption/recovery, original-receipt adoption, malformed inputs/collisions/locks, complete-journal cleanup and archive preflight/blocked-validation boundaries. The [configured recovery tests](../../cmd/catalogctl/seed_demo_recovery_functional_test.go) additionally exercise uncommitted exact-key retry, ambiguous or differing Version refusal, unmatched journals beside completed receipts, and readback-only recovery after committed activation. A real successful activation response with the wrong snapshot ID must be rejected before a foreign-ID lookup or completed receipt; rerunning recovers the actual committed snapshot without another mutation.

For a focused recovery check, run:

```sh
go test -tags=functional -race ./cmd/catalogctl \
  -run '^TestConfiguredDemoInterruptedGovernance$' -shuffle=on -count=1
```

This checks committed response loss during Resource creation, Version creation, publication, validation and snapshot activation. Each case verifies the original receipt, refuses changed recovery inputs and completes the exact recovery. These are functional tests with owned loopback API/TLS servers and temporary files. They require no database, Compose project or separately built application. Failure diagnostics include only the route, HTTP status, service code and public problem detail. Preserve the shuffle seed and source revision when investigating; do not record credentials, request bodies, cursor contents or internal causes.

[Response-binding units](../../cmd/catalogctl/seed_demo_safety_unit_test.go) separately check addressed publisher, Listing and snapshot IDs plus native wire digests, journal limits and state-file safety without listeners. The original `TestExpandedDemo` recovery/expired-response/unrelated-release assertions remain unchanged. Make qualification tests prove scoped default/custom variables, stage order and failure stopping before the next stage; actual `demo-up` is separately executed against an owned PostgreSQL/UI project.

The separately selected `make test-demo-catalog` qualifies the complete browser and Catalog-selected real-client workflow. Original minimal/expanded fixture consumers remain separately verified.

Preparation may read the selected `DEMO_STATE_FILE` as bounded unverified hints. Known original receipt keys cause exact embedded original Plugin archives to be included when transitioning a TLS-only directory for the first time. This changes fixture preparation only; the API loader independently verifies/adopts the receipt, and custom omitted entries receive no automatic legacy compatibility.

Existing prepared paths/bytes remain immutable.

## Complete curated developer workflow

Install prerequisites explicitly with `make ui-install`, `make a2a-tools-install` and `make mcp-tools-install`. Then run `make test-demo-catalog` alone. This target never installs tools, loads a workflow env file, or uses the retained developer project.

It copies `samples/`, selects unused loopback ports and a private subnet, builds current API/UI images and runs the actual `demo-check`/`demo-up` Make workflow in a unique owned PostgreSQL project. The selected project, ports, images, archive inputs, manifest, profile and receipt reach Make through the environment. Extension uses an explicit `DEMO_EXTEND=true` override. This exercises recursive input propagation in the real launcher; inert Make tests separately qualify workflow-file precedence, quoted inputs and refusal before child work.

The browser checks twelve published results, three per kind, real project descriptions, four-kind preview/detail handoff and both namespaces. It saves reviewed-screenshot candidates and a browser outcome report under ignored `output/playwright/oac-demo-catalog-*/`. Catalog-selected Hello World and Everything Cards then drive the pinned real A2A SDK and MCP Inspector clients, with separate private test trust/credentials and exact local endpoint admission.

Loading and browsing do not invoke these runtimes; the qualification invokes them only in its explicit client phases. Context7 is retained as a Plugin connection, not contacted. AWS, hosted endpoints and source-only/model-backed Agents are not invoked.

The workflow adds one Agent by editing only the copied YAML, previews and explicitly extends the same receipt. It compares all previous receipt identities, native documents and submission-bound evidence, preserves an unrelated governed release, follows every two-item Search page, verifies ARD/AI Catalog counts and package-only Skill exclusions, and proves completed reruns produce no authoring mutations with the archive stopped. Evidence-read and bootstrap-membership audits remain separately classified.

Peer stop/restart leaves published metadata unchanged and preserves TLS/key identities; native calls pass again after restart.

Every exit attempts exact-project `compose-reset`, checks container/network/volume removal and removes private receipt, TLS and client inputs. Success requires both outcome and cleanup; a failed or interrupted report is not qualification. The bounded report is `output/demo/qualification/oac-demo-catalog-*.json`. Built local images and public screenshot/report artifacts remain for inspection. This explicit functional gate supplements `test-demo-runtimes` and the full disposable interoperability gates; it does not add browser/SDK execution to unit tests or change their existing minimal fixtures.
