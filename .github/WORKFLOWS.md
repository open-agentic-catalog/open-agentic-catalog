# GitHub Actions

The workflows call maintained Make targets. Contribution checks run on pull requests and pushes to `main`. Pages prepares the current manual on `main`; delivery needs explicit maintainer activation. Release qualification is manual and requires the exact clean version tag.

Agents do not dispatch hosted jobs, upload project data or publish artifacts. The maintainer owns repository settings and all external execution. Ordinary verification has read-only permissions and does not persist checkout credentials.

## Choose a workflow

| File | Purpose | Important boundary |
| --- | --- | --- |
| [contributions.yml](workflows/contributions.yml) | Always run source lint and isolated units; select UI, docs and clients by changed inputs. | Require `Contribution checks complete` in branch protection. Selected failures and cancellations fail that check. |
| [pages.yml](workflows/pages.yml) | Prepare the complete HTML manual and matching PDF. | Only the separate `github-pages` deployment job has Pages permissions. |
| [release.yml](workflows/release.yml) | Build once, transfer and qualify exact images and client/chart packages. | Publishing is opt-in and uses the protected `release` environment. ARM64 inspection is not native execution. |
| [quality.yml](workflows/quality.yml) | Isolated Go/Python units, source/local regression, UI and the manual. | Separate jobs preserve the test layers and toolchains. The UI job includes browser tests. |
| [clients.yml](workflows/clients.yml) | Go, Python, TypeScript and Rust SDK checks, the Go webhook receiver, and selected editor and raw Go-generator checks. | Local HTTP/TLS fixtures and installed consumers are functional tests. The TypeScript harness owns its runtime matrix. |
| [images.yml](workflows/images.yml) | Backend/UI images for AMD64 and ARM64, exact-image evidence and both Helm packages. | ARM64 is cross-built and inspected. It is not executed. Optional image scans use the same job's images. |
| [functional.yml](workflows/functional.yml) | One selected PostgreSQL, process, packaged API, browser, local OIDC, backup/restore, federation or webhook receiver suite. | Existing harnesses own disposable services, credentials, ports and cleanup. |
| [extended.yml](workflows/extended.yml) | Fresh combined application coverage or selected source/dependency security checks. | Combined coverage runs alone. Existing advisory failures remain failures. |
| [assurance.yml](workflows/assurance.yml) | One focused Go/UI property or mutation check, bounded Go fuzzing, or the existing Job retry arithmetic proof. | A complete mutation measurement can contain survivors. The proof covers one helper, not the service. |
| [benchmarks.yml](workflows/benchmarks.yml) | Ten samples of each existing Go runtime metrics microbenchmark. | Report-only measurements. No hosted timing threshold, unmatched comparison or application capacity claim. |

Quality allows `all`, `unit`, `source`, `lint`, `ui` or `docs`. The `lint` selection runs `make check` without broad regression or API launcher tests. Clients allow `all`, `go`, `python`, `typescript` or `rust`. The default `all` retains complete coverage for those workflows. A focused selection leaves other suites unselected; it does not establish their results. SDK jobs still use at most two concurrent runners and retain the existing language/runtime checks.

The `docs` selection runs `docs-check`, then `docs-site-check`. Its summary reports Markdown/contracts and site outcomes separately. The docs profile installs pinned Markdownlint alongside Go and the documentation Node toolchain. The job needs no Docker socket or other source linters.

The Go selection runs `webhook-report-check`, which includes `sdk-check SDK_LANGUAGE=go`. It checks the independent publication-report receiver without running the SDK gate twice. `client-go-check` remains a separate generated-client result. The `coverage/publication-report-unit.out` profile contains receiver unit coverage, excluding SDK and functional coverage. Existing upload guards also protect this selected file.

The functional workflow also offers `test-backup-restore`, `test-federation-peers` and `test-webhook-report`. Select one suite per run. Federation peers install locked UI dependencies, Chromium system libraries and the browser before the campaign. Backup/restore and receiver tests use the non-UI prerequisite profile. Summaries describe the selected scope; raw database backups, fixture state and credentials are not exported.

Assurance selects `go-properties`, `go-fuzz`, `go-mutation`, `formal`, `ui-properties` or `ui-mutation`. Go mutation also selects one of the thirteen reviewed packages. Property checks use fixed seeds and bounded counts; Go fuzzing uses ten seconds per maintained target and two workers. Formal acquisition has a step-scoped token; proof execution does not. UI property/mutation selections need locked npm packages but no browser installation.

The published [testing guide](../documentation/src/content/docs/contributing/testing.md#github-actions) covers workflow selection, runner requirements, reports and failures. The [build and release guide](../documentation/src/content/docs/contributing/build-and-release.md#github-actions) covers image inputs, the tagged-candidate procedure, artifact identity and distribution boundaries.

## Local execution with act

Use `make workflow-local-tools-install`, then `make workflow-local-list`. The [local runner guide](../test/ci/README.md#local-workflow-commands) lists the eight fixed selections, prerequisites and current qualification limits. Setup and listing accept working changes. Job execution requires clean committed source and uses a disposable copy with private configuration. No GitHub token or project remote is required.

The local runner uses the host UID/GID and a private home. It provides sudo inside the disposable container for tool installation. Setup binds its digest-pinned base and local recipe to the installed image ID. A stale recipe or host identity requires explicit setup again.

Local runs save selected summaries, coverage, packages and image metadata under ignored `output/artifacts/workflows/<run-id>/`. Each `run.json` records execution, report collection and wrapper cleanup separately. Quality jobs copy their selected summary into the disposable workspace before runner removal. Other jobs retain their existing report paths. Failed runs keep available selected files; they do not export raw fixture state or console logs. See [local result fields and cleanup](../test/ci/README.md#read-local-results).

Manual report workflows default to `upload_artifacts=false`. The automatic contribution workflow never uploads reports. Pages delivery uses `deploy=false`, or maintainer-enabled `PAGES_ENABLED=true` on `main`. Every upload is disabled under act. The local wrapper fixes this input to false and starts no artifact or cache server. Source security uses local Trivy scans; automatic npm registry audit submission is removed. Dependency and advisory downloads remain permitted. Hosted dispatch and any external uploads remain maintainer-only.

## Prerequisites and tool ownership

Jobs target fresh `ubuntu-24.04` AMD64 GitHub-hosted VMs. Each job installs only its selected profile through [the CI helper](../test/ci/tools.py), then calls the existing Make installers and checks. Existing Go, Node/npm, Python/uv, Java, Rust and scanner manifests remain authoritative. The helper supplies missing command-line tools and exports their paths for later steps. It does not install application dependencies or start services.

Acquisition requires access to the selected language registries, release archives, advisory databases and container registries. SDK/editor acquisition receives a step-scoped read-only `GH_TOKEN`; later checks do not receive it. Test traffic uses owned local fixtures. Hosted discovery services, resource endpoints and external identity providers are not test targets.

The workflows set `CATALOG_ENV_FILE=/dev/null`. This bypasses a local Make env file; it does not remove inherited environment variables. Jobs use fresh hosted environments and do not load deployment credentials. Verification requires no personal access token or registry login. Only the explicitly selected protected release-delivery job logs in to GHCR.

The preflight checks available disk and memory before tool acquisition. Rust requires at least 20 GiB of free disk. Configure the SDK job to use a runner with sufficient capacity. This conservative admission budget is not a measured cold-run footprint. Do not delete system directories, prune unrelated resources or use RAM disks to bypass the limit.

## Validate workflow files

Install the workflow validator, then run the checks separately:

```sh
make workflow-tools-install CATALOG_ENV_FILE=/dev/null
make workflow-check CATALOG_ENV_FILE=/dev/null
make yaml-check CATALOG_ENV_FILE=/dev/null
make docs-check CATALOG_ENV_FILE=/dev/null
```

`workflow-tools-install` acquires pinned actionlint. `workflow-check` also requires installed ShellCheck and Go. Check targets do not acquire tools implicitly. `make test-tooling-unit` includes the isolated CI helper tests.

## Reports and failures

Jobs preserve failing exit codes and write step summaries. Artifact uploads are disabled by default and always disabled under act. Only the maintainer can select hosted uploads through `upload_artifacts=true`. Those uploads use explicit safe paths and seven-day retention, including selected coverage reports, SPDX inventories and chart archives. They exclude raw fixture directories, credentials, dependency trees and the complete `output/` tree. Image evidence and chart archives use job-owned output paths. Exporters refuse existing destinations. Ordinary report uploads do not publish to a package, chart or image registry. Only the protected release-delivery job performs the explicitly selected external delivery.

A missing report, cancelled job or unselected check is not a pass. Summaries label the status before artifact upload; the upload can still fail the final job. An older report is not evidence for a failed invocation. Coverage percentages from different profiles must not be added or averaged. `coverage-full` creates and reconciles its own fresh profiles.

Assurance artifacts contain safe property/proof reports and reduced mutation counts with source identities. They omit raw mutants, source diffs and verifier logs. Failed fuzzing can retain new bounded synthetic counterexamples. Benchmark artifacts contain the existing source/environment-bound JSON report. Measurements from unrelated runners do not provide a valid before/after performance comparison.

Disposable harnesses own normal cleanup. Forced cancellation can interrupt cleanup, so do not claim cancellation proves cleanup success. Do not use global Docker prune or retained-stack teardown as a recovery step. A fresh runner's disposal is separate from the harness's cleanup result.

Dependency findings fail the selected scan. The workflows do not suppress findings or update dependencies automatically.

## Build and release behavior

Full Git history and tags support existing build identity. Normal builds let Make derive version, full commit and source state. Explicit version/commit overrides report unknown source state. Local image tags use OCI-safe values; binary SemVer build metadata can contain `+`.

`version-check` checks maintained version inputs. A deliberately selected `release-check` requires clean source at the exact `v<VERSION>` tag without overrides. It does not verify tag signatures, sign an artifact, publish or deploy. CI never bumps versions, regenerates checked-in bindings, updates locks, commits or creates tags.

Image build, inspection, SBOM and scans stay together. Ordinary functional harnesses build fixture images. The release workflow instead admits retained candidates and combines their source-bound results into one gate. Separate manual workflow runs do not form that gate.

Repository administrators configure Actions permissions, available runners and required checks. These workflows do not change repository settings or deploy production services. The protected maintainer job can attach artifacts to an existing release.

## Further checks remain explicit

| Area | Existing entry points | Additional prerequisite |
| --- | --- | --- |
| Application load | `test-load`, `test-scale`, `test-soak` | An isolated measurement environment and an explicitly selected workload. |
| Kubernetes | `test-minikube-upgrade`, `test-minikube-ui` | An owned cluster, adequate capacity and the scenario's documented networking. |
| Independent peers | `test-interoperability`, `test-interoperability-agntcy`, `test-interoperability-gateway`, `test-interoperability-ans` | Exact pinned peer acquisition, owned local deployment and cleanup. |
| Models and editor | `test-langgraph-model`, `test-vscode` | A deliberately selected local model or installed-editor/Xvfb environment. |

Run these additional checks through their maintained Make targets. They are not part of the automatic contribution checks.

## Automatic contribution selection

`admission.py` selects existing checks from changed paths. Shared API, backend, version, test and workflow inputs select all SDKs. SDK-only changes select their language. Documentation selection includes the content map and projected SDK/example/reference sources. UI changes select the complete existing UI job. Expensive cluster, model and load tests remain explicit.

The fixed `Contribution checks complete` job runs even when another job fails. It rejects missing, failed, cancelled or unexpectedly skipped selected work. Require this check in branch protection. The maintainer must test fork events and branch settings on GitHub; local checks cannot enforce repository policy against changed workflow source.

## Exact package and release handoff

See [the candidate procedure](../test/ci/README.md#exact-candidate-qualification) for local commands, receipts and refusal paths. SDK checks record current source and run identity beside the actual qualified packages. `stage-sdk` refuses stale receipts or changed packages. Go retains a source manifest without an invented archive. The TypeScript editor checks still require the qualified tarball to match the extension lock.

The release workflow requires `upload_artifacts=true` because its jobs transfer exact candidates. It builds backend/UI images once for AMD64 and ARM64, scans them and archives those exact image IDs. The existing API, Compose UI and OIDC UI journeys consume the AMD64 candidates. Their cleanup preserves borrowed images. The final job also requires source, security, client and chart checks from the same source.

Before delivery, configure required reviewers on the `release` environment and restrict it to version tags. Verify the signed tag yourself. Prepare the matching GitHub release. Select `publish=true` only when ready to push to GHCR and attach the qualified assets. No workflow creates a release, signs artifacts or deploys production.

A failed delivery reports completed operations. Rerun only the failed delivery job to reuse the original handoff key and immutable artifacts. Do not rerun builds after a partial delivery. Artifact expiry requires a new qualification run and maintainer review. Hosted actions and permission settings require separate maintainer qualification.
