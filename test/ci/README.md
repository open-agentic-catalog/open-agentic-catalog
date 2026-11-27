# Workflow prerequisites

This directory supports the [GitHub Actions workflows](../../.github/WORKFLOWS.md). `tools.py` owns runner capacity checks, explicit tool acquisition, path exports and the installed actionlint check. `pins.json` contains CLI versions not already owned by another tool manifest.

## Local workflow commands

`local_workflows.py` provides fixed local act selections. Install the pinned tools, then list the commands:

```sh
make workflow-local-tools-install
make workflow-local-list
```

Setup requires Linux AMD64, Python 3.12, a non-root host account, Docker with Buildx, a local Unix socket and 8 GiB free. It verifies the act archive checksum, retains its MIT license and pulls one digest-pinned Ubuntu 24.04 base. It builds a local runner from [Containerfile.act](Containerfile.act) with the host UID/GID and passwordless sudo inside that container. The recipe adds no packages. Setup checks the non-root identity, sudo, Python venv/TLS support, Node 24, compiler tools and Docker Buildx. Setup does not run a project workflow.

The installation receipt binds the base, recipe checksum, host identity and exact local image ID. Setup records the ready state only after every prerequisite probe passes. Job commands reject the incomplete receipt after a failed probe. Rerun setup after a recipe or host identity change. The local image and its base remain reusable Docker dependencies.

| Command | Selection |
| --- | --- |
| `workflow-local-lint` | `quality.yml`: source lint only |
| `workflow-local-unit` | `quality.yml`: isolated Go and Python units |
| `workflow-local-ui` | `quality.yml`: UI checks |
| `workflow-local-docs` | `quality.yml`: documentation checks |
| `workflow-local-charts` | `images.yml`: both Helm charts |
| `workflow-local-image` | `images.yml`: one AMD64 backend image, with inspection and scanning |
| `workflow-local-security` | `extended.yml`: source and UI dependency scans |
| `workflow-local-postgres` | `functional.yml`: disposable PostgreSQL coverage |

Set `WORKFLOW_LOCAL_PRODUCT=ui` for the UI image selection. Run each Make command alone. These targets bypass Make env files before inclusion. Listing needs no installed act or Docker and starts no containers.

The image selection also accepts `WORKFLOW_LOCAL_SCAN=true|false`. Image and chart selections accept `WORKFLOW_LOCAL_RELEASE_CANDIDATE=true|false`. Values must use exactly `true` or `false`. The defaults enable image scanning and disable candidate checks. Candidate checks require clean source at the existing `v<VERSION>` tag.

```sh
make workflow-local-image WORKFLOW_LOCAL_PRODUCT=ui WORKFLOW_LOCAL_SCAN=false
make workflow-local-charts WORKFLOW_LOCAL_RELEASE_CANDIDATE=true
```

Execution requires clean committed source. The wrapper copies Git objects, history, tags and HEAD into a private temporary checkout. It omits ignored files, private Git configuration, hooks and remotes. It does not commit, stash or reset the original checkout.

The wrapper excludes ambient credentials and configuration, fixes uploads to false, and disables artifact/cache servers and telemetry. Public dependencies and advisory data can be downloaded. Project source, reports, inventories and credentials are not submitted externally. All workflows skip uploads under act, even when an upload input is true. There is no local upload option.

Installed act files stay under ignored `.tools/ci/act/`. Source copies, action caches and private configuration stay in an owned on-disk `/tmp` directory. The wrapper removes that directory after execution. It checks actual runner mounts before removing job volumes and verifies ownership before removing its tool-cache volume. It refuses existing act containers, conflicting job volumes and existing tags for the selected run. Other existing containers, volumes and images remain protected.

The runner binds the disposable source at its host path. Jobs use a private home within the shared scratch directory. Docker jobs use explicit host networking for host-published loopback ports. Image, chart and PostgreSQL selections receive the Docker socket and its group. Other selections use a runner without that socket. Compose installs its selected plugin inside the disposable runner home.

PostgreSQL fixture names include the CI run ID and shell PID. This avoids PID collisions between runner containers. Startup and cleanup use the same project name.

Execution uses a monotonic deadline matching the selected job budget. Timeout or interruption signals the owned runner's processes before stopping act. The harness receives 30 seconds to run cleanup. Further bounded termination handles an unresponsive process. Selected reports remain available after a failed or interrupted run.

After the job ends, a network-disabled helper restores ownership only within the disposable workspace when needed. It does not follow child links. The wrapper then copies selected reports and removes temporary source, configuration and caches. Scoped cleanup removes per-run image tags after recording their image IDs. The runner image, downloaded base images and normal Docker build cache remain reusable dependencies. Repository cleanup does not prune Docker.

## Read local results

Accepted runs write ignored `output/artifacts/workflows/<run-id>/run.json`. Selected files remain beneath that directory's `reports/` subtree. The wrapper prints the destination and separate execution, report and cleanup outcomes. The result names the source commit, runner image, effective inputs and retained files with their sizes and SHA-256 digests. Invalid inputs, missing tools and dirty source are refused before report creation.

| Selection | Retained files |
| --- | --- |
| Lint and documentation | The selected job summary |
| Units | The unit summary and `coverage/unit.out` |
| UI | The UI summary and existing component coverage summary/LCOV files |
| Charts | The chart summary and one package for each maintained chart |
| Images | The image summary, selected SPDX inventory and built image identity |
| Source security | The job summary |
| PostgreSQL | The fixture summary and `coverage/postgres.out` |

Raw console logs, credentials, fixture state, source copies and dependency trees are not exported. Selected Go coverage profiles permit 256 MiB; other reports permit 64 MiB. The combined limit remains 256 MiB. The native unit profile currently needs about 211 MiB. Symlinks, redirected ancestors, special files and extra matching packages cause failure. Each destination is new, so an older report cannot satisfy the current run.

`execution` records setup or the act process outcome. `setup_failed` means that the job did not start. `reportStatus` identifies complete, partial or failed collection. `cleanup` records the wrapper's observed resources and workspace separately; native fixture cleanup remains part of the execution result.

Overall `status` passes only when execution, required reports and cleanup all pass. A zero-job invocation without the required summary fails. The original failing process exit is preserved; missing reports or cleanup failure also return nonzero. Cleanup failures are reported even when Docker setup fails before the job starts.

If a later report fails validation or copying, `files` still lists every earlier complete copy with its size and digest. Report collection remains failed, and the original nonzero execution exit is preserved. If a file write fails, the collector removes that incomplete file. Existing destination files remain protected.

If cleanup fails, inspect `cleanupErrors` and the console diagnostic. `remainingDirectory` identifies an owned temporary directory that could not be removed safely. Inspect resources with `docker ps -a --filter label=io.open-agentic-catalog.workflow-local=<run-id>`. Finish the affected harness cleanup before removing its remaining directory. Do not prune Docker or delete unrelated act resources. An abrupt forced kill cannot guarantee cleanup.

Both `make clean` and `make clean-output` include the exported report directory. Normal cleanup also covers installed act files. Follow the [cleanup guide](../../documentation/src/content/docs/contributing/build-and-release.md#clean-generated-output-separately) before deleting anything. These cleanup targets do not remove Docker dependencies.

Report actual workflow results separately from helper and runtime controls. A failed native check remains a failed local workflow, even when report collection and cleanup succeed. Review upstream act binary advisories separately from workflow results. Local execution does not qualify hosted events, permissions or OIDC.

Run isolated helpers with `make test-tooling-unit`. Run Git/Make/process cases with `python3 -B -m unittest discover -s test/ci -p 'test_functional_*.py'`. The ordinary functional Go gate includes the same process fixtures. Select installed-tool controls with `CATALOG_TEST_ACT=/absolute/path/to/act`; add `CATALOG_TEST_ACT_RUNNER=1` for the small network-disabled Docker control. Add `CATALOG_TEST_ACT_RUNTIME=1` for owned Compose, loopback, image-build, report and cancellation controls. Run these Docker selections serially. They do not execute complete application workflows.

## Interfaces

Use Python 3.12 and a fixed workflow-selected profile for hosted bootstrap:

```sh
python3.12 -B test/ci/tools.py preflight --profile quality
python3.12 -B test/ci/tools.py install --profile quality
```

Profiles select system/tool prerequisites only. Existing Make installers still own Python development dependencies, UI/docs packages, SDKs, the editor and formal tools. Workflow steps install browser system libraries after the selected locked packages when the selected suite needs a browser. The Python SDK and formal profiles select the runner's installed `JAVA_HOME_21_X64`. Full coverage omits source-lint and Node tooling. Runtime microbenchmarks reuse the unit prerequisite profile without a database or Docker.

The `docs` profile selects Go, the documentation Node/npm versions and pinned Markdownlint. It omits YAML, ShellCheck, Hadolint, actionlint and Docker prerequisites. The documentation job runs Markdown/repository contracts before site checks and records their outcomes separately.

The Go client job runs `sdk-go-examples-check`, which checks the SDK once and runs both receiver examples' unit tests. The MCP Scanner receiver also exercises its local MCP/TLS transport. The optional client report upload includes unit coverage for the publication-report receiver at `coverage/publication-report-unit.out`. Run `client-go-check` separately to qualify the standalone Go generator. Use the native Make targets for SDK checks when working locally.

The functional workflow uses `functional` prerequisites for backup/restore and standalone receiver tests. Its peer federation selection uses `functional-ui` and installs UI dependencies, Chromium libraries and the browser. All selections call fixed existing Make targets and preserve failing exits. The act wrapper retains its eight selections; run the three additional campaigns through their native Make targets.

Archive acquisition checks reviewed SHA-256 values retained in `pins.json`, with Node using the existing `test/sdk/node-runtimes.json` records. Go, Node and npm versions keep their owning manifests. Unknown or inconsistent asset selections fail before download. Native Go tools retain exact version-suffixed installation and the Go checksum database. These byte checks are not publisher signatures or artifact attestations.

Installations report selected tool versions without printing the environment. They write only owned `.tools/ci/` paths and the hosted job's Compose plugin path. ShellCheck installs under `.tools/ci/bin/` without changing system packages. No daemon, Catalog fixture or deployment starts here.

Compose 5 uses Docker Buildx/Bake for builds. The Docker installation must include Buildx for profiles that build images.

Compose plugin installation requires `GITHUB_ACTIONS=true` and refuses ordinary local invocation before it accesses the home plugin directory. Local artifact qualification uses a verified standalone binary under an owned directory. It does not replace the user's Docker plugin.

The uv bootstrap uses `test/ci/uv/requirements.txt`, pip hash mode and a fresh owned environment. The existing `make dev-tools-install` separately synchronizes `pyproject.toml` and `uv.lock` into `.tools/dev`, including yamllint and its locked dependencies. It remains safe to repeat that locked development sync.

Markdownlint uses the private native project under `test/ci/markdown/`. `markdown_install()` copies its manifest/lock into the existing owned tool directory and runs `npm ci`, without lifecycle scripts, audit submissions or funding requests. Ordinary installation does not resolve dependencies, apply a moving date filter or repair the lock. The separate npm archive is verified before extraction and leaves the reviewed Node distribution unchanged.

`make workflow-tools-install` acquires actionlint explicitly. `make workflow-check` checks its Go module/version metadata without automatic toolchain acquisition, then checks workflow files and shell commands with installed ShellCheck. The upload-policy test in `make test-tooling-unit` uses the same `.yml` and `.yaml` inventory under `.github/workflows/` and rejects new workflow names until reviewed. Each Make workflow target runs alone and bypasses local Make env files. `make check` includes `workflow-check`.

## Deliberate input updates

Keep direct version selection in each existing owning manifest. Fetch official artifact metadata during a deliberate update, compare the exact filename and reviewed SHA-256, and update the existing archive record. Do not retrieve a checksum manifest as the ordinary install-time trust anchor. Native project locks retain package sources and integrity.

Regenerate the Markdownlint lock with the selected npm, scripts/audits/funding disabled, and review its complete dependency diff. Keep the ordinary cutoff in `pins.json` for unrelated releases. The [dependency policy](../../documentation/src/content/docs/reference/dependencies.md#ci-bootstrap-and-source-tools) records the exact supported npm/Markdownlint security exceptions. Do not use forced upgrades or overrides to bypass incompatible ranges.

Use the qualified Trivy and current advisory data to review the verified unpacked npm distribution with `trivy rootfs`. Check the bundled package inventory, including Undici. A source scan excludes installed tool trees, and default filesystem scanning does not inventory this lock-free distribution. No generated npm self-lock or custom SBOM converter is needed. This review belongs to pin qualification, not ordinary installation. Preserve lower-severity findings separately from failed High/Critical gates.

The hosted installer and local act harness keep their Linux AMD64/Python 3.12 requirements. Native contributor instructions use regular installed Node/npm, Python and Go tools. The existing Node helper supports Intel/ARM Macs and compatible Linux x64/ARM64 distributions. Pi and Explorer retain their separate Linux/systemd qualification limits; tool acquisition does not port those harnesses.

## Source and cleanup ownership

`test_unit_tools.py` contains isolated checksum-selection, capacity and missing-tool admission cases. It starts no server or subprocess and acquires no tools. `make test-tooling-unit` includes the `ci` owner. The packaging cleanup fixture covers `.tools/ci/` and rejects symlink redirection.

`.tools/ci/` is ignored and included in the existing `make clean` inventory. Cleanup retains its source, mount and nested-repository protections. Do not clean tools used by another task or replace them with a RAM disk. Rust's 20 GiB admission budget requires a runner with sufficient free disk.

## Exact candidate qualification

`admission.py` owns changed-path selection, fixed outcomes and qualified SDK staging. Its units run without Git, Docker or network calls. Functional tests exercise process admission separately. `candidate.py` owns exact image archives. `release_delivery.py` assembles admitted results and assets. Only its explicitly selected protected hosted `publish` command can perform external delivery. Never run that command locally.

Root `pyproject.toml` and `uv.lock` changes select UI, documentation and every SDK language. Those jobs consume the shared Python installer or locked development tools. Changes beneath a specific SDK or documentation directory retain their existing selection. Selected failures, cancellations and unexpected skips fail the completion gate.

The UI, documentation and TypeScript SDK jobs all consume the root `LICENSE` and `catalog-ui/scripts/native-test-completion.mjs`. Changing either selects all three. Changing `examples/tutorials/local-oidc/service-read.py` also selects the Python SDK job, which runs that tutorial against its installed packages.

`fuzz_corpus.py` collects new synthetic Go counterexamples after a failed fuzz measurement. Its package allowlist has a unit contract against `make test-fuzz`. Collection uses untracked, nonignored Git paths and preserves source files. It accepts regular files with hexadecimal names and the Go corpus header, within 1 MiB per file and 16 MiB total. Reports stay under ignored `output/`; redirected paths and existing destination files are refused. Collection does not change the failed measurement result. Unit cases and the native functional selection test the helper and the workflow's actual shell command without a fuzz campaign or upload.

For an SDK handoff, run the existing check and stage its actual output:

```sh
SDK_RUN_ID=review-python-1 make sdk-check SDK_LANGUAGE=python
python3 -B test/ci/admission.py stage-sdk --language python --run review-python-1 --output output/artifacts/qualified-sdk/review-python-1
```

A stale receipt, changed package, incomplete package set or changed source fails admission. Do not run `sdk-package` between qualification and staging. Preserve the existing language/runtime matrices and TypeScript extension-lock checks.

To prepare both Linux AMD64 candidates, install the image tools and a current [Trivy database](../../documentation/src/content/docs/contributing/build-and-release.md#scan-local-images). Start a shell at the repository root. Keep the `candidates` directory for the runtime and standalone checks below.

```sh
set -eu
mkdir -p output/release
candidates=$(mktemp -d output/release/local-XXXXXXXX)
(
  export CATALOG_ENV_FILE=/dev/null CONTAINER_PLATFORM=linux/amd64
  unset VERSION COMMIT
  image_tag=$(basename "$candidates" | tr '[:upper:]' '[:lower:]')
  export IMAGE="open-agentic-catalog:$image_tag" UI_IMAGE="open-agentic-catalog-ui:$image_tag"
  backend="$candidates/backend"
  ui="$candidates/ui"
  mkdir "$backend" "$ui"

  make container-build
  make --silent container-inspect > "$backend/inspection.json"
  make licenses ARTIFACT_OUTPUT="$backend/licenses"
  make sbom ARTIFACT_OUTPUT="$backend/sbom"
  make container-scan ARTIFACT_OUTPUT="$backend-scan-private"
  python3 -B test/ci/candidate.py seal --product backend --image "$IMAGE" \
    --directory "$backend" --platform "$CONTAINER_PLATFORM" --scan-directory "$backend-scan-private"

  make ui-container-build
  make --silent ui-container-inspect > "$ui/inspection.json"
  make ui-sbom ARTIFACT_OUTPUT="$ui/sbom"
  make ui-container-scan ARTIFACT_OUTPUT="$ui-scan-private"
  python3 -B test/ci/candidate.py seal --product ui --image "$UI_IMAGE" \
    --directory "$ui" --platform "$CONTAINER_PLATFORM" --scan-directory "$ui-scan-private"
)
```

Each `seal` command checks the inspection, scan and SPDX inventory against the selected image. It writes `candidate.json` and `image.tar` in that product's directory. You can rehearse on modified source. For release builds, use the clean matching tag.

To rehearse the three Linux AMD64 runtime jobs, use the checkout that produced the candidates and install the packaged UI prerequisites, including Chromium. In the same shell, run the commands below. For a downloaded workflow handoff, set `candidates=output/release` after unpacking the backend and UI candidates there. Admission checks each candidate's commit and source digest before loading its archive.

```sh
(
  set -eu
  export CATALOG_ENV_FILE=/dev/null
  unset VERSION COMMIT
  export CATALOG_CANDIDATE_BACKEND="${candidates:?select the candidate directory}/backend/candidate.json"
  export CATALOG_CANDIDATE_UI="$candidates/ui/candidate.json"
  python3 -B test/ci/candidate.py load \
    --backend "$CATALOG_CANDIDATE_BACKEND" --ui "$CATALOG_CANDIDATE_UI" --platform linux/amd64
  make -j1 test-functional-default-off
  make test-ui-compose
  make test-ui-oidc
)
```

The [release workflow](../../.github/workflows/release.yml) uses the backend image for the API suite and both images for the browser suites. Keep both candidate variables set for all three jobs. Write a `runtime-result` receipt only after its suite and fixture cleanup succeed. The receipt records the admitted image IDs.

These journeys use immutable local image IDs and verify served build identity. They still create owned database, identity-provider and fixture services. Cleanup removes fixture images, not borrowed candidates. Instrumented, MCP-only and other specialized modes reject candidate inputs. Harness source/configuration identity remains separate from image identity.

Candidate runtime admission binds to the current native Linux AMD64 or ARM64 host. `admit` and `load` accept `--platform linux/amd64|linux/arm64` as an additional exact-host assertion; it never enables emulation. Backend and UI candidates must select that same platform and build. This bounded admission does not qualify an ARM64 workflow toolchain: the maintained CI installer/preflight still supports Linux AMD64 only.

For a native ARM64 container test, record `uname -sm`, Docker daemon architecture, selected image IDs and the versions of tools used. On that host, `make container-verify CONTAINER_PLATFORM=linux/arm64` builds, inspects and executes the backend image. Run the candidate-bound API and UI journeys when compatible tools, browser and fixture images are available. Record which journeys ran.

The release workflow inspects and scans both backend and UI images for Linux AMD64 and ARM64. It runs the packaged API and browser journeys against the retained Linux AMD64 images. A separate job executes the standalone downloads on native Linux AMD64, Linux ARM64, macOS Intel and macOS Apple Silicon. Release assembly requires all four native results alongside the source, security, client, chart, image and packaged runtime checks.

Release delivery uses the protected `release` environment. Configure its tag restriction and reviewer before dispatch. The maintainer supplies the signed tag and matching GitHub release draft. Pages deployment uses the separate `github-pages` environment. No local test dispatches hosted jobs, uploads artifacts, logs into a registry or publishes anything.

## Release package handoffs

The [release chart check](../../documentation/src/content/docs/contributing/build-and-release.md#package-helm-charts) uses `helm show chart` on each archive produced by `make helm-package`. The chart name and package version must match that chart's source metadata, and `appVersion` must match root `VERSION`. It then records the archive hashes for assembly. Before upload, `make helm-oci-test HELM_OCI_QUALIFIED="$ARTIFACT_OUTPUT"` pushes the qualified files to a loopback registry and verifies anonymous Helm pulls against those hashes.

The release workflow runs `release_packages.py cli --standalone` against each sealed backend candidate. It extracts the Linux server and CLI, cross-builds the additional CLI and coordinator downloads, checks linked Go modules against the image notices, and packages the five portable Skill files. Each native runner exercises the retained programs on its own OS and architecture. The extracted CLI must read the loopback manifest once without credentials and return the exact entry and request ID. A mismatch fails qualification before the native result file is created. Assembly checks the Linux executables against image inspection, every archived notice against the image inventory, the Skill against its source and every native result against the archive digest. To repeat the native check on a matching host, run `python3 -B test/ci/release_standalone.py --directory <program-handoff> --platform <os>/<arch> --output <new-result-file>`.

```sh
python3 -B test/ci/release_packages.py cli --standalone \
  --candidate "${candidates:?select the candidate directory}/backend/candidate.json" \
  --platform linux/amd64 --output "$candidates/cli-amd64"
```

Check the [Linux host prerequisites](../vscode/README.md). With the qualified TypeScript SDK archive in place, run `make vscode-tools-install` to install the pinned editor tools. Use the same SDK run ID and a fresh editor output directory for the check.

```sh
python3 -B test/ci/release_packages.py editor \
  --output "output/artifacts/qualified-editor/${SDK_RUN_ID:?set the tested TypeScript SDK run ID}"
```

This command runs `make test-vscode` and stages the installed VSIX with `editor.json`. The receipt records its SHA-256, the tested SDK hash, the source and successful fixture cleanup. Use the staged VSIX for release assembly.

Release assembly requires every native program result, the Skill, the editor receipt, all four SDK receipts, both charts and the existing runtime/image evidence. `SHA256SUMS` lists only flat public downloads. Internal image archive hashes remain in `release.json`. The evidence archive excludes image archives and package downloads.

Unit tests intercept every delivery command. Functional fixtures create tags only inside disposable repositories. Ordinary image/chart workflows can run through act with uploads disabled. The release policy deliberately refuses act. Run SDK and installed-editor gates through their native Make targets.

The [maintainer procedure](../../documentation/src/content/docs/contributing/build-and-release.md#configure-and-approve-delivery) owns activation, approvals, prepared drafts and failed-job reruns. The release workflow accepts manual dispatch on the exact version tag. Admission verifies the SSH signature with the repository variable `RELEASE_ALLOWED_SIGNERS` and checks protected-main ancestry. Qualification selects the exact input names for the same run and attempt. A delivery retry downloads the successful bundle by artifact ID. A manual `publish=false` disables delivery even when the repository switch is enabled. No local verification changes GitHub settings or sends project data externally.

## Hosted package retention

Ordinary image, chart, SDK and editor checks build packages to verify them. Their optional `upload_artifacts` input uploads reports only. Package files remain on the temporary runner.

The release workflow passes `retain_packages=true` to the reusable client workflow. Manual dispatch of the client workflow does not expose this input. SDK and VSIX uploads also require a version-tag ref and the release handoff upload input. Manual `release_checks=true` selects installed-editor tests without enabling package uploads. Release chart and image archives keep their existing same-run handoff. Pages delivery remains separately enabled. Local act keeps its selected inspection evidence under ignored output and performs no hosted uploads.
