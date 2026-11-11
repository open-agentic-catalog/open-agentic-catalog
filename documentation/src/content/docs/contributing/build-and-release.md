---
title: Build, test and release artifacts
---

Build and test local container images. Promote the tested images through your continuous integration and delivery (CI/CD) pipeline.

The repository provides image builds, inspection, license files, software bills of materials (SBOMs) in [SPDX format](https://spdx.dev/use/specifications/) and versioned Helm packages. These commands do not publish artifacts, create a registry or export image archives. They require no particular pipeline.

## GitHub Actions

Pushes to `main` build and inspect AMD64 backend/UI images and package both Helm charts. These builds never publish images or packages. TypeScript contribution checks also build a VSIX after its unit checks.

Use **Images and charts** (`images.yml`) manually to include both `linux/amd64` and `linux/arm64`. The separate chart job validates and packages the application and monitoring charts. See [workflow selection and reports](testing.md#github-actions).

| Manual input | Default | Effect |
| --- | --- | --- |
| `upload_artifacts` | `false` | Retain selected reports for seven days. Package archives stay on the temporary runner. Local act skips uploads. |
| `scan` | `false` | Scan each exact image for High and Critical findings. |
| `release_candidate` | `false` | Require clean source at the exact `v<VERSION>` tag before building. |

All builds run `version-check`. Make derives the version, full commit and source state from the checkout. CI never bumps `VERSION`, creates tags or updates locks. A branch commit can have release version metadata when tagged. That metadata does not enable publishing.

The backend AMD64 build executes both version commands and packages its extracted CLI. Other image jobs inspect binaries without executing them. ARM64 inspection does not establish native runtime behavior. Every UI image build also renders the bundled manual.

### Run checks for a release candidate

The **Qualified release candidates** workflow (`release.yml`) runs for pushed `v*` tags and manual selections. It requires the exact `v<VERSION>` ref and clean source. Root `VERSION` remains `0.1.0` until the maintainer deliberately changes it. `release-check` checks the tag and source, not the tag signature.

| Event or selection | Checks and internal handoff | External delivery |
| --- | --- | --- |
| Push to `main` or a pull request | Selected contribution checks; `main` also builds images and charts. | None. |
| Push the exact `v<VERSION>` tag | Complete source, client, chart, image and runtime checks. Internal artifact uploads are required. | Disabled unless `RELEASE_PUBLISH_ENABLED` equals `true` and the protected job is approved. |
| Manual at the exact tag, `upload_artifacts=true`, `publish=false` | The same complete checks and internal handoff. | None, even when the repository switch is enabled. |
| Manual at the exact tag, both inputs `true` | The same complete checks and internal handoff. | Requires the repository switch and protected job approval. |
| A branch, wrong tag or local act release invocation | Refused by release policy. | None. |

Tag events have no manual input defaults. The policy job supplies effective upload and publish values to dependent jobs. Uploads between release jobs are Actions artifacts, not public release downloads. Ordinary verification tokens have read-only repository permissions.

The workflow builds backend/UI candidates once for AMD64 and ARM64. Each candidate retains its image archive, inspection, SPDX inventory, notices and bounded scan summary. Raw scan output stays private. Later jobs run existing AMD64 API, Compose UI and OIDC UI journeys against those images. They do not rebuild application images.

Ordinary client checks build and test SDK packages and the VSIX without uploading those files. Even `upload_artifacts=true` uploads only selected reports. Release qualification enables the internal package handoff at the exact version tag. It also runs `test-vscode` on Linux and retains the exact VSIX after successful cleanup. The editor receipt binds the source and tested TypeScript SDK. Raw editor reports, SSH fixtures and tool paths stay private. No Windows or macOS execution is claimed.

### Download release packages

Files become available only after a maintainer publishes a completed GitHub Release. Use its exact tag's assets, not development build directories. No packages are published to npm, PyPI, crates.io, the Go proxy or the VS Code Marketplace.

| Download | Contents or use |
| --- | --- |
| `catalogctl-<version>-linux-amd64.tar.gz` | Static Linux AMD64 CLI and existing backend license notices. |
| `catalogctl-<version>-linux-arm64.tar.gz` | Static Linux ARM64 CLI and notices; cross-built and inspected. |
| `open-agentic-catalog-go-<version>.tar.gz` | Go SDK source module. Extract it and use a local module replacement. |
| `open_agentic_catalog-<python-version>-py3-none-any.whl` | Install with `python -m pip install /path/to/file.whl`. |
| `open_agentic_catalog-<python-version>.tar.gz` | Python source distribution. RC versions use PEP 440, for example `0.2.0rc1`. |
| `open-agentic-catalog-client-<version>.tgz` | Install with `npm install /path/to/file.tgz --ignore-scripts --no-audit --no-fund`. |
| `open-agentic-catalog-<version>.crate` | Extract it and set the Rust dependency's `path` to that directory. |
| `open-agentic-catalog-<version>.vsix` | Use **Extensions: Install from VSIX** in VS Code. |
| `open-agentic-catalog-<chart-version>.tgz` | Application Helm chart. Its version is independent of the application version. |
| `open-agentic-catalog-monitoring-<chart-version>.tgz` | Optional monitoring chart, with its own version. |
| `release.json`, `release-evidence.tar.gz`, `SHA256SUMS` | Source identity, bounded test/package evidence and public download checksums. |

Download all listed assets into one directory. Run `sha256sum -c SHA256SUMS` there before installation. Each entry uses the download's basename. Internal image archives stay in the Actions handoff and are excluded from this public checksum list. `release.json` retains their hashes separately. Checksums detect changed bytes; they are not signatures.

Extract the CLI archive for your architecture. Run `./catalogctl version` and `./catalogctl --help` from the extracted directory. Keep its notices with the binary. The release checks execute these commands on AMD64 and inspect ARM64 without executing it.

Use the [Go](../../../../../sdk/go/README.md#install-the-sdk), [Python](../../../../../sdk/python/README.md#install-the-sdk), [TypeScript](../../../../../sdk/typescript/README.md#install-the-sdk) and [Rust](../../../../../sdk/rust/README.md#install-from-local-source) instructions for SDK setup. Each SDK still supports source installation. Consumers can download runtime dependencies during installation.

After container delivery, pull explicit versions:

```sh
docker pull ghcr.io/open-agentic-catalog/open-agentic-catalog:0.1.0
docker pull ghcr.io/open-agentic-catalog/catalog-ui:0.1.0
```

Both indexes contain `linux/amd64` and `linux/arm64` members. No floating `latest` tag is created. Helm users set `image.repository`, `image.tag`, `ui.image.repository` and `ui.image.tag` through existing chart values. Set `ui.enabled=true` when required. Use the chart's digest values when selecting immutable image digests. [Deployment instructions](../operations/deployment.mdx) own configuration, Secrets, PostgreSQL and migration prerequisites. A downloaded chart alone does not configure those dependencies.

### Configure and approve delivery

The maintainer configures these GitHub settings. Agents never configure them or perform publication during local verification.

1. Create the `release` environment with required reviewers and release-tag restrictions.
2. Enable `RELEASE_PUBLISH_ENABLED=true` only after approval protection is configured.
3. Prepare the reviewed commit and signed `v<VERSION>` tag, then push the tag yourself.
4. Create a matching draft GitHub Release with release notes before approving delivery.
5. Review all required workflow results, then approve the protected delivery job.
6. Verify the completed draft's assets and GHCR images before publishing the draft yourself.

Open **Settings > Environments** to configure `release`. Add required reviewers and allow only the `v*` tag pattern. For a sole maintainer, leave **Prevent self-review** unchecked so you can approve your own run. Add `RELEASE_PUBLISH_ENABLED` under **Settings > Secrets and variables > Actions > Variables**, as a repository variable. The policy job does not use variables from the `release` environment.

The `environment: release` declaration does not create required-reviewer protection. Only the delivery job requests `contents: write` and `packages: write`. It authenticates with the built-in `GITHUB_TOKEN`; no ecosystem registry credentials are needed. Existing GHCR packages must allow this repository's workflow to write them. See [GHCR permissions and visibility](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry).

New GHCR packages are private by default. After the first successful delivery, set both packages to **Public** in their package settings. Verify anonymous pulls before publishing the draft release. A successful image push does not establish public visibility.

Delivery checks the remote tag's commit, existing release assets and versioned image tags before its first write. A differing asset or image stops delivery. Identical files are skipped; missing files are uploaded without deleting existing assets. Delivery never creates a release or edits release notes. A published release is accepted only when all expected files and images already match; it receives no writes.

GHCR images can be public before the draft release is published. Registry and release uploads cannot commit atomically. The job records completed operations and partial failures. Rerun the failed delivery job with its original seven-day artifact handoff. Do not rerun successful build jobs after partial delivery. If those artifacts expire, stop for maintainer review or choose a new version. Never rebuild the same version silently or delete partial uploads automatically.

After publishing, verify public asset checksums and anonymous pulls of both versioned images. Check both platform entries in each index. Actual hosted events, approval protection, token permissions and public visibility require a maintainer-run check. Local tests use intercepted publisher calls and never upload files or images.

## Prerequisites and identities

The source lint gate `make check` includes [API validation](testing.md#api-validation), [Python linting](testing.md#python-linting) and workflow validation. Run `make validation-tools-install` first with Node.js 22.13+ within major 22, npm 10+ and the repository Go toolchain. Install actionlint with `make workflow-tools-install` and shared Python tools with `make dev-tools-install` in separate invocations. These development tools are not needed to compile or run the Catalog image and are not included in release artifacts.

The optional [pre-commit checks](testing.md#optional-pre-commit-checks) additionally use a separately installed Gitleaks 8.30.1 binary. Builds do not install or enable hooks.

Use GNU Make, installed Python 3.12-3.14 and the repository's Go toolchain. Container builds require a Docker or Podman engine with BuildKit-compatible build arguments and cache mounts. `CONTAINER_ENGINE` selects the engine; `GO`, `PYTHON`, `TRIVY` and `HELM` select tools through Make command-line assignments, for example `make helm-package HELM=/path/to/helm`. Helm is required for chart packaging, not image builds.

The exact Trivy version in `test/packaging/trivy.json` (currently 0.76.1) is required for SBOM and image scanning. The Containerfile pins the native Go builder and Dockerfile frontend by digest and `go-licenses` by version. Initial acquisition needs access to image/module registries; later license collection and compilation run without network access. Cross-compilation itself requires neither QEMU nor target hardware.

| Input | Meaning |
| --- | --- |
| `VERSION`, `COMMIT` | Optional bounded build overrides. Otherwise Make derives development/tagged identity from root `VERSION` and the actual checkout; overrides report unknown source state. |
| `sourceState` | Derived clean, modified or unknown provenance, embedded in all three binaries and the `io.open-agentic-catalog.source-state` image label. |
| `IMAGE` | Local engine tag or, for inspection, an existing local image ID. Building requires a tag. |
| `CONTAINER_PLATFORM` | `linux/amd64` or `linux/arm64`; defaults to the host Go architecture. |
| `CHART_VERSION` | Chart version in [Semantic Versioning (SemVer)](https://semver.org/spec/v2.0.0.html) format, default `0.1.0`, independent of the binary version. |
| `APP_VERSION` | Chart application version, default `$(VERSION)`; does not rebuild an image or change upstream monitoring image pins. |
| `ARTIFACT_OUTPUT` | License/SBOM/chart output directory, default ignored `output/artifacts`. |

Local rebuilds and dirty worktrees are allowed. A commit label alone does not prove the contents came from a clean checkout. Keep that distinction in local test records; a pipeline can enforce its own source policy. Retagging preserves the image and embedded version/commit. Changing either embedded value creates a new build requiring the applicable tests. A local image ID is not a registry manifest digest or a multi-platform index digest.

Inspection accepts version/commit values of 1-128 ASCII letters, digits, dots, underscores, hyphens or plus signs, starting with a letter or digit. Chart application versions use the same character/length bound. Chart versions must additionally follow SemVer. These are non-secret build identifiers, not branch paths or arbitrary command strings.

## Shared development tools

Root `pyproject.toml` is dependency-only: its dev group pins Ruff 0.17.3, jsonschema 4.26.0, bump-my-version 1.6.7, coverage 7.17.0 and yamllint 1.38.0; `uv.lock` locks their transitive dependencies, including yamllint's PyYAML/pathspec closure and supported wheels. Coverage instruments the optional native tooling selection. It is not a Python application package. The installer requires [uv](https://github.com/astral-sh/uv) 0.13.6 and an installed Python 3.12-3.14 interpreter. Python 3.12.3 is the qualified local baseline. Install uv explicitly if the existing executable is older; do not upgrade a personal tool environment implicitly:

```sh
mkdir -p output
if uv_bootstrap_dir=$(mktemp -d "$PWD/output/uv-bootstrap.XXXXXXXX"); then
  python3 -m venv "$uv_bootstrap_dir" &&
  "$uv_bootstrap_dir/bin/python" -I -m pip --isolated install \
    --disable-pip-version-check --require-hashes --only-binary=:all: \
    -r test/ci/uv/requirements.txt &&
  make dev-tools-install UV="$uv_bootstrap_dir/bin/uv"
else
  false
fi
```

With the required uv already on `PATH`, run `make dev-tools-install` directly.

`DEV_PYTHON=/path/to/python3.12` selects the installed interpreter. Run installation alone. It synchronizes only the locked dev group into `.tools/dev`. It ignores workflow env files, ambient uv configuration and inherited settings that redirect uv, Git or virtual environments.

It does not download Python, build source distributions, rewrite a stale lock, change personal `.venv` environments or install npm/browser/scanner/SDK tools. Repeat installation is safe. Installation fails if the environment is incomplete or redirected. Inspect the environment. Do not replace retained files blindly. `make clean` removes `.tools/dev`, not the explicit bootstrap under output or personal environments.

The bootstrap uses a new directory because pip can skip hash verification for an already-installed exact version. Its requirements file covers only uv itself; ordinary Python development dependencies remain in `pyproject.toml` and `uv.lock`. Select an installed Python 3.12-3.14 executable in place of `python3` when necessary. The recorded wheels cover Intel/ARM Macs and compatible Linux x64/ARM64 hosts. Cross-platform wheel availability does not establish native execution qualification.

Normal Make targets prefer `.tools/dev/bin/python`, `.tools/dev/bin/ruff` and `.tools/dev/bin/yamllint` when available; `PYTHON`, `RUFF`, `YAMLLINT` and `BUMPVERSION` command-line overrides remain explicit. Without the shared environment, stdlib helpers use system Python and Ruff/yamllint use PATH; targets that require missing or wrong packages fail. Python-tool checks and version commands use installed dependencies; they do not use auto-syncing `uv run`.

The packaging and interoperability Go functional wrappers also prefer the installed `.tools/dev/bin/python` without activation. They retain the virtual-environment executable path and fall back to `python3` from PATH when it is absent. `PYTHON` overrides select direct Make recipes; each Go wrapper owns its interpreter selection.

Maintain pins deliberately with uv 0.13.6. Update `pyproject.toml`. Regenerate `uv.lock` with `uv lock --python /path/to/python --no-python-downloads`. Rerun locked installation, pin-parity, conformance and tooling tests. Review licenses and advisories. Keep `ruff.toml` as the rule authority.

## Application version and release checks

Root `VERSION` supplies the application release version, currently `0.1.0`. All three binaries, OpenAPI `info.version`, SDK packages and the VS Code package share this release base. UI/docs npm toolchain versions, the OpenAPI format version, standards profiles and chart package versions remain independent. The `/api/v1` route prefix identifies the API family and does not change with the application release version.

```sh
make version
make version-check
make version-bump-dry-run PART=patch
make version-bump PART=patch
```

Bumps require a clean actual Git checkout and the pinned installed bump tool. The bump updates `VERSION`, derived Python/npm/Cargo SDK metadata and extension release metadata. No command commits, creates tags or pushes.

The derived metadata includes these files:

| Consumer | Files changed by a version bump |
| --- | --- |
| Control API | `api/openapi/control-v1.yaml`, changing only `info.version`. |
| Python SDK and example | `sdk/python/pyproject.toml` and the SDK entries in `test/sdk/python/uv.lock` and `examples/langgraph/uv.lock`. |
| TypeScript SDK | `sdk/typescript/package.json` and its lockfile. |
| Rust SDK | `sdk/rust/Cargo.toml` and its SDK package entry in `sdk/rust/Cargo.lock`. |
| Rust consumers | SDK dependency entries in the manifest/lock under `test/sdk/rust/consumer/`, `examples/rust-catalog-report/Cargo.toml` and `examples/rust-catalog-report/Cargo.lock`. |
| VS Code extension | `extensions/vscode/package.json` and `extensions/vscode/package-lock.json`, including local SDK archive references. |

Third-party dependency versions stay unchanged. `make version-check` rejects an OpenAPI `info.version` that differs from root `VERSION`. The bump command updates the canonical two-space `version` field under `info`; other version fields remain unchanged.

Python maps an RC to PEP 440 (`0.2.0-rc.1` becomes `0.2.0rc1`); npm and Cargo keep SemVer. `version-check` rejects mismatched metadata. The Rust report application retains its own independent package version.

Reinstall SDK tools after lock changes. For Rust, run `make sdk-tools-install SDK_LANGUAGE=rust` to refresh the installed fingerprint, which includes the sample manifest and lock. If you use the optional LangGraph example, rerun `make langgraph-tools-install` too. Then run `make sdk-generate` and `make sdk-check` before committing the bump. The Rust selection, `make sdk-check SDK_LANGUAGE=rust`, also checks the report sample.

If you use the extension, package the updated TypeScript SDK, [refresh its local SDK lock](#refresh-the-local-sdk-lock), reinstall extension tools and repeat its checks/package qualification.

SDK packages are prepared locally and distributed as GitHub Release downloads after maintainer approval. No npm, PyPI or crates.io publication or Go submodule tag is created. Package names do not establish registry ownership. SDK packages and OpenAPI `info.version` follow root `VERSION`; `/api/v1` remains the API route family.

| `PART` | Change |
| --- | --- |
| `major`, `minor`, `patch` | Change the numeric version and reset any release candidate (RC) suffix. |
| `rc` | Start `rc.1` at the current numeric base or increment an existing `rc.N`. |
| `final` | Remove an existing RC suffix. |

For example, after a minor bump, use `rc` for `0.2.0-rc.1`. Use `rc` again for `0.2.0-rc.2`, then `final` for `0.2.0`. Review and commit each intended edit before another bump. Run each `version-bump`, `version-bump-dry-run` or `release-check` command in a separate Make invocation. These targets ignore workflow env files.

Make captures source identity once when needed: full Git SHA and clean/modified/unknown state. A clean checkout with the exact `v<VERSION>` tag at `HEAD` yields that release or prerelease version. Other builds use development versions, such as `0.1.0-dev+g0123456789ab` or `0.1.0-rc.1.dev+g0123456789ab`.

Staged, unstaged and non-ignored new files mark the tree modified; ignored output does not. A Git-less archive uses VERSION with a dev suffix and unknown provenance. Failed Git inspection fails the build rather than pretending the checkout is clean. Inherited Git redirects and unrelated parent repositories are ignored. Do not change source while building; a commit identifies the base tree, not dirty bytes.

Explicit `VERSION`/`COMMIT` fixture overrides use the existing 1-128 character identifier bound. With either override, `sourceState` becomes `unknown`. Existing env files that set `VERSION=dev` or `COMMIT=unknown` retain that override. Remove those assignments to use automatic identity.

Bare `go build` and raw Containerfile builds default to `dev` and `unknown` unless you supply build metadata. They do not obtain Catalog version or commit metadata from Git. Runtime code does not query Git.

Build identity is distinct from startup time, deployment time, image digest and signed provenance. There is no varying build timestamp. SemVer build metadata does not define upgrade ordering, and its plus sign must not be copied into OCI image tags: `IMAGE`/`UI_IMAGE` remain separate selections.

After separately authorized review, commit and signed `v<VERSION>` tagging, `make release-check` verifies clean actual source and the exact tag. It rejects metadata overrides and never publishes. Advance VERSION explicitly for subsequent development. No tag is created as part of implementation or tests; fixtures use disposable repositories. Inspect a retained artifact using exactly its supplied metadata, not newly computed identity from a later source tree.

`catalogd version` prints version, commit and bracketed source state; `catalogctl version` and `catalog-ui version` print the three JSON fields. Each also accepts `--version`.

API `/version` adds `sourceState` to its existing version/profile fields. The worker's private version/status reads carry the same state. The UI's same-origin `/_build` read, under its configured base path, reports only its own embedded fields, with no API/provider access or session data.

Service information shows separate Catalog and UI host builds. Persistent startup/version history remains unavailable; embedded build information adds no database history.

## Build local binaries

Use automatic source identity for ordinary builds:

```sh
make build
make binary-check
```

For an explicit fixture build, supply bounded metadata overrides:

```sh
make build VERSION=0.1.0 COMMIT=unknown
make binary-check VERSION=0.1.0 COMMIT=unknown
```

`build` writes `bin/catalogd` and `bin/catalogctl` using Go's selected OS and architecture, disables cgo, trims source paths and strips symbols and debug information. Automatic metadata needs Python 3.12+ and Git for a checkout. It does not need installed Python packages, uv, a container engine, Helm or Node. `CONTAINER_PLATFORM` selects container builds only; it does not change these local binaries.

`binary-check` rebuilds both binaries and checks pure-Go build metadata, embedded fallback TLS roots, an empty linker build ID and the Go tool's explicit no-symbols result. Inspection errors fail the target. For Linux ELF architecture, dynamic-loading and DWARF section checks, use `container-inspect` on the built image as described below. `make verify` includes the local binary check; neither target builds a container image.

### Clean local builds and dependencies

The explicit cleanup inventory includes `.tools/ci/`, which contains workflow-only prerequisites. It does not include workflow source files, retained application data or arbitrary report directories.

The npm inventory also includes `test/interoperability/mcp/node_modules/` and `examples/typescript-ard-list/node_modules/`. Cleanup removes the `publication-report` and `mcp-scan-receiver` executables under `bin/examples/`. Other executables in that directory remain.

Normal cleanup also removes the root `httpserver.test` binary. It removes `.tools/`, `bin/examples/`, `bin/` and `documentation/public/` only when planned cleanup leaves them empty. Custom tools and maintained public assets keep their parent directories. The dry run includes these empty-parent removals. Parent removal uses `rmdir`, so a newly created file stops removal rather than being deleted.

Normal cleanup does not empty `output/`. It preserves review reports, test evidence and runtime inputs outside the listed artifact and cache paths.

Cleanup requires Linux, Python 3, Git and a readable `/proc/self/mountinfo` inventory. Both targets refuse an unavailable or malformed mount inventory before deletion. They need no container engine or installed npm packages.

Preview the exact existing paths before removing them:

```sh
make clean-dry-run
make clean
```

`make clean` removes the API, CLI and UI binaries; npm dependencies in the root, UI, documentation, API-validation, TypeScript SDK and VS Code projects; UI/docs build output and generated documentation; and generated Go-client output. It also removes local API-validation/client-generator/SDK/formal/generated-API/benchmark tools, the owned `.tools/dev/` and `.tools/ci/` installations, and default packages under `output/artifacts/`. VS Code cleanup includes `extensions/vscode/.tools/`, `extensions/vscode/dist/`, generated notices, `test/vscode/.tools/` and `output/vscode/staging/`; see the [extension workflow](#optional-vs-code-package) for its narrower cleanup scope. Rust cleanup includes `sdk/rust/target/`, `test/sdk/rust/consumer/target/` and `examples/rust-catalog-report/target/`.

Within `coverage/`, cleanup removes the standard files `default.out`, `unit.out`, `core.out`, `postgres.out`, `process-postgres.out`, `full.out`, `full.txt` and `index.html`. It also removes `publication-report-unit.out` and `mcp-scanner-receiver-unit.out`. Cleanup also removes `coverage/.staging/`, root `.ruff_cache/`, `.mypy_cache/` and `.hypothesis/`, and Python bytecode beside tracked Python sources. Coverage recipes place temporary files under `.staging/`; normal exit and handled signals remove them, while a forcibly killed run can leave scratch data for later cleanup. Cleanup ignores repeated HUP, INT and TERM while it removes owned scratch.

Old scratch paths outside `.staging/` remain for separate inspection; cleanup does not sweep filename prefixes. The inventory is explicit in `test/packaging/clean.py`; an ignored path is not automatically disposable.

Cleanup removes the locked `examples/langgraph/.venv/` environment and checkout-local `.tools/gitleaks/`, `.tools/agntcy/`, `.tools/gateway/` and `.tools/ans/` installations. The ANS installation includes its private read-only Go module cache.

The inventory also removes the known investigation-local caches `output/n88/go-build-cache/`, `output/n88/trivy-cache/` and `output/deadcode-research/n16-rebuild/go-cache/`. Sibling reports are preserved. Subsequent builds and scans recreate or download their cache contents; cleanup does not discover arbitrary directories by a cache-like name.

Cleanup preserves source, lockfiles, `.env` files, personal virtual environments and `go.work`. The specific `examples/langgraph/.venv/` environment is disposable; personal `.venv/` directories elsewhere are preserved. Cleanup also preserves custom/historical coverage profiles, `coverage/performance/`, retained measurements, browser evidence, backups, model downloads and other retained `output/` contents. This includes demo loading receipts and archive/runtime inputs, tutorial credentials, UI-host inputs, monitoring credentials and SonarQube credentials. Custom artifact, cache or Cargo target destinations outside the explicit inventory are preserved; a custom path beneath `output/artifacts/` is still removed. Neither command stops processes or removes containers, images, volumes, local object data or shared Go/npm/browser caches.

Files removed by `clean` are not moved to trash; rebuild or reinstall disposable outputs as needed.

Stop any local builds, tests and UI/docs development or preview servers before cleaning, because their files and dependencies will be removed. A checkout lock rejects cleanup while a participating VS Code build, watch, install or test runs. Other producers do not acquire that lock; stop them yourself before cleanup. Cleanup targets ignore Make env files and reject other goals in the same invocation, including `make -j clean build`.

Both targets inspect the complete deletion inventory once under the checkout lock, before removal or permission changes. They refuse tracked content, symlinked targets/ancestors, nested repositories, unexpected file types and unreadable subtrees. Mount checks include selected files/directories, ancestors below the checkout root and descendant files/directories, including same-filesystem bind mounts. Child symlinks, such as LangGraph's normal `lib64` link, are removed without following their destinations. Read-only cache permissions change only inside an inspected disposable target during cleanup; dry-run leaves contents and modes unchanged.

Do not modify those paths concurrently. A filesystem failure during deletion can leave a partially cleaned checkout, and the command reports failure. Repeating cleanup with unchanged, unmounted inputs is safe.

Reinstall only the tools you need: `make dev-tools-install`, `make ui-install`, `make docs-install`, `make validation-tools-install`, `make client-go-tools-install`, `make sdk-tools-install` (all SDKs by default) or `make formal-tools-install`. Restore benchmark comparison tooling with `make benchstat-tools-install`; see [benchmark reports](../../../../../test/performance/README.md#matched-go-benchmark-comparison). Restore optional tools with `make langgraph-tools-install`, `make agntcy-tools-install`, `make gateway-tools-install` or `make ans-tools-install`. Select the documented uv executable for LangGraph installation. Reinstall a checkout-local Gitleaks binary with the [pinned scanner procedure](testing.md#optional-pre-commit-checks); there is no Gitleaks installer target. Tools selected outside the checkout remain in place.

For VS Code, rebuild the TypeScript SDK archive before `make -C extensions/vscode tools-install`; follow the [extension prerequisites and package steps](#optional-vs-code-package). Add `make -C extensions/vscode test-tools-install` only when you need editor qualification; root `make vscode-tools-install` installs that same build/test tool set. Then rebuild with the corresponding build targets. SDK tool cleanup preserves checked-in generated SDK source and locks. Use `make perf-clean PERF_RUN_ID=<id>` for a validated abandoned performance run or the separately documented deployment cleanup commands when that is the intended task.

#### Clean generated output separately

`make clean` does not select the report directories below. Use the separate output targets when you want to discard generated reports and test work.

Stop relevant builds, tests and other writers first. Preview the exact removal plan:

```sh
make clean-output-dry-run
```

Review the `Would remove` and `Preserving` lines. Then run the removal target:

```sh
make clean-output
```

Run each target alone. Both ignore Make env files and need the same Linux, Python and Git prerequisites as normal cleanup.

| Disposable output | Locations |
| --- | --- |
| Reports and test work | `output/review/`, `output/sdk/`, `output/mutation/`, `output/ui-mutation/`, `output/property/`, `output/playwright/`, `output/coverage/`, `output/verification/`, `output/ui-dependencies/` |
| Packages and staging | `output/artifacts/`, `output/vscode/staging/` |
| Existing cache targets | `output/n88/go-build-cache/`, `output/n88/trivy-cache/`, `output/deadcode-research/n16-rebuild/go-cache/` |
| Root log files | Regular files that match `output/*.log` |

The command deletes selected reports regardless of age. Copy reports that you need outside these locations before removal. The command does not empty `output/`.

| Preserved inputs | Locations |
| --- | --- |
| Development runtime inputs | `output/demo/`, `output/ui-compose/`, `output/monitoring/`, `output/sonarqube/` |
| Backups and import receipts | `output/backup/`, `output/backups/`, `output/native-import/` |
| Model and tool state | `output/interoperability/`, `output/langgraph-model/`, `output/search-relevance/` |
| Performance state | `output/performance/` |
| Other output | Unknown locations and non-log root files, except the explicit paths in the removal table |

A `.git` file, directory or link preserves its complete containing directory. The command lists that directory as a nested Git repository. It removes eligible siblings and leaves the repository's parents in place. Normal cleanup still refuses nested repositories.

Selected target or ancestor symlinks cause refusal. Child links within disposable report trees are removed without following their destinations. Tracked content, kernel mounts, unreadable subtrees and unexpected file types stop preflight before deletion. Preview leaves contents and permissions unchanged.

These commands do not stop containers or inspect Docker. Host mount checks cannot detect bind sources in another mount namespace. The protected runtime directories keep the normal Compose inputs outside removal. Keep custom credentials, models, backups and other persistent inputs outside the disposable report directories.

## Build both architectures

Run from the repository root, substituting your version and source identity:

```sh
make container-build IMAGE=open-agentic-catalog:local-amd64 CONTAINER_PLATFORM=linux/amd64 VERSION=0.1.0 COMMIT=unknown
make container-build IMAGE=open-agentic-catalog:local-arm64 CONTAINER_PLATFORM=linux/arm64 VERSION=0.1.0 COMMIT=unknown
```

Each command creates one image in the selected engine's local image store. The compiler runs on the build machine and produces static, stripped binaries with portable CPU defaults. The scratch runtime contains the executables and `/licenses/`, not a shell, compiler, credentials or deployment configuration. Both architectures use the maintained embedded public TLS roots.

Separate tags work with ordinary single-platform image stores. No combined local multi-platform image or registry server is needed. A future pipeline can use standard Buildx multi-platform publishing, or publish each variant and assemble a registry index. That publishing setup, authentication and registry selection are outside these Make targets. Ad hoc image transfer can use standard `docker save` or an appropriate image-copy tool independently.

## Inspect and test

```sh
make container-inspect IMAGE=open-agentic-catalog:local-arm64 CONTAINER_PLATFORM=linux/arm64 VERSION=0.1.0 COMMIT=unknown
make container-verify IMAGE=open-agentic-catalog:local-amd64 CONTAINER_PLATFORM=linux/amd64 VERSION=0.1.0 COMMIT=unknown
```

`container-inspect` never builds or executes the selected image. It resolves the local image ID, validates platform, labels, non-root settings, commands and exposed ports, then copies both binaries from a temporary stopped container. It checks ELF architecture, absence of dynamic loading and debug/symbol tables, Go build settings, fallback-root dependency and license-file readability. Its JSON output records the image ID, platform and binary hashes/build information. Trimpath builds omit linker flags from Go metadata: cross-architecture inspection checks version/commit labels, while execution verifies the binaries' version responses.

`container-verify` builds and inspects the image. It also executes both version commands without network access, a writable root filesystem or capabilities. Its version commands require an engine able to execute the selected architecture. For cross-built ARM64 images on AMD64, use `container-inspect`; native ARM hardware is not required by the build workflow.

Run the [testing gates](testing.md) appropriate to the change. These include `make check`, `make verify`, container/Compose/chart checks and applicable disposable functional and Minikube tests. Packaging does not introduce a second release-only functional suite. Follow [deployment prerequisites](../operations/deployment.mdx) for the current tested compatibility boundary.

## Build and run the UI locally

Install [Node.js](https://nodejs.org/en/download) 22.23.4 and select npm 12.2.2 separately; that npm version is not bundled with this Node distribution. The `catalog-ui/.nvmrc` file selects the Node version for nvm. The UI build and test tools require Node 22.18 or later within major 22. Install the repository Go toolchain and Make prerequisites from [local development](../guides/local-development.mdx#start-the-service).

Run the following commands from the repository root in separate terminals:

```sh
make ui-install
make run
```

```sh
make ui-dev
```

Open `http://127.0.0.1:4180`. Choose a fixed local identity. The backend still enforces that identity's actual membership and permissions. The development launcher creates temporary session keys.

Sign in again after restarting it. It does not seed or reset Catalog data. To add sample resources to your own local instance, follow [the populated development catalog procedure](../guides/local-development.mdx#populated-development-catalog).

For an owned development backend already running on port 28183, stop the existing UI launcher and run:

```sh
make ui-build
node catalog-ui/scripts/dev.mjs --api-url http://127.0.0.1:28183 --port 14180
```

Open `http://127.0.0.1:14180`. The launcher accepts literal-loopback HTTP API origins only. Use the configured hostname and port exactly, not `localhost` as a substitute for `127.0.0.1`.

## Optional UI image

Build the optional client separately, then inspect the same selected image:

```sh
make ui-container-build UI_IMAGE=open-agentic-catalog-ui:local-amd64 CONTAINER_PLATFORM=linux/amd64 VERSION=0.1.0 COMMIT=unknown
make ui-container-inspect UI_IMAGE=open-agentic-catalog-ui:local-amd64 CONTAINER_PLATFORM=linux/amd64 VERSION=0.1.0 COMMIT=unknown
make ui-sbom UI_IMAGE=open-agentic-catalog-ui:local-amd64 CONTAINER_PLATFORM=linux/amd64 VERSION=0.1.0 COMMIT=unknown
```

Use `linux/arm64` and a distinct local tag for the ARM64 build. Inspection does not execute foreign binaries. UI image inspection checks the static Go host, non-root entrypoint, readable browser assets and included Go/frontend notices. It does not require the backend binaries or embedded standards bundle.

The UI image also includes the static manual at `/app/assets/docs/`. A separate stage installs the locked documentation packages. Rendering runs without network access and uses `DOCS_BASE=/docs/`. The runtime contains no Node, npm, Astro server or native build tools. Backend images remain independent of this build.

Every UI image build renders the manual from its current repository inputs. These include the four SDK source READMEs, published under **Build integrations > SDKs**. Docker can reuse an unchanged documentation layer; a changed README, page, content map or documentation build input invalidates it. The stage copies SDK guides only, without SDK generators or runtime dependencies. It does not copy an existing host `documentation/dist/`.

Rebuild and replace the retained Compose services with `make compose-up LOCAL_UI=on`. Repeat the optional overlay selections used by that project. A standalone `make docs-build` updates local preview output; it does not replace the manual in a running UI container.

The Go host serves the manual publicly at `/docs/`, including deployments with another UI base path. Reserve `/docs` and its descendants for this route. The manual contains public reader pages and explicit downloads. It does not read browser sessions or forward requests to the Catalog API. Help selects the bundled route unless an HTTPS `documentationUrl` overrides navigation. The chart routes `/docs` to the UI Service when the UI uses a subpath. Reverse proxies must preserve both prefixes. See [UI documentation settings](../reference/configuration.mdx#ui-documentation) for source and deployment configuration.

Inspection requires a complete manual. It verifies every file against `documentation-bundle.json` from the selected image and checks the matching notices under `/licenses/documentation/`. React and manual inventories are separate. A source fixture can omit the manual, but a malformed bundle fails host startup.

The manual uses its own Content Security Policy. Final HTML supplies exact inline script and style hashes. The policy allows same-origin search workers, WebAssembly and documentation style attributes. The application and authentication policies remain unchanged. Stable files require cache revalidation; only hashed Astro assets receive immutable caching.

`/licenses/frontend/inventory.json` retains production npm package versions, declared licenses and notice paths together with the lockfile and built-asset hashes. Inspection validates the extracted assets, and the SBOM combines this image-owned evidence with Trivy's Go inventory. This inventory includes all production dependencies recorded in the lockfile. It does not identify exactly which modules remain after tree shaking.

Build/test dependencies, Node and the OIDC test provider are not shipped-runtime packages. An older image is never compared with the current checkout's lockfile. The targets resolve the local image ID without building or publishing and use the same `ARTIFACT_OUTPUT` and no-overwrite rules as the backend targets.

The manual adds reviewed browser asset suppliers to the SBOM and UI scan. Its complete npm inventory remains separate build evidence. The notices retain all available license texts, including those for build-only components; their presence does not mean those tools ship as executable code. Supplier evidence is conservative and does not claim an exact module inventory after bundling.

## Licenses and SBOMs

Each distributed component carries its own project license and applicable third-party notices. Use the component's packaged contents to determine which notices it needs. A dependency declaration alone does not mean that its source ships in the package.

| Component | Notice contents |
| --- | --- |
| Backend image | Go runtime and linked dependencies, embedded standards and embedded samples under `/licenses/`. |
| UI image | Go host notices, frontend notices under `/licenses/frontend/`, and manual notices under `/licenses/documentation/`. Browser notice pages accompany the static assets. |
| VSIX | The extension license, NOTICE and notices for SDK code and libraries bundled into the extension. |
| Each SDK | Its own LICENSE and NOTICE. Python, npm and Rust archives retain these files. Go remains a source module with these files. |

Preserve notices beside third-party source files that the repository includes. Examples include pinned standards, sample Skills and documentation icons. External test peers and resource runtimes listed only as metadata do not belong in these component notice bundles. Shared collectors can support several builds while producing separate outputs.

The VSIX collector retains Undici's root MIT license and its nested fetch MIT and Milo ISC notices. Each nested notice identifies its source path. Missing or empty required notice files stop packaging before the previous complete artifact and manifest are replaced.

Backend license collection runs during every image build. The pinned `go-licenses save` tool examines the target architecture's imports for both commands. `/licenses/` includes the project Apache-2.0 license, Go runtime license and patents notice, linked dependency notices, and embedded upstream notices. `/licenses/samples/` preserves sample licenses, Plugin notices and the artwork dedication. Upstream paths and `schemas/checksums.txt` preserve the pinned standards' identity. Inspection checks every upstream notice against that manifest and requires each current sample notice. The collector does not enter the runtime image.

The manual collector includes nested license files for Pagefind. Its default UI package omits a license file, so the collector includes the license from the pinned upstream revision. Generation requires notice text for every reviewed browser supplier and includes the navigation icons' attribution. SDK packaging checks each archive's LICENSE and NOTICE against that SDK's source files.

```sh
make licenses IMAGE=open-agentic-catalog:local-amd64 CONTAINER_PLATFORM=linux/amd64 VERSION=0.1.0 COMMIT=unknown
make sbom IMAGE=open-agentic-catalog:local-amd64 CONTAINER_PLATFORM=linux/amd64 VERSION=0.1.0 COMMIT=unknown
make sbom IMAGE=open-agentic-catalog:local-arm64 CONTAINER_PLATFORM=linux/arm64 VERSION=0.1.0 COMMIT=unknown
```

`licenses` extracts the exact files already in the image; it does not generate a second inventory. Output is `licenses-linux-<architecture>-<image-ID-prefix>/`. An SBOM records components; it does not replace license and attribution texts. Inventories and hashes support verification, but the licenses do not require these particular formats. `sbom` uses Trivy's selected local Docker/Podman source against the resolved image ID, without rebuilding or falling back to registry pulls.

Output is `sbom-linux-<architecture>-<image-ID-prefix>.spdx.json`. Trivy identifies the Go binaries and dependencies; a small SPDX entry identifies the embedded standards bundle through the checksum manifest already shipped in `/licenses/upstream/`. It does not invent package license conclusions. An SBOM is an inventory, not license texts, a signature or a vulnerability-free claim; use the separate security checks for vulnerabilities. It does not establish provenance or authenticate an artifact.

The target checks each binary's linked module versions and compiler standard library against its own SPDX application relationships. Another binary's inventory cannot fill a missing dependency. The check uses extracted build information, not the current checkout's `go.mod`. It preserves native package URLs and adds the applicable image-owned standards or browser suppliers after validation. Missing modules, wrong versions or missing per-binary relationships stop the target before it publishes a file. No vulnerability database is required for this inventory check.

For Podman SBOMs and scans, Trivy needs access to the selected Podman service socket. Configure `CONTAINER_ENGINE=podman` and `TRIVY_PODMAN_HOST` for that socket; the target does not start or reconfigure a service. A missing local image/socket fails instead of pulling a similarly named registry image.

The license tool can warn about assembly in Go modules because it cannot analyze further non-Go imports. The current static build uses the modules' own assembly and includes their license texts; inspect such warnings when updating dependencies. Do not suppress a newly missing license. This workflow retains the existing permissive dependency policy and does not collect inventories for external services.

## Package Helm charts

```sh
make helm-package CHART_VERSION=0.1.0 APP_VERSION=0.1.0
make helm-package CHART_VERSION=0.1.1-rc.1+build.2 APP_VERSION=0.1.0
```

The target stages and validates `open-agentic-catalog-<CHART_VERSION>.tgz` and `open-agentic-catalog-monitoring-<CHART_VERSION>.tgz` without rewriting tracked files. Both archives include the project license for their service-owned templates and configuration. The independent monitoring companion includes its shared dashboards, configuration and image pins; the application chart does not depend on it. Charts do not include Go dependencies or inventories of externally referenced Prometheus/Grafana images. Install the companion only when needed; see [optional Helm monitoring](../operations/helm-monitoring.mdx).

Chart versions accept SemVer prereleases and `+` build metadata, without a leading `v`.

The Catalog's full `appVersion` is retained in chart metadata. Kubernetes version labels replace `+` with `_`, truncate to 63 characters and strip trailing punctuation. When `image.tag` is empty, the application chart replaces `+` with `_` in the default image tag too; build/tag the image accordingly.

An explicit `image.tag` is used unchanged, and `image.digest` takes precedence over both. A chart-only update can keep the old `APP_VERSION`; packaging never changes `deployments/monitoring/images.json`.

Both charts require Kubernetes 1.34+. The target lints and renders the actual staged archives with the explicit 1.34.0 Kubernetes baseline before making either output available, independently of Helm's built-in default Kubernetes version.

Use `make helm-package-test` for the packaging script's metadata, image selection and output-safety regressions in temporary directories, without retaining release archives. It honors `HELM` and requires Python and Helm, but no cluster. `make helm-check` includes those tests alongside chart lint and Go render contracts for configuration and shared assets.

The same gate also packages/renders the local deployment coordinator's target inputs. Actual Helm tests are separate from `make test` and `make verify`; installing Helm does not expand the default suite. Install/upgrade sequencing and database migrations remain in [deployment](../operations/deployment.mdx) and [maintenance](../operations/maintenance.mdx); recovery remains in [backup and restore](../operations/backup-restore.mdx).

## Failures and cleanup

Existing license/SBOM/chart outputs are not overwritten. Choose another `ARTIFACT_OUTPUT` or chart version to regenerate; inspect and remove only the exact generated files you no longer need. A missing or wrong-platform image, mismatched labels, failed tool or incomplete license/SBOM result fails the target. Temporary staging directories and stopped inspection containers are cleaned up on ordinary completion or failure. Images, shared caches, builders and caller directories are never pruned. A forcibly terminated build, inspection or packaging command may leave task-specific temporary resources that require manual cleanup.

If ARM64 verification reports an execution-format error on AMD64, use `container-inspect` for cross-build validation and run runtime tests on the appropriate hardware. If package generation fails, check the selected tool/version and output path. The workflow does not promise a fully offline initial build or bit-identical image rebuilds.

## Scan local images

Build the desired backend/UI image first, then pass the same version, commit and platform used by the build. These targets inspect and scan existing local artifacts without rebuilding, pulling target images or executing their binaries:

```sh
make container-scan IMAGE=open-agentic-catalog:local-amd64 CONTAINER_PLATFORM=linux/amd64 VERSION=0.1.0 COMMIT=unknown
make ui-container-scan UI_IMAGE=open-agentic-catalog-ui:local-amd64 CONTAINER_PLATFORM=linux/amd64 VERSION=0.1.0 COMMIT=unknown
```

Repeat with the matching `linux/arm64` images and build inputs for that architecture. `TRIVY=/absolute/path/to/trivy` selects the [qualified scanner](../reference/dependencies.md#coverage-reporters-and-trivy); `CONTAINER_ENGINE` must be one Docker or Podman executable, with connection settings supplied through its environment. Docker defaults and `DOCKER_HOST` remain available; Podman can use `TRIVY_PODMAN_HOST`. The inspected image ID, platform, labels, binary metadata, licenses and UI assets must match.

Go scan inventories must include the compiler/standard-library version, main module and linked dependencies from each inspected binary. The scanner uses only that engine's image source and immutable local image ID, and the selected tag is rechecked before success.

Local IDs identify image configurations, not registry manifest/index digests or signed provenance. ARM64 inspection and scanning are static; they do not qualify ARM64 execution. Docker was used for the recorded two-architecture qualification; another Podman/socket setup still needs its own local qualification.

Download the vulnerability database before scanning. Use the selected Trivy executable:

```sh
trivy image --download-db-only
```

`TRIVY_CACHE_DIR` selects a nondefault cache for both acquisition and the Make targets. A scan requires regular database and metadata files, schema version 2, and an unexpired `NextUpdate`; it fails on missing, stale, changing or unusable input.

Each run copies one database snapshot to temporary private storage, records its SHA-256 and timestamps, and removes the copy afterward. Allow space for that additional database copy. Local image scans disable database and check-bundle updates and online dependency identification. Fresh advisory acquisition can require network access. SBOM generation alone requires no vulnerability database.

Image scanning enables vulnerabilities and secrets, including native image-configuration secret scanning with `--image-config-scanners secret`. This covers scanner-recognized secrets in image metadata as well as its filesystem selection.

The UI target also scans the inspected image's supplemented SPDX npm inventory, including its package URLs. It includes frontend production dependencies and reviewed manual browser suppliers. Bundled JavaScript alone does not expose these package identities. The scan never substitutes the current checkout's lockfile. Conservative supplier evidence can include code that bundling removes; it is not an exact module inventory after bundling.

The image-config option does not apply to SPDX or development dependency scans. Existing Containerfile, Compose and image-setting checks own configuration policy; image scans do not claim a second complete misconfiguration audit.

Both images already use fresh `FROM scratch` runtime stages with explicit copies of binaries, licenses and, for the UI, built browser assets. Builder layers and caches are not inherited by those stages; files deliberately copied or embedded in artifacts still need review.

Do not put credentials in build arguments, environment variables or copied files. Removing them in a later layer does not make that inclusion safe. Runtime secrets remain deployment inputs; future private build acquisition should use BuildKit secret mounts. Layer squashing or a custom image-archive scanner is not part of this workflow.

Each scan writes a unique private `scan-*` directory under `ARTIFACT_OUTPUT` (default `output/artifacts/`). `run.json` retains image/build identity, platform, scanner version, database hash/timestamps, policy, last phase and outcome. `image.json` retains all vulnerability severities and secret findings; UI runs additionally retain `ui.spdx.json` and `frontend.json`.

Reports can contain sensitive findings; keep them private and out of commits or support bundles.

The scanner uses empty configuration and ignore files. It removes inherited Trivy policy settings to prevent hidden exclusions, remote scanning or suppression. Only cache and Podman socket Trivy environment settings are retained.

Maintained Trivy scans pass `--disable-telemetry` and `--skip-version-check` explicitly. Together these flags disable the optional request for [usage telemetry and update notices](https://github.com/aquasecurity/trivy/blob/e713f67f7d54decdcd9b3132c9751833dcced698/docs/guide/advanced/air-gap.md#check-updates-service). Use both flags for manual scans. Advisory database and check-bundle acquisition use separate settings.

Every High/Critical vulnerability or secret fails the scan. Reports retain lower severities. Native scan errors, timeouts, incomplete inventories, malformed reports and changed identity also fail.

The helper distinguishes passed (0), findings (1) and operational/integrity failure (2); Make reports any recipe failure as nonzero. Terminal output shows only outcome and evidence location, never raw secret matches or scanner stderr. Inspect `run.json`'s phase when diagnosing failures.

No current exception list is applied, and no clean result claims absence of unknown vulnerabilities, complete secret detection, legal approval or release acceptance.

The packaging qualification procedure in `test/packaging/README.md` includes an opt-in synthetic-secret control. It compares a clean image with a derived image that adds a synthetic secret only in ENV metadata, retaining identical filesystem layers. It proves the selected native detector and failure policy, not detection of every credential or secrets in previously deleted files. Trivy's built-in path/content exclusions and binary/size limits remain in effect. Previously deleted-layer detection has not been qualified by this control; build review and preventing secret inclusion remain necessary.

For release deployment, select the packaged application chart and target image in complete values, then use `make helm-deploy-check` and `make helm-deploy` with explicit context, namespace and release. The [maintenance guide](../operations/maintenance.mdx#coordinated-release-deployment) covers the drain/migration/readiness sequence, timeouts and forward recovery. Packaging itself remains non-deploying.

## Optional VS Code package

Develop the optional desktop client in `extensions/vscode/`. It uses the packaged TypeScript SDK for Catalog calls. Its editor host and webviews are separate from the React product UI. The [consumer guide](../../../../../extensions/vscode/README.md) covers installation, connections and resource use. This VS Code extension is distinct from the Agent Plugins that it can install.

### Set up the extension tools

Use Linux amd64, GNU Make and Python 3.12-3.14 for the maintained workflow. The build, check and package targets require a verified current TypeScript SDK archive and a matching extension lock. If the checkout already includes those local artifacts, SDK maintainer tools are not required. Otherwise prepare them explicitly from the repository root:

```sh
make sdk-tools-install SDK_LANGUAGE=typescript
make sdk-check SDK_LANGUAGE=typescript
make sdk-package SDK_LANGUAGE=typescript
```

Follow [Refresh the local SDK lock](#refresh-the-local-sdk-lock) if its archive integrity changed. Install only the extension build tools, then check and package:

```sh
make -C extensions/vscode tools-install
make -C extensions/vscode check
make -C extensions/vscode package
make -C extensions/vscode verify
```

The build installer acquires one pinned Node 24.22.1/npm 12.2.2 runtime and locked npm tools under `extensions/vscode/.tools/` and `node_modules/`. It leaves your default Node and SDK caches alone. Build/check/package need no VS Code download, ORAS, A2A sample, Go or SDK compatibility/browser installation. They validate the SDK source/archive/lock, installed tools and dependencies, acquire nothing and never refresh locks implicitly. See the [dependency policy](../reference/dependencies.md#optional-vs-code-client-and-sdk-runtime).

Full editor qualification also needs Git, Go 1.27.3, the [GitHub CLI](https://cli.github.com/), Xvfb, OpenSSL, OpenSSH client/server, uvx 0.13.6, local Docker and a C compiler for race tests. Install these prerequisites explicitly; the extension does not install resource runtimes. Configure `gh` for public fixture acquisition and install the SDK test tools above. Run `make -C extensions/vscode test-tools-install`, then `make -C extensions/vscode test`. The test installer explicitly adds the pinned desktop host, ORAS, A2A executor, Remote SSH extension, matching VS Code Server and Chrome for Testing; its stamp is separate from build tools. No database, Compose stack, cluster or model account is required. Root `vscode-tools-install` retains this aggregate installation behavior.

### Build, watch and debug

Open `extensions/vscode/` as a folder in VS Code. Run `make build` there for an unminified bundle and TypeScript source maps under `dist/`. Start an explicitly selected local editor with `make debug CATALOG_VSCODE_EXECUTABLE=/absolute/path/to/code`. It uses the same Python build script, opens the development extension and exposes its debugger on loopback port 9229. It uses separate user, extension and shared storage under `output/vscode/development/`; cleanup preserves this directory.

Press F5 with **Catalog: Attach to development host** in the source window. Set a breakpoint in `src/extension.ts`, then run **Catalog: Open Catalog** in the development window. This uses the standard Node debugger and the actual editor extension host. Close that development window when finished. The selected editor's default `extensionHost` launch path failed to expose its debug target during qualification; the documented explicit launch/attach path passes the real TypeScript breakpoint check without changing the editor pin.

Run `make watch` or the **Catalog: watch** task for rebuilds after source changes. Fix compiler errors before debugging. Reload the development window to load rebuilt code. Watch does not restart the editor. Stop watch with Ctrl+C before another build, check, package, test or cleanup. Press F5 to attach after the initial build.

Commands refuse conflicting writers across Make processes rather than mixing their outputs. If the source editor uses an HTTP proxy, exclude `127.0.0.1`, `localhost` and `::1` in its `http.noProxy` setting for the local debugger. Catalog and resource transports keep their separate explicit settings. The F5 configuration is for the selected desktop host; it does not qualify remote placement or another platform.

`package` builds a minified production bundle in a private staging directory. It never compiles the test library or copies development source maps into the VSIX. Development output stays separate. npm `build`, `watch`, `check` and `package` scripts call the same Python build script; use its pinned Node/npm installation. There is no recursive prepublish/package hook.

**Catalog: Help (Offline)** opens fixed sections of the same README in development and installed hosts. It requires no sign-in or trusted workspace. Setup, Catalog, My tools and Agent controls link to the relevant sections; no resource destination or private input chooses a Help URL.

`make -C extensions/vscode screenshots` separately captures normal illustrations through an installed VSIX, fresh profile and disposable Catalog. It uses benign records and a real local Hello World exchange. Review the images and source-bound report under `output/vscode/guide-screenshots/` before deliberately copying accepted PNGs to `extensions/vscode/media/`. Rebuild and qualify the package after copying. The [capture workflow](../../../../../test/vscode/README.md#normal-guide-screenshots) preserves hostile-content regressions separately; their screenshots are not guide assets. Capture neither publishes artifacts nor refreshes retained services.

### Refresh the local SDK lock

Use this after an intentional SDK source or release-metadata change. It updates the extension's local SDK archive reference and integrity; it does not change the intended release version. Install the pinned TypeScript SDK tools first, then check and package that SDK:

```sh
make sdk-tools-install SDK_LANGUAGE=typescript
make sdk-check SDK_LANGUAGE=typescript
make sdk-package SDK_LANGUAGE=typescript
```

From the repository root, run the installed pinned Node/npm pair. The package manifest selects the archive actually built:

```sh
python3 - <<'PYSDK'
import json
import os
from pathlib import Path
import subprocess

repo = Path.cwd()
stamp = json.loads((repo / "test/sdk/.tools/typescript/installed.json").read_text())
artifacts = repo / "output/artifacts/sdk/typescript"
manifest = json.loads((artifacts / "manifest.json").read_text())
archive_name = "open-agentic-catalog-client-" + manifest["releaseBase"] + ".tgz"
if archive_name not in manifest["artifacts"] or not (artifacts / archive_name).is_file():
    raise SystemExit("Package the selected TypeScript SDK first")
node = Path(stamp["node"])
environment = os.environ.copy()
environment["PATH"] = str(node.parent) + os.pathsep + environment.get("PATH", "")
subprocess.run(
    [str(node), stamp["npm"], "install", "--package-lock-only", "--ignore-scripts",
     "--no-audit", "--no-fund", "../../output/artifacts/sdk/typescript/" + archive_name],
    cwd=repo / "extensions/vscode", env=environment, check=True,
)
PYSDK

git diff -- extensions/vscode/package.json extensions/vscode/package-lock.json
```

Review the exact local archive path, package version and SHA-512 integrity. Do not bypass the installer's integrity check or substitute a global npm. `extensions/vscode/tooling/runner.py` compares the current SDK archive with the locked integrity before installation.

After reviewing the diff, reinstall and qualify the extension:

```sh
make vscode-tools-install
make vscode-check
make vscode-package
make test-vscode
```

These commands use owned test profiles. They do not publish the VSIX or commit version metadata. Source, SDK package and lock inputs must stay unchanged during qualification.

### Find the source to change

| Path | Responsibility |
| --- | --- |
| `extensions/vscode/src/` | TypeScript client behavior, isolated decisions and native editor adapters. `extension.ts` registers commands and connects these components. |
| `extensions/vscode/media/` | Catalog and A2A webview presentation, local icons and reviewed screenshots. |
| `extensions/vscode/test/` | Isolated unit tests and separate local HTTP/TLS or subprocess tests. |
| `extensions/vscode/Makefile`, `tooling/`, `.vscode/` | Local commands, pinned build tools, bundling/notices, package inspection, deterministic VSIX output and developer tasks. |
| `test/vscode/` | Separate editor-tool installation, owned peers, actual Make controls and installed-editor workflows. |
| `sdk/typescript/` | Shared Catalog API client. Change Catalog API behavior here rather than duplicate its requests in the extension. |
| `extensions/vscode/README.md` | Consumer guide, published in the manual and bundled as offline Help. |

Keep Catalog credentials and SDK calls in the extension host. Webviews send validated messages to that host; they do not receive access tokens. Preserve current-access checks, explicit consent and private-view clearing when identity or connection changes. Target MCP, A2A and package credentials remain separate from Catalog authentication.

### Build and review a change

Add tests at the affected boundary using the [extension test guidance](testing.md#optional-vs-code-extension). Then run these targets separately:

```sh
make vscode-check
make vscode-package
make test-vscode
```

`vscode-check` runs types, formatting, ESLint and isolated units. `vscode-package` builds without tests. `test-vscode` packages the current source again, runs real local functional tests and checks the installed VSIX. Root Go and product-UI checks do not replace these gates. Keep source and package inputs unchanged during qualification. Identical production inputs reproduce the same VSIX bytes; a report applies only while its source and artifact hashes still match. Packaging alone is not a new editor pass.

The self-contained artifact is `output/artifacts/vscode/open-agentic-catalog-<version>.vsix`. Its SDK and runtime dependencies are bundled. Follow the [installation instructions](../../../../../extensions/vscode/README.md#install-the-local-package) for manual use. Automated editor tests create their own profiles; do not point them at a personal profile or retained deployment.

Root `VERSION` owns the extension manifest and lock metadata. SDK or release changes require a fresh TypeScript SDK package, a [reviewed local SDK lock refresh](#refresh-the-local-sdk-lock), explicit tool installation and qualification. Ordinary builds use checked-in SDK bindings; regenerate only when changing the owning API contract. Local `clean-dry-run` and `clean` use the existing cleanup preflight with an extension-only inventory. They preserve SDK caches, reports and developer profiles; root cleanup has broader scope. Both refuse active extension writers. Reinstall removed tools explicitly.

### Package contents and failure evidence

The packager copies a fixed runtime/media/guide/license inventory into staging and invokes pinned VSCE there. Inspection requires the exact ZIP members, regular files, matching package/VSIX identity and byte-identical staged assets. It rejects unexpected ZIP members, path traversal, duplicate entries, symlinks, and differences in metadata or the entrypoint. ZIP ordering, modes and timestamps are normalized. These local hashes identify inputs; they are not signed attestations.

The final VSIX and `manifest.json` live under `output/artifacts/vscode/`. The packager writes the validated artifact, then its manifest as a completion marker. Those two writes are not one atomic transaction: verify matching digests and current inputs with `make -C extensions/vscode verify`. Failures before publication preserve the previous complete pair. An interrupted final write leaves missing or mismatched evidence that verification refuses. `output/vscode/package-attempt.json` records the latest package attempt; an older successful manifest is historical evidence, not proof that a failed command passed.

The [consumer guide](../../../../../extensions/vscode/README.md#verify-and-replace-a-local-build) owns local install, replacement and rollback steps. The package includes a PNG installer icon, keywords and offline changelog. This checkout supplies no verified public repository/support URL; the package does not invent one. Obtain source and support from the maintainer who supplies the VSIX. No target publishes to Marketplace/Open VSX, creates tags or configures CI/CD.

Offline Help resolves only the two maintained layouts: source `README.md` and VSCE's packaged `readme.md`. Both render the same guide, internal section links and two images in real editor checks. Do not add a second editable guide or an arbitrary file fallback.

Extension runtime code does not enter backend or UI images. The UI image packages only the manual's shared guide and screenshots. Run [documentation checks](documentation.mdx#verify-a-documentation-change), build and inspect a local UI image, then check its actual manual route and images in an owned container when those reader inputs change.
