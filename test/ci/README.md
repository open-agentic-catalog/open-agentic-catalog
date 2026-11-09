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

The Go client job reuses its existing prerequisites for `webhook-report-check`. That target checks the Go SDK and the independent publication-report receiver. `client-go-check` remains separate. The receiver profile at `coverage/publication-report-unit.out` excludes SDK and functional coverage. The act wrapper has no SDK selection. Run these checks through their native Make targets.

The functional workflow uses `functional` prerequisites for backup/restore and standalone receiver tests. Its peer federation selection uses `functional-ui` and installs UI dependencies, Chromium libraries and the browser. All selections call fixed existing Make targets and preserve failing exits. The act wrapper retains its eight selections; run the three additional campaigns through their native Make targets.

Acquisition verifies exact-release publisher checksums or uses the selected language registry's integrity controls. Those downloaded manifests do not provide independent committed checksums or attestations. ShellCheck uses the committed digest for its exact Linux AMD64 archive because its release has no separate checksum asset.

Installations report selected tool versions without printing the environment. They write only owned `.tools/ci/` paths and the hosted job's Compose plugin path. ShellCheck installs under `.tools/ci/bin/` without changing system packages. No daemon, Catalog fixture or deployment starts here.

Compose 5 uses Docker Buildx/Bake for builds. The Docker installation must include Buildx for profiles that build images.

Compose plugin installation requires `GITHUB_ACTIONS=true` and refuses ordinary local invocation before it accesses the home plugin directory. Local artifact qualification uses a verified standalone binary under an owned directory. It does not replace the user's Docker plugin.

The Markdown CLI installer applies `dependencyCutoff` from `pins.json` through npm's `--before` option. Review the resolved packages when advancing that cutoff. The YAML tool and its PyYAML/pathspec dependencies also use exact pins in that manifest.

`make workflow-tools-install` acquires actionlint explicitly. `make workflow-check` checks its Go module/version metadata without automatic toolchain acquisition, then checks workflow files and shell commands with installed ShellCheck. Each target runs alone and bypasses local Make env files. `make check` includes `workflow-check`.

## Source and cleanup ownership

`test_unit_tools.py` contains isolated checksum-selection, capacity and missing-tool admission cases. It starts no server or subprocess and acquires no tools. `make test-tooling-unit` includes the `ci` owner. The packaging cleanup fixture covers `.tools/ci/` and rejects symlink redirection.

`.tools/ci/` is ignored and included in the existing `make clean` inventory. Cleanup retains its source, mount and nested-repository protections. Do not clean tools used by another task or replace them with a RAM disk. Rust's 20 GiB admission budget requires a runner with sufficient free disk.

## Exact candidate qualification

`admission.py` owns changed-path selection, fixed outcomes and qualified SDK staging. Its units run without Git, Docker or network calls. Functional tests exercise process admission separately. `candidate.py` owns exact image archives. `release_delivery.py` assembles admitted results and assets. Only its explicitly selected protected hosted `publish` command can perform external delivery. Never run that command locally.

Root `pyproject.toml` and `uv.lock` changes select UI, documentation and every SDK language. Those jobs consume the shared Python installer or locked development tools. Changes beneath a specific SDK or documentation directory retain their existing selection. Selected failures, cancellations and unexpected skips fail the completion gate.

`fuzz_corpus.py` collects new synthetic Go counterexamples after a failed fuzz measurement. Its package allowlist has a unit contract against `make test-fuzz`. Collection uses untracked, nonignored Git paths and preserves source files. It accepts regular files with hexadecimal names and the Go corpus header, within 1 MiB per file and 16 MiB total. Reports stay under ignored `output/`; redirected paths and existing destination files are refused. Collection does not change the failed measurement result. Unit cases and the native functional selection test the helper and the workflow's actual shell command without a fuzz campaign or upload.

For an SDK handoff, run the existing check and stage its actual output:

```sh
SDK_RUN_ID=review-python-1 make sdk-check SDK_LANGUAGE=python
python3 -B test/ci/admission.py stage-sdk --language python --run review-python-1 --output output/artifacts/qualified-sdk/review-python-1
```

A stale receipt, changed package, incomplete package set or changed source fails admission. Do not run `sdk-package` between qualification and staging. Preserve the existing language/runtime matrices and TypeScript extension-lock checks.

For local candidate preparation, select fresh ignored directories. Build each image once. Run its existing inspection, SBOM and scanner against that image. Store inspection JSON and SBOM/notices inside the candidate directory. Keep raw scans outside it. For example, use `make --silent container-inspect > output/release/backend-amd64/inspection.json`. Then seal the candidate:

```sh
python3 -B test/ci/candidate.py seal --directory output/release/backend-amd64 --image open-agentic-catalog:candidate-review-amd64 --product backend --platform linux/amd64 --scan-directory output/release/backend-amd64-scan-private
```

The command requires the matching successful scan, exact inspection and SPDX inventory. It saves `image.tar` and inventories exact bytes. The release workflow provides the complete build sequence for both products/platforms. A local modified source is permitted for testing; hosted release qualification separately requires a clean matching tag.

Load transferred archives before using another engine. Explicit admission refuses source drift, archive changes, wrong platforms and substituted image IDs:

```sh
python3 -B test/ci/candidate.py load --backend output/release/backend-amd64/candidate.json --ui output/release/ui-amd64/candidate.json
CATALOG_CANDIDATE_BACKEND="$PWD/output/release/backend-amd64/candidate.json" make test-functional-default-off
CATALOG_CANDIDATE_BACKEND="$PWD/output/release/backend-amd64/candidate.json" CATALOG_CANDIDATE_UI="$PWD/output/release/ui-amd64/candidate.json" make test-ui-compose
CATALOG_CANDIDATE_BACKEND="$PWD/output/release/backend-amd64/candidate.json" CATALOG_CANDIDATE_UI="$PWD/output/release/ui-amd64/candidate.json" make test-ui-oidc
```

These journeys use immutable local image IDs and verify served build identity. They still create owned database, identity-provider and fixture services. Cleanup removes fixture images, not borrowed candidates. Instrumented, MCP-only and other specialized modes reject candidate inputs. Harness source/configuration identity remains separate from image identity.

Candidate runtime admission binds to the current native Linux AMD64 or ARM64 host. `admit` and `load` accept `--platform linux/amd64|linux/arm64` as an additional exact-host assertion; it never enables emulation. Backend and UI candidates must select that same platform and build. This bounded admission does not qualify an ARM64 workflow toolchain: the maintained CI installer/preflight still supports Linux AMD64 only.

Before native ARM64 qualification, record `uname -sm`, installed compatible Go/Node/Chromium/Compose versions, Docker daemon architecture, and the architecture/immutable IDs of every selected local image. Do not acquire an AMD64 tool closure and call it native ARM64 evidence. On a compatible native ARM64 host, start with the existing `make container-verify CONTAINER_PLATFORM=linux/arm64` using the inspected backend image. Run wider candidate-bound API/UI fixtures only when their tool, browser and external-image prerequisites are actually available. Record each unrun scope explicitly; an AMD64 host or emulated execution does not satisfy native ARM64 qualification. These local checks do not add a hosted platform matrix or change release policy.

The release workflow requires all source/security/client/chart/image/runtime jobs and complete same-source receipts. AMD64 executes the packaged journeys. ARM64 receives packaging inspection and scans only. Its image archive is still retained for delivery.

The maintainer must enable protected `github-pages` and `release` environments, restrict deployment refs and verify hosted events and permissions. Signed tags and the matching GitHub release remain maintainer inputs. No local test dispatches hosted jobs, uploads artifacts, logs into a registry or publishes anything.

## Release package handoffs

`release_packages.py cli` extracts `catalogctl` and notices from a retained backend image. It reuses static image inspection and never recompiles. Every archive is unpacked into a fresh temporary directory outside the checkout, with exact member inventory, regular-file types, modes, sizes and SHA-256 payload equality against the inspected image extraction. AMD64 runs version/help from that unpacked artifact and requires the expected version, commit and source state; ARM64 receives content inspection only. Keep the CLI output outside the sealed candidate directory. Failed packaging removes its owned output, and temporary consumers are always removed.

```sh
python3 -B test/ci/release_packages.py cli --candidate output/release/backend-amd64/candidate.json --platform linux/amd64 --output output/release/cli-amd64
```

`release_packages.py editor --output output/artifacts/qualified-editor/<run>` runs `make test-vscode`, then stages only the tested VSIX and `editor.json`. Its receipt binds the source, SDK, artifact and successful cleanup. Do not run another package command after testing. The command requires the existing full editor test tools and Linux prerequisites. Raw reports remain private.

Release assembly requires both CLI platforms, the editor receipt, all four SDK receipts, both charts and the existing runtime/image evidence. `SHA256SUMS` lists only flat public downloads. Internal image archive hashes remain in `release.json`. The evidence archive excludes image archives and package downloads.

Unit tests intercept every delivery command. Functional fixtures create tags only inside disposable repositories. Ordinary image/chart workflows can run through act with uploads disabled. The release policy deliberately refuses act. Run SDK and installed-editor gates through their native Make targets.

The [maintainer procedure](../../documentation/src/content/docs/contributing/build-and-release.md#configure-and-approve-delivery) owns activation, approvals, prepared drafts and failed-job reruns. Tag events use effective handoff inputs from the policy job. A manual `publish=false` disables delivery even when the repository switch is enabled. No local verification changes GitHub settings or sends project data externally.

## Hosted package retention

Ordinary image, chart, SDK and editor checks build packages to verify them. Their optional `upload_artifacts` input uploads reports only. Package files remain on the temporary runner.

The release workflow passes `retain_packages=true` to the reusable client workflow. This input is absent from manual dispatch. SDK and VSIX uploads also require a version-tag ref and the release handoff upload input. Manual `release_checks=true` selects installed-editor tests without enabling package uploads. Release chart and image archives keep their existing same-run handoff. Pages delivery remains separately enabled. Local act keeps its selected inspection evidence under ignored output and performs no hosted uploads.
