# Optional staged checks

`runner.py` implements optional checks of staged secrets, JSON/YAML, Go formatting, Python, shell, Markdown, Containerfiles, workflows and whitespace. It also installs or removes its own `pre-commit` hook. The maintained [testing guide](../../documentation/src/content/docs/contributing/testing.md#optional-pre-commit-checks) owns setup, use and bypass instructions. Builds and tests never install a hook in your checkout.

The helper reads source from Git's effective index. It reuses the strict JSON/JSON-LD parser and YAML selection rules from a verified staged snapshot of `test/packaging/data_checks.py`. Both that validator and any used `.yamllint.yml` policy must be regular files matching the index.

Installed `yamllint` receives staged YAML on stdin and an explicit snapshot of the staged policy, including for Control OpenAPI. Upstream YAML gets syntax/duplicate-key checks only; raw Helm templates remain a rendered-check obligation. Staged data symlinks and submodules fail.

Deletions are omitted and literal paths preserve unusual filenames. No relevant YAML means no yamllint prerequisite.

`gitleaks.toml` extends pinned Gitleaks defaults; `gitleaksignore` contains only reviewed finding fingerprints. Both scanner policy files must also match the index. Gitleaks reads private copies of those verified staged policies. Later working-file edits cannot change the scan. Tool output and staged blobs are capped at 8 MiB; one shared 30-second deadline covers each helper invocation.

Diagnostics show escaped filenames, rules and lines without source snippets or raw tool messages. Go formatting includes `samples/` and `catalog-ui/`. It skips symlinks, npm packages, generated bindings and immutable upstream inputs.

Git whitespace checks exclude `internal/a2apb/` and `schemas/upstream/`. Go formatting also excludes `samples/upstream/` and npm dependencies. Secret scanning retains these paths.

Ruff, ShellCheck, Hadolint and actionlint check batches of staged files in a private directory. Markdownlint reads each staged document through stdin to preserve literal filenames. Used policies and pins must be regular files matching the index. Tools use verified policy snapshots and exclude inherited configuration variables. Workflow ShellCheck does not follow external sources. No tool writes to the checkout.

Tools prefer explicit `RUFF`, `SHELLCHECK`, `MARKDOWNLINT`, `HADOLINT` and `ACTIONLINT` commands, then owned `.tools/` installations, then `PATH`. Versions must match the existing pins. Only selected file types require their tools; workflow checks also require ShellCheck. Hook installation requires Git, Python 3.12+ and pinned Gitleaks. Go changes require `gofmt`.

Use the [Linux AMD64 setup commands](../../documentation/src/content/docs/contributing/testing.md#install-hook-tools-on-linux-amd64) for the existing pinned tools. The [troubleshooting table](../../documentation/src/content/docs/contributing/testing.md#enable-and-use-the-hook) covers executable overrides, stale versions, policy drift and limits. Setup does not install a hook.

Run isolated selection and diagnostic tests with `python3 -B -m unittest discover -s test/hooks -p 'test_unit_*.py'`. They use no Git, subprocesses or tools. `make test-tooling-unit` includes this selection.

Run deterministic Git/format/helper tests with `go test -tags=functional ./test/hooks -shuffle=on -count=1`; `make verify` includes them. The tests use real Git/gofmt with scanner, yamllint and file-linter doubles, plus targeted fault injection for failure branches. They require installed Git/Python/gofmt and use disposable repositories rather than this checkout's hooks.

Coverage includes partial staging, alternate indexes/worktrees, policy/validator integrity, verified snapshots, staged JSON/YAML and UI Go files, malformed and oversized scanner reports, safe CLI failures, and empty-check/uninstall independence from scanner setup. ShellCheck includes the wrapper in `make check`. The Go coverage percentage does not measure Python helper branches.

File-linter regressions cover missing and stale tools, failed version commands, malformed and inconsistent pins, policy symlinks and later working-file edits. They exercise shared deadlines, real subprocess timeouts, bounded stderr and private-directory cleanup. The staged tree and index remain unchanged on failure.

After installing the pinned file linters, yamllint and Gitleaks from the testing guide, run real-tool qualification explicitly:

```sh
python3 -B -m unittest discover -s test/hooks -p qualify_test.py
```

This fails if yamllint is absent or the scanner is absent or incompatible. It installs the hook only in a disposable repository, commits clean content, rejects staged synthetic credentials despite a clean working copy, checks redaction and exact fingerprint exceptions, accepts clean staged content despite unstaged synthetic credentials, and verifies explicit bypass and uninstall. It also rejects synthetic secrets in generated/vendored paths even with inline allow comments: formatting exclusions do not disable secret scanning. It removes the disposable repository, including its synthetic commits, automatically.

The data qualification checks actual JSON/YAML failures, upstream syntax-only policy, Helm exclusions, partial staging, literal paths, UI formatting and a no-YAML run without yamllint on PATH. It records full-hook timings against the shared budget. It does not contact a Catalog, scanner service or registry.

The file-linter campaign uses all five pinned tools with passing and failing staged inputs. It checks sanitized diagnostics, relative Ruff exclusions and literal Markdown filenames. A workflow control selects actionlint and ShellCheck through `PATH` without explicit command variables. It also verifies that workflow ShellCheck does not read an unstaged external script.

A separate real campaign removes Go and all file linters from `PATH`. Plain-text checks pass. Markdown passes with only its explicit CLI and Node. A Python change then fails because Ruff is absent. No checkout cache is created.

Another real campaign changes each working scanner policy after verification. Gitleaks still rejects the staged synthetic credential and preserves the working edits.
